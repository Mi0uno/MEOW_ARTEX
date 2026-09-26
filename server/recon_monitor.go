package server

// 资产监控调度器:全局开关打开后,对「company_scope 里有 kind=domain 规则」的
// 企业按固定间隔(缺省 24h)持续做被动子域测绘——crt.sh 恒定参与 + 已启用的外部
// 接口 provider;新发现的子域再做 DNS 解析 + HTTP(S) 存活探活;产出全量直写正式
// 资产图(不走暂存区——监控轮没有任务行,staged_assets.task_id FK 到 tasks),
// 审计轨迹由 recon_monitor_runs 承载(collectors 明细 + new_domains 清单)。
//
// 与 recon 流水线任务(stage=recon)的关系:复用同一批收集器与 buildReconPlan 的
// 数据源自动检测,但不建任务行、不写探索图、每轮零 LLM(useAI=false,确定性默认
// query)。定时器范式与 vulndb_sync 一致(30s 启动延迟 / wake chan cap1 / running
// 互斥);两点不同:开关缺省视为关(主动触探客户目标,须显式开启),且「due」按
// 企业水位(targets.last_run_at)逐家判定——全局单例,一轮只跑一家,两家之间
// 隔 ≥20s 错峰。重启续跑:水位落库,开机后 overdue 自然补跑。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/recon"
)

const (
	settingReconMonitorEnabled  = "recon_monitor_enabled"
	settingReconMonitorInterval = "recon_monitor_interval_hours"
	// reconMonitorDefaultInterval 全局缺省间隔(小时);合法域 [1,720],越界回退缺省。
	reconMonitorDefaultInterval = 24
	reconMonitorMinInterval     = 1
	reconMonitorMaxInterval     = 720
	// 单企业一轮的硬预算、两家之间的错峰间隔、主循环醒来查 due 的节拍。
	reconMonitorCompanyTimeout = 30 * time.Minute
	reconMonitorCompanyGap     = 20 * time.Second
	reconMonitorTick           = 60 * time.Second
	// 单轮汇总去重后的资产条数上限;new_domains 清单再按 300 条截断置 new_truncated。
	reconMonitorMaxAssets     = 20000
	reconMonitorMaxNewDomains = 300
	// 新子域探活参数(契约:并发 ≤8,单次尝试 6s;授权目标指纹采集,自签证书常见,
	// 允许 x509 错误降级重试)。
	reconMonitorProbeConcurrency = 8
	reconMonitorProbeTimeout     = 6 * time.Second
)

// monitorCollectorStat 是单收集器在本轮的执行轨迹(recon_monitor_runs.collectors)。
type monitorCollectorStat struct {
	Key    string `json:"key"`
	Status string `json:"status"`
	Found  int    `json:"found"`
	Error  string `json:"error"`
}

// reconMonitor 是资产监控调度器(单例,挂在 Server 上,与 vulnSync 同范式)。
type reconMonitor struct {
	mu      sync.Mutex
	running bool
	wake    chan struct{}
	s       *Server
}

func newReconMonitor(s *Server) *reconMonitor {
	return &reconMonitor{wake: make(chan struct{}, 1), s: s}
}

// isRunning reports whether a monitor round is live.
func (v *reconMonitor) isRunning() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.running
}

// poke 非阻塞唤醒主循环(配置开启时顺带叫醒,不必等下个 60s tick)。
func (v *reconMonitor) poke() {
	select {
	case v.wake <- struct{}{}:
	default:
	}
}

// trigger 请求立即跑一轮所有 due 的企业(受全局开关约束);已有轮次在跑时返回 false。
func (v *reconMonitor) trigger() bool {
	v.mu.Lock()
	if v.running {
		v.mu.Unlock()
		return false
	}
	v.mu.Unlock()
	v.poke()
	return true
}

