package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
)

// guardTestServer 起一个真实 server(经生产 New 注入 resolver),用于验证
// "任务级门限 → intercept.TaskGuardResolver → 拦截链路"这条端到端接线。
//
// 与 trafficEvidenceServer 同样的门禁:未显式配置 ARTEX_PG_DSN 就跳过,
// 绝不回退 config.json 去连用户正库。
func guardTestServer(t *testing.T) (*Server, *Task) {
	t.Helper()
	s, task, _ := guardTestServerWithToken(t)
	return s, task
}

// guardTestServerWithToken 同上,另外交出 JWT:走 HTTP 端点的用例必须带鉴权头,
// 否则拿到的是 401 而不是被测的业务分支。
func guardTestServerWithToken(t *testing.T) (*Server, *Task, string) {
	t.Helper()
	if _, err := testDSN(); err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := New(ctx, m, t.TempDir(), t.TempDir(), t.TempDir())
	task, err := m.CreateTask(fmt.Sprintf("guard-resolver-%d", time.Now().UnixNano()), "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		n, err := strconv.ParseInt(task.ID, 10, 64)
		if err != nil {
			return
		}
		m.pg.Exec(`DELETE FROM task_archives WHERE task_id=$1`, n)
		_ = m.pg.DeleteTask(n)
	})
	tok, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return s, task, tok
}

// guardModeOf 读内存任务句柄的当前门限(测试断言用)。
func guardModeOf(t *testing.T, s *Server, taskID string) string {
	t.Helper()
	task, ok := s.m.Task(taskID)
	if !ok || task == nil {
		t.Fatalf("task %s missing from manager", taskID)
	}
	mode, _ := task.taskGuardSnapshot()
	return mode
}

// TestTaskGuardResolverReadsMemoryHandle 断言注入的 resolver 直接反映内存任务
// 句柄的门限,且 PATCH 之后立即生效(不需要重启、不需要重新注入)。
func TestTaskGuardResolverReadsMemoryHandle(t *testing.T) {
	s, task := guardTestServer(t)

	// 默认:查得到任务(ok=true),门限为空 = 跟随全局。
	mode, ruleIDs, ok := s.taskGuardResolver()(task.ID)
	if !ok {
		t.Fatalf("resolver(task %s) ok = false, want true(任务已在内存里)", task.ID)
	}
	if mode != "" || len(ruleIDs) != 0 {
		t.Fatalf("default resolver = %q / %v, want \"\" / empty", mode, ruleIDs)
	}

	// 改成 off 后必须立刻读到 off —— 这正是"PATCH 要镜像进内存"的原因。
	off := "off"
	empty := []int64{}
	if _, err := s.m.UpdateTaskMetadata(task.ID, db.TaskPatch{GuardMode: &off, GuardRuleIDs: &empty}); err != nil {
		t.Fatal(err)
	}
	mode, _, ok = s.taskGuardResolver()(task.ID)
	if !ok || mode != "off" {
		t.Fatalf("after PATCH resolver = %q / ok=%v, want off / true", mode, ok)
	}

	// 改回跟随全局:空串是合法值,必须真的写回内存(用零值判断会在这里失败)。
	inherit := ""
	if _, err := s.m.UpdateTaskMetadata(task.ID, db.TaskPatch{GuardMode: &inherit}); err != nil {
		t.Fatal(err)
	}
	mode, _, ok = s.taskGuardResolver()(task.ID)
	if !ok || mode != "" {
		t.Fatalf("after reset resolver = %q / ok=%v, want \"\" / true", mode, ok)
	}
}

// TestTaskGuardResolverUnknownTaskIsNotOff 是本功能最重要的安全断言:
// 查不到任务必须返回 ok=false(拦截侧按"跟随全局"处理),绝不能返回 "off"。
// 返回 "off" 意味着任何一次加载竞态都会静默放通监管。
func TestTaskGuardResolverUnknownTaskIsNotOff(t *testing.T) {
	s, _ := guardTestServer(t)
	mode, _, ok := s.taskGuardResolver()("999999999")
	if ok {
		t.Fatal("resolver(unknown task) ok = true, want false")
	}
	if mode == "off" {
		t.Fatal("resolver(unknown task) returned \"off\" — 查不到配置绝不能等于放通")
	}
}

