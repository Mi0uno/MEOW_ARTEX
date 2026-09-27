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
// 逐份 flush(http.NewResponseController),长包期间连接不空闲超时。
// 本文件签名冻结:handler 挂载在 server.go 报告路由区(POST /api/reports/export-batch)。

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/report"
)

// batchExportMaxIDs 单次批量导出上限:防止一次请求拉爆内存/拖死响应。
const batchExportMaxIDs = 200

// batchExportManifestRow 导出清单的一行:一份报告在本批次里的导出结果。
type batchExportManifestRow struct {
	Index     int       // 序号(1 起,请求顺序)
	ID        int64     // 报告 ID
	Title     string    // 报告标题(清单用原文,不安全化)
	Kind      string    // task|finding|custom
	TaskID    int64     // >0 = 关联任务
	FindingID int64     // >0 = 关联发现
	UpdatedAt time.Time // 报告更新时间
	Err       string    // 空 = 成功;非空 = 失败原因
}

// exportReportsBatch 批量导出已选报告为 zip 流式下载。
func (s *Server) exportReportsBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs        []int64 `json:"ids"`
		Format     string  `json:"format"`
		TemplateID int64   `json:"template_id"`
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
		writeErr(w, 400, "单次最多导出 200 份报告")
		return
	}
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format != "md" && format != "docx" {
		writeErr(w, 400, "format 必须是 md 或 docx")
		return
	}
	// 模板校验与单份导出同语义:不存在 404,查询故障 500。
	var tpl *db.ReportTemplate
	if req.TemplateID > 0 {
		t, err := s.m.PG().GetReportTemplate(req.TemplateID)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if t == nil {
			writeErr(w, 404, "template not found")
			return
		}
		tpl = t
	}

	now := time.Now()
	// 响应头必须在任何 body 之前设置,随后立即提交 200 开始 zip 流。
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename*=UTF-8''%s", urlPathEscape(fmt.Sprintf("报告导出-%s.zip", now.Format("20060102-150405")))))
	w.WriteHeader(200)

	rc := http.NewResponseController(w)
	zw := zip.NewWriter(w)
	defer zw.Close()

	rows := make([]batchExportManifestRow, 0, len(req.IDs))
	okCount := 0
	for i, id := range req.IDs {
		row := batchExportManifestRow{Index: i + 1, ID: id}
		rep, err := s.m.PG().GetReport(id)
		if err != nil {
			// DB 级故障:直接中断。注意此时 200/zip 头已提交,无法再回 500,
			// 只能停止处理后续报告并把原因落进清单(比在 zip 流里追加 JSON 干净)。
			log.Printf("[reports] 批量导出读取报告 #%d 失败(DB): %v", id, err)
			row.Err = "读取失败: " + err.Error()
			rows = append(rows, row)
			break
		}
		if rep == nil {
			row.Err = "报告不存在"
			rows = append(rows, row)
			continue
		}
		row.Title = rep.Title
		row.Kind = rep.Kind
		if rep.TaskID != nil {
			row.TaskID = *rep.TaskID
		}
		if rep.FindingID != nil {
			row.FindingID = *rep.FindingID
		}
		row.UpdatedAt = rep.UpdatedAt

		// 渲染语义镜像单份导出 exportReport(report_templates_api.go)。
		data := report.TemplateData{
			Title:   rep.Title,
			Date:    now,
			Summary: rep.Summary,
			Content: rep.Content,
		}
		if rep.TaskID != nil && *rep.TaskID > 0 {
			s.fillTemplateTaskData(&data, *rep.TaskID)
		}
		// rawContent 是报告原始正文:保真 docx 路径的 content 块必须用它,
		// 误用渲染后的整份模板输出会让外壳静态章节出现两遍(单份导出同坑)。
		rawContent := rep.Content
		content := rawContent
		if tpl != nil {
			content = report.RenderTemplate(tpl.Content, data)
		}

		ext := "md"
		payload := []byte(content)
		if format == "docx" {
			ext = "docx"
			payload = nil
			// 格式保真优先:模板带原始 docx 外壳时在原包上只替换变量文本;
			// 无外壳回退内置生成器。失败记入失败清单继续,不中断整个 zip。
			if orig, ok := s.origDocxBytes(tpl); ok {
				d, err := report.FillDocx(orig, report.DocxData{
					Inline: map[string]string{
						"title":          data.Title,
						"date":           now.Format("2006-01-02"),
						"summary":        data.Summary,
						"assets_summary": data.AssetsSummary,
					},
					Blocks: map[string]report.DocxBlock{
						// content 用原始正文而非 data.Content(渲染后的整份模板输出)。
						"content":         {Lines: strings.Split(strings.ReplaceAll(rawContent, "\r\n", "\n"), "\n")},
						"findings_detail": {Lines: strings.Split(strings.ReplaceAll(data.FindingsDetail, "\r\n", "\n"), "\n")},
						"findings_table":  report.DocxBlockFromMarkdown(data.FindingsTable),
					},
				})
				if err != nil {
					row.Err = "docx 保真填充失败: " + err.Error()
					rows = append(rows, row)
					continue
				}
				payload = d
			}
			if payload == nil {
				d, err := report.DocxFromMarkdown(content)
				if err != nil {
					row.Err = "docx 生成失败: " + err.Error()
					rows = append(rows, row)
					continue
				}
				payload = d
			}
		}

		h := &zip.FileHeader{Name: batchExportEntryName(i+1, rep.Title, ext), Modified: rep.UpdatedAt}
		fw, err := zw.CreateHeader(h)
		if err != nil {
			log.Printf("[reports] 批量导出写 zip 条目失败(#%d): %v", id, err)
			break
		}
		if _, err := fw.Write(payload); err != nil {
			log.Printf("[reports] 批量导出写 zip 内容失败(#%d): %v", id, err)
			break
		}
		_ = rc.Flush() // 每写完一个 entry flush 一次,长包不空闲超时
		okCount++
		rows = append(rows, row)
		s.logReportEvent(rep.ID, "exported", "user", map[string]any{
			"format": format, "template_id": req.TemplateID, "batch": true, "batch_size": len(req.IDs),
		})
	}

	// 结尾:导出清单(必有) + 失败清单(仅在有失败时)。
	if err := writeZipBytesEntry(zw, "_导出清单.md", []byte(batchExportManifestMD(rows))); err != nil {
		log.Printf("[reports] 批量导出写清单失败: %v", err)
	} else {
		_ = rc.Flush()
	}
	var failLines []string
	for _, row := range rows {
		if row.Err != "" {
			failLines = append(failLines, fmt.Sprintf("#%d %s: %s", row.ID, strings.TrimSpace(row.Title), row.Err))
		}
	}
	if len(failLines) > 0 {
		if err := writeZipBytesEntry(zw, "_导出失败清单.txt", []byte(strings.Join(failLines, "\n")+"\n")); err != nil {
			log.Printf("[reports] 批量导出写失败清单失败: %v", err)
		} else {
			_ = rc.Flush()
		}
	}
	log.Printf("[reports] 批量导出 %d 份(成功 %d / 失败 %d, format=%s)", len(req.IDs), okCount, len(rows)-okCount, format)
}

