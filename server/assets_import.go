package server

// 资产文件导入:POST /api/assets/import(multipart/form-data)。
//
// 契约要点(前后端已冻结,字段名不许改):
//   - 字段 file 必填,扩展名 .txt/.csv/.xlsx,≤10MB,≤20000 数据行;
//   - 字段 task_id 可选(数字字符串),>0 时每条成功资产登记任务侧来源
//     SetTaskAssetSource(taskID, id, "import", "文件导入 · <原文件名>", nil);
//   - 字段 company_id 可选(数字字符串),>0 时本次导入的全部资产(含 db 层
//     连带注册的根域名/子域名/IP side effect)强制归属该企业——跳过按范围的
//     自动认领;企业不存在返回 400;<=0 或缺省行为完全不变(自动认领);
//   - 成功 200 返回 {total,imported,failed,skipped,results,errors},其中
//     total=imported+failed(进了 upsert 的行),skipped=空行/注释行以外
//     无法识别的行;results/errors 数组各最多存 200 条,真实计数看统计字段;
//   - 400(无文件/扩展名不支持/超 10MB/超行数/企业不存在)返回前必须排空
//     请求体,否则 keep-alive 连接上浏览器会把响应当网络错误报 Failed to fetch;
//   - 503:assetStore 为 nil。
//
// 落库映射与 recon_api.go 的 adoptStagedAsset 完全同语义:domain→UpsertSubdomain
// (根域名/企业归属由 store 内部处理),ip→UpsertIP,service→extra.url 非空走
// UpsertHTTPService,否则按 net.ParseIP 走 UpsertOtherService。单条失败只记
// errors 不中断整批。

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/recon"
	"github.com/xuri/excelize/v2"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// 上传上限(与前端约定的阈值,不许改)。
const (
	maxImportFileBytes = 10 << 20 // 单个文件 ≤ 10MB
	maxImportDataRows  = 20000    // 数据行 ≤ 20000
	maxImportReports   = 200      // results/errors 数组各最多保留 200 条
)

// errImportRowLimit 数据行超上限——整个请求按 400 拒绝,不做部分导入。
var errImportRowLimit = errors.New("数据行超过 20000 行上限")

// parsedLine 是解析器产出的统一中间结构:txt/csv/xlsx 三种来源都收敛成它,
// 后面的分类+upsert 循环只面对这一种形状,避免每个分支各写一遍落库逻辑。
type parsedLine struct {
	row   int    // 1-based:txt=物理行号;csv/xlsx=含表头行
	text  string // 交给 recon.ClassifyLine 的目标文本(表头模式下是选中的目标格)
	port  int    // 表头模式下从端口列补上的端口(text 本身没带端口时)
	title string // 表头模式下标题列的值,仅用于 http service 的 PageTitle
}

// importIssue 是解析阶段发现的问题行(无法识别),最终进响应的 errors 数组。
type importIssue struct {
	row   int
	value string
	err   string
}

