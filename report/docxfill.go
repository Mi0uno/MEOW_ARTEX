package report

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/beevik/etree"
)

// ---- docx 格式保真填充 ----
//
// 原理:不再"解析成 md 再重新生成",而是直接改写用户上传的原始 docx 包里的
// word/document.xml——模板中的固定文字、颜色、字体、表格样式、页眉页脚、图片
// (Logo)等一切内容原字节保留,只有「变量标记」所在位置的文本被替换,
// 且新文本继承标记处的格式(字体/颜色/字号跟随原 run)。
//
// 标记约定(用户在 Word 里直接输入,允许被 Word 拆散到多个 run):
//   行内: {{title}} {{date}} {{summary}} {{assets_summary}}
//   独占段落(整段只有这一个标记): {{content}} {{findings_table}} {{findings_detail}}
//
// 块标记替换:段落文本剥掉 {{}} 后能命中 Blocks 里的【任意键】即展开(不限于内置
// 变量名),这让模板可以自定义块标记。展开形态:DocxBlock.Table 优先渲染成 Word
// 表格(首行表头加粗);否则 Lines 逐段展开(继承标记段落的段落属性,标题行加粗);
// 传入的 Lines 整块是一张 markdown 表格文本(如 findings_table 的 md 表格)时,
// 自动解析成 Word 表格,不再以 "| a | b |" 字面段落残留。

// DocxBlock 是一个块级变量的内容。
type DocxBlock struct {
	Lines []string   // 逐段落文本(# 开头的行渲染为加粗标题)
	Table [][]string // 非空 = 渲染为 Word 表格(首行表头加粗)
}

// DocxBlockFromMarkdown 把一段 markdown 文本组装成块变量:整块是一张 md 表格
// (如 findings_table 的渲染产物)时解析成 Table(填充时渲染为 Word 表格),
// 否则整块按行段落展开。供 server 侧组装 FillDocx 的 Blocks 使用。
func DocxBlockFromMarkdown(md string) DocxBlock {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	if rows := mdTableRows(lines); rows != nil {
		return DocxBlock{Table: rows}
	}
	return DocxBlock{Lines: lines}
}

// DocxData 是 docx 填充数据集。
type DocxData struct {
	Inline map[string]string    // 行内变量 → 文本
	Blocks map[string]DocxBlock // 块变量 → 段落/表格
}

var docxVarRe = regexp.MustCompile(`\{\{\s*([a-z_]+)\s*\}\}`)

// FillDocx 在原始 docx 上做变量替换,返回新 docx 字节流。
func FillDocx(orig []byte, data DocxData) ([]byte, error) {
	docXML, parts, err := readDocxParts(orig)
	if err != nil {
		return nil, err
	}
	filled, err := FillDocxXML(docXML, data)
	if err != nil {
		return nil, err
	}
	return writeDocxParts(parts, filled)
}

// FillDocxXML 对 document.xml 字节流做变量替换,返回新的 XML 字节流。
func FillDocxXML(docXML []byte, data DocxData) ([]byte, error) {
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(docXML); err != nil {
		return nil, fmt.Errorf("document.xml 解析失败: %w", err)
	}
	root := doc.Root()
	if root == nil {
		return nil, fmt.Errorf("document.xml 无根元素")
	}

	var walk func(el *etree.Element)
	walk = func(el *etree.Element) {
		if isW(el, "p") {
			replaceInlineInPara(el, data.Inline)
		}
		for _, ch := range el.ChildElements() {
			walk(ch)
		}
	}
	walk(root)

	replaceBlocks(root, data)

	// 保底:模板没有 {{content}} 标记时,把报告正文追加到文档末尾
	// (sectPr 之前),保证导出永不丢报告正文。
	if blk, ok := data.Blocks["content"]; ok && len(blk.Lines) > 0 &&
		!strings.Contains(string(docXML), "{{content") {
		appendLinesBeforeSectPr(root, blk.Lines)
	}

	return doc.WriteToBytes()
}

