package server

// 任务创建智能化:POST /api/tasks/parse-input 的 AI 识别拆分,以及创建任务时
// 用户显式提交的操作约束(rewards/constraints)的校验落点。
//
// 与 specAIFill(src_platform_ai.go)同一哲学:AI 只建议不落库——识别结果回给前端
// 预填表单,人工检查后随创建请求提交;真正的持久化发生在 createTask
// (constraints → task_constraints origin=user,reward_note → tasks.reward_note)。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/norma/llm"
)

// taskParseMaxRunes 送入 LLM 的描述原文上限(按 rune 计,与 specAIFillMaxRunes 同量级):
// 大段粘贴是常态,但一次全塞会超模型上下文;按 rune 截断计数保证多字节不被切坏
// (这里是超限报 400,不是静默截断——丢用户的描述原文不可接受)。
const taskParseMaxRunes = 24000

// taskParseBodyLimit 请求体上限(1MB):description ≤24000 runes(UTF-8 最多约 96KB),
// 1MB 已远超任何合法请求。
const taskParseBodyLimit = 1 << 20

// taskParseTimeout LLM 调用超时。拆数万字长文比普通分类任务慢,与 specAIFill 同给 300s
// (前端 api.ts 另有 320s 兜底,服务端先超时才能把错误文案送回去)。
const taskParseTimeout = 300 * time.Second

// taskIntakeMaxConstraints 约束条数上限:parse-input 侧超限截断,createTask 侧超限报 400
// (用户显式提交,报错比静默丢强),但两侧必须是同一个数,前端编辑器也按 40 封顶。
const taskIntakeMaxConstraints = 40

// taskIntakeMaxConstraintRunes 单条约束文本上限(按 rune 计)。
const taskIntakeMaxConstraintRunes = 2000

// taskConstraintInput 是创建请求/识别结果里的单条操作约束(前端 TaskConstraintInput):
// 创建时还没有 id/时间戳,只有 kind+text;随创建请求写入 task_constraints(origin=user)。
type taskConstraintInput struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// taskInputParse 是 AI 识别拆分的输出结构,与前端 TaskInputParse 一一对应;
// note 说明识别依据(用了哪些段落、哪些归类存疑),展示给用户参考。
type taskInputParse struct {
	Goal        string                `json:"goal"`
	Description string                `json:"description"`
	Constraints []taskConstraintInput `json:"constraints"`
	Rewards     string                `json:"rewards"`
	Note        string                `json:"note"`
}

