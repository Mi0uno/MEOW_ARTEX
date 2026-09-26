package db

// 自进化(经验洞察 evolve_insights + 攻击剧本 evolve_playbooks)db 层,契约
// AI_MEMORY_CONTRACTS.md §3。分级信任:复盘产出落 candidate(不注入任务);
// corroboration 达到阈值(≥2 次独立任务印证)或人工转正 → active。
// 硬约束同 memory.go:集合字段返回前归一为非 nil 空 slice;JSONB/BIGINT[]/TEXT[]
// 列 ::text 扫描 + 容错 unmarshal;raw SQL 里只允许 `--` 注释;印证写路径在单事务里。

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// EvolveInsight 是一条经验洞察(正/负经验)。
type EvolveInsight struct {
	ID            int64     `json:"id"`
	ScopeKind     string    `json:"scope_kind"`
	ScopeID       int64     `json:"scope_id"`
	StackTags     []string  `json:"stack_tags"`
	Category      string    `json:"category"`
	Kind          string    `json:"kind"` // positive|negative
	Title         string    `json:"title"`
	Content       string    `json:"content"`
	Status        string    `json:"status"`
	Corroboration int       `json:"corroboration"`
	SourceTaskIDs []int64   `json:"source_task_ids"`
	Confidence    float64   `json:"confidence"`
	CreatedBy     string    `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// EvolvePlaybookStep 是剧本的一步。
type EvolvePlaybookStep struct {
	N      int    `json:"n"`
	Action string `json:"action"`
	Detail string `json:"detail,omitempty"`
	Notes  string `json:"notes,omitempty"`
}

// EvolvePlaybook 是一个攻击剧本。
type EvolvePlaybook struct {
	ID              int64                `json:"id"`
	Title           string               `json:"title"`
	Summary         string               `json:"summary"`
	ScopeKind       string               `json:"scope_kind"`
	ScopeID         int64                `json:"scope_id"`
	StackTags       []string             `json:"stack_tags"`
	Preconditions   map[string]any       `json:"preconditions"`
	Steps           []EvolvePlaybookStep `json:"steps"`
	Status          string               `json:"status"`
	SuccessCount    int                  `json:"success_count"`
	FailCount       int                  `json:"fail_count"`
	AvgSeconds      int                  `json:"avg_seconds"`
	EvidenceTaskIDs []int64              `json:"evidence_task_ids"`
	SourceTaskIDs   []int64              `json:"source_task_ids"`
	Corroboration   int                  `json:"corroboration"`
	CreatedBy       string               `json:"created_by"`
	CreatedAt       time.Time            `json:"created_at"`
	UpdatedAt       time.Time            `json:"updated_at"`
}

// EvolveInsightFilter 是洞察列表的筛选条件。零值 = 不限。
type EvolveInsightFilter struct {
	Status    string // candidate|active|rejected|retired(空=全部)
	Kind      string // positive|negative
	Category  string
	Q         string // title/content ILIKE
	CompanyID int64  // 命中 company scope;global/src_target 行不受其过滤
	Limit     int
	Offset    int
}

// EvolvePlaybookFilter 是剧本列表的筛选条件。零值 = 不限。
type EvolvePlaybookFilter struct {
	Status    string
	Q         string // title/summary ILIKE
	CompanyID int64  // 命中 company scope;global/src_target 行不受其过滤
	Limit     int
	Offset    int
}

// decodePlaybookSteps 容错解码 JSONB steps 列。
func decodePlaybookSteps(raw string) []EvolvePlaybookStep {
	out := []EvolvePlaybookStep{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	if out == nil {
		out = []EvolvePlaybookStep{}
	}
	return out
}

// statusCounts 按 status 聚合计数,四个状态键恒在(前端页签计数依赖全键,且计数
// 不受任何分类筛选影响)。仅供 overview 类接口使用。
func (d *DB) statusCounts(query string, args ...any) (map[string]int64, error) {
	out := map[string]int64{"candidate": 0, "active": 0, "rejected": 0, "retired": 0}
	rows, err := d.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		var n int64
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, rows.Err()
}

// evolveScopeCond 拼装「scope 命中传入任一 (kind,id) 对 或 global」的条件。
func evolveScopeCond(scopes []MemoryScope, args []any) (string, []any) {
	conds := []string{"scope_kind = 'global'"}
	for _, s := range scopes {
		if s.Kind == "" || s.Kind == "global" {
			continue
		}
		conds = append(conds, fmt.Sprintf("(scope_kind = $%d AND scope_id = $%d)", len(args)+1, len(args)+2))
		args = append(args, s.Kind, s.ID)
	}
	return strings.Join(conds, " OR "), args
}

// appendTaskID 去重追加 taskID(印证/证据链不允许重复任务计数)。
func appendTaskID(ids []int64, taskID int64) []int64 {
	if taskID > 0 && !slices.Contains(ids, taskID) {
		ids = append(ids, taskID)
	}
	return ids
}

const evolveInsightCols = `id, scope_kind, scope_id, array_to_json(stack_tags)::text, category, kind,
	title, content, status, corroboration, array_to_json(source_task_ids)::text, confidence,
	created_by, created_at, updated_at`

func scanEvolveInsight(sc interface{ Scan(...any) error }) (*EvolveInsight, error) {
	var i EvolveInsight
	var tags, srcTasks string
	if err := sc.Scan(&i.ID, &i.ScopeKind, &i.ScopeID, &tags, &i.Category, &i.Kind,
		&i.Title, &i.Content, &i.Status, &i.Corroboration, &srcTasks, &i.Confidence,
		&i.CreatedBy, &i.CreatedAt, &i.UpdatedAt); err != nil {
		return nil, err
	}
	i.StackTags = decodeStrArray(tags)
	i.SourceTaskIDs = decodeInt64Array(srcTasks)
	return &i, nil
}

// CreateEvolveInsight 新建一条洞察(缺省值与 schema 默认对齐)。
func (d *DB) CreateEvolveInsight(i *EvolveInsight) (int64, error) {
	if i == nil {
		return 0, fmt.Errorf("insight 必填")
	}
	if strings.TrimSpace(i.Title) == "" {
		return 0, fmt.Errorf("title 必填")
	}
	if i.ScopeKind == "" {
		i.ScopeKind = "global"
	}
	if i.Category == "" {
		i.Category = "technique"
	}
	if i.Kind == "" {
		i.Kind = "positive"
	}
	if i.Status == "" {
		i.Status = "candidate"
	}
	if i.Corroboration == 0 {
		i.Corroboration = 1
	}
	if i.CreatedBy == "" {
		i.CreatedBy = "agent"
	}
	if i.StackTags == nil {
		i.StackTags = []string{}
	}
	if i.SourceTaskIDs == nil {
		i.SourceTaskIDs = []int64{}
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO evolve_insights(scope_kind, scope_id, stack_tags, category, kind, title, content,
	status, corroboration, source_task_ids, confidence, created_by)
VALUES ($1,$2,$3::text[],$4,$5,$6,$7,$8,$9,$10::bigint[],$11,$12)
RETURNING id`,
		i.ScopeKind, i.ScopeID, i.StackTags, i.Category, i.Kind, i.Title, i.Content,
		i.Status, i.Corroboration, i.SourceTaskIDs, i.Confidence, i.CreatedBy).Scan(&id)
	return id, err
}

