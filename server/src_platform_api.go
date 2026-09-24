package server

// SRC 平台选择之一:平台 CRUD、平台下目标挂链、平台规范附件。
//
// 三级结构的中间层在这里落地:平台(src_platforms)→ 目标(src_targets)→ 资产。
// 平台与目标是多对多,授权范围(scope_note)挂在「平台×目标」这一层——
// 同一家企业同时收录在补天和漏洞盒子时,两个平台下的授权范围各不相同,
// 所以挂链接口必须同时拿到 platform_id 与 target_id,不能只给目标。
//
// 附件的物理 I/O 与解析在 src_platform_files.go,本文件只管 HTTP 语义。

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// srcPlatformPayload 是新建/更新平台的请求体(字段名与前端 api.ts 冻结一致)。
// Enabled 用指针而不是 bool:更新时省略该字段必须保持原值,
// 若用零值 false,前端任何一次只改名字的保存都会把平台悄悄停用。
type srcPlatformPayload struct {
	Name            string `json:"name"`
	Homepage        string `json:"homepage"`
	Description     string `json:"description"`
	RewardStandard  string `json:"reward_standard"`
	ScoringStandard string `json:"scoring_standard"`
	CodeOfConduct   string `json:"code_of_conduct"`
	Rules           string `json:"rules"`
	Enabled         *bool  `json:"enabled"`
}

// srcPlatformInputOf 把请求体转成 db 层输入;enabled 缺省时用 fallback(新建=true)。
func srcPlatformInputOf(p srcPlatformPayload, fallbackEnabled bool) db.SRCPlatformInput {
	enabled := fallbackEnabled
	if p.Enabled != nil {
		enabled = *p.Enabled
	}
	return db.SRCPlatformInput{
		Name:            strings.TrimSpace(p.Name),
		Homepage:        strings.TrimSpace(p.Homepage),
		Description:     p.Description,
		RewardStandard:  p.RewardStandard,
		ScoringStandard: p.ScoringStandard,
		CodeOfConduct:   p.CodeOfConduct,
		Rules:           p.Rules,
		Enabled:         enabled,
	}
}

// writeSRCPlatformErr 统一把 db 层错误映射成 HTTP 状态码。
// 重名必须 409 而不是 400:前端据此给出「换个名字」而不是「输入不合法」的提示。
func writeSRCPlatformErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrSRCNameTaken), errors.Is(err, db.ErrSRCTargetNameTaken):
		writeErr(w, http.StatusConflict, err.Error())
	case strings.Contains(err.Error(), "必填"):
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

// listSRCPlatforms GET /api/src/platforms?q=&enabled=&limit=&offset=
// counts 由 db 层从**未过滤**全量算出,前端分类页签的数字才不会在选中某类后归零。
func (s *Server) listSRCPlatforms(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	items, total, counts, err := s.m.PG().ListSRCPlatforms(
		strings.TrimSpace(q.Get("q")), strings.TrimSpace(q.Get("enabled")), limit, offset)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if items == nil {
		items = []*db.SRCPlatform{}
	}
	if counts == nil {
		counts = map[string]int{}
	}
	writeJSON(w, 200, map[string]any{"platforms": items, "total": total, "counts": counts})
}

