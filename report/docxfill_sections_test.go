package report

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/beevik/etree"
)

// ---- 合成 fixture:标题样式(数字 styleId)/封面/目录条目/两节正文/2 列表 ----

const sectFixtureNS = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`

func sectFixtureStylesXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:styles ` + sectFixtureNS + `>` +
		`<w:style w:type="paragraph" w:default="1" w:styleId="1"><w:name w:val="Normal"/></w:style>` +
		`<w:style w:type="paragraph" w:styleId="31"><w:name w:val="标题 1（测试）"/></w:style>` +
		`<w:style w:type="paragraph" w:styleId="32"><w:name w:val="标题 2（测试）"/></w:style>` +
		`<w:style w:type="paragraph" w:styleId="13"><w:name w:val="toc 1"/></w:style>` +
		`</w:styles>`
}

func sectFixtureDocXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:document ` + sectFixtureNS + `><w:body>` +
		// 封面:宋体大字标题 + 行内标记
		`<w:p><w:r><w:rPr><w:rFonts w:ascii="宋体" w:eastAsia="宋体"/><w:sz w:val="52"/></w:rPr><w:t>{{title}}封面标题</w:t></w:r></w:p>` +
		// 文档信息表(2 列 2 行):与真实模板一致放在首个标题之前的前置区
		// (节替换范围是「标题段之后到下一个标题段/sectPr」,表在正文区会被连带替换)。
		// 标签格 + 值格(值格带颜色,供格式保留断言)。
		`<w:tbl><w:tr>` +
		`<w:tc><w:p><w:r><w:t>项目名称</w:t></w:r></w:p></w:tc>` +
		`<w:tc><w:p><w:r><w:rPr><w:rFonts w:eastAsia="宋体"/><w:color w:val="C00000"/></w:rPr><w:t>旧项目名</w:t></w:r></w:p></w:tc>` +
		`</w:tr><w:tr>` +
		`<w:tc><w:p><w:r><w:t>文档名称</w:t></w:r></w:p></w:tc>` +
		`<w:tc><w:p><w:r><w:t>旧文档名</w:t></w:r></w:p></w:tc>` +
		`</w:tr></w:tbl>` +
		// 目录条目 1:编号+文字+尾随页码数字(无制表符)——TOC 守卫用例
		`<w:p><w:r><w:t>一. 摘要4</w:t></w:r></w:p>` +
		// 目录条目 2:点线制表位 + 制表符 + 页码——TOC 守卫用例
		`<w:p><w:pPr><w:tabs><w:tab w:val="right" w:leader="dot" w:pos="9020"/></w:tabs></w:pPr>` +
		`<w:r><w:t>二. 项目概述</w:t></w:r><w:r><w:tab/></w:r><w:r><w:t>4</w:t></w:r></w:p>` +
		// 章节一:标题(数字 styleId 31)+ 正文(宋体/颜色/字号,供样式继承断言)
		`<w:p><w:pPr><w:pStyle w:val="31"/></w:pPr><w:r><w:t>摘要</w:t></w:r></w:p>` +
		`<w:p><w:pPr><w:pStyle w:val="1"/></w:pPr>` +
		`<w:r><w:rPr><w:rFonts w:ascii="宋体" w:eastAsia="宋体"/><w:color w:val="1F3864"/><w:sz w:val="21"/></w:rPr><w:t>旧摘要正文</w:t></w:r></w:p>` +
		// 章节二
		`<w:p><w:pPr><w:pStyle w:val="31"/></w:pPr><w:r><w:t>项目概述</w:t></w:r></w:p>` +
		`<w:p><w:pPr><w:pStyle w:val="1"/></w:pPr>` +
		`<w:r><w:rPr><w:rFonts w:ascii="宋体" w:eastAsia="宋体"/><w:color w:val="1F3864"/></w:rPr><w:t>旧概述正文</w:t></w:r></w:p>` +
		`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/></w:sectPr>` +
		`</w:body></w:document>`
}

func sectFixtureDocx(t *testing.T) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	parts := []struct{ name, body string }{
		{"[Content_Types].xml", `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/></Types>`},
		{"word/styles.xml", sectFixtureStylesXML()},
		{"word/document.xml", sectFixtureDocXML()},
		{"word/header1.xml", `<?xml version="1.0"?><w:hdr ` + sectFixtureNS + `><w:p><w:r><w:t>页眉原文</w:t></w:r></w:p></w:hdr>`},
	}
	for _, f := range parts {
		w, err := zw.Create(f.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeFixtureFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tpl.docx")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// ---- 输出文档检查辅助 ----

type outDoc struct {
	parts map[string][]byte
	root  *etree.Element // document.xml 根
	body  *etree.Element
}

func parseOutDocx(t *testing.T, data []byte) *outDoc {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("输出不是有效 docx(zip): %v", err)
	}
	od := &outDoc{parts: map[string][]byte{}}
	var docXML []byte
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			t.Fatal(err)
		}
		rc.Close()
		od.parts[f.Name] = buf.Bytes()
		if f.Name == "word/document.xml" {
			docXML = buf.Bytes()
		}
	}
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(docXML); err != nil {
		t.Fatalf("输出 document.xml 解析失败: %v", err)
	}
	od.root = doc.Root()
	od.body = findWChild(od.root, "body")
	if od.body == nil {
		t.Fatal("输出缺少 w:body")
	}
	return od
}

// parasWithText 返回全文含 substr 的段落。
func parasWithText(t *testing.T, root *etree.Element, substr string) []*etree.Element {
	t.Helper()
	var out []*etree.Element
	var walk func(el *etree.Element)
	walk = func(el *etree.Element) {
		if isW(el, "p") && strings.Contains(paraFullText(el), substr) {
			out = append(out, el)
		}
		for _, ch := range el.ChildElements() {
			walk(ch)
		}
	}
	walk(root)
	return out
}

// requireTextCount 断言含 substr 的段落数量。
func requireTextCount(t *testing.T, od *outDoc, substr string, want int, what string) {
	t.Helper()
	if got := len(parasWithText(t, od.root, substr)); got != want {
		t.Fatalf("%s:含 %q 的段落数期望 %d,得到 %d", what, substr, want, got)
	}
}

// firstRunWithText 返回段落内首个文本含 substr 的 run。
func firstRunWithText(p *etree.Element, substr string) *etree.Element {
	var walk func(el *etree.Element) *etree.Element
	walk = func(el *etree.Element) *etree.Element {
		for _, ch := range el.ChildElements() {
			if isW(ch, "r") && strings.Contains(runFullText(ch), substr) {
				return ch
			}
			if r := walk(ch); r != nil {
				return r
			}
		}
		return nil
	}
	return walk(p)
}

// ---- 场景 A:精确/前缀匹配 + TOC 不误匹配 + 样式继承 + TableLabels + 未匹配节追加 ----

func TestFillDocxSectionsFixture(t *testing.T) {
	path := writeFixtureFile(t, sectFixtureDocx(t))
	out, err := FillDocxSections(path, SectionFillOptions{
		Inline:      map[string]string{"title": "新报告标题"},
		TableLabels: map[string]string{"项目名称": "测试项目X"},
		Sections: []TemplateSectionFill{
			{Heading: "摘要", Body: "新摘要正文，含**关键结论**标注。\n\n- 列表项保留前缀\n\n> 引用内容缩进"},
			{Heading: "二. 项目概述", Body: "新概述正文 `code_snippet` 结束。"},
			{Heading: "风险处置建议", Body: "追加的新章节内容。"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	od := parseOutDocx(t, out)

	// 包部件:styles.xml 字节不变,header1.xml 原字节保留。
	if string(od.parts["word/styles.xml"]) != sectFixtureStylesXML() {
		t.Fatal("styles.xml 字节必须保持不变")
	}
	if !strings.Contains(string(od.parts["word/header1.xml"]), "页眉原文") {
		t.Fatal("header1.xml 应原样保留")
	}

	// TOC 不误匹配:两条目录条目原样保留;摘要匹配的是样式标题段(仍在)而非目录条目。
	requireTextCount(t, od, "一. 摘要4", 1, "目录条目 1")
	requireTextCount(t, od, "二. 项目概述", 1, "目录条目 2")
	styledHeading := 0
	for _, ch := range od.body.ChildElements() {
		if isW(ch, "p") && paraStyleID(ch) == "31" && paraFullText(ch) == "摘要" {
			styledHeading++
		}
	}
	if styledHeading != 1 {
		t.Fatalf("「摘要」样式标题段应保留 1 个,得到 %d", styledHeading)
	}

	// 行内标记:{{title}} 已替换,标记不再残留。
	requireTextCount(t, od, "新报告标题", 1, "行内替换")
	requireTextCount(t, od, "{{title}}", 0, "行内标记残留")

	// 旧正文消失、新正文存在。
	requireTextCount(t, od, "旧摘要正文", 0, "旧摘要正文")
	requireTextCount(t, od, "旧概述正文", 0, "旧概述正文")
	requireTextCount(t, od, "新摘要正文", 1, "新摘要正文")
	requireTextCount(t, od, "新概述正文", 1, "新概述正文")

	// 样式继承:新正文 run 保留基底 宋体/颜色/字号;粗体 run 只额外覆盖 w:b。
	sumP := parasWithText(t, od.root, "新摘要正文")[0]
	if ps := paraStyleID(sumP); ps != "1" {
		t.Fatalf("新正文应继承基底段落样式 Normal,得到 pStyle=%q", ps)
	}
	r := firstRunWithText(sumP, "新摘要正文")
	if r == nil {
		t.Fatal("找不到新摘要正文的 run")
	}
	rpr := findWChild(r, "rPr")
	if rpr == nil {
		t.Fatal("新正文 run 缺少 rPr(应继承基底字符样式)")
	}
	rf := findWChild(rpr, "rFonts")
	if rf == nil || styleAttrVal(rf, "eastAsia") != "宋体" {
		t.Fatalf("新正文应继承基底中文字体宋体: %+v", rf)
	}
	if c := findWChild(rpr, "color"); c == nil || styleAttrVal(c, "val") != "1F3864" {
		t.Fatalf("新正文应继承基底颜色 1F3864")
	}
	if sz := findWChild(rpr, "sz"); sz == nil || styleAttrVal(sz, "val") != "21" {
		t.Fatalf("新正文应继承基底字号 21")
	}
	boldR := firstRunWithText(sumP, "关键结论")
	if boldR == nil {
		t.Fatal("找不到粗体片段 run")
	}
	brpr := findWChild(boldR, "rPr")
	if findWChild(brpr, "b") == nil {
		t.Fatal("粗体片段应带 w:b")
	}
	if findWChild(brpr, "rFonts") == nil || styleAttrVal(findWChild(brpr, "rFonts"), "eastAsia") != "宋体" {
		t.Fatal("粗体片段在覆盖 w:b 之外应保留基底字体")
	}

	// 列表:保留文本前缀 + 左缩进;引用:左缩进。
	listP := parasWithText(t, od.root, "- 列表项保留前缀")
	if len(listP) != 1 {
		t.Fatalf("列表段应保留文本前缀且仅 1 个,得到 %d", len(listP))
	}
	if ppr := findWChild(listP[0], "pPr"); ppr == nil {
		t.Fatal("列表段应带段落属性")
	} else if ind := findWChild(ppr, "ind"); ind == nil || styleAttrVal(ind, "left") != "360" {
		t.Fatalf("列表段应有左缩进 360")
	}
	quoteP := parasWithText(t, od.root, "引用内容缩进")
	if len(quoteP) != 1 {
		t.Fatalf("引用段应存在,得到 %d", len(quoteP))
	}
	if ppr := findWChild(quoteP[0], "pPr"); ppr == nil {
		t.Fatal("引用段应带段落属性")
	} else if ind := findWChild(ppr, "ind"); ind == nil || styleAttrVal(ind, "left") != "420" {
		t.Fatalf("引用段应有左缩进 420")
	}

	// 行内码:Consolas + 略小字号。
	codeP := parasWithText(t, od.root, "新概述正文")[0]
	cr := firstRunWithText(codeP, "code_snippet")
	if cr == nil {
		t.Fatal("找不到行内码 run")
	}
	crf := findWChild(findWChild(cr, "rPr"), "rFonts")
	if crf == nil || styleAttrVal(crf, "ascii") != "Consolas" {
		t.Fatalf("行内码应用 Consolas")
	}
	csz := findWChild(findWChild(cr, "rPr"), "sz")
	if csz == nil || styleAttrVal(csz, "val") != "19" { // 21-2,略小字号
		t.Fatalf("行内码字号应略小于基底(19),得到 %+v", csz)
	}

	// TableLabels:项目名称右邻格填值且保留原 run 格式;标签格不动。
	var infoTbl *etree.Element
	for _, ch := range od.body.ChildElements() {
		if isW(ch, "tbl") {
			infoTbl = ch
			break
		}
	}
	if infoTbl == nil {
		t.Fatal("文档信息表丢失")
	}
	requireTextCount(t, od, "项目名称", 1, "表格标签格")
	valueCells := parasWithText(t, od.root, "测试项目X")
	if len(valueCells) != 1 {
		t.Fatalf("项目名称右邻格应填入「测试项目X」,得到 %d 处", len(valueCells))
	}
	vp := valueCells[0]
	vr := firstRunWithText(vp, "测试项目X")
	if vr == nil {
		t.Fatal("找不到填值 run")
	}
	vrf := findWChild(findWChild(vr, "rPr"), "rFonts")
	if vrf == nil || styleAttrVal(vrf, "eastAsia") != "宋体" {
		t.Fatal("填值应保留右邻格原有 run 格式(宋体)")
	}
	if c := findWChild(findWChild(vr, "rPr"), "color"); c == nil || styleAttrVal(c, "val") != "C00000" {
		t.Fatal("填值应保留右邻格原有颜色 C00000")
	}
	requireTextCount(t, od, "旧项目名", 0, "右邻格旧文本应被清掉")

	// 未匹配节:按序追加到 sectPr 之前(信息表在前置区,天然在其后)。
	appendP := parasWithText(t, od.root, "追加的新章节内容")
	if len(appendP) != 1 {
		t.Fatalf("未匹配节应追加文末,得到 %d 处", len(appendP))
	}
	tblIdx, appIdx, sectIdx := -1, -1, -1
	for i, ch := range od.body.ChildElements() {
		switch {
		case isW(ch, "tbl") && tblIdx < 0:
			tblIdx = i
		case ch == appendP[0]:
			appIdx = i
		case isW(ch, "sectPr"):
			sectIdx = i
		}
	}
	if !(tblIdx < appIdx && appIdx < sectIdx) {
		t.Fatalf("未匹配节应追加在文档信息表之后、sectPr 之前: tbl=%d append=%d sect=%d", tblIdx, appIdx, sectIdx)
	}
}

// 场景 B:输入标题与外壳文本完全不重合 → ①②命中率 <50% → 顺序对齐兜底。
// TOC 条目因守卫不入标题序列,兜底必须对齐到真实标题段。
func TestFillDocxSectionsSequentialFallback(t *testing.T) {
	path := writeFixtureFile(t, sectFixtureDocx(t))
	out, err := FillDocxSections(path, SectionFillOptions{
		Sections: []TemplateSectionFill{
			{Heading: "甲章标题", Body: "兜底新正文一。"},
			{Heading: "乙章标题", Body: "兜底新正文二。"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	od := parseOutDocx(t, out)
	requireTextCount(t, od, "旧摘要正文", 0, "兜底应替换第 1 节")
	requireTextCount(t, od, "旧概述正文", 0, "兜底应替换第 2 节")
	requireTextCount(t, od, "兜底新正文一", 1, "兜底第 1 节新正文")
	requireTextCount(t, od, "兜底新正文二", 1, "兜底第 2 节新正文")
	// TOC 条目不是兜底目标,保持原样。
	requireTextCount(t, od, "一. 摘要4", 1, "目录条目 1 不受兜底影响")
	requireTextCount(t, od, "二. 项目概述", 1, "目录条目 2 不受兜底影响")
	// 样式标题段保留。
	requireTextCount(t, od, "摘要", 2, "「摘要」标题段+目录条目")
}

// ---- 真实模板集成:team_template.docx 按 spec §4 D4 断言清单逐条落实 ----

func TestFillDocxSectionsTeamTemplateIntegration(t *testing.T) {
	const tplPath = "testdata/team_template.docx"
	orig, err := os.ReadFile(tplPath)
	if err != nil {
		t.Fatal(err)
	}
	out, err := FillDocxSections(tplPath, SectionFillOptions{
		Inline:      map[string]string{"title": "测试项目X渗透测试报告", "date": "2026-09-25"},
		TableLabels: map[string]string{"项目名称": "测试项目X"},
		Sections: []TemplateSectionFill{
			{Heading: "摘要", Body: "集成测试新摘要正文：本次评估整体风险可控，共记录演示问题 1 项。"},
			{Heading: "项目概述", Body: "集成测试新项目概述：覆盖目标系统的 Web 安全与弱口令风险测试。"},
			{Heading: "问题归纳", Body: "集成测试新问题归纳：演示漏洞已重写，旧样例章节整体替换。"},
			{Heading: "安全风险总结", Body: "集成测试新安全风险总结：系统当前状态为可控。"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	od := parseOutDocx(t, out)

	// ① 输出可 unzip(上方 parseOutDocx 已验证),且包部件清单与原包一致。
	origZr, err := zip.NewReader(bytes.NewReader(orig), int64(len(orig)))
	if err != nil {
		t.Fatal(err)
	}
	if len(origZr.File) != len(od.parts) {
		t.Fatalf("包部件数量应一致: 原 %d,新 %d", len(origZr.File), len(od.parts))
	}
	// ② styles.xml 与原包字节一致;③ header*/footer*/media 部件仍在且字节未动。
	for _, f := range origZr.File {
		newBody, ok := od.parts[f.Name]
		if !ok {
			t.Fatalf("包部件丢失: %s", f.Name)
		}
		if f.Name == "word/document.xml" {
			continue
		}
		old, err := readZipFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if string(old) != string(newBody) {
			t.Fatalf("非 document.xml 部件被改动: %s", f.Name)
		}
	}

	// ④ 旧样例正文消失(样式区+遗留平铺副本一并清除)。
	outMD, _, err := ParseDocxReader(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("输出应可被 ParseDocxReader 解析: %v", err)
	}
	for _, gone := range []string{
		"Nacos未授权访问",
		"未授权用户可以使用默认凭证登录",
		"问题数据包",
		"高危xx个，中危xx个，低危xx个",
	} {
		if strings.Contains(outMD, gone) {
			t.Fatalf("旧样例正文应消失,仍含 %q", gone)
		}
	}
	// ⑤ 新正文文本在。
	for _, want := range []string{
		"集成测试新摘要正文",
		"集成测试新项目概述",
		"集成测试新问题归纳",
		"集成测试新安全风险总结",
	} {
		if !strings.Contains(outMD, want) {
			t.Fatalf("新正文缺失 %q", want)
		}
	}
	// ⑥ 「项目概述」标题段保留且 pStyle=31 不变。
	found := false
	for _, ch := range od.body.ChildElements() {
		if isW(ch, "p") && paraStyleID(ch) == "31" && paraFullText(ch) == "项目概述" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("「项目概述」标题段应保留且 pStyle=31 不变")
	}
	// ⑦ 目录条目未被误改/误匹配。
	if !strings.Contains(outMD, "一. 摘要") || !strings.Contains(outMD, "三. 项目概述") {
		t.Fatal("目录条目应原样保留")
	}
	// ⑧ 文档信息表「项目名称」右邻格 = 测试项目X。
	if !strings.Contains(outMD, "测试项目X") {
		t.Fatal("文档信息表应填入「测试项目X」")
	}
	labelHit := false
	var walkTbl func(el *etree.Element)
	walkTbl = func(el *etree.Element) {
		for _, ch := range el.ChildElements() {
			if isW(ch, "tbl") {
				for _, tr := range ch.ChildElements() {
					if !isW(tr, "tr") {
						continue
					}
					var tcs []*etree.Element
					for _, tc := range tr.ChildElements() {
						if isW(tc, "tc") {
							tcs = append(tcs, tc)
						}
					}
					for i, tc := range tcs {
						if cellFullText(tc) == "项目名称" && i+1 < len(tcs) && cellFullText(tcs[i+1]) == "测试项目X" {
							labelHit = true
						}
					}
				}
			}
			walkTbl(ch)
		}
	}
	walkTbl(od.root)
	if !labelHit {
		t.Fatal("文档信息表中「项目名称」右邻格应为「测试项目X」")
	}
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(rc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