// importResult / importErrorEntry 是响应数组条目,字段名与前端契约精确一致。
type importResult struct {
	Row   int    `json:"row"`
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

type importErrorEntry struct {
	Row   int    `json:"row"`
	Value string `json:"value"`
	Error string `json:"error"`
}

// importAssets POST /api/assets/import — 资产文件导入。
// 与 POST /api/assets(JSON 批量登记)同风格:单条失败只记 errors 不中断整批。
func (s *Server) importAssets(w http.ResponseWriter, r *http.Request) {
	as := s.assetStore()
	if as == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	// multipart 整体比文件本体略大,MaxBytesReader 留足余量,真正的 10MB
	// 上限仍以文件字节长度为准。
	r.Body = http.MaxBytesReader(w, r.Body, maxImportFileBytes+(4<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		drainRequestBody(r)
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, 400, "上传内容超过大小上限(文件 ≤10MB)")
			return
		}
		writeErr(w, 400, "解析上传表单失败: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		drainRequestBody(r)
		writeErr(w, 400, "缺少文件字段 file")
		return
	}
	defer file.Close()
	filename := header.Filename

	ext := strings.ToLower(filepath.Ext(filename))
	if ext != ".txt" && ext != ".csv" && ext != ".xlsx" {
		drainRequestBody(r)
		writeErr(w, 400, "不支持的文件扩展名,仅支持 .txt/.csv/.xlsx")
		return
	}
	data, err := io.ReadAll(file)
	if err != nil {
		drainRequestBody(r)
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, 400, "文件超过 10MB 上限")
			return
		}
		writeErr(w, 400, "读取上传文件失败: "+err.Error())
		return
	}
	if len(data) == 0 {
		writeErr(w, 400, "上传文件为空")
		return
	}
	if len(data) > maxImportFileBytes {
		writeErr(w, 400, "文件超过 10MB 上限")
		return
	}
	taskID, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("task_id")), 10, 64)
	if taskID < 0 {
		taskID = 0
	}
	// company_id > 0 强制本次导入全部归属该企业(替代按范围自动认领);
	// 解析失败/<=0 一律视为缺省,行为不变(与 task_id 同款宽松解析)。
	companyID, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("company_id")), 10, 64)
	if companyID < 0 {
		companyID = 0
	}
	if companyID > 0 {
		if err := validateImportCompany(s, companyID); err != nil {
			drainRequestBody(r)
			writeErr(w, err.status, err.msg)
			return
		}
	}

	lines, issues, perr := parseImportFile(ext, data)
	if perr != nil {
		drainRequestBody(r)
		writeErr(w, 400, perr.Error())
		return
	}

	results := make([]importResult, 0, len(lines))
	errs := make([]importErrorEntry, 0, len(issues)+1)
	var imported, failed, skipped int
	for _, pl := range lines {
		raw := recon.ClassifyLine(pl.text)
		if raw == nil {
			// 非空但无法识别(txt 的杂行、无表头逐格尝试仍失败):计 skipped 并进 errors。
			skipped++
			if len(errs) < maxImportReports {
				errs = append(errs, importErrorEntry{Row: pl.row, Value: pl.text, Error: "无法识别"})
			}
			continue
		}
		id, err := upsertImportedAsset(as, taskID, companyID, raw, pl.title)
		if err != nil {
			failed++
			if len(errs) < maxImportReports {
				errs = append(errs, importErrorEntry{Row: pl.row, Value: raw.Value, Error: err.Error()})
			}
			continue
		}
		if taskID > 0 {
			// 与 insertAssets 同款:登记这条资产在任务侧的来源。
			_ = as.SetTaskAssetSource(taskID, id, "import", "文件导入 · "+filename, nil)
		}
		imported++
		if len(results) < maxImportReports {
			results = append(results, importResult{Row: pl.row, ID: id, Type: string(raw.Type), Value: raw.Value})
		}
	}
	// 解析阶段就识别不了的问题行(超行数上限已走 400,这里只剩无法识别)。
	for _, issue := range issues {
		skipped++
		if len(errs) < maxImportReports {
			errs = append(errs, importErrorEntry{Row: issue.row, Value: issue.value, Error: issue.err})
		}
	}
	writeJSON(w, 200, map[string]any{
		"total":    imported + failed,
		"imported": imported,
		"failed":   failed,
		"skipped":  skipped,
		"results":  results,
		"errors":   errs,
	})
}

// drainRequestBody 排空请求体。任何 400 返回前必须做:keep-alive 连接上未读完
// 的 body 会让浏览器把这次响应当成网络层错误报 Failed to fetch(本项目踩过的坑)。
func drainRequestBody(r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
}

