package server

// 代理中心:Server 侧接入。
//
// 本文件分两部分:
//   1. proxyStore —— 把 db 层适配成 proxycenter.Store(包边界:proxycenter 不依赖 db)。
//   2. proxy_api.go —— REST 接口。
//
// 出网切换的关键:Manager.ProxyAddr() 会优先返回 proxycenter 的本地监听地址,
// 因此平台上二十余处 http.Client 构造点无需逐个改造——它们全都已经通过
// Manager.ProxyAddr() 取出口地址。

import (
	"fmt"
	"log"
	"net/url"
	"strings"

	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/proxycenter"
)

// proxyStore 是 proxycenter.Store 的实现。
type proxyStore struct {
	pg *pgdb.DB
	s  *Server
}

// newProxyStore 构造适配器。
func newProxyStore(s *Server, pg *pgdb.DB) *proxyStore { return &proxyStore{pg: pg, s: s} }

func (p *proxyStore) ListGroups() ([]proxycenter.Group, error) {
	rows, err := p.pg.ListEnabledProxyGroups()
	if err != nil {
		return nil, err
	}
	out := make([]proxycenter.Group, 0, len(rows))
	for _, g := range rows {
		out = append(out, proxycenter.Group{
			ID: g.ID, Name: g.Name, Kind: g.Kind, Strategy: g.Strategy, ListenPort: g.ListenPort,
			Enabled: g.Enabled, AutoCheck: g.AutoCheck, CheckIntervalMinutes: g.CheckIntervalMinutes,
			TestURL: g.TestURL, TimeoutMs: g.TimeoutMs, MaxFailures: g.MaxFailures, Concurrency: g.Concurrency,
			Rules: schedRulesFromDB(g.Rules),
		})
	}
	return out, nil
}

func (p *proxyStore) ListCandidates(groupID int64) ([]proxycenter.Candidate, error) {
	rows, err := p.pg.ListDispatchableProxies(groupID)
	if err != nil {
		return nil, err
	}
	out := make([]proxycenter.Candidate, 0, len(rows))
	for _, r := range rows {
		u, err := url.Parse(r.URL)
		if err != nil || u.Host == "" {
			// 库里存了脏地址:跳过而不是让整组调度失败。
			continue
		}
		out = append(out, proxycenter.Candidate{
			ID: r.ID, Raw: r.URL, URL: u, LatencyMs: r.LatencyMs, Region: r.Region, Source: r.Source,
			Priority: r.Priority, Streak: r.Streak,
		})
	}
	return out, nil
}

func (p *proxyStore) ListTargets(groupID int64, statuses []string) ([]proxycenter.CheckTarget, error) {
	rows, err := p.pg.ListProxiesForCheck(groupID, statuses, 0)
	if err != nil {
		return nil, err
	}
	out := make([]proxycenter.CheckTarget, 0, len(rows))
	for _, r := range rows {
		out = append(out, proxycenter.CheckTarget{ID: r.ID, URL: r.URL, SourceRef: r.SourceRef})
	}
	return out, nil
}

func (p *proxyStore) ApplyCheck(groupID int64, out proxycenter.CheckOutcome, maxFailures int, rules proxycenter.SchedRules) error {
	return p.pg.ApplyProxyCheckResult(pgdb.ProxyCheckResult{
		ProxyID: out.ProxyID, Alive: out.Alive, LatencyMs: out.LatencyMs,
		ExitIP: out.ExitIP, Anonymity: out.Anonymity, Region: out.Region,
		Err: out.Err, MaxFail: maxFailures,
		Rules: schedRulesToDB(rules),
	})
}

// schedRulesFromDB / schedRulesToDB 是 db.SchedRules ⇄ proxycenter.SchedRules 的
// 逐字段转换(两个类型镜像存在,因为 proxycenter 不 import db)。
func schedRulesFromDB(r pgdb.SchedRules) proxycenter.SchedRules {
	return proxycenter.SchedRules{
		AutoBoost: r.AutoBoost, BoostEvery: r.BoostEvery, BoostStep: r.BoostStep,
		BoostCap: r.BoostCap, DemoteOnFail: r.DemoteOnFail,
	}
}

func schedRulesToDB(r proxycenter.SchedRules) pgdb.SchedRules {
	return pgdb.SchedRules{
		AutoBoost: r.AutoBoost, BoostEvery: r.BoostEvery, BoostStep: r.BoostStep,
		BoostCap: r.BoostCap, DemoteOnFail: r.DemoteOnFail,
	}
}

