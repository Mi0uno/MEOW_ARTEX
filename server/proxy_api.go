package server

// 代理中心 REST 接口。
//
// 三块:
//   代理组   /api/proxy/groups…           组的增删改查 + 进入组后的代理/源管理
//   测活     /api/proxy/groups/{id}/check 立即测活 + 批次台账
//   仪表盘   /api/proxy/dashboard         流量与调度总览(序列 + 排行 + 出网状态)
//
// 出网切换 /api/proxy/egress 是「本地裸网 ⇄ 代理池」的开关。

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/proxycenter"
)

// settings keys(与 Manager 里的其它设置同一套 KV)。
const (
	settingProxyEgressMode  = "proxy_egress_mode"  // direct | pool
	settingProxyActiveGroup = "proxy_active_group" // 代理池模式下选中的组 id
)

// registerProxyRoutes 注册代理中心全部路由。
func (s *Server) registerProxyRoutes(mux *http.ServeMux) {
	// ---- 出网模式(裸网 ⇄ 代理池) ----
	mux.HandleFunc("GET /api/proxy/egress", s.getProxyEgress)
	mux.HandleFunc("PUT /api/proxy/egress", s.setProxyEgress)

	// ---- 仪表盘 ----
	mux.HandleFunc("GET /api/proxy/dashboard", s.proxyDashboard)
	mux.HandleFunc("GET /api/proxy/requests", s.listProxyRequests)
	mux.HandleFunc("DELETE /api/proxy/requests", s.purgeProxyRequests)

	// ---- 代理组 ----
	mux.HandleFunc("GET /api/proxy/groups", s.listProxyGroups)
	mux.HandleFunc("POST /api/proxy/groups", s.createProxyGroup)
	mux.HandleFunc("GET /api/proxy/groups/{id}", s.getProxyGroup)
	mux.HandleFunc("PUT /api/proxy/groups/{id}", s.updateProxyGroup)
	mux.HandleFunc("DELETE /api/proxy/groups/{id}", s.deleteProxyGroup)

	// ---- 组内代理 ----
	mux.HandleFunc("GET /api/proxy/groups/{id}/proxies", s.listProxies)
	mux.HandleFunc("POST /api/proxy/groups/{id}/proxies", s.createProxy)
	mux.HandleFunc("DELETE /api/proxy/groups/{id}/proxies", s.deleteProxies)
	mux.HandleFunc("PUT /api/proxy/{id}", s.updateProxy)
	mux.HandleFunc("DELETE /api/proxy/{id}", s.deleteProxy)

	// ---- 代理池 API 接入 ----
	mux.HandleFunc("GET /api/proxy/providers", s.listProxyProviders)
	mux.HandleFunc("GET /api/proxy/groups/{id}/sources", s.listProxySources)
	mux.HandleFunc("POST /api/proxy/groups/{id}/sources", s.createProxySource)
	mux.HandleFunc("POST /api/proxy/groups/{id}/sources/auto", s.autoConnectProxySources)
	mux.HandleFunc("PUT /api/proxy/sources/{id}", s.updateProxySource)
	mux.HandleFunc("DELETE /api/proxy/sources/{id}", s.deleteProxySource)
	mux.HandleFunc("POST /api/proxy/sources/{id}/sync", s.syncProxySource)

	// ---- 测活 ----
	mux.HandleFunc("POST /api/proxy/groups/{id}/check", s.checkProxies)
	mux.HandleFunc("GET /api/proxy/check-runs", s.listProxyCheckRuns)

	// ---- 调度规则(优先级升降级) ----
	mux.HandleFunc("GET /api/proxy/schedule", s.getProxySchedule)
	mux.HandleFunc("PUT /api/proxy/groups/{id}/sched-rules", s.updateProxySchedRules)

	// ---- 地区识别 ----
	mux.HandleFunc("POST /api/proxy/groups/{id}/geo", s.resolveProxyGeo)
	mux.HandleFunc("GET /api/proxy/countries", s.listProxyCountries)
}

// proxyReady 检查代理中心是否可用(统一 503 出口)。
func (s *Server) proxyReady(w http.ResponseWriter) bool {
	if s.m == nil || s.m.PG() == nil {
		writeErr(w, 503, "数据库未初始化")
		return false
	}
	return true
}

// proxyPathID 解析路径里的 id。
func proxyPathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "无效的 id")
		return 0, false
	}
	return id, true
}

