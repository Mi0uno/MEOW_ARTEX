package server

// CVE 情报 v2:NVD 历史回填 + CISA KEV(见 VULNDB_CONTRACTS_V2.md §4)。
//
// NVD 历史回填是独立于 runOnce 轮询的后台 goroutine:从起始年 1 月 1 日起按
// 120 天窗口(官方单次查询上限)推进到今天,每窗口翻页拉全并逐条 UpsertCVE
// (幂等,中断重跑安全);进度以「光标」形式存 settings,进程重启可断点续跑。
// NVD 阶段完成后自动进入 PocOrExp 历史挂载阶段(由 Agent B 的
// backfillPocOrExpYears 实现),全部完成后光标置 done。
//
// CISA KEV 是挂在 runOnce 轮询里的常规源:拉已知在野利用目录,给库内条目打
// kev 标记,并把已移出目录的条目反向清除。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/recon"
)

const (
	// backfillISOFormat NVD API 要求的 UTC 时间格式。
	backfillISOFormat = "2006-01-02T15:04:05.000"
	// backfillWindowDays NVD 单次查询的窗口宽度(官方限制:不超过连续 120 天)。
	backfillWindowDays = 120
	// backfillPageSize NVD 单页条数上限(官方上限 2000,超过则用 startIndex 翻页)。
	backfillPageSize = 2000
	// backfillRequestInterval 无 API key 时 NVD 限速 5 次/30 秒 → 请求间隔 6.5 秒留余量。
	backfillRequestInterval = 6500 * time.Millisecond
	// backfillRetryBackoff 429/网络错误的退避时长。
	backfillRetryBackoff = 30 * time.Second
	// backfillWindowMaxRounds 同一窗口最多尝试的轮数(失败退避后重试)。
	backfillWindowMaxRounds = 3
	// backfillStartYearMin 允许的最早回填起点(API 传入值会夹取到 [2011, 当前年])。
	backfillStartYearMin = 2011
	// backfillTotalTimeout 整个回填(NVD + pocorexp 两阶段)的总预算。
	backfillTotalTimeout = 24 * time.Hour
	// backfillKEVFeedURL CISA 已知在野利用(Known Exploited Vulnerabilities)目录。
	backfillKEVFeedURL = "https://www.cisa.gov/sites/default/files/feeds/known_exploited_vulnerabilities.json"
	// settingVulnBackfillCursor 断点续跑光标的 settings 键。
	settingVulnBackfillCursor = "vulndb_backfill_cursor"
)

// 回填全局单例状态(backfill 与 runOnce 常规轮互不互斥,数据面靠幂等;
// 但 backfill 自身同一时刻只允许一个实例在跑)。
var (
	backfillMu      sync.Mutex
	backfillRunning bool
	backfillCancel  context.CancelFunc // 当前运行的取消函数(DELETE API 触发优雅停)
	backfillLastErr string             // 最近一次失败原因(仅内存态,重启即清空)
)

// backfillCursor 断点续跑光标(settings 键 vulndb_backfill_cursor 的 JSON 值)。
// window_end=已完成的最后一个 NVD 窗口终点;year_done 仅 pocorexp 阶段用
// (已完成的最大年份);JSON 字段名是冻结契约,不得改动。
type backfillCursor struct {
	Phase     string `json:"phase"`      // nvd | pocorexp
	WindowEnd string `json:"window_end"` // ISO(2006-01-02T15:04:05.000),空=未开始
	YearDone  int    `json:"year_done"`  // pocorexp 阶段已完成的最大年份
	StartYear int    `json:"start_year"` // 本次回填的起始年
	Done      bool   `json:"done"`
}

// backfillLoadCursor 读取光标;不存在返回 (nil, nil)。
func backfillLoadCursor(pg *db.DB) (*backfillCursor, error) {
	val, ok, err := pg.GetSetting(settingVulnBackfillCursor)
	if err != nil {
		return nil, err
	}
	if !ok || strings.TrimSpace(val) == "" {
		return nil, nil
	}
	var c backfillCursor
	if err := json.Unmarshal([]byte(val), &c); err != nil {
		return nil, fmt.Errorf("光标 JSON 解析失败: %w", err)
	}
	return &c, nil
}

