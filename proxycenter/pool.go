package proxycenter

// 本地代理池监听器:一个真实的 HTTP 代理,把进来的请求按策略转发到组内某条上游代理。
//
// 这是「代理池模式」的落地点:agent 的出网地址指向本监听器(127.0.0.1:<port>),
// 由它动态选一条上游代理出去,并把「本次调度到哪条代理」记进 proxy_requests——
// 仪表盘的「动态调度」就是读这张表。
//
// 这样做的原因:平台里构造 http.Client 的地方有二十余处,逐个改成「读代理设置」
// 既易漏又难维护;而它们全都已经通过 Manager.ProxyAddr() 取出口地址。把调度做在
// 一个本地监听器后面,出口切换只需改 ProxyAddr() 一处,且天然对 socks5 与 http
// 上游统一处理。
//
// 支持的入口形态:
//   - CONNECT host:port  (HTTPS 与大多数隧道流量)
//   - 绝对 URI 的普通 HTTP 请求(GET http://…)
//
// 支持的上游形态:http / https(手动 CONNECT 隧道)、socks5(经 x/net/proxy)。

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	xproxy "golang.org/x/net/proxy"
)

// maxProxyAttempts 是单次请求最多尝试的候选数。
//
// 为什么必须重试而不能试一条就报错:池里装的是公开/在野代理,单条随时失效。
// 只试一条时,整个池的可用性等于「本次恰好抽到的那条」,一次抽坏就是一次任务
// 出网失败——实测组内 30 条「alive」里只有 6 条能建 HTTPS 隧道,round_robin
// 一条不重试的失败率因此接近一半,任务侧表现为「切了代理池反而连不上」。
const maxProxyAttempts = 3

// Candidate 是调度候选:一条可用于出网的代理。
type Candidate struct {
	ID        int64
	Raw       string // 原始地址(含凭据),仅进程内使用
	URL       *url.URL
	LatencyMs int
	Region    string
	Source    string
}

// RequestRecord 是一次出网的流水(异步写库)。
type RequestRecord struct {
	GroupID    int64
	ProxyID    int64
	ProxyURL   string
	Method     string
	Host       string
	URL        string
	StatusCode int
	LatencyMs  int
	BytesIn    int64
	BytesOut   int64
	OK         bool
	Error      string
	TaskID     int64
	Stage      string
}

// PoolOptions 是构造监听器的依赖注入。
type PoolOptions struct {
	GroupID    int64
	Strategy   string
	ListenAddr string // 形如 127.0.0.1:18080;端口为 0 时由内核分配
	// Refresh 从库中取当前可调度的代理(alive 优先)。
	Refresh func() ([]Candidate, error)
	// OnRequest 记录一次出网流水(实现方应异步落库,不得阻塞代理路径)。
	OnRequest func(RequestRecord)
	// OnProxyFailure 某条代理出网失败时回调(用于累计失败次数、必要时降级)。
	OnProxyFailure func(proxyID int64)
	// Timeout 是连上游与握手的超时。
	Timeout time.Duration
	Logf    func(format string, args ...any)
}

// Pool 是本地代理池监听器。
type Pool struct {
	opts PoolOptions

	listener net.Listener
	server   *http.Server

	// candidates 是内存中的候选快照,后台定期刷新;代理路径只读快照,不查库。
	candidates atomic.Pointer[[]Candidate]
	rr         atomic.Uint64 // round_robin 游标

	// transports 按候选 id 缓存 Transport,复用连接池(否则每请求新建会拖慢且泄漏)。
	trMu       sync.Mutex
	transports map[int64]*http.Transport

	// active 记录最近一次被调度到的代理(仪表盘「当前调度」用,免查库)。
	activeMu sync.RWMutex
	active   *Candidate

	stopOnce sync.Once
	stopped  chan struct{}
}

