package db

import (
	"encoding/json"
	"time"
)

// ---- 报告事件流 (report_events) ----
// 记录报告中心产出的生成过程:谁(入口)在何时对哪份报告/模板做了什么。
// 报告删除后事件保留,历史可溯。

// ReportEvent 是一条生成过程事件。
type ReportEvent struct {
	ID         int64           `json:"id"`
	ReportID   *int64          `json:"report_id,omitempty"`
	TemplateID *int64          `json:"template_id,omitempty"`
	Kind       string          `json:"kind"`  // created|archived|updated|exported|template_applied|uploaded|deleted
	Actor      string          `json:"actor"` // user|agent|system
	TaskID     *int64          `json:"task_id,omitempty"`
	Detail     json.RawMessage `json:"detail"`
	CreatedAt  time.Time       `json:"created_at"`
}

const reportEventCols = `id, report_id, template_id, kind, actor, task_id, detail, created_at`

func scanReportEvent(sc interface{ Scan(...any) error }) (*ReportEvent, error) {
	var e ReportEvent
	if err := sc.Scan(&e.ID, &e.ReportID, &e.TemplateID, &e.Kind, &e.Actor, &e.TaskID, &e.Detail, &e.CreatedAt); err != nil {
		return nil, err
	}
	if e.Detail == nil {
		e.Detail = json.RawMessage("{}")
	}
	return &e, nil
}

// AddReportEvent 追加一条事件。detail 为 nil 时存 {}。
func (d *DB) AddReportEvent(reportID, templateID, taskID *int64, kind, actor string, detail json.RawMessage) error {
	if detail == nil {
		detail = json.RawMessage("{}")
	}
	_, err := d.Exec(`
INSERT INTO report_events(report_id, template_id, kind, actor, task_id, detail)
VALUES ($1, $2, $3, $4, $5, $6)`,
		reportID, templateID, kind, actor, taskID, detail)
	return err
}

// ListReportEvents 返回某份报告的事件流(旧→新),供「生成过程」时间线。
func (d *DB) ListReportEvents(reportID int64) ([]*ReportEvent, error) {
	rows, err := d.Query(`SELECT `+reportEventCols+` FROM report_events WHERE report_id=$1 ORDER BY id`, reportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ReportEvent{}
	for rows.Next() {
		e, err := scanReportEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListTemplateEvents 返回某模板的事件流(旧→新)。
func (d *DB) ListTemplateEvents(templateID int64) ([]*ReportEvent, error) {
	rows, err := d.Query(`SELECT `+reportEventCols+` FROM report_events WHERE template_id=$1 ORDER BY id`, templateID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ReportEvent{}
	for rows.Next() {
		e, err := scanReportEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