// upsertImportedAsset 把一条语义分类结果映射进正式资产图,语义与 recon_api.go
// 的 adoptStagedAsset 保持一致(domain 自动补根域名、按企业范围认领归属都由
// store 内部完成,这里不做根域名判断)。title 是 csv/xlsx 表头模式标题列的值,
// 仅用于 http service 的 PageTitle。companyID>0 时强制归属该企业(契约一),
// db 层连带注册的根域名/子域名/IP side effect 一并归属;零值=自动认领。
func upsertImportedAsset(as *db.AssetStore, taskID, companyID int64, raw *recon.RawAsset, title string) (int64, error) {
	switch raw.Type {
	case recon.TypeDomain:
		return as.UpsertSubdomain(db.UpsertSubdomainReq{Domain: raw.Value, TaskID: taskID, CompanyID: companyID})
	case recon.TypeIP:
		var ports []db.PortService
		if raw.Port > 0 {
			ports = append(ports, db.PortService{Port: raw.Port})
		}
		return as.UpsertIP(db.UpsertIPReq{IP: raw.Value, OpenPorts: ports, TaskID: taskID, CompanyID: companyID})
	case recon.TypeService:
		extra := map[string]any{}
		_ = json.Unmarshal(raw.Extra, &extra)
		if u, _ := extra["url"].(string); u != "" {
			ip := ""
			if net.ParseIP(raw.Value) != nil {
				ip = raw.Value
			}
			return as.UpsertHTTPService(db.UpsertHTTPServiceReq{URL: u, PageTitle: title, IP: ip, TaskID: taskID, CompanyID: companyID})
		}
		req := db.UpsertOtherServiceReq{Port: raw.Port, TaskID: taskID, CompanyID: companyID}
		if net.ParseIP(raw.Value) != nil {
			req.IP = raw.Value
		} else {
			req.Domain = raw.Value
		}
		return as.UpsertOtherService(req)
	}
	return 0, fmt.Errorf("无法识别的类型 %q", raw.Type)
}

// importCompanyError 是 company_id 校验失败的响应载荷(状态码+文案)。
type importCompanyError struct {
	status int
	msg    string
}

func (e *importCompanyError) Error() string { return e.msg }

// validateImportCompany 校验导入指定的归属企业存在(契约一)。GetCompany 未命中
// 返回 nil 值(而非 error),这里落成 400「企业不存在」。
func validateImportCompany(s *Server, companyID int64) *importCompanyError {
	cs := s.companyStore()
	if cs == nil {
		return &importCompanyError{status: 503, msg: "database unavailable"}
	}
	c, err := cs.GetCompany(companyID)
	if err != nil {
		return &importCompanyError{status: 500, msg: err.Error()}
	}
	if c == nil {
		return &importCompanyError{status: 400, msg: "企业不存在"}
	}
	return nil
}

// =====================================================================
// 解析器:按扩展名把上传字节流收敛成有序 parsedLine 列表
// =====================================================================

// parseImportFile 按扩展名分派解析器。返回的 perr 非 nil 表示整个文件应被
// 拒绝(xlsx 打不开 / csv 结构损坏 / 超数据行上限,handler 落成 400)。
func parseImportFile(ext string, data []byte) (lines []parsedLine, issues []importIssue, perr error) {
	switch ext {
	case ".txt":
		return parseTxtImport(data)
	case ".csv":
		rows, err := decodeCSVRows(data)
		if err != nil {
			return nil, nil, fmt.Errorf("解析 CSV 失败: %w", err)
		}
		return parseGridImport(rows)
	case ".xlsx":
		rows, err := decodeXLSXRows(data)
		if err != nil {
			return nil, nil, fmt.Errorf("解析 XLSX 失败: %w", err)
		}
		return parseGridImport(rows)
	}
	return nil, nil, fmt.Errorf("不支持的文件扩展名 %q", ext)
}

// parseTxtImport 纯文本逐行解析:空行与 # 注释行静默跳过(不计 skipped),
// 其余行原样交给 handler 侧的 ClassifyLine 语义分类,物理行号(1-based)保留。
func parseTxtImport(data []byte) ([]parsedLine, []importIssue, error) {
	text := decodeImportText(data)
	var lines []parsedLine
	dataRows := 0
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		dataRows++
		if dataRows > maxImportDataRows {
			return nil, nil, errImportRowLimit
		}
		lines = append(lines, parsedLine{row: i + 1, text: line})
	}
	return lines, nil, nil
}

// gridColumns 记录表头模式下各目标列的下标(-1=该列不存在)。
type gridColumns struct {
	url, domain, ip, port, title int
}

// 列名关键词(大小写不敏感、忽略空白/全半角,经 normalizeHeaderCell 归一后
// 做 Contains 匹配)。
var (
	urlColKeywords    = []string{"url", "链接", "link"}
	domainColKeywords = []string{"域名", "domain", "host", "主机", "子域名", "subdomain"}
	ipColKeywords     = []string{"ip", "ip地址", "addr", "address"}
	portColKeywords   = []string{"端口", "port"}
	titleColKeywords  = []string{"标题", "title", "网站名称", "站点名称"}
)

