package server

// 插件参数渲染与注入防护单测(纯函数,不依赖 PG)。覆盖:
//   - 占位渲染:{{name}} / {{name:默认}} / {{name:选1|选2}} / {{content}} / {{file}};
//   - 白名单:未声明键、无占位键、保留名注入、必填缺失、非法键名一律拒绝;
//   - 注入防护:换行/元字符/引号/反引号消毒、enum 选项锁、bool 归一、超长拒绝;
//   - binary 链路:远端路径构造、after_clean 尾缀、{{file}} 注入。

import (
	"encoding/json"
	"strings"
	"testing"

	pgdb "github.com/Autumn-27/artex/db"
)

func mustRender(t *testing.T, entry string, schema json.RawMessage, params, reserved map[string]string) (string, map[string]string) {
	t.Helper()
	cmd, effective, err := renderC2PluginCommand(entry, schema, params, reserved)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	return cmd, effective
}

// TestRenderC2PluginCommandBasics 基础渲染:用户值 / 声明默认 / 内联默认三级取值。
func TestRenderC2PluginCommandBasics(t *testing.T) {
	schema := json.RawMessage(`{
		"host":{"type":"string","desc":"目标"},
		"sort":{"type":"enum","options":["cpu","mem"],"default":"cpu"},
		"count":{"type":"string","default":"25"}
	}`)
	// 用户值命中 + enum/schema 默认生效。
	cmd, eff := mustRender(t,
		"ps aux | grep {{host}} | sort -n -k{{sort}} | head -{{count}}",
		schema,
		map[string]string{"host": "nginx"},
		nil)
	want := "ps aux | grep nginx | sort -n -kcpu | head -25"
	if cmd != want {
		t.Fatalf("渲染结果不对:\n got %q\nwant %q", cmd, want)
	}
	if eff["host"] != "nginx" || eff["sort"] != "cpu" || eff["count"] != "25" {
		t.Fatalf("生效参数表不对: %v", eff)
	}
	// 内联声明 {{x:选1|选2}}:默认取首项,用户值必须命中选项。
	cmd, _ = mustRender(t, "verbose={{verbose:0|1}}", json.RawMessage(`{}`), nil, nil)
	if cmd != "verbose=0" {
		t.Fatalf("内联默认应生效: %q", cmd)
	}
	if _, _, err := renderC2PluginCommand("verbose={{verbose:0|1}}", json.RawMessage(`{}`),
		map[string]string{"verbose": "2"}, nil); err == nil {
		t.Fatal("内联选项之外的取值应被拒绝")
	}
	// {{content}} 保留占位:引用信任正文;正文为空报错。
	cmd, _ = mustRender(t, `python3 -c "{{content}}"`, json.RawMessage(`{}`), nil,
		map[string]string{"content": "print(1)"})
	if cmd != `python3 -c "print(1)"` {
		t.Fatalf("content 注入不对: %q", cmd)
	}
	if _, _, err := renderC2PluginCommand(`{{content}}`, json.RawMessage(`{}`), nil,
		map[string]string{"content": ""}); err == nil {
		t.Fatal("引用空 content 应报错")
	}
}

