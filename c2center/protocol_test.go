package c2center

// 协议层单测:AES-256-GCM 线格式、防重放 LRU、时间窗、uid 指纹。

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
)

func testKey() []byte {
	key := make([]byte, keySize)
	for i := range key {
		key[i] = byte(i*7 + 3)
	}
	return key
}

func TestSealOpenRoundtrip(t *testing.T) {
	key := testKey()
	plain := []byte(`{"t":"poll","ts":1,"nc":"x"}`)
	sealed, err := Seal(key, plain)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// 线格式:12B nonce || ciphertext+tag;总长 = 12 + len(plain) + 16。
	if len(sealed) != 12+len(plain)+16 {
		t.Fatalf("密文长度 %d,应为 %d(nonce+tag+明文)", len(sealed), 12+len(plain)+16)
	}
	got, err := Open(key, sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("往返不一致: %s", got)
	}
}

func TestOpenTamperReturnsErrAuth(t *testing.T) {
	key := testKey()
	sealed, err := Seal(key, []byte("hello beacon"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// 篡改密文本体。
	bad := append([]byte(nil), sealed...)
	bad[len(bad)-1] ^= 0xff
	if _, err := Open(key, bad); !errors.Is(err, ErrAuth) {
		t.Fatalf("篡改密文应返回 ErrAuth,得到: %v", err)
	}
	// 截断。
	if _, err := Open(key, sealed[:8]); !errors.Is(err, ErrAuth) {
		t.Fatalf("截断密文应返回 ErrAuth,得到: %v", err)
	}
	// 错误密钥。
	other := testKey()
	other[0] ^= 0xff
	if _, err := Open(other, sealed); !errors.Is(err, ErrAuth) {
		t.Fatalf("错误密钥应返回 ErrAuth,得到: %v", err)
	}
}

func TestSealBadKeyLength(t *testing.T) {
	if _, err := Seal([]byte("short"), []byte("x")); err == nil {
		t.Fatal("短密钥应报错")
	}
	if _, err := Seal(make([]byte, 31), []byte("x")); err == nil {
		t.Fatal("31 字节密钥应报错")
	}
}

func TestFreshTS(t *testing.T) {
	now := time.Now()
	if !freshTS(now.Unix(), now) {
		t.Fatal("当前时间应通过")
	}
	if !freshTS(now.Unix()-599, now) {
		t.Fatal("窗口内偏移应通过")
	}
	if freshTS(now.Unix()-601, now) {
		t.Fatal("超窗(过去)应拒绝")
	}
	if freshTS(now.Unix()+601, now) {
		t.Fatal("超窗(未来)应拒绝")
	}
	if freshTS(0, now) {
		t.Fatal("ts=0 应拒绝")
	}
}

func TestLRUSetDedup(t *testing.T) {
	s := newLRUSet(1024)
	nc := func(i int) string { return itoa(i) }
	// 1024 个新 nc 全部接受。
	for i := 0; i < 1024; i++ {
		if !s.add(nc(i)) {
			t.Fatalf("第 %d 个新 nc 被误拒", i)
		}
	}
	// 未满容量的新 nc:nc#1023 尚未被逐出,重复出现应拒绝。
	if s.add(nc(1023)) {
		t.Fatal("容量内的 nc 重复出现应拒绝")
	}
	// 第 1025 个新 nc:挤掉最老的 nc#0。
	if !s.add(nc(1024)) {
		t.Fatal("第 1025 个新 nc 应接受")
	}
	// nc#0 已被逐出:重新出现应视为「新」。
	if !s.add(nc(0)) {
		t.Fatal("nc#0 已被逐出,重新 add 应接受")
	}
}

func TestNonceTablePerKey(t *testing.T) {
	nt := newNonceTable()
	if !nt.seen("sid1", "nc1") {
		t.Fatal("首次出现应接受")
	}
	if nt.seen("sid1", "nc1") {
		t.Fatal("同 sid 同 nc 应判重放")
	}
	if !nt.seen("sid2", "nc1") {
		t.Fatal("不同 sid 的同 nc 应互相独立")
	}
}

func TestBeaconUIDFormula(t *testing.T) {
	want := sha256.Sum256([]byte("1|host1|alice|windows"))
	wantHex := hexEncode(want[:])[:16]
	if got := BeaconUID(1, "host1", "alice", "windows"); got != wantHex {
		t.Fatalf("uid 不一致: got %s want %s", got, wantHex)
	}
	// 换任意一个盐位都不同。
	if BeaconUID(2, "host1", "alice", "windows") == wantHex {
		t.Fatal("listenerID 变化应改变 uid")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func hexEncode(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0xf])
	}
	return string(out)
}
