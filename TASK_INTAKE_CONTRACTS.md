# TASK_INTAKE_CONTRACTS.md — 任务创建智能化 + VPS 选择 + 总览分页（冻结版）

本轮六项需求（2026-09-26）：SRC 建任务可选 VPS、规范附件上传后自动 AI 填入、
普通/SRC 任务描述 AI 识别拆分（目标/约束/奖励）、任务详情总览显示奖励、
任务总览页四个列表补分页器。

**多 agent 纪律**：契约文件（types.ts / api.ts / schema.sql / 本文件）已由主会话改完并提交，
各 agent **只读**，不得改这些文件。**禁止 `git stash` / `git checkout` / 切分支 / 提交**，
只改自己名下的文件。交付前自检（tsc / go test 按下述范围），集成与构建由主会话统一做。

---

## 1. wire 契约（已冻结进 types.ts / api.ts）

### POST /api/tasks/parse-input（新端点，Agent A 实现）

请求：
```json
{ "description": "…大段原文…", "goal": "…已有目标(可空)…",
  "src_mode": false, "src_platform_id": 0, "src_target_id": 0 }
```
响应：`{ "result": TaskInputParse }`（types.ts 已定义：goal / description / constraints[] / rewards / note）。

服务端规则：
- description trim 后非空，≤24000 runes（与 specAIFillMaxRunes 同量级）；超限 400。
- LLM：ActiveProfile → `s.providerForProfile`（照抄 `server/src_platform_ai.go` 的 specAIFill 路径），
  ctx 300s、temp 0.2、Thinking disabled、MaxTokens 16384、transport/解析失败重试 1 次（同 specAIComplete）。
- 提示词要求（中文，参照 specAIComplete 的写法）：
  - goal = 用户想达成什么（可多行条目）；
  - constraints = 边界与规则：禁止事项 → `{kind:"deny"}`；测试范围/允许事项 → `{kind:"allow"}`；逐条拆分，禁止合并成一条；
  - description = 剔除目标/约束/奖励后剩余的背景与测试对象信息（没有剩余就给空串）；
  - rewards = 仅 `src_mode=true` 时识别奖励标准（金额/积分/贡献值区间表整理为 markdown 文本），否则恒空串；
  - 原文没依据的字段一律空串/空数组，禁止编造；只输出 JSON，无围栏。
- 输出归一：constraints ≤40 条、每条 text ≤2000 runes、kind 归一为 allow|deny，超限截断（不报错）。
- LLM 未配置回 503（文案与 specAIFill 一致）。
- 路由注册在 server.go 既有任务路由旁：`mux.HandleFunc("POST /api/tasks/parse-input", s.parseTaskInput)`。

### POST /api/tasks（createTask 扩展，Agent A 实现）

createTaskReq 新增：
```go
Constraints []TaskConstraintInput `json:"constraints,omitempty"` // {kind:"allow"|"deny", text}
RewardNote  string               `json:"reward_note,omitempty"`
```
- 校验：kind ∈ allow|deny、text 非空；≤40 条、单条 ≤2000 runes（超限 400，与 parse-input 的「截断」不同——这里是用户显式提交，报错比静默丢强）。
- 写入时机：CreateTaskWithOptions 成功后、launchTask **之前**（约束要赶在首轮 Planner 前就位，与 applySRCConstraints 同理）：
  `s.m.PG().Exploration(t.ExpID).AddConstraint(kind, text, "user")`（server/src_api.go:185 有现成范式）。
- reward_note 持久化纵切：
  - db/schema.sql **已加** `ALTER TABLE tasks ADD COLUMN IF NOT EXISTS reward_note TEXT NOT NULL DEFAULT ''`；
  - db/tasks.go：Task 结构体 + scanTask 列清单 + INSERT（CreateTaskWithOptions / TaskCreateOptions.RewardNote）；
  - server/manager.go：taskFromPG 拷一行；
  - server/dto.go：TaskDTO + taskDTO 各加 RewardNote（json tag `reward_note,omitempty`）。

## 2. 前端行为契约

### 共享组件（Agent B 新建）
- `components/vps-exec-select.tsx`：把 tasks/page.tsx CreateTaskSheet 里的「执行环境」三选
  （local/machine/auto + 在线空闲机器下拉 + 提示文案）原样抽成受控组件，props：
  `{mode, machineId, onChange}`（onChange 传归一后的 mode 与哨兵处理过的 machineId）。
  CreateTaskSheet 改用之（行为零变化）；SRC 对话框与批量页接入。
- `components/task-constraints-editor.tsx`：约束行编辑器，props
  `{value: TaskConstraintInput[], onChange}`；行=[kind Select(允许/禁止)] [text Input] [删除]，
  底部「添加一条」，上限 40 行，附一句说明「创建后写入操作约束，任务总览可查看/修改」。
- `components/task-intake-ai.tsx`：AI 识别拆分区块。props
  `{description, goal, srcMode, srcPlatformId, srcTargetId, onApply(result), disabled?}`。
  内含：触发按钮（Sparkles 图标 + 「AI 识别拆分」）、识别中 spinner、note 展示、错误 toast。
  - **自动触发**：description trim 长度 ≥60 且 goal 为空且当前内容未识别过（按内容判重）时，
    描述输入框失焦自动触发一次；手动按钮任何时候可点。
  - 识别中不得阻塞编辑（不 disable 输入框）；同一时间只允许一个在途请求（复用/丢弃旧结果）。