// SimilarEvolveInsight 在 status IN (candidate,active) 中找 title 相似度最高且
// similarity(title,$1) >= minSim 的行(pg_trgm);没有命中返回 (nil, nil)。
func (d *DB) SimilarEvolveInsight(title string, minSim float64) (*EvolveInsight, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, nil
	}
	i, err := scanEvolveInsight(d.QueryRow(`
SELECT `+evolveInsightCols+` FROM evolve_insights
WHERE status IN ('candidate','active') AND similarity(title, $1) >= $2
ORDER BY similarity(title, $1) DESC, id DESC LIMIT 1`, title, minSim))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return i, nil
}

// CorroborateEvolveInsight 单事务印证:corroboration+1、source_task_ids 去重追加
// taskID;status='candidate' 且 corroboration 达到 autoActivateAt → status='active'
// 且 confidence=max(confidence,0.7)。返回本次调用是否触发了自动转正。行不存在返回
// (false, nil)。
func (d *DB) CorroborateEvolveInsight(id, taskID int64, autoActivateAt int) (bool, error) {
	tx, err := d.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck
	var status string
	var corr int
	var srcTasks string
	err = tx.QueryRow(`
SELECT status, corroboration, array_to_json(source_task_ids)::text
FROM evolve_insights WHERE id=$1 FOR UPDATE`, id).Scan(&status, &corr, &srcTasks)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	src := appendTaskID(decodeInt64Array(srcTasks), taskID)
	newCorr := corr + 1
	activated := status == "candidate" && newCorr >= autoActivateAt
	if activated {
		_, err = tx.Exec(`
UPDATE evolve_insights SET corroboration=$2, source_task_ids=$3::bigint[],
	status='active', confidence=GREATEST(confidence, 0.7::real) WHERE id=$1`,
			id, newCorr, src)
	} else {
		_, err = tx.Exec(`UPDATE evolve_insights SET corroboration=$2, source_task_ids=$3::bigint[] WHERE id=$1`,
			id, newCorr, src)
	}
	if err != nil {
		return false, err
	}
	return activated, tx.Commit()
}

