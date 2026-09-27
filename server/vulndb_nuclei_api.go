package server

// nuclei 模板正文回填 API(契约 POC_INTEL_CONTRACTS.md,Agent C 所有)。
//
// 本文件当前是【骨架】。Agent C 按契约 §C.3 实现:
//
//	registerNucleiBackfillRoutes(mux, s) 已在 server.go 挂载(主会话冻结,签名不得改)。
//	GET    /api/vulndb/nuclei-backfill → 200 PocBackfillStatus
//	POST   /api/vulndb/nuclei-backfill → 202 {status:"started"} / 409 进行中
//	DELETE /api/vulndb/nuclei-backfill → 200 {status:"cancelling"} / 404 无作业

import "net/http"

// registerNucleiBackfillRoutes 挂载 nuclei 回填路由。
func registerNucleiBackfillRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("GET /api/vulndb/nuclei-backfill", s.nucleiBackfillStatus)
	mux.HandleFunc("POST /api/vulndb/nuclei-backfill", s.nucleiBackfillStart)
	mux.HandleFunc("DELETE /api/vulndb/nuclei-backfill", s.nucleiBackfillCancel)
}

func (s *Server) nucleiBackfillStatus(w http.ResponseWriter, r *http.Request) {
	writeErr(w, 501, "nuclei 回填实现中") // TODO(Agent C): 按契约 §C.3 实现
}

func (s *Server) nucleiBackfillStart(w http.ResponseWriter, r *http.Request) {
	writeErr(w, 501, "nuclei 回填实现中") // TODO(Agent C): 按契约 §C.3 实现
}

func (s *Server) nucleiBackfillCancel(w http.ResponseWriter, r *http.Request) {
	writeErr(w, 501, "nuclei 回填实现中") // TODO(Agent C): 按契约 §C.3 实现
}