// backfillSaveCursor 写入光标(断点续跑的最小单位=NVD 窗口/pocorexp 年份)。
func backfillSaveCursor(pg *db.DB, c *backfillCursor) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return pg.SetSetting(settingVulnBackfillCursor, string(b))
}

// vulnBackfillWorker 服务启动时拉起的自动续跑协程(server.go 冻结启动行):
// 先睡 30 秒避开启动期争抢;仅当光标存在且未完成、且采集开关未关闭时才续跑,
// 否则一次性退出(手动触发走 POST API 的独立协程)。
func (s *Server) vulnBackfillWorker(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
	}
	cur, err := backfillLoadCursor(s.m.PG())
	if err != nil {
		log.Printf("[vulndb-backfill] 读取续跑光标失败: %v", err)
		return
	}
	if cur == nil || cur.Done {
		return // 无未完成光标:不启动
	}
	if val, _, _ := s.m.PG().GetSetting(settingVulnDBSyncOn); val == "false" {
		return // 采集总开关被关闭:不续跑
	}
	log.Printf("[vulndb-backfill] 发现未完成光标,自动续跑: phase=%s start_year=%d window_end=%s",
		cur.Phase, cur.StartYear, cur.WindowEnd)
	if !s.backfillTryLaunch(ctx, cur.StartYear, cur) {
		log.Printf("[vulndb-backfill] 回填已在运行中,跳过自动续跑")
	}
}

// backfillTryLaunch 尝试占用回填单例并启动运行协程;已在跑返回 false。
// ctx 必须是服务级 context(如 s.ctx),不能是 HTTP 请求级(请求结束会取消)。
func (s *Server) backfillTryLaunch(ctx context.Context, startYear int, resume *backfillCursor) bool {
	backfillMu.Lock()
	if backfillRunning {
		backfillMu.Unlock()
		return false
	}
	backfillRunning = true
	backfillMu.Unlock()

	go func() {
		defer func() {
			backfillMu.Lock()
			backfillRunning = false
			backfillCancel = nil
			backfillMu.Unlock()
		}()
		runCtx, cancel := context.WithTimeout(ctx, backfillTotalTimeout)
		defer cancel()
		backfillMu.Lock()
		backfillCancel = cancel
		backfillLastErr = ""
		backfillMu.Unlock()
		s.vulnBackfillExecute(runCtx, startYear, resume)
	}()
	return true
}

