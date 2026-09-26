package db

// 资产监控 db 层(契约 MONITOR_CONTRACTS.md):recon_monitor_targets(每企业监控
// 偏好 + 上轮水位)与 recon_monitor_runs(每轮摘要,监控的审计轨迹——监控轮没有
// 任务行,不走 staged_assets)。本文件只实现冻结签名,调度与单轮执行在 server 层。
// 硬约束:集合字段(json.RawMessage)空值一律 "[]",绝不让 nil 漏出去(JSON null
// 会打崩前端);raw SQL 字符串里只允许 `--` 注释(曾把 `//` 写进 SQL 导致 42601
// 全链路静默失败)。

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// =====================================================================
// MonitorTarget —— recon_monitor_targets
// =====================================================================

// MonitorTarget 是一个企业的监控偏好行。没有行的企业 = 跟随全局(缺省即监控),
// enabled=false 是显式退出;interval_hours NULL = 跟随全局间隔。
type MonitorTarget struct {
	CompanyID     int64      `json:"company_id"`
	Enabled       bool       `json:"enabled"`
	IntervalHours *int       `json:"interval_hours"`
	LastRunAt     *time.Time `json:"last_run_at"`
	LastRunID     *int64     `json:"last_run_id"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

const monitorTargetCols = `company_id, enabled, interval_hours, last_run_at, last_run_id, created_at, updated_at`

func scanMonitorTarget(sc interface{ Scan(...any) error }) (*MonitorTarget, error) {
	var t MonitorTarget
	if err := sc.Scan(&t.CompanyID, &t.Enabled, &t.IntervalHours, &t.LastRunAt, &t.LastRunID,
		&t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

// UpsertMonitorTarget 写一个企业的监控偏好。三态语义:
//   - enabled:  nil=不动(新行缺省 true),非 nil=覆盖;
//   - interval: clearInterval=true → 清除覆盖(NULL,回全局);
//     intervalHours 非 nil → 设置覆盖(必须 >0);两者都否 → 不动(新行=NULL)。
func (d *DB) UpsertMonitorTarget(companyID int64, enabled *bool, intervalHours *int, clearInterval bool) (*MonitorTarget, error) {
	if companyID <= 0 {
		return nil, fmt.Errorf("company_id 必填")
	}
	if intervalHours != nil && *intervalHours <= 0 {
		return nil, fmt.Errorf("interval_hours 必须为正整数(小时)")
	}
	t, err := scanMonitorTarget(d.QueryRow(`
INSERT INTO recon_monitor_targets(company_id, enabled, interval_hours)
VALUES ($1, COALESCE($2::bool, true),
        CASE WHEN $4::bool THEN NULL ELSE $3::int END)
ON CONFLICT (company_id) DO UPDATE SET
    enabled        = COALESCE($2::bool, recon_monitor_targets.enabled),
    interval_hours = CASE
        WHEN $4::bool THEN NULL
        ELSE COALESCE($3::int, recon_monitor_targets.interval_hours)
    END,
    updated_at = now()
RETURNING `+monitorTargetCols, companyID, enabled, intervalHours, clearInterval))
	if err != nil {
		return nil, fmt.Errorf("upsert monitor target %d: %w", companyID, err)
	}
	return t, nil
}

// GetMonitorTarget 读一个企业的监控偏好行;无行返回 (nil, nil)(=跟随全局,不是错误)。
func (d *DB) GetMonitorTarget(companyID int64) (*MonitorTarget, error) {
	t, err := scanMonitorTarget(d.QueryRow(
		`SELECT `+monitorTargetCols+` FROM recon_monitor_targets WHERE company_id=$1`, companyID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

// ListMonitorTargets 列出全部监控偏好行(有显式行的企业;其余企业=跟随全局)。
func (d *DB) ListMonitorTargets() ([]MonitorTarget, error) {
	rows, err := d.Query(`SELECT ` + monitorTargetCols + ` FROM recon_monitor_targets ORDER BY company_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MonitorTarget{}
	for rows.Next() {
		t, err := scanMonitorTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// ResetMonitorTargetRun 把一个企业的水位清零(last_run_at/last_run_id 置 NULL),
// 下次调度自然立即视为 due——「试跑一家」用。
func (d *DB) ResetMonitorTargetRun(companyID int64) error {
	_, err := d.Exec(`UPDATE recon_monitor_targets SET last_run_at=NULL, last_run_id=NULL WHERE company_id=$1`, companyID)
	return err
}

// =====================================================================
// MonitorCompany —— 有 kind=domain 范围的候选企业
// =====================================================================

// MonitorCompany 是一个纳入监控候选的企业及其域名范围清单。
type MonitorCompany struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	DomainCount int      `json:"domain_count"`
	Domains     []string `json:"domains"`
}

// ListMonitorCandidateCompanies 列出 company_scope 里有 kind='domain' 规则的企业
// (全局开关打开后自动纳入监控的全集),按企业 id 升序,域名清单按字典序。
func (d *DB) ListMonitorCandidateCompanies() ([]MonitorCompany, error) {
	rows, err := d.Query(`
SELECT c.id, c.name, COUNT(s.id)::int, COALESCE(string_agg(s.domain, ',' ORDER BY s.domain), '')
FROM companies c
JOIN company_scope s ON s.company_id = c.id AND s.kind = 'domain'
GROUP BY c.id, c.name
ORDER BY c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MonitorCompany{}
	for rows.Next() {
		var m MonitorCompany
		var domains string
		if err := rows.Scan(&m.ID, &m.Name, &m.DomainCount, &domains); err != nil {
			return nil, err
		}
		m.Domains = []string{}
		if domains != "" {
			m.Domains = strings.Split(domains, ",")
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// =====================================================================
// MonitorRun —— recon_monitor_runs
// =====================================================================

// MonitorRun 是一轮监控的摘要记录。Collectors/NewDomains 用 RawMessage 直通
// JSONB 列;空值一律 "[]"(列表接口不查 new_domains 列,scan 出来也置 "[]")。
type MonitorRun struct {
	ID           int64           `json:"id"`
	CompanyID    *int64          `json:"company_id,omitempty"`
	CompanyName  string          `json:"company_name"`
	Trigger      string          `json:"trigger"`
	Status       string          `json:"status"`
	Collectors   json.RawMessage `json:"collectors"`
	TotalFound   int             `json:"total_found"`
	NewCount     int             `json:"new_count"`
	NewDomains   json.RawMessage `json:"new_domains"`
	NewTruncated bool            `json:"new_truncated"`
	ProbeOK      int             `json:"probe_ok"`
	ProbeFailed  int             `json:"probe_failed"`
	Error        string          `json:"error"`
	StartedAt    time.Time       `json:"started_at"`
	FinishedAt   *time.Time      `json:"finished_at,omitempty"`
}

// 列表列清单不带 new_domains(可能 300 条,防大 JSON 拖列表;契约冻结)。
const monitorRunListCols = `id, company_id, company_name, trigger, status, collectors::text,
	total_found, new_count, new_truncated, probe_ok, probe_failed, error, started_at, finished_at`

const monitorRunFullCols = `id, company_id, company_name, trigger, status, collectors::text,
	total_found, new_count, new_domains::text, new_truncated, probe_ok, probe_failed, error, started_at, finished_at`

// scanMonitorRun 按列清单顺序扫描;full=true 时多扫一个 new_domains 列。
func scanMonitorRun(sc interface{ Scan(...any) error }, full bool) (*MonitorRun, error) {
	var r MonitorRun
	var collectors string
	var newDomains string
	dest := []any{&r.ID, &r.CompanyID, &r.CompanyName, &r.Trigger, &r.Status, &collectors,
		&r.TotalFound, &r.NewCount}
	if full {
		dest = append(dest, &newDomains)
	}
	dest = append(dest, &r.NewTruncated, &r.ProbeOK, &r.ProbeFailed, &r.Error, &r.StartedAt, &r.FinishedAt)
	if err := sc.Scan(dest...); err != nil {
		return nil, err
	}
	r.Collectors = jsonRawOrEmptyArray(collectors)
	if full {
		r.NewDomains = jsonRawOrEmptyArray(newDomains)
	} else {
		r.NewDomains = json.RawMessage("[]")
	}
	return &r, nil
}

// jsonRawOrEmptyArray 把 JSONB::text 扫描结果归一为非空 RawMessage:空串/空白 → "[]"。
func jsonRawOrEmptyArray(s string) json.RawMessage {
	if strings.TrimSpace(s) == "" {
		return json.RawMessage("[]")
	}
	return json.RawMessage(s)
}

// StartMonitorRun 开一轮监控记录(status='running'),返回轮 id。
func (d *DB) StartMonitorRun(companyID int64, companyName, trigger string) (int64, error) {
	if trigger == "" {
		trigger = "schedule"
	}
	var cid any
	if companyID > 0 {
		cid = companyID
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO recon_monitor_runs(company_id, company_name, trigger, status)
VALUES ($1, $2, $3, 'running')
RETURNING id`, cid, companyName, trigger).Scan(&id)
	return id, err
}

// FinishMonitorRun 结束一轮监控:轮行落摘要 + 同一事务里回写 targets 水位
// (无 targets 行则插一行——「跑过即纳入管理」,水位是重启续跑/overdue 补跑的依据)。
func (d *DB) FinishMonitorRun(runID, companyID int64, status string, collectors json.RawMessage,
	totalFound, newCount int, newDomains json.RawMessage, newTruncated bool,
	probeOK, probeFailed int, errMsg string) error {
	collectors = jsonRawOrEmptyArray(string(collectors))
	newDomains = jsonRawOrEmptyArray(string(newDomains))
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`
UPDATE recon_monitor_runs SET
    status=$2, collectors=$3::jsonb, total_found=$4, new_count=$5,
    new_domains=$6::jsonb, new_truncated=$7, probe_ok=$8, probe_failed=$9,
    error=$10, finished_at=now()
WHERE id=$1`,
		runID, status, collectors, totalFound, newCount,
		newDomains, newTruncated, probeOK, probeFailed, errMsg); err != nil {
		return err
	}
	if companyID > 0 {
		if _, err := tx.Exec(`
INSERT INTO recon_monitor_targets(company_id,last_run_at,last_run_id) VALUES($2,now(),$1)
ON CONFLICT (company_id) DO UPDATE SET last_run_at=now(), last_run_id=$1`, runID, companyID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MonitorRunFilter 是轮次列表的筛选条件。空值 = 不限。
type MonitorRunFilter struct {
	CompanyID int64  // 0=不限
	Status    string // running | done | failed(空=全部)
	Limit     int
	Offset    int
}

// ListMonitorRuns 分页检索轮次列表,返回当前页与**过滤后**的 total
// (分页铁律:total 在 WHERE 过滤之后算)。列表不含 new_domains。
func (d *DB) ListMonitorRuns(f MonitorRunFilter) ([]MonitorRun, int, error) {
	if f.Limit <= 0 {
		f.Limit = 20
	}
	if f.Limit > 200 {
		f.Limit = 200
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	where := []string{"1=1"}
	args := []any{}
	if f.CompanyID > 0 {
		args = append(args, f.CompanyID)
		where = append(where, fmt.Sprintf("company_id = $%d", len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		where = append(where, fmt.Sprintf("status = $%d", len(args)))
	}
	kw := strings.Join(where, " AND ")

	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM recon_monitor_runs WHERE `+kw, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT ` + monitorRunListCols + ` FROM recon_monitor_runs WHERE ` + kw +
		` ORDER BY id DESC` + fmt.Sprintf(` LIMIT $%d OFFSET $%d`, len(args)+1, len(args)+2)
	args = append(args, f.Limit, f.Offset)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []MonitorRun{}
	for rows.Next() {
		r, err := scanMonitorRun(rows, false)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *r)
	}
	return out, total, rows.Err()
}

// GetMonitorRun 读一轮完整记录(含 new_domains);不存在返回 (nil, nil)。
func (d *DB) GetMonitorRun(id int64) (*MonitorRun, error) {
	r, err := scanMonitorRun(d.QueryRow(
		`SELECT `+monitorRunFullCols+` FROM recon_monitor_runs WHERE id=$1`, id), true)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// MonitorRunsByIDs 按 id 集合批量取完整轮次(含 new_domains),以 id 为键;
// 查不到的 id 不在 map 里。空入参直接返回空 map。
func (d *DB) MonitorRunsByIDs(ids []int64) (map[int64]*MonitorRun, error) {
	out := map[int64]*MonitorRun{}
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	rows, err := d.Query(`SELECT `+monitorRunFullCols+` FROM recon_monitor_runs
WHERE id IN (`+strings.Join(placeholders, ",")+`) ORDER BY id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanMonitorRun(rows, true)
		if err != nil {
			return nil, err
		}
		out[r.ID] = r
	}
	return out, rows.Err()
}

// LatestMonitorRoundAt 返回最近一轮的结束时间(MAX(finished_at));
// 还没有任何已结束的轮次返回 (nil, nil)。
func (d *DB) LatestMonitorRoundAt() (*time.Time, error) {
	var t sql.NullTime
	if err := d.QueryRow(`SELECT MAX(finished_at) FROM recon_monitor_runs`).Scan(&t); err != nil {
		return nil, err
	}
	if !t.Valid {
		return nil, nil
	}
	return &t.Time, nil
}
