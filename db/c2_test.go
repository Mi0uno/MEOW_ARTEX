package db

import (
	"fmt"
	"testing"
	"time"
)

// c2TestListener 落一个测试监听器(enc_key 密文用假字节——本层只搬运密文)。
func c2TestListener(t *testing.T, d *DB, name string) *C2Listener {
	t.Helper()
	id, err := d.CreateC2Listener(C2ListenerInput{
		Name:         name,
		BindHost:     "127.0.0.1",
		Port:         0,
		URLPath:      "auto-" + fmt.Sprint(time.Now().UnixNano()),
		Callback:     "http://203.0.113.9:8443",
		EncKeySealed: []byte("sealed-key-bytes-" + name),
		Notes:        "c2 访问层测试",
	})
	if err != nil {
		t.Fatalf("建监听器失败: %v", err)
	}
	l, err := d.GetC2Listener(id)
	if err != nil || l == nil {
		t.Fatalf("回读监听器失败: %v(%v)", l, err)
	}
	return l
}

// TestC2ListenerLifecycle 建查改启停删 + 密文列纪律(enc_key 只经 GetC2ListenerKey 出库)。
func TestC2ListenerLifecycle(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	l := c2TestListener(t, d, fmt.Sprintf("c2-lifecycle-%d", time.Now().UnixNano()))
	defer d.DeleteC2Listener(l.ID) //nolint:errcheck

	if l.Port != 0 || l.Proto != "http" || !l.Enabled || l.SessionCount != 0 {
		t.Fatalf("监听器默认值不对: %+v", l)
	}
	// 密钥密文原样可取(解封在 server 层)。
	sealed, err := d.GetC2ListenerKey(l.ID)
	if err != nil || string(sealed) != "sealed-key-bytes-"+l.Name {
		t.Fatalf("GetC2ListenerKey 应返回原密文: %q(%v)", sealed, err)
	}

	// 更新普通字段;不带密文 = 不改密钥。
	newName := l.Name + "-v2"
	if err := d.UpdateC2Listener(l.ID, C2ListenerInput{
		Name: newName, BindHost: "0.0.0.0", Port: 8443,
		URLPath: l.URLPath, Callback: "http://203.0.113.9:9443",
		Notes: "改过",
	}); err != nil {
		t.Fatalf("更新监听器失败: %v", err)
	}
	got, _ := d.GetC2Listener(l.ID)
	if got.Port != 8443 || got.Callback != "http://203.0.113.9:9443" || got.Name != newName {
		t.Fatalf("更新未生效: %+v", got)
	}
	if sealed2, _ := d.GetC2ListenerKey(l.ID); string(sealed2) != string(sealed) {
		t.Fatalf("更新不应动密钥")
	}
	// 无名拒绝。
	if err := d.UpdateC2Listener(l.ID, C2ListenerInput{Name: " "}); err == nil {
		t.Fatalf("空名称应被拒绝")
	}

	if err := d.SetC2ListenerEnabled(l.ID, false); err != nil {
		t.Fatalf("停用失败: %v", err)
	}
	if got, _ = d.GetC2Listener(l.ID); got.Enabled {
		t.Fatalf("停用未生效")
	}

	if _, err := d.ListC2Listeners(); err != nil {
		t.Fatalf("列监听器失败: %v", err)
	}
	deleted, err := d.DeleteC2Listener(l.ID)
	if err != nil || !deleted {
		t.Fatalf("删除失败: %v(%v)", deleted, err)
	}
	if got, _ := d.GetC2Listener(l.ID); got != nil {
		t.Fatalf("删除后仍能读到")
	}
}

