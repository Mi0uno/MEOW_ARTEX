package agent

// 漏洞库读向联动骨架(契约 POC_INTEL_CONTRACTS.md §4):
//   - vulndb_search 工具:运行中检索平台漏洞库(CVE/0day/Nday 情报+PoC 索引),
//     让 agent 在拿到目标指纹(产品/版本/服务)后主动查已知漏洞与可用 PoC;
//   - CVE 情报简报回调:planner/worker/mainagent 系统提示词追加按任务资产指纹
//     自动匹配的漏洞情报摘要(appendVulnBrief,位置在 memory 简报之后、vps 块之前)。
//
// 全部走回调注入(server 侧经 SetVulnSearchFn/SetVulnBrief 挂进来),本包不依赖
// db 漏洞表——未接线(回调为 nil)时工具报「漏洞库查询不可用」、简报不注入,
// 行为退化为无漏洞库联动的原始行为,零回归(与 memory_recall 同一套 nil-safe 语义)。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	actool "github.com/Autumn-27/norma/tool"
)

// vulndbSearchTool 漏洞库检索工具(只读)。nil-safe:构造只闭包字段本身;fn 为 nil
// 时 Call 返回错误 Result(toolcatalog 播种时只读 Name/Desc/Schema,种子实例 fn 恒 nil)。
func (ts *ToolSet) vulndbSearchTool() actool.CoreTool {
	return readTool("vulndb_search",
		"查询平台漏洞库：CVE/0day/Nday 情报与已收录 PoC 索引。拿到目标的组件/产品/版本指纹后先查这里——命中已知漏洞(尤其 KEV 在野利用)时,可参考其 PoC 来源与利用指引设计验证路径;上报漏洞前也可用它判重。",
		obj(map[string]any{
			"query": str("检索词:CVE 编号 / 厂商 / 产品 / 组件名 / 关键词(如 log4j、shiro、若依)"),
			"severity": map[string]any{
				"type":        "string",
				"enum":        []string{"", "critical", "high", "medium", "low"},
				"default":     "",
				"description": "按严重度过滤(空=不限)",
			},
			"kev": map[string]any{
				"type":        "string",
				"enum":        []string{"", "1", "0"},
				"default":     "",
				"description": "KEV 在野利用过滤:1=仅 KEV,0=仅非 KEV,空=不限",
			},
			"has_poc": map[string]any{
				"type":        "string",
				"enum":        []string{"", "1", "0"},
				"default":     "",
				"description": "PoC 过滤:1=仅看有 PoC 的,0=仅无 PoC,空=不限",
			},
			"limit": map[string]any{
				"type":        "integer",
				"default":     10,
				"description": "返回条数上限(1-30,默认 10)",
			},
		}, "query"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if ts.vulndbSearch == nil {
				return actool.Errorf("漏洞库查询不可用"), nil
			}
			var a struct {
				Query    string `json:"query"`
				Severity string `json:"severity"`
				Kev      string `json:"kev"`
				HasPoc   string `json:"has_poc"`
				Limit    int    `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			query := strings.TrimSpace(a.Query)
			if query == "" {
				return actool.Errorf("query 必填"), nil
			}
			limit := a.Limit
			if limit <= 0 {
				limit = 10
			}
			if limit > 30 {
				limit = 30
			}
			out, err := ts.vulndbSearch(ctx, query, a.Severity, a.Kev, a.HasPoc, limit)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("漏洞库查询失败: %s", err.Error())), nil
			}
			return actool.Text(out), nil
		})
}

// SetVulndbSearch 注入漏洞库检索回调(server 侧实现,闭包内固定 Provider/PG 句柄)。
// nil = vulndb_search 工具报「漏洞库查询不可用」。
func (t *ToolSet) SetVulndbSearch(fn func(ctx context.Context, query, severity, kev, hasPoc string, limit int) (string, error)) {
	t.vulndbSearch = fn
}

// wireVulndbSearch 把 agent 持有的漏洞库检索回调绑定到本 run 的工具集上。
// 两侧 nil 检查保证未接线时工具自然降级(与 wireMemoryRecall 同款)。
func wireVulndbSearch(tsx *ToolSet, fn func(ctx context.Context, query, severity, kev, hasPoc string, limit int) (string, error)) {
	if tsx == nil || fn == nil {
		return
	}
	tsx.SetVulndbSearch(fn)
}

// appendVulnBrief 把 CVE 情报简报块追加到系统提示词(逐字位置语义:memory 简报之后、
// vps.SystemBlock 之前;nil/空白不追加)。
func appendVulnBrief(sysBody string, fn func(taskID string) string, taskID int64) string {
	if fn == nil {
		return sysBody
	}
	if blk := fn(fmt.Sprintf("%d", taskID)); strings.TrimSpace(blk) != "" {
		return sysBody + "\n\n" + blk
	}
	return sysBody
}

// SetVulnBrief 注入 CVE 情报简报回调(系统提示词拼装时以 taskID 调用,返回简报文本,
// 无内容返回 "")。planner/worker/mainagent 各持一份;nil = 不注入。
func (p *Planner) SetVulnBrief(fn func(taskID string) string) { p.vulnBriefFn = fn }

// SetVulnSearchFn 注入漏洞库检索回调(经 wireVulndbSearch 下传本 run 工具集)。nil = 工具不可用。
func (p *Planner) SetVulnSearchFn(fn func(ctx context.Context, query, severity, kev, hasPoc string, limit int) (string, error)) {
	p.vulnSearchFn = fn
}

// SetVulnBrief 见 Planner.SetVulnBrief。
func (w *Worker) SetVulnBrief(fn func(taskID string) string) { w.vulnBriefFn = fn }

// SetVulnSearchFn 见 Planner.SetVulnSearchFn。
func (w *Worker) SetVulnSearchFn(fn func(ctx context.Context, query, severity, kev, hasPoc string, limit int) (string, error)) {
	w.vulnSearchFn = fn
}

// SetVulnBrief 见 Planner.SetVulnBrief。
func (m *MainAgent) SetVulnBrief(fn func(taskID string) string) { m.vulnBriefFn = fn }

// SetVulnSearchFn 见 Planner.SetVulnSearchFn。
func (m *MainAgent) SetVulnSearchFn(fn func(ctx context.Context, query, severity, kev, hasPoc string, limit int) (string, error)) {
	m.vulnSearchFn = fn
}
