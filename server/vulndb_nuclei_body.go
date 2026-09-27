package server

// nuclei 模板正文回填(契约 POC_INTEL_CONTRACTS.md,Agent C 所有)。
//
// 本文件当前是【骨架】。Agent C 按契约 §C 填充实现:
//
//   - nucleiFetchCVEPaths 复用 vulndb_pocsources.go 的既有树解析(nucleiParseTree/
//     nucleiFilterCVEPaths)得到 CVE→模板路径映射。
//   - nucleiBodyBackfill:对库内 source="nuclei" 且 content 为空的 PoC 行,逐个
//     GET https://raw.githubusercontent.com/projectdiscovery/nuclei-templates/main/<path>
//     (不占 GitHub API 配额;200ms 间隔+失败退避),SetVulnPocContent 写正文。
//     光标 settings vulndb_nuclei_body_cursor(JSON),总预算 2h,可取消续跑。
//   - syncNucleiTemplates 挂新增模板时同步抓正文(改 vulndb_pocsources.go 既有函数,
//     该文件也归 C)。
//
// PocBackfillStatus 是 nuclei/trickest 回填共形的状态 wire 结构(前端同一组件渲染)。

// PocBackfillStatus 回填作业状态(GET /api/vulndb/{nuclei|trickest}-backfill 响应体)。
type PocBackfillStatus struct {
	Running    bool   `json:"running"`     // 作业进行中
	Done       bool   `json:"done"`        // 光标推进完成(全部处理过)
	Processed  int    `json:"processed"`   // 已处理条数(nuclei=模板行数,trickest=条目数)
	Matched    int    `json:"matched"`     // 命中数(nuclei=正文写入行数,trickest=挂载 PoC 行数)
	Error      string `json:"error"`       // 最后一次错误(空=无)
	StartedAt  string `json:"started_at"`  // RFC3339;无作业为空
	FinishedAt string `json:"finished_at"` // RFC3339;未结束为空
}

// nucleiBodyStatus 返回 nuclei 正文回填状态(骨架)。
func (s *Server) nucleiBodyStatus() PocBackfillStatus {
	return PocBackfillStatus{} // TODO(Agent C): 按契约 §C.2 实现
}
