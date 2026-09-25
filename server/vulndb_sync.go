package server

// CVE 实时搜集:定时从公开漏洞源拉取情报并自动发现 PoC。
//
// 数据源(全部公开、无需商业凭据):
//   - NVD CVE API 2.0:官方 CVE 库,含 CVSS 严重度、受影响产品、参考链接。
//   - GitHub Advisory (GHSA):开源生态漏洞,含受影响包版本。
//   - Exploit-DB:公开 EXP 索引(通过其 GitHub 镜像的 files_exploits.csv)。
//   - GitHub PoC 搜索:按 CVE 编号搜 PoC 仓库(限速,仅对近期高危 CVE 抽样)。
//
// 运行方式:后台 goroutine 定时轮询(默认 6 小时,可配开关与间隔),不占 agent
// 并发槽、不耗 LLM。每轮记入 vulndb_sync_runs 供页面展示同步状态。

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/recon"
)

const (
	settingVulnDBSyncOn       = "vulndb_sync_enabled"
	settingVulnDBSyncInterval = "vulndb_sync_interval_hours"
	settingVulnGitHubToken    = "vulndb_github_token" // 可选:GitHub 个人令牌,放大 PoC 搜索配额
	vulnSyncDefaultInterval   = 6                     // 小时
	vulnSyncFetchLimit        = 2000
	vulnSyncTimeout           = 5 * time.Minute
	// vulnBackfillWindowStart 历史回填窗口起点(含)。NVD 全量回填从这里拉到今天;
	// 更早的年代只由 Exploit-DB 反向建条目(只补确有公开 EXP 的那部分,避免灌入
	// 大量无严重度/无 PoC 的空壳条目)。用户可在回填 API 里改 start_year。
	vulnBackfillWindowStart = 2016
)

// vulnSync 是 CVE 采集器的运行状态(单例,挂在 Server 上)。
type vulnSync struct {
	mu      sync.Mutex
	running bool
	wake    chan struct{}
	s       *Server
}

func newVulnSync(s *Server) *vulnSync {
	return &vulnSync{wake: make(chan struct{}, 1), s: s}
}

// trigger 请求立即跑一轮;已有采集在跑时返回 false。
func (v *vulnSync) trigger() bool {
	v.mu.Lock()
	if v.running {
		v.mu.Unlock()
		return false
	}
	v.mu.Unlock()
	select {
	case v.wake <- struct{}{}:
	default:
	}
	return true
}

// run 是采集器主循环:按配置的间隔轮询,收到 wake 立即跑一轮。
func (v *vulnSync) run(ctx context.Context) {
	// 启动后先等 30 秒再跑首轮,避免与启动期的其他初始化抢网络与 DB。
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
	}
	for {
		if v.enabled() {
			v.runOnce(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-v.wake:
			// 手动触发:立即进入下一轮。
		case <-time.After(v.interval()):
		}
	}
}

func (v *vulnSync) enabled() bool {
	val, _, _ := v.s.m.PG().GetSetting(settingVulnDBSyncOn)
	// 默认开启(未设置视为开):用户要的是「24 小时不间断搜集」。
	return val != "false"
}

func (v *vulnSync) interval() time.Duration {
	h := vulnSyncDefaultInterval
	if val, _, _ := v.s.m.PG().GetSetting(settingVulnDBSyncInterval); val != "" {
		if n, err := strconv.Atoi(val); err == nil && n > 0 {
			h = n
		}
	}
	return time.Duration(h) * time.Hour
}

