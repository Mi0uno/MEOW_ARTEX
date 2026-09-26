package server

// §24A(三期补丁 A v3.1)载荷生成联动免杀的服务端测试:
//   - script + 档案 → 走共享 helper(startC2EvasionJob)建变形作业,响应 202
//     {job_id}(无 id/payload 字段);作业与 /api/c2/evasion/generate 路径同构
//     (kind/profile/listener/task 四要素逐字段一致);
//   - 编译型产物 + 档案 → 400 引导文案(§24A 逐字);
//   - 档案格式不一致 / 档案不存在 → 400。
//
// PG 集成测试:需 ARTEX_PG_DSN 显式指向隔离库(如 5499),未设置按既有惯例跳过。

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	pgdb "github.com/Autumn-27/artex/db"
)

// c2PatchTestEnv 是 §24A/§24B 共用的测试环境:隔离库 + 最小 Server + 密封盒。
type c2PatchTestEnv struct {
	s  *Server
	pg *pgdb.DB
}

// newC2PatchTestEnv 组装环境(NewManager 连库失败自动跳过;不跑 New() 的全套
// init——本组测试只需要 pg/密封盒/共享 helper/全局 C2 Manager 注册点)。
func newC2PatchTestEnv(t *testing.T) *c2PatchTestEnv {
	t.Helper()
	if _, err := testDSN(); err != nil {
		t.Skipf("ARTEX_PG_DSN 未设置,跳过 PG 测试(%v)", err)
	}
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	box, err := newAPISecretBox(m.dir)
	if err != nil {
		t.Fatalf("初始化凭据密封盒失败: %v", err)
	}
	// 最小 Server 字面量:补上 New() 会初始化的 provider 缓存与健康注册表,
	// 防后台变形作业 goroutine 触碰 nil map(生产 Server 恒有,测试字面量需自备)。
	s := &Server{m: m, ctx: context.Background(), apiSecret: box,
		provByProfile: map[int64]*provEntry{}, llmHealth: newLLMHealthRegistry(m.PG())}
	return &c2PatchTestEnv{s: s, pg: m.PG()}
}

// createC2TestListener 建一条测试监听器行(随机 32B key 密封入库)。
func (e *c2PatchTestEnv) createC2TestListener(t *testing.T, name, proto string, mutate func(*pgdb.C2ListenerInput)) int64 {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("生成测试密钥失败: %v", err)
	}
	sealed, err := e.s.sealC2Secret(key)
	if err != nil {
		t.Fatalf("密封测试密钥失败: %v", err)
	}
	in := pgdb.C2ListenerInput{
		Name: name, BindHost: "127.0.0.1", Port: 0, URLPath: "c2pt",
		Callback: "http://127.0.0.1:1", EncKeySealed: sealed, Proto: proto,
	}
	if mutate != nil {
		mutate(&in)
	}
	id, err := e.pg.CreateC2Listener(in)
	if err != nil {
		t.Fatalf("建测试监听器失败: %v", err)
	}
	return id
}

// createC2TestProfile 建一份测试免杀档案。
func (e *c2PatchTestEnv) createC2TestProfile(t *testing.T, name, format string) int64 {
	t.Helper()
	id, err := e.pg.CreateC2EvasionProfile(pgdb.C2EvasionProfileInput{
		Name: name, TargetAV: pgdb.C2EvasionTargetDefender, PayloadFormat: format,
		Techniques: json.RawMessage(`{"kb_keys":[]}`), Instructions: "测试附加指示", Source: "manual",
	})
	if err != nil {
		t.Fatalf("建测试档案失败: %v", err)
	}
	return id
}

// postC2Payload 直接打 createC2Payload handler。
func (e *c2PatchTestEnv) postC2Payload(t *testing.T, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("编码请求失败: %v", err)
	}
	rec := httptest.NewRecorder()
	e.s.createC2Payload(rec, httptest.NewRequest(http.MethodPost, "/api/c2/payloads", bytes.NewReader(buf)))
	return rec
}