// writeZipBytesEntry 往 zip 写一个内存条目(清单/失败清单用)。
func writeZipBytesEntry(zw *zip.Writer, name string, data []byte) error {
	fw, err := zw.CreateHeader(&zip.FileHeader{Name: name, Modified: time.Now()})
	if err != nil {
		return err
	}
	_, err = fw.Write(data)
	return err
}

// batchExportEntryName 生成 zip 条目名:{三位序号}-{安全标题}.{扩展名}。
// 标题先按 rune 截断到 60 字符(防超长文件名),再复用 sanitizeReportFilename
// 去文件系统非法字符;空标题回退 report。
func batchExportEntryName(idx int, title, ext string) string {
	runes := []rune(strings.TrimSpace(title))
	if len(runes) > 60 {
		runes = runes[:60]
	}
	name := sanitizeReportFilename(string(runes))
	if name == "" {
		name = "report"
	}
	return fmt.Sprintf("%03d-%s.%s", idx, name, ext)
}

// batchExportKindLabel 清单分类列的中文映射(与前端 KIND_META 同款)。
func batchExportKindLabel(kind string) string {
	switch kind {
	case "task":
		return "任务报告"
	case "finding":
		return "漏洞报告"
	case "custom":
		return "自定义"
	default:
		return kind
	}
}

// batchExportManifestMD 生成 _导出清单.md 内容:markdown 表格,
// 列 = 序号/标题/分类/关联/更新时间/结果(成功 或 失败:<原因>)。纯函数便于测试。
func batchExportManifestMD(rows []batchExportManifestRow) string {
	var sb strings.Builder
	sb.WriteString("# 报告导出清单\n\n")
	sb.WriteString("| 序号 | 标题 | 分类 | 关联 | 更新时间 | 结果 |\n")
	sb.WriteString("| --- | --- | --- | --- | --- | --- |\n")
	for _, row := range rows {
		ref := "—"
		switch {
		case row.TaskID > 0:
			ref = fmt.Sprintf("任务 #%d", row.TaskID)
		case row.FindingID > 0:
			ref = fmt.Sprintf("发现 #%d", row.FindingID)
		}
		result := "成功"
		if row.Err != "" {
			result = "失败:" + row.Err
		}
		sb.WriteString(fmt.Sprintf("| %d | %s | %s | %s | %s | %s |\n",
			row.Index, mdCellText(row.Title), batchExportKindLabel(row.Kind), ref,
			row.UpdatedAt.Format("2006-01-02 15:04"), result))
	}
	return sb.String()
}

// mdCellText markdown 表格单元格转义:竖线会截断单元格,换行会断表。
func mdCellText(s string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ", "\r", "").Replace(strings.TrimSpace(s))
}
