package server

// VPS 中心 × agent 集成(契约 §6,Agent C 范围)。五个部分:
//
//  1. 远程执行环境:vpsContextResolver 给任务 worker 递一份 per-run 的
//     agent.VPSRunContext —— 同名同 schema 的 SSH 版 Bash + 系统提示词机器档案段;
//     文件类工具(Read/Write 等)保持 SDK 本机实现不动(它们操作本地工作区)。
//  2. 两个领域工具:vps_machine_info(读机器档案+工具清单)/ vps_install_toolset
//     (按名跑工具集安装脚本)。照 recon_tools.go 的 actool.CoreTool 闭包写法,
//     seed 进 tools 表绑 worker,经 hostTools 注入。
//  3. 任务级 REST:POST/DELETE /api/tasks/{id}/vps、GET /api/tasks/{id}/vps-environment
//     (响应形状与 web/src/lib/api.ts 冻结版对齐)。
//  4. 创建即指派:assignVPSOnCreate,Manager.CreateTaskWithOptions 在任务进入
//     running 之前调用,失败不阻断建任务。
//  5. 终态自动释放:releaseTaskVPSOnTerminal,挂在 SetTaskTerminalHook 既有回调链
//     (与报告归档组合,不覆盖)。
//
// 铁律:除终端/agent 经 Manager 外,任何人不得绕过 Manager 直接 SSH;新代码禁止 os/exec。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/agent"
	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/vpscenter"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/permission"
)

// SSH 版 Bash 的执行参数(与 SDK Bash 语义对齐:缺省 120s、上限 600s)。
const (
	vpsBashDefaultTimeout = 120 * time.Second
	vpsBashMaxTimeout     = 600 * time.Second
	// vpsBashOutputLimit 回传给模型的输出上限(截尾;完整输出在执行流水里)。
	vpsBashOutputLimit = 256 << 10
	// vpsToolsetOutputTail 工具集安装结果回传给模型的输出尾部长度。
	vpsToolsetOutputTail = 4 << 10
)

// ---- 1. 远程执行环境(worker 的 SSH Bash + 提示词注入) ----

// vpsContextResolver 返回挂在任务 agent(worker/planner/main)上的 per-run 解析器
// (与 agentsForTask 里 ProxyAddrForTask 的解析同位、同模式):每个 run 开头查一次
// MachineForTask,任务晚指派/改派的下一个意图即生效;未指派返回 nil(维持本机执行,
// 零开销)。机器句柄不在工具执行路径上散落查询——一次解析,整个 run 复用。
//
// 严格模式:查询出错 ≠ 未指派。任务可能正绑定在远程机器上,此刻静默回落本机执行
// 就是「掉马甲」——宁可整个 run 在远端语义上失败,也绝不把命令泄漏到平台本机。
// 因此 err != nil 时返回一个 fail-closed 占位 Bash(同名同 schema、拒绝执行),
// 而不是 nil。
func (s *Server) vpsContextResolver() func(taskID int64) *agent.VPSRunContext {
	return func(taskID int64) *agent.VPSRunContext {
		mgr := VPSManager()
		if mgr == nil || taskID <= 0 {
			return nil
		}
		machine, err := mgr.MachineForTask(taskID)
		if err != nil {
			return &agent.VPSRunContext{
				Bash:        s.vpsBashUnavailableTool(err),
				SystemBlock: vpsUnavailableSystemBlock(err),
			}
		}
		if machine == nil {
			return nil
		}
		return &agent.VPSRunContext{
			Bash:        s.vpsBashTool(taskID, machine),
			SystemBlock: s.vpsSystemBlock(machine, taskID),
		}
	}
}