// triggerCompany 绕过全局开关立即试跑一家(手动试跑,便于配置后先验证一家)。
// 已有轮次在跑时返回 false。水位清零由调用方(ResetMonitorTargetRun)先行完成。
func (v *reconMonitor) triggerCompany(companyID int64) bool {
	v.mu.Lock()
	if v.running {
		v.mu.Unlock()
		return false
	}
	v.running = true
	v.mu.Unlock()
	go func() {
		defer func() {
			v.mu.Lock()
			v.running = false
			v.mu.Unlock()
		}()
		pg := v.s.m.PG()
		if pg == nil {
			return
		}
		mc, ok := v.monitorCompanyFor(companyID)
		if !ok {
			log.Printf("[recon-monitor] 企业 #%d 不存在,试跑取消", companyID)
			return
		}
		log.Printf("[recon-monitor] %s: 手动试跑开始", mc.Name)
		v.runCompanyWithBudget(mc, "manual")
	}()
	return true
}

// monitorCompanyFor 解析试跑企业的监控目标:候选名单(有域名范围的企业)优先;
// 不在名单里的企业也给名字和空域名,保证「试跑一家」总能落一条轮次记录。
func (v *reconMonitor) monitorCompanyFor(companyID int64) (db.MonitorCompany, bool) {
	pg := v.s.m.PG()
	if candidates, err := pg.ListMonitorCandidateCompanies(); err == nil {
		for _, mc := range candidates {
			if mc.ID == companyID {
				return mc, true
			}
		}
	}
	if c, err := pg.Companies().GetCompany(companyID); err == nil && c != nil {
		return db.MonitorCompany{ID: companyID, Name: c.Name, Domains: []string{}}, true
	}
	return db.MonitorCompany{}, false
}

// run 是调度器主循环:每 60s 醒来一次,开关开着就把 due 的企业逐家跑掉;
// 收到 wake(手动触发/开启开关)立即进入下一轮。
func (v *reconMonitor) run(ctx context.Context) {
	// 启动后先等 30 秒再跑首轮,避免与启动期的其他初始化抢网络与 DB(与 vulndb_sync 一致)。
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
	}
	for {
		if v.enabled() {
			v.runDue(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-v.wake:
			// 手动触发/开启开关:立即进入下一轮。
		case <-time.After(reconMonitorTick):
		}
	}
}

// enabled 读全局开关。与 vulndb 相反:缺省视为关——监控会主动触探客户目标,
// 必须显式开启。
func (v *reconMonitor) enabled() bool {
	val, _, _ := v.s.m.PG().GetSetting(settingReconMonitorEnabled)
	return val == "true"
}

// intervalHours 读全局间隔设置:缺省 24,越界(不在 [1,720])回退缺省。
func (v *reconMonitor) intervalHours() int {
	h := reconMonitorDefaultInterval
	if val, _, _ := v.s.m.PG().GetSetting(settingReconMonitorInterval); val != "" {
		if n, err := strconv.Atoi(val); err == nil && monitorIntervalInRange(n) {
			h = n
		}
	}
	return h
}

// monitorIntervalInRange 全局间隔的合法域 [1,720]。
func monitorIntervalInRange(n int) bool {
	return n >= reconMonitorMinInterval && n <= reconMonitorMaxInterval
}

// monitorDue 判定一家企业本轮是否到期:lastRunAt 为 nil(从未跑过/被手动重置)→
// 立即 due;有效间隔 = 企业覆盖(>0)优先,否则全局间隔。
func monitorDue(lastRunAt *time.Time, intervalHours, globalIntervalHours int, now time.Time) bool {
	if lastRunAt == nil {
		return true
	}
	eff := globalIntervalHours
	if intervalHours > 0 {
		eff = intervalHours
	}
	if eff <= 0 {
		return true
	}
	return !now.Before(lastRunAt.Add(time.Duration(eff) * time.Hour))
}

