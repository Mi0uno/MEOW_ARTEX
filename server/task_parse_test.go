package server

// task_parse.go 的单测:只覆盖纯函数(LLM 输出解析/归一、创建请求约束校验),
// 不起真 LLM。handler 的 provider/超时/重试路径照抄 specAIFill,不在本轮覆盖范围。

import (
	"strings"
	"testing"
)

func TestParseTaskInputPlain(t *testing.T) {
	res, err := parseTaskInputResult(`{"goal":"拿到 admin 后台权限","description":"目标站 example.com","constraints":[{"kind":"deny","text":"不得对 /api/gateway 压测"}],"rewards":"","note":"按第一段拆出目标"}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Goal != "拿到 admin 后台权限" || res.Description != "目标站 example.com" {
		t.Fatalf("goal/description = %q/%q", res.Goal, res.Description)
	}
	if len(res.Constraints) != 1 || res.Constraints[0].Kind != "deny" || res.Constraints[0].Text != "不得对 /api/gateway 压测" {
		t.Fatalf("constraints = %+v", res.Constraints)
	}
	if res.Rewards != "" || res.Note != "按第一段拆出目标" {
		t.Fatalf("rewards/note = %q/%q", res.Rewards, res.Note)
	}
}

func TestParseTaskInputFenced(t *testing.T) {
	out := "```json\n{\"goal\":\"g\",\"description\":\"d\",\"constraints\":[],\"rewards\":\"r\",\"note\":\"\"}\n```"
	res, err := parseTaskInputResult(out)
	if err != nil {
		t.Fatalf("parse fenced: %v", err)
	}
	if res.Goal != "g" || res.Description != "d" || res.Rewards != "r" {
		t.Fatalf("fields = %+v", res)
	}
	if res.Constraints == nil || len(res.Constraints) != 0 {
		t.Fatalf("constraints 应为空数组而非 %+v", res.Constraints)
	}
}

func TestParseTaskInputProseWrapped(t *testing.T) {
	// 模型无视「只输出 JSON」的指令,输出「好的,结果如下:{...}希望有帮助」——
	// stripJSONEnvelope 取首个 { 到末个 } 兜底,前后散文不参与解析。
	out := "好的,识别结果如下:\n{\"goal\":\"目标\",\"description\":\"\",\"constraints\":[{\"kind\":\"allow\",\"text\":\"仅测试 *.example.com\"}],\"rewards\":\"\",\"note\":\"\"}\n希望有帮助。"
	res, err := parseTaskInputResult(out)
	if err != nil {
		t.Fatalf("parse prose-wrapped: %v", err)
	}
	if res.Goal != "目标" {
		t.Fatalf("goal = %q", res.Goal)
	}
	if len(res.Constraints) != 1 || res.Constraints[0].Kind != "allow" {
		t.Fatalf("constraints = %+v", res.Constraints)
	}
}

func TestParseTaskInputAllEmptyFails(t *testing.T) {
	// 全空 = 模型什么都没拆出来,必须按失败处理(上层重试一次),不能把空壳当结果。
	for _, out := range []string{
		`{"goal":"","description":"","constraints":[],"rewards":"","note":"没找到可拆的内容"}`,
		`{}`,
	} {
		if res, err := parseTaskInputResult(out); err == nil {
			t.Fatalf("全空输出应报错,得到了 %+v", res)
		}
	}
	// note 有值不算内容:note 是识别说明,不是拆分结果。
	if _, err := parseTaskInputResult(`{"note":"只有说明没有内容"}`); err == nil {
		t.Fatal("只有 note 的输出应按失败处理")
	}
}

func TestParseTaskInputBadJSONFails(t *testing.T) {
	for _, out := range []string{
		"完全不是 JSON 的散文",
		"",
		"```json\n{截断的JSON\n```",
	} {
		if _, err := parseTaskInputResult(out); err == nil {
			t.Fatalf("非法输出 %q 应报错", out)
		}
	}
}

func TestParseTaskInputConstraintsNormalized(t *testing.T) {
	long := strings.Repeat("边", taskIntakeMaxConstraintRunes+50)
	raw := make([]taskConstraintInput, 0, taskIntakeMaxConstraints+5)
	raw = append(raw,
		taskConstraintInput{Kind: "Deny", Text: "  禁止全量扫描  "}, // 大写 + 首尾空白 → deny
		taskConstraintInput{Kind: " ALLOW ", Text: "允许测试主站"},  // 带空白的 allow
		taskConstraintInput{Kind: "forbid", Text: "模型瞎写的种类"},  // 未知 kind → 宁严勿松按 deny
		taskConstraintInput{Kind: "allow", Text: ""},          // 空文本丢弃
		taskConstraintInput{Kind: "allow", Text: "   "},       // 纯空白丢弃
		taskConstraintInput{Kind: "deny", Text: long},         // 超长截断到 2000 runes
		taskConstraintInput{Kind: "", Text: "kind 缺省的约束"},     // 空 kind → deny
	)
	for i := 0; i < taskIntakeMaxConstraints+5; i++ { // 总条数超 40 → 截断
		raw = append(raw, taskConstraintInput{Kind: "allow", Text: strings.Repeat(string(rune('a'+i%26)), i+1)})
	}
	res, err := parseTaskInputResult(`{"goal":"g","description":"d","rewards":"","note":""}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	res.Constraints = normalizeParsedConstraints(raw)

	if len(res.Constraints) != taskIntakeMaxConstraints {
		t.Fatalf("constraints 条数 = %d, want %d", len(res.Constraints), taskIntakeMaxConstraints)
	}
	if res.Constraints[0].Kind != "deny" || res.Constraints[0].Text != "禁止全量扫描" {
		t.Fatalf("c[0] = %+v", res.Constraints[0])
	}
	if res.Constraints[1].Kind != "allow" || res.Constraints[1].Text != "允许测试主站" {
		t.Fatalf("c[1] = %+v", res.Constraints[1])
	}
	if res.Constraints[2].Kind != "deny" {
		t.Fatalf("未知 kind 应按 deny(宁严勿松), got %+v", res.Constraints[2])
	}
	// 空文本的两条被丢弃,后续下标整体前移:3=超长约束,4=kind 缺省。
	got := res.Constraints[3].Text
	if n := len([]rune(got)); n != taskIntakeMaxConstraintRunes {
		t.Fatalf("超长约束应截断到 %d runes, got %d", taskIntakeMaxConstraintRunes, n)
	}
	if !strings.HasPrefix(long, got) {
		t.Fatal("截断应保留前缀(硬截断,不追加省略号)")
	}
	if res.Constraints[4].Kind != "deny" || res.Constraints[4].Text != "kind 缺省的约束" {
		t.Fatalf("缺省 kind 应按 deny, got %+v", res.Constraints[4])
	}
	for i, c := range res.Constraints {
		if c.Kind != "allow" && c.Kind != "deny" {
			t.Fatalf("c[%d].kind = %q, want allow|deny", i, c.Kind)
		}
		if strings.TrimSpace(c.Text) == "" {
			t.Fatalf("c[%d].text 为空,应被丢弃", i)
		}
	}
}

func TestParseTaskInputConstraintsNeverNil(t *testing.T) {
	// 铁律:返回集合的接口不许把 nil 漏出去——模型给 null/缺字段时也要归一为空数组,
	// 否则 JSON null 直接打崩前端渲染。
	for _, out := range []string{
		`{"goal":"g","constraints":null}`,
		`{"goal":"g"}`,
	} {
		res, err := parseTaskInputResult(out)
		if err != nil {
			t.Fatalf("parse %q: %v", out, err)
		}
		if res.Constraints == nil {
			t.Fatalf("%q: constraints 为 nil,前端会拿到 JSON null", out)
		}
	}
}

func TestTaskConstraintValidation(t *testing.T) {
	// 空入参 = 用户没提交约束,静默放行(nil, nil)。
	if got, err := validateTaskConstraintInputs(nil); err != nil || got != nil {
		t.Fatalf("nil 入参应返回 (nil, nil), got %+v, %v", got, err)
	}

	got, err := validateTaskConstraintInputs([]taskConstraintInput{
		{Kind: " Allow ", Text: " 允许测试 *.example.com "},
		{Kind: "deny", Text: "不得动生产数据"},
	})
	if err != nil {
		t.Fatalf("合法列表不应报错: %v", err)
	}
	if len(got) != 2 || got[0].Kind != "allow" || got[0].Text != "允许测试 *.example.com" || got[1].Kind != "deny" {
		t.Fatalf("归一结果 = %+v", got)
	}

	cases := []struct {
		name string
		in   []taskConstraintInput
	}{
		{"kind 非法", []taskConstraintInput{{Kind: "maybe", Text: "x"}}},
		{"kind 缺省", []taskConstraintInput{{Kind: "", Text: "x"}}},
		{"text 为空", []taskConstraintInput{{Kind: "deny", Text: "   "}}},
		{"条数超限", func() []taskConstraintInput {
			many := make([]taskConstraintInput, 0, taskIntakeMaxConstraints+1)
			for i := 0; i <= taskIntakeMaxConstraints; i++ {
				many = append(many, taskConstraintInput{Kind: "deny", Text: strings.Repeat("x", i+1)})
			}
			return many
		}()},
		{"单条超限", []taskConstraintInput{{Kind: "deny", Text: strings.Repeat("长", taskIntakeMaxConstraintRunes+1)}}},
	}
	for _, tc := range cases {
		if got, err := validateTaskConstraintInputs(tc.in); err == nil {
			t.Fatalf("%s: 应报 400,得到了 %+v", tc.name, got)
		}
	}
}
