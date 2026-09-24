package server

// AI 占位模板识别任务:报告模板的「数据占位版」草稿生成是异步任务(stage='report',
// pipeline 形态),完全复刻 recon 流水线的任务化模式——不进 agent 引擎、不占 agent
// 并发槽、与 HTTP 请求生命周期解耦(服务端自有 ctx),过程可从三处监看:
//   任务列表/详情(/function/tasks?stage=report)、template_engineer 监看会话
//   (/chat?stage=report)、模板管理页的状态列。
//
// 何以任务化:旧实现是同步 HTTP 等 LLM 整份模板(实测 ~60s),浏览器/代理 60s 掐断
// 连接即 "context canceled",用户侧表现为 500(2026-09-24 实锤于 out/artex-8787.log)。
// 识别任务在服务端跑满全程,前端只轮询状态,网络抖动不再丢结果。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
)

// templateAnalyzeSystem AI 占位模板改写的固定系统提示词。走 analyzeCompletion 直连
// LLM(不走 agent harness):输出确定性优先(temperature 0.2 + thinking disabled),
// 只输出模板本身,草稿直接进编辑器。
const templateAnalyzeSystem = "你是渗透测试平台的报告模板工程师。用户会给一份报告模板(已从 Word/Markdown 解析为 Markdown)。" +
	"你的任务:把它改写成「数据占位版模板」——\n" +
	"1. 保留全部章节结构与固定文字(公司名/说明性文字/表格骨架);\n" +
	"2. 把「需要填入测试数据/结果」的位置替换为内置变量,可用变量:\n" +
	"   {{title}} 报告标题;{{date}} 日期;{{summary}} 摘要;{{content}} 报告正文全文;\n" +
	"   {{findings_table}} 漏洞表格;{{findings_detail}} 漏洞逐条详情;{{assets_summary}} 资产统计;\n" +
	"3. 不要编造内容、不要删除章节、不要改动变量语法(双花括号);\n" +
	"4. 只输出改写后的完整 Markdown 模板本身,不要任何解释、不要代码块围栏。"

// templateAnalyzeBudget LLM 生成整份模板的时间预算。独立于任何 HTTP 超时;
// 前端只轮询模板状态,不再有对应的前端 signal 对齐问题。
const templateAnalyzeBudget = 280 * time.Second

// templateRunner 识别任务的取消注册表(仿 reconRunner;识别任务无 evidence sink)。
type templateRunner struct {
	mu      sync.Mutex
	cancels map[int64]context.CancelFunc
}

func newTemplateRunner() *templateRunner {
	return &templateRunner{cancels: map[int64]context.CancelFunc{}}
}

func (tr *templateRunner) running(taskID int64) bool {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	_, ok := tr.cancels[taskID]
	return ok
}

func (tr *templateRunner) register(taskID int64, cancel context.CancelFunc) bool {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if _, ok := tr.cancels[taskID]; ok {
		return false
	}
	tr.cancels[taskID] = cancel
	return true
}

func (tr *templateRunner) unregister(taskID int64) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	delete(tr.cancels, taskID)
}

func (tr *templateRunner) stop(taskID int64) bool {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if cancel, ok := tr.cancels[taskID]; ok {
		cancel()
		return true
	}
	return false
}

