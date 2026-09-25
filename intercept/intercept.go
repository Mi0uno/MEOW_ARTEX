// Package intercept implements the user-configurable tool-call interception layer.
// Rules are loaded from the database, cached in memory, and evaluated in priority
// order (highest first) on every PreToolUse event. Five actions are supported:
//
//   - allow: immediately permits the call, skipping lower-priority rules.
//   - deny:  blocks the call and returns a message to the model.
//   - ask:   blocks the call, creates an intercept_pending record, writes an
//     activity to the active conversation, then waits for the user to approve or
//     deny via the /api/intercept/pending/{id}/decide endpoint.
//   - flag:  判疑放通 — permits the call like allow, but records a "flagged" audit
//     row so a suspicious-but-not-blocking verdict stays reviewable afterwards.
//     It never blocks and never waits for a human (that would make it deny/ask).
//
// Tool calls that match no rule go through Judge: the pre-decision engine
// (JEV/Laya, own switch) first, then the LLM fallback judge (llm_judge_enabled).
//
// The timeout behaviour is configurable at runtime via SetTimeoutConfig.
package intercept

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
)

// ctxKey is the unexported context key type to avoid collisions.
type ctxKey int

// ConvIDKey stores the active conversation ID in a context.Context so the
// interceptor can associate "ask" pending records with the right conversation.
const ConvIDKey ctxKey = 0

// WithConvID returns a child context carrying convID.
func WithConvID(ctx context.Context, convID int64) context.Context {
	return context.WithValue(ctx, ConvIDKey, convID)
}

// ConvIDFromContext extracts the conversation ID (0 if absent).
func ConvIDFromContext(ctx context.Context) int64 {
	v, _ := ctx.Value(ConvIDKey).(int64)
	return v
}

// taskCtxKey is a separate unexported key type for task context values.
type taskCtxKey int

const (
	taskInfoCtxKey taskCtxKey = 1
	taskEmitCtxKey taskCtxKey = 2
)

type taskCtxInfo struct{ taskID, agentName string }

// WithTaskContext injects task metadata and an emit function into ctx so that
// HandleAsk can tag pending records and write intercept_request activities to
// the task's exploration stream (making them appear inline in session transcripts).
func WithTaskContext(ctx context.Context, taskID, agentName string, emit func(db.Activity)) context.Context {
	ctx = context.WithValue(ctx, taskInfoCtxKey, taskCtxInfo{taskID, agentName})
	if emit != nil {
		ctx = context.WithValue(ctx, taskEmitCtxKey, emit)
	}
	return ctx
}

func taskInfoFromCtx(ctx context.Context) (taskID, agentName string) {
	if v, ok := ctx.Value(taskInfoCtxKey).(taskCtxInfo); ok {
		return v.taskID, v.agentName
	}
	return "", ""
}

func taskEmitFromCtx(ctx context.Context) func(db.Activity) {
	f, _ := ctx.Value(taskEmitCtxKey).(func(db.Activity))
	return f
}

// compiledRule is an InterceptRule with the regex pre-compiled (nil for string rules).
type compiledRule struct {
	db.InterceptRule
	re *regexp.Regexp
}

// pendingManager tracks in-flight "ask" requests via per-request channels.
type pendingManager struct {
	mu sync.Mutex
	ch map[int64]chan bool
}

func newPendingManager() *pendingManager { return &pendingManager{ch: map[int64]chan bool{}} }

func (p *pendingManager) add(id int64) chan bool {
	ch := make(chan bool, 1)
	p.mu.Lock()
	p.ch[id] = ch
	p.mu.Unlock()
	return ch
}

func (p *pendingManager) resolve(id int64, allowed bool) {
	p.mu.Lock()
	ch, ok := p.ch[id]
	delete(p.ch, id)
	p.mu.Unlock()
	if ok {
		ch <- allowed
	}
}

func (p *pendingManager) remove(id int64) {
	p.mu.Lock()
	delete(p.ch, id)
	p.mu.Unlock()
}

// Reviewer runs the LLM fallback judge for one tool call and returns its verdict
// as a Decision (Action ∈ allow|ask|deny; empty Action means the reply could not
// be parsed). It is injected by the server layer so the intercept package stays
// free of any llm dependency. profileID == 0 means "use the active/default profile".
type Reviewer func(ctx context.Context, profileID int64, prompt string, input ReviewInput) (Decision, error)

