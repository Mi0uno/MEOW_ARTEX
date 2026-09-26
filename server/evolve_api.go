package server

// 自进化 API(契约 AI_MEMORY_CONTRACTS.md §4 路由表逐字):经验洞察 + 攻击剧本的
// 列表/新建/状态流转/删除与总览计数。注册进 Handler() 的 mux 后统一走 requireAuth。

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// registerEvolveRoutes 注册自进化全部路由。
func (s *Server) registerEvolveRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/evolve/overview", s.evolveOverview)
	mux.HandleFunc("GET /api/evolve/insights", s.evolveListInsights)
	mux.HandleFunc("POST /api/evolve/insights", s.evolveCreateInsight)
	mux.HandleFunc("POST /api/evolve/insights/{id}/status", s.evolveSetInsightStatus)
	mux.HandleFunc("DELETE /api/evolve/insights/{id}", s.evolveDeleteInsight)
	mux.HandleFunc("GET /api/evolve/playbooks", s.evolveListPlaybooks)
	mux.HandleFunc("POST /api/evolve/playbooks", s.evolveCreatePlaybook)
	mux.HandleFunc("POST /api/evolve/playbooks/{id}/status", s.evolveSetPlaybookStatus)
	mux.HandleFunc("DELETE /api/evolve/playbooks/{id}", s.evolveDeletePlaybook)
}

// evolveOverview GET /api/evolve/overview — 洞察/剧本按状态计数(页签计数,不受筛选影响)。
func (s *Server) evolveOverview(w http.ResponseWriter, r *http.Request) {
	insights, playbooks, err := s.m.PG().EvolveOverview()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if insights == nil {
		insights = map[string]int64{}
	}
	if playbooks == nil {
		playbooks = map[string]int64{}
	}
	writeJSON(w, 200, map[string]any{"insights": insights, "playbooks": playbooks})
}