// vulnBackfillExecute 回填主流程:阶段一 NVD 分窗拉取 → 阶段二 PocOrExp 历史挂载
// → 光标置 done。取消/超时/失败都把光标留在最后完成的进度上,等待续跑。
func (s *Server) vulnBackfillExecute(ctx context.Context, startYear int, resume *backfillCursor) {
	pg := s.m.PG()
	now := time.Now().UTC()
	cur := &backfillCursor{Phase: "nvd", StartYear: startYear}
	if resume != nil {
		cur = resume // 续跑:沿用光标里的进度与起始年
	}
	cur.Done = false
	if err := backfillSaveCursor(pg, cur); err != nil {
		log.Printf("[vulndb-backfill] 写入光标失败: %v", err)
	}

	// ---- 阶段一:NVD 历史分窗回填 ----
	if cur.Phase == "nvd" {
		from := time.Date(cur.StartYear, 1, 1, 0, 0, 0, 0, time.UTC)
		if cur.WindowEnd != "" {
			if t, err := time.Parse(backfillISOFormat, cur.WindowEnd); err == nil {
				from = t // 从最后完成的窗口终点继续
			} else {
				log.Printf("[vulndb-backfill] 光标 window_end 解析失败(%s),回退到起始年: %v", cur.WindowEnd, err)
			}
		}
		if err := s.backfillNVDWindows(ctx, cur, from, now); err != nil {
			if ctx.Err() != nil {
				// 优雅停(手动取消或总超时):光标停在最后完成的窗口。
				log.Printf("[vulndb-backfill] 回填暂停于 window_end=%s,光标已保留", cur.WindowEnd)
				return
			}
			backfillMu.Lock()
			backfillLastErr = err.Error()
			backfillMu.Unlock()
			log.Printf("[vulndb-backfill] NVD 历史回填中止: %v", err)
			return
		}
		// NVD 完成 → 进入 pocorexp 阶段。
		cur.Phase = "pocorexp"
		cur.WindowEnd = ""
		cur.YearDone = 0
		if err := backfillSaveCursor(pg, cur); err != nil {
			log.Printf("[vulndb-backfill] 写入光标失败: %v", err)
		}
	}

	// ---- 阶段二:PocOrExp 历史挂载(Agent B 提供,只挂不建、全量幂等) ----
	fromYear := cur.StartYear
	if cur.YearDone > fromYear {
		fromYear = cur.YearDone + 1
	}
	if fromYear <= now.Year() {
		attached, err := backfillPocOrExpYears(ctx, pg, fromYear, now.Year())
		if err != nil {
			if ctx.Err() != nil {
				log.Printf("[vulndb-backfill] 回填暂停于 pocorexp 阶段(year_done=%d),光标已保留", cur.YearDone)
				return
			}
			backfillMu.Lock()
			backfillLastErr = err.Error()
			backfillMu.Unlock()
			log.Printf("[vulndb-backfill] PocOrExp 历史挂载中止: %v", err)
			return
		}
		cur.YearDone = now.Year()
		if err := backfillSaveCursor(pg, cur); err != nil {
			log.Printf("[vulndb-backfill] 写入光标失败: %v", err)
		}
		log.Printf("[vulndb-backfill] PocOrExp 历史挂载完成: %d 条", attached)
	}

	cur.Done = true
	if err := backfillSaveCursor(pg, cur); err != nil {
		log.Printf("[vulndb-backfill] 写入光标失败: %v", err)
	}
	log.Printf("[vulndb-backfill] 历史回填全部完成(start_year=%d)", cur.StartYear)
}

// backfillNVDWindows 按 120 天窗口从 from 推进到 until,逐窗口拉取入库;
// 每完成一个窗口推进一次光标。任何窗口重试耗尽即中止(光标留在上一窗口,
// 续跑时重试该窗口),避免静默跳窗造成数据空洞。
func (s *Server) backfillNVDWindows(ctx context.Context, cur *backfillCursor, from, until time.Time) error {
	cursor := from
	for cursor.Before(until) {
		if err := ctx.Err(); err != nil {
			return err
		}
		wEnd := cursor.AddDate(0, 0, backfillWindowDays)
		if wEnd.After(until) {
			wEnd = until
		}
		inserted, updated, err := s.backfillNVDWindow(ctx, cursor, wEnd)
		if err != nil {
			return fmt.Errorf("窗口 %s ~ %s: %w",
				cursor.Format(backfillISOFormat), wEnd.Format(backfillISOFormat), err)
		}
		// 窗口完成 → 推进光标。
		cur.WindowEnd = wEnd.Format(backfillISOFormat)
		if err := backfillSaveCursor(s.m.PG(), cur); err != nil {
			log.Printf("[vulndb-backfill] 写入光标失败: %v", err)
		}
		log.Printf("[vulndb-backfill] 窗口完成 %s ~ %s: 新增 %d 更新 %d",
			cursor.Format("2006-01-02"), wEnd.Format("2006-01-02"), inserted, updated)
		cursor = wEnd
	}
	return nil
}

