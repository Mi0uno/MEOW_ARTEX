package server

// 任务总结报告 AI 撰写(step_key=task_report_ai):
//
//	触发链:任务终态 → autoArchiveTaskReport(保底报告落库)→
//	        go maybeStartTaskReportEnhance(taskID) → 建作业+监看会话+流水线任务。
//
// 三重静默跳过(保底报告永远在,润写只是锦上添花):settings 开关关 /
// 最新作业已 running|done(重启与重复终态幂等)/ LLM 无可用配置。
// 保底报告永不删:AI 产出只做 UpsertTaskReport 覆盖写,失败时保底版本原样保留。
// 成本护栏:并发上限由 launchReportPipelineTask 的 reportGenSem(容量 2)统一兜住。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

// taskReportAIBudget AI 撰写是「一份报告一次调用」,预算放宽到 600s(导出分章是
// 每章 280s;这里全文一次成文给慢模型留余量)。预算独立于 HTTP 超时(任务化)。
const taskReportAIBudget = 600 * time.Second

// taskReportAISystem 正式渗透测试报告撰写的固定系统提示词(直连 LLM,
// temperature 0.2 + thinking disabled,输出确定性优先)。
const taskReportAISystem = "你是资深渗透测试报告撰写人。根据输入的任务实测数据,撰写一份可直接交付的正式渗透测试报告(Markdown)。" +
	"结构要求:\n" +
	"# 测试概述(背景/目标/结论摘要)\n" +
	"## 测试范围与方法\n" +
	"## 资产概览\n" +
	"## 漏洞发现(逐条:名称/等级/URL/原理/PoC/修复建议,按严重程度降序;无漏洞则写明未发现)\n" +
	"## 总结与建议\n" +
	"硬性要求:数据只准来自输入材料,不得编造、不得推测,材料里没有的信息写「待补充」;" +
	"简体中文书面语;不要使用代码围栏(```);只输出报告正文 Markdown,不要任何解释。"

// maybeStartTaskReportEnhance 任务报告归档成功后的 AI 润写触发(异步):
// 开关 report_ai_enhance(”或"true"=开,"false"=关);同报告最新作业已 running|done
// 则跳过(failed 允许重试);LLM 无配置静默跳过。任何失败都不影响已归档的保底报告。
func (s *Server) maybeStartTaskReportEnhance(taskID int64) {
	bindReportRouteServer(s) // 该路径早于大多数 HTTP 请求,顺手回填路由层引用
	if v, _, err := s.m.PG().GetSetting("report_ai_enhance"); err == nil && v == "false" {
		return // 显式关闭;读设置失败按默认开处理
	}
	rep, err := s.m.PG().GetReportByTask(taskID)
	if err != nil || rep == nil {
		return // 保底报告未归档成功,无从润写
	}
	// 幂等:最新作业在跑或已成稿(重启/重复终态)→ 跳过。
	if latest, e := s.m.PG().GetLatestReportJob("task_report_ai", rep.ID); e == nil && latest != nil &&
		(latest.Status == "running" || latest.Status == "done") {
		return
	}
	profileID := int64(0)
	if p, e := s.m.PG().ActiveProfile(); e == nil && p != nil {
		profileID = p.ID
	}
	if profileID == 0 {
		return // LLM 未配置:保底报告已在,静默跳过
	}
	if _, _, ok := s.providerForProfile(profileID); !ok {
		return
	}
	job, err := s.m.PG().CreateReportJob("task_report_ai", rep.ID, 0, taskID, "")
	if err != nil {
		log.Printf("[report-ai] 任务 %d 建 AI 撰写作业失败: %v", taskID, err)
		return
	}
	conv, err := s.m.PG().CreateConversation("report_writer", "AI 撰写 · "+rep.Title, &profileID)
	if err != nil {
		_ = s.m.PG().SetReportJobFailed(job.ID, "创建监看会话失败: "+err.Error())
		log.Printf("[report-ai] 任务 %d 建监看会话失败: %v", taskID, err)
		return
	}
	reqText := fmt.Sprintf("请基于任务 #%d 的实测产出(保底总结报告 + 发现明细),撰写正式渗透测试报告:概述/测试范围与方法/资产概览/逐条漏洞(名称、等级、URL、原理、PoC、修复建议)/总结与建议。数据只准来自输入材料。", taskID)
	if _, err := s.m.PG().AppendConvActivity(conv.ID, db.Activity{
		Worker: "report_writer", Kind: "user", Summary: firstLine(reqText, 200), Detail: reqText,
	}); err != nil {
		log.Printf("[report-ai] conv %d 写撰写请求失败: %v", conv.ID, err)
	}
	cfg, _ := json.Marshal(reportPipelineCfg{
		JobID: job.ID, ReportID: rep.ID, SourceTaskID: taskID,
		ProfileID: profileID, ConvID: conv.ID,
	})
	coverageOff := false
	// 企业归属继承:报告归属哪家企业,润写任务就归属哪家(报告中心任务页按企业
	// 过滤时可见)。推导失败/无归属不阻塞润写(任务落「未关联企业」桶)。
	jobCompanies, err := s.m.PG().ReportCompanyIDs(rep.ID, 8)
	if err != nil {
		log.Printf("[report-ai] 报告 %d 企业归属推导失败(任务将无企业归属): %v", rep.ID, err)
		jobCompanies = nil
	}
	t, err := s.m.CreateTaskWithOptions("AI 撰写 · "+rep.Title,
		"基于任务实测产出撰写正式渗透测试报告", db.TaskCreateOptions{
			Stage: "report", Kind: "pipeline", StepKey: "task_report_ai", StepConfig: cfg,
			LLMProfileIDs: []int64{profileID}, CoverageEnabled: &coverageOff,
			CompanyIDs: jobCompanies,
		})
	if err != nil {
		_ = s.m.PG().SetReportJobFailed(job.ID, "创建撰写任务失败: "+err.Error())
		log.Printf("[report-ai] 任务 %d 建 AI 撰写任务失败: %v", taskID, err)
		return
	}
	if err := s.m.PG().SetReportJobMonitor(job.ID, taskNumericID(t.ID), conv.ID); err != nil {
		log.Printf("[report-ai] 作业 %d 记录监看信息失败: %v", job.ID, err)
	}
	s.launchReportPipelineTask(t)
	log.Printf("[report-ai] 任务 %d 的报告 AI 撰写已启动: 作业 %d 流水线任务 %s 会话 %d", taskID, job.ID, t.ID, conv.ID)
}

