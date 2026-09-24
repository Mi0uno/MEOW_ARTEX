package recon

// 外部接口 Provider 全量注册(16 家):每家 = ProviderDef(元数据 + Query + TestFn),
// 注册后自动获得三处接入 —— 创建页步骤/手动执行(通用收集器包装)/外部接口测试连接。
// 凭据形状各异(单 key / email+key / id+secret / basic auth),统一经 Secrets 注入。
//
// 安全:全部请求经 safeGet/safePost(公网校验:http/https + 拒绝 localhost/环回/
// 私有/保留地址,DNS 解析后逐 IP 检查)。
//
// 接口路径备注(按公开文档实现,账号套餐不同路径有差异时改对应 Query 一处即可):
//   - 0.zone / DayDayMap 的开放接口文档较少,采用通用路径 + 正文域名提取兜底;
//   - GitHub 域名枚举走 code search,响应体内正则提取目标域名后缀的主机名。

import (
	"context"
	"encoding/base64"
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

// ---- 通用出站助手 ----

func doRequest(ctx context.Context, method, rawURL string, headers map[string]string, body io.Reader) ([]byte, error) {
	if _, err := ValidatePublicURL(rawURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ARTEX-Recon/1.0")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
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
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("凭据无效或无权限(HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("上游返回 HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxCrtshRespBytes))
}

func safeGetHeaders(ctx context.Context, rawURL string, headers map[string]string) ([]byte, error) {
	return doRequest(ctx, http.MethodGet, rawURL, headers, nil)
}

func safePostJSON(ctx context.Context, rawURL string, headers map[string]string, payload any) ([]byte, error) {
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	h := map[string]string{"Content-Type": "application/json"}
	for k, v := range headers {
		h[k] = v
	}
	return doRequest(ctx, http.MethodPost, rawURL, h, strings.NewReader(string(buf)))
}

// extractHostnames 从任意响应正文里提取目标域名后缀的主机名(无结构化 API 时的兜底)。
func extractHostnames(body []byte, domain string, limit int) []string {
	if domain == "" || limit <= 0 {
		return nil
	}
	re := regexp.MustCompile(`(?i)[a-z0-9_-]+(?:\.[a-z0-9_-]+)*\.` + regexp.QuoteMeta(domain))
	seen := map[string]bool{}
	var out []string
	suffix := "." + strings.ToLower(domain)
	for _, m := range re.FindAllString(string(body), -1) {
		h := strings.ToLower(strings.TrimSuffix(m, "."))
		if h == domain || !strings.HasSuffix(h, suffix) {
			continue
		}
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}

func domainsToAssets(domain string, subs []string) []RawAsset {
	seen := map[string]bool{}
	var out []RawAsset
	for _, s := range subs {
		h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), "."))
		h = strings.TrimPrefix(h, "*.")
		if h == "" || !reHostname.MatchString(h) {
			continue
		}
		if !seen[h] {
			seen[h] = true
			out = append(out, RawAsset{Type: TypeDomain, Value: h})
		}
	}
	return out
}

// ---- 通用收集器包装:一个 ProviderDef 自动获得收集器能力 ----

type providerCollector struct{ def ProviderDef }

func (c providerCollector) Key() string         { return c.def.Key }
func (c providerCollector) Title() string       { return c.def.Title }
func (c providerCollector) Kind() CollectorKind { return KindAPI }

func (c providerCollector) Collect(ctx context.Context, in CollectorInput) (*CollectorResult, error) {
	in2 := QueryInput{Size: configInt(in.Config, "size", 100), Page: configInt(in.Config, "page", 1)}
	creds := map[string]string{}
	base := ""
	for k, v := range in.Secrets {
		s, _ := v.(string)
		if s == "" {
			continue
		}
		if k == "base_url" {
			base = s
			continue
		}
		creds[k] = s
	}
	if base == "" {
		base = c.def.DefaultBase
	}
	switch c.def.Kind {
	case ProviderKindDomainEnum:
		in2.Domain = configString(in.Config, "domain")
		if in2.Domain == "" {
			roots := companyRootDomains(in.Companies)
			if len(roots) == 0 {
				return nil, fmt.Errorf("%s 是域名枚举数据源:请填 domain 或关联带根域名范围的企业", c.def.Key)
			}
			in2.Domain = roots[0]
		}
		if _, ok := sanitizeDomain(in2.Domain); !ok {
			return nil, fmt.Errorf("无效域名: %q", in2.Domain)
		}
	default:
		in2.Query = configString(in.Config, "query")
		if in2.Query == "" {
			return nil, fmt.Errorf("%s 需要查询语句(query)", c.def.Key)
		}
	}
	assets, raw, err := c.def.Query(ctx, base, creds, in2)
	if err != nil {
		return nil, err
	}
	rawHash, _ := in.RawSink(raw)
	return &CollectorResult{
		Assets:  assets,
		RawHash: rawHash,
		Stats:   map[string]any{"total": len(assets)},
	}, nil
}

