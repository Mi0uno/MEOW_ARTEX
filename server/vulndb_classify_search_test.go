package server

import (
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestBuildClassifyQueries(t *testing.T) {
	f := &db.DBFinding{
		Name:     "用友 NC BeanShell 前台远程代码执行漏洞",
		VulnClass: "RCE",
		Summary:  "影响版本 NC 6.5",
		Evidence: "product: NC 6.5",
	}
	qs := buildClassifyQueries(f)
	if len(qs) == 0 {
		t.Fatal("期望产出查询词")
	}
	// 标题去「漏洞」后缀,更接近组件名。
	if qs[0] != "用友 NC BeanShell 前台远程代码执行" {
		t.Errorf("首查询词应去掉「漏洞」后缀,得到 %q", qs[0])
	}
	// 版本号拼进第二个查询词,提高 NVD/GitHub 精确率。
	want := "用友 NC BeanShell 前台远程代码执行 6.5"
	if len(qs) < 2 || qs[1] != want {
		t.Errorf("第二个查询词应为 %q,得到 %v", want, qs)
	}
	// 去噪后的标题 != 原标题时,原标题作为兜底查询词保留。
	if qs[len(qs)-1] != f.Name {
		t.Errorf("原标题应保留为兜底查询词,得到 %v", qs)
	}
	// 无重复。
	seen := map[string]bool{}
	for _, q := range qs {
		if seen[q] {
			t.Errorf("查询词重复: %q", q)
		}
		seen[q] = true
	}
}

func TestBuildClassifyQueriesEmptyName(t *testing.T) {
	f := &db.DBFinding{VulnClass: "SQL注入"}
	qs := buildClassifyQueries(f)
	if len(qs) == 0 || qs[0] != "SQL注入" {
		t.Errorf("名称为空时回退 VulnClass,得到 %v", qs)
	}
}

func TestParseDDGLite(t *testing.T) {
	page := `<html><body><table>
<tr><td>1.&nbsp;</td><td><a rel="nofollow" href="https://www.example.com/advisory" class='result-link'>Vendor Advisory: Acme CMS RCE</a></td></tr>
<tr><td colspan='2'><td class='result-snippet'>Acme CMS 3.2.1 前台远程代码执行漏洞通告</td></td></tr>
<tr><td>2.&nbsp;</td><td><a rel="nofollow" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgithub.com%2Fevil%2Facme-poc&amp;rut=abc" class='result-link'>GitHub - evil/acme-poc</a></td></tr>
<tr><td colspan='2'><td class='result-snippet'>PoC for Acme CMS &lt;b&gt;RCE&lt;/b&gt; &amp; more</td></td></tr>
</table></body></html>`
	evs := parseDDGLite(page)
	if len(evs) != 2 {
		t.Fatalf("期望解析出 2 条证据,得到 %d: %+v", len(evs), evs)
	}
	if evs[0].URL != "https://www.example.com/advisory" {
		t.Errorf("第一条 URL 错误: %q", evs[0].URL)
	}
	if evs[0].Title != "Vendor Advisory: Acme CMS RCE" {
		t.Errorf("第一条标题错误: %q", evs[0].Title)
	}
	// uddg 跳转链接要还原成真实 URL(&amp; 边界不吞)。
	if evs[1].URL != "https://github.com/evil/acme-poc" {
		t.Errorf("uddg 跳转未还原: %q", evs[1].URL)
	}
	// 摘要实体反转义 + 标签剥除。
	if evs[1].Snippet != "PoC for Acme CMS RCE & more" {
		t.Errorf("摘要清洗错误: %q", evs[1].Snippet)
	}
}

func TestParseDDGLiteEmpty(t *testing.T) {
	// 人机校验页/空白页:静默返回空,不打断判定链路。
	if evs := parseDDGLite("<html><body>anomaly detected</body></html>"); len(evs) != 0 {
		t.Errorf("空页面应返回空,得到 %+v", evs)
	}
}

func TestCPEVendorProduct(t *testing.T) {
	cases := []struct {
		cpe                          string
		wantV, wantP, wantA          string
	}{
		{"cpe:2.3:a:yonyou:nc:6.5:*:*:*:*:*:*:*", "yonyou", "nc", "6.5"},
		{"cpe:2.3:a:apache:log4j:*:*:*:*:*:*:*:*", "apache", "log4j", ""},
		{"cpe:2.3:h:acme:dev:1.0", "acme", "dev", "1.0"},
		{"cpe:2.3:a:x", "", "", ""},
		{"not-cpe:2.3:a:acme:cms:1", "", "", ""},
	}
	for _, c := range cases {
		v, p, a := cpeVendorProduct(c.cpe)
		if v != c.wantV || p != c.wantP || a != c.wantA {
			t.Errorf("cpeVendorProduct(%q) = (%q,%q,%q), 期望 (%q,%q,%q)",
				c.cpe, v, p, a, c.wantV, c.wantP, c.wantA)
		}
	}
}

func TestParseNVDEvidence(t *testing.T) {
	raw := []byte(`{"vulnerabilities":[{"cve":{"id":"CVE-2026-1234",
		"descriptions":[{"lang":"en","value":"Acme CMS 3.2.1 allows unauthenticated RCE"}],
		"configurations":[{"nodes":[{"cpeMatch":[{"criteria":"cpe:2.3:a:acme:cms:3.2.1:*:*:*:*:*:*:*"}]}]}]}}]}`)
	evs := parseNVDEvidence(raw)
	if len(evs) != 1 {
		t.Fatalf("期望 1 条证据,得到 %d", len(evs))
	}
	ev := evs[0]
	if ev.Source != "NVD" || ev.Title != "CVE-2026-1234" {
		t.Errorf("来源/标题错误: %+v", ev)
	}
	if ev.URL != "https://nvd.nist.gov/vuln/detail/CVE-2026-1234" {
		t.Errorf("URL 错误: %q", ev.URL)
	}
	// CPE 参照要进摘要,给 LLM 提取厂商用官方写法。
	if !strings.Contains(ev.Snippet, "cpe:2.3:a:acme:cms") {
		t.Errorf("摘要应含 CPE: %q", ev.Snippet)
	}
	if !strings.Contains(ev.Snippet, "unauthenticated RCE") {
		t.Errorf("摘要应含英文描述: %q", ev.Snippet)
	}
}

func TestMergeClassifyEvidence(t *testing.T) {
	seen := map[string]bool{}
	out := mergeClassifyEvidence(nil, seen, []classifyEvidence{
		{Source: "NVD", Title: "a", URL: "https://a"},
		{Source: "GitHub", Title: "b", URL: "https://b"},
	})
	out = mergeClassifyEvidence(out, seen, []classifyEvidence{
		{Source: "NVD", Title: "a-again", URL: "https://a"}, // URL 重复
		{Source: "网页搜索", Title: "c", URL: ""},            // 无 URL 按标题去重
		{Source: "网页搜索", Title: "c", URL: ""},
	})
	if len(out) != 3 {
		t.Fatalf("期望去重后 3 条,得到 %d: %+v", len(out), out)
	}
}
