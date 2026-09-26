package server

// 资产识别纯函数单测(契约 ASSET_EXTRACT_CONTRACTS.md §3.2):
// 覆盖 URL/host:port/IPv4/裸 hostname 提取、email 右半不误提、loopback/0.0.0.0/
// 广播丢弃、内网 IP 保留、run 内 (type,value,port) 去重、已有资产值比对跳过、
// LLM 输出围栏解析与净化、missing 净化。不连数据库、不调 LLM。

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func assetSeg(kind, text string) assetTextSegment {
	return assetTextSegment{Kind: kind, Text: text}
}

func findCandidate(cands []assetCandidate, typ, value string, port int) *assetCandidate {
	for i := range cands {
		c := &cands[i]
		if c.Type == typ && c.Value == value && c.Port == port {
			return c
		}
	}
	return nil
}

// TestAssetExtractURL URL → service 候选:完整 URL 进候选字段,尾部句读剥离,
// 端口缺省为 0,URL 内部 host 不重复产 domain。
func TestAssetExtractURL(t *testing.T) {
	cands := extractAssetCandidates([]assetTextSegment{
		assetSeg("tool_use", `入口 http://Admin.Example.com:8443/login?next=/a] 备用 https://portal.demo.org。`),
	})
	c := findCandidate(cands, "service", "admin.example.com", 8443)
	if c == nil {
		t.Fatalf("应提取出 http service 候选,得到 %+v", cands)
	}
	if !strings.HasPrefix(c.URL, "http://Admin.Example.com:8443/login?next=/a") || strings.HasSuffix(c.URL, "]") {
		t.Fatalf("URL 应保留原样并剥尾部句读,得到 %q", c.URL)
	}
	if c2 := findCandidate(cands, "service", "portal.demo.org", 0); c2 == nil || c2.URL != "https://portal.demo.org" {
		t.Fatalf("无端口 URL 应提取 port=0 且剥掉 CJK 句读,得到 %+v", cands)
	}
	for _, c := range cands {
		if c.Type == "domain" && (c.Value == "admin.example.com" || c.Value == "portal.demo.org") {
			t.Fatalf("URL 命中处应被掩蔽,不得重复产出 domain 候选: %+v", cands)
		}
	}
}

// TestAssetExtractHostPort 域名 host:port → service(无 URL);IP host:port → ip。
func TestAssetExtractHostPort(t *testing.T) {
	cands := extractAssetCandidates([]assetTextSegment{
		assetSeg("tool_use", "数据库在 db.internal.corp:5432 上,跳板 10.0.0.5:8080 可用"),
	})
	if findCandidate(cands, "service", "db.internal.corp", 5432) == nil {
		t.Fatalf("应提取域名 host:port 为 service 候选,得到 %+v", cands)
	}
	if findCandidate(cands, "ip", "10.0.0.5", 8080) == nil {
		t.Fatalf("应提取 IP host:port 为 ip 候选,得到 %+v", cands)
	}
	for _, c := range cands {
		if c.Type == "domain" && c.Value == "db.internal.corp" {
			t.Fatalf("host:port 命中处应被掩蔽: %+v", cands)
		}
	}
}

// TestAssetExtractIPv4 裸 IPv4(内网保留);非法 octet 丢弃。
func TestAssetExtractIPv4(t *testing.T) {
	cands := extractAssetCandidates([]assetTextSegment{
		assetSeg("finding", "目标内网 10.0.0.5 与 172.16.3.4;非法 999.1.1.1 忽略"),
	})
	if findCandidate(cands, "ip", "10.0.0.5", 0) == nil {
		t.Fatalf("内网 IP 10.0.0.5 应保留,得到 %+v", cands)
	}
	if findCandidate(cands, "ip", "172.16.3.4", 0) == nil {
		t.Fatalf("内网 IP 172.16.3.4 应保留,得到 %+v", cands)
	}
	if findCandidate(cands, "ip", "999.1.1.1", 0) != nil {
		t.Fatalf("octet>255 应被 ParseIP 校验丢弃,得到 %+v", cands)
	}
}

// TestAssetExtractHostname 裸 hostname → domain。
func TestAssetExtractHostname(t *testing.T) {
	cands := extractAssetCandidates([]assetTextSegment{
		assetSeg("tool_use", "节点 api.example-v2.org 已上线,备用 beta.test.example"),
	})
	if findCandidate(cands, "domain", "api.example-v2.org", 0) == nil {
		t.Fatalf("应提取裸 hostname 为 domain 候选,得到 %+v", cands)
	}
	if findCandidate(cands, "domain", "beta.test.example", 0) == nil {
		t.Fatalf("多级 hostname 应提取,得到 %+v", cands)
	}
}

