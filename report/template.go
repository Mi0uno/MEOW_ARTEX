package report

import (
	"regexp"
	"strings"
	"time"
)

// ---- 模板渲染:把报告中心的一份报告按上传模板填充输出 ----
//
// 模板是 markdown(上传 docx 会先解析归一为 md),内置占位符:
//   {{title}} 报告标题      {{date}} 生成日期
//   {{summary}} 摘要        {{content}} 报告正文(markdown 全文)
//   {{findings_table}} 漏洞表格(mk)
//   {{findings_detail}} 漏洞逐条详情
//   {{assets_summary}} 资产统计
// 兼容 {{ var }} 带空格写法。模板不含 {{content}} 时,正文自动追加到文末,
// 保证导出永不丢正文。

// TemplateData 是渲染占位符的数据集。
type TemplateData struct {
	Title          string
	Date           time.Time
	Summary        string
	Content        string
	FindingsTable  string
	FindingsDetail string
	AssetsSummary  string
}

// TemplateVariables 是内置占位符清单(前端「插入变量」用)。
var TemplateVariables = []struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Desc  string `json:"desc"`
}{
	{"title", "报告标题", "报告中心的报告标题"},
	{"date", "生成日期", "导出当天日期"},
	{"summary", "摘要", "报告摘要行"},
	{"content", "报告正文", "报告完整 markdown 正文"},
	{"findings_table", "漏洞表格", "markdown 表格:严重等级/名称/类别/状态"},
	{"findings_detail", "漏洞详情", "逐条漏洞的完整信息"},
	{"assets_summary", "资产统计", "按类型的资产计数"},
}

var templateVarRe = regexp.MustCompile(`\{\{\s*([a-z_]+)\s*\}\}`)

// AnalyzeMarkdown 统计 markdown 模板结构。
func AnalyzeMarkdown(md string) TemplateStructure {
	st := TemplateStructure{}
	inCode := false
	for _, line := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inCode = !inCode
			continue
		}
		if inCode {
			continue
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "#"):
			level := 0
			for level < len(trimmed) && trimmed[level] == '#' {
				level++
			}
			if level <= 6 {
				st.Headings = append(st.Headings, TemplateHeading{Level: level, Title: strings.TrimSpace(trimmed[level:])})
			}
		case strings.HasPrefix(trimmed, "|"):
			continue // 表格行不计段落
		case trimmed != "":
			st.Paragraphs++
			st.Words += len([]rune(trimmed))
		}
	}
	return st
}

// RenderTemplate 用 data 填充模板。未知占位符原样保留(便于用户自定义扩展)。
func RenderTemplate(tpl string, data TemplateData) string {
	values := map[string]string{
		"title":           data.Title,
		"date":            data.Date.Format("2006-01-02"),
		"summary":         data.Summary,
		"content":         data.Content,
		"findings_table":  data.FindingsTable,
		"findings_detail": data.FindingsDetail,
		"assets_summary":  data.AssetsSummary,
	}
	out := templateVarRe.ReplaceAllStringFunc(tpl, func(m string) string {
		key := templateVarRe.FindStringSubmatch(m)[1]
		if v, ok := values[key]; ok {
			return v
		}
		return m
	})
	// 保底:模板没写 {{content}} 时正文追加到文末,导出永不丢正文。
	if !strings.Contains(tpl, "{{content}}") && strings.TrimSpace(data.Content) != "" {
		out = strings.TrimRight(out, "\n") + "\n\n" + strings.TrimSpace(data.Content) + "\n"
	}
	return out
}

// FindingsTable 把漏洞行(严重等级/名称/类别/状态)渲染成 markdown 表格。
func FindingsTable(rows [][4]string) string {
	if len(rows) == 0 {
		return "_无确认漏洞。_\n"
	}
	var b strings.Builder
	b.WriteString("| 严重等级 | 名称 | 类别 | 状态 |\n|---|---|---|---|\n")
	for _, r := range rows {
		cells := make([]string, len(r))
		for i, c := range r {
			cells[i] = strings.ReplaceAll(strings.ReplaceAll(c, "|", "\\|"), "\n", " ")
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}
	return b.String()
}

// FindingsDetailMd 把逐条漏洞渲染成 markdown 段落块。
func FindingsDetailMd(items []string) string {
	if len(items) == 0 {
		return "_无确认漏洞。_\n"
	}
	return strings.Join(items, "\n\n") + "\n"
}