// DecisionEngineInput 是喂给前置决策引擎的一次调用快照。它刻意带上与 LLM 裁判
// 同等量的上下文:引擎在链路上更靠前,信息更少会让它对"看起来可疑"的授权渗透动作
// 先行拦截,而后一层的裁判根本没机会纠正(实测:只给工具名+参数时,JEV 把
// HTTP 请求走私的只读探测脚本、token 刷新脚本判成高风险)。
type DecisionEngineInput struct {
	Tool      string          `json:"tool_name"`
	Arguments json.RawMessage `json:"arguments"`
	// WorkingDir 是本次运行的工作目录,帮助引擎区分"攻击者产物/临时脚本"与"目标生产资产"。
	WorkingDir string `json:"working_directory,omitempty"`
	// Background 是显式绑定的人类消息(仅 user_message 来源),让引擎知道
	// "用户要求做什么",而不是只看单次调用。
	Background string `json:"background,omitempty"`
	// TaskID / AgentName 标明这次调用属于哪个任务的哪一步;空 = 无归属的裸调用。
	TaskID    string `json:"task_id,omitempty"`
	AgentName string `json:"agent_name,omitempty"`
}

// DecisionEngine is the callback for a pre-decision engine (JEV/Laya): a fast
// structured classification that runs BEFORE the LLM fallback judge on calls no
// rule matched. Returning (Decision, true) means it reached a terminal verdict.
//
// It is parallel to — and independent of — Reviewer: the engine has its own
// enable switch (jev_guard_enabled). Wiring it inside the Reviewer closure (the
// previous design) silently bound it to llm_judge_enabled, so with the LLM judge
// off the engine never ran at all, despite its own switch being on and its
// credentials configured. Keeping the two callbacks separate is what makes the
// engine's own switch authoritative.
type DecisionEngine func(ctx context.Context, in DecisionEngineInput) (Decision, bool)

// TaskGuardResolver resolves a task's own guard configuration, so a task can
// override the global posture. mode is one of:
//
//	""  / "inherit" — follow the global configuration (the default posture);
//	"off"           — this task runs no interception at all;
//	"enforce"       — run interception regardless of any global switch.
//
// ruleIDs narrows matching to those rule ids; empty means "all enabled rules".
// ok=false means no configuration was found for that task → treat as inherit.
//
// A nil resolver means "no task-level overrides exist": every task follows the
// global posture, exactly as before this hook was introduced. That default is
// deliberate — a missing/never-injected resolver must never widen permissions.
type TaskGuardResolver func(taskID string) (mode string, ruleIDs []int64, ok bool)

// Interceptor loads intercept rules from the database and evaluates them on
// tool calls. It is safe for concurrent use.
type Interceptor struct {
	db           *db.DB
	mu           sync.RWMutex
	cached       []compiledRule  // sorted by priority DESC; nil means not loaded yet
	enabledTools map[string]bool // nil means not loaded yet
	pending      *pendingManager
	reviewer     Reviewer       // nil = LLM fallback judge not wired
	engine       DecisionEngine // nil = pre-decision engine (JEV/Laya) not wired
	guardRes     TaskGuardResolver
	// srcRes 判断一个任务是否 SRC 模式,是 scope='src' 规则的适用条件。nil 时这些
	// 规则一律不生效——这是新增维度,缺解析器时按"非 SRC"跳过(宁漏勿误),绝不能
	// 反向把 SRC 规则放宽到全部任务。
	srcRes func(taskID string) bool
}

// SetTaskGuardResolver installs the task-level guard resolver. Passing nil
// disables task-level overrides (every task follows the global posture).
func (i *Interceptor) SetTaskGuardResolver(r TaskGuardResolver) {
	i.mu.Lock()
	i.guardRes = r
	i.mu.Unlock()
}

// SetTaskSRCResolver installs the "is this task SRC-mode" callback used to apply
// rules with scope='src' only to SRC tasks. Passing nil keeps every such rule
// inert: an unwired resolver must fail closed (skip the rule), never widen
// SRC-scoped rules to all tasks.
func (i *Interceptor) SetTaskSRCResolver(r func(taskID string) bool) {
	i.mu.Lock()
	i.srcRes = r
	i.mu.Unlock()
}

// resolveTaskGuard resolves the task-level guard configuration for the task in
// ctx. off is true only for an explicit mode "off"; ruleIDs is nil when there is
// no restriction.
//
// 关键:只有明确的 "off" 才是否决。resolver 未注入、ctx 里没有 taskID、resolver
// 查不到该任务(ok=false)、mode 是 ""/ "inherit" —— 这四种情况一律返回"不否决、
// 不限定",即与引入本钩子之前完全一致。安全控制不能因为"没查到配置"而悄悄放通,
// 所以这里的默认值必须是"照常监管"而不是"跳过监管"。
//
// "enforce" 不需要在这里特判:它要求"即使全局关也照常跑",而监管链路上并不存在
// 一个会整体关闭它的全局开关(工具范围 IsToolEnabled、两层模型开关都是各自独立
// 的),所以 enforce 与 inherit 在当前实现下是同一路径。
func (i *Interceptor) resolveTaskGuard(ctx context.Context) (off bool, ruleIDs []int64) {
	taskID, _ := taskInfoFromCtx(ctx)
	if taskID == "" {
		return false, nil
	}
	i.mu.RLock()
	r := i.guardRes
	i.mu.RUnlock()
	if r == nil {
		return false, nil
	}
	mode, ids, ok := r(taskID)
	if !ok {
		return false, nil
	}
	if mode == "off" {
		return true, nil
	}
	if len(ids) == 0 {
		return false, nil
	}
	return false, ids
}