// appendLinesBeforeSectPr 把一组文本段落追加到 body 末尾(w:sectPr 之前)。
// 无标记段落可捕获基底样式,走空基底(行为与历史版本一致:标题行加粗,正文无格式)。
func appendLinesBeforeSectPr(root *etree.Element, lines []string) {
	body := findWChild(root, "body")
	if body == nil {
		body = root
	}
	idx := len(body.Child)
	for _, ch := range body.ChildElements() {
		if isW(ch, "sectPr") {
			idx = childIndex(body, ch)
			break
		}
	}
	for _, line := range lines {
		kind := paraPlain
		if isHeadingLine(line) {
			kind = paraHeading
		}
		np := newStyledPara(docxBaseStyle{}, kind, line)
		body.InsertChildAt(idx, np)
		idx++
	}
}

func isW(el *etree.Element, tag string) bool {
	return el != nil && el.Space == "w" && el.Tag == tag
}

// collectT 递归收集段落内全部 w:t 元素(文档序)。
func collectT(p *etree.Element) []*etree.Element {
	var out []*etree.Element
	var walk func(el *etree.Element)
	walk = func(el *etree.Element) {
		for _, ch := range el.ChildElements() {
			if isW(ch, "t") {
				out = append(out, ch)
			}
			walk(ch)
		}
	}
	walk(p)
	return out
}

// paraFullText 拼接段落全部文本。
func paraFullText(p *etree.Element) string {
	var sb strings.Builder
	for _, t := range collectT(p) {
		sb.WriteString(t.Text())
	}
	return sb.String()
}

// paraMarkers 提取段落内全部变量名(按出现序,允许跨 run)。
func paraMarkers(p *etree.Element) []string {
	var names []string
	for _, m := range docxVarRe.FindAllStringSubmatch(paraFullText(p), -1) {
		names = append(names, strings.TrimSpace(m[1]))
	}
	return names
}

// replaceInlineInPara 在单个段落内做行内变量替换(标记允许跨 run)。
// 替换文本写入标记起点的第一个 w:t(继承其格式),被覆盖的其余 w:t 清空。
// 全程使用字节坐标:变量标记与拼接边界均为 ASCII,字节切分不会破坏 UTF-8。
func replaceInlineInPara(p *etree.Element, vars map[string]string) {
	if len(vars) == 0 {
		return
	}
	ts := collectT(p)
	if len(ts) == 0 {
		return
	}
	texts := make([]string, len(ts))
	starts := make([]int, len(ts))
	refresh := func() {
		off := 0
		for i, t := range ts {
			texts[i] = t.Text()
			starts[i] = off
			off += len(texts[i])
		}
	}
	refresh()

	full := strings.Join(texts, "")
	type spanT struct {
		a, b int
		name string
	}
	var spans []spanT
	for _, m := range docxVarRe.FindAllStringSubmatchIndex(full, -1) {
		name := strings.TrimSpace(full[m[0]:m[1]])
		name = strings.TrimSuffix(strings.TrimPrefix(name, "{{"), "}}")
		spans = append(spans, spanT{m[0], m[1], name})
	}

	// 倒序替换:后面的 span 先处理,前面 span 的字节偏移不受影响。
	for i := len(spans) - 1; i >= 0; i-- {
		sp := spans[i]
		val, ok := vars[sp.name]
		if !ok {
			continue
		}
		a, b := sp.a, sp.b
		first, last := -1, -1
		for ti := range ts {
			s, e := starts[ti], starts[ti]+len(texts[ti])
			if len(texts[ti]) == 0 {
				continue
			}
			if s < b && e > a {
				if first < 0 {
					first = ti
				}
				last = ti
			}
		}
		if first < 0 {
			continue
		}
		mid := val
		if first == last {
			pre := texts[first][:a-starts[first]]
			suf := texts[first][b-starts[first]:]
			ts[first].SetText(pre + mid + suf)
		} else {
			pre := texts[first][:a-starts[first]]
			ts[first].SetText(pre + mid)
			for ti := first + 1; ti < last; ti++ {
				ts[ti].SetText("")
			}
			suf := texts[last][b-starts[last]:]
			ts[last].SetText(suf)
		}
		refresh()
	}
}

