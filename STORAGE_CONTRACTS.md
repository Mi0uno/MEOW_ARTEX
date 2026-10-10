# STORAGE_CONTRACTS · 存储桶接入 + 历史数据一键迁移（2026-10-10 冻结）

多 agent 开发契约。语义：**双写过渡——桶为真源 + 本地回退**。新产物照旧写本地（`.part`+rename），active 桶存在时异步上桶；读取走「本地 → 桶」回源。迁移引擎把历史文件搬上桶并登记 `storage_objects`。

## 分支与流程

独立分支开发，每个实施点一个 issue，提交走 PR，自审通过后合并并关联关闭 issue（`Closes #N`）。分支命名 `storage/<n>-<slug>`，从上一个点的合并结果顺延。

## 冻结共享文件（只允许主会话改，其他 agent 只读）

- `db/schema.sql` — 追加存储三表（见下）
- `web/src/lib/types.ts` — 追加 storage 类型段
- `web/src/lib/api.ts` — 追加 `// ---- 存储中心 ----` 段 + import 类型名
- `web/src/navigation/sidebar/sidebar-items.ts` — 系统设置组加「存储设置」

## 所有权矩阵

| 角色 | 范围 |
| --- | --- |
| 主会话 | 契约、冻结共享文件、集成、构建冒烟、PR 审核 |
| Backend-A | `storagecenter/` 包(新)、`db/storage.go`(新)、`db/storage_test.go`(新) |
| Backend-B | `server/storage_api.go`(新)、`server/storage_migrate.go`(新)、`server/storage_artifacts.go`(新)、`server/server.go` 路由区**只允许追加一行** `s.registerStorageRoutes(mux)` 与启动钩子 `initStorageMigration()` |
| Web | `web/src/app/(main)/system/storage/page.tsx`(新) 及 `_components/`(新目录) |

## 数据类别（迁移/纳管注册表）

| category | 本地根(相对 dataDir) | 说明 |
| --- | --- | --- |
| report-jobs | `report-jobs/` | 报告导出产物 |
| report-templates | `report-templates/` | 模板 orig docx |
| uploads | `tasks/*/uploads/` `sessions/*/uploads/` `drafts/*/uploads/` | 会话附件 |
| evidence | `evidence/blobs/` | 内容寻址(sha256 布局直接映射 key) |
| task-archives | `archives/` | 任务归档 zip |
| c2 | `c2/` | uploads/outputs/payloads/downloads |
| vps | `vps/outputs/` | VPS 执行输出 |
| wordbooks | `wordbooks/` | 侦察词表 |
| src-platform | `src-platforms/` | SRC 平台附件 |
| task-workdirs | `tasks/<id>/`(仅终态任务,排除 uploads) | transcripts/cmd-output |

**traffic 流量体不纳管**（热路径缓存，类别注册表留口后续可加）。

## schema（冻结，追加进 schema.sql 尾部）

```sql
-- 三表:storage_buckets / storage_objects / storage_migrations
-- 凭据 secretbox+base64;active 部分唯一索引保证单默认桶;
-- storage_objects 是回源与迁移幂等的登记表(key = <category>/<rel_path>);
-- storage_migrations 仿 report_jobs:status CHECK + cursor JSONB 游标。
```

字段以 schema.sql 实际追加块为准（本契约不复制全文避免漂移）。

## wire 契约（冻结）

| 方法 路径 | 请求 | 响应 | 状态码 |
| --- | --- | --- | --- |
| `GET /api/storage/buckets?protocol=&status=&q=&limit=&offset=` | — | `{items:StorageBucket[], total, counts:{s3,webdav}, status_counts:{ok,degraded,unchecked}}` | 200 |
| `POST /api/storage/buckets` | `{name,protocol,endpoint,bucket,region,access_key,secret_key,path_prefix,activate?}` | `{id}` | 200/400 |
| `PUT /api/storage/buckets/{id}` | 同上(secret_key 留空=不改) | `{updated}` | 200/404 |
| `DELETE /api/storage/buckets/{id}` | — | `{deleted}` | 200/404/409(active 或被迁移引用) |
| `POST /api/storage/buckets/{id}/check` | — | `{ok,error,latency_ms}` | 200 |
| `POST /api/storage/buckets/{id}/activate` | — | `{active:true}` | 200/409(未测活通过) |
| `POST /api/storage/deactivate` | — | `{active:false}` | 200 |
| `GET /api/storage/overview` | — | `{categories:[{category,files,bytes,uploaded_files}], active_bucket_id}` | 200 |
| `POST /api/storage/migrate` | `{categories:string[], bucket_id?}` | `{migration_id}` | 202/409(进行中) |
| `GET /api/storage/migrate/status` | — | `{running, migration?, last?}` | 200 |
| `POST /api/storage/migrate/cancel` | — | `{canceled}` | 200 |
| `POST /api/storage/purge-local` | `{categories:["report-jobs","task-archives"]}` | `{purged, skipped}` | 200/400 |

- `StorageBucket` wire 字段：`id/name/protocol/endpoint/bucket/region/path_prefix/active/status/last_check_at/last_check_error/created_at/updated_at`。**access_key/secret_key 永不回显**（响应只有 has_secret bool）。
- `status` ∈ `ok | degraded | unchecked`。degraded = 最近一次异步上桶失败。
- 迁移 status 的 migration 行：`{id,bucket_id,categories,status,total_files,done_files,failed_files,error,started_at,finished_at}`，status ∈ `running|done|failed|canceled`。

## settings keys

| key | 默认 | 含义 |
| --- | --- | --- |
| `storage_active_id` | ''(空=未启用) | active 桶 id |
| `storage_migrate_cursor` | — | 迁移游标 JSONB(每类别最后完成 rel_path) |

## 通用铁律（同 NOTIFY 契约）

1. raw SQL 字符串内禁止 `//` 注释（只允许 `--`）；SQL 一律参数占位。
2. 返回集合的 Go 接口禁止 nil 漏出（nil slice → `[]`），否则 JSON null 打崩前端。
3. 密文进 TEXT 列一律 secretbox + base64；API 永不回显密钥明文。
4. 列表接口 `total` 必须在过滤之后算；counts 按未过滤条件统计。
5. S3/WebDAV 出网走 `ProxyAddr()/ProxyCACert()` 全局出口，构造 http.Client 显式 `Proxy`（不继承环境变量）。
6. 测试必须显式 `ARTEX_PG_DSN` 指向演示库；构建 `go build ./cmd/artex`；前端 `npx tsc --noEmit`。
7. 提交显式 pathspec；不动矩阵外文件；中文注释。
