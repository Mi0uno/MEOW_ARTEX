package server

// 平台规范附件的物理 I/O 与解析,以及附件类别改派的 HTTP handler。
//
// 与字典库(wordbook_files.go)同样的分层理由:路径 containment 校验集中在一处,
// handler 不直调 os.*——只要有一个入口漏了检查,用户的文件名就能把文件写到 dataDir 之外。
// 解析与落盘的参数均为已验证的 int64 id 与文件名;唯一承载 HTTP 输入的入口是
// 末尾的 updateSRCPlatformFile(改派只收 {kind},不触碰任何路径)。

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/report"
	pdf "github.com/ledongthuc/pdf"
	"github.com/xuri/excelize/v2"
)

// srcPlatformFileMaxBytes 是单份规范附件的上限(32MB)。
// 平台规范(奖励标准/评分标准/守则/规范)是文字材料,32MB 远超任何真实文档;
// 设上限是因为解析正文会被并入平台规范注入 AI,一个超大文件会把注入文本一起打爆。
const srcPlatformFileMaxBytes = 32 << 20

// srcPlatformRoot 返回全部平台附件的根目录(绝对路径)。
func srcPlatformRoot(dataDir string) string {
	return filepath.Clean(filepath.Join(dataDir, "src-platforms"))
}

// srcPlatformDir 返回某平台附件的目录(绝对路径);校验失败返回空串。
// 目录名只用平台 id(纯数字),本身不含路径成分;仍然过一遍 containment 检查,
// 因为 id 一旦为 0/负数,Join 会退化成根目录,后续删盘就会误伤别的平台。
func srcPlatformDir(dataDir string, platformID int64) string {
	if platformID <= 0 {
		return ""
	}
	root := srcPlatformRoot(dataDir)
	dir := filepath.Clean(filepath.Join(root, strconv.FormatInt(platformID, 10)))
	if !stringsHasPrefixDir(dir, root+string(filepath.Separator)) {
		return ""
	}
	return dir
}

// srcPlatformStoredName 归一化**用户上传**的原始文件名:只保留文件名部分。
// 必须 Base 化而不能直接拒绝含路径的名:浏览器在某些场景会把完整本地路径塞进
// multipart 的 filename(经典值 "C:\\fakepath\\报告.docx"),这类名是正常的,
// 直接拒绝会让用户传不了文件。拒绝空/"."/".."/含分隔符与 NUL 是 Base 之后的兜底。
func srcPlatformStoredName(raw string) string {
	name := filepath.Base(strings.TrimSpace(raw))
	if name == "" || name == "." || name == ".." {
		return ""
	}
	// Base 之后理论上已无分隔符;但 Linux 的 filepath.Base 不把 `\` 当分隔符,
	// 所以显式再拒一次(反斜杠名在 Windows 上也是非法文件名)。
	if strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return ""
	}
	return name
}

// srcPlatformPathName 严格校验**来自 DB 的 stored_name**(落盘文件名)。
//
// 与 srcPlatformStoredName 的区别是不做 Base 化、只接受原样合法名:stored_name 全部
// 由 srcPlatformNewStoredName 生成(十六进制 + 安全后缀),永远不含路径成分。
// 一旦它含分隔符或 ..,就说明 DB 行被人工 SQL 改过或历史数据迁移出错——
// 这时 Base 化会把它「悄悄映射」到目录里的另一个文件(读错/删错文件),
// 直接拒绝才能让调用方明确报错,而不是静默地操作一个非预期的文件。
func srcPlatformPathName(raw string) string {
	name := strings.TrimSpace(raw)
	if name == "" || name == "." || name == ".." || name != filepath.Base(name) {
		return ""
	}
	if strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return ""
	}
	return name
}

