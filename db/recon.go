package db

import (
	"encoding/json"
	"fmt"
	"time"
)

// ---- 收集器执行记录 (task_collector_runs) ----

// CollectorRun 是流水线中一个收集器的一次执行轨迹。
type CollectorRun struct {
	ID              int64           `json:"id"`
	TaskID          int64           `json:"task_id"`
	Collector       string          `json:"collector"`
	Position        int             `json:"position"`
	Status          string          `json:"status"` // running|done|failed|skipped
	Stats           json.RawMessage `json:"stats"`
	Error           string          `json:"error"`
	RawEvidenceHash string          `json:"raw_evidence_hash"`
	StartedAt       time.Time       `json:"started_at"`
	EndedAt         *time.Time      `json:"ended_at,omitempty"`
}

const collectorRunCols = `id, task_id, collector, position, status, stats, error, raw_evidence_hash, started_at, ended_at`

func scanCollectorRun(sc interface{ Scan(...any) error }) (*CollectorRun, error) {
	var r CollectorRun
	if err := sc.Scan(&r.ID, &r.TaskID, &r.Collector, &r.Position, &r.Status, &r.Stats, &r.Error, &r.RawEvidenceHash, &r.StartedAt, &r.EndedAt); err != nil {
		return nil, err
	}
	if r.Stats == nil {
		r.Stats = json.RawMessage("{}")
	}
	return &r, nil
}

// CreateCollectorRun 开一条 running 记录，返回其 id。
func (d *DB) CreateCollectorRun(taskID int64, collector string, position int) (int64, error) {
	var id int64
	err := d.QueryRow(`INSERT INTO task_collector_runs(task_id, collector, position) VALUES ($1,$2,$3) RETURNING id`,
		taskID, collector, position).Scan(&id)
	return id, err
}

// FinishCollectorRun 收尾一条执行记录。stats 为 nil 时保留原值。
func (d *DB) FinishCollectorRun(runID int64, status string, stats json.RawMessage, errMsg string, rawHash string) error {
	switch status {
	case "done", "failed", "skipped":
	default:
		return fmt.Errorf("invalid collector run status %q", status)
	}
	if stats == nil {
		stats = json.RawMessage("null")
	}
	_, err := d.Exec(`
UPDATE task_collector_runs
SET status=$2,
    stats=COALESCE($3, stats),
    error=$4,
    raw_evidence_hash=CASE WHEN $5<>'' THEN $5 ELSE raw_evidence_hash END,
    ended_at=now()
WHERE id=$1`, runID, status, stats, errMsg, rawHash)
	return err
}

