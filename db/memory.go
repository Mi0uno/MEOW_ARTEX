package db

// AI 记忆库 db 层(契约 AI_MEMORY_CONTRACTS.md §3):情节记忆 memory_runs(每任务一份
// 复盘,task_id 幂等 upsert) + 语义记忆 memory_facts(双时间戳失效制:新事实使旧事实
// valid_to=now(),不物理覆盖)。本文件只实现 §3 签名,复盘/注入语义在 server 层。
// 硬约束:所有集合字段返回前必须初始化为非 nil 空 slice(Go nil → JSON null 会打崩
// 前端);JSONB/BIGINT[] 列一律 ::text 扫描 + 容错 unmarshal;raw SQL 字符串里只允许
// `--` 注释(曾把 `//` 写进 SQL 导致 42601 全链路静默失败)。

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// MemoryScope 是一次记忆读取的作用域命中项。Kind: company | src_target | global。
type MemoryScope struct {
	Kind string
	ID   int64
}

// MemoryRunFailure 是复盘里的一条失败教训。
type MemoryRunFailure struct {
	What      string `json:"what"`
	RootCause string `json:"root_cause"`
	CostNote  string `json:"cost_note,omitempty"`
}

// MemoryRun 是一次任务复盘(memory_runs 行,与 task 1:1)。
type MemoryRun struct {
	ID             int64              `json:"id"`
	TaskID         int64              `json:"task_id"`
	Stage          string             `json:"stage"`
	ScopeCompanies []int64            `json:"scope_companies"`
	ScopeSrcTarget int64              `json:"scope_src_target_id"`
	Status         string             `json:"status"` // pending|done|failed|skipped
	Source         string             `json:"source"` // ai|backfill|manual
	Summary        string             `json:"summary"`
	Highlights     []string           `json:"highlights"`
	Failures       []MemoryRunFailure `json:"failures"`
	OpenLeads      []string           `json:"open_leads"`
	Stats          map[string]any     `json:"stats"`
	ReflectError   string             `json:"reflect_error"`
	CreatedAt      time.Time          `json:"created_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
}

// MemoryFact 是一条语义记忆事实。valid_to 非空 = 已被取代/已失效。
type MemoryFact struct {
	ID           int64      `json:"id"`
	ScopeKind    string     `json:"scope_kind"`
	ScopeID      int64      `json:"scope_id"`
	AssetID      int64      `json:"asset_id"`
	Kind         string     `json:"kind"`
	Subject      string     `json:"subject"`
	Content      string     `json:"content"`
	SourceTaskID int64      `json:"source_task_id"`
	SourceKind   string     `json:"source_kind"`
	Confidence   float64    `json:"confidence"`
	ObservedAt   time.Time  `json:"observed_at"`
	ValidFrom    time.Time  `json:"valid_from"`
	ValidTo      *time.Time `json:"valid_to"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// MemoryRunFilter 是复盘列表的筛选条件。零值 = 不限。
type MemoryRunFilter struct {
	CompanyID int64 // 命中 scope_companies(0=不限)
	TaskID    int64
	Status    string // pending|done|failed|skipped(空=不限)
	Limit     int
	Offset    int
}

// MemoryFactFilter 是事实列表的筛选条件。零值 = 不限。
type MemoryFactFilter struct {
	CompanyID      int64 // scope_kind='company' AND scope_id=CompanyID(0=不限)
	ScopeKind      string
	Kind           string
	Q              string // content ILIKE
	IncludeInvalid bool   // true = 含已失效(valid_to 非空)行
	Limit          int
	Offset         int
}

// MemoryCompanyStat 是企业维度的记忆统计(无记忆的企业 runs=0 也返回)。
type MemoryCompanyStat struct {
	CompanyID   int64      `json:"company_id"`
	CompanyName string     `json:"company_name"`
	Runs        int64      `json:"runs"`
	FactsActive int64      `json:"facts_active"`
	LastRunAt   *time.Time `json:"last_run_at"`
}

// MemoryOverviewCounts 是记忆库概览计数。insights/playbooks 按 status 计数,
// 四个状态键恒在(前端页签计数依赖全键)。
type MemoryOverviewCounts struct {
	FactsTotal  int64              `json:"facts_total"`
	FactsActive int64              `json:"facts_active"`
	RunsTotal   int64              `json:"runs_total"`
	Insights    map[string]int64   `json:"insights"`
	Playbooks   map[string]int64   `json:"playbooks"`
}

// decodeStrArray 容错解码 JSON 字符串数组列:空串/坏 JSON/null 一律回非 nil 空 slice。
func decodeStrArray(raw string) []string {
	out := []string{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	if out == nil {
		out = []string{}
	}
	return out
}

// decodeInt64Array 容错解码 JSON int64 数组列(array_to_json(BIGINT[])::text)。
func decodeInt64Array(raw string) []int64 {
	out := []int64{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	if out == nil {
		out = []int64{}
	}
	return out
}

// decodeRunFailures 容错解码 memory_runs.failures。
func decodeRunFailures(raw string) []MemoryRunFailure {
	out := []MemoryRunFailure{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	if out == nil {
		out = []MemoryRunFailure{}
	}
	return out
}

// decodeJSONMap 容错解码 JSONB 对象列:空/坏 JSON/null 一律回非 nil 空 map。
func decodeJSONMap(raw string) map[string]any {
	out := map[string]any{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	if out == nil {
		out = map[string]any{}
	}
	return out
}

const memoryRunCols = `id, task_id, stage, array_to_json(scope_companies)::text, scope_src_target_id,
	status, source, summary, highlights::text, failures::text, open_leads::text, stats::text,
	reflect_error, created_at, updated_at`

func scanMemoryRun(sc interface{ Scan(...any) error }) (*MemoryRun, error) {
	var r MemoryRun
	var companies, highlights, failures, openLeads, stats string
	if err := sc.Scan(&r.ID, &r.TaskID, &r.Stage, &companies, &r.ScopeSrcTarget, &r.Status, &r.Source,
		&r.Summary, &highlights, &failures, &openLeads, &stats, &r.ReflectError, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	r.ScopeCompanies = decodeInt64Array(companies)
	r.Highlights = decodeStrArray(highlights)
	r.Failures = decodeRunFailures(failures)
	r.OpenLeads = decodeStrArray(openLeads)
	r.Stats = decodeJSONMap(stats)
	return &r, nil
}

// UpsertMemoryRun 按 task_id 幂等写入复盘:已存在则全字段覆盖(re-reflect 复用同一行,
// 幂等键 = uq_memory_runs_task)。updated_at 由触发器刷新。
func (d *DB) UpsertMemoryRun(r *MemoryRun) (int64, error) {
	if r == nil {
		return 0, fmt.Errorf("run 必填")
	}
	if r.TaskID <= 0 {
		return 0, fmt.Errorf("task_id 必填")
	}
	// 集合字段先归一为非 nil 空容器(nil 编码成 JSON null 会打崩前端)。
	if r.Status == "" {
		r.Status = "pending"
	}
	if r.Source == "" {
		r.Source = "ai"
	}
	if r.ScopeCompanies == nil {
		r.ScopeCompanies = []int64{}
	}
	if r.Highlights == nil {
		r.Highlights = []string{}
	}
	if r.Failures == nil {
		r.Failures = []MemoryRunFailure{}
	}
	if r.OpenLeads == nil {
		r.OpenLeads = []string{}
	}
	if r.Stats == nil {
		r.Stats = map[string]any{}
	}
	highlights, _ := json.Marshal(r.Highlights)
	failures, _ := json.Marshal(r.Failures)
	openLeads, _ := json.Marshal(r.OpenLeads)
	stats, _ := json.Marshal(r.Stats)
	var id int64
	err := d.QueryRow(`
INSERT INTO memory_runs(task_id, stage, scope_companies, scope_src_target_id, status, source,
	summary, highlights, failures, open_leads, stats, reflect_error)
VALUES ($1,$2,$3::bigint[],$4,$5,$6,$7,$8,$9,$10,$11,$12)
ON CONFLICT (task_id) DO UPDATE SET
	stage = EXCLUDED.stage,
	scope_companies = EXCLUDED.scope_companies,
	scope_src_target_id = EXCLUDED.scope_src_target_id,
	status = EXCLUDED.status,
	source = EXCLUDED.source,
	summary = EXCLUDED.summary,
	highlights = EXCLUDED.highlights,
	failures = EXCLUDED.failures,
	open_leads = EXCLUDED.open_leads,
	stats = EXCLUDED.stats,
	reflect_error = EXCLUDED.reflect_error
RETURNING id`,
		r.TaskID, r.Stage, r.ScopeCompanies, r.ScopeSrcTarget, r.Status, r.Source,
		r.Summary, highlights, failures, openLeads, stats, r.ReflectError).Scan(&id)
	return id, err
}

// SetMemoryRunStatus 只改状态与复盘错误文案(re-reflect 置 pending / 终态置 done|failed)。
func (d *DB) SetMemoryRunStatus(id int64, status, reflectErr string) error {
	_, err := d.Exec(`UPDATE memory_runs SET status=$2, reflect_error=$3 WHERE id=$1`, id, status, reflectErr)
	return err
}

func (d *DB) memoryRunBy(where string, args ...any) (*MemoryRun, error) {
	r, err := scanMemoryRun(d.QueryRow(`SELECT `+memoryRunCols+` FROM memory_runs WHERE `+where, args...))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// MemoryRunByTask 按 task_id 取复盘行,无则 (nil, nil)。
func (d *DB) MemoryRunByTask(taskID int64) (*MemoryRun, error) {
	return d.memoryRunBy("task_id=$1", taskID)
}

// MemoryRun 按主键取复盘行,无则 (nil, nil)。
func (d *DB) MemoryRun(id int64) (*MemoryRun, error) {
	return d.memoryRunBy("id=$1", id)
}

// ListMemoryRuns 分页检索复盘列表。total 必须在 WHERE 过滤之后算(报表中心教训)。
func (d *DB) ListMemoryRuns(f MemoryRunFilter) ([]*MemoryRun, int, error) {
	if f.Limit <= 0 || f.Limit > 200 {
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
	if f.CompanyID > 0 {
		add("scope_companies @> ARRAY[$%d::bigint]", f.CompanyID)
	}
	if f.TaskID > 0 {
		add("task_id = $%d", f.TaskID)
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	kw := strings.Join(where, " AND ")

	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM memory_runs WHERE `+kw, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT ` + memoryRunCols + ` FROM memory_runs WHERE ` + kw +
		` ORDER BY id DESC` + fmt.Sprintf(` LIMIT $%d OFFSET $%d`, len(args)+1, len(args)+2)
	args = append(args, f.Limit, f.Offset)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*MemoryRun{}
	for rows.Next() {
		r, err := scanMemoryRun(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

const memoryFactCols = `id, scope_kind, scope_id, asset_id, kind, subject, content, source_task_id,
	source_kind, confidence, observed_at, valid_from, valid_to, created_at, updated_at`

func scanMemoryFact(sc interface{ Scan(...any) error }) (*MemoryFact, error) {
	var f MemoryFact
	var srcTask sql.NullInt64
	var validTo sql.NullTime
	if err := sc.Scan(&f.ID, &f.ScopeKind, &f.ScopeID, &f.AssetID, &f.Kind, &f.Subject, &f.Content,
		&srcTask, &f.SourceKind, &f.Confidence, &f.ObservedAt, &f.ValidFrom, &validTo, &f.CreatedAt, &f.UpdatedAt); err != nil {
		return nil, err
	}
	if srcTask.Valid {
		f.SourceTaskID = srcTask.Int64
	}
	if validTo.Valid {
		t := validTo.Time
		f.ValidTo = &t
	}
	return &f, nil
}

// CreateMemoryFact 新增一条事实(source_task_id=0 存 NULL;缺省值与 schema 默认对齐)。
func (d *DB) CreateMemoryFact(f *MemoryFact) (int64, error) {
	if f == nil {
		return 0, fmt.Errorf("fact 必填")
	}
	if strings.TrimSpace(f.Content) == "" {
		return 0, fmt.Errorf("content 必填")
	}
	if f.ScopeKind == "" {
		f.ScopeKind = "company"
	}
	if f.Kind == "" {
		f.Kind = "other"
	}
	if f.SourceKind == "" {
		f.SourceKind = "ai"
	}
	if f.ObservedAt.IsZero() {
		f.ObservedAt = time.Now()
	}
	if f.ValidFrom.IsZero() {
		f.ValidFrom = f.ObservedAt
	}
	var srcTask any
	if f.SourceTaskID > 0 {
		srcTask = f.SourceTaskID
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO memory_facts(scope_kind, scope_id, asset_id, kind, subject, content,
	source_task_id, source_kind, confidence, observed_at, valid_from)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
RETURNING id`,
		f.ScopeKind, f.ScopeID, f.AssetID, f.Kind, f.Subject, f.Content,
		srcTask, f.SourceKind, f.Confidence, f.ObservedAt, f.ValidFrom).Scan(&id)
	return id, err
}

// MemoryFact 按主键取事实,无则 (nil, nil)。
func (d *DB) MemoryFact(id int64) (*MemoryFact, error) {
	f, err := scanMemoryFact(d.QueryRow(`SELECT `+memoryFactCols+` FROM memory_facts WHERE id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return f, nil
}

// InvalidateMemoryFact 失效制:置 valid_to=now(),绝不物理覆盖(漏洞会修、资产会变)。
func (d *DB) InvalidateMemoryFact(id int64) error {
	_, err := d.Exec(`UPDATE memory_facts SET valid_to=now() WHERE id=$1`, id)
	return err
}

// DeleteMemoryFact 物理删除(人工清理用)。
func (d *DB) DeleteMemoryFact(id int64) error {
	_, err := d.Exec(`DELETE FROM memory_facts WHERE id=$1`, id)
	return err
}

// ListMemoryFacts 分页检索事实列表。total 在 WHERE 过滤之后算。
func (d *DB) ListMemoryFacts(f MemoryFactFilter) ([]*MemoryFact, int, error) {
	if f.Limit <= 0 || f.Limit > 200 {
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
	if f.CompanyID > 0 {
		add("scope_kind = 'company' AND scope_id = $%d", f.CompanyID)
	}
	if f.ScopeKind != "" {
		add("scope_kind = $%d", f.ScopeKind)
	}
	if f.Kind != "" {
		add("kind = $%d", f.Kind)
	}
	if f.Q != "" {
		add("content ILIKE $%d", "%"+f.Q+"%")
	}
	if !f.IncludeInvalid {
		where = append(where, "valid_to IS NULL")
	}
	kw := strings.Join(where, " AND ")

	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM memory_facts WHERE `+kw, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT ` + memoryFactCols + ` FROM memory_facts WHERE ` + kw +
		` ORDER BY updated_at DESC, id DESC` + fmt.Sprintf(` LIMIT $%d OFFSET $%d`, len(args)+1, len(args)+2)
	args = append(args, f.Limit, f.Offset)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*MemoryFact{}
	for rows.Next() {
		f, err := scanMemoryFact(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, f)
	}
	return out, total, rows.Err()
}

// ActiveMemoryFacts 取注入用有效事实:valid_to IS NULL 且 (scope 命中传入任一
// (kind,id) 对 或 scope_kind='global'),按 updated_at DESC。
func (d *DB) ActiveMemoryFacts(scopes []MemoryScope, limit int) ([]*MemoryFact, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	args := []any{}
	conds := []string{"scope_kind = 'global'"}
	for _, s := range scopes {
		if s.Kind == "" || s.Kind == "global" {
			continue
		}
		conds = append(conds, fmt.Sprintf("(scope_kind = $%d AND scope_id = $%d)", len(args)+1, len(args)+2))
		args = append(args, s.Kind, s.ID)
	}
	q := `SELECT ` + memoryFactCols + ` FROM memory_facts
WHERE valid_to IS NULL AND (` + strings.Join(conds, " OR ") + `)
ORDER BY updated_at DESC, id DESC` + fmt.Sprintf(` LIMIT $%d`, len(args)+1)
	args = append(args, limit)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*MemoryFact{}
	for rows.Next() {
		f, err := scanMemoryFact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// MemoryCompanyStats 全部企业( companies 表)左连记忆统计;无记忆的企业 runs=0 也返回。
// 封 500 行,runs 降序、同名按 id 稳定排序。
func (d *DB) MemoryCompanyStats() ([]MemoryCompanyStat, error) {
	rows, err := d.Query(`
SELECT c.id, c.name, COALESCE(r.runs, 0), COALESCE(f.facts_active, 0), r.last_run_at
FROM companies c
LEFT JOIN LATERAL (
	SELECT COUNT(*) AS runs, MAX(updated_at) AS last_run_at
	FROM memory_runs mr WHERE c.id = ANY(mr.scope_companies)
) r ON true
LEFT JOIN LATERAL (
	SELECT COUNT(*) AS facts_active FROM memory_facts mf
	WHERE mf.scope_kind = 'company' AND mf.scope_id = c.id AND mf.valid_to IS NULL
) f ON true
ORDER BY COALESCE(r.runs, 0) DESC, c.name, c.id
LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MemoryCompanyStat{}
	for rows.Next() {
		var s MemoryCompanyStat
		var lastRun sql.NullTime
		if err := rows.Scan(&s.CompanyID, &s.CompanyName, &s.Runs, &s.FactsActive, &lastRun); err != nil {
			return nil, err
		}
		if lastRun.Valid {
			t := lastRun.Time
			s.LastRunAt = &t
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// MemoryOverview 记忆库概览计数。companyID=0 不限;>0 时 facts 按 company scope、
// runs 按 scope_companies 命中统计。insights/playbooks 四个状态键恒在。
func (d *DB) MemoryOverview(companyID int64) (MemoryOverviewCounts, error) {
	out := MemoryOverviewCounts{
		Insights:  map[string]int64{"candidate": 0, "active": 0, "rejected": 0, "retired": 0},
		Playbooks: map[string]int64{"candidate": 0, "active": 0, "rejected": 0, "retired": 0},
	}
	factWhere := "1=1"
	runWhere := "1=1"
	var args []any
	if companyID > 0 {
		factWhere = "scope_kind = 'company' AND scope_id = $1"
		runWhere = "$1::bigint = ANY(scope_companies)"
		args = append(args, companyID)
	}
	if err := d.QueryRow(`SELECT
		COUNT(*), COUNT(*) FILTER (WHERE valid_to IS NULL)
		FROM memory_facts WHERE `+factWhere, args...).Scan(&out.FactsTotal, &out.FactsActive); err != nil {
		return out, err
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM memory_runs WHERE `+runWhere, args...).Scan(&out.RunsTotal); err != nil {
		return out, err
	}
	insights, err := d.statusCounts(`SELECT status, COUNT(*) FROM evolve_insights GROUP BY status`)
	if err != nil {
		return out, err
	}
	out.Insights = insights
	playbooks, err := d.statusCounts(`SELECT status, COUNT(*) FROM evolve_playbooks GROUP BY status`)
	if err != nil {
		return out, err
	}
	out.Playbooks = playbooks
	return out, nil
}
