package server

// trickest/cve 全网 PoC 聚合源(契约 POC_INTEL_CONTRACTS.md §5,Agent B 所有)。
//
//   - syncTrickest:每轮定向拉取(vulndb_sync.go runOnce 第 10 源已预挂)。
//     队列=无 PoC 条目的三优先级合并(KEV → critical → high → 最新),
//     条数上限 settings vulndb_trickest_limit(0/缺省=200)。
//     逐条 GET https://raw.githubusercontent.com/trickest/cve/main/<年>/<CVE>.md
//     (404=该 CVE 无聚合记录,静默跳过;单条 10s 预算、整轮 8min)。
//     parseTrickestMain 提取后:`#### Github` 段 cap 20/条目 + `#### Reference`
//     段安全站 cap 5 → CreateVulnPoc{Source:"trickest", Kind:"poc"},Content=链接
//     行文本;其余引用 cap 10 → MergeVulnRefs。写 vulndb_sync_runs(source="trickest")。
//   - trickestBackfill:历史回填(全量 poc_count=0 AND cve_id<>''),按
//     vulndb_entries.id 升序推进,光标 settings vulndb_trickest_cursor(JSON),
//     共享 raw 限速器(200ms/条 ≈5 req/s)、总预算 2h、可取消续跑。状态结构与
//     nuclei 回填共形:PocBackfillStatus(见 vulndb_nuclei_body.go)。
//
// 签名不得更改(syncTrickest 由主会话冻结的 runOnce 调用)。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
)

const (
	// trickestRawBase trickest/cve 聚合仓库的 raw 文件根(<年>/<CVE-ID>.md)。
	trickestRawBase = "https://raw.githubusercontent.com/trickest/cve/main"
	// trickestSyncBudget syncTrickest 整轮预算。
	trickestSyncBudget = 8 * time.Minute
	// trickestPerEntryTimeout 单个 CVE 聚合页的抓取预算。
	trickestPerEntryTimeout = 10 * time.Second
	// trickestDefaultLimit vulndb_trickest_limit 缺省/非法时的每轮条数。
	trickestDefaultLimit = 200
	// trickestMaxLimit vulndb_trickest_limit 的防呆上限(ListVulnEntries 单页
	// 上限 200,四条腿各一页,总量超出部分自动被截断)。
	trickestMaxLimit = 800
	// 每条目挂载上限(契约 §5.1)。
	trickestGithubCapPerEntry = 20
	trickestSecureRefCap      = 5
	trickestRefsCap           = 10

	// settingVulnTrickestCursor 历史回填光标的 settings 键(JSON:done/last_id)。
	settingVulnTrickestCursor = "vulndb_trickest_cursor"
	// trickestBackfillBudget 历史回填总预算。
	trickestBackfillBudget = 2 * time.Hour
	// trickestBackfillPage 回填每批扫描的条目数(批末推进光标)。
	trickestBackfillPage = 100
)

// ---- 共享队列:无 PoC 条目的三优先级采样(契约 §5.1/§5.4 同款) ----

// vulnPocHuntQueue 按「KEV 无 PoC → critical 无 PoC → high 无 PoC → 最新无 PoC」
// 四条腿合并去重,产出至多 want 条待猎取 PoC 的 cve 条目。syncTrickest 与
// syncGitHubPoCs 共用,保证两个源把配额花在同一批高价值条目上。单腿一次
// ListVulnEntries(上限 200/页):限额超 200 时靠多腿覆盖,不再翻页。
func (v *vulnSync) vulnPocHuntQueue(want int) []*db.VulnEntry {
	if want <= 0 {
		return nil
	}
	legs := []db.VulnEntryFilter{
		{Category: db.VulnCategoryCVE, Kev: "1", HasPoc: "0"},
		{Category: db.VulnCategoryCVE, Severity: "critical", HasPoc: "0"},
		{Category: db.VulnCategoryCVE, Severity: "high", HasPoc: "0"},
		// created 腿必须显式 Desc=true:ListVulnEntries 里 Sort 非空且 Desc=false
		// 被解释为「显式升序」,零值会拿到最老的条目。
		{Category: db.VulnCategoryCVE, HasPoc: "0", Sort: "created", Desc: true},
	}
	seen := make(map[int64]bool, want)
	out := make([]*db.VulnEntry, 0, want)
	for _, leg := range legs {
		if len(out) >= want {
			break
		}
		leg.Limit = want - len(out)
		page, _, _, err := v.s.m.PG().ListVulnEntries(leg)
		if err != nil {
			log.Printf("[vulndb-sync] PoC 队列取数失败(severity=%s kev=%s): %v", leg.Severity, leg.Kev, err)
			continue
		}
		for _, e := range page {
			if len(out) >= want {
				break
			}
			if e == nil || seen[e.ID] || strings.TrimSpace(e.CVEID) == "" {
				continue
			}
			seen[e.ID] = true
			out = append(out, e)
		}
	}
	return out
}

