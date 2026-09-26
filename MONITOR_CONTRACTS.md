# MONITOR_CONTRACTS — 资产监控（24h 持续子域测绘）冻结契约

> 本文件是多 agent 并行开发的唯一规范。共享文件（schema.sql / web/src/lib/types.ts /
> web/src/lib/api.ts / sidebar-items.ts）已由主会话冻结并提交，**除所有权矩阵明确分配的
> 行区外任何人不得改动**。wire 形状以下文「冻结原文」为准，实现必须逐字对齐。

## 需求定稿（用户已拍板）

- 监控对象：全局开关打开后，**所有 `company_scope` 里有 `kind='domain'` 规则的企业自动纳入**；
  每企业可单独停开（targets 行 enabled=false=显式退出）、可覆盖间隔（interval_hours NULL=跟随全局）。
- 节奏：固定间隔轮询。全局默认 24h（settings 可调 1..720 小时），多企业**错峰串行**（全局单例，
  一轮只跑一家，两家之间隔 ≥20s）。
- 探测深度：被动收集（已启用的外部接口 provider + **crt.sh 恒定参与**）+ 对**新发现子域**做
  DNS 解析 + HTTP(S) 存活探测（纯 Go，禁 os/exec）。
- 入库：**全自动直写正式资产**（不走暂存区——`staged_assets.task_id` FK 到 tasks，监控轮没有
  任务行；审计轨迹由 `recon_monitor_runs` 承载：collectors 明细 + new_domains 清单）。
- 感知：资产 `created_at` 暴露为「首次发现」+ `newer_than_days` 筛选 + 监控页轮次摘要。
- 每轮**零 LLM**：`buildReconPlan(ctx, ids, 0, false)`（useAI=false，确定性默认 query）。
- 监控轮**不建任务行、不写探索图**。重启续跑：水位（last_run_at）落库，开机后 overdue 自然补跑。

## 关键实现事实（读代码已确认）

- `recon.Collector` 接口：`Collect(ctx, CollectorInput{Companies, Config, Secrets, RawSink}) (*CollectorResult, error)`，
  产出 `[]RawAsset{Type, Value, Port, Extra}`（recon/recon.go:44-83）。
- 自动计划：`s.buildReconPlan(ctx, companyIDs, profileID, useAI)` → `plan.Sources`（`ReconSourcePlan`），
  只含 16 家 provider；**crt.sh 是收集器不是 provider，不在计划里，监控轮必须显式追加**
  （`recon.PipelineStep{Key:"crtsh"}`，Config 留空，域名来自 Companies[i].Domains）。
- search 型 provider 的步骤 Config：`{"external_api_id": sp.ExternalAPI, "domain": sp.Domain, "query": sp.Query}`
  （SkipReason 非空的 Source 跳过）——照抄 server/recon_runner.go:150-163 的组装。
- 凭据运行时注入：`s.resolveExternalSecrets(stepKey, cfg)`（server/recon_api.go:923）；
  字典步骤监控轮不用（subdomain_brute 不参与）。
- 出网代理：监控轮走全局出口 `ctx = recon.WithProxy(ctx, s.m.ProxyAddr(), s.m.ProxyCACert())`
  （manager 上若有不同签名的方法，以实际为准并在 PR 里注明）。
- 直写资产复用 `s.adoptStagedAsset(as, 0, &db.StagedAsset{Type:..., Value:..., Port:..., Extra:...})`
  （server/recon_api.go:196）——taskID 传 0、CompanyID 用 Upsert 的 forced 归属；
  **adoptStagedAsset 不感知 CompanyID**，直写时不要走它处理需要强制归属的场景，见 Agent C 细则。
- 「新发现」判定：轮内先 `ExistingValues(typ, values)` 批量查存量（advisory lock 外的只读快照，
  极小概率与手动任务竞态 → 误标 new 并多探一次，无害），差集即 new。
- 证据落盘：`s.reconSink(data)`（server/recon_runner.go:64）。
- 定时器范式：server/vulndb_sync.go（30s 启动延迟 / wake chan cap1 / running 互斥 / settings 开关）。
- 列表接口铁律：`total` 必须在过滤**之后**算；Go 集合字段**绝不让 nil 漏出去**（初始化空 slice）。
- raw SQL 字符串里**禁止 `//` 注释**（42601 全链路静默失败的教训）；schema 已由主会话写入并验证唯一。

## 所有权矩阵（并行期）

