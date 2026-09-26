package server

// 任务资产识别入库管线(契约 ASSET_EXTRACT_CONTRACTS.md §3):
//
//	终态钩子(只追加) → maybeStartAssetExtract → 门禁(kind=agent/running 互斥)
//	→ 确定性段(零 LLM: 过程数据 → email 预删 → URL/host:port/IPv4/裸 hostname
//	四路正则候选 → 值比对跳过已有 → 暂存 staged_assets, collector=auto_extract)
//	→ LLM 段(开关 asset_extract_ai 缺省开;关闭/无 LLM/调用失败/解析失败一律降级
//	partial 不阻断) → AI 确认 + missing 逐条采纳入库(source=ai) → run 落终态。
//
// 红线(契约 §9): 确定性段永不写正式资产图;内网 IP 保留,loopback/0.0.0.0/广播
// 丢弃;全部 db 错误 log-and-continue;run 行保证终态(panic 恢复置 failed)。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

// settingAssetExtractAI settings 键:「AI 识别」总开关(契约 §3.3,缺省开,仅显式 "false" 关)。
const settingAssetExtractAI = "asset_extract_ai"

const (
	assetStatusPending = "pending"
	assetStatusRunning = "running"
	assetStatusDone    = "done"
	assetStatusPartial = "partial"
	assetStatusFailed  = "failed"

	// 暂存 collector 标识:确定性提取 vs LLM 新发现(契约 §3.2.5/§3.4.1)。
	assetCollectorAuto = "auto_extract"
	assetCollectorAI   = "ai_extract"

	// 只对 agent 任务跑提取(契约 §9);recon 等 pipeline 任务不碰。
	assetExtractTaskKind = "agent"
)

// 扫描与容量上限(契约 §3.2/§3.3/§3.5)。
const (
	assetScanMaxBytes    = 500 << 10 // 过程数据总扫描字符 cap 500KB
	assetCandidateCap    = 300       // 确定性候选 cap(超出丢弃)
	assetDetailCap       = 300       // run.detail 明细条数 cap
	assetPromptActsLimit = 40        // LLM 输入的 tool_use 摘录条数
	assetPromptActsBytes = 40 << 10  // 摘录总字符 cap 40KB
	assetMissingCap      = 50        // LLM 新发现 cap
	assetSkipScanAssets  = 1000      // 值比对时取本任务已有资产的条数上限
)

// assetExtractSem 资产识别的全局并发闸(容量 1,仿 memoryReflectSem):重 LLM 调用串行。
var assetExtractSem = make(chan struct{}, 1)

