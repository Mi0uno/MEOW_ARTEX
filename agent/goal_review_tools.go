package agent

// 目标审核专属工具(契约 GOAL_REVIEW_CONTRACTS.md §2.4,仅审核循环内部使用)。
// 工具名:goal_audit_context / goal_audit_node / goal_audit_verdict。
// 不进 builtinToolsByAgent(不走 AugmentTools 运行时解析);server 侧 SeedToolWithStage 播种绑 goal_auditor。
// 工具 Run 内对 t.ts==nil / t.taskID==0 fail-closed 报不可用。

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// auditReady reports whether this ToolSet carries the task context the audit
// tools need (exploration store + PG task id). The server-driven AuditGoal always
// sets both; the seed-only catalog shell and mis-bound callers don't.
func (t *ToolSet) auditReady() bool { return t.ts != nil && t.taskID > 0 }

// auditUnavailable is the fail-closed reply for audit tools without task context.
func auditUnavailable(tool string) (actool.Result, error) {
	return actool.Errorf(tool + " 审核上下文不可用：当前审核循环未绑定任务探索图（缺探索图或 task_id），无法读取案卷。"), nil
}

// auditContext 一次返回案卷:goal 节点全文+状态、evidence 节点(GetNodeWithSources 全来源链)、
// 该 goal 现有 proves 入边(历史证据及各自状态)、open 目标/意图计数、本任务 findings 简表。
// in.TaskBrief / in.PriorRejections 原文附后。
func (t *ToolSet) auditContext(in AuditGoalInput) actool.CoreTool {
	// 不走 readExpTool(它的 needExploration 守卫文案是通用版):审核工具按契约
	// 统一 fail-closed 报「审核上下文不可用」,ts==nil 与 taskID==0 同文案。
	return readTool("goal_audit_context", "获取目标审核案卷(任务原始需求、目标与证据全文、历史证据与结论)。", obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			if !t.auditReady() {
				return auditUnavailable("goal_audit_context")
			}
			dossier := map[string]any{
				"task_id":          t.taskID,
				"task_brief":       in.TaskBrief,
				"prior_rejections": in.PriorRejections,
			}
			// 探索图根的原始需求(description/goal)——任务原始需求的兜底来源。
			if d, g, err := t.ts.Root(); err == nil {
				dossier["exploration_root"] = map[string]any{"description": d, "goal": g}
			}
			// goal 节点全文+状态。
			goal, err := t.ts.GetNode(in.GoalID)
			if err != nil || goal == nil {
				return actool.Errorf(fmt.Sprintf("goal_audit_context：目标节点 %d 读取失败（%v）", in.GoalID, err)), nil
			}
			dossier["goal"] = goal
			// evidence 节点全来源链(哪个 worker 哪条 intent 产出)。
			ev, err := t.ts.GetNodeWithSources(in.EvidenceID)
			if err != nil || ev == nil {
				return actool.Errorf(fmt.Sprintf("goal_audit_context：证据节点 %d 读取失败（%v）", in.EvidenceID, err)), nil
			}
			_ = t.ts.PopulateFindingTrafficIDs([]*db.Node{ev})
			dossier["evidence"] = ev
			// 该 goal 现有 proves 入边:历史提交过的证据及各自状态(重复提交同证据一眼可见)。
			dossier["proves_evidence"] = t.provesEvidence(in.GoalID)
			// open 目标/意图计数(量化核对的全局分母)。
			goals, _ := t.ts.ListByKind(db.KindGoal, 1000)
			openGoals, metGoals := 0, 0
			for _, g := range goals {
				switch g.State {
				case "open":
					openGoals++
				case "met":
					metGoals++
				}
			}
			intents, _ := t.ts.ListByKind(db.KindIntent, 2000)
			openIntents := 0
			for _, n := range intents {
				if n.State == "open" {
					openIntents++
				}
			}
			dossier["goal_intent_counts"] = map[string]any{
				"goals_total": len(goals), "goals_open": openGoals, "goals_met": metGoals, "intents_open": openIntents,
			}
			// 本任务 findings 简表(id/名称/严重度/摘要/状态/产出 intent)。
			dossier["findings"] = t.findingsSummary()
			return jsonResult(dossier)
		})
}