| 文件 | 归属 | 说明 |
|---|---|---|
| db/schema.sql | ❄️ 已冻结 | 监控两表 + idx_av2_type_created 已追加（文末），勿再动 |
| web/src/lib/types.ts / api.ts | ❄️ 已冻结 | 监控类型与 API 函数已加，Agent D 只读 |
| web/src/navigation/sidebar-items.ts | ❄️ 已冻结 | 「资产监控」入口已加 |
| **db/recon_monitor.go（新）** | **Agent A** | targets/runs CRUD + 候选企业查询 |
| **db/assets.go** | **Agent A** | Asset.CreatedAt 暴露 + QueryAssets(filter) + ExistingValues |
| **db/recon_monitor_test.go（新）** | **Agent A** | 需 5499 演示库（ARTEX_PG_DSN） |
| **recon/liveness.go（新）** | **Agent B** | 纯 Go 探活模块 |
| **recon/liveness_test.go（新）** | **Agent B** | 不碰 collectors*.go / runner.go |
| **server/recon_monitor.go（新）** | **Agent C** | 调度器 + 单轮执行 |
| **server/recon_monitor_api.go（新）** | **Agent C** | 路由 handlers + registerReconMonitorRoutes |
| **server/assets.go（仅 listAssets 函数体）** | **Agent C** | 解析 `newer_than_days` 并在 >0 时走 QueryAssets（其余路径行为不变） |
| **server/server.go（三处接线）** | **Agent C** | ① Server struct 加 `reconMonitor *reconMonitor`（vulnSync 字段旁，~:53）② New() 里 vulnSync 启动两行后加 `s.reconMonitor = newReconMonitor(s)` + `go s.reconMonitor.run(s.ctx)`（~:170）③ 路由区 `POST /api/recon/plan` 行后加 `s.registerReconMonitorRoutes(mux)`（~:812） |
| **web/src/app/(main)/function/recon/monitor/page.tsx（新）** | **Agent D** | 监控页（可拆 components/recon-monitor-*.tsx） |
| **web/src/app/(main)/function/assets/page.tsx（指定行区）** | **Agent D** | 根域名/子域名/service 页签加「首次发现」列 + 「新发现」筛选 |
| **web/src/app/(main)/function/assets/detail/page.tsx（指定行区）** | **Agent D** | 资产表加「首次发现」列 |

**纪律**：提交只许显式 pathspec（`git add <自己的文件>`），绝不 `git add -A`；不 rebase/不回拨分支；
不动他人在途文件。

## 冻结原文：schema DDL

```sql
CREATE TABLE IF NOT EXISTS recon_monitor_targets (
    company_id     BIGINT PRIMARY KEY REFERENCES companies(id) ON DELETE CASCADE,
    enabled        BOOLEAN NOT NULL DEFAULT true,
    interval_hours INTEGER CHECK (interval_hours IS NULL OR interval_hours > 0),
    last_run_at    TIMESTAMPTZ,
    last_run_id    BIGINT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- trg_recon_monitor_targets_upd → set_updated_at()

CREATE TABLE IF NOT EXISTS recon_monitor_runs (
    id            BIGSERIAL PRIMARY KEY,
    company_id    BIGINT REFERENCES companies(id) ON DELETE SET NULL,
    company_name  TEXT NOT NULL DEFAULT '',
    trigger       TEXT NOT NULL DEFAULT 'schedule' CHECK (trigger IN ('schedule','manual')),
    status        TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running','done','failed')),
    collectors    JSONB NOT NULL DEFAULT '[]',
    total_found   INTEGER NOT NULL DEFAULT 0,
    new_count     INTEGER NOT NULL DEFAULT 0,
    new_domains   JSONB NOT NULL DEFAULT '[]',
    new_truncated BOOLEAN NOT NULL DEFAULT false,
    probe_ok      INTEGER NOT NULL DEFAULT 0,
    probe_failed  INTEGER NOT NULL DEFAULT 0,
    error         TEXT NOT NULL DEFAULT '',
    started_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at   TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_monitor_runs_company ON recon_monitor_runs(company_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_monitor_runs_status ON recon_monitor_runs(status, id DESC);
CREATE INDEX IF NOT EXISTS idx_av2_type_created ON assets(type, created_at DESC);
```

## 冻结原文：settings 键

- `recon_monitor_enabled`：`"true"`/`"false"`，**缺省视为关**（与 vulndb 相反：主动触探目标，须显式开）。
- `recon_monitor_interval_hours`：正整数，缺省 24，合法域 [1, 720]，越界回退缺省。

## 冻结原文：API wire（路由全部挂 `/api` 前缀）

### GET /api/recon/monitor → `ReconMonitorOverview`

