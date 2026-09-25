package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Task is a row in the task registry (1:1 with an exploration).
type Task struct {
	ID            int64      `json:"id"`
	Name          string     `json:"name"` // 可选任务名称;空=未命名
	CategoryID    *int64     `json:"category_id,omitempty"`
	CategoryName  string     `json:"category_name,omitempty"`
	Pinned        bool       `json:"pinned"`
	PinnedAt      *time.Time `json:"pinned_at,omitempty"`
	Description   string     `json:"description"`
	Goal          string     `json:"goal"`
	ExplorationID int64      `json:"exploration_id"`
	Status        string     `json:"status"`
	Paused        bool       `json:"paused"`
	Queued        bool       `json:"queued"`
	QueuedAt      *time.Time `json:"queued_at,omitempty"`
	QueueMode     string     `json:"queue_mode,omitempty"`
	LLMProfileID  *int64     `json:"llm_profile_id,omitempty"`
	// Task-level ordered LLM chain. LLMProfileID remains the compatibility alias
	// for ActiveLLMProfileID while older API clients still send one profile id.
	LLMProfileIDs      []int64    `json:"llm_profile_ids,omitempty"`
	ActiveLLMProfileID *int64     `json:"active_llm_profile_id,omitempty"`
	LLMChainRevision   int64      `json:"-"`
	LLMFailoverState   string     `json:"llm_failover_state,omitempty"`
	LLMFailoverReason  string     `json:"llm_failover_reason,omitempty"`
	SourceTaskIDs      []int64    `json:"source_task_ids,omitempty"`
	CompanyIDs         []int64    `json:"company_ids,omitempty"`
	ParentRef          string     `json:"parent_ref,omitempty"` // 父任务 id(编排 spawn 记录;空=顶层)
	CreatedAt          time.Time  `json:"created_at"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"` // 进入终态(done/failed/timeout)的时刻;非终态为 nil
	// 任务级超时(见 docs/任务级超时与收尾设计.md)。
	TimeoutSeconds int        `json:"timeout_seconds"`        // 0=不限时
	FirstRunAt     *time.Time `json:"first_run_at,omitempty"` // 首次真正开始运行的时刻(非 created_at);nil=尚未运行
	DeadlineAt     *time.Time `json:"deadline_at,omitempty"`  // = first_run_at + timeout_seconds;nil=不限或未运行
	// planner 心跳触发间隔(秒)：距上轮 plan 结束/任务开始满该值且期间无触发 → 触发一轮。
	// 下限=默认=300(5min)，低于一律抬到 300(在 CreateTask 归一)。见 docs/planner-trigger-impl-plan.md
	PlanHeartbeatSeconds int `json:"plan_heartbeat_seconds"`
	// CoverageEnabled 是「资产覆盖度功能」总开关(默认 true)。false 时：不计算/不展示测试
	// 覆盖度、不自动累积 task_scope(source=auto)、不给 agent 开放 add_task_scope/
	// list_untested_assets、态势里不注入 coverage 块(scope 字段仍保留)。company 关联
	// (task_scope kind=company)与此开关无关，永不受影响。见 db/task_scope.go。
	CoverageEnabled bool `json:"coverage_enabled"`
	// 三阶段平台字段(创建后不变)。Stage: recon/exploit/post，存量任务=exploit；
	// Kind: agent(现有引擎)/pipeline(确定性收集器流水线，当前仅 recon)；
	// Pipeline: 兼容保留的多步编排 JSON；StepKey/StepConfig: 单步骤任务模型
	// (recon 的每个细分步骤一条任务，StepKey=收集器 key)。
	Stage      string          `json:"stage"`
	Kind       string          `json:"kind"`
	Pipeline   json.RawMessage `json:"pipeline,omitempty"`
	StepKey    string          `json:"step_key"`
	StepConfig json.RawMessage `json:"step_config,omitempty"`
	// SRC 模式:任务产出计入 SRC 成果,并按平台/目标规范约束 AI 边界。
	// SRCProfileID 是旧「企业模式」的规范 id,仅为存量任务回显保留;
	// 新任务是「平台×目标」两级归属,约束由 SRCPlatformID+SRCTargetID 解析。
	SRCMode       bool  `json:"src_mode"`
	SRCProfileID  int64 `json:"src_profile_id"`
	SRCPlatformID int64 `json:"src_platform_id"`
	SRCTargetID   int64 `json:"src_target_id"`
	// 出网方式(任务级覆盖):''=跟随全局;direct=强制直连;pool=强制走代理池。
	// ProxyGroupID 仅在 pool 时有效(0=用全局选中的代理组)。
	ProxyMode    string `json:"proxy_mode"`
	ProxyGroupID int64  `json:"proxy_group_id"`
	// 监管门限(任务级覆盖):''/inherit=跟随全局;off=本任务不跑监管;enforce=强制启用。
	// 与出网方式同一套「任务级覆盖全局」的语义,由 server 层的 resolver 注入到
	// intercept 包(见 intercept.TaskGuardResolver),在每次工具调用前生效。
	// GuardRuleIDs 非空=本任务只用这些规则;空=用全部已启用规则(注意:空不等于"不启用")。
	GuardMode    string  `json:"guard_mode"`
	GuardRuleIDs []int64 `json:"guard_rule_ids"`
}

