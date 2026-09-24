package db

// 代理中心(proxy_groups / proxies / proxy_sources / proxy_requests / proxy_check_runs):
// 统一管理出网代理,支撑「本地裸网 ⇄ 代理池」热切换。
//
// 分层:
//   proxy_groups      代理组(调度策略/监听端口/测活参数)—— 调度的单位
//   proxies           组内单个代理(手工 / 商业 API / 公益 API / 全网在野搜集)
//   proxy_sources     组上接入的代理池 API,定时拉取入库
//   proxy_requests    走代理池的请求流水(仪表盘数据源,记录本次调度到哪条代理)
//   proxy_check_runs  测活批次台账
//
// 枚举取值一律在 Go 侧白名单校验(表上不加 CHECK,便于扩展)。

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ---- 枚举常量与白名单 ----

const (
	ProxyGroupKindManual = "manual"
	ProxyGroupKindAPI    = "api"
	ProxyGroupKindWild   = "wild"
	ProxyGroupKindMixed  = "mixed"
)

// ValidProxyGroupKind 校验代理组类型。
func ValidProxyGroupKind(k string) bool {
	switch k {
	case ProxyGroupKindManual, ProxyGroupKindAPI, ProxyGroupKindWild, ProxyGroupKindMixed:
		return true
	}
	return false
}

const (
	ProxyStrategyRoundRobin   = "round_robin"
	ProxyStrategyRandom       = "random"
	ProxyStrategyLeastLatency = "least_latency"
	ProxyStrategyFailover     = "failover"
)

// ValidProxyStrategy 校验调度策略。
func ValidProxyStrategy(s string) bool {
	switch s {
	case ProxyStrategyRoundRobin, ProxyStrategyRandom, ProxyStrategyLeastLatency, ProxyStrategyFailover:
		return true
	}
	return false
}

const (
	ProxySourceManual = "manual"
	ProxySourceAPI    = "api"
	ProxySourcePublic = "public"
	ProxySourceWild   = "wild"
)

// ValidProxySource 校验代理来源。
func ValidProxySource(s string) bool {
	switch s {
	case ProxySourceManual, ProxySourceAPI, ProxySourcePublic, ProxySourceWild:
		return true
	}
	return false
}

const (
	ProxyStatusUntested = "untested"
	ProxyStatusAlive    = "alive"
	ProxyStatusDead     = "dead"
	ProxyStatusChecking = "checking"
)

// ValidProxyStatus 校验代理状态。
func ValidProxyStatus(s string) bool {
	switch s {
	case ProxyStatusUntested, ProxyStatusAlive, ProxyStatusDead, ProxyStatusChecking:
		return true
	}
	return false
}

const (
	// 出网模式:direct=本地裸网(直连) / pool=代理池(经选中的代理组)
	EgressModeDirect = "direct"
	EgressModePool   = "pool"
)

// ValidEgressMode 校验出网模式。
func ValidEgressMode(m string) bool {
	return m == EgressModeDirect || m == EgressModePool
}

// ---- 代理组 ----

// ProxyGroup 是一个代理组:调度的单位,承载策略、监听端口与测活参数。
type ProxyGroup struct {
	ID                   int64     `json:"id"`
	Name                 string    `json:"name"`
	Kind                 string    `json:"kind"`
	Strategy             string    `json:"strategy"`
	ListenPort           int       `json:"listen_port"`
	Enabled              bool      `json:"enabled"`
	AutoCheck            bool      `json:"auto_check"`
	CheckIntervalMinutes int       `json:"check_interval_minutes"`
	TestURL              string    `json:"test_url"`
	TimeoutMs            int       `json:"timeout_ms"`
	MaxFailures          int       `json:"max_failures"`
	Concurrency          int       `json:"concurrency"`
	Remark               string    `json:"remark"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`

	// 派生计数(列表页展示,非表字段)。
	ProxyCount  int `json:"proxy_count"`
	AliveCount  int `json:"alive_count"`
	DeadCount   int `json:"dead_count"`
	SourceCount int `json:"source_count"`
}

const proxyGroupCols = `g.id, g.name, g.kind, g.strategy, g.listen_port, g.enabled, g.auto_check,
	g.check_interval_minutes, g.test_url, g.timeout_ms, g.max_failures, g.concurrency, g.remark,
	g.created_at, g.updated_at`

// proxyGroupCountCols 附带组内代理与源站计数(列表页一次查完,避免 N+1)。
const proxyGroupCountCols = `,
	(SELECT COUNT(*) FROM proxies p WHERE p.group_id = g.id),
	(SELECT COUNT(*) FROM proxies p WHERE p.group_id = g.id AND p.status = 'alive'),
	(SELECT COUNT(*) FROM proxies p WHERE p.group_id = g.id AND p.status = 'dead'),
	(SELECT COUNT(*) FROM proxy_sources s WHERE s.group_id = g.id)`

func scanProxyGroup(sc interface{ Scan(...any) error }, withCounts bool) (*ProxyGroup, error) {
	var g ProxyGroup
	dest := []any{&g.ID, &g.Name, &g.Kind, &g.Strategy, &g.ListenPort, &g.Enabled, &g.AutoCheck,
		&g.CheckIntervalMinutes, &g.TestURL, &g.TimeoutMs, &g.MaxFailures, &g.Concurrency, &g.Remark,
		&g.CreatedAt, &g.UpdatedAt}
	if withCounts {
		dest = append(dest, &g.ProxyCount, &g.AliveCount, &g.DeadCount, &g.SourceCount)
	}
	if err := sc.Scan(dest...); err != nil {
		return nil, err
	}
	return &g, nil
}

// ProxyGroupFilter 是代理组列表的筛选条件。空值 = 不限。
type ProxyGroupFilter struct {
	Kind   string // manual | api | wild | mixed
	Q      string // 名称/备注关键字
	Sort   string // updated | created | name | count
	Desc   bool
	Limit  int
	Offset int
}

