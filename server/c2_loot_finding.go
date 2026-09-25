package server

// loot→finding 转发(契约 C2_CONTRACTS.md §15):把一条 C2 战利品提升为平台
// 正式漏洞发现(finding),走「转发现」按钮的单一入口。
//
// 复用纪律:finding 创建走 db.Exploration(...).RecordFinding —— 与 report_finding
// 工具(agent/tools.go)完全同一条原子路径(探索图 finding 节点 + findings 行 +
// intent yields 边);创建成功后回调 agent.OnFindingRecorded(wireFindingHooks 在
// Server 构造时注册的钩子链),漏洞库自动归类与 SRC 成果自动创建因此原样生效,
// 不自建平行路径。
//
// id 体系(已核实钉死):RecordedFinding.FindingID 是 findings 表 BIGSERIAL 主键,
// 即发现详情页 GET /api/exploration/findings/{id}(→ pg.GetFinding:WHERE f.id=$1)
// 直接消费的那套 id —— to-finding 的返回值与 c2_loot.finding_id 回填值都用它,
// 前端拿 finding_id 拼 /function/findings/detail?id=<id> 即可直达。
//
// 密文纪律:credential/hash 类正文不落明文 —— 明文只存在 c2_loot.content 密文列,
// finding 正文只写「已加密保管」的脱敏提示;file/other 类原文截断后入正文。

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/agent"
	pgdb "github.com/Autumn-27/artex/db"
)

// c2LootEvidenceLimit file/other 类战利品正文进 finding 的截断上限(run rune)。
const c2LootEvidenceLimit = 2000

// c2LootVulnClass 战利品类型 → 漏洞类别(finding.vulnclass)。
func c2LootVulnClass(kind string) string {
	switch kind {
	case pgdb.C2LootKindCredential:
		return "凭据获取"
	case pgdb.C2LootKindHash:
		return "凭据哈希获取"
	case pgdb.C2LootKindFile:
		return "敏感文件收集"
	case pgdb.C2LootKindScreenshot:
		return "屏幕捕获"
	default:
		return "后渗透收获"
	}
}

// c2LootSeverity 战利品类型 → 建议严重度(凭据/哈希=high,文件=medium,其余=low)。
func c2LootSeverity(kind string) string {
	switch kind {
	case pgdb.C2LootKindCredential, pgdb.C2LootKindHash:
		return "high"
	case pgdb.C2LootKindFile:
		return "medium"
	default:
		return "low"
	}
}

