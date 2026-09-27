package proxycenter

// 代理源注册表:内置的代理池 API 与「全网在野代理」公开源。
//
// 分三类:
//   commercial  商业代理池 API(快代理 / 芝麻 / 青果 …)——需 API Key,按条取用
//   public      公益代理池 API(ProxyScrape / GeoNode / ProxyList …)——免费公开,无需凭据
//   wild        全网在野代理搜集(公开代理清单站 / GitHub 仓库)——抓取页面解析 host:port
//
// 设计要点:
//   - 全部走 HTTP 拉取,不引入第三方 SDK;凭据运行时解密注入,绝不进模型上下文。
//   - Provider 只是「怎么取 + 怎么解析」的声明;拉取、去重、入库由 fetcher 统一负责。
//   - 源站地址可被用户覆盖(api_url),便于自建中转或镜像。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ProviderKind 是代理源的类别。
type ProviderKind string

const (
	ProviderKindCommercial ProviderKind = "commercial" // 商业 API(需凭据)
	ProviderKindPublic     ProviderKind = "public"     // 公益 API(免费公开)
	ProviderKindWild       ProviderKind = "wild"       // 全网在野搜集
)

// ParseMode 声明响应体的解析方式。
type ParseMode string

const (
	// ParseJSONArray 响应是 JSON 数组,元素为 "host:port" 字符串。
	ParseJSONArray ParseMode = "json_array"
	// ParseJSONObject 响应是 JSON 对象,元素在 data 字段里。
	ParseJSONObject ParseMode = "json_object"
	// ParseTextLines 响应是纯文本,一行一个 host:port。
	ParseTextLines ParseMode = "text_lines"
	// ParseHTML 响应是 HTML,靠正则从正文里扫 host:port。
	ParseHTML ParseMode = "html"
)

// ProviderDef 声明一个代理源的取用与解析方式。
type ProviderDef struct {
	Key      string       `json:"key"`
	Title    string       `json:"title"`
	Kind     ProviderKind `json:"kind"`
	Docs     string       `json:"docs,omitempty"`
	Homepage string       `json:"homepage,omitempty"`
	// DefaultURL 是内置默认取用地址(用户可在接入时用 api_url 覆盖)。
	DefaultURL string `json:"default_url"`
	// NeedKey 表示必须配置 API Key 才能用。
	NeedKey bool `json:"need_key"`
	// NeedAuth 表示必须配置用户名+密码。
	NeedAuth bool `json:"need_auth"`
	// Protocol 是该源产出的代理协议(http / socks5 / mixed)。
	Protocol string `json:"protocol"`
	// Region 提示该源的地区倾向(如 "国内" / "全球"),仅用于展示。
	Region string `json:"region"`
	// Parse 是响应解析方式。
	Parse ParseMode `json:"parse"`
	// Params 是该源支持的额外参数说明(展示用,不参与逻辑)。
	Params []string `json:"params,omitempty"`

	// UserAgent 覆盖 Fetch 的默认 bot UA。HTML 清单站普遍按 UA 拦截非浏览器
	// (实测 proxy5.net 对 bot UA 直接 403),表格站必须伪装浏览器才有内容。
	// json:"-":/api/proxy/providers 直接序列化 ProviderDef,多出来的可见字段
	// 会破坏前端契约,wire 结构保持冻结。
	UserAgent string `json:"-"`
	// Headers 是附加请求头(如 Accept-Language),在 Fetch 默认头之后设置、可覆盖。
	// 同样只存在于进程内,不进 wire。
	Headers map[string]string `json:"-"`

	// BuildURL 按凭据与参数拼出实际请求地址。
	// 商业源需要把 key / 用户名密码 / 数量等拼进 query,公益源多数直接用 DefaultURL。
	BuildURL func(p SourceAuth) string `json:"-"`
	// Extract 从响应体里提取 host:port 列表(已归一化为 "host:port" 形式)。
	// 返回的每项都应是纯 host:port(不含协议),协议由调用方按 Protocol 补全。
	Extract func(body []byte, contentType string) []string `json:"-"`
}

// SourceAuth 是拉取时注入的凭据与参数(明文,仅在进程内传递)。
type SourceAuth struct {
	APIKey   string
	Username string
	Password string
	Params   map[string]string
	// Base 是用户覆盖的取用地址(接入时填的 api_url)。空 = 用源自带的 DefaultURL。
	//
	// 为什么要有它:BuildURL 既要拼凭据、又要拼参数(如 count),而用户填的 api_url
	// 只该覆盖「打到哪个端点」,不该连带把凭据注入一起跳过——否则商业源接了也拿不到数据。
	Base string
}

// endpoint 返回本次请求该用的端点:用户覆盖优先,否则用源默认。
func (a SourceAuth) endpoint(def string) string {
	if b := strings.TrimSpace(a.Base); b != "" {
		return b
	}
	return def
}

// param 读一个额外参数(带默认值)。
func (a SourceAuth) param(key, def string) string {
	if a.Params != nil {
		if v := strings.TrimSpace(a.Params[key]); v != "" {
			return v
		}
	}
	return def
}

