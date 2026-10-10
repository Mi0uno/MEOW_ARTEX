package storagecenter

// S3 兼容客户端(path-style,手写 SigV4——零 SDK 依赖,通吃 AWS/MinIO/OSS/COS/R2)。
// 载荷签名一律 UNSIGNED-PAYLOAD(上传流式免缓冲;各家 S3 兼容实现均支持)。

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

type s3Backend struct {
	cfg    Config
	client *http.Client
}

func newS3Backend(cfg Config, client *http.Client) (Backend, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("s3: 桶名不能为空")
	}
	endpoint, err := NormalizeEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	cfg.Endpoint = endpoint
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	return &s3Backend{cfg: cfg, client: client}, nil
}

// ---- SigV4 签名 ----

// uriEncode RFC3986 路径编码(保留字符 A-Za-z0-9-._~;keepSlash 时保留 /)。
func uriEncode(s string, keepSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '.', c == '_', c == '~':
			b.WriteByte(c)
		case c == '/' && keepSlash:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// canonicalQuery 构造排序后的规范化查询串(键值都编码,+ 归一 %20,键排序)。
func canonicalQuery(q url.Values) string {
	pairs := make([]string, 0, len(q))
	for k, vs := range q {
		ek := uriEncode(k, false)
		for _, v := range vs {
			pairs = append(pairs, ek+"="+uriEncode(v, false))
		}
	}
	sort.Strings(pairs)
	return strings.Join(pairs, "&")
}

// buildCanonicalRequest 按 SigV4 规范拼规范化请求串。headers 键必须已小写,
// 本函数负责排序与值 trim;payloadHash 用 UNSIGNED-PAYLOAD 或空串哈希。
func buildCanonicalRequest(method, canonicalURI, canonicalQ string,
	headers map[string]string, payloadHash string) string {
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var hb strings.Builder
	for _, k := range names {
		hb.WriteString(k)
		hb.WriteByte(':')
		hb.WriteString(strings.TrimSpace(headers[k]))
		hb.WriteByte('\n')
	}
	return strings.Join([]string{
		method, canonicalURI, canonicalQ, hb.String(),
		strings.Join(names, ";"), payloadHash,
	}, "\n")
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

// signRequest 计算签名并写进请求头(host/x-amz-date/x-amz-content-sha256/Authorization)。
// 拆出 pure 函数便于用 AWS 官方向量做单测。
func (s *s3Backend) signRequest(req *http.Request, payloadHash string, now time.Time) string {
	amzDate := now.UTC().Format("20060102T150405Z")
	dateShort := amzDate[:8]
	canonicalURI := uriEncode(req.URL.Path, true)
	headers := map[string]string{
		"host":                 req.Host,
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           amzDate,
	}
	for k, vs := range req.Header {
		lk := strings.ToLower(k)
		if lk == "host" || strings.HasPrefix(lk, "x-amz-") {
			headers[lk] = strings.Join(vs, ",")
		}
	}
	canonical := buildCanonicalRequest(req.Method, canonicalURI,
		canonicalQuery(req.URL.Query()), headers, payloadHash)

	scope := dateShort + "/" + s.cfg.Region + "/s3/aws4_request"
	sum := sha256.Sum256([]byte(canonical))
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])

	kDate := hmacSHA256([]byte("AWS4"+s.cfg.Secret), []byte(dateShort))
	kRegion := hmacSHA256(kDate, []byte(s.cfg.Region))
	kService := hmacSHA256(kRegion, []byte("s3"))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	signature := hex.EncodeToString(hmacSHA256(kSigning, []byte(stringToSign)))

	signed := make([]string, 0, len(headers))
	for k := range headers {
		signed = append(signed, k)
	}
	sort.Strings(signed)
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.cfg.AccessKey+
		"/"+scope+", SignedHeaders="+strings.Join(signed, ";")+", Signature="+signature)
	return signature
}

// ---- 协议操作 ----

func (s *s3Backend) objectURL(key string, query url.Values) *url.URL {
	u, _ := url.Parse(s.cfg.Endpoint)
	u.Path = "/" + s.cfg.Bucket + "/" + key
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	return u
}

