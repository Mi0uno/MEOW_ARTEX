package server

// 免杀工作台(契约 C2_CONTRACTS.md §13)+ 二期路由接线(§11):
//
//	档案     /api/c2/evasion/profiles…             免杀档案 CRUD(目标 AV/技术配置)
//	变形     POST /api/c2/evasion/generate          202 建 c2_jobs(kind=evasion) + stage=post 监看任务
//	作业     /api/c2/jobs…                          列表/详情/重试(防抢占 CAS 领取)
//	工具     c2_evasion_generate                    agent 自主发起变形并等结果(stage=post)
//
// 管线(仿 report_pipeline.go / report_task.go 的任务化模式):
//	RenderPayload 出基础载荷 → LLM 变形(瞬态重试 analyzeWithRetry 同款)→ 本地静态
//	自测 → 命中≥3 且轮次<2 喂回再变形 → 落盘 <BaseDir>/c2/payloads/ → CreatePayload
//	(回填 evasion_profile_id)→ job done(result 存静态报告+rounds+plan)。
//	LLM 调用全程不动 c2center 包——变形只发生在 server 侧对已渲染文本的变换。
//
// 【红线】静态自测是纯本地函数,绝不把载荷内容发往 LLM 之外的任何端点:本文件
// 的全部网络出站只有 analyzeWithRetry → providerForProfile 的 LLM 配置一条路
// (载荷内容进提示词是变形的本职);除此之外零外发、零 os/exec。
//
// 监看:每条变形作业挂一条 stage='post' 监看任务 + c2_evasion_engineer 监看会话
// (任务中心/对话 ?stage=post 自动收录;会话归组依赖 agents.stage,见 initC2Evasion
// 的 agent 播种)。接线上 registerC2EvasionRoutes/initC2Evasion 两个签名逐字冻结,
// 由 Agent A 在 c2_api.go/c2_center.go 各加一行调用。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	c2center "github.com/Autumn-27/artex/c2center"
	pgdb "github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// ---- 冻结接线(签名逐字冻结,Agent A 调用) ----

// registerC2EvasionRoutes 注册免杀工作台与作业路由(含 §15 的 loot→finding,
// 路径同属 /api/c2/*;鉴权由 Handler 的 requireAuth 统一挂载)。
func (s *Server) registerC2EvasionRoutes(mux *http.ServeMux) {
	// 免杀档案
	mux.HandleFunc("GET /api/c2/evasion/profiles", s.listC2EvasionProfiles)
	mux.HandleFunc("POST /api/c2/evasion/profiles", s.createC2EvasionProfile)
	mux.HandleFunc("PUT /api/c2/evasion/profiles/{id}", s.updateC2EvasionProfile)
	mux.HandleFunc("DELETE /api/c2/evasion/profiles/{id}", s.deleteC2EvasionProfile)
	// 变形发起
	mux.HandleFunc("POST /api/c2/evasion/generate", s.generateC2Evasion)
	// 作业
	mux.HandleFunc("GET /api/c2/jobs", s.listC2Jobs)
	mux.HandleFunc("GET /api/c2/jobs/{id}", s.getC2Job)
	mux.HandleFunc("POST /api/c2/jobs/{id}/retry", s.retryC2Job)
	// loot→finding(实现在 c2_loot_finding.go)
	mux.HandleFunc("POST /api/c2/loot/{id}/to-finding", s.c2LootToFinding)
}

// initC2Evasion 免杀工作台初始化(Agent A 在 initC2 尾部追加调用):
//  1. 播种监看会话用的 post 阶段 agent 行(conversations 按 agents.stage 归组,
//     没有这一行监看对话不会出现在 ?stage=post 下;幂等,首插 only);
//  2. 把崩溃遗留的 running 作业拨回 queued(变形是整体重跑语义,幂等);
//  3. 恢复未终态作业(TakeC2Job 的 queued→running CAS 防双跑);
//  4. 播种 c2_evasion_generate agent 工具。
func (s *Server) initC2Evasion() {
	if s.m == nil || s.m.PG() == nil {
		return
	}
	pg := s.m.PG()
	if a, err := pg.GetAgentByKey(c2EvasionWatchAgent); err != nil {
		log.Printf("[c2-evasion] 查监看 agent 失败: %v", err)
	} else if a == nil {
		if _, err := pg.CreateAgent(c2EvasionWatchAgent, "免杀工程师",
			"免杀变形引擎的监看会话:载荷变形作业的计划、静态自测结果与产出在这里播报,可在会话里追问改稿。",
			"post"); err != nil {
			log.Printf("[c2-evasion] 播种监看 agent 失败: %v", err)
		}
	}
	if n, err := pg.ResetStuckC2Jobs(pgdb.C2JobKindEvasion); err != nil {
		log.Printf("[c2-evasion] 遗留 running 作业拨回失败(下次启动重试): %v", err)
	} else if n > 0 {
		log.Printf("[c2-evasion] 已把 %d 条遗留 running 作业拨回 queued", n)
	}
	if jobs, err := pg.ListOpenC2Jobs(pgdb.C2JobKindEvasion); err != nil {
		log.Printf("[c2-evasion] 恢复未终态作业失败: %v", err)
	} else {
		for _, j := range jobs {
			s.launchC2EvasionJob(j.ID)
		}
		if len(jobs) > 0 {
			log.Printf("[c2-evasion] 已恢复 %d 条未终态变形作业", len(jobs))
		}
	}
	s.seedC2EvasionTool()
	log.Printf("[c2-evasion] 免杀工作台已启动(档案/变形作业/静态自测)")
}

// ---- 常量与并发闸 ----