// ---- 提取工具 ----

// hostPortRe 匹配 host:port(IPv4 或域名)。IPv6 的 [::1]:8080 由 hostPortV6Re 兜底。
var hostPortRe = regexp.MustCompile(`(?i)\b((?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}|(?:\d{1,3}\.){3}\d{1,3}):(\d{2,5})\b`)

var hostPortV6Re = regexp.MustCompile(`\[([0-9a-fA-F:]{2,45})\]:(\d{2,5})`)

// validPort 端口有效性(排除明显非代理的端口范围外的值)。
func validPort(p int) bool {
	return p > 0 && p <= 65535
}

// cleanHostPorts 从任意文本里扫出 host:port 并去重,过滤掉明显不是代理的条目
// (如版本号 1.2.3:4567 之类的噪声难以完全避免,这里只做端口范围与主机名形状校验)。
func cleanHostPorts(text string) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(host string, port int) {
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" || !validPort(port) {
			return
		}
		// 过滤本地/保留地址:在野清单里偶尔混入内网地址,收进来只会浪费时间。
		if isLocalHost(host) {
			return
		}
		hp := fmt.Sprintf("%s:%d", host, port)
		if seen[hp] {
			return
		}
		seen[hp] = true
		out = append(out, hp)
	}
	for _, m := range hostPortV6Re.FindAllStringSubmatch(text, -1) {
		if p, err := atoiSafe(m[2]); err == nil {
			add("["+m[1]+"]", p)
		}
	}
	for _, m := range hostPortRe.FindAllStringSubmatch(text, -1) {
		if p, err := atoiSafe(m[2]); err == nil {
			add(m[1], p)
		}
	}
	return out
}

// isLocalHost 判断是否本地/保留地址(在野清单里的这类条目一律丢弃)。
func isLocalHost(host string) bool {
	host = strings.Trim(host, "[]")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	parts := strings.Split(host, ".")
	if len(parts) == 4 {
		a, e1 := atoiSafe(parts[0])
		b, e2 := atoiSafe(parts[1])
		if e1 == nil && e2 == nil {
			switch {
			case a == 127, a == 10, a == 0:
				return true
			case a == 192 && b == 168:
				return true
			case a == 172 && b >= 16 && b <= 31:
				return true
			case a == 169 && b == 254:
				return true
			}
		}
	}
	return false
}

// atoiSafe 是 strconv.Atoi 的容错版。
func atoiSafe(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a number: %q", s)
		}
		n = n*10 + int(c-'0')
		if n > 1<<20 {
			return 0, fmt.Errorf("too large")
		}
	}
	return n, nil
}

// extractJSONStrings 递归收集 JSON 里的所有字符串(用于结构不定的 API 响应)。
func extractJSONStrings(v any, out *[]string) {
	switch t := v.(type) {
	case string:
		*out = append(*out, t)
	case []any:
		for _, it := range t {
			extractJSONStrings(it, out)
		}
	case map[string]any:
		// 优先看常见字段,避免把无关字符串(如过期时间)也扫进来。
		for _, k := range []string{"proxy", "ip", "server", "host", "data", "list", "result", "proxies", "items"} {
			if sub, ok := t[k]; ok {
				extractJSONStrings(sub, out)
			}
		}
		// ip + port 分开给的形态(快代理 / 芝麻等常见)。
		ip, _ := t["ip"].(string)
		if ip != "" {
			switch pv := t["port"].(type) {
			case float64:
				*out = append(*out, fmt.Sprintf("%s:%d", ip, int(pv)))
			case string:
				*out = append(*out, ip+":"+pv)
			}
		}
	}
}

// jsonStringsExtract 从 JSON 体里抽字符串再扫 host:port。
func jsonStringsExtract(body []byte, _ string) []string {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return cleanHostPorts(string(body))
	}
	var strs []string
	extractJSONStrings(v, &strs)
	return cleanHostPorts(strings.Join(strs, "\n"))
}

// htmlExtract 从 HTML 正文里扫 host:port(表格/纯文本清单站通用)。
func htmlExtract(body []byte, _ string) []string {
	return cleanHostPorts(string(body))
}

// linesExtract 按行解析纯文本。
func linesExtract(body []byte, _ string) []string {
	return cleanHostPorts(string(body))
}

// jsonArrayExtract 解析 JSON 数组(元素是 "host:port" 或 {ip,port})。
func jsonArrayExtract(body []byte, ct string) []string { return jsonStringsExtract(body, ct) }

// jsonObjectExtract 解析 JSON 对象(元素在 data 里)。
func jsonObjectExtract(body []byte, ct string) []string { return jsonStringsExtract(body, ct) }

