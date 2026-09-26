package db

// C2 中心(c2_listeners / c2_sessions / c2_tasks / c2_transfers / c2_loot /
// c2_payloads):自研轻量 beacon 的指挥控制面。契约见 C2_CONTRACTS.md。
//
// 纪律(照 vps.go):
//   - c2_listeners.enc_key 与 c2_loot(credential/hash 类).content 是 secretbox 密文;
//     明文只允许在 server 层内存中出现,任何接口响应都不回显。本层只搬运密文:
//     GetC2ListenerKey 返回原始密文(server 侧解封),ListC2Loot 在 SQL 里就把
//     content 置空串、只带 has_content 标记,完整密文只有 GetC2Loot 单行查询给。
//   - c2_sessions.meta 是 JSONB,扫出来统一转 map[string]any 且永不返回 nil。
//   - ListC2Sessions / ListC2Loot 的 total 在 WHERE(全部筛选)之后、LIMIT 之前算。
//   - 可空 BIGINT(task_id/finding_id)一律 COALESCE(x,0):0 = 无。
//   - 列表响应永不返回 nil slice。
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

// 会话三态:online(心跳正常)/offline(超时未归)/dead(平台终止)。
const (
	C2SessionStatusOnline  = "online"
	C2SessionStatusOffline = "offline"
	C2SessionStatusDead    = "dead"
)

// ValidC2SessionStatus 校验会话状态。
func ValidC2SessionStatus(s string) bool {
	switch s {
	case C2SessionStatusOnline, C2SessionStatusOffline, C2SessionStatusDead:
		return true
	}
	return false
}

// 任务状态:queued(待发)/sent(已发未归)/done/error。
const (
	C2TaskStatusQueued = "queued"
	C2TaskStatusSent   = "sent"
	C2TaskStatusDone   = "done"
	C2TaskStatusError  = "error"
)

// ValidC2TaskStatus 校验任务状态。
func ValidC2TaskStatus(s string) bool {
	switch s {
	case C2TaskStatusQueued, C2TaskStatusSent, C2TaskStatusDone, C2TaskStatusError:
		return true
	}
	return false
}

// 任务类型:shell/files_list/upload/download/loot/info。
const (
	C2TaskKindShell     = "shell"
	C2TaskKindFilesList = "files_list"
	C2TaskKindUpload    = "upload"
	C2TaskKindDownload  = "download"
	C2TaskKindLoot      = "loot"
	C2TaskKindInfo      = "info"
)

// ValidC2TaskKind 校验任务类型。
func ValidC2TaskKind(k string) bool {
	switch k {
	case C2TaskKindShell, C2TaskKindFilesList, C2TaskKindUpload, C2TaskKindDownload, C2TaskKindLoot, C2TaskKindInfo:
		return true
	}
	return false
}

// 任务操作者:user(人在页面下发)/agent(AI 工具下发)。
const (
	C2TaskActorUser  = "user"
	C2TaskActorAgent = "agent"
)

// ValidC2TaskActor 校验任务操作者。
func ValidC2TaskActor(a string) bool {
	return a == C2TaskActorUser || a == C2TaskActorAgent
}

// 传输方向与状态。
const (
	C2TransferUp   = "up"   // 平台 → 目标
	C2TransferDown = "down" // 目标 → 平台
)

// ValidC2TransferDirection 校验传输方向。
func ValidC2TransferDirection(d string) bool {
	return d == C2TransferUp || d == C2TransferDown
}

// ValidC2TransferStatus 校验传输状态。
func ValidC2TransferStatus(s string) bool {
	switch s {
	case "queued", "running", "done", "error":
		return true
	}
	return false
}

// 战利品类型。credential/hash 的 content 密文存储。
const (
	C2LootKindCredential = "credential"
	C2LootKindHash       = "hash"
	C2LootKindFile       = "file"
	C2LootKindScreenshot = "screenshot"
	C2LootKindOther      = "other"
)

// ValidC2LootKind 校验战利品类型。
func ValidC2LootKind(k string) bool {
	switch k {
	case C2LootKindCredential, C2LootKindHash, C2LootKindFile, C2LootKindScreenshot, C2LootKindOther:
		return true
	}
	return false
}

// LootContentEncrypted 判断该战利品类型的 content 是否密文存储(解密只在 server 层)。
func LootContentEncrypted(kind string) bool {
	return kind == C2LootKindCredential || kind == C2LootKindHash
}

// 载荷格式与目标平台。
const (
	C2PayloadFormatPs1   = "ps1"
	C2PayloadFormatBash  = "bash"
	C2PayloadFormatPy    = "py"
	C2PayloadFormatGoSrc = "gosrc"
)

// ValidC2PayloadFormat 校验载荷格式。
func ValidC2PayloadFormat(f string) bool {
	switch f {
	case C2PayloadFormatPs1, C2PayloadFormatBash, C2PayloadFormatPy, C2PayloadFormatGoSrc:
		return true
	}
	return false
}

// ValidC2PayloadTarget 校验载荷目标平台。
func ValidC2PayloadTarget(t string) bool {
	switch t {
	case "windows", "linux", "macos", "cross":
		return true
	}
	return false
}

// 监听器载体(二期 §12):local=平台本机(一期行为);vps=以 VPS 机器为出入口,
// 平台经 SSH 远程转发把 VPS:remote_port 的流量转回本机监听器。
const (
	C2ListenerHostKindLocal = "local"
	C2ListenerHostKindVPS   = "vps"
)

// ValidC2ListenerHostKind 校验监听器载体类型。
func ValidC2ListenerHostKind(k string) bool {
	return k == C2ListenerHostKindLocal || k == C2ListenerHostKindVPS
}

// 监听器传输协议(三期 §18):http(一期)|ws|tcp|kcp|oss。https 与 DNS 系四期。
const (
	C2ProtoHTTP = "http"
	C2ProtoWS   = "ws"
	C2ProtoTCP  = "tcp"
	C2ProtoKCP  = "kcp"
	C2ProtoOSS  = "oss"
)

// ValidC2ListenerProto 校验监听器协议(空 = http 一期兼容)。
func ValidC2ListenerProto(p string) bool {
	switch p {
	case "", C2ProtoHTTP, C2ProtoWS, C2ProtoTCP, C2ProtoKCP, C2ProtoOSS:
		return true
	}
	return false
}

// C2ListenerTransportConfigured 判断该协议是否需要传输配置(oss 必需,kcp 可选 psk)。
func C2ListenerTransportConfigured(proto string) bool {
	return proto == C2ProtoOSS || proto == C2ProtoKCP
}

