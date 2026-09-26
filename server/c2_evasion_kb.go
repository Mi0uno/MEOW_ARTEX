package server

// 免杀知识库 · 内置档案播种与变形提示词注入(契约 C2_CONTRACTS.md §20,Agent E):
//
//   1. 内置免杀档案播种:8 个 source='builtin' 档案,按 AV 分型(defender/360/
//      huorong/generic)× 载荷格式(ps1/py/gosrc)主力组合。启动幂等(照
//      vpscenter.SeedBuiltinToolsets 模式:按 name 只补缺失,存在一律不覆盖——
//      用户改过的内容不被启动冲掉)。
//   2. KB 注入(零改动方案):变形 runner(server/c2_evasion.go,只读)把档案
//      instructions 原样拼进变形请求的「附加指示」,因此内置档案的 instructions
//      在播种时直接内嵌对应 AV 的 KB 摘要(c2center.KBSeedDigestForKeys),
//      LLM 变形时自然带上知识要点——不改 runner 一行。
//      预留钩子:c2KBForProfile(av) 可取整 AV 知识摘要,若主会话批准在
//      c2EvasionUserPrompt 增加一行按 target_av 注入,可直接使用。
//
//   落库口径:db/c2_evasion.go 的 Go 白名单(ValidC2EvasionSource)尚未含
//   'builtin',CreateC2EvasionProfile 过不了输入归一——故在 server 层直接
//   INSERT,但校验全部复用 db 包导出的白名单(ValidC2EvasionTargetAV /
//   ValidC2PayloadFormat)+ JSON 合法性,字段与 db 层 CREATE 列一字不差。
//   schema 侧 source CHECK 已扩 'builtin'(db/schema.sql v3 段),DB 层无阻碍;
//   需要 db 包补的只有 C2EvasionSourceBuiltin 常量与白名单放行(见报告)。

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	c2center "github.com/Autumn-27/artex/c2center"
	pgdb "github.com/Autumn-27/artex/db"
)

// c2EvasionSourceBuiltin 内置档案来源(与 schema v3 的 source CHECK 对齐)。
const c2EvasionSourceBuiltin = "builtin"

// c2EvasionKBDigestMaxChars 单份内置档案 instructions 里 KB 摘要的字符预算
// (rune 口径;几十条要点压出来约 2-3K,远低于撑爆上下文的量级)。
const c2EvasionKBDigestMaxChars = 6000

// c2BuiltinEvasionProfile 一份内置免杀档案的种子定义。
type c2BuiltinEvasionProfile struct {
	Name       string   // 全局唯一(播种幂等键:source='builtin' AND name)
	TargetAV   string   // defender/360/huorong/generic
	Format     string   // ps1/py/gosrc
	KBKeys     []string // 引用的知识条目(techniques JSON 与 instructions 共用)
	Focus      string   // 该组合的变形侧重点一句话(进 techniques JSON)
	Discipline string   // 额外纪律/说明(附在 KB 摘要后;可为空)
}

