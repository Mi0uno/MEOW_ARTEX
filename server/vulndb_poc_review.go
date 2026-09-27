package server

// PoC LLM 审核管线(契约 POC_INTEL_CONTRACTS.md,Agent D 所有)。
//
// 本文件当前是【骨架】。Agent D 按契约 §D 填充实现:
//
//   - maybeTriggerPocReview:vulndb_sync.go runOnce 末尾已预挂调用(go 协程)。
//     实现:settings vulndb_poc_review 为 "false" 或 LLM 未配置时直接返回;
//     否则异步起批量审核(pocReviewSem cap 2 限流,防重入互斥),送 pending PoC
//     逐条过 LLM:输入=PoC 内容(github 链接先 FetchGitHubReadme 现抓)+条目上下文,
//     输出 JSON {verdict:approved|rejected, confidence:0-1, poc_type, reason};
//     LLM 失败/不可解析/低置信(<0.6)→ 保持 pending(诚实);写 db.SetVulnPocReview。
//   - 触发面:①本函数(runOnce 挂)②API 手动重审(vulndb_poc_review_api.go)
//   - 判定红线:纯文本审查,绝不执行/请求 PoC 代码里的 URL 与 payload;
//     rejected 判据:仓库 404/课程推销/与 CVE 编号不匹配/纯论文无代码。
//
// 签名不得更改(maybeTriggerPocReview 由主会话冻结的 runOnce 调用)。

// maybeTriggerPocReview 在一轮采集结束后异步送审新增 pending PoC(骨架=no-op)。
func (s *Server) maybeTriggerPocReview() {
	// TODO(Agent D): 按契约 §D.1 实现
}
