package db

// 资产监控 db 层单测。跑在显式指定的演示库(ARTEX_PG_DSN,如 5499 集群)上,
// 绝不回退 config.json、绝不连 5432(见 testmain_test.go 的门禁说明)。
// 纪律:测试数据一律用独占前缀 monitor-it-<rand>(公司名/域名/IP 全部唯一),
// 且 t.Cleanup 删除自建行——5499 是共享演示库,不留垃圾、不碰他人数据。

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func monitorTestSetup(t *testing.T) (*DB, *AssetStore, *CompanyStore) {
	t.Helper()
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	// 注意:t.Cleanup 在测试函数 return(含其 defer)之后才跑,所以 Close 必须挂在
	// t.Cleanup 上且先注册(LIFO=最后关),否则后续注册的数据清理会在连接池已关闭后
	// 执行、静默失败,把测试垃圾留在共享演示库里。
	t.Cleanup(func() { _ = d.Close() })
	return d, d.Assets(), d.Companies()
}

// monitorTestName 生成全局唯一的独占前缀测试值(公司名/域名/URL 复用)。
func monitorTestName() string {
	return fmt.Sprintf("monitor-it-%d-%d", time.Now().UnixNano(), rand.Intn(1_000_000))
}

// monitorTestCompany 建一个测试企业并注册清理(连带删除名下资产;targets 级联)。
func monitorTestCompany(t *testing.T, cs *CompanyStore) int64 {
	t.Helper()
	cid, _, err := cs.UpsertCompany(monitorTestName(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := cs.DeleteCompanyWithAssets(cid, true); err != nil {
			t.Errorf("cleanup company %d: %v", cid, err)
		}
	})
	return cid
}

// monitorTestRunCleanup 注册轮次清理:先删轮行再(由企业清理级联)删 targets,
// 避免 runs.company_id ON DELETE SET NULL 把测试轮留成孤儿行。
func monitorTestRunCleanup(t *testing.T, d *DB, runID int64) {
	t.Cleanup(func() {
		if _, err := d.Exec(`DELETE FROM recon_monitor_runs WHERE id = $1`, runID); err != nil {
			t.Errorf("cleanup run %d: %v", runID, err)
		}
	})
}

// assertJSONEq 语义比较 JSON(JSONB 列会归一化空白,不能逐字节比)。
func assertJSONEq(t *testing.T, want json.RawMessage, got string) {
	t.Helper()
	var w, g any
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("want not JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("got not JSON: %v (%s)", err, got)
	}
	if !reflect.DeepEqual(w, g) {
		t.Errorf("JSON mismatch:\n got %s\nwant %s", got, want)
	}
}

