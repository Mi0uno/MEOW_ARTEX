package server

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// testDSN 解析测试套件用的 PostgreSQL DSN。测试绝不回退到 config.json——
// 那是运行实例（含用户正库）的配置来源，读它等于把测试的建删数据写进用户正在
// 使用的库（2026-09-24 实测：测试清理删掉了运行中实例的任务行，内存里的
// worker/planner 变成对不存在任务反复重试的「幽灵」，白烧两轮 LLM 调用）。
// 必须显式设置 ARTEX_PG_DSN 指向隔离库（如演示集群 5499）才跑 PG 测试。
func testDSN() (string, error) {
	dsn := strings.TrimSpace(os.Getenv("ARTEX_PG_DSN"))
	if dsn == "" {
		return "", errors.New("ARTEX_PG_DSN 未设置（测试不回退 config.json，避免污染运行实例）")
	}
	return dsn, nil
}

// blockConfigFallback 把 ARTEX_CONFIG 指向一个不存在的文件，使生产 config 回退
// 必然失败（Load 读不到文件 → PostgresDSN 报「未找到数据库配置」），返回清理函数。
//
// 本包需要这层额外保险：生产代码 server.NewManager 内部自己调用 pgdb.DSN()
// （优先级 env > config.json），而 40+ 个测试经 NewManager 间接建连（如
// finding_traffic_test.go 的 trafficEvidenceServer），并不显式取 DSN。若进程
// 工作目录恰好是仓库根（IDE 的「运行包测试」常这样），config.json 就解析到用户
// 正库配置，这些测试会绕过 testDSN 直接连上生产库并建删数据——2026-09-24 事故的
// 同一类路径。屏蔽后未设 ARTEX_PG_DSN 时 NewManager 必然报错，测试按既有语义跳过。
func blockConfigFallback() func() {
	dir, err := os.MkdirTemp("", "artex-test-noconfig")
	if err != nil {
		return func() {}
	}
	os.Setenv("ARTEX_CONFIG", filepath.Join(dir, "config.json")) //nolint:errcheck
	return func() { _ = os.RemoveAll(dir) }
}

// TestMain acquires a PostgreSQL advisory lock (7337741002) for the entire
// server test suite so cross-package DELETE cleanup races with db/agent
// packages are avoided when running `go test ./...`.
//
// 门禁：只认显式设置的 ARTEX_PG_DSN；未设置时完全不建立任何数据库连接，
// 需要 PG 的测试各自跳过。绝不回退 config.json（生产实例的配置来源）。
func TestMain(m *testing.M) {
	dsn, err := testDSN()
	if err != nil {
		// 未显式指定隔离库——不连任何库，并屏蔽 config.json 回退（见上）。
		cleanup := blockConfigFallback()
		code := m.Run()
		cleanup()
		os.Exit(code)
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil || conn.Ping() != nil {
		os.Exit(m.Run())
	}
	defer conn.Close()
	if _, err := conn.Exec(`SELECT pg_advisory_lock(7337741002)`); err != nil {
		os.Exit(m.Run())
	}
	defer conn.Exec(`SELECT pg_advisory_unlock(7337741002)`) //nolint:errcheck
	os.Exit(m.Run())
}
