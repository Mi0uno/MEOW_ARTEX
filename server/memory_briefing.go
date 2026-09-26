package server

// 记忆简报 + recall(契约 AI_MEMORY_CONTRACTS.md §6):
//
//	简报 = 确定性拼装(不打 LLM):scope 解析(与复盘同一回退链)→ 七段固定结构
//	(已知资产/历史发现/记忆事实/最近复盘/经验洞察/可用剧本/防重复指引)→ 每条 300 字、
//	整体 8000 字 → 进程内 60s 缓存。
//	planner/worker/mainagent 系统提示词注入 memoryBriefBlock;API 用 sections 拆分。
//	recall = memory_recall 工具的服务端实现:facts/runs/insights/playbooks 合并检索,
//	每行带来源前缀(溯源是防投毒的一部分,铁律 9)。

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
)

// memoryStaleDays 资产/事实「距上次验证超 N 天」标 <待复核>(settings memory_stale_days 可覆盖)。
const memoryStaleDays = 14

const (
	settingMemoryStaleDays = "memory_stale_days"

	memoryBriefCacheTTL = 60 * time.Second
	memoryItemMaxRunes  = 300  // 简报每条内容上限
	memoryTextMaxRunes  = 8000 // 简报整体上限

	// memoryAntiRepeat 是「七、防重复指引」的固定文案(契约 §6 逐字,不得改写)。
	memoryAntiRepeat = "上述已知资产已在前序任务中枚举/验证过。除非标注 <待复核> 或信息早于失效期，不要对它们重复子域枚举、端口扫描或指纹识别，直接复用结论派发下一步意图；把预算留给未知面与已发现的可利用点。"
)

// memorySection 是简报的结构化一段(API sections 与注入文本共用)。
type memorySection struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// memoryBriefCache 是简报的进程内 60s 缓存(契约 §6:simple map+mutex 即可)。
type memoryBriefCache struct {
	mu   sync.Mutex
	rows map[int64]memoryBriefEntry
}

type memoryBriefEntry struct {
	at       time.Time
	sections []memorySection
	text     string
	ok       bool
}

var memoryBrief = &memoryBriefCache{rows: map[int64]memoryBriefEntry{}}

// memoryScopesForTaskID 解析任务的记忆 scope(契约 §5.4/§6 回退链):
// 运行时任务的 CompanyIDs/SRCTargetID → 库任务行 → 回退查任务资产 distinct company_id。
// 不含 global(global 由 db Active* 系列隐式并入)。恒非 nil。
func (s *Server) memoryScopesForTaskID(taskID int64) []db.MemoryScope {
	scopes := []db.MemoryScope{}
	if taskID <= 0 {
		return scopes
	}
	companyIDs := []int64{}
	var srcTarget int64
	if rt, ok := s.m.Task(strconv.FormatInt(taskID, 10)); ok && rt != nil {
		companyIDs = append(companyIDs, rt.CompanyIDs...)
		srcTarget = rt.SRCTargetID
	} else if pt, err := s.m.PG().GetTask(taskID); err == nil && pt != nil {
		companyIDs = append(companyIDs, pt.CompanyIDs...)
		srcTarget = pt.SRCTargetID
	}
	if len(companyIDs) == 0 {
		// 回退链:任务资产的 distinct company_id(截 500 个资产足够覆盖归属)。
		if as := s.assetStore(); as != nil {
			if assets, err := as.QueryByTask(taskID, "", 500, 0); err == nil {
				seen := map[int64]bool{}
				for _, a := range assets {
					if a == nil || a.CompanyID == nil || *a.CompanyID <= 0 || seen[*a.CompanyID] {
						continue
					}
					seen[*a.CompanyID] = true
					companyIDs = append(companyIDs, *a.CompanyID)
				}
			} else {
				log.Printf("[memory] 任务 %d 按资产回退企业归属失败: %v", taskID, err)
			}
		}
	}
	seen := map[int64]bool{}
	for _, cid := range companyIDs {
		if cid > 0 && !seen[cid] {
			seen[cid] = true
			scopes = append(scopes, db.MemoryScope{Kind: "company", ID: cid})
		}
	}
	if srcTarget > 0 {
		scopes = append(scopes, db.MemoryScope{Kind: "src_target", ID: srcTarget})
	}
	return scopes
}

