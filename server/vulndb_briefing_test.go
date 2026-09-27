package server

// vulndb_briefing.go 纯函数单测(契约 POC_INTEL_CONTRACTS.md §4.6):
// 停用词表/指纹分词/简报组拼/检索文本格式——不出网、不连库。

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// TestVulnBriefNoiseWords 停用词表两方向:真指纹绝不停用,泛词必须停掉。
func TestVulnBriefNoiseWords(t *testing.T) {
	// 真指纹(契约点名 nginx/apache/shiro,补常见组件)不得进停用词表。
	keep := []string{"nginx", "apache", "shiro", "tomcat", "struts", "weblogic", "websphere",
		"jenkins", "gitlab", "confluence", "zabbix", "elasticsearch", "redis", "mysql",
		"php", "java", "spring", "drupal", "若依", "通达oa", "用友"}
	for _, w := range keep {
		if vulnBriefNoiseWords[w] {
			t.Errorf("真指纹 %q 不应在停用词表", w)
		}
		if !vulnBriefTermAllowed(w) {
			t.Errorf("真指纹 %q 应通过放行判定", w)
		}
	}
	// 契约点名必停:html/script/text/javascript/http/https/www/com + 泛运维词。
	stop := []string{"html", "script", "text", "javascript", "http", "https", "www", "com",
		"server", "service", "web", "system", "admin", "portal", "index", "welcome", "login"}
	for _, w := range stop {
		if !vulnBriefNoiseWords[w] {
			t.Errorf("泛词 %q 应在停用词表", w)
		}
		if vulnBriefTermAllowed(w) {
			t.Errorf("泛词 %q 不应通过放行判定", w)
		}
	}
	// 放行判定边界:纯数字/版本碎片/超短词不放行;含字母的中长词放行。
	cases := map[string]bool{
		"3.6":    false, // 分词后为纯数字 token,无字母
		"18":     false, // 纯数字
		"go":     false, // <3 字节
		"js":     false, // <3 字节
		"oa":     false, // <3 字节(「通达OA」整体会放行,这里只测孤词)
		"oa123":  true,  // 字母+数字混合且 ≥3 字节
		"用友":     true,  // CJK 按 3 字节/字,≥3 字节放行
		"nc":     false,
		"1.1.1v": true, // u2002-jira 这类版本化指纹含字母,放行
	}
	for in, want := range cases {
		if got := vulnBriefTermAllowed(in); got != want {
			t.Errorf("vulnBriefTermAllowed(%q) = %v, 期望 %v", in, got, want)
		}
	}
}

// TestVulnBriefTokenize 分词:按非字母/数字切分(不改大小写,小写归一由调用方做),CJK 连续串保词。
func TestVulnBriefTokenize(t *testing.T) {
	got := vulnBriefTokenize("Nginx 1.18.0 (Ubuntu)")
	want := []string{"Nginx", "1", "18", "0", "Ubuntu"}
	if len(got) != len(want) {
		t.Fatalf("tokenize 切片长度 = %d (%v), 期望 %v", len(got), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tokenize[%d] = %q, 期望 %q", i, got[i], want[i])
		}
	}
	if got := vulnBriefTokenize("通达OA-办公系统"); len(got) != 2 || got[0] != "通达OA" || got[1] != "办公系统" {
		t.Errorf("CJK 连续串应保词: got %v", got)
	}
}

// TestVulnBriefFingerprints 资产→指纹词:technologies 优先、去重、去噪、纯数字丢弃。
func TestVulnBriefFingerprints(t *testing.T) {
	assets := []*db.Asset{
		{
			Technologies: []string{"Nginx", "jQuery 3.6.1", "nginx", "OpenResty"},
			ServiceName:  "http server",
			PageTitle:    "Welcome to nginx! 某某管理系统登录页",
		},
		{Technologies: []string{"Apache Shiro"}},
		nil, // 容错:空资产不炸
	}
	terms := vulnBriefFingerprints(assets)
	got := map[string]bool{}
	for _, term := range terms {
		got[term] = true
	}
	want := []string{"nginx", "jquery", "openresty", "apache", "shiro"}
	for _, w := range want {
		if !got[w] {
			t.Errorf("指纹词缺 %q: got %v", w, terms)
		}
	}
	// 噪声/虚词/版本碎片一律不出现。
	for _, n := range []string{"http", "server", "welcome", "to", "3", "6", "1", "nginx!"} {
		if got[n] {
			t.Errorf("指纹词混入噪声 %q: got %v", n, terms)
		}
	}
	// 去重:nginx 只出现一次(technologies 与 page_title 各来一次)。
	count := 0
	for _, term := range terms {
		if term == "nginx" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("nginx 应去重为 1 次, 实际 %d 次: %v", count, terms)
	}
	// 优先级:technologies 排在 page_title 分词之前。
	if terms[0] != "nginx" {
		t.Errorf("指纹词应按 technologies 优先排序, got[0]=%q: %v", terms[0], terms)
	}
	// 上限:不超过 vulnBriefMaxTerms。
	if len(terms) > vulnBriefMaxTerms {
		t.Errorf("指纹词应截 %d 个, 实际 %d", vulnBriefMaxTerms, len(terms))
	}
	// 空资产/空指纹:返回空切片,不炸。
	if terms := vulnBriefFingerprints([]*db.Asset{{}}); len(terms) != 0 {
		t.Errorf("无指纹资产应得空列表, got %v", terms)
	}
	if terms := vulnBriefFingerprints(nil); len(terms) != 0 {
		t.Errorf("nil 资产应得空列表, got %v", terms)
	}
}