// NewPool 构造监听器(不启动)。
func NewPool(opts PoolOptions) *Pool {
	if opts.Timeout <= 0 {
		opts.Timeout = 15 * time.Second
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	return &Pool{
		opts:       opts,
		transports: map[int64]*http.Transport{},
		stopped:    make(chan struct{}),
	}
}

// ListenAddr 返回实际监听地址(端口为 0 时是内核分配的真实端口)。
func (p *Pool) ListenAddr() string {
	if p.listener == nil {
		return ""
	}
	return p.listener.Addr().String()
}

// ProxyURL 返回可供 agent 使用的代理地址(带 http:// 前缀)。
func (p *Pool) ProxyURL() string {
	addr := p.ListenAddr()
	if addr == "" {
		return ""
	}
	// 监听在 0.0.0.0/[::] 时,给 agent 用回环地址即可(同机)。
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if host == "" || host == "::" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// Start 开始监听并启动后台刷新。
func (p *Pool) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", p.opts.ListenAddr)
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", p.opts.ListenAddr, err)
	}
	p.listener = ln
	// 先同步刷一次候选,避免刚启动时无代理可用。
	p.refreshCandidates()
	p.server = &http.Server{
		Handler:           p,
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() {
		if err := p.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			select {
			case <-p.stopped:
			default:
				p.opts.Logf("代理池监听 %s 退出: %v", ln.Addr(), err)
			}
		}
	}()
	go p.refreshLoop(ctx)
	return nil
}

// Stop 关闭监听器与缓存的连接池。
func (p *Pool) Stop() {
	p.stopOnce.Do(func() {
		close(p.stopped)
		if p.server != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = p.server.Shutdown(ctx)
		}
		p.trMu.Lock()
		for _, tr := range p.transports {
			tr.CloseIdleConnections()
		}
		p.transports = map[int64]*http.Transport{}
		p.trMu.Unlock()
	})
}

// refreshLoop 定期刷新候选:测活结果会变,不能让快照永久陈旧。
func (p *Pool) refreshLoop(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.stopped:
			return
		case <-t.C:
			p.refreshCandidates()
		}
	}
}

// refreshCandidates 拉取候选并换掉快照(失败保留旧快照:短暂 DB 抖动不该让池子空转)。
func (p *Pool) refreshCandidates() {
	if p.opts.Refresh == nil {
		return
	}
	list, err := p.opts.Refresh()
	if err != nil {
		p.opts.Logf("刷新候选失败(沿用旧快照): %v", err)
		return
	}
	p.candidates.Store(&list)
}

// ActiveProxy 返回最近一次调度到的代理地址(无则空串)。
func (p *Pool) ActiveProxy() string {
	p.activeMu.RLock()
	defer p.activeMu.RUnlock()
	if p.active == nil {
		return ""
	}
	return p.active.Raw
}

// CandidateCount 返回当前候选数。
func (p *Pool) CandidateCount() int {
	c := p.candidates.Load()
	if c == nil {
		return 0
	}
	return len(*c)
}

// pick 按策略选一条上游代理。
//
//   - round_robin   : 游标自增轮转,让每条代理都被均匀使用
//   - random        : 随机(天然分散,且无需维护状态)
//   - least_latency : 候选已按延迟升序,取第一条
//   - failover      : 固定取第一条,失败时由上层重试下一条
//
// 返回 (候选, 是否找到)。exclude 用于 failover 重试时跳过已失败的 id。
func (p *Pool) pick(exclude map[int64]bool) (*Candidate, bool) {
	c := p.candidates.Load()
	if c == nil || len(*c) == 0 {
		return nil, false
	}
	list := *c
	// failover/least_latency 依赖「候选已按延迟升序」,取满足条件的第一条。
	if p.opts.Strategy == "least_latency" || p.opts.Strategy == "failover" {
		for i := range list {
			if exclude == nil || !exclude[list[i].ID] {
				return &list[i], true
			}
		}
		return nil, false
	}
	if p.opts.Strategy == "random" {
		// 从随机起点线性探测,避免为了排除几条而全量过滤。
		start := randIntN(len(list))
		for i := 0; i < len(list); i++ {
			cand := &list[(start+i)%len(list)]
			if exclude == nil || !exclude[cand.ID] {
				return cand, true
			}
		}
		return nil, false
	}
	// round_robin
	start := int(p.rr.Add(1)-1) % len(list)
	for i := 0; i < len(list); i++ {
		cand := &list[(start+i)%len(list)]
		if exclude == nil || !exclude[cand.ID] {
			return cand, true
		}
	}
	return nil, false
}

