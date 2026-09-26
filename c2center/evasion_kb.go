package c2center

// 免杀知识库 seeds(契约 C2_CONTRACTS.md §20,Agent E):
// 全网公开防御研究沉淀的技术条目,供内置免杀档案(server/c2_evasion_kb.go)在
// 播种时按目标 AV 取摘要注入变形提示词。本文件是纯数据 + 纯函数:
//   - 不依赖本包任何运行时(监听器/载荷渲染零接触),只 import strings/fmt;
//   - 红线:只沉淀技术原理与防御视角要点,不含任何可利用代码;
//   - 每条 Key 全局唯一,内置档案 techniques JSON 以 kb_keys 引用,改名=破坏
//     档案引用(测试 TestC2EvasionKBSeedsIntegrity 会拦)。
//
// TargetAV 是标签(逗号分隔): defender / 360 / huorong 为对应环境专属条目,
// 含 "generic" 的条目适用于所有环境(通用技术面)。腾讯管家/国产共性条目打
// "360,huorong"。搜集溯源见 docs/c2-evasion-kb-notes.md。

import (
	"fmt"
	"strings"
)

// EvasionKBEntry 是一条免杀知识条目(中英术语对照;Points 3-6 条要点;Refs 来源 URL)。
type EvasionKBEntry struct {
	Key      string   // 全局唯一,内置档案 techniques.kb_keys 引用它
	TargetAV string   // 适用环境标签,逗号分隔:defender/360/huorong;generic=通用
	Title    string   // 标题(中英术语对照)
	Summary  string   // 两三句摘要
	Points   []string // 3-6 条要点
	Refs     []string // 公开来源 URL(溯源:docs/c2-evasion-kb-notes.md)
}

