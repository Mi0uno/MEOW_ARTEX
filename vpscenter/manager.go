package vpscenter

// Manager 是 VPS 中心运行时:SSH 指派/释放、命令执行、Web 终端、测活循环。
//
// 职责边界(照 proxycenter 惯例):
//   - Manager 只管运行时状态(SSH 连接、终端槽位、输出落盘),不 import db;
//     一切持久化经 Store 接口回到 server/db 层,单测注入内存 fake。
//   - 凭据解密只发生在 Store(实现方)→ MachineRuntime → Dial 之间,
//     Manager 对外的 Machine 视图永不携带凭据。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 执行流水来源白名单(与 schema CHECK 对齐)。
const (
	SourceAgent    = "agent"
	SourceTerminal = "terminal"
	SourceToolset  = "toolset"
	SourceManual   = "manual"
	SourceDetect   = "detect"
)

// 机器状态(与 schema CHECK 对齐)。
const (
	StatusOnline      = "online"
	StatusOffline     = "offline"
	StatusUnknown     = "unknown"
	StatusBusy        = "busy"
	StatusMaintenance = "maintenance"
)

const (
	// defaultCheckInterval 是 RunCheckLoop 的轮询间隔。
	defaultCheckInterval = 60 * time.Second
	// maxTerminalPerMachine 是单机并发 Web 终端上限。
	maxTerminalPerMachine = 2
	// toolsetExecTimeout 是工具集脚本(apt/go install,可能编译很久)的执行上限。
	toolsetExecTimeout = 30 * time.Minute
)

// Machine 是 Manager 视角下的一台机器(公共视图,永不携带凭据)。
type Machine struct {
	ID        int64
	Name      string
	Host      string
	Port      int
	Username  string
	AuthType  string
	Status    string
	OsName    string
	OsVersion string
	Kernel    string
	Arch      string
	CPUCores  int
	MemoryMB  int
	DiskGB    int
	Region    string
	Provider  string
	Tags      []string
	// WorkspaceBase 是该机器上平台工作区根目录(每任务一个 task_<id> 子目录)。
	WorkspaceBase string
	// AssignedTaskID 是当前占用该机器的任务 id;0 = 空闲。
	AssignedTaskID int64
	LastError      string
	LastCheckAt    time.Time
	LastOnlineAt   time.Time
}

// MachineRuntime 在 Machine 之上附加已解密的凭据,仅供 Manager 内部 Dial 使用。
type MachineRuntime struct {
	Machine
	Password      string
	PrivateKey    []byte
	KeyPassphrase string
}

// sshConfig 把运行时凭据装配成 SSH 连接参数。
func (rt *MachineRuntime) sshConfig(timeout time.Duration) SSHConfig {
	return SSHConfig{
		Host: rt.Host, Port: rt.Port, Username: rt.Username,
		AuthType: rt.AuthType, Password: rt.Password,
		PrivateKey: rt.PrivateKey, KeyPassphrase: rt.KeyPassphrase,
		Timeout: timeout,
	}
}

// Toolset 是一个工具集(安装脚本模板)。
type Toolset struct {
	ID          int64
	Name        string
	Description string
	Script      string
	Builtin     bool
}

// ExecRecord 是新开一条执行流水的最小信息(输出类字段执行完由 ExecResult 回填)。
type ExecRecord struct {
	MachineID int64
	TaskID    int64 // 0 = 非任务触发
	Source    string
	ToolsetID int64 // 0 = 非工具集
	Command   string
}

// ExecResult 是一条执行流水的完成回填。ExitCode 为 nil 表示没能拿到退出码
// (连接失败/超时);OutputPath 是完整输出文件的落盘路径(空=未落盘)。
type ExecResult struct {
	ExitCode   *int
	DurationMs int
	OutputSize int
	OutputPath string
	Error      string
}

