package server

// SRC 专属 agent(src_operator)的三个工具:src_get_context / src_score_finding /
// src_draft_submission。工具只做【确定性取数与拼装】——平台规范、目标授权范围、
// 资产清单、评分/奖励标准都来自 db 层现成函数,不打 LLM、不落库;评级与写报告
// 的「判断」留给模型按返回的标准完成。
//
// 注册链路(与 traffic/orchestration host 工具同机制):
//  1. seedSRCOperatorTools 把 agent.SRCToolSeeds() 的声明 seed 进 tools 表
//     (key upsert、默认绑 src_operator/pentest/worker、stage=exploit);
//  2. 同一把手把 s.srcOperatorTools() 的执行实例追加进 agent.ToolAugment 的
//     host 工具出口(orchestration.go 的 hostTools 列表不归本任务改,故在此包装);
//  3. 运行时 ToolResolve 按 tools 表绑定逐 agent 过滤——只有绑定了的 agent
//     (src_operator/pentest/worker 及用户在 UI 改过的绑定)真能看到并调用。
//
// 任务归属:任务内运行 RunInfo 自带 TaskID,工具自动取当前任务(参考
// agentFindingTrafficAccess);对话页运行(RunInfo 无 TaskID)允许显式传 task_id。
// 非 SRC 任务一律返回错误文本。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// srcToolsWired 防止 ToolAugment 被重复包装(测试进程里 New 可能跑多次,
// 每次都包一层会让工具重复出现在 base 列表)。
var srcToolsWired bool

// seedSRCOperatorTools 是 server.New() 的唯一入口:种子 + 执行注册,幂等。
func (s *Server) seedSRCOperatorTools() {
	if s.m == nil || s.m.PG() == nil {
		return
	}
	// 1) tools 表按 key 播种(ON CONFLICT DO NOTHING:用户在 UI 改过的描述/绑定不被覆盖)。
	for _, seed := range agent.SRCToolSeeds() {
		schema, _ := json.Marshal(seed.Schema)
		agents, _ := json.Marshal(seed.Agents)
		if err := s.m.PG().SeedToolWithStage(seed.Key, seed.Desc, schema, agents, seed.Stage); err != nil {
			log.Printf("[src-tools] seed %s 失败: %v", seed.Key, err)
			continue
		}
		// 后加 stage 列的老库:给存量行回填归属(仅空值)。
		if err := s.m.PG().BackfillToolStage(seed.Key, seed.Stage); err != nil {
			log.Printf("[src-tools] 回填 %s stage 失败: %v", seed.Key, err)
		}
	}
	// 2) 执行函数注册:把 3 个真工具追加进 agent.ToolAugment 的 host 工具出口,
	//    之后由 ToolResolve 按 tools 表绑定过滤——这正是 hostTools() 的行为,
	//    只是注入点收敛在本文件(orchestration.go 的 hostTools 列表不归本任务改)。
	if srcToolsWired {
		return
	}
	srcToolsWired = true
	prev := agent.ToolAugment
	tools := s.srcOperatorTools()
	agent.ToolAugment = func(ctx context.Context, agentKey string) ([]actool.CoreTool, agent.DeferredInfo, func()) {
		extra, def, cleanup := []actool.CoreTool(nil), agent.DeferredInfo{}, func() {}
		if prev != nil {
			extra, def, cleanup = prev(ctx, agentKey)
		}
		extra = append(extra, tools...)
		return extra, def, cleanup
	}
}

