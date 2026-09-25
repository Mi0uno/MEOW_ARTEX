package db

// commands.go 删除路径的 PG 集成测试。照既有 db 测试模式:ARTEX_PG_DSN 未设置
// 自动跳过;cleanup 用 defer 而不是 t.Cleanup(连接已关时 t.Cleanup 静默失败)。

import (
	"testing"
)

func TestDeleteLLMRecordsByIDs(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	if err := d.EnsureLLMRecordsTable(); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	const task = "delete-by-ids-test"
	defer func() { _, _ = d.Exec(`DELETE FROM llm_records WHERE COALESCE(task_id,'') = $1`, task) }()
	ids := make([]int64, 0, 3)
	for i := 0; i < 3; i++ {
		var id int64
		if err := d.QueryRow(
			`INSERT INTO llm_records(model, task_id, status) VALUES ('m-test', $1, 'ok') RETURNING id`, task,
		).Scan(&id); err != nil {
			t.Fatalf("insert: %v", err)
		}
		ids = append(ids, id)
	}

	// 按 id 批量删 2 条(ids[0]/ids[2]),中间一条保留
	n, err := d.DeleteLLMRecordsByIDs([]int64{ids[0], ids[2]})
	if err != nil {
		t.Fatalf("delete by ids: %v", err)
	}
	if n != 2 {
		t.Fatalf("deleted = %d, want 2", n)
	}
	var left int
	if err := d.QueryRow(`SELECT COUNT(*) FROM llm_records WHERE COALESCE(task_id,'') = $1`, task).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 1 {
		t.Fatalf("left = %d, want 1", left)
	}

	// 空 ids:no-op(0, nil)
	if n, err := d.DeleteLLMRecordsByIDs(nil); err != nil || n != 0 {
		t.Fatalf("empty ids delete = (%d, %v), want (0, nil)", n, err)
	}

	// 单删:前端传一个 id 的语义
	if n, err := d.DeleteLLMRecordsByIDs(ids[1:]); err != nil || n != 1 {
		t.Fatalf("single delete = (%d, %v), want (1, nil)", n, err)
	}
	var gone int
	if err := d.QueryRow(`SELECT COUNT(*) FROM llm_records WHERE COALESCE(task_id,'') = $1`, task).Scan(&gone); err != nil {
		t.Fatal(err)
	}
	if gone != 0 {
		t.Fatalf("after single delete left = %d, want 0", gone)
	}
}

func TestDeleteCommands(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	// activity.exploration_id NOT NULL REFERENCES explorations,先造一个真实
	// exploration;删除它时 activity 级联清掉,是兜底 cleanup。
	var expID int64
	if err := d.QueryRow(
		`INSERT INTO explorations(description, goal) VALUES ('commands delete test', 'test') RETURNING id`,
	).Scan(&expID); err != nil {
		t.Fatalf("insert exploration: %v", err)
	}
	defer func() { _, _ = d.Exec(`DELETE FROM explorations WHERE id = $1`, expID) }()

	insertAct := func(kind, tool, toolUseID, detail string) int64 {
		t.Helper()
		var id int64
		if err := d.QueryRow(
			`INSERT INTO activity(exploration_id, kind, tool, tool_use_id, detail) VALUES ($1,$2,$3,$4,$5) RETURNING id`,
			expID, kind, tool, toolUseID, detail,
		).Scan(&id); err != nil {
			t.Fatalf("insert activity: %v", err)
		}
		return id
	}
	u1 := insertAct("tool_use", "Bash", "tu-del-1", "echo one")
	r1 := insertAct("tool_result", "", "tu-del-1", "one")
	u2 := insertAct("tool_use", "Read", "tu-del-2", "read file")
	_ = insertAct("tool_result", "", "tu-del-2", "contents")
	u3 := insertAct("tool_use", "Edit", "tu-del-3", "edit file")
	r3 := insertAct("tool_result", "", "tu-del-3", "ok")

	// 空 ids:no-op(0, nil)
	if n, err := d.DeleteCommands(nil); err != nil || n != 0 {
		t.Fatalf("empty ids delete = (%d, %v), want (0, nil)", n, err)
	}

	// 删 u1/u3:tool_use 计数 2,配对的 tool_result 一并删除,u2/r2 保留
	n, err := d.DeleteCommands([]int64{u1, u3})
	if err != nil {
		t.Fatalf("delete commands: %v", err)
	}
	if n != 2 {
		t.Fatalf("deleted = %d, want 2", n)
	}
	for _, id := range []int64{r1, r3} {
		var cnt int
		if err := d.QueryRow(`SELECT COUNT(*) FROM activity WHERE id = $1`, id).Scan(&cnt); err != nil {
			t.Fatal(err)
		}
		if cnt != 0 {
			t.Fatalf("paired tool_result %d should be removed", id)
		}
	}
	var leftUses, leftResults int
	if err := d.QueryRow(`SELECT COUNT(*) FROM activity WHERE exploration_id = $1 AND kind = 'tool_use'`, expID).Scan(&leftUses); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM activity WHERE exploration_id = $1 AND kind = 'tool_result'`, expID).Scan(&leftResults); err != nil {
		t.Fatal(err)
	}
	if leftUses != 1 || leftResults != 1 {
		t.Fatalf("left tool_use/tool_result = %d/%d, want 1/1", leftUses, leftResults)
	}

	// 幸存记录仍能被 ListCommands 正常 join 出来
	records, total, err := d.ListCommands(&expID, "", 0, 50)
	if err != nil {
		t.Fatalf("list commands: %v", err)
	}
	if total != 1 || len(records) != 1 || records[0].ID != u2 || records[0].Output != "contents" {
		t.Fatalf("survivor list = (total=%d, %+v), want u2 paired with its result", total, records)
	}
}
