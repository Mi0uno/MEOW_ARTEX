package db

import (
	"fmt"
	"testing"
	"time"
)

// TestListStaleVpsAssignedMachinesOnlyTerminal 锁死开机对账判据:只有「任务行已消失」
// 或「任务已到终态(done/failed/timeout)」的指派才算悬空;created/running/paused 的
// 指派必须保留。判据一旦写反(比如「非 running/paused 就算悬空」),重启就会把
// created/排队中任务的机器放掉——任务开跑掉回本机执行,正是「掉马甲」回归的形态。
func TestListStaleVpsAssignedMachinesOnlyTerminal(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	statuses := []string{"created", "running", "paused", "done", "failed", "timeout"}
	keep := map[int64]bool{}    // machine ids that must NOT be released
	stale := map[int64]bool{}   // machine ids that MUST be reported stale
	for _, st := range statuses {
		tk, err := d.CreateTask("vps 对账测试 "+st, "goal", nil, 0, 0)
		if err != nil {
			t.Fatalf("建任务(%s)失败: %v", st, err)
		}
		m, err := d.CreateVpsMachine(VpsMachineInput{
			Name:               fmt.Sprintf("reconcile-test-%s-%d", st, tk.ID),
			Host:               "203.0.113.10",
			Username:           "root",
			AuthType:           VpsAuthKey,
			PrivateKeySealed:   []byte("test-key"),
		})
		if err != nil {
			t.Fatalf("建机器(%s)失败: %v", st, err)
		}
		// created 是任务的默认初始状态;其余状态显式迁移。
		if st != "created" {
			if err := d.SetStatus(tk.ID, st); err != nil {
				t.Fatalf("置任务状态 %s 失败: %v", st, err)
			}
		}
		if err := d.AssignVpsMachineToTask(m.ID, tk.ID, "test"); err != nil {
			t.Fatalf("指派(%s)失败: %v", st, err)
		}
		if st == "done" || st == "failed" || st == "timeout" {
			stale[m.ID] = true
		} else {
			keep[m.ID] = true
		}
		defer func() {
			// 先解绑再删任务:assigned_task_id 无外键,但留着悬空行会污染后续用例。
			d.Exec(`DELETE FROM vps_assignments WHERE machine_id=$1`, m.ID) //nolint:errcheck
			d.Exec(`DELETE FROM vps_machines WHERE id=$1`, m.ID)            //nolint:errcheck
			d.DeleteTask(tk.ID)                                             //nolint:errcheck
		}()
	}

	// 指向不存在任务行的机器:悬空的教科书形态(任务被硬删/归档清行)。
	ghost, err := d.CreateVpsMachine(VpsMachineInput{
		Name:             fmt.Sprintf("reconcile-test-ghost-%d", time.Now().UnixNano()),
		Host:             "203.0.113.10",
		Username:         "root",
		AuthType:         VpsAuthKey,
		PrivateKeySealed: []byte("test-key"),
	})
	if err != nil {
		t.Fatalf("建 ghost 机器失败: %v", err)
	}
	if err := d.AssignVpsMachineToTask(ghost.ID, 987654321, "test"); err != nil {
		t.Fatalf("指派 ghost 失败: %v", err)
	}
	stale[ghost.ID] = true
	defer func() {
		d.Exec(`DELETE FROM vps_assignments WHERE machine_id=$1`, ghost.ID) //nolint:errcheck
		d.Exec(`DELETE FROM vps_machines WHERE id=$1`, ghost.ID)            //nolint:errcheck
	}()

	staleRows, err := d.ListStaleVpsAssignedMachines()
	if err != nil {
		t.Fatalf("读取悬空指派失败: %v", err)
	}
	got := map[int64]bool{}
	for _, m := range staleRows {
		got[m.ID] = true
	}
	for id := range stale {
		if !got[id] {
			t.Fatalf("机器 #%d(任务终态或已消失)应判为悬空", id)
		}
	}
	for id := range keep {
		if got[id] {
			t.Fatalf("机器 #%d(任务 created/running/paused)不应判为悬空——判据写反会导致重启掉马甲", id)
		}
	}
}