// detectGridHeader 首行任一格命中列名关键词即认定表头模式;不是表头返回 nil。
func detectGridHeader(first []string) *gridColumns {
	norm := make([]string, len(first))
	for i, c := range first {
		norm[i] = normalizeHeaderCell(c)
	}
	pick := func(keywords []string) int {
		for i, c := range norm {
			for _, kw := range keywords {
				if strings.Contains(c, kw) {
					return i
				}
			}
		}
		return -1
	}
	cols := &gridColumns{
		url:    pick(urlColKeywords),
		domain: pick(domainColKeywords),
		ip:     pick(ipColKeywords),
		port:   pick(portColKeywords),
		title:  pick(titleColKeywords),
	}
	if cols.url < 0 && cols.domain < 0 && cols.ip < 0 && cols.port < 0 && cols.title < 0 {
		return nil
	}
	return cols
}

// normalizeHeaderCell 归一表头格:转小写、去掉所有空白(含全角空格)、
// 全角 ASCII 字符折成半角(「ＵＲＬ」「(IP)」等 Excel 常见输入)。
func normalizeHeaderCell(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case unicode.IsSpace(r): // 含全角空格 U+3000
			continue
		case r >= 0xFF01 && r <= 0xFF5E: // 全角 ASCII 区 → 半角
			r -= 0xFEE0
		}
		b.WriteRune(r)
	}
	return b.String()
}

// parseGridImport csv/xlsx 共用的二维表解析(行数据统一 [][]string):
// 首行命中列名关键词走表头模式,否则整行文本先试、失败再逐格尝试。
// 行号 1-based 且含表头行;全空行静默跳过不计 skipped。
func parseGridImport(rows [][]string) ([]parsedLine, []importIssue, error) {
	var lines []parsedLine
	var issues []importIssue
	dataRows := 0
	// consume 登记 1 行数据:空行静默跳过;build 返回 false 表示该行无法识别。
	consume := func(row int, cells []string, build func() (parsedLine, bool)) error {
		if gridRowEmpty(cells) {
			return nil
		}
		dataRows++
		if dataRows > maxImportDataRows {
			return errImportRowLimit
		}
		pl, ok := build()
		if !ok {
			issues = append(issues, importIssue{row: row, value: gridRowText(cells), err: "无法识别"})
			return nil
		}
		lines = append(lines, pl)
		return nil
	}

	cols := detectGridHeader(rows[0])
	for i, cells := range rows {
		row := i + 1
		if cols != nil {
			if i == 0 {
				continue // 表头行本身不作为数据
			}
			if err := consume(row, cells, func() (parsedLine, bool) {
				return headerRowLine(row, cols, cells)
			}); err != nil {
				return nil, nil, err
			}
			continue
		}
		if err := consume(row, cells, func() (parsedLine, bool) {
			return plainRowLine(row, cells)
		}); err != nil {
			return nil, nil, err
		}
	}
	return lines, issues, nil
}

// headerRowLine 表头模式的一行:目标格文本(优先级 url 格 → 域名格 → ip 格,
// 找到非空即用)过语义分类;分类没带端口且端口列是数字则补上;解析出 service
// 且标题列非空时把标题带上(handler 落成 http service 的 PageTitle)。
func headerRowLine(row int, cols *gridColumns, cells []string) (parsedLine, bool) {
	cell := func(idx int) string {
		if idx < 0 || idx >= len(cells) {
			return ""
		}
		return strings.TrimSpace(cells[idx])
	}
	target := firstImportCell(cell(cols.url), cell(cols.domain), cell(cols.ip))
	if target == "" {
		return parsedLine{}, false
	}
	raw := recon.ClassifyLine(target)
	if raw == nil {
		return parsedLine{}, false
	}
	pl := parsedLine{row: row, text: target, port: raw.Port}
	if pl.port == 0 {
		if n, err := strconv.Atoi(cell(cols.port)); err == nil && n > 0 && n <= 65535 {
			pl.port = n
		}
	}
	if raw.Type == recon.TypeService {
		pl.title = cell(cols.title)
	}
	return pl, true
}

