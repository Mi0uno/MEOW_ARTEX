package server

// 漏洞库 API:条目 CRUD + PoC 管理 + 采集状态 + 手动触发同步。

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// listVulnEntries GET /api/vulndb/entries?category=&categories=&severity=&status=&source=&q=&has_poc=&kev=&sort=&desc=&limit=&offset=
// categories 支持逗号分隔多分类(如 categories=0day,nday = 漏洞库页排除 CVE)。
func (s *Server) listVulnEntries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	taskID, _ := strconv.ParseInt(q.Get("task_id"), 10, 64)
	var cats []string
	for _, c := range strings.Split(q.Get("categories"), ",") {
		if c = strings.TrimSpace(c); c != "" {
			cats = append(cats, c)
		}
	}
	items, total, counts, err := s.m.PG().ListVulnEntries(db.VulnEntryFilter{
		Category:   strings.TrimSpace(q.Get("category")),
		Categories: cats,
		Severity:   strings.TrimSpace(q.Get("severity")),
		Status:     strings.TrimSpace(q.Get("status")),
		Source:     strings.TrimSpace(q.Get("source")),
		Q:          strings.TrimSpace(q.Get("q")),
		TaskID:     taskID,
		HasPoc:     strings.TrimSpace(q.Get("has_poc")),
		Kev:        strings.TrimSpace(q.Get("kev")),
		Sort:       strings.TrimSpace(q.Get("sort")),
		Desc:       q.Get("desc") != "0",
		Limit:      limit, Offset: offset,
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if items == nil {
		items = []*db.VulnEntry{}
	}
	// 严重度分类计数(CVE 页严重度页签):受除严重度外其它筛选影响。
	sevCounts, err := s.m.PG().CountVulnEntriesBySeverity(db.VulnEntryFilter{
		Category: strings.TrimSpace(q.Get("category")),
		Categories: cats,
		Status:   strings.TrimSpace(q.Get("status")),
		Source:   strings.TrimSpace(q.Get("source")),
		Q:        strings.TrimSpace(q.Get("q")),
		TaskID:   taskID,
		HasPoc:   strings.TrimSpace(q.Get("has_poc")),
		Kev:      strings.TrimSpace(q.Get("kev")),
	})
	if err != nil {
		sevCounts = map[string]int{}
	}
	writeJSON(w, 200, map[string]any{"entries": items, "total": total, "counts": counts, "sev_counts": sevCounts})
}

// getVulnEntry GET /api/vulndb/entries/{id} — 条目全文 + 其 PoC 列表(含正文)。
func (s *Server) getVulnEntry(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad entry id")
		return
	}
	e, err := s.m.PG().GetVulnEntry(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if e == nil {
		writeErr(w, 404, "entry not found")
		return
	}
	pocs, err := s.m.PG().ListVulnPocs(id, true)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"entry": e, "pocs": pocs})
}

// vulnEntryPayload 是新建/更新条目的请求体。
type vulnEntryPayload struct {
	Category     string          `json:"category"`
	Title        string          `json:"title"`
	CVEID        string          `json:"cve_id"`
	Vendor       string          `json:"vendor"`
	Product      string          `json:"product"`
	Affected     string          `json:"affected"`
	Severity     string          `json:"severity"`
	VulnClass    string          `json:"vuln_class"`
	Description  string          `json:"description"`
	Exploitation string          `json:"exploitation"`
	Remediation  string          `json:"remediation"`
	Refs         json.RawMessage `json:"refs"`
	Tags         json.RawMessage `json:"tags"`
	Status       string          `json:"status"`
}

func (p vulnEntryPayload) toInput() db.VulnEntryInput {
	return db.VulnEntryInput{
		Category: p.Category, Title: p.Title, CVEID: p.CVEID, Vendor: p.Vendor, Product: p.Product,
		Affected: p.Affected, Severity: p.Severity, VulnClass: p.VulnClass, Description: p.Description,
		Exploitation: p.Exploitation, Remediation: p.Remediation, Source: "manual",
		Refs: p.Refs, Tags: p.Tags, Status: p.Status,
	}
}

