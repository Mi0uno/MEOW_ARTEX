package db

// C2 插件板块访问层(契约 C2_CONTRACTS.md §21,三期 Agent G)。c2_plugins 是可推送
// 执行的能力包库:script(脚本,entry 模板经命令通道下发)/ binary(二进制,经文件
// 通道上传后执行)。表结构见 schema.sql 尾部,本层不改 schema。
//
// 纪律(照 c2.go / c2_evasion.go):
//   - params_schema 是 JSONB:扫描统一转 json.RawMessage 且空值归一 "{}",永不
//     返回 nil(可直接序列化);语义(server 侧参数渲染)在 server 层解释。
//   - blob_path 是平台侧二进制 blob 路径,服务端内部用,json:"-" 永不序列化
//     (与 c2_loot/c2_payloads 的路径纪律一致)。
//   - List 的 total 在 WHERE(全部筛选)之后、LIMIT 之前算;列表永不 nil slice。
//   - 内置插件(builtin=true)只能由 SeedBuiltinC2Plugins 播种维护:Update/Delete
//     在 SQL 里就带 builtin = false 守卫(server 层另有 403 前置,这里是兜底)。
//   - kind='plugin' 的 c2_tasks 由 CreateC2PluginTask 落库:db/c2.go 的
//     ValidC2TaskKind 白名单归 Agent T 的 v3 改动所有(零接触),故插件任务走
//     本文件独立入口,复用同一张表与同一套流水通道(schema CHECK 已放行 plugin)。
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

// 插件目标平台(与载荷 target 同一取值集,复用 ValidC2PayloadTarget 校验)。
const (
	C2PluginPlatformWindows = "windows"
	C2PluginPlatformLinux   = "linux"
	C2PluginPlatformMacOS   = "macos"
	C2PluginPlatformCross   = "cross"
)

// 插件形态:script(entry 模板直走命令通道)/ binary(文件通道上传 blob 后执行)。
const (
	C2PluginKindScript = "script"
	C2PluginKindBinary = "binary"
)

// ValidC2PluginKind 校验插件形态。
func ValidC2PluginKind(k string) bool {
	return k == C2PluginKindScript || k == C2PluginKindBinary
}

// 插件语言:ps1(Windows PowerShell)/ sh(POSIX shell)/ py(Python)。
const (
	C2PluginLanguagePs1 = "ps1"
	C2PluginLanguageSh  = "sh"
	C2PluginLanguagePy  = "py"
)

// ValidC2PluginLanguage 校验插件语言。
func ValidC2PluginLanguage(l string) bool {
	switch l {
	case C2PluginLanguagePs1, C2PluginLanguageSh, C2PluginLanguagePy:
		return true
	}
	return false
}

// ---- 行结构与取列 ----

// C2Plugin 是一个能力包。ParamsSchema 描述 entry 模板的可注入参数
// (type: string|bool|enum,可选 default/options/desc),渲染在 server 层做。
type C2Plugin struct {
	ID           int64           `json:"id"`
	Name         string          `json:"name"`
	Platform     string          `json:"platform"`
	Kind         string          `json:"kind"`
	Language     string          `json:"language"`
	Entry        string          `json:"entry"`
	Content      string          `json:"content"`
	ParamsSchema json.RawMessage `json:"params_schema"`
	Description  string          `json:"description"`
	Builtin      bool            `json:"builtin"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`

	// BlobPath binary 插件的平台侧 blob 路径,服务端内部用,永不序列化。
	BlobPath string `json:"-"`
}

// c2PluginCols 是插件行的取列(List/Get 共用,口径一致);params_schema 恒非空
// (NOT NULL DEFAULT '{}'),blob_path 原样出库但结构体标 json:"-"。
const c2PluginCols = `p.id, p.name, p.platform, p.kind, p.language, p.entry, p.content,
	p.blob_path, p.params_schema, p.description, p.builtin, p.created_at, p.updated_at`