// vpsBashUnavailableTool fail-closed 占位 Bash:与 SDK Bash 同名同 schema(替换后
// 模型视角不变),任何调用都明确报错,绝不落到本机执行。错误信息区分「指派在但查
// 不出来」与「命令失败」,引导模型稍后重试而不是对目标下结论。
func (s *Server) vpsBashUnavailableTool(resolveErr error) actool.CoreTool {
	ref := actool.NewBash()
	return actool.Build(actool.Spec{
		Name:        ref.Name(),
		Description: ref.Description(),
		Prompt:      ref.Prompt(),
		Schema:      ref.InputSchema(),
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
			return actool.Errorf(fmt.Sprintf(
				"任务执行环境无法确认(读取 VPS 指派失败:%v)。为杜绝命令意外落到平台本机执行,"+
					"Bash 已暂停;这是平台侧故障,不是命令失败,请稍后重试。", resolveErr)), nil
		},
	})
}

// vpsUnavailableSystemBlock fail-closed 期间追加的系统提示段:告诉模型为什么 Bash
// 会报错、该怎么做(等待恢复),避免模型误判成目标不可达或转投本机工具。
func vpsUnavailableSystemBlock(resolveErr error) string {
	return "\n\n**执行环境：远程 VPS（指派状态暂时无法确认）**\n" +
		"平台此刻读取本任务的 VPS 指派失败(" + resolveErr.Error() + ")。" +
		"为杜绝命令落到平台本机,Bash 暂被禁用;请稍后重试(下一个动作会重新解析指派)。\n"
}

// vpsBashTool 构造 SSH 版 Bash:与 SDK 默认 Bash 同名、同 schema、同描述/提示
// (LLM 视角不变),Call 改走 Manager.ExecCommand(source=agent)。命令由 Manager
// 统一包装 mkdir -p <ws> && cd <ws> && bash -lc '<command>'。
func (s *Server) vpsBashTool(taskID int64, machine *vpscenter.Machine) actool.CoreTool {
	// 以 SDK 现货 Bash 为蓝本拷贝模型可见面,后续 SDK 调整 schema 自动跟随。
	ref := actool.NewBash()
	return actool.Build(actool.Spec{
		Name:        ref.Name(),
		Description: ref.Description(),
		Prompt:      ref.Prompt(),
		Schema:      ref.InputSchema(),
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed() // 任务 Guard 钩子已逐调用把关,同其余 host 工具
		},
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			var p struct {
				Command         string `json:"command"`
				TimeoutMS       int    `json:"timeout_ms"`
				RunInBackground bool   `json:"run_in_background"`
			}
			if err := json.Unmarshal(in, &p); err != nil {
				return actool.Errorf("bad json: " + err.Error()), nil
			}
			if strings.TrimSpace(p.Command) == "" {
				return actool.Errorf("Error: command is required"), nil
			}
			if p.RunInBackground {
				// SSH 一次性 exec 没有本机那套后台任务管理器:明确告知,别静默前台化。
				return actool.Errorf("远程 VPS 执行暂不支持 run_in_background=true。" +
					"请直接前台运行;确需后台化用 nohup ... >/dev/null 2>&1 & 启动,再用 Bash 轮询输出文件。"), nil
			}
			mgr := VPSManager()
			if mgr == nil {
				return actool.Errorf("VPS 中心未初始化,无法在远程机器执行命令"), nil
			}
			output, exitCode, err := mgr.ExecCommand(ctx, machine.ID, taskID, vpscenter.SourceAgent,
				p.Command, vpsBashTimeout(p.TimeoutMS))
			if err != nil {
				// exitCode -1 = 连接/传输层失败:让模型明确知道是连不上机器,而非命令失败。
				return actool.Errorf(fmt.Sprintf(
					"命令未能送达机器 «%s»(%s:%d):%v。这是传输层失败(SSH 连不上/超时),不是命令执行失败;"+
						"请勿据此对目标下结论,可稍后重试或改用平台本机工具。", machine.Name, machine.Host, machine.Port, err)), nil
			}
			out, truncated := truncateVPSOutput(output, vpsBashOutputLimit)
			if truncated {
				out += fmt.Sprintf("\n…[输出超过 %d KB 已截断;完整输出见平台「VPS 中心 → 执行流水」]", vpsBashOutputLimit/1024)
			}
			return actool.Text(fmt.Sprintf("exit_code=%d\n%s", exitCode, out)), nil
		},
	})
}

