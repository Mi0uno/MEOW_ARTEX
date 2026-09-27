package proxycenter

// 本次修复的回归测试:
//  1. taskTag —— 从代理请求里解出任务归属(proxy_requests.task_id 的归因来源)
//  2. handleConnect 换候选重试 —— 抽到坏代理不再直接 502
//  3. 测活必须验 CONNECT 隧道 —— 只转发明文的代理不得判为 alive

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestTaskTag 解析代理请求里的任务身份(池侧归因的唯一来源)。
func TestTaskTag(t *testing.T) {
	basic := func(user, pass string) string {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
	}
	cases := []struct {
		name     string
		header   string
		wantID   int64
		wantStag string
	}{
		{"带阶段", basic("t43", "exploit"), 43, "exploit"},
		{"无阶段", basic("t43", ""), 43, ""},
		{"recon 阶段", basic("t7", "recon"), 7, "recon"},
		{"无身份头", "", 0, ""},
		{"非 Basic", "Bearer abc", 0, ""},
		{"用户名不是任务标记", basic("proxyuser", "pw"), 0, ""},
		{"非法 base64", "Basic !!!", 0, ""},
		{"任务号非法", basic("tabc", "x"), 0, ""},
		{"任务号为零", basic("t0", "x"), 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
			if tc.header != "" {
				r.Header.Set("Proxy-Authorization", tc.header)
			}
			id, stage := taskTag(r)
			if id != tc.wantID || stage != tc.wantStag {
				t.Errorf("taskTag = (%d,%q), 期望 (%d,%q)", id, stage, tc.wantID, tc.wantStag)
			}
		})
	}
}

