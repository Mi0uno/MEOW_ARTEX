# APPROVAL_CONSOLE_CONTRACTS（审批台契约 v1，冻结基线）

审批台 = 监管&规则中心里的专用审批工作台（升级原「审批记录」页，路由不变
`/system/intercept/approvals`）。本文件是 Go 后端 agent（G）与 Web 前端 agent（W）
的唯一协作规范。**契约以本文件为准；实现若与本文冲突，改实现不改本文。**

## 0. 所有权矩阵（冻结后 agents 只读共享文件）

| 文件 | 归属 | 说明 |
| --- | --- | --- |
| `APPROVAL_CONSOLE_CONTRACTS.md` | 主会话冻结 | 本文件 |
| `web/src/lib/types.ts`（本契约相关段） | 主会话已冻结并提交 | W 只读 |
| `web/src/lib/api.ts`（本契约相关段） | 主会话已冻结并提交 | W 只读 |
| `db/intercept.go` | **G** | 扩展 InterceptApprovalRow / Filter / 行查询列 |
| `db/intercept_console.go`（新建） | **G** | 审批台分页+计数+工具清单查询 |
| `server/intercept_console.go`（新建） | **G** | handler + 参数解析 |
| `server/server.go` 仅 1115-1132 行 intercept 路由块内**追加一行** | **G** | `GET /api/intercept/console` |
| `server/intercept_console_test.go`（新建） | **G** | 纯函数单测 |
| `web/src/components/approval-console.tsx`（新建） | **W** | 审批台整页组件 |
| `web/src/lib/approval-script.ts`（新建） | **W** | 脚本字段提取/摘要 helper（ApprovalDetail 亦复用） |
| `web/src/app/(main)/system/intercept/approvals/page.tsx` | **W** | 换渲染 ApprovalConsole |
| `web/src/components/approval-records.tsx` | **W**（限定范围） | 仅允许：①引入 approval-script 的脚本查看区块进 ApprovalDetail；②originLabel 显示任务名。不得改 ApprovalRecords 的数据流/导出面 |
| `web/src/navigation/sidebar/sidebar-items.ts` 仅 approvals 行 title | **W** | 「审批记录」→「审批台」 |

其余文件一律不碰。并行会话 WIP（COMPLIANCE 相关：agent/constraints.go、db/constraints.go、
db/db.go、db/schema.sql、intercept/intercept.go、server/constraints_api.go、server/src_api.go、
server/src_constraints*.go、db/compliance.go、server/compliance.go 等）**绝对不动**。

## 1. 新端点 `GET /api/intercept/console`（G）

Query 参数（全部可选）：

| 参数 | 取值 | 语义 |
| --- | --- | --- |
| `status` | `pending/allowed/denied/timeout/flagged` | 分类页签；空=全部 |
| `decision_source` | `rule/model/unknown` | 判定来源筛选 |
| `q` | 任意文本 | 搜索：`tool_name` / `reason` / 任务名(tasks.name) / 会话标题(c.title) / `tool_input::text`（脚本/命令原文）。LIKE 特殊字符须转义 |
| `task_id` | tasks.id（text 形态） | 任务来源精确过滤（透传比较 `ip.task_id = $n`，不做 join 过滤） |
| `tool` | 工具名 | 工具精确过滤 |
| `page`/`size` | 1-based；size≤100 | 分页 |

响应 200：

```json
{
  "items": [InterceptApprovalRow...],
  "total": 123,
  "page": 1,
  "page_size": 20,
  "counts": {"pending": 2, "allowed": 10, "denied": 4, "timeout": 1, "flagged": 3},
  "tools": [{"tool": "Bash", "count": 34}]
}
```

- `counts`：**受** q/task_id/tool/decision_source 影响，**不受 status 影响**（页签自身维度，
  与 CVE 页 sev_counts 同约定）。一条 `GROUP BY status` 查询出全部五档，缺失档补 0。
- `tools`：工具清单下拉的数据源；**受** status/q/task_id/decision_source 影响，**不受 tool
  影响**；`GROUP BY tool_name ORDER BY count DESC LIMIT 50`。
- items 永不为 null（空切片兜底）；total=过滤后总数（先 total 后分页，报告中心老教训）。
- 未登录 401；参数非法 400（沿用 interceptFilterParams 的中文报错风格）。
- 现有端点（/history、/task/{id}、/pending、/decide）行为一概不变。

## 2. `InterceptApprovalRow` 扩展（G 落 db，wire 已冻结）

`approvalRowColumns` 增加四列（LEFT JOIN tasks，判据 `t.id::text = ip.task_id`，与
srcGuardRecentBlocksSQL 同款，避免非数字 task_id 铸型报错）：

