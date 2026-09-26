package server

// C2 中心:Server 侧接入(契约 C2_CONTRACTS.md §4/§6)。
//
// 本文件分三部分(照 vps_center.go 的组织方式):
//   1. c2Store  —— 把 db 层适配成 c2center.Store(包边界:c2center 不依赖 db)。
//                  薄桥:逐字段搬运;唯一有逻辑的是 GetListenerKey——在这层做
//                  secretbox 解封,32B 明文 key 只进 c2center 运行时内存。
//   2. initC2   —— 构造 Manager、建 blob 目录、播种 agent 工具、启动运行时
//                  (拉起 enabled 监听器 + 30s 离线巡检)。
//   3. 密钥加解密 —— secretbox 封装,监听器创建(seal)与信封解密(open)共用。
//
// REST 接口在 c2_api.go,agent 工具在 c2_agent.go。
//
// 对接口径(已对照 c2center/manager.go 实码):XxxInput 字段与结构体同名;
// ListenerInput 不含密钥——CreateListener 适配器内部生成 32B key 并 seal 入库;
// TransferInput 带 LocalPath;PayloadInput 不带 FilePath(落盘路径由 server 层
// 经 db.UpdateC2PayloadFilePath 回填)。

import (
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	c2center "github.com/Autumn-27/artex/c2center"
	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/vpscenter"
)

// c2Manager 是进程内唯一的 C2 中心运行时。
//
// 为什么是包级变量而不是 Server 字段:server.go 的 Server/Manager 结构体不在本模块
// 文件所有权内(契约 §6 只允许在 New()/Handler() 各加一行),而 C2 中心是单例运行时,
// 与进程一一对应——用带锁的包级单例即可,语义等价且不越权(照 vpsMgr 惯例)。
var (
	c2MgrMu sync.RWMutex
	c2Mgr   *c2center.Manager
)

// setC2Manager 绑定进程级 Manager(initC2 调用)。
func setC2Manager(m *c2center.Manager) {
	c2MgrMu.Lock()
	defer c2MgrMu.Unlock()
	c2Mgr = m
}

// C2Manager 返回进程级 C2 中心运行时(未初始化返回 nil)。
func C2Manager() *c2center.Manager {
	c2MgrMu.RLock()
	defer c2MgrMu.RUnlock()
	return c2Mgr
}

// ---- Store 适配器:c2center.Store → db ----

// c2Store 实现 c2center.Store(监听器密钥在本层解封,明文只进 Manager 内存)。
type c2Store struct {
	s  *Server
	pg *pgdb.DB
}

func newC2Store(s *Server, pg *pgdb.DB) *c2Store { return &c2Store{s: s, pg: pg} }

// c2centerListenerFromDB db 监听器行 → c2center 公共视图(永不携带 enc_key)。
func c2centerListenerFromDB(l *pgdb.C2Listener) *c2center.Listener {
	return &c2center.Listener{
		ID: l.ID, Name: l.Name, Proto: l.Proto, BindHost: l.BindHost, Port: l.Port,
		URLPath: l.URLPath, Callback: l.Callback, Enabled: l.Enabled,
		SessionCount: int(l.SessionCount), Notes: l.Notes,
		HostKind: l.HostKind, VPSMachineID: l.VPSMachineID, RemotePort: l.RemotePort,
		CreatedAt: l.CreatedAt, UpdatedAt: l.UpdatedAt,
	}
}

func c2centerSessionFromDB(m *pgdb.C2Session) *c2center.Session {
	return &c2center.Session{
		ID: m.ID, ListenerID: m.ListenerID, BeaconUID: m.BeaconUID, SID: m.SID,
		Hostname: m.Hostname, Username: m.Username, Elevated: m.Elevated,
		OS: m.OS, Arch: m.Arch, LocalIPs: m.LocalIPs, PID: m.PID, Process: m.Process,
		SleepSec: m.SleepSec, JitterPct: m.JitterPct, TaskID: m.TaskID,
		Status: m.Status, Labels: m.Labels, Meta: m.Meta,
		Transport: m.Transport, // 方向在 Meta["direction"](§18 正向连接标注)
		FirstSeen: m.FirstSeen, LastSeen: m.LastSeen,
	}
}

