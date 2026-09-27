package server

// PoC LLM 审核管线单测(纯函数 + 假 Provider,不出网、不连库)。
// 覆盖契约 §7.4:审核 JSON 解析三态(严格/围栏/混杂)、低置信保持 pending、
// rejected → SetVulnPocReview 入参拼装。

import (
	"context"
	"iter"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

// pocReviewStubProvider 测试桩:固定文本输出,按 analyzeWithRetry 的消费方式走 Stream。
type pocReviewStubProvider struct{ out string }

func (p *pocReviewStubProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(y func(llm.StreamEvent, error) bool) {
		if !y(llm.StreamEvent{Type: llm.SETextDelta, Text: p.out}, nil) {
			return
		}
		y(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
	}
}

func (p *pocReviewStubProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	for _, err := range p.Stream(ctx, req) {
		if err != nil {
			return llm.Message{}, "", llm.Usage{}, err
		}
	}
	return llm.Message{}, "end_turn", llm.Usage{}, nil
}

// TestPocReviewParseVerdict 审核输出解析:严格 JSON/围栏包裹/多余文本混杂三态可解析,
// 枚举越界/缺字段/非 JSON 一律报错(调用方保持 pending)。
func TestPocReviewParseVerdict(t *testing.T) {
	good := `{"verdict":"rejected","confidence":0.9,"poc_type":"invalid","reason":"仓库空壳只求 star"}`
	cases := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"严格 JSON", good, false},
		{"markdown 围栏包裹", "```json\n" + good + "\n```", false},
		{"多余文本混杂", "审核结论:\n" + good + "\n以上。", false},
		{"围栏无语言标注", "```\n" + good + "\n```", false},
		{"非法 verdict", `{"verdict":"maybe","confidence":0.9,"poc_type":"invalid","reason":"x"}`, true},
		{"非法 poc_type", `{"verdict":"approved","confidence":0.9,"poc_type":"code","reason":"x"}`, true},
		{"缺 reason", `{"verdict":"approved","confidence":0.9,"poc_type":"runnable"}`, true},
		{"空 reason", `{"verdict":"approved","confidence":0.9,"poc_type":"runnable","reason":"  "}`, true},
		{"confidence 越界", `{"verdict":"approved","confidence":1.5,"poc_type":"runnable","reason":"x"}`, true},
		{"confidence 非数值", `{"verdict":"approved","confidence":"高","poc_type":"runnable","reason":"x"}`, true},
		{"纯文本非 JSON", "我觉得这个 PoC 挺好的,建议通过。", true},
		{"空输出", "   ", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, err := parsePocReviewVerdict(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望解析失败,实际得到 %+v", v)
				}
				return
			}
			if err != nil {
				t.Fatalf("期望解析成功,实际报错: %v", err)
			}
			if v.Verdict != "rejected" || v.PocType != "invalid" {
				t.Fatalf("字段解析不符: %+v", v)
			}
			if v.Confidence != 0.9 || !strings.Contains(v.Reason, "star") {
				t.Fatalf("字段解析不符: %+v", v)
			}
		})
	}
}

// TestPocReviewJudgeFakeProvider 假 Provider 走完整 judge 链:低置信/垃圾输出不落库,
// 围栏包裹的高置信判定正常解析。
func TestPocReviewJudgeFakeProvider(t *testing.T) {
	ctx := context.Background()
	// 低置信 0.4:解析成功但证据不足 → 保持 pending(诚实降级,不猜)。
	low := `{"verdict":"rejected","confidence":0.4,"poc_type":"invalid","reason":"疑似课程推销页"}`
	v, raw, err := judgePocReview(ctx, &pocReviewStubProvider{out: low}, "sys", "user")
	if err != nil {
		t.Fatalf("低置信判定不应报错: %v", err)
	}
	if v.Verdict != "rejected" || v.Confidence != 0.4 {
		t.Fatalf("判定解析不符: %+v", v)
	}
	if !strings.Contains(raw, "推销") {
		t.Fatalf("原始输出应原样返回: %q", raw)
	}
	if _, _, ok := pocReviewApplyArgs(v); ok {
		t.Fatal("低置信判定不应通过落库门槛")
	}
	// 围栏 + 前后杂讯 + 高置信:正常解析。
	ok := "好的,结论如下:```json\n" +
		`{"verdict":"approved","confidence":0.85,"poc_type":"runnable","reason":"含完整利用代码与复现步骤"}` +
		"\n```\n请查收。"
	v2, _, err := judgePocReview(ctx, &pocReviewStubProvider{out: ok}, "sys", "user")
	if err != nil {
		t.Fatalf("围栏输出应可解析: %v", err)
	}
	if v2.Verdict != "approved" || v2.PocType != "runnable" || v2.Confidence != 0.85 {
		t.Fatalf("判定解析不符: %+v", v2)
	}
	// 垃圾输出:报错,调用方保持 pending。
	if _, _, err := judgePocReview(ctx, &pocReviewStubProvider{out: "完全不是 JSON 的回答"}, "sys", "user"); err == nil {
		t.Fatal("垃圾输出应报解析错误")
	}
}

