package server

// Decision 决策引擎(前置决策模型)配置:
// 在 intercept 审批流水线中,规则未命中后先由决策引擎做快速结构化安全评估,
// 高置信直接判定,低置信转监管模型(LLM 兜底,见 /system/ai-guard)或人工审批。
//
// 配置存 settings,按 provider 一组键;JEV 复用历史键(jev_guard_enabled/jev_api_key),
// 升级不丢已配置的凭据。AI管理 → Decision 页负责配置;监管&规则 → AI监管 页负责编排。

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// decisionEngineDef 是一个决策引擎的静态定义与 settings 键映射。
type decisionEngineDef struct {
	Key          string
	Title        string
	Description  string
	EnabledKey   string
	APIKeyKey    string
	BaseKey      string
	ModelKey     string
	DefaultBase  string
	DefaultModel string
	// EndpointPath 是已接入引擎的协议端点路径:配置里填根地址,运行时拼接此路径。
	// 协议预留引擎为空,仅做根地址连通性检查。
	EndpointPath string
	// Available 表示客户端协议已接入(true=可参与前置判定;false=预留位,仅可配置)。
	Available bool
}

var decisionEngines = []decisionEngineDef{
	{
		Key:          "jev",
		Title:        "JEV · TypeSafe System One",
		Description:  "前置结构化安全分类器:对工具调用做 choice/noul 快速评估,高置信直接放行/拦截,低置信转监管模型或人工。",
		EnabledKey:   "jev_guard_enabled",
		APIKeyKey:    "jev_api_key",
		BaseKey:      "jev_base_url",
		ModelKey:     "jev_model",
		DefaultBase:  "https://api.typesafe.ai",
		DefaultModel: "jev-latest",
		EndpointPath: "/v1/systemone",
		Available:    true,
	},
	{
		Key:         "laya",
		Title:       "Laya",
		Description: "决策模型(预留位):配置与启用入口已就绪,协议接入后即作为前置决策引擎加入流水线。",
		EnabledKey:  "decision_laya_enabled",
		APIKeyKey:   "laya_api_key",
		BaseKey:     "laya_base_url",
		ModelKey:    "laya_model",
		Available:   false,
	},
}

func decisionEngineDefByKey(key string) *decisionEngineDef {
	for i := range decisionEngines {
		if decisionEngines[i].Key == key {
			return &decisionEngines[i]
		}
	}
	return nil
}

// decisionEngineConfig 是一个引擎的运行时配置(API key 为明文,仅在服务端内部流转)。
type decisionEngineConfig struct {
	Def            decisionEngineDef
	Enabled        bool
	APIKey         string
	BaseURL, Model string
}

func (s *Server) decisionEngineConfig(def *decisionEngineDef) decisionEngineConfig {
	enabled, _, _ := s.m.pg.GetSetting(def.EnabledKey)
	apiKey, _, _ := s.m.pg.GetSetting(def.APIKeyKey)
	base, _, _ := s.m.pg.GetSetting(def.BaseKey)
	if base == "" {
		base = def.DefaultBase
	}
	model, _, _ := s.m.pg.GetSetting(def.ModelKey)
	if model == "" {
		model = def.DefaultModel
	}
	return decisionEngineConfig{Def: *def, Enabled: enabled == "true", APIKey: apiKey, BaseURL: base, Model: model}
}

