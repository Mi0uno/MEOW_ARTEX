package recon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ---- 共用解析工具 ----

var (
	reHostname = regexp.MustCompile(`^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$`)
	reIPv4     = regexp.MustCompile(`^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$`)
)

// sanitizeDomain 归一并严格校验一个域名:仅接受合法 hostname(字母数字开头、
// 含点分 TLD),显式拒绝以 '-' 开头的值——杜绝任何输入被下游解析为开关。
// 不合法返回 ok=false。
func sanitizeDomain(d string) (string, bool) {
	d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
	if d == "" || strings.HasPrefix(d, "-") || !reHostname.MatchString(d) {
		return "", false
	}
	return d, true
}

// classifyLine 把一行文本归类成 RawAsset(域名/子域名/IP/URL service)。
// 无法识别的行返回 nil——导入文本混入注释、标题时静默跳过。
func classifyLine(line string) *RawAsset {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return nil
	}
	// URL → service(http/https 显式端口语义保留)
	if strings.Contains(line, "://") {
		u, err := url.Parse(line)
		if err != nil || u.Hostname() == "" {
			return nil
		}
		host := strings.ToLower(u.Hostname())
		port := u.Port()
		extra := map[string]any{"url": line, "scheme": strings.ToLower(u.Scheme)}
		eb, _ := json.Marshal(extra)
		p := 0
		if port != "" {
			p, _ = strconv.Atoi(port)
		}
		return &RawAsset{Type: TypeService, Value: host, Port: p, Extra: eb}
	}
	// host[:port]
	host, port := line, 0
	if h, pStr, err := net.SplitHostPort(line); err == nil {
		host, port = h, func() int { n, _ := strconv.Atoi(pStr); return n }()
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	switch {
	case reIPv4.MatchString(host):
		if net.ParseIP(host) == nil {
			return nil
		}
		return &RawAsset{Type: TypeIP, Value: host, Port: port}
	case reHostname.MatchString(host):
		return &RawAsset{Type: TypeDomain, Value: host, Port: port}
	}
	return nil
}

// companyRootDomains 汇总输入企业带进来的根域名(去重,保序,逐个过严格校验)。
func companyRootDomains(companies []CompanyTarget) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range companies {
		for _, d := range c.Domains {
			if clean, ok := sanitizeDomain(d); ok && !seen[clean] {
				seen[clean] = true
				out = append(out, clean)
			}
		}
	}
	return out
}

