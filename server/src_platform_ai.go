package server

// SRC 平台规范 AI 识别填入:把平台附件正文交给 LLM,结构化归类为
// 奖励标准/评分标准/守则/规范四类文本,预填前端表单供人工确认后保存。
//
// 为什么只返回建议不落库:AI 对「哪段文字属于哪一类」的判断会出错,而平台规范
// 会转成 task_constraints 注入 AI 约束(防越界的最高优先级边界)——错认类别的
// 规范直接落库等于让错误的边界永久生效。人工检查这道闸必须留在链路上。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

// specAIFillMaxRunes 送入 LLM 的附件正文总量上限(按 rune 计,约 2.4 万字)。
// 附件数量不设限,但一次全塞会超模型上下文;按 rune 截断保证不切坏多字节字符。
const specAIFillMaxRunes = 24000

// specAIFillBodyLimit 请求体上限(1MB):body 只有 id 数组,1MB 已远超任何合法请求。
const specAIFillBodyLimit = 1 << 20

// specPolishMaxRunes 单段规范润色的输入上限:四类文本域单段一般几千字,
// 16k runes 已远超正常篇幅;超限让用户拆分而不是静默截断(截断会丢规则)。
const specPolishMaxRunes = 16000

// specPolishFieldLabel 四类规范字段的中文名与润色侧重要求。
// 润色的红线:所有事实(金额、分值、禁令、流程)一个都不能变——这些文字会注入
// AI 约束,润色改了事实等于改了边界。只优化结构与措辞。
var specPolishFields = map[string]string{
	"reward_standard":  "奖励标准:必须保留全部奖金/积分数值与区间",
	"scoring_standard": "评分标准:必须保留全部等级划分与定级依据",
	"code_of_conduct":  "守则:必须保留全部禁止项与行为红线,措辞保持强硬",
	"rules":            "规范:必须保留全部流程要求与提交约束",
}

// specAIFillTimeout LLM 调用超时。整理数万字规范文档比普通分类任务慢得多,
// 60s 会在长文档上白白失败,给足 120s。
const specAIFillTimeout = 120 * time.Second

// srcSpecSuggestion 是 AI 识别的输出结构,四个文本字段与平台规范一一对应,
// note 说明识别依据(用了哪些附件、哪些内容存疑)。
type srcSpecSuggestion struct {
	RewardStandard  string `json:"reward_standard"`
	ScoringStandard string `json:"scoring_standard"`
	CodeOfConduct   string `json:"code_of_conduct"`
	Rules           string `json:"rules"`
	Note            string `json:"note"`
}

