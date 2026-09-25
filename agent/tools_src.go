package agent

// 本文件是 SRC 专属 agent(src_operator)的三个专属工具的「模型可见目录」:
// key / 描述 / 参数 schema / 默认 agent 绑定 / 阶段归属。镜像 toolcatalog.go 的
// ToolSeed 形态——只声明模型看到什么,执行函数在 server/src_tools.go(工具要读
// src_platforms/src_targets 的平台库与 Manager 的任务解析,住在 server 层)。
//
// 服务端 seedSRCOperatorTools 按这里的声明 seed 进 tools 表(key upsert、默认绑
// src_operator/pentest/worker、stage=exploit),并把同 key 的执行实例注入 host 工具
// 出口;运行时 ToolResolve 再按 tools 表的绑定逐 agent 过滤——与 traffic/orchestration
// host 工具同一机制,保证工具真的可被绑定的 agent 调到。

// srcToolAgents 是三个 SRC 工具的默认绑定:专属 agent src_operator 之外,
// pentest(独立渗透)与 worker(执行)也绑——SRC 模式的流水线任务里 worker
// 同样需要平台规范与授权边界。
var srcToolAgents = []string{"src_operator", "pentest", "worker"}

// SRCToolSeeds 返回三个 SRC 工具的可播种快照(顺序即期望的工具排列)。
func SRCToolSeeds() []ToolSeed {
	return []ToolSeed{
		{
			Key: "src_get_context",
			Desc: "获取当前 SRC 任务的平台上下文(开工前必调):SRC 平台名称/主页与四类规范全文" +
				"(奖励标准/评分标准/守则/平台规范)、目标在该平台下的授权范围(范围说明/允许的测试类型/" +
				"速率限制/严格禁止事项/补充规范)、目标资产清单摘要(类型+值)。一切探测与验证都必须落在" +
				"授权范围内;范围外资产一律标记 out-of-scope 并跳过,禁止事项任何理由都不例外。" +
				"任务内运行无需参数(自动取当前任务);对话页使用时若无法确定任务请传 task_id。",
			Schema: srcObjSchema(map[string]any{
				"task_id": map[string]any{
					"type":        "integer",
					"description": "可选:目标任务 id。任务内运行自动取当前任务,省略即可",
				},
			}),
			Agents: srcToolAgents,
			Stage:  "exploit",
		},
		{
			Key: "src_score_finding",
			Desc: "产出发现后调用:返回该 SRC 平台的评分标准与奖励标准全文+目标授权范围,供你按平台" +
				"标准对候选发现(标题/描述/初判严重度)做正式自评——给出等级与理由,并按奖励标准判断" +
				"哪些方向值得继续深入。工具只确定性取数,评定由你依据标准完成,不要凭通用 CVSS 想当然。",
			Schema: srcObjSchema(map[string]any{
				"title":       srcStrParam("发现的标题"),
				"description": srcStrParam("发现的描述:漏洞类型、位置、影响"),
				"severity":    srcStrParam("你的初判严重度(critical/high/medium/low),仅作对照,最终以平台标准评定"),
				"task_id": map[string]any{
					"type":        "integer",
					"description": "可选:目标任务 id。任务内运行自动取当前任务,省略即可",
				},
			}, "title", "description", "severity"),
			Agents: srcToolAgents,
			Stage:  "exploit",
		},
		{
			Key: "src_draft_submission",
			Desc: "报告阶段调用:按 SRC 平台要求把指定漏洞拼装成提交草稿(Markdown:标题/漏洞详情/" +
				"影响/复现/资产归属+平台规范要点),返回文本、不落库。finding_id 使用独立漏洞记录 ID" +
				"(list_findings / report_finding 返回的 finding_id,不是探索节点 ID)。草稿提交前需人工核对。",
			Schema: srcObjSchema(map[string]any{
				"finding_id": map[string]any{
					"type":        "integer",
					"description": "独立漏洞记录 ID",
				},
				"task_id": map[string]any{
					"type":        "integer",
					"description": "可选:目标任务 id。任务内运行自动取当前任务,省略即可",
				},
			}, "finding_id"),
			Agents: srcToolAgents,
			Stage:  "exploit",
		},
	}
}

// srcObjSchema/srcStrParam 是本文件的 schema 小助手,形态与 server 侧 objSchema/
// strParam 一致(包级循环依赖不允许反向引用,故就地声明)。
func srcObjSchema(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		req := make([]any, 0, len(required))
		for _, r := range required {
			req = append(req, r)
		}
		m["required"] = req
	}
	return m
}

func srcStrParam(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
