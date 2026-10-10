package storagecenter

import (
	"encoding/xml"
	"net/http"
	"strings"
	"testing"
	"time"
)

// SigV4 规范化请求:对齐 AWS 官方文档「GET Object」示例的 canonical request 原文。
// (docs.aws.amazon.com/IAM/latest/UserGuide/reference_sigv/examples.html)
func TestS3CanonicalRequestVector(t *testing.T) {
	headers := map[string]string{
		"host":                 "examplebucket.s3.amazonaws.com",
		"range":                "bytes=0-9",
		"x-amz-content-sha256": emptyPayloadHash,
		"x-amz-date":           "20130524T000000Z",
	}
	got := buildCanonicalRequest("GET", "/test.txt", "", headers, emptyPayloadHash)
	want := strings.Join([]string{
		"GET",
		"/test.txt",
		"",
		"host:examplebucket.s3.amazonaws.com",
		"range:bytes=0-9",
		"x-amz-content-sha256:" + emptyPayloadHash,
		"x-amz-date:20130524T000000Z",
		"",
		"host;range;x-amz-content-sha256;x-amz-date",
		emptyPayloadHash,
	}, "\n")
	if got != want {
		t.Fatalf("canonical request 不符 AWS 官方向量:\n got=%q\nwant=%q", got, want)
	}
}

// 带查询串的规范化(键排序 + 值编码)。
func TestS3CanonicalQuery(t *testing.T) {
	got := canonicalQuery(map[string][]string{
		"list-type":          {"2"},
		"prefix":             {"a b/c"},
		"max-keys":           {"100"},
		"continuation-token": {"xyz=="},
	})
	// 键序:continuation-token < list-type < max-keys < prefix;值 RFC3986 编码(= → %3D,空格 → %20,/ → %2F)
	want := "continuation-token=" + uriEncode("xyz==", false) +
		"&list-type=2&max-keys=100&prefix=" + uriEncode("a b/c", false)
	if got != want {
		t.Fatalf("canonical query: got %q want %q", got, want)
	}
	if !strings.HasPrefix(want, "continuation-token=xyz") || !strings.Contains(want, "prefix=a%20b%2Fc") {
		t.Fatalf("编码形状异常: %q", want)
	}
}

func TestObjectKey(t *testing.T) {
	k, err := ObjectKey("artex/", "report-jobs", "job_1.docx")
	if err != nil || k != "artex/report-jobs/job_1.docx" {
		t.Fatalf("key = %q err = %v", k, err)
	}
	if _, err := ObjectKey("", "c2", "../etc/passwd"); err == nil {
		t.Fatal(".. 逃逸未被拒绝")
	}
	if _, err := ObjectKey("", "c2", ""); err == nil {
		t.Fatal("空 relPath 未被拒绝")
	}
	// Windows 路径分隔符归一
	k, _ = ObjectKey("", "uploads", `tasks\12\uploads\a.txt`)
	if k != "uploads/tasks/12/uploads/a.txt" {
		t.Fatalf("反斜杠未归一: %q", k)
	}
}

func TestNormalizeEndpoint(t *testing.T) {
	if e, _ := NormalizeEndpoint(" minio.local:9000 "); e != "https://minio.local:9000" {
		t.Fatalf("缺 scheme 应补 https: %q", e)
	}
	if e, _ := NormalizeEndpoint("http://a.b//"); e != "http://a.b" {
		t.Fatalf("尾斜杠应去: %q", e)
	}
	if _, err := NormalizeEndpoint("ftp://a.b"); err == nil {
		t.Fatal("ftp 应被拒绝")
	}
}

func TestRelPathFromLocal(t *testing.T) {
	rel, err := RelPathFromLocal(`D:\data`, `D:\data\report-jobs\a.docx`)
	if err != nil || rel != "report-jobs/a.docx" {
		t.Fatalf("rel = %q err = %v", rel, err)
	}
	if _, err := RelPathFromLocal(`D:\data`, `D:\elsewhere\a.docx`); err == nil {
		t.Fatal("目录外路径未被拒绝")
	}
}

// 签名链确定性:同输入同输出,scope/形状正确(端到端正确性靠 MinIO 冒烟验收)。
func TestS3SignDeterministic(t *testing.T) {
	b, err := newS3Backend(Config{
		Protocol: "s3", Endpoint: "http://127.0.0.1:9000", Bucket: "bkt",
		Region: "us-east-1", AccessKey: "ak", Secret: "sk",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s3 := b.(*s3Backend)
	u := s3.objectURL("x/y.txt", nil)
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	req1, _ := http.NewRequest(http.MethodPut, u.String(), nil)
	req1.Host = "127.0.0.1:9000"
	sig1 := s3.signRequest(req1, emptyPayloadHash, now)
	req2, _ := http.NewRequest(http.MethodPut, u.String(), nil)
	req2.Host = "127.0.0.1:9000"
	sig2 := s3.signRequest(req2, emptyPayloadHash, now)
	if sig1 != sig2 || len(sig1) != 64 {
		t.Fatalf("签名非确定或长度异常: %q vs %q", sig1, sig2)
	}
	auth := req1.Header.Get("Authorization")
	scope := "20261010/us-east-1/s3/aws4_request"
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential="+"ak/"+scope+", ") ||
		!strings.Contains(auth, "SignedHeaders=host;x-amz-content-sha256;x-amz-date") {
		t.Fatalf("Authorization 形状异常: %q", auth)
	}
	if req1.Header.Get("x-amz-date") != "20261010T000000Z" {
		t.Fatalf("x-amz-date 异常: %q", req1.Header.Get("x-amz-date"))
	}
}

// PROPFIND 多响应解析(webdav Stat 的解码路径)。
func TestWebDAVPropfindParse(t *testing.T) {
	body := []byte(`<?xml version="1.0" encoding="utf-8"?>
<D:multistatus xmlns:D="DAV:">
 <D:response>
  <D:href>/dav/bkt/sub/a.txt</D:href>
  <D:propstat><D:prop><D:getcontentlength>1234</D:getcontentlength></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>
 </D:response>
</D:multistatus>`)
	var resp struct {
		Responses []struct {
			Propstats []struct {
				Prop struct {
					GetContentLength int64 `xml:"getcontentlength"`
				} `xml:"prop"`
			} `xml:"propstat"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Responses) != 1 || resp.Responses[0].Propstats[0].Prop.GetContentLength != 1234 {
		t.Fatalf("解析异常: %+v", resp)
	}
}

func TestParentDirs(t *testing.T) {
	got := parentDirs("a/b/c.txt")
	want := []string{"a", "a/b"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("parentDirs = %v", got)
	}
}

// objectURL:不得把 "http://" 塌缩成 "http:/"(冒烟抓到的回归)。
func TestWebDAVObjectURL(t *testing.T) {
	b, err := newWebDAVBackend(Config{
		Protocol: "webdav", Endpoint: "http://127.0.0.1:8932", Bucket: "artex",
		AccessKey: "u", Secret: "p",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := b.(*webdavBackend).objectURL("report-jobs/a b.txt")
	want := "http://127.0.0.1:8932/artex/report-jobs/a%20b.txt"
	if got != want {
		t.Fatalf("objectURL = %q, want %q", got, want)
	}
}
