package db

import (
	"database/sql"
	"encoding/json"
	"time"
)

// InterceptContextEntry is a bounded, recorded session event, not model reasoning.
type InterceptContextEntry struct {
	Kind      string `json:"kind"`
	Tool      string `json:"tool,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Text      string `json:"text"`
	IsError   bool   `json:"is_error,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// InterceptScriptFile 是审批时刻命令引用的本地脚本文件快照(审计附件):
// 转人工的理由常是「无法确认脚本内容」,审核人必须能看到要审的脚本本身,
// 而不是只有引用它的一行命令。快照在审批时抓取,之后文件的变更不改历史记录。
type InterceptScriptFile struct {
	Path      string `json:"path"`
	Content   string `json:"content,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Missing   bool   `json:"missing,omitempty"` // 快照读取失败(文件不存在或不可读)
}

// InterceptAudit is captured at review time. It is deliberately excluded from
// polling/list responses; old rows have no audit instead of reconstructed data.
type InterceptAudit struct {
	RunID            string                  `json:"run_id,omitempty"`
	ToolUseID        string                  `json:"tool_use_id,omitempty"`
	Correlation      string                  `json:"correlation"` // exact | ambiguous | unavailable
	InputDigest      string                  `json:"input_digest"`
	UserMessage      string                  `json:"user_message"`
	UserTruncated    bool                    `json:"user_truncated,omitempty"`
	Context          []InterceptContextEntry `json:"context"`
	ContextTruncated bool                    `json:"context_truncated,omitempty"`
	// ScriptFiles 汇总命令引用的解释器脚本内容快照(python x.py / bash x.sh 等),
	// 供审核台展示;读取失败记 Missing,绝不因快照失败改变审批流程。
	ScriptFiles      []InterceptScriptFile `json:"script_files,omitempty"`
	CapturedAt       time.Time             `json:"captured_at"`
	ModelFallback    bool                  `json:"model_fallback,omitempty"`
	ModelInput       json.RawMessage       `json:"model_input,omitempty"`
	ModelInputDigest string                `json:"model_input_digest,omitempty"`
	InitialAction    string                `json:"initial_action"`
	InitialReason    string                `json:"initial_reason"`
	EffectiveAction  string                `json:"effective_action,omitempty"`
	DecisionReason   string                `json:"decision_reason,omitempty"`
	RuleName         string                `json:"rule_name,omitempty"`
	ConfigDigest     string                `json:"config_digest,omitempty"`
	ProfileID        int64                 `json:"profile_id,omitempty"`
	ExecutionStatus  string                `json:"execution_status"`
	Output           string                `json:"output,omitempty"`
	OutputTruncated  bool                  `json:"output_truncated,omitempty"`
	ExecutionEndedAt *time.Time            `json:"execution_ended_at,omitempty"`
}

type InterceptDetail struct {
	InterceptApprovalRow
	Audit *InterceptAudit `json:"audit"`
}

func (d *DB) GetInterceptDetail(id int64) (*InterceptDetail, error) {
	var out InterceptDetail
	var raw []byte
	err := d.QueryRow(approvalRowSelectWithAudit+` WHERE ip.id=$1`, id).Scan(
		&out.ID, &out.RuleID, &out.ConversationID, &out.TaskID, &out.AgentName,
		&out.ToolName, &out.ToolInput, &out.Status, &out.Reason, &out.DecidedAt, &out.CreatedAt,
		&out.DecisionSource, &out.ConvTitle, &out.ConvAgentKey, &out.RuleName,
		&out.TaskName, &out.TaskStage, &out.TaskKind, &out.TaskSrc, &raw,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out.Audit); err != nil {
			return nil, err
		}
	}
	return &out, nil
}

// BackfillInterceptScriptFiles 把按需补抓的脚本快照并回 pending 行的 audit:
// 旧版本创建/快照失败的 pending 记录,审批人裁决时仍需要脚本内容。仅 pending
// 行可补(裁决后的历史记录保持原样);幂等,audit 为 NULL 时先起空对象。
func (d *DB) BackfillInterceptScriptFiles(id int64, files []InterceptScriptFile) error {
	if len(files) == 0 {
		return nil
	}
	raw, err := json.Marshal(map[string]any{"script_files": files})
	if err != nil {
		return err
	}
	_, err = d.Exec(`UPDATE intercept_pending SET audit = COALESCE(audit,'{}'::jsonb) || $2::jsonb
		WHERE id=$1 AND status='pending'`, id, raw)
	return err
}

// ResolveIntercept atomically settles a pending request. A timeout cannot
// overwrite a human decision and repeat decisions cannot rewrite history.
func (d *DB) ResolveIntercept(id int64, status, action, reason string) (bool, error) {
	execution := "not_executed"
	if action == "allow" {
		execution = "awaiting_result"
	}
	patch, err := json.Marshal(map[string]any{
		"effective_action": action, "decision_reason": reason, "execution_status": execution,
	})
	if err != nil {
		return false, err
	}
	r, err := d.Exec(`UPDATE intercept_pending SET status=$2, decided_at=NOW(),
		audit=CASE WHEN audit IS NULL THEN NULL ELSE audit || $3::jsonb ||
        CASE WHEN $4='allow' AND audit->>'correlation' IS DISTINCT FROM 'exact'
        THEN '{"execution_status":"unknown"}'::jsonb ELSE '{}'::jsonb END END
        WHERE id=$1 AND status='pending'`, id, status, patch, action)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

// CompleteIntercept only updates the exact recorded call after it was allowed.
// A blocked tool_result must never be presented as a failed execution.
func (d *DB) CompleteIntercept(id int64, runID, toolUseID, status, output string, truncated bool) error {
	patch, err := json.Marshal(map[string]any{
		"execution_status": status, "output": output, "output_truncated": truncated,
		"execution_ended_at": time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	_, err = d.Exec(`UPDATE intercept_pending SET audit=audit || $4::jsonb
		WHERE id=$1 AND audit->>'run_id'=$2 AND audit->>'tool_use_id'=$3
		AND audit->>'effective_action'='allow' AND audit->>'execution_status'='awaiting_result'`,
		id, runID, toolUseID, patch)
	return err
}
