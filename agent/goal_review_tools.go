package agent

// 目标审核专属工具(契约 GOAL_REVIEW_CONTRACTS.md §2.4,仅审核循环内部使用)。
// 工具名:goal_audit_context / goal_audit_node / goal_audit_verdict。
// 不进 builtinToolsByAgent(不走 AugmentTools 运行时解析);server 侧 SeedToolWithStage 播种绑 goal_auditor。
// 工具 Run 内对 t.ts==nil / t.taskID==0 fail-closed 报不可用。

import (
	"context"
	"encoding/json"

	actool "github.com/Autumn-27/norma/tool"
)

// auditContext 一次返回案卷:goal 节点全文+状态、evidence 节点(GetNodeWithSources 全来源链)、
// 该 goal 现有 proves 入边(历史证据及各自状态)、open 目标/意图计数、本任务 findings 简表。
// in.TaskBrief / in.PriorRejections 原文附后。
//
// TODO(goal-review Agent A): 按契约 §2.4 实现。
func (t *ToolSet) auditContext(in AuditGoalInput) actool.CoreTool {
	_ = in
	return t.readExpTool("goal_audit_context", "获取目标审核案卷(任务原始需求、目标与证据全文、历史证据与结论)。", obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			return actool.Errorf("goal_audit_context 尚未接入"), nil
		})
}

// auditNode 单节点完整 payload+来源链(审核员深挖用)。
//
// TODO(goal-review Agent A): 按契约 §2.4 实现。
func (t *ToolSet) auditNode() actool.CoreTool {
	return t.readExpTool("goal_audit_node", "查看单个探索节点(事实/漏洞/意图)的完整内容与产出链路。", obj(map[string]any{
		"node_id": idp("节点 id"),
	}, "node_id"),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			return actool.Errorf("goal_audit_node 尚未接入"), nil
		})
}

// auditVerdictTo 终局工具:审核员提交结构化结论,写入 out 供 AuditGoal 返回。
//
// TODO(goal-review Agent A): 按契约 §2.4 实现(verdict 非法值报错要求重调;gaps 缺失→空数组)。
func (t *ToolSet) auditVerdictTo(out *GoalReviewVerdict) actool.CoreTool {
	_ = out
	return writeTool("goal_audit_verdict", "提交目标审核结论(终局动作,一次审核只提交一次)。", obj(map[string]any{
		"verdict":    map[string]any{"type": "string", "enum": []string{"approved", "rejected"}, "description": "approved=证据充分目标达成;rejected=证据不足或不符合目标语义"},
		"confidence": map[string]any{"type": "number", "description": "0-1 置信度"},
		"reasons":    str("裁定理由:证据与目标要求的逐条对应关系"),
		"gaps":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "rejected 时必填:可执行的补证方向缺口清单"},
	}, "verdict", "reasons"),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			return actool.Errorf("goal_audit_verdict 尚未接入"), nil
		})
}
