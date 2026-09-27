package server

// nuclei 模板正文回填 API(契约 POC_INTEL_CONTRACTS.md §6;路由已在 server.go
// 冻结挂载,启动 worker 也在 registerNucleiBackfillRoutes 里拉起——server.go
// 冻结,不另加启动行):
//
//	GET    /api/vulndb/nuclei-backfill → 200 PocBackfillStatus
//	POST   /api/vulndb/nuclei-backfill → 202 {status:"started"} / 409 进行中
//	DELETE /api/vulndb/nuclei-backfill → 200 {status:"cancelling"} / 404 无作业

import (
	"io"
	"net/http"
)

// registerNucleiBackfillRoutes 挂载 nuclei 回填路由,并拉起启动自动续跑协程
// (仿 vulndbBackfillWorker:发现未完成光标 30 秒后续跑,见 nucleiBodyWorker)。
func registerNucleiBackfillRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("GET /api/vulndb/nuclei-backfill", s.nucleiBackfillStatus)
	mux.HandleFunc("POST /api/vulndb/nuclei-backfill", s.nucleiBackfillStart)
	mux.HandleFunc("DELETE /api/vulndb/nuclei-backfill", s.nucleiBackfillCancel)
	go s.nucleiBodyWorker(s.ctx)
}

// nucleiBackfillStatus GET — 回填状态(PocBackfillStatus,与 trickest 共形)。
func (s *Server) nucleiBackfillStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.nucleiBodyStatus())
}

// nucleiBackfillStart POST — 启动回填(全局单例;存在未完成光标自动续跑)。
func (s *Server) nucleiBackfillStart(w http.ResponseWriter, r *http.Request) {
	if r.Body != nil {
		// 请求体可为空/{},排空即可(不支持参数;取消后重开是幂等续跑)。
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
	}
	// 必须用服务级 context:请求级 context 在 handler 返回后即被取消。
	if !s.nucleiBodyTryLaunch(s.ctx) {
		writeErr(w, 409, "nuclei 正文回填进行中")
		return
	}
	writeJSON(w, 202, map[string]any{"status": "started"})
}

// nucleiBackfillCancel DELETE — 优雅取消:触发 ctx 取消,当前行处理完后协程
// 退出;光标停在最后处理完的行,可续跑。无作业时 404(契约口径)。
func (s *Server) nucleiBackfillCancel(w http.ResponseWriter, r *http.Request) {
	nucleiBodyMu.Lock()
	running := nucleiBodyRunning
	cancel := nucleiBodyCancel
	nucleiBodyMu.Unlock()

	if !running || cancel == nil {
		writeErr(w, 404, "无进行中的 nuclei 回填作业")
		return
	}
	cancel()
	writeJSON(w, 200, map[string]any{"status": "cancelling"})
}