// cleanupC2EvasionArtifacts 按 id 逆序清掉变形作业链路产生的行(无 FK,先删子表;
// 作业可能异步失败留下任务/会话行)。先取消再等作业进终态,避免后台 runner 与
// 删除竞态(失败收尾要写监看会话,先删会话会撞 FK)。
func (e *c2PatchTestEnv) cleanupC2EvasionArtifacts(t *testing.T, listenerIDs, profileIDs []int64) {
	t.Helper()
	for _, lid := range listenerIDs {
		rows, err := e.pg.Query(`SELECT id, task_id FROM c2_jobs WHERE listener_id=$1`, lid)
		if err != nil {
			continue
		}
		var jobIDs, taskIDs []int64
		for rows.Next() {
			var jid, tid int64
			_ = rows.Scan(&jid, &tid)
			jobIDs = append(jobIDs, jid)
			if tid > 0 {
				taskIDs = append(taskIDs, tid)
			}
		}
		rows.Close()
		for _, jid := range jobIDs { // 取消(若仍在跑)并等进终态(有界,防悬挂)
			c2EvRunner.mu.Lock()
			cancel := c2EvRunner.cancels[jid]
			c2EvRunner.mu.Unlock()
			if cancel != nil {
				cancel()
			}
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				j, err := e.pg.GetC2Job(jid)
				if err != nil || j == nil || j.Status == pgdb.C2JobStatusDone || j.Status == pgdb.C2JobStatusError {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			_, _ = e.pg.Exec(`DELETE FROM c2_jobs WHERE id=$1`, jid)
		}
		for _, tid := range taskIDs {
			e.pg.Exec(`DELETE FROM conversations WHERE id = COALESCE((SELECT (step_config::jsonb->>'conv_id')::bigint FROM tasks WHERE id=$1 AND step_config IS NOT NULL), -1)`, tid) //nolint:errcheck
			e.pg.Exec(`DELETE FROM tasks WHERE id=$1`, tid)                                                                                                                            //nolint:errcheck
		}
		_, _ = e.pg.Exec(`DELETE FROM c2_payloads WHERE listener_id=$1`, lid)
		_, _ = e.pg.Exec(`DELETE FROM c2_listeners WHERE id=$1`, lid)
	}
	for _, pid := range profileIDs {
		e.pg.Exec(`DELETE FROM c2_evasion_profiles WHERE id=$1`, pid) //nolint:errcheck
	}
}

// suppressDefaultLLMProfile 暂时屏蔽共享库里的默认 LLM 档案(is_default):
// 变形作业的后台 runner 会真调 LLM(钱/时延/非确定),本组测试只断言作业链路;
// 屏蔽后作业按既有语义快速失败(「未配置 LLM」),测试完恢复原状。
func (e *c2PatchTestEnv) suppressDefaultLLMProfile(t *testing.T) {
	t.Helper()
	var ids []int64
	rows, err := e.pg.Query(`SELECT id FROM llm_profiles WHERE is_default`)
	if err != nil {
		t.Fatalf("查询默认 LLM 档案失败: %v", err)
	}
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) == 0 {
		return
	}
	if _, err := e.pg.Exec(`UPDATE llm_profiles SET is_default=false WHERE is_default`); err != nil {
		t.Fatalf("屏蔽默认 LLM 档案失败: %v", err)
	}
	t.Cleanup(func() {
		for _, id := range ids {
			e.pg.Exec(`UPDATE llm_profiles SET is_default=true WHERE id=$1`, id) //nolint:errcheck
		}
	})
}