// ---- 出网模式 ----

// getProxyEgress GET /api/proxy/egress — 当前出网模式与统计。
func (s *Server) getProxyEgress(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	writeJSON(w, 200, s.proxyEgressStatus())
}

// proxyEgressStatus 组装出网状态(两个接口共用)。
func (s *Server) proxyEgressStatus() map[string]any {
	mode, groupID := "direct", int64(0)
	activeName, listenAddr, activeProxy := "", "", ""
	loadedGroups := 0
	if pc := s.m.ProxyCenter(); pc != nil {
		mode, groupID = pc.EgressMode()
		loadedGroups = len(pc.PoolStatuses())
		listenAddr = pc.ActiveProxyAddr()
		activeProxy = pc.ActiveProxy()
	}
	if groupID > 0 {
		if g, err := s.m.PG().GetProxyGroup(groupID); err == nil && g != nil {
			activeName = g.Name
		}
	}
	total, alive, dead, _ := s.m.PG().ProxyCountsByStatus()
	effective := activeProxy
	if mode != "pool" {
		// 裸网模式:生效出口是全局代理(可能为空=直连)。
		effective = s.m.GlobalProxy()
	}
	return map[string]any{
		"mode":              mode,
		"active_group_id":   groupID,
		"active_group_name": activeName,
		"listen_addr":       listenAddr,
		"effective_proxy":   effective,
		"loaded_groups":     loadedGroups,
		"total_proxies":     total,
		"alive_proxies":     alive,
		"dead_proxies":      dead,
		"running":           s.m.ProxyCenter() != nil,
	}
}

