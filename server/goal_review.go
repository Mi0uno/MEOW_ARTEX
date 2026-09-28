package server

// 目标审核管线(契约 GOAL_REVIEW_CONTRACTS.md §3.1/§3.4)。
// reviewProve 接到 agent.GoalReviewHook(接线在 assembly.go initGoalReview),prove_goal 每次提交证明时同步调用。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"runtime/debug"
	"strings"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

// GoalAuditorAgentKey —— 审核循环的 ToolSet worker 标签 / agents 行 key / 活动 worker。
const GoalAuditorAgentKey = "goal_auditor"

// reviewProve 是 prove_goal 的审核闸门:
//  1. 未启用(settings goal_review_enabled=false) → {Skip:true} 旧行为;
//  2. 重复短路:同 goal+evidence 最近 rejected → 原样返回(不重跑 LLM、不写新行);
//  3. provider 解析(仿 taskLLMRuntime.current):agentBindingProvider(goal_auditor) → 任务链 ActiveLLMProfileID → globalProvider;
//     全不可用 → unverified 按 fail_mode 消化(open→approved+附注 / closed→rejected),并落 goal_reviews 行(fail_note);
//  4. 组装 TaskBrief + PriorRejections → agent.AuditGoal(emit 走 engine emitActivity,Worker 已由 agent 侧强制 goal_auditor);
//  5. 结论落 CreateGoalReview + 终结活动 kind="goal_review"(Metadata={review_id,goal_id,verdict,gaps});
//  6. 任何内部错误 → unverified 分支,绝不 panic 打断 planner 回合。
func (s *Server) reviewProve(ctx context.Context, req agent.GoalReviewRequest) (v agent.GoalReviewVerdict, err error) {
	// 第 6 步的兜底:闸门内部任何 panic 都不得打断 planner 回合——降级为 unverified。
	// 正常内部错误走 reviewProveInner 内的 unverified 分支(会落库+发活动);若
	// unverified 分支自身也 panic,这里只构造结论不再碰库(诚实留痕让位于不崩进程)。
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[goal-review] task %d goal %d 审核 panic: %v\n%s", req.TaskID, req.GoalID, r, debug.Stack())
			v = agent.GoalReviewVerdict{Verdict: "unverified", Gaps: []string{}, FailNote: fmt.Sprintf("审核内部错误: %v", r)}
			err = nil
		}
	}()
	return s.reviewProveInner(ctx, req)
}