// replaceBlocks 定位「整段恰为块标记」的段落并展开。标记名 = 段落文本剥掉 {{}}
// 后的任意键(命中 Blocks 即展开,不限于内置变量名,模板可自定义块标记):
//   - DocxBlock.Table 非空(或 Lines 整块是 md 表格文本)→ 带边框表格(表头加粗)
//   - 否则 Lines 逐段展开(继承标记段落的段落属性与首 run 字符属性,标题行加粗)
//
// 构建走 D2 的样式化构建器:标记段落周边捕获基底样式,正文段继承基底字体/字号/
// 颜色;标记段无样式可捕获时退回无格式段落(与历史行为一致)。
//
// 表格内不展开块标记(不支持嵌套)。
func replaceBlocks(root *etree.Element, data DocxData) {
	var process func(parent *etree.Element)
	process = func(parent *etree.Element) {
		for _, p := range parent.ChildElements() {
			if !isW(p, "p") {
				continue
			}
			name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(paraFullText(p)), "{{"), "}}"))
			block, ok := data.Blocks[name]
			if !ok {
				continue
			}
			idx := childIndex(parent, p)
			if idx < 0 {
				continue
			}
			// 基底样式捕获:标记段落的 pPr + 首个带 rPr 的 run(展开内容继承格式)。
			base := captureParaStyle(p)
			ins := idx
			// 传入 Lines 是纯 md 表格文本时自动升级为 Word 表格(如 findings_table
			// 以 md 文本组装传入的场景);混有非表格行则整块按段落展开,不丢行。
			table, lines := block.Table, block.Lines
			if len(table) == 0 {
				if rows := mdTableRows(lines); rows != nil {
					table, lines = rows, nil
				}
			}
			if len(table) > 0 {
				ins++
				parent.InsertChildAt(ins, newStyledTable(base, table))
			}
			for _, line := range lines {
				kind := paraPlain
				if isHeadingLine(line) {
					kind = paraHeading
				}
				ins++
				parent.InsertChildAt(ins, newStyledPara(base, kind, line))
			}
			parent.RemoveChild(p)
		}
		for _, ch := range parent.ChildElements() {
			if isW(ch, "tbl") {
				continue
			}
			process(ch)
		}
	}
	process(root)
}

// mdTableRows 判断一组文本行是否「整块是一张 markdown 表格」,是则解析出行数据
// (跳过 |---|---| 分隔行),否则返回 nil(调用方回退按段落展开)。
func mdTableRows(lines []string) [][]string {
	var rows [][]string
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" {
			continue
		}
		if !strings.HasPrefix(t, "|") {
			return nil
		}
		if isMdTableSeparator(t) {
			continue
		}
		rows = append(rows, splitMdTableRow(t))
	}
	if len(rows) == 0 {
		return nil
	}
	return rows
}

// isMdTableSeparator 识别 markdown 表格的分隔行(| --- | :---: |)。
func isMdTableSeparator(t string) bool {
	return strings.HasPrefix(t, "|") && strings.Contains(t, "---")
}

// splitMdTableRow 拆一行 "| a | b |" 为单元格文本;处理 \| 转义(还原为 |),
// 其余反斜杠原样保留,避免吞掉 Windows 路径等真实内容。
func splitMdTableRow(t string) []string {
	t = strings.TrimSpace(t)
	t = strings.TrimPrefix(t, "|")
	t = strings.TrimSuffix(t, "|")
	var cells []string
	var cur strings.Builder
	esc := false
	for i := 0; i < len(t); i++ {
		c := t[i]
		if esc {
			if c != '|' {
				cur.WriteByte('\\')
			}
			cur.WriteByte(c)
			esc = false
			continue
		}
		switch c {
		case '\\':
			esc = true
		case '|':
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	if esc {
		cur.WriteByte('\\')
	}
	cells = append(cells, strings.TrimSpace(cur.String()))
	return cells
}

func findWChild(p *etree.Element, tag string) *etree.Element {
	for _, ch := range p.ChildElements() {
		if isW(ch, tag) {
			return ch
		}
	}
	return nil
}

func childIndex(parent, child *etree.Element) int {
	for i, ch := range parent.Child {
		if ch == child {
			return i
		}
	}
	return -1
}

// isHeadingLine 识别 markdown 标题行(#/##/…)。
func isHeadingLine(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "#")
}

// stripHeadingMark 去掉标题行的 # 记号。
func stripHeadingMark(line string) string {
	t := strings.TrimSpace(line)
	n := 0
	for n < len(t) && t[n] == '#' {
		n++
	}
	return strings.TrimSpace(t[n:])
}
