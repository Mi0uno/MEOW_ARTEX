package proxycenter

// 代理中心核心逻辑测试:调度策略选择、代理地址解析、源清单解析。
// 这些是不依赖网络与数据库的纯逻辑,是「动态调度真的在按策略选」的最低保证。

import (
	"net/url"
	"strings"
	"testing"
)

// mkCandidates 造一组候选(id 递增,延迟递增)。
func mkCandidates(n int) []Candidate {
	out := make([]Candidate, 0, n)
	for i := 1; i <= n; i++ {
		u, _ := url.Parse("http://10.0.0." + string(rune('0'+i)) + ":8080")
		out = append(out, Candidate{ID: int64(i), Raw: u.String(), URL: u, LatencyMs: i * 10})
	}
	return out
}

// TestPickRoundRobin 轮询:连续 N 次应覆盖全部候选,不重复偏向某条。
func TestPickRoundRobin(t *testing.T) {
	p := NewPool(PoolOptions{Strategy: "round_robin"})
	list := mkCandidates(4)
	p.candidates.Store(&list)

	seen := map[int64]int{}
	for i := 0; i < 8; i++ {
		c, ok := p.pick(nil)
		if !ok {
			t.Fatalf("第 %d 次未选出候选", i)
		}
		seen[c.ID]++
	}
	// 8 次 4 条,每条应恰好 2 次。
	for id, n := range seen {
		if n != 2 {
			t.Errorf("轮询不均匀:代理 #%d 被选 %d 次,期望 2 次", id, n)
		}
	}
	if len(seen) != 4 {
		t.Errorf("只覆盖 %d 条候选,期望 4 条", len(seen))
	}
}

// TestPickLeastLatency 最低延迟优先:永远选延迟最小的那条。
func TestPickLeastLatency(t *testing.T) {
	p := NewPool(PoolOptions{Strategy: "least_latency"})
	list := mkCandidates(3) // 延迟 10/20/30
	p.candidates.Store(&list)

	for i := 0; i < 5; i++ {
		c, ok := p.pick(nil)
		if !ok {
			t.Fatal("未选出候选")
		}
		if c.LatencyMs != 10 {
			t.Errorf("最低延迟优先选到了 %dms,期望 10ms", c.LatencyMs)
		}
	}
}

// TestPickFailoverSkipsExcluded failover:排除已失败的候选后应取下一个。
func TestPickFailoverSkipsExcluded(t *testing.T) {
	p := NewPool(PoolOptions{Strategy: "failover"})
	list := mkCandidates(3)
	p.candidates.Store(&list)

	exclude := map[int64]bool{1: true}
	c, ok := p.pick(exclude)
	if !ok {
		t.Fatal("排除 #1 后应仍能选出候选")
	}
	if c.ID == 1 {
		t.Errorf("failover 未跳过已失败的代理 #1")
	}
	// 全排除应返回未找到(而不是死循环或返回空指针)。
	exclude[2], exclude[3] = true, true
	if _, ok := p.pick(exclude); ok {
		t.Error("全部候选被排除时应返回未找到")
	}
}

// TestPickRandomCoversAll 随机:多次抽样应能覆盖到多条(而不是恒定一条)。
func TestPickRandomCoversAll(t *testing.T) {
	p := NewPool(PoolOptions{Strategy: "random"})
	list := mkCandidates(5)
	p.candidates.Store(&list)

	seen := map[int64]bool{}
	for i := 0; i < 200; i++ {
		c, ok := p.pick(nil)
		if !ok {
			t.Fatal("未选出候选")
		}
		seen[c.ID] = true
	}
	if len(seen) < 4 {
		t.Errorf("随机调度 200 次只覆盖 %d 条,随机性异常", len(seen))
	}
}

// TestPickEmptyPool 空池必须安全返回未找到(否则调用方会 nil 解引用)。
func TestPickEmptyPool(t *testing.T) {
	p := NewPool(PoolOptions{Strategy: "round_robin"})
	if _, ok := p.pick(nil); ok {
		t.Error("空池应返回未找到")
	}
	empty := []Candidate{}
	p.candidates.Store(&empty)
	if _, ok := p.pick(nil); ok {
		t.Error("空候选列表应返回未找到")
	}
}

// TestCleanHostPorts 清单解析:抽 host:port、去重、丢弃内网与非法端口。
func TestCleanHostPorts(t *testing.T) {
	text := `
1.2.3.4:8080
5.6.7.8:3128
1.2.3.4:8080
127.0.0.1:8080
192.168.1.1:3128
10.0.0.5:8080
169.254.1.1:80
9.9.9.9:99999
8.8.8.8:1080
`
	got := cleanHostPorts(text)
	want := map[string]bool{"1.2.3.4:8080": true, "5.6.7.8:3128": true, "8.8.8.8:1080": true}
	if len(got) != len(want) {
		t.Fatalf("解析出 %d 条 %v,期望 %d 条", len(got), got, len(want))
	}
	for _, hp := range got {
		if !want[hp] {
			t.Errorf("出现了不该保留的条目: %s", hp)
		}
	}
}

