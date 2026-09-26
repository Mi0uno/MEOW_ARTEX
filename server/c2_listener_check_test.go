package server

// §24B(三期补丁 A v3.1)监听器测活的服务端测试:
//   - local http/ws/tcp 监听器:本机真实起监听 → check ok=true(端口项 + 信道项);
//   - 未运行 → ok=false 单项「监听器未运行,请先启动」;
//   - kcp → 进程级探活(detail 注明 UDP 无连接);
//   - oss → 死端点配置走错误路径(无外部 OSS 凭据,§24C 验收口径);
//   - vps → SSH 转发不在位 + 机器不存在分支(不建真实 SSH)。
//
// PG 集成测试:需 ARTEX_PG_DSN 指向隔离库(如 5499),未设置按既有惯例跳过。

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	c2center "github.com/Autumn-27/artex/c2center"
	pgdb "github.com/Autumn-27/artex/db"
)

// c2CheckEnv 在 c2PatchTestEnv 之上注册进程级 C2 Manager 单例(测活读运行时状态)。
type c2CheckEnv struct {
	*c2PatchTestEnv
	mgr *c2center.Manager
}

func newC2CheckEnv(t *testing.T) *c2CheckEnv {
	t.Helper()
	e := newC2PatchTestEnv(t)
	mgr := c2center.NewManager(newC2Store(e.s, e.pg), slog.Default())
	setC2Manager(mgr)
	t.Cleanup(func() { setC2Manager(nil) })
	return &c2CheckEnv{c2PatchTestEnv: e, mgr: mgr}
}

// createCheckListener 建监听器行 + 起本地监听(端口 0 内核分配),返回 db 行。
func (e *c2CheckEnv) createCheckListener(t *testing.T, name, proto string, mutate func(*pgdb.C2ListenerInput), start bool) *pgdb.C2Listener {
	t.Helper()
	var transportSealed []byte
	if proto == c2center.ProtoOSS {
		// 指向本机必然拒绝的端口:oss 启动不联网,探测期走错误路径(无外部凭据)。
		cfg, _ := json.Marshal(map[string]any{
			"endpoint": "http://127.0.0.1:1", "bucket": "b", "access_key": "ak",
			"secret_key": "sk", "path_style": true,
		})
		sealed, err := e.s.sealC2Secret(cfg)
		if err != nil {
			t.Fatalf("密封 oss 配置失败: %v", err)
		}
		transportSealed = sealed
	}
	lid := e.createC2TestListener(t, name, proto, func(in *pgdb.C2ListenerInput) {
		in.TransportConfigEnc = transportSealed
		if mutate != nil {
			mutate(in)
		}
	})
	if start {
		l, err := e.pg.GetC2Listener(lid)
		if err != nil || l == nil {
			t.Fatalf("读取测试监听器失败: %v(%v)", l, err)
		}
		if err := e.mgr.StartListener(c2centerListenerFromDB(l)); err != nil {
			t.Fatalf("启动测试监听器失败: %v", err)
		}
		t.Cleanup(func() {
			_ = e.mgr.StopListener(lid)
			_, _ = e.pg.Exec(`DELETE FROM c2_listeners WHERE id=$1`, lid) // 行不留库
		})
		return l
	}
	l, err := e.pg.GetC2Listener(lid)
	if err != nil || l == nil {
		t.Fatalf("读取测试监听器失败: %v(%v)", l, err)
	}
	// 未启动路径同样清行(测活「未运行」分支用)。
	t.Cleanup(func() {
		_, _ = e.pg.Exec(`DELETE FROM c2_listeners WHERE id=$1`, lid)
	})
	return l
}

// postCheck 直接打 checkC2Listener handler,解析冻结形状的响应。
func postCheck(t *testing.T, s *Server, id int64) (int, c2ListenerCheckResult) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/c2/listeners/%d/check", id), nil)
	req.SetPathValue("id", fmt.Sprintf("%d", id))
	rec := httptest.NewRecorder()
	s.checkC2Listener(rec, req)
	var res c2ListenerCheckResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("测活响应解析失败(%d): %v / %s", rec.Code, err, rec.Body.String())
	}
	return rec.Code, res
}

// findCheckItem 按名称取检查项;缺失视为失败。
func findCheckItem(t *testing.T, res c2ListenerCheckResult, item string) c2ListenerCheckItem {
	t.Helper()
	for _, it := range res.Checks {
		if it.Item == item {
			return it
		}
	}
	t.Fatalf("检查项 %q 缺失: %+v", item, res.Checks)
	return c2ListenerCheckItem{}
}

