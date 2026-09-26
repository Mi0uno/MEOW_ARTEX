package server

// 记忆模块纯函数单测(契约 AI_MEMORY_CONTRACTS.md §10 SRV):复盘 JSON 解析容错、
// 内容净化截断、简报 section 拆分与预算截断。不打 DB、不打 LLM、不碰网络。

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func TestParseMemoryReflectJSONValid(t *testing.T) {
	raw := `{"summary":"本任务完成了 ThinkPHP 摸排","highlights":["拿到后台路径"],"failures":[{"what":"爆破 admin","root_cause":"WAF拦截","cost_note":"3 分钟"}],"open_leads":["/backup.zip 未验证"],"facts":[{"op":"add","kind":"tech_stack","subject":"https://demo.example.com","content":"ThinkPHP 5.0","confidence":0.8}],"insights":[{"title":"WAF 拦高频爆破","content":"改低频口令喷洒","kind":"negative","category":"technique","scope":"company","stack_tags":["nginx"]}],"playbooks":[{"match_id":0,"title":"低频口令喷洒","summary":"慢速喷洒绕 WAF","preconditions":{"waf":true},"steps":[{"n":1,"action":"收集用户名","detail":"从 /about 提取"}],"outcome":"success"}]}`
	res, err := parseMemoryReflectJSON(raw)
	if err != nil {
		t.Fatalf("valid JSON 解析失败: %v", err)
	}
	if res.Summary != "本任务完成了 ThinkPHP 摸排" || len(res.Highlights) != 1 || len(res.Failures) != 1 || len(res.OpenLeads) != 1 {
		t.Fatalf("顶层字段解析不符: %+v", res)
	}
	if len(res.Facts) != 1 || res.Facts[0].Kind != "tech_stack" || res.Facts[0].Op != "add" || res.Facts[0].Confidence != 0.8 {
		t.Fatalf("facts 解析不符: %+v", res.Facts)
	}
	if len(res.Insights) != 1 || res.Insights[0].Scope != "company" || res.Insights[0].Kind != "negative" || res.Insights[0].StackTags[0] != "nginx" {
		t.Fatalf("insights 解析不符: %+v", res.Insights)
	}
	if len(res.Playbooks) != 1 || res.Playbooks[0].Outcome != "success" || len(res.Playbooks[0].Steps) != 1 || res.Playbooks[0].Steps[0].N != 1 {
		t.Fatalf("playbooks 解析不符: %+v", res.Playbooks)
	}
}

func TestParseMemoryReflectJSONFenced(t *testing.T) {
	raw := "```json\n{\"summary\":\"围栏包住\",\"highlights\":[],\"failures\":[],\"open_leads\":[],\"facts\":[],\"insights\":[],\"playbooks\":[]}\n```"
	res, err := parseMemoryReflectJSON(raw)
	if err != nil {
		t.Fatalf("围栏 JSON 解析失败: %v", err)
	}
	if res.Summary != "围栏包住" {
		t.Fatalf("summary 不符: %q", res.Summary)
	}
	// 围栏外带杂话 + 无 json 语言标注。
	raw2 := "好的，以下是复盘：\n```\n{\"summary\":\"杂话围栏\",\"highlights\":[],\"failures\":[],\"open_leads\":[],\"facts\":[],\"insights\":[],\"playbooks\":[]}\n```\n完毕"
	if res2, err := parseMemoryReflectJSON(raw2); err != nil || res2.Summary != "杂话围栏" {
		t.Fatalf("杂话围栏解析失败: %v %+v", err, res2)
	}
	// 无围栏但前后有文本:退化为首尾大括号截取。
	raw3 := "结果 {\"summary\":\"裸 JSON\",\"highlights\":[],\"failures\":[],\"open_leads\":[],\"facts\":[],\"insights\":[],\"playbooks\":[]} 结束"
	if res3, err := parseMemoryReflectJSON(raw3); err != nil || res3.Summary != "裸 JSON" {
		t.Fatalf("裸 JSON 解析失败: %v %+v", err, res3)
	}
}

func TestParseMemoryReflectJSONGarbage(t *testing.T) {
	for _, raw := range []string{"", "完全不是 JSON", "{\"summary\": 未闭合"} {
		if _, err := parseMemoryReflectJSON(raw); err == nil {
			t.Fatalf("垃圾输入应报错: %q", raw)
		}
	}
}

