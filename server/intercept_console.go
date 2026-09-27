package server

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/Autumn-27/artex/db"
)

// interceptConsole 服务审批台:GET /api/intercept/console
// (契约 APPROVAL_CONSOLE_CONTRACTS §1)。一次带回分页行、过滤后总数、五档计数与
// 工具清单;counts 不受 status 影响、tools 不受 tool 影响(各自的分组维度,与
// CVE 页 sev_counts 同约定)。
func (s *Server) interceptConsole(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	q := r.URL.Query()
	filter, err := interceptConsoleFilterParams(q)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	page, size := interceptPageParams(q)
	items, total, counts, tools, err := pg.ConsoleInterceptsPage(page, size, filter)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// 集合类字段全部兜底非 nil:Go 的 nil slice/map 序列化成 JSON null,前端对
	// null 迭代/取键会崩(本项目已踩过)。
	if items == nil {
		items = []db.InterceptApprovalRow{}
	}
	if counts == nil {
		counts = map[string]int{}
	}
	if tools == nil {
		tools = []db.ConsoleToolCount{}
	}
	writeJSON(w, 200, map[string]any{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": size,
		"counts":    counts,
		"tools":     tools,
	})
}

// interceptConsoleFilterParams 解析审批台的查询参数。status/decision_source 的
// 白名单与中文报错沿用 interceptFilterParams 的口径(只读复用口径、不改原函数——
// server/intercept.go 是并行 WIP);q/task_id/tool 直接透传,空串=不过滤。
// 纯函数,便于单测。
func interceptConsoleFilterParams(q url.Values) (db.InterceptApprovalFilter, error) {
	filter := db.InterceptApprovalFilter{
		Status:         q.Get("status"),
		DecisionSource: q.Get("decision_source"),
		Q:              q.Get("q"),
		TaskID:         q.Get("task_id"),
		Tool:           q.Get("tool"),
	}
	// flagged = AI判疑放通(标记但放行),与其他四档一样可筛。
	switch filter.Status {
	case "", "pending", "allowed", "denied", "timeout", "flagged":
	default:
		return filter, fmt.Errorf("status 必须是 pending、allowed、denied、timeout 或 flagged")
	}
	switch filter.DecisionSource {
	case "", "model", "rule", "unknown":
	default:
		return filter, fmt.Errorf("decision_source 必须是 model、rule 或 unknown")
	}
	return filter, nil
}
