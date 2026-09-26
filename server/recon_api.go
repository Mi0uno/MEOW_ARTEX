package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/recon"
)

// ---- 信息搜集 API(stage=recon 任务的执行面) ----

// reconCollectorMeta GET /api/recon/collectors — 已注册收集器元数据(前端流水线编排)。
func (s *Server) reconCollectorMeta(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"collectors": recon.Descriptions()})
}

// reconPlanHandler POST /api/recon/plan — 自动检测可用数据源 + 生成执行计划。
// body: {company_ids:[...], profile_id?:N, use_ai?:bool}
// 创建信息搜集任务前用它预览「将自动调用哪些数据源」:已配置且启用的外部接口全部
// 纳入,search 型数据源的 query 由 AI 生成(失败回退内置默认),未配置的记原因跳过。
func (s *Server) reconPlanHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CompanyIDs []int64 `json:"company_ids"`
		ProfileID  int64   `json:"profile_id"`
		UseAI      *bool   `json:"use_ai"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	if len(req.CompanyIDs) == 0 {
		writeErr(w, 400, "请至少选择一个企业作为目标")
		return
	}
	useAI := req.UseAI == nil || *req.UseAI
	profileID := req.ProfileID
	if profileID == 0 {
		// 未显式指定时用全局激活配置(与任务侧解析顺序一致)。
		if p, err := s.m.pg.ActiveProfile(); err == nil && p != nil {
			profileID = p.ID
		}
	}
	plan, err := s.ReconPlanFor(r.Context(), req.CompanyIDs, profileID, useAI)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, plan)
}

// taskCollectorRuns GET /api/tasks/{id}/collector-runs — 流水线各收集器执行轨迹。
func (s *Server) taskCollectorRuns(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	runs, err := s.m.PG().ListCollectorRuns(taskNumericID(t.ID))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if runs == nil {
		runs = []*db.CollectorRun{}
	}
	writeJSON(w, 200, map[string]any{"runs": runs})
}

// taskStagedAssets GET /api/tasks/{id}/staged-assets?status= — 暂存区列表 + 计数。
func (s *Server) taskStagedAssets(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	status := r.URL.Query().Get("status")
	assets, err := s.m.PG().ListStagedAssets(taskNumericID(t.ID), status)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	pending, accepted, ignored, err := s.m.PG().CountStagedAssets(taskNumericID(t.ID))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if assets == nil {
		assets = []*db.StagedAsset{}
	}
	writeJSON(w, 200, map[string]any{
		"assets": assets,
		"counts": map[string]int{"pending": pending, "accepted": accepted, "ignored": ignored},
	})
}

// stagedAssetDecide POST /api/tasks/{id}/staged-assets/decide — 暂存资产采纳/忽略。
// body: {"ids":[...], "action":"adopt"|"ignore"};ids 省略时作用于全部 pending。
// adopt 把 pending 资产 upsert 进正式资产图(staged→assets,幂等去重),成功后落
// accepted;单条 upsert 失败仅记错误、不中断。ignore 直接落 ignored。
// 已落定(accepted/ignored)的 id 静默跳过,重复提交不产生副作用。
func (s *Server) stagedAssetDecide(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	as := s.assetStore()
	if as == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	var req struct {
		IDs    []int64 `json:"ids"`
		Action string  `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if req.Action != "adopt" && req.Action != "ignore" {
		writeErr(w, 400, "action 必须是 adopt 或 ignore")
		return
	}
	taskID := taskNumericID(t.ID)
	assets, err := s.m.PG().ListPendingStagedAssets(taskID, req.IDs)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}

	type decideResult struct {
		ID      int64  `json:"id"`
		AssetID int64  `json:"asset_id,omitempty"`
		Status  string `json:"status"`
	}
	type decideError struct {
		ID    int64  `json:"id"`
		Value string `json:"value"`
		Error string `json:"error"`
	}
	var results []decideResult
	var errs []decideError
	target := "ignored"
	if req.Action == "adopt" {
		target = "accepted"
	}
	for _, a := range assets {
		if req.Action == "adopt" {
			assetID, err := s.adoptStagedAsset(as, taskID, a, 0)
			if err != nil {
				errs = append(errs, decideError{ID: a.ID, Value: a.Value, Error: err.Error()})
				continue
			}
			_ = as.SetTaskAssetSource(taskID, assetID, "recon",
				fmt.Sprintf("信息搜集采纳 · %s · %s", a.Collector, a.Value), nil)
			if _, err := s.m.PG().DecideStagedAsset(taskID, a.ID, target); err != nil {
				errs = append(errs, decideError{ID: a.ID, Value: a.Value, Error: "落定状态失败: " + err.Error()})
				continue
			}
			results = append(results, decideResult{ID: a.ID, AssetID: assetID, Status: target})
		} else {
			if _, err := s.m.PG().DecideStagedAsset(taskID, a.ID, target); err != nil {
				errs = append(errs, decideError{ID: a.ID, Value: a.Value, Error: "落定状态失败: " + err.Error()})
				continue
			}
			results = append(results, decideResult{ID: a.ID, Status: target})
		}
	}
	pending, accepted, ignored, err := s.m.PG().CountStagedAssets(taskID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"action":            req.Action,
		"results":           results,
		"errors":            errs,
		"counts":            map[string]int{"pending": pending, "accepted": accepted, "ignored": ignored},
		"remaining_pending": pending,
	})
}

