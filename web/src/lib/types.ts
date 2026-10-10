// ARTEX domain model — types used across the UI.
// Derived from the functional spec (section 7: 关键数据形状).

export type TaskStatus = "created" | "queued" | "running" | "paused" | "done" | "failed" | "timeout";
export type TaskStage = "recon" | "exploit" | "post" | "report";
export type EngineMode = "exploring" | "paused" | "stalled" | "idle";

export interface Task {
  id: string;
  name?: string; // 可选任务名称;空/缺省=未命名,展示时回退到描述
  category_id?: number;
  category_name?: string;
  pinned?: boolean;
  pinned_at?: string | null;
  description: string;
  goal: string;
  status: TaskStatus;
  stage?: TaskStage; // 三阶段平台;缺省=exploit
  kind?: "agent" | "pipeline"; // agent=现有引擎;pipeline=信息搜集确定性步骤任务
  step_key?: string; // recon 单步骤任务:收集器 key(手动录入/证书透明日志/…)
  src_mode?: boolean; // SRC 模式:产出计入 SRC 成果,并按平台/目标规范约束 AI 边界
  /** 任务归属的 SRC 平台(新三级结构);旧任务的 src_profile_id 仅作历史回显 */
  src_platform_id?: number;
  /** 任务归属的 SRC 目标(平台×目标) */
  src_target_id?: number;
  /** 出网方式(任务级覆盖):""=跟随全局 / "direct"=强制直连 / "pool"=强制走代理池 */
  proxy_mode?: string;
  /** 仅 pool 时有效:指定用哪个代理组;0/缺省=用全局选中的组 */
  proxy_group_id?: number;
  /** 实际生效的出网地址(后端回显,便于确认任务真的走了代理池);空=直连 */
  proxy_egress?: string;
  /** 监管门限(任务级覆盖):""=跟随全局 / "off"=本任务不跑监管 / "enforce"=强制启用监管 */
  guard_mode?: string;
  /** 仅本任务启用这些拦截规则 id;空数组=用全部已启用规则 */
  guard_rule_ids?: number[];
  step_config?: Record<string, unknown>; // 步骤配置
  created_at: string;
  created_unix?: number; // created_at as unix seconds (run-duration calc)
  completed_at?: string; // RFC3339 finish time (done/failed); "" if unfinished
  completed_unix?: number; // completed_at as unix seconds (0/undef if unfinished)
  last_activity_unix?: number; // unix seconds of the last activity (0/undef if none)
  paused?: boolean;
  queued?: boolean;
  active?: boolean;
  in_flight?: number;
  last_activity?: string;
  stalled?: boolean;
  goals_total?: number;
  goals_met?: number;
  engine_mode?: EngineMode;
  tokens?: TokenTotal; // whole-task token consumption
  llm_profile_id?: number; // LLM profile used; absent = default profile
  llm_profile_ids?: number[]; // ordered task-level failover chain
  active_llm_profile_id?: number; // profile used by the next LLM call
  llm_failover_state?: "default" | "ready" | "chain_exhausted" | string;
  llm_failover_reason?: string;
  source_task_ids?: string[]; // directly related tasks inherited as read-only context
  archive_blocked_by_task_id?: string; // live direct dependent that must be archived first
  company_ids?: number[]; // associated company scopes; current company assets join the task at creation
  coverage_enabled?: boolean; // 资产覆盖度功能开关(创建时定,默认开)；false=不计算/不展示覆盖度
  /** CVE 情报简报联动(任务级三态):null/缺省=跟随全局(settings vulndb_task_brief);true/false=强制开/关 */
  vuln_brief?: boolean | null;
  /** 奖励标准(创建时 AI 识别拆分或人工填写,创建后不可改);空=无;任务详情总览有内容才渲染卡片 */
  reward_note?: string;
}

// ---- 三阶段平台:信息搜集 ----

export interface ReconCollectorDesc {
  key: string;
  title: string;
  kind: "api" | "tool" | "manual" | string;
}

// 信息搜集的自动检测计划:哪些数据源会跑、query 从哪来、哪些因未配置跳过。
export interface ReconSourcePlan {
  provider: string;
  title: string;
  kind: "search" | "domain_enum" | string;
  external_api?: number;
  api_name?: string;
  domain?: string;
  query?: string;
  query_source?: "ai" | "default" | "manual" | string;
  skip_reason?: string;
}

export interface ReconPlan {
  sources: ReconSourcePlan[];
  domains: string[];
  available: number;
  skipped: number;
  ai_used: boolean;
  ai_note?: string;
  companies: number[];
  target_name?: string;
}

export interface ReconPipelineStep {
  key: string;
  enabled?: boolean; // 缺省=true
  config?: Record<string, unknown>;
}

export interface ReconPipeline {
  collectors: ReconPipelineStep[];
}

export interface CollectorRun {
  id: number;
  task_id: number;
  collector: string;
  position: number;
  status: "running" | "done" | "failed" | "skipped" | string;
  stats?: Record<string, unknown>;
  error: string;
  raw_evidence_hash: string;
  started_at: string;
  ended_at?: string | null;
}

export interface StagedAsset {
  id: number;
  task_id: number;
  run_id?: number;
  company_id?: number;
  collector: string;
  type: "root_domain" | "domain" | "ip" | "service" | "app" | string;
  value: string;
  port: number;
  extra?: Record<string, unknown>;
  status: "pending" | "accepted" | "ignored" | string;
  batch_id: number;
  created_at: string;
  decided_at?: string | null;
}

export interface StagedAssetCounts {
  pending: number;
  accepted: number;
  ignored: number;
}

export interface StagedDecideResult {
  action: "adopt" | "ignore" | string;
  results: { id: number; asset_id?: number; status: string }[];
  errors: { id: number; value: string; error: string }[];
  counts: StagedAssetCounts;
  remaining_pending: number;
}

// ---- 任务资产识别入库（契约见 ASSET_EXTRACT_CONTRACTS.md） ----

// 识别轮（1:1 task）：确定性提取候选进暂存，LLM 确认的直接采纳入库。
export interface AssetExtractRun {
  id: number;
  task_id: number;
  status: "pending" | "running" | "done" | "partial" | "failed" | string;
  deterministic_count: number;
  ai_confirmed_count: number;
  ai_new_count: number;
  adopted_count: number;
  error: string;
  created_at: string;
  updated_at: string;
}

export interface AssetExtractConfig {
  ai_enabled: boolean;
}

// 手动归档单条资产的载荷：字段名与 POST /api/assets 内层 assets[] 一致，
// 按类型选填（root_domain/subdomain→domain；ip→ip；service 有 url 走 HTTP 服务，
// 否则 host/ip + port + service_name 走端口服务；app→app_name）。
export interface TaskAssetUpsert {
  type: NewAssetType;
  domain?: string;
  icp?: string;
  record_type?: string;
  record_value?: string[];
  ip?: string;
  bound_domains?: string[];
  open_ports?: { port: number; service?: string }[];
  app_name?: string;
  bundle_id?: string;
  category?: string;
  description?: string;
  url?: string;
  technologies?: string[];
  page_title?: string;
  service_ip?: string;
  port?: number;
  service_name?: string;
}

export interface ArchiveAssetResult {
  results: { index: number; id: number; type: string }[];
  errors: { index: number; error: string }[];
}


// ---- 资产监控（24h 持续子域测绘，契约见 MONITOR_CONTRACTS.md） ----

export interface MonitorCollectorStat {
  key: string;
  status: string;
  found: number;
  error?: string;
}

export interface MonitorNewAsset {
  type: string;
  value: string;
}

export interface ReconMonitorConfig {
  enabled: boolean;
  interval_hours: number;
  running: boolean;
  last_round_at?: string | null;
  /** 监控独立出网: ""=跟随全局 / direct=强制直连 / pool=代理池 */
  proxy_mode: string;
  /** pool 模式指定的代理组 id;0=跟随全局组选择 */
  proxy_group_id: number;
}

export interface ReconMonitorTarget {
  company_id: number;
  company_name: string;
  domain_count: number;
  domains: string[];
  watched: boolean;
  enabled: boolean;
  interval_hours: number | null;
  last_run_at?: string | null;
  next_run_at?: string | null;
  last_run_id?: number | null;
  last_run?: { status: string; new_count: number; finished_at?: string | null } | null;
}

export interface ReconMonitorOverview {
  config: ReconMonitorConfig;
  targets: ReconMonitorTarget[];
}

export interface ReconMonitorRun {
  id: number;
  company_id?: number | null;
  company_name: string;
  trigger: string;
  status: string;
  collectors: MonitorCollectorStat[];
  total_found: number;
  new_count: number;
  new_domains?: MonitorNewAsset[];
  new_truncated?: boolean;
  probe_ok: number;
  probe_failed: number;
  error: string;
  started_at: string;
  finished_at?: string | null;
}

// ---- 漏洞库(0day / nday / CVE + PoC 管理) ----

export type VulnCategory = "0day" | "nday" | "cve";

export interface VulnEntry {
  id: number;
  category: VulnCategory | string;
  title: string;
  cve_id?: string;
  vendor: string;
  product: string;
  affected: string;
  severity: string;
  vuln_class: string;
  description?: string;
  exploitation?: string;
  remediation?: string;
  source: "manual" | "ai_finding" | "cve_feed" | string;
  task_id?: number;
  finding_id?: number;
  refs: string[];
  tags: string[];
  poc_count: number;
  // 在野利用标记(CISA KEV 已知被在野利用目录,采集器维护)。
  kev?: boolean;
  status: string;
  created_at: string;
  updated_at: string;
}

export interface VulnPoc {
  id: number;
  entry_id: number;
  name: string;
  kind: "poc" | "exp" | "nuclei" | "script" | string;
  language: string;
  content?: string;
  source: "manual" | "github" | "exploitdb" | "pocorexp" | "nuclei" | "trickest" | "ai_generated" | string;
  source_url: string;
  verified: boolean;
  // PoC 审核软质检层(POC_INTEL_CONTRACTS):自动源=pending,LLM/人工打标。
  review_status: "pending" | "approved" | "rejected" | string;
  review_note: string; // 审核理由(LLM 或人工);空=未写
  reviewed_at?: string;
  created_at: string;
  updated_at: string;
}

// 任务 CVE 情报简报(GET /api/tasks/{id}/vuln-brief,按任务资产指纹自动匹配库内漏洞)。
export interface VulnFingerprintHit {
  term: string;
  hits: number; // 该指纹词在库内的带 PoC 命中数(0=无命中,前端灰显)
}

export interface VulnBriefDTO {
  task_id: string;
  enabled: boolean; // 本任务是否启用联动(任务级三态 or 全局开关)
  generated_at: string;
  fingerprints: VulnFingerprintHit[]; // 按命中数降序,最多 24 个
  counts: { kev: number; high: number; rest: number; total: number }; // 分类页签计数
  entries: VulnEntry[]; // 全量命中(上限 30,兼容保留);分页列表走 /vuln-brief/entries
}

// 任务简报「匹配漏洞」分页列表(GET /api/tasks/{id}/vuln-brief/entries)。
export interface VulnBriefEntriesDTO {
  entries: VulnEntry[];
  total: number;
}

// PoC 审核三态计数(GET /api/vulndb/review/stats)。
export interface PocReviewStats {
  pending: number;
  approved: number;
  rejected: number;
}

// PoC 历史回填作业状态(nuclei/trickest 共形)。
export interface PocBackfillStatus {
  running: boolean;
  done: boolean;
  processed: number;
  matched: number;
  error: string;
  started_at: string;
  finished_at: string;
}

export interface VulnSyncRun {
  id: number;
  source: string;
  status: string;
  fetched: number;
  inserted: number;
  updated: number;
  pocs_found: number;
  error: string;
  started_at: string;
  finished_at?: string;
}

// ---- SRC 平台选择 ----
//
// 三级结构:平台 → 目标 → 资产。平台是顶层实体(SRC 提交平台,如补天/漏洞盒子),
// 目标是被测试的对象(厂商/学校),资产是目标下的具体资产。
// 平台与目标是多对多:同一家企业可能同时收录在多个平台,各平台下的授权范围与
// 规范各不相同,所以授权范围字段挂在「平台×目标」关联(src_platform_targets)上。

/** 平台级规范文件的四类用途,与后端 src_platform_files.kind 同取值。 */
export type SRCPlatformFileKind =
  | "reward_standard"
  | "scoring_standard"
  | "code_of_conduct"
  | "rules"
  | "other"; // other=统一上传入口先收文件,AI 识别/手动改派后再归类