// c2EvasionKB 全量知识条目(顺序即 KBSeedDigest 输出顺序;AV 专属在前,通用在后,
// 由 KBEntriesForAV 保证)。新增条目往对应分段追加,不改动既有 Key。
var c2EvasionKB = []EvasionKBEntry{
	// ---- Windows Defender 检测面(专属) ----
	{
		Key:      "def-amsi",
		TargetAV: "defender",
		Title:    "AMSI 反恶意软件扫描接口 (Antimalware Scan Interface)",
		Summary:  "PowerShell/WSH/VBA/JS 等脚本宿主在执行前把脚本内容(含运行时生成与非混淆内容)提交给已注册的杀软做内存级扫描,Defender 是默认消费方。变形后的脚本若仍经宿主执行,最终提交内容才是扫描对象。",
		Points: []string{
			"扫描点是宿主最终提交的内容:文件落盘形态与提交形态可以不同,静态文件变形对 AMSI 无效但对落盘扫描仍有效",
			"PowerShell 脚本块日志与 AMSI 同源,防御侧可还原运行时生成的内容",
			"ASR 规则复用 AMSI 通道约束脚本行为,混淆密度本身可能触发规则",
			"对 py/bash 等非 AMSI 宿主,该面不适用,重点回到文件静态特征",
		},
		Refs: []string{
			"https://learn.microsoft.com/en-us/defender-endpoint/amsi-on-mdav",
			"https://redcanary.com/blog/threat-detection/better-know-a-data-source/amsi",
		},
	},
	{
		Key:      "def-etw",
		TargetAV: "defender",
		Title:    "ETW 行为遥测 (Event Tracing for Windows)",
		Summary:  "Defender/EDR 依赖进程内 ETW 提供者(脚本宿主、.NET CLR、Threat-Intelligence 等)采集脚本与内存行为遥测。进程停止发 ETW 事件本身就是强告警信号。",
		Points: []string{
			"PowerShell 与 .NET 宿主的活动经 ETW 供防御侧采集,脚本变形不改变遥测通道",
			"ETW 事件流突然中断是可检测异常,防御侧以完整性监控兜底",
			"变形管线的优化目标是静态特征面,不应对抗遥测通道(行为等价铁律)",
		},
		Refs: []string{
			"https://www.binarly.io/blog/design-issues-modern-edrs-bypassing-etw",
			"https://learn.microsoft.com/en-us/windows/win32/debug/event-tracing-portal",
		},
	},
	{
		Key:      "def-cloud-bafs",
		TargetAV: "defender",
		Title:    "云查杀 MAPS / 首见即拦 BAFS / 样本上报 (Cloud protection & sample submission)",
		Summary:  "本地未定毒的样本在云保护与自动样本提交开启时秒级上报云端判定,BAFS 依赖此前提对新样本快速拦截,样本提交分 None/Safe/All 三档。新编译/新变形载荷的首跑窗口决定拦截概率。",
		Points: []string{
			"全新字节的载荷在云端未收录时本地判定为主,首跑是关键窗口",
			"变形产物每次编译字节不同,云查杀需要重新积累信誉,静态变形收益主要在首跑",
			"篡改保护阻止脚本方式关闭云保护/样本提交并记录事件,不要在载荷里碰 Defender 配置",
			"本地静态自测通过 ≠ 云查杀通过;报告口径按静态风险分级即可",
		},
		Refs: []string{
			"https://learn.microsoft.com/en-us/defender-endpoint/cloud-protection-microsoft-antivirus-sample-submissions",
			"https://learn.microsoft.com/en-us/defender-endpoint/block-at-first-sight-microsoft-defender-antivirus",
		},
	},
	{
		Key:      "def-tamper-asr",
		TargetAV: "defender",
		Title:    "篡改保护与攻击面缩减 (Tamper Protection & ASR)",
		Summary:  "篡改保护阻止对 Defender 设置(Set-MpPreference 等)的脚本化修改,ASR 规则集直接约束脚本宿主行为(如禁止派生、Win32 API 声明)。载荷中出现关防护/改配置字符串是高危特征。",
		Points: []string{
			"Set-MpPreference/Add-MpPreference 等字符串是静态特征清单常客,变形应消除或等价改写",
			"ASR 规则面向脚本宿主的行为(Office 产进程、obfuscated 脚本)有专门规则项",
			"对配置类特征串的规避只求过静态扫描,行为层面必须保持载荷原语义",
		},
		Refs: []string{
			"https://learn.microsoft.com/en-us/defender-endpoint/prevent-changes-to-security-settings-with-tamper-protection",
			"https://learn.microsoft.com/en-us/defender-endpoint/attack-surface-reduction-rules-reference",
		},
	},
	{
		Key:      "def-emulator",
		TargetAV: "defender",
		Title:    "静态模拟器与解释器策略 (mpengine emulator & script policy)",
		Summary:  "Defender 对 PE/脚本/压缩包做模拟执行(mpengine 含 x86/JS/脚本解释器),配合熵值、Packer 特征与字符串特征判毒;解释器策略还包括 PowerShell 语言模式等纵深。大段高熵编码块与自解压结构是模拟器重点观察对象。",
		Points: []string{
			"无意义大段编码抬高熵且会被模拟执行还原,等价转义优于整块加密",
			"模拟器可还原常见编码层,单层 base64/hex 不构成有效静态对抗",
			"PowerShell 语言模式(Constrained Language Mode)是运行时纵深,与静态变形无关",
			"引擎自身攻击面(mpengine)公开研究多,但平台不做引擎对抗,只做特征规避",
		},
		Refs: []string{
			"https://msrc.microsoft.com/update-guide/vulnerability/CVE-2017-0290",
			"https://learn.microsoft.com/en-us/defender-endpoint/amsi-on-mdav",
		},
	},
	{
		Key:      "def-behavior-chain",
		TargetAV: "defender",
		Title:    "行为链检测 (Behavioral chain detection)",
		Summary:  "Defender 实时防护对行为链敏感:脚本宿主派生进程、动态声明 Win32 API、计划任务/注册表持久化、以及针对防护软件自身的行为都会进入实时与云端规则。静态变形必须不改变这些行为面。",
		Points: []string{
			"行为链(宿主→派生→注入/持久化)是独立于静态特征的检测面,变形不改变行为",
			"Win32_Process/Add-Type 动态声明类字符串在静态清单内,等价改写可降静态命中",
			"持久化相关字符串(schtasks/reg add)同属静态与行为双重规则,变形时避免新增此类佐料",
		},
		Refs: []string{
			"https://attack.mitre.org/techniques/T1562/",
			"https://redcanary.com/blog/threat-detection/better-know-a-data-source/amsi",
		},
	},

	// ---- 360(专属) ----
	{
		Key:      "cn-360-engine",
		TargetAV: "360",
		Title:    "360 引擎矩阵 (QVM AI 引擎 + 云查杀 + 主防)",
		Summary:  "360 采用多引擎矩阵:QVM 人工智能引擎(机器学习判定+黑白名单闭环)、云查杀引擎、主动防御(主防)与内核级防护(核晶)。其判定是特征+启发+行为+云的综合,单一维度变形只覆盖其中一角。",
		Points: []string{
			"QVM 对样本做模式识别,同源多形态产物重复提交会互相拉低信誉",
			"云查杀依赖上报:断网/未收录时静态判定为主,新编译产物有窗口期",
			"主防看行为(远程线程/写内存/shellcode 加载组合),静态变形不改变行为面",
			"平台红线:载荷行为与信道逐字节等价,不做行为对抗",
		},
		Refs: []string{
			"https://patents.google.com/patent/CN105468975B/zh",
			"https://github.com/TideSec/BypassAntiVirus",
		},
	},
	{
		Key:      "cn-360-triggers",
		TargetAV: "360",
		Title:    "360 已知易触发点 (known triggers)",
		Summary:  "公开实测(TideSec 免杀系列等)显示 360 对敏感 API 组合与常见公开工具特征串敏感,纯本地静态变形(消除特征串/降熵)在云未收录场景通过率较高;脚本类以字符串特征命中为主。",
		Points: []string{
			"iex/DownloadString/base64 大块解码/socket 反连等符号组合是脚本类高发命中",
			"公开工具默认特征串(名字/注释/变量风格)易进特征库,变量与结构重命名有直接收益",
			"多引擎并存意味着本地特征与云特征都要看,静态自测只覆盖本地近似",
		},
		Refs: []string{
			"https://github.com/TideSec/BypassAntiVirus",
		},
	},

	// ---- 火绒(专属) ----
	{
		Key:      "cn-huorong-profile",
		TargetAV: "huorong",
		Title:    "火绒检测侧重 (Huorong detection profile)",
		Summary:  "火绒以静态特征串+行为防御为主,内核 Hook 驱动做行为拦截,自定义规则生态发达;公开实测中其静态与动态均偏严,特征串命中即报。对静态变形而言,火绒是'特征串敏感型'的典型。",
		Points: []string{
			"特征串命中即报:变量名/函数名/注释/常量串都要避免与公开样本雷同",
			"行为面由内核驱动拦截,静态变形不改变行为,保持载荷原语义即可",
			"用户自定义规则可加任意串,产物中的可疑命名(如免杀/进程注入字样)有概率被自定义规则命中",
		},
		Refs: []string{
			"https://www.huorong.cn",
			"https://github.com/TideSec/BypassAntiVirus",
		},
	},
	{
		Key:      "cn-huorong-triggers",
		TargetAV: "huorong",
		Title:    "火绒已知易触发点 (known triggers)",
		Summary:  "脚本类火绒重点盯 PowerShell 敏感符号(Invoke-Expression、下载执行组合)与常见远控模板串;二进制类对 Packer/加密壳特征与导入表形态敏感。熵过高同样可疑。",
		Points: []string{
			"脚本:Invoke-Expression/IEX、DownloadString、FromBase64String 组合是高发命中,拆分与等价改写直接有效",
			"二进制:已知 Packer 签名特征比熵更易触发,Go 原生编译物默认无壳,静态面主要是字符串与符号",
			"公开模板变量名(如 payload/shellcode/reverse)建议全部重命名",
		},
		Refs: []string{
			"https://bbs.huorong.cn/thread-149663-1-1.html",
			"https://github.com/TideSec/BypassAntiVirus",
		},
	},

	// ---- 国产共性(360+火绒) ----
	{
		Key:      "cn-tencent-tav",
		TargetAV: "360,huorong",
		Title:    "腾讯管家 TAV 引擎 (Tencent Anti-Virus)",
		Summary:  "腾讯电脑管家自研 TAV 引擎做高效静态检测,叠加云查杀与哈勃沙箱虚拟执行;动态侧的沙箱判定是其主要干扰面,静态面与国产同行一致偏特征串。",
		Points: []string{
			"TAV 静态检测覆盖多格式文件,特征串与结构特征并重",
			"哈勃沙箱虚拟执行是动态面,静态变形不改变沙箱观测的行为",
			"同类国产引擎的静态规避手法(特征串消除/降熵)对 TAV 同样适用",
		},
		Refs: []string{
			"https://guanjia.qq.com",
		},
	},
	{
		Key:      "cn-static-signature",
		TargetAV: "360,huorong",
		Title:    "国产 AV 静态特征码思路 (signature-based scanning)",
		Summary:  "国产 AV 的静态查杀以特征码/特征串匹配为核心,公开实操以污点检测/二分定位找到触发串后改写;花指令(插入无效字节)与等价编码是常见绕过思路。平台的 LLM 变形即此思路的自动化。",
		Points: []string{
			"定位-改写思路:找到命中的具体字符串/常量,做等价变换而非整体重写",
			"花指令(无行为影响的无效佐料)可稀释特征,但过量会抬熵,本地静态自测有熵阈值兜底",
			"特征库持续更新,变形产物要重新过静态自测,旧结论不沿用",
		},
		Refs: []string{
			"https://github.com/TideSec/BypassAntiVirus",
		},
	},

	// ---- 通用技术面(generic) ----
	{
		Key:      "byp-amsi-lineage",
		TargetAV: "generic",
		Title:    "AMSI bypass 技术谱系 (AMSI bypass lineage)",
		Summary:  "公开研究谱系:反射改写初始化失败标志(2016 起点)→ 内存 patch 扫描入口返回路径 → CLR 只读段字符串改写 → 无 patch 变体(硬件断点/上下文破坏)。防御侧以内存完整性与 ETW 停发兜底检测。",
		Points: []string{
			"谱系演进方向是'少留内存写痕迹',防御面从内存完整性扩展到遥测一致性",
			"patch 类手法改变进程内存完整性,属行为对抗,超出本平台静态变形边界",
			"了解谱系的用途:识别载荷中'声称过 AMSI'的死代码佐料,变形时删除即可降熵降特征",
		},
		Refs: []string{
			"https://medium.com/@byte_St0rm/adventures-in-the-wonderful-world-of-amsi-25d235eb749c",
			"https://www.mdsec.co.uk/2018/06/exploring-powershell-amsi-and-logging-evasion/",
			"https://www.sophos.com/en-us/blog/amsi-bypasses-remain-tricks-of-the-malware-trade",
		},
	},
	{
		Key:      "byp-clr-patchless",
		TargetAV: "generic",
		Title:    "CLR 字符串改写与无 patch 变体 (CLR string patch & patchless variants)",
		Summary:  "除直接 patch 扫描函数外,公开研究还有改写 CLR 只读段中接口名让查找失败、以及硬件断点/线程上下文劫持等无内存写入变体;检测靠只读段完整性与断点注册监控。",
		Points: []string{
			"变体的共同目标是绕开'内存可写页'检测,实现复杂度与检测面此消彼长",
			"脚本载荷中手工复刻这些逻辑会引入 Win32 API 特征串,得不偿失",
			"防御视角:完整性基线+遥测一致性是通用检测思路",
		},
		Refs: []string{
			"https://pentestlaboratories.com/2021/05/17/amsi-bypass-methods/",
			"https://s3cur3th1ssh1t.github.io/Bypass_AMSI_by_manual_modification/",
		},
	},
	{
		Key:      "byp-etw-patch",
		TargetAV: "generic",
		Title:    "ETW patch (EtwEventWrite 改写)",
		Summary:  "公开样本常见组合是 AMSI patch + ETW patch:改写事件上报入口让进程停止发遥测。Binarly/WithSecure 等记录其检测面:事件流中断与内存完整性。",
		Points: []string{
			"ETW 与 AMSI 常被同时提及,但二者是不同通道:遥测 vs 提交扫描",
			"改写 ETW 入口属行为对抗,平台静态变形不涉及",
			"变形时删除载荷里遗留的 ETW 相关字符串佐料可降静态命中",
		},
		Refs: []string{
			"https://www.binarly.io/blog/design-issues-modern-edrs-bypassing-etw",
			"https://www.withsecure.com/en/resources-hub/w-labs/attack-detection-fundamentals-2021-windows-lab-2",
			"https://www.sonicwall.com/blog/remcos-rat-targets-europe-new-amsi-and-etw-evasion-tactics-uncovered",
		},
	},
	{
		Key:      "byp-unhooking",
		TargetAV: "generic",
		Title:    "unhooking / 干净 ntdll 刷新 (unhooking & fresh ntdll)",
		Summary:  "EDR 在 ntdll 导出函数入口写跳转指令监控;公开对抗是取干净 ntdll 覆盖内存副本:磁盘重读、KnownDlls 节映射、挂起进程快照三种来源。检测面是节完整性比对。",
		Points: []string{
			"本质是内存节完整性对抗,属行为/内核对抗范畴,超出静态变形边界",
			"载荷含 ntdll/KnownDlls 等字符串是常见静态特征,变形可等价消除",
			"防御视角:对加载模块做 .text 哈希基线比对是通用检测",
		},
		Refs: []string{
			"https://www.ired.team/offensive-security/defense-evasion",
			"https://www.binarly.io/blog/design-issues-modern-edrs-bypassing-etw",
		},
	},
	{
		Key:      "byp-syscalls",
		TargetAV: "generic",
		Title:    "direct / indirect syscall 与 SSN 解析 (direct & indirect syscalls)",
		Summary:  "绕开被 hook 的 ntdll 直接执行 syscall 指令:direct 在自产代码内完成,indirect 跳到干净 ntdll 的 syscall gadget 返回以改善栈回溯;SSN(系统服务号)动态解析由 SysWhispers/Hell's Gate 谱系工具化。防御转向内核回调与栈对齐检测。",
		Points: []string{
			"syscall 编号是 Windows 版本相关的常量表,产物里出现 SSN 表/哈希解析逻辑是特征",
			"indirect 变体针对的是用户态栈回溯观感,内核态观测不变",
			"平台载荷(gosrc/py)默认走标准 API,静态变形不引入 syscall 逻辑",
		},
		Refs: []string{
			"https://github.com/jthuraisamy/SysWhispers2",
			"https://github.com/jthuraisamy/SysWhispers3",
			"https://github.com/am0nsec/HellsGate",
		},
	},
	{
		Key:      "byp-usermode-hooks",
		TargetAV: "generic",
		Title:    "用户态 Hook 体系 (user-mode hooking: trampoline / IAT / inline)",
		Summary:  "EDR 用户态监控主力是 ntdll 导出入口的 E9 跳转 trampoline,辅以 IAT/inline hook;公开研究(Virus Bulletin 等)系统梳理了其检测输入与脆弱点,防御侧自身也在演进(直接内核回调)。",
		Points: []string{
			"hook 点集中在少数敏感 API(内存/进程/线程操作),这些 API 名出现在载荷里即静态特征",
			"动态解析 API 名(哈希/逐字符拼装)是脚本与二进制共通的等价改写方向",
			"防御视角:hook 完整性自检是 EDR 标配,对抗它属行为范畴",
		},
		Refs: []string{
			"https://www.rbtsec.com",
			"https://www.binarly.io/blog/design-issues-modern-edrs-bypassing-etw",
		},
	},
	{
		Key:      "lod-srdi",
		TargetAV: "generic",
		Title:    "sRDI shellcode 反射式 DLL 注入 (Shellcode Reflective DLL Injection)",
		Summary:  "NetSPI 2017 发布的技术:在普通 DLL 外包一层位置无关 bootstrap,把 DLL 转成 shellcode 注入执行,可指定导出函数入口;本项目 shellcode 产物即此路线(bootstrap 常量来自开源项目,BSD,文件头注明)。",
		Points: []string{
			"产物特征:bootstrap stub 是公开常量,其字节序列本身可能被特征化,变形空间在 stub 参数区",
			"执行入口经回调/远程线程触发,行为面由调用方决定,与 DLL 内容变形正交",
			"静态自测口径:shellcode 高熵属预期,平台对二进制产物不做熵告警",
		},
		Refs: []string{
			"https://github.com/monoxgas/sRDI",
			"https://www.ired.team/offensive-security/code-injection-process-injection",
		},
	},
	{
		Key:      "lod-hollowing",
		TargetAV: "generic",
		Title:    "进程挖空 (Process Hollowing, T1055.012)",
		Summary:  "挂起合法进程→替换其内存映像→改线程上下文恢复执行的注入形态;检测面是父子映像不一致与跨进程写序列。属行为对抗,静态变形仅在'载荷文本中不出现相关 API 名'层面相关。",
		Points: []string{
			"公开实现细节泛型化后,API 序列(NtUnmapViewOfSection/SetThreadContext 等)成为行为规则输入",
			"脚本/源码载荷里硬编码这些 API 名是静态特征,等价消除即可",
			"平台红线:不改变载荷执行入口与用法,不引入新的注入行为",
		},
		Refs: []string{
			"https://www.ired.team/offensive-security/code-injection-process-injection/process-hollowing-and-pe-image-relocations",
			"https://attack.mitre.org/techniques/T1055/",
		},
	},
	{
		Key:      "lod-callback-exec",
		TargetAV: "generic",
		Title:    "回调源执行 (callback-based execution)",
		Summary:  "把载荷指针挂进系统自带的枚举/注册回调 API(字体枚举、证书存储枚举等,LOLBAS 收录大量条目)间接触发执行,规避'直接创建远程线程'类检测;行为面不变,静态面减少线程类 API 名。",
		Points: []string{
			"公开条目库(LOLBAS)是此类活体样本的检索入口,也是特征库的来源之一",
			"脚本载荷模仿此思路会新增枚举 API 依赖,变形禁止新增依赖面(铁律 3)",
			"防御视角:枚举类 API 的回调参数审计是检测点",
		},
		Refs: []string{
			"https://github.com/LOLBAS-Project/LOLBAS",
			"https://attack.mitre.org/techniques/T1106/",
		},
	},
	{
		Key:      "lod-staged",
		TargetAV: "generic",
		Title:    "分阶段加载 (staged loading / cradle)",
		Summary:  "小体积 stager 从信道拉取完整 stage 后加载执行(本项目 staged 产物的通用机制);脚本侧的下载执行 cradle 是最老的形态,静态特征与行为链双重规则覆盖。分阶段把'大体积高熵产物'换成'小体积低熵拉取器'。",
		Points: []string{
			"stager 产物体积小、熵低、特征面小;stage 数据在信道内传输不落盘即可避开文件扫描",
			"cradle 字符串(下载+执行组合)是脚本静态特征清单常客,等价改写有直接收益",
			"staged 与 stageless 的取舍在 OPSEC:落盘版弱(平台 UI 已标注),内存版靠信道",
		},
		Refs: []string{
			"https://attack.mitre.org/techniques/T1106/",
			"https://redcanary.com/blog/threat-detection/better-know-a-data-source/amsi",
		},
	},
	{
		Key:      "lod-dll-sideload",
		TargetAV: "generic",
		Title:    "DLL 侧载 (DLL Side-Loading, T1574.002)",
		Summary:  "把恶意 DLL 放在合法签名 EXE 的加载路径上(利用其导入表/搜索顺序),借宿主文件信誉执行;Cybereason 记录了规模化滥用。检测点是签名进程加载异常路径 DLL。",
		Points: []string{
			"侧载强依赖宿主环境(特定签名 EXE),属投递形态而非载荷变形范畴",
			"DLL 产物的导出名(如约定导出)不能变,变形空间在内部实现与字符串",
			"防御视角:对签名进程的 DLL 加载路径做基线监控",
		},
		Refs: []string{
			"https://attack.mitre.org/techniques/T1574/",
			"https://www.cybereason.com/blog/dll-side-loading",
		},
	},
	{
		Key:      "lod-injection-detect",
		TargetAV: "generic",
		Title:    "进程注入检测面 (process injection detection, T1055)",
		Summary:  "注入族的共性检测输入是跨进程句柄操作与内存写入序列(OpenProcess+WriteProcessMemory+CreateRemoteThread 等组合),公开综述将其列为 EDR 行为规则主力;载荷文本中出现这些 API 名则叠加静态命中。",
		Points: []string{
			"行为检测输入是 API 序列而非单 API,静态变形不改变序列(铁律)",
			"载荷源码/脚本里的注入 API 名可做等价改写(动态解析/拆分)降静态命中",
			"gosrc 载荷默认用标准库网络栈,无需也不应引入注入逻辑",
		},
		Refs: []string{
			"https://attack.mitre.org/techniques/T1055/",
			"https://www.manageengine.com",
		},
	},
	{
		Key:      "go-default-features",
		TargetAV: "generic",
		Title:    "Go 默认编译特征 (Go default build artifacts)",
		Summary:  "Go 产物自带 build ID、runtime.* 常量串、完整符号与 pclntab、构建路径(默认嵌 GOROOT/模块路径);YARA 社区规则以这些常量识别 Go 编译物。-trimpath 与 -ldflags \"-s -w\" 去路径与符号,但运行时特征仍在。",
		Points: []string{
			"识别常量:Go build ID、go1.x 版本串、runtime.main 等,删不掉(运行时依赖)",
			"路径与符号可去:编译期 -trimpath -ldflags \"-s -w\"(平台 VPS 构建已用)",
			"gosrc 源码层免杀重点在字符串常量与依赖面:标识符/注释不进运行时字节,字符串常量全进",
			"默认 Go 二进制体积大、.text 特征明显,是'像 Go'的弱识别面,属可接受特征",
		},
		Refs: []string{
			"https://pkg.go.dev/cmd/link",
			"https://github.com/goretk/gore",
			"https://github.com/burrowers/garble",
		},
	},
	{
		Key:      "go-garble",
		TargetAV: "generic",
		Title:    "garble 混淆 (garble obfuscator)",
		Summary:  "garble 包装 Go 工具链做符号/路径哈希化并剥 build/debug 信息;-literals 把字符串字面量改写为运行时还原表达式;-tiny 删 panic/trace 信息(约 -15% 体积)。导出方法名因接口机制不混淆,seed 固定时产物可聚类。",
		Points: []string{
			"字符串常量是 Go 产物静态特征主力,-literals 直接针对它,代价是运行时开销与更高熵",
			"导出方法与接口方法名不被混淆,保留命名痕迹",
			"固定 seed 的产物确定性可复现,便于回归;对外投递应轮换 seed",
			"平台红线:gosrc 免杀走构建路径与源码工程,不在此处展开工具链替换",
		},
		Refs: []string{
			"https://github.com/burrowers/garble",
		},
	},
	{
		Key:      "go-entropy-resources",
		TargetAV: "generic",
		Title:    "Go 产物熵与资源嵌入 (Go binary entropy & resource embedding)",
		Summary:  "大段嵌入的加密/编码数据块抬高整文件熵,是启发式告警输入;go:embed 与资源文件是把'代码特征'换成'数据特征'的常见手法。熵要和解压/解码行为放在一起看:纯高熵数据不执行则风险低,但会吸引模拟器关注。",
		Points: []string{
			"整文件熵高≠恶意,但特征规则常以'高熵段+无签名'组合做启发",
			"把敏感字符串做成运行时拼接(等价变换)比整块加密更利于压低熵",
			"go:embed 嵌入的资源改动不影响代码逻辑,是源码层可变形面",
		},
		Refs: []string{
			"https://github.com/burrowers/garble",
			"https://pkg.go.dev/cmd/link",
		},
	},
	{
		Key:      "go-signature-forgery",
		TargetAV: "generic",
		Title:    "签名伪造与克隆 (Authenticode signature forgery/cloning)",
		Summary:  "把合法签名块整体拷贝到未签名文件可骗过'只看有无签名/签名者'的弱校验(无私钥也行),SpecterOps 经典研究,MITRE T1553.002 收录;防御=完整哈希与时间戳校验。属强 OPSEC 争议动作,平台不做任何签名能力,只沉淀检测要点。",
		Points: []string{
			"弱校验只查签名存在性,强校验验证文件哈希与证书链完整性",
			"克隆签名在主流检测侧已被覆盖(哈希不符即暴露),实战价值有限",
			"合规红线:教育平台只做原理沉淀,不提供签名工具",
		},
		Refs: []string{
			"https://posts.specterops.io/code-signing-certificate-cloning-attacks-and-defenses-6f98657fc6ec",
			"https://attack.mitre.org/techniques/T1553/",
		},
	},
	{
		Key:      "go-static-research",
		TargetAV: "generic",
		Title:    "Go 混淆对抗研究 (Go deobfuscation research: GoStringUngarbler / GoResolver)",
		Summary:  "防御侧已工具化还原 Go 混淆:Mandiant GoStringUngarbler 还原 garble 字符串,Volexity GoResolver 用控制流图相似度恢复函数名。结论:符号混淆对静态规则有效、对人工分析只加时间成本。",
		Points: []string{
			"字符串还原已自动化,把关键语义只放字符串里的做法不可靠",
			"控制流结构是比符号更强的指纹,重构函数结构能改变 CFG 指纹(平台变形允许的等价调整)",
			"研究以防御视角为主,可用来评估自家变形产物的可逆性",
		},
		Refs: []string{
			"https://cloud.google.com/blog/topics/threat-intelligence/gostringungarbler-deobfuscating-strings-in-garbled-binaries",
			"https://www.volexity.com/blog/2025/04/01/goresolver-using-control-flow-graph-similarity-to-deobfuscate-golang-binaries-automatically",
		},
	},
	{
		Key:      "ob-invoke-obfuscation",
		TargetAV: "generic",
		Title:    "Invoke-Obfuscation 混淆谱系 (PowerShell obfuscation framework)",
		Summary:  "Bohannon 的分层混淆框架:Token/String/Encoding/Launcher 四层组合(字符串拆分、格式符拼接、编码级联、运行时拼装执行),MuddyWater 等组织长期实战使用。检测研究 Revoke-Obfuscation 证明'混淆密度本身即高危信号'。",
		Points: []string{
			"分层思路可借鉴:变量拆分/常量编码/结构调整/无效佐料是四个正交变形维度",
			"过度混淆会触发统计检测(混淆密度),变形要克制,以消除特征串为准",
			"AMSI 抓宿主最终提交内容,PowerShell 载荷的静态变形主要保护落盘/传输形态",
		},
		Refs: []string{
			"https://github.com/danielbohannon/Invoke-Obfuscation",
			"https://attack.mitre.org/techniques/T1027/",
		},
	},
	{
		Key:      "ob-string-split",
		TargetAV: "generic",
		Title:    "字符串拆分与编码层 (string splitting & encoding layers)",
		Summary:  "等价变换族:按位拆分+join、字符码还原、base64/hex 级联、格式化占位重排——运行时还原后逐字节不变。对不同检测面的效果分叉:文件落盘扫描有效;AMSI/模拟器会还原后扫描,单层编码无效。",
		Points: []string{
			"先看检测面再选层:落盘静态扫描(国产 AV 主力)用拆分/拼接即可,模拟器面需要非常规等价式",
			"级联编码抬熵且可被逐层还原,优先用拆分与重组而非整块编码",
			"还原逻辑本身(解码函数名/调用)也是特征,保持项目默认命名更稳",
		},
		Refs: []string{
			"https://www.mdsec.co.uk/2018/06/exploring-powershell-amsi-and-logging-evasion/",
			"https://learn.microsoft.com/en-us/defender-endpoint/amsi-on-mdav",
		},
	},
	{
		Key:      "ob-detection-research",
		TargetAV: "generic",
		Title:    "混淆检测研究 (obfuscation detection research: Revoke-Obfuscation)",
		Summary:  "Bohannon & Holmes 的 Revoke-Obfuscation(BHUSA2017)用统计特征与语料相似度判定混淆,确立'异常符号密度/熵/拼接模式'三信号;后续 ML 检测沿用该思路。对变形的指导:降特征串的同时不要制造统计异常。",
		Points: []string{
			"三大统计信号:特殊符号密度、字符分布熵、拼接调用模式",
			"变形后的代码要保持'自然代码观感':注释可删但不要满屏转义",
			"本地静态自测的熵阈值(>5.5 标高)与该研究口径一致",
		},
		Refs: []string{
			"https://github.com/danielbohannon/Invoke-Obfuscation",
			"https://attack.mitre.org/techniques/T1027/",
		},
	},
	{
		Key:      "ob-python-packers",
		TargetAV: "generic",
		Title:    "Python 打包器 (PyInstaller / PyArmor packing)",
		Summary:  "PyInstaller 打包物(自带 loader 结构+高熵资源)既是 AV 误报重灾区也是检测焦点,pyinstxtractor 可直接还原;PyArmor(Pro) 主动反静态还原但有公开解链案例。py 载荷保持纯脚本形态比打包更利于可控性。",
		Points: []string{
			"打包后特征从'脚本符号'变成'loader 结构',后者特征库覆盖更全",
			"exec/eval/urllib/socket/subprocess/__import__ 是 py 静态清单常客,拆分与等价导入可降命中",
			"py 载荷的运行时依赖(目标机有无依赖库)是变形约束:禁止新增 import(铁律 3)",
		},
		Refs: []string{
			"https://github.com/extremecoders-re/pyinstxtractor",
			"https://pyarmor.dashingsoft.com",
		},
	},
	{
		Key:      "ob-entropy-signals",
		TargetAV: "generic",
		Title:    "熵与编码特征信号 (entropy & encoded-block signals)",
		Summary:  "高熵段(大块 base64/加密数据)是跨引擎通用的启发输入;字节级 Shanon 熵 >5.5 常被用作'疑似加密/编码块'阈值(与平台静态自测口径一致)。变形目标是'特征串消失且熵不升'。",
		Points: []string{
			"等价转义/拆分几乎不升熵,整块加密/压缩必升熵——优先前者",
			"删无意义的混淆佐料(为绕过而绕过的死代码)是降熵降特征的双赢操作",
			"局部高熵不可避免时,缩小高熵段占比比消除它更现实",
		},
		Refs: []string{
			"https://attack.mitre.org/techniques/T1027/",
			"https://www.vmray.com/malware-obfuscation-techniques",
		},
	},
	{
		Key:      "gen-static-discipline",
		TargetAV: "generic",
		Title:    "静态变形纪律 (static mutation discipline)",
		Summary:  "本平台变形管线的边界:只做静态特征规避(变量/函数重命名、常量等价编码、结构等价调整、无行为佐料、字符串拆分重组),信道行为逐字节等价,不碰 AMSI/ETW/注入等行为面。静态自测(特征串+熵)是唯一交付门禁。",
		Points: []string{
			"CB/UP/KEY/URL_PATH 等信道占位符一字不改,加密与轮询节奏不动",
			"禁止新增 import/依赖,禁止改变执行入口与用法",
			"命中特征串优先'等价改写'而非'删除'——删功能=改行为",
			"PLAN 段要说明每处改动为什么不影响行为,便于人工复核",
		},
		Refs: []string{
			"https://attack.mitre.org/techniques/T1027/",
		},
	},
	{
		Key:      "gen-mutation-discipline",
		TargetAV: "generic",
		Title:    "特征串清单与验证闭环 (marker list & feedback loop)",
		Summary:  "平台本地静态自测按格式维护特征串清单(ps1/py/bash 各一组,大小写不敏感计数)+整份代码熵;命中≥3 时把清单喂回再变形一轮(最多两轮)。知识要点的作用是把'为什么这些串会命中'讲给变形模型听。",
		Points: []string{
			"清单里的串是各 AV 静态规则的高频近似,命中≠必死,但清零是最优目标",
			"喂回清单时只允许规避清单内特征,不允许借机改信道行为",
			"两轮后仍 high 属预期内:换档案/换模型重跑,而不是加大混淆",
		},
		Refs: []string{
			"https://github.com/TideSec/BypassAntiVirus",
		},
	},
}