// TestC2SessionUpsertSweep 幂等复活/按 sid 查/sleep/状态/绑定/筛选/离线清扫。
func TestC2SessionUpsertSweep(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	l := c2TestListener(t, d, fmt.Sprintf("c2-sessions-%d", time.Now().UnixNano()))
	defer d.DeleteC2Listener(l.ID) //nolint:errcheck
	uid := fmt.Sprintf("uid-%d", time.Now().UnixNano())

	in := C2SessionInput{
		ListenerID: l.ID, BeaconUID: uid, SID: "sid-" + uid,
		Hostname: "目标主机甲", Username: "admin", Elevated: true,
		OS: "windows", Arch: "x64", LocalIPs: "10.0.0.5,192.168.1.5", PID: 4321, Process: "explorer",
	}
	id1, err := d.UpsertC2Session(in)
	if err != nil {
		t.Fatalf("注册会话失败: %v", err)
	}
	// 幂等复活:同 listener+uid 再注册 = 同一行,主机信息刷新 + 新 sid + 回 online。
	in.SID = "sid-" + uid + "-rotated"
	in.Hostname = "目标主机甲-改名"
	id2, err := d.UpsertC2Session(in)
	if err != nil || id2 != id1 {
		t.Fatalf("同 beacon 重连应复活同一会话: %d vs %d (%v)", id1, id2, err)
	}
	m, err := d.GetC2SessionBySID(in.SID)
	if err != nil || m == nil || m.ID != id1 || m.Hostname != "目标主机甲-改名" || m.Status != C2SessionStatusOnline {
		t.Fatalf("按新 sid 查复活会话失败: %+v(%v)", m, err)
	}
	if m.Meta == nil {
		t.Fatalf("meta 永不 nil")
	}
	if m2, _ := d.GetC2Session(id1); m2 == nil || m2.SID != in.SID {
		t.Fatalf("按 id 查失败: %+v", m2)
	}
	if m3, _ := d.GetC2SessionBySID("sid-不存在-xyz"); m3 != nil {
		t.Fatalf("不存在的 sid 应返回 nil")
	}
	// 清理本用例会话(按 uid 定位,listener 删除不级联)。
	defer d.Exec(`DELETE FROM c2_sessions WHERE listener_id=$1`, l.ID) //nolint:errcheck

	// sleep 调整与越界拒绝。
	if err := d.SetC2SessionSleep(in.SID, 30, 10); err != nil {
		t.Fatalf("调 sleep 失败: %v", err)
	}
	if m, _ = d.GetC2Session(id1); m.SleepSec != 30 || m.JitterPct != 10 {
		t.Fatalf("sleep 未生效: %+v", m)
	}
	if err := d.SetC2SessionSleep(in.SID, 0, 10); err == nil {
		t.Fatalf("sleep_sec=0 应拒绝")
	}
	if err := d.SetC2SessionSleep(in.SID, 30, 95); err == nil {
		t.Fatalf("jitter_pct=95 应拒绝")
	}
	// 状态机与白名单。
	if err := d.SetC2SessionStatusBySID(in.SID, C2SessionStatusDead); err != nil {
		t.Fatalf("kill 失败: %v", err)
	}
	if err := d.SetC2SessionStatusBySID(in.SID, "zombie"); err == nil {
		t.Fatalf("非法状态应拒绝")
	}
	if err := d.SetC2SessionStatusBySID(in.SID, C2SessionStatusOnline); err != nil {
		t.Fatalf("恢复 online 失败: %v", err)
	}
	// 绑定/解绑平台任务。
	if err := d.BindC2SessionTask(id1, 555); err != nil {
		t.Fatalf("绑定失败: %v", err)
	}
	if m, _ = d.GetC2Session(id1); m.TaskID != 555 {
		t.Fatalf("绑定未生效: %+v", m)
	}
	if err := d.BindC2SessionTask(id1, 0); err != nil {
		t.Fatalf("解绑失败: %v", err)
	}
	if m, _ = d.GetC2Session(id1); m.TaskID != 0 {
		t.Fatalf("解绑未生效: task_id=%d", m.TaskID)
	}

	// 列表筛选:status / listener / q。
	if _, total, err := d.ListC2Sessions(C2SessionFilter{ListenerID: l.ID, Limit: 10}); err != nil || total < 1 {
		t.Fatalf("按监听器筛选失败: total=%d (%v)", total, err)
	}
	if _, total, err := d.ListC2Sessions(C2SessionFilter{Status: C2SessionStatusOnline, Q: "主机甲", Limit: 10}); err != nil || total < 1 {
		t.Fatalf("按关键字筛选失败: total=%d (%v)", total, err)
	}
	if _, _, err := d.ListC2Sessions(C2SessionFilter{Status: "bogus"}); err == nil {
		t.Fatalf("非法状态筛选应拒绝")
	}

	// 离线清扫:回拨 last_seen 后 sweep 应置 offline,且不碰 dead 会话。
	if _, err := d.UpsertC2Session(C2SessionInput{ListenerID: l.ID, BeaconUID: uid + "-b", SID: "sid-" + uid + "-b"}); err != nil {
		t.Fatalf("注册第二会话失败: %v", err)
	}
	d.Exec(`UPDATE c2_sessions SET last_seen = now() - interval '2 hours', sleep_sec=10 WHERE listener_id=$1`, l.ID) //nolint:errcheck
	n, err := d.SweepOfflineSessions(2)                                                                              // 10*(1+2)=30s 宽限
	if err != nil {
		t.Fatalf("清扫失败: %v", err)
	}
	if n < 2 {
		t.Fatalf("应扫下线 2 个会话,实际 %d", n)
	}
	if m, _ = d.GetC2Session(id1); m.Status != C2SessionStatusOffline {
		t.Fatalf("超时会话应为 offline: %s", m.Status)
	}
	// dead 不被清扫翻状态。
	d.SetC2SessionStatusBySID(in.SID, C2SessionStatusDead)                                   //nolint:errcheck
	d.Exec(`UPDATE c2_sessions SET last_seen = now() - interval '2 hours' WHERE id=$1`, id1) //nolint:errcheck
	if _, err := d.SweepOfflineSessions(2); err != nil {
		t.Fatalf("二次清扫失败: %v", err)
	}
	if m, _ = d.GetC2Session(id1); m.Status != C2SessionStatusDead {
		t.Fatalf("dead 会话不应被清扫改状态: %s", m.Status)
	}
	if c, err := d.CountC2SessionsByListener(l.ID); err != nil || c < 2 {
		t.Fatalf("按监听器数会话失败: %d (%v)", c, err)
	}
	if _, _, _, err := d.CountC2SessionsByStatus(); err != nil {
		t.Fatalf("三态计数失败: %v", err)
	}
}