func (s *Server) reviewProveInner(ctx context.Context, req agent.GoalReviewRequest) (agent.GoalReviewVerdict, error) {
	// ---- 1. 读 config:未启用 → Skip(完全旧行为,不落审核记录)。config 读失败按缺省
	// true/open/150 继续——审核开关读不到不该放大成「跳过审核」。 ----
	enabled, failMode, timeoutSec, cfgErr := s.m.PG().GoalReviewConfig()
	if cfgErr != nil {
		log.Printf("[goal-review] 读审核配置失败,按缺省(开/open/150s)继续: %v", cfgErr)
		enabled, failMode, timeoutSec = true, "open", 150
	}
	if !enabled {
		return agent.GoalReviewVerdict{Skip: true}, nil
	}

	// ---- 2. 重复短路:同 goal+evidence 最近一次 rejected → 原样返回上次内容
	// (ReviewID 指旧行,不重跑 LLM、不写新行、不再发活动),防驳回风暴烧 token。
	// approved 不短路:正常情况不会再 prove,撞上就走全流程重新留痕。 ----
	if prev, e := s.m.PG().LatestGoalReviewFor(req.GoalID, req.EvidenceID); e != nil {
		log.Printf("[goal-review] 读取历史审核失败(跳过短路,继续全流程): %v", e)
	} else if prev != nil && prev.Verdict == "rejected" {
		gaps := prev.Gaps
		if gaps == nil {
			gaps = []string{}
		}
		return agent.GoalReviewVerdict{
			Verdict: "rejected", Confidence: prev.Confidence, Reasons: prev.Reasons,
			Gaps: gaps, ReviewID: prev.ID, DurationMS: prev.DurationMS, FailNote: prev.FailNote,
		}, nil
	}

	// ---- 3. provider 解析(优先级仿 taskLLMRuntime.current):goal_auditor 的 agent
	// 绑定 → 任务链 ActiveLLMProfileID → 全局激活配置;全不可用 → unverified 分支。 ----
	prov, _, ok := s.agentBindingProvider(GoalAuditorAgentKey)
	if !ok {
		if pt, e := s.m.PG().GetTask(req.TaskID); e == nil && pt != nil && pt.ActiveLLMProfileID != nil {
			if p, _, pok := s.providerForProfile(*pt.ActiveLLMProfileID); pok {
				prov, ok = p, true
			}
		} else if e != nil {
			log.Printf("[goal-review] 读任务 %d 失败(继续解析全局 provider): %v", req.TaskID, e)
		}
	}
	if !ok {
		if p, _, gok := s.globalProvider(); gok {
			prov, ok = p, true
		}
	}
	if !ok {
		return s.goalReviewUnverified(req, failMode, "未配置可用的 LLM(goal_auditor 无绑定、任务链与全局配置均不可用)"), nil
	}

	// ---- 4. 组装 TaskBrief + PriorRejections → 独立审核循环(带超时预算)。 ----
	brief, prior := s.goalReviewContext(req)
	inMem, loaded := s.m.Task(fmt.Sprint(req.TaskID))
	if !loaded || inMem == nil || inMem.Store == nil {
		return s.goalReviewUnverified(req, failMode, "任务探索图未加载,审核上下文不可用"), nil
	}
	actx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()
	emit := func(a db.Activity) {
		if s.engine != nil {
			s.engine.emitActivity(inMem, a) // Worker 由 agent 侧强制 goal_auditor
		}
	}
	start := time.Now()
	v, auditErr := agent.AuditGoal(actx, prov, agent.AuditGoalInput{
		TaskID: req.TaskID, GoalID: req.GoalID, EvidenceID: req.EvidenceID,
		Reason: req.Reason, TaskBrief: brief, PriorRejections: prior,
	}, inMem.Store, emit)
	if auditErr != nil {
		return s.goalReviewUnverified(req, failMode, "审核循环失败: "+auditErr.Error()), nil
	}
	if v.Verdict != "approved" && v.Verdict != "rejected" {
		// 审核员未提交结论等诚实失败:AuditGoal 返回 unverified,这里按 fail_mode 消化。
		note := v.FailNote
		if note == "" {
			note = "审核员未提交结论"
		}
		if v.DurationMS == 0 {
			v.DurationMS = time.Since(start).Milliseconds()
		}
		return s.consumeUnverified(req, failMode, note, v), nil
	}
	if v.Gaps == nil {
		v.Gaps = []string{}
	}
	if v.DurationMS == 0 {
		v.DurationMS = time.Since(start).Milliseconds()
	}

	// ---- 5. 结论落库 + 终结活动。落库失败只记日志(结论照常回给 planner,不截断回合)。 ----
	row := &db.GoalReview{
		TaskID: req.TaskID, GoalID: req.GoalID, EvidenceID: req.EvidenceID,
		Reason: req.Reason, Verdict: v.Verdict, Confidence: v.Confidence,
		Reasons: v.Reasons, Gaps: v.Gaps, Reviewer: GoalAuditorAgentKey,
		DurationMS: v.DurationMS, FailNote: v.FailNote,
	}
	if id, e := s.m.PG().CreateGoalReview(row); e != nil {
		log.Printf("[goal-review] 审核结论落库失败(仅日志,不打断 planner): %v", e)
	} else {
		v.ReviewID = id
	}
	s.emitGoalReviewActivity(req, v)
	return v, nil
}

// goalReviewUnverified 审核系统不可用的统一出口:按 fail_mode 消化成对外结论,
// 落 verdict='unverified' 行(fail_note 记原因)并发终结活动。
func (s *Server) goalReviewUnverified(req agent.GoalReviewRequest, failMode, reason string) agent.GoalReviewVerdict {
	return s.consumeUnverified(req, failMode, reason, agent.GoalReviewVerdict{})
}

