package report

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

// docxDocumentXML 渲染 markdown 并解出 word/document.xml,供断言。
func docxDocumentXML(t *testing.T, md string) string {
	t.Helper()
	b, err := DocxFromMarkdown(md)
	if err != nil {
		t.Fatalf("DocxFromMarkdown: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("打开 docx: %v", err)
	}
	for _, f := range zr.File {
		if f.Name != "word/document.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("打开 document.xml: %v", err)
		}
		defer rc.Close()
		buf, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("读取 document.xml: %v", err)
		}
		return string(buf)
	}
	t.Fatal("docx 缺少 word/document.xml")
	return ""
}

// TestDocxBoldRunSplit 粗体拆 run:加 <w:b/>,且星号不残留、正文不丢。
func TestDocxBoldRunSplit(t *testing.T) {
	xml := docxDocumentXML(t, "前缀文字 **重点结论** 后缀文字\n")
	if !strings.Contains(xml, "<w:b/>") {
		t.Fatalf("粗体 run 未加 <w:b/>:\n%s", xml)
	}
	if !strings.Contains(xml, `>重点结论<`) {
		t.Fatalf("粗体文本丢失:\n%s", xml)
	}
	for _, seg := range []string{"前缀文字", "后缀文字"} {
		if !strings.Contains(xml, seg) {
			t.Fatalf("普通文本 %q 丢失:\n%s", seg, xml)
		}
	}
	if strings.Contains(xml, "**") {
		t.Fatalf("星号残留:\n%s", xml)
	}
}

// TestDocxBoldUnmatchedNoLeftover 未配对/夹杂的星号也要清干净,不能把 ** 当正文。
func TestDocxBoldUnmatchedNoLeftover(t *testing.T) {
	xml := docxDocumentXML(t, "开头 **未闭合加粗 与 *孤立星号 结尾\n")
	if strings.ContainsRune(markdownVisibleText(xml), '*') {
		t.Fatalf("可见文本残留星号:\n%s", xml)
	}
	if !strings.Contains(xml, "未闭合加粗") {
		t.Fatalf("正文丢失:\n%s", xml)
	}
}

// markdownVisibleText 抽出 document.xml 里全部 <w:t> 文本(断言用)。
func markdownVisibleText(docXML string) string {
	var b strings.Builder
	for {
		start := strings.Index(docXML, "<w:t")
		if start < 0 {
			b.WriteString(docXML)
			return b.String()
		}
		docXML = docXML[start:]
		open := strings.Index(docXML, ">")
		close := strings.Index(docXML, "</w:t>")
		if open < 0 || close < 0 || close < open {
			return b.String()
		}
		b.WriteString(docXML[open+1 : close])
		docXML = docXML[close+len("</w:t>"):]
	}
}

// TestDocxInlineCodeRun 行内代码:等宽字体 run,反引号不残留。
func TestDocxInlineCodeRun(t *testing.T) {
	xml := docxDocumentXML(t, "执行 `net user` 后查看结果\n")
	if !strings.Contains(xml, "Consolas") {
		t.Fatalf("行内代码未用等宽字体:\n%s", xml)
	}
	if !strings.Contains(xml, ">net user<") {
		t.Fatalf("行内代码内容丢失:\n%s", xml)
	}
	if strings.Contains(xml, "`") {
		t.Fatalf("反引号残留:\n%s", xml)
	}
	if !strings.Contains(xml, "后查看结果") {
		t.Fatalf("普通文本丢失:\n%s", xml)
	}
}

// TestDocxQuoteBlockIndented 引用块:左缩进段落且文字保留。
func TestDocxQuoteBlockIndented(t *testing.T) {
	xml := docxDocumentXML(t, "> 这是一条引用说明\n")
	if !strings.Contains(xml, `<w:ind w:left="400"/>`) {
		t.Fatalf("引用块未缩进:\n%s", xml)
	}
	if !strings.Contains(xml, "这是一条引用说明") {
		t.Fatalf("引用文字丢失:\n%s", xml)
	}
}

// TestDocxOrderedListKept 有序列表:编号前缀保留、行不丢。
func TestDocxOrderedListKept(t *testing.T) {
	xml := docxDocumentXML(t, "1. 先收集信息\n2. 再验证漏洞\n")
	for _, want := range []string{"1. 先收集信息", "2. 再验证漏洞"} {
		if !strings.Contains(xml, want) {
			t.Fatalf("有序列表行 %q 丢失:\n%s", want, xml)
		}
	}
	if !strings.Contains(xml, `<w:ind w:left="360"/>`) {
		t.Fatalf("有序列表未按列表段落缩进:\n%s", xml)
	}
}

// TestDocxMixedStructureBottomLine 混合结构底线:每行正文都在、无 markdown 残留。
func TestDocxMixedStructureBottomLine(t *testing.T) {
	md := "# 渗透测试报告\n" +
		"\n" +
		"## 漏洞详情\n" +
		"\n" +
		"该接口存在 **SQL 注入** 风险,参数为 `id`。\n" +
		"\n" +
		"> 复现前请先备份。\n" +
		"\n" +
		"1. 打开登录页\n" +
		"2. 提交 payload\n" +
		"\n" +
		"| 等级 | 名称 |\n" +
		"| --- | --- |\n" +
		"| 高 | **注入** |\n"
	xml := docxDocumentXML(t, md)
	text := markdownVisibleText(xml)
	for _, want := range []string{
		"渗透测试报告", "漏洞详情",
		"该接口存在", "SQL 注入", "风险,参数为", "id",
		"复现前请先备份。",
		"1. 打开登录页", "2. 提交 payload",
		"等级", "名称", "高", "注入",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("正文缺失 %q,可见文本=%q", want, text)
		}
	}
	if strings.Contains(text, "*") || strings.Contains(text, "`") || strings.Contains(text, "> ") {
		t.Fatalf("markdown 记号残留,可见文本=%q", text)
	}
}
