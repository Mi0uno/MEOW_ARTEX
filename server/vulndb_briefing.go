package server

// CVE 情报简报 + 漏洞库检索的服务端实现(契约 POC_INTEL_CONTRACTS.md,Agent A 所有)。
//
//	vulnBriefBlock:任务启动/运行时给 planner/worker/mainagent 系统提示词追加的
//	  「CVE 情报简报」文本(按任务资产指纹自动匹配库内漏洞)。返回 "" = 不注入。
//	vulndbSearchFor:vulndb_search 工具的后端(agent.vulndbSearchTool 调用)。
//
// 两条链路都是【纯确定性】拼装,不打 LLM:
//	简报 = 开关判据(任务级三态→全局缺省开)→ 任务资产指纹(technologies/service_name/
//	page_title 分词+停用词去噪)→ db.MatchVulnEntriesForFingerprints → 分段组文,
//	per-task 进程内缓存 10min(任务资产任务内基本不变)。
//	检索 = ListVulnEntries(Q+分类+严重度/KEV/PoC 过滤) → LLM 友好文本。
//
// 开关语义与 vulndb_api.go 的 settings 读取同一判据:只有显式 "false" 才算关。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Autumn-27/artex/db"
)

const (
	// vulnBriefCacheTTL 简报进程内缓存时长(契约 §4.1:per-task ~10min)。
	vulnBriefCacheTTL = 10 * time.Minute
	// vulnBriefMaxTerms 参与匹配的指纹词上限(technologies 优先,page_title 分词殿后;
	// 控制匹配 SQL 的 ILIKE 模式规模)。
	vulnBriefMaxTerms = 40
	// vulnBriefMaxAssets 取任务资产条数上限(与 memory 简报同量级,足够覆盖指纹面)。
	vulnBriefMaxAssets = 200
	// vulnBriefMatchLimit 注入简报的命中条目上限(契约 §4.1:固定 30)。
	vulnBriefMatchLimit = 30
	// vulnBriefTextMaxRunes 简报整体字数上限(与记忆简报同量级,防提示词膨胀)。
	vulnBriefTextMaxRunes = 8000
	// vulnBriefFPMaxHits 简报 wire 里带命中计数的指纹词上限(契约 §10.2:前 24 词逐词 COUNT)。
	vulnBriefFPMaxHits = 24
)

// vulnBriefNoiseWords 指纹去噪停用词表。只停「协议/域名碎片、标记语言、页面模板
// 与泛运维词」——nginx/apache/shiro/seeyon(致远oa) 这类真指纹绝不停用(契约 §4.1/§10.2 明示)。
// 判定在小写化后进行;表外的词一律放行,宁可多匹配不可漏指纹。
var vulnBriefNoiseWords = map[string]bool{
	// 协议/传输层与域名碎片(§10.2 扩充:tcp/udp)
	"http": true, "https": true, "tcp": true, "udp": true,
	"www": true, "com": true, "cn": true, "net": true, "org": true,
	// 标记语言/前端资源泛词(§10.2 扩充:spa/editor/js/content)
	"html": true, "htm": true, "script": true, "javascript": true, "text": true,
	"stylesheet": true, "css": true, "image": true, "images": true, "img": true,
	"icon": true, "icons": true, "font": true, "fonts": true, "static": true,
	"asset": true, "assets": true, "media": true, "js": true, "spa": true,
	"editor": true, "content": true,
	// 页面模板/导航/路径泛词(§10.2 扩充:main/public/example/sample/local/temp)
	"index": true, "default": true, "home": true, "homepage": true, "page": true,
	"title": true, "welcome": true, "login": true, "logout": true, "redirect": true,
	"main": true, "public": true, "example": true, "sample": true, "local": true, "temp": true,
	// 泛运维/泛产品词(不代表具体组件指纹;§10.2 扩充:services/enterprise/core/common)
	"server": true, "service": true, "services": true, "web": true, "site": true, "website": true,
	"system": true, "platform": true, "application": true, "app": true, "software": true,
	"admin": true, "management": true, "manager": true, "console": true, "panel": true,
	"dashboard": true, "portal": true, "powered": true, "copyright": true,
	"version": true, "update": true, "online": true, "cloud": true,
	"enterprise": true, "core": true, "common": true,
	// 版本/状态/请求类虚词(§10.2 扩充:v1/v2/old/new/unknown/suspected/test/request/response/proxy)
	"v1": true, "v2": true, "old": true, "new": true, "unknown": true, "suspected": true,
	"test": true, "request": true, "response": true, "proxy": true,
	// 英文虚词(page_title 分词残渣)
	"the": true, "and": true, "for": true, "with": true, "you": true, "your": true,
	"this": true, "that": true, "from": true, "not": true, "can": true, "will": true,
	"have": true, "has": true, "been": true, "more": true, "all": true, "any": true,
	"about": true, "please": true, "sign": true, "our": true, "get": true,
}

