package proxycenter

// 地区识别测试:缓存命中、失败降级、限速、内网 IP 短路。
// 这些是「自动识别代理地区」的核心保证——尤其不能把内网 IP 送去外部接口,
// 也不能在接口失败时把整批解析搞崩。

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStore 是内存版缓存,用于验证两级缓存与限速行为。
type fakeStore struct {
	mu      sync.Mutex
	cache   map[string]*GeoResult
	lookups int
	puts    int
}

func newFakeStore() *fakeStore { return &fakeStore{cache: map[string]*GeoResult{}} }

func (f *fakeStore) lookup(ips []string) (map[string]*GeoResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups++
	out := map[string]*GeoResult{}
	for _, ip := range ips {
		if r, ok := f.cache[ip]; ok {
			out[ip] = r
		}
	}
	return out, nil
}

func (f *fakeStore) put(g GeoResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts++
	cp := g
	f.cache[g.IP] = &cp
	return nil
}

// TestGeoRejectsNonPublicIPs 内网/保留地址必须直接判失败,不得发外部请求。
// 这也是安全约束的落点:不能把内网地址交给第三方接口。
func TestGeoRejectsNonPublicIPs(t *testing.T) {
	fs := newFakeStore()
	g := NewGeoResolver(GeoOptions{
		Lookup: fs.lookup, Put: fs.put,
		Enabled:     true,
		MinInterval: time.Millisecond,
	})
	bad := []string{"127.0.0.1", "10.0.0.5", "192.168.1.1", "169.254.169.254", "::1", "not-an-ip", ""}
	for _, ip := range bad {
		r := g.Resolve(context.Background(), ip)
		if r == nil {
			t.Errorf("%q 应返回结果(而非 nil)", ip)
			continue
		}
		if r.OK {
			t.Errorf("%q 不该被判定为可解析的公网地址", ip)
		}
		if r.Err == "" {
			t.Errorf("%q 应带失败原因", ip)
		}
	}
	// 全部被短路,不该产生任何外部请求(这里用「无缓存写入」间接证明没走到 fetch)。
	if fs.puts != 0 {
		t.Errorf("内网地址不该被缓存为解析结果,却有 %d 次写入", fs.puts)
	}
}

// TestGeoUsesCache 命中缓存时不再请求外部接口。
func TestGeoUsesCache(t *testing.T) {
	fs := newFakeStore()
	fs.cache["1.2.3.4"] = &GeoResult{IP: "1.2.3.4", OK: true, CountryCode: "US", Country: "United States"}
	g := NewGeoResolver(GeoOptions{
		Lookup: fs.lookup, Put: fs.put,
		Enabled:     false, // 关掉外部请求:能拿到结果就说明走的缓存
		MinInterval: time.Millisecond,
	})
	r := g.Resolve(context.Background(), "1.2.3.4")
	if r == nil || !r.OK || r.CountryCode != "US" {
		t.Fatalf("缓存未命中: %+v", r)
	}
	// 第二次应走内存缓存,不再查 DB。
	before := fs.lookups
	g.Resolve(context.Background(), "1.2.3.4")
	if fs.lookups != before {
		t.Error("第二次解析不该再查 DB(应命中进程内缓存)")
	}
}

// TestGeoDisabledNoExternalCall 关闭时只用缓存,不请求外部接口。
func TestGeoDisabledNoExternalCall(t *testing.T) {
	fs := newFakeStore()
	g := NewGeoResolver(GeoOptions{
		Lookup: fs.lookup, Put: fs.put,
		Enabled: false, MinInterval: time.Millisecond,
	})
	r := g.Resolve(context.Background(), "8.8.8.8")
	if r == nil {
		t.Fatal("应返回结果")
	}
	if r.OK {
		t.Error("关闭状态不该解析成功")
	}
	if !strings.Contains(r.Err, "关闭") {
		t.Errorf("失败原因应说明已关闭,实际: %q", r.Err)
	}
}

// TestGeoRateLimit 两次外部请求之间应遵守最小间隔(主动限速,避免被接口封)。
func TestGeoRateLimit(t *testing.T) {
	g := NewGeoResolver(GeoOptions{MinInterval: 120 * time.Millisecond})
	start := time.Now()
	g.waitTurn()
	g.waitTurn()
	elapsed := time.Since(start)
	if elapsed < 120*time.Millisecond {
		t.Errorf("限速未生效:两次调用只隔了 %s,应 >= 120ms", elapsed)
	}
}

// TestGeoResolveBatchDedupes 批量解析应按 IP 去重(同一出口 IP 只解析一次)。
func TestGeoResolveBatchDedupes(t *testing.T) {
	fs := newFakeStore()
	g := NewGeoResolver(GeoOptions{
		Lookup: fs.lookup, Put: fs.put,
		Enabled: false, MinInterval: time.Millisecond,
	})
	seen := []string{}
	g.ResolveBatch(context.Background(), []string{"8.8.8.8", "8.8.8.8", " 8.8.8.8 ", "", "1.1.1.1"}, func(r *GeoResult) {
		seen = append(seen, r.IP)
	})
	if len(seen) != 2 {
		t.Errorf("去重后应解析 2 个 IP,实际 %d 个: %v", len(seen), seen)
	}
}