// startConnectProxy 起一个最小可用的 HTTP 代理,只实现 CONNECT(转发到目标)。
func startConnectProxy(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "only CONNECT", http.StatusMethodNotAllowed)
			return
		}
		up, err := net.DialTimeout("tcp", r.Host, 3*time.Second)
		if err != nil {
			http.Error(w, "dial failed", http.StatusBadGateway)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			up.Close()
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		client, buf, err := hj.Hijack()
		if err != nil {
			up.Close()
			return
		}
		defer client.Close()
		defer up.Close()
		if _, err := buf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		if err := buf.Flush(); err != nil {
			return
		}
		done := make(chan struct{}, 2)
		go func() { _, _ = io.Copy(up, buf); done <- struct{}{} }()
		go func() { _, _ = io.Copy(client, up); done <- struct{}{} }()
		<-done
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestConnectRetriesOtherCandidates:round_robin 下第一条候选是死端口时,
// 必须换下一条把请求救回来(旧实现只试 1 条,直接 502)。
func TestConnectRetriesOtherCandidates(t *testing.T) {
	echo := startEchoServer(t)
	good := startConnectProxy(t)

	// 死候选排第一:旧实现会在这里直接失败。
	deadURL, _ := url.Parse("http://127.0.0.1:1")
	goodURL, _ := url.Parse(good.URL)
	list := []Candidate{
		{ID: 1, Raw: deadURL.String(), URL: deadURL},
		{ID: 2, Raw: goodURL.String(), URL: goodURL},
	}
	p := NewPool(PoolOptions{GroupID: 1, Strategy: "round_robin", Timeout: 3 * time.Second})
	p.candidates.Store(&list)

	var recMu sync.Mutex
	var records []RequestRecord
	p.opts.OnRequest = func(rec RequestRecord) {
		recMu.Lock()
		records = append(records, rec)
		recMu.Unlock()
	}
	collected := func() (failed, succeeded bool) {
		recMu.Lock()
		defer recMu.Unlock()
		for _, rec := range records {
			if rec.ProxyID == 1 && !rec.OK {
				failed = true
			}
			if rec.ProxyID == 2 && rec.OK {
				succeeded = true
			}
		}
		return
	}

	// 经池发一次 CONNECT。
	proxyURL, _ := url.Parse("http://" + p.listenAddrForTest(t))
	conn, err := dialViaHTTPProxy(context.Background(), proxyURL, echo, 3*time.Second)
	if err != nil {
		t.Fatalf("经池建隧道失败(没有换候选重试?): %v", err)
	}

	// 隧道真的通了:发一行、收一行。
	if _, err := conn.Write([]byte("ping\n")); err != nil {
		t.Fatalf("隧道写入失败: %v", err)
	}
	buf := make([]byte, 16)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := conn.Read(buf)
	if err != nil || n == 0 {
		t.Fatalf("隧道读回失败: n=%d err=%v", n, err)
	}
	// 成功流水在隧道关闭后写(见 handleConnect),先关再断言。
	_ = conn.Close()

	// 死候选应留下失败流水,好候选留下成功流水。
	var failed, succeeded bool
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if failed, succeeded = collected(); failed && succeeded {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !failed {
		t.Error("第一条候选失败应留下失败流水(否则仪表盘看不到坏代理)")
	}
	if !succeeded {
		t.Error("重试成功的候选应留下成功流水")
	}
}

// TestTagProxyAddrRoundTrip:编码(TagProxyAddr)与解码(taskTag)必须闭环——
// 两者是同一份契约的两端,分开放就会悄悄漂移。中间经一次真实的
// HTTP 代理请求头往返,而不是直接调函数,才真正证明「客户端会自动带上它」。
func TestTagProxyAddrRoundTrip(t *testing.T) {
	tagged := TagProxyAddr("http://127.0.0.1:18080", 43, "exploit")
	r := httptest.NewRequest(http.MethodConnect, tagged, nil)
	// 模拟 Go 的 Transport:把代理 URL 的 userinfo 转成 Proxy-Authorization。
	if u, err := url.Parse(tagged); err == nil && u.User != nil {
		pw, _ := u.User.Password()
		token := base64.StdEncoding.EncodeToString([]byte(u.User.Username() + ":" + pw))
		r.Header.Set("Proxy-Authorization", "Basic "+token)
	}
	id, stage := taskTag(r)
	if id != 43 || stage != "exploit" {
		t.Fatalf("往返后 = (%d,%q),期望 (43,exploit)", id, stage)
	}

	// 不带身份的地址必须原样返回,且解析结果为 0。
	if got := TagProxyAddr("http://127.0.0.1:18080", 0, "exploit"); got != "http://127.0.0.1:18080" {
		t.Errorf("taskID<=0 时应原样返回,实得 %q", got)
	}
	if got := TagProxyAddr("", 43, ""); got != "" {
		t.Errorf("空地址应原样返回,实得 %q", got)
	}
}

// listenAddrForTest 在测试里真起一个池监听器(handleConnect 需要真实连接)。
func (p *Pool) listenAddrForTest(t *testing.T) string {
	t.Helper()
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("启动池监听器失败: %v", err)
	}
	t.Cleanup(p.Stop)
	return p.ListenAddr()
}

// TestConnectRecordsTaskAttribution:带任务标识的代理地址(形如
// http://t43:exploit@127.0.0.1:port,server 的 ProxyAddrForTaskTagged 就是这么生成的)
// 必须让出网流水带上 task_id 与 stage——这是界面上「任务到底走没走代理」的唯一依据。
// 旧实现里这两个字段永远是空的,池是共享监听器,不靠请求自带身份就无从归因。
func TestConnectRecordsTaskAttribution(t *testing.T) {
	echo := startEchoServer(t)
	good := startConnectProxy(t)
	goodURL, _ := url.Parse(good.URL)
	list := []Candidate{{ID: 7, Raw: goodURL.String(), URL: goodURL}}

	p := NewPool(PoolOptions{GroupID: 1, Strategy: "round_robin", Timeout: 3 * time.Second})
	p.candidates.Store(&list)

	var recMu sync.Mutex
	var records []RequestRecord
	p.opts.OnRequest = func(rec RequestRecord) {
		recMu.Lock()
		records = append(records, rec)
		recMu.Unlock()
	}

	addr := p.listenAddrForTest(t)
	tagged, err := url.Parse("http://" + url.UserPassword("t43", "exploit").String() + "@" + addr)
	if err != nil {
		t.Fatalf("构造带标识代理地址失败: %v", err)
	}

	conn, err := dialViaHTTPProxy(context.Background(), tagged, echo, 3*time.Second)
	if err != nil {
		t.Fatalf("经带标识的池地址建隧道失败: %v", err)
	}
	_, _ = conn.Write([]byte("ping\n"))
	buf := make([]byte, 16)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, rerr := conn.Read(buf); rerr != nil {
		t.Fatalf("隧道读回失败: %v", rerr)
	}
	_ = conn.Close()

	var got *RequestRecord
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		recMu.Lock()
		for i := range records {
			if records[i].OK && records[i].ProxyID == 7 {
				got = &records[i]
			}
		}
		recMu.Unlock()
		if got != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got == nil {
		t.Fatal("没有收到成功流水")
	}
	if got.TaskID != 43 {
		t.Errorf("流水 task_id 应为 43,实得 %d(任务归因丢失)", got.TaskID)
	}
	if got.Stage != "exploit" {
		t.Errorf("流水 stage 应为 exploit,实得 %q", got.Stage)
	}
}

// TestConnectRecordsNoAttributionForAnonymousClients:不带任务标识的客户端
// (手工 curl、浏览器、平台自身探测)必须记成 0,不能臆断归因到某个任务。
func TestConnectRecordsNoAttributionForAnonymousClients(t *testing.T) {
	echo := startEchoServer(t)
	good := startConnectProxy(t)
	goodURL, _ := url.Parse(good.URL)
	list := []Candidate{{ID: 9, Raw: goodURL.String(), URL: goodURL}}

	p := NewPool(PoolOptions{GroupID: 1, Strategy: "round_robin", Timeout: 3 * time.Second})
	p.candidates.Store(&list)

	var recMu sync.Mutex
	var records []RequestRecord
	p.opts.OnRequest = func(rec RequestRecord) {
		recMu.Lock()
		records = append(records, rec)
		recMu.Unlock()
	}

	plain, _ := url.Parse("http://" + p.listenAddrForTest(t))
	conn, err := dialViaHTTPProxy(context.Background(), plain, echo, 3*time.Second)
	if err != nil {
		t.Fatalf("建隧道失败: %v", err)
	}
	_, _ = conn.Write([]byte("ping\n"))
	buf := make([]byte, 16)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _ = conn.Read(buf)
	_ = conn.Close()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		recMu.Lock()
		n := len(records)
		recMu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	recMu.Lock()
	defer recMu.Unlock()
	if len(records) == 0 {
		t.Fatal("没有收到流水")
	}
	for _, rec := range records {
		if rec.TaskID != 0 {
			t.Errorf("无标识客户端不该被归因到任务,实得 task_id=%d", rec.TaskID)
		}
	}
}

// startEchoServer 起一个 TCP 回显服务,充当 CONNECT 的目标。
func startEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起回显服务失败: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); c.Close() }()
		}
	}()
	return ln.Addr().String()
}

