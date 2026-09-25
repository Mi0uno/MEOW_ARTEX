// Package vpscenter 是 VPS 中心的领域包:自有 Linux 机器池的 SSH 连接、任务指派、
// 命令执行、Web 终端与工具集检测/安装。
//
// 分层与 proxycenter 相同:本包只管「运行时状态」(SSH 连接、终端槽位、测活循环),
// 一切持久化经 Store 接口回到 server/db 层(本包不 import db,便于单测)。
// 硬性纪律:禁止 os/exec —— 到机器上的一切操作都走 golang.org/x/crypto/ssh。
package vpscenter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// 认证方式:password(密码)/ key(私钥,可选口令)。
const (
	AuthPassword = "password"
	AuthKey      = "key"
)

// SSHConfig 是一次 SSH 连接的完整参数(含明文凭据,仅进程内传递,绝不落日志)。
type SSHConfig struct {
	Host          string
	Port          int
	Username      string
	AuthType      string // password | key
	Password      string
	PrivateKey    []byte
	KeyPassphrase string
	Timeout       time.Duration // TCP+握手超时;<=0 时取 15s
}

// SysInfo 是一次系统信息采集的结果(测活时回填机器档案)。
type SysInfo struct {
	OSName    string // /etc/os-release 的 ID(如 ubuntu/debian/centos)
	OSVersion string // VERSION_ID(如 22.04)
	Kernel    string // uname -r
	Arch      string // uname -m
	CPUCores  int
	MemoryMB  int
	DiskGB    int // 根分区容量
}

// ToolDetection 是一个工具在某台机器上的探测结果。
// json tag 必须保留:该结构体会被 detect API 直接序列化,没有 tag 会变成
// 大写字段名,前端读 tool_name/installed/version 全是 undefined。
type ToolDetection struct {
	ToolName  string `json:"tool_name"`
	Installed bool   `json:"installed"`
	Version   string `json:"version"` // 空串 = 装了但探不到版本
}

// SSHClient 是到一台机器的 SSH 连接句柄。Manager 只依赖该接口,测试注入 fake。
type SSHClient interface {
	// Exec 在远端跑一条命令(bash -lc 语义:合并 stdout+stderr,返回退出码)。
	// 命令本身的非零退出码不算错误(err 只在连接/传输/超时层失败时非 nil)。
	Exec(ctx context.Context, cmd string, timeout time.Duration) (stdoutAndStderr []byte, exitCode int, err error)
	// OpenPTY 起一个交互式会话(cols/rows 为初始终端尺寸)。
	OpenPTY(ctx context.Context, cols, rows uint16) (PTYSession, error)
	// SysInfo 采集 os-release / uname / nproc / 内存 / 磁盘。
	SysInfo(ctx context.Context) (*SysInfo, error)
	// DetectTools 探测指定工具是否安装及版本(command -v + --version)。
	DetectTools(ctx context.Context, names []string) ([]ToolDetection, error)
	Close() error
}

// PTYSession 是一个交互式终端会话。
type PTYSession interface {
	io.Reader
	io.Writer
	Resize(cols, rows uint16) error
	// ExitCode 在会话结束时投递一次退出码,然后关闭通道。
	ExitCode() <-chan int
	Close() error
}

// Dial 建立到机器的 SSH 连接(password 与 key 两种认证;私钥支持
// ed25519/rsa/ecdsa,可选 passphrase)。连接失败返回错误。
func Dial(cfg SSHConfig) (SSHClient, error) {
	cfg.Host = strings.TrimSpace(cfg.Host)
	if cfg.Host == "" {
		return nil, errors.New("SSH 主机为空")
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		cfg.Port = 22
	}
	if cfg.Username == "" {
		cfg.Username = "root"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	auths, err := sshAuthMethods(&cfg)
	if err != nil {
		return nil, err
	}
	client, err := ssh.Dial("tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)), &ssh.ClientConfig{
		User:            cfg.Username,
		Auth:            auths,
		Timeout:         cfg.Timeout,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // 自有机器池:不校验主机指纹(指纹入库属二期)
	})
	if err != nil {
		return nil, fmt.Errorf("连接 %s@%s:%d 失败: %w", cfg.Username, cfg.Host, cfg.Port, err)
	}
	return &sshClient{conn: client}, nil
}

