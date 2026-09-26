package vpscenter

// 远程端口转发回归测试(契约 C2_CONTRACTS.md §12):进程内用 x/crypto/ssh 起一个
// 支持 tcpip-forward/forwarded-tcpip 的真实 SSH server(127.0.0.1 随机端口,密码
// 认证),不打真实机器、不用 os/exec。覆盖:转发 TCP 往返字节一致、Stop 后远端
// 端口关闭且连接断开(幂等)、端口被占报错、SSH 断开经 ErrCh 可感知。

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// forwardTestSSHServer 是进程内支持远程端口转发的 SSH server。
type forwardTestSSHServer struct {
	t *testing.T

	mu     sync.Mutex
	binds  map[uint32]net.Listener // 远端申请的监听(端口 → listener)
	conns  []net.Conn              // 活跃的底层 SSH 连接(kill 用)
	closed chan string             // 每条 SSH 连接退出时投递一条(容量 8)

	ln net.Listener
}

// startForwardTestSSHServer 起测试 server,生命周期挂 t.Cleanup。
func startForwardTestSSHServer(t *testing.T) *forwardTestSSHServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成主机密钥: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("构造主机 signer: %v", err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if conn.User() == "testuser" && string(password) == "testpass" {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("认证失败")
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("测试 server 监听失败: %v", err)
	}
	ts := &forwardTestSSHServer{t: t, binds: map[uint32]net.Listener{}, closed: make(chan string, 8), ln: ln}
	t.Cleanup(ts.close)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go ts.serveConn(conn, cfg)
		}
	}()
	return ts
}

// hostPort 返回测试 server 的 SSH 连接地址。
func (ts *forwardTestSSHServer) hostPort() (string, int) {
	ts.t.Helper()
	h, p, err := net.SplitHostPort(ts.ln.Addr().String())
	if err != nil {
		ts.t.Fatalf("解析监听地址: %v", err)
	}
	var pnum int
	_, _ = fmt.Sscanf(p, "%d", &pnum)
	return h, pnum
}

// kill 断开全部活跃 SSH 连接(模拟机器网络故障,验证 ErrCh 感知)。
func (ts *forwardTestSSHServer) kill() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for _, c := range ts.conns {
		_ = c.Close()
	}
}

func (ts *forwardTestSSHServer) close() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	_ = ts.ln.Close()
	for _, l := range ts.binds {
		_ = l.Close()
	}
	for _, c := range ts.conns {
		_ = c.Close()
	}
}

// serveConn 处理一条客户端连接:认证 → 全局请求(tcpip-forward 系)与通道并发处理。
func (ts *forwardTestSSHServer) serveConn(conn net.Conn, cfg *ssh.ServerConfig) {
	ts.mu.Lock()
	ts.conns = append(ts.conns, conn)
	ts.mu.Unlock()

	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer sconn.Close()
	go ts.handleGlobalReqs(sconn, reqs)
	for nch := range chans {
		// 远程转发方向上客户端不开通道;万一开了(协议探测)直接拒。
		_ = nch.Reject(ssh.UnknownChannelType, "unsupported")
	}
	select {
	case ts.closed <- "conn":
	default:
	}
}

