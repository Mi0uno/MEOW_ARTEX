package db

// C2 中心二期访问层(契约 C2_CONTRACTS.md §11-§16):免杀工作台
// (c2_evasion_profiles / c2_jobs)与二期回填列(c2_payloads.evasion_profile_id、
// c2_loot.finding_id)。表结构见 schema.sql 尾部,本层不改 schema。
//
// 纪律(照 c2.go / vps.go):
//   - c2_jobs.input / c2_evasion_profiles.techniques 是 JSONB:扫描统一转
//     json.RawMessage 且空值归一 "{}",永不返回 nil(可直接序列化)。
//   - List 的 total 在 WHERE(全部筛选)之后、LIMIT 之前算;列表永不 nil slice。
//   - 可空 BIGINT(profile_id/payload_id/listener_id/vps_machine_id/task_id/
//     finding_id)一律 COALESCE(x,0):0 = 无。
//   - c2_jobs 不携带任何密文;载荷字节/凭据明文永不进这两张表。
//
// 枚举取值一律在 Go 侧白名单校验(表上已有 CHECK,这里再兜一道)。

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ---- 枚举常量与白名单 ----

// C2 作业类型:evasion(载荷变形)/ build(gosrc 经 VPS 编译,Agent A 的构建管线用)。
const (
	C2JobKindEvasion = "evasion"
	C2JobKindBuild   = "build"
)

// ValidC2JobKind 校验作业类型。
func ValidC2JobKind(k string) bool {
	switch k {
	case C2JobKindEvasion, C2JobKindBuild:
		return true
	}
	return false
}

// C2 作业状态:queued(排队)/running(执行中)/done/error。
const (
	C2JobStatusQueued  = "queued"
	C2JobStatusRunning = "running"
	C2JobStatusDone    = "done"
	C2JobStatusError   = "error"
)

// ValidC2JobStatus 校验作业状态。
func ValidC2JobStatus(s string) bool {
	switch s {
	case C2JobStatusQueued, C2JobStatusRunning, C2JobStatusDone, C2JobStatusError:
		return true
	}
	return false
}

// 免杀档案的目标 AV 环境。
const (
	C2EvasionTargetDefender = "defender"
	C2EvasionTarget360      = "360"
	C2EvasionTargetHuorong  = "huorong"
	C2EvasionTargetGeneric  = "generic"
	C2EvasionTargetCustom   = "custom"
)

// ValidC2EvasionTargetAV 校验目标 AV 白名单。
func ValidC2EvasionTargetAV(av string) bool {
	switch av {
	case C2EvasionTargetDefender, C2EvasionTarget360, C2EvasionTargetHuorong,
		C2EvasionTargetGeneric, C2EvasionTargetCustom:
		return true
	}
	return false
}

// 免杀档案来源:manual(手工)/llm(AI 生成)/builtin(三期知识库内置,契约 §20)。
const (
	C2EvasionSourceManual  = "manual"
	C2EvasionSourceLLM     = "llm"
	C2EvasionSourceBuiltin = "builtin"
)

// ValidC2EvasionSource 校验档案来源。
func ValidC2EvasionSource(src string) bool {
	return src == C2EvasionSourceManual || src == C2EvasionSourceLLM || src == C2EvasionSourceBuiltin
}

// ---- 免杀档案 ----

