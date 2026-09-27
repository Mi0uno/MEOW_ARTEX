package server

// 任务侧 CVE 情报 API(契约 POC_INTEL_CONTRACTS.md,Agent A 所有)。
//
//	registerVulnTaskRoutes(mux, s) 已在 server.go 挂载(主会话冻结,签名不得改)。
//	GET /api/tasks/{id}/vuln-brief → 200 {"brief": vulnBriefWire}
//	  vulnBriefWire 与 web/src/lib/types.ts 的 VulnBriefDTO 对齐:
//	  {task_id, enabled, generated_at, fingerprints, entries}。
//	  entries 按 db.MatchVulnEntriesForFingerprints 返回的 []db.VulnEntry 序列化。
//	  任务不存在 404;库错误降级为 enabled=false + 空 entries(不 500)。

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

// vulnBriefWire GET /api/tasks/{id}/vuln-brief 响应体里的 brief 对象
// (wire 字段名与 web/src/lib/types.ts VulnBriefDTO 逐一对齐)。
type vulnBriefWire struct {
	TaskID       string          `json:"task_id"`
	Enabled      bool            `json:"enabled"`
	GeneratedAt  string          `json:"generated_at"`
	Fingerprints []string        `json:"fingerprints"`
	Entries      []*db.VulnEntry `json:"entries"`
}

// registerVulnTaskRoutes 挂载任务↔漏洞库联动的路由。
func registerVulnTaskRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("GET /api/tasks/{id}/vuln-brief", s.taskVulnBrief)
}

// taskVulnBrief GET /api/tasks/{id}/vuln-brief — 任务 CVE 情报简报。
// 数据与 agent 注入的简报同源同缓存(vulnBriefingFor),保证「界面看到的=AI 拿到的」。
func (s *Server) taskVulnBrief(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	num, err := parseTaskID(id)
	if err != nil {
		writeErr(w, 404, "任务不存在")
		return
	}
	// 存在性:运行时句柄优先,回落库行(与 memoryBriefingHandler 同一模式)。
	if _, ok := s.m.Task(id); !ok {
		if pt, gerr := s.m.PG().GetTask(num); gerr != nil || pt == nil {
			writeErr(w, 404, "任务不存在")
			return
		}
	}
	res, err := s.vulnBriefingFor(num)
	if err != nil {
		if errors.Is(err, ErrTaskGone) {
			writeErr(w, 404, "任务不存在")
			return
		}
		// 库错误降级:简报是锦上添花的情报卡,不能让任务详情页 500。
		log.Printf("[vuln-brief] 任务 %s 简报接口降级(库错误): %v", id, err)
		res = &vulnBriefResult{generatedAt: time.Now(), fingerprints: []string{}, entries: []*db.VulnEntry{}}
	}
	writeJSON(w, 200, map[string]any{"brief": vulnBriefWire{
		TaskID:       id,
		Enabled:      res.enabled,
		GeneratedAt:  res.generatedAt.Format(time.RFC3339),
		Fingerprints: res.fingerprints,
		Entries:      res.entries,
	}})
}
