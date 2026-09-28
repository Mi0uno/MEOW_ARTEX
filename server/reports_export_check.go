package server

// 批量导出前的「已导出过」检查(导出提醒):
//
//	POST /api/reports/export-check   body {ids:[]int64} → 200 {exported:[]int64}
//
// 前端在打开批量导出对话框前调用:命中(有此前导出痕迹)则弹窗提醒,由用户选择
// 「仍导出全部」或「只导出没导出过的」。「导出过」判定口径见 db.FilterExportedReportIDs
// (report_events 的 exported 事件 ∪ report_jobs 的 done 导出作业,跨 v1 同步/v2 后台作业)。
//
// 本文件刻意独立于 reports_batch_export.go 与 server.go 的 registerReportExportRoutes
// (两者都是报告导出 v2 契约的属地),路由挂在 server.go 报告中心区,避免所有权交叉。

import (
	"encoding/json"
	"io"
	"net/http"
)

// checkReportsExported 查询 ids 中此前已导出过的报告 id(升序,永不为 null)。
// 校验与批量导出对齐:ids 空 400;上限同 batchExportMaxIDs。
func (s *Server) checkReportsExported(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil && err != io.EOF {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if len(req.IDs) == 0 {
		writeErr(w, 400, "请先勾选要导出的报告")
		return
	}
	if len(req.IDs) > batchExportMaxIDs {
		writeErr(w, 400, "单次最多检查 200 份报告")
		return
	}
	exported, err := s.m.PG().FilterExportedReportIDs(req.IDs)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if exported == nil {
		exported = []int64{}
	}
	writeJSON(w, 200, map[string]any{"exported": exported})
}