// Store 是 Manager 需要的持久化能力(由 server 侧实现,桥到 db 包)。
type Store interface {
	// GetMachine 读单台机器(不存在返回 nil, nil)。
	GetMachine(id int64) (*Machine, error)
	// GetMachineRuntime 读单台机器 + 已解密凭据(不存在返回 nil, nil)。
	GetMachineRuntime(id int64) (*MachineRuntime, error)
	// GetMachineByTask 取当前指派给某任务的机器(无指派返回 nil, nil)。
	GetMachineByTask(taskID int64) (*Machine, error)
	// ListMachinesForCheck 取全部待测活机器(含解密凭据,量级为用户录入数)。
	ListMachinesForCheck() ([]MachineRuntime, error)
	// ListFreeOnlineMachines 取空闲且在线的机器(AssignAuto 候选,按优先级排序)。
	ListFreeOnlineMachines() ([]Machine, error)
	// UpdateMachineCheck 回写一次测活结果:status 空串=保持原状态(busy 不许被改);
	// ok=true 刷 last_online_at 并清 last_error;info 非空时刷新系统档案。
	UpdateMachineCheck(id int64, status string, ok bool, info *SysInfo, lastErr string) error
	// AssignMachineToTask 占用机器(写 assigned_task_id + status=busy + 指派历史);
	// 机器已被占用或不存在时返回错误(唯一索引兜底并发)。
	AssignMachineToTask(machineID, taskID int64, by string) error
	// ReleaseMachineFromTask 释放机器(清 assigned_task_id + status=online +
	// 关指派历史行)。未指派时为 no-op nil。
	ReleaseMachineFromTask(machineID, taskID int64, reason string) error
	// CreateExecution 开一条执行流水,返回流水 id。
	CreateExecution(rec ExecRecord) (int64, error)
	// FinishExecution 回填执行流水的完成字段。
	FinishExecution(id int64, res ExecResult) error
	// UpsertDetections 覆盖写机器×工具检测矩阵(PK(machine_id,tool_name))。
	UpsertDetections(machineID int64, dets []ToolDetection) error
	// GetToolset 读工具集(不存在返回 nil, nil)。
	GetToolset(id int64) (*Toolset, error)
	// GetToolsetByName 按名读工具集(内置工具集播种用;不存在返回 nil, nil)。
	GetToolsetByName(name string) (*Toolset, error)
}

// Manager 是 VPS 中心运行时。
type Manager struct {
	store Store
	log   *slog.Logger

	// BaseDir 是执行完整输出的落盘根目录(<BaseDir>/vps/outputs/<execID>.txt)。
	// 启动时由 server 层注入(dataDir);为空则不落盘只留表内预览。
	BaseDir string
	// CheckInterval 是自动测活轮询间隔,默认 60s。
	CheckInterval time.Duration
	// checkDialTimeout 是到机器的 SSH 拨号超时(测活/执行共用),默认 15s。
	checkDialTimeout time.Duration

	// newClient 是 SSH 拨号函数,测试替换成 fake 注入。
	newClient func(cfg SSHConfig) (SSHClient, error)

	// termMu/termCount 守卫单机并发终端上限。
	termMu    sync.Mutex
	termCount map[int64]int
}

// NewManager 构造运行时。log 可为 nil(回落 slog.Default())。
func NewManager(st Store, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		store:            st,
		log:              log,
		CheckInterval:    defaultCheckInterval,
		checkDialTimeout: 15 * time.Second,
		newClient:        Dial,
		termCount:        map[int64]int{},
	}
}

// OutputPath 返回某条执行流水的完整输出文件路径。
func (m *Manager) OutputPath(execID int64) string {
	if m.BaseDir == "" {
		return ""
	}
	return filepath.Join(m.BaseDir, "vps", "outputs", strconv.FormatInt(execID, 10)+".txt")
}

// logf 输出带模块前缀的日志。
func (m *Manager) logf(format string, args ...any) {
	m.log.Info("[vps-center] " + fmt.Sprintf(format, args...))
}

// shq 做 POSIX 单引号转义(命令包装用)。
func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// workspaceFor 计算任务工作区:task_<taskID>;taskID=0 时用 manual_<时间戳>。
func workspaceFor(base string, taskID int64) string {
	if taskID > 0 {
		return filepath.ToSlash(filepath.Join(base, fmt.Sprintf("task_%d", taskID)))
	}
	return filepath.ToSlash(filepath.Join(base, "manual_"+strconv.FormatInt(time.Now().Unix(), 10)))
}

