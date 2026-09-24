package proxycenter

// 代理测活:经每条代理请求 test_url,判定是否真能出网,并顺带采集延迟、
// 出口 IP 与匿名度。结果由调用方回写库(db.ApplyProxyCheckResult)。
//
// 匿名度判据(经典三档):
//   elite       目标站看不到代理痕迹,也看不到真实 IP
//   anonymous   目标站看到代理 IP,但看不到真实 IP(有 Via/X-Forwarded-For 之类痕迹)
//   transparent 目标站能看到真实 IP(响应里回显了本机出口 IP)
//
// 判据依赖 test_url 回显请求头/IP。httpbin.org/ip 只回显 origin IP,所以匿名度
// 只能区分「代理 IP 与本机出口 IP 是否相同」;更精确的判定需要 httpbin.org/headers
// 这类回显请求头的端点。这里做尽力而为的判定,拿不到证据时不臆断(留空)。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// CheckOutcome 是一条代理的测活结论。
type CheckOutcome struct {
	ProxyID   int64
	Alive     bool
	LatencyMs int
	ExitIP    string
	Anonymity string
	Region    string
	Err       string
}

// Checker 对一个代理集合做并发测活。
type Checker struct {
	// TestURL 是测活目标,需能回显出口 IP 才能采到 exit_ip。
	TestURL string
	// Timeout 是单条代理的测活超时。
	Timeout time.Duration
	// Concurrency 是并发度。
	Concurrency int
	// LocalIP 是本机出口 IP(用于透明代理判定);为空时跳过该判定。
	LocalIP string
	// Logf 是日志。
	Logf func(format string, args ...any)
}

// CheckTarget 是待测的一条代理。
type CheckTarget struct {
	ID        int64
	URL       string
	SourceRef string
}

// Check 并发测活 targets,逐条把结果交给 emit。
// emit 可能被多个 goroutine 并发调用,实现方需自行加锁或投递到 channel。
func (c *Checker) Check(ctx context.Context, targets []CheckTarget, emit func(CheckOutcome)) {
	if c.TestURL == "" {
		c.TestURL = DefaultTestURL
	}
	if c.Timeout <= 0 {
		c.Timeout = CheckTimeout
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 20
	}
	if c.Logf == nil {
		c.Logf = func(string, ...any) {}
	}
	sem := make(chan struct{}, c.Concurrency)
	var wg sync.WaitGroup
	for _, t := range targets {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		default:
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(t CheckTarget) {
			defer wg.Done()
			defer func() { <-sem }()
			emit(c.checkOne(ctx, t))
		}(t)
	}
	wg.Wait()
}

// checkOne 测活单条代理。
func (c *Checker) checkOne(ctx context.Context, t CheckTarget) CheckOutcome {
	out := CheckOutcome{ProxyID: t.ID}
	pu, err := url.Parse(strings.TrimSpace(t.URL))
	if err != nil || pu.Host == "" {
		out.Err = "代理地址无效"
		return out
	}
	client, err := c.clientFor(pu)
	if err != nil {
		out.Err = err.Error()
		return out
	}
	defer client.CloseIdleConnections()

	reqCtx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.TestURL, nil)
	if err != nil {
		out.Err = err.Error()
		return out
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; MEOWFUCK-ProxyCenter/1.0)")
	req.Header.Set("Accept", "*/*")

	start := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		out.Err = shortErr(err)
		return out
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 400 {
		out.Err = fmt.Sprintf("目标返回 HTTP %d", resp.StatusCode)
		return out
	}
	out.Alive = true
	out.LatencyMs = int(elapsed.Milliseconds())
	out.ExitIP = parseExitIP(body)
	out.Anonymity = c.judgeAnonymity(resp, out.ExitIP)
	return out
}

