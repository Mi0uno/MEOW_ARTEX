package server

// 渗透成果自动入漏洞库:搜索取证 + AI 分析,判定 finding 是 0day / nday / 已知
// CVE,并提取厂商/产品信息,自动归类入库。
//
// 触发点:report_finding 登记漏洞后(与报告自动归档同一处钩子)。
// 判定逻辑:
//  1. 确定性:finding 证据里出现 CVE-YYYY-NNNN 编号 → 直接归 cve(不调 LLM);
//     厂商用 NVD 按 CVE 反查 CPE 得到(失败留空)。
//  2. 搜索取证 + AI 综合判定:先从本地漏洞库/NVD/GitHub/网页搜索四源收集
//     「该漏洞是否已被公开披露」的证据(见 vulndb_classify_search.go),连同
//     finding 与确定性线索(官方补丁等 nday 提示词)一起交给 LLM 判
//     0day/nday/cve + 置信度 + 厂商/产品/受影响版本。
//     AI 不可用或判定失败 → 默认归 nday(保守:不轻易宣称 0day)。
//
// 证据与结论都落库:相关链接并入 refs,「归类依据」段附在描述尾部,便于人工复核。
// 入库后回填 SRC 成果的 entry_id,让成果页能跳到漏洞库条目。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
)

// cvePattern 匹配 CVE 编号(大小写不敏感)。
var cvePattern = regexp.MustCompile(`(?i)\bCVE-\d{4}-\d{4,7}\b`)

// ndayHints 是「已公开漏洞」的确定性线索(命中即作为强偏向喂给 AI,并在
// AI 失败时兜底归 nday)。
var ndayHints = []string{
	"官方补丁", "已修复", "补丁版本", "升级到", "厂商公告", "安全公告", "advisory",
	"已知漏洞", "公开披露", "披露于", "cnvd-", "cnnvd-", "ghsa-", "poc 已公开", "已有 poc",
}

// classifyResult 是一次归类判定的完整产出。
type classifyResult struct {
	Category   string // 0day | nday | cve
	CVEID      string
	Vendor     string
	Product    string
	Affected   string
	Confidence string // high | medium | low
	Reason     string
	Evidence   []classifyEvidence
	// AIVerdict 为 false 表示走了确定性/兜底路径(没有 AI 参与判定)。
	AIVerdict bool
}

// matchNDayHint 返回第一条命中的「已公开」线索;无命中返回空串。
func matchNDayHint(hay string) string {
	lower := strings.ToLower(hay)
	for _, hint := range ndayHints {
		if strings.Contains(lower, hint) {
			return hint
		}
	}
	return ""
}

// lockFinding 对同一 finding 的自动归类做进程内互斥:report_finding 登记钩子与
// update_finding_report 都会异步入库,而幂等守卫(FindVulnEntryByFinding)是
// 「先查后插」——归类要跑搜索取证+AI(分钟级),两次触发落在竞态窗口内会双双
// 通过守卫,同一 finding 入库两条。锁住整个归类过程,后到者等前者落库后复查守卫
// 自然跳过。返回解锁函数。
func (s *Server) lockFinding(findingID int64) func() {
	s.classifyMu.Lock()
	defer s.classifyMu.Unlock()
	if s.classifyLocks == nil {
		s.classifyLocks = map[int64]*sync.Mutex{}
	}
	mu, ok := s.classifyLocks[findingID]
	if !ok {
		mu = &sync.Mutex{}
		s.classifyLocks[findingID] = mu
	}
	mu.Lock()
	return mu.Unlock
}

