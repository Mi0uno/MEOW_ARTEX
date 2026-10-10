package server

// 报告生成作业 API(report_jobs):
//
//	POST /api/reports/{id}/export-ai     AI 按模板导出(异步:建作业+监看会话+流水线任务,202)
//	GET  /api/reports/{id}/jobs          报告的作业历史(导出 + AI 撰写)
//	GET  /api/report-jobs/{id}           作业进度(前端轮询)
//	GET  /api/report-jobs/{id}/download  产物下载(md/docx)
//
// registerReportExportRoutes 由 server.go 路由区调用,签名冻结(包级函数、只有
// mux 参数,拿不到 Server 接收者)。因此 Server 实例走包级引用:由 server.go 的
// New() 无条件绑定(bindReportRouteServer);launchReportPipelineTask 里再绑一次
// 属幂等兜底。进程内单实例语义,取最后一次绑定。

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/Autumn-27/artex/db"
)

// reportRouteServer 进程内 Server 引用(见文件头注释;进程内单实例语义)。
var reportRouteServer atomic.Pointer[Server]

// bindReportRouteServer 回填路由层使用的 Server 引用(幂等,取最后一次)。
func bindReportRouteServer(s *Server) {
	if s != nil {
		reportRouteServer.Store(s)
	}
}

// reportRoute 把方法值包成 http.Handler:请求时再解 Server 引用(见上)。
func reportRoute(h func(*Server, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := reportRouteServer.Load()
		if s == nil {
			writeErr(w, 503, "服务初始化中,请稍后重试")
			return
		}
		h(s, w, r)
	}
}

// registerReportExportRoutes 注册报告生成作业路由(签名冻结,见文件头)。
func registerReportExportRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/reports/{id}/export-ai", reportRoute((*Server).exportReportAI))
	mux.HandleFunc("GET /api/reports/{id}/jobs", reportRoute((*Server).listReportJobs))
	mux.HandleFunc("GET /api/report-jobs/{id}", reportRoute((*Server).getReportJobStatus))
	mux.HandleFunc("GET /api/report-jobs/{id}/download", reportRoute((*Server).downloadReportJob))
}