// ListProxyGroups 分页检索代理组,并返回类型计数(供分类页签展示)。
func (d *DB) ListProxyGroups(f ProxyGroupFilter) ([]*ProxyGroup, int, map[string]int, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	where := []string{"1=1"}
	args := []any{}
	if q := strings.TrimSpace(f.Q); q != "" {
		where = append(where, fmt.Sprintf("(g.name ILIKE $%d OR g.remark ILIKE $%d)", len(args)+1, len(args)+1))
		args = append(args, "%"+q+"%")
	}
	kw := strings.Join(where, " AND ")

	// 类型计数:只受 Q 影响,不受 kind 筛选影响(页签要显示各类型总数)。
	counts := map[string]int{}
	rows, err := d.Query(`SELECT kind, COUNT(*) FROM proxy_groups g WHERE `+kw+` GROUP BY kind`, args...)
	if err != nil {
		return nil, 0, nil, err
	}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			rows.Close()
			return nil, 0, nil, err
		}
		counts[k] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, nil, err
	}

	filtered := kw
	fargs := append([]any(nil), args...)
	if k := strings.TrimSpace(f.Kind); k != "" {
		fargs = append(fargs, k)
		filtered += fmt.Sprintf(" AND g.kind = $%d", len(fargs))
	}

	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM proxy_groups g WHERE `+filtered, fargs...).Scan(&total); err != nil {
		return nil, 0, nil, err
	}

	order := "g.updated_at DESC, g.id DESC"
	switch f.Sort {
	case "created":
		order = "g.created_at DESC, g.id DESC"
	case "name":
		order = "g.name ASC"
	case "count":
		order = "(SELECT COUNT(*) FROM proxies p WHERE p.group_id = g.id) DESC, g.id DESC"
	}
	if !f.Desc && f.Sort != "" {
		order = strings.Replace(order, " DESC", " ASC", 1)
	}

	q := `SELECT ` + proxyGroupCols + proxyGroupCountCols + ` FROM proxy_groups g WHERE ` + filtered +
		` ORDER BY ` + order + fmt.Sprintf(` LIMIT $%d OFFSET $%d`, len(fargs)+1, len(fargs)+2)
	fargs = append(fargs, f.Limit, f.Offset)
	items, err := d.Query(q, fargs...)
	if err != nil {
		return nil, 0, nil, err
	}
	defer items.Close()
	out := []*ProxyGroup{}
	for items.Next() {
		g, err := scanProxyGroup(items, true)
		if err != nil {
			return nil, 0, nil, err
		}
		out = append(out, g)
	}
	return out, total, counts, items.Err()
}

// GetProxyGroup 读单个代理组(不存在返回 nil, nil)。
func (d *DB) GetProxyGroup(id int64) (*ProxyGroup, error) {
	g, err := scanProxyGroup(d.QueryRow(`SELECT `+proxyGroupCols+proxyGroupCountCols+
		` FROM proxy_groups g WHERE g.id=$1`, id), true)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return g, nil
}

// ProxyGroupInput 是新建/更新代理组的输入。
type ProxyGroupInput struct {
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
	Remark               string
}

// normalizeProxyGroupInput 填默认值并校验(新建与更新共用)。
func normalizeProxyGroupInput(in *ProxyGroupInput) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return fmt.Errorf("组名必填")
	}
	if in.Kind == "" {
		in.Kind = ProxyGroupKindMixed
	}
	if !ValidProxyGroupKind(in.Kind) {
		return fmt.Errorf("无效组类型: %q", in.Kind)
	}
	if in.Strategy == "" {
		in.Strategy = ProxyStrategyRoundRobin
	}
	if !ValidProxyStrategy(in.Strategy) {
		return fmt.Errorf("无效调度策略: %q", in.Strategy)
	}
	if in.ListenPort < 0 || in.ListenPort > 65535 {
		return fmt.Errorf("监听端口必须在 0-65535(0=自动分配)")
	}
	if in.CheckIntervalMinutes <= 0 {
		in.CheckIntervalMinutes = 10
	}
	if in.TimeoutMs <= 0 {
		in.TimeoutMs = 10000
	}
	if in.MaxFailures <= 0 {
		in.MaxFailures = 3
	}
	if in.Concurrency <= 0 || in.Concurrency > 200 {
		in.Concurrency = 20
	}
	in.TestURL = strings.TrimSpace(in.TestURL)
	if in.TestURL == "" {
		in.TestURL = "http://httpbin.org/ip"
	}
	return nil
}

// CreateProxyGroup 新建代理组。
func (d *DB) CreateProxyGroup(in ProxyGroupInput) (*ProxyGroup, error) {
	if err := normalizeProxyGroupInput(&in); err != nil {
		return nil, err
	}
	g, err := scanProxyGroup(d.QueryRow(`
INSERT INTO proxy_groups(name, kind, strategy, listen_port, enabled, auto_check, check_interval_minutes,
	test_url, timeout_ms, max_failures, concurrency, remark)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
RETURNING `+proxyGroupCols,
		in.Name, in.Kind, in.Strategy, in.ListenPort, in.Enabled, in.AutoCheck, in.CheckIntervalMinutes,
		in.TestURL, in.TimeoutMs, in.MaxFailures, in.Concurrency, in.Remark), false)
	if err != nil {
		return nil, err
	}
	return d.GetProxyGroup(g.ID)
}

// UpdateProxyGroup 更新代理组(全字段覆盖)。
func (d *DB) UpdateProxyGroup(id int64, in ProxyGroupInput) error {
	if err := normalizeProxyGroupInput(&in); err != nil {
		return err
	}
	_, err := d.Exec(`UPDATE proxy_groups SET
	name=$2, kind=$3, strategy=$4, listen_port=$5, enabled=$6, auto_check=$7, check_interval_minutes=$8,
	test_url=$9, timeout_ms=$10, max_failures=$11, concurrency=$12, remark=$13, updated_at=now()
WHERE id=$1`,
		id, in.Name, in.Kind, in.Strategy, in.ListenPort, in.Enabled, in.AutoCheck, in.CheckIntervalMinutes,
		in.TestURL, in.TimeoutMs, in.MaxFailures, in.Concurrency, in.Remark)
	return err
}

// DeleteProxyGroup 删除代理组(组内代理与源站随外键级联删除)。
func (d *DB) DeleteProxyGroup(id int64) (bool, error) {
	res, err := d.Exec(`DELETE FROM proxy_groups WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListEnabledProxyGroups 取全部启用的代理组(调度器装载用)。