// c2BuiltinEvasionProfiles 8 个内置档案(defender/360/huorong/generic ×
// ps1/py/gosrc 主力组合)。ps1 覆盖全部四型(脚本类是变形管线主力);
// gosrc 覆盖 defender/360/generic(编译型载荷走构建路径,档案作为知识预置);
// 火绒补 py(其静态+动态双严,py 是常见实测口径)。
func c2BuiltinEvasionProfiles() []c2BuiltinEvasionProfile {
	return []c2BuiltinEvasionProfile{
		{
			Name: "内置免杀 · Defender · ps1", TargetAV: pgdb.C2EvasionTargetDefender, Format: pgdb.C2PayloadFormatPs1,
			KBKeys: []string{"def-amsi", "def-etw", "def-cloud-bafs", "def-tamper-asr", "def-emulator", "def-behavior-chain",
				"ob-string-split", "ob-invoke-obfuscation", "ob-detection-research", "ob-entropy-signals",
				"gen-static-discipline", "gen-mutation-discipline"},
			Focus:      "脚本类:优先消除 iex/DownloadString/Set-MpPreference 等清单特征串(拆分/等价改写),压住熵;不碰 AMSI/ETW 行为面。",
			Discipline: "注意:AMSI 扫描的是宿主最终提交内容,静态变形保护的是落盘/传输形态,勿在载荷里加 bypass 逻辑(铁律 2/3)。",
		},
		{
			Name: "内置免杀 · Defender · gosrc", TargetAV: pgdb.C2EvasionTargetDefender, Format: pgdb.C2PayloadFormatGoSrc,
			KBKeys: []string{"def-cloud-bafs", "def-emulator", "go-default-features", "go-garble", "go-entropy-resources",
				"go-static-research", "go-signature-forgery", "gen-static-discipline"},
			Focus:      "编译类:gosrc 走构建路径(-trimpath -ldflags \"-s -w\"),源码层重点在字符串常量与依赖面,勿引入 syscall/注入逻辑。",
			Discipline: "本档案为 gosrc 知识预置:gosrc 载荷不经 LLM 变形管线,从载荷页构建;变形字段供构建侧与人工参考。",
		},
		{
			Name: "内置免杀 · 360 · ps1", TargetAV: pgdb.C2EvasionTarget360, Format: pgdb.C2PayloadFormatPs1,
			KBKeys: []string{"cn-360-engine", "cn-360-triggers", "cn-static-signature",
				"ob-string-split", "ob-invoke-obfuscation", "ob-entropy-signals",
				"gen-static-discipline", "gen-mutation-discipline"},
			Focus:      "脚本类:特征串定位-改写优先(360 云未收录时本地静态判定为主);避免大段编码,360 主防看行为,保持载荷原语义。",
			Discipline: "",
		},
		{
			Name: "内置免杀 · 360 · gosrc", TargetAV: pgdb.C2EvasionTarget360, Format: pgdb.C2PayloadFormatGoSrc,
			KBKeys: []string{"cn-360-engine", "cn-360-triggers", "cn-static-signature",
				"go-default-features", "go-garble", "go-entropy-resources", "gen-static-discipline"},
			Focus:      "编译类:Go 运行时特征不可去,重点做字符串常量等价变换与依赖面收敛;QVM 对同源多形态敏感,勿重复堆产物。",
			Discipline: "本档案为 gosrc 知识预置:gosrc 载荷不经 LLM 变形管线,从载荷页构建;变形字段供构建侧与人工参考。",
		},
		{
			Name: "内置免杀 · 火绒 · ps1", TargetAV: pgdb.C2EvasionTargetHuorong, Format: pgdb.C2PayloadFormatPs1,
			KBKeys: []string{"cn-huorong-profile", "cn-huorong-triggers", "cn-static-signature", "cn-tencent-tav",
				"ob-string-split", "ob-invoke-obfuscation", "ob-entropy-signals",
				"gen-static-discipline", "gen-mutation-discipline"},
			Focus:      "脚本类:火绒特征串命中即报,变量名/函数名/注释/常量串全面去模板化,禁止可疑命名(免杀/注入等字样)。",
			Discipline: "",
		},
		{
			Name: "内置免杀 · 火绒 · py", TargetAV: pgdb.C2EvasionTargetHuorong, Format: pgdb.C2PayloadFormatPy,
			KBKeys: []string{"cn-huorong-profile", "cn-huorong-triggers", "cn-static-signature",
				"ob-python-packers", "ob-string-split", "ob-entropy-signals",
				"gen-static-discipline", "gen-mutation-discipline"},
			Focus:      "脚本类(py):exec/eval/socket/subprocess 等敏感符号拆分或等价导入;禁止新增依赖(目标机依赖面不可控)。",
			Discipline: "",
		},
		{
			Name: "内置免杀 · 通用 · ps1", TargetAV: pgdb.C2EvasionTargetGeneric, Format: pgdb.C2PayloadFormatPs1,
			KBKeys: []string{"ob-string-split", "ob-invoke-obfuscation", "ob-detection-research", "ob-entropy-signals",
				"lod-staged", "gen-static-discipline", "gen-mutation-discipline"},
			Focus:      "通用脚本:以静态自测清单为准逐项消除,克制混淆(统计检测盯混淆密度),保持自然代码观感。",
			Discipline: "",
		},
		{
			Name: "内置免杀 · 通用 · gosrc", TargetAV: pgdb.C2EvasionTargetGeneric, Format: pgdb.C2PayloadFormatGoSrc,
			KBKeys: []string{"go-default-features", "go-garble", "go-entropy-resources", "go-static-research",
				"lod-staged", "gen-static-discipline"},
			Focus:      "通用编译类:构建参数已去符号/路径,源码层收敛字符串常量;高熵嵌入块能小则小。",
			Discipline: "本档案为 gosrc 知识预置:gosrc 载荷不经 LLM 变形管线,从载荷页构建;变形字段供构建侧与人工参考。",
		},
	}
}

