package proxycenter

// 调度规则(quality 策略)的单元测试:
//  1. qualityWeight 分档 —— 优先级/连胜/延迟三路信号换算成抽签权重
//  2. 加权随机统计 —— 高权重候选在 crypto/rand 随机源下被显著更频繁选中
//  3. SchedRules 归一化 —— 零值(存量 '{}' 行)回默认,越界值钳回默认

import (
	"net/url"
	"testing"
)

// TestQualityWeight 验证权重公式的每个分档。
func TestQualityWeight(t *testing.T) {
	cases := []struct {
		name string
		cand Candidate
		want int
	}{
		{"兜底权重1", Candidate{}, 1},
		{"延迟未测0不加分", Candidate{LatencyMs: 0}, 1},
		{"延迟快档加4", Candidate{LatencyMs: 200}, 5},
		{"延迟临界201走中档", Candidate{LatencyMs: 201}, 3},
		{"延迟中档加2", Candidate{LatencyMs: 500}, 3},
		{"延迟慢档加1", Candidate{LatencyMs: 501}, 2},
		{"延迟1000仍慢档", Candidate{LatencyMs: 1000}, 2},
		{"延迟超1000不加分", Candidate{LatencyMs: 1001}, 1},
		{"优先级乘2", Candidate{Priority: 3}, 7},
		{"连胜计入", Candidate{Streak: 5}, 6},
		{"连胜封顶20", Candidate{Streak: 30}, 21},
		{"三路信号叠加", Candidate{Priority: 2, Streak: 4, LatencyMs: 150}, 1 + 4 + 4 + 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := qualityWeight(tc.cand); got != tc.want {
				t.Errorf("qualityWeight(%+v) = %d, 期望 %d", tc.cand, got, tc.want)
			}
		})
	}
}

// TestQualityPickFavorsHighWeight:加权随机下,高权重候选被选中的频率应显著更高。
// crypto/rand 的抽样抖动不可控,2000 次给宽松边界(期望占比 ~93%,只要求 >75%)。
func TestQualityPickFavorsHighWeight(t *testing.T) {
	lowURL := &url.URL{Scheme: "http", Host: "127.0.0.1:1"}
	highURL := &url.URL{Scheme: "http", Host: "127.0.0.1:2"}
	// 低权重 = 1(未测、无优先级);高权重 = 1 + 20*2 + 20(连胜封顶) + 4(快档) = 65。
	list := []Candidate{
		{ID: 1, Raw: lowURL.String(), URL: lowURL},
		{ID: 2, Raw: highURL.String(), URL: highURL, Priority: 20, Streak: 20, LatencyMs: 100},
	}
	p := NewPool(PoolOptions{GroupID: 1, Strategy: "quality"})
	p.candidates.Store(&list)

	const samples = 2000
	counts := map[int64]int{}
	for i := 0; i < samples; i++ {
		cand, ok := p.pick(nil)
		if !ok {
			t.Fatal("quality 策略应总能选到候选")
		}
		counts[cand.ID]++
	}
	if got := counts[2]; got <= samples*3/4 {
		t.Errorf("高权重候选被选 %d/%d 次,应远高于低权重(权重 65:1)", got, samples)
	}
	if got := counts[1]; got >= samples/4 {
		t.Errorf("低权重候选被选 %d/%d 次,占比异常偏高", got, samples)
	}
}

// TestQualityPickExcludes:exclude 必须照常生效(failover 重试跳过已失败候选)。
func TestQualityPickExcludes(t *testing.T) {
	u1 := &url.URL{Scheme: "http", Host: "127.0.0.1:1"}
	u2 := &url.URL{Scheme: "http", Host: "127.0.0.1:2"}
	list := []Candidate{
		{ID: 1, Raw: u1.String(), URL: u1, Priority: 100},
		{ID: 2, Raw: u2.String(), URL: u2},
	}
	p := NewPool(PoolOptions{GroupID: 1, Strategy: "quality"})
	p.candidates.Store(&list)

	// 高权重的 1 被排除后,只能选到 2。
	for i := 0; i < 50; i++ {
		cand, ok := p.pick(map[int64]bool{1: true})
		if !ok {
			t.Fatal("排除后仍应有候选可选")
		}
		if cand.ID != 2 {
			t.Fatalf("排除 1 后仍选到 %d", cand.ID)
		}
	}
	// 全部排除:找不到。
	if _, ok := p.pick(map[int64]bool{1: true, 2: true}); ok {
		t.Fatal("全部被排除时应返回未找到")
	}
}

// TestNormalizeSchedRules:零值回默认(存量 '{}' 行的语义),越界钳回默认,合法值保留。
func TestNormalizeSchedRules(t *testing.T) {
	// 全零 → 全套默认(含 AutoBoost=true)。
	var zero SchedRules
	NormalizeSchedRules(&zero)
	if zero != (SchedRules{AutoBoost: true, BoostEvery: 5, BoostStep: 1, BoostCap: 50, DemoteOnFail: 2}) {
		t.Errorf("零值应归一化为默认配置,实得 %+v", zero)
	}

	// 用户显式关掉 auto_boost:只要其余字段非零就尊重存储值,不误判为未配置。
	explicit := SchedRules{AutoBoost: false, BoostEvery: 5, BoostStep: 1, BoostCap: 50, DemoteOnFail: 0}
	NormalizeSchedRules(&explicit)
	if explicit != (SchedRules{AutoBoost: false, BoostEvery: 5, BoostStep: 1, BoostCap: 50, DemoteOnFail: 0}) {
		t.Errorf("显式配置(含 demote_on_fail=0)应原样保留,实得 %+v", explicit)
	}

	// 越界值逐字段钳回默认。
	clamped := SchedRules{AutoBoost: false, BoostEvery: 0, BoostStep: 999, BoostCap: -3, DemoteOnFail: 101}
	NormalizeSchedRules(&clamped)
	if clamped.BoostEvery != 5 || clamped.BoostStep != 1 || clamped.BoostCap != 50 || clamped.DemoteOnFail != 2 {
		t.Errorf("越界字段应钳回默认,实得 %+v", clamped)
	}

	// 合法边界值保留(只测上界:下界已被零值/越界用例覆盖)。
	bounds := SchedRules{AutoBoost: true, BoostEvery: 200, BoostStep: 50, BoostCap: 1000, DemoteOnFail: 100}
	NormalizeSchedRules(&bounds)
	if bounds.BoostEvery != 200 || bounds.BoostStep != 50 || bounds.BoostCap != 1000 || bounds.DemoteOnFail != 100 {
		t.Errorf("合法边界值应保留,实得 %+v", bounds)
	}
}