// srcPlatformFilePath 拼接并校验单份附件的完整路径;校验失败返回空串。
// Clean + 目录前缀比较:即使 storedName 被污染成 ../../etc/passwd,
// Clean 之后也不可能再落在平台目录内。
func srcPlatformFilePath(dataDir string, platformID int64, storedName string) string {
	dir := srcPlatformDir(dataDir, platformID)
	name := srcPlatformPathName(storedName)
	if dir == "" || name == "" {
		return ""
	}
	p := filepath.Clean(filepath.Join(dir, name))
	if !stringsHasPrefixDir(p, dir+string(filepath.Separator)) {
		return ""
	}
	return p
}

// srcPlatformFormat 是附件按扩展名归的解析档位。
type srcPlatformFormat int

const (
	srcFormatText   srcPlatformFormat = iota // .md/.markdown/.txt:直接当 UTF-8 文本
	srcFormatDocx                            // .docx:程序化解析成 markdown
	srcFormatXlsx                            // .xlsx:excelize 全工作表文本化
	srcFormatPDF                             // .pdf:ledongthuc/pdf 逐页抽文本
	srcFormatLegacy                          // .doc:老二进制格式,入口即拒绝
	srcFormatOther                           // 其余:只存档不解析
)

// srcPlatformFormatOf 按原始文件名判档。
// 为什么 .doc 单独拒绝而不是也走「仅存档」:.doc 是用户「以为能解析」的最常见误传,
// 直接给出「另存为 .docx」的可行指引,比让它静默变成一条不注入的死附件有用得多。
// .xlsx/.pdf 升级为可解析:统一上传入口先收文件再归类,正文能不能抽出来直接决定
// AI 识别填入有没有原料——纯存档会让这两个最常见格式的附件永远进不了 AI 约束。
func srcPlatformFormatOf(filename string) srcPlatformFormat {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(filename))) {
	case ".md", ".markdown", ".txt":
		return srcFormatText
	case ".docx":
		return srcFormatDocx
	case ".xlsx":
		return srcFormatXlsx
	case ".pdf":
		return srcFormatPDF
	case ".doc":
		return srcFormatLegacy
	}
	return srcFormatOther
}

// srcPlatformStoredExt 归一化落盘扩展名(小写、仅 [a-z0-9]、长度受限)。
// 不能直接信任原始后缀:上传名是不可信输入(可能含 <>:"|?* 或超长后缀),
// 这些字符在 Windows 上根本建不了文件,一旦落盘名非法附件就永远存不下来。
// 未知后缀统一落 .bin——它只决定磁盘上的名字,不参与解析(解析只看原始扩展名)。
func srcPlatformStoredExt(filename string) string {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(filename)))
	if len(ext) < 2 || len(ext) > 13 {
		return ".bin"
	}
	for _, c := range ext[1:] {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return ".bin"
		}
	}
	return ext
}

// srcPlatformArchiveNote 是「仅存档」附件的固定说明。
// 明写「不会注入 AI 约束」而不是留空:用户传了 pdf 后如果界面什么都不说,
// 他会以为规范已经生效——这是这类功能最危险的误解。
const srcPlatformArchiveNote = "该格式暂不支持解析，仅存档；不会注入 AI 约束"

// srcPlatformParse 是一次附件解析的结果。
// ContentText 只在 parsed 时非空:skipped/failed 的正文留空,
// db 层的 SRCPlatformSpecText 据此把它们排除在 AI 约束之外。
type srcPlatformParse struct {
	ContentText string
	Status      string // parsed / skipped / failed
	Note        string
}

