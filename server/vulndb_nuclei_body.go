package server

// nuclei 模板正文回填(契约 POC_INTEL_CONTRACTS.md §6,Agent C 所有)。
//
// 背景:vulndb_pocsources.go 的 syncNucleiTemplates 把 nuclei 官方模板以
// source="nuclei" 的 PoC 行挂进库,但历史行 content 只有路径提示文字。本文件
// 把这些行的正文(模板 YAML)真正补齐:
//
//   - 目标行 = vulndb_pocs 里 source='nuclei' 且 content 为空的行(本文件内
//     只读 SELECT 经 s.m.PG();写入只走冻结的 db.SetVulnPocContent——幂等
//     UPDATE,行被删时补建最小行)。
//   - 下载走 raw.githubusercontent.com(不占 GitHub API 配额),200ms/请求,
//     429/5xx 指数退避,单请求 30s 上限,整轮总预算 2h。
//   - 光标存 settings(vulndb_nuclei_body_cursor,按 pocs.id 升序推进),
//     进程重启后 30s 由 nucleiBodyWorker 自动续跑;取消=内存 flag 触发 ctx。
//
// PocBackfillStatus 是 nuclei/trickest 回填共形的状态 wire 结构(前端同一组件渲染)。

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/recon"
)

const (
	// settingNucleiBodyCursor 断点续跑光标的 settings 键(JSON,见 nucleiBodyCursor)。
	settingNucleiBodyCursor = "vulndb_nuclei_body_cursor"
	// nucleiRawBaseURL 模板正文的 raw 下载根(路径拼在后面;raw 域名不占 API 配额)。
	nucleiRawBaseURL = "https://raw.githubusercontent.com/projectdiscovery/nuclei-templates/main/"
	// nucleiBodyReqInterval 相邻请求间隔(5 req/s,对 raw CDN 温和)。
	nucleiBodyReqInterval = 200 * time.Millisecond
	// nucleiBodyReqTimeout 单请求超时。
	nucleiBodyReqTimeout = 30 * time.Second
	// nucleiBodyTotalBudget 整轮回填总预算;到点优雅停,光标保留可续跑。
	nucleiBodyTotalBudget = 2 * time.Hour
	// nucleiBodyAutoResumeDelay 服务启动后自动续跑前的等待(避开启动期争抢)。
	nucleiBodyAutoResumeDelay = 30 * time.Second
	// nucleiBodyMaxBodyBytes 模板单文件通常几十 KB,4MB 上限防异常响应拖爆内存。
	nucleiBodyMaxBodyBytes = 4 << 20
	// nucleiBodyMaxAttempts 单行下载的最大尝试轮数(429/5xx/网络错误指数退避重试)。
	nucleiBodyMaxAttempts = 5
	// nucleiBodyMaxConsecutiveFails 连续失败行数上限:超过视为上游/库不可用,
	// 中止本轮(光标停在最后成功行),避免一次网络故障把整个队列空烧一遍。
	nucleiBodyMaxConsecutiveFails = 20
	// nucleiBodyBatchSize 每批读取的目标行数(光标按行推进,分批常驻内存可控)。
	nucleiBodyBatchSize = 500
)

// 回填全局单例状态(同一时刻只允许一个实例在跑;进度是内存态,重启清零,
// 断点续跑的持久事实只看 settings 光标)。
var (
	nucleiBodyMu       sync.Mutex
	nucleiBodyRunning  bool
	nucleiBodyCancel   context.CancelFunc
	nucleiBodyProgress PocBackfillStatus
)

// PocBackfillStatus 回填作业状态(GET /api/vulndb/{nuclei|trickest}-backfill 响应体)。
// 冻结结构,字段不得改动(trickest 回填与前端组件共形)。
type PocBackfillStatus struct {
	Running    bool   `json:"running"`     // 作业进行中
	Done       bool   `json:"done"`        // 光标推进完成(全部处理过)
	Processed  int    `json:"processed"`   // 已处理条数(nuclei=模板行数,trickest=条目数)
	Matched    int    `json:"matched"`     // 命中数(nuclei=正文写入行数,trickest=挂载 PoC 行数)
	Error      string `json:"error"`       // 最后一次错误(空=无)
	StartedAt  string `json:"started_at"`  // RFC3339;无作业为空
	FinishedAt string `json:"finished_at"` // RFC3339;未结束为空
}

// nucleiBodyCursor settings 键 vulndb_nuclei_body_cursor 的 JSON 值;
// 字段名是冻结契约({done, last_id}),不得改动。last_id=已处理的最大 pocs.id。
type nucleiBodyCursor struct {
	Done   bool  `json:"done"`
	LastID int64 `json:"last_id"`
}

