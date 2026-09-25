package server

// 报告中心专属 agent(report_writer,stage='report')的四个对话工具:
// report_get_context / report_get_template / report_list_templates / report_save_report。
// 工具只做【确定性取数与落库】——任务上下文、保底报告、模板清单、发现明细都来自
// db 层现成函数与 renderTaskReport,不打 LLM;组织成文的「判断」留给模型。
//
// 注册链路(与 SRC 工具同机制,见 src_tools.go):
//  1. seedReportWriterTools 把本文件的工具声明 seed 进 tools 表(key upsert、
//     默认绑 report_writer、stage=report);
//  2. 同一把手把执行实例追加进 agent.ToolAugment 的 host 工具出口;
//  3. 运行时 ToolResolve 按 tools 表绑定逐 agent 过滤——只有绑定了的 agent
//     (默认 report_writer,用户可在 UI 改绑)真能看到并调用。
//
// 工具声明就近放本文件:SRC 的模式是「声明在 agent 包(tools_src.go)」,但 agent 包
// 除 promptcatalog.go 外不归本任务改,故 server 侧自持声明,形态与 ToolSeed 一致。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// reportWriterToolsWired 防止 ToolAugment 被重复包装(测试进程里 New 可能跑多次,
// 每次都包一层会让工具重复出现在 base 列表)。
var reportWriterToolsWired bool

// reportWriterToolAgents 报告工具默认绑定:专属 agent report_writer(报告中心对话)。
var reportWriterToolAgents = []string{"report_writer"}

// reportFindingsCap 取数单次返回的发现清单上限(控上下文体积;总数如实标注)。
const reportFindingsCap = 50

// reportTemplatesCap 模板轻量列表上限(够选型用,避免超大库刷屏)。
const reportTemplatesCap = 50

// reportWriterToolSeeds 返回四个报告工具的可播种快照(顺序即期望的工具排列)。
func reportWriterToolSeeds() []agent.ToolSeed {
	return []agent.ToolSeed{
		{
			Key: "report_get_context",
			Desc: "报告撰写取数入口(写/改报告前必调):返回任务概况(目标/描述/状态/阶段/SRC 上下文)、" +
				"保底总结报告全文(平台确定性渲染的 Markdown 底稿)与漏洞发现清单(id/名称/严重度/URL/摘要;" +
				"include_findings=true 时附 PoC 与详细报告)。传 report_id 可一并取报告中心已有报告正文。" +
				"任务内运行自动取当前任务;对话页使用时若无法确定任务请显式传 task_id 或 report_id。",
			Schema: objSchema(map[string]any{
				"task_id": map[string]any{
					"type":        "integer",
					"description": "可选:目标任务 id。任务内运行自动取当前任务,省略即可",
				},
				"report_id": map[string]any{
					"type":        "integer",
					"description": "可选:报告中心报告 id,其标题/摘要/正文随上下文一并返回",
				},
				"include_findings": map[string]any{
					"type":        "boolean",
					"description": "可选:true=发现清单附 PoC 与详细报告(体积大,撰写漏洞章节时才开)",
				},
			}),
			Agents: reportWriterToolAgents,
			Stage:  "report",
		},
		{
			Key: "report_get_template",
			Desc: "读取指定报告模板全文:占位版模板正文(content,内置变量待填)、配套导出写作指令" +
				"(export_prompt:各章数据来源/口吻/格式/脱敏要求)与章节结构。按模板撰写报告前必调;" +
				"不确定有哪些模板先调 report_list_templates。",
			Schema: objSchema(map[string]any{
				"template_id": map[string]any{
					"type":        "integer",
					"description": "模板 id(report_list_templates 返回)",
				},
			}, "template_id"),
			Agents: reportWriterToolAgents,
			Stage:  "report",
		},
		{
			Key: "report_list_templates",
			Desc: "列出可选的报告模板(前 50 条:id/名称/分类/格式/是否配导出写作指令)," +
				"供用户选模板或按模板撰写前选型。q 支持按名称关键字过滤。",
			Schema: objSchema(map[string]any{
				"q": strParam("可选:模板名称关键字"),
			}),
			Agents: reportWriterToolAgents,
			Stage:  "report",
		},
		{
			Key: "report_save_report",
			Desc: "把成稿保存回报告中心(最终动作,不保存等于没写)。传 task_id 覆盖该任务的报告" +
				"(同一任务只保留一份,重复保存覆盖);传 report_id 按报告类型覆盖指定报告。" +
				"content 必填(Markdown 全文);title/summary 缺省沿用原值。返回 report_id 与字数。",
			Schema: objSchema(map[string]any{
				"task_id": map[string]any{
					"type":        "integer",
					"description": "可选:目标任务 id,覆盖其报告中心任务报告。任务内运行自动取当前任务",
				},
				"report_id": map[string]any{
					"type":        "integer",
					"description": "可选:报告中心报告 id,按报告类型覆盖该报告",
				},
				"title":   strParam("可选:报告标题(缺省沿用原标题)"),
				"summary": strParam("可选:摘要行(缺省沿用原摘要)"),
				"content": strParam("报告正文 Markdown 全文"),
			}, "content"),
			Agents: reportWriterToolAgents,
			Stage:  "report",
		},
	}
}

