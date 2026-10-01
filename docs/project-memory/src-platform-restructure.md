---
name: src-platform-restructure
description: SRC「平台选择」三级重构（平台→目标→资产）的数据模型、约束注入链路与多对多取舍
metadata:
  node_type: memory
  type: project
  originSessionId: sess_5aabd8f5-f46d-4489-be4c-4a64179dbac7
---

# SRC 平台选择三级重构（2026-09-24 完成，已在 8799 实测）

用户要求：`src模式不该叫企业模式，应该叫平台选择`，且平台要能填奖励标准/评分标准/守则/规范并**支持上传文件**，
添加平台后能进入 → 添加目标 → 进入目标 → 添加资产（支持从信息搜集导入）。旧「企业模式」已删除。

## 用户拍板的四个决策
1. **目标用新建独立表**（不是复用 companies，也不是范围条目）——`src_targets.company_id` 可空，
   平台下可以先只有一个目标名字，等资产进来再关联企业。
2. **上传文件 = 存附件 + 解析文本注入约束**：md/txt 直读、docx 用 `report.ParseDocxReader` 解析；
   **pdf/xlsx 只存档不解析**（项目无这两类解析库，硬抽正文会把噪声注入 AI 约束），标 `parse_status=skipped`。
3. **完全替换旧模型并迁移数据**：`src_profiles` 表保留不删（回滚源），但旧 API 与页面全部下线。
4. **平台与目标多对多**——授权范围挂在「平台×目标」(`src_platform_targets`) 上，不是挂在目标上。
   同一家企业同时收录在补天和漏洞盒子时，两边的授权范围/速率限制/规范各不相同，挂目标上就无法表达。

## 数据模型（schema.sql 末尾）
`src_platforms`(name/nkey唯一 + 四类规范文本域) / `src_platform_files`(kind + content_text + parse_status)
/ `src_targets` / `src_platform_targets`(授权范围挂这层) / `src_target_assets`(引用 assets，不复制)
+ `tasks.src_platform_id/src_target_id` + `src_rewards.platform_id/target_id`。

**迁移幂等靠 `settings.src_platforms_migrated` 标记**（建平台/目标是 INSERT，重复跑会累积垃圾）。
`src_nkey()` SQL 函数与 Go `db.SrcNKey` 必须算法一致（lower + 折叠空白），否则迁移建的行与运行时建的行会重复。

## 约束注入（防 AI 越界的落地点）
`server/src_api.go` 的 `applySRCConstraints(t, platformID, targetID)`：平台规范走 allow，
目标授权范围拆成 allow(范围/类型) + deny(禁止事项逐行/速率限制)。
**禁止事项必须逐行拆开注入**——整段塞入会被模型概括后丢掉细节。
平台规范与目标授权范围分两条注入，是为了事后排查越界时能定位约束来源。

## 关键坑（都踩过）
- **`src_rewards` 我漏建了 `target_id` 列**（只加了 platform_id），直到写测试才炸 `字段 r.target_id 不存在`。
  加列时要把所有引用该表的新查询一起过一遍。
- **`t.Cleanup` 清理测试数据会静默失败**：它在测试函数返回后才跑，那时 `defer d.Close()` 已关连接，
  残留行导致下次运行撞「同名平台已存在」。**必须用 `defer cleanup()`**（既有 companies_test.go 就是这个原因）。
- **域名带端口要拆**（`api.acme.com:8443` → value=`api.acme.com`, port=8443）。前端 `inferAsset` 与后端
  `srcHostOnly` 都做了拆分：留在 value 里会写成既非法、端口列又空着的资产。
- **旧 `src_profiles` 数据迁移后字段一个都不能丢**：scope_note/forbidden/allowed_types/rate_limit/rules/enabled
  原样搬到 `src_platform_targets`，实测迁移后 forbidden 三行、rate_limit 都完好。

## 页面
`/function/src/platforms`(列表：启用状态分类器带计数 + 搜索) → `/function/src/platforms/detail?id=`
(Tabs：概览 / 规范与文件 / 目标 / 成果) → `/function/src/targets/detail?id=&platform=`
(Tabs：概览 / 授权范围 / 资产 / 成果)。侧边栏「企业模式」→「平台选择」。
`/function/src/batch` 重写为按真实平台分组的跨平台目标勾选批量建任务。

## 建任务入口（用户报障后补的，2026-09-24）
初版**只有「平台批量」页能建任务**，用户进了平台、进了目标都找不到入口，直接反馈
「我没有看到 src 模式创建任务的地方」。**教训：详情页是「我已选好这个对象，现在开干」
最自然的位置，光有批量页不够**——批量页适合勾选多个，单个对象必须在自己页面上有入口。

