package db

// ---- 存储中心 (storage_buckets / storage_objects / storage_migrations) ----
// 契约 STORAGE_CONTRACTS.md。语义:双写过渡——桶为真源+本地回退。
// 凭据由 server 层 secretbox 加密后以 base64 存 secret_enc(密文进 TEXT 一律 base64)。

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// StorageBucket 一个已接入的存储桶。SecretEnc 永不进 JSON(由 server 层 DTO 转换)。
type StorageBucket struct {
	ID             int64          `json:"id"`
	Name           string         `json:"name"`
	Protocol       string         `json:"protocol"` // s3 | webdav
	Endpoint       string         `json:"endpoint"`
	Bucket         string         `json:"bucket"`
	Region         string         `json:"region"`
	PathPrefix     string         `json:"path_prefix"`
	AccessKey      string         `json:"access_key"`
	SecretEnc      string         `json:"-"`
	Active         bool           `json:"active"`
	Status         string         `json:"status"` // ok | degraded | unchecked
	LastCheckAt    sql.NullTime   `json:"-"`
	LastCheckError string         `json:"last_check_error"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	HasSecret      bool           `json:"has_secret"` // 响应用:密钥已设置(不回显)
	LastCheckAtMs  int64          `json:"last_check_at"`
}

const storageBucketCols = `id, name, protocol, endpoint, bucket, region, path_prefix,
	access_key, secret_enc, active, status, last_check_at, last_check_error, created_at, updated_at`

func scanStorageBucket(sc interface{ Scan(...any) error }) (*StorageBucket, error) {
	var b StorageBucket
	if err := sc.Scan(&b.ID, &b.Name, &b.Protocol, &b.Endpoint, &b.Bucket, &b.Region,
		&b.PathPrefix, &b.AccessKey, &b.SecretEnc, &b.Active, &b.Status,
		&b.LastCheckAt, &b.LastCheckError, &b.CreatedAt, &b.UpdatedAt); err != nil {
		return nil, err
	}
	b.HasSecret = b.SecretEnc != ""
	if b.LastCheckAt.Valid {
		b.LastCheckAtMs = b.LastCheckAt.Time.UnixMilli()
	}
	return &b, nil
}

// CreateStorageBucket 新建桶配置;secret 已由调用方加密。返回新行。
func (d *DB) CreateStorageBucket(b *StorageBucket) (*StorageBucket, error) {
	return scanStorageBucket(d.QueryRow(`
INSERT INTO storage_buckets(name, protocol, endpoint, bucket, region, path_prefix, access_key, secret_enc)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
RETURNING `+storageBucketCols,
		b.Name, b.Protocol, b.Endpoint, b.Bucket, b.Region, b.PathPrefix, b.AccessKey, b.SecretEnc))
}

// UpdateStorageBucket 更新桶配置;secretEnc 空串 = 不改旧密钥。
func (d *DB) UpdateStorageBucket(id int64, b *StorageBucket, secretEnc string) error {
	if secretEnc != "" {
		_, err := d.Exec(`UPDATE storage_buckets SET
			name=$2, protocol=$3, endpoint=$4, bucket=$5, region=$6, path_prefix=$7,
			access_key=$8, secret_enc=$9, updated_at=now() WHERE id=$1`,
			id, b.Name, b.Protocol, b.Endpoint, b.Bucket, b.Region, b.PathPrefix, b.AccessKey, secretEnc)
		return err
	}
	_, err := d.Exec(`UPDATE storage_buckets SET
		name=$2, protocol=$3, endpoint=$4, bucket=$5, region=$6, path_prefix=$7,
		access_key=$8, updated_at=now() WHERE id=$1`,
		id, b.Name, b.Protocol, b.Endpoint, b.Bucket, b.Region, b.PathPrefix, b.AccessKey)
	return err
}

// GetStorageBucket 单桶;不存在返回 nil。
func (d *DB) GetStorageBucket(id int64) (*StorageBucket, error) {
	b, err := scanStorageBucket(d.QueryRow(`SELECT `+storageBucketCols+` FROM storage_buckets WHERE id=$1`, id))
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	return b, nil
}

// GetActiveStorageBucket 当前默认桶;未启用返回 nil。
func (d *DB) GetActiveStorageBucket() (*StorageBucket, error) {
	b, err := scanStorageBucket(d.QueryRow(`SELECT `+storageBucketCols+` FROM storage_buckets WHERE active`))
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	return b, nil
}

// StorageBucketFilter 列表筛选与分页(Q 匹配名称/端点/桶名)。
type StorageBucketFilter struct {
	Protocol, Status, Q string
	Limit, Offset       int
}

// ListStorageBuckets 桶列表,新者在前。计数铁律(同 ListReportJobs):
// counts(协议)与 status_counts 各自不含本维度过滤但含其余;total 全过滤后算。
// 返回 slice 与 counts 永不为 nil。
func (d *DB) ListStorageBuckets(f StorageBucketFilter) ([]*StorageBucket, int, map[string]int, map[string]int, error) {
	conds := []string{}
	var args []any
	if q := strings.TrimSpace(f.Q); q != "" {
		args = append(args, "%"+q+"%")
		conds = append(conds, fmt.Sprintf("(name ILIKE $%d OR endpoint ILIKE $%d OR bucket ILIKE $%d)",
			len(args), len(args), len(args)))
	}
	where := func(dims ...string) (string, []any) {
		c := append([]string{}, conds...)
		a := append([]any{}, args...)
		for _, dim := range dims {
			switch dim {
			case "protocol":
				if f.Protocol == "" {
					continue
				}
				a = append(a, f.Protocol)
				c = append(c, fmt.Sprintf("protocol = $%d", len(a)))
			case "status":
				if f.Status == "" {
					continue
				}
				a = append(a, f.Status)
				c = append(c, fmt.Sprintf("status = $%d", len(a)))
			}
		}
		if len(c) == 0 {
			return " WHERE TRUE", a
		}
		return " WHERE " + strings.Join(c, " AND "), a
	}

	protocolCounts := map[string]int{}
	pw, pargs := where()
	rows, err := d.Query(`SELECT protocol, COUNT(*) FROM storage_buckets`+pw+` GROUP BY protocol`, pargs...)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			rows.Close()
			return nil, 0, nil, nil, err
		}
		protocolCounts[k] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, nil, nil, err
	}

	statusCounts := map[string]int{}
	sw, sargs := where("protocol")
	rows, err = d.Query(`SELECT status, COUNT(*) FROM storage_buckets`+sw+` GROUP BY status`, sargs...)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			rows.Close()
			return nil, 0, nil, nil, err
		}
		statusCounts[k] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, nil, nil, err
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	wh, wargs := where("protocol", "status")
	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM storage_buckets`+wh, wargs...).Scan(&total); err != nil {
		return nil, 0, nil, nil, err
	}
	rows, err = d.Query(`SELECT `+storageBucketCols+` FROM storage_buckets`+wh+
		fmt.Sprintf(` ORDER BY id DESC LIMIT %d OFFSET %d`, limit, offset), wargs...)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	defer rows.Close()
	items := []*StorageBucket{}
	for rows.Next() {
		b, err := scanStorageBucket(rows)
		if err != nil {
			return nil, 0, nil, nil, err
		}
		items = append(items, b)
	}
	return items, total, protocolCounts, statusCounts, rows.Err()
}

