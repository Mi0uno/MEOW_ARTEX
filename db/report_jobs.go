package db

import (
	"database/sql"
	"fmt"
	"time"
)

// ---- 报告生成作业 (report_jobs) ----
// 报告中心所有「走 LLM 的报告产出」的统一作业表:
//   export         AI 按模板导出(产物文件落 <data>/report-jobs/,result_path 内部持有);
//   task_report_ai 任务总结报告 AI 撰写(产出覆盖写回 reports,无产物文件)。
// 每条作业 = 一条 stage='report' 监看任务(monitor_task_id) + 一条 report_writer
// 监看会话(conv_id),报告中心的任务/对话视图因此天然收录全部报告生成过程。

// ReportJob 一条报告生成作业。result_path 不导出到 JSON(内部路径不外泄,
// 产物统一经下载接口流出)。
type ReportJob struct {
	ID            int64      `json:"id"`
	Kind          string     `json:"kind"` // export | task_report_ai
	ReportID      int64      `json:"report_id"`
	TemplateID    int64      `json:"template_id,omitempty"`
	SourceTaskID  int64      `json:"source_task_id,omitempty"` // task_report_ai:被撰写的来源任务
	MonitorTaskID int64      `json:"monitor_task_id,omitempty"`
	ConvID        int64      `json:"conv_id,omitempty"`
	Format        string     `json:"format,omitempty"` // export: md|docx
	Status        string     `json:"status"`           // running|done|failed
	Error         string     `json:"error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	resultPath    string
}

const reportJobCols = `id, kind, report_id, template_id, source_task_id, monitor_task_id, conv_id,
	format, status, error, result_path, created_at, updated_at`

func scanReportJob(sc interface{ Scan(...any) error }) (*ReportJob, error) {
	var j ReportJob
	var convID sql.NullInt64
	if err := sc.Scan(&j.ID, &j.Kind, &j.ReportID, &j.TemplateID, &j.SourceTaskID, &j.MonitorTaskID, &convID,
		&j.Format, &j.Status, &j.Error, &j.resultPath, &j.CreatedAt, &j.UpdatedAt); err != nil {
		return nil, err
	}
	if convID.Valid {
		j.ConvID = convID.Int64
	}
	return &j, nil
}

// CreateReportJob 新建一条 running 状态的报告生成作业。
func (d *DB) CreateReportJob(kind string, reportID, templateID, sourceTaskID int64, format string) (*ReportJob, error) {
	return scanReportJob(d.QueryRow(`
INSERT INTO report_jobs(kind, report_id, template_id, source_task_id, format)
VALUES ($1, $2, $3, $4, $5)
RETURNING `+reportJobCols, kind, reportID, templateID, sourceTaskID, format))
}

// SetReportJobMonitor 记录监看任务与会话 id。
func (d *DB) SetReportJobMonitor(id, monitorTaskID, convID int64) error {
	_, err := d.Exec(`UPDATE report_jobs SET
		monitor_task_id=$2, conv_id=$3, updated_at=now() WHERE id=$1`, id, monitorTaskID, convID)
	return err
}

// GetReportJob 取单条作业;不存在返回 nil。
func (d *DB) GetReportJob(id int64) (*ReportJob, error) {
	j, err := scanReportJob(d.QueryRow(`SELECT `+reportJobCols+` FROM report_jobs WHERE id=$1`, id))
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	return j, nil
}

// JobResultPath 取产物文件路径(仅下载接口使用)。
func (d *DB) JobResultPath(id int64) (string, error) {
	var p string
	if err := d.QueryRow(`SELECT result_path FROM report_jobs WHERE id=$1`, id).Scan(&p); err != nil {
		return "", err
	}
	return p, nil
}

// SetReportJobDone 作业完成并记录产物路径。
func (d *DB) SetReportJobDone(id int64, resultPath string) error {
	_, err := d.Exec(`UPDATE report_jobs SET
		status='done', result_path=$2, error='', updated_at=now() WHERE id=$1`, id, resultPath)
	return err
}

// SetReportJobFailed 作业失败(保留原产物不动,可重试)。
func (d *DB) SetReportJobFailed(id int64, reason string) error {
	_, err := d.Exec(`UPDATE report_jobs SET
		status='failed', error=$2, updated_at=now() WHERE id=$1`, id, reason)
	return err
}

// ListReportJobsByReport 报告的作业历史,新者在前。
func (d *DB) ListReportJobsByReport(reportID int64, limit int) ([]*ReportJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := d.Query(`SELECT `+reportJobCols+` FROM report_jobs WHERE report_id=$1
		ORDER BY id DESC LIMIT `+fmt.Sprintf("%d", limit), reportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ReportJob{}
	for rows.Next() {
		j, err := scanReportJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// GetLatestReportJob 取某报告某类型的最新一条作业(无则 nil);导出防重入与
// 任务报告 AI 撰写的幂等判断用。
func (d *DB) GetLatestReportJob(kind string, reportID int64) (*ReportJob, error) {
	j, err := scanReportJob(d.QueryRow(`SELECT `+reportJobCols+` FROM report_jobs
		WHERE kind=$1 AND report_id=$2 ORDER BY id DESC LIMIT 1`, kind, reportID))
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	return j, nil
}

// ReportCompanyIDs 推导一份报告的企业归属(去重,最多 limit 家,<=0 时用 8):
// 任务报告走 report.task_id → task_scope(kind='company');漏洞报告走
// finding → 探索节点锚点 → 资产 → 企业。与 CountReportsByCompany 的派生链一致。
// 报告不存在返回 nil,nil(调用方按「无归属」处理,不报错)。
func (d *DB) ReportCompanyIDs(reportID int64, limit int) ([]int64, error) {
	if limit <= 0 {
		limit = 8
	}
	rows, err := d.Query(`
SELECT DISTINCT company_id FROM (
    SELECT ts.company_id
      FROM reports r
      JOIN task_scope ts ON ts.task_id = r.task_id AND ts.kind = 'company'
     WHERE r.id = $1 AND r.task_id IS NOT NULL
    UNION
    SELECT a.company_id
      FROM reports r
      JOIN findings f ON f.id = r.finding_id
      JOIN exploration_anchors ea ON ea.node_id = f.node_id
      JOIN assets a ON a.id = ea.asset_id
     WHERE r.id = $1 AND a.company_id IS NOT NULL
) c
WHERE company_id IS NOT NULL
LIMIT `+fmt.Sprintf("%d", limit), reportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// BackfillReportJobTaskScope 给存量报告生成任务(AI 导出/AI 润写)补企业归属:
// 这些任务创建于「继承报告企业归属」之前,task_scope 无 company 行,在报告中心
// 任务页按企业过滤时不可见。幂等:只处理还没有企业归属行的任务,重启可重复跑。
// 返回补写的任务数。
func (d *DB) BackfillReportJobTaskScope() (int, error) {
	rows, err := d.Query(`
SELECT t.id, COALESCE((t.step_config::jsonb ->> 'report_id')::bigint, 0)
FROM tasks t
WHERE t.stage = 'report' AND t.step_key IN ('report_export','task_report_ai')
  AND NOT EXISTS (SELECT 1 FROM task_scope ts WHERE ts.task_id = t.id AND ts.kind = 'company')`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type candidate struct {
		taskID   int64
		reportID int64
	}
	cands := []candidate{}
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.taskID, &c.reportID); err != nil {
			return 0, err
		}
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	fixed := 0
	for _, c := range cands {
		if c.reportID <= 0 {
			continue
		}
		companies, err := d.ReportCompanyIDs(c.reportID, 8)
		if err != nil || len(companies) == 0 {
			continue // 报告无归属(或已删):保持任务无企业,落在「未关联企业」桶
		}
		if _, err := d.Exec(`
INSERT INTO task_scope(task_id, kind, company_id, source, reason)
SELECT $1, 'company', cid, 'auto', '报告生成作业继承报告企业归属(回填)'
FROM unnest($2::bigint[]) AS cid
WHERE EXISTS (SELECT 1 FROM companies WHERE id = cid)`, c.taskID, companies); err != nil {
			return fixed, err
		}
		fixed++
	}
	return fixed, nil
}
