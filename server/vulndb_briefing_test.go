package server

// vulndb_briefing.go 纯函数单测(契约 POC_INTEL_CONTRACTS.md §4.6):
// 停用词表/指纹分词/简报组拼/检索文本格式——不出网、不连库。

import (
	"encoding/json"
	"fmt"
	"net/url"
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

// TestVulnBriefNoiseWordsExpanded 契约 §10.2 扩充停用词全表锁定(逐词,含 <3 字节的
// v1/v2/js——表内兜底)+ 真指纹(含契约点名的 seeyon/致远oa)绝不停;并验证指纹聚合
// 链路上的大小写归一与灰显零命中词保留。
func TestVulnBriefNoiseWordsExpanded(t *testing.T) {
	// 契约 §10.2 清单逐词:unknown/suspected/response/request/proxy/tcp/udp/http/https/
	// spa/editor/login/index/main/static/manager/system/enterprise/default/admin/example/
	// sample/test/temp/local/service/services/app/web/core/common/public/old/new/v1/v2/
	// content/js。
	stop := []string{
		"unknown", "suspected", "response", "request", "proxy", "tcp", "udp", "http", "https",
		"spa", "editor", "login", "index", "main", "static", "manager", "system", "enterprise",
		"default", "admin", "example", "sample", "test", "temp", "local", "service", "services",
		"app", "web", "core", "common", "public", "old", "new", "v1", "v2", "content", "js",
	}
	for _, w := range stop {
		if !vulnBriefNoiseWords[w] {
			t.Errorf("契约 §10.2 停用词 %q 缺失", w)
		}
	}
	// 真指纹绝不停:契约点名 nginx/apache/shiro/致远oa/seeyon + 常见组件。
	keep := []string{"nginx", "apache", "shiro", "seeyon", "致远oa", "tomcat", "weblogic",
		"jenkins", "spring", "若依", "通达oa"}
	for _, w := range keep {
		if vulnBriefNoiseWords[w] {
			t.Errorf("真指纹 %q 不应被 §10.2 扩充误停", w)
		}
	}
	// 大小写归一在指纹聚合处完成:大写形态的停用词被滤掉,真指纹保留。
	terms := vulnBriefFingerprints([]*db.Asset{{
		Technologies: []string{"Seeyon 致远OA", "Unknown Service"},
		PageTitle:    "系统登录 - static content v1",
	}})
	got := map[string]bool{}
	for _, term := range terms {
		got[term] = true
	}
	for _, w := range []string{"seeyon", "致远oa", "系统登录"} {
		if !got[w] {
			t.Errorf("真指纹 %q 应保留: got %v", w, terms)
		}
	}
	for _, w := range []string{"unknown", "service", "static", "content", "v1"} {
		if got[w] {
			t.Errorf("停用词 %q(大小写不敏感)不应出现在指纹里: got %v", w, terms)
		}
	}
}

// TestVulnBriefFingerprintHits hits 拼装(契约 §10.2):截前 24 词逐词计数、按 hits
// 降序(同数保持信号强度顺序)、0 命中词保留、计数器错误上抛。
func TestVulnBriefFingerprintHits(t *testing.T) {
	terms := make([]string, 0, 30)
	for i := 0; i < 30; i++ {
		terms = append(terms, fmt.Sprintf("term%02d", i))
	}
	counts := map[string]int{"term00": 3, "term01": 7, "term02": 3, "term05": 12}
	hits, err := assembleVulnFingerprintHits(terms, func(term string) (int, error) {
		return counts[term], nil // map 缺省 0=零命中词保留
	})
	if err != nil {
		t.Fatalf("拼装不应报错: %v", err)
	}
	if len(hits) != vulnBriefFPMaxHits {
		t.Fatalf("应截前 %d 个指纹词, 实际 %d", vulnBriefFPMaxHits, len(hits))
	}
	// 降序:hits[0]=term05(12) > hits[1]=term01(7)。
	if hits[0].Term != "term05" || hits[0].Hits != 12 {
		t.Errorf("hits[0] 应为 term05 x12, got %+v", hits[0])
	}
	if hits[1].Term != "term01" || hits[1].Hits != 7 {
		t.Errorf("hits[1] 应为 term01 x7, got %+v", hits[1])
	}
	// 同数稳定:term00 与 term02 都是 3,保持原信号强度顺序(term00 在前)。
	idx := map[string]int{}
	for i, h := range hits {
		idx[h.Term] = i
	}
	if idx["term00"] >= idx["term02"] {
		t.Errorf("同 hits 应保持原信号强度顺序: term00(%d) 应在 term02(%d) 前", idx["term00"], idx["term02"])
	}
	// 超出 24 的词(term24..term29)不参与。
	for _, h := range hits {
		if h.Term == "term24" || h.Term == "term29" {
			t.Errorf("term24/term29 超出前 24 截断, 不应出现: %v", hits)
		}
	}
	// 空输入 → 空切片(非 nil,wire 序列化 [])。
	empty, err := assembleVulnFingerprintHits(nil, func(string) (int, error) { return 0, nil })
	if err != nil || empty == nil || len(empty) != 0 {
		t.Errorf("空输入应得非 nil 空切片, got %v, err %v", empty, err)
	}
	// 计数器错误上抛。
	if _, err := assembleVulnFingerprintHits([]string{"a"}, func(string) (int, error) {
		return 0, fmt.Errorf("db down")
	}); err == nil {
		t.Errorf("计数器错误应上抛")
	}
}

// TestVulnBriefMatchCounts counts 分组(契约 §10.2):kev/high/rest 三 scope total,
// total=三者之和(三过滤互斥完备);零值/错误传播。
func TestVulnBriefMatchCounts(t *testing.T) {
	totals := map[string]int{"kev": 2, "high": 5, "rest": 8}
	c, err := assembleVulnMatchCounts(func(scope string) (int, error) { return totals[scope], nil })
	if err != nil {
		t.Fatalf("counts 不应报错: %v", err)
	}
	if c.KEV != 2 || c.High != 5 || c.Rest != 8 {
		t.Errorf("分类计数不符: got %+v", c)
	}
	if c.Total != 15 {
		t.Errorf("total 应为三分类之和 15, got %d", c.Total)
	}
	// 全零:total=0(联动关/零命中 wire 全 0)。
	if c, err := assembleVulnMatchCounts(func(string) (int, error) { return 0, nil }); err != nil || c.Total != 0 {
		t.Errorf("全零计数应得 total=0, got %+v, err %v", c, err)
	}
	// 任一 scope 查询失败应上抛。
	if _, err := assembleVulnMatchCounts(func(scope string) (int, error) {
		if scope == "high" {
			return 0, fmt.Errorf("db down")
		}
		return 1, nil
	}); err == nil {
		t.Errorf("scope 计数错误应上抛")
	}
}

// TestVulnBriefEntriesQuery 分页参数解析(契约 §10.2):缺省值/scope 映射/白名单 400/
// desc/limit 夹取/offset 归零。
func TestVulnBriefEntriesQuery(t *testing.T) {
	// 全缺省。
	q, err := parseVulnBriefEntriesQuery(url.Values{})
	if err != nil {
		t.Fatalf("空参数不应报错: %v", err)
	}
	if q.Scope != "" || q.Severity != "" || q.Q != "" || q.Sort != "severity" ||
		!q.Desc || q.Limit != 20 || q.Offset != 0 {
		t.Errorf("缺省值不符: %+v", q)
	}
	// scope:all 映射空(db 层 ""=全部);kev/high/rest 透传。
	for in, want := range map[string]string{"all": "", "kev": "kev", "high": "high", "rest": "rest"} {
		q, err := parseVulnBriefEntriesQuery(url.Values{"scope": {in}})
		if err != nil || q.Scope != want {
			t.Errorf("scope=%q 应得 %q (err %v), got %+v", in, want, err, q)
		}
	}
	// 白名单外 → 400 错误。
	for _, bad := range []struct{ key, val string }{
		{"scope", "everything"}, {"severity", "all"}, {"sort", "hits"},
	} {
		if _, err := parseVulnBriefEntriesQuery(url.Values{bad.key: {bad.val}}); err == nil {
			t.Errorf("%s=%q 白名单外应报错(400)", bad.key, bad.val)
		}
	}
	// 合法 severity/sort 透传;q 去首尾空白。
	q, err = parseVulnBriefEntriesQuery(url.Values{
		"scope": {"rest"}, "severity": {"medium"}, "sort": {"updated"}, "q": {" shiro "},
	})
	if err != nil {
		t.Fatalf("合法参数不应报错: %v", err)
	}
	if q.Scope != "rest" || q.Severity != "medium" || q.Sort != "updated" || q.Q != "shiro" {
		t.Errorf("参数透传不符: %+v", q)
	}
	// desc:仅 "0" 升序;"1"/其他/缺省都按降序。
	for in, want := range map[string]bool{"0": false, "1": true, "true": true, "": true} {
		q, err := parseVulnBriefEntriesQuery(url.Values{"desc": {in}})
		if err != nil || q.Desc != want {
			t.Errorf("desc=%q 应得 %v (err %v), got %+v", in, want, err, q)
		}
	}
	// limit 夹取 1-100,缺省 20;非数字按缺省。
	for in, want := range map[string]int{"": 20, "abc": 20, "0": 1, "-5": 1, "1": 1, "20": 20, "100": 100, "500": 100} {
		q, err := parseVulnBriefEntriesQuery(url.Values{"limit": {in}})
		if err != nil || q.Limit != want {
			t.Errorf("limit=%q 应夹取为 %d (err %v), got %d", in, want, err, q.Limit)
		}
	}
	// offset:负数/非数字归 0,合法值透传。
	for in, want := range map[string]int{"": 0, "x": 0, "-3": 0, "0": 0, "40": 40} {
		q, err := parseVulnBriefEntriesQuery(url.Values{"offset": {in}})
		if err != nil || q.Offset != want {
			t.Errorf("offset=%q 应得 %d (err %v), got %d", in, want, err, q.Offset)
		}
	}
}
