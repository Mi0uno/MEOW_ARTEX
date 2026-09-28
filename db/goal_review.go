package db

// 目标审核记录 CRUD(契约 GOAL_REVIEW_CONTRACTS.md §1)。
// 铁律:gaps 出参永不为 nil(空→[]string{});列表 total 在过滤后算;RETURNING 用 bare 列清单。

import (
	"errors"
	"time"
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

// GoalReviewFilter —— 列表过滤。TaskID>0 必带;GoalID=0 全部。
type GoalReviewFilter struct {
	TaskID int64
	GoalID int64
	Limit  int // clamp 1-200,默认 50
	Offset int
}

// CreateGoalReview 写一条审核记录(r.Gaps nil→JSON [])。返回新行 id。
//
// TODO(goal-review Agent B): 按契约 §1 实现。
func (d *DB) CreateGoalReview(r *GoalReview) (int64, error) {
	_ = r
	return 0, errors.New("goal_reviews: 未接入")
}

// ListGoalReviews 过滤后分页(total=过滤后总数,铁律同 ListReports)。
//
// TODO(goal-review Agent B): 按契约 §1 实现。
func (d *DB) ListGoalReviews(filter GoalReviewFilter) ([]GoalReview, int, error) {
	_ = filter
	return []GoalReview{}, 0, nil
}

// LatestGoalReviewFor 取该 goal+evidence 最近一次审核行(重复短路用);无行 → (nil, nil)。
//
// TODO(goal-review Agent B): 按契约 §1 实现。
func (d *DB) LatestGoalReviewFor(goalID, evidenceID int64) (*GoalReview, error) {
	_ = goalID
	_ = evidenceID
	return nil, nil
}

// GoalReviewConfig 读审核配置;缺省 enabled=true / failMode="open" / timeoutSec=150。
//
// TODO(goal-review Agent B): 按契约 §1 实现(settings 键 goal_review_enabled /
// goal_review_fail_mode / goal_review_timeout_seconds,经 GetSetting)。
func (d *DB) GoalReviewConfig() (enabled bool, failMode string, timeoutSec int, err error) {
	return true, "open", 150, nil
}

// SetGoalReviewConfig 部分更新:指针=nil 保持原值。failMode ∉ {open,closed} 或 timeout ∉ [30,600] 报错。
//
// TODO(goal-review Agent B): 按契约 §1 实现(经 SetSetting)。
func (d *DB) SetGoalReviewConfig(enabled *bool, failMode *string, timeoutSec *int) error {
	_ = enabled
	_ = failMode
	_ = timeoutSec
	return errors.New("goal_reviews: 未接入")
}
