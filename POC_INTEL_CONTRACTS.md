# POC_INTEL_CONTRACTS — CVE 情报联动 + PoC 聚合 + nuclei 回填 + LLM 审核（冻结版）

> 主会话已冻结下方全部签名/路由/结构。四 agent 并行实现，动他人文件=交付作废。
> 冻结基线 = 本文件的 git 提交，契约冲突以此为准，禁止重新讨论。

## §0 背景与决策（已拍板，勿再议）
1. **任务↔漏洞库读向联动三件套**：启动时按资产指纹自动匹配 CVE 注入简报 + `vulndb_search` agent 工具 + 任务详情简报卡。全局开关 `vulndb_task_brief`（缺省开）+ 任务级 `tasks.vuln_brief` 三态（NULL=跟随全局）。
2. **trickest/cve 作为主 PoC 聚合源**（替代自研全网爬）：raw 域名按 `<年>/<CVE-ID>.md` 拉取，`#### Github` 段=PoC 仓库，`#### Reference` 段=引用链接。MIT，自动持续更新。DDG 批量猎取不做。
3. **nuclei 模板正文全量入库**：raw 域名下载 YAML 进 `vulndb_pocs.content`，不占 GitHub API 配额。
4. **LLM 审核是软质检层**：自动源 PoC 入库即 `pending`，LLM 异步打 `approved/rejected`；rejected 不删只标存疑；人工可改判。`verified` 字段语义不变（仍只由人工/任务实测置位）。
5. 8799 演示库无真 LLM（测试桩 403）——LLM 相关链路必须「诚实降级」（保持 pending / 记录原因），**严禁假装成功**。

## §1 文件所有权矩阵
| 文件 | 归属 | 说明 |
|---|---|---|
| db/schema.sql, db/vulndb.go, db/tasks.go | **主会话已冻结** | 只读。新 db 需求发会话讨论 |
| agent/（全部改动） | **主会话已冻结** | vulndb_tool.go/种子/注入点已就位，零回归降级 |
| server/vulndb_api.go（config 段）, server.go, dto.go, manager.go | **主会话已冻结** | settings 键/路由挂载/createTask 已就位 |
| web/src/lib/types.ts, api.ts（冻结部分） | **主会话已冻结** | 新类型+api 函数已就位，agents 只读 |
| server/task_llm.go | **Agent A** | SetVulnBrief/SetVulnSearchFn 接线三处 |
| server/assembly.go | **Agent A** | 如需 ToolResolve 侧接线（一般不需要） |
| server/vulndb_briefing.go, vulndb_task_api.go | **Agent A** | 骨架已建，填实现 |
| server/vulndb_sync.go（syncGitHubPoCs 扩量） | **Agent B** | runOnce 第 10 源+审核触发行【已冻结，勿删】 |
| server/vulndb_trickest.go, vulndb_trickest_api.go, vulndb_poc_fetch.go | **Agent B** | 骨架已建，填实现 |
| server/vulndb_pocsources.go（仅 syncNucleiTemplates 段） | **Agent C** | 挂新增时同步抓正文 |
| server/vulndb_nuclei_body.go, vulndb_nuclei_api.go | **Agent C** | 骨架已建，填实现 |
| server/vulndb_poc_review.go, vulndb_poc_review_api.go | **Agent D** | 骨架已建，填实现 |
| web/src/components/vuln-briefing-dialog.tsx | **Agent A** 新建 | 任务详情简报卡对话框 |
| web/src/components/vulndb-backfill-card.tsx | **Agent C** 新建 | 回填卡组件（nuclei+trickest 共用，props 传 kind） |
| web/src/components/vulndb-poc-review.tsx | **Agent D** 新建 | 审核徽标/筛选组件 |
| 任务详情页/创建抽屉/cve 页/任务列表页 | **集成期主会话挂载** | agents 只写组件与「可独立验证」的页面外挂点 |

红线（全 agent）：源码一律 Write/Edit（Bash 直写被拦）；raw SQL 字符串内禁 `//` 注释（只能 `--`，血泪教训）；出网抓取优先复用既有 `fetchJSON`/`pocorexpFetchText`；新 http.Client 必须走 `s.m.ProxyAddr()`；SSRF 校验不绕过（raw.githubusercontent.com 公网域名可直接过 ValidatePublicURL）；禁 git stash/checkout/切分支/全量 add，提交显式 pathspec；不动 8787 实例与 5432 正库。

