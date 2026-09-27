package server

import (
	"strings"
	"testing"
	"time"
)

// TestBatchExportEntryName 覆盖:序号补零、非法字符替换、60 rune 截断、空标题回退。
func TestBatchExportEntryName(t *testing.T) {
	cases := []struct {
		name  string
		idx   int
		title string
		ext   string
		want  string
	}{
		{"序号补零到三位", 1, "内网渗透总结", "md", "001-内网渗透总结.md"},
		{"两位序号也补零", 42, "报告", "docx", "042-报告.docx"},
		{"非法字符全部替换", 3, `a/b\c:d*e?f"g<h>i|j`, "md", "003-a_b_c_d_e_f_g_h_i_j.md"},
		{"超长中文按60rune截断", 7, strings.Repeat("渗", 70), "md", "007-" + strings.Repeat("渗", 60) + ".md"},
		{"截断后恰好60不变形", 8, strings.Repeat("报", 60), "md", "008-" + strings.Repeat("报", 60) + ".md"},
		{"空标题回退report", 12, "   ", "md", "012-report.md"},
		{"全非法字符标题回退", 13, "\\/:*?\"<>|", "docx", "013-_________.docx"},
	}
	for _, c := range cases {
		if got := batchExportEntryName(c.idx, c.title, c.ext); got != c.want {
			t.Errorf("%s: batchExportEntryName(%d, %q, %q) = %q, want %q", c.name, c.idx, c.title, c.ext, got, c.want)
		}
	}
}

// TestBatchExportManifestMD 覆盖:表头形态、成功行/失败行、中文分类映射、关联列。
func TestBatchExportManifestMD(t *testing.T) {
	up := time.Date(2026, 9, 27, 10, 30, 0, 0, time.UTC)
	rows := []batchExportManifestRow{
		{Index: 1, ID: 11, Title: "任务总结", Kind: "task", TaskID: 3, UpdatedAt: up},
		{Index: 2, ID: 12, Title: "SQL注入", Kind: "finding", FindingID: 7, UpdatedAt: up, Err: "报告不存在"},
		{Index: 3, ID: 13, Title: "复盘笔记", Kind: "custom", UpdatedAt: up},
		{Index: 4, ID: 14, Title: "无关联", Kind: "task", UpdatedAt: up},
	}
	md := batchExportManifestMD(rows)
	wants := []string{
		"# 报告导出清单",
		"| 序号 | 标题 | 分类 | 关联 | 更新时间 | 结果 |",
		"| --- | --- | --- | --- | --- | --- |",
		"| 1 | 任务总结 | 任务报告 | 任务 #3 | 2026-09-27 10:30 | 成功 |",
		"| 2 | SQL注入 | 漏洞报告 | 发现 #7 | 2026-09-27 10:30 | 失败:报告不存在 |",
		"| 3 | 复盘笔记 | 自定义 | — | 2026-09-27 10:30 | 成功 |",
		"| 4 | 无关联 | 任务报告 | — | 2026-09-27 10:30 | 成功 |",
	}
	for _, want := range wants {
		if !strings.Contains(md, want) {
			t.Errorf("清单缺少期望行 %q\n实际输出:\n%s", want, md)
		}
	}
}

// TestBatchExportManifestMDEscapes 标题里的竖线/换行不应破坏表格。
func TestBatchExportManifestMDEscapes(t *testing.T) {
	up := time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)
	md := batchExportManifestMD([]batchExportManifestRow{
		{Index: 1, ID: 9, Title: "a|b\nc", Kind: "task", UpdatedAt: up},
	})
	if !strings.Contains(md, "| a\\|b c |") {
		t.Errorf("竖线/换行未转义\n实际输出:\n%s", md)
	}
}
