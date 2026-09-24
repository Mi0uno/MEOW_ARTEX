package server

// SRC 平台选择:成果 CRUD + 概览,以及「平台/目标规范 → 任务硬约束」的注入。
//
// 平台/目标本身的 CRUD 在 src_platform_api.go(平台)与 src_target_api.go(目标),
// 本文件只保留成果与注入两部分。

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// ---- SRC 成果 ----

// listSRCRewards GET /api/src/rewards?company_id=&platform_id=&target_id=&task_id=&status=&severity=&q=&sort=&limit=&offset=
func (s *Server) listSRCRewards(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	companyID, _ := strconv.ParseInt(q.Get("company_id"), 10, 64)
	platformID, _ := strconv.ParseInt(q.Get("platform_id"), 10, 64)
	targetID, _ := strconv.ParseInt(q.Get("target_id"), 10, 64)
	taskID, _ := strconv.ParseInt(q.Get("task_id"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	items, total, counts, err := s.m.PG().ListSRCRewards(db.SRCRewardFilter{
		CompanyID: companyID, PlatformID: platformID, TargetID: targetID, TaskID: taskID,
		Status: strings.TrimSpace(q.Get("status")), Severity: strings.TrimSpace(q.Get("severity")),
		Q: strings.TrimSpace(q.Get("q")), Sort: strings.TrimSpace(q.Get("sort")),
		Limit: limit, Offset: offset,
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if items == nil {
		items = []*db.SRCReward{}
	}
	writeJSON(w, 200, map[string]any{"rewards": items, "total": total, "counts": counts})
}

// srcRewardPayload 是新建/更新成果的请求体。
type srcRewardPayload struct {
	Title       string `json:"title"`
	Severity    string `json:"severity"`
	VulnClass   string `json:"vuln_class"`
	Status      string `json:"status"`
	PlatformRef string `json:"platform_ref"`
	Feedback    string `json:"feedback"`
	CompanyID   *int64 `json:"company_id"`
	PlatformID  *int64 `json:"platform_id"`
	TargetID    *int64 `json:"target_id"`
	TaskID      *int64 `json:"task_id"`
	FindingID   *int64 `json:"finding_id"`
	EntryID     *int64 `json:"entry_id"`
}

// createSRCReward POST /api/src/rewards
func (s *Server) createSRCReward(w http.ResponseWriter, r *http.Request) {
	var p srcRewardPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	rw, err := s.m.PG().CreateSRCReward(db.SRCRewardInput{
		CompanyID: p.CompanyID, PlatformID: p.PlatformID, TargetID: p.TargetID,
		TaskID: p.TaskID, FindingID: p.FindingID, EntryID: p.EntryID,
		Title: p.Title, Severity: p.Severity, VulnClass: p.VulnClass, Status: p.Status,
		PlatformRef: p.PlatformRef, Feedback: p.Feedback,
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"reward": rw})
}

// updateSRCReward PUT /api/src/rewards/{id} — 主要更新提交状态与平台反馈。
func (s *Server) updateSRCReward(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad reward id")
		return
	}
	var p srcRewardPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if err := s.m.PG().UpdateSRCReward(id, db.SRCRewardInput{
		Title: p.Title, Severity: p.Severity, VulnClass: p.VulnClass,
		Status: p.Status, PlatformRef: p.PlatformRef, Feedback: p.Feedback,
	}); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	rw, err := s.m.PG().GetSRCReward(id)
	if err != nil || rw == nil {
		writeErr(w, 404, "reward not found")
		return
	}
	writeJSON(w, 200, map[string]any{"reward": rw})
}

// deleteSRCReward DELETE /api/src/rewards/{id}
func (s *Server) deleteSRCReward(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad reward id")
		return
	}
	deleted, err := s.m.PG().DeleteSRCReward(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

// srcOverview GET /api/src/overview — 成果概览(状态分布 + 企业维度 + 平台维度)。
func (s *Server) srcOverview(w http.ResponseWriter, r *http.Request) {
	st, err := s.m.PG().SRCRewardOverview()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, st)
}

// applySRCConstraints 把「平台规范 + 该平台下目标的授权范围」转成任务的
// allow/deny 约束写入 task_constraints。
//
// 这是「防止 AI 越界」的落地点:约束经既有机制注入 Planner/Worker 系统提示
// (agent/constraints.go 的 constraintBlock),对模型是最高优先级边界。
//
// 两段规范分开注入的理由:平台规范(奖励/评分/守则)与目标授权范围(能打哪些域名)
// 对模型是同等重要的边界信息,合成一条会让「哪条来自平台、哪条来自目标」不可分辨,
// 事后排查越界时无法定位约束来源。
//
// 平台与目标都可为空(只给平台 = 只注入平台规范),全空则什么都不做。
func (s *Server) applySRCConstraints(t *Task, platformID, targetID int64) {
	if t == nil {
		return
	}
	if platformID <= 0 && targetID <= 0 {
		log.Printf("[src] 任务 %s 标记了 SRC 模式但未指定平台/目标,无法注入约束", t.ID)
		return
	}
	store := s.m.PG().Exploration(t.ExpID)
	written := 0
	addConstraint := func(kind, text string) {
		if strings.TrimSpace(text) == "" {
			return
		}
		if _, err := store.AddConstraint(kind, text, "system"); err == nil {
			written++
		} else {
			log.Printf("[src] 任务 %s 写入 %s 约束失败: %v", t.ID, kind, err)
		}
	}

	// 平台规范(奖励标准/评分标准/守则/规范 + 附件解析正文):作为 allow 注入,
	// 它描述的是「这个平台下做什么是被鼓励/允许的」。
	if platformID > 0 {
		text, err := s.m.PG().SRCPlatformSpecText(platformID)
		if err != nil {
			log.Printf("[src] 读取平台 #%d 规范失败: %v", platformID, err)
		} else {
			addConstraint("allow", text)
		}
	}

	// 目标授权范围:拆成 allow(授权范围/允许类型)与 deny(禁止事项/速率限制)注入。
	// 逐条拆开而非整段塞入,是为了让规划者能逐条对照——整段文本容易被模型概括后丢掉细节。
	if platformID > 0 && targetID > 0 {
		link, err := s.m.PG().GetSRCTargetLink(platformID, targetID)
		if err != nil {
			log.Printf("[src] 读取平台 #%d/目标 #%d 关联失败: %v", platformID, targetID, err)
		} else if link != nil && link.Enabled {
			addConstraint("allow", prefixed("SRC 授权范围:", link.ScopeNote))
			addConstraint("allow", prefixed("允许的测试类型:", link.AllowedTypes))
			addConstraint("deny", prefixed("速率/强度限制(不得超出):", link.RateLimit))
			// 禁止事项逐行 → 独立 deny 约束(每条都单独注入,便于规划者逐条遵守)。
			for _, line := range splitConstraintLines(link.Forbidden) {
				addConstraint("deny", line)
			}
			addConstraint("deny", prefixed("SRC 平台规范补充:", link.Rules))
		} else if link == nil {
			log.Printf("[src] 目标 #%d 未挂在平台 #%d 下,跳过授权范围注入", targetID, platformID)
		}
	}

	log.Printf("[src] 任务 %s 已注入 SRC 约束(平台=%d 目标=%d),共 %d 条", t.ID, platformID, targetID, written)
}

// prefixed 拼接约束前缀;正文为空时返回空串(调用方据此跳过)。
func prefixed(prefix, body string) string {
	if strings.TrimSpace(body) == "" {
		return ""
	}
	return prefix + strings.TrimSpace(body)
}

// splitConstraintLines 把多行文本拆成非空行,并去掉行首的列表符号。
func splitConstraintLines(text string) []string {
	out := []string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
