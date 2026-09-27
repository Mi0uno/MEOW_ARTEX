package server

// 任务侧 CVE 情报 API(契约 POC_INTEL_CONTRACTS.md,Agent A 所有)。
//
// 本文件当前是【骨架】。Agent A 按契约 §A.3 实现:
//
//	registerVulnTaskRoutes(mux, s) 已在 server.go 挂载(主会话冻结,签名不得改)。
//	GET /api/tasks/{id}/vuln-brief → 200 {"brief": VulnBriefDTO}
//	  VulnBriefDTO = {task_id, enabled, generated_at, fingerprints, entries}
//	  entries 按 db.MatchVulnEntriesForFingerprints 返回的 []db.VulnEntry 序列化。
//	  任务不存在 404;LLM/库错误降级为 enabled=false + 空 entries(不 500)。

import "net/http"

// registerVulnTaskRoutes 挂载任务↔漏洞库联动的路由。
func registerVulnTaskRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("GET /api/tasks/{id}/vuln-brief", s.taskVulnBrief)
}

// taskVulnBrief GET /api/tasks/{id}/vuln-brief — 任务 CVE 情报简报(骨架)。
func (s *Server) taskVulnBrief(w http.ResponseWriter, r *http.Request) {
	writeErr(w, 501, "CVE 情报简报实现中") // TODO(Agent A): 按契约 §A.3 实现
}