// parseSRCPlatformFile 按档位解析已落盘的附件。
// 解析失败不返回 error 而是落到 Status=failed:用户上传的文件不该因为解析器读不懂
// 就丢掉,原件永远保留,失败原因写进 parse_note 供界面展示。
func parseSRCPlatformFile(path string, format srcPlatformFormat) srcPlatformParse {
	switch format {
	case srcFormatText:
		b, err := os.ReadFile(path)
		if err != nil {
			return srcPlatformParse{Status: "failed", Note: "读取文件失败:" + err.Error()}
		}
		text := sanitizeSRCText(string(b))
		return srcPlatformParse{
			ContentText: text,
			Status:      "parsed",
			Note:        fmt.Sprintf("文本直读:约 %d 字", utf8.RuneCountInString(text)),
		}
	case srcFormatDocx:
		f, err := os.Open(path)
		if err != nil {
			return srcPlatformParse{Status: "failed", Note: "读取文件失败:" + err.Error()}
		}
		defer f.Close()
		markdown, structure, err := report.ParseDocxReader(f)
		if err != nil {
			return srcPlatformParse{Status: "failed", Note: "docx 解析失败:" + err.Error()}
		}
		text := sanitizeSRCText(markdown)
		return srcPlatformParse{
			ContentText: text,
			Status:      "parsed",
			Note: fmt.Sprintf("docx 解析:章节 %d · 段落 %d · 表格 %d · 约 %d 字",
				len(structure.Headings), structure.Paragraphs, structure.Tables, structure.Words),
		}
	case srcFormatLegacy:
		// 入口已拒绝 .doc,这里兜底成「仅存档」而不是假装解析过。
		return srcPlatformParse{
			Status: "skipped",
			Note:   "老版 .doc 二进制格式无法解析，仅存档；不会注入 AI 约束",
		}
	case srcFormatXlsx:
		text, sheets, rows, err := xlsxToText(path)
		if err != nil {
			return srcPlatformParse{Status: "failed", Note: "xlsx 解析失败:" + err.Error()}
		}
		text = sanitizeSRCText(text)
		return srcPlatformParse{
			ContentText: text,
			Status:      "parsed",
			Note: fmt.Sprintf("xlsx 解析:工作表 %d · 行 %d · 约 %d 字",
				sheets, rows, utf8.RuneCountInString(text)),
		}
	case srcFormatPDF:
		text, pages, err := pdfToText(path)
		if err != nil {
			// 解析报错不挡上传:原件已落盘,failed 只是让界面明示「这份没进 AI 约束」。
			return srcPlatformParse{Status: "failed", Note: "pdf 解析失败:" + err.Error()}
		}
		if strings.TrimSpace(text) == "" {
			// 一页文本都抽不到基本是扫描件/纯图片 PDF:明说原因,而不是让用户
			// 以为「传上去了就是生效了」。
			return srcPlatformParse{
				Status: "failed",
				Note:   fmt.Sprintf("pdf 共 %d 页,未抽取到文本(可能是扫描件/图片型 PDF,请改用文字版)", pages),
			}
		}
		text = sanitizeSRCText(text)
		return srcPlatformParse{
			ContentText: text,
			Status:      "parsed",
			Note:        fmt.Sprintf("pdf 解析:%d 页 · 约 %d 字", pages, utf8.RuneCountInString(text)),
		}
	default:
		return srcPlatformParse{Status: "skipped", Note: srcPlatformArchiveNote}
	}
}

// xlsxToText 把工作簿的全部工作表文本化:每行一条,单元格间用制表符分隔,
// 行与行之间换行。表格化的奖励标准(等级×分数)拍平成制表符矩阵后 LLM 仍能按列理解,
// 且比试图重建 markdown 表格简单可靠得多。
// 返回 (正文, 工作表数, 总行数, 错误);空行跳过,省正文配额。
func xlsxToText(path string) (string, int, int, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return "", 0, 0, err
	}
	defer f.Close()
	var b strings.Builder
	sheets, rows := 0, 0
	for _, sheet := range f.GetSheetList() {
		grid, err := f.GetRows(sheet)
		if err != nil {
			return "", 0, 0, fmt.Errorf("读取工作表 «%s» 失败: %w", sheet, err)
		}
		sheets++
		for _, row := range grid {
			cells := make([]string, 0, len(row))
			for _, c := range row {
				cells = append(cells, strings.TrimSpace(c))
			}
			if strings.Join(cells, "") == "" {
				continue
			}
			b.WriteString(strings.Join(cells, "\t"))
			b.WriteString("\n")
			rows++
		}
	}
	return b.String(), sheets, rows, nil
}

