package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/intercept"
)

// 本次修复的回归锁:低置信度判定绝不能被当成拦截。
//
// 旧实现的 bug:jevGuardVerdict.Action 直接取 JEV 返回的原始分类,于是当模型返回
// block 但置信度低于阈值时,代码只把 Reason 改写成"置信度不足",Action 仍是 block
// → 被 jevDecisionEngine 的 `case "block"` 拦下。结果是"我不确定"比"我确定"更严格:
// 实测把 HTTP 请求走私的只读探测脚本、token 刷新脚本、凭据验证脚本全部拦死
// (2026-09-24,任务 43 的 15 条拦截里有 7 条是"置信度不足")。
//
// 这里用一个假的 JEV 响应驱动判定函数,不需要真实 API Key。
func TestJevLowConfidenceNeverBlocks(t *testing.T) {
	cases := []struct {
		name       string
		choice     string
		confidence float64
		noul       map[string]float64
		wantAction string // "" = 未裁决(交给下一层)
	}{
		// 核心:block 但置信度不足 → 必须未裁决,不能是 block。
		{name: "block 但低置信", choice: "block", confidence: 0.5, wantAction: ""},
		{name: "block 但刚好低于阈值", choice: "block", confidence: 0.74, wantAction: ""},
		// 高置信 block 才是真拦截。
		{name: "block 且高置信", choice: "block", confidence: 0.9, wantAction: "block"},
		// review 永远不是终局裁决。
		{name: "review", choice: "review", confidence: 0.95, wantAction: ""},
		// allow 置信不足 → 未裁决(不能当 allow 放行,也不能当 block 拦)。
		{name: "allow 但低置信", choice: "allow", confidence: 0.5, wantAction: ""},
		{name: "allow 且高置信", choice: "allow", confidence: 0.9, wantAction: "allow"},
		// 提权信号默认不参与硬拦:授权渗透里提权是被授权的核心动作。
		{name: "提权信号不硬拦", choice: "allow", confidence: 0.9,
			noul: map[string]float64{"is_privilege": 0.99}, wantAction: "allow"},
		// 破坏性信号仍然硬拦(阈值 0.95)。
		{name: "破坏性信号硬拦", choice: "allow", confidence: 0.9,
			noul: map[string]float64{"is_destructive": 0.99}, wantAction: "block"},
		// 破坏性信号未达"确凿"阈值 → 不硬拦(旧阈值 0.9 会拦,实测卡在边界)。
		{name: "破坏性信号未达确凿阈值", choice: "allow", confidence: 0.9,
			noul: map[string]float64{"is_destructive": 0.92}, wantAction: "allow"},
	}

	th := defaultJevThresholds()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := judgeJevVerdict(tc.choice, tc.confidence, tc.noul, th)
			if got.Action != tc.wantAction {
				t.Fatalf("choice=%s conf=%.2f noul=%v → action=%q, want %q (reason=%q)",
					tc.choice, tc.confidence, tc.noul, got.Action, tc.wantAction, got.Reason)
			}
		})
	}
}

// 低置信策略配成 flag 时,低置信要变成"判疑放通"而不是未裁决。
func TestJevLowConfidencePolicyFlag(t *testing.T) {
	th := defaultJevThresholds()
	th.LowConfidenceAction = "flag"
	got := judgeJevVerdict("block", 0.5, nil, th)
	if got.Action != "flag" {
		t.Fatalf("低置信策略=flag 时 action=%q, want flag", got.Action)
	}
}

// 引擎把"未裁决"翻成 (Decision{}, false) 而不是任何终局动作——这是整条链路
// "交给下一层"的落点。
func TestJevEngineAbstainProducesNoVerdict(t *testing.T) {
	// 直接验证 jevDecisionEngine 的分支表:未裁决(空 Action)必须返回 false。
	dec, ok := jevVerdictToDecision(&jevGuardVerdict{Action: "", Reason: "置信度不足"})
	if ok {
		t.Fatalf("未裁决的 verdict 不应产生终局裁决, got %+v", dec)
	}
	// 判疑放通必须是 flag(放通留痕),不是 deny。
	dec, ok = jevVerdictToDecision(&jevGuardVerdict{Action: "flag", Reason: "低置信"})
	if !ok || dec.Action != "flag" {
		t.Fatalf("flag verdict → %+v ok=%v, want action=flag", dec, ok)
	}
}

// JEV 的 state 必须带上工作目录、用户背景与任务归属——这是"授权渗透的提权尝试"
// 与"越权操作"的区别所在。旧实现只给工具名+参数(且工具描述传空串),信息量比
// 后一层的 LLM 裁判少,导致它先行误判。
func TestJevStateCarriesFullContext(t *testing.T) {
	in := intercept.DecisionEngineInput{
		Tool:       "Bash",
		Arguments:  json.RawMessage(`{"command":"python creds2.py"}`),
		WorkingDir: `D:\AI\MEOW_ARTEX\data\tasks\43\i3900`,
		Background: "对这个目标做口令测试",
		TaskID:     "43",
		AgentName:  "worker",
	}
	state := buildJevState(in)
	for _, want := range []string{
		"Bash",
		`D:\AI\MEOW_ARTEX\data\tasks\43\i3900`,
		"对这个目标做口令测试",
		"#43",
		"worker",
		"creds2.py",
		"不可信输入", // 提示注入防护必须保留
	} {
		if !strings.Contains(state, want) {
			t.Errorf("jev state 缺少 %q\nstate:\n%s", want, state)
		}
	}
}

// 阈值越界必须被拒绝,避免把门限调成"永不拦截"或"全拦"。
func TestJevThresholdsValidation(t *testing.T) {
	s := &Server{}
	bad := []jevThresholds{
		{BlockConfidence: 0, AllowConfidence: 0.8, DangerScore: 0.95},
		{BlockConfidence: 1.5, AllowConfidence: 0.8, DangerScore: 0.95},
		{BlockConfidence: 0.75, AllowConfidence: 0, DangerScore: 0.95},
		{BlockConfidence: 0.75, AllowConfidence: 0.8, DangerScore: 2},
		{BlockConfidence: 0.75, AllowConfidence: 0.8, DangerScore: 0.95, LowConfidenceAction: "deny"},
	}
	for i, th := range bad {
		if err := s.SetJevThresholds(th); err == nil {
			t.Errorf("case %d: 越界阈值应被拒绝, got nil", i)
		}
	}
}

var _ = context.Background
