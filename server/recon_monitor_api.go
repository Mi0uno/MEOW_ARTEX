package server

// 资产监控 API:总览(config+targets)/全局配置/企业目标三态覆盖/手动触发/
// 轮次列表与详情。wire 形状逐字对齐 MONITOR_CONTRACTS.md「冻结原文:API wire」。
// 列表铁律:集合字段绝不让 nil 漏出去(domains/targets/collectors/new_domains
// 一律初始化空)。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/Autumn-27/artex/db"
)

// monitorConfigDTO GET/PUT config 响应里的 config 对象。
type monitorConfigDTO struct {
	Enabled       bool       `json:"enabled"`
	IntervalHours int        `json:"interval_hours"`
	Running       bool       `json:"running"`
	LastRoundAt   *time.Time `json:"last_round_at"`
}

// monitorLastRunDTO target.last_run 摘要(仅 status/new_count/finished_at)。
type monitorLastRunDTO struct {
	Status     string     `json:"status"`
	NewCount   int        `json:"new_count"`
	FinishedAt *time.Time `json:"finished_at"`
}

// monitorTargetDTO targets[] 元素(overview 与 PUT target 响应同形)。
type monitorTargetDTO struct {
	CompanyID     int64              `json:"company_id"`
	CompanyName   string             `json:"company_name"`
	DomainCount   int                `json:"domain_count"`
	Domains       []string           `json:"domains"`
	Watched       bool               `json:"watched"`
	Enabled       bool               `json:"enabled"`
	IntervalHours *int               `json:"interval_hours"`
	LastRunAt     *time.Time         `json:"last_run_at"`
	NextRunAt     *time.Time         `json:"next_run_at"`
	LastRunID     *int64             `json:"last_run_id"`
	LastRun       *monitorLastRunDTO `json:"last_run"`
}

// monitorTargetDTOOf 把候选企业与(可能缺省的)targets 行合并成 wire 的 target 对象。
// lastRun 为该企业最近一轮的摘要来源(可 nil);monitorOn=全局开关(globalH=有效全局
// 间隔)决定 watched/next_run_at 的展示口径。
func monitorTargetDTOOf(mc db.MonitorCompany, row *db.MonitorTarget, lastRun *db.MonitorRun, monitorOn bool, globalH int) monitorTargetDTO {
	dto := monitorTargetDTO{
		CompanyID:   mc.ID,
		CompanyName: mc.Name,
		DomainCount: mc.DomainCount,
		Domains:     mc.Domains,
		Enabled:     true, // 无行=true
	}
	if dto.Domains == nil {
		dto.Domains = []string{}
	}
	if row != nil {
		dto.Enabled = row.Enabled
		dto.IntervalHours = row.IntervalHours
		dto.LastRunAt = row.LastRunAt
		dto.LastRunID = row.LastRunID
	}
	// 有效监控中 = 全局开 && 有域名范围 && 行未显式退出。
	dto.Watched = monitorOn && dto.DomainCount > 0 && len(dto.Domains) > 0 && dto.Enabled
	if dto.Watched && dto.LastRunAt != nil {
		eff := globalH
		if dto.IntervalHours != nil && *dto.IntervalHours > 0 {
			eff = *dto.IntervalHours
		}
		next := dto.LastRunAt.Add(time.Duration(eff) * time.Hour)
		dto.NextRunAt = &next
	}
	if lastRun != nil {
		dto.LastRun = &monitorLastRunDTO{Status: lastRun.Status, NewCount: lastRun.NewCount, FinishedAt: lastRun.FinishedAt}
	}
	return dto
}

// monitorConfig 汇总当前 config:settings(缺省关/24h)+ running 标志 + 最近一轮完成时间。
func (s *Server) monitorConfig() monitorConfigDTO {
	cfg := monitorConfigDTO{
		Enabled:       s.reconMonitor.enabled(),
		IntervalHours: s.reconMonitor.intervalHours(),
		Running:       s.reconMonitor.isRunning(),
	}
	if t, err := s.m.PG().LatestMonitorRoundAt(); err == nil {
		cfg.LastRoundAt = t
	}
	return cfg
}