// srcOperatorTools 构造三个工具实例:描述/schema 取自 agent.SRCToolSeeds()(单一
// 事实来源,与 tools 表播的种一致),执行函数在下方。三个都是只读工具:不写图、
// 不写库(草稿只返回文本)。
func (s *Server) srcOperatorTools() []actool.CoreTool {
	seedBy := map[string]agent.ToolSeed{}
	for _, sd := range agent.SRCToolSeeds() {
		seedBy[sd.Key] = sd
	}
	get := func(key string) (string, map[string]any) { return seedBy[key].Desc, seedBy[key].Schema }
	descCtx, schemaCtx := get("src_get_context")
	descScore, schemaScore := get("src_score_finding")
	descDraft, schemaDraft := get("src_draft_submission")
	return []actool.CoreTool{
		roTool("src_get_context", descCtx, schemaCtx, s.runSRCGetContext),
		roTool("src_score_finding", descScore, schemaScore, s.runSRCScoreFinding),
		roTool("src_draft_submission", descDraft, schemaDraft, s.runSRCDraftSubmission),
	}
}

// srcTaskCtx 是三个工具共用的「当前 SRC 任务上下文」:任务 + 平台 + 目标挂链。
type srcTaskCtx struct {
	task     *Task
	platform *db.SRCPlatform
	link     *db.SRCTargetLink // 可为 nil:目标未挂到该平台下(授权范围未设置)
}

// resolveSRCTask 解析工具调用归属的任务:显式 task_id 优先,否则用运行上下文里的
// RunInfo(任务内的 worker/planner/mainagent 运行自带)。非 SRC 任务返回固定错误
// 文本——三个工具只对 src_mode 任务有意义。
func (s *Server) resolveSRCTask(ctx context.Context, rawTaskID json.RawMessage) (*Task, string) {
	id := parseProfileID(rawTaskID)
	if id <= 0 {
		if ri := agent.RunInfoFrom(ctx); ri.TaskID > 0 {
			id = ri.TaskID
		}
	}
	if id <= 0 {
		return nil, "无法确定当前任务:请在 SRC 任务内调用(自动取当前任务),或显式传 task_id"
	}
	task := s.m.ResolveTask(strconv.FormatInt(id, 10))
	if task == nil {
		return nil, "任务不存在: #" + strconv.FormatInt(id, 10)
	}
	if !task.SRCMode {
		return nil, "该任务不是 SRC 模式:src_* 工具只对 SRC 任务有意义"
	}
	return task, ""
}

// loadSRCContext 在任务之上补齐平台与「平台×目标」授权范围。平台缺失(已删/未配)
// 返回错误文本;挂链缺失降级为 link=nil(授权范围按未设置渲染)。
func (s *Server) loadSRCContext(task *Task) (*srcTaskCtx, string) {
	p, err := s.m.pg.GetSRCPlatform(task.SRCPlatformID)
	if err != nil {
		return nil, "SRC 平台读取失败: " + err.Error()
	}
	if p == nil {
		return nil, fmt.Sprintf("未找到 SRC 平台 #%d(可能已删除),请先在任务上配置有效的 SRC 平台", task.SRCPlatformID)
	}
	link, err := s.m.pg.GetSRCTargetLink(task.SRCPlatformID, task.SRCTargetID)
	if err != nil {
		return nil, "SRC 目标授权范围读取失败: " + err.Error()
	}
	return &srcTaskCtx{task: task, platform: p, link: link}, ""
}

// scopeMap 把「平台×目标」授权范围渲染成结构化对象(link 为 nil 时按未设置降级)。
func scopeMap(c *srcTaskCtx) map[string]any {
	if c.link == nil {
		return map[string]any{"configured": false,
			"note": "该目标未在此平台下设置授权范围(scope_note/forbidden 等为空);不确定边界时不要动作,先向用户确认"}
	}
	return map[string]any{
		"configured":    true,
		"enabled":       c.link.Enabled,
		"scope_note":    c.link.ScopeNote,
		"allowed_types": c.link.AllowedTypes,
		"rate_limit":    c.link.RateLimit,
		"forbidden":     c.link.Forbidden,
		"rules":         c.link.Rules,
	}
}