// TestC2TasksQueue 任务白名单/原子取走/终态回填/增量拉取/计数。
func TestC2TasksQueue(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	l := c2TestListener(t, d, fmt.Sprintf("c2-tasks-%d", time.Now().UnixNano()))
	defer d.DeleteC2Listener(l.ID) //nolint:errcheck
	sid := fmt.Sprintf("sid-task-%d", time.Now().UnixNano())
	sessionID, err := d.UpsertC2Session(C2SessionInput{ListenerID: l.ID, BeaconUID: sid, SID: sid})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	defer d.Exec(`DELETE FROM c2_sessions WHERE id=$1`, sessionID) //nolint:errcheck

	// 白名单兜底:非法 kind/actor 拒绝。
	if _, err := d.CreateC2Task(C2TaskInput{SessionID: sessionID, Kind: "reboot"}); err == nil {
		t.Fatalf("非法 kind 应拒绝")
	}
	if _, err := d.CreateC2Task(C2TaskInput{SessionID: sessionID, Kind: C2TaskKindShell, Actor: "robot"}); err == nil {
		t.Fatalf("非法 actor 应拒绝")
	}

	ids := map[int64]bool{}
	for i := 0; i < 3; i++ {
		id, err := d.CreateC2Task(C2TaskInput{
			SessionID: sessionID, Kind: C2TaskKindShell,
			Input: fmt.Sprintf("whoami #اشر%d", i), Actor: C2TaskActorAgent, CreatedBy: "c2_exec",
		})
		if err != nil {
			t.Fatalf("建任务失败: %v", err)
		}
		ids[id] = true
	}
	if n, err := d.CountQueuedC2Tasks(); err != nil || n < 3 {
		t.Fatalf("排队计数不对: %d (%v)", n, err)
	}

	// 原子取走:queued→sent,且不会二次取到。
	taken, err := d.TakeQueuedTasks(sessionID, 10)
	if err != nil || len(taken) != 3 {
		t.Fatalf("应取走 3 个任务: %d (%v)", len(taken), err)
	}
	for _, tk := range taken {
		if !ids[tk.ID] || tk.Status != C2TaskStatusSent || tk.SentAt == nil {
			t.Fatalf("取走的任务应置 sent: %+v", tk)
		}
	}
	if again, err := d.TakeQueuedTasks(sessionID, 10); err != nil || len(again) != 0 {
		t.Fatalf("二次取走应为空: %d (%v)", len(again), err)
	}

	// 终态回填。
	first := taken[0].ID
	if err := d.FinishC2Task(first, C2TaskStatusDone, "DESKTOP-ABC\\admin", "", 0, ""); err != nil {
		t.Fatalf("回填 done 失败: %v", err)
	}
	if err := d.FinishC2Task(first, "sent", "x", "", 0, ""); err == nil {
		t.Fatalf("非终态应拒绝")
	}
	if tk, _ := d.GetC2Task(first); tk == nil || tk.Status != C2TaskStatusDone || tk.DoneAt == nil || tk.Output == "" {
		t.Fatalf("终态回填未生效: %+v", tk)
	}
	if err := d.FinishC2Task(taken[1].ID, C2TaskStatusError, "", "", 0, "命令超时"); err != nil {
		t.Fatalf("回填 error 失败: %v", err)
	}

	// 增量拉取(after_id):after_id 只越过终态任务,queued/sent 要重复返回
	// (前端靠这个看状态迁移;集成补充钉死的语义)。RETURNING 顺序无保证,
	// 按状态找仍处 sent 的那条。
	var sentOne *C2Task
	for _, tk := range taken {
		if tk.Status == C2TaskStatusSent {
			sentOne = tk
		}
	}
	if sentOne == nil {
		t.Fatalf("取走的任务里应有一条仍是 sent")
	}
	after := sentOne.ID
	for _, tk := range taken {
		if tk.ID > after {
			after = tk.ID // after 推进到最大 id:此时 id>after 分支无命中
		}
	}
	inc, err := d.ListC2Tasks(sessionID, after, 100)
	if err != nil || len(inc) != 1 || inc[0].ID != sentOne.ID {
		t.Fatalf("sent 任务应在 after_id 处重复返回: n=%d (%v)", len(inc), err)
	}
	// id 不大于 after 但仍 queued 的任务同样必须返回(OR 分支)。
	qID, err := d.CreateC2Task(C2TaskInput{
		SessionID: sessionID, Kind: C2TaskKindShell, Input: "id arch", Actor: C2TaskActorAgent,
	})
	if err != nil {
		t.Fatalf("补建排队任务失败: %v", err)
	}
	if inc, err = d.ListC2Tasks(sessionID, qID, 100); err != nil || len(inc) != 2 {
		t.Fatalf("queued/sent 任务应跨 after_id 重复返回: n=%d (%v)", len(inc), err)
	}
	gotIDs := map[int64]bool{}
	for _, tk := range inc {
		gotIDs[tk.ID] = true
	}
	if !gotIDs[qID] || !gotIDs[sentOne.ID] {
		t.Fatalf("应同时返回 queued(%d) 与 sent(%d): %v", qID, sentOne.ID, gotIDs)
	}
	// 全部落到终态后,after 越过即空。
	if err := d.FinishC2Task(sentOne.ID, C2TaskStatusDone, "", "", 0, ""); err != nil {
		t.Fatalf("回填 sent→done 失败: %v", err)
	}
	if err := d.FinishC2Task(qID, C2TaskStatusDone, "", "", 0, ""); err != nil {
		t.Fatalf("回填 queued→done 失败: %v", err)
	}
	afterMax := qID
	for _, tk := range taken {
		if tk.ID > afterMax {
			afterMax = tk.ID
		}
	}
	if inc, err = d.ListC2Tasks(sessionID, afterMax, 100); err != nil || len(inc) != 0 {
		t.Fatalf("终态任务被 after 越过后不应复现: n=%d (%v)", len(inc), err)
	}
	// 按状态计数 + 最近任务。
	if q, s, dn, e, err := d.CountC2TasksByStatus(sessionID); err != nil || q+s+dn+e < 3 {
		t.Fatalf("按状态计数不对: %d/%d/%d/%d (%v)", q, s, dn, e, err)
	}
	recent, err := d.ListRecentC2Tasks(10)
	if err != nil || len(recent) == 0 {
		t.Fatalf("最近任务拉取失败: %d (%v)", len(recent), err)
	}
}