// reconMonitorOverview GET /api/recon/monitor — 配置 + 候选企业监控状态总览。
// targets = 候选企业(有 kind=domain 范围)LEFT JOIN targets 行合并。
func (s *Server) reconMonitorOverview(w http.ResponseWriter, _ *http.Request) {
	pg := s.m.PG()
	if pg == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	cfg := s.monitorConfig()
	candidates, err := pg.ListMonitorCandidateCompanies()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	rows, err := pg.ListMonitorTargets()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	rowBy := make(map[int64]*db.MonitorTarget, len(rows))
	var runIDs []int64
	for i := range rows {
		rowBy[rows[i].CompanyID] = &rows[i]
		if rows[i].LastRunID != nil {
			runIDs = append(runIDs, *rows[i].LastRunID)
		}
	}
	runsByID := map[int64]*db.MonitorRun{}
	if len(runIDs) > 0 {
		if m, err := pg.MonitorRunsByIDs(runIDs); err == nil {
			runsByID = m
		}
	}
	targets := make([]monitorTargetDTO, 0, len(candidates))
	for _, mc := range candidates {
		row := rowBy[mc.ID]
		var lastRun *db.MonitorRun
		if row != nil && row.LastRunID != nil {
			lastRun = runsByID[*row.LastRunID]
		}
		targets = append(targets, monitorTargetDTOOf(mc, row, lastRun, cfg.Enabled, cfg.IntervalHours))
	}
	writeJSON(w, 200, map[string]any{"config": cfg, "targets": targets})
}

// reconMonitorSetConfig PUT /api/recon/monitor/config — 全局开关与间隔。
// interval 越界(合法域 [1,720])400;开启时顺带唤醒调度器。返回更新后的 config。
func (s *Server) reconMonitorSetConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled       *bool `json:"enabled"`
		IntervalHours *int  `json:"interval_hours"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if req.IntervalHours != nil && !monitorIntervalInRange(*req.IntervalHours) {
		writeErr(w, 400, fmt.Sprintf("interval_hours 必须在 %d..%d 之间", reconMonitorMinInterval, reconMonitorMaxInterval))
		return
	}
	pg := s.m.PG()
	if pg == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	turningOn := false
	if req.Enabled != nil {
		v := "false"
		if *req.Enabled {
			v = "true"
			turningOn = true
		}
		if err := pg.SetSetting(settingReconMonitorEnabled, v); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if req.IntervalHours != nil {
		if err := pg.SetSetting(settingReconMonitorInterval, strconv.Itoa(*req.IntervalHours)); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if turningOn {
		s.reconMonitor.poke()
	}
	writeJSON(w, 200, s.monitorConfig())
}

// parseMonitorIntervalHours 解析 targets PUT 的 interval_hours 三态:
//   - 字段缺省(raw == nil) → (nil, false, nil) 不动
//   - 显式 null            → (nil, true, nil)  清除覆盖回全局
//   - 正整数               → (&n, false, nil)  设置覆盖
//   - 其它(0/负数/小数/字符串/对象…) → error(调用方 400)
func parseMonitorIntervalHours(raw json.RawMessage) (*int, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	if string(raw) == "null" {
		return nil, true, nil
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return nil, false, errors.New("interval_hours 必须是正整数或 null")
	}
	if n < 1 {
		return nil, false, errors.New("interval_hours 必须是正整数或 null")
	}
	return &n, false, nil
}

// reconMonitorSetTarget PUT /api/recon/monitor/targets/{companyID} — 单企业
// 开关与间隔覆盖。企业不存在 400;返回更新后的 target 对象(同 targets[] 元素形状)。
func (s *Server) reconMonitorSetTarget(w http.ResponseWriter, r *http.Request) {
	companyID, err := strconv.ParseInt(r.PathValue("companyID"), 10, 64)
	if err != nil || companyID <= 0 {
		writeErr(w, 400, "bad company id")
		return
	}
	pg := s.m.PG()
	if pg == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	if c, err := pg.Companies().GetCompany(companyID); err != nil || c == nil {
		writeErr(w, 400, "企业不存在")
		return
	}
	var req struct {
		Enabled       *bool           `json:"enabled"`
		IntervalHours json.RawMessage `json:"interval_hours"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	interval, clearInterval, err := parseMonitorIntervalHours(req.IntervalHours)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	row, err := pg.UpsertMonitorTarget(companyID, req.Enabled, interval, clearInterval)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, s.monitorTargetResponse(companyID, row))
}

