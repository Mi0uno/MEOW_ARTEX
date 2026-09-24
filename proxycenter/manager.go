package proxycenter

// Manager 是代理中心的运行时:持有各代理组的本地监听器,并驱动两个后台任务
// (源站拉取、代理测活)。
//
// 职责边界:
//   - Manager 只管「运行时状态」:监听器、调度快照、在跑的批次。
//   - 一切持久化都经 Store 接口回到 server/db 层(本包不直接依赖 db,便于单测)。
//
// 出网切换的落点:server 侧的 Manager.ProxyAddr() 读本 Manager 的 ActiveProxyAddr(),
// 于是「本地裸网 ⇄ 代理池」只需改一处,平台上二十余处 http.Client 构造点无需逐个改。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Group 是 Manager 视角下的一个代理组配置(与 db.ProxyGroup 同形,避免本包依赖 db)。
type Group struct {
	ID                   int64
	Name                 string
	Kind                 string
	Strategy             string
	ListenPort           int
	Enabled              bool
	AutoCheck            bool
	CheckIntervalMinutes int
	TestURL              string
	TimeoutMs            int
	MaxFailures          int
	Concurrency          int
}

// SourceConfig 是 Manager 视角下的一个源站配置(含明文凭据,仅在进程内传递)。
type SourceConfig struct {
	ID              int64
	GroupID         int64
	ProviderKey     string
	Name            string
	APIURL          string
	APIKey          string
	Username        string
	Password        string
	Params          map[string]string
	Enabled         bool
	IntervalMinutes int
}

// Store 是 Manager 需要的持久化能力(由 server 侧实现,桥到 db 包)。
type Store interface {
	// ListGroups 取全部启用的代理组。
	ListGroups() ([]Group, error)
	// ListCandidates 取某组可用于调度的代理(存活优先、延迟升序)。
	ListCandidates(groupID int64) ([]Candidate, error)
	// ListTargets 取某组待测活的代理(statuses 为空=全部)。
	ListTargets(groupID int64, statuses []string) ([]CheckTarget, error)
	// ApplyCheck 回写一条测活结果。
	ApplyCheck(groupID int64, out CheckOutcome, maxFailures int) error
	// RecordRequest 写一条出网流水。
	RecordRequest(rec RequestRecord) error
	// BumpUse 累加代理被调度次数。
	BumpUse(proxyID int64) error
	// DueSources 取到期的启用源站(含明文凭据)。
	DueSources() ([]SourceConfig, error)
	// MarkSourceSync 记录一次拉取结果。
	MarkSourceSync(id int64, status string, imported, total int, errMsg string)
	// ImportProxies 把拉到的 host:port 批量入库,返回新增数。
	ImportProxies(groupID int64, protocol, source, sourceRef string, hostPorts []string) (int, error)
	// StartCheckRun / FinishCheckRun 是测活批次台账。
	StartCheckRun(groupID int64, trigger string) (int64, error)
	FinishCheckRun(id int64, status string, checked, alive, dead int, errMsg string)
	// ---- 地区识别 ----
	// GeoLookup 批量查地区缓存。
	GeoLookup(ips []string) (map[string]*GeoResult, error)
	// GeoPut 写地区缓存。
	GeoPut(g GeoResult) error
	// ListGeoPending 取待解析地区的代理(有出口 IP 或 host)。
	ListGeoPending(groupID int64, limit int) ([]GeoPending, error)
	// SetProxyGeo 按出口 IP 把地区写回该组全部同出口的代理,返回影响行数。
	SetProxyGeo(groupID int64, exitIP, countryCode, country string) (int64, error)
}

