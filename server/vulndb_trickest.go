package server

// trickest/cve 全网 PoC 聚合源(契约 POC_INTEL_CONTRACTS.md,Agent B 所有)。
//
// 本文件当前是【骨架】。Agent B 按契约 §B 填充实现:
//
//   - syncTrickest:每轮定向拉取(vulndb_sync.go runOnce 第 10 源已预挂)。
//     队列=无 PoC 的条目,优先级 KEV → 高危(critical/high) → 最新;
//     条数上限 settings vulndb_trickest_limit(默认 200)。
//     逐条 GET https://raw.githubusercontent.com/trickest/cve/main/<year>/<CVE>.md
//     (404=该 CVE 无聚合记录,静默跳过);parseTrickestMain 提取链接后:
//     `#### Github` 段 → CreateVulnPoc{Source:"trickest", Kind:"poc"}(cap 20/条目);
//     `#### Reference` 段安全站(packetstorm/exploit-db/seeblog/vulhub 等)→ PoC 行,
//     其余并入条目 refs(MergeVulnRefs)。写 vulndb_sync_runs(source="trickest")。
//   - trickestBackfill:历史回填(全量无 PoC 条目),光标 settings
//     vulndb_trickest_cursor(JSON),5 req/s 礼貌限速,总预算 2h,可取消续跑。
//     状态结构必须与 nuclei 回填共形:PocBackfillStatus(见 vulndb_nuclei_body.go)。
//
// 签名不得更改(syncTrickest 由主会话冻结的 runOnce 调用)。

import "context"

// syncTrickest 跑一轮 trickest 定向聚合,返回新增 PoC 数。
func (v *vulnSync) syncTrickest(ctx context.Context) (int, error) {
	return 0, nil // TODO(Agent B): 按契约 §B.1 实现
}
