package server

// 目标审核管线(契约 GOAL_REVIEW_CONTRACTS.md §3.1)。
// reviewProve 接到 agent.GoalReviewHook(接线在 assembly.go,Agent B),prove_goal 每次提交证明时同步调用。

import (
	"context"

	"github.com/Autumn-27/artex/agent"
)

// GoalAuditorAgentKey —— 审核循环的 ToolSet worker 标签 / agents 行 key / 活动 worker。
const GoalAuditorAgentKey = "goal_auditor"

// reviewProve 是 prove_goal 的审核闸门:
//  1. 未启用(settings goal_review_enabled=false) → {Skip:true} 旧行为;
//  2. 重复短路:同 goal+evidence 最近 rejected → 原样返回(不重跑 LLM);
//  3. provider 解析(仿 taskLLMRuntime.current):agentBindingProvider(goal_auditor) → 任务链 ActiveLLMProfileID → globalProvider;
//     全不可用 → unverified 按 fail_mode 消化(open→approved+附注 / closed→rejected),并落 goal_reviews 行(fail_note);
//  4. 组装 TaskBrief + PriorRejections → agent.AuditGoal(emit 走 engine emitActivity,Worker 已由 agent 侧强制 goal_auditor);
//  5. 结论落 CreateGoalReview + 终结活动 kind="goal_review"(Metadata={review_id,goal_id,verdict,gaps});
//  6. 任何内部错误 → unverified 分支,绝不 panic 打断 planner 回合。
//
// TODO(goal-review Agent B): 按契约 §3.1 实现。
func (s *Server) reviewProve(ctx context.Context, req agent.GoalReviewRequest) (agent.GoalReviewVerdict, error) {
	_ = ctx
	_ = req
	return agent.GoalReviewVerdict{Skip: true}, nil
}

// ensureGoalAuditorAgent 启动时幂等播种 goal_auditor agents 行(仿 initC2Evasion 播种
// c2_evasion_engineer:GetAgentByKey 判空 → CreateAgent,role=assistant,stage='')。
// db/db.go 带并行 WIP 禁碰,builtinAgents 不加行。
//
// TODO(goal-review Agent B): 实现并在合适的 init/启动点调用。
func ensureGoalAuditorAgent() error {
	return nil
}