// randIntN 返回 [0, n) 的随机数,用于 random 调度策略。
//
// 用 crypto/rand 而非 math/rand:调度起点若可预测,上游就能预判我们走哪条代理
// (在野代理里存在互相串通/同源出口的情况),降低流量分散的意义。crypto/rand 在
// 这里的开销可忽略(一次调用约百纳秒),不值得为省这点开销引入可预测性。
func randIntN(n int) int {
	if n <= 1 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		// 熵源不可用属极端情况:退回轮询游标,保证调度不中断。
		return int(time.Now().UnixNano() % int64(n))
	}
	return int(v.Int64())
}

// ServeHTTP 实现 http.Handler:CONNECT 走隧道,普通请求走转发。
func (p *Pool) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	p.handleForward(w, r)
}

// taskTag 从一个代理请求里解出「这是哪个任务的出网」。
//
// 池是组级共享监听器(一个组一个端口),所以任务身份只能由请求自己带:调用方把
// 任务标识写进代理 URL 的 userinfo(形如 http://t43:exploit@127.0.0.1:18080),
// Go 的 Transport 与 curl 都会把它转成 Proxy-Authorization: Basic。解不出来就
// 返回 0(非任务流量:手工 curl、浏览器、平台自身的探测),不臆断归因。
//
// 编码侧是 TagProxyAddr——两者必须成对改动,所以放在同一个文件里。
func taskTag(r *http.Request) (taskID int64, stage string) {
	raw := r.Header.Get("Proxy-Authorization")
	const prefix = "Basic "
	if len(raw) <= len(prefix) || !strings.EqualFold(raw[:len(prefix)], prefix) {
		return 0, ""
	}
	dec, err := base64.StdEncoding.DecodeString(raw[len(prefix):])
	if err != nil {
		return 0, ""
	}
	user, pass, _ := strings.Cut(string(dec), ":")
	if !strings.HasPrefix(user, "t") {
		return 0, ""
	}
	id, err := strconv.ParseInt(user[1:], 10, 64)
	if err != nil || id <= 0 {
		return 0, ""
	}
	return id, pass
}

// TagProxyAddr 把一个池监听地址打上任务身份,返回可直接用作 HTTP_PROXY 的地址。
//
// 形如 http://127.0.0.1:18080 → http://t43:exploit@127.0.0.1:18080。
// taskID<=0 或地址不合法时原样返回(归因缺失只是少一条审计,不能因此让调用方
// 拿不到出口地址)。stage 作为密码位传递,可为空。
//
// 「谁能打标」由 server 判定:只有确认是本池监听地址时才调用本函数——全局代理
// 与 MITM 出口不是本池的流量,打了标也没人解。
func TagProxyAddr(addr string, taskID int64, stage string) string {
	if taskID <= 0 {
		return addr
	}
	u, err := url.Parse(addr)
	if err != nil || u.Host == "" {
		return addr
	}
	u.User = url.UserPassword(fmt.Sprintf("t%d", taskID), stage)
	return u.String()
}

