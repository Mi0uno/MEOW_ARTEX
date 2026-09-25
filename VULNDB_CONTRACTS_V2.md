# VULNDB_CONTRACTS_V2 — CVE 情报 v2(历史回填 + PoC/文章自动抓取)冻结契约

> 本文件是多 agent 并行开发的**唯一规范**。共享文件已由主会话改完并冻结,agents **只读**,
> 各自只写自己名下的文件。分支 `feature/cve-intel`。

## 0. 背景与决策(主会话已定,不要重新讨论)

- 现状:CVE 情报(`vulndb_entries` category=cve)只有定时器灌的近期数据(默认 6h 一轮,
  NVD 只拉最近 7 天),**没有历史存量**;PoC 只有 Exploit-DB 索引 + GitHub 搜索抽样(5 条/轮)。
- **回填范围决策:NVD 全量回填 10 年(2016-01-01 起,API 参数可改 start_year);更早年代由
  Exploit-DB 反向建档(只补确有公开 EXP 的老 CVE,约 2016 前数千条)。** 不做 15 年 NVD 全量:
  2011-2015 的 CVE 绝大多数影响 EOL 软件、CVSS v2 口径、无 PoC,全量灌入只会稀释检索体验。
- **PoC/情报新增源(全部挂进既有 6h 定时轮 `runOnce`,接线已由主会话完成)**:
  1. CISA KEV(在野利用标记) — Agent A
  2. PocOrExp_in_Github(ycdxsb/PocOrExp_in_Github,当日增量 Today.md + 历史逐年回填) — Agent B
  3. nuclei 官方模板(projectdiscovery/nuclei-templates,按 CVE 挂模板) — Agent B
  4. 安全社区文章 RSS(FreeBuf/安全客/先知,正则提取 CVE 编号,入库/补参考链接) — Agent C

## 1. 文件所有权矩阵(动了他人的文件 = 交付作废)

| 文件 | 归属 | 说明 |
|---|---|---|
| `db/schema.sql` | 主会话已冻结 | kev 列 + 部分索引 + ALTER 迁移(已写好) |
| `db/vulndb.go` | 主会话已冻结 | VulnEntry.KEV/Kev 过滤/SetKevByCVE/ListKevCveIds/MergeVulnRefs/MergeVulnTags/UpsertCVE refs 合并(已写好) |
| `server/vulndb_sync.go` | 主会话已冻结 | runOnce 已接线全部新源;syncExploitDB 反向建档;GitHub 搜索 token 化 |
| `server/server.go` | 主会话已冻结 | backfill 三条路由已注册 + `go s.vulnBackfillWorker(s.ctx)` 启动行 |
| `server/vulndb_api.go` | 主会话已冻结 | listVulnEntries 已接 kev 查询参数 |
| `web/src/lib/types.ts` | 主会话已冻结 | VulnEntry.kev?: boolean |
| `web/src/lib/api.ts` | 主会话已冻结 | kev 查询参数 + vulnBackfillStatus/startVulnBackfill/cancelVulnBackfill |
| `server/vulndb_backfill.go` `vulndb_backfill_api.go` `vulndb_backfill_test.go` | **Agent A** | NVD 历史回填 worker + API + KEV |
| `server/vulndb_pocsources.go` `vulndb_pocsources_test.go` | **Agent B** | PocOrExp + nuclei + PocOrExp 历史回填 |
| `server/vulndb_articles.go` `vulndb_articles_test.go` | **Agent C** | 文章 RSS 抓取 |
| `web/src/app/(main)/function/vulndb/cve/page.tsx` | **Agent C** | CVE 页前端 |
| `web/src/components/vulndb-dialogs.tsx` | **Agent C** | 只做**增量**修改(该文件被 0day/Nday 页共用,别破坏既有行为) |

**禁止**:任何 agent 不得 `git stash` / `git checkout` / 切分支 / 提交;不得改上表「已冻结」文件;
不得动 8787 实例与 5432 正库;不起服务(冒烟由主会话统一做)。
**Mimosa 约束**:源码一律用 Write/Edit 工具写(Bash heredoc 会被拦);源码里禁止出现 os/exec;
所有出网请求必须先过 `recon.ValidatePublicURL(url)`(http/https、拒绝内网/环回/保留地址)。

## 2. 可直接复用的既有设施(server 包内,均已存在)