// c2LootToFinding POST /api/c2/loot/{id}/to-finding —
// 会话必须已绑定任务(400「绑定」)→ 复用 RecordFinding 登记(钩子链自动生效)→
// 条件回填 c2_loot.finding_id(重复转换 409「转换/重复」)→ 200 {finding_id}。
func (s *Server) c2LootToFinding(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	pg := s.m.PG()
	lo, err := pg.GetC2Loot(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if lo == nil {
		writeErr(w, 404, "战利品不存在")
		return
	}
	// 重复转换:finding_id 已非空 → 409(文案含「转换/重复」,前端按关键词归一提示)。
	if lo.FindingID > 0 {
		writeErr(w, 409, fmt.Sprintf("该战利品已转换为发现(#%d),不可重复转换", lo.FindingID))
		return
	}
	// 会话必须已绑定平台任务(400 文案含「绑定」,前端提示先去会话页绑定)。
	m, err := pg.GetC2Session(lo.SessionID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if m == nil {
		writeErr(w, 400, "战利品来源会话不存在,请先在会话页确认并绑定任务")
		return
	}
	if m.TaskID <= 0 {
		writeErr(w, 400, "该会话未绑定任务,请先在会话页绑定任务后再转发现")
		return
	}
	// 任务必须在内存(ResolveTask 会按需从库加载);ExpID 是 RecordFinding 的锚。
	t := s.m.ResolveTask(strconv.FormatInt(m.TaskID, 10))
	if t == nil {
		writeErr(w, 400, fmt.Sprintf("绑定的平台任务 #%d 不存在或未加载,请先在会话页重新绑定任务", m.TaskID))
		return
	}

	// 组正文:类型 + 内容(credential/hash 脱敏提示,不落明文)+ 来源路径 + 会话信息。
	contentNote := "内容已加密保管于战利品库(战利品 #" + strconv.FormatInt(lo.ID, 10) +
		"),此处不落明文;请到 C2 战利品页查看解密详情。"
	if !pgdb.LootContentEncrypted(lo.Kind) && strings.TrimSpace(lo.Content) != "" {
		contentNote = "内容:" + truncateRunes(strings.TrimSpace(lo.Content), c2LootEvidenceLimit)
	} else if strings.TrimSpace(lo.Content) == "" {
		contentNote = "无正文内容(见名称与来源路径)。"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "类型: %s(%s)\n", c2LootVulnClass(lo.Kind), lo.Kind)
	b.WriteString(contentNote + "\n")
	if sp := strings.TrimSpace(lo.SourcePath); sp != "" {
		fmt.Fprintf(&b, "来源路径: %s\n", sp)
	}
	fmt.Fprintf(&b, "来源会话: #%d %s/%s(监听器 #%d,系统 %s/%s)",
		m.ID, m.Hostname, m.Username, m.ListenerID, m.OS, m.Arch)

	summary := strings.TrimSpace(lo.Name)
	if summary == "" {
		summary = c2LootVulnClass(lo.Kind)
	}
	summary = fmt.Sprintf("%s:后渗透阶段自会话 #%d(%s)收获,已由 C2 战利品转发", summary, m.ID, m.Hostname)

	// 复用既有登记路径(与 report_finding 工具同一条 RecordFinding 原子写:
	// 探索图 finding 节点 + findings 行 + intent yields 边)。
	recorded, err := pg.Exploration(t.ExpID).RecordFinding(r.Context(), pgdb.RecordFindingInput{
		TaskID:        m.TaskID,
		ExplorationID: t.ExpID,
		VulnClass:     c2LootVulnClass(lo.Kind),
		Name:          strings.TrimSpace(lo.Name),
		Severity:      c2LootSeverity(lo.Kind),
		Summary:       summary,
		Evidence:      b.String(),
		Worker:        "c2_loot",
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			writeErr(w, 499, "请求已取消")
			return
		}
		writeErr(w, 500, "登记发现失败: "+err.Error())
		return
	}
	if recorded == nil || recorded.FindingID <= 0 {
		writeErr(w, 500, "登记发现失败:未返回 finding id")
		return
	}
	// 条件回填 c2_loot.finding_id(finding_id IS NULL 才写):并发双击/重复请求在
	// 这里决出唯一赢家,输家 409(已建的 finding 属于赢家路径之外的孤儿,量级为
	// 极端并发,保留不删——删 findings 会牵连漏洞库/SRC 关联)。
	won, err := pg.SetC2LootFinding(lo.ID, recorded.FindingID)
	if err != nil {
		writeErr(w, 500, "回填战利品 finding_id 失败: "+err.Error())
		return
	}
	if !won {
		writeErr(w, 409, fmt.Sprintf("该战利品已转换为发现(#%d),不可重复转换", recorded.FindingID))
		return
	}
	// wireFindingHooks 注册的联动(漏洞库自动归类/SRC 成果自动创建)原样生效:
	// 与 report_finding 工具(agent/tools.go)同一回调,钩子内部各自异步。
	if agent.OnFindingRecorded != nil {
		agent.OnFindingRecorded(recorded.FindingID, m.TaskID)
	}
	log.Printf("[c2-evasion] 战利品 #%d(%s) 已转换为发现 #%d(会话 #%d,任务 #%d)",
		lo.ID, lo.Name, recorded.FindingID, m.ID, m.TaskID)
	writeJSON(w, 200, map[string]any{
		"finding_id":      recorded.FindingID,
		"finding_node_id": recorded.NodeID,
	})
}
