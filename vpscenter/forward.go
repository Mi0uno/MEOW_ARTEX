package vpscenter

// SSH 远程端口转发(vps 载体监听器;契约 C2_CONTRACTS.md §12)。
//
// 机制:平台本机的 C2 监听器照常绑定本机地址,VPS 只做出入口——本文件经
// x/crypto/ssh 的「tcpip-forward」全局请求在 VPS 上申请监听 remote_port,循环
// Accept 远端送来的「forwarded-tcpip」通道,每条通道 dial 本地监听地址并双向
// io.Copy。流量路径:beacon → VPS:remote_port → SSH 隧道 → 平台 listener。
//
// 硬纪律(契约 §0/§12):禁止 os/exec、禁止 shell 出 ssh 命令——「在 VPS 上开
// 监听端口」是 RFC 4254 §7 的协议能力,不走 `ssh -R` 子进程;拨号与凭据复用
// ExecCommand 同源路径(GetMachineRuntime 解封 → sshConfig → newClient)。
//
// 文件内另含 ExecCommandDir:§14 的 gosrc VPS 构建(归 Agent A)需要显式工作目录
// <workspace_base>/c2build_<payloadID>,而一期 ExecCommand 的工作目录固定为任务
// 工作区(task_<id>/manual_<ts>);放本文件以免触碰 manager.go 的一期冻结面。
// 实现只是 exec 核心换 dirFn(与 ApplyToolset 同一模式),无新逻辑。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// RFC 4254 §7 线格式:tcpip-forward / cancel-tcpip-forward 的请求与应答载荷。
type tcpipForwardReq struct {
	Host string
	Port uint32
}

// forwardedTCPIP 是远端开来 forwarded-tcpip 通道时的 ExtraData
// (被连接的地址/端口 + 发起方地址/端口;端口转发不路由用,解析只为健壮)。
type forwardedTCPIP struct {
	Addr       string
	Port       uint32
	OriginAddr string
	OriginPort uint32
}

// RemoteForward 是一条「VPS:remotePort → 平台 localAddr」的 SSH 远程转发。
// 生命周期:OpenRemoteForward 建 → server 侧经 ErrCh 感知断开 → Stop 幂等终止。
type RemoteForward struct {
	machineID  int64
	remoteHost string // 申请绑定的远端地址(固定 0.0.0.0:载体是公网出入口)
	remotePort int    // 实际生效端口(远端确认;申请 0 时取远端分配值)
	localAddr  string
	conn       *ssh.Client

	mu      sync.Mutex
	stopped bool

	stopOnce sync.Once
	done     chan struct{} // serve 循环退出时关闭
	errCh    chan error    // 缓冲 1:非 Stop 的异常退出(SSH 断开)投递一次原因
}

// rawSSHClient 从 Dial 返回的 SSHClient 取底层 *ssh.Client。转发要发全局请求/
// Accept 通道,SSHClient 接口没有这层能力;同包类型断言,测试注入的 fake 连接
// 到这里会拿到 false(调用方报明确错误,不 panic)。
func rawSSHClient(cl SSHClient) (*ssh.Client, bool) {
	if sc, ok := cl.(*sshClient); ok && sc != nil && sc.conn != nil {
		return sc.conn, true
	}
	return nil, false
}

// OpenRemoteForward 在机器 machineID 上申请监听 remotePort 并把流量转回
// localAddr(平台本地监听器的实际地址,如 127.0.0.1:41234)。remotePort=0 时由
// 远端分配,实际端口经 RemotePort() 取回。
//
// 失败路径:机器不存在/连不上/远端拒绝(端口被占、权限不足、sshd 关闭端口转发)
// 都在本调用内原样报错并关闭连接;成功后转发在后台服务,SSH 断开经 ErrCh 回报。
func (m *Manager) OpenRemoteForward(machineID int64, remotePort int, localAddr string) (*RemoteForward, error) {
	if machineID <= 0 {
		return nil, errors.New("machine_id 必填")
	}
	if remotePort < 0 || remotePort > 65535 {
		return nil, fmt.Errorf("remote_port 无效(0=远端分配,1-65535): %d", remotePort)
	}
	if strings.TrimSpace(localAddr) == "" {
		return nil, errors.New("local_addr 必填(平台本地监听器的实际地址)")
	}
	rt, err := m.store.GetMachineRuntime(machineID)
	if err != nil {
		return nil, err
	}
	if rt == nil {
		return nil, fmt.Errorf("机器 #%d 不存在", machineID)
	}
	cl, err := m.newClient(rt.sshConfig(m.checkDialTimeout))
	if err != nil {
		return nil, err
	}
	conn, ok := rawSSHClient(cl)
	if !ok {
		_ = cl.Close()
		return nil, errors.New("远程转发需要原生 SSH 连接(测试 fake 不支持)")
	}

	// RFC 4254 §7.1:tcpip-forward 全局申请(wantReply=true,拿远端确认)。
	bindHost := "0.0.0.0"
	okResp, reply, err := conn.SendRequest("tcpip-forward", true,
		ssh.Marshal(tcpipForwardReq{Host: bindHost, Port: uint32(remotePort)}))
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("发送 tcpip-forward 失败: %w", err)
	}
	if !okResp {
		_ = conn.Close()
		return nil, fmt.Errorf("机器 «%s» 拒绝监听 %s:%d(端口被占/权限不足/sshd 未启用端口转发)",
			rt.Host, bindHost, remotePort)
	}
	bound := remotePort
	if bound == 0 && len(reply) > 0 {
		var rp tcpipForwardReq
		if uerr := ssh.Unmarshal(reply, &rp); uerr == nil && rp.Port > 0 {
			bound = int(rp.Port)
		}
	}

	f := &RemoteForward{
		machineID:  machineID,
		remoteHost: bindHost,
		remotePort: bound,
		localAddr:  localAddr,
		conn:       conn,
		done:       make(chan struct{}),
		errCh:      make(chan error, 1),
	}
	// 注册 forwarded-tcpip 通道处理器(客户端侧接受服务端开来的转发通道的唯一
	// 入口;连接断开时该通道关闭,serve 据此感知并回报)。须在 serve 前注册。
	incoming := conn.HandleChannelOpen("forwarded-tcpip")
	if incoming == nil {
		_ = conn.Close()
		return nil, errors.New("forwarded-tcpip 通道处理器注册失败(类型已被占用)")
	}
	go f.serve(incoming)
	m.logf("机器 «%s»(#%d) 远程转发已建立:%s:%d → %s", rt.Name, machineID, bindHost, bound, localAddr)
	return f, nil
}