// TestC2ListenerCheckLocalHTTP http 监听器实测:运行中 → ok=true(latency 非 nil,
// TCP 端口 + 信道响应双 ok);停止后 → ok=false「监听器未运行,请先启动」。
func TestC2ListenerCheckLocalHTTP(t *testing.T) {
	e := newC2CheckEnv(t)
	l := e.createCheckListener(t, "c2atest-chk-http", "http", nil, true)

	code, res := postCheck(t, e.s, l.ID)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if !res.OK {
		t.Fatalf("运行中监听器应 ok=true: %+v", res.Checks)
	}
	if res.LatencyMS == nil || *res.LatencyMS < 0 {
		t.Fatalf("成功 dial 应给出 latency_ms: %v", res.LatencyMS)
	}
	tcpItem := findCheckItem(t, res, "TCP 端口")
	if !tcpItem.OK {
		t.Fatalf("TCP 端口项应 ok: %+v", tcpItem)
	}
	chanItem := findCheckItem(t, res, "信道响应")
	if !chanItem.OK || !strings.Contains(chanItem.Detail, l.URLPath) {
		t.Fatalf("信道响应项应 ok 且 detail 带 /%s/e: %+v", l.URLPath, chanItem)
	}

	// 停止 → 未运行分支(单项,不做后续探测)。
	if err := e.mgr.StopListener(l.ID); err != nil {
		t.Fatalf("停止测试监听器失败: %v", err)
	}
	_, res = postCheck(t, e.s, l.ID)
	if res.OK {
		t.Fatalf("未运行监听器应 ok=false: %+v", res.Checks)
	}
	if res.LatencyMS != nil || len(res.Checks) != 1 {
		t.Fatalf("未运行分支应只有单项且无 latency: %+v", res)
	}
	st := findCheckItem(t, res, "运行状态")
	if st.OK || st.Detail != "监听器未运行,请先启动" {
		t.Fatalf("未运行文案不符: %+v", st)
	}
}

// TestC2ListenerCheckLocalWS ws 监听器实测:普通 GET(非 upgrade)拿到伪装 404
// 也算通(§24B:任何 HTTP 响应=通)。
func TestC2ListenerCheckLocalWS(t *testing.T) {
	e := newC2CheckEnv(t)
	l := e.createCheckListener(t, "c2atest-chk-ws", "ws", nil, true)

	_, res := postCheck(t, e.s, l.ID)
	if !res.OK {
		t.Fatalf("ws 运行中应 ok=true: %+v", res.Checks)
	}
	chanItem := findCheckItem(t, res, "信道响应")
	if !chanItem.OK {
		t.Fatalf("ws 信道探测应 ok(升级失败的 404 伪装也算通): %+v", chanItem)
	}
}

// TestC2ListenerCheckLocalTCP tcp 监听器实测:只拨端口,无信道项。
func TestC2ListenerCheckLocalTCP(t *testing.T) {
	e := newC2CheckEnv(t)
	l := e.createCheckListener(t, "c2atest-chk-tcp", "tcp", nil, true)

	_, res := postCheck(t, e.s, l.ID)
	if !res.OK || res.LatencyMS == nil {
		t.Fatalf("tcp 运行中应 ok=true 且有 latency: %+v", res)
	}
	findCheckItem(t, res, "TCP 端口")
	for _, it := range res.Checks {
		if it.Item == "信道响应" {
			t.Fatalf("tcp 不应做 HTTP 信道探测: %+v", res.Checks)
		}
	}
}

// TestC2ListenerCheckKCP kcp:进程级探活,detail 注明 UDP 无连接;latency 为空。
func TestC2ListenerCheckKCP(t *testing.T) {
	e := newC2CheckEnv(t)
	l := e.createCheckListener(t, "c2atest-chk-kcp", "kcp", nil, true)

	_, res := postCheck(t, e.s, l.ID)
	if !res.OK {
		t.Fatalf("kcp 运行中应 ok=true: %+v", res.Checks)
	}
	if res.LatencyMS != nil {
		t.Fatalf("kcp 无 TCP dial,latency 应为 null: %v", res.LatencyMS)
	}
	item := findCheckItem(t, res, "进程探活")
	if !item.OK || !strings.Contains(item.Detail, "UDP 无连接") || !strings.Contains(item.Detail, "进程级探活") {
		t.Fatalf("kcp 探活文案不符: %+v", item)
	}
}