// KBEntriesForAV 按目标 AV 取知识条目:AV 专属条目在前(声明序),通用(generic)
// 条目在后。custom/未知 AV 只返回通用条目。结果每次独立过滤,调用方可安全改写。
func KBEntriesForAV(av string) []EvasionKBEntry {
	av = strings.ToLower(strings.TrimSpace(av))
	specific := []EvasionKBEntry{}
	generic := []EvasionKBEntry{}
	for _, e := range c2EvasionKB {
		tags := strings.Split(strings.ToLower(e.TargetAV), ",")
		matchedSpecific := false
		isGeneric := false
		for _, tg := range tags {
			tg = strings.TrimSpace(tg)
			if tg == "generic" {
				isGeneric = true
			}
			if tg != "" && tg == av {
				matchedSpecific = true
			}
		}
		switch {
		case matchedSpecific:
			specific = append(specific, e)
		case isGeneric:
			generic = append(generic, e)
		}
	}
	out := make([]EvasionKBEntry, 0, len(specific)+len(generic))
	out = append(out, specific...)
	out = append(out, generic...)
	return out
}

// KBEntryByKey 按 Key 精确取条目(内置档案 techniques 引用完整性校验用)。
func KBEntryByKey(key string) (EvasionKBEntry, bool) {
	for _, e := range c2EvasionKB {
		if e.Key == key {
			return e, true
		}
	}
	return EvasionKBEntry{}, false
}