```jsonc
{
  "config": { "enabled": false, "interval_hours": 24, "running": false, "last_round_at": null },
  "targets": [
    {
      "company_id": 1, "company_name": "示例", "domain_count": 3,
      "domains": ["a.com", "b.com"],
      "watched": true,            // 有效监控中 = config.enabled && domain_count>0 && row.enabled(缺省true)
      "enabled": true,            // 行值；无行=true
      "interval_hours": null,     // null=跟随全局
      "last_run_at": null,
      "next_run_at": null,        // watched 时 = last_run_at + 有效间隔；否则 null
      "last_run_id": null,
      "last_run": null            // { "status": "done", "new_count": 5, "finished_at": "..." } | null
    }
  ]
}
```

### PUT /api/recon/monitor/config　body `{ "enabled"?: boolean, "interval_hours"?: number }` → 返回更新后的 config 对象。interval 越界 400；开启时顺带 wake。

### PUT /api/recon/monitor/targets/{companyID}　body `{ "enabled"?: boolean, "interval_hours"?: number|null }` → 返回更新后的 target 对象（同 targets[] 元素形状）。企业不存在 400；**interval_hours 三态**：数值=设置覆盖、`null`=清除覆盖回全局、字段缺省=不动（后端用 json.RawMessage 解析区分；前端始终显式传 number|null）。

### POST /api/recon/monitor/trigger　body `{ "company_id"?: number }` → `{ "started": boolean }`。
- 带 company_id：把该企业水位清零（last_run_at/last_run_id 置 NULL）后 wake——**绕过全局开关**（便于试跑一家）。
- 不带：仅 wake（跑所有 due 的）。已有轮次在跑 → `{"started": false}`。

### GET /api/recon/monitor/runs　`?company_id=&status=&limit=&offset=` → `{ "total": 0, "runs": ReconMonitorRun[] }`
列表里 `new_domains` 一律空数组（详情接口才有），防大 JSON 拖列表。

### GET /api/recon/monitor/runs/{id} → `ReconMonitorRun`（完整，含 new_domains）。404 = 不存在。

### GET /api/assets 增参　`newer_than_days=<正整数>`：`created_at >= now() - N days`。与 company_id/task_id/type 组合生效（DSL 路径不生效）。

```jsonc
// ReconMonitorRun（列表与详情同形；详情才有 new_domains 非空）
{
  "id": 10, "company_id": 1, "company_name": "示例",
  "trigger": "schedule",           // schedule | manual
  "status": "done",                // running | done | failed
  "collectors": [ { "key": "crtsh", "status": "done", "found": 120, "error": "" } ],
  "total_found": 120, "new_count": 7,
  "new_domains": [ { "type": "domain", "value": "x.a.com" } ],
  "new_truncated": false,          // new_domains 超过 300 条截断置 true
  "probe_ok": 3, "probe_failed": 4,
  "error": "", "started_at": "...", "finished_at": "..."
}
```

## 冻结原文：TS 类型（已写入 web/src/lib/types.ts，勿改）

```ts
export interface MonitorCollectorStat { key: string; status: string; found: number; error?: string; }
export interface MonitorNewAsset { type: string; value: string; }
export interface ReconMonitorConfig { enabled: boolean; interval_hours: number; running: boolean; last_round_at?: string | null; }
export interface ReconMonitorTarget {
  company_id: number; company_name: string; domain_count: number; domains: string[];
  watched: boolean; enabled: boolean; interval_hours: number | null;
  last_run_at?: string | null; next_run_at?: string | null;
  last_run_id?: number | null;
  last_run?: { status: string; new_count: number; finished_at?: string | null } | null;
}
export interface ReconMonitorOverview { config: ReconMonitorConfig; targets: ReconMonitorTarget[]; }
export interface ReconMonitorRun {
  id: number; company_id?: number | null; company_name: string;
  trigger: string; status: string;
  collectors: MonitorCollectorStat[];
  total_found: number; new_count: number;
  new_domains?: MonitorNewAsset[]; new_truncated?: boolean;
  probe_ok: number; probe_failed: number; error: string;
  started_at: string; finished_at?: string | null;
}
```

api.ts 已冻结的函数：`reconMonitorOverview / saveReconMonitorConfig / saveReconMonitorTarget /
triggerReconMonitor / reconMonitorRuns / reconMonitorRun / assetsNewer`（签名见 api.ts 监控段注释）。

## 单轮执行语义（Agent C 实现，A/B 提供底座）

1. `StartMonitorRun` → running 行。
2. 组步骤：`buildReconPlan(ctx, [cid], 0, false)` 可用 Sources → Steps + **crtsh 追加在首**；
   步骤为空也继续（crtsh 恒在）。
