package intercept

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// 任务级门限(Agent D 注入 resolver,A 侧提供钩子)。这组测试锁死三条契约:
//  1. resolver 未注入 → 行为与引入钩子之前完全一致(安全控制的默认不能变松);
//  2. mode=off → 规则与两层模型都不介入,guard 直接放通且不写审计;
//  3. ruleIDs 非空 → 只用这些 id 的规则参与匹配。
//
// 门禁与 db/server 测试一致:只认显式 ARTEX_PG_DSN(隔离库),未设置则跳过。

func taskGuardTestDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("ARTEX_PG_DSN"))
	if dsn == "" {
		t.Skip("ARTEX_PG_DSN 未设置 — 跳过需要数据库的任务级门限用例")
	}
	return dsn
}

// taskCtx 把 taskID 放进 ctx:taskID 是任务级配置的唯一索引来源(见 WithTaskContext)。
func taskCtx(taskID string) context.Context {
	return WithTaskContext(context.Background(), taskID, "test-agent", nil)
}

// off 必须在触达规则缓存之前短路:用一个 db 为 nil 的 Interceptor 证明这一点
// ——若短路晚于 i.rules(),这里会直接 panic 而不是通过。
func TestTaskGuardOffShortCircuitsBeforeRules(t *testing.T) {
	ic := &Interceptor{pending: newPendingManager()} // db == nil
	ic.SetTaskGuardResolver(func(taskID string) (string, []int64, bool) {
		return "off", nil, true
	})
	if _, matched := ic.Match(taskCtx("t-off"), "Bash", []byte(`{"command":"ls"}`)); matched {
		t.Fatal("mode=off must report no rule match")
	}
}

// off 同样必须在决策引擎之前短路:引擎被调用就意味着还发生了 15s 级网络调用。
func TestTaskGuardOffSkipsDecisionEngine(t *testing.T) {
	ic := &Interceptor{pending: newPendingManager()}
	ic.SetTaskGuardResolver(func(taskID string) (string, []int64, bool) {
		return "off", nil, true
	})
	calls := 0
	ic.SetDecisionEngine(func(context.Context, string, json.RawMessage) (Decision, bool) {
		calls++
		return Decision{Action: "deny"}, true
	})
	if _, ok := ic.Judge(taskCtx("t-off"), "Bash", json.RawMessage(`{"command":"rm -rf /"}`)); ok {
		t.Fatal("mode=off must produce no verdict")
	}
	if calls != 0 {
		t.Fatalf("decision engine ran %d time(s) under mode=off, want 0", calls)
	}
}

// resolver 未注入时,即便 ctx 里带着 taskID,规则也必须照常命中——这是"默认不放松"
// 的核心断言:钩子没接上不等于监管关闭。
func TestTaskGuardWithoutResolverKeepsRulesActive(t *testing.T) {
	d, err := db.Open(taskGuardTestDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	tool := "TaskGuardNoResolverTool"
	rule, err := d.CreateInterceptRule("task-guard-nores-"+t.Name(), "tool_name", "string", tool, "deny", "", 9999, true, false, 0, "deny")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM intercept_rules WHERE id=$1`, rule.ID)

	ic := New(d) // 不调用 SetTaskGuardResolver
	dec, matched := ic.Match(taskCtx("t-any"), tool, []byte(`{}`))
	if !matched || dec.Action != "deny" {
		t.Fatalf("without a resolver the rule must still match, got matched=%v dec=%+v", matched, dec)
	}
	// 空 taskID(chat 会话无任务上下文)同样不受影响。
	if _, matched := ic.Match(context.Background(), tool, []byte(`{}`)); !matched {
		t.Fatal("without task context the rule must still match")
	}
}

// mode=off 时规则不介入,且同一进程内其他任务的规则匹配不受影响。
func TestTaskGuardOffDisablesRules(t *testing.T) {
	d, err := db.Open(taskGuardTestDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	tool := "TaskGuardOffTool"
	rule, err := d.CreateInterceptRule("task-guard-off-"+t.Name(), "tool_name", "string", tool, "deny", "", 9999, true, false, 0, "deny")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM intercept_rules WHERE id=$1`, rule.ID)

	ic := New(d)
	ic.SetTaskGuardResolver(func(taskID string) (string, []int64, bool) {
		if taskID == "t-off" {
			return "off", nil, true
		}
		return "", nil, true // 其他任务跟随全局
	})
	if _, matched := ic.Match(taskCtx("t-off"), tool, []byte(`{}`)); matched {
		t.Fatal("mode=off must not match any rule")
	}
	if _, matched := ic.Match(taskCtx("t-other"), tool, []byte(`{}`)); !matched {
		t.Fatal("a task following the global posture must still match")
	}
}