// sshAuthMethods 按认证类型构造签名方法。
func sshAuthMethods(cfg *SSHConfig) ([]ssh.AuthMethod, error) {
	switch cfg.AuthType {
	case AuthKey:
		if len(cfg.PrivateKey) == 0 {
			return nil, errors.New("私钥认证但未提供私钥")
		}
		var signer ssh.Signer
		var err error
		if cfg.KeyPassphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(cfg.PrivateKey, []byte(cfg.KeyPassphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(cfg.PrivateKey)
		}
		if err != nil {
			// 无口令解析失败但配了空口令时,再试一次带空口令的解析,
			// 给出更贴近真相的错误信息(加密私钥缺口令是常见错)。
			return nil, fmt.Errorf("解析私钥失败(检查格式与口令,支持 ed25519/rsa/ecdsa): %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	default: // password(含空串:显式密码认证但密码为空,让服务端去拒)
		return []ssh.AuthMethod{ssh.Password(cfg.Password)}, nil
	}
}

// sshClient 是 SSHClient 的真实实现。
type sshClient struct{ conn *ssh.Client }

func (c *sshClient) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Exec 跑一条命令:合并输出 + 退出码;超时杀会话。
func (c *sshClient) Exec(ctx context.Context, cmd string, timeout time.Duration) ([]byte, int, error) {
	if c == nil || c.conn == nil {
		return nil, -1, errors.New("SSH 连接已关闭")
	}
	if strings.TrimSpace(cmd) == "" {
		return nil, 0, nil
	}
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sess, err := c.conn.NewSession()
	if err != nil {
		return nil, -1, fmt.Errorf("创建 SSH 会话失败: %w", err)
	}
	defer sess.Close()

	// 用 StdoutPipe/StderrPipe + WaitGroup 确定性等待,而非 sess.Stdout writer:
	// writer 模式下 Run 收到 exit-status 即返回,而输出拷贝 goroutine 可能尚未跑完——
	// 命令瞬间退出时(数据与 exit-status 背靠背到达)会读到空输出(实测 echo XY 3/3 为空)。
	// 管道 EOF 出现在远端关闭 channel 之后,故 wg.Wait() 返回时数据必然已全部到达。
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return nil, -1, fmt.Errorf("获取 stdout 管道失败: %w", err)
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		return nil, -1, fmt.Errorf("获取 stderr 管道失败: %w", err)
	}
	var outBuf, errBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(&outBuf, stdout) }()
	go func() { defer wg.Done(); _, _ = io.Copy(&errBuf, stderr) }()

	// 超时/取消:杀远端进程并关会话,让 Run 尽快返回。
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = sess.Signal(ssh.SIGKILL)
			_ = sess.Close()
		case <-done:
		}
	}()

	runErr := sess.Run(cmd)
	wg.Wait()
	// 显式拼接为独立缓冲,避免 append(outBuf.Bytes(), ...) 共享底层数组的别名问题。
	out := make([]byte, 0, outBuf.Len()+errBuf.Len())
	out = append(out, outBuf.Bytes()...)
	out = append(out, errBuf.Bytes()...)
	switch {
	case runErr == nil:
		return out, 0, nil
	case ctx.Err() != nil:
		return out, -1, fmt.Errorf("命令执行超时(%s): %w", timeout, ctx.Err())
	}
	var exitErr *ssh.ExitError
	if errors.As(runErr, &exitErr) {
		// 命令自己的非零退出码是正常结果,不是传输错误。
		return out, exitErr.ExitStatus(), nil
	}
	var exitMissing *ssh.ExitMissingError
	if errors.As(runErr, &exitMissing) {
		return out, -1, fmt.Errorf("远端会话异常关闭: %w", runErr)
	}
	return out, -1, runErr
}

// OpenPTY 起交互式会话(xterm-256color;开 PTY 后远端 stderr 自动并入 stdout)。
func (c *sshClient) OpenPTY(ctx context.Context, cols, rows uint16) (PTYSession, error) {
	if c == nil || c.conn == nil {
		return nil, errors.New("SSH 连接已关闭")
	}
	if cols <= 0 {
		cols = 120
	}
	if rows <= 0 {
		rows = 36
	}
	sess, err := c.conn.NewSession()
	if err != nil {
		return nil, fmt.Errorf("创建 SSH 会话失败: %w", err)
	}
	if err := sess.RequestPty("xterm-256color", int(rows), int(cols), ssh.TerminalModes{}); err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("申请 PTY 失败: %w", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	if err := sess.Shell(); err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("启动远端 shell 失败: %w", err)
	}
	ps := &ptySession{sess: sess, stdin: stdin, stdout: stdout, exitCh: make(chan int, 1)}
	go ps.awaitExit()
	return ps, nil
}

// ptySession 是 PTYSession 的真实实现。
type ptySession struct {
	sess   *ssh.Session
	stdin  io.WriteCloser
	stdout io.Reader
	exitCh chan int
}

func (p *ptySession) Read(b []byte) (int, error)  { return p.stdout.Read(b) }
func (p *ptySession) Write(b []byte) (int, error) { return p.stdin.Write(b) }

func (p *ptySession) Resize(cols, rows uint16) error {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	return p.sess.WindowChange(int(rows), int(cols))
}

func (p *ptySession) ExitCode() <-chan int { return p.exitCh }

func (p *ptySession) Close() error {
	_ = p.stdin.Close()
	return p.sess.Close()
}

// awaitExit 等会话结束并把退出码投递到 exitCh(恰好一次)。
func (p *ptySession) awaitExit() {
	code := 0
	if err := p.sess.Wait(); err != nil {
		var exitErr *ssh.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitStatus()
		} else {
			code = -1 // 连接断开等非正常结束
		}
	}
	select {
	case p.exitCh <- code:
	default:
	}
	close(p.exitCh)
}

// ---- 系统信息采集 ----