| 字段 | 类型 | 语义 |
| --- | --- | --- |
| `task_name` | string | 关联任务名；""=无关联任务/任务已删 |
| `task_stage` | string | `""/recon/exploit/post/report` |
| `task_kind` | string | tasks.kind（agent/recon/...） |
| `task_src` | bool | tasks.src_mode |

旧端点复用同一 SELECT，自动带出；G 同步扩展 `scanInterceptApprovalRow` 与 Go 结构体，
并保证 db 包编译通过（单测跑 `ARTEX_PG_DSN` 指向 5499 或跳过依赖库的纯函数测试）。

Go `InterceptApprovalFilter` 扩展：`Q`/`TaskID`/`Tool` 三个 string 字段（空=不过滤），
`listInterceptsPage` 同步支持（task_id 参数与既有 ListTaskInterceptsPage 的固定 taskID
参数互斥不冲突：TaskID 为空时维持原行为）。

## 3. 前端 wire（已冻结在 types.ts/api.ts，W 只读）

```ts
// types.ts 新增
export interface InterceptToolCount { tool: string; count: number }
export interface InterceptConsoleCounts {
  pending: number; allowed: number; denied: number; timeout: number; flagged: number;
}
export interface InterceptConsolePage {
  items: InterceptApprovalRow[]; total: number; page: number; page_size: number;
  counts: InterceptConsoleCounts; tools: InterceptToolCount[];
}
// InterceptApprovalRow 扩展（审批台/分页端点恒返回；/intercept/pending 裸端点不带 → 可选）
task_name?: string; task_stage?: string; task_kind?: string; task_src?: boolean;
// InterceptApprovalFilter 扩展（全部可选）
q?: string; task_id?: string; tool?: string;
// api.ts 新增
api.interceptConsole(page, size, filter) → InterceptConsolePage
```

`interceptPageQuery` 已扩展序列化 q/task_id/tool。

## 4. 审批台页面需求（W）

1. **分类器（页签）**：全部 / 待审批 / 已允许 / 已拒绝 / 已超时 / 已判疑，**可点**，
   计数来自 `counts`（全部=N 各档之和，前端合计），选中态 `aria-pressed`（可点页签铁律）。
2. **筛选器**：判定来源 Select + 工具 Select（选项来自 `tools`，含计数）+ 清除筛选。
3. **搜索**：Input 400ms 防抖，回车立即搜；placeholder 说明搜索范围（工具/命令/脚本/任务名/理由）。
4. **分页器**：TablePagination（10/20/50）。
5. **待处理区**：置顶区块（5s 轮询 `api.interceptPending`），行内可 允许/拒绝（沿用旧组件交互语义）。
6. **任务来源列（不要只写编号）**：显示任务名（`task_name`）+ stage 徽标 + SRC 徽标；
   点击跳 `/function/tasks/detail?id={task_id}`；无任务时显示会话标题；两者皆无显示 —。
   决定时间/申请时间列保留。
7. **内容/意图列**：一句话摘要（`intentSummary`，见 §5）；含脚本字段时追加「脚本」按钮。
8. **脚本查看器**：Dialog 展示提取到的脚本段（label + 行号 + 复制按钮 + 自动换行 pre）；
   详情展开区（ApprovalDetail）同样插入脚本区块（不止 `command` 一个键）。
9. 5s 自动轮询刷新（沿用旧行为），手动刷新按钮；错误 Alert + 重试。
10. 列宽铁律：`table-fixed` + 固定列宽和明显小于容器宽；长文本一律块级元素 truncate。

## 5. 脚本提取规则（W 落 `web/src/lib/approval-script.ts`）

`extractScriptSections(toolName, toolInput): {label: string; text: string}[]`：
- 候选键按序扫描：`command`、`script`、`code`、`content`、`file_text`、`new_string`、
  `payload`、`body`、`template`、`query`、`input`；值须为非空 string。
- `tool_input` 为对象时逐键匹配；同名多段全部返回（label=键名）；无命中→空数组。
- `intentSummary(toolName, toolInput): string`：`command`→首行；`url`/`path`/`host`/
  `target`→拼接；否则第一个字符串值截 80 字；再退 JSON.stringify 截 80 字。

## 6. 验收（集成期主会话执行）

- go build ./... + go vet；tsc --noEmit 零错误。
- `GET /api/intercept/console` 冒烟：无参默认页、status 页签、q 命中脚本原文、task_id、
  tool、counts 与筛选联动、400 非法参数。
- 浏览器实测：页签切换/搜索防抖/分页/脚本查看器/任务来源链接/待审批裁决全链路。
