package server

import (
	"context"
	"encoding/json"

	actool "github.com/Autumn-27/norma/tool"
)

// reportTools 是报告产出侧的 agent 工具:把任务总结报告归档进报告中心,
// 让报告中心有持续、自动的来源(任务收尾/报告撰写 agent 调用)。

// reportTools 返回报告工具集(经 hostTools 注入,seed 绑定 reporter/pentest/worker)。
func (s *Server) reportTools() []actool.CoreTool {
	return []actool.CoreTool{
		s.toolArchiveTaskReport(),
	}
}

// toolArchiveTaskReport 渲染当前任务的总结报告并存入报告中心。
// 幂等:同一任务的报告只保留一份,重复调用覆盖旧存档。
func (s *Server) toolArchiveTaskReport() actool.CoreTool {
	return wrTool("archive_task_report",
		"把指定任务的渗透测试总结报告(目标/统计/确认漏洞/证据要点)渲染并存入报告中心。"+
			"任务收尾、里程碑达成或用户要求归档报告时调用。同一任务重复调用会覆盖旧存档。",
		objSchema(map[string]any{
			"task_id": strParam("目标任务 id"),
			"title":   strParam("覆盖报告标题(可选,缺省用任务描述)"),
			"summary": strParam("覆盖摘要行(可选,缺省自动生成统计)"),
		}, "task_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				TaskID  string `json:"task_id"`
				Title   string `json:"title"`
				Summary string `json:"summary"`
			}
			_ = json.Unmarshal(in, &a)
			if a.TaskID == "" {
				return actool.Errorf("task_id 不能为空"), nil
			}
			t := s.m.ResolveTask(a.TaskID)
			if t == nil {
				return actool.Errorf("任务不存在"), nil
			}
			title, summary, md := s.renderTaskReport(t)
			if a.Title != "" {
				title = a.Title
			}
			if a.Summary != "" {
				summary = a.Summary
			}
			id, created, err := s.m.PG().UpsertTaskReport(taskNumericID(t.ID), title, summary, md)
			if err != nil {
				return actool.Errorf("归档失败: " + err.Error()), nil
			}
			kind := "updated"
			if created {
				kind = "created"
			}
			s.logReportEventTask(id, taskNumericID(t.ID), kind, "agent", map[string]any{
				"tool": "archive_task_report", "words": len([]rune(md)),
			})
			return jsonResult(map[string]any{
				"report_id":       id,
				"created":         created,
				"report_center":   "报告中心已更新,可在 报告中心 页查看/导出",
				"template_export": "导出时可在报告详情选择模板(md/docx)",
			})
		})
}
