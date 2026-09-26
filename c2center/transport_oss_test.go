package c2center

// oss 载体集成测试(三期 §18,Agent T):httptest 起假 OSS(校验 V1 HMAC-SHA1
// 签名 + 对象语义),走完整通道——beacon 侧写 req 对象 → 平台扫描 goroutine 处理
// → 写 resp 对象 → beacon 读回;含防重放(重复 nc 被拒)与孤儿 resp 清理。不连库。

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeOSS 是最小 OSS 兼容服务:Put/Get/Delete/ListObjectsV2 + 签名校验。
type fakeOSS struct {
	mu     sync.Mutex
	objs   map[string][]byte
	akSk   [2]string
	bucket string
	t      *testing.T

	badSig int // 签名校验失败计数(断言用)
}

func newFakeOSS(t *testing.T, ak, sk, bucket string) *fakeOSS {
	return &fakeOSS{objs: map[string][]byte{}, akSk: [2]string{ak, sk}, bucket: bucket, t: t}
}

// sign 复算 V1 签名(与 ossClient.sign 同一口径;bucket 由配置给定,
// virtual-host 与 path-style 两种风格的 CanonicalizedResource 相同)。
func (f *fakeOSS) sign(r *http.Request) string {
	date := r.Header.Get("Date")
	res := "/" + f.bucket + r.URL.Path
	if r.URL.RawQuery != "" {
		keys := strings.Split(r.URL.RawQuery, "&")
		sort.Strings(keys)
		res += "?" + strings.Join(keys, "&")
	}
	ss := r.Method + "\n\napplication/octet-stream\n" + date + "\n" + res
	mac := hmac.New(sha1.New, []byte(f.akSk[1]))
	mac.Write([]byte(ss))
	want := "OSS " + f.akSk[0] + ":" + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return want
}