- `v.s.m.PG()` → `*db.DB`。漏洞库方法:`UpsertCVE(cveID, title, description, severity, vendor, product, affected string, refs json.RawMessage) (id int64, inserted bool, err error)`、
  `FindVulnEntryByCVE(cveID) (*db.VulnEntry, error)`(无则 nil,nil)、`CreateVulnPoc(db.VulnPocInput{EntryID,Name,Kind,Language,Content,Source,SourceURL}) (*db.VulnPoc, bool, error)`
  (同 entry+source_url 已存在 → 返回 created=false,幂等)、`CreateVulnEntry(db.VulnEntryInput)`、
  `SetKevByCVE(cveID string, kev bool) (exists bool, err error)`、`ListKevCveIds() ([]string, error)`、
  `MergeVulnRefs(entryID int64, urls []string) error`、`MergeVulnTags(entryID int64, tags []string) error`、
  `StartVulnSyncRun(source string) (int64, error)` / `FinishVulnSyncRun(id, status, fetched, inserted, updated, pocs int, errMsg string) error`(参照 vulndb_sync.go 各 sync 函数的 defer 写法)、
  `GetSetting(key) (value string, ok bool, err error)` / `SetSetting(key, value string) error`、
  `ListVulnEntries(db.VulnEntryFilter{...}) ([]*db.VulnEntry, int, map[string]int, error)`。
- `fetchJSON(ctx, rawURL, headers map[string]string, out any) error`(vulndb_sync.go,内部已过
  ValidatePublicURL,UA=ARTEX-VulnDB/1.0,429 报「上游限流(429)」)。
- `truncateRunes(s, max)`、常量 `vulnBackfillWindowStart = 2016`。
- sync_run 的 source 取值(v2 新增,前端按此显示中文):`nvd_backfill`(A,每个窗口一条)、
  `kev`(A)、`pocorexp_today`(B)、`nuclei`(B)、`articles`(C)。

## 3. 已冻结的调用点(签名必须逐字一致,否则编译失败)

`server/vulndb_sync.go` 的 runOnce 已写好以下调用:

```go
n, err := v.syncKEV(ctx)              // Agent A
p, err := v.syncPocOrExpToday(ctx)    // Agent B
p, err := v.syncNucleiTemplates(ctx)  // Agent B
p, err := v.syncArticleFeeds(ctx)     // Agent C
```

`server/server.go` 已冻结注册:backfill 三条路由指向 A 的 handler,以及启动行
`go s.vulnBackfillWorker(s.ctx)`(A 实现该方法)。A 还需在 `vulndb_backfill.go` 内提供
handler 用的共享状态(单例 mutex/原子 + 光标读写),全部放自己的文件里。

```go
func (v *vulnSync) syncKEV(ctx context.Context) (marked int, err error)
func (v *vulnSync) syncPocOrExpToday(ctx context.Context) (pocs int, err error)
func (v *vulnSync) syncNucleiTemplates(ctx context.Context) (pocs int, err error)
func (v *vulnSync) syncArticleFeeds(ctx context.Context) (merged int, err error)
func backfillPocOrExpYears(ctx context.Context, pg *db.DB, startYear, endYear int) (attached int, err error) // Agent B,包级函数
func (s *Server) vulnBackfillStatus(w http.ResponseWriter, r *http.Request)  // Agent A
func (s *Server) vulnBackfillStart(w http.ResponseWriter, r *http.Request)   // Agent A
func (s *Server) vulnBackfillCancel(w http.ResponseWriter, r *http.Request)  // Agent A
```

**辅助函数命名隔离**(同包防冲突):各 agent 新增的包级 helper 一律带自己前缀
(`backfill*`/`pocorexp*`/`nuclei*`/`article*`),不许定义与既有函数同名或与其它 agent
可能重名的通用名(如 `fetchText`)。

## 4. Agent A — NVD 历史回填 + KEV(`server/vulndb_backfill.go` + `_api.go` + `_test.go`)

### 4.1 回填 worker(独立 goroutine,不进 runOnce 轮询)
- API:`POST /api/vulndb/backfill` body `{"start_year": 2016}`(缺省/0 = `vulnBackfillWindowStart`)
  → 202 `{"status":"started"}`;已在跑 → 409 `{"error":"回填进行中"}`。start_year 夹取 [2011, 当前年]。
