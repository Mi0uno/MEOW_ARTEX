# ASSET_EXTRACT_CONTRACTS.md — 任务资产识别入库（契约冻结 v1）

> 目标：解决「agent 任务跑完没有落库对应资产」。三条链路：
> ① **确定性提取**（终态自动 + 手动重跑）：扫任务过程数据提取 URL/IP/域名候选 → `staged_assets` 暂存；
> ② **LLM 辅助识别**（可开关）：对候选 + 任务上下文判定，确认的直接采纳入库；
> ③ **手动归档**：任务详情「测试资产」页签新增归档对话框，任意类型单条直录。
>
> 所有权矩阵（**其余文件一律只读**）：
> | 文件 | 归属 |
> |---|---|
> | `db/schema.sql`（asset_extract_runs 块） | 主会话已冻结，只读 |
> | `web/src/lib/types.ts` / `api.ts` / `task-assets.ts` | 主会话已冻结，只读 |
> | `web/src/components/task-detail-view.tsx`（staged 页签） | 主会话已冻结，只读 |
> | `db/asset_extract.go`、`server/asset_extract.go`、`server/asset_extract_api.go` | Agent B 新建 |
> | `server/server.go`（钩子+4 路由）、`server/recon_api.go`（adoptStagedAsset 扩展）、`server/assets.go`（insertAssets 扩展） | Agent B 独占修改 |
> | `web/src/components/task-asset-archive.tsx`（新建）、`web/src/app/(main)/function/tasks/detail/_tabs/assets-tab.tsx` | Agent W 独占修改 |

---

## 1. 数据模型（已冻结进 schema.sql）

```sql
CREATE TABLE IF NOT EXISTS asset_extract_runs (
    id                  BIGSERIAL PRIMARY KEY,
    task_id             BIGINT NOT NULL UNIQUE REFERENCES tasks(id) ON DELETE CASCADE,
    status              TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending','running','done','partial','failed')),
    deterministic_count INT NOT NULL DEFAULT 0,   -- 确定性提取入暂存数
    ai_confirmed_count  INT NOT NULL DEFAULT 0,   -- LLM 确认的候选数
    ai_new_count        INT NOT NULL DEFAULT 0,   -- LLM 新发现（候选外）数
    adopted_count       INT NOT NULL DEFAULT 0,   -- 本次实际入库数
    detail              JSONB NOT NULL DEFAULT '[]',  -- 候选决策明细 cap300
    error               TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
```
（附 set_updated_at 触发器，同既有表风格。）

**status 语义**：`done`=全链成功（含 AI 段）；`partial`=确定性段完成但 AI 段跳过/失败（`error` 存原因）；`failed`=管线异常；`running` 中重复触发 → 409；`pending` 仅内部瞬态。

## 2. db 层（Agent B，新建 `db/asset_extract.go`）

- `AssetExtractRun` 结构体（json tag 全小写下划线，wire 见 §6）+ `UpsertAssetExtractRun(*AssetExtractRun) (int64, error)`（按 task_id upsert，存 detail JSONB）。
- `GetAssetExtractRunByTask(taskID int64) (*AssetExtractRun, error)`（无行返回 nil,nil）。
- `TaskActivityTexts(taskID int64, limit int) ([]TaskActivityText, error)`：`SELECT a.kind,a.summary,a.detail FROM activity a JOIN explorations e ON e.id=a.exploration_id WHERE e.task_id=$1 ORDER BY a.id DESC LIMIT $2`；`TaskActivityText{Kind, Summary, Detail string}`。行号列名以 db/exploration.go 实际 schema 为准（activity 列：kind/summary/detail）。
- `TaskFindingEvidence(taskID int64, limit int) ([]string, error)`：`SELECT evidence FROM findings WHERE task_id=$1 AND COALESCE(evidence,'')<>'' ORDER BY id DESC LIMIT $2`。
- 任务企业归属：复用 db/task_scope.go 既有查询取 `kind='company'` 的 company_id 列表（agent 自行 grep 函数名；取不到 = 空列表）。

## 3. 提取管线（Agent B，`server/asset_extract.go`）

### 3.1 触发与门禁
- 终态自动：`server.go` 终态钩子组合回调追加 `s.maybeStartAssetExtract(taskID)`（在 `maybeStartMemoryReflect` 之后）。
- `maybeStartAssetExtract(taskID)`：任务不存在/`Kind != "agent"` → 直接返回（**不写 run 行**）；`GetAssetExtractRunByTask` status=='running' → 跳过；否则 `go s.runAssetExtract(taskID)`。
- 手动触发（API §5 POST）：同样要求 kind==agent，recon 任务 400「信息搜集任务请使用暂存区采纳」；running 中 409；LLM/开关不校验（确定性段总是可跑）。
- 并发闸 `assetExtractSem = make(chan struct{}, 1)`（仿 memory_reflect.go:44）。

