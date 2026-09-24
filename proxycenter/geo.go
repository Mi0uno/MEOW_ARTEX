package proxycenter

// 代理地区识别:由出口 IP 解析出国家/地区。
//
// 为什么要有缓存:地区查询走第三方免费接口(ip-api 免费档约 45 次/分钟),
// 而在野代理清单里大量条目共用同一出口 IP(实测 3 万条里出口 IP 高度重复),
// 不缓存会立刻被限流。缓存落在 proxy_geo_cache 表,进程重启后依然有效。
//
// 为什么先解析出口 IP 而不是代理 host:出口 IP 才是「流量实际从哪出去」的答案。
// 代理 host 可能只是中转入口,与出口国家不一致;只有在出口 IP 未知时才退回 host。
//
// 限速:接口是免费的共享资源,这里主动做令牌间隔(默认 1.2 秒一次请求),
// 而不是打满对方配额——被限流后整批解析全失败,反而更慢。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// geoEndpoint 是地区解析接口。ip-api 免费档无需 key,返回国家码/国家/省市。
const geoEndpoint = "http://ip-api.com/json/%s?fields=status,message,country,countryCode,regionName,city,query"

// GeoResolver 解析 IP 地区,带内存 + DB 两级缓存与主动限速。
type GeoResolver struct {
	// Lookup 批量查 DB 缓存。
	Lookup func(ips []string) (map[string]*GeoResult, error)
	// Put 写 DB 缓存。
	Put func(g GeoResult) error
	// MinInterval 是两次外部请求之间的最小间隔(主动限速)。
	MinInterval time.Duration
	// Timeout 是单次解析请求超时。
	Timeout time.Duration
	// Enabled 关闭时不发外部请求(仅用缓存)。
	Enabled bool
	// Logf 日志。
	Logf func(format string, args ...any)

	// mem 是进程内二级缓存:同一轮解析里重复 IP 不再查 DB。
	memMu sync.Mutex
	mem   map[string]*GeoResult

	// 限速闸门
	gateMu   sync.Mutex
	lastCall time.Time

	client *http.Client
}

// GeoResult 与 db.GeoResult 同形(本包不依赖 db,由 Store 适配)。
type GeoResult struct {
	IP          string
	OK          bool
	CountryCode string
	Country     string
	RegionName  string
	City        string
	Err         string
}

// GeoOptions 是 NewGeoResolver 的入参。
//
// 单独一个不含锁的选项结构:直接传 GeoResolver 会按值复制它内部的 sync.Mutex
// (go vet 会报 "passes lock by value"),那种锁是失效的副本。
type GeoOptions struct {
	Lookup      func(ips []string) (map[string]*GeoResult, error)
	Put         func(g GeoResult) error
	MinInterval time.Duration
	Timeout     time.Duration
	Enabled     bool
	Logf        func(format string, args ...any)
}