// ── 提取正则(契约 §3.2.3,RE2) ──
var (
	// 先删 email 再扫 hostname,防邮箱右半被误提(契约 §3.2.2)。
	reAssetEmail = regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`)
	reAssetURL   = regexp.MustCompile(`(?i)https?://[^\s"'` + "`" + `<>()\[\]{}\\,;]+`)
	// host:port(域名形态)。
	reAssetHostPort = regexp.MustCompile(`\b(?:[a-zA-Z0-9-]+\.)+[a-zA-Z]{2,}:\d{2,5}\b`)
	// IPv4:port 与裸 IPv4(octet≤255 由 net.ParseIP 校验)。
	reAssetIPv4 = regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}(:\d{2,5})?\b`)
	// 裸 hostname(≤253 长度在 cleanAssetHostname 校验)。
	reAssetHost = regexp.MustCompile(`\b(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}\b`)
)

// assetCandidate 是确定性提取出的一条资产候选(净化后)。
type assetCandidate struct {
	Type   string `json:"type"`             // service|ip|domain
	Value  string `json:"value"`            // host 或 ip
	Port   int    `json:"port"`             // 显式端口,无则 0
	URL    string `json:"url,omitempty"`    // 仅 URL 来源(service)
	Source string `json:"source,omitempty"` // 来源段 kind(activity kind / finding)
}

// assetDecisionDetail 是 run.detail 里的一条候选决策明细(契约 §3.5)。
type assetDecisionDetail struct {
	Value    string `json:"value"`
	Type     string `json:"type"`
	Port     int    `json:"port"`
	Decision string `json:"decision"` // confirmed|pending|dup
}

// ── 门禁与启动(契约 §3.1) ──

// maybeStartAssetExtract 终态钩子入口:任务不存在/非 agent 直接返回(不写 run 行);
// running 中跳过;否则异步起管线。由 server.go 的终态组合回调在 maybeStartMemoryReflect
// 之后追加调用。
func (s *Server) maybeStartAssetExtract(taskID int64) {
	if _, ok, _ := s.beginAssetExtract(taskID); !ok {
		return
	}
	go s.runAssetExtract(taskID)
}

// beginAssetExtract 门禁 + running 行落库(契约 §3.1/§3.5):kind 校验、running 互斥、
// upsert status=running(清零计数)。返回 (runID, started, conflict);started=false 且
// conflict=false 表示任务不存在/非 agent(调用方静默);conflict=true 表示已有识别在跑。
func (s *Server) beginAssetExtract(taskID int64) (runID int64, started bool, conflict bool) {
	if taskID <= 0 {
		return 0, false, false
	}
	pt, err := s.m.PG().GetTask(taskID)
	if err != nil || pt == nil {
		return 0, false, false
	}
	if pt.Kind != assetExtractTaskKind {
		return 0, false, false
	}
	if existing, err := s.m.PG().GetAssetExtractRunByTask(taskID); err == nil && existing != nil && existing.Status == assetStatusRunning {
		return 0, false, true
	}
	id, err := s.m.PG().UpsertAssetExtractRun(&db.AssetExtractRun{
		TaskID: taskID,
		Status: assetStatusRunning,
		Detail: json.RawMessage("[]"),
	})
	if err != nil {
		log.Printf("[asset-extract] 任务 %d 写 running run 失败: %v", taskID, err)
		return 0, false, false
	}
	return id, true, false
}

// ── 管线主体(契约 §3.2-§3.5) ──

// runAssetExtract 管线主体:串行闸内跑确定性段 + AI 段,保证 run 行落到终态
// (done=全链成功;partial=确定性完成但 AI 段跳过/失败;failed=管线异常)。
func (s *Server) runAssetExtract(taskID int64) {
	assetExtractSem <- struct{}{}
	defer func() { <-assetExtractSem }()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[asset-extract] 任务 %d 管线 panic: %v", taskID, r)
			_ = s.m.PG().SetAssetExtractRunStatus(taskID, assetStatusFailed, truncateRunesHard(fmt.Sprintf("管线异常: %v", r), 300))
		}
	}()

	// ── 确定性段(零 LLM,契约 §3.2) ──
	segments, actTexts := s.assetExtractSegments(taskID)
	candidates := extractAssetCandidates(segments)
	if len(candidates) > assetCandidateCap {
		candidates = candidates[:assetCandidateCap]
	}
	companyOverride := s.assetExtractCompanyOverride(taskID)
	skipKeys := s.assetTaskSkipKeys(taskID)

	// 批次号:每次识别一个新批次,与 recon runner 同一取法(Max+1)。
	batchID, err := s.m.PG().MaxStagedAssetBatchID(taskID)
	if err != nil {
		log.Printf("[asset-extract] 任务 %d 取批次号失败: %v", taskID, err)
	}
	batchID++

	details := []assetDecisionDetail{}
	deterministic := 0
	for _, c := range candidates {
		decision := assetStatusPending
		if candidateSkipped(c, skipKeys) {
			decision = "dup"
		} else {
			sa := candidateStaged(c, companyOverride, assetCollectorAuto)
			sa.TaskID = taskID
			sa.BatchID = batchID
			if _, err := s.m.PG().InsertStagedAsset(sa); err != nil {
				log.Printf("[asset-extract] 任务 %d 写暂存失败(%s %s): %v", taskID, c.Type, c.Value, err)
			} else {
				deterministic++
			}
		}
		if len(details) < assetDetailCap {
			details = append(details, assetDecisionDetail{Value: c.Value, Type: c.Type, Port: c.Port, Decision: decision})
		}
	}

	// 确定性计数先行落库(AI 段再覆盖终态):中途崩溃也能看见提取成果。
	if _, err := s.m.PG().UpsertAssetExtractRun(&db.AssetExtractRun{
		TaskID: taskID, Status: assetStatusRunning,
		DeterministicCount: deterministic,
		Detail:             assetDetailJSON(details),
	}); err != nil {
		log.Printf("[asset-extract] 任务 %d 写确定性计数失败: %v", taskID, err)
	}

	// ── LLM 段(契约 §3.3,关闭/失败降级 partial 不阻断) ──
	aiErr := ""
	aiConfirmed, aiNew, adopted := 0, 0, 0
	_, _, provOK := s.globalProvider()
	switch {
	case !s.assetExtractAIEnabled():
		aiErr = "AI 识别已关闭,候选已入暂存待人工采纳"
	case !provOK:
		aiErr = "LLM 未配置,候选已入暂存待人工采纳"
	default:
		parsed, err := s.assetExtractLLM(taskID, candidates, actTexts)
		if err != nil {
			aiErr = truncateRunesHard("LLM 判定失败: "+err.Error(), 300)
			break
		}
		// ① AI 确认的候选采纳入库(契约 §3.4.2);已是本任务资产的(dup)无需再采纳。
		for _, idx := range parsed.Confirmed {
			if idx < 0 || idx >= len(candidates) {
				continue
			}
			c := candidates[idx]
			if candidateSkipped(c, skipKeys) {
				continue
			}
			markAssetDecision(details, c, "confirmed")
			if s.adoptAssetCandidate(c, taskID, companyOverride, assetCollectorAuto) {
				adopted++
			}
			aiConfirmed++
		}
		// ② missing 先插暂存行(collector=ai_extract)再采纳(契约 §3.4.1)。
		for _, m := range parsed.Missing {
			c := missingToCandidate(m)
			if c == nil || candidateSkipped(*c, skipKeys) {
				continue
			}
			sa := candidateStaged(*c, companyOverride, assetCollectorAI)
			sa.TaskID = taskID
			sa.BatchID = batchID
			if _, err := s.m.PG().InsertStagedAsset(sa); err != nil {
				log.Printf("[asset-extract] 任务 %d 写 AI 新发现暂存失败(%s %s): %v", taskID, c.Type, c.Value, err)
			} else {
				aiNew++
			}
			if s.adoptAssetCandidate(*c, taskID, companyOverride, assetCollectorAI) {
				adopted++
			}
		}
	}

	// ── 终态落库(契约 §1/§3.5):done=全链成功;partial=AI 段跳过/失败 ──
	status := assetStatusDone
	if aiErr != "" {
		status = assetStatusPartial
	}
	final := &db.AssetExtractRun{
		TaskID: taskID, Status: status,
		DeterministicCount: deterministic,
		AIConfirmedCount:   aiConfirmed,
		AINewCount:         aiNew,
		AdoptedCount:       adopted,
		Detail:             assetDetailJSON(details),
		Error:              aiErr,
	}
	if _, err := s.m.PG().UpsertAssetExtractRun(final); err != nil {
		log.Printf("[asset-extract] 任务 %d 写终态 run 失败: %v", taskID, err)
		// 兜底:至少把状态落定,不留悬挂 running。
		if err2 := s.m.PG().SetAssetExtractRunStatus(taskID, status, aiErr); err2 != nil {
			log.Printf("[asset-extract] 任务 %d 兜底落状态失败: %v / %v", taskID, err, err2)
		}
		return
	}
	log.Printf("[asset-extract] 任务 %d 完成: status=%s 提取=%d AI确认=%d 新发现=%d 入库=%d",
		taskID, status, deterministic, aiConfirmed, aiNew, adopted)
}

// markAssetDecision 把明细列表里同候选的 decision 改写为 confirmed(找不到不新增)。
func markAssetDecision(details []assetDecisionDetail, c assetCandidate, decision string) {
	for i := range details {
		if details[i].Type == c.Type && details[i].Value == c.Value && details[i].Port == c.Port {
			details[i].Decision = decision
			return
		}
	}
}

// adoptAssetCandidate 采纳入库(契约 §3.4.2):upsert 正式资产 → 溯源(source=ai)
// → 暂存行落定 accepted。单条失败仅记日志,返回是否入库成功。
func (s *Server) adoptAssetCandidate(c assetCandidate, taskID, companyID int64, collector string) bool {
	as := s.assetStore()
	if as == nil {
		log.Printf("[asset-extract] 任务 %d 资产库不可用,无法采纳 %s %s", taskID, c.Type, c.Value)
		return false
	}
	assetID, err := s.adoptStagedAsset(as, taskID, candidateStaged(c, companyID, collector), companyID)
	if err != nil {
		log.Printf("[asset-extract] 任务 %d 采纳 %s %s 失败: %v", taskID, c.Type, c.Value, err)
		return false
	}
	if err := as.SetTaskAssetSource(taskID, assetID, "ai", "AI 识别入库 · "+c.Value, nil); err != nil {
		log.Printf("[asset-extract] 任务 %d 写资产溯源失败(%s): %v", taskID, c.Value, err)
	}
	if row, err := s.m.PG().FindStagedAsset(taskID, collector, c.Type, c.Value, c.Port); err == nil && row != nil {
		if _, err := s.m.PG().DecideStagedAsset(taskID, row.ID, "accepted"); err != nil {
			log.Printf("[asset-extract] 任务 %d 落定暂存 #%d 失败: %v", taskID, row.ID, err)
		}
	}
	return true
}

// ── 取数(契约 §3.2.1 / §3.3 输入) ──

// assetTextSegment 是一段参与扫描的过程文本及其来源 kind。
type assetTextSegment struct {
	Kind string
	Text string
}

// assetExtractSegments 取数:activity 摘要/详情(summary+detail 非空才参与)+
// 发现证据,总扫描字符 cap 500KB。全部容错,单查询失败仅记日志。
// 同时返回活动文本供 LLM 段摘录(新者在前)。
func (s *Server) assetExtractSegments(taskID int64) (segments []assetTextSegment, acts []db.TaskActivityText) {
	segments = []assetTextSegment{}
	acts = []db.TaskActivityText{}
	total := 0
	texts, err := s.m.PG().TaskActivityTexts(taskID, 600)
	if err != nil {
		log.Printf("[asset-extract] 任务 %d 取活动文本失败: %v", taskID, err)
	} else {
		acts = texts
		for _, t := range texts {
			for _, raw := range []string{t.Summary, t.Detail} {
				piece := strings.TrimSpace(raw)
				if piece == "" || total >= assetScanMaxBytes {
					continue
				}
				if total+len(piece) > assetScanMaxBytes {
					piece = piece[:assetScanMaxBytes-total]
				}
				segments = append(segments, assetTextSegment{Kind: t.Kind, Text: piece})
				total += len(piece)
			}
		}
	}
	evs, err := s.m.PG().TaskFindingEvidence(taskID, 100)
	if err != nil {
		log.Printf("[asset-extract] 任务 %d 取发现证据失败: %v", taskID, err)
	} else {
		for _, ev := range evs {
			if total >= assetScanMaxBytes {
				break
			}
			if total+len(ev) > assetScanMaxBytes {
				ev = ev[:assetScanMaxBytes-total]
			}
			segments = append(segments, assetTextSegment{Kind: "finding", Text: ev})
			total += len(ev)
		}
	}
	return segments, acts
}

// assetExtractCompanyOverride 企业归属覆盖(契约 §3.2.6):任务企业列表恰好 1 家时
// 返回该 id,否则 0(0=不强制,走 ICP/范围自动认领,与现有 adopt 行为一致)。
func (s *Server) assetExtractCompanyOverride(taskID int64) int64 {
	ids, err := s.m.PG().TaskCompanyIDs(taskID)
	if err != nil {
		log.Printf("[asset-extract] 任务 %d 取企业归属失败: %v", taskID, err)
		return 0
	}
	if len(ids) == 1 && ids[0] > 0 {
		return ids[0]
	}
	return 0
}

// assetTaskSkipKeys 取本任务已有资产做值比对集合(契约 §3.2.4)。失败返回空集合
// (宁多暂存不漏资产,暂存本身幂等)。
func (s *Server) assetTaskSkipKeys(taskID int64) map[string]bool {
	keys := map[string]bool{}
	as := s.assetStore()
	if as == nil {
		return keys
	}
	assets, err := as.QueryByTask(taskID, "", assetSkipScanAssets, 0)
	if err != nil {
		log.Printf("[asset-extract] 任务 %d 取已有资产失败: %v", taskID, err)
		return keys
	}
	return assetSkipKeySet(assets)
}

// ── LLM 段(契约 §3.3) ──

// assetExtractAIInput 是资产甄别 LLM 输出的原始 schema(契约 §3.3)。
type assetExtractAIInput struct {
	Confirmed []int                 `json:"confirmed"`
	Missing   []assetExtractMissing `json:"missing"`
	Note      string                `json:"note"`
}

// assetExtractMissing 是 LLM 声称「过程数据里有但候选清单漏掉」的一条资产。
type assetExtractMissing struct {
	Type  string `json:"type"`
	Value string `json:"value"`
	Port  int    `json:"port"`
	URL   string `json:"url"`
}

// assetExtractLLM 组 prompt → analyzeWithRetry(瞬态退避 1 次,单次预算 100s,
// ctx 总超时 240s,仿 memory_reflect)→ 围栏容错解析。
func (s *Server) assetExtractLLM(taskID int64, candidates []assetCandidate, acts []db.TaskActivityText) (*assetExtractAIInput, error) {
	prov, _, ok := s.globalProvider()
	if !ok {
		return nil, fmt.Errorf("LLM 未配置")
	}
	user := s.assetExtractUserPrompt(taskID, candidates, acts)
	ctx, cancel := context.WithTimeout(s.ctx, 240*time.Second)
	defer cancel()
	out, err := analyzeWithRetry(ctx, prov, assetExtractSystemPrompt(), user, 1, 100*time.Second)
	if err != nil {
		return nil, err
	}
	return parseAssetExtractJSON(out)
}

// assetExtractSystemPrompt 资产甄别系统提示词(契约 §3.3 要点)。
func assetExtractSystemPrompt() string {
	return "你是渗透测试任务的资产甄别助手。给你任务的目标、授权范围、从过程数据提取的资产候选清单与最近工具摘录,请判定哪些是真实测试资产。要求:\n" +
		"1. 只输出一个纯 JSON 对象,不要解释、不要 Markdown 代码围栏。\n" +
		"2. 只确认与任务目标/授权范围相关的真实基础设施:页面正文里引用的第三方域名、文档示例、包名、统计 SDK 域名一律不确认;拿不准就不确认(宁缺勿滥)。\n" +
		"3. missing 只收录过程数据里出现但候选清单漏掉的资产,不得凭空编造。\n" +
		"输出 schema:\n" +
		`{"confirmed":[0,2,5],"missing":[{"type":"domain|ip|service","value":"...","port":0,"url":""}],"note":"一句话依据"}` + "\n" +
		"confirmed=候选清单下标数组;missing.type 只能是 domain/ip/service,知道完整 URL 时填写 url 字段,port 无则填 0。没有就给空数组。"
}

// assetExtractUserPrompt 组装用户输入(契约 §3.3):任务元信息 + 授权范围摘要 +
// 候选清单(编号列表,每行「#i type value[:port] (来源段 kind)」)+ 最近 40 条
// tool_use 摘录(cap 40KB)。
func (s *Server) assetExtractUserPrompt(taskID int64, candidates []assetCandidate, acts []db.TaskActivityText) string {
	var b strings.Builder
	meta := ""
	if pt, err := s.m.PG().GetTask(taskID); err == nil && pt != nil {
		meta = fmt.Sprintf("任务 #%d(阶段 %s)\n目标:%s\n描述:%s\n", taskID, pt.Stage,
			truncateRunesHard(pt.Goal, 500), truncateRunesHard(pt.Description, 500))
	} else {
		meta = fmt.Sprintf("任务 #%d\n", taskID)
	}
	b.WriteString(meta)
	b.WriteString("\n== 授权范围摘要 ==\n")
	b.WriteString(s.assetScopeSummary(taskID))
	fmt.Fprintf(&b, "\n== 候选清单(共 %d 条) ==\n", len(candidates))
	if len(candidates) == 0 {
		b.WriteString("(无)\n")
	}
	for i, c := range candidates {
		v := c.Value
		if c.Port > 0 {
			v += ":" + strconv.Itoa(c.Port)
		}
		src := c.Source
		if src == "" {
			src = "unknown"
		}
		fmt.Fprintf(&b, "#%d %s %s (%s)\n", i, c.Type, v, src)
	}
	b.WriteString("\n== 最近过程摘录(tool_use) ==\n")
	excerpts := []string{}
	total := 0
	for _, a := range acts {
		if a.Kind != "tool_use" {
			continue
		}
		line := strings.TrimSpace(a.Summary)
		if line == "" {
			continue
		}
		line = "- " + truncateRunesHard(line, 300)
		if total+len(line) > assetPromptActsBytes || len(excerpts) >= assetPromptActsLimit {
			break
		}
		excerpts = append(excerpts, line)
		total += len(line)
	}
	if len(excerpts) == 0 {
		b.WriteString("(无)\n")
	} else {
		b.WriteString(strings.Join(excerpts, "\n") + "\n")
	}
	b.WriteString("\n请按系统提示词输出判定 JSON。")
	return b.String()
}

// assetScopeSummary 授权范围摘要(契约 §3.3):task_scope 域名/网段值 + 企业名;
// 取不到写「未登记」。全部容错。
func (s *Server) assetScopeSummary(taskID int64) string {
	lines := []string{}
	if pg := s.m.PG(); pg != nil {
		if ids, err := pg.TaskCompanyIDs(taskID); err == nil {
			for _, id := range ids {
				if c, err := pg.Companies().GetCompany(id); err == nil && c != nil && c.Name != "" {
					lines = append(lines, "企业: "+c.Name)
				}
			}
		}
	}
	if as := s.assetStore(); as != nil {
		if rows, err := as.ListTaskScope(taskID); err == nil {
			for _, r := range rows {
				switch r.Kind {
				case "root_domain", "subdomain":
					if r.Domain != "" {
						lines = append(lines, "域名("+r.Kind+"): "+r.Domain)
					}
				case "ip", "cidr":
					if r.Net != "" {
						lines = append(lines, "网段: "+r.Net)
					}
				case "icp", "keyword":
					if r.Value != "" {
						lines = append(lines, r.Kind+": "+r.Value)
					}
				}
			}
		} else {
			log.Printf("[asset-extract] 任务 %d 取授权范围失败: %v", taskID, err)
		}
	}
	if len(lines) == 0 {
		return "未登记\n"
	}
	return strings.Join(lines, "\n") + "\n"
}

// parseAssetExtractJSON 围栏容错解析(仿 parseMemoryReflectJSON):剥 ```json 围栏;
// 失败再取首 '{' 到末 '}' 子串;再失败返回错误(调用方置 partial)。
func parseAssetExtractJSON(raw string) (*assetExtractAIInput, error) {
	text := stripLLMJSONFence(raw)
	var in assetExtractAIInput
	if err := json.Unmarshal([]byte(text), &in); err != nil {
		if i := strings.Index(text, "{"); i >= 0 {
			if j := strings.LastIndex(text, "}"); j > i {
				if err2 := json.Unmarshal([]byte(text[i:j+1]), &in); err2 == nil {
					return sanitizeAssetExtractAI(&in), nil
				}
			}
		}
		return nil, err
	}
	return sanitizeAssetExtractAI(&in), nil
}

