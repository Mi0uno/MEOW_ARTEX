package db

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// c2TestEvasionProfile 落一份测试免杀档案。
func c2TestEvasionProfile(t *testing.T, d *DB, name string) *C2EvasionProfile {
	t.Helper()
	id, err := d.CreateC2EvasionProfile(C2EvasionProfileInput{
		Name:          name,
		TargetAV:      C2EvasionTargetDefender,
		PayloadFormat: C2PayloadFormatPs1,
		Techniques:    json.RawMessage(`{"string_split":true,"junk_code":"medium"}`),
		Instructions:  "重点拆分 DownloadString 相关字符串,保留信道行为",
		Source:        C2EvasionSourceManual,
		Notes:         "免杀访问层测试档案",
	})
	if err != nil {
		t.Fatalf("建档失败: %v", err)
	}
	p, err := d.GetC2EvasionProfile(id)
	if err != nil || p == nil {
		t.Fatalf("回读档案失败: %v(%v)", p, err)
	}
	return p
}

// TestC2EvasionProfiles 档案 CRUD + target_av/payload_format 过滤 + 枚举白名单。
func TestC2EvasionProfiles(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	p := c2TestEvasionProfile(t, d, fmt.Sprintf("免杀档案甲-%d", time.Now().UnixNano()))
	defer d.DeleteC2EvasionProfile(p.ID) //nolint:errcheck

	if p.TargetAV != C2EvasionTargetDefender || p.PayloadFormat != C2PayloadFormatPs1 ||
		p.Source != C2EvasionSourceManual {
		t.Fatalf("档案默认值不对: %+v", p)
	}
	var tech map[string]any
	if err := json.Unmarshal(p.Techniques, &tech); err != nil || tech["string_split"] != true {
		t.Fatalf("techniques 回读不对: %s (%v)", p.Techniques, err)
	}

	// 整体覆盖更新。
	if err := d.UpdateC2EvasionProfile(p.ID, C2EvasionProfileInput{
		Name: p.Name + "-v2", TargetAV: C2EvasionTargetHuorong,
		PayloadFormat: C2PayloadFormatPy, Instructions: "改为火绒环境", Source: C2EvasionSourceLLM,
	}); err != nil {
		t.Fatalf("更新档案失败: %v", err)
	}
	got, _ := d.GetC2EvasionProfile(p.ID)
	if got.TargetAV != C2EvasionTargetHuorong || got.PayloadFormat != C2PayloadFormatPy ||
		got.Source != C2EvasionSourceLLM {
		t.Fatalf("更新未生效: %+v", got)
	}
	// 更新未带 techniques = 归一 "{}"(整体覆盖语义)。
	if string(got.Techniques) != "{}" {
		t.Fatalf("缺省 techniques 应归一 {}: %s", got.Techniques)
	}

	// 枚举白名单拒绝。
	if err := d.UpdateC2EvasionProfile(p.ID, C2EvasionProfileInput{
		Name: "x", TargetAV: "mcafee", PayloadFormat: C2PayloadFormatPs1,
	}); err == nil {
		t.Fatalf("非法目标 AV 应被拒绝")
	}
	if _, err := d.CreateC2EvasionProfile(C2EvasionProfileInput{
		Name: "x", TargetAV: C2EvasionTargetGeneric, PayloadFormat: "exe",
	}); err == nil {
		t.Fatalf("非法载荷格式应被拒绝")
	}
	if _, err := d.CreateC2EvasionProfile(C2EvasionProfileInput{
		Name: "x", TargetAV: C2EvasionTargetGeneric, PayloadFormat: C2PayloadFormatPs1,
		Techniques: json.RawMessage(`{not-json`),
	}); err == nil {
		t.Fatalf("非法 techniques JSON 应被拒绝")
	}
	if _, err := d.CreateC2EvasionProfile(C2EvasionProfileInput{Name: " "}); err == nil {
		t.Fatalf("空名称应被拒绝")
	}

	// 列表 + 过滤。
	if _, total, err := d.ListC2EvasionProfiles(C2EvasionProfileFilter{
		TargetAV: C2EvasionTargetHuorong, Limit: 10,
	}); err != nil || total < 1 {
		t.Fatalf("按目标 AV 过滤失败: total=%d (%v)", total, err)
	}
	if _, total, err := d.ListC2EvasionProfiles(C2EvasionProfileFilter{
		PayloadFormat: C2PayloadFormatPy, Limit: 10,
	}); err != nil || total < 1 {
		t.Fatalf("按载荷格式过滤失败: total=%d (%v)", total, err)
	}
	if _, _, err := d.ListC2EvasionProfiles(C2EvasionProfileFilter{TargetAV: "bogus"}); err == nil {
		t.Fatalf("非法目标 AV 筛选应拒绝")
	}
	if p0, err := d.GetC2EvasionProfile(0); err != nil || p0 != nil {
		t.Fatalf("id=0 应返回 nil,nil: %v(%v)", p0, err)
	}

	deleted, err := d.DeleteC2EvasionProfile(p.ID)
	if err != nil || !deleted {
		t.Fatalf("删除失败: %v(%v)", deleted, err)
	}
	if got, _ := d.GetC2EvasionProfile(p.ID); got != nil {
		t.Fatalf("删除后仍能读到")
	}
}