// KBAllKeys 返回全部条目 Key(顺序同声明;测试与文档用)。
func KBAllKeys() []string {
	keys := make([]string, 0, len(c2EvasionKB))
	for _, e := range c2EvasionKB {
		keys = append(keys, e.Key)
	}
	return keys
}

// KBAVLabel 目标 AV 的显示名(摘要头用;与 server 侧 c2EvasionAVDesc 口径对齐)。
func KBAVLabel(av string) string {
	switch strings.ToLower(strings.TrimSpace(av)) {
	case "defender":
		return "Windows Defender"
	case "360":
		return "360 安全卫士"
	case "huorong":
		return "火绒安全"
	case "custom":
		return "自定义环境"
	default:
		return "通用环境"
	}
}

// KBSeedDigest 把某 AV 的知识条目压成提示词可用的摘要文本:
//
//	【免杀知识摘要 · <环境>】(共 N 条)
//	◆ <标题> [<Key>]
//	<Summary>
//	- 要点1
//	- 要点2 …
//
// 每条最多带 3 条要点(控长)。maxChars<=0 表示不限长;超长时按 rune 截断并在
// 末尾注明「已截断」。纯函数,确定性输出(同一 av/maxChars 恒同串)。
func KBSeedDigest(av string, maxChars int) string {
	keys := make([]string, 0, len(c2EvasionKB))
	for _, e := range KBEntriesForAV(av) {
		keys = append(keys, e.Key)
	}
	return kbSeedDigestRender(KBAVLabel(av), keys, maxChars)
}