// exportReportAI POST /api/reports/{id}/export-ai — AI 按模板导出,异步任务化。
// body: {template_id, format: "md"|"docx", profile_id?}。校验顺序:报告/模板存在
// → profile(0=激活配置,provider 可用性)→ 防重入(同报告已有 running 导出作业
// 409)。通过后:建作业 → report_writer 监看会话(首条 user 消息)→ 建 report
// 流水线任务(report_export)→ 记录监看关系 → 启动。202 {job_id, task_id, conv_id}。
// 400/404 是参数问题不建作业;作业建立后的失败走作业 failed(可见可重试)。
func (s *Server) exportReportAI(w http.ResponseWriter, r *http.Request) {
	reportID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || reportID <= 0 {
		writeErr(w, 400, "bad report id")
		return
	}
	var req struct {
		TemplateID int64  `json:"template_id"`
		Format     string `json:"format"`
		ProfileID  int64  `json:"profile_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil && err != io.EOF {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	req.Format = strings.ToLower(strings.TrimSpace(req.Format))
	switch req.Format {
	case "md", "docx":
	default:
		writeErr(w, 400, "format 必须是 md 或 docx")
		return
	}
	rep, err := s.m.PG().GetReport(reportID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if rep == nil {
		writeErr(w, 404, "report not found")
		return
	}
	tpl, err := s.m.PG().GetReportTemplate(req.TemplateID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if tpl == nil {
		writeErr(w, 404, "template not found")
		return
	}
	if req.ProfileID == 0 {
		if p, e := s.m.PG().ActiveProfile(); e == nil && p != nil {
			req.ProfileID = p.ID
		}
	}
	if req.ProfileID == 0 {
		writeErr(w, 400, "未配置可用的 LLM 模型,请先在 AI管理 · LLM 添加并激活")
		return
	}
	if _, _, ok := s.providerForProfile(req.ProfileID); !ok {
		writeErr(w, 400, "所选 LLM 配置不可用")
		return
	}
	// 防重入:同报告已有运行中的导出作业拒绝重入(完成/失败后可再次发起)。
	if latest, e := s.m.PG().GetLatestReportJob("export", reportID); e == nil && latest != nil && latest.Status == "running" {
		writeErr(w, 409, "该报告已有进行中的 AI 导出作业,请等待完成或失败后再试")
		return
	}
	job, err := s.m.PG().CreateReportJob("export", reportID, req.TemplateID, 0, req.Format)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	profileID := req.ProfileID
	conv, err := s.m.PG().CreateConversation("report_writer", "AI 导出 · "+rep.Title, &profileID)
	if err != nil {
		_ = s.m.PG().SetReportJobFailed(job.ID, "创建监看会话失败: "+err.Error())
		writeErr(w, 500, "创建监看会话失败: "+err.Error())
		return
	}
	reqText := fmt.Sprintf("请按模板「%s」AI 生成报告并导出为 %s 格式。生成按模板章节分章进行,进度在本会话同步;完成后到报告详情页「生成记录」下载。", tpl.Name, req.Format)
	if _, err := s.m.PG().AppendConvActivity(conv.ID, db.Activity{
		Worker: "report_writer", Kind: "user", Summary: firstLine(reqText, 200), Detail: reqText,
	}); err != nil {
		log.Printf("[report-export] conv %d 写导出请求失败: %v", conv.ID, err)
	}
	cfg, _ := json.Marshal(reportPipelineCfg{
		JobID: job.ID, ReportID: reportID, TemplateID: req.TemplateID,
		Format: req.Format, ProfileID: req.ProfileID, ConvID: conv.ID,
	})
	coverageOff := false
	// 企业归属继承:报告归属哪家企业,导出任务就归属哪家(报告中心任务页按企业
	// 过滤时可见)。推导失败/无归属不阻塞导出(任务落「未关联企业」桶)。
	jobCompanies, err := s.m.PG().ReportCompanyIDs(reportID, 8)
	if err != nil {
		log.Printf("[report-export] 报告 %d 企业归属推导失败(任务将无企业归属): %v", reportID, err)
		jobCompanies = nil
	}
	t, err := s.m.CreateTaskWithOptions("AI 导出 · "+rep.Title,
		fmt.Sprintf("按模板「%s」AI 生成报告(%s)", tpl.Name, req.Format), db.TaskCreateOptions{
			Stage: "report", Kind: "pipeline", StepKey: "report_export", StepConfig: cfg,
			LLMProfileIDs: []int64{req.ProfileID}, CoverageEnabled: &coverageOff,
			CompanyIDs: jobCompanies,
		})
	if err != nil {
		_ = s.m.PG().SetReportJobFailed(job.ID, "创建导出任务失败: "+err.Error())
		writeErr(w, 500, "创建导出任务失败: "+err.Error())
		return
	}
	if err := s.m.PG().SetReportJobMonitor(job.ID, taskNumericID(t.ID), conv.ID); err != nil {
		log.Printf("[report-export] 作业 %d 记录监看信息失败: %v", job.ID, err)
	}
	s.launchReportPipelineTask(t)
	writeJSON(w, 202, map[string]any{"job_id": job.ID, "task_id": taskNumericID(t.ID), "conv_id": conv.ID})
}

// listReportJobs GET /api/reports/{id}/jobs — 报告的作业历史(新者在前,20 条)。
func (s *Server) listReportJobs(w http.ResponseWriter, r *http.Request) {
	reportID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || reportID <= 0 {
		writeErr(w, 400, "bad report id")
		return
	}
	jobs, err := s.m.PG().ListReportJobsByReport(reportID, 20)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if jobs == nil {
		jobs = []*db.ReportJob{}
	}
	writeJSON(w, 200, map[string]any{"jobs": jobs})
}

// getReportJobStatus GET /api/report-jobs/{id} — 作业进度(轮询)。
func (s *Server) getReportJobStatus(w http.ResponseWriter, r *http.Request) {
	jobID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || jobID <= 0 {
		writeErr(w, 400, "bad job id")
		return
	}
	job, err := s.m.PG().GetReportJob(jobID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if job == nil {
		writeErr(w, 404, "job not found")
		return
	}
	writeJSON(w, 200, map[string]any{"job": job})
}

// downloadReportJob GET /api/report-jobs/{id}/download — 产物下载。
// done 且产物文件在才可下载;文件名 = 报告标题 + 格式后缀(与快速导出一致)。
func (s *Server) downloadReportJob(w http.ResponseWriter, r *http.Request) {
	jobID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || jobID <= 0 {
		writeErr(w, 400, "bad job id")
		return
	}
	job, err := s.m.PG().GetReportJob(jobID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if job == nil {
		writeErr(w, 404, "job not found")
		return
	}
	if job.Status != "done" {
		writeErr(w, 409, "作业尚未完成或已失败,暂无产物可下载")
		return
	}
	path, err := s.m.PG().JobResultPath(job.ID)
	if err != nil || path == "" {
		writeErr(w, 404, "产物文件不存在")
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		// 本地不在(已被迁移清理):尝试从默认存储桶回源(双写过渡读路径)
		fbName := fmt.Sprintf("report-%d", job.ReportID)
		if rep, e := s.m.PG().GetReport(job.ReportID); e == nil && rep != nil {
			if t := strings.TrimSpace(rep.Title); t != "" {
				fbName = t
			}
		}
		rel := "report-jobs/" + filepath.ToSlash(filepath.Base(path))
		if !s.serveArtifact(w, r, "report-jobs", rel, sanitizeReportFilename(fbName)) {
			writeErr(w, 404, "产物文件不存在或已被清理")
			return
		}
		return
	}
	name := fmt.Sprintf("report-%d", job.ReportID)
	if rep, e := s.m.PG().GetReport(job.ReportID); e == nil && rep != nil {
		if title := strings.TrimSpace(rep.Title); title != "" {
			name = title
		}
	}
	name = sanitizeReportFilename(name)
	switch job.Format {
	case "docx":
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
		name += ".docx"
	default:
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		name += ".md"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", urlPathEscape(name)))
	_, _ = w.Write(data)
}

// sanitizeReportFilename 下载文件名去掉文件系统非法字符(同 exportReport 的替换表)。
func sanitizeReportFilename(s string) string {
	return strings.NewReplacer(
		"\\", "_", "/", "_", ":", "_", "*", "_", "?", "_",
		"\"", "_", "<", "_", ">", "_", "|", "_",
	).Replace(strings.TrimSpace(s))
}
