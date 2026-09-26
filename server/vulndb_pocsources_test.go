package server

// PocOrExp / nuclei 纯函数单测:不连库、不出网(规范见 VULNDB_CONTRACTS_V2.md §5.5)。

import (
	"reflect"
	"testing"
)

// TestParsePocOrExpMD 表驱动覆盖:多链接/段内去重/无链接/非 github 链接/坏标题/重复 CVE。
func TestParsePocOrExpMD(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    map[string][]string
	}{
		{
			name: "多链接+段内去重+非github链接",
			content: `# PocOrExp in Github

## CVE-2024-49138
Windows 最近的某个提权漏洞描述,详见 https://example.com/post。
- [https://github.com/user1/repo1](https://github.com/user1/repo1) :  ![starts](https://img.shields.io/github/stars/user1/repo1.svg) ![forks](https://img.shields.io/github/forks/user1/repo1.svg)
- [https://github.com/user2/repo2](https://github.com/user2/repo2) :  描述文字
- [https://gitlab.com/user3/repo3](https://gitlab.com/user3/repo3) :  gitlab 不该收
- [https://github.com/user1/repo1](https://github.com/user1/repo1) :  重复链接去重
`,
			want: map[string][]string{
				"CVE-2024-49138": {
					"https://github.com/user1/repo1",
					"https://github.com/user2/repo2",
				},
			},
		},
		{
			name: "无链接行:该 CVE 不出现在结果里",
			content: `## CVE-2023-9999
只有描述,没有任何 bullet 链接。
- 普通文字 bullet
- [链接文字](https://example.com/x) 非 github
`,
			want: map[string][]string{},
		},
		{
			name: "坏标题整段丢弃,不影响后续好标题",
			content: `## CVE-2024-123
- [https://github.com/bad/a](https://github.com/bad/a) :  序号只有 3 位,标题作废
## CVE-24-12345
- [https://github.com/bad/b](https://github.com/bad/b) :  年份位数不对
## CVE-2024-abcdef
- [https://github.com/bad/c](https://github.com/bad/c) :  序号非数字
## CVE-2024-12345678
- [https://github.com/bad/d](https://github.com/bad/d) :  序号 8 位越界
## 摘要
- [https://github.com/bad/e](https://github.com/bad/e) :  根本不是 CVE 标题
## CVE-2024-7777
- [https://github.com/good/repo](https://github.com/good/repo) :  唯一有效段
`,
			want: map[string][]string{
				"CVE-2024-7777": {"https://github.com/good/repo"},
			},
		},
		{
			name: "重复 CVE 标题:合并去重、保持出现顺序",
			content: `## CVE-2022-1111
- [https://github.com/aaa/x](https://github.com/aaa/x) :
## CVE-2022-1111
- [https://github.com/bbb/y](https://github.com/bbb/y) :
- [https://github.com/aaa/x](https://github.com/aaa/x) :  跨段重复
`,
			want: map[string][]string{
				"CVE-2022-1111": {
					"https://github.com/aaa/x",
					"https://github.com/bbb/y",
				},
			},
		},
		{
			name: "编号位数边界与 owner/repo 特殊字符、子路径截断",
			content: `## CVE-2021-40344
- [https://github.com/Some-Org/some.repo_v2/tree/main/sub](https://github.com/Some-Org/some.repo_v2) :  子路径只取仓库
## CVE-1999-0001
- [https://github.com/o/r](https://github.com/o/r) :  最短年份+序号
`,
			want: map[string][]string{
				"CVE-2021-40344": {"https://github.com/Some-Org/some.repo_v2"},
				"CVE-1999-0001":  {"https://github.com/o/r"},
			},
		},
		{
			name:    "CRLF 行尾正常解析",
			content: "## CVE-2020-1111\r\n- [https://github.com/crlf/repo](https://github.com/crlf/repo) :\r\n",
			want: map[string][]string{
				"CVE-2020-1111": {"https://github.com/crlf/repo"},
			},
		},
		{
			name:    "空输入",
			content: "",
			want:    map[string][]string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parsePocOrExpMD(tc.content)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parsePocOrExpMD() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestNucleiParseTree 校验 git/trees JSON 解析:blob 路径收集 + truncated 标记。
func TestNucleiParseTree(t *testing.T) {
	t.Run("正常树:只收 blob,读取 truncated=false", func(t *testing.T) {
		body := []byte(`{
		  "sha": "deadbeef",
		  "tree": [
		    {"path": "http", "type": "tree"},
		    {"path": "http/cves", "type": "tree"},
		    {"path": "http/cves/2024/CVE-2024-1234.yaml", "type": "blob"},
		    {"path": "http/cves/CVE-2021-34567.yaml", "type": "blob"},
		    {"path": "README.md", "type": "blob"}
		  ],
		  "truncated": false
		}`)
		paths, truncated, err := nucleiParseTree(body)
		if err != nil {
			t.Fatalf("nucleiParseTree() err = %v", err)
		}
		if truncated {
			t.Fatalf("truncated = true, want false")
		}
		want := []string{
			"http/cves/2024/CVE-2024-1234.yaml",
			"http/cves/CVE-2021-34567.yaml",
			"README.md",
		}
		if !reflect.DeepEqual(paths, want) {
			t.Fatalf("paths = %#v, want %#v", paths, want)
		}
	})
	t.Run("截断树:truncated=true", func(t *testing.T) {
		body := []byte(`{"truncated": true, "tree": [{"path": "http/cves/2024/CVE-2024-1234.yaml", "type": "blob"}]}`)
		_, truncated, err := nucleiParseTree(body)
		if err != nil {
			t.Fatalf("nucleiParseTree() err = %v", err)
		}
		if !truncated {
			t.Fatalf("truncated = false, want true")
		}
	})
	t.Run("坏 JSON 报错", func(t *testing.T) {
		if _, _, err := nucleiParseTree([]byte("not json")); err == nil {
			t.Fatalf("期望解析报错,得到 nil")
		}
	})
}

// TestNucleiFilterCVEPaths 校验路径筛选:平铺与按年份子目录两种布局都兼容。
func TestNucleiFilterCVEPaths(t *testing.T) {
	paths := []string{
		"http/cves/2024/CVE-2024-1234.yaml",     // 按年份子目录(现行布局)
		"http/cves/CVE-2021-34567.yaml",         // 平铺(早期布局)
		"http/cves/2023/CVE-2023-99999.yaml",    // 5 位序号
		"http/cves/2024/CVE-2024-1234.yaml",     // 重复路径:保留先出现
		"http/cves/README.md",                   // 非 CVE 文件
		"http/cves/2022/CVE-2022-1.yaml",        // 序号 1 位,过短
		"http/cves/2020/CVE-2020-12345678.yaml", // 序号 8 位,过长
		"http/cves/2022/CVE-2022-2222.yml",      // 扩展名不对
		"http/cves/2022/CVE-2022-3333.yaml.bak", // 后缀不在结尾
		"dns/cves/CVE-2019-7777.yaml",           // 不在 http/cves 下
		"http/cves/2019/_category.json",         // 杂项
	}
	want := map[string]string{
		"CVE-2024-1234":  "http/cves/2024/CVE-2024-1234.yaml",
		"CVE-2021-34567": "http/cves/CVE-2021-34567.yaml",
		"CVE-2023-99999": "http/cves/2023/CVE-2023-99999.yaml",
	}
	got := nucleiFilterCVEPaths(paths)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nucleiFilterCVEPaths() = %#v, want %#v", got, want)
	}
	if len(got) != 3 {
		t.Fatalf("期望筛出 3 条,得到 %d", len(got))
	}
}

// TestNucleiContentsPaths 校验 contents API 目录条目 → 文件路径列表。
func TestNucleiContentsPaths(t *testing.T) {
	entries := []nucleiContentsEntry{
		{Name: "CVE-2024-1234.yaml", Path: "http/cves/2024/CVE-2024-1234.yaml", Type: "file"},
		{Name: "2024", Path: "http/cves/2024", Type: "dir"},
		{Name: ".gitkeep", Path: "http/cves/.gitkeep", Type: "file"},
		{Name: "空路径", Path: "", Type: "file"},
	}
	want := []string{
		"http/cves/2024/CVE-2024-1234.yaml",
		"http/cves/.gitkeep",
	}
	got := nucleiContentsPaths(entries)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nucleiContentsPaths() = %#v, want %#v", got, want)
	}
}