// sanitizeAssetExtractAI 净化 LLM 输出:confirmed 去重去负;missing 截 50、清洗
// 控制字符并归一小写;集合恒非 nil(红线:不得漏成 JSON null)。
func sanitizeAssetExtractAI(in *assetExtractAIInput) *assetExtractAIInput {
	out := &assetExtractAIInput{
		Confirmed: []int{},
		Missing:   []assetExtractMissing{},
		Note:      cleanMemoryText(in.Note, 200),
	}
	seen := map[int]bool{}
	for _, idx := range in.Confirmed {
		if idx < 0 || seen[idx] {
			continue
		}
		seen[idx] = true
		out.Confirmed = append(out.Confirmed, idx)
	}
	for _, m := range capList(in.Missing, assetMissingCap) {
		out.Missing = append(out.Missing, assetExtractMissing{
			Type:  strings.ToLower(cleanMemoryText(m.Type, 20)),
			Value: strings.ToLower(cleanMemoryText(m.Value, 253)),
			Port:  m.Port,
			URL:   cleanMemoryText(m.URL, 2048),
		})
	}
	return out
}

// ── 开关(契约 §3.3) ──

// assetExtractAIEnabled 读「AI 识别」开关:缺省开,仅显式 "false" 关。
func (s *Server) assetExtractAIEnabled() bool {
	if v, _, err := s.m.PG().GetSetting(settingAssetExtractAI); err == nil && v == "false" {
		return false
	}
	return true
}

