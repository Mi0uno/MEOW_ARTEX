package server

// VPS 中心:Server 侧接入。
//
// 本文件分三部分(照 proxy_center.go 的组织方式):
//   1. vpsStore   —— 把 db 层适配成 vpscenter.Store(包边界:vpscenter 不依赖 db),
//                    凭据解密只发生在 GetMachineRuntime → vpscenter.Dial 之前;
//   2. initVPS    —— 构造 Manager、播种内置工具集、开机对账(悬空指派自动释放)、
//                    启动后台测活循环;
//   3. 凭据加解密 —— secretbox 封装,机器录入/编辑(seal)与拨号(open)共用。
//
// REST 接口在 vps_api.go,WebSocket 终端在 vps_terminal.go。

import (
	"fmt"
	"log"
	"log/slog"
	"sync"
	"time"

	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/vpscenter"
)

// vpsManager 是进程内唯一的 VPS 中心运行时。
//
// 为什么是包级变量而不是 Server 字段:server.go 的 Server/Manager 结构体不在本模块
// 文件所有权内(契约只允许在 New()/Handler() 各加一行),而 VPS 中心是单例运行时,
// 与进程一一对应——用带锁的包级单例即可,语义等价且不越界。
var (
	vpsMgrMu sync.RWMutex
	vpsMgr   *vpscenter.Manager
)

// setVPSManager 绑定进程级 Manager(initVPS 调用)。
func setVPSManager(m *vpscenter.Manager) {
	vpsMgrMu.Lock()
	defer vpsMgrMu.Unlock()
	vpsMgr = m
}

// VPSManager 返回进程级 VPS 中心运行时(未初始化返回 nil)。
// Agent 集成(Agent C)经它拿句柄:MachineForTask / ExecCommand / Assign / Release。
func VPSManager() *vpscenter.Manager {
	vpsMgrMu.RLock()
	defer vpsMgrMu.RUnlock()
	return vpsMgr
}

// ---- Store 适配器:vpscenter.Store → db ----

// vpsStore 实现 vpscenter.Store(凭据在本层解密,明文只进 Manager 的运行时视图)。
type vpsStore struct {
	s  *Server
	pg *pgdb.DB
}

func newVPSStore(s *Server, pg *pgdb.DB) *vpsStore { return &vpsStore{s: s, pg: pg} }

// machineFromDB 把 db 机器行转成 vpscenter 公共视图(永不携带凭据)。
func machineFromDB(m *pgdb.VpsMachine) *vpscenter.Machine {
	return &vpscenter.Machine{
		ID: m.ID, Name: m.Name, Host: m.Host, Port: m.Port,
		Username: m.Username, AuthType: m.AuthType, Status: m.Status,
		OsName: m.OsName, OsVersion: m.OsVersion, Kernel: m.Kernel, Arch: m.Arch,
		CPUCores: m.CPUCores, MemoryMB: m.MemoryMB, DiskGB: m.DiskGB,
		Region: m.Region, Provider: m.Provider, Tags: m.Tags,
		WorkspaceBase: m.WorkspaceBase, AssignedTaskID: m.AssignedTaskID,
		LastError: m.LastError,
		LastCheckAt: func() time.Time {
			if m.LastCheckAt != nil {
				return *m.LastCheckAt
			}
			return time.Time{}
		}(),
		LastOnlineAt: func() time.Time {
			if m.LastOnlineAt != nil {
				return *m.LastOnlineAt
			}
			return time.Time{}
		}(),
	}
}

// runtimeFromDB 读机器 + 解密凭据(密文只在进程内短暂存在)。
func (p *vpsStore) runtimeFromDB(m *pgdb.VpsMachine) (*vpscenter.MachineRuntime, error) {
	rt := &vpscenter.MachineRuntime{Machine: *machineFromDB(m)}
	pw, pk, ph, err := p.pg.GetVpsMachineSecrets(m.ID)
	if err != nil {
		return nil, err
	}
	if p.s.apiSecret != nil {
		if len(pw) > 0 {
			if plain, oerr := p.s.apiSecret.open(pw); oerr == nil {
				rt.Password = string(plain)
			} else {
				return nil, fmt.Errorf("解密机器 #%d 密码失败: %w", m.ID, oerr)
			}
		}
		if len(pk) > 0 {
			plain, oerr := p.s.apiSecret.open(pk)
			if oerr != nil {
				return nil, fmt.Errorf("解密机器 #%d 私钥失败: %w", m.ID, oerr)
			}
			rt.PrivateKey = plain
		}
		if len(ph) > 0 {
			plain, oerr := p.s.apiSecret.open(ph)
			if oerr != nil {
				return nil, fmt.Errorf("解密机器 #%d 私钥口令失败: %w", m.ID, oerr)
			}
			rt.KeyPassphrase = string(plain)
		}
	} else {
		// 加密盒未初始化(异常路径):凭据可用性按空处理,拨号时会得到明确错误。
		log.Printf("[vps-center] 凭据加密盒未初始化,机器 #%d 无法解密凭据", m.ID)
	}
	return rt, nil
}