// 任务级出网模式取值(与 settings 的全局模式同一套语义,外加 ”=跟随全局)。
const (
	TaskProxyModeInherit = ""       // 跟随全局出网模式(默认,保持既有行为)
	TaskProxyModeDirect  = "direct" // 本任务强制直连(本地裸网)
	TaskProxyModePool    = "pool"   // 本任务强制走代理池
)

// ValidTaskProxyMode 校验任务级出网模式。
func ValidTaskProxyMode(m string) bool {
	switch m {
	case TaskProxyModeInherit, TaskProxyModeDirect, TaskProxyModePool:
		return true
	}
	return false
}

// 任务级监管门限取值(与 db/schema.sql 的 tasks_guard_mode_check 必须逐字一致,
// 否则非法值会在 INSERT/UPDATE 时被 CHECK 静默拒绝,表现为"保存成功但没生效")。
const (
	TaskGuardModeInherit = ""        // 跟随全局监管配置(默认,保持既有行为)
	TaskGuardModeOff     = "off"     // 本任务不跑监管(规则与模型都不介入)
	TaskGuardModeEnforce = "enforce" // 强制启用监管(即使全局关)
)

// ValidTaskGuardMode 校验任务级监管门限。空串合法(表示跟随全局),所以不能用
// 零值判断"未设置"——调用方需要区分"没传"时必须用指针。
func ValidTaskGuardMode(m string) bool {
	switch m {
	case TaskGuardModeInherit, "inherit", TaskGuardModeOff, TaskGuardModeEnforce:
		return true
	}
	return false
}

// NormalizeTaskGuardMode 把 'inherit' 折叠成 ''(两者语义相同,统一存空串,
// 让"跟随全局"在库里只有一种表示,避免列表徽标/比较逻辑要判两个值)。
func NormalizeTaskGuardMode(m string) string {
	if m == "inherit" {
		return TaskGuardModeInherit
	}
	return m
}

// ErrTaskGuardModeInvalid 表示任务级监管门限取值非法(handler 据此回 400)。
var ErrTaskGuardModeInvalid = errors.New("invalid task guard mode")

// NormalizeTaskGuardRuleIDs 归一规则集限定:去重 + 丢弃非正 id + 保持调用方顺序。
// 返回 nil 表示"不限定"(用全部已启用规则)。nil 与空切片在拦截侧同义,
// 但归一后统一成 nil,便于 scanTask 回读时与 []int64{} 保持一致。
func NormalizeTaskGuardRuleIDs(ids []int64) []int64 {
	var out []int64
	seen := map[int64]bool{}
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// marshalGuardRuleIDs 把规则集序列化成写库用的 JSONB 字面量。
// guard_rule_ids 列是 NOT NULL,而 json.Marshal(nil slice) 得到 "null"(JSON null,
// 不是 SQL NULL),会让下游 SQL 聚合与前端渲染多一种要兼容的形态;所以 nil 一律
// 写成 '[]'。空数组语义 = "不限定规则",与 nil 相同,不会放宽也不会收紧监管。
func marshalGuardRuleIDs(ids []int64) ([]byte, error) {
	if len(ids) == 0 {
		return []byte("[]"), nil
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return nil, fmt.Errorf("marshal guard rule ids: %w", err)
	}
	return raw, nil
}

// parseGuardRuleIDs 反序列化 guard_rule_ids。空值/非法值一律当作"不限定规则"
// 返回 nil:这一列坏掉时应当退回"用全部已启用规则"(更严),绝不能因为解析失败
// 而变成"没有任何规则可用"。
func parseGuardRuleIDs(raw []byte) []int64 {
	if len(raw) == 0 {
		return nil
	}
	var ids []int64
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil
	}
	return NormalizeTaskGuardRuleIDs(ids)
}

// TaskDeleteResult reports optional related-data cleanup performed in the same
// transaction as the task/exploration delete.
type TaskDeleteResult struct {
	AssetsDeleted     int64
	AssetsDetached    int64
	FindingsDeleted   int64
	LLMRecordsDeleted int64
}

// TaskDeletePreparation is produced inside the PostgreSQL deletion transaction
// after asset and anchor writers have been excluded. Prepare callbacks may use
// TrafficHosts to stage an external traffic deletion before PostgreSQL commits.
type TaskDeletePreparation struct {
	ExplorationID int64
	TrafficHosts  []string
}

// IsTerminal reports whether a task status is a terminal (finished) state.
// 单一真源，替换散落各处的 done/failed 硬编码判定。
func IsTerminal(status string) bool {
	return status == "done" || status == "failed" || status == "timeout"
}

// CreateTask creates an exploration + task in one transaction and returns the task.
// timeoutSeconds is the task-level wall-clock budget (0 = 不限时); deadline_at is
// stamped later at first real run (see engine), not here.
// MinPlanHeartbeatSeconds 是 planner 心跳间隔的下限 = 默认 = 10min。
// 低于它(含缺省 0 / 负值 / 误配的小值)一律抬到 10min，防止把 planner 打爆。
const MinPlanHeartbeatSeconds = 600