// backfillNVDWindow 拉取单个 120 天窗口:每个窗口写一条 vulndb_sync_runs
// (source=nvd_backfill);429/网络错误退避 30 秒重试,同窗口最多 3 轮。
func (s *Server) backfillNVDWindow(ctx context.Context, start, end time.Time) (inserted, updated int, err error) {
	runID, err := s.m.PG().StartVulnSyncRun("nvd_backfill")
	if err != nil {
		return 0, 0, err
	}
	defer func() {
		status := "done"
		errMsg := ""
		if err != nil {
			status, errMsg = "failed", err.Error()
		}
		_ = s.m.PG().FinishVulnSyncRun(runID, status, inserted+updated, inserted, updated, 0, errMsg)
	}()

	var lastErr error
	for round := 1; round <= backfillWindowMaxRounds; round++ {
		if round > 1 {
			// 429/网络错误退避 30 秒再试;ctx 被取消则立即放弃。
			select {
			case <-ctx.Done():
				return inserted, updated, ctx.Err()
			case <-time.After(backfillRetryBackoff):
			}
		}
		// 每轮从窗口头重拉:UpsertCVE 幂等,已入库的只会被记为更新,无副作用。
		n, u, rerr := s.backfillNVDWindowOnce(ctx, start, end)
		inserted, updated, lastErr = n, u, rerr
		if rerr == nil {
			return n, u, nil
		}
		if ctx.Err() != nil {
			return inserted, updated, ctx.Err()
		}
		log.Printf("[vulndb-backfill] 窗口 %s..%s 第 %d/%d 轮失败: %v",
			start.Format("2006-01-02"), end.Format("2006-01-02"), round, backfillWindowMaxRounds, rerr)
	}
	return inserted, updated, fmt.Errorf("重试 %d 轮后仍失败: %w", backfillWindowMaxRounds, lastErr)
}

// backfillNVDWindowOnce 单轮拉取一个窗口:totalResults 超过单页上限时翻页拉全。
func (s *Server) backfillNVDWindowOnce(ctx context.Context, start, end time.Time) (inserted, updated int, err error) {
	startIndex := 0
	for {
		if err := ctx.Err(); err != nil {
			return inserted, updated, err
		}
		// NVD 无 API key 限速 5 次/30 秒:每个请求之间固定等 6.5 秒。
		if err := backfillRateWait(ctx); err != nil {
			return inserted, updated, err
		}
		api := fmt.Sprintf(
			"https://services.nvd.nist.gov/rest/json/cves/2.0?pubStartDate=%s&pubEndDate=%s&resultsPerPage=%d&startIndex=%d",
			url.QueryEscape(start.Format(backfillISOFormat)),
			url.QueryEscape(end.Format(backfillISOFormat)),
			backfillPageSize, startIndex)
		var page backfillNVDPage
		// 用回填专用的长超时抓取(见 backfillFetchNVDPage):NVD 大页 60s 拉不完。
		if err := backfillFetchNVDPage(ctx, api, &page); err != nil {
			return inserted, updated, err
		}
		for i := range page.Vulnerabilities {
			ins, _, err := s.backfillUpsertNVDItem(&page.Vulnerabilities[i].CVE)
			if err != nil {
				log.Printf("[vulndb-backfill] upsert %s 失败: %v", page.Vulnerabilities[i].CVE.ID, err)
				continue
			}
			if ins {
				inserted++
			} else {
				updated++
			}
		}
		// 翻页终止条件:页为空、页大小异常(防御格式变化导致死循环)或已到总数。
		if page.ResultsPerPage <= 0 || len(page.Vulnerabilities) == 0 {
			break
		}
		startIndex += page.ResultsPerPage
		if startIndex >= page.TotalResults {
			break
		}
	}
	return inserted, updated, nil
}

// backfillRateWait 请求间限速等待(6.5 秒);ctx 取消立即返回。
func backfillRateWait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(backfillRequestInterval):
		return nil
	}
}

// backfillCVSSMetric CVSS 指标块。v3 系的 baseSeverity 在 cvssData 里;
// v2 的 cvssData 只有 baseScore(展示严重度由分数映射得出,见 backfillSeverityFromCVSS2)。
type backfillCVSSMetric struct {
	CVSSData struct {
		BaseScore    float64 `json:"baseScore"`
		BaseSeverity string  `json:"baseSeverity"`
	} `json:"cvssData"`
}

