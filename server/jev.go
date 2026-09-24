package server

// Jev (TypeSafe System One) 门限控制:作为独立的「前置决策引擎」接入 intercept 审批
// 流水线,在规则未命中时对工具调用做快速结构化安全评估。
//
// 调用顺序(见 intercept.Judge):规则 → JEV(本文件,开关 jev_guard_enabled) →
// LLM 兜底裁判(llm_judge_enabled) → 人工审批(ask)。
// JEV 与 LLM 裁判各有独立开关:它不再内嵌在 LLM 裁判闭包里,因此 llm_judge_enabled
// 关闭时 JEV 依然执行(旧实现把两者耦合,导致 JEV 永不生效)。
// 高置信直接判定(block→deny / allow),低置信返回"未裁决"交给下一层。
//
// API: POST https://api.typesafe.ai/v1/systemone
// Auth: Bearer token (settings: jev_api_key)
// 开关: settings: jev_guard_enabled = "true"
//
// 本文件只负责协议客户端与单次分类;裁决到 Decision 的适配在 server/intercept.go
// 的 jevDecisionEngine。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	jevHTTPTimeout   = 15 * time.Second
	jevConfidenceMin = 0.6 // 全局最低置信度,低于此转人工/LLM
	jevAllowConf     = 0.8 // "allow" 放行需 ≥ 0.8(高风险动作阈值)
	jevNoulDanger    = 0.9 // 任一 Noul 超过此值直接禁止
)

// ---- settings helpers ----

func (s *Server) jevAPIKey() string {
	v, _, _ := s.m.pg.GetSetting("jev_api_key")
	return v
}

// jevBaseURL / jevModelName 读 Decision 页的可配置端点与模型;缺省回退内置默认。
func (s *Server) jevBaseURL() string {
	v, _, _ := s.m.pg.GetSetting("jev_base_url")
	if v == "" {
		return "https://api.typesafe.ai"
	}
	return v
}

func (s *Server) jevModelName() string {
	v, _, _ := s.m.pg.GetSetting("jev_model")
	if v == "" {
		return "jev-latest"
	}
	return v
}

func (s *Server) jevGuardEnabled() bool {
	v, _, _ := s.m.pg.GetSetting("jev_guard_enabled")
	return v == "true"
}

func (s *Server) setJevGuardEnabled(on bool) error {
	v := "false"
	if on {
		v = "true"
	}
	return s.m.pg.SetSetting("jev_guard_enabled", v)
}

func (s *Server) setJevAPIKey(key string) error {
	return s.m.pg.SetSetting("jev_api_key", key)
}

// ---- Jev API client ----

// jevQuestion 是一条发给 Jev 的问题定义。
type jevQuestion struct {
	Type         string            `json:"type"` // choice | score | noul
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria,omitempty"` // choice 的选项映射
}

// jevRequest 是 POST /v1/systemone 的请求体。
type jevRequest struct {
	State     string                 `json:"state"`
	Model     string                 `json:"model"`
	Questions map[string]jevQuestion `json:"questions"`
}

// jevAnswer 是单个问题的回答。
type jevAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Score         float64            `json:"score,omitempty"`
	Noul          float64            `json:"noul,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

// jevResponse 是 POST /v1/systemone 的响应体。
type jevResponse struct {
	Model   string               `json:"model"`
	Answers map[string]jevAnswer `json:"answers"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// jevClassify 调 Jev API 做一次分类评估。baseURL/model 来自 Decision 页配置:
// baseURL 是根地址(如 https://api.typesafe.ai),系统自动拼接 /v1/systemone 端点路径。
func jevClassify(ctx context.Context, baseURL, model, apiKey, state string, questions map[string]jevQuestion) (*jevResponse, error) {
	endpoint := strings.TrimRight(baseURL, "/") + "/v1/systemone"
	reqBody := jevRequest{
		State:     state,
		Model:     model,
		Questions: questions,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("jev API 返回 %d: %s", resp.StatusCode, string(b))
	}
	var result jevResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("jev 响应解析失败: %w", err)
	}
	return &result, nil
}

