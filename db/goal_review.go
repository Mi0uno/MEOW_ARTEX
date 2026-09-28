package db

// 目标审核记录 CRUD(契约 GOAL_REVIEW_CONTRACTS.md §1)。
// 铁律:gaps 出参永不为 nil(空→[]string{});列表 total 在过滤后算;RETURNING 用 bare 列清单。

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// settings 键(契约 §1):缺省 enabled=true / failMode=open / timeoutSec=150(clamp 30-600)。
const (
	SettingGoalReviewEnabled = "goal_review_enabled"
	SettingGoalReviewFail    = "goal_review_fail_mode"
	SettingGoalReviewTimeout = "goal_review_timeout_seconds"
)

// GoalReview —— goal_reviews 表一行。
type GoalReview struct {
	ID         int64     `json:"id"`
	TaskID     int64     `json:"task_id"`
	GoalID     int64     `json:"goal_id"`
	EvidenceID int64     `json:"evidence_id"`
	Reason     string    `json:"reason"`
	Verdict    string    `json:"verdict"` // approved | rejected | unverified
	Confidence float64   `json:"confidence"`
	Reasons    string    `json:"reasons"`
	Gaps       []string  `json:"gaps"`
	Reviewer   string    `json:"reviewer"`
	DurationMS int64     `json:"duration_ms"`
	FailNote   string    `json:"fail_note"`
	CreatedAt  time.Time `json:"created_at"`
}

// goalReviewCols bare 列清单(无别名,铁律):插入 RETURNING 与查询共用同一列序。
const goalReviewCols = `id, task_id, goal_id, evidence_id, reason, verdict, confidence,
reasons, gaps, reviewer, duration_ms, fail_note, created_at`

// scanGoalReview 从一行组装 GoalReview;gaps NULL/空 → []string{}(永不为 nil)。
func scanGoalReview(sc interface{ Scan(...any) error }) (*GoalReview, error) {
	var r GoalReview
	var gaps []byte
	if err := sc.Scan(&r.ID, &r.TaskID, &r.GoalID, &r.EvidenceID, &r.Reason, &r.Verdict,
		&r.Confidence, &r.Reasons, &gaps, &r.Reviewer, &r.DurationMS, &r.FailNote, &r.CreatedAt); err != nil {
		return nil, err
	}
	r.Gaps = []string{}
	if len(gaps) > 0 {
		_ = json.Unmarshal(gaps, &r.Gaps) // 坏 JSON 退回空数组,不让脏行打崩列表
		if r.Gaps == nil {
			r.Gaps = []string{}
		}
	}
	return &r, nil
}

// CreateGoalReview 写一条审核记录(r.Gaps nil→JSON [])。返回新行 id。
func (d *DB) CreateGoalReview(r *GoalReview) (int64, error) {
	if r == nil {
		return 0, errors.New("goal_reviews: nil row")
	}
	switch r.Verdict {
	case "approved", "rejected", "unverified":
	default:
		return 0, fmt.Errorf("goal_reviews: 非法 verdict %q", r.Verdict)
	}
	gaps := r.Gaps
	if gaps == nil {
		gaps = []string{}
	}
	raw, err := json.Marshal(gaps)
	if err != nil {
		return 0, err
	}
	reviewer := r.Reviewer
	if reviewer == "" {
		reviewer = "goal_auditor"
	}
	row := d.QueryRow(`INSERT INTO goal_reviews
(task_id, goal_id, evidence_id, reason, verdict, confidence, reasons, gaps, reviewer, duration_ms, fail_note)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10,$11)
RETURNING `+goalReviewCols,
		r.TaskID, r.GoalID, r.EvidenceID, r.Reason, r.Verdict, r.Confidence, r.Reasons,
		string(raw), reviewer, r.DurationMS, r.FailNote)
	saved, err := scanGoalReview(row)
	if err != nil {
		return 0, err
	}
	*r = *saved // 回填自增 id/created_at 等库端默认值
	return r.ID, nil
}

// GoalReviewFilter —— 列表过滤。TaskID>0 必带;GoalID=0 全部。
type GoalReviewFilter struct {
	TaskID int64
	GoalID int64
	Limit  int // clamp 1-200,默认 50
	Offset int
}

