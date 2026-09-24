package recon

// 外部接口(空间测绘 API)支撑层:
//   - ValidatePublicURL: 所有出站请求前强制校验(仅 http/https;拒绝 localhost/
//     环回/私有/保留地址,DNS 解析后逐 IP 检查)——杜绝把内网当测绘网关打;
//   - ProviderDef: 每个 provider 的元数据 + 测试连接实现(外部接口页的"测试"按钮);
//   - FofaCollector / HunterCollector: 采集器,凭据经 CollectorInput.Secrets 注入
//     (运行时从 external_apis 解密读取,绝不落进任务配置 JSON)。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ---- 出站 URL 公网校验 ----

var rejectCIDRs = mustParseCIDRs(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
	"172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15", "240.0.0.0/4",
	"255.255.255.255/32", "224.0.0.0/4",
)

func mustParseCIDRs(specs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(specs))
	for _, s := range specs {
		_, network, err := net.ParseCIDR(s)
		if err != nil || network == nil {
			panic(fmt.Sprintf("recon: bad cidr %s", s))
		}
		out = append(out, network)
	}
	return out
}

func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		for _, n := range rejectCIDRs {
			if n.Contains(v4) {
				return false
			}
		}
		return true
	}
	// IPv6: 上面通筛已覆盖 ::1/fe80::/fc00::/ff00::;再排除 2001:db8::/32 文档段。
	_, docNet, _ := net.ParseCIDR("2001:db8::/32")
	if docNet != nil && docNet.Contains(ip) {
		return false
	}
	return ip.IsGlobalUnicast()
}

// ValidatePublicURL 校验一个即将发起服务端请求的 URL:
// 仅允许 http/https;拒绝 localhost/环回/私有/保留地址(DNS 解析后逐 IP 检查)。
func ValidatePublicURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("URL 无效: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, fmt.Errorf("仅允许 http/https 请求")
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("URL 缺少主机")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || host == "0.0.0.0" {
		return nil, fmt.Errorf("拒绝访问本机/私有地址")
	}
	check := func(ip net.IP) error {
		if !isPublicIP(ip) {
			return fmt.Errorf("拒绝访问私有/保留地址: %s", ip)
		}
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if err := check(ip); err != nil {
			return nil, err
		}
	} else {
		ips, err := net.LookupIP(host)
		if err != nil {
			return nil, fmt.Errorf("主机解析失败: %w", err)
		}
		for _, ip := range ips {
			if err := check(ip); err != nil {
				return nil, err
			}
		}
	}
	return u, nil
}

