package db

// SRC 模式:企业 SRC 规范(src_profiles) + 成果产出(src_rewards)。
//
// 规范的作用是把「企业/平台的 SRC 授权边界」变成注入 Agent 的硬约束,
// 防止 AI 越界(打到非授权域名、用高危 payload、超速率)。
// 成果页跟踪每条产出从待提交到平台反馈的全过程。

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// SRCProfile 是一个企业的 SRC 规范。
type SRCProfile struct {
	ID           int64     `json:"id"`
	CompanyID    int64     `json:"company_id"`
	CompanyName  string    `json:"company_name,omitempty"`
	Name         string    `json:"name"`
	Platform     string    `json:"platform"`
	ScopeNote    string    `json:"scope_note"`
	Forbidden    string    `json:"forbidden"`
	AllowedTypes string    `json:"allowed_types"`
	RateLimit    string    `json:"rate_limit"`
	Rules        string    `json:"rules"`
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

const srcProfileCols = `p.id, p.company_id, COALESCE(c.name,''), p.name, p.platform, p.scope_note,
	p.forbidden, p.allowed_types, p.rate_limit, p.rules, p.enabled, p.created_at, p.updated_at`

func scanSRCProfile(sc interface{ Scan(...any) error }) (*SRCProfile, error) {
	var p SRCProfile
	if err := sc.Scan(&p.ID, &p.CompanyID, &p.CompanyName, &p.Name, &p.Platform, &p.ScopeNote,
		&p.Forbidden, &p.AllowedTypes, &p.RateLimit, &p.Rules, &p.Enabled, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	return &p, nil
}

// ListSRCProfiles 列出全部 SRC 规范(可按企业筛选;q 搜企业名/平台/规范名)。
func (d *DB) ListSRCProfiles(companyID int64, q string, limit, offset int) ([]*SRCProfile, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	where := []string{"1=1"}
	args := []any{}
	if companyID > 0 {
		where = append(where, fmt.Sprintf("p.company_id=$%d", len(args)+1))
		args = append(args, companyID)
	}
	if strings.TrimSpace(q) != "" {
		where = append(where, fmt.Sprintf("(c.name ILIKE $%d OR p.platform ILIKE $%d OR p.name ILIKE $%d)",
			len(args)+1, len(args)+1, len(args)+1))
		args = append(args, "%"+strings.TrimSpace(q)+"%")
	}
	kw := strings.Join(where, " AND ")
	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM src_profiles p LEFT JOIN companies c ON c.id=p.company_id WHERE `+kw, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q2 := `SELECT ` + srcProfileCols + ` FROM src_profiles p LEFT JOIN companies c ON c.id=p.company_id WHERE ` + kw +
		fmt.Sprintf(` ORDER BY c.name, p.id LIMIT $%d OFFSET $%d`, len(args)+1, len(args)+2)
	args = append(args, limit, offset)
	rows, err := d.Query(q2, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*SRCProfile{}
	for rows.Next() {
		p, err := scanSRCProfile(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

// GetSRCProfile 按 id 取规范;不存在返回 nil。
func (d *DB) GetSRCProfile(id int64) (*SRCProfile, error) {
	p, err := scanSRCProfile(d.QueryRow(`SELECT `+srcProfileCols+` FROM src_profiles p
		LEFT JOIN companies c ON c.id=p.company_id WHERE p.id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// GetSRCProfileByCompany 按企业取规范(SRC 任务创建时自动关联)。
func (d *DB) GetSRCProfileByCompany(companyID int64) (*SRCProfile, error) {
	p, err := scanSRCProfile(d.QueryRow(`SELECT `+srcProfileCols+` FROM src_profiles p
		LEFT JOIN companies c ON c.id=p.company_id WHERE p.company_id=$1 AND p.enabled`, companyID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// SRCProfileInput 是新建/更新规范的输入。
type SRCProfileInput struct {
	CompanyID    int64
	Name         string
	Platform     string
	ScopeNote    string
	Forbidden    string
	AllowedTypes string
	RateLimit    string
	Rules        string
	Enabled      bool
}

// UpsertSRCProfile 按企业 upsert(一个企业一套规范)。
func (d *DB) UpsertSRCProfile(in SRCProfileInput) (*SRCProfile, error) {
	if in.CompanyID <= 0 {
		return nil, fmt.Errorf("必须选择企业")
	}
	var id int64
	if err := d.QueryRow(`
INSERT INTO src_profiles(company_id, name, platform, scope_note, forbidden, allowed_types, rate_limit, rules, enabled)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT (company_id) DO UPDATE SET
	name=EXCLUDED.name, platform=EXCLUDED.platform, scope_note=EXCLUDED.scope_note,
	forbidden=EXCLUDED.forbidden, allowed_types=EXCLUDED.allowed_types,
	rate_limit=EXCLUDED.rate_limit, rules=EXCLUDED.rules, enabled=EXCLUDED.enabled, updated_at=now()
RETURNING id`,
		in.CompanyID, in.Name, in.Platform, in.ScopeNote, in.Forbidden, in.AllowedTypes,
		in.RateLimit, in.Rules, in.Enabled).Scan(&id); err != nil {
		return nil, err
	}
	return d.GetSRCProfile(id)
}

// DeleteSRCProfile 删除规范;返回是否删除。
func (d *DB) DeleteSRCProfile(id int64) (bool, error) {
	res, err := d.Exec(`DELETE FROM src_profiles WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SRCConstraintText 把规范渲染成注入 Agent 的约束文本。
// 返回空串表示没有可用规范(调用方跳过注入)。
func (p *SRCProfile) SRCConstraintText() string {
	if p == nil || !p.Enabled {
		return ""
	}
	var b strings.Builder
	b.WriteString("【SRC 授权边界(最高优先级,不得违反)】\n")
	if p.Platform != "" {
		fmt.Fprintf(&b, "- 提交平台:%s\n", p.Platform)
	}
	if p.ScopeNote != "" {
		fmt.Fprintf(&b, "- 授权范围:%s\n", p.ScopeNote)
	}
	if p.AllowedTypes != "" {
		fmt.Fprintf(&b, "- 允许的测试类型:%s\n", p.AllowedTypes)
	}
	if p.RateLimit != "" {
		fmt.Fprintf(&b, "- 速率/强度限制:%s\n", p.RateLimit)
	}
	if strings.TrimSpace(p.Forbidden) != "" {
		b.WriteString("- 严格禁止(违反即越界,任何理由都不例外):\n")
		for _, line := range strings.Split(p.Forbidden, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			fmt.Fprintf(&b, "  · %s\n", strings.TrimPrefix(line, "- "))
		}
	}
	if strings.TrimSpace(p.Rules) != "" {
		b.WriteString("- 补充规范:\n")
		b.WriteString(p.Rules)
		b.WriteString("\n")
	}
	b.WriteString("超出上述范围的任何动作一律不做;不确定是否越界时,选择不动作并说明。")
	return b.String()
}

// ---- SRC 成果 ----

// SRCReward 是一条 SRC 成果(可提交的漏洞产出)。
type SRCReward struct {
	ID          int64      `json:"id"`
	CompanyID   *int64     `json:"company_id,omitempty"`
	CompanyName string     `json:"company_name,omitempty"`
	TaskID      *int64     `json:"task_id,omitempty"`
	FindingID   *int64     `json:"finding_id,omitempty"`
	EntryID     *int64     `json:"entry_id,omitempty"`
	Title       string     `json:"title"`
	Severity    string     `json:"severity"`
	VulnClass   string     `json:"vuln_class"`
	Status      string     `json:"status"`
	PlatformRef string     `json:"platform_ref"`
	Feedback    string     `json:"feedback"`
	SubmittedAt *time.Time `json:"submitted_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

const srcRewardCols = `r.id, r.company_id, COALESCE(c.name,''), r.task_id, r.finding_id, r.entry_id,
	r.title, r.severity, r.vuln_class, r.status, r.platform_ref, r.feedback, r.submitted_at, r.created_at, r.updated_at`

func scanSRCReward(sc interface{ Scan(...any) error }) (*SRCReward, error) {
	var r SRCReward
	var submitted sql.NullTime
	if err := sc.Scan(&r.ID, &r.CompanyID, &r.CompanyName, &r.TaskID, &r.FindingID, &r.EntryID,
		&r.Title, &r.Severity, &r.VulnClass, &r.Status, &r.PlatformRef, &r.Feedback,
		&submitted, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	if submitted.Valid {
		t := submitted.Time
		r.SubmittedAt = &t
	}
	return &r, nil
}

// SRCRewardFilter 是成果列表的筛选条件。
type SRCRewardFilter struct {
	CompanyID int64
	TaskID    int64
	Status    string // pending | submitted | accepted | rejected | duplicate
	Severity  string
	Q         string // 标题/漏洞类别关键字
	Sort      string // created | updated | severity
	Desc      bool
	Limit     int
	Offset    int
}

// ListSRCRewards 分页检索成果,并返回状态计数(受其它筛选影响,不受 Status 影响)。
func (d *DB) ListSRCRewards(f SRCRewardFilter) ([]*SRCReward, int, map[string]int, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	where := []string{"1=1"}
	args := []any{}
	if f.CompanyID > 0 {
		where = append(where, fmt.Sprintf("r.company_id=$%d", len(args)+1))
		args = append(args, f.CompanyID)
	}
	if f.TaskID > 0 {
		where = append(where, fmt.Sprintf("r.task_id=$%d", len(args)+1))
		args = append(args, f.TaskID)
	}
	if f.Severity != "" {
		where = append(where, fmt.Sprintf("r.severity=$%d", len(args)+1))
		args = append(args, f.Severity)
	}
	if strings.TrimSpace(f.Q) != "" {
		where = append(where, fmt.Sprintf("(r.title ILIKE $%d OR r.vuln_class ILIKE $%d)", len(args)+1, len(args)+1))
		args = append(args, "%"+strings.TrimSpace(f.Q)+"%")
	}
	kw := strings.Join(where, " AND ")

	counts := map[string]int{}
	rows, err := d.Query(`SELECT r.status, COUNT(*) FROM src_rewards r WHERE `+kw+` GROUP BY r.status`, args...)
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
		filtered += fmt.Sprintf(" AND r.status=$%d", len(fargs)+1)
		fargs = append(fargs, f.Status)
	}
	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM src_rewards r WHERE `+filtered, fargs...).Scan(&total); err != nil {
		return nil, 0, nil, err
	}

	order := "r.created_at DESC, r.id DESC"
	switch f.Sort {
	case "updated":
		order = "r.updated_at DESC, r.id DESC"
	case "severity":
		order = `CASE r.severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END ASC, r.created_at DESC`
	}
	q := `SELECT ` + srcRewardCols + ` FROM src_rewards r LEFT JOIN companies c ON c.id=r.company_id WHERE ` + filtered +
		` ORDER BY ` + order + fmt.Sprintf(` LIMIT $%d OFFSET $%d`, len(fargs)+1, len(fargs)+2)
	fargs = append(fargs, f.Limit, f.Offset)
	items, err := d.Query(q, fargs...)
	if err != nil {
		return nil, 0, nil, err
	}
	defer items.Close()
	out := []*SRCReward{}
	for items.Next() {
		r, err := scanSRCReward(items)
		if err != nil {
			return nil, 0, nil, err
		}
		out = append(out, r)
	}
	return out, total, counts, items.Err()
}

// GetSRCReward 取一条成果。
func (d *DB) GetSRCReward(id int64) (*SRCReward, error) {
	r, err := scanSRCReward(d.QueryRow(`SELECT `+srcRewardCols+` FROM src_rewards r
		LEFT JOIN companies c ON c.id=r.company_id WHERE r.id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// SRCRewardInput 是新建/更新成果的输入。
type SRCRewardInput struct {
	CompanyID   *int64
	TaskID      *int64
	FindingID   *int64
	EntryID     *int64
	Title       string
	Severity    string
	VulnClass   string
	Status      string
	PlatformRef string
	Feedback    string
}

// CreateSRCReward 新建一条成果。
func (d *DB) CreateSRCReward(in SRCRewardInput) (*SRCReward, error) {
	if strings.TrimSpace(in.Title) == "" {
		return nil, fmt.Errorf("标题必填")
	}
	if in.Status == "" {
		in.Status = "pending"
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO src_rewards(company_id, task_id, finding_id, entry_id, title, severity, vuln_class, status, platform_ref, feedback)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`,
		in.CompanyID, in.TaskID, in.FindingID, in.EntryID, strings.TrimSpace(in.Title),
		in.Severity, in.VulnClass, in.Status, in.PlatformRef, in.Feedback).Scan(&id)
	if err != nil {
		return nil, err
	}
	return d.GetSRCReward(id)
}

// UpdateSRCReward 更新成果(提交状态/平台反馈等)。
// 状态改为 submitted 时自动记提交时间。
func (d *DB) UpdateSRCReward(id int64, in SRCRewardInput) error {
	_, err := d.Exec(`UPDATE src_rewards SET
	title=$2, severity=$3, vuln_class=$4, status=$5, platform_ref=$6, feedback=$7,
	submitted_at = CASE WHEN $5 IN ('submitted','accepted','rejected','duplicate') AND submitted_at IS NULL
	                    THEN now() ELSE submitted_at END,
	updated_at=now()
WHERE id=$1`,
		id, strings.TrimSpace(in.Title), in.Severity, in.VulnClass, in.Status, in.PlatformRef, in.Feedback)
	return err
}

// DeleteSRCReward 删除成果。
func (d *DB) DeleteSRCReward(id int64) (bool, error) {
	res, err := d.Exec(`DELETE FROM src_rewards WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// AutoCreateSRCReward 任务在 SRC 模式下产出 finding 时自动建成果(幂等:
// 同一 finding 只建一条)。返回是否为新建。
func (d *DB) AutoCreateSRCReward(companyID, taskID, findingID int64, title, severity, vulnClass string) (int64, bool, error) {
	var existing int64
	err := d.QueryRow(`SELECT id FROM src_rewards WHERE finding_id=$1 LIMIT 1`, findingID).Scan(&existing)
	if err == nil {
		return existing, false, nil // 已有成果
	}
	if err != sql.ErrNoRows {
		return 0, false, err
	}
	r, err := d.CreateSRCReward(SRCRewardInput{
		CompanyID: &companyID, TaskID: &taskID, FindingID: &findingID,
		Title: title, Severity: severity, VulnClass: vulnClass, Status: "pending",
	})
	if err != nil {
		return 0, false, err
	}
	return r.ID, true, nil
}

// SetSRCRewardEntryByFinding 把漏洞库条目回填到该 finding 的成果记录(自动归类后调用)。
// 成果不存在时静默跳过(非 SRC 任务的发现没有成果记录)。
func (d *DB) SetSRCRewardEntryByFinding(findingID, entryID int64) error {
	if findingID <= 0 || entryID <= 0 {
		return nil
	}
	_, err := d.Exec(`UPDATE src_rewards SET entry_id=$2, updated_at=now()
		WHERE finding_id=$1 AND (entry_id IS NULL OR entry_id <> $2)`, findingID, entryID)
	return err
}

// SRCCompanyStat 是一个企业的成果统计。
type SRCCompanyStat struct {
	CompanyID   int64  `json:"company_id"`
	CompanyName string `json:"company_name"`
	Total       int    `json:"total"`
	Accepted    int    `json:"accepted"`
}

// SRCRewardStats 成果概览统计(企业模式/成果页头部卡片)。
type SRCRewardStats struct {
	Total     int              `json:"total"`
	ByStatus  map[string]int   `json:"by_status"`
	ByCompany []SRCCompanyStat `json:"by_company"`
}

// SRCRewardOverview 成果概览。
func (d *DB) SRCRewardOverview() (*SRCRewardStats, error) {
	// ByCompany 必须是空数组而非 nil:Go 的 nil slice 序列化成 JSON null,
	// 前端读 .length 会直接崩(8787 的 rewards 页「This page couldn't load」就是这个)。
	st := &SRCRewardStats{ByStatus: map[string]int{}, ByCompany: []SRCCompanyStat{}}
	rows, err := d.Query(`SELECT status, COUNT(*) FROM src_rewards GROUP BY status`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			rows.Close()
			return nil, err
		}
		st.ByStatus[s] = n
		st.Total += n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	crows, err := d.Query(`
SELECT COALESCE(r.company_id,0), COALESCE(c.name,'未关联企业'), COUNT(*),
       COUNT(*) FILTER (WHERE r.status='accepted')
  FROM src_rewards r LEFT JOIN companies c ON c.id=r.company_id
 GROUP BY r.company_id, c.name ORDER BY COUNT(*) DESC`)
	if err != nil {
		return nil, err
	}
	defer crows.Close()
	for crows.Next() {
		var row SRCCompanyStat
		if err := crows.Scan(&row.CompanyID, &row.CompanyName, &row.Total, &row.Accepted); err != nil {
			return nil, err
		}
		st.ByCompany = append(st.ByCompany, row)
	}
	return st, crows.Err()
}