// NewGeoResolver 构造解析器。
func NewGeoResolver(opts GeoOptions) *GeoResolver {
	if opts.MinInterval <= 0 {
		opts.MinInterval = 1200 * time.Millisecond
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	return &GeoResolver{
		Lookup:      opts.Lookup,
		Put:         opts.Put,
		MinInterval: opts.MinInterval,
		Timeout:     opts.Timeout,
		Enabled:     opts.Enabled,
		Logf:        opts.Logf,
		mem:         map[string]*GeoResult{},
		client:      &http.Client{Timeout: opts.Timeout},
	}
}

// Resolve 解析一个 IP 的地区。命中缓存直接返回;否则(在启用时)请求外部接口。
// 失败也返回结果(OK=false + Err),由调用方决定是否写回。
func (g *GeoResolver) Resolve(ctx context.Context, ip string) *GeoResult {
	ip = strings.TrimSpace(ip)
	if net.ParseIP(ip) == nil {
		return &GeoResult{IP: ip, Err: "不是合法 IP"}
	}
	// 内网/保留地址没有地区可言,直接判失败并缓存(避免反复请求)。
	if !isPublicIPAddr(net.ParseIP(ip)) {
		return &GeoResult{IP: ip, Err: "非公网地址"}
	}
	if r := g.fromCache(ip); r != nil {
		return r
	}
	if !g.Enabled {
		return &GeoResult{IP: ip, Err: "地区识别已关闭"}
	}
	r := g.fetch(ctx, ip)
	g.store(r)
	return r
}

// ResolveBatch 批量解析(顺序执行 + 限速;并发对免费接口不礼貌且更易被限流)。
// 每个成功/失败结果都回调 emit,便于调用方边解析边落库。
func (g *GeoResolver) ResolveBatch(ctx context.Context, ips []string, emit func(*GeoResult)) {
	seen := map[string]bool{}
	for _, ip := range ips {
		ip = strings.TrimSpace(ip)
		if ip == "" || seen[ip] {
			continue
		}
		seen[ip] = true
		select {
		case <-ctx.Done():
			return
		default:
		}
		r := g.Resolve(ctx, ip)
		if emit != nil {
			emit(r)
		}
	}
}

// fromCache 查两级缓存(内存 → DB)。命中即回填内存。
func (g *GeoResolver) fromCache(ip string) *GeoResult {
	g.memMu.Lock()
	if r, ok := g.mem[ip]; ok {
		g.memMu.Unlock()
		return r
	}
	g.memMu.Unlock()

	if g.Lookup == nil {
		return nil
	}
	m, err := g.Lookup([]string{ip})
	if err != nil {
		g.Logf("查地区缓存失败: %v", err)
		return nil
	}
	r, ok := m[ip]
	if !ok {
		return nil
	}
	g.memMu.Lock()
	g.mem[ip] = r
	g.memMu.Unlock()
	return r
}

// store 写两级缓存。
func (g *GeoResolver) store(r *GeoResult) {
	if r == nil || r.IP == "" {
		return
	}
	g.memMu.Lock()
	g.mem[r.IP] = r
	g.memMu.Unlock()
	if g.Put != nil {
		if err := g.Put(*r); err != nil {
			g.Logf("写地区缓存失败(%s): %v", r.IP, err)
		}
	}
}

// fetch 请求外部接口解析一个 IP。
func (g *GeoResolver) fetch(ctx context.Context, ip string) *GeoResult {
	g.waitTurn()

	url := fmt.Sprintf(geoEndpoint, ip)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return &GeoResult{IP: ip, Err: err.Error()}
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; MEOWFUCK-ProxyCenter/1.0)")
	resp, err := g.client.Do(req)
	if err != nil {
		return &GeoResult{IP: ip, Err: shortErr(err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		// 被限流:返回可重试的错误,调用方下次再试(不写缓存为「成功」)。
		return &GeoResult{IP: ip, Err: "地区接口限流(429)"}
	}
	if resp.StatusCode != http.StatusOK {
		return &GeoResult{IP: ip, Err: fmt.Sprintf("地区接口返回 HTTP %d", resp.StatusCode)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
	if err != nil {
		return &GeoResult{IP: ip, Err: shortErr(err)}
	}
	var v struct {
		Status      string `json:"status"`
		Message     string `json:"message"`
		Country     string `json:"country"`
		CountryCode string `json:"countryCode"`
		RegionName  string `json:"regionName"`
		City        string `json:"city"`
		Query       string `json:"query"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return &GeoResult{IP: ip, Err: "地区接口响应无法解析"}
	}
	if v.Status != "success" {
		msg := strings.TrimSpace(v.Message)
		if msg == "" {
			msg = "地区接口返回失败"
		}
		return &GeoResult{IP: ip, Err: msg}
	}
	return &GeoResult{
		IP: ip, OK: true,
		CountryCode: strings.ToUpper(strings.TrimSpace(v.CountryCode)),
		Country:     strings.TrimSpace(v.Country),
		RegionName:  strings.TrimSpace(v.RegionName),
		City:        strings.TrimSpace(v.City),
	}
}

// waitTurn 是限速闸门:保证两次外部请求之间至少间隔 MinInterval。
func (g *GeoResolver) waitTurn() {
	g.gateMu.Lock()
	defer g.gateMu.Unlock()
	if !g.lastCall.IsZero() {
		if wait := g.MinInterval - time.Since(g.lastCall); wait > 0 {
			time.Sleep(wait)
		}
	}
	g.lastCall = time.Now()
}

// GeoPending 是待解析地区的一条代理(由 Store 提供)。
type GeoPending struct {
	ProxyID int64
	GroupID int64
	ExitIP  string
	Host    string
}

// GeoPendingStat 是一次批量地区解析的结果统计。
type GeoPendingStat struct {
	Resolved int
	Failed   int
	Updated  int64 // 实际被写入地区字段的代理行数(按出口 IP 会命中多条)
}

// ResolveForGroup 解析某组待识别地区的代理。
//
// 解析顺序:有出口 IP 的先用出口 IP(流量真实出处);出口 IP 未知时退回代理 host
// (对直连型代理,host 本身就是出口)。同出口 IP 的代理会被一次批量填上。
func (m *Manager) ResolveForGroup(ctx context.Context, groupID int64, limit int) (*GeoPendingStat, error) {
	if limit <= 0 || limit > 2000 {
		limit = 200
	}
	pending, err := m.store.ListGeoPending(groupID, limit)
	if err != nil {
		return nil, err
	}
	if len(pending) == 0 {
		return &GeoPendingStat{}, nil
	}
	// 去重后逐个解析:同一出口 IP 只解析一次。
	type target struct {
		ip      string
		groupID int64
	}
	targets := []target{}
	seen := map[string]bool{}
	for _, p := range pending {
		ip := strings.TrimSpace(p.ExitIP)
		if ip == "" {
			ip = strings.TrimSpace(p.Host)
		}
		if ip == "" || seen[ip] {
			continue
		}
		seen[ip] = true
		targets = append(targets, target{ip: ip, groupID: p.GroupID})
	}

	stat := &GeoPendingStat{}
	for _, t := range targets {
		select {
		case <-ctx.Done():
			return stat, ctx.Err()
		default:
		}
		r := m.geo.Resolve(ctx, t.ip)
		if r == nil {
			continue
		}
		if !r.OK {
			stat.Failed++
			continue
		}
		stat.Resolved++
		// 按出口 IP 批量回写:同出口的代理一次填完。
		if n, err := m.store.SetProxyGeo(t.groupID, t.ip, r.CountryCode, r.Country); err != nil {
			m.logf("回写代理地区失败(%s): %v", t.ip, err)
		} else {
			stat.Updated += n
		}
	}
	m.logf("地区识别完成 组 #%d:解析 %d 条,失败 %d 条,更新 %d 个代理",
		groupID, stat.Resolved, stat.Failed, stat.Updated)
	return stat, nil
}

// RunGeoLoop 定时回填地区:每 3 分钟解析一批待识别代理。
//
// 用「小批量 + 长间隔」而不是一次性全量:地区接口限速严格,而在野代理有数万条;
// 持续小步推进既不会触发限流,也能让新导入的代理逐步补齐地区。
func (m *Manager) RunGeoLoop() {
	// 启动后等 90 秒,让源站拉取与首轮测活先跑(测活产出出口 IP,地区解析才有依据)。
	select {
	case <-m.ctx.Done():
		return
	case <-time.After(90 * time.Second):
	}
	for {
		groups, err := m.store.ListGroups()
		if err != nil {
			m.logf("读取代理组失败(地区回填): %v", err)
		} else {
			for _, g := range groups {
				select {
				case <-m.ctx.Done():
					return
				default:
				}
				if _, err := m.ResolveForGroup(m.ctx, g.ID, 120); err != nil {
					m.logf("地区回填 组«%s» 失败: %v", g.Name, err)
				}
			}
		}
		select {
		case <-m.ctx.Done():
			return
		case <-time.After(3 * time.Minute):
		}
	}
}

// GeoEnabled 返回地区识别是否启用(默认启用)。
func (m *Manager) GeoEnabled() bool { return m.geo != nil && m.geo.Enabled }

// SetGeoEnabled 开关地区识别(运行时可切)。
func (m *Manager) SetGeoEnabled(on bool) {
	if m.geo != nil {
		m.geo.Enabled = on
	}
}
