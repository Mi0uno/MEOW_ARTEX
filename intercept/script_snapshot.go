package intercept

// 审批审计的脚本文件快照:转人工/留痕的命令引用了解释器脚本时(python x.py /
// bash x.sh / ./x.py …),在审批时刻把磁盘内容抓一份存进 audit.script_files。
// 转人工的典型理由是「无法确认脚本内容是否越权」——审核人必须能看到脚本本身,
// 否则审批台只剩一行命令,无从裁决。快照是只读旁路:解析/读取失败一律静默降级
// (记 Missing),绝不改变审批流程的走向。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Autumn-27/artex/db"
)

const (
	scriptSnapshotLimit    = 16 * 1024 // 单文件快照上限(超长脚本截断并标记)
	scriptSnapshotMaxFiles = 3         // 单次命令最多快照的文件数
	// 防超长命令正则爆炸:最多扫描的解释器出现次数。
	scriptSnapshotMaxMatches = 8
)

// scriptFileExts 视为脚本文件的扩展名(小写)。只有命中扩展名(或 ./ ../ 相对
// 执行形态)的解释器参数才会被读进审计,避免把 cat/ls 的任意参数当快照对象。
var scriptFileExts = map[string]bool{
	".py": true, ".sh": true, ".bash": true, ".js": true, ".mjs": true, ".cjs": true,
	".php": true, ".rb": true, ".pl": true, ".ps1": true, ".psm1": true, ".lua": true,
}

// interpreterArgRe 匹配「解释器 + 可选 flag + 脚本路径」。路径支持单双引号
// (引号内允许空格)与裸词;组:2=双引号体 3=单引号体 4=裸词。
var interpreterArgRe = regexp.MustCompile(
	`(?i)\b(?:python(?:3(?:\.\d+)?)?|py|node|deno|bun|php|ruby|perl|bash|sh|zsh|ksh|dash|pwsh|powershell)\s+((?:-{1,2}[\w-]+\s+)*)(?:"([^"\n]+)"|'([^'\n]+)'|([^\s;&|<>]+))`)

// cdDirRe 收集命令里的 cd 目标(带序号),供相对路径解析兜底:
// 「cd "X"; python y.py」里 y.py 的基准是 X,不一定是审查工作目录。
var cdDirRe = regexp.MustCompile(`(?:^|[;&|]\s*)cd\s+(?:"([^"\n]+)"|'([^'\n]+)'|([^\s;&|<>]+))`)

// captureScriptFiles 解析 Bash 类工具输入里的脚本引用并快照。返回 nil 表示
// 没有可快照的脚本(非命令输入 / 未引用脚本)。
func captureScriptFiles(ctx context.Context, input []byte) []db.InterceptScriptFile {
	workDir := ""
	if dir, _, ok := ReviewEnvironmentFromContext(ctx); ok {
		workDir = dir
	}
	return CaptureScriptFilesForInput(input, workDir)
}

// CaptureScriptFilesForInput 是快照捕获的显式入口,供详情接口给「审批时刻没抓到
// 快照的 pending 记录」按需回填使用:回填时没有审查 ctx,工作目录由调用方从任务
// 运行目录推导(可为空;命令里的 cd 目录始终优先)。
func CaptureScriptFilesForInput(input []byte, workDir string) []db.InterceptScriptFile {
	var payload struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return nil
	}
	command := strings.TrimSpace(payload.Command)
	if command == "" {
		return nil
	}

	type cdEntry struct {
		at  int
		dir string
	}
	var cdDirs []cdEntry
	for _, loc := range cdDirRe.FindAllStringSubmatchIndex(command, scriptSnapshotMaxMatches) {
		for _, gi := range []int{1, 2, 3} {
			if loc[gi*2] >= 0 {
				cdDirs = append(cdDirs, cdEntry{loc[0], strings.TrimSpace(command[loc[gi*2]:loc[gi*2+1]])})
				break
			}
		}
	}
	latestCDBefore := func(offset int) string {
		dir := ""
		for _, c := range cdDirs {
			if c.at < offset {
				dir = c.dir
			}
		}
		return dir
	}

	var files []db.InterceptScriptFile
	seen := map[string]bool{}
	for _, loc := range interpreterArgRe.FindAllStringSubmatchIndex(command, scriptSnapshotMaxMatches) {
		raw := ""
		for _, gi := range []int{2, 3, 4} {
			if loc[gi*2] >= 0 {
				raw = command[loc[gi*2]:loc[gi*2+1]]
				break
			}
		}
		p := strings.TrimSpace(raw)
		// 纯 flag 组(如 python -3)或 -c/-m 内联代码不是文件引用。
		if p == "" || strings.HasPrefix(p, "-") {
			continue
		}
		if !scriptFileExts[strings.ToLower(filepath.Ext(p))] &&
			!strings.HasPrefix(p, "./") && !strings.HasPrefix(p, "../") {
			continue
		}
		path := p
		if !filepath.IsAbs(p) {
			bases := []string{}
			nearestCD := latestCDBefore(loc[0])
			if nearestCD != "" {
				bases = append(bases, nearestCD)
			}
			if workDir != "" && workDir != nearestCD {
				bases = append(bases, workDir)
			}
			resolved := ""
			for _, b := range bases {
				cand := filepath.Join(filepath.FromSlash(strings.Trim(b, `"`)), filepath.FromSlash(p))
				if fileReadable(cand) {
					resolved = cand
					break
				}
			}
			if resolved == "" {
				// 两种模糊命中不落审计:没有任何基准目录可比对的裸路径,或带空格的
				// 引号候选(bash -c "python x.py" 的内联代码、pwsh -Command 的脚本块)。
				// 记录一条解析不到的假路径只会有噪音;「明确引用了但文件缺失」(无空格
				// 路径)仍然照常记 Missing,给审核人「脚本没快照到」的信号。
				if len(bases) == 0 || strings.ContainsAny(p, " \t") {
					continue
				}
				resolved = filepath.Join(filepath.FromSlash(strings.Trim(bases[0], `"`)), filepath.FromSlash(p))
			}
			path = resolved
		}
		if seen[path] || len(files) >= scriptSnapshotMaxFiles {
			continue
		}
		seen[path] = true
		files = append(files, readScriptSnapshot(path))
	}
	return files
}

// readScriptSnapshot 读取单个脚本文件;失败记 Missing,错误细节不外泄
// (路径本身已展示,错误串只会带来噪音)。
func readScriptSnapshot(path string) db.InterceptScriptFile {
	out := db.InterceptScriptFile{Path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		out.Missing = true
		return out
	}
	content, cut := bounded(string(data), scriptSnapshotLimit)
	out.Content, out.Truncated = content, cut
	return out
}

func fileReadable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
