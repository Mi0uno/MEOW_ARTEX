package server

// 记忆库 API(契约 AI_MEMORY_CONTRACTS.md §4 路由表逐字)。
// 注册进 Handler() 的 mux 后统一走 requireAuth(注册器模式仿 registerC2Routes)。

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

// registerMemoryRoutes 注册记忆库全部路由。
func (s *Server) registerMemoryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/memory/companies", s.memoryCompanies)
	mux.HandleFunc("GET /api/memory/overview", s.memoryOverview)
	mux.HandleFunc("GET /api/memory/facts", s.memoryListFacts)
	mux.HandleFunc("POST /api/memory/facts", s.memoryCreateFact)
	mux.HandleFunc("POST /api/memory/facts/{id}/invalidate", s.memoryInvalidateFact)
	mux.HandleFunc("DELETE /api/memory/facts/{id}", s.memoryDeleteFact)
	mux.HandleFunc("GET /api/memory/runs", s.memoryListRuns)
	mux.HandleFunc("GET /api/memory/runs/{id}", s.memoryGetRun)
	mux.HandleFunc("POST /api/memory/runs/{id}/re-reflect", s.memoryReReflect)
	mux.HandleFunc("GET /api/memory/briefing", s.memoryBriefingHandler)
	mux.HandleFunc("GET /api/memory/config", s.memoryGetConfig)
	mux.HandleFunc("PUT /api/memory/config", s.memorySetConfig)
}

// memoryCompanies GET /api/memory/companies — 企业维度统计(选择器数据源,无记忆的企业也要返回)。
func (s *Server) memoryCompanies(w http.ResponseWriter, r *http.Request) {
	stats, err := s.m.PG().MemoryCompanyStats()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if stats == nil {
		stats = []db.MemoryCompanyStat{}
	}
	writeJSON(w, 200, map[string]any{"companies": stats})
}

// memoryOverview GET /api/memory/overview?company_id= — 概览计数。
func (s *Server) memoryOverview(w http.ResponseWriter, r *http.Request) {
	companyID, _ := strconv.ParseInt(r.URL.Query().Get("company_id"), 10, 64)
	overview, err := s.m.PG().MemoryOverview(companyID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, overview)
}

