package recon

// 探活模块单测:铁律是绝不发起真实外网请求。
//
// 手段:DNS 一律注入(opts.Resolve 返回固定 IP);HTTP 一律注入 opts.Client——
// 要么 Transport 是埋了计数器的哑巴(证明守卫路径零请求),要么 DialContext
// 把任意地址的 TCP 连接重定向到本机 httptest.Server 的监听口(模拟「目标域
// 就在这台本地服务器上应答」),TLS 用 InsecureSkipVerify 吃下 httptest 自签证书。
// 全程不出 127.0.0.1。

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// publicDummy 是测试注入用的公网假 IP(仅作数据,永不真的连它——
// DialContext 已把连接劫持到本地 httptest.Server)。
var publicDummy = net.ParseIP("93.184.216.34")

// fakeResolve 返回一个注入用 resolver:恒答给出的 IP。
func fakeResolve(ips ...net.IP) func(context.Context, string) ([]net.IP, error) {
	return func(context.Context, string) ([]net.IP, error) { return ips, nil }
}

// redirectDial 把任意 network/addr 的拨号都劫持到本地监听口。
func redirectDial(ln net.Listener) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", ln.Addr().String())
	}
}

// zeroTransport 是埋了计数器的哑巴 Transport:任何 RoundTrip 都计数并报错,
// 用来证明守卫路径「一个 HTTP 包都没发」。
type zeroTransport struct {
	hits atomic.Int64
}

func (t *zeroTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.hits.Add(1)
	return nil, errors.New("守卫生效时不该有任何 HTTP 出行")
}

// ---- ClassifyIP 全分类 ----

func TestClassifyIP(t *testing.T) {
	cases := []struct {
		ip   net.IP
		want string
	}{
		{nil, "invalid"},
		// public
		{net.ParseIP("8.8.8.8"), "public"},
		{net.ParseIP("93.184.216.34"), "public"},
		{net.ParseIP("2606:4700::1111"), "public"},
		// private(含 4-in-6 映射)
		{net.ParseIP("10.1.2.3"), "private"},
		{net.ParseIP("172.16.31.9"), "private"},
		{net.ParseIP("172.31.255.255"), "private"},
		{net.ParseIP("192.168.0.1"), "private"},
		{net.ParseIP("fd00::1"), "private"},
		{net.ParseIP("::ffff:10.0.0.1"), "private"},
		// loopback
		{net.ParseIP("127.0.0.1"), "loopback"},
		{net.ParseIP("127.8.8.8"), "loopback"},
		{net.ParseIP("::1"), "loopback"},
		// linklocal
		{net.ParseIP("169.254.3.3"), "linklocal"},
		{net.ParseIP("fe80::1"), "linklocal"},
		// multicast
		{net.ParseIP("224.0.0.1"), "multicast"},
		{net.ParseIP("239.255.255.250"), "multicast"},
		{net.ParseIP("ff02::1"), "multicast"},
		// reserved(未指定/广播等非全球单播)
		{net.ParseIP("0.0.0.0"), "reserved"},
		{net.ParseIP("::"), "reserved"},
		{net.ParseIP("255.255.255.255"), "reserved"},
	}
	for _, c := range cases {
		if got := ClassifyIP(c.ip); got != c.want {
			t.Errorf("ClassifyIP(%v) = %q, 期望 %q", c.ip, got, c.want)
		}
	}
}

// ---- title 解析 ----