// vpsBashTimeout 把 Bash 入参的 timeout_ms 解析成执行超时:缺省 120s,钳到 ≤600s。
func vpsBashTimeout(timeoutMS int) time.Duration {
	if timeoutMS <= 0 {
		return vpsBashDefaultTimeout
	}
	d := time.Duration(timeoutMS) * time.Millisecond
	if d > vpsBashMaxTimeout {
		return vpsBashMaxTimeout
	}
	return d
}

// truncateVPSOutput 把输出截到 limit 字节内(UTF-8 安全回退,不拦腰截断多字节字符)。
func truncateVPSOutput(out []byte, limit int) (string, bool) {
	if len(out) <= limit {
		return string(out), false
	}
	s := string(out[:limit])
	for i := 0; i < utf8.UTFMax-1 && len(s) > 0; i++ {
		if r, size := utf8.DecodeLastRuneInString(s); r != utf8.RuneError || size > 1 {
			break
		}
		s = s[:len(s)-1]
	}
	return s, true
}

// tailVPSOutput 取输出尾部 ≤limit 字节(UTF-8 安全),用于安装结果摘要。
// 尾切起点可能落在多字节字符中间:残缺的是开头部分字节,须从头丢弃到 rune 边界
// (与 truncateVPSOutput 相反——那边截掉的是尾巴)。
func tailVPSOutput(out []byte, limit int) string {
	if len(out) <= limit {
		return string(out)
	}
	s := out[len(out)-limit:]
	for len(s) > 0 {
		// DecodeRune 遇到「残缺前缀」返回 (RuneError, 1):丢掉这一字节重试;
		// 合法 rune(含真实的 U+FFFD 字符, size>1)即到达边界。
		if r, size := utf8.DecodeRune(s); r != utf8.RuneError || size > 1 {
			break
		}
		s = s[1:]
	}
	return string(s)
}

// vpsWorkspacePath 计算任务在远程机器上的工作目录(与 vpscenter.workspaceFor 同规则;
// 手写斜杠拼接,避免 Windows 主机上 filepath.Join 产出反斜杠路径)。
func vpsWorkspacePath(base string, taskID int64) string {
	base = strings.TrimRight(strings.ReplaceAll(base, "\\", "/"), "/")
	return fmt.Sprintf("%s/task_%d", base, taskID)
}

// vpsToolRoster 读机器的工具检测矩阵(库内最近一次检测结果,不现连探测全量版本):
// 返回(已装[含版本], 已知未装)。从未检测过时已装列表给自查提示、未装清单留空
// (「没测过」≠「没装」,不能误导模型)。
func (s *Server) vpsToolRoster(machineID int64) (installed, missing []string) {
	installed, missing = []string{}, []string{}
	if s == nil || s.m == nil || s.m.PG() == nil {
		return installed, missing
	}
	dets, err := s.m.PG().ListVpsToolDetections(machineID)
	if err != nil {
		return installed, missing
	}
	if len(dets) == 0 {
		return []string{"(尚未做工具检测:可用 Bash `command -v <工具>` 自查,或在 VPS 中心机器详情页触发检测)"}, missing
	}
	seen := map[string]bool{}
	for _, d := range dets {
		seen[d.ToolName] = true
		if !d.Installed {
			continue
		}
		if d.Version != "" {
			installed = append(installed, d.ToolName+"("+d.Version+")")
		} else {
			installed = append(installed, d.ToolName)
		}
	}
	for _, name := range vpscenter.KnownToolNames() {
		if !seen[name] {
			missing = append(missing, name)
		}
	}
	return installed, missing
}