// seedReportWriterTools 播种并注册报告 agent 的配套工具(server.New 唯一入口,幂等)。
func (s *Server) seedReportWriterTools() {
	if s.m == nil || s.m.PG() == nil {
		return
	}
	// 1) tools 表按 key 播种(ON CONFLICT DO NOTHING:用户在 UI 改过的描述/绑定不被覆盖)。
	for _, seed := range reportWriterToolSeeds() {
		schema, _ := json.Marshal(seed.Schema)
		agents, _ := json.Marshal(seed.Agents)
		if err := s.m.PG().SeedToolWithStage(seed.Key, seed.Desc, schema, agents, seed.Stage); err != nil {
			log.Printf("[report-tools] seed %s 失败: %v", seed.Key, err)
			continue
		}
		// 后加 stage 列的老库:给存量行回填归属(仅空值)。
		if err := s.m.PG().BackfillToolStage(seed.Key, seed.Stage); err != nil {
			log.Printf("[report-tools] 回填 %s stage 失败: %v", seed.Key, err)
		}
	}
	// 2) 执行函数注册:把四个真工具追加进 agent.ToolAugment 的 host 工具出口,
	//    之后由 ToolResolve 按 tools 表绑定过滤。
	if reportWriterToolsWired {
		return
	}
	reportWriterToolsWired = true
	prev := agent.ToolAugment
	tools := s.reportWriterTools()
	agent.ToolAugment = func(ctx context.Context, agentKey string) ([]actool.CoreTool, agent.DeferredInfo, func()) {
		extra, def, cleanup := []actool.CoreTool(nil), agent.DeferredInfo{}, func() {}
		if prev != nil {
			extra, def, cleanup = prev(ctx, agentKey)
		}
		extra = append(extra, tools...)
		return extra, def, cleanup
	}
}

// reportWriterTools 构造四个工具实例:描述/schema 取自 reportWriterToolSeeds()(单一
// 事实来源,与 tools 表播的种一致),执行函数在下方。三个只读取数,一个落库保存。
func (s *Server) reportWriterTools() []actool.CoreTool {
	seedBy := map[string]agent.ToolSeed{}
	for _, sd := range reportWriterToolSeeds() {
		seedBy[sd.Key] = sd
	}
	get := func(key string) (string, map[string]any) { return seedBy[key].Desc, seedBy[key].Schema }
	descCtx, schemaCtx := get("report_get_context")
	descTpl, schemaTpl := get("report_get_template")
	descList, schemaList := get("report_list_templates")
	descSave, schemaSave := get("report_save_report")
	return []actool.CoreTool{
		roTool("report_get_context", descCtx, schemaCtx, s.runReportGetContext),
		roTool("report_get_template", descTpl, schemaTpl, s.runReportGetTemplate),
		roTool("report_list_templates", descList, schemaList, s.runReportListTemplates),
		wrTool("report_save_report", descSave, schemaSave, s.runReportSaveReport),
	}
}

// resolveReportTask 解析工具调用归属的任务:显式 task_id 优先,否则用运行上下文里的
// RunInfo(任务内运行自带)。返回 0 表示无法确定(调用方自行决定降级或报错)。
func resolveReportTask(ctx context.Context, rawTaskID json.RawMessage) int64 {
	if id := parseProfileID(rawTaskID); id > 0 {
		return id
	}
	if ri := agent.RunInfoFrom(ctx); ri.TaskID > 0 {
		return ri.TaskID
	}
	return 0
}

