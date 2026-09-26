package c2center

// tcp 载体(契约 §18.2):net.Listen + 每连接一个帧循环(4B big-endian 长度 +
// 裸密文,首帧必须是 reg)。连接由 beacon 侧主动保持:一条连接上反复 reg/poll/res;
// 服务端帧读空闲超时(30min)判死断连,beacon 按自身退避重连重注册(会话幂等复活)。

import (
	"fmt"
	"net"
	"time"
)

// frameReadTimeout 是帧式连接的读空闲时限:超过未收到任何帧判死。
// beacon 的 sleep 最大 3600s + 退避,30min 内空闲即视为半死连接回收(重连无感,
// 会话行按 listener_id+beacon_uid 幂等)。
const frameReadTimeout = 30 * time.Minute

// startFrameTCP 拉起 tcp 监听。
func (s *channelServer) startFrameTCP() error {
	ln, err := net.Listen("tcp", s.listenAddr())
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", s.listenAddr(), err)
	}
	s.ln = ln
	go s.acceptLoop(ln, func(c net.Conn) Conn {
		return newFrameConn(newTimeoutConn(c, frameReadTimeout), nil)
	})
	return nil
}

// acceptLoop 裸 listener 的接受循环(tcp/kcp 共用):每连接一个 goroutine 跑帧
// 循环;listener 关闭(Accept 报错)时退出。
func (s *channelServer) acceptLoop(ln net.Listener, wrap func(net.Conn) Conn) {
	for {
		c, err := ln.Accept()
		if err != nil {
			select {
			case <-s.ctx.Done():
			default:
				s.mgr.logf("监听器 «%s»(#%d) accept 退出: %v", s.l.Name, s.l.ID, err)
			}
			return
		}
		go s.serveConn(wrap(c), false)
	}
}

// timeoutConn 是带读空闲时限的 net.Conn(每次读前刷新)。
type timeoutConn struct {
	net.Conn
	readTimeout time.Duration
}

func newTimeoutConn(c net.Conn, readTimeout time.Duration) *timeoutConn {
	return &timeoutConn{Conn: c, readTimeout: readTimeout}
}

func (c *timeoutConn) Read(p []byte) (int, error) {
	_ = c.Conn.SetReadDeadline(time.Now().Add(c.readTimeout))
	return c.Conn.Read(p)
}
