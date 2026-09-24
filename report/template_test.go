package report

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

func TestRenderTemplate(t *testing.T) {
	at, _ := time.Parse(time.DateOnly, "2026-09-23")
	tpl := "# {{title}}\n\n日期:{{date}}\n\n{{summary}}\n\n{{findings_table}}\n\n{{content}}\n"
	out := RenderTemplate(tpl, TemplateData{
		Title: "演示报告", Date: at,
		Summary: "确认发现 2 个", Content: "正文内容。",
		FindingsTable: "| 严重等级 | 名称 |\n|---|---|\n| HIGH | 演示漏洞 |\n",
	})
	for _, want := range []string{"# 演示报告", "2026-09-23", "确认发现 2 个", "演示漏洞", "正文内容。"} {
		if !strings.Contains(out, want) {
			t.Fatalf("渲染结果缺少 %q:\n%s", want, out)
		}
	}
	// 未知占位符保留原样
	if out := RenderTemplate("{{custom_var}}", TemplateData{}); !strings.Contains(out, "{{custom_var}}") {
		t.Fatalf("未知占位符应原样保留")
	}
}

// 模板没有 {{content}} 时,正文自动追加到文末。
func TestRenderTemplateAppendsContent(t *testing.T) {
	out := RenderTemplate("# 报告封面 {{title}}\n", TemplateData{Title: "X", Content: "自动追加的正文"})
	if !strings.Contains(out, "自动追加的正文") {
		t.Fatalf("无 {{content}} 占位符时正文应追加到文末:\n%s", out)
	}
}

func TestDocxRoundTrip(t *testing.T) {
	md := "# 渗透测试报告\n\n" +
		"## 一、概述\n\n" +
		"本次测试覆盖 example.com,共发现 1 个高危漏洞。\n\n" +
		"## 二、漏洞列表\n\n" +
		"| 严重等级 | 名称 | 状态 |\n|---|---|---|\n| HIGH | 演示漏洞 | confirmed |\n\n" +
		"- 信息收集阶段:子域枚举 12 个\n- 利用阶段:获取后台权限\n\n" +
		"### 修复建议\n\n" +
		"对上传接口增加类型校验。"

	docx, err := DocxFromMarkdown(md)
	if err != nil {
		t.Fatalf("docx 生成失败: %v", err)
	}
	got, st, err := ParseDocxReader(bytes.NewReader(docx))
	if err != nil {
		t.Fatalf("docx 解析失败: %v", err)
	}
	// 生成的 HeadingN 会被样式+outlineLvl 双通道识别回标题。
	for _, want := range []string{"渗透测试报告", "一、概述", "本次测试覆盖 example.com", "演示漏洞", "对上传接口增加类型校验。"} {
		if !strings.Contains(got, want) {
			t.Fatalf("roundtrip 后缺少 %q:\n%s", want, got)
		}
	}
	if len(st.Headings) < 3 {
		t.Fatalf("应识别出至少 3 个标题,实得 %d: %+v", len(st.Headings), st.Headings)
	}
	if st.Tables != 1 {
		t.Fatalf("应识别出 1 张表格,实得 %d", st.Tables)
	}
}

func TestParseDocxRejectsNonZip(t *testing.T) {
	if _, _, err := ParseDocxReader(strings.NewReader("not a zip")); err == nil {
		t.Fatal("非 zip 输入应报错")
	}
}

func TestDebugDocx(t *testing.T) {
	md := "# 标题一\n\n段落甲。\n\n## 标题二\n\n段落乙。\n"
	docx, err := DocxFromMarkdown(md)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(docx), int64(len(docx)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			t.Logf("document.xml: %s", string(b))
		}
	}
	got, st, err := ParseDocxReader(bytes.NewReader(docx))
	t.Logf("GOT:\n%s\nHEADINGS:%+v PARA:%d ERR:%v", got, st.Headings, st.Paragraphs, err)
}
