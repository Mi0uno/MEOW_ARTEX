package server

// C2 正向连接(bind)API(契约 C2_CONTRACTS.md §18,Agent T):
//
//	POST /api/c2/sessions/connect {listener_id, host, port}
//	  → 平台以该监听器的密钥/协议主动 dial 目标上监听的 bind beacon/stager,
//	    完成首帧 reg 握手建会话(transport=tcp/kcp,meta.direction=bind)。
//	  → 200 {session_id};失败原样报错(400/404/409/500)。
//
// 仅 tcp/kcp 支持(c2center.NewOutboundConn 对其余协议返回明确错误,这里透传)。

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

// c2ConnectSession POST /api/c2/sessions/connect — 正向连接并建会话。
func (s *Server) c2ConnectSession(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	mgr := s.c2Center(w)
	if mgr == nil {
		return
	}
	var req struct {
		ListenerID int64  `json:"listener_id"`
		Host       string `json:"host"`
		Port       string `json:"port"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if req.ListenerID <= 0 {
		writeErr(w, 400, "listener_id 必填")
		return
	}
	if strings.TrimSpace(req.Host) == "" {
		writeErr(w, 400, "host 必填")
		return
	}
	if strings.TrimSpace(req.Port) == "" {
		writeErr(w, 400, "port 必填")
		return
	}
	l, err := s.m.PG().GetC2Listener(req.ListenerID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if l == nil {
		writeErr(w, 404, "监听器不存在")
		return
	}
	sessID, err := mgr.ConnectBind(c2centerListenerFromDB(l), strings.TrimSpace(req.Host), strings.TrimSpace(req.Port))
	if err != nil {
		// 失败原样报错(含「协议不支持正向」「监听器未运行」「连接被拒」)。
		log.Printf("[c2-center] 正向连接监听器 #%d → %s:%s 失败: %v", req.ListenerID, req.Host, req.Port, err)
		writeErr(w, 400, err.Error())
		return
	}
	sess, err := s.m.PG().GetC2Session(sessID)
	if err != nil || sess == nil {
		writeErr(w, 500, "会话已建立但回读失败")
		return
	}
	log.Printf("[c2-center] 正向连接成功:监听器 #%d → %s:%s,会话 #%d(%s/%s)",
		req.ListenerID, req.Host, req.Port, sess.ID, sess.Hostname, sess.Username)
	writeJSON(w, 200, map[string]any{"session_id": sess.ID})
}