// pdfToText 逐页抽取 PDF 文本(纯 Go,无外部依赖)。
// 页与页之间补一个换行,避免上一页末行与下一页首行粘连;空页跳过。
// 返回 (正文, 页数, 错误)。扫描件/图片型 PDF 抽出来是空正文,由调用方给 failed+note。
func pdfToText(path string) (string, int, error) {
	fd, r, err := pdf.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer fd.Close()
	total := r.NumPage()
	var b strings.Builder
	for p := 1; p <= total; p++ {
		// GetPlainText 内部已处理空内容页(返回 "", nil);单个页面结构异常时
		// 它会把 panic 转成 error 返回,这里直接降级为解析失败,原件照常存档。
		text, err := r.Page(p).GetPlainText(nil)
		if err != nil {
			return "", total, fmt.Errorf("第 %d 页文本抽取失败: %w", p, err)
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		b.WriteString(strings.TrimRight(text, "\n"))
		b.WriteString("\n")
	}
	return b.String(), total, nil
}

// sanitizeSRCText 清掉 NUL 与非法 UTF-8。
// PG 的 TEXT 不接受 0x00,而 .txt/.md 是用户上传的任意字节流——正文里夹带一个 NUL
// 就会让整条 INSERT 报错、附件白传;非法 UTF-8 则会让前端把正文渲染成乱码。
func sanitizeSRCText(s string) string {
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	return s
}

// srcPlatformNewStoredName 生成一个不与现有文件冲突的磁盘名(16 位十六进制 + 扩展名)。
//
// 为什么不用 DB 自增 id 命名(db 的注释提到 <id>.<ext> 也可行):id 要 INSERT 之后才存在,
// 而 INSERT 又必须带上 stored_name,在「先落盘再写库」的顺序下拿不到 id。
// 随机名同样唯一,磁盘名不对外暴露(下载用的是 DB 里的 filename),可读性无所谓。
func srcPlatformNewStoredName(dir, ext string) (string, error) {
	for i := 0; i < 8; i++ {
		var buf [8]byte
		if _, err := rand.Read(buf[:]); err != nil {
			return "", err
		}
		name := hex.EncodeToString(buf[:]) + ext
		if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
			return name, nil
		}
	}
	return "", fmt.Errorf("无法分配附件文件名")
}

// writeSRCPlatformStored 把上传流落盘到平台目录,返回最终磁盘文件名与字节数。
//
// 先写临时文件再 rename 到最终名:同目录 rename 是原子的,磁盘上不会出现半截文件;
// 若进程在写入中途被杀,留下的也只是 tmp-* 临时文件,不会变成一条可下载的坏附件。
// 落盘先于写库(db.CreateSRCPlatformFile 的约定):库里不会出现指向不存在文件的记录。
// 超限/IO 失败时自行删掉临时文件再返回错误,调用方只需处理返回值。
func writeSRCPlatformStored(dir, ext string, src io.Reader) (string, int64, error) {
	stored, err := srcPlatformNewStoredName(dir, ext)
	if err != nil {
		return "", 0, err
	}
	f, err := os.CreateTemp(dir, "tmp-*")
	if err != nil {
		return "", 0, err
	}
	tmp := f.Name()
	fail := func(err error) (string, int64, error) {
		_ = os.Remove(tmp)
		return "", 0, err
	}
	// 多读 1 字节即可判定超限,不必把超出的部分真的读进来。
	n, cpErr := io.Copy(f, io.LimitReader(src, srcPlatformFileMaxBytes+1))
	syncErr := f.Sync()
	closeErr := f.Close()
	if cpErr != nil {
		return fail(cpErr)
	}
	if syncErr != nil {
		return fail(syncErr)
	}
	if closeErr != nil {
		return fail(closeErr)
	}
	if n > srcPlatformFileMaxBytes {
		return fail(fmt.Errorf("文件超过 %dMB 上限", srcPlatformFileMaxBytes>>20))
	}
	if err := os.Rename(tmp, filepath.Join(dir, stored)); err != nil {
		return fail(err)
	}
	return stored, n, nil
}

