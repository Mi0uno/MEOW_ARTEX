package report

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/beevik/etree"
)

// ---- 报告导出格式保真:按章节填充原始 docx 外壳 ----
//
// 原理与 FillDocx(变量标记替换)互补:AI 导出走 FillDocxSections,把逐章生成的
// 正文填进模板原始 docx 包——页眉页脚/封面/logo/字体/配色天然保留,只按章节
// 替换 body 正文。styles.xml/header*/footer*/media 等包部件原字节不重写。
//
// 契约要点(spec §2):
//   - 标题段分类:pStyle styleId 经 styles.xml 解析成样式名(「标题 N」/「heading N」
//     中英都认,数字 styleId 只有经映射才能定性)→ 手打编号模式 → 加粗短行启发式;
//     目录条目(编号+文字+尾随页码数字/制表符前导点)不作为标题。
//   - 三级匹配:归一化精确 → 前缀 → 顺序对齐兜底(①②命中率 <50% 时整体启用)。
//   - 节替换:标题段之后到下一个同级或更高级标题段(或 sectPr)之间全部删除,
//     插入由该节 markdown 构建的样式化元素;基底样式取被删范围内第一个正文段的
//     pPr 与其首个 run 的 rPr,插入内容继承字体/字号/颜色/缩进,粗体只覆盖 w:b。
//   - 未匹配的输入节按序追加到 sectPr 前,内容永不丢。
//
// 边界语义说明:删除范围取「下一个同级或更高级标题」而非「下一个任意标题」——
// 章节标题(一级)下的二级/三级小节属于该章内容,必须一并替换;若任意标题都
// 截断,「4.1.1 漏洞A」这类小节样例正文会残留在旧章节里。

// TemplateSectionFill 是一个待填充章节:标题与该节成文 markdown。
type TemplateSectionFill struct {
	Heading string // 章节标题(与外壳标题段的可见文本匹配;可带「三.」/「3.1」等编号前缀)
	Body    string // 该节成文 markdown(段落/**粗体**/行内码/- 列表/1. 列表/> 引用/```代码```/md 表格)
}

// SectionFillOptions 是 FillDocxSections 的填充数据集。
type SectionFillOptions struct {
	Inline      map[string]string     // 全文 inline 标记替换({{title}}/{{date}}/{{summary}}…),复用现有跨 run 替换语义
	TableLabels map[string]string     // 表格标签填值:某单元格归一化文本==key → 同行下一格填 value(保留该格原有 run 格式)
	Sections    []TemplateSectionFill // 待填充章节
}

// FillDocxSections 读取 origPath 的 docx,按节替换正文,返回新 docx 字节。
// 外壳只改 body 章节正文;header/footer/media/styles.xml 等包部件原字节保留。
func FillDocxSections(origPath string, opts SectionFillOptions) ([]byte, error) {
	orig, err := os.ReadFile(origPath)
	if err != nil {
		return nil, fmt.Errorf("读取模板原始 docx 失败: %w", err)
	}
	return fillDocxSections(orig, opts)
}

// fillDocxSections 是 FillDocxSections 的字节级实现(便于测试)。
func fillDocxSections(orig []byte, opts SectionFillOptions) ([]byte, error) {
	docXML, parts, err := readDocxParts(orig)
	if err != nil {
		return nil, err
	}
	// styles.xml 只读解析成映射,不重写该部件——外壳包部件原字节保留。
	var stylesRaw []byte
	for _, p := range parts {
		if p.name == "word/styles.xml" {
			stylesRaw = p.body
			break
		}
	}
	styles := parseStylesXML(stylesRaw)

	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(docXML); err != nil {
		return nil, fmt.Errorf("document.xml 解析失败: %w", err)
	}
	root := doc.Root()
	if root == nil {
		return nil, fmt.Errorf("document.xml 无根元素")
	}

	// 1) 全文行内标记替换(允许跨 run,替换文本继承标记起点格式)。
	if len(opts.Inline) > 0 {
		var walk func(el *etree.Element)
		walk = func(el *etree.Element) {
			if isW(el, "p") {
				replaceInlineInPara(el, opts.Inline)
			}
			for _, ch := range el.ChildElements() {
				walk(ch)
			}
		}
		walk(root)
	}

	// 2) 表格标签填值(文档信息表等)。
	applyTableLabels(root, opts.TableLabels)

	// 3) 章节正文替换 + 未匹配节追加。
	body := findWChild(root, "body")
	if body == nil {
		body = root
	}
	fillSectionsInBody(body, opts.Sections, styles)

	out, err := doc.WriteToBytes()
	if err != nil {
		return nil, err
	}
	return writeDocxParts(parts, out)
}

