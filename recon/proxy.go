package recon

// 信息搜集流水线的出网代理:经 context 传递,而不是给每个收集器加字段。
//
// 为什么走 context:收集器有 16+ 家 provider + 字典爆破 + crt.sh,它们最终都汇聚到
// 本包内 3 个 HTTP 助手(doRequest / safeGet / collectors 的 client)。把代理挂在
// context 上,只需在这 3 处读一次,新增收集器也自动继承,不必逐个改造签名。
//
// 为什么不用包级变量:流水线可能并发跑多个任务(不同任务可能选了不同代理组),
// 包级变量会互相串。context 天然是每任务一份。

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// proxyCtxKey 是 context 里代理配置的键(私有类型,避免与其它包的键冲突)。
type proxyCtxKey struct{}

// ProxyConfig 是一次流水线执行期间要用的出网代理。
type ProxyConfig struct {
	// Addr 是代理地址(http/https/socks5,可带 user:pass)。空 = 直连。
	Addr string
	// CACert 是需信任的自定义 CA 证书路径(走本机 MITM 录制时才有)。空 = 用系统根证书。
	CACert string
}

// WithProxy 把代理配置挂进 context。addr 为空时原样返回 ctx(直连)。
func WithProxy(ctx context.Context, addr, caCert string) context.Context {
	if strings.TrimSpace(addr) == "" {
		return ctx
	}
	return context.WithValue(ctx, proxyCtxKey{}, ProxyConfig{Addr: addr, CACert: caCert})
}

// proxyFromCtx 取出代理配置(没有则返回零值 = 直连)。
func proxyFromCtx(ctx context.Context) ProxyConfig {
	if ctx == nil {
		return ProxyConfig{}
	}
	if v, ok := ctx.Value(proxyCtxKey{}).(ProxyConfig); ok {
		return v
	}
	return ProxyConfig{}
}

// newHTTPClient 按 context 里的代理配置构造 http.Client。
//
// 语义:ctx 无代理 → 直连(Transport.Proxy 显式置 nil,不读 HTTP_PROXY 环境变量,
// 与平台其它出网路径口径一致——环境变量代理会让「本机是否走代理」变得不可预测)。
func newHTTPClient(ctx context.Context, timeout time.Duration) *http.Client {
	cfg := proxyFromCtx(ctx)
	tr := &http.Transport{
		// 显式 nil:不继承环境变量代理。
		Proxy:                 nil,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
	}
	if cfg.Addr != "" {
		if pu, err := url.Parse(cfg.Addr); err == nil && pu.Host != "" {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	if cfg.CACert != "" {
		if pool := loadCACertPool(cfg.CACert); pool != nil {
			tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		}
	}
	return &http.Client{Timeout: timeout, Transport: tr}
}

// loadCACertPool 读入自定义 CA 证书(走本机 MITM 录制时,上游用它重签证书)。
// 读不到就返回 nil,退回系统根证书——宁可失败也不静默跳过校验。
func loadCACertPool(path string) *x509.CertPool {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil
	}
	return pool
}