// srcPlatformStoredPath 由平台目录 + 已落盘的 stored_name 拼出绝对路径(供解析器读回)。
// 只在本包内使用,输入都是刚由 writeSRCPlatformStored 生成的合法名字。
func srcPlatformStoredPath(dir, storedName string) string {
	return filepath.Join(dir, storedName)
}

// openSRCPlatformFileForRead 打开附件供下载(路径经 containment 校验)。
func openSRCPlatformFileForRead(dataDir string, platformID int64, storedName string) (*os.File, error) {
	p := srcPlatformFilePath(dataDir, platformID, storedName)
	if p == "" {
		return nil, fmt.Errorf("附件路径异常")
	}
	return os.Open(p)
}

// removeSRCPlatformFile 删除附件文件(路径经 containment 校验)。
// 文件已不存在不算错误:盘上文件可能被手工清理过,删行才是权威动作。
func removeSRCPlatformFile(dataDir string, platformID int64, storedName string) {
	p := srcPlatformFilePath(dataDir, platformID, storedName)
	if p == "" {
		return
	}
	_ = os.Remove(p)
}

// removeSRCPlatformDir 删除某平台的附件目录(平台被删时调用)。
// 平台行删除会 CASCADE 掉 src_platform_files,但磁盘文件不在 PG 的事务里,
// 不主动清理就会在 dataDir 里留下永久无人认领的孤儿文件。
func removeSRCPlatformDir(dataDir string, platformID int64) {
	dir := srcPlatformDir(dataDir, platformID)
	if dir == "" {
		return
	}
	_ = os.RemoveAll(dir)
}

// ---- 类别改派 ----

// updateSRCPlatformFile PUT /api/src/platforms/{id}/files/{fid}  body: {kind}
//
// 统一上传入口先收文件(kind='other'),用户/AI 再在这里把附件归入四类之一。
// 类别决定附件正文在平台规范注入里的分组标签(srcFileKindLabel),改派即改语义,
// 所以必须重走一遍 kind 校验,而不是只信前端 Select 的取值。
func (s *Server) updateSRCPlatformFile(w http.ResponseWriter, r *http.Request) {
	platformID, ok := pathInt(r, "id")
	if !ok || platformID <= 0 {
		writeErr(w, 400, "bad platform id")
		return
	}
	fileID, ok := pathInt(r, "fid")
	if !ok || fileID <= 0 {
		writeErr(w, 400, "bad file id")
		return
	}
	var req struct {
		Kind string `json:"kind"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	kind := strings.TrimSpace(req.Kind)
	if !db.ValidSRCFileKind(kind) {
		writeErr(w, 400, "kind 必须是 reward_standard / scoring_standard / code_of_conduct / rules / other")
		return
	}
	// 平台存在性单独确认:没有平台上下文的 404 与「附件不存在」语义不同,
	// 前端据此提示刷新列表还是跳回平台页。
	platform, err := s.m.PG().GetSRCPlatform(platformID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if platform == nil {
		writeErr(w, 404, "平台不存在")
		return
	}
	row, err := s.m.PG().UpdateSRCPlatformFileKind(platformID, fileID, kind)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeErr(w, 404, "附件不存在")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	log.Printf("[src-platform] 平台 #%d «%s» 附件 #%d «%s» 类别改派为 %s",
		platformID, platform.Name, fileID, row.Filename, kind)
	writeJSON(w, 200, map[string]any{"file": row})
}