// trickestLimitSetting 读 settings vulndb_trickest_limit(0/缺省/非法=200)。
func trickestLimitSetting(pg *db.DB) int {
	n := trickestDefaultLimit
	if v, _, _ := pg.GetSetting(settingVulnTrickestLimit); v != "" {
		if x, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && x > 0 {
			n = x
		}
	}
	if n > trickestMaxLimit {
		n = trickestMaxLimit
	}
	return n
}

// vulnPocHuntGHLimitSetting 读 settings vulndb_poc_hunt_github_limit(0/缺省=120)。
func vulnPocHuntGHLimitSetting(pg *db.DB) int {
	n := 120
	if v, _, _ := pg.GetSetting(settingVulnPocHuntGHLimit); v != "" {
		if x, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && x > 0 {
			n = x
		}
	}
	if n > trickestMaxLimit {
		n = trickestMaxLimit
	}
	return n
}

// ---- 聚合页解析 ----

var (
	// trickestCVEPattern 校验 CVE 编号并抽出年份(聚合页按 <年>/<CVE>.md 存放)。
	trickestCVEPattern = regexp.MustCompile(`^CVE-(\d{4})-\d{4,7}$`)
	// trickestURLPattern 从 bullet 行抽第一个 URL(截掉右括号/尖括号等markdown边界)。
	trickestURLPattern = regexp.MustCompile(`https?://[^\s\)\]>]+`)
	// trickestSecureDomains Reference 段里视为「安全站 PoC」的域名(含子域)。
	trickestSecureDomains = []string{
		"packetstormsecurity.com", "exploit-db.com", "seebug.org",
		"vulhub.org", "hackerone.com", "gitlab.com", "gitee.com",
	}
)

// trickestParsed parseTrickestMain 的解析结果(三个列表各自去重、互不重叠)。
type trickestParsed struct {
	Github     []string // #### Github 段的链接(PoC 仓库)
	RefSecures []string // #### Reference 段中命中安全站域名的链接
	Refs       []string // #### Reference 段的其余链接(纯引用)
}

// trickestIsSecureRef 判断引用链接是否命中安全站域名(精确域或子域,防止
// evil-packetstormsecurity.com 这类伪造后缀误判)。
func trickestIsSecureRef(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false
	}
	for _, d := range trickestSecureDomains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// parseTrickestMain 解析 trickest/cve 聚合页 Markdown。结构(2026-09 实测):
//
//	### [CVE-xxxx-xxxxx](https://cve.mitre.org/...)
//	### Description
//	### POC            ← 可缺失,正文忽略
//	#### Reference     ← `- <url>` 列表
//	#### Github        ← `- <url>` 列表
//
// 只认 #### Github / #### Reference 段内的 bullet 行;任何标题行(## 开头)都会
// 结束当前段;重复 URL 去重、保持出现顺序。段内非链接行丢弃。
func parseTrickestMain(md string) trickestParsed {
	var out trickestParsed
	section := ""
	seen := map[string]bool{}
	add := func(bucket *[]string, u string) {
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		*bucket = append(*bucket, u)
	}
	for _, line := range strings.Split(md, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") {
			// 标题行:切换/结束当前段(### POC、### Description、#### Github 等)。
			section = ""
			if h := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(t, "#"))); strings.HasPrefix(h, "###") {
				h = strings.TrimSpace(strings.TrimPrefix(h, "###"))
				switch h {
				case "reference":
					section = "ref"
				case "github":
					section = "github"
				}
			}
			continue
		}
		if section == "" || !strings.HasPrefix(t, "- ") {
			continue
		}
		u := trickestURLPattern.FindString(t)
		if u == "" {
			continue
		}
		switch section {
		case "github":
			add(&out.Github, u)
		case "ref":
			if trickestIsSecureRef(u) {
				add(&out.RefSecures, u)
			} else {
				add(&out.Refs, u)
			}
		}
	}
	return out
}

