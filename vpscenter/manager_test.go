package vpscenter

// Manager 纯逻辑单测:内存 fakeStore + fake SSHClient,不连真实 PG/SSH。
// 覆盖:指派互斥/幂等、自动分配、释放幂等、命令包装、输出落盘、终端槽位上限、
// 工具集执行、探测矩阵、内置种子幂等。

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- fake Store ----

type fakeAssignment struct {
	MachineID int64
	TaskID    int64
	By        string
	Open      bool
	Reason    string
}

type fakeCheckCall struct {
	ID     int64
	Status string
	OK     bool
	Info   *SysInfo
	Err    string
}

type fakeStore struct {
	mu sync.Mutex

	machines    map[int64]*MachineRuntime
	assignments []fakeAssignment
	execs       map[int64]ExecRecord
	execResults map[int64]ExecResult
	detections  map[int64][]ToolDetection
	toolsets    map[int64]*Toolset
	checks      []fakeCheckCall

	nextExecID    int64
	nextToolsetID int64
}

func newFakeStore(machines ...*MachineRuntime) *fakeStore {
	st := &fakeStore{
		machines:    map[int64]*MachineRuntime{},
		execs:       map[int64]ExecRecord{},
		execResults: map[int64]ExecResult{},
		detections:  map[int64][]ToolDetection{},
		toolsets:    map[int64]*Toolset{},
	}
	for _, m := range machines {
		st.machines[m.ID] = m
	}
	return st
}

func (s *fakeStore) GetMachine(id int64) (*Machine, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rt, ok := s.machines[id]
	if !ok {
		return nil, nil
	}
	mc := rt.Machine
	return &mc, nil
}

func (s *fakeStore) GetMachineRuntime(id int64) (*MachineRuntime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rt, ok := s.machines[id]
	if !ok {
		return nil, nil
	}
	cp := *rt
	return &cp, nil
}

func (s *fakeStore) GetMachineByTask(taskID int64) (*Machine, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rt := range s.machines {
		if rt.AssignedTaskID == taskID {
			mc := rt.Machine
			return &mc, nil
		}
	}
	return nil, nil
}

func (s *fakeStore) ListMachinesForCheck() ([]MachineRuntime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []MachineRuntime{}
	for _, rt := range s.machines {
		out = append(out, *rt)
	}
	return out, nil
}

func (s *fakeStore) ListFreeOnlineMachines() ([]Machine, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Machine{}
	for _, rt := range s.machines {
		if rt.Status == StatusOnline && rt.AssignedTaskID == 0 {
			out = append(out, rt.Machine)
		}
	}
	return out, nil
}

func (s *fakeStore) UpdateMachineCheck(id int64, status string, ok bool, info *SysInfo, lastErr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rt, okk := s.machines[id]
	if !okk {
		return fmt.Errorf("机器 #%d 不存在", id)
	}
	if status != "" {
		rt.Status = status
	}
	if ok {
		rt.LastError = ""
		now := time.Now()
		rt.LastCheckAt = now
		rt.LastOnlineAt = now
	} else {
		rt.LastError = lastErr
		rt.LastCheckAt = time.Now()
	}
	if info != nil {
		rt.OsName, rt.OsVersion, rt.Kernel, rt.Arch = info.OSName, info.OSVersion, info.Kernel, info.Arch
		rt.CPUCores, rt.MemoryMB, rt.DiskGB = info.CPUCores, info.MemoryMB, info.DiskGB
	}
	s.checks = append(s.checks, fakeCheckCall{ID: id, Status: status, OK: ok, Info: info, Err: lastErr})
	return nil
}

func (s *fakeStore) AssignMachineToTask(machineID, taskID int64, by string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rt, ok := s.machines[machineID]
	if !ok || rt.AssignedTaskID != 0 {
		return fmt.Errorf("机器 #%d 已被占用或不存在", machineID)
	}
	rt.AssignedTaskID = taskID
	rt.Status = StatusBusy
	s.assignments = append(s.assignments, fakeAssignment{MachineID: machineID, TaskID: taskID, By: by, Open: true})
	return nil
}