// ---- docx 包部件读写(与 FillDocx 共用的打包设施) ----

// docxPart 是 docx 包内的一个部件(保留原始压缩方式与字节)。
type docxPart struct {
	name   string
	method uint16
	body   []byte
}

// readDocxParts 读出 docx 包全部部件;word/document.xml 单独返回。
func readDocxParts(orig []byte) (docXML []byte, parts []docxPart, err error) {
	zr, err := zip.NewReader(bytes.NewReader(orig), int64(len(orig)))
	if err != nil {
		return nil, nil, fmt.Errorf("不是有效的 docx(zip) 文件: %w", err)
	}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return nil, nil, err
		}
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, rc); err != nil {
			rc.Close()
			return nil, nil, err
		}
		rc.Close()
		if f.Name == "word/document.xml" {
			docXML = buf.Bytes()
		}
		parts = append(parts, docxPart{name: f.Name, method: f.Method, body: buf.Bytes()})
	}
	if docXML == nil {
		return nil, nil, fmt.Errorf("docx 缺少 word/document.xml")
	}
	return docXML, parts, nil
}

// writeDocxParts 按原部件顺序重新打包;document.xml 用新字节(压缩方式固定
// Deflate),其余部件原字节原压缩方式直通,保证 styles/header/footer/media 不动。
func writeDocxParts(parts []docxPart, newDocXML []byte) ([]byte, error) {
	out := &bytes.Buffer{}
	zw := zip.NewWriter(out)
	for _, f := range parts {
		body, method := f.body, f.method
		if f.name == "word/document.xml" {
			body, method = newDocXML, zip.Deflate
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

// ---- 标题段分类 ----

// numberingRe 前缀系:中文数字编号(「三.」「四、」)与多级阿拉伯编号(「3.1」「4.1.1」,
// 编号后须跟分隔符,排除「10.9.18.205」这类 IP 文本)。
var (
	cnNumberingRe     = regexp.MustCompile(`^[一二三四五六七八九十百]+[.、．]`)
	arabicNumberingRe = regexp.MustCompile(`^\d+(?:\.\d+)+[、.．\s]`)
)

// shellHeading 是一个已分类的外壳标题段。
type shellHeading struct {
	el    *etree.Element
	level int
	norm  string // 归一化可见文本(去空白/全半角折叠/小写)
}

// classifyShellHeading 对 body 直接子级段落做标题分类,返回 (级别, 归一化文本)。
// 优先级:① pStyle 经 styles.xml 映射成样式名(中英都认)→ ② TOC 守卫拦下目录
// 条目 → ③ 手打编号模式(按编号深度定级)→ ④ 加粗短行启发式。
func classifyShellHeading(p *etree.Element, styles docxStyleMap) (int, string) {
	text := strings.TrimSpace(paraFullText(p))
	if text == "" {
		return 0, ""
	}
	// ① pStyle:数字 styleId 只有经 styles.xml 才能定性(如 31→「标题 1（奇安信）」);
	//   英文 HeadingN 样式 id 在映射缺失时也能直认。
	if ppr := findWChild(p, "pPr"); ppr != nil {
		if ps := findWChild(ppr, "pStyle"); ps != nil {
			if id := styleAttrVal(ps, "val"); id != "" {
				if n, ok := headingFromStyle(id, styles); ok {
					return n, normText(text)
				}
			}
		}
	}
	// ② TOC 守卫:目录条目不是标题(否则会污染章节边界与匹配目标)。
	if isTocEntry(p, text) {
		return 0, ""
	}
	runes := len([]rune(text))
	// ③ 手打编号标题:「三. 项目概述」→ 一级;「4.1.1 漏洞」→ 三级;长文不算。
	if d := numberingDepth(text); d > 0 && runes <= 50 {
		return d, normText(text)
	}
	// ④ 加粗短行启发式(中文模板手动加粗当标题)。
	if runes <= 50 && isParaAllBold(p) {
		return 3, normText(text)
	}
	return 0, ""
}

// numberingDepth 由编号前缀推标题级别:中文编号(「三.」)为 1 级;阿拉伯多级
// 编号按点分深度定级(「3.1」→2,「4.1.1」→3)。
func numberingDepth(text string) int {
	if cnNumberingRe.MatchString(text) {
		return 1
	}
	if m := arabicNumberingRe.FindString(text); m != "" {
		return 1 + strings.Count(m, ".")
	}
	return 0
}

// isTocEntry 判断段落是否目录条目:原文=编号+文字+尾随页码数字(如「一. 摘要4」)
// 或含制表符前导点/正文制表符接页码的段落,不作为标题。
func isTocEntry(p *etree.Element, text string) bool {
	// 点线制表位(dot leader)是目录条目的标志性排版。
	if ppr := findWChild(p, "pPr"); ppr != nil {
		if tabs := findWChild(ppr, "tabs"); tabs != nil {
			for _, tb := range tabs.ChildElements() {
				if isW(tb, "tab") && styleAttrVal(tb, "leader") == "dot" {
					return true
				}
			}
		}
	}
	t := strings.TrimRight(text, " \t")
	if !hasTrailingPageDigits(t) {
		return false
	}
	// 尾随 1-3 位页码 + (编号前缀 或 正文制表符) → 目录条目(「一. 摘要4」「摘要<TAB>4」)。
	return numberingDepth(t) > 0 || paraHasTabRun(p)
}

// hasTrailingPageDigits 判断文本是否以 1-3 位页码数字结尾且数字前还有内容。
func hasTrailingPageDigits(t string) bool {
	rs := []rune(foldFullwidth(t))
	n := 0
	for n < len(rs) {
		c := rs[len(rs)-1-n]
		if c < '0' || c > '9' {
			break
		}
		n++
	}
	return n > 0 && n <= 3 && len(rs) > n
}

// paraHasTabRun 判断段落是否含正文制表符(pPr 里的 tabs 是制表位定义,不算)。
func paraHasTabRun(p *etree.Element) bool {
	var walk func(el *etree.Element) bool
	walk = func(el *etree.Element) bool {
		for _, ch := range el.ChildElements() {
			if isW(ch, "pPr") {
				continue
			}
			if isW(ch, "tab") {
				return true
			}
			if walk(ch) {
				return true
			}
		}
		return false
	}
	return walk(p)
}

// isParaAllBold 判断段落内所有带文本的 run 是否全部加粗(加粗短行启发式)。
func isParaAllBold(p *etree.Element) bool {
	any := false
	var walk func(el *etree.Element) bool
	walk = func(el *etree.Element) bool {
		for _, ch := range el.ChildElements() {
			switch {
			case isW(ch, "pPr"), isW(ch, "rPr"):
				continue
			case isW(ch, "r"):
				if strings.TrimSpace(runFullText(ch)) == "" {
					continue
				}
				any = true
				rpr := findWChild(ch, "rPr")
				if rpr == nil {
					return false
				}
				b := findWChild(rpr, "b")
				if b == nil {
					return false
				}
				if v := styleAttrVal(b, "val"); v == "0" || v == "false" {
					return false
				}
			default:
				if !walk(ch) {
					return false
				}
			}
		}
		return true
	}
	if !walk(p) {
		return false
	}
	return any
}

// runFullText 拼 run 内全部 w:t 文本。
func runFullText(r *etree.Element) string {
	var sb strings.Builder
	var walk func(el *etree.Element)
	walk = func(el *etree.Element) {
		for _, ch := range el.ChildElements() {
			if isW(ch, "t") {
				sb.WriteString(ch.Text())
			}
			walk(ch)
		}
	}
	walk(r)
	return sb.String()
}

// ---- 文本归一化与匹配 ----

// normText 匹配归一化:全半角折叠 → 去全部空白 → 小写。
func normText(s string) string {
	var b strings.Builder
	for _, r := range foldFullwidth(s) {
		switch r {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			continue
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}

// stripNumberingNorm 从归一化文本剥去行首编号得核心文本(「三.项目概述」→「项目概述」)。
func stripNumberingNorm(norm string) string {
	if m := cnNumberingRe.FindStringIndex(norm); m != nil {
		return norm[m[1]:]
	}
	if m := arabicNumberingRe.FindStringIndex(norm); m != nil {
		return norm[m[1]:]
	}
	return norm
}

// matchSectionsToHeads 三级匹配输入节与外壳标题段,返回每个输入节命中的标题
// 下标(-1 = 未命中):
//   - ① 外壳段归一化文本==核心(剥编号后相等也算,兼容正文标题自带编号);
//   - ② 外壳段以核心开头;
//   - ③ 顺序对齐兜底:第 i 个输入节 ↔ 第 i 个外壳标题,仅当①②命中率 <50% 时整体启用。
//
// 已命中的标题段不重复分配。目录条目不参与(分类阶段已被 TOC 守卫排除)。
func matchSectionsToHeads(sections []TemplateSectionFill, heads []*shellHeading) []int {
	assign := make([]int, len(sections))
	used := make([]bool, len(heads))
	hits := 0
	for i, sec := range sections {
		assign[i] = -1
		core := stripNumberingNorm(normText(sec.Heading))
		if core == "" {
			continue
		}
		for j, h := range heads {
			if used[j] {
				continue
			}
			if h.norm == core || stripNumberingNorm(h.norm) == core {
				assign[i], used[j] = j, true
				hits++
				break
			}
		}
		if assign[i] >= 0 {
			continue
		}
		for j, h := range heads {
			if used[j] {
				continue
			}
			if strings.HasPrefix(h.norm, core) {
				assign[i], used[j] = j, true
				hits++
				break
			}
		}
	}
	if len(sections) > 0 && 2*hits < len(sections) && len(heads) > 0 {
		// 命中率不足一半:文本匹配不可信,整体切换为顺序对齐。
		used = make([]bool, len(heads))
		for i := range assign {
			if i < len(heads) {
				assign[i], used[i] = i, true
			} else {
				assign[i] = -1
			}
		}
	}
	return assign
}

// ---- 章节替换 ----

// fillSectionsInBody 在 body 上完成章节匹配、替换与未匹配节追加。
func fillSectionsInBody(body *etree.Element, sections []TemplateSectionFill, styles docxStyleMap) {
	// 标题段分类(只扫 body 直接子级段落;表格内段落不是章节边界)。
	var heads []*shellHeading
	levelOf := map[*etree.Element]int{}
	for _, ch := range body.ChildElements() {
		if !isW(ch, "p") {
			continue
		}
		if lvl, norm := classifyShellHeading(ch, styles); lvl > 0 {
			levelOf[ch] = lvl
			heads = append(heads, &shellHeading{el: ch, level: lvl, norm: norm})
		}
	}
	if len(sections) == 0 {
		return
	}
	assign := matchSectionsToHeads(sections, heads)
	for i, sec := range sections {
		if j := assign[i]; j >= 0 {
			h := heads[j]
			replaceSectionRange(body, h.el, h.level, levelOf, sec.Body)
		}
	}
	// 未匹配的输入节按序追加到 sectPr 前(用文档中出现最多的正文段样式),内容永不丢。
	var pending []TemplateSectionFill
	for i, sec := range sections {
		if assign[i] < 0 {
			pending = append(pending, sec)
		}
	}
	if len(pending) > 0 {
		base := dominantBodyBase(body, levelOf)
		idx := len(body.Child)
		for _, ch := range body.ChildElements() {
			if isW(ch, "sectPr") {
				idx = childIndex(body, ch)
				break
			}
		}
		for _, sec := range pending {
			for _, el := range buildSectionElements(sec.Body, base) {
				body.InsertChildAt(idx, el)
				idx++
			}
		}
	}
}

// replaceSectionRange 删除标题段之后到下一个同级或更高级标题段(或 sectPr、或
// 段内分节符)之间的全部元素,插入由 bodyMD 构建的样式化元素;标题段本身不动。
func replaceSectionRange(body *etree.Element, headingEl *etree.Element, level int, levelOf map[*etree.Element]int, bodyMD string) {
	ch := body.ChildElements()
	start := childIndex(body, headingEl)
	if start < 0 {
		return
	}
	end := len(ch)
	for i := start + 1; i < len(ch); i++ {
		el := ch[i]
		if isW(el, "sectPr") {
			end = i
			break
		}
		// 段内嵌 sectPr(分节符段落)同样是边界,且该段不可删除(承载页面布局)。
		if isW(el, "p") && paraHasSectPr(el) {
			end = i
			break
		}
		if lvl, ok := levelOf[el]; ok && lvl <= level {
			end = i
			break
		}
	}
	// 样式捕获:被删范围内第一个正文段(非标题段)的 pPr + 首 run rPr 为基底;
	// 区间以表格开头时退而取表格内第一个段落;都没有则用文档 Normal(空基底)。
	var base docxBaseStyle
	for i := start + 1; i < end; i++ {
		el := ch[i]
		if !isW(el, "p") {
			continue
		}
		if _, isH := levelOf[el]; isH {
			continue
		}
		base = captureParaStyle(el)
		break
	}
	if base.empty() {
		for i := start + 1; i < end; i++ {
			if p := firstNestedPara(ch[i]); p != nil {
				base = captureParaStyle(p)
				break
			}
		}
	}
	for i := start + 1; i < end; i++ {
		body.RemoveChild(ch[i])
	}
	ins := start + 1
	for _, el := range buildSectionElements(bodyMD, base) {
		body.InsertChildAt(ins, el)
		ins++
	}
}

// paraHasSectPr 判断段落属性里是否内嵌分节符(该段承载页面布局,不可当正文删)。
func paraHasSectPr(p *etree.Element) bool {
	ppr := findWChild(p, "pPr")
	return ppr != nil && findWChild(ppr, "sectPr") != nil
}

// dominantBodyBase 投票选出文档中出现最多的正文段样式(目录条目与标题段不参与),
// 返回其首个段落的基底样式;全文无正文段时返回空基底(Normal)。
func dominantBodyBase(body *etree.Element, levelOf map[*etree.Element]int) docxBaseStyle {
	vote := map[string]int{}
	var order []string
	for _, ch := range body.ChildElements() {
		if !isW(ch, "p") {
			continue
		}
		if _, isH := levelOf[ch]; isH {
			continue
		}
		text := strings.TrimSpace(paraFullText(ch))
		if text == "" || isTocEntry(ch, text) {
			continue
		}
		id := paraStyleID(ch)
		if _, seen := vote[id]; !seen {
			order = append(order, id)
		}
		vote[id]++
	}
	best, bestN := "", -1
	for _, id := range order {
		if vote[id] > bestN {
			best, bestN = id, vote[id]
		}
	}
	for _, ch := range body.ChildElements() {
		if !isW(ch, "p") {
			continue
		}
		if _, isH := levelOf[ch]; isH {
			continue
		}
		if paraStyleID(ch) == best {
			return captureParaStyle(ch)
		}
	}
	return docxBaseStyle{}
}

// paraStyleID 取段落 pStyle 的 styleId(无则空串)。
func paraStyleID(p *etree.Element) string {
	ppr := findWChild(p, "pPr")
	if ppr == nil {
		return ""
	}
	ps := findWChild(ppr, "pStyle")
	if ps == nil {
		return ""
	}
	return styleAttrVal(ps, "val")
}

// ---- 表格标签填值 ----

// applyTableLabels 遍历全部表格:单元格归一化文本精确==key → 同行右邻格填 value。
func applyTableLabels(root *etree.Element, labels map[string]string) {
	if len(labels) == 0 {
		return
	}
	norm := make(map[string]string, len(labels))
	for k, v := range labels {
		norm[normText(k)] = v
	}
	var walk func(el *etree.Element)
	walk = func(el *etree.Element) {
		for _, ch := range el.ChildElements() {
			if isW(ch, "tbl") {
				fillTableLabels(ch, norm)
			}
			walk(ch)
		}
	}
	walk(root)
}

func fillTableLabels(tbl *etree.Element, norm map[string]string) {
	for _, tr := range tbl.ChildElements() {
		if !isW(tr, "tr") {
			continue
		}
		var tcs []*etree.Element
		for _, ch := range tr.ChildElements() {
			if isW(ch, "tc") {
				tcs = append(tcs, ch)
			}
		}
		for i, tc := range tcs {
			val, ok := norm[normText(cellFullText(tc))]
			if !ok || i+1 >= len(tcs) {
				continue
			}
			setCellText(tcs[i+1], val)
		}
	}
}

func cellFullText(tc *etree.Element) string {
	var sb strings.Builder
	for _, ch := range tc.ChildElements() {
		if isW(ch, "p") {
			sb.WriteString(paraFullText(ch))
		}
	}
	return sb.String()
}

// setCellText 改写单元格文本:保留首 run 的 rPr(格式不变),改首 run 文本,
// 多余 run 与多余段落删除(避免残留旧文本)。
func setCellText(tc *etree.Element, val string) {
	var paras []*etree.Element
	for _, ch := range tc.ChildElements() {
		if isW(ch, "p") {
			paras = append(paras, ch)
		}
	}
	if len(paras) == 0 {
		paras = append(paras, tc.CreateElement("w:p"))
	}
	p := paras[0]
	var runs []*etree.Element
	for _, ch := range p.ChildElements() {
		if isW(ch, "r") {
			runs = append(runs, ch)
		}
	}
	if len(runs) == 0 {
		runs = append(runs, p.CreateElement("w:r"))
	}
	first := runs[0]
	for _, ch := range first.ChildElements() {
		if !isW(ch, "rPr") {
			first.RemoveChild(ch)
		}
	}
	t := first.CreateElement("w:t")
	t.CreateAttr("xml:space", "preserve")
	t.SetText(val)
	for _, ch := range p.ChildElements() {
		if ch == first {
			continue
		}
		if isW(ch, "r") || isW(ch, "hyperlink") {
			p.RemoveChild(ch)
		}
	}
	for _, extra := range paras[1:] {
		tc.RemoveChild(extra)
	}
}

// ---- markdown 子集 → 样式化元素构建 ----

// docxBaseStyle 是插入内容的基底样式:段落属性 + 字符属性模板(可为 nil=文档 Normal)。
type docxBaseStyle struct {
	ppr *etree.Element
	rpr *etree.Element
}

func (b docxBaseStyle) empty() bool { return b.ppr == nil && b.rpr == nil }

// captureParaStyle 捕获段落基底样式:pPr 与第一个带 rPr 的 run 的 rPr。
// 插入内容继承其字体/字号/颜色/缩进;粗体只在此基础上覆盖 w:b。
func captureParaStyle(p *etree.Element) docxBaseStyle {
	var base docxBaseStyle
	base.ppr = findWChild(p, "pPr")
	var firstRpr func(el *etree.Element) *etree.Element
	firstRpr = func(el *etree.Element) *etree.Element {
		for _, ch := range el.ChildElements() {
			if isW(ch, "rPr") {
				continue
			}
			if isW(ch, "r") {
				if rpr := findWChild(ch, "rPr"); rpr != nil {
					return rpr
				}
				continue
			}
			if rpr := firstRpr(ch); rpr != nil {
				return rpr
			}
		}
		return nil
	}
	base.rpr = firstRpr(p)
	return base
}

// firstNestedPara 取元素子树里第一个段落(区间以表格开头时的样式捕获来源)。
func firstNestedPara(el *etree.Element) *etree.Element {
	if isW(el, "p") {
		return el
	}
	for _, ch := range el.ChildElements() {
		if p := firstNestedPara(ch); p != nil {
			return p
		}
	}
	return nil
}

// paraKind 是构建段落的形态。
type paraKind int

const (
	paraPlain   paraKind = iota // 普通段落
	paraHeading                 // 标题行:全部 run 加粗(剥 # 记号)
	paraQuote                   // 引用块:左缩进
	paraList                    // 列表:保留文本前缀 + 左缩进
	paraCode                    // 代码行:Consolas + 略小字号
)

// buildSectionElements 把一节 markdown(段落/粗体/行内码/列表/引用/代码块/md 表格)
// 构建成样式化元素序列;空基底(无样式可捕获)时元素不带继承属性,行为退回现状。
func buildSectionElements(md string, base docxBaseStyle) []*etree.Element {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var out []*etree.Element
	var tableBuf []string
	flushTable := func() {
		if len(tableBuf) == 0 {
			return
		}
		if rows := mdTableRows(tableBuf); rows != nil {
			out = append(out, newStyledTable(base, rows))
		}
		tableBuf = nil
	}
	inCode := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") {
			flushTable()
			inCode = !inCode
			continue
		}
		if inCode {
			// 代码块逐行原样保留(含缩进空格),不丢行。
			out = append(out, newStyledPara(base, paraCode, line))
			continue
		}
		if strings.HasPrefix(t, "|") {
			tableBuf = append(tableBuf, t)
			continue
		}
		flushTable()
		switch {
		case t == "":
			// 空行只分隔段落,段落间距由外壳样式提供,不产出空元素。
		case strings.HasPrefix(t, "#"):
			out = append(out, newStyledPara(base, paraHeading, t))
		case strings.HasPrefix(t, ">"):
			out = append(out, newStyledPara(base, paraQuote, strings.TrimSpace(strings.TrimPrefix(t, ">"))))
		case strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* "):
			out = append(out, newStyledPara(base, paraList, t)) // 保留文本前缀
		case orderedListRe.MatchString(t):
			out = append(out, newStyledPara(base, paraList, t))
		default:
			out = append(out, newStyledPara(base, paraPlain, t))
		}
	}
	flushTable()
	return out
}

// newStyledPara 构建一个段落:继承基底 pPr/rPr,粗体只覆盖 w:b,行内码用
// Consolas+略小字号;无基底时退回无样式段落。
func newStyledPara(base docxBaseStyle, kind paraKind, text string) *etree.Element {
	p := etree.NewElement("w:p")
	if kind == paraHeading {
		text = stripHeadingMark(text)
	}
	if base.ppr != nil {
		ppr := base.ppr.Copy()
		// 列表前缀以文本保留,继承来的自动编号要去掉,避免双重编号。
		for _, ch := range ppr.ChildElements() {
			if isW(ch, "numPr") {
				ppr.RemoveChild(ch)
			}
		}
		switch kind {
		case paraQuote:
			setParaIndent(ppr, "420")
		case paraList:
			setParaIndent(ppr, "360")
		}
		p.AddChild(ppr)
	}
	var runs []mdRun
	if kind == paraCode {
		runs = []mdRun{{text: text, code: true}}
	} else {
		runs = splitInlineRuns(text)
		if kind == paraHeading {
			for i := range runs {
				runs[i].bold = true
			}
		}
	}
	if len(runs) == 0 {
		runs = []mdRun{{text: ""}} // 空段落保行结构(空行场景)
	}
	for _, r := range runs {
		re := p.CreateElement("w:r")
		if rpr := buildRunRPr(base.rpr, r.bold, r.code); rpr != nil {
			re.AddChild(rpr)
		}
		te := re.CreateElement("w:t")
		te.CreateAttr("xml:space", "preserve")
		te.SetText(r.text)
	}
	return p
}

// buildRunRPr 由基底 rPr 派生 run 字符属性:粗体只覆盖 w:b(其余字体/字号/颜色
// 继承基底);代码 run 换 Consolas 并略缩字号。无需附加时返回 nil。
func buildRunRPr(base *etree.Element, bold, code bool) *etree.Element {
	if base == nil && !bold && !code {
		return nil
	}
	var rpr *etree.Element
	if base != nil {
		rpr = base.Copy()
	} else {
		rpr = etree.NewElement("w:rPr")
	}
	if bold {
		ensureBold(rpr)
	}
	if code {
		rf := findWChild(rpr, "rFonts")
		if rf == nil {
			rf = etree.NewElement("w:rFonts")
			rpr.InsertChildAt(0, rf)
		}
		setAttr(rf, "w:ascii", "Consolas")
		setAttr(rf, "w:hAnsi", "Consolas")
		sz := findWChild(rpr, "sz")
		if sz == nil {
			sz = etree.NewElement("w:sz")
			rpr.AddChild(sz)
		}
		n := 0
		fmt.Sscanf(styleAttrVal(sz, "val"), "%d", &n)
		if n <= 0 {
			n = 21 // 无基底字号时按正文 10.5pt 估
		}
		if small := n - 2; small >= 12 {
			n = small
		}
		setAttr(sz, "w:val", itoa(n))
		szcs := findWChild(rpr, "szCs")
		if szcs == nil {
			szcs = rpr.CreateElement("w:szCs")
		}
		setAttr(szcs, "w:val", itoa(n))
	}
	if len(rpr.ChildElements()) == 0 {
		return nil
	}
	return rpr
}

// ensureBold 在 rPr 上就地确保加粗:已有 w:b 清掉 w:val="0",没有则补一个
// (紧跟 w:rFonts 之后,贴近 Word 的 rPr 子元素顺序)。粗体只覆盖 w:b,
// 其余字体/字号/颜色保持不动。
func ensureBold(rpr *etree.Element) {
	b := findWChild(rpr, "b")
	if b == nil {
		b = etree.NewElement("w:b")
		pos := 0
		for i, ch := range rpr.ChildElements() {
			if isW(ch, "rFonts") {
				pos = i + 1
				break
			}
		}
		rpr.InsertChildAt(pos, b)
	}
	removeAttr(b, "val")
}

// forceBoldPara 给段落全部 run 强制加粗(表格表头行用)。
func forceBoldPara(p *etree.Element) {
	for _, r := range p.ChildElements() {
		if !isW(r, "r") {
			continue
		}
		rpr := findWChild(r, "rPr")
		if rpr == nil {
			rpr = etree.NewElement("w:rPr")
			r.InsertChildAt(0, rpr)
		}
		ensureBold(rpr)
	}
}

// newStyledTable 构建带边框表格:单元格文字用基底 run 样式(不带入正文段落属性,
// 避免继承缩进/编号),表头行加粗。
func newStyledTable(base docxBaseStyle, rows [][]string) *etree.Element {
	tbl := etree.NewElement("w:tbl")
	pr := tbl.CreateElement("w:tblPr")
	borders := pr.CreateElement("w:tblBorders")
	for _, side := range []string{"top", "left", "bottom", "right", "insideH", "insideV"} {
		b := borders.CreateElement("w:" + side)
		setAttr(b, "w:val", "single")
		setAttr(b, "w:sz", "4")
	}
	for ri, row := range rows {
		tr := tbl.CreateElement("w:tr")
		for _, cell := range row {
			tc := tr.CreateElement("w:tc")
			tcPr := tc.CreateElement("w:tcPr")
			tcW := tcPr.CreateElement("w:tcW")
			setAttr(tcW, "w:w", "0")
			setAttr(tcW, "w:type", "auto")
			p := newStyledPara(docxBaseStyle{rpr: base.rpr}, paraPlain, cell)
			if ri == 0 {
				forceBoldPara(p)
			}
			tc.AddChild(p)
		}
	}
	return tbl
}

// setParaIndent 在段落属性上设置左缩进(覆盖已有 w:ind;引用/列表用)。
func setParaIndent(ppr *etree.Element, left string) {
	ind := findWChild(ppr, "ind")
	if ind == nil {
		ind = ppr.CreateElement("w:ind")
	}
	setAttr(ind, "w:left", left)
}

// setAttr 设置属性值(兼容带/不带前缀的存放形态;不存在则创建)。
func setAttr(el *etree.Element, key, val string) {
	for i := range el.Attr {
		if a := el.Attr[i]; a.Key == key || (a.Space != "" && a.Space+":"+a.Key == key) {
			el.Attr[i].Value = val
			return
		}
	}
	el.CreateAttr(key, val)
}

// removeAttr 删除属性(粗体覆盖时清掉 w:b 的 w:val="0")。
func removeAttr(el *etree.Element, key string) {
	for i := range el.Attr {
		if a := el.Attr[i]; a.Key == key || (a.Space != "" && a.Space+":"+a.Key == key) {
			el.Attr = append(el.Attr[:i], el.Attr[i+1:]...)
			return
		}
	}
}