// ListCollectorRuns 返回任务全部收集器执行记录(按流水线位置顺序)。
func (d *DB) ListCollectorRuns(taskID int64) ([]*CollectorRun, error) {
	rows, err := d.Query(`SELECT `+collectorRunCols+` FROM task_collector_runs WHERE task_id=$1 ORDER BY position, id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CollectorRun
	for rows.Next() {
		r, err := scanCollectorRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetCollectorRun 取一条属于指定任务的执行记录;不存在返回 nil。
func (d *DB) GetCollectorRun(taskID, runID int64) (*CollectorRun, error) {
	r, err := scanCollectorRun(d.QueryRow(`SELECT `+collectorRunCols+` FROM task_collector_runs WHERE id=$1 AND task_id=$2`, runID, taskID))
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

// ---- 暂存区 (staged_assets) ----

// StagedAsset 是收集器产出的一条待采纳资产。采纳前绝不进正式资产图。
type StagedAsset struct {
	ID        int64           `json:"id"`
	TaskID    int64           `json:"task_id"`
	RunID     *int64          `json:"run_id,omitempty"`
	CompanyID *int64          `json:"company_id,omitempty"`
	Collector string          `json:"collector"`
	Type      string          `json:"type"`
	Value     string          `json:"value"`
	Port      int             `json:"port"`
	Extra     json.RawMessage `json:"extra"`
	Status    string          `json:"status"` // pending|accepted|ignored
	BatchID   int64           `json:"batch_id"`
	CreatedAt time.Time       `json:"created_at"`
	DecidedAt *time.Time      `json:"decided_at,omitempty"`
}

const stagedAssetCols = `id, task_id, run_id, company_id, collector, type, value, port, extra, status, batch_id, created_at, decided_at`

func scanStagedAsset(sc interface{ Scan(...any) error }) (*StagedAsset, error) {
	var a StagedAsset
	if err := sc.Scan(&a.ID, &a.TaskID, &a.RunID, &a.CompanyID, &a.Collector, &a.Type, &a.Value, &a.Port, &a.Extra, &a.Status, &a.BatchID, &a.CreatedAt, &a.DecidedAt); err != nil {
		return nil, err
	}
	return &a, nil
}

// InsertStagedAsset 幂等插入一条暂存资产：(task,collector,type,value,port) 重复时
// 仅在原记录仍是 pending 时刷新 run/company 归属，计数通过返回的 inserted 区分。
func (d *DB) InsertStagedAsset(a *StagedAsset) (inserted bool, err error) {
	if a.Type == "" {
		a.Type = "domain"
	}
	if a.Extra == nil {
		a.Extra = json.RawMessage("{}")
	}
	res, err := d.Exec(`
INSERT INTO staged_assets(task_id, run_id, company_id, collector, type, value, port, extra, batch_id)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT (task_id, collector, type, value, port) DO UPDATE
SET run_id=COALESCE(EXCLUDED.run_id, staged_assets.run_id),
    company_id=COALESCE(EXCLUDED.company_id, staged_assets.company_id),
    batch_id=EXCLUDED.batch_id
WHERE staged_assets.status='pending'`,
		a.TaskID, a.RunID, a.CompanyID, a.Collector, a.Type, a.Value, a.Port, a.Extra, a.BatchID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListStagedAssets 返回任务的暂存资产，pending 在前、新者在前。
func (d *DB) ListStagedAssets(taskID int64, status string) ([]*StagedAsset, error) {
	q := `SELECT ` + stagedAssetCols + ` FROM staged_assets WHERE task_id=$1`
	args := []any{taskID}
	if status != "" {
		q += ` AND status=$2`
		args = append(args, status)
	}
	q += ` ORDER BY (status='pending') DESC, id DESC LIMIT 2000`
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*StagedAsset
	for rows.Next() {
		a, err := scanStagedAsset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListStagedAssetsByRun 返回某次收集器执行的产出(限 limit 条,供图节点/回放)。
func (d *DB) ListStagedAssetsByRun(runID int64, limit int) ([]*StagedAsset, error) {
	if limit <= 0 {
		limit = 60
	}
	rows, err := d.Query(`SELECT `+stagedAssetCols+` FROM staged_assets WHERE run_id=$1 ORDER BY id LIMIT $2`, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*StagedAsset
	for rows.Next() {
		a, err := scanStagedAsset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// maxDecideBatch 单次采纳/忽略的暂存资产上限,与列表页 2000 条展示上限对齐。
const maxDecideBatch = 2000

// ListPendingStagedAssets 返回任务的 pending 暂存资产,按 id 升序、封顶 2000。
// ids 非空时只取该任务内指定 id(已落定/不属于本任务的 id 静默跳过)。
func (d *DB) ListPendingStagedAssets(taskID int64, ids []int64) ([]*StagedAsset, error) {
	q := `SELECT ` + stagedAssetCols + ` FROM staged_assets WHERE task_id=$1 AND status='pending'`
	args := []any{taskID}
	if len(ids) > 0 {
		q += ` AND id = ANY($2)`
		args = append(args, ids)
	}
	q += ` ORDER BY id LIMIT $3`
	args = append(args, maxDecideBatch)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*StagedAsset
	for rows.Next() {
		a, err := scanStagedAsset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DecideStagedAsset 把一条 pending 暂存资产落定为 accepted/ignored。
// 只在 status='pending' 时生效,返回是否本次真的变更(幂等:重复落定返回 false)。
func (d *DB) DecideStagedAsset(taskID, id int64, status string) (bool, error) {
	switch status {
	case "accepted", "ignored":
	default:
		return false, fmt.Errorf("invalid staged asset status %q", status)
	}
	res, err := d.Exec(`
UPDATE staged_assets SET status=$3, decided_at=now()
WHERE id=$1 AND task_id=$2 AND status='pending'`, id, taskID, status)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// CountStagedAssets 按状态汇总任务的暂存资产数量。
func (d *DB) CountStagedAssets(taskID int64) (pending, accepted, ignored int, err error) {
	err = d.QueryRow(`
SELECT
  COUNT(*) FILTER (WHERE status='pending'),
  COUNT(*) FILTER (WHERE status='accepted'),
  COUNT(*) FILTER (WHERE status='ignored')
FROM staged_assets WHERE task_id=$1`, taskID).Scan(&pending, &accepted, &ignored)
	return
}

// MaxStagedAssetBatchID 返回任务当前最大批次号(0=尚无批次)。
func (d *DB) MaxStagedAssetBatchID(taskID int64) (int64, error) {
	var max int64
	err := d.QueryRow(`SELECT COALESCE(MAX(batch_id),0) FROM staged_assets WHERE task_id=$1`, taskID).Scan(&max)
	return max, err
}
