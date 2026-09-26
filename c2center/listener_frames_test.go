package c2center

// ws/tcp/kcp 三载体的监听器集成测试(三期 §18,Agent T):起真监听器(127.0.0.1
// 随机端口),模拟 beacon 走 reg → poll → res 全链路;外加「首帧必须 reg」、错误
// 密钥断连、ws 的 POST /e 404。全程不连库。

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// frameBeacon 是帧式载体的测试 beacon:加封一帧→写帧→读响应帧→解封。
type frameBeacon struct {
	t   *testing.T
	key []byte
	c   Conn
}

func newFrameBeacon(t *testing.T, key []byte, c Conn) *frameBeacon {
	return &frameBeacon{t: t, key: key, c: c}
}

// frameResp 是解封后的响应(d 保留原始字节)。
type frameResp struct {
	Type string          `json:"t"`
	Meta RespMeta        `json:"meta"`
	Data json.RawMessage `json:"d"`
}

// call 发一帧并要求响应;传输层错误直接 Fatal。
func (b *frameBeacon) call(typ, sid string, d any) frameResp {
	b.t.Helper()
	resp, err := b.tryCall(typ, sid, d)
	if err != nil {
		b.t.Fatalf("%s 帧交换失败: %v", typ, err)
	}
	return resp
}

// tryCall 发一帧;对端断连/无响应时返回错误(负向用例)。
func (b *frameBeacon) tryCall(typ, sid string, d any) (frameResp, error) {
	env := RequestEnvelope{Type: typ, SID: sid, TS: time.Now().Unix(), NC: newTestNC()}
	if d != nil {
		env.Data = mustJSON(d)
	}
	sealed, err := Seal(b.key, mustJSON(env))
	if err != nil {
		return frameResp{}, err
	}
	if err := b.c.WriteFrame(sealed); err != nil {
		return frameResp{}, err
	}
	raw, err := b.c.ReadFrame()
	if err != nil {
		return frameResp{}, err
	}
	plain, err := Open(b.key, raw)
	if err != nil {
		return frameResp{}, err
	}
	var out frameResp
	if err := json.Unmarshal(plain, &out); err != nil {
		return frameResp{}, err
	}
	return out, nil
}

// frameReg 完成注册,返回 sid。
func (b *frameBeacon) frameReg(listenerID int64, hn string) string {
	b.t.Helper()
	resp := b.call(TypeReg, "", RegPayload{
		UID: BeaconUID(listenerID, hn, "alice", "windows"), Hostname: hn, Username: "alice",
		Elevated: true, OS: "windows", Arch: "amd64", IP: "10.0.0.9", PID: 42, Process: "beacon.test",
	})
	if resp.Type != TypeAck {
		b.t.Fatalf("reg 应回 ack: %s", resp.Type)
	}
	var ack AckPayload
	decodeData(b.t, rawOf(resp), &ack)
	if len(ack.SID) != 32 {
		b.t.Fatalf("sid 长度不符: %d", len(ack.SID))
	}
	return ack.SID
}

// rawOf 把 frameResp 转成 decodeData 需要的形状。
func rawOf(r frameResp) rawEnvelope {
	return rawEnvelope{Type: r.Type, Meta: r.Meta, Data: r.Data}
}

// expectNoResp 断言一帧发出后对端直接断连(非 200 裁决不回明文)。
func (b *frameBeacon) expectNoResp(typ, sid string, d any) {
	b.t.Helper()
	if _, err := b.tryCall(typ, sid, d); err == nil {
		b.t.Fatalf("%s 应被拒并断连", typ)
	}
}

// ---- ws ----

