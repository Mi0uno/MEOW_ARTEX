package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
)

// GET /api/intercept/guard-overview 是 AI监管页的门限总览数据源。响应形状必须与
// web/src/lib/api.ts 的 interceptGuardOverview 严格一致,且集合类字段永远不能是
// JSON null(前端对 null 取键会崩,本项目已踩过这个坑)。
func TestInterceptGuardOverviewHTTP(t *testing.T) {
	dsn, err := testDSN()
	if err != nil {
		t.Skip("no test database configured")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	m := &Manager{pg: d, interceptor: intercept.New(d)}
	s := &Server{m: m, jwtKey: []byte("guard-overview-test-key")}
	h := s.Handler()
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	do := func(authenticated bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/intercept/guard-overview", nil)
		if authenticated {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := do(false); rec.Code != 401 {
		t.Fatalf("unauthenticated overview: %d", rec.Code)
	}
	rec := do(true)
	if rec.Code != 200 {
		t.Fatalf("overview: %d %s", rec.Code, rec.Body.String())
	}
	// 先按原始 JSON 检查:null 会被 unmarshal 成 nil,必须在这层拦住。
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"judge_enabled", "jev_enabled", "jev_configured", "enabled_tools", "rules_total", "rules_enabled", "action_counts", "status_counts"} {
		v, ok := raw[key]
		if !ok {
			t.Fatalf("missing field %q in %s", key, rec.Body.String())
		}
		if string(v) == "null" {
			t.Fatalf("field %q serialised as null — the AI监管页 would break on it", key)
		}
	}
	var body struct {
		JudgeEnabled  bool           `json:"judge_enabled"`
		JevEnabled    bool           `json:"jev_enabled"`
		JevConfigured bool           `json:"jev_configured"`
		EnabledTools  []string       `json:"enabled_tools"`
		RulesTotal    int            `json:"rules_total"`
		RulesEnabled  int            `json:"rules_enabled"`
		ActionCounts  map[string]int `json:"action_counts"`
		StatusCounts  map[string]int `json:"status_counts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.RulesEnabled > body.RulesTotal {
		t.Fatalf("rules_enabled=%d exceeds rules_total=%d", body.RulesEnabled, body.RulesTotal)
	}
	// 聚合值必须与逐行统计一致(证明是按 action/status 分组而不是写死的)。
	var wantRules int
	if err := d.QueryRow(`SELECT COUNT(*) FROM intercept_rules WHERE action='deny'`).Scan(&wantRules); err != nil {
		t.Fatal(err)
	}
	if body.ActionCounts["deny"] != wantRules {
		t.Fatalf("action_counts[deny]=%d, want %d", body.ActionCounts["deny"], wantRules)
	}
	var wantFlagged int
	if err := d.QueryRow(`SELECT COUNT(*) FROM intercept_pending WHERE status='flagged'`).Scan(&wantFlagged); err != nil {
		t.Fatal(err)
	}
	if body.StatusCounts["flagged"] != wantFlagged {
		t.Fatalf("status_counts[flagged]=%d, want %d", body.StatusCounts["flagged"], wantFlagged)
	}
}
