package server

// 资产识别 API(契约 ASSET_EXTRACT_CONTRACTS.md §5):
//
//	GET  /api/tasks/{id}/asset-extract   取识别轮(无则 {run:null})
//	POST /api/tasks/{id}/asset-extract   手动触发(202 异步;kind≠agent 400;running 409)
//	GET  /api/asset-extract/config       读「AI 识别」开关
//	PUT  /api/asset-extract/config       写开关(body {"ai_enabled":bool})

import (
	"encoding/json"
	"net/http"
)

// taskAssetExtractGet GET /api/tasks/{id}/asset-extract — 取该任务的识别轮记录。
func (s *Server) taskAssetExtractGet(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	pg := s.pg(w)
	if pg == nil {
		return
	}
	run, err := pg.GetAssetExtractRunByTask(taskNumericID(t.ID))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"run": run})
}

// taskAssetExtractRun POST /api/tasks/{id}/asset-extract — 手动触发资产识别。
// recon 任务 400(信息搜集走暂存区采纳);其他非 agent 任务 400;running 中 409;
// 成功 202 {run_id}(管线异步执行)。LLM/开关不校验:确定性段总是可跑(契约 §3.1)。
func (s *Server) taskAssetExtractRun(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	pg := s.pg(w)
	if pg == nil {
		return
	}
	taskID := taskNumericID(t.ID)
	pt, err := pg.GetTask(taskID)
	if err != nil || pt == nil {
		writeErr(w, 404, "task not found")
		return
	}
	if pt.Kind != assetExtractTaskKind {
		if pt.Stage == "recon" {
			writeErr(w, 400, "信息搜集任务请使用暂存区采纳")
		} else {
			writeErr(w, 400, "仅 agent 任务支持资产识别")
		}
		return
	}
	runID, started, conflict := s.beginAssetExtract(taskID)
	if conflict {
		writeErr(w, 409, "识别正在进行中")
		return
	}
	if !started {
		writeErr(w, 500, "无法启动识别(写 run 行失败)")
		return
	}
	go s.runAssetExtract(taskID)
	writeJSON(w, 202, map[string]any{"run_id": runID})
}

// assetExtractConfigGet GET /api/asset-extract/config — 读「AI 识别」开关。
func (s *Server) assetExtractConfigGet(w http.ResponseWriter, _ *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	writeJSON(w, 200, map[string]any{"ai_enabled": s.assetExtractAIEnabled()})
}

// assetExtractConfigPut PUT /api/asset-extract/config — 写「AI 识别」开关。
func (s *Server) assetExtractConfigPut(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req struct {
		AIEnabled *bool `json:"ai_enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	if req.AIEnabled == nil {
		writeErr(w, 400, "ai_enabled 不能为空")
		return
	}
	if err := pg.SetSetting(settingAssetExtractAI, assetExtractSettingValue(*req.AIEnabled)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ai_enabled": *req.AIEnabled})
}
