package agent

// 目标审核闸门与审核循环的测试(契约 GOAL_REVIEW_CONTRACTS.md 验收 §6.2)。
// - 纯逻辑分支(verdict→文案映射、verdict 工具解析、无 provider/无结论的诚实失败)不依赖 PG。
// - prove_goal 闸门分支(approved/rejected/Skip/unverified-closed/hook 异常)需要真库,
//   ARTEX_PG_DSN 未设置时跳过(必须显式指向隔离演示库,如 5499;严禁连 5432 运行实例)。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
)

// --- 纯逻辑:驳回文案映射 ---

func TestGoalRejectionTextMapping(t *testing.T) {
	cases := []struct {
		name   string
		goalID int64
		v      GoalReviewVerdict
		want   []string // 依次断言包含
	}{
		{
			name:   "reasons+gaps",
			goalID: 42,
			v:      GoalReviewVerdict{Verdict: "rejected", Reasons: "证据对象是 test2 实例，与 lnut 生产目标不一致", Gaps: []string{"补 lnut 生产实例的验证", "补管理员后台截图"}},
			want:   []string{"目标审核驳回（goal 42 保持 open）", "证据对象是 test2 实例", "补 lnut 生产实例的验证，补管理员后台截图", "勿重复提交同一证据"},
		},
		{
			name:   "reasons缺失回退FailNote(unverified-closed消化)",
			goalID: 7,
			v:      GoalReviewVerdict{Verdict: "rejected", FailNote: "审核系统暂不可用，为防误判不标记 met"},
			want:   []string{"目标审核驳回（goal 7 保持 open）", "审核系统暂不可用，为防误判不标记 met", "缺口：无"},
		},
		{
			name:   "全空兜底",
			goalID: 1,
			v:      GoalReviewVerdict{Verdict: "rejected"},
			want:   []string{"目标审核驳回（goal 1 保持 open）", "审核员未给出理由", "缺口：无"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := goalRejectionText(tc.goalID, tc.v)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Fatalf("文案缺 [%s]:\n%s", w, got)
				}
			}
		})
	}
}

// --- 纯逻辑:goal_audit_verdict 工具解析 ---

