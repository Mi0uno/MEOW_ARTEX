package server

// trickest 历史回填 API(契约 POC_INTEL_CONTRACTS.md §5,Agent B 所有)。
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

// trickestBackfillStatus GET — 回填作业状态(内存进度;空闲时 done 取自光标)。
func (s *Server) trickestBackfillStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.trickestBackfillSnapshot())
}

// trickestBackfillStart POST — 启动/续跑历史回填(全局单例)。
// 存在未完成光标 → 从 last_id 续跑(取消后重开是常见操作,CreateVulnPoc 幂等,
// 续跑不产生脏数据);无光标或已完成 → 从头重扫。
func (s *Server) trickestBackfillStart(w http.ResponseWriter, r *http.Request) {
	var resume *trickestBackfillCursor
	if cur, err := trickestLoadCursor(s.m.PG()); err != nil {
		// 光标读不出来按无光标处理(从头扫),不阻塞启动。
		writeErr(w, 500, "光标读取失败: "+err.Error())
		return
	} else if cur != nil && !cur.Done {
		resume = cur
	}
	// 注意必须用服务级 context:请求级 context 在 handler 返回后即被取消。
	if !s.trickestBackfillTryLaunch(s.ctx, resume) {
		writeErr(w, 409, "trickest 回填进行中")
		return
	}
	writeJSON(w, 202, map[string]any{"status": "started"})
}

// trickestBackfillCancel DELETE — 优雅取消:取消 context,当前条目处理完即停;
// 光标停在最后一个处理完的条目,可续跑。
func (s *Server) trickestBackfillCancel(w http.ResponseWriter, r *http.Request) {
	trickestBackfillMu.Lock()
	running := trickestBackfillRunning
	cancel := trickestBackfillCancel
	trickestBackfillMu.Unlock()

	if !running || cancel == nil {
		writeErr(w, 404, "无进行中的 trickest 回填作业")
		return
	}
	cancel()
	writeJSON(w, 200, map[string]any{"status": "cancelling"})
}
