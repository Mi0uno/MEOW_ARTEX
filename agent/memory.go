package agent

// 跨任务记忆接入(契约 AI_MEMORY_CONTRACTS.md §7):
//   - memory_recall 工具:运行中检索历史记忆(已知资产/历史漏洞/经验洞察/攻击剧本),
//     让 agent 在重复枚举前先查记忆,省 token 也防重复踩坑;
//   - 记忆简报注入回调:planner/worker/mainagent 系统提示词末尾追加历史沉淀摘要。
//
// 全部走回调注入(server 侧经 SetMemoryRecallFn/SetMemoryBrief 挂进来),本包不依赖
// db 记忆表——未接线(回调为 nil)时工具报「记忆功能不可用」、简报不注入,行为退化为
// 无记忆的原始行为,零回归。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	actool "github.com/Autumn-27/norma/tool"
)

// memoryRecallTool 跨任务记忆检索工具(只读)。nil-safe:构造只闭包字段本身,
// 不解引用任何 store(toolcatalog 播种时只读 Name/Desc/Schema);fn 为 nil 时
// Call 返回错误 Result 而非 panic(工具目录里的种子实例 fn 恒为 nil)。
func (ts *ToolSet) memoryRecallTool() actool.CoreTool {
	return readTool("memory_recall",
		"查询跨任务记忆：已知资产结论/历史漏洞/经验洞察/攻击剧本。新的任务里先 recall 再重复枚举——同企业/资产在前序任务中做过的子域枚举、端口扫描、指纹识别、已验证路径,这里直接给结论,避免重复探索浪费预算;命中攻击剧本可按步骤复用。",
		obj(map[string]any{
			"query": str("检索词,如 主机/域名/路径/漏洞类型/技术栈/手法关键词;支持多次细粒度检索"),
			"scope": map[string]any{
				"type":        "string",
				"enum":        []string{"auto", "company", "global"},
				"default":     "auto",
				"description": "检索范围:auto=本任务作用域(企业/目标)+全局;company=仅本任务作用域;global=仅全局记忆",
			},
		}, "query"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if ts.memoryRecall == nil {
				return actool.Errorf("记忆功能不可用"), nil
			}
			var a struct {
				Query string `json:"query"`
				Scope string `json:"scope"`
			}
			_ = json.Unmarshal(in, &a)
			query := strings.TrimSpace(a.Query)
			if query == "" {
				return actool.Errorf("query 必填"), nil
			}
			scope := strings.TrimSpace(a.Scope)
			if scope == "" {
				scope = "auto"
			}
			out, err := ts.memoryRecall(ctx, query, scope)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("记忆检索失败: %s", err.Error())), nil
			}
			return actool.Text(out), nil
		})
}

// SetMemoryRecall 注入本任务的记忆检索回调(见 server 侧接线:闭包内固定当前
// 任务 id 与作用域)。nil = memory_recall 工具返回「记忆功能不可用」。
func (t *ToolSet) SetMemoryRecall(fn func(ctx context.Context, query, scope string) (string, error)) {
	t.memoryRecall = fn
}

// SetMemoryBrief 注入记忆简报回调(契约 §7.2):系统提示词拼装时以 taskID 调用,
// 返回简报文本(无内容返回 "")。planner/worker/mainagent 各持一份。
func (p *Planner) SetMemoryBrief(fn func(taskID string) string) { p.memoryBriefFn = fn }

// SetMemoryRecallFn 注入跨任务记忆检索回调(taskID 由 agent 侧闭包绑定后下传
// ToolSet)。nil = 工具不可用。
func (p *Planner) SetMemoryRecallFn(fn func(ctx context.Context, taskID, query, scope string) (string, error)) {
	p.memoryRecallFn = fn
}

// SetMemoryBrief 见 Planner.SetMemoryBrief。
func (w *Worker) SetMemoryBrief(fn func(taskID string) string) { w.memoryBriefFn = fn }

// SetMemoryRecallFn 见 Planner.SetMemoryRecallFn。
func (w *Worker) SetMemoryRecallFn(fn func(ctx context.Context, taskID, query, scope string) (string, error)) {
	w.memoryRecallFn = fn
}

// SetMemoryBrief 见 Planner.SetMemoryBrief。
func (m *MainAgent) SetMemoryBrief(fn func(taskID string) string) { m.memoryBriefFn = fn }

// SetMemoryRecallFn 见 Planner.SetMemoryRecallFn。
func (m *MainAgent) SetMemoryRecallFn(fn func(ctx context.Context, taskID, query, scope string) (string, error)) {
	m.memoryRecallFn = fn
}

// wireMemoryRecall 把 agent 持有的记忆检索回调绑定到本 run 的工具集上(taskID
// 在这里定格,worker/planner/main 的工具调用无需再带任务上下文)。两侧 nil 检查
// 保证未接线时工具自然降级为「记忆功能不可用」。
func wireMemoryRecall(tsx *ToolSet, taskID int64, fn func(ctx context.Context, taskID, query, scope string) (string, error)) {
	if tsx == nil || fn == nil {
		return
	}
	tsx.SetMemoryRecall(func(ctx context.Context, query, scope string) (string, error) {
		return fn(ctx, fmt.Sprintf("%d", taskID), query, scope)
	})
}

// appendMemoryBrief 把记忆简报块追加到系统提示词(契约 §7.2 的逐字位置语义:
// 约束注入同一层、vps.SystemBlock 之前;nil/空白不追加)。
func appendMemoryBrief(sysBody string, fn func(taskID string) string, taskID int64) string {
	if fn == nil {
		return sysBody
	}
	if blk := fn(fmt.Sprintf("%d", taskID)); strings.TrimSpace(blk) != "" {
		return sysBody + "\n\n" + blk
	}
	return sysBody
}