// MaxTaskSourceCount bounds the amount of live inherited context one task can
// pull into every planner/main-agent prompt. Inheritance is intentionally direct
// only; keeping the fan-in bounded also prevents a single create request from
// multiplying graph and asset-context queries without limit.
const MaxTaskSourceCount = 8

// MaxTaskCompanyCount bounds the number of company asset scopes attached to a
// task. Company scopes are prompt context, and their currently attributed
// assets are snapshotted into the task at creation time.
const MaxTaskCompanyCount = 32

const taskCompanyAssetSource = "company"

var (
	ErrTaskCompanyIDsInvalid = errors.New("invalid task company ids")
	ErrTaskCompanyNotFound   = errors.New("task company not found")
)

// NormalizeTaskCompanyIDs validates IDs and removes duplicates while retaining
// the user's first-seen order.
func NormalizeTaskCompanyIDs(ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	seen := make(map[int64]struct{}, min(len(ids), MaxTaskCompanyCount))
	normalized := make([]int64, 0, min(len(ids), MaxTaskCompanyCount))
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("%w: company id must be positive", ErrTaskCompanyIDsInvalid)
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
		if len(normalized) > MaxTaskCompanyCount {
			return nil, fmt.Errorf("%w: got more than %d unique companies", ErrTaskCompanyIDsInvalid, MaxTaskCompanyCount)
		}
	}
	return normalized, nil
}

func normalizeHeartbeat(sec int) int {
	if sec < MinPlanHeartbeatSeconds {
		return MinPlanHeartbeatSeconds
	}
	return sec
}

func (d *DB) CreateTask(description, goal string, llmProfileID *int64, timeoutSeconds, planHeartbeatSeconds int) (*Task, error) {
	var ids []int64
	if llmProfileID != nil {
		ids = []int64{*llmProfileID}
	}
	return d.CreateTaskWithOptions(description, goal, TaskCreateOptions{
		LLMProfileIDs: ids, TimeoutSeconds: timeoutSeconds, PlanHeartbeatSeconds: planHeartbeatSeconds,
	})
}

// TaskCreateOptions contains the task data that must be committed atomically
// with the task/exploration row.
type TaskCreateOptions struct {
	Name                 string // 可选任务名称;空=未命名
	CategoryID           *int64
	SourceTaskIDs        []int64
	CompanyIDs           []int64
	LLMProfileIDs        []int64
	TimeoutSeconds       int
	PlanHeartbeatSeconds int
	// CoverageEnabled 是「资产覆盖度功能」开关;nil=默认开(true)，让不关心该开关的创建
	// 路径(编排 spawn、老 API)沿用原行为。仅 web 创建任务时可显式传 false 关闭。
	CoverageEnabled *bool
	// 三阶段字段：Stage(默认 exploit)/Kind(默认 agent)/Pipeline(兼容多步编排)/
	// StepKey+StepConfig(单步骤任务，recon 每个细分步骤一条任务)。
	Stage      string
	Kind       string
	Pipeline   json.RawMessage
	StepKey    string
	StepConfig json.RawMessage
	// SRC 模式:任务产出计入 SRC 成果,并按平台/目标规范约束 AI 边界。
	SRCMode       bool
	SRCProfileID  int64
	SRCPlatformID int64
	SRCTargetID   int64
	// 出网方式(任务级覆盖):''=跟随全局;direct=强制直连;pool=强制走代理池。
	ProxyMode    string
	ProxyGroupID int64
	// 监管门限(任务级覆盖):''/inherit=跟随全局;off=本任务不跑监管;enforce=强制启用。
	// GuardRuleIDs 非空=本任务只用这些规则;空=用全部已启用规则。
	GuardMode    string
	GuardRuleIDs []int64
	// VPS 执行环境(创建即指派;不落 tasks 表,由 server 层 Manager 在任务进入
	// running 之前经 vpscenter 指派):VPSMachineID>0=指定机器,VPSAuto=true=自动
	// 挑空闲在线机。指派失败不阻断建任务(任务照常按本机模式跑,只记 warning)。
	VPSMachineID int64
	VPSAuto      bool
}

