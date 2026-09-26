package recon

// 资产监控的存活性探测:对一批「新发现子域」做 DNS 解析 + HTTP(S) 指纹采集。
//
// 安全边界(硬约束,见 MONITOR_CONTRACTS):
//   - 域名逐个过 sanitizeDomain 白名单,与收集器产出同口径;
//   - 解析结果只要没有一个是公网地址,就一个 HTTP 包都不发——防 DNS rebinding
//     把探测流量引到内网(10/8、127/8、169.254/16 等)。探测对象是授权范围内的
//     外部资产,内网地址对它毫无意义,宁可漏探不可误打。
//
// 探测语义:先 https 后 http 各试一次,哪个先成功用哪个;任意状态码(含 4xx/5xx)
// 都算存活——主机应答本身就证明「活着」,响应头/标题照样是指纹。

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// maxProbeBodyBytes 单次探测最多读响应体的字节数(256KB),够 <title> 与指纹所需。
const maxProbeBodyBytes = 256 << 10

// ProbeResult 是单个域名的探测结果。失败时 Alive=false,原因进 Err(永不向上抛 error)。
type ProbeResult struct {
	Domain        string
	IPs           []string
	Alive         bool
	URL           string
	StatusCode    int
	Title         string
	ContentLength int64
	Technologies  []string
	Err           string
}

// ProbeOptions 探测参数。零值即可用(并发 8、单次超时 6s、系统 resolver)。
type ProbeOptions struct {
	Concurrency int           // worker 数,缺省 8
	Timeout     time.Duration // 单次 HTTP 尝试超时,缺省 6s
	InsecureTLS bool          // x509 类握手错误时,用跳过证书校验的克隆 Transport 降级重试一次
	// Client 非 nil 时直接使用(测试注入),此时忽略 InsecureTLS 的 Transport 定制。
	Client *http.Client
	// Resolve 非 nil 时替代系统 DNS(测试注入)。nil = 系统 resolver 解析 A 记录。
	Resolve func(ctx context.Context, host string) ([]net.IP, error)
}

// ClassifyIP 把一个 IP 归入固定类别之一,返回值必属:
// invalid / private / loopback / linklocal / multicast / reserved / public。
// 判断顺序有讲究:先截掉明确的特殊段,最后才落到 public,避免 4-in-6 映射地址误判。
func ClassifyIP(ip net.IP) string {
	switch {
	case ip == nil:
		return "invalid"
	case ip.IsLoopback(): // 127/8、::1
		return "loopback"
	case ip.IsPrivate(): // 10/8、172.16/12、192.168/16、fc00::/7(含 4-in-6)
		return "private"
	case ip.IsLinkLocalUnicast(): // 169.254/16、fe80::/10
		return "linklocal"
	case ip.IsMulticast(): // 224/4、ff00::/8
		return "multicast"
	case ip.IsUnspecified() || !ip.IsGlobalUnicast(): // 0.0.0.0、255.255.255.255 等保留段
		return "reserved"
	default:
		return "public"
	}
}

// ProbeDomains 对一批域名并发探活。永不返回 error——单域失败进 Result.Err;
// 返回 slice 恒非 nil,且与输入等长同序。整批受 ctx 取消控制:取消时未跑的域名
// 直接落 ctx.Err() 文案,已在途的请求随 ctx 中断。
func ProbeDomains(ctx context.Context, domains []string, opts ProbeOptions) []ProbeResult {
	results := make([]ProbeResult, len(domains)) // make 恒非 nil,空输入即空 slice
	if len(domains) == 0 {
		return results
	}
	conc := opts.Concurrency
	if conc <= 0 {
		conc = 8
	}
	if conc > len(domains) {
		conc = len(domains)
	}
	// 客户端整批共构一份:Transport 并发安全,不必每域新建。
	clients := buildProbeClients(ctx, opts)
	resolve := opts.Resolve
	if resolve == nil {
		resolve = defaultResolve
	}

	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for i, dom := range domains {
		wg.Add(1)
		go func(i int, dom string) {
			defer wg.Done()
			// ctx 已取消:不再入队,直接落 ctx.Err() 文案。
			if err := ctx.Err(); err != nil {
				results[i] = canceledResult(dom, err)
				return
			}
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = probeDomain(ctx, dom, opts, clients, resolve)
		}(i, dom)
	}
	wg.Wait()
	return results
}

