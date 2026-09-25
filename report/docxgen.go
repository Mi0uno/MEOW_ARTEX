package report

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"regexp"
	"strings"
)

// ---- docx 生成:把报告 markdown(标题/段落/有序/无序列表/引用/表格/代码块)渲染成 .docx ----
//
// 生成基础但规范的 OOXML 文档:Heading1-4 样式、正文、表格边框、代码块等宽字体。
// 行内标记(**粗体** / `行内代码`)拆 run 渲染——底线:不丢行、不剩星号、粗体可见。
// 中文 east-asia 字体默认微软雅黑,西文 Calibri,行内代码 Consolas。

// DocxFromMarkdown 把 markdown 渲染为 .docx 字节流。
func DocxFromMarkdown(md string) ([]byte, error) {
	var body strings.Builder
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	inCode := false
	codeBuf := []string{}
	i := 0
	for i < len(lines) {
		line := lines[i]
		switch {
		case strings.HasPrefix(strings.TrimSpace(line), "```"):
			if inCode {
				for _, cl := range codeBuf {
					body.WriteString(docxPara(cl, "Code", ""))
				}
				codeBuf = nil
				inCode = false
			} else {
				inCode = true
			}
			i++
			continue
		}
		if inCode {
			codeBuf = append(codeBuf, line)
			i++
			continue
		}
		if strings.HasPrefix(line, "|") && strings.HasSuffix(strings.TrimSpace(line), "|") {
			// 收集连续表格行
			var rows []string
			for i < len(lines) && strings.HasPrefix(lines[i], "|") {
				if !isTableSeparator(lines[i]) {
					rows = append(rows, lines[i])
				}
				i++
			}
			body.WriteString(docxTable(rows))
			continue
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			// 空行忽略(段落间距由样式提供)
		case strings.HasPrefix(trimmed, "#"):
			level, text := splitHeading(trimmed)
			if level > 4 {
				level = 4
			}
			body.WriteString(docxMdPara(text, fmt.Sprintf("Heading%d", level), ""))
		case strings.HasPrefix(trimmed, ">"):
			// 引用块:> 前缀剥掉,内容按行内标记拆 run 后整段缩进灰字。
			text := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(trimmed, ">"), " "))
			if text != "" {
				body.WriteString(docxQuotePara(text))
			}
		case orderedListRe.MatchString(trimmed):
			// 有序列表:保留编号文本前缀(1. / 1)整段缩进,编号即内容,绝不丢行。
			body.WriteString(docxMdParaPPr(trimmed, "", `<w:ind w:left="360"/>`))
		case strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* "):
			body.WriteString(docxMdPara(trimmed[2:], "", "• "))
		default:
			body.WriteString(docxMdPara(trimmed, "", ""))
		}
		i++
	}
	if inCode {
		for _, cl := range codeBuf {
			body.WriteString(docxPara(cl, "Code", ""))
		}
	}

	doc := xmlHeader + `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
		body.String() + `<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440"/></w:sectPr></w:body></w:document>`

	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	for _, f := range []struct{ name, content string }{
		{"[Content_Types].xml", ctypesXML},
		{"_rels/.rels", relsXML},
		{"word/_rels/document.xml.rels", docRelsXML},
		{"word/styles.xml", stylesXML},
		{"word/document.xml", doc},
	} {
		w, err := zw.Create(f.name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(f.content)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// orderedListRe 识别有序列表行:"1. " 或 "1) " 开头(编号即内容,保留前缀原样)。
var orderedListRe = regexp.MustCompile(`^\d{1,3}[.)]\s`)

// mdRun 是行内文本的一个同格式片段。
type mdRun struct {
	text string
	bold bool
	code bool
}

// splitInlineRuns 把一行文本按 **粗体** / __粗体__ / `行内代码` 拆成 run 序列。
// 约束:不丢正文——未配对的标记不再当分隔符,其标记字符直接剔除(成品里不留
// markdown 记号);代码段内部逐字保留,不做嵌套解析;普通文本残余的孤立星号
// 一并剔除(底线:导出的 docx 不出现字面 ** )。
func splitInlineRuns(s string) []mdRun {
	var runs []mdRun
	var plain strings.Builder
	flush := func() {
		if plain.Len() > 0 {
			runs = append(runs, mdRun{text: plain.String()})
			plain.Reset()
		}
	}
	for i := 0; i < len(s); {
		rest := s[i:]
		switch {
		case strings.HasPrefix(rest, "**") || strings.HasPrefix(rest, "__"):
			mark := rest[:2]
			if end := strings.Index(rest[2:], mark); end >= 0 {
				flush()
				runs = append(runs, mdRun{text: rest[2 : 2+end], bold: true})
				i += 2 + end + 2
				continue
			}
			i += 2 // 未配对:只吞掉标记本身
		case rest[0] == '`':
			if end := strings.IndexByte(rest[1:], '`'); end >= 0 {
				flush()
				runs = append(runs, mdRun{text: rest[1 : 1+end], code: true})
				i += end + 2
				continue
			}
			i++ // 未配对反引号:剔除
		default:
			plain.WriteByte(s[i])
			i++
		}
	}
	flush()
	for j := range runs {
		if !runs[j].bold && !runs[j].code && strings.ContainsRune(runs[j].text, '*') {
			runs[j].text = strings.ReplaceAll(runs[j].text, "*", "")
		}
	}
	out := runs[:0]
	for _, r := range runs {
		if r.text != "" {
			out = append(out, r)
		}
	}
	return out
}

// isTableSeparator markdown 表格的 |---|---| 分隔行(只用于切割,不产出表格行)。
func isTableSeparator(line string) bool {
	t := strings.TrimSpace(line)
	return t != "" && strings.HasPrefix(t, "|") && strings.Contains(t, "---")
}

// splitHeading 拆分 "# 标题" → (级别, 文本)。
func splitHeading(line string) (int, string) {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	return n, strings.TrimSpace(line[n:])
}

// stripMdEmphasis 去掉行内 **bold** / __ / ` 标记(纯文本兜底路径用;
// 正文段落请走 docxMdPara,那里会拆 run 保留粗体与等宽)。
func stripMdEmphasis(s string) string {
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, "__", "")
	s = strings.ReplaceAll(s, "`", "")
	return s
}

// docxPara 生成一个 <w:p>;文本原样单 run,不做 markdown 行内解析
// (代码块内容必须逐字保留)。prefix 是列表圆点等文本前缀。style 为空用 Normal。
func docxPara(text, style, prefix string) string {
	var pp strings.Builder
	if style != "" {
		pp.WriteString(`<w:pPr><w:pStyle w:val="` + style + `"/></w:pPr>`)
	}
	run := `<w:t xml:space="preserve">` + xmlEscape(prefix+text) + `</w:t>`
	if style == "Code" {
		run = `<w:rPr><w:rFonts w:ascii="Courier New" w:hAnsi="Courier New"/></w:rPr>` + run
	}
	return `<w:p>` + pp.String() + `<w:r>` + run + `</w:r></w:p>`
}

// docxMdPara 按行内标记拆 run 生成段落(粗体加 <w:b/>,行内代码用等宽字体)。
func docxMdPara(text, style, prefix string) string {
	return docxRunPara(splitInlineRuns(text), style, prefix, "", "")
}

// docxMdParaPPr 同 docxMdPara,但允许追加段落属性(缩进等),有序列表用。
func docxMdParaPPr(text, style, pPrExtra string) string {
	return docxRunPara(splitInlineRuns(text), style, "", pPrExtra, "")
}

// docxQuotePara 引用块段落:左缩进 + 深灰字(引用是辅助信息,视觉上弱化即可)。
// 颜色属于 run 属性(w:color 不能出现在 w:pPr 里),逐 run 附带。
func docxQuotePara(text string) string {
	return docxRunPara(splitInlineRuns(text), "", "", `<w:ind w:left="400"/>`, `<w:color w:val="595959"/>`)
}

// docxRunPara 由 run 序列构造一个 <w:p>;pPrExtra 追加进段落属性(缩进等),
// rPrExtra 追加进每个 run 的字符属性(引用灰字等)。prefix 只进第一个 run
// (列表圆点)。无可用 run 时仍输出空段落保住行结构。
func docxRunPara(runs []mdRun, style, prefix, pPrExtra, rPrExtra string) string {
	var pp strings.Builder
	if style != "" || pPrExtra != "" {
		pp.WriteString("<w:pPr>")
		if style != "" {
			pp.WriteString(`<w:pStyle w:val="` + style + `"/>`)
		}
		pp.WriteString(pPrExtra)
		pp.WriteString("</w:pPr>")
	}
	if len(runs) == 0 {
		runs = []mdRun{{text: ""}}
	}
	for _, r := range runs {
		var rpr strings.Builder
		rpr.WriteString(rPrExtra)
		switch {
		case r.code:
			rpr.WriteString(`<w:rFonts w:ascii="Consolas" w:hAnsi="Consolas"/>`)
		case r.bold:
			rpr.WriteString("<w:b/>")
		}
		rp := ""
		if rpr.Len() > 0 {
			rp = "<w:rPr>" + rpr.String() + "</w:rPr>"
		}
		pp.WriteString("<w:r>" + rp + `<w:t xml:space="preserve">` + xmlEscape(prefix+r.text) + "</w:t></w:r>")
		prefix = ""
	}
	return "<w:p>" + pp.String() + "</w:p>"
}

// docxTable 把 markdown 表格行渲染成带边框的 <w:tbl>;单元格内同样拆行内
// run(粗体/行内代码),表格是报告正文的主要信息载体,格式不能退化成纯文本。
func docxTable(rows []string) string {
	if len(rows) == 0 {
		return ""
	}
	cellsOf := func(row string) []string {
		t := strings.TrimSpace(row)
		t = strings.TrimPrefix(t, "|")
		t = strings.TrimSuffix(t, "|")
		return strings.Split(t, "|")
	}
	var b strings.Builder
	b.WriteString(`<w:tbl><w:tblPr><w:tblStyle w:val="TableGrid"/><w:tblBorders>` +
		`<w:top w:val="single" w:sz="4"/><w:left w:val="single" w:sz="4"/><w:bottom w:val="single" w:sz="4"/><w:right w:val="single" w:sz="4"/><w:insideH w:val="single" w:sz="4"/><w:insideV w:val="single" w:sz="4"/>` +
		`</w:tblBorders></w:tblPr>`)
	for _, row := range rows {
		b.WriteString(`<w:tr>`)
		for _, c := range cellsOf(row) {
			c = strings.TrimSpace(c)
			if c == "---" {
				continue
			}
			b.WriteString(`<w:tc><w:tcPr><w:tcW w:w="0" w:type="auto"/></w:tcPr>`)
			b.WriteString(docxMdPara(c, "", ""))
			b.WriteString(`</w:tc>`)
		}
		b.WriteString(`</w:tr>`)
	}
	b.WriteString(`</w:tbl>`)
	return b.String()
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

const xmlHeader = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`

const ctypesXML = xmlHeader + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
	`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
	`<Default Extension="xml" ContentType="application/xml"/>` +
	`<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>` +
	`<Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.styles+xml"/>` +
	`</Types>`

const relsXML = xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>` +
	`</Relationships>`

const docRelsXML = xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="word/styles.xml"/>` +
	`</Relationships>`

// stylesXML 定义 Heading1-4/Normal/Code:黑体系标题 + 中文正文微软雅黑。
var stylesXML = xmlHeader + `<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
	`<w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Calibri" w:hAnsi="Calibri" w:eastAsia="微软雅黑"/><w:sz w:val="22"/></w:rPr></w:rPrDefault></w:docDefaults>` +
	`<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/></w:style>` +
	headingStyle(1, 32) + headingStyle(2, 28) + headingStyle(3, 26) + headingStyle(4, 24) +
	`<w:style w:type="paragraph" w:styleId="Code"><w:name w:val="Code"/><w:pPr><w:shd w:val="clear" w:fill="F2F2F2"/></w:pPr></w:style>` +
	`</w:styles>`

func headingStyle(level, halfPoints int) string {
	return `<w:style w:type="paragraph" w:styleId="Heading` + itoa(level) + `"><w:name w:val="heading ` + itoa(level) + `"/>` +
		`<w:basedOn w:val="Normal"/><w:pPr><w:outlineLvl w:val="` + itoa(level-1) + `"/><w:keepNext/></w:pPr>` +
		`<w:rPr><w:b/><w:sz w:val="` + itoa(halfPoints) + `"/></w:rPr></w:style>`
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