func (s *s3Backend) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.objectURL(key, nil).String(), r)
	if err != nil {
		return err
	}
	if size >= 0 {
		req.ContentLength = size
	}
	req.Header.Set("x-amz-content-sha256", "UNSIGNED-PAYLOAD")
	s.signRequest(req, "UNSIGNED-PAYLOAD", time.Now())
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("s3 put %s: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("s3 put %s: HTTP %d %s", key, resp.StatusCode, s3ErrText(body))
	}
	return nil
}

func (s *s3Backend) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.objectURL(key, nil).String(), nil)
	if err != nil {
		return nil, err
	}
	s.signRequest(req, emptyPayloadHash, time.Now())
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("s3 get %s: %w", key, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, ErrNotFound
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("s3 get %s: HTTP %d %s", key, resp.StatusCode, s3ErrText(body))
	}
	return resp.Body, nil
}

func (s *s3Backend) Stat(ctx context.Context, key string) (int64, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, s.objectURL(key, nil).String(), nil)
	if err != nil {
		return 0, false, err
	}
	s.signRequest(req, emptyPayloadHash, time.Now())
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, false, fmt.Errorf("s3 head %s: %w", key, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return 0, false, nil
	}
	if resp.StatusCode/100 != 2 {
		return 0, false, fmt.Errorf("s3 head %s: HTTP %d", key, resp.StatusCode)
	}
	return resp.ContentLength, true, nil
}

func (s *s3Backend) Delete(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.objectURL(key, nil).String(), nil)
	if err != nil {
		return err
	}
	s.signRequest(req, emptyPayloadHash, time.Now())
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("s3 delete %s: %w", key, err)
	}
	defer resp.Body.Close()
	// 404 视为已删(幂等删除语义)
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("s3 delete %s: HTTP %d %s", key, resp.StatusCode, s3ErrText(body))
	}
	return nil
}

func (s *s3Backend) Test(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, s.objectURL("", nil).String(), nil)
	if err != nil {
		return err
	}
	s.signRequest(req, emptyPayloadHash, time.Now())
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("s3 探测 %s: %w", s.cfg.Bucket, err)
	}
	defer resp.Body.Close()
	// HEAD bucket:200 = 桶在且有权限;403 = 桶在但无 ListBucket 权限(多数场景可用);
	// 404 = 桶不存在(硬失败)。
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("桶不存在: %s", s.cfg.Bucket)
	}
	if resp.StatusCode == http.StatusForbidden {
		return nil // 桶在,仅无列桶权限——对象读写不受影响
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("s3 探测: HTTP %d", resp.StatusCode)
	}
	return nil
}

// ListObjectsV2 列举指定前缀下的对象键(自动翻页);迁移/巡检用。
func (s *s3Backend) ListObjectsV2(ctx context.Context, prefix string, maxKeys int) ([]ObjectInfo, error) {
	out := []ObjectInfo{}
	marker := ""
	for len(out) < maxKeys {
		q := url.Values{}
		q.Set("list-type", "2")
		q.Set("prefix", prefix)
		q.Set("max-keys", strconv.Itoa(min(1000, maxKeys-len(out))))
		if marker != "" {
			q.Set("continuation-token", marker)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.objectURL("", q).String(), nil)
		if err != nil {
			return nil, err
		}
		s.signRequest(req, emptyPayloadHash, time.Now())
		resp, err := s.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("s3 list: %w", err)
		}
		var list struct {
			Contents []struct {
				Key  string `xml:"Key"`
				Size int64  `xml:"Size"`
			} `xml:"Contents"`
			IsTruncated bool   `xml:"IsTruncated"`
			NextToken   string `xml:"NextContinuationToken"`
		}
		err = xml.NewDecoder(resp.Body).Decode(&list)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("s3 list 解析: %w", err)
		}
		for _, c := range list.Contents {
			out = append(out, ObjectInfo{Key: c.Key, Size: c.Size})
		}
		if !list.IsTruncated || list.NextToken == "" {
			break
		}
		marker = list.NextToken
	}
	return out, nil
}

// s3ErrText 尽力提取错误 XML 里的可读信息。
func s3ErrText(body []byte) string {
	var e struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if err := xml.Unmarshal(body, &e); err == nil && e.Code != "" {
		return e.Code + ": " + e.Message
	}
	return strings.TrimSpace(string(body))
}
