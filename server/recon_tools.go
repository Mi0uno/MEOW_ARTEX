package server

// 信息搜集阶段的 AI 工具:全部 16 家外部数据源都生成**真·可调用工具**
// (wrTool 标准范本:tools 表可管、meterTool 记账、intercept 审批门全覆盖),
// 不是展示壳。命名约定:
//   search 型     → {key}_search (fofa_search / hunter_search / quake_search …)
//   domain_enum 型 → {key}_enum    (chaos_enum / certspotter_enum …)
// 凭据运行时经 resolveExternalSecrets 解密注入,绝不进模型上下文。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/recon"
	actool "github.com/Autumn-27/norma/tool"
)

const (
	// maxQueryToolResults 单次返回给模型的结果条数上限(控上下文体积)。
	maxQueryToolResults = 100
	// 查询超时(出网查询,给足 TLS+上游处理时间)。
	queryToolTimeout = 90 * time.Second
)

// reconQueryTools 返回信息搜集阶段全部 AI 查询工具(经 hostTools 注入)。
// 16 家外部数据源 + subdomain_brute(DNS 字典爆破)。
func (s *Server) reconQueryTools() []actool.CoreTool {
	out := make([]actool.CoreTool, 0, len(recon.ProviderDescriptions())+1)
	for _, def := range recon.ProviderDescriptions() {
		out = append(out, s.toolProviderQuery(def))
	}
	out = append(out, s.toolSubdomainBrute())
	return out
}

// toolNameFor 按约定生成工具名。
func toolNameFor(def recon.ProviderDef) string {
	if def.Key == "fofa" {
		return "fofa_search"
	}
	if def.Key == "hunter" {
		return "hunter_search"
	}
	if def.Kind == recon.ProviderKindDomainEnum {
		return def.Key + "_enum"
	}
	return def.Key + "_search"
}

// toolProviderQuery 由 ProviderDef 生成一个真·可调用工具:
// schema 按 kind 出两型(search 用 query;domain_enum 用 domain),handler 共用
// queryToolCommon(选配置 → 解密凭据 → 查询 → 截断打包给模型)。
func (s *Server) toolProviderQuery(def recon.ProviderDef) actool.CoreTool {
	name := toolNameFor(def)
	var schema map[string]any
	var desc string
	if def.Kind == recon.ProviderKindDomainEnum {
		desc = "用 " + def.Title + " 对目标域名做子域/资产枚举,返回子域名列表。" +
			"需要平台已配置启用的 " + def.Title + " 外部接口(凭据自动解密使用)。"
		schema = objSchema(map[string]any{
			"domain": strParam("目标根域名,如 example.com"),
			"size": map[string]any{
				"type":        "integer",
				"description": "返回条数上限,默认 100",
				"default":     100,
			},
			"external_api_id": map[string]any{
				"type":        "integer",
				"description": "外部接口配置 id;平台只有一条启用的该类接口时可省略",
			},
		}, "domain")
	} else {
		desc = "用 " + def.Title + " 空间测绘查询互联网资产,输入查询语句,返回按类型分类的" +
			"域名/子域名/IP/Web服务列表。用于:扩大攻击面、确认 IP/域名归属与服务、找同主体资产。" +
			"需要平台已配置启用的 " + def.Title + " 外部接口(凭据自动解密使用)。"
		schema = objSchema(map[string]any{
			"query": strParam(def.Title + " 查询语句,语法见其官方文档"),
			"size": map[string]any{
				"type":        "integer",
				"description": "返回条数上限,默认 50,最大 100",
				"default":     50,
			},
			"external_api_id": map[string]any{
				"type":        "integer",
				"description": "外部接口配置 id;平台只有一条启用的该类接口时可省略",
			},
		}, "query")
	}
	return wrTool(name, desc, schema, func(_ context.Context, in json.RawMessage) (actool.Result, error) {
		return s.queryToolCommon(def.Key, in)
	})
}