// TestRenderC2PluginCommandInjection 恶意值转义(fail-closed:消毒 + 选项锁)。
func TestRenderC2PluginCommandInjection(t *testing.T) {
	schema := json.RawMessage(`{"host":{"type":"string"}}`)
	cases := []struct {
		name string
		in   string
	}{
		{"分号拼命令", "127.0.0.1; shutdown -h now"},
		{"管道", "x|cat /etc/passwd"},
		{"后台与重定向", "a&whoami>out"},
		{"换行多命令", "a\nreboot\n"},
		{"回车", "a\r restart"},
		{"反引号", "`id`"},
		{"命令替换", "$(id)"},
		{"引号逃逸", `"'; rm -rf /;'`},
		{"占位符重生", "{{host}};{{host}}"},
		{"NUL", "a\x00b"},
	}
	for _, c := range cases {
		cmd, _, err := renderC2PluginCommand("ping {{host}}", schema,
			map[string]string{"host": c.in}, nil)
		if err != nil {
			t.Fatalf("[%s] 消毒不应报错: %v", c.name, err)
		}
		for _, bad := range []string{";", "|", "&", "$", "`", "\"", "'", "\n", "\r", "(", ")", "<", ">", "{", "}"} {
			if strings.Contains(cmd, bad) {
				t.Fatalf("[%s] 消毒后仍含元字符 %q: %q", c.name, bad, cmd)
			}
		}
		if !strings.HasPrefix(cmd, "ping ") {
			t.Fatalf("[%s] 值应只落在占位处: %q", c.name, cmd)
		}
	}
	// 超长拒绝。
	long := strings.Repeat("a", c2ParamValueMax+1)
	if _, _, err := renderC2PluginCommand("ping {{host}}", schema,
		map[string]string{"host": long}, nil); err == nil {
		t.Fatal("超长参数应被拒绝")
	}
	// 消毒保 Windows 路径(反斜杠合法取值)。
	cmd, _ := mustRender(t, "dir {{p}}", json.RawMessage(`{"p":{"type":"string"}}`),
		map[string]string{"p": `C:\Users\Public`}, nil)
	if cmd != `dir C:\Users\Public` {
		t.Fatalf("反斜杠路径应保留: %q", cmd)
	}
}

// TestRenderC2PluginCommandWhitelist 白名单 fail-closed:未声明/无占位/保留名/
// 必填缺失/非法键名/bool 与 enum 越界一律拒绝,绝不静默丢弃。
func TestRenderC2PluginCommandWhitelist(t *testing.T) {
	schema := json.RawMessage(`{
		"host":{"type":"string"},
		"flag":{"type":"bool","default":"0"},
		"mode":{"type":"enum","options":["a","b"]}
	}`)
	renderErr := func(entry string, params, reserved map[string]string) error {
		_, _, err := renderC2PluginCommand(entry, schema, params, reserved)
		return err
	}
	if err := renderErr("ping {{host}}", map[string]string{"evil": "x"}, nil); err == nil {
		t.Fatal("未声明参数应拒绝")
	}
	if err := renderErr("ping", map[string]string{"host": "x"}, nil); err == nil {
		t.Fatal("模板无占位的参数应拒绝")
	}
	if err := renderErr("run {{file}}", map[string]string{"file": "/etc/x"}, nil); err == nil {
		t.Fatal("保留名 file 注入应拒绝")
	}
	if err := renderErr("{{content}}", map[string]string{"content": "x"}, nil); err == nil {
		t.Fatal("保留名 content 注入应拒绝")
	}
	if err := renderErr("run {{after_clean}}", map[string]string{}, nil); err == nil {
		t.Fatal("binary 形态缺失 file 保留值应报错")
	}
	if err := renderErr("mode={{mode}}", map[string]string{}, nil); err == nil {
		t.Fatal("无任何默认的必填参数缺失应报错")
	}
	if err := renderErr("mode={{mode:a|b}}", map[string]string{"mode": "c"}, nil); err == nil {
		t.Fatal("enum 越界应拒绝(内联选项)")
	}
	if err := renderErr("mode={{mode}}", map[string]string{"mode": "c"}, nil); err == nil {
		t.Fatal("enum 越界应拒绝(schema 选项)")
	}
	if err := renderErr("f={{flag}}", map[string]string{"flag": "maybe"}, nil); err == nil {
		t.Fatal("bool 非法取值应拒绝")
	}
	if err := renderErr("ping {{host}}", map[string]string{"bad-key!": "x"}, nil); err == nil {
		t.Fatal("非法键名应拒绝")
	}
	// bool 归一与 after_clean 控制参数(声明后可传、不进命令)。
	_, eff := mustRender(t, "f={{flag}}", schema, map[string]string{"flag": "true"}, nil)
	if eff["flag"] != "1" {
		t.Fatalf("bool 应归一为 1/0: %v", eff)
	}
	cleanSchema := json.RawMessage(`{"after_clean":{"type":"bool","default":"1"}}`)
	_, eff = mustRender(t, "run {{file}}", cleanSchema, nil, map[string]string{"file": "/tmp/a"})
	if eff["after_clean"] != "1" {
		t.Fatalf("after_clean 声明默认应生效: %v", eff)
	}
	if err := renderErr("run {{file}}", map[string]string{"after_clean": "1"}, nil); err == nil {
		t.Fatal("after_clean 未在 schema 声明时传入应拒绝")
	}
}