// TestC2EvasionJobs 作业生命周期:防抢占 CAS/终态回填/列表过滤/防重入计数/启动恢复。
func TestC2EvasionJobs(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	p := c2TestEvasionProfile(t, d, fmt.Sprintf("作业档案-%d", time.Now().UnixNano()))
	defer d.DeleteC2EvasionProfile(p.ID) //nolint:errcheck
	l := c2TestListener(t, d, fmt.Sprintf("c2-jobs-%d", time.Now().UnixNano()))
	defer d.DeleteC2Listener(l.ID) //nolint:errcheck

	input := json.RawMessage(fmt.Sprintf(`{"listener_id":%d,"format":"ps1","note":"变形请求甲"}`, l.ID))
	jobID, err := d.CreateC2Job(C2JobInput{
		Kind: C2JobKindEvasion, ProfileID: p.ID, ListenerID: l.ID, Input: input,
	})
	if err != nil {
		t.Fatalf("建作业失败: %v", err)
	}
	job, err := d.GetC2Job(jobID)
	if err != nil || job == nil {
		t.Fatalf("回读作业失败: %v(%v)", job, err)
	}
	if job.Status != C2JobStatusQueued || job.Kind != C2JobKindEvasion ||
		job.ProfileID != p.ID || job.ListenerID != l.ID {
		t.Fatalf("作业默认值不对: %+v", job)
	}
	if string(job.Input) != string(input) {
		t.Fatalf("input 原样回读不对: %s vs %s", job.Input, input)
	}
	if string(job.Result) != "{}" {
		t.Fatalf("result 空值应归一 {}: %s", job.Result)
	}

	// 防重入计数:running 前 0,take 后 1。
	if n, err := d.CountRunningC2Jobs(C2JobKindEvasion, p.ID, l.ID); err != nil || n != 0 {
		t.Fatalf("初始 running 计数应为 0: %d (%v)", n, err)
	}
	ok, err := d.TakeC2Job(jobID)
	if err != nil || !ok {
		t.Fatalf("首次领取应成功: %v(%v)", ok, err)
	}
	// 防抢占:第二次领取必须失败(queued→running 条件更新)。
	if ok2, err := d.TakeC2Job(jobID); err != nil || ok2 {
		t.Fatalf("二次领取应被拒绝: %v(%v)", ok2, err)
	}
	if n, _ := d.CountRunningC2Jobs(C2JobKindEvasion, p.ID, l.ID); n != 1 {
		t.Fatalf("running 计数应为 1: %d", n)
	}
	// 未挂档案的临时作业按 profile_id IS NULL 匹配,不与有档案作业互串。
	tmpID, err := d.CreateC2Job(C2JobInput{Kind: C2JobKindEvasion, ListenerID: l.ID, Input: input})
	if err != nil {
		t.Fatalf("建无档案作业失败: %v", err)
	}
	defer d.FinishC2Job(tmpID, C2JobStatusError, nil, "测试清理") //nolint:errcheck
	if n, _ := d.CountRunningC2Jobs(C2JobKindEvasion, 0, l.ID); n != 0 {
		t.Fatalf("无档案计数不应串到有档案作业: %d", n)
	}

	// 终态回填:result JSON + done_at 盖章 + 非终态拒绝。
	result := json.RawMessage(`{"static":{"hits":[],"entropy":4.2,"risk":"low"},"rounds":1}`)
	if err := d.FinishC2Job(jobID, C2JobStatusDone, result, ""); err != nil {
		t.Fatalf("回填终态失败: %v", err)
	}
	if job, _ = d.GetC2Job(jobID); string(job.Result) != string(result) || job.DoneAt == nil {
		t.Fatalf("终态回填未生效: %+v", job)
	}
	if err := d.FinishC2Job(jobID, C2JobStatusQueued, nil, ""); err == nil {
		t.Fatalf("非终态应被拒绝")
	}

	// 列表过滤:kind/status/listener + total。
	if _, total, err := d.ListC2Jobs(C2JobFilter{Kind: C2JobKindEvasion, Status: C2JobStatusDone, ListenerID: l.ID}); err != nil || total < 1 {
		t.Fatalf("按 kind/status/listener 过滤失败: total=%d (%v)", total, err)
	}
	if _, _, err := d.ListC2Jobs(C2JobFilter{Status: "zombie"}); err == nil {
		t.Fatalf("非法状态筛选应拒绝")
	}
	if _, _, err := d.ListC2Jobs(C2JobFilter{Kind: "deploy"}); err == nil {
		t.Fatalf("非法类型筛选应拒绝")
	}

	// 启动恢复:把临时作业拨到 running(模拟进程崩溃遗留),ResetStuckC2Jobs 应把它
	// 拨回 queued 并出现在 ListOpenC2Jobs;随后置 error 收尾。
	if _, err := d.Exec(`UPDATE c2_jobs SET status='running' WHERE id=$1`, tmpID); err != nil {
		t.Fatalf("模拟 running 失败: %v", err)
	}
	if n, err := d.ResetStuckC2Jobs(C2JobKindEvasion); err != nil || n < 1 {
		t.Fatalf("拨回 running 作业失败: %d (%v)", n, err)
	}
	found := false
	open, err := d.ListOpenC2Jobs(C2JobKindEvasion)
	if err != nil {
		t.Fatalf("列 open 作业失败: %v", err)
	}
	for _, j := range open {
		if j.ID == tmpID && j.Status == C2JobStatusQueued {
			found = true
		}
	}
	if !found {
		t.Fatalf("拨回后的作业应出现在 open 列表")
	}
}