func c2centerTaskFromDB(t *pgdb.C2Task) *c2center.Task {
	return &c2center.Task{
		ID: t.ID, SessionID: t.SessionID, Kind: t.Kind, Input: t.Input,
		Output: t.Output, OutputPath: t.OutputPath, OutputSize: t.OutputSize,
		Status: t.Status, Actor: t.Actor, CreatedBy: t.CreatedBy, Error: t.Error,
		CreatedAt: t.CreatedAt, SentAt: t.SentAt, DoneAt: t.DoneAt,
	}
}

func c2centerTransferFromDB(t *pgdb.C2Transfer) *c2center.Transfer {
	return &c2center.Transfer{
		ID: t.ID, SessionID: t.SessionID, TaskID: t.TaskID,
		Direction: t.Direction, RemotePath: t.RemotePath,
		Size: t.Size, DoneSize: t.DoneSize, Status: t.Status, Error: t.Error,
		CreatedAt: t.CreatedAt, DoneAt: t.DoneAt,
	}
}

func c2centerLootFromDB(l *pgdb.C2Loot) *c2center.Loot {
	return &c2center.Loot{
		ID: l.ID, SessionID: l.SessionID, TaskID: l.TaskID, Kind: l.Kind,
		Name: l.Name, Content: l.Content, BlobPath: l.BlobPath,
		SourcePath: l.SourcePath, FindingID: l.FindingID, CreatedAt: l.CreatedAt,
	}
}

func c2centerPayloadFromDB(p *pgdb.C2Payload) *c2center.Payload {
	return &c2center.Payload{
		ID: p.ID, ListenerID: p.ListenerID, Name: p.Name, Format: p.Format,
		Target: p.Target, Token: p.Token, SHA256: p.SHA256, Size: p.Size,
		Oneliner: p.Oneliner, CreatedBy: p.CreatedBy, CreatedAt: p.CreatedAt,
	}
}

// ---- Store 实现(薄桥 db) ----

// CreateListener 实现 c2center.Store(Manager 侧入口)。密钥在本层生成:
// 32B crypto/rand → secretbox 密封入库,明文即弃(c2center.ListenerInput 不带
// 密钥字段,这是契约约定的唯一密钥产生点;页面 API 走 db 直写,同样各自生成)。
func (p *c2Store) CreateListener(in c2center.ListenerInput) (int64, error) {
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return 0, fmt.Errorf("生成监听器密钥失败: %w", err)
	}
	sealed, err := p.s.sealC2Secret(key)
	if err != nil {
		return 0, err
	}
	return p.pg.CreateC2Listener(pgdb.C2ListenerInput{
		Name: in.Name, BindHost: in.BindHost, Port: in.Port,
		URLPath: in.URLPath, Callback: in.Callback, EncKeySealed: sealed, Notes: in.Notes,
		HostKind: in.HostKind, VPSMachineID: in.VPSMachineID, RemotePort: in.RemotePort,
	})
}

func (p *c2Store) ListListeners() ([]c2center.Listener, error) {
	rows, err := p.pg.ListC2Listeners()
	if err != nil {
		return nil, err
	}
	out := make([]c2center.Listener, 0, len(rows))
	for _, l := range rows {
		out = append(out, *c2centerListenerFromDB(l))
	}
	return out, nil
}

func (p *c2Store) GetListener(id int64) (*c2center.Listener, error) {
	l, err := p.pg.GetC2Listener(id)
	if err != nil || l == nil {
		return nil, err
	}
	return c2centerListenerFromDB(l), nil
}

func (p *c2Store) UpdateListener(id int64, in c2center.ListenerInput) error {
	return p.pg.UpdateC2Listener(id, pgdb.C2ListenerInput{
		Name: in.Name, BindHost: in.BindHost, Port: in.Port,
		URLPath: in.URLPath, Callback: in.Callback, Notes: in.Notes,
		HostKind: in.HostKind, VPSMachineID: in.VPSMachineID, RemotePort: in.RemotePort,
	})
}

func (p *c2Store) DeleteListener(id int64) error {
	_, err := p.pg.DeleteC2Listener(id)
	return err
}