### 3.2 确定性段（零 LLM）
1. 取数：`TaskActivityTexts(taskID, 600)`（activity 全 kind，但只有 summary+detail 非空的参与）+ `TaskFindingEvidence(taskID, 100)`；总扫描字符 cap 500KB（超出截断）。
2. 预处理：先删 email（`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`）再扫 hostname，防邮箱右半被误提。
3. 正则（RE2，Go 标准库）：
   - URL：`(?i)https?://[^\s"'`+ "`" + `<>()\[\]{}\\,;]+`（尾部 `.,;:)]}` 剥离）→ host/port/url；
   - host:port：`\b(?:[a-zA-Z0-9-]+\.)+[a-zA-Z]{2,}:\d{2,5}\b`；
   - IPv4:port 与裸 IPv4：`\b\d{1,3}(\.\d{1,3}){3}(:\d{2,5})?\b`（octet≤255 校验；丢弃 127.*/0.0.0.0/255.255.255.255，**内网 IP 保留**——渗透场景是真实资产）；
   - 裸 hostname：`\b(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}\b`（≤253 字符）。
4. 去重：run 内按 (type,value,port) 去重；**已是本任务资产的跳过**（复用 QueryByTask 取 task_ids 反查集合比对）；staged 重复靠 `InsertStagedAsset` ON CONFLICT 幂等。
5. 写暂存：`db.StagedAsset{TaskID, Collector: "auto_extract", CompanyID: companyOverride, Type, Value, Port, Extra}`，cap **300** 条（超出丢弃并计数）。类型映射：
   - URL → `service`（extra.url=完整 URL，value=host，port=显式端口或 0）；
   - host:port(域名) → `service`（无 extra.url，value=host，port）→ 采纳时走 UpsertOtherService；
   - host:port(IP) / 裸 IP → `ip`；
   - 裸 hostname → `domain`。
6. **companyOverride**：任务企业列表**恰好 1 家**时 = 该 id，否则 0（0=不强制，走 ICP/范围自动认领，与现有 adopt 行为一致）。

### 3.3 LLM 段（开关 + 降级）
- 开关 settings key `asset_extract_ai`，**缺省开**（仅显式 `"false"` 关）——读写走 `s.m.PG().GetSetting/SetSetting`。
- 关闭 / `s.globalProvider()` 不可用 / LLM 调用最终失败 → **不阻断**：run 置 `partial`（error 存原因），确定性候选留在暂存 pending。
- 输入（user prompt）：任务元信息（ID/goal/description/stage）+ 授权范围摘要（task_scope kind=domain 的值 + 企业名，取不到就写「未登记」）+ 候选清单（编号列表，cap 300，每行 `#i type value[:port] (来源段 kind)`）+ 最近 40 条 tool_use summary 摘录（cap 40KB，帮 LLM 发现候选外资产）。
- 系统提示要点：你是资产甄别助手；**只确认与任务目标/授权范围相关的真实基础设施**（页面正文里引用的第三方域名、文档示例、包名、统计 SDK 域名一律不确认）；输出**只有一个 JSON 对象**：
  ```json
  {"confirmed":[0,2,5],"missing":[{"type":"domain|ip|service","value":"...","port":0,"url":""}],"note":"一句话依据"}
  ```
  confirmed=候选下标；missing=过程数据里出现但候选清单漏掉的资产，cap 50；拿不准就不确认（宁缺勿滥）。
