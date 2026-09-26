package db

// AI 记忆库 db 层测试(契约 §10):upsert 幂等 / 事实失效链 / 作用域与 Q 过滤 /
// 企业统计与概览。数据全部用 MEMT-<纳秒时间戳> 前缀隔离,defer 清干净(复用库残留敏感)。

import (
	"fmt"
	"testing"
	"time"
)

func memoryTestCleanupRun(d *DB, id int64) {
	_, _ = d.Exec(`DELETE FROM memory_runs WHERE id=$1`, id) //nolint:errcheck
}

func memoryTestCleanupFact(d *DB, id int64) {
	_, _ = d.Exec(`DELETE FROM memory_facts WHERE id=$1`, id) //nolint:errcheck
}

func memoryTestContainsFact(facts []*MemoryFact, id int64) bool {
	for _, f := range facts {
		if f.ID == id {
			return true
		}
	}
	return false
}

// TestMemoryRunUpsertIdempotent 同 task_id 二次 upsert 必须复用同一行并全字段覆盖。
func TestMemoryRunUpsertIdempotent(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	taskID := time.Now().UnixNano() // memory_runs.task_id 无外键,纳秒时间戳唯一即可
	r := &MemoryRun{TaskID: taskID, Stage: "exploit", Status: "pending", Source: "ai", Summary: "MEMT 初版骨架"}
	id, err := d.UpsertMemoryRun(r)
	if err != nil {
		t.Fatal(err)
	}
	defer memoryTestCleanupRun(d, id)

	// 覆盖版:状态/摘要/集合字段全变,行不变。
	r2 := &MemoryRun{TaskID: taskID, Stage: "exploit", Status: "done", Source: "manual",
		Summary: "MEMT 复盘覆盖版",
		Highlights: []string{"拿到后台会话", "绕过 WAF 拿到上传点"},
		Failures:   []MemoryRunFailure{{What: "爆破 22 端口", RootCause: "WAF拦截", CostNote: "约 10 分钟"}},
		OpenLeads:  []string{"子站 x 疑似内网入口"},
		Stats:      map[string]any{"findings": 2}}
	id2, err := d.UpsertMemoryRun(r2)
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id {
		t.Fatalf("同 task_id 二次 upsert 应复用同一行: %d != %d", id2, id)
	}

	got, err := d.MemoryRunByTask(taskID)
	if err != nil || got == nil {
		t.Fatalf("MemoryRunByTask: %v(%v)", got, err)
	}
	if got.Status != "done" || got.Source != "manual" || got.Summary != "MEMT 复盘覆盖版" {
		t.Fatalf("覆盖未生效: %+v", got)
	}
	if len(got.Highlights) != 2 || got.Highlights[0] != "拿到后台会话" {
		t.Fatalf("highlights 不对: %#v", got.Highlights)
	}
	if len(got.Failures) != 1 || got.Failures[0].RootCause != "WAF拦截" {
		t.Fatalf("failures 不对: %#v", got.Failures)
	}
	if len(got.OpenLeads) != 1 {
		t.Fatalf("open_leads 不对: %#v", got.OpenLeads)
	}
	if v, ok := got.Stats["findings"]; !ok || fmt.Sprint(v) != "2" {
		t.Fatalf("stats 不对: %#v", got.Stats)
	}
	if got.ScopeCompanies == nil {
		t.Fatalf("集合字段必须非 nil(空数组),不能是 JSON null")
	}

	got2, err := d.MemoryRun(id)
	if err != nil || got2 == nil || got2.TaskID != taskID {
		t.Fatalf("MemoryRun 按主键回读失败: %v(%v)", got2, err)
	}

	_, total, err := d.ListMemoryRuns(MemoryRunFilter{TaskID: taskID})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("TaskID 过滤 total=%d, want 1", total)
	}
	_, totalEmpty, err := d.ListMemoryRuns(MemoryRunFilter{CompanyID: taskID})
	if err != nil {
		t.Fatal(err)
	}
	if totalEmpty != 0 {
		t.Fatalf("CompanyID 不在 scope_companies 时 total=%d, want 0", totalEmpty)
	}
}

