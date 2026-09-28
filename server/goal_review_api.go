package server

// 目标审核 API(契约 GOAL_REVIEW_CONTRACTS.md §3.2)。路由挂载在 server.go(registerGoalReviewRoutes)。
//
// registerGoalReviewRoutes 是包级函数拿不到 Server 接收者(签名冻结)。照
// report_export_api.go 的 bindReportRouteServer atomic 模式:Server 实例走包级引用,
// 由 assembly.go 的 initGoalReview(启动期,经 seedOrchestrationTools 链)无条件绑定;
// 请求时再解引用。进程内单实例语义,取最后一次绑定。

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"

	"github.com/Autumn-27/artex/db"
)

// goalReviewServer 进程内 Server 引用(见文件头注释;进程内单实例语义)。
var goalReviewServer atomic.Pointer[Server]

// bindGoalReviewServer 回填路由层使用的 Server 引用(幂等,取最后一次)。
func bindGoalReviewServer(s *Server) {
	if s != nil {
		goalReviewServer.Store(s)
	}
}

// goalReviewRoute 把方法值包成 http.Handler:请求时再解 Server 引用(见上)。
func goalReviewRoute(h func(*Server, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := goalReviewServer.Load()
		if s == nil {
			writeErr(w, http.StatusServiceUnavailable, "服务初始化中,请稍后重试")
			return
		}
		h(s, w, r)
	}
}

// registerGoalReviewRoutes 挂载:
//
//	GET /api/goal-reviews            → s.listGoalReviews   (?task= 必带,&goal_id=&limit=&offset=)
//	GET /api/goal-review/config      → s.getGoalReviewConfig
//	PUT /api/goal-review/config      → s.putGoalReviewConfig
func registerGoalReviewRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/goal-reviews", goalReviewRoute((*Server).listGoalReviews))
	mux.HandleFunc("GET /api/goal-review/config", goalReviewRoute((*Server).getGoalReviewConfig))
	mux.HandleFunc("PUT /api/goal-review/config", goalReviewRoute((*Server).putGoalReviewConfig))
}

// listGoalReviews GET /api/goal-reviews?task=<id>&goal_id=&limit=&offset= —
// 目标审核记录列表(新者在前)。task 必带,缺/非法 → 400;返回 {items,total},
// total 过滤后算,items 永不为 null(空 → [])。
func (s *Server) listGoalReviews(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	taskID, err := strconv.ParseInt(q.Get("task"), 10, 64)
	if err != nil || taskID <= 0 {
		writeErr(w, http.StatusBadRequest, "task 参数必带且为正整数")
		return
	}
	goalID, _ := strconv.ParseInt(q.Get("goal_id"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	items, total, err := s.m.PG().ListGoalReviews(db.GoalReviewFilter{
		TaskID: taskID, GoalID: goalID, Limit: limit, Offset: offset,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if items == nil {
		items = []db.GoalReview{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

// getGoalReviewConfig GET /api/goal-review/config — 读审核配置(缺省 true/open/150)。
func (s *Server) getGoalReviewConfig(w http.ResponseWriter, r *http.Request) {
	enabled, failMode, timeoutSec, err := s.m.PG().GoalReviewConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": enabled, "fail_mode": failMode, "timeout_seconds": timeoutSec,
	})
}

// putGoalReviewConfig PUT /api/goal-review/config — 部分更新审核配置。
// body {enabled?, fail_mode?, timeout_seconds?};字段缺省=保持原值。
// fail_mode ∉ {open,closed} 或 timeout_seconds ∉ [30,600] → 400。
func (s *Server) putGoalReviewConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled        *bool   `json:"enabled"`
		FailMode       *string `json:"fail_mode"`
		TimeoutSeconds *int    `json:"timeout_seconds"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := s.m.PG().SetGoalReviewConfig(req.Enabled, req.FailMode, req.TimeoutSeconds); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error()) // 校验失败是调用方的问题
		return
	}
	enabled, failMode, timeoutSec, err := s.m.PG().GoalReviewConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": enabled, "fail_mode": failMode, "timeout_seconds": timeoutSec,
	})
}