// assetExtractSettingValue 布尔开关 → settings 存储值(纯函数)。
func assetExtractSettingValue(enabled bool) string {
	if enabled {
		return "true"
	}
	return "false"
}

// ── 纯函数(提取/过滤/去重/映射)——server/asset_extract_test.go 覆盖,不打 DB/LLM ──

// extractAssetCandidates 确定性提取(契约 §3.2,纯函数):email 预删 → URL →
// host:port → IPv4 → 裸 hostname 四路正则 → 合法性过滤 → run 内按 (type,value,port)
// 去重。前一路命中处替换成空格,防止后继正则在同一段文本里重复提取。集合恒非 nil。
func extractAssetCandidates(segments []assetTextSegment) []assetCandidate {
	out := []assetCandidate{}
	seen := map[string]bool{}
	add := func(c *assetCandidate) {
		if c == nil || c.Value == "" {
			return
		}
		key := assetCandidateKey(*c)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, *c)
	}
	for _, seg := range segments {
		text := stripAssetEmails(seg.Text)
		if strings.TrimSpace(text) == "" {
			continue
		}
		// ① URL → service(完整 URL 进 extra)。
		text = maskAssetMatches(text, reAssetURL, func(m string) {
			if c := assetCandidateFromURL(m); c != nil {
				c.Source = seg.Kind
				add(c)
			}
		})
		// ② host:port:域名走 service(采纳走 UpsertOtherService),IP 走 ip。
		text = maskAssetMatches(text, reAssetHostPort, func(m string) {
			host, port := splitAssetHostPort(m)
			if port <= 0 || port > 65535 {
				return
			}
			if validAssetIPv4(host) {
				if isBlockedAssetIPv4(host) {
					return
				}
				add(&assetCandidate{Type: "ip", Value: host, Port: port, Source: seg.Kind})
				return
			}
			if h := cleanAssetHostname(host); h != "" {
				add(&assetCandidate{Type: "service", Value: h, Port: port, Source: seg.Kind})
			}
		})
		// ③ IPv4[:port] 与裸 IPv4:内网保留,loopback/0.0.0.0/广播丢弃。
		text = maskAssetMatches(text, reAssetIPv4, func(m string) {
			ip, port := splitAssetHostPort(m)
			if !validAssetIPv4(ip) || isBlockedAssetIPv4(ip) {
				return
			}
			add(&assetCandidate{Type: "ip", Value: ip, Port: port, Source: seg.Kind})
		})
		// ④ 裸 hostname → domain。
		for _, m := range reAssetHost.FindAllString(text, -1) {
			if h := cleanAssetHostname(m); h != "" && !validAssetIPv4(h) {
				add(&assetCandidate{Type: "domain", Value: h, Source: seg.Kind})
			}
		}
	}
	return out
}