// parseTaskInput POST /api/tasks/parse-input  body: {description, goal?, src_mode?, src_platform_id?, src_target_id?}
//
// 把一大段任务描述拆成 目标/约束/精简描述;SRC 模式额外识别奖励标准。
// goal/src_* 只作为识别上下文传入(已有目标不重复拆、SRC 才识别奖励),本接口不查库。
func (s *Server) parseTaskInput(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, taskParseBodyLimit)
	var req struct {
		Description   string `json:"description"`
		Goal          string `json:"goal"`
		SRCMode       bool   `json:"src_mode"`
		SRCPlatformID int64  `json:"src_platform_id"`
		SRCTargetID   int64  `json:"src_target_id"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	description := strings.TrimSpace(req.Description)
	if description == "" {
		writeErr(w, 400, "描述为空:没有可识别的内容")
		return
	}
	if utf8.RuneCountInString(description) > taskParseMaxRunes {
		writeErr(w, 400, fmt.Sprintf("描述过长(超过 %d 字),请精简后再试", taskParseMaxRunes))
		return
	}

	// provider 解析照抄 specAIFill:激活配置 → 缓存的 per-profile provider。
	profileID := int64(0)
	if p, err := s.m.pg.ActiveProfile(); err == nil && p != nil {
		profileID = p.ID
	}
	if profileID == 0 {
		writeErr(w, 503, "LLM 未配置:请到 系统 → LLM 配置 添加并激活一个配置")
		return
	}
	prov, _, ok := s.providerForProfile(profileID)
	if !ok {
		writeErr(w, 503, "LLM 未配置:激活的 LLM 配置不可用,请检查 系统 → LLM 配置")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), taskParseTimeout)
	defer cancel()
	start := time.Now()
	res, err := s.taskParseAIComplete(ctx, prov, req.Goal, description, req.SRCMode)
	if err != nil {
		// 失败必须留痕:用户报「超时」时,日志要能回答是慢、是挂、还是输出解析不了。
		log.Printf("[task-intake] AI 识别拆分失败:描述 %d 字,耗时 %s,错误: %v",
			utf8.RuneCountInString(description), time.Since(start).Round(time.Second), err)
		writeErr(w, 502, "AI 识别失败:"+err.Error())
		return
	}
	log.Printf("[task-intake] AI 识别拆分:描述 %d 字 → 目标 %d 字/约束 %d 条/奖励 %d 字,耗时 %s",
		utf8.RuneCountInString(description), utf8.RuneCountInString(res.Goal), len(res.Constraints),
		utf8.RuneCountInString(res.Rewards), time.Since(start).Round(time.Second))
	writeJSON(w, 200, map[string]any{"result": res})
}

// taskParseAIComplete 调 LLM 并解析结果;transport/解析失败整体重试 1 次(同 specAIComplete:
// 重试收益主要在模型输出混入散文/围栏导致的偶发解析失败,不值得第三次拉长响应时间)。
func (s *Server) taskParseAIComplete(ctx context.Context, prov llm.Provider, goal, description string, srcMode bool) (*taskInputParse, error) {
	// rewards 的要求随模式切换:非 SRC 恒空串,不给模型发挥空间(普通任务识别出
	// 奖励只会让前端把它塞进不存在的输入框)。
	rewardsRule := "- rewards:恒为空串(本次不是 SRC 模式,不识别奖励)。"
	if srcMode {
		rewardsRule = "- rewards:奖励标准——把金额/积分/贡献值的等级与区间表整理为 markdown 文本;原文没有奖励信息就给空串。"
	}
	system := "你是渗透测试任务创建助手。用户给出一段大段任务描述原文,请把它拆分进五个字段:\n" +
		"- goal:用户想达成什么(可多行条目,保留原文条目顺序)\n" +
		"- constraints:边界与规则,逐条拆分、禁止把多条合并成一条。禁止事项(不得做什么)→ {\"kind\":\"deny\"};测试范围/允许事项(允许做什么)→ {\"kind\":\"allow\"};原文没有边界/规则就给空数组 []\n" +
		"- description:剔除目标/约束/奖励后剩余的背景与测试对象信息;没有剩余内容就给空串\n" +
		rewardsRule + "\n" +
		"- note:识别依据——拆了哪些段落、哪些归类存疑或不确定\n" +
		"只输出 JSON:{\"goal\":\"\",\"description\":\"\",\"constraints\":[],\"rewards\":\"\",\"note\":\"\"}。" +
		"要求:原文里没有依据的字段一律空串/空数组,禁止编造;保留关键数字(金额/积分/域名/网段);" +
		"不要代码块围栏、不要任何解释文字。"
	user := "任务描述原文:\n" + description
	if g := strings.TrimSpace(goal); g != "" {
		// 已有目标只作参考:模型不该把它再拆一遍,也不能因为描述残缺就把它丢了。
		user = "已有目标(仅供参考,不要重复拆进 goal,也不要丢弃):\n" + g + "\n\n" + user
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("识别超时(%s):描述较长,可精简后重试", taskParseTimeout)
			default:
			}
		}
		out, err := specAIStreamCollect(ctx, prov, system, user, 16384)
		if err != nil {
			lastErr = err
			continue
		}
		res, perr := parseTaskInputResult(out)
		if perr == nil {
			return res, nil
		}
		lastErr = perr
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("未知错误")
	}
	return nil, lastErr
}

// parseTaskInputResult 解析 AI 输出并归一:剥代码块围栏与散文包裹(复用 stripJSONEnvelope,
// 与 specAIComplete 同一套兜底),constraints 归一为 allow|deny。五字段全空说明模型什么
// 都没拆出来,按失败处理(触发上层重试)。
func parseTaskInputResult(out string) (*taskInputParse, error) {
	out = stripJSONEnvelope(out)
	var res taskInputParse
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return nil, fmt.Errorf("AI 输出无法解析为 JSON: %w", err)
	}
	res.Goal = strings.TrimSpace(res.Goal)
	res.Description = strings.TrimSpace(res.Description)
	res.Rewards = strings.TrimSpace(res.Rewards)
	res.Note = strings.TrimSpace(res.Note)
	res.Constraints = normalizeParsedConstraints(res.Constraints)
	if res.Goal == "" && res.Description == "" && res.Rewards == "" && len(res.Constraints) == 0 {
		return nil, fmt.Errorf("AI 未拆分出任何目标/约束/奖励内容")
	}
	return &res, nil
}

// normalizeParsedConstraints 归一 AI 拆出的约束:kind 归一为 allow|deny(只有明确写了
// allow 才按 allow,其余一律 deny——宁严勿松,把不确定的边界当禁止,错杀一条提示总好过
// 放开一条红线);空文本丢弃;超限(>2000 runes/条、>40 条)截断不报错——AI 输出不可信,
// 报错只会让用户重试一次得到同样的截断。
// 返回值永不为 nil:前端契约 constraints 是数组,nil 会序列化成 JSON null 打崩渲染。
func normalizeParsedConstraints(in []taskConstraintInput) []taskConstraintInput {
	out := []taskConstraintInput{}
	for _, c := range in {
		text := strings.TrimSpace(c.Text)
		if text == "" {
			continue
		}
		kind := "deny"
		if strings.EqualFold(strings.TrimSpace(c.Kind), "allow") {
			kind = "allow"
		}
		out = append(out, taskConstraintInput{Kind: kind, Text: truncateRunesHard(text, taskIntakeMaxConstraintRunes)})
		if len(out) >= taskIntakeMaxConstraints {
			break
		}
	}
	return out
}

// validateTaskConstraintInputs 校验用户显式提交的约束列表(createTask 请求):
// 与 parse-input 的「截断」不同——用户显式提交的内容报错比静默丢强(超限/非法回 400,
// 让用户自己改,而不是创建出来的任务少了一条他以为存在的边界)。
// 返回归一后的约束(文本 trim、kind 归一小写),调用方直接逐条落库;空入参返回 nil。
func validateTaskConstraintInputs(in []taskConstraintInput) ([]taskConstraintInput, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > taskIntakeMaxConstraints {
		return nil, fmt.Errorf("操作约束最多 %d 条", taskIntakeMaxConstraints)
	}
	out := make([]taskConstraintInput, 0, len(in))
	for i, c := range in {
		kind := strings.ToLower(strings.TrimSpace(c.Kind))
		if kind != "allow" && kind != "deny" {
			return nil, fmt.Errorf("第 %d 条操作约束的 kind 必须是 allow 或 deny", i+1)
		}
		text := strings.TrimSpace(c.Text)
		if text == "" {
			return nil, fmt.Errorf("第 %d 条操作约束内容为空", i+1)
		}
		if utf8.RuneCountInString(text) > taskIntakeMaxConstraintRunes {
			return nil, fmt.Errorf("第 %d 条操作约束过长(超过 %d 字),请拆分后再提交", i+1, taskIntakeMaxConstraintRunes)
		}
		out = append(out, taskConstraintInput{Kind: kind, Text: text})
	}
	return out, nil
}

// truncateRunesHard 按 rune 硬截断(不追加省略号):约束文本要原样注入 Planner/Worker
// 系统提示,省略号会混进边界语义(与 vulndb_sync.go 展示用的 truncateRunes 区分开)。
func truncateRunesHard(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max])
}