## §2 主会话已冻结的设施（逐字签名）
```go
// db 层（d *DB）：
ListVulnPocsFilter(entryID int64, withContent bool, reviewStatus string) ([]*VulnPoc, error)
VulnPocReviewStats() (pending, approved, rejected int, err error)
SetVulnPocReview(id int64, status, note string) error          // 状态白名单校验,reviewed_at=now()
SetVulnPocContent(entryID int64, sourceURL, content, language string) (bool, error)
    // 幂等：行存在 UPDATE content；不存在补建最小行（source 固定 "nuclei"——仅 C 用这个入口）
MatchVulnEntriesForFingerprints(terms []string, limit int) ([]*VulnEntry, error)
    // 仅 active+poc_count>0，kev→severity→poc_count→updated 排序；<3 字符的词自动忽略
CreateVulnPoc(VulnPocInput)                                    // ReviewStatus 字段：空=pending，"approved"=手工
ListVulnEntries(VulnEntryFilter) ([]*VulnEntry, int, map[string]int, error)
FindVulnEntryByCVE(cveID string) (*VulnEntry, error)           // 无行=nil,nil
UpsertCVE(...) (id int64, inserted bool, err error)
MergeVulnRefs(entryID int64, refs []string) error
StartVulnSyncRun(source string) (int64, error) / FinishVulnSyncRun(id int64, status string, fetched, inserted, updated, pocs int, errMsg string) error
GetSetting(key) (string, bool, error) / SetSetting(key, value string) error

// server 既有（Agent B/D 复用）：
fetchJSON(ctx, rawURL string, headers map[string]string, out any) error        // 内部过 SSRF 校验
pocorexpFetchText(ctx, rawURL string) (string, error)                          // vulndb_pocsources.go,纯文本
analyzeWithRetry(ctx, prov llm.Provider, system, user string, retries int, perAttempt time.Duration) (string, error)
analyzeCompletion(ctx, prov llm.Provider, system, user string) (string, error) // temp 0.2/max 8192/no thinking
providerForProfile(id int64) (llm.Provider, agent.Config, bool)
FetchGitHubReadme(ctx context.Context, repoURL string) (string, error)          // Agent B 实现（骨架已建，当前返回空串）
PocBackfillStatus{Running,Done bool; Processed,Matched int; Error,StartedAt,FinishedAt string} // 回填共形 wire
```

## §3 已冻结调用点（签名不符=编译失败）
```go
// vulndb_sync.go runOnce（主会话已写死，B/D 不得删除）：
if p, err := v.syncTrickest(ctx); err != nil { ... }   // → Agent B 实现
go v.s.maybeTriggerPocReview()                          // → Agent D 实现
// server.go（主会话已写死）：
registerVulnTaskRoutes(mux, s) / registerTrickestBackfillRoutes(mux, s)
registerNucleiBackfillRoutes(mux, s) / registerPocReviewRoutes(mux, s)
// agent 侧（主会话已写死，A 接线用）：
wk/pl/main.SetVulnBrief(fn func(taskID string) string)
wk/pl/main.SetVulnSearchFn(fn func(ctx, query, severity, kev, hasPoc string, limit int) (string, error))
// A 的两个实现签名（骨架已建）：
(s *Server) vulnBriefBlock(taskID string) string
(s *Server) vulndbSearchFor(ctx context.Context, query, severity, kev, hasPoc string, limit int) (string, error)
```

