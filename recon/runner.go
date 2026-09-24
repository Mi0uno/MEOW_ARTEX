package recon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/Autumn-27/artex/db"
)

// RawSink 把字节流落成 evidence blob 并返回 sha256(由 server 层注入
// evidence.Store.PutBlob,本包不依赖 evidence)。
type RawSink func(data []byte) (string, error)

// Runner 执行一条信息搜集流水线。
type Runner struct {
	DB   *db.DB
	Sink RawSink
}

// RunResult 是整条流水线的聚合结果。Failed>0 时 server 层将任务落为
// done(部分成功语义由前端按 failed 数展示),全部收集器失败才落 failed。
type RunResult struct {
	Total   int // 启用的收集器数
	Done    int
	Failed  int
	Staged  int // 本次新入库(含刷新)的暂存资产数
	BatchID int64
}

// Run 顺序执行流水线。约束(设计文档 §4):
//   - 每个收集器一次执行先建 task_collector_runs 行,失败不中断后续收集器;
//   - 全部产出进 staged_assets,永不直连资产图;
//   - 产出逐条落库,单条失败仅计数、不断流。
func (r *Runner) Run(ctx context.Context, taskID int64, companies []CompanyTarget, pipe *Pipeline) (*RunResult, error) {
	if r.DB == nil || r.Sink == nil {
		return nil, fmt.Errorf("recon runner 未初始化")
	}
	batchID, err := r.DB.MaxStagedAssetBatchID(taskID)
	if err != nil {
		return nil, err
	}
	batchID++ // 每次执行一个新批次,供 diff/采纳分组
	out := &RunResult{Total: 0, BatchID: batchID}
	for i, step := range pipe.Collectors {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if !step.IsEnabled() {
			continue
		}
		out.Total++
		collector := Get(step.Key)
		if collector == nil {
			log.Printf("[recon] task %d: 收集器 %q 未注册,跳过", taskID, step.Key)
			out.Failed++
			continue
		}
		runID, err := r.DB.CreateCollectorRun(taskID, collector.Key(), i)
		if err != nil {
			return out, err
		}
		log.Printf("[recon] task %d: 收集器 %s (#%d run %d) 开始", taskID, collector.Key(), i, runID)
		result, err := collector.Collect(ctx, CollectorInput{
			TaskID:    taskID,
			Companies: companies,
			Config:    step.Config,
			Secrets:   step.Secrets,
			RawSink:   r.Sink,
		})
		if err != nil {
			out.Failed++
			_ = r.DB.FinishCollectorRun(runID, "failed", mustJSON(map[string]any{}), err.Error(), "")
			log.Printf("[recon] task %d: 收集器 %s 失败: %v", taskID, collector.Key(), err)
			continue
		}
		// 产出落暂存区;单条失败仅计数。
		staged := 0
		insertErrs := 0
		for _, a := range result.Assets {
			sa := &db.StagedAsset{
				TaskID:    taskID,
				RunID:     &runID,
				Collector: collector.Key(),
				Type:      string(a.Type),
				Value:     a.Value,
				Port:      a.Port,
				Extra:     a.Extra,
				BatchID:   batchID,
			}
			// 企业归属:取第一个值落在其根域名/或声明的企业(多企业时逐个匹配;M1 简化=全部挂第一个企业)。
			// ID=0 的合成目标(如无企业上下文的测试)不带归属。
			if len(companies) > 0 && companies[0].ID > 0 {
				id := companies[0].ID
				sa.CompanyID = &id
			}
			ok, err := r.DB.InsertStagedAsset(sa)
			if err != nil {
				insertErrs++
				continue
			}
			if ok {
				staged++
			}
		}
		out.Done++
		out.Staged += staged
		stats := map[string]any{}
		for k, v := range result.Stats {
			stats[k] = v
		}
		stats["staged"] = staged
		if insertErrs > 0 {
			stats["insert_errors"] = insertErrs
		}
		_ = r.DB.FinishCollectorRun(runID, "done", mustJSON(stats), "", result.RawHash)
		log.Printf("[recon] task %d: 收集器 %s 完成: 产出 %d, 新暂存 %d", taskID, collector.Key(), len(result.Assets), staged)
	}
	return out, nil
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}