// assetCandidateKey run 内去重键(纯函数):(type,value,port)。
func assetCandidateKey(c assetCandidate) string {
	return c.Type + "|" + c.Value + "|" + strconv.Itoa(c.Port)
}

// maskAssetMatches 把正则命中逐段交给 fn,并把命中处替换成空格(纯函数,不改原串)。
func maskAssetMatches(text string, re *regexp.Regexp, fn func(string)) string {
	locs := re.FindAllStringIndex(text, -1)
	if locs == nil {
		return text
	}
	for _, loc := range locs {
		fn(text[loc[0]:loc[1]])
	}
	out := []byte(text)
	for _, loc := range locs {
		for i := loc[0]; i < loc[1]; i++ {
			out[i] = ' '
		}
	}
	return string(out)
}

// stripAssetEmails 先删 email(契约 §3.2.2),防邮箱右半被误提为域名。
func stripAssetEmails(s string) string { return reAssetEmail.ReplaceAllString(s, " ") }

// trimAssetURLTail 剥 URL 尾部句读(契约 §3.2.3 的 ASCII 集 + 常见 CJK 句读,
// URL 正则的排除类只覆盖 ASCII,CJK 文本里 URL 后紧跟「。」会一并吞进匹配)。
func trimAssetURLTail(u string) string {
	return strings.TrimRight(u, ".,;:)]}，。；：）」』】》")
}

