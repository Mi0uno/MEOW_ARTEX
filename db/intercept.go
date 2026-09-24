package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// InterceptRule is one row of intercept_rules.
type InterceptRule struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	Enabled        bool      `json:"enabled"`
	Priority       int       `json:"priority"`
	MatchTarget    string    `json:"match_target"`
	MatchType      string    `json:"match_type"`
	Pattern        string    `json:"pattern"`
	Action         string    `json:"action"`
	Message        string    `json:"message"`
	TimeoutEnabled bool      `json:"timeout_enabled"`
	TimeoutSeconds int       `json:"timeout_seconds"`
	TimeoutAction  string    `json:"timeout_action"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// InterceptPending is one row of intercept_pending.
type InterceptPending struct {
	ID             int64           `json:"id"`
	RuleID         *int64          `json:"rule_id"`
	ConversationID *int64          `json:"conversation_id"`
	TaskID         *string         `json:"task_id"`
	AgentName      string          `json:"agent_name"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	Status         string          `json:"status"`
	DecisionSource string          `json:"decision_source"`
	Reason         string          `json:"reason"` // 规则 message 或模型判定理由(前缀 [模型])
	DecidedAt      *time.Time      `json:"decided_at"`
	CreatedAt      time.Time       `json:"created_at"`
}

const interceptRuleCols = `id, name, enabled, priority, match_target, match_type, pattern, action, message, timeout_enabled, timeout_seconds, timeout_action, created_at, updated_at`

func scanInterceptRule(row interface{ Scan(...any) error }) (InterceptRule, error) {
	var r InterceptRule
	err := row.Scan(&r.ID, &r.Name, &r.Enabled, &r.Priority,
		&r.MatchTarget, &r.MatchType, &r.Pattern, &r.Action, &r.Message,
		&r.TimeoutEnabled, &r.TimeoutSeconds, &r.TimeoutAction,
		&r.CreatedAt, &r.UpdatedAt)
	return r, err
}