func TestParseTitle(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"常规", "<html><head><title>Hello World</title></head></html>", "Hello World"},
		{"大小写不敏感", "<TITLE>UPPER CASE</TITLE>", "UPPER CASE"},
		{"带属性", `<title class="main" id='t1' data-x=1>Attr Title</title>`, "Attr Title"},
		{"多行内容", "<title>line1\n\t line2\r\nline3</title>", "line1 line2 line3"},
		{"HTML 实体反转义", "<title>a &amp; b &lt;tags&gt;</title>", "a & b <tags>"},
		{"无标题", "<html><body>plain</body></html>", ""},
		{"未闭合", "<title>unterminated", ""},
		{"空体", "", ""},
		{"属性加跨行加实体", "<TITLE\n  class=\"x\">multi\n&amp; mixed</TITLE>", "multi & mixed"},
	}
	for _, c := range cases {
		if got := parseTitle([]byte(c.body)); got != c.want {
			t.Errorf("%s: parseTitle = %q, 期望 %q", c.name, got, c.want)
		}
	}
}

// ---- 内网守卫:解析结果非公网 → 零 HTTP 请求 ----

func TestLivenessIntranetGuardBlocksHTTP(t *testing.T) {
	cases := []struct {
		name string
		ips  []net.IP
	}{
		{"私网 10/8", []net.IP{net.ParseIP("10.1.2.3")}},
		{"环回 127/8", []net.IP{net.ParseIP("127.0.0.1")}},
		{"链路本地", []net.IP{net.ParseIP("169.254.1.9")}},
		{"私网混环回(无 public)", []net.IP{net.ParseIP("192.168.1.1"), net.ParseIP("127.0.0.1")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			zt := &zeroTransport{}
			res := ProbeDomains(context.Background(),
				[]string{"rebind.example.com"},
				ProbeOptions{
					Client:  &http.Client{Transport: zt, Timeout: 2 * time.Second},
					Resolve: fakeResolve(c.ips...),
				})
			if len(res) != 1 {
				t.Fatalf("结果数 = %d, 期望 1", len(res))
			}
			r := res[0]
			if r.Alive {
				t.Error("非公网解析不该判定存活")
			}
			if !strings.Contains(r.Err, "非公网") {
				t.Errorf("Err 应说明非公网跳过, 实际 %q", r.Err)
			}
			if r.URL != "" || r.StatusCode != 0 {
				t.Errorf("不该有任何 HTTP 痕迹: URL=%q StatusCode=%d", r.URL, r.StatusCode)
			}
			if n := zt.hits.Load(); n != 0 {
				t.Errorf("守卫生效时 HTTP 请求次数 = %d, 期望 0(DNS rebinding 防线被击穿)", n)
			}
			if len(r.IPs) != len(c.ips) {
				t.Errorf("IPs = %v, 期望解析结果原样记录 %v", r.IPs, c.ips)
			}
			if r.Technologies == nil {
				t.Error("Technologies 不该为 nil")
			}
		})
	}
}

// ---- https → http 回退与指纹采集 ----

func TestLivenessHTTPSFallsBackToHTTP(t *testing.T) {
	var hits atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Server", "unit-nginx")
		w.Header().Set("X-Powered-By", "PHP/7.4.3")
		fmt.Fprint(w, "<html><head><TITLE CLASS=\"p\">回退 页面</TITLE></head><body>ok</body></html>")
	}))
	defer ts.Close()

	res := ProbeDomains(context.Background(),
		[]string{"sub.example-test.com"},
		ProbeOptions{
			Client: &http.Client{
				Transport: &http.Transport{DialContext: redirectDial(ts.Listener)},
				Timeout:   5 * time.Second,
			},
			// 混入一个私网 IP:守卫按「存在 public 即放行」判定。
			Resolve: fakeResolve(net.ParseIP("10.9.9.9"), publicDummy),
		})
	if len(res) != 1 {
		t.Fatalf("结果数 = %d, 期望 1", len(res))
	}
	r := res[0]
	if !r.Alive {
		t.Fatalf("应存活, Err=%q", r.Err)
	}
	if !strings.HasPrefix(r.URL, "http://") {
		t.Errorf("https 失败应回退 http, 最终 URL = %q", r.URL)
	}
	if r.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, 期望 200", r.StatusCode)
	}
	if r.Title != "回退 页面" {
		t.Errorf("Title = %q, 期望 %q", r.Title, "回退 页面")
	}
	if r.ContentLength <= 0 {
		t.Errorf("ContentLength = %d, 应为响应体字节数", r.ContentLength)
	}
	if len(r.Technologies) != 2 || r.Technologies[0] != "unit-nginx" || r.Technologies[1] != "PHP/7.4.3" {
		t.Errorf("Technologies = %v, 期望 [unit-nginx PHP/7.4.3]", r.Technologies)
	}
	if len(r.IPs) != 2 || r.IPs[0] != "10.9.9.9" || r.IPs[1] != "93.184.216.34" {
		t.Errorf("IPs = %v, 期望解析结果原样", r.IPs)
	}
	// https 尝试在 TLS 握手即失败(明文端口吃不了 ClientHello),
	// 到不了 handler;只有回退的 http 请求真正到达。
	if n := hits.Load(); n != 1 {
		t.Errorf("到达 handler 的请求数 = %d, 期望 1(https 尝试不应产生真实请求)", n)
	}
}

