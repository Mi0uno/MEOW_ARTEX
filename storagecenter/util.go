package storagecenter

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// relPath 与 path/filepath.Rel 一致的薄封装(独立函数便于测试与 mock)。
func relPath(baseDir, target string) (string, error) {
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return "", err
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	return filepath.Rel(absBase, absTarget)
}

// mustProxyURL 把 host:port 形式的代理地址转成 http 代理 URL。
// 失败只可能来自非法地址——调用方保证 ProxyAddr 来自平台配置。
func mustProxyURL(addr string) *url.URL {
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	u, err := url.Parse(addr)
	if err != nil {
		// 配置错误到这里已是编程错误;退一个可解析的占位避免 panic
		return &url.URL{Scheme: "http", Host: "127.0.0.1:1"}
	}
	return u
}

// trimKeySpace 收敛字符串字段(建桶入参清理用)。
func trimKeySpace(s string) string { return strings.TrimSpace(s) }

var _ = fmt.Sprintf // 保留 fmt:错误包装在子文件中大量使用
