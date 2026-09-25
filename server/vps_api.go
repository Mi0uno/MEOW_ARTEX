package server

// VPS 中心 REST 接口(契约 §4;路径/负载与 web/src/lib/api.ts 冻结版对齐)。
//
// 五块:
//   仪表盘   /api/vps/dashboard                总览 + 执行量序列 + 占用机器 + 最近流水
//   机器池   /api/vps/machines…                录入(同步首测)/编辑/删除/测活/工具检测/详情
//   执行     /api/vps/exec、/api/vps/executions… 单命令执行 + 流水 + 完整输出
//   指派历史 /api/vps/assignments              (0=全部机器)
//   工具集   /api/vps/toolsets…                CRUD + 一键应用(builtin 拒改删)
//
// 铁律:凭据明文永不回显(响应 DTO 无 password/private_key 字段);集合字段
// nil 一律初始化为空 slice;写错统一 writeErr。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/vpscenter"
)

// registerVPSRoutes 注册 VPS 中心全部路由(含 WebSocket 终端,见 vps_terminal.go)。
// 已核实 requireAuth/noStore/cors 中间件都是原样透传 ResponseWriter(不包装),
// http.Hijacker 能力保留,WebSocket 升级可安全挂在中间件链内(?token= 由
// requireAuth 的 extractToken 校验)。
func (s *Server) registerVPSRoutes(mux *http.ServeMux) {
	// ---- 仪表盘 ----
	mux.HandleFunc("GET /api/vps/dashboard", s.vpsDashboard)

	// ---- 机器池 ----
	mux.HandleFunc("GET /api/vps/machines", s.listVpsMachines)
	mux.HandleFunc("POST /api/vps/machines", s.createVpsMachine)
	mux.HandleFunc("GET /api/vps/machines/{id}", s.getVpsMachine)
	mux.HandleFunc("PUT /api/vps/machines/{id}", s.updateVpsMachine)
	mux.HandleFunc("DELETE /api/vps/machines/{id}", s.deleteVpsMachine)
	mux.HandleFunc("POST /api/vps/machines/{id}/check", s.checkVpsMachine)
	mux.HandleFunc("POST /api/vps/machines/{id}/detect", s.detectVpsMachine)
	mux.HandleFunc("GET /api/vps/machines/{id}/terminal", s.vpsTerminal)

	// ---- 执行与流水 ----
	mux.HandleFunc("POST /api/vps/exec", s.vpsExec)
	mux.HandleFunc("GET /api/vps/executions", s.listVpsExecutions)
	mux.HandleFunc("GET /api/vps/executions/{id}/output", s.vpsExecutionOutput)

	// ---- 指派历史 ----
	mux.HandleFunc("GET /api/vps/assignments", s.listVpsAssignments)

	// ---- 工具集 ----
	mux.HandleFunc("GET /api/vps/toolsets", s.listVpsToolsets)
	mux.HandleFunc("POST /api/vps/toolsets", s.createVpsToolset)
	mux.HandleFunc("PUT /api/vps/toolsets/{id}", s.updateVpsToolset)
	mux.HandleFunc("DELETE /api/vps/toolsets/{id}", s.deleteVpsToolset)
	mux.HandleFunc("POST /api/vps/toolsets/{id}/apply", s.applyVpsToolset)
}

// vpsReady 检查 VPS 中心是否可用(统一 503 出口)。
func (s *Server) vpsReady(w http.ResponseWriter) bool {
	if s.m == nil || s.m.PG() == nil {
		writeErr(w, 503, "数据库未初始化")
		return false
	}
	return true
}

// vpsCenter 取运行时(未初始化时回 503)。
func (s *Server) vpsCenter(w http.ResponseWriter) *vpscenter.Manager {
	if mgr := VPSManager(); mgr != nil {
		return mgr
	}
	writeErr(w, 503, "VPS 中心未初始化")
	return nil
}

// vpsPathID 解析路径里的机器/工具集 id。
func vpsPathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "无效的 id")
		return 0, false
	}
	return id, true
}

// ---- 仪表盘 ----