// TestC2Transfers 传输记录 CRUD 与任务回填。
func TestC2Transfers(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	l := c2TestListener(t, d, fmt.Sprintf("c2-transfers-%d", time.Now().UnixNano()))
	defer d.DeleteC2Listener(l.ID) //nolint:errcheck
	sid := fmt.Sprintf("sid-tf-%d", time.Now().UnixNano())
	sessionID, err := d.UpsertC2Session(C2SessionInput{ListenerID: l.ID, BeaconUID: sid, SID: sid})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	defer d.Exec(`DELETE FROM c2_sessions WHERE id=$1`, sessionID) //nolint:errcheck

	if _, err := d.CreateC2Transfer(C2TransferInput{SessionID: sessionID, Direction: "sideways"}); err == nil {
		t.Fatalf("非法方向应拒绝")
	}
	tfID, err := d.CreateC2Transfer(C2TransferInput{
		SessionID: sessionID, Direction: C2TransferUp, RemotePath: "/tmp/工具.exe", Size: 1234,
	})
	if err != nil {
		t.Fatalf("建传输失败: %v", err)
	}
	taskID, err := d.CreateC2Task(C2TaskInput{SessionID: sessionID, Kind: C2TaskKindUpload, Input: "x"})
	if err != nil {
		t.Fatalf("建上传任务失败: %v", err)
	}
	defer d.Exec(`DELETE FROM c2_tasks WHERE id=$1`, taskID) //nolint:errcheck
	if err := d.SetC2TransferTask(tfID, taskID); err != nil {
		t.Fatalf("回填任务失败: %v", err)
	}
	if err := d.UpdateC2Transfer(tfID, C2TaskStatusDone, 1234, 4096, "/data/c2/uploads/x.bin", ""); err != nil {
		t.Fatalf("完成传输失败: %v", err)
	}
	if err := d.UpdateC2Transfer(tfID, "flying", 0, -1, "", ""); err == nil {
		t.Fatalf("非法状态应拒绝")
	}
	tr, err := d.GetC2Transfer(tfID)
	if err != nil || tr == nil || tr.TaskID != taskID || tr.Status != "done" || tr.DoneAt == nil {
		t.Fatalf("传输回填未生效: %+v (%v)", tr, err)
	}
	if tr.Size != 4096 {
		t.Fatalf("totalSize 回写未生效: size=%d", tr.Size)
	}
	if tr.LocalPath == "" {
		t.Fatalf("local_path 应已记录(内部字段)")
	}
	if items, err := d.ListC2Transfers(sessionID, 50); err != nil || len(items) != 1 {
		t.Fatalf("列传输失败: %d (%v)", len(items), err)
	}
}

