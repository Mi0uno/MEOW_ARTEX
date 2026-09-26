package server

// 漏洞归类的搜索取证:AI 判定 0day/nday 前,先从公开情报源搜集「该漏洞是否
// 已被公开披露」的证据,连同 finding 一起交给 LLM 综合判定;同时从证据里提取
// 厂商/产品(vendor/product/affected)入库。
//
// 四个证据源(全部走既有出网设施;单源失败静默跳过,证据少一条不影响判定):
//   1. 本地漏洞库 vulndb_entries(同步的 NVD/Exploit-DB/PoC 情报,零成本最快);
//   2. NVD keywordSearch(确认是否存在对应 CVE 及其官方描述/CPE 厂商字段);
//   3. GitHub 仓库搜索(公开 PoC/复现);
//   4. DuckDuckGo Lite 网页搜索(厂商公告/安全社区/公众号文章等公开面)。
//
// 搜索词由 LLM 从 finding 提炼(中文成果名 → 英文官方组件名+漏洞类型,直搜
// 英文情报源才有命中率),失败回退确定性构造。总预算 classifySearchBudget;
// settings `vulndb_classify_search=false` 可整体关闭(关=退回纯语义判定)。

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/recon"
	"github.com/Autumn-27/norma/llm"
)

const (
	// settingVulnClassifySearch 归类时是否联网搜索取证("false"=关,缺省开)。
	settingVulnClassifySearch = "vulndb_classify_search"
	// classifySearchTimeout 单个搜索源的超时。
	classifySearchTimeout = 12 * time.Second
	// classifySearchBudget 单次归类的证据收集总预算(含 LLM 提炼搜索词)。
	classifySearchBudget = 60 * time.Second
	// classifyMaxEvidence 单次入库携带的最多证据条数。
	classifyMaxEvidence = 10
)

