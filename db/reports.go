package db

import (
	"fmt"
	"strings"
	"time"
)

// ---- 报告中心 (reports) ----

// Report 是报告中心的一条持久化报告。列表查询不带 Content(正文可能很长),
// 详情(GetReport)才填充。
type Report struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"` // task|finding|custom
	Title     string    `json:"title"`
	Summary   string    `json:"summary"`
	Content   string    `json:"content,omitempty"`
	TaskID    *int64    `json:"task_id,omitempty"`
	FindingID *int64    `json:"finding_id,omitempty"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const reportListCols = `id, kind, title, summary, task_id, finding_id, source, created_at, updated_at`

func scanReport(sc interface{ Scan(...any) error }, withContent bool) (*Report, error) {
	var r Report
	if withContent {
		// 与 GetReport 的列序一致:list 列在前,content 追加在最后。
		if err := sc.Scan(&r.ID, &r.Kind, &r.Title, &r.Summary, &r.TaskID, &r.FindingID, &r.Source, &r.CreatedAt, &r.UpdatedAt, &r.Content); err != nil {
			return nil, err
		}
	} else {
		if err := sc.Scan(&r.ID, &r.Kind, &r.Title, &r.Summary, &r.TaskID, &r.FindingID, &r.Source, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
	}
	return &r, nil
}

// ListReports 按分类(kind,空=全部)、企业归属(companyID,0=不限)、严重度
// (severity,空=不限)与关键字(q 搜标题/摘要/正文)筛选报告,返回当页列表、
// 命中总数与各分类计数(计数受 q/companyID/severity 影响,不受 kind 筛选影响,
// 供分类页签展示)。
//
// 严重度匹配语义:
//   - 漏洞报告(kind=finding):该漏洞本身的等级;
//   - 任务报告(kind=task):任务下任一发现达到该等级——任务报告覆盖整场测试,
//     「含严重发现」是用户筛它的自然语义;
//   - 自定义报告(kind=custom):没有严重度,选了等级即被排除(无从匹配)。
//
// 企业归属经任务链推导:报告 → task_id → task_companies。漏洞报告在 finding
// 无任务时按其锚定资产的企业兜底(report_company_scope 视图语义,见下方子查询)。
func (d *DB) ListReports(kind, q, severity string, companyID int64, limit, offset int) ([]*Report, int, map[string]int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	where := []string{"1=1"}
	args := []any{}
	if q != "" {
		where = append(where, fmt.Sprintf("(r.title ILIKE $%d OR r.summary ILIKE $%d OR r.content ILIKE $%d)", len(args)+1, len(args)+1, len(args)+1))
		args = append(args, "%"+q+"%")
	}
	if companyID > 0 {
		where = append(where, fmt.Sprintf(`EXISTS (
	SELECT 1 FROM task_scope ts WHERE ts.task_id = r.task_id AND ts.kind = 'company' AND ts.company_id = $%d
	UNION ALL
	SELECT 1 FROM findings f
	  JOIN exploration_anchors ea ON ea.node_id = f.node_id
	  JOIN assets a ON a.id = ea.asset_id
	 WHERE f.id = r.finding_id AND a.company_id = $%d
)`, len(args)+1, len(args)+1))
		args = append(args, companyID)
	} else if companyID < 0 {
		// -1 = 「未关联企业」桶:任务与漏洞都没挂到任何企业的报告(含全部自定义报告)。
		where = append(where, `NOT EXISTS (
	SELECT 1 FROM task_scope ts WHERE ts.task_id = r.task_id AND ts.kind = 'company'
	UNION ALL
	SELECT 1 FROM findings f
	  JOIN exploration_anchors ea ON ea.node_id = f.node_id
	  JOIN assets a ON a.id = ea.asset_id
	 WHERE f.id = r.finding_id AND a.company_id IS NOT NULL
)`)
	}
	// 严重度筛选:漏洞报告按该发现本身的等级;任务报告按任务下任一发现的等级
	// (「这场测试打出过严重漏洞」);自定义报告无严重度,任何等级筛选都排除它们。
	// lower():findings.severity 无 CHECK,历史上大小写混存,统一折小写再比。
	if severity != "" {
		where = append(where, fmt.Sprintf(`(
	EXISTS (SELECT 1 FROM findings f WHERE f.id = r.finding_id AND lower(f.severity) = $%d)
	OR (r.kind = 'task' AND r.task_id IS NOT NULL AND EXISTS (
		SELECT 1 FROM findings f WHERE f.task_id = r.task_id AND lower(f.severity) = $%d
	))
)`, len(args)+1, len(args)+1))
		args = append(args, severity)
	}
	kw := strings.Join(where, " AND ")

	// 分类计数:不受 kind 筛选影响——页签要显示各类总数,故用未加 kind 的条件。
	counts := map[string]int{}
	rows, err := d.Query(`SELECT r.kind, COUNT(*) FROM reports r WHERE `+kw+` GROUP BY r.kind`, args...)
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

	// total 必须在 kind 过滤之后算:它表示「当前视图有多少条」,分页与「共 N 份」
	// 都依赖它。曾经在加 kind 之前算,导致切到「任务报告」页签时列表只有 7 条、
	// 却显示「共 174 份」,分页器也按 174 算(点下一页得到空白页)。
	filtered := kw
	fargs := append([]any(nil), args...)
	if kind != "" {
		filtered += fmt.Sprintf(" AND r.kind=$%d", len(fargs)+1)
		fargs = append(fargs, kind)
	}

	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM reports r WHERE `+filtered, fargs...).Scan(&total); err != nil {
		return nil, 0, nil, err
	}

	q2 := `SELECT ` + reportListCols + ` FROM reports r WHERE ` + filtered +
		fmt.Sprintf(` ORDER BY r.updated_at DESC, r.id DESC LIMIT $%d OFFSET $%d`, len(fargs)+1, len(fargs)+2)
	fargs = append(fargs, limit, offset)
	items, err := d.Query(q2, fargs...)
	if err != nil {
		return nil, 0, nil, err
	}
	defer items.Close()
	var out []*Report
	for items.Next() {
		r, err := scanReport(items, false)
		if err != nil {
			return nil, 0, nil, err
		}
		out = append(out, r)
	}
	return out, total, counts, items.Err()
}

