package server

// NVD 历史回填 + KEV 的纯函数单测:不连库、不出网。

import (
	"encoding/json"
	"testing"
)

// TestVulnBackfillCursorRoundTrip 光标 JSON 序列化/反序列化 round-trip,
// 并校验 JSON 字段名与冻结契约一致(settings 里的值跨版本读写)。
func TestVulnBackfillCursorRoundTrip(t *testing.T) {
	cases := []backfillCursor{
		{Phase: "nvd", WindowEnd: "2017-04-30T00:00:00.000", YearDone: 0, StartYear: 2016, Done: false},
		{Phase: "pocorexp", WindowEnd: "", YearDone: 2019, StartYear: 2011, Done: false},
		{Phase: "pocorexp", WindowEnd: "", YearDone: 2026, StartYear: 2016, Done: true},
		{Phase: "nvd", WindowEnd: "", YearDone: 0, StartYear: 2020, Done: false},
	}
	for _, want := range cases {
		b, err := json.Marshal(&want)
		if err != nil {
			t.Fatalf("序列化失败: %v", err)
		}
		var got backfillCursor
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("反序列化失败: %v", err)
		}
		if got != want {
			t.Errorf("round-trip 不一致: want %+v got %+v", want, got)
		}
	}

	// 字段名必须是契约里的 snake_case:phase/window_end/year_done/start_year/done。
	b, _ := json.Marshal(&backfillCursor{Phase: "nvd", StartYear: 2016})
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("解析为 map 失败: %v", err)
	}
	for _, key := range []string{"phase", "window_end", "year_done", "start_year", "done"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("光标 JSON 缺少契约字段 %q,实际: %s", key, b)
		}
	}
}

// TestSeverityFromCVSS2Score CVSS v2 分数 → 展示严重度映射(表驱动)。
func TestSeverityFromCVSS2Score(t *testing.T) {
	cases := []struct {
		score float64
		want  string
	}{
		{0, ""},      // 无分数 → 未知
		{-1, ""},     // 异常分数 → 未知
		{0.1, "low"}, // >0 即 low
		{3.9, "low"}, // <4 low
		{4.0, "medium"},
		{6.9, "medium"},
		{7.0, "high"},
		{8.9, "high"},
		{9.0, "critical"},
		{10.0, "critical"},
	}
	for _, c := range cases {
		if got := backfillSeverityFromCVSS2(c.score); got != c.want {
			t.Errorf("backfillSeverityFromCVSS2(%v) = %q, want %q", c.score, got, c.want)
		}
	}
}

// backfillTestMetric 构造一个 CVSS 指标块。
func backfillTestMetric(score float64, severity string) backfillCVSSMetric {
	var m backfillCVSSMetric
	m.CVSSData.BaseScore = score
	m.CVSSData.BaseSeverity = severity
	return m
}

// TestSeverityFromNVDChain severity 依次取 v3.1 → v3.0 → v2 兜底的选择链。
func TestSeverityFromNVDChain(t *testing.T) {
	mk := func(v31, v30 []backfillCVSSMetric, v2Score float64, hasV2 bool) *backfillNVDCve {
		var c backfillNVDCve
		c.Metrics.CVSSMetricV31 = v31
		c.Metrics.CVSSMetricV30 = v30
		if hasV2 {
			c.Metrics.CVSSMetricV2 = []backfillCVSSMetric{backfillTestMetric(v2Score, "")}
		}
		return &c
	}
	cases := []struct {
		name string
		c    *backfillNVDCve
		want string
	}{
		{"v31 优先", mk([]backfillCVSSMetric{backfillTestMetric(8.1, "HIGH")}, nil, 0, false), "high"},
		{"v31 大写归一", mk([]backfillCVSSMetric{backfillTestMetric(9.8, "CRITICAL")}, nil, 0, false), "critical"},
		{"无 v31 用 v30", mk(nil, []backfillCVSSMetric{backfillTestMetric(5.3, "Medium")}, 0, false), "medium"},
		{"v31/v30 均空用 v2 映射", mk(nil, nil, 9.5, true), "critical"},
		{"v2 中分映射 medium", mk(nil, nil, 6.5, true), "medium"},
		{"全部缺失为空", mk(nil, nil, 0, false), ""},
	}
	for _, c := range cases {
		if got := backfillSeverity(c.c); got != c.want {
			t.Errorf("%s: backfillSeverity() = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestKEVBackfillParse KEV 目录 JSON 解析:内联 fixture 覆盖大小写归一、
// 去重、空白项跳过,以及坏 JSON 报错。
func TestKEVBackfillParse(t *testing.T) {
	fixture := `{
	  "title": "CISA Catalog of Known Exploited Vulnerabilities",
	  "catalogVersion": "2024-05-01-0000",
	  "dateReleased": "2024-05-01T00:00:00.0000Z",
	  "count": 4,
	  "vulnerabilities": [
	    {"cveID": "CVE-2024-1234", "vendorProject": "X", "product": "Y", "dateAdded": "2024-01-02", "knownRansomwareCampaignUse": "Known"},
	    {"cveID": "cve-2023-9999", "vendorProject": "A", "product": "B"},
	    {"cveID": "CVE-2024-1234"},
	    {"cveID": "   "},
	    {"vendorProject": "no-cve-id-entry"}
	  ]
	}`
	got, err := backfillParseKEV([]byte(fixture))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	want := []string{"CVE-2024-1234", "CVE-2023-9999"}
	if len(got) != len(want) {
		t.Fatalf("解析数量不符: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 项: got %q want %q(应去重、大写、跳过空白)", i, got[i], want[i])
		}
	}

	// 坏 JSON 必须报错(调用方中止本轮并写入 sync_run,而非静默清空标记)。
	if _, err := backfillParseKEV([]byte("not json at all")); err == nil {
		t.Error("坏 JSON 应返回错误")
	}
	// 无 vulnerabilities 字段:合法 JSON、空列表。
	empty, err := backfillParseKEV([]byte(`{"title":"x"}`))
	if err != nil {
		t.Errorf("空目录不应报错: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("空目录应返回 0 条, got %v", empty)
	}
}