func TestSanitizeMemoryReflectCapsAndDefaults(t *testing.T) {
	// 超限输入走完整解析路径:12 条高亮(限10)、40 条事实(限30)、15 条洞察(限10)、
	// 8 条剧本(限5)、600 字摘要(限500)、300 字单条(限200)。
	var b strings.Builder
	b.WriteString(`{"summary":"`)
	b.WriteString(strings.Repeat("长", 600))
	b.WriteString(`","highlights":[`)
	for i := 0; i < 12; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"` + strings.Repeat("h", 300) + `"`)
	}
	b.WriteString(`],"failures":[],"open_leads":[],"facts":[`)
	for i := 0; i < 40; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"op":"奇怪","kind":"奇怪","subject":"bad\u0001\u007fsubj","content":"` + strings.Repeat("c", 300) + `","confidence":9}`)
	}
	b.WriteString(`],"insights":[`)
	for i := 0; i < 15; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"title":"t","kind":"负向","scope":"宇宙"}`)
	}
	b.WriteString(`],"playbooks":[`)
	for i := 0; i < 8; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"title":"p","outcome":" Fail "}`)
	}
	b.WriteString(`]}`)
	res, err := parseMemoryReflectJSON(b.String())
	if err != nil {
		t.Fatalf("超限 JSON 解析失败: %v", err)
	}
	if got := len([]rune(res.Summary)); got > 500 {
		t.Fatalf("summary 未截到 500: %d", got)
	}
	if len(res.Highlights) != 10 || len(res.Facts) != 30 || len(res.Insights) != 10 || len(res.Playbooks) != 5 {
		t.Fatalf("集合长度上限未生效: H=%d F=%d I=%d P=%d", len(res.Highlights), len(res.Facts), len(res.Insights), len(res.Playbooks))
	}
	f := res.Facts[0]
	if f.Op != "add" || f.Kind != "other" {
		t.Fatalf("非法 op/kind 未归一: %+v", f)
	}
	if strings.ContainsAny(f.Subject, "\x01\x7f") {
		t.Fatalf("控制字符未过滤: %q", f.Subject)
	}
	if len([]rune(f.Content)) > 200 || f.Confidence != 0.5 {
		t.Fatalf("content 截断/置信度归一未生效: %d %v", len([]rune(f.Content)), f.Confidence)
	}
	if res.Insights[0].Kind != "positive" || res.Insights[0].Scope != "company" || res.Insights[0].Category != "technique" {
		t.Fatalf("insight 默认值未生效: %+v", res.Insights[0])
	}
	if res.Playbooks[0].Outcome != "success" {
		t.Fatalf("非法 outcome 未归一 success: %q", res.Playbooks[0].Outcome)
	}
	if res.Playbooks[0].Preconditions == nil || res.Playbooks[0].Steps == nil {
		t.Fatalf("集合字段必须非 nil")
	}
	// 空输入也要全字段非 nil。
	empty := sanitizeMemoryReflect(&memoryReflectInput{})
	if empty.Highlights == nil || empty.Failures == nil || empty.OpenLeads == nil || empty.Facts == nil || empty.Insights == nil || empty.Playbooks == nil {
		t.Fatalf("净化结果存在 nil 集合: %+v", empty)
	}
}

func TestCleanMemoryTextRuneCap(t *testing.T) {
	in := strings.Repeat("记", 250)
	if got := len([]rune(cleanMemoryText(in, 200))); got != 200 {
		t.Fatalf("rune 截断应 200,得到 %d", got)
	}
	// 字节数超过 200 但 rune 数不超:不得误截(契约明确按 rune 不按字节)。
	ok := strings.Repeat("记", 100) // 300 字节,100 rune
	if got := cleanMemoryText(ok, 200); got != ok {
		t.Fatalf("300 字节 100 rune 不应截断")
	}
	if got := cleanMemoryText("a\x00b\n\tc\x1fd", 50); got != "ab\n\tcd" {
		t.Fatalf("控制字符过滤结果不符: %q", got)
	}
}

