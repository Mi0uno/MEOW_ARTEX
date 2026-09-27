package server

// parseTrickestMain / githubOwnerRepo 的纯函数单测(契约 §5.2:不出网,fixture
// 按 2026-09 实测的 trickest/cve 聚合页真实结构构造)。

import (
	"strings"
	"testing"
	"unicode/utf8"
)

const trickestFixtureFull = `### [CVE-2021-44228](https://cve.mitre.org/cgi-bin/cvename.cgi?name=CVE-2021-44228)
![](https://img.shields.io/static/v1?label=Product&message=Apache%20Log4j2&color=blue)
![](https://img.shields.io/static/v1?label=Version&message=2.0-beta9%20&color=brightgreen)

### Description

Apache Log4j2 JNDI features used in configuration do not protect against attacker controlled LDAP endpoints.

### POC

#### Reference
- http://packetstormsecurity.com/files/165225/Apache-Log4j2-2.14.1-Remote-Code-Execution.html
- https://www.exploit-db.com/exploits/50592
- https://tools.cisco.com/security/center/content/CiscoSecurityAdvisory/cisco-sa-apache-log4j-qRuKNEbd
- https://github.com/nu11secur1ty/CVE-mitre/tree/main/CVE-2021-44228
- http://packetstormsecurity.com/files/165225/Apache-Log4j2-2.14.1-Remote-Code-Execution.html
- 这一行没有链接,应被丢弃
- https://evil-packetstormsecurity.com/fake
- https://seebug.org/vuldb/ssvid-99497
- https://vulhub.org/#/environments/log4j/CVE-2021-44228/
- https://www.gitee.com/mirrors/poc
- https://hackerone.com/reports/1432

#### Github
- https://github.com/0xInfection/LogMePwn
- https://github.com/0xInfection/LogMePwn
- https://github.com/0xThiebaut/CVE-2021-44228
`

func TestParseTrickestMainFull(t *testing.T) {
	p := parseTrickestMain(trickestFixtureFull)

	wantGH := []string{
		"https://github.com/0xInfection/LogMePwn",
		"https://github.com/0xThiebaut/CVE-2021-44228",
	}
	if len(p.Github) != len(wantGH) {
		t.Fatalf("Github 段 = %v, 期望 %v", p.Github, wantGH)
	}
	for i, u := range wantGH {
		if p.Github[i] != u {
			t.Errorf("Github[%d] = %s, 期望 %s", i, p.Github[i], u)
		}
	}

	// 安全站:packetstorm + exploit-db 子域 + seebug + vulhub + gitee 子域 + hackerone。
	// 重复的 packetstorm 链接去重;github.com 链接与 cisco 链接不是安全站;
	// evil-packetstormsecurity.com 是伪造后缀,必须不命中。
	wantSecure := []string{
		"http://packetstormsecurity.com/files/165225/Apache-Log4j2-2.14.1-Remote-Code-Execution.html",
		"https://www.exploit-db.com/exploits/50592",
		"https://seebug.org/vuldb/ssvid-99497",
		"https://vulhub.org/#/environments/log4j/CVE-2021-44228/",
		"https://www.gitee.com/mirrors/poc",
		"https://hackerone.com/reports/1432",
	}
	if len(p.RefSecures) != len(wantSecure) {
		t.Fatalf("RefSecures = %v, 期望 %v", p.RefSecures, wantSecure)
	}
	for i, u := range wantSecure {
		if p.RefSecures[i] != u {
			t.Errorf("RefSecures[%d] = %s, 期望 %s", i, p.RefSecures[i], u)
		}
	}

	wantRefs := []string{
		"https://tools.cisco.com/security/center/content/CiscoSecurityAdvisory/cisco-sa-apache-log4j-qRuKNEbd",
		"https://github.com/nu11secur1ty/CVE-mitre/tree/main/CVE-2021-44228",
		"https://evil-packetstormsecurity.com/fake",
	}
	if len(p.Refs) != len(wantRefs) {
		t.Fatalf("Refs = %v, 期望 %v", p.Refs, wantRefs)
	}
	for i, u := range wantRefs {
		if p.Refs[i] != u {
			t.Errorf("Refs[%d] = %s, 期望 %s", i, p.Refs[i], u)
		}
	}
}

// 无 POC 段、Github 段为空、无任何 #### 段的退化形态:解析不 panic、返回空。
func TestParseTrickestMainDegenerate(t *testing.T) {
	noPoc := `### [CVE-2024-1234](https://cve.mitre.org/cgi-bin/cvename.cgi?name=CVE-2024-1234)

### Description

Text only advisory, no POC sections at all.
`
	p := parseTrickestMain(noPoc)
	if len(p.Github) != 0 || len(p.RefSecures) != 0 || len(p.Refs) != 0 {
		t.Fatalf("无段文档应全空,得到 %+v", p)
	}

	emptyGH := `### [CVE-2024-5678](https://cve.mitre.org/cgi-bin/cvename.cgi?name=CVE-2024-5678)

### Description

Advisory with an empty Github section.

#### Reference
- https://unit42.paloaltonetworks.com/cve-2024-3400/

#### Github
`
	p = parseTrickestMain(emptyGH)
	if len(p.Github) != 0 {
		t.Fatalf("空 Github 段应为空,得到 %v", p.Github)
	}
	if len(p.RefSecures) != 0 || len(p.Refs) != 1 {
		t.Fatalf("引用段解析不符: secure=%v refs=%v", p.RefSecures, p.Refs)
	}
}