// safeGet 发起一次经过公网校验的 GET,返回 body(限 10MB)。
func safeGet(ctx context.Context, rawURL string) ([]byte, error) {
	if _, err := ValidatePublicURL(rawURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ARTEX-Recon/1.0")
	// 出网代理按任务解析(ctx 里带则走代理池,否则直连)。
	client := newHTTPClient(ctx, 60*time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("上游限流(429),稍后重试")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("上游返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCrtshRespBytes))
	if err != nil {
		return nil, err
	}
	return body, nil
}

// ---- Provider 注册表(外部接口页元数据 + 测试连接) ----

type CredField struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Secret bool   `json:"secret"`
}

// ProviderKind: search=查询语句型(fofa/quake/shodan…); domain_enum=域名枚举型(给定根域名拉子域)。
type ProviderKind string

const (
	ProviderKindSearch     ProviderKind = "search"
	ProviderKindDomainEnum ProviderKind = "domain_enum"
)

// QueryInput 是查询函数的统一输入:search 型用 Query,domain_enum 型用 Domain。
type QueryInput struct {
	Query  string
	Domain string
	Size   int
	Page   int
}

type ProviderDef struct {
	Key         string       `json:"key"`
	Title       string       `json:"title"`
	DefaultBase string       `json:"default_base"`
	CredFields  []CredField  `json:"cred_fields"`
	Kind        ProviderKind `json:"kind"`
	// TestFn/Query 不参与 JSON 序列化(函数字段,marshal 会直接报错)。
	TestFn func(ctx context.Context, base string, creds map[string]string) (map[string]any, error)                    `json:"-"`
	Query  func(ctx context.Context, base string, creds map[string]string, in QueryInput) ([]RawAsset, []byte, error) `json:"-"`
}

// Test 用一条最小查询验证凭据;返回配额信息(可空)。
func (p ProviderDef) Test(ctx context.Context, base string, creds map[string]string) (map[string]any, error) {
	if p.TestFn != nil {
		return p.TestFn(ctx, base, creds)
	}
	switch p.Key {
	case "fofa":
		return p.testFofa(ctx, base, creds)
	case "hunter":
		return p.testHunter(ctx, base, creds)
	}
	return nil, fmt.Errorf("未知 provider %q", p.Key)
}

func (p ProviderDef) testFofa(ctx context.Context, base string, creds map[string]string) (map[string]any, error) {
	email, key := creds["email"], creds["key"]
	if email == "" || key == "" {
		return nil, fmt.Errorf("缺少 email 或 key")
	}
	q := base64.StdEncoding.EncodeToString([]byte("ARTEX 测试连接"))
	u := fmt.Sprintf("%s/api/v1/search/all?email=%s&key=%s&qbase64=%s&size=1&fields=host",
		strings.TrimSuffix(base, "/"), url.QueryEscape(email), url.QueryEscape(key), q)
	body, err := safeGet(ctx, u)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Error  bool   `json:"error"`
		Errmsg string `json:"errmsg"`
		Size   int    `json:"size"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("fofa 响应解析失败: %w", err)
	}
	if parsed.Error {
		return nil, fmt.Errorf("fofa: %s", parsed.Errmsg)
	}
	return map[string]any{"ok": true, "note": fmt.Sprintf("查询成功,本次返回 %d 条", parsed.Size)}, nil
}

func (p ProviderDef) testHunter(ctx context.Context, base string, creds map[string]string) (map[string]any, error) {
	key := creds["api_key"]
	if key == "" {
		return nil, fmt.Errorf("缺少 api-key")
	}
	q := base64.StdEncoding.EncodeToString([]byte("test"))
	u := fmt.Sprintf("%s/openApi/search?api-key=%s&search=%s&page=1&page_size=10&is_web=3",
		strings.TrimSuffix(base, "/"), url.QueryEscape(key), q)
	body, err := safeGet(ctx, u)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Total int64 `json:"total"`
			Quota struct {
				RemainQuota       int64 `json:"remain_quota"`
				SimultaneousQuota int64 `json:"simultaneous_quota"`
			} `json:"quota"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("hunter 响应解析失败: %w", err)
	}
	if parsed.Code != 200 {
		return nil, fmt.Errorf("hunter: %s", parsed.Msg)
	}
	return map[string]any{
		"total":              parsed.Data.Total,
		"remain_quota":       parsed.Data.Quota.RemainQuota,
		"simultaneous_quota": parsed.Data.Quota.SimultaneousQuota,
	}, nil
}

var providers = map[string]ProviderDef{
	"fofa": {
		Key:         "fofa",
		Title:       "FOFA",
		DefaultBase: "https://fofa.info",
		CredFields:  []CredField{{Key: "email", Label: "Email"}, {Key: "key", Label: "API Key", Secret: true}},
		Kind:        ProviderKindSearch,
	},
	"hunter": {
		Key:         "hunter",
		Title:       "Hunter (奇安信)",
		DefaultBase: "https://hunter.qianxin.com",
		CredFields:  []CredField{{Key: "api_key", Label: "API-Key", Secret: true}},
		Kind:        ProviderKindSearch,
	},
}

// registerProvider 追加 provider 定义(providers.go 的 init 调用)。
func registerProvider(p ProviderDef) { providers[p.Key] = p }

// ProviderDescriptions 返回全部已注册 provider 元数据(外部接口页新建表单),
// 固定顺序:fofa/hunter 在前,其余按注册顺序。
func ProviderDescriptions() []ProviderDef {
	order := []string{"fofa", "hunter"}
	seen := map[string]bool{"fofa": true, "hunter": true}
	for key := range providers {
		if !seen[key] {
			order = append(order, key)
		}
	}
	out := make([]ProviderDef, 0, len(order))
	for _, key := range order {
		out = append(out, providers[key])
	}
	return out
}

// Provider 取一个 provider 定义;nil=未注册。
func Provider(key string) (ProviderDef, bool) {
	p, ok := providers[key]
	return p, ok
}

// ---- FOFA ----

// MaxFofaSize 单次拉取条数上限。
const MaxFofaSize = 5000

// FofaQuery 执行一次 FOFA 查询(收集器与 AI 工具共用):返回产出资产与原始响应。
// base 传 provider 的网关地址(默认官方);email/key 为凭据。
func FofaQuery(ctx context.Context, base, email, key, query string, size int, fields string) ([]RawAsset, []byte, error) {
	if query == "" {
		return nil, nil, fmt.Errorf("fofa 需要查询语句(query)")
	}
	if size <= 0 || size > MaxFofaSize {
		size = 100
	}
	if fields == "" {
		fields = "host,ip,port,title"
	}
	if email == "" || key == "" {
		return nil, nil, fmt.Errorf("fofa 凭据缺失:请在「外部接口」配置 FOFA 接口")
	}
	u := fmt.Sprintf("%s/api/v1/search/all?email=%s&key=%s&qbase64=%s&size=%d&fields=%s",
		strings.TrimSuffix(base, "/"), url.QueryEscape(email), url.QueryEscape(key),
		base64.StdEncoding.EncodeToString([]byte(query)), size, url.QueryEscape(fields))
	body, err := safeGet(ctx, u)
	if err != nil {
		return nil, nil, err
	}
	var parsed struct {
		Error   bool    `json:"error"`
		Errmsg  string  `json:"errmsg"`
		Size    int     `json:"size"`
		Results [][]any `json:"results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, nil, fmt.Errorf("fofa 响应解析失败: %w", err)
	}
	if parsed.Error {
		return nil, nil, fmt.Errorf("fofa: %s", parsed.Errmsg)
	}
	cols := strings.Split(fields, ",")
	hosts := map[string]bool{}
	var assets []RawAsset
	for _, row := range parsed.Results {
		get := func(name string) string {
			for i, c := range cols {
				if strings.TrimSpace(c) == name && i < len(row) {
					s, _ := row[i].(string)
					return strings.TrimSpace(s)
				}
			}
			return ""
		}
		host, ip, port := get("host"), get("ip"), get("port")
		portN := 0
		fmt.Sscanf(port, "%d", &portN)
		if host != "" {
			var a *RawAsset
			if strings.Contains(host, "://") {
				a = classifyLine(host)
			} else if reHostname.MatchString(strings.ToLower(host)) {
				a = &RawAsset{Type: TypeDomain, Value: strings.ToLower(host)}
			} else if reIPv4.MatchString(host) && net.ParseIP(host) != nil {
				a = &RawAsset{Type: TypeIP, Value: host, Port: portN}
			}
			if a != nil {
				k := fmt.Sprintf("%s|%s|%d", a.Type, a.Value, a.Port)
				if !hosts[k] {
					hosts[k] = true
					assets = append(assets, *a)
				}
			}
		}
		if ip != "" && reIPv4.MatchString(ip) && net.ParseIP(ip) != nil {
			k := fmt.Sprintf("ip|%s|%d", ip, portN)
			if !hosts[k] {
				hosts[k] = true
				assets = append(assets, RawAsset{Type: TypeIP, Value: ip, Port: portN})
			}
		}
	}
	return assets, body, nil
}

type FofaCollector struct{}

func (FofaCollector) Key() string         { return "fofa" }
func (FofaCollector) Title() string       { return "空间测绘 (FOFA)" }
func (FofaCollector) Kind() CollectorKind { return KindAPI }

// credString 宽松读取 Secrets 字符串值。
func credString(secrets map[string]any, key string) string {
	if secrets == nil {
		return ""
	}
	s, _ := secrets[key].(string)
	return strings.TrimSpace(s)
}

func (FofaCollector) Collect(ctx context.Context, in CollectorInput) (*CollectorResult, error) {
	query := configString(in.Config, "query")
	size := configInt(in.Config, "size", 100)
	fields := configString(in.Config, "fields")
	email := credString(in.Secrets, "email")
	key := credString(in.Secrets, "key")
	base := credString(in.Secrets, "base_url")
	if base == "" {
		base = providers["fofa"].DefaultBase
	}
	assets, raw, err := FofaQuery(ctx, base, email, key, query, size, fields)
	if err != nil {
		return nil, err
	}
	rawHash, _ := in.RawSink(raw)
	return &CollectorResult{
		Assets:  assets,
		RawHash: rawHash,
		Stats:   map[string]any{"total": len(assets), "size": len(assets)},
	}, nil
}

// ---- Hunter ----

// HunterQuery 执行一次 Hunter 查询(收集器与 AI 工具共用)。
func HunterQuery(ctx context.Context, base, key, query string, size, page int) ([]RawAsset, []byte, error) {
	if query == "" {
		return nil, nil, fmt.Errorf("hunter 需要查询语句(query)")
	}
	if size <= 0 || size > 100 {
		size = 100
	}
	if page <= 0 {
		page = 1
	}
	if key == "" {
		return nil, nil, fmt.Errorf("hunter 凭据缺失:请在「外部接口」配置 Hunter 接口")
	}
	u := fmt.Sprintf("%s/openApi/search?api-key=%s&search=%s&page=%d&page_size=%d&is_web=3",
		strings.TrimSuffix(base, "/"), url.QueryEscape(key),
		base64.StdEncoding.EncodeToString([]byte(query)), page, size)
	body, err := safeGet(ctx, u)
	if err != nil {
		return nil, nil, err
	}
	var parsed struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Total int64 `json:"total"`
			Arr   []struct {
				URL      string `json:"url"`
				IP       string `json:"ip"`
				Port     int    `json:"port"`
				Domain   string `json:"domain"`
				Protocol string `json:"protocol"`
			} `json:"arr"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, nil, fmt.Errorf("hunter 响应解析失败: %w", err)
	}
	if parsed.Code != 200 {
		return nil, nil, fmt.Errorf("hunter: %s", parsed.Msg)
	}
	seen := map[string]bool{}
	var assets []RawAsset
	add := func(a *RawAsset) {
		if a == nil {
			return
		}
		k := fmt.Sprintf("%s|%s|%d", a.Type, a.Value, a.Port)
		if !seen[k] {
			seen[k] = true
			assets = append(assets, *a)
		}
	}
	for _, item := range parsed.Data.Arr {
		if item.Domain != "" && reHostname.MatchString(strings.ToLower(item.Domain)) {
			add(&RawAsset{Type: TypeDomain, Value: strings.ToLower(item.Domain)})
		}
		if item.IP != "" && reIPv4.MatchString(item.IP) && net.ParseIP(item.IP) != nil {
			add(&RawAsset{Type: TypeIP, Value: item.IP, Port: item.Port})
		}
		if item.URL != "" {
			if a := classifyLine(item.URL); a != nil {
				add(a)
			}
		}
	}
	return assets, body, nil
}

type HunterCollector struct{}

func (HunterCollector) Key() string         { return "hunter" }
func (HunterCollector) Title() string       { return "空间测绘 (Hunter)" }
func (HunterCollector) Kind() CollectorKind { return KindAPI }

func (HunterCollector) Collect(ctx context.Context, in CollectorInput) (*CollectorResult, error) {
	query := configString(in.Config, "query")
	size := configInt(in.Config, "size", 100)
	page := configInt(in.Config, "page", 1)
	key := credString(in.Secrets, "api_key")
	base := credString(in.Secrets, "base_url")
	if base == "" {
		base = providers["hunter"].DefaultBase
	}
	assets, raw, err := HunterQuery(ctx, base, key, query, size, page)
	if err != nil {
		return nil, err
	}
	rawHash, _ := in.RawSink(raw)
	return &CollectorResult{
		Assets:  assets,
		RawHash: rawHash,
		Stats:   map[string]any{"total": len(assets), "total_available": len(assets)},
	}, nil
}

// CollectorToolKeys 返回全部收集器 key(用于识别/清理误入 tools 表的展示壳行;
// 收集器的真正入口是步骤任务与工具执行页,不是 agent 工具)。
func CollectorToolKeys() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	return out
}

func init() {
	Register(FofaCollector{})
	Register(HunterCollector{})
}