// SetReviewer installs the LLM fallback judge callback. Passing nil disables it.
func (i *Interceptor) SetReviewer(r Reviewer) {
	i.mu.Lock()
	i.reviewer = r
	i.mu.Unlock()
}

// SetDecisionEngine installs the pre-decision engine callback (JEV/Laya). Passing
// nil disables it. Its enable switch lives in the callback itself (e.g.
// jev_guard_enabled), so it keeps running even when the LLM judge is turned off.
func (i *Interceptor) SetDecisionEngine(de DecisionEngine) {
	i.mu.Lock()
	i.engine = de
	i.mu.Unlock()
}

// defaultEnabledTools is the hard-coded set of tools that enter the intercept
// rule system when no intercept_enabled_tools setting has been saved.
var defaultEnabledTools = []string{
	"Bash", "WebFetch", "web_search",
	"shell_open", "shell_send",
	"Write", "Edit", "MultiEdit",
}

// New creates an Interceptor backed by d. The rule cache is lazy-loaded on
// first use.
func New(d *db.DB) *Interceptor {
	return &Interceptor{db: d, pending: newPendingManager()}
}

// Invalidate clears the in-memory rule cache and the enabled-tools cache.
// The next call to Match or IsToolEnabled will reload from the database.
// Call this after any CRUD operation on rules or tool config.
func (i *Interceptor) Invalidate() {
	i.mu.Lock()
	i.cached = nil
	i.enabledTools = nil
	i.mu.Unlock()
}

func (i *Interceptor) loadLocked() error {
	rules, err := i.db.ListInterceptRules()
	if err != nil {
		return err
	}
	var out []compiledRule
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		cr := compiledRule{InterceptRule: r}
		if r.MatchType == "regex" {
			re, err := regexp.Compile(r.Pattern)
			if err != nil {
				continue // skip rules with bad regex rather than crashing
			}
			cr.re = re
		}
		out = append(out, cr)
	}
	i.cached = out

	// Load enabled-tools set from settings, falling back to hard-coded defaults.
	val, ok, _ := i.db.GetSetting("intercept_enabled_tools")
	if !ok {
		m := make(map[string]bool, len(defaultEnabledTools))
		for _, n := range defaultEnabledTools {
			m[n] = true
		}
		i.enabledTools = m
	} else {
		var names []string
		if json.Unmarshal([]byte(val), &names) != nil {
			i.enabledTools = map[string]bool{}
		} else {
			m := make(map[string]bool, len(names))
			for _, n := range names {
				m[n] = true
			}
			i.enabledTools = m
		}
	}
	return nil
}

func (i *Interceptor) rules() ([]compiledRule, error) {
	i.mu.RLock()
	if i.cached != nil {
		out := i.cached
		i.mu.RUnlock()
		return out, nil
	}
	i.mu.RUnlock()

	i.mu.Lock()
	defer i.mu.Unlock()
	if i.cached != nil {
		return i.cached, nil
	}
	if err := i.loadLocked(); err != nil {
		return nil, err
	}
	return i.cached, nil
}

// IsToolEnabled returns true if the named tool is in the intercept-enabled set
// (i.e. it should enter the rule-matching path). Uses the same double-check lock
// pattern as rules().
func (i *Interceptor) IsToolEnabled(name string) bool {
	i.mu.RLock()
	if i.enabledTools != nil {
		v := i.enabledTools[name]
		i.mu.RUnlock()
		return v
	}
	i.mu.RUnlock()

	i.mu.Lock()
	defer i.mu.Unlock()
	if i.enabledTools == nil {
		_ = i.loadLocked()
	}
	return i.enabledTools[name]
}

// GetEnabledTools returns the ordered list of tool names that are currently
// configured to enter the intercept rule system. When the setting has never been
// saved the hard-coded default list is returned.
func (i *Interceptor) GetEnabledTools() ([]string, error) {
	val, ok, err := i.db.GetSetting("intercept_enabled_tools")
	if err != nil {
		return nil, err
	}
	if !ok {
		out := make([]string, len(defaultEnabledTools))
		copy(out, defaultEnabledTools)
		return out, nil
	}
	var names []string
	if err := json.Unmarshal([]byte(val), &names); err != nil {
		return []string{}, nil
	}
	return names, nil
}

