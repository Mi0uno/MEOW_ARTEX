package c2center

// 正向连接(bind)测试(三期 §18,Agent T):平台侧 NewOutboundConn/ConnectBind
// 对「本地起的假 bind beacon」完成 reg 握手建会话;断言 transport=tcp、
// meta.direction=bind;覆盖 http/ws/oss 协议的明确报错。不连库。

import (
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBindBeacon 是目标机上监听端口的 bind beacon:接受平台连入,处理
// reg(回 ack)与 poll(回空 tasks),并记录收到的信封类型。
type fakeBindBeacon struct {
	t        *testing.T
	ln       net.Listener
	key      []byte
	mu       sync.Mutex
	received []string
	errs     []string
	sid      string
}

func startFakeBindBeacon(t *testing.T, key []byte) *fakeBindBeacon {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &fakeBindBeacon{t: t, ln: ln, key: key}
	go b.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return b
}

func (b *fakeBindBeacon) addr() string {
	_, port, _ := net.SplitHostPort(b.ln.Addr().String())
	return port
}

func (b *fakeBindBeacon) serve() {
	for {
		c, err := b.ln.Accept()
		if err != nil {
			return
		}
		go b.handle(newFrameConn(newTimeoutConn(c, 30*time.Second), nil))
	}
}

// handle 是 bind beacon 的连接循环:连入后先发 reg(它是 beacon),随后循环
// poll 收任务;记录收到的响应类型序列(ack, tasks, ...)。
func (b *fakeBindBeacon) handle(c Conn) {
	defer c.Close() //nolint:errcheck
	// bind beacon 主动发 reg;sid 以平台 ack 下发的为准。
	reg := RequestEnvelope{Type: TypeReg, TS: time.Now().Unix(), NC: newTestNC(),
		Data: mustJSON(RegPayload{UID: BeaconUID(1, "bind-host", "bob", "linux"),
			Hostname: "bind-host", Username: "bob", OS: "linux", Arch: "amd64", PID: 9, Process: "bind-beacon"})}
	resp, err := b.exchange(c, reg)
	if err != nil {
		b.mu.Lock()
		b.errs = append(b.errs, "reg: "+err.Error())
		b.mu.Unlock()
		return
	}
	dm, _ := resp.Data.(map[string]any)
	sid, _ := dm["sid"].(string)
	b.mu.Lock()
	b.sid = sid
	b.mu.Unlock()
	for i := 0; i < 3; i++ {
		poll := RequestEnvelope{Type: TypePoll, SID: sid, TS: time.Now().Unix(), NC: newTestNC()}
		if _, err := b.exchange(c, poll); err != nil {
			b.mu.Lock()
			b.errs = append(b.errs, "poll: "+err.Error())
			b.mu.Unlock()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// exchange 发一帧并读响应(记录响应类型,返回完整响应供调用方取 sid)。
func (b *fakeBindBeacon) exchange(c Conn, env RequestEnvelope) (ResponseEnvelope, error) {
	sealed, err := Seal(b.key, mustJSON(env))
	if err != nil {
		return ResponseEnvelope{}, err
	}
	if err := c.WriteFrame(sealed); err != nil {
		return ResponseEnvelope{}, err
	}
	raw, err := c.ReadFrame()
	if err != nil {
		return ResponseEnvelope{}, err
	}
	plain, err := Open(b.key, raw)
	if err != nil {
		return ResponseEnvelope{}, err
	}
	var resp ResponseEnvelope
	if err := json.Unmarshal(plain, &resp); err != nil {
		return ResponseEnvelope{}, err
	}
	b.mu.Lock()
	b.received = append(b.received, resp.Type)
	b.mu.Unlock()
	return resp, nil
}

func (b *fakeBindBeacon) errLog() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.errs...)
}

func (b *fakeBindBeacon) types() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.received...)
}

// mustSealEnvelope 测试用:构造并加密响应信封。
func mustSealEnvelope(t *testing.T, key []byte, typ string, d any) []byte {
	t.Helper()
	plain, err := json.Marshal(ResponseEnvelope{Type: typ, Meta: RespMeta{SleepSec: 5, JitterPct: 5}, Data: d})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := Seal(key, plain)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func TestOutboundConnect(t *testing.T) {
	mgr, st, key, l := newTestListener(t)
	l = fakeAddListener(st, ProtoTCP, "abc", 0, key, "")
	mgr.PollWait = 300 * time.Millisecond
	if err := mgr.StartListener(l); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.StopListener(l.ID) })

	beacon := startFakeBindBeacon(t, key)
	port := beacon.addr()

	sessID, err := mgr.ConnectBind(l, "127.0.0.1", port)
	if err != nil {
		t.Fatalf("ConnectBind: %v", err)
	}
	if sessID <= 0 {
		t.Fatalf("会话 id 非法: %d", sessID)
	}
	sess, err := st.GetSession(sessID)
	if err != nil || sess == nil {
		t.Fatalf("会话未入库: %v", err)
	}
	if sess.Transport != ProtoTCP {
		t.Fatalf("transport 应为 tcp: %q", sess.Transport)
	}
	if sess.Meta["direction"] != "bind" {
		t.Fatalf("meta.direction 应为 bind: %+v", sess.Meta)
	}

	// 平台侧后续帧循环在跑:下发任务应被 bind beacon 的 poll 取走。
	if _, err := st.CreateTask(TaskInput{SessionID: sess.ID, Kind: "shell", Input: `{"command":"id"}`, Actor: "user"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if types := beacon.types(); len(types) >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	// bind beacon 应先收到 ack(reg 响应),随后收到 tasks(poll 响应)。
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		types := beacon.types()
		if len(types) >= 2 && types[0] == TypeAck && types[1] == TypeTasks {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	types := beacon.types()
	if len(types) < 2 || types[0] != TypeAck || types[1] != TypeTasks {
		t.Fatalf("bind beacon 收到的响应序列不符: %v errs=%v", types, beacon.errLog())
	}
}

func TestOutboundConnectProtoMatrix(t *testing.T) {
	for _, proto := range []string{ProtoHTTP, ProtoWS, ProtoOSS, ""} {
		mgr, st, key, _ := newTestListener(t)
		l := fakeAddListener(st, proto, "abc", 0, key, "")
		if _, err := mgr.NewOutboundConn(l, "127.0.0.1", "1"); err == nil {
			t.Fatalf("协议 %q 应不支持正向连接", proto)
		}
	}
	// 未运行监听器的 ConnectBind 明确报错。
	mgr, st, key, _ := newTestListener(t)
	l := fakeAddListener(st, ProtoTCP, "abc", 0, key, "")
	if _, err := mgr.ConnectBind(l, "127.0.0.1", "1"); err == nil {
		t.Fatal("未运行监听器应明确报错")
	} else if !strings.Contains(err.Error(), "未在运行") {
		t.Fatalf("错误应说明监听器未运行: %v", err)
	}
}
