package server

// VPS Web 终端:SSH PTY ⇄ WebSocket(契约 §5)。
//
// 鉴权路径:本路由注册在 registerVPSRoutes(mux) 内,处于 requireAuth 中间件链上。
// 已核实 server.go 的中间件实现(requireAuth/noStore/cors)都是把原始
// ResponseWriter 原样透传(不包装),http.Hijacker 能力保留,websocket.Upgrade
// 可直接成功;浏览器侧经 api.vpsTerminalUrl(id) 拼 wsUrl() 时 token 走 ?token=
// 查询参数,requireAuth 的 extractToken 已覆盖该形态,无需链外二次校验。
//
// 协议:
//   C→S JSON: {"type":"stdin","data":"..."} / {"type":"resize","cols":120,"rows":36} / {"type":"ping"}
//   S→C JSON: {"type":"stdout","data":"..."} / {"type":"exit","code":n} / {"type":"error","message":"..."}
//
// 约束:单机并发终端 ≤2(Manager.OpenPTY 仲裁);空闲 30 分钟服务端主动关;
// stdout 按 UTF-8 边界分帧(xterm 直接吃 UTF-8 解码后的串)。

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/Autumn-27/artex/vpscenter"
	"github.com/gorilla/websocket"
)

// vpsIdleTimeout 是终端空闲(无任何客户端消息)后的服务端关闭时限。
const vpsIdleTimeout = 30 * time.Minute

// vpsUpgrader 是终端专用升级器。CheckOrigin 与 cors() 同一判据:同 host 或
// localhost 系开发源;Next 静态导出与后端同源,开发模式 3000 端口也在放行之列。
var vpsUpgrader = websocket.Upgrader{
	ReadBufferSize:  8192,
	WriteBufferSize: 8192,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // 非浏览器客户端
		}
		return corsOriginAllowed(origin, r.Host)
	},
}

// vpsTerminal GET /api/vps/machines/{id}/terminal — WebSocket 升级。
func (s *Server) vpsTerminal(w http.ResponseWriter, r *http.Request) {
	if !s.vpsReady(w) {
		return
	}
	machineID, ok := vpsPathID(w, r)
	if !ok {
		return
	}
	mgr := VPSManager()
	if mgr == nil {
		writeErr(w, 503, "VPS 中心未初始化")
		return
	}
	conn, err := vpsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade 失败时已写过 HTTP 错误响应,这里只记日志。
		log.Printf("[vps-center] 终端升级失败(机器 #%d): %v", machineID, err)
		return
	}

	// 升级后任何错误都走 WS error 帧(不再有状态码可用)。
	sess, err := mgr.OpenPTY(r.Context(), machineID, 120, 36)
	if err != nil {
		writeWSJSON(conn, map[string]any{"type": "error", "message": err.Error()})
		_ = conn.Close()
		return
	}
	s.runTerminalSession(conn, sess, machineID)
}

// runTerminalSession 驱动一路终端:两条泵 + 一个退出等待者,任一端结束即清理。
func (s *Server) runTerminalSession(conn *websocket.Conn, sess vpscenter.PTYSession, machineID int64) {
	closed := make(chan struct{})
	var closeOnce sync.Once
	cleanup := func() {
		closeOnce.Do(func() {
			_ = sess.Close()
			_ = conn.Close()
			close(closed)
		})
	}
	defer cleanup()

	// 写互斥:stdout 泵与 exit/error 帧共用一条连接。
	writeMu := make(chan struct{}, 1)
	writeJSON := func(v any) bool {
		select {
		case writeMu <- struct{}{}:
		case <-closed:
			return false
		}
		defer func() { <-writeMu }()
		if err := conn.WriteJSON(v); err != nil {
			return false
		}
		return true
	}

	// 1) stdout 泵:PTY → WS(UTF-8 边界安全分帧)。
	go func() {
		buf := make([]byte, 4096)
		var carry []byte
		for {
			n, rerr := sess.Read(buf)
			if n > 0 {
				data := append(carry, buf[:n]...)
				send, keep := splitUTF8(data)
				carry = append([]byte(nil), keep...)
				if len(send) > 0 {
					if !writeJSON(map[string]any{"type": "stdout", "data": string(send)}) {
						return
					}
				}
			}
			if rerr != nil {
				// 远端会话结束:flush 残尾后退出,由 exit 等待者发 exit 帧。
				if len(carry) > 0 {
					writeJSON(map[string]any{"type": "stdout", "data": string(carry)})
				}
				return
			}
			select {
			case <-closed:
				return
			default:
			}
		}
	}()

	// 2) 退出等待者:会话结束 → 发 exit 帧 → 关连接(触发读循环退出)。
	go func() {
		code, ok := <-sess.ExitCode()
		if ok {
			writeJSON(map[string]any{"type": "exit", "code": code})
		}
		cleanup()
	}()

	// 3) 读循环(当前 goroutine):WS → PTY;空闲 30 分钟主动关。
	conn.SetReadDeadline(time.Now().Add(vpsIdleTimeout))
	for {
		var msg struct {
			Type string `json:"type"`
			Data string `json:"data"`
			Cols uint16 `json:"cols"`
			Rows uint16 `json:"rows"`
		}
		if err := conn.ReadJSON(&msg); err != nil {
			// 客户端断开或空闲超时。
			log.Printf("[vps-center] 终端会话结束(机器 #%d): %v", machineID, err)
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(vpsIdleTimeout))
		switch msg.Type {
		case "stdin":
			if _, err := sess.Write([]byte(msg.Data)); err != nil {
				return
			}
		case "resize":
			_ = sess.Resize(msg.Cols, msg.Rows)
		case "ping":
			// 保活帧:读超时已刷新,无需回包(协议 S→C 无 pong)。
		default:
			// 未知帧忽略,避免脏消息杀会话。
		}
	}
}

// writeWSJSON 是升级失败/会话未建立阶段的尽力而为写帧。
func writeWSJSON(conn *websocket.Conn, v any) {
	_ = conn.WriteJSON(v)
}

// splitUTF8 把字节流按 UTF-8 边界切开:尾部不完整的多字节序列留给下一次
// (JSON 序列化会把残缺字节替换成 U+FFFD,必须避免)。
func splitUTF8(b []byte) (send, keep []byte) {
	n := len(b)
	// 从尾部最多回看 3 字节,找可能被截断的 UTF-8 序列首字节。
	for i := n - 1; i >= 0 && i >= n-3; i-- {
		c := b[i]
		if c < 0x80 {
			return b, nil // ASCII 结尾,完整
		}
		if c&0xC0 == 0xC0 { // 序列首字节
			size := utf8SeqLen(c)
			if size == 0 {
				return b, nil // 非法首字节,原样交出(交给下游替换)
			}
			if i+size > n {
				return b[:i], b[i:]
			}
			return b, nil
		}
	}
	return b, nil
}

// utf8SeqLen 按 UTF-8 首字节返回序列长度(0 = 非法首字节)。
func utf8SeqLen(c byte) int {
	switch {
	case c&0xE0 == 0xC0:
		return 2
	case c&0xF0 == 0xE0:
		return 3
	case c&0xF8 == 0xF0:
		return 4
	default:
		return 0
	}
}