// GetReport 读取一份报告全文;不存在返回 nil。
func (d *DB) GetReport(id int64) (*Report, error) {
	r, err := scanReport(d.QueryRow(`SELECT `+reportListCols+`, content FROM reports WHERE id=$1`, id), true)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

// UpsertTaskReport 归档一份任务总结报告:同一任务只保留一份,重复归档覆盖。
// 返回记录 id 与本次是否为新建。
func (d *DB) UpsertTaskReport(taskID int64, title, summary, content string) (int64, bool, error) {
	var id int64
	var created bool
	err := d.QueryRow(`
INSERT INTO reports (kind, title, summary, content, task_id, source)
VALUES ('task', $1, $2, $3, $4, 'task_tab')
ON CONFLICT (task_id) WHERE kind='task' AND task_id IS NOT NULL
DO UPDATE SET title=EXCLUDED.title, summary=EXCLUDED.summary, content=EXCLUDED.content, updated_at=now()
RETURNING id, (xmax = 0) AS created`,
		title, summary, content, taskID).Scan(&id, &created)
	return id, created, err
}

// UpsertFindingReport 归档一份漏洞报告:同一发现只保留一份,重复归档覆盖。
func (d *DB) UpsertFindingReport(findingID int64, taskID *int64, title, summary, content string) (int64, bool, error) {
	var id int64
	var created bool
	err := d.QueryRow(`
INSERT INTO reports (kind, title, summary, content, finding_id, task_id, source)
VALUES ('finding', $1, $2, $3, $4, $5, 'finding')
ON CONFLICT (finding_id) WHERE kind='finding' AND finding_id IS NOT NULL
DO UPDATE SET title=EXCLUDED.title, summary=EXCLUDED.summary, content=EXCLUDED.content,
              task_id=COALESCE(EXCLUDED.task_id, reports.task_id), updated_at=now()
RETURNING id, (xmax = 0) AS created`,
		title, summary, content, findingID, taskID).Scan(&id, &created)
	return id, created, err
}

// CreateReport 新建一条自定义报告。
func (d *DB) CreateReport(title, summary, content string) (*Report, error) {
	r, err := scanReport(d.QueryRow(`
INSERT INTO reports (kind, title, summary, content, source)
VALUES ('custom', $1, $2, $3, 'manual')
RETURNING `+reportListCols, title, summary, content), false)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// DeleteReport 删除一份报告;返回是否删除。
func (d *DB) DeleteReport(id int64) (bool, error) {
	res, err := d.Exec(`DELETE FROM reports WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// GetReportByTask 按来源任务取已归档的任务报告;不存在返回 nil。
// 供自动归档判断「是否已有存档」。
func (d *DB) GetReportByTask(taskID int64) (*Report, error) {
	r, err := scanReport(d.QueryRow(`SELECT `+reportListCols+` FROM reports
WHERE kind='task' AND task_id=$1 LIMIT 1`, taskID), false)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

// GetReportByFinding 按来源发现取已归档的漏洞报告;不存在返回 nil。
func (d *DB) GetReportByFinding(findingID int64) (*Report, error) {
	r, err := scanReport(d.QueryRow(`SELECT `+reportListCols+` FROM reports
WHERE kind='finding' AND finding_id=$1 LIMIT 1`, findingID), false)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

// ListFindingsWithReport 返回已有详细报告正文的发现(供启动回填归档)。
// 只取报告正文非空的记录,避免把空壳归档进报告中心。
func (d *DB) ListFindingsWithReport() ([]*DBFinding, error) {
	return d.ListFindingsForExport(FindingFilter{}, nil)
}

// ListTerminalTasks 返回所有已进入终态的存活任务(id 与阶段),供启动回填。
func (d *DB) ListTerminalTasks() ([]TerminalTaskRow, error) {
	rows, err := d.Query(`SELECT id, stage FROM tasks
WHERE deleted_at IS NULL AND status IN ('done','failed','timeout')
ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TerminalTaskRow{}
	for rows.Next() {
		var r TerminalTaskRow
		if err := rows.Scan(&r.ID, &r.Stage); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TerminalTaskRow 是回填用的轻量任务行。
type TerminalTaskRow struct {
	ID    int64
	Stage string
}

// ReportCompanyCount 是一家企业在报告中心下的报告数(按分类细分),// 供「先选企业」列表展示归属与计数。
type ReportCompanyCount struct {
	CompanyID int64 `json:"company_id"`
	Total     int   `json:"total"`
	Task      int   `json:"task"`
	Finding   int   `json:"finding"`
	Custom    int   `json:"custom"`
}

// CountReportsByCompany 统计每个企业名下的报告数。归属推导同 ListReports:
// 任务报告经 task_companies,漏洞报告经锚定资产的企业。自定义报告无归属,
// 单独由 unassigned 返回(供「未关联企业」桶展示)。
func (d *DB) CountReportsByCompany() ([]ReportCompanyCount, int, error) {
	// 一条 UNION 把报告的「报告 id → 企业 id」全部铺平,再按企业聚合。
	// 任务归属读 task_scope(kind='company',任务创建/人工添加的企业范围),
	// 漏洞报告经锚定资产的企业兜底(finding → 探索节点锚点 → 资产 → 企业)。
	rows, err := d.Query(`
WITH report_company AS (
    SELECT r.id AS report_id, r.kind, ts.company_id
      FROM reports r
      JOIN task_scope ts ON ts.task_id = r.task_id AND ts.kind = 'company'
     WHERE r.task_id IS NOT NULL
    UNION
    SELECT r.id, r.kind, a.company_id
      FROM reports r
      JOIN findings f ON f.id = r.finding_id
      JOIN exploration_anchors ea ON ea.node_id = f.node_id
      JOIN assets a ON a.id = ea.asset_id
     WHERE r.finding_id IS NOT NULL AND a.company_id IS NOT NULL
)
SELECT company_id,
       COUNT(DISTINCT report_id),
       COUNT(DISTINCT report_id) FILTER (WHERE kind='task'),
       COUNT(DISTINCT report_id) FILTER (WHERE kind='finding'),
       COUNT(DISTINCT report_id) FILTER (WHERE kind='custom')
  FROM report_company
 WHERE company_id IS NOT NULL
 GROUP BY company_id`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []ReportCompanyCount{}
	for rows.Next() {
		var c ReportCompanyCount
		if err := rows.Scan(&c.CompanyID, &c.Total, &c.Task, &c.Finding, &c.Custom); err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	// 未归属任何企业的报告数(自定义报告 + 任务/发现都没挂企业的)。
	var unassigned int
	if err := d.QueryRow(`
SELECT COUNT(*) FROM reports r
 WHERE NOT EXISTS (SELECT 1 FROM task_scope ts WHERE ts.task_id = r.task_id AND ts.kind = 'company' AND r.task_id IS NOT NULL)
   AND NOT EXISTS (SELECT 1 FROM findings f
                     JOIN exploration_anchors ea ON ea.node_id = f.node_id
                     JOIN assets a ON a.id = ea.asset_id
                    WHERE f.id = r.finding_id AND r.finding_id IS NOT NULL AND a.company_id IS NOT NULL)`).
		Scan(&unassigned); err != nil {
		return nil, 0, err
	}
	return out, unassigned, nil
}
