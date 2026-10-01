---
name: task-gone-guard
description: 任务库行消失时的运行时护栏（ErrTaskGone / cancelTaskRuntime）及其两个自锁陷阱
globs:
  - "D:/AI/MEOW_ARTEX/**"
---

# 任务库行消失的运行时护栏（2026-09-24）

任务库行被删除（API 删除、归档清理、或外部直接改库）后，内存里的 worker/planner 仍持有句柄，会一直对不存在的任务重试/规划。

**修复**：`server/task_llm.go` 新增 `ErrTaskGone`（`taskGoneError` 实现 `Is`），`taskLLMRuntime.current()` 在 `GetTask` 返回 nil 时返回它；`server/engine.go` 的 `retryableWorkerModelError` 对 `errors.Is(err, ErrTaskGone)` 直接返回 false（不再白跑两轮重试）；planner 的 `runRound` 识别后调 `cancelTaskRuntime` 并退出循环。

**两个必须记住的陷阱**（写这段代码时都踩到并修正了）：
1. **不能在 planner 协程里调 `StopTask`** —— 它 `rt.wg.Wait()` 等的是包含自己的 WaitGroup，会**自锁**。故新增 `cancelTaskRuntime`：只 `cancelExec` + `rt.cancel()`，不等 join。
2. **`cancelTaskRuntime` 不能安装 delete 屏障**（不能 `deleting.Store`）—— `Engine.Run` 开头 `IsDeleting` 会早退，而**归档恢复走的正是 Run**；归档任务同样把 `deleted_at` 置为 now()，装屏障会导致归档恢复后再也起不来。

注意：生产删除路径（`server/server_mgmt.go` 的 `pgDeleteTask`）本身护栏是完整的（`BeginDelete` → `cancelExec` → `waitTaskQuiescent` → `StopTask`），护栏只针对绕过 API 的库行消失场景。

相关：[[test-db-isolation]] [[project-artex]]