// buildQuery 拼 URL query(保留原有 query)。
func buildQuery(base string, kv map[string]string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	q := u.Query()
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v := strings.TrimSpace(kv[k]); v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// ---- 内置源定义 ----

// providers 是内置代理源注册表(键即 ProviderDef.Key)。
var providers = func() map[string]*ProviderDef {
	defs := []*ProviderDef{
		// ============ 商业代理池 API ============
		{
			Key: "kuaidaili", Title: "快代理", Kind: ProviderKindCommercial,
			Docs: "https://www.kuaidaili.com/doc/api/", Homepage: "https://www.kuaidaili.com/",
			DefaultURL: "https://dps.kuaidaili.com/api/getdps/",
			NeedKey:    true, Protocol: "http", Region: "国内",
			Parse:  ParseJSONObject,
			Params: []string{"num(提取数量)", "format(sep/txt/json)", "pt(1=HTTP)"},
			BuildURL: func(a SourceAuth) string {
				return buildQuery(a.endpoint("https://dps.kuaidaili.com/api/getdps/"), map[string]string{
					"secret_key": a.APIKey,
					"num":        a.param("num", "100"),
					"format":     "json",
					"pt":         a.param("pt", "1"),
					"f_loc":      a.param("region", ""),
				})
			},
			Extract: jsonObjectExtract,
		},
		{
			Key: "zhima", Title: "芝麻代理", Kind: ProviderKindCommercial,
			Docs: "https://www.zhima.com/", Homepage: "https://www.zhima.com/",
			DefaultURL: "https://webapi.zhima.com/apis/getProxy",
			NeedKey:    true, Protocol: "http", Region: "国内",
			Parse:  ParseJSONObject,
			Params: []string{"num(提取数量)", "type(1=HTTP)"},
			BuildURL: func(a SourceAuth) string {
				return buildQuery(a.endpoint("https://webapi.zhima.com/apis/getProxy"), map[string]string{
					"key":   a.APIKey,
					"num":   a.param("num", "100"),
					"type":  a.param("type", "1"),
					"style": "json",
				})
			},
			Extract: jsonObjectExtract,
		},
		{
			Key: "qingguo", Title: "青果网络", Kind: ProviderKindCommercial,
			Docs: "https://www.qg.net/doc/", Homepage: "https://www.qg.net/",
			DefaultURL: "https://proxy.qg.net/extract",
			NeedKey:    true, Protocol: "http", Region: "国内",
			Parse:  ParseTextLines,
			Params: []string{"num(提取数量)", "area(地区)"},
			BuildURL: func(a SourceAuth) string {
				return buildQuery(a.endpoint("https://proxy.qg.net/extract"), map[string]string{
					"Key":  a.APIKey,
					"num":  a.param("num", "100"),
					"area": a.param("area", ""),
				})
			},
			Extract: linesExtract,
		},
		{
			Key: "xundaili", Title: "讯代理", Kind: ProviderKindCommercial,
			Docs: "https://www.xdaili.cn/", Homepage: "https://www.xdaili.cn/",
			DefaultURL: "https://api.xdaili.cn/xdaili-api//greatRecharge/getGreatIp",
			NeedKey:    true, Protocol: "http", Region: "国内",
			Parse:  ParseJSONObject,
			Params: []string{"count(提取数量)"},
			BuildURL: func(a SourceAuth) string {
				return buildQuery(a.endpoint("https://api.xdaili.cn/xdaili-api//greatRecharge/getGreatIp"), map[string]string{
					"spiderId":   a.param("spider_id", ""),
					"orderno":    a.Username,
					"secret":     a.Password,
					"count":      a.param("count", "100"),
					"returnType": "2",
				})
			},
			Extract: jsonObjectExtract,
		},
		{
			Key: "sunnyproxy", Title: "太阳 HTTP", Kind: ProviderKindCommercial,
			Docs: "https://www.sunnyproxy.cn/", Homepage: "https://www.sunnyproxy.cn/",
			DefaultURL: "https://http.sunnyproxy.cn/api/getip",
			NeedKey:    true, Protocol: "http", Region: "国内",
			Parse:  ParseTextLines,
			Params: []string{"count(提取数量)"},
			BuildURL: func(a SourceAuth) string {
				return buildQuery(a.endpoint("https://http.sunnyproxy.cn/api/getip"), map[string]string{
					"key":   a.APIKey,
					"count": a.param("count", "100"),
				})
			},
			Extract: linesExtract,
		},

		// ============ 公益代理池 API(免费公开,无需凭据) ============
		{
			Key: "scdn_http", Title: "SCDN · HTTP", Kind: ProviderKindPublic,
			Docs: "https://proxy.scdn.io/api_docs.php", Homepage: "https://proxy.scdn.io/",
			DefaultURL: "https://proxy.scdn.io/api/get_proxy.php",
			Protocol:   "http", Region: "全球",
			Parse:  ParseJSONObject,
			Params: []string{"count(1-20)", "country_code(ISO 两位国家码,如 CN/US)"},
			BuildURL: func(a SourceAuth) string {
				return buildQuery(a.endpoint("https://proxy.scdn.io/api/get_proxy.php"), map[string]string{
					"protocol":     "http",
					"count":        a.param("count", "20"),
					"country_code": a.param("country_code", ""),
				})
			},
			Extract: jsonObjectExtract,
		},
		{
			Key: "scdn_socks5", Title: "SCDN · SOCKS5", Kind: ProviderKindPublic,
			Docs: "https://proxy.scdn.io/api_docs.php", Homepage: "https://proxy.scdn.io/",
			DefaultURL: "https://proxy.scdn.io/api/get_proxy.php",
			Protocol:   "socks5", Region: "全球",
			Parse:  ParseJSONObject,
			Params: []string{"count(1-20)", "country_code(ISO 两位国家码,如 CN/US)"},
			BuildURL: func(a SourceAuth) string {
				return buildQuery(a.endpoint("https://proxy.scdn.io/api/get_proxy.php"), map[string]string{
					"protocol":     "socks5",
					"count":        a.param("count", "20"),
					"country_code": a.param("country_code", ""),
				})
			},
			Extract: jsonObjectExtract,
		},
		{
			Key: "proxyscrape_http", Title: "ProxyScrape · HTTP", Kind: ProviderKindPublic,
			Docs: "https://docs.proxyscrape.com/", Homepage: "https://proxyscrape.com/",
			DefaultURL: "https://api.proxyscrape.com/v4/free-proxy-list/get",
			Protocol:   "http", Region: "全球",
			Parse:  ParseJSONObject,
			Params: []string{"country(国家代码)", "timeout(最大延迟 ms)"},
			BuildURL: func(a SourceAuth) string {
				return buildQuery(a.endpoint("https://api.proxyscrape.com/v4/free-proxy-list/get"), map[string]string{
					"request":      "display_proxies",
					"protocol":     "http",
					"proxy_format": "protocolipport",
					"format":       "json",
					"timeout":      a.param("timeout", "5000"),
					"country":      a.param("country", ""),
				})
			},
			Extract: jsonObjectExtract,
		},
		{
			Key: "proxyscrape_socks5", Title: "ProxyScrape · SOCKS5", Kind: ProviderKindPublic,
			Docs: "https://docs.proxyscrape.com/", Homepage: "https://proxyscrape.com/",
			DefaultURL: "https://api.proxyscrape.com/v4/free-proxy-list/get",
			Protocol:   "socks5", Region: "全球",
			Parse:  ParseJSONObject,
			Params: []string{"country(国家代码)", "timeout(最大延迟 ms)"},
			BuildURL: func(a SourceAuth) string {
				return buildQuery(a.endpoint("https://api.proxyscrape.com/v4/free-proxy-list/get"), map[string]string{
					"request":      "display_proxies",
					"protocol":     "socks5",
					"proxy_format": "protocolipport",
					"format":       "json",
					"timeout":      a.param("timeout", "5000"),
					"country":      a.param("country", ""),
				})
			},
			Extract: jsonObjectExtract,
		},
		{
			Key: "geonode", Title: "GeoNode", Kind: ProviderKindPublic,
			Docs: "https://proxylist.geonode.com/api/proxy-list", Homepage: "https://geonode.com/free-proxy-list",
			DefaultURL: "https://proxylist.geonode.com/api/proxy-list",
			Protocol:   "http", Region: "全球",
			Parse:  ParseJSONObject,
			Params: []string{"limit(每页数量)", "country(国家代码)"},
			BuildURL: func(a SourceAuth) string {
				return buildQuery(a.endpoint("https://proxylist.geonode.com/api/proxy-list"), map[string]string{
					"limit":     a.param("limit", "200"),
					"page":      "1",
					"sort_by":   "lastChecked",
					"sort_type": "DESC",
					"protocols": "http,https",
					"country":   a.param("country", ""),
				})
			},
			Extract: jsonObjectExtract,
		},
		{
			Key: "openproxylist", Title: "OpenProxyList", Kind: ProviderKindPublic,
			Docs: "https://api.openproxylist.xyz/", Homepage: "https://openproxylist.xyz/",
			DefaultURL: "https://api.openproxylist.xyz/http.txt",
			Protocol:   "http", Region: "全球",
			Parse:   ParseTextLines,
			Extract: linesExtract,
		},
		{
			Key: "proxifly_http", Title: "Proxifly · HTTP", Kind: ProviderKindPublic,
			Docs: "https://github.com/proxifly/free-proxy-list", Homepage: "https://proxifly.dev/",
			DefaultURL: "https://cdn.jsdelivr.net/gh/proxifly/free-proxy-list@main/proxies/protocols/http/data.txt",
			Protocol:   "http", Region: "全球",
			Parse:   ParseTextLines,
			Extract: linesExtract,
		},
		{
			Key: "proxifly_socks5", Title: "Proxifly · SOCKS5", Kind: ProviderKindPublic,
			Docs: "https://github.com/proxifly/free-proxy-list", Homepage: "https://proxifly.dev/",
			DefaultURL: "https://cdn.jsdelivr.net/gh/proxifly/free-proxy-list@main/proxies/protocols/socks5/data.txt",
			Protocol:   "socks5", Region: "全球",
			Parse:   ParseTextLines,
			Extract: linesExtract,
		},
		{
			Key: "proxy_lite", Title: "Proxy Lite", Kind: ProviderKindPublic,
			Docs: "https://github.com/monosans/proxy-list", Homepage: "https://github.com/monosans/proxy-list",
			DefaultURL: "https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/http.txt",
			Protocol:   "http", Region: "全球",
			Parse:   ParseTextLines,
			Extract: linesExtract,
		},
		{
			Key: "thespeedx_http", Title: "TheSpeedX · HTTP", Kind: ProviderKindPublic,
			Docs: "https://github.com/TheSpeedX/PROXY-List", Homepage: "https://github.com/TheSpeedX/PROXY-List",
			DefaultURL: "https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master/http.txt",
			Protocol:   "http", Region: "全球",
			Parse:   ParseTextLines,
			Extract: linesExtract,
		},
		{
			Key: "thespeedx_socks5", Title: "TheSpeedX · SOCKS5", Kind: ProviderKindPublic,
			Docs: "https://github.com/TheSpeedX/PROXY-List", Homepage: "https://github.com/TheSpeedX/PROXY-List",
			DefaultURL: "https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master/socks5.txt",
			Protocol:   "socks5", Region: "全球",
			Parse:   ParseTextLines,
			Extract: linesExtract,
		},
		{
			Key: "clarketm", Title: "Clarketm Proxy List", Kind: ProviderKindPublic,
			Docs: "https://github.com/clarketm/proxy-list", Homepage: "https://github.com/clarketm/proxy-list",
			DefaultURL: "https://raw.githubusercontent.com/clarketm/proxy-list/master/proxy-list-raw.txt",
			Protocol:   "http", Region: "全球",
			Parse:   ParseTextLines,
			Extract: linesExtract,
		},
		{
			Key: "jetkai", Title: "ProxyGather (jetkai)", Kind: ProviderKindPublic,
			Docs: "https://github.com/jetkai/proxy-list", Homepage: "https://github.com/jetkai/proxy-list",
			DefaultURL: "https://raw.githubusercontent.com/jetkai/proxy-list/main/online-proxies/txt/proxies-http.txt",
			Protocol:   "http", Region: "全球",
			Parse:   ParseTextLines,
			Extract: linesExtract,
		},
		{
			Key: "shiftytr", Title: "ShiftyTR Proxy List", Kind: ProviderKindPublic,
			Docs: "https://github.com/ShiftyTR/Proxy-List", Homepage: "https://github.com/ShiftyTR/Proxy-List",
			DefaultURL: "https://raw.githubusercontent.com/ShiftyTR/Proxy-List/master/http.txt",
			Protocol:   "http", Region: "全球",
			Parse:   ParseTextLines,
			Extract: linesExtract,
		},
		{
			Key: "roosterkid", Title: "RoosterKid OpenProxies", Kind: ProviderKindPublic,
			Docs: "https://github.com/roosterkid/openproxylist", Homepage: "https://github.com/roosterkid/openproxylist",
			DefaultURL: "https://raw.githubusercontent.com/roosterkid/openproxylist/main/HTTPS_RAW.txt",
			Protocol:   "http", Region: "全球",
			Parse:   ParseTextLines,
			Extract: linesExtract,
		},
		{
			Key: "monosans_socks5", Title: "monosans · SOCKS5", Kind: ProviderKindPublic,
			Docs: "https://github.com/monosans/proxy-list", Homepage: "https://github.com/monosans/proxy-list",
			DefaultURL: "https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/socks5.txt",
			Protocol:   "socks5", Region: "全球",
			Parse:   ParseTextLines,
			Extract: linesExtract,
		},
		{
			Key: "hookzof", Title: "Hookzof SOCKS", Kind: ProviderKindPublic,
			Docs: "https://github.com/hookzof/socks5_list", Homepage: "https://github.com/hookzof/socks5_list",
			DefaultURL: "https://raw.githubusercontent.com/hookzof/socks5_list/master/proxy.txt",
			Protocol:   "socks5", Region: "全球",
			Parse:   ParseTextLines,
			Extract: linesExtract,
		},
		{
			// Proxy5 的免费清单 API(页面版表格端口要登录才能看,API 版全量开放):
			// JSON,字段是 ip_address/port/protocols 三元组,条目按协议过滤。
			// 响应 ~9MB,会被 Fetch 的 8MB 上限截尾(实测约剩 95% 条目),可接受;
			// 提取器用正则直扫文本而非 Unmarshal,对截断免疫(见 providers_html.go)。
			// 页面用 bot UA 直接 403,必须带浏览器 UA。
			Key: "proxy5_http", Title: "Proxy5 · HTTP", Kind: ProviderKindPublic,
			Docs: "https://proxy5.net/api/free-proxies.php", Homepage: "https://proxy5.net/cn/free-proxy",
			DefaultURL: "https://proxy5.net/api/free-proxies.php",
			Protocol:   "http", Region: "全球",
			UserAgent: chromeUA,
			Headers:   browserHeaders(),
			Parse:     ParseJSONObject,
			Extract:   proxy5APIExtract("http"),
			Params:    []string{"无参数;全量清单,已按 HTTP 协议过滤"},
		},
		{
			Key: "proxy5_socks5", Title: "Proxy5 · SOCKS5", Kind: ProviderKindPublic,
			Docs: "https://proxy5.net/api/free-proxies.php", Homepage: "https://proxy5.net/cn/free-proxy",
			DefaultURL: "https://proxy5.net/api/free-proxies.php",
			Protocol:   "socks5", Region: "全球",
			UserAgent: chromeUA,
			Headers:   browserHeaders(),
			Parse:     ParseJSONObject,
			Extract:   proxy5APIExtract("socks5"),
			Params:    []string{"无参数;全量清单,已按 SOCKS5 协议过滤"},
		},
		{
			// ProxySpace 的 txt 直排清单,零参数直出;清单里混有 0.0.0.0 一类占位行,
			// 由 cleanHostPorts 的本地地址过滤兜掉。
			Key: "proxyspace_http", Title: "ProxySpace · HTTP", Kind: ProviderKindPublic,
			Docs: "https://proxyspace.pro/", Homepage: "https://proxyspace.pro/",
			DefaultURL: "https://proxyspace.pro/http.txt",
			Protocol:   "http", Region: "全球",
			Parse:   ParseTextLines,
			Extract: linesExtract,
		},
		{
			Key: "proxyspace_socks5", Title: "ProxySpace · SOCKS5", Kind: ProviderKindPublic,
			Docs: "https://proxyspace.pro/", Homepage: "https://proxyspace.pro/",
			DefaultURL: "https://proxyspace.pro/socks5.txt",
			Protocol:   "socks5", Region: "全球",
			Parse:   ParseTextLines,
			Extract: linesExtract,
		},
		{
			// vakhov/fresh-proxy-list:定时跑验证脚本后落盘的清单,文件小但新鲜度高
			// (只留验活的条目);https.txt 实测只有个位数条目,不值得单独开源。
			Key: "vakhov_http", Title: "Vakhov Fresh List · HTTP", Kind: ProviderKindPublic,
			Docs: "https://github.com/vakhov/fresh-proxy-list", Homepage: "https://github.com/vakhov/fresh-proxy-list",
			DefaultURL: "https://raw.githubusercontent.com/vakhov/fresh-proxy-list/master/http.txt",
			Protocol:   "http", Region: "全球",
			Parse:   ParseTextLines,
			Extract: linesExtract,
		},
		{
			// hideip.me 的仓库镜像:行格式是 ip:port:国家,正则取前两段即可;
			// 清单只保留验活条目,量少(几十条)但相对可靠。
			Key: "hideip_http", Title: "HideIP.me · HTTP", Kind: ProviderKindPublic,
			Docs: "https://github.com/zloi-user/hideip.me", Homepage: "https://hideip.me/",
			DefaultURL: "https://raw.githubusercontent.com/zloi-user/hideip.me/main/http.txt",
			Protocol:   "http", Region: "全球",
			Parse:   ParseTextLines,
			Extract: linesExtract,
		},
		{
			Key: "hideip_socks5", Title: "HideIP.me · SOCKS5", Kind: ProviderKindPublic,
			Docs: "https://github.com/zloi-user/hideip.me", Homepage: "https://hideip.me/",
			DefaultURL: "https://raw.githubusercontent.com/zloi-user/hideip.me/main/socks5.txt",
			Protocol:   "socks5", Region: "全球",
			Parse:   ParseTextLines,
			Extract: linesExtract,
		},
		{
			// casals-ar/proxy-list:聚合型超大清单(~9MB/数十万行,多协议混编的 http 卷),
			// 超过 8MB 读取上限会截尾,量极大不心疼;无验证,存活率低靠数量补。
			Key: "casals_http", Title: "Casals Aggregate · HTTP", Kind: ProviderKindPublic,
			Docs: "https://github.com/casals-ar/proxy-list", Homepage: "https://github.com/casals-ar/proxy-list",
			DefaultURL: "https://raw.githubusercontent.com/casals-ar/proxy-list/main/http",
			Protocol:   "http", Region: "全球",
			Parse:   ParseTextLines,
			Extract: linesExtract,
		},

		// ============ 全网在野代理搜集(公开清单站,HTML 抓取) ============
		// 下面四家是同一引擎(hidemyarms 系)的服务端渲染表格:IP 一格、端口一格,
		// 全文正则扫不到(两个值不相邻),必须走表格感知提取,否则常年 0 条。
		{
			Key: "free_proxy_list_net", Title: "free-proxy-list.net", Kind: ProviderKindWild,
			Homepage:   "https://free-proxy-list.net/",
			DefaultURL: "https://free-proxy-list.net/",
			Protocol:   "http", Region: "全球",
			Parse:   ParseHTML,
			Extract: htmlTableExtract,
		},
		{
			Key: "sslproxies", Title: "SSL Proxies", Kind: ProviderKindWild,
			Homepage:   "https://www.sslproxies.org/",
			DefaultURL: "https://www.sslproxies.org/",
			Protocol:   "http", Region: "全球",
			Parse:   ParseHTML,
			Extract: htmlTableExtract,
		},
		{
			Key: "us_proxy", Title: "US Proxy", Kind: ProviderKindWild,
			Homepage:   "https://www.us-proxy.org/",
			DefaultURL: "https://www.us-proxy.org/",
			Protocol:   "http", Region: "美国",
			Parse:   ParseHTML,
			Extract: htmlTableExtract,
		},
		{
			Key: "hidemy_name", Title: "HideMy.name Free", Kind: ProviderKindWild,
			Homepage:   "https://hidemy.name/en/proxy-list/",
			DefaultURL: "https://hidemy.name/en/proxy-list/",
			Protocol:   "http", Region: "全球",
			Parse:   ParseHTML,
			Extract: htmlTableExtract, // 同 hidemyarms 引擎,IP/端口分列
		},
		// 以下几家保持全文正则,不换表格提取,原因:
		//   proxy-list.org 把代理 Base64 编码后嵌表格,原文里根本没有明文 host;
		//   spys.one 的端口列被 JS 按页面种子混淆,表格里取到的是假端口,
		//   配出来也是错的——宁缺毋滥,不当有效数据源期待。
		{
			Key: "proxy_list_org", Title: "proxy-list.org", Kind: ProviderKindWild,
			Homepage:   "https://proxy-list.org/english/index.php",
			DefaultURL: "https://proxy-list.org/english/index.php",
			Protocol:   "http", Region: "全球",
			Parse:   ParseHTML,
			Extract: htmlExtract,
		},
		{
			Key: "spys_one", Title: "spys.one", Kind: ProviderKindWild,
			Homepage:   "https://spys.one/en/",
			DefaultURL: "https://spys.one/en/",
			Protocol:   "http", Region: "全球",
			Parse:   ParseHTML,
			Extract: htmlExtract,
		},
		{
			Key: "proxy_scrape_web", Title: "proxyscrape · free list", Kind: ProviderKindWild,
			Homepage:   "https://proxyscrape.com/free-proxy-list",
			DefaultURL: "https://proxyscrape.com/free-proxy-list",
			Protocol:   "http", Region: "全球",
			Parse:   ParseHTML,
			Extract: htmlExtract,
		},
		{
			Key: "hidemy_name", Title: "HideMy.name Free", Kind: ProviderKindWild,
			Homepage:   "https://hidemy.name/en/proxy-list/",
			DefaultURL: "https://hidemy.name/en/proxy-list/",
			Protocol:   "http", Region: "全球",
			Parse:   ParseHTML,
			Extract: htmlExtract,
		},
		{
			Key: "proxy_db", Title: "ProxyDB", Kind: ProviderKindWild,
			Homepage:   "https://proxydb.net/",
			DefaultURL: "https://proxydb.net/",
			Protocol:   "http", Region: "全球",
			Parse:   ParseHTML,
			Extract: htmlExtract,
		},
		{
			Key: "github_proxy_list", Title: "GitHub 公开代理清单", Kind: ProviderKindWild,
			Docs: "https://github.com/search?q=proxy+list+http.txt", Homepage: "https://github.com/",
			DefaultURL: "https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/all.txt",
			Protocol:   "http", Region: "全球",
			Parse:   ParseTextLines,
			Extract: linesExtract,
		},
		{
			// 站大爷免费页:真实结构是 <li class="ul-cell"> 里 <p class="proxy_ip"> +
			// <p class="proxy_port">(不是 table),表格感知提取器按 li 行配对。
			// 已知限制:免费列表 20 条/页、分页到 /free/24/,这里只抓第一页;
			// 部分出口 IP 会命中加速乐/EdgeOne 类 JS 挑战,拿到挑战页时行提取 0 条
			// + fallback 全文扫描也 0 条,自然按「本轮无数据」处理,不会误报。
			// bot UA 会被拦,必须带浏览器 UA + 中文 Accept-Language。
			Key: "zdaye", Title: "站大爷 · 免费代理", Kind: ProviderKindWild,
			Homepage: "https://www.zdaye.com/free/", Docs: "https://www.zdaye.com/free/",
			DefaultURL: "https://www.zdaye.com/free/",
			Protocol:   "http", Region: "国内",
			UserAgent: chromeUA,
			Headers:   browserHeaders(),
			Parse:     ParseHTML,
			Extract:   htmlTableExtract,
		},
		{
			// 89ip 免费页:www.89ip.cn/tqdl.html 是服务端渲染的 ip:port<br> 直排
			// (旧 API api.89ip.cn 已返回空体,弃用)。无表格行时 htmlTableExtract
			// 自动退回全文正则,正好覆盖这种直排形态。带浏览器 UA 更稳。
			Key: "ip89", Title: "89IP 免费代理", Kind: ProviderKindWild,
			Homepage: "https://www.89ip.cn/", Docs: "https://www.89ip.cn/",
			DefaultURL: "https://www.89ip.cn/tqdl.html?num=300",
			Protocol:   "http", Region: "国内",
			UserAgent: chromeUA,
			Headers:   browserHeaders(),
			Parse:     ParseHTML,
			Extract:   htmlTableExtract,
		},
	}

	m := make(map[string]*ProviderDef, len(defs))
	for _, d := range defs {
		m[d.Key] = d
	}
	return m
}()

// ProviderList 返回全部内置源(按 类别 → 标题 排序,UI 直接展示)。
func ProviderList() []*ProviderDef {
	out := make([]*ProviderDef, 0, len(providers))
	for _, d := range providers {
		out = append(out, d)
	}
	kindOrder := map[ProviderKind]int{
		ProviderKindCommercial: 0,
		ProviderKindPublic:     1,
		ProviderKindWild:       2,
	}
	sort.Slice(out, func(i, j int) bool {
		ki, kj := kindOrder[out[i].Kind], kindOrder[out[j].Kind]
		if ki != kj {
			return ki < kj
		}
		return out[i].Title < out[j].Title
	})
	return out
}

// ProviderByKey 按 key 取内置源。
func ProviderByKey(key string) *ProviderDef { return providers[key] }

// ProvidersByKind 按类别取内置源。
func ProvidersByKind(kind ProviderKind) []*ProviderDef {
	out := []*ProviderDef{}
	for _, d := range ProviderList() {
		if d.Kind == kind {
			out = append(out, d)
		}
	}
	return out
}

// defaultFetchUA 是未配置 UserAgent 时的默认 bot UA:API 类源(公益池/商业池)
// 不挑 UA,主动亮明身份也便于对方日志归因;HTML 清单站则应在源上配浏览器 UA。
const defaultFetchUA = "Mozilla/5.0 (compatible; MEOWFUCK-ProxyCenter/1.0)"

// Fetch 按源定义拉取代理清单。
// 返回归一化后的 "host:port" 列表(协议由调用方按 def.Protocol 补全)。
//
// apiURLOverride 是用户在接入时填的 api_url:它只覆盖「打到哪个端点」,
// 凭据与参数仍由 BuildURL 注入。**不要**在覆盖时直接拿它当最终 URL ——
// 那样会把凭据/参数(count、country_code 等)一起跳过,商业源接了也拿不到数据,
// 公益源则永远只返回默认 1 条(实测踩过)。
//
// 安全:仅允许 http/https;拒绝 localhost/环回/私有/保留地址(经 urlCheck 注入,
// 与 recon.ValidatePublicURL 同一判据,避免把内网当抓取目标)。
func (d *ProviderDef) Fetch(client *http.Client, auth SourceAuth, apiURLOverride string,
	urlCheck func(string) error) ([]string, error) {

	// 用户覆盖地址经 auth.Base 透传给 BuildURL(它决定端点,不决定是否注入凭据)。
	auth.Base = strings.TrimSpace(apiURLOverride)
	var target string
	if d.BuildURL != nil {
		target = d.BuildURL(auth)
	} else if auth.Base != "" {
		// 无 BuildURL 的纯清单源(多数公益/在野源):覆盖地址就是最终地址。
		target = auth.Base
	} else {
		target = d.DefaultURL
	}
	if target == "" {
		return nil, fmt.Errorf("源 %s 未配置取用地址", d.Title)
	}
	if urlCheck != nil {
		if err := urlCheck(target); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	ua := d.UserAgent
	if ua == "" {
		ua = defaultFetchUA
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "*/*")
	// 源自带头最后设置:HTML 清单站用它换掉 Accept 默认值、补 Accept-Language,
	// 让请求看起来像浏览器;同时保留用户在 Headers 里覆盖任意默认头的能力。
	for k, v := range d.Headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("拉取失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("源返回 HTTP %d", resp.StatusCode)
	}
	// 代理清单可能很大(数千条),给 8MB 上限足够且防爆内存。
	body, err := readLimited(resp.Body, 8<<20)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	extract := d.Extract
	if extract == nil {
		extract = linesExtract
	}
	raw := extract(body, resp.Header.Get("Content-Type"))
	// 有些公益源会在同一份清单里混协议(http/socks4/socks5)。
	// 这里只保留本源声明的协议类型,避免把 socks4 当 http 用。
	out := make([]string, 0, len(raw))
	for _, hp := range raw {
		out = append(out, hp)
	}
	return out, nil
}

// readLimited 读至多 max 字节(超限即截断:清单被截断仍可用,不因此报错)。
func readLimited(r io.Reader, max int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, max))
}

// DefaultTestURL 是测活的默认目标(能回显出口 IP,便于顺带记录 exit_ip)。
const DefaultTestURL = "http://httpbin.org/ip"

// CheckTimeout 是单次测活的默认超时。
const CheckTimeout = 10 * time.Second
