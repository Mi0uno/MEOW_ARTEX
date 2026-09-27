package server

// CVE 情报 v2 的 PoC 补充源(Agent B,规范见 VULNDB_CONTRACTS_V2.md §5):
//
//   - PocOrExp_in_Github(ycdxsb/PocOrExp_in_Github):当日增量 Today.md 挂进
//     6h 定时轮;逐年历史 README.md 由 NVD 历史回填流程(Agent A)在 NVD 阶段
//     完成后调用 backfillPocOrExpYears 挂载。
//   - nuclei 官方模板(projectdiscovery/nuclei-templates):一次拉全库 git tree,
//     把 http/cves/ 下的 CVE 模板挂到库内已有条目。
//
// 两个源都「只挂不建」:CVE 条目不存在时跳过(NVD 回填/常规采集轮会建条目),
// CreateVulnPoc 对同 entry+source_url 幂等,因此全流程可安全重跑。
// 本文件所有包级 helper 均带 pocorexp/nuclei 前缀,避免与其他 agent 的同名冲突。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/recon"
)

const (
	// pocorexpBaseURL PocOrExp_in_Github 仓库的 raw 文件根(Today.md 与 <year>/README.md 同格式)。
	pocorexpBaseURL = "https://raw.githubusercontent.com/ycdxsb/PocOrExp_in_Github/main"
	// pocorexpMinYear 仓库最早的历史年份目录(1999),再早的年份请求必然 404。
	pocorexpMinYear = 1999
	// pocorexpMaxBodyBytes 响应体读取上限:主索引 PocOrExp.md 约 12MB、逐年 README 约
	// 200KB、nuclei 模板树数 MB,统一 64MB 有界读,防止上游异常响应拖爆内存。
	pocorexpMaxBodyBytes = 64 << 20

	// nucleiTreeAPI 一次拿全库文件树(GitHub trees API,recursive)。
	nucleiTreeAPI = "https://api.github.com/repos/projectdiscovery/nuclei-templates/git/trees/main?recursive=1"
	// nucleiRepoAPIBase nuclei-templates 的 GitHub API 根(树被截断时回退逐目录调 contents)。
	nucleiRepoAPIBase = "https://api.github.com/repos/projectdiscovery/nuclei-templates"
	// nucleiTemplateURLPrefix 模板的网页端永久链接前缀(入库 SourceURL 用)。
	nucleiTemplateURLPrefix = "https://github.com/projectdiscovery/nuclei-templates/blob/main/"
)

// pocorexpHTTPError 上游非 200 响应;404 被 PocOrExp 历史回填用来区分「当年无数据」。
type pocorexpHTTPError struct {
	Status int
}

func (e *pocorexpHTTPError) Error() string {
	return fmt.Sprintf("上游返回 HTTP %d", e.Status)
}

// pocorexpFetchText 抓取纯文本资源(PocOrExp 的 Markdown 与 nuclei 的 JSON 共用):
// 先过公网地址校验(拒绝内网/环回/保留地址),UA 与既有采集器一致,响应体最多读
// 64MB,客户端超时 60 秒;非 200 返回 *pocorexpHTTPError。
func pocorexpFetchText(ctx context.Context, rawURL string, headers map[string]string) ([]byte, error) {
	if _, err := recon.ValidatePublicURL(rawURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ARTEX-VulnDB/1.0")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &pocorexpHTTPError{Status: resp.StatusCode}
	}
	return io.ReadAll(io.LimitReader(resp.Body, pocorexpMaxBodyBytes))
}

var (
	// pocorexpCVEPattern CVE 编号校验:4 位年份 + 4~7 位序号(标题整串匹配)。
	pocorexpCVEPattern = regexp.MustCompile(`^CVE-\d{4}-\d{4,7}$`)
	// pocorexpRepoPattern 行内抽 GitHub 仓库链接。owner/repo 允许字母数字与 ._-,
	// 贪婪匹配到斜杠/括号等边界为止;badges 的 img.shields.io 域名不会被误收。
	pocorexpRepoPattern = regexp.MustCompile(`https://github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)`)
)

