package server

// 任务侧 CVE 情报 API(契约 POC_INTEL_CONTRACTS.md §10,Agent 1 所有)。
//
//	registerVulnTaskRoutes(mux, s) 已在 server.go 挂载(主会话冻结,签名不得改)。
//	GET /api/tasks/{id}/vuln-brief → 200 {"brief": vulnBriefWire}
//	  vulnBriefWire 与 web/src/lib/types.ts 的 VulnBriefDTO 对齐:
//	  {task_id, enabled, generated_at, fingerprints:[{term,hits}], counts, entries}。
//	  fingerprints=前 24 指纹词的逐词命中计数(按 hits 降序);counts=kev/high/rest/total
//	  三分类计数;entries=兼容保留的全量命中(上限 30),分页列表走 /vuln-brief/entries。
//	  enabled=false → fingerprints=[]/counts 全 0/entries=[]。
//	  任务不存在 404;库错误降级为 enabled=false + 空数据(不 500,简报是锦上添花)。
//	GET /api/tasks/{id}/vuln-brief/entries?scope=&severity=&q=&sort=&desc=&limit=&offset=
//	  → 200 {"entries":[VulnEntry],"total":N}(契约 §10.2 逐字;简报对话框「匹配漏洞」页签)。

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

// vulnBriefWire GET /api/tasks/{id}/vuln-brief 响应体里的 brief 对象
// (wire 字段名与 web/src/lib/types.ts VulnBriefDTO 逐一对齐)。
type vulnBriefWire struct {
	TaskID       string               `json:"task_id"`
	Enabled      bool                 `json:"enabled"`
	GeneratedAt  string               `json:"generated_at"`
	Fingerprints []vulnFingerprintHit `json:"fingerprints"`
	Counts       vulnBriefCounts      `json:"counts"`
	Entries      []*db.VulnEntry      `json:"entries"`
}

// registerVulnTaskRoutes 挂载任务↔漏洞库联动的路由。
func registerVulnTaskRoutes(mux *http.ServeMux, s *Server) {
	mux.HandleFunc("GET /api/tasks/{id}/vuln-brief", s.taskVulnBrief)
	mux.HandleFunc("GET /api/tasks/{id}/vuln-brief/entries", s.taskVulnBriefEntries)
}

// vulnBriefResultDegraded 库错误时的降级产物:enabled=false + 全空数据(wire 序列化
// 出 [],不是 null)。与「联动关闭」的 wire 形状一致。
func vulnBriefResultDegraded() *vulnBriefResult {
	return &vulnBriefResult{generatedAt: time.Now(), fingerprints: []string{},
		fpHits: []vulnFingerprintHit{}, entries: []*db.VulnEntry{}}
}

// taskVulnBrief GET /api/tasks/{id}/vuln-brief — 任务 CVE 情报简报。
// 数据与 agent 注入的简报同源同缓存(vulnBriefingFor),保证「界面看到的=AI 拿到的」。
func (s *Server) taskVulnBrief(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	num, err := parseTaskID(id)
	if err != nil {
		writeErr(w, 404, "任务不存在")
		return
	}
	// 存在性:运行时句柄优先,回落库行(与 memoryBriefingHandler 同一模式)。
	if _, ok := s.m.Task(id); !ok {
		if pt, gerr := s.m.PG().GetTask(num); gerr != nil || pt == nil {
			writeErr(w, 404, "任务不存在")
			return
		}
	}
	res, err := s.vulnBriefingFor(num)
	if err != nil {
		if errors.Is(err, ErrTaskGone) {
			writeErr(w, 404, "任务不存在")
			return
		}
		// 库错误降级:简报是锦上添花的情报卡,不能让任务详情页 500。
		log.Printf("[vuln-brief] 任务 %s 简报接口降级(库错误): %v", id, err)
		res = vulnBriefResultDegraded()
	}
	writeJSON(w, 200, map[string]any{"brief": vulnBriefWire{
		TaskID:       id,
		Enabled:      res.enabled,
		GeneratedAt:  res.generatedAt.Format(time.RFC3339),
		Fingerprints: res.fpHits,
		Counts:       res.counts,
		Entries:      res.entries,
	}})
}

