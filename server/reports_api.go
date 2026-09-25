package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/report"
)

// ---- 报告中心 API ----
// 报告是产出物的持久化快照:任务总结报告 / 漏洞报告(来自 findings.report)
// 由各自页面归档入库,自定义报告直接在报告中心新建。同一来源重复归档覆盖。

// listReports GET /api/reports?kind=&q=&company_id=&severity=&limit=&offset= —
// 列表 + 分类计数。company_id>0 时只返回归属该企业的报告(经任务链/锚定资产推导);
// severity 取 critical/high/medium/low(大小写不敏感),其它非空值 400——静默忽略
// 会让用户以为筛选生效了却看到全量,更难排查。
func (s *Server) listReports(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	companyID, _ := strconv.ParseInt(r.URL.Query().Get("company_id"), 10, 64)
	severity := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("severity")))
	switch severity {
	case "", "critical", "high", "medium", "low":
	default:
		writeErr(w, 400, "severity 必须是 critical/high/medium/low 之一")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	items, total, counts, err := s.m.PG().ListReports(kind, q, severity, companyID, limit, offset)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if items == nil {
		items = []*db.Report{}
	}
	writeJSON(w, 200, map[string]any{"reports": items, "total": total, "counts": counts})
}

// listReportCompanies GET /api/reports/companies —
// 报告中心的「先选企业」列表:每个企业名下的报告数与分类细分,附企业名。
func (s *Server) listReportCompanies(w http.ResponseWriter, r *http.Request) {
	counts, unassigned, err := s.m.PG().CountReportsByCompany()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	companies, err := s.m.PG().Companies().ListCompanies()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	nameByID := make(map[int64]string, len(companies))
	for _, c := range companies {
		nameByID[c.ID] = c.Name
	}
	items := make([]map[string]any, 0, len(counts))
	total := 0
	for _, c := range counts {
		total += c.Total
		items = append(items, map[string]any{
			"company_id": c.CompanyID,
			"name":       nameByID[c.CompanyID],
			"total":      c.Total,
			"task":       c.Task,
			"finding":    c.Finding,
			"custom":     c.Custom,
		})
	}
	writeJSON(w, 200, map[string]any{
		"companies": items, "assigned": total, "unassigned": unassigned,
	})
}

// getReportDetail GET /api/reports/{id} — 报告全文。
func (s *Server) getReportDetail(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, 200, map[string]any{"report": rep})
}

// reportEvent helper:给一份报告记一条事件(失败仅记日志,不影响主流程)。
func (s *Server) logReportEvent(reportID int64, kind, actor string, detail map[string]any) {
	id := reportID
	dj, _ := json.Marshal(detail)
	if err := s.m.PG().AddReportEvent(&id, nil, nil, kind, actor, dj); err != nil {
		log.Printf("[report] 事件记录失败(%s #%d): %v", kind, reportID, err)
	}
}

// reportEventWithTask helper:带任务归属的事件。
func (s *Server) logReportEventTask(reportID, taskID int64, kind, actor string, detail map[string]any) {
	rid, tid := reportID, taskID
	dj, _ := json.Marshal(detail)
	if err := s.m.PG().AddReportEvent(&rid, nil, &tid, kind, actor, dj); err != nil {
		log.Printf("[report] 事件记录失败(%s #%d): %v", kind, reportID, err)
	}
}

// listReportEvents GET /api/reports/{id}/events — 生成过程事件流。
func (s *Server) listReportEvents(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad report id")
		return
	}
	events, err := s.m.PG().ListReportEvents(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if events == nil {
		events = []*db.ReportEvent{}
	}
	writeJSON(w, 200, map[string]any{"events": events})
}

// createReport POST /api/reports — 新建自定义报告 {title, summary?, content}。
func (s *Server) createReport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		writeErr(w, 400, "title 必填")
		return
	}
	rep, err := s.m.PG().CreateReport(req.Title, strings.TrimSpace(req.Summary), req.Content)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	log.Printf("[reports] 新建自定义报告 «%s» #%d", rep.Title, rep.ID)
	s.logReportEvent(rep.ID, "created", "user", map[string]any{"words": len([]rune(req.Content))})
	writeJSON(w, 201, map[string]any{"report": rep})
}

// deleteReportDetail DELETE /api/reports/{id}。
func (s *Server) deleteReportDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad report id")
		return
	}
	deleted, err := s.m.PG().DeleteReport(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !deleted {
		writeErr(w, 404, "report not found")
		return
	}
	s.logReportEvent(id, "deleted", "user", nil)
	writeJSON(w, 200, map[string]any{"deleted": true})
}

// generateReport POST /api/reports/generate — 从现有产出归档报告入库。
// body: {"source":"task","task_id":"..."} 或 {"source":"finding","finding_id":N}。
// 任务报告即时渲染探索图;漏洞报告要求 findings.report 已有正文。
func (s *Server) generateReport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source    string `json:"source"`
		TaskID    string `json:"task_id"`
		FindingID int64  `json:"finding_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	switch req.Source {
	case "task":
		t := s.m.ResolveTask(req.TaskID)
		if t == nil {
			writeErr(w, 404, "task not found")
			return
		}
		title, summary, md := s.renderTaskReport(t)
		id, created, err := s.m.PG().UpsertTaskReport(taskNumericID(t.ID), title, summary, md)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		kind := "updated"
		if created {
			kind = "created"
		}
		s.logReportEventTask(id, taskNumericID(t.ID), kind, "user", map[string]any{"source": "task_page", "words": len([]rune(md))})
		log.Printf("[reports] 任务 %s 报告已归档 #%d(新建=%v)", t.ID, id, created)
		writeJSON(w, 200, map[string]any{"id": id, "created": created})
	case "finding":
		if req.FindingID <= 0 {
			writeErr(w, 400, "finding_id 必填")
			return
		}
		f, err := s.m.PG().GetFinding(req.FindingID)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if f == nil {
			writeErr(w, 404, "finding not found")
			return
		}
		if strings.TrimSpace(f.Report) == "" {
			writeErr(w, 400, "该发现还没有详细报告,无法归档")
			return
		}
		title := strings.TrimSpace(f.Name)
		if title == "" {
			title = strings.TrimSpace(f.VulnClass)
		}
		if title == "" {
			title = "未分类漏洞"
		}
		md := report.SingleFindingMarkdown(f, time.Now())
		id, created, err := s.m.PG().UpsertFindingReport(f.ID, f.TaskID, title, strings.TrimSpace(f.Summary), md)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		kind := "updated"
		if created {
			kind = "created"
		}
		var tid *int64
		if f.TaskID != nil {
			tid = f.TaskID
			s.logReportEventTask(id, *tid, kind, "user", map[string]any{"source": "finding_page", "finding_id": f.ID, "words": len([]rune(md))})
		} else {
			s.logReportEvent(id, kind, "user", map[string]any{"source": "finding_page", "finding_id": f.ID, "words": len([]rune(md))})
		}
		log.Printf("[reports] 发现 #%d 报告已归档 #%d(新建=%v)", f.ID, id, created)
		writeJSON(w, 200, map[string]any{"id": id, "created": created})
	default:
		writeErr(w, 400, "source 必须是 task 或 finding")
	}
}
