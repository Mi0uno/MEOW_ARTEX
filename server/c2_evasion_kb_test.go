package server

// 免杀知识库单测(契约 C2_CONTRACTS.md §20 第 3 条,Agent E):
//   1. TestC2EvasionKBSeedsIntegrity    知识条目完整性 + 内置档案引用的 KB key 全存在;
//   2. TestC2EvasionKBSeedDigest        摘要组装/确定性/超长截断(注明)语义;
//   3. TestC2EvasionKBProfilesSeedIdempotent 内置档案播种幂等(PG,5499 口径)。
//
// 前两个是纯函数测试(不连库);第三个需要 ARTEX_PG_DSN 显式指向隔离库
// (如 127.0.0.1:5499),未设置自动跳过——照 db/testmain_test.go 的门禁口径。

import (
	"encoding/json"
	"strings"
	"testing"

	c2center "github.com/Autumn-27/artex/c2center"
	pgdb "github.com/Autumn-27/artex/db"
)

// TestC2EvasionKBSeedsIntegrity seeds 完整性:条目结构合法、Key 唯一、内置档案
// techniques 引用的 kb_keys 全部存在、instructions 与 kb_keys 一致。
func TestC2EvasionKBSeedsIntegrity(t *testing.T) {
	keys := c2center.KBAllKeys()
	if len(keys) < 25 {
		t.Fatalf("知识条目 %d 条,契约要求 ≥25", len(keys))
	}
	seen := map[string]bool{}
	for _, k := range keys {
		if k == "" {
			t.Fatalf("存在空 Key")
		}
		if seen[k] {
			t.Fatalf("Key 重复: %s", k)
		}
		seen[k] = true
	}

	validTags := map[string]bool{"defender": true, "360": true, "huorong": true, "generic": true}
	for _, k := range keys {
		e, ok := c2center.KBEntryByKey(k)
		if !ok {
			t.Fatalf("条目 %s 取回失败", k)
		}
		if strings.TrimSpace(e.Title) == "" || strings.TrimSpace(e.Summary) == "" {
			t.Fatalf("条目 %s 标题/摘要为空", k)
		}
		if len(e.Summary) < 20 {
			t.Fatalf("条目 %s 摘要过短(<20 字符): %q", k, e.Summary)
		}
		if n := len(e.Points); n < 3 || n > 6 {
			t.Fatalf("条目 %s 要点数 %d,要求 3-6", k, n)
		}
		for i, p := range e.Points {
			if strings.TrimSpace(p) == "" {
				t.Fatalf("条目 %s 第 %d 条要点为空", k, i)
			}
		}
		if len(e.Refs) < 1 {
			t.Fatalf("条目 %s 无来源(Refs 为空)", k)
		}
		for _, r := range e.Refs {
			if !strings.HasPrefix(r, "http://") && !strings.HasPrefix(r, "https://") {
				t.Fatalf("条目 %s 来源不是 URL: %q", k, r)
			}
		}
		tags := strings.Split(e.TargetAV, ",")
		if len(tags) == 0 {
			t.Fatalf("条目 %s 无 AV 标签", k)
		}
		for _, tg := range tags {
			if !validTags[strings.TrimSpace(tg)] {
				t.Fatalf("条目 %s AV 标签非法: %q", k, tg)
			}
		}
	}

	// 内置档案:8 份、组合合法、kb_keys 全存在、instructions 内嵌 KB 摘要。
	profiles := c2BuiltinEvasionProfiles()
	if len(profiles) != 8 {
		t.Fatalf("内置档案 %d 份,契约要求 8", len(profiles))
	}
	nameSeen := map[string]bool{}
	formatByAV := map[string][]string{}
	for _, p := range profiles {
		if p.Name == "" || nameSeen[p.Name] {
			t.Fatalf("内置档案名称为空或重复: %q", p.Name)
		}
		nameSeen[p.Name] = true
		if !pgdb.ValidC2EvasionTargetAV(p.TargetAV) {
			t.Fatalf("内置档案 «%s» 目标 AV 非法: %q", p.Name, p.TargetAV)
		}
		if !pgdb.ValidC2PayloadFormat(p.Format) {
			t.Fatalf("内置档案 «%s» 载荷格式非法: %q", p.Name, p.Format)
		}
		formatByAV[p.TargetAV] = append(formatByAV[p.TargetAV], p.Format)

		if len(p.KBKeys) == 0 {
			t.Fatalf("内置档案 «%s» 未引用任何知识条目", p.Name)
		}
		for _, k := range p.KBKeys {
			if _, ok := c2center.KBEntryByKey(k); !ok {
				t.Fatalf("内置档案 «%s» 引用不存在的 KB key: %s", p.Name, k)
			}
		}
		techniques, err := c2BuiltinEvasionTechniques(p)
		if err != nil || !json.Valid(techniques) {
			t.Fatalf("内置档案 «%s» techniques JSON 非法: %v", p.Name, err)
		}
		var tj struct {
			KBKeys []string `json:"kb_keys"`
			Focus  string   `json:"focus"`
		}
		if err := json.Unmarshal(techniques, &tj); err != nil {
			t.Fatalf("内置档案 «%s» techniques 解析失败: %v", p.Name, err)
		}
		if len(tj.KBKeys) != len(p.KBKeys) || strings.TrimSpace(tj.Focus) == "" {
			t.Fatalf("内置档案 «%s» techniques 内容与种子不一致", p.Name)
		}
		inst := c2BuiltinEvasionInstructions(p)
		if !strings.Contains(inst, "【免杀知识摘要") {
			t.Fatalf("内置档案 «%s» instructions 未内嵌 KB 摘要(零改动注入点缺失)", p.Name)
		}
		if n := len([]rune(inst)); n > c2EvasionKBDigestMaxChars+200 {
			t.Fatalf("内置档案 «%s» instructions 过长(%d runes),摘要截断未生效", p.Name, n)
		}
	}

	// 覆盖面:defender/360/huorong/generic 四型齐全,ps1 全覆盖。
	for _, av := range []string{"defender", "360", "huorong", "generic"} {
		if len(formatByAV[av]) == 0 {
			t.Fatalf("缺少 %s 的内置档案", av)
		}
	}
	if c2center.KBSeedDigest("custom", 0) == "" {
		t.Fatalf("custom 环境应至少拿到通用知识条目")
	}
	// 注入钩子:整 AV 摘要非空,defender 的第一屏要有 AMSI 条目。
	if kb := c2KBForProfile("defender"); !strings.Contains(kb, "AMSI") {
		t.Fatalf("c2KBForProfile(defender) 未含 AMSI 条目")
	}
}