// AutoClassifyFinding 把一个 finding 自动归类入漏洞库。
// ctx 用于 HTTP 同步调用(reclassify)时跟随请求取消;异步调用传 context.Background()。
func (s *Server) autoClassifyFinding(ctx context.Context, findingID int64) {
	if findingID <= 0 {
		return
	}
	f, err := s.m.PG().GetFinding(findingID)
	if err != nil || f == nil {
		return
	}
	// 已有条目关联则跳过(幂等:同一 finding 只入库一次)。
	if e, err := s.m.PG().FindVulnEntryByFinding(findingID); err == nil && e != nil {
		return
	}
	unlock := s.lockFinding(findingID)
	defer unlock()
	// 锁内复查:等锁期间并发的第一次归类可能已经落库。
	if e, err := s.m.PG().FindVulnEntryByFinding(findingID); err == nil && e != nil {
		return
	}

	res := s.classifyFinding(ctx, f)
	title := strings.TrimSpace(f.Name)
	if title == "" {
		title = strings.TrimSpace(f.VulnClass)
	}
	if title == "" {
		title = fmt.Sprintf("发现 #%d", f.ID)
	}
	// 描述用 summary;利用指引用 report(若已有详细报告)。
	desc := strings.TrimSpace(f.Summary)
	if desc == "" {
		desc = truncateRunes(strings.TrimSpace(f.Evidence), 2000)
	}
	desc = appendClassifyBasis(desc, res)

	// refs:NVD 链接 + 证据 URL(去重,限量)。必须初始化为空 slice——
	// nil 序列化成 JSON null,前端 .filter/.length 会崩。
	refs := []string{}
	if res.CVEID != "" {
		refs = append(refs, fmt.Sprintf("https://nvd.nist.gov/vuln/detail/%s", res.CVEID))
	}
	for _, ev := range res.Evidence {
		if ev.URL == "" {
			continue
		}
		dup := false
		for _, have := range refs {
			if have == ev.URL {
				dup = true
				break
			}
		}
		if !dup && len(refs) < 8 {
			refs = append(refs, ev.URL)
		}
	}
	tags := []string{"实测成果"}
	if len(res.Evidence) > 0 {
		tags = append(tags, "AI+搜索")
	}
	if res.Category == db.VulnCategoryZeroDay {
		switch res.Confidence {
		case "high":
			tags = append(tags, "置信high")
		case "low":
			// 低置信 0day 必须标出来,引导人工复核。
			tags = append(tags, "待复核")
		}
	}
	entry, err := s.m.PG().CreateVulnEntry(db.VulnEntryInput{
		Category:     res.Category,
		Title:        title,
		CVEID:        res.CVEID,
		Vendor:       res.Vendor,
		Product:      res.Product,
		Affected:     res.Affected,
		Severity:     strings.ToLower(strings.TrimSpace(f.Severity)),
		VulnClass:    strings.TrimSpace(f.VulnClass),
		Description:  desc,
		Exploitation: strings.TrimSpace(f.Report),
		Source:       "ai_finding",
		TaskID:       f.TaskID,
		FindingID:    &findingID,
		Refs:         vulnJSON(refs),
		Tags:         vulnJSON(tags),
		Status:       "active",
	})
	if err != nil {
		// finding_id 唯一索引冲突(idx_vulndb_finding_unique,跨实例/残余竞态兜底):
		// 已有条目占了该 finding → 改关联到它,不报错。
		if existing, e2 := s.m.PG().FindVulnEntryByFinding(findingID); e2 == nil && existing != nil {
			s.linkFindingToEntry(findingID, existing.ID, f.TaskID)
			log.Printf("[vulndb] 发现 %d 命中已有条目 #%d(finding 唯一索引兜底)", findingID, existing.ID)
			return
		}
		// CVE 唯一索引冲突 = 该 CVE 已在库中(定时搜集先入了):改为关联到已有条目。
		if res.CVEID != "" {
			if existing, e2 := s.m.PG().FindVulnEntryByCVE(res.CVEID); e2 == nil && existing != nil {
				s.linkFindingToEntry(findingID, existing.ID, f.TaskID)
				log.Printf("[vulndb] 发现 %d 命中已有 CVE 条目 #%d(%s)", findingID, existing.ID, res.CVEID)
				return
			}
		}
		log.Printf("[vulndb] 发现 %d 自动入库失败: %v", findingID, err)
		return
	}
	s.linkFindingToEntry(findingID, entry.ID, f.TaskID)
	log.Printf("[vulndb] 发现 %d 自动入库 #%d 分类=%s cve=%s 厂商=%q 产品=%q 置信=%s 证据=%d%s",
		findingID, entry.ID, res.Category, res.CVEID, res.Vendor, res.Product, res.Confidence, len(res.Evidence), res.reasonNote())
}

// reasonNote 供日志用的判定依据后缀。
func (r classifyResult) reasonNote() string {
	if r.Reason == "" {
		return ""
	}
	return " 依据:" + truncateRunes(r.Reason, 80)
}