// taskMemoryScopes 运行时任务句柄版(契约 §7 接线语义;与 memoryScopesForTaskID 同链)。
func (s *Server) taskMemoryScopes(t *Task) []db.MemoryScope {
	if t == nil {
		return []db.MemoryScope{}
	}
	id, err := strconv.ParseInt(t.ID, 10, 64)
	if err != nil || id <= 0 {
		return []db.MemoryScope{}
	}
	return s.memoryScopesForTaskID(id)
}

// memoryBriefBlock agent 注入用:末尾不带多余空行;无内容返回 ""。
func (s *Server) memoryBriefBlock(taskID string) string {
	_, text, ok := s.memoryBriefingSections(taskID)
	if !ok {
		return ""
	}
	return text
}

// memoryBriefingSections 拼装简报(API 与注入共用;60s 进程内缓存)。
// ok=false 表示无任何可注入内容(任务不存在/无 scope 且无任何记忆)。
func (s *Server) memoryBriefingSections(taskID string) ([]memorySection, string, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(taskID), 10, 64)
	if err != nil || id <= 0 {
		return nil, "", false
	}
	memoryBrief.mu.Lock()
	if e := memoryBrief.rows[id]; e.at.Add(memoryBriefCacheTTL).After(time.Now()) {
		memoryBrief.mu.Unlock()
		return e.sections, e.text, e.ok
	}
	memoryBrief.mu.Unlock()
	sections, text, ok := s.buildMemoryBriefing(id)
	memoryBrief.mu.Lock()
	memoryBrief.rows[id] = memoryBriefEntry{at: time.Now(), sections: sections, text: text, ok: ok}
	memoryBrief.mu.Unlock()
	return sections, text, ok
}

// buildMemoryBriefing 真正的取数+拼装(缓存未命中路径)。
func (s *Server) buildMemoryBriefing(taskID int64) ([]memorySection, string, bool) {
	scopes := s.memoryScopesForTaskID(taskID)
	staleDays := s.memoryStaleDaysSetting()
	now := time.Now()

	// ① 活动事实(≤30):三、记忆事实;同一条目也参与已知资产要点(tech_stack/asset_note)。
	facts, err := s.m.PG().ActiveMemoryFacts(scopes, 30)
	if err != nil {
		log.Printf("[memory] 简报取事实失败(任务 %d): %v", taskID, err)
		facts = nil
	}
	// ② 近 3 次 done 复盘(排除本任务自己)。
	runs := s.memoryBriefRuns(taskID, scopes)
	// ③ 企业历史发现(≤15)。
	findings := s.memoryBriefFindings(taskID, scopes)
	// ④ 经验洞察(active,≤12)。
	insights, err := s.m.PG().ActiveEvolveInsights(scopes, 12)
	if err != nil {
		log.Printf("[memory] 简报取洞察失败(任务 %d): %v", taskID, err)
		insights = nil
	}
	// ⑤ 可用剧本(active,≤4)。
	playbooks, err := s.m.PG().ActiveEvolvePlaybooks(scopes, 4)
	if err != nil {
		log.Printf("[memory] 简报取剧本失败(任务 %d): %v", taskID, err)
		playbooks = nil
	}
	// ⑥ 资产概览:按类型计数 + web 服务标题/技术栈样例(≤25 行)。
	assetSummary, assetLines := s.memoryBriefAssets(taskID, staleDays, now)

	hasData := len(facts) > 0 || len(runs) > 0 || len(findings) > 0 ||
		len(insights) > 0 || len(playbooks) > 0 || assetSummary != "" || len(assetLines) > 0
	if !hasData {
		return nil, "", false
	}

	sections := []memorySection{}
	// 一、已知资产要点
	sec1 := []string{}
	if assetSummary != "" {
		sec1 = append(sec1, assetSummary)
	}
	sec1 = append(sec1, assetLines...)
	for _, f := range facts {
		if f.Kind == "asset_note" || f.Kind == "tech_stack" || f.Kind == "path" {
			line := fmt.Sprintf("[fact#%d] %s: %s", f.ID, f.Subject, f.Content)
			if memoryIsStale(f.UpdatedAt, staleDays, now) {
				line += " <待复核>"
			}
			sec1 = append(sec1, truncateRunesHard(line, memoryItemMaxRunes))
		}
	}
	sections = append(sections, memorySection{Title: "一、已知资产要点", Body: memoryJoinOrNone(sec1)})
	// 二、历史发现(漏洞)
	sections = append(sections, memorySection{Title: "二、历史发现（漏洞）", Body: memoryJoinOrNone(findings)})
	// 三、记忆事实
	sec3 := []string{}
	for _, f := range facts {
		line := fmt.Sprintf("[fact#%d] (%s) %s: %s", f.ID, f.Kind, f.Subject, f.Content)
		if memoryIsStale(f.UpdatedAt, staleDays, now) {
			line += " <待复核>"
		}
		sec3 = append(sec3, truncateRunesHard(line, memoryItemMaxRunes))
	}
	sections = append(sections, memorySection{Title: "三、记忆事实", Body: memoryJoinOrNone(sec3)})
	// 四、最近任务复盘
	sec4 := []string{}
	for _, r := range runs {
		if r == nil {
			continue
		}
		sec4 = append(sec4, truncateRunesHard(fmt.Sprintf("[run#%d 任务%d] %s", r.ID, r.TaskID, r.Summary), memoryItemMaxRunes))
	}
	sections = append(sections, memorySection{Title: "四、最近任务复盘", Body: memoryJoinOrNone(sec4)})
	// 五、经验洞察(active)
	sec5 := []string{}
	for _, i := range insights {
		if i == nil {
			continue
		}
		sec5 = append(sec5, truncateRunesHard(fmt.Sprintf("[insight#%d active] (%s) %s: %s", i.ID, i.Kind, i.Title, i.Content), memoryItemMaxRunes))
	}
	sections = append(sections, memorySection{Title: "五、经验洞察（active）", Body: memoryJoinOrNone(sec5)})
	// 六、可用剧本(active,带 [剧本#id] 前缀)
	sec6 := []string{}
	for _, p := range playbooks {
		if p == nil {
			continue
		}
		sec6 = append(sec6, truncateRunesHard(fmt.Sprintf("[剧本#%d] [playbook#%d] %s: %s", p.ID, p.ID, p.Title, p.Summary), memoryItemMaxRunes))
	}
	sections = append(sections, memorySection{Title: "六、可用剧本（active）", Body: memoryJoinOrNone(sec6)})
	// 七、防重复指引(固定文案)
	sections = append(sections, memorySection{Title: "七、防重复指引", Body: memoryAntiRepeat})

	// 整体拼装(总标题 + 各段),整体截 8000 字(纯函数,单测覆盖)。
	text := assembleMemoryBriefingText(sections)
	return sections, text, true
}