// trickestGithubSlug 从仓库链接取 owner/repo 展示串(非 github 链接原样返回)。
func trickestGithubSlug(u string) string {
	if s := strings.TrimPrefix(u, "https://github.com/"); s != u {
		return s
	}
	return u
}

// ---- syncTrickest(runOnce 第 10 源) ----

// syncTrickest 跑一轮 trickest 定向聚合,返回新增 PoC 数。
func (v *vulnSync) syncTrickest(ctx context.Context) (pocs int, err error) {
	runID, err := v.s.m.PG().StartVulnSyncRun("trickest")
	if err != nil {
		return 0, err
	}
	defer func() {
		status := "done"
		errMsg := ""
		if err != nil {
			status, errMsg = "failed", err.Error()
		}
		_ = v.s.m.PG().FinishVulnSyncRun(runID, status, 0, 0, 0, pocs, errMsg)
	}()

	ctx, cancel := context.WithTimeout(ctx, trickestSyncBudget)
	defer cancel()

	limit := trickestLimitSetting(v.s.m.PG())
	items := v.vulnPocHuntQueue(limit)
	log.Printf("[vulndb-sync] trickest 队列 %d 条(限额 %d)", len(items), limit)
	for _, e := range items {
		if ctx.Err() != nil {
			return pocs, fmt.Errorf("trickest 本轮预算耗尽: %w", ctx.Err())
		}
		n, ferr := trickestAttachOne(ctx, v.s.m.PG(), e.ID, e.CVEID)
		if ferr != nil {
			if ctx.Err() != nil {
				return pocs, fmt.Errorf("trickest 本轮预算耗尽: %w", ctx.Err())
			}
			log.Printf("[vulndb-sync] trickest %s 拉取失败: %v", e.CVEID, ferr)
			continue
		}
		pocs += n
	}
	// 存量 github 源 PoC 的 README 补抓(每轮 cap 20 条防超时,契约 §5.6)。
	v.pocReadmeBackfillBatch(ctx)
	return pocs, nil
}

// trickestAttachOne 抓取单个 CVE 的 trickest 聚合页并挂载解析结果,返回新增
// PoC 行数。404=trickest 无该 CVE 的聚合记录,静默跳过(0, nil)。
// syncTrickest 与历史回填共用(回填只有 entry id + cve_id,不需要整条 entry)。
func trickestAttachOne(ctx context.Context, pg *db.DB, entryID int64, cveID string) (int, error) {
	cve := strings.ToUpper(strings.TrimSpace(cveID))
	m := trickestCVEPattern.FindStringSubmatch(cve)
	if m == nil {
		return 0, nil // 非标准 CVE 编号(理论上 cve_id<>'' 过滤后不会有):跳过
	}
	if err := rawFetchWait(ctx); err != nil {
		return 0, err
	}
	ectx, cancel := context.WithTimeout(ctx, trickestPerEntryTimeout)
	defer cancel()
	raw := fmt.Sprintf("%s/%s/%s.md", trickestRawBase, m[1], cve)
	body, err := pocorexpFetchText(ectx, raw, nil)
	if err != nil {
		var httpErr *pocorexpHTTPError
		if errors.As(err, &httpErr) && httpErr.Status == http.StatusNotFound {
			return 0, nil // 该 CVE 无聚合记录:静默跳过
		}
		return 0, err
	}
	parsed := parseTrickestMain(string(body))
	attached := 0
	gh := parsed.Github
	if len(gh) > trickestGithubCapPerEntry {
		gh = gh[:trickestGithubCapPerEntry]
	}
	for _, u := range gh {
		_, created, cerr := pg.CreateVulnPoc(db.VulnPocInput{
			EntryID: entryID,
			Name:    "Trickest: " + trickestGithubSlug(u),
			Kind:    "poc", Language: "", Source: "trickest", SourceURL: u,
			Content: u,
		})
		if cerr != nil {
			log.Printf("[vulndb-sync] trickest PoC 入库失败(%s): %v", cve, cerr)
			continue
		}
		if created {
			attached++
		}
	}
	secure := parsed.RefSecures
	if len(secure) > trickestSecureRefCap {
		secure = secure[:trickestSecureRefCap]
	}
	for _, u := range secure {
		name := "Trickest 引用: " + trickestHost(u)
		_, created, cerr := pg.CreateVulnPoc(db.VulnPocInput{
			EntryID: entryID,
			Name:    name,
			Kind:    "poc", Language: "", Source: "trickest", SourceURL: u,
			Content: u,
		})
		if cerr != nil {
			log.Printf("[vulndb-sync] trickest 引用 PoC 入库失败(%s): %v", cve, cerr)
			continue
		}
		if created {
			attached++
		}
	}
	refs := parsed.Refs
	if len(refs) > trickestRefsCap {
		refs = refs[:trickestRefsCap]
	}
	if len(refs) > 0 {
		if err := pg.MergeVulnRefs(entryID, refs); err != nil {
			log.Printf("[vulndb-sync] trickest 引用合并失败(%s): %v", cve, err)
		}
	}
	return attached, nil
}