// nucleiBodyCursorRe 截断/半行写入的 JSON 兜底解析:进程在 SetSetting 中途
// 崩溃可能留下残缺 JSON,严格解析失败时按正则抠出 last_id/done,光标不丢。
var (
	nucleiBodyCursorLastRe = regexp.MustCompile(`"last_id"\s*:\s*(\d+)`)
	nucleiBodyCursorDoneRe = regexp.MustCompile(`"done"\s*:\s*(true|false)`)
)

// nucleiBodyParseCursor 解析光标字符串(纯函数):空串返回 nil;严格 JSON 优先,
// 失败时正则兜底残缺写入;完全无法解读返回 nil(调用方按全新回填处理)。
func nucleiBodyParseCursor(val string) *nucleiBodyCursor {
	val = strings.TrimSpace(val)
	if val == "" {
		return nil
	}
	var c nucleiBodyCursor
	if err := json.Unmarshal([]byte(val), &c); err == nil {
		return &c
	}
	// 截断兜底:能抠出多少抠多少(缺省 last_id=0/done=false 与零值语义一致)。
	salvaged := &nucleiBodyCursor{}
	if m := nucleiBodyCursorLastRe.FindStringSubmatch(val); m != nil {
		if id, perr := strconv.ParseInt(m[1], 10, 64); perr == nil {
			salvaged.LastID = id
		}
	}
	if m := nucleiBodyCursorDoneRe.FindStringSubmatch(val); m != nil {
		salvaged.Done = m[1] == "true"
	}
	return salvaged
}

// nucleiBodyLoadCursor 读取光标;不存在或不可解读返回 (nil, nil)。
func nucleiBodyLoadCursor(pg *db.DB) (*nucleiBodyCursor, error) {
	val, ok, err := pg.GetSetting(settingNucleiBodyCursor)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return nucleiBodyParseCursor(val), nil
}

// nucleiBodySaveCursor 写入光标(每处理完一行推进一次,续跑粒度=单行)。
func nucleiBodySaveCursor(pg *db.DB, c *nucleiBodyCursor) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return pg.SetSetting(settingNucleiBodyCursor, string(b))
}

// nucleiBodyAdvance 光标推进(纯函数):处理完 id 行后置 LastID=id。只前进
// 不回退(防御异常数据把光标拨回去造成重复回扫)。
func nucleiBodyAdvance(cur *nucleiBodyCursor, id int64) {
	if id > cur.LastID {
		cur.LastID = id
	}
}

// nucleiBodyResumeCursor 启动时的续跑决策(纯函数):存在未完成光标 → 原样
// 续跑;done 或无光标 → 零值光标全量重扫(目标查询只捞 content 为空的行,
// 重扫代价极小)。
func nucleiBodyResumeCursor(cur *nucleiBodyCursor) *nucleiBodyCursor {
	if cur != nil && !cur.Done {
		return cur
	}
	return &nucleiBodyCursor{}
}

// ---- 模板路径归一(纯函数,单测覆盖) ----

var (
	// nucleiBlobURLPattern 网页端永久链接(入库 SourceURL 统一用这个前缀)。
	nucleiBlobURLPattern = regexp.MustCompile(`^https://github\.com/projectdiscovery/nuclei-templates/(?:blob|raw)/[^/]+/(.+)$`)
	// nucleiRawURLPattern raw 直链(历史数据/手工数据兜底)。
	nucleiRawURLPattern = regexp.MustCompile(`^https://raw\.githubusercontent\.com/projectdiscovery/nuclei-templates/[^/]+/(.+)$`)
	// nucleiContentPathPattern content 提示文字里的模板路径(两段来源都失败时的最后兜底)。
	nucleiContentPathPattern = regexp.MustCompile(`http/cves/[A-Za-z0-9_./-]*CVE-\d{4}-\d{4,7}\.yaml`)
)

// nucleiValidTemplatePath 校验路径形如 http/cves/…(…/)CVE-<year>-<id>.yaml
// (复用 nucleiTreeEntryPattern,与树解析同一正则口径)。
func nucleiValidTemplatePath(p string) bool {
	return strings.HasPrefix(p, "http/cves/") && nucleiTreeEntryPattern.MatchString(p)
}