const (
	// c2EvasionMaxRounds 变形轮次上限:首轮 + 至多一轮「命中清单喂回」再变形(契约 §13)。
	c2EvasionMaxRounds = 2
	// c2EvasionLLMBudget 单次 LLM 变形的时间预算(独立于任何 HTTP 超时,仿模板识别)。
	c2EvasionLLMBudget = 240 * time.Second
	// c2EvasionToolWait agent 工具等作业终态的窗口(超时返回 job_id 让模型稍后再查)。
	c2EvasionToolWait = 8 * time.Minute
	// c2EvasionPollInterval 作业轮询间隔(LLM 级任务,秒级足够)。
	c2EvasionPollInterval = 3 * time.Second
)

// c2EvasionWatchAgent 免杀监看会话挂在它名下的 agent key(stage='post')。
const c2EvasionWatchAgent = "c2_evasion_engineer"

// errC2EvasionBusy 同档案+监听器已有 running 作业(防重入 409)。
var errC2EvasionBusy = errors.New("该档案+监听器已有变形作业在运行中,请等它完成后再发起")

// c2EvasionSem 变形作业的全局并发闸(单并发,契约 §13;等待不占 agent 并发槽)。
var c2EvasionSem = make(chan struct{}, 1)

// c2EvasionJobRunner 作业级取消注册表(仿 templateRunner;按 c2_jobs.id)。
type c2EvasionJobRunner struct {
	mu      sync.Mutex
	cancels map[int64]context.CancelFunc
}

var c2EvRunner = &c2EvasionJobRunner{cancels: map[int64]context.CancelFunc{}}

func (r *c2EvasionJobRunner) register(id int64, cancel context.CancelFunc) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.cancels[id]; ok {
		return false
	}
	r.cancels[id] = cancel
	return true
}

func (r *c2EvasionJobRunner) unregister(id int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cancels, id)
}

// ---- 本地静态自测(契约 §13 第 4 条冻结清单;纯本地,永不外发) ----

// c2StaticHit 单个特征串的命中。
type c2StaticHit struct {
	Marker string `json:"marker"`
	Count  int    `json:"count"`
}

// c2StaticReport 静态自测报告:risk 按命中总数分级 low(<3)|medium(3-5)|high(>5);
// entropy 为整份代码的字节 Shanon 熵(0-8),>5.5 记 entropy_high。
type c2StaticReport struct {
	Hits        []c2StaticHit `json:"hits"`
	Entropy     float64       `json:"entropy"`
	EntropyHigh bool          `json:"entropy_high"`
	Risk        string        `json:"risk"`
}

// c2StaticMarkers 各格式的 AV 静态特征串(大小写不敏感计数;gosrc 不在清单内——
// 编译型载荷走构建路径,免杀管线不接受 gosrc)。
var c2StaticMarkers = map[string][]string{
	pgdb.C2PayloadFormatPs1: {
		"enc", "DownloadString", "DownloadFile", "IEX", "Invoke-Expression",
		"FromBase64String", "Set-MpPreference", "Add-Type", "Win32_Process",
		"Net.WebClient", "schtasks", "reg add",
	},
	pgdb.C2PayloadFormatPy: {
		"exec(", "eval(", "urllib", "requests.", "socket.", "subprocess",
		"base64.b64decode", "__import__", "pty",
	},
	pgdb.C2PayloadFormatBash: {
		"curl", "wget", "/dev/tcp", "nc -e", "base64 -d", "python3 -c", "chmod +x",
	},
}

// c2EvasionStaticScan 对载荷代码做本地静态自测:特征串扫描 + Shanon 熵。
// 红线:只读内存字符串,不发任何网络请求(载荷内容绝不外发)。
func c2EvasionStaticScan(format, code string) c2StaticReport {
	rep := c2StaticReport{Hits: []c2StaticHit{}, Risk: "low"}
	markers, ok := c2StaticMarkers[format]
	if !ok {
		// gosrc/未知格式:不在免杀静态自测清单内(契约:gosrc 走构建路径)。
		return rep
	}
	lower := strings.ToLower(code)
	total := 0
	for _, m := range markers {
		n := strings.Count(lower, strings.ToLower(m))
		if n <= 0 {
			continue
		}
		rep.Hits = append(rep.Hits, c2StaticHit{Marker: m, Count: n})
		total += n
	}
	rep.Entropy = shannonEntropy(code)
	rep.EntropyHigh = rep.Entropy > 5.5
	switch {
	case total > 5:
		rep.Risk = "high"
	case total >= 3:
		rep.Risk = "medium"
	}
	return rep
}

// c2StaticTotalHits 报告里的命中总数(分级依据)。
func c2StaticTotalHits(rep c2StaticReport) int {
	n := 0
	for _, h := range rep.Hits {
		n += h.Count
	}
	return n
}

// shannonEntropy 字节级 Shanon 熵(bits/byte,0-8)。高熵段(base64/加密块)是
// 常见静态告警特征,>5.5 标高。
func shannonEntropy(s string) float64 {
	n := len(s)
	if n == 0 {
		return 0
	}
	var freq [256]int
	for i := 0; i < n; i++ {
		freq[s[i]]++
	}
	ent := 0.0
	total := float64(n)
	for _, c := range freq {
		if c == 0 {
			continue
		}
		p := float64(c) / total
		ent -= p * math.Log2(p)
	}
	return ent
}

// ---- LLM 提示词与输出解析(契约 §13 第 3 条冻结语义) ----