// MachineID 返回载体机器 id。
func (f *RemoteForward) MachineID() int64 { return f.machineID }

// RemotePort 返回实际生效的远端端口(申请 0 时是远端分配值)。
func (f *RemoteForward) RemotePort() int { return f.remotePort }

// LocalAddr 返回转发目标(平台本地监听器地址)。
func (f *RemoteForward) LocalAddr() string { return f.localAddr }

// ErrCh 返回异常退出原因流(SSH 断开等;Stop 正常停止不投递)。缓冲 1,server 侧
// 应尽早消费以感知断开(日志+运行时状态标注;beacon 的重试机制兜底重连)。
func (f *RemoteForward) ErrCh() <-chan error { return f.errCh }

// serve 循环:消费远端开来的 forwarded-tcpip 通道,逐条 dial 本地地址并双向拷贝。
// incoming 在 SSH 连接断开(或 Stop 关连接)时关闭,循环退出。
func (f *RemoteForward) serve(incoming <-chan ssh.NewChannel) {
	defer close(f.done)
	for nch := range incoming {
		if nch.ChannelType() != "forwarded-tcpip" {
			_ = nch.Reject(ssh.UnknownChannelType, "unsupported channel type")
			continue
		}
		var fh forwardedTCPIP
		if uerr := ssh.Unmarshal(nch.ExtraData(), &fh); uerr != nil {
			_ = nch.Reject(ssh.ConnectionFailed, "malformed forwarded-tcpip payload")
			continue
		}
		local, derr := net.DialTimeout("tcp", f.localAddr, 5*time.Second)
		if derr != nil {
			// 本地监听不可达:拒绝该条通道(对 beacon 表现为连接被远端复位,
			// 它会按退避重试),转发主体继续服务后续连接。
			_ = nch.Reject(ssh.ConnectionFailed, "local listener unreachable")
			continue
		}
		ch, chReqs, aerr := nch.Accept()
		if aerr != nil {
			_ = local.Close()
			continue
		}
		go ssh.DiscardRequests(chReqs) // 转发通道无带内请求,丢弃即可
		go pipeForwardPair(local, ch)
	}
	// 通道流关闭 = SSH 连接断开:非 Stop 的异常退出,回报原因(server 感知)。
	f.mu.Lock()
	stopped := f.stopped
	f.mu.Unlock()
	if !stopped {
		select {
		case f.errCh <- fmt.Errorf("SSH 连接断开,转发 %s:%d → %s 已失效", f.remoteHost, f.remotePort, f.localAddr):
		default:
		}
	}
}

// pipeForwardPair 在本地 TCP 连接与 SSH 通道间双向拷贝;任一侧结束即关两侧,
// 让另一侧的 Copy 立刻返回(通道与连接都幂等 Close)。
func pipeForwardPair(local net.Conn, ch ssh.Channel) {
	defer local.Close()
	defer ch.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(local, ch); done <- struct{}{} }()
	go func() { _, _ = io.Copy(ch, local); done <- struct{}{} }()
	<-done
}

// Stop 停止转发(幂等):先向远端注销 tcpip-forward(尽力而为,连接可能已断),
// 再关 SSH 连接——关连接会解除 serve 的 Accept 阻塞并断开全部转发通道;最后
// 有界等待 serve 退出(3s,极端网络下不无限等)。返回关连接的错误:连接已断时
// 非 nil,属正常,调用方可忽略。
func (f *RemoteForward) Stop() error {
	var closeErr error
	f.stopOnce.Do(func() {
		f.mu.Lock()
		f.stopped = true
		f.mu.Unlock()
		_, _, _ = f.conn.SendRequest("cancel-tcpip-forward", false,
			ssh.Marshal(tcpipForwardReq{Host: f.remoteHost, Port: uint32(f.remotePort)}))
		closeErr = f.conn.Close()
	})
	select {
	case <-f.done:
	case <-time.After(3 * time.Second):
	}
	return closeErr
}

// ExecCommandDir 在机器上执行一条命令,工作目录显式指定(绝对路径;§14 的
// c2build_<payloadID> 构建工作区用)。语义与 ExecCommand 完全一致(包装
// mkdir+cd+bash -lc、落执行流水、完整输出返回),仅目录由调用方给定——避免
// 逐条命令落散落的 manual_<ts> 目录。source 沿用白名单(此处固定 manual)。
func (m *Manager) ExecCommandDir(ctx context.Context, machineID int64, dir, source, command string, timeout time.Duration) (output []byte, exitCode int, err error) {
	rec := ExecRecord{MachineID: machineID, Source: source, Command: command}
	out, code, xerr, _ := m.exec(ctx, rec, timeout, func(rt *MachineRuntime) string { return dir })
	return out, code, xerr
}
