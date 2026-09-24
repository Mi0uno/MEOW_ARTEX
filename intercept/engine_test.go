package intercept

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// 关键回归:旧实现把决策引擎(JEV)嵌在 reviewer 闭包内,而 Judge 开头用
// `if !cfg.Enabled || rv == nil { return }` 提前返回(llm_judge_enabled=false),
// 于是引擎永不执行——开关与凭据都配好却毫无痕迹。下面的用例锁死
// "引擎不受 llm_judge_enabled 影响"这一契约。
//
// Judge 的前置引擎分支不读设置,所以这些用例无需数据库;只有 ask 档要取
// 人工审批超时(judgeConfig),那条单独用隔离库跑(见 engineAskTimeoutTestDSN)。

func TestDecisionEngineRunsWithoutLLMJudge(t *testing.T) {
	ic := &Interceptor{pending: newPendingManager()}
	calls := 0
	ic.SetDecisionEngine(func(_ context.Context, in DecisionEngineInput) (Decision, bool) {
		calls++
		if in.Tool != "Bash" {
			t.Fatalf("engine got tool %q", in.Tool)
		}
		return Decision{Action: "flag", Message: "Jev 判疑: 参数可疑"}, true
	})
	// 不注入 reviewer:模拟 llm_judge_enabled=false 且未接 LLM 的现场。
	dec, ok := ic.Judge(context.Background(), "Bash", json.RawMessage(`{"command":"ls"}`))
	if !ok {
		t.Fatal("decision engine verdict was dropped (regression: engine still gated by llm_judge_enabled)")
	}
	if calls != 1 {
		t.Fatalf("engine called %d times, want 1", calls)
	}
	if dec.Action != "flag" {
		t.Fatalf("action=%q, want flag", dec.Action)
	}
	// 引擎裁决必须走与模型裁决相同的归一化:reason 带 [模型] 前缀,
	// 否则 decision_source 会落成 unknown 而不是 model。
	if !strings.HasPrefix(dec.Message, "[模型]") {
		t.Fatalf("engine reason missing [模型] prefix: %q", dec.Message)
	}
}

func TestDecisionEngineAbstainsFallsThrough(t *testing.T) {
	ic := &Interceptor{pending: newPendingManager()}
	ic.SetDecisionEngine(func(context.Context, DecisionEngineInput) (Decision, bool) {
		return Decision{}, false // 低置信:交给下一层
	})
	// reviewer 为 nil → 第二层无裁决,整体返回 false(保持放行语义)。
	if _, ok := ic.Judge(context.Background(), "Bash", json.RawMessage(`{}`)); ok {
		t.Fatal("abstaining engine must not produce a verdict")
	}
}

func TestDecisionEngineInvalidActionIsNotAVerdict(t *testing.T) {
	ic := &Interceptor{pending: newPendingManager()}
	ic.SetDecisionEngine(func(context.Context, DecisionEngineInput) (Decision, bool) {
		return Decision{Action: "approve"}, true // 引擎返回白名单外的动作
	})
	if _, ok := ic.Judge(context.Background(), "Bash", json.RawMessage(`{}`)); ok {
		t.Fatal("invalid engine action must not pass the call through as approved")
	}
}

// 引擎的 allow/deny/flag 三档不读设置,可无库验证;ask 需要取审批超时,单独测。
func TestEngineVerdictShapes(t *testing.T) {
	for _, action := range []string{"allow", "deny", "flag"} {
		t.Run(action, func(t *testing.T) {
			ic := &Interceptor{pending: newPendingManager()}
			ic.SetDecisionEngine(func(context.Context, DecisionEngineInput) (Decision, bool) {
				return Decision{Action: action}, true
			})
			dec, ok := ic.Judge(context.Background(), "Write", json.RawMessage(`{"path":"a.txt"}`))
			if !ok || dec.Action != action {
				t.Fatalf("action=%q ok=%v, want %q", dec.Action, ok, action)
			}
			if dec.RuleID != 0 {
				t.Fatalf("engine verdict must carry no rule id, got %d", dec.RuleID)
			}
			if len(dec.ModelInput) == 0 || dec.ModelInputDigest == "" {
				t.Fatal("engine verdict must retain the reviewed input for audit")
			}
		})
	}
}

// engineAskTimeoutTestDSN 只认显式环境变量,与 db/server 测试同一条门禁:
// 未设置隔离库时跳过,绝不回退 config.json(那是运行实例的正库)。
func engineAskTimeoutTestDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("ARTEX_PG_DSN"))
	if dsn == "" {
		t.Skip("ARTEX_PG_DSN 未设置 — 跳过需要数据库的引擎 ask 用例")
	}
	return dsn
}

// 引擎给出 ask 时必须带上人工审批超时与超时动作:否则 HandleAsk 会退回 60s
// 默认值,且 TimeoutAction 为空串被当作 deny,与配置不符。
func TestEngineAskCarriesApprovalTimeout(t *testing.T) {
	d, err := db.Open(engineAskTimeoutTestDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	ic := New(d)
	ic.SetDecisionEngine(func(context.Context, DecisionEngineInput) (Decision, bool) {
		return Decision{Action: "ask"}, true
	})
	dec, ok := ic.Judge(context.Background(), "Bash", json.RawMessage(`{"command":"ls"}`))
	if !ok || dec.Action != "ask" {
		t.Fatalf("action=%q ok=%v, want ask", dec.Action, ok)
	}
	cfg := ic.GetJudgeConfig()
	if !dec.TimeoutEnabled || dec.TimeoutSeconds != cfg.AskTimeoutSeconds || dec.TimeoutAction != cfg.AskTimeoutAction {
		t.Fatalf("ask timeout not wired: %+v (want seconds=%d action=%q)", dec, cfg.AskTimeoutSeconds, cfg.AskTimeoutAction)
	}
}