// TestC2EvasionKBSeedDigest 摘要组装与截断:确定性、截断注明、预算边界、按 keys 取摘要。
func TestC2EvasionKBSeedDigest(t *testing.T) {
	for _, av := range []string{"defender", "360", "huorong", "generic", "custom", "未知环境"} {
		d := c2center.KBSeedDigest(av, 0)
		if d == "" {
			t.Fatalf("KBSeedDigest(%q) 为空", av)
		}
		if !strings.HasPrefix(d, "【免杀知识摘要 ·") {
			t.Fatalf("KBSeedDigest(%q) 缺摘要头: %q", av, firstLine(d, 40))
		}
		if d2 := c2center.KBSeedDigest(av, 0); d2 != d {
			t.Fatalf("KBSeedDigest(%q) 非确定性输出", av)
		}
	}

	// 截断:限长生效且注明;不限长结果必长于限长结果。
	full := c2center.KBSeedDigest("generic", 0)
	capped := c2center.KBSeedDigest("generic", 300)
	if n := len([]rune(capped)); n > 300 {
		t.Fatalf("限长 300 实得 %d runes", n)
	}
	if !strings.Contains(capped, "已截断") {
		t.Fatalf("截断结果未注明: %q", lastLine(capped, 60))
	}
	if len([]rune(full)) <= len([]rune(capped)) {
		t.Fatalf("不限长摘要(%d)应长于限长摘要(%d)", len([]rune(full)), len([]rune(capped)))
	}
	// 预算比截断说明还小的极端情况:仍不超预算且非空。
	tiny := c2center.KBSeedDigest("generic", 5)
	if n := len([]rune(tiny)); n > 5 || n == 0 {
		t.Fatalf("预算 5 实得 %d runes(应为 1-5)", n)
	}
	// 内容抽检:defender 首屏有 AMSI 条目;通用摘要有变形纪律条目。
	if d := c2center.KBSeedDigest("defender", 0); !strings.Contains(d, "AMSI") {
		t.Fatalf("defender 摘要缺 AMSI 条目")
	}
	if d := c2center.KBSeedDigest("generic", 0); !strings.Contains(d, "变形纪律") {
		t.Fatalf("generic 摘要缺变形纪律条目")
	}

	// KBSeedDigestForKeys:按 keys 精确取;未知 key 跳过;空结果返回空串。
	byKeys := c2center.KBSeedDigestForKeys("Windows Defender", []string{"def-amsi", "no-such-key"}, 0)
	if !strings.Contains(byKeys, "AMSI") || strings.Contains(byKeys, "no-such-key") {
		t.Fatalf("KBSeedDigestForKeys 行为不对: %q", firstLine(byKeys, 80))
	}
	if got := c2center.KBSeedDigestForKeys("x", []string{"no-such-key"}, 0); got != "" {
		t.Fatalf("全未知 keys 应返回空串,得: %q", got)
	}
	// byKeys 限长同样遵守预算。
	cappedKeys := c2center.KBSeedDigestForKeys("x", []string{"def-amsi", "def-etw"}, 100)
	if n := len([]rune(cappedKeys)); n > 100 {
		t.Fatalf("ForKeys 限长 100 实得 %d runes", n)
	}
}