// trickestHost 取 URL 的 host(解析失败回退为截断的原始串,供展示名用)。
func trickestHost(u string) string {
	if p, err := url.Parse(u); err == nil && p.Hostname() != "" {
		return p.Hostname()
	}
	return truncateRunes(u, 40)
}

// ---- 历史回填(全量无 PoC 条目) ----

// trickestBackfillCursor 回填光标(settings 键 vulndb_trickest_cursor 的 JSON 值):
// 按 vulndb_entries.id 升序推进,last_id=最后一个处理完的条目 id。
type trickestBackfillCursor struct {
	Done   bool  `json:"done"`
	LastID int64 `json:"last_id"`
}

// trickestCursorFromJSON 解析光标 JSON(契约 wire:{done bool, last_id int64};
// 坏 JSON 报错,缺字段取零值)。独立成纯函数便于单测。
func trickestCursorFromJSON(s string) (*trickestBackfillCursor, error) {
	var c trickestBackfillCursor
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return nil, fmt.Errorf("光标 JSON 解析失败: %w", err)
	}
	return &c, nil
}

// trickestLoadCursor 读取光标;不存在返回 (nil, nil)。
func trickestLoadCursor(pg *db.DB) (*trickestBackfillCursor, error) {
	val, ok, err := pg.GetSetting(settingVulnTrickestCursor)
	if err != nil {
		return nil, err
	}
	if !ok || strings.TrimSpace(val) == "" {
		return nil, nil
	}
	return trickestCursorFromJSON(val)
}

// trickestSaveCursor 写入光标(断点续跑的最小单位=单个条目)。
func trickestSaveCursor(pg *db.DB, c *trickestBackfillCursor) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return pg.SetSetting(settingVulnTrickestCursor, string(b))
}

// 回填全局单例状态(与 NVD 回填同款模式:同一时刻只允许一个实例在跑;
// 取消=ctx 取消,内存态进度供 GET 接口展示)。
var (
	trickestBackfillMu      sync.Mutex
	trickestBackfillRunning bool
	trickestBackfillCancel  context.CancelFunc
	trickestBackfillStat    PocBackfillStatus
)

// trickestBackfillSnapshot 回填作业当前状态(内存进度 + 空闲时读光标 done)。
func (s *Server) trickestBackfillSnapshot() PocBackfillStatus {
	trickestBackfillMu.Lock()
	st := trickestBackfillStat
	trickestBackfillMu.Unlock()
	if !st.Running {
		if cur, err := trickestLoadCursor(s.m.PG()); err == nil && cur != nil {
			st.Done = cur.Done
		}
	}
	return st
}

// trickestBackfillTryLaunch 尝试占用回填单例并启动运行协程;已在跑返回 false。
// resume 为 nil 时从头重扫(光标重置);非 nil 时从 last_id 续跑。
// ctx 必须是服务级 context(s.ctx),不能是 HTTP 请求级。
func (s *Server) trickestBackfillTryLaunch(ctx context.Context, resume *trickestBackfillCursor) bool {
	trickestBackfillMu.Lock()
	if trickestBackfillRunning {
		trickestBackfillMu.Unlock()
		return false
	}
	trickestBackfillRunning = true
	trickestBackfillMu.Unlock()

	go func() {
		defer func() {
			trickestBackfillMu.Lock()
			trickestBackfillRunning = false
			trickestBackfillCancel = nil
			trickestBackfillMu.Unlock()
		}()
		runCtx, cancel := context.WithTimeout(ctx, trickestBackfillBudget)
		defer cancel()
		trickestBackfillMu.Lock()
		trickestBackfillCancel = cancel
		trickestBackfillStat = PocBackfillStatus{
			Running:   true,
			StartedAt: time.Now().UTC().Format(time.RFC3339),
		}
		trickestBackfillMu.Unlock()
		s.trickestBackfillExecute(runCtx, resume)
	}()
	return true
}