// seedReconQueryTools 把全部 AI 查询工具 seed 进 tools 表:
// stage=recon,默认绑 pentest+worker+recon_worker。并清掉早期误种的收集器展示壳行
// (那些 key 是收集器标识,不是 agent 工具,当时只写了个壳——已由真工具取代)。
func (s *Server) seedReconQueryTools() {
	if s.m.PG() == nil {
		return
	}
	for _, t := range s.reconQueryTools() {
		schema, _ := json.Marshal(t.InputSchema())
		agents, _ := json.Marshal([]string{"pentest", "worker", "recon_worker"})
		if err := s.m.PG().SeedToolWithStage(t.Name(), t.Description(), schema, agents, "recon"); err != nil {
			log.Printf("[recon-tools] seed %s 失败: %v", t.Name(), err)
		}
	}
	// 清理壳行:收集器 key 不是 agent 工具(它们的入口是步骤任务/工具执行页)。
	if _, err := s.m.PG().DeleteToolsByKeys(recon.CollectorToolKeys()); err != nil {
		log.Printf("[recon-tools] 清理壳行失败: %v", err)
	}
}

// pickExternalAPI 按 provider 选外部接口配置:显式 id 优先;缺省时该 provider 必须
// 恰有一条启用配置(多条时要求模型指定 id,避免打错账号)。
func (s *Server) pickExternalAPI(provider string, externalAPIID int64) (*db.ExternalAPI, error) {
	store := s.m.PG().ExternalAPIs()
	if externalAPIID > 0 {
		a, err := store.Get(externalAPIID)
		if err != nil {
			return nil, fmt.Errorf("外部接口 #%d 读取失败", externalAPIID)
		}
		if a.Provider != provider {
			return nil, fmt.Errorf("外部接口 «%s» 的 provider 是 %s,不是 %s", a.Name, a.Provider, provider)
		}
		if !a.Enabled {
			return nil, fmt.Errorf("外部接口 «%s» 已停用", a.Name)
		}
		return a, nil
	}
	enabled, err := store.List()
	if err != nil {
		return nil, err
	}
	var matches []*db.ExternalAPI
	for _, a := range enabled {
		if a.Provider == provider && a.Enabled {
			matches = append(matches, a)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return nil, fmt.Errorf("没有启用的 %s 接口,请先在「信息搜集 → 外部接口」配置", provider)
	default:
		names := make([]string, 0, len(matches))
		for _, a := range matches {
			names = append(names, fmt.Sprintf("#%d «%s»", a.ID, a.Name))
		}
		return nil, fmt.Errorf("有 %d 条启用的 %s 接口,请用 external_api_id 指定: %s",
			len(matches), provider, strings.Join(names, ", "))
	}
}

// queryToolCommon 共用执行:选配置 → 解密凭据 → 查询 → 截断打包给模型。
func (s *Server) queryToolCommon(provider string, in json.RawMessage) (actool.Result, error) {
	var p struct {
		Query         string `json:"query"`
		Domain        string `json:"domain"`
		Size          int    `json:"size"`
		ExternalAPIID int64  `json:"external_api_id"`
	}
	_ = json.Unmarshal(in, &p)
	if p.Size <= 0 || p.Size > maxQueryToolResults {
		p.Size = 50
	}
	def, ok := recon.Provider(provider)
	if !ok {
		return actool.Errorf("未知 provider " + provider), nil
	}
	if def.Kind == recon.ProviderKindDomainEnum && strings.TrimSpace(p.Domain) == "" {
		return actool.Errorf("domain 不能为空(枚举型数据源需要目标根域名)"), nil
	}
	a, err := s.pickExternalAPI(provider, p.ExternalAPIID)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	creds, err := s.resolveExternalSecrets(provider, map[string]any{"external_api_id": float64(a.ID)})
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	base, _ := creds["base_url"].(string)
	ctx, cancel := context.WithTimeout(context.Background(), queryToolTimeout)
	defer cancel()

	stringCreds := map[string]string{}
	for k, v := range creds {
		sv, _ := v.(string)
		stringCreds[k] = sv
	}
	qin := recon.QueryInput{Query: p.Query, Domain: p.Domain, Size: p.Size, Page: 1}
	assets, _, err := def.Query(ctx, base, stringCreds, qin)
	if err != nil {
		return actool.Errorf("查询失败: " + err.Error()), nil
	}
	if len(assets) > maxQueryToolResults {
		assets = assets[:maxQueryToolResults]
	}
	return jsonResult(map[string]any{
		"provider": provider,
		"total":    len(assets),
		"hint":     "结果已按 域名/子域名/IP/Web服务 分类;这些资产尚未进入资产图,需要落库请建信息搜集步骤任务",
		"assets":   assets,
	})
}

// toolSubdomainBrute 子域字典爆破 AI 工具:读取字典库中选定的字典文件,
// 对目标根域名做 DNS 暴力枚举,返回解析成功的子域列表(含泛解析过滤)。
// 默认绑定 pentest + worker + recon_worker。
func (s *Server) toolSubdomainBrute() actool.CoreTool {
	return wrTool("subdomain_brute",
		"子域字典爆破:对目标根域名用字典做 DNS 暴力枚举,返回解析成功的子域列表。"+
			"内置泛解析检测(自动过滤假阳性)。需要先在「字典库」上传子域字典(.txt 每行一条)。",
		objSchema(map[string]any{
			"domain": strParam("目标根域名,如 example.com"),
			"wordbook_id": map[string]any{
				"type":        "integer",
				"description": "字典库中的字典 id;省略时自动选用第一个 subdomain 类型字典",
			},
			"max_results": map[string]any{
				"type":        "integer",
				"description": "返回条数上限,默认 100,最大 5000",
				"default":     100,
			},
		}, "domain"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var p struct {
				Domain     string `json:"domain"`
				WordbookID int64  `json:"wordbook_id"`
				MaxResults int    `json:"max_results"`
			}
			_ = json.Unmarshal(in, &p)
			if p.Domain == "" {
				return actool.Errorf("domain 不能为空"), nil
			}
			// 解析字典路径
			wb, wbErr := s.m.PG().GetWordbook(p.WordbookID)
			if wbErr != nil || wb == nil {
				books, listErr := s.m.PG().ListWordbooks()
				if listErr == nil {
					for _, b := range books {
						if b.Kind == "subdomain" {
							wb = b
							break
						}
					}
				}
			}
			if wb == nil {
				return actool.Errorf("没有可用的子域字典,请先在「字典库」上传(.txt 每行一条)"), nil
			}
			path := wordbookFilePath(s.m.dir, wb.ID)
			if _, statErr := os.Stat(path); statErr != nil {
				return actool.Errorf("字典文件缺失,请重新上传"), nil
			}
			words, _, readErr := recon.ReadWordbookLines(path, 200000)
			if readErr != nil {
				return actool.Errorf("读字典失败: " + readErr.Error()), nil
			}
			if len(words) == 0 {
				return actool.Errorf("字典有效词为空"), nil
			}
			// 并发 DNS 解析 + 泛解析过滤
			resolver := net.DefaultResolver
			dCtx, dCancel := context.WithTimeout(context.Background(), 300*time.Second)
			defer dCancel()
			wildIPs := map[string]bool{}
			for i := 0; i < 3; i++ {
				probe := fmt.Sprintf("artex-probe-%d.%s", i, p.Domain)
				ips, lookupErr := resolver.LookupHost(dCtx, probe)
				if lookupErr != nil || len(ips) == 0 {
					break
				}
				for _, ip := range ips {
					wildIPs[ip] = true
				}
			}
			type dnsHit struct{ host string }
			var (
				mu           sync.Mutex
				hits         []string
				wildFiltered int
			)
			sem := make(chan struct{}, 50)
			var wg sync.WaitGroup
			for _, w := range words {
				select {
				case <-dCtx.Done():
				case sem <- struct{}{}:
				}
				if dCtx.Err() != nil {
					break
				}
				wg.Add(1)
				go func(word string) {
					defer wg.Done()
					defer func() { <-sem }()
					host := word + "." + p.Domain
					qCtx, qCancel := context.WithTimeout(dCtx, 3*time.Second)
					defer qCancel()
					ips, lookupErr := net.DefaultResolver.LookupHost(qCtx, host)
					if lookupErr != nil || len(ips) == 0 {
						return
					}
					mu.Lock()
					defer mu.Unlock()
					for _, ip := range ips {
						if wildIPs[ip] {
							wildFiltered++
							return
						}
					}
					if len(hits) < maxQueryToolResults {
						hits = append(hits, strings.ToLower(host))
					}
				}(w)
			}
			wg.Wait()
			var outAssets []map[string]any
			for _, h := range hits {
				outAssets = append(outAssets, map[string]any{"type": "domain", "value": h})
			}
			return jsonResult(map[string]any{
				"tool":          "subdomain_brute",
				"domain":        p.Domain,
				"dictionary":    wb.Name,
				"total_words":   len(words),
				"total":         len(hits),
				"wild_filtered": wildFiltered,
				"wildcard":      len(wildIPs) > 0,
				"hint":          "命中子域为 DNS 解析验证过的真实资产;尚未入库,需落库请建信息搜集步骤任务",
				"subdomains":    outAssets,
			})
		})
}