// parsePocOrExpMD 解析 PocOrExp_in_Github 的 Markdown(Today.md 与 <year>/README.md
// 同格式):按 `## CVE-xxx` 标题分段,收集每个 CVE 段内 bullet 行上的 github.com 仓库
// 链接(段内去重、保持出现顺序)。返回 CVE 编号 → 规范化仓库 URL 列表;
// 坏标题、非 github 链接、无链接的段一律丢弃。
func parsePocOrExpMD(content string) map[string][]string {
	result := make(map[string][]string)
	seen := make(map[string]map[string]bool) // 每 CVE 一个去重集(跨重复标题段也去重)
	current := ""
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			cve := strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))
			if pocorexpCVEPattern.MatchString(cve) {
				current = cve
			} else {
				current = "" // 坏标题:整段丢弃,避免链接被挂到脏键上
			}
			continue
		}
		if current == "" || !strings.HasPrefix(trimmed, "- ") {
			continue // 只认 bullet 链接行,描述段落里的散链不收
		}
		for _, m := range pocorexpRepoPattern.FindAllStringSubmatch(trimmed, -1) {
			u := "https://github.com/" + m[1] + "/" + m[2]
			if seen[current][u] {
				continue
			}
			if seen[current] == nil {
				seen[current] = make(map[string]bool)
			}
			seen[current][u] = true
			result[current] = append(result[current], u)
		}
	}
	return result
}

// pocorexpRepoSlug 从规范化仓库 URL 取 owner/repo(用于展示名)。
func pocorexpRepoSlug(repoURL string) string {
	return strings.TrimPrefix(repoURL, "https://github.com/")
}

// pocorexpAttachSections 把解析出的 CVE → 仓库链接列表挂到库内已有条目上
// (只挂不建:条目不存在跳过;CreateVulnPoc 幂等,重复链接不计入新增)。
// 按 CVE 排序处理保证行为确定;ctx 取消时立即返回已挂载数与错误。
func pocorexpAttachSections(ctx context.Context, pg *db.DB, sections map[string][]string) (int, error) {
	attached := 0
	cves := make([]string, 0, len(sections))
	for cve := range sections {
		cves = append(cves, cve)
	}
	sort.Strings(cves)
	for _, cve := range cves {
		if err := ctx.Err(); err != nil {
			return attached, err
		}
		entry, err := pg.FindVulnEntryByCVE(cve)
		if err != nil {
			log.Printf("[vulndb-sync] PocOrExp 查询 %s 失败: %v", cve, err)
			continue
		}
		if entry == nil {
			continue // 条目不存在:NVD 回填/常规采集轮会建,这里只挂不建
		}
		for _, repoURL := range sections[cve] {
			if err := ctx.Err(); err != nil {
				return attached, err
			}
			_, created, err := pg.CreateVulnPoc(db.VulnPocInput{
				EntryID:   entry.ID,
				Kind:      "poc",
				Source:    "pocorexp",
				SourceURL: repoURL,
				Name:      "PocOrExp: " + pocorexpRepoSlug(repoURL),
				Language:  "",
				Content:   "GitHub PoC/EXP: " + repoURL,
			})
			if err != nil {
				log.Printf("[vulndb-sync] PocOrExp PoC 入库失败(%s): %v", cve, err)
				continue
			}
			if created {
				attached++
			}
		}
	}
	return attached, nil
}

