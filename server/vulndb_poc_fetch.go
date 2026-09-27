package server

// PoC 内容抓取(契约 POC_INTEL_CONTRACTS.md,Agent B 所有)。
//
// 本文件当前是【骨架】。Agent B 按契约 §B.4 实现:
//
//	FetchGitHubReadme(ctx, repoURL):给 github.com/owner/repo(可带 /tree/... 路径),
//	归一化出 owner/repo 后经 raw.githubusercontent.com 拉 README(依次试
//	README.md / readme.md / README.en.md,读 readmetxt 上限 4KB),礼貌限速
//	(全局共享的 raw 抓取限速器,trickest/nuclei 同用)。
//	用途:①github 源存量 PoC 的 content 为空时补抓;②Agent D 的 LLM 审核
//	对 github 链接现抓内容做判定。
//	签名不得更改(D 侧已按此调用)。

import "context"

// FetchGitHubReadme 抓取 GitHub 仓库 README 文本(≤4KB,失败返回空串与 error)。
func FetchGitHubReadme(ctx context.Context, repoURL string) (string, error) {
	return "", nil // TODO(Agent B): 按契约 §B.4 实现
}