// c2EvasionSystem 变形引擎的固定系统提示词(走 analyzeCompletion 直连 LLM,
// 输出确定性优先,temperature 0.2 + thinking disabled,同模板识别口径)。
const c2EvasionSystem = "你是渗透测试平台的载荷变形引擎(免杀专家)。用户会给你一份已可工作的 beacon 载荷原始代码," +
	"以及载荷格式、目标 AV 环境、技术配置、附加指示,可能还有上一轮本地静态自测的命中清单。\n" +
	"你的任务:对原始载荷做变形处理以规避目标 AV 的静态特征检测,并严格按以下格式输出" +
	"(标签名固定、各自独占一行、两段之外不要输出任何内容):\n" +
	"<PLAN>\n…变形说明:逐条简述改了什么、为什么不影响行为…\n</PLAN>\n" +
	"<CODE>\n…变形后的完整载荷代码…\n</CODE>\n\n" +
	"铁律(必须全部遵守):\n" +
	"1. 信道行为逐字节等价:载荷与 C2 信道的交互必须与原载荷完全一致——CB/UP/KEY/URL_PATH " +
	"等信道占位符原样保留(取值一字不改),加密与解密逻辑不动,轮询/休眠/心跳节奏不动,请求打包格式不动;\n" +
	"2. 只允许这些变形:改变量名/函数名、改常量的编码方式(等价转义/拆分/编码,运行时还原后逐字节不变)、" +
	"调整代码结构(函数拆分/顺序调换)、插入不影响行为的无效佐料、字符串拆分重组;\n" +
	"3. 禁止引入任何外部依赖(不得新增 import/require/using 而改变依赖面);\n" +
	"4. 禁止改变执行入口与用法:变形后的载荷必须能用与原载荷完全相同的方式执行并上线;\n" +
	"5. <CODE> 必须是完整可直接落盘执行的整份代码,不要省略、不要留占位、不要附解释。"

// c2EvasionAVDesc 目标 AV 的人类可读描述(进提示词)。
func c2EvasionAVDesc(av string) string {
	switch av {
	case pgdb.C2EvasionTargetDefender:
		return "Windows Defender(微软自带,重 AMSI/特征签名/行为链)"
	case pgdb.C2EvasionTarget360:
		return "360 安全卫士(国产,重云查杀/特征串/敏感 API 组合)"
	case pgdb.C2EvasionTargetHuorong:
		return "火绒安全(国产,重静态特征串与脚本行为)"
	case pgdb.C2EvasionTargetCustom:
		return "用户自定义环境(见附加指示)"
	default:
		return "通用环境(常见 AV 静态特征)"
	}
}

// c2EvasionUserPrompt 组装变形请求:原始载荷+格式+目标 AV+技术配置+附加指示
// (+上轮命中清单)。载荷内容只进 LLM 提示词(变形本职),绝不发往其它端点。
func c2EvasionUserPrompt(base, format, targetAV, techniquesJSON, instructions string, prev *c2StaticReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "载荷格式: %s\n目标 AV 环境: %s\n", format, c2EvasionAVDesc(targetAV))
	if t := strings.TrimSpace(techniquesJSON); t != "" && t != "{}" {
		fmt.Fprintf(&b, "技术配置(JSON): %s\n", t)
	}
	if ins := strings.TrimSpace(instructions); ins != "" {
		fmt.Fprintf(&b, "附加指示: %s\n", ins)
	}
	if prev != nil {
		b.WriteString("上一轮变形后本地静态自测仍命中以下特征(本轮重点规避,但不得违反铁律):\n")
		for _, h := range prev.Hits {
			fmt.Fprintf(&b, "  - %s ×%d\n", h.Marker, h.Count)
		}
		if prev.EntropyHigh {
			fmt.Fprintf(&b, "  - 熵偏高(%.2f > 5.5):高熵块过多,注意降低无意义的大段编码\n", prev.Entropy)
		}
	}
	fmt.Fprintf(&b, "\n原始载荷:\n%s", base)
	return b.String()
}

// splitC2EvasionOutput 解析变形输出的双段:
//
//	<PLAN>变形说明</PLAN> … <CODE>变形后完整载荷</CODE>
//
// 剥围栏逻辑照抄报告管线(stripAnalyzeFences,report_task.go)。CODE 是交付物:
// 缺失视为解析失败(调用方按契约重试一次,仍失败置 job error);PLAN 单缺不判死
// (说明性文字,可缺)。
func splitC2EvasionOutput(out string) (plan, code string) {
	s := strings.TrimSpace(out)
	ci := strings.Index(s, "<CODE>")
	if ci < 0 {
		return "", "" // 连 CODE 段都没有 = 两段缺失,交由调用方重试
	}
	if pi := strings.Index(s, "<PLAN>"); pi >= 0 {
		body := s[pi+len("<PLAN>"):]
		if e := strings.Index(body, "</PLAN>"); e >= 0 {
			body = body[:e]
		}
		plan = stripAnalyzeFences(body)
	}
	body := s[ci+len("<CODE>"):]
	if e := strings.Index(body, "</CODE>"); e >= 0 {
		body = body[:e]
	}
	code = stripAnalyzeFences(body)
	return plan, code
}

// ---- 发起与 runner ----

// c2EvasionJobInput 作业入参(存 c2_jobs.input;重试按它重建)。
type c2EvasionJobInput struct {
	ListenerID   int64  `json:"listener_id"`
	Format       string `json:"format"`
	Target       string `json:"target"`
	Name         string `json:"name"`
	ProfileID    int64  `json:"profile_id"`
	Instructions string `json:"instructions"`
}