// ---- https 直连成功 + 最终 URL 记录(跟随跳转) ----

func TestLivenessHTTPSSuccessFollowsRedirect(t *testing.T) {
	var hits atomic.Int64
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		w.Header().Set("Server", "unit-tls")
		fmt.Fprint(w, "<html><title>HTTPS OK</title></html>")
	}))
	defer ts.Close()

	res := ProbeDomains(context.Background(),
		[]string{"tls.example-test.com"},
		ProbeOptions{
			Client: &http.Client{
				Transport: &http.Transport{
					DialContext: redirectDial(ts.Listener),
					// 测试专用:httptest 证书不对 example-test.com 签发。
					TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
				},
				Timeout: 5 * time.Second,
			},
			Resolve: fakeResolve(publicDummy),
		})
	if len(res) != 1 {
		t.Fatalf("结果数 = %d, 期望 1", len(res))
	}
	r := res[0]
	if !r.Alive {
		t.Fatalf("应存活, Err=%q", r.Err)
	}
	if !strings.HasPrefix(r.URL, "https://") {
		t.Errorf("https 先行且成功, 最终 URL 应为 https, 实际 %q", r.URL)
	}
	if !strings.HasSuffix(r.URL, "/final") {
		t.Errorf("应记录跳转后的最终 URL, 实际 %q", r.URL)
	}
	if r.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, 期望 200(跳转后)", r.StatusCode)
	}
	if r.Title != "HTTPS OK" {
		t.Errorf("Title = %q, 期望 %q", r.Title, "HTTPS OK")
	}
	// 302 + /final 共 2 次;若误回退 http 还会更多。
	if n := hits.Load(); n != 2 {
		t.Errorf("handler 收到请求数 = %d, 期望 2", n)
	}
}

// ---- 全部失败:Err 汇总 https/http 各一句话 ----

func TestLivenessAllAttemptsFailSummary(t *testing.T) {
	res := ProbeDomains(context.Background(),
		[]string{"dead.example-test.com"},
		ProbeOptions{
			Client: &http.Client{
				Transport: &http.Transport{
					DialContext: func(context.Context, string, string) (net.Conn, error) {
						return nil, errors.New("dial boom: connection refused")
					},
				},
				Timeout: 2 * time.Second,
			},
			Resolve: fakeResolve(publicDummy),
		})
	r := res[0]
	if r.Alive {
		t.Fatal("全失败不该存活")
	}
	if !strings.Contains(r.Err, "https:") || !strings.Contains(r.Err, "http:") {
		t.Errorf("Err 应含 https/http 两段摘要, 实际 %q", r.Err)
	}
	if !strings.Contains(r.Err, "dial boom") {
		t.Errorf("Err 应含末次错误内容, 实际 %q", r.Err)
	}
	if r.StatusCode != 0 || r.URL != "" {
		t.Errorf("失败结果不该带 StatusCode/URL: %d %q", r.StatusCode, r.URL)
	}
}

