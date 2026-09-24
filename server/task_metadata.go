package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
)

const (
	maxTaskNameRunes           = 200
	maxTaskMetadataRequestSize = 16 << 10
)

// updateTaskMetadata changes list-only task metadata. It intentionally does not
// touch lifecycle state, scheduling, or the task's immutable description/goal.
//
// 监管门限(tasks.guard_mode/guard_rule_ids)也走这里,与 name/pinned 同一端点。
// 三个字段的"没传"与"传空"必须区分:guard_mode 的空串是合法值(= 跟随全局),
// guard_rule_ids 的空数组也是合法值(= 不限定规则,用全部已启用规则)。用零值判断
// 会让用户永远改不回默认姿态,所以一律用指针接收。
func (s *Server) updateTaskMetadata(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if _, ok := s.m.Task(taskID); !ok {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTaskMetadataRequestSize)
	var request struct {
		Name         *string  `json:"name"`
		Pinned       *bool    `json:"pinned"`
		GuardMode    *string  `json:"guard_mode"`
		GuardRuleIDs *[]int64 `json:"guard_rule_ids"`
	}
	if err := decode(r, &request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "请求正文过大")
		} else {
			writeErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	if request.Name == nil && request.Pinned == nil && request.GuardMode == nil && request.GuardRuleIDs == nil {
		writeErr(w, http.StatusBadRequest, "至少需要提供 name、pinned、guard_mode 或 guard_rule_ids")
		return
	}
	if request.Name != nil {
		name := strings.TrimSpace(*request.Name)
		if name == "" {
			writeErr(w, http.StatusBadRequest, "任务名称不能为空")
			return
		}
		if utf8.RuneCountInString(name) > maxTaskNameRunes {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("任务名称最多 %d 个字符", maxTaskNameRunes))
			return
		}
		request.Name = &name
	}
	if request.GuardMode != nil {
		mode := db.NormalizeTaskGuardMode(strings.ToLower(strings.TrimSpace(*request.GuardMode)))
		if !db.ValidTaskGuardMode(mode) {
			writeErr(w, http.StatusBadRequest, "guard_mode 必须是空、inherit、off 或 enforce")
			return
		}
		request.GuardMode = &mode
	}
	if request.GuardRuleIDs != nil {
		// 归一(去重/丢非正 id)后交给 db 层,非法内容直接判错而不是静默丢弃:
		// 规则集是安全控制的一部分,悄悄少一条规则等于悄悄放宽监管。
		ids := db.NormalizeTaskGuardRuleIDs(*request.GuardRuleIDs)
		if len(ids) != len(*request.GuardRuleIDs) {
			writeErr(w, http.StatusBadRequest, "guard_rule_ids 含重复或非法的规则 id")
			return
		}
		request.GuardRuleIDs = &ids
	}
	task, err := s.m.UpdateTaskMetadata(taskID, db.TaskPatch{
		Name: request.Name, Pinned: request.Pinned,
		GuardMode: request.GuardMode, GuardRuleIDs: request.GuardRuleIDs,
	})
	if err != nil {
		if errors.Is(err, db.ErrTaskGuardModeInvalid) {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if task == nil {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	// 门限变更留痕:拦截审计行只记录实际发生的拦截,记录不到"门限被改成 off
	// 导致什么都没发生"——而后者恰恰是最需要审计的。
	if request.GuardMode != nil || request.GuardRuleIDs != nil {
		mode, ruleIDs := task.taskGuardSnapshot()
		logTaskGuardChange(taskID, mode, ruleIDs)
	}
	writeJSON(w, http.StatusOK, taskDTO(task, s.resolvedTaskStatus(task)))
}