// TestAssetExtractEmailNotHost email 先删:右半域名不得被误提(契约 §3.2.2)。
func TestAssetExtractEmailNotHost(t *testing.T) {
	cands := extractAssetCandidates([]assetTextSegment{
		assetSeg("tool_use", "联系 admin@corp.example.com 或 sec-team@internal.corp 获取授权"),
	})
	for _, c := range cands {
		if c.Value == "corp.example.com" || c.Value == "internal.corp" || c.Value == "example.com" {
			t.Fatalf("email 右半不得被提取为域名: %+v", cands)
		}
	}
	if len(cands) != 0 {
		t.Fatalf("纯 email 文本不应产出任何候选,得到 %+v", cands)
	}
}

// TestAssetExtractBlockedIPs loopback/0.0.0.0/定向广播丢弃,内网保留(契约 §9)。
func TestAssetExtractBlockedIPs(t *testing.T) {
	cands := extractAssetCandidates([]assetTextSegment{
		assetSeg("tool_use", "本机 127.0.0.1 通配 0.0.0.0 广播 255.255.255.255,业务 10.0.0.5 正常"),
	})
	for _, c := range cands {
		if c.Type == "ip" && (c.Value == "127.0.0.1" || c.Value == "0.0.0.0" || c.Value == "255.255.255.255") {
			t.Fatalf("loopback/0.0.0.0/广播应丢弃: %+v", cands)
		}
	}
	if findCandidate(cands, "ip", "10.0.0.5", 0) == nil {
		t.Fatalf("10.0.0.5 应保留,得到 %+v", cands)
	}
	if !isBlockedAssetIPv4("127.0.0.1") || !isBlockedAssetIPv4("0.0.0.0") || !isBlockedAssetIPv4("255.255.255.255") {
		t.Fatal("isBlockedAssetIPv4 应拦截 127.0.0.1 / 0.0.0.0 / 255.255.255.255")
	}
	if isBlockedAssetIPv4("10.0.0.5") || isBlockedAssetIPv4("192.168.1.1") {
		t.Fatal("内网 IP 不应被拦截")
	}
}