// vpsSystemBlock 组装 worker 系统提示词的 VPS 段(契约 §6):机器档案 + 执行语义。
// 与 constraintBlock 同一层追加,system 每 run 重拼,不会被 compaction 压掉。
func (s *Server) vpsSystemBlock(machine *vpscenter.Machine, taskID int64) string {
	if machine == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n**执行环境：远程 VPS**\n")
	fmt.Fprintf(&b, "你的所有 Bash 命令将在该 Linux 机器上执行（不再跑在平台本机）：\n")
	fmt.Fprintf(&b, "- 机器：%s（%s:%d），当前状态 %s\n", machine.Name, machine.Host, machine.Port, machine.Status)
	if machine.OsName != "" || machine.Arch != "" || machine.Kernel != "" {
		fmt.Fprintf(&b, "- 系统：%s %s，内核 %s，架构 %s；CPU %d 核 / 内存 %d MB / 磁盘 %d GB\n",
			machine.OsName, machine.OsVersion, machine.Kernel, machine.Arch,
			machine.CPUCores, machine.MemoryMB, machine.DiskGB)
	}
	installed, missing := s.vpsToolRoster(machine.ID)
	fmt.Fprintf(&b, "- 已装工具：%s\n", strings.Join(installed, ", "))
	if len(missing) > 0 {
		fmt.Fprintf(&b, "- 已知但未安装：%s（可用 vps_install_toolset 装配套装，或 Bash 自行安装）\n", strings.Join(missing, ", "))
	}
	fmt.Fprintf(&b, "- 每次调用是独立 shell（环境变量/后台进程不跨调用保留），工作目录固定 %s；需要持久状态请写文件。\n", vpsWorkspacePath(machine.WorkspaceBase, taskID))
	b.WriteString("- 平台 Read/Write 工具操作的是平台本机工作区，与该机器的文件系统无关；远程文件请用 Bash cat/tee/重定向读写。\n")
	b.WriteString("- 可用 vps_machine_info 查看机器档案与工具清单；用 vps_install_toolset 安装工具集（耗时可能数分钟）。\n")
	return b.String()
}

// ---- 2. 两个领域工具(照 recon_tools.go 的写法,seed 绑 worker) ----

// vpsAgentTools 返回 VPS agent 工具。经 hostTools 注入每个 agent 的候选集,
// 再由 tools 表按绑定过滤(默认只绑 worker)。
func (s *Server) vpsAgentTools() []actool.CoreTool {
	return []actool.CoreTool{
		s.toolVPSMachineInfo(),
		s.toolVPSInstallToolset(),
	}
}

// toolVPSMachineInfo 返回当前任务指派机器的档案 JSON。读库内最近的工具检测矩阵,
// 不现连探测;任务未指派时明确告知命令跑在本机。
func (s *Server) toolVPSMachineInfo() actool.CoreTool {
	return roTool("vps_machine_info",
		"查看当前任务指派的执行机器(VPS)档案:名称/地址/系统/内核/架构/CPU/内存/磁盘/状态/"+
			"任务工作区路径/已装工具及版本(最近一次检测结果)/已知未装工具清单。"+
			"仅任务指派了 VPS 时可用;未指派时 Bash 在平台本机执行。",
		objSchema(map[string]any{}),
		func(ctx context.Context, _ json.RawMessage) (actool.Result, error) {
			mgr := VPSManager()
			if mgr == nil {
				return actool.Errorf("VPS 中心未初始化"), nil
			}
			taskID := agent.RunInfoFrom(ctx).TaskID
			if taskID <= 0 {
				return actool.Errorf("vps_machine_info 只能在任务执行的 agent 里调用(当前会话没有任务)"), nil
			}
			machine, err := mgr.MachineForTask(taskID)
			if err != nil {
				return actool.Errorf("读取任务机器失败: " + err.Error()), nil
			}
			if machine == nil {
				return actool.Errorf("本任务未指派 VPS 机器:Bash 命令在平台本机执行(创建任务时可在「执行环境」选择机器)"), nil
			}
			installed, missing := s.vpsToolRoster(machine.ID)
			tags := machine.Tags
			if tags == nil {
				tags = []string{}
			}
			return jsonResult(map[string]any{
				"machine_id": machine.ID, "name": machine.Name,
				"host": machine.Host, "port": machine.Port, "status": machine.Status,
				"os": machine.OsName, "os_version": machine.OsVersion,
				"kernel": machine.Kernel, "arch": machine.Arch,
				"cpu_cores": machine.CPUCores, "memory_mb": machine.MemoryMB, "disk_gb": machine.DiskGB,
				"tags":      tags,
				"workspace": vpsWorkspacePath(machine.WorkspaceBase, taskID),
				"installed_tools":    installed,
				"known_missing_tools": missing,
				"hint": "工具清单来自最近一次工具检测;套装安装用 vps_install_toolset,单个工具也可用 Bash 直接装",
			})
		})
}

