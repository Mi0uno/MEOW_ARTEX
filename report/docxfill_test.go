package report

import (
	"bytes"
	"strings"
	"testing"
)

// 用生成器造一个含变量标记的"原始 docx",再走填充管线。
func TestFillDocxInlineVars(t *testing.T) {
	md := "# {{title}}\n\n日期:{{date}}\n\n{{summary}}\n"
	orig, err := DocxFromMarkdown(md)
	if err != nil {
		t.Fatal(err)
	}
	filled, err := FillDocx(orig, DocxData{
		Inline: map[string]string{
			"title":   "演示项目渗透测试报告",
			"date":    "2026-09-23",
			"summary": "覆盖 example.com",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, st, err := ParseDocxReader(bytes.NewReader(filled))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"演示项目渗透测试报告", "2026-09-23", "覆盖 example.com"} {
		if !strings.Contains(got, want) {
			t.Fatalf("填充结果缺少 %q:\n%s", want, got)
		}
	}
	// 未提供的变量原样保留
	if !strings.Contains(got, "{{summary}}") == false && strings.Contains(got, "{{missing_var}}") {
		t.Fatal("未提供变量应保留原样")
	}
	if st.Paragraphs == 0 {
		t.Fatal("段落不应为空")
	}
}

// 变量被 Word 拆散到多个 run(真实 Word 的常见形态)也应正确替换。
func TestFillDocxXMLSplitRuns(t *testing.T) {
	const ns = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`
	xml := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:document ` + ns + `><w:body>` +
		`<w:p><w:r><w:t>报告标题:</w:t></w:r><w:r><w:t>{{ti</w:t></w:r><w:r><w:t>tle}}</w:t></w:r></w:p>` +
		`<w:p><w:r><w:t>正文段落,不涉及变量。</w:t></w:r></w:p>` +
		`</w:body></w:document>`
	filled, err := FillDocxXML([]byte(xml), DocxData{
		Inline: map[string]string{"title": "演示报告"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(filled)
	if !strings.Contains(got, "演示报告") {
		t.Fatalf("跨 run 变量替换失败:\n%s", got)
	}
	if strings.Contains(got, "{{ti") {
		t.Fatalf("残留未替换标记:\n%s", got)
	}
}

// 块标记(独占段落)展开为一组段落。
func TestFillDocxXMLBlockVar(t *testing.T) {
	const ns = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`
	xml := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:document ` + ns + `><w:body>` +
		`<w:p><w:r><w:t>漏洞列表</w:t></w:r></w:p>` +
		`<w:p><w:r><w:t>{{findings_table}}</w:t></w:r></w:p>` +
		`<w:p><w:r><w:t>{{content}}</w:t></w:r></w:p>` +
		`</w:body></w:document>`
	filled, err := FillDocxXML([]byte(xml), DocxData{
		Blocks: map[string]DocxBlock{
			"findings_table": {Table: [][]string{
				{"严重等级", "名称", "状态"},
				{"HIGH", "演示漏洞", "confirmed"},
			}},
			"content": {Lines: []string{"# 一、概述", "测试覆盖 example.com。", "## 1.1 范围", "仅主站。"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(filled)
	for _, want := range []string{"<w:tbl>", "演示漏洞", "一、概述", "测试覆盖 example.com。"} {
		if !strings.Contains(got, want) {
			t.Fatalf("块展开缺少 %q:\n%s", want, got)
		}
	}
}

func TestFillDocxRejectsNonZip(t *testing.T) {
	if _, err := FillDocx([]byte("not zip"), DocxData{}); err == nil {
		t.Fatal("非 zip 输入应报错")
	}
}

// 任意块标记键:外壳里的自定义标记(如 {{findings_table}})以 md 表格文本作为
// Lines 传入时,应解析成 Word 表格展开,不再以字面量或表格文本行残留。
func TestFillDocxXMLArbitraryBlockKeyAsMdTable(t *testing.T) {
	const ns = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`
	xml := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:document ` + ns + `><w:body>` +
		`<w:p><w:r><w:t>{{findings_table}}</w:t></w:r></w:p>` +
		`</w:body></w:document>`
	filled, err := FillDocxXML([]byte(xml), DocxData{
		Blocks: map[string]DocxBlock{
			"findings_table": {Lines: []string{
				"| 严重等级 | 名称 | 类别 | 状态 |",
				"|---|---|---|---|",
				"| HIGH | 演示漏洞 | SQL 注入 | confirmed |",
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(filled)
	if strings.Contains(got, "{{findings_table}}") {
		t.Fatalf("块标记不应残留:\n%s", got)
	}
	if strings.Count(got, "<w:tbl>") != 1 {
		t.Fatalf("md 表格文本应解析成一张 Word 表格:\n%s", got)
	}
	for _, want := range []string{"严重等级", "演示漏洞", "SQL 注入", "confirmed"} {
		if !strings.Contains(got, want) {
			t.Fatalf("表格缺少单元格 %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "<w:b/>") {
		t.Fatalf("表头行应加粗:\n%s", got)
	}
}

// 任意块标记键(非内置变量名):段落文本剥 {{}} 后命中 Blocks 即展开为段落。
func TestFillDocxXMLCustomBlockKeyParagraphs(t *testing.T) {
	const ns = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`
	xml := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:document ` + ns + `><w:body>` +
		`<w:p><w:r><w:t>{{my_custom_block}}</w:t></w:r></w:p>` +
		`</w:body></w:document>`
	filled, err := FillDocxXML([]byte(xml), DocxData{
		Blocks: map[string]DocxBlock{
			"my_custom_block": {Lines: []string{"# 测试结论", "整体风险可控。", "## 遗留问题", "无。"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(filled)
	if strings.Contains(got, "{{my_custom_block}}") {
		t.Fatalf("自定义块标记不应残留:\n%s", got)
	}
	for _, want := range []string{"测试结论", "整体风险可控。", "遗留问题"} {
		if !strings.Contains(got, want) {
			t.Fatalf("段落展开缺少 %q:\n%s", want, got)
		}
	}
}

// DocxBlockFromMarkdown:纯 md 表格 → Table(分隔行跳过、\| 转义还原);
// 含非表格行 → 整块按行段落展开,不丢行。
func TestDocxBlockFromMarkdown(t *testing.T) {
	blk := DocxBlockFromMarkdown("| 严重等级 | 名称 |\n|---|---|\n| HIGH | 带竖线\\|的名称 |\n")
	if blk.Table == nil {
		t.Fatalf("纯表格文本应解析为 Table: %+v", blk)
	}
	if len(blk.Table) != 2 {
		t.Fatalf("分隔行应被跳过,期望 2 行数据,得到 %d: %+v", len(blk.Table), blk.Table)
	}
	if blk.Table[0][0] != "严重等级" || blk.Table[1][1] != "带竖线|的名称" {
		t.Fatalf("单元格解析错误: %+v", blk.Table)
	}

	mixed := DocxBlockFromMarkdown("# 概述\n\n覆盖 example.com。\n")
	if mixed.Table != nil {
		t.Fatalf("含非表格行不应解析为 Table: %+v", mixed)
	}
	// 按行原样保留(含空行),不丢行、不改写内容。
	if len(mixed.Lines) != 4 || mixed.Lines[0] != "# 概述" || mixed.Lines[2] != "覆盖 example.com。" {
		t.Fatalf("非表格文本应按行保留,得到: %#v", mixed.Lines)
	}

	// findings_table 无漏洞时的降级文本不是表格,按段落走(尾部换行产生一个空行,
	// 段落展开时空行无副作用)。
	none := DocxBlockFromMarkdown("_无确认漏洞。_\n")
	if none.Table != nil || none.Lines[0] != "_无确认漏洞。_" {
		t.Fatalf("非表格降级文本应按段落展开: %+v", none)
	}
}
