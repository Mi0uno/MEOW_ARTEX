package server

// trickest 历史回填 API(契约 POC_INTEL_CONTRACTS.md,Agent B 所有)。
//
// 本文件当前是【骨架】。Agent B 按契约 §B.3 实现:
//
//	registerTrickestBackfillRoutes(mux, s) 已在 server.go 挂载(主会话冻结,签名不得改)。
//	GET    /api/vulndb/trickest-backfill → 200 PocBackfillStatus(与 nuclei 回填共形)
//	POST   /api/vulndb/trickest-backfill → 202 {status:"started"} / 409 进行中
//	DELETE /api/vulndb/trickest-backfill → 200 {status:"cancelling"} / 404 无作业
//	响应体结构 = PocBackfillStatus{running,done,processed,matched,error,started_at,finished_at},
//	前端用同一组件渲染两个回填卡(nuclei/trickest)。

import "net/http"

// registerTrickestBackfillRoutes 挂载 trickest 回填路由。
func registerTrickestBackfillRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("GET /api/vulndb/trickest-backfill", s.trickestBackfillStatus)
	mux.HandleFunc("POST /api/vulndb/trickest-backfill", s.trickestBackfillStart)
	mux.HandleFunc("DELETE /api/vulndb/trickest-backfill", s.trickestBackfillCancel)
}

func (s *Server) trickestBackfillStatus(w http.ResponseWriter, r *http.Request) {
	writeErr(w, 501, "trickest 回填实现中") // TODO(Agent B): 按契约 §B.3 实现
}

func (s *Server) trickestBackfillStart(w http.ResponseWriter, r *http.Request) {
	writeErr(w, 501, "trickest 回填实现中") // TODO(Agent B): 按契约 §B.3 实现
}

func (s *Server) trickestBackfillCancel(w http.ResponseWriter, r *http.Request) {
	writeErr(w, 501, "trickest 回填实现中") // TODO(Agent B): 按契约 §B.3 实现
}