// runOnce 跑一轮完整采集:各源依次拉取,汇总统计。
func (v *vulnSync) runOnce(ctx context.Context) {
	v.mu.Lock()
	if v.running {
		v.mu.Unlock()
		return
	}
	v.running = true
	v.mu.Unlock()
	defer func() {
		v.mu.Lock()
		v.running = false
		v.mu.Unlock()
	}()

	log.Printf("[vulndb-sync] 开始采集")
	start := time.Now()
	totalNew, totalUpd, totalPocs := 0, 0, 0

	// 1) NVD CVE API:官方 CVE 库。
	if n, u, err := v.syncNVD(ctx); err != nil {
		log.Printf("[vulndb-sync] NVD 失败: %v", err)
	} else {
		totalNew += n
		totalUpd += u
	}
	// 2) GitHub Advisory:开源生态漏洞。
	if n, u, err := v.syncGitHubAdvisory(ctx); err != nil {
		log.Printf("[vulndb-sync] GitHub Advisory 失败: %v", err)
	} else {
		totalNew += n
		totalUpd += u
	}
	// 3) Exploit-DB:公开 EXP 索引(为已有条目补 PoC;窗口外的老洞反向建条目)。
	if p, err := v.syncExploitDB(ctx); err != nil {
		log.Printf("[vulndb-sync] Exploit-DB 失败: %v", err)
	} else {
		totalPocs += p
	}
	// 4) GitHub PoC 搜索:为近期高危 CVE 发现 PoC 仓库(限速,抽样)。
	if p, err := v.syncGitHubPoCs(ctx); err != nil {
		log.Printf("[vulndb-sync] GitHub PoC 搜索失败: %v", err)
	} else {
		totalPocs += p
	}
	// ---- v2 新增源(实现见 VULNDB_CONTRACTS_V2.md 的文件所有权矩阵) ----
	// 5) CISA KEV:已知被在野利用目录,给条目打 kev 标记。
	if n, err := v.syncKEV(ctx); err != nil {
		log.Printf("[vulndb-sync] KEV 失败: %v", err)
	} else {
		totalUpd += n
	}
	// 6) PocOrExp_in_Github:当日 GitHub PoC/EXP 增量。
	if p, err := v.syncPocOrExpToday(ctx); err != nil {
		log.Printf("[vulndb-sync] PocOrExp 失败: %v", err)
	} else {
		totalPocs += p
	}
	// 7) nuclei 官方模板:按 CVE 挂模板 PoC。
	if p, err := v.syncNucleiTemplates(ctx); err != nil {
		log.Printf("[vulndb-sync] nuclei 模板失败: %v", err)
	} else {
		totalPocs += p
	}
	// 8) 安全社区文章 RSS:FreeBuf/安全客/先知,提取 CVE 编号入库/补参考链接。
	if p, err := v.syncArticleFeeds(ctx); err != nil {
		log.Printf("[vulndb-sync] 文章抓取失败: %v", err)
	} else {
		totalPocs += p
	}

	log.Printf("[vulndb-sync] 采集完成:新增 %d 更新 %d PoC %d,耗时 %s",
		totalNew, totalUpd, totalPocs, time.Since(start).Round(time.Second))
}

