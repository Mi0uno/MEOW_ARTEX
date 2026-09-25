package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ---- 报告模板 (report_templates) ----
// 上传 md/docx 模板 → 解析结构落库 → 按模板导出报告(见 server/reports_api.go)。

// ReportTemplate 是一份报告导出模板。content 统一为 markdown
// (docx 上传后解析为 md 落地);structure 是解析产物(章节树/统计)。
// OrigPath 非空时指向上传的原始 docx 包,格式保真导出以其为外壳。
// AI 占位识别异步任务化:analyze_status ”/running/done/failed,
// 草稿落 suggested_content(analyze_task_id/analyze_conv_id 指向监看任务与会话)。
// ExportPrompt 是与占位模板配套的「导出写作指令」:识别时由 AI 一并产出,
// AI 按模板导出时注入 LLM,描述各章节数据来源/口吻/格式要求;可在模板管理页编辑。
type ReportTemplate struct {
	ID               int64           `json:"id"`
	Name             string          `json:"name"`
	Category         string          `json:"category"` // 分类(自由文本,空=未分类)
	Format           string          `json:"format"`   // markdown|docx|doc
	Content          string          `json:"content"`
	Structure        json.RawMessage `json:"structure"`
	SourceNote       string          `json:"source_note"`
	OrigPath         string          `json:"orig_path,omitempty"`
	AnalyzeStatus    string          `json:"analyze_status"`
	SuggestedContent string          `json:"suggested_content,omitempty"`
	ExportPrompt     string          `json:"export_prompt,omitempty"`
	AnalyzeError     string          `json:"analyze_error,omitempty"`
	AnalyzeTaskID    int64           `json:"analyze_task_id,omitempty"`
	AnalyzeConvID    int64           `json:"analyze_conv_id,omitempty"`
	AnalyzedAt       *time.Time      `json:"analyzed_at,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
}

const reportTemplateCols = `id, name, category, format, content, structure, source_note, orig_path,
	analyze_status, suggested_content, export_prompt, analyze_error, analyze_task_id, analyze_conv_id, analyzed_at, created_at`

// reportTemplateListCols 列表不回传正文与 AI 草稿(都可能很大),详情/编辑用 GetReportTemplate。
const reportTemplateListCols = `id, name, category, format, structure, source_note, orig_path,
	analyze_status, analyze_error, analyze_task_id, analyze_conv_id, analyzed_at, created_at`

func scanReportTemplate(sc interface{ Scan(...any) error }, withContent bool) (*ReportTemplate, error) {
	var t ReportTemplate
	var analyzedAt sql.NullTime
	// analyze_conv_id 可为 NULL(未识别过的模板),必须用 Null 承载,否则 NULL→int64 扫描报错。
	var convID sql.NullInt64
	if withContent {
		if err := sc.Scan(&t.ID, &t.Name, &t.Category, &t.Format, &t.Content, &t.Structure, &t.SourceNote, &t.OrigPath,
			&t.AnalyzeStatus, &t.SuggestedContent, &t.ExportPrompt, &t.AnalyzeError, &t.AnalyzeTaskID, &convID, &analyzedAt, &t.CreatedAt); err != nil {
			return nil, err
		}
	} else {
		if err := sc.Scan(&t.ID, &t.Name, &t.Category, &t.Format, &t.Structure, &t.SourceNote, &t.OrigPath,
			&t.AnalyzeStatus, &t.AnalyzeError, &t.AnalyzeTaskID, &convID, &analyzedAt, &t.CreatedAt); err != nil {
			return nil, err
		}
	}
	if convID.Valid {
		t.AnalyzeConvID = convID.Int64
	}
	if t.Structure == nil {
		t.Structure = json.RawMessage("{}")
	}
	if analyzedAt.Valid {
		v := analyzedAt.Time
		t.AnalyzedAt = &v
	}
	return &t, nil
}

// CreateReportTemplate 新建模板。
func (d *DB) CreateReportTemplate(name, category, format, content string, structure json.RawMessage, sourceNote string) (*ReportTemplate, error) {
	if structure == nil {
		structure = json.RawMessage("{}")
	}
	return scanReportTemplate(d.QueryRow(`
INSERT INTO report_templates(name, category, format, content, structure, source_note)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING `+reportTemplateCols, name, category, format, content, structure, sourceNote), true)
}

// ListReportTemplates 按分类与名称关键字分页检索(均空 = 全部),新者在前。
// 同时返回命中总数。
func (d *DB) ListReportTemplates(category, q string, limit, offset int) ([]*ReportTemplate, int, error) {
	where := []string{"1=1"}
	args := []any{}
	if category != "" {
		where = append(where, fmt.Sprintf("category = $%d", len(args)+1))
		args = append(args, category)
	}
	if q != "" {
		where = append(where, fmt.Sprintf("name ILIKE $%d", len(args)+1))
		args = append(args, "%"+q+"%")
	}
	w := strings.Join(where, " AND ")
	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM report_templates WHERE `+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := d.Query(`SELECT `+reportTemplateListCols+` FROM report_templates WHERE `+w+
		fmt.Sprintf(` ORDER BY id DESC LIMIT %d OFFSET %d`, limit, offset), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*ReportTemplate{}
	for rows.Next() {
		t, err := scanReportTemplate(rows, false)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, t)
	}
	return out, total, rows.Err()
}