func registerProviderCollector(def ProviderDef) {
	Register(providerCollector{def: def})
}

// ---- 14 家新 Provider ----

func init() {
	// 3. Quake(360):服务搜索
	registerProvider(ProviderDef{
		Key: "quake", Title: "Quake (360)", DefaultBase: "https://quake.360.net",
		CredFields: []CredField{{Key: "token", Label: "API Token", Secret: true}},
		Kind:       ProviderKindSearch,
		Query: func(ctx context.Context, base string, c map[string]string, in QueryInput) ([]RawAsset, []byte, error) {
			page := in.Page
			if page <= 0 {
				page = 1
			}
			payload := map[string]any{"query": in.Query, "start": (page - 1) * in.Size, "size": in.Size}
			body, err := safePostJSON(ctx, base+"/api/v3/search/quake_service",
				map[string]string{"X-QuakeToken": c["token"]}, payload)
			if err != nil {
				return nil, nil, err
			}
			var parsed struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
				Data    []struct {
					IP       string `json:"ip"`
					Port     any    `json:"port"`
					Hostname string `json:"hostname"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return nil, nil, fmt.Errorf("quake 响应解析失败: %w", err)
			}
			if parsed.Code != 0 {
				return nil, nil, fmt.Errorf("quake: %s", parsed.Message)
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
			for _, item := range parsed.Data {
				port, _ := strconv.Atoi(fmt.Sprint(item.Port))
				if item.IP != "" && net.ParseIP(item.IP) != nil {
					add(&RawAsset{Type: TypeIP, Value: item.IP, Port: port})
				}
				h := strings.ToLower(strings.TrimSpace(item.Hostname))
				if h != "" && reHostname.MatchString(h) {
					add(&RawAsset{Type: TypeDomain, Value: h})
				}
			}
			return assets, body, nil
		},
	})

	// 4. Shodan
	registerProvider(ProviderDef{
		Key: "shodan", Title: "Shodan", DefaultBase: "https://api.shodan.io",
		CredFields: []CredField{{Key: "key", Label: "API Key", Secret: true}},
		Kind:       ProviderKindSearch,
		Query: func(ctx context.Context, base string, c map[string]string, in QueryInput) ([]RawAsset, []byte, error) {
			u := fmt.Sprintf("%s/shodan/host/search?key=%s&query=%s&minify=true",
				base, url.QueryEscape(c["key"]), url.QueryEscape(in.Query))
			body, err := safeGetHeaders(ctx, u, nil)
			if err != nil {
				return nil, nil, err
			}
			var parsed struct {
				Matches []struct {
					IPStr     string   `json:"ip_str"`
					Port      int      `json:"port"`
					Hostnames []string `json:"hostnames"`
				} `json:"matches"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return nil, nil, fmt.Errorf("shodan 响应解析失败: %w", err)
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
			for _, m := range parsed.Matches {
				if m.IPStr != "" {
					add(&RawAsset{Type: TypeIP, Value: m.IPStr, Port: m.Port})
				}
				for _, h := range m.Hostnames {
					h = strings.ToLower(strings.TrimSpace(h))
					if reHostname.MatchString(h) {
						add(&RawAsset{Type: TypeDomain, Value: h})
					}
				}
			}
			return assets, body, nil
		},
	})

	// 5. ZoomEye (v2)
	registerProvider(ProviderDef{
		Key: "zoomeye", Title: "ZoomEye", DefaultBase: "https://api.zoomeye.hk",
		CredFields: []CredField{{Key: "api_key", Label: "API-KEY", Secret: true}},
		Kind:       ProviderKindSearch,
		Query: func(ctx context.Context, base string, c map[string]string, in QueryInput) ([]RawAsset, []byte, error) {
			page := in.Page
			if page <= 0 {
				page = 1
			}
			body, err := safePostJSON(ctx, base+"/v2/host/search",
				map[string]string{"API-KEY": c["api_key"]},
				map[string]any{"query": in.Query, "page": page, "size": in.Size})
			if err != nil {
				return nil, nil, err
			}
			var parsed struct {
				Code int    `json:"code"`
				Msg  string `json:"message"`
				Data struct {
					Matches []struct {
						IP   string `json:"ip"`
						Port any    `json:"port"`
						Site string `json:"site"`
					} `json:"matches"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return nil, nil, fmt.Errorf("zoomeye 响应解析失败: %w", err)
			}
			if parsed.Code != 0 {
				return nil, nil, fmt.Errorf("zoomeye: %s", parsed.Msg)
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
			for _, m := range parsed.Data.Matches {
				port, _ := strconv.Atoi(fmt.Sprint(m.Port))
				if m.IP != "" {
					add(&RawAsset{Type: TypeIP, Value: m.IP, Port: port})
				}
				if m.Site != "" {
					if a := classifyLine("https://" + strings.TrimPrefix(m.Site, "https://")); a != nil && a.Value != "" {
						add(a)
					}
				}
			}
			return assets, body, nil
		},
	})

	// 6. ThreatBook(域名枚举)
	registerProvider(ProviderDef{
		Key: "threatbook", Title: "ThreatBook (微步)", DefaultBase: "https://api.threatbook.cn",
		CredFields: []CredField{{Key: "key", Label: "API Key", Secret: true}},
		Kind:       ProviderKindDomainEnum,
		Query: func(ctx context.Context, base string, c map[string]string, in QueryInput) ([]RawAsset, []byte, error) {
			u := fmt.Sprintf("%s/v3/scene/subdomains?key=%s&resource=%s",
				base, url.QueryEscape(c["key"]), url.QueryEscape(in.Domain))
			body, err := safeGetHeaders(ctx, u, nil)
			if err != nil {
				return nil, nil, err
			}
			var parsed struct {
				ErrorCode int `json:"error_code"`
				Data      struct {
					Subdomains map[string][]string `json:"subdomains"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return nil, nil, fmt.Errorf("threatbook 响应解析失败: %w", err)
			}
			if parsed.ErrorCode != 0 {
				return nil, nil, fmt.Errorf("threatbook: error_code=%d", parsed.ErrorCode)
			}
			var subs []string
			for _, list := range parsed.Data.Subdomains {
				subs = append(subs, list...)
			}
			return domainsToAssets(in.Domain, subs), body, nil
		},
	})

	// 7. 0.zone(域名枚举):POST /api/open/assetQuery + 0zone-key header,
	//    domain.froot 精确根域名 + asset_types=["site"](对应 type=site 申请页)。
	registerProvider(ProviderDef{
		Key: "0zone", Title: "0.zone (零零信安)", DefaultBase: "https://0.zone",
		CredFields: []CredField{{Key: "api_key", Label: "API Key", Secret: true}},
		Kind:       ProviderKindDomainEnum,
		Query: func(ctx context.Context, base string, c map[string]string, in QueryInput) ([]RawAsset, []byte, error) {
			page := in.Page
			if page <= 0 {
				page = 1
			}
			body, err := safePostJSON(ctx, base+"/api/open/assetQuery",
				map[string]string{"0zone-key": c["api_key"]},
				map[string]any{
					"domain.froot": in.Domain,
					"asset_types":  []string{"site"},
					"page_num":     page,
					"page_size":    in.Size,
				})
			if err != nil {
				return nil, nil, err
			}
			// 结构化优先:{"code":200,"data":{"list":[{"asset":"https://x.example.com",...}]}}
			var parsed struct {
				Code int `json:"code"`
				Data struct {
					List []struct {
						Asset  string `json:"asset"`
						Domain string `json:"domain"`
					} `json:"list"`
				} `json:"data"`
			}
			var assets []RawAsset
			if json.Unmarshal(body, &parsed) == nil && parsed.Code == 200 && len(parsed.Data.List) > 0 {
				seen := map[string]bool{}
				for _, item := range parsed.Data.List {
					var a *RawAsset
					v := strings.TrimSpace(item.Asset)
					if v == "" {
						v = strings.TrimSpace(item.Domain)
					}
					if v == "" {
						continue
					}
					if strings.Contains(v, "://") {
						a = classifyLine(v)
					} else if reHostname.MatchString(strings.ToLower(v)) {
						a = &RawAsset{Type: TypeDomain, Value: strings.ToLower(v)}
					}
					if a != nil {
						k := fmt.Sprintf("%s|%s|%d", a.Type, a.Value, a.Port)
						if !seen[k] {
							seen[k] = true
							assets = append(assets, *a)
						}
					}
				}
				if len(assets) > 0 {
					return assets, body, nil
				}
			}
			// 兜底:结构对不上时从正文正则提取目标域名后缀的主机名。
			return domainsToAssets(in.Domain, extractHostnames(body, in.Domain, in.Size)), body, nil
		},
	})

	// 8. DayDayMap(域名枚举):POST /api/v1/get-domain + apikey 参数(社区集成标准)。
	registerProvider(ProviderDef{
		Key: "daydaymap", Title: "DayDayMap", DefaultBase: "https://api.daydaymap.com",
		CredFields: []CredField{{Key: "api_key", Label: "API Key", Secret: true}},
		Kind:       ProviderKindDomainEnum,
		Query: func(ctx context.Context, base string, c map[string]string, in QueryInput) ([]RawAsset, []byte, error) {
			page := in.Page
			if page <= 0 {
				page = 1
			}
			body, err := safePostJSON(ctx, base+"/api/v1/get-domain",
				nil,
				map[string]any{"apikey": c["api_key"], "domain": in.Domain, "page": page, "size": in.Size})
			if err != nil {
				return nil, nil, err
			}
			// 结构化优先:{"code":200,"data":[{subdomain|domain|name...}]} 常见多种键名,逐个兼容。
			var parsed struct {
				Code int `json:"code"`
				Data []struct {
					Subdomain string `json:"subdomain"`
					Domain    string `json:"domain"`
					Name      string `json:"name"`
					Host      string `json:"host"`
				} `json:"data"`
			}
			var assets []RawAsset
			if json.Unmarshal(body, &parsed) == nil && parsed.Code == 200 && len(parsed.Data) > 0 {
				seen := map[string]bool{}
				for _, item := range parsed.Data {
					h := strings.ToLower(strings.TrimSpace(item.Subdomain))
					if h == "" {
						h = strings.ToLower(strings.TrimSpace(item.Domain))
					}
					if h == "" {
						h = strings.ToLower(strings.TrimSpace(item.Name))
					}
					if h == "" {
						h = strings.ToLower(strings.TrimSpace(item.Host))
					}
					if h == "" || !reHostname.MatchString(h) {
						continue
					}
					if !seen[h] {
						seen[h] = true
						assets = append(assets, RawAsset{Type: TypeDomain, Value: h})
					}
				}
				if len(assets) > 0 {
					return assets, body, nil
				}
			}
			// 兜底:正文正则提取。
			return domainsToAssets(in.Domain, extractHostnames(body, in.Domain, in.Size)), body, nil
		},
	})

	// 9. VirusTotal(域名枚举:子域)
	registerProvider(ProviderDef{
		Key: "virustotal", Title: "VirusTotal", DefaultBase: "https://www.virustotal.com",
		CredFields: []CredField{{Key: "api_key", Label: "API Key", Secret: true}},
		Kind:       ProviderKindDomainEnum,
		Query: func(ctx context.Context, base string, c map[string]string, in QueryInput) ([]RawAsset, []byte, error) {
			u := fmt.Sprintf("%s/api/v3/domains/%s/subdomains?limit=40", base, url.PathEscape(in.Domain))
			body, err := safeGetHeaders(ctx, u, map[string]string{"x-apikey": c["api_key"]})
			if err != nil {
				return nil, nil, err
			}
			var parsed struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return nil, nil, fmt.Errorf("virustotal 响应解析失败: %w", err)
			}
			var subs []string
			for _, d := range parsed.Data {
				subs = append(subs, d.ID)
			}
			return domainsToAssets(in.Domain, subs), body, nil
		},
	})

	// 10. Censys
	registerProvider(ProviderDef{
		Key: "censys", Title: "Censys", DefaultBase: "https://search.censys.io",
		CredFields: []CredField{
			{Key: "api_id", Label: "API ID"},
			{Key: "api_secret", Label: "API Secret", Secret: true},
		},
		Kind: ProviderKindSearch,
		Query: func(ctx context.Context, base string, c map[string]string, in QueryInput) ([]RawAsset, []byte, error) {
			page := in.Page
			if page <= 0 {
				page = 1
			}
			basic := base64.StdEncoding.EncodeToString([]byte(c["api_id"] + ":" + c["api_secret"]))
			body, err := safePostJSON(ctx, base+"/api/v2/hosts/search",
				map[string]string{"Authorization": "Basic " + basic},
				map[string]any{"q": in.Query, "per_page": in.Size, "page": page})
			if err != nil {
				return nil, nil, err
			}
			var parsed struct {
				Result struct {
					Hits []struct {
						IP       string `json:"ip"`
						Services []struct {
							Port int `json:"port"`
						} `json:"services"`
						DNS struct {
							Names []string `json:"names"`
						} `json:"dns"`
					} `json:"hits"`
				} `json:"result"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return nil, nil, fmt.Errorf("censys 响应解析失败: %w", err)
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
			for _, h := range parsed.Result.Hits {
				for _, svc := range h.Services {
					if h.IP != "" {
						add(&RawAsset{Type: TypeIP, Value: h.IP, Port: svc.Port})
					}
				}
				for _, n := range h.DNS.Names {
					n = strings.ToLower(strings.TrimSpace(n))
					if reHostname.MatchString(n) {
						add(&RawAsset{Type: TypeDomain, Value: n})
					}
				}
			}
			return assets, body, nil
		},
	})

	// 11. GitHub(仅域名枚举:code search 提取目标域名)
	registerProvider(ProviderDef{
		Key: "github", Title: "GitHub (域名枚举)", DefaultBase: "https://api.github.com",
		CredFields: []CredField{{Key: "token", Label: "Personal Access Token", Secret: true}},
		Kind:       ProviderKindDomainEnum,
		Query: func(ctx context.Context, base string, c map[string]string, in QueryInput) ([]RawAsset, []byte, error) {
			u := fmt.Sprintf("%s/search/code?q=%s&per_page=%d",
				base, url.QueryEscape(`"`+in.Domain+`"`), min(in.Size, 100))
			body, err := safeGetHeaders(ctx, u, map[string]string{
				"Authorization": "Bearer " + c["token"],
				"Accept":        "application/vnd.github+json",
			})
			if err != nil {
				return nil, nil, err
			}
			return domainsToAssets(in.Domain, extractHostnames(body, in.Domain, in.Size)), body, nil
		},
	})

	// 12. Chaos (ProjectDiscovery,仅域名枚举)
	registerProvider(ProviderDef{
		Key: "chaos", Title: "Chaos (ProjectDiscovery)", DefaultBase: "https://chaos.projectdiscovery.io",
		CredFields: []CredField{{Key: "token", Label: "API Token", Secret: true}},
		Kind:       ProviderKindDomainEnum,
		Query: func(ctx context.Context, base string, c map[string]string, in QueryInput) ([]RawAsset, []byte, error) {
			u := fmt.Sprintf("%s/api/v1/subdomains/%s", base, url.PathEscape(in.Domain))
			body, err := safeGetHeaders(ctx, u, map[string]string{"Authorization": c["token"]})
			if err != nil {
				return nil, nil, err
			}
			var parsed struct {
				Subdomains []string `json:"subdomains"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return nil, nil, fmt.Errorf("chaos 响应解析失败: %w", err)
			}
			var subs []string
			for _, s := range parsed.Subdomains {
				subs = append(subs, s+"."+in.Domain)
			}
			return domainsToAssets(in.Domain, subs), body, nil
		},
	})

	// 13. Bevigil(仅域名枚举)
	registerProvider(ProviderDef{
		Key: "bevigil", Title: "Bevigil", DefaultBase: "https://apis.bevigil.com",
		CredFields: []CredField{{Key: "access_token", Label: "Access Token", Secret: true}},
		Kind:       ProviderKindDomainEnum,
		Query: func(ctx context.Context, base string, c map[string]string, in QueryInput) ([]RawAsset, []byte, error) {
			u := fmt.Sprintf("%s/osint/subdomains?domain_id=%s", base, url.QueryEscape(in.Domain))
			body, err := safeGetHeaders(ctx, u, map[string]string{"access-token": c["access_token"]})
			if err != nil {
				return nil, nil, err
			}
			var parsed struct {
				Subdomains []string `json:"subdomains"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return nil, nil, fmt.Errorf("bevigil 响应解析失败: %w", err)
			}
			return domainsToAssets(in.Domain, parsed.Subdomains), body, nil
		},
	})

	// 14. IntelX(仅域名枚举:两步 search/result)
	registerProvider(ProviderDef{
		Key: "intelx", Title: "IntelX", DefaultBase: "https://2.intelx.io",
		CredFields: []CredField{{Key: "key", Label: "API Key", Secret: true}},
		Kind:       ProviderKindDomainEnum,
		Query: func(ctx context.Context, base string, c map[string]string, in QueryInput) ([]RawAsset, []byte, error) {
			sid, err := safePostJSON(ctx, base+"/intelligent/search",
				map[string]string{"x-key": c["key"]},
				map[string]any{"term": in.Domain, "maxcount": in.Size, "media": 0})
			if err != nil {
				return nil, nil, err
			}
			var sr struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(sid, &sr); err != nil || sr.ID == "" {
				return nil, nil, fmt.Errorf("intelx search 未返回 id")
			}
			u := fmt.Sprintf("%s/intelligent/search/result?id=%s&limit=%d",
				base, url.QueryEscape(sr.ID), in.Size)
			body, err := safeGetHeaders(ctx, u, map[string]string{"x-key": c["key"]})
			if err != nil {
				return nil, nil, err
			}
			return domainsToAssets(in.Domain, extractHostnames(body, in.Domain, in.Size)), body, nil
		},
	})

	// 15. PassiveTotal (RiskIQ,仅域名枚举;Basic 认证)
	registerProvider(ProviderDef{
		Key: "passivetotal", Title: "PassiveTotal (RiskIQ)", DefaultBase: "https://api.passivetotal.org",
		CredFields: []CredField{
			{Key: "api_user", Label: "API User"},
			{Key: "api_key", Label: "API Key", Secret: true},
		},
		Kind: ProviderKindDomainEnum,
		Query: func(ctx context.Context, base string, c map[string]string, in QueryInput) ([]RawAsset, []byte, error) {
			u := fmt.Sprintf("%s/v2/enrichment/subdomain?query=%s", base, url.QueryEscape(in.Domain))
			basic := base64.StdEncoding.EncodeToString([]byte(c["api_user"] + ":" + c["api_key"]))
			body, err := safeGetHeaders(ctx, u, map[string]string{"Authorization": "Basic " + basic})
			if err != nil {
				return nil, nil, err
			}
			var parsed struct {
				Subdomains []string `json:"subdomains"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return nil, nil, fmt.Errorf("passivetotal 响应解析失败: %w", err)
			}
			var subs []string
			for _, s := range parsed.Subdomains {
				subs = append(subs, s+"."+in.Domain)
			}
			return domainsToAssets(in.Domain, subs), body, nil
		},
	})

	// 16. CertSpotter(仅域名枚举:证书透明)
	registerProvider(ProviderDef{
		Key: "certspotter", Title: "CertSpotter", DefaultBase: "https://api.certspotter.com",
		CredFields: []CredField{{Key: "token", Label: "API Token", Secret: true}},
		Kind:       ProviderKindDomainEnum,
		Query: func(ctx context.Context, base string, c map[string]string, in QueryInput) ([]RawAsset, []byte, error) {
			u := fmt.Sprintf("%s/v1/issuances?domain=%s&include_subdomains=true&expand=dns_names",
				base, url.QueryEscape(in.Domain))
			body, err := safeGetHeaders(ctx, u, map[string]string{"Authorization": "Bearer " + c["token"]})
			if err != nil {
				return nil, nil, err
			}
			var parsed []struct {
				DNSNames []string `json:"dns_names"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return nil, nil, fmt.Errorf("certspotter 响应解析失败: %w", err)
			}
			var subs []string
			for _, iss := range parsed {
				subs = append(subs, iss.DNSNames...)
			}
			return domainsToAssets(in.Domain, subs), body, nil
		},
	})

	// 统一注册收集器 + 测试连接:每家 Test = 同一 Query 跑最小量
	// (search 型用探针语句,domain_enum 型用 example.com),失败即凭据/网络问题。
	for key, def := range providers {
		if key == "fofa" || key == "hunter" {
			continue // 已有专用 Test 实现
		}
		d := def
		d.TestFn = func(ctx context.Context, base string, c map[string]string) (map[string]any, error) {
			in := QueryInput{Size: 1, Page: 1}
			if d.Kind == ProviderKindDomainEnum {
				in.Domain = "example.com"
			} else {
				in.Query = "test"
			}
			assets, _, err := d.Query(ctx, base, c, in)
			if err != nil {
				return nil, err
			}
			return map[string]any{"ok": true, "note": fmt.Sprintf("查询成功,返回 %d 条", len(assets))}, nil
		}
		registerProviderCollector(d)
		providers[key] = d
	}
}