// fetchJSON 发一个 GET 并把 JSON 解到 out。
func fetchJSON(ctx context.Context, rawURL string, headers map[string]string, out any) error {
	if _, err := recon.ValidatePublicURL(rawURL); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ARTEX-VulnDB/1.0")
	req.Header.Set("Accept", "application/json")
	for k, val := range headers {
		req.Header.Set(k, val)
	}
	client := &http.Client{Timeout: 60 * time.Second}
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// syncNVD 从 NVD CVE API 2.0 拉取最近发布的 CVE。
func (v *vulnSync) syncNVD(ctx context.Context) (inserted, updated int, err error) {
	runID, err := v.s.m.PG().StartVulnSyncRun("nvd")
	if err != nil {
		return 0, 0, err
	}
	defer func() {
		status := "done"
		errMsg := ""
		if err != nil {
			status, errMsg = "failed", err.Error()
		}
		_ = v.s.m.PG().FinishVulnSyncRun(runID, status, inserted+updated, inserted, updated, 0, errMsg)
	}()

	// 拉最近 7 天发布的 CVE(按发布时间倒序)。
	end := time.Now().UTC()
	start := end.AddDate(0, 0, -7)
	api := fmt.Sprintf("https://services.nvd.nist.gov/rest/json/cves/2.0?pubStartDate=%s&pubEndDate=%s&resultsPerPage=%d",
		url.QueryEscape(start.Format("2006-01-02T15:04:05.000")),
		url.QueryEscape(end.Format("2006-01-02T15:04:05.000")),
		vulnSyncFetchLimit)
	ctx, cancel := context.WithTimeout(ctx, vulnSyncTimeout)
	defer cancel()

	var parsed struct {
		Vulnerabilities []struct {
			CVE struct {
				ID           string `json:"id"`
				Published    string `json:"published"`
				Descriptions []struct {
					Lang  string `json:"lang"`
					Value string `json:"value"`
				} `json:"descriptions"`
				Metrics struct {
					CVSSMetricV31 []struct {
						CVSSData struct {
							BaseScore    float64 `json:"baseScore"`
							BaseSeverity string  `json:"baseSeverity"`
						} `json:"cvssData"`
					} `json:"cvssMetricV31"`
					CVSSMetricV30 []struct {
						CVSSData struct {
							BaseScore    float64 `json:"baseScore"`
							BaseSeverity string  `json:"baseSeverity"`
						} `json:"cvssData"`
					} `json:"cvssMetricV30"`
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
			} `json:"cve"`
		} `json:"vulnerabilities"`
	}
	if err := fetchJSON(ctx, api, nil, &parsed); err != nil {
		return 0, 0, err
	}

	for _, item := range parsed.Vulnerabilities {
		c := item.CVE
		if c.ID == "" {
			continue
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
		severity := ""
		if len(c.Metrics.CVSSMetricV31) > 0 {
			severity = strings.ToLower(c.Metrics.CVSSMetricV31[0].CVSSData.BaseSeverity)
		} else if len(c.Metrics.CVSSMetricV30) > 0 {
			severity = strings.ToLower(c.Metrics.CVSSMetricV30[0].CVSSData.BaseSeverity)
		}
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

		_, isNew, err := v.s.m.PG().UpsertCVE(c.ID, title, desc, severity, vendor, product, "", refJSON)
		if err != nil {
			log.Printf("[vulndb-sync] NVD upsert %s 失败: %v", c.ID, err)
			continue
		}
		if isNew {
			inserted++
		} else {
			updated++
		}
	}
	return inserted, updated, nil
}

// syncGitHubAdvisory 从 GitHub Advisory 数据库拉取最近漏洞(公开 REST API)。
func (v *vulnSync) syncGitHubAdvisory(ctx context.Context) (inserted, updated int, err error) {
	runID, err := v.s.m.PG().StartVulnSyncRun("github_advisory")
	if err != nil {
		return 0, 0, err
	}
	defer func() {
		status := "done"
		errMsg := ""
		if err != nil {
			status, errMsg = "failed", err.Error()
		}
		_ = v.s.m.PG().FinishVulnSyncRun(runID, status, inserted+updated, inserted, updated, 0, errMsg)
	}()

	ctx, cancel := context.WithTimeout(ctx, vulnSyncTimeout)
	defer cancel()
	// 按发布时间倒序取最近 100 条(GitHub 单页上限 100)。
	var advisories []struct {
		GHSAID          string `json:"ghsa_id"`
		CVEID           string `json:"cve_id"`
		Summary         string `json:"summary"`
		Description     string `json:"description"`
		Severity        string `json:"severity"`
		HTMLURL         string `json:"html_url"`
		PublishedAt     string `json:"published_at"`
		Vulnerabilities []struct {
			Package struct {
				Ecosystem string `json:"ecosystem"`
				Name      string `json:"name"`
			} `json:"package"`
			VulnerableVersionRange string `json:"vulnerable_version_range"`
		} `json:"vulnerabilities"`
	}
	api := "https://api.github.com/advisories?per_page=100&sort=published&direction=desc"
	if err := fetchJSON(ctx, api, nil, &advisories); err != nil {
		return 0, 0, err
	}

	for _, a := range advisories {
		cve := strings.ToUpper(strings.TrimSpace(a.CVEID))
		if cve == "" {
			// 无 CVE 编号的 GHSA 归入 nday(已公开但无 CVE 编号)。
			if strings.TrimSpace(a.GHSAID) == "" || strings.TrimSpace(a.Summary) == "" {
				continue
			}
			// nday 类没有唯一键,按 GHSA 编号判重跳过。
			if v.ndayExists(a.GHSAID) {
				continue
			}
			vendor, product, affected := ghsaAffected(a.Vulnerabilities)
			if _, err := v.s.m.PG().CreateVulnEntry(db.VulnEntryInput{
				Category: db.VulnCategoryNDay,
				Title:    fmt.Sprintf("%s %s", a.GHSAID, truncateRunes(a.Summary, 120)),
				Vendor:   vendor, Product: product, Affected: affected,
				Severity: strings.ToLower(a.Severity), Description: a.Description,
				Source: "cve_feed", Status: "active",
				Refs: vulnJSON([]string{a.HTMLURL}),
			}); err == nil {
				inserted++
			}
			continue
		}
		vendor, product, affected := ghsaAffected(a.Vulnerabilities)
		refs := vulnJSON([]string{a.HTMLURL})
		_, isNew, err := v.s.m.PG().UpsertCVE(cve, fmt.Sprintf("%s %s", cve, truncateRunes(a.Summary, 120)),
			a.Description, strings.ToLower(a.Severity), vendor, product, affected, refs)
		if err != nil {
			log.Printf("[vulndb-sync] GHSA upsert %s 失败: %v", cve, err)
			continue
		}
		if isNew {
			inserted++
		} else {
			updated++
		}
	}
	return inserted, updated, nil
}

// ghsaAffected 从 GHSA 的 vulnerabilities 提取厂商/产品/影响版本。
func ghsaAffected(vulns []struct {
	Package struct {
		Ecosystem string `json:"ecosystem"`
		Name      string `json:"name"`
	} `json:"package"`
	VulnerableVersionRange string `json:"vulnerable_version_range"`
}) (vendor, product, affected string) {
	if len(vulns) == 0 {
		return "", "", ""
	}
	vendor = vulns[0].Package.Ecosystem
	product = vulns[0].Package.Name
	parts := make([]string, 0, len(vulns))
	for _, v := range vulns {
		if v.VulnerableVersionRange != "" {
			parts = append(parts, v.Package.Name+" "+v.VulnerableVersionRange)
		}
	}
	return vendor, product, strings.Join(parts, "; ")
}

// ndayExists 判断同名 nday 条目是否已存在(nday 无唯一键,按标题判重)。
func (v *vulnSync) ndayExists(prefix string) bool {
	items, _, _, err := v.s.m.PG().ListVulnEntries(db.VulnEntryFilter{
		Category: db.VulnCategoryNDay, Q: prefix, Limit: 1,
	})
	return err == nil && len(items) > 0
}

// syncExploitDB 从 Exploit-DB 的公开 CSV 索引为已有 CVE 条目补 PoC。
func (v *vulnSync) syncExploitDB(ctx context.Context) (pocs int, err error) {
	runID, err := v.s.m.PG().StartVulnSyncRun("exploitdb")
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

	ctx, cancel := context.WithTimeout(ctx, vulnSyncTimeout)
	defer cancel()
	// Exploit-DB 官方索引现由 GitLab 托管(GitHub 的 offensive-security 仓库已 404)。
	const csvURL = "https://gitlab.com/exploit-database/exploitdb/-/raw/main/files_exploits.csv"
	if _, err := recon.ValidatePublicURL(csvURL); err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, csvURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "ARTEX-VulnDB/1.0")
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("Exploit-DB 索引返回 HTTP %d", resp.StatusCode)
	}
	reader := csv.NewReader(io.LimitReader(resp.Body, 64<<20))
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		return 0, err
	}
	if len(rows) < 2 {
		return 0, nil
	}
	// 列: id,file,description,date_published,author,type,platform,port,date_added,
	//     date_updated,verified,codes,tags,aliases,screenshot_url,application_url,source_url
	// CVE 编号在 codes 列(多个用 ; 分隔,混有 OSVDB 等其它编号)。
	header := rows[0]
	idxID, idxFile, idxDesc, idxType, idxCodes, idxDate := -1, -1, -1, -1, -1, -1
	for i, h := range header {
		switch strings.TrimSpace(strings.ToLower(h)) {
		case "id":
			idxID = i
		case "file":
			idxFile = i
		case "description":
			idxDesc = i
		case "type":
			idxType = i
		case "codes":
			idxCodes = i
		case "date_published":
			idxDate = i
		}
	}
	if idxCodes < 0 || idxFile < 0 {
		return 0, fmt.Errorf("Exploit-DB 索引列格式变化")
	}
	for _, row := range rows[1:] {
		if len(row) <= idxCodes || len(row) <= idxFile {
			continue
		}
		codesField := strings.TrimSpace(row[idxCodes])
		if codesField == "" {
			continue
		}
		// codes 列形如 "CVE-2009-3699;OSVDB-58726",只取 CVE 编号。
		for _, one := range strings.Split(codesField, ";") {
			cve := strings.ToUpper(strings.TrimSpace(one))
			if !strings.HasPrefix(cve, "CVE-") {
				continue
			}
			entry, err := v.s.m.PG().FindVulnEntryByCVE(cve)
			if err != nil {
				log.Printf("[vulndb-sync] Exploit-DB 查询 %s 失败: %v", cve, err)
				continue
			}
			if entry == nil {
				// 库里没有这条 CVE:窗口内(2016 起)的交给 NVD/GHSA 全量回填建档,这里跳过;
				// 窗口外的老洞反向建档——只有确有公开 EXP 的老 CVE 才值得入库,这正好是
				// 「历史老洞」里真正有利用价值的子集(避免灌入大量无严重度/无 PoC 的空壳)。
				if !edbpublishedBefore(idxDate, row, vulnBackfillWindowStart) {
					continue
				}
				desc0 := ""
				if idxDesc >= 0 && len(row) > idxDesc {
					desc0 = strings.TrimSpace(row[idxDesc])
				}
				edURL := ""
				if idxID >= 0 && len(row) > idxID {
					edURL = "https://www.exploit-db.com/exploits/" + strings.TrimSpace(row[idxID])
				}
				entry, err = v.s.m.PG().CreateVulnEntry(db.VulnEntryInput{
					Category: db.VulnCategoryCVE,
					Title:    fmt.Sprintf("%s %s", cve, truncateRunes(desc0, 120)),
					CVEID:    cve,
					Source:   "cve_feed", Status: "active",
					Refs: vulnJSON([]string{edURL}),
				})
				if err != nil {
					log.Printf("[vulndb-sync] Exploit-DB 反向建档 %s 失败: %v", cve, err)
					continue
				}
			}
			file := strings.TrimSpace(row[idxFile])
			if file == "" {
				continue
			}
			desc := ""
			if idxDesc >= 0 && len(row) > idxDesc {
				desc = strings.TrimSpace(row[idxDesc])
			}
			kind := "exp"
			if idxType >= 0 && len(row) > idxType && strings.Contains(strings.ToLower(row[idxType]), "dos") {
				kind = "poc"
			}
			srcURL := "https://www.exploit-db.com/exploits/" + strings.TrimSuffix(file, ".txt")
			if idxID >= 0 && len(row) > idxID {
				srcURL = "https://www.exploit-db.com/exploits/" + strings.TrimSpace(row[idxID])
			}
			_, created, err := v.s.m.PG().CreateVulnPoc(db.VulnPocInput{
				EntryID: entry.ID, Name: fmt.Sprintf("Exploit-DB: %s", truncateRunes(desc, 80)),
				Kind: kind, Language: "text", Source: "exploitdb", SourceURL: srcURL,
				Content: fmt.Sprintf("Exploit-DB 条目:%s\n\n%s\n\n下载:%s", file, desc, srcURL),
			})
			if err != nil {
				log.Printf("[vulndb-sync] Exploit-DB PoC 入库失败(%s): %v", cve, err)
				continue
			}
			if created {
				pocs++
			}
		}
	}
	return pocs, nil
}

