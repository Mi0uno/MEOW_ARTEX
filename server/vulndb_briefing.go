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
)

// vulnBriefNoiseWords 指纹去噪停用词表。只停「协议/域名碎片、标记语言、页面模板
// 与泛运维词」——nginx/apache/shiro/tomcat 这类真指纹绝不停用(契约 §4.1 明示)。
// 判定在小写化后进行;表外的词一律放行,宁可多匹配不可漏指纹。
var vulnBriefNoiseWords = map[string]bool{
	// 协议/域名碎片
	"http": true, "https": true, "www": true, "com": true, "cn": true, "net": true, "org": true,
	// 标记语言/前端资源泛词
	"html": true, "htm": true, "script": true, "javascript": true, "text": true,
	"stylesheet": true, "css": true, "image": true, "images": true, "img": true,
	"icon": true, "icons": true, "font": true, "fonts": true, "static": true,
	"asset": true, "assets": true, "media": true,
	// 页面模板泛词
	"index": true, "default": true, "home": true, "homepage": true, "page": true,
	"title": true, "welcome": true, "login": true, "logout": true, "redirect": true,
	// 泛运维/泛产品词(不代表具体组件指纹)
	"server": true, "service": true, "web": true, "site": true, "website": true,
	"system": true, "platform": true, "application": true, "app": true, "software": true,
	"admin": true, "management": true, "manager": true, "console": true, "panel": true,
	"dashboard": true, "portal": true, "powered": true, "copyright": true,
	"version": true, "update": true, "online": true, "cloud": true,
	// 英文虚词(page_title 分词残渣)
	"the": true, "and": true, "for": true, "with": true, "you": true, "your": true,
	"this": true, "that": true, "from": true, "not": true, "can": true, "will": true,
	"have": true, "has": true, "been": true, "more": true, "all": true, "any": true,
	"about": true, "please": true, "sign": true, "our": true, "new": true, "get": true,
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

// vulnBriefResult 一次简报构建的完整产物:注入文本 + API 展示数据共用。
type vulnBriefResult struct {
	generatedAt  time.Time
	enabled      bool
	fingerprints []string
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
		return &vulnBriefResult{generatedAt: time.Now(), fingerprints: []string{}, entries: []*db.VulnEntry{}}, nil
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
				entries, merr := s.m.PG().MatchVulnEntriesForFingerprints(res.fingerprints, vulnBriefMatchLimit)
				if merr != nil {
					return nil, merr
				}
				res.entries = entries
				res.text = assembleVulnBriefText(res.fingerprints, entries)
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
