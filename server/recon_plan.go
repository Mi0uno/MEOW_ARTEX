package server

// 信息搜集的 AI 编排:自动检测可用数据源 + AI 生成查询语句。
//
// 背景:16 家外部数据源里 13 家是 domain_enum 型(只需目标域名,无需 query),
// 只有 fofa/hunter/quake/shodan/zoomeye 是 search 型需要查询语句。此前创建任务
// 要求用户逐一手选数据源并手写 query,既繁琐又容易漏掉可用的数据源。
//
// 现在:创建信息搜集任务只选企业,系统自动检测「已配置且已启用」的外部接口,
// 全部纳入搜集范围;search 型数据源的 query 由 AI 依企业资产范围生成,失败时
// 回退到内置的保守默认语句。目标是「最全面地搜集暴露面」,不因缺配置而中断。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/recon"
	"github.com/Autumn-27/norma/llm"
)

// ReconSourcePlan 是一个数据源的执行计划(自动检测产物)。
type ReconSourcePlan struct {
	Provider    string `json:"provider"`     // provider key(fofa/hunter/quake/…)
	Title       string `json:"title"`        // 展示名
	Kind        string `json:"kind"`         // search | domain_enum
	ExternalAPI int64  `json:"external_api"` // 选用的外部接口配置 id
	APIName     string `json:"api_name"`     // 接口配置名(展示用)
	Domain      string `json:"domain,omitempty"`
	Query       string `json:"query,omitempty"`
	QuerySource string `json:"query_source,omitempty"` // ai | default | manual
	SkipReason  string `json:"skip_reason,omitempty"`  // 不可用原因(展示给用户)
}

// ReconPlan 是一次自动检测的完整结果。
type ReconPlan struct {
	Sources    []ReconSourcePlan `json:"sources"`
	Domains    []string          `json:"domains"`   // 企业范围里的根域名
	Available  int               `json:"available"` // 可用数据源数
	Skipped    int               `json:"skipped"`   // 因未配置/未启用跳过的数量
	AIUsed     bool              `json:"ai_used"`   // query 是否由 AI 生成
	AINote     string            `json:"ai_note,omitempty"`
	Companies  []int64           `json:"companies"`
	TargetName string            `json:"target_name,omitempty"`
}

// defaultQueryFor 是 search 型数据源在没有 AI 时的保守默认查询语句。
// 各家语法不同(见 recon/external.go 的 Query 实现),这里按 provider 出默认式。
func defaultQueryFor(provider, domain string) string {
	if domain == "" {
		return ""
	}
	switch provider {
	case "fofa":
		// FOFA: domain="x" 语法
		return fmt.Sprintf(`domain="%s"`, domain)
	case "hunter":
		// Hunter: domain.suffix="x"
		return fmt.Sprintf(`domain.suffix="%s"`, domain)
	case "quake":
		// Quake: domain:"x"
		return fmt.Sprintf(`domain:"%s"`, domain)
	case "shodan":
		// Shodan: hostname 过滤(它的查询语言用 hostname:)
		return fmt.Sprintf(`hostname:%s`, domain)
	case "zoomeye":
		// ZoomEye: host="x" 或 domain="x"
		return fmt.Sprintf(`host="%s"`, domain)
	default:
		return fmt.Sprintf(`domain="%s"`, domain)
	}
}