// toolVPSInstallToolset 按名找工具集并 ApplyToolset(命令行安装脚本,source=toolset,
// 全程落执行流水),返回执行摘要 + 输出尾部。脚本可能要下载/编译,耗时可能数分钟。
func (s *Server) toolVPSInstallToolset() actool.CoreTool {
	return wrTool("vps_install_toolset",
		"在当前任务指派的 VPS 机器上安装一个工具集(按名称,内置有「基础侦察」「Web漏扫」「爆破破解」,"+
			"全部可用集合见 vps_machine_info 提示或 VPS 中心工具集页):执行集合的安装脚本并返回执行摘要。"+
			"耗时可能数分钟(apt-get/go install 需联网下载编译);脚本是幂等的,已装工具会跳过,失败可重跑。",
		objSchema(map[string]any{
			"name": strParam("工具集名称,须与工具集页的名称完全一致(如「基础侦察」)"),
		}, "name"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var p struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(in, &p)
			p.Name = strings.TrimSpace(p.Name)
			if p.Name == "" {
				return actool.Errorf("name 不能为空"), nil
			}
			mgr := VPSManager()
			if mgr == nil {
				return actool.Errorf("VPS 中心未初始化"), nil
			}
			taskID := agent.RunInfoFrom(ctx).TaskID
			if taskID <= 0 {
				return actool.Errorf("vps_install_toolset 只能在任务执行的 agent 里调用"), nil
			}
			machine, err := mgr.MachineForTask(taskID)
			if err != nil {
				return actool.Errorf("读取任务机器失败: " + err.Error()), nil
			}
			if machine == nil {
				return actool.Errorf("本任务未指派 VPS 机器:Bash 在平台本机执行,无需安装远程工具"), nil
			}
			if s.m == nil || s.m.PG() == nil {
				return actool.Errorf("数据库未初始化,无法读取工具集"), nil
			}
			ts, err := s.m.PG().GetVpsToolsetByName(p.Name)
			if err != nil {
				return actool.Errorf("查询工具集失败: " + err.Error()), nil
			}
			if ts == nil {
				names := []string{}
				if sets, lerr := s.m.PG().ListVpsToolsets(); lerr == nil {
					for _, t := range sets {
						names = append(names, t.Name)
					}
				}
				return actool.Errorf(fmt.Sprintf("工具集 «%s» 不存在;可用: %s", p.Name, strings.Join(names, ", "))), nil
			}
			execID, output, exitCode, aerr := mgr.ApplyToolset(ctx, machine.ID, ts.ID)
			res := map[string]any{
				"toolset": ts.Name, "machine": machine.Name, "execution_id": execID,
				"output_tail": tailVPSOutput(output, vpsToolsetOutputTail),
				"hint":        "可用 vps_machine_info 复查已装工具;脚本幂等,失败可重跑",
			}
			switch {
			case aerr != nil:
				res["status"] = "failed"
				res["detail"] = "传输层失败(SSH 连接/超时),安装脚本未完整执行: " + aerr.Error()
			case exitCode != 0:
				res["status"] = "failed"
				res["detail"] = fmt.Sprintf("安装脚本退出码 %d,请看 output_tail 定位失败步骤;修复后可重跑(幂等)", exitCode)
			default:
				res["status"] = "success"
				res["detail"] = "安装脚本执行完成(退出码 0)"
			}
			return jsonResult(res)
		})
}