// vulnBriefEntriesQuery GET /vuln-brief/entries 的解析产物(契约 §10.2 参数逐字)。
type vulnBriefEntriesQuery struct {
	Scope    string // ""=all; kev|high|rest(语义同 counts)
	Severity string // critical|high|medium|low(""=不限)
	Q        string // 标题/厂商/产品/CVE编号 ILIKE
	Sort     string // severity(缺省)|poc|updated
	Desc     bool   // 缺省 true("0"=升序)
	Limit    int    // 夹取 1-100,缺省 20
	Offset   int    // ≥0
}

// parseVulnBriefEntriesQuery 解析并校验分页查询参数(契约 §10.2 逐字):
// scope=all(缺省)|kev|high|rest;severity=critical|high|medium|low;q 自由文本;
// sort=severity(缺省)|poc|updated;desc 缺省 1,仅 "0" 算升序(其余值宽容按缺省);
// limit 夹取 1-100 缺省 20(非数字按缺省);offset≥0(负数归 0)。
// scope/severity/sort 白名单外返回错误(handler 映射 400)。
func parseVulnBriefEntriesQuery(q url.Values) (vulnBriefEntriesQuery, error) {
	out := vulnBriefEntriesQuery{Sort: "severity", Desc: true, Limit: 20}
	switch v := q.Get("scope"); v {
	case "", "all":
	case "kev", "high", "rest":
		out.Scope = v
	default:
		return out, fmt.Errorf("scope 非法: %q", v)
	}
	switch v := q.Get("severity"); v {
	case "":
	case "critical", "high", "medium", "low":
		out.Severity = v
	default:
		return out, fmt.Errorf("severity 非法: %q", v)
	}
	switch v := q.Get("sort"); v {
	case "":
	case "severity", "poc", "updated":
		out.Sort = v
	default:
		return out, fmt.Errorf("sort 非法: %q", v)
	}
	out.Q = strings.TrimSpace(q.Get("q"))
	if q.Get("desc") == "0" {
		out.Desc = false
	}
	if v, err := strconv.Atoi(strings.TrimSpace(q.Get("limit"))); err == nil {
		out.Limit = max(1, min(v, 100))
	}
	if v, err := strconv.Atoi(strings.TrimSpace(q.Get("offset"))); err == nil && v > 0 {
		out.Offset = v
	}
	return out, nil
}

// taskVulnBriefEntries GET /api/tasks/{id}/vuln-brief/entries — 简报「匹配漏洞」
// 分页列表(契约 §10.2)。terms 与简报同源(vulnBriefingFor 的 per-task 10min 缓存,
// miss 则重算并写缓存);联动关闭/无指纹/构建降级 → 200 空集(不打库)。
func (s *Server) taskVulnBriefEntries(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	num, err := parseTaskID(id)
	if err != nil {
		writeErr(w, 404, "任务不存在")
		return
	}
	if _, ok := s.m.Task(id); !ok {
		if pt, gerr := s.m.PG().GetTask(num); gerr != nil || pt == nil {
			writeErr(w, 404, "任务不存在")
			return
		}
	}
	q, perr := parseVulnBriefEntriesQuery(r.URL.Query())
	if perr != nil {
		writeErr(w, 400, perr.Error())
		return
	}
	terms := []string{}
	if res, berr := s.vulnBriefingFor(num); berr != nil {
		if errors.Is(berr, ErrTaskGone) {
			writeErr(w, 404, "任务不存在")
			return
		}
		// 与简报主接口同一降级哲学:200 空集 + 日志,不让对话框把详情页拖崩。
		log.Printf("[vuln-brief] 任务 %s 匹配列表降级(简报构建失败): %v", id, berr)
	} else if res.enabled {
		terms = res.fingerprints
	}
	entries, total := []*db.VulnEntry{}, 0
	if len(terms) > 0 {
		var qerr error
		entries, total, qerr = s.m.PG().MatchVulnEntriesForFingerprintsPage(db.VulnMatchFilter{
			Terms:    terms,
			Scope:    q.Scope,
			Severity: q.Severity,
			Q:        q.Q,
			Sort:     q.Sort,
			Desc:     q.Desc,
			Limit:    q.Limit,
			Offset:   q.Offset,
		})
		if qerr != nil {
			log.Printf("[vuln-brief] 任务 %s 匹配列表查询失败(降级空集): %v", id, qerr)
			entries, total = []*db.VulnEntry{}, 0
		}
	}
	writeJSON(w, 200, map[string]any{"entries": entries, "total": total})
}