// setProxyEgress PUT /api/proxy/egress — 切换裸网/代理池。
//
// 切换后必须重建 agent:出网地址是在 agent 构造时注入的(SetProxy),
// 不重建的话已经在跑的 agent 仍用旧地址,用户会以为切换没生效。
func (s *Server) setProxyEgress(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	var req struct {
		Mode    string `json:"mode"`
		GroupID int64  `json:"group_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	req.Mode = strings.TrimSpace(req.Mode)
	if !pgdb.ValidEgressMode(req.Mode) {
		writeErr(w, 400, "mode 必须是 direct 或 pool")
		return
	}
	pc := s.m.ProxyCenter()
	if pc == nil {
		writeErr(w, 503, "代理中心未初始化")
		return
	}
	if req.Mode == "pool" {
		if req.GroupID <= 0 {
			writeErr(w, 400, "代理池模式必须指定代理组")
			return
		}
		if g, err := s.m.PG().GetProxyGroup(req.GroupID); err != nil {
			writeErr(w, 500, err.Error())
			return
		} else if g == nil {
			writeErr(w, 404, "代理组不存在")
			return
		}
		if err := pc.EnsurePool(req.GroupID); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if err := pc.SetEgress(req.Mode, req.GroupID); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.m.PG().SetSetting(settingProxyEgressMode, req.Mode); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := s.m.PG().SetSetting(settingProxyActiveGroup, strconv.FormatInt(req.GroupID, 10)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// 重建 agent,让新出网地址对之后启动的 run 生效。
	s.rebuildAgentsForEgress()
	log.Printf("[proxy-center] 出网模式切换为 %s(组 #%d)", req.Mode, req.GroupID)
	writeJSON(w, 200, s.proxyEgressStatus())
}

// rebuildAgentsForEgress 重建 agent 装配,把新出网地址注入进去。
//
// applyLLM 是本项目既有的「重建全部 agent」入口(改全局代理/流量捕获时也走它),
// 复用它可保证出网地址、提示词变量、工具集三者的装配口径一致。
func (s *Server) rebuildAgentsForEgress() {
	cfg, ok := s.loadLLMConfig()
	if !ok {
		log.Printf("[proxy-center] 无可用 LLM 配置,跳过 agent 重建(出网地址对旧 agent 不生效)")
		return
	}
	if err := s.applyLLM(cfg); err != nil {
		log.Printf("[proxy-center] 重建 agent 失败(出网地址可能对旧 agent 不生效): %v", err)
	}
}

// ---- 仪表盘 ----

// proxyDashboard GET /api/proxy/dashboard?window_mins=&group_id=
// 一次返回总览 + 时间序列 + 按代理的调度排行 + 出网状态(前端一次请求取齐)。
func (s *Server) proxyDashboard(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	q := r.URL.Query()
	windowMins := atoiDefault(q.Get("window_mins"), 60)
	if windowMins <= 0 || windowMins > 60*24*7 {
		windowMins = 60
	}
	groupID, _ := strconv.ParseInt(q.Get("group_id"), 10, 64)
	since := time.Now().Add(-time.Duration(windowMins) * time.Minute)

	summary, err := s.m.PG().ProxyTrafficSummarySince(since, groupID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	summary.WindowMins = windowMins
	series, err := s.m.PG().ProxyTrafficSeries(since, groupID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	usage, err := s.m.PG().ProxyUsageSince(since, groupID, 20)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"summary": summary,
		"series":  series,
		"usage":   usage,
		"egress":  s.proxyEgressStatus(),
	})
}

// listProxyRequests GET /api/proxy/requests?group_id=&proxy_id=&task_id=&only_failed=&host=&q=&limit=&offset=
func (s *Server) listProxyRequests(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	q := r.URL.Query()
	groupID, _ := strconv.ParseInt(q.Get("group_id"), 10, 64)
	proxyID, _ := strconv.ParseInt(q.Get("proxy_id"), 10, 64)
	taskID, _ := strconv.ParseInt(q.Get("task_id"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	items, total, err := s.m.PG().ListProxyRequests(pgdb.ProxyRequestFilter{
		GroupID:    groupID,
		ProxyID:    proxyID,
		TaskID:     taskID,
		OnlyFailed: q.Get("only_failed") == "1",
		Host:       strings.TrimSpace(q.Get("host")),
		Q:          strings.TrimSpace(q.Get("q")),
		Limit:      limit,
		Offset:     offset,
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if items == nil {
		items = []*pgdb.ProxyRequest{}
	}
	writeJSON(w, 200, map[string]any{"requests": items, "total": total})
}

// purgeProxyRequests DELETE /api/proxy/requests?before_mins=
// before_mins=0 表示清空全部流水。
func (s *Server) purgeProxyRequests(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	mins := atoiDefault(r.URL.Query().Get("before_mins"), 0)
	before := time.Now()
	if mins > 0 {
		before = before.Add(-time.Duration(mins) * time.Minute)
	} else {
		// 清空全部:用一个足够远的未来时间,让 "ts < before" 命中所有行。
		before = before.Add(24 * time.Hour)
	}
	n, err := s.m.PG().PurgeProxyRequests(before)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": n})
}

// ---- 代理组 ----

// listProxyGroups GET /api/proxy/groups?kind=&q=&sort=&desc=&limit=&offset=
func (s *Server) listProxyGroups(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	items, total, counts, err := s.m.PG().ListProxyGroups(pgdb.ProxyGroupFilter{
		Kind:   strings.TrimSpace(q.Get("kind")),
		Q:      strings.TrimSpace(q.Get("q")),
		Sort:   strings.TrimSpace(q.Get("sort")),
		Desc:   q.Get("desc") != "0",
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if items == nil {
		items = []*pgdb.ProxyGroup{}
	}
	writeJSON(w, 200, map[string]any{"groups": items, "total": total, "counts": counts})
}

// proxyGroupPayload 是新建/更新代理组的请求体。
type proxyGroupPayload struct {
	Name                 string `json:"name"`
	Kind                 string `json:"kind"`
	Strategy             string `json:"strategy"`
	ListenPort           int    `json:"listen_port"`
	Enabled              *bool  `json:"enabled"`
	AutoCheck            *bool  `json:"auto_check"`
	CheckIntervalMinutes int    `json:"check_interval_minutes"`
	TestURL              string `json:"test_url"`
	TimeoutMs            int    `json:"timeout_ms"`
	MaxFailures          int    `json:"max_failures"`
	Concurrency          int    `json:"concurrency"`
	Remark               string `json:"remark"`
}

// toInput 转成 db 输入(布尔用指针以便「未传=默认开」)。
func (p proxyGroupPayload) toInput() pgdb.ProxyGroupInput {
	enabled := true
	if p.Enabled != nil {
		enabled = *p.Enabled
	}
	autoCheck := true
	if p.AutoCheck != nil {
		autoCheck = *p.AutoCheck
	}
	return pgdb.ProxyGroupInput{
		Name: p.Name, Kind: p.Kind, Strategy: p.Strategy, ListenPort: p.ListenPort,
		Enabled: enabled, AutoCheck: autoCheck, CheckIntervalMinutes: p.CheckIntervalMinutes,
		TestURL: p.TestURL, TimeoutMs: p.TimeoutMs, MaxFailures: p.MaxFailures,
		Concurrency: p.Concurrency, Remark: p.Remark,
	}
}

// createProxyGroup POST /api/proxy/groups
func (s *Server) createProxyGroup(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	var p proxyGroupPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	g, err := s.m.PG().CreateProxyGroup(p.toInput())
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	log.Printf("[proxy-center] 新建代理组 «%s»(#%d)", g.Name, g.ID)
	writeJSON(w, 201, map[string]any{"group": g})
}

// getProxyGroup GET /api/proxy/groups/{id}
func (s *Server) getProxyGroup(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	id, ok := proxyPathID(w, r)
	if !ok {
		return
	}
	g, err := s.m.PG().GetProxyGroup(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if g == nil {
		writeErr(w, 404, "代理组不存在")
		return
	}
	writeJSON(w, 200, map[string]any{"group": g})
}

// updateProxyGroup PUT /api/proxy/groups/{id}
func (s *Server) updateProxyGroup(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	id, ok := proxyPathID(w, r)
	if !ok {
		return
	}
	var p proxyGroupPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if err := s.m.PG().UpdateProxyGroup(id, p.toInput()); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	g, err := s.m.PG().GetProxyGroup(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if g == nil {
		writeErr(w, 404, "代理组不存在")
		return
	}
	// 策略/端口/启用状态可能变了:停掉旧监听器,按需重建。
	if pc := s.m.ProxyCenter(); pc != nil {
		pc.StopPool(id)
		if g.Enabled {
			if err := pc.EnsurePool(id); err != nil {
				log.Printf("[proxy-center] 重建组 #%d 监听器失败: %v", id, err)
			}
		}
	}
	writeJSON(w, 200, map[string]any{"group": g})
}

// deleteProxyGroup DELETE /api/proxy/groups/{id}
func (s *Server) deleteProxyGroup(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	id, ok := proxyPathID(w, r)
	if !ok {
		return
	}
	// 先停监听器:否则组删了、监听器还在,流量会打进一个空池。
	if pc := s.m.ProxyCenter(); pc != nil {
		pc.StopPool(id)
	}
	deleted, err := s.m.PG().DeleteProxyGroup(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

// ---- 组内代理 ----

// listProxies GET /api/proxy/groups/{id}/proxies?status=&source=&protocol=&region=&q=&sort=&desc=&limit=&offset=
func (s *Server) listProxies(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	groupID, ok := proxyPathID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	items, total, counts, err := s.m.PG().ListProxies(pgdb.ProxyFilter{
		GroupID:     groupID,
		Status:      strings.TrimSpace(q.Get("status")),
		Source:      strings.TrimSpace(q.Get("source")),
		Protocol:    strings.TrimSpace(q.Get("protocol")),
		Region:      strings.TrimSpace(q.Get("region")),
		CountryCode: strings.TrimSpace(q.Get("country_code")),
		Q:           strings.TrimSpace(q.Get("q")),
		Sort:        strings.TrimSpace(q.Get("sort")),
		Desc:        q.Get("desc") != "0",
		Limit:       limit,
		Offset:      offset,
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if items == nil {
		items = []*pgdb.Proxy{}
	}
	writeJSON(w, 200, map[string]any{"proxies": items, "total": total, "counts": counts})
}

// createProxy POST /api/proxy/groups/{id}/proxies — 单条新增(批量由前端循环)。
func (s *Server) createProxy(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	groupID, ok := proxyPathID(w, r)
	if !ok {
		return
	}
	var req struct {
		URL    string `json:"url"`
		Label  string `json:"label"`
		Region string `json:"region"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	p, existing, err := s.m.PG().CreateProxy(pgdb.ProxyInput{
		GroupID: groupID, URL: req.URL, Label: req.Label, Region: req.Region,
		Source: pgdb.ProxySourceManual,
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"proxy": p, "existing": existing})
}

// updateProxy PUT /api/proxy/{id} — 部分更新:label/region/priority 缺省(nil)=保持原值,
// status 空串=保持。调度规则页的置顶/升降优先级也走这里(带 priority 字段),
// 不单开 /priority 子路由——ServeMux 下它与 PUT /api/proxy/groups/{id} 互不占优会 panic。
func (s *Server) updateProxy(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	id, ok := proxyPathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Label    *string `json:"label"`
		Region   *string `json:"region"`
		Status   string  `json:"status"`
		Priority *int    `json:"priority"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if req.Priority != nil && (*req.Priority < 0 || *req.Priority > 10000) {
		writeErr(w, 400, "priority 必须在 0-10000")
		return
	}
	if err := s.m.PG().UpdateProxy(id, req.Label, req.Region, req.Status, req.Priority); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeErr(w, 404, "代理不存在")
			return
		}
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// deleteProxy DELETE /api/proxy/{id}
func (s *Server) deleteProxy(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	id, ok := proxyPathID(w, r)
	if !ok {
		return
	}
	deleted, err := s.m.PG().DeleteProxy(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

// deleteProxies DELETE /api/proxy/groups/{id}/proxies
// body: {"ids":[...]} 或 {"status":"dead"}(按状态清理;ids 优先)。
func (s *Server) deleteProxies(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	groupID, ok := proxyPathID(w, r)
	if !ok {
		return
	}
	var req struct {
		IDs    []int64 `json:"ids"`
		Status string  `json:"status"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	var n int64
	var err error
	if len(req.IDs) > 0 {
		n, err = s.m.PG().DeleteProxies(groupID, req.IDs)
	} else {
		n, err = s.m.PG().DeleteProxiesByStatus(groupID, strings.TrimSpace(req.Status))
	}
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": n})
}

// ---- 代理池 API 接入 ----

// listProxyProviders GET /api/proxy/providers — 内置源注册表。
func (s *Server) listProxyProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"providers": proxycenter.ProviderList()})
}