// adoptStagedAsset 把一条暂存资产按类型映射进正式资产图。service 类型优先走
// extra.url(http 服务,URL 内自动提取域名/端口/根域名),否则按 host:port 归其他服务。
// companyID>0 时给每个 Upsert*Req 强制归属该企业(跳过 ICP/范围自动认领,与资产
// 导入 semantics 一致);=0 时行为与既有完全一致(契约 ASSET_EXTRACT_CONTRACTS.md §4)。
func (s *Server) adoptStagedAsset(as *db.AssetStore, taskID int64, a *db.StagedAsset, companyID int64) (int64, error) {
	extra := map[string]any{}
	_ = json.Unmarshal(a.Extra, &extra)
	extraStr := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := extra[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	switch a.Type {
	case "root_domain":
		return as.UpsertRootDomain(db.UpsertRootDomainReq{Domain: a.Value, TaskID: taskID, CompanyID: companyID})
	case "domain":
		return as.UpsertSubdomain(db.UpsertSubdomainReq{Domain: a.Value, TaskID: taskID, CompanyID: companyID})
	case "ip":
		var ports []db.PortService
		if a.Port > 0 {
			ports = append(ports, db.PortService{Port: a.Port, Service: extraStr("service", "service_name")})
		}
		return as.UpsertIP(db.UpsertIPReq{IP: a.Value, OpenPorts: ports, TaskID: taskID, CompanyID: companyID})
	case "service":
		if u := extraStr("url"); u != "" {
			ip := ""
			if net.ParseIP(a.Value) != nil {
				ip = a.Value
			}
			return as.UpsertHTTPService(db.UpsertHTTPServiceReq{URL: u, IP: ip, TaskID: taskID, CompanyID: companyID})
		}
		req := db.UpsertOtherServiceReq{Port: a.Port, ServiceName: extraStr("service", "service_name"), TaskID: taskID, CompanyID: companyID}
		if net.ParseIP(a.Value) != nil {
			req.IP = a.Value
		} else {
			req.Domain = a.Value
		}
		return as.UpsertOtherService(req)
	case "app":
		name := extraStr("name", "app_name")
		if name == "" {
			name = a.Value
		}
		appReq := db.UpsertAppReq{
			Name:        name,
			BundleID:    extraStr("bundle_id"),
			Category:    extraStr("category"),
			Description: extraStr("description"),
			ICP:         extraStr("icp", "app_icp"),
			TaskID:      taskID,
		}
		// UpsertAppReq 的 CompanyID 是 *int64(显式覆盖语义,与其他 Req 的零值=旧行为不同)。
		if companyID > 0 {
			id := companyID
			appReq.CompanyID = &id
		}
		return as.UpsertApp(appReq)
	}
	return 0, fmt.Errorf("未知暂存类型 %q", a.Type)
}

// rerunReconTask POST /api/tasks/{id}/recon/rerun — 终态流水线重跑(新批次)。
func (s *Server) rerunReconTaskHandler(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if t.Stage != "recon" {
		writeErr(w, 400, "仅信息搜集任务可重跑流水线")
		return
	}
	if err := s.rerunReconTask(t); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	log.Printf("[recon] task %s: 已触发重跑", t.ID)
	writeJSON(w, 200, map[string]any{"id": t.ID, "rerun": true})
}

// taskCollectorRunRaw GET /api/tasks/{id}/collector-runs/{runId}/raw —
// 原始输出落盘内容下载/预览(text/plain)。仅允许访问本任务自己的 run。
func (s *Server) taskCollectorRunRaw(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	runID, err := strconv.ParseInt(r.PathValue("runId"), 10, 64)
	if err != nil || runID <= 0 {
		writeErr(w, 400, "bad run id")
		return
	}
	run, err := s.m.PG().GetCollectorRun(taskNumericID(t.ID), runID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if run == nil {
		writeErr(w, 404, "run not found")
		return
	}
	if run.RawEvidenceHash == "" {
		writeErr(w, 404, "该执行没有原始输出")
		return
	}
	f, _, err := s.evidenceStore().OpenRaw(run.RawEvidenceHash)
	if err != nil {
		writeErr(w, 404, "原始输出 blob 不存在")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=raw-%s-run%d.txt", run.Collector, run.ID))
	http.ServeContent(w, r, "", time.Time{}, f)
}

// reconExecute POST /api/recon/execute — 手动执行一次信息搜集工具
// (fofa/hunter/crtsh),结果直接返回,同时落 tool_executions 历史 + 原始输出留证。
func (s *Server) reconExecute(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tool      string         `json:"tool"`
		Config    map[string]any `json:"config"`
		CompanyID *int64         `json:"company_id,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	req.Tool = strings.TrimSpace(req.Tool)
	collector := recon.Get(req.Tool)
	if collector == nil {
		writeErr(w, 400, "未知工具: "+req.Tool)
		return
	}
	execID, err := s.m.PG().CreateToolExecution("recon", req.Tool, jsonRaw(req.Config))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// 凭据注入(仅 fofa/hunter 需要;缺配置会在 resolve 处给出明确错误)。
	secrets, secretErr := s.resolveExternalSecrets(req.Tool, req.Config)
	if secretErr != nil {
		_ = s.m.PG().FinishToolExecution(execID, "failed", jsonRaw(map[string]any{"error": secretErr.Error()}), "")
		writeJSON(w, 200, map[string]any{"id": execID, "status": "failed", "error": secretErr.Error()})
		return
	}
	// 字典路径注入(subdomain_brute 用)。
	if err := s.resolveWordbookPath(req.Config); err != nil {
		_ = s.m.PG().FinishToolExecution(execID, "failed", jsonRaw(map[string]any{"error": err.Error()}), "")
		writeJSON(w, 200, map[string]any{"id": execID, "status": "failed", "error": err.Error()})
		return
	}
	// 企业目标:手动执行可关联企业(crtsh 用它的根域名;fofa/hunter 不强求)。
	var targets []recon.CompanyTarget
	if req.CompanyID != nil && *req.CompanyID > 0 {
		if t, err := s.companyTargets([]int64{*req.CompanyID}); err == nil {
			targets = t
		}
	}
	result, runErr := collector.Collect(r.Context(), recon.CollectorInput{
		TaskID:    0,
		Companies: targets,
		Config:    req.Config,
		Secrets:   secrets,
		RawSink:   s.reconSink,
	})
	if runErr != nil {
		_ = s.m.PG().FinishToolExecution(execID, "failed", jsonRaw(map[string]any{"error": runErr.Error()}), "")
		writeJSON(w, 200, map[string]any{"id": execID, "status": "failed", "error": runErr.Error()})
		return
	}
	_ = s.m.PG().FinishToolExecution(execID, "done",
		jsonRaw(map[string]any{"total": len(result.Assets), "stats": result.Stats}), result.RawHash)
	if len(result.Assets) > 200 {
		result.Assets = result.Assets[:200]
	}
	writeJSON(w, 200, map[string]any{
		"id": execID, "status": "done", "assets": result.Assets,
		"stats": result.Stats, "raw_evidence_hash": result.RawHash,
	})
}

// reconExecuteList GET /api/recon/execute — 工具执行历史(最新在前)。
func (s *Server) reconExecuteList(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := s.m.PG().ListToolExecutions("recon", limit)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if rows == nil {
		rows = []*db.ToolExecution{}
	}
	writeJSON(w, 200, map[string]any{"executions": rows})
}

// jsonRaw 序列化兜底:失败回退空对象。
func jsonRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

// wordbookFilePath 按 id 推导字典文件路径。id 只能是正整数(调用方 ParseInt 保证),
// 这里再做一层防御:拼接后校验路径必须落在 wordbooks 目录内,杜绝任何穿越可能。
func wordbookFilePath(dataDir string, id int64) string {
	if id <= 0 {
		return ""
	}
	dir := filepath.Join(dataDir, "wordbooks")
	p := filepath.Clean(filepath.Join(dir, strconv.FormatInt(id, 10)+".txt"))
	if !strings.HasPrefix(p, dir+string(filepath.Separator)) {
		return ""
	}
	return p
}

// reconWordbookContent GET /api/recon/wordbooks/{id}/content — 全文内容(text/plain)。
func (s *Server) reconWordbookContent(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad id")
		return
	}
	wb, err := s.m.PG().GetWordbook(id)
	if err != nil || wb == nil {
		writeErr(w, 404, "wordbook not found")
		return
	}
	f, err := os.Open(wordbookFilePath(s.m.dir, id))
	if err != nil {
		writeErr(w, 404, "字典文件不存在")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.Copy(w, f)
}

// reconWordbookSaveContent PUT /api/recon/wordbooks/{id}/content — 在线编辑保存。
// body = 纯文本全文;写回文件并刷新行数/字节统计。
func (s *Server) reconWordbookSaveContent(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad id")
		return
	}
	wb, err := s.m.PG().GetWordbook(id)
	if err != nil || wb == nil {
		writeErr(w, 404, "wordbook not found")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeErr(w, 400, "读取失败: "+err.Error())
		return
	}
	path := wordbookFilePath(s.m.dir, id)
	if path == "" {
		writeErr(w, 500, "路径异常")
		return
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	lines := strings.Count(string(body), "\n") + 1
	if err := s.m.PG().UpdateWordbookStats(id, int64(len(body)), lines, path); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "line_count": lines, "byte_size": len(body)})
}

func taskNumericID(id string) int64 {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// ---- 字典库(用户上传,信息搜集枚举类工具的弹药) ----
// 文件 I/O 统一走 wordbook_files.go 的安全助手;HTTP handler 不含 os.* 直调。

// reconWordbookList GET /api/recon/wordbooks — 字典清单。
func (s *Server) reconWordbookList(w http.ResponseWriter, _ *http.Request) {
	rows, err := s.m.PG().ListWordbooks()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if rows == nil {
		rows = []*db.Wordbook{}
	}
	writeJSON(w, 200, map[string]any{"wordbooks": rows})
}

// reconWordbookUpload POST /api/recon/wordbooks (multipart: file + name? + kind? + note?)
// 大小上限 64MB;上传后自动统计行数,同名覆盖更新。
func (s *Server) reconWordbookUpload(w http.ResponseWriter, r *http.Request) {
	const maxUpload = 64 << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload+1<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeErr(w, 400, "multipart 解析失败: "+err.Error())
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "缺少 file 字段")
		return
	}
	defer file.Close()
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = strings.TrimSuffix(hdr.Filename, filepath.Ext(hdr.Filename))
	}
	if name == "" {
		writeErr(w, 400, "缺少字典名称")
		return
	}
	kind := strings.TrimSpace(r.FormValue("kind"))
	switch kind {
	case "subdomain", "path", "password", "username", "other":
	default:
		kind = "other"
	}
	note := strings.TrimSpace(r.FormValue("note"))

	id, err := s.m.PG().CreateWordbook(name, kind, "", note, 0, 0)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	f, dstPath, err := writeWordbookFile(s.m.dir, id)
	if err != nil {
		_, _ = s.m.PG().DeleteWordbook(id)
		writeErr(w, 500, err.Error())
		return
	}
	written, cpErr := io.Copy(f, file)
	syncErr := f.Sync()
	closeErr := f.Close()
	if cpErr != nil || syncErr != nil || closeErr != nil || written > maxUpload {
		removeWordbookFile(s.m.dir, id)
		_, _ = s.m.PG().DeleteWordbook(id)
		writeErr(w, 400, fmt.Sprintf("写入失败(过大或 IO 错误): %d bytes", written))
		return
	}
	lineCount := 0
	if lf, err := os.Open(dstPath); err == nil {
		sc := bufio.NewScanner(lf)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			lineCount++
		}
		lf.Close()
	}
	if err := s.m.PG().UpdateWordbookStats(id, written, lineCount, dstPath); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": id, "line_count": lineCount, "byte_size": written})
}

// reconWordbookDelete DELETE /api/recon/wordbooks/{id} — 删元数据+文件。
func (s *Server) reconWordbookDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad id")
		return
	}
	wb, err := s.m.PG().GetWordbook(id)
	if err != nil || wb == nil {
		writeJSON(w, 200, map[string]any{"deleted": false})
		return
	}
	removeWordbookFile(s.m.dir, id)
	deleted, err := s.m.PG().DeleteWordbook(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

// reconWordbookDownload GET /api/recon/wordbooks/{id}/download — 原文下载。
func (s *Server) reconWordbookDownload(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad id")
		return
	}
	wb, err := s.m.PG().GetWordbook(id)
	if err != nil || wb == nil {
		writeErr(w, 404, "wordbook not found")
		return
	}
	f, err := openWordbookForRead(s.m.dir, id)
	if err != nil {
		writeErr(w, 404, "字典文件不存在")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=wordbook-%d.txt", id))
	_, _ = io.Copy(w, f)
}

// reconWordbookPreview GET /api/recon/wordbooks/{id}/preview?limit=50 — 前 N 行预览。
func (s *Server) reconWordbookPreview(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad id")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	wb, err := s.m.PG().GetWordbook(id)
	if err != nil || wb == nil {
		writeErr(w, 404, "wordbook not found")
		return
	}
	f, err := openWordbookForRead(s.m.dir, id)
	if err != nil {
		writeErr(w, 404, "字典文件不存在")
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	lines := make([]string, 0, limit)
	for sc.Scan() && len(lines) < limit {
		lines = append(lines, sc.Text())
	}
	writeJSON(w, 200, map[string]any{"lines": lines, "truncated": len(lines) >= limit})
}

// resolveWordbookPath 为步骤配置里的 wordbook_id 注入字典文件路径(收集器经
// config["wordbook_path"] 读取)。文件缺失给出明确错误。
func (s *Server) resolveWordbookPath(cfg map[string]any) error {
	idF, _ := cfg["wordbook_id"].(float64)
	if idF <= 0 {
		return nil
	}
	wb, err := s.m.PG().GetWordbook(int64(idF))
	if err != nil || wb == nil {
		return fmt.Errorf("字典 #%d 不存在(可能已删除,请重新选择)", int64(idF))
	}
	path := wordbookSafePath(s.m.dir, wb.ID)
	if path == "" {
		return fmt.Errorf("字典路径异常,请重新上传")
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("字典 «%s» 文件缺失,请重新上传", wb.Name)
	}
	cfg["wordbook_path"] = path
	return nil
}

// ---- 外部接口(external_apis) ----

// externalAPIDTO 是对外形状:凭据永不回传明文,只给掩码提示。
type externalAPIDTO struct {
	ID            int64             `json:"id"`
	Provider      string            `json:"provider"`
	ProviderTitle string            `json:"provider_title"`
	Name          string            `json:"name"`
	BaseURL       string            `json:"base_url"`
	Enabled       bool              `json:"enabled"`
	LastTestAt    string            `json:"last_test_at,omitempty"`
	LastTestOK    *bool             `json:"last_test_ok,omitempty"`
	QuotaInfo     json.RawMessage   `json:"quota_info,omitempty"`
	Masked        map[string]string `json:"masked"`
}

func rfc3339Ptr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

// maskCredentials 按 provider 生成掩码提示(邮箱明文前 2 字符、key 只留尾 4)。
func maskCredentials(provider string, creds map[string]any) map[string]string {
	mask := map[string]string{}
	for k, v := range creds {
		s, _ := v.(string)
		if s == "" {
			continue
		}
		if len(s) <= 6 {
			mask[k] = strings.Repeat("*", len(s))
		} else if strings.Contains(k, "email") {
			mask[k] = s[:2] + "***" + s[strings.IndexByte(s, '@'):]
		} else {
			mask[k] = "***" + s[len(s)-4:]
		}
	}
	return mask
}

func (s *Server) externalAPIDTO(a *db.ExternalAPI) (*externalAPIDTO, error) {
	dto := &externalAPIDTO{
		ID: a.ID, Provider: a.Provider, Name: a.Name, BaseURL: a.BaseURL, Enabled: a.Enabled,
		LastTestAt: rfc3339Ptr(a.LastTestAt), LastTestOK: a.LastTestOK, Masked: map[string]string{},
	}
	if p, ok := recon.Provider(a.Provider); ok {
		dto.ProviderTitle = p.Title
	}
	if len(a.QuotaInfo) > 0 && string(a.QuotaInfo) != "{}" {
		dto.QuotaInfo = json.RawMessage(a.QuotaInfo)
	}
	if s.apiSecret != nil {
		if plain, err := s.apiSecret.open(a.Credentials); err == nil {
			var creds map[string]any
			if json.Unmarshal(plain, &creds) == nil {
				dto.Masked = maskCredentials(a.Provider, creds)
			}
		}
	}
	return dto, nil
}

type externalAPIReq struct {
	Provider    string         `json:"provider"`
	Name        *string        `json:"name"`
	BaseURL     *string        `json:"base_url"`
	Credentials map[string]any `json:"credentials"`
	Enabled     *bool          `json:"enabled,omitempty"`
}

func (s *Server) listExternalAPIs(w http.ResponseWriter, _ *http.Request) {
	rows, err := s.m.PG().ExternalAPIs().List()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	dtos := make([]*externalAPIDTO, 0, len(rows))
	for _, a := range rows {
		dto, err := s.externalAPIDTO(a)
		if err != nil {
			continue
		}
		dtos = append(dtos, dto)
	}
	writeJSON(w, 200, map[string]any{
		"apis":      dtos,
		"providers": recon.ProviderDescriptions(),
	})
}

func (s *Server) createExternalAPI(w http.ResponseWriter, r *http.Request) {
	if s.apiSecret == nil {
		writeErr(w, 503, "凭据加密盒未初始化")
		return
	}
	var req externalAPIReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	req.Provider = strings.TrimSpace(req.Provider)
	p, ok := recon.Provider(req.Provider)
	if !ok {
		writeErr(w, 400, "未知 provider: "+req.Provider)
		return
	}
	name := ""
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
	}
	if name == "" {
		name = p.Title
	}
	base := ""
	if req.BaseURL != nil {
		base = strings.TrimSpace(*req.BaseURL)
	}
	if base == "" {
		base = p.DefaultBase
	}
	if _, err := recon.ValidatePublicURL(strings.TrimSuffix(base, "/") + "/"); err != nil {
		writeErr(w, 400, "base_url 校验失败: "+err.Error())
		return
	}
	plain, err := json.Marshal(req.Credentials)
	if err != nil {
		writeErr(w, 400, "凭据格式无效")
		return
	}
	sealed, err := s.apiSecret.seal(plain)
	if err != nil {
		writeErr(w, 500, "凭据加密失败: "+err.Error())
		return
	}
	id, err := s.m.PG().ExternalAPIs().Create(req.Provider, name, base, sealed)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	log.Printf("[external-apis] 新建 %s «%s» #%d", req.Provider, name, id)
	writeJSON(w, 201, map[string]any{"id": id})
}

func (s *Server) updateExternalAPI(w http.ResponseWriter, r *http.Request) {
	if s.apiSecret == nil {
		writeErr(w, 503, "凭据加密盒未初始化")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad id")
		return
	}
	var req externalAPIReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	patch := db.ExternalAPIPatch{}
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		n := strings.TrimSpace(*req.Name)
		patch.Name = &n
	}
	if req.BaseURL != nil {
		base := strings.TrimSpace(*req.BaseURL)
		if base != "" {
			if _, err := recon.ValidatePublicURL(strings.TrimSuffix(base, "/") + "/"); err != nil {
				writeErr(w, 400, "base_url 校验失败: "+err.Error())
				return
			}
			patch.BaseURL = &base
		}
	}
	if req.Enabled != nil {
		patch.Enabled = req.Enabled
	}
	if len(req.Credentials) > 0 {
		plain, err := json.Marshal(req.Credentials)
		if err != nil {
			writeErr(w, 400, "凭据格式无效")
			return
		}
		sealed, err := s.apiSecret.seal(plain)
		if err != nil {
			writeErr(w, 500, "凭据加密失败: "+err.Error())
			return
		}
		patch.Credentials = sealed
	}
	if err := s.m.PG().ExternalAPIs().Update(id, patch); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) deleteExternalAPI(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad id")
		return
	}
	deleted, err := s.m.PG().ExternalAPIs().Delete(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	log.Printf("[external-apis] 删除 #%d (existed=%v)", id, deleted)
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

func (s *Server) testExternalAPI(w http.ResponseWriter, r *http.Request) {
	if s.apiSecret == nil {
		writeErr(w, 503, "凭据加密盒未初始化")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad id")
		return
	}
	a, err := s.m.PG().ExternalAPIs().Get(id)
	if err != nil {
		writeErr(w, 404, "external api not found")
		return
	}
	p, ok := recon.Provider(a.Provider)
	if !ok {
		writeErr(w, 400, "未知 provider: "+a.Provider)
		return
	}
	plain, err := s.apiSecret.open(a.Credentials)
	if err != nil {
		writeErr(w, 500, "凭据解密失败: "+err.Error())
		return
	}
	var creds map[string]any
	if err := json.Unmarshal(plain, &creds); err != nil {
		writeErr(w, 500, "凭据格式无效")
		return
	}
	stringCreds := map[string]string{}
	for k, v := range creds {
		sv, _ := v.(string)
		stringCreds[k] = sv
	}
	base := a.BaseURL
	if base == "" {
		base = p.DefaultBase
	}
	quota, testErr := p.Test(r.Context(), base, stringCreds)
	testOK := testErr == nil
	quotaBytes := []byte("{}")
	if testOK {
		quotaBytes, _ = json.Marshal(quota)
	}
	_ = s.m.PG().ExternalAPIs().RecordTest(id, testOK, quotaBytes)
	if !testOK {
		writeJSON(w, 200, map[string]any{"ok": false, "message": testErr.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "message": "连接成功", "quota_info": quota})
}

// resolveExternalSecrets 为引用外部接口的步骤任务运行时解密凭据:
// step_config.external_api_id → external_apis 行 → 解密为 Secrets(含 base_url)。
func (s *Server) resolveExternalSecrets(stepKey string, stepConfig map[string]any) (map[string]any, error) {
	idF, _ := stepConfig["external_api_id"].(float64)
	if idF <= 0 {
		return nil, nil // 步骤不引用外部接口
	}
	a, err := s.m.PG().ExternalAPIs().Get(int64(idF))
	if err != nil {
		return nil, fmt.Errorf("外部接口 #%d 读取失败(可能已删除,请在任务配置里重新选择)", int64(idF))
	}
	if !a.Enabled {
		return nil, fmt.Errorf("外部接口 «%s» 已停用,请启用后重跑", a.Name)
	}
	if a.Provider != stepKey {
		return nil, fmt.Errorf("外部接口 «%s» 的 provider(%s)与步骤(%s)不匹配", a.Name, a.Provider, stepKey)
	}
	if s.apiSecret == nil {
		return nil, fmt.Errorf("凭据加密盒未初始化")
	}
	plain, err := s.apiSecret.open(a.Credentials)
	if err != nil {
		return nil, fmt.Errorf("外部接口凭据解密失败: %v", err)
	}
	creds := map[string]any{}
	if err := json.Unmarshal(plain, &creds); err != nil {
		return nil, fmt.Errorf("外部接口凭据格式无效")
	}
	base := a.BaseURL
	if base == "" {
		if p, ok := recon.Provider(a.Provider); ok {
			base = p.DefaultBase
		}
	}
	creds["base_url"] = base
	return creds, nil
}
