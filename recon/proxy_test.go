package recon

// 流水线出网代理:验证代理经 context 传递后确实作用到 HTTP 客户端上。
// 这是「任务可选是否走代理池」在信息搜集侧的落点——配错会让搜集流量走直连,
// 把真实 IP 暴露出去,所以必须有测试锁住。

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// TestProxyFromCtxEmpty 没有代理时返回零值(直连)。
func TestProxyFromCtxEmpty(t *testing.T) {
	if cfg := proxyFromCtx(context.Background()); cfg.Addr != "" {
		t.Errorf("无代理的 ctx 应返回空地址,实际 %q", cfg.Addr)
	}
	if cfg := proxyFromCtx(nil); cfg.Addr != "" {
		t.Error("nil ctx 应安全返回空配置")
	}
}

// TestWithProxyRoundTrip 代理配置能原样取出。
func TestWithProxyRoundTrip(t *testing.T) {
	ctx := WithProxy(context.Background(), "http://127.0.0.1:18080", "/tmp/ca.pem")
	cfg := proxyFromCtx(ctx)
	if cfg.Addr != "http://127.0.0.1:18080" {
		t.Errorf("Addr = %q", cfg.Addr)
	}
	if cfg.CACert != "/tmp/ca.pem" {
		t.Errorf("CACert = %q", cfg.CACert)
	}
}

// TestWithProxyEmptyAddrIsNoop 空地址不该挂 context(直连语义)。
func TestWithProxyEmptyAddrIsNoop(t *testing.T) {
	base := context.Background()
	for _, addr := range []string{"", "   "} {
		ctx := WithProxy(base, addr, "/tmp/ca.pem")
		if ctx != base {
			t.Errorf("空地址 %q 应原样返回 ctx", addr)
		}
	}
}

// TestNewHTTPClientUsesProxy ctx 里带代理时,客户端的 Transport 必须走它。
func TestNewHTTPClientUsesProxy(t *testing.T) {
	ctx := WithProxy(context.Background(), "http://127.0.0.1:18080", "")
	c := newHTTPClient(ctx, time.Second)
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatal("Transport 类型不对")
	}
	if tr.Proxy == nil {
		t.Fatal("ctx 带代理时 Transport.Proxy 不该为 nil")
	}
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	pu, err := tr.Proxy(req)
	if err != nil {
		t.Fatalf("解析代理失败: %v", err)
	}
	if pu == nil || pu.Host != "127.0.0.1:18080" {
		t.Errorf("代理地址不对: %v", pu)
	}
}

// TestNewHTTPClientDirectByDefault 没有代理时必须直连。
//
// 关键:不能继承 HTTP_PROXY 环境变量。平台其它出网路径都显式置 nil,
// 这里若跟着环境变量走,「本机是否走代理」会变得不可预测——
// 用户以为在直连,实际流量却从某个环境变量指定的代理出去了。
func TestNewHTTPClientDirectByDefault(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://env-proxy.example.com:3128")
	t.Setenv("HTTPS_PROXY", "http://env-proxy.example.com:3128")

	c := newHTTPClient(context.Background(), time.Second)
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatal("Transport 类型不对")
	}
	if tr.Proxy != nil {
		t.Error("无代理 ctx 时 Transport.Proxy 必须为 nil(不继承环境变量)")
	}
}

// TestNewHTTPClientSocks5 socks5 代理也应被接受。
func TestNewHTTPClientSocks5(t *testing.T) {
	ctx := WithProxy(context.Background(), "socks5://127.0.0.1:1080", "")
	c := newHTTPClient(ctx, time.Second)
	tr := c.Transport.(*http.Transport)
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	pu, err := tr.Proxy(req)
	if err != nil {
		t.Fatalf("解析 socks5 代理失败: %v", err)
	}
	if pu == nil || pu.Scheme != "socks5" {
		t.Errorf("socks5 代理未被识别: %v", pu)
	}
}

// TestNewHTTPClientBadProxyFallsBackDirect 代理地址非法时退回直连而不是 panic。
func TestNewHTTPClientBadProxyFallsBackDirect(t *testing.T) {
	ctx := WithProxy(context.Background(), "://bad-address", "")
	c := newHTTPClient(ctx, time.Second)
	tr := c.Transport.(*http.Transport)
	if tr.Proxy != nil {
		req, _ := http.NewRequest(http.MethodGet, "http://example.com/", nil)
		if pu, err := tr.Proxy(req); err == nil && pu != nil {
			// 若 url.Parse 能容忍,至少要确保 host 是空的会被忽略
			if pu.Host != "" {
				t.Errorf("非法代理地址不该产生有效代理: %v", pu)
			}
		}
	}
}

// TestProxyURLCarriesCredentials 带凭据的代理地址要完整保留(供上层注入 Proxy-Authorization)。
func TestProxyURLCarriesCredentials(t *testing.T) {
	ctx := WithProxy(context.Background(), "http://user:pass@127.0.0.1:18080", "")
	c := newHTTPClient(ctx, time.Second)
	tr := c.Transport.(*http.Transport)
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	pu, err := tr.Proxy(req)
	if err != nil || pu == nil {
		t.Fatalf("解析失败: %v / %v", pu, err)
	}
	if pu.User == nil || pu.User.Username() != "user" {
		t.Errorf("代理凭据丢失: %v", pu.User)
	}
	pw, _ := pu.User.Password()
	if pw != "pass" {
		t.Errorf("代理密码丢失: %q", pw)
	}
}

// TestLoadCACertPoolMissing 读不到 CA 文件返回 nil(退回系统根证书,不静默跳过校验)。
func TestLoadCACertPoolMissing(t *testing.T) {
	if pool := loadCACertPool("/nonexistent/path/ca.pem"); pool != nil {
		t.Error("不存在的 CA 文件应返回 nil")
	}
}

// TestProxyURLEscaping 确保代理地址解析不因特殊字符出错。
func TestProxyURLEscaping(t *testing.T) {
	ctx := WithProxy(context.Background(), "http://p%40ss:word@127.0.0.1:8080", "")
	cfg := proxyFromCtx(ctx)
	if _, err := url.Parse(cfg.Addr); err != nil {
		t.Errorf("代理地址应可解析: %v", err)
	}
}