// provesEvidence lists every node with a proves edge INTO this goal (the
// evidence submitted for it so far, whatever the verdicts were) with its state.
func (t *ToolSet) provesEvidence(goalID int64) []map[string]any {
	out := []map[string]any{}
	edges, err := t.ts.Edges(5000)
	if err != nil {
		return out
	}
	for _, e := range edges {
		if e.Rel != db.RelProves || e.To != goalID {
			continue
		}
		n, err := t.ts.GetNode(e.From)
		if err != nil || n == nil {
			continue
		}
		var p struct {
			Summary string `json:"summary"`
			Text    string `json:"text"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		sum := p.Summary
		if sum == "" {
			sum = p.Text
		}
		out = append(out, map[string]any{"id": n.ID, "kind": n.Kind, "state": n.State, "summary": sum})
	}
	return out
}

// findingsSummary is the per-task findings 简表 (id/名称/严重度/摘要/状态/产出 intent).
func (t *ToolSet) findingsSummary() []map[string]any {
	out := []map[string]any{}
	f, err := t.ts.ListByKindWithSources(db.KindFinding, 500)
	if err != nil {
		return out
	}
	intentOf, _ := t.ts.FindingIntentsWithSources() // finding id -> 产生它的 intent id
	for _, n := range f {
		m := compactFinding(n)
		var p struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		if p.Name != "" {
			m["name"] = p.Name
		}
		if iid, ok := intentOf[n.ID]; ok {
			m["intent_id"] = iid
		}
		out = append(out, m)
	}
	return out
}

// auditNode 单节点完整 payload+来源链(审核员深挖用)。
func (t *ToolSet) auditNode() actool.CoreTool {
	return readTool("goal_audit_node", "查看单个探索节点(事实/漏洞/意图)的完整内容与产出链路。", obj(map[string]any{
		"node_id": idp("节点 id"),
	}, "node_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if !t.auditReady() {
				return auditUnavailable("goal_audit_node")
			}
			var a struct {
				NodeID json.RawMessage `json:"node_id"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.NodeID)
			if id <= 0 {
				return actool.Errorf("node_id 必填"), nil
			}
			n, err := t.ts.GetNodeWithSources(id)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if n == nil {
				return actool.Errorf(fmt.Sprintf("未找到探索节点 %d", id)), nil
			}
			_ = t.ts.PopulateFindingTrafficIDs([]*db.Node{n})
			return jsonResult(n) // full payload incl. detail / evidence, plus explicit finding IDs
		})
}

// auditVerdictTo 终局工具:审核员提交结构化结论,写入 out 供 AuditGoal 返回。
// verdict 只认 approved/rejected(非法值报错要求重调);gaps 缺失→空数组;confidence clamp 0-1。
func (t *ToolSet) auditVerdictTo(out *GoalReviewVerdict) actool.CoreTool {
	return writeTool("goal_audit_verdict", "提交目标审核结论(终局动作,一次审核只提交一次)。", obj(map[string]any{
		"verdict":    map[string]any{"type": "string", "enum": []string{"approved", "rejected"}, "description": "approved=证据充分目标达成;rejected=证据不足或不符合目标语义"},
		"confidence": map[string]any{"type": "number", "description": "0-1 置信度"},
		"reasons":    str("裁定理由:证据与目标要求的逐条对应关系"),
		"gaps":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "rejected 时必填:可执行的补证方向缺口清单"},
	}, "verdict", "reasons"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if !t.auditReady() {
				return auditUnavailable("goal_audit_verdict")
			}
			if out == nil {
				return actool.Errorf("goal_audit_verdict 内部错误：结论落点未初始化"), nil
			}
			var a struct {
				Verdict    string   `json:"verdict"`
				Confidence float64  `json:"confidence"`
				Reasons    string   `json:"reasons"`
				Gaps       []string `json:"gaps"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("goal_audit_verdict 入参解析失败（" + err.Error() + "），请修正后重调"), nil
			}
			if a.Verdict != "approved" && a.Verdict != "rejected" {
				return actool.Errorf(fmt.Sprintf("verdict 只接受 \"approved\" 或 \"rejected\"，收到 %q——请重新调用 goal_audit_verdict 提交合法结论", a.Verdict)), nil
			}
			gaps := a.Gaps
			if gaps == nil {
				gaps = []string{}
			}
			conf := a.Confidence
			if conf < 0 {
				conf = 0
			}
			if conf > 1 {
				conf = 1
			}
			*out = GoalReviewVerdict{Verdict: a.Verdict, Confidence: conf, Reasons: a.Reasons, Gaps: gaps}
			note := ""
			if a.Verdict == "rejected" && len(gaps) == 0 {
				note = "（提示：rejected 时 gaps 应给出可执行的补证方向）"
			}
			return actool.Text(fmt.Sprintf("已记录审核结论：%s（置信度 %.2f）%s。审核结束，请勿再提交。", a.Verdict, conf, note)), nil
		})
}