// TestMemoryFactInvalidationChain 失效制:Invalidate 置 valid_to、Active 列表排除、
// include_invalid 过滤可见、Delete 物理删除。
func TestMemoryFactInvalidationChain(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	ts := time.Now().UnixNano()
	scopeID := ts
	taskID := ts + 7
	fid, err := d.CreateMemoryFact(&MemoryFact{
		ScopeKind: "company", ScopeID: scopeID, Kind: "tech_stack",
		Subject:    fmt.Sprintf("MEMT-%d-host", ts),
		Content:    fmt.Sprintf("MEMT-%d nginx 1.24 部署于 80 端口", ts),
		SourceTaskID: taskID, Confidence: 0.8,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer memoryTestCleanupFact(d, fid)

	// content 必填。
	if _, err := d.CreateMemoryFact(&MemoryFact{ScopeKind: "global"}); err == nil {
		t.Fatalf("空 content 应被拒绝")
	}

	fact, err := d.MemoryFact(fid)
	if err != nil || fact == nil {
		t.Fatalf("MemoryFact 回读: %v(%v)", fact, err)
	}
	if fact.SourceTaskID != taskID || fact.Confidence < 0.79 || fact.Confidence > 0.81 || fact.ValidTo != nil {
		t.Fatalf("字段回读不对: %+v", fact)
	}

	active, err := d.ActiveMemoryFacts([]MemoryScope{{Kind: "company", ID: scopeID}}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !memoryTestContainsFact(active, fid) {
		t.Fatalf("未失效事实应出现在 ActiveMemoryFacts")
	}

	if err := d.InvalidateMemoryFact(fid); err != nil {
		t.Fatal(err)
	}
	inv, err := d.MemoryFact(fid)
	if err != nil || inv == nil || inv.ValidTo == nil {
		t.Fatalf("失效后 valid_to 应非空: %+v(%v)", inv, err)
	}
	active2, err := d.ActiveMemoryFacts([]MemoryScope{{Kind: "company", ID: scopeID}}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if memoryTestContainsFact(active2, fid) {
		t.Fatalf("已失效事实不应出现在 ActiveMemoryFacts")
	}

	_, totalDefault, err := d.ListMemoryFacts(MemoryFactFilter{CompanyID: scopeID})
	if err != nil {
		t.Fatal(err)
	}
	if totalDefault != 0 {
		t.Fatalf("默认过滤应排除已失效事实, total=%d", totalDefault)
	}
	_, totalAll, err := d.ListMemoryFacts(MemoryFactFilter{CompanyID: scopeID, IncludeInvalid: true})
	if err != nil {
		t.Fatal(err)
	}
	if totalAll != 1 {
		t.Fatalf("include_invalid 应含已失效事实, total=%d", totalAll)
	}

	if err := d.DeleteMemoryFact(fid); err != nil {
		t.Fatal(err)
	}
	gone, err := d.MemoryFact(fid)
	if err != nil || gone != nil {
		t.Fatalf("删除后应不存在: %v(%v)", gone, err)
	}
}

// TestMemoryFactScopesQAndActive 作用域命中(company/src_target/global)、limit、Q 过滤。
func TestMemoryFactScopesQAndActive(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	ts := time.Now().UnixNano()
	cid := ts
	sid := ts + 1
	mk := func(scopeKind string, scopeID int64, token string) int64 {
		t.Helper()
		id, err := d.CreateMemoryFact(&MemoryFact{
			ScopeKind: scopeKind, ScopeID: scopeID, Kind: "path",
			Subject: fmt.Sprintf("MEMT-%s-subject", token),
			Content: fmt.Sprintf("MEMT 线索 %s /admin", token),
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	fCompany := mk("company", cid, fmt.Sprintf("%d-a", ts))
	fSrc := mk("src_target", sid, fmt.Sprintf("%d-b", ts))
	fGlobal := mk("global", 0, fmt.Sprintf("%d-c", ts))
	defer memoryTestCleanupFact(d, fCompany)
	defer memoryTestCleanupFact(d, fSrc)
	defer memoryTestCleanupFact(d, fGlobal)

	active, err := d.ActiveMemoryFacts([]MemoryScope{{Kind: "company", ID: cid}, {Kind: "src_target", ID: sid}}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !memoryTestContainsFact(active, fCompany) || !memoryTestContainsFact(active, fSrc) || !memoryTestContainsFact(active, fGlobal) {
		t.Fatalf("scope 命中 + global 都应返回: company=%v src=%v global=%v",
			memoryTestContainsFact(active, fCompany), memoryTestContainsFact(active, fSrc), memoryTestContainsFact(active, fGlobal))
	}

	limited, err := d.ActiveMemoryFacts([]MemoryScope{{Kind: "company", ID: cid}, {Kind: "src_target", ID: sid}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 2 {
		t.Fatalf("limit=2 应返回 2 条, got %d", len(limited))
	}

	// 不传 scopes → 只回 global。
	onlyGlobal, err := d.ActiveMemoryFacts(nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !memoryTestContainsFact(onlyGlobal, fGlobal) || memoryTestContainsFact(onlyGlobal, fCompany) {
		t.Fatalf("空 scopes 只应返回 global: got company=%v global=%v",
			memoryTestContainsFact(onlyGlobal, fCompany), memoryTestContainsFact(onlyGlobal, fGlobal))
	}

	// Q 过滤:token 唯一,total 必须在过滤之后算。
	token := fmt.Sprintf("MEMTQ%d", ts)
	fq := mk("company", cid, token)
	defer memoryTestCleanupFact(d, fq)
	facts, total, err := d.ListMemoryFacts(MemoryFactFilter{Q: token, IncludeInvalid: true})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(facts) != 1 || facts[0].ID != fq {
		t.Fatalf("Q 过滤应只命中 1 条: total=%d len=%d", total, len(facts))
	}
}

// TestMemoryCompanyStatsAndOverview 企业统计(无记忆企业 runs=0 也要出现)与概览计数。
func TestMemoryCompanyStatsAndOverview(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	cs := d.Companies()

	ts := time.Now().UnixNano()
	cidA, _, err := cs.UpsertCompany(fmt.Sprintf("MEMT Corp A %d", ts), "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, cidA)
	cidB, _, err := cs.UpsertCompany(fmt.Sprintf("MEMT Corp B %d", ts), "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupCompany(d, cidB)

	rid, err := d.UpsertMemoryRun(&MemoryRun{TaskID: ts, Stage: "recon", ScopeCompanies: []int64{cidA},
		Status: "done", Source: "backfill", Summary: "MEMT 企业 A 的历史复盘"})
	if err != nil {
		t.Fatal(err)
	}
	defer memoryTestCleanupRun(d, rid)

	fActive, err := d.CreateMemoryFact(&MemoryFact{ScopeKind: "company", ScopeID: cidA, Kind: "asset_note",
		Subject: fmt.Sprintf("MEMT-%d-a", ts), Content: fmt.Sprintf("MEMT-%d 有效事实", ts)})
	if err != nil {
		t.Fatal(err)
	}
	defer memoryTestCleanupFact(d, fActive)
	fDead, err := d.CreateMemoryFact(&MemoryFact{ScopeKind: "company", ScopeID: cidA, Kind: "asset_note",
		Subject: fmt.Sprintf("MEMT-%d-d", ts), Content: fmt.Sprintf("MEMT-%d 已失效事实", ts)})
	if err != nil {
		t.Fatal(err)
	}
	defer memoryTestCleanupFact(d, fDead)
	if err := d.InvalidateMemoryFact(fDead); err != nil {
		t.Fatal(err)
	}

	stats, err := d.MemoryCompanyStats()
	if err != nil {
		t.Fatal(err)
	}
	var statA, statB *MemoryCompanyStat
	for i := range stats {
		switch stats[i].CompanyID {
		case cidA:
			statA = &stats[i]
		case cidB:
			statB = &stats[i]
		}
	}
	if statA == nil || statB == nil {
		t.Fatalf("两家测试企业都应出现在统计里: A=%v B=%v", statA, statB)
	}
	if statA.Runs != 1 || statA.FactsActive != 1 || statA.LastRunAt == nil {
		t.Fatalf("企业 A 统计不对: %+v", statA)
	}
	if statB.Runs != 0 || statB.FactsActive != 0 {
		t.Fatalf("无记忆企业 B 应为 0: %+v", statB)
	}

	ov, err := d.MemoryOverview(cidA)
	if err != nil {
		t.Fatal(err)
	}
	if ov.FactsTotal != 2 || ov.FactsActive != 1 || ov.RunsTotal != 1 {
		t.Fatalf("企业 A 概览不对: %+v", ov)
	}
	for _, status := range []string{"candidate", "active", "rejected", "retired"} {
		if _, ok := ov.Insights[status]; !ok {
			t.Fatalf("insights 缺状态键 %s: %#v", status, ov.Insights)
		}
		if _, ok := ov.Playbooks[status]; !ok {
			t.Fatalf("playbooks 缺状态键 %s: %#v", status, ov.Playbooks)
		}
	}
	ovAll, err := d.MemoryOverview(0)
	if err != nil {
		t.Fatal(err)
	}
	if ovAll.RunsTotal < ov.RunsTotal || ovAll.FactsTotal < ov.FactsTotal {
		t.Fatalf("全局概览应不小于企业概览: all=%+v company=%+v", ovAll, ov)
	}
}
