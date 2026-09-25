package report

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/beevik/etree"
)

// ---- docx 解析:把 word/document.xml 还原为归一 markdown + 章节结构 ----
//
// 精准读取约定:
//   - 段落样式 w:pStyle(w:val="HeadingN") 与大纲级别 w:outlineLvl 双信号识别标题层级
//     (各语言 Word 的内置标题样式与用户自定大纲级别均能命中);
//   - 文本只收集 w:t 内的 chardata; w:br→换行, w:tab→制表;
//   - 修订删除(w:del)与域代码(w:instrText)内容跳过;
//   - w:tbl 还原为 markdown 表格(首行表头);
//   - ListParagraph 样式识别为无序列表。

// TemplateStructure 是解析出的模板结构(JSON 落库)。
type TemplateStructure struct {
	Headings   []TemplateHeading `json:"headings"`
	Paragraphs int               `json:"paragraphs"`
	Tables     int               `json:"tables"`
	Words      int               `json:"words"`
}

// TemplateHeading 是一个识别出的章节标题。
type TemplateHeading struct {
	Level int    `json:"level"`
	Title string `json:"title"`
}

// ParseDocxReader 从 .docx 字节流提取 markdown 与结构。
func ParseDocxReader(r io.Reader) (markdown string, structure TemplateStructure, err error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", structure, err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", structure, fmt.Errorf("不是有效的 docx(zip) 文件: %w", err)
	}
	var docFile, stylesFile *zip.File
	for _, f := range zr.File {
		switch f.Name {
		case "word/document.xml":
			docFile = f
		case "word/styles.xml":
			stylesFile = f
		}
	}
	if docFile == nil {
		return "", structure, fmt.Errorf("docx 缺少 word/document.xml")
	}
	// 一并解析 styles.xml 建 styleId→样式名映射:真实模板的标题样式是数字
	// styleId(如 31/32)+ 中文样式名「标题 N（奇安信）」,只有经映射才能定性,
	// 只认英文 HeadingN 会全部漏判。
	styles := docxStyleMap{}
	if stylesFile != nil {
		rc, err := stylesFile.Open()
		if err == nil {
			sdata, rerr := io.ReadAll(rc)
			rc.Close()
			if rerr == nil {
				styles = parseStylesXML(sdata)
			}
		}
	}
	rc, err := docFile.Open()
	if err != nil {
		return "", structure, err
	}
	defer rc.Close()
	return parseDocumentXML(rc, styles)
}

// docxState 是 document.xml 的流式解析状态。
type docxState struct {
	md        strings.Builder
	structure TemplateStructure
	styles    docxStyleMap // styleId→样式名映射(来自 styles.xml,可空)

	// 段落级
	inP       bool
	paraText  strings.Builder
	paraStyle string
	outline   int // -1 = 未设置
	// 加粗启发式:中文公文模板常手动加粗当标题(无 Heading 样式/大纲级别)。
	// 段落内所有带文本的 run 都加粗且文本较短 → 视为小标题。
	paraAllBold bool
	inRun       bool
	runBold     bool

	// 文本收集
	inT     bool // 在 w:t 内
	inInstr bool // 在 w:instrText 内(域代码,跳过)
	inDel   bool // 在修订删除内(跳过)

	// 表格级
	inTbl     bool
	inCell    bool
	rows      []string
	rowCells  []string
	cellParas []string
}

func (s *docxState) resetPara() {
	s.inP = false
	s.paraText.Reset()
	s.paraStyle = ""
	s.outline = -1
	s.paraAllBold = true
	s.inRun = false
	s.runBold = false
}

// flushPara 结束一个段落:单元格内并入 cellParas,否则按样式产出 markdown。
func (s *docxState) flushPara() {
	text := strings.TrimSpace(s.paraText.String())
	style, outline, allBold := s.paraStyle, s.outline, s.paraAllBold
	s.resetPara()
	if text == "" {
		return
	}
	s.structure.Words += len([]rune(text))
	if s.inCell {
		s.cellParas = append(s.cellParas, text)
		return
	}
	s.emitPara(text, style, outline, allBold)
}

// emitPara 把一个正文段落写进 markdown(标题/列表/普通段落)。
// 标题判定优先级:大纲级别 → Heading 样式 → 加粗短行启发式(中文公文手动加粗标题)。
func (s *docxState) emitPara(text, style string, outline int, allBold bool) {
	s.structure.Paragraphs++
	runes := len([]rune(text))
	if outline >= 0 && outline <= 5 {
		s.md.WriteString(strings.Repeat("#", outline+1) + " " + text + "\n\n")
		s.structure.Headings = append(s.structure.Headings, TemplateHeading{Level: outline + 1, Title: text})
		return
	}
	if n, ok := headingFromStyle(style, s.styles); ok {
		s.md.WriteString(strings.Repeat("#", n) + " " + text + "\n\n")
		s.structure.Headings = append(s.structure.Headings, TemplateHeading{Level: n, Title: text})
		return
	}
	if strings.EqualFold(style, "ListParagraph") {
		s.md.WriteString("- " + text + "\n")
		return
	}
	// 启发式:全加粗且 ≤50 字的独立段落 → 视为小标题(中文公文模板的常见写法)。
	if allBold && runes > 0 && runes <= 50 {
		s.md.WriteString("### " + text + "\n\n")
		s.structure.Headings = append(s.structure.Headings, TemplateHeading{Level: 3, Title: text})
		return
	}
	s.md.WriteString(text + "\n\n")
}

