package db

// VPS 中心(vps_machines / vps_assignments / vps_executions / vps_toolsets /
// vps_tool_detections):任务级执行环境。自有 VPS 手动录入,agent 的命令类工具
// 经 SSH 在指派机器上执行,Web 终端 / 工具集 / 流水审计。
//
// 纪律:
//   - 凭据列 password_enc/private_key_enc/key_passphrase_enc 是 secretbox 密文,
//     任何查询/响应都不回显(单独的 GetVpsMachineSecrets 仅供 server 层解密拨号)。
//   - tags 是 JSONB,扫出来统一转 []string 且永不返回 nil。
//   - ListVpsMachines 的 total 在 WHERE(全部筛选)之后、LIMIT 之前算(分页依赖)。
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

const (
	VpsAuthPassword = "password"
	VpsAuthKey      = "key"
)

// ValidVpsAuthType 校验认证类型。
func ValidVpsAuthType(t string) bool {
	return t == VpsAuthPassword || t == VpsAuthKey
}

const (
	VpsStatusOnline      = "online"
	VpsStatusOffline     = "offline"
	VpsStatusUnknown     = "unknown"
	VpsStatusBusy        = "busy"
	VpsStatusMaintenance = "maintenance"
)

// ValidVpsStatus 校验机器状态。
func ValidVpsStatus(s string) bool {
	switch s {
	case VpsStatusOnline, VpsStatusOffline, VpsStatusUnknown, VpsStatusBusy, VpsStatusMaintenance:
		return true
	}
	return false
}

const (
	VpsProviderManual = "manual"
	VpsProviderCloud  = "cloud"
)

// ValidVpsProvider 校验机器来源。
func ValidVpsProvider(p string) bool {
	return p == VpsProviderManual || p == VpsProviderCloud
}

const (
	VpsSourceAgent    = "agent"
	VpsSourceTerminal = "terminal"
	VpsSourceToolset  = "toolset"
	VpsSourceManual   = "manual"
	VpsSourceDetect   = "detect"
)

// ValidVpsSource 校验执行流水来源。
func ValidVpsSource(s string) bool {
	switch s {
	case VpsSourceAgent, VpsSourceTerminal, VpsSourceToolset, VpsSourceManual, VpsSourceDetect:
		return true
	}
	return false
}

// ---- 机器 ----