// Manager 是代理中心运行时。
type Manager struct {
	store Store

	mu    sync.Mutex
	pools map[int64]*Pool // groupID -> 本地监听器

	// 出网模式:direct=裸网 / pool=代理池
	egressMu    sync.RWMutex
	egressMode  string
	egressGroup int64

	// 运行中的测活批次(groupID -> 运行中),防重复触发
	checking   map[int64]bool
	checkingMu sync.Mutex

	// 源站拉取的防重入
	syncing   map[int64]bool
	syncingMu sync.Mutex

	// 记录流水用的异步队列:代理路径绝不能因写库而阻塞。
	recCh chan RequestRecord

	// geo 是地区解析器(出口 IP → 国家/地区),带缓存与主动限速。
	geo *GeoResolver

	ctx context.Context
}

// NewManager 构造运行时。
func NewManager(ctx context.Context, store Store) *Manager {
	m := &Manager{
		store:      store,
		pools:      map[int64]*Pool{},
		egressMode: "direct",
		checking:   map[int64]bool{},
		syncing:    map[int64]bool{},
		recCh:      make(chan RequestRecord, 2048),
		ctx:        ctx,
	}
	m.geo = NewGeoResolver(GeoOptions{
		Lookup:  store.GeoLookup,
		Put:     store.GeoPut,
		Logf:    m.logf,
		Enabled: true,
	})
	go m.recordLoop()
	return m
}

// logf 是带子模块前缀的日志。
func (m *Manager) logf(format string, args ...any) {
	log.Printf("[proxy-center] "+format, args...)
}

// ---- 出网模式 ----

// EgressMode 返回当前出网模式与选中的组。
func (m *Manager) EgressMode() (string, int64) {
	m.egressMu.RLock()
	defer m.egressMu.RUnlock()
	return m.egressMode, m.egressGroup
}

// SetEgress 切换出网模式。mode=pool 时会确保该组的监听器已启动。
func (m *Manager) SetEgress(mode string, groupID int64) error {
	switch mode {
	case "direct", "pool":
	default:
		return fmt.Errorf("无效出网模式: %q", mode)
	}
	if mode == "pool" {
		if groupID <= 0 {
			return fmt.Errorf("代理池模式必须指定代理组")
		}
		if err := m.EnsurePool(groupID); err != nil {
			return err
		}
	}
	m.egressMu.Lock()
	m.egressMode = mode
	m.egressGroup = groupID
	m.egressMu.Unlock()
	return nil
}

// ActiveProxyAddr 返回 agent 出网应使用的地址:
// 代理池模式 → 本地监听器地址;裸网模式 → 空串(由调用方回落到全局出口代理)。
func (m *Manager) ActiveProxyAddr() string {
	m.egressMu.RLock()
	mode, gid := m.egressMode, m.egressGroup
	m.egressMu.RUnlock()
	if mode != "pool" || gid <= 0 {
		return ""
	}
	m.mu.Lock()
	p := m.pools[gid]
	m.mu.Unlock()
	if p == nil {
		return ""
	}
	return p.ProxyURL()
}

// ActiveProxy 返回最近一次被调度到的上游代理(仪表盘展示「当前调度」)。
func (m *Manager) ActiveProxy() string {
	m.egressMu.RLock()
	mode, gid := m.egressMode, m.egressGroup
	m.egressMu.RUnlock()
	if mode != "pool" || gid <= 0 {
		return ""
	}
	m.mu.Lock()
	p := m.pools[gid]
	m.mu.Unlock()
	if p == nil {
		return ""
	}
	return p.ActiveProxy()
}

// PoolAddr 返回某组监听器的地址(未启动则空串)。
func (m *Manager) PoolAddr(groupID int64) string {
	m.mu.Lock()
	p := m.pools[groupID]
	m.mu.Unlock()
	if p == nil {
		return ""
	}
	return p.ProxyURL()
}

