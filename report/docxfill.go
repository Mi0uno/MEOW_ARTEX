package report

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
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
// 块标记替换:{{content}}/{{findings_detail}} 展开为一组段落(继承标记段落的
// 段落属性,标题行加粗);{{findings_table}} 展开为带边框的 Word 表格(表头加粗)。

// DocxBlock 是一个块级变量的内容。
type DocxBlock struct {
	Lines []string   // 逐段落文本(# 开头的行渲染为加粗标题)
	Table [][]string // 非空 = 渲染为 Word 表格(首行表头加粗)
}

// DocxData 是 docx 填充数据集。
type DocxData struct {
	Inline map[string]string    // 行内变量 → 文本
	Blocks map[string]DocxBlock // 块变量 → 段落/表格
}

var docxVarRe = regexp.MustCompile(`\{\{\s*([a-z_]+)\s*\}\}`)

// FillDocx 在原始 docx 上做变量替换,返回新 docx 字节流。
func FillDocx(orig []byte, data DocxData) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(orig), int64(len(orig)))
	if err != nil {
		return nil, fmt.Errorf("不是有效的 docx(zip) 文件: %w", err)
	}
	type fileCopy struct {
		name   string
		method uint16
		body   []byte
	}
	var docXML []byte
	files := make([]fileCopy, 0, len(zr.File))
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, rc); err != nil {
			rc.Close()
			return nil, err
		}
		rc.Close()
		if f.Name == "word/document.xml" {
			docXML = buf.Bytes()
		}
		files = append(files, fileCopy{name: f.Name, method: f.Method, body: buf.Bytes()})
	}
	if docXML == nil {
		return nil, fmt.Errorf("docx 缺少 word/document.xml")
	}

	filled, err := FillDocxXML(docXML, data)
	if err != nil {
		return nil, err
	}

	out := &bytes.Buffer{}
	zw := zip.NewWriter(out)
	for _, f := range files {
		body, method := f.body, f.method
		if f.name == "word/document.xml" {
			body, method = filled, zip.Deflate
		}
		hdr := &zip.FileHeader{Name: f.name, Method: method}
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(body); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
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
		np := newPara(body, nil, line, isHeadingLine(line))
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

// replaceBlocks 定位「整段恰为块标记」的段落并展开:
//   - {{content}}/{{findings_detail}} → 一组段落(继承标记段落的段落属性,标题行加粗)
//   - {{findings_table}}              → 带边框表格(表头加粗)
//
// 表格内不展开块标记(不支持嵌套)。
func replaceBlocks(root *etree.Element, data DocxData) {
	var process func(parent *etree.Element)
	process = func(parent *etree.Element) {
		children := parent.ChildElements()
		for _, p := range children {
			if !isW(p, "p") {
				continue
			}
			name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(paraFullText(p)), "{{"), "}}"))
			block, ok := data.Blocks[name]
			if !ok {
				continue
			}
			ppr := findWChild(p, "pPr")
			idx := childIndex(parent, p)
			if idx < 0 {
				continue
			}
			ins := idx
			if block.Table != nil {
				for _, row := range block.Table {
					tbl := newTable(parent, row)
					ins++
					parent.InsertChildAt(ins, tbl)
				}
			} else {
				for _, line := range block.Lines {
					np := newPara(parent, ppr, line, isHeadingLine(line))
					ins++
					parent.InsertChildAt(ins, np)
				}
			}
			parent.RemoveChild(p)
			_ = ins
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

// newPara 在 parent 下构造段落(自动挂载,返回引用)。
// pprTemplate 继承标记段落的段落属性;heading=true 时文本加粗并去掉 # 记号。
func newPara(parent *etree.Element, pprTemplate *etree.Element, text string, heading bool) *etree.Element {
	p := parent.CreateElement("w:p")
	if pprTemplate != nil {
		p.InsertChildAt(0, pprTemplate.Copy())
	}
	r := p.CreateElement("w:r")
	if heading {
		rpr := r.CreateElement("w:rPr")
		rpr.CreateElement("w:b")
	}
	t := r.CreateElement("w:t")
	t.CreateAttr("xml:space", "preserve")
	t.SetText(stripHeadingMark(text))
	return p
}

// newTable 在 parent 下构造一个带边框表格(首行表头加粗)。row 是一行单元格文本。
func newTable(parent *etree.Element, row []string) *etree.Element {
	tbl := parent.CreateElement("w:tbl")
	pr := tbl.CreateElement("w:tblPr")
	borders := pr.CreateElement("w:tblBorders")
	for _, side := range []string{"top", "left", "bottom", "right", "insideH", "insideV"} {
		b := borders.CreateElement("w:" + side)
		b.CreateAttr("w:val", "single")
		b.CreateAttr("w:sz", "4")
	}
	tr := tbl.CreateElement("w:tr")
	for _, cell := range row {
		tc := tr.CreateElement("w:tc")
		p := tc.CreateElement("w:p")
		r := p.CreateElement("w:r")
		t := r.CreateElement("w:t")
		t.CreateAttr("xml:space", "preserve")
		t.SetText(cell)
	}
	return tbl
}