// listProxySources GET /api/proxy/groups/{id}/sources
func (s *Server) listProxySources(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	groupID, ok := proxyPathID(w, r)
	if !ok {
		return
	}
	items, err := s.m.PG().ListProxySources(groupID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if items == nil {
		items = []*pgdb.ProxySource{}
	}
	writeJSON(w, 200, map[string]any{"sources": items})
}

// proxySourcePayload 是新建/更新源站的请求体。
// APIKey/Password 为空串表示「不改动已存的凭据」;要清空传 "__clear__"。
type proxySourcePayload struct {
	ProviderKey     string            `json:"provider_key"`
	Name            string            `json:"name"`
	APIURL          string            `json:"api_url"`
	APIKey          *string           `json:"api_key"`
	Username        string            `json:"username"`
	Password        *string           `json:"password"`
	Params          map[string]string `json:"params"`
	Enabled         *bool             `json:"enabled"`
	IntervalMinutes int               `json:"interval_minutes"`
}

// buildInput 把 payload 转成 db 输入,并按需加密凭据。
// 返回的 input 里凭据是密文;nil 表示不改动。
func (s *Server) buildProxySourceInput(groupID int64, p proxySourcePayload) (pgdb.ProxySourceInput, error) {
	in := pgdb.ProxySourceInput{
		GroupID: groupID, ProviderKey: strings.TrimSpace(p.ProviderKey),
		Name: strings.TrimSpace(p.Name), APIURL: strings.TrimSpace(p.APIURL),
		Username: strings.TrimSpace(p.Username), IntervalMinutes: p.IntervalMinutes,
	}
	enabled := true
	if p.Enabled != nil {
		enabled = *p.Enabled
	}
	in.Enabled = enabled
	if len(p.Params) > 0 {
		b, err := json.Marshal(p.Params)
		if err != nil {
			return in, err
		}
		in.Params = b
	}
	if s.apiSecret != nil {
		if p.APIKey != nil && *p.APIKey != "" {
			sealed, err := s.apiSecret.seal([]byte(*p.APIKey))
			if err != nil {
				return in, err
			}
			in.APIKeySealed = sealed
		}
		if p.Password != nil && *p.Password != "" {
			sealed, err := s.apiSecret.seal([]byte(*p.Password))
			if err != nil {
				return in, err
			}
			in.PasswordSealed = sealed
		}
	}
	return in, nil
}

// createProxySource POST /api/proxy/groups/{id}/sources
func (s *Server) createProxySource(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	groupID, ok := proxyPathID(w, r)
	if !ok {
		return
	}
	var p proxySourcePayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	in, err := s.buildProxySourceInput(groupID, p)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	src, err := s.m.PG().CreateProxySource(in)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"source": src})
}

