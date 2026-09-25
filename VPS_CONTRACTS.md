# VPS 模块开发契约（冻结版 2026-09-25）

本文件是 VPS 中心模块多 agent 并行开发的**唯一契约**。所有接口形状、文件所有权、协议以此为准，
实现有出入时改实现、不改契约；确有必要改契约的，先在最终报告里声明，不得静默偏离。

## 0. 模块目标（一句话）

用户把自己的 Linux VPS 录入平台 → 创建任务时指派一台机器 → agent 的命令类工具（Bash）经
SSH 在该机器上执行（不再跑在本机 Windows）→ agent 能感知机器上装了哪些渗透工具（工具集
检测结果注入上下文，缺了可自己装）→ 全程有 Web 终端、执行流水、仪表盘可查可控。

一期范围：手动录入（provider=manual）、Web 终端（SSH PTY→WebSocket）、工具集、agent 集成、
任务创建时指定 VPS、仪表盘。云 API 开通是二期，本期只在数据模型留 provider 字段。

## 1. 文件所有权（防竞态，越界 = 事故）

| 文件 | 归属 | 其他人 |
|---|---|---|
| `db/schema.sql`、`web/src/lib/api.ts`、`web/src/lib/types.ts`、`web/src/navigation/sidebar/sidebar-items.ts` | **已由主会话写完并冻结** | **任何人不得再改**（含格式化） |
| `vpscenter/**`（新包） | Agent A | — |
| `db/vps.go`（新文件） | Agent A | — |
| `server/vps_center.go`、`server/vps_api.go`、`server/vps_terminal.go`（新文件） | Agent A | — |
| `server/server.go` | Agent A 只允许在 `New()` 加一行 `s.initVPS()` 与 `Handler()` 加一行 `s.registerVPSRoutes(mux)`，除此之外不动 | — |
| `web/src/app/(main)/function/vps/**`、`web/src/components/vps-*.tsx`（新文件） | Agent B | — |
| `CreateTaskSheet`、任务详情页 `_tabs` 注册 | Agent B | — |
| `server/vps_agent.go`（新文件）、`server/task_llm.go`、任务创建/DTO 的 vps 字段 | Agent C（A 完成后启动） | — |

公共约束：
- **新代码禁止 `os/exec`**（Mimosa 钩子会拦；SSH 全部走 `golang.org/x/crypto/ssh`，go.mod 已有）。
- 返回集合的接口，nil 必须初始化为空 slice（Go nil → JSON null 会打崩前端）。
- 单测不得连真实 PG/SSH；纯逻辑用 fake。`go build ./...` 会被 data/tasks/ 残留污染，只 build 具体包。
- 源码一律用 Write/Edit 工具写，不要用 Bash heredoc（编码 + 钩子双坑）。
- 不要 git commit，不要动 8787/8799 运行中的实例。

## 2. 数据模型（已入 schema.sql，列名以它为准）

`vps_machines`（机器池，assigned_task_id 冗余列 + 部分唯一索引保证一机一任务）、
`vps_assignments`（指派历史）、`vps_executions`（执行流水，source ∈ agent/terminal/toolset/manual/detect）、
`vps_toolsets`（工具集，builtin 种子 Go 侧播种）、`vps_tool_detections`（机器×工具检测矩阵，PK(machine_id,tool_name)）。

凭据列 `password_enc/private_key_enc/key_passphrase_enc BYTEA` = secretbox（`server/secretbox.go`
的 `apiSecretBox`/解封函数）密文。**任何接口响应永不回显凭据明文**；`auth_type=password` 时必须有密码，
`key` 时必须有私钥。

## 3. vpscenter 包 API（Agent A 实现，Agent C 消费）

新包 `vpscenter/`，**不 import db 包**（照 proxycenter 惯例，持久化经 Store 接口回调）：

