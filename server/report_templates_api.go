package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/report"
	"github.com/Autumn-27/norma/llm"
)

// ---- 报告模板 API:上传 md/docx 模板 → 按模板导出报告 ----

// templateEvent helper:给模板记一条事件(失败仅记日志,不影响主流程)。
func (s *Server) templateEvent(templateID int64, kind, actor string, detail map[string]any) {
	tid := templateID
	dj, _ := json.Marshal(detail)
	if err := s.m.PG().AddReportEvent(nil, &tid, nil, kind, actor, dj); err != nil {
		log.Printf("[report-template] 事件记录失败(%s #%d): %v", kind, templateID, err)
	}
}

// listReportTemplates GET /api/report-templates?category=&q=&limit=&offset= —
// 分类筛选 + 名称关键字检索 + 分页。
func (s *Server) listReportTemplates(w http.ResponseWriter, r *http.Request) {
	category := strings.TrimSpace(r.URL.Query().Get("category"))
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	ts, total, err := s.m.PG().ListReportTemplates(category, q, limit, offset)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if ts == nil {
		ts = []*db.ReportTemplate{}
	}
	writeJSON(w, 200, map[string]any{"templates": ts, "total": total})
}

// getReportTemplate GET /api/report-templates/{id} — 模板全文(编辑用)。
func (s *Server) getReportTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad template id")
		return
	}
	t, err := s.m.PG().GetReportTemplate(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if t == nil {
		writeErr(w, 404, "template not found")
		return
	}
	writeJSON(w, 200, map[string]any{"template": t})
}

// uploadReportTemplate POST /api/report-templates — multipart 上传模板。
// file: .md/.markdown/.txt 直接入库;.docx 程序化解析为 markdown(标题层级/段落/表格);
// .doc(老二进制格式)不支持,提示另存为 .docx。name 缺省取文件名去扩展名。
func (s *Server) uploadReportTemplate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		// 排空残余请求体再返回,避免大文件时连接被提前重置、浏览器收不到错误响应。
		_, _ = io.Copy(io.Discard, r.Body)
		writeErr(w, 400, "invalid multipart: "+err.Error())
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "缺少 file 字段")
		return
	}
	defer file.Close()

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = strings.TrimSuffix(hdr.Filename, filepath.Ext(hdr.Filename))
	}
	if name == "" {
		name = "未命名模板"
	}
	category := strings.TrimSpace(r.FormValue("category"))

	ext := strings.ToLower(filepath.Ext(hdr.Filename))
	var format, content, sourceNote string
	structure := report.TemplateStructure{}
	switch ext {
	case ".md", ".markdown", ".txt":
		b, err := io.ReadAll(file)
		if err != nil {
			writeErr(w, 400, "读取文件失败: "+err.Error())
			return
		}
		format, content = "markdown", string(b)
		structure = report.AnalyzeMarkdown(content)
		sourceNote = fmt.Sprintf("markdown 模板:章节 %d · 段落 %d", len(structure.Headings), structure.Paragraphs)
	case ".docx":
		markdown, st, err := report.ParseDocxReader(file)
		if err != nil {
			writeErr(w, 400, "docx 解析失败: "+err.Error())
			return
		}
		format, content = "docx", markdown
		structure = st
		sourceNote = fmt.Sprintf("docx 解析:章节 %d · 段落 %d · 表格 %d · 约 %d 字", len(st.Headings), st.Paragraphs, st.Tables, st.Words)
	case ".doc":
		writeErr(w, 400, "老版 .doc 二进制格式暂不支持,请用 Word/WPS 另存为 .docx 后重新上传")
		return
	default:
		writeErr(w, 400, "仅支持 .docx / .md / .markdown / .txt 模板")
		return
	}

	st, err := json.Marshal(structure)
	if err != nil {
		st = []byte("{}")
	}
	t, err := s.m.PG().CreateReportTemplate(name, category, format, content, st, sourceNote)
	if err != nil {
		log.Printf("[report-template] 上传入库失败(%s/%s): %v", name, hdr.Filename, err)
		writeErr(w, 500, err.Error())
		return
	}
	// docx 原始包落盘(格式保真导出以它为外壳,只替换变量文本)。
	// DB 只存文件名(相对 dataDir/report-templates/),便于跨目录迁移。
	if format == "docx" {
		dir := filepath.Join(s.m.dir, "report-templates")
		if mkErr := os.MkdirAll(dir, 0o755); mkErr == nil {
			fname := fmt.Sprintf("%d.docx", t.ID)
			if _, err := file.Seek(0, io.SeekStart); err == nil {
				orig, err := io.ReadAll(file)
				if err == nil {
					if saveErr := os.WriteFile(filepath.Join(dir, fname), orig, 0o644); saveErr != nil {
						log.Printf("[report-template] 原始 docx 落盘失败: %v", saveErr)
					} else if err := s.m.PG().SetReportTemplateOrigPath(t.ID, fname); err != nil {
						log.Printf("[report-template] 记录原始 docx 路径失败: %v", err)
					}
				}
			}
		}
	}
	tid := t.ID
	s.templateEvent(tid, "uploaded", "user", map[string]any{
		"format": format, "filename": hdr.Filename, "structure": sourceNote,
	})
	// 上传即自动 AI 识别(异步任务):未配置 LLM 时把失败原因落到模板行,前端可见可重试。
	go func() {
		if _, _, err := s.startTemplateAnalyzeTask(tid, 0); err != nil {
			log.Printf("[report-template] 自动 AI 识别未启动(模板 %d): %v", tid, err)
			if err2 := s.m.PG().SetReportTemplateAnalyzeFailed(tid, err.Error()); err2 != nil {
				log.Printf("[report-template] 模板 %d 落识别失败原因失败: %v", tid, err2)
			}
			s.templateEvent(tid, "ai_analyze_failed", "agent", map[string]any{"error": err.Error()})
		}
	}()
	writeJSON(w, 201, map[string]any{"template": t})
}

