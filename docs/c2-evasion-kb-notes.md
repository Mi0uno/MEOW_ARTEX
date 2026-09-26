# C2 免杀知识库 · 全网搜集笔记（Agent E，三期 §20）

> 溯源文档：本文是 `c2center/evasion_kb.go` 内置知识条目（seeds）的来源与摘要，
> 每个条目标注来源 URL。**红线**：只沉淀技术原理与防御视角条目，不复制任何可利用
> 代码/载荷；所有结论来自公开防御研究与开源项目文档。
>
> 搜集方式：WebSearch/WebFetch，共 9 轮主题检索（26 个子查询，2026-09-26），
> 覆盖任务书 6 类目标。检索工具返回的原始链接以本清单为准（个别聚合结果只给到
> 域名级，凡未核实到精确路径的均不入 Refs）。

## 术语速查

| 缩写 | 全称 | 中文 |
|---|---|---|
| AMSI | Antimalware Scan Interface | 反恶意软件扫描接口 |
| ETW | Event Tracing for Windows | Windows 事件跟踪 |
| MAPS | Microsoft Advanced Protection Service | 微软云保护服务（云查杀） |
| BAFS | Block at First Sight | 首见即拦 |
| ASR | Attack Surface Reduction | 攻击面缩减规则 |
| SSN | System Service Number | 系统服务号（syscall 号） |
| sRDI | Shellcode Reflective DLL Injection | shellcode 反射式 DLL 注入 |
| QVM | （360）AI 启发式杀毒引擎 | 360 人工智能引擎 |
| TAV | Tencent Anti-Virus | 腾讯自研反病毒引擎 |

## 1. Windows Defender 当前检测面

- **AMSI（反恶意软件扫描接口）**：PowerShell/WSH/VBA(.NET4.8)/JS 等脚本宿主在
  执行前把脚本内容（含非混淆与运行时生成内容）提交给已注册的 AV 做内存级扫描；
  Defender 是默认消费方。对"无文件/解释器执行"是核心观测面。防御侧 ASR 规则也
  复用 AMSI 通道约束脚本行为。
  来源：https://learn.microsoft.com/en-us/defender-endpoint/amsi-on-mdav 、
  https://redcanary.com/blog/threat-detection/better-know-a-data-source/amsi 、
  https://textslashplain.com/2024/10/25/defensive-technology-antimalware-scan-interface-amsi
- **ETW（事件跟踪）**：EDR/Defender 依赖进程内 ETW 提供者（如 Microsoft-Windows-
  PowerShell、.NET CLR、Threat-Intelligence）采集脚本/内存/敏感 API 行为遥测；
  用户态 patch EtwEventWrite 可让进程停止发事件（公开野样本常见组合：
  AMSI patch + ETW patch，见 SonicWall Remcos 分析、WithSecure 实验室演练）。
  来源：https://www.binarly.io/blog/design-issues-modern-edrs-bypassing-etw 、
  https://www.sonicwall.com/blog/remcos-rat-targets-europe-new-amsi-and-etw-evasion-tactics-uncovered 、
  https://www.withsecure.com/en/resources-hub/w-labs/attack-detection-fundamentals-2021-windows-lab-2
- **云查杀（MAPS）与样本上报**：本地未定毒样本在「云保护+自动样本提交」开启时
  秒级上报云端判定（BAFS 首见即拦即依赖此前提），三档样本提交级别 None/Safe/All。
  对新编译/新变形载荷的意义：首跑窗口决定拦截概率；篡改保护（Tamper Protection）
  会阻止用 Set-MpPreference 等方式关掉这些开关并记事件。
  来源：https://learn.microsoft.com/en-us/defender-endpoint/cloud-protection-microsoft-antivirus-sample-submissions 、
  https://learn.microsoft.com/en-us/defender-endpoint/block-at-first-sight-microsoft-defender-antivirus 、
  https://learn.microsoft.com/en-us/defender-endpoint/prevent-changes-to-security-settings-with-tamper-protection 、
  https://learn.microsoft.com/en-us/defender-endpoint/attack-surface-reduction-rules-reference
- **静态模拟器（mpengine）与解释器策略**：Defender 对 PE/脚本/压缩包做模拟执行
  （mpengine，x86/JS/PowerShell 均有解释器），配合熵、Packer 特征、字符串特征
  判毒；解释器策略还包括 PowerShell 语言模式与脚本块日志（防御纵深）。
  任务书里的 "MinEU" 未检索到对应公开组件——学界/业界的公开线索是 mpengine 模拟器
  本身（含 CVE-2017-0290 mpengine 引擎 RCE 的攻击面记录）与同名异物的学术 taint
  tracker "Minemu"（RAID 2011），二者都不是 "MinEU 策略"；KB 条目按「静态模拟器
  与解释器策略」沉淀。
  来源：https://msrc.microsoft.com/update-guide/vulnerability/CVE-2017-0290 、
  https://learn.microsoft.com/en-us/defender-endpoint/amsi-on-mdav