// startTemplateAnalyzeTask 创建一条 stage='report' 识别任务 + template_engineer 监看
// 会话并异步启动,返回任务与会话 id。profileID 0=当前激活配置。
// 行内并发由 templateMu 串行化:同一模板同时只允许一个识别任务(running 中拒绝重入)。
func (s *Server) startTemplateAnalyzeTask(templateID, profileID int64) (int64, int64, error) {
	s.templateMu.Lock()
	defer s.templateMu.Unlock()

	tpl, err := s.m.PG().GetReportTemplate(templateID)
	if err != nil {
		return 0, 0, fmt.Errorf("读取模板失败: %w", err)
	}
	if tpl == nil {
		return 0, 0, fmt.Errorf("模板不存在")
	}
	if tpl.AnalyzeStatus == "running" {
		return 0, 0, fmt.Errorf("该模板正在识别中,请稍候")
	}
	if profileID == 0 {
		if p, err := s.m.pg.ActiveProfile(); err == nil && p != nil {
			profileID = p.ID
		}
	}
	if profileID == 0 {
		return 0, 0, fmt.Errorf("未配置可用的 LLM 模型,请先在 AI管理 · LLM 添加并激活")
	}
	if _, _, ok := s.providerForProfile(profileID); !ok {
		return 0, 0, fmt.Errorf("所选 LLM 配置不可用")
	}

	// 监看会话:绑在内置「模板工程师」(stage='report')名下,识别请求先落一条用户
	// 消息;草稿完成后由 runner 补 assistant 消息。用户后续可在会话里追问改稿。
	conv, err := s.m.PG().CreateConversation("template_engineer", "AI 占位模板 · "+tpl.Name, &profileID)
	if err != nil {
		return 0, 0, fmt.Errorf("创建监看会话失败: %w", err)
	}
	req := fmt.Sprintf("请把报告模板「%s」(%s)改写为数据占位版模板:保留章节结构与固定文字,数据位置替换为内置变量。模板结构:%s",
		tpl.Name, tpl.Category, tpl.SourceNote)
	if _, err := s.m.PG().AppendConvActivity(conv.ID, db.Activity{
		Worker: "template_engineer", Kind: "user", Summary: firstLine(req, 200), Detail: req,
	}); err != nil {
		log.Printf("[report-template] conv %d 写入识别请求失败: %v", conv.ID, err)
	}

	cfg, _ := json.Marshal(map[string]any{
		"template_id": templateID, "profile_id": profileID, "conv_id": conv.ID,
	})
	coverageOff := false
	t, err := s.m.CreateTaskWithOptions("AI 占位模板 · "+tpl.Name,
		fmt.Sprintf("把报告模板「%s」改写为数据占位版模板", tpl.Name), db.TaskCreateOptions{
			Stage: "report", Kind: "pipeline", StepKey: "template_analyze", StepConfig: cfg,
			LLMProfileIDs: []int64{profileID}, CoverageEnabled: &coverageOff,
		})
	if err != nil {
		return 0, 0, fmt.Errorf("创建识别任务失败: %w", err)
	}
	if err := s.m.PG().SetReportTemplateAnalyzeStart(templateID, taskNumericID(t.ID), conv.ID); err != nil {
		log.Printf("[report-template] 模板 %d 标记 running 失败: %v", templateID, err)
	}
	s.launchTemplateTask(t)
	return taskNumericID(t.ID), conv.ID, nil
}