// startC2EvasionJob 发起一条变形作业:校验 → 防重入 → 建监看会话 + stage=post
// 监看任务 + queued 作业 → 异步启动。返回 (job_id, 平台任务数字 id)。
// errC2EvasionBusy 由调用方映射 409;其余错误 400。
func (s *Server) startC2EvasionJob(in c2EvasionJobInput) (int64, int64, error) {
	pg := s.m.PG()
	l, err := pg.GetC2Listener(in.ListenerID)
	if err != nil {
		return 0, 0, err
	}
	if l == nil {
		return 0, 0, fmt.Errorf("监听器 #%d 不存在", in.ListenerID)
	}
	// 档案:存在则给默认格式/附加指示;格式与档案不一致直接拒绝。
	profileID := in.ProfileID
	var profile *pgdb.C2EvasionProfile
	if profileID > 0 {
		if profile, err = pg.GetC2EvasionProfile(profileID); err != nil {
			return 0, 0, err
		}
		if profile == nil {
			return 0, 0, fmt.Errorf("免杀档案 #%d 不存在", profileID)
		}
		if strings.TrimSpace(in.Format) == "" {
			in.Format = profile.PayloadFormat
		}
	}
	if strings.TrimSpace(in.Format) == "" {
		in.Format = pgdb.C2PayloadFormatPs1
	}
	if in.Format == pgdb.C2PayloadFormatGoSrc {
		return 0, 0, fmt.Errorf("gosrc 载荷走构建路径(载荷页生成/VPS 编译),免杀变形仅支持 ps1/bash/py")
	}
	if !pgdb.ValidC2PayloadFormat(in.Format) {
		return 0, 0, fmt.Errorf("无效载荷格式: %q(仅 ps1/bash/py)", in.Format)
	}
	if profile != nil && in.Format != profile.PayloadFormat {
		return 0, 0, fmt.Errorf("载荷格式 %s 与档案 «%s»(%s)不一致,请改格式或换档案",
			in.Format, profile.Name, profile.PayloadFormat)
	}
	if profile != nil && strings.TrimSpace(in.Instructions) == "" {
		in.Instructions = profile.Instructions
	}
	if strings.TrimSpace(in.Target) == "" {
		in.Target = "windows"
	}
	if !pgdb.ValidC2PayloadTarget(in.Target) {
		return 0, 0, fmt.Errorf("无效目标平台: %q(仅 windows/linux/macos/cross)", in.Target)
	}
	if strings.TrimSpace(in.Name) == "" {
		in.Name = fmt.Sprintf("%s-%s-%s-免杀", l.Name, in.Format, in.Target)
	}
	// 防重入:同档案+监听器有 running 作业 → 拒绝(检查与建行之间的小窗口可接受,
	// 真正的执行互斥由 TakeC2Job 的 CAS 保证)。
	if n, err := pg.CountRunningC2Jobs(pgdb.C2JobKindEvasion, profileID, in.ListenerID); err != nil {
		return 0, 0, err
	} else if n > 0 {
		return 0, 0, errC2EvasionBusy
	}

	// 监看会话:绑在 post 阶段「免杀工程师」名下,请求先落一条用户消息;
	// 完成后由 runner 补 assistant 消息。
	var profileArg *int64
	if p, err := pg.ActiveProfile(); err == nil && p != nil {
		profileArg = &p.ID
	}
	conv, err := pg.CreateConversation(c2EvasionWatchAgent, "免杀变形 · "+in.Name, profileArg)
	if err != nil {
		return 0, 0, fmt.Errorf("创建监看会话失败: %w", err)
	}
	req := fmt.Sprintf("请对监听器 «%s»(#%d) 的 %s/%s 载荷做 LLM 变形(目标 AV:%s),本地静态自测达标后登记为新载荷。",
		l.Name, l.ID, in.Target, in.Format, profileTargetAV(profile))
	if _, err := pg.AppendConvActivity(conv.ID, pgdb.Activity{
		Worker: c2EvasionWatchAgent, Kind: "user", Summary: firstLine(req, 200), Detail: req,
	}); err != nil {
		log.Printf("[c2-evasion] conv %d 写入变形请求失败: %v", conv.ID, err)
	}

	cfg, _ := json.Marshal(map[string]any{
		"listener_id": in.ListenerID, "format": in.Format, "target": in.Target,
		"profile_id": profileID, "conv_id": conv.ID,
	})
	coverageOff := false
	var llmIDs []int64
	if profileArg != nil {
		llmIDs = []int64{*profileArg}
	}
	t, err := s.m.CreateTaskWithOptions("免杀变形 · "+in.Name,
		fmt.Sprintf("对 %s/%s 载荷做 LLM 变形(目标 AV:%s),静态自测达标后登记为新载荷", in.Target, in.Format, profileTargetAV(profile)),
		pgdb.TaskCreateOptions{
			Stage: "post", Kind: "pipeline", StepKey: "c2_evasion", StepConfig: cfg,
			LLMProfileIDs: llmIDs, CoverageEnabled: &coverageOff,
		})
	if err != nil {
		return 0, 0, fmt.Errorf("创建监看任务失败: %w", err)
	}
	input, _ := json.Marshal(in)
	jobID, err := pg.CreateC2Job(pgdb.C2JobInput{
		Kind: pgdb.C2JobKindEvasion, ProfileID: profileID, ListenerID: in.ListenerID,
		TaskID: taskNumericID(t.ID), Input: input,
	})
	if err != nil {
		return 0, 0, err
	}
	log.Printf("[c2-evasion] 变形作业 #%d 已排队(监听器 #%d %s/%s 档案 #%d 任务 %s)",
		jobID, in.ListenerID, in.Target, in.Format, profileID, t.ID)
	s.launchC2EvasionJob(jobID)
	return jobID, taskNumericID(t.ID), nil
}

// profileTargetAV 档案的目标 AV(无档案 = generic)。
func profileTargetAV(p *pgdb.C2EvasionProfile) string {
	if p == nil || strings.TrimSpace(p.TargetAV) == "" {
		return pgdb.C2EvasionTargetGeneric
	}
	return p.TargetAV
}

// launchC2EvasionJob 异步跑一条变形作业(幂等:runner 注册表防双跑)。
func (s *Server) launchC2EvasionJob(jobID int64) {
	ctx, cancel := context.WithCancel(s.ctx)
	if !c2EvRunner.register(jobID, cancel) {
		cancel()
		return // 已在跑
	}
	go func() {
		defer c2EvRunner.unregister(jobID)
		c2EvasionSem <- struct{}{}
		defer func() { <-c2EvasionSem }()
		s.runC2EvasionJob(ctx, jobID)
	}()
}