- **行为检测**：不再是单纯"文件查杀"——脚本宿主派生、Add-Type/Win32_API 声明、
  进程注入 API 序列（OpenProcess+WriteProcessMemory+CreateRemoteThread）、计划
  任务/注册表持久化、Defender 自身配置被改等行为链都会触发实时与云端规则
  （MITRE T1562 记录的减弱防御类行为同源）。
  来源：https://attack.mitre.org/techniques/T1562/ 、
  https://redcanary.com/blog/threat-detection/better-know-a-data-source/amsi

## 2. 国产三件套（360 / 火绒 / 腾讯管家）

- **360**：多引擎矩阵——QVM 人工智能引擎（机器学习判定，专利文档可查其黑白名单
  + 特征自动学习闭环）、云查杀引擎、主防（行为防御）与"核晶"内核防护；实测经验
  （TideSec《远控免杀从入门到实践》系列）表明其静态特征串与云查杀上报敏感，
  纯本地静态变形在断网/云未收录时通过率高，敏感 API 组合（远程线程/写内存/加载
  shellcode）易触发主防。
  来源：https://patents.google.com/patent/CN105468975B/zh 、
  https://github.com/TideSec/BypassAntiVirus
- **火绒**：主打静态特征串 + 行为防御（系统级 Hook 驱动做行为拦截），特色是
  自定义规则/剑（分析工具）生态；实测中静态+动态均偏严，特征串命中即报；其内核
  Hook 驱动（hr 前缀）偶发兼容性蓝屏（论坛案例），说明其防护深度在内核层。
  来源：https://www.huorong.cn 、
  https://bbs.huorong.cn/thread-149663-1-1.html 、
  https://github.com/TideSec/BypassAntiVirus
- **腾讯电脑管家**：自研 TAV 引擎（高效静态检测+多格式支持）+ 云查杀 + 哈勃沙箱
  虚拟执行；动态侧沙箱判定是主要干扰面。
  来源：https://guanjia.qq.com
- **共性与已知易触发点**：敏感 API 组合（iex/DownloadString、Win32 API 动态声明、
  base64 大块解码、socket 反连）、脚本混淆特征（高熵、异常符号密度）、特征码
  定位（污点检测/二分定位后改写，见 SegmentFault/腾讯云社区系列实操文的公开总结，
  收录于 TideSec 仓库）。国产 AV 对"云未收录的新编译产物"普遍弱于对"老特征串"。
  来源：https://github.com/TideSec/BypassAntiVirus

## 3. AMSI / ETW / 内核对抗技术谱系

- **AMSI bypass 谱系**：2016 Matt Graeber 反射置 `AmsiUtils.amsiInitFailed`
  标志位为起点 → 内存 patch `AmsiScanBuffer`/`AmsiOpenSession` 返回路径（返回
  E_INVALIDARG/AMSI_RESULT_CLEAN）→ CLR .rdata 字符串改写（让查找失败）→
  patchless 变体（硬件断点/hook 劫持/上下文破坏）。防御侧：检测内存完整性篡改、
  ETW 停发、CLR 字符串异常。该领域公开研究共识：bypass 技巧长期活跃（Sophos 2021
  综述、mdsec 2018 Adam Chester、pentestlaboratories 2021 分类、S3cur3Th1sSh1t
  手工修改综述）。
  来源：https://medium.com/@byte_St0rm/adventures-in-the-wonderful-world-of-amsi-25d235eb749c 、
  https://www.mdsec.co.uk/2018/06/exploring-powershell-amsi-and-logging-evasion/ 、
  https://www.sophos.com/en-us/blog/amsi-bypasses-remain-tricks-of-the-malware-trade 、
  https://pentestlaboratories.com/2021/05/17/amsi-bypass-methods/ 、
  https://s3cur3th1ssh1t.github.io/Bypass_AMSI_by_manual_modification/
- **ETW patch**：改写 `EtwEventWrite` 入口使其直接返回→进程级遥测静默；检测面：
  ETW 事件流中断、内存完整性。来源同上（WithSecure 演练、Binarly、SonicWall）。