// c2BuiltinEvasionTechniques 内置档案的 techniques JSON:kb_keys 引用知识库,
// focus 是该组合的变形侧重(LLM 变形时经「技术配置(JSON)」整体进提示词)。
func c2BuiltinEvasionTechniques(p c2BuiltinEvasionProfile) (json.RawMessage, error) {
	raw, err := json.Marshal(map[string]any{
		"kb_keys": p.KBKeys,
		"focus":   p.Focus,
	})
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

// c2BuiltinEvasionInstructions 内置档案的 instructions:头部说明 + 该档案
// kb_keys 对应的 KB 摘要(零改动注入点)+ 组合纪律。变形 runner 把档案
// instructions 作为「附加指示」拼进变形请求,KB 摘要由此自然进入提示词。
func c2BuiltinEvasionInstructions(p c2BuiltinEvasionProfile) string {
	var b strings.Builder
	b.WriteString("【内置免杀档案知识要点】变形时在遵守系统铁律(信道行为逐字节等价、禁新增依赖、禁改执行入口)的前提下参考:\n")
	b.WriteString(c2center.KBSeedDigestForKeys(c2center.KBAVLabel(p.TargetAV), p.KBKeys, c2EvasionKBDigestMaxChars))
	if d := strings.TrimSpace(p.Discipline); d != "" {
		b.WriteString("\n补充: ")
		b.WriteString(d)
	}
	return b.String()
}

// c2KBForProfile 变形提示词注入钩子:按目标 AV 返回整份 AV 知识摘要(AV 专属
// 在前、通用在后,限长)。当前零改动方案不改 runner——KB 摘要已在播种时内嵌进
// 内置档案 instructions;若主会话批准在 c2EvasionUserPrompt/c2EvasionSystem
// 增加一行按 target_av 注入,调本函数即可。
func c2KBForProfile(av string) string {
	return c2center.KBSeedDigest(av, c2EvasionKBDigestMaxChars)
}

// seedC2EvasionKBProfiles 播种内置免杀档案(幂等:source='builtin' AND name
// 只补缺失,已存在一律不覆盖——用户对种子改过的内容不被启动冲掉,照
// SeedBuiltinToolsets 语义)。返回本次新播条数。
//
// 校验走 db 包导出白名单(ValidC2EvasionTargetAV/ValidC2PayloadFormat)+
// JSON 合法性;source='builtin' 因 db 层白名单未放行而走 server 层直接 INSERT
// (列与 db 层 CREATE 完全一致;schema CHECK 已放行 builtin)。
func seedC2EvasionKBProfiles(pg *pgdb.DB) (int, error) {
	if pg == nil {
		return 0, fmt.Errorf("数据库未初始化")
	}
	seeded := 0
	var firstErr error
	for _, p := range c2BuiltinEvasionProfiles() {
		// db 包输入校验(与 normalizeC2EvasionProfileInput 同口径,source 除外)。
		if strings.TrimSpace(p.Name) == "" {
			return seeded, fmt.Errorf("内置档案名称必填")
		}
		if !pgdb.ValidC2EvasionTargetAV(p.TargetAV) {
			return seeded, fmt.Errorf("内置档案 «%s» 目标 AV 非法: %q", p.Name, p.TargetAV)
		}
		if !pgdb.ValidC2PayloadFormat(p.Format) {
			return seeded, fmt.Errorf("内置档案 «%s» 载荷格式非法: %q", p.Name, p.Format)
		}
		techniques, err := c2BuiltinEvasionTechniques(p)
		if err != nil {
			return seeded, fmt.Errorf("内置档案 «%s» techniques 序列化失败: %w", p.Name, err)
		}
		if !json.Valid(techniques) {
			return seeded, fmt.Errorf("内置档案 «%s» techniques 不是合法 JSON", p.Name)
		}
		instructions := c2BuiltinEvasionInstructions(p)
		notes := "内置档案(启动幂等播种,只补缺不覆盖):techniques.kb_keys 引用免杀知识库(" +
			"c2center/evasion_kb.go),instructions 内嵌对应 KB 摘要,LLM 变形时随附加指示进提示词。可编辑,勿改名。"

		// 存在即跳过(幂等键:source='builtin' AND name)。
		var id int64
		err = pg.QueryRow(
			`SELECT id FROM c2_evasion_profiles WHERE source=$1 AND name=$2`,
			c2EvasionSourceBuiltin, p.Name).Scan(&id)
		if err == nil {
			continue // 已存在:不覆盖
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return seeded, fmt.Errorf("查内置档案 «%s» 失败: %w", p.Name, err)
		}
		_, err = pg.Exec(`
INSERT INTO c2_evasion_profiles(name, target_av, payload_format, techniques, instructions, source, notes)
VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			p.Name, p.TargetAV, p.Format, []byte(techniques), instructions, c2EvasionSourceBuiltin, notes)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("播种内置档案 «%s» 失败: %w", p.Name, err)
			}
			log.Printf("[c2-evasion-kb] 播种内置档案 «%s» 失败: %v", p.Name, err)
			continue
		}
		seeded++
		log.Printf("[c2-evasion-kb] 已播种内置免杀档案 «%s»(%s/%s,%d 条知识要点)",
			p.Name, p.TargetAV, p.Format, len(p.KBKeys))
	}
	return seeded, firstErr
}

// seedC2EvasionKB 启动接线入口(照 initC2Evasion 的播种惯例:尽力而为,失败只
// 记日志不阻断启动)。待主会话批准后在 initC2Evasion 尾部追加一行:
//
//	s.seedC2EvasionKB() // 免杀知识库内置档案(Agent E;c2_evasion_kb.go)
func (s *Server) seedC2EvasionKB() {
	if s.m == nil || s.m.PG() == nil {
		return
	}
	n, err := seedC2EvasionKBProfiles(s.m.PG())
	if err != nil {
		log.Printf("[c2-evasion-kb] 内置档案播种未完成: %v", err)
	}
	if n > 0 {
		log.Printf("[c2-evasion-kb] 免杀知识库就绪:新播 %d 份内置档案(共 %d 份)", n, len(c2BuiltinEvasionProfiles()))
	}
}