// ListGoalReviews 过滤后分页(total=过滤后总数,铁律同 ListReports)。
func (d *DB) ListGoalReviews(filter GoalReviewFilter) ([]GoalReview, int, error) {
	if filter.TaskID <= 0 {
		return []GoalReview{}, 0, errors.New("goal_reviews: task_id 必带")
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	where := "task_id = $1"
	args := []any{filter.TaskID}
	if filter.GoalID > 0 {
		where += fmt.Sprintf(" AND goal_id = $%d", len(args)+1)
		args = append(args, filter.GoalID)
	}

	// total 在全部过滤条件之后算:它是「当前视图有多少条」,分页依赖它。
	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM goal_reviews WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := `SELECT ` + goalReviewCols + ` FROM goal_reviews WHERE ` + where +
		fmt.Sprintf(` ORDER BY id DESC LIMIT $%d OFFSET $%d`, len(args)+1, len(args)+2)
	args = append(args, limit, offset)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []GoalReview{}
	for rows.Next() {
		r, err := scanGoalReview(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *r)
	}
	return out, total, rows.Err()
}

// LatestGoalReviewFor 取该 goal+evidence 最近一次审核行(重复短路用);无行 → (nil, nil)。
func (d *DB) LatestGoalReviewFor(goalID, evidenceID int64) (*GoalReview, error) {
	r, err := scanGoalReview(d.QueryRow(
		`SELECT `+goalReviewCols+` FROM goal_reviews WHERE goal_id=$1 AND evidence_id=$2 ORDER BY id DESC LIMIT 1`,
		goalID, evidenceID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// GoalReviewConfig 读审核配置;缺省 enabled=true / failMode="open" / timeoutSec=150。
func (d *DB) GoalReviewConfig() (enabled bool, failMode string, timeoutSec int, err error) {
	enabled, failMode, timeoutSec = true, "open", 150
	if v, ok, e := d.GetSetting(SettingGoalReviewEnabled); e != nil {
		return false, "", 0, e
	} else if ok {
		enabled = v == "true" || v == "1"
	}
	if v, ok, e := d.GetSetting(SettingGoalReviewFail); e != nil {
		return false, "", 0, e
	} else if ok && (v == "open" || v == "closed") {
		failMode = v // 存了别的脏值 → 回落缺省 open(fail-open 语义)
	}
	if v, ok, e := d.GetSetting(SettingGoalReviewTimeout); e != nil {
		return false, "", 0, e
	} else if ok {
		if n, perr := strconv.Atoi(v); perr == nil {
			timeoutSec = clampGoalReviewTimeout(n)
		} // 解析失败 → 保持缺省 150
	}
	return enabled, failMode, timeoutSec, nil
}

// clampGoalReviewTimeout 把超时秒数收进 [30,600]。
func clampGoalReviewTimeout(n int) int {
	if n < 30 {
		return 30
	}
	if n > 600 {
		return 600
	}
	return n
}

// SetGoalReviewConfig 部分更新:指针=nil 保持原值。failMode ∉ {open,closed} 或 timeout ∉ [30,600] 报错。
func (d *DB) SetGoalReviewConfig(enabled *bool, failMode *string, timeoutSec *int) error {
	// 先全量校验再落库:避免「enabled 写进去、fail_mode 校验失败」的半截更新。
	mode := ""
	if failMode != nil {
		mode = *failMode
		if mode != "open" && mode != "closed" {
			return fmt.Errorf("fail_mode 必须是 open 或 closed,得到 %q", mode)
		}
	}
	if timeoutSec != nil && (*timeoutSec < 30 || *timeoutSec > 600) {
		return fmt.Errorf("timeout_seconds 必须在 30-600 之间,得到 %d", *timeoutSec)
	}
	if enabled != nil {
		v := "false"
		if *enabled {
			v = "true"
		}
		if err := d.SetSetting(SettingGoalReviewEnabled, v); err != nil {
			return err
		}
	}
	if failMode != nil {
		if err := d.SetSetting(SettingGoalReviewFail, mode); err != nil {
			return err
		}
	}
	if timeoutSec != nil {
		if err := d.SetSetting(SettingGoalReviewTimeout, strconv.Itoa(clampGoalReviewTimeout(*timeoutSec))); err != nil {
			return err
		}
	}
	return nil
}

// TaskGoalBrief 是审核 TaskBrief 的数据面:任务原始需求(input 名称/描述/任务目标/阶段)
// + 探索图根目标(explorations.goal)。tasks 与 explorations 1:1(tasks.exploration_id)。
type TaskGoalBrief struct {
	TaskID      int64  `json:"task_id"`
	Name        string `json:"name"`        // 任务名称(空=未命名)
	Description string `json:"description"` // 任务描述(创建时的原始输入)
	Goal        string `json:"goal"`        // tasks.goal(任务目标)
	Stage       string `json:"stage"`       // recon/exploit/post
	ExpGoal     string `json:"exp_goal"`    // explorations.goal(探索图根目标)
}

// TaskGoalBrief 读任务的审核简报数据(JOIN tasks+explorations);任务不存在/已删 → (nil, nil)。
// 本方法只读,不改 exploration.go(契约 §2.6 的并行约定)。
func (d *DB) TaskGoalBrief(taskID int64) (*TaskGoalBrief, error) {
	var b TaskGoalBrief
	err := d.QueryRow(`SELECT t.id, COALESCE(t.name,''), t.description, t.goal,
COALESCE(t.stage,'exploit'), e.goal
FROM tasks t
JOIN explorations e ON e.id = t.exploration_id
WHERE t.id = $1 AND t.deleted_at IS NULL`, taskID).
		Scan(&b.TaskID, &b.Name, &b.Description, &b.Goal, &b.Stage, &b.ExpGoal)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}