// runSRCGetContext 返回平台四类规范全文 + 目标授权范围 + 资产清单摘要(≤100 条)。
func (s *Server) runSRCGetContext(ctx context.Context, in json.RawMessage) (actool.Result, error) {
	var a struct {
		TaskID json.RawMessage `json:"task_id"`
	}
	_ = json.Unmarshal(in, &a)
	task, errText := s.resolveSRCTask(ctx, a.TaskID)
	if errText != "" {
		return actool.Errorf(errText), nil
	}
	c, errText := s.loadSRCContext(task)
	if errText != "" {
		return actool.Errorf(errText), nil
	}
	// 资产清单摘要:类型+值,最多 100 条(控上下文体积;total 如实标注)。
	assets, total, counts, err := s.m.pg.ListSRCTargetAssets(task.SRCTargetID, db.SRCTargetAssetFilter{
		PlatformID: task.SRCPlatformID, Limit: 100,
	})
	if err != nil {
		return actool.Errorf("目标资产清单读取失败: " + err.Error()), nil
	}
	list := make([]map[string]any, 0, len(assets))
	for _, x := range assets {
		list = append(list, map[string]any{"type": x.Type, "value": x.Value})
	}
	// 现成的「平台规范+授权范围」注入文本,模型可直接当作边界提示复述。
	constraintText, _ := s.m.pg.SrcTargetConstraintText(task.SRCPlatformID, task.SRCTargetID)
	return jsonResult(map[string]any{
		"task_id": task.ID,
		"platform": map[string]any{
			"id":               c.platform.ID,
			"name":             c.platform.Name,
			"homepage":         c.platform.Homepage,
			"reward_standard":  c.platform.RewardStandard,
			"scoring_standard": c.platform.ScoringStandard,
			"code_of_conduct":  c.platform.CodeOfConduct,
			"rules":            c.platform.Rules,
		},
		"target":           targetRef(c),
		"scope":            scopeMap(c),
		"constraint_text":  constraintText,
		"assets_total":     total,
		"assets_by_type":   counts,
		"assets":           list,
		"assets_truncated": total > len(list),
		"hint":             "以上即本任务的授权边界:task_constraints 是最高优先级,范围外资产一律记 out-of-scope 并跳过;禁止事项任何理由都不例外",
	})
}

// runSRCScoreFinding 返回评分标准+奖励标准+目标范围,要求模型据此自评。只取数不评定。
func (s *Server) runSRCScoreFinding(ctx context.Context, in json.RawMessage) (actool.Result, error) {
	var a struct {
		Title       string          `json:"title"`
		Description string          `json:"description"`
		Severity    string          `json:"severity"`
		TaskID      json.RawMessage `json:"task_id"`
	}
	_ = json.Unmarshal(in, &a)
	if strings.TrimSpace(a.Title) == "" || strings.TrimSpace(a.Description) == "" {
		return actool.Errorf("title 与 description 必填:请描述发现是什么、在哪里、能造成什么"), nil
	}
	task, errText := s.resolveSRCTask(ctx, a.TaskID)
	if errText != "" {
		return actool.Errorf(errText), nil
	}
	c, errText := s.loadSRCContext(task)
	if errText != "" {
		return actool.Errorf(errText), nil
	}
	return jsonResult(map[string]any{
		"task_id":          task.ID,
		"platform":         c.platform.Name,
		"candidate":        map[string]any{"title": a.Title, "description": a.Description, "severity_guess": a.Severity},
		"scoring_standard": c.platform.ScoringStandard,
		"reward_standard":  c.platform.RewardStandard,
		"target":           targetRef(c),
		"scope":            scopeMap(c),
		"action_required":  "请依据以上评分标准给出该发现的等级与理由;再对照奖励标准判断该方向是否值得深入。平台标准缺失时如实说明无法按平台评定,不要套用通用分级充数",
	})
}