## §4 Agent A — 任务↔漏洞库联动三件套
1. **简报** `vulnBriefBlock`：开关判据任务级 `t.VulnBrief`（db.Task 三态）→ 全局 `vulndb_task_brief`（缺省开）；关闭返回 ""。指纹=任务作用域资产的 `technologies`+`service_name`+`page_title` 分词（`db` 资产查询参考 `memoryScopesForTaskID`/`QueryByTask`），停用词表（nginx/apache…**不停用**——指纹有价值；停的是 html/script/text/javascript/http/https/www/com/运维类泛词等，自建 `vulnBriefNoiseWords` map+单测）。`MatchVulnEntriesForFingerprints(词, 30)`，简报格式：`【CVE 情报简报——按本任务资产指纹匹配（N 条）】`+KEV 段+高危段（critical/high）+其他段，每条 `- [KEV][critical] CVE-xxx 标题（厂商 产品）PoC xN`；尾注「可用 vulndb_search 工具按指纹/CVE 深查；PoC 仅作验证参考，使用前遵守任务约束」。per-task 缓存 10min（sync.Map+时间戳）。LLM 不参与（纯确定性）。命中为空返回 ""。
2. **工具后端** `vulndbSearchFor`：`ListVulnEntries{Q,Categories:["0day","nday","cve","cnvd"],Severity,Kev,HasPoc,Limit}`，文本输出每条 `CVE编号|标题|severity|KEV?|厂商 产品|PoC xN|tags`，上限 limit（1-30 夹取）。
3. **API** `taskVulnBrief`：`{"brief":{task_id,enabled,generated_at,fingerprints,entries}}`；任务不存在 404；任务行 `VulnBrief=false` 或（nil 且全局关）→ enabled=false + 空 entries。fingerprints 返回实际参与匹配的词（调试用）。
4. **接线** task_llm.go 三处 `SetVulnBrief(s.vulnBriefBlock)` + `SetVulnSearchFn(s.vulndbSearchFor)`（紧挨 SetMemoryBrief 行）。
5. **前端** `vuln-briefing-dialog.tsx`（复用 memory-briefing-dialog 结构）：简报卡对话框（fingerprints 徽标+按 KEV/高危分组表格）；任务详情总览卡（命中数>0 显示「CVE 情报 N 条」入口）+创建抽屉高级设置「CVE 情报联动」三态 Select（哨兵 "inherit"，Radix 空串坑）→ `createTask({vulnBrief})`。
6. 单测：`vulnBriefNoiseWords`/指纹分词/简报组拼（纯函数，不出网）。

## §5 Agent B — trickest 聚合 + GitHub 扩量 + README 抓取
1. `syncTrickest`（骨架已建）：`StartVulnSyncRun("trickest")`；队列=`ListVulnEntries`三次（Kev="1"+HasPoc="0" → Severity 两轮 critical/high+HasPoc="0" → Sort="created"+HasPoc="0"）合并去重至 `trickest_limit`（settings，0=用默认 200）；逐条 `pocorexpFetchText(ctx, "https://raw.githubusercontent.com/trickest/cve/main/<年>/<CVE>.md")`（404=无记录跳过；单条 10s 预算、总量预算 8min）；`parseTrickestMain(md)` 纯函数：返回 `{github []string, refSecures []string, refs []string}`（按 `#### Github`/`#### Reference` 段切，行首 `- ` 提 URL；refSecures=域名命中 packetstormsecurity/exploit-db/seebug/vulhub/hackerone/gitlab/gitee 的引用）；Github 段 cap **20**/条目 + refSecures cap 5 → `CreateVulnPoc{Source:"trickest", Kind:"poc", SourceURL}`（Content=链接行文本），其余 refs → `MergeVulnRefs`（cap 10）。FinishVulnSyncRun(pocs=新增行数)。
2. `parseTrickestMain` 单测（用 §0.2 真实样例结构造 fixture，含无 POC 段/空段/重复 URL）。
3. `trickestBackfill`：光标 settings `vulndb_trickest_cursor` JSON `{done bool, last_id int64}`（按 vulndb_entries.id 升序推进，WHERE poc_count=0 AND cve_id<>''），200ms/条（≈5 req/s）、总预算 2h、取消=ctx+内存 flag；三个 handler 照 backfill_api.go 模式（202/409/404），状态体=PocBackfillStatus（§2）。
4. `syncGitHubPoCs` 扩量（vulndb_sync.go）：抽样改为三优先级同 §5.1 队列，条数=settings `vulndb_poc_hunt_github_limit`（默认 120，无 token 时夹回 ≤10 防限流炸）；KEV 优先靠队列序，2s 节流与 401/403 快速失败保持。
5. `FetchGitHubReadme(ctx, repoURL)`（骨架已建）：归一 owner/repo（剥 https/github.com/tree//blob/ 尾路径），raw 依次试 README.md/readme.md/README.en.md/README-cn.md，上限 4KB，共享 B 的 raw 限速器；404=("", nil)。
6. `syncGitHubPoCs` 入库后对新 PoC content 为空者补抓 README（每轮 cap 20 条防超时）。

