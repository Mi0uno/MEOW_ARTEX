package proxycenter

// HTML 清单站专用提取器与浏览器伪装头。
//
// 背景:站大爷 / 快代理免费页 / proxy5 这类站点把 IP 与端口分放在同一「行」的
// 不同单元格里(IP 一格、端口一格),两个值在原文里并不相邻,全文正则
// (cleanHostPorts)对这种形态扫不出任何东西。这里用 x/net/html 解析 DOM,
// 按「行容器」收集单元格文本,行内同时出现 host 形状与 port 形状才配对产出。
//
// 铁律:判断不了的行直接放弃(宁缺毋滥)——脏端口入库只会浪费测活配额,
// 还可能把错端口当真代理用出去。

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// chromeUA 是桌面版 Chrome 完整 UA 串:HTML 清单站普遍按 UA 白名单放行浏览器、
// 对非浏览器 UA 返回 403 或 JS 挑战页(实测 proxy5.net 用 bot UA 直接 403),
// 所以抓页面的源必须带它。只存在于进程内(json:"-"),不进 wire。
const chromeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// browserHeaders 给 HTML 清单站配一组像人浏览的请求头。
// Accept 覆盖 Fetch 里的默认 "*/*",Accept-Language 让中文站优先回中文页。
func browserHeaders() map[string]string {
	return map[string]string{
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8",
		"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
	}
}

// ---- 单元格形状判定(全部整格锚定,避免格内长文本被部分匹配误判) ----

var cellIPv4Re = regexp.MustCompile(`^(?:\d{1,3}\.){3}\d{1,3}$`)
var cellDomainRe = regexp.MustCompile(`(?i)^(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}$`)
var cellHostPortRe = regexp.MustCompile(`^(?i)((?:\d{1,3}\.){3}\d{1,3}|(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}):(\d{1,5})$`)

// 从候选文本里抹掉 IPv4 与小数后,剩下的独立数字才按端口看待:
// IP 四段(116.63.130.30)、延迟(0.38)、日期时间(09-27 15:00:01)里的数都不是端口。
var anyIPv4Re = regexp.MustCompile(`(?:\d{1,3}\.){3}\d{1,3}`)
var decimalNumRe = regexp.MustCompile(`\d+\.\d+`)
var numberTokenRe = regexp.MustCompile(`\d+`)

// portCandidate 是行内一个「像端口」的数字及其置信度。
// score 3 = 整格就是纯数字,或格内带 port/端口 字样;score 1 = 格里顺带出现的数字。
type portCandidate struct {
	value int
	score int
}

// portCandidatesIn 从一段单元格文本里挑端口候选。
func portCandidatesIn(text string) []portCandidate {
	trimmed := strings.TrimSpace(text)
	// 先抹 IPv4 与小数,再取数字 token,顺序不能反(反了 IP 段会被当端口)。
	s := anyIPv4Re.ReplaceAllString(trimmed, " ")
	s = decimalNumRe.ReplaceAllString(s, " ")
	low := strings.ToLower(text)
	var out []portCandidate
	seen := map[int]bool{}
	for _, tok := range numberTokenRe.FindAllString(s, -1) {
		n, err := atoiSafe(tok)
		if err != nil || !validPort(n) || seen[n] {
			continue
		}
		seen[n] = true
		score := 1
		// 整格纯数字,或带 port/端口 字样 → 高置信(端口列几乎都是这两种)。
		if s == tok || strings.Contains(low, "port") || strings.Contains(text, "端口") {
			score = 3
		}
		out = append(out, portCandidate{value: n, score: score})
	}
	return out
}

// hostPortFromCells 在一行的单元格文本里配出 host:port。
// 端口判不了(多个同分不同值)就放弃整行。
func hostPortFromCells(cells []string) (string, bool) {
	// 有的站把 host:port 塞同一格,先处理这种,拿到即返回。
	host := ""
	for _, c := range cells {
		t := strings.TrimSpace(c)
		if t == "" {
			continue
		}
		if m := cellHostPortRe.FindStringSubmatch(t); m != nil {
			h := strings.ToLower(m[1])
			p, err := atoiSafe(m[2])
			if err != nil || !validPort(p) || isLocalHost(h) {
				continue
			}
			return fmt.Sprintf("%s:%d", h, p), true
		}
		if host == "" && (cellIPv4Re.MatchString(t) || cellDomainRe.MatchString(t)) {
			host = strings.ToLower(t)
		}
	}
	if host == "" || isLocalHost(host) {
		return "", false
	}
	// 端口候选:跳过 host 格本身(IP 里的段不是端口)。
	// 取分最高的唯一值;最高分上还有不同取值 → 判不了,放弃。
	best, bestScore, ambiguous := 0, 0, false
	for _, c := range cells {
		t := strings.TrimSpace(c)
		if strings.EqualFold(t, host) {
			continue
		}
		for _, pc := range portCandidatesIn(c) {
			switch {
			case pc.score > bestScore:
				best, bestScore, ambiguous = pc.value, pc.score, false
			case pc.score == bestScore && pc.value != best:
				ambiguous = true
			}
		}
	}
	if best == 0 || ambiguous {
		return "", false
	}
	return fmt.Sprintf("%s:%d", host, best), true
}