// TestC2BuildPluginCommandBinary binary 链路:远端路径、after_clean 尾缀与平台差异。
func TestC2BuildPluginCommandBinary(t *testing.T) {
	p := &pgdb.C2Plugin{
		ID: 7, Name: "端口扫描工具", Kind: pgdb.C2PluginKindBinary, Language: pgdb.C2PluginLanguageSh,
		Entry:    "chmod +x {{file}} && {{file}} --help",
		Content:  "#!/bin/sh\necho demo\n",
		BlobPath: "", ParamsSchema: json.RawMessage(`{"after_clean":{"type":"bool","default":"1"}}`),
	}
	sessionWin := &pgdb.C2Session{OS: "Windows 11 Pro"}
	sessionLin := &pgdb.C2Session{OS: "linux"}

	remote := c2PluginRemotePath(sessionWin.OS, p)
	if !strings.HasPrefix(remote, `C:\Users\Public\artex_pl_7_`) {
		t.Fatalf("windows 远端路径前缀不对: %q", remote)
	}
	if !strings.HasPrefix(c2PluginRemotePath(sessionLin.OS, p), "/tmp/artex_pl_7_") {
		t.Fatalf("posix 远端路径前缀不对: %q", c2PluginRemotePath(sessionLin.OS, p))
	}
	// 文件名消毒:插件名里的空格/中文/路径片段归一,不允许目录分隔或 ".."。
	stem := c2PluginFileStem("../../etc passwd 端口扫描")
	if strings.ContainsAny(stem, "/\\ ") || strings.Contains(stem, "..") {
		t.Fatalf("文件名段消毒不彻底: %q", stem)
	}

	cmd, eff, err := c2BuildPluginCommand(sessionWin, p, map[string]string{"after_clean": "1"})
	if err != nil {
		t.Fatalf("binary 渲染失败: %v", err)
	}
	if !strings.Contains(cmd, remote) || !strings.Contains(cmd, "chmod +x") {
		t.Fatalf("binary 命令应含 {{file}} 注入: %q", cmd)
	}
	if !strings.Contains(cmd, "Remove-Item -LiteralPath '"+remote+"'") {
		t.Fatalf("windows 清理尾缀缺失: %q", cmd)
	}
	if eff["after_clean"] != "1" {
		t.Fatalf("after_clean 生效值不对: %v", eff)
	}
	cmdLin, _, err := c2BuildPluginCommand(sessionLin, p, nil)
	if err != nil {
		t.Fatalf("binary 渲染失败: %v", err)
	}
	if !strings.Contains(cmdLin, "; rm -f '/tmp/") {
		t.Fatalf("posix 清理尾缀缺失: %q", cmdLin)
	}
	// c2AfterCleanEnabled:未声明 after_clean 的 schema 永不清理。
	if c2AfterCleanEnabled(json.RawMessage(`{}`), map[string]string{"after_clean": "1"}) {
		t.Fatal("未声明 after_clean 不应清理")
	}
	// 内置种子插件整体自检:渲染烟测(每个内置插件按声明默认值都能渲染出命令)。
	for _, seed := range pgdb.BuiltinC2Plugins() {
		row := &pgdb.C2Plugin{
			ID: 1, Name: seed.Name, Kind: seed.Kind, Language: seed.Language,
			Entry: seed.Entry, Content: seed.Content, ParamsSchema: seed.ParamsSchema,
		}
		sess := &pgdb.C2Session{OS: "windows"}
		if seed.Platform != pgdb.C2PluginPlatformWindows {
			sess = &pgdb.C2Session{OS: "linux"}
		}
		if _, _, err := c2BuildPluginCommand(sess, row, nil); err != nil {
			t.Fatalf("内置插件 «%s» 默认参数渲染失败: %v", seed.Name, err)
		}
	}
}