// configString / configInt / configBool 是宽松的 config 取值助手。
func configString(cfg map[string]any, key string) string {
	if cfg == nil {
		return ""
	}
	switch v := cfg[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

func configInt(cfg map[string]any, key string, def int) int {
	if cfg == nil {
		return def
	}
	if f, ok := cfg[key].(float64); ok && f >= 0 {
		return int(f)
	}
	return def
}

// ---- manual 收集器 ----

// ManualCollector 纯文本导入:config.text 逐行解析(域名/IP/URL)。
// 建任务页的"手动录入"文本框走这里;无外部依赖,永远可用。
type ManualCollector struct{}

func (ManualCollector) Key() string         { return "manual" }
func (ManualCollector) Title() string       { return "手动录入" }
func (ManualCollector) Kind() CollectorKind { return KindManual }

func (ManualCollector) Collect(_ context.Context, in CollectorInput) (*CollectorResult, error) {
	text := configString(in.Config, "text")
	if text == "" {
		return nil, fmt.Errorf("手动录入内容为空")
	}
	var assets []RawAsset
	seen := map[string]bool{}
	skipped := 0
	for _, line := range strings.Split(text, "\n") {
		a := classifyLine(line)
		if a == nil {
			if strings.TrimSpace(line) != "" && !strings.HasPrefix(strings.TrimSpace(line), "#") {
				skipped++
			}
			continue
		}
		k := fmt.Sprintf("%s|%s|%d", a.Type, a.Value, a.Port)
		if !seen[k] {
			seen[k] = true
			assets = append(assets, *a)
		}
	}
	raw := []byte(text)
	rawHash, _ := in.RawSink(raw)
	return &CollectorResult{
		Assets:  assets,
		RawHash: rawHash,
		Stats:   map[string]any{"lines": len(strings.Split(text, "\n")), "parsed": len(assets), "skipped": skipped},
	}, nil
}

// ---- crt.sh 证书透明日志收集器 ----

// CrtshCollector 通过证书透明日志(crt.sh)做被动子域枚举:
// 对每个根域名发一次 HTTPS GET,解析返回 JSON 里的 SAN/CN 主机名。
// 纯 HTTP 实现,无本地进程调用、无额外依赖。
//
// 安全边界:
//   - 请求目标是编译期常量 host("crt.sh"),https 固定,URL 查询参数经
//     url.QueryEscape;域名参数本身先过 sanitizeDomain 白名单;
//   - 响应体大小受限(MaxCrtshRespBytes),超限截断报错;
//   - 产出主机名逐个过 reHostname 白名单才进暂存区。
type CrtshCollector struct {
	// HTTPClient 允许测试注入;nil=用带超时的默认客户端。
	HTTPClient *http.Client
}

func (CrtshCollector) Key() string         { return "crtsh" }
func (CrtshCollector) Title() string       { return "证书透明日志 (crt.sh)" }
func (CrtshCollector) Kind() CollectorKind { return KindAPI }

// crtshHost 是唯一允许的远端主机(编译期常量);crtshQueryBase 是固定 https 前缀。
const (
	crtshHost       = "crt.sh"
	crtshQueryBase  = "https://crt.sh/?q="
	MaxCrtshDomains = 64
)

// defaultCrtshTimeout 单域名查询时限;crt.sh 偶发拥堵,给足余量。
const defaultCrtshTimeout = 120

// maxCrtshRespBytes 单次响应体上限(10MB),防异常大响应打爆内存。
const maxCrtshRespBytes = 10 << 20

// crtshEntry 是 crt.sh JSON 输出的最小字段集。
type crtshEntry struct {
	CommonName string `json:"common_name"`
	NameValue  string `json:"name_value"`
}

// collectOneDomain 查询一个根域名的 CT 记录,返回白名单内的主机名列表。
func (c CrtshCollector) collectOneDomain(ctx context.Context, client *http.Client, domain string) ([]string, []byte, error) {
	// domain 已过 sanitizeDomain;QueryEscape 兜底转义。host 为常量白名单校验。
	u := crtshQueryBase + url.QueryEscape("%."+domain)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", "ARTEX-Recon/1.0")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, nil, fmt.Errorf("crt.sh 限流(429),稍后重试")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("crt.sh 返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCrtshRespBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(body) > maxCrtshRespBytes {
		return nil, nil, fmt.Errorf("crt.sh 响应超过 %d 字节上限", maxCrtshRespBytes)
	}
	var entries []crtshEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, nil, fmt.Errorf("crt.sh JSON 解析失败: %w", err)
	}
	suffix := "." + domain
	seen := map[string]bool{}
	var hosts []string
	add := func(raw string) {
		h := strings.ToLower(strings.TrimSpace(raw))
		h = strings.TrimPrefix(h, "*.")
		h = strings.TrimSuffix(h, ".")
		if h == "" || !reHostname.MatchString(h) {
			return
		}
		// 只要本域及其子域;crt.sh 偶尔返回共用 SAN 的无关主机。
		if h != domain && !strings.HasSuffix(h, suffix) {
			return
		}
		if !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	for _, e := range entries {
		add(e.CommonName)
		for _, nv := range strings.Split(e.NameValue, "\n") {
			add(nv)
		}
	}
	return hosts, body, nil
}

func (c CrtshCollector) Collect(ctx context.Context, in CollectorInput) (*CollectorResult, error) {
	domains := companyRootDomains(in.Companies)
	if extra := configString(in.Config, "domains"); extra != "" {
		for _, d := range strings.Split(extra, ",") {
			if clean, ok := sanitizeDomain(d); ok {
				domains = append(domains, clean)
			}
		}
	}
	seen := map[string]bool{}
	var uniq []string
	for _, d := range domains {
		if !seen[d] {
			seen[d] = true
			uniq = append(uniq, d)
		}
		if len(uniq) >= MaxCrtshDomains {
			break
		}
	}
	if len(uniq) == 0 {
		return nil, fmt.Errorf("crt.sh 需要目标域名:请先在企业范围里录入根域名(kind=domain)")
	}

	timeout := configInt(in.Config, "timeout", defaultCrtshTimeout)
	if timeout <= 0 || timeout > 600 {
		timeout = defaultCrtshTimeout
	}
	limit := configInt(in.Config, "limit", 0) // 产出总上限,0=不限

	client := c.HTTPClient
	if client == nil {
		// 出网代理按任务解析(ctx 里带则走代理池,否则直连)。
		client = newHTTPClient(ctx, time.Duration(timeout)*time.Second)
	}

	var all []RawAsset
	hostSeen := map[string]bool{}
	var rawAll bytes.Buffer
	perDomain := make([]map[string]any, 0, len(uniq))
	for _, d := range uniq {
		if ctx.Err() != nil {
			break
		}
		hosts, body, err := c.collectOneDomain(ctx, client, d)
		rawAll.Write(body)
		rawAll.WriteByte('\n')
		entry := map[string]any{"domain": d, "found": len(hosts)}
		if err != nil && ctx.Err() == nil {
			entry["error"] = err.Error()
		}
		perDomain = append(perDomain, entry)
		for _, h := range hosts {
			if limit > 0 && len(all) >= limit {
				break
			}
			if !hostSeen[h] {
				hostSeen[h] = true
				all = append(all, RawAsset{Type: TypeDomain, Value: h})
			}
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	rawHash, _ := in.RawSink(rawAll.Bytes())
	return &CollectorResult{
		Assets:  all,
		RawHash: rawHash,
		Stats:   map[string]any{"domains": perDomain, "total": len(all)},
	}, nil
}

func init() {
	Register(ManualCollector{})
	Register(CrtshCollector{})
}
