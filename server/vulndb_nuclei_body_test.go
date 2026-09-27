package server

// nuclei 正文回填的纯函数单测(契约 §6.4:光标推进/截断 JSON 解析/路径归一,
// 全部不出网不碰库)。

import (
	"strings"
	"testing"
)

func TestNucleiBodyParseCursor(t *testing.T) {
	t.Run("空串与空白返回 nil", func(t *testing.T) {
		if got := nucleiBodyParseCursor(""); got != nil {
			t.Fatalf("空串应返回 nil,得到 %+v", got)
		}
		if got := nucleiBodyParseCursor("   \n\t"); got != nil {
			t.Fatalf("空白应返回 nil,得到 %+v", got)
		}
	})
	t.Run("合法 JSON", func(t *testing.T) {
		c := nucleiBodyParseCursor(`{"done":true,"last_id":4242}`)
		if c == nil || !c.Done || c.LastID != 4242 {
			t.Fatalf("解析错误: %+v", c)
		}
		c = nucleiBodyParseCursor(`{"done":false,"last_id":0}`)
		if c == nil || c.Done || c.LastID != 0 {
			t.Fatalf("解析错误: %+v", c)
		}
	})
	t.Run("截断 JSON 正则兜底", func(t *testing.T) {
		// 进程在 SetSetting 写入中途崩溃的残值:严格解析必败,按正则抠字段。
		c := nucleiBodyParseCursor(`{"done":false,"last_id":123`)
		if c == nil || c.Done || c.LastID != 123 {
			t.Fatalf("截断光标应兜底出 last_id=123: %+v", c)
		}
		c = nucleiBodyParseCursor(`{"done":true,"last_id":99`) // done 先于截断点
		if c == nil || !c.Done || c.LastID != 99 {
			t.Fatalf("截断光标应兜底出 done=true,last_id=99: %+v", c)
		}
		// 只有半截键名:字段缺省零值,语义=从头全量回扫。
		c = nucleiBodyParseCursor(`{"done":fals`)
		if c == nil || c.Done || c.LastID != 0 {
			t.Fatalf("残缺光标应回退零值: %+v", c)
		}
	})
	t.Run("垃圾内容回退零值光标", func(t *testing.T) {
		c := nucleiBodyParseCursor("not-json-at-all")
		if c == nil || c.Done || c.LastID != 0 {
			t.Fatalf("垃圾应返回零值光标: %+v", c)
		}
	})
}

func TestNucleiBodyAdvance(t *testing.T) {
	t.Run("只前进不回退", func(t *testing.T) {
		cur := &nucleiBodyCursor{LastID: 100}
		nucleiBodyAdvance(cur, 200)
		if cur.LastID != 200 {
			t.Fatalf("应推进到 200,得到 %d", cur.LastID)
		}
		nucleiBodyAdvance(cur, 150) // 乱序/重复行不得回退光标
		if cur.LastID != 200 {
			t.Fatalf("光标被回退: %d", cur.LastID)
		}
	})
	t.Run("零值光标推进", func(t *testing.T) {
		cur := &nucleiBodyCursor{}
		nucleiBodyAdvance(cur, 7)
		if cur.LastID != 7 {
			t.Fatalf("应推进到 7,得到 %d", cur.LastID)
		}
	})
}

func TestNucleiBodyResumeCursor(t *testing.T) {
	t.Run("未完成光标原样续跑", func(t *testing.T) {
		cur := &nucleiBodyCursor{Done: false, LastID: 55}
		got := nucleiBodyResumeCursor(cur)
		if got != cur || got.LastID != 55 {
			t.Fatalf("应原样续跑: %+v", got)
		}
	})
	t.Run("done 光标回退全量重扫", func(t *testing.T) {
		got := nucleiBodyResumeCursor(&nucleiBodyCursor{Done: true, LastID: 55})
		if got == nil || got.Done || got.LastID != 0 {
			t.Fatalf("done 应换全新光标: %+v", got)
		}
	})
	t.Run("无光标全量重扫", func(t *testing.T) {
		got := nucleiBodyResumeCursor(nil)
		if got == nil || got.Done || got.LastID != 0 {
			t.Fatalf("nil 应换全新光标: %+v", got)
		}
	})
}

func TestNucleiBodyTemplatePathFromSource(t *testing.T) {
	blob := "https://github.com/projectdiscovery/nuclei-templates/blob/main/"
	raw := "https://raw.githubusercontent.com/projectdiscovery/nuclei-templates/main/"
	cases := []struct {
		name       string
		sourceURL  string
		content    string
		want       string
	}{
		{"blob 带年份子目录", blob + "http/cves/2021/CVE-2021-44228.yaml", "", "http/cves/2021/CVE-2021-44228.yaml"},
		{"blob 平铺早期路径", blob + "http/cves/CVE-2020-1234.yaml", "", "http/cves/CVE-2020-1234.yaml"},
		{"raw 直链", raw + "http/cves/2023/CVE-2023-0001.yaml", "", "http/cves/2023/CVE-2023-0001.yaml"},
		{"blob 带 query/fragment", blob + "http/cves/2021/CVE-2021-44228.yaml?plain=1#L1", "", "http/cves/2021/CVE-2021-44228.yaml"},
		{"source_url 失效时 content 兜底", "", "nuclei 官方模板,可直接用 nuclei -t http/cves/2022/CVE-2022-9999.yaml 运行。\nhttp/cves/2022/CVE-2022-9999.yaml", "http/cves/2022/CVE-2022-9999.yaml"},
		{"非本仓库链接拒绝", "https://github.com/other/repo/blob/main/http/cves/2021/CVE-2021-1.yaml", "", ""},
		{"非 cves 目录拒绝", blob + "network/cves/CVE-2020-1234.yaml", "", ""},
		{"非 yaml 拒绝", blob + "http/cves/2021/CVE-2021-44228.txt", "", ""},
		{"来源皆空返回空串", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nucleiTemplatePathFromSource(tc.sourceURL, tc.content)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNucleiBodyRawURLJoin(t *testing.T) {
	// 下载 URL = 固定 raw 根 + 归一路径;路径绝不能带 scheme 头造成拼接畸形。
	path := nucleiTemplatePathFromSource(
		"https://github.com/projectdiscovery/nuclei-templates/blob/main/http/cves/2024/CVE-2024-1234.yaml", "")
	if path == "" || strings.Contains(path, "://") {
		t.Fatalf("归一路径异常: %q", path)
	}
	if got := nucleiRawBaseURL + path; got != "https://raw.githubusercontent.com/projectdiscovery/nuclei-templates/main/http/cves/2024/CVE-2024-1234.yaml" {
		t.Fatalf("拼接结果异常: %q", got)
	}
}