// ListInterceptRules returns all rules ordered by priority DESC then id.
func (d *DB) ListInterceptRules() ([]InterceptRule, error) {
	rows, err := d.Query(`SELECT ` + interceptRuleCols + ` FROM intercept_rules ORDER BY priority DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InterceptRule
	for rows.Next() {
		r, err := scanInterceptRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CreateInterceptRule inserts a new rule.
func (d *DB) CreateInterceptRule(name, matchTarget, matchType, pattern, action, message string, priority int, enabled bool, timeoutEnabled bool, timeoutSeconds int, timeoutAction string) (InterceptRule, error) {
	row := d.QueryRow(`
INSERT INTO intercept_rules(name, enabled, priority, match_target, match_type, pattern, action, message, timeout_enabled, timeout_seconds, timeout_action)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING `+interceptRuleCols,
		name, enabled, priority, matchTarget, matchType, pattern, action, message, timeoutEnabled, timeoutSeconds, timeoutAction)
	return scanInterceptRule(row)
}

// UpdateInterceptRule replaces all editable fields of an existing rule.
func (d *DB) UpdateInterceptRule(id int64, name, matchTarget, matchType, pattern, action, message string, priority int, enabled bool, timeoutEnabled bool, timeoutSeconds int, timeoutAction string) (InterceptRule, error) {
	row := d.QueryRow(`
UPDATE intercept_rules
   SET name=$2, enabled=$3, priority=$4, match_target=$5,
       match_type=$6, pattern=$7, action=$8, message=$9,
       timeout_enabled=$10, timeout_seconds=$11, timeout_action=$12
WHERE id=$1
RETURNING `+interceptRuleCols,
		id, name, enabled, priority, matchTarget, matchType, pattern, action, message, timeoutEnabled, timeoutSeconds, timeoutAction)
	return scanInterceptRule(row)
}

// DeleteInterceptRule removes a rule.
func (d *DB) DeleteInterceptRule(id int64) error {
	_, err := d.Exec(`DELETE FROM intercept_rules WHERE id=$1`, id)
	return err
}

// ToggleInterceptRule flips the enabled state of a rule.
func (d *DB) ToggleInterceptRule(id int64, enabled bool) error {
	_, err := d.Exec(`UPDATE intercept_rules SET enabled=$2 WHERE id=$1`, id, enabled)
	return err
}

// CreateInterceptPending inserts a pending approval record and returns its ID.
// convID == 0 → conversation_id stored as NULL (background task).
// taskID == "" → task_id stored as NULL.
func (d *DB) CreateInterceptPending(ruleID, convID int64, taskID, agentName, toolName string, input []byte, reason string, audits ...*InterceptAudit) (int64, error) {
	raw := json.RawMessage(input)
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	var convIDPtr *int64
	if convID != 0 {
		convIDPtr = &convID
	}
	var taskIDPtr *string
	if taskID != "" {
		taskIDPtr = &taskID
	}
	// ruleID == 0 → NULL: the LLM fallback judge has no owning rule.
	var ruleIDPtr *int64
	if ruleID != 0 {
		ruleIDPtr = &ruleID
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO intercept_pending(rule_id, conversation_id, task_id, agent_name, tool_name, tool_input, reason, decision_source, audit)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		ruleIDPtr, convIDPtr, taskIDPtr, agentName, toolName, raw, reason, interceptSource(ruleID, reason), firstAudit(audits)).Scan(&id)
	return id, err
}

// DecideInterceptPending updates a pending record's status (allowed/denied/timeout).
func (d *DB) DecideInterceptPending(id int64, status string) error {
	_, err := d.Exec(`UPDATE intercept_pending SET status=$2, decided_at=NOW() WHERE id=$1`, id, status)
	return err
}

// CreateDecidedIntercept inserts an intercept_pending row ALREADY in a final state
// (status = 'allowed' | 'denied' | 'flagged'), decided_at stamped now. Used to log
// allow/deny/flag rule matches for observability — they don't block and need no user
// action, so unlike CreateInterceptPending (which starts 'pending') this records the
// outcome directly. 'flagged' is the AI判疑(标记但放通) audit state: the tool DID run,
// the row exists only so a suspicious-but-passed call stays reviewable. The accepted
// values are enforced by intercept_pending_status_check in schema.sql.
func (d *DB) CreateDecidedIntercept(ruleID, convID int64, taskID, agentName, toolName string, input []byte, status, reason string, audits ...*InterceptAudit) (int64, error) {
	raw := json.RawMessage(input)
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	var convIDPtr *int64
	if convID != 0 {
		convIDPtr = &convID
	}
	var taskIDPtr *string
	if taskID != "" {
		taskIDPtr = &taskID
	}
	// ruleID == 0 → NULL: the LLM fallback judge has no owning rule.
	var ruleIDPtr *int64
	if ruleID != 0 {
		ruleIDPtr = &ruleID
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO intercept_pending(rule_id, conversation_id, task_id, agent_name, tool_name, tool_input, status, reason, decided_at, decision_source, audit)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW(), $9, $10) RETURNING id`,
		ruleIDPtr, convIDPtr, taskIDPtr, agentName, toolName, raw, status, reason, interceptSource(ruleID, reason), firstAudit(audits)).Scan(&id)
	return id, err
}

const interceptPendingCols = `id, rule_id, conversation_id, task_id, agent_name, tool_name, tool_input, status, reason, decided_at, created_at, decision_source`

func scanInterceptPending(s interface{ Scan(...any) error }, p *InterceptPending) error {
	return s.Scan(&p.ID, &p.RuleID, &p.ConversationID, &p.TaskID, &p.AgentName,
		&p.ToolName, &p.ToolInput, &p.Status, &p.Reason, &p.DecidedAt, &p.CreatedAt, &p.DecisionSource)
}

