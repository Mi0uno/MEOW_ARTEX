package server

// vps_agent.go 的纯逻辑单测:超时钳制/输出截尾/工作区路径/提示词块/工具行为。
// 不连真实 PG/SSH——机器句柄经 vpscenter.Store 的内存 fake 提供,全局 Manager
// 用 setVPSManager 显式置入并在测试结束恢复。

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/vpscenter"
	actool "github.com/Autumn-27/norma/tool"
)

// ---- 1. Bash 执行超时钳制 ----

func TestVPSBashTimeout(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want time.Duration
	}{
		{"缺省走默认120s", 0, 120 * time.Second},
		{"负数同缺省", -5, 120 * time.Second},
		{"毫秒正常换算", 1500, 1500 * time.Millisecond},
		{"超过600s钳到上限", 10 * 60 * 1000, 600 * time.Second},
		{"远超上限也钳到上限", 1 << 30, 600 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := vpsBashTimeout(c.in); got != c.want {
				t.Fatalf("vpsBashTimeout(%d) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// ---- 2. 输出截尾/取尾(UTF-8 安全) ----

func TestTruncateVPSOutput(t *testing.T) {
	// 未超限:原样返回、不标记截断。
	out, truncated := truncateVPSOutput([]byte("hello world"), 1024)
	if truncated || out != "hello world" {
		t.Fatalf("未超限应原样返回: out=%q truncated=%v", out, truncated)
	}
	// 超限:恰好截到 limit 字节并标记。
	src := strings.Repeat("a", 300)
	out, truncated = truncateVPSOutput([]byte(src), 100)
	if !truncated || len(out) != 100 || out != src[:100] {
		t.Fatalf("超限应截到 limit 字节: len=%d truncated=%v", len(out), truncated)
	}
	// 多字节字符不拦腰截断:边界落在汉字中间时回退到完整字符。
	multi := []byte(strings.Repeat("渗", 64)) // 每个汉字 3 字节,共 192 字节
	out, truncated = truncateVPSOutput(multi, 100)
	if !truncated {
		t.Fatal("应标记截断")
	}
	if !utf8.ValidString(out) {
		t.Fatalf("截尾结果必须是合法 UTF-8: %q", out)
	}
	if len(out) > 100 {
		t.Fatalf("截尾后仍超限: %d", len(out))
	}
	if want := 33 * 3; len(out) != want { // 100/3=33 个完整汉字
		t.Fatalf("应保留 33 个完整汉字(99 字节), got %d 字节", len(out))
	}
}

func TestTailVPSOutput(t *testing.T) {
	// 未超限:原样。
	if got := tailVPSOutput([]byte("abc"), 100); got != "abc" {
		t.Fatalf("未超限应原样: %q", got)
	}
	// 超限:取尾部 limit 字节。
	if got := tailVPSOutput([]byte("0123456789"), 4); got != "6789" {
		t.Fatalf("应取尾部: %q", got)
	}
	// 多字节:尾部落在汉字中间时丢弃开头残缺字节,保留完整字符。
	multi := []byte(strings.Repeat("渗", 64)) // 192 字节
	got := tailVPSOutput(multi, 100)
	if !utf8.ValidString(got) {
		t.Fatalf("尾部结果必须是合法 UTF-8: %q", got)
	}
	// 100 字节尾部 = 上一个汉字的最后 1 字节(残缺,丢弃) + 33 个完整汉字(99 字节)。
	if want := strings.Repeat("渗", 33); got != want {
		t.Fatalf("取尾应保留 33 个完整汉字, got %d 字节 %q", len(got), got)
	}
}

// ---- 3. 远程工作区路径(Windows 主机上禁反斜杠) ----

func TestVPSWorkspacePath(t *testing.T) {
	cases := []struct {
		name string
		base string
		want string
	}{
		{"普通路径", "/srv/artex", "/srv/artex/task_9"},
		{"尾部斜杠去掉", "/srv/artex/", "/srv/artex/task_9"},
		{"Windows 反斜杠换正斜杠", `C:\artex`, "C:/artex/task_9"},
		{"Windows 尾部反斜杠", `C:\artex\`, "C:/artex/task_9"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := vpsWorkspacePath(c.base, 9); got != c.want {
				t.Fatalf("vpsWorkspacePath(%q, 9) = %q, want %q", c.base, got, c.want)
			}
		})
	}
}

// ---- 4. 系统提示词块 ----

func TestVPSSystemBlock(t *testing.T) {
	s := &Server{}
	if got := s.vpsSystemBlock(nil, 5); got != "" {
		t.Fatalf("未指派机器应返回空串, got %q", got)
	}
	m := &vpscenter.Machine{
		ID: 3, Name: "hunter-01", Host: "10.0.0.8", Port: 22, Status: "busy",
		OsName: "Ubuntu", Arch: "x86_64", WorkspaceBase: "/srv/artex",
	}
	got := s.vpsSystemBlock(m, 5)
	for _, want := range []string{
		"hunter-01", "10.0.0.8", "Ubuntu", "x86_64",
		"/srv/artex/task_5", // 工作目录固定
		"独立 shell",         // 独立 shell 语义
		"Read/Write",        // 平台文件工具操作本地工作区的提醒
		"vps_machine_info",  // 工具指引
		"vps_install_toolset",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("系统提示词块缺少 %q:\n%s", want, got)
		}
	}
}

// ---- 5. 工具清单读库守护(nil 安全,集合永不为 nil) ----

func TestVPSToolRosterNilSafe(t *testing.T) {
	s := &Server{}
	installed, missing := s.vpsToolRoster(42)
	if installed == nil || missing == nil {
		t.Fatalf("集合必须初始化为空 slice: installed=%v missing=%v", installed, missing)
	}
}

// ---- 6. 全局 Manager 为 nil 时的容错路径 ----

func TestAssignAndReleaseVPSNilManager(t *testing.T) {
	prev := VPSManager()
	defer setVPSManager(prev)
	setVPSManager(nil)

	// 不 panic、不阻断(nil task 也不 panic)。
	assignVPSOnCreate(nil, 1, true)
	assignVPSOnCreate(&Task{ID: "7"}, 1, true)
	releaseTaskVPSOnTerminal(7)
	releaseTaskVPSOnTerminal(0)
}

// ---- 7. vps_machine_info 工具 ----

// fakeVPSStore 只实现被测路径用到的查询;其余方法嵌入 nil 接口(不应被调用)。
type fakeVPSStore struct {
	vpscenter.Store
	byTask map[int64]*vpscenter.Machine
}

func (f *fakeVPSStore) GetMachineByTask(taskID int64) (*vpscenter.Machine, error) {
	m, ok := f.byTask[taskID]
	if !ok || m == nil {
		return nil, nil
	}
	return m, nil
}

// withVPSManager 置入临时全局 Manager,测试结束自动恢复(t.Cleanup)。
func withVPSManager(t *testing.T, m *vpscenter.Manager) {
	t.Helper()
	prev := VPSManager()
	setVPSManager(m)
	t.Cleanup(func() { setVPSManager(prev) })
}

// vpsToolRun 以给定任务会话上下文调用工具,返回文本结果(CoreTool 的入口是 Call)。
func vpsToolRun(t *testing.T, tool actool.CoreTool, taskID int64, input string) actool.Result {
	t.Helper()
	ctx := context.Background()
	if taskID > 0 {
		ctx = agent.WithRunInfo(ctx, agent.RunInfo{TaskID: taskID})
	}
	res, err := tool.Call(ctx, json.RawMessage(input), nil)
	if err != nil {
		t.Fatalf("工具 Call 返回 error: %v", err)
	}
	return res
}

func TestVPSMachineInfoTool(t *testing.T) {
	t.Run("VPS中心未初始化时报错而非panic", func(t *testing.T) {
		withVPSManager(t, nil)
		s := &Server{}
		res := vpsToolRun(t, s.toolVPSMachineInfo(), 7, `{}`)
		if !res.IsError || !strings.Contains(res.Flatten(), "VPS 中心未初始化") {
			t.Fatalf("期望明确的未初始化错误: %q", res.Flatten())
		}
	})

	t.Run("无任务会话报错", func(t *testing.T) {
		withVPSManager(t, vpscenter.NewManager(&fakeVPSStore{}, slog.Default()))
		s := &Server{}
		res := vpsToolRun(t, s.toolVPSMachineInfo(), 0, `{}`)
		if !res.IsError || !strings.Contains(res.Flatten(), "任务") {
			t.Fatalf("期望任务会话错误: %q", res.Flatten())
		}
	})

	t.Run("未指派机器时明确告知本机执行", func(t *testing.T) {
		withVPSManager(t, vpscenter.NewManager(&fakeVPSStore{byTask: map[int64]*vpscenter.Machine{}}, slog.Default()))
		s := &Server{}
		res := vpsToolRun(t, s.toolVPSMachineInfo(), 7, `{}`)
		if !res.IsError || !strings.Contains(res.Flatten(), "未指派") {
			t.Fatalf("期望未指派提示: %q", res.Flatten())
		}
	})

	t.Run("指派机器返回档案JSON", func(t *testing.T) {
		store := &fakeVPSStore{byTask: map[int64]*vpscenter.Machine{
			7: {ID: 3, Name: "hunter-01", Host: "10.0.0.8", Port: 22, Status: "busy",
				OsName: "Ubuntu", Arch: "x86_64", WorkspaceBase: "/srv/artex", Tags: []string{"gpu"}},
		}}
		withVPSManager(t, vpscenter.NewManager(store, slog.Default()))
		s := &Server{} // 无 PG:工具清单走「尚未检测」守护路径
		res := vpsToolRun(t, s.toolVPSMachineInfo(), 7, `{}`)
		if res.IsError {
			t.Fatalf("不应报错: %q", res.Flatten())
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(res.Flatten()), &doc); err != nil {
			t.Fatalf("输出应为 JSON: %v\n%s", err, res.Flatten())
		}
		if doc["name"] != "hunter-01" || doc["host"] != "10.0.0.8" {
			t.Fatalf("档案缺机器标识: %v", doc)
		}
		if doc["workspace"] != "/srv/artex/task_7" {
			t.Fatalf("workspace 应为任务目录, got %v", doc["workspace"])
		}
		if _, ok := doc["installed_tools"].([]any); !ok {
			t.Fatalf("installed_tools 必须是数组(平台铁律:集合不能是 null): %v", doc["installed_tools"])
		}
		if _, ok := doc["known_missing_tools"].([]any); !ok {
			t.Fatalf("known_missing_tools 必须是数组: %v", doc["known_missing_tools"])
		}
	})
}

// ---- 8. vps_install_toolset 工具 ----

func TestVPSInstallToolsetTool(t *testing.T) {
	t.Run("name必填", func(t *testing.T) {
		withVPSManager(t, nil)
		s := &Server{}
		res := vpsToolRun(t, s.toolVPSInstallToolset(), 7, `{"name":" "}`)
		if !res.IsError || !strings.Contains(res.Flatten(), "name 不能为空") {
			t.Fatalf("期望 name 必填错误: %q", res.Flatten())
		}
	})

	t.Run("数据库未就绪时明确报错", func(t *testing.T) {
		store := &fakeVPSStore{byTask: map[int64]*vpscenter.Machine{
			7: {ID: 3, Name: "hunter-01", Host: "10.0.0.8", Port: 22},
		}}
		withVPSManager(t, vpscenter.NewManager(store, slog.Default()))
		s := &Server{} // s.m == nil → 工具集查询不可用
		res := vpsToolRun(t, s.toolVPSInstallToolset(), 7, `{"name":"基础侦察"}`)
		if !res.IsError || !strings.Contains(res.Flatten(), "数据库未初始化") {
			t.Fatalf("期望数据库未就绪错误: %q", res.Flatten())
		}
	})

	t.Run("schema要求name参数", func(t *testing.T) {
		s := &Server{}
		schema := s.toolVPSInstallToolset().InputSchema()
		raw, err := json.Marshal(schema)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"name"`) {
			t.Fatalf("schema 应含 name 参数: %s", raw)
		}
	})
}

// ---- 9. 工具注册面 ----

func TestVPSAgentToolSurface(t *testing.T) {
	s := &Server{}
	tools := s.vpsAgentTools()
	if len(tools) != 2 {
		t.Fatalf("应注册两个 VPS agent 工具, got %d", len(tools))
	}
	wantNames := map[string]bool{"vps_machine_info": false, "vps_install_toolset": false}
	for _, tl := range tools {
		if _, ok := wantNames[tl.Name()]; !ok {
			t.Fatalf("意外工具: %s", tl.Name())
		}
		wantNames[tl.Name()] = true
	}
	for name, seen := range wantNames {
		if !seen {
			t.Fatalf("缺少工具: %s", name)
		}
	}
}

// TestVPSBashToolKeepsLLMFacingSurface 验证 SSH 版 Bash 与 SDK 本机 Bash 同名、
// 同描述、同 schema——LLM 视角不变,只有执行位置变了(契约 §6)。
func TestVPSBashToolKeepsLLMFacingSurface(t *testing.T) {
	s := &Server{}
	machine := &vpscenter.Machine{ID: 3, Name: "hunter-01", Host: "10.0.0.8", Port: 22}
	ssh := s.vpsBashTool(7, machine)
	ref := actool.NewBash()
	if ssh.Name() != ref.Name() {
		t.Fatalf("工具名必须与 SDK Bash 一致: %s vs %s", ssh.Name(), ref.Name())
	}
	if ssh.Description() != ref.Description() {
		t.Fatalf("描述必须与 SDK Bash 一致:\n%s\n---\n%s", ssh.Description(), ref.Description())
	}
	if !reflect.DeepEqual(ssh.InputSchema(), ref.InputSchema()) {
		t.Fatalf("InputSchema 必须与 SDK Bash 一致:\n%v\n---\n%v", ssh.InputSchema(), ref.InputSchema())
	}
}