// wrapCommand 统一命令包装:mkdir -p <ws> && cd <ws> && bash -lc '<command>'。
// 每次调用是独立 shell,但工作目录固定;持久状态请落盘文件。
func wrapCommand(ws, command string) string {
	return "mkdir -p " + shq(ws) + " && cd " + shq(ws) + " && bash -lc " + shq(command)
}

// ---- 指派 ----

// Assign 把机器指派给任务:校验在线&空闲 → 写 assigned_task_id + 指派历史 →
// 尽力在机器上建任务工作区目录。工作区建失败不阻断指派(命令包装里每次都会 mkdir)。
func (m *Manager) Assign(taskID, machineID int64, by string) (*Machine, error) {
	if taskID <= 0 {
		return nil, errors.New("指派需要有效的任务")
	}
	rt, err := m.store.GetMachineRuntime(machineID)
	if err != nil {
		return nil, err
	}
	if rt == nil {
		return nil, fmt.Errorf("机器 #%d 不存在", machineID)
	}
	if rt.AssignedTaskID != 0 {
		if rt.AssignedTaskID == taskID {
			return &rt.Machine, nil // 重复指派同一台:幂等成功
		}
		return nil, fmt.Errorf("机器 «%s» 已被任务 #%d 占用", rt.Name, rt.AssignedTaskID)
	}
	if rt.Status != StatusOnline {
		return nil, fmt.Errorf("机器 «%s» 状态为 %s,仅在线空闲机器可指派", rt.Name, rt.Status)
	}
	if err := m.store.AssignMachineToTask(machineID, taskID, by); err != nil {
		return nil, err
	}
	m.logf("机器 «%s»(#%d) 已指派给任务 #%d(by %s)", rt.Name, machineID, taskID, by)

	// 尽力建任务工作区:失败不阻断(执行包装里有 mkdir -p 兜底)。
	ws := workspaceFor(rt.WorkspaceBase, taskID)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, derr := m.newClient(rt.sshConfig(m.checkDialTimeout))
	if derr != nil {
		m.logf("机器 «%s» 暂不可连,任务工作区 %s 延后创建: %v", rt.Name, ws, derr)
	} else {
		if _, _, eerr := client.Exec(ctx, "mkdir -p "+shq(ws), 15*time.Second); eerr != nil {
			m.logf("机器 «%s» 建任务工作区 %s 失败(执行时兜底): %v", rt.Name, ws, eerr)
		}
		_ = client.Close()
	}

	return m.store.GetMachine(machineID)
}

// AssignAuto 自动挑选一台在线空闲机器指派给任务(maintenance/unknown/offline 不参与)。
func (m *Manager) AssignAuto(taskID int64, by string) (*Machine, error) {
	if taskID <= 0 {
		return nil, errors.New("指派需要有效的任务")
	}
	candidates, err := m.store.ListFreeOnlineMachines()
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, errors.New("没有空闲且在线的机器可自动分配")
	}
	return m.Assign(taskID, candidates[0].ID, by)
}

// Release 释放任务占用的机器(清 assigned_task_id + 关指派历史)。
// 幂等:任务没有占用任何机器时 no-op nil。
func (m *Manager) Release(taskID int64, reason string) error {
	if taskID <= 0 {
		return nil
	}
	machine, err := m.store.GetMachineByTask(taskID)
	if err != nil {
		return err
	}
	if machine == nil {
		return nil
	}
	if err := m.store.ReleaseMachineFromTask(machine.ID, taskID, reason); err != nil {
		return err
	}
	m.logf("机器 «%s»(#%d) 已从任务 #%d 释放(%s)", machine.Name, machine.ID, taskID, reason)
	return nil
}

// MachineForTask 取任务当前占用的机器;无指派返回 nil, nil。
func (m *Manager) MachineForTask(taskID int64) (*Machine, error) {
	if taskID <= 0 {
		return nil, nil
	}
	return m.store.GetMachineByTask(taskID)
}

// ---- 执行 ----

// dialFor 取机器运行时并建立 SSH 连接。
func (m *Manager) dialFor(machineID int64) (*MachineRuntime, SSHClient, error) {
	rt, err := m.store.GetMachineRuntime(machineID)
	if err != nil {
		return nil, nil, err
	}
	if rt == nil {
		return nil, nil, fmt.Errorf("机器 #%d 不存在", machineID)
	}
	client, err := m.newClient(rt.sshConfig(m.checkDialTimeout))
	if err != nil {
		return rt, nil, err
	}
	return rt, client, nil
}