- `api.parseTaskInput` 已冻结在 api.ts（自带 320s 兜底超时，调用方无需传 signal）。

### 应用识别结果（CreateTaskSheet 与 SRC 对话框共用规则）
1. `result.description` 非空 → **替换**描述框内容（这就是「不再一大坨」的落点）；
2. `result.goal` 非空：目标框为空 → 直接填；目标框已有内容且不同 → **不覆盖**，
   在目标框上方显示「采用 AI 识别的目标」小按钮（点击才覆盖）；
3. `result.constraints` 非空 → 约束编辑器整体替换为识别结果（可再手改）；
4. `result.rewards` 非空（仅 SRC）→ 填入「奖励标准」textarea（可改）；
5. toast 汇总「已拆分：目标/约束/奖励…」；识别前后内容都允许 Ctrl+A 重填，不做撤销栈。

### CreateTaskSheet（普通任务，tasks/page.tsx）
- 描述 label 行加 AI 识别拆分区块（自动触发 + 手动按钮）。
- 目标字段下方新增「操作约束(可选)」约束编辑器；创建请求带 `constraints`。
- 执行环境改用共享 vps-exec-select（行为零变化）。

### CreateSRCTaskDialog（src-create-task-dialog.tsx）
- 出网方式下方新增执行环境（共享组件）；提交带 vpsMachineId/vpsAuto（后端本来就支持，见 createTaskReq）。
- 描述输入从 Input 换 Textarea（识别拆分需要多行粘贴），加 AI 识别拆分区块
  （srcMode=true + 当前 platform/target id，锁定与快速创建两种模式都要传）。
- 新增「奖励标准(可选)」textarea（rows 4，placeholder 说明会随任务保存并在总览展示），提交带 rewardNote。
- 新增约束编辑器；默认描述模板文本很短（<60 字）不会误触发自动识别。

### SRC 批量页（src/batch/page.tsx）
- 只加执行环境共享组件 + 提交带 vps 字段（批量场景不做 AI 拆分）。

### 任务详情总览（tasks/detail/_tabs/overview-tab.tsx）
- 「任务描述与目标」卡片之后新增「奖励标准」卡片：`task?.reward_note?.trim()` 非空才渲染，
  whitespace-pre-wrap 展示文本；空则整块不渲染（不占位）。
- 识别出的约束无需新 UI：创建时写入 task_constraints，既有「操作约束」面板自动可见。

### spec-tab.tsx（Agent C，规范附件自动填入）
- `upload()` 成功后（parse_status 非 failed 且有正文）自动对该文件触发一次 aiFill
  （file_ids=[新文件 id]），复用既有 aiFill 的预填与非空覆盖语义；aiBusy 状态与手动按钮共用。
- 已有 aiBusy 在途时跳过自动触发（toast 提示可手动点）；上传失败的分支不触发。
- 手动「AI 识别并填入」按钮保留，行为不变。

### dashboard/page.tsx（Agent D，任务总览分页器）
- 四个列表全部去掉写死 slice，改客户端分页：
  - 「任务」表：默认 10 条/页，options [5,10,20,50]，用 `components/table-pagination.tsx`；
  - 「活动流」：activity 拉取 limit 30→200，默认 8 条/页 [6,8,12,24]；
  - 「发现」：默认 8 条/页 [8,16,32]；
  - 「最近请求」：traffic(0,50)→(0,200)，默认 5 条/页 [5,10,20]。
- TablePagination 的边框/内边距在卡片里若突兀，允许给这四处包一层或抽本地轻量 pager，
  但四个列表必须同一套分页控件与交互；每页条数选择与「共 N 条」计数保留。
- 切换分页不打断 5s 轮询（分页只是视图切片，数据照常刷新；页码越界时回退到最后一页）。

## 3. 文件所有权矩阵（冲突面）

| 文件 | 归属 | 其他人 |
|---|---|---|
| web/src/lib/types.ts / api.ts、db/schema.sql、本文件 | 主会话（已冻结提交） | 全员只读 |
| server/task_parse.go(+_test) 新建、server/server.go、server/dto.go、server/manager.go、db/tasks.go | Agent A | 无人可碰 |
| components/vps-exec-select.tsx / task-constraints-editor.tsx / task-intake-ai.tsx 新建、tasks/page.tsx、src-create-task-dialog.tsx、src/batch/page.tsx、tasks/detail/_tabs/overview-tab.tsx | Agent B | 无人可碰 |
| src/platforms/_components/spec-tab.tsx | Agent C | 无人可碰 |
| dashboard/page.tsx | Agent D | 无人可碰 |

已知环境事实：db 包测试必须显式 `ARTEX_PG_DSN` 指向 5499 库；server 包 5 个 finding-traffic
测试在本机固有失败（与本轮无关）；`go build ./...` 会被 data/tasks/25 残留污染，只 build `./cmd/artex`；
web 侧多 agent 并行时 `npx tsc --noEmit` 可能报别人文件的错——只保证自己名下文件零错误即可。
