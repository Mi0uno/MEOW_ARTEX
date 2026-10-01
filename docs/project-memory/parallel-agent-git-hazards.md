---
name: parallel-agent-git-hazards
description: 多 agent 并行开发时另一个会话 git stash/checkout 会静默吞掉共享契约文件——识别与恢复方法
metadata:
  node_type: memory
  type: feedback
  originSessionId: sess_5aabd8f5-f46d-4489-be4c-4a64179dbac7
---

# 并行 agent 开发时的 git 竞态（2026-09-24 实际发生）

用 5 个 agent 并行开发（我先冻结契约文件再分发）时，**另一个会话执行了 `git stash push`**，
把整个工作树（含我冻结的 12 个契约文件）收走，工作区回退到重构前状态。多个 agent 同时报告
「tsc 报错说 @/lib/types 没有导出 SRCPlatform」，但它们各自以为自己写错了。

**更隐蔽的二次伤害**：那个 stash 是在更早的 commit 上创建的，`git checkout stash@{0} -- <files>`
恢复后**连带把 stash 创建之后进入 HEAD 的内容也回退了**——本次丢了 JEV 门限阈值
（`JevThresholds` 类型、`jevThresholds`/`saveJevThresholds` API、两条 `/api/decision/engines/jev/thresholds` 路由），
表现为 `system/ai-guard/page.tsx` 报「模块没有导出成员 JevThresholds」。

## 识别信号
- 多个 agent 同时报告同一类「模块没有导出该成员」错误，而契约文件明明写过。
- `git stash list` 出现非自己创建的条目；`git status` 里自己的改动凭空消失。
- `git diff HEAD -- <file> | grep '^-'` 出现的删除行**不是自己删的**。

## 恢复方法（本次用的）
```bash
# 1. 逐文件对比工作树与 stash 的标记词,确认哪些被回退
for f in <files>; do
  wt=$(grep -c "<marker>" "$f"); st=$(git show "stash@{0}:$f" | grep -c "<marker>")
  printf "%-40s worktree=%s stash=%s\n" "$f" "$wt" "$st"
done
# 2. 只 checkout 自己那批文件(不要 stash pop:会把别人的在途改动一起翻上来)
git checkout stash@{0} -- <my files>
# 3. 检查是否有非自己的删除被一起回退(关键!stash 基线可能早于 HEAD)
git diff HEAD -- <file> | grep '^-' | grep -v "^---" | grep -viE "<我的改动关键词>"
# 4. 逐条补回被误删的他人内容
```
恢复后必须 `git diff HEAD` 全量复核一遍**非自己负责的删除行**，否则会带着别人的功能回退上线。

## 预防
- 冻结契约后**尽早提交一次**（哪怕只是个中间 commit），stash 就不会连带收走。
- 给每个 agent 的 prompt 里写明：**不要 `git stash` / `git checkout` / 切分支**，只改自己独占的文件。
- 交付前用 `git worktree add /tmp/baseline HEAD` 跑一遍基线测试，才能证明失败用例是既存的而不是自己引入的
  （本次靠它确认 server 包 5 个 finding-traffic 失败在干净 HEAD 上同样失败）。
- **2026-09-25 实测又两种新形态**：① 并行会话会把你已写好的文件**收进它的提交**（无害但要 `git show` 核对没丢内容）；
  ② 并行会话也在跑 `next build` 时会撞 `.next` 目录锁报「Another next build process is already running」——
  等 node 进程只剩 Mimosa MCP 再重试即可；构建中途它还在改前端时，可先用纯 Go 二进制（不带 embedui）做 API 层 E2E，UI 构建延后。
- **2026-09-25 第三形态（最隐蔽）：提交的页面引用只存在于工作区未提交改动的契约类型——已提交基线本身构建不过**。
  实例：`spec-tab.tsx`（已提交）import `SPECAIFillSuggestion`，`ai-guard/page.tsx`（已提交）import `SRCGuardOverview`、
  用 `InterceptRule.scope`——三者当时只在主工作区**未提交**的 `web/src/lib/types.ts` 里（疑似 stash 事故遗漏回写的同一批）。
  后果：任何干净 checkout（worktree/CI/上游合并）上 `next build` 直接挂。**识别**：干净树上 tsc 报
  「has no exported member / does not exist on type」而主工作区能编译。**修法**：从主工作区 dirty 文件里把缺失的
  类型/字段**原样嫁接**进干净树（纯类型声明，additive 无行为），再全量 `npx tsc --noEmit` 一次列全（next build 每次只报
  第一个错，会 whack-a-mole）。**预防**：改契约文件（types.ts/api.ts/schema.sql）的会话要**第一时间提交**，别让已提交
  代码依赖未提交契约；合并/验证一律在 `git worktree` 干净树上做 tsc，别只信主工作区能编译。

相关：[[project-artex]] [[src-platform-restructure]]
