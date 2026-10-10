package server

// 存储中心纯函数测试:类别目录判定/终态任务目录判定/上传目录祖先判定。

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStorageTerminalTaskDir(t *testing.T) {
	root := t.TempDir() // 充当 dataDir
	tasks := filepath.Join(root, "tasks")
	for _, id := range []string{"12", "34", "abc"} {
		_ = os.MkdirAll(filepath.Join(tasks, id), 0o755)
	}
	terminal := map[int64]bool{12: true}
	cases := map[string]bool{
		filepath.Join(tasks, "12"):                true,  // 终态任务目录
		filepath.Join(tasks, "12", "transcripts"): true,  // 其子目录
		filepath.Join(tasks, "34"):                false, // 运行中任务
		filepath.Join(tasks, "abc"):               false, // 非任务目录
		filepath.Join(tasks):                      true,  // 根层继续走
	}
	for path, want := range cases {
		if got := storageTerminalTaskDir(root, path, terminal); got != want {
			t.Fatalf("storageTerminalTaskDir(%s) = %v, want %v", path, got, want)
		}
	}
}

func TestStorageUnderUploadsAncestor(t *testing.T) {
	root := t.TempDir()
	up := filepath.Join(root, "tasks", "12", "uploads")
	_ = os.MkdirAll(up, 0o755)
	if !storageUnderUploadsAncestor(root, filepath.Join(up, "a.txt")) {
		t.Fatal("uploads 子文件应命中")
	}
	if storageUnderUploadsAncestor(root, filepath.Join(root, "tasks", "12", "transcripts", "x.log")) {
		t.Fatal("非 uploads 路径不应命中")
	}
}

func TestStoragePurgeableCategories(t *testing.T) {
	// 契约:v1 只开放 report-jobs / task-archives
	if len(storagePurgeableCategories) != 2 {
		t.Fatalf("清理类别应只有两类: %v", storagePurgeableCategories)
	}
	seen := map[string]bool{}
	for _, c := range storageCategories {
		seen[c] = true
	}
	for _, c := range storagePurgeableCategories {
		if !seen[c] {
			t.Fatalf("清理类别 %s 不在类别注册表", c)
		}
	}
}
