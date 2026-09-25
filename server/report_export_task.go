package server

// AI 按模板导出任务(step_key=report_export):报告中心「AI 生成」的异步执行体。
//
// 与 templateRunner 同构的任务化模式——不进 agent 引擎、不占并发槽、与 HTTP 请求
// 生命周期解耦;过程写 report_writer 监看会话(报告中心对话可见),任务在
// /function/tasks?stage=report 可见。与识别任务的区别:产物不落模板行,而是落
// report_jobs 作业行(产物文件进 <dataDir>/report-jobs/,经下载接口流出)。
//
// 分章生成(确定性拆章):按 markdown 顶层标题把模板切成章,每章一次
// analyzeCompletion(280s/章),章间无依赖、顺序拼接保持模板原顺序。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/report"
)

// reportExportChapterBudget 单章生成的时间预算(同 templateAnalyzeBudget;
// 前端只轮询作业状态,预算独立于任何 HTTP 超时)。
const reportExportChapterBudget = 280 * time.Second

// reportExportChapterSystem 单章成文的固定系统提示词。走 analyzeCompletion 直连
// LLM(temperature 0.2 + thinking disabled,输出确定性优先)。
const reportExportChapterSystem = "你是资深渗透测试报告撰写人,正在按报告模板逐章生成一份正式渗透测试报告。" +
	"要求:\n" +
	"1. 保留本章模板原文的固定文字、标题层级与表格骨架,把其中需要填数据的位置用「数据上下文」里的真实数据填好;\n" +
	"2. 数据只准来自数据上下文,不得编造、不得推测;上下文里没有的数据写「待补充」;\n" +
	"3. 简体中文书面语;不要使用代码围栏(```);只写本章内容,不要输出其它章节。\n" +
	"只输出本章成文的 Markdown 正文,不要任何解释。"

// reportPipelineRunner report 阶段流水线任务(按模板导出 / 任务报告 AI 撰写)的
// 取消注册表,仿 tmplRunner。Server 结构体字段冻结不可加,故用包内单例
// (进程内只有一个 Server 实例)。
type reportPipelineRunner struct {
	mu      sync.Mutex
	cancels map[int64]context.CancelFunc
}

var reportRunner = &reportPipelineRunner{cancels: map[int64]context.CancelFunc{}}

func (rr *reportPipelineRunner) register(taskID int64, cancel context.CancelFunc) bool {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if _, ok := rr.cancels[taskID]; ok {
		return false // 已在跑(重启恢复同路径,幂等)
	}
	rr.cancels[taskID] = cancel
	return true
}

func (rr *reportPipelineRunner) unregister(taskID int64) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	delete(rr.cancels, taskID)
}

// stopReportPipelineTask 取消一个在跑的 report 流水线任务(与 stopTemplateTask
// 同语义;当前 server.go 的 pause 分支未路由到这里,保留接口供后续接线)。
func (s *Server) stopReportPipelineTask(t *Task) bool {
	taskID, err := taskNumericIDErr(t.ID)
	if err != nil {
		return false
	}
	rr := reportRunner
	rr.mu.Lock()
	cancel, ok := rr.cancels[taskID]
	rr.mu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

// reportPipelineCfg report 流水线任务 StepConfig 的公共形状(按需取子集,见
// spec §1 运行语义契约)。
type reportPipelineCfg struct {
	JobID        int64  `json:"job_id"`
	ReportID     int64  `json:"report_id"`
	TemplateID   int64  `json:"template_id"`
	SourceTaskID int64  `json:"source_task_id"`
	Format       string `json:"format"` // report_export: md|docx
	ProfileID    int64  `json:"profile_id"`
	ConvID       int64  `json:"conv_id"`
}

// templateSection 拆章结果:Title 是章标题行文本(无标题章为空),Body 是含
// 标题行的整章 markdown。
type templateSection struct {
	Title string
	Body  string
}

// splitTemplateSections 确定性拆章:按 markdown 顶层标题(#/##)切分模板正文,
// 每章 = 标题行到下一标题行;首个标题之前若有实质内容自成一章(序言);整份
// 无标题则单章全量。只做文本切割不做语义判断——拆章失败意味着模板没有可识别
// 的标题层级,退化为单章整模板生成,行为仍然正确。
func splitTemplateSections(content string) []templateSection {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	// 顶层标题边界:# 或 ## 且 # 后是空白(markdown 规范;### 以下视为章内小节,
	// 不拆章——拆太碎会把每章一次调用的预算放大)。
	isBoundary := func(line string) bool {
		t := strings.TrimSpace(line)
		n := 0
		for n < len(t) && t[n] == '#' {
			n++
		}
		if n == 0 || n > 2 {
			return false
		}
		rest := t[n:]
		return rest == "" || rest[0] == ' ' || rest[0] == '\t'
	}
	var sections []templateSection
	var cur []string
	flush := func() {
		body := strings.TrimSpace(strings.Join(cur, "\n"))
		cur = nil
		if body == "" {
			return
		}
		title := ""
		first := strings.SplitN(body, "\n", 2)[0]
		if isBoundary(first) {
			title = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(first), "#"))
		}
		sections = append(sections, templateSection{Title: title, Body: body})
	}
	for _, line := range lines {
		if isBoundary(line) && len(cur) > 0 {
			flush()
		}
		cur = append(cur, line)
	}
	flush()
	return sections
}

