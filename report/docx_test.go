package report

import (
	"os"
	"strings"
	"testing"
)

// headingFromStyle:数字 styleId 只有经 styles.xml 映射成样式名才能定性;
// 「标题 N」/「heading N」中英都认,字符样式与目录样式不算标题。
func TestHeadingFromStyleWithStyleMap(t *testing.T) {
	styles := docxStyleMap{
		"31": {name: "标题 1（奇安信）", styleType: "paragraph"},
		"32": {name: "标题 2（奇安信）", styleType: "paragraph"},
		"13": {name: "toc 1", styleType: "paragraph"},
		"50": {name: "标题 2 Char", styleType: "character"}, // 字符样式不是段落标题
	}
	if n, ok := headingFromStyle("31", styles); !ok || n != 1 {
		t.Fatalf("styleId=31(标题 1)应判为一级标题,得到 %d,%v", n, ok)
	}
	if n, ok := headingFromStyle("32", styles); !ok || n != 2 {
		t.Fatalf("styleId=32(标题 2)应判为二级标题,得到 %d,%v", n, ok)
	}
	if _, ok := headingFromStyle("13", styles); ok {
		t.Fatal("toc 样式不应判为标题")
	}
	if _, ok := headingFromStyle("50", styles); ok {
		t.Fatal("字符样式不应判为段落标题")
	}
	// 无映射时英文 HeadingN 样式 id 直认(裸文档兼容)。
	if n, ok := headingFromStyle("Heading2", nil); !ok || n != 2 {
		t.Fatalf("Heading2 应直认为二级标题,得到 %d,%v", n, ok)
	}
	if _, ok := headingFromStyle("", styles); ok {
		t.Fatal("空样式不应判为标题")
	}
}

// headingLevelFromName:样式名各种后缀(全角括号说明/(self)/Char)不影响级别判定;
// 仅含「标题」字样但无级数的样式名(如「封皮主标题」)不算标题。
func TestHeadingLevelFromName(t *testing.T) {
	cases := []struct {
		name  string
		level int
		ok    bool
	}{
		{"标题 1（奇安信）", 1, true},
		{"标题 2（奇安信）", 2, true},
		{"标题 3(self)", 3, true},
		{"heading 4", 4, true},
		{"Heading 4", 4, true},
		{"标题5", 5, true},
		{"标题 0（奇安信）", 1, true}, // 0 级按一级处理
		{"toc 1", 0, false},
		{"封皮主标题（奇安信）", 0, false},
		{"目录-名称（奇安信）", 0, false},
		{"Normal", 0, false},
	}
	for _, c := range cases {
		n, ok := headingLevelFromName(c.name)
		if ok != c.ok || (ok && n != c.level) {
			t.Fatalf("样式名 %q:期望 (%d,%v),得到 (%d,%v)", c.name, c.level, c.ok, n, ok)
		}
	}
}

// 真实模板集成:team_template.docx 的标题样式是数字 styleId(31/32 等)+中文样式名,
// 升级后应识别出章节标题(至少 8 个),且正文标题可见文本不带编号、层级正确。
func TestParseTeamTemplateHeadings(t *testing.T) {
	f, err := os.Open("testdata/team_template.docx")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	md, st, err := ParseDocxReader(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Headings) < 8 {
		t.Fatalf("真实模板应至少识别出 8 个标题,实际 %d 个:%+v", len(st.Headings), st.Headings)
	}
	levelOf := map[string]int{}
	for _, h := range st.Headings {
		levelOf[h.Title] = h.Level
	}
	// 文档信息(加粗短行启发式)与正文章节(样式映射)都应被识别。
	for _, want := range []string{"文档信息", "摘要", "项目概述", "问题归纳", "安全风险总结", "测试目的", "Web安全"} {
		if _, ok := levelOf[want]; !ok {
			t.Fatalf("缺少标题 %q;识别到 %d 个:%+v", want, len(st.Headings), st.Headings)
		}
	}
	if levelOf["摘要"] != 1 || levelOf["问题归纳"] != 1 || levelOf["安全风险总结"] != 1 {
		t.Fatalf("一级章节层级应为 1:摘要=%d 问题归纳=%d 安全风险总结=%d",
			levelOf["摘要"], levelOf["问题归纳"], levelOf["安全风险总结"])
	}
	if levelOf["测试目的"] != 2 || levelOf["Web安全"] != 2 {
		t.Fatalf("二级小节层级应为 2:测试目的=%d Web安全=%d", levelOf["测试目的"], levelOf["Web安全"])
	}
	if !strings.Contains(md, "# 摘要") || !strings.Contains(md, "## 测试目的") {
		t.Fatalf("markdown 应含一级/二级标题行:\n%.400s", md)
	}
	// 目录条目(「一. 摘要4」等)不应被误判为标题。
	for _, h := range st.Headings {
		if strings.HasSuffix(h.Title, "4") && strings.HasPrefix(h.Title, "一.") {
			t.Fatalf("目录条目被误判为标题:%+v", h)
		}
	}
}