// TestC2PayloadEvasionScriptCreatesJob §24A 主路径:script+档案 → 202 {job_id},
// 作业建立且与 generate 路径(直接调共享 helper)同构。
func TestC2PayloadEvasionScriptCreatesJob(t *testing.T) {
	e := newC2PatchTestEnv(t)
	e.suppressDefaultLLMProfile(t)
	lid := e.createC2TestListener(t, "c2atest-l1", "http", nil)
	pid := e.createC2TestProfile(t, "c2atest-prof-ps1", "ps1")
	lid2 := e.createC2TestListener(t, "c2atest-l2", "http", nil) // 同构对照用(不同监听器,互不触发防重入)
	t.Cleanup(func() { e.cleanupC2EvasionArtifacts(t, []int64{lid, lid2}, []int64{pid}) })

	// 1) 载荷生成端点携带档案 → 202 {job_id},且无 id/payload 字段。
	rec := e.postC2Payload(t, map[string]any{
		"listener_id": lid, "format": "ps1", "artifact": "script",
		"direction": "reverse", "target": "windows", "name": "c2atest-job",
		"evasion_profile_id": pid,
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202(body=%s)", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	jobIDF, ok := resp["job_id"].(float64)
	if !ok || jobIDF <= 0 {
		t.Fatalf("响应缺 job_id: %v", resp)
	}
	for _, banned := range []string{"id", "payload", "oneliner"} {
		if _, exists := resp[banned]; exists {
			t.Fatalf("响应不应携带 %q 字段(前端按作业轮询): %v", banned, resp)
		}
	}
	jobID := int64(jobIDF)
	job, err := e.pg.GetC2Job(jobID)
	if err != nil || job == nil {
		t.Fatalf("作业 #%d 读取失败: %v(%v)", jobID, job, err)
	}
	if job.Kind != pgdb.C2JobKindEvasion || job.ProfileID != pid || job.ListenerID != lid {
		t.Fatalf("作业四要素不符: kind=%s profile=%d listener=%d(want evasion/%d/%d)",
			job.Kind, job.ProfileID, job.ListenerID, pid, lid)
	}
	if job.TaskID <= 0 {
		t.Fatalf("作业 #%d 未挂监看任务(task_id=%d)", jobID, job.TaskID)
	}
	var in c2EvasionJobInput
	if err := json.Unmarshal(job.Input, &in); err != nil {
		t.Fatalf("作业入参解析失败: %v", err)
	}
	if in.Format != "ps1" || in.Target != "windows" || in.ProfileID != pid || in.Name != "c2atest-job" {
		t.Fatalf("作业入参不符: %+v", in)
	}

	// 2) 同构对照:generate 路径 = 直接调共享 helper(同入参,换监听器防重入)。
	genJobID, genTaskID, err := e.s.startC2EvasionJob(c2EvasionJobInput{
		ListenerID: lid2, Format: "ps1", Target: "windows", Name: "c2atest-job", ProfileID: pid,
	})
	if err != nil {
		t.Fatalf("共享 helper 直调失败: %v", err)
	}
	if genJobID <= 0 || genTaskID <= 0 {
		t.Fatalf("共享 helper 返回异常: job=%d task=%d", genJobID, genTaskID)
	}
	genJob, err := e.pg.GetC2Job(genJobID)
	if err != nil || genJob == nil {
		t.Fatalf("对照作业 #%d 读取失败: %v(%v)", genJobID, genJob, err)
	}
	var genIn c2EvasionJobInput
	_ = json.Unmarshal(genJob.Input, &genIn)
	in.ListenerID, genIn.ListenerID = 0, 0 // 监听器是两路径唯一的入参差异
	if fmt.Sprintf("%+v", in) != fmt.Sprintf("%+v", genIn) {
		t.Fatalf("两路径作业入参不同构: payload=%+v generate=%+v", in, genIn)
	}
	if genJob.Kind != job.Kind || genJob.ProfileID != job.ProfileID || genJob.TaskID <= 0 {
		t.Fatalf("两路径作业行不同构: payload=(%s/%d/%d) generate=(%s/%d/%d)",
			job.Kind, job.ProfileID, job.TaskID, genJob.Kind, genJob.ProfileID, genJob.TaskID)
	}
}

// TestC2PayloadEvasionCompiledRejected 编译型产物 + 档案 → 400 §24A 引导文案。
func TestC2PayloadEvasionCompiledRejected(t *testing.T) {
	e := newC2PatchTestEnv(t)
	lid := e.createC2TestListener(t, "c2atest-l3", "http", nil)
	pid := e.createC2TestProfile(t, "c2atest-prof-compiled", "ps1")
	t.Cleanup(func() { e.cleanupC2EvasionArtifacts(t, []int64{lid}, []int64{pid}) })

	for _, artifact := range []string{"exe", "dll", "shellcode", "ebpf"} {
		rec := e.postC2Payload(t, map[string]any{
			"listener_id": lid, "format": "gosrc", "artifact": artifact,
			"direction": "reverse", "target": "windows", "evasion_profile_id": pid,
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400(body=%s)", artifact, rec.Code, rec.Body.String())
		}
		if !bytes.Contains(rec.Body.Bytes(), []byte("编译型产物暂不支持档案变形")) {
			t.Fatalf("%s: 400 文案缺引导语: %s", artifact, rec.Body.String())
		}
	}
}

// TestC2PayloadEvasionProfileChecks 档案校验:格式不一致 → 400;档案不存在 → 400。
func TestC2PayloadEvasionProfileChecks(t *testing.T) {
	e := newC2PatchTestEnv(t)
	lid := e.createC2TestListener(t, "c2atest-l4", "http", nil)
	pid := e.createC2TestProfile(t, "c2atest-prof-mismatch", "ps1")
	t.Cleanup(func() { e.cleanupC2EvasionArtifacts(t, []int64{lid}, []int64{pid}) })

	// 格式不一致(档案 ps1 vs 请求 py)→ helper 拒绝。
	rec := e.postC2Payload(t, map[string]any{
		"listener_id": lid, "format": "py", "artifact": "script",
		"direction": "reverse", "target": "windows", "evasion_profile_id": pid,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("格式不一致: status = %d, want 400(body=%s)", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("不一致")) {
		t.Fatalf("格式不一致文案缺失: %s", rec.Body.String())
	}

	// 档案不存在 → 400。
	rec = e.postC2Payload(t, map[string]any{
		"listener_id": lid, "format": "py", "artifact": "script",
		"direction": "reverse", "target": "windows", "evasion_profile_id": 987654321,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("档案不存在: status = %d, want 400(body=%s)", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("不存在")) {
		t.Fatalf("档案不存在文案缺失: %s", rec.Body.String())
	}
}