// cutAssetHost 截掉主机里第一个非法字符起的尾部(URL 匹配可能吞进 CJK 字符)。
func cutAssetHost(h string) string {
	for i := 0; i < len(h); i++ {
		c := h[i]
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '-' || c == ':'
		if !ok {
			return h[:i]
		}
	}
	return h
}

// validAssetIPv4 校验点分四段且 octet≤255(正则只保证 1-3 位数字)。
func validAssetIPv4(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil
}

// isBlockedAssetIPv4 丢弃 loopback/0.0.0.0/定向广播(契约 §9);内网 IP 是渗透
// 场景的真实资产,保留。
func isBlockedAssetIPv4(s string) bool {
	ip := net.ParseIP(s).To4()
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsUnspecified() {
		return true
	}
	return ip[0] == 255 && ip[1] == 255 && ip[2] == 255 && ip[3] == 255
}

// cleanAssetHostname 归一小写、去尾点,校验非空且 ≤253 字符;非法返回空串。
func cleanAssetHostname(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimSuffix(h, ".")
	if h == "" || len(h) > 253 {
		return ""
	}
	return h
}

// splitAssetHostPort 拆 host:port(裸 host 亦可),port 非法/缺省为 0。
func splitAssetHostPort(s string) (string, int) {
	host, port := s, 0
	if h, p, err := net.SplitHostPort(s); err == nil {
		host = h
		if n, e := strconv.Atoi(p); e == nil {
			port = n
		}
	}
	return host, port
}