// specAIFill POST /api/src/platforms/{id}/spec/ai-fill  body: {file_ids?: []int64}
//
// file_ids 缺省 = 该平台全部有正文的附件;指定时只取列出的、且确实解析出了正文的附件
// (skipped/failed 的附件没有正文,静默跳过而不是报错——它们本来就不参与识别)。
func (s *Server) specAIFill(w http.ResponseWriter, r *http.Request) {
	platformID, ok := pathInt(r, "id")
	if !ok || platformID <= 0 {
		writeErr(w, 400, "bad platform id")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, specAIFillBodyLimit)
	var req struct {
		FileIDs []int64 `json:"file_ids"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}

	platform, err := s.m.PG().GetSRCPlatform(platformID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if platform == nil {
		writeErr(w, 404, "平台不存在")
		return
	}
	files, err := s.m.PG().ListSRCPlatformFiles(platformID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	corpus, used := buildSRCAICorpus(files, req.FileIDs)
	if corpus == "" {
		writeErr(w, 400, "没有可用正文:请先上传可解析的附件(md/txt/docx/xlsx/pdf),解析成功后再试")
		return
	}

	// provider 解析照抄 vulndb_classify.go:激活配置 → 缓存的 per-profile provider。
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

	ctx, cancel := context.WithTimeout(context.Background(), specAIFillTimeout)
	defer cancel()
	sug, err := s.specAIComplete(ctx, prov, platform.Name, corpus)
	if err != nil {
		writeErr(w, 502, "AI 识别失败:"+err.Error())
		return
	}
	log.Printf("[src-platform] AI 识别填入:平台 #%d «%s» 用了 %d 份附件(%s)",
		platformID, platform.Name, len(used), strings.Join(used, "、"))
	writeJSON(w, 200, map[string]any{"suggestion": sug})
}

// buildSRCAICorpus 拼装送入 LLM 的正文:每份附件一段「### 附件:<文件名>」分隔,
// 让模型能引用到具体文件;总量按 rune 截断到 specAIFillMaxRunes。
// 返回 (正文, 实际使用的文件名列表)。
func buildSRCAICorpus(files []*db.SRCPlatformFile, ids []int64) (string, []string) {
	want := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id > 0 {
			want[id] = struct{}{}
		}
	}
	var b strings.Builder
	used := []string{}
	for _, f := range files {
		if len(want) > 0 {
			if _, hit := want[f.ID]; !hit {
				continue
			}
		}
		text := strings.TrimSpace(f.ContentText)
		if text == "" {
			continue // skipped/failed 的附件没有正文,不参与识别
		}
		fmt.Fprintf(&b, "\n### 附件:%s\n%s\n", f.Filename, text)
		used = append(used, f.Filename)
	}
	return truncateRunes(b.String(), specAIFillMaxRunes), used
}

// specAIComplete 调 LLM 并解析结果;解析失败(或 transport 失败)整体重试 1 次,
// 仍失败返回错误(handler 回 502)。重试的收益主要在模型输出混入散文/围栏导致
// 的偶发解析失败,不值得引入第三次尝试拉长响应时间。
func (s *Server) specAIComplete(ctx context.Context, prov llm.Provider, platformName, corpus string) (*srcSpecSuggestion, error) {
	system := "你是 SRC(安全应急响应中心)平台规范的整理助手。用户给出某平台若干规范附件的正文," +
		"请把内容归类进四个字段:\n" +
		"- reward_standard:奖励标准(漏洞等级对应的奖金/积分/奖励规则)\n" +
		"- scoring_standard:评分标准(漏洞评分/定级/排名规则)\n" +
		"- code_of_conduct:守则(白帽子行为准则/道德规范/保密要求)\n" +
		"- rules:平台规范(提交流程/收录范围/处理时效等一般性规定)\n" +
		"只输出 JSON:{\"reward_standard\":\"\",\"scoring_standard\":\"\",\"code_of_conduct\":\"\",\"rules\":\"\",\"note\":\"\"}。" +
		"要求:某一类在正文里没有依据就留空串,禁止编造正文里没有的内容;" +
		"按正文原意整理,保留关键数字(分数/金额/等级);" +
		"note 说明识别依据——用了哪些附件、哪些内容存疑或归类不确定。" +
		"不要代码块围栏、不要任何解释文字。"
	user := fmt.Sprintf("平台名称: %s\n以下是它的规范附件正文,请归类为要求的 JSON:\n%s", platformName, corpus)

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("识别超时")
			default:
			}
		}
		out, err := analyzeCompletion(ctx, prov, system, user)
		if err != nil {
			lastErr = err
			continue
		}
		sug, perr := parseSRCSpecSuggestion(out)
		if perr == nil {
			return sug, nil
		}
		lastErr = perr
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("未知错误")
	}
	return nil, lastErr
}

// parseSRCSpecSuggestion 剥 ```json 围栏与散文前后缀,再反序列化;
// 四个字段全空说明模型什么都没归类出来,按失败处理(触发重试)。
func parseSRCSpecSuggestion(out string) (*srcSpecSuggestion, error) {
	out = stripJSONEnvelope(out)
	var sug srcSpecSuggestion
	if err := json.Unmarshal([]byte(out), &sug); err != nil {
		return nil, fmt.Errorf("AI 输出无法解析为 JSON: %w", err)
	}
	if strings.TrimSpace(sug.RewardStandard) == "" &&
		strings.TrimSpace(sug.ScoringStandard) == "" &&
		strings.TrimSpace(sug.CodeOfConduct) == "" &&
		strings.TrimSpace(sug.Rules) == "" {
		return nil, fmt.Errorf("AI 未归类出任何规范内容")
	}
	return &sug, nil
}

// stripJSONEnvelope 剥掉代码块围栏;模型仍可能输出「好的,结果如下:{...}」这类
// 散文包裹,取首个 { 到末个 } 的子串兜底。围栏与散文只剥一层,双倍包裹的
// 输出交给 json.Unmarshal 自然报错(会触发重试)。
func stripJSONEnvelope(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	if i := strings.IndexByte(s, '{'); i >= 0 {
		if j := strings.LastIndexByte(s, '}'); j > i {
			return s[i : j+1]
		}
	}
	return strings.TrimSpace(s)
}

// polishSpecField POST /api/src/platforms/spec/polish  body: {field, text}
//
// 对单段规范文本做 AI 润色:只优化结构与措辞,事实(金额/分值/禁令/流程)一个不改。
// 返回 {polished},前端预填回文本框人工检查——与 ai-fill 同一哲学:AI 只建议不落库。
//
// 不需要平台 id:这是纯文本变换,与具体平台无关,便于在平台详情页任意一个
// 规范文本框上使用(尚未保存的新平台也能用)。
func (s *Server) polishSpecField(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, specAIFillBodyLimit)
	var req struct {
		Field string `json:"field"`
		Text  string `json:"text"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	label, ok := specPolishFields[req.Field]
	if !ok {
		writeErr(w, 400, "未知的规范字段:"+req.Field)
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeErr(w, 400, "文本为空,无需润色")
		return
	}
	if utf8.RuneCountInString(text) > specPolishMaxRunes {
		writeErr(w, 400, fmt.Sprintf("文本过长(超过 %d 字),请分段润色", specPolishMaxRunes))
		return
	}

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

	ctx, cancel := context.WithTimeout(context.Background(), specAIFillTimeout)
	defer cancel()
	system := "你是渗透测试平台的安全规范编辑。任务:对用户给出的这一段 SRC 平台「" + label +
		"」文本做润色——优化结构、分条表述、修正错别字与语病,让它更清晰地约束 AI 行为。\n" +
		"铁律:所有事实一个都不能改——金额、分值、等级、禁令、流程、数值范围必须原样保留;" +
		"不许新增原文没有的规则,不许删除原文已有的规则;只输出润色后的文本本身,不要任何解释或围栏。"
	out, err := analyzeCompletion(ctx, prov, system, text)
	if err != nil {
		writeErr(w, 502, "AI 润色失败:"+err.Error())
		return
	}
	polished := strings.TrimSpace(out)
	if polished == "" {
		writeErr(w, 502, "AI 润色失败:模型返回为空")
		return
	}
	log.Printf("[src-platform] 规范润色:字段 %s,输入 %d 字 → 输出 %d 字", req.Field,
		utf8.RuneCountInString(text), utf8.RuneCountInString(polished))
	writeJSON(w, 200, map[string]any{"polished": polished})
}