// buildReconPlan 自动检测可用数据源并生成执行计划。
//
// 规则:
//   - 只纳入「已启用」的外部接口配置;同一 provider 有多个启用配置时取第一个
//     (与创建页原行为一致——多个账号时按 id 升序取最早的)。
//   - domain_enum 型:填目标域名(企业范围里的第一个根域名),无需 query。
//   - search 型:query 由 AI 生成(profileID>0 且有可用 provider 时),失败回退默认。
//   - 未配置的 provider 记入 SkipReason 而非报错:搜集范围由可用性决定,不阻断。
func (s *Server) buildReconPlan(ctx context.Context, companyIDs []int64, profileID int64, useAI bool) (*ReconPlan, error) {
	domains, targetName, err := s.companyRootDomains(companyIDs)
	if err != nil {
		return nil, err
	}
	plan := &ReconPlan{Domains: domains, Companies: companyIDs, TargetName: targetName}

	// 已启用的外部接口,按 provider 分组(同 provider 取 id 最小的那条)。
	apis, err := s.m.PG().ExternalAPIs().List()
	if err != nil {
		return nil, fmt.Errorf("读取外部接口失败: %w", err)
	}
	byProvider := map[string]*db.ExternalAPI{}
	for _, a := range apis {
		if !a.Enabled {
			continue
		}
		if prev, ok := byProvider[a.Provider]; !ok || a.ID < prev.ID {
			byProvider[a.Provider] = a
		}
	}

	// 遍历全部已注册 provider(16 家),可用的纳入、不可用的记原因。
	for _, def := range recon.ProviderDescriptions() {
		kind := "search"
		if def.Kind == recon.ProviderKindDomainEnum {
			kind = "domain_enum"
		}
		sp := ReconSourcePlan{Provider: def.Key, Title: def.Title, Kind: kind}
		api, ok := byProvider[def.Key]
		if !ok {
			sp.SkipReason = "未配置或未启用的外部接口"
			plan.Sources = append(plan.Sources, sp)
			plan.Skipped++
			continue
		}
		sp.ExternalAPI = api.ID
		sp.APIName = api.Name
		if kind == "domain_enum" {
			if len(domains) == 0 {
				sp.SkipReason = "企业未录入根域名范围"
				plan.Sources = append(plan.Sources, sp)
				plan.Skipped++
				continue
			}
			sp.Domain = domains[0]
		} else {
			// search 型需要 query;先给默认式,AI 可用时覆盖。
			if len(domains) == 0 {
				sp.SkipReason = "企业未录入根域名范围,无法构造查询语句"
				plan.Sources = append(plan.Sources, sp)
				plan.Skipped++
				continue
			}
			sp.Query = defaultQueryFor(def.Key, domains[0])
			sp.QuerySource = "default"
		}
		plan.Sources = append(plan.Sources, sp)
		plan.Available++
	}

	// AI 生成 search 型数据源的查询语句(只处理真正需要 query 的)。
	if useAI && profileID > 0 && len(domains) > 0 {
		if s.refineReconQueries(ctx, plan, profileID) {
			plan.AIUsed = true
		}
	}
	return plan, nil
}

