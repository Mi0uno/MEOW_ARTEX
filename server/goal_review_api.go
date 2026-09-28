package server

// 目标审核 API(契约 GOAL_REVIEW_CONTRACTS.md §3.2)。路由挂载在 server.go(registerGoalReviewRoutes)。

import (
	"net/http"
)

// registerGoalReviewRoutes 挂载:
//   GET /api/goal-reviews            → s.listGoalReviews   (?task= 必带,&goal_id=&limit=&offset=)
//   GET /api/goal-review/config      → s.getGoalReviewConfig
//   PUT /api/goal-review/config      → s.putGoalReviewConfig
func registerGoalReviewRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/goal-reviews", s_listGoalReviews)
	mux.HandleFunc("GET /api/goal-review/config", s_getGoalReviewConfig)
	mux.HandleFunc("PUT /api/goal-review/config", s_putGoalReviewConfig)
}

// TODO(goal-review Agent B): 三个 handler 按契约 §3.2 实现,删除本占位变量与转发。
var s_listGoalReviews = func(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusNotImplemented, "goal-reviews: 未接入")
	_ = r
}

// TODO(goal-review Agent B): 实现。
var s_getGoalReviewConfig = func(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusNotImplemented, "goal-review config: 未接入")
	_ = r
}

// TODO(goal-review Agent B): 实现(fail_mode ∉ {open,closed} 或 timeout ∉ [30,600] → 400)。
var s_putGoalReviewConfig = func(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusNotImplemented, "goal-review config: 未接入")
	_ = r
}