// memoryListFacts GET /api/memory/facts?company_id=&scope_kind=&kind=&q=&include_invalid=1&limit=&offset=
func (s *Server) memoryListFacts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	companyID, _ := strconv.ParseInt(q.Get("company_id"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	facts, total, err := s.m.PG().ListMemoryFacts(db.MemoryFactFilter{
		CompanyID:      companyID,
		ScopeKind:      strings.TrimSpace(q.Get("scope_kind")),
		Kind:           strings.TrimSpace(q.Get("kind")),
		Q:              strings.TrimSpace(q.Get("q")),
		IncludeInvalid: q.Get("include_invalid") == "1",
		Limit:          limit, Offset: offset,
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if facts == nil {
		facts = []*db.MemoryFact{}
	}
	writeJSON(w, 200, map[string]any{"facts": facts, "total": total})
}

// memoryCreateFact POST /api/memory/facts — 人工新增(scope_kind 必填非 global,content 必填非空)。
func (s *Server) memoryCreateFact(w http.ResponseWriter, r *http.Request) {
	var p struct {
		ScopeKind  string  `json:"scope_kind"`
		ScopeID    int64   `json:"scope_id"`
		Kind       string  `json:"kind"`
		Subject    string  `json:"subject"`
		Content    string  `json:"content"`
		AssetID    int64   `json:"asset_id"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	scopeKind := strings.TrimSpace(p.ScopeKind)
	if scopeKind == "" || scopeKind == "global" {
		writeErr(w, 400, "scope_kind 必填且不能为 global")
		return
	}
	if scopeKind == "company" && p.ScopeID <= 0 {
		writeErr(w, 400, "scope_kind=company 时 scope_id 必填(>0)")
		return
	}
	if strings.TrimSpace(p.Content) == "" {
		writeErr(w, 400, "content 必填")
		return
	}
	kind := strings.TrimSpace(p.Kind)
	if !memoryFactKinds[kind] {
		kind = "other"
	}
	conf := p.Confidence
	if conf <= 0 || conf > 1 {
		conf = 0.5
	}
	now := time.Now()
	id, err := s.m.PG().CreateMemoryFact(&db.MemoryFact{
		ScopeKind: scopeKind, ScopeID: p.ScopeID,
		Kind: kind, Subject: strings.TrimSpace(p.Subject), Content: strings.TrimSpace(p.Content),
		AssetID:      p.AssetID,
		SourceTaskID: 0, SourceKind: "manual",
		Confidence: conf, ObservedAt: now, ValidFrom: now,
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": id})
}

// memoryInvalidateFact POST /api/memory/facts/{id}/invalidate
func (s *Server) memoryInvalidateFact(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad fact id")
		return
	}
	if err := s.m.PG().InvalidateMemoryFact(id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"updated": true})
}

// memoryDeleteFact DELETE /api/memory/facts/{id}
func (s *Server) memoryDeleteFact(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad fact id")
		return
	}
	if err := s.m.PG().DeleteMemoryFact(id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": true})
}

// memoryListRuns GET /api/memory/runs?company_id=&task_id=&status=&limit=&offset=
func (s *Server) memoryListRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	companyID, _ := strconv.ParseInt(q.Get("company_id"), 10, 64)
	taskID, _ := strconv.ParseInt(q.Get("task_id"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	runs, total, err := s.m.PG().ListMemoryRuns(db.MemoryRunFilter{
		CompanyID: companyID, TaskID: taskID, Status: q.Get("status"), Limit: limit, Offset: offset,
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if runs == nil {
		runs = []*db.MemoryRun{}
	}
	writeJSON(w, 200, map[string]any{"runs": runs, "total": total})
}

// memoryGetRun GET /api/memory/runs/{id} — 404 文案含「不存在」(wire 契约)。
func (s *Server) memoryGetRun(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad run id")
		return
	}
	run, err := s.m.PG().MemoryRun(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if run == nil {
		writeErr(w, 404, "复盘记录不存在")
		return
	}
	writeJSON(w, 200, run)
}

// memoryReReflect POST /api/memory/runs/{id}/re-reflect — 202 {run_id};
// 该 run 已 pending → 409(文案含「进行中」);不存在 → 404。
func (s *Server) memoryReReflect(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad run id")
		return
	}
	run, err := s.m.PG().MemoryRun(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if run == nil {
		writeErr(w, 404, "复盘记录不存在")
		return
	}
	if run.Status == memoryStatusPending {
		writeErr(w, 409, "复盘进行中，请稍后再试")
		return
	}
	go s.startMemoryReflect(run.TaskID, memorySourceManual)
	writeJSON(w, 202, map[string]any{"run_id": id})
}

// memoryBriefingHandler GET /api/memory/briefing?task_id= — 任务不存在 → 404;
// 无内容 → 200 {has_content:false, text:""}(前端显示「无历史记忆」,不是错误)。
func (s *Server) memoryBriefingHandler(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimSpace(r.URL.Query().Get("task_id"))
	if taskID == "" {
		writeErr(w, 400, "task_id 必填")
		return
	}
	if _, ok := s.m.Task(taskID); !ok {
		if pt, err := s.m.PG().GetTask(mustParseInt64(taskID)); err != nil || pt == nil {
			writeErr(w, 404, "任务不存在")
			return
		}
	}
	sections, text, ok := s.memoryBriefingSections(taskID)
	resp := map[string]any{
		"task_id":      taskID,
		"has_content":  ok,
		"text":         text,
		"sections":     []memorySection{},
		"generated_at": time.Now().Format(time.RFC3339),
	}
	if sections != nil {
		resp["sections"] = sections
	}
	writeJSON(w, 200, resp)
}

// memoryGetConfig GET /api/memory/config — 缺省 = 开启(值不为 "false" 即开)。
func (s *Server) memoryGetConfig(w http.ResponseWriter, r *http.Request) {
	on := true
	if v, _, err := s.m.PG().GetSetting(settingMemoryAIReflect); err == nil && v == "false" {
		on = false
	}
	writeJSON(w, 200, map[string]any{"ai_reflect": on})
}

// memorySetConfig PUT /api/memory/config {ai_reflect:bool} — 布尔持久化为 "true"/"false"。
func (s *Server) memorySetConfig(w http.ResponseWriter, r *http.Request) {
	var p struct {
		AIReflect *bool `json:"ai_reflect"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&p); err != nil || p.AIReflect == nil {
		writeErr(w, 400, "ai_reflect(bool) 必填")
		return
	}
	v := "false"
	if *p.AIReflect {
		v = "true"
	}
	if err := s.m.PG().SetSetting(settingMemoryAIReflect, v); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"saved": true})
}

// mustParseInt64 宽松解析任务 id(解析失败返回 0,调用方据此走 not-found)。
func mustParseInt64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