## §6 Agent C — nuclei 模板正文全量入库
1. `nucleiBodyBackfill`：复用 vulndb_pocsources.go 的 `nucleiTreeAPI`/`nucleiParseTree`/`nucleiFilterCVEPaths` 拿「路径→CVE」映射；目标行=db 里 `source='nuclei' AND content=''` 的 PoC（新 db 查询可在 C 的文件里写裸 SQL，经 `s.m.PG()`，只读 SELECT 允许；写入必须走 `SetVulnPocContent`——它幂等处理 UPDATE/补建）。逐个 raw 下载 YAML（200ms 间隔+429/5xx 指数退避，总预算 2h），光标 settings `vulndb_nuclei_body_cursor` `{done bool, last_id int64}`（按 poc.id 升序）。
2. 三个 handler（骨架已建）填实现：GET=status、POST=启动（409 进行中）、DELETE=取消；状态体=PocBackfillStatus。
3. `syncNucleiTemplates`（vulndb_pocsources.go）：`nucleiAttachTemplates` 挂载新 PoC 行后**当场抓正文**（每轮新增量小，直接抓；失败保留空 content 等回填兜底，不报失败）。
4. 单测：光标推进/截断 JSON 解析（纯函数）。

## §7 Agent D — LLM PoC 审核软质检层
1. `maybeTriggerPocReview`（骨架已建）：`vulndb_poc_review`=="false" 或 `loadLLMConfig()` 不可用 → 直接 return；`pocReviewMutex` TryLock 防重入；取 pending PoC（D 文件内裸 SELECT，cap 50/轮，updated_at 早于 5min 前防抖）→ 逐条 `reviewOnePoc`（`pocReviewSem`(cap 2) 内执行）。
2. `reviewOnePoc`：内容=PoC.Content 为空且 SourceURL 是 github → `FetchGitHubReadme`（B 已实现，空内容注明「无正文」）；上下文=条目标题/CVE/厂商产品/描述前 500 字。系统提示词要点：你是 PoC 情报审核员；**只做文本判断，绝不访问/执行 PoC 内的任何 URL/payload**；输出严格 JSON `{"verdict":"approved"|"rejected","confidence":0~1,"poc_type":"runnable|reference|invalid","reason":"中文一句话"}`；rejected 判据=仓库 404/空壳求 star/课程与咨询推销/编号与 CVE 不匹配/纯论文无可用代码；nuclei 模板 ID 与 CVE 一致性校验。`analyzeWithRetry(retries=1, 60s)`；剥围栏→JSON 解析；失败/不可解析/confidence<0.6 → **保持 pending**（诚实降级，不猜）；verdict 有效 → `SetVulnPocReview(id, verdict, reason+"（poc_type）")`。
3. 三个 handler（骨架已建）：stats=VulnPocReviewStats；单条重审（同步调 reviewOnePoc，LLM 不可用 503，成功返回更新后 GetVulnPoc）；批量（body {ids?}，缺省=全部 pending cap 200，go 异步，202 {queued}）。
4. 单测：审核 JSON 解析/围栏剥离/低置信保持 pending（纯函数+假 Provider）。

## §8 验证要求（每个 agent 交付前自测）
- `GOSUMDB="sum.golang.org" go build ./server/ ./db/ ./agent/`（并行期他人 WIP 可能短暂红——以自己文件无编译错误+worktree 干净构建为准，急了用 `git worktree add out/build-<x> <你的提交>`）；定向 `go test ./server/ -run TestXxx`。
- 前端 agents 不跑 tsc（冻结基线已绿；新组件类型照 types.ts 冻结写法）。集成期主会话统一 tsc+挂载。
- 交付格式：文件清单 + 自查（红线逐条）+ 取舍说明。

## §9 明确不做（本轮范围外）
PoC 可执行性自动验证（沙箱跑）；DDG 全网批量猎取（trickest 已覆盖，单条归类取证保留）；hot_cves.csv 热榜富化；trickest summary_html/GitHub API 配额消耗路径；PoC 审核的流式进度推送（轮询足够）；web 端 github token 配置 UI（settings 手工配，`vulndb_github_token` 已有三处消费）。