// KBSeedDigestForKeys 按 Key 列表取摘要(顺序按条目声明序,未知 Key 跳过),
// 格式与截断语义同 KBSeedDigest,label 只进标题行(内置档案传目标 AV 显示名)。
func KBSeedDigestForKeys(label string, keys []string, maxChars int) string {
	return kbSeedDigestRender(label, keys, maxChars)
}

// kbSeedDigestRender 摘要渲染核(label 只进标题行)。
func kbSeedDigestRender(label string, keys []string, maxChars int) string {
	want := make(map[string]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}
	picked := make([]EvasionKBEntry, 0, len(keys))
	for _, e := range c2EvasionKB {
		if want[e.Key] {
			picked = append(picked, e)
		}
	}
	if len(picked) == 0 {
		return ""
	}
	truncNote := ""
	build := func(es []EvasionKBEntry) string {
		var b strings.Builder
		fmt.Fprintf(&b, "【免杀知识摘要 · %s】(共 %d 条)\n", label, len(es))
		for _, e := range es {
			fmt.Fprintf(&b, "◆ %s [%s]\n%s\n", e.Title, e.Key, e.Summary)
			n := 0
			for _, p := range e.Points {
				if n >= 3 {
					break
				}
				fmt.Fprintf(&b, "- %s\n", p)
				n++
			}
		}
		return strings.TrimRight(b.String(), "\n")
	}
	if maxChars <= 0 {
		return build(picked)
	}
	truncNote = fmt.Sprintf("\n…(已截断:完整 %d 条知识条目见内置知识库)", len(picked))
	full := build(picked)
	runes := []rune(full)
	if len(runes) <= maxChars {
		return full
	}
	note := []rune(truncNote)
	if maxChars <= len(note) {
		// 预算比截断说明还小:只返回说明本身的极限截断(保证 ≤ maxChars)。
		return string(note[:maxChars])
	}
	keep := maxChars - len(note)
	return string(runes[:keep]) + truncNote
}