// #### Github 在 #### Reference 之前的段序(仓库实测两种顺序都出现)。
func TestParseTrickestMainSectionOrderSwapped(t *testing.T) {
	md := `### [CVE-2024-3400](https://cve.mitre.org/cgi-bin/cvename.cgi?name=CVE-2024-3400)

### POC

#### Github
- https://github.com/0xr2r/CVE-2024-3400-Palo-Alto-OS-Command-Injection

#### Reference
- https://security.paloaltonetworks.com/CVE-2024-3400
`
	p := parseTrickestMain(md)
	if len(p.Github) != 1 || p.Github[0] != "https://github.com/0xr2r/CVE-2024-3400-Palo-Alto-OS-Command-Injection" {
		t.Fatalf("Github 段解析不符: %v", p.Github)
	}
	// security.paloaltonetworks.com 不是安全站清单里的域 → refs。
	if len(p.Refs) != 1 || p.Refs[0] != "https://security.paloaltonetworks.com/CVE-2024-3400" {
		t.Fatalf("Refs 解析不符: %v", p.Refs)
	}
	if len(p.RefSecures) != 0 {
		t.Fatalf("RefSecures 应为空: %v", p.RefSecures)
	}
}

func TestTrickestIsSecureRef(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://packetstormsecurity.com/files/x", true},
		{"http://www.exploit-db.com/exploits/1", true},
		{"https://exploit-db.com/exploits/1", true},
		{"https://www.seebug.org/vuldb/1", true},
		{"https://gitlab.com/gitlab-org/cves/-/blob/main/2024/CVE-2024-1234.json", true},
		{"https://gitee.com/a/b", true},
		{"https://hackerone.com/reports/1", true},
		{"https://github.com/a/b", false},
		{"https://tools.cisco.com/x", false},
		{"https://evil-packetstormsecurity.com/fake", false},
		{"not a url", false},
	}
	for _, c := range cases {
		if got := trickestIsSecureRef(c.url); got != c.want {
			t.Errorf("trickestIsSecureRef(%q) = %v, 期望 %v", c.url, got, c.want)
		}
	}
}

func TestGithubOwnerRepo(t *testing.T) {
	cases := []struct {
		in          string
		owner, repo string
		ok          bool
	}{
		{"https://github.com/owner/repo", "owner", "repo", true},
		{"http://github.com/owner/repo/", "owner", "repo", true},
		{"github.com/owner/repo", "owner", "repo", true},
		{"https://github.com/owner/repo.git", "owner", "repo", true},
		{"https://github.com/owner/repo/tree/main/deep/dir", "owner", "repo", true},
		{"https://github.com/owner/repo/blob/main/cmd/x.go", "owner", "repo", true},
		{"https://github.com/owner", "", "", false},
		{"https://github.com/", "", "", false},
		{"https://example.com/owner/repo", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		owner, repo, ok := githubOwnerRepo(c.in)
		if ok != c.ok || owner != c.owner || repo != c.repo {
			t.Errorf("githubOwnerRepo(%q) = (%q,%q,%v), 期望 (%q,%q,%v)",
				c.in, owner, repo, ok, c.owner, c.repo, c.ok)
		}
	}
}

func TestTruncateReadme(t *testing.T) {
	if got := truncateReadme("short"); got != "short" {
		t.Fatalf("短文本不应被截断: %q", got)
	}
	long := make([]byte, githubReadmeMaxBytes+100)
	for i := range long {
		long[i] = 'a'
	}
	if got := truncateReadme(string(long)); len(got) != githubReadmeMaxBytes {
		t.Fatalf("4KB 截断后长度 = %d, 期望 %d", len(got), githubReadmeMaxBytes)
	}
	// 多字节字符边界:中文每字 3 字节,截断点落在字符中间时须回退到合法 UTF-8。
	cjk := strings.Repeat("中", githubReadmeMaxBytes/3+10)
	got := truncateReadme(cjk)
	if !utf8.ValidString(got) {
		t.Fatalf("截断结果不是合法 UTF-8")
	}
}

func TestTrickestCursorJSON(t *testing.T) {
	// 契约 wire:{done bool, last_id int64};字段名冻结,前端/续跑都依赖。
	in := `{"done":true,"last_id":42}`
	cur, err := trickestCursorFromJSON(in)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !cur.Done || cur.LastID != 42 {
		t.Fatalf("解析结果不符: %+v", cur)
	}
	// 空/缺字段容错。
	cur, err = trickestCursorFromJSON(`{}`)
	if err != nil || cur.Done || cur.LastID != 0 {
		t.Fatalf("空对象解析不符: %+v err=%v", cur, err)
	}
	if _, err := trickestCursorFromJSON(`not-json`); err == nil {
		t.Fatalf("坏 JSON 应报错")
	}
}