// nucleiTemplatePathFromSource 从 PoC 行归一模板路径(纯函数):source_url 的
// blob/raw 链接优先(权威),失败再从 content 提示文字里抠;都失败返回 ""。
func nucleiTemplatePathFromSource(sourceURL, content string) string {
	u := strings.TrimSpace(sourceURL)
	if parsed, err := url.Parse(u); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		// 剥掉 query/fragment,只留 scheme://host/path 供正则匹配。
		u = parsed.Scheme + "://" + parsed.Host + parsed.Path
	}
	for _, re := range []*regexp.Regexp{nucleiBlobURLPattern, nucleiRawURLPattern} {
		if m := re.FindStringSubmatch(u); m != nil && nucleiValidTemplatePath(m[1]) {
			return m[1]
		}
	}
	if m := nucleiContentPathPattern.FindString(content); m != "" {
		return m
	}
	return ""
}

// ---- 下载(带退避;出网走带 SSRF 校验的专用客户端) ----

// nucleiBodyHTTPClient 回填专用 HTTP 客户端(30s 单请求上限)。代理池/流量录制
// 开启时按约定走 s.m.ProxyAddr() 出网,并信任录制代理的 MITM CA(同 customtool
// 的 httpProxyTransport 模式);直连时与既有采集器一致。
func (s *Server) nucleiBodyHTTPClient() *http.Client {
	client := &http.Client{Timeout: nucleiBodyReqTimeout}
	addr := strings.TrimSpace(s.m.ProxyAddr())
	if addr == "" {
		return client
	}
	pu, err := url.Parse("http://" + strings.TrimPrefix(strings.TrimPrefix(addr, "http://"), "https://"))
	if err != nil {
		return client
	}
	tr := &http.Transport{Proxy: http.ProxyURL(pu)}
	if caFile := s.m.ProxyCACert(); caFile != "" {
		if pem, rerr := os.ReadFile(caFile); rerr == nil {
			pool := x509.NewCertPool()
			if pool.AppendCertsFromPEM(pem) {
				tr.TLSClientConfig = &tls.Config{RootCAs: pool}
			}
		}
	}
	client.Transport = tr
	return client
}

// nucleiBodyPace 请求间限速等待(200ms);ctx 取消立即返回。
func nucleiBodyPace(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(nucleiBodyReqInterval):
		return nil
	}
}

// nucleiBodyFetchOnce 单次下载模板正文:先过公网地址校验(拒绝内网/环回),
// 非 200 返回 *pocorexpHTTPError(404 被 callers 用来区分「上游已删除」)。
func nucleiBodyFetchOnce(ctx context.Context, client *http.Client, rawURL string) ([]byte, error) {
	if _, err := recon.ValidatePublicURL(rawURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ARTEX-VulnDB/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &pocorexpHTTPError{Status: resp.StatusCode}
	}
	return io.ReadAll(io.LimitReader(resp.Body, nucleiBodyMaxBodyBytes))
}

// nucleiBodyFetchRetry 带退避的模板下载:429/5xx/网络错误指数退避重试
// (400ms 起,翻倍),404(上游已删)与 401/403(重试无意义)立即返回。
func nucleiBodyFetchRetry(ctx context.Context, client *http.Client, rawURL string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < nucleiBodyMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(nucleiBodyReqInterval * time.Duration(1<<attempt)):
			}
		}
		body, err := nucleiBodyFetchOnce(ctx, client, rawURL)
		if err == nil {
			return body, nil
		}
		lastErr = err
		var httpErr *pocorexpHTTPError
		if errors.As(err, &httpErr) {
			if httpErr.Status == http.StatusNotFound || httpErr.Status == http.StatusUnauthorized || httpErr.Status == http.StatusForbidden {
				return nil, err
			}
		}
	}
	return nil, fmt.Errorf("重试 %d 轮后仍失败: %w", nucleiBodyMaxAttempts, lastErr)
}

// ---- 目标行读取(只读 SELECT,经 s.m.PG()) ----

// nucleiBodyRow 一条待回填的目标行(只取回填所需列,不碰大字段之外的东西)。
type nucleiBodyRow struct {
	ID        int64
	EntryID   int64
	SourceURL string
	Content   string
}