// seedVPSAgentTools 把两个 VPS agent 工具 seed 进 tools 表(照 recon_tools 惯例,
// SeedTool 首插入 only,用户改过的描述/绑定不被覆盖),默认绑 worker。
func (s *Server) seedVPSAgentTools() {
	if s.m == nil || s.m.PG() == nil {
		return
	}
	for _, t := range s.vpsAgentTools() {
		schema, _ := json.Marshal(t.InputSchema())
		agents, _ := json.Marshal([]string{"worker"})
		if err := s.m.PG().SeedToolWithStage(t.Name(), t.Description(), schema, agents, "exploit"); err != nil {
			log.Printf("[vps-agent] seed %s 失败: %v", t.Name(), err)
		}
	}
}

// ---- 3. 任务级 REST(响应形状与 api.ts 冻结版对齐) ----

// registerTaskVPSRoutes 注册任务 ↔ VPS 指派路由(挂 requireAuth 下的 api 中间件链)。
func (s *Server) registerTaskVPSRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/tasks/{id}/vps", s.assignTaskVPS)
	mux.HandleFunc("DELETE /api/tasks/{id}/vps", s.releaseTaskVPS)
	mux.HandleFunc("GET /api/tasks/{id}/vps-environment", s.taskVPSEnvironment)
}

// taskVPSPathTask 解析路径里的任务并返回数字 id。
func taskVPSPathTask(s *Server, w http.ResponseWriter, r *http.Request) (*int64, bool) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return nil, false
	}
	id, err := strconv.ParseInt(t.ID, 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "无效的任务 id")
		return nil, false
	}
	return &id, true
}