```go
// ssh.go —— 薄封装 golang.org/x/crypto/ssh
type SSHConfig struct{ Host string; Port int; Username string; AuthType string; Password string; PrivateKey []byte; KeyPassphrase string; Timeout time.Duration }
type SSHClient interface {
    Exec(ctx, cmd string, timeout time.Duration) (stdoutAndStderr []byte, exitCode int, err error) // bash -lc 语义
    OpenPTY(ctx, cols, rows uint16) (PTYSession, error)
    SysInfo(ctx) (*SysInfo, error)          // os-release / uname / nproc / free / df
    DetectTools(ctx, names []string) ([]ToolDetection, error) // command -v + --version
    Close() error
}
type PTYSession interface {
    io.Reader; io.Writer
    Resize(cols, rows uint16) error
    ExitCode() <-chan int   // 会话结束时投递一次
    Close() error
}
// Dial(cfg SSHConfig) (SSHClient, error)：password / key(+passphrase, 支持 ed25519/rsa/ecdsa) 两种认证。

// manager.go
type Store interface { /* db 适配器在 server 层实现；Get/List/Update 机器、流水落库、检测矩阵 upsert 等，签名 A 自定 */ }
type Manager struct{ /* ... */ }
func NewManager(st Store, log *slog.Logger) *Manager
// 生命周期:
func (m *Manager) Assign(taskID int64, machineID int64, by string) (*Machine, error) // 校验在线&空闲；写 assigned_task_id + assignments 历史；建任务工作区目录
func (m *Manager) AssignAuto(taskID int64, by string) (*Machine, error)              // 挑 status=online 且未占用的机器（maintenance/unknown 不参与）
func (m *Manager) Release(taskID int64, reason string) error                         // 清 assigned_task_id + 关历史行（幂等：无指派时 no-op nil）
func (m *Manager) MachineForTask(taskID int64) (*Machine, error)                     // 无指派返回 nil, nil
// 执行:
func (m *Manager) ExecCommand(ctx, machineID, taskID int64, source, command string, timeout time.Duration) (output []byte, exitCode int, err error)
//   落执行流水（完整输出写 <BaseDir>/vps/outputs/<execID>.txt，表里存预览前 2KB 与大小）。
//   命令统一包一层: mkdir -p <ws> && cd <ws> && bash -lc '<command>'，ws=<workspace_base>/task_<taskID>（taskID=0 时用 manual_<时间戳>）。
func (m *Manager) OpenPTY(ctx, machineID int64, cols, rows uint16) (PTYSession, error) // PTY 会话结束记一条 source=terminal 流水（会话全程命令无法逐条拆分，记首条+时长）
func (m *Manager) CheckMachine(ctx, machineID int64) (*Machine, error)   // 连接+SysInfo，刷新状态/系统信息，offline 时记 last_error
func (m *Manager) DetectMachine(ctx, machineID int64) ([]ToolDetection, error)
func (m *Manager) ApplyToolset(ctx, machineID, toolsetID int64) (execID int64, output []byte, exitCode int, err error) // source=toolset
// 后台循环:
func (m *Manager) RunCheckLoop(ctx) // 每 checkInterval(默认60s) 分批测活所有机器，busy 状态只做连通性探测不改成 offline(任务还挂着,掉线留给用户判断,仪表盘能看到 last_check)
// 工具集种子(照 toolcatalog 惯例):
func BuiltinToolsets() []BuiltinToolset // 3 个内置:基础侦察(nmap/httpx/nuclei/subfinder/whatweb)、Web漏扫(sqlmap/ffuf/gobuster/nuclei)、爆破破解(hydra/john/hashcat)。脚本用 apt-get + go install,幂等(|| true / install -y),开头有 if ! command -v go 的 golang 兜底安装。

// tools.go —— 已知工具探测清单(~18个): nmap masscan nuclei httpx subfinder sqlmap ffuf gobuster dirsearch hydra john hashcat whatweb wafw00f amass curl python3 git dig
```

## 4. REST 契约（Agent A 实现；路径/负载已冻结进 api.ts，不得偏离）

统一 `writeJSON`/`writeErr`，挂在 `requireAuth` 下（`registerVPSRoutes(mux)` 内注册）：