// C2EvasionProfile 是一份免杀档案:目标 AV 环境 + 技术配置,作为 LLM 变形与
// 静态自测的输入。Techniques 是自由 JSON(混淆层级/字符串编码/加载器形态等),
// 语义由 server 侧管线解释,本层只保证它是合法 JSON。
type C2EvasionProfile struct {
	ID            int64           `json:"id"`
	Name          string          `json:"name"`
	TargetAV      string          `json:"target_av"`
	PayloadFormat string          `json:"payload_format"`
	Techniques    json.RawMessage `json:"techniques"`
	Instructions  string          `json:"instructions"`
	Source        string          `json:"source"`
	Notes         string          `json:"notes"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

const c2EvasionProfileCols = `p.id, p.name, p.target_av, p.payload_format, p.techniques,
	p.instructions, p.source, p.notes, p.created_at, p.updated_at`

func scanC2EvasionProfile(sc interface{ Scan(...any) error }) (*C2EvasionProfile, error) {
	p := &C2EvasionProfile{Techniques: json.RawMessage("{}")}
	var tech []byte
	if err := sc.Scan(&p.ID, &p.Name, &p.TargetAV, &p.PayloadFormat, &tech,
		&p.Instructions, &p.Source, &p.Notes, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if len(tech) > 0 {
		p.Techniques = json.RawMessage(tech)
	}
	return p, nil
}

// C2EvasionProfileInput 是新建/编辑免杀档案的输入(整体覆盖)。
type C2EvasionProfileInput struct {
	Name          string
	TargetAV      string
	PayloadFormat string
	Techniques    json.RawMessage
	Instructions  string
	Source        string
	Notes         string
}

// normalizeC2EvasionProfileInput 填默认值并做白名单校验。
func normalizeC2EvasionProfileInput(in *C2EvasionProfileInput) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return fmt.Errorf("档案名称必填")
	}
	if in.TargetAV == "" {
		in.TargetAV = C2EvasionTargetGeneric
	}
	if !ValidC2EvasionTargetAV(in.TargetAV) {
		return fmt.Errorf("无效目标 AV: %q(仅 defender/360/huorong/generic/custom)", in.TargetAV)
	}
	if in.PayloadFormat == "" {
		in.PayloadFormat = C2PayloadFormatPs1
	}
	if !ValidC2PayloadFormat(in.PayloadFormat) {
		return fmt.Errorf("无效载荷格式: %q(仅 ps1/bash/py/gosrc)", in.PayloadFormat)
	}
	if in.Source == "" {
		in.Source = C2EvasionSourceManual
	}
	if !ValidC2EvasionSource(in.Source) {
		return fmt.Errorf("无效来源: %q(仅 manual/llm)", in.Source)
	}
	if len(in.Techniques) == 0 {
		in.Techniques = json.RawMessage("{}")
	}
	var probe any
	if err := json.Unmarshal(in.Techniques, &probe); err != nil {
		return fmt.Errorf("techniques 必须是合法 JSON: %w", err)
	}
	return nil
}

// CreateC2EvasionProfile 建免杀档案。
func (d *DB) CreateC2EvasionProfile(in C2EvasionProfileInput) (int64, error) {
	if err := normalizeC2EvasionProfileInput(&in); err != nil {
		return 0, err
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO c2_evasion_profiles(name, target_av, payload_format, techniques, instructions, source, notes)
VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		in.Name, in.TargetAV, in.PayloadFormat, []byte(in.Techniques), in.Instructions,
		in.Source, in.Notes).Scan(&id)
	return id, err
}

// GetC2EvasionProfile 读单份档案(不存在返回 nil, nil)。
func (d *DB) GetC2EvasionProfile(id int64) (*C2EvasionProfile, error) {
	p, err := scanC2EvasionProfile(d.QueryRow(
		`SELECT `+c2EvasionProfileCols+` FROM c2_evasion_profiles p WHERE p.id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// C2EvasionProfileFilter 是档案列表的筛选条件。零值 = 不限。
type C2EvasionProfileFilter struct {
	TargetAV      string // defender/360/huorong/generic/custom
	PayloadFormat string // ps1/bash/py/gosrc
	Limit         int
	Offset        int
}

// ListC2EvasionProfiles 分页检索档案(新者在前;total 在全部筛选之后算)。
func (d *DB) ListC2EvasionProfiles(f C2EvasionProfileFilter) ([]*C2EvasionProfile, int, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	where := []string{"1=1"}
	args := []any{}
	add := func(cond string, v any) {
		where = append(where, fmt.Sprintf(cond, len(args)+1))
		args = append(args, v)
	}
	if f.TargetAV != "" {
		if !ValidC2EvasionTargetAV(f.TargetAV) {
			return nil, 0, fmt.Errorf("无效目标 AV: %q", f.TargetAV)
		}
		add("p.target_av = $%d", f.TargetAV)
	}
	if f.PayloadFormat != "" {
		if !ValidC2PayloadFormat(f.PayloadFormat) {
			return nil, 0, fmt.Errorf("无效载荷格式: %q", f.PayloadFormat)
		}
		add("p.payload_format = $%d", f.PayloadFormat)
	}
	kw := strings.Join(where, " AND ")

	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM c2_evasion_profiles p WHERE `+kw, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT ` + c2EvasionProfileCols + ` FROM c2_evasion_profiles p WHERE ` + kw +
		` ORDER BY p.id DESC` + fmt.Sprintf(` LIMIT $%d OFFSET $%d`, len(args)+1, len(args)+2)
	args = append(args, f.Limit, f.Offset)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*C2EvasionProfile{}
	for rows.Next() {
		p, err := scanC2EvasionProfile(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

// UpdateC2EvasionProfile 整体覆盖更新(前端整表单提交)。
func (d *DB) UpdateC2EvasionProfile(id int64, in C2EvasionProfileInput) error {
	if err := normalizeC2EvasionProfileInput(&in); err != nil {
		return err
	}
	_, err := d.Exec(`UPDATE c2_evasion_profiles SET
	name=$2, target_av=$3, payload_format=$4, techniques=$5, instructions=$6, source=$7, notes=$8
WHERE id=$1`,
		id, in.Name, in.TargetAV, in.PayloadFormat, []byte(in.Techniques),
		in.Instructions, in.Source, in.Notes)
	return err
}

// DeleteC2EvasionProfile 删档案(作业行保留 profile_id 引用,由页面按「档案已删」展示)。
func (d *DB) DeleteC2EvasionProfile(id int64) (bool, error) {
	res, err := d.Exec(`DELETE FROM c2_evasion_profiles WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ---- C2 作业 ----

// C2Job 是一条 C2 异步作业(免杀变形 / VPS 构建)。TaskID 挂 stage='post' 监看
// 任务(任务中心自动收录);Result 存静态自测报告/构建日志摘要;Input 存发起参数
// (重试时按它重建)。载荷字节不进库,只在 server 层落盘。
type C2Job struct {
	ID           int64           `json:"id"`
	Kind         string          `json:"kind"`
	ProfileID    int64           `json:"profile_id"`
	PayloadID    int64           `json:"payload_id"`
	ListenerID   int64           `json:"listener_id"`
	VPSMachineID int64           `json:"vps_machine_id"`
	TaskID       int64           `json:"task_id"`
	Status       string          `json:"status"`
	Input        json.RawMessage `json:"input"`
	Result       json.RawMessage `json:"result"`
	Error        string          `json:"error"`
	CreatedAt    time.Time       `json:"created_at"`
	DoneAt       *time.Time      `json:"done_at,omitempty"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

const c2JobCols = `j.id, j.kind, COALESCE(j.profile_id,0), COALESCE(j.payload_id,0),
	COALESCE(j.listener_id,0), COALESCE(j.vps_machine_id,0), COALESCE(j.task_id,0),
	j.status, j.input, j.result, j.error, j.created_at, j.done_at, j.updated_at`

func scanC2Job(sc interface{ Scan(...any) error }) (*C2Job, error) {
	j := &C2Job{Input: json.RawMessage("{}"), Result: json.RawMessage("{}")}
	var input, result []byte
	var doneAt sql.NullTime
	if err := sc.Scan(&j.ID, &j.Kind, &j.ProfileID, &j.PayloadID, &j.ListenerID,
		&j.VPSMachineID, &j.TaskID, &j.Status, &input, &result, &j.Error,
		&j.CreatedAt, &doneAt, &j.UpdatedAt); err != nil {
		return nil, err
	}
	if len(input) > 0 {
		j.Input = json.RawMessage(input)
	}
	if len(result) > 0 {
		j.Result = json.RawMessage(result)
	}
	if doneAt.Valid {
		v := doneAt.Time
		j.DoneAt = &v
	}
	return j, nil
}

// C2JobInput 是建作业的输入(初始 queued)。
type C2JobInput struct {
	Kind         string
	ProfileID    int64
	PayloadID    int64
	ListenerID   int64
	VPSMachineID int64
	TaskID       int64
	Input        json.RawMessage
}

// CreateC2Job 建一条作业(初始 queued)。
func (d *DB) CreateC2Job(in C2JobInput) (int64, error) {
	if !ValidC2JobKind(in.Kind) {
		return 0, fmt.Errorf("无效作业类型: %q(仅 evasion/build)", in.Kind)
	}
	input := in.Input
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	var probe any
	if err := json.Unmarshal(input, &probe); err != nil {
		return 0, fmt.Errorf("input 必须是合法 JSON: %w", err)
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO c2_jobs(kind, profile_id, payload_id, listener_id, vps_machine_id, task_id, input)
VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		in.Kind, nullableID(in.ProfileID), nullableID(in.PayloadID), nullableID(in.ListenerID),
		nullableID(in.VPSMachineID), nullableID(in.TaskID), []byte(input)).Scan(&id)
	return id, err
}

// GetC2Job 读单条作业(不存在返回 nil, nil)。
func (d *DB) GetC2Job(id int64) (*C2Job, error) {
	j, err := scanC2Job(d.QueryRow(`SELECT `+c2JobCols+` FROM c2_jobs j WHERE j.id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return j, nil
}

// C2JobFilter 是作业列表的筛选条件。零值 = 不限。
type C2JobFilter struct {
	Kind       string // evasion/build
	Status     string // queued/running/done/error
	ListenerID int64  // 0 = 不限
	Limit      int
	Offset     int
}

// ListC2Jobs 分页检索作业(新者在前;total 在全部筛选之后算)。
func (d *DB) ListC2Jobs(f C2JobFilter) ([]*C2Job, int, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	where := []string{"1=1"}
	args := []any{}
	add := func(cond string, v any) {
		where = append(where, fmt.Sprintf(cond, len(args)+1))
		args = append(args, v)
	}
	if f.Kind != "" {
		if !ValidC2JobKind(f.Kind) {
			return nil, 0, fmt.Errorf("无效作业类型: %q", f.Kind)
		}
		add("j.kind = $%d", f.Kind)
	}
	if f.Status != "" {
		if !ValidC2JobStatus(f.Status) {
			return nil, 0, fmt.Errorf("无效作业状态: %q", f.Status)
		}
		add("j.status = $%d", f.Status)
	}
	if f.ListenerID > 0 {
		add("j.listener_id = $%d", f.ListenerID)
	}
	kw := strings.Join(where, " AND ")

	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM c2_jobs j WHERE `+kw, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT ` + c2JobCols + ` FROM c2_jobs j WHERE ` + kw +
		` ORDER BY j.id DESC` + fmt.Sprintf(` LIMIT $%d OFFSET $%d`, len(args)+1, len(args)+2)
	args = append(args, f.Limit, f.Offset)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*C2Job{}
	for rows.Next() {
		j, err := scanC2Job(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, j)
	}
	return out, total, rows.Err()
}

// TakeC2Job 防抢占领取:queued→running 的条件更新(仿一期 TakeQueuedTasks 的
// 原子语义),返回 false = 已被别的执行方领走/不在排队态。
func (d *DB) TakeC2Job(id int64) (bool, error) {
	res, err := d.Exec(`UPDATE c2_jobs SET status='running' WHERE id=$1 AND status='queued'`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// CountRunningC2Jobs 数某 kind 下「同档案+监听器」的 running 作业(防重入 409 判定)。
// profileID 0 = 未挂档案的临时作业,按 profile_id IS NULL 匹配。
func (d *DB) CountRunningC2Jobs(kind string, profileID, listenerID int64) (int, error) {
	if !ValidC2JobKind(kind) {
		return 0, fmt.Errorf("无效作业类型: %q", kind)
	}
	var n int
	err := d.QueryRow(`SELECT COUNT(*) FROM c2_jobs
WHERE kind=$1 AND status='running' AND listener_id=$2
  AND (($3::bigint <> 0 AND profile_id = $3::bigint) OR ($3::bigint = 0 AND profile_id IS NULL))`,
		kind, listenerID, profileID).Scan(&n)
	return n, err
}

// FinishC2Job 回填作业终态(st ∈ done|error);result 是静态报告/构建日志 JSON
// (nil 归一 "{}")。 done_at 仅终态盖章。
func (d *DB) FinishC2Job(id int64, status string, result json.RawMessage, errText string) error {
	if status != C2JobStatusDone && status != C2JobStatusError {
		return fmt.Errorf("作业终态只能是 done/error: %q", status)
	}
	if len(result) == 0 {
		result = json.RawMessage("{}")
	}
	var probe any
	if err := json.Unmarshal(result, &probe); err != nil {
		return fmt.Errorf("result 必须是合法 JSON: %w", err)
	}
	_, err := d.Exec(`UPDATE c2_jobs SET status=$2, result=$3, error=$4, done_at=now() WHERE id=$1`,
		id, status, []byte(result), errText)
	return err
}

// ResetStuckC2Jobs 把某 kind 下「进程崩溃遗留」的 running 作业拨回 queued
// (启动恢复用;变形是整体重跑语义,拨回后由 runner 幂等重跑)。返回拨回条数。
func (d *DB) ResetStuckC2Jobs(kind string) (int64, error) {
	if !ValidC2JobKind(kind) {
		return 0, fmt.Errorf("无效作业类型: %q", kind)
	}
	res, err := d.Exec(`UPDATE c2_jobs SET status='queued' WHERE kind=$1 AND status='running'`, kind)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ListOpenC2Jobs 列某 kind 下未终态(queued/running)作业,id 升序(启动恢复用)。
func (d *DB) ListOpenC2Jobs(kind string) ([]*C2Job, error) {
	if !ValidC2JobKind(kind) {
		return nil, fmt.Errorf("无效作业类型: %q", kind)
	}
	rows, err := d.Query(`SELECT `+c2JobCols+` FROM c2_jobs j
WHERE j.kind=$1 AND j.status IN ('queued','running') ORDER BY j.id ASC`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*C2Job{}
	for rows.Next() {
		j, err := scanC2Job(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ---- 二期回填列 ----

// SetC2PayloadEvasionProfile 回填载荷的变形来源档案(profileID<=0 置 NULL)。
// 免杀 runner 的时序:CreateC2Payload 拿 id → 落盘 → 这里回填档案。
func (d *DB) SetC2PayloadEvasionProfile(payloadID, profileID int64) error {
	_, err := d.Exec(`UPDATE c2_payloads SET evasion_profile_id=$2 WHERE id=$1`,
		payloadID, nullableID(profileID))
	return err
}

// SetC2LootFinding 回填战利品的转换产物 finding_id。条件更新(finding_id IS NULL)
// 保证「重复转换 409」语义在并发下也成立:返回 false = 已被转换过。
func (d *DB) SetC2LootFinding(lootID, findingID int64) (bool, error) {
	res, err := d.Exec(`UPDATE c2_loot SET finding_id=$2 WHERE id=$1 AND finding_id IS NULL`,
		lootID, findingID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