// probeDomain 跑单个域名的完整流程:DNS → 内网守卫 → https → (降级) → http。
func probeDomain(ctx context.Context, domain string, opts ProbeOptions, clients probeClients, resolve func(context.Context, string) ([]net.IP, error)) ProbeResult {
	res := ProbeResult{Domain: domain, IPs: []string{}, Technologies: []string{}}
	d, ok := sanitizeDomain(domain)
	if !ok {
		res.Err = "域名不合法,跳过探测"
		return res
	}
	res.Domain = d

	// ① DNS 解析 A 记录。
	ips, err := resolve(ctx, d)
	if err != nil {
		res.Err = fmt.Sprintf("DNS 解析失败: %s", oneLineErr(err))
		return res
	}
	if len(ips) == 0 {
		res.Err = "DNS 解析无 A 记录"
		return res
	}
	for _, ip := range ips {
		res.IPs = append(res.IPs, ip.String())
	}

	// ② 内网守卫:解析出了 IP 但没有一个 public → 一个 HTTP 请求都不发。
	hasPublic := false
	for _, ip := range ips {
		if ClassifyIP(ip) == "public" {
			hasPublic = true
			break
		}
	}
	if !hasPublic {
		res.Err = "解析结果非公网地址,跳过探测"
		return res
	}

	// ③ HTTP 探测:先 https 后 http,哪个先成功用哪个。
	var errLines []string
	pr, errHTTPS := probeOnce(ctx, clients.primary, "https://"+d)
	if errHTTPS == nil {
		return mergeProbeResult(res, pr)
	}
	errLines = append(errLines, "https: "+oneLineErr(errHTTPS))

	// ④ 降级重试:仅内部客户端 + InsecureTLS 时存在,注入客户端不走此路径。
	if clients.insecure != nil && isCertError(errHTTPS) {
		pr, err = probeOnce(ctx, clients.insecure, "https://"+d)
		if err == nil {
			return mergeProbeResult(res, pr)
		}
		errLines = append(errLines, "https(跳过证书校验): "+oneLineErr(err))
	}

	pr, err = probeOnce(ctx, clients.primary, "http://"+d)
	if err == nil {
		return mergeProbeResult(res, pr)
	}
	errLines = append(errLines, "http: "+oneLineErr(err))

	// ⑤ 全部失败:Err = 末次错误摘要(https/http 各一句话)。
	res.Err = strings.Join(errLines, "; ")
	return res
}

// probeOnce 发起一次 GET 并采集指纹。返回 ProbeResult 仅填充 HTTP 侧字段
// (URL/StatusCode/Title/ContentLength/Technologies);err 非 nil 表示本次尝试失败。
func probeOnce(ctx context.Context, client *http.Client, rawURL string) (ProbeResult, error) {
	res := ProbeResult{IPs: []string{}, Technologies: []string{}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return res, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return res, err
	}
	defer resp.Body.Close()

	// 响应体限流读取;读到一半断了不算探测失败——响应头已到手,
	// 标题能采多少采多少(残缺标题靠正则贪婪兜底,采不到就空)。
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxProbeBodyBytes))

	res.Alive = true
	res.StatusCode = resp.StatusCode
	if resp.Request != nil && resp.Request.URL != nil {
		res.URL = resp.Request.URL.String() // 跟随跳转后的最终 URL
	} else {
		res.URL = rawURL
	}
	if resp.ContentLength >= 0 {
		res.ContentLength = resp.ContentLength // Content-Length 头优先
	} else {
		res.ContentLength = int64(len(body)) // 无头(chunked 等)按实读字节数
	}
	res.Title = parseTitle(body)
	if v := strings.TrimSpace(resp.Header.Get("Server")); v != "" {
		res.Technologies = append(res.Technologies, v)
	}
	if v := strings.TrimSpace(resp.Header.Get("X-Powered-By")); v != "" && !techHas(res.Technologies, v) {
		res.Technologies = append(res.Technologies, v)
	}
	return res, nil
}

// mergeProbeResult 把一次成功尝试的 HTTP 侧字段并回带 DNS 侧字段的结果。
func mergeProbeResult(base ProbeResult, ok ProbeResult) ProbeResult {
	base.Alive = true
	base.URL = ok.URL
	base.StatusCode = ok.StatusCode
	base.Title = ok.Title
	base.ContentLength = ok.ContentLength
	base.Technologies = ok.Technologies
	return base
}