func TestAuditVerdictToolParsing(t *testing.T) {
	// 审核循环恒有 ts/taskID;verdict 工具只读门标志不触库,零值 store 即可。
	tsx := &ToolSet{ts: &db.ExplorationStore{}, taskID: 7}
	call := func(t *testing.T, tsx *ToolSet, in string) (actool.Result, *GoalReviewVerdict) {
		t.Helper()
		var out GoalReviewVerdict
		res, err := tsx.auditVerdictTo(&out).Call(context.Background(), json.RawMessage(in), nil)
		if err != nil {
			t.Fatalf("Call err: %v", err)
		}
		return res, &out
	}

	res, out := call(t, tsx, `{"verdict":"rejected","confidence":1.7,"reasons":"缺生产实例证据","gaps":["补 lnut 验证"]}`)
	if out.Verdict != "rejected" || out.Reasons != "缺生产实例证据" || len(out.Gaps) != 1 || out.Gaps[0] != "补 lnut 验证" {
		t.Fatalf("verdict 解析错: %+v", out)
	}
	if out.Confidence != 1 {
		t.Fatalf("confidence 未 clamp 到 1: %v", out.Confidence)
	}
	if out.Gaps == nil {
		t.Fatal("Gaps 不得为 nil")
	}
	if !strings.Contains(res.Flatten(), "已记录审核结论：rejected") {
		t.Fatalf("确认文案错: %s", res.Flatten())
	}

	res, out = call(t, tsx, `{"verdict":"approved","reasons":"逐条对应，证据充分"}`)
	if out.Verdict != "approved" || out.Gaps == nil || len(out.Gaps) != 0 {
		t.Fatalf("approved 解析错(gaps 缺失须为空数组): %+v", out)
	}
	if out.Confidence != 0 {
		t.Fatalf("confidence 缺省应为 0: %v", out.Confidence)
	}
	if !strings.Contains(res.Flatten(), "approved") {
		t.Fatalf("确认文案错: %s", res.Flatten())
	}

	// 负值 clamp 到 0。
	if _, out = call(t, tsx, `{"verdict":"approved","confidence":-3,"reasons":"r"}`); out.Confidence != 0 {
		t.Fatalf("负 confidence 未 clamp 到 0: %v", out.Confidence)
	}

	// 非法 verdict → 报错要求重调,不写 out。
	var hold GoalReviewVerdict
	res, err := tsx.auditVerdictTo(&hold).Call(context.Background(), json.RawMessage(`{"verdict":"maybe","reasons":"r"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Flatten(), "重新调用") || hold.Verdict != "" {
		t.Fatalf("非法 verdict 应报错要求重调且不落 out: %s %+v", res.Flatten(), hold)
	}

	// 坏 JSON → 报解析失败。
	res, err = tsx.auditVerdictTo(&hold).Call(context.Background(), json.RawMessage(`{bad`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Flatten(), "解析失败") {
		t.Fatalf("坏 JSON 应报解析失败: %s", res.Flatten())
	}

	// fail-closed:无任务上下文 → 明确报不可用。
	res, err = (&ToolSet{}).auditVerdictTo(&hold).Call(context.Background(), json.RawMessage(`{"verdict":"approved","reasons":"r"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Flatten(), "审核上下文不可用") || hold.Verdict != "" {
		t.Fatalf("无上下文应 fail-closed: %s", res.Flatten())
	}
}

// --- 纯逻辑:goal_audit_context / goal_audit_node 无上下文 fail-closed ---

func TestAuditReadToolsUnavailableWithoutContext(t *testing.T) {
	tsx := &ToolSet{}
	for name, tool := range map[string]actool.CoreTool{
		"goal_audit_context": tsx.auditContext(AuditGoalInput{TaskID: 7, GoalID: 1, EvidenceID: 2}),
		"goal_audit_node":    tsx.auditNode(),
	} {
		res, err := tool.Call(context.Background(), json.RawMessage(`{"node_id":1}`), nil)
		if err != nil {
			t.Fatalf("%s Call err: %v", name, err)
		}
		if !strings.Contains(res.Flatten(), "审核上下文不可用") {
			t.Fatalf("%s 应报审核上下文不可用: %s", name, res.Flatten())
		}
	}
}

// --- AuditGoal 诚实失败路径(无 provider / 审核员未提交结论) ---

func TestAuditGoalNilProvider(t *testing.T) {
	v, err := AuditGoal(context.Background(), nil, AuditGoalInput{TaskID: 7, GoalID: 1, EvidenceID: 2}, nil, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if v.Verdict != "unverified" || v.Gaps == nil || v.FailNote == "" {
		t.Fatalf("无 provider 应诚实 unverified: %+v", v)
	}
}

// goalReviewStubProvider 只回一段文本、从不调工具——审核员跑完但不提交结论。
type goalReviewStubProvider struct{}

func (goalReviewStubProvider) Stream(_ context.Context, _ llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		if !yield(llm.StreamEvent{Type: llm.SEMessageStart}, nil) {
			return
		}
		if !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "证据不足，但我忘了提交结论。"}, nil) {
			return
		}
		if !yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: "end_turn"}, nil) {
			return
		}
		yield(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
	}
}

func (p goalReviewStubProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	acc := llm.NewAccumulator()
	for ev, err := range p.Stream(ctx, req) {
		if err != nil {
			return llm.Message{}, "", llm.Usage{}, err
		}
		acc.Add(ev)
	}
	return acc.Message(), acc.StopReason, acc.Usage, nil
}

func TestAuditGoalNoVerdictSubmitted(t *testing.T) {
	var activities []db.Activity
	v, err := AuditGoal(context.Background(), goalReviewStubProvider{},
		AuditGoalInput{TaskID: 7, GoalID: 1, EvidenceID: 2, Reason: "主张", TaskBrief: "原始需求"}, nil,
		func(a db.Activity) { activities = append(activities, a) })
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if v.Verdict != "unverified" || v.FailNote != "审核员未提交结论" || v.Gaps == nil {
		t.Fatalf("审核员未提交结论应诚实 unverified: %+v", v)
	}
	// 每条活动强制 Worker="goal_auditor"(会话列表「目标审核」分组的依据)。
	if len(activities) == 0 {
		t.Fatal("审核循环未发任何活动")
	}
	for _, a := range activities {
		if a.Worker != "goal_auditor" {
			t.Fatalf("活动 Worker=%q, want goal_auditor (kind=%s)", a.Worker, a.Kind)
		}
	}
}