// vpsDashboard GET /api/vps/dashboard?window_h= — 一次返回总览 + 小时桶执行量
// 序列 + 当前占用机器 + 最近流水(前端一次请求取齐,仿 proxyDashboard)。
func (s *Server) vpsDashboard(w http.ResponseWriter, r *http.Request) {
	if !s.vpsReady(w) {
		return
	}
	windowH := atoiDefault(r.URL.Query().Get("window_h"), 24)
	if windowH <= 0 || windowH > 24*30 {
		windowH = 24
	}
	since := time.Now().Add(-time.Duration(windowH) * time.Hour)

	pg := s.m.PG()
	summary, err := pg.VpsDashboardSummary()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	series, err := pg.VpsExecutionSeries(since)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	busy, err := pg.ListVpsBusyMachines()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	recent, err := pg.ListRecentVpsExecutions(20)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"summary":       summary,
		"series":        nonNilPtrSlice(series),
		"busy_machines": nonNilPtrSlice(busy),
		"recent":        nonNilPtrSlice(recent),
	})
}

// nonNilPtrSlice 保证集合字段永不序列化成 null。
func nonNilPtrSlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// ---- 机器池 ----

// listVpsMachines GET /api/vps/machines?status=&provider=&tag=&q=&limit=&offset=
func (s *Server) listVpsMachines(w http.ResponseWriter, r *http.Request) {
	if !s.vpsReady(w) {
		return
	}
	q := r.URL.Query()
	items, total, err := s.m.PG().ListVpsMachines(pgdb.VpsMachineFilter{
		Status:   strings.TrimSpace(q.Get("status")),
		Provider: strings.TrimSpace(q.Get("provider")),
		Tag:      strings.TrimSpace(q.Get("tag")),
		Q:        strings.TrimSpace(q.Get("q")),
		Limit:    atoiDefault(q.Get("limit"), 0),
		Offset:   atoiDefault(q.Get("offset"), 0),
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"machines": nonNilPtrSlice(items), "total": total})
}

// vpsMachinePayload 是录入/编辑机器的请求体。
// 凭据字段只在创建时或显式提交时出现,服务端加密入库、响应永不回传
// (留空 = 不修改已存凭据)。
type vpsMachinePayload struct {
	Name          string   `json:"name"`
	Host          string   `json:"host"`
	Port          int      `json:"port"`
	Username      string   `json:"username"`
	AuthType      string   `json:"auth_type"`
	Password      *string  `json:"password"`
	PrivateKey    *string  `json:"private_key"`
	KeyPassphrase *string  `json:"key_passphrase"`
	Region        string   `json:"region"`
	Tags          []string `json:"tags"`
	WorkspaceBase string   `json:"workspace_base"`
	Notes         string   `json:"notes"`
	Status        string   `json:"status"`
}

// sealedCreds 按需加密凭据(空指针或空串 = 不改动)。
func (s *Server) sealedCreds(p vpsMachinePayload) (pw, key, phrase []byte, err error) {
	seal := func(v *string) ([]byte, error) {
		if v == nil || *v == "" {
			return nil, nil
		}
		return s.sealVPSCredential(*v)
	}
	if pw, err = seal(p.Password); err != nil {
		return nil, nil, nil, err
	}
	if key, err = seal(p.PrivateKey); err != nil {
		return nil, nil, nil, err
	}
	if phrase, err = seal(p.KeyPassphrase); err != nil {
		return nil, nil, nil, err
	}
	return pw, key, phrase, nil
}

// validateAuthCreds 校验「auth_type=password 时必须有密码,key 时必须有私钥」。
// existingHasPassword/existingHasKey 描述库里已存的凭据(编辑场景)。
func validateAuthCreds(authType string, pw, key []byte, existingHasPassword, existingHasKey bool) error {
	switch authType {
	case pgdb.VpsAuthPassword:
		if len(pw) == 0 && !existingHasPassword {
			return errors.New("密码认证必须提供密码")
		}
	case pgdb.VpsAuthKey:
		if len(key) == 0 && !existingHasKey {
			return errors.New("私钥认证必须提供私钥")
		}
	}
	return nil
}

