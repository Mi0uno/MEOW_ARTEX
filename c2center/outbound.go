package c2center

// 正向连接(bind)平台侧(契约 §18):beacon 在目标机上监听端口等首帧,平台主动
// dial 过去完成 reg 握手建会话。
//
//   - NewOutboundConn(§18 冻结签名,Agent P 依赖):以监听器的协议拨号,拿到帧式
//     Conn;仅 tcp/kcp 支持(http/ws/oss 返回明确错误)。
//   - ConnectBind:NewOutboundConn → 借该监听器运行时的处理核完成首帧 reg 握手
//     (同步)→ 会话行 transport=tcp/kcp、meta.direction=bind → 后续帧循环转后台。
//     监听器必须已在运行(处理核与密钥/防重放表都挂在运行时上)。

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// outboundDialTimeout 是正向拨号时限。
const outboundDialTimeout = 10 * time.Second

// NewOutboundConn 以监听器的协议主动拨号目标(bind 方向),返回帧式 Conn。
// 帧格式与对应载体一致:tcp/kcp 为 4B big-endian 长度 + 裸密文(kcp 可选 psk XOR
// 混淆,与监听侧同 key)。仅 tcp/kcp 支持;http/ws/oss 无正向语义,返回明确错误。
// 【契约 §18 冻结签名】
func (m *Manager) NewOutboundConn(l *Listener, host, port string) (Conn, error) {
	if l == nil || l.ID <= 0 {
		return nil, fmt.Errorf("监听器无效")
	}
	if strings.TrimSpace(host) == "" {
		return nil, fmt.Errorf("host 必填")
	}
	switch l.Proto {
	case ProtoTCP:
		d := net.Dialer{Timeout: outboundDialTimeout}
		c, err := d.Dial("tcp", net.JoinHostPort(host, port))
		if err != nil {
			return nil, fmt.Errorf("tcp 连接 %s 失败: %w", net.JoinHostPort(host, port), err)
		}
		return newFrameConn(newTimeoutConn(c, frameReadTimeout), nil), nil
	case ProtoKCP:
		psk := ""
		if cs := m.runningChannelServer(l.ID); cs != nil {
			if cfg, err := cs.transportConfig(); err == nil && cfg != nil {
				psk = cfg.PSK
			}
		}
		return dialKCP(host, port, psk)
	case ProtoHTTP, ProtoWS, ProtoOSS, "":
		return nil, fmt.Errorf("协议 %s 不支持正向连接(仅 tcp/kcp)", transportName(l.Proto))
	default:
		return nil, fmt.Errorf("不支持的监听器协议: %q", l.Proto)
	}
}

// transportName 空协议的展示口径。
func transportName(p string) string {
	if p == "" {
		return ProtoHTTP
	}
	return p
}

// ConnectBind 正向连接目标并完成 reg 握手建会话(server API 入口)。
// 返回新会话 id;首帧非 reg / 握手失败原样报错并关闭连接,后续帧循环转后台。
func (m *Manager) ConnectBind(l *Listener, host, port string) (int64, error) {
	if l == nil || l.ID <= 0 {
		return 0, fmt.Errorf("监听器无效")
	}
	if _, err := strconv.Atoi(port); err != nil {
		return 0, fmt.Errorf("port 非法: %q", port)
	}
	cs := m.runningChannelServer(l.ID)
	if cs == nil {
		return 0, fmt.Errorf("监听器 #%d 未在运行,正向连接需先启动监听器", l.ID)
	}
	conn, err := m.NewOutboundConn(l, host, port)
	if err != nil {
		return 0, err
	}
	sessID, err := cs.handshakeBind(conn)
	if err != nil {
		_ = conn.Close()
		return 0, err
	}
	go cs.serveConn(conn, true, true) // 首帧已由握手消费,循环以非首帧继续
	return sessID, nil
}

// runningChannelServer 取运行中的监听器运行时(未运行返回 nil)。
func (m *Manager) runningChannelServer(id int64) *channelServer {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running[id]
}