// consumeUnverified 把一次 unverified(AuditGoal 返回或内部错误)消化为最终结论:
// open → approved(FailNote 附注放行原因);closed → rejected(Reasons 说明防误判)。
// 两种都落 verdict='unverified' 行。
func (s *Server) consumeUnverified(req agent.GoalReviewRequest, failMode, reason string, base agent.GoalReviewVerdict) agent.GoalReviewVerdict {
	out := agent.GoalReviewVerdict{Gaps: []string{}}
	if failMode == "closed" {
		out.Verdict = "rejected"
		out.Reasons = "审核系统暂不可用(" + reason + "),为防误判不标记 met;请稍后重试或补充更完整的证据"
		out.FailNote = reason
	} else {
		out.Verdict = "approved"
		out.FailNote = "审核系统不可用(" + reason + "),按配置放行"
	}
	out.Confidence = base.Confidence
	out.DurationMS = base.DurationMS
	id, err := s.m.PG().CreateGoalReview(&db.GoalReview{
		TaskID: req.TaskID, GoalID: req.GoalID, EvidenceID: req.EvidenceID,
		Reason: req.Reason, Verdict: "unverified",
		Reasons: out.Reasons, Gaps: []string{}, Reviewer: GoalAuditorAgentKey,
		DurationMS: out.DurationMS, FailNote: reason,
	})
	if err != nil {
		log.Printf("[goal-review] unverified 留痕落库失败(仅日志): %v", err)
	} else {
		out.ReviewID = id
	}
	s.emitGoalReviewActivity(req, out)
	return out
}

// emitGoalReviewActivity 发审核终结活动(契约 §3.1 第 5 步):Worker=goal_auditor、
// Kind=goal_review、NodeID 不设、Metadata={review_id,goal_id,verdict,gaps}。
func (s *Server) emitGoalReviewActivity(req agent.GoalReviewRequest, v agent.GoalReviewVerdict) {
	if s.engine == nil {
		return
	}
	inMem, loaded := s.m.Task(fmt.Sprint(req.TaskID))
	if !loaded || inMem == nil {
		return // 任务不在内存(如归档后),无处挂活动;审核行本身已留痕
	}
	verb := "审核通过"
	switch v.Verdict {
	case "rejected":
		verb = "驳回"
	case "unverified":
		verb = "未验证"
	}
	summary := verb + ":" + firstLine(v.Reasons, 160)
	if summary == verb+":" && v.FailNote != "" {
		summary = verb + ":" + firstLine(v.FailNote, 160)
	}
	meta, _ := json.Marshal(map[string]any{
		"review_id": v.ReviewID, "goal_id": req.GoalID, "verdict": v.Verdict, "gaps": v.Gaps,
	})
	s.engine.emitActivity(inMem, db.Activity{
		Worker: GoalAuditorAgentKey, Kind: "goal_review",
		Summary: summary, Metadata: meta,
	})
}

// goalReviewContext 组装审核循环的静态上下文(契约 §3.1 第 4 步):
// TaskBrief=任务原始需求(名称/描述/任务目标/阶段/探索图根目标),
// PriorRejections=该 goal 历史驳回摘要(最多 5 条,可空)。
func (s *Server) goalReviewContext(req agent.GoalReviewRequest) (brief, prior string) {
	var b strings.Builder
	b.WriteString("## 任务原始需求\n")
	if bt, e := s.m.PG().TaskGoalBrief(req.TaskID); e != nil {
		b.WriteString("(任务简报读取失败: " + e.Error() + ")\n")
	} else if bt != nil {
		name := bt.Name
		if name == "" {
			name = "(未命名)"
		}
		fmt.Fprintf(&b, "任务名称:%s\n任务描述:%s\n任务目标:%s\n阶段:%s\n探索图根目标:%s\n",
			name, bt.Description, bt.Goal, bt.Stage, bt.ExpGoal)
	} else {
		b.WriteString("(任务不存在或已删除)\n")
	}
	b.WriteString("\n## 待复核目标\n")
	fmt.Fprintf(&b, "goal #%d:%s\n", req.GoalID, req.GoalText)
	fmt.Fprintf(&b, "证据节点 #%d\nplanner 的达标主张:%s\n", req.EvidenceID, req.Reason)
	brief = b.String()

	// 历史驳回摘要:只取该 goal 的 rejected(防重复提交的上下文),最近的在前。
	rows, _, err := s.m.PG().ListGoalReviews(db.GoalReviewFilter{TaskID: req.TaskID, GoalID: req.GoalID, Limit: 20})
	if err != nil {
		return brief, ""
	}
	var lines []string
	for _, r := range rows {
		if r.Verdict != "rejected" {
			continue
		}
		line := "- " + firstLine(r.Reasons, 200)
		if len(r.Gaps) > 0 {
			line += "(缺口:" + strings.Join(r.Gaps, ";") + ")"
		}
		lines = append(lines, line)
		if len(lines) >= 5 {
			break
		}
	}
	if len(lines) > 0 {
		prior = "该目标此前被驳回过(勿重复提交同一证据):\n" + strings.Join(lines, "\n")
	}
	return brief, prior
}