// classifyEvidence 是一条「该漏洞可能已被公开披露」的检索证据。
type classifyEvidence struct {
	Source  string `json:"source"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// versionPattern 抓组件版本号(x.y[.z...]),用于提升 NVD/GitHub 查询精确率。
var versionPattern = regexp.MustCompile(`(?i)\b\d+\.\d+(?:\.\d+){0,3}\b`)

// buildClassifyQueries 从 finding 确定性构造检索词(LLM 不可用时的回退,
// 也作为 LLM 提炼结果的兜底补充)。
func buildClassifyQueries(f *db.DBFinding) []string {
	name := strings.TrimSpace(f.Name)
	if name == "" {
		name = strings.TrimSpace(f.VulnClass)
	}
	// 去掉常见标题后缀噪音,让查询更接近组件名本身。
	clean := name
	for _, suffix := range []string{"漏洞复现", "复现漏洞", "漏洞", "复现"} {
		clean = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(clean), suffix))
	}
	seen := map[string]bool{}
	var qs []string
	add := func(q string) {
		q = strings.TrimSpace(q)
		if len([]rune(q)) < 2 || seen[q] {
			return
		}
		seen[q] = true
		qs = append(qs, q)
	}
	add(clean)
	// 摘要/证据里的版本号能显著提高检索命中率。
	if clean != "" {
		if v := versionPattern.FindString(f.Summary + " " + f.Evidence); v != "" {
			add(clean + " " + v)
		}
	}
	if clean != name {
		add(name)
	}
	return qs
}

// llmClassifyQueries 让 LLM 把 finding 提炼成适合公开情报源检索的查询词
// (中文成果名翻译成英文官方组件名;中文组件同时保留中文名)。失败返回 nil。
func (s *Server) llmClassifyQueries(ctx context.Context, prov llm.Provider, f *db.DBFinding) []string {
	system := "你是漏洞情报检索助手。把漏洞发现提炼成 2-3 个检索词,用于在 NVD/GitHub/搜索引擎检索该漏洞是否为已知漏洞。" +
		"要求:组件名用英文官方名(如「用友」→ Yonyou/UFIDA),中文组件同时保留中文名;附带漏洞类型关键词(rce/sql injection 等)。" +
		"只输出 JSON:{\"queries\":[\"...\"]},不要解释。"
	user := fmt.Sprintf("漏洞名称: %s\n类别: %s\n摘要: %s\n证据片段: %s",
		f.Name, f.VulnClass, truncateRunes(f.Summary, 500), truncateRunes(f.Evidence, 800))
	qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := analyzeCompletion(qctx, prov, system, user)
	if err != nil {
		log.Printf("[vulndb] 归类搜索词提炼失败(回退确定性构造): %v", err)
		return nil
	}
	out = strings.TrimSpace(out)
	out = strings.TrimPrefix(out, "```json")
	out = strings.TrimPrefix(out, "```")
	out = strings.TrimSuffix(strings.TrimSpace(out), "```")
	var parsed struct {
		Queries []string `json:"queries"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &parsed); err != nil {
		log.Printf("[vulndb] 归类搜索词提炼返回无法解析: %v", err)
		return nil
	}
	var qs []string
	for _, q := range parsed.Queries {
		q = strings.TrimSpace(q)
		if q != "" {
			qs = append(qs, q)
		}
	}
	return qs
}

// searchClassifyLocal 在本地漏洞库(同步情报)中检索同名/同组件的已知条目。
func (s *Server) searchClassifyLocal(ctx context.Context, q string) []classifyEvidence {
	if strings.TrimSpace(q) == "" {
		return nil
	}
	entries, _, _, err := s.m.PG().ListVulnEntries(db.VulnEntryFilter{Q: q, Limit: 3})
	if err != nil {
		return nil
	}
	var out []classifyEvidence
	for _, e := range entries {
		u := ""
		if e.CVEID != "" {
			u = "https://nvd.nist.gov/vuln/detail/" + e.CVEID
		}
		snippet := e.Description
		if strings.TrimSpace(snippet) == "" {
			snippet = fmt.Sprintf("已有条目:分类=%s 严重度=%s", e.Category, e.Severity)
		}
		out = append(out, classifyEvidence{
			Source: "本地漏洞库", Title: e.Title, URL: u,
			Snippet: truncateRunes(snippet, 200),
		})
	}
	return out
}

// nvdSearchResp 是 NVD CVE API 2.0 单次响应的通用解析结构
// (keywordSearch 与 cveId 查询共用同一响应形状)。
type nvdSearchResp struct {
	Vulnerabilities []struct {
		CVE struct {
			ID           string `json:"id"`
			Descriptions []struct {
				Lang  string `json:"lang"`
				Value string `json:"value"`
			} `json:"descriptions"`
			Configurations []struct {
				Nodes []struct {
					CPEMatch []struct {
						Criteria string `json:"criteria"`
					} `json:"cpeMatch"`
				} `json:"nodes"`
			} `json:"configurations"`
		} `json:"cve"`
	} `json:"vulnerabilities"`
}

// firstCPE 返回响应里第一条 CPE(官方厂商/产品写法参照)。
func (r *nvdSearchResp) firstCPE() string {
	for _, item := range r.Vulnerabilities {
		for _, cfg := range item.CVE.Configurations {
			for _, node := range cfg.Nodes {
				for _, m := range node.CPEMatch {
					if m.Criteria != "" {
						return m.Criteria
					}
				}
			}
		}
	}
	return ""
}

// cpeVendorProduct 从 CPE 2.3 串(cpe:2.3:a:vendor:product:version:...)拆出
// 官方厂商/产品/版本;通配与缺失段返回空。
func cpeVendorProduct(cpe string) (vendor, product, affected string) {
	parts := strings.Split(cpe, ":")
	if len(parts) < 6 || parts[0] != "cpe" {
		return "", "", ""
	}
	vendor, product = parts[3], parts[4]
	if v := parts[5]; v != "*" && v != "-" {
		affected = v
	}
	return vendor, product, affected
}

// parseNVDEvidence 把 NVD 响应转成证据条目(带首条 CPE 参照,给 LLM 提取
// vendor/product 用官方写法)。
func parseNVDEvidence(raw []byte) []classifyEvidence {
	var parsed nvdSearchResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	var out []classifyEvidence
	for _, item := range parsed.Vulnerabilities {
		c := item.CVE
		if c.ID == "" {
			continue
		}
		desc := ""
		for _, d := range c.Descriptions {
			if d.Lang == "en" {
				desc = d.Value
				break
			}
		}
		if cpe := parsed.firstCPE(); cpe != "" {
			desc += " (CPE: " + cpe + ")"
		}
		out = append(out, classifyEvidence{
			Source: "NVD", Title: c.ID, URL: "https://nvd.nist.gov/vuln/detail/" + c.ID,
			Snippet: truncateRunes(desc, 300),
		})
	}
	return out
}

// searchNVDKeyword 用 NVD keywordSearch 检索是否有对应 CVE。
func searchNVDKeyword(ctx context.Context, q string) []classifyEvidence {
	if strings.TrimSpace(q) == "" {
		return nil
	}
	api := "https://services.nvd.nist.gov/rest/json/cves/2.0?keywordSearch=" + url.QueryEscape(q) + "&resultsPerPage=3"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "ARTEX-VulnDB/1.0")
	client := &http.Client{Timeout: classifySearchTimeout}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[vulndb] 归类 NVD 检索失败: %v", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("[vulndb] 归类 NVD 检索 HTTP %d", resp.StatusCode)
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil
	}
	return parseNVDEvidence(raw)
}

// nvdLookupCVE 按 CVE 编号精确查 NVD,返回官方厂商/产品(CPE)与受影响版本。
// 确定性分类路径(cve 编号直取)不调 LLM,厂商识别全靠这一步;失败静默。
func nvdLookupCVE(ctx context.Context, cveID string) (vendor, product, affected string) {
	api := "https://services.nvd.nist.gov/rest/json/cves/2.0?cveId=" + url.QueryEscape(cveID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return "", "", ""
	}
	req.Header.Set("User-Agent", "ARTEX-VulnDB/1.0")
	client := &http.Client{Timeout: classifySearchTimeout}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[vulndb] 归类 NVD 反查 %s 失败: %v", cveID, err)
		return "", "", ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", ""
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", "", ""
	}
	var parsed nvdSearchResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", "", ""
	}
	return cpeVendorProduct(parsed.firstCPE())
}

// searchGitHubPoc 搜公开 PoC/复现仓库(复用漏洞库同步的 GitHub 出网模式)。
func searchGitHubPoc(ctx context.Context, token, q string) []classifyEvidence {
	if strings.TrimSpace(q) == "" {
		return nil
	}
	api := "https://api.github.com/search/repositories?q=" + url.QueryEscape(q+" poc") + "&per_page=3&sort=stars"
	headers := map[string]string{}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	var result struct {
		Items []struct {
			FullName    string `json:"full_name"`
			HTMLURL     string `json:"html_url"`
			Description string `json:"description"`
		} `json:"items"`
	}
	if err := fetchJSON(ctx, api, headers, &result); err != nil {
		log.Printf("[vulndb] 归类 GitHub PoC 检索失败: %v", err)
		return nil
	}
	var out []classifyEvidence
	for _, repo := range result.Items {
		out = append(out, classifyEvidence{
			Source: "GitHub", Title: repo.FullName, URL: repo.HTMLURL,
			Snippet: truncateRunes(repo.Description, 200),
		})
	}
	return out
}

// DuckDuckGo Lite 结果页的极简结构:锚点带 result-link、摘要在
// result-snippet 单元格;链接可能经 /l/?uddg=<encoded> 跳转。
var (
	ddgAnchorRe  = regexp.MustCompile(`(?is)<a\s([^>]*result-link[^>]*)>(.*?)</a>`)
	ddgHrefRe    = regexp.MustCompile(`href=["']([^"']+)["']`)
	ddgSnippetRe = regexp.MustCompile(`(?is)<td[^>]*result-snippet[^>]*>(.*?)</td>`)
	ddgTagRe     = regexp.MustCompile(`(?s)<[^>]*>`)
	ddgUddgRe    = regexp.MustCompile(`[?&]uddg=([^&'"]+)`)
)

// searchWebDDG 用 DuckDuckGo Lite 做通用网页搜索(厂商公告/安全文章等公开面)。
// 免凭据但可能限流/人机校验:拿不到结果时静默返回空,不影响判定链路。
func searchWebDDG(ctx context.Context, q string) []classifyEvidence {
	if strings.TrimSpace(q) == "" {
		return nil
	}
	target := "https://lite.duckduckgo.com/lite/?q=" + url.QueryEscape(q)
	if _, err := recon.ValidatePublicURL(target); err != nil {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil
	}
	// 数据中心 UA 会被 DDG 拒,用普通浏览器 UA。
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	client := &http.Client{Timeout: classifySearchTimeout}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[vulndb] 归类网页搜索失败: %v", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("[vulndb] 归类网页搜索 HTTP %d", resp.StatusCode)
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	return parseDDGLite(string(raw))
}

// parseDDGLite 解析 DuckDuckGo Lite 结果页(标题链接+按序配对的摘要)。
func parseDDGLite(page string) []classifyEvidence {
	// 清洗单元格文本:先反转义再剥标签——DDG 摘要里的标签是实体转义存放的
	// (&lt;b&gt;),先剥后反转义会让标签复活。
	cleanText := func(s string) string {
		return strings.TrimSpace(ddgTagRe.ReplaceAllString(html.UnescapeString(s), ""))
	}
	var out []classifyEvidence
	snippets := ddgSnippetRe.FindAllStringSubmatch(page, -1)
	for i, m := range ddgAnchorRe.FindAllStringSubmatch(page, 8) {
		href := ""
		if am := ddgHrefRe.FindStringSubmatch(m[1]); am != nil {
			href = html.UnescapeString(am[1])
		}
		// 还原 /l/?uddg=<encoded> 跳转的真实 URL。
		if um := ddgUddgRe.FindStringSubmatch(href); um != nil {
			if dec, err := url.QueryUnescape(um[1]); err == nil {
				href = dec
			}
		}
		if !strings.HasPrefix(href, "http") {
			continue
		}
		title := cleanText(m[2])
		if title == "" {
			continue
		}
		snippet := ""
		if i < len(snippets) {
			snippet = cleanText(snippets[i][1])
		}
		out = append(out, classifyEvidence{
			Source: "网页搜索", Title: truncateRunes(title, 120), URL: href,
			Snippet: truncateRunes(snippet, 200),
		})
		if len(out) >= 3 {
			break
		}
	}
	return out
}

// mergeClassifyEvidence 把新证据并入聚合列表(按 URL/标题去重)。
func mergeClassifyEvidence(out []classifyEvidence, seen map[string]bool, evs []classifyEvidence) []classifyEvidence {
	for _, ev := range evs {
		key := ev.URL
		if key == "" {
			key = "t:" + ev.Title
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ev)
	}
	return out
}

// gatherClassifyEvidence 汇总四源检索证据(预算内尽力而为,失败静默)。
// settings 关闭或全部源失败时返回 nil(调用方按纯语义判定)。
func (s *Server) gatherClassifyEvidence(ctx context.Context, f *db.DBFinding) []classifyEvidence {
	if val, _, _ := s.m.pg.GetSetting(settingVulnClassifySearch); strings.TrimSpace(val) == "false" {
		return nil
	}
	overall, cancel := context.WithTimeout(ctx, classifySearchBudget)
	defer cancel()

	queries := buildClassifyQueries(f)
	// LLM 提炼搜索词(中文成果名 → 英文组件名),确定性结果兜底去重后拼在后面。
	if p, err := s.m.pg.ActiveProfile(); err == nil && p != nil {
		if prov, _, ok := s.providerForProfile(p.ID); ok {
			if qs := s.llmClassifyQueries(overall, prov, f); len(qs) > 0 {
				extra := buildClassifyQueries(f)
				for _, q := range extra {
					dup := false
					for _, have := range qs {
						if strings.EqualFold(have, q) {
							dup = true
							break
						}
					}
					if !dup {
						qs = append(qs, q)
					}
				}
				queries = qs
			}
		}
	}
	if len(queries) == 0 {
		return nil
	}
	q1 := queries[0]
	q2 := ""
	if len(queries) > 1 {
		q2 = queries[1]
	}

	out := []classifyEvidence{}
	seen := map[string]bool{}
	add := func(evs []classifyEvidence) {
		out = mergeClassifyEvidence(out, seen, evs)
	}
	// 单源限时跑一段网络检索。
	run := func(fn func(context.Context) []classifyEvidence) {
		cctx, ccancel := context.WithTimeout(overall, classifySearchTimeout)
		defer ccancel()
		add(fn(cctx))
	}

	// 本地库零成本,先跑;命中不足再补第二个查询词。
	add(s.searchClassifyLocal(overall, q1))
	if len(out) < 2 && q2 != "" {
		add(s.searchClassifyLocal(overall, q2))
	}
	run(func(c context.Context) []classifyEvidence { return searchNVDKeyword(c, q1) })
	token := ""
	if val, _, err := s.m.pg.GetSetting(settingVulnGitHubToken); err == nil {
		token = strings.TrimSpace(val)
	}
	run(func(c context.Context) []classifyEvidence { return searchGitHubPoc(c, token, q1) })
	run(func(c context.Context) []classifyEvidence { return searchWebDDG(c, q1) })
	if len(out) < 3 && q2 != "" {
		run(func(c context.Context) []classifyEvidence { return searchWebDDG(c, q2) })
	}

	if len(out) > classifyMaxEvidence {
		out = out[:classifyMaxEvidence]
	}
	return out
}
