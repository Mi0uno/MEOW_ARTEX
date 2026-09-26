package server

// NVD 历史回填的 HTTP API(路由已由 server.go 冻结注册,见 VULNDB_CONTRACTS_V2.md §8):
//
//	GET    /api/vulndb/backfill  → 状态(内存 running 态 + settings 光标)
//	POST   /api/vulndb/backfill  → 启动(body {"start_year":N},可省略)
//	DELETE /api/vulndb/backfill  → 优雅取消(光标保留,可续跑)

import (
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// vulnBackfillStatus GET /api/vulndb/backfill — 回填状态。
func (s *Server) vulnBackfillStatus(w http.ResponseWriter, r *http.Request) {
	backfillMu.Lock()
	running := backfillRunning
	lastErr := backfillLastErr
	backfillMu.Unlock()

	phase, startYear, windowEnd, done := "", 0, "", false
	if cur, err := backfillLoadCursor(s.m.PG()); err != nil {
		// 光标读取失败不阻塞状态展示,按无光标返回。
		lastErr = "光标读取失败: " + err.Error()
	} else if cur != nil {
		phase, startYear, windowEnd, done = cur.Phase, cur.StartYear, cur.WindowEnd, cur.Done
	}
	writeJSON(w, 200, map[string]any{
		"running":    running,
		"phase":      phase,
		"start_year": startYear,
		"window_end": windowEnd,
		"done":       done,
		"error":      lastErr,
	})
}

// vulnBackfillStart POST /api/vulndb/backfill — 启动历史回填(全局单例)。
// body {"start_year":2016} 可省略,缺省用 vulnBackfillWindowStart;夹取 [2011, 当前年]。
func (s *Server) vulnBackfillStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		StartYear int `json:"start_year"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil && err != io.EOF {
			writeErr(w, 400, "bad json: "+err.Error())
			return
		}
	}
	startYear := body.StartYear
	if startYear <= 0 {
		startYear = vulnBackfillWindowStart
	}
	if startYear < backfillStartYearMin {
		startYear = backfillStartYearMin
	}
	if y := time.Now().Year(); startYear > y {
		startYear = y
	}

	// 存在未完成且起始年相同的光标 → 续跑而非重头再来(取消后重开是常见操作,
	// UpsertCVE/CreateVulnPoc 幂等,续跑不会产生脏数据)。
	var resume *backfillCursor
	if cur, err := backfillLoadCursor(s.m.PG()); err == nil && cur != nil && !cur.Done && cur.StartYear == startYear {
		resume = cur
	}

	// 注意必须用服务级 context:请求级 context 在 handler 返回后即被取消。
	if !s.backfillTryLaunch(s.ctx, startYear, resume) {
		writeErr(w, 409, "回填进行中")
		return
	}
	writeJSON(w, 202, map[string]any{"status": "started"})
}

// vulnBackfillCancel DELETE /api/vulndb/backfill — 优雅取消:触发 context 取消,
// 当前 HTTP 请求/窗口处理中止后协程退出;光标保留在最后完成的进度,可续跑。
func (s *Server) vulnBackfillCancel(w http.ResponseWriter, r *http.Request) {
	backfillMu.Lock()
	running := backfillRunning
	cancel := backfillCancel
	backfillMu.Unlock()

	if !running || cancel == nil {
		writeJSON(w, 200, map[string]any{"status": "idle"})
		return
	}
	cancel()
	writeJSON(w, 200, map[string]any{"status": "cancelled"})
}