// GetReportTemplate 取模板全文;不存在返回 nil。
func (d *DB) GetReportTemplate(id int64) (*ReportTemplate, error) {
	t, err := scanReportTemplate(d.QueryRow(`SELECT `+reportTemplateCols+` FROM report_templates WHERE id=$1`, id), true)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	return t, nil
}

// UpdateReportTemplate 修改名称、分类与正文。
func (d *DB) UpdateReportTemplate(id int64, name, category, content string) error {
	_, err := d.Exec(`UPDATE report_templates SET name=$2, category=$3, content=$4 WHERE id=$1`, id, name, category, content)
	return err
}

// DeleteReportTemplate 删除模板;返回是否删除。调用方负责删除原始 docx 文件。
func (d *DB) DeleteReportTemplate(id int64) (bool, error) {
	res, err := d.Exec(`DELETE FROM report_templates WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetReportTemplateOrigPath 记录原始 docx 包的落盘路径(格式保真导出用)。
func (d *DB) SetReportTemplateOrigPath(id int64, path string) error {
	_, err := d.Exec(`UPDATE report_templates SET orig_path=$2 WHERE id=$1`, id, path)
	return err
}

// SetReportTemplateAnalyzeStart 标记识别任务启动(running + 监看任务/会话 id),
// 清空旧草稿/旧导出指令与错误(重跑识别 = 旧结论作废,重新产出)。
func (d *DB) SetReportTemplateAnalyzeStart(id, taskID, convID int64) error {
	_, err := d.Exec(`UPDATE report_templates SET
		analyze_status='running', analyze_task_id=$2, analyze_conv_id=$3,
		suggested_content='', export_prompt='', analyze_error='', analyzed_at=NULL WHERE id=$1`, id, taskID, convID)
	return err
}

// SetReportTemplateAnalyzeDone 写入 AI 草稿(占位模板 + 配套导出写作指令)并落 done;
// exportPrompt 可为空(模型未产出指令时不阻塞占位模板本身);失败路径用 SetReportTemplateAnalyzeFailed。
func (d *DB) SetReportTemplateAnalyzeDone(id int64, suggestion, exportPrompt string) error {
	_, err := d.Exec(`UPDATE report_templates SET
		analyze_status='done', suggested_content=$2, export_prompt=$3, analyze_error='', analyzed_at=now() WHERE id=$1`,
		id, suggestion, exportPrompt)
	return err
}

// SetReportTemplateExportPrompt 单独更新导出写作指令(用户在模板管理页手工编辑)。
func (d *DB) SetReportTemplateExportPrompt(id int64, prompt string) error {
	_, err := d.Exec(`UPDATE report_templates SET export_prompt=$2 WHERE id=$1`, id, prompt)
	return err
}

// SetReportTemplateAnalyzeFailed 落失败与原因(保留旧草稿不动,失败可重试)。
func (d *DB) SetReportTemplateAnalyzeFailed(id int64, reason string) error {
	_, err := d.Exec(`UPDATE report_templates SET
		analyze_status='failed', analyze_error=$2, analyzed_at=now() WHERE id=$1`, id, reason)
	return err
}