// TestC2EvasionKBProfilesSeedIdempotent 内置档案播种幂等(PG 集成):
// 两次播种第二次补 0;库里 source='builtin' 恰为 8 份且内容合法。
// builtin 行不清理(内置种子语义,照 SeedBuiltinToolsets 先例,期望留在隔离库)。
func TestC2EvasionKBProfilesSeedIdempotent(t *testing.T) {
	dsn, err := testDSN()
	if err != nil {
		t.Skipf("ARTEX_PG_DSN 未设置,跳过 PG 测试(%v)", err)
	}
	pg, err := pgdb.Open(dsn) // Open 幂等应用 schema(含 v3 的 source='builtin' 放行)
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer pg.Close()

	want := len(c2BuiltinEvasionProfiles())
	n1, err := seedC2EvasionKBProfiles(pg)
	if err != nil {
		t.Fatalf("首次播种失败: %v", err)
	}
	if n1 < 0 || n1 > want {
		t.Fatalf("首次播种条数异常: %d(预算 %d)", n1, want)
	}
	n2, err := seedC2EvasionKBProfiles(pg)
	if err != nil {
		t.Fatalf("二次播种失败: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("二次播种又插入 %d 条,幂等被破坏", n2)
	}

	var total int
	if err := pg.QueryRow(`SELECT COUNT(*) FROM c2_evasion_profiles WHERE source='builtin'`).Scan(&total); err != nil {
		t.Fatalf("统计内置档案失败: %v", err)
	}
	if total != want {
		t.Fatalf("库内 builtin 档案 %d 份,应为 %d", total, want)
	}

	rows, err := pg.Query(`SELECT name, target_av, payload_format, techniques, instructions
FROM c2_evasion_profiles WHERE source='builtin' ORDER BY id`)
	if err != nil {
		t.Fatalf("读取内置档案失败: %v", err)
	}
	defer rows.Close()
	gotNames := map[string]bool{}
	for rows.Next() {
		var name, av, format, instructions string
		var techniques []byte
		if err := rows.Scan(&name, &av, &format, &techniques, &instructions); err != nil {
			t.Fatalf("扫描内置档案行失败: %v", err)
		}
		if !isBuiltinProfileName(name) {
			t.Fatalf("库内出现未知内置档案 «%s»", name)
		}
		gotNames[name] = true
		if !pgdb.ValidC2EvasionTargetAV(av) || !pgdb.ValidC2PayloadFormat(format) {
			t.Fatalf("内置档案 «%s» 枚举非法: %s/%s", name, av, format)
		}
		if !strings.Contains(instructions, "【免杀知识摘要") {
			t.Fatalf("内置档案 «%s» instructions 未内嵌 KB 摘要", name)
		}
		var tj struct {
			KBKeys []string `json:"kb_keys"`
		}
		if err := json.Unmarshal(techniques, &tj); err != nil || len(tj.KBKeys) == 0 {
			t.Fatalf("内置档案 «%s» techniques.kb_keys 非法(%v)", name, err)
		}
		for _, k := range tj.KBKeys {
			if _, ok := c2center.KBEntryByKey(k); !ok {
				t.Fatalf("内置档案 «%s» 引用不存在的 KB key: %s", name, k)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历内置档案失败: %v", err)
	}
	for _, p := range c2BuiltinEvasionProfiles() {
		if !gotNames[p.Name] {
			t.Fatalf("内置档案 «%s» 未落库", p.Name)
		}
	}
}

// isBuiltinProfileName 名字是否属于当前种子里定义的内置档案。
func isBuiltinProfileName(name string) bool {
	for _, p := range c2BuiltinEvasionProfiles() {
		if p.Name == name {
			return true
		}
	}
	return false
}

// lastLine 取文本最后一行(截 n 个 rune;测试可读性用,同 firstLine 对偶)。
func lastLine(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	r := []rune(s)
	if len(r) > n {
		r = r[len(r)-n:]
	}
	return string(r)
}