// handleConnect 处理 CONNECT 隧道。
//
// 建立隧道失败时依次换候选重试(最多 maxProxyAttempts 条)。重试对 CONNECT 是
// 安全的:握手失败意味着到目标的字节一个都没发出去,不会重复投递。只试一条会让
// 池的可用性退化成「抽到的那条」的可用性——公开代理池里这个值很低。
func (p *Pool) handleConnect(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	taskID, stage := taskTag(r)
	target := r.Host
	if !strings.Contains(target, ":") {
		target += ":443"
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "该服务器不支持连接劫持", http.StatusInternalServerError)
		return
	}
	clientConn, clientBuf, err := hj.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer clientConn.Close()

	exclude := map[int64]bool{}
	var lastErr error
	for attempt := 0; attempt < maxProxyAttempts; attempt++ {
		cand, found := p.pick(exclude)
		if !found {
			break
		}
		upConn, derr := p.dialThrough(r.Context(), cand, target)
		if derr != nil {
			lastErr = derr
			exclude[cand.ID] = true
			p.noteFailure(cand, r, "CONNECT "+target, derr, time.Since(start), taskID, stage)
			continue
		}
		p.markActive(cand)
		// 告诉客户端隧道已建立。
		if _, werr := clientBuf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); werr != nil {
			upConn.Close()
			return
		}
		if ferr := clientBuf.Flush(); ferr != nil {
			upConn.Close()
			return
		}
		var up, down int64
		done := make(chan struct{}, 2)
		go func() {
			n, _ := io.Copy(upConn, clientBuf)
			atomic.AddInt64(&up, n)
			// 客户端读完即关写方向,让上游感知 EOF。
			if cw, ok := upConn.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			}
			done <- struct{}{}
		}()
		go func() {
			n, _ := io.Copy(clientConn, upConn)
			atomic.AddInt64(&down, n)
			done <- struct{}{}
		}()
		<-done
		upConn.Close()
		clientConn.Close()
		<-done
		p.record(RequestRecord{
			GroupID: p.opts.GroupID, ProxyID: cand.ID, ProxyURL: cand.Raw,
			Method: "CONNECT", Host: hostOnly(target), URL: "https://" + target,
			StatusCode: 200, LatencyMs: int(time.Since(start).Milliseconds()),
			BytesOut: atomic.LoadInt64(&up), BytesIn: atomic.LoadInt64(&down),
			OK: true, TaskID: taskID, Stage: stage,
		})
		return
	}
	// 一条都没建起来:回 502,并记账(否则仪表盘看不到失败)。
	msg := "无可用代理"
	if lastErr != nil {
		msg = lastErr.Error()
	}
	_, _ = clientBuf.WriteString("HTTP/1.1 502 Bad Gateway\r\n\r\n")
	_ = clientBuf.Flush()
	p.record(RequestRecord{
		GroupID: p.opts.GroupID, Method: "CONNECT", Host: hostOnly(target),
		URL: "https://" + target, StatusCode: 502,
		LatencyMs: int(time.Since(start).Milliseconds()), OK: false, Error: msg,
		TaskID: taskID, Stage: stage,
	})
}

// handleForward 处理普通 HTTP 请求(绝对 URI 形式)。
func (p *Pool) handleForward(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	taskID, stage := taskTag(r)
	if !r.URL.IsAbs() {
		http.Error(w, "代理需要绝对 URI 请求", http.StatusBadRequest)
		return
	}
	exclude := map[int64]bool{}
	var lastErr error
	for attempt := 0; attempt < maxProxyAttempts; attempt++ {
		cand, found := p.pick(exclude)
		if !found {
			break
		}
		tr := p.transportFor(cand)
		outReq := r.Clone(r.Context())
		// 逐跳头必须剥掉,否则会被上游代理误用。
		stripHopByHop(outReq.Header)
		outReq.RequestURI = ""
		resp, err := tr.RoundTrip(outReq)
		if err != nil {
			lastErr = err
			exclude[cand.ID] = true
			p.noteFailure(cand, r, r.URL.String(), err, time.Since(start), taskID, stage)
			continue
		}
		p.markActive(cand)
		stripHopByHop(resp.Header)
		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		n, _ := io.Copy(w, resp.Body)
		resp.Body.Close()
		reqLen := r.ContentLength
		if reqLen < 0 {
			reqLen = 0
		}
		p.record(RequestRecord{
			GroupID: p.opts.GroupID, ProxyID: cand.ID, ProxyURL: cand.Raw,
			Method: r.Method, Host: r.URL.Host, URL: r.URL.String(),
			StatusCode: resp.StatusCode, LatencyMs: int(time.Since(start).Milliseconds()),
			BytesIn: n, BytesOut: reqLen, OK: resp.StatusCode < 400,
			TaskID: taskID, Stage: stage,
		})
		return
	}
	msg := "无可用代理"
	if lastErr != nil {
		msg = lastErr.Error()
	}
	http.Error(w, msg, http.StatusBadGateway)
	p.record(RequestRecord{
		GroupID: p.opts.GroupID, Method: r.Method, Host: r.URL.Host, URL: r.URL.String(),
		StatusCode: 502, LatencyMs: int(time.Since(start).Milliseconds()), OK: false, Error: msg,
		TaskID: taskID, Stage: stage,
	})
}

// transportFor 取(或建)候选对应的 Transport:复用连接池,避免每请求重建。
func (p *Pool) transportFor(cand *Candidate) *http.Transport {
	p.trMu.Lock()
	defer p.trMu.Unlock()
	if tr, ok := p.transports[cand.ID]; ok {
		return tr
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyURL(cand.URL),
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   p.opts.Timeout,
		ExpectContinueTimeout: time.Second,
		// 上游代理多半是匿名转发,不能假设它们支持 HTTP/2 隧道。
		ForceAttemptHTTP2: false,
	}
	p.transports[cand.ID] = tr
	return tr
}

