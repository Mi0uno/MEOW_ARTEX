package c2center

// vps 载体抽象单测(契约 C2_CONTRACTS.md §12):StartListenerWithIngress 的登记/
// 连带停止、boot 恢复路径经 IngressOpener 建转发、opener 失败时本地监听保持运行。
// 转发本体是接口(IngressForwarder),用计数 fake 验证 Stop 语义,不碰真 SSH。

import (
	"context"
	"sync"
	"testing"
	"time"
)

// countingForwarder 是 IngressForwarder 的计数 fake。
type countingForwarder struct {
	mu    sync.Mutex
	stops int
}

func (f *countingForwarder) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	return nil
}

func (f *countingForwarder) stopCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stops
}

// setCarrier 给已有测试监听器改载体字段(fakeStore 的 map 同包可直改)。
func setCarrier(st *fakeStore, id int64, kind string) {
	st.mu.Lock()
	if l, ok := st.listeners[id]; ok {
		l.HostKind = kind
		if kind == HostKindVPS {
			l.VPSMachineID = 9
			l.RemotePort = 8443
		}
	}
	st.mu.Unlock()
}

// waitUntil 轮询等条件成立(boot 是异步 goroutine,需要等)。
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", what)
}

// TestStartListenerWithIngress 手动 start 编排:登记转发器 → StopListener 连带停
// (先转发后监听);重复登记覆盖旧转发器且旧转发器被 Stop(不泄漏 SSH 连接)。
func TestStartListenerWithIngress(t *testing.T) {
	mgr, _, _, l := newTestListener(t)
	ig := &countingForwarder{}
	if err := mgr.StartListenerWithIngress(l, ig); err != nil {
		t.Fatalf("StartListenerWithIngress: %v", err)
	}
	if !mgr.ListenerRunning(l.ID) || !mgr.IngressRunning(l.ID) {
		t.Fatalf("本地监听与转发器都应在位: running=%v ingress=%v",
			mgr.ListenerRunning(l.ID), mgr.IngressRunning(l.ID))
	}
	// 重复 start 携带新转发器:旧转发器必须被 Stop(防泄漏)。
	ig2 := &countingForwarder{}
	if err := mgr.StartListenerWithIngress(l, ig2); err != nil {
		t.Fatalf("重复 StartListenerWithIngress 应幂等成功: %v", err)
	}
	if got := ig.stopCount(); got != 1 {
		t.Fatalf("旧转发器应被 Stop 恰一次, got %d", got)
	}
	// 停止:转发器与监听器一并下线;再停报「未在运行」。
	if err := mgr.StopListener(l.ID); err != nil {
		t.Fatalf("StopListener: %v", err)
	}
	if mgr.ListenerRunning(l.ID) || mgr.IngressRunning(l.ID) {
		t.Fatal("停止后本地监听与转发器都不应在位")
	}
	if got := ig2.stopCount(); got != 1 {
		t.Fatalf("在位转发器应被连带 Stop 恰一次, got %d", got)
	}
	if err := mgr.StopListener(l.ID); err == nil {
		t.Fatal("停止未运行监听器应报错")
	}
	// StartListenerWithIngress 本地失败时原样返回错误(转发器不吞)。
	bad := &Listener{ID: 99999, Name: "ghost", BindHost: "127.0.0.1", Port: 1, URLPath: "x"}
	if err := mgr.StartListenerWithIngress(bad, &countingForwarder{}); err == nil {
		t.Fatal("本地监听失败应返回错误")
	}
}

// TestBootRecoversVPSIngress boot 恢复路径:Manager.Start 对 enabled 监听器先起
// 本地,再经 IngressOpener 为 vps 载体建转发;ctx 取消时转发与监听一并停止。
func TestBootRecoversVPSIngress(t *testing.T) {
	mgr, st, _, local := newTestListener(t)
	vps := st.addListener("vps-l", "vps", 0, true, testKey())
	setCarrier(st, vps.ID, HostKindVPS)

	opened := map[int64]bool{}
	var openMu sync.Mutex
	mgr.IngressOpener = func(l *Listener) (IngressForwarder, error) {
		openMu.Lock()
		defer openMu.Unlock()
		if l.HostKind != HostKindVPS {
			return nil, nil // local 载体:无转发
		}
		opened[l.ID] = true
		return &countingForwarder{}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		mgr.Start(ctx)
		close(done)
	}()

	waitUntil(t, "vps 载体监听器上线", func() bool { return mgr.ListenerRunning(vps.ID) })
	waitUntil(t, "local 监听器上线", func() bool { return mgr.ListenerRunning(local.ID) })
	waitUntil(t, "vps 转发器登记", func() bool { return mgr.IngressRunning(vps.ID) })
	if mgr.IngressRunning(local.ID) {
		t.Fatal("local 载体不应有转发器")
	}
	openMu.Lock()
	both := opened[vps.ID]
	openMu.Unlock()
	if !both {
		t.Fatal("vps 载体应回调过 IngressOpener")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ctx 取消后 Start 未退出")
	}
	if mgr.ListenerRunning(vps.ID) || mgr.ListenerRunning(local.ID) {
		t.Fatal("ctx 取消后监听器应全部停止")
	}
}

// TestBootIngressFailureKeepsLocal boot 时转发建立失败(SSH 不可达等):只记日志,
// 本地监听保持运行且不登记转发器——用户可在页面手动 start 重建。
func TestBootIngressFailureKeepsLocal(t *testing.T) {
	mgr, st, _, _ := newTestListener(t)
	vps := st.addListener("vps-l", "vps2", 0, true, testKey())
	setCarrier(st, vps.ID, HostKindVPS)

	mgr.IngressOpener = func(l *Listener) (IngressForwarder, error) {
		return nil, context.DeadlineExceeded // 模拟 SSH 建连失败
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		mgr.Start(ctx)
		close(done)
	}()

	waitUntil(t, "vps 载体本地监听上线", func() bool { return mgr.ListenerRunning(vps.ID) })
	if mgr.IngressRunning(vps.ID) {
		t.Fatal("转发失败不应登记转发器")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ctx 取消后 Start 未退出")
	}
}
