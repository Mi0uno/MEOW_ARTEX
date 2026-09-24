package recon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

// ---- 纯单元:流水线解析 ----

func TestParsePipelineRejectsInvalid(t *testing.T) {
	cases := map[string]json.RawMessage{
		"空配置":   nil,
		"非对象":   json.RawMessage(`[1,2]`),
		"收集器为空": json.RawMessage(`{"collectors":[]}`),
		"未知收集器": json.RawMessage(`{"collectors":[{"key":"nope"}]}`),
		"重复收集器": json.RawMessage(`{"collectors":[{"key":"manual"},{"key":"manual"}]}`),
		"全部禁用":  json.RawMessage(`{"collectors":[{"key":"manual","enabled":false}]}`),
		"缺少key": json.RawMessage(`{"collectors":[{"config":{}}]}`),
	}
	for name, raw := range cases {
		if _, err := ParsePipeline(raw); err == nil {
			t.Errorf("%s: 期望报错,却通过了", name)
		}
	}
}

func TestParsePipelineAcceptsValid(t *testing.T) {
	raw := json.RawMessage(`{"collectors":[{"key":"manual","config":{"text":"a.com"}},{"key":"crtsh","enabled":false}]}`)
	p, err := ParsePipeline(raw)
	if err != nil {
		t.Fatalf("合法流水线被拒: %v", err)
	}
	if len(p.Collectors) != 2 {
		t.Fatalf("应有 2 步,得到 %d", len(p.Collectors))
	}
	if !p.Collectors[0].IsEnabled() {
		t.Error("第一步应默认启用")
	}
	if p.Collectors[1].IsEnabled() {
		t.Error("第二步显式禁用,应为 false")
	}
}

// ---- 纯单元:目标文本解析与域名白名单 ----

func TestClassifyLine(t *testing.T) {
	cases := []struct {
		in   string
		typ  RawAssetType
		val  string
		port int
		ok   bool
	}{
		{"example.com", TypeDomain, "example.com", 0, true},
		{" EXAMPLE.com. ", TypeDomain, "example.com", 0, true},
		{"sub.example.com:8080", TypeDomain, "sub.example.com", 8080, true},
		{"1.2.3.4", TypeIP, "1.2.3.4", 0, true},
		{"1.2.3.4:9999", TypeIP, "1.2.3.4", 9999, true},
		{"https://admin.example.com/login", TypeService, "admin.example.com", 0, true},
		{"# 注释", "", "", 0, false},
		{"", "", "", 0, false},
		{"-flag", "", "", 0, false},
		{"999.1.1.1", "", "", 0, false},
	}
	for _, c := range cases {
		got := classifyLine(c.in)
		if !c.ok {
			if got != nil {
				t.Errorf("%q: 期望被忽略,却得到 %v", c.in, got)
			}
			continue
		}
		if got == nil {
			t.Errorf("%q: 期望解析成功", c.in)
			continue
		}
		if got.Type != c.typ || got.Value != c.val || got.Port != c.port {
			t.Errorf("%q: 得到 %v/%v/%v,期望 %v/%v/%v", c.in, got.Type, got.Value, got.Port, c.typ, c.val, c.port)
		}
	}
}

func TestSanitizeDomain(t *testing.T) {
	for _, bad := range []string{"", "-evil", "a b.com", "onlytld", "!!", "-d"} {
		if _, ok := sanitizeDomain(bad); ok {
			t.Errorf("%q: 应拒绝", bad)
		}
	}
	for _, good := range []string{"example.com", "SUB.Example.COM.", "a-b.example.cn"} {
		if _, ok := sanitizeDomain(good); !ok {
			t.Errorf("%q: 应接受", good)
		}
	}
}

// ---- 端到端:manual 收集器 → Runner → 暂存区(需要本地 PG;无库则跳过) ----

// testDSN 解析测试套件用的 PostgreSQL DSN。测试绝不回退到 config.json——
// 那是运行实例(含用户正库)的配置来源,读它等于把测试的建删数据写进用户正在
// 使用的库(2026-09-24 实测:测试清理删掉了运行中实例的任务行,内存里的
// worker/planner 变成对不存在任务反复重试的「幽灵」,白烧两轮 LLM 调用)。
// 必须显式设置 ARTEX_PG_DSN 指向隔离库(如演示集群 5499)才跑端到端 PG 测试。
func testDSN() (string, error) {
	dsn := strings.TrimSpace(os.Getenv("ARTEX_PG_DSN"))
	if dsn == "" {
		return "", errors.New("ARTEX_PG_DSN 未设置(测试不回退 config.json,避免污染运行实例)")
	}
	return dsn, nil
}

func TestManualPipelineEndToEnd(t *testing.T) {
	dsn, err := testDSN()
	if err != nil {
		t.Skipf("无隔离库配置,跳过端到端: %v", err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Skipf("PG 不可达,跳过端到端: %v", err)
	}
	defer pg.Close()

	// 任务行是暂存区的外键:借真实建任务路径(缺 LLM 配置也能建)。
	task, err := pg.CreateTaskWithOptions("recon-e2e-test", "测试流水线落盘", db.TaskCreateOptions{
		Stage:    "recon",
		Kind:     "pipeline",
		Pipeline: json.RawMessage(`{"collectors":[{"key":"manual","config":{"text":"e2e-a.example.com\n192.0.2.10:8080\nhttps://e2e-b.example.com"}}]}`),
	})
	if err != nil {
		t.Skipf("无法创建测试任务(通常=PG 未就绪): %v", err)
	}
	defer func() { _ = pg.DeleteTask(task.ID) }()

	pipe, err := ParsePipeline(task.Pipeline)
	if err != nil {
		t.Fatalf("流水线解析失败: %v", err)
	}
	sink := func(data []byte) (string, error) {
		// 测试桩:返回伪 hash,不落真实 blob。
		return fakeHash(data), nil
	}
	// 不带企业归属(CompanyID=nil),专注验证流水线→暂存区落盘。
	runner := &Runner{DB: pg, Sink: sink}
	res, err := runner.Run(context.Background(), task.ID, nil, pipe)
	if err != nil {
		t.Fatalf("流水线执行失败: %v", err)
	}
	if res.Done != 1 || res.Failed != 0 || res.Staged != 3 {
		t.Fatalf("聚合结果不符: %+v", res)
	}
	pending, _, _, err := pg.CountStagedAssets(task.ID)
	if err != nil {
		t.Fatalf("计数失败: %v", err)
	}
	if pending != 3 {
		t.Fatalf("暂存区应有 3 条 pending,得到 %d", pending)
	}
	assets, err := pg.ListStagedAssets(task.ID, "pending")
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	if len(assets) != 3 {
		t.Fatalf("应有 3 条,得到 %d", len(assets))
	}
	if assets[0].Value != "https://e2e-b.example.com" && assets[0].Value != "e2e-b.example.com" {
		t.Errorf("service 资产 value 异常: %q", assets[0].Value)
	}
	runs, err := pg.ListCollectorRuns(task.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("collector runs 应有 1 条: %v %d", err, len(runs))
	}
	if runs[0].Status != "done" || runs[0].Stats == nil {
		t.Fatalf("run 状态异常: %+v", runs[0])
	}
}

// fakeHash 生成 64 位十六进制假 hash(仅测试桩用)。
func fakeHash(data []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 64)
	var seed uint32 = uint32(len(data))
	for i := range out {
		seed = seed*31 + uint32(i)
		out[i] = hexdigits[seed%16]
	}
	return string(out)
}