func (p *c2Store) SetListenerEnabled(id int64, enabled bool) error {
	return p.pg.SetC2ListenerEnabled(id, enabled)
}

// GetListenerKey 解封监听器密钥(契约 §4:解封发生在这层,32B 明文只进 Manager)。
func (p *c2Store) GetListenerKey(id int64) ([]byte, error) {
	sealed, err := p.pg.GetC2ListenerKey(id)
	if err != nil || sealed == nil {
		return nil, err
	}
	plain, err := p.s.openC2Secret(sealed)
	if err != nil {
		return nil, fmt.Errorf("解封监听器 #%d 密钥失败: %w", id, err)
	}
	if len(plain) != 32 {
		return nil, fmt.Errorf("监听器 #%d 密钥长度异常(%dB,应为 32B)", id, len(plain))
	}
	return plain, nil
}

// GetListenerTransportConfig 解封监听器传输配置(三期 §18,满足 c2center 的
// transportConfigStore 可选接口):oss 的 endpoint/bucket/AK/SK、kcp 的 psk。
// 明文 JSON 只进 c2center 运行时内存;未配置返回 (nil, nil)。
func (p *c2Store) GetListenerTransportConfig(id int64) ([]byte, error) {
	sealed, err := p.pg.GetC2ListenerTransportConfig(id)
	if err != nil || sealed == nil {
		return nil, err
	}
	plain, err := p.s.openC2Secret(sealed)
	if err != nil {
		return nil, fmt.Errorf("解封监听器 #%d 传输配置失败: %w", id, err)
	}
	return plain, nil
}

func (p *c2Store) UpsertSession(in c2center.SessionInput) (int64, error) {
	return p.pg.UpsertC2Session(pgdb.C2SessionInput{
		ListenerID: in.ListenerID, BeaconUID: in.BeaconUID, SID: in.SID,
		Hostname: in.Hostname, Username: in.Username, Elevated: in.Elevated,
		OS: in.OS, Arch: in.Arch, LocalIPs: in.LocalIPs, PID: in.PID, Process: in.Process,
		Transport: in.Transport, Direction: in.Direction,
	})
}

func (p *c2Store) GetSessionBySID(sid string) (*c2center.Session, error) {
	m, err := p.pg.GetC2SessionBySID(sid)
	if err != nil || m == nil {
		return nil, err
	}
	return c2centerSessionFromDB(m), nil
}

func (p *c2Store) GetSession(id int64) (*c2center.Session, error) {
	m, err := p.pg.GetC2Session(id)
	if err != nil || m == nil {
		return nil, err
	}
	return c2centerSessionFromDB(m), nil
}

func (p *c2Store) ListSessions(f c2center.SessionFilter) ([]c2center.Session, int, error) {
	rows, total, err := p.pg.ListC2Sessions(pgdb.C2SessionFilter{
		Status:     f.Status,
		ListenerID: f.ListenerID,
		TaskID:     f.TaskID,
		CompanyID:  f.CompanyID,
		Q:          f.Q,
		Limit:      f.Limit,
		Offset:     f.Offset,
	})
	if err != nil {
		return nil, 0, err
	}
	out := make([]c2center.Session, 0, len(rows))
	for _, m := range rows {
		out = append(out, *c2centerSessionFromDB(m))
	}
	return out, total, nil
}

func (p *c2Store) SetSessionSleep(sid string, sleepSec, jitterPct int) error {
	return p.pg.SetC2SessionSleep(sid, sleepSec, jitterPct)
}

func (p *c2Store) SetSessionStatusBySID(sid string, status string) error {
	return p.pg.SetC2SessionStatusBySID(sid, status)
}

func (p *c2Store) BindSessionTask(id, taskID int64) error {
	return p.pg.BindC2SessionTask(id, taskID)
}

func (p *c2Store) SweepOfflineSessions(graceMultiplier int) (int64, error) {
	return p.pg.SweepOfflineSessions(graceMultiplier)
}

func (p *c2Store) CreateTask(in c2center.TaskInput) (int64, error) {
	return p.pg.CreateC2Task(pgdb.C2TaskInput{
		SessionID: in.SessionID, Kind: in.Kind, Input: in.Input,
		Actor: in.Actor, CreatedBy: in.CreatedBy,
	})
}