// trickestBackfillEntry 回填扫描批(只取挂载所需的最小列)。
type trickestBackfillEntry struct {
	ID    int64
	CVEID string
}

// trickestBackfillExecute 回填主流程:按 id 升序分批扫 poc_count=0 且 cve_id
// 非空的条目,逐条抓 trickest 聚合页挂 PoC。每批(100 条)推进一次
// 光标;取消/超时时光标停在最后一个处理完的条目,可续跑。
func (s *Server) trickestBackfillExecute(ctx context.Context, cur *trickestBackfillCursor) {
	pg := s.m.PG()
	if cur == nil {
		cur = &trickestBackfillCursor{}
	}
	cur.Done = false
	if err := trickestSaveCursor(pg, cur); err != nil {
		log.Printf("[vulndb-backfill] trickest 写入光标失败: %v", err)
	}
	log.Printf("[vulndb-backfill] trickest 回填开始: last_id=%d", cur.LastID)

	processed, matched := 0, 0
	defer func() {
		if err := trickestSaveCursor(pg, cur); err != nil {
			log.Printf("[vulndb-backfill] trickest 写入光标失败: %v", err)
		}
		trickestBackfillMu.Lock()
		trickestBackfillStat.Running = false
		trickestBackfillStat.Done = cur.Done
		trickestBackfillStat.Processed = processed
		trickestBackfillStat.Matched = matched
		trickestBackfillStat.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		trickestBackfillMu.Unlock()
		if cur.Done {
			log.Printf("[vulndb-backfill] trickest 回填完成: 处理 %d 条,挂载 %d 条 PoC", processed, matched)
		} else {
			log.Printf("[vulndb-backfill] trickest 回填暂停于 last_id=%d(已处理 %d,光标已保留)", cur.LastID, processed)
		}
	}()

	for {
		if ctx.Err() != nil {
			return
		}
		batch, err := trickestBackfillNextBatch(pg, cur.LastID, trickestBackfillPage)
		if err != nil {
			trickestBackfillMu.Lock()
			trickestBackfillStat.Error = "扫描失败: " + err.Error()
			trickestBackfillMu.Unlock()
			log.Printf("[vulndb-backfill] trickest 批扫描失败: %v", err)
			return
		}
		if len(batch) == 0 {
			cur.Done = true // 队列扫完:光标置 done(下次 POST 会重头再扫)
			return
		}
		for _, it := range batch {
			if ctx.Err() != nil {
				return // 取消/超时:光标停在最后一个处理完的条目
			}
			n, ferr := trickestAttachOne(ctx, pg, it.ID, it.CVEID)
			processed++
			matched += n
			if ferr != nil && ctx.Err() == nil {
				// 单条网络失败:记入状态继续走,靠幂等重跑补齐。
				trickestBackfillMu.Lock()
				trickestBackfillStat.Error = fmt.Sprintf("%s: %v", it.CVEID, ferr)
				trickestBackfillMu.Unlock()
			}
			cur.LastID = it.ID
			if processed%20 == 0 {
				trickestBackfillMu.Lock()
				trickestBackfillStat.Processed = processed
				trickestBackfillStat.Matched = matched
				trickestBackfillMu.Unlock()
			}
		}
		trickestBackfillMu.Lock()
		trickestBackfillStat.Processed = processed
		trickestBackfillStat.Matched = matched
		trickestBackfillMu.Unlock()
		if err := trickestSaveCursor(pg, cur); err != nil {
			log.Printf("[vulndb-backfill] trickest 写入光标失败: %v", err)
		}
	}
}

// trickestBackfillNextBatch 取 id 大于 after 的一批待回填条目(只读 SELECT,
// 列最小化;ListVulnEntries 表达不了 id 游标)。挂载过程中条目可能被其它源
// 补上 PoC,条件不再精确成立——CreateVulnPoc 幂等,多挂无害。
func trickestBackfillNextBatch(pg *db.DB, after int64, limit int) ([]trickestBackfillEntry, error) {
	rows, err := pg.Query(`SELECT id, cve_id FROM vulndb_entries
WHERE poc_count = 0 AND cve_id <> '' AND id > $1
ORDER BY id ASC LIMIT $2`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []trickestBackfillEntry{}
	for rows.Next() {
		var e trickestBackfillEntry
		if err := rows.Scan(&e.ID, &e.CVEID); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