func TestMemorySectionSplitterShape(t *testing.T) {
	sections := []memorySection{
		{Title: "一、已知资产要点", Body: "共 3 个资产(app×1, http×2)\n- https://demo.example.com; 标题: Demo"},
		{Title: "二、历史发现（漏洞）", Body: "- [HIGH] 后台上传(getshell,2026-09-01)"},
		{Title: "三、记忆事实", Body: "[fact#1] (tech_stack) https://demo.example.com: ThinkPHP 5.0"},
		{Title: "四、最近任务复盘", Body: "[run#3 任务45] 完成 ThinkPHP 摸排"},
		{Title: "五、经验洞察（active）", Body: "[insight#7 active] (negative) WAF 拦高频爆破: 改低频"},
		{Title: "六、可用剧本（active）", Body: "[剧本#2] [playbook#2] 低频口令喷洒: 慢速绕 WAF"},
		{Title: "七、防重复指引", Body: memoryAntiRepeat},
	}
	text := assembleMemoryBriefingText(sections)
	wantTitles := []string{"一、已知资产要点", "二、历史发现（漏洞）", "三、记忆事实", "四、最近任务复盘", "五、经验洞察（active）", "六、可用剧本（active）", "七、防重复指引"}
	if len(sections) != len(wantTitles) {
		t.Fatalf("应有 7 段,得到 %d", len(sections))
	}
	for i, sec := range sections {
		if sec.Title != wantTitles[i] {
			t.Fatalf("第 %d 段标题不符: %q != %q", i, sec.Title, wantTitles[i])
		}
	}
	if !strings.HasPrefix(text, "【记忆简报——来自历史任务沉淀】") {
		t.Fatalf("简报文本缺总标题: %q", firstRunes(text, 40))
	}
	if strings.Contains(text, "\n\n\n") {
		t.Fatalf("简报文本不应有连续空行")
	}
	if strings.HasSuffix(text, "\n") || strings.HasSuffix(text, " ") {
		t.Fatalf("简报文本末尾不应带空行/空格")
	}
	if !strings.Contains(text, "七、防重复指引") {
		t.Fatalf("简报缺防重复指引段")
	}
	// 每段都以「段落标题行」起头,前端按同名拆分才有锚点。
	for _, sec := range sections {
		if !strings.Contains(text, "\n\n"+sec.Title+"\n") {
			t.Fatalf("段 %q 未按固定结构出现在文本中", sec.Title)
		}
	}
	if !strings.Contains(sections[6].Body, "不要对它们重复子域枚举、端口扫描或指纹识别") {
		t.Fatalf("防重复指引固定文案被改动")
	}
}

func TestMemoryBriefingBudgetTruncation(t *testing.T) {
	// 整体 8000 rune 预算:构造超长段落,拼装结果必须被截断在预算内且非 nil。
	huge := []memorySection{{Title: "一、已知资产要点", Body: strings.Repeat("长", 9000)}}
	text := assembleMemoryBriefingText(huge)
	if len([]rune(text)) > 8000 {
		t.Fatalf("整体 8000 rune 截断失败: %d", len([]rune(text)))
	}
	// 每条 300 rune 上限。
	if got := len([]rune(truncateRunesHard(strings.Repeat("条", 400), memoryItemMaxRunes))); got != memoryItemMaxRunes {
		t.Fatalf("每条 300 rune 截断失败: %d", got)
	}
	// memoryJoinOrNone:空段给占位,集合非 nil。
	if body := memoryJoinOrNone(nil); body != "(暂无记录)" {
		t.Fatalf("空段应给 (暂无记录): %q", body)
	}
	if body := memoryJoinOrNone([]string{"a", "", "b"}); body != "a\nb" {
		t.Fatalf("空行应被剔除: %q", body)
	}
}

func TestMemoryIsStaleAndTimeParse(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.Local)
	if !memoryIsStale(now.Add(-15*24*time.Hour), 14, now) {
		t.Fatalf("15 天前应判 stale")
	}
	if memoryIsStale(now.Add(-13*24*time.Hour), 14, now) {
		t.Fatalf("13 天前不应判 stale")
	}
	if memoryIsStale(time.Time{}, 14, now) {
		t.Fatalf("零值时间不应判 stale")
	}
	if p := parseMemoryTime("2026-09-01T08:00:00Z"); p == nil || p.IsZero() {
		t.Fatalf("RFC3339 解析失败")
	}
	if p := parseMemoryTime("2026-09-01 08:00:00"); p == nil {
		t.Fatalf("PG 时间戳解析失败")
	}
	if parseMemoryTime("垃圾") != nil {
		t.Fatalf("垃圾时间应返回 nil")
	}
}

func TestMemoryRunFailuresShape(t *testing.T) {
	// MemoryRunFailure JSON 形状与 types.ts 对齐(what/root_cause/cost_note omitempty)。
	b, err := json.Marshal([]db.MemoryRunFailure{{What: "w", RootCause: "r"}})
	if err != nil {
		t.Fatalf("marshal 失败: %v", err)
	}
	want := `[{"what":"w","root_cause":"r"}]`
	if string(b) != want {
		t.Fatalf("failure wire 形状不符: %s", b)
	}
}

// firstRunes 取前 n 个 rune(测试报错信息用)。
func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