- `GET /api/vulndb/backfill` → 200 `{"running":bool,"phase":"nvd"|"pocorexp"|"", "start_year":int,
  "window_end":string(ISO,空=未开始),"done":bool,"error":string}`(从 settings 光标 + 内存态读)。
- `DELETE /api/vulndb/backfill` → 200 `{"status":"cancelled"}`;没在跑 → `{"status":"idle"}`。
  取消=优雅停止:当前请求完成后停,光标保留。
- **断点续跑**:进度存 settings 键 `vulndb_backfill_cursor`,JSON:
  `{"phase":"nvd|pocorexp","window_end":"2020-05-01T00:00:00.000","year_done":0,"start_year":2016,"done":false}`
  (year_done 仅 pocorexp 阶段用=已完成的最大年份)。**自动续跑**:主会话已在 server.go 冻结了
  启动行 `go s.vulnBackfillWorker(s.ctx)`(紧跟 vulnSync 启动处)。A 在 `vulndb_backfill.go` 里实现
  `func (s *Server) vulnBackfillWorker(ctx context.Context)`:启动先 sleep 30 秒(避开启动期争抢),
  然后若光标存在且 done=false 且 `vulndb_sync_enabled != "false"` → 续跑;光标 done=true 或不存在
  → 直接返回(该 goroutine 一次性退出;手动触发走 API 的独立 goroutine)。
- **NVD 拉取**:窗口 120 天(官方上限),从 `start_year-01-01` 推进到今天;每窗口
  `https://services.nvd.nist.gov/rest/json/cves/2.0?pubStartDate=<ISO>&pubEndDate=<ISO>&resultsPerPage=2000&startIndex=<n>`
  (ISO 格式 `2006-01-02T15:04:05.000`,UTC)。totalResults > 2000 时翻页。**无 API key 限速
  5 次/30 秒 → 每个请求之间 sleep 6.5 秒**;429/失败退避 30s 重试,同一窗口最多 3 轮。
- 解析:字段结构与 vulndb_sync.go 的 syncNVD 相同(id/descriptions/metrics/configurations/refs);
  severity 取 cvssMetricV31 → v30 → **v2 兜底**(v2 分数映射:>=9 critical,>=7 high,>=4 medium,>0 low,
  注释注明这是展示口径不是评分口径)。每条 `UpsertCVE(...)`(幂等,中断重跑安全)。
  每个窗口写一条 sync_run(source=`nvd_backfill`),finished 时更新。
- 完成后自动进入 pocorexp 阶段:调 `backfillPocOrExpYears(ctx, v.s.m.PG(), startYear, 当前年)`
  (B 提供),然后把光标 done=true。ctx 用 `context.WithTimeout(整体 24h)`+可取消。
- 并发保护:backfill 与 runOnce 的常规轮互不互斥(数据面靠 UpsertCVE/CreateVulnPoc 幂等),
  但 backfill 自身全局单例(包级 mutex/原子)。

### 4.2 KEV(`syncKEV`)
- 拉 `https://www.cisa.gov/sites/default/files/feeds/known_exploited_vulnerabilities.json`
  (纯文本+JSON,自己写 helper,命名 `backfillFetch...` 前缀),解析 `vulnerabilities[].cveID`。
- 全量 upsert 标记:`SetKevByCVE(cve, true)`;开始前 `ListKevCveIds()` 记旧集合,结束后对
  旧集合中不在新集合的 `SetKevByCVE(id, false)`(目录移除反向清除)。marked=新置 true 数。
- sync_run source=`kev`。失败不 panic,错误经 sync_run 与返回值上抛(runOnce 会打日志)。

### 4.3 单测(`vulndb_backfill_test.go`,纯函数、不连库)
- 光标 JSON 序列化/反序列化 round-trip;v2 分数→severity 映射表驱动;KEV JSON 解析(内联 fixture)。

## 5. Agent B — PocOrExp + nuclei(`server/vulndb_pocsources.go` + `_test.go`)

### 5.1 数据格式(已实测)
`https://raw.githubusercontent.com/ycdxsb/PocOrExp_in_Github/main/Today.md`(约 24KB,每日更新)
与 `https://raw.githubusercontent.com/ycdxsb/PocOrExp_in_Github/main/<year>/README.md`(每年约 200KB,
1999..2026 都有)格式一致:

```
## CVE-2024-49138
一段描述文字…
- [https://github.com/USER/REPO](https://github.com/USER/REPO) :  ![starts](https://img.shields.io/github/stars/USER/REPO.svg) ![forks](…)
- [https://github.com/USER2/REPO2](…) :  …
```

- 纯文本抓取 helper 自己写(命名 `pocorexpFetch...` 前缀,先 `recon.ValidatePublicURL`,UA 同现有,
  **LimitReader 64MB**——主文件 PocOrExp.md 12MB,别用无界读)。
- `func parsePocOrExpMD(content string) map[string][]string`(包级,可测):按 `## CVE-` 标题分段,
  行内正则抽 `https://github.com/<owner>/<repo>` 链接(bullet 行);**只要 github.com 链接**,
  去重保序;CVE 编号校验 `^CVE-\d{4}-\d{4,7}$`。

### 5.2 增量(`syncPocOrExpToday`,挂 runOnce)
- 拉 Today.md → parse → 对每个 CVE:`FindVulnEntryByCVE`,条目存在才挂 PoC
  (不存在跳过——NVD 回填/常规轮会建),`CreateVulnPoc{Kind:"poc", Source:"pocorexp",
  SourceURL:repoURL, Name:"PocOrExp: <owner/repo>", Language:"", Content:"GitHub PoC/EXP: <url>"}`
  (created=false 的幂等跳过不算数)。pocs=新增数。sync_run source=`pocorexp_today`。

### 5.3 历史回填(`backfillPocOrExpYears`,包级函数,被 A 的回填 worker 调)
- 对 startYear..endYear 每年拉 `<year>/README.md`(某年 404=当年无数据,跳过继续),
  parse 后同 5.2 挂载;每年之间 sleep 1 秒;返回总挂载数。**只挂不建**,全量幂等可重跑。

### 5.4 nuclei 模板(`syncNucleiTemplates`,挂 runOnce)
- 数据:`https://api.github.com/repos/projectdiscovery/nuclei-templates/git/trees/main?recursive=1`
  (单次调用拿全库树;若响应 `truncated`=true,回退逐目录 `contents/http/cves/<year>`,
  year 从 `vulnBackfillWindowStart` 到当前年)。
- 筛路径 `http/cves/CVE-<year>-<id>.yaml`(regex 从文件名提 CVE),每条对库内已有条目挂:
  `CreateVulnPoc{Kind:"nuclei", Source:"nuclei", SourceURL:"https://github.com/projectdiscovery/nuclei-templates/blob/main/<path>",
  Name:"nuclei 模板: <CVE>", Language:"yaml", Content:"nuclei 官方模板,可直接用 nuclei -t <path> 运行。\n<path>"}`。
- 树响应可达数 MB,LimitReader 64MB + `github_token` 尊重:vulnSync 里已有 `settingVulnGitHubToken`
  (键名 `vulndb_github_token`),有 token 就加 `Authorization: Bearer` 头(核心 API 60 次/时 → 5000 次/时)。
- sync_run source=`nuclei`。

### 5.5 单测(纯函数,不连库、不出网)
- `parsePocOrExpMD`:多链接/无链接行/非 github 链接/坏标题/重复 CVE;
- nuclei 树 JSON(内联 fixture,含 truncated 字段)→ 路径筛选函数。

## 6. Agent C — 安全社区文章 RSS(`server/vulndb_articles.go` + 前端)

### 6.1 后端(`syncArticleFeeds`,挂 runOnce)
- 源表(写死在文件里,带注释说明都是公开 RSS):
  freebuf `https://www.freebuf.com/feed`、anquanke `https://api.anquanke.com/data/v1/rss`、
  xz(先知) `https://xz.aliyun.com/feed`。
- encoding/xml 解 RSS 2.0(channel/title/link/description/item[]);某源失败(超时 20s/非 200/解析错)
  记日志继续,不拖垮整源;**全部失败才**返回 error。总预算 90s。
- 每条 item:title+description 里正则 `(?i)CVE-\d{4}-\d{4,7}` 抽编号(去重);无编号的条目跳过。
- 每个编号:`FindVulnEntryByCVE` → 已有:`MergeVulnRefs(id, [itemLink])`;没有:
  `UpsertCVE(cve, title=cve+" "+truncateRunes(itemTitle,120), desc=truncateRunes(去 HTML 标签后的
  description,1000), severity="", ...)`,再 Find 回来 `MergeVulnTags(id, ["文章"])`。
  同一 item 内多 CVE 共享一个 link。merged=本轮动作数(建条目+补 ref 合计)。