// dialThrough 经候选代理建立到 target 的 TCP 隧道。
//
// http/https 上游:手写 CONNECT(需自己拼 Proxy-Authorization)。
// socks5 上游:交给 x/net/proxy。
func (p *Pool) dialThrough(ctx context.Context, cand *Candidate, target string) (net.Conn, error) {
	switch cand.URL.Scheme {
	case "socks5":
		var auth *xproxy.Auth
		if u := cand.URL.User; u != nil {
			pw, _ := u.Password()
			auth = &xproxy.Auth{User: u.Username(), Password: pw}
		}
		d, err := xproxy.SOCKS5("tcp", cand.URL.Host, auth, &net.Dialer{Timeout: p.opts.Timeout})
		if err != nil {
			return nil, fmt.Errorf("socks5 代理不可用: %w", err)
		}
		type ctxDialer interface {
			DialContext(context.Context, string, string) (net.Conn, error)
		}
		if cd, ok := d.(ctxDialer); ok {
			return cd.DialContext(ctx, "tcp", target)
		}
		return d.Dial("tcp", target)
	default:
		return dialViaHTTPProxy(ctx, cand.URL, target, p.opts.Timeout)
	}
}

// dialViaHTTPProxy 经 http/https 上游做 CONNECT 隧道。
func dialViaHTTPProxy(ctx context.Context, proxyURL *url.URL, target string, timeout time.Duration) (net.Conn, error) {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", proxyURL.Host)
	if err != nil {
		return nil, fmt.Errorf("连接代理 %s 失败: %w", proxyURL.Host, err)
	}
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: target},
		Host:   target,
		Header: make(http.Header),
	}
	if u := proxyURL.User; u != nil {
		pw, _ := u.Password()
		token := base64.StdEncoding.EncodeToString([]byte(u.Username() + ":" + pw))
		req.Header.Set("Proxy-Authorization", "Basic "+token)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("向代理发送 CONNECT 失败: %w", err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("读取代理响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		conn.Close()
		return nil, fmt.Errorf("代理拒绝 CONNECT:%s", resp.Status)
	}
	// 握手完成后清掉 deadline,隧道本身可以长时间空闲。
	_ = conn.SetDeadline(time.Time{})
	// br 可能已经缓冲了隧道数据,必须包回去,否则丢包。
	return &bufferedConn{Conn: conn, r: br}, nil
}

// bufferedConn 把 bufio.Reader 里已缓冲的数据接回 Read 路径。
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// markActive 记录最近调度到的代理。
func (p *Pool) markActive(c *Candidate) {
	p.activeMu.Lock()
	cp := *c
	p.active = &cp
	p.activeMu.Unlock()
}

// noteFailure 记一次失败流水并回调(累计失败计数由回调方负责)。
func (p *Pool) noteFailure(cand *Candidate, r *http.Request, target string, err error, elapsed time.Duration, taskID int64, stage string) {
	host, method := "", ""
	if r != nil {
		method = r.Method
		host = r.Host
	}
	p.record(RequestRecord{
		GroupID: p.opts.GroupID, ProxyID: cand.ID, ProxyURL: cand.Raw,
		Method: method, Host: host, URL: target,
		StatusCode: 0, LatencyMs: int(elapsed.Milliseconds()),
		OK: false, Error: err.Error(), TaskID: taskID, Stage: stage,
	})
	if p.opts.OnProxyFailure != nil {
		p.opts.OnProxyFailure(cand.ID)
	}
}

// record 投递一条流水(回调方应异步落库)。
func (p *Pool) record(rec RequestRecord) {
	if p.opts.OnRequest != nil {
		p.opts.OnRequest(rec)
	}
}

// stripHopByHop 剥掉逐跳头(代理转发必须做,RFC 7230 §6.1)。
func stripHopByHop(h http.Header) {
	for _, k := range []string{
		"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
	} {
		h.Del(k)
	}
}

// hostOnly 去掉 host:port 的端口部分(流水里 host 列只存主机)。
func hostOnly(hp string) string {
	if h, _, err := net.SplitHostPort(hp); err == nil {
		return h
	}
	return hp
}