// evolveListInsights GET /api/evolve/insights?status=&kind=&category=&q=&company_id=&limit=&offset=
func (s *Server) evolveListInsights(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	companyID, _ := strconv.ParseInt(q.Get("company_id"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	items, total, err := s.m.PG().ListEvolveInsights(db.EvolveInsightFilter{
		Status:    strings.TrimSpace(q.Get("status")),
		Kind:      strings.TrimSpace(q.Get("kind")),
		Category:  strings.TrimSpace(q.Get("category")),
		Q:         strings.TrimSpace(q.Get("q")),
		CompanyID: companyID,
		Limit:     limit, Offset: offset,
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if items == nil {
		items = []*db.EvolveInsight{}
	}
	writeJSON(w, 200, map[string]any{"insights": items, "total": total})
}

// evolveCreateInsight POST /api/evolve/insights — title 必填;scope_kind=company 时 scope_id 必填>0。
func (s *Server) evolveCreateInsight(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Title     string   `json:"title"`
		Content   string   `json:"content"`
		Kind      string   `json:"kind"`
		Category  string   `json:"category"`
		ScopeKind string   `json:"scope_kind"`
		ScopeID   int64    `json:"scope_id"`
		StackTags []string `json:"stack_tags"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	title := strings.TrimSpace(p.Title)
	if title == "" {
		writeErr(w, 400, "title 必填")
		return
	}
	scopeKind := strings.TrimSpace(p.ScopeKind)
	if scopeKind == "" {
		scopeKind = "global"
	}
	if scopeKind == "company" && p.ScopeID <= 0 {
		writeErr(w, 400, "scope_kind=company 时 scope_id 必填(>0)")
		return
	}
	kind := p.Kind
	if kind != "negative" {
		kind = "positive"
	}
	category := strings.TrimSpace(p.Category)
	if category == "" {
		category = "technique"
	}
	tags := p.StackTags
	if tags == nil {
		tags = []string{}
	}
	id, err := s.m.PG().CreateEvolveInsight(&db.EvolveInsight{
		ScopeKind: scopeKind, ScopeID: p.ScopeID,
		StackTags: tags, Category: category, Kind: kind,
		Title: title, Content: strings.TrimSpace(p.Content),
		Status: "candidate", Corroboration: 1,
		SourceTaskIDs: []int64{}, Confidence: 0.5, CreatedBy: "user",
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": id})
}

// validEvolveStatus 状态流转白名单:active/rejected/retired(candidate 也允许,用于撤销)。
func validEvolveStatus(s string) bool {
	switch s {
	case "candidate", "active", "rejected", "retired":
		return true
	}
	return false
}

// evolveSetInsightStatus POST /api/evolve/insights/{id}/status
func (s *Server) evolveSetInsightStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad insight id")
		return
	}
	var p struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if !validEvolveStatus(p.Status) {
		writeErr(w, 400, "status 必须是 candidate|active|rejected|retired")
		return
	}
	if err := s.m.PG().SetEvolveInsightStatus(id, p.Status); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"updated": true})
}

// evolveDeleteInsight DELETE /api/evolve/insights/{id}
func (s *Server) evolveDeleteInsight(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad insight id")
		return
	}
	if err := s.m.PG().DeleteEvolveInsight(id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": true})
}

// evolveListPlaybooks GET /api/evolve/playbooks?status=&q=&company_id=&limit=&offset=
func (s *Server) evolveListPlaybooks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	companyID, _ := strconv.ParseInt(q.Get("company_id"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	items, total, err := s.m.PG().ListEvolvePlaybooks(db.EvolvePlaybookFilter{
		Status:    strings.TrimSpace(q.Get("status")),
		Q:         strings.TrimSpace(q.Get("q")),
		CompanyID: companyID,
		Limit:     limit, Offset: offset,
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if items == nil {
		items = []*db.EvolvePlaybook{}
	}
	writeJSON(w, 200, map[string]any{"playbooks": items, "total": total})
}

// evolveCreatePlaybook POST /api/evolve/playbooks — title 必填。
func (s *Server) evolveCreatePlaybook(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Title         string                  `json:"title"`
		Summary       string                  `json:"summary"`
		ScopeKind     string                  `json:"scope_kind"`
		ScopeID       int64                   `json:"scope_id"`
		StackTags     []string                `json:"stack_tags"`
		Preconditions map[string]any          `json:"preconditions"`
		Steps         []db.EvolvePlaybookStep `json:"steps"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	title := strings.TrimSpace(p.Title)
	if title == "" {
		writeErr(w, 400, "title 必填")
		return
	}
	scopeKind := strings.TrimSpace(p.ScopeKind)
	if scopeKind == "" {
		scopeKind = "global"
	}
	if scopeKind == "company" && p.ScopeID <= 0 {
		writeErr(w, 400, "scope_kind=company 时 scope_id 必填(>0)")
		return
	}
	tags := p.StackTags
	if tags == nil {
		tags = []string{}
	}
	pre := p.Preconditions
	if pre == nil {
		pre = map[string]any{}
	}
	steps := p.Steps
	if steps == nil {
		steps = []db.EvolvePlaybookStep{}
	}
	// 步骤号重排,防前端漏 n/n 断档。
	for i := range steps {
		steps[i].N = i + 1
	}
	id, err := s.m.PG().CreateEvolvePlaybook(&db.EvolvePlaybook{
		Title: title, Summary: strings.TrimSpace(p.Summary),
		ScopeKind: scopeKind, ScopeID: p.ScopeID,
		StackTags: tags, Preconditions: pre, Steps: steps,
		Status: "candidate", Corroboration: 1,
		EvidenceTaskIDs: []int64{}, SourceTaskIDs: []int64{},
		CreatedBy: "user",
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": id})
}

// evolveSetPlaybookStatus POST /api/evolve/playbooks/{id}/status
func (s *Server) evolveSetPlaybookStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad playbook id")
		return
	}
	var p struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if !validEvolveStatus(p.Status) {
		writeErr(w, 400, "status 必须是 candidate|active|rejected|retired")
		return
	}
	if err := s.m.PG().SetEvolvePlaybookStatus(id, p.Status); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"updated": true})
}

// evolveDeletePlaybook DELETE /api/evolve/playbooks/{id}
func (s *Server) evolveDeletePlaybook(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad playbook id")
		return
	}
	if err := s.m.PG().DeleteEvolvePlaybook(id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": true})
}