// runDue 找出全部 watched(有域名范围且未被显式退出)且 due 的企业,按
// (从未跑过优先,再按水位最旧优先)逐家串行执行;两家之间隔 ≥20s(可被 ctx 打断)。
// running 互斥保证全局单例:同一时刻只跑一家。
func (v *reconMonitor) runDue(ctx context.Context) {
	v.mu.Lock()
	if v.running {
		v.mu.Unlock()
		return
	}
	v.running = true
	v.mu.Unlock()
	defer func() {
		v.mu.Lock()
		v.running = false
		v.mu.Unlock()
	}()

	pg := v.s.m.PG()
	if pg == nil {
		return
	}
	candidates, err := pg.ListMonitorCandidateCompanies()
	if err != nil {
		log.Printf("[recon-monitor] 读取候选企业失败: %v", err)
		return
	}
	rows, err := pg.ListMonitorTargets()
	if err != nil {
		log.Printf("[recon-monitor] 读取监控目标失败: %v", err)
		return
	}
	rowBy := make(map[int64]*db.MonitorTarget, len(rows))
	for i := range rows {
		rowBy[rows[i].CompanyID] = &rows[i]
	}
	globalH := v.intervalHours()
	now := time.Now()
	type dueCompany struct {
		mc        db.MonitorCompany
		lastRunAt *time.Time
	}
	var due []dueCompany
	for _, mc := range candidates {
		if mc.DomainCount <= 0 || len(mc.Domains) == 0 {
			continue // watched 的前提:company_scope 里有 kind=domain 规则
		}
		row := rowBy[mc.ID]
		if row != nil && !row.Enabled {
			continue // targets 行 enabled=false = 显式退出
		}
		override, last := 0, (*time.Time)(nil)
		if row != nil {
			last = row.LastRunAt
			if row.IntervalHours != nil {
				override = *row.IntervalHours
			}
		}
		if monitorDue(last, override, globalH, now) {
			due = append(due, dueCompany{mc: mc, lastRunAt: last})
		}
	}
	if len(due) == 0 {
		return
	}
	// 从未跑过的排最前,其余按水位最旧优先。
	sort.Slice(due, func(i, j int) bool {
		a, b := due[i].lastRunAt, due[j].lastRunAt
		if a == nil {
			return b != nil
		}
		if b == nil {
			return false
		}
		return a.Before(*b)
	})
	log.Printf("[recon-monitor] %d 家企业到期,开始串行执行", len(due))
	for i, dc := range due {
		if ctx.Err() != nil {
			return
		}
		v.runCompanyWithBudget(dc.mc, "schedule")
		if i < len(due)-1 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(reconMonitorCompanyGap):
			}
		}
	}
}

// runCompanyWithBudget 给单企业一轮包上 30 分钟预算与全局出网代理后执行。
func (v *reconMonitor) runCompanyWithBudget(mc db.MonitorCompany, trigger string) {
	ctx, cancel := context.WithTimeout(v.s.ctx, reconMonitorCompanyTimeout)
	defer cancel()
	// 出网走全局出口(代理池/MITM/全局代理的当前落点)。与任务侧按任务归因不同:
	// 监控轮没有任务身份,统一走全局出口即可。
	ctx = recon.WithProxy(ctx, v.s.m.ProxyAddr(), v.s.m.ProxyCACert())
	v.runOneCompany(ctx, mc, trigger)
}

