package server

// 安全社区文章 RSS 采集(v2,契约 §6.1):从 FreeBuf / 安全客 / 先知三个公开 RSS 源
// 拉取最新文章,用正则提取标题与摘要里提到的 CVE 编号,给漏洞库补情报:
//   - 编号已有条目 → 把文章链接合并进 refs(参考来源);
//   - 编号没有条目 → 建一条轻量 CVE 条目(严重度/厂商留空,等 NVD/GHSA 下一轮补全),
//     并打「文章」标签,便于识别该条目来自社区文章。
//
// 定位是「情报补充」而非权威数据源:文章只提供发现渠道与参考链接,描述只截断入库,
// 不抓正文全文。挂在 vulndb_sync 的 runOnce 轮里(默认 6h 一轮);单源失败只记日志,
// 全部源失败才上抛错误,不让某个社区站的可用性问题拖垮整轮采集。

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Autumn-27/artex/recon"
)

const (
	// articleSyncBudget 单轮总预算(三个源 + 逐条入库)。超时后本轮提前收尾,下轮继续。
	articleSyncBudget = 90 * time.Second
	// articleFeedTimeout 单源超时:社区站偶尔很慢,不能拖住整轮。
	articleFeedTimeout = 20 * time.Second
	// articleFetchLimit 单源响应上限 16MB(RSS 正常几十 KB,防异常站点撑爆内存)。
	articleFetchLimit = 16 << 20
	// articleTitleMax / articleDescMax 入库标题与描述的截断长度(按字符)。
	articleTitleMax = 120
	articleDescMax  = 1000
)

// articleFeeds 公开 RSS 源表(均为安全社区的公开订阅地址,无需凭据)。
// freebuf 偶尔对非浏览器 UA 返回非 200:失败即记日志跳过,不重试、不拖垮整轮。
var articleFeeds = []struct {
	name string
	url  string
}{
	{"freebuf", "https://www.freebuf.com/feed"},
	{"anquanke", "https://api.anquanke.com/data/v1/rss"},
	{"xz", "https://xz.aliyun.com/feed"},
}

// articleCVERe 匹配 CVE 编号主体(大小写不敏感)。前后边界不在正则里做,原因:
// Go RE2 不支持 lookahead,而 `\b` 挡不住「编号后紧跟更多数字」的截断误报
// (如 CVE-2024-12345678 会配出 CVE-2024-1234567),边界由 articleExtractCVEs 校验。
var articleCVERe = regexp.MustCompile(`(?i)CVE-\d{4}-\d{4,7}`)

// articleHTMLTagRe 匹配 HTML 标签(RSS 摘要里常见 <p>/<a href=…>/<br/> 等)。
var articleHTMLTagRe = regexp.MustCompile(`<[^>]+>`)

// articleRSSFeed 是 RSS 2.0 的最小解析结构(channel/title/link/description/item[])。
// 顶层 Items 顺带兼容 RSS 1.0(RDF)把 item 放在 channel 外层的情况。
type articleRSSFeed struct {
	Channel struct {
		Title       string              `xml:"title"`
		Link        string              `xml:"link"`
		Description string              `xml:"description"`
		Items       []articleRSSXMLItem `xml:"item"`
	} `xml:"channel"`
	Items []articleRSSXMLItem `xml:"item"`
}

// articleRSSXMLItem 是 RSS <item> 的原始字段。
type articleRSSXMLItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
}

// articleRSSItem 是清洗后的文章条目(供编号提取与入库用)。
type articleRSSItem struct {
	Feed        string
	Title       string
	Link        string
	Description string
}