func TestWSChain(t *testing.T) {
	mgr, st, key, _ := newTestListener(t)
	l := fakeAddListener(st, ProtoWS, "abc", 0, key, "")
	mgr.PollWait = 300 * time.Millisecond
	if err := mgr.StartListener(l); err != nil {
		t.Fatalf("StartListener: %v", err)
	}
	t.Cleanup(func() { _ = mgr.StopListener(l.ID) })
	addr := mgr.ListenerAddr(l.ID)

	dial := func() *wsConn {
		t.Helper()
		ws, _, err := websocket.DefaultDialer.Dial("ws://"+addr+"/abc/e", nil)
		if err != nil {
			t.Fatalf("ws dial: %v", err)
		}
		t.Cleanup(func() { _ = ws.Close() })
		return newWSConn(ws)
	}

	b := newFrameBeacon(t, key, dial())
	sid := b.frameReg(1, "ws-host")
	sess, err := st.GetSessionBySID(sid)
	if err != nil || sess == nil {
		t.Fatalf("会话未入库: %v", err)
	}
	if sess.Transport != ProtoWS {
		t.Fatalf("会话 transport 应为 ws: %q", sess.Transport)
	}

	// 同一连接上取任务并回结果。
	taskID, err := st.CreateTask(TaskInput{SessionID: sess.ID, Kind: "shell",
		Input: `{"command":"id"}`, Actor: "user"})
	if err != nil {
		t.Fatal(err)
	}
	resp := b.call(TypePoll, sid, nil)
	if resp.Type != TypeTasks {
		t.Fatalf("poll 应回 tasks: %s", resp.Type)
	}
	var tp TasksPayload
	decodeData(t, rawOf(resp), &tp)
	if len(tp.Tasks) != 1 || tp.Tasks[0].ID != taskID {
		t.Fatalf("任务未取到: %+v", tp.Tasks)
	}
	resp = b.call(TypeRes, sid, ResPayload{Results: []ResResult{{
		TaskID: taskID, Status: TaskDone, Output: []byte("ws-out"),
	}}})
	if resp.Type != TypeTasks {
		t.Fatalf("res 应合并 poll: %s", resp.Type)
	}
	got, _ := st.GetTask(taskID)
	if got.Status != TaskDone || got.Output != "ws-out" {
		t.Fatalf("结果未回填: %+v", got)
	}

	// 新 ws 连接首帧必须 reg:poll 被拒并断连。
	b2 := newFrameBeacon(t, key, dial())
	b2.expectNoResp(TypePoll, sid, nil)

	// ws 载体的 POST /e 不受理(伪装 404)。
	respHTTP, err := http.Post("http://"+addr+"/abc/e", "application/octet-stream", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	respHTTP.Body.Close()
	if respHTTP.StatusCode != http.StatusNotFound {
		t.Fatalf("ws 载体 POST /e 应 404: %d", respHTTP.StatusCode)
	}
}

// ---- tcp ----

func TestTCPChain(t *testing.T) {
	mgr, st, key, _ := newTestListener(t)
	l := fakeAddListener(st, ProtoTCP, "abc", 0, key, "")
	mgr.PollWait = 300 * time.Millisecond // 合并 res/poll 不必挂满 25s
	if err := mgr.StartListener(l); err != nil {
		t.Fatalf("StartListener: %v", err)
	}
	t.Cleanup(func() { _ = mgr.StopListener(l.ID) })
	addr := mgr.ListenerAddr(l.ID)

	dial := func() *frameBeacon {
		t.Helper()
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("tcp dial: %v", err)
		}
		return newFrameBeacon(t, key, newFrameConn(newTimeoutConn(c, 10*time.Second), nil))
	}

	b := dial()
	sid := b.frameReg(1, "tcp-host")
	sess, err := st.GetSessionBySID(sid)
	if err != nil || sess == nil {
		t.Fatalf("会话未入库: %v", err)
	}
	if sess.Transport != ProtoTCP {
		t.Fatalf("会话 transport 应为 tcp: %q", sess.Transport)
	}

	taskID, err := st.CreateTask(TaskInput{SessionID: sess.ID, Kind: "shell",
		Input: `{"command":"whoami"}`, Actor: "user"})
	if err != nil {
		t.Fatal(err)
	}
	resp := b.call(TypePoll, sid, nil)
	var tp TasksPayload
	decodeData(t, rawOf(resp), &tp)
	if len(tp.Tasks) != 1 || tp.Tasks[0].ID != taskID {
		t.Fatalf("任务未取到: %+v", tp.Tasks)
	}
	b.call(TypeRes, sid, ResPayload{Results: []ResResult{{
		TaskID: taskID, Status: TaskDone, Output: []byte("ok"),
	}}})
	got, _ := st.GetTask(taskID)
	if got.Status != TaskDone || got.Output != "ok" {
		t.Fatalf("结果未回填: %+v", got)
	}

	// 首帧非 reg → 断连不回明文。
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	bad := newFrameBeacon(t, key, newFrameConn(newTimeoutConn(c, 5*time.Second), nil))
	bad.expectNoResp(TypePoll, sid, nil)

	// 错误密钥 → 断连。
	c2, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	wrong := randKey32(t)
	wrong[0] ^= 0xFF
	bad2 := newFrameBeacon(t, wrong, newFrameConn(newTimeoutConn(c2, 5*time.Second), nil))
	bad2.expectNoResp(TypeReg, "", RegPayload{UID: "u-wrong-key"})
}

// ---- kcp ----

func TestKCPChain(t *testing.T) {
	t.Run("no-psk", func(t *testing.T) { kcpChainCase(t, "") })
	t.Run("psk", func(t *testing.T) { kcpChainCase(t, "preshared-secret") })
}

func kcpChainCase(t *testing.T, psk string) {
	mgr, st, key, _ := newTestListener(t)
	cfgJSON := ""
	if psk != "" {
		cfgJSON = `{"psk":"` + psk + `"}`
	}
	l := fakeAddListener(st, ProtoKCP, "abc", 0, key, cfgJSON)
	mgr.PollWait = 300 * time.Millisecond
	if err := mgr.StartListener(l); err != nil {
		t.Fatalf("StartListener: %v", err)
	}
	t.Cleanup(func() { _ = mgr.StopListener(l.ID) })
	addr := mgr.ListenerAddr(l.ID)
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("addr: %v", err)
	}

	conn, err := dialKCP(host, port, psk)
	if err != nil {
		t.Fatalf("dialKCP: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	b := newFrameBeacon(t, key, conn)
	sid := b.frameReg(1, "kcp-host")
	sess, err := st.GetSessionBySID(sid)
	if err != nil || sess == nil {
		t.Fatalf("会话未入库: %v", err)
	}
	if sess.Transport != ProtoKCP {
		t.Fatalf("会话 transport 应为 kcp: %q", sess.Transport)
	}
	resp := b.call(TypePoll, sid, nil)
	if resp.Type != TypeTasks {
		t.Fatalf("poll 失败: %s", resp.Type)
	}
	var tp TasksPayload
	decodeData(t, rawOf(resp), &tp)
	if tp.Tasks == nil {
		t.Fatal("空队列应为空数组")
	}
}
