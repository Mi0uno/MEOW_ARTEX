package report

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
)

// ---- docx 生成:把报告 markdown(标题/段落/无序列表/表格/代码块)渲染成 .docx ----
//
// 生成基础但规范的 OOXML 文档:Heading1-4 样式、正文、表格边框、代码块等宽字体。
// 中文east-asia 字体默认微软雅黑,西文 Calibri。

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
			body.WriteString(docxPara(text, fmt.Sprintf("Heading%d", level), ""))
		case strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* "):
			body.WriteString(docxPara(stripMdEmphasis(trimmed[2:]), "", "• "))
		default:
			body.WriteString(docxPara(stripMdEmphasis(trimmed), "", ""))
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

// stripMdEmphasis 去掉行内 **bold** / *it* / `code` 标记(基础导出保留纯文本)。
func stripMdEmphasis(s string) string {
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, "__", "")
	s = strings.ReplaceAll(s, "`", "")
	return s
}

// docxPara 生成一个 <w:p>;prefix 是列表圆点等文本前缀。style 为空用 Normal。
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

// docxTable 把 markdown 表格行渲染成带边框的 <w:tbl>。
func docxTable(rows []string) string {
	if len(rows) == 0 {
		return ""
	}
	cellsOf := func(row string) []string {
		t := strings.TrimSpace(row)
		t = strings.TrimPrefix(t, "|")
		t = strings.TrimSuffix(t, "|")
		parts := strings.Split(t, "|")
		for i := range parts {
			parts[i] = strings.TrimSpace(stripMdEmphasis(parts[i]))
		}
		return parts
	}
	var b strings.Builder
	b.WriteString(`<w:tbl><w:tblPr><w:tblStyle w:val="TableGrid"/><w:tblBorders>` +
		`<w:top w:val="single" w:sz="4"/><w:left w:val="single" w:sz="4"/><w:bottom w:val="single" w:sz="4"/><w:right w:val="single" w:sz="4"/><w:insideH w:val="single" w:sz="4"/><w:insideV w:val="single" w:sz="4"/>` +
		`</w:tblBorders></w:tblPr>`)
	for ri, row := range rows {
		b.WriteString(`<w:tr>`)
		for _, c := range cellsOf(row) {
			if c == "---" {
				continue
			}
			b.WriteString(`<w:tc><w:tcPr><w:tcW w:w="0" w:type="auto"/></w:tcPr>`)
			b.WriteString(docxPara(c, "", ""))
			b.WriteString(`</w:tc>`)
		}
		_ = ri
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
	`<Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>` +
	`</Types>`

const relsXML = xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>` +
	`</Relationships>`

const docRelsXML = xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>` +
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
