package server

// CNVD 数据端点采集(可配置):CNVD 官网(cnvd.org.cn)对自动化访问启用了两级
// 加速乐 JS 挑战 + 验证码,纯 HTTP 客户端无法稳定直连(2026-09-26 实测,一阶段
// cookie 挑战可解、二阶段混淆挑战需真实 DOM),也没有可靠公开镜像;官方结构化
// 数据只对注册合作单位开放。因此本源做成「可配置数据端点」:
//
//	设置键 vulndb_cnvd_url 指向任意返回 JSON 数组的 http(s) 端点,形如:
//	[{ "cnvd": "CNVD-2026-123456", "title": "...", "severity": "高",
//	   "date": "2026-09-26", "description": "...", "url": "https://..." }]
//	典型用法:官方会员 API(注册合作单位获得)、部署在可过挑战出口上的自建镜像。
//
// 未配置时本源静默跳过(不写采集记录,情报源页签不出现);CNVD 情报仍可持续从
// 文章源(vulndb_articles.go 的 CNVD 编号提取)入库,只是覆盖面小一些。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

// settingVulnCNVDFeedURL CNVD 数据端点地址(系统设置;空 = 未启用)。
const settingVulnCNVDFeedURL = "vulndb_cnvd_url"

// cnvdFeedFetch 拉取数据端点。与通用 fetchJSON 的差异:**不做 SSRF 公网校验**——
// 本端点是管理员在设置里显式配置的可信地址,典型部署形态恰恰是本机/内网的采集
// 代理(tools/cnvd_fetch.py),必须允许环回与内网地址;其余约束(超时、响应体
// 上限、429/状态码检查)与通用实现同等强度。仅限此处,勿扩散。
func cnvdFeedFetch(ctx context.Context, rawURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ARTEX-VulnDB/1.0")
	req.Header.Set("Accept", "application/json")
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// cnvdFeedEntry 是数据端点 JSON 数组的一个元素。字段全部宽容解析:
// severity 支持中文(高/中/低)与英文,cnvd/title 必填,其余可选。
type cnvdFeedEntry struct {
	CNVD        string `json:"cnvd"`
	Title       string `json:"title"`
	Severity    string `json:"severity"`
	Date        string `json:"date"`
	Description string `json:"description"`
	URL         string `json:"url"`
}

// syncCNVDFeed 从配置的数据端点拉取 CNVD 漏洞列表入库(category=cnvd)。
// 返回本轮新建条目数;未配置端点返回 (0, nil) 且不写采集记录。
func (v *vulnSync) syncCNVDFeed(ctx context.Context) (created int, err error) {
	feedURL := ""
	if val, _, err := v.s.m.PG().GetSetting(settingVulnCNVDFeedURL); err == nil {
		feedURL = strings.TrimSpace(val)
	}
	if feedURL == "" {
		return 0, nil
	}

	runID, err := v.s.m.PG().StartVulnSyncRun("cnvd_feed")
	if err != nil {
		return 0, err
	}
	defer func() {
		status := "done"
		errMsg := ""
		if err != nil {
			status, errMsg = "failed", err.Error()
		}
		_ = v.s.m.PG().FinishVulnSyncRun(runID, status, created, created, 0, 0, errMsg)
	}()

	var entries []cnvdFeedEntry
	if err = cnvdFeedFetch(ctx, feedURL, &entries); err != nil {
		return 0, fmt.Errorf("拉取 CNVD 数据端点失败: %w", err)
	}
	log.Printf("[vulndb-sync] CNVD 数据端点返回 %d 条", len(entries))

	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return created, ctx.Err()
		}
		id := strings.ToUpper(strings.TrimSpace(e.CNVD))
		if !validCNVDID(id) {
			continue // 无编号/坏编号的行跳过(宁可漏收不误收)
		}
		n, uerr := v.upsertCNVDEntry(id, e)
		if uerr != nil {
			log.Printf("[vulndb-sync] CNVD %s 入库失败: %v", id, uerr)
			continue
		}
		created += n
	}
	return created, nil
}

// upsertCNVDEntry 按编号入库/补 ref(与文章源 CNVD 提取共用同一套判重规则:
// 标题 = 编号 + 空格 + 描述,FindVulnEntryByTitlePrefix 前缀匹配 + 精确复核)。
func (v *vulnSync) upsertCNVDEntry(id string, e cnvdFeedEntry) (n int, err error) {
	entry, err := v.s.m.PG().FindVulnEntryByTitlePrefix(id)
	if err != nil {
		return 0, err
	}
	if entry != nil && !strings.HasPrefix(entry.Title, id+" ") {
		entry = nil // 前缀撞上更长编号:视为不存在
	}
	if entry != nil {
		if u := strings.TrimSpace(e.URL); u != "" && !articleHasRef(entry.Refs, u) {
			if err := v.s.m.PG().MergeVulnRefs(entry.ID, []string{u}); err != nil {
				return 0, err
			}
		}
		return 0, nil
	}
	title := strings.TrimSpace(id + " " + truncateRunes(strings.TrimSpace(e.Title), articleTitleMax))
	if title == id {
		return 0, fmt.Errorf("标题为空") // 无标题的裸编号不入库(检索体验太差)
	}
	refs := []string{}
	if u := strings.TrimSpace(e.URL); u != "" {
		refs = append(refs, u)
	}
	_, err = v.s.m.PG().CreateVulnEntry(db.VulnEntryInput{
		Category:    db.VulnCategoryCNVD,
		Title:       title,
		Description: truncateRunes(e.Description, articleDescMax),
		Severity:    cnvdNormalizeSeverity(e.Severity),
		Source:      "cve_feed",
		Status:      "active",
		Refs:        vulnJSON(refs),
		Tags:        vulnJSON([]string{"CNVD"}),
	})
	if err != nil {
		return 0, err
	}
	return 1, nil
}

// cnvdNormalizeSeverity 端点严重度字段 → 库内统一取值(高/中/低与英文都认)。
func cnvdNormalizeSeverity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "高", "high", "critical":
		return "high"
	case "中", "medium", "moderate":
		return "medium"
	case "低", "low":
		return "low"
	}
	return ""
}

// validCNVDID 校验 CNVD 编号形状(CNVD-年份-序列号)。
func validCNVDID(id string) bool {
	if !strings.HasPrefix(id, "CNVD-") {
		return false
	}
	parts := strings.Split(id, "-")
	if len(parts) != 3 || len(parts[1]) != 4 || len(parts[2]) < 4 || len(parts[2]) > 7 {
		return false
	}
	for _, p := range parts[1:] {
		for _, b := range []byte(p) {
			if b < '0' || b > '9' {
				return false
			}
		}
	}
	return true
}