// assembleMemoryBriefingText 把七段拼成注入文本:总标题 + 段落两换行分隔,
// 整体截 8000 rune,末尾不带多余空行。
func assembleMemoryBriefingText(sections []memorySection) string {
	var b strings.Builder
	b.WriteString("【记忆简报——来自历史任务沉淀】")
	for _, sec := range sections {
		b.WriteString("\n\n" + sec.Title + "\n" + sec.Body)
	}
	return strings.TrimSpace(truncateRunesHard(b.String(), memoryTextMaxRunes))
}

// memoryBriefRuns 近 3 次 done 复盘(排除本任务)。
func (s *Server) memoryBriefRuns(taskID int64, scopes []db.MemoryScope) []*db.MemoryRun {
	kind, cid := primaryMemoryScope(scopes)
	filter := db.MemoryRunFilter{Limit: 20}
	if kind == "company" {
		filter.CompanyID = cid
	}
	runs, _, err := s.m.PG().ListMemoryRuns(filter)
	if err != nil {
		log.Printf("[memory] 简报取复盘失败(任务 %d): %v", taskID, err)
		return nil
	}
	out := []*db.MemoryRun{}
	for _, r := range runs {
		if r == nil || r.Status != memoryStatusDone || r.TaskID == taskID {
			continue
		}
		out = append(out, r)
		if len(out) >= 3 {
			break
		}
	}
	return out
}

// memoryBriefFindings 企业历史发现(≤15:标题/严重度/类别/时间)。优先主 company 的
// 资产子树;无企业归属的任务退回按本任务发现。
func (s *Server) memoryBriefFindings(taskID int64, scopes []db.MemoryScope) []string {
	kind, cid := primaryMemoryScope(scopes)
	filter := db.FindingFilter{Sort: "time"}
	if kind == "company" && cid > 0 {
		filter.AssetScope = fmt.Sprintf("c:%d", cid)
	} else {
		filter.TaskID = strconv.FormatInt(taskID, 10)
	}
	page, _, err := s.m.PG().ListFindingsPage(filter, 1, 15)
	if err != nil {
		log.Printf("[memory] 简报取发现失败(任务 %d): %v", taskID, err)
		return nil
	}
	out := []string{}
	for _, f := range page {
		if f == nil {
			continue
		}
		name := strings.TrimSpace(f.Name)
		if name == "" {
			name = strings.TrimSpace(f.VulnClass)
		}
		out = append(out, truncateRunesHard(fmt.Sprintf("- [%s] %s(%s,%s)",
			strings.ToUpper(strings.TrimSpace(f.Severity)), name, f.VulnClass, f.CreatedAt.Format("2006-01-02")), memoryItemMaxRunes))
	}
	return out
}