// decisionEngineDTO 是对外视图:API key 只回是否已配置,不回明文。
type decisionEngineDTO struct {
	Key          string `json:"key"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	Enabled      bool   `json:"enabled"`
	APIKeySet    bool   `json:"api_key_set"`
	BaseURL      string `json:"base_url"`
	Model        string `json:"model"`
	DefaultBase  string `json:"default_base_url"`
	DefaultModel string `json:"default_model"`
	EndpointPath string `json:"endpoint_path"`
	Available    bool   `json:"available"`
}

func decisionEngineView(c decisionEngineConfig) decisionEngineDTO {
	return decisionEngineDTO{
		Key: c.Def.Key, Title: c.Def.Title, Description: c.Def.Description,
		Enabled: c.Enabled, APIKeySet: c.APIKey != "",
		BaseURL: c.BaseURL, Model: c.Model,
		DefaultBase: c.Def.DefaultBase, DefaultModel: c.Def.DefaultModel,
		EndpointPath: c.Def.EndpointPath,
		Available:    c.Def.Available,
	}
}

// decisionListEngines GET /api/decision/engines — 决策引擎清单(AI监管/Decision 两页共用)。
func (s *Server) decisionListEngines(w http.ResponseWriter, _ *http.Request) {
	out := make([]decisionEngineDTO, 0, len(decisionEngines))
	for i := range decisionEngines {
		out = append(out, decisionEngineView(s.decisionEngineConfig(&decisionEngines[i])))
	}
	writeJSON(w, 200, map[string]any{"engines": out})
}

// decisionUpdateEngine PUT /api/decision/engines/{key} — 局部更新配置。
// api_key 三态:缺省=不动;空串=清除;非空=写入(只写不读,响应不含明文)。
func (s *Server) decisionUpdateEngine(w http.ResponseWriter, r *http.Request) {
	def := decisionEngineDefByKey(r.PathValue("key"))
	if def == nil {
		writeErr(w, 404, "未知决策引擎")
		return
	}
	var req struct {
		Enabled *bool   `json:"enabled"`
		BaseURL *string `json:"base_url"`
		APIKey  *string `json:"api_key"`
		Model   *string `json:"model"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if req.Enabled != nil {
		v := "false"
		if *req.Enabled {
			v = "true"
		}
		if err := s.m.pg.SetSetting(def.EnabledKey, v); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if req.BaseURL != nil {
		if err := s.m.pg.SetSetting(def.BaseKey, *req.BaseURL); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if req.APIKey != nil {
		if err := s.m.pg.SetSetting(def.APIKeyKey, *req.APIKey); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if req.Model != nil {
		if err := s.m.pg.SetSetting(def.ModelKey, *req.Model); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// decisionTestEngine POST /api/decision/engines/{key}/test — 连通性测试。
// 已接入引擎走真实协议探测;预留引擎退化为 base_url 可达性检查。
func (s *Server) decisionTestEngine(w http.ResponseWriter, r *http.Request) {
	def := decisionEngineDefByKey(r.PathValue("key"))
	if def == nil {
		writeErr(w, 404, "未知决策引擎")
		return
	}
	cfg := s.decisionEngineConfig(def)
	if cfg.APIKey == "" {
		writeErr(w, 400, "api_key 未配置,请先在下方保存凭据")
		return
	}
	start := time.Now()
	if def.Available {
		// 真实协议探测:一条最小 choice 问题。
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		resp, err := jevClassify(ctx, cfg.BaseURL, cfg.Model, cfg.APIKey, "connectivity probe: ping", map[string]jevQuestion{
			"ping": {Type: "choice", Instructions: "Reply with ok.", Criteria: map[string]string{"ok": "The probe succeeded."}},
		})
		if err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "latency_ms": time.Since(start).Milliseconds(), "error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{
			"ok": true, "latency_ms": time.Since(start).Milliseconds(),
			"detail": fmt.Sprintf("模型 %s 返回 %d 项回答", resp.Model, len(resp.Answers)),
		})
		return
	}
	// 预留引擎:仅验证 base_url 可达。
	if cfg.BaseURL == "" {
		writeErr(w, 400, "base_url 未配置")
		return
	}
	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, cfg.BaseURL, nil)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "latency_ms": time.Since(start).Milliseconds(), "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	writeJSON(w, 200, map[string]any{
		"ok": resp.StatusCode < 500, "latency_ms": time.Since(start).Milliseconds(),
		"detail": fmt.Sprintf("base_url 可达(HTTP %d);协议尚未接入,仅验证连通性", resp.StatusCode),
	})
}
