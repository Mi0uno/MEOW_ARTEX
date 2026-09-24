package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"sync"

	"github.com/Autumn-27/artex/recon"
)

// reconRunner 管理信息搜集流水线任务的执行生命周期。
// 与 agent 引擎完全独立:不走 planner/worker、不占并发槽、不过审批门。
type reconRunner struct {
	mu      sync.Mutex
	cancels map[int64]context.CancelFunc // taskID -> 运行中流水线的取消函数
	sink    recon.RawSink                // evidence blob 注入
}

func newReconRunner(sink recon.RawSink) *reconRunner {
	return &reconRunner{cancels: map[int64]context.CancelFunc{}, sink: sink}
}

// running reports whether a pipeline is live for the task.
func (rr *reconRunner) running(taskID int64) bool {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	_, ok := rr.cancels[taskID]
	return ok
}

// stop cancels a live pipeline; returns false when none is running.
func (rr *reconRunner) stop(taskID int64) bool {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if cancel, ok := rr.cancels[taskID]; ok {
		cancel()
		return true
	}
	return false
}

func (rr *reconRunner) register(taskID int64, cancel context.CancelFunc) bool {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if _, ok := rr.cancels[taskID]; ok {
		return false
	}
	rr.cancels[taskID] = cancel
	return true
}

func (rr *reconRunner) unregister(taskID int64) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	delete(rr.cancels, taskID)
}

// reconSink 把收集器原始输出落成 evidence blob(内容寻址,GC 感知:
// task_collector_runs.raw_evidence_hash 引用的 blob 不会被回收)。
func (s *Server) reconSink(data []byte) (string, error) {
	return s.evidenceStore().PutBlob(bytes.NewReader(data))
}

// companyTargets 把任务关联的企业解析成收集器输入:企业 scope(kind=domain)里的
// 根域名列表。没有域名的企业保留 ID(手动录入不需要域名)。
func (s *Server) companyTargets(companyIDs []int64) ([]recon.CompanyTarget, error) {
	out := make([]recon.CompanyTarget, 0, len(companyIDs))
	for _, id := range companyIDs {
		target := recon.CompanyTarget{ID: id}
		if company, err := s.m.PG().Companies().GetCompany(id); err == nil && company != nil {
			target.Name = company.Name
		}
		rules, err := s.m.PG().Companies().GetScope(id)
		if err != nil {
			return nil, fmt.Errorf("读取企业 #%d 范围失败: %w", id, err)
		}
		for _, r := range rules {
			if r.Kind == "domain" && r.Domain != "" {
				target.Domains = append(target.Domains, r.Domain)
			}
		}
		out = append(out, target)
	}
	return out, nil
}