// createVpsMachine POST /api/vps/machines — 录入并同步首测(超时 15s)。
// 测活失败也入库(status=offline + last_error 随响应带回)。
func (s *Server) createVpsMachine(w http.ResponseWriter, r *http.Request) {
	if !s.vpsReady(w) {
		return
	}
	var p vpsMachinePayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if p.AuthType == "" {
		p.AuthType = pgdb.VpsAuthPassword
	}
	pw, key, phrase, err := s.sealedCreds(p)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := validateAuthCreds(p.AuthType, pw, key, false, false); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	m, err := s.m.PG().CreateVpsMachine(pgdb.VpsMachineInput{
		Name: p.Name, Host: p.Host, Port: p.Port, Username: p.Username, AuthType: p.AuthType,
		PasswordSealed: pw, PrivateKeySealed: key, KeyPassphraseSealed: phrase,
		Region: p.Region, Tags: p.Tags, WorkspaceBase: p.WorkspaceBase, Notes: p.Notes,
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	log.Printf("[vps-center] 机器 «%s»(#%d) 已录入,开始同步首测", m.Name, m.ID)

	// 同步首测:15s 上限,结果(online/offline + 系统档案)直接落库。
	// 用独立 ctx:客户端提前断开也不影响首测结果入库。
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if mgr := s.vpsCenter(w); mgr != nil {
		if _, cerr := mgr.CheckMachine(ctx, m.ID); cerr != nil {
			log.Printf("[vps-center] 机器 «%s» 首测异常: %v", m.Name, cerr)
		}
	}
	fresh, err := s.m.PG().GetVpsMachine(m.ID)
	if err != nil || fresh == nil {
		writeErr(w, 500, "机器已录入但回读失败")
		return
	}
	writeJSON(w, 201, vpsMachineDTO(fresh))
}

// updateVpsMachine PUT /api/vps/machines/{id} — 编辑;凭据字段缺省=不改。
func (s *Server) updateVpsMachine(w http.ResponseWriter, r *http.Request) {
	if !s.vpsReady(w) {
		return
	}
	id, ok := vpsPathID(w, r)
	if !ok {
		return
	}
	existing, err := s.m.PG().GetVpsMachine(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if existing == nil {
		writeErr(w, 404, "机器不存在")
		return
	}
	var p vpsMachinePayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	pw, key, phrase, err := s.sealedCreds(p)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// 编辑时密码/私钥输入框留空 = 不修改;auth_type 变更后必须仍有对应凭据。
	authType := p.AuthType
	if authType == "" {
		authType = existing.AuthType
	}
	existingPw, existingKey, _, gerr := s.m.PG().GetVpsMachineSecrets(id)
	if gerr != nil {
		writeErr(w, 500, gerr.Error())
		return
	}
	hasPw := pw != nil || existingPw != nil
	hasKey := key != nil || existingKey != nil
	if err := validateAuthCreds(authType, pw, key, hasPw, hasKey); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	name, host, username := p.Name, p.Host, p.Username
	if name == "" {
		name = existing.Name
	}
	if host == "" {
		host = existing.Host
	}
	if username == "" {
		username = existing.Username
	}
	port := p.Port
	if port <= 0 {
		port = existing.Port
	}
	wsBase := p.WorkspaceBase
	if wsBase == "" {
		wsBase = existing.WorkspaceBase
	}
	if err := s.m.PG().UpdateVpsMachine(id, pgdb.VpsMachineUpdate{
		Name: name, Host: host, Port: port, Username: username, AuthType: authType,
		PasswordSealed: pw, PrivateKeySealed: key, KeyPassphraseSealed: phrase,
		Region: p.Region, Tags: p.Tags, WorkspaceBase: wsBase, Notes: p.Notes,
		Status: p.Status,
	}); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	fresh, err := s.m.PG().GetVpsMachine(id)
	if err != nil || fresh == nil {
		writeErr(w, 500, "机器已更新但回读失败")
		return
	}
	log.Printf("[vps-center] 机器 «%s»(#%d) 已编辑", fresh.Name, id)
	writeJSON(w, 200, vpsMachineDTO(fresh))
}

// deleteVpsMachine DELETE /api/vps/machines/{id} — 被占用时 409 拒绝。
func (s *Server) deleteVpsMachine(w http.ResponseWriter, r *http.Request) {
	if !s.vpsReady(w) {
		return
	}
	id, ok := vpsPathID(w, r)
	if !ok {
		return
	}
	m, err := s.m.PG().GetVpsMachine(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if m == nil {
		writeErr(w, 404, "机器不存在")
		return
	}
	if m.AssignedTaskID != 0 {
		writeErr(w, 409, fmt.Sprintf("机器正被任务 #%d 占用,请先解绑再删除", m.AssignedTaskID))
		return
	}
	deleted, err := s.m.PG().DeleteVpsMachine(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	log.Printf("[vps-center] 机器 «%s»(#%d) 已删除", m.Name, id)
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

// checkVpsMachine POST /api/vps/machines/{id}/check — 手动测活(同步)。
func (s *Server) checkVpsMachine(w http.ResponseWriter, r *http.Request) {
	if !s.vpsReady(w) {
		return
	}
	id, ok := vpsPathID(w, r)
	if !ok {
		return
	}
	mgr := s.vpsCenter(w)
	if mgr == nil {
		return
	}
	// 独立 ctx:客户端超时断开也让测活结果落库完成。
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m, err := mgr.CheckMachine(ctx, id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if m == nil {
		writeErr(w, 404, "机器不存在")
		return
	}
	// api.ts 的 checkVpsMachine 直接以机器对象为响应体(不包 machine 键)。
	// CheckMachine 返回的是 vpscenter 运行时视图(无 json tag),必须过 DTO 转换。
	writeJSON(w, 200, vpsRuntimeMachineDTO(m))
}

// detectVpsMachine POST /api/vps/machines/{id}/detect — 工具检测(同步)。
func (s *Server) detectVpsMachine(w http.ResponseWriter, r *http.Request) {
	if !s.vpsReady(w) {
		return
	}
	id, ok := vpsPathID(w, r)
	if !ok {
		return
	}
	mgr := s.vpsCenter(w)
	if mgr == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	dets, err := mgr.DetectMachine(ctx, id)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	if dets == nil {
		dets = []vpscenter.ToolDetection{}
	}
	writeJSON(w, 200, map[string]any{"detections": dets})
}

// getVpsMachine GET /api/vps/machines/{id} — 详情(机器 + 检测矩阵 + 当前任务)。
func (s *Server) getVpsMachine(w http.ResponseWriter, r *http.Request) {
	if !s.vpsReady(w) {
		return
	}
	id, ok := vpsPathID(w, r)
	if !ok {
		return
	}
	m, err := s.m.PG().GetVpsMachine(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if m == nil {
		writeErr(w, 404, "机器不存在")
		return
	}
	dets, err := s.m.PG().ListVpsToolDetections(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var currentTask any
	if m.AssignedTaskID > 0 {
		currentTask = map[string]any{"id": m.AssignedTaskID, "name": m.AssignedTaskName}
	} else {
		currentTask = nil
	}
	writeJSON(w, 200, map[string]any{
		"machine":      vpsMachineDTO(m),
		"detections":   nonNilPtrSlice(dets),
		"current_task": currentTask,
	})
}

// ---- 执行与流水 ----

// vpsExec POST /api/vps/exec — 单命令执行 {machine_id, command, timeout_sec?},
// 默认 120s、上限 600s。响应 {exit_code, duration_ms, output, truncated}。
func (s *Server) vpsExec(w http.ResponseWriter, r *http.Request) {
	if !s.vpsReady(w) {
		return
	}
	mgr := s.vpsCenter(w)
	if mgr == nil {
		return
	}
	var req struct {
		MachineID  int64  `json:"machine_id"`
		Command    string `json:"command"`
		TimeoutSec int    `json:"timeout_sec"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if req.MachineID <= 0 {
		writeErr(w, 400, "machine_id 必填")
		return
	}
	if strings.TrimSpace(req.Command) == "" {
		writeErr(w, 400, "command 不能为空")
		return
	}
	if req.TimeoutSec <= 0 {
		req.TimeoutSec = 120
	}
	if req.TimeoutSec > 600 {
		req.TimeoutSec = 600
	}
	timeout := time.Duration(req.TimeoutSec) * time.Second
	start := time.Now()
	// 独立 ctx:请求先断也不影响流水落库。
	execCtx, cancelExec := context.WithTimeout(context.Background(), timeout+30*time.Second)
	defer cancelExec()
	out, code, err := mgr.ExecCommand(execCtx,
		req.MachineID, 0, vpscenter.SourceManual, req.Command, timeout)
	if err != nil {
		// 连接/传输失败是「机器的问题」,502 带原因;命令自身非零退出不算错误。
		writeErr(w, 502, err.Error())
		return
	}
	// 响应输出截 256KB(与 agent 侧截尾口径一致;完整输出在流水文件里)。
	const respCap = 256 << 10
	truncated := false
	respOut := out
	if len(respOut) > respCap {
		respOut = respOut[:respCap]
		truncated = true
	}
	var exitCode any
	if code >= 0 {
		exitCode = code
	} else {
		exitCode = nil
	}
	writeJSON(w, 200, map[string]any{
		"exit_code":   exitCode,
		"duration_ms": time.Since(start).Milliseconds(),
		"output":      string(respOut),
		"truncated":   truncated,
	})
}

// listVpsExecutions GET /api/vps/executions?machine_id=&task_id=&source=&limit=&offset=
func (s *Server) listVpsExecutions(w http.ResponseWriter, r *http.Request) {
	if !s.vpsReady(w) {
		return
	}
	q := r.URL.Query()
	machineID, _ := strconv.ParseInt(q.Get("machine_id"), 10, 64)
	taskID, _ := strconv.ParseInt(q.Get("task_id"), 10, 64)
	items, total, err := s.m.PG().ListVpsExecutions(pgdb.VpsExecutionFilter{
		MachineID: machineID,
		TaskID:    taskID,
		Source:    strings.TrimSpace(q.Get("source")),
		Limit:     atoiDefault(q.Get("limit"), 0),
		Offset:    atoiDefault(q.Get("offset"), 0),
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"executions": nonNilPtrSlice(items), "total": total})
}

// vpsExecutionOutput GET /api/vps/executions/{id}/output — 完整输出(text/plain)。
func (s *Server) vpsExecutionOutput(w http.ResponseWriter, r *http.Request) {
	if !s.vpsReady(w) {
		return
	}
	id, ok := vpsPathID(w, r)
	if !ok {
		return
	}
	rec, err := s.m.PG().GetVpsExecution(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if rec == nil {
		writeErr(w, 404, "流水不存在")
		return
	}
	mgr := VPSManager()
	path := ""
	if mgr != nil {
		path = mgr.OutputPath(id)
	}
	if path == "" || fileMissing(path) {
		if rec.OutputPath != "" && !fileMissing(rec.OutputPath) {
			path = rec.OutputPath
		}
	}
	if path == "" || fileMissing(path) {
		writeErr(w, 404, "该流水没有落盘输出")
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		writeErr(w, 500, "读取输出文件失败: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(200)
	_, _ = w.Write(data)
}

func fileMissing(path string) bool {
	_, err := os.Stat(path)
	return err != nil
}

// ---- 指派历史 ----

// listVpsAssignments GET /api/vps/assignments?machine_id=&limit=(machine_id=0 全部)
func (s *Server) listVpsAssignments(w http.ResponseWriter, r *http.Request) {
	if !s.vpsReady(w) {
		return
	}
	q := r.URL.Query()
	machineID, _ := strconv.ParseInt(q.Get("machine_id"), 10, 64)
	items, err := s.m.PG().ListVpsAssignments(machineID, atoiDefault(q.Get("limit"), 0))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"assignments": nonNilPtrSlice(items)})
}

// ---- 工具集 ----

// listVpsToolsets GET /api/vps/toolsets
func (s *Server) listVpsToolsets(w http.ResponseWriter, r *http.Request) {
	if !s.vpsReady(w) {
		return
	}
	items, err := s.m.PG().ListVpsToolsets()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"toolsets": nonNilPtrSlice(items)})
}

// vpsToolsetPayload 是工具集的请求体。
type vpsToolsetPayload struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Script      string `json:"script"`
}

// createVpsToolset POST /api/vps/toolsets
func (s *Server) createVpsToolset(w http.ResponseWriter, r *http.Request) {
	if !s.vpsReady(w) {
		return
	}
	var p vpsToolsetPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	t, err := s.m.PG().CreateVpsToolset(pgdb.VpsToolsetInput(p), false)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, t)
}

// updateVpsToolset PUT /api/vps/toolsets/{id} — builtin 拒改(400)。
func (s *Server) updateVpsToolset(w http.ResponseWriter, r *http.Request) {
	if !s.vpsReady(w) {
		return
	}
	id, ok := vpsPathID(w, r)
	if !ok {
		return
	}
	existing, err := s.m.PG().GetVpsToolset(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if existing == nil {
		writeErr(w, 404, "工具集不存在")
		return
	}
	if existing.Builtin {
		writeErr(w, 400, "内置工具集不可修改")
		return
	}
	var p vpsToolsetPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	// 局部更新语义:未提供的字段(空串)保持原值。
	if p.Name == "" {
		p.Name = existing.Name
	}
	if p.Script == "" {
		p.Script = existing.Script
	}
	if p.Description == "" {
		p.Description = existing.Description
	}
	if err := s.m.PG().UpdateVpsToolset(id, pgdb.VpsToolsetInput{
		Name: p.Name, Description: p.Description, Script: p.Script,
	}); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	t, err := s.m.PG().GetVpsToolset(id)
	if err != nil || t == nil {
		writeErr(w, 500, "工具集已更新但回读失败")
		return
	}
	writeJSON(w, 200, t)
}

// deleteVpsToolset DELETE /api/vps/toolsets/{id} — builtin 拒删(400)。
func (s *Server) deleteVpsToolset(w http.ResponseWriter, r *http.Request) {
	if !s.vpsReady(w) {
		return
	}
	id, ok := vpsPathID(w, r)
	if !ok {
		return
	}
	existing, err := s.m.PG().GetVpsToolset(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if existing == nil {
		writeErr(w, 404, "工具集不存在")
		return
	}
	if existing.Builtin {
		writeErr(w, 400, "内置工具集不可删除")
		return
	}
	deleted, err := s.m.PG().DeleteVpsToolset(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

// applyVpsToolset POST /api/vps/toolsets/{id}/apply — {machine_ids[]} 逐台同步执行。
// 单台失败不中断整批,逐台返回结果(脚本可能要装包编译,总耗时较长)。
func (s *Server) applyVpsToolset(w http.ResponseWriter, r *http.Request) {
	if !s.vpsReady(w) {
		return
	}
	id, ok := vpsPathID(w, r)
	if !ok {
		return
	}
	mgr := s.vpsCenter(w)
	if mgr == nil {
		return
	}
	ts, err := s.m.PG().GetVpsToolset(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if ts == nil {
		writeErr(w, 404, "工具集不存在")
		return
	}
	var req struct {
		MachineIDs []int64 `json:"machine_ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if len(req.MachineIDs) == 0 {
		writeErr(w, 400, "machine_ids 不能为空")
		return
	}
	results := make([]map[string]any, 0, len(req.MachineIDs))
	for _, mid := range req.MachineIDs {
		applyCtx, cancelApply := context.WithTimeout(context.Background(), 45*time.Minute)
		execID, _, code, aerr := mgr.ApplyToolset(applyCtx, mid, id)
		cancelApply()
		res := map[string]any{
			"machine_id":   mid,
			"ok":           aerr == nil,
			"execution_id": execID,
		}
		if aerr != nil {
			res["error"] = aerr.Error()
			res["exit_code"] = nil
		} else {
			res["error"] = ""
			if code >= 0 {
				res["exit_code"] = code
			} else {
				res["exit_code"] = nil
			}
		}
		results = append(results, res)
	}
	writeJSON(w, 200, map[string]any{"results": results})
}