// launchTemplateTask 异步跑一条识别任务。running 状态不重复启动(幂等,重启恢复同路径)。
func (s *Server) launchTemplateTask(t *Task) {
	taskID, parseErr := taskNumericIDErr(t.ID)
	if parseErr != nil {
		log.Printf("[report-template] 非法任务 id %q,跳过", t.ID)
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	if !s.tmplRunner.register(taskID, cancel) {
		cancel()
		return // 已在跑
	}
	go func() {
		defer s.tmplRunner.unregister(taskID)
		s.runTemplateTask(ctx, t)
	}()
}

func (s *Server) stopTemplateTask(t *Task) bool {
	taskID, err := taskNumericIDErr(t.ID)
	if err != nil {
		return false
	}
	return s.tmplRunner.stop(taskID)
}

// runTemplateTask 识别任务执行体:置 running → 监看会话发开工消息 → 直连 LLM 生成
// (独立 280s 预算)→ 草稿落模板行 + 事件 + 会话消息 + 探索图节点 → 置终态。
func (s *Server) runTemplateTask(ctx context.Context, t *Task) {
	var cfg struct {
		TemplateID int64 `json:"template_id"`
		ProfileID  int64 `json:"profile_id"`
		ConvID     int64 `json:"conv_id"`
	}
	if len(t.StepConfig) > 0 {
		_ = json.Unmarshal(t.StepConfig, &cfg)
	}
	fail := func(reason string) {
		log.Printf("[report-template] 任务 %s 失败: %s", t.ID, reason)
		if cfg.TemplateID > 0 {
			_ = s.m.PG().SetReportTemplateAnalyzeFailed(cfg.TemplateID, reason)
			s.templateEvent(cfg.TemplateID, "ai_analyze_failed", "agent", map[string]any{
				"task_id": t.ID, "error": reason,
			})
		}
		if cfg.ConvID > 0 {
			_, _ = s.m.PG().AppendConvActivity(cfg.ConvID, db.Activity{
				Worker: "template_engineer", Kind: "text", IsError: true,
				Summary: "AI 识别失败:" + reason, Detail: reason,
			})
		}
		s.writeTemplateGraphNode(t, false, "AI 占位模板识别失败:"+reason)
		_ = s.m.SetTaskStatus(t.ID, "failed")
	}

	tpl, err := s.m.PG().GetReportTemplate(cfg.TemplateID)
	if err != nil || tpl == nil {
		fail("模板不存在或读取失败")
		return
	}
	if err := s.m.SetTaskStatus(t.ID, "running"); err != nil {
		log.Printf("[report-template] 任务 %s 置 running 失败: %v", t.ID, err)
	}
	// 监看会话置 busy:对话列表显示运行中,生成期间拒绝重入消息(pgSendConversationMessage 409)。
	busyKey := fmt.Sprintf("conv-%d", cfg.ConvID)
	s.chatMu.Lock()
	s.chatBusy[busyKey] = true
	s.chatMu.Unlock()
	defer func() {
		s.chatMu.Lock()
		delete(s.chatBusy, busyKey)
		s.chatMu.Unlock()
	}()
	if _, err := s.m.PG().AppendConvActivity(cfg.ConvID, db.Activity{
		Worker: "template_engineer", Kind: "text",
		Summary: fmt.Sprintf("已开始分析模板「%s」(%s),预计 1~2 分钟,完成后草稿自动送到模板管理页。", tpl.Name, tpl.SourceNote),
	}); err != nil {
		log.Printf("[report-template] conv %d 写入开工消息失败: %v", cfg.ConvID, err)
	}
	_ = s.m.PG().TouchConversation(cfg.ConvID)

	prov, _, ok := s.providerForProfile(cfg.ProfileID)
	if !ok {
		fail("LLM 配置不可用")
		return
	}
	structureJSON, _ := json.Marshal(tpl.Structure)
	user := fmt.Sprintf("模板结构: %s\n\n模板正文:\n%s", string(structureJSON), tpl.Content)
	genCtx, cancel := context.WithTimeout(ctx, templateAnalyzeBudget)
	defer cancel()
	suggestion, err := analyzeCompletion(genCtx, prov, templateAnalyzeSystem, user)
	if err != nil {
		if ctx.Err() != nil {
			fail("已停止或服务重启")
			return
		}
		fail("AI 分析失败: " + err.Error())
		return
	}
	suggestion = strings.TrimSpace(suggestion)
	suggestion = strings.TrimPrefix(suggestion, "```markdown")
	suggestion = strings.TrimPrefix(suggestion, "```")
	suggestion = strings.TrimSuffix(strings.TrimSpace(suggestion), "```")
	suggestion = strings.TrimSpace(suggestion)
	if suggestion == "" {
		fail("AI 返回了空结果,请重试或换一个模型")
		return
	}
	if err := s.m.PG().SetReportTemplateAnalyzeDone(cfg.TemplateID, suggestion); err != nil {
		fail("草稿写入失败: " + err.Error())
		return
	}
	s.templateEvent(cfg.TemplateID, "ai_analyzed", "agent", map[string]any{
		"task_id": t.ID, "profile_id": cfg.ProfileID, "words": len([]rune(suggestion)),
	})
	if _, err := s.m.PG().AppendConvActivity(cfg.ConvID, db.Activity{
		Worker: "template_engineer", Kind: "text", Summary: firstLine(suggestion, 200), Detail: suggestion,
	}); err != nil {
		log.Printf("[report-template] conv %d 写入草稿失败: %v", cfg.ConvID, err)
	}
	s.writeTemplateGraphNode(t, true, fmt.Sprintf("AI 占位模板草稿已生成(%d 字),已写入模板「%s」,到模板管理页确认采用", len([]rune(suggestion)), tpl.Name))
	_ = s.m.SetTaskStatus(t.ID, "done")
	_ = s.m.PG().TouchConversation(cfg.ConvID)
	log.Printf("[report-template] 任务 %s 完成: 模板 %d 草稿 %d 字", t.ID, cfg.TemplateID, len([]rune(suggestion)))
}

// writeTemplateGraphNode 识别结果落探索图(任务详情→播报板可见):
// 起点(fact/origin,建任务自带) --spawns--> fact(结果/失败原因)。
// 注意 fact 节点的 state 只允许 confirmed/dismissed/origin(见 schema ck_node_state),
// 成功=confirmed、失败=dismissed。
func (s *Server) writeTemplateGraphNode(t *Task, ok bool, summary string) {
	store := s.m.PG().Exploration(t.ExpID)
	origin, err := store.OriginFactID()
	if err != nil || origin <= 0 {
		log.Printf("[report-template] 任务 %s 取起点节点失败(exp=%d): origin=%d err=%v", t.ID, t.ExpID, origin, err)
		return
	}
	state := "confirmed"
	if !ok {
		state = "dismissed"
	}
	nodeID, err := store.AddNode("fact", map[string]any{"summary": summary}, 3, state, "system", nil)
	if err != nil {
		log.Printf("[report-template] 任务 %s 写探索节点失败: %v", t.ID, err)
		return
	}
	if err := store.Link(origin, "spawns", nodeID); err != nil {
		log.Printf("[report-template] 任务 %s 连边失败: %v", t.ID, err)
	}
}

// taskNumericIDErr 同 taskNumericID 但带错误,供需要区分非法 id 的路径用。
func taskNumericIDErr(id string) (int64, error) {
	return strconv.ParseInt(id, 10, 64)
}
