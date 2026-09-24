package server

// SRC 平台选择(平台 → 目标 → 资产)之二:目标 CRUD、平台×目标关联、目标资产、可导入来源。
//
// 本文件只做 HTTP 层:落库一律走 db 层已冻结的方法(db/src_targets.go、db/recon.go),
// 资产本体一律复用 AssetStore 的 Upsert 系列——与资产 API、信息搜集采纳走同一条路径。
// 自己写 INSERT 会绕过公司归属解析与各类型的去重索引,必然造出重复资产。
//
// 两条贯穿全文件的约束:
//  1. 授权范围是「平台×目标」这一层的属性(同一目标在补天与漏洞盒子下的范围不同),
//     所以任何涉及授权范围的接口都必须同时给 platform_id 与 target_id;
//  2. 返回集合前一律确保是空切片:Go 的 nil 切片会序列化成 JSON null,前端 .length 直接崩。

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// ---- 目标 CRUD ----

// srcTargets GET /api/src/targets?q=&platform_id=&limit=&offset=
// platform_id>0 时只返回挂在该平台下的目标(平台详情页的「目标」页签走这条)。
func (s *Server) srcTargets(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	q := r.URL.Query()
	platformID, _ := strconv.ParseInt(strings.TrimSpace(q.Get("platform_id")), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	targets, total, err := pg.ListSRCTargets(platformID, strings.TrimSpace(q.Get("q")), limit, offset)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if targets == nil {
		targets = []*db.SRCTarget{}
	}
	writeJSON(w, 200, map[string]any{"targets": targets, "total": total})
}

// srcTargetPayload 是新建/更新目标的请求体。
//
// Enabled 用指针:PUT 的调用方若只改名字,不该因为少传一个字段把目标静默停用。
// CompanyID 用指针:前端用 null 表达「不关联企业」(0 会被 db 层当作无意义值)。
type srcTargetPayload struct {
	Name        string `json:"name"`
	CompanyID   *int64 `json:"company_id"`
	Description string `json:"description"`
	Enabled     *bool  `json:"enabled"`
}

// createSRCTarget POST /api/src/targets
func (s *Server) createSRCTarget(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var p srcTargetPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	// 未显式给 enabled 时按「启用」建:与 src_targets.enabled 的默认值一致,
	// 否则第三方调用只传 name 会建出一个查不到的目标。
	enabled := true
	if p.Enabled != nil {
		enabled = *p.Enabled
	}
	t, err := pg.CreateSRCTarget(db.SRCTargetInput{
		Name: p.Name, CompanyID: p.CompanyID, Description: p.Description, Enabled: enabled,
	})
	if err != nil {
		writeErr(w, s.srcTargetWriteErrCode(err), err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"target": t})
}

// getSRCTarget GET /api/src/targets/{id}
func (s *Server) getSRCTarget(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	targetID, ok := pathInt(r, "id")
	if !ok || targetID <= 0 {
		writeErr(w, 400, "目标 ID 无效")
		return
	}
	t, ok := s.requireSRCTarget(w, pg, targetID)
	if !ok {
		return
	}
	writeJSON(w, 200, map[string]any{"target": t})
}

// updateSRCTarget PUT /api/src/targets/{id}
func (s *Server) updateSRCTarget(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	targetID, ok := pathInt(r, "id")
	if !ok || targetID <= 0 {
		writeErr(w, 400, "目标 ID 无效")
		return
	}
	cur, ok := s.requireSRCTarget(w, pg, targetID)
	if !ok {
		return
	}
	var p srcTargetPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	enabled := cur.Enabled
	if p.Enabled != nil {
		enabled = *p.Enabled
	}
	t, err := pg.UpdateSRCTarget(targetID, db.SRCTargetInput{
		Name: p.Name, CompanyID: p.CompanyID, Description: p.Description, Enabled: enabled,
	})
	if err != nil {
		writeErr(w, s.srcTargetWriteErrCode(err), err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"target": t})
}

// deleteSRCTarget DELETE /api/src/targets/{id}
// 挂链与资产关联由外键 CASCADE 清理;资产本体留在资产图里(可能还被别的目标引用)。
func (s *Server) deleteSRCTarget(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	targetID, ok := pathInt(r, "id")
	if !ok || targetID <= 0 {
		writeErr(w, 400, "目标 ID 无效")
		return
	}
	deleted, err := pg.DeleteSRCTarget(targetID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

// ---- 平台×目标关联(授权范围挂这一层) ----

// srcTargetLinks GET /api/src/targets/{id}/links
// 返回该目标所属的全部平台关联(含各平台下各自独立的授权范围)。
func (s *Server) srcTargetLinks(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	targetID, ok := pathInt(r, "id")
	if !ok || targetID <= 0 {
		writeErr(w, 400, "目标 ID 无效")
		return
	}
	if _, ok := s.requireSRCTarget(w, pg, targetID); !ok {
		return
	}
	links, err := pg.ListSRCTargetLinks(targetID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if links == nil {
		links = []*db.SRCTargetLink{}
	}
	writeJSON(w, 200, map[string]any{"links": links})
}

// srcTargetLinkPayload 是授权范围/规范的请求体。
type srcTargetLinkPayload struct {
	ScopeNote    string `json:"scope_note"`
	Forbidden    string `json:"forbidden"`
	AllowedTypes string `json:"allowed_types"`
	RateLimit    string `json:"rate_limit"`
	Rules        string `json:"rules"`
	Enabled      *bool  `json:"enabled"`
}

func (p srcTargetLinkPayload) toInput() db.SRCTargetLinkInput {
	enabled := true
	if p.Enabled != nil {
		enabled = *p.Enabled
	}
	return db.SRCTargetLinkInput{
		ScopeNote: p.ScopeNote, Forbidden: p.Forbidden, AllowedTypes: p.AllowedTypes,
		RateLimit: p.RateLimit, Rules: p.Rules, Enabled: enabled,
	}
}

// saveSRCTargetLink PUT /api/src/targets/{id}/links/{platformId}
// 幂等 upsert:目标还没挂到该平台时这一条就是「建立关联」,已挂时是「改授权范围」——
// 两条路径共用 LinkSRCTarget,避免出现「关联存在但范围没写进去」的中间态。
func (s *Server) saveSRCTargetLink(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	targetID, ok := pathInt(r, "id")
	if !ok || targetID <= 0 {
		writeErr(w, 400, "目标 ID 无效")
		return
	}
	platformID, ok := pathInt(r, "platformId")
	if !ok || platformID <= 0 {
		writeErr(w, 400, "平台 ID 无效")
		return
	}
	if _, ok := s.requireSRCTarget(w, pg, targetID); !ok {
		return
	}
	// 平台不存在时先给明确 404:否则会撞 src_platform_targets 的外键,
	// 用户只看到一句 SQL 报错,不知道是自己选错了平台。
	if platform, err := pg.GetSRCPlatform(platformID); err != nil {
		writeErr(w, 500, err.Error())
		return
	} else if platform == nil {
		writeErr(w, 404, "平台不存在")
		return
	}
	var p srcTargetLinkPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	link, err := pg.LinkSRCTarget(platformID, targetID, p.toInput())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"link": link})
}

// unlinkSRCTarget DELETE /api/src/targets/{id}/links/{platformId}
// 只删「平台×目标」这一行:目标本身与资产关联保留(它可能还挂在别的平台下)。
func (s *Server) unlinkSRCTarget(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	targetID, ok := pathInt(r, "id")
	if !ok || targetID <= 0 {
		writeErr(w, 400, "目标 ID 无效")
		return
	}
	platformID, ok := pathInt(r, "platformId")
	if !ok || platformID <= 0 {
		writeErr(w, 400, "平台 ID 无效")
		return
	}
	deleted, err := pg.UnlinkSRCTarget(platformID, targetID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

// ---- 目标资产 ----

// srcTargetAssets GET /api/src/targets/{id}/assets?platform_id=&type=&q=&limit=&offset=
func (s *Server) srcTargetAssets(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	targetID, ok := pathInt(r, "id")
	if !ok || targetID <= 0 {
		writeErr(w, 400, "目标 ID 无效")
		return
	}
	if _, ok := s.requireSRCTarget(w, pg, targetID); !ok {
		return
	}
	q := r.URL.Query()
	platformID, _ := strconv.ParseInt(strings.TrimSpace(q.Get("platform_id")), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	assets, total, counts, err := pg.ListSRCTargetAssets(targetID, db.SRCTargetAssetFilter{
		PlatformID: platformID, Type: strings.TrimSpace(q.Get("type")),
		Q: strings.TrimSpace(q.Get("q")), Limit: limit, Offset: offset,
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if assets == nil {
		assets = []*db.SRCTargetAsset{}
	}
	if counts == nil {
		counts = map[string]int{}
	}
	writeJSON(w, 200, map[string]any{"assets": assets, "total": total, "counts": counts})
}

// srcTargetAssetInput 是手工录入的一条资产。
type srcTargetAssetInput struct {
	Type  string `json:"type"`
	Value string `json:"value"`
	Port  int    `json:"port"`
}

// srcTargetAssetsPayload 是「添加资产」的请求体:三种来源任选其一(可组合)。
type srcTargetAssetsPayload struct {
	PlatformID int64                 `json:"platform_id"`
	Assets     []srcTargetAssetInput `json:"assets"`     // 手工录入:先落资产图再关联
	AssetIDs   []int64               `json:"asset_ids"`  // 关联已有资产
	StagedIDs  []int64               `json:"staged_ids"` // 从信息搜集暂存区导入
	Source     string                `json:"source"`
}

// addSRCTargetAssets POST /api/src/targets/{id}/assets
//
// 三条来源各自的落库路径:
//   - 手工录入:按类型调 AssetStore 的 Upsert 写进正式资产图,拿到 asset_id 再挂链;
//   - 信息搜集:复用 recon_api.go 的 adoptStagedAsset(与「采纳」按钮同一条映射),
//     再挂链——暂存类型 → 资产类型的映射规则只允许有一份,复制一份必然漂移;
//   - 关联已有:不落库,直接挂链。
//
// 单条失败只记进 errors[]、不中断整批:批量导入里一条脏数据不该让其余 99 条白干。
func (s *Server) addSRCTargetAssets(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	as := s.assetStore()
	if as == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	targetID, ok := pathInt(r, "id")
	if !ok || targetID <= 0 {
		writeErr(w, 400, "目标 ID 无效")
		return
	}
	if _, ok := s.requireSRCTarget(w, pg, targetID); !ok {
		return
	}
	var p srcTargetAssetsPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	// 资产关联是「平台×目标」下的记录:没有平台就没有可挂的维度。
	if p.PlatformID <= 0 {
		writeErr(w, 400, "请先选择平台(platform_id)")
		return
	}
	if platform, err := pg.GetSRCPlatform(p.PlatformID); err != nil {
		writeErr(w, 500, err.Error())
		return
	} else if platform == nil {
		writeErr(w, 404, "平台不存在")
		return
	}
	// source 会写进 src_target_assets.source(CHECK 约束)。先在这里挡掉非法值,
	// 否则用户拿到的是 500 的 SQL 约束报错,看不出是自己传错了字段。
	bodySource := strings.TrimSpace(p.Source)
	if bodySource != "" && !srcAssetSourceAllowed(bodySource) {
		writeErr(w, 400, "source 必须是 manual/recon/existing 之一")
		return
	}

	errs := []string{}
	// touched 记录本次涉及到的 asset_id(三路合并去重),最后据此回读关联行返回给前端。
	touched := map[int64]bool{}
	markTouched := func(id int64) {
		if id > 0 {
			touched[id] = true
		}
	}

	// 1) 手工录入 → 资产图。
	manualIDs := []int64{}
	for i, in := range p.Assets {
		if strings.TrimSpace(in.Value) == "" {
			errs = append(errs, fmt.Sprintf("手工录入第 %d 条:值不能为空", i+1))
			continue
		}
		id, err := srcManualAssetUpsert(as, in)
		if err != nil {
			errs = append(errs, fmt.Sprintf("手工录入「%s」:%s", strings.TrimSpace(in.Value), err.Error()))
			continue
		}
		manualIDs = append(manualIDs, id)
		markTouched(id)
	}

	// 2) 信息搜集暂存区 → 资产图(复用采纳映射)。
	stagedIDs := []int64{}
	if len(p.StagedIDs) > 0 {
		rows, err := srcStagedRowsByIDs(pg, p.StagedIDs)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		found := map[int64]bool{}
		for _, a := range rows {
			found[a.ID] = true
			assetID, err := s.adoptStagedAsset(as, a.TaskID, a)
			if err != nil {
				errs = append(errs, fmt.Sprintf("暂存资产 #%d(%s):%s", a.ID, a.Value, err.Error()))
				continue
			}
			stagedIDs = append(stagedIDs, assetID)
			markTouched(assetID)
		}
		// 勾选的 id 在库里不存在时明确报出来:静默跳过会让用户以为已经导入了。
		for _, id := range p.StagedIDs {
			if !found[id] {
				errs = append(errs, fmt.Sprintf("暂存资产 #%d 不存在", id))
			}
		}
	}

	// 3) 关联已有资产 → 不落库。
	existingIDs := []int64{}
	for _, id := range p.AssetIDs {
		if id > 0 {
			existingIDs = append(existingIDs, id)
			markTouched(id)
		}
	}

	// 三路分开挂链:source 语义各不相同(手工=manual / 信息搜集=recon / 已有=existing),
	// 合并成一次调用会把三类来源压成同一个标签,事后无法分辨资产是从哪来的。
	added, skipped := 0, 0
	link := func(ids []int64, source, note string) {
		if len(ids) == 0 {
			return
		}
		a, sk, err := pg.AddSRCTargetAssets(p.PlatformID, targetID, ids, source, note)
		added += a
		skipped += sk
		if err != nil {
			// 不整批失败:其余两路可能已经成功,前端要能看到真实结果。
			errs = append(errs, fmt.Sprintf("关联失败(%s):%s", source, err.Error()))
		}
	}
	link(manualIDs, "manual", "手工录入")
	link(stagedIDs, "recon", "信息搜集导入")
	if len(existingIDs) > 0 {
		src := bodySource
		if src == "" {
			// 关联已有资产不传 source 时按语义补 existing,而不是让 db 层兜底成 manual。
			src = "existing"
		}
		link(existingIDs, src, "关联已有资产")
	}

	// 按 id 回读本次涉及到的关联行。
	//
	// 为什么不用 ListSRCTargetAssets 分页回读:本目标下资产可能已有几百条,
	// 「重复关联一个很久以前挂上的资产」时新行不在首页里,前端会拿到空 assets
	// 而 added/skipped 显示 1——数字与列表对不上比不返回列表更让人困惑。
	out, err := srcLinkedAssetRows(pg, targetID, p.PlatformID, srcSortedIDs(touched))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"added": added, "skipped": skipped, "assets": out, "errors": errs,
	})
}

// removeSRCTargetAssets DELETE /api/src/targets/{id}/assets?ids=1,2,3
// ids 是关联行(src_target_assets.id)的 id,不是 asset_id:同一资产在不同平台下的
// 关联行是两行,按 asset_id 删会连别的平台的关联一起删掉。
func (s *Server) removeSRCTargetAssets(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	targetID, ok := pathInt(r, "id")
	if !ok || targetID <= 0 {
		writeErr(w, 400, "目标 ID 无效")
		return
	}
	ids := []int64{}
	for _, part := range csvValues(r.URL.Query().Get("ids")) {
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			writeErr(w, 400, "资产关联 ID 无效:"+part)
			return
		}
		ids = append(ids, id)
	}
	deleted, err := pg.RemoveSRCTargetAssets(targetID, ids)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

// ---- 可导入来源 ----

// srcTargetImportSource 是导入候选行。staged 与 asset 两个 kind 共用一份结构,
// 前端因此能用同一个列表组件渲染两边(见 web 的 add-assets-dialog)。
type srcTargetImportSource struct {
	Kind      string `json:"kind"`
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Value     string `json:"value"`
	Port      int    `json:"port,omitempty"`
	TaskID    int64  `json:"task_id,omitempty"`
	TaskName  string `json:"task_name,omitempty"`
	Collector string `json:"collector,omitempty"`
	// Imported=true 时前端置灰该项。
	Imported bool `json:"imported"`
}

const (
	srcImportSourceDefaultLimit = 200
	srcImportSourceMaxLimit     = 500
	// 一次扫描/合并的上限:与暂存区列表的 2000 条展示上限对齐,避免内存里拼出无界切片。
	srcImportSourceScanCap = 2000
	// 单个企业最多回看多少个任务:一个企业跑过几百轮信息搜集属于异常,取最近 100 个足够。
	srcImportSourceMaxTasks = 100
)

// srcTargetImportSources GET /api/src/targets/{id}/import-sources?platform_id=&kind=&q=&limit=
//
// kind=staged 给「待采纳的暂存资产」,kind=asset 给「企业资产图里已有的资产」。
// 两者都按目标关联的企业过滤:目标没关联企业时没有「企业下」的来源可给,
// 返回空数组而不是报错(前端据此显示「先到概览页签关联企业」的引导)。
func (s *Server) srcTargetImportSources(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	targetID, ok := pathInt(r, "id")
	if !ok || targetID <= 0 {
		writeErr(w, 400, "目标 ID 无效")
		return
	}
	target, ok := s.requireSRCTarget(w, pg, targetID)
	if !ok {
		return
	}
	q := r.URL.Query()
	platformID, _ := strconv.ParseInt(strings.TrimSpace(q.Get("platform_id")), 10, 64)
	kind := strings.TrimSpace(q.Get("kind"))
	if kind == "" {
		// 前端始终显式传 kind;空值兜底成 staged,而不是把两个 kind 混在一个列表里——
		// 两张表的 id 可能撞车,混排会让前端的按 id 勾选选错行。
		kind = "staged"
	}
	if kind != "staged" && kind != "asset" {
		writeErr(w, 400, "kind 必须是 staged 或 asset")
		return
	}
	keyword := strings.TrimSpace(q.Get("q"))
	limit := srcImportSourceLimit(q.Get("limit"))

	// 已关联的 asset_id 集合(按当前平台):kind=asset 的置灰依据。
	linked, err := pg.SrcTargetAssetIDs(targetID, platformID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	linkedSet := make(map[int64]bool, len(linked))
	for _, id := range linked {
		linkedSet[id] = true
	}

	sources := []srcTargetImportSource{}
	if target.CompanyID != nil && *target.CompanyID > 0 {
		switch kind {
		case "staged":
			sources, err = s.srcImportStagedSources(pg, *target.CompanyID, keyword, limit)
		case "asset":
			sources, err = s.srcImportAssetSources(*target.CompanyID, keyword, limit, linkedSet)
		}
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	writeJSON(w, 200, map[string]any{"sources": sources})
}

// srcImportStagedSources 列出该企业下待采纳的暂存资产。
//
// 为什么不按 staged_assets.company_id 直接过滤:采集器写入时只挂「第一个企业」
// (见 recon/runner.go 的 M1 简化),多企业任务产出的行会漏掉其余企业。所以走
// task_scope 的企业归属:任务属于该企业,它的待采纳产出就算该企业的。
func (s *Server) srcImportStagedSources(pg *db.DB, companyID int64, keyword string, limit int) ([]srcTargetImportSource, error) {
	taskIDs, err := srcCompanyTaskIDs(pg, companyID)
	if err != nil {
		return nil, err
	}
	out := []srcTargetImportSource{}
	seen := map[int64]bool{}
	for _, taskID := range taskIDs {
		rows, err := pg.ListPendingStagedAssets(taskID, nil)
		if err != nil {
			return nil, err
		}
		taskName := s.srcTaskName(pg, taskID)
		for _, a := range rows {
			if a == nil || seen[a.ID] {
				continue
			}
			seen[a.ID] = true
			if keyword != "" && !srcImportMatchStaged(a, taskName, keyword) {
				continue
			}
			out = append(out, srcTargetImportSource{
				Kind: "staged", ID: a.ID, Type: a.Type, Value: a.Value, Port: a.Port,
				TaskID: a.TaskID, TaskName: taskName, Collector: a.Collector,
				// staged 还没采纳时没有 asset_id,无法与「已关联集合」比对,一律 false:
				// 重复导入会被 ON CONFLICT 幂等跳过,宁可让用户多点一次,也不要因为
				// 猜错把本来可导入的项置灰。
				Imported: false,
			})
			if len(out) >= srcImportSourceScanCap {
				break
			}
		}
		if len(out) >= srcImportSourceScanCap {
			break
		}
	}
	// 新者在前:用户刚跑完一轮信息搜集时,想导入的几乎总是最新那批。
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// srcImportAssetSources 列出该企业资产图里已有的资产(可关联为「已有资产」)。
func (s *Server) srcImportAssetSources(companyID int64, keyword string, limit int, linked map[int64]bool) ([]srcTargetImportSource, error) {
	as := s.assetStore()
	if as == nil {
		return []srcTargetImportSource{}, nil
	}
	// 有搜索词时先多取一批再在内存里过滤:QueryByCompany 没有 q 参数,若按 limit 取
	// 再过滤,「前 limit 条里没命中」会被误报成「该企业没有资产」。
	fetch := limit
	if keyword != "" {
		fetch = srcImportSourceScanCap
	}
	// typ 传空串 = 不过滤类型(db 层只在 typ!="" 时拼条件)。
	assets, err := as.QueryByCompany(companyID, "", fetch, 0)
	if err != nil {
		return nil, err
	}
	kw := strings.ToLower(keyword)
	out := []srcTargetImportSource{}
	for _, a := range assets {
		if a == nil {
			continue
		}
		value := srcAssetDisplayValue(a)
		if kw != "" && !strings.Contains(strings.ToLower(value), kw) {
			continue
		}
		port := 0
		if a.Port != nil {
			port = *a.Port
		}
		out = append(out, srcTargetImportSource{
			Kind: "asset", ID: a.ID, Type: a.Type, Value: value, Port: port,
			Imported: linked[a.ID],
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// srcImportMatchStaged 与前端搜索框的提示(「值 / 任务 / 收集器」)保持一致:
// 只按 value 过滤会让用户搜任务名时以为没有数据。
func srcImportMatchStaged(a *db.StagedAsset, taskName, keyword string) bool {
	kw := strings.ToLower(keyword)
	return strings.Contains(strings.ToLower(a.Value), kw) ||
		strings.Contains(strings.ToLower(a.Collector), kw) ||
		strings.Contains(strings.ToLower(taskName), kw)
}

// srcCompanyTaskIDs 返回归属该企业的任务 id(最近优先)。
//
// 用 task_scope 而不是内存里的 Task.CompanyIDs:task_scope 是企业归属的唯一事实来源
// (与 db.TaskCompanyIDs 查的是同一张表),内存副本可能落后于运行中 agent 通过
// add_task_scope 新加的企业范围。
func srcCompanyTaskIDs(pg *db.DB, companyID int64) ([]int64, error) {
	rows, err := pg.Query(`
SELECT DISTINCT ts.task_id FROM task_scope ts
WHERE ts.kind='company' AND ts.company_id=$1
ORDER BY ts.task_id DESC LIMIT $2`, companyID, srcImportSourceMaxTasks)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// srcTaskName 取任务名(空=未命名)。管理器内存里没有(如未加载)时回落到 PG;
// 两边都拿不到就留空——前端对空 task_name 有兜底展示。
func (s *Server) srcTaskName(pg *db.DB, taskID int64) string {
	if t, ok := s.m.Task(strconv.FormatInt(taskID, 10)); ok && t != nil {
		return t.Name
	}
	if t, err := pg.GetTask(taskID); err == nil && t != nil {
		return t.Name
	}
	return ""
}

// srcAssetDisplayValue 按 db 层 srcTargetAssetValueExpr 的同一优先级取「值」列:
// domain → url → ip → app_name。前端只认一个可读值,不该理解 assets 的列布局。
func srcAssetDisplayValue(a *db.Asset) string {
	for _, v := range []string{a.Domain, a.URL, a.IP, a.AppName} {
		if v != "" {
			return v
		}
	}
	return ""
}

// srcImportSourceLimit 归一 limit:默认 200、上限 500(契约值)。
func srcImportSourceLimit(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return srcImportSourceDefaultLimit
	}
	if n > srcImportSourceMaxLimit {
		return srcImportSourceMaxLimit
	}
	return n
}

// ---- 内部小工具 ----

// requireSRCTarget 取目标;不存在时写 404 并返回 false,调用方直接 return。
func (s *Server) requireSRCTarget(w http.ResponseWriter, pg *db.DB, id int64) (*db.SRCTarget, bool) {
	t, err := pg.GetSRCTarget(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return nil, false
	}
	if t == nil {
		writeErr(w, 404, "目标不存在")
		return nil, false
	}
	return t, true
}

// srcTargetWriteErrCode 把 db 层的错误映射成 HTTP 码:重名是可预期的用户错误(409),
// 其余按 400 交给用户看提示;真正的库故障会带 SQL 文本,仍走 400 也算可接受(信息不丢)。
func (s *Server) srcTargetWriteErrCode(err error) int {
	if errors.Is(err, db.ErrSRCTargetNameTaken) {
		return 409
	}
	return 400
}

// srcAssetSourceAllowed 与 src_target_assets.source 的 CHECK 约束一致(见 db/schema.sql):
// manual=手工录入 / recon=从信息搜集导入 / existing=关联已有资产。
func srcAssetSourceAllowed(source string) bool {
	switch source {
	case "manual", "recon", "existing":
		return true
	}
	return false
}

// srcStagedRowSelect 按 id 批量取暂存行。
//
// 为什么在 server 层写这条 SQL:db 层的 ListStagedAssets / ListPendingStagedAssets 都必须
// 先给 task_id,而一次勾选可能横跨多个任务(同一企业下跑过多轮信息搜集),按单任务查会
// 静默丢掉其他任务的行。只取导入真正要用的列,不复制 db 层那份 13 列的全列清单。
const srcStagedRowSelect = `SELECT id, task_id, collector, type, value, port, extra
FROM staged_assets WHERE id = ANY($1) ORDER BY id`

// srcLinkedAssetRowSelect 按 asset_id 回读「平台×目标」下的关联行。
//
// 值列表达式与 db 层 srcTargetAssetValueExpr 保持同一优先级(domain → url → ip → app_name);
// 该常量在 db 包内未导出,这里只能重写一遍——顺序必须一致,否则同一资产在「资产」页签
// 与「添加资产」返回结果里会显示成两个不同的值。
const srcLinkedAssetRowSelect = `SELECT ta.id, ta.platform_id, ta.target_id, ta.asset_id, ta.source, ta.note, ta.created_at,
	a.type,
	COALESCE(NULLIF(a.domain,''), NULLIF(a.url,''), NULLIF(a.ip,''), NULLIF(a.app_name,''), ''),
	COALESCE(a.port,0), a.company_id
FROM src_target_assets ta JOIN assets a ON a.id=ta.asset_id
WHERE ta.target_id=$1 AND ta.platform_id=$2 AND ta.asset_id = ANY($3)
ORDER BY ta.id DESC`

// srcLinkedAssetRows 回读指定资产在本目标本平台下的关联行(新挂的在前)。
// assetIDs 为空时返回空切片:不要让空 IN 列表变成全表扫描。
func srcLinkedAssetRows(pg *db.DB, targetID, platformID int64, assetIDs []int64) ([]*db.SRCTargetAsset, error) {
	if targetID <= 0 || platformID <= 0 || len(assetIDs) == 0 {
		return []*db.SRCTargetAsset{}, nil
	}
	rows, err := pg.Query(srcLinkedAssetRowSelect, targetID, platformID, assetIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*db.SRCTargetAsset{}
	for rows.Next() {
		x := &db.SRCTargetAsset{}
		if err := rows.Scan(&x.ID, &x.PlatformID, &x.TargetID, &x.AssetID, &x.Source, &x.Note, &x.CreatedAt,
			&x.Type, &x.Value, &x.Port, &x.CompanyID); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// srcSortedIDs 把去重用的 set 摊平成稳定有序的切片:同样的输入必须产生同样的 SQL 参数,
// 否则同一次提交在日志/回放里看起来像两次不同的请求。
func srcSortedIDs(set map[int64]bool) []int64 {
	out := make([]int64, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// srcStagedRowsByIDs 取勾选的暂存行(参数化,ids 直接进 ANY 数组)。
func srcStagedRowsByIDs(pg *db.DB, ids []int64) ([]*db.StagedAsset, error) {
	if len(ids) == 0 {
		return []*db.StagedAsset{}, nil
	}
	rows, err := pg.Query(srcStagedRowSelect, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*db.StagedAsset{}
	for rows.Next() {
		a := &db.StagedAsset{}
		if err := rows.Scan(&a.ID, &a.TaskID, &a.Collector, &a.Type, &a.Value, &a.Port, &a.Extra); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// srcManualAssetUpsert 把一条手工录入的资产写进正式资产图,返回 asset_id。
//
// 一律复用 AssetStore 的 Upsert 系列(与资产 API、信息搜集采纳同一条落库路径):
// 自己 INSERT 会绕过公司归属解析与各类型的去重索引,必然造出重复资产。
// TaskID 传 0:这条资产不属于任何任务,是用户在 SRC 目标下直接登记的。
func srcManualAssetUpsert(as *db.AssetStore, in srcTargetAssetInput) (int64, error) {
	value := strings.TrimSpace(in.Value)
	if value == "" {
		return 0, fmt.Errorf("资产值不能为空")
	}
	switch in.Type {
	case "root_domain":
		return as.UpsertRootDomain(db.UpsertRootDomainReq{Domain: srcHostOnly(value)})
	case "subdomain":
		return as.UpsertSubdomain(db.UpsertSubdomainReq{Domain: srcHostOnly(value)})
	case "ip":
		// 端口进 open_ports(与 adoptStagedAsset 对 ip 的处理一致),不建服务资产。
		ip, port := srcSplitHostPort(value)
		if in.Port > 0 {
			port = in.Port
		}
		var ports []db.PortService
		if port > 0 {
			ports = append(ports, db.PortService{Port: port, Service: srcServiceNameForPort(port)})
		}
		return as.UpsertIP(db.UpsertIPReq{IP: ip, OpenPorts: ports})
	case "service":
		// 值里带 scheme 就是 http 服务(域名/端口/根域名由 db 层从 URL 解析);
		// 否则按 host[:port] 归其他服务。
		if strings.Contains(value, "://") {
			return as.UpsertHTTPService(db.UpsertHTTPServiceReq{URL: value})
		}
		host, port := value, in.Port
		if port <= 0 {
			host, port = srcSplitHostPort(value)
		}
		req := db.UpsertOtherServiceReq{Port: port, ServiceName: srcServiceNameForPort(port)}
		if net.ParseIP(host) != nil {
			req.IP = host
		} else {
			req.Domain = host
		}
		return as.UpsertOtherService(req)
	case "app":
		return as.UpsertApp(db.UpsertAppReq{Name: value})
	case "endpoint":
		// 手工录入没有方法字段,UpsertEndpoint 又要求非空:默认 GET,
		// 用户可在资产详情页改成真实方法,好过因为缺字段直接拒绝录入。
		return as.UpsertEndpoint(db.UpsertEndpointReq{URL: value, Method: "GET"})
	}
	// 与 assets.type 的 CHECK 约束一致(root_domain/ip/subdomain/app/service/endpoint)。
	return 0, fmt.Errorf("不支持的资产类型 %q", in.Type)
}

// srcHostOnly 去掉值里的端口与路径,只留主机段。
// 域名类资产(根域名/子域名)的值必须是纯主机名:把「api.acme.com:8443」整串写进
// domain 列,既污染了资产图的唯一键,又让端口信息丢失(端口列反而是空的)。
// 前端已做拆分,这里是服务端的兜底——直接调 API 的调用方也可能传带端口的值。
func srcHostOnly(v string) string {
	host, _ := srcSplitHostPort(v)
	// 去掉路径/查询:domain 列不该出现 "/api/v1?x=1" 这类内容。
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	return strings.TrimSuffix(strings.TrimSpace(host), ".")
}

// srcSplitHostPort 拆 "host:port"(IPv6 需带方括号);拆不出来时整串当 host。
func srcSplitHostPort(v string) (string, int) {
	host, portText, err := net.SplitHostPort(v)
	if err != nil {
		return v, 0
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		return v, 0
	}
	return host, port
}

// srcWellKnownPorts 只列常见端口:手工录入「1.2.3.4:22」这类值时,用户不会填服务名,
// 而 UpsertOtherService 要求 service_name 非空且它参与去重键——给一个稳定的名字,
// 同一端口重复录入才会命中同一行而不是各建一份。
var srcWellKnownPorts = map[int]string{
	21: "ftp", 22: "ssh", 23: "telnet", 25: "smtp", 53: "dns",
	110: "pop3", 143: "imap", 443: "https", 445: "smb", 873: "rsync",
	1433: "mssql", 1521: "oracle", 2049: "nfs", 3306: "mysql", 3389: "rdp",
	5432: "postgresql", 5900: "vnc", 6379: "redis", 8080: "http", 8443: "https",
	9200: "elasticsearch", 11211: "memcached", 27017: "mongodb",
}

// srcServiceNameForPort 端口 → 服务名;未知端口用 "port-<n>" 兜底(仍稳定可去重)。
func srcServiceNameForPort(port int) string {
	if name, ok := srcWellKnownPorts[port]; ok {
		return name
	}
	if port > 0 {
		return "port-" + strconv.Itoa(port)
	}
	return "unknown"
}