func (p *proxyStore) RecordRequest(rec proxycenter.RequestRecord) error {
	return p.pg.RecordProxyRequest(pgdb.ProxyRequest{
		GroupID: rec.GroupID, ProxyID: rec.ProxyID, ProxyURL: rec.ProxyURL,
		Method: rec.Method, Host: rec.Host, URL: rec.URL,
		StatusCode: rec.StatusCode, LatencyMs: rec.LatencyMs,
		BytesIn: rec.BytesIn, BytesOut: rec.BytesOut,
		OK: rec.OK, Error: rec.Error, TaskID: rec.TaskID, Stage: rec.Stage,
	})
}

func (p *proxyStore) BumpUse(proxyID int64) error { return p.pg.BumpProxyUse(proxyID) }

// DueSources 取到期的启用源站,并解密凭据(密文只在进程内解开)。
func (p *proxyStore) DueSources() ([]proxycenter.SourceConfig, error) {
	rows, err := p.pg.ListDueProxySources()
	if err != nil {
		return nil, err
	}
	return p.decryptSources(rows)
}

// allSources 取全部启用源站(不限到期),供「立即拉取」单条调用。
func (p *proxyStore) allSources() ([]proxycenter.SourceConfig, error) {
	rows, err := p.pg.ListAllEnabledProxySources()
	if err != nil {
		return nil, err
	}
	return p.decryptSources(rows)
}

// decryptSources 把源站行转成运行时配置,顺带解密凭据。
func (p *proxyStore) decryptSources(rows []*pgdb.ProxySource) ([]proxycenter.SourceConfig, error) {
	out := make([]proxycenter.SourceConfig, 0, len(rows))
	for _, s := range rows {
		cfg := proxycenter.SourceConfig{
			ID: s.ID, GroupID: s.GroupID, ProviderKey: s.ProviderKey, Name: s.Name,
			APIURL: s.APIURL, Username: s.Username, Enabled: s.Enabled,
			IntervalMinutes: s.IntervalMinutes,
			Params:          proxycenter.MarshalParams(s.Params),
		}
		// 凭据解密失败不致命:公益源本就没有凭据,继续拉即可。
		if p.s.apiSecret != nil {
			key, un, pw, err := p.pg.GetProxySourceSecrets(s.ID)
			if err != nil {
				log.Printf("[proxy-center] 读取源 #%d 凭据失败: %v", s.ID, err)
			} else {
				if key != "" {
					if plain, err := p.s.apiSecret.open([]byte(key)); err == nil {
						cfg.APIKey = string(plain)
					} else {
						log.Printf("[proxy-center] 解密源 #%d API Key 失败: %v", s.ID, err)
					}
				}
				cfg.Username = un
				if pw != "" {
					if plain, err := p.s.apiSecret.open([]byte(pw)); err == nil {
						cfg.Password = string(plain)
					}
				}
			}
		}
		out = append(out, cfg)
	}
	return out, nil
}

func (p *proxyStore) MarkSourceSync(id int64, status string, imported, total int, errMsg string) {
	if err := p.pg.SetProxySourceSync(id, status, imported, total, errMsg); err != nil {
		log.Printf("[proxy-center] 记录源 #%d 拉取结果失败: %v", id, err)
	}
}

// ImportProxies 把拉到的 host:port 批量入库。
//
// 逐条走 CreateProxy:它内部有 (group_id, url) 唯一约束,天然幂等,
// 重复拉取同一份清单不会产生重复行。批量导入用单事务会更省,但源站清单动辄
// 数千条,单事务反而容易长时间持锁;这里按条 upsert,失败只计不计中断。
func (p *proxyStore) ImportProxies(groupID int64, protocol, source, sourceRef string, hostPorts []string) (int, error) {
	imported := 0
	for _, hp := range hostPorts {
		raw := buildProxyRawURL(protocol, hp)
		if raw == "" {
			continue
		}
		_, existing, err := p.pg.CreateProxy(pgdb.ProxyInput{
			GroupID: groupID, URL: raw, Source: source, SourceRef: sourceRef,
		})
		if err != nil {
			// 单条脏数据(端口非法等)不该中断整批导入。
			continue
		}
		if !existing {
			imported++
		}
	}
	return imported, nil
}

// buildProxyRawURL 把 "host:port" 按协议补全成完整代理地址。
func buildProxyRawURL(protocol, hostPort string) string {
	hostPort = strings.TrimSpace(hostPort)
	if hostPort == "" {
		return ""
	}
	// 清单里偶尔自带协议前缀,统一剥掉再按本源协议补,避免 "http://http://..."。
	if i := strings.Index(hostPort, "://"); i >= 0 {
		hostPort = hostPort[i+3:]
	}
	if _, _, _, err := pgdb.ParseProxyURL("http://" + hostPort); err != nil {
		return ""
	}
	switch protocol {
	case "https", "socks5":
		return protocol + "://" + hostPort
	default:
		return "http://" + hostPort
	}
}

