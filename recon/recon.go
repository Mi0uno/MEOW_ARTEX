// Package recon 实现信息搜集阶段的确定性收集器流水线。
//
// 设计要点(见设计文档 §4)：
//   - Collector 是最小插件接口:一次 Collect 吃目标输入,吐 RawAsset 列表 + 原始输出落盘;
//   - 流水线 = tasks.pipeline JSON,顺序执行,单收集器失败不中断整体(最终 done_with_errors
//     语义由 server 层根据聚合结果落任务状态);
//   - 所有产出只进暂存区(staged_assets),绝不直连正式资产图。
package recon

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// CollectorKind 标记收集器的执行方式(用于前端徽标与后续权限区分)。
type CollectorKind string

const (
	KindAPI    CollectorKind = "api"    // 调外部 HTTP API(fofa/hunter,M2 接入)
	KindTool   CollectorKind = "tool"   // 调本地安装的命令行工具(subfinder/httpx…)
	KindManual CollectorKind = "manual" // 纯文本导入,无外部依赖
)

// RawAssetType 暂存资产的类型,与 assets 节点类型对齐(M2 采纳时直接映射)。
type RawAssetType string

const (
	TypeRootDomain RawAssetType = "root_domain"
	TypeDomain     RawAssetType = "domain" // 子域名
	TypeIP         RawAssetType = "ip"
	TypeService    RawAssetType = "service"
)

// MaxPipelineCollectors 单任务收集器数量上限,防止配置被灌爆。
const MaxPipelineCollectors = 16

// MaxConfigBytes 单收集器配置体积上限。
const MaxConfigBytes = 1 << 20

// RawAsset 是收集器产出的一条待暂存资产。
type RawAsset struct {
	Type  RawAssetType    `json:"type"`
	Value string          `json:"value"`
	Port  int             `json:"port,omitempty"`
	Extra json.RawMessage `json:"extra,omitempty"`
}

// CompanyTarget 是流水线输入里的一个企业目标(域名来自企业 scope kind=domain)。
type CompanyTarget struct {
	ID      int64    `json:"id"`
	Name    string   `json:"name"`
	Domains []string `json:"domains,omitempty"` // 根域名列表
}

// CollectorInput 是收集器的执行输入。
type CollectorInput struct {
	TaskID    int64
	Companies []CompanyTarget
	// Config 是流水线 JSON 里该收集器节点的 config 对象(原样透传,收集器自行校验)。
	Config map[string]any
	// Secrets 是运行时注入的凭据(来自 external_apis 解密),不落任何配置 JSON。
	Secrets map[string]any
	// RawSink 把收集器原始输出落成 evidence blob,返回 sha256。实现方保证非 nil。
	RawSink func(data []byte) (hash string, err error)
}

// CollectorResult 是收集器的执行产出。
type CollectorResult struct {
	Assets  []RawAsset
	RawHash string         // 原始输出 blob(空=该收集器无原始输出)
	Stats   map[string]any // 进 collector_runs.stats,前端展示
}

// Collector 是信息搜集收集器的最小插件接口。
type Collector interface {
	Key() string
	Title() string
	Kind() CollectorKind
	Collect(ctx context.Context, in CollectorInput) (*CollectorResult, error)
}

// ---- 注册表 ----

var (
	regMu    sync.RWMutex
	registry = map[string]Collector{}
)

// Register 注册一个收集器;key 冲突直接 panic(启动期错误)。
func Register(c Collector) {
	regMu.Lock()
	defer regMu.Unlock()
	if c.Key() == "" {
		panic("recon: collector key is empty")
	}
	if _, dup := registry[c.Key()]; dup {
		panic(fmt.Sprintf("recon: collector %q already registered", c.Key()))
	}
	registry[c.Key()] = c
}

// Get 按键取收集器;nil=未注册。
func Get(key string) Collector {
	regMu.RLock()
	defer regMu.RUnlock()
	return registry[key]
}

// Descriptions 返回全部已注册收集器的元数据(前端流水线编排用),按 key 排序。
func Descriptions() []CollectorDesc {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]CollectorDesc, 0, len(registry))
	for _, c := range registry {
		out = append(out, CollectorDesc{Key: c.Key(), Title: c.Title(), Kind: string(c.Kind())})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// CollectorDesc 是收集器的对外元数据。
type CollectorDesc struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Kind  string `json:"kind"`
}

// ---- 流水线定义与校验 ----

// PipelineStep 是流水线里的一个收集器节点。
type PipelineStep struct {
	Key     string         `json:"key"`
	Enabled *bool          `json:"enabled,omitempty"` // nil=true
	Config  map[string]any `json:"config,omitempty"`
	// Secrets 由 server 层在运行时注入(external_apis 解密),json:"-" 确保永不序列化。
	Secrets map[string]any `json:"-"`
}

// Pipeline 是一次信息搜集任务的完整编排。
type Pipeline struct {
	Collectors []PipelineStep `json:"collectors"`
}

// Enabled reports whether the step runs (nil = 默认开).
func (s PipelineStep) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

// ParsePipeline 校验并归一 tasks.pipeline JSON。要求:
//   - 顶层是对象,collectors 数组非空且 ≤ MaxPipelineCollectors;
//   - 每步 key 已注册、无重复、enabled 节点至少有一个开启;
//   - config 体积受 MaxConfigBytes 约束。
func ParsePipeline(raw json.RawMessage) (*Pipeline, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("流水线配置为空")
	}
	if len(raw) > MaxConfigBytes*MaxPipelineCollectors {
		return nil, fmt.Errorf("流水线配置过大")
	}
	var p Pipeline
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("流水线 JSON 无效: %w", err)
	}
	if len(p.Collectors) == 0 {
		return nil, fmt.Errorf("流水线至少需要一个收集器")
	}
	if len(p.Collectors) > MaxPipelineCollectors {
		return nil, fmt.Errorf("收集器最多 %d 个", MaxPipelineCollectors)
	}
	seen := map[string]bool{}
	enabledCount := 0
	for i, step := range p.Collectors {
		if step.Key == "" {
			return nil, fmt.Errorf("第 %d 个收集器缺少 key", i+1)
		}
		if seen[step.Key] {
			return nil, fmt.Errorf("收集器 %q 重复", step.Key)
		}
		seen[step.Key] = true
		if Get(step.Key) == nil {
			return nil, fmt.Errorf("收集器 %q 未注册", step.Key)
		}
		if step.IsEnabled() {
			enabledCount++
		}
		if len(step.Config) > 0 {
			b, err := json.Marshal(step.Config)
			if err != nil || len(b) > MaxConfigBytes {
				return nil, fmt.Errorf("收集器 %q 配置过大", step.Key)
			}
		}
	}
	if enabledCount == 0 {
		return nil, fmt.Errorf("流水线至少要启用一个收集器")
	}
	return &p, nil
}
