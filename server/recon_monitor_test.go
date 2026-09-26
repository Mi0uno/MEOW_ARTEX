package server

// 资产监控的纯函数单测:due 判定、全局间隔合法域、targets PUT interval_hours
// 三态解析、公网 IP 甄别。全部不触 DB(无需 ARTEX_PG_DSN),TestMain 未配置
// 隔离库时也照常跑。

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/recon"
)

func monitorTestTime(t time.Time) *time.Time { return &t }

func TestMonitorDue(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		lastRunAt *time.Time
		interval  int // 企业覆盖
		global    int // 全局间隔
		want      bool
	}{
		{"从未跑过立即due", nil, 24, 24, true},
		{"手动重置(nil)优先于一切", nil, 0, 0, true},
		{"企业覆盖短于全局_未到期2h", monitorTestTime(now.Add(-2 * time.Hour)), 3, 24, false},
		{"企业覆盖短于全局_到期4h", monitorTestTime(now.Add(-4 * time.Hour)), 3, 24, true},
		{"无覆盖跟全局_未到期", monitorTestTime(now.Add(-23 * time.Hour)), 0, 24, false},
		{"无覆盖跟全局_恰好到期", monitorTestTime(now.Add(-24 * time.Hour)), 0, 24, true},
		{"无覆盖跟全局_早于水位", monitorTestTime(now.Add(-25 * time.Hour)), 0, 24, true},
		{"企业覆盖压过全局_未到期", monitorTestTime(now.Add(-23 * time.Hour)), 720, 1, false},
		{"非法覆盖(负数)回退全局", monitorTestTime(now.Add(-23 * time.Hour)), -5, 24, false},
		{"非法覆盖且全局非法_立即due", monitorTestTime(now.Add(-1 * time.Hour)), 0, 0, true},
		{"720小时上限_恰好到期", monitorTestTime(now.Add(-720 * time.Hour)), 720, 24, true},
		{"未来水位不算到期", monitorTestTime(now.Add(1 * time.Hour)), 24, 24, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := monitorDue(tc.lastRunAt, tc.interval, tc.global, now); got != tc.want {
				t.Fatalf("monitorDue(last=%v, interval=%d, global=%d) = %v, want %v",
					tc.lastRunAt, tc.interval, tc.global, got, tc.want)
			}
		})
	}
}

func TestMonitorIntervalInRange(t *testing.T) {
	cases := []struct {
		n    int
		want bool
	}{
		{0, false}, {1, true}, {23, true}, {24, true}, {720, true}, {721, false}, {-1, false},
	}
	for _, tc := range cases {
		if got := monitorIntervalInRange(tc.n); got != tc.want {
			t.Fatalf("monitorIntervalInRange(%d) = %v, want %v", tc.n, got, tc.want)
		}
	}
}

func TestParseMonitorIntervalHours(t *testing.T) {
	t.Run("字段缺省_不动", func(t *testing.T) {
		interval, clear, err := parseMonitorIntervalHours(nil)
		if err != nil || interval != nil || clear {
			t.Fatalf("缺省字段应 (nil,false,nil), got (%v,%v,%v)", interval, clear, err)
		}
	})
	t.Run("显式null_清除覆盖", func(t *testing.T) {
		interval, clear, err := parseMonitorIntervalHours(json.RawMessage("null"))
		if err != nil || interval != nil || !clear {
			t.Fatalf("null 应 (nil,true,nil), got (%v,%v,%v)", interval, clear, err)
		}
	})
	t.Run("正整数_设置覆盖", func(t *testing.T) {
		interval, clear, err := parseMonitorIntervalHours(json.RawMessage("48"))
		if err != nil || interval == nil || *interval != 48 || clear {
			t.Fatalf("48 应 (48,false,nil), got (%v,%v,%v)", interval, clear, err)
		}
	})
	t.Run("空JSON体_等同缺省", func(t *testing.T) {
		interval, clear, err := parseMonitorIntervalHours(json.RawMessage{})
		if err != nil || interval != nil || clear {
			t.Fatalf("空 raw 应 (nil,false,nil), got (%v,%v,%v)", interval, clear, err)
		}
	})
	for _, raw := range []string{"0", "-3", "1.5", `"24"`, "{}", "true", "[24]"} {
		t.Run("非法值_"+raw, func(t *testing.T) {
			interval, clear, err := parseMonitorIntervalHours(json.RawMessage(raw))
			if err == nil {
				t.Fatalf("raw %s 应报错, got (%v,%v,nil)", raw, interval, clear)
			}
		})
	}
}

// 零产出甄别:crt.sh 把单域错误吞进 stats.domains[].error,收集器状态仍 done——
// collectorIssueSummary 要能把原因翻出来;域真没数据(无 error 键)必须返回空串,
// 否则正常「无记录」会被误判成失败。
func TestCollectorIssueSummary(t *testing.T) {
	crtshAllFailed := &recon.CollectorResult{Stats: map[string]any{
		"domains": []map[string]any{
			{"domain": "a.com", "found": 0, "error": "crt.sh 限流(429),稍后重试"},
		},
		"total": 0,
	}}
	if got := collectorIssueSummary(crtshAllFailed); got == "" || !strings.Contains(got, "1/1 个域查询失败") || !strings.Contains(got, "429") {
		t.Fatalf("全失败应返回含计数与原因的摘要, got %q", got)
	}
	noError := &recon.CollectorResult{Stats: map[string]any{
		"domains": []map[string]any{{"domain": "a.com", "found": 0}},
		"total":   0,
	}}
	if got := collectorIssueSummary(noError); got != "" {
		t.Fatalf("无 error 键(真无数据)应返回空串, got %q", got)
	}
	partial := &recon.CollectorResult{Stats: map[string]any{
		"domains": []map[string]any{{"domain": "a.com", "found": 3}, {"domain": "b.com", "found": 0, "error": "x"}},
	}}
	if got := collectorIssueSummary(partial); got == "" || !strings.Contains(got, "1/2") {
		t.Fatalf("部分失败应返回计数摘要, got %q", got)
	}
	if got := collectorIssueSummary(nil); got != "" {
		t.Fatalf("nil result 应返回空串, got %q", got)
	}
}

func TestMonitorFirstPublicIP(t *testing.T) {
	cases := []struct {
		name string
		ips  []string
		want string
	}{
		{"空解析结果", nil, ""},
		{"全部私网", []string{"10.0.0.1", "192.168.1.1", "172.16.0.9"}, ""},
		{"环回不算", []string{"127.0.0.1"}, ""},
		{"链路本地不算", []string{"169.254.1.1"}, ""},
		{"私网后跟公网", []string{"10.0.0.5", "93.184.216.34"}, "93.184.216.34"},
		{"首个公网胜出", []string{"93.184.216.34", "1.1.1.1"}, "93.184.216.34"},
		{"公网IPv6", []string{"2606:2800:220:1::1"}, "2606:2800:220:1::1"},
		{"IPv6本机不算", []string{"::1", "8.8.8.8"}, "8.8.8.8"},
		{"垃圾字符串跳过", []string{"not-an-ip", "8.8.4.4"}, "8.8.4.4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstPublicIP(tc.ips); got != tc.want {
				t.Fatalf("firstPublicIP(%v) = %q, want %q", tc.ips, got, tc.want)
			}
		})
	}
}
