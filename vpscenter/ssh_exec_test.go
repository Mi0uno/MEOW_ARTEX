package vpscenter

// Exec 回归测试:在进程内用 x/crypto/ssh 起一个真实 SSH server(127.0.0.1 随机端口,
// 密码认证),复现并钉死「命令瞬间退出但输出与 exit-status 背靠背到达时 Exec 读到空输出」
// 的竞态。server 端不起真 shell、不用 os/exec:直接在 session channel 上写数据、
// 回 exit-status、Close,这正是触发竞态的最小时序。

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// startTestSSHServer 起一个进程内 SSH server,返回 host/port。每个 session 的
// exec 请求在独立 goroutine 中交给 handle 处理。server 生命周期挂到 t.Cleanup。
func startTestSSHServer(t *testing.T, handle func(ch ssh.Channel, cmd string)) (host string, port int) {
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
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // server 已随测试关闭
			}
			go serveSSHConn(conn, cfg, handle)
		}
	}()

	h, p, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("解析监听地址: %v", err)
	}
	var pnum int
	_, _ = fmt.Sscanf(p, "%d", &pnum)
	return h, pnum
}

// serveSSHConn 处理一条客户端连接:过认证,接受 session channel,分发 exec 请求。
func serveSSHConn(conn net.Conn, cfg *ssh.ServerConfig, handle func(ssh.Channel, string)) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		if nch.ChannelType() != "session" {
			_ = nch.Reject(ssh.UnknownChannelType, "unsupported channel")
			continue
		}
		ch, chReqs, err := nch.Accept()
		if err != nil {
			continue
		}
		go serveSession(ch, chReqs, handle)
	}
}

// serveSession 读 session channel 上的请求,把 exec 交给 handle(参考
// x/crypto/ssh 官方 test fixture 的写法)。
func serveSession(ch ssh.Channel, reqs <-chan *ssh.Request, handle func(ssh.Channel, string)) {
	defer ch.Close()
	for req := range reqs {
		if req.Type != "exec" {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			continue
		}
		var payload struct{ Command string }
		if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
			_ = req.Reply(false, nil)
			return
		}
		_ = req.Reply(true, nil)
		handle(ch, payload.Command)
		return
	}
}

// scriptedHandler 按脚本应答:先写 stdout/stderr,再回 exit-status,最后关通道——
// 数据与退出状态连发,客户端 Run 返回时输出拷贝 goroutine 可能尚未完成。
func scriptedHandler(stdoutData, stderrData string, code uint32) func(ssh.Channel, string) {
	return func(ch ssh.Channel, _ string) {
		if stdoutData != "" {
			if _, err := ch.Write([]byte(stdoutData)); err != nil {
				return
			}
		}
		if stderrData != "" {
			if _, err := ch.Stderr().Write([]byte(stderrData)); err != nil {
				return
			}
		}
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{code}))
		_ = ch.Close()
	}
}

// hangHandler 只应答 exec,随后挂起比客户端超时更久:客户端超时后会
// Signal+Close 杀会话,Exec 按超时路径返回。注意不能靠 ch.Read 阻塞等待——
// 客户端 Session.start() 在 Stdin 为空时会立刻 CloseWrite,服务端 stdin 一开始
// 就能读到 EOF,但通道并未真正关闭(真关闭由客户端超时杀会话触发)。
func hangHandler(ch ssh.Channel, _ string) {
	time.Sleep(3 * time.Second)
}

// dialTestServer 用被测代码的真实入口 Dial 连上测试 server。
func dialTestServer(t *testing.T, host string, port int) SSHClient {
	t.Helper()
	cl, err := Dial(SSHConfig{
		Host:     host,
		Port:     port,
		Username: "testuser",
		AuthType: AuthPassword,
		Password: "testpass",
		Timeout:  5 * time.Second,
	})
	if err != nil {
		t.Fatalf("拨号测试 SSH server: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	return cl
}

// TestExecCapturesFastExitOutput 钉死核心竞态:server 端写完 stdout 立刻回
// exit-status 0 并 Close(等价于远端 `echo XY` 瞬间退出)。修复前 Exec 会随机
// 读到空输出;5 连发全非空才算过。
func TestExecCapturesFastExitOutput(t *testing.T) {
	host, port := startTestSSHServer(t, scriptedHandler("HELLO\n", "", 0))
	cl := dialTestServer(t, host, port)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		out, code, err := cl.Exec(ctx, "anything", 10*time.Second)
		if err != nil {
			t.Fatalf("第 %d 次 Exec: %v", i+1, err)
		}
		if code != 0 {
			t.Fatalf("第 %d 次 Exec 退出码 = %d, 期望 0", i+1, code)
		}
		if !strings.Contains(string(out), "HELLO") {
			t.Fatalf("第 %d 次 Exec 输出 = %q, 缺少 HELLO(竞态:Run 返回时输出尚未拷贝完成)", i+1, out)
		}
	}
}

// TestExecMergesStderr 验证 stdout+stderr 合并语义保留:两边数据都要出现在
// 返回的合并输出里,退出码正确。
func TestExecMergesStderr(t *testing.T) {
	host, port := startTestSSHServer(t, scriptedHandler("OUTPART\n", "ERRDATA", 0))
	cl := dialTestServer(t, host, port)
	out, code, err := cl.Exec(context.Background(), "anything", 10*time.Second)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if code != 0 {
		t.Fatalf("退出码 = %d, 期望 0", code)
	}
	s := string(out)
	if !strings.Contains(s, "OUTPART") || !strings.Contains(s, "ERRDATA") {
		t.Fatalf("合并输出 = %q, 应同时含 stdout(OUTPART)与 stderr(ERRDATA)内容", s)
	}
}

// TestExecNonZeroExit 验证命令自身非零退出码是正常结果:exitCode 透传、err 为 nil,
// 且退出前的输出不丢。
func TestExecNonZeroExit(t *testing.T) {
	host, port := startTestSSHServer(t, scriptedHandler("SEVEN\n", "", 7))
	cl := dialTestServer(t, host, port)
	out, code, err := cl.Exec(context.Background(), "anything", 10*time.Second)
	if err != nil {
		t.Fatalf("非零退出码不应报传输错误: %v", err)
	}
	if code != 7 {
		t.Fatalf("退出码 = %d, 期望 7", code)
	}
	if !strings.Contains(string(out), "SEVEN") {
		t.Fatalf("输出 = %q, 缺少 SEVEN", out)
	}
}

// TestExecTimeout 验证超时路径:server 应答 exec 后故意不回 exit-status,
// 短超时后 Exec 应返回 -1 + 超时错误,且不能卡死。
func TestExecTimeout(t *testing.T) {
	host, port := startTestSSHServer(t, hangHandler)
	cl := dialTestServer(t, host, port)
	start := time.Now()
	_, code, err := cl.Exec(context.Background(), "anything", 300*time.Millisecond)
	if err == nil {
		t.Fatal("期望超时错误, 得到 nil")
	}
	if code != -1 {
		t.Fatalf("退出码 = %d, 期望 -1", code)
	}
	if !strings.Contains(err.Error(), "超时") {
		t.Fatalf("错误 = %v, 期望包含「超时」", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("超时返回耗时 %v, 会话未被及时杀死", elapsed)
	}
}