// syncGitHubPoCs 为近期高危 CVE 在 GitHub 搜 PoC 仓库。
// GitHub 未认证搜索限速 10 次/分钟,故只对少量最新 critical/high CVE 抽样。
func (v *vulnSync) syncGitHubPoCs(ctx context.Context) (pocs int, err error) {
	runID, err := v.s.m.PG().StartVulnSyncRun("github_poc")
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

	// 取最近的高危 CVE 条目(无 token 时最多 5 条避免触发 GitHub 限速;
	// 配置了 vulndb_github_token 则放大到 60 条,搜索配额 30 次/分钟)。
	token := ""
	if val, _, err := v.s.m.PG().GetSetting(settingVulnGitHubToken); err == nil {
		token = strings.TrimSpace(val)
	}
	sample := 5
	if token != "" {
		sample = 60
	}
	items, _, _, err := v.s.m.PG().ListVulnEntries(db.VulnEntryFilter{
		Category: db.VulnCategoryCVE, Sort: "updated", Limit: sample,
	})
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, vulnSyncTimeout)
	defer cancel()
	for _, e := range items {
		if e.CVEID == "" {
			continue
		}
		// 已有 PoC 的跳过,省配额。
		if e.PocCount > 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return pocs, ctx.Err()
		case <-time.After(2 * time.Second): // 温和节流
		}
		var result struct {
			Items []struct {
				FullName    string `json:"full_name"`
				HTMLURL     string `json:"html_url"`
				Description string `json:"description"`
				Language    string `json:"language"`
			} `json:"items"`
		}
		api := "https://api.github.com/search/repositories?q=" + url.QueryEscape(e.CVEID+" poc") + "&per_page=3&sort=stars"
		headers := map[string]string{}
		if token != "" {
			headers["Authorization"] = "Bearer " + token
		}
		if err := fetchJSON(ctx, api, headers, &result); err != nil {
			log.Printf("[vulndb-sync] GitHub PoC 搜索 %s 失败: %v", e.CVEID, err)
			continue
		}
		for _, repo := range result.Items {
			_, created, err := v.s.m.PG().CreateVulnPoc(db.VulnPocInput{
				EntryID: e.ID, Name: "GitHub: " + repo.FullName, Kind: "poc",
				Language: repo.Language, Source: "github", SourceURL: repo.HTMLURL,
				Content: fmt.Sprintf("仓库:%s\n\n%s\n\n%s", repo.FullName, repo.Description, repo.HTMLURL),
			})
			if err != nil {
				continue
			}
			if created {
				pocs++
			}
		}
	}
	return pocs, nil
}

func vulnJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// edbpublishedBefore 判断 Exploit-DB 行的 date_published 列是否早于给定年份
// (老洞反向建档的判据)。列缺失/解析失败一律 false:宁可漏一条也不误建档。
func edbpublishedBefore(idxDate int, row []string, year int) bool {
	if idxDate < 0 || len(row) <= idxDate {
		return false
	}
	s := strings.TrimSpace(row[idxDate])
	if len(s) < 4 {
		return false
	}
	y, err := strconv.Atoi(s[:4])
	if err != nil {
		return false
	}
	return y < year
}

// truncateRunes 按字符截断(避免多字节被切断)。
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