// ListPendingIntercepts returns all unresolved approval requests, newest first.
func (d *DB) ListPendingIntercepts() ([]InterceptPending, error) {
	rows, err := d.Query(`SELECT ` + interceptPendingCols + ` FROM intercept_pending WHERE status='pending' ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InterceptPending
	for rows.Next() {
		var p InterceptPending
		if err := scanInterceptPending(rows, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetInterceptPending returns one pending record (nil if absent).
func (d *DB) GetInterceptPending(id int64) (*InterceptPending, error) {
	var p InterceptPending
	err := scanInterceptPending(
		d.QueryRow(`SELECT `+interceptPendingCols+` FROM intercept_pending WHERE id=$1`, id),
		&p,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &p, err
}

// InterceptApprovalRow is intercept_pending enriched with conversation and rule info.
type InterceptApprovalRow struct {
	InterceptPending
	ConvTitle    string `json:"conv_title"`
	ConvAgentKey string `json:"conv_agent_key"`
	RuleName     string `json:"rule_name"`
}

func scanInterceptApprovalRow(rows interface{ Scan(...any) error }, r *InterceptApprovalRow) error {
	return rows.Scan(
		&r.ID, &r.RuleID, &r.ConversationID, &r.TaskID, &r.AgentName,
		&r.ToolName, &r.ToolInput, &r.Status, &r.Reason, &r.DecidedAt, &r.CreatedAt,
		&r.DecisionSource, &r.ConvTitle, &r.ConvAgentKey, &r.RuleName,
	)
}

// Keep legacy rows without decision_source consistent with their displayed source.
const approvalDecisionSource = `COALESCE(NULLIF(ip.decision_source,''), CASE
 WHEN ip.rule_id IS NOT NULL THEN 'rule'
 WHEN ip.reason LIKE '[模型]%' THEN 'model' ELSE 'unknown' END)`

const approvalRowColumns = `ip.id, ip.rule_id, ip.conversation_id, ip.task_id, ip.agent_name,
       ip.tool_name, ip.tool_input, ip.status, ip.reason, ip.decided_at, ip.created_at, ` + approvalDecisionSource + `,
       COALESCE(c.title,'') AS conv_title,
       COALESCE(c.agent_key,'') AS conv_agent_key,
       COALESCE(ir.name,'') AS rule_name`

const approvalRowJoins = `
FROM intercept_pending ip
LEFT JOIN conversations c ON c.id = ip.conversation_id
LEFT JOIN intercept_rules ir ON ir.id = ip.rule_id`

const approvalRowSelect = `SELECT ` + approvalRowColumns + approvalRowJoins
const approvalRowSelectWithAudit = `SELECT ` + approvalRowColumns + `, ip.audit` + approvalRowJoins

// interceptSource 归类一条审计行的判定来源。flag 与其他动作走同一条规则:
// 由规则产生的(ruleID!=0)是 rule,由模型/决策引擎产生的是 model(reason 带
// [模型] 前缀,见 intercept.finishEngineDecision 与 Judge 的消息前缀)。因此
// 新增 flag 档不需要在这里加分支——只要调用方保证 RuleID 与 reason 前缀如实。
func interceptSource(ruleID int64, reason string) string {
	if ruleID != 0 {
		return "rule"
	}
	if strings.HasPrefix(reason, "[模型]") {
		return "model"
	}
	return "unknown"
}

func firstAudit(audits []*InterceptAudit) any {
	if len(audits) == 0 || audits[0] == nil {
		return nil
	}
	raw, err := json.Marshal(audits[0])
	if err != nil {
		return nil
	}
	return raw
}

// ListAllIntercepts returns up to limit intercept_pending rows (newest first)
// joined with conversation and rule info.
func (d *DB) ListAllIntercepts(limit int) ([]InterceptApprovalRow, error) {
	rows, err := d.Query(approvalRowSelect+` ORDER BY ip.created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InterceptApprovalRow
	for rows.Next() {
		var r InterceptApprovalRow
		if err := scanInterceptApprovalRow(rows, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InterceptApprovalFilter combines exact status and decision-source filters.
// Empty fields include all values.
type InterceptApprovalFilter struct {
	Status         string
	DecisionSource string
}

// ListAllInterceptsPage returns one 1-based page and the total matching count.
func (d *DB) ListAllInterceptsPage(page, size int, filter InterceptApprovalFilter) ([]InterceptApprovalRow, int, error) {
	return d.listInterceptsPage("", page, size, filter)
}

// ListTaskIntercepts returns all intercept_pending rows for a specific task (newest first).
func (d *DB) ListTaskIntercepts(taskID string) ([]InterceptApprovalRow, error) {
	rows, err := d.Query(approvalRowSelect+` WHERE ip.task_id=$1 ORDER BY ip.created_at DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InterceptApprovalRow
	for rows.Next() {
		var r InterceptApprovalRow
		if err := scanInterceptApprovalRow(rows, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListTaskInterceptsPage is the paginated variant of ListTaskIntercepts.
func (d *DB) ListTaskInterceptsPage(taskID string, page, size int, filter InterceptApprovalFilter) ([]InterceptApprovalRow, int, error) {
	return d.listInterceptsPage(taskID, page, size, filter)
}

// --- AI监管总览聚合 ---
//
// 这三个查询服务于 GET /api/intercept/guard-overview:AI监管页要在一次请求里拿到
// "监管是否生效 + 五档动作各自命中多少"。聚合放在 SQL 侧(而不是把全部规则/审计行
// 拉到 Go 里数),因为 intercept_pending 是持续增长的流水表,页面只需要计数。

// CountInterceptRules returns (total, enabled) rule counts.
func (d *DB) CountInterceptRules() (int, int, error) {
	var total, enabled int
	err := d.QueryRow(`SELECT COUNT(*), COUNT(*) FILTER (WHERE enabled) FROM intercept_rules`).Scan(&total, &enabled)
	return total, enabled, err
}

// CountInterceptRulesByAction returns rule counts keyed by action. Never nil: a
// nil map would serialise as JSON null and break the frontend, so the caller
// always gets an empty map when there are no rules.
func (d *DB) CountInterceptRulesByAction() (map[string]int, error) {
	rows, err := d.Query(`SELECT action, COUNT(*) FROM intercept_rules GROUP BY action`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var action string
		var n int
		if err := rows.Scan(&action, &n); err != nil {
			return nil, err
		}
		out[action] = n
	}
	return out, rows.Err()
}

// CountInterceptPendingByStatus returns audit-row counts keyed by status
// (pending/allowed/denied/timeout/flagged). Never nil, same reason as above.
func (d *DB) CountInterceptPendingByStatus() (map[string]int, error) {
	rows, err := d.Query(`SELECT status, COUNT(*) FROM intercept_pending GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		out[status] = n
	}
	return out, rows.Err()
}

func (d *DB) listInterceptsPage(taskID string, page, size int, filter InterceptApprovalFilter) ([]InterceptApprovalRow, int, error) {
	if page < 1 {
		page = 1
	}
	if size <= 0 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	offset := (page - 1) * size

	conditions := []string{}
	args := []any{}
	add := func(column, value string) {
		if value != "" {
			args = append(args, value)
			conditions = append(conditions, column+"=$"+fmt.Sprint(len(args)))
		}
	}
	add("ip.task_id", taskID)
	add("ip.status", filter.Status)
	add(approvalDecisionSource, filter.DecisionSource)
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}
	var total int
	if err := d.QueryRow("SELECT COUNT(*) FROM intercept_pending ip"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limitArg := len(args) + 1
	offsetArg := limitArg + 1
	dataQ := approvalRowSelect + where +
		" ORDER BY ip.created_at DESC, ip.id DESC LIMIT $" + fmt.Sprint(limitArg) +
		" OFFSET $" + fmt.Sprint(offsetArg)
	args = append(args, size, offset)
	rows, err := d.Query(dataQ, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []InterceptApprovalRow{}
	for rows.Next() {
		var r InterceptApprovalRow
		if err := scanInterceptApprovalRow(rows, &r); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}