// syncPocOrExpToday 拉当日增量 Today.md,把 PoC/EXP 仓库挂到库内已有 CVE 条目。
// 挂进 6h 定时轮 runOnce;sync_run source=pocorexp_today。
func (v *vulnSync) syncPocOrExpToday(ctx context.Context) (pocs int, err error) {
	runID, err := v.s.m.PG().StartVulnSyncRun("pocorexp_today")
	if err != nil {
		return 0, err
	}
	defer func() {
		status := "done"
		errMsg := ""
		if err != nil {
			status, errMsg = "failed", err.Error()
		}
		_ = v.s.m.PG().FinishVulnSyncRun(runID, status, 0, 0, 0, pocs, errMsg)
	}()

	ctx, cancel := context.WithTimeout(ctx, vulnSyncTimeout)
	defer cancel()
	body, err := pocorexpFetchText(ctx, pocorexpBaseURL+"/Today.md", nil)
	if err != nil {
		return 0, fmt.Errorf("拉取 PocOrExp Today.md 失败: %w", err)
	}
	sections := parsePocOrExpMD(string(body))
	log.Printf("[vulndb-sync] PocOrExp Today 解析出 %d 个 CVE", len(sections))
	pocs, err = pocorexpAttachSections(ctx, v.s.m.PG(), sections)
	if err != nil {
		return pocs, err
	}
	// 补挂当年文件:Today.md 只含「今天新建仓库」的 CVE——发布几天后才出现 PoC
	// 仓库的 CVE,在仓库出现那天条目可能还没入库,之后就再也不会被 Today.md
	// 覆盖,造成「最新 CVE 都没 PoC」(2026-09-26 用户报障的根因之一)。当年
	// README.md 约 200KB,每轮重挂一次(幂等)即可堵住这个缺口。
	year := time.Now().UTC().Year()
	ybody, yerr := pocorexpFetchText(ctx, fmt.Sprintf("%s/%d/README.md", pocorexpBaseURL, year), nil)
	if yerr != nil {
		var httpErr *pocorexpHTTPError
		if !errors.As(yerr, &httpErr) || httpErr.Status != http.StatusNotFound {
			log.Printf("[vulndb-sync] PocOrExp %d 年文件补挂失败: %v", year, yerr)
		}
		return pocs, nil // Today.md 已挂完,当年文件失败不作为整轮错误
	}
	yAttached, aerr := pocorexpAttachSections(ctx, v.s.m.PG(), parsePocOrExpMD(string(ybody)))
	if aerr != nil {
		return pocs, aerr
	}
	pocs += yAttached
	return pocs, nil
}

// backfillPocOrExpYears 拉取 PocOrExp_in_Github 逐年历史 README(startYear..endYear
// 闭区间),把解析出的 PoC/EXP 仓库挂到库内已有条目上(只挂不建,全量幂等可重跑)。
// 由 NVD 历史回填流程(Agent A)在 NVD 阶段完成后调用;年份边界自动夹取到
// [1999, 当前年],某年 404 视为当年无数据静默跳过,其余单年错误记日志继续
// (不拖垮整个回填;漏掉的年份靠幂等重跑补齐)。返回累计新增挂载数。
func backfillPocOrExpYears(ctx context.Context, pg *db.DB, startYear, endYear int) (attached int, err error) {
	if pg == nil {
		return 0, fmt.Errorf("数据库句柄为空")
	}
	if startYear < pocorexpMinYear {
		startYear = pocorexpMinYear
	}
	if endYear > time.Now().UTC().Year() {
		endYear = time.Now().UTC().Year()
	}
	if startYear > endYear {
		return 0, nil
	}
	log.Printf("[vulndb-sync] PocOrExp 历史回填开始:%d-%d 年", startYear, endYear)
	for year := startYear; year <= endYear; year++ {
		// 年与年之间温和节流 1 秒(对上游友好),且可被 ctx 取消中断。
		if year > startYear {
			select {
			case <-ctx.Done():
				return attached, ctx.Err()
			case <-time.After(time.Second):
			}
		}
		if err := ctx.Err(); err != nil {
			return attached, err
		}
		body, ferr := pocorexpFetchText(ctx, fmt.Sprintf("%s/%d/README.md", pocorexpBaseURL, year), nil)
		if ferr != nil {
			var httpErr *pocorexpHTTPError
			if errors.As(ferr, &httpErr) && httpErr.Status == http.StatusNotFound {
				continue // 该年目录不存在:当年无数据,静默跳过
			}
			log.Printf("[vulndb-sync] PocOrExp %d 年数据拉取失败: %v", year, ferr)
			continue
		}
		n, aerr := pocorexpAttachSections(ctx, pg, parsePocOrExpMD(string(body)))
		if aerr != nil {
			return attached, aerr
		}
		attached += n
		log.Printf("[vulndb-sync] PocOrExp %d 年挂载 %d 条 PoC", year, n)
	}
	return attached, nil
}

// ---- nuclei 官方模板(projectdiscovery/nuclei-templates) ----

var (
	// nucleiTreeEntryPattern 从文件名提 CVE:http/cves/ 下(无论是否按年份分子目录)
	// 的 CVE-<year>-<id>.yaml。模板仓库早期为平铺、后来按年份分目录,这里两种都兼容。
	nucleiTreeEntryPattern = regexp.MustCompile(`(?:^|/)CVE-(\d{4}-\d{4,7})\.yaml$`)
)