// TestGeoResolveBatchStopsOnCancel ctx 取消后应尽快停止(批量解析可能很长)。
func TestGeoResolveBatchStopsOnCancel(t *testing.T) {
	fs := newFakeStore()
	g := NewGeoResolver(GeoOptions{
		Lookup: fs.lookup, Put: fs.put,
		Enabled: false, MinInterval: time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n := 0
	g.ResolveBatch(ctx, []string{"8.8.8.8", "1.1.1.1", "9.9.9.9"}, func(*GeoResult) { n++ })
	if n != 0 {
		t.Errorf("ctx 已取消,不该继续解析,实际解析了 %d 个", n)
	}
}

// TestSCDNProviderRegistered SCDN 源应已注册且形状正确。
func TestSCDNProviderRegistered(t *testing.T) {
	for _, key := range []string{"scdn_http", "scdn_socks5"} {
		def := ProviderByKey(key)
		if def == nil {
			t.Fatalf("源 %s 未注册", key)
		}
		if def.Kind != ProviderKindPublic {
			t.Errorf("%s 应为公益池类别,实际 %s", key, def.Kind)
		}
		if def.NeedKey || def.NeedAuth {
			t.Errorf("%s 不需要凭据,不该标记 NeedKey/NeedAuth", key)
		}
		u := def.BuildURL(SourceAuth{})
		if !strings.Contains(u, "proxy.scdn.io") {
			t.Errorf("%s 的取用地址不对: %s", key, u)
		}
		if !strings.Contains(u, "count=") {
			t.Errorf("%s 应带 count 参数: %s", key, u)
		}
	}
	// http 与 socks5 两个源必须走不同协议,否则重复接入没有意义。
	if ProviderByKey("scdn_http").Protocol == ProviderByKey("scdn_socks5").Protocol {
		t.Error("scdn_http 与 scdn_socks5 的协议不该相同")
	}
}

// TestBuildURLHonorsEndpointOverride 用户覆盖的 api_url 只该换端点,不该丢掉参数/凭据。
//
// 这是实测踩过的坑:前端接入对话框总会把 api_url 预填成 default_url,
// 若覆盖时直接拿它当最终 URL,BuildURL 就被整个跳过——count 等参数永远不生效
// (SCDN 因此永远只返回默认 1 条),商业源的 API Key 也永远不会被注入。
func TestBuildURLHonorsEndpointOverride(t *testing.T) {
	mirror := "https://mirror.example.com/get_proxy.php"

	// 带参数的公益源:覆盖端点后参数必须还在。
	scdn := ProviderByKey("scdn_http")
	u := scdn.BuildURL(SourceAuth{Base: mirror})
	if !strings.Contains(u, "mirror.example.com") {
		t.Errorf("端点覆盖未生效: %s", u)
	}
	if !strings.Contains(u, "count=") {
		t.Errorf("端点覆盖后 count 参数丢了: %s", u)
	}
	if !strings.Contains(u, "protocol=http") {
		t.Errorf("端点覆盖后 protocol 参数丢了: %s", u)
	}
	if strings.Contains(u, "proxy.scdn.io") {
		t.Errorf("端点覆盖后不该再打到原地址: %s", u)
	}

	// 带凭据的商业源:覆盖端点后凭据必须还在。
	const fixtureKey = "test-fixture-not-a-real-key"
	kdl := ProviderByKey("kuaidaili")
	u2 := kdl.BuildURL(SourceAuth{APIKey: fixtureKey, Base: mirror})
	if !strings.Contains(u2, "mirror.example.com") {
		t.Errorf("商业源端点覆盖未生效: %s", u2)
	}
	if !strings.Contains(u2, fixtureKey) {
		t.Errorf("端点覆盖后 API Key 丢了(商业源会完全不可用): %s", u2)
	}

	// 不覆盖时应走源自带地址。
	u3 := scdn.BuildURL(SourceAuth{})
	if !strings.Contains(u3, "proxy.scdn.io") {
		t.Errorf("未覆盖时应用默认地址: %s", u3)
	}
}

// TestFetchOverrideKeepsParams Fetch 层同样要保证覆盖端点不丢参数。
func TestFetchOverrideKeepsParams(t *testing.T) {
	// 用打桩 Transport 截获最终请求 URL(不真的发网络请求)。
	var captured string
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			captured = req.URL.String()
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("1.2.3.4:8080\n")),
				Header:     http.Header{},
			}, nil
		}),
	}
	def := ProviderByKey("scdn_http")
	list, err := def.Fetch(client, SourceAuth{}, "https://mirror.example.com/p.php", nil)
	if err != nil {
		t.Fatalf("Fetch 失败: %v", err)
	}
	if captured == "" {
		t.Fatal("未截获到请求 URL")
	}
	if !strings.Contains(captured, "mirror.example.com") {
		t.Errorf("Fetch 未采用覆盖端点: %s", captured)
	}
	if !strings.Contains(captured, "count=") {
		t.Errorf("Fetch 覆盖端点后丢了参数(SCDN 会只返回 1 条): %s", captured)
	}
	if len(list) != 1 {
		t.Errorf("应解析出 1 条代理,实际 %d 条", len(list))
	}
}

// roundTripFunc 让测试能打桩 http.Client 的 Transport。
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