// assetCandidateFromURL 从一个 URL 匹配串提取 service 候选(契约 §3.2.5:
// extra.url=完整 URL,value=host,port=显式端口或 0);无效返回 nil。
func assetCandidateFromURL(raw string) *assetCandidate {
	u := trimAssetURLTail(raw)
	parsed, err := url.Parse(u)
	if err != nil || parsed.Hostname() == "" {
		return nil
	}
	host := cleanAssetHostname(cutAssetHost(parsed.Hostname()))
	if host == "" || !strings.Contains(host, ".") {
		return nil
	}
	port := 0
	if p := parsed.Port(); p != "" {
		if n, e := strconv.Atoi(p); e == nil {
			port = n
		}
	}
	return &assetCandidate{Type: "service", Value: host, Port: port, URL: u}
}

// assetSkipKeySet 从任务已有资产反查「值集合」(契约 §3.2.4,纯函数):
// root_domain/subdomain → 域名值;ip → IP 值;service → URL 原值 + host:port 组合 +
// host 本身。集合恒非 nil。
func assetSkipKeySet(assets []*db.Asset) map[string]bool {
	keys := map[string]bool{}
	for _, a := range assets {
		if a == nil {
			continue
		}
		switch a.Type {
		case "root_domain", "subdomain", "domain":
			if d := cleanAssetHostname(a.Domain); d != "" {
				keys["domain|"+d] = true
			}
		case "ip":
			if ip := strings.TrimSpace(a.IP); ip != "" {
				keys["ip|"+ip] = true
			}
		case "service":
			host := cleanAssetHostname(a.Domain)
			port := 0
			if a.Port != nil {
				port = *a.Port
			}
			if a.URL != "" {
				keys["url|"+a.URL] = true
				if u, err := url.Parse(a.URL); err == nil {
					if h := cleanAssetHostname(cutAssetHost(u.Hostname())); h != "" && host == "" {
						host = h
					}
					if p, e := strconv.Atoi(u.Port()); e == nil && port == 0 {
						port = p
					}
				}
			}
			if host != "" {
				keys["service|"+host+"|"+strconv.Itoa(port)] = true
				keys["servicehost|"+host] = true
			}
		}
	}
	return keys
}

