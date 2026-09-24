package recon

// SubdomainBrute 字典爆破收集器:读用户上传的子域字典(「字典库」上传),
// 拼 <word>.<domain> 并发 DNS 解析,命中产出为已验证子域。
// 内置泛解析检测:先解析随机探针子域,命中泛解析 IP 集的候选全部丢弃——
// 对齐信息搜集假阳性纪律。
//
// 安全边界:DNS 查询目标 = 授权根域名字面拼接,逐词过 sanitizeDomain 白名单;
// 并发量、单条超时、总量、产出量全部有上限;不产生任何 HTTP 流量。
// 字典文件路径由 server 层经 Secrets["wordbook_path"] 注入(按 wordbook_id 从
// 字典库解析),收集器不感知存储布局。

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// ---- 参数上限 ----

const (
	BruteMaxWords      = 200000 // 单次爆破候选词上限(防超大字典拖爆)
	BruteConcurrency   = 50     // DNS 并发数
	BruteQueryTimeout  = 3      // 单条解析超时(秒)
	BruteOverallMaxSec = 600    // 整体上限(秒)
	BruteMaxHits       = 5000   // 命中产出上限
	WildcardProbes     = 3      // 泛解析探针数
)

// SubdomainBruteCollector 字典爆破收集器。
// config: {domain?(留空=企业范围第一个根域名), wordbook_id(必填,字典库 id), size?(产出上限)}
type SubdomainBruteCollector struct{}

func (SubdomainBruteCollector) Key() string         { return "subdomain_brute" }
func (SubdomainBruteCollector) Title() string       { return "子域字典爆破 (DNS)" }
func (SubdomainBruteCollector) Kind() CollectorKind { return KindTool }

// ReadWordbookLines 读取字典文件为词列表(去空行/注释/首尾空白,小写归一,去重,限上限)。
// 返回 (词表, 文件总行数, 错误)。
func ReadWordbookLines(path string, limit int) ([]string, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	content := make([]byte, 0, 1<<20)
	buf := make([]byte, 64*1024)
	for {
		n, err := f.Read(buf)
		content = append(content, buf[:n]...)
		if err != nil || len(content) > 256<<20 {
			break
		}
	}
	lines := strings.Split(string(content), "\n")
	seen := map[string]bool{}
	var words []string
	for _, line := range lines {
		w := strings.ToLower(strings.TrimSpace(line))
		w = strings.TrimSuffix(w, ".")
		if w == "" || strings.HasPrefix(w, "#") {
			continue
		}
		// 词必须是合法主机名标签(允许下划线,部分 DNS 记录用 _dmarc 等)。
		if !validWordLabel(w) {
			continue
		}
		if !seen[w] {
			seen[w] = true
			words = append(words, w)
		}
		if limit > 0 && len(words) >= limit {
			break
		}
	}
	return words, len(lines), nil
}

func validWordLabel(w string) bool {
	if w == "" {
		return false
	}
	for _, r := range w {
		ok := r == '_' || r == '-' || r == '.' ||
			(r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return len(w) <= 253
}

// randomLabels 生成 n 个随机主机名标签(泛解析探针用)。
func randomLabels(n int) []string {
	out := make([]string, 0, n)
	buf := make([]byte, 12)
	for i := 0; i < n; i++ {
		if _, err := rand.Read(buf[:8]); err != nil {
			out = append(out, fmt.Sprintf("artex-probe-%d-x7qz", i))
			continue
		}
		out = append(out, fmt.Sprintf("%x", buf[:8]))
	}
	return out
}

// detectWildcard 探测泛解析:解析 count 个随机子域,若全部解析成功则泛解析成立,
// 返回出现过的 IP 集合;任一探针 NXDOMAIN 即无泛解析,返回 nil。
func detectWildcard(ctx context.Context, resolver *net.Resolver, domain string, count int) map[string]bool {
	ipSeen := map[string]bool{}
	for _, label := range randomLabels(count) {
		probe := label + "." + domain
		ips, err := resolver.LookupHost(ctx, probe)
		if err != nil || len(ips) == 0 {
			return nil
		}
		for _, ip := range ips {
			ipSeen[ip] = true
		}
	}
	if len(ipSeen) == 0 {
		return nil
	}
	return ipSeen
}

func (SubdomainBruteCollector) Collect(ctx context.Context, in CollectorInput) (*CollectorResult, error) {
	domain := configString(in.Config, "domain")
	if domain == "" {
		roots := companyRootDomains(in.Companies)
		if len(roots) == 0 {
			return nil, fmt.Errorf("subdomain_brute 需要目标域名:填 domain 或关联带根域名范围的企业")
		}
		domain = roots[0]
	}
	domain, ok := sanitizeDomain(domain)
	if !ok {
		return nil, fmt.Errorf("无效域名: %q", domain)
	}

	// 字典路径:server 层经 resolveWordbookPath 注入到 Config(从字典库按 id 解析)。
	wbPath := configString(in.Config, "wordbook_path")
	if wbPath == "" {
		return nil, fmt.Errorf("需要字典:config 提供 wordbook_id(在「字典库」上传后选用)")
	}
	words, totalLines, err := ReadWordbookLines(wbPath, BruteMaxWords)
	if err != nil {
		return nil, fmt.Errorf("读字典失败: %v", err)
	}
	if len(words) == 0 {
		return nil, fmt.Errorf("字典有效词为空(检查文件内容/编码)")
	}

	resolver := net.DefaultResolver
	// 泛解析探针(在枚举前先做)。
	wildIPs := detectWildcard(ctx, resolver, domain, WildcardProbes)

	var (
		mu       sync.Mutex
		hits     []RawAsset
		wildHits int
	)
	sem := make(chan struct{}, BruteConcurrency)
	ctx2, cancel := context.WithTimeout(ctx, BruteOverallMaxSec*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for _, w := range words {
		select {
		case <-ctx2.Done():
		case sem <- struct{}{}:
		}
		if ctx2.Err() != nil {
			break
		}
		wg.Add(1)
		go func(word string) {
			defer wg.Done()
			defer func() { <-sem }()
			host := word + "." + domain
			qctx, qcancel := context.WithTimeout(ctx2, BruteQueryTimeout*time.Second)
			defer qcancel()
			ips, err := resolver.LookupHost(qctx, host)
			if err != nil || len(ips) == 0 {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, ip := range ips {
				if wildIPs[ip] {
					wildHits++
					return
				}
			}
			if len(hits) < BruteMaxHits {
				hits = append(hits, RawAsset{Type: TypeDomain, Value: strings.ToLower(host)})
			}
		}(w)
	}
	wg.Wait()
	cancel()

	rawSummary := fmt.Sprintf("domain=%s words=%d hits=%d wildcard=%v wild_filtered=%d", domain, len(words), len(hits), wildIPs != nil, wildHits)
	rawHash, _ := in.RawSink([]byte(rawSummary))
	return &CollectorResult{
		Assets:  hits,
		RawHash: rawHash,
		Stats: map[string]any{
			"domain":        domain,
			"words":         len(words),
			"total_lines":   totalLines,
			"total":         len(hits),
			"wildcard":      wildIPs != nil,
			"wild_filtered": wildHits,
		},
	}, nil
}

// strconvReserved 区域已清理。

func init() {
	Register(SubdomainBruteCollector{})
}