// SetEnabledTools persists the list of tool names that should enter the intercept
// rule system, then invalidates the cache so the next call picks up the new list.
func (i *Interceptor) SetEnabledTools(tools []string) error {
	b, err := json.Marshal(tools)
	if err != nil {
		return err
	}
	if err := i.db.SetSetting("intercept_enabled_tools", string(b)); err != nil {
		return err
	}
	i.Invalidate()
	return nil
}

// Decision is the outcome of a successful rule match.
type Decision struct {
	ModelInput       json.RawMessage
	ModelInputDigest string
	ModelFallback    bool
	RuleName         string
	ConfigDigest     string
	ProfileID        int64
	// "allow" | "deny" | "ask" | "flag"
	// flag = AI判疑(标记但放通):动作等同 allow(工具照常执行、不阻塞 worker),
	// 但审计落一条 status='flagged' 的记录,供人事后复盘。它不是"降级拦截"。
	Action         string
	Message        string
	RuleID         int64
	TimeoutEnabled bool
	TimeoutSeconds int
	TimeoutAction  string // "deny" | "allow"
}

// --- LLM fallback judge ---

// Judge settings keys (stored in the settings KV table). See docs §3.
const (
	settingJudgeEnabled          = "llm_judge_enabled"
	settingJudgeProfileID        = "llm_judge_profile_id"
	settingJudgePrompt           = "llm_judge_prompt"
	settingJudgeTimeoutSecs      = "llm_judge_timeout_seconds"
	settingJudgeFailAction       = "llm_judge_fail_action"
	settingJudgeAskTimeoutSecs   = "llm_judge_ask_timeout_seconds"
	settingJudgeAskTimeoutAction = "llm_judge_ask_timeout_action"
)

// Judge default values.
const (
	defaultJudgeTimeoutSecs = 15
	// 失效方向必须是"拒"而不是"放"：这道闸门拦的是危险工具调用，LLM 审批
	// 挂掉时默认放行等于安全控制静默失效（与 AskTimeoutAction 默认 deny
	// 保持一致）。确实依赖 LLM 放行的用户可在设置里显式改回 allow。
	defaultJudgeFailAction       = "deny"
	defaultJudgeAskTimeoutSecs   = 300
	defaultJudgeAskTimeoutAction = "deny"
)

// JudgeConfig is the resolved LLM-fallback-judge configuration. Prompt is always
// non-empty (falls back to DefaultJudgePrompt).
type JudgeConfig struct {
	Enabled           bool   `json:"enabled"`
	ProfileID         int64  `json:"profile_id"` // 0 = follow active/default
	Prompt            string `json:"prompt"`
	TimeoutSeconds    int    `json:"timeout_seconds"`
	FailAction        string `json:"fail_action"` // allow|ask|deny|flag
	AskTimeoutSeconds int    `json:"ask_timeout_seconds"`
	AskTimeoutAction  string `json:"ask_timeout_action"` // allow|deny
}

// judgeConfig reads the judge configuration from settings, applying defaults for
// missing/invalid keys. Read fresh on each fallback judgement — the LLM call that
// follows dwarfs a few KV reads, and freshness avoids a cache-invalidation path.
func (i *Interceptor) judgeConfig() JudgeConfig {
	c := JudgeConfig{
		Enabled:   i.db.GetBool(settingJudgeEnabled, false),
		ProfileID: int64(i.getSettingInt(settingJudgeProfileID, 0)),
		// fail_action 允许 flag:模型不可用时"判疑放通"是比 allow 更可取的降级——
		// 工具照样执行,但每次失败都留痕,事后能看出监管实际处于降级状态。
		TimeoutSeconds:    i.getSettingInt(settingJudgeTimeoutSecs, defaultJudgeTimeoutSecs),
		FailAction:        i.getSettingChoice(settingJudgeFailAction, defaultJudgeFailAction, "allow", "ask", "deny", "flag"),
		AskTimeoutSeconds: i.getSettingInt(settingJudgeAskTimeoutSecs, defaultJudgeAskTimeoutSecs),
		AskTimeoutAction:  i.getSettingChoice(settingJudgeAskTimeoutAction, defaultJudgeAskTimeoutAction, "allow", "deny"),
	}
	// Prompt: stored value if non-empty, else the built-in template.
	if v, ok, _ := i.db.GetSetting(settingJudgePrompt); ok && strings.TrimSpace(v) != "" {
		c.Prompt = v
	} else {
		c.Prompt = DefaultJudgePrompt
	}
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = defaultJudgeTimeoutSecs
	}
	if c.AskTimeoutSeconds <= 0 {
		c.AskTimeoutSeconds = defaultJudgeAskTimeoutSecs
	}
	return c
}