// findingsDigest 发现列表的紧凑文本摘要(等级/名称/类别/状态/摘要/PoC 证据,
// reportCap>0 时附详细报告正文开头)。报告导出与任务报告 AI 撰写共用的取数源;
// 截断复用 vulndb_sync 的 truncateRunes;数据只来自 findings 表现有字段,不做
// 加工推断。
func findingsDigest(pg *db.DB, taskID int64, maxFindings, evidenceCap, reportCap int) string {
	fs, err := pg.ListFindingsForExport(db.FindingFilter{TaskID: fmt.Sprint(taskID)}, nil)
	if err != nil || len(fs) == 0 {
		return ""
	}
	var b strings.Builder
	for i, f := range fs {
		if i >= maxFindings {
			b.WriteString(fmt.Sprintf("\n(其余 %d 条发现未列出)\n", len(fs)-maxFindings))
			break
		}
		fmt.Fprintf(&b, "### [%s] %s\n", strings.ToUpper(strings.TrimSpace(f.Severity)), f.Name)
		if f.VulnClass != "" {
			fmt.Fprintf(&b, "- 类别: %s\n", f.VulnClass)
		}
		fmt.Fprintf(&b, "- 状态: %s\n", f.Status)
		if f.Summary != "" {
			fmt.Fprintf(&b, "- 摘要: %s\n", truncateRunes(f.Summary, evidenceCap))
		}
		if ev := strings.TrimSpace(f.Evidence); ev != "" {
			fmt.Fprintf(&b, "- PoC/证据:\n%s\n", truncateRunes(ev, evidenceCap*2))
		}
		if reportCap > 0 && strings.TrimSpace(f.Report) != "" {
			fmt.Fprintf(&b, "- 详细报告(节选):\n%s\n", truncateRunes(f.Report, reportCap))
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

// buildReportExportContext 组装分章生成共用的「数据上下文」:报告原文 + 保底
// 总结报告 + 发现摘要。全部是平台已有实测数据,LLM 只准从这里取数。
func (s *Server) buildReportExportContext(rep *db.Report, sourceTaskID int64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "【报告标题】%s\n【报告摘要】%s\n", rep.Title, rep.Summary)
	if c := strings.TrimSpace(rep.Content); c != "" {
		fmt.Fprintf(&b, "\n【报告原文】\n%s\n", truncateRunes(c, 12000))
	}
	if sourceTaskID > 0 {
		if t := s.m.ResolveTask(strconv.FormatInt(sourceTaskID, 10)); t != nil {
			if _, _, md := s.renderTaskReport(t); strings.TrimSpace(md) != "" {
				fmt.Fprintf(&b, "\n【任务保底总结报告(实测产出汇总)】\n%s\n", truncateRunes(md, 8000))
			}
			if digest := findingsDigest(s.m.PG(), sourceTaskID, 30, 300, 0); digest != "" {
				fmt.Fprintf(&b, "\n【漏洞发现摘要】\n%s\n", digest)
			}
		}
	}
	return b.String()
}

// runReportExportTask 导出任务执行体:置 running → 锁监看会话 → 取数(报告原文 +
// 模板用户确认版 + 导出写作指令)→ 分章直连 LLM 生成(每章 280s,章完成即写会话
// 消息)→ 顺序拼接 → 落产物文件 → 作业 done + 事件 → 任务 done。
func (s *Server) runReportExportTask(ctx context.Context, t *Task) {
	var cfg reportPipelineCfg
	if len(t.StepConfig) > 0 {
		_ = json.Unmarshal(t.StepConfig, &cfg)
	}
	fail := func(reason string) {
		log.Printf("[report-export] 任务 %s 失败: %s", t.ID, reason)
		if cfg.JobID > 0 {
			_ = s.m.PG().SetReportJobFailed(cfg.JobID, reason)
		}
		if cfg.ConvID > 0 {
			if _, err := s.m.PG().AppendConvActivity(cfg.ConvID, db.Activity{
				Worker: "report_writer", Kind: "text", IsError: true,
				Summary: "AI 按模板导出失败:" + reason, Detail: reason,
			}); err != nil {
				log.Printf("[report-export] conv %d 写失败消息失败: %v", cfg.ConvID, err)
			}
			_ = s.m.PG().TouchConversation(cfg.ConvID)
		}
		// 失败不写 exported 事件(那是成功语义);图节点落失败原因可查。
		s.writeTemplateGraphNode(t, false, "AI 按模板导出失败:"+reason)
		_ = s.m.SetTaskStatus(t.ID, "failed")
	}

	job, err := s.m.PG().GetReportJob(cfg.JobID)
	if err != nil || job == nil {
		fail("导出作业不存在")
		return
	}
	if err := s.m.SetTaskStatus(t.ID, "running"); err != nil {
		log.Printf("[report-export] 任务 %s 置 running 失败: %v", t.ID, err)
	}
	// 监看会话置 busy:生成期间拒绝重入消息(与 runTemplateTask 同语义)。
	busyKey := fmt.Sprintf("conv-%d", cfg.ConvID)
	s.chatMu.Lock()
	s.chatBusy[busyKey] = true
	s.chatMu.Unlock()
	defer func() {
		s.chatMu.Lock()
		delete(s.chatBusy, busyKey)
		s.chatMu.Unlock()
	}()

	rep, err := s.m.PG().GetReport(cfg.ReportID)
	if err != nil || rep == nil {
		fail("报告不存在或已被删除")
		return
	}
	tpl, err := s.m.PG().GetReportTemplate(cfg.TemplateID)
	if err != nil || tpl == nil {
		fail("模板不存在或已被删除")
		return
	}
	sections := splitTemplateSections(tpl.Content)
	guide := strings.TrimSpace(tpl.ExportPrompt)
	if _, err := s.m.PG().AppendConvActivity(cfg.ConvID, db.Activity{
		Worker: "report_writer", Kind: "text",
		Summary: fmt.Sprintf("已开始按模板「%s」AI 生成报告,共 %d 章,每章独立生成,预计几分钟;完成后到报告详情页「生成记录」下载。", tpl.Name, len(sections)),
	}); err != nil {
		log.Printf("[report-export] conv %d 写开工消息失败: %v", cfg.ConvID, err)
	}
	_ = s.m.PG().TouchConversation(cfg.ConvID)

	prov, _, ok := s.providerForProfile(cfg.ProfileID)
	if !ok {
		fail("LLM 配置不可用")
		return
	}
	dataCtx := s.buildReportExportContext(rep, cfg.SourceTaskID)

	chapters := make([]string, 0, len(sections))
	// fillSections 模板外壳逐节填充的数据(docx 且模板带原始包时交给
	// report.FillDocxSections):Heading=该节占位标题,Body=该节成文
	// (已剥掉模型回显的章节标题行,外壳里标题段本身保留原样式)。
	fillSections := make([]report.TemplateSectionFill, 0, len(sections))
	for i, sec := range sections {
		if ctx.Err() != nil {
			fail("已停止或服务重启")
			return
		}
		user := fmt.Sprintf("== 本章模板原文(第 %d/%d 章) ==\n%s\n\n== 导出写作指令(整份模板通用,务必遵守;为空则按模板结构与数据常识写) ==\n%s\n\n== 数据上下文(数据只准来自这里) ==\n%s",
			i+1, len(sections), sec.Body, guideOrEmpty(guide), dataCtx)
		// 分章生成同为无人值守链路:瞬态失败重试 1 次(20s 退避),避免长任务死于一次限流。
		out, err := analyzeWithRetry(ctx, prov, reportExportChapterSystem, user, 1, reportExportChapterBudget)
		if err != nil {
			if ctx.Err() != nil {
				fail("已停止或服务重启")
				return
			}
			fail(fmt.Sprintf("第 %d/%d 章生成失败: %v", i+1, len(sections), err))
			return
		}
		out = trimLLMFences(out)
		out = strings.TrimSpace(out)
		if out == "" {
			fail(fmt.Sprintf("第 %d/%d 章生成结果为空,请重试或更换模型", i+1, len(sections)))
			return
		}
		chapters = append(chapters, out)
		// 只收集带标题的节:空标题的「序言」节在 docx 模板里就是外壳封面自身
		// 的文字(模板 md 本解析自外壳,壳里已有),回填反而会在文末重复追加
		//(契约对未匹配节按序追加 sectPr 前,而空标题永远匹配不上)。
		if sec.Title != "" {
			fillSections = append(fillSections, report.TemplateSectionFill{
				Heading: sec.Title,
				Body:    stripEchoedHeading(out, sec.Title),
			})
		}
		title := sec.Title
		if title == "" {
			title = "序言"
		}
		if _, err := s.m.PG().AppendConvActivity(cfg.ConvID, db.Activity{
			Worker: "report_writer", Kind: "text",
			Summary: fmt.Sprintf("第 %d/%d 章「%s」已完成(%d 字)", i+1, len(sections), firstLine(title, 40), len([]rune(out))),
			Detail:  out,
		}); err != nil {
			log.Printf("[report-export] conv %d 写章节进度失败: %v", cfg.ConvID, err)
		}
		_ = s.m.PG().TouchConversation(cfg.ConvID)
	}

	full := strings.Join(chapters, "\n\n")
	words := len([]rune(full))

	// 产物落盘:<dataDir>/report-jobs/job_<id>.<ext>,路径只进作业行不外泄。
	dir := filepath.Join(s.m.dir, "report-jobs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail("创建产物目录失败: " + err.Error())
		return
	}
	ext := "md"
	if cfg.Format == "docx" {
		ext = "docx"
	}
	path := filepath.Join(dir, fmt.Sprintf("job_%d.%s", job.ID, ext))
	var data []byte
	// shellFilled=是否走了模板原始 docx 外壳填充路径(决定收尾文案)。
	shellFilled := false
	if cfg.Format == "docx" {
		data, shellFilled, err = s.renderReportExportDocx(tpl, rep, full, fillSections)
		if err != nil {
			fail("docx 渲染失败: " + err.Error())
			return
		}
	} else {
		data = []byte(full)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		fail("产物写入失败: " + err.Error())
		return
	}
	if err := s.m.PG().SetReportJobDone(job.ID, path); err != nil {
		fail("作业状态更新失败: " + err.Error())
		return
	}
	s.logReportEventTask(rep.ID, taskNumericID(t.ID), "exported", "agent", map[string]any{
		"job_id": job.ID, "format": cfg.Format, "ai": true, "words": words,
	})
	// 走了模板外壳填充路径时,收尾消息补格式保真说明(用户感知 docx 与原模板一致)。
	fillNote := ""
	if shellFilled {
		fillNote = ",已按模板原格式填充(页眉页脚/字体/配色保留)"
	}
	if _, err := s.m.PG().AppendConvActivity(cfg.ConvID, db.Activity{
		Worker: "report_writer", Kind: "text",
		Summary: fmt.Sprintf("AI 按模板导出已完成(共 %d 章,%d 字)%s,到报告详情页「生成记录」下载 %s。", len(sections), words, fillNote, strings.ToUpper(ext)),
	}); err != nil {
		log.Printf("[report-export] conv %d 写收尾消息失败: %v", cfg.ConvID, err)
	}
	s.writeTemplateGraphNode(t, true, fmt.Sprintf("AI 按模板「%s」生成报告完成(%d 章,%d 字)", tpl.Name, len(sections), words))
	_ = s.m.SetTaskStatus(t.ID, "done")
	_ = s.m.PG().TouchConversation(cfg.ConvID)
	log.Printf("[report-export] 任务 %s 完成: 作业 %d 报告 %d 模板 %d(%d 字,外壳填充=%v)", t.ID, job.ID, rep.ID, tpl.ID, words, shellFilled)
}

// guideOrEmpty 写作指令为空时的占位文案(提示词里保持段落结构)。
func guideOrEmpty(guide string) string {
	if strings.TrimSpace(guide) == "" {
		return "(无)"
	}
	return guide
}

// trimLLMFences 剥掉模型偶尔给整章套上的 ```markdown 围栏(同 runTemplateTask)。
func trimLLMFences(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```markdown")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	return strings.TrimSpace(s)
}

// renderReportExportDocx 生成 AI 导出的 docx 产物字节。优先走「模板原始外壳
// 逐节填充」:模板带原始 docx 包(OrigPath 非空且文件在盘)时调
// report.FillDocxSections——页眉页脚/封面/logo/字体/配色原样保留,只按章节
// 替换正文。外壳缺失/路径非法/填充失败一律打日志并退回现有的裸生成路径
// (DocxFromMarkdown),保证导出永不失败丢内容,只是没有模板格式。
// 第二个返回值 = 是否走了外壳填充路径(监看会话收尾文案据此补充格式说明)。
func (s *Server) renderReportExportDocx(tpl *db.ReportTemplate, rep *db.Report, full string, sections []report.TemplateSectionFill) ([]byte, bool, error) {
	// fallback 退回裸生成;reason 非空说明是「有外壳但用不上」的降级,打日志可查。
	fallback := func(reason string) ([]byte, bool, error) {
		if reason != "" {
			log.Printf("[report-export] 模板 %d 退回裸生成 docx: %s", tpl.ID, reason)
		}
		data, err := report.DocxFromMarkdown(full)
		return data, false, err
	}
	if tpl.OrigPath == "" {
		// 无外壳是常态(md 上传的模板),静默走原路径。
		return fallback("")
	}
	// 复用 origDocxPath(report_templates_api.go)的既有解析:OrigPath 存的是
	// dataDir/report-templates/ 下的文件名(兼容历史绝对路径),带路径穿越防御。
	origPath, ok := s.origDocxPath(tpl)
	if !ok {
		return fallback("OrigPath 无法解析为安全路径: " + tpl.OrigPath)
	}
	// 外壳文件可能已被清理(删模板外的手工清理/迁移),先 stat 再填。
	if _, err := os.Stat(origPath); err != nil {
		return fallback("原始 docx 文件不存在: " + origPath)
	}
	opts := report.SectionFillOptions{
		// 全文 inline 标记替换:封面标题/日期/摘要等 {{title}}/{{date}}/{{summary}}。
		Inline: map[string]string{
			"title":   rep.Title,
			"date":    time.Now().Format("2006-01-02"),
			"summary": rep.Summary,
		},
		// 文档信息表标签填值:项目名称/文档名称两格取报告标题。
		TableLabels: map[string]string{
			"项目名称": rep.Title,
			"文档名称": rep.Title,
		},
		Sections: sections,
	}
	data, err := report.FillDocxSections(origPath, opts)
	if err != nil {
		return fallback("外壳填充失败: " + err.Error())
	}
	return data, true, nil
}

// normalizeTitleKey 章节标题的归一化键(仅用于模型回显比对,不落盘):
// 去掉全部空白(含全角空格)→ 全半角折叠(全角 ASCII 区 U+FF01–U+FF5E 折为
// 半角,如「(」→"(")→ ASCII 小写。
func normalizeTitleKey(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsSpace(r) {
			continue
		}
		if r >= 0xFF01 && r <= 0xFF5E {
			r -= 0xFEE0
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// stripEchoedHeading 剥掉模型回显的章节标题行:生成正文首行是 md 标题行
// (#/## 前缀)且去记号后的归一化文本与该节占位标题归一化相同时,删掉整行——
// 外壳里标题段本身保留原样式,正文再带一份会重复出标题。比对两侧都过
// normalizeTitleKey,模型回显的全半角/空白差异不算回显失败。
func stripEchoedHeading(body, heading string) string {
	if heading == "" || body == "" {
		return body
	}
	lines := strings.SplitN(body, "\n", 2)
	first := strings.TrimSpace(lines[0])
	n := 0
	for n < len(first) && first[n] == '#' {
		n++
	}
	if n == 0 {
		return body // 首行不是 md 标题行,不视为回显
	}
	if normalizeTitleKey(strings.TrimSpace(first[n:])) == normalizeTitleKey(heading) {
		if len(lines) == 2 {
			return strings.TrimSpace(lines[1])
		}
		return ""
	}
	return body
}
