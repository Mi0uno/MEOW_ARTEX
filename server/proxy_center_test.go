package server

// 代理清单条目补协议:源站清单给的是裸 host:port,入库前要按本源协议补全。

import "testing"

func TestBuildProxyRawURL(t *testing.T) {
	cases := []struct{ protocol, in, want string }{
		{"http", "1.2.3.4:8080", "http://1.2.3.4:8080"},
		{"socks5", "1.2.3.4:1080", "socks5://1.2.3.4:1080"},
		{"https", "1.2.3.4:443", "https://1.2.3.4:443"},
		// 清单里自带协议时要剥掉再按本源协议补,避免 "http://http://…"。
		{"http", "http://1.2.3.4:8080", "http://1.2.3.4:8080"},
		{"socks5", "http://1.2.3.4:1080", "socks5://1.2.3.4:1080"},
		{"http", "", ""},
		{"http", "not a host", ""},
		{"http", "1.2.3.4:99999", ""},
	}
	for _, c := range cases {
		if got := buildProxyRawURL(c.protocol, c.in); got != c.want {
			t.Errorf("buildProxyRawURL(%q,%q) = %q,期望 %q", c.protocol, c.in, got, c.want)
		}
	}
}