// vulnBriefTestEntry 造条目的便捷函数。
func vulnBriefTestEntry(kev bool, severity, cveID, title, vendor, product string, pocs int) *db.VulnEntry {
	return &db.VulnEntry{
		KEV: kev, Severity: severity, CVEID: cveID, Title: title,
		Vendor: vendor, Product: product, PocCount: pocs,
	}
}

// TestVulnBriefAssembleText 简报组拼:标题计数/KEV 段/高危段/其他段/条目格式/尾注/空命中。
func TestVulnBriefAssembleText(t *testing.T) {
	entries := []*db.VulnEntry{
		vulnBriefTestEntry(false, "high", "CVE-2016-4437", "Apache Shiro 反序列化漏洞", "apache", "shiro", 5),
		vulnBriefTestEntry(true, "critical", "CVE-2021-44228", "Apache Log4j2 远程代码执行漏洞", "apache", "log4j2", 3),
		vulnBriefTestEntry(false, "medium", "", "某组件信息泄露", "", "", 1),
		vulnBriefTestEntry(false, "high", "CVE-2022-22965", "Spring Framework RCE", "vmware", "spring framework", 0),
	}
	text := assembleVulnBriefText([]string{"shiro", "log4j2"}, entries)

	if !strings.Contains(text, "【CVE 情报简报——按本任务资产指纹匹配（4 条）】") {
		t.Errorf("总标题/计数不符:\n%s", text)
	}
	if !strings.Contains(text, "匹配指纹: shiro, log4j2") {
		t.Errorf("应列出匹配指纹:\n%s", text)
	}
	if !strings.Contains(text, "一、KEV 在野利用（1 条）") {
		t.Errorf("KEV 段缺失或计数不符:\n%s", text)
	}
	if !strings.Contains(text, "二、高危漏洞（critical/high, 2 条）") {
		t.Errorf("高危段缺失或计数不符:\n%s", text)
	}
	if !strings.Contains(text, "三、其他匹配（1 条）") {
		t.Errorf("其他段缺失或计数不符:\n%s", text)
	}
	// 契约条目格式:- [KEV][critical] CVE-xxx 标题（厂商 产品）PoC xN(括号后直接跟 PoC)。
	wantLine := "- [KEV][critical] CVE-2021-44228 Apache Log4j2 远程代码执行漏洞（apache log4j2）PoC x3"
	if !strings.Contains(text, wantLine) {
		t.Errorf("KEV 条目格式不符, 期望包含:\n%s\n实际:\n%s", wantLine, text)
	}
	// 非 KEV 条目不带 [KEV] 标记。
	if strings.Count(text, "- [high]") != 2 {
		t.Errorf("非 KEV 高危条目应为 - [high] 格式 x2:\n%s", text)
	}
	// 无 CVE 编号的条目不输出空编号;无厂商产品时用空格隔开 PoC 计数。
	if !strings.Contains(text, "- [medium] 某组件信息泄露 PoC x1") {
		t.Errorf("无编号条目格式不符:\n%s", text)
	}
	if !strings.Contains(text, "- [high] CVE-2022-22965 Spring Framework RCE（vmware spring framework）\n") {
		t.Errorf("PoC x0 条目不应带 PoC 计数:\n%s", text)
	}
	// 尾注指引。
	if !strings.Contains(text, "vulndb_search") || !strings.Contains(text, "任务约束") {
		t.Errorf("尾注指引缺失:\n%s", text)
	}
	// 空命中 → ""(不注入)。
	if got := assembleVulnBriefText([]string{"shiro"}, nil); got != "" {
		t.Errorf("空命中应返回空串, got %q", got)
	}
}

// TestVulnBriefSearchFormat 检索文本:首行命中计数 + 行内管道字段 + 空命中「未命中」。
func TestVulnBriefSearchFormat(t *testing.T) {
	items := []*db.VulnEntry{
		vulnBriefTestEntry(true, "critical", "CVE-2021-44228", "Apache Log4j2 RCE", "apache", "log4j2", 3),
		vulnBriefTestEntry(false, "high", "CVE-2016-4437", "Apache Shiro 反序列化", "apache", "shiro", 2),
	}
	items[0].Tags = json.RawMessage(`["rce","kev"]`)
	out := formatVulnSearchResult("shiro", items, 7)
	lines := strings.Split(out, "\n")
	if lines[0] != "漏洞库检索「shiro」命中 2/7 条" {
		t.Errorf("首行不符: %q", lines[0])
	}
	if !strings.Contains(lines[1], "CVE-2021-44228 | Apache Log4j2 RCE | critical | KEV | apache log4j2 | PoC x3 | rce,kev") {
		t.Errorf("条目行格式不符: %q", lines[1])
	}
	if !strings.Contains(lines[2], "CVE-2016-4437 | Apache Shiro 反序列化 | high | apache shiro | PoC x2") {
		t.Errorf("条目行格式不符: %q", lines[2])
	}
	if got := formatVulnSearchResult("shiro", nil, 0); got != "未命中" {
		t.Errorf("空命中应输出「未命中」, got %q", got)
	}
}