// ExecCommand 在机器上执行一条命令(bash -lc 语义,工作目录为任务工作区):
//   - 命令统一包装 mkdir -p <ws> && cd <ws> && bash -lc '<command>';
//   - 完整输出写 <BaseDir>/vps/outputs/<execID>.txt(schema 的流水表无预览列,
//     表内只记 output_size/exit_code/error 等;输出前 2KB 预览由 API 层截取);
//   - 全程落执行流水(source ∈ agent/terminal/toolset/manual/detect)。
//
// 返回完整输出与退出码;err 仅在连接/传输/超时失败时非 nil(命令自身非零
// 退出码不是错误)。
func (m *Manager) ExecCommand(ctx context.Context, machineID, taskID int64, source, command string, timeout time.Duration) (output []byte, exitCode int, err error) {
	rec := ExecRecord{MachineID: machineID, TaskID: taskID, Source: source, Command: command}
	out, code, xerr, _ := m.exec(ctx, rec, timeout, func(rt *MachineRuntime) string {
		return workspaceFor(rt.WorkspaceBase, taskID)
	})
	return out, code, xerr
}

// exec 是执行的核心路径:落流水 → 拨号 → 包装命令 → 执行 → 回填流水。
// dirFn 决定工作目录(任务工作区 / workspace_base 根)。
func (m *Manager) exec(ctx context.Context, rec ExecRecord, timeout time.Duration,
	dirFn func(rt *MachineRuntime) string) (output []byte, exitCode int, err error, execID int64) {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	id, rerr := m.store.CreateExecution(rec)
	if rerr != nil {
		m.logf("写执行流水失败(继续执行): %v", rerr)
		id = 0
	}

	rt, client, derr := m.dialFor(rec.MachineID)
	if derr != nil {
		if client != nil {
			_ = client.Close()
		}
		m.finishExecWith(id, ExecResult{Error: derr.Error()})
		return nil, -1, derr, id
	}
	defer client.Close()

	ws := dirFn(rt)
	start := time.Now()
	out, code, xerr := client.Exec(ctx, wrapCommand(ws, rec.Command), timeout)
	res := ExecResult{DurationMs: int(time.Since(start).Milliseconds()), OutputSize: len(out)}
	if xerr != nil {
		res.Error = xerr.Error()
	} else {
		res.ExitCode = ptrInt(code)
	}
	res.OutputPath = m.writeOutputFile(id, out)
	m.finishExecWith(id, res)
	return out, code, xerr, id
}

// writeOutputFile 把完整输出写到 <BaseDir>/vps/outputs/<execID>.txt。
// BaseDir 为空或写失败时返回空串(表里 output_size 仍是真实大小)。
func (m *Manager) writeOutputFile(execID int64, out []byte) string {
	path := m.OutputPath(execID)
	if path == "" || execID <= 0 {
		return ""
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		m.logf("创建输出目录失败: %v", err)
		return ""
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		m.logf("写执行输出 #%d 失败: %v", execID, err)
		return ""
	}
	return path
}

// finishExecWith 回填执行流水(失败只记日志,不影响主流程)。
func (m *Manager) finishExecWith(execID int64, res ExecResult) {
	if execID <= 0 {
		return
	}
	if err := m.store.FinishExecution(execID, res); err != nil {
		m.logf("回填执行流水 #%d 失败: %v", execID, err)
	}
}

// finishDetectExec 记一条探测型流水(detect:无输出落盘,只留条目)。
func (m *Manager) finishDetectExec(machineID int64, summary string, count int) {
	id, err := m.store.CreateExecution(ExecRecord{
		MachineID: machineID, Source: SourceDetect, Command: summary,
	})
	if err != nil {
		return
	}
	m.finishExecWith(id, ExecResult{DurationMs: 0, OutputSize: count, ExitCode: ptrInt(0)})
}

// ---- Web 终端 ----

