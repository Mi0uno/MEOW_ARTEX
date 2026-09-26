package c2center

// 传输框架单测(三期 §18,Agent T):帧编解码(半包/粘包/超长帧)、XOR 混淆、
// TransportConfig 解析,以及 fakeStore 的传输配置扩展。全程不连库。

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeAddListener 造一个带协议(与可选传输配置)的监听器并写入密钥(测试装配;
// fakestore_test.go 的 addListener 固定 proto=http,不满足四传输测试需要)。
func fakeAddListener(st *fakeStore, proto, urlPath string, port int, key []byte, cfgJSON string) *Listener {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.nextLID++
	id := st.nextLID
	l := &Listener{
		ID: id, Name: "t-" + proto, Proto: proto, BindHost: "127.0.0.1", Port: port,
		URLPath: urlPath, Enabled: true, TransportConfigJSON: cfgJSON,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	st.listeners[id] = l
	st.keys[id] = key
	return cloneListener(l)
}

// GetListenerTransportConfig 让 fakeStore 满足 transportConfigStore(oss/kcp 启动用)。
func (f *fakeStore) GetListenerTransportConfig(id int64) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.listeners[id]
	if !ok {
		return nil, errNotFound
	}
	if l.TransportConfigJSON == "" {
		return nil, nil
	}
	return []byte(l.TransportConfigJSON), nil
}

func randKey32(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

// ---- 帧编解码 ----

func TestFrameRoundtrip(t *testing.T) {
	var buf bytes.Buffer
	frames := [][]byte{
		[]byte("small"),
		bytes.Repeat([]byte{0xAB}, 300*1024), // 大帧(跨多轮 Write)
		[]byte(""),
	}
	// writeFrame 拒绝空帧,跳过第三个
	for _, f := range frames[:2] {
		if err := writeFrame(&buf, nil, f); err != nil {
			t.Fatalf("writeFrame: %v", err)
		}
	}
	for i, want := range frames[:2] {
		got, err := readFrame(&buf, nil)
		if err != nil {
			t.Fatalf("readFrame #%d: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("帧 #%d 内容不符: len=%d", i, len(got))
		}
	}
	if _, err := readFrame(&buf, nil); err != io.EOF {
		t.Fatalf("应 EOF: %v", err)
	}
}

func TestFrameHalfPacketsAndSticky(t *testing.T) {
	// 半包:一个字节一个字节喂;粘包:多帧拼一起一次喂。io.ReadFull 应都消化。
	var buf bytes.Buffer
	if err := writeFrame(&buf, nil, []byte("frame-one")); err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(&buf, nil, []byte("frame-two")); err != nil {
		t.Fatal(err)
	}
	half := &byteReader{r: bytes.NewReader(buf.Bytes())}
	got, err := readFrame(half, nil)
	if err != nil || string(got) != "frame-one" {
		t.Fatalf("半包读帧失败: %q %v", got, err)
	}
	got, err = readFrame(half, nil)
	if err != nil || string(got) != "frame-two" {
		t.Fatalf("半包读帧失败: %q %v", got, err)
	}
}

type byteReader struct{ r io.Reader }

func (b *byteReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1] // 每次只给 1 字节,制造最坏半包
	}
	return b.r.Read(p)
}

func TestFrameTooLarge(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF}) // 长度 4G > frameMaxBytes
	if _, err := readFrame(&buf, nil); err == nil {
		t.Fatal("超长帧应报错")
	}
	// 写侧同样拒绝超长
	if err := writeFrame(io.Discard, nil, make([]byte, frameMaxBytes+1)); err == nil {
		t.Fatal("超长写入应报错")
	}
}

func TestFrameXORObfuscation(t *testing.T) {
	key := []byte("preshared-key")
	plain := bytes.Repeat([]byte{0x42}, 1000)

	// 不混淆读不出混淆帧,反之亦然
	var buf bytes.Buffer
	if err := writeFrame(&buf, key, plain); err != nil {
		t.Fatal(err)
	}
	obfuscated := buf.Bytes()
	if bytes.Contains(obfuscated, []byte{0x42, 0x42, 0x42, 0x42}) {
		t.Fatal("混淆后不应出现明文特征")
	}
	got, err := readFrame(bytes.NewReader(obfuscated), key)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("混淆往返不符: %v", err)
	}
	if _, err := readFrame(bytes.NewReader(obfuscated), nil); err == nil {
		t.Fatal("无 key 读混淆帧应失败")
	}

	// 多帧交错读写位置互不影响(frameConn 逐帧复位)
	var buf2 bytes.Buffer
	for i := 0; i < 3; i++ {
		if err := writeFrame(&buf2, key, []byte(strings.Repeat("x", i+1))); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 3; i++ {
		got, err := readFrame(&buf2, key)
		if err != nil || len(got) != i {
			t.Fatalf("多帧往返 #%d 失败: len=%d err=%v", i, len(got), err)
		}
	}
}

func TestFrameConnOverTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		fc := newFrameConn(newTimeoutConn(c, time.Second), nil)
		_ = fc.WriteFrame([]byte("ping"))
		raw, err := fc.ReadFrame()
		if err == nil {
			_ = fc.WriteFrame(raw)
		}
		_ = fc.Close()
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	fc := newFrameConn(c, nil)
	got, err := fc.ReadFrame()
	if err != nil || string(got) != "ping" {
		t.Fatalf("ReadFrame: %q %v", got, err)
	}
	if err := fc.WriteFrame([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	got, err = fc.ReadFrame()
	if err != nil || string(got) != "pong" {
		t.Fatalf("回显: %q %v", got, err)
	}
	_ = fc.Close()
}

// ---- TransportConfig ----

func TestTransportConfigParse(t *testing.T) {
	cfg, err := parseTransportConfig(nil)
	if err != nil || cfg == nil {
		t.Fatalf("空配置应得零值: %v", err)
	}
	raw, _ := json.Marshal(map[string]any{
		"endpoint": "oss-cn.test:9000", "bucket": "b", "access_key": "ak",
		"secret_key": "sk", "prefix": "p/", "psk": "k", "scan_seconds": 2,
	})
	cfg, err = parseTransportConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != "oss-cn.test:9000" || cfg.Bucket != "b" || cfg.AccessKey != "ak" ||
		cfg.SecretKey != "sk" || cfg.Prefix != "p/" || cfg.PSK != "k" {
		t.Fatalf("解析不符: %+v", cfg)
	}
	if cfg.scanInterval() != 2*time.Second {
		t.Fatalf("scan_interval: %v", cfg.scanInterval())
	}
	if cfg.ossPrefix("abc") != "p" {
		t.Fatalf("prefix 应优先: %q", cfg.ossPrefix("abc"))
	}
	// 缺省与下限
	cfg2, _ := parseTransportConfig([]byte(`{}`))
	if cfg2.scanInterval() != 3*time.Second {
		t.Fatalf("默认扫描间隔应 3s: %v", cfg2.scanInterval())
	}
	if cfg2.ossPrefix("") != "artex" {
		t.Fatalf("prefix 缺省应回落 artex: %q", cfg2.ossPrefix(""))
	}
	if _, err := parseTransportConfig([]byte("{bad")); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
}

// ---- 矩阵 ----

func TestValidateFormatForProto(t *testing.T) {
	for _, f := range []string{"ps1", "bash", "py", "gosrc"} {
		for _, p := range []string{"http", "ws", "tcp", "oss", ""} {
			if err := validateFormatForProto(f, p); err != nil {
				t.Fatalf("%s/%s 应支持: %v", f, p, err)
			}
		}
	}
	// kcp:仅 py/gosrc
	for _, f := range []string{"py", "gosrc"} {
		if err := validateFormatForProto(f, ProtoKCP); err != nil {
			t.Fatalf("kcp/%s 应支持: %v", f, err)
		}
	}
	for _, f := range []string{"ps1", "bash"} {
		if err := validateFormatForProto(f, ProtoKCP); err == nil {
			t.Fatalf("kcp/%s 应明确报错", f)
		} else if !strings.Contains(err.Error(), "kcp") {
			t.Fatalf("错误应提及协议: %v", err)
		}
	}
	if err := validateFormatForProto("ps1", "grpc"); err == nil {
		t.Fatal("未知协议应报错")
	}
}

func TestTransportConfigForOss(t *testing.T) {
	l := &Listener{ID: 1, Proto: ProtoOSS, URLPath: "u"}
	if _, err := transportConfigFor(l); err == nil {
		t.Fatal("oss 无配置应报错")
	}
	l.TransportConfigJSON = `{"endpoint":"e","bucket":"b","access_key":"a","secret_key":"s"}`
	cfg, err := transportConfigFor(l)
	if err != nil || cfg.Bucket != "b" {
		t.Fatalf("oss 配置解析不符: %+v %v", cfg, err)
	}
	// kcp 只取 psk,不要求 oss 四要素
	l2 := &Listener{ID: 1, Proto: ProtoKCP, TransportConfigJSON: `{"psk":"k"}`}
	cfg2, err := transportConfigFor(l2)
	if err != nil || cfg2.PSK != "k" {
		t.Fatalf("kcp psk 解析不符: %+v %v", cfg2, err)
	}
}