func (d *DB) ListEnabledProxyGroups() ([]*ProxyGroup, error) {
	rows, err := d.Query(`SELECT ` + proxyGroupCols + ` FROM proxy_groups g WHERE g.enabled ORDER BY g.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ProxyGroup{}
	for rows.Next() {
		g, err := scanProxyGroup(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ---- 代理 ----

// Proxy 是组内的一条代理。
type Proxy struct {
	ID        int64  `json:"id"`
	GroupID   int64  `json:"group_id"`
	URL       string `json:"url"`
	Protocol  string `json:"protocol"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Label     string `json:"label"`
	Source    string `json:"source"`
	SourceRef string `json:"source_ref"`
	Region    string `json:"region"`
	// 地区识别结果(由出口 IP 解析):ISO 两位国家码 + 国家名。空 = 未识别。
	CountryCode   string     `json:"country_code"`
	Country       string     `json:"country"`
	Anonymity     string     `json:"anonymity"`
	LatencyMs     int        `json:"latency_ms"`
	Status        string     `json:"status"`
	FailCount     int        `json:"fail_count"`
	SuccessCount  int        `json:"success_count"`
	LastCheckedAt *time.Time `json:"last_checked_at,omitempty"`
	LastOkAt      *time.Time `json:"last_ok_at,omitempty"`
	ExitIP        string     `json:"exit_ip"`
	UseCount      int64      `json:"use_count"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

const proxyCols = `id, group_id, url, protocol, host, port, label, source, source_ref, region,
	country_code, country,
	anonymity, latency_ms, status, fail_count, success_count, last_checked_at, last_ok_at,
	exit_ip, use_count, created_at, updated_at`

func scanProxy(sc interface{ Scan(...any) error }) (*Proxy, error) {
	var p Proxy
	var lastChecked, lastOk sql.NullTime
	if err := sc.Scan(&p.ID, &p.GroupID, &p.URL, &p.Protocol, &p.Host, &p.Port, &p.Label, &p.Source,
		&p.SourceRef, &p.Region, &p.CountryCode, &p.Country, &p.Anonymity, &p.LatencyMs, &p.Status,
		&p.FailCount, &p.SuccessCount,
		&lastChecked, &lastOk, &p.ExitIP, &p.UseCount, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if lastChecked.Valid {
		t := lastChecked.Time
		p.LastCheckedAt = &t
	}
	if lastOk.Valid {
		t := lastOk.Time
		p.LastOkAt = &t
	}
	return &p, nil
}

// ProxyFilter 是代理列表的筛选条件。空值 = 不限。
type ProxyFilter struct {
	GroupID  int64  // 必填(代理总是属于某个组)
	Status   string // untested | alive | dead | checking
	Source   string // manual | api | public | wild
	Protocol string // http | https | socks5
	Region   string
	// CountryCode 按识别出的国家码精确筛选(ISO 两位大写,如 CN/US)。
	CountryCode string
	Q           string // 地址/标签/出口 IP 关键字
	Sort        string // latency | checked | used | created
	Desc        bool
	Limit       int
	Offset      int
}

// ListProxies 分页检索组内代理,并返回状态计数(供页签展示)。
func (d *DB) ListProxies(f ProxyFilter) ([]*Proxy, int, map[string]int, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	where := []string{"group_id = $1"}
	args := []any{f.GroupID}
	add := func(cond string, v any) {
		where = append(where, fmt.Sprintf(cond, len(args)+1))
		args = append(args, v)
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		where = append(where, fmt.Sprintf(
			"(url ILIKE $%d OR label ILIKE $%d OR host ILIKE $%d OR exit_ip ILIKE $%d)",
			len(args)+1, len(args)+1, len(args)+1, len(args)+1))
		args = append(args, "%"+q+"%")
	}
	if f.Source != "" {
		add("source = $%d", f.Source)
	}
	if f.Protocol != "" {
		add("protocol = $%d", f.Protocol)
	}
	if r := strings.TrimSpace(f.Region); r != "" {
		add("region ILIKE $%d", "%"+r+"%")
	}
	if cc := strings.ToUpper(strings.TrimSpace(f.CountryCode)); cc != "" {
		add("country_code = $%d", cc)
	}
	kw := strings.Join(where, " AND ")

	// 状态计数:不受 status 筛选影响(页签要显示各状态总数)。
	counts := map[string]int{}
	rows, err := d.Query(`SELECT status, COUNT(*) FROM proxies WHERE `+kw+` GROUP BY status`, args...)
	if err != nil {
		return nil, 0, nil, err
	}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			rows.Close()
			return nil, 0, nil, err
		}
		counts[s] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, nil, err
	}

	filtered := kw
	fargs := append([]any(nil), args...)
	if f.Status != "" {
		fargs = append(fargs, f.Status)
		filtered += fmt.Sprintf(" AND status = $%d", len(fargs))
	}

	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM proxies WHERE `+filtered, fargs...).Scan(&total); err != nil {
		return nil, 0, nil, err
	}

	// 延迟排序把未测的(0)排在最后,避免「未测」抢在最快前面。
	order := "latency_ms = 0, latency_ms ASC, id DESC"
	switch f.Sort {
	case "checked":
		order = "last_checked_at DESC NULLS LAST, id DESC"
	case "used":
		order = "use_count DESC, id DESC"
	case "created":
		order = "created_at DESC, id DESC"
	}
	if !f.Desc && f.Sort != "" {
		order = strings.Replace(order, " DESC", " ASC", 1)
	}

	q := `SELECT ` + proxyCols + ` FROM proxies WHERE ` + filtered +
		` ORDER BY ` + order + fmt.Sprintf(` LIMIT $%d OFFSET $%d`, len(fargs)+1, len(fargs)+2)
	fargs = append(fargs, f.Limit, f.Offset)
	items, err := d.Query(q, fargs...)
	if err != nil {
		return nil, 0, nil, err
	}
	defer items.Close()
	out := []*Proxy{}
	for items.Next() {
		p, err := scanProxy(items)
		if err != nil {
			return nil, 0, nil, err
		}
		out = append(out, p)
	}
	return out, total, counts, items.Err()
}