// DefaultGroupID 返回「任务要求走代理池但全局没开池」时该用哪个组:
// 优先全局选中的那个,否则第一个已启动监听的组。
// 都没有时返回 0(调用方退回全局出口)。
func (m *Manager) DefaultGroupID() int64 {
	m.egressMu.RLock()
	gid := m.egressGroup
	m.egressMu.RUnlock()
	if gid > 0 {
		return gid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// 取 id 最小的已启动组:顺序稳定,便于排查。
	var best int64
	for id := range m.pools {
		if best == 0 || id < best {
			best = id
		}
	}
	return best
}

// EnsurePool 确保某组的监听器已启动(幂等)。
func (m *Manager) EnsurePool(groupID int64) error {
	m.mu.Lock()
	if _, ok := m.pools[groupID]; ok {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	groups, err := m.store.ListGroups()
	if err != nil {
		return err
	}
	var g *Group
	for i := range groups {
		if groups[i].ID == groupID {
			g = &groups[i]
			break
		}
	}
	if g == nil {
		return fmt.Errorf("代理组 #%d 不存在或未启用", groupID)
	}
	if g.ListenPort <= 0 {
		port, err := pickFreePort()
		if err != nil {
			return fmt.Errorf("分配监听端口失败: %w", err)
		}
		g.ListenPort = port
	}
	timeout := time.Duration(g.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	pool := NewPool(PoolOptions{
		GroupID:    g.ID,
		Strategy:   g.Strategy,
		ListenAddr: fmt.Sprintf("127.0.0.1:%d", g.ListenPort),
		Timeout:    timeout,
		Logf:       func(f string, a ...any) { log.Printf("[proxy-center] "+f, a...) },
		Refresh: func() ([]Candidate, error) {
			return m.store.ListCandidates(g.ID)
		},
		OnRequest: func(rec RequestRecord) {
			// 非阻塞投递:队列满时丢弃并计数,绝不让代理路径卡住。
			select {
			case m.recCh <- rec:
			default:
			}
		},
		OnProxyFailure: func(proxyID int64) {
			// 调度路径上的失败也计入失败次数,让「能连上但出不了网」的代理
			// 也会被降级(否则只靠测活发现不了这类代理)。
			_ = m.store.ApplyCheck(g.ID, CheckOutcome{ProxyID: proxyID, Err: "调度出网失败"}, g.MaxFailures)
		},
	})
	if err := pool.Start(m.ctx); err != nil {
		return err
	}
	m.mu.Lock()
	m.pools[groupID] = pool
	m.mu.Unlock()
	log.Printf("[proxy-center] 代理组 «%s» 监听器已启动: %s(策略 %s)", g.Name, pool.ProxyURL(), g.Strategy)
	return nil
}

// StopPool 关掉某组的监听器(组被删/停用时调用)。
func (m *Manager) StopPool(groupID int64) {
	m.mu.Lock()
	p := m.pools[groupID]
	delete(m.pools, groupID)
	m.mu.Unlock()
	if p != nil {
		p.Stop()
	}
	// 若当前出网正指向被关掉的组,退回裸网,避免流量黑洞。
	m.egressMu.Lock()
	if m.egressMode == "pool" && m.egressGroup == groupID {
		m.egressMode = "direct"
		m.egressGroup = 0
	}
	m.egressMu.Unlock()
}

// StopAll 关闭全部监听器(进程退出/重载配置时)。
func (m *Manager) StopAll() {
	m.mu.Lock()
	pools := m.pools
	m.pools = map[int64]*Pool{}
	m.mu.Unlock()
	for _, p := range pools {
		p.Stop()
	}
}

// StartEnabledPools 启动全部启用组的监听器(进程启动时调用)。
func (m *Manager) StartEnabledPools() {
	groups, err := m.store.ListGroups()
	if err != nil {
		log.Printf("[proxy-center] 读取代理组失败: %v", err)
		return
	}
	for _, g := range groups {
		if err := m.EnsurePool(g.ID); err != nil {
			log.Printf("[proxy-center] 启动代理组 «%s» 监听器失败: %v", g.Name, err)
		}
	}
}

// PoolStatus 返回各监听器的运行状态(接口展示用)。
type PoolStatus struct {
	GroupID     int64  `json:"group_id"`
	ListenAddr  string `json:"listen_addr"`
	Candidates  int    `json:"candidates"`
	ActiveProxy string `json:"active_proxy"`
	Running     bool   `json:"running"`
}

// PoolStatuses 返回全部已启动监听器的状态。
func (m *Manager) PoolStatuses() []PoolStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []PoolStatus{}
	for gid, p := range m.pools {
		out = append(out, PoolStatus{
			GroupID:     gid,
			ListenAddr:  p.ProxyURL(),
			Candidates:  p.CandidateCount(),
			ActiveProxy: p.ActiveProxy(),
			Running:     true,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GroupID < out[j].GroupID })
	return out
}

// ---- 异步写流水 ----

// recordLoop 消费流水队列并落库(单 goroutine 串行写,避免并发写库抖动)。
func (m *Manager) recordLoop() {
	for {
		select {
		case <-m.ctx.Done():
			return
		case rec := <-m.recCh:
			if err := m.store.RecordRequest(rec); err != nil {
				log.Printf("[proxy-center] 写请求流水失败: %v", err)
			}
			if rec.OK && rec.ProxyID > 0 {
				_ = m.store.BumpUse(rec.ProxyID)
			}
		}
	}
}

// ---- 测活 ----

// CheckGroup 对某组做一轮测活。ids 非空时只测这些。
// 返回批次 id 与本次检查条数。
func (m *Manager) CheckGroup(groupID int64, ids []int64, trigger string) (int64, int, error) {
	m.checkingMu.Lock()
	if m.checking[groupID] {
		m.checkingMu.Unlock()
		return 0, 0, fmt.Errorf("该组正在测活中")
	}
	m.checking[groupID] = true
	m.checkingMu.Unlock()
	defer func() {
		m.checkingMu.Lock()
		delete(m.checking, groupID)
		m.checkingMu.Unlock()
	}()

	groups, err := m.store.ListGroups()
	if err != nil {
		return 0, 0, err
	}
	var g *Group
	for i := range groups {
		if groups[i].ID == groupID {
			g = &groups[i]
			break
		}
	}
	if g == nil {
		return 0, 0, fmt.Errorf("代理组 #%d 不存在或未启用", groupID)
	}
	targets, err := m.store.ListTargets(groupID, nil)
	if err != nil {
		return 0, 0, err
	}
	if len(ids) > 0 {
		keep := map[int64]bool{}
		for _, id := range ids {
			keep[id] = true
		}
		filtered := targets[:0]
		for _, t := range targets {
			if keep[t.ID] {
				filtered = append(filtered, t)
			}
		}
		targets = filtered
	} else if trigger == "auto" && len(targets) > autoCheckBatch {
		// 自动测活按批滚动:在野搜集一轮能导入上万条,全量测活要跑几十分钟
		// 且持续占满并发槽。取上限后每轮推进一段,多轮自然覆盖全量。
		targets = pickAutoCheckBatch(targets, autoCheckBatch)
	}
	if len(targets) == 0 {
		return 0, 0, fmt.Errorf("该组没有可测活的代理")
	}

	runID, err := m.store.StartCheckRun(groupID, trigger)
	if err != nil {
		return 0, 0, err
	}
	go m.runCheck(runID, *g, targets)
	return runID, len(targets), nil
}

// autoCheckBatch 是单轮自动测活的条数上限。
const autoCheckBatch = 300

// autoCheckCursor 记录自动测活的滚动位置(跨轮次单调递增)。
var autoCheckCursor atomic.Uint64

// pickAutoCheckBatch 从全量候选里取一批(按 id 顺序滚动切分)。
// 每轮取连续一段,多轮下来自然覆盖全量,且不会反复轰炸同一批。
func pickAutoCheckBatch(all []CheckTarget, batch int) []CheckTarget {
	if len(all) <= batch {
		return all
	}
	start := int((autoCheckCursor.Add(uint64(batch)) - uint64(batch)) % uint64(len(all)))
	out := make([]CheckTarget, 0, batch)
	for i := 0; i < batch; i++ {
		out = append(out, all[(start+i)%len(all)])
	}
	return out
}

// runCheck 执行一轮测活并收台账。
func (m *Manager) runCheck(runID int64, g Group, targets []CheckTarget) {
	start := time.Now()
	log.Printf("[proxy-center] 开始测活 组«%s» %d 条", g.Name, len(targets))

	// 本机出口 IP:透明代理判定需要它。探测失败不致命,只放弃该判定。
	localIP := DetectLocalIP(m.ctx, 5*time.Second)
	checker := &Checker{
		TestURL:     g.TestURL,
		Timeout:     time.Duration(g.TimeoutMs) * time.Millisecond,
		Concurrency: g.Concurrency,
		LocalIP:     localIP,
		Logf:        func(f string, a ...any) { log.Printf("[proxy-center] "+f, a...) },
	}

	var mu sync.Mutex
	checked, alive, dead := 0, 0, 0
	// 测活过程中收集到的出口 IP:测活是「地区识别」最自然的触发点——
	// 这里刚拿到真实出口 IP,顺手解析地区,用户不必再手动点一次。
	seenExit := map[string]bool{}
	checker.Check(m.ctx, targets, func(out CheckOutcome) {
		if err := m.store.ApplyCheck(g.ID, out, g.MaxFailures); err != nil {
			log.Printf("[proxy-center] 回写测活结果失败(#%d): %v", out.ProxyID, err)
		}
		if out.Alive && strings.TrimSpace(out.ExitIP) != "" {
			mu.Lock()
			seenExit[strings.TrimSpace(out.ExitIP)] = true
			mu.Unlock()
		}
		mu.Lock()
		checked++
		if out.Alive {
			alive++
		} else {
			dead++
		}
		mu.Unlock()
	})

	status, errMsg := "done", ""
	if m.ctx.Err() != nil {
		status, errMsg = "failed", "进程退出"
	}
	m.store.FinishCheckRun(runID, status, checked, alive, dead, errMsg)
	log.Printf("[proxy-center] 测活完成 组«%s»:检查 %d 存活 %d 失效 %d,耗时 %s",
		g.Name, checked, alive, dead, time.Since(start).Round(time.Second))

	// 测活拿到出口 IP 后立即解析地区(顺序 + 限速,不阻塞测活本身)。
	m.resolveExitsForGroup(g.ID, seenExit)
}

// resolveExitsForGroup 解析一组出口 IP 的地区并回写。
func (m *Manager) resolveExitsForGroup(groupID int64, exits map[string]bool) {
	if len(exits) == 0 {
		return
	}
	resolved, failed := 0, 0
	var updated int64
	for ip := range exits {
		select {
		case <-m.ctx.Done():
			return
		default:
		}
		r := m.geo.Resolve(m.ctx, ip)
		if r == nil {
			continue
		}
		if !r.OK {
			failed++
			continue
		}
		resolved++
		if n, err := m.store.SetProxyGeo(groupID, ip, r.CountryCode, r.Country); err != nil {
			m.logf("回写代理地区失败(%s): %v", ip, err)
		} else {
			updated += n
		}
	}
	if resolved > 0 || failed > 0 {
		m.logf("测活后地区识别 组 #%d:解析 %d 失败 %d,更新 %d 个代理", groupID, resolved, failed, updated)
	}
}

// Checking 返回某组是否正在测活。
func (m *Manager) Checking(groupID int64) bool {
	m.checkingMu.Lock()
	defer m.checkingMu.Unlock()
	return m.checking[groupID]
}

// ---- 源站拉取 ----

// SyncSource 立即拉取一个源站。
func (m *Manager) SyncSource(src SourceConfig) (int, int, error) {
	def := ProviderByKey(src.ProviderKey)
	if def == nil && strings.TrimSpace(src.APIURL) == "" {
		return 0, 0, fmt.Errorf("未知的代理源 %q 且未提供 API 地址", src.ProviderKey)
	}
	if def == nil {
		// 自定义源:按纯文本清单解析。
		def = &ProviderDef{
			Key: src.ProviderKey, Title: src.Name, DefaultURL: src.APIURL,
			Protocol: "http", Parse: ParseTextLines, Extract: linesExtract,
		}
	}
	client := &http.Client{Timeout: 60 * time.Second}
	auth := SourceAuth{
		APIKey: src.APIKey, Username: src.Username, Password: src.Password, Params: src.Params,
	}
	list, err := def.Fetch(client, auth, src.APIURL, validateFetchURL)
	if err != nil {
		m.store.MarkSourceSync(src.ID, "failed", 0, 0, err.Error())
		return 0, 0, err
	}
	if len(list) == 0 {
		// 拉到了但一条都没解析出来,通常意味着源站改版了——如实记录,别当成功。
		msg := "未从响应中解析出任何代理(源站可能改版或需凭据)"
		m.store.MarkSourceSync(src.ID, "failed", 0, 0, msg)
		return 0, 0, fmt.Errorf("%s", msg)
	}
	sourceKind := "api"
	if def.Kind == ProviderKindPublic || def.Kind == ProviderKindWild {
		sourceKind = "public"
		if def.Kind == ProviderKindWild {
			sourceKind = "wild"
		}
	}
	imported, err := m.store.ImportProxies(src.GroupID, def.Protocol, sourceKind,
		fmt.Sprintf("%d:%s", src.ID, def.Key), list)
	if err != nil {
		m.store.MarkSourceSync(src.ID, "failed", 0, len(list), err.Error())
		return 0, len(list), err
	}
	m.store.MarkSourceSync(src.ID, "ok", imported, len(list), "")
	return imported, len(list), nil
}

// SyncSourceByID 按 id 拉取(供接口调用;凭据由 store 解密)。
func (m *Manager) SyncSourceByID(id int64) (int, int, error) {
	src, err := m.sourceByID(id)
	if err != nil {
		return 0, 0, err
	}
	return m.SyncSource(src)
}

// sourceByID 从到期源列表里找指定 id(Store 只暴露 DueSources;单条查询由
// server 侧在调用前注入到列表里,这里用同一接口取)。
func (m *Manager) sourceByID(id int64) (SourceConfig, error) {
	list, err := m.store.DueSources()
	if err != nil {
		return SourceConfig{}, err
	}
	for _, s := range list {
		if s.ID == id {
			return s, nil
		}
	}
	return SourceConfig{}, fmt.Errorf("代理源 #%d 不存在或未启用", id)
}

// RunSourceSyncLoop 定时拉取到期源站。
func (m *Manager) RunSourceSyncLoop() {
	// 启动后等 20 秒,避开启动期初始化。
	select {
	case <-m.ctx.Done():
		return
	case <-time.After(20 * time.Second):
	}
	for {
		m.syncDueSources()
		select {
		case <-m.ctx.Done():
			return
		case <-time.After(5 * time.Minute):
		}
	}
}

// syncDueSources 拉取全部到期源站(同一源的并发拉取由 syncing 挡掉)。
func (m *Manager) syncDueSources() {
	srcs, err := m.store.DueSources()
	if err != nil {
		log.Printf("[proxy-center] 读取到期代理源失败: %v", err)
		return
	}
	for _, s := range srcs {
		m.syncingMu.Lock()
		if m.syncing[s.ID] {
			m.syncingMu.Unlock()
			continue
		}
		m.syncing[s.ID] = true
		m.syncingMu.Unlock()

		imported, total, err := m.SyncSource(s)
		if err != nil {
			log.Printf("[proxy-center] 拉取源 «%s» 失败: %v", s.Name, err)
		} else {
			log.Printf("[proxy-center] 拉取源 «%s»:导入 %d / 共 %d", s.Name, imported, total)
		}

		m.syncingMu.Lock()
		delete(m.syncing, s.ID)
		m.syncingMu.Unlock()
	}
}

// RunCheckLoop 定时测活(按各组的 check_interval_minutes)。
//
// 用「上次测活时间」而非固定 ticker:每组间隔不同,统一 1 分钟轮询判断谁到期。
func (m *Manager) RunCheckLoop() {
	lastRun := map[int64]time.Time{}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	// 启动后等 30 秒再开始,让源站先拉一轮(有代理可测)。
	select {
	case <-m.ctx.Done():
		return
	case <-time.After(30 * time.Second):
	}
	for {
		groups, err := m.store.ListGroups()
		if err != nil {
			log.Printf("[proxy-center] 读取代理组失败: %v", err)
		} else {
			for _, g := range groups {
				if !g.AutoCheck || g.CheckIntervalMinutes <= 0 {
					continue
				}
				interval := time.Duration(g.CheckIntervalMinutes) * time.Minute
				if since := time.Since(lastRun[g.ID]); !lastRun[g.ID].IsZero() && since < interval {
					continue
				}
				if m.Checking(g.ID) {
					continue
				}
				if _, n, err := m.CheckGroup(g.ID, nil, "auto"); err != nil {
					log.Printf("[proxy-center] 自动测活 组«%s» 跳过: %v", g.Name, err)
				} else {
					lastRun[g.ID] = time.Now()
					log.Printf("[proxy-center] 自动测活 组«%s» 已启动:%d 条", g.Name, n)
				}
			}
		}
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ---- 辅助 ----

// pickFreePort 从 18080 起找一个空闲端口。
func pickFreePort() (int, error) {
	for port := 18080; port < 18180; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		_ = ln.Close()
		return port, nil
	}
	return 0, fmt.Errorf("18080-18179 无空闲端口")
}

// validateFetchURL 是拉取源站前的 SSRF 校验:仅 http/https,拒绝内网/环回/保留地址。
//
// 与 recon.ValidatePublicURL 同一判据。代理源清单站全是公网地址,不该出现内网目标;
// 一旦出现,要么是配置错误,要么是有人试图把本机当跳板——两种都该拒。
func validateFetchURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("代理源地址无效: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("代理源仅允许 http/https 地址")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("代理源地址缺少主机")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return fmt.Errorf("拒绝访问本机地址")
	}
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicIPAddr(ip) {
			return fmt.Errorf("拒绝访问私有/保留地址: %s", ip)
		}
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("主机解析失败: %w", err)
	}
	for _, ip := range ips {
		if !isPublicIPAddr(ip) {
			return fmt.Errorf("拒绝访问私有/保留地址: %s", ip)
		}
	}
	return nil
}

// isPublicIPAddr 判断是否公网可路由地址。
func isPublicIPAddr(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return false
	}
	return ip.IsGlobalUnicast()
}

// MarshalParams 把任意 map 序列化成 SourceConfig.Params 需要的 string map。
// JSON 数字/布尔统一转成字符串,便于源站拼 query。
func MarshalParams(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 {
		return out
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return out
	}
	for k, v := range m {
		switch t := v.(type) {
		case string:
			out[k] = t
		case float64:
			// JSON 数字都是 float64;整数按整数渲染,避免 "100.000000"。
			if t == float64(int64(t)) {
				out[k] = fmt.Sprintf("%d", int64(t))
			} else {
				out[k] = fmt.Sprintf("%g", t)
			}
		case bool:
			out[k] = fmt.Sprintf("%t", t)
		default:
			if b, err := json.Marshal(t); err == nil {
				out[k] = string(b)
			}
		}
	}
	return out
}
