package db

import (
	"encoding/json"
	"time"
)

// ToolExecution 是一次工具手动/自动执行的记录(三阶段工具执行页的历史底账)。
type ToolExecution struct {
	ID              int64           `json:"id"`
	Stage           string          `json:"stage"`
	ToolKey         string          `json:"tool_key"`
	Input           json.RawMessage `json:"input"`
	Status          string          `json:"status"` // running|done|failed
	Summary         json.RawMessage `json:"summary"`
	RawEvidenceHash string          `json:"raw_evidence_hash"`
	CreatedAt       time.Time       `json:"created_at"`
	EndedAt         *time.Time      `json:"ended_at,omitempty"`
}

const toolExecutionCols = `id, stage, tool_key, input, status, summary, raw_evidence_hash, created_at, ended_at`

func scanToolExecution(sc interface{ Scan(...any) error }) (*ToolExecution, error) {
	var e ToolExecution
	if err := sc.Scan(&e.ID, &e.Stage, &e.ToolKey, &e.Input, &e.Status, &e.Summary, &e.RawEvidenceHash, &e.CreatedAt, &e.EndedAt); err != nil {
		return nil, err
	}
	return &e, nil
}

// CreateToolExecution 开一条 running 记录。
func (d *DB) CreateToolExecution(stage, toolKey string, input json.RawMessage) (int64, error) {
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	var id int64
	err := d.QueryRow(`INSERT INTO tool_executions(stage, tool_key, input) VALUES ($1,$2,$3) RETURNING id`,
		stage, toolKey, input).Scan(&id)
	return id, err
}

// FinishToolExecution 收尾一条执行记录。
func (d *DB) FinishToolExecution(id int64, status string, summary json.RawMessage, rawHash string) error {
	if len(summary) == 0 {
		summary = json.RawMessage("{}")
	}
	_, err := d.Exec(`
UPDATE tool_executions SET status=$2, summary=$3,
  raw_evidence_hash=CASE WHEN $4<>'' THEN $4 ELSE raw_evidence_hash END, ended_at=now()
WHERE id=$1`, id, status, summary, rawHash)
	return err
}

// ListToolExecutions 按阶段倒序列出执行记录。
func (d *DB) ListToolExecutions(stage string, limit int) ([]*ToolExecution, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := d.Query(`SELECT `+toolExecutionCols+` FROM tool_executions WHERE stage=$1 ORDER BY id DESC LIMIT $2`,
		stage, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ToolExecution
	for rows.Next() {
		e, err := scanToolExecution(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
