package server

// 文章采集的纯函数单测(契约 §6.3):CVE 正则抽取 / HTML 清洗 / RSS XML 解析。
// 全部内联 fixture,不连库、不出网。

import (
	"encoding/json"
	"testing"
)

func TestCVEExtrArticleExtract(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"中英混排", "修复了 WordPress 插件 CVE-2024-1234 的任意文件上传漏洞", []string{"CVE-2024-1234"}},
		{"中文紧邻编号", "漏洞编号CVE-2022-0847已公开利用,影响所有内核版本", []string{"CVE-2022-0847"}},
		{"多编号保序", "同时涉及 CVE-2024-1111、CVE-2023-9999 与 CVE-2024-1234567", []string{"CVE-2024-1111", "CVE-2023-9999", "CVE-2024-1234567"}},
		{"大小写归一并去重", "patched CVE-2024-1234 and cve-2024-1234 today", []string{"CVE-2024-1234"}},
		{"括号/标点边界", "(CVE-2021-34567),另见[CVE-2020-1472]。", []string{"CVE-2021-34567", "CVE-2020-1472"}},
		{"截断误报:无序列号", "文中提到 CVE-2024- 但没给全编号", nil},
		{"截断误报:序列号超长", "CVE-2024-12345678 是编造的编号", nil},
		{"前缀粘连", "参见条目 xCVE-2024-1234 的说明", nil},
		{"连字符前缀粘连", "内部代号 -CVE-2024-1234 不算编号", nil},
		{"序列号过短", "CVE-2023-99 只有两位序列号,不合法", nil},
		{"空文本", "", nil},
	}
	for _, c := range cases {
		got := articleExtractCVEs(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("%s: articleExtractCVEs(%q) = %v, want %v", c.name, c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%s: articleExtractCVEs(%q)[%d] = %q, want %q", c.name, c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestCVEExtrArticleCNVD(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"中英混排", "国家漏洞库收录 CNVD-2026-123456,涉及某国产 OA 系统", []string{"CNVD-2026-123456"}},
		{"中文紧邻编号", "漏洞编号CNVD-2025-99999已公开,请及时修复", []string{"CNVD-2025-99999"}},
		{"多编号去重保序", "对应 CNVD-2026-1234 与 cnvd-2026-1234 及 CNVD-2024-7777777", []string{"CNVD-2026-1234", "CNVD-2024-7777777"}},
		{"截断误报:序列号超长", "CNVD-2026-12345678 不是合法编号", nil},
		{"前缀粘连", "xCNVD-2026-1234 不算", nil},
		{"7 位序列号合法", "CNVD-2023-1234567 可收", []string{"CNVD-2023-1234567"}},
		{"8 位序列号不合法", "CNVD-2023-12345678 不收", nil},
		{"空文本", "", nil},
	}
	for _, c := range cases {
		got := articleExtractCNVDs(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("%s: articleExtractCNVDs(%q) = %v, want %v", c.name, c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%s: articleExtractCNVDs(%q)[%d] = %q, want %q", c.name, c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestValidCNVDID(t *testing.T) {
	yes := []string{"CNVD-2026-1234", "CNVD-2026-1234567", "CNVD-1999-0001"}
	no := []string{"", "CNVD-2026", "CNVD-2026-123", "CNVD-20261-1234", "CNVD-2026-12345678", "CNVD-2026-12a4", "CVE-2026-1234"}
	for _, id := range yes {
		if !validCNVDID(id) {
			t.Fatalf("validCNVDID(%q) = false, want true", id)
		}
	}
	for _, id := range no {
		if validCNVDID(id) {
			t.Fatalf("validCNVDID(%q) = true, want false", id)
		}
	}
}

func TestArticleStripHTML(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"去标签", "<p>Hello <b>world</b></p>", "Hello world"},
		{"实体反转义", "A &amp; B &lt;tag&gt; &#39;quoted&#39;", `A & B <tag> 'quoted'`},
		{"标签替换为空格防粘连", "<p>remote</p><p>code</p><p>execution</p>", "remote code execution"},
		{"折叠空白", "a \n\t b   c", "a b c"},
		{"带属性的标签", `<a href="https://example.com/x?a=1&amp;b=2">链接</a>正文`, "链接 正文"},
		{"空串", "", ""},
		{"纯文本原样", "没有任何标记的普通文本", "没有任何标记的普通文本"},
	}
	for _, c := range cases {
		if got := articleStripHTML(c.in); got != c.want {
			t.Fatalf("%s: articleStripHTML(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// articleRSSFixture 模拟 FreeBuf 风格的 RSS 2.0(含 CDATA 标题与 HTML 摘要)。
const articleRSSFixture = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:content="http://purl.org/rss/1.0/modules/content/">
<channel>
<title><![CDATA[FreeBuf网络安全行业门户]]></title>
<link>https://www.freebuf.com</link>
<description>关注黑客与安全</description>
<item>
<title><![CDATA[XXL-JOB CVE-2024-双层绕过分析]]></title>
<link>https://www.freebuf.com/news/1.html</link>
<description><![CDATA[<p>分析 <b>CVE-2024-5678</b> 与 cve-2024-5678 的鉴权绕过。</p><a href="#">阅读全文</a>]]></description>
</item>
<item>
<title>纯观点文章&amp;行业观察</title>
<link>https://www.freebuf.com/news/2.html</link>
<description>不含任何编号的普通文章。</description>
</item>
<item>
<title>编号被截断的文章</title>
<link>https://www.freebuf.com/news/3.html</link>
<description>提到了 CVE-2021- 但没有写全。</description>
</item>
</channel>
</rss>`

// articleRDFFixture 模拟先知风格的 RSS 1.0(RDF:item 在 channel 外层)。
const articleRDFFixture = `<?xml version="1.0" encoding="utf-8"?>
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns="http://purl.org/rss/1.0/">
<channel rdf:about="https://xz.aliyun.com/feed">
<title>先知社区</title>
<link>https://xz.aliyun.com</link>
<description>先知安全技术社区</description>
</channel>
<item rdf:about="https://xz.aliyun.com/news/9">
<title>某框架反序列化 CVE-2025-24680 漏洞分析</title>
<link>https://xz.aliyun.com/news/9</link>
<description>完整利用链覆盖 CVE-2025-24680。</description>
</item>
</rdf:RDF>`

func TestArticleParseRSS(t *testing.T) {
	items, err := articleParseRSS(articleRSSFixture)
	if err != nil {
		t.Fatalf("解析 RSS 2.0 失败: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("RSS 2.0 应解析出 3 条,得到 %d 条: %+v", len(items), items)
	}
	// CDATA 标题原样取出(CDATA 内的文本不做实体解码)。
	if items[0].Title != "XXL-JOB CVE-2024-双层绕过分析" {
		t.Fatalf("CDATA 标题解析错误: %q", items[0].Title)
	}
	if items[0].Link != "https://www.freebuf.com/news/1.html" {
		t.Fatalf("item link 解析错误: %q", items[0].Link)
	}
	// 摘要里的编号应能抽出且去重(大小写归一);截断编号不误报。
	cves := articleExtractCVEs(items[0].Title + " " + items[0].Description)
	if len(cves) != 1 || cves[0] != "CVE-2024-5678" {
		t.Fatalf("摘要编号提取错误: %v", cves)
	}
	// XML 层的 &amp; 实体应被解码为 &。
	if items[1].Title != "纯观点文章&行业观察" {
		t.Fatalf("XML 实体未解码: %q", items[1].Title)
	}
	if got := articleExtractCVEs(items[2].Title + " " + items[2].Description); len(got) != 0 {
		t.Fatalf("截断编号不应命中: %v", got)
	}

	// RSS 1.0(RDF):item 在 channel 外层也要能取到。
	rdfItems, err := articleParseRSS(articleRDFFixture)
	if err != nil {
		t.Fatalf("解析 RDF 失败: %v", err)
	}
	if len(rdfItems) != 1 || rdfItems[0].Link != "https://xz.aliyun.com/news/9" {
		t.Fatalf("RDF 条目解析错误: %+v", rdfItems)
	}

	// 非 XML 输入应报错而不是静默返回空。
	if _, err := articleParseRSS("这不是 XML"); err == nil {
		t.Fatal("非法 XML 应返回错误")
	}
}

func TestArticleHasRef(t *testing.T) {
	raw := json.RawMessage(`["https://www.freebuf.com/news/1.html","https://xz.aliyun.com/news/9"]`)
	if !articleHasRef(raw, "https://www.freebuf.com/news/1.html") {
		t.Fatal("已有链接应判定为存在")
	}
	if articleHasRef(raw, "https://www.freebuf.com/news/999.html") {
		t.Fatal("不存在的链接不应判定为存在")
	}
	if articleHasRef(json.RawMessage(`not-json`), "x") {
		t.Fatal("非法 JSON 应按「没有」处理")
	}
	if articleHasRef(nil, "x") || articleHasRef(json.RawMessage(`[]`), "x") {
		t.Fatal("空 refs 应按「没有」处理")
	}
}