// TestAssetExtractDedup run 内按 (type,value,port) 去重(跨来源段亦去重)。
func TestAssetExtractDedup(t *testing.T) {
	cands := extractAssetCandidates([]assetTextSegment{
		assetSeg("tool_use", "10.0.0.9 探测 10.0.0.9:8080"),
		assetSeg("finding", "复核 10.0.0.9:8080 与 10.0.0.9"),
	})
	n := 0
	for _, c := range cands {
		if c.Type == "ip" && c.Value == "10.0.0.9" && c.Port == 8080 {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("10.0.0.9:8080 应跨来源段去重为 1 条,实得 %d(%+v)", n, cands)
	}
	if len(extractAssetCandidates([]assetTextSegment{assetSeg("tool_use", "10.0.0.9 10.0.0.9 10.0.0.9")})) != 1 {
		t.Fatal("同 (type,value,port) 应 run 内去重为 1 条")
	}
}

// TestAssetExtractEmptyInput 空输入不 panic、集合非 nil(红线:不得漏成 JSON null)。
func TestAssetExtractEmptyInput(t *testing.T) {
	cands := extractAssetCandidates(nil)
	if cands == nil {
		t.Fatal("返回集合不得为 nil")
	}
	if len(cands) != 0 {
		t.Fatalf("空输入应得空集合,得到 %+v", cands)
	}
}

// TestAssetSkipKeySetAndCandidateSkipped 已有资产值比对(契约 §3.2.4)。
func TestAssetSkipKeySetAndCandidateSkipped(t *testing.T) {
	port := 443
	assets := []*db.Asset{
		{Type: "subdomain", Domain: "www.example.com"},
		{Type: "ip", IP: "10.0.0.5"},
		{Type: "service", Domain: "app.example.com", Port: &port},
		{Type: "service", URL: "https://old.example.com/login", Domain: "old.example.com"},
	}
	keys := assetSkipKeySet(assets)
	if keys == nil {
		t.Fatal("值比对集合不得为 nil")
	}
	if !candidateSkipped(assetCandidate{Type: "domain", Value: "www.example.com"}, keys) {
		t.Fatal("已有子域名的 domain 候选应判 dup")
	}
	if !candidateSkipped(assetCandidate{Type: "ip", Value: "10.0.0.5", Port: 8080}, keys) {
		t.Fatal("已有 IP 的 ip 候选(任意端口)应判 dup")
	}
	if !candidateSkipped(assetCandidate{Type: "service", Value: "app.example.com", Port: 443}, keys) {
		t.Fatal("同 host:port 的 service 候选应判 dup")
	}
	if !candidateSkipped(assetCandidate{Type: "service", Value: "app.example.com", Port: 0, URL: "https://app.example.com/"}, keys) {
		t.Fatal("无端口 URL 在 host 已有 service 资产时应判 dup")
	}
	if !candidateSkipped(assetCandidate{Type: "service", Value: "old.example.com", Port: 0, URL: "https://old.example.com/login"}, keys) {
		t.Fatal("URL 原值已有的 service 候选应判 dup")
	}
	if candidateSkipped(assetCandidate{Type: "domain", Value: "new.example.com"}, keys) {
		t.Fatal("新域名不应判 dup")
	}
	if candidateSkipped(assetCandidate{Type: "ip", Value: "10.0.0.6"}, keys) {
		t.Fatal("新 IP 不应判 dup")
	}
	if candidateSkipped(assetCandidate{Type: "service", Value: "app.example.com", Port: 8443}, keys) {
		t.Fatal("同 host 不同显式端口的 service 候选不应判 dup")
	}
}

// TestMissingToCandidate LLM 新发现净化(契约 §3.3/§3.4.1)。
func TestMissingToCandidate(t *testing.T) {
	if c := missingToCandidate(assetExtractMissing{Type: "domain", Value: "Found.Example.com"}); c == nil || c.Value != "found.example.com" {
		t.Fatalf("domain 应归一小写保留,得到 %+v", c)
	}
	if missingToCandidate(assetExtractMissing{Type: "service", Value: "srv.example.com", Port: 0}) != nil {
		t.Fatal("service 无 URL 无端口应拒绝(UpsertOtherService 硬要求)")
	}
	if c := missingToCandidate(assetExtractMissing{Type: "service", Value: "10.0.0.9", Port: 3306}); c == nil || c.Type != "ip" {
		t.Fatalf("IP:port 无 URL 应归为 ip 类型,得到 %+v", c)
	}
	if missingToCandidate(assetExtractMissing{Type: "ip", Value: "127.0.0.1"}) != nil {
		t.Fatal("loopback 新发现应拒绝")
	}
	if c := missingToCandidate(assetExtractMissing{Type: "service", Value: "whatever", Port: 80, URL: "http://real.example.com/admin"}); c == nil || c.Value != "real.example.com" {
		t.Fatalf("带 URL 的 service 应以 URL host 为准,得到 %+v", c)
	}
	if missingToCandidate(assetExtractMissing{Type: "app", Value: "x"}) != nil {
		t.Fatal("类型白名单外的应拒绝")
	}
}

// TestParseAssetExtractJSON 围栏容错解析 + 净化(仿 parseMemoryReflectJSON)。
func TestParseAssetExtractJSON(t *testing.T) {
	raw := "```json\n" + `{"confirmed":[2,-1,2],"missing":[{"type":"domain","value":"X.Example.com"},{"type":"domain","value":""},{"type":"bad","value":"y"}],"note":"ok"}` + "\n```"
	parsed, err := parseAssetExtractJSON(raw)
	if err != nil {
		t.Fatalf("围栏 JSON 应可解析: %v", err)
	}
	if len(parsed.Confirmed) != 1 || parsed.Confirmed[0] != 2 {
		t.Fatalf("confirmed 应去重去负,得到 %v", parsed.Confirmed)
	}
	if len(parsed.Missing) != 3 || parsed.Missing[0].Value != "x.example.com" {
		t.Fatalf("missing 应清洗保留(合法性在 missingToCandidate 过滤),得到 %+v", parsed.Missing)
	}
	if c := missingToCandidate(parsed.Missing[0]); c == nil || c.Value != "x.example.com" {
		t.Fatalf("合法 missing 应转候选,得到 %+v", c)
	}
	if missingToCandidate(parsed.Missing[1]) != nil {
		t.Fatal("空值 missing 应被 missingToCandidate 过滤")
	}
	if missingToCandidate(parsed.Missing[2]) != nil {
		t.Fatal("白名单外类型应被 missingToCandidate 过滤")
	}
	if parsed.Confirmed == nil || parsed.Missing == nil {
		t.Fatal("解析集合不得为 nil(红线)")
	}
	// 杂文本前缀 + 首尾大括号截取的兜底路径。
	parsed2, err := parseAssetExtractJSON("前置废话 {\"confirmed\":[0],\"missing\":[],\"note\":\"\"} 后置废话")
	if err != nil || len(parsed2.Confirmed) != 1 {
		t.Fatalf("非围栏 JSON 应走首尾大括号兜底: %v %+v", err, parsed2)
	}
	if _, err := parseAssetExtractJSON("完全不是 JSON"); err == nil {
		t.Fatal("完全非法输出应返回错误(调用方置 partial)")
	}
}

// TestAssetCandidateStagedMapping 候选 → 暂存行的类型映射与 extra(契约 §3.2.5)。
func TestAssetCandidateStagedMapping(t *testing.T) {
	sa := candidateStaged(assetCandidate{Type: "service", Value: "h.example.com", Port: 8080, URL: "http://h.example.com:8080/"}, 7, assetCollectorAuto)
	if sa.Collector != "auto_extract" || sa.Type != "service" || sa.Port != 8080 {
		t.Fatalf("暂存行字段不符: %+v", sa)
	}
	if sa.CompanyID == nil || *sa.CompanyID != 7 {
		t.Fatalf("companyOverride 应写进 CompanyID: %+v", sa.CompanyID)
	}
	var extra map[string]any
	if err := json.Unmarshal(sa.Extra, &extra); err != nil || extra["url"] != "http://h.example.com:8080/" {
		t.Fatalf("URL 候选 extra 应带 url: %s (%v)", string(sa.Extra), err)
	}
	// 无 URL 的 host:port service 补 service_name=tcp(UpsertOtherService 硬要求)。
	sa2 := candidateStaged(assetCandidate{Type: "service", Value: "db.internal.corp", Port: 5432}, 0, assetCollectorAI)
	if sa2.Collector != "ai_extract" {
		t.Fatalf("AI 新发现 collector 应为 ai_extract: %+v", sa2)
	}
	var extra2 map[string]any
	_ = json.Unmarshal(sa2.Extra, &extra2)
	if extra2["service_name"] != "tcp" {
		t.Fatalf("无 URL service 应补 service_name=tcp: %s", string(sa2.Extra))
	}
	if sa2.CompanyID != nil {
		t.Fatal("companyOverride=0 时不得强制归属")
	}
}

// TestAssetExtractSettingValue 开关存储值(纯函数)。
func TestAssetExtractSettingValue(t *testing.T) {
	if assetExtractSettingValue(true) != "true" || assetExtractSettingValue(false) != "false" {
		t.Fatal("开关存储值映射错误")
	}
}

// TestAssetExtractNoiseFiltered 回归(8799 冒烟实测踩到):URL 主机为 loopback 的
// service 候选与代码 token 形态的假域名(socket.gethostbyname/s.recv)必须被拒;
// 真实域名与内网常见后缀(.corp)不受影响。
func TestAssetExtractNoiseFiltered(t *testing.T) {
	cands := extractAssetCandidates([]assetTextSegment{
		assetSeg("tool_result", "代理 http://127.0.0.1:7890 已起;socket.gethostbyname 与 s.recv 是代码片段;真实目标 web01.corp 与 api.example.com 正常"),
	})
	if findCandidate(cands, "service", "127.0.0.1", 7890) != nil {
		t.Fatalf("URL 主机为 loopback 应丢弃: %+v", cands)
	}
	for _, bad := range []string{"socket.gethostbyname", "s.recv"} {
		if findCandidate(cands, "domain", bad, 0) != nil {
			t.Fatalf("代码 token %s 不应被提取为域名: %+v", bad, cands)
		}
	}
	if findCandidate(cands, "domain", "web01.corp", 0) == nil {
		t.Fatalf("内网后缀域名 web01.corp 应保留: %+v", cands)
	}
	if findCandidate(cands, "domain", "api.example.com", 0) == nil {
		t.Fatalf("真实域名 api.example.com 应保留: %+v", cands)
	}
}