// OpenPTY 起一个交互式终端会话(单机并发 ≤2,超限报错)。
// 会话结束自动记一条 source=terminal 流水(命令无法逐条拆分,记首条输入+时长)。
func (m *Manager) OpenPTY(ctx context.Context, machineID int64, cols, rows uint16) (PTYSession, error) {
	if !m.acquireTerminal(machineID) {
		return nil, fmt.Errorf("机器 #%d 的并发终端已满(上限 %d),请先关闭空闲终端", machineID, maxTerminalPerMachine)
	}
	rt, client, err := m.dialFor(machineID)
	if err != nil {
		m.releaseTerminal(machineID)
		if client != nil {
			_ = client.Close()
		}
		return nil, err
	}
	inner, err := client.OpenPTY(ctx, cols, rows)
	if err != nil {
		m.releaseTerminal(machineID)
		_ = client.Close()
		return nil, err
	}
	return newTerminalSession(machineID, rt.AssignedTaskID, client, inner,
		m.releaseTerminal, m.store.CreateExecution,
		func(id int64, res ExecResult) { m.finishExecWith(id, res) }), nil
}

// acquireTerminal 占用一个终端槽位(超限返回 false)。
func (m *Manager) acquireTerminal(machineID int64) bool {
	m.termMu.Lock()
	defer m.termMu.Unlock()
	if m.termCount[machineID] >= maxTerminalPerMachine {
		return false
	}
	m.termCount[machineID]++
	return true
}

// releaseTerminal 释放一个终端槽位。
func (m *Manager) releaseTerminal(machineID int64) {
	m.termMu.Lock()
	defer m.termMu.Unlock()
	if n := m.termCount[machineID]; n <= 1 {
		delete(m.termCount, machineID)
	} else {
		m.termCount[machineID] = n - 1
	}
}

// terminalSession 包装 PTYSession:透传读写、捕获首条输入、会话结束记 terminal
// 流水并释放终端槽位。inner.ExitCode() 由转发 goroutine 独占消费,上层经
// ts 自己的 exitCh 拿到恰好一次的退出码。
type terminalSession struct {
	inner  PTYSession
	client SSHClient
	exitCh chan int
	start  time.Time

	firstMu   sync.Mutex
	first     bytes.Buffer
	firstDone bool

	machineID int64
	taskID    int64

	onRelease func(int64)
	createRec func(ExecRecord) (int64, error)
	finishRec func(int64, ExecResult)

	recorded sync.Once
}

// newTerminalSession 装配终端包装并启动结束转发 goroutine。
func newTerminalSession(machineID, taskID int64, client SSHClient, inner PTYSession,
	onRelease func(int64), createRec func(ExecRecord) (int64, error), finishRec func(int64, ExecResult)) *terminalSession {
	ts := &terminalSession{
		inner:     inner,
		client:    client,
		exitCh:    make(chan int, 1),
		start:     time.Now(),
		machineID: machineID,
		taskID:    taskID,
		onRelease: onRelease,
		createRec: createRec,
		finishRec: finishRec,
	}
	go func() {
		code, ok := <-inner.ExitCode()
		// 会话结束:记流水(幂等)→ 转发退出码 → 释放槽位。
		ts.recorded.Do(func() { ts.record(code, ok) })
		if ok {
			ts.exitCh <- code
		}
		close(ts.exitCh)
	}()
	return ts
}

// Read 透传远端输出。
func (t *terminalSession) Read(p []byte) (int, error) { return t.inner.Read(p) }

// Write 透传键入并捕获首行输入(作流水 command)。
func (t *terminalSession) Write(p []byte) (int, error) {
	t.firstMu.Lock()
	if !t.firstDone {
		t.first.Write(p)
		if bytes.ContainsAny(p, "\r\n") {
			t.firstDone = true
		}
	}
	t.firstMu.Unlock()
	return t.inner.Write(p)
}

// Resize 透传终端尺寸变更。
func (t *terminalSession) Resize(cols, rows uint16) error { return t.inner.Resize(cols, rows) }

// ExitCode 返回本包装的退出码通道(会话结束时恰好投递一次后关闭)。
func (t *terminalSession) ExitCode() <-chan int { return t.exitCh }