func (s *fakeStore) ReleaseMachineFromTask(machineID, taskID int64, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rt, ok := s.machines[machineID]
	if ok && rt.AssignedTaskID == taskID {
		rt.AssignedTaskID = 0
		rt.Status = StatusOnline
	}
	for i := range s.assignments {
		a := &s.assignments[i]
		if a.MachineID == machineID && a.TaskID == taskID && a.Open {
			a.Open = false
			a.Reason = reason
		}
	}
	return nil
}

func (s *fakeStore) CreateExecution(rec ExecRecord) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextExecID++
	s.execs[s.nextExecID] = rec
	return s.nextExecID, nil
}

func (s *fakeStore) FinishExecution(id int64, res ExecResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.execs[id]; !ok {
		return fmt.Errorf("流水 #%d 不存在", id)
	}
	s.execResults[id] = res
	return nil
}

func (s *fakeStore) UpsertDetections(machineID int64, dets []ToolDetection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.detections[machineID] = dets
	return nil
}

func (s *fakeStore) GetToolset(id int64) (*Toolset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ts, ok := s.toolsets[id]; ok {
		return ts, nil
	}
	return nil, nil
}

func (s *fakeStore) GetToolsetByName(name string) (*Toolset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ts := range s.toolsets {
		if ts.Name == name {
			return ts, nil
		}
	}
	return nil, nil
}

// ---- fake SSHClient / PTYSession ----

type fakeSSH struct {
	mu       sync.Mutex
	execs    []string
	execFn   func(cmd string) ([]byte, int, error)
	sysInfo  *SysInfo
	sysErr   error
	detected []ToolDetection
	detErr   error
	ptys     []*fakePTY
	closed   bool
}

func (f *fakeSSH) Exec(_ context.Context, cmd string, _ time.Duration) ([]byte, int, error) {
	f.mu.Lock()
	f.execs = append(f.execs, cmd)
	fn := f.execFn
	f.mu.Unlock()
	if fn != nil {
		return fn(cmd)
	}
	return []byte("ok"), 0, nil
}

func (f *fakeSSH) OpenPTY(_ context.Context, cols, rows uint16) (PTYSession, error) {
	p := &fakePTY{cols: cols, rows: rows, exit: make(chan int, 1)}
	f.mu.Lock()
	f.ptys = append(f.ptys, p)
	f.mu.Unlock()
	return p, nil
}

func (f *fakeSSH) SysInfo(_ context.Context) (*SysInfo, error) {
	if f.sysErr != nil {
		return nil, f.sysErr
	}
	if f.sysInfo != nil {
		return f.sysInfo, nil
	}
	return &SysInfo{OSName: "ubuntu", OSVersion: "22.04", Kernel: "5.15.0", Arch: "x86_64", CPUCores: 2, MemoryMB: 2048, DiskGB: 40}, nil
}

func (f *fakeSSH) DetectTools(_ context.Context, names []string) ([]ToolDetection, error) {
	if f.detErr != nil {
		return nil, f.detErr
	}
	if f.detected != nil {
		return f.detected, nil
	}
	out := []ToolDetection{}
	for _, n := range names {
		out = append(out, ToolDetection{ToolName: n, Installed: n == "nmap" || n == "curl", Version: map[string]string{"nmap": "7.94", "curl": "8.5.0"}[n]})
	}
	return out, nil
}

func (f *fakeSSH) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeSSH) executedCmds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.execs...)
}

type fakePTY struct {
	rBuf   bytes.Buffer
	wBuf   bytes.Buffer
	exit   chan int
	cols   uint16
	rows   uint16
	mu     sync.Mutex
	closed bool
}

func (p *fakePTY) Read(b []byte) (int, error)     { return p.rBuf.Read(b) }
func (p *fakePTY) Write(b []byte) (int, error)    { return p.wBuf.Write(b) }
func (p *fakePTY) Resize(cols, rows uint16) error { p.cols, p.rows = cols, rows; return nil }
func (p *fakePTY) ExitCode() <-chan int           { return p.exit }
func (p *fakePTY) Close() error                   { p.mu.Lock(); p.closed = true; p.mu.Unlock(); return nil }
func (p *fakePTY) deliverExit(code int)           { p.exit <- code; close(p.exit) }
func (p *fakePTY) written() string                { p.mu.Lock(); defer p.mu.Unlock(); return p.wBuf.String() }