// 注意:where 闭包内维度过滤只写局部副本,不污染外层 conds(计数口径铁律)。

// DeleteStorageBucket 删桶;返回是否删了行。
func (d *DB) DeleteStorageBucket(id int64) (bool, error) {
	res, err := d.Exec(`DELETE FROM storage_buckets WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// CountStorageObjectsForBucket 桶上的对象登记数(删除前置校验)。
func (d *DB) CountStorageObjectsForBucket(id int64) (int, error) {
	var n int
	err := d.QueryRow(`SELECT COUNT(*) FROM storage_objects WHERE bucket_id=$1`, id).Scan(&n)
	return n, err
}

// ActivateStorageBucket 设默认桶(事务内先清旧的,partial unique index 兜底单行)。
func (d *DB) ActivateStorageBucket(id int64) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`UPDATE storage_buckets SET active=false WHERE active`); err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE storage_buckets SET active=true, updated_at=now() WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("桶不存在: %d", id)
	}
	return tx.Commit()
}

// DeactivateStorageBucket 取消默认桶(全部置 inactive;平台回落纯本地存储)。
func (d *DB) DeactivateStorageBucket() error {
	_, err := d.Exec(`UPDATE storage_buckets SET active=false WHERE active`)
	return err
}

// SetStorageBucketCheck 记录测活结果:成功 → status=ok;失败 → status=degraded。
func (d *DB) SetStorageBucketCheck(id int64, ok bool, errText string) error {
	status := "ok"
	if !ok {
		status = "degraded"
	}
	_, err := d.Exec(`UPDATE storage_buckets SET
		status=$2, last_check_at=now(), last_check_error=$3, updated_at=now() WHERE id=$1`,
		id, status, errText)
	return err
}

// SetStorageBucketStatus 异步上桶失败时标 degraded(成功恢复 ok)。
func (d *DB) SetStorageBucketStatus(id int64, status, errText string) error {
	_, err := d.Exec(`UPDATE storage_buckets SET
		status=$2, last_check_error=$3, updated_at=now() WHERE id=$1`, id, status, errText)
	return err
}

// ---- storage_objects ----

// InsertStorageObject 登记一个已上桶对象;已存在(幂等重跑)返回 false。
func (d *DB) InsertStorageObject(bucketID int64, category, relPath string, size int64) (bool, error) {
	res, err := d.Exec(`INSERT INTO storage_objects(bucket_id, category, rel_path, size)
		VALUES ($1,$2,$3,$4) ON CONFLICT (bucket_id, rel_path) DO NOTHING`,
		bucketID, category, relPath, size)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// HasStorageObject 对象是否已登记上桶(读回退判定与迁移幂等判重)。
func (d *DB) HasStorageObject(bucketID int64, relPath string) (bool, error) {
	var ok bool
	err := d.QueryRow(`SELECT EXISTS(SELECT 1 FROM storage_objects WHERE bucket_id=$1 AND rel_path=$2)`,
		bucketID, relPath).Scan(&ok)
	return ok, err
}

// GetStorageObject 从桶上读对象大小(0 = 未知,仅判断存在性时用)。
func (d *DB) GetStorageObjectSize(bucketID int64, relPath string) (int64, bool, error) {
	var size int64
	err := d.QueryRow(`SELECT size FROM storage_objects WHERE bucket_id=$1 AND rel_path=$2`,
		bucketID, relPath).Scan(&size)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return 0, false, nil
		}
		return 0, false, err
	}
	return size, true, nil
}

// CountStorageObjectsByCategory 各类别已上桶登记数(overview 用),永不为 nil。
func (d *DB) CountStorageObjectsByCategory() (map[string]int, error) {
	out := map[string]int{}
	rows, err := d.Query(`SELECT category, COUNT(*) FROM storage_objects GROUP BY category`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// ListUploadedRelPaths 类别下已上桶的相对路径集合(迁移跳过与本地清理的白名单)。
func (d *DB) ListUploadedRelPaths(bucketID int64, category string) (map[string]bool, error) {
	out := map[string]bool{}
	rows, err := d.Query(`SELECT rel_path FROM storage_objects WHERE bucket_id=$1 AND category=$2`,
		bucketID, category)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out[p] = true
	}
	return out, rows.Err()
}

// ---- storage_migrations ----

// StorageMigration 一条历史数据迁移作业。
type StorageMigration struct {
	ID          int64           `json:"id"`
	BucketID    int64           `json:"bucket_id"`
	Categories  []string        `json:"categories"`
	Status      string          `json:"status"` // running|done|failed|canceled
	Cursor      json.RawMessage `json:"cursor,omitempty"`
	TotalFiles  int64           `json:"total_files"`
	DoneFiles   int64           `json:"done_files"`
	FailedFiles int64           `json:"failed_files"`
	Error       string          `json:"error"`
	StartedAt   time.Time       `json:"started_at"`
	FinishedAt  sql.NullTime    `json:"-"`
	FinishedMs  int64           `json:"finished_at"`
}

const storageMigrationCols = `id, bucket_id, categories, status, cursor, total_files, done_files,
	failed_files, error, started_at, finished_at`

func scanStorageMigration(sc interface{ Scan(...any) error }) (*StorageMigration, error) {
	var m StorageMigration
	var cats string // TEXT[] 经 database/sql 到手是数组字面量 "{a,b}",手工解析
	var cursor []byte
	if err := sc.Scan(&m.ID, &m.BucketID, &cats, &m.Status, &cursor, &m.TotalFiles,
		&m.DoneFiles, &m.FailedFiles, &m.Error, &m.StartedAt, &m.FinishedAt); err != nil {
		return nil, err
	}
	m.Categories = parseTextArray(cats)
	m.Cursor = json.RawMessage(cursor)
	if m.FinishedAt.Valid {
		m.FinishedMs = m.FinishedAt.Time.UnixMilli()
	}
	return &m, nil
}

// parseTextArray 解析 Postgres 一维 TEXT[] 字面量 "{a,b}"(NULL → 空切片)。
// 迁移类别是固定 slug 集合,不含逗号/引号/转义,简化解析即可。
func parseTextArray(s string) []string {
	out := []string{}
	s = strings.TrimSpace(s)
	if s == "" || s == "{}" || s == "NULL" {
		return out
	}
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// CreateStorageMigration 新建 running 作业(categories 空数组在 SQL 侧给 '{}')。
func (d *DB) CreateStorageMigration(bucketID int64, categories []string) (*StorageMigration, error) {
	if categories == nil {
		categories = []string{}
	}
	return scanStorageMigration(d.QueryRow(`
INSERT INTO storage_migrations(bucket_id, categories) VALUES ($1, $2)
RETURNING `+storageMigrationCols, bucketID, categories))
}

// SetStorageMigrationProgress 推进计数(高频调用,只动计数列)。
func (d *DB) SetStorageMigrationProgress(id, done, failed, total int64) error {
	_, err := d.Exec(`UPDATE storage_migrations SET
		done_files=$2, failed_files=$3, total_files=$4 WHERE id=$1`, id, done, failed, total)
	return err
}

// FinishStorageMigration 作业终态并落游标快照(重启续跑的真源在 settings,这里是留档)。
func (d *DB) FinishStorageMigration(id int64, status, errText string, cursorJSON []byte) error {
	_, err := d.Exec(`UPDATE storage_migrations SET
		status=$2, error=$3, cursor=$4, finished_at=now() WHERE id=$1`,
		id, status, errText, cursorJSON)
	return err
}

// GetStorageMigration 单条;不存在 nil。
func (d *DB) GetStorageMigration(id int64) (*StorageMigration, error) {
	m, err := scanStorageMigration(d.QueryRow(`SELECT `+storageMigrationCols+
		` FROM storage_migrations WHERE id=$1`, id))
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	return m, nil
}

// GetRunningStorageMigration 进行中的迁移作业(防重入);无则 nil。
func (d *DB) GetRunningStorageMigration() (*StorageMigration, error) {
	m, err := scanStorageMigration(d.QueryRow(`SELECT `+storageMigrationCols+
		` FROM storage_migrations WHERE status='running' ORDER BY id DESC LIMIT 1`))
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	return m, nil
}

// GetLatestStorageMigration 最近一条迁移作业(任意状态);无则 nil。
func (d *DB) GetLatestStorageMigration() (*StorageMigration, error) {
	m, err := scanStorageMigration(d.QueryRow(`SELECT `+storageMigrationCols+
		` FROM storage_migrations ORDER BY id DESC LIMIT 1`))
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	return m, nil
}

// ListTerminalTaskWorkdirIDs 终态任务 id 集合(task-workdirs 迁移只动终态任务)。
func (d *DB) ListTerminalTaskWorkdirIDs() (map[int64]bool, error) {
	out := map[int64]bool{}
	rows, err := d.Query(`SELECT id FROM tasks WHERE status IN ('done','failed','timeout')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