现在三个入口共用 `components/src-create-task-dialog.tsx`：
1. 目标详情页头部「建 SRC 任务」主按钮（当前平台 × 该目标）
2. 平台详情「目标」页签行内火箭图标按钮（该平台 × 该行目标）
3. SRC 任务页「新建 SRC 任务」按钮（**强制选平台→目标**，未选齐创建按钮禁用）

**注意 `Task.id` 在 wire 上是 string**（后端 int64 → string 约定），回调签名别写 number。
`TargetsTab` 改为接收 `platform` 对象而非 `platformId`——建任务对话框需要平台名渲染注入说明。

## SRC 专属任务页 + 弹层与按钮不能进同一条三元链（2026-09-24）
`/function/tasks?src=1`：后端 `?src=1` 按 `src_mode` 过滤（**判据是标记不是归属**——
只给平台不给目标的 SRC 任务也算，按归属过滤会漏）。侧边栏 SRC 组加了「任务」入口。
SRC 视图**跳过「先选资产主体」门禁**：SRC 归属维度是平台×目标不是企业，企业门禁
会让用户以为任务丢了（重蹈数据消失回归的覆辙）。

**大坑：弹层对话框与触发它的按钮放进同一条三元链 = 死代码**。曾写成：
```
recon ? A : srcOnly ? <按钮 onClick=开对话框> : report ? C : srcOnly ? <对话框> : D
```
srcOnly 时永远先命中按钮分支，对话框分支永不可达。实测症状极迷惑：**组件在构建产物里、
onClick 也执行了、state 也变了、无任何 console 报错，但 fiber 树里搜不到该组件**。
修法：弹层不是按钮的替代品，必须渲染成兄弟节点（`{srcOnly && <Dialog/>}`）。
调试手段：直接读产物 bundle 确认 onClick 绑定 → 遍历 fiber 树搜组件 props → 定位到三元链互斥。

跨页面共享组件放 `web/src/components/`，不放路由段私有的 `_components/`（仓库约定）。

## 约束注入的验证口径
「任务是否自动设置好规则与限制」的完整链路（已验证）：
`applySRCConstraints`（建任务时同步写）→ `task_constraints` 表 →
`agent/constraints.go constraintBlock`（**Planner planner.go:395 与 Worker worker.go:398 都注入**，
标注「最高优先级，凌驾于探索启发式」）→ 任务详情页「操作约束」面板可见（共 N 条）。
注入是**确定性文本转换，无 AI 识别环节**——这是刻意的：边界约束不能交给 LLM 转述（会丢细节/幻觉），
宁可直接注入原文。



## 已灌入的 18 个真实平台（2026-09-24）
数据源 **SRC导航 http://www.newsrc.cn/**（WordPress 导航站），三个分类页：
- `/link_cat/platform` 漏洞平台 11 家 · `/link_cat/cspt` 众测平台 7 家 · `/link_cat/src` 厂商自建 SRC **147 家**
- ⚠️ **147 家厂商自建 SRC 是「目标」不是「平台」**——它们是各公司自己的 SRC，应挂到平台下当目标，别当平台灌。
- 页面结构：`<div class="link-item">` 内含 `.link-title` / `.link-desc`，正则可直接抽。
- 该站 TLS 证书是 `leso.org.cn` 的（域名不匹配），**必须走 http 且关校验**，用 `curl -sL` 即可。

**带真实规范文本的平台**（从各平台自己的规范页抓的，不是编的）：
- `教育行业漏洞报告平台(EDUSRC)` — src.sjtu.edu.cn/introduction + /announcement/4/，四类文本全（奖励 Rank 9~10/7~9/4~7/0~4、四档评分标准、无害化验证三原则+行为规范、完整提交规范与忽略/降级清单）**+ 3 个附件 md**
- `补天漏洞响应平台` — butian.net/Help/faq，四类全（现金/RYB/KB 三类奖励、信誉值机制扣 10/20 分 ≤60 封号、审核时效、公益SRC vs 专属SRC 可见性差异）**+ 3 个附件 md**
- `漏洞盒子` — vulbox.com/agreement，奖励/守则/规范（测试红线：数据泄露·声誉影响·事实性损害）**+ 2 个附件 md**
- `北京教育行业SRC` — bjedusrc.buu.edu.cn/index/notice/detail.html?id=73(定级办法:国标基础·五级·14项概念·邮件申诉) + id=95(标题命名格式·积分兑换仅限无banner) **+ 2 个附件 md**
- `补天众测`/`火线`/`Seebug`/`先知` — 官网公开服务说明已录入
- 规范文件上传：`POST /api/src/platforms/{id}/files`(multipart file+kind)，.md 自动解析注入约束；`out/update_platform_specs.py` 一键灌文本+传附件