// nucleiParseTree 解析 git/trees API 的 JSON 响应,返回全部文件(blob)路径与
// truncated 标记(树过大被 GitHub 截断时为 true,调用方需回退逐目录拉取)。
func nucleiParseTree(body []byte) (paths []string, truncated bool, err error) {
	var tree struct {
		Truncated bool `json:"truncated"`
		Tree      []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
	}
	if err := json.Unmarshal(body, &tree); err != nil {
		return nil, false, err
	}
	paths = make([]string, 0, len(tree.Tree))
	for _, e := range tree.Tree {
		if e.Type == "blob" {
			paths = append(paths, e.Path)
		}
	}
	return paths, tree.Truncated, nil
}

// nucleiContentsEntry contents API 目录条目(只需要文件名/路径/类型)。
type nucleiContentsEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"`
}

// nucleiContentsPaths 从 contents API 目录条目里取出文件路径(纯函数,便于测试)。
func nucleiContentsPaths(entries []nucleiContentsEntry) []string {
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Type == "file" && e.Path != "" {
			paths = append(paths, e.Path)
		}
	}
	return paths
}

// nucleiFilterCVEPaths 筛出 http/cves/ 下的 CVE 模板文件,返回 CVE 编号 → 模板路径
// (文件名正则提 CVE;同编号多个路径时保留先出现的)。
func nucleiFilterCVEPaths(paths []string) map[string]string {
	out := make(map[string]string)
	for _, p := range paths {
		if !strings.HasPrefix(p, "http/cves/") {
			continue
		}
		m := nucleiTreeEntryPattern.FindStringSubmatch(p)
		if m == nil {
			continue
		}
		cve := "CVE-" + m[1]
		if _, ok := out[cve]; !ok {
			out[cve] = p
		}
	}
	return out
}

// nucleiFetchJSON 复用纯文本抓取 helper 拉 JSON 并反序列化(64MB 有界读同源)。
func nucleiFetchJSON(ctx context.Context, rawURL string, headers map[string]string, out any) error {
	body, err := pocorexpFetchText(ctx, rawURL, headers)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// nucleiFetchCVEPathsByYear 逐年份目录调 GitHub contents API 收集模板路径
// (树响应 truncated=true 时的回退;每目录一次调用,年份从 vulnBackfillWindowStart
// 到当前年)。注意 contents API 单目录最多返回 1000 条,超限年份可能不全——
// 正常路径是未截断的全库树,这里只作兜底。
func nucleiFetchCVEPathsByYear(ctx context.Context, headers map[string]string) ([]string, error) {
	endYear := time.Now().UTC().Year()
	var all []string
	for year := vulnBackfillWindowStart; year <= endYear; year++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		api := fmt.Sprintf("%s/contents/http/cves/%d", nucleiRepoAPIBase, year)
		var entries []nucleiContentsEntry
		if err := nucleiFetchJSON(ctx, api, headers, &entries); err != nil {
			// 单年失败(目录不存在/超 1000 条/网络抖动)记日志继续,不拖垮整轮。
			log.Printf("[vulndb-sync] nuclei 模板 %d 年目录拉取失败: %v", year, err)
			continue
		}
		all = append(all, nucleiContentsPaths(entries)...)
	}
	return all, nil
}

