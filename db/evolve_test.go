package db

// 自进化 db 层测试(契约 §10):相似去重命中/未命中、印证自动转正阈值、
// outcome 计数与 avg_seconds 增量、列表 total 过滤顺序、状态与作用域过滤。
// 数据全部用 MEMT-<纳秒时间戳> 前缀隔离,defer 清干净(复用库残留敏感)。

import (
	"fmt"
	"testing"
	"time"
)

func evolveTestCleanupInsight(d *DB, id int64) {
	_, _ = d.Exec(`DELETE FROM evolve_insights WHERE id=$1`, id) //nolint:errcheck
}

func evolveTestCleanupPlaybook(d *DB, id int64) {
	_, _ = d.Exec(`DELETE FROM evolve_playbooks WHERE id=$1`, id) //nolint:errcheck
}

func evolveTestContainsInsight(items []*EvolveInsight, id int64) bool {
	for _, i := range items {
		if i.ID == id {
			return true
		}
	}
	return false
}

func evolveTestContainsPlaybook(items []*EvolvePlaybook, id int64) bool {
	for _, p := range items {
		if p.ID == id {
			return true
		}
	}
	return false
}

// TestEvolveInsightDedupeAndCorroborate 相似去重命中/未命中 + 印证自动转正(阈值 2
// 端到端 + 阈值 3 的「1 次不转、2 次转」) + 任务去重 + 状态过滤。
func TestEvolveInsightDedupeAndCorroborate(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	ts := time.Now().UnixNano()
	title := fmt.Sprintf("MEMT-%d-Nginx 版本信息泄露可批量指纹定位", ts)
	task1 := ts
	id, err := d.CreateEvolveInsight(&EvolveInsight{
		ScopeKind: "global", Title: title,
		Content:  "响应头出现完整版本号时可作为指纹锚点;server_tokens off 可消除",
		Kind:     "negative", Category: "pitfall",
		StackTags: []string{"nginx"}, Corroboration: 1, SourceTaskIDs: []int64{task1}, Confidence: 0.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer evolveTestCleanupInsight(d, id)

	// 空标题拒绝;标题必填。
	if _, err := d.CreateEvolveInsight(&EvolveInsight{ScopeKind: "global"}); err == nil {
		t.Fatalf("空 title 应被拒绝")
	}

	// 相似命中:同标题 similarity=1。
	hit, err := d.SimilarEvolveInsight(title, 0.75)
	if err != nil {
		t.Fatal(err)
	}
	if hit == nil || hit.ID != id {
		t.Fatalf("同标题应命中去重: got %+v", hit)
	}
	// 未命中:内容完全不相干且低于阈值。
	miss, err := d.SimilarEvolveInsight("zzqx 完全无关的经验洞见标题 no overlap", 0.75)
	if err != nil {
		t.Fatal(err)
	}
	if miss != nil {
		t.Fatalf("不相干标题不应命中: %+v", miss)
	}

	// 阈值 2 端到端:初始 corroboration=1,第二个独立任务印证 → 自动转正。
	activated, err := d.CorroborateEvolveInsight(id, ts+1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !activated {
		t.Fatalf("corroboration 1→2 达阈值 2 应自动转正")
	}
	rows, total, err := d.ListEvolveInsights(EvolveInsightFilter{Q: fmt.Sprint(ts)})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("Q 应只命中本条: total=%d", total)
	}
	if rows[0].Status != "active" || rows[0].Corroboration != 2 || rows[0].Confidence < 0.69 {
		t.Fatalf("转正后状态/置信度不对: %+v", rows[0])
	}
	if len(rows[0].SourceTaskIDs) != 2 {
		t.Fatalf("source_task_ids 应为 2 个任务: %#v", rows[0].SourceTaskIDs)
	}
	if len(rows[0].StackTags) != 1 || rows[0].StackTags[0] != "nginx" {
		t.Fatalf("stack_tags 回读不对: %#v", rows[0].StackTags)
	}

	// 阈值 3:第 1 次印证不转,第 2 次才转(覆盖「1st no, 2nd yes」)。
	title2 := fmt.Sprintf("MEMT-%d-另一个待验证洞察", ts)
	id2, err := d.CreateEvolveInsight(&EvolveInsight{
		ScopeKind: "global", Title: title2, Content: "MEMT 阈值测试",
		Kind: "positive", Category: "technique", Corroboration: 1, Confidence: 0.4,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer evolveTestCleanupInsight(d, id2)
	first, err := d.CorroborateEvolveInsight(id2, ts+2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if first {
		t.Fatalf("corroboration 1→2 未达阈值 3 不应转正")
	}
	second, err := d.CorroborateEvolveInsight(id2, ts+3, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !second {
		t.Fatalf("corroboration 2→3 达阈值 3 应转正")
	}
	// 同一任务重复印证:不重复计数。
	if _, err := d.CorroborateEvolveInsight(id2, ts+3, 99); err != nil {
		t.Fatal(err)
	}
	rows2, _, err := d.ListEvolveInsights(EvolveInsightFilter{Q: title2})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows2) != 1 || rows2[0].Corroboration != 4 || len(rows2[0].SourceTaskIDs) != 2 {
		t.Fatalf("重复任务不应重复计数: %+v", rows2)
	}

	// 不存在的行:静默 (false, nil)。
	if activated, err := d.CorroborateEvolveInsight(-1, 1, 2); activated || err != nil {
		t.Fatalf("不存在的 id 应返回 (false, nil): %v %v", activated, err)
	}

	// 状态过滤与 Active 排除。
	if err := d.SetEvolveInsightStatus(id, "retired"); err != nil {
		t.Fatal(err)
	}
	retired, totalRetired, err := d.ListEvolveInsights(EvolveInsightFilter{Status: "retired", Q: fmt.Sprint(ts)})
	if err != nil {
		t.Fatal(err)
	}
	if totalRetired != 1 || retired[0].ID != id {
		t.Fatalf("状态过滤不对: total=%d", totalRetired)
	}
	actives, err := d.ActiveEvolveInsights(nil, 200)
	if err != nil {
		t.Fatal(err)
	}
	if evolveTestContainsInsight(actives, id) {
		t.Fatalf("retired 不应出现在 ActiveEvolveInsights")
	}
	if !evolveTestContainsInsight(actives, id2) {
		t.Fatalf("global active 洞察应出现在 ActiveEvolveInsights")
	}
}

// TestEvolveInsightCompanyFilter CompanyID 只收窄 company 命中行,global/src_target 不受过滤。
func TestEvolveInsightCompanyFilter(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	ts := time.Now().UnixNano()
	cid := ts
	mk := func(scopeKind string, scopeID int64, kind string) int64 {
		t.Helper()
		id, err := d.CreateEvolveInsight(&EvolveInsight{
			ScopeKind: scopeKind, ScopeID: scopeID,
			Title:   fmt.Sprintf("MEMT-%d-%s", ts, scopeKind),
			Content: fmt.Sprintf("MEMT 公司过滤测试 %d kind=%s", ts, kind),
			Kind:    kind,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	fGlobal := mk("global", 0, "positive")
	fGlobal2 := mk("global", 0, "negative")
	fCompany := mk("company", cid, "positive")
	fWrong := mk("company", ts+999, "positive")
	defer evolveTestCleanupInsight(d, fGlobal)
	defer evolveTestCleanupInsight(d, fGlobal2)
	defer evolveTestCleanupInsight(d, fCompany)
	defer evolveTestCleanupInsight(d, fWrong)

	// 命中企业的 CompanyID:该企业行 + 所有 global 行(共 3),别家企业的行不出现。
	rows, total, err := d.ListEvolveInsights(EvolveInsightFilter{CompanyID: cid, Q: fmt.Sprint(ts)})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(rows) != 3 {
		t.Fatalf("CompanyID 过滤应返回 company 命中 + global 共 3 条: total=%d", total)
	}
	for _, r := range rows {
		if r.ID == fWrong {
			t.Fatalf("别家企业的 company 行不应出现: %+v", r)
		}
	}
	// 不相关的 CompanyID:company 行全部排除,global 照常返回(2 条)。
	_, totalOther, err := d.ListEvolveInsights(EvolveInsightFilter{CompanyID: ts + 1234567, Q: fmt.Sprint(ts)})
	if err != nil {
		t.Fatal(err)
	}
	if totalOther != 2 {
		t.Fatalf("不相关 CompanyID 只应返回 global 共 2 条: total=%d", totalOther)
	}
	// Kind 过滤:total 在过滤之后算(3 条里只有 1 条 negative)。
	neg, totalNeg, err := d.ListEvolveInsights(EvolveInsightFilter{Kind: "negative", Q: fmt.Sprint(ts)})
	if err != nil {
		t.Fatal(err)
	}
	if totalNeg != 1 || len(neg) != 1 || neg[0].ID != fGlobal2 {
		t.Fatalf("kind 过滤 total 不对: total=%d", totalNeg)
	}
}

// TestEvolvePlaybookOutcomeAndCorroborate 剧本相似去重、outcome 计数与 avg_seconds
// 增量、印证自动转正、状态过滤与删除。
func TestEvolvePlaybookOutcomeAndCorroborate(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	ts := time.Now().UnixNano()
	title := fmt.Sprintf("MEMT-%d-Redis 未授权写计划任务拿权限剧本", ts)
	task1, task2 := ts+11, ts+12
	pid, err := d.CreateEvolvePlaybook(&EvolvePlaybook{
		Title:   title,
		Summary: "针对未鉴权 Redis 的标准动作序列 " + fmt.Sprint(ts),
		ScopeKind: "global", StackTags: []string{"redis"},
		Preconditions: map[string]any{"port": 6379, "auth": false},
		Steps: []EvolvePlaybookStep{
			{N: 1, Action: "探测 6379", Detail: "INFO 回显确认未鉴权"},
			{N: 2, Action: "写计划任务", Notes: "cron 路径按目标系统区分"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer evolveTestCleanupPlaybook(d, pid)

	// 空标题拒绝。
	if _, err := d.CreateEvolvePlaybook(&EvolvePlaybook{Title: " "}); err == nil {
		t.Fatalf("空 title 应被拒绝")
	}

	// 相似命中/未命中。
	hit, err := d.SimilarEvolvePlaybook(title, 0.75)
	if err != nil {
		t.Fatal(err)
	}
	if hit == nil || hit.ID != pid {
		t.Fatalf("同标题剧本应命中去重: %+v", hit)
	}
	if miss, err := d.SimilarEvolvePlaybook("zzqx 完全无关的剧本标题 no overlap", 0.75); err != nil || miss != nil {
		t.Fatalf("不相干标题不应命中: %+v %v", miss, err)
	}

	// outcome 计数与 avg_seconds:
	//   成功 100s → avg=100;失败 0s(不计样本);成功 200s → avg=(100*2+200)/3=133。
	if err := d.RecordEvolvePlaybookOutcome(pid, task1, true, 100); err != nil {
		t.Fatal(err)
	}
	if err := d.RecordEvolvePlaybookOutcome(pid, task1, false, 0); err != nil {
		t.Fatal(err)
	}
	if err := d.RecordEvolvePlaybookOutcome(pid, task2, true, 200); err != nil {
		t.Fatal(err)
	}
	rows, total, err := d.ListEvolvePlaybooks(EvolvePlaybookFilter{Q: fmt.Sprint(ts)})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ID != pid {
		t.Fatalf("Q 应只命中本剧本: total=%d", total)
	}
	p := rows[0]
	if p.SuccessCount != 2 || p.FailCount != 1 || p.AvgSeconds != 133 {
		t.Fatalf("outcome 计数不对: %+v", p)
	}
	if len(p.EvidenceTaskIDs) != 2 || len(p.SourceTaskIDs) != 2 {
		t.Fatalf("证据/来源任务应去重为 2 个: evidence=%#v source=%#v", p.EvidenceTaskIDs, p.SourceTaskIDs)
	}
	if v, ok := p.Preconditions["port"]; !ok || fmt.Sprint(v) != "6379" {
		t.Fatalf("preconditions 回读不对: %#v", p.Preconditions)
	}
	if len(p.Steps) != 2 || p.Steps[0].N != 1 || p.Steps[0].Action != "探测 6379" {
		t.Fatalf("steps 回读不对: %#v", p.Steps)
	}

	// 阈值 2 端到端:初始 corroboration=1 → 第 1 次印证即转正。
	title2 := fmt.Sprintf("MEMT-%d-第二个剧本待印证", ts)
	pid2, err := d.CreateEvolvePlaybook(&EvolvePlaybook{Title: title2, Summary: "MEMT 印证测试", Corroboration: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer evolveTestCleanupPlaybook(d, pid2)
	activated, err := d.CorroborateEvolvePlaybook(pid2, ts+21, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !activated {
		t.Fatalf("corroboration 1→2 达阈值 2 应自动转正")
	}
	rows2, _, err := d.ListEvolvePlaybooks(EvolvePlaybookFilter{Q: title2})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows2) != 1 || rows2[0].Status != "active" || rows2[0].Corroboration != 2 {
		t.Fatalf("剧本转正后状态不对: %+v", rows2)
	}

	// 不存在的行:outcome 静默无操作。
	if err := d.RecordEvolvePlaybookOutcome(-1, 1, true, 10); err != nil {
		t.Fatalf("不存在的 id outcome 应无操作: %v", err)
	}

	// 状态过滤 + Active 排除 + 删除。
	if err := d.SetEvolvePlaybookStatus(pid, "retired"); err != nil {
		t.Fatal(err)
	}
	actives, err := d.ActiveEvolvePlaybooks(nil, 200)
	if err != nil {
		t.Fatal(err)
	}
	if evolveTestContainsPlaybook(actives, pid) {
		t.Fatalf("retired 不应出现在 ActiveEvolvePlaybooks")
	}
	if !evolveTestContainsPlaybook(actives, pid2) {
		t.Fatalf("global active 剧本应出现在 ActiveEvolvePlaybooks")
	}
	if err := d.DeleteEvolvePlaybook(pid2); err != nil {
		t.Fatal(err)
	}
	_, totalAfter, err := d.ListEvolvePlaybooks(EvolvePlaybookFilter{Q: fmt.Sprint(ts)})
	if err != nil {
		t.Fatal(err)
	}
	if totalAfter != 1 {
		t.Fatalf("删除后 Q 只应剩 1 条: total=%d", totalAfter)
	}
}

// TestEvolveOverviewMaps 概览计数四状态键恒在(计数不受分类筛选影响)。
// 各测试 defer 清理后库里可能没有自进化行,这里自造一条洞察 + 一条剧本再断言。
func TestEvolveOverviewMaps(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	ts := time.Now().UnixNano()
	insightID, err := d.CreateEvolveInsight(&EvolveInsight{
		ScopeKind: "global", Title: fmt.Sprintf("MEMT-%d-概览测试洞察", ts),
		Content: "MEMT 概览测试", Status: "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer evolveTestCleanupInsight(d, insightID)
	playbookID, err := d.CreateEvolvePlaybook(&EvolvePlaybook{
		Title: fmt.Sprintf("MEMT-%d-概览测试剧本", ts), Summary: "MEMT 概览测试", Status: "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer evolveTestCleanupPlaybook(d, playbookID)

	ins, pb, err := d.EvolveOverview()
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"candidate", "active", "rejected", "retired"} {
		if _, ok := ins[status]; !ok {
			t.Fatalf("insights 缺状态键 %s: %#v", status, ins)
		}
		if _, ok := pb[status]; !ok {
			t.Fatalf("playbooks 缺状态键 %s: %#v", status, pb)
		}
	}
	if ins["active"] < 1 || pb["active"] < 1 {
		t.Fatalf("刚建了 active 行,计数不应为 0: %#v %#v", ins, pb)
	}
}
