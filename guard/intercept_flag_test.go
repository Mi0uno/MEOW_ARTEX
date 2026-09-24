package guard

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
)

// guardTestDSN 只认显式环境变量,与 db/server 测试同一条门禁:未设置隔离库时跳过,
// 绝不回退 config.json(那是运行实例的正库)。
func guardTestDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("ARTEX_PG_DSN"))
	if dsn == "" {
		t.Skip("ARTEX_PG_DSN 未设置 — 跳过需要数据库的 guard 用例")
	}
	return dsn
}

// flag(AI判疑)的核心契约:工具必须照常执行(不 block、不等人工),同时留下一条
// status='flagged' 的审计行。任何一半失效都会让这一档退化成 allow(无痕)或 ask(阻塞)。
// 本用例同时验证 schema 的 intercept_pending_status_check 真的接受 'flagged'
// ——约束若没放开,写入会被数据库静默拒绝,这正是历史上踩过的坑。
func TestFlagActionPassesThroughAndAudits(t *testing.T) {
	d, err := db.Open(guardTestDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	// 只让 Bash 进监管,避免依赖库里既有的工具开关设置;用完还原。
	prevTools, _, _ := d.GetSetting("intercept_enabled_tools")
	t.Cleanup(func() { _ = d.SetSetting("intercept_enabled_tools", prevTools) })
	if err := d.SetSetting("intercept_enabled_tools", `["Bash"]`); err != nil {
		t.Fatal(err)
	}

	rule, err := d.CreateInterceptRule("flag-fixture-"+t.Name(), "tool_name", "string", "Bash", "flag", "", 9999, true, false, 0, "deny")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM intercept_pending WHERE rule_id=$1`, rule.ID)
		_, _ = d.Exec(`DELETE FROM intercept_rules WHERE id=$1`, rule.ID)
	})

	ic := intercept.New(d)
	g := NewWithInterceptor(ic)
	input, _ := json.Marshal(map[string]string{"command": "ls -la"})
	blocked, _, _ := g.Hooks().PreToolUse(context.Background(), "Bash", input)
	if blocked {
		t.Fatal("flag must pass the call through, got block (that would make it deny)")
	}

	var status, reason, source, effective, execution string
	err = d.QueryRow(`
SELECT p.status, p.reason, p.decision_source,
       COALESCE(p.audit->>'effective_action',''), COALESCE(p.audit->>'execution_status','')
FROM intercept_pending p WHERE p.rule_id=$1 ORDER BY p.id DESC LIMIT 1`, rule.ID).
		Scan(&status, &reason, &source, &effective, &execution)
	if err != nil {
		t.Fatalf("no audit row written for flag: %v", err)
	}
	if status != "flagged" {
		t.Fatalf("status=%q, want flagged", status)
	}
	if source != "rule" {
		t.Fatalf("decision_source=%q, want rule", source)
	}
	// flag 的执行语义与 allow 相同(工具确实执行),审计必须照 allow 记录,
	// 否则 CompleteIntercept 关联不上执行结果。本用例没有 Trace 上下文,关联是
	// unavailable,execution_status 因此按既有规则降级为 unknown(同 allow 的行为)。
	if effective != "allow" {
		t.Fatalf("audit effective_action=%q, want allow", effective)
	}
	if execution != "unknown" && execution != "awaiting_result" {
		t.Fatalf("audit execution_status=%q, want unknown (no trace) or awaiting_result", execution)
	}
	if !strings.Contains(reason, "放行") {
		t.Fatalf("reason should state the call was passed, got %q", reason)
	}

	// 判疑不产生待审批:approvals 页的 pending 列表必须保持干净。
	var pending int
	if err := d.QueryRow(`SELECT COUNT(*) FROM intercept_pending WHERE rule_id=$1 AND status='pending'`, rule.ID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("flag created %d pending approval(s), want 0 (flag must not block)", pending)
	}
}
