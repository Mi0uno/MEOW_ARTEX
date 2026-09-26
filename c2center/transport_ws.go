package c2center

// ws 载体(契约 §18.1):proto=ws 时信箱从「POST /<url_path>/e 的 body」换成
// 「GET /<url_path>/e upgrade 后的文本消息」,内容仍是 base64(密文)——与 http
// 同一处理核,帧语义逐字节兼容一期。升级在既有 http.Server 上完成(同一端口也
// 保留 /<url_path>/p/<token> 载荷投递)。ping/pong 保活:服务端每 30s 发 ping,
// 90s 无任何入站帧/pong 判死断连。

import (
	"encoding/base64"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// wsPingInterval 是服务端保活 ping 周期。
	wsPingInterval = 30 * time.Second
	// wsReadTimeout 是无入站帧/pong 的判死时限(3 个 ping 周期)。
	wsReadTimeout = 90 * time.Second
	// wsWriteTimeout 是单次写(含 ping)的时限。
	wsWriteTimeout = 30 * time.Second
)

// upgrader 是 ws 升级器:beacon 不是浏览器,不校验 Origin;失败不回明文。
var upgrader = websocket.Upgrader{
	CheckOrigin:      func(*http.Request) bool { return true },
	HandshakeTimeout: 10 * time.Second,
}

// handleWS 处理 /<url_path>/e 的 GET upgrade;升级后与 tcp/kcp 同一帧循环。
func (s *channelServer) handleWS(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		http.NotFound(w, r) // 升级失败:与「路径不存在」同伪装
		return
	}
	c := newWSConn(ws)
	go s.wsKeepalive(c)
	s.serveConn(c, false)
}

// newWSConn 装配 wsConn:pong 与数据帧都刷新读时限(长休眠保活)。
func newWSConn(ws *websocket.Conn) *wsConn {
	c := &wsConn{conn: ws}
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(wsReadTimeout))
	})
	return c
}

// wsKeepalive 周期 ping;ctx 取消或连接已死时退出。
func (s *channelServer) wsKeepalive(c *wsConn) {
	t := time.NewTicker(wsPingInterval)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			if err := c.ping(); err != nil {
				return
			}
		}
	}
}

// wsConn 是 websocket 上的 Conn 实现:消息 = base64(密文) 文本帧(与一期 http
// body 同格式)。ping/pong 由 gorilla 默认 handler 自动回 pong,保活由本侧 ping。
type wsConn struct {
	conn *websocket.Conn
}

// ReadFrame 读一条数据消息并 base64 解码;入站前刷新读时限。gorilla 的 ReadMessage
// 会消化 ping/pong(自动回 pong)不落到这里,返回的恒为数据帧。
func (c *wsConn) ReadFrame() ([]byte, error) {
	_ = c.conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
	_, data, err := c.conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// WriteFrame 加密帧 base64 后以文本消息发出。
func (c *wsConn) WriteFrame(frame []byte) error {
	_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	return c.conn.WriteMessage(websocket.TextMessage, []byte(base64.StdEncoding.EncodeToString(frame)))
}

// ping 发保活 ping(WriteControl 可与数据写并发,gorilla 保证)。
func (c *wsConn) ping() error {
	return c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
}

// Close 关闭 ws(尽力发 close 帧,再断底层连接;重复 Close 由 gorilla 幂等)。
func (c *wsConn) Close() error {
	_ = c.conn.SetWriteDeadline(time.Now().Add(time.Second))
	_ = c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	return c.conn.Close()
}