// TestPocReviewApplyArgs SetVulnPocReview 入参拼装:rejected 落 note=reason（poc_type）,
// 低置信/不合规判定一律 ok=false(不落库保持 pending)。
func TestPocReviewApplyArgs(t *testing.T) {
	// rejected + 高置信:入参应为 ("rejected", "CVE 编号不匹配（invalid）", true)。
	v := pocReviewVerdict{Verdict: "rejected", Confidence: 0.95, PocType: "invalid", Reason: "CVE 编号不匹配"}
	status, note, ok := pocReviewApplyArgs(v)
	if !ok {
		t.Fatal("高置信 rejected 应落库")
	}
	if status != "rejected" {
		t.Fatalf("status 期望 rejected,得到 %q", status)
	}
	if want := "CVE 编号不匹配（invalid）"; note != want {
		t.Fatalf("note 拼装不符: 期望 %q,得到 %q", want, note)
	}
	// 同一判定置信掉到 0.59:不落库,保持 pending。
	low := v
	low.Confidence = 0.59
	if s, n, ok := pocReviewApplyArgs(low); ok || s != "" || n != "" {
		t.Fatalf("置信 0.59 不应落库: (%q, %q, %v)", s, n, ok)
	}
	// 门槛边界 0.6:恰好达标,允许落库。
	boundary := v
	boundary.Confidence = pocReviewMinConfidence
	if _, _, ok := pocReviewApplyArgs(boundary); !ok {
		t.Fatal("置信恰为 0.6 应允许落库")
	}
	// approved + runnable:status/note 拼装正确。
	good := pocReviewVerdict{Verdict: "approved", Confidence: 0.8, PocType: "runnable", Reason: "含可用利用代码"}
	status, note, ok = pocReviewApplyArgs(good)
	if !ok || status != "approved" || note != "含可用利用代码（runnable）" {
		t.Fatalf("approved 入参不符: (%q, %q, %v)", status, note, ok)
	}
	// 非法 verdict(防御,parse 层已拦):不落库。
	bad := pocReviewVerdict{Verdict: "pass", Confidence: 0.9, PocType: "runnable", Reason: "x"}
	if _, _, ok := pocReviewApplyArgs(bad); ok {
		t.Fatal("非法 verdict 不应落库")
	}
}

// TestPocReviewUserPrompt 提示词拼装:空正文注明仅元信息;条目上下文带 CVE 且截断生效。
func TestPocReviewUserPrompt(t *testing.T) {
	poc := &db.VulnPoc{ID: 7, EntryID: 3, Name: "poc-1234", Kind: "poc", Source: "trickest",
		SourceURL: "https://github.com/some/one"}
	// 条目缺失 + 无正文:应注明仅元信息,且提示不得访问来源链接。
	user := buildPocReviewUserPrompt(nil, poc, "")
	if !strings.Contains(user, "无正文") || !strings.Contains(user, "仅元信息") {
		t.Fatalf("空正文应注明仅元信息:\n%s", user)
	}
	if !strings.Contains(user, "不要访问") {
		t.Fatal("来源链接旁应注明不要访问")
	}
	// 有条目:标题/CVE 在;描述截到 500 rune;正文截到 12000 rune。
	// 描述正文用「甲」做哨兵字(避开「描述:」标签本身含的「描」)。
	entry := &db.VulnEntry{Title: "某组件 RCE", CVEID: "CVE-2024-1234", Vendor: "Acme", Product: "Gate",
		Severity: "critical", Description: strings.Repeat("甲", 600)}
	user = buildPocReviewUserPrompt(entry, poc, strings.Repeat("Ω", 20000))
	if !strings.Contains(user, "CVE-2024-1234") || !strings.Contains(user, "某组件 RCE") {
		t.Fatalf("条目上下文缺失:\n%s", user)
	}
	if got := strings.Count(user, "甲"); got != pocReviewDescChars {
		t.Fatalf("描述应截断到 %d rune,实际出现 %d 次", pocReviewDescChars, got)
	}
	if got := strings.Count(user, "Ω"); got != pocReviewMaxContentChars {
		t.Fatalf("正文应截断到 %d 字符,实际出现 %d 次", pocReviewMaxContentChars, got)
	}
	if strings.Contains(user, "无正文") {
		t.Fatal("有正文时不应再注明仅元信息")
	}
}

// TestPocReviewTruncate 截断助手:中文按 rune、超长补省略号。
func TestPocReviewTruncate(t *testing.T) {
	if got := pocReviewTruncate("  你好 ", 10); got != "你好" {
		t.Fatalf("短文本应仅去空白: %q", got)
	}
	if got := pocReviewTruncate("abcd", 2); got != "ab…" {
		t.Fatalf("超长应截断补省略号: %q", got)
	}
	if got := pocReviewTruncate("anything", 0); got != "" {
		t.Fatalf("n<=0 应返回空: %q", got)
	}
}

// TestPocReviewDedupeIDs 批量入参去重与非正值过滤。
func TestPocReviewDedupeIDs(t *testing.T) {
	got := dedupePocReviewIDs([]int64{3, -1, 3, 0, 5, 3})
	if len(got) != 2 || got[0] != 3 || got[1] != 5 {
		t.Fatalf("去重结果不符: %v", got)
	}
}