| 方法路径 | 语义 | 响应 |
|---|---|---|
| GET `/api/vps/dashboard?window_h=` | 仪表盘 | `{summary, series[], busy_machines[], recent[]}`（types.VpsDashboard） |
| GET `/api/vps/machines?status=&provider=&tag=&q=&limit=&offset=` | 分页列表，assigned_task_name 需 JOIN tasks 带名 | `{machines[], total}` |
| POST `/api/vps/machines` | 录入；**同步**做首测（超时 15s），测活失败也入库（status=offline, last_error 带回） | `VpsMachine` |
| PUT `/api/vps/machines/{id}` | 编辑；凭据字段缺省=不改 | `VpsMachine` |
| DELETE `/api/vps/machines/{id}` | 被占用时 409 拒绝 | `{deleted:true}` |
| POST `/api/vps/machines/{id}/check` | 手动测活 | `VpsMachine` |
| POST `/api/vps/machines/{id}/detect` | 工具检测 | `{detections[]}` |
| GET `/api/vps/machines/{id}` | 详情 | `{machine, detections[], current_task}` |
| POST `/api/vps/exec` | 单命令执行 `{machine_id, command, timeout_sec?}` 默认 120s 上限 600s | `{exit_code, duration_ms, output, truncated}` |
| GET `/api/vps/executions?machine_id=&task_id=&source=&limit=&offset=` | 流水 | `{executions[], total}` |
| GET `/api/vps/executions/{id}/output` | 完整输出（text/plain） | 文本 |
| GET `/api/vps/assignments?machine_id=&limit=` | 指派历史（0=全部），带 task_name | `{assignments[]}` |
| GET/POST `/api/vps/toolsets`、PUT/DELETE `/api/vps/toolsets/{id}` | 工具集 CRUD；builtin 拒改删（400） | |
| POST `/api/vps/toolsets/{id}/apply` | `{machine_ids[]}` 逐台同步执行 | `{results[]}` |
| GET `/api/vps/machines/{id}/terminal` | **WebSocket 升级**（见 §5） | — |

Agent C 的三个（不在 A 范围）：`POST /api/tasks/{id}/vps`（`{machine_id:0表示auto, auto:bool}`/`DELETE` 释放）、
`GET /api/tasks/{id}/vps-environment`、任务创建与 DTO 的 vps 字段。

## 5. WebSocket 终端协议（A 后端 / B 前端）

- 端点 `GET /api/vps/machines/{id}/terminal`，鉴权与 SSE 同款 `?token=` JWT（**注意**：`requireAuth`/`noStore`
  中间件若包了 ResponseWriter 会丢 `http.Hijacker`——实现时先验证，必要时该路由注册在 api 中间件之外、
  自己校验 token）。
- 升级后服务端 OpenPTY(120, 36)，双向转发：
  - C→S JSON：`{"type":"stdin","data":"..."}`、`{"type":"resize","cols":120,"rows":36}`、`{"type":"ping"}`
  - S→C JSON：`{"type":"stdout","data":"..."}`（base64 不用——JSON string 直接装 UTF-8，xterm 收到的是
    UTF-8 解码后的串）、`{"type":"exit","code":n}`、`{"type":"error","message":"..."}`
- 会话结束（远端断/机器掉线）发 exit 后关闭连接。空闲超时 30 分钟服务端主动关。
- 写死上限：单机并发终端 ≤2，超出发 error 后拒。

## 6. Agent 集成契约（Agent C）

- **任务创建**：`TaskCreateOptions` 加 `VPSMachineID int64` 与 `VPSAuto bool`；`POST /api/tasks` 的 body 加
  `vps_machine_id`/`vps_auto`；创建成功后（进入 running 前）调 `vpscenter.Manager.Assign`（失败不阻断建任务，
  任务照常本机跑 + 返回体带 warning）。任务 DTO 加 `vps_machine: {id,name,host,status} | null`（仅创建/详情返回）。
- **执行路由**：**worker/planner/mainagent 三个角色**的 Bash 工具在 `MachineForTask(taskID) != nil` 时统一改走
  `ExecCommand(source="agent")`，输出截尾 256KB；否则维持 SDK 默认本机执行。只换 worker 的话，planner 的
  开局探测与 main agent 对话里的命令会漏回本机执行（掉马甲）。三角色必须共用同一个按名替换入口
  （`agent.applyVPSBashSwap`）与同一个 per-run 解析器（`server.vpsContextResolver`）；解析器查询**出错**时
  返回 fail-closed 占位 Bash（拒绝执行），绝不静默回落本机。改造点在 norma SDK 工具组装处（`server/task_llm.go` 的
  `agentsForTask` 解析一次机器句柄下传，与该函数既有的代理出口解析同构；不要在 worker.go 里散落查询）。
  SDK 的 Read/Write/Grep 保持本机（它们操作本地工作区文件），prompt 里说清楚。