// createVulnEntry POST /api/vulndb/entries
func (s *Server) createVulnEntry(w http.ResponseWriter, r *http.Request) {
	var p vulnEntryPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	e, err := s.m.PG().CreateVulnEntry(p.toInput())
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"entry": e})
}

// updateVulnEntry PUT /api/vulndb/entries/{id}
func (s *Server) updateVulnEntry(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad entry id")
		return
	}
	var p vulnEntryPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if err := s.m.PG().UpdateVulnEntry(id, p.toInput()); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	e, err := s.m.PG().GetVulnEntry(id)
	if err != nil || e == nil {
		writeErr(w, 404, "entry not found")
		return
	}
	writeJSON(w, 200, map[string]any{"entry": e})
}

// deleteVulnEntry DELETE /api/vulndb/entries/{id}
func (s *Server) deleteVulnEntry(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad entry id")
		return
	}
	deleted, err := s.m.PG().DeleteVulnEntry(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

// reclassifyVulnEntry POST /api/vulndb/entries/{id}/reclassify —
// 对 AI 自动入库的条目重新识别:删旧条目(连带其 PoC)后重走
// 「搜索取证 + AI 分类」链路。仅允许 source=ai_finding 且带 finding_id 的条目。
// 同步执行(典型 20-60s,内部各环节有独立超时),前端用专用长超时调用。
func (s *Server) reclassifyVulnEntry(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad entry id")
		return
	}
	e, err := s.m.PG().GetVulnEntry(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if e == nil {
		writeErr(w, 404, "entry not found")
		return
	}
	if e.Source != "ai_finding" || e.FindingID == nil || *e.FindingID <= 0 {
		writeErr(w, 400, "仅 AI 自动入库的条目支持重新识别")
		return
	}
	findingID := *e.FindingID
	if _, err := s.m.PG().DeleteVulnEntry(id); err != nil {
		writeErr(w, 500, "删除旧条目失败: "+err.Error())
		return
	}
	s.autoClassifyFinding(r.Context(), findingID)
	fresh, err := s.m.PG().FindVulnEntryByFinding(findingID)
	if err != nil || fresh == nil {
		writeErr(w, 500, "重新识别未产出条目(详见服务端日志)")
		return
	}
	writeJSON(w, 200, map[string]any{"entry": fresh})
}

// ---- PoC ----

// listVulnPocs GET /api/vulndb/entries/{id}/pocs?content=1
func (s *Server) listVulnPocs(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad entry id")
		return
	}
	withContent := r.URL.Query().Get("content") == "1"
	pocs, err := s.m.PG().ListVulnPocs(id, withContent)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if pocs == nil {
		pocs = []*db.VulnPoc{}
	}
	writeJSON(w, 200, map[string]any{"pocs": pocs})
}

// vulnPocPayload 是新建/更新 PoC 的请求体。
type vulnPocPayload struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Language  string `json:"language"`
	Content   string `json:"content"`
	SourceURL string `json:"source_url"`
	Verified  bool   `json:"verified"`
}