// TestTaskGuardResolverIsWiredIntoInterceptor 确认 New 里真的注入了 resolver:
// 未注入时 intercept 侧拿到 nil,任务级门限会整个失效。
func TestTaskGuardResolverIsWiredIntoInterceptor(t *testing.T) {
	s, task := guardTestServer(t)

	// 造一条必然命中 Bash 的规则,再对比 off 与 inherit 两种门限下的匹配结果。
	rule, err := s.m.pg.CreateInterceptRule(
		fmt.Sprintf("guard-test-%d", time.Now().UnixNano()),
		"tool_name", "string", "Bash", "deny", "guard test rule",
		100, true, false, 0, "deny",
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.m.pg.DeleteInterceptRule(rule.ID) })
	s.m.interceptor.Invalidate()

	ctx := intercept.WithTaskContext(context.Background(), task.ID, "test", nil)

	// 先确认前提成立:跟随全局时这条规则命中。若这一步不成立,下面的"未命中"
	// 就说明不了任何事(可能是规则压根没生效,而不是 off 起了作用)。
	if _, hit := s.m.interceptor.Match(ctx, "Bash", []byte(`{"command":"ls"}`)); !hit {
		t.Fatal("rule did not match on an inherit task — 测试前提不成立")
	}

	off := "off"
	if _, err := s.m.UpdateTaskMetadata(task.ID, db.TaskPatch{GuardMode: &off}); err != nil {
		t.Fatal(err)
	}
	if _, hit := s.m.interceptor.Match(ctx, "Bash", []byte(`{"command":"ls"}`)); hit {
		t.Fatal("task with guard_mode=off still matched a rule — resolver 没接进 Match")
	}
}

// TestUpdateTaskMetadataGuardEndpoint 覆盖 PATCH 端点的输入形态:
// 没传(不动)、传空(改回默认)、传非法值(400)、空 body(400)。
func TestUpdateTaskMetadataGuardEndpoint(t *testing.T) {
	s, task, tok := guardTestServerWithToken(t)
	handler := s.Handler()

	patch := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/tasks/"+task.ID, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}

	// 非法值 → 400。
	if w := patch(`{"guard_mode":"disabled"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid guard_mode: code = %d, want 400 (%s)", w.Code, w.Body)
	}
	// 合法:off。
	if w := patch(`{"guard_mode":"off"}`); w.Code != http.StatusOK {
		t.Fatalf("set off: code = %d, want 200 (%s)", w.Code, w.Body)
	}
	if got := guardModeOf(t, s, task.ID); got != "off" {
		t.Fatalf("in-memory guard_mode after PATCH off = %q, want off", got)
	}
	// name-only 的 PATCH 不能顺手把门限清掉(没传就不动)。
	name := "renamed while guarding"
	if w := patch(fmt.Sprintf(`{"name":%q}`, name)); w.Code != http.StatusOK {
		t.Fatalf("rename: code = %d, want 200 (%s)", w.Code, w.Body)
	}
	if got := guardModeOf(t, s, task.ID); got != "off" {
		t.Fatalf("guard_mode after name-only PATCH = %q, want off(没传就不该动)", got)
	}
	// 传空串 → 改回跟随全局。这是"指针区分没传/传空"的核心用例:
	// 若实现用零值判断,这次会被当成"没传",门限永远留在 off。
	w := patch(`{"guard_mode":""}`)
	if w.Code != http.StatusOK {
		t.Fatalf("reset to inherit: code = %d, want 200 (%s)", w.Code, w.Body)
	}
	var dto TaskDTO
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.GuardMode != "" {
		t.Fatalf("response guard_mode after reset = %q, want \"\"", dto.GuardMode)
	}
	if got := guardModeOf(t, s, task.ID); got != "" {
		t.Fatalf("in-memory guard_mode after reset = %q, want \"\"", got)
	}
	// 空 body 没有任何字段 → 400(保持原有语义)。
	if w := patch(`{}`); w.Code != http.StatusBadRequest {
		t.Fatalf("empty patch: code = %d, want 400", w.Code)
	}
	// 非法的规则 id → 400,不能静默丢弃(悄悄少一条规则等于悄悄放宽监管)。
	if w := patch(`{"guard_rule_ids":[-1]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid guard_rule_ids: code = %d, want 400 (%s)", w.Code, w.Body)
	}
}

// TestCreateTaskWithGuardMode 覆盖建任务时就把门限定下来的路径(前端表单走的入口)。
func TestCreateTaskWithGuardMode(t *testing.T) {
	s, _, tok := guardTestServerWithToken(t)
	handler := s.Handler()

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}

	w := post(fmt.Sprintf(`{"description":"guard create %d","goal":"g","guard_mode":"off"}`, time.Now().UnixNano()))
	if w.Code != http.StatusCreated {
		t.Fatalf("create with guard_mode=off: code = %d, want 201 (%s)", w.Code, w.Body)
	}
	var dto TaskDTO
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if n, err := strconv.ParseInt(dto.ID, 10, 64); err == nil {
			s.m.pg.Exec(`DELETE FROM task_archives WHERE task_id=$1`, n)
			_ = s.m.pg.DeleteTask(n)
		}
	})
	if dto.GuardMode != "off" {
		t.Fatalf("created task guard_mode = %q, want off", dto.GuardMode)
	}
	// 内存句柄也要带上(否则 resolver 会按跟随全局跑,与列表回显矛盾)。
	if got := guardModeOf(t, s, dto.ID); got != "off" {
		t.Fatalf("created task in-memory guard_mode = %q, want off", got)
	}
	// 非法值 → 400,而不是建出一个门限没生效的任务。
	bad := post(fmt.Sprintf(`{"description":"guard create bad %d","goal":"g","guard_mode":"nope"}`, time.Now().UnixNano()))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("create with invalid guard_mode: code = %d, want 400 (%s)", bad.Code, bad.Body)
	}
}