// assignTaskVPS POST /api/tasks/{id}/vps — 指派执行机器。
// body {machine_id:int, auto:bool}:machine_id>0 指定机器;machine_id=0 且 auto=true
// 自动分配。机器已被其它任务占用回 409。
func (s *Server) assignTaskVPS(w http.ResponseWriter, r *http.Request) {
	if !s.vpsReady(w) {
		return
	}
	mgr := s.vpsCenter(w)
	if mgr == nil {
		return
	}
	taskID, ok := taskVPSPathTask(s, w, r)
	if !ok {
		return
	}
	var req struct {
		MachineID int64 `json:"machine_id"`
		Auto      bool  `json:"auto"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	var (
		machine *vpscenter.Machine
		err     error
	)
	switch {
	case req.MachineID > 0:
		machine, err = mgr.Assign(*taskID, req.MachineID, "user")
	case req.Auto:
		machine, err = mgr.AssignAuto(*taskID, "user")
	default:
		writeErr(w, 400, "machine_id=0 时必须 auto=true")
		return
	}
	if err != nil {
		// Manager.Assign 的占用冲突文案固定含「已被任务」→ 409;其余(不存在/不在线)400。
		if strings.Contains(err.Error(), "已被任务") {
			writeErr(w, 409, err.Error())
			return
		}
		writeErr(w, 400, err.Error())
		return
	}
	// 响应是完整 VpsMachine 形状(api.ts 冻结):从 db 回读,凭据字段本就不在行 DTO 里。
	row, gerr := s.m.PG().GetVpsMachine(machine.ID)
	if gerr != nil || row == nil {
		writeErr(w, 500, "指派成功但回读机器失败: "+fmt.Sprint(gerr))
		return
	}
	writeJSON(w, 200, map[string]any{"machine": vpsMachineDTO(row)})
}

// releaseTaskVPS DELETE /api/tasks/{id}/vps — 释放执行机器(幂等,无指派也成功)。
func (s *Server) releaseTaskVPS(w http.ResponseWriter, r *http.Request) {
	if !s.vpsReady(w) {
		return
	}
	mgr := s.vpsCenter(w)
	if mgr == nil {
		return
	}
	taskID, ok := taskVPSPathTask(s, w, r)
	if !ok {
		return
	}
	if err := mgr.Release(*taskID, "user_release"); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"released": true})
}

// taskVPSEnvironment GET /api/tasks/{id}/vps-environment — 任务详情「执行环境」tab:
// {machine, executions[]}。未指派时 machine=null、executions 为空数组(Go nil 切片
// 会序列化成 null 打崩前端,一律初始化)。
func (s *Server) taskVPSEnvironment(w http.ResponseWriter, r *http.Request) {
	if !s.vpsReady(w) {
		return
	}
	taskID, ok := taskVPSPathTask(s, w, r)
	if !ok {
		return
	}
	var machine *pgdb.VpsMachine
	if mgr := VPSManager(); mgr != nil {
		if m, err := mgr.MachineForTask(*taskID); err == nil && m != nil {
			if row, gerr := s.m.PG().GetVpsMachine(m.ID); gerr == nil {
				machine = row
			}
		}
	}
	executions := []*pgdb.VpsExecution{}
	if machine != nil {
		if items, _, err := s.m.PG().ListVpsExecutions(pgdb.VpsExecutionFilter{
			MachineID: machine.ID, TaskID: *taskID, Limit: 200,
		}); err == nil {
			executions = items
		}
	}
	writeJSON(w, 200, map[string]any{"machine": vpsMachineDTO(machine), "executions": nonNilPtrSlice(executions)})
}

// ---- 4. 创建即指派 ----

// assignVPSOnCreate 创建即指派(契约 §6):任务行已建、尚未进入 running 时把机器
// 占上,消除「任务先启动、指派后补」的竞态。任何失败都不阻断建任务:任务照常按
// 本机模式跑,只记 warning。
func assignVPSOnCreate(t *Task, machineID int64, auto bool) {
	if t == nil {
		return
	}
	mgr := VPSManager()
	if mgr == nil {
		log.Printf("[vps-agent] 任务 #%s 请求了 VPS 执行环境,但 VPS 中心未初始化,任务按本机模式执行", t.ID)
		return
	}
	taskID, err := strconv.ParseInt(t.ID, 10, 64)
	if err != nil || taskID <= 0 {
		return
	}
	var (
		assigned *vpscenter.Machine
		aerr     error
	)
	switch {
	case machineID > 0:
		assigned, aerr = mgr.Assign(taskID, machineID, "create")
	case auto:
		assigned, aerr = mgr.AssignAuto(taskID, "create")
	}
	if aerr != nil {
		log.Printf("[vps-agent] 任务 #%s 创建时指派机器失败(任务照常本机执行,可在任务详情「执行环境」重试): %v", t.ID, aerr)
		return
	}
	if assigned != nil {
		log.Printf("[vps-agent] 任务 #%s 创建即指派机器 «%s»(#%d),工作区 %s",
			t.ID, assigned.Name, assigned.ID, vpsWorkspacePath(assigned.WorkspaceBase, taskID))
	}
}

// ---- 5. 终态自动释放 ----

// releaseTaskVPSOnTerminal 任务终态自动释放(契约 §6):挂在 SetTaskTerminalHook
// 既有回调链(与报告归档组合调用,不覆盖)。平台的终态集合是 done/failed/timeout
// (IsTerminal 判定;用户取消的任务也以这两个状态落终态),都走这条链;paused 不
// 触发(会话可恢复)。Release 幂等,无指派 no-op。
func releaseTaskVPSOnTerminal(taskID int64) {
	mgr := VPSManager()
	if mgr == nil || taskID <= 0 {
		return
	}
	if err := mgr.Release(taskID, "task_end"); err != nil {
		log.Printf("[vps-agent] 任务 #%d 终态释放机器失败: %v", taskID, err)
	}
}