// TestMonitorTargetUpsertThreeState 覆盖 UpsertMonitorTarget 的三态语义:
// enabled 不动/设置;interval 设置/不动/清除;新行缺省 true + NULL。
func TestMonitorTargetUpsertThreeState(t *testing.T) {
	d, _, cs := monitorTestSetup(t)
	cid := monitorTestCompany(t, cs)

	// 新行:全部「不动」→ enabled 缺省 true、interval NULL、无水位。
	tgt, err := d.UpsertMonitorTarget(cid, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !tgt.Enabled {
		t.Errorf("fresh row: enabled want true, got %v", tgt.Enabled)
	}
	if tgt.IntervalHours != nil {
		t.Errorf("fresh row: interval_hours want nil, got %v", *tgt.IntervalHours)
	}
	if tgt.LastRunAt != nil || tgt.LastRunID != nil {
		t.Errorf("fresh row: watermark want nil, got %v %v", tgt.LastRunAt, tgt.LastRunID)
	}

	// enabled 显式 false → 生效;随后「不动」→ 保持 false。
	off := false
	if tgt, err = d.UpsertMonitorTarget(cid, &off, nil, false); err != nil {
		t.Fatal(err)
	}
	if tgt.Enabled {
		t.Error("explicit enabled=false not persisted")
	}
	if tgt, err = d.UpsertMonitorTarget(cid, nil, nil, false); err != nil {
		t.Fatal(err)
	}
	if tgt.Enabled {
		t.Error("enabled must stay false when arg is nil")
	}

	// interval 设置 48 → 生效;「不动」→ 保持 48;清除 → NULL。
	if tgt, err = d.UpsertMonitorTarget(cid, nil, intp(48), false); err != nil {
		t.Fatal(err)
	}
	if tgt.IntervalHours == nil || *tgt.IntervalHours != 48 {
		t.Errorf("interval_hours want 48, got %v", tgt.IntervalHours)
	}
	if tgt, err = d.UpsertMonitorTarget(cid, nil, nil, false); err != nil {
		t.Fatal(err)
	}
	if tgt.IntervalHours == nil || *tgt.IntervalHours != 48 {
		t.Errorf("interval_hours must stay 48 when untouched, got %v", tgt.IntervalHours)
	}
	if tgt, err = d.UpsertMonitorTarget(cid, nil, nil, true); err != nil {
		t.Fatal(err)
	}
	if tgt.IntervalHours != nil {
		t.Errorf("clearInterval must null interval_hours, got %v", *tgt.IntervalHours)
	}

	// interval <=0 非法(CHECK 约束之前先在本层拦下)。
	if _, err := d.UpsertMonitorTarget(cid, nil, intp(0), false); err == nil {
		t.Error("interval_hours=0 must be rejected")
	}

	// ListMonitorTargets 含该行;无行企业 GetMonitorTarget 返回 nil,nil。
	targets, err := d.ListMonitorTargets()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tg := range targets {
		if tg.CompanyID == cid {
			found = true
		}
	}
	if !found {
		t.Error("ListMonitorTargets missing test row")
	}
	other := monitorTestCompany(t, cs)
	if tg, err := d.GetMonitorTarget(other); err != nil || tg != nil {
		t.Errorf("GetMonitorTarget(no row) want nil,nil got %v,%v", tg, err)
	}
}

// TestMonitorCandidateCompanies 覆盖候选企业查询:有 kind=domain 范围的企业入选
// (域名清单有序、计数正确);只有 ip 范围的企业不算候选。
func TestMonitorCandidateCompanies(t *testing.T) {
	d, _, cs := monitorTestSetup(t)

	// 候选企业:两条 domain 规则。
	cid := monitorTestCompany(t, cs)
	root := monitorTestName() + ".example"
	if _, _, _, verrs, err := cs.AddScopeInputsChecked(cid, []ScopeInput{
		{Kind: "domain", Value: root},
		{Kind: "domain", Value: "zzz." + root},
	}, "monitor-it-test"); err != nil || len(verrs) > 0 {
		t.Fatalf("add scope: err=%v verrs=%v", err, verrs)
	}

	// 非候选企业:只有 ip/cidr 范围。
	ipOnly := monitorTestCompany(t, cs)
	if _, _, _, verrs, err := cs.AddScopeInputsChecked(ipOnly, []ScopeInput{
		{Kind: "cidr", Value: "192.0.2.0/24"},
	}, "monitor-it-test"); err != nil || len(verrs) > 0 {
		t.Fatalf("add scope: err=%v verrs=%v", err, verrs)
	}

	cands, err := d.ListMonitorCandidateCompanies()
	if err != nil {
		t.Fatal(err)
	}
	var got *MonitorCompany
	ipCandidate := false
	for i := range cands {
		switch cands[i].ID {
		case cid:
			got = &cands[i]
		case ipOnly:
			ipCandidate = true
		}
	}
	if got == nil {
		t.Fatalf("company %d with domain scope not in candidates", cid)
	}
	if ipCandidate {
		t.Errorf("company %d has no domain scope but appeared as candidate", ipOnly)
	}
	if got.Name == "" || got.DomainCount != 2 {
		t.Errorf("candidate = %+v, want domain_count=2", got)
	}
	if got.Domains == nil || len(got.Domains) != 2 {
		t.Fatalf("domains = %v, want 2 entries", got.Domains)
	}
	if got.Domains[0] != root || got.Domains[1] != "zzz."+root {
		t.Errorf("domains = %v, want sorted [%s zzz.%s]", got.Domains, root, root)
	}
}

// TestMonitorRunStartFinishWatermark 覆盖 Start/Finish 往返、水位回写
// (Finish 在无 targets 行时补插)与 ResetMonitorTargetRun 清零。
func TestMonitorRunStartFinishWatermark(t *testing.T) {
	d, _, cs := monitorTestSetup(t)
	cid := monitorTestCompany(t, cs)

	runID, err := d.StartMonitorRun(cid, "monitor-it-测试企业", "manual")
	if err != nil {
		t.Fatal(err)
	}
	monitorTestRunCleanup(t, d, runID)

	// running 往返:空集合字段必须以 "[]" 直通,绝不 nil。
	r, err := d.GetMonitorRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if r == nil {
		t.Fatal("GetMonitorRun returned nil for fresh run")
	}
	if r.Status != "running" || r.Trigger != "manual" || r.FinishedAt != nil {
		t.Errorf("fresh run = %+v", r)
	}
	if r.CompanyID == nil || *r.CompanyID != cid {
		t.Errorf("fresh run company_id = %v, want %d", r.CompanyID, cid)
	}
	if string(r.Collectors) != "[]" || string(r.NewDomains) != "[]" {
		t.Errorf("fresh run collections must be [], got collectors=%s new_domains=%s", r.Collectors, r.NewDomains)
	}

	collectors := json.RawMessage(`[{"key":"crtsh","status":"done","found":12,"error":""},{"key":"fofa","status":"failed","found":0,"error":"boom"}]`)
	newDomains := json.RawMessage(`[{"type":"domain","value":"x.monitor-it.example"},{"type":"ip","value":"192.0.2.7"}]`)
	if err := d.FinishMonitorRun(runID, cid, "done", collectors, 12, 2, newDomains, false, 1, 1, ""); err != nil {
		t.Fatal(err)
	}

	// 详情含 new_domains / collectors,数值字段完整。
	r, err = d.GetMonitorRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "done" || r.FinishedAt == nil || r.TotalFound != 12 || r.NewCount != 2 ||
		r.ProbeOK != 1 || r.ProbeFailed != 1 || r.Error != "" {
		t.Errorf("finished run = %+v", r)
	}
	assertJSONEq(t, newDomains, string(r.NewDomains))
	var stat []map[string]any
	if err := json.Unmarshal(r.Collectors, &stat); err != nil {
		t.Fatalf("collectors not valid JSON: %v (%s)", err, r.Collectors)
	}
	if len(stat) != 2 || stat[0]["key"] != "crtsh" {
		t.Errorf("collectors = %s", r.Collectors)
	}

	// 水位回写:本测试没先 UpsertMonitorTarget,Finish 必须补插 targets 行。
	tgt, err := d.GetMonitorTarget(cid)
	if err != nil {
		t.Fatal(err)
	}
	if tgt == nil {
		t.Fatal("FinishMonitorRun must upsert a targets row (无行则插)")
	}
	if tgt.LastRunID == nil || *tgt.LastRunID != runID || tgt.LastRunAt == nil {
		t.Errorf("watermark = %+v, want last_run_id=%d last_run_at!=nil", tgt, runID)
	}

	// LatestMonitorRoundAt:本测试刚结束一轮,MAX(finished_at) 必非空。
	latest, err := d.LatestMonitorRoundAt()
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil {
		t.Error("LatestMonitorRoundAt want non-nil after a finished round")
	}

	// ResetMonitorTargetRun 清零水位。
	if err := d.ResetMonitorTargetRun(cid); err != nil {
		t.Fatal(err)
	}
	if tgt, err = d.GetMonitorTarget(cid); err != nil {
		t.Fatal(err)
	}
	if tgt == nil || tgt.LastRunAt != nil || tgt.LastRunID != nil {
		t.Errorf("after reset watermark want nil/nil, got %+v", tgt)
	}
}

// TestMonitorRunListFilterAndTotal 覆盖 ListMonitorRuns:company_id/status 过滤、
// total 在过滤后计算、id DESC、limit/offset、列表 new_domains 一律 "[]"。
func TestMonitorRunListFilterAndTotal(t *testing.T) {
	d, _, cs := monitorTestSetup(t)
	cidA := monitorTestCompany(t, cs)
	cidB := monitorTestCompany(t, cs)

	mkRun := func(cid int64, trigger, status string, newDomains json.RawMessage) int64 {
		t.Helper()
		id, err := d.StartMonitorRun(cid, "monitor-it-过滤测试", trigger)
		if err != nil {
			t.Fatal(err)
		}
		monitorTestRunCleanup(t, d, id)
		if status != "running" {
			if err := d.FinishMonitorRun(id, cid, status, json.RawMessage(`[]`), 1, 1,
				newDomains, false, 0, 0, ""); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	domains := json.RawMessage(`[{"type":"domain","value":"only-in-detail.monitor-it.example"}]`)
	idDone := mkRun(cidA, "manual", "done", domains) // 旧
	idRun := mkRun(cidA, "schedule", "running", nil) // 新(running)
	idOther := mkRun(cidB, "schedule", "done", nil)  // 别家企业

	// 仅 company_id 过滤:total=2(过滤后计数),id DESC,列表 new_domains 恒 "[]"。
	runs, total, err := d.ListMonitorRuns(MonitorRunFilter{CompanyID: cidA})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(runs) != 2 {
		t.Fatalf("company filter: total=%d len=%d, want 2/2", total, len(runs))
	}
	if runs[0].ID != idRun || runs[1].ID != idDone {
		t.Errorf("order want [%d %d], got [%d %d]", idRun, idDone, runs[0].ID, runs[1].ID)
	}
	for _, r := range runs {
		if string(r.NewDomains) != "[]" {
			t.Errorf("list must not carry new_domains, run %d got %s", r.ID, r.NewDomains)
		}
		if r.CompanyID == nil || *r.CompanyID != cidA {
			t.Errorf("run %d company_id = %v, want %d", r.ID, r.CompanyID, cidA)
		}
	}

	// status 过滤(running):只命中一条。
	runs, total, err = d.ListMonitorRuns(MonitorRunFilter{CompanyID: cidA, Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(runs) != 1 || runs[0].ID != idRun {
		t.Errorf("running filter: total=%d runs=%v, want 1/[%d]", total, runs, idRun)
	}

	// status 过滤(done):两条(含别家企业),total 与页长一致。
	runs, total, err = d.ListMonitorRuns(MonitorRunFilter{Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if total < 2 {
		t.Errorf("done filter: total=%d, want >=2", total)
	}

	// limit/offset:翻页仍报过滤后 total=2,第二页是更旧那条。
	runs, total, err = d.ListMonitorRuns(MonitorRunFilter{CompanyID: cidA, Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(runs) != 1 || runs[0].ID != idDone {
		t.Errorf("page 2: total=%d runs=%v, want 2/[%d]", total, runs, idDone)
	}

	// 详情接口才有 new_domains;MonitorRunsByIDs 与详情同形。
	byIDs, err := d.MonitorRunsByIDs([]int64{idDone, idOther, 1 << 40})
	if err != nil {
		t.Fatal(err)
	}
	if len(byIDs) != 2 {
		t.Fatalf("MonitorRunsByIDs want 2 hits (missing id skipped), got %d", len(byIDs))
	}
	if string(byIDs[idDone].NewDomains) == "" {
		t.Fatal("byIDs new_domains empty")
	}
	assertJSONEq(t, domains, string(byIDs[idDone].NewDomains))
	empty, err := d.MonitorRunsByIDs(nil)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Errorf("MonitorRunsByIDs(nil) want empty non-nil map, got %v %v", empty, err)
	}
}

// TestExistingValues 覆盖类型→列映射、批量 IN 命中、空入参与未知类型报错。
func TestExistingValues(t *testing.T) {
	_, av2, cs := monitorTestSetup(t)
	cid := monitorTestCompany(t, cs)

	root := monitorTestName() + ".example"
	sub := "a." + root
	ipStr := fmt.Sprintf("192.0.2.%d", 10+rand.Intn(200)) // TEST-NET-1,不出网
	// 注意:UpsertHTTPService 会归一化 URL(小写 scheme/host、去根路径尾斜杠),
	// 存量判定必须按归一化后的值比对(调用方 Agent C 同样要先归一化再查)。
	svcURL := fmt.Sprintf("https://%s", sub)

	for _, up := range []struct {
		name string
		fn   func() (int64, error)
	}{
		{"root_domain", func() (int64, error) { return av2.UpsertRootDomain(UpsertRootDomainReq{Domain: root, CompanyID: cid}) }},
		{"subdomain", func() (int64, error) { return av2.UpsertSubdomain(UpsertSubdomainReq{Domain: sub, CompanyID: cid}) }},
		{"ip", func() (int64, error) { return av2.UpsertIP(UpsertIPReq{IP: ipStr, CompanyID: cid}) }},
		{"service", func() (int64, error) { return av2.UpsertHTTPService(UpsertHTTPServiceReq{URL: svcURL, CompanyID: cid}) }},
	} {
		if _, err := up.fn(); err != nil {
			t.Fatalf("seed %s: %v", up.name, err)
		}
	}

	// domain → subdomain 行的 domain 列;root_domain → root_domain 行的 domain 列。
	got, err := av2.ExistingValues("domain", []string{sub, "miss." + root})
	if err != nil {
		t.Fatal(err)
	}
	if !got[sub] || got["miss."+root] || len(got) != 1 {
		t.Errorf("domain hits = %v, want only %s", got, sub)
	}
	got, err = av2.ExistingValues("root_domain", []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if !got[root] || len(got) != 1 {
		t.Errorf("root_domain hits = %v, want only %s", got, root)
	}
	// domain 查询只认 subdomain 行:同值存在于 root_domain 行不算命中(刻意语义)。
	if got, err = av2.ExistingValues("domain", []string{root}); err != nil || got[root] {
		t.Errorf("domain query must not match root_domain row, got %v err=%v", got, err)
	}

	// ip / service。
	if got, err = av2.ExistingValues("ip", []string{ipStr, "192.0.2.254"}); err != nil || !got[ipStr] || got["192.0.2.254"] {
		t.Errorf("ip hits = %v err=%v", got, err)
	}
	if got, err = av2.ExistingValues("service", []string{svcURL}); err != nil || !got[svcURL] {
		t.Errorf("service hits = %v err=%v", got, err)
	}

	// 空入参 → 空 map;未知类型 → 报错。
	if got, err = av2.ExistingValues("domain", nil); err != nil || len(got) != 0 {
		t.Errorf("empty values want empty map, got %v err=%v", got, err)
	}
	if _, err = av2.ExistingValues("app", []string{"x"}); err == nil {
		t.Error("unsupported type must error")
	}
}

// TestQueryAssetsNewerThanDays 覆盖 QueryAssets:newer_than_days 生效、
// total 在过滤后计算、created_at DESC 排序与 CreatedAt 字段暴露。
func TestQueryAssetsNewerThanDays(t *testing.T) {
	d, av2, cs := monitorTestSetup(t)
	cid := monitorTestCompany(t, cs)

	fresh := monitorTestName() + ".example"
	stale := monitorTestName() + ".example"
	if _, err := av2.UpsertRootDomain(UpsertRootDomainReq{Domain: fresh, CompanyID: cid}); err != nil {
		t.Fatal(err)
	}
	if _, err := av2.UpsertRootDomain(UpsertRootDomainReq{Domain: stale, CompanyID: cid}); err != nil {
		t.Fatal(err)
	}
	// 把 stale 行的 created_at 回拨 40 天(只动自建行)。
	if _, err := d.Exec(`UPDATE assets SET created_at = now() - interval '40 days' WHERE type='root_domain' AND domain=$1`, stale); err != nil {
		t.Fatal(err)
	}

	// 无天数筛选:两行全中,CreatedAt 已暴露,按 created_at DESC → fresh 在前。
	got, total, err := av2.QueryAssets(AssetListFilter{CompanyID: cid, Type: "root_domain"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(got) != 2 {
		t.Fatalf("no filter: total=%d len=%d, want 2/2", total, len(got))
	}
	if got[0].Domain != fresh || got[1].Domain != stale {
		t.Errorf("order want [%s %s], got [%s %s]", fresh, stale, got[0].Domain, got[1].Domain)
	}
	if got[0].CreatedAt == "" {
		t.Error("created_at must be exposed on assets")
	}

	// newer_than_days=1:只命中 fresh,total 是过滤后的 1。
	got, total, err = av2.QueryAssets(AssetListFilter{CompanyID: cid, Type: "root_domain", NewerThanDays: 1})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(got) != 1 || got[0].Domain != fresh {
		t.Errorf("newer_than_days=1: total=%d got=%v, want 1/[%s]", total, got, fresh)
	}

	// newer_than_days=60:回拨 40 天的行仍在窗口内。
	if _, total, err = av2.QueryAssets(AssetListFilter{CompanyID: cid, Type: "root_domain", NewerThanDays: 60}); err != nil || total != 2 {
		t.Errorf("newer_than_days=60: total=%d err=%v, want 2", total, err)
	}

	// limit/offset 翻页:total 保持过滤后总数。
	got, total, err = av2.QueryAssets(AssetListFilter{CompanyID: cid, Type: "root_domain", Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(got) != 1 || got[0].Domain != stale {
		t.Errorf("page 2: total=%d got=%v, want 2/[%s]", total, got, stale)
	}
}