// ---- 启动时播种(契约 §3.4) ----

// ensureGoalAuditorAgent 启动时幂等播种 goal_auditor agents 行(仿 initC2Evasion 播种
// c2_evasion_engineer:GetAgentByKey 判空 → CreateAgent,role=assistant,stage=”)。
// db/db.go 带并行 WIP 禁碰,builtinAgents 不加行。
func ensureGoalAuditorAgent(pg *db.DB) error {
	a, err := pg.GetAgentByKey(GoalAuditorAgentKey)
	if err != nil {
		return err
	}
	if a != nil {
		return nil
	}
	created, err := pg.CreateAgent(GoalAuditorAgentKey, "目标审核",
		"目标审核 agent:对 planner 提交的 prove_goal 做独立复核——分析任务原始需求、确认目标语义、核对证据链与覆盖度,批准或驳回 met 标记并给出缺口。", "")
	if err != nil {
		return err
	}
	// New() 里 seedPrompts 先于本播种跑,首建行赶不上那轮;这里补一次首插
	// (SeedPromptIfEmpty 只空时写,提示词正文由 agent 包注册进 BuiltinPromptSeeds 后生效)。
	if created != nil {
		if tmpl := agent.BuiltinPromptSeeds()[GoalAuditorAgentKey]; tmpl != "" {
			if err := pg.SeedPromptIfEmpty(created.ID, tmpl); err != nil {
				log.Printf("[goal-review] goal_auditor 提示词首插失败: %v", err)
			}
		}
	}
	return nil
}

// seedGoalAuditTools 播种审核循环的三个内部工具(契约 §2.4):真身在 agent 包,
// tools 表只管展示与文案;绑 goal_auditor、stage 通用,首插 only(ON CONFLICT DO
// NOTHING)不覆盖用户改过的描述。不进 builtinToolsByAgent,不走运行时 AugmentTools。
func seedGoalAuditTools(pg *db.DB) {
	bindings, _ := json.Marshal([]string{GoalAuditorAgentKey})
	auditAgents := bindings
	seeds := []struct {
		key, desc, schema string
	}{
		{GoalAuditorAgentKey + "_context", "读取目标审核案卷:goal 节点全文+状态、evidence 全来源链、该 goal 现有 proves 入边、open 目标/意图计数、本任务 findings 简表,附任务原始需求与历史驳回摘要。审核第一步先调它。",
			`{"type":"object","properties":{},"additionalProperties":false}`},
		{GoalAuditorAgentKey + "_node", "读取单个探索节点的完整 payload 与来源链(审核员深挖证据时用)。",
			`{"type":"object","properties":{"node_id":{"type":"integer","description":"探索节点 id"}},"required":["node_id"],"additionalProperties":false}`},
		{GoalAuditorAgentKey + "_verdict", "提交目标审核终局结论(每次审核必须且只能调一次):verdict=approved|rejected,confidence 0-1,reasons 必填,gaps 为驳回时的可执行补证方向数组。",
			`{"type":"object","properties":{"verdict":{"type":"string","enum":["approved","rejected"],"description":"approved=证据链支持 met;rejected=不支持"},"confidence":{"type":"number","minimum":0,"maximum":1},"reasons":{"type":"string","description":"结论理由(对照任务原始需求逐条说明)"},"gaps":{"type":"array","items":{"type":"string"},"description":"驳回时的缺口清单"}},"required":["verdict","confidence","reasons"],"additionalProperties":false}`},
	}
	for _, sd := range seeds {
		if err := pg.SeedToolWithStage(sd.key, sd.desc, json.RawMessage(sd.schema), auditAgents, ""); err != nil {
			log.Printf("[goal-review] seed 工具 %s 失败: %v", sd.key, err)
		}
	}
}