// runC2EvasionJob 变形作业执行体:领取(CAS 防抢占)→ 置 running → 渲染基础载荷 →
// LLM 变形(瞬态退避重试)→ 静态自测(命中≥3 且轮次<2 喂回再变形)→ 落盘登记 → 终态。
func (s *Server) runC2EvasionJob(ctx context.Context, jobID int64) {
	pg := s.m.PG()
	job, err := pg.GetC2Job(jobID)
	if err != nil || job == nil {
		log.Printf("[c2-evasion] 作业 #%d 读取失败,跳过: %v(%v)", jobID, job, err)
		return
	}
	var in c2EvasionJobInput
	if len(job.Input) > 0 {
		_ = json.Unmarshal(job.Input, &in)
	}
	if in.ListenerID <= 0 || !pgdb.ValidC2PayloadFormat(in.Format) {
		s.failC2EvasionJob(job, jobID, "作业入参不完整(listener_id/format 缺失)")
		return
	}
	convID := s.c2EvasionConvID(job)

	// 终态失败出口:作业 error + 监看任务 failed + 监看会话可见。
	fail := func(reason string) {
		s.failC2EvasionJob(job, jobID, reason)
		if convID > 0 {
			pg.TouchConversation(convID) //nolint:errcheck
		}
	}

	// 防抢占领取:queued→running 条件更新,输家直接退出。
	ok, err := pg.TakeC2Job(jobID)
	if err != nil {
		fail("领取作业失败: " + err.Error())
		return
	}
	if !ok {
		log.Printf("[c2-evasion] 作业 #%d 不在排队态(已被领取/终态),跳过", jobID)
		return
	}
	if convID > 0 {
		if _, err := pg.AppendConvActivity(convID, pgdb.Activity{
			Worker: c2EvasionWatchAgent, Kind: "text",
			Summary: fmt.Sprintf("已开始变形作业 #%d(%s/%s),完成后新载荷自动进 C2 载荷页。", jobID, in.Target, in.Format),
		}); err != nil {
			log.Printf("[c2-evasion] conv %d 写入开工消息失败: %v", convID, err)
		}
		pg.TouchConversation(convID) //nolint:errcheck
	}
	if job.TaskID > 0 {
		if err := s.m.SetTaskStatus(strconv.FormatInt(job.TaskID, 10), "running"); err != nil {
			log.Printf("[c2-evasion] 监看任务 %d 置 running 失败: %v", job.TaskID, err)
		}
	}

	// 档案(目标 AV/技术配置/附加指示)。
	var profile *pgdb.C2EvasionProfile
	if in.ProfileID > 0 {
		if profile, err = pg.GetC2EvasionProfile(in.ProfileID); err != nil || profile == nil {
			fail(fmt.Sprintf("免杀档案 #%d 不存在或读取失败", in.ProfileID))
			return
		}
	}
	targetAV := profileTargetAV(profile)
	techniques := "{}"
	if profile != nil && len(profile.Techniques) > 0 {
		techniques = string(profile.Techniques)
	}

	// LLM 配置:无激活配置 → job error(契约 §13;页面提示去 AI管理→LLM 配置)。
	profileID := int64(0)
	if p, err := pg.ActiveProfile(); err == nil && p != nil {
		profileID = p.ID
	}
	if profileID == 0 {
		fail("未配置 LLM,请先在 AI管理→LLM 配置模型")
		return
	}
	prov, _, okProv := s.providerForProfile(profileID)
	if !okProv {
		fail("未配置 LLM,请先在 AI管理→LLM 配置模型")
		return
	}

	// 基础载荷:c2center.RenderPayload 渲染(server/c2_center.go 的单例 Manager
	// 只负责监听器运行;这里只用渲染纯函数,不触碰 c2center 包行为)。
	l, err := pg.GetC2Listener(in.ListenerID)
	if err != nil || l == nil {
		fail("监听器不存在或读取失败")
		return
	}
	sealed, err := pg.GetC2ListenerKey(in.ListenerID)
	if err != nil {
		fail("读取监听器密钥失败: " + err.Error())
		return
	}
	key, err := s.openC2Secret(sealed)
	if err != nil {
		fail("解封监听器密钥失败: " + err.Error())
		return
	}
	l2 := c2centerListenerFromDB(l)
	filename, base, _, err := c2center.RenderPayload(in.Format, in.Target, l2, key)
	if err != nil {
		fail("渲染基础载荷失败: " + err.Error())
		return
	}
	baseRep := c2EvasionStaticScan(in.Format, string(base))

	// 变形主循环:命中≥3 且轮次<2 → 把命中清单喂回再变形一轮(契约 §13)。
	var plan, code string
	var rep c2StaticReport
	rounds := 0
	user := c2EvasionUserPrompt(string(base), in.Format, targetAV, techniques, in.Instructions, nil)
	for round := 1; round <= c2EvasionMaxRounds; round++ {
		rounds = round
		out, err := analyzeWithRetry(ctx, prov, c2EvasionSystem, user, 2, c2EvasionLLMBudget)
		if err != nil {
			if ctx.Err() != nil {
				fail("已停止或服务重启")
				return
			}
			fail("AI 变形失败: " + firstLine(err.Error(), 200) + "(瞬态失败已自动退避重试)")
			return
		}
		plan, code = splitC2EvasionOutput(out)
		if strings.TrimSpace(code) == "" {
			// 两段缺失:按契约重试一次(单发,不再退避);仍失败置 job error。
			log.Printf("[c2-evasion] 作业 #%d 第 %d 轮输出缺 <CODE> 段,重试一次", jobID, round)
			rctx, rcancel := context.WithTimeout(ctx, c2EvasionLLMBudget)
			out2, rerr := analyzeCompletion(rctx, prov, c2EvasionSystem, user)
			rcancel()
			if rerr == nil {
				plan, code = splitC2EvasionOutput(out2)
			}
		}
		if strings.TrimSpace(code) == "" {
			fail("AI 返回缺少 <CODE> 段,已按约定重试一次仍失败,请换模型或调整档案后重试")
			return
		}
		rep = c2EvasionStaticScan(in.Format, code)
		if c2StaticTotalHits(rep) >= 3 && round < c2EvasionMaxRounds {
			log.Printf("[c2-evasion] 作业 #%d 第 %d 轮静态自测命中 %d 处(%s),喂回再变形一轮",
				jobID, round, c2StaticTotalHits(rep), rep.Risk)
			user = c2EvasionUserPrompt(string(base), in.Format, targetAV, techniques, in.Instructions, &rep)
			continue
		}
		break
	}

	// 产物登记:CreatePayload → 落盘 <BaseDir>/c2/payloads/<id>_<filename>(文件名带
	// payload id,与一期口径一致)→ 回填路径与档案。
	token := c2RandomHex(16)
	oneliner := c2center.Oneliner(in.Format, l2, token)
	sum := sha256Hex([]byte(code))
	name := strings.TrimSpace(in.Name)
	payloadID, err := pg.CreateC2Payload(pgdb.C2PayloadInput{
		ListenerID: in.ListenerID, Name: name, Format: in.Format, Target: in.Target,
		Token: token, SHA256: sum, Size: int64(len(code)), Oneliner: oneliner, CreatedBy: "c2_evasion",
	})
	if err != nil {
		fail("登记变形载荷失败: " + err.Error())
		return
	}
	path := filepath.Join(c2BlobDir(s.m.dir, "payloads"),
		fmt.Sprintf("%d_%s", payloadID, sanitizeReportFilename(filename)))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		_, _ = pg.DeleteC2Payload(payloadID)
		fail("建载荷目录失败: " + err.Error())
		return
	}
	if err := os.WriteFile(path, []byte(code), 0o600); err != nil {
		_, _ = pg.DeleteC2Payload(payloadID)
		fail("写载荷文件失败: " + err.Error())
		return
	}
	if err := pg.UpdateC2PayloadFilePath(payloadID, path); err != nil {
		fail("回填载荷路径失败: " + err.Error())
		return
	}
	if in.ProfileID > 0 {
		if err := pg.SetC2PayloadEvasionProfile(payloadID, in.ProfileID); err != nil {
			log.Printf("[c2-evasion] 载荷 #%d 回填档案失败: %v", payloadID, err)
		}
	}

	result, _ := json.Marshal(map[string]any{
		"payload_id": payloadID, "rounds": rounds,
		"plan":        truncateRunes(strings.TrimSpace(plan), 4000),
		"static":      rep,
		"base_static": baseRep,
		"oneliner":    oneliner,
		"deliver_url": c2center.DeliveryURL(l2, token),
	})
	if err := pg.FinishC2Job(jobID, pgdb.C2JobStatusDone, result, ""); err != nil {
		log.Printf("[c2-evasion] 作业 #%d 回填 done 失败: %v", jobID, err)
	}
	summary := fmt.Sprintf("变形完成:经 %d 轮变形与本地静态自测,特征命中 %d 处(风险 %s,熵 %.2f),"+
		"已登记为新载荷 #%d 并回填档案,到 C2 载荷页下载投递。",
		rounds, c2StaticTotalHits(rep), rep.Risk, rep.Entropy, payloadID)
	if rep.Risk == "high" {
		summary += "注意:静态自测仍为高风险,建议换档案/模型后再跑一轮。"
	}
	if convID > 0 {
		detail := summary + "\n\n【变形说明】\n" + firstLine(strings.TrimSpace(plan), 2000)
		if _, err := pg.AppendConvActivity(convID, pgdb.Activity{
			Worker: c2EvasionWatchAgent, Kind: "text", Summary: firstLine(summary, 200), Detail: detail,
		}); err != nil {
			log.Printf("[c2-evasion] conv %d 写入完成消息失败: %v", convID, err)
		}
		pg.TouchConversation(convID) //nolint:errcheck
	}
	if job.TaskID > 0 {
		_ = s.m.SetTaskStatus(strconv.FormatInt(job.TaskID, 10), "done")
	}
	log.Printf("[c2-evasion] 作业 #%d 完成: %d 轮变形,载荷 #%d,静态自测 %s(命中 %d,熵 %.2f)",
		jobID, rounds, payloadID, rep.Risk, c2StaticTotalHits(rep), rep.Entropy)
}

