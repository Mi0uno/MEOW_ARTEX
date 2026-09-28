package db

// FilterExportedReportIDs(报告「已导出过」判定)的 PG 集成测试。跑在显式指定的
// 演示库(ARTEX_PG_DSN,如 5499 集群)上,绝不回退 config.json(见 testmain_test.go 门禁)。
// 纪律同 recon_monitor_test:自建行 t.Cleanup 全部清理——report_events/report_jobs
// 对 report_id 无级联,三处(事件/作业/报告)都要显式删,共享演示库不留垃圾。

import (
	"slices"
	"testing"
)

func TestFilterExportedReportIDs(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	// 报告 A:走事件路径(exported 事件)。
	repA, err := d.CreateReport("export-check-it-事件路径", "", "正文")
	if err != nil {
		t.Fatalf("CreateReport: %v", err)
	}
	// 报告 B:走作业路径(done 的导出作业)。
	repB, err := d.CreateReport("export-check-it-作业路径", "", "正文")
	if err != nil {
		t.Fatalf("CreateReport: %v", err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM report_events WHERE report_id IN ($1, $2)`, repA.ID, repB.ID)
		_, _ = d.Exec(`DELETE FROM report_jobs WHERE report_id IN ($1, $2)`, repA.ID, repB.ID)
		_, _ = d.DeleteReport(repA.ID)
		_, _ = d.DeleteReport(repB.ID)
	})

	ghost := int64(999999999) // 不存在的 id:任何分支都不该命中

	// 1) 无任何导出痕迹 → 空。
	got, err := d.FilterExportedReportIDs([]int64{repA.ID, repB.ID, ghost})
	if err != nil {
		t.Fatalf("FilterExportedReportIDs(无痕迹): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("无导出痕迹却命中: %v", got)
	}

	// 2) 事件路径:exported 事件命中 A,不命中 B/幽灵 id。
	if err := d.AddReportEvent(&repA.ID, nil, nil, "exported", "user", nil); err != nil {
		t.Fatalf("AddReportEvent: %v", err)
	}
	got, err = d.FilterExportedReportIDs([]int64{repA.ID, repB.ID, ghost})
	if err != nil {
		t.Fatalf("FilterExportedReportIDs(事件): %v", err)
	}
	if !slices.Equal(got, []int64{repA.ID}) {
		t.Fatalf("事件路径期望 [%d],得到 %v", repA.ID, got)
	}

	// 3) 作业路径:done 的 export 作业命中 B;running 的不算。
	job, err := d.CreateReportJob("export", repB.ID, 0, 0, "md")
	if err != nil {
		t.Fatalf("CreateReportJob: %v", err)
	}
	got, err = d.FilterExportedReportIDs([]int64{repB.ID})
	if err != nil {
		t.Fatalf("FilterExportedReportIDs(running 作业): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("running 作业不应算已导出: %v", got)
	}
	if err := d.SetReportJobDone(job.ID, ""); err != nil {
		t.Fatalf("SetReportJobDone: %v", err)
	}
	got, err = d.FilterExportedReportIDs([]int64{repA.ID, repB.ID, ghost})
	if err != nil {
		t.Fatalf("FilterExportedReportIDs(作业): %v", err)
	}
	if !slices.Equal(got, []int64{repA.ID, repB.ID}) {
		t.Fatalf("事件∪作业期望 [%d %d],得到 %v", repA.ID, repB.ID, got)
	}

	// 4) 空 ids → 空结果且不报错。
	got, err = d.FilterExportedReportIDs(nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("空 ids 期望空结果,得到 %v (err=%v)", got, err)
	}
}
