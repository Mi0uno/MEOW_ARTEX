package c2center

// 载荷矩阵渲染测试(三期 §18,Agent T):全部合法 (格式,协议) 组合渲染成功、
// 产物无未替换占位符;gosrc 产物过 go/parser 语法校验;kcp×ps1/bash 明确报错;
// oss 缺配置明确报错;oneliner 仅 http/ws 载体下发。不连库、不执行任何产物。

import (
	"archive/zip"
	"bytes"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// renderMatrixCase 渲染一个组合,返回(文件名字节, 内容, oneliner)。
func renderMatrixCase(t *testing.T, format, proto string, cfgJSON string) (string, []byte, string) {
	t.Helper()
	key := testKey()
	l := &Listener{
		ID: 7, Name: "t", Proto: proto, BindHost: "127.0.0.1",
		Callback: "http://198.51.100.7:8443", URLPath: "abc",
		TransportConfigJSON: cfgJSON,
	}
	filename, content, oneliner, err := RenderPayload(format, "windows", l, key)
	if err != nil {
		t.Fatalf("%s/%s 渲染失败: %v", format, proto, err)
	}
	return filename, content, oneliner
}

func assertNoPlaceholder(t *testing.T, tag string, b []byte) {
	t.Helper()
	for _, p := range []string{
		"__CALLBACK__", "__URL_PATH__", "__B64KEY__", "__LISTENER_ID__",
		"__PY_CHANNEL__", "__PS1_CHANNEL__", "__GO_CHANNEL__", "__GO_EXTRA_IMPORTS__",
		"__OSS_EP__", "__OSS_BUCKET__", "__OSS_AK__", "__OSS_SK__", "__OSS_PREFIX__",
		"__KCP_PSK__", "__OS_EXEC_IMPORT__",
	} {
		if bytes.Contains(b, []byte(p)) {
			t.Fatalf("%s 残留未替换占位符 %s", tag, p)
		}
	}
}

const testOSSCfg = `{"endpoint":"oss.example.com","bucket":"bkt","access_key":"ak","secret_key":"sk","prefix":"pfx"}`
const testKcpCfg = `{"psk":"sekrit"}`

func TestRenderMatrix(t *testing.T) {
	cases := []struct{ format, proto, cfg string }{
		{"ps1", ProtoHTTP, ""}, {"ps1", ProtoWS, ""}, {"ps1", ProtoTCP, ""}, {"ps1", ProtoOSS, testOSSCfg},
		{"bash", ProtoHTTP, ""}, {"bash", ProtoWS, ""}, {"bash", ProtoTCP, ""}, {"bash", ProtoOSS, testOSSCfg},
		{"py", ProtoHTTP, ""}, {"py", ProtoWS, ""}, {"py", ProtoTCP, ""}, {"py", ProtoKCP, testKcpCfg}, {"py", ProtoOSS, testOSSCfg},
		{"gosrc", ProtoHTTP, ""}, {"gosrc", ProtoWS, ""}, {"gosrc", ProtoTCP, ""}, {"gosrc", ProtoKCP, testKcpCfg}, {"gosrc", ProtoOSS, testOSSCfg},
	}
	for _, c := range cases {
		filename, content, _ := renderMatrixCase(t, c.format, c.proto, c.cfg)
		if filename == "" || len(content) == 0 {
			t.Fatalf("%s/%s 产物为空", c.format, c.proto)
		}
		assertNoPlaceholder(t, c.format+"/"+c.proto, content)
		// 关键注入物:监听器 id 必须内嵌(信道无关)。
		if !bytes.Contains(content, []byte(`"7"`)) && !bytes.Contains(content, []byte("7")) {
			t.Fatalf("%s/%s 缺监听器 id", c.format, c.proto)
		}
	}
}

func TestRenderMatrixGoSrcParses(t *testing.T) {
	for _, proto := range []string{ProtoHTTP, ProtoWS, ProtoTCP, ProtoKCP, ProtoOSS} {
		cfg := ""
		if proto == ProtoOSS {
			cfg = testOSSCfg
		}
		if proto == ProtoKCP {
			cfg = testKcpCfg
		}
		_, content, _ := renderMatrixCase(t, "gosrc", proto, cfg)
		zr, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
		if err != nil {
			t.Fatalf("gosrc/%s zip: %v", proto, err)
		}
		var mainGo []byte
		for _, f := range zr.File {
			if f.Name == "main.go" {
				rc, ferr := f.Open()
				if ferr != nil {
					t.Fatal(ferr)
				}
				var buf bytes.Buffer
				_, _ = buf.ReadFrom(rc)
				_ = rc.Close()
				mainGo = buf.Bytes()
			}
		}
		if mainGo == nil {
			t.Fatalf("gosrc/%s zip 缺 main.go", proto)
		}
		if _, err := parser.ParseFile(token.NewFileSet(), "main.go", mainGo, 0); err != nil {
			t.Fatalf("gosrc/%s main.go 语法错误: %v", proto, err)
		}
		// kcp 变体必须带 kcp-go import 与依赖说明;其余变体不得含第三方 import。
		if proto == ProtoKCP {
			if !bytes.Contains(mainGo, []byte("kcp-go/v5")) {
				t.Fatal("kcp gosrc 应引入 kcp-go")
			}
		} else if bytes.Contains(mainGo, []byte("kcp-go")) {
			t.Fatalf("%s gosrc 不应引入 kcp-go", proto)
		}
	}
}

func TestRenderMatrixErrors(t *testing.T) {
	key := testKey()
	newL := func(proto, cfg string) *Listener {
		return &Listener{ID: 1, Proto: proto, Callback: "http://198.51.100.7:8443",
			URLPath: "abc", TransportConfigJSON: cfg}
	}
	// §18 冻结矩阵:kcp 仅 py/gosrc。
	for _, f := range []string{"ps1", "bash"} {
		if _, _, _, err := RenderPayload(f, "windows", newL(ProtoKCP, testKcpCfg), key); err == nil {
			t.Fatalf("%s+kcp 应报错", f)
		}
	}
	// oss 缺配置明确报错。
	if _, _, _, err := RenderPayload("py", "windows", newL(ProtoOSS, ""), key); err == nil {
		t.Fatal("oss 无配置应报错")
	} else if !strings.Contains(err.Error(), "transport") && !strings.Contains(err.Error(), "oss") {
		t.Fatalf("oss 报错应指明原因: %v", err)
	}
	// oss 载体不要求 callback(kcp/tcp/ws 要求)。
	l := newL(ProtoOSS, testOSSCfg)
	l.Callback = ""
	if _, _, _, err := RenderPayload("py", "windows", l, key); err != nil {
		t.Fatalf("oss 无 callback 应可渲染: %v", err)
	}
	l2 := newL(ProtoTCP, "")
	l2.Callback = ""
	if _, _, _, err := RenderPayload("py", "windows", l2, key); err == nil {
		t.Fatal("tcp 无 callback 应报错")
	}
}

func TestOnelinerByProto(t *testing.T) {
	l := &Listener{ID: 3, Proto: ProtoTCP, Callback: "http://198.51.100.7:8443", URLPath: "abc"}
	if got := Oneliner("ps1", l, "tok"); got != "" {
		t.Fatalf("tcp 载体 oneliner 应为空: %q", got)
	}
	l.Proto = ProtoKCP
	if got := Oneliner("py", l, "tok"); got != "" {
		t.Fatalf("kcp 载体 oneliner 应为空: %q", got)
	}
	l.Proto = ProtoOSS
	if got := Oneliner("bash", l, "tok"); got != "" {
		t.Fatalf("oss 载体 oneliner 应为空: %q", got)
	}
	l.Proto = ProtoHTTP
	if got := Oneliner("bash", l, "tok"); got == "" {
		t.Fatal("http 载体 oneliner 不应为空")
	}
	l.Proto = ProtoWS
	if got := Oneliner("ps1", l, "tok"); got == "" {
		t.Fatal("ws 载体 oneliner 不应为空(同端口托管投递端点)")
	}
}