// nodeText 收集元素内全部文本并把连续空白压成单空格(单元格里常混换行/缩进)。
// 文本节点之间补空格:<br> 分隔的 "1.2.3.4:80" 与 "5.6.7.8:8080" 若直接拼接
// 会粘成 ":805.6." 之类的串,正则就全匹配不上了。
// 跳过 script/style:清单站常在行内塞脚本,里面的数字会把端口判定搅浑。
func nodeText(n *html.Node) string {
	var parts []string
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode {
			parts = append(parts, x.Data)
			return
		}
		if x.Type == html.ElementNode && (x.Data == "script" || x.Data == "style") {
			return
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

// rowCellTexts 收集一行的单元格文本:
//   - tr → 所有 td/th 直接子元素(表头/表体通用);
//   - li → 直接子元素的文本(站大爷把 IP/端口分放在两个 <p> 里),
//     嵌套列表不算格;没有任何子元素就把整行当一格兜底。
func rowCellTexts(n *html.Node) []string {
	isTr := n.Data == "tr"
	var cells []string
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		if isTr && c.Data != "td" && c.Data != "th" {
			continue
		}
		if c.Data == "ul" || c.Data == "ol" {
			// 行内嵌套列表是子清单不是单元格,留给外层遍历单独处理。
			continue
		}
		cells = append(cells, nodeText(c))
	}
	if !isTr && len(cells) == 0 {
		if t := strings.TrimSpace(nodeText(n)); t != "" {
			cells = []string{t}
		}
	}
	return cells
}

// htmlTableExtract 是「行清单」感知的 HTML 提取:遍历 DOM 上所有 tr/li,
// 行内同时存在 host 形状与 port 形状才产出 host:port,去重 + 过滤本地地址。
// 一行都没捞到时退回 cleanHostPorts 全文扫描(89ip 这类 ip:port<br> 直排页、
// 或反爬只回了挑战页时的兜底,保证行为不比旧的全文正则差)。
func htmlTableExtract(body []byte, _ string) []string {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return cleanHostPorts(string(body))
	}
	seen := map[string]bool{}
	out := []string{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "tr" || n.Data == "li") {
			if hp, ok := hostPortFromCells(rowCellTexts(n)); ok && !seen[hp] {
				seen[hp] = true
				out = append(out, hp)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if len(out) == 0 {
		// 行清单一条没捞到时退回全文兜底(89ip 这类 ip:port<br> 直排页)。
		// 兜底文本取「去掉 script/style 后的 DOM 文本」而非原始字节:
		// 不少清单站把混淆端口、demo 数据塞在脚本里,原始字节扫描会把
		// 脚本变量当代理收进来。
		if doc != nil {
			return cleanHostPorts(nodeText(doc))
		}
		return cleanHostPorts(string(body))
	}
	return out
}

// ---- proxy5 JSON API 专用提取 ----

// proxy5PairRe 在原始 JSON 文本上配对 ip_address/port/protocols 三元组。
// 为什么不用 json.Unmarshal:该 API 响应 ~9MB,超过 Fetch 的 8MB 读取上限,
// 截断后整段 Unmarshal 必失败;正则扫描对截断免疫,只损失尾部数据。
var proxy5PairRe = regexp.MustCompile(`"ip_address"\s*:\s*"([^"]+)"\s*,\s*"port"\s*:\s*(\d+)[^{}]*?"protocols"\s*:\s*\[([^\]]*)\]`)

// proxy5PairLooseRe / ipAddrRe / portNumRe 是宽松配对:不要求 ip 与 port 相邻、
// 不要求 protocols 存在,只在同一个 {…} 对象块里各自找值。站点改字段顺序时
// 至少还能按对象块配对出数据,而不是直接归零。
var proxy5ObjRe = regexp.MustCompile(`\{[^{}]*\}`)
var ipAddrRe = regexp.MustCompile(`"ip_address"\s*:\s*"([^"]+)"`)
var portNumRe = regexp.MustCompile(`"port"\s*:\s*(\d+)`)

// proxy5APIExtract 生成按协议过滤的 proxy5 提取器。
// 清单里 HTTP 与 SOCKS5 混在一起(各约四成),协议写错入库只会白测;
// want 取 "http" / "socks5",按 protocols 数组包含关系过滤。
func proxy5APIExtract(want string) func([]byte, string) []string {
	return func(body []byte, _ string) []string {
		type pair struct {
			host  string
			port  int
			proto string
		}
		var pairs []pair
		for _, m := range proxy5PairRe.FindAllSubmatch(body, -1) {
			pairs = append(pairs, pair{string(m[1]), atoiSafeMust(string(m[2])), strings.ToUpper(string(m[3]))})
		}
		if len(pairs) == 0 {
			// 严格三元组一个没配上(字段顺序变了?)退对象块宽松配对,
			// 协议无从过滤就不再过滤——宁可有少量错协议候选,好过整源归零。
			for _, obj := range proxy5ObjRe.FindAll(body, -1) {
				im := ipAddrRe.FindSubmatch(obj)
				pm := portNumRe.FindSubmatch(obj)
				if im == nil || pm == nil {
					continue
				}
				pairs = append(pairs, pair{string(im[1]), atoiSafeMust(string(pm[1])), ""})
			}
		}
		wantUp := strings.ToUpper(want)
		seen := map[string]bool{}
		out := []string{}
		for _, p := range pairs {
			if p.proto != "" && !strings.Contains(p.proto, wantUp) {
				continue
			}
			h := strings.ToLower(strings.TrimSpace(p.host))
			if h == "" || !validPort(p.port) || isLocalHost(h) {
				continue
			}
			hp := fmt.Sprintf("%s:%d", h, p.port)
			if seen[hp] {
				continue
			}
			seen[hp] = true
			out = append(out, hp)
		}
		return out
	}
}

// atoiSafeMust atoiSafe 的必成功版:正则捕获组保证是纯数字,失败只能说明
// 数字超长(攻击性构造的响应),按 0 处理让 validPort 拦下。
func atoiSafeMust(s string) int {
	n, err := atoiSafe(s)
	if err != nil {
		return 0
	}
	return n
}