// runOneCompany 执行单企业一轮监控(契约「单轮执行语义」1-8):
// 建轮次行 → 组步骤(crt.sh 恒在最前 + buildReconPlan 可用 Sources,零 LLM) →
// 逐步骤收集(resolveExternalSecrets → Collect,单步失败记轨迹继续) → 汇总去重
// (封顶 20000) → ExistingValues 差集出 new 集合 → 全量直写资产图(强制归属) →
// new 子域探活(Alive 落 HTTP 服务) → FinishMonitorRun 收尾。
func (v *reconMonitor) runOneCompany(ctx context.Context, mc db.MonitorCompany, trigger string) {
	pg := v.s.m.PG()
	as := pg.Assets()
	if mc.Domains == nil {
		mc.Domains = []string{}
	}
	start := time.Now()
	runID, err := pg.StartMonitorRun(mc.ID, mc.Name, trigger)
	if err != nil {
		log.Printf("[recon-monitor] %s: 建轮次失败: %v", mc.Name, err)
		return
	}

	stats := make([]monitorCollectorStat, 0, 8)
	doneCollectors, failedCollectors := 0, 0

	// 步骤组装:crt.sh 是收集器不是 provider,不在自动计划里,恒定追加在最前
	// (Config 留空,域名来自 Companies[i].Domains);其余取 buildReconPlan(零 LLM,
	// 确定性默认 query)里 SkipReason 为空(已配置且已启用)的 Sources。
	steps := []recon.PipelineStep{{Key: "crtsh", Enabled: boolPtr(true), Config: map[string]any{}}}
	plan, planErr := v.s.buildReconPlan(ctx, []int64{mc.ID}, 0, false)
	if planErr != nil {
		log.Printf("[recon-monitor] %s: 数据源自动检测失败(本轮仅 crt.sh 参与): %v", mc.Name, planErr)
	} else {
		for _, sp := range plan.Sources {
			if sp.SkipReason != "" {
				continue
			}
			// external_api_id 用 float64 装配:resolveExternalSecrets 按该类型断言读取
			// (与任务侧从 JSON 解出的数值类型一致)。
			cfg := map[string]any{"external_api_id": float64(sp.ExternalAPI)}
			if sp.Domain != "" {
				cfg["domain"] = sp.Domain
			}
			if sp.Query != "" {
				cfg["query"] = sp.Query
			}
			steps = append(steps, recon.PipelineStep{Key: sp.Provider, Enabled: boolPtr(true), Config: cfg})
		}
	}

	target := recon.CompanyTarget{ID: mc.ID, Name: mc.Name, Domains: mc.Domains}
	var collected []recon.RawAsset
	for _, step := range steps {
		if ctx.Err() != nil {
			break
		}
		stat := monitorCollectorStat{Key: step.Key, Status: "done"}
		collector := recon.Get(step.Key)
		if collector == nil {
			stat.Status, stat.Error = "failed", "收集器未注册"
			stats = append(stats, stat)
			failedCollectors++
			continue
		}
		secrets, err := v.s.resolveExternalSecrets(step.Key, step.Config)
		if err != nil {
			stat.Status, stat.Error = "failed", err.Error()
			stats = append(stats, stat)
			failedCollectors++
			log.Printf("[recon-monitor] %s: 收集器 %s 凭据解析失败: %v", mc.Name, step.Key, err)
			continue
		}
		result, err := collector.Collect(ctx, recon.CollectorInput{
			TaskID:    0,
			Companies: []recon.CompanyTarget{target},
			Config:    step.Config,
			Secrets:   secrets,
			RawSink:   v.s.reconSink,
		})
		if err != nil {
			stat.Status, stat.Error = "failed", err.Error()
			stats = append(stats, stat)
			failedCollectors++
			log.Printf("[recon-monitor] %s: 收集器 %s 失败: %v", mc.Name, step.Key, err)
			continue
		}
		stat.Found = len(result.Assets)
		stats = append(stats, stat)
		doneCollectors++
		collected = append(collected, result.Assets...)
	}

	// 汇总去重(type+value),封顶 20000 条/轮。
	type assetKey struct {
		typ, value string
	}
	seen := make(map[assetKey]bool, len(collected))
	assets := make([]recon.RawAsset, 0, len(collected))
	for _, a := range collected {
		if a.Value == "" {
			continue
		}
		k := assetKey{string(a.Type), a.Value}
		if seen[k] {
			continue
		}
		if len(assets) >= reconMonitorMaxAssets {
			break
		}
		seen[k] = true
		assets = append(assets, a)
	}

	// 按 type 批量查存量 → 差集 = new 集合。存量快照在锁外读,极小概率与手动任务
	// 竞态误标 new 并多探一次——无害(契约口径)。查询失败时该 type 全部按新发现处理。
	byType := map[string][]string{}
	for _, a := range assets {
		byType[string(a.Type)] = append(byType[string(a.Type)], a.Value)
	}
	existing := map[string]map[string]bool{}
	for typ, vals := range byType {
		got, err := as.ExistingValues(typ, vals)
		if err != nil {
			log.Printf("[recon-monitor] %s: 查询 %s 存量失败(该类型全部按新发现处理): %v", mc.Name, typ, err)
			continue
		}
		existing[typ] = got
	}
	isNew := func(typ, value string) bool {
		if got, ok := existing[typ]; ok && got[value] {
			return false
		}
		return true
	}

	// 全量直写(不只 new——老资产也要刷新 last_seen/归属):与 adoptStagedAsset
	// 同构映射,但 CompanyID 强制归属本企业、TaskID=0。单条失败仅计数、不断流。
	writeOK, writeFailed := 0, 0
	for _, a := range assets {
		if _, err := v.writeAsset(as, mc.ID, a); err != nil {
			writeFailed++
			log.Printf("[recon-monitor] %s: 直写 %s %s 失败: %v", mc.Name, a.Type, a.Value, err)
			continue
		}
		writeOK++
	}

	// new 集合里的子域做 DNS/HTTP 存活探活(纯 Go,经 ctx 里的全局出口);
	// Alive 的结果落 HTTP 服务资产( side effect 自动补子域/IP/根域归属)。
	probeOK, probeFailed := 0, 0
	var probeTargets []string
	for _, a := range assets {
		if a.Type == recon.TypeDomain && isNew(string(a.Type), a.Value) {
			probeTargets = append(probeTargets, a.Value)
		}
	}
	if len(probeTargets) > 0 && ctx.Err() == nil {
		results := recon.ProbeDomains(ctx, probeTargets, recon.ProbeOptions{
			Concurrency: reconMonitorProbeConcurrency,
			Timeout:     reconMonitorProbeTimeout,
			InsecureTLS: true,
		})
		for _, r := range results {
			if !r.Alive {
				probeFailed++
				continue
			}
			probeOK++
			if r.URL == "" {
				continue
			}
			sc, cl := r.StatusCode, r.ContentLength
			req := db.UpsertHTTPServiceReq{
				URL: r.URL, StatusCode: &sc, PageTitle: r.Title, ContentLength: &cl,
				Technologies: r.Technologies, CompanyID: mc.ID,
			}
			if ip := firstPublicIP(r.IPs); ip != "" {
				req.IP = ip
			}
			if _, err := as.UpsertHTTPService(req); err != nil {
				log.Printf("[recon-monitor] %s: 探活结果落库 %s 失败: %v", mc.Name, r.URL, err)
			}
		}
	}

	// new_domains 清单:domain/root_domain/ip/service 四类的 new 值,cap 300 截断
	// 置 new_truncated(防大 JSON)。
	type monitorNewAsset struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	}
	newCount := 0
	newItems := make([]monitorNewAsset, 0, 16)
	truncated := false
	for _, a := range assets {
		switch a.Type {
		case recon.TypeDomain, recon.TypeRootDomain, recon.TypeIP, recon.TypeService:
		default:
			continue
		}
		if !isNew(string(a.Type), a.Value) {
			continue
		}
		newCount++
		if len(newItems) < reconMonitorMaxNewDomains {
			newItems = append(newItems, monitorNewAsset{Type: string(a.Type), Value: a.Value})
		} else {
			truncated = true
		}
	}

	// 收尾:全部收集器 failed → failed;ctx 取消 → failed + 「已停止」;否则 done。
	status, errMsg := "done", ""
	if doneCollectors == 0 && failedCollectors > 0 {
		status, errMsg = "failed", "全部收集器失败"
	}
	if ctx.Err() != nil {
		status, errMsg = "failed", "已停止"
	}
	statsJSON, _ := json.Marshal(stats)
	newJSON, _ := json.Marshal(newItems)
	if err := pg.FinishMonitorRun(runID, mc.ID, status, statsJSON, len(assets), newCount,
		newJSON, truncated, probeOK, probeFailed, errMsg); err != nil {
		log.Printf("[recon-monitor] %s: 收尾轮次 #%d 失败: %v", mc.Name, runID, err)
	}
	if newCount > 0 {
		// 推送/通知是二期;先落日志。
		log.Printf("[recon-monitor] %s: 新增 %d 条(轮次 #%d %s,共 %d 条资产,探活 ok/failed %d/%d,写入 ok/failed %d/%d,耗时 %s)",
			mc.Name, newCount, runID, status, len(assets), probeOK, probeFailed, writeOK, writeFailed,
			time.Since(start).Round(time.Second))
	} else {
		log.Printf("[recon-monitor] %s: 轮次 #%d %s(共 %d 条资产,新增 0,探活 ok/failed %d/%d,写入 ok/failed %d/%d,耗时 %s)",
			mc.Name, runID, status, len(assets), probeOK, probeFailed, writeOK, writeFailed,
			time.Since(start).Round(time.Second))
	}
}