// --- prove_goal 闸门分支(需要 PG;ARTEX_PG_DSN 未设置时跳过) ---

func TestProveGoalReviewGate(t *testing.T) {
	dsn, err := testDSN()
	if err != nil {
		t.Skip("no test database configured (ARTEX_PG_DSN)")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	// 每个子用例独立一套 exploration+goal+fact,避免状态串扰。
	setup := func(t *testing.T) (tsx *ToolSet, es *db.ExplorationStore, taskID, goal, fact int64) {
		t.Helper()
		expID, err := d.CreateExploration("闸门测试", "拿下 lnut 生产站点管理员权限")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, expID) })
		es = d.Exploration(expID)
		goal, err = es.AddGoal(map[string]any{"text": "拿到 lnut 生产站点管理员权限"}, "human")
		if err != nil {
			t.Fatal(err)
		}
		fact, err = es.AddNode(db.KindFact, map[string]any{"summary": "test2 实例拿到了 admin", "evidence": "在 test2 靶机登录成功"}, 5, "confirmed", "worker-1", nil)
		if err != nil {
			t.Fatal(err)
		}
		// expID 充当 PG task id(桩 hook 只记录不触库)。
		tsx = NewToolSet(es, "planner")
		tsx.SetTaskID(expID)
		return tsx, es, expID, goal, fact
	}

	setHook := func(t *testing.T, fn func(ctx context.Context, req GoalReviewRequest) (GoalReviewVerdict, error)) {
		t.Helper()
		prev := GoalReviewHook
		GoalReviewHook = fn
		t.Cleanup(func() { GoalReviewHook = prev })
	}

	prove := func(t *testing.T, tsx *ToolSet, goal, fact int64) string {
		t.Helper()
		in := json.RawMessage(fmt.Sprintf(`{"goal_id":%d,"evidence_id":%d,"reason":"test2 实例已拿下 admin，目标达成"}`, goal, fact))
		res, herr := tsx.proveGoal().Call(context.Background(), in, nil)
		if herr != nil {
			t.Fatal(herr)
		}
		return res.Flatten()
	}

	goalState := func(t *testing.T, es *db.ExplorationStore, goal int64) string {
		t.Helper()
		n, err := es.GetNode(goal)
		if err != nil || n == nil {
			t.Fatalf("读 goal %d: %v", goal, err)
		}
		return n.State
	}

	t.Run("nil_hook_旧行为", func(t *testing.T) {
		tsx, es, _, goal, fact := setup(t)
		setHook(t, nil)
		got := prove(t, tsx, goal, fact)
		if st := goalState(t, es, goal); st != "met" {
			t.Fatalf("nil hook 应走旧行为落 met, got %s", st)
		}
		if strings.Contains(got, "已过目标审核") {
			t.Fatalf("nil hook 文案不应带审核标记: %s", got)
		}
	})

	t.Run("approved_过审落met", func(t *testing.T) {
		tsx, es, taskID, goal, fact := setup(t)
		var gotReq GoalReviewRequest
		setHook(t, func(_ context.Context, req GoalReviewRequest) (GoalReviewVerdict, error) {
			gotReq = req
			return GoalReviewVerdict{Verdict: "approved", Confidence: 0.9, Reasons: "逐条对应", Gaps: []string{}}, nil
		})
		got := prove(t, tsx, goal, fact)
		if st := goalState(t, es, goal); st != "met" {
			t.Fatalf("approved 应落 met, got %s", st)
		}
		if !strings.Contains(got, "已过目标审核") {
			t.Fatalf("approved 文案应带「已过目标审核」: %s", got)
		}
		if gotReq.GoalID != goal || gotReq.EvidenceID != fact || gotReq.TaskID != taskID || !strings.Contains(gotReq.GoalText, "lnut") || gotReq.Reason == "" {
			t.Fatalf("hook 请求组装错: %+v", gotReq)
		}
	})

	t.Run("rejected_保持open且proves边保留", func(t *testing.T) {
		tsx, es, _, goal, fact := setup(t)
		setHook(t, func(_ context.Context, _ GoalReviewRequest) (GoalReviewVerdict, error) {
			return GoalReviewVerdict{Verdict: "rejected", Reasons: "证据对象是 test2 实例，与 lnut 生产目标不一致", Gaps: []string{"补 lnut 生产实例验证"}}, nil
		})
		got := prove(t, tsx, goal, fact)
		if st := goalState(t, es, goal); st != "open" {
			t.Fatalf("rejected 应保持 open, got %s", st)
		}
		for _, w := range []string{"目标审核驳回（goal", "test2 实例", "补 lnut 生产实例验证", "勿重复提交同一证据"} {
			if !strings.Contains(got, w) {
				t.Fatalf("驳回文案缺 [%s]: %s", w, got)
			}
		}
		// 驳回也保留 proves 边 = 尝试记录(契约 §2.2)。
		edges, err := es.Edges(100)
		if err != nil {
			t.Fatal(err)
		}
		kept := false
		for _, e := range edges {
			if e.Rel == db.RelProves && e.From == fact && e.To == goal {
				kept = true
			}
		}
		if !kept {
			t.Fatal("rejected 后 proves 边应保留")
		}
	})

	t.Run("skip_旧行为无审核标记", func(t *testing.T) {
		tsx, es, _, goal, fact := setup(t)
		setHook(t, func(_ context.Context, _ GoalReviewRequest) (GoalReviewVerdict, error) {
			return GoalReviewVerdict{Skip: true}, nil
		})
		got := prove(t, tsx, goal, fact)
		if st := goalState(t, es, goal); st != "met" {
			t.Fatalf("Skip 应走旧行为落 met, got %s", st)
		}
		if strings.Contains(got, "已过目标审核") {
			t.Fatalf("Skip 文案不应带审核标记: %s", got)
		}
	})

	t.Run("unverified_closed_消化为rejected", func(t *testing.T) {
		tsx, es, _, goal, fact := setup(t)
		setHook(t, func(_ context.Context, _ GoalReviewRequest) (GoalReviewVerdict, error) {
			// 服务端 unverified+closed 的消化产物:rejected,理由在 FailNote。
			return GoalReviewVerdict{Verdict: "rejected", FailNote: "审核系统暂不可用，为防误判不标记 met"}, nil
		})
		got := prove(t, tsx, goal, fact)
		if st := goalState(t, es, goal); st != "open" {
			t.Fatalf("unverified-closed 应保持 open, got %s", st)
		}
		if !strings.Contains(got, "审核系统暂不可用") {
			t.Fatalf("文案应带 FailNote 理由: %s", got)
		}
	})

	t.Run("hook_err_防误判保持open", func(t *testing.T) {
		tsx, es, _, goal, fact := setup(t)
		setHook(t, func(_ context.Context, _ GoalReviewRequest) (GoalReviewVerdict, error) {
			return GoalReviewVerdict{}, errors.New("goal_reviews 表不存在")
		})
		got := prove(t, tsx, goal, fact)
		if st := goalState(t, es, goal); st != "open" {
			t.Fatalf("hook err 应防误判保持 open, got %s", st)
		}
		if !strings.Contains(got, "审核系统异常") || !strings.Contains(got, "goal_reviews 表不存在") {
			t.Fatalf("文案应注明审核系统异常与原因: %s", got)
		}
	})

	t.Run("approved_最后一目标自动收官", func(t *testing.T) {
		tsx, _, _, goal, fact := setup(t)
		setHook(t, func(_ context.Context, _ GoalReviewRequest) (GoalReviewVerdict, error) {
			return GoalReviewVerdict{Verdict: "approved", Reasons: "ok", Gaps: []string{}}, nil
		})
		got := prove(t, tsx, goal, fact)
		if !tsx.GoalMet {
			t.Fatal("唯一目标过审后应自动判定任务完成(GoalMet)")
		}
		if !strings.Contains(got, "任务自动判定完成") || !strings.Contains(got, "已过目标审核") {
			t.Fatalf("收官文案错: %s", got)
		}
	})
}
