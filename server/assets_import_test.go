package server

// 资产文件导入测试:纯解析器单测为主(不依赖 DB);最后一个 handler 集成测试
// 走 NewManager 门禁,postgres 不可用自动跳过(与 assets_scope_test.go 同模式)。

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/recon"
	"github.com/xuri/excelize/v2"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// importMultipartRequest 把 (filename, content, taskID) 打包成一条
// POST /api/assets/import 的 multipart 请求。
func importMultipartRequest(t *testing.T, filename, content, taskID string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if taskID != "" {
		if err := mw.WriteField("task_id", taskID); err != nil {
			t.Fatal(err)
		}
	}
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/assets/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func TestImportAssetsParseTxt(t *testing.T) {
	content := "# 注释行\n" +
		"a-1.example.com\n" +
		"\n" +
		"   \n" +
		"1.2.3.4:22\n" +
		"http://b-1.example.com:8080/login\n" +
		"!!\r\n"
	lines, issues, perr := parseImportFile(".txt", []byte(content))
	if perr != nil {
		t.Fatalf("perr = %v, want nil", perr)
	}
	if len(issues) != 0 {
		t.Fatalf("issues = %+v, want empty(txt 无法识别行由 handler 分类后计 skipped)", issues)
	}
	wantRows := []int{2, 5, 6, 7} // 物理行号:注释/空行跳过但占行号
	if len(lines) != len(wantRows) {
		t.Fatalf("lines = %+v, want %d 条", lines, len(wantRows))
	}
	for i, row := range wantRows {
		if lines[i].row != row {
			t.Errorf("lines[%d].row = %d, want %d", i, lines[i].row, row)
		}
	}
	if lines[0].text != "a-1.example.com" {
		t.Errorf("lines[0].text = %q", lines[0].text)
	}
	if lines[1].text != "1.2.3.4:22" {
		t.Errorf("lines[1].text = %q", lines[1].text)
	}
	if lines[2].text != "http://b-1.example.com:8080/login" {
		t.Errorf("lines[2].text = %q", lines[2].text)
	}
	// 语义分类能识别 txt 的资产行(URL/host:port/域名);「!!」这类杂行
	// 分类不出——handler 侧正是据此把它计成 skipped。
	for i, pl := range lines[:3] {
		if recon.ClassifyLine(pl.text) == nil {
			t.Errorf("lines[%d].text %q 无法被语义分类", i, pl.text)
		}
	}
	if recon.ClassifyLine(lines[3].text) != nil {
		t.Errorf("lines[3].text %q 不应被语义分类", lines[3].text)
	}
}

func TestImportAssetsParseCSVWithHeader(t *testing.T) {
	content := "域名,端口,标题\r\n" +
		"a-1.example.com,443,示例站点\r\n" +
		"1.2.3.4,,\r\n" +
		"http://1.2.3.4:8080/login,20,后台\r\n"
	lines, issues, perr := parseImportFile(".csv", []byte(content))
	if perr != nil {
		t.Fatalf("perr = %v, want nil", perr)
	}
	if len(issues) != 0 {
		t.Fatalf("issues = %+v, want empty", issues)
	}
	if len(lines) != 3 {
		t.Fatalf("lines = %+v, want 3 条", lines)
	}
	// 行号 1-based 且含表头行;标题列只对 service 行生效(契约),域名行不带标题
	if lines[0].row != 2 || lines[0].text != "a-1.example.com" || lines[0].port != 443 || lines[0].title != "" {
		t.Errorf("lines[0] = %+v", lines[0])
	}
	if lines[1].row != 3 || lines[1].text != "1.2.3.4" || lines[1].port != 0 {
		t.Errorf("lines[1] = %+v", lines[1])
	}
	// URL 格优先于空域名格;URL 自带端口不被端口列覆盖;标题带上
	if lines[2].row != 4 || lines[2].text != "http://1.2.3.4:8080/login" || lines[2].port != 8080 || lines[2].title != "后台" {
		t.Errorf("lines[2] = %+v", lines[2])
	}
}

func TestImportAssetsParseCSVNoHeader(t *testing.T) {
	content := "a-1.example.com\n" +
		"1.2.3.4,8080,extra\n" +
		"garbage!!\n"
	lines, issues, perr := parseImportFile(".csv", []byte(content))
	if perr != nil {
		t.Fatalf("perr = %v, want nil", perr)
	}
	if len(lines) != 2 {
		t.Fatalf("lines = %+v, want 2 条", lines)
	}
	if lines[0].row != 1 || lines[0].text != "a-1.example.com" {
		t.Errorf("lines[0] = %+v", lines[0])
	}
	// 整行带逗号分类失败,逐格尝试落到第一格(裸 IP,无表头模式不认端口列)
	if lines[1].row != 2 || lines[1].text != "1.2.3.4" {
		t.Errorf("lines[1] = %+v", lines[1])
	}
	if len(issues) != 1 || issues[0].row != 3 || issues[0].value != "garbage!!" || issues[0].err != "无法识别" {
		t.Errorf("issues = %+v, want [{3 garbage!! 无法识别}]", issues)
	}
}

func TestImportAssetsParseCSVDelimAndGBK(t *testing.T) {
	// 分隔符嗅探:分号
	lines, _, perr := parseImportFile(".csv", []byte("域名;端口\na-1.example.com;443\n"))
	if perr != nil {
		t.Fatalf("perr = %v, want nil", perr)
	}
	if len(lines) != 1 || lines[0].text != "a-1.example.com" || lines[0].port != 443 {
		t.Fatalf("semicolon lines = %+v", lines)
	}
	// GBK(GB18030 兼容)编码字节流:Windows Excel 导出 csv 的真实场景
	utf8Src := "域名,端口\r\na-1.example.com,443\r\n1.2.3.4,22\r\n"
	gbk, err := simplifiedchinese.GB18030.NewEncoder().String(utf8Src)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains([]byte(gbk), []byte{0xD3, 0xF2}) { // 「域」的 GBK 编码,确认真的不是 UTF-8
		t.Fatalf("GBK 编码结果异常: %q", gbk)
	}
	lines, _, perr = parseImportFile(".csv", []byte(gbk))
	if perr != nil {
		t.Fatalf("perr = %v, want nil", perr)
	}
	if len(lines) != 2 || lines[0].text != "a-1.example.com" || lines[0].port != 443 || lines[1].text != "1.2.3.4" || lines[1].port != 22 {
		t.Fatalf("gbk lines = %+v", lines)
	}
}

func TestImportAssetsParseXLSX(t *testing.T) {
	// 内存里用 excelize 生成一个 workbook 再解析回读:表头 + 域名/端口/标题、
	// 裸 IP 行、URL 行(数字与浮点端口混用)。
	f := excelize.NewFile()
	const sheet = "Sheet1"
	f.SetCellValue(sheet, "A1", "域名")
	f.SetCellValue(sheet, "B1", "端口")
	f.SetCellValue(sheet, "C1", "标题")
	f.SetCellValue(sheet, "A2", "a-1.example.com")
	f.SetCellValue(sheet, "B2", 443) // 数字端口
	f.SetCellValue(sheet, "C2", "示例站点")
	f.SetCellValue(sheet, "A3", "1.2.3.4")
	f.SetCellValue(sheet, "B3", 22.0) // 浮点端口
	f.SetCellValue(sheet, "A4", "http://b-1.example.com:8080/login")
	f.SetCellValue(sheet, "C4", "后台")
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	lines, issues, perr := parseImportFile(".xlsx", buf.Bytes())
	if perr != nil {
		t.Fatalf("perr = %v, want nil", perr)
	}
	if len(issues) != 0 {
		t.Fatalf("issues = %+v, want empty", issues)
	}
	if len(lines) != 3 {
		t.Fatalf("lines = %+v, want 3 条", lines)
	}
	// 标题列只对 service 行生效(契约),域名行不带标题
	if lines[0].row != 2 || lines[0].text != "a-1.example.com" || lines[0].port != 443 || lines[0].title != "" {
		t.Errorf("lines[0] = %+v", lines[0])
	}
	if lines[1].row != 3 || lines[1].text != "1.2.3.4" || lines[1].port != 22 {
		t.Errorf("lines[1] = %+v", lines[1])
	}
	if lines[2].row != 4 || lines[2].text != "http://b-1.example.com:8080/login" || lines[2].port != 8080 || lines[2].title != "后台" {
		t.Errorf("lines[2] = %+v", lines[2])
	}
}

func TestImportAssetsXlsxCellString(t *testing.T) {
	cases := map[string]string{
		"443.0":   "443",
		"443.00":  "443",
		"443":     "443",
		"1.2.3":   "1.2.3",
		"80.":     "80.",
		"abc.def": "abc.def",
		" 443.0 ": "443",
	}
	for in, want := range cases {
		if got := xlsxCellString(in); got != want {
			t.Errorf("xlsxCellString(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestImportAssetsNormalizeHeaderCell(t *testing.T) {
	cases := map[string]string{
		"ＵＲＬ":        "url",  // 全角
		"IP 地 址":     "ip地址", // 空白忽略
		"（端口）":       "(端口)", // 全角括号折成半角;Contains 匹配仍命中「端口」关键词
		"Sub-Domain": "sub-domain",
	}
	for in, want := range cases {
		if got := normalizeHeaderCell(in); got != want {
			t.Errorf("normalizeHeaderCell(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestImportAssetsRowLimit(t *testing.T) {
	// 恰好 20000 行 + 100 条注释/空行 → 不超限(注释空行不计数据行)
	var sb strings.Builder
	for i := 0; i < 20000; i++ {
		sb.WriteString("a" + strconv.Itoa(i) + ".example.com\n")
	}
	for i := 0; i < 100; i++ {
		sb.WriteString("# c\n\n")
	}
	if _, _, perr := parseImportFile(".txt", []byte(sb.String())); perr != nil {
		t.Fatalf("20000 行不应超限, perr = %v", perr)
	}
	// 20001 行 → 整体拒绝
	sb.WriteString("overflow.example.com\n")
	_, _, perr := parseImportFile(".txt", []byte(sb.String()))
	if !errors.Is(perr, errImportRowLimit) {
		t.Fatalf("perr = %v, want errImportRowLimit", perr)
	}
}

// TestImportAssetsHandler 是 DB 集成测试:postgres 不可用自动跳过;
// 需 ARTEX_PG_DSN 显式指向隔离库(如 127.0.0.1:5499),绝不连生产库。
func TestImportAssetsHandler(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	s := &Server{m: m}

	// 503:assetStore 为 nil(零值 Manager 的 pg 为 nil)
	nilStore := &Server{m: &Manager{}}
	rec := httptest.NewRecorder()
	nilStore.importAssets(rec, httptest.NewRequest(http.MethodPost, "/api/assets/import", strings.NewReader("x")))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil store status = %d, want 503", rec.Code)
	}

	post := func(filename, content, taskID string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		s.importAssets(rec, importMultipartRequest(t, filename, content, taskID))
		return rec
	}

	// 400:缺少 file 字段
	if rec := post("", "x", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing file status = %d, body = %s", rec.Code, rec.Body.String())
	}
	// 400:扩展名不支持
	if rec := post("assets.json", "{}", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad extension status = %d, body = %s", rec.Code, rec.Body.String())
	}
	// 400:数据行超上限
	var big strings.Builder
	for i := 0; i < 20001; i++ {
		big.WriteString("a" + strconv.Itoa(i) + ".example.com\n")
	}
	if rec := post("big.txt", big.String(), ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("row limit status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// 正常导入:带 task_id,走 SetTaskAssetSource 来源登记路径
	task, err := m.CreateTask("asset import test", "文件导入集成测试", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	txt := "# 注释\n" +
		"import-1.example.com\n" +
		"\n" +
		"203.0.113.10:22\n" +
		"http://import-2.example.com:8080/login\n" +
		"!!\n"
	rec = post("assets.txt", txt, task.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("import status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Total    int                `json:"total"`
		Imported int                `json:"imported"`
		Failed   int                `json:"failed"`
		Skipped  int                `json:"skipped"`
		Results  []importResult     `json:"results"`
		Errors   []importErrorEntry `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 3 || resp.Imported != 3 || resp.Failed != 0 || resp.Skipped != 1 {
		t.Errorf("counts = %+v, want total=3 imported=3 failed=0 skipped=1", resp)
	}
	if len(resp.Results) != 3 {
		t.Fatalf("results = %+v, want 3 条", resp.Results)
	}
	if resp.Results[0].Type != "domain" || resp.Results[0].Value != "import-1.example.com" || resp.Results[0].Row != 2 || resp.Results[0].ID <= 0 {
		t.Errorf("results[0] = %+v", resp.Results[0])
	}
	if resp.Results[1].Type != "ip" || resp.Results[1].Value != "203.0.113.10" || resp.Results[1].Row != 4 {
		t.Errorf("results[1] = %+v", resp.Results[1])
	}
	if resp.Results[2].Type != "service" || resp.Results[2].Value != "import-2.example.com" || resp.Results[2].Row != 5 {
		t.Errorf("results[2] = %+v", resp.Results[2])
	}
	if len(resp.Errors) != 1 || resp.Errors[0].Row != 6 || resp.Errors[0].Value != "!!" || resp.Errors[0].Error != "无法识别" {
		t.Errorf("errors = %+v, want [{6 !! 无法识别}]", resp.Errors)
	}
}