// ---- 监听器 ----

// C2Listener 是 beacon 回连入口(不含 enc_key,可直接序列化为响应)。
type C2Listener struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Proto    string `json:"proto"`
	BindHost string `json:"bind_host"`
	// Port 0 = 启动时自动分配;实际监听地址以运行时为准。
	Port     int    `json:"port"`
	URLPath  string `json:"url_path"`
	Callback string `json:"callback"`
	Enabled  bool   `json:"enabled"`
	// SessionCount 是该监听器当前 online 会话数(列表页「在线会话数」列)。
	SessionCount int64  `json:"session_count"`
	Notes        string `json:"notes"`
	// 二期载体字段(契约 §12):vps 载体时 VPSMachineID>0 且 RemotePort>0,
	// Callback 可空(前端用机器 host 拼)。VPSMachineName 由 vps_machines 带出
	//(local 载体或机器已删为空串),仅展示用。
	HostKind       string `json:"host_kind"`
	VPSMachineID   int64  `json:"vps_machine_id"`
	RemotePort     int    `json:"remote_port"`
	VPSMachineName string `json:"vps_machine_name,omitempty"`
	// 三期(§18):oss/kcp 的传输配置是否已设置(密文永不回显,仅此布尔)。
	TransportConfigured bool `json:"transport_configured"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// c2ListenerCols 是监听器行的取列;enc_key/transport_config_enc 永不出现
// (分别单独 GetC2ListenerKey/GetC2ListenerTransportConfig)。机器名用相关子查询
// 带出(List/Get 共用同一取列串,口径一致)。
const c2ListenerCols = `l.id, l.name, l.proto, l.bind_host, l.port, l.url_path, l.callback,
	l.enabled,
	(SELECT COUNT(*) FROM c2_sessions cs WHERE cs.listener_id = l.id AND cs.status = 'online'),
	l.notes, l.created_at, l.updated_at,
	COALESCE(l.host_kind,'local'), COALESCE(l.vps_machine_id,0), l.remote_port,
	COALESCE((SELECT vm.name FROM vps_machines vm WHERE vm.id = l.vps_machine_id), ''),
	(l.transport_config_enc IS NOT NULL AND length(l.transport_config_enc) > 0)`

func scanC2Listener(sc interface{ Scan(...any) error }) (*C2Listener, error) {
	var l C2Listener
	if err := sc.Scan(&l.ID, &l.Name, &l.Proto, &l.BindHost, &l.Port, &l.URLPath, &l.Callback,
		&l.Enabled, &l.SessionCount, &l.Notes, &l.CreatedAt, &l.UpdatedAt,
		&l.HostKind, &l.VPSMachineID, &l.RemotePort, &l.VPSMachineName,
		&l.TransportConfigured); err != nil {
		return nil, err
	}
	return &l, nil
}

// C2ListenerInput 是新建/编辑监听器的输入。EncKeySealed 是 server 层封好的
// secretbox 密文(创建必填;编辑传 nil = 不改密钥——监听器密钥一经生成不可换,
// 换了所有已投递载荷全部失效)。TransportConfigEnc 同法(三期 §18):oss 的
// endpoint/bucket/AK/SK 与 kcp 的 psk 密封 JSON;编辑传 nil = 不改配置。
type C2ListenerInput struct {
	Name         string
	BindHost     string
	Port         int
	URLPath      string
	Callback     string
	EncKeySealed []byte
	Notes        string
	// 二期载体字段(契约 §12):HostKind 空=local;vps 时 VPSMachineID>0 且
	// RemotePort>0,local 时二者强制归零。
	HostKind     string
	VPSMachineID int64
	RemotePort   int
	// 三期(§18):proto 空=http;TransportConfigEnc nil=不改(创建时为 NULL)。
	Proto              string
	TransportConfigEnc []byte
}

// normalizeC2ListenerInput 填默认值并校验。
func normalizeC2ListenerInput(in *C2ListenerInput) error {
	in.Name = strings.TrimSpace(in.Name)
	in.BindHost = strings.TrimSpace(in.BindHost)
	in.URLPath = strings.Trim(strings.TrimSpace(in.URLPath), "/")
	in.Callback = strings.TrimSpace(in.Callback)
	if in.Name == "" {
		return fmt.Errorf("监听器名称必填")
	}
	if in.Proto == "" {
		in.Proto = C2ProtoHTTP
	}
	if !ValidC2ListenerProto(in.Proto) {
		return fmt.Errorf("无效协议: %q(仅 http/ws/tcp/kcp/oss)", in.Proto)
	}
	if in.BindHost == "" {
		in.BindHost = "0.0.0.0"
	}
	if in.Port < 0 || in.Port > 65535 {
		return fmt.Errorf("端口无效(0=自动分配,1-65535)")
	}
	if in.HostKind == "" {
		in.HostKind = C2ListenerHostKindLocal
	}
	if !ValidC2ListenerHostKind(in.HostKind) {
		return fmt.Errorf("无效载体类型: %q(仅 local/vps)", in.HostKind)
	}
	if in.HostKind == C2ListenerHostKindVPS {
		if in.VPSMachineID <= 0 {
			return fmt.Errorf("vps 载体必须指定机器(vps_machine_id)")
		}
		if in.RemotePort <= 0 || in.RemotePort > 65535 {
			return fmt.Errorf("vps 载体必须指定远端端口 remote_port(1-65535)")
		}
	} else {
		in.VPSMachineID = 0
		in.RemotePort = 0
	}
	return nil
}

// CreateC2Listener 建监听器(密钥密文必填,由 server 层生成并加密)。
func (d *DB) CreateC2Listener(in C2ListenerInput) (int64, error) {
	if err := normalizeC2ListenerInput(&in); err != nil {
		return 0, err
	}
	if len(in.EncKeySealed) == 0 {
		return 0, fmt.Errorf("enc_key 密文必填(由 server 层生成)")
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO c2_listeners(name, bind_host, port, url_path, callback, enc_key, notes,
	host_kind, vps_machine_id, remote_port, proto, transport_config_enc)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id`,
		in.Name, in.BindHost, in.Port, in.URLPath, in.Callback, in.EncKeySealed, in.Notes,
		in.HostKind, nullableID(in.VPSMachineID), in.RemotePort, in.Proto, in.TransportConfigEnc).Scan(&id)
	return id, err
}

