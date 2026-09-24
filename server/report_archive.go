package server

// 报告中心自动汇聚:平台产出的报告不再依赖人工点击归档。
//
// 两条自动来源:
//   ① 任务进入终态(done/failed/timeout)→ 自动渲染并归档任务总结报告;
//   ② 漏洞详细报告被写入(findings.report 非空)→ 自动归档漏洞报告。
//
// 归属与联动:报告经 task_id 关联任务、任务经 task_companies 关联企业,
// 报告中心因此可按企业(资产主体)筛选与分类,与信息搜集的资产图同一条链。
// 人工归档(任务报告页按钮 / archive_task_report 工具)仍可用,覆盖同一行。

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/report"
)

// autoArchiveTaskReport 任务进入终态时自动归档其总结报告。
// 由 Manager 在状态落库成功后异步调用(见 manager.SetTaskStatus 的终态分支)。
// 幂等:同一任务重复归档覆盖同一行(UpsertTaskReport)。
func (s *Server) autoArchiveTaskReport(taskID int64) {
	t := s.m.ResolveTask(strconv.FormatInt(taskID, 10))
	if t == nil {
		return
	}
	// 报告识别任务(report 阶段)与信息搜集流水线不产渗透总结报告,跳过。
	if t.Stage == "report" || t.Stage == "recon" {
		return
	}
	title, summary, md := s.renderTaskReport(t)
	if strings.TrimSpace(md) == "" {
		return
	}
	id, created, err := s.m.PG().UpsertTaskReport(taskID, title, summary, md)
	if err != nil {
		log.Printf("[report-archive] 任务 %d 自动归档失败: %v", taskID, err)
		return
	}
	kind := "updated"
	if created {
		kind = "created"
	}
	s.logReportEventTask(id, taskID, kind, "system", map[string]any{
		"source": "auto_task_terminal", "words": len([]rune(md)),
	})
	log.Printf("[report-archive] 任务 %d 总结报告已自动归档 #%d(新建=%v)", taskID, id, created)
}

// wireFindingHooks 把 report_finding 登记后的联动挂上:
//
//	① 漏洞库自动归类(AI 判 0day/nday/cve 后入库);
//	② SRC 任务产出成果自动建记录(仅 src_mode 任务)。
//
// 在 Server 构造时调用一次;回调内部各自起 goroutine,不阻塞工具返回。
func (s *Server) wireFindingHooks() {
	agent.OnFindingRecorded = func(findingID, taskID int64) {
		go s.autoClassifyFinding(findingID)
		if taskID > 0 {
			go s.autoCreateSRCRewardForFinding(findingID, taskID)
		}
	}
}

// autoCreateSRCRewardForFinding 任务在 SRC 模式下产出漏洞时自动建成果记录。
// 非 SRC 任务静默跳过;同一 finding 幂等(重复调用只建一条)。
func (s *Server) autoCreateSRCRewardForFinding(findingID, taskID int64) {
	t := s.m.ResolveTask(strconv.FormatInt(taskID, 10))
	if t == nil || !t.SRCMode {
		return
	}
	f, err := s.m.PG().GetFinding(findingID)
	if err != nil || f == nil {
		return
	}
	// 归属企业:任务关联的第一个企业(创建 SRC 任务时通常会给)。
	// 新模型下企业不再是必要条件——平台/目标归属才是 SRC 的核心维度,
	// 所以这里不再因缺企业而直接返回,避免「没有企业归属的 SRC 任务」丢成果。
	var companyID int64
	if len(t.CompanyIDs) > 0 {
		companyID = t.CompanyIDs[0]
	}
	if companyID <= 0 && t.SRCPlatformID <= 0 && t.SRCTargetID <= 0 {
		return
	}
	title := strings.TrimSpace(f.Name)
	if title == "" {
		title = strings.TrimSpace(f.VulnClass)
	}
	if title == "" {
		title = fmt.Sprintf("发现 #%d", f.ID)
	}
	id, created, err := s.m.PG().AutoCreateSRCReward(companyID, t.SRCPlatformID, t.SRCTargetID, taskID, findingID, title,
		strings.ToLower(strings.TrimSpace(f.Severity)), strings.TrimSpace(f.VulnClass))
	if err != nil {
		log.Printf("[src] 任务 %d 成果自动创建失败(发现 %d): %v", taskID, findingID, err)
		return
	}
	if created {
		log.Printf("[src] 任务 %d 产出成果 #%d(发现 %d)", taskID, id, findingID)
	}
}