// launchReconTask 异步跑一条信息搜集流水线。可从任意阶段安全调用:
// running 状态的任务不会重复启动(幂等)。
func (s *Server) launchReconTask(t *Task) {
	taskID, parseErr := strconv.ParseInt(t.ID, 10, 64)
	if parseErr != nil || taskID <= 0 {
		log.Printf("[recon] 非法任务 id %q,跳过", t.ID)
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	// 出网代理按任务解析后挂进 ctx:流水线里的收集器(16+ 家 provider + crt.sh +
	// 字典爆破)都从 ctx 读它,无需逐个改造签名。
	ctx = recon.WithProxy(ctx,
		s.m.ProxyAddrForTask(t.ProxyMode, t.ProxyGroupID),
		s.m.ProxyCACertForTask(t.ProxyMode, t.ProxyGroupID))
	if !s.recon.register(taskID, cancel) {
		cancel()
		return // 已在跑
	}
	go func() {
		defer s.recon.unregister(taskID)
		s.runReconPipeline(ctx, t, taskID)
	}()
}

func (s *Server) runReconPipeline(ctx context.Context, t *Task, taskID int64) {
	// 流水线解析优先级:step_key(单步) > pipeline(多步编排) > 自动全量(默认)。
	var pipe *recon.Pipeline
	if t.StepKey != "" {
		collector := recon.Get(t.StepKey)
		if collector == nil {
			log.Printf("[recon] task %s: 未知步骤 %q", t.ID, t.StepKey)
			_ = s.m.SetTaskStatus(t.ID, "failed")
			return
		}
		var cfg map[string]any
		if len(t.StepConfig) > 0 {
			_ = json.Unmarshal(t.StepConfig, &cfg)
		}
		pipe = &recon.Pipeline{Collectors: []recon.PipelineStep{{Key: t.StepKey, Enabled: boolPtr(true), Config: cfg}}}
	} else if len(t.Pipeline) > 0 {
		var err error
		pipe, err = recon.ParsePipeline(t.Pipeline)
		if err != nil {
			log.Printf("[recon] task %s: 流水线无效: %v", t.ID, err)
			_ = s.m.SetTaskStatus(t.ID, "failed")
			return
		}
	} else {
		// 默认:自动检测可用数据源并全量执行——只纳入「已配置且已启用」的外部接口,
		// search 型数据源的查询语句由 AI 依企业资产范围生成(失败回退内置默认)。
		// 未配置的数据源跳过而不是让整条流水线失败:搜集范围由可用性决定。
		// 跳过 manual(手动录入需要人给文本,不适合自动跑)。
		plan, planErr := s.buildReconPlan(ctx, t.CompanyIDs, s.reconPlanProfileID(t), true)
		if planErr != nil {
			log.Printf("[recon] task %s: 自动检测数据源失败: %v", t.ID, planErr)
			_ = s.m.SetTaskStatus(t.ID, "failed")
			return
		}
		pipe = &recon.Pipeline{}
		for _, sp := range plan.Sources {
			if sp.SkipReason != "" {
				continue
			}
			cfg := map[string]any{"external_api_id": sp.ExternalAPI}
			if sp.Domain != "" {
				cfg["domain"] = sp.Domain
			}
			if sp.Query != "" {
				cfg["query"] = sp.Query
			}
			pipe.Collectors = append(pipe.Collectors, recon.PipelineStep{Key: sp.Provider, Enabled: boolPtr(true), Config: cfg})
		}
		if len(pipe.Collectors) == 0 {
			log.Printf("[recon] task %s: 没有可用的数据源(需在「外部接口」配置并启用)", t.ID)
			_ = s.m.SetTaskStatus(t.ID, "failed")
			return
		}
		log.Printf("[recon] task %s: 自动检测模式,%d 个可用数据源(AI 生成 query=%v),%d 个未配置跳过",
			t.ID, len(pipe.Collectors), plan.AIUsed, plan.Skipped)
	}
	targets, err := s.companyTargets(t.CompanyIDs)
	if err != nil {
		log.Printf("[recon] task %s: %v", t.ID, err)
		_ = s.m.SetTaskStatus(t.ID, "failed")
		return
	}
	if err := s.m.SetTaskStatus(t.ID, "running"); err != nil {
		log.Printf("[recon] task %s: 置 running 失败: %v", t.ID, err)
		return
	}
	// 运行时注入:引用外部接口的步骤解密凭据;引用字典的步骤注入字典路径。
	for i := range pipe.Collectors {
		secrets, err := s.resolveExternalSecrets(pipe.Collectors[i].Key, pipe.Collectors[i].Config)
		if err != nil {
			log.Printf("[recon] task %s: %v", t.ID, err)
			_ = s.m.SetTaskStatus(t.ID, "failed")
			return
		}
		pipe.Collectors[i].Secrets = secrets
		if err := s.resolveWordbookPath(pipe.Collectors[i].Config); err != nil {
			log.Printf("[recon] task %s: %v", t.ID, err)
			_ = s.m.SetTaskStatus(t.ID, "failed")
			return
		}
	}
	log.Printf("[recon] task %s: 步骤任务启动,%d 个企业目标", t.ID, len(targets))
	runner := &recon.Runner{DB: s.m.PG(), Sink: s.recon.sink}
	result, runErr := runner.Run(ctx, taskID, targets, pipe)
	if runErr != nil && ctx.Err() == nil {
		log.Printf("[recon] task %s: 流水线执行错误: %v", t.ID, runErr)
		_ = s.m.SetTaskStatus(t.ID, "failed")
		return
	}
	if ctx.Err() != nil {
		// 用户停止/服务退出:落 failed(可重跑),不标 done。
		_ = s.m.SetTaskStatus(t.ID, "failed")
		return
	}
	// 部分收集器失败 = done_with_errors 语义:任务落 done,失败数由 collector-runs 展示;
	// 全部启用收集器都失败才落 failed。
	if result.Total > 0 && result.Done == 0 {
		_ = s.m.SetTaskStatus(t.ID, "failed")
		return
	}
	_ = s.m.SetTaskStatus(t.ID, "done")
	// 步骤任务专属链路图:目标(起点) → 步骤 → 发现 → 汇总,recon 专属节点类型。
	s.writeReconGraph(t, pipe, result)
	pending, _, _, _ := s.m.PG().CountStagedAssets(taskID)
	log.Printf("[recon] task %s: 流水线完成: %d/%d 收集器成功, 暂存 %d 条(累计待采纳 %d)",
		t.ID, result.Done, result.Total, result.Staged, pending)
}

// writeReconGraph 把一次执行写成 recon 专属节点链路:
// 起点(fact/origin,复用) --spawns--> recon_step --yields--> recon_finding …
// recon_summary --derived_from--> recon_step。节点类型是 recon 阶段专属,
// 与渗透阶段的 意图/事实/漏洞 图完全分型。
func (s *Server) writeReconGraph(t *Task, pipe *recon.Pipeline, result *recon.RunResult) {
	store := s.m.PG().Exploration(t.ExpID)
	origin, err := store.OriginFactID()
	if err != nil || origin <= 0 {
		return // 无起点节点(理论不发生:建任务必 seed origin fact)
	}
	runs, err := s.m.PG().ListCollectorRuns(taskNumericID(t.ID))
	if err != nil {
		return
	}
	var summaryStepIDs []int64
	for _, run := range runs {
		if run.Status == "skipped" {
			continue
		}
		collector := recon.Get(run.Collector)
		title := run.Collector
		if collector != nil {
			title = collector.Title()
		}
		stepState := "done"
		if run.Status == "failed" {
			stepState = "failed"
		}
		stepPayload := map[string]any{
			"collector": run.Collector,
			"title":     title,
			"status":    run.Status,
			"error":     run.Error,
			"raw_hash":  run.RawEvidenceHash,
			"stats":     json.RawMessage(run.Stats),
		}
		stepID, err := store.AddNode("recon_step", stepPayload, 5, stepState, "recon", nil)
		if err != nil {
			continue
		}
		_ = store.Link(origin, "spawns", stepID)
		summaryStepIDs = append(summaryStepIDs, stepID)
		// 发现节点:每条暂存资产一个(封顶 60,超出折叠进 stats 防大任务图爆炸)。
		assets, err := s.m.PG().ListStagedAssetsByRun(run.ID, 60)
		if err != nil {
			continue
		}
		for _, a := range assets {
			findingPayload := map[string]any{
				"type":   a.Type,
				"value":  a.Value,
				"port":   a.Port,
				"source": run.Collector,
			}
			fid, err := store.AddNode("recon_finding", findingPayload, 3, "confirmed", "recon", nil)
			if err == nil {
				_ = store.Link(stepID, "yields", fid)
			}
		}
	}
	summaryPayload := map[string]any{
		"batch":   result.BatchID,
		"total":   result.Total,
		"done":    result.Done,
		"failed":  result.Failed,
		"staged":  result.Staged,
		"summary": fmt.Sprintf("批次 %d:收集器 %d/%d 成功,新暂存 %d 条", result.BatchID, result.Done, result.Total, result.Staged),
	}
	if summaryID, err := store.AddNode("recon_summary", summaryPayload, 4, "active", "recon", nil); err == nil {
		for _, stepID := range summaryStepIDs {
			_ = store.Link(summaryID, "derived_from", stepID)
		}
	}
}

func boolPtr(v bool) *bool { return &v }

// rerunReconTask 从终态/初始态重跑一次流水线(幂等 upsert,产出进新批次)。
func (s *Server) rerunReconTask(t *Task) error {
	taskID, err := strconv.ParseInt(t.ID, 10, 64)
	if err != nil {
		return fmt.Errorf("非法任务 id")
	}
	if s.recon.running(taskID) {
		return fmt.Errorf("信息搜集任务正在运行中")
	}
	lifecycle := t.lifecycleSnapshot()
	if lifecycle.Paused {
		return fmt.Errorf("任务处于暂停状态,无法重跑")
	}
	s.launchReconTask(t)
	return nil
}

// stopReconTask 取消运行中的流水线(任务落 failed,可重跑)。
func (s *Server) stopReconTask(t *Task) bool {
	taskID, err := strconv.ParseInt(t.ID, 10, 64)
	if err != nil {
		return false
	}
	return s.recon.stop(taskID)
}