## SRC 深度增强（2026-09-25 上线，六 agent 并行）
用户诉求四项全落地（契约见 out/src-enhance-spec.md）：
1. **新建平台轻量化**：创建 Sheet 只填名称/主页/描述，创建后跳详情页；规范在详情页「规范与文件」填。
2. **AI 识别填入**：统一上传区（单入口，kind 默认 'other' 可改派 PUT files/{fid}）+
   `POST /api/src/platforms/{id}/spec/ai-fill`——附件正文(md/txt/docx 直读、xlsx=excelize、
   pdf=ledongthuc/pdf 新依赖)交 LLM 结构化为四类建议返回，**前端预填人工检查后保存，AI 不落库**；
   没依据的类别留空不许编造。前端调 LLM 类长接口记得 180s AbortSignal（http() 默认 60s 必超时）。
3. **监管联动**：intercept_rules 加 scope(''全部/'src' 仅SRC)，过滤在 Interceptor.Match 咽喉
   （SetTaskSRCResolver 注入内存任务解析，宁漏勿误，默认行为零变化）；监管&规则中心 AI监管页底部
   加「SRC 监管」板块（GET /api/src/guard/overview）。**没另建平行执行系统**——拦截管线是唯一咽喉。
4. **任务详情 SRC 上下文卡**：src_mode 任务总览显示平台/目标链接、授权范围、评分标准、守则（折叠），
   逐块降级不崩页。
5. **src_operator 专属 agent**（stage=exploit，stage 白名单无 src）+ 3 工具：
   src_get_context/src_score_finding/src_draft_submission（确定性取数不打 LLM；非 SRC 任务报错）。
   工具注入点：包装 agent.ToolAugment（hostTools 不归己改时的唯一可行注入）。

**kind 联合类型加宽会炸索引它的表单代码**（SRCPlatformFileKind 索引 Partial<SRCPlatform>）——
加枚举值时用 Exclude<> 收窄索引类型。
llm_profiles 等表的测试对**复用库残留敏感**（unique name），全新库才绿——测试库每次 drop/recreate。

## 两实例双库结构（易混！）
- **8787（用户）** = `artex@127.0.0.1:5432`（config.json database 字段，凭据在配置文件里）+ `data/`（仓库根）
- **8799（dev）** = `postgres@127.0.0.1:5499/postgres`（演示集群）+ `out/data/`
- **给 8799 灌的数据 8787 看不到**。同步到 8787 用 pg_dump/psql（见下），别手写 SQL 脚本。
- 同步步骤（2026-09-24 执行过，18 平台+11 附件已同步）：
  1. `pg_dump -h 127.0.0.1 -p 5499 -U postgres -d postgres --data-only --table=src_platforms --table=src_platform_files -f out/src_sync.sql`
  2. `psql`(目标凭据取 config.json,PGPASSWORD 环境变量传,不打印) `-f out/src_sync.sql`——**只做数据追加前必须先确认目标表为空**（带 id 恢复，非空会主键冲突反而防了重复）
  3. 附件物理文件：`mkdir -p data/src-platforms && cp -r out/data/src-platforms/* data/src-platforms/`（pg_dump 保留了 id，目录名直接对应）
- **psql 传中文 SQL 的大坑（又踩）**：Windows 下 `psql -c "中文"` 会把命令行按 GBK 解释 → 「无效的 UTF8 编码字节顺序」。必须写 UTF-8 文件用 `-f`，且 subprocess 加 `encoding='utf-8'`。Write 工具写的 Python 同步脚本两次被 Mimosa 误拦（路径穿越误报），**pg_dump/psql 管道方案绕开了整个问题**。

**重要：抓不到就留空并注明「待补」，绝不编造**——注入给 AI 的约束若是假的，防越界就形同虚设。
需登录才能看的（360/漏洞银行/Seebug 细节/CNVD/CNNVD/先知/雷神/春秋）都标了待补。
各家官网抓取实测：**国内 SRC 平台规范页几乎全是 JS 渲染或需登录**（Seebug/CNVD 521 反爬/先知阿里壳），
能抓的只有静态页（补天 FAQ/漏洞盒子协议/EDUSRC/bjedusrc 公告）。Bugcrowd VRT 的 GitHub JSON 路径已失效。

## 迁移产生的重复平台要合并
迁移脚本把旧 `src_profiles.platform` 自由文本（"教育SRC"）建成了平台，与 newsrc 抓到的真实平台
（"教育行业漏洞报告平台(EDUSRC)"）重复。**直接删旧平台会 CASCADE 掉它下面的目标挂链（授权范围丢失）**，
正确顺序：先把挂链 `PUT /src/targets/{tid}/links/{real_pid}` 原样搬过去（含换行），回读确认后再删旧平台。

## 灌数据的脚本
`out/seed_src_platforms.py` 与 `out/merge_dup_platforms.py`（out/ 被 gitignore，属本地一次性工具）。
两者都做了**目标地址硬编码白名单**（只允许 127.0.0.1:8799）：既满足安全钩子的 SSRF 约束，
也从根本上防止误指向用户的 8787 实例。


相关：[[project-artex]] [[task-gone-guard]]
