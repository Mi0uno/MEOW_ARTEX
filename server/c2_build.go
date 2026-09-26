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
	"strings"
	"time"

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

// execC2Build 执行一次完整构建。返回构建日志(供 job.result 存尾部)与错误:
//   - errC2BuildStep:命令非零退出/机器缺工具链等确定性失败;
//   - 其它:SSH 传输层失败(可能瞬态)。
func (s *Server) execC2Build(ctx context.Context, job *pgdb.C2Job) ([]byte, error) {
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
