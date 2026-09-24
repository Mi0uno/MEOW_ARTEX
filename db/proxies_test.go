package db

// 代理地址解析测试:补协议、认 socks5、拆用户密码、缺省端口。

import "testing"

func TestParseProxyURLVariants(t *testing.T) {
	cases := []struct {
		in       string
		protocol string
		host     string
		port     int
		wantErr  bool
	}{
		{"1.2.3.4:8080", "http", "1.2.3.4", 8080, false},
		{"http://1.2.3.4:3128", "http", "1.2.3.4", 3128, false},
		{"socks5://1.2.3.4:1080", "socks5", "1.2.3.4", 1080, false},
		{"https://1.2.3.4:443", "https", "1.2.3.4", 443, false},
		// 带凭据:用户密码归 Userinfo,不进 host/port。
		{"user:pass@1.2.3.4:8080", "http", "1.2.3.4", 8080, false},
		{"http://user:pass@1.2.3.4:8080", "http", "1.2.3.4", 8080, false},
		// 缺端口按协议补默认值。
		{"http://1.2.3.4", "http", "1.2.3.4", 8080, false},
		{"https://1.2.3.4", "https", "1.2.3.4", 443, false},
		{"ftp://1.2.3.4:21", "", "", 0, true},
		{"", "", "", 0, true},
		{"http://1.2.3.4:99999", "", "", 0, true},
	}
	for _, c := range cases {
		proto, host, port, err := ParseProxyURL(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q 应报错", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q 解析失败: %v", c.in, err)
			continue
		}
		if proto != c.protocol || host != c.host || port != c.port {
			t.Errorf("%q → (%s,%s,%d),期望 (%s,%s,%d)", c.in, proto, host, port, c.protocol, c.host, c.port)
		}
	}
}