// ListC2Listeners 列全部监听器(新者在前,带 online 会话数)。
func (d *DB) ListC2Listeners() ([]*C2Listener, error) {
	rows, err := d.Query(`SELECT ` + c2ListenerCols + ` FROM c2_listeners l ORDER BY l.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*C2Listener{}
	for rows.Next() {
		l, err := scanC2Listener(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// GetC2Listener 读单个监听器(不存在返回 nil, nil)。
func (d *DB) GetC2Listener(id int64) (*C2Listener, error) {
	l, err := scanC2Listener(d.QueryRow(`SELECT `+c2ListenerCols+` FROM c2_listeners l WHERE l.id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return l, nil
}

// UpdateC2Listener 更新监听器普通字段(整体覆盖,前端整表单提交);
// EncKeySealed 为 nil = 不改密钥;TransportConfigEnc 为 nil = 不改传输配置。
func (d *DB) UpdateC2Listener(id int64, in C2ListenerInput) error {
	if err := normalizeC2ListenerInput(&in); err != nil {
		return err
	}
	_, err := d.Exec(`UPDATE c2_listeners SET
	name=$2, bind_host=$3, port=$4, url_path=$5, callback=$6, notes=$7,
	host_kind=$8, vps_machine_id=$9, remote_port=$10, proto=$11,
	enc_key = CASE WHEN $12::boolean THEN $13 ELSE enc_key END,
	transport_config_enc = CASE WHEN $14::boolean THEN $15 ELSE transport_config_enc END
WHERE id=$1`,
		id, in.Name, in.BindHost, in.Port, in.URLPath, in.Callback, in.Notes,
		in.HostKind, nullableID(in.VPSMachineID), in.RemotePort, in.Proto,
		in.EncKeySealed != nil, in.EncKeySealed,
		in.TransportConfigEnc != nil, in.TransportConfigEnc)
	return err
}

// DeleteC2Listener 删监听器(有会话时由 server 层先 409,本层不做级联)。
func (d *DB) DeleteC2Listener(id int64) (bool, error) {
	res, err := d.Exec(`DELETE FROM c2_listeners WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetC2ListenerEnabled 启用/停用(enabled 监听器随进程启动自动拉起)。
func (d *DB) SetC2ListenerEnabled(id int64, enabled bool) error {
	_, err := d.Exec(`UPDATE c2_listeners SET enabled=$2 WHERE id=$1`, id, enabled)
	return err
}

// CountC2ListenersForMachine 统计以该 VPS 机器为载体的监听器数(删除机器前 409 用)。
func (d *DB) CountC2ListenersForMachine(machineID int64) (int, error) {
	var n int
	err := d.QueryRow(`SELECT COUNT(*) FROM c2_listeners WHERE vps_machine_id=$1`, machineID).Scan(&n)
	return n, err
}

// GetC2ListenerKey 读监听器密钥密文(仅 server 层解封用,绝不进任何响应)。
func (d *DB) GetC2ListenerKey(id int64) ([]byte, error) {
	var sealed []byte
	err := d.QueryRow(`SELECT enc_key FROM c2_listeners WHERE id=$1`, id).Scan(&sealed)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return sealed, err
}

// GetC2ListenerTransportConfig 读监听器传输配置密文(三期 §18;仅 server 层解封
// 用——oss 启动/载荷渲染要 endpoint/bucket/AK/SK,kcp 要 psk,绝不进任何响应)。
// 未配置返回 nil, nil。
func (d *DB) GetC2ListenerTransportConfig(id int64) ([]byte, error) {
	var sealed []byte
	err := d.QueryRow(`SELECT transport_config_enc FROM c2_listeners WHERE id=$1`, id).Scan(&sealed)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if sealed == nil {
		return nil, nil
	}
	return sealed, nil
}

// C2ListenerCounts 一次取齐(启用数, 总数),总览用。
func (d *DB) C2ListenerCounts() (enabled, total int64, err error) {
	err = d.QueryRow(`SELECT COUNT(*) FILTER (WHERE enabled), COUNT(*) FROM c2_listeners`).
		Scan(&enabled, &total)
	return enabled, total, err
}

// ---- 会话 ----

// C2Session 是一台上线主机。sid 是 beacon 持有的会话令牌;
// listener_id+beacon_uid 幂等(同一主机重连=同一会话复活)。
type C2Session struct {
	ID         int64  `json:"id"`
	ListenerID int64  `json:"listener_id"`
	BeaconUID  string `json:"beacon_uid"`
	SID        string `json:"sid"`
	Hostname   string `json:"hostname"`
	Username   string `json:"username"`
	Elevated   bool   `json:"elevated"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	LocalIPs   string `json:"local_ips"`
	PID        int    `json:"pid"`
	Process    string `json:"process"`
	SleepSec   int    `json:"sleep_sec"`
	JitterPct  int    `json:"jitter_pct"`
	// TaskID 是绑定的平台任务 id;0 = 未绑定。
	TaskID int64  `json:"task_id"`
	Status string `json:"status"`
	Labels string `json:"labels"`
	// Meta 是 beacon 上报的扩展信息,永不 nil(可直接序列化)。
	// 三期(§18)正向连接会话在此标注 direction=bind。
	Meta map[string]any `json:"meta"`
	// Transport 是会话承载传输(http/ws/tcp/kcp/oss;一期数据回填 http)。
	Transport string    `json:"transport"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// c2SessionCols 是会话行的取列;可空 task_id 用 COALESCE 归零,transport 兜底 http。
const c2SessionCols = `s.id, s.listener_id, s.beacon_uid, s.sid, s.hostname, s.username, s.elevated,
	s.os, s.arch, s.local_ips, s.pid, s.process, s.sleep_sec, s.jitter_pct,
	COALESCE(s.task_id,0), s.status, s.labels, s.meta, s.first_seen, s.last_seen,
	COALESCE(s.transport,'http')`

func scanC2Session(sc interface{ Scan(...any) error }) (*C2Session, error) {
	var m C2Session
	var meta []byte
	if err := sc.Scan(&m.ID, &m.ListenerID, &m.BeaconUID, &m.SID, &m.Hostname, &m.Username,
		&m.Elevated, &m.OS, &m.Arch, &m.LocalIPs, &m.PID, &m.Process,
		&m.SleepSec, &m.JitterPct, &m.TaskID, &m.Status, &m.Labels, &meta,
		&m.FirstSeen, &m.LastSeen, &m.Transport); err != nil {
		return nil, err
	}
	m.Meta = map[string]any{}
	if len(meta) > 0 {
		_ = json.Unmarshal(meta, &m.Meta)
		if m.Meta == nil {
			m.Meta = map[string]any{}
		}
	}
	return &m, nil
}

// C2SessionInput 是 beacon 注册/复活的会话输入(由 manager 从 reg 信封映射)。
// Transport/Direction 是三期(§18)追加:空 = 保持原值;Direction 仅正向连接
// 会话带 "bind"(写入 meta.direction)。
type C2SessionInput struct {
	ListenerID int64
	BeaconUID  string
	SID        string
	Hostname   string
	Username   string
	Elevated   bool
	OS         string
	Arch       string
	LocalIPs   string
	PID        int
	Process    string
	Transport  string
	Direction  string
}

// normalizeC2SessionInput 校验注册输入。
func normalizeC2SessionInput(in *C2SessionInput) error {
	if in.ListenerID <= 0 {
		return fmt.Errorf("listener_id 必填")
	}
	if strings.TrimSpace(in.SID) == "" {
		return fmt.Errorf("sid 必填")
	}
	return nil
}

// UpsertC2Session 按 listener_id+beacon_uid 幂等注册:存在则刷新主机信息、换绑
// 新 sid 并置回 online(复活),不存在则新建。sid 全局唯一:极端情况(uid 变了但
// sid 撞上历史行)也按复活处理,保证唯一索引不被打破。
// 三期(§18):Transport 空 = 保持原值(心跳路径);Direction 非空(仅 "bind")
// 合并进 meta.direction,空 = 不动 meta。
func (d *DB) UpsertC2Session(in C2SessionInput) (int64, error) {
	if err := normalizeC2SessionInput(&in); err != nil {
		return 0, err
	}
	// 注意:in.Transport 原样透传(空 = 保持原值,心跳路径);插入时的 http 兜底
	// 由 INSERT 的 COALESCE(NULLIF($12,''),'http') 完成,这里不做归一化。
	// 1) 同一 beacon(listener_id+beacon_uid)复活。
	var id int64
	err := d.QueryRow(`UPDATE c2_sessions SET
	sid=$3, hostname=$4, username=$5, elevated=$6, os=$7, arch=$8, local_ips=$9,
	pid=$10, process=$11, status='online', last_seen=now(),
	transport = CASE WHEN $12::text <> '' THEN $12 ELSE transport END,
	meta = CASE WHEN $13::text <> '' THEN c2_sessions.meta || jsonb_build_object('direction', $13::text) ELSE meta END
WHERE listener_id=$1 AND beacon_uid=$2 RETURNING id`,
		in.ListenerID, in.BeaconUID, in.SID, in.Hostname, in.Username, in.Elevated,
		in.OS, in.Arch, in.LocalIPs, in.PID, in.Process,
		in.Transport, in.Direction).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	// 2) 新会话;sid 撞唯一索引时同样按复活更新。
	err = d.QueryRow(`INSERT INTO c2_sessions
	(listener_id, beacon_uid, sid, hostname, username, elevated, os, arch, local_ips, pid, process, status, transport, meta)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'online',
	COALESCE(NULLIF($12,''),'http'),
	CASE WHEN $13::text <> '' THEN jsonb_build_object('direction', $13::text) ELSE '{}'::jsonb END)
ON CONFLICT (sid) DO UPDATE SET
	hostname=EXCLUDED.hostname, username=EXCLUDED.username, elevated=EXCLUDED.elevated,
	os=EXCLUDED.os, arch=EXCLUDED.arch, local_ips=EXCLUDED.local_ips,
	pid=EXCLUDED.pid, process=EXCLUDED.process, status='online', last_seen=now(),
	transport = CASE WHEN $12::text <> '' THEN $12 ELSE c2_sessions.transport END,
	meta = CASE WHEN $13::text <> '' THEN c2_sessions.meta || jsonb_build_object('direction', $13::text) ELSE c2_sessions.meta END
RETURNING id`,
		in.ListenerID, in.BeaconUID, in.SID, in.Hostname, in.Username, in.Elevated,
		in.OS, in.Arch, in.LocalIPs, in.PID, in.Process,
		in.Transport, in.Direction).Scan(&id)
	return id, err
}

// GetC2SessionBySID 按会话令牌读会话(beacon 信道路径用;不存在返回 nil, nil)。
func (d *DB) GetC2SessionBySID(sid string) (*C2Session, error) {
	m, err := scanC2Session(d.QueryRow(`SELECT `+c2SessionCols+` FROM c2_sessions s WHERE s.sid=$1`, sid))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}

// GetC2Session 按数字 id 读会话(不存在返回 nil, nil)。
func (d *DB) GetC2Session(id int64) (*C2Session, error) {
	m, err := scanC2Session(d.QueryRow(`SELECT `+c2SessionCols+` FROM c2_sessions s WHERE s.id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}

// C2SessionFilter 是会话列表的筛选条件。零值 = 不限。
type C2SessionFilter struct {
	Status     string // online/offline/dead
	ListenerID int64  // 0 = 全部监听器
	TaskID     int64  // 0 = 不限;>0 只看绑定该平台任务的会话
	// CompanyID 经 task_id → tasks → task_scope(kind='company') EXISTS 过滤;
	// 0 = 不限。
	CompanyID int64
	// Q 模糊匹配主机名/用户名/内网 IP。
	Q      string
	Limit  int
	Offset int
}

// ListC2Sessions 分页检索会话(按最近心跳倒序)。total 在全部筛选之后算。
func (d *DB) ListC2Sessions(f C2SessionFilter) ([]*C2Session, int, error) {
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
		if !ValidC2SessionStatus(f.Status) {
			return nil, 0, fmt.Errorf("无效状态: %q", f.Status)
		}
		add("s.status = $%d", f.Status)
	}
	if f.ListenerID > 0 {
		add("s.listener_id = $%d", f.ListenerID)
	}
	if f.TaskID > 0 {
		add("s.task_id = $%d", f.TaskID)
	}
	if f.CompanyID > 0 {
		add(`EXISTS (SELECT 1 FROM task_scope ts
WHERE ts.task_id = s.task_id AND ts.kind = 'company' AND ts.company_id = $%d)`, f.CompanyID)
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		where = append(where, fmt.Sprintf(
			"(s.hostname ILIKE $%d OR s.username ILIKE $%d OR s.local_ips ILIKE $%d)",
			len(args)+1, len(args)+1, len(args)+1))
		args = append(args, "%"+q+"%")
	}
	kw := strings.Join(where, " AND ")

	// total 必须在 WHERE 之后算(分页依赖它)。
	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM c2_sessions s WHERE `+kw, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT ` + c2SessionCols + ` FROM c2_sessions s WHERE ` + kw +
		` ORDER BY s.last_seen DESC` + fmt.Sprintf(` LIMIT $%d OFFSET $%d`, len(args)+1, len(args)+2)
	args = append(args, f.Limit, f.Offset)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*C2Session{}
	for rows.Next() {
		m, err := scanC2Session(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, m)
	}
	return out, total, rows.Err()
}

