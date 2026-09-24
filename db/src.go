package db

// SRC 平台选择:成果产出(src_rewards)的检索与统计。
//
// 平台与目标的 CRUD 在 src_platforms.go / src_targets.go;本文件只负责成果。
// 成果记录同时带 company_id / platform_id / target_id 三个归属:企业是「谁家的资产」,
// 平台是「提交到哪」,目标是「打的哪个目标」——三者互相独立,缺哪个都不影响另外两个。
// 成果页跟踪每条产出从待提交到平台反馈的全过程。

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ---- SRC 成果 ----

// SRCReward 是一条 SRC 成果(可提交的漏洞产出)。
type SRCReward struct {
	ID           int64      `json:"id"`
	CompanyID    *int64     `json:"company_id,omitempty"`
	CompanyName  string     `json:"company_name,omitempty"`
	PlatformID   *int64     `json:"platform_id,omitempty"`
	PlatformName string     `json:"platform_name,omitempty"`
	TargetID     *int64     `json:"target_id,omitempty"`
	TaskID       *int64     `json:"task_id,omitempty"`
	FindingID    *int64     `json:"finding_id,omitempty"`
	EntryID      *int64     `json:"entry_id,omitempty"`
	Title        string     `json:"title"`
	Severity     string     `json:"severity"`
	VulnClass    string     `json:"vuln_class"`
	Status       string     `json:"status"`
	PlatformRef  string     `json:"platform_ref"`
	Feedback     string     `json:"feedback"`
	SubmittedAt  *time.Time `json:"submitted_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

const srcRewardCols = `r.id, r.company_id, COALESCE(c.name,''), r.platform_id, COALESCE(p.name,''), r.target_id,
	r.task_id, r.finding_id, r.entry_id,
	r.title, r.severity, r.vuln_class, r.status, r.platform_ref, r.feedback, r.submitted_at, r.created_at, r.updated_at`

func scanSRCReward(sc interface{ Scan(...any) error }) (*SRCReward, error) {
	var r SRCReward
	var submitted sql.NullTime
	if err := sc.Scan(&r.ID, &r.CompanyID, &r.CompanyName, &r.PlatformID, &r.PlatformName, &r.TargetID,
		&r.TaskID, &r.FindingID, &r.EntryID,
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
	CompanyID  int64
	PlatformID int64
	TargetID   int64
	TaskID     int64
	Status     string // pending | submitted | accepted | rejected | duplicate
	Severity   string
	Q          string // 标题/漏洞类别关键字
	Sort       string // created | updated | severity
	Desc       bool
	Limit      int
	Offset     int
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
	if f.PlatformID > 0 {
		where = append(where, fmt.Sprintf("r.platform_id=$%d", len(args)+1))
		args = append(args, f.PlatformID)
	}
	if f.TargetID > 0 {
		where = append(where, fmt.Sprintf("r.target_id=$%d", len(args)+1))
		args = append(args, f.TargetID)
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
	q := `SELECT ` + srcRewardCols + ` FROM src_rewards r
	LEFT JOIN companies c ON c.id=r.company_id
	LEFT JOIN src_platforms p ON p.id=r.platform_id WHERE ` + filtered +
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
	LEFT JOIN companies c ON c.id=r.company_id
	LEFT JOIN src_platforms p ON p.id=r.platform_id WHERE r.id=$1`, id))
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
	PlatformID  *int64
	TargetID    *int64
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
INSERT INTO src_rewards(company_id, platform_id, target_id, task_id, finding_id, entry_id, title, severity, vuln_class, status, platform_ref, feedback)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id`,
		nullableIDPtr(in.CompanyID), nullableIDPtr(in.PlatformID), nullableIDPtr(in.TargetID),
		nullableIDPtr(in.TaskID), nullableIDPtr(in.FindingID), nullableIDPtr(in.EntryID),
		strings.TrimSpace(in.Title),
		in.Severity, in.VulnClass, in.Status, in.PlatformRef, in.Feedback).Scan(&id)
	if err != nil {
		return nil, err
	}
	return d.GetSRCReward(id)
}

// nullableIDPtr 把 *int64 里 <=0 的值转成 NULL(外键列不接受 0)。
func nullableIDPtr(v *int64) any {
	if v == nil || *v <= 0 {
		return nil
	}
	return *v
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
//
// platformID/targetID 取自任务本身的归属(任务建时就定了),这样成果自动带上
// 「提交到哪个平台、打的哪个目标」,平台详情页与目标详情页无需再回查任务。
func (d *DB) AutoCreateSRCReward(companyID, platformID, targetID, taskID, findingID int64, title, severity, vulnClass string) (int64, bool, error) {
	var existing int64
	err := d.QueryRow(`SELECT id FROM src_rewards WHERE finding_id=$1 LIMIT 1`, findingID).Scan(&existing)
	if err == nil {
		return existing, false, nil // 已有成果
	}
	if err != sql.ErrNoRows {
		return 0, false, err
	}
	in := SRCRewardInput{
		TaskID: &taskID, FindingID: &findingID,
		Title: title, Severity: severity, VulnClass: vulnClass, Status: "pending",
	}
	// 归属用指针传:0 值表示「无该维度归属」,传进去会被转成 NULL 而不是写 0。
	if companyID > 0 {
		in.CompanyID = &companyID
	}
	if platformID > 0 {
		in.PlatformID = &platformID
	}
	if targetID > 0 {
		in.TargetID = &targetID
	}
	r, err := d.CreateSRCReward(in)
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

// SRCPlatformStat 是一个平台的成果统计。
type SRCPlatformStat struct {
	PlatformID   int64  `json:"platform_id"`
	PlatformName string `json:"platform_name"`
	Total        int    `json:"total"`
	Accepted     int    `json:"accepted"`
}

// SRCRewardStats 成果概览统计(成果页头部卡片 + 平台详情页)。
type SRCRewardStats struct {
	Total      int               `json:"total"`
	ByStatus   map[string]int    `json:"by_status"`
	ByCompany  []SRCCompanyStat  `json:"by_company"`
	ByPlatform []SRCPlatformStat `json:"by_platform"`
}

// SRCRewardOverview 成果概览。
func (d *DB) SRCRewardOverview() (*SRCRewardStats, error) {
	// ByCompany/ByPlatform 必须是空数组而非 nil:Go 的 nil slice 序列化成 JSON null,
	// 前端读 .length 会直接崩(8787 的 rewards 页「This page couldn't load」就是这个)。
	st := &SRCRewardStats{
		ByStatus:   map[string]int{},
		ByCompany:  []SRCCompanyStat{},
		ByPlatform: []SRCPlatformStat{},
	}
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
	for crows.Next() {
		var row SRCCompanyStat
		if err := crows.Scan(&row.CompanyID, &row.CompanyName, &row.Total, &row.Accepted); err != nil {
			crows.Close()
			return nil, err
		}
		st.ByCompany = append(st.ByCompany, row)
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return nil, err
	}
	prows, err := d.Query(`
SELECT COALESCE(r.platform_id,0), COALESCE(p.name,'未关联平台'), COUNT(*),
       COUNT(*) FILTER (WHERE r.status='accepted')
  FROM src_rewards r LEFT JOIN src_platforms p ON p.id=r.platform_id
 GROUP BY r.platform_id, p.name ORDER BY COUNT(*) DESC`)
	if err != nil {
		return nil, err
	}
	defer prows.Close()
	for prows.Next() {
		var row SRCPlatformStat
		if err := prows.Scan(&row.PlatformID, &row.PlatformName, &row.Total, &row.Accepted); err != nil {
			return nil, err
		}
		st.ByPlatform = append(st.ByPlatform, row)
	}
	return st, prows.Err()
}
