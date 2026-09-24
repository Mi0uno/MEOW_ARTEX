package report

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strings"
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
	var docFile *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			docFile = f
			break
		}
	}
	if docFile == nil {
		return "", structure, fmt.Errorf("docx 缺少 word/document.xml")
	}
	rc, err := docFile.Open()
	if err != nil {
		return "", structure, err
	}
	defer rc.Close()
	return parseDocumentXML(rc)
}

// docxState 是 document.xml 的流式解析状态。
type docxState struct {
	md        strings.Builder
	structure TemplateStructure

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
	if n, ok := headingFromStyle(style); ok {
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
func parseDocumentXML(r io.Reader) (string, TemplateStructure, error) {
	s := &docxState{outline: -1}
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

// headingFromStyle 从样式 id 推断标题级别:Heading1-6(各语言 Word 的内置标题样式 id 均含 HeadingN)。
var headingStyleRe = regexp.MustCompile(`(?i)^heading\s*([1-6])$`)

func headingFromStyle(style string) (int, bool) {
	if style == "" {
		return 0, false
	}
	if m := headingStyleRe.FindStringSubmatch(strings.TrimSpace(style)); m != nil {
		return int(m[1][0] - '0'), true
	}
	return 0, false
}