// TestC2LootContentDiscipline 列表密文不出库(content 恒空串+has_content),
// 详情才带原始密文;kind 白名单与筛选。
func TestC2LootContentDiscipline(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	l := c2TestListener(t, d, fmt.Sprintf("c2-loot-%d", time.Now().UnixNano()))
	defer d.DeleteC2Listener(l.ID) //nolint:errcheck
	sid := fmt.Sprintf("sid-loot-%d", time.Now().UnixNano())
	sessionID, err := d.UpsertC2Session(C2SessionInput{ListenerID: l.ID, BeaconUID: sid, SID: sid})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	defer d.Exec(`DELETE FROM c2_sessions WHERE id=$1`, sessionID) //nolint:errcheck

	if _, err := d.CreateC2Loot(C2LootInput{SessionID: sessionID, Kind: "bitcoin"}); err == nil {
		t.Fatalf("非法 kind 应拒绝")
	}
	credID, err := d.CreateC2Loot(C2LootInput{
		SessionID: sessionID, Kind: C2LootKindCredential, Name: "管理员凭据",
		Content: "sealed-cipher-bytes", SourcePath: "C:/Users/admin/东八区.txt",
	})
	if err != nil {
		t.Fatalf("记战利品失败: %v", err)
	}
	defer d.DeleteC2Loot(credID) //nolint:errcheck
	if _, err := d.CreateC2Loot(C2LootInput{
		SessionID: sessionID, Kind: C2LootKindOther, Name: "备忘录", Content: "明文内容",
	}); err != nil {
		t.Fatalf("记 other 战利品失败: %v", err)
	}

	items, total, err := d.ListC2Loot(C2LootFilter{SessionID: sessionID, Limit: 10})
	if err != nil || total != 2 || len(items) != 2 {
		t.Fatalf("列表计数不对: total=%d len=%d (%v)", total, len(items), err)
	}
	for _, it := range items {
		if it.Content != "" {
			t.Fatalf("列表 content 必须恒空串(密文不出库): %+v", it)
		}
		if !it.HasContent {
			t.Fatalf("有内容的行 has_content 应为 true: %+v", it)
		}
	}
	if f, _, err := d.ListC2Loot(C2LootFilter{SessionID: sessionID, Kind: C2LootKindCredential}); err != nil || len(f) != 1 {
		t.Fatalf("按 kind 筛选失败: %d (%v)", len(f), err)
	}
	// 各 kind 计数(页签角标):五键齐全、credential 至少 1。
	counts, err := d.CountC2LootByKind()
	if err != nil {
		t.Fatalf("kind 计数失败: %v", err)
	}
	for _, k := range []string{C2LootKindCredential, C2LootKindHash, C2LootKindFile, C2LootKindScreenshot, C2LootKindOther} {
		if _, ok := counts[k]; !ok {
			t.Fatalf("计数缺 kind %s: %v", k, counts)
		}
	}
	if counts[C2LootKindCredential] < 1 {
		t.Fatalf("credential 计数应 ≥1: %v", counts)
	}
	if f, _, err := d.ListC2Loot(C2LootFilter{SessionID: sessionID, Q: "东八区"}); err != nil || len(f) != 1 {
		t.Fatalf("按关键字筛选失败: %d (%v)", len(f), err)
	}
	if _, _, err := d.ListC2Loot(C2LootFilter{Kind: "nuclear"}); err == nil {
		t.Fatalf("非法 kind 筛选应拒绝")
	}
	// 详情才带原始密文。
	full, err := d.GetC2Loot(credID)
	if err != nil || full == nil || full.Content != "sealed-cipher-bytes" {
		t.Fatalf("详情应带原始密文: %+v (%v)", full, err)
	}
	if deleted, _ := d.DeleteC2Loot(credID); !deleted {
		t.Fatalf("删除失败")
	}
}