// monitorTargetResponse 组 PUT target 的响应:企业 + scope 域名 + 更新后的行 +
// 最近一轮摘要(同 overview 的 targets[] 元素形状)。
func (s *Server) monitorTargetResponse(companyID int64, row *db.MonitorTarget) monitorTargetDTO {
	pg := s.m.PG()
	mc := db.MonitorCompany{ID: companyID, Domains: []string{}}
	if c, err := pg.Companies().GetCompany(companyID); err == nil && c != nil {
		mc.Name = c.Name
	}
	if rules, err := pg.Companies().GetScope(companyID); err == nil {
		for _, r := range rules {
			if r.Kind == "domain" && r.Domain != "" {
				mc.Domains = append(mc.Domains, r.Domain)
				mc.DomainCount++
			}
		}
	}
	var lastRun *db.MonitorRun
	if row != nil && row.LastRunID != nil {
		if runs, err := pg.MonitorRunsByIDs([]int64{*row.LastRunID}); err == nil {
			lastRun = runs[*row.LastRunID]
		}
	}
	cfg := s.monitorConfig()
	return monitorTargetDTOOf(mc, row, lastRun, cfg.Enabled, cfg.IntervalHours)
}

// reconMonitorTrigger POST /api/recon/monitor/trigger — 手动触发。
// 带 company_id(>0):清零该企业水位(last_run_at/last_run_id 置 NULL)后绕过全局
// 开关立即试跑一家;不带:仅唤醒调度器跑所有 due 的(受全局开关约束)。
// 已有轮次在跑 → {"started": false}。
func (s *Server) reconMonitorTrigger(w http.ResponseWriter, r *http.Request) {
	if s.reconMonitor == nil {
		writeErr(w, 503, "监控调度器未初始化")
		return
	}
	var req struct {
		CompanyID int64 `json:"company_id"`
	}
	// body 可省略(不带 company_id = 跑所有 due 的)。
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	pg := s.m.PG()
	if pg == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	started := false
	if req.CompanyID > 0 {
		if c, err := pg.Companies().GetCompany(req.CompanyID); err != nil || c == nil {
			writeErr(w, 400, "企业不存在")
			return
		}
		if err := pg.ResetMonitorTargetRun(req.CompanyID); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		started = s.reconMonitor.triggerCompany(req.CompanyID)
	} else {
		started = s.reconMonitor.trigger()
	}
	writeJSON(w, 200, map[string]any{"started": started})
}

// reconMonitorRuns GET /api/recon/monitor/runs?company_id=&status=&limit=&offset=
// 列表 new_domains 一律空数组(详情接口才有,防大 JSON 拖列表);total 在过滤之后算
// (db 层保证)。running 状态可 5s 轮询。
func (s *Server) reconMonitorRuns(w http.ResponseWriter, r *http.Request) {
	pg := s.m.PG()
	if pg == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	q := r.URL.Query()
	limit := atoiDefault(q.Get("limit"), 50)
	if limit <= 0 {
		limit = 50
	} else if limit > 200 {
		limit = 200
	}
	offset := atoiDefault(q.Get("offset"), 0)
	if offset < 0 {
		offset = 0
	}
	companyID, _ := strconv.ParseInt(q.Get("company_id"), 10, 64)
	runs, total, err := pg.ListMonitorRuns(db.MonitorRunFilter{
		CompanyID: companyID,
		Status:    q.Get("status"),
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if runs == nil {
		runs = []db.MonitorRun{}
	}
	for i := range runs {
		if runs[i].Collectors == nil {
			runs[i].Collectors = json.RawMessage("[]")
		}
		if runs[i].NewDomains == nil {
			runs[i].NewDomains = json.RawMessage("[]")
		}
	}
	writeJSON(w, 200, map[string]any{"total": total, "runs": runs})
}

// reconMonitorRunDetail GET /api/recon/monitor/runs/{id} — 单轮完整详情(含
// new_domains 清单)。不存在 404。
func (s *Server) reconMonitorRunDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad run id")
		return
	}
	pg := s.m.PG()
	if pg == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	run, err := pg.GetMonitorRun(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if run == nil {
		writeErr(w, 404, "monitor run not found")
		return
	}
	if run.Collectors == nil {
		run.Collectors = json.RawMessage("[]")
	}
	if run.NewDomains == nil {
		run.NewDomains = json.RawMessage("[]")
	}
	writeJSON(w, 200, run)
}

// registerReconMonitorRoutes 挂载资产监控端点(路由区锚点:POST /api/recon/plan 之后)。
func (s *Server) registerReconMonitorRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/recon/monitor", s.reconMonitorOverview)
	mux.HandleFunc("PUT /api/recon/monitor/config", s.reconMonitorSetConfig)
	mux.HandleFunc("PUT /api/recon/monitor/targets/{companyID}", s.reconMonitorSetTarget)
	mux.HandleFunc("POST /api/recon/monitor/trigger", s.reconMonitorTrigger)
	mux.HandleFunc("GET /api/recon/monitor/runs", s.reconMonitorRuns)
	mux.HandleFunc("GET /api/recon/monitor/runs/{id}", s.reconMonitorRunDetail)
}