func (p *vpsStore) GetMachine(id int64) (*vpscenter.Machine, error) {
	m, err := p.pg.GetVpsMachine(id)
	if err != nil || m == nil {
		return nil, err
	}
	return machineFromDB(m), nil
}

func (p *vpsStore) GetMachineRuntime(id int64) (*vpscenter.MachineRuntime, error) {
	m, err := p.pg.GetVpsMachine(id)
	if err != nil || m == nil {
		return nil, err
	}
	return p.runtimeFromDB(m)
}

func (p *vpsStore) GetMachineByTask(taskID int64) (*vpscenter.Machine, error) {
	m, err := p.pg.GetVpsMachineByTask(taskID)
	if err != nil || m == nil {
		return nil, err
	}
	return machineFromDB(m), nil
}

// ListMachinesForCheck 取全量机器(含解密凭据)。机器量级为用户手动录入的
// 个位数到百级,一次全量可控。
func (p *vpsStore) ListMachinesForCheck() ([]vpscenter.MachineRuntime, error) {
	rows, _, err := p.pg.ListVpsMachines(pgdb.VpsMachineFilter{Limit: 500})
	if err != nil {
		return nil, err
	}
	out := make([]vpscenter.MachineRuntime, 0, len(rows))
	for _, m := range rows {
		rt, err := p.runtimeFromDB(m)
		if err != nil {
			// 解密失败的机器跳过本轮测活(会在日志留痕),不拖垮整轮。
			log.Printf("[vps-center] 机器 #%d 凭据解密失败,跳过测活: %v", m.ID, err)
			continue
		}
		out = append(out, *rt)
	}
	return out, nil
}

func (p *vpsStore) ListFreeOnlineMachines() ([]vpscenter.Machine, error) {
	rows, _, err := p.pg.ListVpsMachines(pgdb.VpsMachineFilter{Status: pgdb.VpsStatusOnline, Limit: 500})
	if err != nil {
		return nil, err
	}
	out := []vpscenter.Machine{}
	for _, m := range rows {
		if m.AssignedTaskID == 0 {
			out = append(out, *machineFromDB(m))
		}
	}
	return out, nil
}

func (p *vpsStore) UpdateMachineCheck(id int64, status string, ok bool, info *vpscenter.SysInfo, lastErr string) error {
	var dbInfo *pgdb.VpsSysInfo
	if info != nil {
		dbInfo = &pgdb.VpsSysInfo{
			OsName: info.OSName, OsVersion: info.OSVersion, Kernel: info.Kernel, Arch: info.Arch,
			CPUCores: info.CPUCores, MemoryMB: info.MemoryMB, DiskGB: info.DiskGB,
		}
	}
	return p.pg.UpdateVpsMachineCheck(id, status, ok, dbInfo, lastErr)
}

func (p *vpsStore) AssignMachineToTask(machineID, taskID int64, by string) error {
	return p.pg.AssignVpsMachineToTask(machineID, taskID, by)
}

func (p *vpsStore) ReleaseMachineFromTask(machineID, taskID int64, reason string) error {
	return p.pg.ReleaseVpsMachineFromTask(machineID, taskID, reason)
}

func (p *vpsStore) CreateExecution(rec vpscenter.ExecRecord) (int64, error) {
	return p.pg.CreateVpsExecution(rec.MachineID, rec.TaskID, rec.Source, rec.ToolsetID, rec.Command)
}

func (p *vpsStore) FinishExecution(id int64, res vpscenter.ExecResult) error {
	return p.pg.FinishVpsExecution(id, res.ExitCode, res.DurationMs, res.OutputSize, res.OutputPath, res.Error)
}

func (p *vpsStore) UpsertDetections(machineID int64, dets []vpscenter.ToolDetection) error {
	out := make([]pgdb.VpsToolDetection, 0, len(dets))
	for _, d := range dets {
		out = append(out, pgdb.VpsToolDetection{ToolName: d.ToolName, Installed: d.Installed, Version: d.Version})
	}
	return p.pg.UpsertVpsToolDetections(machineID, out)
}

func (p *vpsStore) GetToolset(id int64) (*vpscenter.Toolset, error) {
	t, err := p.pg.GetVpsToolset(id)
	if err != nil || t == nil {
		return nil, err
	}
	return &vpscenter.Toolset{ID: t.ID, Name: t.Name, Description: t.Description, Script: t.Script, Builtin: t.Builtin}, nil
}

func (p *vpsStore) GetToolsetByName(name string) (*vpscenter.Toolset, error) {
	t, err := p.pg.GetVpsToolsetByName(name)
	if err != nil || t == nil {
		return nil, err
	}
	return &vpscenter.Toolset{ID: t.ID, Name: t.Name, Description: t.Description, Script: t.Script, Builtin: t.Builtin}, nil
}

// ---- 凭据加解密(响应永不回显明文;这里只服务入库与拨号) ----

// sealVPSCredential 把明文凭据加密成入库密文。
func (s *Server) sealVPSCredential(plain string) ([]byte, error) {
	if s.apiSecret == nil {
		return nil, fmt.Errorf("凭据加密盒未初始化")
	}
	return s.apiSecret.seal([]byte(plain))
}