- 解析：复用围栏容错模式（剥 ```json 后取首 `{` 到末 `}`，仿 parseMemoryReflectJSON server/memory_reflect.go:590）；解析失败 = partial。
- LLM 调用：`analyzeWithRetry(ctx, prov, system, user, 1, 100*time.Second)`（server/report_pipeline.go:57，勿改它）；ctx 总超时 240s（仿 memory_reflect.go:347）。

### 3.4 采纳入库（AI 确认 + missing 都走这条）
1. missing 先插暂存行（Collector=**"ai_extract"**，同 §3.2 类型映射/CompanyID）；
2. 逐条 `s.adoptStagedAsset(as, taskID, sa, companyOverride)`（§4 扩展签名）拿 asset_id → `as.SetTaskAssetSource(taskID, assetID, "ai", "AI 识别入库 · "+value, nil)` → `db.DecideStagedAsset(taskID, stagedID, "accepted")`；
3. adopted_count 累计；单条失败记日志继续。
- **确定性候选中未被 AI 确认的保持 pending**，人工在「暂存区」页签采纳。

### 3.5 run 落库
- 开始时 upsert `status=running`（清零计数）；结束按 §1 语义置 done/partial/failed + 计数 + detail（每候选 `{value,type,port,decision:"confirmed"|"pending"|"dup"}` cap300）。

## 4. `adoptStagedAsset` 扩展（Agent B，server/recon_api.go）

签名 `adoptStagedAsset(as *db.AssetStore, taskID int64, a *db.StagedAsset, companyID int64)`：
- `companyID > 0` 时给每个 `Upsert*Req` 填 `CompanyID`（**跳过 ICP 解析强制归属**，与资产导入 semantics 一致）；`=0` 时行为与现状完全一致（ICP/范围自动认领）。
- 两处既有调用点（`stagedAssetDecide`、`addSRCTargetAssets`）都传 0，**行为零变化**。

## 5. API（Agent B，`server/asset_extract_api.go` + server.go 路由）

| 方法/路径 | 语义 | 响应 |
|---|---|---|
| `GET /api/tasks/{id}/asset-extract` | 取 run（无则 `{run:null}`） | `{run: AssetExtractRun\|null}` |
| `POST /api/tasks/{id}/asset-extract` | 手动触发（202 异步）；kind≠agent 400；running 409 | `{run_id:int64}` |
| `GET /api/asset-extract/config` | 读开关 | `{ai_enabled:bool}` |
| `PUT /api/asset-extract/config` | 写开关（body `{"ai_enabled":bool}`） | 同上 |

## 6. wire 契约（已冻结进 types.ts / api.ts）

```ts
export interface AssetExtractRun {
  id: number; task_id: number;
  status: "pending" | "running" | "done" | "partial" | "failed" | string;
  deterministic_count: number; ai_confirmed_count: number;
  ai_new_count: number; adopted_count: number;
  error: string; created_at: string; updated_at: string;
}
export interface AssetExtractConfig { ai_enabled: boolean; }
export interface TaskAssetUpsert { /* 见 types.ts，字段名与 POST /api/assets 内层 assets[] 完全一致 */ }
```
api.ts：`taskAssetExtract` / `runAssetExtract` / `assetExtractConfig` / `saveAssetExtractConfig` / `archiveTaskAsset(taskId, asset)`（POST /assets，body `{task_id, source:"manual", source_summary:"手动归档", assets:[asset]}`）。

## 7. `insertAssets` 扩展（Agent B，server/assets.go）

请求根级加 `Source string json:"source"` / `SourceSummary string json:"source_summary"`；`TaskID>0` 时 `SetTaskAssetSource` 的 source/summary 用请求值，空串回落 `"api"` / `"通过资产 API 登记"`（既有调用零变化）。

## 8. 前端（Agent W）

1. **assets-tab.tsx 头部**（「测试资产」标题行右侧，现有「新增测试资产」按钮左侧）：
   - AI 识别状态徽标：加载 `api.taskAssetExtract(taskId)`；无 run=灰点「未识别」；running=Spinner「识别中」（5s 轮询至终态，终态后刷新资产列表）；done=`已入库 N`（tooltip：提取 x · AI确认 y · 新发现 z）；partial=黄「部分完成」（tooltip error）；failed=红「识别失败」（tooltip error）。
   - 「AI 识别」Switch（`api.assetExtractConfig/saveAssetExtractConfig`，一次加载，变更即存 + toast）。
   - 「重新识别」icon 按钮（RefreshCwIcon，title=重新识别资产）→ `api.runAssetExtract` → toast「已开始识别」+ 进轮询；409 toast「识别正在进行中」。
   - 「归档资产」按钮 → 新组件 `task-asset-archive.tsx`（Sheet 仿 AddTaskAssetsSheet 骨架）：类型 Select（根域名/子域名/IP/HTTP 服务/端口服务/应用）+ 动态字段（域名；域名+解析类型；IP+绑定域名(可选)；URL+标题(可选)；host 或 IP+端口+服务名(可选)；应用名+地址(可选)）→ `api.archiveTaskAsset` → 成功 toast「已归档」+ onArchived=refresh。
2. **staged 页签**（主会话已挂进 task-detail-view.tsx，Agent W 不动该文件）：确认 ReconStagedPanel 在 agent 任务正常加载即可（GET staged-assets 与 stage 无关）。
3. 来源徽标：`taskAssetSourceLabel` 已补 `auto:"自动提取"` / `ai:"AI 识别"` / `recon:"信息搜集"` / `import:"文件导入"`（主会话已冻结），SourceCell 自动生效，无需改动。

## 9. 边界与红线

- **只对 `kind=="agent"` 任务跑提取**；recon/报告/C2 免杀等 pipeline 任务一律不碰。
- 确定性段永不写正式资产图，只进暂存——**只有 AI 确认或人工采纳才入库**。
- 内网 IP 保留；loopback/0.0.0.0/广播丢弃；email 预删除。
- 所有 db 错误 log-and-continue（单条资产失败不毁整轮）；run 行保证终态，不留悬挂 running（panic 用 defer 恢复置 failed 可选）。
- 任务删除 → run 行/暂存行 CASCADE，无需清理逻辑。