- **unhooking（解除用户态 Hook）**：EDR 以 ntdll 导出函数入口写 E9 jmp trampoline
  （及 IAT/inline hook）监控；对抗是拿"干净 ntdll"覆盖 .text：磁盘读、\KnownDlls
  节映射（免磁盘 IO）、挂起进程快照三种公开来源。
  来源：https://www.binarly.io/blog/design-issues-modern-edrs-bypassing-etw 、
  https://www.ired.team/offensive-security/defense-evasion
- **direct / indirect syscall**：绕开 ntdll hook 直接在自产代码里执行 syscall 指令
  （SSN 动态解析：SysWhispers、Hell's Gate/Halo's Gate 谱系）；indirect 变体跳到
  干净 ntdll 的 `syscall; ret` gadget 返回，改善栈回溯观感。防御侧：内核回调/
  ETW Threat-Intelligence 仍可见，栈对齐检测是主流反制。
  来源：https://github.com/jthuraisamy/SysWhispers2 、
  https://github.com/jthuraisamy/SysWhispers3 、
  https://github.com/am0nsec/HellsGate 、
  https://www.rbtsec.com （API Hooking Techniques 系列文）
- **DLL 侧载（T1574.002）**：把恶意 DLL 放在合法签名 EXE 会加载的位置（利用其
  导入表/搜索顺序），借合法文件信誉；Cybereason 记录了规模化滥用，NVISO 紫队
  演练给出检测点（合法进程加载异常路径 DLL、签名 EXE 的 DLL 依赖被替换）。
  来源：https://attack.mitre.org/techniques/T1574/ 、
  https://www.cybereason.com/blog/dll-side-loading 、
  https://blog.nviso.eu

## 4. 加载器形态

- **sRDI**：把普通 DLL 包一层 PIC bootstrap（NetSPI 2017 发布，BSD），转成
  position-independent shellcode 后可经远程线程/回调执行，指定导出函数；
  与经典 RDI 区别=加载器在 shellcode 前置 stub 而非 DLL 导出表。本项目载荷矩阵
  的 shellcode 产物即此路线（bootstrap 常量注明开源出处）。
  来源：https://github.com/monoxgas/sRDI 、
  https://www.ired.team/offensive-security/code-injection-process-injection
- **进程挖空（Process Hollowing, T1055.012）**：挂起合法进程→卸载其映像→写入
  恶意 PE→改线程上下文恢复执行；检测面：父子进程映像不一致、RWX 区域、
  NtUnmapViewOfSection+SetThreadContext 序列。
  来源：https://www.ired.team/offensive-security/code-injection-process-injection/process-hollowing-and-pe-image-relocations 、
  https://attack.mitre.org/techniques/T1055/
- **回调源执行**：不直接调 CreateThread/LoadLibrary，而把载荷指针塞进各类合法
  回调枚举 API（字体枚举、证书存储枚举等公开 LOLBAS 条目族），绕开"可疑启动
  线程"类检测；LOLBAS 项目收录大量此类活体样本条目。
  来源：https://github.com/LOLBAS-Project/LOLBAS 、
  https://attack.mitre.org/techniques/T1106/
- **分阶段加载（stager/cradle）**：小体积 stager 从信道拉取 stage 后反射加载
  （本平台 staged shellcode 即此语义）；脚本侧 cradle（DownloadString+IEX）是
  最老也最被重点盯防的形态——字符串特征与行为链双重规则。
  来源：https://attack.mitre.org/techniques/T1106/ 、
  https://redcanary.com/blog/threat-detection/better-know-a-data-source/amsi
- **进程注入检测面（T1055 总览）**：注入族共性 API 序列与跨进程内存操作是 EDR
  行为规则的主力输入（ManageEngine/SOC 实验室综述）。
  来源：https://attack.mitre.org/techniques/T1055/ 、
  https://www.manageengine.com （in-memory malware detection 综述文）

## 5. Go 二进制免杀

- **默认编译特征**：Go 产物自带 `Go build ID`、`runtime.*` 字符串、完整函数符号
  与 pclntab、默认嵌入的 GOROOT/构建路径（`-trimpath`/`-ldflags "-s -w"` 可去掉
  调试符号与路径，但 build ID 与运行时特征仍在）；YARA 社区规则常以这些常量串
  识别 Go 编译物。高熵是另一面：Go 二进制普遍偏大、.text 大段特征明显。
  来源：https://github.com/burrowers/garble 、
  https://pkg.go.dev/cmd/link 、
  https://github.com/goretk/gore
- **garble 混淆**：包装 go 工具链做符号/路径哈希化、剥 build/debug 信息；
  `-literals` 把字符串字面量改写为运行时还原表达式；`-tiny` 再删 panic/trace
  信息（体积约 -15%）；导出方法名因接口需要不混淆（残留可识别面）；seed 可复现
  ——不轮换 seed 时同类产物可被聚类。
  来源：https://github.com/burrowers/garble