// runSRCDraftSubmission 取 finding + 平台规范/奖励标准 + 目标范围,确定性拼装提交
// 草稿 markdown 返回(不落库)。finding 不存在返回工具错误文本。
func (s *Server) runSRCDraftSubmission(ctx context.Context, in json.RawMessage) (actool.Result, error) {
	var a struct {
		FindingID json.RawMessage `json:"finding_id"`
		TaskID    json.RawMessage `json:"task_id"`
	}
	_ = json.Unmarshal(in, &a)
	fid := parseProfileID(a.FindingID)
	if fid <= 0 {
		return actool.Errorf("finding_id 必填:使用独立漏洞记录 ID(list_findings / report_finding 返回的 finding_id)"), nil
	}
	task, errText := s.resolveSRCTask(ctx, a.TaskID)
	if errText != "" {
		return actool.Errorf(errText), nil
	}
	f, err := s.m.pg.GetFinding(fid)
	if err != nil {
		return actool.Errorf("漏洞读取失败: " + err.Error()), nil
	}
	if f == nil {
		return actool.Errorf(fmt.Sprintf("漏洞不存在: finding_id=%d(先用 report_finding 登记,或用 list_findings 查正确 id)", fid)), nil
	}
	// 归属校验:只允许为本任务的(含继承来源任务的)漏洞起草,防止跨任务拼草稿。
	if _, _, allowed := findingProvenanceInTask(task, f.TaskID); !allowed {
		return actool.Errorf("该漏洞不属于当前任务,不能作为本任务的 SRC 提交草稿来源"), nil
	}
	c, errText := s.loadSRCContext(task)
	if errText != "" {
		return actool.Errorf(errText), nil
	}
	return actool.Text(srcDraftMarkdown(s.m.Assets(), c, f)), nil
}

// targetRef 任务目标的简述(目标可能已被删除,名称降级为 id)。
func targetRef(c *srcTaskCtx) map[string]any {
	out := map[string]any{"id": c.task.SRCTargetID}
	if c.link != nil && c.link.TargetName != "" {
		out["name"] = c.link.TargetName
	}
	return out
}

// maxDraftSection 草稿单节正文上限(控返回体积;完整内容仍在漏洞记录里)。
const maxDraftSection = 6000

// clipSection 截断超长正文并注明截断。
func clipSection(s string) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= maxDraftSection {
		return s
	}
	return string(runes[:maxDraftSection]) + "\n\n(……已截断,完整内容见漏洞记录)"
}