// CreateTaskWithOptions creates an exploration, task, direct source relations,
// and the ordered task LLM chain in one transaction.
func (d *DB) CreateTaskWithOptions(description, goal string, opts TaskCreateOptions) (*Task, error) {
	if len(opts.SourceTaskIDs) > MaxTaskSourceCount {
		return nil, fmt.Errorf("too many source tasks: got %d, maximum is %d", len(opts.SourceTaskIDs), MaxTaskSourceCount)
	}
	companyIDs, err := NormalizeTaskCompanyIDs(opts.CompanyIDs)
	if err != nil {
		return nil, err
	}
	opts.CompanyIDs = companyIDs
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var expID int64
	if err := tx.QueryRow(`INSERT INTO explorations(description, goal) VALUES ($1,$2) RETURNING id`, description, goal).Scan(&expID); err != nil {
		return nil, err
	}
	// origin fact: the exploration graph's root, a KindFact node (state='origin')
	// holding the original task description. Goals/intents/findings descend from
	// it, and the seeded target asset is anchored to it as lineage (the asset graph
	// is global and shared, not isolated per task). Being a fact (not a special 'begin' kind) lets every
	// intent uniformly connect to a fact node, including the first ones.
	originPayload, _ := json.Marshal(map[string]any{
		"summary":     "任务起点：" + description + "；目标：" + goal,
		"description": description,
		"goal":        goal,
	})
	if _, err := tx.Exec(`
INSERT INTO exploration_nodes(exploration_id, kind, payload, priority, state, origin)
VALUES ($1, 'fact', $2, 0, 'origin', 'system')`, expID, string(originPayload)); err != nil {
		return nil, err
	}
	if opts.TimeoutSeconds < 0 {
		opts.TimeoutSeconds = 0
	}
	opts.PlanHeartbeatSeconds = normalizeHeartbeat(opts.PlanHeartbeatSeconds)
	coverageEnabled := opts.CoverageEnabled == nil || *opts.CoverageEnabled
	var categoryName string
	if opts.CategoryID != nil {
		if *opts.CategoryID <= 0 {
			return nil, fmt.Errorf("%w: category id must be positive", ErrTaskCategoryInvalid)
		}
		if err := tx.QueryRow(`SELECT name FROM task_categories WHERE id=$1`, *opts.CategoryID).Scan(&categoryName); err != nil {
			if err == sql.ErrNoRows {
				return nil, ErrTaskCategoryNotFound
			}
			return nil, err
		}
	}
	// 三阶段+报告归一：空值回退默认 exploit/agent；recon 任务缺省走流水线 kind;
	// report 阶段(AI 识别任务)同为确定性流水线形态,由 server 侧 runner 驱动。
	switch opts.Stage {
	case "":
		opts.Stage = "exploit"
	case "recon", "exploit", "post", "report":
	default:
		return nil, fmt.Errorf("invalid task stage %q", opts.Stage)
	}
	switch opts.Kind {
	case "":
		if opts.Stage == "recon" || opts.Stage == "report" {
			opts.Kind = "pipeline"
		} else {
			opts.Kind = "agent"
		}
	case "agent", "pipeline":
	default:
		return nil, fmt.Errorf("invalid task kind %q", opts.Kind)
	}
	// 监管门限归一与校验:与 proxy_mode 同处理——非法值必须报错,不能落到 CHECK 上
	// 被静默拒绝(那样会得到"创建成功但门限没生效"的假象)。'inherit' 折叠成 ''。
	opts.GuardMode = NormalizeTaskGuardMode(opts.GuardMode)
	if !ValidTaskGuardMode(opts.GuardMode) {
		return nil, fmt.Errorf("%w: %q", ErrTaskGuardModeInvalid, opts.GuardMode)
	}
	opts.GuardRuleIDs = NormalizeTaskGuardRuleIDs(opts.GuardRuleIDs)
	var active *int64
	if len(opts.LLMProfileIDs) > 0 {
		id := opts.LLMProfileIDs[0]
		active = &id
	}
	// guard_rule_ids 是 JSONB NOT NULL:显式把 nil 序列化成 '[]' 而不是 'null',
	// 让"不限定规则"在库里只有一种表示(SQL 聚合/前端渲染都不用额外兼容 JSON null)。
	guardRuleIDs, err := marshalGuardRuleIDs(opts.GuardRuleIDs)
	if err != nil {
		return nil, err
	}
	t := &Task{
		Name: opts.Name, CategoryID: opts.CategoryID, CategoryName: categoryName,
		Description: description, Goal: goal, ExplorationID: expID,
		LLMProfileID: active, ActiveLLMProfileID: active,
		LLMProfileIDs:  append([]int64(nil), opts.LLMProfileIDs...),
		SourceTaskIDs:  append([]int64(nil), opts.SourceTaskIDs...),
		CompanyIDs:     append([]int64(nil), opts.CompanyIDs...),
		TimeoutSeconds: opts.TimeoutSeconds, PlanHeartbeatSeconds: opts.PlanHeartbeatSeconds,
		CoverageEnabled: coverageEnabled,
		Stage:           opts.Stage, Kind: opts.Kind, Pipeline: append(json.RawMessage(nil), opts.Pipeline...),
		StepKey: opts.StepKey, StepConfig: append(json.RawMessage(nil), opts.StepConfig...),
		SRCMode: opts.SRCMode, SRCProfileID: opts.SRCProfileID,
		SRCPlatformID: opts.SRCPlatformID, SRCTargetID: opts.SRCTargetID,
		ProxyMode: opts.ProxyMode, ProxyGroupID: opts.ProxyGroupID,
		GuardMode: opts.GuardMode, GuardRuleIDs: append([]int64(nil), opts.GuardRuleIDs...),
	}
	// 可空外键的 0 值必须写成 NULL 而不是 0:这些列上有外键约束,写 0 会因
	// 「引用的行不存在」直接报错,而「未关联」在语义上也确实是 NULL。
	srcProfileID := nullableID(opts.SRCProfileID)
	srcPlatformID := nullableID(opts.SRCPlatformID)
	srcTargetID := nullableID(opts.SRCTargetID)
	proxyGroupID := nullableID(opts.ProxyGroupID)
	if err := tx.QueryRow(`
INSERT INTO tasks(name, category_id, description, goal, exploration_id, llm_profile_id, active_llm_profile_id, timeout_seconds, plan_heartbeat_seconds, coverage_enabled, stage, kind, pipeline, step_key, step_config, src_mode, src_profile_id, src_platform_id, src_target_id, proxy_mode, proxy_group_id, guard_mode, guard_rule_ids)
VALUES ($1,$2,$3,$4,$5,$6,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)
RETURNING id, status, paused, created_at`, opts.Name, opts.CategoryID, description, goal, expID, active, opts.TimeoutSeconds, opts.PlanHeartbeatSeconds, coverageEnabled, opts.Stage, opts.Kind, opts.Pipeline, opts.StepKey, opts.StepConfig, opts.SRCMode, srcProfileID, srcPlatformID, srcTargetID, opts.ProxyMode, proxyGroupID, opts.GuardMode, guardRuleIDs).Scan(&t.ID, &t.Status, &t.Paused, &t.CreatedAt); err != nil {
		return nil, err
	}
	if err := insertTaskRelations(tx, t.ID, opts.SourceTaskIDs); err != nil {
		return nil, err
	}
	if err := insertTaskCompanies(tx, t.ID, opts.CompanyIDs); err != nil {
		return nil, err
	}
	if err := insertTaskLLMProfiles(tx, t.ID, opts.LLMProfileIDs); err != nil {
		return nil, err
	}
	if len(opts.LLMProfileIDs) == 0 {
		t.LLMFailoverState = "default"
	} else {
		t.LLMFailoverState = "ready"
	}
	return t, tx.Commit()
}

