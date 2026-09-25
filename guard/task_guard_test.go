package guard

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
)

// 任务级门限 mode=off 的端到端契约:工具放通,且**不产生任何审计行**。
// 后半句同样重要——任务主动关掉监管,不该在审批记录里刷出一条条噪音记录;
// 若这里写进了行,"关闭"就变成了"关闭但留痕",用户会误以为监管仍在拦截。
func TestTaskGuardOffPassesThroughWithoutAudit(t *testing.T) {
	d, err := db.Open(guardTestDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	prevTools, _, _ := d.GetSetting("intercept_enabled_tools")
	t.Cleanup(func() { _ = d.SetSetting("intercept_enabled_tools", prevTools) })
	if err := d.SetSetting("intercept_enabled_tools", `["Bash"]`); err != nil {
		t.Fatal(err)
	}

	rule, err := d.CreateInterceptRule("task-off-"+t.Name(), "tool_name", "string", "Bash", "deny", "", 9999, true, false, 0, "deny", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM intercept_pending WHERE rule_id=$1`, rule.ID)
		_, _ = d.Exec(`DELETE FROM intercept_rules WHERE id=$1`, rule.ID)
	})

	ic := intercept.New(d)
	ic.SetTaskGuardResolver(func(taskID string) (string, []int64, bool) {
		if taskID == "task-off-e2e" {
			return "off", nil, true
		}
		return "", nil, true
	})
	g := NewWithInterceptor(ic)

	run := func(taskID string) bool {
		t.Helper()
		ctx := intercept.WithTaskContext(context.Background(), taskID, "test-agent", nil)
		input, _ := json.Marshal(map[string]string{"command": "rm -rf /var/www"})
		blocked, _, _ := g.Hooks().PreToolUse(ctx, "Bash", input)
		return blocked
	}

	// mode=off:deny 规则被跳过,放通,且没有审计行。
	if run("task-off-e2e") {
		t.Fatal("mode=off must pass the call through even when a deny rule matches")
	}
	var offRows int
	if err := d.QueryRow(`SELECT COUNT(*) FROM intercept_pending WHERE rule_id=$1`, rule.ID).Scan(&offRows); err != nil {
		t.Fatal(err)
	}
	if offRows != 0 {
		t.Fatalf("mode=off wrote %d audit row(s), want 0 (a disabled task must not clutter the approval log)", offRows)
	}

	// 对照:同一规则在跟随全局的任务上必须照常拦截并落审计行,
	// 证明上一步的放通来自任务配置而不是规则本身失效。
	if !run("task-inherit-e2e") {
		t.Fatal("a task following the global posture must still be blocked by the deny rule")
	}
	var inheritRows int
	if err := d.QueryRow(`SELECT COUNT(*) FROM intercept_pending WHERE rule_id=$1 AND status='denied'`, rule.ID).Scan(&inheritRows); err != nil {
		t.Fatal(err)
	}
	if inheritRows != 1 {
		t.Fatalf("inheriting task wrote %d denied audit row(s), want 1", inheritRows)
	}
}