// c2EvasionConvID 从监看任务的 step_config 补 conv_id(仿 runTemplateTask 读
// t.StepConfig 的口径;任务经 CreateTaskWithOptions/LoadExisting 必在内存)。
func (s *Server) c2EvasionConvID(job *pgdb.C2Job) int64 {
	if job == nil || job.TaskID <= 0 {
		return 0
	}
	t := s.m.ResolveTask(strconv.FormatInt(job.TaskID, 10))
	if t == nil {
		return 0
	}
	var cfg struct {
		ConvID int64 `json:"conv_id"`
	}
	if len(t.StepConfig) > 0 {
		_ = json.Unmarshal(t.StepConfig, &cfg)
	}
	return cfg.ConvID
}

// failC2EvasionJob 作业失败收尾:job error + 监看任务 failed + 监看会话可见。
func (s *Server) failC2EvasionJob(job *pgdb.C2Job, jobID int64, reason string) {
	log.Printf("[c2-evasion] 作业 #%d 失败: %s", jobID, reason)
	if err := s.m.PG().FinishC2Job(jobID, pgdb.C2JobStatusError, nil, reason); err != nil {
		log.Printf("[c2-evasion] 作业 #%d 置 error 失败: %v", jobID, err)
	}
	if job != nil && job.TaskID > 0 {
		_ = s.m.SetTaskStatus(strconv.FormatInt(job.TaskID, 10), "failed")
	}
	if convID := s.c2EvasionConvID(job); convID > 0 {
		if _, err := s.m.PG().AppendConvActivity(convID, pgdb.Activity{
			Worker: c2EvasionWatchAgent, Kind: "text", IsError: true,
			Summary: "变形作业失败:" + firstLine(reason, 200), Detail: reason,
		}); err != nil {
			log.Printf("[c2-evasion] conv %d 写入失败消息: %v", convID, err)
		}
	}
}

// ---- HTTP handlers ----