// SetEvolveInsightStatus 人工转正/否决/退役/撤销(状态由 server 白名单校验)。
func (d *DB) SetEvolveInsightStatus(id int64, status string) error {
	_, err := d.Exec(`UPDATE evolve_insights SET status=$2 WHERE id=$1`, id, status)
	return err
}

// DeleteEvolveInsight 物理删除。
func (d *DB) DeleteEvolveInsight(id int64) error {
	_, err := d.Exec(`DELETE FROM evolve_insights WHERE id=$1`, id)
	return err
}

// ListEvolveInsights 分页检索洞察列表。total 在 WHERE 过滤之后算。
// CompanyID>0 只收窄 company 命中行,global/src_target 行照常返回(契约原话)。
func (d *DB) ListEvolveInsights(f EvolveInsightFilter) ([]*EvolveInsight, int, error) {
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
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.Kind != "" {
		add("kind = $%d", f.Kind)
	}
	if f.Category != "" {
		add("category = $%d", f.Category)
	}
	if f.Q != "" {
		where = append(where, fmt.Sprintf("(title ILIKE $%d OR content ILIKE $%d)", len(args)+1, len(args)+1))
		args = append(args, "%"+f.Q+"%")
	}
	if f.CompanyID > 0 {
		where = append(where, fmt.Sprintf(
			"((scope_kind = 'company' AND scope_id = $%d) OR scope_kind IN ('global','src_target'))", len(args)+1))
		args = append(args, f.CompanyID)
	}
	kw := strings.Join(where, " AND ")

	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM evolve_insights WHERE `+kw, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT ` + evolveInsightCols + ` FROM evolve_insights WHERE ` + kw +
		` ORDER BY id DESC` + fmt.Sprintf(` LIMIT $%d OFFSET $%d`, len(args)+1, len(args)+2)
	args = append(args, f.Limit, f.Offset)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*EvolveInsight{}
	for rows.Next() {
		i, err := scanEvolveInsight(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, i)
	}
	return out, total, rows.Err()
}