// refineReconQueries 让 AI 依企业资产范围为 search 型数据源生成更全面的查询语句。
// 失败不阻断:保留默认式,在 AINote 里说明。
func (s *Server) refineReconQueries(ctx context.Context, plan *ReconPlan, profileID int64) bool {
	var need []int
	for i := range plan.Sources {
		if plan.Sources[i].Kind == "search" && plan.Sources[i].SkipReason == "" {
			need = append(need, i)
		}
	}
	if len(need) == 0 {
		return false
	}
	prov, _, ok := s.providerForProfile(profileID)
	if !ok {
		plan.AINote = "AI 不可用(LLM 配置无效),已使用内置默认查询语句"
		return false
	}

	// 提示词:要求为每个数据源输出该平台语法的查询语句,覆盖子域/同 IP/证书等面。
	type ask struct {
		Provider string `json:"provider"`
		Title    string `json:"title"`
	}
	asks := make([]ask, 0, len(need))
	for _, i := range need {
		asks = append(asks, ask{Provider: plan.Sources[i].Provider, Title: plan.Sources[i].Title})
	}
	askJSON, _ := json.Marshal(asks)

	system := "你是网络空间测绘查询专家。为给定的每个数据源生成**最全面覆盖目标暴露面**的查询语句。\n" +
		"各数据源的查询语法不同,必须用该平台自己的语法:\n" +
		"- fofa: domain=\"example.com\" 或 cert=\"example.com\" 或 ip=\"1.2.3.0/24\"\n" +
		"- hunter: domain.suffix=\"example.com\"\n" +
		"- quake: domain:\"example.com\"\n" +
		"- shodan: hostname:example.com 或 ssl.cert.subject.cn:example.com\n" +
		"- zoomeye: host=\"example.com\"\n" +
		"目标:尽可能全面地覆盖该企业的资产(主域、子域、证书关联、同 C 段)。\n" +
		"只输出 JSON 对象:{\"queries\":{\"<provider>\":\"<query>\"}},不要任何解释或代码块围栏。"

	user := fmt.Sprintf("目标根域名: %s\n企业: %s\n需要生成查询的数据源: %s",
		strings.Join(plan.Domains, ", "), plan.TargetName, string(askJSON))

	genCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := analyzeCompletion(genCtx, prov, system, user)
	if err != nil {
		log.Printf("[recon-plan] AI 生成查询语句失败: %v", err)
		plan.AINote = "AI 生成查询语句失败,已使用内置默认语句: " + err.Error()
		return false
	}
	// 剥离可能的代码块围栏后解析。
	out = strings.TrimSpace(out)
	out = strings.TrimPrefix(out, "```json")
	out = strings.TrimPrefix(out, "```")
	out = strings.TrimSuffix(strings.TrimSpace(out), "```")
	var parsed struct {
		Queries map[string]string `json:"queries"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &parsed); err != nil {
		log.Printf("[recon-plan] AI 返回无法解析为 JSON: %v", err)
		plan.AINote = "AI 返回格式异常,已使用内置默认语句"
		return false
	}
	applied := 0
	for _, i := range need {
		q := strings.TrimSpace(parsed.Queries[plan.Sources[i].Provider])
		if q == "" {
			continue
		}
		plan.Sources[i].Query = q
		plan.Sources[i].QuerySource = "ai"
		applied++
	}
	if applied == 0 {
		plan.AINote = "AI 未返回有效查询语句,已使用内置默认语句"
		return false
	}
	log.Printf("[recon-plan] AI 为 %d 个数据源生成了查询语句", applied)
	return true
}

// companyRootDomains 取企业范围里的根域名(去重、稳定排序)与企业名。
func (s *Server) companyRootDomains(companyIDs []int64) ([]string, string, error) {
	seen := map[string]bool{}
	var domains []string
	var name string
	for _, id := range companyIDs {
		if c, err := s.m.PG().Companies().GetCompany(id); err == nil && c != nil && name == "" {
			name = c.Name
		}
		rules, err := s.m.PG().Companies().GetScope(id)
		if err != nil {
			return nil, "", fmt.Errorf("读取企业 #%d 范围失败: %w", id, err)
		}
		for _, r := range rules {
			if r.Kind != "domain" || r.Domain == "" {
				continue
			}
			d := strings.ToLower(strings.TrimSpace(r.Domain))
			if d != "" && !seen[d] {
				seen[d] = true
				domains = append(domains, d)
			}
		}
	}
	sort.Strings(domains)
	return domains, name, nil
}

// ReconPlanFor 供 API 层调用:自动检测 + AI 生成查询(不落库,仅预览)。
func (s *Server) ReconPlanFor(ctx context.Context, companyIDs []int64, profileID int64, useAI bool) (*ReconPlan, error) {
	return s.buildReconPlan(ctx, companyIDs, profileID, useAI)
}

// reconPlanProfileID 取生成查询语句用的 LLM 配置:任务自身配置链优先,其次全局激活。
// 返回 0 表示没有可用配置(调用方会用内置默认查询语句)。
func (s *Server) reconPlanProfileID(t *Task) int64 {
	if t != nil {
		st := t.llmStateSnapshot()
		if st.ActiveID != nil && *st.ActiveID > 0 {
			return *st.ActiveID
		}
		if len(st.ProfileIDs) > 0 && st.ProfileIDs[0] > 0 {
			return st.ProfileIDs[0]
		}
	}
	if p, err := s.m.pg.ActiveProfile(); err == nil && p != nil {
		return p.ID
	}
	return 0
}

// 保证 llm 包被引用(analyzeCompletion 的签名依赖)。
var _ llm.Provider