// SetC2SessionSleep 调整会话休眠(beacon 下次心跳按响应 meta 覆盖本地 sleep)。
func (d *DB) SetC2SessionSleep(sid string, sleepSec, jitterPct int) error {
	if sleepSec < 1 || sleepSec > 3600 {
		return fmt.Errorf("sleep_sec 必须在 1-3600 之间")
	}
	if jitterPct < 0 || jitterPct > 90 {
		return fmt.Errorf("jitter_pct 必须在 0-90 之间")
	}
	_, err := d.Exec(`UPDATE c2_sessions SET sleep_sec=$2, jitter_pct=$3 WHERE sid=$1`,
		sid, sleepSec, jitterPct)
	return err
}

// SetC2SessionStatusBySID 按令牌改会话状态(kill=置 dead,beacon 下次心跳自行退出)。
func (d *DB) SetC2SessionStatusBySID(sid string, status string) error {
	if !ValidC2SessionStatus(status) {
		return fmt.Errorf("无效状态: %q", status)
	}
	_, err := d.Exec(`UPDATE c2_sessions SET status=$2 WHERE sid=$1`, sid, status)
	return err
}

// BindC2SessionTask 绑定/解绑平台任务(taskID 0 = 解绑)。
func (d *DB) BindC2SessionTask(id, taskID int64) error {
	_, err := d.Exec(`UPDATE c2_sessions SET task_id=$2 WHERE id=$1`, id, nullableID(taskID))
	return err
}