3. 逐步骤（顺序执行）：`resolveExternalSecrets` → `recon.Get(key).Collect(ctx, CollectorInput{Companies:[{ID,Name,Domains}], Config, Secrets, RawSink: s.reconSink})`。
   单步失败记 collectors[].status=failed 继续；`found` 记产出条数。
4. 汇总 RawAsset（按 type+value 去重，封顶 20000 条/轮）→ `ExistingValues` 按 type 批量查存量 →
   差集 = new 集合。
5. 全量直写资产（不只 new——老资产也要刷 last_seen/归属）：按 adoptStagedAsset 同构映射
   （root_domain→UpsertRootDomain / domain→UpsertSubdomain / ip→UpsertIP(+Port) /
   service→extra.url 优先 UpsertHTTPService 否则 UpsertOtherService / app→UpsertApp），
   **全部带 `CompanyID: cid` 强制归属、TaskID=0**。
6. 对 new 集合里的 `type=domain` 值跑 `recon.ProbeDomains`（并发 ≤8，单目标 ~15s 总预算）；
   Alive 的结果落 `UpsertHTTPService{URL: result.URL, StatusCode, PageTitle, ContentLength,
   Technologies, IP: 首个公网解析 IP, CompanyID: cid}`（side effect 自动补子域/IP/根域归属）；
   计 probe_ok / probe_failed。
7. `FinishMonitorRun`：status（全部收集器 failed → failed，否则 done；ctx 取消 → failed +
   error 置 "已停止"）、collectors JSON、total_found、new_count、new_domains（type∈{domain,
   root_domain,ip,service}，cap 300 + new_truncated）、probe_ok/failed；并回写
   targets.last_run_at=now()、last_run_id。
8. new_count>0 时 `log.Printf("[recon-monitor] %s: 新增 %d …")`（推送/通知是二期）。

## 各 Agent 交付清单

- **Agent A（db 层）**：`db/recon_monitor.go`（MonitorTarget/MonitorRun 结构与 scan、
  UpsertMonitorTarget、GetMonitorTarget、ListMonitorTargets、DeleteMonitorTarget、
  ResetMonitorTargetRun、ListMonitorCandidateCompanies（含 string_agg 根域名+计数）、
  StartMonitorRun、FinishMonitorRun（事务内回写 targets 水位）、ListMonitorRuns(filter)
  （**total 在过滤后算**、列表不选 new_domains）、GetMonitorRun、LatestMonitorRunsByIDs）+
  `db/assets.go`（Asset.CreatedAt 暴露——所有 SELECT 列清单加 `created_at::text`（建议抽公共
  列常量），scanAssets 同步；QueryAssets(AssetListFilter)（company_id/task_id/type/
  newer_than_days/limit/offset，返回 ([]*Asset, total, err)）；ExistingValues(typ, values)
  （500/批 IN 查询；domain/root_domain→domain 列，ip→ip，service→url））+ 单测
  （5499 库，显式 ARTEX_PG_DSN，勿连 5432）。
- **Agent B（探活）**：`recon/liveness.go`——`ProbeResult{Domain, IPs, Alive, URL, StatusCode,
  Title, ContentLength, Technologies, Err}`、`ProbeOptions{Concurrency int, Timeout
  time.Duration, InsecureTLS bool, Client *http.Client, Resolve func(ctx, host)([]net.IP, error)}`、
  `ProbeDomains(ctx, domains, opts)`。DNS 系统 resolver 解析 A 记录；**全部解析结果为
  私网/环回/链路本地/保留地址 → 不发任何 HTTP**（防 DNS rebinding 打内网）；HTTP 先
  https 后 http 各一次（超时默认 6s/次），读 ≤256KB 解析 `<title>`，Server/X-Powered-By 进
  Technologies；InsecureTLS=true 时对 x509 类握手错误降级重试一次（注释写明：授权目标指纹
  采集，自签证书常见）。出网 Client 缺省用 `newHTTPClient(ctx, timeout)`（代理感知）。
  单测：classifyIP 分类、title 解析、内网守卫（注入 Resolve + httptest）、https→http 回退。
- **Agent C（调度器 + API）**：按「单轮执行语义」实现 `server/recon_monitor.go` +
  `server/recon_monitor_api.go` + server.go 三处接线；due 判定抽纯函数
  `monitorDue(lastRunAt *time.Time, intervalHours, globalIntervalHours int, now time.Time) bool`
  便于单测；ctx 用 `context.WithTimeout(s.ctx, 30*time.Minute)` 包单企业轮；轮间隔
  `time.Sleep(20s)`；主循环 tick 60s + wake。单测：monitorDue、config 校验。
