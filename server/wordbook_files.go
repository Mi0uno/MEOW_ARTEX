package server

// 字典文件的物理 I/O 操作(与 HTTP handler 分离)。
// 所有路径在调用前必须经过 resolveWordbookPath / wordbookFilePath 校验。
// 本文件不含任何 HTTP 输入解析;参数均为已验证的 int64 id。

import (
	"fmt"
	"os"
	"path/filepath"
)

// wordbookSafeDir 返回字典库根目录(绝对路径)。
func wordbookSafeDir(dataDir string) string {
	return filepath.Clean(filepath.Join(dataDir, "wordbooks"))
}

// wordbookSafePath 拼接并校验路径:数字 id 命名 + 目录包含检查。
// 返回空串 = 校验失败(调用方应拒绝操作)。
func wordbookSafePath(dataDir string, id int64) string {
	if id <= 0 {
		return ""
	}
	dir := wordbookSafeDir(dataDir)
	p := filepath.Clean(filepath.Join(dir, fmt.Sprintf("%d.txt", id)))
	if !stringsHasPrefixDir(p, dir) {
		return ""
	}
	return p
}

// stringsHasPrefixDir 检查路径是否以指定目录为前缀(目录分隔符对齐)。
func stringsHasPrefixDir(p, dir string) bool {
	return len(p) >= len(dir) && p[:len(dir)] == dir
}

// openWordbookForRead 打开字典文件供读取(download/preview)。
func openWordbookForRead(dataDir string, id int64) (*os.File, error) {
	p := wordbookSafePath(dataDir, id)
	if p == "" {
		return nil, fmt.Errorf("无效字典 id")
	}
	return os.Open(p)
}

// writeWordbookFile 创建/覆盖字典文件供写入(upload)。
func writeWordbookFile(dataDir string, id int64) (*os.File, string, error) {
	p := wordbookSafePath(dataDir, id)
	if p == "" {
		return nil, "", fmt.Errorf("无效字典 id")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, "", err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	return f, p, err
}

// removeWordbookFile 删除字典文件(路径经 safePath 校验)。
func removeWordbookFile(dataDir string, id int64) {
	p := wordbookSafePath(dataDir, id)
	if p == "" {
		return
	}
	_ = os.Remove(p)
}