// Close 关会话(幂等):停远端 + 断连接。流水与槽位释放由转发 goroutine 兜底;
// 若转发还没触发(远端迟迟不退),本地关闭会让 sess.Wait 返回,goroutine 随即收尾。
func (t *terminalSession) Close() error {
	err := t.inner.Close()
	if t.client != nil {
		_ = t.client.Close()
	}
	return err
}

// record 落终端流水并释放槽位(转发 goroutine 调用,经 recorded 幂等)。
func (t *terminalSession) record(code int, ok bool) {
	defer func() {
		if t.onRelease != nil {
			t.onRelease(t.machineID)
		}
	}()
	t.firstMu.Lock()
	raw := t.first.String()
	t.firstMu.Unlock()
	first := firstInputLine(raw)
	dur := time.Since(t.start)
	command := first
	if command == "" {
		command = "(无输入)"
	}
	var ec *int
	if ok && code >= 0 {
		ec = ptrInt(code)
	}
	res := ExecResult{DurationMs: int(dur.Milliseconds()), ExitCode: ec}
	if t.createRec == nil {
		return
	}
	id, err := t.createRec(ExecRecord{
		MachineID: t.machineID, TaskID: t.taskID, Source: SourceTerminal,
		Command: fmt.Sprintf("[terminal] %s(会话时长 %s)", command, dur.Round(time.Second)),
	})
	if err != nil {
		return
	}
	if t.finishRec != nil {
		t.finishRec(id, res)
	}
}

// firstInputLine 取捕获输入的第一行(截 200 字符)。
func firstInputLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// ---- 测活 ----

// CheckMachine 手动测活:连接 + SysInfo,刷新状态与系统档案;
// 失败标 offline 并记 last_error(busy 机器只探连通性,不改状态)。
func (m *Manager) CheckMachine(ctx context.Context, machineID int64) (*Machine, error) {
	rt, client, err := m.dialFor(machineID)
	if err != nil {
		if client != nil {
			_ = client.Close()
		}
		status := StatusOffline
		if rt != nil && rt.Status == StatusBusy {
			status = "" // 任务还挂着,不擅自改状态;last_error 照记供排查
		}
		lastErr := ""
		if err != nil {
			lastErr = err.Error()
		}
		_ = m.store.UpdateMachineCheck(machineID, status, false, nil, lastErr)
		return m.store.GetMachine(machineID)
	}
	defer client.Close()

	info, serr := client.SysInfo(ctx)
	if serr != nil {
		// 能握手但采不到档案:连接本身是通的,按在线处理(档案留旧值)。
		m.logf("机器 «%s» 系统信息采集失败: %v", rt.Name, serr)
		status := StatusOnline
		if rt.Status == StatusBusy {
			status = ""
		}
		_ = m.store.UpdateMachineCheck(machineID, status, true, nil, "")
		return m.store.GetMachine(machineID)
	}
	status := StatusOnline
	if rt.Status == StatusBusy {
		status = "" // busy 只探连通性,不把占用状态冲掉
	}
	if err := m.store.UpdateMachineCheck(machineID, status, true, info, ""); err != nil {
		return nil, err
	}
	return m.store.GetMachine(machineID)
}

// DetectMachine 跑一轮已知工具探测并覆盖写检测矩阵,返回最新结果。
func (m *Manager) DetectMachine(ctx context.Context, machineID int64) ([]ToolDetection, error) {
	_, client, err := m.dialFor(machineID)
	if err != nil {
		if client != nil {
			_ = client.Close()
		}
		return nil, err
	}
	defer client.Close()

	names := KnownToolNames()
	dets, err := client.DetectTools(ctx, names)
	if err != nil {
		return nil, err
	}
	m.finishDetectExec(machineID,
		fmt.Sprintf("[detect] %d 个已知工具探测(command -v + --version)", len(names)), len(dets))
	if err := m.store.UpsertDetections(machineID, dets); err != nil {
		return nil, err
	}
	return dets, nil
}

func ptrInt(v int) *int { return &v }