// nucleiBodyTargetRows 读一批待回填行:source='nuclei' 且 content 为空、
// id 大于 afterID,按 id 升序(光标推进顺序)。只读,不写库。
func nucleiBodyTargetRows(pg *db.DB, afterID int64, limit int) ([]*nucleiBodyRow, error) {
	rows, err := pg.Query(`SELECT p.id, p.entry_id, COALESCE(p.source_url, ''), COALESCE(p.content, '')
		FROM vulndb_pocs p
		WHERE p.source = 'nuclei' AND (p.content = '' OR p.content IS NULL) AND p.id > $1
		ORDER BY p.id ASC LIMIT $2`, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*nucleiBodyRow, 0, limit)
	for rows.Next() {
		r := &nucleiBodyRow{}
		if err := rows.Scan(&r.ID, &r.EntryID, &r.SourceURL, &r.Content); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- 回填主流程 ----

// nucleiBodyTryLaunch 尝试占用回填单例并启动运行协程;已在跑返回 false。
// ctx 必须是服务级 context(如 s.ctx),不能是 HTTP 请求级(请求结束会取消)。
func (s *Server) nucleiBodyTryLaunch(ctx context.Context) bool {
	nucleiBodyMu.Lock()
	if nucleiBodyRunning {
		nucleiBodyMu.Unlock()
		return false
	}
	nucleiBodyRunning = true
	nucleiBodyMu.Unlock()

	go func() {
		defer func() {
			nucleiBodyMu.Lock()
			nucleiBodyRunning = false
			nucleiBodyCancel = nil
			nucleiBodyMu.Unlock()
		}()
		runCtx, cancel := context.WithTimeout(ctx, nucleiBodyTotalBudget)
		defer cancel()
		nucleiBodyMu.Lock()
		nucleiBodyCancel = cancel
		nucleiBodyProgress = PocBackfillStatus{StartedAt: time.Now().UTC().Format(time.RFC3339)}
		nucleiBodyMu.Unlock()

		// 存在未完成光标 → 断点续跑(按 pocs.id 升序推进,幂等);done/无光标
		// → 全量重扫(content 为空的行才进队列,重扫代价极小)。
		var resume *nucleiBodyCursor
		if cur, err := nucleiBodyLoadCursor(s.m.PG()); err != nil {
			log.Printf("[nuclei-body] 读取续跑光标失败: %v", err)
		} else if cur != nil && !cur.Done {
			resume = cur
		}
		s.nucleiBodyExecute(runCtx, resume)
	}()
	return true
}

// nucleiBodyExecute 回填主流程:按批读目标行,逐行归一路径→raw 下载 YAML→
// SetVulnPocContent 写正文→推进光标。取消/超时/连续失败都把光标停在最后
// 处理完的行上,等待续跑。
func (s *Server) nucleiBodyExecute(ctx context.Context, resume *nucleiBodyCursor) {
	pg := s.m.PG()
	cur := nucleiBodyResumeCursor(resume)
	cur.Done = false
	if err := nucleiBodySaveCursor(pg, cur); err != nil {
		log.Printf("[nuclei-body] 写入光标失败: %v", err)
	}
	log.Printf("[nuclei-body] 回填启动: last_id=%d", cur.LastID)

	client := s.nucleiBodyHTTPClient()
	setProgress := func(f func(st *PocBackfillStatus)) {
		nucleiBodyMu.Lock()
		f(&nucleiBodyProgress)
		nucleiBodyMu.Unlock()
	}
	finish := func(errMsg string) {
		setProgress(func(st *PocBackfillStatus) {
			st.Error = errMsg
			st.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		})
	}

	consecutiveFails := 0
	for {
		if err := ctx.Err(); err != nil {
			finish("") // 优雅停:光标停在最后处理完的行,不算错误
			return
		}
		rows, err := nucleiBodyTargetRows(pg, cur.LastID, nucleiBodyBatchSize)
		if err != nil {
			log.Printf("[nuclei-body] 读取目标行失败: %v", err)
			finish("读取目标行失败: " + err.Error())
			return
		}
		if len(rows) == 0 {
			cur.Done = true
			if err := nucleiBodySaveCursor(pg, cur); err != nil {
				log.Printf("[nuclei-body] 写入光标失败: %v", err)
			}
			nucleiBodyMu.Lock()
			done := PocBackfillStatus{
				Running:    false,
				Done:       true,
				Processed:  nucleiBodyProgress.Processed,
				Matched:    nucleiBodyProgress.Matched,
				StartedAt:  nucleiBodyProgress.StartedAt,
				FinishedAt: time.Now().UTC().Format(time.RFC3339),
			}
			nucleiBodyProgress = done
			nucleiBodyMu.Unlock()
			log.Printf("[nuclei-body] 回填完成: processed=%d matched=%d last_id=%d",
				done.Processed, done.Matched, cur.LastID)
			return
		}
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				finish("")
				return
			}
			setProgress(func(st *PocBackfillStatus) { st.Processed++ })
			matched, failed := s.nucleiBodyProcessRow(ctx, pg, client, row)
			if matched {
				setProgress(func(st *PocBackfillStatus) { st.Matched++ })
			}
			// 每行推进一次光标(续跑粒度=单行;写失败也不能丢进度)。
			nucleiBodyAdvance(cur, row.ID)
			if err := nucleiBodySaveCursor(pg, cur); err != nil {
				log.Printf("[nuclei-body] 写入光标失败: %v", err)
			}
			if failed {
				consecutiveFails++
				if consecutiveFails >= nucleiBodyMaxConsecutiveFails {
					msg := fmt.Sprintf("连续 %d 行下载失败,中止本轮等续跑", consecutiveFails)
					log.Printf("[nuclei-body] %s", msg)
					finish(msg)
					return
				}
			} else {
				consecutiveFails = 0
			}
		}
	}
}