// runReportGetContext 组装任务/报告上下文。任务未在内存(已卸载/不存在)时不硬失败:
// report_id 可用仍返回报告部分;两者都拿不到才报错。
func (s *Server) runReportGetContext(ctx context.Context, in json.RawMessage) (actool.Result, error) {
	var a struct {
		TaskID          json.RawMessage `json:"task_id"`
		ReportID        json.RawMessage `json:"report_id"`
		IncludeFindings bool            `json:"include_findings"`
	}
	_ = json.Unmarshal(in, &a)
	taskID := resolveReportTask(ctx, a.TaskID)
	var task *Task
	if taskID > 0 {
		task = s.m.ResolveTask(strconv.FormatInt(taskID, 10))
	}
	reportID := parseProfileID(a.ReportID)
	if task == nil && reportID <= 0 {
		return actool.Errorf("无法确定上下文:请显式传 task_id 或 report_id(任务内运行可省略参数)"), nil
	}
	out := map[string]any{}
	if task != nil {
		out["task"] = s.reportToolTaskOverview(task)
		// 保底总结报告:平台按探索图确定性渲染的底稿,模型以此为数据基准改写,
		// 不得引入其中没有的事实(数据真实性的锚)。
		title, summary, md := s.renderTaskReport(task)
		out["fallback_report"] = map[string]any{
			"title": title, "summary": summary, "markdown": md,
			"note": "平台确定性渲染的保底总结报告;撰写时应以它为数据底稿,不得引入其中没有的事实",
		}
		fs, err := s.m.PG().ListFindingsForExport(db.FindingFilter{TaskID: strconv.FormatInt(taskID, 10)}, nil)
		if err != nil {
			return actool.Errorf("漏洞清单读取失败: " + err.Error()), nil
		}
		out["findings_total"] = len(fs)
		out["findings"] = s.reportFindingList(fs, a.IncludeFindings)
		out["findings_truncated"] = len(fs) > reportFindingsCap
	} else if taskID > 0 {
		out["task_note"] = fmt.Sprintf("任务 #%d 不存在或未加载,任务概况/保底报告/发现清单不可用", taskID)
	}
	if reportID > 0 {
		rep, err := s.m.PG().GetReport(reportID)
		if err != nil {
			return actool.Errorf("报告读取失败: " + err.Error()), nil
		}
		if rep == nil {
			return actool.Errorf(fmt.Sprintf("报告不存在: report_id=%d", reportID)), nil
		}
		out["report"] = map[string]any{
			"id": rep.ID, "kind": rep.Kind,
			"title": rep.Title, "summary": rep.Summary, "content": rep.Content,
		}
	}
	return jsonResult(out)
}

// reportToolTaskOverview 任务概况:目标/描述/状态/阶段 + SRC 模式的平台与授权范围提示。
// 平台/目标名读取失败降级省略,不阻断取数。
func (s *Server) reportToolTaskOverview(t *Task) map[string]any {
	out := map[string]any{
		"id":          taskNumericID(t.ID),
		"goal":        t.Goal,
		"description": t.Description,
		"status":      t.Status,
		"stage":       t.Stage,
	}
	if !t.SRCMode {
		return out
	}
	out["src_mode"] = true
	if p, err := s.m.PG().GetSRCPlatform(t.SRCPlatformID); err == nil && p != nil {
		out["src_platform"] = p.Name
	}
	if link, err := s.m.PG().GetSRCTargetLink(t.SRCPlatformID, t.SRCTargetID); err == nil && link != nil {
		out["src_target"] = link.TargetName
		out["src_scope_note"] = link.ScopeNote
	}
	return out
}

// reportFindingList 发现清单(id/名称/严重度/URL/摘要;withDetail 时附 PoC 与详细报告,
// 单字段截断防刷爆上下文)。上限 reportFindingsCap,超出部分调用方按 findings_total
// 与 findings_truncated 如实转述。
func (s *Server) reportFindingList(fs []*db.DBFinding, withDetail bool) []map[string]any {
	list := make([]map[string]any, 0, reportFindingsCap)
	for i, f := range fs {
		if i >= reportFindingsCap {
			break
		}
		item := map[string]any{
			"id":        f.ID,
			"name":      f.Name,
			"vulnclass": f.VulnClass,
			"severity":  strings.ToUpper(f.Severity),
			"status":    f.Status,
			"url":       s.findingPrimaryURL(f),
			"summary":   f.Summary,
		}
		if withDetail {
			item["poc"] = clipSection(f.Evidence)
			item["detail"] = clipSection(f.Report)
		}
		list = append(list, item)
	}
	return list
}

