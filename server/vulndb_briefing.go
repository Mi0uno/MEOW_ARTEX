package server

// CVE 情报简报 + 漏洞库检索的服务端实现(契约 POC_INTEL_CONTRACTS.md,Agent A 所有)。
//
// 本文件当前是【骨架】:
//   - vulnBriefBlock:任务启动/运行时给 planner/worker/mainagent 系统提示词追加的
//     「CVE 情报简报」文本(按任务资产指纹自动匹配库内漏洞)。返回 "" = 不注入。
//   - vulndbSearchFor:vulndb_search 工具的后端(agent.vulndbSearchTool 调用)。
//
// Agent A 按契约 §A 填充实现,签名不得更改(task_llm.go 接线与 agent 侧骨架已冻结)。
//
// 实现要求(摘要,详见契约):
//   1. vulnBriefBlock(taskID):解析任务作用域(CompanyIDs→资产,参考 memoryScopesForTaskID)
//      → QueryByTask 取资产指纹(technologies/service_name/page_title 关键词,去噪停用词)
//      → db.MatchVulnEntriesForFingerprints(指纹, 30) → 组简报(KEV 段/带PoC高危段/其他段
//      +「用 vulndb_search 深查」指引)。开关判据:任务级 tasks.vuln_brief 三态优先,
//      回落全局 settings vulndb_task_brief(缺省开)。per-task 进程内缓存 ~10min。
//   2. vulndbSearchFor(ctx, query, severity, kev, hasPoc, limit):ListVulnEntries 检索,
//      输出对 LLM 友好的条目清单文本(编号/标题/严重度/KEV/厂商产品/PoC 数)。

import "context"

// vulnBriefBlock 返回本任务的 CVE 情报简报文本(空串=不注入)。
func (s *Server) vulnBriefBlock(taskID string) string {
	return "" // TODO(Agent A): 按契约 §A.1 实现
}

// vulndbSearchFor 是 vulndb_search 工具的服务端检索实现,返回格式化文本。
func (s *Server) vulndbSearchFor(ctx context.Context, query, severity, kev, hasPoc string, limit int) (string, error) {
	return "", errVulnSearchUnavailable // TODO(Agent A): 按契约 §A.2 实现
}

// errVulnSearchUnavailable 是骨架期的占位错误,Agent A 实现后可移除。
var errVulnSearchUnavailable = errVulnSearchUnavailableType{}

type errVulnSearchUnavailableType struct{}

func (errVulnSearchUnavailableType) Error() string { return "漏洞库查询不可用" }
