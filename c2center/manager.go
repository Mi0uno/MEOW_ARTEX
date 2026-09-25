// Package c2center 是 C2 中心的领域包:自研轻量 beacon 的 HTTP 监听器、会话注册/
// 心跳/休眠/终止、shell 与文件通道、载荷生成与投递(契约见 C2_CONTRACTS.md)。
//
// 分层纪律(仿 vpscenter/proxycenter,违反 = 架构破坏):
//   - 本包只管「运行时状态」(HTTP 监听器、防重放表、blob 落盘),不 import db/server;
//     一切持久化经 Store 接口回到 server/db 层,单测注入内存 fake。
//   - 明文 enc_key 只在 server 适配器解封后经 GetListenerKey 交给本包,本包不落盘密钥、
//     不经任何 API 回显。
//   - 硬性纪律:本包禁止执行任何进程(标准库的进程启动包在禁用清单里,连常量参数都拦);
//     shell 全部由目标机上的 beacon 自身完成。gosrc 载荷的生成源码里出现的进程执行
//     import 是「载荷内容」数据,由占位符在渲染期拼接,不属于本包的依赖。
//
// 监听器生命周期仿 proxycenter/pool.go 的 Pool:每个监听器一个独立端口的 http.Server,
// New 不启动,Start 里 net.Listen + goroutine Serve,Stop 用 sync.Once + Shutdown。
package c2center

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// 会话状态(与 schema CHECK 对齐)。
const (
	StatusOnline  = "online"
	StatusOffline = "offline"
	StatusDead    = "dead"
)

// 任务状态(与 schema CHECK 对齐)。
const (
	TaskQueued = "queued"
	TaskSent   = "sent"
	TaskDone   = "done"
	TaskError  = "error"
)

// 传输方向:up=平台→目标(blob 在 uploads/,beacon 只读);down=目标→平台(产物在
// downloads/,beacon 只写)。
const (
	DirectionUp   = "up"
	DirectionDown = "down"
)