func insertTaskCompanies(tx *sql.Tx, taskID int64, companyIDs []int64) error {
	if len(companyIDs) == 0 {
		return nil
	}
	// Company scope edits rebuild assets.company_id. Serialize the creation-time
	// snapshot with those edits so the task sees one committed attribution state.
	if err := lockCompanyScopeMutation(tx); err != nil {
		return err
	}
	var inserted int
	err := tx.QueryRow(`
WITH requested(company_id, position) AS (
    SELECT company_id, position
    FROM unnest($2::bigint[]) WITH ORDINALITY AS requested(company_id, position)
), inserted AS (
    INSERT INTO task_scope(task_id, kind, company_id, source, reason)
    SELECT $1, 'company', companies.id, 'manual', '任务创建时关联企业'
    FROM requested
    JOIN companies ON companies.id=requested.company_id
    ORDER BY requested.position
    RETURNING company_id
)
SELECT count(*) FROM inserted`, taskID, companyIDs).Scan(&inserted)
	if err != nil {
		return err
	}
	if inserted != len(companyIDs) {
		return fmt.Errorf("%w: one or more companies do not exist", ErrTaskCompanyNotFound)
	}

	// Updating task_ids fires sync_task_asset_links, which first creates generic
	// source rows. The provenance upsert must therefore run afterwards so the
	// company name and creation-time reason remain visible to operators.
	if _, err := tx.Exec(`
UPDATE assets
SET task_ids=CASE
    WHEN $1=ANY(task_ids) THEN task_ids
    ELSE array_append(task_ids, $1)
END
WHERE company_id=ANY($2::bigint[])`, taskID, companyIDs); err != nil {
		return err
	}
	if _, err := tx.Exec(`
INSERT INTO task_asset_links(task_id, asset_id, source, source_summary)
SELECT $1, asset.id, $3, '任务创建时关联企业：' || company.name
FROM assets asset
JOIN companies company ON company.id=asset.company_id
WHERE asset.company_id=ANY($2::bigint[])
  AND $1=ANY(asset.task_ids)
ON CONFLICT (task_id, asset_id) DO UPDATE
SET source=EXCLUDED.source,
    source_summary=EXCLUDED.source_summary,
    source_node_id=NULL`, taskID, companyIDs, taskCompanyAssetSource); err != nil {
		return err
	}
	return nil
}

func insertTaskRelations(tx *sql.Tx, taskID int64, sourceIDs []int64) error {
	seen := map[int64]bool{}
	for _, sourceID := range sourceIDs {
		if sourceID <= 0 || sourceID == taskID || seen[sourceID] {
			return fmt.Errorf("invalid or duplicate source task id %d", sourceID)
		}
		seen[sourceID] = true
		res, err := tx.Exec(`INSERT INTO task_relations(task_id, source_task_id)
SELECT $1, id FROM tasks WHERE id=$2 AND deleted_at IS NULL`, taskID, sourceID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("source task %d not found", sourceID)
		}
	}
	return nil
}

func insertTaskLLMProfiles(tx *sql.Tx, taskID int64, profileIDs []int64) error {
	seen := map[int64]bool{}
	for position, profileID := range profileIDs {
		if profileID <= 0 || seen[profileID] {
			return fmt.Errorf("invalid or duplicate LLM profile id %d", profileID)
		}
		seen[profileID] = true
		if _, err := tx.Exec(`INSERT INTO task_llm_profiles(task_id, profile_id, position) VALUES ($1,$2,$3)`, taskID, profileID, position); err != nil {
			return err
		}
	}
	return nil
}