func (i *Interceptor) getSettingInt(key string, def int) int {
	v, ok, err := i.db.GetSetting(key)
	if err != nil || !ok {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return n
}

func (i *Interceptor) getSettingChoice(key, def string, allowed ...string) string {
	v, ok, err := i.db.GetSetting(key)
	if err != nil || !ok {
		return def
	}
	v = strings.TrimSpace(v)
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	return def
}

// GetJudgeConfig returns the resolved judge configuration for the API/UI. Prompt
// is the effective prompt (built-in template when unset), so the UI can prefill.
func (i *Interceptor) GetJudgeConfig() JudgeConfig { return i.judgeConfig() }

// SetJudgeConfig persists the judge configuration. An empty Prompt clears the
// override (the built-in template is used again).
func (i *Interceptor) SetJudgeConfig(c JudgeConfig) error {
	if err := i.db.SetBool(settingJudgeEnabled, c.Enabled); err != nil {
		return err
	}
	if err := i.db.SetSetting(settingJudgeProfileID, strconv.FormatInt(c.ProfileID, 10)); err != nil {
		return err
	}
	// Store the prompt only when it differs from the built-in template, so version
	// updates to DefaultJudgePrompt flow through for users who never customized it.
	promptToStore := ""
	if strings.TrimSpace(c.Prompt) != "" && strings.TrimSpace(c.Prompt) != strings.TrimSpace(DefaultJudgePrompt) {
		promptToStore = c.Prompt
	}
	if err := i.db.SetSetting(settingJudgePrompt, promptToStore); err != nil {
		return err
	}
	if err := i.db.SetSetting(settingJudgeTimeoutSecs, strconv.Itoa(c.TimeoutSeconds)); err != nil {
		return err
	}
	if err := i.db.SetSetting(settingJudgeFailAction, c.FailAction); err != nil {
		return err
	}
	if err := i.db.SetSetting(settingJudgeAskTimeoutSecs, strconv.Itoa(c.AskTimeoutSeconds)); err != nil {
		return err
	}
	if err := i.db.SetSetting(settingJudgeAskTimeoutAction, c.AskTimeoutAction); err != nil {
		return err
	}
	return nil
}

// engineInput 组装前置决策引擎的输入:工具名、参数,以及 LLM 裁判同样能看到的
// 工作目录、人类背景、任务归属。这三项是判定"授权渗透的提权尝试"与"越权操作"的
// 关键——只看工具名和参数时,任何触及凭据/提权语义的调用都会显得可疑。
func (i *Interceptor) engineInput(ctx context.Context, tool string, arguments json.RawMessage) DecisionEngineInput {
	in := DecisionEngineInput{Tool: tool, Arguments: append(json.RawMessage(nil), arguments...)}
	if wd, bg, ok := ReviewEnvironmentFromContext(ctx); ok {
		in.WorkingDir = wd
		if bg.Source == BackgroundUserMessage {
			in.Background = bg.Text
		}
	}
	if taskID, agent, ok := TaskContextFromContext(ctx); ok {
		in.TaskID, in.AgentName = taskID, agent
	}
	return in
}

// Judge runs the fallback decision path for a tool call that matched no rule. It
// asks, in order:
//
//  1. the pre-decision engine (JEV/Laya) — its own switch lives in the callback,
//     so it runs regardless of llm_judge_enabled;
//  2. the LLM fallback judge — only when llm_judge_enabled is on and a reviewer
//     is wired.
//
// A task configured with mode "off" gets no verdict from either layer: it opted
// out of interception, so the caller passes the call through (and, because it
// never reaches Log/HandleAsk, no audit row is written).
//
// It returns (Decision, true) when a terminal verdict was reached, or
// (Decision{}, false) when no layer decided (caller then keeps the current
// behavior: allow). On model error or an unparseable reply the LLM path falls
// back to the configured FailAction. Ask verdicts carry the human-approval
// timeout so the existing HandleAsk consumes them unchanged.
func (i *Interceptor) Judge(ctx context.Context, tool string, arguments json.RawMessage) (Decision, bool) {
	// 任务级关闭:规则与两层模型全部不介入。放在最前面,是为了让"本任务不跑监管"
	// 连决策引擎的 15s 网络调用都不发生。
	if guardOff, _ := i.resolveTaskGuard(ctx); guardOff {
		return Decision{}, false
	}
	i.mu.RLock()
	engine := i.engine
	rv := i.reviewer
	i.mu.RUnlock()

	// ── 1) 前置决策引擎(JEV/Laya):与 LLM 裁判平行,自己带开关 ──
	// 这一层必须放在 llm_judge_enabled 判断之前:旧实现把引擎嵌在 reviewer 闭包
	// 内部,而 Judge 开头就用 !cfg.Enabled 提前返回,于是 llm_judge_enabled=false
	// 时引擎一次也不会执行(开关与凭据都配好了却毫无痕迹)。
	if engine != nil {
		if dec, ok := engine(ctx, i.engineInput(ctx, tool, arguments)); ok {
			if out, valid := i.finishEngineDecision(ctx, dec, tool, arguments); valid {
				return out, true
			}
		}
	}

	// ── 2) LLM 兜底裁判 ──
	// 先看 reviewer 是否接入:未接入时这一层必然无裁决,不必读设置(省一次 KV 读)。
	// 与 cfg.Enabled 是"与"关系,交换顺序不改变结果。
	if rv == nil {
		return Decision{}, false
	}
	cfg := i.judgeConfig()
	if !cfg.Enabled {
		return Decision{}, false
	}

	cctx := ctx
	if cfg.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		cctx, cancel = context.WithTimeout(ctx, time.Duration(cfg.TimeoutSeconds)*time.Second)
		defer cancel()
	}

	input, contextErr := BuildReviewInput(cctx, tool, arguments)
	cfg.Prompt = EffectiveJudgePrompt(cfg.Prompt)
	var out Decision
	var err error
	var modelInput []byte
	if contextErr != nil {
		// Invalid current arguments cannot be reviewed faithfully, regardless of
		// the configured model-failure strategy. A human must resolve the input.
		out = Decision{Action: "ask", ModelFallback: true, Message: "审查上下文不完整，需要人工确认：" + contextErr.Error()}
	} else {
		modelInput, _ = json.Marshal(input)
		out, err = rv(cctx, cfg.ProfileID, cfg.Prompt, input)
	}
	if err != nil {
		out = Decision{ProfileID: out.ProfileID, ModelFallback: true, Action: cfg.FailAction, Message: "模型审批失败,按失败策略处理: " + err.Error()}
	}
	switch out.Action {
	case "allow", "ask", "deny", "flag":
		// valid verdict
	default:
		out = Decision{ProfileID: out.ProfileID, ModelFallback: true, Action: cfg.FailAction, Message: "模型输出无法解析,按失败策略处理"}
	}
	// A model verdict never carries a rule; keep RuleID 0 (→ NULL) for history.
	out.RuleID = 0
	if len(modelInput) > 0 {
		out.ModelInput = modelInput
		out.ModelInputDigest = digestInput(modelInput)
	}
	if out.ProfileID != 0 {
		cfg.ProfileID = out.ProfileID
	}
	out.ProfileID = cfg.ProfileID
	configJSON, _ := json.Marshal(cfg)
	out.ConfigDigest = digestInput(configJSON)
	if out.Message == "" {
		out.Message = "[模型] " + judgeActionLabel(out.Action)
	} else if !strings.HasPrefix(out.Message, "[模型]") {
		out.Message = "[模型] " + out.Message
	}
	if out.Action == "ask" {
		out.TimeoutEnabled = true
		out.TimeoutSeconds = cfg.AskTimeoutSeconds
		out.TimeoutAction = cfg.AskTimeoutAction
	}
	return out, true
}

