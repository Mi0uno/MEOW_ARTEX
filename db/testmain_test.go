package db

import (
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// testDSNFromEnv 解析测试套件用的 PostgreSQL DSN，只认显式环境变量。测试绝不
// 回退到 config.json——那是运行实例（含用户正库）的配置来源，读它等于把测试的
// 建删数据写进用户正在使用的库（2026-09-24 实测：测试清理删掉了运行中实例的
// 任务行，内存里的 worker/planner 变成对不存在任务反复重试的「幽灵」，白烧两轮
// LLM 调用）。必须显式设置 ARTEX_PG_DSN 指向隔离库（如演示集群 5499）才跑 PG 测试。
//
// 命名说明：本包已存在 testDSN(t)（跳过语义的便捷包装，db_test.go），故此处用
// testDSNFromEnv 作为底层解析器，避免与既有的 testDSN 重名。
func testDSNFromEnv() (string, error) {
	dsn := strings.TrimSpace(os.Getenv("ARTEX_PG_DSN"))
	if dsn == "" {
		return "", errors.New("ARTEX_PG_DSN 未设置（测试不回退 config.json，避免污染运行实例）")
	}
	return dsn, nil
}

// TestMain acquires a PostgreSQL advisory lock (7337741002) for the entire
// db test suite. Packages agent and server hold the same lock, so parallel
// `go test ./...` runs serialize across packages on the shared dev DB and
// avoid cross-package cleanup races (e.g. DELETE FROM assets WHERE id > X
// from one package deleting assets created by another).
//
// 门禁：只认显式设置的 ARTEX_PG_DSN；未设置时完全不建立任何数据库连接，
// 需要 PG 的测试各自跳过。绝不回退 config.json（生产实例的配置来源）。
func TestMain(m *testing.M) {
	dsn, err := testDSNFromEnv()
	if err != nil {
		// 未显式指定隔离库——不连任何库，PG 测试会各自跳过。
		os.Exit(m.Run())
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