// Listener 是监听器的持久化视图。enc_key 永不出现在此结构(server 适配器经
// GetListenerKey 单独交密钥);SessionCount 由 ListListeners 实现方回填。
type Listener struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	Proto        string    `json:"proto"` // http(https 二期)
	BindHost     string    `json:"bind_host"`
	Port         int       `json:"port"` // 0=启动时自动分配
	URLPath      string    `json:"url_path"`
	Callback     string    `json:"callback"`
	Enabled      bool      `json:"enabled"`
	SessionCount int       `json:"session_count"`
	Notes        string    `json:"notes"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ListenerInput 是创建/更新监听器的入参(proto 固定 http,不在入参里;enabled 走
// SetListenerEnabled;enc_key 由 server 适配器在 CreateListener 内生成加密)。
type ListenerInput struct {
	Name     string `json:"name"`
	BindHost string `json:"bind_host"`
	Port     int    `json:"port"`
	URLPath  string `json:"url_path"`
	Callback string `json:"callback"`
	Notes    string `json:"notes"`
}

// Session 是一台上线主机。sid 是 beacon 持有的会话令牌;listener_id+beacon_uid 幂等复活。
// TaskID 0=未绑定(schema 可空列的 COALESCE 惯例)。
type Session struct {
	ID         int64          `json:"id"`
	ListenerID int64          `json:"listener_id"`
	BeaconUID  string         `json:"beacon_uid"`
	SID        string         `json:"sid"`
	Hostname   string         `json:"hostname"`
	Username   string         `json:"username"`
	Elevated   bool           `json:"elevated"`
	OS         string         `json:"os"`
	Arch       string         `json:"arch"`
	LocalIPs   string         `json:"local_ips"`
	PID        int            `json:"pid"`
	Process    string         `json:"process"`
	SleepSec   int            `json:"sleep_sec"`
	JitterPct  int            `json:"jitter_pct"`
	TaskID     int64          `json:"task_id"`
	Status     string         `json:"status"`
	Labels     string         `json:"labels"`
	Meta       map[string]any `json:"meta"`
	FirstSeen  time.Time      `json:"first_seen"`
	LastSeen   time.Time      `json:"last_seen"`
}

// SessionInput 是 reg 心跳的入参。UpsertSession 按 listener_id+beacon_uid 幂等:
// 存在则更新主机信息、sid 换成新令牌并置 online(刷新 last_seen),不存在则建
// (sleep/jitter 用库默认 10/20)。调用方须保证 Status 为 dead 的会话不被心跳复活
// ——c2center 的监听器在心跳路径已做 guard,server 适配器建议同样兜底。
type SessionInput struct {
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
}

// SessionFilter 是会话列表筛选(零值=不筛)。
type SessionFilter struct {
	Status     string `json:"status"`
	ListenerID int64  `json:"listener_id"`
	TaskID     int64  `json:"task_id"`
	Q          string `json:"q"`
	CompanyID  int64  `json:"company_id"`
	Limit      int    `json:"limit"`
	Offset     int    `json:"offset"`
}

// Task 是一条下发任务。Input 约定(监听器按此解析,server 下发时照此构造):
// shell={"command":"..."};files_list={"path":"..."};upload/download={"transfer_id":N,"remote_path":"..."}。
type Task struct {
	ID         int64      `json:"id"`
	SessionID  int64      `json:"session_id"`
	Kind       string     `json:"kind"` // shell|files_list|upload|download|loot|info
	Input      string     `json:"input"`
	Output     string     `json:"output"` // ≤256KB 存全文;大输出只存 2KB 预览
	OutputPath string     `json:"output_path"`
	OutputSize int64      `json:"output_size"`
	Status     string     `json:"status"`
	Actor      string     `json:"actor"` // user|agent
	CreatedBy  string     `json:"created_by"`
	Error      string     `json:"error"`
	CreatedAt  time.Time  `json:"created_at"`
	SentAt     *time.Time `json:"sent_at,omitempty"`
	DoneAt     *time.Time `json:"done_at,omitempty"`
}

// TaskInput 是创建任务的入参。
type TaskInput struct {
	SessionID int64  `json:"session_id"`
	Kind      string `json:"kind"`
	Input     string `json:"input"`
	Actor     string `json:"actor"`
	CreatedBy string `json:"created_by"`
}

// Transfer 是一次文件传输。up 的平台侧 blob 与 down 的产物都在 <BaseDir>/c2/ 下,
// 表内只存路径(文件名一律带数字 id,路径只由服务端构造)。
type Transfer struct {
	ID         int64      `json:"id"`
	SessionID  int64      `json:"session_id"`
	TaskID     int64      `json:"task_id"`
	Direction  string     `json:"direction"` // up|down
	RemotePath string     `json:"remote_path"`
	Size       int64      `json:"size"`
	DoneSize   int64      `json:"done_size"`
	Status     string     `json:"status"` // queued|running|done|error
	Error      string     `json:"error"`
	CreatedAt  time.Time  `json:"created_at"`
	DoneAt     *time.Time `json:"done_at,omitempty"`
}

// TransferInput 是创建传输的入参。
type TransferInput struct {
	SessionID  int64  `json:"session_id"`
	TaskID     int64  `json:"task_id"`
	Direction  string `json:"direction"`
	RemotePath string `json:"remote_path"`
	Size       int64  `json:"size"`
	LocalPath  string `json:"local_path"`
}

// Loot 是一条战利品。credential/hash 类 content 存 secretbox 密文,解密只在 server 层。
type Loot struct {
	ID         int64     `json:"id"`
	SessionID  int64     `json:"session_id"`
	TaskID     int64     `json:"task_id"`
	Kind       string    `json:"kind"` // credential|hash|file|screenshot|other
	Name       string    `json:"name"`
	Content    string    `json:"content"`
	BlobPath   string    `json:"blob_path"`
	SourcePath string    `json:"source_path"`
	FindingID  int64     `json:"finding_id"`
	CreatedAt  time.Time `json:"created_at"`
}

// LootInput 是创建战利品的入参。
type LootInput struct {
	SessionID  int64  `json:"session_id"`
	TaskID     int64  `json:"task_id"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	BlobPath   string `json:"blob_path"`
	SourcePath string `json:"source_path"`
}

// LootFilter 是战利品列表筛选(零值=不筛)。
type LootFilter struct {
	Kind      string `json:"kind"`
	SessionID int64  `json:"session_id"`
	Q         string `json:"q"`
	Limit     int    `json:"limit"`
	Offset    int    `json:"offset"`
}

// Payload 是一份载荷投递物。token 用于监听器免鉴权投递端点 /<url_path>/p/<token>。
type Payload struct {
	ID         int64     `json:"id"`
	ListenerID int64     `json:"listener_id"`
	Name       string    `json:"name"`
	Format     string    `json:"format"` // ps1|bash|py|gosrc
	Target     string    `json:"target"` // windows|linux|macos|cross
	Token      string    `json:"token"`
	SHA256     string    `json:"sha256"`
	Size       int64     `json:"size"`
	Oneliner   string    `json:"oneliner"`
	CreatedBy  string    `json:"created_by"`
	CreatedAt  time.Time `json:"created_at"`
}