// ""/"inherit"/查不到(ok=false)/未知 taskID 都必须跟随全局:只有明确的 off 才是否决。
func TestTaskGuardNonOffModesFollowGlobal(t *testing.T) {
	d, err := db.Open(taskGuardTestDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	tool := "TaskGuardInheritTool"
	rule, err := d.CreateInterceptRule("task-guard-inherit-"+t.Name(), "tool_name", "string", tool, "deny", "", 9999, true, false, 0, "deny")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM intercept_rules WHERE id=$1`, rule.ID)

	ic := New(d)
	modes := map[string]string{
		"t-empty":    "",
		"t-inherit":  "inherit",
		"t-enforce":  "enforce",
		"t-unknown":  "something-else",
		"t-notfound": "off", // 由 resolver 返回 ok=false,不应被当作 off
	}
	ic.SetTaskGuardResolver(func(taskID string) (string, []int64, bool) {
		if taskID == "t-notfound" {
			return "", nil, false // 查不到该任务配置
		}
		return modes[taskID], nil, true
	})
	for taskID := range modes {
		if _, matched := ic.Match(taskCtx(taskID), tool, []byte(`{}`)); !matched {
			t.Errorf("task %q (mode=%q) must follow the global posture and match", taskID, modes[taskID])
		}
	}
}

// ruleIDs 非空 → 只有这些 id 的规则参与匹配;空数组 = 用全部已启用规则。
func TestTaskGuardRuleIDsRestrictMatching(t *testing.T) {
	d, err := db.Open(taskGuardTestDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	// 两条规则匹配同一个工具名,靠 id 区分谁生效。
	tool := "TaskGuardRuleIDsTool"
	first, err := d.CreateInterceptRule("task-guard-rid-a-"+t.Name(), "tool_name", "string", tool, "deny", "", 9998, true, false, 0, "deny")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM intercept_rules WHERE id=$1`, first.ID)
	second, err := d.CreateInterceptRule("task-guard-rid-b-"+t.Name(), "tool_name", "string", tool, "flag", "", 9999, true, false, 0, "deny")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM intercept_rules WHERE id=$1`, second.ID)

	ic := New(d)
	ic.SetTaskGuardResolver(func(taskID string) (string, []int64, bool) {
		switch taskID {
		case "t-only-second":
			return "", []int64{second.ID}, true
		case "t-only-first":
			return "", []int64{first.ID}, true
		case "t-empty-ids":
			return "", []int64{}, true // 空数组 = 用全部启用规则
		case "t-no-such-rule":
			return "", []int64{999999999}, true
		}
		return "", nil, true
	})

	// priority DESC:second(9999) 先于 first(9998)。限定到 first 时应命中 first。
	dec, matched := ic.Match(taskCtx("t-only-first"), tool, []byte(`{}`))
	if !matched || dec.RuleID != first.ID {
		t.Fatalf("ruleIDs=[first] must restrict matching to that rule, got matched=%v ruleID=%d", matched, dec.RuleID)
	}
	dec, matched = ic.Match(taskCtx("t-only-second"), tool, []byte(`{}`))
	if !matched || dec.RuleID != second.ID || dec.Action != "flag" {
		t.Fatalf("ruleIDs=[second] must match only the second rule, got matched=%v dec=%+v", matched, dec)
	}
	// 空 id 列表 = 不限定,按优先级命中 second。
	dec, matched = ic.Match(taskCtx("t-empty-ids"), tool, []byte(`{}`))
	if !matched || dec.RuleID != second.ID {
		t.Fatalf("empty ruleIDs must use all enabled rules, got matched=%v ruleID=%d", matched, dec.RuleID)
	}
	// 限定到一条不存在的规则 → 没有任何规则可匹配。
	if _, matched := ic.Match(taskCtx("t-no-such-rule"), tool, []byte(`{}`)); matched {
		t.Fatal("ruleIDs naming a nonexistent rule must match nothing")
	}
}
