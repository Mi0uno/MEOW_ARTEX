package server

// PoC 审核 API(契约 POC_INTEL_CONTRACTS.md §7.3,Agent D 所有)。
//
//	registerPocReviewRoutes(mux, s) 已在 server.go 挂载(主会话冻结,签名不得改)。
//	GET  /api/vulndb/review/stats → 200 {pending,approved,rejected} (db.VulnPocReviewStats)
//	POST /api/vulndb/pocs/{id}/review → 重审单条:走与自动审核同一 LLM 判定,
//	      200 {poc} (更新后的 db.VulnPoc,含判定未落库时保持 pending 的原样);
//	      LLM 不可用 503「未配置可用的 LLM 模型」;PoC 不存在 404。
//	POST /api/vulndb/pocs/review → body {ids?: number[]}(缺省/空=全部 pending,cap 200)
//	      → 202 {queued: n},后台逐条审核(sem 限流)。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/Autumn-27/norma/llm"
)

// registerPocReviewRoutes 挂载 PoC 审核路由。
func registerPocReviewRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("GET /api/vulndb/review/stats", s.pocReviewStats)
	mux.HandleFunc("POST /api/vulndb/pocs/{id}/review", s.pocReviewOne)
	mux.HandleFunc("POST /api/vulndb/pocs/review", s.pocReviewBatch)
}

// pocReviewStats GET /api/vulndb/review/stats — 审核三态全库计数(工作台徽标用)。
func (s *Server) pocReviewStats(w http.ResponseWriter, r *http.Request) {
	pending, approved, rejected, err := s.m.PG().VulnPocReviewStats()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]int{"pending": pending, "approved": approved, "rejected": rejected})
}

// pocReviewOne POST /api/vulndb/pocs/{id}/review — 同步重审单条。
func (s *Server) pocReviewOne(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad poc id")
		return
	}
	poc, err := s.m.PG().GetVulnPoc(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if poc == nil {
		writeErr(w, 404, "poc not found")
		return
	}
	if _, err := s.reviewOnePoc(r.Context(), poc); err != nil {
		if errors.Is(err, errPocReviewNoLLM) {
			writeErr(w, 503, err.Error())
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	updated, err := s.m.PG().GetVulnPoc(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if updated == nil {
		writeErr(w, 404, "poc not found")
		return
	}
	writeJSON(w, 200, map[string]any{"poc": updated})
}

// pocReviewBatch POST /api/vulndb/pocs/review — 批量送审(ids 缺省/空=全部 pending,cap 200)。
func (s *Server) pocReviewBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	ids := req.IDs
	if len(ids) == 0 {
		pocs, err := s.listPendingPocsForReview(r.Context(), pocReviewManualBatchCap, time.Time{})
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		for _, p := range pocs {
			ids = append(ids, p.ID)
		}
	} else {
		ids = dedupePocReviewIDs(ids)
	}
	if len(ids) > pocReviewManualBatchCap {
		ids = ids[:pocReviewManualBatchCap]
	}
	if len(ids) == 0 {
		writeJSON(w, 202, map[string]int{"queued": 0})
		return
	}
	// 受理前先确认 LLM 可用:避免 202 之后整批静默空转(诚实降级,与单条 503 同判据)。
	prov, err := s.pocReviewProvider()
	if err != nil {
		writeErr(w, 503, err.Error())
		return
	}
	go s.runPocReviewBatch(prov, ids)
	writeJSON(w, 202, map[string]int{"queued": len(ids)})
}

// runPocReviewBatch 后台逐条审核选定的 PoC(pocReviewSem 限流);LLM 判不了的单条
// 记日志保持 pending。执行中已被人工改判的行跳过,不覆盖人工结论。
func (s *Server) runPocReviewBatch(prov llm.Provider, ids []int64) {
	ctx, cancel := context.WithTimeout(s.ctx, pocReviewBatchBudget)
	defer cancel()
	for _, id := range ids {
		poc, err := s.m.PG().GetVulnPoc(id)
		if err != nil {
			log.Printf("[poc-review] 批量: PoC #%d 读取失败,跳过: %v", id, err)
			continue
		}
		if poc == nil || poc.ReviewStatus != "pending" {
			continue
		}
		select {
		case pocReviewSem <- struct{}{}:
		case <-ctx.Done():
			log.Printf("[poc-review] 批量审核预算用尽,剩余条目留待下轮")
			return
		}
		func() {
			defer func() { <-pocReviewSem }()
			if _, err := s.reviewPocWithProvider(ctx, prov, poc); err != nil {
				log.Printf("[poc-review] 批量: PoC #%d 审核失败(保持 pending): %v", id, err)
			}
		}()
	}
}

// dedupePocReviewIDs 去重并过滤非正值,保持原有顺序。
func dedupePocReviewIDs(ids []int64) []int64 {
	seen := make(map[int64]bool, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
