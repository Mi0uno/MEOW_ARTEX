package server

// 报告中心流水线任务的统一分发口(模板识别之外的 report 阶段任务都从这里进):
//
//	step_key=report_export   → AI 按模板导出(server/report_export_task.go)
//	step_key=task_report_ai  → 任务总结报告 AI 撰写(server/report_ai_task.go)
//
// 调用方两处,都带 beginTaskOperation 包装:goals.go 的统一 launch 链(创建路径)
// 与 restoreTaskRuntimes(重启恢复)。AI 导出/AI 撰写的启动入口(startXXXTask)在
// 建完任务后也直接调本函数,与 templateRunner 的 startTemplateAnalyzeTask 同构。
//
// 三类 runner 共同语义:不进 agent 引擎、不占 agent 并发槽(occupiesConcurrencySlot
// 对 stage=report 豁免)、过程写 report_writer 监看会话(报告中心对话可见)、
// 产出整体覆盖写入(重启重跑幂等)。
//
// 成本护栏:reportGenSem(容量 2)是「报告生成类 LLM 长任务」的全局并发上限——
// 导出分章与 AI 撰写都是重 LLM 输出,超出的任务按创建顺序在信号量上自然排队。

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/Autumn-27/norma/llm"
)

// reportGenSem 报告生成的全局并发闸(容量=同时最多 2 个重 LLM 报告任务)。
// 由 launchReportPipelineTask 的执行协程获取、退出释放;等待不占 agent 并发槽。
var reportGenSem = make(chan struct{}, 2)

// isTransientLLMError 判断 LLM 错误是否值得重试:限流/超时/网络抖动/服务端 5xx/过载。
// 401/403/400 这类语义错误(鉴权/配置/参数)重试无意义,必须放行走快速失败路径。
// 实测案例:上传自动识别撞 anthropic 429(rate_limit_error)一次即判失败——自动链路
// 无人值守,这类瞬态失败应自愈而不是甩给用户。
func isTransientLLMError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, kw := range []string{
		"status 429", "status 500", "status 502", "status 503", "status 504", "status 529",
		"rate limit", "ratelimit", "限流", "overloaded",
		"context deadline", "timeout", "timed out",
		"connection reset", "connection refused", "broken pipe", "unexpected eof", "no such host",
	} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}

// analyzeWithRetry 直连 LLM 生成,对瞬态错误自动退避重试(20s、40s…)。
// perAttempt 是每次尝试的独立预算;retries 是额外重试次数;ctx 取消立即终止。
// 三个报告 runner(模板识别/AI 导出分章/AI 润写)统一走这里。
func analyzeWithRetry(ctx context.Context, prov llm.Provider, system, user string, retries int, perAttempt time.Duration) (string, error) {
	var out string
	var err error
	for attempt := 0; ; attempt++ {
		genCtx, cancel := context.WithTimeout(ctx, perAttempt)
		out, err = analyzeCompletion(genCtx, prov, system, user)
		cancel()
		if err == nil || ctx.Err() != nil || attempt >= retries || !isTransientLLMError(err) {
			return out, err
		}
		backoff := time.Duration(20*(attempt+1)) * time.Second
		log.Printf("[report-pipeline] LLM 瞬态失败(%s),%s 后重试(%d/%d)", firstLine(err.Error(), 160), backoff, attempt+1, retries)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return out, err
		}
	}
}

// backfillReportPipelineScope 存量报告生成任务的企业归属回填(幂等,只补缺失):
// 「继承报告企业归属」上线前创建的 AI 导出/AI 润写任务没有 task_scope company 行,
// 报告中心任务页按企业过滤时不可见。启动时跑一次,量小(仅 report 阶段流水线任务)。
func (s *Server) backfillReportPipelineScope() {
	n, err := s.m.PG().BackfillReportJobTaskScope()
	if err != nil {
		log.Printf("[report-pipeline] 企业归属回填失败(下次启动重试): %v", err)
		return
	}
	if n > 0 {
		log.Printf("[report-pipeline] 已为 %d 条存量报告生成任务补企业归属", n)
	}
}

// launchReportPipelineTask 按 step_key 分发;未知步骤置任务失败(数据异常可见可查)。
// 幂等:同一任务重复进入只跑一次(register 失败即返回,重启恢复同路径)。
func (s *Server) launchReportPipelineTask(t *Task) {
	bindReportRouteServer(s) // HTTP 路由层需要 Server 引用(见 report_export_api.go)
	taskID, err := taskNumericIDErr(t.ID)
	if err != nil {
		log.Printf("[report-pipeline] 非法任务 id %q,置为失败", t.ID)
		_ = s.m.SetTaskStatus(t.ID, "failed")
		return
	}
	switch t.StepKey {
	case "report_export", "task_report_ai":
		// 下面统一启动
	default:
		log.Printf("[report-pipeline] 未知的 report 步骤 %q(任务 %s),置为失败", t.StepKey, t.ID)
		_ = s.m.SetTaskStatus(t.ID, "failed")
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	if !reportRunner.register(taskID, cancel) {
		cancel()
		return // 已在跑
	}
	go func() {
		defer reportRunner.unregister(taskID)
		reportGenSem <- struct{}{}
		defer func() { <-reportGenSem }()
		if t.StepKey == "report_export" {
			s.runReportExportTask(ctx, t)
		} else {
			s.runTaskReportAITask(ctx, t)
		}
	}()
}