// finishEngineDecision normalises a pre-decision-engine verdict into the same
// shape the LLM path produces: a validated action, a [模型]-prefixed reason and
// the ask timeout. An invalid action is reported as "no verdict" (false) rather
// than silently defaulting — a misbehaving engine must not be able to pass a
// call through as if it had been approved.
func (i *Interceptor) finishEngineDecision(ctx context.Context, dec Decision, tool string, arguments json.RawMessage) (Decision, bool) {
	switch dec.Action {
	case "allow", "ask", "deny", "flag":
	default:
		return Decision{}, false
	}
	if dec.Message == "" {
		dec.Message = "[模型] " + judgeActionLabel(dec.Action)
	} else if !strings.HasPrefix(dec.Message, "[模型]") {
		dec.Message = "[模型] " + dec.Message
	}
	// 引擎没有规则归属(RuleID=0 → NULL);模型输入留档,审计页才能复盘引擎看到了什么。
	dec.RuleID = 0
	if len(dec.ModelInput) == 0 {
		if input, err := BuildReviewInput(ctx, tool, arguments); err == nil {
			if b, err := json.Marshal(input); err == nil {
				dec.ModelInput = b
				dec.ModelInputDigest = digestInput(b)
			}
		}
	}
	// ask 必须带上人工审批超时,否则 HandleAsk 会退回 60s 默认值并可能误判超时动作。
	if dec.Action == "ask" {
		cfg := i.judgeConfig()
		dec.TimeoutEnabled = true
		dec.TimeoutSeconds = cfg.AskTimeoutSeconds
		dec.TimeoutAction = cfg.AskTimeoutAction
	}
	return dec, true
}