// updateReportTemplate PUT /api/report-templates/{id} — 修改名称、分类与模板正文。
func (s *Server) updateReportTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad template id")
		return
	}
	existing, err := s.m.PG().GetReportTemplate(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var req struct {
		Name     string `json:"name"`
		Category string `json:"category"`
		Content  string `json:"content"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if existing == nil {
		writeErr(w, 404, "template not found")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = existing.Name
	}
	if err := s.m.PG().UpdateReportTemplate(id, name, strings.TrimSpace(req.Category), req.Content); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	tid := id
	s.templateEvent(tid, "updated", "user", map[string]any{
		"words": len([]rune(req.Content)),
	})
	writeJSON(w, 200, map[string]any{"ok": true})
}

// deleteReportTemplate DELETE /api/report-templates/{id}。
func (s *Server) deleteReportTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad template id")
		return
	}
	deleted, err := s.m.PG().DeleteReportTemplate(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !deleted {
		writeErr(w, 404, "template not found")
		return
	}
	tid := id
	s.templateEvent(tid, "deleted", "user", nil)
	writeJSON(w, 200, map[string]any{"deleted": true})
}

// analyzeReportTemplate POST /api/report-templates/{id}/analyze —
// AI 读取模板(解析出的结构与内容),生成「数据占位版」模板草稿。
// 异步任务化(202 立即返回):创建 stage='report' 识别任务 + template_engineer 监看
// 会话并后台启动;草稿完成后写入模板行(suggested_content),由用户在编辑器确认采用。
// 前端轮询 GET /api/report-templates/{id} 的 analyze_status 获取进度。
// body: {profile_id?: number} — 分析用的 LLM 配置;0/缺省 = 当前激活配置。
func (s *Server) analyzeReportTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad template id")
		return
	}
	var req struct {
		ProfileID int64 `json:"profile_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil && err != io.EOF {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	taskID, convID, err := s.startTemplateAnalyzeTask(id, req.ProfileID)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{
		"status": "running", "task_id": taskID, "conv_id": convID,
	})
}

// analyzeCompletion 单次补全:与 streamCollectText 同构,但模板生成长度预算更大。
func analyzeCompletion(ctx context.Context, prov llm.Provider, system, user string) (string, error) {
	temp := 0.2
	req := llm.CompletionRequest{
		System:      []string{system},
		Messages:    []llm.Message{llm.UserText(user)},
		MaxTokens:   8192,
		Temperature: &temp,
		Thinking:    "disabled",
	}
	var sb strings.Builder
	for ev, err := range prov.Stream(ctx, req) {
		if err != nil {
			return "", err
		}
		if ev.Type == llm.SETextDelta {
			sb.WriteString(ev.Text)
		}
	}
	return sb.String(), nil
}

