package db

// 任务资产识别入库 db 层(契约 ASSET_EXTRACT_CONTRACTS.md §2):
// asset_extract_runs 每任务一行(1:1,重跑覆盖) + 过程数据纯取数查询。
// 管线本体在 server/asset_extract.go,本文件不做任何业务决策。
// 注:SQL 字符串内一律不加 // 注释(项目红线,PG 42601 且被吞)。

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// AssetExtractRun 是一次资产识别轮的记录(wire 契约 §6,json tag 全小写下划线)。
type AssetExtractRun struct {
	ID                 int64           `json:"id"`
	TaskID             int64           `json:"task_id"`
	Status             string          `json:"status"` // pending|running|done|partial|failed
	DeterministicCount int             `json:"deterministic_count"`
	AIConfirmedCount   int             `json:"ai_confirmed_count"`
	AINewCount         int             `json:"ai_new_count"`
	AdoptedCount       int             `json:"adopted_count"`
	Detail             json.RawMessage `json:"detail"`
	Error              string          `json:"error"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

const assetExtractRunCols = `id, task_id, status, deterministic_count, ai_confirmed_count, ai_new_count, adopted_count, detail, error, created_at, updated_at`

func scanAssetExtractRun(sc interface{ Scan(...any) error }) (*AssetExtractRun, error) {
	var r AssetExtractRun
	if err := sc.Scan(&r.ID, &r.TaskID, &r.Status, &r.DeterministicCount, &r.AIConfirmedCount,
		&r.AINewCount, &r.AdoptedCount, &r.Detail, &r.Error, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	if r.Detail == nil {
		r.Detail = json.RawMessage("[]")
	}
	return &r, nil
}

// UpsertAssetExtractRun 按 task_id upsert 一条识别轮记录(重跑覆盖),返回行 id。
func (d *DB) UpsertAssetExtractRun(r *AssetExtractRun) (int64, error) {
	if r == nil {
		return 0, errors.New("asset extract run is required")
	}
	if r.TaskID <= 0 {
		return 0, fmt.Errorf("invalid task id %d", r.TaskID)
	}
	if r.Detail == nil {
		r.Detail = json.RawMessage("[]")
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO asset_extract_runs(task_id, status, deterministic_count, ai_confirmed_count, ai_new_count, adopted_count, detail, error)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (task_id) DO UPDATE SET
    status=EXCLUDED.status,
    deterministic_count=EXCLUDED.deterministic_count,
    ai_confirmed_count=EXCLUDED.ai_confirmed_count,
    ai_new_count=EXCLUDED.ai_new_count,
    adopted_count=EXCLUDED.adopted_count,
    detail=EXCLUDED.detail,
    error=EXCLUDED.error
RETURNING id`,
		r.TaskID, r.Status, r.DeterministicCount, r.AIConfirmedCount, r.AINewCount,
		r.AdoptedCount, r.Detail, r.Error).Scan(&id)
	return id, err
}

// GetAssetExtractRunByTask 取任务的识别轮记录;无行返回 nil,nil。
func (d *DB) GetAssetExtractRunByTask(taskID int64) (*AssetExtractRun, error) {
	r, err := scanAssetExtractRun(d.QueryRow(`SELECT `+assetExtractRunCols+` FROM asset_extract_runs WHERE task_id=$1`, taskID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

// SetAssetExtractRunStatus 只改状态与错误文案(终态兜底路径,保证不留悬挂 running)。
func (d *DB) SetAssetExtractRunStatus(taskID int64, status, errMsg string) error {
	_, err := d.Exec(`UPDATE asset_extract_runs SET status=$2, error=$3 WHERE task_id=$1`, taskID, status, errMsg)
	return err
}

// TaskActivityText 是任务过程数据的一条摘录(activity 列:kind/summary/detail)。
type TaskActivityText struct {
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
	Detail  string `json:"detail"`
}

// TaskActivityTexts 取任务最近 limit 条活动文本(经 tasks.exploration_id 关联,
// 与 schema.sql 实际外键一致),新者在前。集合恒非 nil;limit<=0 返回空集。
func (d *DB) TaskActivityTexts(taskID int64, limit int) ([]TaskActivityText, error) {
	out := []TaskActivityText{}
	if limit <= 0 {
		return out, nil
	}
	rows, err := d.Query(`
SELECT COALESCE(a.kind,''), COALESCE(a.summary,''), COALESCE(a.detail,'')
FROM activity a
JOIN tasks t ON t.exploration_id = a.exploration_id
WHERE t.id=$1
ORDER BY a.id DESC LIMIT $2`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t TaskActivityText
		if err := rows.Scan(&t.Kind, &t.Summary, &t.Detail); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TaskFindingEvidence 取任务最近的非空发现证据原文,新者在前。集合恒非 nil。
func (d *DB) TaskFindingEvidence(taskID int64, limit int) ([]string, error) {
	out := []string{}
	if limit <= 0 {
		return out, nil
	}
	rows, err := d.Query(`
SELECT evidence FROM findings
WHERE task_id=$1 AND COALESCE(evidence,'')<>''
ORDER BY id DESC LIMIT $2`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ev string
		if err := rows.Scan(&ev); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// FindStagedAsset 按 (task,collector,type,value,port) 身份取暂存行(即
// InsertStagedAsset 的 ON CONFLICT 判定键),供提取管线取回行 id 做采纳落定;
// 无行返回 nil,nil。
func (d *DB) FindStagedAsset(taskID int64, collector, typ, value string, port int) (*StagedAsset, error) {
	a, err := scanStagedAsset(d.QueryRow(`SELECT `+stagedAssetCols+` FROM staged_assets
WHERE task_id=$1 AND collector=$2 AND type=$3 AND value=$4 AND port=$5`,
		taskID, collector, typ, value, port))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return a, nil
}