// srcDraftMarkdown 拼装提交草稿:标题/漏洞详情/影响/复现/资产归属+平台规范要点。
// 只引用库里的真实字段,不编造内容;平台没配的段落如实标注。as 可为 nil(资产段降级)。
func srcDraftMarkdown(as *db.AssetStore, c *srcTaskCtx, f *db.DBFinding) string {
	title := strings.TrimSpace(f.Name)
	if title == "" {
		title = strings.TrimSpace(f.VulnClass)
	}
	if title == "" {
		title = "未命名漏洞"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# SRC 提交草稿:%s\n\n", title)
	fmt.Fprintf(&b, "- 提交平台:%s", c.platform.Name)
	if c.platform.Homepage != "" {
		fmt.Fprintf(&b, "(%s)", c.platform.Homepage)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "- 测试目标:%s\n", draftTargetName(c))
	fmt.Fprintf(&b, "- 漏洞类型:%s\n", orDash(f.VulnClass))
	fmt.Fprintf(&b, "- 建议等级:%s(最终等级请按平台评分标准核定)\n\n", orDash(strings.ToUpper(f.Severity)))

	b.WriteString("## 漏洞详情\n\n")
	// 详细报告优先(报告 agent 已写的成品),退回摘要;都没有就明确请模型补写。
	detail := strings.TrimSpace(f.Report)
	if detail == "" {
		detail = strings.TrimSpace(f.Summary)
	}
	if detail == "" {
		detail = "(漏洞记录未填写详情,请补写:漏洞成因、位置、能造成什么)"
	}
	b.WriteString(clipSection(detail))
	b.WriteString("\n\n")

	b.WriteString("## 影响\n\n")
	if strings.TrimSpace(f.Severity) != "" {
		fmt.Fprintf(&b, "- 建议等级:%s\n", strings.ToUpper(f.Severity))
	}
	if sum := strings.TrimSpace(f.Summary); sum != "" && sum != strings.TrimSpace(f.Report) {
		b.WriteString(clipSection(sum))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	b.WriteString("## 复现步骤\n\n")
	if ev := strings.TrimSpace(f.Evidence); ev != "" {
		b.WriteString("```\n")
		b.WriteString(clipSection(ev))
		b.WriteString("\n```\n")
	} else {
		b.WriteString("(漏洞记录未附带证据,请补写可复现的分步操作与请求/命令)\n")
	}
	b.WriteString("\n")

	b.WriteString("## 资产归属\n\n")
	fmt.Fprintf(&b, "- 目标:%s\n", draftTargetName(c))
	if scope := scopeMap(c); scope["configured"] == true {
		if note, _ := scope["scope_note"].(string); strings.TrimSpace(note) != "" {
			fmt.Fprintf(&b, "- 授权范围:%s\n", clipSection(note))
		}
	}
	for _, line := range draftAssetLines(as, f) {
		fmt.Fprintf(&b, "- %s\n", line)
	}
	b.WriteString("\n")

	b.WriteString("## 提交前核对(平台要求)\n\n")
	b.WriteString(clipSection(draftChecklist(c)))
	return b.String()
}

// draftTargetName 草稿用目标名(link 里带名字;丢了就退回 id)。
func draftTargetName(c *srcTaskCtx) string {
	if c.link != nil && c.link.TargetName != "" {
		return c.link.TargetName
	}
	return "目标 #" + strconv.FormatInt(c.task.SRCTargetID, 10)
}

// draftAssetLines 把 finding 关联的资产解析成「类型:值」行,取值优先级复用
// srcAssetDisplayValue(与 db 层 srcTargetAssetValueExpr 一致)。资产库缺失或
// 全部已删时如实降级,不编造资产。
func draftAssetLines(as *db.AssetStore, f *db.DBFinding) []string {
	if len(f.AssetIDs) == 0 || as == nil {
		return []string{"漏洞记录未关联资产;请按平台要求补充受影响资产(URL/域名/IP)"}
	}
	assets, err := as.GetByIDs(f.AssetIDs)
	if err != nil {
		return []string{"资产信息读取失败(" + err.Error() + "),请按平台要求手动补充受影响资产"}
	}
	out := []string{}
	for _, a := range assets {
		if a == nil {
			continue
		}
		val := srcAssetDisplayValue(a)
		if val == "" {
			val = "#" + strconv.FormatInt(a.ID, 10)
		}
		out = append(out, fmt.Sprintf("%s: %s", strings.TrimSpace(a.Type), val))
	}
	if len(out) == 0 {
		out = append(out, "关联资产已不存在;请按平台要求手动补充受影响资产")
	}
	return out
}

// draftChecklist 平台规范要点:四类规范里非空的段落 + 目标禁止事项提醒。
// 缺失的类别如实标注「平台未提供」,提示人工核对而不是留白。
func draftChecklist(c *srcTaskCtx) string {
	var b strings.Builder
	write := func(label, body string) {
		if strings.TrimSpace(body) == "" {
			fmt.Fprintf(&b, "- %s:平台未提供,提交前请到平台页核对\n", label)
			return
		}
		fmt.Fprintf(&b, "- %s:\n%s\n", label, clipSection(body))
	}
	write("奖励标准", c.platform.RewardStandard)
	write("评分标准", c.platform.ScoringStandard)
	write("守则", c.platform.CodeOfConduct)
	write("平台规范", c.platform.Rules)
	if c.link != nil && strings.TrimSpace(c.link.Forbidden) != "" {
		fmt.Fprintf(&b, "- 目标禁止事项(提交内容不得包含越界测试的证据):\n%s\n", clipSection(c.link.Forbidden))
	}
	b.WriteString("- 本草稿由平台按「平台规范+目标授权范围」确定性拼装,提交前请人工核对;平台要求的验证环境/时间等字段请按平台表单补充\n")
	return b.String()
}

// orDash 空串降级为占位符,草稿不出现空标题行。
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return strings.TrimSpace(s)
}
