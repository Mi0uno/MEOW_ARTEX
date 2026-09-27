package server

// PoC 审核 API(契约 POC_INTEL_CONTRACTS.md,Agent D 所有)。
//
// 本文件当前是【骨架】。Agent D 按契约 §D.3 实现:
//
//	registerPocReviewRoutes(mux, s) 已在 server.go 挂载(主会话冻结,签名不得改)。
//	GET  /api/vulndb/review/stats → 200 {pending,approved,rejected} (db.VulnPocReviewStats)
//	POST /api/vulndb/pocs/{id}/review → 重审单条:走与自动审核同一 LLM 判定,
//	      200 {poc} (更新后的 db.VulnPoc);LLM 不可用 503。
//	POST /api/vulndb/pocs/review → body {ids?: number[]}(缺省=全部 pending,cap 200)
//	      → 202 {queued: n}

import "net/http"

// registerPocReviewRoutes 挂载 PoC 审核路由。
func registerPocReviewRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("GET /api/vulndb/review/stats", s.pocReviewStats)
	mux.HandleFunc("POST /api/vulndb/pocs/{id}/review", s.pocReviewOne)
	mux.HandleFunc("POST /api/vulndb/pocs/review", s.pocReviewBatch)
}

func (s *Server) pocReviewStats(w http.ResponseWriter, r *http.Request) {
	writeErr(w, 501, "PoC 审核实现中") // TODO(Agent D): 按契约 §D.3 实现
}

func (s *Server) pocReviewOne(w http.ResponseWriter, r *http.Request) {
	writeErr(w, 501, "PoC 审核实现中") // TODO(Agent D): 按契约 §D.3 实现
}

func (s *Server) pocReviewBatch(w http.ResponseWriter, r *http.Request) {
	writeErr(w, 501, "PoC 审核实现中") // TODO(Agent D): 按契约 §D.3 实现
}