// VpsMachine 是一台可 SSH 的执行机器(不含任何凭据字段,可直接序列化为响应)。
type VpsMachine struct {
	ID            int64    `json:"id"`
	Name          string   `json:"name"`
	Host          string   `json:"host"`
	Port          int      `json:"port"`
	Username      string   `json:"username"`
	AuthType      string   `json:"auth_type"`
	Status        string   `json:"status"`
	OsName        string   `json:"os_name"`
	OsVersion     string   `json:"os_version"`
	Kernel        string   `json:"kernel"`
	Arch          string   `json:"arch"`
	CPUCores      int      `json:"cpu_cores"`
	MemoryMB      int      `json:"memory_mb"`
	DiskGB        int      `json:"disk_gb"`
	Region        string   `json:"region"`
	Provider      string   `json:"provider"`
	Tags          []string `json:"tags"`
	WorkspaceBase string   `json:"workspace_base"`
	// AssignedTaskID 是当前占用该机器的任务 id;0 = 空闲。
	AssignedTaskID int64 `json:"assigned_task_id"`
	// AssignedTaskName 由 LEFT JOIN tasks 带出(仅列表/详情回显)。
	AssignedTaskName string     `json:"assigned_task_name,omitempty"`
	LastError        string     `json:"last_error"`
	LastCheckAt      *time.Time `json:"last_check_at,omitempty"`
	LastOnlineAt     *time.Time `json:"last_online_at,omitempty"`
	Notes            string     `json:"notes"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// vpsMachineCols 是机器行的取列(LEFT JOIN tasks t);凭据列永不出现。
const vpsMachineCols = `m.id, m.name, m.host, m.port, m.username, m.auth_type, m.status,
	m.os_name, m.os_version, m.kernel, m.arch, m.cpu_cores, m.memory_mb, m.disk_gb,
	m.region, m.provider, m.tags, m.workspace_base,
	COALESCE(m.assigned_task_id,0), COALESCE(t.name,''),
	m.last_error, m.last_check_at, m.last_online_at, m.notes, m.created_at, m.updated_at`

const vpsMachineFrom = ` FROM vps_machines m LEFT JOIN tasks t ON t.id = m.assigned_task_id `

func scanVpsMachine(sc interface{ Scan(...any) error }) (*VpsMachine, error) {
	var m VpsMachine
	var tags []byte
	var lastCheck, lastOnline sql.NullTime
	if err := sc.Scan(&m.ID, &m.Name, &m.Host, &m.Port, &m.Username, &m.AuthType, &m.Status,
		&m.OsName, &m.OsVersion, &m.Kernel, &m.Arch, &m.CPUCores, &m.MemoryMB, &m.DiskGB,
		&m.Region, &m.Provider, &tags, &m.WorkspaceBase,
		&m.AssignedTaskID, &m.AssignedTaskName,
		&m.LastError, &lastCheck, &lastOnline, &m.Notes, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}
	m.Tags = []string{}
	if len(tags) > 0 {
		_ = json.Unmarshal(tags, &m.Tags)
		if m.Tags == nil {
			m.Tags = []string{}
		}
	}
	if lastCheck.Valid {
		t := lastCheck.Time
		m.LastCheckAt = &t
	}
	if lastOnline.Valid {
		t := lastOnline.Time
		m.LastOnlineAt = &t
	}
	return &m, nil
}

// VpsMachineFilter 是机器列表的筛选条件。空值 = 不限。
type VpsMachineFilter struct {
	Status   string // online/offline/unknown/busy/maintenance
	Provider string // manual/cloud
	Tag      string // JSONB 包含匹配(tags @> ["tag"])
	Q        string // 名称/主机/用户名/地区关键字
	Limit    int
	Offset   int
}

// ListVpsMachines 分页检索机器池。total 在全部筛选之后计算(分页依赖它)。
func (d *DB) ListVpsMachines(f VpsMachineFilter) ([]*VpsMachine, int, error) {
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
	if f.Status != "" {
		if !ValidVpsStatus(f.Status) {
			return nil, 0, fmt.Errorf("无效状态: %q", f.Status)
		}
		add("m.status = $%d", f.Status)
	}
	if f.Provider != "" {
		if !ValidVpsProvider(f.Provider) {
			return nil, 0, fmt.Errorf("无效来源: %q", f.Provider)
		}
		add("m.provider = $%d", f.Provider)
	}
	if tag := strings.TrimSpace(f.Tag); tag != "" {
		b, _ := json.Marshal([]string{tag})
		add("m.tags @> $%d::jsonb", string(b))
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		where = append(where, fmt.Sprintf(
			"(m.name ILIKE $%d OR m.host ILIKE $%d OR m.username ILIKE $%d OR m.region ILIKE $%d)",
			len(args)+1, len(args)+1, len(args)+1, len(args)+1))
		args = append(args, "%"+q+"%")
	}
	kw := strings.Join(where, " AND ")

	// total 必须在 WHERE 之后算(分页依赖它)。
	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM vps_machines m WHERE `+kw, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT ` + vpsMachineCols + vpsMachineFrom + ` WHERE ` + kw +
		` ORDER BY m.id DESC` + fmt.Sprintf(` LIMIT $%d OFFSET $%d`, len(args)+1, len(args)+2)
	args = append(args, f.Limit, f.Offset)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*VpsMachine{}
	for rows.Next() {
		m, err := scanVpsMachine(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, m)
	}
	return out, total, rows.Err()
}

// GetVpsMachine 读单台机器(不存在返回 nil, nil)。
func (d *DB) GetVpsMachine(id int64) (*VpsMachine, error) {
	m, err := scanVpsMachine(d.QueryRow(`SELECT `+vpsMachineCols+vpsMachineFrom+` WHERE m.id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}