// memoryBriefAssets 资产概览:按类型计数一行 + web 服务标题/技术栈样例(≤25 行)。
// 「距上次验证超 N 天」的行标 <待复核>。
func (s *Server) memoryBriefAssets(taskID int64, staleDays int, now time.Time) (string, []string) {
	as := s.assetStore()
	if as == nil {
		return "", []string{}
	}
	counts, err := as.CountsByTypeForTask(taskID)
	var summary string
	if err == nil && len(counts) > 0 {
		types := make([]string, 0, len(counts))
		for typ := range counts {
			types = append(types, typ)
		}
		sort.Strings(types)
		parts := make([]string, 0, len(types))
		total := 0
		for _, typ := range types {
			parts = append(parts, fmt.Sprintf("%s×%d", typ, counts[typ]))
			total += counts[typ]
		}
		summary = fmt.Sprintf("共 %d 个资产(%s)", total, strings.Join(parts, ", "))
	} else if err != nil {
		log.Printf("[memory] 简报取资产计数失败(任务 %d): %v", taskID, err)
	}
	lines := []string{}
	assets, err := as.QueryByTask(taskID, "", 200, 0)
	if err != nil {
		log.Printf("[memory] 简报取资产失败(任务 %d): %v", taskID, err)
		return summary, lines
	}
	stale := " <待复核>"
	for _, a := range assets {
		if a == nil || len(lines) >= 25 {
			break
		}
		title := strings.TrimSpace(a.PageTitle)
		url := strings.TrimSpace(a.URL)
		if url == "" && title == "" && len(a.Technologies) == 0 {
			continue // 只取 web 服务样例
		}
		parts := []string{}
		if url != "" {
			parts = append(parts, url)
		}
		if title != "" {
			parts = append(parts, "标题: "+title)
		}
		if len(a.Technologies) > 0 {
			parts = append(parts, "技术: "+strings.Join(a.Technologies, ","))
		}
		line := "- " + strings.Join(parts, "; ")
		lastSeen := parseMemoryTime(a.LastSeen)
		if lastSeen != nil && memoryIsStale(*lastSeen, staleDays, now) {
			line += stale
		}
		lines = append(lines, truncateRunesHard(line, memoryItemMaxRunes))
	}
	return summary, lines
}

// memoryStaleDaysSetting 读 settings 覆盖(非法/缺省 → 常量 14)。
func (s *Server) memoryStaleDaysSetting() int {
	if s.m == nil || s.m.PG() == nil {
		return memoryStaleDays
	}
	if v, _, err := s.m.PG().GetSetting(settingMemoryStaleDays); err == nil && v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return memoryStaleDays
}

// memoryIsStale 距上次验证是否超过 N 天。
func memoryIsStale(t time.Time, staleDays int, now time.Time) bool {
	if t.IsZero() || staleDays <= 0 {
		return false
	}
	return now.Sub(t) > time.Duration(staleDays)*24*time.Hour
}

// parseMemoryTime 容错解析 last_seen(RFC3339 / "2006-01-02 15:04:05");失败返回 nil。
func parseMemoryTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}

// memoryJoinOrNone 段正文:空段给「(暂无记录)」,非空按行拼接。
func memoryJoinOrNone(lines []string) string {
	nonEmpty := []string{}
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			nonEmpty = append(nonEmpty, l)
		}
	}
	if len(nonEmpty) == 0 {
		return "(暂无记录)"
	}
	return strings.Join(nonEmpty, "\n")
}