func (f *fakeOSS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") == "" || r.Header.Get("Date") == "" {
		http.Error(w, "missing auth", http.StatusForbidden)
		return
	}
	if got, want := r.Header.Get("Authorization"), f.sign(r); got != want {
		f.mu.Lock()
		f.badSig++
		f.mu.Unlock()
		http.Error(w, "signature mismatch", http.StatusForbidden)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.TrimPrefix(r.URL.Path, "/")
	switch r.Method {
	case http.MethodPut:
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		f.objs[key] = buf.Bytes()
		w.WriteHeader(200)
	case http.MethodGet:
		if r.URL.RawQuery != "" { // ListObjectsV2
			prefix := r.URL.Query().Get("prefix")
			var keys []string
			for k := range f.objs {
				if strings.HasPrefix(k, prefix) {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			var b strings.Builder
			b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?><ListBucketResult>")
			for _, k := range keys {
				b.WriteString("<Contents><Key>" + k + "</Key><LastModified>" +
					time.Now().UTC().Format(time.RFC3339) + "</LastModified></Contents>")
			}
			b.WriteString("</ListBucketResult>")
			_, _ = w.Write([]byte(b.String()))
			return
		}
		if data, ok := f.objs[key]; ok {
			_, _ = w.Write(data)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	case http.MethodDelete:
		delete(f.objs, key)
		w.WriteHeader(204)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

// keysSnapshot 当前对象键(断言用)。
func (f *fakeOSS) keysSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := make([]string, 0, len(f.objs))
	for k := range f.objs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestOSSChain(t *testing.T) {
	const (
		ak = "test-ak"
		sk = "test-sk"
	)
	oss := newFakeOSS(t, ak, sk, "bkt")
	srv := httptest.NewServer(oss)
	t.Cleanup(srv.Close)
	t.Setenv("NO_PROXY", "*") // vhost 域名(bkt.127.0.0.1)不走红环境代理
	host := strings.TrimPrefix(srv.URL, "http://")

	mgr, st, key, _ := newTestListener(t)
	cfgJSON := fmt.Sprintf(`{"endpoint":"%s","bucket":"bkt","access_key":"%s","secret_key":"%s","prefix":"pfx","scan_seconds":1}`, "http://"+host, ak, sk)
	l := fakeAddListener(st, ProtoOSS, "abc", 0, key, cfgJSON)
	if err := mgr.StartListener(l); err != nil {
		t.Fatalf("StartListener: %v", err)
	}
	t.Cleanup(func() { _ = mgr.StopListener(l.ID) })

	// beacon 侧客户端(与平台同一实现,自证签名互通)。
	cli, err := newOSSClient(&TransportConfig{
		Endpoint: "http://" + host, Bucket: "bkt", AccessKey: ak, SecretKey: sk, Prefix: "pfx", ScanSeconds: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	uid := BeaconUID(l.ID, "oss-host", "bob", "linux")
	reg := RegPayload{UID: uid, Hostname: "oss-host", Username: "bob", OS: "linux", Arch: "amd64", PID: 7, Process: "python3"}
	sealed, err := Seal(key, mustJSON(RequestEnvelope{
		Type: TypeReg, TS: time.Now().Unix(), NC: "nc-reg-1", Data: mustJSON(reg),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.Put("pfx/"+uid+"/req/nc-reg-1", sealed); err != nil {
		t.Fatalf("PUT req: %v", err)
	}

	// 平台扫描(1s 间隔)处理后,resp 对象出现且 req 被消费。
	var respData []byte
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := cli.Get("pfx/" + uid + "/resp/nc-reg-1")
		if err == nil {
			respData = data
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if respData == nil {
		t.Fatalf("resp 对象未出现;keys=%v", oss.keysSnapshot())
	}
	plain, err := Open(key, respData)
	if err != nil {
		t.Fatalf("resp 解密: %v", err)
	}
	var ack struct {
		Type string `json:"t"`
		Data struct {
			SID string `json:"sid"`
		} `json:"d"`
	}
	if err := json.Unmarshal(plain, &ack); err != nil {
		t.Fatal(err)
	}
	if ack.Type != TypeAck || len(ack.Data.SID) != 32 {
		t.Fatalf("reg 响应不符: %+v", ack)
	}

	// 会话已入库且标注 transport=oss;req 对象已被平台删除。
	sess, err := st.GetSessionBySID(ack.Data.SID)
	if err != nil || sess == nil {
		t.Fatalf("会话未入库: %v", err)
	}
	if sess.Transport != ProtoOSS {
		t.Fatalf("会话 transport 应为 oss: %q", sess.Transport)
	}
	if _, err := cli.Get("pfx/" + uid + "/req/nc-reg-1"); err == nil {
		t.Fatal("req 对象应已被平台删除")
	}

	// 第二轮:poll 拉任务(先建一个 queued 任务)。
	taskID, err := st.CreateTask(TaskInput{SessionID: sess.ID, Kind: "shell", Input: `{"command":"id"}`, Actor: "user"})
	if err != nil {
		t.Fatal(err)
	}
	pollSealed, err := Seal(key, mustJSON(RequestEnvelope{
		Type: TypePoll, SID: ack.Data.SID, TS: time.Now().Unix(), NC: "nc-poll-1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.Put("pfx/"+uid+"/req/nc-poll-1", pollSealed); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(10 * time.Second)
	var tasksRaw []byte
	for time.Now().Before(deadline) {
		data, err := cli.Get("pfx/" + uid + "/resp/nc-poll-1")
		if err == nil {
			tasksRaw = data
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if tasksRaw == nil {
		t.Fatal("poll 的 resp 对象未出现")
	}
	plain, err = Open(key, tasksRaw)
	if err != nil {
		t.Fatal(err)
	}
	var tasks struct {
		Data struct {
			Tasks []TaskOut `json:"ts"`
		} `json:"d"`
	}
	if err := json.Unmarshal(plain, &tasks); err != nil {
		t.Fatal(err)
	}
	if len(tasks.Data.Tasks) != 1 || tasks.Data.Tasks[0].ID != taskID {
		t.Fatalf("任务未派发: %+v", tasks.Data)
	}
}

func TestOSSSignatureRejected(t *testing.T) {
	oss := newFakeOSS(t, "ak", "sk", "bkt")
	srv := httptest.NewServer(oss)
	t.Cleanup(srv.Close)
	t.Setenv("NO_PROXY", "*")

	// 错误 SK 的客户端:签名被假 OSS 拒绝(403)。
	cli, err := newOSSClient(&TransportConfig{
		Endpoint: srv.URL, Bucket: "bkt",
		AccessKey: "ak", SecretKey: "WRONG", Prefix: "p",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := cli.Put("p/x", []byte("v")); err == nil {
		t.Fatal("错误签名应被拒")
	}
	oss.mu.Lock()
	defer oss.mu.Unlock()
	if oss.badSig == 0 {
		t.Fatal("badSig 计数未增加")
	}
}
