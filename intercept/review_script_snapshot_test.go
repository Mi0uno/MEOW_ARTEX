package intercept

// 审查输入 v5 的脚本快照注入单测:BuildReviewInput 把命令引用的脚本内容带进
// ReviewInput.ScriptFiles,LLM 裁判据此核实脚本真实行为;未引用脚本时字段省略,
// 引用了但读不到时条目带 missing。不打 DB、不碰网络。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewInputCarriesScriptSnapshot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "probe.py"), []byte("import requests\nprint('probe')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := WithReviewWorkingDirectory(t.Context(), dir)
	args := json.RawMessage(`{"command":"python probe.py 2>&1"}`)
	in, err := BuildReviewInput(ctx, "Bash", args)
	if err != nil {
		t.Fatal(err)
	}
	if in.Version != 5 || len(in.ScriptFiles) != 1 {
		t.Fatalf("v5 输入应携带 1 段脚本快照: %+v", in)
	}
	f := in.ScriptFiles[0]
	if f.Missing || !strings.Contains(f.Content, "import requests") {
		t.Fatalf("快照内容不符: %+v", f)
	}
	// 序列化后字段名契约:reviewCompletion 整体 marshal 输入,字段名即模型所见。
	raw, _ := json.Marshal(in)
	var wire struct {
		ScriptFiles []struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		} `json:"script_files"`
	}
	if json.Unmarshal(raw, &wire) != nil || len(wire.ScriptFiles) != 1 || wire.ScriptFiles[0].Content == "" {
		t.Fatalf("script_files 未按契约序列化: %s", raw)
	}
}

func TestReviewInputOmitsScriptFieldWhenNoScript(t *testing.T) {
	ctx := WithReviewWorkingDirectory(t.Context(), t.TempDir())
	in, err := BuildReviewInput(ctx, "Bash", json.RawMessage(`{"command":"ls -la"}`))
	if err != nil {
		t.Fatal(err)
	}
	if in.ScriptFiles != nil {
		t.Fatalf("未引用脚本不应有 script_files: %+v", in.ScriptFiles)
	}
	raw, _ := json.Marshal(in)
	if strings.Contains(string(raw), "script_files") {
		t.Fatalf("script_files 应被 omitempty 省略: %s", raw)
	}
}
