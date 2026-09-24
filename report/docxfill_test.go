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