// memoryRecallFor memory_recall 工具的服务端实现(契约 §6):
// scope=auto → 任务 scope+global;company → 仅任务 scope;global → 仅全局。
// 合并 facts/runs/insights/playbooks 的检索结果,输出 top10 行,每行带来源前缀。
// ctx 预留给下游超时传递(db 层当前不接受 ctx,保留参数位保证接线签名稳定)。
func (s *Server) memoryRecallFor(_ context.Context, taskID, query, scope string) (string, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(taskID), 10, 64)
	if err != nil || id <= 0 {
		return "", fmt.Errorf("非法任务 id %q", taskID)
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return "", fmt.Errorf("query 必填")
	}
	if scope != "global" && scope != "company" {
		scope = "auto"
	}
	scopes := s.memoryScopesForTaskID(id)
	kind, cid := primaryMemoryScope(scopes)
	lines := []string{}

	// ① 事实:任务 scope(company 行按主企业过滤;global 行单独取)+ global。
	if scope != "global" && kind == "company" {
		if facts, _, ferr := s.m.PG().ListMemoryFacts(db.MemoryFactFilter{
			CompanyID: cid, Q: q, Limit: 8,
		}); ferr == nil {
			for _, f := range facts {
				if f != nil {
					lines = append(lines, fmt.Sprintf("[fact#%d] (%s) %s: %s", f.ID, f.Kind, f.Subject, truncateRunesHard(f.Content, memoryItemMaxRunes)))
				}
			}
		}
	}
	{
		globalLimit := 10
		if scope == "auto" {
			globalLimit = 5
		}
		if facts, _, ferr := s.m.PG().ListMemoryFacts(db.MemoryFactFilter{ScopeKind: "global", Q: q, Limit: globalLimit}); ferr == nil {
			for _, f := range facts {
				if f != nil {
					lines = append(lines, fmt.Sprintf("[fact#%d] (global) %s: %s", f.ID, f.Subject, truncateRunesHard(f.Content, memoryItemMaxRunes)))
				}
			}
		}
	}

	// ② 复盘:按主企业检索,Go 侧对 summary/highlights 做包含匹配(run 检索无 Q 过滤)。
	if scope != "global" {
		filter := db.MemoryRunFilter{Limit: 50}
		if kind == "company" {
			filter.CompanyID = cid
		}
		if runs, _, rerr := s.m.PG().ListMemoryRuns(filter); rerr == nil {
			n := 0
			for _, r := range runs {
				if r == nil || n >= 3 {
					break
				}
				if !memoryRunMatches(r, q) {
					continue
				}
				lines = append(lines, fmt.Sprintf("[run#%d 任务%d] %s", r.ID, r.TaskID, truncateRunesHard(r.Summary, memoryItemMaxRunes)))
				n++
			}
		}
	}

	// ③ 洞察 / ④ 剧本:仅 active(分级信任:candidate 不注入任务,§0)。
	limit := 8
	if scope == "auto" {
		limit = 5
	}
	companyArg := cid
	if scope == "global" {
		companyArg = 0
	}
	if insights, _, ierr := s.m.PG().ListEvolveInsights(db.EvolveInsightFilter{
		Status: "active", Q: q, CompanyID: companyArg, Limit: limit,
	}); ierr == nil {
		for _, i := range insights {
			if i == nil {
				continue
			}
			if scope == "global" && i.ScopeKind != "global" {
				continue
			}
			lines = append(lines, fmt.Sprintf("[insight#%d active] (%s) %s: %s", i.ID, i.Kind, i.Title, truncateRunesHard(i.Content, memoryItemMaxRunes)))
		}
	}
	if playbooks, _, perr := s.m.PG().ListEvolvePlaybooks(db.EvolvePlaybookFilter{
		Status: "active", Q: q, CompanyID: companyArg, Limit: limit,
	}); perr == nil {
		for _, p := range playbooks {
			if p == nil {
				continue
			}
			if scope == "global" && p.ScopeKind != "global" {
				continue
			}
			lines = append(lines, fmt.Sprintf("[playbook#%d] %s: %s", p.ID, p.Title, truncateRunesHard(p.Summary, memoryItemMaxRunes)))
		}
	}

	// 合并去重 + top10。
	seen := map[string]bool{}
	out := []string{}
	for _, l := range lines {
		if seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
		if len(out) >= 10 {
			break
		}
	}
	if len(out) == 0 {
		return "未检索到相关跨任务记忆。", nil
	}
	return "跨任务记忆检索结果:\n" + strings.Join(out, "\n"), nil
}

// memoryRunMatches 复盘行的包含匹配:query 命中 summary/highlights/open_leads 任一。
func memoryRunMatches(r *db.MemoryRun, q string) bool {
	if r == nil {
		return false
	}
	needle := strings.ToLower(q)
	hay := strings.ToLower(r.Summary + "\n" + r.ReflectError)
	for _, h := range r.Highlights {
		hay += "\n" + h
	}
	for _, l := range r.OpenLeads {
		hay += "\n" + l
	}
	for _, f := range r.Failures {
		hay += "\n" + f.What + "\n" + f.RootCause
	}
	return strings.Contains(hay, needle)
}