// runTaskReportAITask 撰写任务执行体:置 running → 锁监看会话 → 取数(保底报告 +
// 发现明细,只读 db 层,不依赖其他文件的取数封装)→ 单次 analyzeCompletion 成文 →
// UpsertTaskReport 覆盖 → 作业 done + 事件 → 任务 done。失败作业落 failed,
// 保底报告原样保留。
func (s *Server) runTaskReportAITask(ctx context.Context, t *Task) {
	var cfg reportPipelineCfg
	if len(t.StepConfig) > 0 {
		_ = json.Unmarshal(t.StepConfig, &cfg)
	}
	fail := func(reason string) {
		log.Printf("[report-ai] 任务 %s 失败: %s", t.ID, reason)
		if cfg.JobID > 0 {
			_ = s.m.PG().SetReportJobFailed(cfg.JobID, reason)
		}
		if cfg.ReportID > 0 {
			// 失败语义用 ai_enhance_failed;成功才是 ai_enhanced(前端事件流区分)。
			s.logReportEventTask(cfg.ReportID, taskNumericID(t.ID), "ai_enhance_failed", "agent", map[string]any{
				"job_id": cfg.JobID, "error": reason,
			})
		}
		if cfg.ConvID > 0 {
			if _, err := s.m.PG().AppendConvActivity(cfg.ConvID, db.Activity{
				Worker: "report_writer", Kind: "text", IsError: true,
				Summary: "报告 AI 撰写失败:" + reason, Detail: reason,
			}); err != nil {
				log.Printf("[report-ai] conv %d 写失败消息失败: %v", cfg.ConvID, err)
			}
			_ = s.m.PG().TouchConversation(cfg.ConvID)
		}
		s.writeTemplateGraphNode(t, false, "任务报告 AI 撰写失败:"+reason)
		_ = s.m.SetTaskStatus(t.ID, "failed")
	}

	job, err := s.m.PG().GetReportJob(cfg.JobID)
	if err != nil || job == nil {
		fail("撰写作业不存在")
		return
	}
	if err := s.m.SetTaskStatus(t.ID, "running"); err != nil {
		log.Printf("[report-ai] 任务 %s 置 running 失败: %v", t.ID, err)
	}
	if cfg.ConvID > 0 {
		busyKey := fmt.Sprintf("conv-%d", cfg.ConvID)
		s.chatMu.Lock()
		s.chatBusy[busyKey] = true
		s.chatMu.Unlock()
		defer func() {
			s.chatMu.Lock()
			delete(s.chatBusy, busyKey)
			s.chatMu.Unlock()
		}()
	}
	rep, err := s.m.PG().GetReport(cfg.ReportID)
	if err != nil || rep == nil {
		fail("报告不存在或已被删除")
		return
	}
	if _, err := s.m.PG().AppendConvActivity(cfg.ConvID, db.Activity{
		Worker: "report_writer", Kind: "text",
		Summary: "已开始基于任务实测产出撰写正式报告(保底报告保留,成稿后覆盖),预计几分钟。",
	}); err != nil {
		log.Printf("[report-ai] conv %d 写开工消息失败: %v", cfg.ConvID, err)
	}
	_ = s.m.PG().TouchConversation(cfg.ConvID)

	// 取数:保底报告(实测产出汇总)+ 发现明细(含 PoC 与详细报告节选)。
	// 源任务必须已加载进内存(renderTaskReport 走内存句柄);未加载即失败可见,
	// 不静默降级——数据来源不全会成文失真。
	sourceTask := s.m.ResolveTask(fmt.Sprintf("%d", cfg.SourceTaskID))
	if sourceTask == nil {
		fail("源任务未加载,无法取数(任务可能已被清理)")
		return
	}
	title, summary, baseMD := s.renderTaskReport(sourceTask)
	if strings.TrimSpace(baseMD) == "" {
		fail("任务实测产出为空,无法撰写")
		return
	}
	digest := findingsDigest(s.m.PG(), cfg.SourceTaskID, 30, 300, 1200)
	if digest == "" {
		digest = "(无漏洞发现记录)"
	}
	prov, _, ok := s.providerForProfile(cfg.ProfileID)
	if !ok {
		fail("LLM 配置不可用")
		return
	}
	user := fmt.Sprintf("【报告标题】%s\n【报告摘要】%s\n\n【保底总结报告(实测产出汇总)】\n%s\n\n【漏洞发现明细(含 PoC 节选)】\n%s",
		title, summary, truncateRunes(baseMD, 12000), truncateRunes(digest, 16000))

	// 无人值守链路:瞬态失败(限流/超时)重试 1 次(20s 退避),保底报告始终兜底。
	out, err := analyzeWithRetry(ctx, prov, taskReportAISystem, user, 1, taskReportAIBudget)
	if err != nil {
		if ctx.Err() != nil {
			fail("已停止或服务重启")
			return
		}
		fail("AI 撰写失败: " + err.Error())
		return
	}
	out = trimLLMFences(out)
	if strings.TrimSpace(out) == "" {
		fail("AI 返回了空结果,请重试或更换模型")
		return
	}
	words := len([]rune(out))
	// 覆盖写回:同一任务只保留一份任务报告;title/summary 沿用保底渲染
	//(标题稳定,避免 AI 另起炉灶导致列表排序/去重漂移),只有正文被润写。
	if _, _, err := s.m.PG().UpsertTaskReport(cfg.SourceTaskID, title, summary, out); err != nil {
		fail("报告覆盖写入失败: " + err.Error())
		return
	}
	if err := s.m.PG().SetReportJobDone(job.ID, ""); err != nil {
		fail("作业状态更新失败: " + err.Error())
		return
	}
	s.logReportEventTask(cfg.ReportID, cfg.SourceTaskID, "ai_enhanced", "agent", map[string]any{
		"job_id": job.ID, "words": words,
	})
	if _, err := s.m.PG().AppendConvActivity(cfg.ConvID, db.Activity{
		Worker: "report_writer", Kind: "text",
		Summary: fmt.Sprintf("报告 AI 撰写完成(%d 字),已覆盖写入任务总结报告;成稿前内容仍可在对话里追问。", words),
		Detail:  out,
	}); err != nil {
		log.Printf("[report-ai] conv %d 写收尾消息失败: %v", cfg.ConvID, err)
	}
	s.writeTemplateGraphNode(t, true, fmt.Sprintf("任务报告 AI 撰写完成(%d 字),已覆盖任务总结报告", words))
	_ = s.m.SetTaskStatus(t.ID, "done")
	_ = s.m.PG().TouchConversation(cfg.ConvID)
	log.Printf("[report-ai] 任务 %s 完成: 作业 %d 报告 %d(%d 字)", t.ID, job.ID, rep.ID, words)
}
