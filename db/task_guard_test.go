package db

import (
	"fmt"
	"testing"
	"time"
)

// TestTaskGuardModeRoundTrip 覆盖任务级监管门限的读写链路:创建时落库、
// 列表/单条回读、PATCH 改门限(含"改回跟随全局"与"清空规则集")、以及非法值被拒。
//
// 重点在"改回默认":guard_mode 的 "" 与 guard_rule_ids 的 [] 都是合法值,
// 如果实现用零值判断"没传",用户就永远改不回跟随全局——这是本功能最容易翻车的地方,
// 所以单独断言。
func TestTaskGuardModeRoundTrip(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	suffix := time.Now().UnixNano()
	task, err := d.CreateTaskWithOptions(
		fmt.Sprintf("task-guard-%d", suffix), "goal",
		TaskCreateOptions{GuardMode: TaskGuardModeOff, GuardRuleIDs: []int64{7, 3, 7, -1}},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.DeleteTask(task.ID) }()

	// 创建路径:off 落库;规则集归一(去重 + 丢非正 id + 保序)。
	if task.GuardMode != TaskGuardModeOff {
		t.Fatalf("created guard_mode = %q, want %q", task.GuardMode, TaskGuardModeOff)
	}
	if got := fmt.Sprint(task.GuardRuleIDs); got != "[7 3]" {
		t.Fatalf("created guard_rule_ids = %v, want [7 3] (去重+丢非正 id)", got)
	}

	// 回读路径:taskCols/scanTask 必须带上这两列(JSONB 走 []byte 中转)。
	got, err := d.GetTask(task.ID)
	if err != nil || got == nil {
		t.Fatalf("GetTask = %+v, %v", got, err)
	}
	if got.GuardMode != TaskGuardModeOff || fmt.Sprint(got.GuardRuleIDs) != "[7 3]" {
		t.Fatalf("GetTask guard = %q / %v, want off / [7 3]", got.GuardMode, got.GuardRuleIDs)
	}
	list, err := d.ListTasks()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range list {
		if item.ID != task.ID {
			continue
		}
		found = true
		if item.GuardMode != TaskGuardModeOff {
			t.Fatalf("ListTasks guard_mode = %q, want off", item.GuardMode)
		}
	}
	if !found {
		t.Fatalf("task %d missing from ListTasks", task.ID)
	}

	// 改回跟随全局 + 清空规则集:传空串/空切片必须真的写进去,
	// 而不是被当成"没传"保持原样。
	inherit := ""
	empty := []int64{}
	updated, err := d.UpdateTask(task.ID, TaskPatch{GuardMode: &inherit, GuardRuleIDs: &empty})
	if err != nil || updated == nil {
		t.Fatalf("UpdateTask = %+v, %v", updated, err)
	}
	if updated.GuardMode != TaskGuardModeInherit {
		t.Fatalf("after reset guard_mode = %q, want \"\"", updated.GuardMode)
	}
	if len(updated.GuardRuleIDs) != 0 {
		t.Fatalf("after reset guard_rule_ids = %v, want empty", updated.GuardRuleIDs)
	}
	// 再查一次库,确认不是只有返回值对(即 UPDATE 真的落库了)。
	reloaded, err := d.GetTask(task.ID)
	if err != nil || reloaded == nil {
		t.Fatalf("GetTask after reset = %+v, %v", reloaded, err)
	}
	if reloaded.GuardMode != TaskGuardModeInherit || len(reloaded.GuardRuleIDs) != 0 {
		t.Fatalf("persisted reset = %q / %v, want \"\" / empty", reloaded.GuardMode, reloaded.GuardRuleIDs)
	}

	// 'inherit' 是 '' 的别名,写入后应归一成空串。
	inheritAlias := "inherit"
	aliased, err := d.UpdateTask(task.ID, TaskPatch{GuardMode: &inheritAlias})
	if err != nil || aliased == nil {
		t.Fatalf("UpdateTask(inherit) = %+v, %v", aliased, err)
	}
	if aliased.GuardMode != TaskGuardModeInherit {
		t.Fatalf("guard_mode after 'inherit' = %q, want \"\" (归一)", aliased.GuardMode)
	}

	// 非法值必须在写库前被拒(不能落到 CHECK 约束上,那样调用方看不懂错因)。
	bad := "disabled"
	if _, err := d.UpdateTask(task.ID, TaskPatch{GuardMode: &bad}); err == nil {
		t.Fatal("UpdateTask with invalid guard_mode should fail")
	}
	if _, err := d.CreateTaskWithOptions(
		fmt.Sprintf("task-guard-bad-%d", suffix), "goal",
		TaskCreateOptions{GuardMode: "nope"},
	); err == nil {
		t.Fatal("CreateTaskWithOptions with invalid guard_mode should fail")
	}
}

// TestTaskGuardDefaultsAreSafe 断言未配置时的默认值方向:空串 + 空规则集 =
// 跟随全局 + 用全部已启用规则。这两个默认值是"照常监管",任何回归到
// "没配置就等于放通"的改动都会在这里被抓住。
func TestTaskGuardDefaultsAreSafe(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	task, err := d.CreateTask(fmt.Sprintf("task-guard-default-%d", time.Now().UnixNano()), "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.DeleteTask(task.ID) }()

	got, err := d.GetTask(task.ID)
	if err != nil || got == nil {
		t.Fatalf("GetTask = %+v, %v", got, err)
	}
	if got.GuardMode != TaskGuardModeInherit {
		t.Fatalf("default guard_mode = %q, want \"\"(跟随全局)", got.GuardMode)
	}
	if len(got.GuardRuleIDs) != 0 {
		t.Fatalf("default guard_rule_ids = %v, want empty(用全部规则)", got.GuardRuleIDs)
	}
}

// TestValidTaskGuardMode 锁住取值集合与 schema 的 CHECK 一致。
func TestValidTaskGuardMode(t *testing.T) {
	valid := []string{"", "inherit", "off", "enforce"}
	for _, m := range valid {
		if !ValidTaskGuardMode(m) {
			t.Errorf("ValidTaskGuardMode(%q) = false, want true", m)
		}
	}
	invalid := []string{"on", "disabled", "OFF", "Enforce", "true", "1"}
	for _, m := range invalid {
		if ValidTaskGuardMode(m) {
			t.Errorf("ValidTaskGuardMode(%q) = true, want false", m)
		}
	}
	if got := NormalizeTaskGuardMode("inherit"); got != "" {
		t.Errorf("NormalizeTaskGuardMode(inherit) = %q, want \"\"", got)
	}
	if got := NormalizeTaskGuardMode("off"); got != "off" {
		t.Errorf("NormalizeTaskGuardMode(off) = %q, want off", got)
	}
}