// ---- ctx 取消 ----

func TestLivenessBatchCtxPreCanceled(t *testing.T) {
	zt := &zeroTransport{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var resolveCalls atomic.Int64
	res := ProbeDomains(ctx,
		[]string{"a.example-test.com", "b.example-test.com", "c.example-test.com"},
		ProbeOptions{
			Client: &http.Client{Transport: zt},
			Resolve: func(context.Context, string) ([]net.IP, error) {
				resolveCalls.Add(1)
				return []net.IP{publicDummy}, nil
			},
		})
	if res == nil || len(res) != 3 {
		t.Fatalf("返回应非 nil 且等长, 实际 len=%d", len(res))
	}
	for i, r := range res {
		if r.Alive {
			t.Errorf("res[%d] 不该存活", i)
		}
		if !strings.Contains(r.Err, "context canceled") {
			t.Errorf("res[%d].Err 应为 ctx.Err() 文案, 实际 %q", i, r.Err)
		}
	}
	if n := zt.hits.Load(); n != 0 {
		t.Errorf("取消后不该有 HTTP 请求, 实际 %d", n)
	}
	if n := resolveCalls.Load(); n != 0 {
		t.Errorf("取消后不该再解析 DNS, 实际 %d", n)
	}
}

func TestLivenessBatchCtxCancelMidFlight(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-started // 首个域名已在途
		cancel()
		close(release)
	}()

	res := ProbeDomains(ctx,
		[]string{"slow.example-test.com", "q1.example-test.com", "q2.example-test.com"},
		ProbeOptions{
			Concurrency: 1,
			Client: &http.Client{
				Transport: &http.Transport{DialContext: redirectDial(ts.Listener)},
				Timeout:   5 * time.Second,
			},
			Resolve: fakeResolve(publicDummy),
		})
	if len(res) != 3 {
		t.Fatalf("结果数 = %d, 期望 3", len(res))
	}
	for i, r := range res {
		if r.Alive {
			t.Errorf("res[%d] 不该存活", i)
		}
		if !strings.Contains(r.Err, "context canceled") {
			t.Errorf("res[%d].Err 应含取消文案, 实际 %q", i, r.Err)
		}
	}
}

// ---- 并发批量:结果等长同序、slot 不串 ----

func TestLivenessBatchConcurrencyOrder(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Host 头保留原始域名(劫持的只是 TCP 对端),回显进标题做对账。
		fmt.Fprintf(w, "<html><title>host=%s</title></html>", r.Host)
	}))
	defer ts.Close()

	domains := make([]string, 12)
	for i := range domains {
		domains[i] = fmt.Sprintf("d%02d.example-test.com", i)
	}
	res := ProbeDomains(context.Background(), domains, ProbeOptions{
		Concurrency: 4,
		Client: &http.Client{
			Transport: &http.Transport{DialContext: redirectDial(ts.Listener)},
			Timeout:   5 * time.Second,
		},
		Resolve: fakeResolve(publicDummy),
	})
	if len(res) != len(domains) {
		t.Fatalf("结果数 = %d, 期望 %d", len(res), len(domains))
	}
	for i, r := range res {
		if r.Domain != domains[i] {
			t.Errorf("res[%d].Domain = %q, 期望 %q(结果须与输入同序)", i, r.Domain, domains[i])
		}
		if !r.Alive || r.Title != "host="+domains[i] {
			t.Errorf("res[%d] = Alive=%v Title=%q, 期望存活且标题对上域名", i, r.Alive, r.Title)
		}
	}
}

// ---- 输入边界 ----

