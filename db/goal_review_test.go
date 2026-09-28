package db

// goal_reviews CRUD 的 PG 集成测试(契约 GOAL_REVIEW_CONTRACTS.md §1)。
// 跑在显式指定的演示库(ARTEX_PG_DSN,如 5499 集群)上,绝不回退 config.json、
// 绝不连 5432(见 testmain_test.go 的门禁说明)。纪律:goal_reviews.task_id 有 FK,
// 测试经 CreateTask 建真实最小任务并在清理时 DeleteTask(级联删审核行);
// settings 键测试前后清理,不污染共享演示库。

import (
	"testing"
	"time"
)

// goalReviewTestSetup 打开演示库并注册连接清理。
func goalReviewTestSetup(t *testing.T) *DB {
	t.Helper()
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// TestGoalReviewCRUD 覆盖 CreateGoalReview/ListGoalReviews/LatestGoalReviewFor:
// 回填与缺省、gaps nil→[]、goal_id 过滤 total 口径、分页、倒序、短路取最近一行。
func TestGoalReviewCRUD(t *testing.T) {
	d := goalReviewTestSetup(t)
	tk, err := d.CreateTask("goal_review CRUD 测试任务", "验证审核记录读写", nil, 0, 0)
	if err != nil {
		t.Skipf("建平台任务失败(库不可写?): %v", err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(tk.ID) }) // 级联删 goal_reviews

	// 空列表:items 永不为 nil,total=0。
	items, total, err := d.ListGoalReviews(GoalReviewFilter{TaskID: tk.ID})
	if err != nil || total != 0 || items == nil || len(items) != 0 {
		t.Fatalf("空列表应为 ([],0,nil): items=%v total=%d err=%v", items, total, err)
	}

	// 建三行:goal 11 两条(approved → rejected),goal 22 一条(unverified)。
	mk := func(goalID, evID int64, verdict string, gaps []string) int64 {
		t.Helper()
		id, err := d.CreateGoalReview(&GoalReview{
			TaskID: tk.ID, GoalID: goalID, EvidenceID: evID,
			Reason: "达标主张", Verdict: verdict, Confidence: 0.7,
			Reasons: "理由:" + verdict, Gaps: gaps, DurationMS: 1234, FailNote: "",
		})
		if err != nil {
			t.Fatalf("CreateGoalReview(%s): %v", verdict, err)
		}
		if id <= 0 {
			t.Fatalf("CreateGoalReview 应返回正 id,得到 %d", id)
		}
		return id
	}
	mk(11, 101, "approved", nil)
	rejectedID := mk(11, 101, "rejected", []string{"补 A", "补 B"})
	mk(22, 202, "unverified", nil)

	// 非法 verdict 拒绝。
	if _, err := d.CreateGoalReview(&GoalReview{TaskID: tk.ID, GoalID: 1, EvidenceID: 1, Verdict: "bogus"}); err == nil {
		t.Fatal("非法 verdict 应报错")
	}

	// 读回:gaps nil 落库后读出为 []string{}(铁律);created_at/回填生效。
	first, err := d.LatestGoalReviewFor(11, 101)
	if err != nil || first == nil {
		t.Fatalf("LatestGoalReviewFor(11,101) 应命中: %v", err)
	}
	if first.ID != rejectedID {
		t.Fatalf("应取最近一行 id=%d,得到 %d", rejectedID, first.ID)
	}
	if first.Verdict != "rejected" || first.Gaps == nil || len(first.Gaps) != 2 {
		t.Fatalf("读回字段不符: %+v gaps=%v", first, first.Gaps)
	}
	if first.CreatedAt.IsZero() || first.Reviewer != "goal_auditor" {
		t.Fatalf("created_at/reviewer 缺省未回填: %+v", first)
	}
	if time.Since(first.CreatedAt) > 5*time.Minute {
		t.Fatalf("created_at 应为刚写入的时刻: %v", first.CreatedAt)
	}

	// 无行 → (nil, nil)。
	if got, err := d.LatestGoalReviewFor(999, 999); err != nil || got != nil {
		t.Fatalf("无行应为 (nil,nil): %v %v", got, err)
	}

	// 全量:3 行,倒序。
	items, total, err = d.ListGoalReviews(GoalReviewFilter{TaskID: tk.ID})
	if err != nil || total != 3 || len(items) != 3 {
		t.Fatalf("全量应 3 行: total=%d len=%d err=%v", total, len(items), err)
	}
	if items[0].ID <= items[1].ID || items[1].ID <= items[2].ID {
		t.Fatalf("应按 id 倒序: %d,%d,%d", items[0].ID, items[1].ID, items[2].ID)
	}

	// goal_id 过滤:total 过滤后算(铁律)。
	filtered, total, err := d.ListGoalReviews(GoalReviewFilter{TaskID: tk.ID, GoalID: 11})
	if err != nil || total != 2 || len(filtered) != 2 {
		t.Fatalf("goal 11 过滤应 2 行(total 过滤后算): total=%d len=%d err=%v", total, len(filtered), err)
	}
	if _, total, _ = d.ListGoalReviews(GoalReviewFilter{TaskID: tk.ID, GoalID: 22}); total != 1 {
		t.Fatalf("goal 22 过滤应 total=1,得到 %d", total)
	}

	// 分页:limit=1&offset=1 → 第 2 新一行;limit 超上限 clamp 200 不炸。
	page, total, err := d.ListGoalReviews(GoalReviewFilter{TaskID: tk.ID, Limit: 1, Offset: 1})
	if err != nil || total != 3 || len(page) != 1 || page[0].ID != items[1].ID {
		t.Fatalf("分页不符: total=%d page=%v err=%v(对照全量第2行 id=%d)", total, page, err, items[1].ID)
	}
	if _, _, err := d.ListGoalReviews(GoalReviewFilter{TaskID: tk.ID, Limit: 500}); err != nil {
		t.Fatalf("limit 超上限应 clamp 不报错: %v", err)
	}

	// TaskID 缺失 → 报错(列表按任务,必带)。
	if _, _, err := d.ListGoalReviews(GoalReviewFilter{}); err == nil {
		t.Fatal("缺 task_id 应报错")
	}
}

// TestGoalReviewTaskBrief 覆盖 TaskGoalBrief 的 JOIN 读法与缺任务语义。
func TestGoalReviewTaskBrief(t *testing.T) {
	d := goalReviewTestSetup(t)
	tk, err := d.CreateTask("goal_review 简报任务", "任务级目标文本", nil, 0, 0)
	if err != nil {
		t.Skipf("建平台任务失败(库不可写?): %v", err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(tk.ID) })

	b, err := d.TaskGoalBrief(tk.ID)
	if err != nil || b == nil {
		t.Fatalf("TaskGoalBrief 应命中: %v", err)
	}
	if b.TaskID != tk.ID || b.Description != "goal_review 简报任务" {
		t.Fatalf("简报字段不符: %+v", b)
	}
	if b.Goal != "任务级目标文本" || b.ExpGoal == "" {
		t.Fatalf("任务目标/探索图根目标不符: %+v", b)
	}
	if b.Stage != "exploit" { // 存量默认阶段
		t.Fatalf("stage 缺省应为 exploit,得到 %q", b.Stage)
	}

	if b, err := d.TaskGoalBrief(-1); err != nil || b != nil {
		t.Fatalf("不存在的任务应为 (nil,nil): %v %v", b, err)
	}
}

// TestGoalReviewConfig 覆盖 config 缺省值、合法/非法更新与 clamp。
func TestGoalReviewConfig(t *testing.T) {
	d := goalReviewTestSetup(t)
	const (
		kEnabled = SettingGoalReviewEnabled
		kFail    = SettingGoalReviewFail
		kTimeout = SettingGoalReviewTimeout
	)
	clean := func() {
		_, _ = d.Exec(`DELETE FROM settings WHERE key = ANY($1)`, []string{kEnabled, kFail, kTimeout})
	}
	// 先清掉共享库可能残留的键,验证缺省值;结束同样清理,不污染演示库。
	if _, err := d.Exec(`DELETE FROM settings WHERE key = ANY($1)`, []string{kEnabled, kFail, kTimeout}); err != nil {
		t.Fatalf("清理 settings 失败: %v", err)
	}
	t.Cleanup(clean)

	enabled, failMode, timeoutSec, err := d.GoalReviewConfig()
	if err != nil {
		t.Fatalf("读缺省配置: %v", err)
	}
	if !enabled || failMode != "open" || timeoutSec != 150 {
		t.Fatalf("缺省应为 true/open/150: %v/%s/%d", enabled, failMode, timeoutSec)
	}

	// 非法值:fail_mode 与 timeout 边界外都报错,且不落库。
	if err := d.SetGoalReviewConfig(nil, strPtr("bogus"), nil); err == nil {
		t.Fatal("fail_mode=bogus 应报错")
	}
	if err := d.SetGoalReviewConfig(nil, nil, intPtr(29)); err == nil {
		t.Fatal("timeout=29 应报错")
	}
	if err := d.SetGoalReviewConfig(nil, nil, intPtr(601)); err == nil {
		t.Fatal("timeout=601 应报错")
	}
	if _, ok, _ := d.GetSetting(kFail); ok {
		t.Fatal("校验失败不应写 settings")
	}

	// 合法更新:指针=nil 保持原值;timeout 边界值原样收下。
	if err := d.SetGoalReviewConfig(boolPtr(false), strPtr("closed"), intPtr(30)); err != nil {
		t.Fatalf("合法更新失败: %v", err)
	}
	enabled, failMode, timeoutSec, err = d.GoalReviewConfig()
	if err != nil || enabled || failMode != "closed" || timeoutSec != 30 {
		t.Fatalf("更新后应 false/closed/30: %v/%s/%d err=%v", enabled, failMode, timeoutSec, err)
	}
	if err := d.SetGoalReviewConfig(nil, nil, intPtr(600)); err != nil {
		t.Fatalf("timeout=600 应合法: %v", err)
	}
	if _, _, timeoutSec, _ = d.GoalReviewConfig(); timeoutSec != 600 {
		t.Fatalf("timeout=600 应读回 600,得到 %d", timeoutSec)
	}

	// 只改 enabled,fail_mode/timeout 保持。
	if err := d.SetGoalReviewConfig(boolPtr(true), nil, nil); err != nil {
		t.Fatalf("部分更新失败: %v", err)
	}
	enabled, failMode, timeoutSec, _ = d.GoalReviewConfig()
	if !enabled || failMode != "closed" || timeoutSec != 600 {
		t.Fatalf("部分更新应保持 closed/600: %v/%s/%d", enabled, failMode, timeoutSec)
	}

	// 存了脏值 → 读侧回落缺省(fail-open),不炸。
	if err := d.SetSetting(kFail, "weird"); err != nil {
		t.Fatalf("写脏值失败: %v", err)
	}
	if _, failMode, _, _ = d.GoalReviewConfig(); failMode != "open" {
		t.Fatalf("脏 fail_mode 应回落 open,得到 %q", failMode)
	}
	if err := d.SetSetting(kTimeout, "not-a-number"); err != nil {
		t.Fatalf("写脏 timeout 失败: %v", err)
	}
	if _, _, timeoutSec, _ = d.GoalReviewConfig(); timeoutSec != 150 {
		t.Fatalf("脏 timeout 应回落 150,得到 %d", timeoutSec)
	}
}

func strPtr(s string) *string { return &s }
func intPtr(n int) *int       { return &n }
func boolPtr(b bool) *bool    { return &b }
