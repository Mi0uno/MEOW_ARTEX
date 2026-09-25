package server

import (
	"net/http"

	"github.com/Autumn-27/artex/db"
)

// srcGuardRecentBlocksLimit 是「SRC 监管」板块 recent_blocks 的截断条数:
// 面板只要最近流水(契约允许后端定截断),取多了拖慢聚合、取少了看不到现场。
const srcGuardRecentBlocksLimit = 20

// srcGuardOverview 一次返回监管&规则中心「SRC 监管」板块的聚合数据
// (GET /api/src/guard/overview,响应结构与 web/src/lib/types.ts 的
// SRCGuardOverview 严格对齐,字段名不许改):
//
//   - src_task_count / src_task_active:SRC 模式任务总数与活跃数(运行中或排队);
//   - rules_scoped_src / rules_total:scope='src' 的启用规则数 / 全部启用规则数;
//   - recent_blocks:SRC 任务最近 20 条非放行的拦截流水(被拦截/待审/判疑),时间倒序。
//
// 集合类字段 recent_blocks 必须非 nil:Go 把 nil 切片序列化成 JSON null,前端对
// null 做 map/length 操作会直接崩(与 interceptGuardOverview 同一条项目红线)。
func (s *Server) srcGuardOverview(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	taskTotal, taskActive, err := pg.CountSRCModeTasks()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	rulesScoped, rulesTotal, err := pg.CountInterceptRulesByScope()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	blocks, err := pg.ListSRCRecentBlocks(srcGuardRecentBlocksLimit)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if blocks == nil {
		blocks = []db.SRCGuardBlock{}
	}
	writeJSON(w, 200, map[string]any{
		"src_task_count":   taskTotal,
		"src_task_active":  taskActive,
		"rules_scoped_src": rulesScoped,
		"rules_total":      rulesTotal,
		"recent_blocks":    blocks,
	})
}
