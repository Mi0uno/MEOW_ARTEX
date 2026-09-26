package c2center

// kcp 载体(契约 §18.3):kcp-go v5 Listen + 每连接帧循环(帧格式同 tcp:4B
// big-endian 长度 + 裸密文,首帧必须是 reg)。可选预共享 key 混淆(transport_config
// 的 psk 字段):整帧(含 4B 长度头)与 key 循环 XOR、逐帧复位,收发两侧对称实现。
//
// 会话参数:双端都开 stream 模式(长度前缀帧需要连续字节流语义;非 stream 模式下
// kcp-go 的 Read 会丢弃超出缓冲的报文剩余部分,破坏帧解析)并按低延迟调 nodelay。

import (
	"fmt"
	"net"
	"strconv"

	kcp "github.com/xtaci/kcp-go/v5"
)

// startKCP 拉起 kcp 监听(psk 可空 = 不混淆)。
func (s *channelServer) startKCP() error {
	cfg, err := s.transportConfig()
	if err != nil {
		return err
	}
	var key []byte
	if cfg != nil && cfg.PSK != "" {
		key = []byte(cfg.PSK)
	}
	ln, err := kcp.Listen(net.JoinHostPort(s.l.BindHost, strconv.Itoa(s.l.Port)))
	if err != nil {
		return fmt.Errorf("kcp 监听 %s 失败: %w", s.listenAddr(), err)
	}
	s.ln = ln
	go s.acceptLoop(ln, func(c net.Conn) Conn {
		tuneKCP(c)
		return newFrameConn(newTimeoutConn(c, frameReadTimeout), key)
	})
	return nil
}

// tuneKCP 会话参数:stream 模式 + 低延迟(收发两端必须一致,否则帧流错位)。
func tuneKCP(c net.Conn) {
	if s, ok := c.(*kcp.UDPSession); ok {
		s.SetStreamMode(true)
		s.SetNoDelay(1, 10, 2, 1)
		s.SetWindowSize(256, 256)
	}
}

// transportConfig 取解封后的传输配置(store 未实现可选接口时为零值)。
func (s *channelServer) transportConfig() (*TransportConfig, error) {
	if s.cfg != nil {
		return s.cfg, nil
	}
	cfg := &TransportConfig{}
	src, ok := s.mgr.store.(transportConfigStore)
	if ok {
		raw, err := src.GetListenerTransportConfig(s.l.ID)
		if err != nil {
			return nil, fmt.Errorf("读取监听器 #%d 传输配置失败: %w", s.l.ID, err)
		}
		cfg, err = parseTransportConfig(raw)
		if err != nil {
			return nil, err
		}
	}
	s.cfg = cfg
	return cfg, nil
}

// dialKCP 主动拨号(正向连接用):与监听侧同参数 + 同 psk。
func dialKCP(host, port, psk string) (Conn, error) {
	conn, err := kcp.Dial(net.JoinHostPort(host, port))
	if err != nil {
		return nil, fmt.Errorf("kcp 连接 %s 失败: %w", net.JoinHostPort(host, port), err)
	}
	tuneKCP(conn)
	var key []byte
	if psk != "" {
		key = []byte(psk)
	}
	return newFrameConn(newTimeoutConn(conn, frameReadTimeout), key), nil
}