func (p *c2Store) TakeQueuedTasks(sessionID int64, limit int) ([]c2center.Task, error) {
	rows, err := p.pg.TakeQueuedTasks(sessionID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]c2center.Task, 0, len(rows))
	for _, t := range rows {
		out = append(out, *c2centerTaskFromDB(t))
	}
	return out, nil
}

func (p *c2Store) FinishTask(id int64, status, output, outPath string, outSize int64, errText string) error {
	return p.pg.FinishC2Task(id, status, output, outPath, outSize, errText)
}

func (p *c2Store) GetTask(id int64) (*c2center.Task, error) {
	t, err := p.pg.GetC2Task(id)
	if err != nil || t == nil {
		return nil, err
	}
	return c2centerTaskFromDB(t), nil
}

func (p *c2Store) ListTasks(sessionID, afterID int64, limit int) ([]c2center.Task, error) {
	rows, err := p.pg.ListC2Tasks(sessionID, afterID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]c2center.Task, 0, len(rows))
	for _, t := range rows {
		out = append(out, *c2centerTaskFromDB(t))
	}
	return out, nil
}

func (p *c2Store) CountQueuedTasks() (int, error) { return p.pg.CountQueuedC2Tasks() }

func (p *c2Store) CreateTransfer(in c2center.TransferInput) (int64, error) {
	return p.pg.CreateC2Transfer(pgdb.C2TransferInput{
		SessionID: in.SessionID, TaskID: in.TaskID, Direction: in.Direction,
		RemotePath: in.RemotePath, Size: in.Size, LocalPath: in.LocalPath,
	})
}

func (p *c2Store) UpdateTransfer(id int64, status string, doneSize, totalSize int64, localPath, errText string) error {
	return p.pg.UpdateC2Transfer(id, status, doneSize, totalSize, localPath, errText)
}

func (p *c2Store) ListTransfers(sessionID int64, limit int) ([]c2center.Transfer, error) {
	rows, err := p.pg.ListC2Transfers(sessionID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]c2center.Transfer, 0, len(rows))
	for _, t := range rows {
		out = append(out, *c2centerTransferFromDB(t))
	}
	return out, nil
}

func (p *c2Store) GetTransfer(id int64) (*c2center.Transfer, error) {
	t, err := p.pg.GetC2Transfer(id)
	if err != nil || t == nil {
		return nil, err
	}
	return c2centerTransferFromDB(t), nil
}

func (p *c2Store) CreateLoot(in c2center.LootInput) (int64, error) {
	return p.pg.CreateC2Loot(pgdb.C2LootInput{
		SessionID: in.SessionID, TaskID: in.TaskID, Kind: in.Kind,
		Name: in.Name, Content: in.Content, BlobPath: in.BlobPath, SourcePath: in.SourcePath,
	})
}

func (p *c2Store) ListLoot(f c2center.LootFilter) ([]c2center.Loot, int, error) {
	rows, total, err := p.pg.ListC2Loot(pgdb.C2LootFilter{
		Kind: f.Kind, SessionID: f.SessionID, Q: f.Q, Limit: f.Limit, Offset: f.Offset,
	})
	if err != nil {
		return nil, 0, err
	}
	out := make([]c2center.Loot, 0, len(rows))
	for _, l := range rows {
		out = append(out, *c2centerLootFromDB(l))
	}
	return out, total, nil
}

// GetLoot 返回的 Content 是原始密文(credential/hash),解密在 server 层。
func (p *c2Store) GetLoot(id int64) (*c2center.Loot, error) {
	l, err := p.pg.GetC2Loot(id)
	if err != nil || l == nil {
		return nil, err
	}
	return c2centerLootFromDB(l), nil
}

func (p *c2Store) DeleteLoot(id int64) error {
	_, err := p.pg.DeleteC2Loot(id)
	return err
}