// ---- 门限控制逻辑 ----

// jevGuardVerdict 是 Jev 门限的判定结果。
type jevGuardVerdict struct {
	Action     string             `json:"action"` // block | review | allow
	Confidence float64            `json:"confidence"`
	Reason     string             `json:"reason"`
	NoulScores map[string]float64 `json:"noul_scores"`
}

// jevGuardReview 对一次工具调用做 Jev 安全分类。
// 返回 verdict + confidence;调用方据此决定 放行/拒绝/转人工。
func (s *Server) jevGuardReview(ctx context.Context, toolKey string, toolDesc string, paramsJSON string) (*jevGuardVerdict, error) {
	apiKey := s.jevAPIKey()
	if apiKey == "" {
		return nil, fmt.Errorf("jev_api_key 未配置")
	}

	// state:工具调用的完整上下文(简洁,让 Jev 快速判断)
	state := fmt.Sprintf(
		"工具名: %s\n工具描述: %s\n调用参数: %s\n上下文: 授权渗透测试平台中的 AI agent 正在执行此工具调用",
		toolKey, toolDesc, paramsJSON,
	)

	questions := map[string]jevQuestion{
		"action": {
			Type:         "choice",
			Instructions: "This is a tool call in an authorized penetration testing platform. What safety action should be taken?",
			Criteria: map[string]string{
				"block":  "The operation is dangerous and should be blocked (irreversible data destruction, credential theft, scope escape)",
				"review": "The operation needs human review before proceeding (ambiguous risk, unusual parameters, potential scope issues)",
				"allow":  "The operation is safe to proceed (standard recon, read-only queries, expected pentest actions)",
			},
		},
		"is_destructive": {
			Type:         "noul",
			Instructions: "Does this tool call perform an irreversible destructive operation (delete data, format disk, reset credentials, uninstall software)?",
		},
		"is_exfiltration": {
			Type:         "noul",
			Instructions: "Does this tool call send sensitive data outside the authorized testing scope?",
		},
		"is_privilege": {
			Type:         "noul",
			Instructions: "Does this tool call attempt to escalate privileges or bypass authentication on the target system?",
		},
	}

	resp, err := jevClassify(ctx, s.jevBaseURL(), s.jevModelName(), apiKey, state, questions)
	if err != nil {
		return nil, err
	}

	action, ok := resp.Answers["action"]
	if !ok {
		return nil, fmt.Errorf("jev 未返回 action 分类")
	}

	// Noul 危险信号:任一超过 0.9 → 直接禁止
	noulScores := map[string]float64{}
	for _, key := range []string{"is_destructive", "is_exfiltration", "is_privilege"} {
		if a, ok := resp.Answers[key]; ok {
			noulScores[key] = a.Noul
		}
	}

	verdict := &jevGuardVerdict{
		Action:     action.Choice,
		Confidence: action.Confidence,
		NoulScores: noulScores,
	}

	// 判定逻辑
	if action.Choice == "block" && action.Confidence >= jevConfidenceMin {
		verdict.Reason = "Jev 分类: 高风险操作"
	} else if action.Choice == "allow" && action.Confidence >= jevAllowConf {
		verdict.Reason = "Jev 分类: 安全操作"
	} else if action.Confidence < jevConfidenceMin {
		verdict.Reason = "Jev 置信度不足,需人工判断"
	} else {
		verdict.Reason = "Jev 分类: 需人工确认"
	}

	for k, v := range noulScores {
		if v >= jevNoulDanger {
			verdict.Action = "block"
			verdict.Reason = fmt.Sprintf("Jev 检测到高危信号(%s=%.2f),直接禁止", k, v)
			break
		}
	}

	return verdict, nil
}

// ---- 门限控制逻辑 ----