// GetProxy 读单条代理(不存在返回 nil, nil)。
func (d *DB) GetProxy(id int64) (*Proxy, error) {
	p, err := scanProxy(d.QueryRow(`SELECT `+proxyCols+` FROM proxies WHERE id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// ProxyInput 是新建/更新代理的输入。
type ProxyInput struct {
	GroupID   int64
	URL       string
	Label     string
	Source    string
	SourceRef string
	Region    string
}

// ParseProxyURL 解析代理地址为 (protocol, host, port)。
// 只做形状校验(http/https/socks5 + 有主机);连通性由测活负责。
func ParseProxyURL(raw string) (protocol, host string, port int, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", 0, fmt.Errorf("代理地址必填")
	}
	// 没写协议时按 http 补全(用户常直接粘贴 host:port)。
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, perr := parseProxyURLStrict(raw)
	if perr != nil {
		return "", "", 0, perr
	}
	protocol = u.scheme
	host = u.host
	port = u.port
	if port == 0 {
		switch protocol {
		case "https":
			port = 443
		default:
			port = 8080
		}
	}
	return protocol, host, port, nil
}

// proxyURLParts 是 parseProxyURLStrict 的结果。
type proxyURLParts struct {
	scheme string
	host   string
	port   int
}

// parseProxyURLStrict 解析代理地址,只认 http/https/socks5 三种协议。
// 用户名密码(user:pass@host:port)由 url.Parse 归入 Userinfo,不进入 host/port。
func parseProxyURLStrict(raw string) (*proxyURLParts, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("解析代理地址 %q: %w", raw, err)
	}
	switch u.Scheme {
	case "http", "https", "socks5":
	case "":
		return nil, fmt.Errorf("代理 %q 缺少协议(用 http://、https:// 或 socks5://)", raw)
	default:
		return nil, fmt.Errorf("不支持的代理协议 %q(用 http、https 或 socks5)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("代理 %q 缺少主机地址", raw)
	}
	port := 0
	if ps := u.Port(); ps != "" {
		p, err := strconv.Atoi(ps)
		if err != nil || p <= 0 || p > 65535 {
			return nil, fmt.Errorf("代理 %q 的端口无效", raw)
		}
		port = p
	}
	return &proxyURLParts{scheme: u.Scheme, host: host, port: port}, nil
}

// CreateProxy 新建代理。同组同地址重复时返回已存在的那条(existing=true)。
func (d *DB) CreateProxy(in ProxyInput) (p *Proxy, existing bool, err error) {
	in.URL = strings.TrimSpace(in.URL)
	if in.GroupID <= 0 {
		return nil, false, fmt.Errorf("必须指定代理组")
	}
	protocol, host, port, err := ParseProxyURL(in.URL)
	if err != nil {
		return nil, false, err
	}
	if in.Source == "" {
		in.Source = ProxySourceManual
	}
	if !ValidProxySource(in.Source) {
		return nil, false, fmt.Errorf("无效来源: %q", in.Source)
	}
	// ON CONFLICT DO NOTHING + RETURNING:重复时无行返回,再查一次拿到既有行。
	p, err = scanProxy(d.QueryRow(`
INSERT INTO proxies(group_id, url, protocol, host, port, label, source, source_ref, region)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT (group_id, url) DO NOTHING
RETURNING `+proxyCols,
		in.GroupID, in.URL, protocol, host, port, in.Label, in.Source, in.SourceRef, in.Region))
	if err == sql.ErrNoRows {
		old, gerr := scanProxy(d.QueryRow(`SELECT `+proxyCols+` FROM proxies WHERE group_id=$1 AND url=$2`,
			in.GroupID, in.URL))
		if gerr != nil {
			return nil, false, gerr
		}
		return old, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	return p, false, nil
}

// UpdateProxy 更新代理的可编辑字段(地址不可改:改了就是另一条代理)。
func (d *DB) UpdateProxy(id int64, label, region, status string) error {
	if status != "" && !ValidProxyStatus(status) {
		return fmt.Errorf("无效状态: %q", status)
	}
	_, err := d.Exec(`UPDATE proxies SET
	label=$2, region=$3,
	status=CASE WHEN $4 = '' THEN status ELSE $4 END,
	updated_at=now()
WHERE id=$1`, id, label, region, status)
	return err
}

// DeleteProxy 删除单条代理。
func (d *DB) DeleteProxy(id int64) (bool, error) {
	res, err := d.Exec(`DELETE FROM proxies WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteProxies 批量删除(按 id 列表,组内限定防越权)。
func (d *DB) DeleteProxies(groupID int64, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	res, err := d.Exec(`DELETE FROM proxies WHERE group_id=$1 AND id = ANY($2)`, groupID, ids)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteProxiesByStatus 按状态批量清理(如一键清掉全部 dead)。
// status 为空时清空整组代理。
func (d *DB) DeleteProxiesByStatus(groupID int64, status string) (int64, error) {
	if status != "" && !ValidProxyStatus(status) {
		return 0, fmt.Errorf("无效状态: %q", status)
	}
	res, err := d.Exec(`DELETE FROM proxies WHERE group_id=$1 AND ($2 = '' OR status = $2)`, groupID, status)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---- 代理池 API 源站 ----

// ProxySource 是挂在组上的一个代理池 API 接入配置。
type ProxySource struct {
	ID              int64           `json:"id"`
	GroupID         int64           `json:"group_id"`
	ProviderKey     string          `json:"provider_key"`
	Name            string          `json:"name"`
	APIURL          string          `json:"api_url"`
	Username        string          `json:"username"`
	Params          json.RawMessage `json:"params"`
	Enabled         bool            `json:"enabled"`
	IntervalMinutes int             `json:"interval_minutes"`
	LastSyncAt      *time.Time      `json:"last_sync_at,omitempty"`
	LastStatus      string          `json:"last_status"`
	LastError       string          `json:"last_error"`
	ImportedCount   int             `json:"imported_count"`
	TotalCount      int             `json:"total_count"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`

	// 凭据存在性标记(密文永不回显)。HasAPIKey/HasPassword 由 api_key/password 是否非空派生。
	HasAPIKey   bool `json:"has_api_key"`
	HasPassword bool `json:"has_password"`
}

// proxySourceCols 不取 api_key/password 明文,改为取「是否已配置」布尔:
// 凭据密文绝不出接口(与 external_apis 的 maskCredentials 同一原则)。
const proxySourceCols = `id, group_id, provider_key, name, api_url, username,
	(api_key <> ''), (password <> ''),
	params, enabled, interval_minutes, last_sync_at, last_status, last_error,
	imported_count, total_count, created_at, updated_at`

func scanProxySource(sc interface{ Scan(...any) error }) (*ProxySource, error) {
	var s ProxySource
	var lastSync sql.NullTime
	if err := sc.Scan(&s.ID, &s.GroupID, &s.ProviderKey, &s.Name, &s.APIURL, &s.Username,
		&s.HasAPIKey, &s.HasPassword,
		&s.Params, &s.Enabled, &s.IntervalMinutes, &lastSync, &s.LastStatus, &s.LastError,
		&s.ImportedCount, &s.TotalCount, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	if lastSync.Valid {
		t := lastSync.Time
		s.LastSyncAt = &t
	}
	if s.Params == nil {
		s.Params = json.RawMessage("{}")
	}
	return &s, nil
}

// ListProxySources 列出某组的全部代理池接入(不分页:一个组的源通常个位数)。
func (d *DB) ListProxySources(groupID int64) ([]*ProxySource, error) {
	rows, err := d.Query(`SELECT `+proxySourceCols+` FROM proxy_sources WHERE group_id=$1 ORDER BY id`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ProxySource{}
	for rows.Next() {
		s, err := scanProxySource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetProxySource 读单个源站(不存在返回 nil, nil)。
func (d *DB) GetProxySource(id int64) (*ProxySource, error) {
	s, err := scanProxySource(d.QueryRow(`SELECT `+proxySourceCols+` FROM proxy_sources WHERE id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

// GetProxySourceSecrets 读出源站的凭据密文(仅拉取时内部使用,不进任何接口响应)。
func (d *DB) GetProxySourceSecrets(id int64) (apiKey, username, password string, err error) {
	err = d.QueryRow(`SELECT api_key, username, password FROM proxy_sources WHERE id=$1`, id).
		Scan(&apiKey, &username, &password)
	if err == sql.ErrNoRows {
		return "", "", "", nil
	}
	return apiKey, username, password, err
}

// ProxySourceInput 是新建/更新源站的输入。
// APIKeySealed/PasswordSealed 是已加密的密文;nil 表示「本次不改动凭据」。
type ProxySourceInput struct {
	GroupID         int64
	ProviderKey     string
	Name            string
	APIURL          string
	Username        string
	APIKeySealed    []byte
	PasswordSealed  []byte
	Params          json.RawMessage
	Enabled         bool
	IntervalMinutes int
}

// CreateProxySource 新建代理池接入。
func (d *DB) CreateProxySource(in ProxySourceInput) (*ProxySource, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.GroupID <= 0 {
		return nil, fmt.Errorf("必须指定代理组")
	}
	if in.ProviderKey == "" && strings.TrimSpace(in.APIURL) == "" {
		return nil, fmt.Errorf("请选择内置代理池或填写自定义 API 地址")
	}
	if in.Params == nil {
		in.Params = json.RawMessage("{}")
	}
	if in.IntervalMinutes < 0 {
		in.IntervalMinutes = 30
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO proxy_sources(group_id, provider_key, name, api_url, api_key, username, password, params, enabled, interval_minutes)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`,
		in.GroupID, in.ProviderKey, in.Name, in.APIURL, string(in.APIKeySealed), in.Username,
		string(in.PasswordSealed), in.Params, in.Enabled, in.IntervalMinutes).Scan(&id)
	if err != nil {
		return nil, err
	}
	return d.GetProxySource(id)
}

// UpdateProxySource 更新源站。凭据为 nil 时保持原值不变(与 settings 的「null=不改」同一约定)。
func (d *DB) UpdateProxySource(id int64, in ProxySourceInput) error {
	if in.Params == nil {
		in.Params = json.RawMessage("{}")
	}
	_, err := d.Exec(`UPDATE proxy_sources SET
	provider_key=$2, name=$3, api_url=$4, username=$5, params=$6, enabled=$7, interval_minutes=$8,
	api_key = CASE WHEN $9::boolean THEN $10 ELSE api_key END,
	password = CASE WHEN $11::boolean THEN $12 ELSE password END,
	updated_at=now()
WHERE id=$1`,
		id, in.ProviderKey, in.Name, in.APIURL, in.Username, in.Params, in.Enabled, in.IntervalMinutes,
		in.APIKeySealed != nil, string(in.APIKeySealed),
		in.PasswordSealed != nil, string(in.PasswordSealed))
	return err
}

// DeleteProxySource 删除源站(已导入的代理保留,便于追溯)。
func (d *DB) DeleteProxySource(id int64) (bool, error) {
	res, err := d.Exec(`DELETE FROM proxy_sources WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetProxySourceSync 记录一次拉取结果。
func (d *DB) SetProxySourceSync(id int64, status string, imported, total int, errMsg string) error {
	_, err := d.Exec(`UPDATE proxy_sources SET
	last_status=$2, last_error=$3, imported_count=$4, total_count=$5, last_sync_at=now(), updated_at=now()
WHERE id=$1`, id, status, errMsg, imported, total)
	return err
}

// ListDueProxySources 取全部到期的启用源站(定时拉取用)。
// interval_minutes=0 表示只手动拉,不参与定时。
func (d *DB) ListDueProxySources() ([]*ProxySource, error) {
	rows, err := d.Query(`SELECT ` + proxySourceCols + ` FROM proxy_sources
WHERE enabled AND interval_minutes > 0
  AND (last_sync_at IS NULL OR last_sync_at < now() - (interval_minutes || ' minutes')::interval)
ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ProxySource{}
	for rows.Next() {
		s, err := scanProxySource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListAllEnabledProxySources 取全部启用的源站(不限到期)。
// 供「立即拉取」单条使用:用户手动点拉取时不该受 interval 限制。
func (d *DB) ListAllEnabledProxySources() ([]*ProxySource, error) {
	rows, err := d.Query(`SELECT ` + proxySourceCols + ` FROM proxy_sources WHERE enabled ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ProxySource{}
	for rows.Next() {
		s, err := scanProxySource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ---- 请求流水(仪表盘数据源) ----

// ProxyRequest 是一条走代理池的请求流水。
type ProxyRequest struct {
	ID         int64     `json:"id"`
	Ts         time.Time `json:"ts"`
	GroupID    int64     `json:"group_id"`
	ProxyID    int64     `json:"proxy_id"`
	ProxyURL   string    `json:"proxy_url"`
	Method     string    `json:"method"`
	Host       string    `json:"host"`
	URL        string    `json:"url"`
	StatusCode int       `json:"status_code"`
	LatencyMs  int       `json:"latency_ms"`
	BytesIn    int64     `json:"bytes_in"`
	BytesOut   int64     `json:"bytes_out"`
	OK         bool      `json:"ok"`
	Error      string    `json:"error"`
	TaskID     int64     `json:"task_id"`
	Stage      string    `json:"stage"`
}

const proxyRequestCols = `id, ts, COALESCE(group_id,0), COALESCE(proxy_id,0), proxy_url, method, host, url,
	status_code, latency_ms, bytes_in, bytes_out, ok, error, COALESCE(task_id,0), stage`

func scanProxyRequest(sc interface{ Scan(...any) error }) (*ProxyRequest, error) {
	var r ProxyRequest
	if err := sc.Scan(&r.ID, &r.Ts, &r.GroupID, &r.ProxyID, &r.ProxyURL, &r.Method, &r.Host, &r.URL,
		&r.StatusCode, &r.LatencyMs, &r.BytesIn, &r.BytesOut, &r.OK, &r.Error, &r.TaskID, &r.Stage); err != nil {
		return nil, err
	}
	return &r, nil
}

// ProxyRequestFilter 是流水列表的筛选条件。空值 = 不限。
type ProxyRequestFilter struct {
	GroupID    int64
	ProxyID    int64
	OnlyFailed bool
	Host       string
	Q          string // URL 关键字
	TaskID     int64
	Limit      int
	Offset     int
}

// ListProxyRequests 分页检索请求流水(最新在前)。
func (d *DB) ListProxyRequests(f ProxyRequestFilter) ([]*ProxyRequest, int, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	where := []string{"1=1"}
	args := []any{}
	add := func(cond string, v any) {
		where = append(where, fmt.Sprintf(cond, len(args)+1))
		args = append(args, v)
	}
	if f.GroupID > 0 {
		add("group_id = $%d", f.GroupID)
	}
	if f.ProxyID > 0 {
		add("proxy_id = $%d", f.ProxyID)
	}
	if f.TaskID > 0 {
		add("task_id = $%d", f.TaskID)
	}
	if f.OnlyFailed {
		where = append(where, "NOT ok")
	}
	if h := strings.TrimSpace(f.Host); h != "" {
		add("host ILIKE $%d", "%"+h+"%")
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		add("url ILIKE $%d", "%"+q+"%")
	}
	kw := strings.Join(where, " AND ")

	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM proxy_requests WHERE `+kw, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT ` + proxyRequestCols + ` FROM proxy_requests WHERE ` + kw +
		fmt.Sprintf(` ORDER BY ts DESC, id DESC LIMIT $%d OFFSET $%d`, len(args)+1, len(args)+2)
	args = append(args, f.Limit, f.Offset)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*ProxyRequest{}
	for rows.Next() {
		r, err := scanProxyRequest(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// RecordProxyRequest 写一条请求流水(调度器每次出网成功后调用)。
func (d *DB) RecordProxyRequest(r ProxyRequest) error {
	_, err := d.Exec(`
INSERT INTO proxy_requests(group_id, proxy_id, proxy_url, method, host, url, status_code, latency_ms,
	bytes_in, bytes_out, ok, error, task_id, stage)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		nullableID(r.GroupID), nullableID(r.ProxyID), r.ProxyURL, r.Method, r.Host, r.URL,
		r.StatusCode, r.LatencyMs, r.BytesIn, r.BytesOut, r.OK, r.Error,
		nullableID(r.TaskID), r.Stage)
	return err
}

// nullableID 把 0 转成 NULL(外键列不接受 0)。
func nullableID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

// ProxyTrafficStat 是仪表盘的一个时间桶聚合。
type ProxyTrafficStat struct {
	Bucket   time.Time `json:"bucket"`
	Total    int64     `json:"total"`
	Failed   int64     `json:"failed"`
	AvgMs    int       `json:"avg_ms"`
	BytesIn  int64     `json:"bytes_in"`
	BytesOut int64     `json:"bytes_out"`
}

// ProxyTrafficSummary 是仪表盘顶部的总览数字。
type ProxyTrafficSummary struct {
	Total       int64   `json:"total"`
	Failed      int64   `json:"failed"`
	SuccessRate float64 `json:"success_rate"`
	AvgMs       int     `json:"avg_ms"`
	BytesIn     int64   `json:"bytes_in"`
	BytesOut    int64   `json:"bytes_out"`
	WindowMins  int     `json:"window_mins"`
}

// ProxyTrafficSummarySince 统计窗口内的总览(仪表盘顶部卡片)。
func (d *DB) ProxyTrafficSummarySince(since time.Time, groupID int64) (*ProxyTrafficSummary, error) {
	s := &ProxyTrafficSummary{}
	var avg sql.NullFloat64
	err := d.QueryRow(`
SELECT COUNT(*),
       COUNT(*) FILTER (WHERE NOT ok),
       COALESCE(AVG(latency_ms) FILTER (WHERE ok), 0),
       COALESCE(SUM(bytes_in),0), COALESCE(SUM(bytes_out),0)
  FROM proxy_requests
 WHERE ts >= $1 AND ($2 = 0 OR group_id = $2)`, since, groupID).
		Scan(&s.Total, &s.Failed, &avg, &s.BytesIn, &s.BytesOut)
	if err != nil {
		return nil, err
	}
	s.AvgMs = int(avg.Float64)
	if s.Total > 0 {
		s.SuccessRate = float64(s.Total-s.Failed) / float64(s.Total) * 100
	}
	return s, nil
}

// ProxyTrafficSeries 按分钟桶返回时间序列(仪表盘趋势图)。
func (d *DB) ProxyTrafficSeries(since time.Time, groupID int64) ([]*ProxyTrafficStat, error) {
	rows, err := d.Query(`
SELECT date_trunc('minute', ts) AS bucket,
       COUNT(*),
       COUNT(*) FILTER (WHERE NOT ok),
       COALESCE(AVG(latency_ms) FILTER (WHERE ok), 0)::int,
       COALESCE(SUM(bytes_in),0), COALESCE(SUM(bytes_out),0)
  FROM proxy_requests
 WHERE ts >= $1 AND ($2 = 0 OR group_id = $2)
 GROUP BY bucket ORDER BY bucket`, since, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ProxyTrafficStat{}
	for rows.Next() {
		var st ProxyTrafficStat
		if err := rows.Scan(&st.Bucket, &st.Total, &st.Failed, &st.AvgMs, &st.BytesIn, &st.BytesOut); err != nil {
			return nil, err
		}
		out = append(out, &st)
	}
	return out, rows.Err()
}

// ProxyUsageStat 是按代理聚合的调度占比(仪表盘「动态调度」排行)。
type ProxyUsageStat struct {
	ProxyID  int64  `json:"proxy_id"`
	ProxyURL string `json:"proxy_url"`
	Region   string `json:"region"`
	// 地区识别结果:国家码 + 国家名(空 = 未识别)。
	CountryCode string `json:"country_code"`
	Country     string `json:"country"`
	Source      string `json:"source"`
	Status      string `json:"status"`
	LatencyMs   int    `json:"latency_ms"`
	Total       int64  `json:"total"`
	Failed      int64  `json:"failed"`
	AvgMs       int    `json:"avg_ms"`
}

// ProxyUsageSince 返回窗口内各代理被调度的次数与成功率(降序)。
func (d *DB) ProxyUsageSince(since time.Time, groupID int64, limit int) ([]*ProxyUsageStat, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := d.Query(`
SELECT COALESCE(r.proxy_id,0), r.proxy_url, COALESCE(p.region,''),
       COALESCE(p.country_code,''), COALESCE(p.country,''),
       COALESCE(p.source,''), COALESCE(p.status,''), COALESCE(p.latency_ms,0),
       COUNT(*), COUNT(*) FILTER (WHERE NOT r.ok),
       COALESCE(AVG(r.latency_ms) FILTER (WHERE r.ok), 0)::int
  FROM proxy_requests r LEFT JOIN proxies p ON p.id = r.proxy_id
 WHERE r.ts >= $1 AND ($2 = 0 OR r.group_id = $2)
 GROUP BY r.proxy_id, r.proxy_url, p.region, p.country_code, p.country, p.source, p.status, p.latency_ms
 ORDER BY COUNT(*) DESC LIMIT $3`, since, groupID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ProxyUsageStat{}
	for rows.Next() {
		var st ProxyUsageStat
		if err := rows.Scan(&st.ProxyID, &st.ProxyURL, &st.Region,
			&st.CountryCode, &st.Country, &st.Source, &st.Status, &st.LatencyMs,
			&st.Total, &st.Failed, &st.AvgMs); err != nil {
			return nil, err
		}
		out = append(out, &st)
	}
	return out, rows.Err()
}

// PurgeProxyRequests 清理窗口之前的流水(避免无限增长)。
func (d *DB) PurgeProxyRequests(before time.Time) (int64, error) {
	res, err := d.Exec(`DELETE FROM proxy_requests WHERE ts < $1`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---- 测活批次台账 ----

// ProxyCheckRun 是一次测活批次。
type ProxyCheckRun struct {
	ID         int64      `json:"id"`
	GroupID    int64      `json:"group_id"`
	Trigger    string     `json:"trigger"`
	Status     string     `json:"status"`
	Checked    int        `json:"checked"`
	Alive      int        `json:"alive"`
	Dead       int        `json:"dead"`
	Error      string     `json:"error"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// StartProxyCheckRun 开一批测活台账。
func (d *DB) StartProxyCheckRun(groupID int64, trigger string) (int64, error) {
	var id int64
	err := d.QueryRow(`INSERT INTO proxy_check_runs(group_id, trigger, status) VALUES ($1,$2,'running')
RETURNING id`, nullableID(groupID), trigger).Scan(&id)
	return id, err
}

// FinishProxyCheckRun 收一批测活台账。
func (d *DB) FinishProxyCheckRun(id int64, status string, checked, alive, dead int, errMsg string) error {
	_, err := d.Exec(`UPDATE proxy_check_runs SET status=$2, checked=$3, alive=$4, dead=$5,
	error=$6, finished_at=now() WHERE id=$1`, id, status, checked, alive, dead, errMsg)
	return err
}

// ListProxyCheckRuns 列出最近的测活批次。
func (d *DB) ListProxyCheckRuns(groupID int64, limit int) ([]*ProxyCheckRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := d.Query(`SELECT id, COALESCE(group_id,0), trigger, status, checked, alive, dead, error,
	started_at, finished_at FROM proxy_check_runs
WHERE ($1 = 0 OR group_id = $1) ORDER BY started_at DESC LIMIT $2`, groupID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ProxyCheckRun{}
	for rows.Next() {
		var r ProxyCheckRun
		var fin sql.NullTime
		if err := rows.Scan(&r.ID, &r.GroupID, &r.Trigger, &r.Status, &r.Checked, &r.Alive, &r.Dead,
			&r.Error, &r.StartedAt, &fin); err != nil {
			return nil, err
		}
		if fin.Valid {
			t := fin.Time
			r.FinishedAt = &t
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// ---- 测活结果回写 ----

// ProxyCheckResult 是一次测活的结论(回写单条代理)。
type ProxyCheckResult struct {
	ProxyID   int64
	Alive     bool
	LatencyMs int
	ExitIP    string
	Anonymity string
	Region    string
	Err       string
	MaxFail   int // 连续失败阈值:达到即标记 dead
}

// ApplyProxyCheckResult 把一次测活结果写回代理行:
// 成功 → alive、延迟/出口 IP/匿名度刷新、fail_count 清零;
// 失败 → fail_count+1,达到阈值才标 dead(偶发抖动不立即判死)。
func (d *DB) ApplyProxyCheckResult(res ProxyCheckResult) error {
	maxFail := res.MaxFail
	if maxFail <= 0 {
		maxFail = 3
	}
	if res.Alive {
		_, err := d.Exec(`UPDATE proxies SET
	status='alive', latency_ms=$2, exit_ip=$3,
	anonymity = CASE WHEN $4 = '' THEN anonymity ELSE $4 END,
	region = CASE WHEN $5 = '' THEN region ELSE $5 END,
	fail_count=0, success_count=success_count+1, last_checked_at=now(), last_ok_at=now(), updated_at=now()
WHERE id=$1`, res.ProxyID, res.LatencyMs, res.ExitIP, res.Anonymity, res.Region)
		return err
	}
	_, err := d.Exec(`UPDATE proxies SET
	status = CASE WHEN fail_count + 1 >= $2 THEN 'dead' ELSE status END,
	latency_ms = CASE WHEN fail_count + 1 >= $2 THEN 0 ELSE latency_ms END,
	fail_count = fail_count + 1, last_checked_at=now(), updated_at=now()
WHERE id=$1`, res.ProxyID, maxFail)
	return err
}

// ListProxiesForCheck 取待测活的代理(测活器用)。
// statuses 为空时取全部;limit<=0 时不限量。
func (d *DB) ListProxiesForCheck(groupID int64, statuses []string, limit int) ([]*Proxy, error) {
	where := []string{"group_id=$1"}
	args := []any{groupID}
	if len(statuses) > 0 {
		where = append(where, "status = ANY($2)")
		args = append(args, statuses)
	}
	q := `SELECT ` + proxyCols + ` FROM proxies WHERE ` + strings.Join(where, " AND ") + ` ORDER BY id`
	if limit > 0 {
		args = append(args, limit)
		q += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Proxy{}
	for rows.Next() {
		p, err := scanProxy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// dispatchCandidateLimit 是单次装载的候选上限。
//
// 为什么必须设上限:在野搜集一轮就能导入上万条代理,而调度器每 10 秒刷新一次候选。
// 无上限时若一条 alive 都没有,退回查询会把整表拉进内存(实测 31008 行),
// 每 10 秒一次既浪费内存也压 DB。取前 N 条(按延迟升序)已足够调度使用:
// 延迟好的代理本就该优先被选中,长尾没必要进内存。
const dispatchCandidateLimit = 500

// ListDispatchableProxies 取可用于调度的代理:alive 优先,延迟升序。
// 没有任何 alive 时返回全部未标记 dead 的(冷启动阶段让调度器仍能试)。
func (d *DB) ListDispatchableProxies(groupID int64) ([]*Proxy, error) {
	rows, err := d.Query(`SELECT `+proxyCols+` FROM proxies
WHERE group_id=$1 AND status='alive' ORDER BY latency_ms = 0, latency_ms ASC, id LIMIT $2`,
		groupID, dispatchCandidateLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Proxy{}
	for rows.Next() {
		p, err := scanProxy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if len(out) > 0 {
		return out, nil
	}
	// 冷启动:还没测活过(alive=0)时不能把整组判死,退回「非 dead」集合。
	rows2, err := d.Query(`SELECT `+proxyCols+` FROM proxies
WHERE group_id=$1 AND status <> 'dead' ORDER BY latency_ms = 0, latency_ms ASC, id LIMIT $2`,
		groupID, dispatchCandidateLimit)
	if err != nil {
		return nil, err
	}
	defer rows2.Close()
	for rows2.Next() {
		p, err := scanProxy(rows2)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows2.Err()
}

// BumpProxyUse 累加代理被调度次数(每次成功调度调用)。
func (d *DB) BumpProxyUse(id int64) error {
	_, err := d.Exec(`UPDATE proxies SET use_count = use_count + 1 WHERE id=$1`, id)
	return err
}

// CountProxySourcesByProvider 统计某组已接入的各 provider 数量(去重用)。
// 返回 provider_key -> 条数。
func (d *DB) CountProxySourcesByProvider(groupID int64) (map[string]int, error) {
	rows, err := d.Query(`SELECT provider_key, COUNT(*) FROM proxy_sources WHERE group_id=$1 GROUP BY provider_key`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// ProxyCountsByStatus 统计全平台各状态的代理数(仪表盘顶部)。
func (d *DB) ProxyCountsByStatus() (total, alive, dead int, err error) {
	var t, a, dd sql.NullInt64
	err = d.QueryRow(`SELECT COUNT(*),
		COUNT(*) FILTER (WHERE status='alive'),
		COUNT(*) FILTER (WHERE status='dead') FROM proxies`).Scan(&t, &a, &dd)
	if err != nil {
		return 0, 0, 0, err
	}
	return int(t.Int64), int(a.Int64), int(dd.Int64), nil
}

// ---- 地区识别 ----

// GeoResult 是一个 IP 的地区解析结果。
type GeoResult struct {
	IP          string
	OK          bool
	CountryCode string
	Country     string
	RegionName  string
	City        string
	Err         string
}

// GeoCacheLookup 批量查地区缓存。返回 ip -> 结果(未缓存的 ip 不在返回里)。
func (d *DB) GeoCacheLookup(ips []string) (map[string]*GeoResult, error) {
	out := map[string]*GeoResult{}
	if len(ips) == 0 {
		return out, nil
	}
	rows, err := d.Query(`SELECT ip, ok, country_code, country, region_name, city, error
FROM proxy_geo_cache WHERE ip = ANY($1)`, ips)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var g GeoResult
		if err := rows.Scan(&g.IP, &g.OK, &g.CountryCode, &g.Country, &g.RegionName, &g.City, &g.Err); err != nil {
			return nil, err
		}
		out[g.IP] = &g
	}
	return out, rows.Err()
}

// GeoCachePut 写一条地区缓存(已存在则覆盖:IP 归属会变,但短期复用足够)。
func (d *DB) GeoCachePut(g GeoResult) error {
	_, err := d.Exec(`
INSERT INTO proxy_geo_cache(ip, ok, country_code, country, region_name, city, error)
VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT (ip) DO UPDATE SET
	ok=$2, country_code=$3, country=$4, region_name=$5, city=$6, error=$7, updated_at=now()`,
		g.IP, g.OK, g.CountryCode, g.Country, g.RegionName, g.City, g.Err)
	return err
}

// SetProxyGeo 把地区结果写回代理行(按出口 IP 匹配该组全部同出口的代理)。
//
// 为什么按 exit_ip 而不是按代理 id:在野清单里大量条目共用同一出口 IP,
// 一次解析就该把同出口的代理全部填上,省下大量限速配额。
func (d *DB) SetProxyGeo(groupID int64, exitIP, countryCode, country string) (int64, error) {
	if strings.TrimSpace(exitIP) == "" {
		return 0, nil
	}
	res, err := d.Exec(`UPDATE proxies SET country_code=$3, country=$4, updated_at=now()
WHERE group_id=$1 AND exit_ip=$2 AND (country_code <> $3 OR country <> $4)`,
		groupID, exitIP, countryCode, country)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetProxyGeoByID 按代理 id 写地区(出口 IP 未知时用代理自身 host 解析的结果)。
func (d *DB) SetProxyGeoByID(proxyID int64, countryCode, country string) error {
	_, err := d.Exec(`UPDATE proxies SET country_code=$2, country=$3, updated_at=now() WHERE id=$1`,
		proxyID, countryCode, country)
	return err
}

// ListGeoPending 取待解析地区的代理:有出口 IP 或可解析的 host,且尚未识别国家。
// limit<=0 时不限量(调用方务必给上限,避免一次拉全表)。
func (d *DB) ListGeoPending(groupID int64, limit int) ([]*Proxy, error) {
	q := `SELECT ` + proxyCols + ` FROM proxies
WHERE ($1 = 0 OR group_id = $1)
  AND country_code = ''
  AND (exit_ip <> '' OR host <> '')
ORDER BY (exit_ip <> '') DESC, last_ok_at DESC NULLS LAST, id`
	if limit > 0 {
		return d.queryProxies(q+fmt.Sprintf(" LIMIT %d", limit), groupID)
	}
	return d.queryProxies(q, groupID)
}

// queryProxies 是 ListGeoPending 的取行辅助。
func (d *DB) queryProxies(q string, args ...any) ([]*Proxy, error) {
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Proxy{}
	for rows.Next() {
		p, err := scanProxy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ProxyCountryStat 是按国家聚合的代理计数(筛选器下拉用)。
type ProxyCountryStat struct {
	CountryCode string `json:"country_code"`
	Country     string `json:"country"`
	Total       int    `json:"total"`
	Alive       int    `json:"alive"`
}

// ListProxyCountries 返回某组已识别出的国家分布(按总数降序)。
// groupID=0 表示全平台。
func (d *DB) ListProxyCountries(groupID int64) ([]*ProxyCountryStat, error) {
	rows, err := d.Query(`
SELECT country_code, MAX(country) AS country, COUNT(*), COUNT(*) FILTER (WHERE status='alive')
  FROM proxies
 WHERE ($1 = 0 OR group_id = $1) AND country_code <> ''
 GROUP BY country_code ORDER BY COUNT(*) DESC, country_code`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ProxyCountryStat{}
	for rows.Next() {
		var s ProxyCountryStat
		if err := rows.Scan(&s.CountryCode, &s.Country, &s.Total, &s.Alive); err != nil {
			return nil, err
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}

// CountGeoPending 统计某组待解析地区的代理数(UI 显示「还有多少条未识别」)。
func (d *DB) CountGeoPending(groupID int64) (int, error) {
	var n int
	err := d.QueryRow(`SELECT COUNT(*) FROM proxies
WHERE ($1 = 0 OR group_id = $1) AND country_code = '' AND (exit_ip <> '' OR host <> '')`,
		groupID).Scan(&n)
	return n, err
}