const taskCols = `id, COALESCE(name,''), category_id,
COALESCE((SELECT category.name FROM task_categories category WHERE category.id=tasks.category_id),''),
description, goal, exploration_id, status, paused, queued, queued_at, COALESCE(queue_mode,''), llm_profile_id, active_llm_profile_id, COALESCE(parent_ref,''), pinned_at, created_at, completed_at, COALESCE(timeout_seconds,0), COALESCE(plan_heartbeat_seconds,300), COALESCE(coverage_enabled,true), first_run_at, deadline_at, COALESCE(stage,'exploit'), COALESCE(kind,'agent'), pipeline, COALESCE(step_key,''), step_config, COALESCE(src_mode,false), COALESCE(src_profile_id,0), COALESCE(src_platform_id,0), COALESCE(src_target_id,0), COALESCE(proxy_mode,''), COALESCE(proxy_group_id,0), COALESCE(guard_mode,''), guard_rule_ids`

func scanTask(sc interface{ Scan(...any) error }) (*Task, error) {
	var t Task
	// pipeline/step_config 可空(NULL=未配置):经 []byte 中转,空值保持 nil RawMessage。
	var pipeline, stepConfig []byte
	// guard_rule_ids 是 JSONB(NOT NULL DEFAULT '[]'):同样走 []byte 中转再解成 []int64,
	// 老库刚迁移完/手工写坏时的空值与非法值都退回 nil(=不限定规则,用全部已启用规则)。
	var guardRuleIDs []byte
	if err := sc.Scan(&t.ID, &t.Name, &t.CategoryID, &t.CategoryName, &t.Description, &t.Goal, &t.ExplorationID, &t.Status, &t.Paused, &t.Queued, &t.QueuedAt, &t.QueueMode, &t.LLMProfileID, &t.ActiveLLMProfileID, &t.ParentRef, &t.PinnedAt, &t.CreatedAt, &t.CompletedAt, &t.TimeoutSeconds, &t.PlanHeartbeatSeconds, &t.CoverageEnabled, &t.FirstRunAt, &t.DeadlineAt, &t.Stage, &t.Kind, &pipeline, &t.StepKey, &stepConfig, &t.SRCMode, &t.SRCProfileID, &t.SRCPlatformID, &t.SRCTargetID, &t.ProxyMode, &t.ProxyGroupID, &t.GuardMode, &guardRuleIDs); err != nil {
		return nil, err
	}
	if len(pipeline) > 0 {
		t.Pipeline = json.RawMessage(pipeline)
	}
	if len(stepConfig) > 0 {
		t.StepConfig = json.RawMessage(stepConfig)
	}
	t.GuardMode = NormalizeTaskGuardMode(t.GuardMode)
	t.GuardRuleIDs = parseGuardRuleIDs(guardRuleIDs)
	t.Pinned = t.PinnedAt != nil
	return &t, nil
}

// SetParentRef records a task's parent task id (编排 agent spawn_task 关联).
func (d *DB) SetParentRef(id int64, parentRef string) error {
	_, err := d.Exec(`UPDATE tasks SET parent_ref=NULLIF($2,'') WHERE id=$1`, id, parentRef)
	return err
}

// ListTasks returns alive tasks with pinned tasks first, then newest ids.
func (d *DB) ListTasks() ([]*Task, error) {
	// id 是 BIGSERIAL，同一时刻创建的任务也有稳定且唯一的顺序。
	rows, err := d.Query(`SELECT ` + taskCols + ` FROM tasks WHERE deleted_at IS NULL
ORDER BY (pinned_at IS NOT NULL) DESC, pinned_at DESC NULLS LAST, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if err := d.hydrateTasksContext(out); err != nil {
		return nil, err
	}
	return out, nil
}

// TaskPatch updates task list metadata without changing execution state.
//
// Guard 相关字段一律用指针:监管门限的"跟随全局"就是空串、规则集"不限定"就是空数组,
// 都是合法值。用零值判断会把"改回跟随全局"变成"没传,保持原样",用户就再也关不掉
// 自己开的门限——所以必须靠 nil(没传)与 &""/&[]int64{}(传空)区分。
type TaskPatch struct {
	Name   *string
	Pinned *bool
	// GuardMode: nil=不改;非 nil 时必须是合法取值(""/"inherit"/"off"/"enforce"),
	// 'inherit' 会归一成 '' 存储。
	GuardMode *string
	// GuardRuleIDs: nil=不改;非 nil 时整列替换(空切片=改回"不限定规则")。
	GuardRuleIDs *[]int64
}

// UpdateTask applies a partial task name/pin/guard mutation and returns the updated row.
// Re-pinning an already pinned task preserves its original position.
func (d *DB) UpdateTask(id int64, patch TaskPatch) (*Task, error) {
	// 门限归一与校验必须在写库前完成:非法值直接报错,不落到 CHECK 约束上被拒
	// (那样调用方只会看到一条含糊的约束错误,不知道是哪个字段不合法)。
	var guardMode *string
	if patch.GuardMode != nil {
		mode := NormalizeTaskGuardMode(*patch.GuardMode)
		if !ValidTaskGuardMode(mode) {
			return nil, fmt.Errorf("%w: %q", ErrTaskGuardModeInvalid, *patch.GuardMode)
		}
		guardMode = &mode
	}
	var guardRuleIDs []byte
	if patch.GuardRuleIDs != nil {
		raw, err := marshalGuardRuleIDs(NormalizeTaskGuardRuleIDs(*patch.GuardRuleIDs))
		if err != nil {
			return nil, err
		}
		guardRuleIDs = raw
	}
	task, err := scanTask(d.QueryRow(`UPDATE tasks SET
	name = CASE WHEN $2::boolean THEN $3 ELSE name END,
	pinned_at = CASE
		WHEN $4::boolean IS NULL THEN pinned_at
		WHEN $4::boolean THEN COALESCE(pinned_at, now())
		ELSE NULL
	END,
	guard_mode = CASE WHEN $5::boolean THEN $6 ELSE guard_mode END,
	guard_rule_ids = CASE WHEN $7::boolean THEN $8::jsonb ELSE guard_rule_ids END
WHERE id=$1 AND deleted_at IS NULL
RETURNING `+taskCols, id, patch.Name != nil, patch.Name, patch.Pinned,
		guardMode != nil, guardMode, patch.GuardRuleIDs != nil, guardRuleIDs))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return task, nil
}

// GetTask returns one alive task (nil if not found/deleted).
func (d *DB) GetTask(id int64) (*Task, error) {
	t, err := scanTask(d.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id=$1 AND deleted_at IS NULL`, id))
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	if err := d.hydrateTaskContext(t); err != nil {
		return nil, err
	}
	return t, nil
}