// GetVpsMachineByTask 取当前指派给某任务的机器(无指派返回 nil, nil)。
func (d *DB) GetVpsMachineByTask(taskID int64) (*VpsMachine, error) {
	m, err := scanVpsMachine(d.QueryRow(
		`SELECT `+vpsMachineCols+vpsMachineFrom+` WHERE m.assigned_task_id=$1`, taskID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}

// GetVpsMachineSecrets 读机器凭据密文(仅 server 层解密拨号用,绝不进响应)。
func (d *DB) GetVpsMachineSecrets(id int64) (passwordEnc, privateKeyEnc, passphraseEnc []byte, err error) {
	err = d.QueryRow(`SELECT password_enc, private_key_enc, key_passphrase_enc FROM vps_machines WHERE id=$1`, id).
		Scan(&passwordEnc, &privateKeyEnc, &passphraseEnc)
	if err == sql.ErrNoRows {
		return nil, nil, nil, nil
	}
	return passwordEnc, privateKeyEnc, passphraseEnc, err
}

// VpsMachineInput 是录入机器的输入(凭据为已加密密文;nil=无)。
type VpsMachineInput struct {
	Name                string
	Host                string
	Port                int
	Username            string
	AuthType            string
	PasswordSealed      []byte
	PrivateKeySealed    []byte
	KeyPassphraseSealed []byte
	Region              string
	Tags                []string
	WorkspaceBase       string
	Notes               string
}

// normalizeVpsMachineInput 填默认值并校验。
func normalizeVpsMachineInput(in *VpsMachineInput) error {
	in.Name = strings.TrimSpace(in.Name)
	in.Host = strings.TrimSpace(in.Host)
	in.Username = strings.TrimSpace(in.Username)
	if in.Name == "" {
		return fmt.Errorf("机器名必填")
	}
	if in.Host == "" {
		return fmt.Errorf("主机地址必填")
	}
	if in.Username == "" {
		return fmt.Errorf("SSH 用户名必填")
	}
	if in.Port <= 0 || in.Port > 65535 {
		in.Port = 22
	}
	if in.AuthType == "" {
		in.AuthType = VpsAuthPassword
	}
	if !ValidVpsAuthType(in.AuthType) {
		return fmt.Errorf("无效认证类型: %q(仅 password/key)", in.AuthType)
	}
	if in.AuthType == VpsAuthPassword && len(in.PasswordSealed) == 0 {
		return fmt.Errorf("密码认证必须提供密码")
	}
	if in.AuthType == VpsAuthKey && len(in.PrivateKeySealed) == 0 {
		return fmt.Errorf("私钥认证必须提供私钥")
	}
	if in.WorkspaceBase == "" {
		in.WorkspaceBase = "/root/.artex-ws"
	}
	if !strings.HasPrefix(in.WorkspaceBase, "/") {
		return fmt.Errorf("workspace_base 必须是绝对路径(以 / 开头)")
	}
	if in.Tags == nil {
		in.Tags = []string{}
	}
	return nil
}

// CreateVpsMachine 录入机器(初始状态 unknown,连通性由首测定)。
func (d *DB) CreateVpsMachine(in VpsMachineInput) (*VpsMachine, error) {
	if err := normalizeVpsMachineInput(&in); err != nil {
		return nil, err
	}
	tags, err := json.Marshal(in.Tags)
	if err != nil {
		return nil, err
	}
	var id int64
	err = d.QueryRow(`
INSERT INTO vps_machines(name, host, port, username, auth_type, password_enc, private_key_enc, key_passphrase_enc,
	region, tags, workspace_base, notes)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id`,
		in.Name, in.Host, in.Port, in.Username, in.AuthType,
		in.PasswordSealed, in.PrivateKeySealed, in.KeyPassphraseSealed,
		in.Region, string(tags), in.WorkspaceBase, in.Notes).Scan(&id)
	if err != nil {
		return nil, err
	}
	return d.GetVpsMachine(id)
}

// VpsMachineUpdate 是编辑机器的输入。普通字段整体覆盖(前端整表单提交),
// 凭据为 nil 表示「不改动」;Status 空串=不改(仅维护状态切换走这里)。
type VpsMachineUpdate struct {
	Name                string
	Host                string
	Port                int
	Username            string
	AuthType            string
	PasswordSealed      []byte // nil=不改
	PrivateKeySealed    []byte // nil=不改
	KeyPassphraseSealed []byte // nil=不改
	Region              string
	Tags                []string
	WorkspaceBase       string
	Notes               string
	Status              string // 空串=不改
}

// UpdateVpsMachine 更新机器。
func (d *DB) UpdateVpsMachine(id int64, in VpsMachineUpdate) error {
	if strings.TrimSpace(in.Name) == "" {
		return fmt.Errorf("机器名必填")
	}
	if strings.TrimSpace(in.Host) == "" {
		return fmt.Errorf("主机地址必填")
	}
	if strings.TrimSpace(in.Username) == "" {
		return fmt.Errorf("SSH 用户名必填")
	}
	if in.Port <= 0 || in.Port > 65535 {
		in.Port = 22
	}
	if !ValidVpsAuthType(in.AuthType) {
		return fmt.Errorf("无效认证类型: %q(仅 password/key)", in.AuthType)
	}
	if in.WorkspaceBase == "" {
		in.WorkspaceBase = "/root/.artex-ws"
	}
	if !strings.HasPrefix(in.WorkspaceBase, "/") {
		return fmt.Errorf("workspace_base 必须是绝对路径(以 / 开头)")
	}
	if in.Status != "" && !ValidVpsStatus(in.Status) {
		return fmt.Errorf("无效状态: %q", in.Status)
	}
	if in.Tags == nil {
		in.Tags = []string{}
	}
	tags, err := json.Marshal(in.Tags)
	if err != nil {
		return err
	}
	_, err = d.Exec(`UPDATE vps_machines SET
	name=$2, host=$3, port=$4, username=$5, auth_type=$6, region=$7, tags=$8::jsonb,
	workspace_base=$9, notes=$10,
	status = CASE WHEN $11 = '' THEN status ELSE $11 END,
	password_enc = CASE WHEN $12::boolean THEN $13 ELSE password_enc END,
	private_key_enc = CASE WHEN $14::boolean THEN $15 ELSE private_key_enc END,
	key_passphrase_enc = CASE WHEN $16::boolean THEN $17 ELSE key_passphrase_enc END,
	updated_at=now()
WHERE id=$1`,
		id, strings.TrimSpace(in.Name), strings.TrimSpace(in.Host), in.Port, strings.TrimSpace(in.Username),
		in.AuthType, in.Region, string(tags), in.WorkspaceBase, in.Notes, in.Status,
		in.PasswordSealed != nil, in.PasswordSealed,
		in.PrivateKeySealed != nil, in.PrivateKeySealed,
		in.KeyPassphraseSealed != nil, in.KeyPassphraseSealed)
	return err
}

// DeleteVpsMachine 删除机器(指派历史与执行流水保留,便于审计追溯)。
func (d *DB) DeleteVpsMachine(id int64) (bool, error) {
	res, err := d.Exec(`DELETE FROM vps_machines WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// VpsSysInfo 是测活采集的系统档案(回填机器行)。
type VpsSysInfo struct {
	OsName    string
	OsVersion string
	Kernel    string
	Arch      string
	CPUCores  int
	MemoryMB  int
	DiskGB    int
}

// UpdateVpsMachineCheck 回写一次测活结果:
//   - status 空串 = 保持原状态(busy 机器只探连通性,不许被测活翻状态);
//   - ok=true 刷 last_online_at 并清 last_error;ok=false 记 lastErr;
//   - info 非空时刷新系统档案;last_check_at 恒刷新。
func (d *DB) UpdateVpsMachineCheck(id int64, status string, ok bool, info *VpsSysInfo, lastErr string) error {
	sets := []string{"last_check_at=now()"}
	args := []any{}
	add := func(expr string, v ...any) {
		args = append(args, v...)
		expr = strings.Replace(expr, "?", fmt.Sprintf("$%d", len(args)), 1)
		sets = append(sets, expr)
	}
	if status != "" {
		add("status=?", status)
	}
	if ok {
		add("last_online_at=now()")
		sets = append(sets, "last_error=''")
	} else {
		add("last_error=?", lastErr)
	}
	if info != nil {
		add("os_name=?", info.OsName)
		add("os_version=?", info.OsVersion)
		add("kernel=?", info.Kernel)
		add("arch=?", info.Arch)
		add("cpu_cores=?", info.CPUCores)
		add("memory_mb=?", info.MemoryMB)
		add("disk_gb=?", info.DiskGB)
	}
	args = append(args, id)
	_, err := d.Exec(`UPDATE vps_machines SET `+strings.Join(sets, ", ")+` WHERE id=$`+fmt.Sprint(len(args)), args...)
	return err
}

// ListStaleVpsAssignedMachines 取「指派悬空」的机器:assigned_task_id 指向的任务
// 行不存在,或任务已到终态(done/failed/timeout——与任务终态自动释放的判据一致)。
// 判据必须是「终态才算悬空」,不能写成「非 running/paused 就算悬空」:created 状态
// 同样持有指派(创建即指派;并发受限排队时任务状态列也停在 created,queued 只是
// 布尔标记),开机对账若把这类任务当悬空释放,重启后任务开跑就掉回本机执行
// (掉马甲)。paused 不释放同理——会话可恢复。
func (d *DB) ListStaleVpsAssignedMachines() ([]*VpsMachine, error) {
	rows, err := d.Query(`SELECT ` + vpsMachineCols + vpsMachineFrom + `
WHERE m.assigned_task_id IS NOT NULL AND (t.id IS NULL OR t.status IN ('done','failed','timeout'))
ORDER BY m.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*VpsMachine{}
	for rows.Next() {
		m, err := scanVpsMachine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListVpsBusyMachines 取当前被占用的机器(仪表盘「谁在用」)。
func (d *DB) ListVpsBusyMachines() ([]*VpsMachine, error) {
	rows, err := d.Query(`SELECT ` + vpsMachineCols + vpsMachineFrom + ` WHERE m.assigned_task_id IS NOT NULL ORDER BY m.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*VpsMachine{}
	for rows.Next() {
		m, err := scanVpsMachine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---- 指派(机器占用 + 历史) ----

// AssignVpsMachineToTask 占用机器:写 assigned_task_id + status=busy + 开指派历史行。
// 并发指派的仲裁由部分唯一索引 uq_vps_machines_active_task 兜底:占不到行即失败。
func (d *DB) AssignVpsMachineToTask(machineID, taskID int64, by string) error {
	res, err := d.Exec(`UPDATE vps_machines SET assigned_task_id=$2, status='busy', updated_at=now()
WHERE id=$1 AND assigned_task_id IS NULL`, machineID, taskID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("机器 #%d 已被占用或不存在", machineID)
	}
	_, err = d.Exec(`INSERT INTO vps_assignments(machine_id, task_id, assigned_by) VALUES ($1,$2,$3)`,
		machineID, taskID, by)
	return err
}

// ReleaseVpsMachineFromTask 释放机器:清 assigned_task_id + status=online +
// 关指派历史行。未指派时 no-op nil(幂等)。
func (d *DB) ReleaseVpsMachineFromTask(machineID, taskID int64, reason string) error {
	if _, err := d.Exec(`UPDATE vps_machines SET assigned_task_id=NULL, status='online', updated_at=now()
WHERE id=$1 AND assigned_task_id=$2`, machineID, taskID); err != nil {
		return err
	}
	_, err := d.Exec(`UPDATE vps_assignments SET released_at=now(), release_reason=$3
WHERE machine_id=$1 AND task_id=$2 AND released_at IS NULL`, machineID, taskID, reason)
	return err
}

// ---- 指派历史 ----

// VpsAssignment 是一次指派(含历史;ReleasedAt 为空 = 进行中)。
type VpsAssignment struct {
	ID            int64      `json:"id"`
	MachineID     int64      `json:"machine_id"`
	TaskID        int64      `json:"task_id"`
	TaskName      string     `json:"task_name,omitempty"`
	MachineName   string     `json:"machine_name,omitempty"`
	AssignedBy    string     `json:"assigned_by"`
	AssignedAt    time.Time  `json:"assigned_at"`
	ReleasedAt    *time.Time `json:"released_at,omitempty"`
	ReleaseReason string     `json:"release_reason"`
}

// ListVpsAssignments 列指派历史(machineID=0 表示全部机器),带任务名与机器名。
func (d *DB) ListVpsAssignments(machineID int64, limit int) ([]*VpsAssignment, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := d.Query(`
SELECT a.id, a.machine_id, a.task_id, COALESCE(t.name,''), COALESCE(m.name,''),
	a.assigned_by, a.assigned_at, a.released_at, a.release_reason
FROM vps_assignments a
LEFT JOIN tasks t ON t.id = a.task_id
LEFT JOIN vps_machines m ON m.id = a.machine_id
WHERE ($1 = 0 OR a.machine_id = $1)
ORDER BY a.id DESC LIMIT $2`, machineID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*VpsAssignment{}
	for rows.Next() {
		var a VpsAssignment
		var released sql.NullTime
		if err := rows.Scan(&a.ID, &a.MachineID, &a.TaskID, &a.TaskName, &a.MachineName,
			&a.AssignedBy, &a.AssignedAt, &released, &a.ReleaseReason); err != nil {
			return nil, err
		}
		if released.Valid {
			t := released.Time
			a.ReleasedAt = &t
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

// ---- 执行流水 ----

// VpsExecution 是一条执行流水(完整输出落 <BaseDir>/vps/outputs/<id>.txt,表里只记大小与路径)。
type VpsExecution struct {
	ID         int64     `json:"id"`
	MachineID  int64     `json:"machine_id"`
	TaskID     int64     `json:"task_id"` // 0 = 非任务触发
	Source     string    `json:"source"`
	ToolsetID  int64     `json:"toolset_id"` // 0 = 非工具集
	Command    string    `json:"command"`
	ExitCode   *int      `json:"exit_code"` // null = 没拿到退出码(连接失败/超时)
	DurationMs int       `json:"duration_ms"`
	OutputSize int       `json:"output_size"`
	OutputPath string    `json:"output_path"`
	Error      string    `json:"error"`
	CreatedAt  time.Time `json:"created_at"`
}

const vpsExecCols = `id, machine_id, COALESCE(task_id,0), source, COALESCE(toolset_id,0),
	command, exit_code, duration_ms, output_size, output_path, error, created_at`

func scanVpsExecution(sc interface{ Scan(...any) error }) (*VpsExecution, error) {
	var e VpsExecution
	var exitCode sql.NullInt64
	if err := sc.Scan(&e.ID, &e.MachineID, &e.TaskID, &e.Source, &e.ToolsetID,
		&e.Command, &exitCode, &e.DurationMs, &e.OutputSize, &e.OutputPath, &e.Error, &e.CreatedAt); err != nil {
		return nil, err
	}
	if exitCode.Valid {
		v := int(exitCode.Int64)
		e.ExitCode = &v
	}
	return &e, nil
}

// VpsExecutionFilter 是流水列表的筛选条件。0/空 = 不限。
type VpsExecutionFilter struct {
	MachineID int64
	TaskID    int64
	Source    string
	Limit     int
	Offset    int
}

// ListVpsExecutions 分页检索执行流水(total 在全部筛选之后算)。
func (d *DB) ListVpsExecutions(f VpsExecutionFilter) ([]*VpsExecution, int, error) {
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
	if f.MachineID > 0 {
		add("machine_id = $%d", f.MachineID)
	}
	if f.TaskID > 0 {
		add("task_id = $%d", f.TaskID)
	}
	if f.Source != "" {
		if !ValidVpsSource(f.Source) {
			return nil, 0, fmt.Errorf("无效来源: %q", f.Source)
		}
		add("source = $%d", f.Source)
	}
	kw := strings.Join(where, " AND ")

	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM vps_executions WHERE `+kw, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT ` + vpsExecCols + ` FROM vps_executions WHERE ` + kw +
		fmt.Sprintf(` ORDER BY id DESC LIMIT $%d OFFSET $%d`, len(args)+1, len(args)+2)
	args = append(args, f.Limit, f.Offset)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*VpsExecution{}
	for rows.Next() {
		e, err := scanVpsExecution(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// GetVpsExecution 读单条流水(不存在返回 nil, nil)。
func (d *DB) GetVpsExecution(id int64) (*VpsExecution, error) {
	e, err := scanVpsExecution(d.QueryRow(`SELECT `+vpsExecCols+` FROM vps_executions WHERE id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

// CreateVpsExecution 开一条执行流水,返回流水 id。
func (d *DB) CreateVpsExecution(machineID, taskID int64, source string, toolsetID int64, command string) (int64, error) {
	if !ValidVpsSource(source) {
		return 0, fmt.Errorf("无效来源: %q", source)
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO vps_executions(machine_id, task_id, source, toolset_id, command)
VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		machineID, nullableID(taskID), source, nullableID(toolsetID), command).Scan(&id)
	return id, err
}

// FinishVpsExecution 回填流水的完成字段(exitCode nil = 没拿到退出码)。
func (d *DB) FinishVpsExecution(id int64, exitCode *int, durationMs, outputSize int, outputPath, errMsg string) error {
	var ec any
	if exitCode != nil {
		ec = *exitCode
	}
	_, err := d.Exec(`UPDATE vps_executions SET
	exit_code=$2, duration_ms=$3, output_size=$4, output_path=$5, error=$6 WHERE id=$1`,
		id, ec, durationMs, outputSize, outputPath, errMsg)
	return err
}

// ListRecentVpsExecutions 取最近流水(仪表盘)。
func (d *DB) ListRecentVpsExecutions(limit int) ([]*VpsExecution, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := d.Query(`SELECT `+vpsExecCols+` FROM vps_executions ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*VpsExecution{}
	for rows.Next() {
		e, err := scanVpsExecution(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- 工具集 ----

// VpsToolset 是一个工具集(安装脚本模板)。
type VpsToolset struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Script      string    `json:"script"`
	Builtin     bool      `json:"builtin"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ListVpsToolsets 列全部工具集(内置在前,同名冲突不存在——name 唯一键)。
func (d *DB) ListVpsToolsets() ([]*VpsToolset, error) {
	rows, err := d.Query(`SELECT id, name, description, script, builtin, created_at, updated_at
FROM vps_toolsets ORDER BY builtin DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*VpsToolset{}
	for rows.Next() {
		var t VpsToolset
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.Script, &t.Builtin, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

// GetVpsToolset 读单个工具集(不存在返回 nil, nil)。
func (d *DB) GetVpsToolset(id int64) (*VpsToolset, error) {
	var t VpsToolset
	err := d.QueryRow(`SELECT id, name, description, script, builtin, created_at, updated_at
FROM vps_toolsets WHERE id=$1`, id).
		Scan(&t.ID, &t.Name, &t.Description, &t.Script, &t.Builtin, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// GetVpsToolsetByName 按名读工具集(内置播种判重用;不存在返回 nil, nil)。
func (d *DB) GetVpsToolsetByName(name string) (*VpsToolset, error) {
	var t VpsToolset
	err := d.QueryRow(`SELECT id, name, description, script, builtin, created_at, updated_at
FROM vps_toolsets WHERE name=$1`, name).
		Scan(&t.ID, &t.Name, &t.Description, &t.Script, &t.Builtin, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// VpsToolsetInput 是新建/更新工具集的输入。
type VpsToolsetInput struct {
	Name        string
	Description string
	Script      string
}

// normalizeVpsToolsetInput 校验并消毒。
func normalizeVpsToolsetInput(in *VpsToolsetInput) error {
	in.Name = strings.TrimSpace(in.Name)
	in.Script = strings.TrimSpace(in.Script)
	if in.Name == "" {
		return fmt.Errorf("工具集名称必填")
	}
	if len(in.Name) > 100 {
		return fmt.Errorf("工具集名称过长")
	}
	if in.Script == "" {
		return fmt.Errorf("安装脚本必填")
	}
	return nil
}

// CreateVpsToolset 新建工具集(builtin 标记内置种子)。
func (d *DB) CreateVpsToolset(in VpsToolsetInput, builtin bool) (*VpsToolset, error) {
	if err := normalizeVpsToolsetInput(&in); err != nil {
		return nil, err
	}
	var id int64
	err := d.QueryRow(`INSERT INTO vps_toolsets(name, description, script, builtin)
VALUES ($1,$2,$3,$4) RETURNING id`, in.Name, in.Description, in.Script, builtin).Scan(&id)
	if err != nil {
		return nil, err
	}
	return d.GetVpsToolset(id)
}

// UpdateVpsToolset 更新工具集(builtin 的拒绝由 server 层负责)。
func (d *DB) UpdateVpsToolset(id int64, in VpsToolsetInput) error {
	if err := normalizeVpsToolsetInput(&in); err != nil {
		return err
	}
	_, err := d.Exec(`UPDATE vps_toolsets SET name=$2, description=$3, script=$4 WHERE id=$1`,
		id, in.Name, in.Description, in.Script)
	return err
}

// DeleteVpsToolset 删除工具集。
func (d *DB) DeleteVpsToolset(id int64) (bool, error) {
	res, err := d.Exec(`DELETE FROM vps_toolsets WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ---- 工具检测矩阵 ----

// VpsToolDetection 是机器×工具的最新探测结果。
type VpsToolDetection struct {
	ToolName   string    `json:"tool_name"`
	Installed  bool      `json:"installed"`
	Version    string    `json:"version"`
	DetectedAt time.Time `json:"detected_at"`
}

// UpsertVpsToolDetections 覆盖写检测矩阵(PK(machine_id,tool_name))。
// 空切片也会清掉旧矩阵(探测脚本全量重跑,旧结果整体失效)。
func (d *DB) UpsertVpsToolDetections(machineID int64, dets []VpsToolDetection) error {
	if len(dets) == 0 {
		_, err := d.Exec(`DELETE FROM vps_tool_detections WHERE machine_id=$1`, machineID)
		return err
	}
	for _, det := range dets {
		if _, err := d.Exec(`
INSERT INTO vps_tool_detections(machine_id, tool_name, installed, version, detected_at)
VALUES ($1,$2,$3,$4,now())
ON CONFLICT (machine_id, tool_name) DO UPDATE SET
	installed=$3, version=$4, detected_at=now()`,
			machineID, det.ToolName, det.Installed, det.Version); err != nil {
			return err
		}
	}
	return nil
}

// ListVpsToolDetections 列某台机器的最新检测矩阵(按工具名排序,稳定展示)。
func (d *DB) ListVpsToolDetections(machineID int64) ([]*VpsToolDetection, error) {
	rows, err := d.Query(`SELECT tool_name, installed, version, detected_at
FROM vps_tool_detections WHERE machine_id=$1 ORDER BY tool_name`, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*VpsToolDetection{}
	for rows.Next() {
		var det VpsToolDetection
		if err := rows.Scan(&det.ToolName, &det.Installed, &det.Version, &det.DetectedAt); err != nil {
			return nil, err
		}
		out = append(out, &det)
	}
	return out, rows.Err()
}

// ---- 仪表盘 ----

// VpsDashboardSummary 是仪表盘顶部的总览数字。
type VpsDashboardSummary struct {
	Total           int `json:"total"`
	Online          int `json:"online"`
	Offline         int `json:"offline"`
	Busy            int `json:"busy"`
	Maintenance     int `json:"maintenance"`
	ExecutionsToday int `json:"executions_today"`
	ActiveTasks     int `json:"active_tasks"`
}

// VpsDashboardSummary 一次取齐仪表盘总览。
func (d *DB) VpsDashboardSummary() (*VpsDashboardSummary, error) {
	s := &VpsDashboardSummary{}
	err := d.QueryRow(`
SELECT COUNT(*),
	COUNT(*) FILTER (WHERE status='online'),
	COUNT(*) FILTER (WHERE status='offline'),
	COUNT(*) FILTER (WHERE status='busy'),
	COUNT(*) FILTER (WHERE status='maintenance')
FROM vps_machines`).Scan(&s.Total, &s.Online, &s.Offline, &s.Busy, &s.Maintenance)
	if err != nil {
		return nil, err
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM vps_executions WHERE created_at >= date_trunc('day', now())`).
		Scan(&s.ExecutionsToday); err != nil {
		return nil, err
	}
	// 活跃任务数 = 当前被占用的机器数(一机一任务,部分唯一索引保证)。
	if err := d.QueryRow(`SELECT COUNT(*) FROM vps_machines WHERE assigned_task_id IS NOT NULL`).
		Scan(&s.ActiveTasks); err != nil {
		return nil, err
	}
	return s, nil
}

// VpsSeriesPoint 是仪表盘的一个小时桶。
type VpsSeriesPoint struct {
	Ts    time.Time `json:"ts"`
	Count int       `json:"count"`
}

// VpsExecutionSeries 按小时桶统计窗口内的命令执行量。
func (d *DB) VpsExecutionSeries(since time.Time) ([]*VpsSeriesPoint, error) {
	rows, err := d.Query(`
SELECT date_trunc('hour', created_at) AS bucket, COUNT(*)
FROM vps_executions WHERE created_at >= $1
GROUP BY bucket ORDER BY bucket`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*VpsSeriesPoint{}
	for rows.Next() {
		var p VpsSeriesPoint
		if err := rows.Scan(&p.Ts, &p.Count); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}