// autoArchiveFindingReport 漏洞详细报告写入后自动归档。// 由 update_finding_report 工具与 report_finding 之后的写入路径调用;
// findings.report 为空时不归档(还没有正文,归档只会得到空壳)。
func (s *Server) autoArchiveFindingReport(findingID int64) {
	if findingID <= 0 {
		return
	}
	f, err := s.m.PG().GetFinding(findingID)
	if err != nil || f == nil {
		return
	}
	if strings.TrimSpace(f.Report) == "" {
		return
	}
	title := strings.TrimSpace(f.Name)
	if title == "" {
		title = strings.TrimSpace(f.VulnClass)
	}
	if title == "" {
		title = "未分类漏洞"
	}
	md := report.SingleFindingMarkdown(f, time.Now())
	id, created, err := s.m.PG().UpsertFindingReport(f.ID, f.TaskID, title, strings.TrimSpace(f.Summary), md)
	if err != nil {
		log.Printf("[report-archive] 发现 %d 自动归档失败: %v", findingID, err)
		return
	}
	kind := "updated"
	if created {
		kind = "created"
	}
	detail := map[string]any{"source": "auto_finding_report", "finding_id": f.ID, "words": len([]rune(md))}
	if f.TaskID != nil {
		s.logReportEventTask(id, *f.TaskID, kind, "system", detail)
	} else {
		s.logReportEvent(id, kind, "system", detail)
	}
	log.Printf("[report-archive] 发现 %d 漏洞报告已自动归档 #%d(新建=%v)", findingID, id, created)
}

// backfillReportArchive 启动时补齐历史产出:把「已有正文但尚未入库」的漏洞报告
// 与「已终态但未归档」的任务报告补进报告中心,并回填任务的企业归属。
// 一次性(settings flag 守卫),失败只记日志不阻塞启动。
func (s *Server) backfillReportArchive() {
	const flag = "report_auto_archive_backfill_v1"
	if v, _, _ := s.m.PG().GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.PG().SetSetting(flag, "true") }()

	// ① 已写正文但没入库的漏洞报告。
	findings, err := s.m.PG().ListFindingsWithReport()
	if err != nil {
		log.Printf("[report-archive] 回填: 读取漏洞报告失败: %v", err)
	} else {
		n := 0
		for _, f := range findings {
			if strings.TrimSpace(f.Report) == "" {
				continue
			}
			if r, err := s.m.PG().GetReportByFinding(f.ID); err == nil && r != nil {
				continue // 已有存档
			}
			title := strings.TrimSpace(f.Name)
			if title == "" {
				title = strings.TrimSpace(f.VulnClass)
			}
			if title == "" {
				title = "未分类漏洞"
			}
			md := report.SingleFindingMarkdown(f, time.Now())
			if _, _, err := s.m.PG().UpsertFindingReport(f.ID, f.TaskID, title, strings.TrimSpace(f.Summary), md); err != nil {
				log.Printf("[report-archive] 回填发现 %d 失败: %v", f.ID, err)
				continue
			}
			n++
		}
		log.Printf("[report-archive] 回填漏洞报告 %d 份", n)
	}

	// ② 已终态但未归档的任务报告(仅渗透/后渗透阶段,流水线与识别任务不产报告)。
	tasks, err := s.m.PG().ListTerminalTasks()
	if err != nil {
		log.Printf("[report-archive] 回填: 读取终态任务失败: %v", err)
		return
	}
	n := 0
	for _, pt := range tasks {
		if pt.Stage == "recon" || pt.Stage == "report" {
			continue
		}
		if r, err := s.m.PG().GetReportByTask(pt.ID); err == nil && r != nil {
			continue // 已有存档
		}
		t := s.m.ResolveTask(strconv.FormatInt(pt.ID, 10))
		if t == nil {
			log.Printf("[report-archive] 回填任务 %d 跳过: 任务未在内存中加载", pt.ID)
			continue
		}
		title, summary, md := s.renderTaskReport(t)
		if strings.TrimSpace(md) == "" {
			log.Printf("[report-archive] 回填任务 %d 跳过: 报告正文为空", pt.ID)
			continue
		}
		if _, _, err := s.m.PG().UpsertTaskReport(pt.ID, title, summary, md); err != nil {
			log.Printf("[report-archive] 回填任务 %d 失败: %v", pt.ID, err)
			continue
		}
		n++
	}
	log.Printf("[report-archive] 回填任务报告 %d 份", n)
}