// findingPrimaryURL 从发现锚定的资产里取第一个可展示地址(域名/URL/IP/App)。
// 资产已删或未锚定返回空串——由模型如实处理,不编造 URL。
func (s *Server) findingPrimaryURL(f *db.DBFinding) string {
	if len(f.AssetIDs) == 0 || s.m.Assets() == nil {
		return ""
	}
	assets, err := s.m.Assets().GetByIDs(f.AssetIDs)
	if err != nil {
		return ""
	}
	for _, a := range assets {
		if v := srcAssetDisplayValue(a); v != "" {
			return v
		}
	}
	return ""
}

// runReportGetTemplate 返回模板全文:占位版正文 + 导出写作指令 + 章节结构。
func (s *Server) runReportGetTemplate(ctx context.Context, in json.RawMessage) (actool.Result, error) {
	var a struct {
		TemplateID json.RawMessage `json:"template_id"`
	}
	_ = json.Unmarshal(in, &a)
	id := parseProfileID(a.TemplateID)
	if id <= 0 {
		return actool.Errorf("template_id 必填:不确定有哪些模板时先调 report_list_templates"), nil
	}
	tpl, err := s.m.PG().GetReportTemplate(id)
	if err != nil {
		return actool.Errorf("模板读取失败: " + err.Error()), nil
	}
	if tpl == nil {
		return actool.Errorf(fmt.Sprintf("模板不存在: template_id=%d", id)), nil
	}
	return jsonResult(map[string]any{
		"template_id":   tpl.ID,
		"name":          tpl.Name,
		"category":      tpl.Category,
		"format":        tpl.Format,
		"content":       tpl.Content,
		"export_prompt": tpl.ExportPrompt,
		"structure":     tpl.Structure,
		"hint": "content 是占位版模板(内置变量待填);export_prompt 是配套导出写作指令," +
			"撰写正文时按它组织各章数据来源、口吻与格式",
	})
}

// runReportListTemplates 前 50 条轻量列表,供选模板。db 层列表查询不带正文与导出
// 指令(列裁剪),has_export_prompt 用 GetReportTemplate 逐条补判(上限 50,代价可控)。
func (s *Server) runReportListTemplates(ctx context.Context, in json.RawMessage) (actool.Result, error) {
	var a struct {
		Q string `json:"q"`
	}
	_ = json.Unmarshal(in, &a)
	ts, total, err := s.m.PG().ListReportTemplates("", strings.TrimSpace(a.Q), reportTemplatesCap, 0)
	if err != nil {
		return actool.Errorf("模板列表读取失败: " + err.Error()), nil
	}
	items := make([]map[string]any, 0, len(ts))
	for _, t := range ts {
		hasPrompt := false
		if full, err := s.m.PG().GetReportTemplate(t.ID); err == nil && full != nil {
			hasPrompt = strings.TrimSpace(full.ExportPrompt) != ""
		}
		items = append(items, map[string]any{
			"id":                t.ID,
			"name":              t.Name,
			"category":          t.Category,
			"format":            t.Format,
			"has_export_prompt": hasPrompt,
		})
	}
	return jsonResult(map[string]any{
		"templates": items, "total": total,
		"hint": "has_export_prompt=true 的模板带 AI 导出写作指令,按模板生成正文时优先选它",
	})
}

// runReportSaveReport 把模型成稿写回报告中心。task_id 路径覆盖任务报告;report_id
// 路径按报告 kind 覆盖(task/finding 走各自 Upsert,custom 走「删旧建新」组合——
// db 层没有 custom 的更新函数,id 会变,返回值如实给新 id)。
func (s *Server) runReportSaveReport(ctx context.Context, in json.RawMessage) (actool.Result, error) {
	var a struct {
		TaskID   json.RawMessage `json:"task_id"`
		ReportID json.RawMessage `json:"report_id"`
		Title    string          `json:"title"`
		Summary  string          `json:"summary"`
		Content  string          `json:"content"`
	}
	_ = json.Unmarshal(in, &a)
	if strings.TrimSpace(a.Content) == "" {
		return actool.Errorf("content 必填:报告正文(Markdown 全文),不允许空保存"), nil
	}
	taskID := resolveReportTask(ctx, a.TaskID)
	reportID := parseProfileID(a.ReportID)
	if taskID <= 0 && reportID <= 0 {
		return actool.Errorf("无法确定写入目标:请传 task_id(覆盖任务报告)或 report_id(覆盖指定报告)"), nil
	}
	if taskID > 0 {
		return s.saveTaskReportByID(taskID, a.Title, a.Summary, a.Content)
	}
	return s.saveReportByKind(reportID, a.Title, a.Summary, a.Content)
}