// backfillNVDCve NVD CVE API 2.0 的单条 CVE 字段结构,与 vulndb_sync.go 的
// syncNVD 保持一致(id/descriptions/metrics/configurations/references),
// 额外补了 cvssMetricV2 用于 severity 兜底(历史 CVE 大多只有 v2 评分)。
type backfillNVDCve struct {
	ID           string `json:"id"`
	Descriptions []struct {
		Lang  string `json:"lang"`
		Value string `json:"value"`
	} `json:"descriptions"`
	Metrics struct {
		CVSSMetricV31 []backfillCVSSMetric `json:"cvssMetricV31"`
		CVSSMetricV30 []backfillCVSSMetric `json:"cvssMetricV30"`
		CVSSMetricV2  []backfillCVSSMetric `json:"cvssMetricV2"`
	} `json:"metrics"`
	References []struct {
		URL string `json:"url"`
	} `json:"references"`
	Configurations []struct {
		Nodes []struct {
			CPEMatch []struct {
				Criteria string `json:"criteria"`
			} `json:"cpeMatch"`
		} `json:"nodes"`
	} `json:"configurations"`
}

// backfillNVDPage NVD 分页响应。
type backfillNVDPage struct {
	ResultsPerPage  int `json:"resultsPerPage"`
	StartIndex      int `json:"startIndex"`
	TotalResults    int `json:"totalResults"`
	Vulnerabilities []struct {
		CVE backfillNVDCve `json:"cve"`
	} `json:"vulnerabilities"`
}

// backfillSeverityFromCVSS2 把 CVSS v2 分数映射为展示严重度。注意:这是
// 展示口径不是评分口径——v2 官方评级只有 low/medium/high(无 critical 档),
// 这里按分数与 v3 档位对齐,方便前端统一筛选与渲染;分数<=0 视为未知。
func backfillSeverityFromCVSS2(score float64) string {
	switch {
	case score >= 9:
		return "critical"
	case score >= 7:
		return "high"
	case score >= 4:
		return "medium"
	case score > 0:
		return "low"
	default:
		return ""
	}
}

// backfillSeverity 依次取 CVSS v3.1 → v3.0 → v2 兜底的展示严重度
// (2016 年前的回填数据大多只有 v2 评分)。
func backfillSeverity(c *backfillNVDCve) string {
	if len(c.Metrics.CVSSMetricV31) > 0 {
		return strings.ToLower(c.Metrics.CVSSMetricV31[0].CVSSData.BaseSeverity)
	}
	if len(c.Metrics.CVSSMetricV30) > 0 {
		return strings.ToLower(c.Metrics.CVSSMetricV30[0].CVSSData.BaseSeverity)
	}
	if len(c.Metrics.CVSSMetricV2) > 0 {
		return backfillSeverityFromCVSS2(c.Metrics.CVSSMetricV2[0].CVSSData.BaseScore)
	}
	return ""
}

// backfillUpsertNVDItem 把一条 NVD CVE 写入库(字段处理与 syncNVD 一致,幂等)。
func (s *Server) backfillUpsertNVDItem(c *backfillNVDCve) (inserted, updated bool, err error) {
	if strings.TrimSpace(c.ID) == "" {
		return false, false, fmt.Errorf("CVE id 为空")
	}
	// 英文描述优先(中文描述在 NVD 里较少见)。
	desc := ""
	for _, d := range c.Descriptions {
		if d.Lang == "en" {
			desc = d.Value
			break
		}
	}
	if desc == "" && len(c.Descriptions) > 0 {
		desc = c.Descriptions[0].Value
	}
	severity := backfillSeverity(c)
	// 受影响产品:从 CPE 里取厂商/产品(取第一个,作为检索维度)。
	vendor, product := "", ""
	for _, cfg := range c.Configurations {
		for _, node := range cfg.Nodes {
			for _, m := range node.CPEMatch {
				parts := strings.Split(m.Criteria, ":")
				if len(parts) >= 5 {
					if vendor == "" {
						vendor = parts[3]
					}
					if product == "" {
						product = parts[4]
					}
				}
			}
		}
	}
	refs := make([]string, 0, len(c.References))
	for _, r := range c.References {
		refs = append(refs, r.URL)
	}
	refJSON, _ := json.Marshal(refs)
	title := fmt.Sprintf("%s %s", c.ID, truncateRunes(desc, 120))

	_, inserted, err = s.m.PG().UpsertCVE(c.ID, title, desc, severity, vendor, product, "", refJSON)
	updated = err == nil && !inserted
	return inserted, updated, err
}

// ---- CISA KEV(挂在 runOnce 常规轮) ----

