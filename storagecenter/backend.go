// Package storagecenter 存储桶接入:对象存储 Backend 抽象 + S3(SigV4)/WebDAV 客户端。
// 不 import db/server——纯协议层;桶配置行由 server 层读库后经 NewBackend 装配。
// 出网铁律:一切请求走调用方注入的 ProxyAddr/CAFile(全局出口),不继承环境变量。
package storagecenter

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

// Config 一个存储桶的连接配置(来自 storage_buckets 行,凭据已解密)。
type Config struct {
	Protocol   string // s3 | webdav
	Endpoint   string // 形如 https://minio.example:9000,不带桶名
	Bucket     string
	Region     string // s3 专用,缺省 us-east-1
	PathPrefix string // 对象 key 前缀,如 artex/(空 = 根)
	AccessKey  string // webdav = 用户名
	Secret     string // webdav = 密码
	ProxyAddr  string // 全局出口代理地址(host:port),空 = 直连
	CAFile     string // MITM CA 证书路径,空 = 系统根
	Timeout    time.Duration
}

// Backend 对象存储协议抽象。key 一律是「桶内对象键」(已含前缀),
// 由 ObjectKey 统一构造;实现层不做任何 key 变换。
type Backend interface {
	// Put 上传对象。size < 0 表示未知(实现自选分块或缓冲)。
	Put(ctx context.Context, key string, r io.Reader, size int64) error
	// Get 拉取对象流;调用方负责 Close。不存在返回 ErrNotFound。
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// Stat 对象大小与是否存在;不存在 ok=false。
	Stat(ctx context.Context, key string) (size int64, ok bool, err error)
	Delete(ctx context.Context, key string) error
	// Test 连通性探测:s3 = HEAD bucket,webdav = PROPFIND depth 0。
	Test(ctx context.Context) error
}

// ErrNotFound 对象不存在(Get/Stat 语义,调用方据此回落本地文件)。
var ErrNotFound = fmt.Errorf("object not found")

// ObjectInfo 列举结果里的对象条目。
type ObjectInfo struct {
	Key  string
	Size int64
}

// NormalizeEndpoint 归一端点:补 scheme(无前缀按 https)、去尾部斜杠与空白。
// 只接受 http/https——存储端点不是任意 URL。
func NormalizeEndpoint(raw string) (string, error) {
	e := strings.TrimSpace(raw)
	if e == "" {
		return "", fmt.Errorf("端点不能为空")
	}
	if !strings.Contains(e, "://") {
		e = "https://" + e
	}
	e = strings.TrimRight(e, "/")
	if !strings.HasPrefix(e, "http://") && !strings.HasPrefix(e, "https://") {
		return "", fmt.Errorf("端点仅支持 http/https: %s", raw)
	}
	return e, nil
}

// ObjectKey 统一对象键:path_prefix + category + '/' + rel_path。
// relPath 恒以 / 分隔(Windows 本地路径在进入本层前先转好);拒绝 .. 逃逸。
func ObjectKey(prefix, category, relPath string) (string, error) {
	relPath = strings.ReplaceAll(relPath, "\\", "/")
	relPath = strings.TrimLeft(relPath, "/")
	if relPath == "" || strings.Contains(relPath, "..") {
		return "", fmt.Errorf("非法对象路径: %q", relPath)
	}
	prefix = strings.Trim(prefix, "/")
	parts := []string{}
	for _, p := range []string{prefix, category, relPath} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "/"), nil
}

// RelPathFromLocal 本地绝对路径 → dataDir 相对路径(恒 / 分隔);不在基目录下报错。
func RelPathFromLocal(baseDir, localPath string) (string, error) {
	rel, err := relPath(baseDir, localPath)
	if err != nil {
		return "", err
	}
	rel = strings.ReplaceAll(rel, "\\", "/")
	if rel == "" || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("%s 不在数据目录 %s 下", localPath, baseDir)
	}
	return rel, nil
}

// NewBackend 按协议装配客户端。timeout 缺省 120s(对象上传走大文件,别用 60s 默认)。
func NewBackend(cfg Config) (Backend, error) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 120 * time.Second
	}
	client, err := httpClientFor(cfg)
	if err != nil {
		return nil, err
	}
	switch cfg.Protocol {
	case "s3":
		return newS3Backend(cfg, client)
	case "webdav":
		return newWebDAVBackend(cfg, client)
	default:
		return nil, fmt.Errorf("不支持的存储协议: %s", cfg.Protocol)
	}
}

// httpClientFor 出网客户端:显式代理(不走环境变量)+ 可选 MITM CA。
func httpClientFor(cfg Config) (*http.Client, error) {
	tr := &http.Transport{
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
	}
	if cfg.ProxyAddr != "" {
		tr.Proxy = http.ProxyURL(mustProxyURL(cfg.ProxyAddr))
	} else {
		tr.Proxy = nil // 显式直连——绝不继承 HTTP_PROXY 环境变量
	}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("读 CA 证书失败: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("CA 证书解析失败: %s", cfg.CAFile)
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool}
	}
	return &http.Client{Transport: tr, Timeout: cfg.Timeout}, nil
}

// PathJoin 本地路径拼装(测试辅助与 server 层共用语义)。
func PathJoin(elem ...string) string { return path.Join(elem...) }