// PayloadInput 是登记载荷的入参(文件落盘由 server 按 <id>_<filename> 约定写入
// <BaseDir>/c2/payloads/)。
type PayloadInput struct {
	ListenerID int64  `json:"listener_id"`
	Name       string `json:"name"`
	Format     string `json:"format"`
	Target     string `json:"target"`
	Token      string `json:"token"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	Oneliner   string `json:"oneliner"`
	CreatedBy  string `json:"created_by"`
}

// Store 是 Manager 需要的持久化能力(由 server 侧实现,桥到 db 包)。仿 vpscenter.Store。
type Store interface {
	// 监听器
	CreateListener(in ListenerInput) (int64, error)
	ListListeners() ([]Listener, error) // 含 SessionCount
	GetListener(id int64) (*Listener, error)
	UpdateListener(id int64, in ListenerInput) error
	DeleteListener(id int64) error
	SetListenerEnabled(id int64, enabled bool) error
	GetListenerKey(id int64) ([]byte, error) // 返回解封后的 32B key(server 适配器里做 secretbox open)
	// 会话
	UpsertSession(in SessionInput) (int64, error) // 按 listener_id+beacon_uid 幂等:存在则更新主机信息并置 online,不存在则建
	GetSessionBySID(sid string) (*Session, error)
	GetSession(id int64) (*Session, error)
	ListSessions(f SessionFilter) ([]Session, int, error)
	SetSessionSleep(sid string, sleepSec, jitterPct int) error
	SetSessionStatusBySID(sid string, status string) error
	BindSessionTask(id, taskID int64) error
	SweepOfflineSessions(graceMultiplier int) (int64, error) // last_seen 超 sleep*(1+grace) 的 online→offline
	// 任务
	CreateTask(in TaskInput) (int64, error)
	TakeQueuedTasks(sessionID int64, limit int) ([]Task, error) // queued→sent 原子批量,返回被取走的
	FinishTask(id int64, status, output, outPath string, outSize int64, errText string) error
	GetTask(id int64) (*Task, error)
	ListTasks(sessionID, afterID int64, limit int) ([]Task, error)
	CountQueuedTasks() (int, error)
	// 文件传输
	CreateTransfer(in TransferInput) (int64, error)
	UpdateTransfer(id int64, status string, doneSize, totalSize int64, localPath, errText string) error // totalSize<0 = 保持原值
	ListTransfers(sessionID int64, limit int) ([]Transfer, error)
	GetTransfer(id int64) (*Transfer, error)
	// 战利品
	CreateLoot(in LootInput) (int64, error)
	ListLoot(f LootFilter) ([]Loot, int, error)
	GetLoot(id int64) (*Loot, error) // content 为密文,解密在 server 层
	DeleteLoot(id int64) error
	// 载荷
	CreatePayload(in PayloadInput) (int64, error)
	ListPayloads(listenerID int64) ([]Payload, error)
	GetPayload(id int64) (*Payload, error)
	GetPayloadByToken(token string) (*Payload, error)
	DeletePayload(id int64) error
}

// Manager 是 C2 中心运行时:持有运行中的监听器,提供启停与 30s 巡检。
type Manager struct {
	store Store
	log   *slog.Logger

	// BaseDir 是 blob 根目录(<BaseDir>/c2/{outputs,uploads,downloads,payloads})。
	// 启动时由 server 层注入(dataDir);为空则不落盘(大输出只留表内预览,
	// beacon 文件通道不可用)。
	BaseDir string
	// SweepInterval 是 offline 巡检间隔,默认 30s。
	SweepInterval time.Duration
	// SweepGraceMultiplier 是 sweep 的宽限倍数:last_seen 超 sleep*(1+grace) 判 offline,默认 2。
	SweepGraceMultiplier int
	// PollWait 是 beacon 长轮询的最长挂起时长,默认 25s。
	PollWait time.Duration

	mu      sync.Mutex
	ctx     context.Context
	running map[int64]*channelServer
}

// NewManager 构造运行时。log 可为 nil(回落 slog.Default())。
func NewManager(st Store, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		store:                st,
		log:                  log,
		SweepInterval:        defaultSweepInterval,
		SweepGraceMultiplier: defaultSweepGrace,
		PollWait:             defaultPollWait,
		running:              map[int64]*channelServer{},
	}
}

// Start 拉起全部 enabled 监听器并进入 30s 巡检循环(offline sweep)。
// 阻塞直到 ctx 取消;取消时停掉所有监听器。单个监听器启动失败只记日志不中断
// (端口被占不该拖垮其它监听器,用户可在页面修复后手动 start)。
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()

	listeners, err := m.store.ListListeners()
	if err != nil {
		m.logf("启动时读取监听器列表失败: %v", err)
	} else {
		for i := range listeners {
			if !listeners[i].Enabled {
				continue
			}
			if err := m.StartListener(&listeners[i]); err != nil {
				m.logf("监听器 «%s»(#%d) 启动失败: %v", listeners[i].Name, listeners[i].ID, err)
			}
		}
	}

	interval := m.SweepInterval
	if interval <= 0 {
		interval = defaultSweepInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.stopAll()
			return
		case <-t.C:
			m.sweepOnce()
		}
	}
}

// StartListener 启动单个监听器(端口被占/密钥非法返回明确错误)。对已在运行的
// 同 id 监听器是幂等 no-op。监听器配置(端口/url_path/密钥)变更后需先 stop 再 start。
func (m *Manager) StartListener(l *Listener) error {
	if l == nil || l.ID <= 0 {
		return errors.New("监听器无效")
	}
	m.mu.Lock()
	if m.ctx == nil {
		m.ctx = context.Background()
	}
	ctx := m.ctx
	if _, ok := m.running[l.ID]; ok {
		m.mu.Unlock()
		return nil // 已在运行:幂等
	}
	m.mu.Unlock()

	key, err := m.store.GetListenerKey(l.ID)
	if err != nil {
		return fmt.Errorf("读取监听器密钥失败: %w", err)
	}
	if len(key) != keySize {
		return fmt.Errorf("监听器密钥长度非法(%d 字节,需 %d)", len(key), keySize)
	}

	cs, err := newChannelServer(m, *l, key, ctx)
	if err != nil {
		return err
	}
	if err := cs.start(); err != nil {
		return err // 含「监听 <addr> 失败: ...」的端口占用原文
	}
	m.mu.Lock()
	m.running[l.ID] = cs
	m.mu.Unlock()
	m.logf("C2 监听器 «%s»(#%d) 已启动:%s(信道 /%s/e)", l.Name, l.ID, cs.addr(), cs.l.URLPath)
	return nil
}

// StopListener 停止单个监听器。未在运行时返回错误。
func (m *Manager) StopListener(id int64) error {
	m.mu.Lock()
	cs := m.running[id]
	delete(m.running, id)
	m.mu.Unlock()
	if cs == nil {
		return fmt.Errorf("监听器 #%d 未在运行", id)
	}
	cs.stop()
	m.logf("C2 监听器 #%d 已停止", id)
	return nil
}

// ListenerRunning 返回监听器是否在运行。
func (m *Manager) ListenerRunning(id int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.running[id]
	return ok
}

// ListenerAddr 返回运行中监听器的实际监听地址(端口为 0 时是内核分配的真实端口);
// 未运行返回空串。【契约 §4 之外的追加方法】:server API 的 start/stop 返回 {addr}
// 需要它,冻结稿未列,已在提交说明中登记。
func (m *Manager) ListenerAddr(id int64) string {
	m.mu.Lock()
	cs := m.running[id]
	m.mu.Unlock()
	if cs == nil {
		return ""
	}
	return cs.addr()
}

// NotifySession 唤醒某会话挂起中的长轮询,让刚入库的 queued 任务立刻被取走。
// 【契约 §4 之外的追加方法】:server 在 CreateTask 后调用可省掉最多 600ms 的
// 轮询延迟;不调用也正确(监听器每 600ms 自查一次)。已在提交说明中登记。
func (m *Manager) NotifySession(sessionID int64) {
	sess, err := m.store.GetSession(sessionID)
	if err != nil || sess == nil {
		return
	}
	m.mu.Lock()
	cs := m.running[sess.ListenerID]
	m.mu.Unlock()
	if cs != nil {
		cs.wakeSession(sessionID)
	}
}

// sweepOnce 跑一轮 offline 巡检:心跳超时的 online 会话 → offline。
func (m *Manager) sweepOnce() {
	grace := m.SweepGraceMultiplier
	if grace < 0 {
		grace = defaultSweepGrace
	}
	n, err := m.store.SweepOfflineSessions(grace)
	if err != nil {
		m.logf("offline 巡检失败: %v", err)
		return
	}
	if n > 0 {
		m.logf("offline 巡检:%d 个会话心跳超时,已置 offline", n)
	}
}

// stopAll 停掉全部运行中的监听器(ctx 取消时调用)。
func (m *Manager) stopAll() {
	m.mu.Lock()
	running := m.running
	m.running = map[int64]*channelServer{}
	m.mu.Unlock()
	for id, cs := range running {
		cs.stop()
		m.logf("C2 监听器 #%d 已随 Manager 停止", id)
	}
}

// logf 输出带模块前缀的日志。
func (m *Manager) logf(format string, args ...any) {
	m.log.Info("[c2-center] " + fmt.Sprintf(format, args...))
}