// writeAsset 按 adoptStagedAsset 的同构映射把一条收集产出直写正式资产图;
// 两点不同:CompanyID 强制归属(监控轮按企业走,不依赖 scope 自动认领)、
// TaskID=0(监控轮没有任务行)。
func (v *reconMonitor) writeAsset(as *db.AssetStore, companyID int64, a recon.RawAsset) (int64, error) {
	extra := map[string]any{}
	_ = json.Unmarshal(a.Extra, &extra)
	extraStr := func(keys ...string) string {
		for _, k := range keys {
			if s, ok := extra[k].(string); ok && s != "" {
				return s
			}
		}
		return ""
	}
	switch a.Type {
	case recon.TypeRootDomain:
		return as.UpsertRootDomain(db.UpsertRootDomainReq{Domain: a.Value, TaskID: 0, CompanyID: companyID})
	case recon.TypeDomain:
		return as.UpsertSubdomain(db.UpsertSubdomainReq{Domain: a.Value, TaskID: 0, CompanyID: companyID})
	case recon.TypeIP:
		var ports []db.PortService
		if a.Port > 0 {
			ports = append(ports, db.PortService{Port: a.Port, Service: extraStr("service", "service_name")})
		}
		return as.UpsertIP(db.UpsertIPReq{IP: a.Value, OpenPorts: ports, TaskID: 0, CompanyID: companyID})
	case recon.TypeService:
		if u := extraStr("url"); u != "" {
			ip := ""
			if net.ParseIP(a.Value) != nil {
				ip = a.Value
			}
			return as.UpsertHTTPService(db.UpsertHTTPServiceReq{URL: u, IP: ip, TaskID: 0, CompanyID: companyID})
		}
		req := db.UpsertOtherServiceReq{Port: a.Port, ServiceName: extraStr("service", "service_name"), TaskID: 0, CompanyID: companyID}
		if net.ParseIP(a.Value) != nil {
			req.IP = a.Value
		} else {
			req.Domain = a.Value
		}
		return as.UpsertOtherService(req)
	case "app":
		name := extraStr("name", "app_name")
		if name == "" {
			name = a.Value
		}
		return as.UpsertApp(db.UpsertAppReq{
			Name: name, BundleID: extraStr("bundle_id"), Category: extraStr("category"),
			Description: extraStr("description"), ICP: extraStr("icp", "app_icp"),
			CompanyID: &companyID, TaskID: 0,
		})
	}
	return 0, fmt.Errorf("未知资产类型 %q", a.Type)
}

// firstPublicIP 取解析结果里第一个公网地址(私网/环回/链路本地/组播/保留段一律
// 不算,分类复用 recon.ClassifyIP);没有公网地址返回空串(落库时就不设 IP)。
func firstPublicIP(ips []string) string {
	for _, s := range ips {
		ip := net.ParseIP(strings.TrimSpace(s))
		if ip == nil {
			continue
		}
		if recon.ClassifyIP(ip) == "public" {
			return ip.String()
		}
	}
	return ""
}
