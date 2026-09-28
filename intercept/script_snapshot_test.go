package intercept

// 脚本快照提取的纯函数单测:解释器参数识别、cd/工作目录基准解析、缺文件降级、
// 内联代码(-c)跳过、数量上限。不打 DB、不碰网络。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func scriptTestInput(command string) []byte {
	b, _ := json.Marshal(map[string]string{"command": command})
	return b
}

func TestCaptureScriptFilesResolvesViaCD(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "smtp_sender.py"), []byte("import smtplib\nprint('x')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := `cd "` + dir + `"; python smtp_sender.py 2>&1 | head -30`
	files := captureScriptFiles(WithReviewWorkingDirectory(context.Background(), t.TempDir()), scriptTestInput(command))
	if len(files) != 1 {
		t.Fatalf("应快照 1 个脚本, got %d: %+v", len(files), files)
	}
	f := files[0]
	if f.Missing || !strings.Contains(f.Content, "import smtplib") {
		t.Fatalf("快照内容不符: %+v", f)
	}
	if !strings.HasSuffix(filepath.Clean(f.Path), filepath.Clean(filepath.Join(dir, "smtp_sender.py"))) {
		t.Fatalf("快照路径应解析到 cd 目录: %s", f.Path)
	}
}

func TestCaptureScriptFilesResolvesViaWorkingDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "scan.sh"), []byte("#!/bin/sh\necho hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := captureScriptFiles(WithReviewWorkingDirectory(context.Background(), dir), scriptTestInput(`bash scan.sh`))
	if len(files) != 1 || files[0].Missing || !strings.Contains(files[0].Content, "echo hi") {
		t.Fatalf("工作目录基准解析失败: %+v", files)
	}
}

func TestCaptureScriptFilesMissingFileMarked(t *testing.T) {
	dir := t.TempDir()
	files := captureScriptFiles(WithReviewWorkingDirectory(context.Background(), dir), scriptTestInput(`python ghost_script.py`))
	if len(files) != 1 || !files[0].Missing || files[0].Content != "" {
		t.Fatalf("缺文件应记 Missing: %+v", files)
	}
}

func TestCaptureScriptFilesSkipsNonScripts(t *testing.T) {
	ctx := WithReviewWorkingDirectory(context.Background(), t.TempDir())
	if got := captureScriptFiles(ctx, scriptTestInput(`ls -la; cat notes.txt`)); got != nil {
		t.Fatalf("无脚本引用应返回 nil: %+v", got)
	}
	// -c/-m 内联代码不是文件引用。
	if got := captureScriptFiles(ctx, scriptTestInput(`python -c "print(1)"`)); got != nil {
		t.Fatalf("内联代码不应快照: %+v", got)
	}
	// 非 command 输入(Write 工具等)直接跳过。
	if got := captureScriptFiles(ctx, []byte(`{"file_text":"x.py"}`)); got != nil {
		t.Fatalf("非命令输入不应快照: %+v", got)
	}
}

func TestCaptureScriptFilesCapsAndDedupes(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.py", "b.py", "c.py", "d.py"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("pass\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	command := `cd "` + dir + `" && python a.py; python b.py; python a.py; python c.py; python d.py`
	files := captureScriptFiles(WithReviewWorkingDirectory(context.Background(), t.TempDir()), scriptTestInput(command))
	if len(files) > scriptSnapshotMaxFiles {
		t.Fatalf("应封顶 %d 个, got %d", scriptSnapshotMaxFiles, len(files))
	}
	paths := map[string]bool{}
	for _, f := range files {
		paths[f.Path] = true
	}
	if len(paths) != len(files) {
		t.Fatalf("同一文件不应重复快照: %+v", files)
	}
}

func TestCaptureScriptFilesBashCInlineCodeNotSnapshotted(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "inner.py"), []byte("print('inner')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// bash -c 的引号参数是内联代码而非文件:外层匹配吞掉内层引用,模糊候选解析不到
	// 真实文件时应静默跳过(已知的表达力边界,嵌套内联不递归——命令行本身仍可见)。
	command := `cd "` + dir + `" && bash -c "python inner.py"`
	files := captureScriptFiles(WithReviewWorkingDirectory(context.Background(), t.TempDir()), scriptTestInput(command))
	if len(files) != 0 {
		t.Fatalf("内联代码不应产生快照或 Missing 记录: %+v", files)
	}
	// 带空格的真实文件路径(引号包裹)应照常快照。
	sub := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sub, "my tools"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "my tools", "spaced.py"), []byte("print('spaced')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spaced := `cd "` + sub + `" && python "my tools/spaced.py"`
	files = captureScriptFiles(WithReviewWorkingDirectory(context.Background(), t.TempDir()), scriptTestInput(spaced))
	if len(files) != 1 || files[0].Missing || !strings.Contains(files[0].Content, "spaced") {
		t.Fatalf("带空格的真实脚本应被快照: %+v", files)
	}
}