// backfillFetchNVDPage 抓取 NVD API 单页(带回填专用的 180s 长超时):
// 单页 2000 条的响应体可达 10MB+,常规轮 fetchJSON 的 60s 客户端超时在跨境
// 链路上不够(2026-09-26 冒烟实测第一窗口两次 body 读取超时)。限速仍由
// backfillRateWait 控制,放宽超时不影响 NVD 的请求次数限制。
func backfillFetchNVDPage(ctx context.Context, rawURL string, out any) error {
	if _, err := recon.ValidatePublicURL(rawURL); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ARTEX-VulnDB/1.0")
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 180 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("上游限流(429)")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("上游返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// backfillFetchJSONText 抓取一个公开 JSON 文本资源(KEV 目录等):
// 先过 recon.ValidatePublicURL(拒绝内网/环回/保留地址),带 UA 标识,
// 32MB 读取上限,60 秒超时,返回原始字节。
func backfillFetchJSONText(ctx context.Context, rawURL string) ([]byte, error) {
	if _, err := recon.ValidatePublicURL(rawURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ARTEX-VulnDB/1.0")
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("上游返回 HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}

// backfillParseKEV 解析 CISA KEV 目录 JSON,返回去重保序、大写规范化的 CVE 编号列表。
func backfillParseKEV(body []byte) ([]string, error) {
	var feed struct {
		Vulnerabilities []struct {
			CVEID string `json:"cveID"`
		} `json:"vulnerabilities"`
	}
	if err := json.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("KEV 目录 JSON 解析失败: %w", err)
	}
	seen := make(map[string]bool, len(feed.Vulnerabilities))
	out := make([]string, 0, len(feed.Vulnerabilities))
	for _, v := range feed.Vulnerabilities {
		id := strings.ToUpper(strings.TrimSpace(v.CVEID))
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

// syncKEV 拉 CISA 已知在野利用目录并同步 kev 标记:
// 全量 upsert 置 true;开始前记旧集合,结束后把已移出目录的旧条目反向置 false。
// marked=本次实际置 true 的库内条目数(目录里有而库内暂无的 CVE,等 NVD 回填
// 入库后下一轮自然补标记,见 db.SetKevByCVE 的注释)。
func (v *vulnSync) syncKEV(ctx context.Context) (marked int, err error) {
	runID, err := v.s.m.PG().StartVulnSyncRun("kev")
	if err != nil {
		return 0, err
	}
	defer func() {
		status := "done"
		errMsg := ""
		if err != nil {
			status, errMsg = "failed", err.Error()
		}
		_ = v.s.m.PG().FinishVulnSyncRun(runID, status, marked, 0, marked, 0, errMsg)
	}()

	ctx, cancel := context.WithTimeout(ctx, vulnSyncTimeout)
	defer cancel()
	body, err := backfillFetchJSONText(ctx, backfillKEVFeedURL)
	if err != nil {
		return 0, err
	}
	cves, err := backfillParseKEV(body)
	if err != nil {
		return 0, err
	}
	if len(cves) == 0 {
		return 0, fmt.Errorf("KEV 目录解析结果为空,疑似上游格式变化")
	}
	// 旧集合:结束后把已移出目录的条目反向清除。
	old, err := v.s.m.PG().ListKevCveIds()
	if err != nil {
		return 0, err
	}
	newSet := make(map[string]bool, len(cves))
	for _, c := range cves {
		newSet[c] = true
	}
	for _, c := range cves {
		if err := ctx.Err(); err != nil {
			return marked, err
		}
		exists, err := v.s.m.PG().SetKevByCVE(c, true)
		if err != nil {
			// 单条失败不拖垮整轮,继续处理其余条目。
			log.Printf("[vulndb-sync] KEV 标记 %s 失败: %v", c, err)
			continue
		}
		if exists {
			marked++
		}
	}
	// 目录移除反向清除。
	for _, id := range old {
		if newSet[id] {
			continue
		}
		if _, err := v.s.m.PG().SetKevByCVE(id, false); err != nil {
			log.Printf("[vulndb-sync] KEV 反向清除 %s 失败: %v", id, err)
		}
	}
	return marked, nil
}