// appendClassifyBasis 把判定结论与证据清单以 markdown 追加到描述尾部,
// 条目详情页可直接人工复核判定是否靠谱。
func appendClassifyBasis(desc string, res classifyResult) string {
	if res.Reason == "" && len(res.Evidence) == 0 {
		return desc
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(desc, "\n"))
	// 描述在详情页按纯文本渲染(whitespace-pre-wrap),不用 markdown 语法。
	b.WriteString("\n\n—— 归类依据(" + time.Now().Format("2006-01-02") + " 自动搜索取证) ——\n")
	verdict := "AI 综合判定"
	if !res.AIVerdict {
		verdict = "确定性判定"
	}
	line := fmt.Sprintf("判定:%s(%s", res.Category, verdict)
	if res.Confidence != "" {
		line += ",置信度 " + res.Confidence
	}
	if res.Reason != "" {
		line += "):" + res.Reason
	} else {
		line += ")"
	}
	b.WriteString(line + "\n")
	if res.Vendor != "" || res.Product != "" {
		b.WriteString("厂商/产品:" + strings.TrimSpace(res.Vendor+" / "+res.Product))
		if res.Affected != "" {
			b.WriteString("(影响 " + res.Affected + ")")
		}
		b.WriteString("\n")
	}
	for i, ev := range res.Evidence {
		if i >= 6 {
			b.WriteString(fmt.Sprintf("…另有 %d 条证据略\n", len(res.Evidence)-6))
			break
		}
		b.WriteString(fmt.Sprintf("[E%d] [%s] %s", i+1, ev.Source, ev.Title))
		if ev.URL != "" {
			b.WriteString(" " + ev.URL)
		}
		if ev.Snippet != "" {
			b.WriteString(" — " + truncateRunes(ev.Snippet, 120))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// linkFindingToEntry 把漏洞库条目回填到 SRC 成果(若该 finding 有成果记录)。
func (s *Server) linkFindingToEntry(findingID, entryID int64, taskID *int64) {
	if taskID == nil {
		return
	}
	if err := s.m.PG().SetSRCRewardEntryByFinding(findingID, entryID); err != nil {
		log.Printf("[vulndb] 回填成果 entry_id 失败(发现 %d): %v", findingID, err)
	}
}

// classifyFinding 判定分类:先确定性 CVE,再搜索取证 + AI,最后保守默认。
func (s *Server) classifyFinding(ctx context.Context, f *db.DBFinding) classifyResult {
	hay := strings.Join([]string{f.Name, f.VulnClass, f.Summary, f.Evidence, f.Report}, "\n")

	// ① 确定性:出现 CVE 编号 → cve(不调 LLM);厂商用 NVD CPE 反查。
	if m := cvePattern.FindString(hay); m != "" {
		res := classifyResult{
			Category:   db.VulnCategoryCVE,
			CVEID:      strings.ToUpper(m),
			Confidence: "high",
			Reason:     "证据中含 CVE 编号 " + strings.ToUpper(m),
		}
		lctx, cancel := context.WithTimeout(ctx, classifySearchTimeout)
		defer cancel()
		res.Vendor, res.Product, res.Affected = nvdLookupCVE(lctx, res.CVEID)
		return res
	}

	// ② 搜索取证 + AI 综合判定(nday 线索作为强偏向一并给出)。
	hint := matchNDayHint(hay)
	evidence := s.gatherClassifyEvidence(ctx, f)
	if res := s.aiClassifyFinding(ctx, f, hay, evidence, hint); res != nil {
		return *res
	}

	// ③ 兜底:AI 不可用/失败,命中已公开线索或保守默认,均归 nday。
	// 搜集到的证据必须带上——否则 LLM 缺位的实例(演示库/断网)白搜一场,
	// 条目上不留任何取证痕迹,人工没法复核。
	res := classifyResult{Category: db.VulnCategoryNDay, Confidence: "low", Evidence: evidence}
	switch {
	case hint != "":
		res.Reason = "证据含已公开线索:" + hint
	case len(evidence) > 0:
		res.Reason = fmt.Sprintf("检索到 %d 条公开情报,AI 不可用未综合判定,保守归 nday", len(evidence))
	default:
		res.Reason = "无确定线索,保守归 nday"
	}
	return res
}

// aiClassifyFinding 搜索取证后让 LLM 综合判定分类,并提取厂商/产品。
// 返回 nil 表示 AI 不可用/失败(调用方走保守兜底)。
func (s *Server) aiClassifyFinding(ctx context.Context, f *db.DBFinding, hay string, evidence []classifyEvidence, hint string) *classifyResult {
	profileID := int64(0)
	if p, err := s.m.pg.ActiveProfile(); err == nil && p != nil {
		profileID = p.ID
	}
	if profileID == 0 {
		return nil
	}
	prov, _, ok := s.providerForProfile(profileID)
	if !ok {
		return nil
	}

	system := "你是漏洞情报分析师。基于公开情报检索证据,判定漏洞分类,并提取厂商/产品信息。\n" +
		"分类定义:\n" +
		"- \"cve\":已确认存在对应 CVE 编号的公开漏洞(cve_id 必须给出合法 CVE-YYYY-NNNN)\n" +
		"- \"nday\":已公开的已知漏洞(检索到官方补丁/厂商公告/公开 PoC/安全文章等公开信息)\n" +
		"- \"0day\":多源检索均未发现该漏洞的任何公开信息,且利用手法为自主构造而非通用扫描器指纹\n" +
		"判定要点:\n" +
		"- 证据按 [E1][E2] 编号给出,reason 里引用编号(如「[E2] 命中同组件 CVE」)\n" +
		"- 组件名+漏洞类型+版本与检索到的 CVE/PoC 描述高度吻合 → nday 或 cve\n" +
		"- 证据为空或全是无关结果 ≠ 0day:证据不足时选 nday,不得随意宣称 0day\n" +
		"vendor/product 用官方名称(中文厂商保留中文如 用友/畅捷通,英文厂商用英文名);affected 填受影响版本范围,未知留空。\n" +
		"只输出 JSON,不要代码围栏:{\"category\":\"cve|nday|0day\",\"cve_id\":\"\",\"confidence\":\"high|medium|low\",\"reason\":\"\",\"vendor\":\"\",\"product\":\"\",\"affected\":\"\"}"

	var b strings.Builder
	fmt.Fprintf(&b, "漏洞名称: %s\n类别: %s\n严重度: %s\n摘要: %s\n证据片段:\n%s",
		f.Name, f.VulnClass, f.Severity, truncateRunes(f.Summary, 800), truncateRunes(hay, 3000))
	if hint != "" {
		fmt.Fprintf(&b, "\n\n已知确定线索:漏洞文本命中「%s」,强烈倾向 nday。", hint)
	}
	b.WriteString("\n\n公开情报检索结果:")
	if len(evidence) == 0 {
		b.WriteString("\n(搜索未启用或未检索到相关结果——注意:这不构成 0day 的充分证据)")
	}
	for i, ev := range evidence {
		fmt.Fprintf(&b, "\n[E%d] (%s) %s", i+1, ev.Source, ev.Title)
		if ev.URL != "" {
			fmt.Fprintf(&b, " %s", ev.URL)
		}
		if ev.Snippet != "" {
			fmt.Fprintf(&b, "\n    %s", ev.Snippet)
		}
	}

	jctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := analyzeCompletion(jctx, prov, system, b.String())
	if err != nil {
		log.Printf("[vulndb] AI 分类失败: %v", err)
		return nil
	}
	out = strings.TrimSpace(out)
	out = strings.TrimPrefix(out, "```json")
	out = strings.TrimPrefix(out, "```")
	out = strings.TrimSuffix(strings.TrimSpace(out), "```")
	var parsed struct {
		Category   string `json:"category"`
		CVEID      string `json:"cve_id"`
		Confidence string `json:"confidence"`
		Reason     string `json:"reason"`
		Vendor     string `json:"vendor"`
		Product    string `json:"product"`
		Affected   string `json:"affected"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &parsed); err != nil {
		log.Printf("[vulndb] AI 分类返回无法解析: %v", err)
		return nil
	}
	cat := strings.ToLower(strings.TrimSpace(parsed.Category))
	if !db.ValidVulnCategory(cat) {
		return nil
	}
	cve := strings.ToUpper(strings.TrimSpace(parsed.CVEID))
	if cat == db.VulnCategoryCVE && !cvePattern.MatchString(cve) {
		// AI 说 cve 但没给合法编号:降为 nday,避免污染 CVE 唯一键。
		cat, cve = db.VulnCategoryNDay, ""
	}
	if cat != db.VulnCategoryCVE {
		cve = ""
	}
	conf := strings.ToLower(strings.TrimSpace(parsed.Confidence))
	switch conf {
	case "high", "medium", "low":
	default:
		conf = "medium"
	}
	// 厂商:AI 提取 + NVD CPE 证据修正(NVD 的官方写法优先级更高,但只在
	// AI 留空或证据里有 CPE 时才覆盖,避免把国产软件填成英文 CPE 厂商)。
	// 这里简化:AI 给了就用 AI 的;AI 没给且证据里有 NVD CPE,从证据里解析。
	vendor, product, affected := strings.TrimSpace(parsed.Vendor), strings.TrimSpace(parsed.Product), strings.TrimSpace(parsed.Affected)
	if vendor == "" || product == "" {
		for _, ev := range evidence {
			if ev.Source != "NVD" {
				continue
			}
			if m := regexp.MustCompile(`CPE: (cpe:[^)\s]+)`).FindStringSubmatch(ev.Snippet); m != nil {
				cv, cp, ca := cpeVendorProduct(m[1])
				if vendor == "" {
					vendor = cv
				}
				if product == "" {
					product = cp
				}
				if affected == "" {
					affected = ca
				}
				break
			}
		}
	}
	if db.VulnCategoryZeroDay == cat && hint != "" {
		// 文本里明确出现「官方补丁/公开披露」等线索还判 0day:压回 nday。
		cat = db.VulnCategoryNDay
	}
	return &classifyResult{
		Category:   cat,
		CVEID:      cve,
		Vendor:     vendor,
		Product:    product,
		Affected:   affected,
		Confidence: conf,
		Reason:     strings.TrimSpace(parsed.Reason),
		Evidence:   evidence,
		AIVerdict:  true,
	}
}