func (p *c2Store) CreatePayload(in c2center.PayloadInput) (int64, error) {
	// c2center.PayloadInput 无 FilePath:文件由 server 层落盘后经
	// db.UpdateC2PayloadFilePath 回填,这里按空登记。
	return p.pg.CreateC2Payload(pgdb.C2PayloadInput{
		ListenerID: in.ListenerID, Name: in.Name, Format: in.Format, Target: in.Target,
		Token: in.Token, SHA256: in.SHA256, Size: in.Size,
		Oneliner: in.Oneliner, CreatedBy: in.CreatedBy,
	})
}

func (p *c2Store) ListPayloads(listenerID int64) ([]c2center.Payload, error) {
	rows, err := p.pg.ListC2Payloads(listenerID)
	if err != nil {
		return nil, err
	}
	out := make([]c2center.Payload, 0, len(rows))
	for _, pl := range rows {
		out = append(out, *c2centerPayloadFromDB(pl))
	}
	return out, nil
}

func (p *c2Store) GetPayload(id int64) (*c2center.Payload, error) {
	pl, err := p.pg.GetC2Payload(id)
	if err != nil || pl == nil {
		return nil, err
	}
	return c2centerPayloadFromDB(pl), nil
}

func (p *c2Store) GetPayloadByToken(token string) (*c2center.Payload, error) {
	pl, err := p.pg.GetC2PayloadByToken(token)
	if err != nil || pl == nil {
		return nil, err
	}
	return c2centerPayloadFromDB(pl), nil
}

func (p *c2Store) DeletePayload(id int64) error {
	_, err := p.pg.DeleteC2Payload(id)
	return err
}

// 编译期断言:适配器必须满足 c2center.Store。
var _ c2center.Store = (*c2Store)(nil)

// ---- 密钥加解密(响应永不回显明文;这里只服务入库与信封解密) ----

// sealC2Secret 把监听器明文密钥加密成入库密文。
func (s *Server) sealC2Secret(plain []byte) ([]byte, error) {
	if s.apiSecret == nil {
		return nil, fmt.Errorf("凭据加密盒未初始化")
	}
	return s.apiSecret.seal(plain)
}

// openC2Secret 解开入库密文(仅信封加解密前使用)。
func (s *Server) openC2Secret(sealed []byte) ([]byte, error) {
	if s.apiSecret == nil {
		return nil, fmt.Errorf("凭据加密盒未初始化")
	}
	return s.apiSecret.open(sealed)
}

// ---- 初始化编排 ----

// c2BlobDir 返回 C2 blob 子目录的绝对路径(kind ∈ uploads/outputs/downloads/payloads)。
// 目录分工见契约 §3:uploads 由本 server API 写、其余由 c2center 写,读方相反;
// 文件名一律带数字 id,路径只由服务端构造。
func c2BlobDir(baseDir, kind string) string {
	return filepath.Join(baseDir, "c2", kind)
}

// ensureC2BlobDirs 建齐 blob 目录(幂等,尽力而为;读写路径上各 handler 还会兜 MkdirAll)。
func ensureC2BlobDirs(baseDir string) {
	for _, kind := range []string{"uploads", "outputs", "downloads", "payloads"} {
		if err := os.MkdirAll(c2BlobDir(baseDir, kind), 0o755); err != nil {
			log.Printf("[c2-center] 建 blob 目录 %s 失败: %v", kind, err)
		}
	}
}

// initC2 构造 C2 中心运行时并启动(New() 调用,照 initVPS 惯例;契约 §6):
//  1. 建 blob 目录(uploads/outputs/downloads/payloads);
//  2. 播种 8 个 C2 agent 工具(见 c2_agent.go;SeedTool 首插入 only);
//  3. 构造 Manager 并 goroutine 启动:拉起 enabled 监听器 + 30s 离线巡检
//     (vps 载体的入口转发经 IngressOpener 回调由本文件建立,见 c2OpenIngress);
//  4. 二期接线尾部:免杀工作台(Agent B,c2_evasion.go)+ gosrc VPS 构建
//     runner(c2_build.go)。
//
// 任务终态不挂钩(契约 §6:C2 会话不随平台任务终态销毁)。
func (s *Server) initC2() {
	if s.m == nil || s.m.PG() == nil {
		return
	}
	pg := s.m.PG()
	store := newC2Store(s, pg)
	mgr := c2center.NewManager(store, slog.Default())
	mgr.BaseDir = s.m.dir
	mgr.IngressOpener = s.c2OpenIngress // vps 载体(§12):boot 恢复路径的转发编排点
	setC2Manager(mgr)

	ensureC2BlobDirs(s.m.dir)
	s.seedC2AgentTools()

	go mgr.Start(s.ctx)
	log.Printf("[c2-center] C2 中心已启动(监听器/会话/任务/战利品/载荷)")

	// ---- 二期接线(契约 §11)----
	s.initC2Evasion() // 免杀工作台(Agent B,c2_evasion.go;签名冻结,只此一行调用)
	s.initC2Build()   // gosrc 经 VPS 构建 runner(§14,c2_build.go)
	// 三期 §19 staged 信道接线:c2_payload_api.go 的取 stage 回调(契约 §23 集成项)。
	mgr.StageDataOpener = s.c2StageDataFor
}