func judgeActionLabel(action string) string {
	switch action {
	case "allow":
		return "放行"
	case "deny":
		return "拦截"
	case "ask":
		return "转人工审批"
	case "flag":
		return "判疑放通"
	default:
		return action
	}
}

// Match evaluates the rule list (priority DESC) against a tool call.
// Returns (Decision, true) for the first matching enabled rule, or
// (Decision{}, false) if no rule matches.
//
// It takes ctx because task-level configuration lives there (see WithTaskContext):
// a task with mode "off" must not match any rule, and a task with ruleIDs must
// only be matched against those rules. Both are applied here rather than in the
// caller so no call path can bypass task-level gating by forgetting to ask.
func (i *Interceptor) Match(ctx context.Context, toolName string, input []byte) (Decision, bool) {
	guardOff, only := i.resolveTaskGuard(ctx)
	if guardOff {
		return Decision{}, false
	}
	// scope='src' 的规则只对 SRC 模式任务生效。判定在循环外做一次(回调读内存
	// 句柄,无 IO),而不是逐条规则查库。taskID 为空(无任务上下文,如纯聊天)、
	// 解析器未接、任务不存在——三种情况一律按"非 SRC"跳过这些规则:宁漏勿误。
	taskID, _ := taskInfoFromCtx(ctx)
	srcTask := false
	if taskID != "" {
		i.mu.RLock()
		srcRes := i.srcRes
		i.mu.RUnlock()
		srcTask = srcRes != nil && srcRes(taskID)
	}
	rules, err := i.rules()
	if err != nil || len(rules) == 0 {
		return Decision{}, false
	}
	if len(only) > 0 {
		allowed := make(map[int64]bool, len(only))
		for _, id := range only {
			allowed[id] = true
		}
		filtered := make([]compiledRule, 0, len(rules))
		for _, r := range rules {
			if allowed[r.ID] {
				filtered = append(filtered, r)
			}
		}
		rules = filtered
	}
	for _, r := range rules {
		// 与任务级规则集限定(only)是"且"关系:两个过滤维度各自收窄,互不放宽。
		if r.Scope == "src" && !srcTask {
			continue
		}
		if ruleMatches(r, toolName, input) {
			msg := r.Message
			if msg == "" {
				msg = defaultMessage(r.Action, r.Name)
			}
			configJSON, _ := json.Marshal(r.InterceptRule)
			return Decision{
				RuleName: r.Name, ConfigDigest: digestInput(configJSON),
				Action:         r.Action,
				Message:        msg,
				RuleID:         r.ID,
				TimeoutEnabled: r.TimeoutEnabled,
				TimeoutSeconds: r.TimeoutSeconds,
				TimeoutAction:  r.TimeoutAction,
			}, true
		}
	}
	return Decision{}, false
}

func ruleMatches(r compiledRule, toolName string, input []byte) bool {
	var subject string
	switch r.MatchTarget {
	case "tool_name":
		subject = toolName
	case "tool_input":
		subject = string(input)
	default:
		return false
	}
	if r.MatchType == "regex" {
		return r.re != nil && r.re.MatchString(subject)
	}
	return strings.Contains(subject, r.Pattern)
}

func defaultMessage(action, name string) string {
	switch action {
	case "deny":
		return "拦截规则 [" + name + "] 禁止执行此工具"
	case "ask":
		return "拦截规则 [" + name + "] 要求用户审批，请等待"
	case "flag":
		// 判疑放通不阻塞,消息只用于审计行:说清"已放行但标记",避免事后误读为拦截。
		return "拦截规则 [" + name + "] 标记为可疑，已放行（留痕待复盘）"
	default:
		return ""
	}
}

// Log records an allow/deny/flag rule or model decision into intercept_pending as an
// ALREADY-decided row (status = "allowed" | "denied" | "flagged"), for observability.
// Unlike HandleAsk it does NOT block and needs no user action — it makes explicit review
// decisions visible on the history page (GET /api/intercept/history) and the task's
// intercept list. Best-effort: a DB error is swallowed so logging never changes the tool
// call's outcome. The pending list (status='pending') is unaffected, so it still shows
// only asks awaiting a decision.
func (i *Interceptor) Log(ctx context.Context, convID int64, dec Decision, toolName string, input []byte, status string) {
	taskID, agentName := taskInfoFromCtx(ctx)
	audit := auditFor(ctx, dec, input, status)
	if status == "flagged" {
		// flag 与 allow 的执行语义相同(工具确实会执行),审计必须照 allow 记录,
		// 否则 CompleteIntercept 的条件(effective_action='allow' 且
		// execution_status='awaiting_result')不成立,执行结果永远关联不上。
		audit.EffectiveAction = "allow"
		audit.ExecutionStatus = "awaiting_result"
		if audit.Correlation != "exact" {
			audit.ExecutionStatus = "unknown"
		}
	}
	id, err := i.db.CreateDecidedIntercept(dec.RuleID, convID, taskID, agentName, toolName, input, status, dec.Message, audit)
	if err == nil {
		i.bindResult(ctx, id, audit)
	}
}