export const SRC_PLATFORM_FILE_KINDS: { value: SRCPlatformFileKind; label: string }[] = [
  { value: "reward_standard", label: "奖励标准" },
  { value: "scoring_standard", label: "评分标准" },
  { value: "code_of_conduct", label: "守则" },
  { value: "rules", label: "规范" },
];

export interface SRCPlatform {
  id: number;
  name: string;
  nkey?: string;
  homepage: string;
  description: string;
  reward_standard: string;
  scoring_standard: string;
  code_of_conduct: string;
  rules: string;
  enabled: boolean;
  // 统计(列表页与详情页概览用;后端聚合,避免前端逐个拉列表)
  target_count?: number;
  asset_count?: number;
  file_count?: number;
  reward_count?: number;
  created_at: string;
  updated_at: string;
}

/** 平台规范附件。content_text 是解析出的正文,会与四类文本域一起注入 AI 约束。 */
export interface SRCPlatformFile {
  id: number;
  platform_id: number;
  kind: SRCPlatformFileKind | string;
  filename: string;
  byte_size: number;
  content_text: string;
  // parsed=已解析出正文并注入约束 / skipped=格式不支持,仅存档 / failed=解析报错
  parse_status: "parsed" | "skipped" | "failed" | string;
  parse_note: string;
  created_at: string;
}

/** 目标:被测试的对象。company_id 可空——平台下可以先只有名字,等资产进来再关联企业。 */
export interface SRCTarget {
  id: number;
  name: string;
  company_id?: number;
  company_name?: string;
  description: string;
  enabled: boolean;
  // 统计
  platform_count?: number;
  asset_count?: number;
  reward_count?: number;
  created_at: string;
  updated_at: string;
}