- sync_run source=`articles`。
- HTML 清洗:只去标签(`<[^>]+>` → "")+ `html.UnescapeString`,不引第三方依赖。

### 6.2 前端(CVE 页 + vulndb-dialogs,增量修改)
页面:`web/src/app/(main)/function/vulndb/cve/page.tsx`;对话框:`web/src/components/vulndb-dialogs.tsx`。
已冻结可用的 client:`api.vulnSyncStatus()`(runs 里会出现新 source 值)、`api.vulnBackfillStatus()/
startVulnBackfill({start_year})/cancelVulnBackfill()`、`api.vulnEntries({... kev: "1"|"0"|"all"})`、
`VulnEntry.kev?: boolean`。
- **同步面板升级**(页面已有雏形):按 source 分组显示各源最近一轮(状态/新增/PoC/时间),
  source 中文映射:NVD / GitHub Advisory / Exploit-DB / GitHub PoC / CISA KEV(在野利用) /
  PocOrExp / nuclei 模板 / 安全社区文章 / NVD 历史回填。
- **历史回填卡片**:回填状态(运行中显示 phase 中文:「NVD 历史分窗拉取中(已推进至 <window_end 日期>)」
  /「PocOrExp 历史挂载中」)+「启动历史回填」按钮(起始年选择 2016(推荐)/2011/2020,确认对话框写明
  「全量约 20+ 万条,首次回填预计 15-30 分钟,可中断续跑,不影响实例正常使用」)+ 运行中取消按钮。
  running 时每 5s 轮询。
- **列表**:严重度旁加「在野利用」筛选(全部/在野利用/非在野),表格加 KEV 徽标(红色小 Badge「KEV」,
  title 提示 CISA 已知在野利用)。**列宽铁律(踩过两次)**:`table-fixed` + 标题列不设宽 + 其余列给
  最小可读显式宽 + 内容 truncate;新列宽度从现有固定列里省出来,固定列总和必须明显小于容器宽。
- **详情对话框 PoC 列表**:source 显示中文徽标(PocOrExp/nuclei 模板/Exploit-DB/GitHub)。
- 前端校验:`cd web && npx tsc --noEmit`(next build 由主会话统一跑,agents 不跑 build)。

### 6.3 单测
CVE 正则抽取(中英混排/多编号/大小写/误报如 "CVE-2024-" 截断)、HTML 清洗、RSS XML 解析(内联 fixture)。

## 7. 验证要求(每个 agent 交付前自查)

1. `cd "D:\AI\MEOW_ARTEX" && GOSUMDB="sum.golang.org" go build ./server/` —
   报错若**只**涉及其他 agent 的符号(`syncKEV`/`syncPocOrExpToday`/`syncNucleiTemplates`/
   `syncArticleFeeds`/`backfillPocOrExpYears`/`vulnBackfill*`,视你角色而定)可忽略;其余必须清零。
   (`go build ./...` 会被 data/tasks 残留 .go 污染,禁止使用。)
2. `GOSUMDB="sum.golang.org" go test ./server/ -run '<你的测试名前缀>'` 必须全绿
   (纯函数测试,不需要数据库,不要设 ARTEX_PG_DSN 去连任何库)。
3. 前端改动(仅 C):`cd web && npx tsc --noEmit` 清零。
4. 交付格式:文件清单 + 自查结果 + 有意做出的取舍点,一段话。

## 8. 已冻结 API 一览(前端契约,主会话已写进 api.ts)

```
GET    /api/vulndb/backfill              → {running,phase,start_year,window_end,done,error}
POST   /api/vulndb/backfill {start_year} → 202 {status:"started"} | 409 {error}
DELETE /api/vulndb/backfill              → {status:"cancelled"|"idle"}
GET    /api/vulndb/entries?kev=1|0       → 既有列表接口新增 kev 过滤参数
```

## 9. 明确不做(二期再说)

- 不接商业情报源;不做 PoC 可用性自动验证(verified 字段仍只由人工/任务实测置位);
- 不做文章正文全文抓取(RSS 摘要层即可);不加 LLM 造价(全链路确定性,零 token 成本);
- PocOrExp 反向建档(只挂不建,老洞建档由 Exploit-DB 反向负责)。