// clientFor 按代理协议构造 http.Client。
//
// 两个必须坚持的点:
//  1. 不能沿用系统环境代理(HTTP_PROXY),否则「测代理」会变成「测本机出口」,
//     结论全错。所以 Transport.Proxy 显式设为该代理本身。
//  2. 不能关闭 TLS 校验。测活走的是第三方代理,关掉校验就等于默认接受中间人
//     ——而中间人代理恰恰是本功能最该识别出来的东西:它既能读到全部明文流量,
//     也会让「这条代理可用」的结论失去意义。证书不合格的代理就是不可用,记失败
//     才是正确结论(带自签 CA 的自建代理可让 test_url 指向 http 端点绕开该限制)。
func (c *Checker) clientFor(pu *url.URL) (*http.Client, error) {
	switch pu.Scheme {
	case "http", "https", "socks5":
	default:
		return nil, fmt.Errorf("不支持的代理协议 %q", pu.Scheme)
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyURL(pu),
		DisableKeepAlives:     true, // 一次性的测活,不需要复用
		TLSHandshakeTimeout:   c.Timeout,
		ResponseHeaderTimeout: c.Timeout,
		ForceAttemptHTTP2:     false,
		DialContext: (&net.Dialer{
			Timeout:   c.Timeout,
			KeepAlive: -1,
		}).DialContext,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   c.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			// 不跟随跳转:跳转可能是代理注入的劫持页,跟随会把劫持当成功。
			return http.ErrUseLastResponse
		},
	}, nil
}

// parseExitIP 从响应体里提取出口 IP(兼容 httpbin 的 {"origin": "1.2.3.4"} 与纯文本)。
func parseExitIP(body []byte) string {
	var v map[string]any
	if err := json.Unmarshal(body, &v); err == nil {
		for _, k := range []string{"origin", "ip", "query", "address"} {
			if s, ok := v[k].(string); ok {
				// httpbin 的 origin 可能是 "1.2.3.4, 5.6.7.8"(多级代理),取第一个。
				if i := strings.IndexByte(s, ','); i > 0 {
					s = s[:i]
				}
				if s = strings.TrimSpace(s); net.ParseIP(s) != nil {
					return s
				}
			}
		}
	}
	// 纯文本形态:整段就是一个 IP。
	if s := strings.TrimSpace(string(body)); net.ParseIP(s) != nil {
		return s
	}
	return ""
}

// judgeAnonymity 依据响应头与出口 IP 判定匿名度。
// 证据不足时返回空串(不臆断),由展示层显示「未测」。
func (c *Checker) judgeAnonymity(resp *http.Response, exitIP string) string {
	// 透明:响应里回显了本机出口 IP(说明真实 IP 泄漏给了目标站)。
	if c.LocalIP != "" && exitIP != "" && exitIP == c.LocalIP {
		return "transparent"
	}
	// 有代理痕迹头 → 匿名(目标站知道是代理,但不知道真实 IP)。
	for _, h := range []string{"Via", "X-Forwarded-For", "Forwarded", "X-Real-Ip", "Proxy-Connection"} {
		if resp.Header.Get(h) != "" {
			return "anonymous"
		}
	}
	// 无痕迹且出口 IP 与本机不同 → 高匿。
	if exitIP != "" && c.LocalIP != "" && exitIP != c.LocalIP {
		return "elite"
	}
	return ""
}

// DetectLocalIP 探测本机出口 IP(透明代理判定用)。失败返回空串。
// 直连探测,不经任何代理:它要的就是「本机真实出口」。
func DetectLocalIP(ctx context.Context, timeout time.Duration) string {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, DefaultTestURL, nil)
	if err != nil {
		return ""
	}
	client := &http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
	return parseExitIP(body)
}

// shortErr 压缩网络错误信息(测活失败原因常常很长,截断便于入库展示)。
func shortErr(err error) string {
	s := err.Error()
	// 去掉 Go 的 "Get \"http://...\": " 前缀噪声。
	if i := strings.LastIndex(s, ": "); i > 0 && len(s) > 80 {
		s = s[i+2:]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