func (p *proxyStore) StartCheckRun(groupID int64, trigger string) (int64, error) {
	return p.pg.StartProxyCheckRun(groupID, trigger)
}

func (p *proxyStore) FinishCheckRun(id int64, status string, checked, alive, dead int, errMsg string) {
	if err := p.pg.FinishProxyCheckRun(id, status, checked, alive, dead, errMsg); err != nil {
		log.Printf("[proxy-center] 收测活台账 #%d 失败: %v", id, err)
	}
}

// ---- 地区识别 ----

// GeoLookup 批量查地区缓存(ip → 结果)。
func (p *proxyStore) GeoLookup(ips []string) (map[string]*proxycenter.GeoResult, error) {
	cached, err := p.pg.GeoCacheLookup(ips)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*proxycenter.GeoResult, len(cached))
	for ip, g := range cached {
		out[ip] = &proxycenter.GeoResult{
			IP: g.IP, OK: g.OK, CountryCode: g.CountryCode, Country: g.Country,
			RegionName: g.RegionName, City: g.City, Err: g.Err,
		}
	}
	return out, nil
}

// GeoPut 写地区缓存。
func (p *proxyStore) GeoPut(g proxycenter.GeoResult) error {
	return p.pg.GeoCachePut(pgdb.GeoResult{
		IP: g.IP, OK: g.OK, CountryCode: g.CountryCode, Country: g.Country,
		RegionName: g.RegionName, City: g.City, Err: g.Err,
	})
}

// ListGeoPending 取待解析地区的代理。
func (p *proxyStore) ListGeoPending(groupID int64, limit int) ([]proxycenter.GeoPending, error) {
	rows, err := p.pg.ListGeoPending(groupID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]proxycenter.GeoPending, 0, len(rows))
	for _, r := range rows {
		out = append(out, proxycenter.GeoPending{
			ProxyID: r.ID, GroupID: r.GroupID, ExitIP: r.ExitIP, Host: r.Host,
		})
	}
	return out, nil
}

// SetProxyGeo 按出口 IP 把地区写回该组全部同出口的代理。
func (p *proxyStore) SetProxyGeo(groupID int64, exitIP, countryCode, country string) (int64, error) {
	return p.pg.SetProxyGeo(groupID, exitIP, countryCode, country)
}

// initProxyCenter 构造代理中心运行时并启动后台任务。
//
// 出网模式与选中组从 settings 恢复:进程重启后用户上次的选择要继续生效,
// 否则「代理池模式」会在重启后静默退回裸网(用户以为还在走代理)。
func (s *Server) initProxyCenter() {
	if s.m == nil || s.m.PG() == nil {
		return
	}
	store := newProxyStore(s, s.m.PG())
	pc := proxycenter.NewManager(s.ctx, store)
	s.m.proxyCenter = pc

	// 恢复上次的出网模式(默认裸网)。
	mode := "direct"
	if v, _, _ := s.m.PG().GetSetting(settingProxyEgressMode); pgdb.ValidEgressMode(v) {
		mode = v
	}
	groupID := int64(0)
	if v, _, _ := s.m.PG().GetSetting(settingProxyActiveGroup); v != "" {
		if n, err := parseInt64(v); err == nil && n > 0 {
			groupID = n
		}
	}
	if mode == "pool" && groupID > 0 {
		if err := pc.SetEgress("pool", groupID); err != nil {
			log.Printf("[proxy-center] 恢复代理池模式失败(退回裸网): %v", err)
			mode = "direct"
		} else {
			log.Printf("[proxy-center] 已恢复代理池模式:组 #%d", groupID)
		}
	}
	// 启动全部启用组的监听器:即便当前是裸网,用户切到代理池时也要立刻可用。
	pc.StartEnabledPools()

	go pc.RunSourceSyncLoop()
	go pc.RunCheckLoop()
	// 地区识别回填:定时小批量解析待识别代理的出口地区(限速接口,不一次拉全量)。
	go pc.RunGeoLoop()
	log.Printf("[proxy-center] 代理中心已启动(出网模式 %s)", mode)
}

// parseInt64 是 strconv.ParseInt 的薄封装(避免在本文件再引 strconv)。
func parseInt64(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n)
	return n, err
}