// candidateSkipped 判断一条候选是否已是本任务资产(值比对,纯函数):
// domain/ip 按值精确比对;service 按 URL 原值或 host:port 比对(无显式端口的 URL
// 在该 host 已有任一 service 资产时也视为重复)。
func candidateSkipped(c assetCandidate, keys map[string]bool) bool {
	switch c.Type {
	case "domain":
		return keys["domain|"+c.Value]
	case "ip":
		return keys["ip|"+c.Value]
	case "service":
		if c.URL != "" && keys["url|"+c.URL] {
			return true
		}
		if keys["service|"+c.Value+"|"+strconv.Itoa(c.Port)] {
			return true
		}
		return c.Port == 0 && keys["servicehost|"+c.Value]
	}
	return false
}

// missingToCandidate 净化 LLM 新发现为候选(纯函数):类型白名单 + 值合法性校验,
// 非法返回 nil。service 无 URL 必须带端口(UpsertOtherService 的硬要求);IP:port
// 无 URL 按契约更贴 ip 类型(port 采纳时记进 open_ports)。
func missingToCandidate(m assetExtractMissing) *assetCandidate {
	value := strings.ToLower(strings.TrimSpace(m.Value))
	if value == "" {
		return nil
	}
	switch m.Type {
	case "domain":
		if validAssetIPv4(value) {
			return nil
		}
		h := cleanAssetHostname(cutAssetHost(value))
		if h == "" {
			return nil
		}
		return &assetCandidate{Type: "domain", Value: h}
	case "ip":
		if !validAssetIPv4(value) || isBlockedAssetIPv4(value) {
			return nil
		}
		return &assetCandidate{Type: "ip", Value: value, Port: m.Port}
	case "service":
		if m.URL != "" {
			if c := assetCandidateFromURL(m.URL); c != nil {
				return c
			}
		}
		if m.Port <= 0 || m.Port > 65535 {
			return nil
		}
		if validAssetIPv4(value) {
			if isBlockedAssetIPv4(value) {
				return nil
			}
			return &assetCandidate{Type: "ip", Value: value, Port: m.Port}
		}
		h := cleanAssetHostname(cutAssetHost(value))
		if h == "" {
			return nil
		}
		return &assetCandidate{Type: "service", Value: h, Port: m.Port}
	}
	return nil
}

// candidateStaged 候选 → 暂存行(契约 §3.2.5 类型映射)。service 无 URL 时补
// service_name=tcp:UpsertOtherService 硬性要求 service_name,而 host:port 文本本身
// 只能证明这是一个 TCP 端口,不虚构更具体的服务名。TaskID/BatchID 由调用方填。
func candidateStaged(c assetCandidate, companyID int64, collector string) *db.StagedAsset {
	sa := &db.StagedAsset{
		Collector: collector,
		Type:      c.Type,
		Value:     c.Value,
		Port:      c.Port,
		Extra:     candidateExtra(c),
	}
	if companyID > 0 {
		id := companyID
		sa.CompanyID = &id
	}
	return sa
}

// candidateExtra 候选的 extra 载荷(纯函数):URL 来源带 url;无 URL 的 service 补
// service_name=tcp。
func candidateExtra(c assetCandidate) json.RawMessage {
	extra := map[string]any{}
	if c.URL != "" {
		extra["url"] = c.URL
	}
	if c.Type == "service" && c.URL == "" {
		extra["service_name"] = "tcp"
	}
	b, err := json.Marshal(extra)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

// assetDetailJSON 序列化候选决策明细(cap 300,契约 §3.5);失败回退空数组。
func assetDetailJSON(details []assetDecisionDetail) json.RawMessage {
	if len(details) > assetDetailCap {
		details = details[:assetDetailCap]
	}
	if details == nil {
		details = []assetDecisionDetail{}
	}
	b, err := json.Marshal(details)
	if err != nil {
		return json.RawMessage("[]")
	}
	return b
}