// updateProxySource PUT /api/proxy/sources/{id}
func (s *Server) updateProxySource(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	id, ok := proxyPathID(w, r)
	if !ok {
		return
	}
	existing, err := s.m.PG().GetProxySource(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if existing == nil {
		writeErr(w, 404, "代理源不存在")
		return
	}
	var p proxySourcePayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	in, err := s.buildProxySourceInput(existing.GroupID, p)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := s.m.PG().UpdateProxySource(id, in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	src, err := s.m.PG().GetProxySource(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"source": src})
}

// deleteProxySource DELETE /api/proxy/sources/{id}
func (s *Server) deleteProxySource(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	id, ok := proxyPathID(w, r)
	if !ok {
		return
	}
	deleted, err := s.m.PG().DeleteProxySource(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

// autoConnectProxySources POST /api/proxy/groups/{id}/sources/auto
// 一键接入全部免凭据源(公益池 + 全网在野搜集),已接入的 provider 跳过。
func (s *Server) autoConnectProxySources(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	groupID, ok := proxyPathID(w, r)
	if !ok {
		return
	}
	if g, err := s.m.PG().GetProxyGroup(groupID); err != nil {
		writeErr(w, 500, err.Error())
		return
	} else if g == nil {
		writeErr(w, 404, "代理组不存在")
		return
	}
	existing, err := s.m.PG().CountProxySourcesByProvider(groupID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	added := 0
	for _, def := range proxycenter.ProviderList() {
		// 只自动接免凭据源:商业源没有 Key 接了也是白接,徒增失败噪声。
		if def.NeedKey || def.NeedAuth {
			continue
		}
		if existing[def.Key] > 0 {
			continue
		}
		_, err := s.m.PG().CreateProxySource(pgdb.ProxySourceInput{
			GroupID: groupID, ProviderKey: def.Key, Name: def.Title,
			APIURL: def.DefaultURL, Enabled: true, IntervalMinutes: 30,
			Params: json.RawMessage("{}"),
		})
		if err != nil {
			log.Printf("[proxy-center] 自动接入源 «%s» 失败: %v", def.Title, err)
			continue
		}
		added++
	}
	items, err := s.m.PG().ListProxySources(groupID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"added": added, "sources": items})
}

// syncProxySource POST /api/proxy/sources/{id}/sync — 立即拉取。
func (s *Server) syncProxySource(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	id, ok := proxyPathID(w, r)
	if !ok {
		return
	}
	pc := s.m.ProxyCenter()
	if pc == nil {
		writeErr(w, 503, "代理中心未初始化")
		return
	}
	store := newProxyStore(s, s.m.PG())
	all, err := store.allSources()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var target *proxycenter.SourceConfig
	for i := range all {
		if all[i].ID == id {
			target = &all[i]
			break
		}
	}
	if target == nil {
		writeErr(w, 404, "代理源不存在或未启用")
		return
	}
	imported, total, err := pc.SyncSource(*target)
	if err != nil {
		// 拉取失败是「源站的问题」而非请求本身错误,回 200 带错误详情,
		// 让前端能就地展示原因(last_error 也已入库)。
		writeJSON(w, 200, map[string]any{"imported": 0, "total": 0, "message": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"imported": imported, "total": total})
}

// ---- 测活 ----

// checkProxies POST /api/proxy/groups/{id}/check
// body: {"ids":[...]}(空=整组)。
func (s *Server) checkProxies(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	groupID, ok := proxyPathID(w, r)
	if !ok {
		return
	}
	pc := s.m.ProxyCenter()
	if pc == nil {
		writeErr(w, 503, "代理中心未初始化")
		return
	}
	var req struct {
		IDs []int64 `json:"ids"`
	}
	// body 可为空(整组测活),解析失败不当作错误。
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)
	runID, checking, err := pc.CheckGroup(groupID, req.IDs, "manual")
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"run_id": runID, "checking": checking})
}

// listProxyCheckRuns GET /api/proxy/check-runs?group_id=&limit=
func (s *Server) listProxyCheckRuns(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	q := r.URL.Query()
	groupID, _ := strconv.ParseInt(q.Get("group_id"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	runs, err := s.m.PG().ListProxyCheckRuns(groupID, limit)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if runs == nil {
		runs = []*pgdb.ProxyCheckRun{}
	}
	writeJSON(w, 200, map[string]any{"runs": runs})
}

// ---- 调度规则 ----

// schedGroupEntry 是调度总览里的一行:组配置 + 升级计数 + TOP 候选。
// 内嵌 *pgdb.ProxyGroup 使 JSON 直接平铺组字段(sched_rules 随之自动带上)。
type schedGroupEntry struct {
	*pgdb.ProxyGroup
	BoostedCount int           `json:"boosted_count"`
	Top          []*pgdb.Proxy `json:"top"`
}

// getProxySchedule GET /api/proxy/schedule — 全量组的调度总览:
// 每组带 boosted_count(已被自动升级的代理数)与 top(最值得调度的前 10 条)。
// 组数量是个位数到几十,一次查完不分页;top 为空时给 [] 不给 null,前端好渲染。
func (s *Server) getProxySchedule(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	pg := s.m.PG()
	groups, err := pg.ListAllProxyGroups()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := make([]schedGroupEntry, 0, len(groups))
	for _, g := range groups {
		boosted, err := pg.CountBoostedProxies(g.ID)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		top, err := pg.ListTopProxies(g.ID, 10)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if top == nil {
			top = []*pgdb.Proxy{}
		}
		out = append(out, schedGroupEntry{ProxyGroup: g, BoostedCount: boosted, Top: top})
	}
	writeJSON(w, 200, map[string]any{"groups": out})
}

// updateProxySchedRules PUT /api/proxy/groups/{id}/sched-rules — 更新组级调度规则。
// 越界数值在 db 层归一化为默认值(不报错,响应里的 group.sched_rules 就是归一化结果);
// 更新后重建监听器:EnsurePool 的失败回调闭包在创建时捕获了旧规则,不重建会一直用旧值。
func (s *Server) updateProxySchedRules(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	id, ok := proxyPathID(w, r)
	if !ok {
		return
	}
	pg := s.m.PG()
	g, err := pg.GetProxyGroup(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if g == nil {
		writeErr(w, 404, "代理组不存在")
		return
	}
	var rules pgdb.SchedRules
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&rules); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if err := pg.UpdateSchedRules(id, rules); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	g, err = pg.GetProxyGroup(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// 与 updateProxyGroup 同一模式:停旧监听器再按需重建,让新规则对调度失败回调生效。
	if pc := s.m.ProxyCenter(); pc != nil {
		pc.StopPool(id)
		if g.Enabled {
			if err := pc.EnsurePool(id); err != nil {
				log.Printf("[proxy-center] 重建组 #%d 监听器失败(调度规则已更新): %v", id, err)
			}
		}
	}
	writeJSON(w, 200, map[string]any{"group": g})
}

// ---- 地区识别 ----

// resolveProxyGeo POST /api/proxy/groups/{id}/geo
// body: {"limit":200}(可选,单次解析上限)。
//
// 手动触发一轮地区识别。同步执行:解析受主动限速约束(约 1.2 秒/个出口 IP),
// 因此 limit 默认只取 60,避免请求长时间挂住;要大批量请让后台定时回填跑。
func (s *Server) resolveProxyGeo(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	groupID, ok := proxyPathID(w, r)
	if !ok {
		return
	}
	pc := s.m.ProxyCenter()
	if pc == nil {
		writeErr(w, 503, "代理中心未初始化")
		return
	}
	var req struct {
		Limit int `json:"limit"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req)
	if req.Limit <= 0 || req.Limit > 300 {
		req.Limit = 60
	}
	stat, err := pc.ResolveForGroup(r.Context(), groupID, req.Limit)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	pending, _ := s.m.PG().CountGeoPending(groupID)
	writeJSON(w, 200, map[string]any{
		"resolved": stat.Resolved,
		"failed":   stat.Failed,
		"updated":  stat.Updated,
		"pending":  pending,
	})
}

// listProxyCountries GET /api/proxy/countries?group_id=
// 返回某组已识别出的国家分布(筛选器下拉用);group_id 省略/为 0 表示全平台。
func (s *Server) listProxyCountries(w http.ResponseWriter, r *http.Request) {
	if !s.proxyReady(w) {
		return
	}
	groupID, _ := strconv.ParseInt(r.URL.Query().Get("group_id"), 10, 64)
	countries, err := s.m.PG().ListProxyCountries(groupID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if countries == nil {
		countries = []*pgdb.ProxyCountryStat{}
	}
	pending, _ := s.m.PG().CountGeoPending(groupID)
	writeJSON(w, 200, map[string]any{
		"countries": countries,
		"pending":   pending,
		"enabled":   s.m.ProxyCenter() != nil && s.m.ProxyCenter().GeoEnabled(),
	})
}
