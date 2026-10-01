---
name: test-db-isolation
description: 测试套件曾默认连生产库造成数据损失（2026-09-24 已修）；跑测试必须显式 ARTEX_PG_DSN
globs:
  - "D:/AI/MEOW_ARTEX/**"
---

# 测试库隔离（2026-09-24 事故与修复）

**事故**：`db`/`server`/`agent`/`recon` 四个测试包的 `TestMain` 经 `db.DSN()` → `config.PostgresDSN()` 解析 DSN，优先级是「env `ARTEX_PG_DSN` > **config.json**」。未设环境变量时会**回退读仓库根 config.json，直接连上用户正在使用的 5432 正库**。一次 `go test` 的建删任务污染了同时运行的 8787 实例：任务行被删掉，但实例内存里的 worker/planner 仍持有该任务句柄 → 对不存在的任务反复重试、反复规划，日志刷 `task N not found`，每个残留意图白烧两轮 LLM 调用（3s 退避 ×2）。

**修复**：四个包一律**只认显式 `ARTEX_PG_DSN`**，未设置即跳过，绝不回退 config.json（对齐 `evidence`/`llmrec` 既有的正确约定）。`server` 包额外用 `ARTEX_CONFIG` 指向不存在文件做屏蔽——因为 40+ 处测试经生产 `NewManager` 间接建连（`server/manager.go` 内部自己调 `pgdb.DSN()`），光改 `testDSN()` 堵不住。

**跑测试的正确姿势**（5499 演示集群）：
```
ARTEX_PG_DSN="postgres://postgres@127.0.0.1:5499/postgres?sslmode=disable" GOSUMDB="sum.golang.org" go test ./server/ ./db/ -count=1
```
不带 `ARTEX_PG_DSN` 跑是安全的（会跳过），但**不要**为了「让测试跑起来」而设置它指向 5432。

**踩坑**：5499 库里的 `auth.jwt_ver` 若残留非 0 值（改过密码的演示库会），测试签的 `ver=0` token 全部 401，表现为大面积 `create task: 401 (token 无效或已过期)` —— 那不是代码回归，是隔离库脏数据，`delete from settings where key='auth.jwt_ver'` 即可。同理 `out/pgdemo-cluster/postmaster.pid` 残留会让 `pg_ctl start` 失败，先删 pid 文件。

相关：[[project-artex]]