// SweepOfflineSessions 把「心跳超时」的 online 会话批量置 offline:
// last_seen < now() - sleep_sec*(1+graceMultiplier) 秒。返回下线数量。
func (d *DB) SweepOfflineSessions(graceMultiplier int) (int64, error) {
	if graceMultiplier < 0 {
		graceMultiplier = 0
	}
	res, err := d.Exec(`UPDATE c2_sessions SET status='offline'
WHERE status='online'
  AND last_seen < now() - make_interval(secs => (sleep_sec * (1 + $1))::double precision)`,
		graceMultiplier)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountC2SessionsByListener 数监听器下的全部会话(删监听器前的 409 判定)。
func (d *DB) CountC2SessionsByListener(listenerID int64) (int, error) {
	var n int
	err := d.QueryRow(`SELECT COUNT(*) FROM c2_sessions WHERE listener_id=$1`, listenerID).Scan(&n)
	return n, err
}

// CountC2SessionsByStatus 一次取齐三态计数,总览用。
func (d *DB) CountC2SessionsByStatus() (online, offline, dead int64, err error) {
	err = d.QueryRow(`SELECT
	COUNT(*) FILTER (WHERE status='online'),
	COUNT(*) FILTER (WHERE status='offline'),
	COUNT(*) FILTER (WHERE status='dead')
FROM c2_sessions`).Scan(&online, &offline, &dead)
	return online, offline, dead, err
}

// ---- 任务 ----

// C2Task 是一条下发任务(命令/文件/采集)。大输出(>256KB)落盘,表内只留预览。
type C2Task struct {
	ID         int64      `json:"id"`
	SessionID  int64      `json:"session_id"`
	Kind       string     `json:"kind"`
	Input      string     `json:"input"`
	Output     string     `json:"output"`
	OutputPath string     `json:"output_path"`
	OutputSize int64      `json:"output_size"`
	Status     string     `json:"status"`
	Actor      string     `json:"actor"`
	CreatedBy  string     `json:"created_by"`
	Error      string     `json:"error"`
	CreatedAt  time.Time  `json:"created_at"`
	SentAt     *time.Time `json:"sent_at,omitempty"`
	DoneAt     *time.Time `json:"done_at,omitempty"`
}

const c2TaskCols = `id, session_id, kind, input, output, output_path, output_size,
	status, actor, created_by, error, created_at, sent_at, done_at`

func scanC2Task(sc interface{ Scan(...any) error }) (*C2Task, error) {
	var t C2Task
	var sentAt, doneAt sql.NullTime
	if err := sc.Scan(&t.ID, &t.SessionID, &t.Kind, &t.Input, &t.Output, &t.OutputPath,
		&t.OutputSize, &t.Status, &t.Actor, &t.CreatedBy, &t.Error, &t.CreatedAt,
		&sentAt, &doneAt); err != nil {
		return nil, err
	}
	if sentAt.Valid {
		v := sentAt.Time
		t.SentAt = &v
	}
	if doneAt.Valid {
		v := doneAt.Time
		t.DoneAt = &v
	}
	return &t, nil
}

// C2TaskInput 是建任务的输入(Input 语义随 kind:shell=命令文本;
// files_list=目录路径;upload/download=JSON {transfer_id,remote_path})。
type C2TaskInput struct {
	SessionID int64
	Kind      string
	Input     string
	Actor     string
	CreatedBy string
}

// CreateC2Task 入队一条任务(初始 queued)。
func (d *DB) CreateC2Task(in C2TaskInput) (int64, error) {
	if in.SessionID <= 0 {
		return 0, fmt.Errorf("session_id 必填")
	}
	if !ValidC2TaskKind(in.Kind) {
		return 0, fmt.Errorf("无效任务类型: %q", in.Kind)
	}
	if in.Actor == "" {
		in.Actor = C2TaskActorUser
	}
	if !ValidC2TaskActor(in.Actor) {
		return 0, fmt.Errorf("无效操作者: %q(仅 user/agent)", in.Actor)
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO c2_tasks(session_id, kind, input, actor, created_by)
VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		in.SessionID, in.Kind, in.Input, in.Actor, in.CreatedBy).Scan(&id)
	return id, err
}

// TakeQueuedTasks 原子批量取走某会话的排队任务(queued→sent),返回被取走的。
// FOR UPDATE SKIP LOCKED:多 worker/多监听器并发轮询互不阻塞、绝不重复下发。
func (d *DB) TakeQueuedTasks(sessionID int64, limit int) ([]*C2Task, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	rows, err := d.Query(`UPDATE c2_tasks SET status='sent', sent_at=now()
WHERE id IN (
	SELECT id FROM c2_tasks WHERE session_id=$1 AND status='queued' ORDER BY id LIMIT $2
	FOR UPDATE SKIP LOCKED
) RETURNING `+c2TaskCols, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*C2Task{}
	for rows.Next() {
		t, err := scanC2Task(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// FinishC2Task 回填任务终态(st ∈ done|error);output 是回传预览,
// 大输出发 manager 落盘后经 outPath/outSize 记账。
func (d *DB) FinishC2Task(id int64, status, output, outPath string, outSize int64, errText string) error {
	if status != C2TaskStatusDone && status != C2TaskStatusError {
		return fmt.Errorf("任务终态只能是 done/error: %q", status)
	}
	_, err := d.Exec(`UPDATE c2_tasks SET
	status=$2, output=$3, output_path=$4, output_size=$5, error=$6, done_at=now()
WHERE id=$1`, id, status, output, outPath, outSize, errText)
	return err
}

// GetC2Task 读单条任务(不存在返回 nil, nil)。
func (d *DB) GetC2Task(id int64) (*C2Task, error) {
	t, err := scanC2Task(d.QueryRow(`SELECT `+c2TaskCols+` FROM c2_tasks WHERE id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

// ListC2Tasks 增量拉取某会话的任务(id 升序)。
//
// 语义(集成补充,已钉死):after_id 只越过【已终态】(done/error)的任务;
// 排队/下发中(queued/sent)的任务要重复返回——前端 2s 轮询靠这个观察状态迁移。
func (d *DB) ListC2Tasks(sessionID, afterID int64, limit int) ([]*C2Task, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := d.Query(`SELECT `+c2TaskCols+` FROM c2_tasks
WHERE session_id=$1 AND (id > $2 OR status IN ('queued','sent'))
ORDER BY id ASC LIMIT $3`, sessionID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*C2Task{}
	for rows.Next() {
		t, err := scanC2Task(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListRecentC2Tasks 取最近的任务(总览;跨会话)。
func (d *DB) ListRecentC2Tasks(limit int) ([]*C2Task, error) {
	if limit <= 0 || limit > 200 {
		limit = 10
	}
	rows, err := d.Query(`SELECT `+c2TaskCols+` FROM c2_tasks ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*C2Task{}
	for rows.Next() {
		t, err := scanC2Task(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CountQueuedC2Tasks 数全平台排队任务(总览)。
func (d *DB) CountQueuedC2Tasks() (int, error) {
	var n int
	err := d.QueryRow(`SELECT COUNT(*) FROM c2_tasks WHERE status='queued'`).Scan(&n)
	return n, err
}

// CountC2TasksByStatus 按状态数某会话的任务(会话档案/agent 工具用)。
func (d *DB) CountC2TasksByStatus(sessionID int64) (queued, sent, done, failed int, err error) {
	err = d.QueryRow(`SELECT
	COUNT(*) FILTER (WHERE status='queued'),
	COUNT(*) FILTER (WHERE status='sent'),
	COUNT(*) FILTER (WHERE status='done'),
	COUNT(*) FILTER (WHERE status='error')
FROM c2_tasks WHERE session_id=$1`, sessionID).
		Scan(&queued, &sent, &done, &failed)
	return queued, sent, done, failed, err
}

// ---- 文件传输 ----

// C2Transfer 是一次分块文件传输记录。local_path 是平台侧 blob/产物路径
// (只由服务端构造,永不进任何响应)。
type C2Transfer struct {
	ID         int64      `json:"id"`
	SessionID  int64      `json:"session_id"`
	TaskID     int64      `json:"task_id"`
	Direction  string     `json:"direction"`
	RemotePath string     `json:"remote_path"`
	Size       int64      `json:"size"`
	DoneSize   int64      `json:"done_size"`
	Status     string     `json:"status"`
	Error      string     `json:"error"`
	CreatedAt  time.Time  `json:"created_at"`
	DoneAt     *time.Time `json:"done_at,omitempty"`

	// LocalPath 平台侧落盘路径,json:"-" 永不序列化(防路径泄漏/穿越探测)。
	LocalPath string `json:"-"`
}

const c2TransferCols = `t.id, t.session_id, COALESCE(t.task_id,0), t.direction, t.remote_path,
	t.size, t.done_size, t.status, t.local_path, t.error, t.created_at, t.done_at`

func scanC2Transfer(sc interface{ Scan(...any) error }) (*C2Transfer, error) {
	var tr C2Transfer
	var doneAt sql.NullTime
	if err := sc.Scan(&tr.ID, &tr.SessionID, &tr.TaskID, &tr.Direction, &tr.RemotePath,
		&tr.Size, &tr.DoneSize, &tr.Status, &tr.LocalPath, &tr.Error, &tr.CreatedAt,
		&doneAt); err != nil {
		return nil, err
	}
	if doneAt.Valid {
		v := doneAt.Time
		tr.DoneAt = &v
	}
	return &tr, nil
}

// C2TransferInput 是建传输记录的输入。
type C2TransferInput struct {
	SessionID  int64
	TaskID     int64 // 关联任务(c2_tasks.id),0 = 无
	Direction  string
	RemotePath string
	Size       int64
	// LocalPath 平台侧 blob/产物路径(建行时已知可直接带;后续也可经
	// UpdateC2Transfer 补记)。
	LocalPath string
}

// CreateC2Transfer 建传输记录(初始 queued)。
func (d *DB) CreateC2Transfer(in C2TransferInput) (int64, error) {
	if in.SessionID <= 0 {
		return 0, fmt.Errorf("session_id 必填")
	}
	if !ValidC2TransferDirection(in.Direction) {
		return 0, fmt.Errorf("无效传输方向: %q(仅 up/down)", in.Direction)
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO c2_transfers(session_id, task_id, direction, remote_path, size, local_path)
VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
		in.SessionID, nullableID(in.TaskID), in.Direction, in.RemotePath, in.Size, in.LocalPath).Scan(&id)
	return id, err
}

// UpdateC2Transfer 回写传输进度/终态;localPath 非空时记录平台侧路径;
// totalSize<0 保持原值,>=0 时回写远端报告的总大小(down 传输由 beacon push 的 ln 带回)。
func (d *DB) UpdateC2Transfer(id int64, status string, doneSize, totalSize int64, localPath, errText string) error {
	if !ValidC2TransferStatus(status) {
		return fmt.Errorf("无效传输状态: %q", status)
	}
	_, err := d.Exec(`UPDATE c2_transfers SET
	status=$2, done_size=$3, error=$4,
	local_path = CASE WHEN $5::boolean THEN $6 ELSE local_path END,
	size = CASE WHEN $7::bigint >= 0 THEN $7::bigint ELSE size END,
	done_at = CASE WHEN $2 IN ('done','error') THEN now() ELSE done_at END
WHERE id=$1`, id, status, doneSize, errText, localPath != "", localPath, totalSize)
	return err
}

// SetC2TransferTask 补挂关联任务(先建 transfer 再建 task 的时序回填;
// Store 接口的 UpdateTransfer 不带 task_id,故单独给一列回填方法)。
func (d *DB) SetC2TransferTask(id, taskID int64) error {
	_, err := d.Exec(`UPDATE c2_transfers SET task_id=$2 WHERE id=$1`, id, nullableID(taskID))
	return err
}

// ListC2Transfers 列传输记录(sessionID 0 = 全部,新者在前)。
func (d *DB) ListC2Transfers(sessionID int64, limit int) ([]*C2Transfer, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := d.Query(`SELECT `+c2TransferCols+` FROM c2_transfers t
WHERE ($1 = 0 OR t.session_id = $1) ORDER BY t.id DESC LIMIT $2`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*C2Transfer{}
	for rows.Next() {
		tr, err := scanC2Transfer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tr)
	}
	return out, rows.Err()
}

// GetC2Transfer 读单条传输(不存在返回 nil, nil)。
func (d *DB) GetC2Transfer(id int64) (*C2Transfer, error) {
	tr, err := scanC2Transfer(d.QueryRow(`SELECT `+c2TransferCols+` FROM c2_transfers t WHERE t.id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return tr, nil
}

// ---- 战利品 ----

// C2Loot 是一条战利品归档。credential/hash 的 Content 是 secretbox 密文:
// 列表查询在 SQL 层就置空串(只给 HasContent 标记),完整密文只有 GetC2Loot 给,
// 解密明文只允许出现在 server 层内存与详情接口。
type C2Loot struct {
	ID         int64     `json:"id"`
	SessionID  int64     `json:"session_id"`
	TaskID     int64     `json:"task_id"`
	Kind       string    `json:"kind"`
	Name       string    `json:"name"`
	Content    string    `json:"content"`
	HasContent bool      `json:"has_content"`
	SourcePath string    `json:"source_path"`
	FindingID  int64     `json:"finding_id"`
	CreatedAt  time.Time `json:"created_at"`

	// BlobPath 大文件落盘路径,服务端内部用,永不序列化。
	BlobPath string `json:"-"`
}

// c2LootListCols 列表取列:content 直接置空串(密文不出库),has_content 原地算。
const c2LootListCols = `lo.id, lo.session_id, COALESCE(lo.task_id,0), lo.kind, lo.name,
	'' AS content, (lo.content <> '') AS has_content,
	lo.source_path, COALESCE(lo.finding_id,0), lo.created_at`

// c2LootFullCols 详情取列:带原始 content 密文(server 层负责解密与不回显)。
const c2LootFullCols = `lo.id, lo.session_id, COALESCE(lo.task_id,0), lo.kind, lo.name,
	lo.content, (lo.content <> '') AS has_content,
	lo.source_path, COALESCE(lo.finding_id,0), lo.created_at`

func scanC2Loot(sc interface{ Scan(...any) error }) (*C2Loot, error) {
	var lo C2Loot
	err := sc.Scan(&lo.ID, &lo.SessionID, &lo.TaskID, &lo.Kind, &lo.Name,
		&lo.Content, &lo.HasContent, &lo.SourcePath, &lo.FindingID, &lo.CreatedAt)
	return &lo, err
}

// C2LootInput 是记战利品的输入(Content 由 server 层决定原文或密文)。
type C2LootInput struct {
	SessionID  int64
	TaskID     int64
	Kind       string
	Name       string
	Content    string
	BlobPath   string
	SourcePath string
}

// CreateC2Loot 归档一条战利品。
func (d *DB) CreateC2Loot(in C2LootInput) (int64, error) {
	if in.SessionID <= 0 {
		return 0, fmt.Errorf("session_id 必填")
	}
	if !ValidC2LootKind(in.Kind) {
		return 0, fmt.Errorf("无效战利品类型: %q", in.Kind)
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO c2_loot(session_id, task_id, kind, name, content, blob_path, source_path)
VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		in.SessionID, nullableID(in.TaskID), in.Kind, in.Name, in.Content,
		in.BlobPath, in.SourcePath).Scan(&id)
	return id, err
}

// C2LootFilter 是战利品列表的筛选条件。零值 = 不限。
type C2LootFilter struct {
	Kind      string
	SessionID int64
	Q         string // 名称/来源路径模糊匹配
	Limit     int
	Offset    int
}

// ListC2Loot 分页检索战利品(content 恒空串+has_content;total 在全部筛选后算)。
func (d *DB) ListC2Loot(f C2LootFilter) ([]*C2Loot, int, error) {
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
		if !ValidC2LootKind(f.Kind) {
			return nil, 0, fmt.Errorf("无效战利品类型: %q", f.Kind)
		}
		add("lo.kind = $%d", f.Kind)
	}
	if f.SessionID > 0 {
		add("lo.session_id = $%d", f.SessionID)
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		where = append(where, fmt.Sprintf("(lo.name ILIKE $%d OR lo.source_path ILIKE $%d)",
			len(args)+1, len(args)+1))
		args = append(args, "%"+q+"%")
	}
	kw := strings.Join(where, " AND ")

	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM c2_loot lo WHERE `+kw, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT ` + c2LootListCols + ` FROM c2_loot lo WHERE ` + kw +
		` ORDER BY lo.id DESC` + fmt.Sprintf(` LIMIT $%d OFFSET $%d`, len(args)+1, len(args)+2)
	args = append(args, f.Limit, f.Offset)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*C2Loot{}
	for rows.Next() {
		lo, err := scanC2Loot(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, lo)
	}
	return out, total, rows.Err()
}

// GetC2Loot 读单条战利品(Content 为原始密文,解密在 server 层;不存在返回 nil, nil)。
func (d *DB) GetC2Loot(id int64) (*C2Loot, error) {
	lo, err := scanC2Loot(d.QueryRow(
		`SELECT `+c2LootFullCols+` FROM c2_loot lo WHERE lo.id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return lo, nil
}

// DeleteC2Loot 删战利品。
func (d *DB) DeleteC2Loot(id int64) (bool, error) {
	res, err := d.Exec(`DELETE FROM c2_loot WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// CountC2LootByKind 各类型战利品计数(总库,页签计数用)。五种 kind 全部给键,
// 没数据的计 0,map 永不 nil。
func (d *DB) CountC2LootByKind() (map[string]int64, error) {
	out := map[string]int64{
		C2LootKindCredential: 0, C2LootKindHash: 0, C2LootKindFile: 0,
		C2LootKindScreenshot: 0, C2LootKindOther: 0,
	}
	rows, err := d.Query(`SELECT kind, COUNT(*) FROM c2_loot GROUP BY kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var n int64
		if err := rows.Scan(&kind, &n); err != nil {
			return nil, err
		}
		out[kind] = n
	}
	return out, rows.Err()
}

// ---- 载荷 ----

// C2Payload 是一份 beacon 投递物。token 唯一,用于监听器免鉴权投递端点;
// FilePath 落盘路径服务端内部用,永不序列化。
type C2Payload struct {
	ID         int64     `json:"id"`
	ListenerID int64     `json:"listener_id"`
	Name       string    `json:"name"`
	Format     string    `json:"format"`
	Target     string    `json:"target"`
	Token      string    `json:"token"`
	SHA256     string    `json:"sha256"`
	Size       int64     `json:"size"`
	Oneliner   string    `json:"oneliner"`
	CreatedBy  string    `json:"created_by"`
	CreatedAt  time.Time `json:"created_at"`

	// 二期列(契约 §13/§14):变形来源档案(0=无)与是否经 VPS 构建出二进制
	//(gosrc 建行时是源码 zip,构建成功后 sha256/size/file_path 回填为二进制)。
	EvasionProfileID int64 `json:"evasion_profile_id"`
	BuiltOnVPS       bool  `json:"built_on_vps"`

	// FilePath 平台侧文件路径,json:"-" 永不序列化。
	FilePath string `json:"-"`
}

const c2PayloadCols = `p.id, p.listener_id, p.name, p.format, p.target, p.token,
	p.file_path, p.sha256, p.size, p.oneliner, p.created_by, p.created_at,
	COALESCE(p.evasion_profile_id,0), p.built_on_vps`

func scanC2Payload(sc interface{ Scan(...any) error }) (*C2Payload, error) {
	var pl C2Payload
	err := sc.Scan(&pl.ID, &pl.ListenerID, &pl.Name, &pl.Format, &pl.Target, &pl.Token,
		&pl.FilePath, &pl.SHA256, &pl.Size, &pl.Oneliner, &pl.CreatedBy, &pl.CreatedAt,
		&pl.EvasionProfileID, &pl.BuiltOnVPS)
	return &pl, err
}

// C2PayloadInput 是登记载荷的输入(字节渲染与落盘在 server 层)。
type C2PayloadInput struct {
	ListenerID int64
	Name       string
	Format     string
	Target     string
	Token      string
	FilePath   string
	SHA256     string
	Size       int64
	Oneliner   string
	CreatedBy  string
	// EvasionProfileID 变形来源档案(免杀管线创建的载荷带;0=无)。
	EvasionProfileID int64
}

// CreateC2Payload 登记载荷(token 必填且全局唯一)。
func (d *DB) CreateC2Payload(in C2PayloadInput) (int64, error) {
	if in.ListenerID <= 0 {
		return 0, fmt.Errorf("listener_id 必填")
	}
	if !ValidC2PayloadFormat(in.Format) {
		return 0, fmt.Errorf("无效载荷格式: %q", in.Format)
	}
	if !ValidC2PayloadTarget(in.Target) {
		return 0, fmt.Errorf("无效目标平台: %q", in.Target)
	}
	if strings.TrimSpace(in.Token) == "" {
		return 0, fmt.Errorf("token 必填")
	}
	if in.Target == "" {
		in.Target = "windows"
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO c2_payloads(listener_id, name, format, target, token, file_path, sha256, size, oneliner, created_by, evasion_profile_id)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`,
		in.ListenerID, in.Name, in.Format, in.Target, in.Token, in.FilePath,
		in.SHA256, in.Size, in.Oneliner, in.CreatedBy, nullableID(in.EvasionProfileID)).Scan(&id)
	return id, err
}

// UpdateC2PayloadFilePath 回填落盘路径(先登记拿 id、再按数字 id 构造文件名的时序)。
func (d *DB) UpdateC2PayloadFilePath(id int64, path string) error {
	_, err := d.Exec(`UPDATE c2_payloads SET file_path=$2 WHERE id=$1`, id, path)
	return err
}

// UpdateC2PayloadBuildResult 回填 VPS 构建产物(契约 §14):下载入口改指二进制文件,
// sha256/size 换成二进制的值,built_on_vps=true。构建失败不调用(job.error 记原因,
// 载荷行保持源码 zip 状态仍可下载)。
func (d *DB) UpdateC2PayloadBuildResult(id int64, filePath, sha256 string, size int64) error {
	_, err := d.Exec(`UPDATE c2_payloads SET
	file_path=$2, sha256=$3, size=$4, built_on_vps=true
WHERE id=$1`, id, filePath, sha256, size)
	return err
}

// ListC2Payloads 列载荷(listenerID 0 = 全部,新者在前)。
func (d *DB) ListC2Payloads(listenerID int64) ([]*C2Payload, error) {
	rows, err := d.Query(`SELECT `+c2PayloadCols+` FROM c2_payloads p
WHERE ($1 = 0 OR p.listener_id = $1) ORDER BY p.id DESC`, listenerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*C2Payload{}
	for rows.Next() {
		pl, err := scanC2Payload(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, pl)
	}
	return out, rows.Err()
}

// GetC2Payload 按数字 id 读载荷(不存在返回 nil, nil)。
func (d *DB) GetC2Payload(id int64) (*C2Payload, error) {
	pl, err := scanC2Payload(d.QueryRow(`SELECT `+c2PayloadCols+` FROM c2_payloads p WHERE p.id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return pl, nil
}

// GetC2PayloadByToken 按投递令牌读载荷(监听器 /p/<token> 端点用)。
func (d *DB) GetC2PayloadByToken(token string) (*C2Payload, error) {
	pl, err := scanC2Payload(d.QueryRow(`SELECT `+c2PayloadCols+` FROM c2_payloads p WHERE p.token=$1`, token))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return pl, nil
}

// DeleteC2Payload 删载荷。
func (d *DB) DeleteC2Payload(id int64) (bool, error) {
	res, err := d.Exec(`DELETE FROM c2_payloads WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