// handleGlobalReqs 处理 tcpip-forward / cancel-tcpip-forward(RFC 4254 §7):
// 申请即在本进程 net.Listen,连接到达时朝客户端开 forwarded-tcpip 通道并双向拷贝。
func (ts *forwardTestSSHServer) handleGlobalReqs(sconn *ssh.ServerConn, reqs <-chan *ssh.Request) {
	for req := range reqs {
		switch req.Type {
		case "tcpip-forward":
			var p tcpipForwardReq
			if err := ssh.Unmarshal(req.Payload, &p); err != nil {
				_ = req.Reply(false, nil)
				continue
			}
			ln, lerr := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p.Port))
			if lerr != nil {
				_ = req.Reply(false, nil) // 端口被占等:拒绝,客户端要原样报错
				continue
			}
			bound := p.Port
			if bound == 0 {
				bound = listenerPort(ln)
			}
			ts.mu.Lock()
			ts.binds[bound] = ln
			ts.mu.Unlock()
			reply := []byte(nil)
			if p.Port == 0 {
				reply = ssh.Marshal(tcpipForwardReq{Port: bound})
			}
			_ = req.Reply(true, reply)
			go ts.acceptForward(sconn, ln, bound)
		case "cancel-tcpip-forward":
			var p tcpipForwardReq
			if err := ssh.Unmarshal(req.Payload, &p); err == nil {
				ts.mu.Lock()
				if ln, ok := ts.binds[p.Port]; ok {
					_ = ln.Close()
					delete(ts.binds, p.Port)
				}
				ts.mu.Unlock()
			}
			_ = req.Reply(true, nil)
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

// acceptForward 收远端监听上的 TCP 连接,开 forwarded-tcpip 通道回客户端并互拷。
func (ts *forwardTestSSHServer) acceptForward(sconn *ssh.ServerConn, ln net.Listener, bound uint32) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener 已关(cancel/测试结束)
		}
		ch, chReqs, cerr := sconn.OpenChannel("forwarded-tcpip", ssh.Marshal(forwardedTCPIP{
			Addr: "127.0.0.1", Port: bound, OriginAddr: "127.0.0.1", OriginPort: 55555,
		}))
		if cerr != nil {
			_ = conn.Close()
			continue
		}
		go ssh.DiscardRequests(chReqs)
		go pipeTCPChannel(conn, ch)
	}
}

// pipeTCPChannel 测试侧的 TCP↔通道互拷(与被测 pipeForwardPair 对称)。
func pipeTCPChannel(conn net.Conn, ch ssh.Channel) {
	defer conn.Close()
	defer ch.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(conn, ch); done <- struct{}{} }()
	go func() { _, _ = io.Copy(ch, conn); done <- struct{}{} }()
	<-done
}

func listenerPort(ln net.Listener) uint32 {
	_, p, _ := net.SplitHostPort(ln.Addr().String())
	var n int
	_, _ = fmt.Sscanf(p, "%d", &n)
	return uint32(n)
}

// newForwardTestManager 构造走真实 Dial 的 Manager(凭据与测试 server 匹配)。
func newForwardTestManager(t *testing.T, ts *forwardTestSSHServer) *Manager {
	t.Helper()
	host, port := ts.hostPort()
	return NewManager(newFakeStore(&MachineRuntime{
		Machine:  Machine{ID: 7, Name: "载体机", Host: host, Port: port, Username: "testuser", AuthType: AuthPassword},
		Password: "testpass",
	}), nil)
}

// startEchoServer 起一个「读到什么回什么」的本地 TCP 服务,当转发目标。
func startEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo 服务监听失败: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	return ln.Addr().String()
}

// TestRemoteForwardRoundTrip 主链路:remotePort=0 申请(远端分配)→ 经 VPS 侧端口
// 双向往返字节一致;并发多条连接互不串扰。
func TestRemoteForwardRoundTrip(t *testing.T) {
	ts := startForwardTestSSHServer(t)
	m := newForwardTestManager(t, ts)
	echoAddr := startEchoServer(t)

	fwd, err := m.OpenRemoteForward(7, 0, echoAddr)
	if err != nil {
		t.Fatalf("OpenRemoteForward: %v", err)
	}
	bound := fwd.RemotePort()
	if bound <= 0 || bound > 65535 {
		t.Fatalf("远端分配端口非法: %d", bound)
	}
	if fwd.LocalAddr() != echoAddr || fwd.MachineID() != 7 {
		t.Fatalf("转发视图不对: %+v", fwd)
	}

	// 两轮往返(第二轮新连接):发送字节必须逐字节回来。
	for i := 0; i < 2; i++ {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", bound), 3*time.Second)
		if err != nil {
			t.Fatalf("第 %d 条连接拨远端端口失败: %v", i+1, err)
		}
		payload := fmt.Sprintf("PING-ARTEX-%d-通道字节\n", i)
		if _, err := conn.Write([]byte(payload)); err != nil {
			t.Fatalf("写转发通道: %v", err)
		}
		buf := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, buf); err != nil {
			t.Fatalf("读回声(经 SSH 转发): %v", err)
		}
		_ = conn.Close()
		if string(buf) != payload {
			t.Fatalf("往返字节不一致: sent=%q got=%q", payload, string(buf))
		}
	}
}

