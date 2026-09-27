package server

// PoC 内容抓取(契约 POC_INTEL_CONTRACTS.md §5,Agent B 所有)。
//
//   - rawFetchWait:包级 raw.githubusercontent.com 抓取限速器(mutex+预约时间片,
//     200ms/请求 ≈5 req/s)。trickest 聚合页、历史回填与 README 抓取共用,
//     保证对 raw 域名的总请求速率礼貌且可预期。
//   - FetchGitHubReadme(ctx, repoURL):给 github.com/owner/repo(可带 /tree|/blob
//     尾路径),归一化出 owner/repo 后经 raw.githubusercontent.com 依次试
//     README.md / readme.md / README.en.md / README-cn.md,读到的正文截到 4KB;
//     全部 404 返回 ("", nil)。
//     用途:①github 源存量 PoC 的 content 为空时补抓;②Agent D 的 LLM 审核
//     对 github 链接现抓内容做判定。
//   - pocReadmeBackfillBatch:每轮给至多 20 条 content 为空的 github 源存量
//     PoC 补抓 README 并回写(SetVulnPocContent 的 UPDATE 路径)。
//
// 签名不得更改(FetchGitHubReadme 由 Agent D 按冻结签名调用)。

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// rawFetchInterval 对 raw.githubusercontent.com 的最小请求间隔(≈5 req/s)。
	rawFetchInterval = 200 * time.Millisecond
	// githubReadmeMaxBytes README 正文截断上限(4KB,契约 §5.5)。
	githubReadmeMaxBytes = 4 << 10
	// vulnPocReadmeBackfillCap 每轮 README 补抓的 PoC 条数上限(防超时)。
	vulnPocReadmeBackfillCap = 20
)

// githubReadmeNames raw 侧依次尝试的 README 文件名(契约顺序)。
var githubReadmeNames = []string{"README.md", "readme.md", "README.en.md", "README-cn.md"}

var (
	rawFetchMu     sync.Mutex
	rawFetchNextAt time.Time // 下一个可用时间片
)

// rawFetchWait 预约下一个 raw 抓取时间片并等到片到(全局串行排队,保证任意
// 多个调用方并发时对 raw 域名仍是 200ms 等距请求)。ctx 取消立即返回。
func rawFetchWait(ctx context.Context) error {
	rawFetchMu.Lock()
	now := time.Now()
	if rawFetchNextAt.Before(now) {
		rawFetchNextAt = now
	}
	wait := rawFetchNextAt.Sub(now)
	rawFetchNextAt = rawFetchNextAt.Add(rawFetchInterval)
	rawFetchMu.Unlock()
	if wait <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

// githubOwnerRepo 从仓库 URL 归一化出 owner/repo:剥 scheme 与 host,取路径前
// 两段(/tree/...、/blob/... 等尾路径自然被丢弃),repo 去掉 .git 后缀。
// 第三返回值 false=解析不出 owner/repo。
func githubOwnerRepo(repoURL string) (owner, repo string, ok bool) {
	s := strings.TrimSpace(repoURL)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimSuffix(s, "/")
	if i := strings.Index(s, "github.com/"); i >= 0 {
		s = s[i+len("github.com/"):]
	} else {
		return "", "", false
	}
	for _, seg := range strings.Split(s, "/") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		if owner == "" {
			owner = seg
			continue
		}
		repo = seg
		break
	}
	if owner == "" || repo == "" {
		return "", "", false
	}
	return owner, strings.TrimSuffix(repo, ".git"), true
}

// truncateReadme 截到 4KB,并回退整字节避免把多字节字符切成非法 UTF-8。
func truncateReadme(s string) string {
	if len(s) <= githubReadmeMaxBytes {
		return s
	}
	s = s[:githubReadmeMaxBytes]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// FetchGitHubReadme 抓取 GitHub 仓库 README 文本(≤4KB)。依次试
// README.md/readme.md/README.en.md/README-cn.md;全部未命中(404)返回
// ("", nil);URL 解析失败返回 error;抓取走共享 raw 限速器。
func FetchGitHubReadme(ctx context.Context, repoURL string) (string, error) {
	owner, repo, ok := githubOwnerRepo(repoURL)
	if !ok {
		return "", fmt.Errorf("无法从 %q 解析 github owner/repo", repoURL)
	}
	for _, name := range githubReadmeNames {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := rawFetchWait(ctx); err != nil {
			return "", err
		}
		raw := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/HEAD/%s", owner, repo, name)
		// HEAD ref 由 raw 侧解析为默认分支,主/从分支改名都不受影响。
		body, err := pocorexpFetchText(ctx, raw, nil)
		if err == nil && len(body) > 0 {
			return truncateReadme(string(body)), nil
		}
		// 404(无该文件名)与偶发网络错误 alike:试下一个候选名。
	}
	return "", nil
}

// pocReadmeBackfillBatch 给 content 为空的存量 github 源 PoC 补抓 README 并
// 回写(每轮 cap 20 条防超时,契约 §5.6)。返回成功补到正文的条数;失败只记
// 日志,不作为调用方的错误(两个 sync 源的主流程不受影响)。
func (v *vulnSync) pocReadmeBackfillBatch(ctx context.Context) int {
	pg := v.s.m.PG()
	rows, err := pg.Query(`SELECT id, entry_id, source_url FROM vulndb_pocs
WHERE source = 'github' AND source_url <> '' AND (content = '' OR content IS NULL)
ORDER BY updated_at ASC LIMIT $1`, vulnPocReadmeBackfillCap)
	if err != nil {
		log.Printf("[vulndb-sync] README 补抓扫描失败: %v", err)
		return 0
	}
	type pocRow struct {
		id      int64
		entryID int64
		url     string
	}
	batch := []pocRow{}
	for rows.Next() {
		var r pocRow
		if err := rows.Scan(&r.id, &r.entryID, &r.url); err != nil {
			rows.Close()
			log.Printf("[vulndb-sync] README 补抓扫描行失败: %v", err)
			return 0
		}
		batch = append(batch, r)
	}
	rows.Close()
	if len(batch) == 0 {
		return 0
	}
	filled := 0
	for _, r := range batch {
		if ctx.Err() != nil {
			return filled
		}
		content, ferr := FetchGitHubReadme(ctx, r.url)
		if ferr != nil || strings.TrimSpace(content) == "" {
			if ferr != nil && ctx.Err() == nil {
				log.Printf("[vulndb-sync] README 补抓 %s 失败: %v", r.url, ferr)
			}
			continue
		}
		// 行必存在(刚扫出来),走 SetVulnPocContent 的 UPDATE 路径;language
		// 传空串保持原值。
		if _, uerr := pg.SetVulnPocContent(r.entryID, r.url, content, ""); uerr != nil {
			log.Printf("[vulndb-sync] README 回写 poc #%d 失败: %v", r.id, uerr)
			continue
		}
		filled++
	}
	if filled > 0 {
		log.Printf("[vulndb-sync] README 补抓完成: %d/%d 条", filled, len(batch))
	}
	return filled
}