// ActiveEvolveInsights 取注入用洞察(status='active',scope 命中或 global),按 updated_at DESC。
func (d *DB) ActiveEvolveInsights(scopes []MemoryScope, limit int) ([]*EvolveInsight, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	cond, args := evolveScopeCond(scopes, nil)
	q := `SELECT ` + evolveInsightCols + ` FROM evolve_insights
WHERE status = 'active' AND (` + cond + `)
ORDER BY updated_at DESC, id DESC` + fmt.Sprintf(` LIMIT $%d`, len(args)+1)
	args = append(args, limit)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*EvolveInsight{}
	for rows.Next() {
		i, err := scanEvolveInsight(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

const evolvePlaybookCols = `id, title, summary, scope_kind, scope_id, array_to_json(stack_tags)::text,
	preconditions::text, steps::text, status, success_count, fail_count, avg_seconds,
	array_to_json(evidence_task_ids)::text, array_to_json(source_task_ids)::text,
	corroboration, created_by, created_at, updated_at`

func scanEvolvePlaybook(sc interface{ Scan(...any) error }) (*EvolvePlaybook, error) {
	var p EvolvePlaybook
	var tags, pre, steps, evidence, srcTasks string
	if err := sc.Scan(&p.ID, &p.Title, &p.Summary, &p.ScopeKind, &p.ScopeID, &tags,
		&pre, &steps, &p.Status, &p.SuccessCount, &p.FailCount, &p.AvgSeconds,
		&evidence, &srcTasks, &p.Corroboration, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.StackTags = decodeStrArray(tags)
	p.Preconditions = decodeJSONMap(pre)
	p.Steps = decodePlaybookSteps(steps)
	p.EvidenceTaskIDs = decodeInt64Array(evidence)
	p.SourceTaskIDs = decodeInt64Array(srcTasks)
	return &p, nil
}

// CreateEvolvePlaybook 新建一个剧本(缺省值与 schema 默认对齐)。
func (d *DB) CreateEvolvePlaybook(p *EvolvePlaybook) (int64, error) {
	if p == nil {
		return 0, fmt.Errorf("playbook 必填")
	}
	if strings.TrimSpace(p.Title) == "" {
		return 0, fmt.Errorf("title 必填")
	}
	if p.ScopeKind == "" {
		p.ScopeKind = "global"
	}
	if p.Status == "" {
		p.Status = "candidate"
	}
	if p.Corroboration == 0 {
		p.Corroboration = 1
	}
	if p.CreatedBy == "" {
		p.CreatedBy = "agent"
	}
	if p.StackTags == nil {
		p.StackTags = []string{}
	}
	if p.Preconditions == nil {
		p.Preconditions = map[string]any{}
	}
	if p.Steps == nil {
		p.Steps = []EvolvePlaybookStep{}
	}
	if p.EvidenceTaskIDs == nil {
		p.EvidenceTaskIDs = []int64{}
	}
	if p.SourceTaskIDs == nil {
		p.SourceTaskIDs = []int64{}
	}
	pre, _ := json.Marshal(p.Preconditions)
	steps, _ := json.Marshal(p.Steps)
	var id int64
	err := d.QueryRow(`
INSERT INTO evolve_playbooks(title, summary, scope_kind, scope_id, stack_tags, preconditions, steps,
	status, success_count, fail_count, avg_seconds, evidence_task_ids, source_task_ids,
	corroboration, created_by)
VALUES ($1,$2,$3,$4,$5::text[],$6,$7,$8,$9,$10,$11,$12::bigint[],$13::bigint[],$14,$15)
RETURNING id`,
		p.Title, p.Summary, p.ScopeKind, p.ScopeID, p.StackTags, pre, steps,
		p.Status, p.SuccessCount, p.FailCount, p.AvgSeconds, p.EvidenceTaskIDs, p.SourceTaskIDs,
		p.Corroboration, p.CreatedBy).Scan(&id)
	return id, err
}

// SimilarEvolvePlaybook 在 status IN (candidate,active) 中找 title 相似度最高且
// similarity(title,$1) >= minSim 的行(pg_trgm);没有命中返回 (nil, nil)。
func (d *DB) SimilarEvolvePlaybook(title string, minSim float64) (*EvolvePlaybook, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, nil
	}
	p, err := scanEvolvePlaybook(d.QueryRow(`
SELECT `+evolvePlaybookCols+` FROM evolve_playbooks
WHERE status IN ('candidate','active') AND similarity(title, $1) >= $2
ORDER BY similarity(title, $1) DESC, id DESC LIMIT 1`, title, minSim))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// CorroborateEvolvePlaybook 单事务印证,语义与 CorroborateEvolveInsight 完全一致。
// 返回本次调用是否触发了自动转正;行不存在返回 (false, nil)。
func (d *DB) CorroborateEvolvePlaybook(id, taskID int64, autoActivateAt int) (bool, error) {
	tx, err := d.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck
	var status string
	var corr int
	var srcTasks string
	err = tx.QueryRow(`
SELECT status, corroboration, array_to_json(source_task_ids)::text
FROM evolve_playbooks WHERE id=$1 FOR UPDATE`, id).Scan(&status, &corr, &srcTasks)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	src := appendTaskID(decodeInt64Array(srcTasks), taskID)
	newCorr := corr + 1
	activated := status == "candidate" && newCorr >= autoActivateAt
	if activated {
		// 剧本表没有 confidence 列(契约 EvolvePlaybook 结构体同样没有该字段),
		// 自动转正只切状态,与洞察的 confidence=max(0.7) 语义区分开。
		_, err = tx.Exec(`UPDATE evolve_playbooks SET corroboration=$2, source_task_ids=$3::bigint[], status='active' WHERE id=$1`,
			id, newCorr, src)
	} else {
		_, err = tx.Exec(`UPDATE evolve_playbooks SET corroboration=$2, source_task_ids=$3::bigint[] WHERE id=$1`,
			id, newCorr, src)
	}
	if err != nil {
		return false, err
	}
	return activated, tx.Commit()
}

// RecordEvolvePlaybookOutcome 记录一次剧本实测结果:success→success_count+1 否则
// fail_count+1;taskID 去重追加进 evidence_task_ids 与 source_task_ids;avg_seconds
// 增量平均。行不存在视为无操作(server 侧先判存在)。
func (d *DB) RecordEvolvePlaybookOutcome(id, taskID int64, success bool, seconds int) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	var sc, fc, avg int
	var evidence, srcTasks string
	err = tx.QueryRow(`
SELECT success_count, fail_count, avg_seconds,
	array_to_json(evidence_task_ids)::text, array_to_json(source_task_ids)::text
FROM evolve_playbooks WHERE id=$1 FOR UPDATE`, id).Scan(&sc, &fc, &avg, &evidence, &srcTasks)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	ev := appendTaskID(decodeInt64Array(evidence), taskID)
	src := appendTaskID(decodeInt64Array(srcTasks), taskID)
	newSC, newFC := sc, fc
	if success {
		newSC++
	} else {
		newFC++
	}
	// avg_seconds 增量平均(只统计 seconds>0 的样本)。schema 没有样本数列,取
	// (success_count+fail_count) 自增前的值近似样本数:avg<=0(还没有有效样本,或
	// 建行带了种子 avg 但没有 outcome 记录)时直接取本次值;否则按
	// (avg*样本数+本次值)/(样本数+1) 递推。选择最简单且单调、永不除零的方案,
	// 对「只看量级」的展示场景足够精确。
	newAvg := avg
	if seconds > 0 {
		samples := sc + fc
		if avg <= 0 || samples <= 0 {
			newAvg = seconds
		} else {
			newAvg = (avg*samples + seconds) / (samples + 1)
		}
	}
	_, err = tx.Exec(`
UPDATE evolve_playbooks SET success_count=$2, fail_count=$3, avg_seconds=$4,
	evidence_task_ids=$5::bigint[], source_task_ids=$6::bigint[] WHERE id=$1`,
		id, newSC, newFC, newAvg, ev, src)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// SetEvolvePlaybookStatus 人工转正/否决/退役/撤销。
func (d *DB) SetEvolvePlaybookStatus(id int64, status string) error {
	_, err := d.Exec(`UPDATE evolve_playbooks SET status=$2 WHERE id=$1`, id, status)
	return err
}

// DeleteEvolvePlaybook 物理删除。
func (d *DB) DeleteEvolvePlaybook(id int64) error {
	_, err := d.Exec(`DELETE FROM evolve_playbooks WHERE id=$1`, id)
	return err
}

// ListEvolvePlaybooks 分页检索剧本列表。total 在 WHERE 过滤之后算。
// CompanyID>0 只收窄 company 命中行,global/src_target 行照常返回(契约原话)。
func (d *DB) ListEvolvePlaybooks(f EvolvePlaybookFilter) ([]*EvolvePlaybook, int, error) {
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
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.Q != "" {
		where = append(where, fmt.Sprintf("(title ILIKE $%d OR summary ILIKE $%d)", len(args)+1, len(args)+1))
		args = append(args, "%"+f.Q+"%")
	}
	if f.CompanyID > 0 {
		where = append(where, fmt.Sprintf(
			"((scope_kind = 'company' AND scope_id = $%d) OR scope_kind IN ('global','src_target'))", len(args)+1))
		args = append(args, f.CompanyID)
	}
	kw := strings.Join(where, " AND ")

	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM evolve_playbooks WHERE `+kw, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT ` + evolvePlaybookCols + ` FROM evolve_playbooks WHERE ` + kw +
		` ORDER BY id DESC` + fmt.Sprintf(` LIMIT $%d OFFSET $%d`, len(args)+1, len(args)+2)
	args = append(args, f.Limit, f.Offset)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*EvolvePlaybook{}
	for rows.Next() {
		p, err := scanEvolvePlaybook(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

// ActiveEvolvePlaybooks 取注入用剧本(status='active',scope 命中或 global),按 updated_at DESC。
func (d *DB) ActiveEvolvePlaybooks(scopes []MemoryScope, limit int) ([]*EvolvePlaybook, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	cond, args := evolveScopeCond(scopes, nil)
	q := `SELECT ` + evolvePlaybookCols + ` FROM evolve_playbooks
WHERE status = 'active' AND (` + cond + `)
ORDER BY updated_at DESC, id DESC` + fmt.Sprintf(` LIMIT $%d`, len(args)+1)
	args = append(args, limit)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*EvolvePlaybook{}
	for rows.Next() {
		p, err := scanEvolvePlaybook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// EvolveOverview 返回洞察与剧本的按 status 计数(四个状态键恒在,不受分类筛选影响)。
func (d *DB) EvolveOverview() (map[string]int64, map[string]int64, error) {
	insights, err := d.statusCounts(`SELECT status, COUNT(*) FROM evolve_insights GROUP BY status`)
	if err != nil {
		return nil, nil, err
	}
	playbooks, err := d.statusCounts(`SELECT status, COUNT(*) FROM evolve_playbooks GROUP BY status`)
	if err != nil {
		return nil, nil, err
	}
	return insights, playbooks, nil
}