// createSRCPlatform POST /api/src/platforms
func (s *Server) createSRCPlatform(w http.ResponseWriter, r *http.Request) {
	var p srcPlatformPayload
	if err := decode(r, &p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	// 新建的平台默认启用:用户刚建它就是要用它。
	platform, err := s.m.PG().CreateSRCPlatform(srcPlatformInputOf(p, true))
	if err != nil {
		writeSRCPlatformErr(w, err)
		return
	}
	log.Printf("[src-platform] 新建平台 #%d «%s»", platform.ID, platform.Name)
	writeJSON(w, 201, map[string]any{"platform": platform})
}

// getSRCPlatform GET /api/src/platforms/{id}
func (s *Server) getSRCPlatform(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok || id <= 0 {
		writeErr(w, 400, "bad platform id")
		return
	}
	platform, err := s.m.PG().GetSRCPlatform(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if platform == nil {
		writeErr(w, 404, "平台不存在")
		return
	}
	writeJSON(w, 200, map[string]any{"platform": platform})
}

// updateSRCPlatform PUT /api/src/platforms/{id}
// 请求体里 enabled 缺省时沿用现值(而不是被零值改停用)。
func (s *Server) updateSRCPlatform(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok || id <= 0 {
		writeErr(w, 400, "bad platform id")
		return
	}
	existing, err := s.m.PG().GetSRCPlatform(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if existing == nil {
		writeErr(w, 404, "平台不存在")
		return
	}
	var p srcPlatformPayload
	if err := decode(r, &p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	platform, err := s.m.PG().UpdateSRCPlatform(id, srcPlatformInputOf(p, existing.Enabled))
	if err != nil {
		writeSRCPlatformErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"platform": platform})
}

// deleteSRCPlatform DELETE /api/src/platforms/{id}
//
// 先删库行再删盘:行是权威,盘上文件是衍生物。反过来做的话,一旦删行失败,
// 库里就会留下一批指向不存在文件的附件(界面能列出但下载必 404)。
// 平台删除只 CASCADE 掉挂链/资产关联/附件行,目标本身保留——它可能还挂在别的平台下。
func (s *Server) deleteSRCPlatform(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok || id <= 0 {
		writeErr(w, 400, "bad platform id")
		return
	}
	deleted, err := s.m.PG().DeleteSRCPlatform(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if deleted {
		removeSRCPlatformDir(s.m.dir, id)
		log.Printf("[src-platform] 删除平台 #%d(含其目标挂链与附件)", id)
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

// ---- 平台 × 目标 ----

// srcPlatformTargets GET /api/src/platforms/{id}/targets?q=&limit=&offset=
//
// 平台不存在时返回空列表而不是 404:本端点的响应契约只有 {targets,total},
// 没有表达「平台不存在」的位置,而详情页的平台是否存在由 getSRCPlatform 判定。
// 若这里回 404,平台被并发删除时前端会弹一个莫名其妙的错误而不是空态。
func (s *Server) srcPlatformTargets(w http.ResponseWriter, r *http.Request) {
	platformID, ok := pathInt(r, "id")
	if !ok || platformID <= 0 {
		writeErr(w, 400, "bad platform id")
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	items, total, err := s.m.PG().ListSRCTargets(platformID, strings.TrimSpace(q.Get("q")), limit, offset)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if items == nil {
		items = []*db.SRCTarget{}
	}
	writeJSON(w, 200, map[string]any{"targets": items, "total": total})
}

// addSRCPlatformTarget POST /api/src/platforms/{id}/targets
// body: {target_id, name, company_id, description, scope_note}
//
// 两种语义合一:target_id>0 = 把已有目标挂到本平台;target_id<=0 = 用 name 新建目标再挂。
// 合一是刻意的——前端「添加目标」对话框里既可以从下拉选已有目标,也可以直接输名字新建,
// 拆成两个端点会让前端得先探测目标是否存在。
func (s *Server) addSRCPlatformTarget(w http.ResponseWriter, r *http.Request) {
	platformID, ok := pathInt(r, "id")
	if !ok || platformID <= 0 {
		writeErr(w, 400, "bad platform id")
		return
	}
	var req struct {
		TargetID    int64  `json:"target_id"`
		Name        string `json:"name"`
		CompanyID   int64  `json:"company_id"`
		Description string `json:"description"`
		ScopeNote   string `json:"scope_note"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	platform, err := s.m.PG().GetSRCPlatform(platformID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if platform == nil {
		writeErr(w, 404, "平台不存在")
		return
	}

	var target *db.SRCTarget
	created := false
	if req.TargetID > 0 {
		// 挂已有目标:先确认它真的存在,否则 Link 会撞外键报一个看不懂的错误。
		target, err = s.m.PG().GetSRCTarget(req.TargetID)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if target == nil {
			writeErr(w, 404, "目标不存在")
			return
		}
	} else {
		name := strings.TrimSpace(req.Name)
		if name == "" {
			writeErr(w, 400, "请填写目标名称,或选择一个已有目标")
			return
		}
		var companyID *int64
		if req.CompanyID > 0 {
			cid := req.CompanyID
			companyID = &cid
		}
		target, err = s.m.PG().CreateSRCTarget(db.SRCTargetInput{
			Name:        name,
			CompanyID:   companyID,
			Description: req.Description,
			Enabled:     true,
		})
		if err != nil {
			writeSRCPlatformErr(w, err)
			return
		}
		created = true
	}

	// 挂链(已存在则更新授权范围,幂等)。授权范围取 body 的 scope_note;
	// 其余四类文本(禁止事项/允许类型/速率限制/规范)留给「目标详情」页单独编辑,
	// 避免在这个入口用空串把它们覆盖掉——LinkSRCTarget 是全量覆盖语义。
	linkIn := db.SRCTargetLinkInput{ScopeNote: strings.TrimSpace(req.ScopeNote), Enabled: true}
	if !created {
		if old, gerr := s.m.PG().GetSRCTargetLink(platformID, target.ID); gerr == nil && old != nil {
			linkIn.Forbidden = old.Forbidden
			linkIn.AllowedTypes = old.AllowedTypes
			linkIn.RateLimit = old.RateLimit
			linkIn.Rules = old.Rules
			linkIn.Enabled = old.Enabled
		}
	}
	link, err := s.m.PG().LinkSRCTarget(platformID, target.ID, linkIn)
	if err != nil {
		// 刚建的目标挂链失败就回滚:否则会留下一个不属于任何平台的目标,
		// 而它在本页(唯一入口)里完全不可见,用户只会看到「添加失败」却多了条脏数据。
		if created {
			if _, derr := s.m.PG().DeleteSRCTarget(target.ID); derr != nil {
				log.Printf("[src-platform] 目标 #%d 挂链失败且回滚失败: %v", target.ID, derr)
			}
		}
		writeErr(w, 500, "挂载目标失败: "+err.Error())
		return
	}
	log.Printf("[src-platform] 平台 #%d «%s» 添加目标 #%d «%s»(新建=%v)",
		platformID, platform.Name, target.ID, target.Name, created)
	writeJSON(w, 201, map[string]any{"target": target, "link": link})
}

// removeSRCPlatformTarget DELETE /api/src/platforms/{id}/targets/{tid}
// 只解除挂链,不删目标:目标可能还挂在别的平台下,且其资产关联与成果都还在。
func (s *Server) removeSRCPlatformTarget(w http.ResponseWriter, r *http.Request) {
	platformID, ok := pathInt(r, "id")
	if !ok || platformID <= 0 {
		writeErr(w, 400, "bad platform id")
		return
	}
	targetID, ok := pathInt(r, "tid")
	if !ok || targetID <= 0 {
		writeErr(w, 400, "bad target id")
		return
	}
	deleted, err := s.m.PG().UnlinkSRCTarget(platformID, targetID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if deleted {
		log.Printf("[src-platform] 平台 #%d 解除目标 #%d 挂链", platformID, targetID)
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

// ---- 平台规范附件 ----

// srcPlatformFiles GET /api/src/platforms/{id}/files — 附件清单(含解析状态与正文)。
// 平台不存在时同样返回空列表(理由同 srcPlatformTargets)。
func (s *Server) srcPlatformFiles(w http.ResponseWriter, r *http.Request) {
	platformID, ok := pathInt(r, "id")
	if !ok || platformID <= 0 {
		writeErr(w, 400, "bad platform id")
		return
	}
	files, err := s.m.PG().ListSRCPlatformFiles(platformID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if files == nil {
		files = []*db.SRCPlatformFile{}
	}
	writeJSON(w, 200, map[string]any{"files": files})
}

// uploadSRCPlatformFile POST /api/src/platforms/{id}/files(multipart: file + kind)
//
// 处理链路:校验平台与 kind → 落盘 → 解析 → 写库 → 记日志。
// 落盘先于写库(db.CreateSRCPlatformFile 的约定):库里不会出现指向不存在文件的记录;
// 反过来若写库失败,则删掉刚落的文件,保证「文件与 DB 行同生共死」,不留孤儿。
//
// 解析档位(见 src_platform_files.go):.md/.markdown/.txt 直读、.docx 解析成 markdown、
// .doc 明确拒绝、其余(含 pdf/xlsx)只存档并标 skipped。
// 解析失败(failed)仍然返回 201 且文件照常存档——用户上传的文件不该因为解析器读不懂就丢。
func (s *Server) uploadSRCPlatformFile(w http.ResponseWriter, r *http.Request) {
	// MaxBytesReader 装在解析之前,否则超限请求会把内存吃满。
	// 额外留 1MB 给 multipart 边界/头部;单文件的 32MB 上限由落盘侧精确把关。
	r.Body = http.MaxBytesReader(w, r.Body, srcPlatformFileMaxBytes+1<<20)
	// fail 统一「排空残余请求体再回错」:本端点的请求体可能高达 32MB,
	// 不排空就返回时服务端会提前关连接,浏览器拿到的是网络错误(而不是我们写的提示),
	// 前端只能显示「上传失败」而看不到真正的原因。所有提前返回都必须走这里。
	fail := func(code int, msg string) {
		_, _ = io.Copy(io.Discard, r.Body)
		writeErr(w, code, msg)
	}

	platformID, ok := pathInt(r, "id")
	if !ok || platformID <= 0 {
		fail(400, "bad platform id")
		return
	}
	platform, err := s.m.PG().GetSRCPlatform(platformID)
	if err != nil {
		fail(500, err.Error())
		return
	}
	if platform == nil {
		fail(404, "平台不存在")
		return
	}

	if err := r.ParseMultipartForm(8 << 20); err != nil {
		_, _ = io.Copy(io.Discard, r.Body)
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, 413, fmt.Sprintf("文件超过 %dMB 上限", srcPlatformFileMaxBytes>>20))
			return
		}
		writeErr(w, 400, "multipart 解析失败: "+err.Error())
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "缺少 file 字段")
		return
	}
	defer file.Close()

	kind := strings.TrimSpace(r.FormValue("kind"))
	if !db.ValidSRCFileKind(kind) {
		writeErr(w, 400, "kind 必须是 reward_standard / scoring_standard / code_of_conduct / rules")
		return
	}
	// 原始文件名只用于展示与判扩展名,一律 Base 化并拒绝路径成分:
	// 浏览器通常会剥掉路径,但 multipart 的 filename 字段是完全由客户端控制的不可信输入。
	filename := srcPlatformStoredName(hdr.Filename)
	if filename == "" {
		writeErr(w, 400, "文件名无效")
		return
	}
	format := srcPlatformFormatOf(filename)
	if format == srcFormatLegacy {
		writeErr(w, 400, "老版 .doc 二进制格式暂不支持,请用 Word/WPS 另存为 .docx 后重新上传")
		return
	}

	dir := srcPlatformDir(s.m.dir, platformID)
	if dir == "" {
		writeErr(w, 500, "平台附件目录异常")
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeErr(w, 500, "建目录失败: "+err.Error())
		return
	}

	stored, size, err := writeSRCPlatformStored(dir, srcPlatformStoredExt(filename), file)
	if err != nil {
		// 落盘失败时磁盘已由 helper 自行清理,这里只需回错。
		writeErr(w, 400, "保存文件失败: "+err.Error())
		return
	}

	parsed := parseSRCPlatformFile(srcPlatformStoredPath(dir, stored), format)
	row, err := s.m.PG().CreateSRCPlatformFile(db.SRCPlatformFile{
		PlatformID:  platformID,
		Kind:        kind,
		Filename:    filename,
		StoredName:  stored,
		ByteSize:    size,
		ContentText: parsed.ContentText,
		ParseStatus: parsed.Status,
		ParseNote:   parsed.Note,
	})
	if err != nil {
		removeSRCPlatformFile(s.m.dir, platformID, stored)
		writeErr(w, 500, "附件入库失败: "+err.Error())
		return
	}

	// 注入与否必须落到日志:用户看到「已上传」但规范没进 AI 约束时,
	// 这条日志是唯一能定位「为什么模型没遵守奖励标准」的线索。
	effect := "已注入 AI 约束"
	if parsed.Status != "parsed" || strings.TrimSpace(parsed.ContentText) == "" {
		effect = "未注入(无正文)"
	}
	log.Printf("[src-platform] 平台 #%d «%s» 上传附件 #%d «%s»(%s/%s,%d 字节,%s):%s",
		platformID, platform.Name, row.ID, filename, kind, parsed.Status, size, effect, parsed.Note)

	writeJSON(w, 201, map[string]any{"file": row})
}

// downloadSRCPlatformFile GET /api/src/platforms/{id}/files/{fid}/download
// 附件按二进制流下载,文件名用上传时的原始名(filename 列,可能含中文)。
func (s *Server) downloadSRCPlatformFile(w http.ResponseWriter, r *http.Request) {
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
	row, err := s.m.PG().GetSRCPlatformFile(platformID, fileID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if row == nil {
		writeErr(w, 404, "附件不存在")
		return
	}
	f, err := openSRCPlatformFileForRead(s.m.dir, platformID, row.StoredName)
	if err != nil {
		writeErr(w, 404, "附件文件不存在(可能已被手工清理)")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}

	// 两个 filename 参数是刻意的:filename= 给老浏览器,其值必须是纯 ASCII 且不含
	// 引号/换行(否则会破坏响应头),所以用严格校验过的落盘名(十六进制 + 安全后缀,
	// 天然满足);filename*= 给现代浏览器,用 url.PathEscape 转义后的中文原文。
	// 只给 filename* 会让少数老客户端把文件名退化成 URL 尾段。
	asciiName := srcPlatformPathName(row.StoredName)
	if asciiName == "" {
		asciiName = fmt.Sprintf("file-%d", row.ID)
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(
		`attachment; filename="%s"; filename*=UTF-8''%s`,
		asciiName, url.PathEscape(row.Filename)))
	// ServeContent 负责 Range / If-Modified-Since / Content-Length;
	// 它只在 Content-Type 未设时才做嗅探,这里已显式设成 octet-stream。
	http.ServeContent(w, r, row.Filename, st.ModTime(), f)
}

// deleteSRCPlatformFile DELETE /api/src/platforms/{id}/files/{fid}
// 先删行拿 stored_name,再删盘:行删成功才动手删文件,避免行还在而文件先没了。
// 文件本就不存在不算错误(盘上文件可能被手工清理过)。
func (s *Server) deleteSRCPlatformFile(w http.ResponseWriter, r *http.Request) {
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
	stored, deleted, err := s.m.PG().DeleteSRCPlatformFile(platformID, fileID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if deleted {
		removeSRCPlatformFile(s.m.dir, platformID, stored)
		log.Printf("[src-platform] 平台 #%d 删除附件 #%d", platformID, fileID)
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}