// plainRowLine 无表头模式:整行文本先试语义分类,失败则逐格尝试(第一格成功即用)。
func plainRowLine(row int, cells []string) (parsedLine, bool) {
	joined := strings.TrimSpace(strings.Join(cells, ","))
	if raw := recon.ClassifyLine(joined); raw != nil {
		return parsedLine{row: row, text: joined, port: raw.Port}, true
	}
	for _, c := range cells {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if raw := recon.ClassifyLine(c); raw != nil {
			return parsedLine{row: row, text: c, port: raw.Port}, true
		}
	}
	return parsedLine{}, false
}

// firstImportCell 返回第一个非空实参。
func firstImportCell(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// gridRowEmpty 整行所有格都为空白。
func gridRowEmpty(cells []string) bool {
	for _, c := range cells {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

// gridRowText 把一行格子拼回可读文本,用于 errors 数组里的 value。
func gridRowText(cells []string) string {
	trimmed := make([]string, len(cells))
	for i, c := range cells {
		trimmed[i] = strings.TrimSpace(c)
	}
	return strings.Join(trimmed, ",")
}

// =====================================================================
// 字节流 → 结构化行
// =====================================================================

// decodeImportText 把上传字节统一成 UTF-8 文本:先剥 UTF-8 BOM;不是合法
// UTF-8 时按 GB18030 重解码(Windows 中文 Excel 导出的 csv 常是 GBK 编码,
// 这是中国用户的真实场景)。xlsx 不走这里(工作簿内部本身就是 UTF-8)。
func decodeImportText(data []byte) string {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if utf8.Valid(data) {
		return string(data)
	}
	if decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(data); err == nil {
		return string(decoded)
	}
	return string(data)
}

// decodeCSVRows 按嗅探出的分隔符把 csv 字节流解析成二维表。
// 分隔符取首行中 , ; \t 出现次数最多者(都不出现用逗号)。
func decodeCSVRows(data []byte) ([][]string, error) {
	text := decodeImportText(data)
	firstLine := text
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		firstLine = text[:i]
	}
	reader := csv.NewReader(strings.NewReader(text))
	reader.Comma = sniffDelimiter(firstLine)
	reader.FieldsPerRecord = -1 // 允许各行列数不一致
	reader.LazyQuotes = true    // 资产清单里常见裸引号,宽容处理
	return reader.ReadAll()
}

// sniffDelimiter 数首行里各候选分隔符的出现次数,最多者胜出(平票取逗号)。
func sniffDelimiter(firstLine string) rune {
	comma, semi, tab := strings.Count(firstLine, ","), strings.Count(firstLine, ";"), strings.Count(firstLine, "\t")
	switch {
	case semi > comma && semi >= tab:
		return ';'
	case tab > comma && tab > semi:
		return '\t'
	default:
		return ','
	}
}

// decodeXLSXRows 用 excelize 打开工作簿并读出第一个工作表的全部行。
// 单元格值统一字符串化(见 xlsxCellString),后续逻辑与 csv 完全共用。
func decodeXLSXRows(data []byte) ([][]string, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, errors.New("工作簿中没有工作表")
	}
	rows, err := f.GetRows(sheets[0])
	if err != nil {
		return nil, err
	}
	out := make([][]string, len(rows))
	for i, r := range rows {
		cells := make([]string, len(r))
		for j, c := range r {
			cells[j] = xlsxCellString(c)
		}
		out[i] = cells
	}
	return out, nil
}

// xlsxCellString 统一单元格值的字符串化:去首尾空白;General 格式的数字经
// excelize 序列化可能带出 "443.0" 这类小数尾巴,去掉它以免端口列解析失败。
func xlsxCellString(v string) string {
	v = strings.TrimSpace(v)
	dot := strings.IndexByte(v, '.')
	if dot <= 0 || dot == len(v)-1 {
		return v
	}
	head, frac := v[:dot], v[dot+1:]
	for _, c := range frac {
		if c != '0' {
			return v
		}
	}
	for _, c := range head {
		if c < '0' || c > '9' {
			return v
		}
	}
	return head
}