// TestC2PayloadsCRUD 载荷登记/令牌查询/路径回填/白名单。
func TestC2PayloadsCRUD(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	l := c2TestListener(t, d, fmt.Sprintf("c2-payloads-%d", time.Now().UnixNano()))
	defer d.DeleteC2Listener(l.ID) //nolint:errcheck

	token := fmt.Sprintf("tok-%d", time.Now().UnixNano())
	if _, err := d.CreateC2Payload(C2PayloadInput{ListenerID: l.ID, Format: "exe", Target: "windows", Token: token}); err == nil {
		t.Fatalf("非法 format 应拒绝")
	}
	if _, err := d.CreateC2Payload(C2PayloadInput{ListenerID: l.ID, Format: C2PayloadFormatPs1, Target: "mainframe", Token: token}); err == nil {
		t.Fatalf("非法 target 应拒绝")
	}
	if _, err := d.CreateC2Payload(C2PayloadInput{ListenerID: l.ID, Format: C2PayloadFormatPs1, Token: " "}); err == nil {
		t.Fatalf("空 token 应拒绝")
	}
	pid, err := d.CreateC2Payload(C2PayloadInput{
		ListenerID: l.ID, Name: "演示载荷", Format: C2PayloadFormatPs1, Target: "windows",
		Token: token, SHA256: "abc", Size: 9, Oneliner: "powershell -c IEX(...)", CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("建载荷失败: %v", err)
	}
	defer d.DeleteC2Payload(pid) //nolint:errcheck
	if err := d.UpdateC2PayloadFilePath(pid, "/data/c2/payloads/1.ps1"); err != nil {
		t.Fatalf("回填路径失败: %v", err)
	}
	if got, _ := d.GetC2Payload(pid); got == nil || got.FilePath == "" || got.Name != "演示载荷" {
		t.Fatalf("回读载荷失败: %+v", got)
	}
	if got, _ := d.GetC2PayloadByToken(token); got == nil || got.ID != pid {
		t.Fatalf("按令牌读载荷失败: %+v", got)
	}
	if got, _ := d.GetC2PayloadByToken("tok-不存在"); got != nil {
		t.Fatalf("不存在令牌应返回 nil")
	}
	items, err := d.ListC2Payloads(l.ID)
	if err != nil || len(items) != 1 {
		t.Fatalf("列载荷失败: %d (%v)", len(items), err)
	}
	if items, err = d.ListC2Payloads(0); err != nil || len(items) < 1 {
		t.Fatalf("列全部载荷失败: %d (%v)", len(items), err)
	}
}

// TestC2SessionCompanyFilter 锁死企业归属筛选路径:
// session.task_id → tasks → task_scope(kind='company') EXISTS。
func TestC2SessionCompanyFilter(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	l := c2TestListener(t, d, fmt.Sprintf("c2-company-%d", time.Now().UnixNano()))
	defer d.DeleteC2Listener(l.ID) //nolint:errcheck
	sid := fmt.Sprintf("sid-co-%d", time.Now().UnixNano())
	sessionID, err := d.UpsertC2Session(C2SessionInput{ListenerID: l.ID, BeaconUID: sid, SID: sid})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	defer d.Exec(`DELETE FROM c2_sessions WHERE id=$1`, sessionID) //nolint:errcheck

	tk, err := d.CreateTask("c2 企业归属测试", "goal", nil, 0, 0)
	if err != nil {
		t.Skipf("建平台任务失败(库不可写?): %v", err)
	}
	defer d.DeleteTask(tk.ID) //nolint:errcheck
	var companyID int64
	if err := d.QueryRow(`INSERT INTO companies(name,nkey) VALUES($1,$2) RETURNING id`,
		fmt.Sprintf("c2-co-%d", time.Now().UnixNano()), fmt.Sprintf("nkey-%d", time.Now().UnixNano())).
		Scan(&companyID); err != nil {
		t.Fatalf("建企业失败: %v", err)
	}
	defer d.Exec(`DELETE FROM companies WHERE id=$1`, companyID)   //nolint:errcheck
	defer d.Exec(`DELETE FROM task_scope WHERE task_id=$1`, tk.ID) //nolint:errcheck
	if _, err := d.Exec(`INSERT INTO task_scope(task_id, kind, company_id) VALUES ($1,'company',$2)`,
		tk.ID, companyID); err != nil {
		t.Fatalf("写 task_scope 失败: %v", err)
	}
	if err := d.BindC2SessionTask(sessionID, tk.ID); err != nil {
		t.Fatalf("绑定任务失败: %v", err)
	}

	// 绑定后按企业能筛到;换个无关企业筛不到。
	if _, total, err := d.ListC2Sessions(C2SessionFilter{CompanyID: companyID, Limit: 10}); err != nil || total != 1 {
		t.Fatalf("按企业筛选应命中 1: %d (%v)", total, err)
	}
	if _, total, err := d.ListC2Sessions(C2SessionFilter{CompanyID: companyID + 999999, Limit: 10}); err != nil || total != 0 {
		t.Fatalf("无关企业不应命中: %d (%v)", total, err)
	}
	// 解绑后同企业筛不到(EXISTS 断链)。
	if err := d.BindC2SessionTask(sessionID, 0); err != nil {
		t.Fatalf("解绑失败: %v", err)
	}
	if _, total, err := d.ListC2Sessions(C2SessionFilter{CompanyID: companyID, Limit: 10}); err != nil || total != 0 {
		t.Fatalf("解绑后不应命中: %d (%v)", total, err)
	}
}

// c2TestVpsMachine 落一台测试 VPS 机器(密码密文用假字节——本层只搬运密文)。
func c2TestVpsMachine(t *testing.T, d *DB, name string) *VpsMachine {
	t.Helper()
	m, err := d.CreateVpsMachine(VpsMachineInput{
		Name:           name,
		Host:           "203.0.113.10",
		Username:       "root",
		AuthType:       VpsAuthPassword,
		PasswordSealed: []byte("sealed-vps-password"),
		WorkspaceBase:  "/root/.artex-ws",
	})
	if err != nil {
		t.Fatalf("建测试 VPS 机器失败: %v", err)
	}
	return m
}

// TestC2ListenerCarrierColumns 二期载体列(契约 §12):默认 local、vps 三元组读写、
// 机器名带出、白名单校验、vps→local 归零。
func TestC2ListenerCarrierColumns(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	vm := c2TestVpsMachine(t, d, fmt.Sprintf("c2-载体机-%d", time.Now().UnixNano()))
	defer d.DeleteVpsMachine(vm.ID) //nolint:errcheck

	// 一期默认:不传载体字段 = local。
	l := c2TestListener(t, d, fmt.Sprintf("c2-carrier-local-%d", time.Now().UnixNano()))
	defer d.DeleteC2Listener(l.ID) //nolint:errcheck
	if l.HostKind != C2ListenerHostKindLocal || l.VPSMachineID != 0 || l.RemotePort != 0 {
		t.Fatalf("默认载体应为 local: %+v", l)
	}

	// vps 载体:三元组入库 + 机器名带出(List 与 Get 同口径)。
	vlName := fmt.Sprintf("c2-carrier-vps-%d", time.Now().UnixNano())
	vid, err := d.CreateC2Listener(C2ListenerInput{
		Name: vlName, BindHost: "127.0.0.1", Port: 0,
		URLPath:      "auto-" + fmt.Sprint(time.Now().UnixNano()),
		Callback:     "", // vps 载体 callback 可空(前端用机器 host 拼)
		EncKeySealed: []byte("sealed-key-bytes-" + vlName),
		HostKind:     C2ListenerHostKindVPS, VPSMachineID: vm.ID, RemotePort: 443,
	})
	if err != nil {
		t.Fatalf("建 vps 载体监听器失败: %v", err)
	}
	defer d.DeleteC2Listener(vid) //nolint:errcheck
	vl, err := d.GetC2Listener(vid)
	if err != nil || vl == nil {
		t.Fatalf("回读 vps 载体监听器失败: %v(%v)", vl, err)
	}
	if vl.HostKind != C2ListenerHostKindVPS || vl.VPSMachineID != vm.ID || vl.RemotePort != 443 {
		t.Fatalf("vps 载体字段未落库: %+v", vl)
	}
	if vl.VPSMachineName != vm.Name {
		t.Fatalf("机器名应从 vps_machines 带出: got=%q want=%q", vl.VPSMachineName, vm.Name)
	}
	listed, err := d.ListC2Listeners()
	if err != nil {
		t.Fatalf("列监听器失败: %v", err)
	}
	found := false
	for _, it := range listed {
		if it.ID == vid {
			found = true
			if it.VPSMachineName != vm.Name {
				t.Fatalf("列表机器名带出失败: %+v", it)
			}
		}
	}
	if !found {
		t.Fatalf("列表未包含 vps 载体监听器 #%d", vid)
	}

	// 白名单校验:vps 缺机器/缺端口/非法 host_kind。
	bad := []C2ListenerInput{
		{Name: "缺机器", HostKind: C2ListenerHostKindVPS, RemotePort: 443},
		{Name: "缺端口", HostKind: C2ListenerHostKindVPS, VPSMachineID: vm.ID},
		{Name: "非法端口", HostKind: C2ListenerHostKindVPS, VPSMachineID: vm.ID, RemotePort: 70000},
		{Name: "非法载体", HostKind: "k8s", VPSMachineID: vm.ID, RemotePort: 443},
	}
	for i := range bad {
		if _, err := d.CreateC2Listener(bad[i]); err == nil {
			t.Fatalf("第 %d 个非法载体输入应被拒绝: %+v", i+1, bad[i])
		}
	}

	// 更新:vps → local 归零;local → vps 补三元组。
	if err := d.UpdateC2Listener(vid, C2ListenerInput{
		Name: vlName, BindHost: "127.0.0.1", Port: 0, URLPath: vl.URLPath,
		HostKind: C2ListenerHostKindLocal,
	}); err != nil {
		t.Fatalf("切回 local 失败: %v", err)
	}
	if got, _ := d.GetC2Listener(vid); got.HostKind != C2ListenerHostKindLocal ||
		got.VPSMachineID != 0 || got.RemotePort != 0 || got.VPSMachineName != "" {
		t.Fatalf("切回 local 应归零载体字段: %+v", got)
	}
	if err := d.UpdateC2Listener(vid, C2ListenerInput{
		Name: vlName, BindHost: "127.0.0.1", Port: 0, URLPath: vl.URLPath,
		HostKind: C2ListenerHostKindVPS, VPSMachineID: vm.ID, RemotePort: 8443,
	}); err != nil {
		t.Fatalf("切到 vps 失败: %v", err)
	}
	if got, _ := d.GetC2Listener(vid); got.VPSMachineID != vm.ID || got.RemotePort != 8443 {
		t.Fatalf("切到 vps 后字段未更新: %+v", got)
	}
}

// TestC2PayloadV2Columns 二期载荷列(契约 §13/§14):evasion_profile_id 建行带入、
// 构建产物回填(sha256/size/file_path/built_on_vps)。
func TestC2PayloadV2Columns(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	l := c2TestListener(t, d, fmt.Sprintf("c2-payload-v2-%d", time.Now().UnixNano()))
	defer d.DeleteC2Listener(l.ID) //nolint:errcheck

	token := fmt.Sprintf("tokv2-%d", time.Now().UnixNano())
	pid, err := d.CreateC2Payload(C2PayloadInput{
		ListenerID: l.ID, Name: "gosrc-源码包", Format: C2PayloadFormatGoSrc, Target: "windows",
		Token: token, SHA256: "srcsha", Size: 12345, CreatedBy: "test",
		EvasionProfileID: 77,
	})
	if err != nil {
		t.Fatalf("建 gosrc 载荷失败: %v", err)
	}
	defer d.DeleteC2Payload(pid) //nolint:errcheck
	pl, err := d.GetC2Payload(pid)
	if err != nil || pl == nil {
		t.Fatalf("回读载荷失败: %v(%v)", pl, err)
	}
	if pl.EvasionProfileID != 77 || pl.BuiltOnVPS {
		t.Fatalf("载荷二期列不对: %+v", pl)
	}

	// 构建产物回填(§14):二进制替换 file_path 并刷新 sha256/size。
	if err := d.UpdateC2PayloadBuildResult(pid, "/data/c2/payloads/9_beacon.exe", "binsha", 4096000); err != nil {
		t.Fatalf("回填构建产物失败: %v", err)
	}
	if got, _ := d.GetC2Payload(pid); got == nil || !got.BuiltOnVPS ||
		got.SHA256 != "binsha" || got.Size != 4096000 || got.EvasionProfileID != 77 {
		t.Fatalf("构建产物回填未生效: %+v", got)
	}
}