- **额外工具**（两个新领域工具，照 `server/recon_tools.go` 的 actool.CoreTool 闭包写法）：
  - `vps_machine_info()`：返回机器档案 JSON（os/arch/已装工具及版本/工作区路径/资源）。
  - `vps_install_toolset(name)`：按名跑工具集脚本（source=toolset），返回执行结果摘要。绑 worker。
- **prompt 注入**：有指派时 worker 系统提示词追加一段：「你的所有 Bash 命令将在 Linux 机器
  `<name>(<host>)` 上执行：OS <os_name> <arch>，已装工具: <清单>；每次调用是独立 shell，工作目录固定
  `<ws>`，持久状态请落盘文件；平台 Read/Write 工具操作的是本地工作区，远程文件请用 Bash cat/tee」。
- **释放**：任务终态（done/failed/timeout）自动 `Release(taskID, "task_end")`——挂进 `SetTaskTerminalHook`
  既有回调链（与报告归档共存，不得覆盖）。paused 不释放（会话可恢复）。**开机对账判据必须与终态一致**
  （任务行消失或 status IN done/failed/timeout 才算悬空）——created/排队中的任务同样持有指派,判据写成
  「非 running/paused」会把它们的机器放掉,重启后任务开跑掉回本机执行。
- 额外：终端/agent 之外任何人不得绕过 Manager 直接 SSH（凭据解密只发生在 vpscenter.Dial 前）。

## 7. 前端契约（Agent B）

- 路由：`/function/vps`（仪表盘）、`/function/vps/list`（机器池）、`/function/vps/detail?id=`（详情）、
  `/function/vps/toolsets`（工具集）。侧边栏入口已挂好。
- 详情页 Tabs：概览 / 终端 / 工具检测 / 执行流水 / 指派历史。终端用 `@xterm/xterm`+`@xterm/addon-fit`
  （需 `npm i`），组件 `web/src/components/vps-terminal.tsx`，**必须 `dynamic(import, {ssr:false})`**
  （静态导出约束），URL 用 `api.vpsTerminalUrl(id)` 拼 `wsUrl()`（api.ts 已提供）。
- 列表页铁律（历史踩坑，别再踩）：`table-fixed` + 标题/弹性列不设宽 + 固定列显式窄宽 + `truncate`；
  筛选是维度不是门禁（默认全部）；`total` 在筛选后算（分页依赖它）；批量删除 `Promise.allSettled` 循环
  单删 API；useSearchParams 包 Suspense；Radix Select 空值用哨兵串不用 ""。
- 状态徽标配色对齐平台既有 Badge 用法；凭据输入框 password 型，编辑时不回显只留「留空则不修改」提示。
- 创建任务抽屉（CreateTaskSheet）高级设置加「执行环境」：默认本机 / 指定机器（下拉只列 `status=online`
  且未占用的）/ 自动分配。任务详情加「执行环境」tab（`_tabs/` 新文件，用 `api.taskVpsEnvironment` +
  指派/解绑按钮 + 该任务流水表）。
- 组件风格对齐代理中心（`/function/proxy` 系列页面是范本）。

## 8. 验收基线（集成时主会话逐条验）

1. `GOSUMDB="sum.golang.org" ./build-local.sh out/artex-vps` 构建通过。
2. 8799 起服务，schema 自动迁移（新表出现）。
3. 录入一台真 VPS → 测活 online + 系统信息回填 + 工具检测矩阵出结果。
4. Web 终端能敲命令、ls/vim 交互正常、resize 不乱。
5. 建任务时选该机器 → worker 的 `uname -a` 输出是 Linux 而非 Windows；`vps_machine_info` 返回档案。
6. 任务 done 后机器自动释放；流水/仪表盘/审计全链路有数据。
