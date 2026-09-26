package server

// 开机回填(契约 AI_MEMORY_CONTRACTS.md §8):
//
//	对「已终态但没有 run 行」的历史 agent 任务逐条写确定性复盘骨架——不打 LLM:
//	summary 取保底报告正文前 500 字(无报告则「(历史任务，无报告存档)」),
//	highlights=[发现 N 条],stats 计数,status='done',source='backfill'。
//	幂等:每条先查 MemoryRunByTask(等价于契约的 LEFT JOIN ... WHERE r.id IS NULL)。
//	在 server.go 里 go s.backfillMemoryRuns() 启动:goroutine + 每条独立,出错仅日志,
//	绝不影响启动耗时。

import (
	"log"
	"strconv"

	"github.com/Autumn-27/artex/db"
)

// backfillMemoryRuns 启动回填入口(阻塞版,由 server.go 用 go 调起)。
// 复用 ListTerminalTasks(终态集合 done/failed/timeout,与契约 §8 的 SQL 同义),
// 再按 kind='agent' + 无既有 run 逐条过滤——等价于契约的 LEFT JOIN 过滤链。
func (s *Server) backfillMemoryRuns() {
	rows, err := s.m.PG().ListTerminalTasks()
	if err != nil {
		log.Printf("[memory] 回填: 读取终态任务失败(下次启动重试): %v", err)
		return
	}
	n := 0
	for _, row := range rows {
		// 与复盘门禁(§5.2)同口径:仅 agent 任务的 exploit/post/'' 阶段。
		// recon/report 阶段任务不打 run,和终态钩子的行为保持一致。
		switch row.Stage {
		case "exploit", "post", "":
		default:
			continue
		}
		pt, err := s.m.PG().GetTask(row.ID)
		if err != nil || pt == nil || pt.Kind != "agent" {
			continue
		}
		if existing, err := s.m.PG().MemoryRunByTask(row.ID); err == nil && existing != nil {
			continue // 已有 run(等价 LEFT JOIN 未命中过滤)
		}
		if s.backfillOneMemoryRun(pt) {
			n++
		}
	}
	if n > 0 {
		log.Printf("[memory] 回填历史任务复盘骨架 %d 条", n)
	}
}

// backfillOneMemoryRun 写单条确定性 run;成功返回 true(出错仅日志)。
func (s *Server) backfillOneMemoryRun(pt *db.Task) bool {
	run := s.newSkeletonMemoryRun(pt.ID)
	// 库任务行是回填场景的权威来源:覆盖骨架里的 scope/阶段。
	run.Stage = pt.Stage
	run.ScopeCompanies = []int64{}
	run.ScopeSrcTarget = 0
	for _, sc := range s.memoryScopesForTaskID(pt.ID) {
		switch sc.Kind {
		case "company":
			run.ScopeCompanies = append(run.ScopeCompanies, sc.ID)
		case "src_target":
			run.ScopeSrcTarget = sc.ID
		}
	}
	run.Status = memoryStatusDone
	run.Source = memorySourceBackfill
	run.Failures = []db.MemoryRunFailure{}
	run.OpenLeads = []string{}

	g := s.gatherMemoryReflectData(pt.ID, pt)
	if g.reportHead == "" {
		run.Summary = "(历史任务，无报告存档)"
	} else {
		run.Summary = truncateRunes(g.reportHead, 500)
	}
	run.Highlights = []string{backfillHighlightsLine(g.findingsN)}
	run.Stats = map[string]any{"tokens_in": 0, "tokens_out": 0,
		"findings": g.findingsN, "assets": g.assetsN, "error_count": g.errorsN}
	if _, err := s.m.PG().UpsertMemoryRun(run); err != nil {
		log.Printf("[memory] 回填任务 %d 失败: %v", pt.ID, err)
		return false
	}
	return true
}

// backfillHighlightsLine 回填高亮:「发现 N 条」(与失败路径的确定性高亮同文案)。
func backfillHighlightsLine(findingsN int) string {
	return "发现 " + strconv.Itoa(findingsN) + " 条"
}