// TestC2ListenerCheckOSSOffline oss:死端点走错误路径(无外部凭据的验收口径)。
// 进程级探活 ok(扫描 goroutine 存活),对象存储项 fail → 总 ok=false。
func TestC2ListenerCheckOSSOffline(t *testing.T) {
	e := newC2CheckEnv(t)
	l := e.createCheckListener(t, "c2atest-chk-oss", "oss", nil, true)

	_, res := postCheck(t, e.s, l.ID)
	if res.OK {
		t.Fatalf("死端点 oss 应 ok=false: %+v", res.Checks)
	}
	findCheckItem(t, res, "进程探活")
	ossItem := findCheckItem(t, res, "对象存储")
	if ossItem.OK {
		t.Fatalf("死端点对象存储项应 fail: %+v", ossItem)
	}
	if !strings.Contains(ossItem.Detail, "签名 GetBucket 失败") {
		t.Fatalf("对象存储失败文案不符: %+v", ossItem)
	}
	// CheckOSS 直接调用同口径报错。
	if err := e.mgr.CheckOSS(c2centerListenerFromDB(l)); err == nil {
		t.Fatalf("CheckOSS 对死端点应返回错误")
	}
}

// TestC2ListenerCheckOSSUnconfigured oss 未配置传输配置:CheckOSS 明确报错
//(§24B「未配置 → ok=false」语义)。
func TestC2ListenerCheckOSSUnconfigured(t *testing.T) {
	e := newC2CheckEnv(t)
	lid := e.createC2TestListener(t, "c2atest-chk-oss0", "oss", nil)
	t.Cleanup(func() { _, _ = e.pg.Exec(`DELETE FROM c2_listeners WHERE id=$1`, lid) })
	if err := e.mgr.CheckOSS(&c2center.Listener{ID: lid, URLPath: "c2pt"}); err == nil {
		t.Fatalf("未配置 oss 应报错")
	}
}

// TestC2ListenerCheckVPSMissingMachine vps 载体:SSH 转发不在位 + 关联机器不存在
// 双 fail(不建真实 SSH;失败分支与文案分类在此覆盖)。
func TestC2ListenerCheckVPSMissingMachine(t *testing.T) {
	e := newC2CheckEnv(t)
	l := e.createCheckListener(t, "c2atest-chk-vps", "http", func(in *pgdb.C2ListenerInput) {
		in.HostKind = pgdb.C2ListenerHostKindVPS
		in.VPSMachineID = 987654321
		in.RemotePort = 59999
	}, true)

	_, res := postCheck(t, e.s, l.ID)
	if res.OK {
		t.Fatalf("vps 缺转发/缺机器应 ok=false: %+v", res.Checks)
	}
	sshItem := findCheckItem(t, res, "SSH 转发")
	if sshItem.OK || !strings.Contains(sshItem.Detail, "SSH 入口转发不在位") {
		t.Fatalf("SSH 转发项应 fail 且注明断开: %+v", sshItem)
	}
	portItem := findCheckItem(t, res, "转发端口")
	if portItem.OK || !strings.Contains(portItem.Detail, "不存在") {
		t.Fatalf("转发端口项应 fail(机器不存在): %+v", portItem)
	}
}

// TestC2CheckDialAndProbeHelpers 纯函数层:真监听 → dial/探测 ok;死端口 → 拒绝
// 分类;404 伪装应答也算 HTTP 探测通过(§24B 核心语义)。
func TestC2CheckDialAndProbeHelpers(t *testing.T) {
	// 真监听(httptest):dial 通 + HTTP 探测拿到 404 也算通。
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	ms, ok, detail := c2CheckDialItem(addr)
	if !ok || ms < 0 {
		t.Fatalf("dial 真监听应 ok: %d/%v/%s", ms, ok, detail)
	}
	code, err := c2CheckHTTPProbe(addr, "/e", 2*time.Second)
	if err != nil || code == 0 {
		t.Fatalf("HTTP 探测真监听应拿到响应: %d/%v", code, err)
	}

	// 死端口:先占一个免费端口再释放,拨它 → 拒绝分类。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占位端口失败: %v", err)
	}
	dead := ln.Addr().String()
	_ = ln.Close()
	_, ok, detail = c2CheckDialItem(dead)
	if ok {
		t.Fatalf("拨死端口应失败")
	}
	if !strings.Contains(detail, "拒绝") && !strings.Contains(detail, "超时") {
		t.Fatalf("死端口 detail 应分类为拒绝/超时: %s", detail)
	}
	_, err = c2CheckHTTPProbe(dead, "/e", 2*time.Second)
	if err == nil {
		t.Fatalf("HTTP 探测死端口应报错")
	}

	// 空地址:明确失败而非 panic。
	if _, ok, detail := c2CheckDialItem(""); ok || detail == "" {
		t.Fatalf("空地址应 fail 带文案: %v/%s", ok, detail)
	}
}