// ---- 测试脚手架 ----

func mkMachine(id int64, name, status string, assigned int64) *MachineRuntime {
	return &MachineRuntime{
		Machine: Machine{
			ID: id, Name: name, Host: "10.0.0." + fmt.Sprint(id), Port: 22, Username: "root",
			AuthType: AuthPassword, Status: status,
			WorkspaceBase: "/root/.artex-ws", AssignedTaskID: assigned, Tags: []string{},
		},
		Password: "pw",
	}
}

func newTestManager(t *testing.T, st *fakeStore, dial func(SSHConfig) (SSHClient, error)) *Manager {
	t.Helper()
	m := NewManager(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.newClient = dial
	m.BaseDir = t.TempDir()
	return m
}

func staticDial(cl SSHClient) func(SSHConfig) (SSHClient, error) {
	return func(SSHConfig) (SSHClient, error) { return cl, nil }
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", msg)
}

// ---- 指派 ----

func TestAssignAssignsAndCreatesWorkspace(t *testing.T) {
	st := newFakeStore(mkMachine(1, "box-1", StatusOnline, 0))
	cl := &fakeSSH{}
	m := newTestManager(t, st, staticDial(cl))

	mc, err := m.Assign(7, 1, "manual")
	if err != nil {
		t.Fatalf("Assign: %v", err)
	}
	if mc.Status != StatusBusy || mc.AssignedTaskID != 7 {
		t.Fatalf("指派后状态错误: %+v", mc)
	}
	if len(st.assignments) != 1 || st.assignments[0].TaskID != 7 || st.assignments[0].By != "manual" {
		t.Fatalf("指派历史未写: %+v", st.assignments)
	}
	// 建工作区目录的命令应已下发。
	cmds := cl.executedCmds()
	if len(cmds) != 1 || !strings.Contains(cmds[0], "mkdir -p '/root/.artex-ws/task_7'") {
		t.Fatalf("建工作区命令不符合预期: %v", cmds)
	}
}

func TestAssignIdempotentForSameTask(t *testing.T) {
	st := newFakeStore(mkMachine(1, "box-1", StatusOnline, 0))
	m := newTestManager(t, st, staticDial(&fakeSSH{}))

	if _, err := m.Assign(7, 1, "manual"); err != nil {
		t.Fatalf("first Assign: %v", err)
	}
	mc, err := m.Assign(7, 1, "manual")
	if err != nil {
		t.Fatalf("repeat Assign 应幂等成功: %v", err)
	}
	if mc.ID != 1 {
		t.Fatalf("幂等指派应返回同一台机器: %+v", mc)
	}
}

func TestAssignRejectsInvalidStates(t *testing.T) {
	cases := []struct {
		name    string
		machine *MachineRuntime
		wantErr string
	}{
		{"占用中", mkMachine(1, "box", StatusOnline, 99), "已被任务"},
		{"busy", mkMachine(2, "box", StatusBusy, 0), "可指派"},
		{"offline", mkMachine(3, "box", StatusOffline, 0), "可指派"},
		{"unknown", mkMachine(4, "box", StatusUnknown, 0), "可指派"},
		{"maintenance", mkMachine(5, "box", StatusMaintenance, 0), "可指派"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newFakeStore(tc.machine)
			m := newTestManager(t, st, staticDial(&fakeSSH{}))
			_, err := m.Assign(7, tc.machine.ID, "manual")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("期望错误含 %q,得到 %v", tc.wantErr, err)
			}
		})
	}
	t.Run("机器不存在", func(t *testing.T) {
		st := newFakeStore()
		m := newTestManager(t, st, staticDial(&fakeSSH{}))
		if _, err := m.Assign(7, 42, "manual"); err == nil {
			t.Fatal("期望错误,得到 nil")
		}
	})
}