// SetPaused persists a task's paused flag.
func (d *DB) SetPaused(id int64, paused bool) error {
	_, err := d.Exec(`UPDATE tasks SET paused=$1 WHERE id=$2`, paused, id)
	return err
}

// Enqueue places a task at the tail of the persistent admission queue. Repeating
// the operation while it is already queued keeps its original FIFO position.
func (d *DB) Enqueue(id int64, mode string) error {
	if mode != "bootstrap" && mode != "resume" {
		return fmt.Errorf("invalid queue mode %q", mode)
	}
	_, err := d.Exec(`UPDATE tasks
SET queued=true,
    queued_at=CASE WHEN queued THEN COALESCE(queued_at, now()) ELSE now() END,
    queue_mode=CASE
        WHEN queue_mode='bootstrap' OR $2='bootstrap' THEN 'bootstrap'
        ELSE 'resume'
    END
WHERE id=$1`, id, mode)
	return err
}

// Dequeue removes the concurrency hold. clearMode is false when a user pauses a
// queued task, preserving whether its next admission must bootstrap or resume.
func (d *DB) Dequeue(id int64, clearMode bool) error {
	_, err := d.Exec(`UPDATE tasks
SET queued=false,
    queued_at=NULL,
    queue_mode=CASE WHEN $2 THEN '' ELSE queue_mode END
WHERE id=$1`, id, clearMode)
	return err
}

// SetQueued is the compatibility helper used by older callers and tests. New
// scheduling code should use Enqueue/Dequeue so FIFO metadata is explicit.
func (d *DB) SetQueued(id int64, queued bool) error {
	if queued {
		return d.Enqueue(id, "bootstrap")
	}
	return d.Dequeue(id, true)
}

// SetStatus updates a task's lifecycle status. Entering a terminal state
// (done/failed/timeout) stamps completed_at once (COALESCE keeps the first stamp
// stable); moving back to a non-terminal state clears it, so a re-run has no stale
// finish time.
func (d *DB) SetStatus(id int64, status string) error {
	_, err := d.Exec(`
UPDATE tasks
   SET status = $1,
       completed_at = CASE WHEN $1 IN ('done','failed','timeout') THEN COALESCE(completed_at, now()) ELSE NULL END
 WHERE id = $2`, status, id)
	return err
}

