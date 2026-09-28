package intercept

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const reviewTextLimit = 4000

const BackgroundUserMessage = "user_message"

// ReviewBackground is explicitly bound from the current human message. Generated
// Worker summaries are not accepted. Background cannot override review policy.
type ReviewBackground struct {
	Source    string `json:"source"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

// ReviewInput contains only the current call and explicitly selected background.
// Execution history and call correlation belong to the separate audit record.
type ReviewInput struct {
	Version    int               `json:"version"`
	WorkingDir string            `json:"working_directory,omitempty"`
	Background *ReviewBackground `json:"background,omitempty"`
	Tool       string            `json:"tool_name"`
	Arguments  json.RawMessage   `json:"arguments"`
	// ScriptFiles(v5)与审计侧 script_files 同一解析器:审查输入与人工审批看到的
	// 脚本信息保持一致,裁判不再对「命令引用了脚本但看不到内容」的场景盲判。
	ScriptFiles []ReviewScriptFile `json:"script_files,omitempty"`
}

// ReviewScriptFile 是审查输入附带的脚本文件快照(v5):命令引用的解释器脚本在
// 审查时刻抓取的磁盘内容,让裁判能核实脚本真实行为,而不是对着一行引用命令猜。
type ReviewScriptFile struct {
	Path      string `json:"path"`
	Content   string `json:"content,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Missing   bool   `json:"missing,omitempty"` // 引用了但没能读到内容
}

type reviewContextKey struct{}
type reviewEnvironment struct {
	workingDir string
	background ReviewBackground
}

// WithReviewContext explicitly binds the permitted background for one run. Never
// fall back to the raw turn transcript: it may contain the full scheduler prompt.
// This is application wiring, not a model-callable tool.
func WithReviewContext(ctx context.Context, workingDir string, background ReviewBackground) context.Context {
	return context.WithValue(ctx, reviewContextKey{}, reviewEnvironment{workingDir, background})
}

// WithReviewWorkingDirectory preserves only explicitly selected background.
// Chat runs can be human-initiated or scheduled, so the Agent must not infer
// message provenance from the text it receives.
func WithReviewWorkingDirectory(ctx context.Context, workingDir string) context.Context {
	env, _ := ctx.Value(reviewContextKey{}).(reviewEnvironment)
	env.workingDir = workingDir
	return context.WithValue(ctx, reviewContextKey{}, env)
}

func BuildReviewInput(ctx context.Context, tool string, arguments json.RawMessage) (ReviewInput, error) {
	if !json.Valid(arguments) {
		return ReviewInput{}, fmt.Errorf("工具参数不是有效 JSON")
	}
	in := ReviewInput{Version: 5, Tool: tool, Arguments: append(json.RawMessage(nil), arguments...)}
	if env, ok := ctx.Value(reviewContextKey{}).(reviewEnvironment); ok {
		in.WorkingDir = env.workingDir
		background := env.background
		if background.Source == BackgroundUserMessage && strings.TrimSpace(background.Text) != "" {
			var cut bool
			background.Text, cut = bounded(background.Text, reviewTextLimit)
			background.Truncated = background.Truncated || cut
			in.Background = &background
		}
	}
	// 脚本快照(v5):命令引用的解释器脚本随审查输入一并交给裁判,与人工审批
	// 看到的 audit.script_files 同源。未引用脚本时字段省略;引用了但读不到的
	// 条目带 missing=true,让裁判明确知道「材料在但内容没取到」。
	for _, f := range captureScriptFiles(ctx, arguments) {
		in.ScriptFiles = append(in.ScriptFiles, ReviewScriptFile{
			Path: f.Path, Content: f.Content, Truncated: f.Truncated, Missing: f.Missing,
		})
	}
	return in, nil
}

// ReviewEnvironmentFromContext 取回本次运行显式绑定的工作目录与背景,供决策引擎
// (JEV) 复用。LLM 裁判走 BuildReviewInput 拿到的是同一份环境,两者信息量必须一致:
// 决策引擎在链路里更靠前,若它看到的信息比后一层少,就会对"看起来可疑"的调用先行
// 拦截,后面的裁判根本没机会纠正。
func ReviewEnvironmentFromContext(ctx context.Context) (workingDir string, background ReviewBackground, ok bool) {
	env, found := ctx.Value(reviewContextKey{}).(reviewEnvironment)
	if !found {
		return "", ReviewBackground{}, false
	}
	return env.workingDir, env.background, true
}

// TaskContextFromContext 取回任务级上下文(taskID + agent 名),供决策引擎判断
// "这次调用属于哪个任务的哪一步"。决策引擎据此知道调用发生在渗透任务的执行面,
// 而不是无归属的裸调用——这是"授权渗透里的提权尝试"与"越权操作"的关键区别。
func TaskContextFromContext(ctx context.Context) (taskID, agentName string, ok bool) {
	taskID, agentName = taskInfoFromCtx(ctx)
	return taskID, agentName, taskID != ""
}