func TestAssignAuto(t *testing.T) {
	st := newFakeStore(
		mkMachine(1, "maint", StatusMaintenance, 0),
		mkMachine(2, "unknown", StatusUnknown, 0),
		mkMachine(3, "offline", StatusOffline, 0),
		mkMachine(4, "taken", StatusOnline, 99),
		mkMachine(5, "free", StatusOnline, 0),
	)
	m := newTestManager(t, st, staticDial(&fakeSSH{}))

	mc, err := m.AssignAuto(7, "auto")
	if err != nil {
		t.Fatalf("AssignAuto: %v", err)
	}
	if mc.ID != 5 {
		t.Fatalf("应挑中唯一空闲在线机器 #5,得到 #%d", mc.ID)
	}
	// 再分配一台任务:已无可选。
	if _, err := m.AssignAuto(8, "auto"); err == nil {
		t.Fatal("无空闲机器时 AssignAuto 应报错")
	}
}

func TestReleaseIdempotent(t *testing.T) {
	st := newFakeStore(mkMachine(1, "box", StatusOnline, 0))
	m := newTestManager(t, st, staticDial(&fakeSSH{}))

	if _, err := m.Assign(7, 1, "manual"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	if err := m.Release(7, "manual"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	mc, _ := st.GetMachine(1)
	if mc.AssignedTaskID != 0 || mc.Status != StatusOnline {
		t.Fatalf("释放后机器应空闲在线: %+v", mc)
	}
	if a := st.assignments[0]; a.Open || a.Reason != "manual" {
		t.Fatalf("指派历史未关闭: %+v", a)
	}
	// 幂等:再释放一次 no-op nil。
	if err := m.Release(7, "manual"); err != nil {
		t.Fatalf("重复 Release 应 no-op nil: %v", err)
	}
	// 从未指派的任务释放也是 no-op nil。
	if err := m.Release(12345, "manual"); err != nil {
		t.Fatalf("无指派 Release 应 no-op nil: %v", err)
	}
}

func TestMachineForTask(t *testing.T) {
	st := newFakeStore(mkMachine(1, "box", StatusOnline, 0))
	m := newTestManager(t, st, staticDial(&fakeSSH{}))

	mc, err := m.MachineForTask(7)
	if err != nil || mc != nil {
		t.Fatalf("无指派应 (nil,nil),得到 (%v,%v)", mc, err)
	}
	if _, err := m.Assign(7, 1, "manual"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	mc, err = m.MachineForTask(7)
	if err != nil || mc == nil || mc.ID != 1 {
		t.Fatalf("MachineForTask 应返回 #1: %v %+v", err, mc)
	}
}

// ---- 执行 ----

func TestExecCommandWrapsCommandWithTaskWorkspace(t *testing.T) {
	st := newFakeStore(mkMachine(1, "box", StatusOnline, 0))
	cl := &fakeSSH{}
	m := newTestManager(t, st, staticDial(cl))

	out, code, err := m.ExecCommand(context.Background(), 1, 7, SourceAgent, "echo hi && uname -a", time.Minute)
	if err != nil || code != 0 || string(out) != "ok" {
		t.Fatalf("exec: out=%q code=%d err=%v", out, code, err)
	}
	cmds := cl.executedCmds()
	if len(cmds) != 1 {
		t.Fatalf("应恰好执行 1 条命令: %v", cmds)
	}
	if !strings.HasPrefix(cmds[0], "mkdir -p '/root/.artex-ws/task_7' && cd '/root/.artex-ws/task_7' && bash -lc ") {
		t.Fatalf("命令包装错误: %s", cmds[0])
	}
	if !strings.Contains(cmds[0], `bash -lc 'echo hi && uname -a'`) {
		t.Fatalf("命令体未按 bash -lc 包装: %s", cmds[0])
	}
	// 流水两段(开+回填)都有记录。
	rec := st.execs[1]
	if rec.Source != SourceAgent || rec.TaskID != 7 {
		t.Fatalf("流水记录错误: %+v", rec)
	}
	if st.execResults[1].ExitCode == nil || *st.execResults[1].ExitCode != 0 {
		t.Fatalf("流水退出码未回填: %+v", st.execResults[1])
	}
}

func TestExecCommandQuotesSingleQuotes(t *testing.T) {
	st := newFakeStore(mkMachine(1, "box", StatusOnline, 0))
	cl := &fakeSSH{}
	m := newTestManager(t, st, staticDial(cl))

	cmd := `grep -r "it's" /tmp`
	if _, _, err := m.ExecCommand(context.Background(), 1, 3, SourceAgent, cmd, time.Minute); err != nil {
		t.Fatalf("exec: %v", err)
	}
	got := cl.executedCmds()[0]
	if !strings.Contains(got, `'grep -r "it'\''s" /tmp'`) {
		t.Fatalf("单引号未转义: %s", got)
	}
}

func TestExecCommandManualWorkspaceForZeroTask(t *testing.T) {
	st := newFakeStore(mkMachine(1, "box", StatusOnline, 0))
	cl := &fakeSSH{}
	m := newTestManager(t, st, staticDial(cl))

	if _, _, err := m.ExecCommand(context.Background(), 1, 0, SourceManual, "ls", time.Minute); err != nil {
		t.Fatalf("exec: %v", err)
	}
	got := cl.executedCmds()[0]
	if !strings.Contains(got, "/root/.artex-ws/manual_") {
		t.Fatalf("taskID=0 应使用 manual_<时间戳> 工作区: %s", got)
	}
}

func TestExecCommandWritesOutputFile(t *testing.T) {
	st := newFakeStore(mkMachine(1, "box", StatusOnline, 0))
	big := strings.Repeat("A", 5000)
	cl := &fakeSSH{execFn: func(string) ([]byte, int, error) { return []byte(big), 0, nil }}
	m := newTestManager(t, st, staticDial(cl))

	out, _, err := m.ExecCommand(context.Background(), 1, 7, SourceAgent, "generate", time.Minute)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if len(out) != 5000 {
		t.Fatalf("完整输出应原样返回: %d", len(out))
	}
	res := st.execResults[1]
	if res.OutputSize != 5000 {
		t.Fatalf("流水应记真实输出大小 5000: %+v", res)
	}
	if res.OutputPath == "" {
		t.Fatal("输出应落盘")
	}
	data, rerr := os.ReadFile(res.OutputPath)
	if rerr != nil || len(data) != 5000 {
		t.Fatalf("落盘文件应含完整输出: %v %d", rerr, len(data))
	}
	if filepath.Base(res.OutputPath) != "1.txt" {
		t.Fatalf("输出文件名应为 <execID>.txt: %s", res.OutputPath)
	}
}

func TestExecCommandDialFailureRecordsExecution(t *testing.T) {
	st := newFakeStore(mkMachine(1, "box", StatusOnline, 0))
	m := newTestManager(t, st, func(SSHConfig) (SSHClient, error) {
		return nil, fmt.Errorf("connection refused")
	})

	out, code, err := m.ExecCommand(context.Background(), 1, 7, SourceAgent, "ls", time.Minute)
	if err == nil || code != -1 || out != nil {
		t.Fatalf("拨号失败应 (nil,-1,err): %q %d %v", out, code, err)
	}
	if res := st.execResults[1]; !strings.Contains(res.Error, "connection refused") || res.ExitCode != nil {
		t.Fatalf("失败流水未记错误: %+v", res)
	}
}

// ---- 终端 ----

func TestOpenPTYSlotLimitAndTerminalRecord(t *testing.T) {
	st := newFakeStore(mkMachine(1, "box", StatusOnline, 0))
	cl := &fakeSSH{}
	m := newTestManager(t, st, staticDial(cl))

	s1, err := m.OpenPTY(context.Background(), 1, 120, 36)
	if err != nil {
		t.Fatalf("OpenPTY #1: %v", err)
	}
	s2, err := m.OpenPTY(context.Background(), 1, 120, 36)
	if err != nil {
		t.Fatalf("OpenPTY #2: %v", err)
	}
	if _, err := m.OpenPTY(context.Background(), 1, 120, 36); err == nil {
		t.Fatal("第 3 路终端应被拒(单机上限 2)")
	}

	// 键入被透传,首行被捕获。
	if _, err := s1.Write([]byte("whoami\r")); err != nil {
		t.Fatalf("pty write: %v", err)
	}
	if got := s1.(*terminalSession).inner.(*fakePTY).written(); got != "whoami\r" {
		t.Fatalf("键入未透传: %q", got)
	}
	if err := s1.Resize(200, 50); err != nil {
		t.Fatalf("resize: %v", err)
	}

	// 会话结束 → 记 source=terminal 流水(首条输入 + 时长)并释放槽位。
	p1 := cl.ptys[0]
	p1.deliverExit(0)
	<-s1.ExitCode()
	waitFor(t, 2*time.Second, func() bool {
		for _, rec := range st.execs {
			if rec.Source == SourceTerminal {
				return true
			}
		}
		return false
	}, "终端流水未落")
	var termRec ExecRecord
	for _, rec := range st.execs {
		if rec.Source == SourceTerminal {
			termRec = rec
		}
	}
	if !strings.Contains(termRec.Command, "whoami") {
		t.Fatalf("终端流水应记首条输入: %+v", termRec)
	}

	// 槽位已释放:能再开一路。
	if _, err := m.OpenPTY(context.Background(), 1, 120, 36); err != nil {
		t.Fatalf("关闭后应可再开终端: %v", err)
	}
	_ = s2.Close()
}

func TestOpenPTYDialFailureReleasesSlot(t *testing.T) {
	st := newFakeStore(mkMachine(1, "box", StatusOnline, 0))
	m := newTestManager(t, st, func(SSHConfig) (SSHClient, error) {
		return nil, fmt.Errorf("unreachable")
	})
	if _, err := m.OpenPTY(context.Background(), 1, 120, 36); err == nil {
		t.Fatal("拨号失败应报错")
	}
	if _, err := m.OpenPTY(context.Background(), 1, 120, 36); err == nil {
		t.Fatal("拨号失败后槽位必须已释放(否则永远开不了)")
	}
}

// ---- 测活 ----

func TestCheckMachineRefreshesSysInfo(t *testing.T) {
	st := newFakeStore(mkMachine(1, "box", StatusUnknown, 0))
	cl := &fakeSSH{sysInfo: &SysInfo{OSName: "debian", OSVersion: "12", Kernel: "6.1.0", Arch: "aarch64", CPUCores: 4, MemoryMB: 8192, DiskGB: 80}}
	m := newTestManager(t, st, staticDial(cl))

	mc, err := m.CheckMachine(context.Background(), 1)
	if err != nil {
		t.Fatalf("CheckMachine: %v", err)
	}
	if mc.Status != StatusOnline || mc.OsName != "debian" || mc.CPUCores != 4 {
		t.Fatalf("测活后档案未回填: %+v", mc)
	}
	if len(st.checks) != 1 || !st.checks[0].OK {
		t.Fatalf("测活回写缺失: %+v", st.checks)
	}
}

func TestCheckMachineBusyStaysBusy(t *testing.T) {
	rt := mkMachine(1, "box", StatusBusy, 0)
	rt.AssignedTaskID = 9
	st := newFakeStore(rt)
	m := newTestManager(t, st, staticDial(&fakeSSH{}))

	mc, err := m.CheckMachine(context.Background(), 1)
	if err != nil {
		t.Fatalf("CheckMachine: %v", err)
	}
	if mc.Status != StatusBusy {
		t.Fatalf("busy 机器测活不许改状态: %+v", mc)
	}
	if st.checks[0].Status != "" {
		t.Fatalf("busy 机器应上报空 status(保持原状态): %+v", st.checks[0])
	}
}

func TestCheckMachineOfflineOnError(t *testing.T) {
	st := newFakeStore(mkMachine(1, "box", StatusOnline, 0))
	m := newTestManager(t, st, func(SSHConfig) (SSHClient, error) {
		return nil, fmt.Errorf("i/o timeout")
	})

	mc, err := m.CheckMachine(context.Background(), 1)
	if err != nil {
		t.Fatalf("CheckMachine 应吞掉连接错误返回机器: %v", err)
	}
	if mc.Status != StatusOffline || !strings.Contains(mc.LastError, "i/o timeout") {
		t.Fatalf("测活失败应 offline+last_error: %+v", mc)
	}
}

func TestCheckMachineBusyNotFlippedOffline(t *testing.T) {
	rt := mkMachine(1, "box", StatusBusy, 0)
	rt.AssignedTaskID = 9
	st := newFakeStore(rt)
	m := newTestManager(t, st, func(SSHConfig) (SSHClient, error) {
		return nil, fmt.Errorf("down")
	})

	mc, _ := m.CheckMachine(context.Background(), 1)
	if mc.Status != StatusBusy {
		t.Fatalf("busy 机器测活失败不许翻 offline: %+v", mc)
	}
	if !strings.Contains(mc.LastError, "down") {
		t.Fatalf("busy 失败也应留 last_error: %+v", mc)
	}
}

// ---- 探测与工具集 ----

func TestDetectMachine(t *testing.T) {
	st := newFakeStore(mkMachine(1, "box", StatusOnline, 0))
	cl := &fakeSSH{detected: []ToolDetection{{ToolName: "nmap", Installed: true, Version: "7.94"}, {ToolName: "curl", Installed: true, Version: "8.5.0"}}}
	m := newTestManager(t, st, staticDial(cl))

	dets, err := m.DetectMachine(context.Background(), 1)
	if err != nil {
		t.Fatalf("DetectMachine: %v", err)
	}
	if len(dets) != 2 || dets[0].Version != "7.94" {
		t.Fatalf("探测结果错误: %+v", dets)
	}
	if len(st.detections[1]) != 2 {
		t.Fatalf("检测矩阵未落库: %+v", st.detections)
	}
	found := false
	for _, rec := range st.execs {
		if rec.Source == SourceDetect {
			found = true
		}
	}
	if !found {
		t.Fatal("探测应记 source=detect 流水")
	}
}

func TestApplyToolset(t *testing.T) {
	st := newFakeStore(mkMachine(1, "box", StatusOnline, 0))
	st.toolsets[3] = &Toolset{ID: 3, Name: "基础侦察", Script: "apt-get install -y nmap || true"}
	cl := &fakeSSH{}
	m := newTestManager(t, st, staticDial(cl))

	id, out, code, err := m.ApplyToolset(context.Background(), 1, 3)
	if err != nil || code != 0 || string(out) != "ok" || id == 0 {
		t.Fatalf("ApplyToolset: id=%d out=%q code=%d err=%v", id, out, code, err)
	}
	cmds := cl.executedCmds()
	if len(cmds) != 1 || !strings.Contains(cmds[0], "apt-get install -y nmap") {
		t.Fatalf("工具集脚本未按包装执行: %v", cmds)
	}
	if !strings.HasPrefix(cmds[0], "mkdir -p '/root/.artex-ws' && cd '/root/.artex-ws'") {
		t.Fatalf("工具集应跑在 workspace_base 根目录: %s", cmds[0])
	}
	rec := st.execs[id]
	if rec.Source != SourceToolset || rec.ToolsetID != 3 || rec.TaskID != 0 {
		t.Fatalf("工具集流水记录错误: %+v", rec)
	}

	t.Run("工具集不存在", func(t *testing.T) {
		if _, _, _, err := m.ApplyToolset(context.Background(), 1, 999); err == nil {
			t.Fatal("期望错误")
		}
	})
}

func TestSeedBuiltinToolsetsIdempotent(t *testing.T) {
	st := newFakeStore()
	created := map[string]string{}
	create := func(name, desc, script string) (int64, error) {
		created[name] = script
		st.nextToolsetID++
		st.toolsets[st.nextToolsetID] = &Toolset{ID: st.nextToolsetID, Name: name, Description: desc, Script: script, Builtin: true}
		return st.nextToolsetID, nil
	}

	if err := SeedBuiltinToolsets(st, create); err != nil {
		t.Fatalf("SeedBuiltinToolsets: %v", err)
	}
	if len(created) != 3 {
		t.Fatalf("应播种 3 个内置工具集: %v", created)
	}
	for _, want := range []string{"基础侦察", "Web漏扫", "爆破破解"} {
		if _, ok := created[want]; !ok {
			t.Fatalf("缺内置工具集 «%s»", want)
		}
	}
	// 幂等:再播种一次不新增。
	if err := SeedBuiltinToolsets(st, create); err != nil {
		t.Fatalf("二次播种: %v", err)
	}
	if len(created) != 3 {
		t.Fatalf("二次播种不应新增: %v", created)
	}
	// 用户改过的种子(同名)不被覆盖。
	st.toolsets[1].Script = "user-modified"
	if err := SeedBuiltinToolsets(st, create); err != nil {
		t.Fatalf("三次播种: %v", err)
	}
	if st.toolsets[1].Script != "user-modified" {
		t.Fatal("已存在工具集不许被种子覆盖")
	}
	// 脚本幂等纪律:开头有 golang 兜底,安装步都带 || true / -y。
	for _, ts := range BuiltinToolsets() {
		if !strings.Contains(ts.Script, "command -v go") {
			t.Fatalf("«%s» 脚本缺 golang 兜底", ts.Name)
		}
		if !strings.Contains(ts.Script, "|| true") {
			t.Fatalf("«%s» 脚本缺 || true 幂等兜底", ts.Name)
		}
	}
}

// ---- 工具清单 ----

func TestKnownToolsCatalog(t *testing.T) {
	names := KnownToolNames()
	if len(names) != 19 {
		t.Fatalf("已知工具应 19 个: %d", len(names))
	}
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Fatalf("工具重复: %s", n)
		}
		seen[n] = true
		if ToolProbeByName(n) == nil {
			t.Fatalf("«%s» 缺探测定义", n)
		}
	}
	if v := parseToolVersion("git", "git version 2.43.0"); v != "2.43.0" {
		t.Fatalf("git 版本解析错误: %q", v)
	}
	if v := parseToolVersion("nmap", "Nmap version 7.94 ( https://nmap.org )"); v != "7.94" {
		t.Fatalf("nmap 版本解析错误: %q", v)
	}
	if v := parseToolVersion("hashcat", "v6.2.6"); v != "6.2.6" {
		t.Fatalf("hashcat 版本解析错误: %q", v)
	}
	if v := parseToolVersion("python3", "Python 3.11.8"); v != "3.11.8" {
		t.Fatalf("python3 版本解析错误: %q", v)
	}
	if v := parseToolVersion("curl", "curl 8.5.0 (x86_64-pc-linux-gnu)"); v != "8.5.0" {
		t.Fatalf("curl 版本解析错误: %q", v)
	}
}

func TestDetectToolsScriptAndParsing(t *testing.T) {
	// 探测脚本应含 command -v 存在性判别与逐工具版本段。
	script := buildDetectScript([]string{"nmap", "masscan", "git"})
	if !strings.Contains(script, "command -v") || !strings.Contains(script, "git --version") {
		t.Fatalf("探测脚本不完整:\n%s", script)
	}
	out := "I|nmap\nN|masscan\nI|git\n" +
		"__ARTEX_V__nmap\nNmap version 7.94 ( https://nmap.org )\n" +
		"__ARTEX_V__git\ngit version 2.43.0\n"
	dets := parseDetectOutput([]string{"nmap", "masscan", "git"}, out)
	if len(dets) != 3 {
		t.Fatalf("应 3 条结果: %+v", dets)
	}
	byName := map[string]ToolDetection{}
	for _, d := range dets {
		byName[d.ToolName] = d
	}
	if !byName["nmap"].Installed || byName["nmap"].Version != "7.94" {
		t.Fatalf("nmap 探测错误: %+v", byName["nmap"])
	}
	if byName["masscan"].Installed {
		t.Fatalf("masscan 未装: %+v", byName["masscan"])
	}
	if !byName["git"].Installed || byName["git"].Version != "2.43.0" {
		t.Fatalf("git 探测错误: %+v", byName["git"])
	}
}