func scanC2Plugin(sc interface{ Scan(...any) error }) (*C2Plugin, error) {
	var p C2Plugin
	var schema []byte
	if err := sc.Scan(&p.ID, &p.Name, &p.Platform, &p.Kind, &p.Language, &p.Entry,
		&p.Content, &p.BlobPath, &schema, &p.Description, &p.Builtin,
		&p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.ParamsSchema = json.RawMessage("{}")
	if len(schema) > 0 {
		p.ParamsSchema = json.RawMessage(schema)
	}
	return &p, nil
}

// C2PluginInput 是新建/编辑插件的输入(整体覆盖)。builtin 不在输入里:手工建的
// 恒为 false,内置行只经 SeedBuiltinC2Plugins 播种维护。
type C2PluginInput struct {
	Name         string
	Platform     string
	Kind         string
	Language     string
	Entry        string
	Content      string
	BlobPath     string
	ParamsSchema json.RawMessage
	Description  string
}

// normalizeC2PluginInput 填默认值并做白名单校验。
func normalizeC2PluginInput(in *C2PluginInput) error {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if in.Name == "" {
		return fmt.Errorf("插件名称必填")
	}
	if in.Platform == "" {
		in.Platform = C2PluginPlatformCross
	}
	if !ValidC2PayloadTarget(in.Platform) {
		return fmt.Errorf("无效平台: %q(仅 windows/linux/macos/cross)", in.Platform)
	}
	if in.Kind == "" {
		in.Kind = C2PluginKindScript
	}
	if !ValidC2PluginKind(in.Kind) {
		return fmt.Errorf("无效插件形态: %q(仅 script/binary)", in.Kind)
	}
	if in.Language == "" {
		in.Language = C2PluginLanguageSh
	}
	if !ValidC2PluginLanguage(in.Language) {
		return fmt.Errorf("无效语言: %q(仅 ps1/sh/py)", in.Language)
	}
	if len(in.ParamsSchema) == 0 {
		in.ParamsSchema = json.RawMessage("{}")
	}
	var probe any
	if err := json.Unmarshal(in.ParamsSchema, &probe); err != nil {
		return fmt.Errorf("params_schema 必须是合法 JSON 对象: %w", err)
	}
	return nil
}

// ---- CRUD 与过滤 ----

// CreateC2Plugin 建插件(手工建,恒 builtin=false)。
func (d *DB) CreateC2Plugin(in C2PluginInput) (int64, error) {
	if err := normalizeC2PluginInput(&in); err != nil {
		return 0, err
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO c2_plugins(name, platform, kind, language, entry, content, blob_path, params_schema, description)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`,
		in.Name, in.Platform, in.Kind, in.Language, in.Entry, in.Content,
		in.BlobPath, []byte(in.ParamsSchema), in.Description).Scan(&id)
	return id, err
}

// GetC2Plugin 读单个插件(不存在返回 nil, nil)。
func (d *DB) GetC2Plugin(id int64) (*C2Plugin, error) {
	p, err := scanC2Plugin(d.QueryRow(`SELECT `+c2PluginCols+` FROM c2_plugins p WHERE p.id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// C2PluginFilter 是插件列表的筛选条件。零值 = 不限。
type C2PluginFilter struct {
	Platform string // windows/linux/macos/cross
	Kind     string // script/binary
	Q        string // 名称/描述模糊匹配
	Limit    int
	Offset   int
}

// ListC2Plugins 分页检索插件(内置在前,其余按 id 倒序)。total 在全部筛选之后算。
func (d *DB) ListC2Plugins(f C2PluginFilter) ([]*C2Plugin, int, error) {
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
	if f.Platform != "" {
		if !ValidC2PayloadTarget(f.Platform) {
			return nil, 0, fmt.Errorf("无效平台: %q", f.Platform)
		}
		add("p.platform = $%d", f.Platform)
	}
	if f.Kind != "" {
		if !ValidC2PluginKind(f.Kind) {
			return nil, 0, fmt.Errorf("无效插件形态: %q", f.Kind)
		}
		add("p.kind = $%d", f.Kind)
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		where = append(where, fmt.Sprintf("(p.name ILIKE $%d OR p.description ILIKE $%d)",
			len(args)+1, len(args)+1))
		args = append(args, "%"+q+"%")
	}
	kw := strings.Join(where, " AND ")

	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM c2_plugins p WHERE `+kw, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	// 内置插件排在前面(固定能力包),用户插件新者在前。
	q := `SELECT ` + c2PluginCols + ` FROM c2_plugins p WHERE ` + kw +
		` ORDER BY p.builtin DESC, p.id DESC` +
		fmt.Sprintf(` LIMIT $%d OFFSET $%d`, len(args)+1, len(args)+2)
	args = append(args, f.Limit, f.Offset)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*C2Plugin{}
	for rows.Next() {
		p, err := scanC2Plugin(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

// UpdateC2Plugin 更新插件(整体覆盖)。SQL 带 builtin = false 守卫:内置行不更新,
// 返回 false(server 层据此 403;查无此行也返回 false,由 server 层先 404 区分)。
func (d *DB) UpdateC2Plugin(id int64, in C2PluginInput) (bool, error) {
	if err := normalizeC2PluginInput(&in); err != nil {
		return false, err
	}
	res, err := d.Exec(`UPDATE c2_plugins SET
	name=$2, platform=$3, kind=$4, language=$5, entry=$6, content=$7,
	blob_path=$8, params_schema=$9, description=$10
WHERE id=$1 AND builtin = false`,
		id, in.Name, in.Platform, in.Kind, in.Language, in.Entry, in.Content,
		in.BlobPath, []byte(in.ParamsSchema), in.Description)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteC2Plugin 删插件。SQL 带 builtin = false 守卫(内置行拒删,返回 false)。
func (d *DB) DeleteC2Plugin(id int64) (bool, error) {
	res, err := d.Exec(`DELETE FROM c2_plugins WHERE id=$1 AND builtin = false`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// CountC2Plugins 全量计数(总览)。
func (d *DB) CountC2Plugins() (builtin, total int64, err error) {
	err = d.QueryRow(`SELECT COUNT(*) FILTER (WHERE builtin), COUNT(*) FROM c2_plugins`).
		Scan(&builtin, &total)
	return builtin, total, err
}

// ---- 运行链路:插件任务落库 ----

// CreateC2PluginTask 落一条 kind='plugin' 的 c2_tasks 运行记录(结果即任务流水,
// beacon 对未知 kind 按 input.command 走既有 shell 通道执行)。input 是 server 层
// 渲染好的 JSON(约定形状 {plugin_id,name,params,command});actor 照旧 user/agent。
//
// 说明:不走 db/c2.go 的 CreateC2Task,因为其 ValidC2TaskKind 白名单归 Agent T 的
// db/c2.go v3 改动所有(本 agent 零接触该文件);schema 的 kind CHECK 已放行
// 'plugin',本入口自带同样的纪律校验(session_id/actor),语义与 CreateC2Task 一致。
func (d *DB) CreateC2PluginTask(sessionID int64, input, actor, createdBy string) (int64, error) {
	if sessionID <= 0 {
		return 0, fmt.Errorf("session_id 必填")
	}
	if strings.TrimSpace(input) == "" {
		return 0, fmt.Errorf("input 必填")
	}
	if actor == "" {
		actor = C2TaskActorUser
	}
	if !ValidC2TaskActor(actor) {
		return 0, fmt.Errorf("无效操作者: %q(仅 user/agent)", actor)
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO c2_tasks(session_id, kind, input, actor, created_by)
VALUES ($1, 'plugin', $2, $3, $4) RETURNING id`,
		sessionID, input, actor, createdBy).Scan(&id)
	return id, err
}

// ---- 内置种子插件(契约 §21:≥6 个跨平台教育性插件,ps1/sh 按平台各出对应实现) ----

// BuiltinC2Plugins 返回内置种子插件清单(6 个概念 × Windows/POSIX 两种实现)。
// 防御视角文案;entry 用 {{param}} / {{param:默认}} / {{param:选项1|选项2}} 占位,
// 选项内联在模板里自文档化,语义声明在 params_schema(类型/默认/说明)。
// 详见 server 层的参数渲染与注入防护(参数值消毒,模板本体是信任内容)。
func BuiltinC2Plugins() []C2PluginInput {
	return []C2PluginInput{
		{
			Name:     "系统信息 · Windows",
			Platform: C2PluginPlatformWindows,
			Kind:     C2PluginKindScript,
			Language: C2PluginLanguagePs1,
			Entry: `Write-Output '== 系统信息(教育用途·资产盘点) =='; ` +
				`Get-CimInstance Win32_OperatingSystem | Format-List Caption,Version,BuildNumber,OSArchitecture,LastBootUpTime; ` +
				`Write-Output ('主机: ' + $env:COMPUTERNAME + ' | 用户: ' + $env:USERDOMAIN + '\' + $env:USERNAME); ` +
				`if ('{{verbose:0|1}}' -eq '1') { Write-Output '== 磁盘 =='; Get-CimInstance Win32_LogicalDisk | Format-Table DeviceID,Size,FreeSpace -Auto; Write-Output '== 最近补丁(5 条) =='; Get-HotFix | Sort-Object InstalledOn -Descending | Select-Object -First 5 HotFixID,InstalledOn | Format-Table -Auto }`,
			ParamsSchema: json.RawMessage(`{"verbose":{"type":"enum","options":["0","1"],"default":"0","desc":"1=附磁盘与补丁详情"}}`),
			Description:  "采集系统版本/架构/最近启动与当前用户,盘点受控主机资产基线;verbose=1 附磁盘与补丁详情。",
		},
		{
			Name:     "系统信息 · POSIX",
			Platform: C2PluginPlatformCross,
			Kind:     C2PluginKindScript,
			Language: C2PluginLanguageSh,
			Entry: `v='{{verbose:0|1}}'; echo '== 系统信息(教育用途·资产盘点) =='; uname -a; id; uptime; ` +
				`if [ "$v" = "1" ]; then echo '== 磁盘 =='; df -h; echo '== 内存 =='; free -m 2>/dev/null || vm_stat; fi`,
			ParamsSchema: json.RawMessage(`{"verbose":{"type":"enum","options":["0","1"],"default":"0","desc":"1=附磁盘与内存详情"}}`),
			Description:  "采集内核/账号/负载基线(Linux 与 macOS 通用);verbose=1 附磁盘与内存详情。",
		},
		{
			Name:     "进程列表 · Windows",
			Platform: C2PluginPlatformWindows,
			Kind:     C2PluginKindScript,
			Language: C2PluginLanguagePs1,
			Entry: `Write-Output '== 进程列表(按 {{sort:cpu|name|ws}} 倒序,前 {{count:25}} 条) =='; ` +
				`Get-Process | Sort-Object -Property {{sort:cpu|name|ws}} -Descending | Select-Object -First {{count:25}} Id,ProcessName,CPU,@{n='MemMB';e={[int]($_.WS/1MB)}} | Format-Table -Auto`,
			ParamsSchema: json.RawMessage(`{"sort":{"type":"enum","options":["cpu","name","ws"],"default":"cpu","desc":"排序键:cpu/name/ws(工作集内存)"},"count":{"type":"string","default":"25","desc":"返回条数(数字)"}}`),
			Description:  "枚举进程并按 CPU/内存/名称排序,用于定位高资源占用与可疑进程(取证基线)。",
		},
		{
			Name:     "进程列表 · POSIX",
			Platform: C2PluginPlatformCross,
			Kind:     C2PluginKindScript,
			Language: C2PluginLanguageSh,
			Entry: `echo '== 进程列表(按 {{sort:cpu|mem}} 倒序,前 {{count:25}} 条) =='; ` +
				`if [ '{{sort:cpu|mem}}' = 'mem' ]; then ps aux | sort -rn -k4; else ps aux | sort -rn -k3; fi | head -n {{count:25}}`,
			ParamsSchema: json.RawMessage(`{"sort":{"type":"enum","options":["cpu","mem"],"default":"cpu","desc":"按 CPU 或内存排序"},"count":{"type":"string","default":"25","desc":"返回条数(数字)"}}`),
			Description:  "ps 枚举进程按 CPU/内存排序(Linux 与 macOS 通用)。",
		},
		{
			Name:     "网络连接 · Windows",
			Platform: C2PluginPlatformWindows,
			Kind:     C2PluginKindScript,
			Language: C2PluginLanguagePs1,
			Entry: `Write-Output '== 活动网络连接(监听/已建立) =='; ` +
				`Get-NetTCPConnection -State Listen,Established -ErrorAction SilentlyContinue | Select-Object LocalAddress,LocalPort,RemoteAddress,RemotePort,State,OwningProcess | Sort-Object OwningProcess | Format-Table -Auto; ` +
				`if ('{{resolve:0|1}}' -eq '1') { Write-Output '== 连接关联进程 =='; Get-Process | Select-Object Id,ProcessName,Path | Format-Table -Auto }`,
			ParamsSchema: json.RawMessage(`{"resolve":{"type":"enum","options":["0","1"],"default":"0","desc":"1=附进程名与可执行路径"}}`),
			Description:  "枚举监听与已建立的 TCP 连接(网络暴露面审计);resolve=1 附进程对照表。",
		},
		{
			Name:         "网络连接 · POSIX",
			Platform:     C2PluginPlatformCross,
			Kind:         C2PluginKindScript,
			Language:     C2PluginLanguageSh,
			Entry:        `echo '== 活动网络连接 =='; (ss -tunap 2>/dev/null || netstat -tunap 2>/dev/null) | head -n {{limit:60}}`,
			ParamsSchema: json.RawMessage(`{"limit":{"type":"string","default":"60","desc":"返回行数上限(数字)"}}`),
			Description:  "ss/netstat 枚举套接字与关联进程(Linux 与 macOS 通用)。",
		},
		{
			Name:     "计划任务枚举 · Windows",
			Platform: C2PluginPlatformWindows,
			Kind:     C2PluginKindScript,
			Language: C2PluginLanguagePs1,
			Entry: `Write-Output '== 计划任务(非 Microsoft 路径) =='; ` +
				`Get-ScheduledTask | Where-Object { $_.TaskPath -notlike '\Microsoft\*' } | Select-Object TaskPath,TaskName,State | Format-Table -Auto; ` +
				`if ('{{detail:0|1}}' -eq '1') { Write-Output '== 运行时间 =='; Get-ScheduledTask | Where-Object { $_.TaskPath -notlike '\Microsoft\*' } | ForEach-Object { $i = Get-ScheduledTaskInfo -TaskName $_.TaskName -TaskPath $_.TaskPath -ErrorAction SilentlyContinue; [PSCustomObject]@{ Task = $_.TaskName; NextRun = $i.NextRunTime; LastRun = $i.LastRunTime } } | Format-Table -Auto }`,
			ParamsSchema: json.RawMessage(`{"detail":{"type":"enum","options":["0","1"],"default":"0","desc":"1=附下次/上次运行时间"}}`),
			Description:  "枚举非系统路径的计划任务(持久化与横向排查的常规审计点);detail=1 附运行时间。",
		},
		{
			Name:     "计划任务枚举 · POSIX",
			Platform: C2PluginPlatformCross,
			Kind:     C2PluginKindScript,
			Language: C2PluginLanguageSh,
			Entry: `echo '== 用户 crontab =='; crontab -l 2>/dev/null; echo '== /etc/cron* =='; ls -la /etc/cron.d /etc/cron.daily /etc/cron.hourly /etc/cron.weekly 2>/dev/null; ` +
				`echo '== systemd timers =='; systemctl list-timers --no-pager 2>/dev/null | head -n {{limit:40}}; echo '== launchd(如为 macOS) =='; ls -la /System/Library/LaunchDaemons /Library/LaunchDaemons 2>/dev/null | head -n 20`,
			ParamsSchema: json.RawMessage(`{"limit":{"type":"string","default":"40","desc":"systemd timers 输出行数上限(数字)"}}`),
			Description:  "枚举 cron/systemd timer/launchd 计划项(Linux 与 macOS 通用)。",
		},
		{
			Name:     "持久化位置检查 · Windows",
			Platform: C2PluginPlatformWindows,
			Kind:     C2PluginKindScript,
			Language: C2PluginLanguagePs1,
			Entry: `Write-Output '== 注册表 Run 键(自启动审计·防御视角) =='; ` +
				`foreach ($k in 'HKLM:\Software\Microsoft\Windows\CurrentVersion\Run','HKLM:\Software\Microsoft\Windows\CurrentVersion\RunOnce','HKCU:\Software\Microsoft\Windows\CurrentVersion\Run') { Write-Output ('[' + $k + ']'); Get-ItemProperty -Path $k -ErrorAction SilentlyContinue | Select-Object * -ExcludeProperty PS* | Format-List }; ` +
				`Write-Output '== 启动文件夹 =='; Get-ChildItem "$env:APPDATA\Microsoft\Windows\Start Menu\Programs\Startup","$env:ProgramData\Microsoft\Windows\Start Menu\Programs\StartUp" -ErrorAction SilentlyContinue | Select-Object Name,FullName | Format-Table -Auto`,
			ParamsSchema: json.RawMessage(`{}`),
			Description:  "审计注册表 Run/RunOnce 与启动文件夹等常见持久化位置(合规自查视角,只读)。",
		},
		{
			Name:     "持久化位置检查 · POSIX",
			Platform: C2PluginPlatformCross,
			Kind:     C2PluginKindScript,
			Language: C2PluginLanguageSh,
			Entry: `echo '== 用户级持久化点审计(只读) =='; crontab -l 2>/dev/null; echo '--- shell 配置尾部 ---'; tail -n {{limit:20}} ~/.bashrc ~/.bash_profile ~/.profile 2>/dev/null; ` +
				`echo '--- autostart / rc.local / ssh key ---'; ls -la ~/.config/autostart /etc/rc.local ~/.ssh/authorized_keys 2>/dev/null`,
			ParamsSchema: json.RawMessage(`{"limit":{"type":"string","default":"20","desc":"shell 配置尾部行数(数字)"}}`),
			Description:  "审计 crontab/shell 配置/autostart/rc.local/authorized_keys 等持久化点(只读)。",
		},
		{
			Name:     "安全软件进程探测 · Windows",
			Platform: C2PluginPlatformWindows,
			Kind:     C2PluginKindScript,
			Language: C2PluginLanguagePs1,
			Entry: `Write-Output '== 防护软件进程识别(合规审计视角) =='; ` +
				`Get-Process | Where-Object { $_.ProcessName -match 'msmpeng|nissrv|windefend|360|hips|usysdiag|wsctrl|qqpctray|kxetray|kav|avp|mcshield' } | Select-Object Id,ProcessName,Path | Format-Table -Auto; ` +
				`Write-Output '== Defender 运行状态 =='; try { Get-MpComputerStatus | Select-Object AMServiceEnabled,RealTimeProtectionEnabled,AntivirusSignatureLastUpdated } catch { Write-Output 'Defender 不可查询(未启用或权限不足)' }`,
			ParamsSchema: json.RawMessage(`{}`),
			Description:  "识别本机在跑的防护软件进程并查 Defender 实时防护状态,用于授权评估前的防护态势盘点(只读)。",
		},
		{
			Name:         "安全软件进程探测 · POSIX",
			Platform:     C2PluginPlatformCross,
			Kind:         C2PluginKindScript,
			Language:     C2PluginLanguageSh,
			Entry:        `echo '== 防护/检测组件进程识别(合规审计视角) =='; ps aux | grep -Ei 'clamav|crowdsec|fail2ban|osquery|auditd|apparmor|selinux|sentinel|carbon|falcon' | grep -v grep; echo '== 完成 =='`,
			ParamsSchema: json.RawMessage(`{}`),
			Description:  "识别本机 EDR/杀毒/审计组件进程(只读),评估授权测试环境的安全监测覆盖。",
		},
	}
}

// SeedBuiltinC2Plugins 幂等播种内置插件(启动调用):按 name+builtin 定位,缺失则
// 插入,已存在则整行覆盖(entry/content/params_schema 等随版本修正;builtin 行
// 用户不可改,覆盖安全)。返回插入与更新的行数。
func (d *DB) SeedBuiltinC2Plugins() (inserted, updated int, err error) {
	for _, in := range BuiltinC2Plugins() {
		if err := normalizeC2PluginInput(&in); err != nil {
			return inserted, updated, fmt.Errorf("内置插件 «%s» 定义非法: %w", in.Name, err)
		}
		res, err := d.Exec(`UPDATE c2_plugins SET
	platform=$2, kind=$3, language=$4, entry=$5, content=$6, blob_path='', params_schema=$7, description=$8
WHERE name=$1 AND builtin`, in.Name, in.Platform, in.Kind, in.Language, in.Entry,
			in.Content, []byte(in.ParamsSchema), in.Description)
		if err != nil {
			return inserted, updated, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			updated++
			continue
		}
		if _, err := d.Exec(`INSERT INTO c2_plugins
	(name, platform, kind, language, entry, content, blob_path, params_schema, description, builtin)
VALUES ($1,$2,$3,$4,$5,$6,'',$7,$8, true)`,
			in.Name, in.Platform, in.Kind, in.Language, in.Entry, in.Content,
			[]byte(in.ParamsSchema), in.Description); err != nil {
			return inserted, updated, err
		}
		inserted++
	}
	return inserted, updated, nil
}