// TestCheckRejectsPlainHTTPOnlyProxy:只能转发明文、不能建 CONNECT 隧道的代理,
// 不得被判为 alive——它支撑不了任务流量(HTTPS)。
func TestCheckRejectsPlainHTTPOnlyProxy(t *testing.T) {
	echoAddr := startEchoServer(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"origin":"1.2.3.4"}`))
	}))
	defer target.Close()

	// 只做明文转发的假代理:CONNECT 一律 405。
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			http.Error(w, "CONNECT not allowed", http.StatusMethodNotAllowed)
			return
		}
		resp, err := http.Get(r.URL.String()) //nolint:noctx // 测试桩
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		_, _ = io.Copy(w, resp.Body)
	}))
	defer plain.Close()

	c := &Checker{TestURL: target.URL, Timeout: 3 * time.Second, Concurrency: 1,
		ConnectProbeTarget: echoAddr}
	out := c.checkOne(context.Background(), CheckTarget{ID: 1, URL: plain.URL})
	if out.Alive {
		t.Fatal("只能转发明文的代理不应判为 alive(任务需要 HTTPS 隧道)")
	}
	if !strings.Contains(out.Err, "CONNECT") {
		t.Errorf("失败原因应指明缺少 CONNECT 能力,实得: %q", out.Err)
	}
}

// TestCheckAcceptsConnectCapableProxy:既能转发明文、又能建隧道的代理正常判活,
// 且仍能采到出口 IP。
func TestCheckAcceptsConnectCapableProxy(t *testing.T) {
	echoAddr := startEchoServer(t)

	targetPlain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"origin":"5.6.7.8"}`))
	}))
	defer targetPlain.Close()

	var mu sync.Mutex
	var plainHits, connectHits int
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			mu.Lock()
			connectHits++
			mu.Unlock()
			up, err := net.DialTimeout("tcp", r.Host, 3*time.Second)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			hj, _ := w.(http.Hijacker)
			client, buf, err := hj.Hijack()
			if err != nil {
				up.Close()
				return
			}
			defer client.Close()
			defer up.Close()
			_, _ = buf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
			_ = buf.Flush()
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(up, buf); done <- struct{}{} }()
			go func() { _, _ = io.Copy(client, up); done <- struct{}{} }()
			<-done
			return
		}
		mu.Lock()
		plainHits++
		mu.Unlock()
		resp, err := http.Get(r.URL.String()) //nolint:noctx // 测试桩
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		_, _ = io.Copy(w, resp.Body)
	}))
	defer proxy.Close()

	c := &Checker{TestURL: targetPlain.URL, Timeout: 3 * time.Second, Concurrency: 1,
		ConnectProbeTarget: echoAddr}
	out := c.checkOne(context.Background(), CheckTarget{ID: 2, URL: proxy.URL})
	if !out.Alive {
		t.Fatalf("具备隧道能力的代理应判为 alive,实得 err=%q", out.Err)
	}
	if out.ExitIP != "5.6.7.8" {
		t.Errorf("出口 IP 应为 5.6.7.8,实得 %q", out.ExitIP)
	}
	mu.Lock()
	defer mu.Unlock()
	if plainHits == 0 || connectHits == 0 {
		t.Errorf("应同时验证明文转发与隧道,实得 plain=%d connect=%d", plainHits, connectHits)
	}
}