// nucleiBodyProcessRow 处理单行:归一路径 → 下载 → 写正文。返回
// (matched, failed):matched=正文实际写入(计入 matched 计数);failed=瞬时
// 故障(网络/库错误,计入连续失败,可中止整轮)。确定跳过的行(404/无路径/
// 空响应)返回 (false,false)——不算故障也不算命中。写入只走冻结的
// SetVulnPocContent(幂等)。
func (s *Server) nucleiBodyProcessRow(ctx context.Context, pg *db.DB, client *http.Client, row *nucleiBodyRow) (matched, failed bool) {
	path := nucleiTemplatePathFromSource(row.SourceURL, row.Content)
	if path == "" {
		log.Printf("[nuclei-body] 行 %d 无法归一模板路径(source_url=%q),跳过", row.ID, row.SourceURL)
		return false, false
	}
	if err := nucleiBodyPace(ctx); err != nil {
		return false, false // ctx 已取消:外层按暂停收尾,不计失败
	}
	body, err := nucleiBodyFetchRetry(ctx, client, nucleiRawBaseURL+path)
	if err != nil {
		var httpErr *pocorexpHTTPError
		if errors.As(err, &httpErr) && httpErr.Status == http.StatusNotFound {
			log.Printf("[nuclei-body] 行 %d 模板上游已删除(%s),跳过", row.ID, path)
			return false, false
		}
		if ctx.Err() != nil {
			return false, false // 取消/超时:不计为行失败,外层统一按暂停处理
		}
		log.Printf("[nuclei-body] 行 %d 下载失败(%s): %v", row.ID, path, err)
		return false, true
	}
	content := string(body)
	if strings.TrimSpace(content) == "" {
		// 空响应不写库(写了空串行仍为空,会被反复重扫);跳过由光标兜住。
		log.Printf("[nuclei-body] 行 %d 模板响应为空(%s),跳过", row.ID, path)
		return false, false
	}
	ok, err := pg.SetVulnPocContent(row.EntryID, row.SourceURL, content, "yaml")
	if err != nil {
		log.Printf("[nuclei-body] 行 %d 写入正文失败: %v", row.ID, err)
		return false, true
	}
	if !ok {
		log.Printf("[nuclei-body] 行 %d 正文写入未生效(entry 不存在)", row.ID)
	}
	return true, false
}

// nucleiBodyWorker 服务启动时的自动续跑协程(server.go 冻结,由
// registerNucleiBackfillRoutes 拉起):睡 30 秒避开启动期争抢;仅当光标存在
// 且未完成、且采集开关未关闭时才续跑,否则一次性退出(手动触发走 POST API)。
func (s *Server) nucleiBodyWorker(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(nucleiBodyAutoResumeDelay):
	}
	cur, err := nucleiBodyLoadCursor(s.m.PG())
	if err != nil {
		log.Printf("[nuclei-body] 读取续跑光标失败: %v", err)
		return
	}
	if cur == nil || cur.Done {
		return // 无未完成光标:不启动
	}
	if val, _, _ := s.m.PG().GetSetting(settingVulnDBSyncOn); val == "false" {
		return // 采集总开关被关闭:不续跑
	}
	log.Printf("[nuclei-body] 发现未完成光标,自动续跑: last_id=%d", cur.LastID)
	if !s.nucleiBodyTryLaunch(ctx) {
		log.Printf("[nuclei-body] 回填已在运行中,跳过自动续跑")
	}
}

// nucleiBodyStatus 返回 nuclei 正文回填状态:running/计数来自内存(本次进程),
// done 补自持久光标(跨重启可见,重启后计数归零属预期)。
func (s *Server) nucleiBodyStatus() PocBackfillStatus {
	nucleiBodyMu.Lock()
	st := nucleiBodyProgress
	running := nucleiBodyRunning
	nucleiBodyMu.Unlock()
	if running {
		st.Running = true
		st.Done = false
		return st
	}
	st.Running = false
	if cur, err := nucleiBodyLoadCursor(s.m.PG()); err == nil && cur != nil {
		st.Done = cur.Done
	}
	return st
}