// openVPSCredential 解开入库密文(仅拨号前使用)。
func (s *Server) openVPSCredential(sealed []byte) ([]byte, error) {
	if s.apiSecret == nil {
		return nil, fmt.Errorf("凭据加密盒未初始化")
	}
	return s.apiSecret.open(sealed)
}

// ---- 初始化编排 ----

// initVPS 构造 VPS 中心运行时并启动后台任务(New() 调用,照 initProxyCenter 惯例):
//  1. 播种内置工具集(幂等:只按 name 插缺失的,不覆盖用户改动);
//  2. 开机对账:assigned_task_id 指向不存在或非 running/paused 任务的机器自动
//     Release(进程重启后任务终态没来得及释放、或任务行被清掉的情况);
//  3. 启动后台测活循环(默认 60s 一轮)。
func (s *Server) initVPS() {
	if s.m == nil || s.m.PG() == nil {
		return
	}
	pg := s.m.PG()
	store := newVPSStore(s, pg)
	mgr := vpscenter.NewManager(store, slog.Default())
	mgr.BaseDir = s.m.dir
	setVPSManager(mgr)

	// 内置工具集播种:只补缺失,已存在的一律不覆盖。
	if err := vpscenter.SeedBuiltinToolsets(store, func(name, description, script string) (int64, error) {
		t, err := pg.CreateVpsToolset(pgdb.VpsToolsetInput{Name: name, Description: description, Script: script}, true)
		if err != nil {
			return 0, err
		}
		log.Printf("[vps-center] 已播种内置工具集 «%s»(#%d)", name, t.ID)
		return t.ID, nil
	}); err != nil {
		log.Printf("[vps-center] 内置工具集播种失败: %v", err)
	}

	// 开机对账:悬空指派自动释放(tasks 表查状态即可,判据与 Agent C 的
	// 任务终态自动释放一致;paused 不释放——会话可恢复)。
	stale, err := pg.ListStaleVpsAssignedMachines()
	if err != nil {
		log.Printf("[vps-center] 开机对账读取悬空指派失败: %v", err)
	} else {
		for _, m := range stale {
			if err := mgr.Release(m.AssignedTaskID, "startup_reconcile"); err != nil {
				log.Printf("[vps-center] 开机对账释放机器 «%s»(任务 #%d)失败: %v",
					m.Name, m.AssignedTaskID, err)
				continue
			}
			log.Printf("[vps-center] 开机对账:机器 «%s»(#%d) 已从悬空任务 #%d 释放",
				m.Name, m.ID, m.AssignedTaskID)
		}
		if len(stale) > 0 {
			log.Printf("[vps-center] 开机对账完成:释放 %d 台悬空机器", len(stale))
		}
	}

	go mgr.RunCheckLoop(s.ctx)
	log.Printf("[vps-center] VPS 中心已启动(机器池/终端/工具集/流水)")
}

// vpsMachineDTO 是给 API 用的便捷转换:db 机器行本身就是响应 DTO
// (无凭据字段,tags 已保证非 nil)。
func vpsMachineDTO(m *pgdb.VpsMachine) *pgdb.VpsMachine {
	if m == nil {
		return nil
	}
	if m.Tags == nil {
		m.Tags = []string{}
	}
	return m
}

// vpsRuntimeMachineDTO 把 vpscenter.Manager 返回的运行时视图(无 json tag)转成
// API 响应 DTO。CheckMachine/Assign 系列走的是这个类型,裸序列化会变成大写
// 字段名打崩前端,凡是这类返回值必须过这里。
func vpsRuntimeMachineDTO(m *vpscenter.Machine) *pgdb.VpsMachine {
	if m == nil {
		return nil
	}
	out := &pgdb.VpsMachine{
		ID:             m.ID,
		Name:           m.Name,
		Host:           m.Host,
		Port:           m.Port,
		Username:       m.Username,
		AuthType:       m.AuthType,
		Status:         m.Status,
		OsName:         m.OsName,
		OsVersion:      m.OsVersion,
		Kernel:         m.Kernel,
		Arch:           m.Arch,
		CPUCores:       m.CPUCores,
		MemoryMB:       m.MemoryMB,
		DiskGB:         m.DiskGB,
		Region:         m.Region,
		Provider:       m.Provider,
		Tags:           m.Tags,
		WorkspaceBase:  m.WorkspaceBase,
		AssignedTaskID: m.AssignedTaskID,
		LastError:      m.LastError,
	}
	if out.Tags == nil {
		out.Tags = []string{}
	}
	if !m.LastCheckAt.IsZero() {
		t := m.LastCheckAt
		out.LastCheckAt = &t
	}
	if !m.LastOnlineAt.IsZero() {
		t := m.LastOnlineAt
		out.LastOnlineAt = &t
	}
	return out
}

// 编译期断言:适配器必须满足 vpscenter.Store。
var _ vpscenter.Store = (*vpsStore)(nil)