// exportReport GET /api/reports/{id}/export?template_id=&format=md|docx —
// 按模板渲染并下载。template_id 缺省 = 报告原文;format 缺省 md。
func (s *Server) exportReport(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad report id")
		return
	}
	rep, err := s.m.PG().GetReport(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if rep == nil {
		writeErr(w, 404, "report not found")
		return
	}

	data := report.TemplateData{
		Title:   rep.Title,
		Date:    time.Now(),
		Summary: rep.Summary,
		Content: rep.Content,
	}
	if rep.TaskID != nil && *rep.TaskID > 0 {
		s.fillTemplateTaskData(&data, *rep.TaskID)
	}

	format := strings.ToLower(r.URL.Query().Get("format"))
	tplID, _ := strconv.ParseInt(r.URL.Query().Get("template_id"), 10, 64)
	exportName := strings.TrimSpace(rep.Title)
	var tpl *db.ReportTemplate
	if tplID > 0 {
		t, err := s.m.PG().GetReportTemplate(tplID)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if t == nil {
			writeErr(w, 404, "template not found")
			return
		}
		tpl = t
		data.Content = report.RenderTemplate(tpl.Content, data)
		exportName = strings.TrimSpace(tpl.Name)
	}
	if exportName == "" {
		exportName = fmt.Sprintf("report-%d", rep.ID)
	}
	s.logReportEvent(rep.ID, "exported", "user", map[string]any{
		"format": format, "template_id": tplID, "template_name": exportName,
	})
	exportName = strings.NewReplacer("\\", "_", "/", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_").Replace(exportName)

	switch format {
	case "docx":
		// 格式保真优先:模板带原始 docx 外壳时,在原包上只替换变量文本,
		// 颜色/字体/表格样式/页眉页脚/Logo 原样保留;否则回退内置生成器。
		var docx []byte
		if orig, ok := s.origDocxBytes(tpl); ok {
			now := time.Now()
			d, err := report.FillDocx(orig, report.DocxData{
				Inline: map[string]string{
					"title":          data.Title,
					"date":           now.Format("2006-01-02"),
					"summary":        data.Summary,
					"assets_summary": data.AssetsSummary,
				},
				Blocks: map[string]report.DocxBlock{
					"content":         {Lines: strings.Split(strings.ReplaceAll(data.Content, "\r\n", "\n"), "\n")},
					"findings_detail": {Lines: strings.Split(strings.ReplaceAll(data.FindingsDetail, "\r\n", "\n"), "\n")},
				},
			})
			if err != nil {
				writeErr(w, 500, "docx 保真填充失败: "+err.Error())
				return
			}
			docx = d
		}
		if docx == nil {
			var err error
			docx, err = report.DocxFromMarkdown(data.Content)
			if err != nil {
				writeErr(w, 500, "docx 生成失败: "+err.Error())
				return
			}
		}
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", urlPathEscape(exportName+".docx")))
		_, _ = w.Write(docx)
	default:
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", urlPathEscape(exportName+".md")))
		_, _ = w.Write([]byte(data.Content))
	}
}

// fillTemplateTaskData 汇总任务关联的漏洞与资产统计,填充模板变量。
// 同时返回漏洞记录列表(保真 docx 导出的结构化数据源)。
func (s *Server) fillTemplateTaskData(data *report.TemplateData, taskID int64) []*db.DBFinding {
	fs, err := s.m.PG().ListFindingsForExport(db.FindingFilter{TaskID: fmt.Sprint(taskID)}, nil)
	if err != nil {
		fs = nil
	}
	if fs != nil {
		rows := make([][4]string, 0, len(fs))
		details := make([]string, 0, len(fs))
		now := time.Now()
		for _, f := range fs {
			rows = append(rows, [4]string{
				strings.ToUpper(f.Severity), f.Name, f.VulnClass, f.Status,
			})
			details = append(details, report.SingleFindingMarkdown(f, now))
		}
		data.FindingsTable = report.FindingsTable(rows)
		data.FindingsDetail = report.FindingsDetailMd(details)
	}
	if as := s.assetStore(); as != nil {
		if counts, err := as.CountsByTypeForTask(taskID); err == nil {
			types := make([]string, 0, len(counts))
			for typ, n := range counts {
				types = append(types, fmt.Sprintf("%s %d", typ, n))
			}
			data.AssetsSummary = strings.Join(types, " · ")
		}
	}
	return fs
}

func urlPathEscape(s string) string {
	return url.PathEscape(s)
}

// origDocxBytes 读取模板的原始 docx 外壳(格式保真导出用)。
// OrigPath 存的是 dataDir/report-templates/ 下的文件名(或历史遗留的绝对路径,
// 同样兼容)。读取前做规范化防御:禁止 .. 跳转,且必须落在 report-templates
// 目录内(绝对路径比较),防路径穿越。
func (s *Server) origDocxBytes(tpl *db.ReportTemplate) ([]byte, bool) {
	if tpl == nil || tpl.OrigPath == "" {
		return nil, false
	}
	absRoot, err := filepath.Abs(filepath.Join(s.m.dir, "report-templates"))
	if err != nil {
		return nil, false
	}
	p := tpl.OrigPath
	if !filepath.IsAbs(p) {
		p = filepath.Join(absRoot, p)
	}
	clean := filepath.Clean(p)
	if strings.Contains(clean, "..") ||
		!strings.HasPrefix(strings.ToLower(clean), strings.ToLower(absRoot+string(os.PathSeparator))) {
		return nil, false
	}
	b, err := os.ReadFile(clean)
	if err != nil {
		return nil, false
	}
	return b, true
}