// ApplyToolset 在机器上执行一个工具集安装脚本(source=toolset,taskID=0,
// 工作目录为 workspace_base 根)。返回流水 id 与执行结果。
func (m *Manager) ApplyToolset(ctx context.Context, machineID, toolsetID int64) (execID int64, output []byte, exitCode int, err error) {
	ts, err := m.store.GetToolset(toolsetID)
	if err != nil {
		return 0, nil, -1, err
	}
	if ts == nil {
		return 0, nil, -1, fmt.Errorf("工具集 #%d 不存在", toolsetID)
	}
	rec := ExecRecord{MachineID: machineID, Source: SourceToolset, ToolsetID: toolsetID, Command: ts.Script}
	out, code, xerr, id := m.exec(ctx, rec, toolsetExecTimeout,
		func(rt *MachineRuntime) string { return rt.WorkspaceBase })
	return id, out, code, xerr
}

// ---- 后台测活循环 ----

// RunCheckLoop 定时(默认 60s)分批并发测活全部机器:
//   - 非 busy:online⇄offline 按连通性翻转 + 刷新系统档案 + last_error;
//   - busy(任务占用中):只做连通性探测,不改状态、不翻 offline——任务还挂着,
//     掉线留给用户判断,仪表盘仍能看到 last_check。
func (m *Manager) RunCheckLoop(ctx context.Context) {
	interval := m.CheckInterval
	if interval <= 0 {
		interval = defaultCheckInterval
	}
	// 启动后等 15 秒,避开进程启动期的初始化风暴。
	select {
	case <-ctx.Done():
		return
	case <-time.After(15 * time.Second):
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		m.checkAll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// checkAll 跑一轮全量测活(并发 8,单机超时 30s)。
func (m *Manager) checkAll(ctx context.Context) {
	machines, err := m.store.ListMachinesForCheck()
	if err != nil {
		m.logf("读取机器列表失败: %v", err)
		return
	}
	if len(machines) == 0 {
		return
	}
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i := range machines {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(rt MachineRuntime) {
			defer wg.Done()
			defer func() { <-sem }()
			m.checkOne(ctx, rt)
		}(machines[i])
	}
	wg.Wait()
}

// checkOne 测一台:成功 → online(刷新档案),失败 → offline+last_error;
// busy 只探连通性不改状态。
func (m *Manager) checkOne(ctx context.Context, rt MachineRuntime) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	client, err := m.newClient(rt.sshConfig(m.checkDialTimeout))
	if err != nil {
		status := StatusOffline
		if rt.Status == StatusBusy {
			status = "" // 任务还挂着,不擅自改状态;last_error 照记供排查
		}
		if uerr := m.store.UpdateMachineCheck(rt.ID, status, false, nil, err.Error()); uerr != nil {
			m.logf("回写机器 «%s» 测活失败结果出错: %v", rt.Name, uerr)
		}
		return
	}
	defer client.Close()

	info, serr := client.SysInfo(cctx)
	if serr != nil {
		// 握手成功但档案采集失败:连接是通的,按在线处理(档案留旧值)。
		status := StatusOnline
		if rt.Status == StatusBusy {
			status = ""
		}
		_ = m.store.UpdateMachineCheck(rt.ID, status, true, nil, "")
		m.logf("机器 «%s» 测活:在线但系统信息采集失败: %v", rt.Name, serr)
		return
	}
	status := StatusOnline
	if rt.Status == StatusBusy {
		status = ""
	}
	if err := m.store.UpdateMachineCheck(rt.ID, status, true, info, ""); err != nil {
		m.logf("回写机器 «%s» 测活结果失败: %v", rt.Name, err)
	}
}

// ---- 内置工具集播种 ----

// SeedBuiltinToolsets 幂等播种内置工具集:只按 name 插缺失的,已存在的
// (无论 builtin 与否)一律不覆盖——用户对种子改过的内容不能被启动冲掉。
// create 由 server 侧提供(落 vps_toolsets,builtin=true)。
func SeedBuiltinToolsets(st Store, create func(name, description, script string) (int64, error)) error {
	for _, ts := range BuiltinToolsets() {
		existing, err := st.GetToolsetByName(ts.Name)
		if err != nil {
			return err
		}
		if existing != nil {
			continue
		}
		if _, err := create(ts.Name, ts.Description, ts.Script); err != nil {
			return fmt.Errorf("播种工具集 «%s» 失败: %w", ts.Name, err)
		}
	}
	return nil
}

// 编译期断言:terminalSession 满足 PTYSession。
var _ PTYSession = (*terminalSession)(nil)