// vulnBriefTokenize 把一段指纹文本拆成词:按「字母(含 CJK)/数字」以外的字符切分,
// 不改大小写(小写归一由调用方在去噪判定时做)。
// CJK 连续串(如「通达OA」「用友」)保持为一个词——中文厂商名恰是库内标题的真实指纹。
func vulnBriefTokenize(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// vulnBriefTermAllowed 指纹词放行判定:含至少一个字母(排除纯数字/版本号碎片)、
// 字节长度 ≥3(db.MatchVulnEntriesForFingerprints 同一底线,短词误配率极高)、
// 不在停用词表。小写化由调用方完成。
func vulnBriefTermAllowed(term string) bool {
	if term == "" || len(term) < 3 {
		return false
	}
	if vulnBriefNoiseWords[term] {
		return false
	}
	for _, r := range term {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// vulnBriefFingerprints 从任务资产聚合指纹词(纯函数,单测覆盖):
// technologies 逐个分词 → service_name → page_title,保持信号强度顺序,
// 小写去重、去噪,截 vulnBriefMaxTerms。
func vulnBriefFingerprints(assets []*db.Asset) []string {
	seen := map[string]bool{}
	terms := []string{}
	add := func(raw string) {
		if len(terms) >= vulnBriefMaxTerms {
			return
		}
		for _, tok := range vulnBriefTokenize(raw) {
			term := strings.ToLower(strings.TrimSpace(tok))
			if seen[term] || !vulnBriefTermAllowed(term) {
				continue
			}
			seen[term] = true
			terms = append(terms, term)
			if len(terms) >= vulnBriefMaxTerms {
				return
			}
		}
	}
	for _, a := range assets {
		if a == nil || len(terms) >= vulnBriefMaxTerms {
			continue
		}
		for _, tech := range a.Technologies {
			add(tech)
		}
		add(a.ServiceName)
		add(a.PageTitle)
	}
	return terms
}

// vulnBriefVendorProduct 条目的「厂商 产品」串(双方皆空返回 "")。
func vulnBriefVendorProduct(e *db.VulnEntry) string {
	if e == nil {
		return ""
	}
	parts := []string{}
	for _, p := range []string{strings.TrimSpace(e.Vendor), strings.TrimSpace(e.Product)} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " ")
}

// vulnBriefParseTags 解析条目 tags(JSON 数组);解析失败/为空返回 nil。
func vulnBriefParseTags(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	tags := []string{}
	if err := json.Unmarshal(raw, &tags); err != nil {
		return nil
	}
	return tags
}

// vulnBriefEntryLine 简报单条格式(契约 §4.1):
// `- [KEV][critical] CVE-xxx 标题（厂商 产品）PoC xN`。
func vulnBriefEntryLine(e *db.VulnEntry) string {
	if e == nil {
		return ""
	}
	var marks []string
	if e.KEV {
		marks = append(marks, "KEV")
	}
	if sev := strings.TrimSpace(e.Severity); sev != "" {
		marks = append(marks, sev)
	}
	line := "-"
	if len(marks) > 0 {
		line += " [" + strings.Join(marks, "][") + "]"
	}
	line += " "
	if cve := strings.TrimSpace(e.CVEID); cve != "" {
		line += cve + " "
	}
	line += strings.TrimSpace(e.Title)
	vp := vulnBriefVendorProduct(e)
	// 契约条目格式:标题（厂商 产品）PoC xN——括号后直接跟 PoC 计数;无厂商产品时
	// 用空格隔开标题,避免「标题PoC」粘连。
	if vp != "" {
		line += "（" + vp + "）"
	}
	if e.PocCount > 0 {
		if vp == "" {
			line += " "
		}
		line += fmt.Sprintf("PoC x%d", e.PocCount)
	}
	return line
}

// assembleVulnBriefText 组简报文本(纯函数,单测覆盖):
// 总标题 + KEV 段 + 高危段(critical/high) + 其他段 + 尾注指引。
// 命中为空返回 ""(调用方不注入/不展示)。
func assembleVulnBriefText(fingerprints []string, entries []*db.VulnEntry) string {
	if len(entries) == 0 {
		return ""
	}
	kev := []*db.VulnEntry{}
	high := []*db.VulnEntry{}
	rest := []*db.VulnEntry{}
	for _, e := range entries {
		if e == nil {
			continue
		}
		switch {
		case e.KEV:
			kev = append(kev, e)
		case e.Severity == "critical" || e.Severity == "high":
			high = append(high, e)
		default:
			rest = append(rest, e)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "【CVE 情报简报——按本任务资产指纹匹配（%d 条）】", len(entries))
	if len(fingerprints) > 0 {
		b.WriteString("\n匹配指纹: " + strings.Join(fingerprints, ", "))
	}
	if len(kev) > 0 {
		fmt.Fprintf(&b, "\n\n一、KEV 在野利用（%d 条）——已确认在野,优先核对资产是否在影响范围", len(kev))
		for _, e := range kev {
			b.WriteString("\n" + vulnBriefEntryLine(e))
		}
	}
	if len(high) > 0 {
		fmt.Fprintf(&b, "\n\n二、高危漏洞（critical/high, %d 条）", len(high))
		for _, e := range high {
			b.WriteString("\n" + vulnBriefEntryLine(e))
		}
	}
	if len(rest) > 0 {
		fmt.Fprintf(&b, "\n\n三、其他匹配（%d 条）", len(rest))
		for _, e := range rest {
			b.WriteString("\n" + vulnBriefEntryLine(e))
		}
	}
	b.WriteString("\n\n可用 vulndb_search 工具按指纹/CVE 深查;PoC 仅作验证参考,使用前严格遵守任务约束与授权范围。")
	return strings.TrimSpace(truncateRunesHard(b.String(), vulnBriefTextMaxRunes))
}

// formatVulnSearchResult 检索结果文本(纯函数):首行「漏洞库检索「query」命中 N/M 条」
// + 每条 `CVE编号 | 标题 | severity | KEV? | 厂商 产品 | PoC xN | tags`。
// 空命中输出「未命中」。
func formatVulnSearchResult(query string, items []*db.VulnEntry, total int) string {
	if len(items) == 0 {
		return "未命中"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "漏洞库检索「%s」命中 %d/%d 条", query, len(items), total)
	for _, e := range items {
		if e == nil {
			continue
		}
		fields := []string{}
		if cve := strings.TrimSpace(e.CVEID); cve != "" {
			fields = append(fields, cve)
		}
		fields = append(fields, strings.TrimSpace(e.Title), strings.TrimSpace(e.Severity))
		if e.KEV {
			fields = append(fields, "KEV")
		}
		if vp := vulnBriefVendorProduct(e); vp != "" {
			fields = append(fields, vp)
		}
		if e.PocCount > 0 {
			fields = append(fields, fmt.Sprintf("PoC x%d", e.PocCount))
		}
		if tags := vulnBriefParseTags(e.Tags); len(tags) > 0 {
			fields = append(fields, strings.Join(tags, ","))
		}
		b.WriteString("\n- " + strings.Join(fields, " | "))
	}
	return b.String()
}

// vulnTermHitCounter 单指纹词命中计数:复用冻结的 db.MatchVulnEntriesForFingerprintsPage
// (Terms=单词、Limit=1),其 total 就是「active+poc_count>0+四列 ILIKE 该词」的 COUNT——
// 与简报匹配天然同一 WHERE,零裸 SQL。生产实现注入 assembleVulnFingerprintHits。
func vulnTermHitCounter(pg *db.DB) func(string) (int, error) {
	return func(term string) (int, error) {
		_, total, err := pg.MatchVulnEntriesForFingerprintsPage(
			db.VulnMatchFilter{Terms: []string{term}, Limit: 1})
		return total, err
	}
}

// vulnScopeTotalCounter scope 维度命中计数(kev|high|rest):同一实现带全量指纹词,
// Scope 过滤由 db 层完成,total 即该分类总数。
func vulnScopeTotalCounter(pg *db.DB, terms []string) func(string) (int, error) {
	return func(scope string) (int, error) {
		_, total, err := pg.MatchVulnEntriesForFingerprintsPage(
			db.VulnMatchFilter{Terms: terms, Scope: scope, Limit: 1})
		return total, err
	}
}

// assembleVulnFingerprintHits 对前 vulnBriefFPMaxHits 个指纹词逐词计数(契约 §10.2),
// 按 hits 降序返回(同数保持原信号强度顺序,稳定排序)。countOf 封装「同一匹配 WHERE
// 的单词计数」,生产走 vulnTermHitCounter,测试注入假计数器。
func assembleVulnFingerprintHits(terms []string, countOf func(string) (int, error)) ([]vulnFingerprintHit, error) {
	if len(terms) > vulnBriefFPMaxHits {
		terms = terms[:vulnBriefFPMaxHits]
	}
	hits := make([]vulnFingerprintHit, 0, len(terms))
	for _, term := range terms {
		n, err := countOf(term)
		if err != nil {
			return nil, err
		}
		hits = append(hits, vulnFingerprintHit{Term: term, Hits: n})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Hits > hits[j].Hits })
	return hits, nil
}

// assembleVulnMatchCounts 三次 scope 轻量计数拼 counts(契约 §10.2):kev/high/rest
// 三过滤互斥且完备,各取一次 total 后求和即全量 total——比一次无上限全量拉取再在
// Go 里分组更精确(命中可远超 30)也更省。scopeTotal 生产走 vulnScopeTotalCounter。
func assembleVulnMatchCounts(scopeTotal func(string) (int, error)) (vulnBriefCounts, error) {
	var c vulnBriefCounts
	scopes := []struct {
		name string
		dst  *int
	}{{"kev", &c.KEV}, {"high", &c.High}, {"rest", &c.Rest}}
	for _, sc := range scopes {
		n, err := scopeTotal(sc.name)
		if err != nil {
			return vulnBriefCounts{}, err
		}
		*sc.dst = n
	}
	c.Total = c.KEV + c.High + c.Rest
	return c, nil
}

// vulnFingerprintHit 单个指纹词的库内命中计数(wire 对齐 web/src/lib/types.ts 的
// VulnFingerprintHit;hits=0 时前端灰显,所以 0 命中词也保留在列表里)。
type vulnFingerprintHit struct {
	Term string `json:"term"`
	Hits int    `json:"hits"`
}

// vulnBriefCounts 命中三分类计数(wire 对齐 VulnBriefDTO.counts,契约 §10.2 语义):
// kev=命中中 KEV 数;high=非 KEV 且 critical/high;rest=其余;total=全部。
// db 三 scope 过滤互斥且完备,total 恒等于三者之和。
type vulnBriefCounts struct {
	KEV   int `json:"kev"`
	High  int `json:"high"`
	Rest  int `json:"rest"`
	Total int `json:"total"`
}

// vulnBriefResult 一次简报构建的完整产物:注入文本 + API 展示数据共用。
type vulnBriefResult struct {
	generatedAt  time.Time
	enabled      bool
	fingerprints []string              // 参与匹配的指纹词(信号强度序,注入文本/entries 端点复用)
	fpHits       []vulnFingerprintHit  // 前 24 词的命中计数(按 hits 降序,契约 §10.2)
	counts       vulnBriefCounts       // 三分类计数(简报对话框分类页签)
	entries      []*db.VulnEntry
	text         string
}

// vulnBriefCache 简报 per-task 进程内缓存(sync.Map+时间戳,契约 §4.1)。
var vulnBriefCache sync.Map // map[int64]vulnBriefCacheEntry

type vulnBriefCacheEntry struct {
	at  time.Time
	res *vulnBriefResult
}

// vulnBriefEnabledFor 开关判据(契约 §4.1):任务级 db.Task.VulnBrief 三态优先
// (true/false=强制开/关,nil=跟随全局),回落全局 settings vulndb_task_brief
// (缺省开,与 vulndb_api.go 同判据:只有显式 "false" 才算关)。
func (s *Server) vulnBriefEnabledFor(pt *db.Task) bool {
	if pt != nil && pt.VulnBrief != nil {
		return *pt.VulnBrief
	}
	if s.m == nil || s.m.PG() == nil {
		return true
	}
	if v, _, err := s.m.PG().GetSetting(settingVulnTaskBrief); err == nil && v == "false" {
		return false
	}
	return true
}

// vulnBriefingFor 构建并缓存任务简报(简报注入与 API 共用同一份产物)。
// 任务不存在返回 ErrTaskGone;瞬时库错误原样上抛且【不缓存】(下次调用自动重试);
// 成功结果(含「联动关闭=空简报」与「零命中」)缓存 vulnBriefCacheTTL。
func (s *Server) vulnBriefingFor(taskID int64) (*vulnBriefResult, error) {
	if taskID <= 0 {
		return nil, ErrTaskGone
	}
	if e, ok := vulnBriefCache.Load(taskID); ok {
		if ce, ok := e.(vulnBriefCacheEntry); ok && ce.at.Add(vulnBriefCacheTTL).After(time.Now()) {
			return ce.res, nil
		}
	}
	if s.m == nil || s.m.PG() == nil {
		return &vulnBriefResult{generatedAt: time.Now(), fingerprints: []string{},
			fpHits: []vulnFingerprintHit{}, entries: []*db.VulnEntry{}}, nil
	}
	pt, err := s.m.PG().GetTask(taskID)
	if err != nil {
		return nil, err
	}
	if pt == nil {
		return nil, ErrTaskGone
	}
	res := &vulnBriefResult{
		generatedAt:  time.Now(),
		enabled:      s.vulnBriefEnabledFor(pt),
		fingerprints: []string{},
		fpHits:       []vulnFingerprintHit{},
		entries:      []*db.VulnEntry{},
	}
	if res.enabled {
		if as := s.assetStore(); as != nil {
			assets, aerr := as.QueryByTask(taskID, "", vulnBriefMaxAssets, 0)
			if aerr != nil {
				return nil, aerr
			}
			res.fingerprints = vulnBriefFingerprints(assets)
			if len(res.fingerprints) > 0 {
				pg := s.m.PG()
				// 指纹带命中计数(契约 §10.2):前 24 词逐词 COUNT,失败与匹配同权上抛
				// (不缓存,下次调用自动重试)。
				hits, herr := assembleVulnFingerprintHits(res.fingerprints, vulnTermHitCounter(pg))
				if herr != nil {
					return nil, herr
				}
				res.fpHits = hits
				entries, merr := pg.MatchVulnEntriesForFingerprints(res.fingerprints, vulnBriefMatchLimit)
				if merr != nil {
					return nil, merr
				}
				res.entries = entries
				res.text = assembleVulnBriefText(res.fingerprints, entries)
				counts, cerr := assembleVulnMatchCounts(vulnScopeTotalCounter(pg, res.fingerprints))
				if cerr != nil {
					return nil, cerr
				}
				res.counts = counts
			}
		}
	}
	vulnBriefCache.Store(taskID, vulnBriefCacheEntry{at: res.generatedAt, res: res})
	return res, nil
}

// vulnBriefBlock 返回本任务的 CVE 情报简报文本(空串=不注入)。
// 一切失败(非法 id/任务不存在/库错误/零命中/联动关闭)都返回 "",不打断任务链路。
func (s *Server) vulnBriefBlock(taskID string) string {
	id, err := strconv.ParseInt(strings.TrimSpace(taskID), 10, 64)
	if err != nil || id <= 0 {
		return ""
	}
	res, err := s.vulnBriefingFor(id)
	if err != nil {
		if !errors.Is(err, ErrTaskGone) {
			log.Printf("[vuln-brief] 任务 %s 简报构建失败(降级为不注入): %v", taskID, err)
		}
		return ""
	}
	return res.text
}

// vulndbSearchFor 是 vulndb_search 工具的服务端检索实现,返回格式化文本。
// ctx 预留给下游超时传递(db 层当前不接受 ctx,保留参数位保证接线签名稳定)。
func (s *Server) vulndbSearchFor(_ context.Context, query, severity, kev, hasPoc string, limit int) (string, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return "", errors.New("query 必填")
	}
	if s.m == nil || s.m.PG() == nil {
		return "", errors.New("漏洞库查询不可用")
	}
	// 严重度/KEV/PoC 三态参数白名单化:乱传值按「不限」处理,不报错。
	switch severity {
	case "critical", "high", "medium", "low":
	default:
		severity = ""
	}
	if kev != "0" && kev != "1" {
		kev = ""
	}
	if hasPoc != "0" && hasPoc != "1" {
		hasPoc = ""
	}
	// 上限夹取 1-30(契约 §4.2);分类固定全库四类(排除未来可能新增的非漏洞分类)。
	limit = max(1, min(limit, 30))
	items, total, _, err := s.m.PG().ListVulnEntries(db.VulnEntryFilter{
		Q:          q,
		Categories: []string{db.VulnCategoryZeroDay, db.VulnCategoryNDay, db.VulnCategoryCVE, db.VulnCategoryCNVD},
		Severity:   severity,
		Kev:        kev,
		HasPoc:     hasPoc,
		Limit:      limit,
	})
	if err != nil {
		return "", err
	}
	return formatVulnSearchResult(q, items, total), nil
}