// TestC2EvasionBackfills 二期回填列:payloads.evasion_profile_id 与 loot.finding_id
// (条件更新防重复转换)。
func TestC2EvasionBackfills(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	p := c2TestEvasionProfile(t, d, fmt.Sprintf("回填档案-%d", time.Now().UnixNano()))
	defer d.DeleteC2EvasionProfile(p.ID) //nolint:errcheck
	l := c2TestListener(t, d, fmt.Sprintf("c2-backfill-%d", time.Now().UnixNano()))
	defer d.DeleteC2Listener(l.ID) //nolint:errcheck

	payloadID, err := d.CreateC2Payload(C2PayloadInput{
		ListenerID: l.ID, Name: "回填载荷", Format: C2PayloadFormatPs1, Target: "windows",
		Token: fmt.Sprintf("tok-%d", time.Now().UnixNano()), CreatedBy: "c2_evasion_test",
	})
	if err != nil {
		t.Fatalf("建载荷失败: %v", err)
	}
	defer d.DeleteC2Payload(payloadID) //nolint:errcheck
	if err := d.SetC2PayloadEvasionProfile(payloadID, p.ID); err != nil {
		t.Fatalf("回填档案失败: %v", err)
	}
	var got any
	if err := d.QueryRow(`SELECT evasion_profile_id FROM c2_payloads WHERE id=$1`, payloadID).Scan(&got); err != nil {
		t.Fatalf("回读 evasion_profile_id 失败: %v", err)
	}
	if got == nil || got.(int64) != p.ID {
		t.Fatalf("evasion_profile_id 回填不对: %v", got)
	}
	if err := d.SetC2PayloadEvasionProfile(payloadID, 0); err != nil {
		t.Fatalf("置空档案(0→NULL)失败: %v", err)
	}
	if err := d.QueryRow(`SELECT evasion_profile_id FROM c2_payloads WHERE id=$1`, payloadID).Scan(&got); err != nil {
		t.Fatalf("回读 evasion_profile_id 失败: %v", err)
	}
	if got != nil {
		t.Fatalf("0 应置 NULL: %v", got)
	}

	// 战利品需要挂在会话上。
	uid := fmt.Sprintf("uid-%d", time.Now().UnixNano())
	sessionID, err := d.UpsertC2Session(C2SessionInput{
		ListenerID: l.ID, BeaconUID: uid, SID: "sid-" + uid, Hostname: "回填主机",
	})
	if err != nil {
		t.Fatalf("建会话失败: %v", err)
	}
	defer d.Exec(`DELETE FROM c2_sessions WHERE listener_id=$1`, l.ID) //nolint:errcheck
	lootID, err := d.CreateC2Loot(C2LootInput{
		SessionID: sessionID, Kind: C2LootKindCredential, Name: "回填凭据", Content: "sealed",
	})
	if err != nil {
		t.Fatalf("建档失败: %v", err)
	}
	defer d.DeleteC2Loot(lootID) //nolint:errcheck

	// 首次回填成功;重复回填被条件更新拒绝(重复转换 409 的依据)。
	ok, err := d.SetC2LootFinding(lootID, 8101)
	if err != nil || !ok {
		t.Fatalf("首次回填 finding_id 应成功: %v(%v)", ok, err)
	}
	if ok2, err := d.SetC2LootFinding(lootID, 8102); err != nil || ok2 {
		t.Fatalf("重复回填应被拒绝: %v(%v)", ok2, err)
	}
	if lo, _ := d.GetC2Loot(lootID); lo == nil || lo.FindingID != 8101 {
		t.Fatalf("finding_id 回读不对: %+v", lo)
	}
}