// createVulnPoc POST /api/vulndb/entries/{id}/pocs
func (s *Server) createVulnPoc(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad entry id")
		return
	}
	var p vulnPocPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	poc, created, err := s.m.PG().CreateVulnPoc(db.VulnPocInput{
		EntryID: id, Name: p.Name, Kind: p.Kind, Language: p.Language,
		Content: p.Content, Source: "manual", SourceURL: p.SourceURL, Verified: p.Verified,
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if !created {
		writeErr(w, 409, "该来源 URL 的 PoC 已存在")
		return
	}
	writeJSON(w, 201, map[string]any{"poc": poc})
}

// updateVulnPoc PUT /api/vulndb/pocs/{id}
func (s *Server) updateVulnPoc(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad poc id")
		return
	}
	var p vulnPocPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if err := s.m.PG().UpdateVulnPoc(id, db.VulnPocInput{
		Name: p.Name, Kind: p.Kind, Language: p.Language, Content: p.Content, Verified: p.Verified,
	}); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// deleteVulnPoc DELETE /api/vulndb/pocs/{id}
func (s *Server) deleteVulnPoc(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad poc id")
		return
	}
	deleted, err := s.m.PG().DeleteVulnPoc(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

// ---- 采集状态 ----

// vulnDBSyncStatus GET /api/vulndb/sync — 采集运行记录(分页/筛选) + 各源最新状态 + 开关。
// 参数:source=&status=&q=&limit=&offset=。响应:
//
//	runs:         本页记录(默认最新 20 条)
//	total:        筛选后总数
//	status_counts:状态计数(不受状态自身筛选影响,页签用)
//	latest:       每个采集源最近一轮(情报源页签实时状态表)
//	counts/total_entries: 漏洞分类计数与条目总数(页签徽标用)
func (s *Server) vulnDBSyncStatus(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	runs, total, statusCounts, err := s.m.PG().ListVulnSyncRunsPage(db.VulnSyncRunFilter{
		Source: strings.TrimSpace(q.Get("source")),
		Status: strings.TrimSpace(q.Get("status")),
		Q:      strings.TrimSpace(q.Get("q")),
		Limit:  limit, Offset: offset,
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if runs == nil {
		runs = []*db.VulnSyncRun{}
	}
	latest, err := s.m.PG().LatestVulnSyncRuns()
	if err != nil {
		latest = []*db.VulnSyncRun{}
	}
	counts, totalEntries, err := s.m.PG().CountVulnEntriesByCategory()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// 默认开启(未设置视为开):用户要的是「24 小时不间断搜集」。
	// 与 vulnSync.enabled() 同一判据:只有显式写 "false" 才算关。
	enabled := true
	intervalH := 6
	if v, _, _ := s.m.PG().GetSetting(settingVulnDBSyncOn); v == "false" {
		enabled = false
	}
	if v, _, _ := s.m.PG().GetSetting(settingVulnDBSyncInterval); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			intervalH = n
		}
	}
	cnvdURL, _, _ := s.m.PG().GetSetting(settingVulnCNVDFeedURL)
	writeJSON(w, 200, map[string]any{
		"runs": runs, "total": total, "status_counts": statusCounts, "latest": latest,
		"counts": counts, "total_entries": totalEntries,
		"enabled": enabled, "interval_hours": intervalH,
		"cnvd_url": cnvdURL,
	})
}

// triggerVulnDBSync POST /api/vulndb/sync — 立即跑一轮采集(手动触发)。
func (s *Server) triggerVulnDBSync(w http.ResponseWriter, r *http.Request) {
	if s.vulnSync == nil {
		writeErr(w, 503, "采集器未初始化")
		return
	}
	if !s.vulnSync.trigger() {
		writeErr(w, 409, "采集正在进行中")
		return
	}
	writeJSON(w, 202, map[string]any{"status": "started"})
}

// setVulnDBSyncConfig PUT /api/vulndb/sync/config — 开关、间隔与 CNVD 数据端点。
func (s *Server) setVulnDBSyncConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled       *bool   `json:"enabled"`
		IntervalHours *int    `json:"interval_hours"`
		CNVDURL       *string `json:"cnvd_url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if req.Enabled != nil {
		v := "false"
		if *req.Enabled {
			v = "true"
		}
		if err := s.m.PG().SetSetting(settingVulnDBSyncOn, v); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if req.IntervalHours != nil && *req.IntervalHours > 0 {
		if err := s.m.PG().SetSetting(settingVulnDBSyncInterval, strconv.Itoa(*req.IntervalHours)); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if req.CNVDURL != nil {
		// 允许置空 = 停用 CNVD 数据端点。
		if err := s.m.PG().SetSetting(settingVulnCNVDFeedURL, strings.TrimSpace(*req.CNVDURL)); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