func (s *docxState) flushRow() {
	if len(s.rowCells) > 0 {
		s.rows = append(s.rows, "| "+strings.Join(s.rowCells, " | ")+" |")
	}
	s.rowCells = nil
}

func (s *docxState) flushTable() {
	if len(s.rows) == 0 {
		return
	}
	s.structure.Tables++
	// Word 合并单元格会让各行列数不齐 → 补空列到最大列数,保证 markdown 表格渲染不错位。
	maxCols := 0
	for _, r := range s.rows {
		if n := strings.Count(r, "|") - 1; n > maxCols {
			maxCols = n
		}
	}
	fixed := make([]string, 0, len(s.rows))
	for _, r := range s.rows {
		n := strings.Count(r, "|") - 1
		if n < maxCols {
			r = strings.TrimSuffix(r, "|") + strings.Repeat(" |", maxCols-n)
		}
		fixed = append(fixed, r)
	}
	s.md.WriteString("\n" + fixed[0] + "\n")
	if len(fixed) > 1 {
		s.md.WriteString("|" + strings.Repeat(" --- |", maxCols) + "\n")
		s.md.WriteString(strings.Join(fixed[1:], "\n") + "\n")
	} else {
		s.md.WriteString("| --- |\n")
	}
	s.md.WriteString("\n")
	s.rows = nil
}

// parseDocumentXML 流式读 document.xml 并组装 markdown。
// styles 为 styleId→样式名映射(可为 nil):数字 pStyle id 需经它解析成样式名
// 后才能按「标题 N」判定标题级别。
func parseDocumentXML(r io.Reader, styles docxStyleMap) (string, TemplateStructure, error) {
	s := &docxState{outline: -1, styles: styles}
	dec := xml.NewDecoder(r)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", s.structure, fmt.Errorf("document.xml 解析失败: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "p":
				if s.inP {
					s.flushPara()
				}
				s.inP = true
				s.paraStyle = ""
				s.outline = -1
				s.paraAllBold = true
			case "r":
				s.inRun = true
				s.runBold = false
			case "b":
				// w:b = 加粗;w:b w:val="0"/"false" = 取消加粗。
				if s.inRun {
					s.runBold = true
					for _, a := range t.Attr {
						if a.Name.Local == "val" && (a.Value == "0" || a.Value == "false") {
							s.runBold = false
						}
					}
				}
			case "tbl":
				s.flushTable()
				s.inTbl = true
			case "tr":
				s.flushRow()
			case "tc":
				s.flushPara()
				s.inCell = true
				s.cellParas = nil
			case "pStyle":
				for _, a := range t.Attr {
					if a.Name.Local == "val" {
						s.paraStyle = a.Value
					}
				}
			case "outlineLvl":
				for _, a := range t.Attr {
					if a.Name.Local == "val" {
						var n int
						if _, err := fmt.Sscanf(a.Value, "%d", &n); err == nil {
							s.outline = n
						}
					}
				}
			case "t":
				s.inT = true
			case "instrText":
				s.inInstr = true
			case "del":
				s.inDel = true
			case "br":
				if s.inP {
					s.paraText.WriteString("\n")
				}
			case "tab":
				if s.inP {
					s.paraText.WriteString("\t")
				}
			}
		case xml.CharData:
			// 只收集 w:t 的正文;域代码与修订删除不计入。
			if s.inT && !s.inInstr && !s.inDel {
				s.paraText.Write(t)
				// 加粗启发式:任何一段非空白文本未加粗 → 整段不算全加粗。
				if s.inRun && !s.runBold && strings.TrimSpace(string(t)) != "" {
					s.paraAllBold = false
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				s.inT = false
			case "instrText":
				s.inInstr = false
			case "del":
				s.inDel = false
			case "r":
				s.inRun = false
			case "p":
				s.flushPara()
				s.rowCells = append(s.rowCells, strings.Join(s.cellParas, " "))
				s.cellParas = nil
				s.inCell = false
			case "tr":
				s.flushRow()
			case "tbl":
				s.flushPara()
				s.flushTable()
				s.inTbl = false
			}
		}
	}
	s.flushPara()
	s.flushTable()

	out := strings.TrimSpace(s.md.String())
	if out == "" {
		return "", s.structure, fmt.Errorf("docx 未解析到正文内容")
	}
	return out + "\n", s.structure, nil
}

