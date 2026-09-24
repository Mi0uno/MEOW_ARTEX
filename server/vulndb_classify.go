package server

// 渗透成果自动入漏洞库:AI 分析识别 finding 是 0day / nday / 已知 CVE,自动归类入库。
//
// 触发点:report_finding 登记漏洞后(与报告自动归档同一处钩子)。
// 判定逻辑(两级,先确定性后 AI):
//  1. 确定性线索:finding 的 evidence/summary 里出现 CVE-YYYY-NNNN 编号 → cve;
//     命中本地漏洞库已有条目 → 直接关联;出现"已公开/官方补丁/版本号"等线索 → nday。
//  2. AI 判定:无确定线索时交给 LLM 按语义判 0day/nday/cve(输出 JSON)。
//     AI 不可用或判定失败 → 默认归 nday(保守:不轻易宣称 0day)。
//
// 入库后回填 SRC 成果的 entry_id,让成果页能跳到漏洞库条目。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

// cvePattern 匹配 CVE 编号(大小写不敏感)。
var cvePattern = regexp.MustCompile(`(?i)\bCVE-\d{4}-\d{4,7}\b`)

// ndayHints 是「已公开漏洞」的确定性线索(命中即倾向 nday 而非 0day)。
var ndayHints = []string{
	"官方补丁", "已修复", "补丁版本", "升级到", "厂商公告", "安全公告", "advisory",
	"已知漏洞", "公开披露", "披露于", "cnvd-", "cnnvd-", "ghsa-", "poc 已公开", "已有 poc",
}

// AutoClassifyFinding 把一个 finding 自动归类入漏洞库。
// 返回入库条目 id 与分类(0=未入库)。异步调用,失败只记日志。
func (s *Server) autoClassifyFinding(findingID int64) {
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

	category, cveID, aiNote := s.classifyFindingCategory(f)
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
	exploit := strings.TrimSpace(f.Report)
	refs := json.RawMessage("[]")
	if cveID != "" {
		refs = vulnJSON([]string{fmt.Sprintf("https://nvd.nist.gov/vuln/detail/%s", cveID)})
	}
	entry, err := s.m.PG().CreateVulnEntry(db.VulnEntryInput{
		Category:     category,
		Title:        title,
		CVEID:        cveID,
		Severity:     strings.ToLower(strings.TrimSpace(f.Severity)),
		VulnClass:    strings.TrimSpace(f.VulnClass),
		Description:  desc,
		Exploitation: exploit,
		Source:       "ai_finding",
		TaskID:       f.TaskID,
		FindingID:    &findingID,
		Refs:         refs,
		Tags:         vulnJSON([]string{"实测成果"}),
		Status:       "active",
	})
	if err != nil {
		// CVE 唯一索引冲突 = 该 CVE 已在库中(定时搜集先入了):改为关联到已有条目。
		if cveID != "" {
			if existing, e2 := s.m.PG().FindVulnEntryByCVE(cveID); e2 == nil && existing != nil {
				s.linkFindingToEntry(findingID, existing.ID, f.TaskID)
				log.Printf("[vulndb] 发现 %d 命中已有 CVE 条目 #%d(%s)", findingID, existing.ID, cveID)
				return
			}
		}
		log.Printf("[vulndb] 发现 %d 自动入库失败: %v", findingID, err)
		return
	}
	s.linkFindingToEntry(findingID, entry.ID, f.TaskID)
	log.Printf("[vulndb] 发现 %d 自动入库 #%d 分类=%s cve=%s%s", findingID, entry.ID, category, cveID, aiNote)
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

// classifyFindingCategory 判定分类:先确定性线索,再 AI,最后保守默认。
func (s *Server) classifyFindingCategory(f *db.DBFinding) (category, cveID, aiNote string) {
	hay := strings.Join([]string{f.Name, f.VulnClass, f.Summary, f.Evidence, f.Report}, "\n")

	// ① 确定性:出现 CVE 编号 → cve。
	if m := cvePattern.FindString(hay); m != "" {
		return db.VulnCategoryCVE, strings.ToUpper(m), "(证据中含 CVE 编号)"
	}
	// ② 确定性:命中已公开漏洞线索 → nday。
	lower := strings.ToLower(hay)
	for _, hint := range ndayHints {
		if strings.Contains(lower, hint) {
			return db.VulnCategoryNDay, "", "(证据含已公开线索:" + hint + ")"
		}
	}
	// ③ AI 判定(无确定线索时)。
	if cat, note := s.aiClassifyFinding(f, hay); cat != "" {
		return cat, "", note
	}
	// ④ 保守默认:不轻易宣称 0day。
	return db.VulnCategoryNDay, "", "(无确定线索,保守归 nday)"
}

// aiClassifyFinding 让 LLM 判定分类。返回空串表示 AI 不可用/失败。
func (s *Server) aiClassifyFinding(f *db.DBFinding, hay string) (string, string) {
	profileID := int64(0)
	if p, err := s.m.pg.ActiveProfile(); err == nil && p != nil {
		profileID = p.ID
	}
	if profileID == 0 {
		return "", ""
	}
	prov, _, ok := s.providerForProfile(profileID)
	if !ok {
		return "", ""
	}

	system := "你是漏洞情报分析师。判断给定漏洞属于哪一类:\n" +
		"- \"cve\":有正式 CVE 编号的已公开漏洞(证据里能看到 CVE-YYYY-NNNN)\n" +
		"- \"nday\":已公开的已知漏洞(有官方补丁/厂商公告/公开 PoC),但证据里没有 CVE 编号\n" +
		"- \"0day\":未公开、无补丁、自研发现的漏洞(无任何公开信息线索,且利用手法是自主构造)\n" +
		"只输出 JSON:{\"category\":\"cve|nday|0day\",\"cve_id\":\"CVE-xxxx-xxxx 或空\",\"reason\":\"一句话依据\"}。" +
		"不要代码块围栏、不要解释。判断不确定时选 nday(不得随意宣称 0day)。"

	user := fmt.Sprintf("漏洞名称: %s\n类别: %s\n严重度: %s\n摘要: %s\n证据片段:\n%s",
		f.Name, f.VulnClass, f.Severity, truncateRunes(f.Summary, 800), truncateRunes(hay, 3000))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := analyzeCompletion(ctx, prov, system, user)
	if err != nil {
		log.Printf("[vulndb] AI 分类失败: %v", err)
		return "", ""
	}
	out = strings.TrimSpace(out)
	out = strings.TrimPrefix(out, "```json")
	out = strings.TrimPrefix(out, "```")
	out = strings.TrimSuffix(strings.TrimSpace(out), "```")
	var parsed struct {
		Category string `json:"category"`
		CVEID    string `json:"cve_id"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &parsed); err != nil {
		log.Printf("[vulndb] AI 分类返回无法解析: %v", err)
		return "", ""
	}
	cat := strings.ToLower(strings.TrimSpace(parsed.Category))
	if !db.ValidVulnCategory(cat) {
		return "", ""
	}
	cve := strings.ToUpper(strings.TrimSpace(parsed.CVEID))
	if cat == db.VulnCategoryCVE && !cvePattern.MatchString(cve) {
		// AI 说 cve 但没给合法编号:降为 nday,避免污染 CVE 唯一键。
		cat, cve = db.VulnCategoryNDay, ""
	}
	if cat != db.VulnCategoryCVE {
		cve = ""
	}
	note := "(AI 判定"
	if parsed.Reason != "" {
		note += ":" + truncateRunes(parsed.Reason, 60)
	}
	note += ")"
	return cat, note
}
