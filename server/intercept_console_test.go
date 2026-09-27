package server

import (
	"net/url"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// 审批台纯函数单测(不连库):LIKE 转义、counts 补零与 nil 防护、参数白名单。

func TestEscapeLikePattern(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"plain", "plain"},
		{"100%", `100\%`},
		{"under_score", `under\_score`},
		{`back\slash`, `back\\slash`},
		{`%\_`, `\%\\\_`},
		{"脚本命令", "脚本命令"},
		{"中文%下划线_混合\\", `中文\%下划线\_混合\\`},
		// 已含转义序列的输入也要整体字面化,不允许二次解释。
		{`100\%`, `100\\\%`},
	}
	for _, c := range cases {
		if got := db.EscapeLikePattern(c.in); got != c.want {
			t.Errorf("EscapeLikePattern(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestInterceptConsoleCountsCompletion(t *testing.T) {
	// nil 输入 → 五档全 0 的非 nil map(nil map 会序列化成 JSON null)。
	got := db.CompleteInterceptCounts(nil)
	if got == nil {
		t.Fatal("CompleteInterceptCounts(nil) = nil, want non-nil map")
	}
	if len(got) != 5 {
		t.Fatalf("len(counts) = %d, want 5: %v", len(got), got)
	}
	for _, s := range []string{"pending", "allowed", "denied", "timeout", "flagged"} {
		if v, ok := got[s]; !ok || v != 0 {
			t.Errorf("counts[%s] = (%d,%v), want (0,true)", s, v, ok)
		}
	}
	// 部分命中:缺档补 0,未知档丢弃(wire 恒为五档)。
	got = db.CompleteInterceptCounts(map[string]int{"pending": 2, "bogus": 9})
	if got["pending"] != 2 {
		t.Errorf("counts[pending] = %d, want 2", got["pending"])
	}
	for _, s := range []string{"allowed", "denied", "timeout", "flagged"} {
		if got[s] != 0 {
			t.Errorf("缺档 %s 应补 0, got %d", s, got[s])
		}
	}
	if _, ok := got["bogus"]; ok {
		t.Errorf("未知档 bogus 应被丢弃: %v", got)
	}
	// 全量输入原样保留。
	full := map[string]int{"pending": 1, "allowed": 2, "denied": 3, "timeout": 4, "flagged": 5}
	got = db.CompleteInterceptCounts(full)
	for _, s := range []string{"pending", "allowed", "denied", "timeout", "flagged"} {
		if got[s] != full[s] {
			t.Errorf("counts[%s] = %d, want %d", s, got[s], full[s])
		}
	}
}

func TestInterceptConsoleFilterParams(t *testing.T) {
	// 全参数合法。
	q := url.Values{"status": {"flagged"}, "decision_source": {"rule"}, "q": {"rm -rf"}, "task_id": {"42"}, "tool": {"Bash"}}
	f, err := interceptConsoleFilterParams(q)
	if err != nil {
		t.Fatalf("合法参数报错: %v", err)
	}
	if f.Status != "flagged" || f.DecisionSource != "rule" || f.Q != "rm -rf" || f.TaskID != "42" || f.Tool != "Bash" {
		t.Errorf("filter 解析不对: %+v", f)
	}
	// 空参数全部合法(空=不过滤)。
	if f, err := interceptConsoleFilterParams(url.Values{}); err != nil || f != (db.InterceptApprovalFilter{}) {
		t.Errorf("空参数应得到零值 filter: %+v err=%v", f, err)
	}
	// status 白名单(400 中文报错,口径与 interceptFilterParams 一致)。
	for _, bad := range []string{"nope", "ALLOWED", "denied "} {
		if _, err := interceptConsoleFilterParams(url.Values{"status": {bad}}); err == nil || !strings.Contains(err.Error(), "status 必须是 pending、allowed、denied、timeout 或 flagged") {
			t.Errorf("非法 status %q 应报白名单错, got %v", bad, err)
		}
	}
	// decision_source 白名单。
	for _, bad := range []string{"ai", "Rule", "model "} {
		if _, err := interceptConsoleFilterParams(url.Values{"decision_source": {bad}}); err == nil || !strings.Contains(err.Error(), "decision_source 必须是 model、rule 或 unknown") {
			t.Errorf("非法 decision_source %q 应报白名单错, got %v", bad, err)
		}
	}
}