// canceledResult 构造 ctx 取消时未跑域名的占位结果。
func canceledResult(domain string, err error) ProbeResult {
	return ProbeResult{
		Domain:       domain,
		IPs:          []string{},
		Technologies: []string{},
		Err:          err.Error(), // ctx.Err() 文案,如 "context canceled"
	}
}

// defaultResolve 系统 resolver 解析 A 记录:LookupIPAddr 返回 v4+v6,
// 这里只保留 IPv4——监控轮「IP: 首个公网解析 IP」的落库口径按 A 记录走。
// 无 A 记录返回空 slice + nil error,由调用方给「无 A 记录」文案。
func defaultResolve(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := (&net.Resolver{}).LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		if a.IP != nil && a.IP.To4() != nil {
			ips = append(ips, a.IP)
		}
	}
	return ips, nil
}

// probeClients 是一批探测共用的客户端组:primary 恒非 nil;
// insecure 仅在「内部构造客户端 + InsecureTLS」时非 nil(注入客户端不做该定制)。
type probeClients struct {
	primary  *http.Client
	insecure *http.Client
}

// buildProbeClients 组装客户端组。
//
// InsecureTLS 降级客户端的存在的理由:监控对象是授权范围内的存量资产,
// 自签/过期/名称不匹配的证书在生产系统里极其常见——首连因 x509 失败只说明
// 证书不规范,不代表资产不可达,而指纹采集的价值恰恰是「这台主机在跑什么」。
// 故对 x509 类握手错误用 InsecureSkipVerify 的克隆 Transport 重试该 scheme 一次,
// 绝不静默放宽首次校验。克隆自 newHTTPClient 的 Transport:Proxy 显式 nil,
// 不继承环境变量代理(与平台其它出网路径口径一致)。
func buildProbeClients(ctx context.Context, opts ProbeOptions) probeClients {
	if opts.Client != nil {
		// 测试注入的客户端原样使用:不做 Transport 克隆/降级定制,保证注入可控。
		return probeClients{primary: opts.Client}
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 6 * time.Second
	}
	primary := newHTTPClient(ctx, timeout)
	pc := probeClients{primary: primary}
	if !opts.InsecureTLS {
		return pc
	}
	tr, ok := primary.Transport.(*http.Transport)
	if !ok {
		return pc
	}
	tr2 := tr.Clone()
	if tr2.TLSClientConfig == nil {
		tr2.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	tr2.TLSClientConfig.InsecureSkipVerify = true // 降级重试专用,理由见函数注释
	pc.insecure = &http.Client{Timeout: timeout, Transport: tr2}
	return pc
}

// isCertError 判断错误链上是否出现 x509/证书类错误(降级重试的触发条件)。
// Go 1.20+ 会把验证失败包进 tls.CertificateVerificationError,一并识别。
func isCertError(err error) bool {
	for ; err != nil; err = errors.Unwrap(err) {
		switch err.(type) {
		case x509.UnknownAuthorityError, *x509.UnknownAuthorityError,
			x509.CertificateInvalidError, *x509.CertificateInvalidError,
			x509.HostnameError, *x509.HostnameError,
			x509.ConstraintViolationError, *x509.ConstraintViolationError,
			x509.InsecureAlgorithmError, *x509.InsecureAlgorithmError,
			x509.SystemRootsError, *x509.SystemRootsError,
			*tls.CertificateVerificationError: // Error() 为指针接收者,链上只会出现指针形态
			return true
		}
	}
	return false
}

// reTitleTag 提取 <title> 文本:i=大小写不敏感,s=内容可跨行,[^>]* 容纳标签属性。
var reTitleTag = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// parseTitle 从响应体解析 <title>:大小写不敏感、容忍属性与多行、折叠空白、反转义实体。
func parseTitle(body []byte) string {
	m := reTitleTag.FindSubmatch(body)
	if m == nil {
		return ""
	}
	t := html.UnescapeString(string(m[1]))
	return strings.Join(strings.Fields(t), " ")
}

// techHas 大小写不敏感地在技术指纹列表里查重(Server 与 X-Powered-By 撞值时不重复收)。
func techHas(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

// oneLineErr 把错误压成单行一句话,防止多行错误把 Err 字段撑花。
func oneLineErr(err error) string {
	return strings.Join(strings.Fields(err.Error()), " ")
}