// SetTerminalStatusGuarded sets a terminal status only when the task is NOT already
// terminal, so a completed↔timeout race resolves to the first writer (won=true).
// Returns won=false (no error) when another terminal status already stuck — the
// caller then leaves it alone. Non-terminal transitions / re-run still use SetStatus.
func (d *DB) SetTerminalStatusGuarded(id int64, status string) (won bool, err error) {
	res, err := d.Exec(`
UPDATE tasks
   SET status = $1,
       completed_at = COALESCE(completed_at, now())
 WHERE id = $2 AND status NOT IN ('done','failed','timeout')`, status, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// StampFirstRun records a task's first-real-run moment and computes its absolute
// deadline (= now + timeoutSeconds). Idempotent: only stamps when first_run_at is
// still NULL, so restarts / re-entries keep the original clock. timeoutSeconds<=0
// leaves deadline_at NULL (不限时). Returns the resulting deadline (nil = 不限/未变).
func (d *DB) StampFirstRun(id int64, timeoutSeconds int) (*time.Time, error) {
	var deadline *time.Time
	err := d.QueryRow(`
UPDATE tasks
   SET first_run_at = COALESCE(first_run_at, now()),
       deadline_at  = CASE
           WHEN first_run_at IS NOT NULL THEN deadline_at            -- 已盖过章：不动
           WHEN $2 > 0 THEN now() + make_interval(secs => $2)
           ELSE NULL END
 WHERE id = $1
 RETURNING deadline_at`, id, timeoutSeconds).Scan(&deadline)
	return deadline, err
}

// DeleteTask preserves the historical behavior: remove the task and its
// exploration subgraph while retaining global assets.
func (d *DB) DeleteTask(id int64) error {
	_, err := d.DeleteTaskCascade(id, false, false)
	return err
}

// DeleteTaskCascade hard-deletes a task and its exploration subgraph
// (nodes/edges/activity via ON DELETE CASCADE). Optional standalone findings and
// assets owned only by this task are deleted in the same transaction; shared
// assets are retained and only have this task id detached. deleteLLMRecords is
// optional to preserve callers of the original two-option API.
func (d *DB) DeleteTaskCascade(id int64, deleteAssets, deleteFindings bool, deleteLLMRecords ...bool) (TaskDeleteResult, error) {
	deleteRecords := len(deleteLLMRecords) > 0 && deleteLLMRecords[0]
	return d.DeleteTaskCascadePrepared(id, deleteAssets, deleteFindings, deleteRecords, nil)
}

// DeleteTaskCascadePrepared coordinates reversible external deletion with the
// PostgreSQL cascade. When prepare is non-nil, host ownership is resolved and
// prepare is invoked inside this transaction while asset and anchor writers are
// excluded. The locks remain held through commit, closing the window where a
// host could become shared after its traffic had already been staged.
//
// prepare must only stage reversible work. Any returned error or later database
// error rolls PostgreSQL back; the caller remains responsible for rolling back
// external work that its callback staged successfully.
func (d *DB) DeleteTaskCascadePrepared(
	id int64,
	deleteAssets, deleteFindings, deleteLLMRecords bool,
	prepare func(TaskDeletePreparation) error,
) (TaskDeleteResult, error) {
	var result TaskDeleteResult
	tx, err := d.Begin()
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var expID int64
	if err := tx.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1 FOR UPDATE`, id).Scan(&expID); err != nil {
		if err == sql.ErrNoRows {
			return result, nil // already gone
		}
		return result, err
	}

	// SHARE ROW EXCLUSIVE conflicts with every INSERT/UPDATE/DELETE on these
	// tables and with another coordinated deletion. Taking both in one fixed order
	// prevents phantoms (a distinct asset row for the same host) as well as new
	// ownership/anchor references until the deletion transaction commits.
	if deleteAssets || prepare != nil {
		if _, err := tx.Exec(`LOCK TABLE assets, exploration_anchors IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			return result, err
		}
	}
	if prepare != nil {
		hosts, err := hostsForTaskDeletion(tx, id, expID)
		if err != nil {
			return result, err
		}
		if err := prepare(TaskDeletePreparation{
			ExplorationID: expID,
			TrafficHosts:  hosts,
		}); err != nil {
			return result, err
		}
	}
	if deleteAssets {
		// Ownership is the union of explicit task_ids and this exploration's anchors.
		// An asset is deletable only when no other live task references it through
		// either mechanism. This covers legacy anchor-only seeds without destroying
		// evidence anchored by another task.
		res, err := tx.Exec(`
WITH candidate_assets AS (
  SELECT id FROM assets WHERE $1 = ANY(task_ids)
  UNION
  SELECT ea.asset_id
  FROM exploration_anchors ea
  JOIN exploration_nodes n ON n.id=ea.node_id
  WHERE n.exploration_id=$2
),
deletable AS (
  SELECT a.id
  FROM assets a
  JOIN candidate_assets c ON c.id=a.id
  WHERE NOT EXISTS (
    SELECT 1 FROM tasks t
    WHERE t.id<>$1 AND t.deleted_at IS NULL AND t.id=ANY(a.task_ids)
  ) AND NOT EXISTS (
    SELECT 1
    FROM exploration_anchors ea
    JOIN exploration_nodes n ON n.id=ea.node_id
    JOIN tasks t ON t.exploration_id=n.exploration_id
    WHERE ea.asset_id=a.id AND t.id<>$1 AND t.deleted_at IS NULL
  )
)
DELETE FROM assets a USING deletable d WHERE a.id=d.id`, id, expID)
		if err != nil {
			return result, err
		}
		result.AssetsDeleted, _ = res.RowsAffected()

		res, err = tx.Exec(`UPDATE assets SET task_ids = array_remove(task_ids, $1) WHERE $1 = ANY(task_ids)`, id)
		if err != nil {
			return result, err
		}
		result.AssetsDetached, _ = res.RowsAffected()
	}
	if deleteFindings {
		res, err := tx.Exec(`DELETE FROM findings WHERE task_id = $1`, id)
		if err != nil {
			return result, err
		}
		result.FindingsDeleted, _ = res.RowsAffected()
	}
	if deleteLLMRecords {
		res, err := tx.Exec(`DELETE FROM llm_records WHERE COALESCE(task_id,'')=$1`, strconv.FormatInt(id, 10))
		if err != nil {
			return result, err
		}
		result.LLMRecordsDeleted, _ = res.RowsAffected()
	}
	// llm_usage (the token metering ledger) is intentionally NOT deleted with the
	// task — it is kept as historical accounting even after the task is gone.
	if _, err := tx.Exec(`DELETE FROM tasks WHERE id=$1`, id); err != nil {
		return result, err
	}
	if _, err := tx.Exec(`DELETE FROM explorations WHERE id=$1`, expID); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
