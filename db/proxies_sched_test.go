package db

// 代理池调度规则的集成测试(需要真库,ARTEX_PG_DSN 门控):
// 自动升降级(boost/streak/封顶/降级)走的是 SQL CASE,只有真库跑得出来。
// 5499 是共享演示库:临时组用唯一名,测试结束删组(代理随外键级联删除)。

import (
	"fmt"
	"testing"
	"time"
)

// TestProxySchedRulesBoost 覆盖:
//  1. 新组存量 sched_rules='{}' → 读出来是归一化默认值(AutoBoost=true 等)
//  2. UpdateSchedRules 落库后读回一致
//  3. 连续成功:streak 递增,每逢 boost_every 倍数 priority + boost_step,封顶 boost_cap
//  4. 失败一次:streak 清零 + priority 降 demote_on_fail(下限 0)
//  5. 同组其他代理不受影响;SetProxyPriority 钳位与计数/TOP 榜
func TestProxySchedRulesBoost(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	// 关库的 Cleanup 注册在前(后执行),删组的注册在后(先执行):
	// t.Cleanup 在测试函数的 defer 之后才跑,若先 Close 再删组会静默失败、往共享库留脏数据。
	t.Cleanup(func() { d.Close() })

	// 建临时组:名字带纳秒时间戳,避免与共享库里的既有组撞名。
	name := fmt.Sprintf("sched-test-%d", time.Now().UnixNano())
	g, err := d.CreateProxyGroup(ProxyGroupInput{Name: name, Kind: ProxyGroupKindManual, Enabled: false})
	if err != nil {
		t.Fatalf("建组失败: %v", err)
	}
	// 级联删除组内代理,务必清理(共享演示库)。
	t.Cleanup(func() { _, _ = d.DeleteProxyGroup(g.ID) })
	gid := g.ID

	// 1) 新组没写过 sched_rules,存的是 DEFAULT '{}':零值必须归一化成默认,不能是全零。
	fresh, err := d.GetProxyGroup(gid)
	if err != nil {
		t.Fatalf("读组失败: %v", err)
	}
	wantDefault := SchedRules{AutoBoost: true, BoostEvery: 5, BoostStep: 1, BoostCap: 50, DemoteOnFail: 2}
	if fresh.Rules != wantDefault {
		t.Fatalf("存量 '{}' 行应归一化为默认 %+v,实得 %+v", wantDefault, fresh.Rules)
	}

	// 2) 更新规则并读回:boost_every=2 / step=3 / cap=5 / demote=2,方便手工推演。
	custom := SchedRules{AutoBoost: true, BoostEvery: 2, BoostStep: 3, BoostCap: 5, DemoteOnFail: 2}
	if err := d.UpdateSchedRules(gid, custom); err != nil {
		t.Fatalf("更新调度规则失败: %v", err)
	}
	updated, err := d.GetProxyGroup(gid)
	if err != nil {
		t.Fatalf("读组失败: %v", err)
	}
	if updated.Rules != custom {
		t.Fatalf("规则读回不一致: 期望 %+v,实得 %+v", custom, updated.Rules)
	}

	// 建两条代理(A 用来升降级,B 用来验证隔离)。
	pa, _, err := d.CreateProxy(ProxyInput{GroupID: gid, URL: "http://10.11.0.1:8080", Source: ProxySourceManual})
	if err != nil {
		t.Fatalf("建代理 A 失败: %v", err)
	}
	pb, _, err := d.CreateProxy(ProxyInput{GroupID: gid, URL: "http://10.11.0.2:8080", Source: ProxySourceManual})
	if err != nil {
		t.Fatalf("建代理 B 失败: %v", err)
	}

	alive := ProxyCheckResult{ProxyID: pa.ID, Alive: true, LatencyMs: 120, Rules: custom}
	// 3) 连续成功 4 次(every=2):第 2 次 0→3,第 4 次 3→6 被封到 cap=5。
	for i := 1; i <= 4; i++ {
		if err := d.ApplyProxyCheckResult(alive); err != nil {
			t.Fatalf("第 %d 次成功回写失败: %v", i, err)
		}
	}
	got, err := d.GetProxy(pa.ID)
	if err != nil {
		t.Fatalf("读代理 A 失败: %v", err)
	}
	if got.Streak != 4 {
		t.Errorf("连续成功 4 次 streak 应为 4,实得 %d", got.Streak)
	}
	if got.Priority != 5 {
		t.Errorf("两次升级(0+3, 3+3)且 cap=5,priority 应为 5,实得 %d", got.Priority)
	}

	// 5) 计数与 TOP 榜:只有 A 被升级;A 应排在 B 前。
	n, err := d.CountBoostedProxies(gid)
	if err != nil {
		t.Fatalf("CountBoostedProxies 失败: %v", err)
	}
	if n != 1 {
		t.Errorf("升级计数应为 1,实得 %d", n)
	}
	top, err := d.ListTopProxies(gid, 10)
	if err != nil {
		t.Fatalf("ListTopProxies 失败: %v", err)
	}
	if len(top) != 2 || top[0].ID != pa.ID || top[1].ID != pb.ID {
		t.Errorf("TOP 榜应为 [A,B],实得 %v", topIDs(top))
	}

	// 4) 失败一次:streak 清零,priority 5-2=3;fail_count=1 未达 maxFail=3,不判死。
	if err := d.ApplyProxyCheckResult(ProxyCheckResult{ProxyID: pa.ID, Alive: false, Err: "boom", Rules: custom}); err != nil {
		t.Fatalf("失败回写报错: %v", err)
	}
	got, err = d.GetProxy(pa.ID)
	if err != nil {
		t.Fatalf("读代理 A 失败: %v", err)
	}
	if got.Streak != 0 {
		t.Errorf("失败后 streak 应清零,实得 %d", got.Streak)
	}
	if got.Priority != 3 {
		t.Errorf("失败后 priority 应降为 3,实得 %d", got.Priority)
	}
	if got.Status != ProxyStatusAlive {
		t.Errorf("单次失败不该判死,实得 status=%s", got.Status)
	}

	// B 从未被测活,应保持初始值(隔离性)。
	gotB, err := d.GetProxy(pb.ID)
	if err != nil {
		t.Fatalf("读代理 B 失败: %v", err)
	}
	if gotB.Priority != 0 || gotB.Streak != 0 {
		t.Errorf("代理 B 不应被影响,实得 priority=%d streak=%d", gotB.Priority, gotB.Streak)
	}

	// SetProxyPriority:手动置顶生效,负值钳 0,不存在的 id 报 ErrNoRows。
	if err := d.SetProxyPriority(pb.ID, 10000); err != nil {
		t.Fatalf("SetProxyPriority 失败: %v", err)
	}
	if err := d.SetProxyPriority(pb.ID, -7); err != nil {
		t.Fatalf("SetProxyPriority 失败: %v", err)
	}
	gotB, _ = d.GetProxy(pb.ID)
	if gotB.Priority != 0 {
		t.Errorf("负值应钳为 0,实得 %d", gotB.Priority)
	}
	if err := d.SetProxyPriority(99999999, 1); err == nil {
		t.Error("不存在的代理应返回 ErrNoRows")
	}
}

// topIDs 是 TOP 榜断言用的 id 列表。
func topIDs(list []*Proxy) []int64 {
	out := make([]int64, 0, len(list))
	for _, p := range list {
		out = append(out, p.ID)
	}
	return out
}
