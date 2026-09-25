package c2center

// Manager 生命周期单测:启停、端口占用、enabled 过滤、sweep、ctx 取消停机。

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestManagerListenerLifecycle(t *testing.T) {
	mgr, st, key, l := newTestListener(t)
	if mgr.ListenerRunning(l.ID) {
		t.Fatal("启动前不应在运行")
	}
	base := startListener(t, mgr, l)
	if !mgr.ListenerRunning(l.ID) {
		t.Fatal("启动后应在运行")
	}
	if !strings.HasPrefix(base, "http://127.0.0.1:") {
		t.Fatalf("实际地址不符: %s", base)
	}
	// 幂等 start。
	if err := mgr.StartListener(l); err != nil {
		t.Fatalf("重复 start 应幂等: %v", err)
	}
	// 已知监听器停了再起。
	if err := mgr.StopListener(l.ID); err != nil {
		t.Fatalf("StopListener: %v", err)
	}
	if mgr.ListenerRunning(l.ID) {
		t.Fatal("停止后不应在运行")
	}
	if mgr.ListenerAddr(l.ID) != "" {
		t.Fatalf("停止后 addr 应为空")
	}
	// 停止未运行的:明确报错。
	if err := mgr.StopListener(l.ID); err == nil {
		t.Fatal("停止未运行监听器应报错")
	}
	// 再起一次仍可用(端口重分配)。
	if err := mgr.StartListener(l); err != nil {
		t.Fatalf("重新 start: %v", err)
	}
	t.Cleanup(func() { _ = mgr.StopListener(l.ID) })

	// 密钥缺失/长度非法 → 明确错误。
	st.keys[l.ID] = []byte("short-key")
	l2 := st.addListener("bad", "bbb", 0, true, key)
	delete(st.keys, l2.ID)
	if err := mgr.StartListener(l2); err == nil {
		t.Fatal("缺密钥应报错")
	}
	st.keys[l2.ID] = []byte("short-key")
	if err := mgr.StartListener(l2); err == nil {
		t.Fatal("短密钥应报错")
	}
	_ = key
}

func TestManagerPortConflict(t *testing.T) {
	mgr, st, key, l := newTestListener(t)
	if err := mgr.StartListener(l); err != nil {
		t.Fatalf("StartListener: %v", err)
	}
	defer func() { _ = mgr.StopListener(l.ID) }()
	port := mgr.ListenerAddr(l.ID) // 127.0.0.1:<port>
	idx := strings.LastIndex(port, ":")
	occupied := port[idx+1:]

	// 另一个监听器抢同一端口 → 明确错误。
	conflict := st.addListener("conflict", "xyz", atoi(occupied), true, key)
	err := mgr.StartListener(conflict)
	if err == nil {
		t.Fatal("端口占用应报错")
	}
	if !strings.Contains(err.Error(), "监听") {
		t.Fatalf("错误应含监听上下文: %v", err)
	}
	if mgr.ListenerRunning(conflict.ID) {
		t.Fatal("冲突监听器不应在运行")
	}
}

func TestManagerStartEnabledOnlyAndCancel(t *testing.T) {
	st := newFakeStore()
	key := testKey()
	on := st.addListener("on", "aaa", 0, true, key)
	off := st.addListener("off", "bbb", 0, false, key)
	mgr := NewManager(st, quietLogger())
	mgr.BaseDir = ""
	mgr.SweepInterval = time.Hour // 干扰最小化

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		mgr.Start(ctx)
		close(done)
	}()

	waitFor(t, 3*time.Second, func() bool { return mgr.ListenerRunning(on.ID) })
	if mgr.ListenerRunning(off.ID) {
		t.Fatal("disabled 监听器不应被 Start 拉起")
	}
	if mgr.ListenerAddr(on.ID) == "" {
		t.Fatal("运行中监听器应有实际地址")
	}
	// ctx 取消 → Start 返回且监听器全停。
	cancel()
	waitFor(t, 3*time.Second, func() bool { return !mgr.ListenerRunning(on.ID) })
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Start 应随 ctx 取消返回")
	}
}

func TestManagerSweep(t *testing.T) {
	mgr, st, _, _ := newTestListener(t)
	uid := BeaconUID(1, "host1", "alice", "windows")
	sid, err := st.UpsertSession(SessionInput{ListenerID: 1, BeaconUID: uid, SID: "s1",
		Hostname: "host1", Username: "alice", OS: "windows"})
	if err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
	// 心跳新鲜:sweep 不动。
	if n, _ := st.SweepOfflineSessions(2); n != 0 {
		t.Fatalf("新鲜会话不应被 sweep: %d", n)
	}
	// 心跳超时(sleep=10,grace=2 → 30s):置 offline。
	st.setLastSeen("s1", time.Now().Add(-time.Minute))
	if n, _ := mgr.store.SweepOfflineSessions(mgr.SweepGraceMultiplier); n != 1 {
		t.Fatalf("超时会话应被 sweep: %d", n)
	}
	if cur, _ := st.GetSession(sid); cur.Status != StatusOffline {
		t.Fatalf("状态应为 offline: %+v", cur)
	}
	// dead 不参与 sweep。
	_ = st.SetSessionStatusBySID("s1", StatusDead)
	st.setLastSeen("s1", time.Now().Add(-time.Hour))
	if n, _ := st.SweepOfflineSessions(2); n != 0 {
		t.Fatalf("dead 会话不应参与 sweep: %d", n)
	}
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("条件在 %v 内未满足", timeout)
}