// c2EvasionProfilePayload 是建/编档案的请求体(techniques 原样 JSON)。
type c2EvasionProfilePayload struct {
	Name          string          `json:"name"`
	TargetAV      string          `json:"target_av"`
	PayloadFormat string          `json:"payload_format"`
	Techniques    json.RawMessage `json:"techniques"`
	Instructions  string          `json:"instructions"`
	Source        string          `json:"source"`
	Notes         string          `json:"notes"`
}

// listC2EvasionProfiles GET /api/c2/evasion/profiles?target_av=&payload_format=&limit=&offset=
func (s *Server) listC2EvasionProfiles(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	q := r.URL.Query()
	items, total, err := s.m.PG().ListC2EvasionProfiles(pgdb.C2EvasionProfileFilter{
		TargetAV:      strings.TrimSpace(q.Get("target_av")),
		PayloadFormat: strings.TrimSpace(q.Get("payload_format")),
		Limit:         atoiDefault(q.Get("limit"), 0),
		Offset:        atoiDefault(q.Get("offset"), 0),
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"profiles": nonNilPtrSlice(items), "total": total})
}

// createC2EvasionProfile POST /api/c2/evasion/profiles → 201 {id}
func (s *Server) createC2EvasionProfile(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	var p c2EvasionProfilePayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	id, err := s.m.PG().CreateC2EvasionProfile(pgdb.C2EvasionProfileInput{
		Name: p.Name, TargetAV: p.TargetAV, PayloadFormat: p.PayloadFormat,
		Techniques: p.Techniques, Instructions: p.Instructions, Source: p.Source, Notes: p.Notes,
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	log.Printf("[c2-evasion] 档案 «%s»(#%d) 已创建", p.Name, id)
	writeJSON(w, 201, map[string]any{"id": id})
}

// updateC2EvasionProfile PUT /api/c2/evasion/profiles/{id} — 局部更新语义:
// 未提供的字段保持原值。
func (s *Server) updateC2EvasionProfile(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	existing, err := s.m.PG().GetC2EvasionProfile(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if existing == nil {
		writeErr(w, 404, "档案不存在")
		return
	}
	var p c2EvasionProfilePayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(p.Name) == "" {
		p.Name = existing.Name
	}
	if strings.TrimSpace(p.TargetAV) == "" {
		p.TargetAV = existing.TargetAV
	}
	if strings.TrimSpace(p.PayloadFormat) == "" {
		p.PayloadFormat = existing.PayloadFormat
	}
	if len(p.Techniques) == 0 {
		p.Techniques = existing.Techniques
	}
	if strings.TrimSpace(p.Instructions) == "" {
		p.Instructions = existing.Instructions
	}
	if strings.TrimSpace(p.Source) == "" {
		p.Source = existing.Source
	}
	if strings.TrimSpace(p.Notes) == "" {
		p.Notes = existing.Notes
	}
	if err := s.m.PG().UpdateC2EvasionProfile(id, pgdb.C2EvasionProfileInput{
		Name: p.Name, TargetAV: p.TargetAV, PayloadFormat: p.PayloadFormat,
		Techniques: p.Techniques, Instructions: p.Instructions, Source: p.Source, Notes: p.Notes,
	}); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	fresh, err := s.m.PG().GetC2EvasionProfile(id)
	if err != nil || fresh == nil {
		writeErr(w, 500, "档案已更新但回读失败")
		return
	}
	writeJSON(w, 200, fresh)
}

// deleteC2EvasionProfile DELETE /api/c2/evasion/profiles/{id}
func (s *Server) deleteC2EvasionProfile(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	deleted, err := s.m.PG().DeleteC2EvasionProfile(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !deleted {
		writeErr(w, 404, "档案不存在")
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": true})
}

// generateC2Evasion POST /api/c2/evasion/generate — {listener_id, format, target,
// name, profile_id?, instructions?} → 202 {job_id, task_id};同档案+监听器有
// running 作业 → 409;gosrc → 400(引导走载荷页构建路径)。
func (s *Server) generateC2Evasion(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	var req c2EvasionJobInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if req.ListenerID <= 0 {
		writeErr(w, 400, "listener_id 必填")
		return
	}
	jobID, taskID, err := s.startC2EvasionJob(req)
	if err != nil {
		if errors.Is(err, errC2EvasionBusy) {
			writeErr(w, 409, err.Error())
			return
		}
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"job_id": jobID, "task_id": taskID})
}

// listC2Jobs GET /api/c2/jobs?kind=&status=&listener_id=&limit=&offset=
func (s *Server) listC2Jobs(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	q := r.URL.Query()
	listenerID, _ := strconv.ParseInt(q.Get("listener_id"), 10, 64)
	items, total, err := s.m.PG().ListC2Jobs(pgdb.C2JobFilter{
		Kind:       strings.TrimSpace(q.Get("kind")),
		Status:     strings.TrimSpace(q.Get("status")),
		ListenerID: listenerID,
		Limit:      atoiDefault(q.Get("limit"), 0),
		Offset:     atoiDefault(q.Get("offset"), 0),
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"jobs": nonNilPtrSlice(items), "total": total})
}

// getC2Job GET /api/c2/jobs/{id} — 含 result(静态报告/构建日志)。
func (s *Server) getC2Job(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	job, err := s.m.PG().GetC2Job(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if job == nil {
		writeErr(w, 404, "作业不存在")
		return
	}
	writeJSON(w, 200, job)
}

// retryC2Job POST /api/c2/jobs/{id}/retry — 仅 error 作业可重试:按原入参新建
// queued 作业(新监看任务),原行保留作审计。build 作业的重试由构建路径负责。
func (s *Server) retryC2Job(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	id, ok := c2PathID(w, r)
	if !ok {
		return
	}
	job, err := s.m.PG().GetC2Job(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if job == nil {
		writeErr(w, 404, "作业不存在")
		return
	}
	if job.Kind != pgdb.C2JobKindEvasion {
		writeErr(w, 400, "仅免杀变形作业支持此重试入口(build 作业请在载荷页重新发起构建)")
		return
	}
	if job.Status != pgdb.C2JobStatusError {
		writeErr(w, 409, "仅失败(error)作业可重试,当前状态: "+job.Status)
		return
	}
	var in c2EvasionJobInput
	if len(job.Input) > 0 {
		_ = json.Unmarshal(job.Input, &in)
	}
	newID, taskID, err := s.startC2EvasionJob(in)
	if err != nil {
		if errors.Is(err, errC2EvasionBusy) {
			writeErr(w, 409, err.Error())
			return
		}
		writeErr(w, 400, err.Error())
		return
	}
	log.Printf("[c2-evasion] 作业 #%d 已重试为新作业 #%d", id, newID)
	writeJSON(w, 200, map[string]any{"job_id": newID, "task_id": taskID})
}

// ---- agent 工具(契约 §13 加分项:AI 自主发起变形并等待结果) ----

// c2WaitJobTerminal 轮询作业到终态(仿 c2WaitTaskTerminal;超时返回当前快照)。
func (s *Server) c2WaitJobTerminal(ctx context.Context, jobID int64, wait time.Duration) (*pgdb.C2Job, error) {
	deadline := time.Now().Add(wait)
	for {
		job, err := s.m.PG().GetC2Job(jobID)
		if err != nil {
			return nil, err
		}
		if job != nil && (job.Status == pgdb.C2JobStatusDone || job.Status == pgdb.C2JobStatusError) {
			return job, nil
		}
		if ctx.Err() != nil {
			return job, ctx.Err()
		}
		if !time.Now().Before(deadline) {
			return job, nil
		}
		select {
		case <-ctx.Done():
			return job, ctx.Err()
		case <-time.After(c2EvasionPollInterval):
		}
	}
}

// seedC2EvasionTool 播种 c2_evasion_generate(首插 only,绑 worker、stage=post)。
func (s *Server) seedC2EvasionTool() {
	if s.m == nil || s.m.PG() == nil {
		return
	}
	t := s.toolC2EvasionGenerate()
	schema, _ := json.Marshal(t.InputSchema())
	agents, _ := json.Marshal([]string{"worker"})
	if err := s.m.PG().SeedToolWithStage(t.Name(), t.Description(), schema, agents, "post"); err != nil {
		log.Printf("[c2-evasion] seed %s 失败: %v", t.Name(), err)
	}
}

// toolC2EvasionGenerate 发起变形作业并等到终态(仿 c2_payload_create 的同路径原则:
// 工具与页面走同一个 startC2EvasionJob)。
func (s *Server) toolC2EvasionGenerate() actool.CoreTool {
	return wrTool("c2_evasion_generate",
		"为指定 C2 监听器发起一次 LLM 载荷变形(免杀):渲染基础 beacon 载荷 → AI 变形 → "+
			"本地静态自测(特征串+熵),命中偏多时自动喂回再变形一轮,产物登记为 C2 新载荷。"+
			"仅支持 ps1/bash/py(gosrc 走载荷页构建路径);监听器需已配置回调地址。返回作业结果与静态自测报告。",
		objSchema(map[string]any{
			"listener_id":  strParam("监听器 id"),
			"format":       strParam("载荷格式:ps1|bash|py(必填)"),
			"target":       strParam("可选:目标平台 windows|linux|macos|cross(默认 windows)"),
			"name":         strParam("可选:产物载荷名称"),
			"profile_id":   strParam("可选:免杀档案 id(提供目标 AV 与技术配置)"),
			"instructions": strParam("可选:附加变形指示"),
		}, "listener_id", "format"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if msg := s.c2RequirePostStage(ctx, "c2_evasion_generate"); msg != "" {
				return actool.Errorf(msg), nil
			}
			if s.m == nil || s.m.PG() == nil {
				return actool.Errorf("数据库未初始化"), nil
			}
			var a c2EvasionJobInput
			_ = json.Unmarshal(in, &a)
			var raw struct {
				ListenerID json.RawMessage `json:"listener_id"`
				ProfileID  json.RawMessage `json:"profile_id"`
			}
			_ = json.Unmarshal(in, &raw)
			a.ListenerID = parseProfileID(raw.ListenerID)
			a.ProfileID = parseProfileID(raw.ProfileID)
			jobID, _, err := s.startC2EvasionJob(a)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			job, err := s.c2WaitJobTerminal(ctx, jobID, c2EvasionToolWait)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("作业 #%d 已排队但等待被取消(%v);可稍后用 GET /api/c2/jobs/%d 查看结果", jobID, err, jobID)), nil
			}
			if job == nil {
				return actool.Errorf(fmt.Sprintf("作业 #%d 状态读取失败", jobID)), nil
			}
			if job.Status == pgdb.C2JobStatusError {
				return actool.Errorf(fmt.Sprintf("作业 #%d 失败: %s", jobID, job.Error)), nil
			}
			if job.Status != pgdb.C2JobStatusDone {
				return jsonResult(map[string]any{
					"job_id": jobID, "status": job.Status,
					"hint": fmt.Sprintf("变形仍在进行中(LLM 变形较慢);可稍后查询作业 #%d 的状态", jobID),
				})
			}
			var result struct {
				PayloadID  int64          `json:"payload_id"`
				Rounds     int            `json:"rounds"`
				Plan       string         `json:"plan"`
				Static     c2StaticReport `json:"static"`
				Oneliner   string         `json:"oneliner"`
				DeliverURL string         `json:"deliver_url"`
			}
			_ = json.Unmarshal(job.Result, &result)
			return jsonResult(map[string]any{
				"job_id": jobID, "status": job.Status,
				"payload_id": result.PayloadID, "rounds": result.Rounds,
				"static_hits": result.Static.Hits, "entropy": result.Static.Entropy,
				"risk": result.Static.Risk, "oneliner": result.Oneliner,
				"deliver_url": result.DeliverURL, "plan": result.Plan,
				"hint": "变形载荷已登记进 C2 载荷页,可用 c2_payload_create 的 oneliner 口径投递",
			})
		})
}
