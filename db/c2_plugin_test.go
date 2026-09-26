package db

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// c2PluginTestSession 落一个测试会话(插件任务挂在它名下,运行记录=任务流水)。
func c2PluginTestSession(t *testing.T, d *DB) int64 {
	t.Helper()
	l := c2TestListener(t, d, fmt.Sprintf("c2-plugin-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _, _ = d.DeleteC2Listener(l.ID) })
	uid := fmt.Sprintf("uid-plugin-%d", time.Now().UnixNano())
	id, err := d.UpsertC2Session(C2SessionInput{
		ListenerID: l.ID, BeaconUID: uid, SID: "sid-" + uid,
		Hostname: "插件测试主机", Username: "tester", OS: "windows", Arch: "x64",
	})
	if err != nil {
		t.Fatalf("注册测试会话失败: %v", err)
	}
	return id
}

// TestC2PluginLifecycle 手工插件的建查改删 + 默认值/白名单 + 列表过滤与分页口径。
func TestC2PluginLifecycle(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	// 白名单兜底:非法平台/形态/语言/JSON 拒绝。
	if _, err := d.CreateC2Plugin(C2PluginInput{Name: "坏平台", Platform: "solaris"}); err == nil {
		t.Fatal("非法 platform 应被拒绝")
	}
	if _, err := d.CreateC2Plugin(C2PluginInput{Name: "坏形态", Kind: "elf"}); err == nil {
		t.Fatal("非法 kind 应被拒绝")
	}
	if _, err := d.CreateC2Plugin(C2PluginInput{Name: "坏语言", Language: "ruby"}); err == nil {
		t.Fatal("非法 language 应被拒绝")
	}
	if _, err := d.CreateC2Plugin(C2PluginInput{Name: "坏JSON", ParamsSchema: json.RawMessage(`{"a":`)}); err == nil {
		t.Fatal("非法 params_schema 应被拒绝")
	}

	name := fmt.Sprintf("端口探测-%d", time.Now().UnixNano())
	id, err := d.CreateC2Plugin(C2PluginInput{
		Name:         name,
		Platform:     C2PluginPlatformLinux,
		Kind:         C2PluginKindScript,
		Language:     C2PluginLanguageSh,
		Entry:        "nc -zv {{host}} {{port:22}}",
		Content:      "# 参考正文",
		ParamsSchema: json.RawMessage(`{"host":{"type":"string","desc":"目标主机"}}`),
		Description:  "手工建的测试插件",
	})
	if err != nil {
		t.Fatalf("建插件失败: %v", err)
	}
	defer func() { _, _ = d.DeleteC2Plugin(id) }()

	got, err := d.GetC2Plugin(id)
	if err != nil || got == nil {
		t.Fatalf("回读插件失败: %v(%v)", got, err)
	}
	if got.Builtin || got.Platform != "linux" || got.Kind != "script" || got.Language != "sh" {
		t.Fatalf("插件默认值不对: %+v", got)
	}
	// params_schema 经 JSONB 规范化,按解析后的结构比对(键序/空白可能被库改写)。
	var schema map[string]any
	if err := json.Unmarshal(got.ParamsSchema, &schema); err != nil {
		t.Fatalf("params_schema 应是合法 JSON 对象: %v(%s)", err, got.ParamsSchema)
	}
	host, ok := schema["host"].(map[string]any)
	if !ok || host["type"] != "string" {
		t.Fatalf("params_schema 内容不对: %s", got.ParamsSchema)
	}

	// 更新(整体覆盖);内置守卫对非 builtin 行无影响。
	ok2, err := d.UpdateC2Plugin(id, C2PluginInput{
		Name: name + "-v2", Platform: C2PluginPlatformCross, Kind: C2PluginKindBinary,
		Language: C2PluginLanguagePy, Entry: "run {{file}}", BlobPath: "/tmp/blob.bin",
		ParamsSchema: json.RawMessage(`{}`), Description: "改过",
	})
	if err != nil || !ok2 {
		t.Fatalf("更新插件失败: %v(%v)", ok2, err)
	}
	got, _ = d.GetC2Plugin(id)
	if got.Name != name+"-v2" || got.BlobPath != "/tmp/blob.bin" {
		t.Fatalf("更新未生效: %+v", got)
	}

	// 列表:平台过滤 + q 匹配 + total 口径;params_schema 永不 nil。
	items, total, err := d.ListC2Plugins(C2PluginFilter{Platform: C2PluginPlatformCross, Q: "v2"})
	if err != nil {
		t.Fatalf("列插件失败: %v", err)
	}
	found := false
	for _, p := range items {
		if p.ID == id {
			found = true
		}
		if p.ParamsSchema == nil {
			t.Fatalf("params_schema 永不 nil: %+v", p)
		}
	}
	if !found {
		t.Fatalf("平台+q 过滤应命中刚建的插件(total=%d)", total)
	}
	if _, total2, err := d.ListC2Plugins(C2PluginFilter{Q: "绝不存在的插件xyz"}); err != nil || total2 != 0 {
		t.Fatalf("无命中应 total=0: %d(%v)", total2, err)
	}

	// 删除;再删一次 = false。
	if ok, err := d.DeleteC2Plugin(id); err != nil || !ok {
		t.Fatalf("删插件失败: %v(%v)", ok, err)
	}
	if ok, _ := d.DeleteC2Plugin(id); ok {
		t.Fatal("重复删除应返回 false")
	}
	if p, _ := d.GetC2Plugin(id); p != nil {
		t.Fatal("删除后应查不到")
	}
}

// TestC2PluginBuiltinSeed 内置播种:幂等(重复播种不重复插行)、builtin 守卫拒改删、
// 种子清单完整性(≥6 且 ps1/sh 按平台各有实现、params_schema 合法 JSON)。
func TestC2PluginBuiltinSeed(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	seeds := BuiltinC2Plugins()
	if len(seeds) < 6 {
		t.Fatalf("内置插件至少 6 个,实得 %d", len(seeds))
	}
	seen := map[string]bool{}
	ps1Count, shCount := 0, 0
	for _, s := range seeds {
		if seen[s.Name] {
			t.Fatalf("种子名重复: %s", s.Name)
		}
		seen[s.Name] = true
		var probe map[string]any
		if err := json.Unmarshal(s.ParamsSchema, &probe); err != nil {
			t.Fatalf("种子 «%s» params_schema 非法: %v", s.Name, err)
		}
		switch s.Language {
		case C2PluginLanguagePs1:
			ps1Count++
		case C2PluginLanguageSh:
			shCount++
		}
	}
	if ps1Count == 0 || shCount == 0 {
		t.Fatalf("ps1/sh 按平台各出对应实现: ps1=%d sh=%d", ps1Count, shCount)
	}

	// 预热 + 清场:演示库可能已被运行实例播过 builtin 行,先幂等播种一轮再用 SQL
	// 清掉(builtin 行常规入口拒删,测试里直接 SQL 清),保证「首次插入」分支可断言;
	// 测试结束时行会被重新播回,不留空窗。
	if _, _, err := d.SeedBuiltinC2Plugins(); err != nil {
		t.Fatalf("预热播种失败: %v", err)
	}
	if _, err := d.Exec(`DELETE FROM c2_plugins WHERE builtin`); err != nil {
		t.Fatalf("清内置行失败: %v", err)
	}

	// 首次播种:全部插入。
	if n, u, err := d.SeedBuiltinC2Plugins(); err != nil || n != len(seeds) || u != 0 {
		t.Fatalf("首次播种应插 %d 行: ins=%d upd=%d err=%v", len(seeds), n, u, err)
	}
	// 重复播种:幂等,全部走 UPDATE,不重复插行。
	if n, u, err := d.SeedBuiltinC2Plugins(); err != nil || n != 0 || u != len(seeds) {
		t.Fatalf("重复播种应全部更新: ins=%d upd=%d err=%v", n, u, err)
	}
	items, total, err := d.ListC2Plugins(C2PluginFilter{Q: ""})
	if err != nil {
		t.Fatalf("列插件失败: %v", err)
	}
	builtinCount := 0
	for _, p := range items {
		if p.Builtin {
			builtinCount++
		}
	}
	if builtinCount != len(seeds) {
		t.Fatalf("builtin 行数应恰为 %d,实得 %d(total=%d)", len(seeds), builtinCount, total)
	}

	// builtin 守卫:改删一律拒绝(SQL 带 builtin=false 守卫,返回 false)。
	var builtinID int64
	for _, p := range items {
		if p.Builtin {
			builtinID = p.ID
			break
		}
	}
	if ok, err := d.UpdateC2Plugin(builtinID, C2PluginInput{Name: "篡改内置"}); err != nil || ok {
		t.Fatalf("内置插件应拒改: ok=%v err=%v", ok, err)
	}
	if ok, err := d.DeleteC2Plugin(builtinID); err != nil || ok {
		t.Fatalf("内置插件应拒删: ok=%v err=%v", ok, err)
	}
	if p, _ := d.GetC2Plugin(builtinID); p == nil {
		t.Fatal("内置插件被守卫误删")
	}
}

// TestC2PluginTaskRunChain 运行链路落库断言:插件运行记录 = kind='plugin' 的
// c2_tasks 行(初始 queued,进既有任务流水通道,可被 TakeQueuedTasks 取走)。
func TestC2PluginTaskRunChain(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	sessionID := c2PluginTestSession(t, d)

	// 非法输入拒收。
	if _, err := d.CreateC2PluginTask(0, `{"command":"x"}`, C2TaskActorUser, "t"); err == nil {
		t.Fatal("session_id=0 应拒绝")
	}
	if _, err := d.CreateC2PluginTask(sessionID, " ", C2TaskActorUser, "t"); err == nil {
		t.Fatal("空 input 应拒绝")
	}
	if _, err := d.CreateC2PluginTask(sessionID, `{"command":"x"}`, "robot", "t"); err == nil {
		t.Fatal("非法 actor 应拒绝")
	}

	input := `{"plugin_id":1,"name":"系统信息 · Windows","params":{"verbose":"1"},"command":"Write-Output hi"}`
	taskID, err := d.CreateC2PluginTask(sessionID, input, C2TaskActorAgent, "c2_plugin_run")
	if err != nil {
		t.Fatalf("建插件任务失败: %v", err)
	}
	got, err := d.GetC2Task(taskID)
	if err != nil || got == nil {
		t.Fatalf("回读插件任务失败: %v(%v)", got, err)
	}
	if got.SessionID != sessionID || got.Kind != "plugin" || got.Input != input {
		t.Fatalf("插件任务行不对: %+v", got)
	}
	if got.Status != C2TaskStatusQueued || got.Actor != C2TaskActorAgent || got.CreatedBy != "c2_plugin_run" {
		t.Fatalf("任务初始态/操作者不对: %+v", got)
	}

	// 走既有通道:queued 任务必须能被 beacon 轮询原样取走(TakeQueuedTasks 不挑 kind)。
	taken, err := d.TakeQueuedTasks(sessionID, 10)
	if err != nil {
		t.Fatalf("取任务失败: %v", err)
	}
	var picked *C2Task
	for _, tk := range taken {
		if tk.ID == taskID {
			picked = tk
		}
	}
	if picked == nil {
		t.Fatalf("插件任务应可被既有轮询通道取走: %d 条", len(taken))
	}
	if picked.Status != C2TaskStatusSent {
		t.Fatalf("取走后应置 sent: %+v", picked)
	}

	// 终态回填复用既有 FinishC2Task(结果进流水)。
	if err := d.FinishC2Task(taskID, C2TaskStatusDone, "== 系统信息 ==", "", 0, ""); err != nil {
		t.Fatalf("回填终态失败: %v", err)
	}
	got, _ = d.GetC2Task(taskID)
	if got.Status != C2TaskStatusDone || got.Output != "== 系统信息 ==" {
		t.Fatalf("终态回填未生效: %+v", got)
	}
}