// nucleiAttachTemplates 把 CVE → 模板路径映射挂到库内已有条目(只挂不建,幂等)。
// 挂载**新增**行后当场抓模板正文写入(新增量小;client 为 nil 时跳过抓取)。
// Content 刻意留空入库:抓取失败保留空 content,由 nuclei 正文回填
// (vulndb_nuclei_body.go,按 source='nuclei' AND content='' 找目标)兜底补齐;
// 当场抓失败不算失败、不报 error。
func nucleiAttachTemplates(ctx context.Context, pg *db.DB, client *http.Client, templates map[string]string) (int, error) {
	attached := 0
	cves := make([]string, 0, len(templates))
	for cve := range templates {
		cves = append(cves, cve)
	}
	sort.Strings(cves)
	for _, cve := range cves {
		if err := ctx.Err(); err != nil {
			return attached, err
		}
		entry, err := pg.FindVulnEntryByCVE(cve)
		if err != nil {
			log.Printf("[vulndb-sync] nuclei 查询 %s 失败: %v", cve, err)
			continue
		}
		if entry == nil {
			continue // 条目不存在:只挂不建
		}
		path := templates[cve]
		sourceURL := nucleiTemplateURLPrefix + path
		_, created, err := pg.CreateVulnPoc(db.VulnPocInput{
			EntryID:   entry.ID,
			Kind:      "nuclei",
			Source:    "nuclei",
			SourceURL: sourceURL,
			Name:      "nuclei 模板: " + cve,
			Language:  "yaml",
			Content:   "", // 正文当场抓(下);失败留空等回填兜底
		})
		if err != nil {
			log.Printf("[vulndb-sync] nuclei PoC 入库失败(%s): %v", cve, err)
			continue
		}
		if !created {
			continue
		}
		attached++
		if client == nil {
			continue // 无可用客户端:保留空 content,回填兜底
		}
		// 当场抓正文(单次尝试,不重试不退避):失败只记日志,不算整轮失败。
		if err := nucleiBodyPace(ctx); err != nil {
			return attached, err
		}
		body, ferr := nucleiBodyFetchOnce(ctx, client, nucleiRawBaseURL+path)
		if ferr != nil {
			log.Printf("[vulndb-sync] nuclei 模板正文当场抓取失败(%s,留待回填): %v", path, ferr)
			continue
		}
		if strings.TrimSpace(string(body)) == "" {
			continue
		}
		if _, werr := pg.SetVulnPocContent(entry.ID, sourceURL, string(body), "yaml"); werr != nil {
			log.Printf("[vulndb-sync] nuclei 模板正文写入失败(%s,留待回填): %v", path, werr)
		}
	}
	return attached, nil
}

// syncNucleiTemplates 拉取 nuclei-templates 全库文件树,把 http/cves/ 下的 CVE
// 模板挂到库内已有条目。树被截断时回退逐年份目录拉取。挂进 6h 定时轮 runOnce;
// sync_run source=nuclei。
func (v *vulnSync) syncNucleiTemplates(ctx context.Context) (pocs int, err error) {
	runID, err := v.s.m.PG().StartVulnSyncRun("nuclei")
	if err != nil {
		return 0, err
	}
	defer func() {
		status := "done"
		errMsg := ""
		if err != nil {
			status, errMsg = "failed", err.Error()
		}
		_ = v.s.m.PG().FinishVulnSyncRun(runID, status, 0, 0, 0, pocs, errMsg)
	}()

	ctx, cancel := context.WithTimeout(ctx, vulnSyncTimeout)
	defer cancel()

	// 可选 GitHub 令牌(vulndb_github_token):核心 API 配额 60 次/时 → 5000 次/时。
	headers := map[string]string{}
	if token, _, err := v.s.m.PG().GetSetting(settingVulnGitHubToken); err == nil && strings.TrimSpace(token) != "" {
		headers["Authorization"] = "Bearer " + strings.TrimSpace(token)
	}

	body, err := pocorexpFetchText(ctx, nucleiTreeAPI, headers)
	if err != nil {
		return 0, fmt.Errorf("拉取 nuclei 模板树失败: %w", err)
	}
	paths, truncated, err := nucleiParseTree(body)
	if err != nil {
		return 0, fmt.Errorf("解析 nuclei 模板树失败: %w", err)
	}
	if truncated {
		// 树被 GitHub 截断:回退为逐年份目录调 contents API 重新收集。
		log.Printf("[vulndb-sync] nuclei 模板树被截断,回退逐目录拉取")
		fallback, ferr := nucleiFetchCVEPathsByYear(ctx, headers)
		if ferr != nil {
			return 0, fmt.Errorf("回退拉取 nuclei 模板目录失败: %w", ferr)
		}
		paths = fallback
	}
	templates := nucleiFilterCVEPaths(paths)
	log.Printf("[vulndb-sync] nuclei 模板解析出 %d 个 CVE", len(templates))
	// 挂新增行时用回填专用客户端(30s 上限/代理感知)当场抓正文。
	pocs, err = nucleiAttachTemplates(ctx, v.s.m.PG(), v.s.nucleiBodyHTTPClient(), templates)
	return pocs, err
}