- **对抗研究（防御视角：混淆可逆性）**：Mandiant GoStringUngarbler 自动还原
  garble 字符串；Volexity GoResolver 用控制流图相似度批量恢复函数名。结论：
  纯符号混淆对静态规则有效、对熟练分析者只加时间成本。
  来源：https://cloud.google.com/blog/topics/threat-intelligence/gostringungarbler-deobfuscating-strings-in-garbled-binaries 、
  https://www.volexity.com/blog/2025/04/01/goresolver-using-control-flow-graph-similarity-to-deobfuscate-golang-binaries-automatically
- **签名伪造（Authenticode cloning）**：把合法签名证书块+签名整体拷到恶意文件
  可骗过"只看有无签名/签名者"的弱校验（无私钥也行）；SpecterOps 经典研究，MITRE
  T1553.002 收录；防御=完整哈希校验。签名伪造属强 OPSEC 争议动作，KB 只沉淀原理
  与检测要点，平台不做任何签名能力。
  来源：https://posts.specterops.io/code-signing-certificate-cloning-attacks-and-defenses-6f98657fc6ec 、
  https://attack.mitre.org/techniques/T1553/
- **打包/熵通用面**：Python 侧同理——PyInstaller 打包物高熵+自带 loader 特征，
  是 AV 误报重灾区也是检测焦点；PyArmor(Pro) 主动反静态还原（Batloader 实战案例）。
  来源：https://github.com/extremecoders-re/pyinstxtractor 、
  https://pyarmor.dashingsoft.com

## 6. 脚本类混淆

- **Invoke-Obfuscation 谱系**：Bohannon 的框架把 PowerShell 混淆做成 token/
  string/encoding/launcher 分层组合（字符串拆分、格式符拼接、编码级联、运行时
  拼装执行）；MuddyWater 等实战组织长期使用（MITRE T1027 收录）。检测研究鼻祖
  Revoke-Obfuscation（Bohannon & Holmes, BHUSA2017）：用统计+ROUGE 判"混淆密度
  本身即高危信号"。
  来源：https://github.com/danielbohannon/Invoke-Obfuscation 、
  https://attack.mitre.org/techniques/T1027/
- **字符串拆分/编码层**：等价变换族——字符串按位拆分+join、ASCII 码还原、base64/
  hex/securestring 级联、format/占位符重排。与 AMSI 的博弈在于"提交扫描前已还原"
  还是"运行时才还原"：AMSI 抓的是宿主最终提交的内容，故纯静态变形对 AMSI 无效，
  但对"文件落盘静态扫描"（国产 AV 主力）依然有效。
  来源：https://www.mdsec.co.uk/2018/06/exploring-powershell-amsi-and-logging-evasion/ 、
  https://learn.microsoft.com/en-us/defender-endpoint/amsi-on-mdav
- **Python 打包面**：PyInstaller/PyArmor 见第 5 节；py 载荷的静态特征集中在
  exec/eval/urllib/socket/subprocess 等敏感符号与 import 形态。
  来源：https://github.com/extremecoders-re/pyinstxtractor

## 7. 对本平台静态自测口径的印证

平台 `server/c2_evasion.go` 的本地静态自测（特征串计数 + Shanon 熵 >5.5 标高）
与上述研究一致：特征串命中对应"已知敏感符号静态规则"，熵对应"编码/加密块启发"。
知识库条目（`c2center/evasion_kb.go`）按本笔记沉淀，变形提示词经内置档案
instructions 注入对应 AV 的摘要。

## 来源质量评估

- **官方文档（最高可信）**：learn.microsoft.com（AMSI/云查杀/BAFS/篡改保护/ASR）、
  MSRC（mpengine 攻击面）、pkg.go.dev/cmd/link。
- **厂商研究（高可信）**：Red Canary、Sophos、WithSecure、SonicWall、Binarly、
  MDSec、Mandiant(Google Cloud)、Volexity、Cybereason、SpecterOps、Trend Micro。
- **开源项目文档（中高可信，含一手实现）**：garble、sRDI、Invoke-Obfuscation、
  SysWhispers2/3、HellsGate、gore、pyinstxtractor、PyArmor、LOLBAS、TideSec/
  BypassAntiVirus。
- **社区/论坛（辅证）**：火绒论坛（驱动行为案例）、MITRE ATT&CK（技术分类锚点）、
  rbtsec/NVISO 等博客。
- **未采信**：任务书所述 "MinEU" 未找到对应公开组件（见第 1 节澄清）；只给域名
  无稳定路径的聚合结果一律不入 Refs。
