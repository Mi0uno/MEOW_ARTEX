package agent

// 目标审核(契约 GOAL_REVIEW_CONTRACTS.md):prove_goal 闸门的独立复核。
// 本文件持有钩子类型与审核循环;接线在 server/assembly.go(Agent B),闸门行为在 tools.go proveGoal(Agent A)。

import (
	"context"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

// GoalReviewRequest —— prove_goal 发起的独立复核请求。
type GoalReviewRequest struct {
	TaskID     int64
	GoalID     int64
	GoalText   string // 目标节点文本
	EvidenceID int64
	Reason     string // planner 的达标主张
}

// GoalReviewVerdict —— 审核结论。proveGoal 只认 approved/rejected 两态:
// unverified 由服务端按 fail_mode 消化成 approved(放行附注)或 rejected(防误判拒标)。
type GoalReviewVerdict struct {
	Verdict    string   `json:"verdict"` // approved | rejected | unverified
	Confidence float64  `json:"confidence"`
	Reasons    string   `json:"reasons"`
	Gaps       []string `json:"gaps"` // 永不为 nil
	ReviewID   int64    `json:"review_id"`
	DurationMS int64    `json:"duration_ms"`
	FailNote   string   `json:"fail_note"` // unverified 原因 / 放行附注
	Skip       bool     `json:"-"`         // true=未启用审核,走完全旧行为(不落审核记录)
}

// GoalReviewHook 由 server 在启动时接线(server/assembly.go);nil=未接线,prove_goal 旧行为。
var GoalReviewHook func(ctx context.Context, req GoalReviewRequest) (GoalReviewVerdict, error)

// AuditGoalInput —— 一次审核循环的输入。TaskBrief/PriorRejections 由 server 组装(纯文本)。
type AuditGoalInput struct {
	TaskID          int64
	GoalID          int64
	EvidenceID      int64
	Reason          string // planner 主张
	TaskBrief       string // 任务原始需求(input/描述/总目标/阶段)
	PriorRejections string // 该 goal 历史驳回摘要(可空)
	NonStreaming    bool
	MaxTokens       int
}

// AuditGoal 跑独立审核循环(仿 DecomposeGoalsWithProvider:captureRun + ToolSet{worker:"goal_auditor"},
// ModeBypass,MaxTurns 6),emit 的每条活动强制 Worker="goal_auditor"。
// 返回审核员的结论;审核员未调 goal_audit_verdict → 返回 unverified(诚实失败,不编造)。
//
// TODO(goal-review Agent A): 按契约 §2.3 实现——系统提示词 goalAuditorDefaultTmpl(注册进
// promptcatalog key=goal_auditor),工具 auditContext/auditNode/auditVerdictTo,结论从
// audit_verdict 落 *GoalReviewVerdict 读取。
func AuditGoal(ctx context.Context, prov llm.Provider, in AuditGoalInput, ts *db.ExplorationStore, emit func(db.Activity)) (GoalReviewVerdict, error) {
	_ = ctx
	_ = prov
	_ = in
	_ = ts
	_ = emit
	return GoalReviewVerdict{Verdict: "unverified", Gaps: []string{}, FailNote: "审核循环未接入"}, nil
}