// TestRemoteForwardPortConflict 端口被占:已绑定端口上再申请必须报错;Stop 释放后
// 同端口可再申请(释放语义)。
func TestRemoteForwardPortConflict(t *testing.T) {
	ts := startForwardTestSSHServer(t)
	m := newForwardTestManager(t, ts)
	echoAddr := startEchoServer(t)

	fwd, err := m.OpenRemoteForward(7, 0, echoAddr)
	if err != nil {
		t.Fatalf("OpenRemoteForward: %v", err)
	}
	bound := fwd.RemotePort()

	if dup, derr := m.OpenRemoteForward(7, bound, echoAddr); derr == nil {
		_ = dup.Stop()
		t.Fatalf("端口 %d 已被占用,第二次申请应报错", bound)
	} else if !containsAny(derr.Error(), []string{"拒绝", "占用"}) {
		t.Fatalf("错误文案应说明远端拒绝: %v", derr)
	}

	if err := fwd.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	re, err := m.OpenRemoteForward(7, bound, echoAddr)
	if err != nil {
		t.Fatalf("Stop 释放后同端口应可再申请: %v", err)
	}
	_ = re.Stop()
}

// TestRemoteForwardStop Stop 语义:远端端口关闭(拨号失败)、SSH 连接断开(服务端
// 感知退出)、ErrCh 不投递(正常停止非异常)、重复 Stop 幂等。
func TestRemoteForwardStop(t *testing.T) {
	ts := startForwardTestSSHServer(t)
	m := newForwardTestManager(t, ts)
	echoAddr := startEchoServer(t)

	fwd, err := m.OpenRemoteForward(7, 0, echoAddr)
	if err != nil {
		t.Fatalf("OpenRemoteForward: %v", err)
	}
	bound := fwd.RemotePort()
	if err := fwd.Stop(); err != nil {
		t.Fatalf("首次 Stop 应成功: %v", err)
	}
	if err := fwd.Stop(); err != nil {
		t.Fatalf("重复 Stop 应幂等(nil): %v", err)
	}
	// 远端端口已关:2s 内拨号必须失败。
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, derr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", bound), time.Second)
		if derr != nil {
			break
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatalf("Stop 后远端端口 %d 仍可连通(未释放)", bound)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// SSH 连接已断:服务端连接退出。
	select {
	case <-ts.closed:
	case <-time.After(3 * time.Second):
		t.Fatalf("Stop 后服务端未感知连接断开")
	}
	// 正常停止不投递 ErrCh。
	select {
	case err := <-fwd.ErrCh():
		t.Fatalf("正常 Stop 不应回报异常: %v", err)
	default:
	}
}

// TestRemoteForwardDetectsDisconnect SSH 断开感知:杀掉服务端连接后,ErrCh 必须在
// 3s 内投递断开原因(server 侧据此标注运行时状态)。
func TestRemoteForwardDetectsDisconnect(t *testing.T) {
	ts := startForwardTestSSHServer(t)
	m := newForwardTestManager(t, ts)
	echoAddr := startEchoServer(t)

	fwd, err := m.OpenRemoteForward(7, 0, echoAddr)
	if err != nil {
		t.Fatalf("OpenRemoteForward: %v", err)
	}
	defer fwd.Stop() //nolint:errcheck
	ts.kill()
	select {
	case err := <-fwd.ErrCh():
		if err == nil {
			t.Fatal("断开原因不应为 nil")
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("SSH 断开后 3s 内 ErrCh 未投递")
	}
}

// TestRemoteForwardRejectsFakeClient 注入的 fake SSHClient(无原生连接)应报明确
// 错误而不是 panic——守卫 rawSSHClient 断言路径。
func TestRemoteForwardRejectsFakeClient(t *testing.T) {
	st := newFakeStore(&MachineRuntime{
		Machine: Machine{ID: 7, Name: "载体机", Host: "127.0.0.1", Port: 22, Username: "u", AuthType: AuthPassword},
	})
	m := NewManager(st, nil)
	m.newClient = func(SSHConfig) (SSHClient, error) { return &fakeSSH{}, nil }
	if _, err := m.OpenRemoteForward(7, 10000, "127.0.0.1:1"); err == nil {
		t.Fatal("fake 连接应被拒绝")
	}
}

// containsAny 小工具:错误文案关键词断言。
func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