// sysInfoScript 一次往返采集全部档案字段(key=value 行,便于解析)。
// 全部用最通用的口径:/proc/meminfo 替代 free、df -k 根分区,兼容最小化镜像。
const sysInfoScript = `. /etc/os-release 2>/dev/null
echo "os=$ID"
echo "name=$NAME"
echo "ver=$VERSION_ID"
echo "kernel=$(uname -r 2>/dev/null)"
echo "arch=$(uname -m 2>/dev/null)"
echo "cores=$(nproc 2>/dev/null || grep -c ^processor /proc/cpuinfo 2>/dev/null || echo 0)"
echo "memmb=$(awk '/MemTotal/{printf "%d", $2/1024}' /proc/meminfo 2>/dev/null)"
echo "diskgb=$(df -k / 2>/dev/null | tail -1 | awk '{print int($2/1048576)}')"
`

// SysInfo 采集系统档案。单个字段拿不到就留空/0,不致命。
func (c *sshClient) SysInfo(ctx context.Context) (*SysInfo, error) {
	out, _, err := c.Exec(ctx, sysInfoScript, 30*time.Second)
	if err != nil {
		return nil, err
	}
	info := &SysInfo{}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "os":
			info.OSName = strings.TrimSpace(v)
		case "ver":
			info.OSVersion = strings.TrimSpace(v)
		case "kernel":
			info.Kernel = strings.TrimSpace(v)
		case "arch":
			info.Arch = strings.TrimSpace(v)
		case "cores":
			info.CPUCores = atoi(v)
		case "memmb":
			info.MemoryMB = atoi(v)
		case "diskgb":
			info.DiskGB = atoi(v)
		}
	}
	return info, nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// ---- 工具探测 ----

// detectToolsScript 批量探测:I|name=已装 / N|name=未装,随后逐个跑版本命令。
const detectMarker = "__ARTEX_V__"

func buildDetectScript(names []string) string {
	var b strings.Builder
	b.WriteString("for t in")
	for _, n := range names {
		b.WriteString(" '" + strings.ReplaceAll(n, "'", "") + "'")
	}
	b.WriteString("; do\n")
	b.WriteString("  if command -v \"$t\" >/dev/null 2>&1; then echo \"I|$t\"; else echo \"N|$t\"; fi\n")
	b.WriteString("done\n")
	for _, n := range names {
		probe := ToolProbeByName(n)
		if probe == nil || probe.VersionCmd == "" {
			continue
		}
		fmt.Fprintf(&b, "echo %q\n", detectMarker+n)
		fmt.Fprintf(&b, "%s 2>&1 | head -3\n", probe.VersionCmd)
	}
	return b.String()
}

// DetectTools 探测工具安装状态与版本(单次批量往返,~30s 上限)。
func (c *sshClient) DetectTools(ctx context.Context, names []string) ([]ToolDetection, error) {
	if len(names) == 0 {
		return []ToolDetection{}, nil
	}
	out, _, err := c.Exec(ctx, buildDetectScript(names), 60*time.Second)
	if err != nil {
		return nil, err
	}
	return parseDetectOutput(names, string(out)), nil
}

// parseDetectOutput 解析探测脚本输出:I|name=已装 / N|name=未装 +
// marker 分段的版本命令输出。纯函数,便于单测。
func parseDetectOutput(names []string, out string) []ToolDetection {
	dets := make([]ToolDetection, 0, len(names))
	installed := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "I|"); ok {
			installed[strings.TrimSpace(rest)] = true
		}
	}
	// 版本段:marker 后到下一个 marker/EOF 之间的输出交给各工具的解析器。
	versionBy := map[string]string{}
	cur := ""
	var vbuf []string
	flush := func() {
		if cur != "" {
			versionBy[cur] = parseToolVersion(cur, strings.Join(vbuf, "\n"))
		}
		cur, vbuf = "", nil
	}
	for _, line := range strings.Split(out, "\n") {
		if name, ok := strings.CutPrefix(strings.TrimSpace(line), detectMarker); ok {
			flush()
			cur = name
			continue
		}
		if cur != "" {
			vbuf = append(vbuf, line)
		}
	}
	flush()

	for _, n := range names {
		// 未安装的工具版本一律空串:版本命令的输出是 "bash: line N: xxx: command
		// not found",版本正则会误抓那个行号当版本号。
		v := ""
		if installed[n] {
			v = versionBy[n]
		}
		dets = append(dets, ToolDetection{
			ToolName:  n,
			Installed: installed[n],
			Version:   v,
		})
	}
	return dets
}

// versionRe 抓「版本号形状」的第一段数字(3.11.8 / v6.2.6 / 1.9.0-jumbo-1)。
var versionRe = regexp.MustCompile(`v?(\d+[\w.+\-]*)`)

// parseToolVersion 从版本命令输出解析版本串。probe 自带解析器时优先。
func parseToolVersion(name, out string) string {
	probe := ToolProbeByName(name)
	if probe == nil {
		return ""
	}
	if probe.ParseVersion != nil {
		return probe.ParseVersion(out)
	}
	m := versionRe.FindStringSubmatch(out)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}