func TestLivenessEmptyAndInvalidInput(t *testing.T) {
	res := ProbeDomains(context.Background(), nil, ProbeOptions{})
	if res == nil {
		t.Fatal("空输入返回值必须非 nil")
	}
	if len(res) != 0 {
		t.Fatalf("空输入应返回空 slice, 实际 len=%d", len(res))
	}

	zt := &zeroTransport{}
	res = ProbeDomains(context.Background(),
		[]string{"-leading-dash.example.com", "not a domain"},
		ProbeOptions{
			Client:  &http.Client{Transport: zt},
			Resolve: fakeResolve(publicDummy),
		})
	if len(res) != 2 {
		t.Fatalf("结果数 = %d, 期望 2", len(res))
	}
	for i, r := range res {
		if r.Alive || !strings.Contains(r.Err, "域名不合法") {
			t.Errorf("res[%d] 应为域名不合法失败, 实际 Alive=%v Err=%q", i, r.Alive, r.Err)
		}
	}
	if n := zt.hits.Load(); n != 0 {
		t.Errorf("非法域名不该发任何请求, 实际 %d", n)
	}
}

// ---- isCertError:降级重试的触发判定 ----

func TestIsCertError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"未知 CA", x509.UnknownAuthorityError{}, true},
		{"主机名不匹配", x509.HostnameError{Host: "x.example.com"}, true},
		{"证书过期", x509.CertificateInvalidError{Reason: x509.Expired}, true},
		{
			"tls 包装的验证错误(Go 1.20+ 实际形态)",
			fmt.Errorf("tls: failed to verify: %w", &tls.CertificateVerificationError{
				Err: x509.UnknownAuthorityError{},
			}),
			true,
		},
		{"连接拒绝", errors.New("dial tcp: connection refused"), false},
		{"超时", context.DeadlineExceeded, false},
		{"TLS 非证书错误", errors.New("tls: first record does not look like a TLS handshake"), false},
	}
	for _, c := range cases {
		if got := isCertError(c.err); got != c.want {
			t.Errorf("%s: isCertError = %v, 期望 %v", c.name, got, c.want)
		}
	}
}

// ---- buildProbeClients:InsecureTLS 降级客户端的构造与注入语义 ----

func TestBuildProbeClientsInsecureTLS(t *testing.T) {
	ctx := context.Background()

	// 注入 Client:原样使用,绝不做 Transport 定制(契约明文)。
	injected := &http.Client{Timeout: time.Second}
	pc := buildProbeClients(ctx, ProbeOptions{Client: injected, InsecureTLS: true})
	if pc.primary != injected {
		t.Error("注入的 Client 应原样作为 primary")
	}
	if pc.insecure != nil {
		t.Error("注入 Client 时不得构造降级客户端")
	}

	// 内部客户端 + InsecureTLS=true:降级客户端 Transport 必须跳过校验,
	// 且不继承环境变量代理(克隆自 newHTTPClient,Proxy 显式 nil)。
	t.Setenv("HTTPS_PROXY", "http://env-proxy.example.com:3128")
	pc = buildProbeClients(ctx, ProbeOptions{InsecureTLS: true, Timeout: 3 * time.Second})
	if pc.insecure == nil {
		t.Fatal("InsecureTLS=true 时应有降级客户端")
	}
	tr, ok := pc.insecure.Transport.(*http.Transport)
	if !ok {
		t.Fatal("降级客户端 Transport 类型不对")
	}
	if tr.TLSClientConfig == nil || !tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("降级客户端必须 InsecureSkipVerify")
	}
	if tr.Proxy != nil {
		t.Error("降级客户端不得继承环境变量代理")
	}
	// primary 本体不受影响,仍走完整校验。
	ptr, ok := pc.primary.Transport.(*http.Transport)
	if !ok || ptr.TLSClientConfig != nil && ptr.TLSClientConfig.InsecureSkipVerify {
		t.Error("primary 不该跳过证书校验")
	}

	// 内部客户端 + InsecureTLS=false:无降级客户端。
	pc = buildProbeClients(ctx, ProbeOptions{})
	if pc.insecure != nil {
		t.Error("InsecureTLS=false 不该有降级客户端")
	}
}