// headingFromStyle 从段落样式推断标题级别,两级判定:
//  1. 样式 id 本身含 HeadingN(英文内置样式 id 直认,兼容无 styles.xml 的裸文档);
//  2. 数字 styleId(如 31/32)经 styles.xml 映射成样式名——名字含「标题 N」/「heading N」
//     (不区分大小写,中英都认,全角括号说明如「标题 1（奇安信）」忽略后缀)→ level=N。
//
// 字符样式(w:type="character")不参与标题判定。outlineLvl 与加粗启发式由调用方
// 保持原有优先级。
func headingFromStyle(style string, styles docxStyleMap) (int, bool) {
	if style == "" {
		return 0, false
	}
	if m := headingStyleRe.FindStringSubmatch(strings.TrimSpace(style)); m != nil {
		return int(m[1][len(m[1])-1] - '0'), true
	}
	if styles != nil {
		if info, ok := styles[style]; ok && info.isParagraph() {
			return headingLevelFromName(info.name)
		}
	}
	return 0, false
}

// docxStyleInfo 是 styles.xml 中一个样式的定性信息。
type docxStyleInfo struct {
	name      string // w:name 样式名
	styleType string // w:type(paragraph/character/table/numbering)
}

func (i docxStyleInfo) isParagraph() bool { return i.styleType == "paragraph" }

// docxStyleMap 是 styleId → 样式信息的映射。
type docxStyleMap map[string]docxStyleInfo

// parseStylesXML 解析 styles.xml 建 styleId→样式名映射(解析失败返回空映射,
// 不阻断文档解析)。注意样式名不一定紧跟在 w:style 开标签后,须遍历子元素取 w:name。
func parseStylesXML(data []byte) docxStyleMap {
	m := docxStyleMap{}
	if len(data) == 0 {
		return m
	}
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(data); err != nil {
		return m
	}
	root := doc.Root()
	if root == nil {
		return m
	}
	for _, st := range root.ChildElements() {
		if st.Tag != "style" {
			continue
		}
		id, name, typ := "", "", ""
		for _, a := range st.Attr {
			switch a.Key {
			case "styleId":
				id = a.Value
			case "type":
				typ = a.Value
			}
		}
		for _, ch := range st.ChildElements() {
			if ch.Tag == "name" {
				name = styleAttrVal(ch, "val")
				break
			}
		}
		if id != "" {
			m[id] = docxStyleInfo{name: name, styleType: typ}
		}
	}
	return m
}

// styleAttrVal 取元素属性值(按 key 匹配,兼容带/不带命名空间前缀两种存放形态)。
func styleAttrVal(el *etree.Element, key string) string {
	for _, a := range el.Attr {
		if a.Key == key {
			return a.Value
		}
	}
	return ""
}

// headingLevelFromName 从样式名判定标题级别:名字含「标题 N」/「heading N」
// (不区分大小写;「标题 2(self)」「标题 1（奇安信）」等后缀忽略)→ N。
func headingLevelFromName(name string) (int, bool) {
	if name == "" {
		return 0, false
	}
	m := headingNameRe.FindStringSubmatch(foldFullwidth(name))
	if m == nil {
		return 0, false
	}
	n := 0
	for _, r := range m[1] {
		n = n*10 + int(r-'0')
	}
	if n <= 0 {
		n = 1 // 「标题 0」视作一级
	}
	if n > 9 {
		n = 9
	}
	return n, true
}

// headingStyleRe 兜底匹配样式 id 本身含 HeadingN 的写法(英文内置样式 id 直认,
// 覆盖无 styles.xml 的裸文档;数字 styleId 走 styles.xml 名字映射)。
var headingStyleRe = regexp.MustCompile(`(?i)^heading\s*([1-6])$`)

// headingNameRe 匹配样式名中的标题级别:「标题 1」「标题2」「heading 3」「Heading 3」。
// 只匹配「标题/heading + 数字」,避免「封皮主标题」这类仅含「标题」字样的样式误判。
var headingNameRe = regexp.MustCompile(`(?i)(?:heading|标题)\s*([0-9]+)`)

// foldFullwidth 把全角 ASCII 区(U+FF01–U+FF5E)折为半角、全角空格折为半角空格。
// 样式名/匹配文本里可能混用全角数字或全角括号,折叠后统一按半角处理。
func foldFullwidth(s string) string {
	rs := []rune(s)
	for i, r := range rs {
		switch {
		case r >= 0xFF01 && r <= 0xFF5E:
			rs[i] = r - 0xFEE0
		case r == 0x3000:
			rs[i] = ' '
		}
	}
	return string(rs)
}
