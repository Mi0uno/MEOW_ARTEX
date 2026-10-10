package db

// 存储中心 db 层测试:桶 CRUD/默认桶切换/测活状态机/对象登记幂等/迁移作业生命周期
// /列表计数口径。数据用 STG-<纳秒> 前缀名隔离,defer 清干净(复用库残留敏感)。

import (
	"fmt"
	"testing"
	"time"
)

func storageTestCleanupBucket(d *DB, id int64) {
	_, _ = d.Exec(`DELETE FROM storage_buckets WHERE id=$1`, id) //nolint:errcheck
}

func storageTestNewBucket(t *testing.T, d *DB, name string) *StorageBucket {
	t.Helper()
	b, err := d.CreateStorageBucket(&StorageBucket{
		Name: name, Protocol: "s3", Endpoint: "http://127.0.0.1:9000",
		Bucket: "bkt-" + name, Region: "us-east-1", AccessKey: "ak", SecretEnc: "c2VhbA==",
	})
	if err != nil {
		t.Fatalf("建桶失败: %v", err)
	}
	return b
}

func TestStorageBucketsCRUDAndCounts(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("存储中心测试库不可用 (%v)", err)
	}
	suffix := fmt.Sprintf("STG-%d", time.Now().UnixNano())
	b1 := storageTestNewBucket(t, d, suffix+"-a")
	b2 := storageTestNewBucket(t, d, suffix+"-b")
	defer storageTestCleanupBucket(d, b1.ID)
	defer storageTestCleanupBucket(d, b2.ID)

	// 更新:secret 留空不改旧值
	if err := d.UpdateStorageBucket(b1.ID, &StorageBucket{
		Name: b1.Name, Protocol: "webdav", Endpoint: "https://dav.local/dav",
		Bucket: "dir", AccessKey: "user",
	}, ""); err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	got, _ := d.GetStorageBucket(b1.ID)
	if got.Protocol != "webdav" || !got.HasSecret || got.SecretEnc != "c2VhbA==" {
		t.Fatalf("更新后字段异常: %+v", got)
	}

	// 默认桶切换 + 取消
	if err := d.ActivateStorageBucket(b2.ID); err != nil {
		t.Fatalf("激活失败: %v", err)
	}
	act, _ := d.GetActiveStorageBucket()
	if act == nil || act.ID != b2.ID {
		t.Fatalf("默认桶应为 %d, got %+v", b2.ID, act)
	}
	if err := d.ActivateStorageBucket(b1.ID); err != nil {
		t.Fatalf("切换激活失败(唯一索引冲突?): %v", err)
	}
	if err := d.DeactivateStorageBucket(); err != nil {
		t.Fatal(err)
	}
	if act, _ = d.GetActiveStorageBucket(); act != nil {
		t.Fatalf("取消后不应有默认桶: %+v", act)
	}

	// 测活状态机:ok / degraded
	if err := d.SetStorageBucketCheck(b1.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if b, _ := d.GetStorageBucket(b1.ID); b.Status != "ok" {
		t.Fatalf("测活成功应 ok, got %s", b.Status)
	}
	if err := d.SetStorageBucketCheck(b1.ID, false, "连接拒绝"); err != nil {
		t.Fatal(err)
	}
	if b, _ := d.GetStorageBucket(b1.ID); b.Status != "degraded" || b.LastCheckError != "连接拒绝" {
		t.Fatalf("测活失败应 degraded: %+v", b)
	}

	// 列表 + 计数口径:protocol 过滤后 counts 仍含两协议;status_counts 跟随协议过滤,
	// 但不受自身维度影响(degraded 的 b1 是 webdav,过滤 s3 时只见到 b2 的 unchecked)。
	if _, _, counts, statusCounts, err := d.ListStorageBuckets(StorageBucketFilter{
		Protocol: "s3", Q: suffix, Limit: 10,
	}); err != nil || counts["s3"] < 1 || counts["webdav"] < 1 || statusCounts["unchecked"] < 1 {
		t.Fatalf("计数口径异常: counts=%v statusCounts=%v err=%v", counts, statusCounts, err)
	}
	if _, _, _, statusCounts, err := d.ListStorageBuckets(StorageBucketFilter{
		Protocol: "webdav", Q: suffix, Limit: 10,
	}); err != nil || statusCounts["degraded"] < 1 {
		t.Fatalf("webdav 口径应含 degraded: %v err=%v", statusCounts, err)
	}

	// 对象登记幂等
	if ins, err := d.InsertStorageObject(b1.ID, "report-jobs", "job_1.docx", 10); err != nil || !ins {
		t.Fatalf("首次登记应成功: %v %v", ins, err)
	}
	if ins, _ := d.InsertStorageObject(b1.ID, "report-jobs", "job_1.docx", 10); ins {
		t.Fatal("重复登记应幂等返回 false")
	}
	if ok, _ := d.HasStorageObject(b1.ID, "job_1.docx"); !ok {
		t.Fatal("登记后应可查到")
	}
	paths, _ := d.ListUploadedRelPaths(b1.ID, "report-jobs")
	if !paths["job_1.docx"] {
		t.Fatalf("ListUploadedRelPaths 缺项: %v", paths)
	}

	// 迁移作业生命周期
	m, err := d.CreateStorageMigration(b1.ID, []string{"report-jobs", "uploads"})
	if err != nil {
		t.Fatalf("建迁移作业失败: %v", err)
	}
	defer func() { _, _ = d.Exec(`DELETE FROM storage_migrations WHERE id=$1`, m.ID) }() //nolint:errcheck
	if run, _ := d.GetRunningStorageMigration(); run == nil || run.ID != m.ID {
		t.Fatalf("应查到 running 作业: %+v", run)
	}
	if err := d.SetStorageMigrationProgress(m.ID, 3, 1, 10); err != nil {
		t.Fatal(err)
	}
	if err := d.FinishStorageMigration(m.ID, "done", "", []byte(`{"report-jobs":"job_1.docx"}`)); err != nil {
		t.Fatal(err)
	}
	got2, _ := d.GetStorageMigration(m.ID)
	if got2.Status != "done" || got2.DoneFiles != 3 || got2.FailedFiles != 1 ||
		len(got2.Categories) != 2 || got2.Categories[0] != "report-jobs" {
		t.Fatalf("终态字段异常: %+v", got2)
	}
	if run, _ := d.GetRunningStorageMigration(); run != nil {
		t.Fatalf("终态后不应再有 running: %+v", run)
	}
	latest, _ := d.GetLatestStorageMigration()
	if latest == nil || latest.ID != m.ID {
		t.Fatalf("latest 应为 %d: %+v", m.ID, latest)
	}

	// 删除桶连带清对象登记(级联)
	if _, err := d.Exec(`DELETE FROM storage_objects WHERE bucket_id=$1`, b1.ID); err != nil {
		t.Fatal(err)
	}
	if del, err := d.DeleteStorageBucket(b1.ID); err != nil || !del {
		t.Fatalf("删桶失败: %v %v", del, err)
	}
	storageTestCleanupBucket(d, b1.ID) // 幂等
}