// ---- vps 载体:入口转发编排(契约 §12) ----

// c2IngressErr 记录各监听器入口转发的最近一次异常(runtime 状态标注:
// 监听器 DTO 的 ingress_error 字段;成功重建转发时清除)。包级变量与 c2Mgr 同级。
var (
	c2IngressErrMu sync.Mutex
	c2IngressErr   = map[int64]string{}
)

func setC2IngressErr(listenerID int64, msg string) {
	c2IngressErrMu.Lock()
	defer c2IngressErrMu.Unlock()
	if msg == "" {
		delete(c2IngressErr, listenerID)
	} else {
		c2IngressErr[listenerID] = msg
	}
}

func getC2IngressErr(listenerID int64) string {
	c2IngressErrMu.Lock()
	defer c2IngressErrMu.Unlock()
	return c2IngressErr[listenerID]
}

// c2OpenIngress 是注入 c2center.Manager.IngressOpener 的回调(boot 恢复路径)。
// 调用时本地监听已起(Manager.Start 先 StartListener),vps 载体在此取实际监听
// 地址并建立 SSH 远程转发;local 载体返回 (nil, nil)。
func (s *Server) c2OpenIngress(l *c2center.Listener) (c2center.IngressForwarder, error) {
	if l == nil || l.HostKind != c2center.HostKindVPS {
		return nil, nil
	}
	return s.c2OpenIngressForward(l.ID, l.VPSMachineID, l.RemotePort)
}

// c2OpenIngressForward 建立「VPS:remotePort → 本地监听器」的 SSH 远程转发并启动
// 断开监看。SSH 失败原样返回(API start 路径要把错误报给前端;boot 路径由
// c2center 只记日志)。成功时清除该监听器的历史转发异常标注。
func (s *Server) c2OpenIngressForward(listenerID, machineID int64, remotePort int) (c2center.IngressForwarder, error) {
	vm := VPSManager()
	if vm == nil {
		return nil, fmt.Errorf("VPS 中心未初始化,无法建立入口转发")
	}
	mgr := C2Manager()
	if mgr == nil {
		return nil, fmt.Errorf("C2 中心未初始化")
	}
	localAddr := mgr.ListenerAddr(listenerID)
	if localAddr == "" {
		return nil, fmt.Errorf("监听器 #%d 的本地监听未运行,无法建立入口转发(先起本地监听)", listenerID)
	}
	fwd, err := vm.OpenRemoteForward(machineID, remotePort, localAddr)
	if err != nil {
		return nil, err
	}
	setC2IngressErr(listenerID, "")
	go s.c2WatchIngress(listenerID, fwd)
	return fwd, nil
}

// c2WatchIngress 盯一条转发的错误流:SSH 断开时记日志并标注运行时状态
// (ingress_error;§12)。本地监听保持运行,beacon 按自身退避重连,用户在页面
// 手动 start 即可重建转发。
func (s *Server) c2WatchIngress(listenerID int64, fwd *vpscenter.RemoteForward) {
	err, ok := <-fwd.ErrCh()
	if !ok || err == nil {
		return
	}
	setC2IngressErr(listenerID, err.Error())
	log.Printf("[c2-center] 监听器 #%d 的 VPS 入口转发断开: %v(本地监听仍在运行,可手动 start 重建)", listenerID, err)
}