// HandleAsk creates a pending approval record and blocks until the user decides
// (via /api/intercept/pending/{id}/decide) or the per-rule timeout elapses.
// Returns true if the user approved.
//
// convID == 0 means no active conversation (background pentest task). The
// pending record is still created (conversation_id = NULL) so the approvals
// page shows it and the sidebar badge lights up. The worker thread blocks just
// like in a chat session — the user must visit the approvals page to unblock it.
func (i *Interceptor) HandleAsk(ctx context.Context, convID int64, dec Decision, toolName string, input []byte) bool {
	ruleID := dec.RuleID
	taskID, agentName := taskInfoFromCtx(ctx)
	taskEmit := taskEmitFromCtx(ctx)

	audit := auditFor(ctx, dec, input, "pending")
	pendingID, err := i.db.CreateInterceptPending(ruleID, convID, taskID, agentName, toolName, input, dec.Message, audit)
	if err != nil {
		return false
	}

	i.bindResult(ctx, pendingID, audit)
	ch := i.pending.add(pendingID)
	defer i.pending.remove(pendingID)
	// Polling clients can decide after INSERT commits but before the channel is
	// registered. Re-read after registration so that decision cannot be lost;
	// later decisions will be delivered through ch.
	if saved, err := i.db.GetInterceptDetail(pendingID); err == nil && saved != nil && saved.Status != "pending" {
		return saved.Status == "allowed" || (saved.Audit != nil && saved.Audit.EffectiveAction == "allow")
	}

	detail, _ := json.Marshal(map[string]any{
		"pending_id": pendingID,
		"tool":       toolName,
		"input":      json.RawMessage(input),
	})
	activity := db.Activity{
		Kind:    "intercept_request",
		Summary: fmt.Sprintf("工具 %s 请求审批 (#%d)", toolName, pendingID),
		Detail:  string(detail),
	}

	if convID != 0 {
		// Chat session: write inline card to the conversation stream.
		_, _ = i.db.AppendConvActivity(convID, activity)
	} else if taskEmit != nil {
		// Task worker: emit to the exploration activity stream so it appears
		// inline in the session transcript (the emit fn stamps NodeID + Worker).
		taskEmit(activity)
	}

	if !dec.TimeoutEnabled {
		// No timeout: wait indefinitely until user decides or worker stops.
		select {
		case allowed := <-ch:
			return allowed
		case <-ctx.Done():
			_, _ = i.db.ResolveIntercept(pendingID, "denied", "deny", "工作已取消")
			_ = i.db.CompleteIntercept(pendingID, audit.RunID, audit.ToolUseID, "not_executed", "执行前工作已取消", false)
			return false
		}
	}

	secs := dec.TimeoutSeconds
	if secs <= 0 {
		secs = 60
	}
	timer := time.NewTimer(time.Duration(secs) * time.Second)
	defer timer.Stop()
	select {
	case allowed := <-ch:
		return allowed
	case <-timer.C:
		allowed := dec.TimeoutAction == "allow"
		action := "deny"
		if allowed {
			action = "allow"
		}
		resolved, err := i.db.ResolveIntercept(pendingID, "timeout", action, "审批超时，按超时策略处理")
		if err != nil {
			return false
		}
		if !resolved {
			detail, err := i.db.GetInterceptDetail(pendingID)
			return err == nil && detail != nil && (detail.Status == "allowed" || (detail.Audit != nil && detail.Audit.EffectiveAction == "allow"))
		}
		return allowed
	case <-ctx.Done():
		_, _ = i.db.ResolveIntercept(pendingID, "denied", "deny", "工作已取消")
		_ = i.db.CompleteIntercept(pendingID, audit.RunID, audit.ToolUseID, "not_executed", "执行前工作已取消", false)
		return false
	}
}

var ErrAlreadyDecided = errors.New("审批已处理或不存在，请刷新记录")

// Decide resolves a pending request. Called by the HTTP decide endpoint.
func (i *Interceptor) Decide(pendingID int64, allowed bool) error {
	status := "denied"
	if allowed {
		status = "allowed"
	}
	action, reason := "deny", "人工拒绝执行"
	if allowed {
		action, reason = "allow", "人工允许执行"
	}
	resolved, err := i.db.ResolveIntercept(pendingID, status, action, reason)
	if err != nil {
		return err
	}
	if !resolved {
		return ErrAlreadyDecided
	}
	i.pending.resolve(pendingID, allowed)
	return nil
}
