package server

// 报告中心批量导出(report center batch export):
//
//	POST /api/reports/export-batch   body {ids:[]int64, format:"md"|"docx", template_id?:int64}
//	                                 响应 = zip 流(逐文件 flush 保活),Content-Disposition 带文件名。
//
// 与单份同步导出(GET /api/reports/{id}/export)同一渲染语义:md 直出(带模板时
// RenderTemplate),docx 走 FillDocx 保真优先、无原始外壳回退 DocxFromMarkdown。
// 不打 LLM——批量导出是本地 CPU 操作,同步直出;AI 逐份导出走 export-ai 作业,不在此列。
//
// zip 内容:
//   - 每份报告一个文件:{三位序号}-{安全标题}.{md|docx}(序号=请求顺序,天然去重)
//   - _导出清单.md:表格(序号/标题/分类/关联/更新时间/导出结果)
//   - _导出失败清单.txt:单份渲染失败的原因(尽力导出,不整体失败;全部成功则无此文件)
//
// 校验:ids 空 400;>200 400;format 非法 400;template_id>0 但模板不存在 404。
// 每份成功导出的报告记一条 exported 事件(detail.batch=true, actor=user)。
// 本文件签名冻结:handler 挂载在 server.go 报告路由区(POST /api/reports/export-batch)。

import (
	"net/http"
)

// exportReportsBatch 批量导出已选报告为 zip。骨架桩:实现时替换本函数体。
func (s *Server) exportReportsBatch(w http.ResponseWriter, r *http.Request) {
	writeErr(w, 501, "批量导出尚未实现")
}