// saveTaskReportByID 覆盖任务报告(UpsertTaskReport 幂等:同一任务一行)。
// 标题/摘要缺省回退保底渲染值(任务在内存)或既有报告值,正文以模型成稿为准。
func (s *Server) saveTaskReportByID(taskID int64, title, summary, content string) (actool.Result, error) {
	if t := s.m.ResolveTask(strconv.FormatInt(taskID, 10)); t != nil {
		defTitle, defSummary, _ := s.renderTaskReport(t)
		if strings.TrimSpace(title) == "" {
			title = defTitle
		}
		if strings.TrimSpace(summary) == "" {
			summary = defSummary
		}
	} else if prev, err := s.m.PG().GetReportByTask(taskID); err == nil && prev != nil {
		if strings.TrimSpace(title) == "" {
			title = prev.Title
		}
		if strings.TrimSpace(summary) == "" {
			summary = prev.Summary
		}
	}
	if strings.TrimSpace(title) == "" {
		title = fmt.Sprintf("任务 #%d 渗透测试报告", taskID)
	}
	if strings.TrimSpace(summary) == "" {
		summary = "AI 撰写的渗透测试总结报告"
	}
	id, _, err := s.m.PG().UpsertTaskReport(taskID, title, summary, content)
	if err != nil {
		return actool.Errorf("任务报告保存失败: " + err.Error()), nil
	}
	s.logReportEventTask(id, taskID, "updated", "agent", map[string]any{
		"tool": "report_save_report", "words": len([]rune(content)),
	})
	return jsonResult(map[string]any{"report_id": id, "words": len([]rune(content))})
}

// saveReportByKind 按报告 kind 覆盖:task/finding 走对应 Upsert(保持来源关联),
// custom 走「删旧建新」组合(id 变更,返回新 id)。报告不存在报错。
func (s *Server) saveReportByKind(reportID int64, title, summary, content string) (actool.Result, error) {
	rep, err := s.m.PG().GetReport(reportID)
	if err != nil {
		return actool.Errorf("报告读取失败: " + err.Error()), nil
	}
	if rep == nil {
		return actool.Errorf(fmt.Sprintf("报告不存在: report_id=%d", reportID)), nil
	}
	if strings.TrimSpace(title) == "" {
		title = rep.Title
	}
	if strings.TrimSpace(summary) == "" {
		summary = rep.Summary
	}
	var id int64
	switch rep.Kind {
	case "task":
		if rep.TaskID == nil || *rep.TaskID <= 0 {
			return actool.Errorf("该报告未关联任务,不能用 report_id 覆盖;请改传 task_id"), nil
		}
		id, _, err = s.m.PG().UpsertTaskReport(*rep.TaskID, title, summary, content)
	case "finding":
		if rep.FindingID == nil || *rep.FindingID <= 0 {
			return actool.Errorf("该报告未关联漏洞记录,无法覆盖保存"), nil
		}
		id, _, err = s.m.PG().UpsertFindingReport(*rep.FindingID, rep.TaskID, title, summary, content)
	case "custom":
		if _, err = s.m.PG().DeleteReport(reportID); err != nil {
			return actool.Errorf("旧报告删除失败: " + err.Error()), nil
		}
		var cr *db.Report
		if cr, err = s.m.PG().CreateReport(title, summary, content); err == nil {
			id = cr.ID
		}
	default:
		return actool.Errorf("未知报告类型: " + rep.Kind), nil
	}
	if err != nil {
		return actool.Errorf("报告保存失败: " + err.Error()), nil
	}
	// 事件归属:报告挂了任务就带任务,报告详情页按任务聚合生成过程。
	if rep.TaskID != nil && *rep.TaskID > 0 {
		s.logReportEventTask(id, *rep.TaskID, "updated", "agent", map[string]any{
			"tool": "report_save_report", "words": len([]rune(content)),
		})
	} else {
		s.logReportEvent(id, "updated", "agent", map[string]any{
			"tool": "report_save_report", "words": len([]rune(content)),
		})
	}
	return jsonResult(map[string]any{"report_id": id, "words": len([]rune(content))})
}