- **Agent D（前端）**：监控页四块（全局配置卡 / 企业监控表 / 轮次历史 / 新发现 feed）+
  资产页与详情页「首次发现」列与新发现筛选。铁律：`table-fixed`+固定列宽和远小于容器宽、
  标题/值列吃余量；running 状态 5s 轮询（无 running 停止轮询）；nil 兜底
  (`r?.targets ?? []`)；页签/筛选可点不要只读徽标；时间格式化复用现有 fmtTime 工具。

## 验收口径

- `go build ./...`（在 `./cmd/artex` 或全仓库）零错误；`go vet` 无新告警；
  `go test ./recon/ ./db/`（db 需 5499）新增用例全绿；`cd web && npx tsc --noEmit` 零错误。
- 8799 冒烟（主会话负责）：开监控 → 试跑一家 → 轮次 done、新增清单可见 → 资产页「新发现」
  筛选命中 → 关开关后循环空转。

## 补充冻结：Agent 间接口精确签名（A 实现、C 调用，逐字对齐）

```go
// db/recon_monitor.go（Agent A）
type MonitorTarget struct {
    CompanyID     int64      `json:"company_id"`
    Enabled       bool       `json:"enabled"`
    IntervalHours *int       `json:"interval_hours"`
    LastRunAt     *time.Time `json:"last_run_at"`
    LastRunID     *int64     `json:"last_run_id"`
    CreatedAt     time.Time  `json:"created_at"`
    UpdatedAt     time.Time  `json:"updated_at"`
}
func (d *DB) UpsertMonitorTarget(companyID int64, enabled *bool, intervalHours *int, clearInterval bool) (*MonitorTarget, error)
func (d *DB) GetMonitorTarget(companyID int64) (*MonitorTarget, error) // nil=无行
func (d *DB) ListMonitorTargets() ([]MonitorTarget, error)
func (d *DB) ResetMonitorTargetRun(companyID int64) error // last_run_at/last_run_id 置 NULL
type MonitorCompany struct {
    ID          int64    `json:"id"`
    Name        string   `json:"name"`
    DomainCount int      `json:"domain_count"`
    Domains     []string `json:"domains"`
}
func (d *DB) ListMonitorCandidateCompanies() ([]MonitorCompany, error) // 有 kind=domain 范围的企业
func (d *DB) StartMonitorRun(companyID int64, companyName, trigger string) (int64, error)
func (d *DB) FinishMonitorRun(runID, companyID int64, status string, collectors json.RawMessage,
    totalFound, newCount int, newDomains json.RawMessage, newTruncated bool,
    probeOK, probeFailed int, errMsg string) error // 事务内回写 targets 水位（无行则插）
type MonitorRunFilter struct { CompanyID int64; Status string; Limit, Offset int }
func (d *DB) ListMonitorRuns(f MonitorRunFilter) ([]MonitorRun, int, error) // 列表不含 new_domains
func (d *DB) GetMonitorRun(id int64) (*MonitorRun, error)                  // 含 new_domains
func (d *DB) MonitorRunsByIDs(ids []int64) (map[int64]*MonitorRun, error)
func (d *DB) LatestMonitorRoundAt() (*time.Time, error) // MAX(finished_at)，无行返回 nil,nil
// MonitorRun JSON tag 对齐本文 wire；Collectors/NewDomains 用 json.RawMessage，空值一律 "[]"/"[]"。
```

```go
// recon/liveness.go（Agent B，C 按此调用）
type ProbeResult struct {
    Domain string; IPs []string; Alive bool; URL string
    StatusCode int; Title string; ContentLength int64
    Technologies []string; Err string
}
type ProbeOptions struct {
    Concurrency int           // 缺省 8
    Timeout     time.Duration // 单次 HTTP 尝试，缺省 6s
    InsecureTLS bool          // x509 错误时降级重试一次
    Client      *http.Client  // nil=内部 newHTTPClient(ctx, timeout)
    Resolve     func(ctx context.Context, host string) ([]net.IP, error) // nil=系统 resolver
}
func ProbeDomains(ctx context.Context, domains []string, opts ProbeOptions) []ProbeResult
```

## 纪律（全体 agent）

- **不运行任何 git 命令**（不 add/commit/stash/pull）——主会话统一集成提交。
- 源码一律走 Write/Edit 工具（Bash 直写会被拦）；**源码禁 os/exec**；raw SQL 字符串内禁 `//` 注释。
- 中文测试数据走 UTF-8 文件 + `--data-binary @file`（如需 curl 验证）。