// TestParseExitIP 出口 IP 提取:兼容 JSON 与纯文本,多级代理取第一个。
func TestParseExitIP(t *testing.T) {
	cases := []struct{ body, want string }{
		{`{"origin": "1.2.3.4"}`, "1.2.3.4"},
		{`{"origin": "1.2.3.4, 5.6.7.8"}`, "1.2.3.4"},
		{`{"ip": "9.9.9.9"}`, "9.9.9.9"},
		{"8.8.8.8", "8.8.8.8"},
		{"  7.7.7.7  ", "7.7.7.7"},
		{`{"foo": "bar"}`, ""},
		{"not an ip", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := parseExitIP([]byte(c.body)); got != c.want {
			t.Errorf("parseExitIP(%q) = %q,期望 %q", c.body, got, c.want)
		}
	}
}

// TestValidateFetchURLRejectsInternal 源站拉取必须拒绝内网地址(SSRF 防护)。
func TestValidateFetchURLRejectsInternal(t *testing.T) {
	bad := []string{
		"http://127.0.0.1:8080/proxy",
		"http://localhost/proxy",
		"http://192.168.1.1/proxy",
		"http://10.0.0.1/proxy",
		"http://169.254.169.254/latest/meta-data",
		"ftp://1.2.3.4/proxy",
		"http://[::1]/proxy",
	}
	for _, u := range bad {
		if err := validateFetchURL(u); err == nil {
			t.Errorf("%q 应被拒绝(内网/非 http)", u)
		}
	}
}

// TestProviderRegistrySane 注册表自检:key 唯一、必填字段齐全、凭据要求一致。
func TestProviderRegistrySane(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range ProviderList() {
		if d.Key == "" || d.Title == "" {
			t.Errorf("源 %+v 缺少 key 或 title", d)
		}
		if seen[d.Key] {
			t.Errorf("源 key 重复: %s", d.Key)
		}
		seen[d.Key] = true
		if d.DefaultURL == "" {
			t.Errorf("源 %s 缺少默认地址", d.Key)
		}
		switch d.Kind {
		case ProviderKindCommercial, ProviderKindPublic, ProviderKindWild:
		default:
			t.Errorf("源 %s 的类别非法: %s", d.Key, d.Kind)
		}
		switch d.Protocol {
		case "http", "https", "socks5":
		default:
			t.Errorf("源 %s 的协议非法: %s", d.Key, d.Protocol)
		}
		if d.Extract == nil {
			t.Errorf("源 %s 缺少解析函数", d.Key)
		}
	}
	// 三档都要有源,否则「接入主流代理池 API 和公益代理池 API」不成立。
	if len(ProvidersByKind(ProviderKindCommercial)) == 0 {
		t.Error("注册表里没有商业代理池源")
	}
	if len(ProvidersByKind(ProviderKindPublic)) == 0 {
		t.Error("注册表里没有公益代理池源")
	}
	if len(ProvidersByKind(ProviderKindWild)) == 0 {
		t.Error("注册表里没有在野搜集源")
	}
}

// TestProviderBuildURLInjectsCreds 商业源必须把凭据拼进请求地址。
// 这里的 key 是明显的测试占位串,不是任何真实凭据。
func TestProviderBuildURLInjectsCreds(t *testing.T) {
	def := ProviderByKey("kuaidaili")
	if def == nil {
		t.Fatal("快代理源未注册")
	}
	const fixtureKey = "test-fixture-not-a-real-key"
	got := def.BuildURL(SourceAuth{APIKey: fixtureKey})
	if got == "" {
		t.Fatal("BuildURL 返回空")
	}
	if !strings.Contains(got, fixtureKey) {
		t.Errorf("BuildURL 未注入 API Key: %s", got)
	}
}

// TestMarshalParams JSON 参数转字符串(整数不带小数点)。
func TestMarshalParams(t *testing.T) {
	got := MarshalParams([]byte(`{"num":100,"region":"bj","flag":true}`))
	if got["num"] != "100" {
		t.Errorf("num = %q,期望 100(整数不该带小数点)", got["num"])
	}
	if got["region"] != "bj" {
		t.Errorf("region = %q", got["region"])
	}
	if got["flag"] != "true" {
		t.Errorf("flag = %q", got["flag"])
	}
	if len(MarshalParams(nil)) != 0 {
		t.Error("nil 参数应返回空 map")
	}
}