// syncArticleFeeds 抓取安全社区文章 RSS,提取 CVE 编号入库/补参考链接。
// merged = 本轮动作数(新建条目数 + 补 ref 条数)。
func (v *vulnSync) syncArticleFeeds(ctx context.Context) (merged int, err error) {
	runID, err := v.s.m.PG().StartVulnSyncRun("articles")
	if err != nil {
		return 0, err
	}
	var created, refAdded, fetched int
	defer func() {
		merged = created + refAdded
		status := "done"
		errMsg := ""
		if err != nil {
			status, errMsg = "failed", err.Error()
		}
		_ = v.s.m.PG().FinishVulnSyncRun(runID, status, fetched, created, refAdded, 0, errMsg)
	}()

	ctx, cancel := context.WithTimeout(ctx, articleSyncBudget)
	defer cancel()

	// 逐源抓取:单源失败(超时/非 200/解析错)记日志继续,全部失败才报错。
	items := make([]articleRSSItem, 0, 64)
	failed := 0
	for _, f := range articleFeeds {
		if ctx.Err() != nil {
			break
		}
		body, err := articleFetchText(ctx, f.url, articleFetchLimit)
		if err != nil {
			failed++
			log.Printf("[vulndb-sync] 文章源 %s(%s)失败: %v", f.name, f.url, err)
			continue
		}
		parsed, err := articleParseRSS(body)
		if err != nil {
			failed++
			log.Printf("[vulndb-sync] 文章源 %s 解析失败: %v", f.name, err)
			continue
		}
		log.Printf("[vulndb-sync] 文章源 %s: %d 条", f.name, len(parsed))
		for i := range parsed {
			parsed[i].Feed = f.name
		}
		items = append(items, parsed...)
	}
	if failed > 0 && failed == len(articleFeeds) {
		return merged, fmt.Errorf("全部 %d 个文章源抓取失败", len(articleFeeds))
	}
	fetched = len(items)

	for _, it := range items {
		// 总预算用尽:本轮到此为止(已处理的动作已计入),下轮继续。
		if ctx.Err() != nil {
			log.Printf("[vulndb-sync] 文章抓取预算用尽,提前收尾")
			return merged, nil
		}
		cves := articleExtractCVEs(it.Title + " " + it.Description)
		if len(cves) == 0 {
			continue // 与漏洞无关的文章(访谈/观点/活动)不碰库
		}
		link := strings.TrimSpace(it.Link)
		for _, cve := range cves {
			entry, err := v.s.m.PG().FindVulnEntryByCVE(cve)
			if err != nil {
				log.Printf("[vulndb-sync] 文章源查询 %s 失败: %v", cve, err)
				continue
			}
			// 已有条目:文章链接并入 refs(db 层 JSONB 去重合并)。已有同链接则跳过,
			// 避免每轮重复计数、重复写。
			if entry != nil {
				if link == "" || articleHasRef(entry.Refs, link) {
					continue
				}
				if err := v.s.m.PG().MergeVulnRefs(entry.ID, []string{link}); err != nil {
					log.Printf("[vulndb-sync] 文章补 ref %s 失败: %v", cve, err)
					continue
				}
				refAdded++
				continue
			}
			// 没有条目:建轻量条目(严重度/厂商/产品留空,由 NVD/GHSA 轮补全)。
			// UpsertCVE 幂等,与其它源并发时也不会建重。
			title := strings.TrimSpace(cve + " " + truncateRunes(articleStripHTML(it.Title), articleTitleMax))
			desc := truncateRunes(articleStripHTML(it.Description), articleDescMax)
			refs := []string{}
			if link != "" {
				refs = append(refs, link)
			}
			id, _, err := v.s.m.PG().UpsertCVE(cve, title, desc, "", "", "", "", vulnJSON(refs))
			if err != nil {
				log.Printf("[vulndb-sync] 文章建档 %s 失败: %v", cve, err)
				continue
			}
			// 打「文章」标签;打标失败不算建档失败(条目已在,下轮不会重试打标,可接受)。
			if err := v.s.m.PG().MergeVulnTags(id, []string{"文章"}); err != nil {
				log.Printf("[vulndb-sync] 文章打标 %s 失败: %v", cve, err)
			}
			created++
		}
	}
	return merged, nil
}

// articleFetchText 拉取一个文本资源:先过 recon.ValidatePublicURL(只允许公网
// http/https,拒绝内网/环回/保留地址),再 GET 并限长读取。
func articleFetchText(ctx context.Context, rawURL string, limit int64) (string, error) {
	if _, err := recon.ValidatePublicURL(rawURL); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, articleFeedTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "ARTEX-VulnDB/1.0")
	req.Header.Set("Accept", "application/rss+xml, application/xml, text/xml, */*")
	client := &http.Client{Timeout: articleFeedTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("上游返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// articleParseRSS 解析 RSS 文本为条目列表(RSS 2.0 为主,兼容 RDF 的外层 item)。
func articleParseRSS(body string) ([]articleRSSItem, error) {
	var feed articleRSSFeed
	if err := xml.Unmarshal([]byte(body), &feed); err != nil {
		return nil, err
	}
	raw := make([]articleRSSXMLItem, 0, len(feed.Channel.Items)+len(feed.Items))
	raw = append(raw, feed.Channel.Items...)
	raw = append(raw, feed.Items...)
	out := make([]articleRSSItem, 0, len(raw))
	for _, it := range raw {
		out = append(out, articleRSSItem{
			Title:       strings.TrimSpace(it.Title),
			Link:        strings.TrimSpace(it.Link),
			Description: it.Description,
		})
	}
	return out, nil
}

// articleExtractCVEs 从文本抽取全部 CVE 编号:统一大写、去重、保序。
// 边界校验过滤三类误报:
//   - 前缀粘连(如 xCVE-2024-1234,编号连在更长 token 里);
//   - 尾部截断(如「CVE-2024-」后面没有序列号);
//   - 序列号超长(如 CVE-2024-12345678,8 位以上不合法,宁可漏收不误收)。
func articleExtractCVEs(s string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, loc := range articleCVERe.FindAllStringIndex(s, -1) {
		start, end := loc[0], loc[1]
		if start > 0 {
			prev := s[start-1]
			if articleIsWordByte(prev) || prev == '-' {
				continue
			}
		}
		if end < len(s) && s[end] >= '0' && s[end] <= '9' {
			continue // 序列号后面还是数字:被截断或超长,不收
		}
		cve := strings.ToUpper(s[start:end])
		if !seen[cve] {
			seen[cve] = true
			out = append(out, cve)
		}
	}
	return out
}

// articleIsWordByte 判断字节是否为 ASCII 单词字符(编号粘连判定用;中文等多字节
// 字符的首尾字节都 ≥0x80,视为非单词字符,因此「漏洞CVE-2024-1234」能正常命中)。
func articleIsWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// articleStripHTML 清洗 RSS 里的 HTML 片段:去标签 + 实体反转义 + 折叠空白。
// 只用标准库,不引第三方依赖。标签替换为空格而非空串,避免「</p><p>」把英文单词
// 粘在一起;多余空白统一压成单个空格。
func articleStripHTML(s string) string {
	s = articleHTMLTagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}

// articleHasRef 判断条目 refs(JSON 字符串数组)里是否已有该链接。
// 解析失败按「没有」处理:后续 MergeVulnRefs 的合并语义在 db 层兜底,不会写坏数据。
func articleHasRef(raw json.RawMessage, url string) bool {
	if len(raw) == 0 {
		return false
	}
	var refs []string
	if err := json.Unmarshal(raw, &refs); err != nil {
		return false
	}
	for _, r := range refs {
		if r == url {
			return true
		}
	}
	return false
}