/** 平台×目标关联:授权范围与规范挂这一层(同一目标在不同平台下规范不同)。 */
export interface SRCTargetLink {
  id: number;
  platform_id: number;
  platform_name?: string;
  target_id: number;
  target_name?: string;
  scope_note: string;
  forbidden: string;
  allowed_types: string;
  rate_limit: string;
  rules: string;
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

/** 目标资产:资产本体仍在 assets 表,这里只记「该平台该目标纳入了哪些资产」。 */
export interface SRCTargetAsset {
  id: number;
  platform_id: number;
  target_id: number;
  asset_id: number;
  source: "manual" | "recon" | "existing" | string;
  note: string;
  created_at: string;
  // 联表带出的资产详情(展示用)
  type?: string;
  value?: string;
  port?: number;
  company_id?: number;
}

/** 从信息搜集/已有资产导入时的候选来源。 */
export interface SRCTargetImportSource {
  // staged=信息搜集暂存区待采纳 / asset=资产图里已有的资产
  kind: "staged" | "asset" | string;
  id: number;
  type: string;
  value: string;
  port?: number;
  // staged 专用:所属任务与产出收集器
  task_id?: number;
  task_name?: string;
  collector?: string;
  // 是否已在该目标下(前端置灰已导入项)
  imported?: boolean;
}

export interface SRCReward {
  id: number;
  company_id?: number;
  company_name?: string;
  platform_id?: number;
  platform_name?: string;
  target_id?: number;
  task_id?: number;
  finding_id?: number;
  entry_id?: number;
  // 该漏洞在报告中心已归档的漏洞报告 id;agent 还没写正文(未归档)时没有
  report_id?: number;
  title: string;
  severity: string;
  vuln_class: string;
  status: "pending" | "submitted" | "accepted" | "rejected" | "duplicate" | string;
  platform_ref: string;
  feedback: string;
  submitted_at?: string;
  created_at: string;
  updated_at: string;
}

export interface SRCOverview {
  total: number;
  by_status: Record<string, number>;
  by_company: { company_id: number; company_name: string; total: number; accepted: number }[];
  // 平台维度聚合。可选:旧后端不返回该字段时前端按空数组处理。
  by_platform?: { platform_id: number; platform_name: string; total: number; accepted: number }[];
}

// ---- 报告中心 ----

export interface ReportSummary {
  id: number;
  kind: "task" | "finding" | "custom" | string;
  title: string;
  summary: string;
  task_id?: number;
  finding_id?: number;
  source: string;
  created_at: string;
  updated_at: string;
}

export interface ReportDetail extends ReportSummary {
  content: string;
}

// 报告中心的「先选企业」列表项:企业名下的报告数与分类细分。
export interface ReportCompanyCount {
  company_id: number;
  name: string;
  total: number;
  task: number;
  finding: number;
  custom: number;
}

// ---- 报告模板(上传 docx/md → 按模板导出报告) ----

export interface ReportTemplateHeading {
  level: number;
  title: string;
}

export interface ReportTemplateStructure {
  headings?: ReportTemplateHeading[];
  paragraphs?: number;
  tables?: number;
  words?: number;
}

export interface ReportTemplate {
  id: number;
  name: string;
  category?: string; // 分类(渗透测试报告/漏扫报告/通用模板等)
  format: "markdown" | "docx" | "doc" | string;
  content?: string; // 仅详情返回
  structure?: ReportTemplateStructure;
  source_note?: string;
  // AI 占位模板识别(异步任务):状态/失败原因 + 监看任务与会话 id(跳转过程用)
  analyze_status?: "" | "running" | "done" | "failed" | string;
  analyze_error?: string;
  suggested_content?: string; // AI 草稿(仅详情返回,由用户确认采用)
  // 导出写作指令:与占位模板配套,AI 按模板导出时注入 LLM(识别产出/手工编辑,仅详情返回)
  export_prompt?: string;
  analyze_task_id?: number;
  analyze_conv_id?: number;
  analyzed_at?: string;
  created_at: string;
}

// 报告生成作业:走 LLM 的报告产出(AI 按模板导出 / 任务报告 AI 撰写)的统一进度实体。
// monitor_task_id/conv_id 指向报告中心的监看任务与会话(过程可回看)。
export interface ReportJob {
  id: number;
  kind: "export" | "task_report_ai" | string;
  report_id: number;
  template_id?: number;
  source_task_id?: number;
  monitor_task_id?: number;
  conv_id?: number;
  format?: string; // export: md | docx
  status: "running" | "done" | "failed" | string;
  error?: string;
  created_at: string;
  updated_at: string;
}

// ---- 报告生成过程事件流 ----

export interface ReportEvent {
  id: number;
  report_id?: number;
  template_id?: number;
  // ai_enhanced/ai_enhance_failed:报告 agent 重写任务总结报告(成功/失败)
  kind:
    | "created"
    | "archived"
    | "updated"
    | "exported"
    | "uploaded"
    | "deleted"
    | "ai_enhanced"
    | "ai_enhance_failed"
    | string;
  actor: "user" | "agent" | "system" | string;
  task_id?: number;
  detail?: Record<string, unknown>;
  created_at: string;
}

// ---- 决策引擎(AI监管前置决策模型) ----

export interface DecisionEngine {
  key: string;
  title: string;
  description: string;
  enabled: boolean;
  api_key_set: boolean;
  base_url: string;
  model: string;
  default_base_url: string;
  default_model: string;
  endpoint_path?: string; // 已接入引擎的协议路径:配置填根地址,运行时自动拼接
  available: boolean; // 协议已接入=false 仅可配置预留
}

// JEV 门限阈值。取向是"宁可交给下一层,不要误拦":JEV 只做前置快筛,误拦会直接
// 中断整个渗透任务,而放过去的调用后面还有监管模型与人工两层兜底。
export interface JevThresholds {
  /** 判 block 时至少要有的置信度,低于它视为未裁决(交给下一层)。 */
  block_confidence: number;
  /** 判 allow 时至少要有的置信度,低于它视为未裁决。 */
  allow_confidence: number;
  /** 单个危险信号超过它才算"确凿",才允许硬拦。 */
  danger_score: number;
  /** 是否允许「提权/绕认证」信号单独触发硬拦截。默认关:授权渗透里提权是被授权的核心动作。 */
  privilege_blocks: boolean;
  /** 置信度不足时的动作:""=交给下一层 / "flag"=判疑放通 / "ask"=转人工 / "allow"=放行。 */
  low_confidence_action: "" | "flag" | "ask" | "allow";
}

export interface JevThresholdsResponse extends JevThresholds {
  defaults: JevThresholds;
}

// ---- 外部接口(空间测绘 API 配置) ----

export interface ExternalAPICredField {
  key: string;
  label: string;
  secret: boolean;
}

export interface ExternalAPIProviderDesc {
  key: string;
  title: string;
  default_base: string;
  cred_fields: ExternalAPICredField[];
  kind?: "search" | "domain_enum" | string; // search=查询语句型;domain_enum=域名枚举型
}

export interface ExternalAPIConfig {
  id: number;
  provider: string;
  provider_title: string;
  name: string;
  base_url: string;
  enabled: boolean;
  last_test_at?: string;
  last_test_ok?: boolean | null;
  quota_info?: Record<string, unknown>;
  masked?: Record<string, string>;
}

export interface TaskCategory {
  id: number;
  name: string;
  task_count: number;
  created_at: string;
  updated_at: string;
}

export interface TaskTemplate {
  id: number;
  name: string;
  description: string;
  goal: string;
  created_at: string;
  updated_at: string;
}

export interface DeleteTaskOptions {
  delete_assets: boolean;
  delete_traffic: boolean;
  delete_files: boolean;
  delete_findings: boolean;
  delete_llm_records: boolean;
}

export interface DeleteTaskResult {
  deleted: string;
  assets_deleted: number;
  assets_detached: number;
  traffic_deleted: number;
  files_deleted: boolean;
  findings_deleted: number;
  llm_records_deleted: number;
  cleanup_warning?: string;
}

export type TaskArchiveState =
  | "archive_queued"
  | "archiving"
  | "archive_failed"
  | "ready"
  | "restore_queued"
  | "restoring"
  | "restore_failed"
  | "delete_queued"
  | "deleting"
  | "delete_failed";

export interface TaskArchiveTokenStats {
  calls?: number;
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
}

export interface TaskArchive {
  id: number;
  task_id: number;
  state: TaskArchiveState;
  phase: string;
  progress: number;
  error?: string;
  warnings?: string[];
  format_version: number;
  sha256?: string;
  original_size: number;
  compressed_size: number;
  task_name: string;
  task_description: string;
  task_goal: string;
  original_status: TaskStatus;
  category_id?: number;
  category_name?: string;
  source_task_ids: number[];
  remaining_timeout_seconds: number;
  data_counts: Record<string, number>;
  aggregate_stats: {
    tokens?: TaskArchiveTokenStats;
    skills?: Record<string, number>;
    tools?: Record<string, number>;
    findings?: Record<string, number>;
  };
  archived_at?: string;
  requested_at: string;
  created_at: string;
  updated_at: string;
}

export interface TaskArchivePage {
  items: TaskArchive[];
  total: number;
  page: number;
  size: number;
}

export interface ArchiveBatchItem {
  id: string;
  archive_id?: number;
  ok: boolean;
  queued: boolean;
  error?: string;
}

// ---- Asset graph (global, shared across tasks) ----
export type AssetType =
  | "company"
  | "domain"
  | "ip"
  | "port"
  | "service"
  | "site"
  | "endpoint"
  | "parameter"
  | "tech"
  | "credential"
  | "data";

export type NodeState = "observed" | "confirmed" | "tombstoned";

export interface AssetNode {
  id: string;
  type: AssetType;
  name: string;
  key: string; // nkey
  value?: string;
  company_id?: string; // 归属公司资产 id；空=未归属
  state: NodeState;
  confidence: number; // 0..1
  attrs?: Record<string, unknown>;
  first_seen: string;
  last_seen: string;
}

export type AssetRel =
  | "owns"
  | "resolves"
  | "exposes"
  | "runs"
  | "serves"
  | "has_endpoint"
  | "has_param"
  | "fingerprinted"
  | "authenticates_as"
  | "reachable"
  | "has_subdomain";

export interface Edge {
  src: string;
  dst: string;
  rel: AssetRel | ExploreRel;
}

// Task asset view — server-side enriched, paginated.
export interface TaskAssetRef {
  id: string;
  name?: string;
  key: string;
  attrs?: Record<string, unknown>;
}

export interface TaskAssetItem extends AssetNode {
  techs?: TaskAssetRef[];
  auth?: TaskAssetRef[];
  params?: TaskAssetRef[];
}

export interface TaskAssetView {
  counts: Record<string, number>;
  total: number;
  items: TaskAssetItem[];
}

// ---- New unified asset model (new backend) ----
export type NewAssetType = "root_domain" | "ip" | "subdomain" | "app" | "service" | "endpoint";

export interface Asset {
  id: number;
  type: NewAssetType;
  company_id?: number;
  task_ids: number[];
  domain?: string;
  root_domain?: string;
  ip?: string;
  c_segment?: string;
  port?: number;
  icp?: string;
  bound_domains?: string[];
  open_ports?: { port: number; service?: string }[];
  record_type?: string;
  record_value?: string[] | string;
  bundle_id?: string;
  app_name?: string;
  category?: string;
  app_description?: string;
  app_icp?: string;
  url?: string;
  service_type?: string;
  service_name?: string;
  favicon_mmh3?: string;
  status_code?: number;
  content_length?: number;
  page_title?: string;
  technologies?: string[];
  auth?: Record<string, unknown>[];
  method?: string;
  params?: Record<string, unknown>[];
  extra?: Record<string, unknown>;
  last_seen: string;
  created_at?: string;
  task_source?: string;
  task_source_summary?: string;
  task_source_node_id?: number;
}

export interface IntentAsset {
  intent_id: number | string;
  asset_id: number;
  type: NewAssetType;
  label: string;
  source: string;
  source_summary: string;
  source_node_id?: number;
  source_task_id: number;
  inherited: boolean;
}

export interface TaskAssetMutation {
  requested: number;
  attached: number;
  existing: number;
}

export interface TaskAssetScopeMutation {
  requested: number;
  assets_linked: number;
  assets_existing: number;
  scopes_added: number;
  scopes_existing: number;
}

// 资产文件导入（POST /api/assets/import）的逐行失败原因。errors 后端最多返回 200 条。
export type AssetImportError = { row: number; value: string; error: string };

// 资产文件导入结果：total=解析出的有效条数；imported=成功入库；failed=入库失败；
// skipped=无法识别被跳过。results 是成功入库行的明细，errors 是失败/跳过行的原因。
export type AssetImportResult = {
  total: number;
  imported: number;
  failed: number;
  skipped: number;
  results: { row: number; id: number; type: string; value: string }[];
  errors: AssetImportError[];
};

// ---- Asset coverage graph (per task) ----
// 力导向「资产覆盖图」的一个节点。key 唯一：资产="a:<id>"、公司="c:<id>"、
// 无资产行的根域名="r:<domain>"。in_scope=false 的是仅用于连线的灰色上下文节点。
export interface CoverageGraphNode {
  key: string;
  kind: "company" | "root_domain" | "subdomain" | "ip" | "service" | "app" | "endpoint";
  label: string;
  tested: boolean;
  in_scope: boolean;
  asset_id?: number;
  company_id?: number;
  domain?: string;
  root_domain?: string;
  ip?: string;
  url?: string;
  port?: number;
  service_type?: string;
  app_name?: string;
  page_title?: string;
  status_code?: number;
}

export interface CoverageGraphEdge {
  src: string;
  dst: string;
}

export interface CoverageGraphData {
  nodes: CoverageGraphNode[];
  edges: CoverageGraphEdge[];
}

// 某资产在本任务探索图里关联到的意图/事实/发现（覆盖图节点抽屉用）。
export interface CoverageAssetRef {
  id: number;
  kind: string;
  state: string;
  summary: string;
  source_task_id?: string;
  inherited?: boolean;
}
export interface CoverageAssetRefs {
  intents: CoverageAssetRef[];
  facts: CoverageAssetRef[];
  findings: CoverageAssetRef[];
}

// ---- Workspace file manager (workDir) ----
export interface WorkspaceEntry {
  name: string;
  path: string; // workspace-relative, forward slashes
  dir: boolean;
  size: number;
  mtime: number; // unix millis
}
export interface WorkspaceListing {
  path: string;
  entries: WorkspaceEntry[];
}
export interface WorkspaceFile {
  path: string;
  size: number;
  binary: boolean;
  too_large?: boolean;
  content?: string;
}

// 任务测试范围的一条（覆盖度分母 + 授权边界）。
export interface TaskScopeRow {
  id: number;
  task_id: number;
  kind: "company" | "root_domain" | "subdomain" | "ip" | "cidr" | "icp" | "keyword";
  company_id?: number;
  company_name?: string; // 后端 JOIN companies 解析，仅 kind=company 有值
  domain?: string;
  net?: string;
  value?: string;
  source: "auto" | "agent" | "manual";
  reason?: string;
}

export type CompanyScopeKind = "domain" | "ip" | "cidr" | "icp" | "keyword";

// 新增企业时提交的结构化资产范围规则。
export interface CompanyScopeRule {
  kind: CompanyScopeKind;
  value: string;
}

// 资产范围写入的结果。errors 是本次提交里不合法的行；warnings 是与本次提交无关、
// 但会让归属结果不符合预期的既有数据问题（如 ip 字段存了主机名的资产）。
export interface CompanyScopeMutation {
  added: number;
  skipped: number;
  invalid: number;
  errors?: string[];
  warnings?: string[];
}

// 公司资产范围规则的一条（归属唯一真值来源）。
export interface ScopeRow {
  id: number;
  company_id: number;
  kind: CompanyScopeKind;
  domain?: string; // kind=domain 时有值
  net?: string; // kind=ip|cidr 时有值
  value?: string; // kind=icp|keyword 时可能由后端直接返回
  raw: string; // 原始用户输入，用于显示和回填
  reason?: string;
}

// 企业：type=company 的资产节点 + 图标 + 资产计数 + 资产范围规则。
export interface Company {
  id: number;
  name: string;
  logo?: string; // 远程图标 URL；空则前端用名称首字母
  asset_count: number;
  scope?: ScopeRow[];
}

// 企业列表页(「先选资产主体」)的一行:在 Company 基础上带任务数/报告数与更新时间。
export interface CompanyWithCounts extends Company {
  task_count?: number;
  report_count?: number;
  created_at?: string;
  updated_at?: string;
}

// ---- Exploration graph (per task) ----
export type ExploreKind = "task" | "begin" | "goal" | "intent" | "fact" | "finding" | "hint" | "digest";
export type GoalState = "open" | "met" | "abandoned";
export type IntentState = "open" | "running" | "paused" | "done" | "blocked" | "exhausted" | "stopped";
export type FindingState = "confirmed" | "dismissed";
export type HintState = "active" | "consumed";
export type ExploreRel = "spawns" | "derived_from" | "yields" | "proves" | "covers";

export interface TaskNode {
  id: string;
  type: ExploreKind;
  payload?: string;
  priority: number; // 0..10
  state: string; // GoalState | IntentState | FindingState | HintState
  origin: string;
  ts: string;
  source_task_id?: string;
  inherited?: boolean;
  delete_reason?: string; // 意图假删除(state='deleted')时的删除原因
}

// 播报板一页:按创建顺序分页的节点 + 这一页涉及的边 + 边另一端的节点(refs,按 id 索引),
// 这样每条播报都能说清「从哪来、产出了什么」,而不用把整张图拉下来。
export interface ExplorationNodePage {
  items: TaskNode[];
  total: number;
  page: number;
  size: number;
  edges: Edge[];
  refs: Record<string, TaskNode>;
  // 节点 id → 该节点锚定的资产(播报板展开时顺带展示,含本页节点与其邻居)。
  assets: Record<string, FindingAsset[]>;
}

export interface ExplorationNodeQuery {
  page?: number;
  size?: number;
  kinds?: ExploreKind[];
  states?: string[];
  q?: string;
  order?: "asc" | "desc";
}

// 目标管理卡片用的目标(后端已把 payload 拆成 text/vulnclass)。
export interface TaskGoal {
  id: string;
  text: string;
  vulnclass?: string;
  state: string; // GoalState
  origin?: string;
  ts: string;
}

// 约束管理卡片用的操作约束(allow=允许 / deny=禁止 / note=背景参考)。
// note 不构成操作边界,仅在规范合规审查时作为上下文(评分/奖励标准等)。
export type ConstraintKind = "allow" | "deny" | "note";
export interface TaskConstraint {
  id: string;
  kind: ConstraintKind;
  text: string;
  origin?: string;
  ts?: string;
}

// 规范合规审查统计(decision 模型逐操作对照约束的裁决计数)。
export interface ComplianceStats {
  checked: number;
  allowed: number;
  denied: number;
  flagged: number;
  asked: number;
  errors: number;
}

// 规范合规审查单条记录;violated 是被违反约束的 task_constraints.id(字符串形式)。
export interface ComplianceEntry {
  id: number;
  ts?: string;
  tool: string;
  agent: string;
  source: "hook" | "tool"; // hook=拦截链自动审 | tool=check_compliance 主动自查
  decision: "allow" | "deny" | "ask" | "flag" | "error";
  confidence: number;
  violated: string[];
  reason: string;
  args_preview: string;
}

// 创建任务时随任务一起写入的操作约束(建任务表单「AI 识别拆分」与手填共用)。
// 与 TaskConstraint 的区别:创建时还没有 id/时间戳,只有 kind+text。
export interface TaskConstraintInput {
  kind: ConstraintKind;
  text: string;
}

// AI 识别拆分任务描述的返回结构(POST /api/tasks/parse-input)。
// 契约:AI 只建议不落库——前端预填表单由人工检查后随创建请求提交;
// 原文没提到某类内容时对应字段为空串,禁止编造。
export interface TaskInputParse {
  /** 提炼后的目标(原文没有可识别的目标时为空串) */
  goal: string;
  /** 整理后的描述:剔除目标/约束/奖励后的背景与测试范围;无剩余内容为空串 */
  description: string;
  /** 拆出的操作约束(禁止→deny,范围/允许事项→allow),可能为空数组 */
  constraints: TaskConstraintInput[];
  /** 奖励标准整理(仅 src_mode=true 时识别;普通任务恒为空串) */
  rewards: string;
  /** 识别依据说明(用了哪些段落、哪些归类存疑),展示给用户参考 */
  note: string;
}

// ---- Findings ----
export type Severity = "critical" | "high" | "medium" | "low";

// 漏洞处置状态:待处理 / 处理中 / 已确认 / 已处理 / 已修复 / 误报 / 忽略 / 重复 / 风险接受。
export type FindingStatus =
  | "pending"
  | "in_progress"
  | "confirmed"
  | "resolved"
  | "fixed"
  | "false_positive"
  | "ignored"
  | "duplicate"
  | "risk_accepted";

// FindingAsset 是一个漏洞绑定的资产(已在后端预渲染 label)。
export interface FindingAsset {
  id: string;
  type: string;
  label: string;
}

export interface Finding {
  traffic_count?: number;
  evidence_version?: number;
  report_evidence_version?: number;
  report_stale?: boolean;
  id: string;
  finding_id?: string; // 独立 findings 表的行 id,状态更新的句柄(任务内旧节点可能缺失)
  vulnclass: string;
  name?: string; // 漏洞名称;为空时展示回退到 vulnclass
  severity: Severity;
  status: FindingStatus;
  summary: string;
  evidence: string;
  report?: string; // 详细报告(Markdown);仅详情接口返回,列表为空
  intent_id?: string;
  param_id?: string;
  task_id?: string;
  task_description?: string;
  source_task_id?: string;
  inherited?: boolean;
  assets?: FindingAsset[];
  ts: string;
}

// FindingsPage 是发现列表的服务端分页响应。
export interface FindingsPage {
  items: Finding[];
  total: number;
  page: number;
  page_size: number;
}

export interface FindingGroup {
  task_id: string | number | null;
  task_name?: string; // 可选任务名称;空/缺省=未命名
  task_description: string;
  task_status: string;
  count: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  last_found_at: string;
}

export interface FindingGroupsPage {
  items: FindingGroup[];
  total: number;
  finding_total: number;
  page: number;
  page_size: number;
}

export interface FindingDeepenResponse {
  task_id: string;
  intent_id: string;
  state: IntentState;
  queued: boolean;
}

// FindingStats 是发现全表聚合(统计卡 + 漏洞类型下拉),服务端计算,不受分页影响。
export interface FindingStats {
  total: number;
  pending: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  vulnclasses: string[];
  tasks: FindingTaskOption[];
}

// FindingTaskOption 是发现页「按任务」筛选下拉的一项:有漏洞的任务(描述为空表示任务已删除,
// 前端回退展示 id)及其漏洞条数。
export interface FindingTaskOption {
  id: string | number;
  name?: string; // 可选任务名称;空/缺省=未命名
  description: string;
  count: number;
}

// FindingQuery 是发现列表分页/筛选/排序参数。
export interface FindingQuery {
  page: number;
  pageSize: number;
  severity?: "all" | Severity;
  status?: "all" | FindingStatus;
  vulnclass?: string;
  task?: string; // 任务 id;"all"/空 = 不按任务筛选
  query?: string;
  sort?: "severity" | "time";
  // 资产树节点 key;选中一个节点 = 选中它的整棵子树。空 = 不按资产筛选。
  assetScope?: string;
}

// ---- Findings by asset (资产视图) ----
export type FindingAssetKind = "company" | "root_domain" | "subdomain" | "ip" | "service" | "app" | "endpoint" | "none";

// FindingAssetNode 是资产树的一个节点。key 形如 a:<id>(资产)、c:<id>(企业)、
// r:<domain>(库里没有资产行的根域名)、__none__(未关联资产)。
export interface FindingAssetNode {
  key: string;
  parent?: string;
  kind: FindingAssetKind;
  label: string;
  asset_id?: number;
  company_id?: number;
  self: number; // 直接挂在该资产上的发现数
  total: number; // 含子孙、按发现去重
  critical: number;
  high: number;
  medium: number;
  low: number;
  last_found_at: string;
}

export interface FindingAssetTree {
  nodes: FindingAssetNode[];
  finding_total: number;
  truncated: boolean;
  dropped_kinds?: string[];
}

// FINDING_UNASSIGNED_ASSET 与后端 db.FindingUnassignedAsset 对应。
export const FINDING_UNASSIGNED_ASSET = "__none__";

// ---- Activity / sessions ----
export type ActivityKind =
  | "tool_use"
  | "tool_result"
  | "text"
  | "thinking"
  | "result"
  | "user"
  | "intent" // LLM-generated exploration objective leading a worker session (UI-synthesized)
  | "round" // planner round boundary marker (engine-emitted)
  | "usage" // live cumulative token usage (per model turn); not rendered
  | "llm_switch" // automatic/manual task-level LLM switch
  | "llm_failover" // task-level provider switch / chain exhaustion audit event
  | "intercept_request"; // user-approval request from the intercept layer

// ChatAttachment 是一次上传的文件:path 相对该会话/任务工作目录(即 agent 的 CWD)。
export interface ChatAttachment {
  name: string;
  path: string;
  size: number;
  abs?: string; // 绝对路径(scope=staging 暂存上传时返回;建任务前把它写进描述)
}

export interface Activity {
  seq: number;
  intent_id?: string;
  worker: string; // session owner: planner | mainagent | work#1 ...
  ts: string;
  kind: ActivityKind;
  tool?: string;
  tool_use_id?: string;
  is_error?: boolean;
  summary: string;
  detail?: string;
  metadata?: {
    llm_transition?: LLMTransition;
  };
  source_task_id?: string;
  inherited?: boolean;
  main_seg?: number; // main-agent conversation segment (present only on worker="mainagent" rows)
  // token usage (present only on kind='result')
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
}

export interface LLMAuditProfile {
  id: number;
  name: string;
  format: string;
  model: string;
}

export interface LLMTransition {
  mode: "automatic" | "manual" | "exhausted";
  reason: string;
  previous?: LLMAuditProfile;
  next?: LLMAuditProfile;
}

export interface TaskLLMResolution {
  profile_id?: number;
  name: string;
  format: string;
  model: string;
  source: "task_chain" | "agent_binding" | "global_profile" | "environment" | "global";
  available: boolean;
  reason?: string;
}

export interface TaskLLMResolutions {
  mainagent: TaskLLMResolution;
  planner: TaskLLMResolution;
  worker: TaskLLMResolution;
}

// ---- Agent triggers (P3 调度，仅自定义 agent) ----
export interface AgentTrigger {
  id: number;
  agent_key: string;
  enabled: boolean;
  interval_sec: number; // 定时:每 N 秒(0=不定时)
  on_finding: boolean; // 任意任务发现 finding 时触发
  on_goal_met: boolean; // 任意任务达成目标时触发
  on_task_timeout: boolean; // 任意任务超时时触发
  on_tool_call: boolean; // 选中工具被调用(执行完成)时触发
  on_task_create: boolean; // 任意任务被创建时触发
  interval_message: string; // 各触发条件的独立用户消息
  finding_message: string;
  goal_message: string;
  task_timeout_message: string;
  tool_call_message: string;
  task_create_message: string;
  tool_names: string[]; // on_tool_call 选中的工具 key(至少一个)
  last_fire?: string;
}

// ---- Conversations (chat page) ----
export interface ActiveFindingRetest {
  id: number;
  finding_id: string;
  conversation_id: number;
  status: "pending" | "running";
}

export interface FindingRetest {
  id: number;
  finding_id: number;
  conversation_id: number | null;
  status: "pending" | "running" | "completed" | "failed" | "stopped";
  verdict: "" | "reproduced" | "fixed" | "inconclusive";
  notes: string;
  summary: string;
  evidence: string;
  error: string;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
}

export interface Conversation {
  id: number;
  running?: boolean; // live server state, returned with the conversation list
  agent_key: string;
  title: string;
  llm_profile_id?: number;
  pinned?: boolean;
  pinned_at?: string | null;
  created_at: string;
  updated_at: string;
}

// ---- Backend logs (/logs page) ----
export interface LogLine {
  seq: number;
  db_id?: number; // server_logs.id; present for DB-persisted lines
  ts: string;
  level: "info" | "warn" | "error";
  tag: string;
  text: string;
}

export type SessionRole = "mainagent" | "planner" | "worker" | "system" | "review";

// 目标审核记录（GET /api/goal-reviews，契约 GOAL_REVIEW_CONTRACTS.md）。
export interface GoalReview {
  id: number;
  task_id: number;
  goal_id: number;
  evidence_id: number;
  reason: string;
  verdict: "approved" | "rejected" | "unverified";
  confidence: number;
  reasons: string;
  gaps: string[];
  reviewer: string;
  duration_ms: number;
  fail_note: string;
  created_at: string;
}
export type SessionStatus = "running" | "paused" | "done" | "blocked" | "exhausted" | "pending" | "stopped" | "deleted";

// Daily token aggregate bucket (GET /api/tokens/daily).
export interface DailyTokenBucket {
  date: string; // "YYYY-MM-DD"
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// Per-worker token usage (GET /api/exploration/tokens).
export interface TokenUsage {
  worker: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface SessionTokenUsage {
  session: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface BatchControlItem {
  id: string;
  ok: boolean;
  status?: string;
  queued?: boolean;
  error?: string;
}

// 批量改分类的逐任务结果。失败只可能是任务已被删除，分类本身的写入是原子的。
export interface BatchCategoryItem {
  id: string;
  ok: boolean;
  error?: string;
}

// Whole-task (all agents) token aggregate.
export interface TokenTotal {
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// Global per-profile token spend from the llm_usage ledger (GET /api/tokens/usage).
export interface ProfileUsage {
  profile_name: string;
  calls: number;
  tasks: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// One (profile, UTC day) token bucket for the dashboard's daily chart (new source).
export interface ProfileDayUsage {
  profile_name: string;
  date: string; // YYYY-MM-DD
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
}

// Response of GET /api/tokens/usage — the dashboard's "new" (llm_usage) token view.
export interface UsageStats {
  by_profile: ProfileUsage[];
  daily: ProfileDayUsage[];
}

// Per-model token usage for one task (GET /api/llm/records/by-model), from the
// always-on llm_usage metering ledger. calls = number of LLM calls on this model.
export interface ModelTokenStat {
  model: string;
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface Session {
  id: string;
  role: SessionRole;
  title: string;
  status: SessionStatus;
  live: boolean;
  last_activity: string;
  intent_id?: string;
  source_task_id?: string;
  inherited?: boolean;
  seg?: number; // main-agent session: which conversation segment (0 = original)
}

// ---- Security ----
export interface AuditEntry {
  ts: string;
  tool: string;
  action: "allow" | "block";
  reason?: string;
  command?: string;
}

export interface Audit {
  entries?: AuditEntry[];
  attributions?: Record<string, number>;
}

// ---- Traffic ----
export interface TrafficExchange {
  id: string;
  ts: string;
  host: string;
  method: string;
  url: string;
  status: number;
  content_type: string;
  resp_len: number;
}

export interface TrafficResp {
  enabled: boolean;
  proxy?: string;
  count?: number; // global total (unfiltered)
  total?: number; // rows matching the current filter (for pagination)
  page?: number;
  size?: number;
  exchanges?: TrafficExchange[];
}

// Full raw request/response of one exchange (lazy-loaded on row select).
export interface TrafficDetail {
  req: string;
  resp: string;
}

// One distinct recorded host with its exchange count (target picker).
export interface TrafficHost {
  host: string;
  count: number;
}

// ---- App settings (runtime toggles) ----
export interface Settings {
  traffic_capture: boolean;
  agent_traffic_binding: boolean; // Agent 自动绑定流量证据，默认关闭；不影响人工绑定
  llm_record: boolean; // LLM 录制开关（默认关）；关闭时不记录任何 LLM 调用
  // Web search. brave_key_set / tavily_key_set reflect whether a key is stored
  // (the values are never returned). On PUT, send the corresponding field to set/clear.
  web_search_enabled: boolean;
  web_search_backend: string; // "ddgs" | "brave-free" | "tavily" | "deepseek"
  brave_key_set: boolean;
  tavily_key_set: boolean;
  // write-only: only sent on PUT to store/clear the key.
  brave_search_api_key?: string;
  tavily_search_api_key?: string;
  // 独立出口代理(http/https/socks5)，用于访问搜索端点；与记录流量的 MITM 代理无关。空=直连。
  web_search_proxy?: string;
  // 全局出口代理(http/https/socks5，可带 user:pass)，所有目标流量走它。开启流量捕获时作为
  // MITM 上游；关闭捕获时直接注入 agent 的 bash/WebFetch。空=直连。
  global_proxy?: string;
  python_interpreter?: string; // 自定义脚本工具的 python 解释器路径(空=运行时检测)
  workers?: number; // 并发工作 agent 数(默认3)；对之后启动的任务生效
  // 任务并发上限:同时「运行中」的任务数上限。关闭=不限;开启后新建任务超限则排队,有空位自动启动。
  task_concurrency_enabled?: boolean; // 默认 false
  task_concurrency_limit?: number; // 开启后默认 5
  // LLM 轮询(故障转移)。默认关；开启后「未指定模型」的 agent 在当前配置不可用
  // （余额不足/key 失效/限流/服务异常）时自动切到下一个配置。
  llm_pool_enabled?: boolean; // 默认 false
  // 绑定了指定配置的 agent/任务失败时是否也回落到轮询链。默认 false = 绑定即独占。
  llm_pool_bind_fallback?: boolean;
  // 操作约束注入范围(默认都开):把任务的 allow/deny 约束拼进对应 agent 的系统提示。
  constraints_inject_planner?: boolean;
  constraints_inject_worker?: boolean;
  // 实验功能:noa 模型驱动上下文压缩(默认关)。开启后平台接入的四类 agent(planner/
  // worker/主 agent/对话)由 noa 接管上下文压缩,取代内置 compaction;每 run 读一次,对
  // 之后启动的 run 生效。
  noa_compaction?: boolean;
  // ---- 代理中心 ----
  // 出网模式:direct=本地裸网(直连) / pool=代理池(经选中的代理组)。默认 direct。
  proxy_egress_mode?: string;
  // 代理池模式下使用的代理组 id(0=未选)。
  proxy_active_group_id?: number;
  // 代理池模式下 agent 出网指向的本地监听地址(只读,由后端回显)。
  proxy_listen_addr?: string;
}

// ---- 代理中心 ----
// 代理组:调度的单位,承载策略、监听端口与测活参数。

/** 组级调度规则(存 proxy_groups.sched_rules):测活/调度结果驱动的自动升降级。 */
export interface ProxySchedRules {
  /** 自动升降级开关:连续存活到阈值自动提优先级,失败自动降 */
  auto_boost: boolean;
  /** 每连续存活 N 次提升一级(默认 5) */
  boost_every: number;
  /** 每次提升的优先级增量(默认 1) */
  boost_step: number;
  /** 自动提升的优先级上限(默认 50;手动置顶不受此限) */
  boost_cap: number;
  /** 每次测活/调度失败的降级量(默认 2;0=不降) */
  demote_on_fail: number;
}

export interface ProxyGroup {
  id: number;
  name: string;
  /** manual(纯手工) / api(接入代理池 API) / wild(全网在野搜集) / mixed(混合) */
  kind: "manual" | "api" | "wild" | "mixed" | string;
  /** round_robin / random / least_latency / failover / quality(质量加权) */
  strategy: "round_robin" | "random" | "least_latency" | "failover" | "quality" | string;
  /** 本地代理池监听端口;0=启用时自动分配 */
  listen_port: number;
  enabled: boolean;
  auto_check: boolean;
  check_interval_minutes: number;
  test_url: string;
  timeout_ms: number;
  max_failures: number;
  concurrency: number;
  remark: string;
  /** 组级调度规则(自动升降级) */
  sched_rules: ProxySchedRules;
  created_at: string;
  updated_at: string;
  // 派生计数(列表页展示)
  proxy_count: number;
  alive_count: number;
  dead_count: number;
  source_count: number;
}

/** 组内单条代理。 */
export interface ProxyEntry {
  id: number;
  group_id: number;
  url: string;
  /** http / https / socks5 */
  protocol: string;
  host: string;
  port: number;
  label: string;
  /** manual(手工) / api(代理池 API) / public(公益池) / wild(全网在野搜集) */
  source: "manual" | "api" | "public" | "wild" | string;
  source_ref: string;
  region: string;
  /** 地区识别结果(由出口 IP 解析):ISO 两位国家码,如 CN/US。空 = 未识别 */
  country_code: string;
  /** 地区识别结果的国家名,如「中国」。空 = 未识别 */
  country: string;
  /** transparent / anonymous / elite */
  anonymity: string;
  /** 最近一次测活延迟(毫秒);0=未测 */
  latency_ms: number;
  /** untested / alive / dead / checking */
  status: "untested" | "alive" | "dead" | "checking" | string;
  fail_count: number;
  success_count: number;
  last_checked_at?: string;
  last_ok_at?: string;
  /** 测活观测到的出口 IP */
  exit_ip: string;
  /** 累计被调度次数 */
  use_count: number;
  /** 调度优先级(越大越优先;手动置顶与自动升降级共用) */
  priority: number;
  /** 连续测活/调度成功次数(一次失败清零) */
  streak: number;
  created_at: string;
  updated_at: string;
}

/** 调度规则总览里的单组视图:组字段 + 提升级代理样本。 */
export interface ProxyScheduleGroup extends ProxyGroup {
  /** 优先级 > 0 的代理数(已提升/置顶) */
  boosted_count: number;
  /** 按优先级排序的前几条代理(alive 优先) */
  top: ProxyEntry[];
}

/** 挂在组上的一个代理池 API 接入配置。 */
export interface ProxySource {
  id: number;
  group_id: number;
  provider_key: string;
  name: string;
  api_url: string;
  username: string;
  params: Record<string, unknown>;
  enabled: boolean;
  interval_minutes: number;
  last_sync_at?: string;
  /** idle / running / ok / failed */
  last_status: string;
  last_error: string;
  imported_count: number;
  total_count: number;
  created_at: string;
  updated_at: string;
  /** 凭据存在性标记(密文永不回显) */
  has_api_key: boolean;
  has_password: boolean;
}

/** 内置代理源定义(商业 / 公益 / 在野搜集)。 */
export interface ProxyProviderDef {
  key: string;
  title: string;
  kind: "commercial" | "public" | "wild" | string;
  docs?: string;
  homepage?: string;
  default_url: string;
  need_key: boolean;
  need_auth: boolean;
  protocol: string;
  region: string;
  parse: string;
  params?: string[];
}

/** 一条走代理池的请求流水。 */
export interface ProxyRequest {
  id: number;
  ts: string;
  group_id: number;
  proxy_id: number;
  proxy_url: string;
  method: string;
  host: string;
  url: string;
  status_code: number;
  latency_ms: number;
  bytes_in: number;
  bytes_out: number;
  ok: boolean;
  error: string;
  task_id: number;
  stage: string;
}

/** 仪表盘时间桶聚合。 */
export interface ProxyTrafficStat {
  bucket: string;
  total: number;
  failed: number;
  avg_ms: number;
  bytes_in: number;
  bytes_out: number;
}

/** 仪表盘顶部总览。 */
export interface ProxyTrafficSummary {
  total: number;
  failed: number;
  success_rate: number;
  avg_ms: number;
  bytes_in: number;
  bytes_out: number;
  window_mins: number;
}

/** 按代理聚合的调度占比(仪表盘「动态调度」排行)。 */
export interface ProxyUsageStat {
  proxy_id: number;
  proxy_url: string;
  region: string;
  /** 地区识别结果:国家码 + 国家名(空 = 未识别) */
  country_code: string;
  country: string;
  source: string;
  status: string;
  latency_ms: number;
  total: number;
  failed: number;
  avg_ms: number;
}

/** 一次测活批次。 */
export interface ProxyCheckRun {
  id: number;
  group_id: number;
  /** manual(手动) / auto(定时) */
  trigger: string;
  /** running / done / failed */
  status: string;
  checked: number;
  alive: number;
  dead: number;
  error: string;
  started_at: string;
  finished_at?: string;
}

/** 按国家聚合的代理计数(代理列表的国家筛选器)。 */
export interface ProxyCountryStat {
  country_code: string;
  country: string;
  total: number;
  alive: number;
}

/** 出网状态总览(仪表盘顶部 + 快速切换)。 */
export interface ProxyEgressStatus {
  /** direct / pool */
  mode: string;
  active_group_id: number;
  active_group_name: string;
  /** 代理池模式下 agent 出网指向的本地地址;direct 时为空 */
  listen_addr: string;
  /** 当前生效的出网地址(direct 时是全局出口代理或空) */
  effective_proxy: string;
  /** 已装载的代理组数(启用中) */
  loaded_groups: number;
  /** 全平台代理统计 */
  total_proxies: number;
  alive_proxies: number;
  dead_proxies: number;
  /** 调度器是否在跑 */
  running: boolean;
}

// ---- LLM config ----
export interface LLMProfile {
  id: string;
  name: string;
  format: "openai" | "anthropic" | "openai-responses";
  base_url?: string;
  proxy?: string;
  model: string;
  api_key_hint?: string;
  rate_per_second: number;
  rate_per_minute: number;
  context_window_k?: number;
  // 思考开关(thinking.type): ""=不发送(默认) | "disabled"=关闭 | "enabled"=开启
  thinking_type?: string;
  // 思考强度: ""=不发送(默认) | "low"/"medium"/"high"/"xhigh"/"max"
  reasoning_effort?: string;
  is_default: boolean;
  // 轮询顺位：越大越先被选中。激活配置恒为链首，与本值无关。
  priority?: number;
  // true = 不作为故障转移目标（仍可被 agent/任务显式绑定使用）。
  pool_exclude?: boolean;
  // true（默认）= 流式(SSE) | false = 真·非流式(stream:false，一次性返回)。
  streaming?: boolean;
  // 单次回复的输出上限(token)。0 = 不发送该字段，由服务端默认值决定。
  // 注意与 context_window_k 区分：后者是模型总容量，只在本地用于压缩阈值。
  max_tokens?: number;
  // 上限用哪个请求字段名，仅 format="openai" 有意义：
  // ""=max_tokens(默认) | "max_completion_tokens"(OpenAI 推理模型只认它)
  max_tokens_field?: string;
  // 自定义会话头名：非空时每次请求带该 HTTP 头，头值=当前会话/意图的 session id。
  // ""=不发送。用于按 session-id 头做提示缓存/粘性路由的网关。
  session_header_key?: string;
  // 本配置对重试的覆盖（建连/空响应/同 provider 安全窗口）。留空/全 0 = 跟随全局策略。
  retry?: LLMRetryOverride;
}

// ---- LLM 重试策略 ----
// 一层重试的两个旋钮。两者都是「0 = 未配置」：
//   attempts    0=用默认次数 | -1=关闭该层重试 | >0=重试次数
//   interval_ms 0=用默认的指数退避 | >0=改用这个固定毫秒间隔
export interface LLMRetryRule {
  attempts: number;
  interval_ms: number;
}

// 单个 LLM 配置能覆盖的三层（都是「跟着端点走」的重试）。
export interface LLMRetryOverride {
  connect: LLMRetryRule; // 建连重试：连接重置/超时/429/5xx，流开始前
  empty: LLMRetryRule; // 空响应重试：完成但没有任何内容（仅 openai 格式）
  stream: LLMRetryRule; // 同 provider 安全窗口重试：未交付输出前的断流重放
}

// 全局策略 = 上面三层的默认值 + 两层只有全局的：
//   breaker 轮询熔断（attempts=连续几次瞬时失败熔断，interval_ms=固定冷却时长）
//   intent  意图重跑（worker 以 model_error 收场后整条意图重跑）
export interface LLMRetryPolicy extends LLMRetryOverride {
  breaker: LLMRetryRule;
  intent: LLMRetryRule;
}

// ---- LLM 轮询（故障转移）----
// 一个配置在轮询链中的位置与健康状态。state:
//   ok       正常
//   degraded 有连续失败但未达熔断阈值
//   tripped  已熔断，冷却期内被跳过（cooldown_secs 为剩余秒数）
export interface LLMPoolMember {
  profile_id: string;
  name: string;
  model: string;
  format: string;
  priority: number;
  active: boolean; // 是否为当前激活配置（恒为链首）
  excluded: boolean; // pool_exclude：不参与轮询
  state: "ok" | "degraded" | "tripped";
  fails: number;
  trips: number;
  cooldown_secs: number;
  last_error?: string;
  last_at?: string;
}

export interface LLMPoolStatus {
  enabled: boolean;
  bind_fallback: boolean;
  chain: LLMPoolMember[];
}

// ---- Agents ----
export interface Agent {
  id: string;
  key: string; // 内置为 goals/planner/mainagent/worker；自定义为用户自定 key
  name: string;
  description?: string;
  role: string;
  stage?: "" | "recon" | "exploit" | "post" | "report"; // 归属阶段:''=通用(跨阶段,只在任务中心的对话可见)
  builtin: boolean;
  enabled: boolean;
  llm_profile_id?: number | null; // 绑定的 LLM 配置；null/absent = 跟随任务/会话/全局
  max_turns?: number; // 0 = 无限制
  run_seconds?: number; // worker 单次运行墙钟上限(秒)；0 = 无限制
  web_search?: boolean; // 是否启用网络搜索(受系统全局开关门控)
  interactive_shell?: boolean; // 是否启用交互式 shell(持久 PTY 会话工具族)
  // P3 触发后处理策略(仅自定义 agent 有意义)
  trigger_run_mode?: "serial" | "parallel"; // 串行排队 / 每次触发各自并发一个会话
  trigger_merge_mode?: "by_task" | "all" | "none"; // 仅 serial：同任务合并 / 全部合并 / 不合并
  trigger_max_parallel?: number; // 仅 parallel：每 agent 并发上限；0=不限
  // 绑定数量(仅列表接口返回)：可见 MCP / 可见 Skill / 绑定工具
  mcp_count?: number;
  skill_count?: number;
  tool_count?: number;
}

export interface PromptVar {
  name: string;
  description: string;
  example: string;
  source: "exploration" | "runtime" | "distilled";
}

export interface PromptVersion {
  version: number;
  ts: string;
  note: string;
  template_text: string;
}

export interface AgentDetail {
  agent: Agent;
  prompt: string;
  variables: PromptVar[];
  versions: PromptVersion[];
  visibility: { mcp: number[]; skill: string[] };
  // 可绑定的 LLM 配置候选(供「默认模型」下拉)；当前绑定见 agent.llm_profile_id
  llm_profiles?: { id: number; name: string; model: string; is_default: boolean }[];
  wrapup_prompt?: string; // 已保存的收尾提示词(空=用内置默认)
  wrapup_default?: string; // 内置默认收尾提示词(占位/恢复默认)
  wrapup_max_turns?: number; // 已保存的收尾轮数(0=用内置默认)
  wrapup_max_turns_default?: number; // 内置默认收尾轮数(供 "0=默认N" 提示)
  // 任务级超时收尾词(仅 worker/planner，task_timeout_wrapup_supported=true 时才显示该分区)
  task_timeout_wrapup_supported?: boolean;
  task_timeout_wrapup_prompt?: string;
  task_timeout_wrapup_default?: string;
  task_timeout_wrapup_max_turns?: number;
  task_timeout_wrapup_max_turns_default?: number;
}

// ---- MCP ----
export interface MCPServer {
  id: number;
  name: string;
  transport: "stdio" | "http" | "sse";
  command?: string;
  args: string[];
  env: Record<string, string>;
  url?: string;
  enabled: boolean;
  insecure?: boolean; // http: skip TLS cert verification (self-signed servers)
  tools?: string[]; // mcp_tools_cache (names only, for the count)
}

export interface MCPTool {
  name: string;
  description: string;
}

// ---- Skills ----
// Fields align with the agentskills.io open specification.
// description covers both "what the skill does" and "when to use it".
export interface SkillItem {
  name: string; // unique key = directory name
  description?: string; // required per spec; covers what + when to use
  license?: string; // optional: SPDX identifier or free text
  compatibility?: string; // optional: environment requirements
  mcps?: string[]; // MCP server names this skill unlocks on load
  files: string[]; // files in the skill directory
  // 调用统计（skill_usage 账本）。从未被调用过的 skill：calls=0、last_used 缺省。
  calls: number;
  tasks: number; // 加载过它的任务数（chat 会话不计入）
  usage_agents: string[]; // 加载过它的 agent key
  last_used?: string;
}

// SkillCall 是一次 Skill() 调用（单个 skill 的最近调用列表）。
export interface SkillCall {
  ts: string;
  agent_key: string;
  task_id: number; // 0 = 非任务场景（对话会话）
  session_id: string;
  args_len: number;
}

// MissingSkill 是被点名但不存在的 skill —— "想用但没有"的缺口。
export interface MissingSkill {
  skill: string;
  calls: number;
  agents: string[];
  last_used?: string;
}

// ---- Tools (内置工具目录) ----
// key + handler live in Go; only these fields are page-editable. system tools lock
// the key and the parameter *structure* (name/type/required) — the per-param
// description/default and the agent binding are what move.
export interface Tool {
  key: string;
  system: boolean;
  description: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  schema: Record<string, any>; // full JSON-Schema (object with properties)
  agents: string[]; // bound agent keys
  enabled: boolean;
  kind?: "builtin" | "shell" | "command" | "script" | "http"; // 自定义工具类型
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  exec?: Record<string, any>; // 自定义工具执行规格(kind!=builtin)
  deferred?: boolean; // schema 延迟(SearchExtraTools/ExecuteExtraTool)
  calls?: number; // persistent runtime invocation count (older APIs may omit it)
  stage?: "" | "recon" | "exploit" | "post"; // 归属阶段;缺省=通用(跨阶段)
}

// ---- 工具手动执行记录(三阶段工具执行页) ----

export interface ToolExecution {
  id: number;
  stage: string;
  tool_key: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  input?: Record<string, any>;
  status: "running" | "done" | "failed" | string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  summary?: Record<string, any>;
  raw_evidence_hash: string;
  created_at: string;
  ended_at?: string | null;
}

export interface RawReconAsset {
  type: string;
  value: string;
  port?: number;
}

// ---- 字典库 ----

export interface Wordbook {
  id: number;
  name: string;
  kind: "subdomain" | "path" | "password" | "username" | "other" | string;
  line_count: number;
  byte_size: number;
  note: string;
  created_at: string;
}

export const WORDBOOK_KIND_LABEL: Record<string, string> = {
  subdomain: "子域字典",
  path: "目录路径",
  password: "密码",
  username: "用户名",
  other: "其他",
};

// ---- Stats ----
export interface Stats {
  assets: number;
  engine_mode: EngineMode;
  llm_configured: boolean;
  roe_enabled: boolean;
  findings_confirmed: number;
  active_task?: Partial<Task>;
}

// ---- Intercept Rules ----
// 五档监管动作:allow=AI放通 / deny=正则拦截·AI拦截 / ask=AI转人工 /
// flag=AI判疑(标记但放通——记审计不阻塞,供人事后复盘)。
export type InterceptAction = "allow" | "deny" | "ask" | "flag";
export type InterceptMatchTarget = "tool_name" | "tool_input";
export type InterceptMatchType = "string" | "regex";

export interface InterceptRule {
  id: number;
  name: string;
  enabled: boolean;
  priority: number;
  match_target: InterceptMatchTarget;
  match_type: InterceptMatchType;
  pattern: string;
  action: InterceptAction;
  message: string;
  timeout_enabled: boolean;
  timeout_seconds: number;
  timeout_action: "deny" | "allow";
  /** 适用范围:""=全部任务(缺省,既有行为);"src"=仅 SRC 模式任务 */
  scope?: "" | "src" | string;
  created_at: string;
  updated_at: string;
}

// ---- SRC 监管聚合(监管&规则中心 · SRC 板块) ----

export interface SRCGuardOverview {
  /** SRC 模式任务总数(src_mode=true) */
  src_task_count: number;
  /** 运行中/已创建的 SRC 任务数细分 */
  src_task_active: number;
  /** 仅 SRC 适用的拦截规则数 */
  rules_scoped_src: number;
  /** 全部拦截规则数 */
  rules_total: number;
  /** SRC 任务最近被拦截/判疑的调用(时间倒序,截断条数由后端定) */
  recent_blocks: {
    id: number;
    task_id?: string;
    task_name?: string;
    agent_name?: string;
    tool_name?: string;
    action: string;
    message?: string;
    created_at?: string;
  }[];
}

// AI 识别填入的建议(后端 LLM 结构化输出;前端预填表单,人工检查后保存)
export interface SPECAIFillSuggestion {
  reward_standard: string;
  scoring_standard: string;
  code_of_conduct: string;
  rules: string;
  /** 识别依据说明(用了哪些附件/置信提示),展示给用户 */
  note?: string;
}

export interface InterceptPending {
  decision_source?: "rule" | "model" | "unknown" | "";
  id: number;
  rule_id?: number;
  conversation_id?: number;
  task_id?: string;
  agent_name: string;
  tool_name: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  tool_input: Record<string, any>;
  // flagged = AI 判疑放通:工具已执行,此记录仅留痕。
  status: "pending" | "allowed" | "denied" | "timeout" | "flagged";
  reason: string; // 规则 message 或模型判定理由(模型判定带 [模型] 前缀)
  decided_at?: string;
  created_at: string;
}

// JudgeConfig: 模型兜底审批(仅当没有任何拦截规则命中时由模型判断)的全局配置。
export interface JudgeConfig {
  enabled: boolean;
  profile_id: number; // 0 = 跟随激活/默认配置
  prompt: string; // 判定提示词;GET 未设置时后端回填内置模板全文
  timeout_seconds: number; // 模型调用超时
  fail_action: "allow" | "ask" | "deny" | "flag"; // 模型出错/超时/不可解析时的回退
  ask_timeout_seconds: number; // 模型判 ask 转人工后的审批等待超时
  ask_timeout_action: "allow" | "deny"; // 审批超时后的默认动作
}

// 审批台（契约 APPROVAL_CONSOLE_CONTRACTS §3）：q/task_id/tool 为审批台新增筛选，
// 旧端点（/intercept/history 等）同样接受并生效。
export interface InterceptApprovalFilter {
  status?: InterceptPending["status"];
  decision_source?: "rule" | "model" | "unknown";
  q?: string; // 搜索：工具名/理由/任务名/会话标题/tool_input 原文（含脚本）
  task_id?: string; // 任务来源精确过滤
  tool?: string; // 工具名精确过滤
}

// InterceptApprovalRow enriches InterceptPending with conversation/task and rule context.
// task_* 四字段仅审批台/分页端点恒返回（LEFT JOIN tasks）；/intercept/pending 裸端点
// 不带——所以是可选的，消费方渲染时按空兜底（契约 APPROVAL_CONSOLE_CONTRACTS §2/§3）。
export interface InterceptApprovalRow extends InterceptPending {
  conv_title: string; // "" if no linked conversation
  conv_agent_key: string; // "" if no linked conversation
  rule_name: string; // "" if rule was deleted
  task_name?: string; // 关联任务名；""/undefined=无关联任务或任务已删
  task_stage?: string; // ""|recon|exploit|post|report
  task_kind?: string; // tasks.kind（agent/recon/...）
  task_src?: boolean; // tasks.src_mode
}

// 审批台分类页签计数（五档恒全量返回，缺失档=0）。
export interface InterceptConsoleCounts {
  pending: number;
  allowed: number;
  denied: number;
  timeout: number;
  flagged: number;
}

// 审批台工具筛选下拉的一行（GROUP BY tool_name，最多 50，按 count 降序）。
export interface InterceptToolCount {
  tool: string;
  count: number;
}

// GET /api/intercept/console 的响应体（契约 APPROVAL_CONSOLE_CONTRACTS §1）。
export interface InterceptConsolePage {
  items: InterceptApprovalRow[];
  total: number;
  page: number;
  page_size: number;
  counts: InterceptConsoleCounts;
  tools: InterceptToolCount[];
}

// ── 资产同步 (ScopeSentry 数据源) ──────────────────────────────────────────────
export interface SSProject {
  id: string; // MongoDB ObjectID — used as filter.project
  name: string;
  logo?: string;
  AssetCount?: number;
  tag?: string;
}

export interface SSTask {
  id: string;
  name: string; // used as filter.task
  status?: number;
  progress?: number;
  creatTime?: string;
  endTime?: string;
}

// ConvTokenSummary — one conversation's token total (+ profile/date) for merging
// chat usage into the dashboard token stats. GET /api/tokens/conversations.
export interface ConvTokenSummary {
  llm_profile_id: number | null;
  created_at: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// ---- Command recording (Bash execution history) ----
export interface CommandRecord {
  id: number;
  exploration_id: number;
  worker: string;
  tool: string;
  command: string; // raw tool input (JSON)
  output: string;
  is_error: boolean;
  created_at: string;
}

// 单个工具的调用统计（/commands/stats）；errors 为其中失败的次数。
export interface ToolStat {
  tool: string;
  total: number;
  errors: number;
}

// ---- LLM recording ----
export interface LLMRecordItem {
  id: number;
  ts: string;
  model: string;
  profile_name: string;
  session_id: string;
  task_id: string;
  worker: string;
  latency_ms: number;
  input_tokens: number;
  output_tokens: number;
  cache_read: number;
  cache_write: number;
  status: string;
  error?: string;
}

export interface LLMRecordDetail extends LLMRecordItem {
  request_body: string;
  response_body: string;
  // provider 实际收发的 HTTP 原文：请求为 buildBody() 发出的完整 body（含工具
  // schema），响应为原始 SSE 帧。上面的 request_body/response_body 是归一化视图，
  // 丢弃了工具 schema 与 tool_use 块。旧记录为空。
  raw_request?: string;
  raw_response?: string;
}

// One distinct task with its LLM-record count (task picker on the records page).
export interface LLMTask {
  task_id: string;
  count: number;
}

// The exact JSON sent to the review model, retained for all model verdicts.
export interface InterceptReviewInput {
  version: number;
  /** v5 起附带:命令引用的解释器脚本文件快照,裁判据此核实脚本行为。 */
  script_files?: { path: string; content?: string; truncated?: boolean; missing?: boolean }[];
  background?: {
    // worker_summary is retained only for immutable v2/v3 snapshots.
    source: "user_message" | "worker_summary";
    text: string;
    truncated?: boolean;
  };
  // Version 1 snapshots are immutable and remain readable in historical audits.
  task?: {
    task_id: number;
    description: string;
    goal: string;
    constraints: { id: number; kind: string; text: string; origin: string; created_at: number }[];
    truncated?: boolean;
  };
  working_directory?: string;
  worker_intent?: string;
  turn_input?: string;
  background_truncated?: boolean;
  // Legacy v1/v2 snapshots only; v3 never sends execution history.
  history?: {
    tool_use_id: string;
    tool: string;
    arguments_preview: string;
    result: string;
    status: "succeeded" | "failed";
    truncated?: boolean;
  }[];
  history_truncated?: boolean;
  correlation?: "exact" | "ambiguous" | "unavailable";
  tool_name: string;
  arguments: Record<string, unknown>;
}

// Immutable review snapshot plus separately recorded execution outcome.
export interface InterceptAudit {
  model_input?: InterceptReviewInput;
  model_input_digest?: string;
  run_id?: string;
  tool_use_id?: string;
  correlation: "exact" | "ambiguous" | "unavailable";
  input_digest: string;
  user_message: string;
  user_truncated?: boolean;
  context:
    | { kind: string; tool?: string; tool_use_id?: string; text: string; is_error?: boolean; truncated?: boolean }[]
    | null;
  context_truncated?: boolean;
  /** 命令引用的解释器脚本文件快照(审批时抓取),供审核人查看脚本内容。 */
  script_files?: { path: string; content?: string; truncated?: boolean; missing?: boolean }[];
  captured_at: string;
  model_fallback?: boolean;
  initial_action: "allow" | "ask" | "deny";
  initial_reason: string;
  effective_action?: "allow" | "deny";
  decision_reason?: string;
  rule_name?: string;
  config_digest?: string;
  profile_id?: number;
  execution_status: "not_started" | "not_executed" | "awaiting_result" | "succeeded" | "failed" | "unknown";
  output?: string;
  output_truncated?: boolean;
  execution_ended_at?: string;
}
export interface InterceptDetail extends InterceptApprovalRow {
  audit: InterceptAudit | null;
}

export type TrafficEvidenceRole = "baseline" | "proof" | "verification" | "supporting";
export interface TrafficEvidenceRef {
  traffic_id: string;
  role?: TrafficEvidenceRole;
  note?: string;
}
export interface TrafficEvidenceSnapshot {
  id: string;
  source_traffic_id: string;
  captured_at: number;
  url: string;
  method: string;
  status: number;
  content_type: string;
  req_head?: string;
  resp_head?: string;
  req_hash: string;
  resp_hash: string;
  req_len: number;
  resp_len: number;
}
export interface FindingTrafficBinding {
  id: string;
  finding_id: string;
  snapshot_id: string;
  role: TrafficEvidenceRole;
  note: string;
  position: number;
  created_at: string;
  snapshot: TrafficEvidenceSnapshot;
}
export interface FindingTraffic {
  finding_id: string;
  version: number;
  report_version: number;
  bindings: FindingTrafficBinding[];
}
export interface EvidenceBodyPreview {
  content: string;
  offset: number;
  total: number;
  next_offset: number;
  truncated: boolean;
  binary: boolean;
}
export interface FindingTrafficDetail {
  binding: FindingTrafficBinding;
  request: EvidenceBodyPreview;
  response: EvidenceBodyPreview;
}

/** GET /api/update/check —— 当前版本与 GitHub 最新正式版的比较结果。 */
export interface UpdateCheck {
  /** 当前运行的版本；开发构建为 "dev" 或 git describe 的带后缀形式。 */
  current: string;
  /** 运行形态。docker 下换装只作用于容器可写层，重建容器会退回镜像版本。 */
  mode: "docker" | "binary";
  os: string;
  arch: string;
  repo: string;
  /** 是否存在可回滚的上一版本（artex.old）。 */
  has_backup: boolean;
  /** 本次启动时自更新自举的结论（换装失败 / 已回滚等），无事发生时为空。 */
  boot_notice?: string;
  rolled_back?: boolean;
  /** 查询 GitHub 失败时给出原因，此时下面的字段都不会有。 */
  error?: string;
  latest?: string;
  notes?: string;
  html_url?: string;
  published_at?: string;
  /** 当前平台对应的发布包名，以及该 Release 是否真的带了它。 */
  asset?: string;
  asset_available?: boolean;
  size?: number;
  has_update?: boolean;
  /** 双方版本号是否可比较；开发构建为 false，此时禁用一键更新。 */
  comparable?: boolean;
  /** comparable 为 false 时的说明。 */
  reason?: string;
}

/** /api/update/stream 推送的一条更新进度。 */
export interface UpdateProgress {
  phase: "idle" | "downloading" | "verifying" | "extracting" | "staged" | "failed";
  /** 仅下载阶段有意义（0-100）；其余阶段为 -1。 */
  percent: number;
  message: string;
  version?: string;
  error?: string;
}

// Original execution selected from an approval, never submitted to the reviewer.
export interface InterceptExecution {
  conversation_id: number | null;
  task_id: string | null;
  session: string;
  seq: number;
  items: Activity[];
}

// ============================================================
// VPS 中心:任务级执行环境(自有 VPS 手动录入,agent 命令经 SSH 远程执行)
// ============================================================

/** 机器状态:online(在线空闲)/ busy(被任务占用)/ offline(测活失败)/ maintenance(维护,不参与自动指派)/ unknown(未测活)。 */
export type VpsStatus = "online" | "offline" | "unknown" | "busy" | "maintenance";
/** manual(手动录入)/ cloud(云 API 开通,二期)。 */
export type VpsProvider = "manual" | "cloud";
/** password(密码认证)/ key(私钥认证)。 */
export type VpsAuthType = "password" | "key";

/** 执行流水来源:agent(worker 工具)/ terminal(Web 终端会话)/ toolset(工具集脚本)/ manual(单命令试跑)/ detect(系统与工具探测)。 */
export type VpsExecSource = "agent" | "terminal" | "toolset" | "manual" | "detect";

export interface VpsMachine {
  id: number;
  name: string;
  host: string;
  port: number;
  username: string;
  auth_type: VpsAuthType | string;
  status: VpsStatus | string;
  os_name: string;
  os_version: string;
  kernel: string;
  arch: string;
  cpu_cores: number;
  memory_mb: number;
  disk_gb: number;
  region: string;
  provider: VpsProvider | string;
  tags: string[];
  workspace_base: string;
  /** 当前占用该机器的任务 id;0 = 空闲。 */
  assigned_task_id: number;
  /** 占用任务名(详情/列表回显用,后端 JOIN 补齐)。 */
  assigned_task_name?: string;
  last_error: string;
  last_check_at?: string;
  last_online_at?: string;
  notes: string;
  created_at: string;
  updated_at: string;
}

/** 录入/编辑机器的请求体。凭据只在创建时或显式更新时提交,永不回显。 */
export interface VpsMachineInput {
  name: string;
  host: string;
  port?: number;
  username: string;
  auth_type: VpsAuthType | string;
  /** 明文仅出现在请求里;服务端加密入库,响应永不回传。 */
  password?: string;
  private_key?: string;
  key_passphrase?: string;
  region?: string;
  tags?: string[];
  workspace_base?: string;
  notes?: string;
  /** 更新时=改状态;创建时忽略。 */
  status?: VpsStatus | string;
}

/** 单个工具的探测结果(机器详情「工具检测」矩阵一行)。 */
export interface VpsToolDetection {
  tool_name: string;
  installed: boolean;
  version: string;
  detected_at: string;
}

/** 一条执行流水。 */
export interface VpsExecution {
  id: number;
  machine_id: number;
  /** 0 = 非任务触发(终端/手动/探测)。 */
  task_id: number;
  source: VpsExecSource | string;
  toolset_id: number;
  command: string;
  exit_code: number | null;
  duration_ms: number;
  output_size: number;
  output_path: string;
  error: string;
  created_at: string;
}

export interface VpsToolset {
  id: number;
  name: string;
  description: string;
  script: string;
  builtin: boolean;
  created_at: string;
  updated_at: string;
}

/** 一次指派(含历史;released_at 为空 = 进行中)。 */
export interface VpsAssignment {
  id: number;
  machine_id: number;
  task_id: number;
  /** 任务名(后端 JOIN 补齐)。 */
  task_name?: string;
  /** 机器名(按任务维度查看时补齐)。 */
  machine_name?: string;
  assigned_by: string;
  assigned_at: string;
  released_at?: string;
  release_reason: string;
}

/** 机器详情 = 机器本体 + 工具检测矩阵 + 当前占用任务。 */
export interface VpsMachineDetail {
  machine: VpsMachine;
  detections: VpsToolDetection[];
  current_task: { id: number; name: string } | null;
}

/** 仪表盘一条请求取齐(仿 proxy/dashboard)。 */
export interface VpsDashboard {
  summary: {
    total: number;
    online: number;
    offline: number;
    busy: number;
    maintenance: number;
    executions_today: number;
    active_tasks: number;
  };
  /** 命令执行量时间序列(按小时桶)。 */
  series: { ts: string; count: number }[];
  /** 当前被占用的机器(谁在用、用了多久)。 */
  busy_machines: VpsMachine[];
  /** 最近执行流水。 */
  recent: VpsExecution[];
}

/** 单命令执行结果(手动试跑框 / agent 工具共用)。 */
export interface VpsExecResult {
  exit_code: number | null;
  duration_ms: number;
  output: string;
  /** 输出被截断时为 true(完整输出走流水详情)。 */
  truncated: boolean;
}

/** 任务详情页「执行环境」tab 的聚合数据。 */
export interface TaskVpsEnvironment {
  machine: VpsMachine | null;
  /** 该任务在此机器的执行流水。 */
  executions: VpsExecution[];
}

// ───────────────────────── C2 中心（契约 C2_CONTRACTS.md）─────────────────────────

/** online=心跳正常 / offline=超时未归 / dead=平台终止。 */
export type C2SessionStatus = "online" | "offline" | "dead";
export type C2TaskStatus = "queued" | "sent" | "done" | "error";
export type C2TaskKind = "shell" | "files_list" | "upload" | "download" | "loot" | "info";
export type C2TransferDirection = "up" | "down";
export type C2TransferStatus = "queued" | "running" | "done" | "error";
/** credential/hash 的 content 密文存储,列表恒空串,详情接口才给明文。 */
export type C2LootKind = "credential" | "hash" | "file" | "screenshot" | "other";
export type C2PayloadFormat = "ps1" | "bash" | "py" | "gosrc";
export type C2PayloadTarget = "windows" | "linux" | "macos" | "cross";
/** 三期传输协议(https 与 DNS 系四期)。 */
export type C2Proto = "http" | "ws" | "tcp" | "kcp" | "oss";
/** 载荷产物形态。 */
export type C2PayloadArtifact = "script" | "exe" | "dll" | "shellcode" | "ebpf";
/** 连接方向:reverse=beacon 连回平台;bind=beacon 监听、平台主动连。 */
export type C2Direction = "reverse" | "bind";

/** 监听器:beacon 回连入口。enc_key/transport_config_enc 永不出后端。 */
export interface C2Listener {
  id: number;
  name: string;
  proto: C2Proto;
  bind_host: string;
  /** 0 = 启动时自动分配;start 后以 runtime_addr 为准。 */
  port: number;
  /** 信道路径随机段。 */
  url_path: string;
  /** 写进载荷的回调 base URL,生成载荷必填。 */
  callback: string;
  enabled: boolean;
  notes: string;
  /** 运行中监听器的实际监听地址(未运行为空串)。 */
  runtime_addr?: string;
  session_count?: number;
  created_at: string;
  updated_at: string;
  /** 载体:local=平台本机;vps=经 SSH 远程转发以 VPS 机器为出入口(二期)。 */
  host_kind: "local" | "vps";
  /** vps 载体绑定的机器 id,0 = 无。 */
  vps_machine_id: number;
  /** vps 载体在机器公网侧的转发端口。 */
  remote_port: number;
  /** vps 载体机器名(JOIN 补齐,前端展示用)。 */
  vps_machine_name?: string;
  /** oss 传输配置是否已设置(凭据永不回显,仅此布尔)。 */
  transport_configured?: boolean;
}

export interface C2ListenerInput {
  name: string;
  bind_host?: string;
  port?: number;
  callback?: string;
  url_path?: string;
  notes?: string;
  host_kind?: "local" | "vps";
  vps_machine_id?: number;
  remote_port?: number;
  /** 三期:传输协议(缺省 http)。 */
  proto?: C2Proto;
  /** 三期:oss 等传输配置(AK/SK 服务端 secretbox 密封,永不回显;编辑时非空字段覆盖、空字段保留原值)。 */
  transport_config?: {
    endpoint?: string;
    bucket?: string;
    access_key?: string;
    secret_key?: string;
    prefix?: string;
  };
}

/** 监听器测活结果(契约 §24B)。checks 为逐项检查;latency_ms 仅网络探活成功时有值。 */
export interface C2ListenerCheck {
  ok: boolean;
  latency_ms: number | null;
  checks: { item: string; ok: boolean; detail: string }[];
}

/** 上线主机。sid 是 beacon 令牌;listener_id+beacon_uid 幂等复活。 */
export interface C2Session {
  id: number;
  listener_id: number;
  beacon_uid: string;
  hostname: string;
  username: string;
  elevated: boolean;
  os: string;
  arch: string;
  local_ips: string;
  pid: number;
  process: string;
  sleep_sec: number;
  jitter_pct: number;
  /** 绑定任务,0 = 未绑定。 */
  task_id: number;
  status: C2SessionStatus;
  labels: string;
  meta: Record<string, unknown>;
  /** 承载传输(http/ws/tcp/kcp/oss;一期数据回填 http)。 */
  transport: C2Proto;
  first_seen: string;
  last_seen: string;
}

/** 下发任务(命令/文件/采集/插件运行)。actor 区分页面人工与 agent 工具。 */
export interface C2Task {
  id: number;
  session_id: number;
  kind: C2TaskKind | "plugin";
  input: string;
  output: string;
  /** 大输出落盘路径名(下载走 /api/c2/tasks/{id}/output)。 */
  output_path: string;
  output_size: number;
  status: C2TaskStatus;
  actor: "user" | "agent";
  created_by: string;
  error: string;
  created_at: string;
  sent_at?: string;
  done_at?: string;
}

/** 文件传输:up=平台→目标,down=目标→平台。 */
export interface C2Transfer {
  id: number;
  session_id: number;
  task_id: number;
  direction: C2TransferDirection;
  remote_path: string;
  size: number;
  done_size: number;
  status: C2TransferStatus;
  error: string;
  created_at: string;
  done_at?: string;
}

/** 战利品。列表里 content 恒空串(credential/hash 密文存储),has_content 标记可查看。 */
export interface C2Loot {
  id: number;
  session_id: number;
  task_id: number;
  kind: C2LootKind;
  name: string;
  content: string;
  has_content?: boolean;
  source_path: string;
  created_at: string;
  /** 已转发现的 finding id(findings 表主键,0 = 未转换;二期 loot→finding)。 */
  finding_id?: number;
}

/** 载荷投递物。oneliner = 目标机一行执行命令;下载走 /api/c2/payloads/{id}/download。 */
export interface C2Payload {
  id: number;
  listener_id: number;
  name: string;
  format: C2PayloadFormat;
  target: C2PayloadTarget;
  token: string;
  sha256: string;
  size: number;
  oneliner: string;
  created_by: string;
  created_at: string;
  /** 变形来源免杀档案,0 = 无(一期直出)。 */
  evasion_profile_id: number;
  /** gosrc 是否已在本平台指派 VPS 上编译出二进制。 */
  built_on_vps: boolean;
  /** 产物形态(三期):script|exe|dll|shellcode|ebpf。 */
  artifact: C2PayloadArtifact;
  /** 连接方向(三期):reverse|bind。 */
  direction: C2Direction;
  /** 是否 stage 模式(三期):产物为 stager。 */
  staged: boolean;
}

/** C2 插件:可推送执行的能力包(三期,契约 §21)。 */
export interface C2Plugin {
  id: number;
  name: string;
  platform: C2PayloadTarget;
  kind: "script" | "binary";
  language: "ps1" | "sh" | "py";
  /** 入口命令模板,{{param}} 占位按 params_schema 渲染。 */
  entry: string;
  content: string;
  params_schema: Record<string, unknown>;
  description: string;
  builtin: boolean;
  created_at: string;
  updated_at: string;
}

export interface C2PluginInput {
  name: string;
  platform?: C2PayloadTarget;
  kind?: "script" | "binary";
  language?: "ps1" | "sh" | "py";
  entry?: string;
  content?: string;
  params_schema?: Record<string, unknown>;
  description?: string;
}

// ── C2 二期:免杀工作台与异步作业(契约 §13/§14)──

/** defender/360/huorong/generic/custom。 */
export type C2EvasionTargetAV = "defender" | "360" | "huorong" | "generic" | "custom";

export interface C2EvasionProfile {
  id: number;
  name: string;
  target_av: C2EvasionTargetAV;
  payload_format: C2PayloadFormat;
  /** 技术开关与参数(自由 JSON),前端按对象展示。 */
  techniques: Record<string, unknown>;
  /** 附加指示(喂给 LLM)。 */
  instructions: string;
  /** manual(手工)/llm(AI 生成)/builtin(三期知识库内置)。 */
  source: "manual" | "llm" | "builtin";
  notes: string;
  created_at: string;
  updated_at: string;
}

export interface C2EvasionProfileInput {
  name: string;
  target_av?: C2EvasionTargetAV;
  payload_format?: C2PayloadFormat;
  techniques?: Record<string, unknown>;
  instructions?: string;
  notes?: string;
}

export type C2JobKind = "evasion" | "build";
export type C2JobStatus = "queued" | "running" | "done" | "error";

/** 静态自测报告(c2_jobs.result 的约定形状,gosrc 构建作业另有 build_log)。 */
export interface C2StaticReport {
  hits: { marker: string; count: number }[];
  entropy: number;
  risk: "low" | "medium" | "high";
  rounds?: number;
  plan?: string;
  build_log?: string;
}

/** C2 异步作业:免杀变形(evasion)/VPS 构建(build)。 */
export interface C2Job {
  id: number;
  kind: C2JobKind;
  profile_id: number;
  payload_id: number;
  listener_id: number;
  vps_machine_id: number;
  /** stage=post 监看任务 id,0 = 无。 */
  task_id: number;
  status: C2JobStatus;
  input: Record<string, unknown>;
  result: C2StaticReport & Record<string, unknown>;
  error: string;
  created_at: string;
  done_at?: string;
}

/** 总览仪表盘一条请求取齐。 */
export interface C2Overview {
  sessions_online: number;
  sessions_offline: number;
  sessions_dead: number;
  listeners_enabled: number;
  listeners_total: number;
  tasks_queued: number;
  recent_sessions: C2Session[];
  recent_tasks: C2Task[];
}

// ── AI 记忆库 + 自进化(契约 AI_MEMORY_CONTRACTS.md)──

/** 记忆归属维度。 */
export type MemoryScopeKind = "company" | "src_target" | "global";

/** 事实分类：asset_note 资产备注 / tech_stack 技术栈 / path 可用路径 / credential_ref 凭据线索指针 / guard_note 防护观察 / other。 */
export type MemoryFactKind = "asset_note" | "tech_stack" | "path" | "credential_ref" | "guard_note" | "other";

/** 记忆事实来源：ai 任务复盘 / manual 人工 / recon 信息搜集 / backfill 历史回填。 */
export type MemoryFactSource = "ai" | "manual" | "recon" | "backfill";

/** 结构化记忆事实（双时间戳失效制：valid_to 非空 = 已被新事实取代）。 */
export interface MemoryFact {
  id: number;
  scope_kind: MemoryScopeKind;
  scope_id: number;
  asset_id: number;
  kind: MemoryFactKind;
  subject: string;
  content: string;
  source_task_id: number;
  source_kind: MemoryFactSource;
  confidence: number;
  observed_at: string;
  valid_from: string;
  valid_to: string | null;
  created_at: string;
  updated_at: string;
}

export interface MemoryFactInput {
  scope_kind: MemoryScopeKind;
  scope_id: number;
  kind?: MemoryFactKind;
  subject?: string;
  content: string;
  asset_id?: number;
}

/** 复盘里的一条失败/白费尝试（自进化最值钱的数据）。 */
export interface MemoryRunFailure {
  what: string;
  root_cause: string;
  cost_note?: string;
}

export type MemoryRunStatus = "pending" | "done" | "failed" | "skipped";
export type MemoryRunSource = "ai" | "backfill" | "manual";

/** 情节记忆：任务终态的一次复盘摘要（与任务 1:1）。 */
export interface MemoryRun {
  id: number;
  task_id: number;
  stage: string;
  scope_companies: number[];
  scope_src_target_id: number;
  status: MemoryRunStatus;
  source: MemoryRunSource;
  summary: string;
  highlights: string[];
  failures: MemoryRunFailure[];
  open_leads: string[];
  stats: Record<string, unknown>;
  reflect_error: string;
  created_at: string;
  updated_at: string;
}

/** 记忆库企业维度统计（企业选择器数据源）。 */
export interface MemoryCompanyStat {
  company_id: number;
  company_name: string;
  runs: number;
  facts_active: number;
  last_run_at: string | null;
}

/** 记忆简报（任务详情卡与完整对话框共用）。 */
export interface MemoryBriefing {
  task_id: string;
  has_content: boolean;
  text: string;
  sections: { title: string; body: string }[];
  generated_at: string;
}

export type EvolveStatus = "candidate" | "active" | "rejected" | "retired";
export type EvolveScopeKind = "global" | "company" | "src_target";
export type EvolveKind = "positive" | "negative";
export type EvolveCategory = "technique" | "tool" | "recon" | "pitfall" | "evasion" | "ops";

/** 经验洞察（ExpeL 式）：成功规律与失败教训。分级信任：candidate 不注入任务。 */
export interface EvolveInsight {
  id: number;
  scope_kind: EvolveScopeKind;
  scope_id: number;
  stack_tags: string[];
  category: EvolveCategory;
  kind: EvolveKind;
  title: string;
  content: string;
  status: EvolveStatus;
  corroboration: number;
  source_task_ids: number[];
  confidence: number;
  created_by: "agent" | "user";
  created_at: string;
  updated_at: string;
}

export interface EvolveInsightInput {
  title: string;
  content?: string;
  kind?: EvolveKind;
  category?: EvolveCategory;
  scope_kind?: EvolveScopeKind;
  scope_id?: number;
  stack_tags?: string[];
}

export interface EvolvePlaybookStep {
  n: number;
  action: string;
  detail?: string;
  notes?: string;
}

/** 攻击剧本：前置条件→步骤→成败计数→证据。人工可审计的结构化文本，不含可执行代码。 */
export interface EvolvePlaybook {
  id: number;
  title: string;
  summary: string;
  scope_kind: EvolveScopeKind;
  scope_id: number;
  stack_tags: string[];
  preconditions: Record<string, unknown>;
  steps: EvolvePlaybookStep[];
  status: EvolveStatus;
  success_count: number;
  fail_count: number;
  avg_seconds: number;
  evidence_task_ids: number[];
  source_task_ids: number[];
  corroboration: number;
  created_by: "agent" | "user";
  created_at: string;
  updated_at: string;
}

export interface EvolvePlaybookInput {
  title: string;
  summary?: string;
  scope_kind?: EvolveScopeKind;
  scope_id?: number;
  stack_tags?: string[];
  preconditions?: Record<string, unknown>;
  steps?: EvolvePlaybookStep[];
}

/** 状态 → 条数（页签计数用，不受当前筛选影响）。 */
export type EvolveStatusCounts = Record<EvolveStatus, number>;

export interface EvolveOverview {
  insights: EvolveStatusCounts;
  playbooks: EvolveStatusCounts;
}

export interface MemoryOverview {
  facts_total: number;
  facts_active: number;
  runs_total: number;
  insights: EvolveStatusCounts;
  playbooks: EvolveStatusCounts;
}

// ============ 存储中心(契约 STORAGE_CONTRACTS.md) ============

// 存储协议:s3 = SigV4 path-style(MinIO/OSS/COS/R2/AWS 通吃),webdav = 纯 HTTP 客户端。
export type StorageProtocol = "s3" | "webdav";

// 桶健康状态:degraded = 最近一次异步上桶失败;unchecked = 尚未测活。
export type StorageBucketStatus = "ok" | "degraded" | "unchecked";

export interface StorageBucket {
  id: number;
  name: string;
  protocol: StorageProtocol;
  endpoint: string;
  bucket: string;
  region: string;
  path_prefix: string;
  active: boolean;
  status: StorageBucketStatus;
  has_secret: boolean; // 密钥永不回显,只回是否已设置
  last_check_at: number | string | null;
  last_check_error: string;
  created_at: number | string;
  updated_at: number | string;
}

// 明文提交(服务端 secretbox 加密落库);secret_key 留空 = 不改旧值。
export interface StorageBucketInput {
  name: string;
  protocol: StorageProtocol;
  endpoint: string;
  bucket: string;
  region?: string;
  path_prefix?: string;
  access_key?: string;
  secret_key?: string;
  activate?: boolean;
}

// 迁移/纳管数据类别(traffic 流量体不纳管,留注册口)。
export type StorageCategory =
  | "report-jobs"
  | "report-templates"
  | "uploads"
  | "evidence"
  | "task-archives"
  | "c2"
  | "vps"
  | "wordbooks"
  | "src-platform"
  | "task-workdirs";

export interface StorageCategoryOverview {
  category: StorageCategory;
  files: number; // 本地文件数
  bytes: number; // 本地字节数
  uploaded_files: number; // 已上桶登记数
}

// 迁移作业行;status: running | done | failed | canceled。
export interface StorageMigration {
  id: number;
  bucket_id: number;
  categories: string[];
  status: "running" | "done" | "failed" | "canceled";
  total_files: number;
  done_files: number;
  failed_files: number;
  error: string;
  started_at: number | string;
  finished_at: number | string | null;
}
