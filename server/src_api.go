package server

// SRC 模式 API:企业 SRC 规范 CRUD + 成果 CRUD + 概览。

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// ---- SRC 规范 ----

// listSRCProfiles GET /api/src/profiles?company_id=&q=&limit=&offset=
func (s *Server) listSRCProfiles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	companyID, _ := strconv.ParseInt(q.Get("company_id"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	items, total, err := s.m.PG().ListSRCProfiles(companyID, q.Get("q"), limit, offset)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if items == nil {
		items = []*db.SRCProfile{}
	}
	writeJSON(w, 200, map[string]any{"profiles": items, "total": total})
}

// getSRCProfile GET /api/src/profiles/{id}
func (s *Server) getSRCProfile(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad profile id")
		return
	}
	p, err := s.m.PG().GetSRCProfile(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if p == nil {
		writeErr(w, 404, "profile not found")
		return
	}
	writeJSON(w, 200, map[string]any{"profile": p})
}

// srcProfilePayload 是新建/更新规范的请求体。
type srcProfilePayload struct {
	CompanyID    int64  `json:"company_id"`
	Name         string `json:"name"`
	Platform     string `json:"platform"`
	ScopeNote    string `json:"scope_note"`
	Forbidden    string `json:"forbidden"`
	AllowedTypes string `json:"allowed_types"`
	RateLimit    string `json:"rate_limit"`
	Rules        string `json:"rules"`
	Enabled      *bool  `json:"enabled"`
}

// saveSRCProfile POST /api/src/profiles — 按企业 upsert。
func (s *Server) saveSRCProfile(w http.ResponseWriter, r *http.Request) {
	var p srcProfilePayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	enabled := true
	if p.Enabled != nil {
		enabled = *p.Enabled
	}
	prof, err := s.m.PG().UpsertSRCProfile(db.SRCProfileInput{
		CompanyID: p.CompanyID, Name: p.Name, Platform: p.Platform, ScopeNote: p.ScopeNote,
		Forbidden: p.Forbidden, AllowedTypes: p.AllowedTypes, RateLimit: p.RateLimit,
		Rules: p.Rules, Enabled: enabled,
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"profile": prof})
}

// deleteSRCProfile DELETE /api/src/profiles/{id}
func (s *Server) deleteSRCProfile(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad profile id")
		return
	}
	deleted, err := s.m.PG().DeleteSRCProfile(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

// ---- SRC 成果 ----

// listSRCRewards GET /api/src/rewards?company_id=&task_id=&status=&severity=&q=&sort=&limit=&offset=
func (s *Server) listSRCRewards(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	companyID, _ := strconv.ParseInt(q.Get("company_id"), 10, 64)
	taskID, _ := strconv.ParseInt(q.Get("task_id"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	items, total, counts, err := s.m.PG().ListSRCRewards(db.SRCRewardFilter{
		CompanyID: companyID, TaskID: taskID,
		Status: strings.TrimSpace(q.Get("status")), Severity: strings.TrimSpace(q.Get("severity")),
		Q: strings.TrimSpace(q.Get("q")), Sort: strings.TrimSpace(q.Get("sort")),
		Limit: limit, Offset: offset,
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if items == nil {
		items = []*db.SRCReward{}
	}
	writeJSON(w, 200, map[string]any{"rewards": items, "total": total, "counts": counts})
}

// srcRewardPayload 是新建/更新成果的请求体。
type srcRewardPayload struct {
	Title       string `json:"title"`
	Severity    string `json:"severity"`
	VulnClass   string `json:"vuln_class"`
	Status      string `json:"status"`
	PlatformRef string `json:"platform_ref"`
	Feedback    string `json:"feedback"`
	CompanyID   *int64 `json:"company_id"`
	TaskID      *int64 `json:"task_id"`
	FindingID   *int64 `json:"finding_id"`
	EntryID     *int64 `json:"entry_id"`
}

// createSRCReward POST /api/src/rewards
func (s *Server) createSRCReward(w http.ResponseWriter, r *http.Request) {
	var p srcRewardPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	rw, err := s.m.PG().CreateSRCReward(db.SRCRewardInput{
		CompanyID: p.CompanyID, TaskID: p.TaskID, FindingID: p.FindingID, EntryID: p.EntryID,
		Title: p.Title, Severity: p.Severity, VulnClass: p.VulnClass, Status: p.Status,
		PlatformRef: p.PlatformRef, Feedback: p.Feedback,
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"reward": rw})
}

// updateSRCReward PUT /api/src/rewards/{id} — 主要更新提交状态与平台反馈。
func (s *Server) updateSRCReward(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad reward id")
		return
	}
	var p srcRewardPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if err := s.m.PG().UpdateSRCReward(id, db.SRCRewardInput{
		Title: p.Title, Severity: p.Severity, VulnClass: p.VulnClass,
		Status: p.Status, PlatformRef: p.PlatformRef, Feedback: p.Feedback,
	}); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	rw, err := s.m.PG().GetSRCReward(id)
	if err != nil || rw == nil {
		writeErr(w, 404, "reward not found")
		return
	}
	writeJSON(w, 200, map[string]any{"reward": rw})
}

// deleteSRCReward DELETE /api/src/rewards/{id}
func (s *Server) deleteSRCReward(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad reward id")
		return
	}
	deleted, err := s.m.PG().DeleteSRCReward(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

// srcOverview GET /api/src/overview — 成果概览(状态分布 + 企业维度)。
func (s *Server) srcOverview(w http.ResponseWriter, r *http.Request) {
	st, err := s.m.PG().SRCRewardOverview()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, st)
}

// applySRCConstraints 把企业的 SRC 规范转成任务的 deny/allow 约束写入 task_constraints。
// 这是「防止 AI 越界」的落地点:约束经既有机制注入 Planner/Worker 系统提示
// (agent/constraints.go 的 constraintBlock),对模型是最高优先级边界。
// 创建 SRC 任务时调用;幂等由调用方保证(只在建任务时写一次)。
func (s *Server) applySRCConstraints(t *Task, companyID int64) {
	if t == nil || companyID <= 0 {
		return
	}
	prof, err := s.m.PG().GetSRCProfileByCompany(companyID)
	if err != nil {
		log.Printf("[src] 读取企业 #%d 的 SRC 规范失败: %v", companyID, err)
		return
	}
	if prof == nil {
		log.Printf("[src] 企业 #%d 没有 SRC 规范,任务将不受 SRC 边界约束", companyID)
		return
	}
	store := s.m.PG().Exploration(t.ExpID)
	written := 0
	// 授权范围与允许类型 → allow 约束(明确能做什么)。
	if strings.TrimSpace(prof.ScopeNote) != "" {
		if _, err := store.AddConstraint("allow", "SRC 授权范围:"+prof.ScopeNote, "system"); err == nil {
			written++
		}
	}
	if strings.TrimSpace(prof.AllowedTypes) != "" {
		if _, err := store.AddConstraint("allow", "允许的测试类型:"+prof.AllowedTypes, "system"); err == nil {
			written++
		}
	}
	if strings.TrimSpace(prof.RateLimit) != "" {
		if _, err := store.AddConstraint("deny", "速率/强度限制(不得超出):"+prof.RateLimit, "system"); err == nil {
			written++
		}
	}
	// 禁止事项逐行 → 独立 deny 约束(每条都单独注入,便于规划者逐条遵守)。
	for _, line := range strings.Split(prof.Forbidden, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
		if line == "" {
			continue
		}
		if _, err := store.AddConstraint("deny", line, "system"); err == nil {
			written++
		}
	}
	// 额外规范正文整体作为一条 deny 约束(兜底,包含平台特有规则)。
	if strings.TrimSpace(prof.Rules) != "" {
		if _, err := store.AddConstraint("deny", "SRC 平台规范:"+prof.Rules, "system"); err == nil {
			written++
		}
	}
	log.Printf("[src] 任务 %s 已注入企业 #%d 的 SRC 规范(平台=%s),%d 条约束",
		t.ID, companyID, prof.Platform, written)
}
