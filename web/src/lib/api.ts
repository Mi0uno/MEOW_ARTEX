// Real backend client. /api/* is proxied to the Go backend (next.config rewrites).
// Returns the domain types in lib/types.ts. Shapes match the backend handlers;
// a few fields the backend serializes differently (e.g. created_at as a unix int)
// are passed through and formatted at the call site.

import type { ChatMention } from "@/lib/chat-mentions";
import { MOCK } from "@/lib/mock/enabled";
import { mockHandle } from "@/lib/mock/handler";
import type {
  ActiveFindingRetest,
  Activity,
  Agent,
  AgentDetail,
  AgentTrigger,
  ArchiveBatchItem,
  Asset,
  AssetImportResult,
  Audit,
  BatchCategoryItem,
  BatchControlItem,
  ChatAttachment,
  CollectorRun,
  CommandRecord,
  Company,
  CompanyScopeMutation,
  CompanyScopeRule,
  CompanyWithCounts,
  Conversation,
  ConvTokenSummary,
  CoverageAssetRefs,
  CoverageGraphData,
  DailyTokenBucket,
  DecisionEngine,
  DeleteTaskOptions,
  DeleteTaskResult,
  Edge,
  EvidenceBodyPreview,
  ExplorationNodePage,
  ExplorationNodeQuery,
  JevThresholds,
  JevThresholdsResponse,
  ExternalAPIConfig,
  ExternalAPIProviderDesc,
  Finding,
  FindingAssetTree,
  FindingDeepenResponse,
  FindingGroupsPage,
  FindingQuery,
  FindingRetest,
  FindingStats,
  FindingStatus,
  FindingsPage,
  FindingTraffic,
  FindingTrafficDetail,
  IntentAsset,
  InterceptApprovalFilter,
  InterceptApprovalRow,
  InterceptDetail,
  InterceptPending,
  InterceptRule,
  JudgeConfig,
  LLMPoolStatus,
  LLMProfile,
  LLMRecordDetail,
  LLMRecordItem,
  LLMRetryOverride,
  LLMRetryPolicy,
  LLMTask,
  MCPServer,
  MCPTool,
  MissingSkill,
  ModelTokenStat,
  PromptVar,
  PromptVersion,
  ProxyCheckRun,
  C2Direction,
  C2EvasionProfile,
  C2EvasionProfileInput,
  C2Job,
  C2JobKind,
  C2JobStatus,
  C2Listener,
  C2ListenerCheck,
  C2ListenerInput,
  C2Loot,
  C2LootKind,
  C2Overview,
  C2Payload,
  C2PayloadArtifact,
  C2PayloadFormat,
  C2PayloadTarget,
  C2Plugin,
  C2PluginInput,
  C2Session,
  C2Task,
  C2Transfer,
  TaskVpsEnvironment,
  VpsAssignment,
  VpsDashboard,
  VpsExecResult,
  VpsExecution,
  VpsMachine,
  VpsMachineDetail,
  VpsMachineInput,
  VpsToolDetection,
  VpsToolset,
  ProxyCountryStat,
  ProxyEgressStatus,
  ProxyEntry,
  ProxyGroup,
  ProxyProviderDef,
  ProxyRequest,
  ProxySource,
  ProxyTrafficStat,
  ProxyTrafficSummary,
  ProxyUsageStat,
  RawReconAsset,
  ReconCollectorDesc,
  ReconPlan,
  ReconPipeline,
  ReportCompanyCount,
  ReportDetail,
  ReportEvent,
  ReportJob,
  ReportSummary,
  ReportTemplate,
  SessionTokenUsage,
  Settings,
  Severity,
  SkillCall,
  SkillItem,
  SSProject,
  SSTask,
  StagedAsset,
  StagedAssetCounts,
  StagedDecideResult,
  Stats,
  SPECAIFillSuggestion,
  SRCGuardOverview,
  SRCOverview,
  SRCPlatform,
  SRCPlatformFile,
  SRCReward,
  SRCTarget,
  SRCTargetAsset,
  SRCTargetImportSource,
  SRCTargetLink,
  Task,
  VulnEntry,
  VulnPoc,
  VulnSyncRun,
  TaskArchive,
  TaskArchivePage,
  TaskAssetMutation,
  TaskAssetScopeMutation,
  TaskCategory,
  TaskConstraint,
  TaskGoal,
  TaskLLMResolutions,
  TaskNode,
  TaskScopeRow,
  TaskTemplate,
  TokenTotal,
  TokenUsage,
  Tool,
  ToolExecution,
  ToolStat,
  TrafficDetail,
  TrafficEvidenceRef,
  TrafficEvidenceRole,
  TrafficHost,
  TrafficResp,
  UpdateCheck,
  UsageStats,
  Wordbook,
  WorkspaceFile,
  WorkspaceListing,
  TaskConstraintInput,
  TaskInputParse,
} from "@/lib/types";

function getToken(): string | null {
  if (typeof window === "undefined") return null;
  return localStorage.getItem("artex_token");
}

// HTTP_TIMEOUT 是单次 API 请求的默认上限：后端挂起（锁等待、代理阻塞）时
// 相关页面表现为报错而不是永远 pending。调用方可用 init.signal 自行控制
// （此时不再叠加默认超时）；SSE/上传等长任务请显式传 signal。
const HTTP_TIMEOUT = 60_000;

export async function http<T>(path: string, init?: RequestInit): Promise<T> {
  if (MOCK) return mockHandle<T>(init?.method ?? "GET", path, init?.body ?? null);
  const token = getToken();
  const signal = init?.signal ?? AbortSignal.timeout(HTTP_TIMEOUT);
  const r = await fetch(`/api${path}`, {
    ...init,
    signal,
    headers: {
      // FormData(body 是 Blob/FormData)由浏览器自动生成带 boundary 的
      // multipart Content-Type,绝不能覆盖成 JSON——否则服务端解析失败。
      ...(init?.body instanceof FormData ? {} : init?.body ? { "Content-Type": "application/json" } : {}),
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...(init?.headers as Record<string, string> | undefined),
    },
  });
  if (r.status === 401) {
    if (typeof window !== "undefined") {
      localStorage.removeItem("artex_token");
      document.cookie = "artex_token=; path=/; max-age=0";
      window.location.href = "/login";
    }
    throw new Error("未授权");
  }
  if (!r.ok) {
    const fallback = `${init?.method ?? "GET"} ${path}: ${r.status}`;
    let message = fallback;
    try {
      const payload = (await r.json()) as { error?: unknown };
      if (typeof payload.error === "string" && payload.error.trim()) {
        message = payload.error.trim();
      }
    } catch {
      // Keep the status-based fallback for empty or non-JSON error responses.
    }
    throw new Error(message);
  }
  if (r.status === 204) return undefined as T;
  return r.json();
}

// sseUrl builds a URL for Server-Sent Events streams. SSE must NOT go through the
// Next.js dev `/api` rewrite: that proxy buffers the streamed response, so event
// frames never reach the browser (the EventSource opens but receives 0 messages).
// We therefore connect straight to the Go backend, whose CORS is open. Override
// with NEXT_PUBLIC_SSE_BASE; set it to "" to force same-origin (e.g. behind a
// production reverse proxy that flushes SSE correctly).
// Token is appended as ?token= because SSE bypasses the Next.js proxy and the
// browser does not send cookies cross-port.
// mockReport returns a canned Markdown report for the demo.
function mockReport(_task?: string): string {
  return `# MEOWFUCK 渗透测试报告 — Acme Corp

## 概览
- 范围：acme.com（含 www / admin / api / shop / vpn 子域）
- 已确认发现：6 项（高危 3 · 中危 3 · 低危 2）
- 引擎模式：exploring

## 关键发现
1. **[高] 后台默认口令** admin.acme.com admin/admin123 → 可完全接管后台。
2. **[高] SQL 注入** www.acme.com/search?q= → 可读取 acme_prod 库。
3. **[高] IDOR** api.acme.com/v1/orders?id= → 可越权读取他人订单（含手机号/地址）。
4. **[中] 反射型 XSS**、**暴露 .git 源码**、**登录无速率限制**。

## 建议
- 后台强制改密 + 启用 MFA、封禁默认口令。
- search 接口参数化查询、输出编码。
- API 增加对象级授权校验（IDOR）、更换强 JWT 密钥。

> （demo）本报告由 mock 数据生成，仅用于界面演示。`;
}

// SSE/WS 直连基址:生产环境静态导出由 Go 后端直接伺服,页面同源即后端(任意端口都对,
// 曾经写死 :8787 导致 8799 等其它端口部署时终端/SSE 全部连错地方);仅 next dev(:3000)
// 时后端才在 :8787。可用 NEXT_PUBLIC_SSE_BASE 强制覆盖(置 "" = 同源)。
function streamBase(): string {
  const env = process.env.NEXT_PUBLIC_SSE_BASE;
  if (env !== undefined) return env;
  if (typeof window === "undefined") return "";
  if (window.location.port === "3000") {
    return `${window.location.protocol}//${window.location.hostname}:8787`;
  }
  return window.location.origin;
}

export function sseUrl(path: string): string {
  const base = streamBase();
  const token = getToken();
  const sep = path.includes("?") ? "&" : "?";
  return token ? `${base}${path}${sep}token=${encodeURIComponent(token)}` : `${base}${path}`;
}

// wsUrl 与 sseUrl 同源直连 Go 后端(Next 代理不支持 WebSocket 升级),用于 Web 终端等
// 双向流。token 同样走 ?token=。
export function wsUrl(path: string): string {
  const base = streamBase();
  let wsBase: string;
  if (base === "") {
    // WebSocket 不能用相对地址,同源时显式拼 ws(s)://host。
    if (typeof window === "undefined") return path;
    wsBase = `${window.location.protocol === "https:" ? "wss:" : "ws:"}//${window.location.host}`;
  } else {
    wsBase = base.replace(/^http/, "ws");
  }
  const token = getToken();
  const sep = path.includes("?") ? "&" : "?";
  return token ? `${wsBase}${path}${sep}token=${encodeURIComponent(token)}` : `${wsBase}${path}`;
}

const get = <T>(p: string) => http<T>(p);
// httpText 取回文本正文(证据原始输出预览/下载),不做 JSON 解析。
export async function httpText(path: string, init?: RequestInit): Promise<string> {
  const token = getToken();
  const signal = AbortSignal.timeout(60_000);
  const r = await fetch(`/api${path}`, {
    ...init,
    signal,
    headers: {
      ...(init?.body ? { "Content-Type": "text/plain" } : {}),
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...(init?.headers as Record<string, string> | undefined),
    },
  });
  if (r.status === 401) throw new Error("未授权");
  if (!r.ok) throw new Error(`${path}: ${r.status}`);
  return r.text();
}
const post = <T>(p: string, body?: unknown) =>
  http<T>(p, { method: "POST", body: body ? JSON.stringify(body) : undefined });
const put = <T>(p: string, body?: unknown) =>
  http<T>(p, { method: "PUT", body: body ? JSON.stringify(body) : undefined });
const patch = <T>(p: string, body?: unknown) =>
  http<T>(p, { method: "PATCH", body: body ? JSON.stringify(body) : undefined });
const del = <T>(p: string, body?: unknown) =>
  http<T>(p, { method: "DELETE", body: body ? JSON.stringify(body) : undefined });

// Go serializes nil slices as JSON null — coerce to [].
const arr = <T>(x: T[] | null | undefined): T[] => x ?? [];
const tq = (task?: string, sep: "?" | "&" = "?") => (task ? `${sep}task=${encodeURIComponent(task)}` : "");

// findingFilterParams 把发现页的筛选条件序列化成 query string。列表 / 分组 /
// 资产树 / 导出共用同一份,新增筛选项只改这里(后端也只解析这一份)。
function findingFilterParams(q: Omit<FindingQuery, "page" | "pageSize">): URLSearchParams {
  const p = new URLSearchParams();
  if (q.severity && q.severity !== "all") p.set("severity", q.severity);
  if (q.status && q.status !== "all") p.set("status", q.status);
  if (q.vulnclass && q.vulnclass !== "all") p.set("vulnclass", q.vulnclass);
  if (q.task && q.task !== "all") p.set("task_id", q.task);
  if (q.query?.trim()) p.set("q", q.query.trim());
  if (q.sort) p.set("sort", q.sort);
  if (q.assetScope) p.set("asset_scope", q.assetScope);
  return p;
}

function interceptPageQuery(page: number, size: number, filter: InterceptApprovalFilter) {
  const query = new URLSearchParams({ page: String(page), size: String(size) });
  if (filter.status) query.set("status", filter.status);
  if (filter.decision_source) query.set("decision_source", filter.decision_source);
  return query.toString();
}

export const api = {
  // 后端应用版本号（release 时由 ldflags 注入，默认 "dev"）。
  health: () => get<{ ok: boolean; service: string; version: string }>("/health"),

  // ---- auth ----
  authStatus: () => get<{ initialized: boolean }>("/auth/status"),
  login: (username: string, password: string) => post<{ token: string }>("/auth/login", { username, password }),
  initPassword: (password: string) => post<{ token: string }>("/auth/init", { password }),
  changePassword: (oldPassword: string, newPassword: string) =>
    post<{ ok: boolean }>("/auth/change-password", { old_password: oldPassword, new_password: newPassword }),

  // ---- tasks ----
  // src=true 只拉 SRC 模式任务(src_mode 标记),SRC 模式任务页用。
  tasks: (input?: { src?: boolean }) =>
    get<{ tasks: Task[]; active: string }>(`/tasks${input?.src ? "?src=1" : ""}`).then((r) => ({
      tasks: arr(r.tasks),
      active: r.active ?? "",
    })),
  task: (id: string) => get<Task>(`/tasks/${encodeURIComponent(id)}`),
  createTask: (input: {
    name?: string;
    categoryId?: number;
    description: string;
    goal: string;
    llmProfileIds?: number[];
    sourceTaskIds?: string[];
    companyIds?: number[];
    timeoutSeconds?: number;
    seedFirstIntent?: boolean;
    planHeartbeatSeconds?: number;
    coverageEnabled?: boolean;
    stage?: "recon" | "exploit" | "post" | "report"; // recon=信息搜集步骤任务;report=报告中心 AI 识别任务(系统创建)
    srcMode?: boolean; // SRC 模式:产出计入 SRC 成果,并按平台/目标规范约束 AI 边界
    srcPlatformId?: number; // SRC 平台归属:决定注入哪套平台规范(奖励/评分/守则)
    srcTargetId?: number; // SRC 目标归属:决定注入该平台下目标的授权范围
    proxyMode?: string; // 出网方式:""=跟随全局 / "direct"=强制直连 / "pool"=强制走代理池
    proxyGroupId?: number; // 仅 pool 时有效:指定代理组;0/缺省=用全局选中的组
    guardMode?: string; // 监管门限:""=跟随全局 / "off"=本任务不跑监管 / "enforce"=强制启用
    guardRuleIds?: number[]; // 仅本任务启用这些拦截规则;空=用全部已启用规则
    vpsMachineId?: number; // 执行环境:指定机器 id(创建即指派,发生在任务启动之前)
    vpsAuto?: boolean; // 执行环境:true=自动分配一台空闲在线机器(vps_machine_id 为空时生效)
    constraints?: TaskConstraintInput[]; // 创建时写入的操作约束(建任务表单 AI 识别拆分/手填的落点)
    rewardNote?: string; // 奖励标准文本(SRC 任务识别奖励的落点;任务详情总览展示)
    stepKey?: string; // recon 单步骤:收集器 key(推荐)
    stepConfig?: Record<string, unknown>; // 步骤配置
    pipeline?: ReconPipeline; // 兼容旧多步编排;stepKey 优先
  }) =>
    post<Task>("/tasks", {
      name: input.name ?? "",
      category_id: input.categoryId ?? null,
      description: input.description,
      goal: input.goal,
      llm_profile_ids: input.llmProfileIds ?? [],
      source_task_ids: input.sourceTaskIds ?? [],
      company_ids: input.companyIds ?? [],
      timeout_seconds: input.timeoutSeconds ?? 0,
      seed_first_intent: input.seedFirstIntent ?? false,
      plan_heartbeat_seconds: input.planHeartbeatSeconds ?? 0, // 0 = 后端归一到默认 600(10min)
      coverage_enabled: input.coverageEnabled ?? true, // 默认开;false=关闭资产覆盖度功能
      stage: input.stage,
      src_mode: input.srcMode ?? false,
      src_platform_id: input.srcPlatformId ?? 0,
      src_target_id: input.srcTargetId ?? 0,
      proxy_mode: input.proxyMode ?? "",
      proxy_group_id: input.proxyGroupId ?? 0,
      guard_mode: input.guardMode ?? "",
      guard_rule_ids: input.guardRuleIds ?? [],
      vps_machine_id: input.vpsMachineId ?? 0,
      vps_auto: input.vpsAuto ?? false,
      constraints: input.constraints ?? [],
      reward_note: input.rewardNote ?? "",
      step_key: input.stepKey,
      step_config: input.stepConfig,
      pipeline: input.pipeline,
    }),
  // AI 识别拆分任务描述:把一大段描述拆成 目标/约束/精简描述(SRC 模式额外识别奖励)。
  // ⚠️ LLM 长请求:必须传 signal=AbortSignal.timeout(320_000),否则 http() 默认 60s 必超时
  // (与 src 平台 spec/ai-fill 同一套超时纪律);识别结果只预填表单,随创建请求才落库。
  parseTaskInput: (
    input: { description: string; goal?: string; srcMode?: boolean; srcPlatformId?: number; srcTargetId?: number },
    signal?: AbortSignal,
  ) =>
    http<{ result: TaskInputParse }>("/tasks/parse-input", {
      method: "POST",
      body: JSON.stringify({
        description: input.description,
        goal: input.goal ?? "",
        src_mode: input.srcMode ?? false,
        src_platform_id: input.srcPlatformId ?? 0,
        src_target_id: input.srcTargetId ?? 0,
      }),
      // 调用方不传 signal 时给 320s 兜底,而不是 http() 的默认 60s——LLM 拆长文实测 1-2 分钟。
      signal: signal ?? AbortSignal.timeout(320_000),
      // 后端包了一层 {result: ...} 信封(server/task_parse.go),在这里拆掉,
      // 调用方拿到的就是 TaskInputParse 本体。
    }).then((r) => r.result),
  taskCategories: () => get<{ categories: TaskCategory[] }>("/task-categories").then((r) => arr(r.categories)),
  // ---- 信息搜集(stage=recon)执行面 ----
  reconCollectors: () => get<{ collectors: ReconCollectorDesc[] }>("/recon/collectors").then((r) => arr(r.collectors)),
  // 自动检测可用数据源 + AI 生成查询语句(创建信息搜集任务前的预览)。
  reconPlan: (companyIds: number[], useAI = true) =>
    post<ReconPlan>("/recon/plan", { company_ids: companyIds, use_ai: useAI }),
  collectorRuns: (taskId: string) =>
    get<{ runs: CollectorRun[] }>(`/tasks/${encodeURIComponent(taskId)}/collector-runs`).then((r) => arr(r.runs)),
  stagedAssets: (taskId: string, status?: string) => {
    const q = status && status !== "all" ? `?status=${encodeURIComponent(status)}` : "";
    return get<{ assets: StagedAsset[]; counts: StagedAssetCounts }>(
      `/tasks/${encodeURIComponent(taskId)}/staged-assets${q}`,
    ).then((r) => ({ assets: arr(r.assets), counts: r.counts }));
  },
  rerunRecon: (taskId: string) =>
    post<{ id: string; rerun: boolean }>(`/tasks/${encodeURIComponent(taskId)}/recon/rerun`),
  // 暂存资产采纳/忽略:ids 省略(空数组)时作用于全部 pending。
  decideStagedAssets: (taskId: string, ids: number[], action: "adopt" | "ignore") =>
    post<StagedDecideResult>(`/tasks/${encodeURIComponent(taskId)}/staged-assets/decide`, { ids, action }),
  // 收集器原始输出(证据 blob 文本)。isTruncated=预览截断标记由调用方自行处理。
  rawEvidence: (taskId: string, runId: number) =>
    httpText(`/tasks/${encodeURIComponent(taskId)}/collector-runs/${runId}/raw`),
  // ---- 外部接口(FOFA/Hunter 配置) ----
  externalApis: () =>
    get<{ apis: ExternalAPIConfig[]; providers: ExternalAPIProviderDesc[] }>("/external-apis").then((r) => ({
      apis: arr(r.apis),
      providers: arr(r.providers),
    })),
  createExternalApi: (input: {
    provider: string;
    name?: string;
    baseUrl?: string;
    credentials: Record<string, string>;
  }) =>
    post<{ id: number }>("/external-apis", {
      provider: input.provider,
      name: input.name ?? "",
      base_url: input.baseUrl ?? "",
      credentials: input.credentials,
    }),
  updateExternalApi: (
    id: number,
    input: { name?: string; baseUrl?: string; credentials?: Record<string, string>; enabled?: boolean },
  ) =>
    patch<{ ok: boolean }>(`/external-apis/${id}`, {
      name: input.name,
      base_url: input.baseUrl,
      credentials: input.credentials,
      enabled: input.enabled,
    }),
  deleteExternalApi: (id: number) => del<{ deleted: boolean }>(`/external-apis/${id}`),
  testExternalApi: (id: number) =>
    post<{ ok: boolean; message: string; quota_info?: Record<string, unknown> }>(`/external-apis/${id}/test`),
  // ---- 工具手动执行(信息搜集) ----
  reconExecute: (input: { tool: string; config: Record<string, unknown>; companyId?: number }) =>
    post<{
      id: number;
      status: "done" | "failed" | string;
      assets?: RawReconAsset[];
      stats?: Record<string, unknown>;
      raw_evidence_hash?: string;
      error?: string;
    }>("/recon/execute", { tool: input.tool, config: input.config, company_id: input.companyId }),
  reconExecutions: (limit = 50) =>
    get<{ executions: ToolExecution[] }>(`/recon/execute?limit=${limit}`).then((r) => arr(r.executions)),
  // ---- 字典库 ----
  wordbooks: () => get<{ wordbooks: Wordbook[] }>("/recon/wordbooks").then((r) => arr(r.wordbooks)),
  uploadWordbook: (file: File, name: string, kind: string, note?: string) => {
    const fd = new FormData();
    fd.append("file", file);
    fd.append("name", name);
    fd.append("kind", kind);
    if (note) fd.append("note", note);
    const token = getToken();
    return fetch(`/api/recon/wordbooks`, {
      method: "POST",
      headers: token ? { Authorization: `Bearer ${token}` } : undefined,
      body: fd,
    }).then(async (r) => {
      if (!r.ok) throw new Error(await r.text());
      return r.json() as Promise<{ id: number; line_count: number }>;
    });
  },
  deleteWordbook: (id: number) => del<{ deleted: boolean }>(`/recon/wordbooks/${id}`),
  wordbookPreview: (id: number, limit = 30) =>
    get<{ lines: string[]; truncated: boolean }>(`/recon/wordbooks/${id}/preview?limit=${limit}`),
  wordbookContent: (id: number) => httpText(`/recon/wordbooks/${id}/content`),
  saveWordbookContent: (id: number, content: string) =>
    httpText(`/recon/wordbooks/${id}/content`, { method: "PUT", body: content }),
  updateTask: (id: string, input: { name?: string; pinned?: boolean }) => patch<Task>(`/tasks/${id}`, input),
  renameTask: (id: string, name: string) => patch<Task>(`/tasks/${id}`, { name }),
  pinTask: (id: string, pinned: boolean) => patch<Task>(`/tasks/${id}`, { pinned }),
  // 任务级监管门限:''=跟随全局 / 'off'=本任务不跑监管 / 'enforce'=强制启用。
  // ruleIds 空数组=用全部已启用规则。
  updateTaskGuard: (id: string, guardMode: string, guardRuleIds: number[] = []) =>
    patch<Task>(`/tasks/${id}`, { guard_mode: guardMode, guard_rule_ids: guardRuleIds }),
  createTaskCategory: (name: string) => post<TaskCategory>("/task-categories", { name }),
  renameTaskCategory: (id: number, name: string) => patch<TaskCategory>(`/task-categories/${id}`, { name }),
  deleteTaskCategory: (id: number) => del<{ deleted: number }>(`/task-categories/${id}`),
  updateTaskCategory: (taskId: string, categoryId?: number) =>
    patch<Task>(`/tasks/${taskId}/category`, { category_id: categoryId ?? null }),
  // categoryId 省略/undefined = 移出分类（后端收到 null）
  updateTasksCategory: (taskIds: string[], categoryId?: number) =>
    post<{ items: BatchCategoryItem[]; category: TaskCategory | null }>("/tasks/category/batch", {
      task_ids: taskIds,
      category_id: categoryId ?? null,
    }),
  taskTemplates: () => get<{ templates: TaskTemplate[] }>("/task-templates").then((r) => arr(r.templates)),
  createTaskTemplate: (input: Pick<TaskTemplate, "name" | "description" | "goal">) =>
    post<TaskTemplate>("/task-templates", input),
  updateTaskTemplate: (id: number, input: Partial<Pick<TaskTemplate, "name" | "description" | "goal">>) =>
    patch<TaskTemplate>(`/task-templates/${id}`, input),
  deleteTaskTemplate: (id: number) => del<{ deleted: number }>(`/task-templates/${id}`),
  updateTaskLLMProfiles: (id: string, llmProfileIds: number[], activeLLMProfileId?: number) =>
    put<{
      id: string;
      llm_profile_ids: number[];
      active_llm_profile_id?: number;
      llm_failover_state: string;
      reopened_intents: number;
      switch_event?: Activity;
    }>(`/tasks/${id}/llm`, {
      llm_profile_ids: llmProfileIds,
      active_llm_profile_id: activeLLMProfileId ?? null,
    }),
  deleteTask: (id: string, options: DeleteTaskOptions) => del<DeleteTaskResult>(`/tasks/${id}`, options),
  controlTask: (id: string, action: "pause" | "resume") =>
    post<{ id: string; paused: boolean; queued: boolean; status: string }>(`/tasks/${id}/control`, { action }),
  controlTasksBatch: (taskIds: string[], action: "pause" | "resume") =>
    post<{ items: BatchControlItem[] }>("/tasks/control/batch", { task_ids: taskIds, action }),
  taskArchives: (input?: { page?: number; size?: number; q?: string; state?: string }) => {
    const query = new URLSearchParams();
    query.set("page", String(input?.page ?? 1));
    query.set("size", String(input?.size ?? 20));
    if (input?.q) query.set("q", input.q);
    if (input?.state) query.set("state", input.state);
    return get<TaskArchivePage>(`/task-archives?${query.toString()}`).then((response) => ({
      ...response,
      items: arr(response.items),
    }));
  },
  taskArchive: (id: number) => get<TaskArchive>(`/task-archives/${id}`),
  archiveTask: (id: string) => post<TaskArchive>(`/tasks/${id}/archive`),
  archiveTasks: (taskIds: string[]) =>
    post<{ items: ArchiveBatchItem[] }>("/tasks/archive/batch", { task_ids: taskIds }),
  restoreTaskArchive: (id: number) => post<TaskArchive>(`/task-archives/${id}/restore`),
  restoreTaskArchives: (archiveIds: number[]) =>
    post<{ items: ArchiveBatchItem[] }>("/task-archives/restore/batch", { archive_ids: archiveIds }),
  deleteTaskArchive: (id: number) => del<TaskArchive>(`/task-archives/${id}`),
  deleteTaskArchives: (archiveIds: number[]) =>
    post<{ items: ArchiveBatchItem[] }>("/task-archives/delete/batch", { archive_ids: archiveIds }),
  controlIntent: (
    taskId: string,
    intentId: string,
    action: "pause" | "resume" | "cancel",
    reason?: string,
    // cancel 专用:soft(默认,假删除,意图置 deleted + 记删除原因)| hard(真删除,级联移除独占子孙)。
    mode?: "soft" | "hard",
  ) =>
    post<{
      id: number;
      state: "paused" | "open" | "deleted" | "";
      deleted?: { intents: number; facts: number; findings: number; activities: number };
    }>(`/tasks/${taskId}/intents/${intentId}/control`, { action, reason: reason ?? "", mode: mode ?? "soft" }),
  sendWorkerMessage: (taskId: string, intentId: string, message: string, requestId: string) =>
    post<{
      id: number;
      state: "running";
      accepted: true;
      request_id: string;
    }>(`/tasks/${taskId}/intents/${intentId}/messages`, { message, request_id: requestId }),
  taskLLMResolution: (id: string) => get<TaskLLMResolutions>(`/tasks/${id}/llm/resolution`),
  // 重跑一条没跑成功的意图(blocked/exhausted/stopped)：置回 open，worker 会重新认领、从头再跑。
  rerunIntent: (taskId: string, intentId: string) =>
    post<{ id: string; reopened: number }>(`/tasks/${taskId}/intents/${intentId}/rerun`),
  // 批量重跑本任务全部 blocked 意图（一次网络/LLM 断连导致多条 blocked 时一键全部重试）。
  rerunBlocked: (taskId: string) => post<{ id: string; reopened: number }>(`/tasks/${taskId}/intents/rerun-blocked`),
  setActive: (id: string) => post<{ active: string }>("/active", { id }),
  // ---- stats ----
  stats: (task?: string) => get<Stats>(`/stats${tq(task)}`),
  // 资产测试覆盖度(粗估，供参考)：范围内资产被 fact 碰过的占比 + 按类型的 总数/已测。
  taskCoverage: (id: string) =>
    get<{
      enabled: boolean; // 资产覆盖度功能是否开启；false 时其余字段为零值
      scope_rows: number;
      denominator: number;
      tested: number;
      pct: number | null;
      by_type: { type: string; total: number; tested: number }[];
    }>(`/tasks/${id}/coverage`),
  // 资产覆盖图：范围内全部资产 + 连接用的根域名/公司节点，含 tested/in_scope。
  taskCoverageGraph: (id: string) => get<CoverageGraphData>(`/tasks/${id}/coverage-graph`),
  // 全局 llm_usage 聚合（仪表盘新版 token 视图）：按 profile 总量 + 按天分桶。
  usageStats: (days = 365) => get<UsageStats>(`/tokens/usage?days=${days}`),
  // ---- 目标管理（总览）----
  // 本任务全部目标（text/vulnclass/state）。
  taskGoals: (id: string) =>
    get<{ goals: TaskGoal[] | null }>(`/tasks/${id}/goals`).then((response) => ({ goals: arr(response.goals) })),
  // 人工新增目标：写入图谱并通知 planner、复活任务。
  addGoal: (id: string, text: string, vulnclass?: string) =>
    post<TaskGoal>(`/tasks/${id}/goals`, { text, vulnclass: vulnclass ?? "" }),
  // 人工修改目标文本（及 vulnclass）：通知 planner「由 old 变为 new」、复活任务。
  updateGoal: (id: string, goalId: string, text: string, vulnclass?: string) =>
    patch<TaskGoal>(`/tasks/${id}/goals/${goalId}`, { text, vulnclass: vulnclass ?? "" }),
  // 人工删除目标（硬删除）：通知 planner，删除不复活任务。
  deleteGoal: (id: string, goalId: string) => del<{ ok: boolean }>(`/tasks/${id}/goals/${goalId}`),

  // ---- 操作约束管理（总览）----
  // 本任务全部操作约束（allow/deny）。
  taskConstraints: (id: string) =>
    get<{ constraints: TaskConstraint[] | null }>(`/tasks/${id}/constraints`).then((response) => ({
      constraints: arr(response.constraints),
    })),
  // 新增约束（不通知 planner，下一轮规划自然读到）。
  addConstraint: (id: string, text: string, kind: TaskConstraint["kind"]) =>
    post<TaskConstraint>(`/tasks/${id}/constraints`, { text, kind }),
  // 修改约束（文本 + allow/deny）。
  updateConstraint: (id: string, constraintId: string, text: string, kind: TaskConstraint["kind"]) =>
    patch<TaskConstraint>(`/tasks/${id}/constraints/${constraintId}`, { text, kind }),
  // 删除约束。
  deleteConstraint: (id: string, constraintId: string) =>
    del<{ ok: boolean }>(`/tasks/${id}/constraints/${constraintId}`),

  // 本任务测试范围列表（含继承自来源任务的范围）。
  taskScope: (id: string) => get<{ scope: TaskScopeRow[] }>(`/tasks/${id}/scope`),
  // 手动新增一条测试范围（kind=company/root_domain/subdomain/ip/cidr）。
  addTaskScope: (id: string, kind: string, value: string, reason?: string) =>
    post<TaskScopeRow>(`/tasks/${id}/scope`, { kind, value, reason }),
  // 删除本任务的一条测试范围。
  deleteTaskScope: (id: string, scopeId: number) => del<{ ok: boolean }>(`/tasks/${id}/scope/${scopeId}`),
  // 某资产在本任务里关联的意图 / 事实 / 发现（覆盖图节点抽屉用）。
  taskAssetRefs: (id: string, assetId: number) => get<CoverageAssetRefs>(`/tasks/${id}/asset-refs?asset_id=${assetId}`),

  // ---- workspace file manager (workDir) ----
  workspaceList: (path = "") => get<WorkspaceListing>(`/workspace/list?path=${encodeURIComponent(path)}`),
  workspaceRead: (path: string) => get<WorkspaceFile>(`/workspace/read?path=${encodeURIComponent(path)}`),
  workspaceWrite: (path: string, content: string) =>
    post<{ ok: boolean; path: string }>(`/workspace/write`, { path, content }),
  workspaceMkdir: (path: string) => post<{ ok: boolean; path: string }>(`/workspace/mkdir`, { path }),
  workspaceDelete: (path: string) => del<{ ok: boolean }>(`/workspace/delete?path=${encodeURIComponent(path)}`),
  workspaceUpload: async (dir: string, files: File[]) => {
    if (MOCK) return { uploaded: files.length };
    const fd = new FormData();
    for (const f of files) fd.append("file", f);
    const token = getToken();
    const r = await fetch(`/api/workspace/upload?path=${encodeURIComponent(dir)}`, {
      method: "POST",
      headers: { ...(token ? { Authorization: `Bearer ${token}` } : {}) },
      body: fd,
    });
    if (!r.ok) throw new Error(`上传失败: ${r.status}`);
    return r.json() as Promise<{ uploaded: number }>;
  },
  workspaceDownload: async (path: string) => {
    let blob: Blob;
    if (MOCK) {
      blob = new Blob([`（demo）${path} 的下载内容示例。`], { type: "text/plain" });
    } else {
      const token = getToken();
      const r = await fetch(`/api/workspace/download?path=${encodeURIComponent(path)}`, {
        headers: { ...(token ? { Authorization: `Bearer ${token}` } : {}) },
      });
      if (!r.ok) throw new Error(`下载失败: ${r.status}`);
      blob = await r.blob();
    }
    const objUrl = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = objUrl;
    a.download = path.split("/").pop() || "download";
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(objUrl);
  },

  // ---- assets ----
  // Server-side paginated: pass limit/offset, get back the page + full match total.
  assets: (type = "", limit = 50, offset = 0) =>
    get<{ count: number; total: number; assets: Asset[] }>(`/assets?type=${type}&limit=${limit}&offset=${offset}`).then(
      (r) => ({ assets: r?.assets ?? [], total: r?.total ?? r?.count ?? 0 }),
    ),
  assetsByCompany: (companyId: number, type = "", limit = 2000, offset = 0) =>
    get<{ count: number; total: number; assets: Asset[] }>(
      `/assets?company_id=${companyId}${type ? `&type=${encodeURIComponent(type)}` : ""}&limit=${limit}&offset=${offset}`,
    ).then((r) => ({ assets: r?.assets ?? [], total: r?.total ?? r?.count ?? 0 })),
  searchAssets: (dsl: string, type = "", limit = 50, offset = 0) =>
    get<{ count: number; total: number; assets: Asset[] }>(
      `/assets?dsl=${encodeURIComponent(dsl)}${type ? `&type=${encodeURIComponent(type)}` : ""}&limit=${limit}&offset=${offset}`,
    ).then((r) => ({ assets: r?.assets ?? [], total: r?.total ?? r?.count ?? 0 })),
  assetCounts: (taskId = "") =>
    get<Record<string, number>>(`/assets/counts${taskId ? `?task_id=${encodeURIComponent(taskId)}` : ""}`),
  // 导入资产文件（txt/csv/xlsx，≤10MB）：后端解析入库并按各企业资产范围自动归属。
  // FormData 直接走 http()——helper 对 FormData 不会覆盖 multipart Content-Type（见其内部注释）。
  // 传 companyId 时把本次导入的资产强制归属到该企业（企业详情页「导入资产」用），
  // 不传则走后端范围自动认领；task_id 均不传（本次不做任务关联）。
  importAssets: (file: File, companyId?: number) => {
    const fd = new FormData();
    fd.append("file", file);
    if (companyId && companyId > 0) fd.append("company_id", String(companyId));
    return http<AssetImportResult>("/assets/import", { method: "POST", body: fd });
  },
  deleteAssets: (ids: number[]) =>
    http<{ deleted: number }>("/assets", { method: "DELETE", body: JSON.stringify({ ids }) }),
  // task-scoped view of the same endpoint — server-side paginated like `assets`
  taskAssets: (taskId: string, type = "", limit = 50, offset = 0) =>
    get<{ count: number; total: number; assets: Asset[] }>(
      `/assets?task_id=${encodeURIComponent(taskId)}&type=${encodeURIComponent(type)}&limit=${limit}&offset=${offset}`,
    ).then((r) => ({ assets: r?.assets ?? [], total: r?.total ?? r?.count ?? 0 })),
  // DSL search scoped to a task — the backend forces the task_id filter, so it
  // always stays within that task's assets (same DSL grammar as `searchAssets`).
  searchTaskAssets: (taskId: string, dsl: string, type = "", limit = 50, offset = 0) =>
    get<{ count: number; total: number; assets: Asset[] }>(
      `/assets?task_id=${encodeURIComponent(taskId)}&dsl=${encodeURIComponent(dsl)}&type=${encodeURIComponent(type)}&limit=${limit}&offset=${offset}`,
    ).then((r) => ({ assets: r?.assets ?? [], total: r?.total ?? r?.count ?? 0 })),
  attachTaskAssets: (taskId: string, assetIds: number[], sourceSummary: string) =>
    post<TaskAssetMutation>(`/tasks/${taskId}/assets`, {
      asset_ids: assetIds,
      source_summary: sourceSummary,
    }),
  registerTaskAssetScopes: (taskId: string, scope: CompanyScopeRule[]) =>
    post<TaskAssetScopeMutation>(`/tasks/${taskId}/assets`, { scope }),
  detachTaskAsset: (taskId: string, assetId: number) => del<{ detached: number }>(`/tasks/${taskId}/assets/${assetId}`),
  taskIntentAssets: (taskId: string) =>
    get<{ assets: IntentAsset[] }>(`/tasks/${taskId}/intent-assets`).then((r) => arr(r.assets)),

  // ---- companies (企业 + 资产范围；归属唯一来源) ----
  companies: () => get<Company[]>("/companies").then(arr),
  // 企业列表页(「先选资产主体」):服务端搜索/范围类型筛选/排序/分页。
  companiesPage: (input?: {
    q?: string;
    kind?: string;
    sort?: "name" | "assets" | "tasks" | "reports" | "updated";
    desc?: boolean;
    limit?: number;
    offset?: number;
  }) => {
    const p = new URLSearchParams();
    if (input?.q) p.set("q", input.q);
    if (input?.kind) p.set("kind", input.kind);
    if (input?.sort) p.set("sort", input.sort);
    if (input?.desc) p.set("desc", "1");
    p.set("limit", String(input?.limit ?? 20));
    p.set("offset", String(input?.offset ?? 0));
    return get<{ companies: CompanyWithCounts[]; total: number }>(`/companies?${p.toString()}`).then((r) => ({
      companies: arr(r.companies),
      total: r.total ?? 0,
    }));
  },
  createCompany: (name: string, scope: CompanyScopeRule[]) =>
    post<{ id: number; created: boolean; scope_added?: number; scope_invalid?: number; scope_errors?: string[] }>(
      "/companies",
      {
        name,
        scope,
      },
    ),
  addCompanyScope: (id: number, scope: CompanyScopeRule[], reason = "") =>
    post<CompanyScopeMutation>(`/companies/${id}/scope`, {
      scope,
      reason,
    }),
  updateCompanyScope: (id: number, scope: CompanyScopeRule[], reason = "") =>
    post<CompanyScopeMutation>(`/companies/${id}/scope`, {
      scope,
      reason,
      reset: true,
    }),
  deleteCompany: (id: number, deleteAssets = false) =>
    http<{ deleted: number; assets_deleted: number }>(`/companies/${id}`, {
      method: "DELETE",
      body: JSON.stringify({ delete_assets: deleteAssets }),
    }),

  // ---- exploration (per task) ----
  frontier: (task?: string) => get<TaskNode[]>(`/exploration/frontier${tq(task)}`).then(arr),
  findings: (task?: string) => get<Finding[]>(`/exploration/findings${tq(task)}`).then(arr),
  findingsPage: (q: FindingQuery) => {
    const p = findingFilterParams(q);
    p.set("page", String(q.page));
    p.set("limit", String(q.pageSize));
    return get<FindingsPage>(`/exploration/findings?${p.toString()}`);
  },
  findingGroups: (q: FindingQuery) => {
    const p = findingFilterParams(q);
    p.set("page", String(q.page));
    p.set("limit", String(q.pageSize));
    return get<FindingGroupsPage>(`/exploration/findings/groups?${p.toString()}`);
  },
  // findingAssetTree 取「按资产」视图的左侧树:只含有发现的资产及其祖先,
  // 节点带子树聚合计数。不分页——树是导航结构,一次取完。
  findingAssetTree: (q: Omit<FindingQuery, "page" | "pageSize">) =>
    get<FindingAssetTree>(`/exploration/findings/asset-tree?${findingFilterParams(q).toString()}`),
  findingStats: () => get<FindingStats>("/exploration/findings/stats"),
  // exportFindings 触发发现页导出并下载文件。scope=selected 时传 ids(finding_id 列表);
  // scope=filtered 时传当前筛选(沿用 FindingQuery 的筛选字段);scope=all 忽略筛选。
  exportFindings: async (opts: {
    format: "md-single" | "md-zip" | "csv" | "json";
    scope: "filtered" | "all" | "selected";
    filters?: Omit<FindingQuery, "page" | "pageSize">;
    ids?: string[];
  }) => {
    const p = new URLSearchParams({ format: opts.format, scope: opts.scope });
    if (opts.scope === "selected") {
      p.set("ids", (opts.ids ?? []).join(","));
    } else if (opts.scope === "filtered" && opts.filters) {
      for (const [k, v] of findingFilterParams(opts.filters)) p.set(k, v);
    }
    const token = getToken();
    const r = await fetch(`/api/exploration/findings/export?${p.toString()}`, {
      headers: token ? { Authorization: `Bearer ${token}` } : {},
    });
    if (!r.ok) throw new Error(`export: ${r.status}`);
    const blob = await r.blob();
    // 文件名优先取后端 Content-Disposition,取不到则用默认名。
    const disp = r.headers.get("Content-Disposition") ?? "";
    const m = disp.match(/filename="?([^"]+)"?/);
    const filename = m?.[1] ?? `findings-export`;
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
  },
  getFinding: (id: string, contextTaskId?: string) =>
    get<Finding>(
      `/exploration/findings/${id}${contextTaskId ? `?context_task=${encodeURIComponent(contextTaskId)}` : ""}`,
    ),
  findingTraffic: (id: string, contextTask?: string) =>
    get<FindingTraffic>(
      `/exploration/findings/${id}/traffic${contextTask ? `?context_task=${encodeURIComponent(contextTask)}` : ""}`,
    ),
  bindFindingTraffic: (id: string, traffic_refs: TrafficEvidenceRef[], contextTask?: string) =>
    post<FindingTraffic>(
      `/exploration/findings/${id}/traffic${contextTask ? `?context_task=${encodeURIComponent(contextTask)}` : ""}`,
      { traffic_refs },
    ),
  editFindingTraffic: (
    id: string,
    bindingId: string,
    version: number,
    fields: { role: TrafficEvidenceRole; note: string },
    contextTask?: string,
  ) =>
    patch<FindingTraffic>(
      `/exploration/findings/${id}/traffic/${bindingId}${contextTask ? `?context_task=${encodeURIComponent(contextTask)}` : ""}`,
      { version, ...fields },
    ),
  removeFindingTraffic: (id: string, bindingId: string, version: number, contextTask?: string) =>
    del<FindingTraffic>(
      `/exploration/findings/${id}/traffic/${bindingId}${contextTask ? `?context_task=${encodeURIComponent(contextTask)}` : ""}`,
      { version },
    ),
  orderFindingTraffic: (id: string, binding_ids: string[], version: number, contextTask?: string) =>
    http<FindingTraffic>(
      `/exploration/findings/${id}/traffic/order${contextTask ? `?context_task=${encodeURIComponent(contextTask)}` : ""}`,
      { method: "PUT", body: JSON.stringify({ binding_ids, version }) },
    ),
  findingTrafficDetail: (id: string, bindingId: string, contextTask?: string) =>
    get<FindingTrafficDetail>(
      `/exploration/findings/${id}/traffic/${bindingId}${contextTask ? `?context_task=${encodeURIComponent(contextTask)}` : ""}`,
    ),
  findingTrafficBody: (
    id: string,
    bindingId: string,
    side: "request" | "response",
    offset: number,
    contextTask?: string,
  ) =>
    get<EvidenceBodyPreview>(
      `/exploration/findings/${id}/traffic/${bindingId}/body?side=${side}&offset=${offset}${contextTask ? `&context_task=${encodeURIComponent(contextTask)}` : ""}`,
    ),
  downloadFindingTrafficBody: async (
    id: string,
    bindingId: string,
    side: "request" | "response",
    contextTask?: string,
  ) => {
    const token = getToken();
    const response = await fetch(
      `/api/exploration/findings/${id}/traffic/${bindingId}/body?side=${side}&download=1${contextTask ? `&context_task=${encodeURIComponent(contextTask)}` : ""}`,
      { headers: token ? { Authorization: `Bearer ${token}` } : {} },
    );
    if (!response.ok) {
      const error = await response.json().catch(() => ({ error: "下载失败" }));
      throw new Error(error.error ?? "下载失败");
    }
    const blob = await response.blob();
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `evidence-${bindingId}-${side}.bin`;
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  },
  // 漏洞链路:该漏洞节点回溯到任务初始节点的子图(节点 + 关系)。
  findingLineage: (id: string) => get<{ nodes: TaskNode[]; edges: Edge[] }>(`/exploration/findings/${id}/lineage`),
  setFindingStatus: (id: string, status: FindingStatus) => patch<Finding>(`/exploration/findings/${id}`, { status }),
  setFindingSeverity: (id: string, severity: Severity) => patch<Finding>(`/exploration/findings/${id}`, { severity }),
  // 一次保存漏洞的名称/类别/严重等级(发现列表行内编辑用),只传出现的字段。
  updateFinding: (
    id: string,
    fields: { name?: string; vulnclass?: string; severity?: Severity; status?: FindingStatus },
  ) => patch<Finding>(`/exploration/findings/${id}`, fields),
  // 删除漏洞:移除 findings 记录 + 来源探索节点(从发现列表/任务发现 Tab/探索图一并消失)。
  deleteFinding: (id: string) => del<{ deleted: boolean; id: number }>(`/exploration/findings/${id}`),
  findingRetests: (id: string) =>
    get<{ retests: FindingRetest[] }>(`/exploration/findings/${encodeURIComponent(id)}/retests`).then((r) =>
      arr(r.retests),
    ),
  activeFindingRetests: () =>
    get<{ retests: ActiveFindingRetest[] }>("/exploration/findings/retests/active").then((r) => arr(r.retests)),
  startFindingRetest: (id: string, notes: string) =>
    post<{ retest: FindingRetest; created: boolean }>(`/exploration/findings/${encodeURIComponent(id)}/retests`, {
      notes,
    }),
  deepenFinding: (id: string, description: string) =>
    post<FindingDeepenResponse>(`/exploration/findings/${id}/deepen`, { description }),
  intents: (task?: string) => get<TaskNode[]>(`/exploration/intents${tq(task)}`).then(arr),
  tokenStats: (task?: string) =>
    get<{ workers: TokenUsage[]; sessions: SessionTokenUsage[]; total: TokenTotal }>(
      `/exploration/tokens${tq(task)}`,
    ).then((r) => ({
      workers: arr(r.workers),
      sessions: arr(r.sessions),
      total: r.total,
    })),
  tokenDaily: (days = 30) => get<DailyTokenBucket[]>(`/tokens/daily?days=${days}`).then(arr),
  conversationTokens: () =>
    get<ConvTokenSummary[]>("/tokens/conversations")
      .then(arr)
      .catch(() => [] as ConvTokenSummary[]),
  explorationGraph: (task?: string) => get<{ nodes: TaskNode[]; edges: Edge[] }>(`/exploration/graph${tq(task)}`),
  // 播报板:服务端按创建顺序分页的探索节点(默认最新在前)。
  explorationNodes: (task: string, query: ExplorationNodeQuery = {}) => {
    const q = new URLSearchParams();
    if (task) q.set("task", task);
    q.set("page", String(query.page ?? 1));
    q.set("size", String(query.size ?? 20));
    if (query.kinds?.length) q.set("kind", query.kinds.join(","));
    if (query.states?.length) q.set("state", query.states.join(","));
    if (query.q?.trim()) q.set("q", query.q.trim());
    if (query.order === "asc") q.set("order", "asc");
    return get<ExplorationNodePage>(`/exploration/nodes?${q.toString()}`).then((r) => ({
      items: arr(r.items),
      total: r.total ?? 0,
      page: r.page ?? 1,
      size: r.size ?? query.size ?? 20,
      edges: arr(r.edges),
      refs: r.refs ?? {},
      assets: r.assets ?? {},
    }));
  },
  activity: (task?: string, opts?: { intent?: string; since?: number; limit?: number }) => {
    const q = new URLSearchParams();
    if (task) q.set("task", task);
    if (opts?.intent) q.set("intent", opts.intent);
    if (opts?.since) q.set("since", String(opts.since));
    if (opts?.limit) q.set("limit", String(opts.limit));
    return get<{ items: Activity[]; cursor: number }>(`/exploration/activity?${q.toString()}`).then((r) => ({
      items: arr(r.items),
      cursor: r.cursor ?? 0,
    }));
  },
  activityDetail: (id: number, task?: string) => get<{ detail: string }>(`/exploration/activity/${id}${tq(task)}`),
  // Reverse-paginated session history. session = "main" | "plan" | "intent:<id>".
  // before=0 → latest page; before=<id> → the older page ending before that id.
  // snapshotCursor is the TASK-level max id at query time — open the task SSE at
  // since=snapshotCursor so history (id≤cursor) and the live tail (id>cursor) meet
  // gap-free. hasMore = still-older steps exist (drives scroll-up loading).
  activityHistory: (task: string, session: string, before = 0, limit = 200) => {
    const q = new URLSearchParams({ session, limit: String(limit) });
    if (task) q.set("task", task);
    if (before > 0) q.set("before", String(before));
    return get<{ items: Activity[]; snapshot_cursor: number; earliest_cursor: number; has_more: boolean }>(
      `/exploration/activity/history?${q.toString()}`,
    ).then((r) => ({
      items: arr(r.items),
      snapshotCursor: r.snapshot_cursor ?? 0,
      earliestCursor: r.earliest_cursor ?? 0,
      hasMore: !!r.has_more,
    }));
  },
  // Paged worker (intent) session list — reaches past the legacy fixed 300 cap.
  // before=0 → newest page; before=<id> → older page. has_more = older intents exist.
  intentsPage: (task: string, before = 0, limit = 300) => {
    const q = new URLSearchParams({ limit: String(limit) });
    if (task) q.set("task", task);
    q.set("before", String(before > 0 ? before : 0));
    q.set("page", "1"); // marker so the backend returns the paged {items,has_more} shape
    return get<{ items: TaskNode[]; has_more: boolean }>(`/exploration/intents?${q.toString()}`).then((r) => ({
      items: arr(r.items),
      hasMore: !!r.has_more,
    }));
  },

  // Main-agent conversation segments of a task. Each segment is a resettable session
  // (clean transcript/context) over the same task; `current` is the writable one.
  mainSessions: (task: string) =>
    get<{ sessions: { seq: number; created_at: string }[]; current: number }>(
      `/exploration/main-sessions${tq(task)}`,
    ).then((r) => ({ sessions: arr(r.sessions), current: r.current ?? 0 })),
  // Start a fresh main-agent session segment (does not touch the task's graph/assets/goal).
  newMainSession: (task: string) =>
    post<{ seq: number; created_at: string; current: number }>(`/exploration/main-session/new${tq(task)}`),

  // ---- traffic / audit / report / chat ----
  audit: (task?: string) => get<Audit>(`/audit${tq(task)}`),
  traffic: (page = 0, size = 100, host = "", method = "", q = "") =>
    get<TrafficResp>(
      `/traffic?page=${page}&size=${size}` +
        (host ? `&host=${encodeURIComponent(host)}` : "") +
        (method && method !== "all" ? `&method=${encodeURIComponent(method)}` : "") +
        (q ? `&q=${encodeURIComponent(q)}` : ""),
    ),
  trafficExchange: (id: string) => get<TrafficDetail>(`/traffic/exchange?id=${encodeURIComponent(id)}`),
  trafficHosts: () => get<{ hosts: TrafficHost[] }>(`/traffic/hosts`),
  trafficDeleteHost: (host: string) => del<{ deleted: number }>(`/traffic?host=${encodeURIComponent(host)}`),
  trafficDeleteHosts: (hosts: string[]) => del<{ deleted: number }>(`/traffic/hosts`, { hosts }),

  // ---- app settings (runtime toggles) ----
  settings: () => get<Settings>(`/settings`),
  setSettings: (patch: Partial<Settings>) => put<Settings>(`/settings`, patch),
  // Run a real "test" search with the given (or saved) config to verify it works.
  testWebSearch: (patch: {
    web_search_backend?: string;
    web_search_proxy?: string;
    brave_search_api_key?: string;
    tavily_search_api_key?: string;
  }) => post<{ ok: boolean; error?: string; count?: number; backend?: string }>(`/settings/web-search/test`, patch),
  report: async (task?: string) => {
    if (MOCK) return mockReport(task);
    const token = getToken();
    const r = await fetch(`/api/report${tq(task)}`, {
      headers: token ? { Authorization: `Bearer ${token}` } : {},
    });
    if (!r.ok) throw new Error(`report: ${r.status}`);
    return r.text();
  },
  // ---- 漏洞库 ----
  vulnEntries: (input?: {
    category?: string;
    categories?: string[];
    severity?: string;
    status?: string;
    source?: string;
    q?: string;
    taskId?: number;
    hasPoc?: string;
    kev?: string;
    sort?: string;
    limit?: number;
    offset?: number;
  }) => {
    const p = new URLSearchParams();
    if (input?.category) p.set("category", input.category);
    if (input?.categories?.length) p.set("categories", input.categories.join(","));
    if (input?.severity) p.set("severity", input.severity);
    if (input?.status) p.set("status", input.status);
    if (input?.source) p.set("source", input.source);
    if (input?.q) p.set("q", input.q);
    if (input?.taskId) p.set("task_id", String(input.taskId));
    if (input?.hasPoc) p.set("has_poc", input.hasPoc);
    if (input?.kev && input.kev !== "all") p.set("kev", input.kev);
    if (input?.sort) p.set("sort", input.sort);
    p.set("limit", String(input?.limit ?? 50));
    p.set("offset", String(input?.offset ?? 0));
    return get<{ entries: VulnEntry[]; total: number; counts: Record<string, number>; sev_counts?: Record<string, number> }>(
      `/vulndb/entries?${p.toString()}`,
    ).then((r) => ({ entries: arr(r.entries), total: r.total, counts: r.counts ?? {}, sevCounts: r.sev_counts ?? {} }));
  },
  vulnEntry: (id: number) =>
    get<{ entry: VulnEntry; pocs: VulnPoc[] }>(`/vulndb/entries/${id}`).then((r) => ({
      entry: r.entry,
      pocs: arr(r.pocs),
    })),
  createVulnEntry: (input: Partial<VulnEntry>) => post<{ entry: VulnEntry }>("/vulndb/entries", input),
  updateVulnEntry: (id: number, input: Partial<VulnEntry>) =>
    put<{ entry: VulnEntry }>(`/vulndb/entries/${id}`, input),
  deleteVulnEntry: (id: number) => del<{ deleted: boolean }>(`/vulndb/entries/${id}`),
  createVulnPoc: (entryId: number, input: Partial<VulnPoc>) =>
    post<{ poc: VulnPoc }>(`/vulndb/entries/${entryId}/pocs`, input),
  updateVulnPoc: (id: number, input: Partial<VulnPoc>) => put<{ ok: boolean }>(`/vulndb/pocs/${id}`, input),
  deleteVulnPoc: (id: number) => del<{ deleted: boolean }>(`/vulndb/pocs/${id}`),
  vulnSyncStatus: (input?: { source?: string; status?: string; q?: string; limit?: number; offset?: number }) => {
    const p = new URLSearchParams();
    if (input?.source) p.set("source", input.source);
    if (input?.status && input.status !== "all") p.set("status", input.status);
    if (input?.q) p.set("q", input.q);
    if (input?.limit) p.set("limit", String(input.limit));
    if (input?.offset) p.set("offset", String(input.offset));
    return get<{
      runs: VulnSyncRun[];
      total: number;
      status_counts: Record<string, number>;
      latest: VulnSyncRun[];
      counts: Record<string, number>;
      total_entries: number;
      enabled: boolean;
      interval_hours: number;
      cnvd_url?: string;
    }>(`/vulndb/sync?${p.toString()}`).then((r) => ({
      runs: arr(r.runs),
      total: r.total ?? 0,
      status_counts: r.status_counts ?? {},
      latest: arr(r.latest),
      counts: r.counts ?? {},
      total_entries: r.total_entries ?? 0,
      enabled: r.enabled,
      interval_hours: r.interval_hours,
      cnvd_url: r.cnvd_url ?? "",
    }));
  },
  triggerVulnSync: () => post<{ status: string }>("/vulndb/sync", {}),
  setVulnSyncConfig: (input: { enabled?: boolean; interval_hours?: number; cnvd_url?: string }) =>
    put<{ ok: boolean }>("/vulndb/sync/config", input),
  // NVD 历史回填:启动/进度/取消(断点续跑,进度存 settings)。
  vulnBackfillStatus: () =>
    get<{ running: boolean; phase: string; start_year: number; window_end: string; done: boolean; error: string }>(
      "/vulndb/backfill",
    ),
  startVulnBackfill: (input: { start_year?: number }) =>
    post<{ status: string }>("/vulndb/backfill", input ?? {}),
  cancelVulnBackfill: () => del<{ status: string }>("/vulndb/backfill"),
  // ---- SRC 平台选择(平台 → 目标 → 资产) ----
  srcPlatforms: (input?: { q?: string; enabled?: string; limit?: number; offset?: number }) => {
    const p = new URLSearchParams();
    if (input?.q) p.set("q", input.q);
    if (input?.enabled && input.enabled !== "all") p.set("enabled", input.enabled);
    p.set("limit", String(input?.limit ?? 50));
    p.set("offset", String(input?.offset ?? 0));
    return get<{ platforms: SRCPlatform[]; total: number; counts: Record<string, number> }>(
      `/src/platforms?${p.toString()}`,
    ).then((r) => ({ platforms: arr(r.platforms), total: r.total ?? 0, counts: r.counts ?? {} }));
  },
  getSRCPlatform: (id: number) =>
    get<{ platform: SRCPlatform }>(`/src/platforms/${id}`).then((r) => r.platform),
  saveSRCPlatform: (input: Partial<SRCPlatform>) => {
    // 有 id 走 PUT(更新),无 id 走 POST(新建)——与平台其它模块一致的语义。
    if (input.id) return put<{ platform: SRCPlatform }>(`/src/platforms/${input.id}`, input);
    return post<{ platform: SRCPlatform }>("/src/platforms", input);
  },
  deleteSRCPlatform: (id: number) => del<{ deleted: boolean }>(`/src/platforms/${id}`),
  // 平台下的目标列表
  srcPlatformTargets: (platformId: number, input?: { q?: string; limit?: number; offset?: number }) => {
    const p = new URLSearchParams();
    if (input?.q) p.set("q", input.q);
    p.set("limit", String(input?.limit ?? 50));
    p.set("offset", String(input?.offset ?? 0));
    return get<{ targets: SRCTarget[]; total: number }>(`/src/platforms/${platformId}/targets?${p.toString()}`).then(
      (r) => ({ targets: arr(r.targets), total: r.total ?? 0 }),
    );
  },
  // 在平台下新增目标(可同时挂链);target_id 非 0 表示挂已有目标
  addSRCPlatformTarget: (
    platformId: number,
    input: { target_id?: number; name?: string; company_id?: number; description?: string; scope_note?: string },
  ) => post<{ target: SRCTarget; link: SRCTargetLink }>(`/src/platforms/${platformId}/targets`, input),
  removeSRCPlatformTarget: (platformId: number, targetId: number) =>
    del<{ deleted: boolean }>(`/src/platforms/${platformId}/targets/${targetId}`),
  // 平台规范附件
  srcPlatformFiles: (platformId: number) =>
    get<{ files: SRCPlatformFile[] }>(`/src/platforms/${platformId}/files`).then((r) => arr(r.files)),
  uploadSRCPlatformFile: (platformId: number, file: File, kind: string) => {
    const fd = new FormData();
    fd.append("file", file);
    fd.append("kind", kind);
    const token = getToken();
    return fetch(`/api/src/platforms/${platformId}/files`, {
      method: "POST",
      // 不能设 Content-Type:必须让浏览器自己带 multipart boundary。
      headers: token ? { Authorization: `Bearer ${token}` } : undefined,
      body: fd,
    }).then(async (r) => {
      if (!r.ok) throw new Error(await r.text());
      return r.json() as Promise<{ file: SRCPlatformFile }>;
    });
  },
  deleteSRCPlatformFile: (platformId: number, fileId: number) =>
    del<{ deleted: boolean }>(`/src/platforms/${platformId}/files/${fileId}`),
  // 附件类别改派(统一上传后 AI/手动归类)
  updateSRCPlatformFileKind: (platformId: number, fileId: number, kind: string) =>
    put<{ file: SRCPlatformFile }>(`/src/platforms/${platformId}/files/${fileId}`, { kind }),
  // AI 识别填入:附件正文 → LLM 结构化为四类规范建议(不落库,前端预填人工确认)。
  // fileIds 缺省 = 用该平台全部有正文的附件。
  aiFillPlatformSpec: (platformId: number, fileIds?: number[]) =>
    post<{ suggestion: SPECAIFillSuggestion }>(`/src/platforms/${platformId}/spec/ai-fill`, {
      file_ids: fileIds,
    }),
  // SRC 监管聚合(监管&规则中心 · SRC 板块)
  srcGuardOverview: () => get<SRCGuardOverview>("/src/guard/overview"),
  // 规范文本框旁的「上传识别」:纯解析不存档,正文直接回填文本框
  parseSpecFile: (file: File) => {
    const fd = new FormData();
    fd.append("file", file);
    const token = getToken();
    return fetch(`/api/src/platforms/spec/parse-file`, {
      method: "POST",
      headers: token ? { Authorization: `Bearer ${token}` } : undefined,
      body: fd,
      signal: AbortSignal.timeout(120_000),
    }).then(async (r) => {
      if (!r.ok) throw new Error(await r.text());
      return r.json() as Promise<{ text: string; parse_status: string; note: string }>;
    });
  },
  // 单段规范 AI 润色:只改措辞结构不改事实,返回润色后文本(前端预填人工确认)。
  // 必须大于服务端 ctx 的 300s——前端先超时会丢掉后端仍在生成的结果。
  polishSpecField: (field: string, text: string) =>
    http<{ polished: string }>(`/src/platforms/spec/polish`, {
      method: "POST",
      body: JSON.stringify({ field, text }),
      signal: AbortSignal.timeout(320_000),
    }),
  // 附件下载走原生导航(带 token 的 fetch 拿不到浏览器下载语义)
  srcPlatformFileDownloadUrl: (platformId: number, fileId: number) =>
    `/api/src/platforms/${platformId}/files/${fileId}/download`,

  // 目标
  srcTargets: (input?: { q?: string; platformId?: number; limit?: number; offset?: number }) => {
    const p = new URLSearchParams();
    if (input?.q) p.set("q", input.q);
    if (input?.platformId) p.set("platform_id", String(input.platformId));
    p.set("limit", String(input?.limit ?? 50));
    p.set("offset", String(input?.offset ?? 0));
    return get<{ targets: SRCTarget[]; total: number }>(`/src/targets?${p.toString()}`).then((r) => ({
      targets: arr(r.targets),
      total: r.total ?? 0,
    }));
  },
  getSRCTarget: (id: number) => get<{ target: SRCTarget }>(`/src/targets/${id}`).then((r) => r.target),
  saveSRCTarget: (input: Partial<SRCTarget>) => {
    if (input.id) return put<{ target: SRCTarget }>(`/src/targets/${input.id}`, input);
    return post<{ target: SRCTarget }>("/src/targets", input);
  },
  deleteSRCTarget: (id: number) => del<{ deleted: boolean }>(`/src/targets/${id}`),
  // 目标所属的平台关联(含各平台下的授权范围)
  srcTargetLinks: (targetId: number) =>
    get<{ links: SRCTargetLink[] }>(`/src/targets/${targetId}/links`).then((r) => arr(r.links)),
  saveSRCTargetLink: (targetId: number, platformId: number, input: Partial<SRCTargetLink>) =>
    put<{ link: SRCTargetLink }>(`/src/targets/${targetId}/links/${platformId}`, input),
  unlinkSRCTarget: (targetId: number, platformId: number) =>
    del<{ deleted: boolean }>(`/src/targets/${targetId}/links/${platformId}`),
  // 目标资产
  srcTargetAssets: (targetId: number, input?: { platformId?: number; type?: string; q?: string; limit?: number; offset?: number }) => {
    const p = new URLSearchParams();
    if (input?.platformId) p.set("platform_id", String(input.platformId));
    if (input?.type && input.type !== "all") p.set("type", input.type);
    if (input?.q) p.set("q", input.q);
    p.set("limit", String(input?.limit ?? 50));
    p.set("offset", String(input?.offset ?? 0));
    return get<{ assets: SRCTargetAsset[]; total: number; counts: Record<string, number> }>(
      `/src/targets/${targetId}/assets?${p.toString()}`,
    ).then((r) => ({ assets: arr(r.assets), total: r.total ?? 0, counts: r.counts ?? {} }));
  },
  addSRCTargetAssets: (
    targetId: number,
    input: {
      platform_id: number;
      // 手工录入:直接给 type/value/port;导入:给 asset_ids(已有资产)或 staged_ids(暂存区)
      assets?: { type: string; value: string; port?: number }[];
      asset_ids?: number[];
      staged_ids?: number[];
      source?: string;
    },
  ) => post<{ added: number; skipped: number; assets: SRCTargetAsset[]; errors: string[] }>(`/src/targets/${targetId}/assets`, input),
  removeSRCTargetAssets: (targetId: number, ids: number[]) =>
    del<{ deleted: number }>(`/src/targets/${targetId}/assets?ids=${ids.join(",")}`),
  // 可导入来源:该目标关联企业下的信息搜集暂存区 + 已有资产
  srcTargetImportSources: (targetId: number, platformId: number, input?: { kind?: string; q?: string; limit?: number }) => {
    const p = new URLSearchParams();
    p.set("platform_id", String(platformId));
    if (input?.kind) p.set("kind", input.kind);
    if (input?.q) p.set("q", input.q);
    p.set("limit", String(input?.limit ?? 200));
    return get<{ sources: SRCTargetImportSource[] }>(`/src/targets/${targetId}/import-sources?${p.toString()}`).then((r) =>
      arr(r.sources),
    );
  },

  srcRewards: (input?: {
    companyId?: number;
    platformId?: number;
    targetId?: number;
    taskId?: number;
    status?: string;
    severity?: string;
    q?: string;
    sort?: string;
    limit?: number;
    offset?: number;
  }) => {
    const p = new URLSearchParams();
    if (input?.companyId) p.set("company_id", String(input.companyId));
    if (input?.platformId) p.set("platform_id", String(input.platformId));
    if (input?.targetId) p.set("target_id", String(input.targetId));
    if (input?.taskId) p.set("task_id", String(input.taskId));
    if (input?.status) p.set("status", input.status);
    if (input?.severity) p.set("severity", input.severity);
    if (input?.q) p.set("q", input.q);
    if (input?.sort) p.set("sort", input.sort);
    p.set("limit", String(input?.limit ?? 50));
    p.set("offset", String(input?.offset ?? 0));
    return get<{ rewards: SRCReward[]; total: number; counts: Record<string, number> }>(
      `/src/rewards?${p.toString()}`,
    ).then((r) => ({ rewards: arr(r.rewards), total: r.total, counts: r.counts ?? {} }));
  },
  createSRCReward: (input: Partial<SRCReward>) => post<{ reward: SRCReward }>("/src/rewards", input),
  updateSRCReward: (id: number, input: Partial<SRCReward>) =>
    put<{ reward: SRCReward }>(`/src/rewards/${id}`, input),
  deleteSRCReward: (id: number) => del<{ deleted: boolean }>(`/src/rewards/${id}`),
  srcOverview: () => get<SRCOverview>("/src/overview"),

  // ---- 报告中心 ----
  listReports: (input?: { kind?: string; q?: string; companyId?: number; severity?: string; limit?: number; offset?: number }) => {
    const p = new URLSearchParams();
    if (input?.kind) p.set("kind", input.kind);
    if (input?.q) p.set("q", input.q);
    if (input?.companyId) p.set("company_id", String(input.companyId));
    if (input?.severity) p.set("severity", input.severity);
    p.set("limit", String(input?.limit ?? 50));
    p.set("offset", String(input?.offset ?? 0));
    return get<{ reports: ReportSummary[]; total: number; counts: Record<string, number> }>(
      `/reports?${p.toString()}`,
    ).then((r) => ({ reports: arr(r.reports), total: r.total, counts: r.counts ?? {} }));
  },
  // 报告中心的「先选企业」列表:每个企业名下的报告数与分类细分。
  reportCompanies: () =>
    get<{ companies: ReportCompanyCount[]; assigned: number; unassigned: number }>("/reports/companies").then((r) => ({
      companies: arr(r.companies),
      assigned: r.assigned ?? 0,
      unassigned: r.unassigned ?? 0,
    })),
  // 「未关联企业」桶:自定义报告 + 任务/漏洞都没挂企业的报告。
  listUnassignedReports: (input?: { kind?: string; q?: string; severity?: string; limit?: number; offset?: number }) => {
    const p = new URLSearchParams();
    if (input?.kind) p.set("kind", input.kind);
    if (input?.q) p.set("q", input.q);
    if (input?.severity) p.set("severity", input.severity);
    p.set("company_id", "-1");
    p.set("limit", String(input?.limit ?? 50));
    p.set("offset", String(input?.offset ?? 0));
    return get<{ reports: ReportSummary[]; total: number; counts: Record<string, number> }>(
      `/reports?${p.toString()}`,
    ).then((r) => ({ reports: arr(r.reports), total: r.total, counts: r.counts ?? {} }));
  },
  reportDetail: (id: number) => get<{ report: ReportDetail }>(`/reports/${id}`).then((r) => r.report),
  createReport: (input: { title: string; summary?: string; content: string }) =>
    post<{ report: ReportDetail }>("/reports", {
      title: input.title,
      summary: input.summary ?? "",
      content: input.content,
    }),
  deleteReport: (id: number) => del<{ deleted: boolean }>(`/reports/${id}`),
  // ---- 报告模板 ----
  reportTemplates: (input?: { category?: string; q?: string; limit?: number; offset?: number }) => {
    const p = new URLSearchParams();
    if (input?.category) p.set("category", input.category);
    if (input?.q) p.set("q", input.q);
    p.set("limit", String(input?.limit ?? 20));
    p.set("offset", String(input?.offset ?? 0));
    return get<{ templates: ReportTemplate[]; total: number }>(`/report-templates?${p.toString()}`).then((r) => ({
      templates: arr(r.templates),
      total: r.total,
    }));
  },
  reportTemplate: (id: number) => get<{ template: ReportTemplate }>(`/report-templates/${id}`).then((r) => r.template),
  uploadReportTemplate: (file: File, name?: string, category?: string) => {
    const fd = new FormData();
    fd.append("file", file);
    if (name) fd.append("name", name);
    if (category) fd.append("category", category);
    const token = getToken();
    return http<{ template: ReportTemplate }>("/report-templates", {
      method: "POST",
      body: fd,
      signal: AbortSignal.timeout(120_000),
      headers: token ? { Authorization: `Bearer ${token}` } : undefined,
    });
  },
  updateReportTemplate: (id: number, patch: { name?: string; category?: string; content?: string; export_prompt?: string }) =>
    put<{ ok: boolean }>(`/report-templates/${id}`, patch),
  deleteReportTemplate: (id: number) => del<{ deleted: boolean }>(`/report-templates/${id}`),
  // AI 占位模板识别(异步任务化):202 立即返回监看任务/会话 id,生成在服务端跑,
  // 不再受 HTTP 超时约束;进度用 reportTemplate(id) 轮询 analyze_status。
  analyzeReportTemplate: (id: number, profileId?: number) =>
    http<{ status: string; task_id: number; conv_id: number }>(`/report-templates/${id}/analyze`, {
      method: "POST",
      body: JSON.stringify({ profile_id: profileId ?? 0 }),
    }),
  reportEvents: (id: number) => get<{ events: ReportEvent[] }>(`/reports/${id}/events`).then((r) => arr(r.events)),
  // 按模板导出报告为文件并触发浏览器下载。
  exportReport: async (id: number, templateId?: number, format: "md" | "docx" = "md") => {
    const token = getToken();
    const p = new URLSearchParams({ format });
    if (templateId) p.set("template_id", String(templateId));
    const url = "/api/reports/" + encodeURIComponent(String(id)) + "/export?" + p.toString();
    const r = await fetch(url, {
      headers: token ? { Authorization: `Bearer ${token}` } : undefined,
    });
    if (!r.ok) throw new Error(`导出失败:${r.status}`);
    const blob = await r.blob();
    const cd = r.headers.get("Content-Disposition") ?? "";
    const m = cd.match(/filename\*=UTF-8''([^;]+)/);
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = m ? decodeURIComponent(m[1]) : `report-${id}.${format}`;
    a.click();
    URL.revokeObjectURL(a.href);
  },
  // 从现有产出归档:任务总结报告(即时渲染探索图)/漏洞报告(需 findings.report 已有正文)。
  generateTaskReport: (taskId: string) =>
    post<{ id: number; created: boolean }>("/reports/generate", { source: "task", task_id: taskId }),
  generateFindingReport: (findingId: number) =>
    post<{ id: number; created: boolean }>("/reports/generate", { source: "finding", finding_id: findingId }),
  // ---- 报告生成作业(AI 按模板导出 / 任务报告 AI 撰写) ----
  // AI 按模板导出(异步):服务端建 stage='report' 任务 + 报告 agent 监看会话,
  // 按模板与导出写作指令分章生成正文后渲染成品;进度用 reportJob(jobId) 轮询。
  exportReportAI: (id: number, input: { templateId: number; format: "md" | "docx"; profileId?: number }) =>
    post<{ job_id: number; task_id: number; conv_id: number }>(`/reports/${id}/export-ai`, {
      template_id: input.templateId,
      format: input.format,
      profile_id: input.profileId ?? 0,
    }),
  // 报告的生成作业历史(新者在前)。
  reportJobs: (id: number) => get<{ jobs: ReportJob[] }>(`/reports/${id}/jobs`).then((r) => arr(r.jobs)),
  // 单条作业进度(轮询用):status running→done/failed。
  reportJob: (jobId: number) => get<{ job: ReportJob }>(`/report-jobs/${jobId}`).then((r) => r.job),
  // 下载作业产物(done 状态)并触发浏览器保存。
  reportJobDownload: async (jobId: number) => {
    const token = getToken();
    const url = "/api/report-jobs/" + encodeURIComponent(String(jobId)) + "/download";
    const r = await fetch(url, { headers: token ? { Authorization: `Bearer ${token}` } : undefined });
    if (!r.ok) throw new Error(`下载失败:${r.status}`);
    const blob = await r.blob();
    const cd = r.headers.get("Content-Disposition") ?? "";
    const m = cd.match(/filename\*=UTF-8''([^;]+)/);
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = m ? decodeURIComponent(m[1]) : `report-job-${jobId}`;
    a.click();
    URL.revokeObjectURL(a.href);
  },
  // ---- 决策引擎(AI管理·Decision 配置;监管&规则·AI监管 编排) ----
  decisionEngines: () => get<{ engines: DecisionEngine[] }>("/decision/engines").then((r) => arr(r.engines)),
  updateDecisionEngine: (
    key: string,
    patch: { enabled?: boolean; base_url?: string; api_key?: string; model?: string },
  ) => put<{ ok: boolean }>(`/decision/engines/${encodeURIComponent(key)}`, patch),
  testDecisionEngine: (key: string) =>
    post<{ ok: boolean; latency_ms?: number; detail?: string; error?: string }>(
      `/decision/engines/${encodeURIComponent(key)}/test`,
    ),
  // JEV 门限阈值:置信度门槛、危险信号阈值、提权是否硬拦、低置信策略。
  // 响应附 defaults,便于页面提示"恢复默认"。
  jevThresholds: () => get<JevThresholdsResponse>("/decision/engines/jev/thresholds"),
  saveJevThresholds: (th: JevThresholds) =>
    put<JevThresholdsResponse>("/decision/engines/jev/thresholds", th),
  chatMentions: (kind: string, query: string, signal?: AbortSignal, cursor = "") =>
    http<{ items: ChatMention[]; next_cursor?: string }>(
      `/chat/mentions?${new URLSearchParams({ kind, q: query, cursor })}`,
      { signal },
    ),
  chat: (message: string, task?: string, attachments?: ChatAttachment[], seg?: number) =>
    post<{ reply: string; mode: string }>(`/chat${tq(task)}`, { message, attachments, seg }),
  chatStatus: (taskId: string) => get<{ running: boolean }>(`/tasks/${taskId}/chat/status`),
  // 方式1 文件上传:落到会话/任务工作目录 uploads/，返回可供 agent Read 的相对路径。
  chatUpload: async (scope: "task" | "session" | "staging", id: string, files: File[]) => {
    if (MOCK)
      return {
        attachments: files.map((f) => ({
          name: f.name,
          path: `uploads/${f.name}`,
          size: f.size,
          abs: `/mock/${f.name}`,
        })),
      };
    const fd = new FormData();
    for (const f of files) fd.append("file", f);
    const token = getToken();
    // query 统一走 URLSearchParams 构造（同 chatMentions）：参数值一律正确编码，
    // 不做字符串手拼，避免半成品编码造成的注入/参数混淆。
    const query = new URLSearchParams({ scope, id });
    const r = await fetch(`/api/chat/upload?${query}`, {
      method: "POST",
      headers: { ...(token ? { Authorization: `Bearer ${token}` } : {}) },
      body: fd,
    });
    if (!r.ok) throw new Error(`上传失败: ${r.status} ${await r.text()}`);
    return r.json() as Promise<{ attachments: ChatAttachment[] }>;
  },
  stopChat: (taskId: string) => post<{ status: string }>(`/tasks/${taskId}/chat/stop`, {}),
  gc: (ttl = 86400) => post<{ removed: number }>(`/gc?ttl=${ttl}`, {}),

  // ---- LLM ----
  getLLM: () =>
    get<{
      configured: boolean;
      provider: string;
      model: string;
      base_url: string;
      proxy?: string;
      key_set: boolean;
      rate_per_second?: number;
      rate_per_minute?: number;
      context_window_k?: number;
      thinking_type?: string;
      reasoning_effort?: string;
    }>("/llm"),
  setLLM: (
    provider: string,
    model: string,
    base_url: string,
    api_key: string,
    rate_per_second = 0,
    rate_per_minute = 0,
    proxy = "",
    context_window_k = 0,
  ) => post("/llm", { provider, model, base_url, proxy, api_key, rate_per_second, rate_per_minute, context_window_k }),
  testLLM: (
    provider: string,
    model: string,
    base_url: string,
    api_key: string,
    proxy = "",
    thinking_type = "",
    reasoning_effort = "",
    profile_id?: number,
    streaming = true, // 用该配置真实的收发模式来测，别让"流式能通、非流式不通"漏到会话里
    session_header_key = "", // 非空=测试请求也带该自定义会话头（值为一次性 session id）
  ) =>
    // reply = 模型实际回复(已截断);一个字都不回的配置后端直接判失败
    post<{ ok: boolean; error?: string; latency_ms?: number; model?: string; reply?: string }>("/llm/test", {
      provider,
      model,
      base_url,
      proxy,
      api_key,
      thinking_type,
      reasoning_effort,
      profile_id,
      streaming,
      session_header_key,
    }),
  llmProfiles: () => get<{ profiles: LLMProfile[] }>("/llm/profiles").then((r) => arr(r.profiles)),
  saveLLMProfile: (p: {
    id?: number; // omit/0 = create; set = update that profile
    name: string;
    format: string;
    model: string;
    base_url?: string;
    proxy?: string;
    api_key?: string; // blank on update keeps the existing key
    rate_per_second?: number;
    rate_per_minute?: number;
    context_window_k?: number;
    thinking_type?: string; // ""(不发送)|"disabled"|"enabled"
    reasoning_effort?: string; // ""(不发送)|"low"|"medium"|"high"|"xhigh"|"max"
    priority?: number; // 轮询顺位，越大越先用
    pool_exclude?: boolean; // true=不作为故障转移目标
    streaming?: boolean; // true(默认)=流式 | false=非流式
    max_tokens?: number; // 单次回复输出上限；0=不发送，由服务端默认值决定
    max_tokens_field?: string; // ""=max_tokens(默认) | "max_completion_tokens"（仅 openai 格式）
    session_header_key?: string; // 非空=每次请求带该 HTTP 头，头值=当前会话 session id；""=不发送
    retry?: LLMRetryOverride; // 本配置的重试覆盖；各项留 0 = 跟随全局重试策略
  }) => post<{ id: number }>("/llm/profiles", p),
  deleteLLMProfile: (id: string) => del<{ deleted: number }>(`/llm/profiles/${id}`),
  activateLLMProfile: (id: string) => post<{ ok: boolean }>("/llm/profiles/active", { id: Number(id) }),
  // 轮询链的实际顺序 + 各配置的熔断状态。
  llmPool: () => get<LLMPoolStatus>("/llm/pool"),
  // 清除熔断，让下一次调用立刻重试该配置；不传 id = 全部清除。
  resetLLMPool: (id?: string) => post<LLMPoolStatus>("/llm/pool/reset", { id: id ? Number(id) : 0 }),
  // 全局重试策略（五层各自的次数+间隔）。全 0 = 全部走内置默认。
  llmRetryPolicy: () => get<LLMRetryPolicy>("/llm/retry-policy"),
  saveLLMRetryPolicy: (p: LLMRetryPolicy) => post<LLMRetryPolicy>("/llm/retry-policy", p),
  fetchLLMModels: (provider: string, base_url: string, api_key: string, proxy = "", profile_id?: number) =>
    post<{ ok: boolean; error?: string; models?: string[] }>("/llm/models", {
      provider,
      base_url,
      api_key,
      proxy,
      profile_id,
    }),
  // 最近一次成功拉取的模型列表(固化缓存;随 profile 存,编辑端点时直接可选)。
  llmModelsCache: (profileId: number) =>
    get<{ models: string[] }>(`/llm/models-cache?profile_id=${profileId}`).then((r) => arr(r.models)),

  // ---- agents ----
  agents: () => get<{ agents: Agent[] }>("/agents").then((r) => arr(r.agents)),
  getAgent: (key: string) => get<AgentDetail>(`/agents/${key}`),
  createAgent: (key: string, name: string, description = "", stage: "" | "recon" | "exploit" | "post" | "report" = "") =>
    post<Agent>("/agents", { key, name, description, stage }),
  updateAgent: (key: string, name: string, description = "") =>
    patch<{ ok: boolean }>(`/agents/${key}`, { name, description }),
  deleteAgent: (key: string) => del<{ deleted: string }>(`/agents/${key}`),

  // ---- conversations (chat page) ----
  // stage: 三阶段分组入口传 recon|exploit|post,只拉本阶段 agent 的会话;缺省=全部(任务中心)。
  conversations: (stage?: string) =>
    get<{ conversations: Conversation[] }>(
      stage ? `/conversations?stage=${encodeURIComponent(stage)}` : "/conversations",
    ).then((r) => arr(r.conversations)),
  createConversation: (agent_key: string, title = "", llm_profile_id?: number | null) =>
    post<Conversation>("/conversations", { agent_key, title, llm_profile_id: llm_profile_id ?? null }),
  updateConversation: (id: number, input: { title?: string; pinned?: boolean }) =>
    patch<Conversation>(`/conversations/${id}`, input),
  renameConversation: (id: number, title: string) => patch<Conversation>(`/conversations/${id}`, { title }),
  pinConversation: (id: number, pinned: boolean) => patch<Conversation>(`/conversations/${id}`, { pinned }),
  updateConversationProfile: (id: number, llm_profile_id: number | null) =>
    patch<{ ok: boolean }>(`/conversations/${id}/profile`, { llm_profile_id }),
  deleteConversation: (id: number) => del<{ deleted: number }>(`/conversations/${id}`),
  deleteConversations: (ids: number[]) =>
    post<{ items: { id: number; ok: boolean; error?: string }[] }>("/conversations/delete/batch", { ids }),
  // Incremental tail: steps after `since` (id ASC) — live poll + post-send fetch.
  conversationMessages: (id: number, since = 0) =>
    get<{ items: Activity[]; cursor: number; running: boolean }>(`/conversations/${id}/messages?since=${since}`).then(
      (r) => ({ items: arr(r.items), cursor: r.cursor ?? 0, running: !!r.running }),
    ),
  // Reverse pagination: the latest `limit` steps (before=0) or the page of older
  // steps ending before id `before`. hasMore = still-older steps exist.
  conversationHistory: (id: number, before = 0, limit = 200) => {
    const q = new URLSearchParams({ limit: String(limit) });
    if (before > 0) q.set("before", String(before));
    return get<{ items: Activity[]; cursor: number; running: boolean; hasMore: boolean }>(
      `/conversations/${id}/messages?${q.toString()}`,
    ).then((r) => ({ items: arr(r.items), cursor: r.cursor ?? 0, running: !!r.running, hasMore: !!r.hasMore }));
  },
  conversationMsgDetail: (id: number, seq: number) =>
    get<{ detail: string }>(`/conversations/${id}/messages/${seq}`).then((r) => r.detail ?? ""),
  sendConversationMessage: (id: number, message: string, attachments?: ChatAttachment[]) =>
    post<{ status: string }>(`/conversations/${id}/messages`, { message, attachments }),
  stopConversation: (id: number) => post<{ status: string }>(`/conversations/${id}/stop`, {}),
  saveAgentPrompt: (key: string, template: string, note = "") =>
    put<{ version: number }>(`/agents/${key}/prompt`, { template, note }),
  resetAgentPrompt: (key: string) => post<{ version: number }>(`/agents/${key}/prompt/reset`, {}),
  // 收尾提示词(超时/步数耗尽的 settlement 提示);prompt 空串=清除覆盖、用内置默认;
  // max_turns 省略则不动、传 0=用内置默认轮数
  saveAgentWrapup: (key: string, prompt: string, maxTurns?: number) =>
    put<{ ok: boolean }>(`/agents/${key}/wrapup`, { prompt, max_turns: maxTurns }),
  resetAgentWrapup: (key: string) =>
    post<{ ok: boolean; wrapup_default: string; wrapup_max_turns_default: number }>(`/agents/${key}/wrapup/reset`, {}),
  // 任务级超时收尾词(仅 worker/planner);prompt 空=清除、用内置默认
  saveAgentTaskTimeoutWrapup: (key: string, prompt: string, maxTurns?: number) =>
    put<{ ok: boolean }>(`/agents/${key}/wrapup/task-timeout`, { prompt, max_turns: maxTurns }),
  resetAgentTaskTimeoutWrapup: (key: string) =>
    post<{ ok: boolean; task_timeout_wrapup_default: string; task_timeout_wrapup_max_turns_default: number }>(
      `/agents/${key}/wrapup/task-timeout/reset`,
      {},
    ),
  // P3 triggers (仅自定义 agent)
  agentTriggers: (key: string) =>
    get<{ triggers: AgentTrigger[] }>(`/agents/${key}/triggers`).then((r) => arr(r.triggers)),
  createTrigger: (key: string, t: Omit<AgentTrigger, "id" | "agent_key" | "last_fire">) =>
    post<AgentTrigger>(`/agents/${key}/triggers`, t),
  updateTrigger: (id: number, t: Omit<AgentTrigger, "id" | "agent_key" | "last_fire">) =>
    patch<{ ok: boolean }>(`/triggers/${id}`, t),
  deleteTrigger: (id: number) => del<{ deleted: number }>(`/triggers/${id}`),
  saveAgentConfig: (
    key: string,
    patch: {
      llm_profile_id?: number | null; // number=绑定；null=解绑(跟随任务/全局)；缺省=不动
      max_turns?: number;
      run_seconds?: number;
      web_search?: boolean;
      interactive_shell?: boolean;
      trigger_run_mode?: "serial" | "parallel";
      trigger_merge_mode?: "by_task" | "all" | "none";
      trigger_max_parallel?: number;
      stage?: "" | "recon" | "exploit" | "post" | "report"; // 归属阶段;缺省=不动
    },
  ) => put<{ ok: boolean }>(`/agents/${key}/config`, patch),
  agentPromptVersions: (key: string) =>
    get<{ versions: PromptVersion[] }>(`/agents/${key}/prompts`).then((r) => arr(r.versions)),
  agentVariables: (key: string) =>
    get<{ variables: PromptVar[] }>(`/agents/${key}/variables`).then((r) => arr(r.variables)),
  previewAgentPrompt: (key: string, template: string, sample?: Record<string, string>) =>
    post<{ rendered: string; error?: string }>(`/agents/${key}/prompt/preview`, { template, sample }),
  getAgentVisibility: (key: string) => get<{ mcp: number[]; skill: string[] }>(`/agents/${key}/visibility`),
  setAgentVisibility: (key: string, mcp: number[], skill: string[]) =>
    put<{ ok: boolean }>(`/agents/${key}/visibility`, { mcp, skill }),

  // ---- tools (内置工具目录) ----
  tools: () => get<{ tools: Tool[] }>("/tools").then((r) => arr(r.tools)),
  toolsStage: (stage: string) =>
    get<{ tools: Tool[] }>(`/tools?stage=${encodeURIComponent(stage)}`).then((r) => arr(r.tools)),
  saveTool: (key: string, patch: Pick<Tool, "description" | "schema" | "agents" | "enabled">) =>
    put<{ ok: boolean }>(`/tools/${key}`, patch),
  resetTool: (key: string) => post<{ ok: boolean }>(`/tools/${key}/reset`, {}),
  // custom tools (自定义工具)
  createCustomTool: (
    t: Pick<Tool, "key" | "description" | "schema" | "agents" | "enabled" | "kind" | "exec" | "deferred">,
  ) => post<{ key: string }>("/tools/custom", t),
  updateCustomTool: (
    key: string,
    t: Pick<Tool, "description" | "schema" | "agents" | "enabled" | "kind" | "exec" | "deferred">,
  ) => put<{ ok: boolean }>(`/tools/custom/${key}`, t),
  deleteCustomTool: (key: string) => del<{ deleted: string }>(`/tools/custom/${key}`),
  testCustomTool: (body: { kind: string; exec: Record<string, unknown>; params: Record<string, unknown> }) =>
    post<{ output: string; is_error: boolean }>("/tools/custom/test", body),
  detectPython: () => post<{ python_interpreter: string }>("/settings/python/detect", {}),

  // ---- mcp ----
  mcpServers: () => get<{ servers: MCPServer[] }>("/mcp").then((r) => arr(r.servers)),
  saveMcpServer: (m: Partial<MCPServer>) => post<{ id: number }>("/mcp", m),
  deleteMcpServer: (id: number) => del<{ deleted: number }>(`/mcp/${id}`),
  mcpTools: (id: number) => get<{ tools: MCPTool[] }>(`/mcp/${id}/tools`).then((r) => arr(r.tools)),
  refreshMcpServer: (id: number) => post<{ tools: MCPTool[] }>(`/mcp/${id}/refresh`, {}).then((r) => arr(r.tools)),

  // ---- 资产同步 (ScopeSentry 数据源) ----
  ssStatus: () =>
    get<{ exists: boolean; configured: boolean; enabled: boolean; reachable: boolean; url?: string; tools: string[] }>(
      "/sync/scopesentry/status",
    ),
  ssDatasource: (body: { url?: string; api_key?: string }) =>
    post<{ id: number; enabled: boolean }>("/sync/scopesentry/datasource", body),
  ssProjects: (page = 1, size = 50, search = "") =>
    get<{ projects: SSProject[]; tag: Record<string, number> }>(
      `/sync/scopesentry/projects?page=${page}&size=${size}${search ? `&search=${encodeURIComponent(search)}` : ""}`,
    ).then((r) => ({ projects: arr(r.projects), tag: r.tag ?? {} })),
  ssTasks: (page = 1, size = 50, search = "") =>
    get<{ tasks: SSTask[] }>(
      `/sync/scopesentry/tasks?page=${page}&size=${size}${search ? `&search=${encodeURIComponent(search)}` : ""}`,
    ).then((r) => arr(r.tasks)),
  ssSync: (body: {
    dimension: "project" | "task";
    targets: string[];
    asset_types: string[];
    create_company?: boolean;
    page_size?: number;
  }) =>
    post<{
      synced: Record<string, number>;
      companies: string[] | null;
      warnings: string[] | null;
      errors: string[] | null;
    }>("/sync/scopesentry/sync", body),

  // ---- skills (文件系统) ----
  skills: () => get<{ skills: SkillItem[] }>("/skills").then((r) => arr(r.skills)),
  createSkill: (s: {
    name: string;
    description: string;
    license?: string;
    compatibility?: string;
    mcps?: string[];
    instructions?: string;
  }) => post<{ name: string }>("/skills", s),
  // uploadSkill installs a skill from a .zip (multipart). Surfaces the backend
  // error text (e.g. 已存在 / 缺少 SKILL.md) so the UI can show a precise message.
  uploadSkill: async (file: File, overwrite = false): Promise<{ name: string; files: number }> => {
    if (MOCK) return { name: file.name.replace(/\.zip$/i, ""), files: 1 };
    const fd = new FormData();
    fd.append("file", file);
    const token = getToken();
    const r = await fetch(`/api/skills/upload${overwrite ? "?overwrite=true" : ""}`, {
      method: "POST",
      body: fd,
      headers: token ? { Authorization: `Bearer ${token}` } : {},
    });
    const body = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(body?.error || `上传失败(${r.status})`);
    return body;
  },
  deleteSkill: (name: string) => del<{ deleted: string }>(`/skills/${name}`),
  updateSkillMeta: (
    name: string,
    meta: { mcps?: string[]; description?: string; license?: string; compatibility?: string },
  ) => put<{ ok: boolean }>(`/skills/${name}/meta`, meta),
  createSkillDir: (skill: string, path: string) => post<{ dir: string }>(`/skills/${skill}/dirs`, { path }),
  skillFiles: (name: string) => get<{ files: string[] }>(`/skills/${name}/files`).then((r) => r.files),
  readSkillFile: (name: string, file: string) =>
    get<{ content: string; file: string }>(`/skills/${name}/files/${file}`).then((r) => r.content),
  writeSkillFile: (name: string, file: string, content: string) =>
    put<{ ok: boolean }>(`/skills/${name}/files/${file}`, { content }),
  deleteSkillPath: (skill: string, path: string) => del<{ deleted: string }>(`/skills/${skill}/files/${path}`),
  skillUsage: (name: string, limit = 50) =>
    get<{ calls: SkillCall[] }>(`/skills/${name}/usage?limit=${limit}`).then((r) => arr(r.calls)),
  missingSkills: (limit = 20) =>
    get<{ missing: MissingSkill[] }>(`/skills/missing?limit=${limit}`).then((r) => arr(r.missing)),

  // ---- visibility (MCP resource side) ---- (agent ids are strings per spec)
  resourceVisibility: (kind: string, id: number) =>
    get<{ agents: string[] }>(`/visibility/${kind}/${id}`).then((r) => arr(r.agents)),
  toggleVisibility: (agentId: string, kind: string, resourceId: number, visible: boolean) =>
    post<{ ok: boolean }>("/visibility/toggle", { agent_id: agentId, kind, resource_id: resourceId, visible }),

  // ---- visibility (Skill，按名称) ----
  skillVisibility: (name: string) => get<{ agents: string[] }>(`/visibility/skill/${name}`).then((r) => arr(r.agents)),
  toggleSkillVisibility: (agentId: string, skillName: string, visible: boolean) =>
    post<{ ok: boolean }>("/visibility/skill/toggle", { agent_id: agentId, skill_name: skillName, visible }),

  // ---- intercept rules ----
  interceptRules: () => get<{ rules: InterceptRule[] }>("/intercept/rules").then((r) => arr(r.rules)),
  createInterceptRule: (rule: Omit<InterceptRule, "id" | "created_at" | "updated_at">) =>
    post<InterceptRule>("/intercept/rules", rule),
  updateInterceptRule: (id: number, rule: Omit<InterceptRule, "id" | "created_at" | "updated_at">) =>
    put<InterceptRule>(`/intercept/rules/${id}`, rule),
  deleteInterceptRule: (id: number) => del<{ deleted: number }>(`/intercept/rules/${id}`),
  toggleInterceptRule: (id: number, enabled: boolean) =>
    post<{ ok: boolean; enabled: boolean }>(`/intercept/rules/${id}/toggle`, { enabled }),

  // ---- intercept pending (ask) ----
  interceptPending: () => get<{ pending: InterceptPending[] }>("/intercept/pending").then((r) => arr(r.pending)),
  interceptGetOne: (id: number) => get<InterceptPending>(`/intercept/pending/${id}`),
  interceptDecide: (id: number, decision: "allowed" | "denied") =>
    post<{ ok: boolean }>(`/intercept/pending/${id}/decide`, { decision }),
  interceptExecution: (id: number, conversationId?: number) =>
    get<import("@/lib/types").InterceptExecution>(
      `/intercept/history/${id}/execution${conversationId ? `?conversation=${conversationId}` : ""}`,
    ),
  interceptDetail: (id: number) => get<InterceptDetail>(`/intercept/history/${id}`),
  interceptHistory: () => get<{ items: InterceptApprovalRow[] }>("/intercept/history").then((r) => arr(r.items)),
  interceptHistoryPage: (page = 1, size = 20, filter: InterceptApprovalFilter = {}) =>
    get<{ items: InterceptApprovalRow[]; total?: number }>(
      `/intercept/history?${interceptPageQuery(page, size, filter)}`,
    ).then((r) => ({
      items: arr(r.items),
      total: r.total ?? r.items?.length ?? 0,
    })),
  interceptTask: (taskId: string) =>
    get<{ items: InterceptApprovalRow[] }>(`/intercept/task/${taskId}`).then((r) => arr(r.items)),
  interceptTaskPage: (taskId: string, page = 1, size = 20, filter: InterceptApprovalFilter = {}) =>
    get<{ items: InterceptApprovalRow[]; total?: number }>(
      `/intercept/task/${encodeURIComponent(taskId)}?${interceptPageQuery(page, size, filter)}`,
    ).then((r) => ({
      items: arr(r.items),
      total: r.total ?? r.items?.length ?? 0,
    })),

  // ---- intercept tool-config (全局工具拦截范围) ----
  interceptGetToolConfig: async (): Promise<{ enabled_tools: string[] }> => {
    if (MOCK) return { enabled_tools: ["bash"] };
    const token = getToken();
    const r = await fetch("/api/intercept/tool-config", {
      headers: token ? { Authorization: `Bearer ${token}` } : {},
    });
    if (!r.ok) throw new Error(await r.text());
    return r.json();
  },
  interceptSetToolConfig: async (enabledTools: string[]): Promise<void> => {
    if (MOCK) return;
    const token = getToken();
    const r = await fetch("/api/intercept/tool-config", {
      method: "PUT",
      headers: {
        "Content-Type": "application/json",
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      body: JSON.stringify({ enabled_tools: enabledTools }),
    });
    if (!r.ok) throw new Error(await r.text());
  },

  // ---- intercept LLM judge (模型兜底审批,全局配置) ----
  interceptGetJudgeConfig: () => get<JudgeConfig>("/intercept/judge"),
  interceptSetJudgeConfig: (cfg: JudgeConfig) => put<{ ok: boolean }>("/intercept/judge", cfg),

  // ---- AI监管总览(门限是否生效 / 五档动作分布 / 拦截范围) ----
  // 一次取齐 AI监管页需要的全局状态:决策引擎开关、监管模型开关、拦截范围、
  // 各档动作的历史命中计数——避免页面为每块信息各发一次请求。
  interceptGuardOverview: () =>
    get<{
      judge_enabled: boolean;
      jev_enabled: boolean;
      jev_configured: boolean;
      enabled_tools: string[];
      rules_total: number;
      rules_enabled: number;
      // 按 action(规则) 与 status(审计) 聚合的历史命中数。
      action_counts: Record<string, number>;
      status_counts: Record<string, number>;
    }>("/intercept/guard-overview"),

  // ---- commands (tool execution history, any tool) ----
  commands: (params?: { task?: string; q?: string; page?: number; size?: number }) => {
    const sp = new URLSearchParams();
    if (params?.task) sp.set("task", params.task);
    if (params?.q) sp.set("q", params.q);
    sp.set("page", String(params?.page ?? 0));
    sp.set("size", String(params?.size ?? 50));
    return get<{ commands: CommandRecord[]; total: number }>(`/commands?${sp}`);
  },
  // 按记录 id 批量删除工具执行记录（勾选删除/批量清理用；传单个 id 即单删）。
  deleteCommands: (ids: number[]) => del<{ deleted: number }>("/commands", { ids }),
  // 各工具调用次数；沿用列表的 task/q 筛选，统计的是整个结果集而非当前页。
  commandStats: (params?: { task?: string; q?: string }) => {
    const sp = new URLSearchParams();
    if (params?.task) sp.set("task", params.task);
    if (params?.q) sp.set("q", params.q);
    return get<{ stats: ToolStat[] }>(`/commands/stats?${sp}`);
  },

  // ---- LLM records ----
  llmRecords: (params?: { model?: string; session?: string; task?: string; page?: number; size?: number }) => {
    const sp = new URLSearchParams();
    if (params?.model) sp.set("model", params.model);
    if (params?.session) sp.set("session", params.session);
    if (params?.task) sp.set("task", params.task);
    if (params?.page !== undefined) sp.set("page", String(params.page));
    sp.set("size", String(params?.size ?? 50));
    return get<{ records: LLMRecordItem[]; total: number }>(`/llm/records?${sp}`);
  },
  llmRecordDetail: (id: number) => get<LLMRecordDetail>(`/llm/records/${id}`),
  llmTasks: () => get<{ tasks: LLMTask[] }>(`/llm/records/tasks`),
  llmRecordsDeleteTask: (task: string) => del<{ deleted: number }>(`/llm/records?task=${encodeURIComponent(task)}`),
  // 按记录 id 批量删除 LLM 调用记录（勾选删除/批量清理用；传单个 id 即单删）。
  deleteLlmRecords: (ids: number[]) => del<{ deleted: number }>("/llm/records", { ids }),
  // 按模型聚合本任务的 token 用量（来自常开的 llm_usage 计量账本，逐次调用精确，
  // per-agent 绑定 / 轮询 / 中断消耗都覆盖）。
  tokensByModel: (task: string) =>
    get<{ models: ModelTokenStat[] }>(`/llm/records/by-model?task=${encodeURIComponent(task)}`),

  // ---- 一键更新 ----
  // 检查以后端为准：下载是后端做的，浏览器能连 GitHub 而服务器连不上的情况很常见
  // （服务器在内网、代理只配在浏览器上），那时点更新必然失败。
  // 后端对 GitHub 的查询结果有 30 分钟缓存（未认证的 GitHub API 是 60 次/小时/IP，
  // 顶栏每次整页加载都会查一次，不缓存会很快耗光配额）。force=true 强制回源，
  // 留给用户显式点「检查更新」时用。
  checkUpdate: (force = false) => get<UpdateCheck>(`/update/check${force ? "?force=1" : ""}`),
  // 202 即返回，实际下载在后台跑，进度走 /api/update/stream。
  applyUpdate: () => post<{ ok: boolean; target: string }>(`/update/apply`),
  rollbackUpdate: () => post<{ ok: boolean }>(`/update/rollback`),

  // ---- 代理中心 ----
  // 出网状态总览：当前模式(裸网/代理池)、生效代理、全平台代理统计。
  proxyEgress: () => get<ProxyEgressStatus>(`/proxy/egress`),
  // 快速切换本地裸网 ⇄ 代理池。mode=pool 时需带 groupId。
  // 切换会重建 agent(出网地址在 agent 构造时注入)，返回切换后的最新状态。
  proxySetEgress: (mode: string, groupId = 0) =>
    put<ProxyEgressStatus>(`/proxy/egress`, { mode, group_id: groupId }),
  // 代理组列表(分页 + 搜索 + 类型筛选 + 排序)，counts 是各类型总数(供页签)。
  proxyGroups: (input?: {
    kind?: string;
    q?: string;
    sort?: string;
    desc?: boolean;
    limit?: number;
    offset?: number;
  }) => {
    const p = new URLSearchParams();
    if (input?.kind) p.set("kind", input.kind);
    if (input?.q) p.set("q", input.q);
    if (input?.sort) p.set("sort", input.sort);
    if (input?.desc === false) p.set("desc", "0");
    p.set("limit", String(input?.limit ?? 50));
    p.set("offset", String(input?.offset ?? 0));
    return get<{ groups: ProxyGroup[]; total: number; counts: Record<string, number> }>(
      `/proxy/groups?${p.toString()}`,
    ).then((r) => ({ groups: arr(r.groups), total: r.total, counts: r.counts ?? {} }));
  },
  proxyGroup: (id: number) => get<{ group: ProxyGroup }>(`/proxy/groups/${id}`).then((r) => r.group),
  createProxyGroup: (input: Partial<ProxyGroup>) => post<{ group: ProxyGroup }>(`/proxy/groups`, input),
  updateProxyGroup: (id: number, input: Partial<ProxyGroup>) =>
    put<{ group: ProxyGroup }>(`/proxy/groups/${id}`, input),
  deleteProxyGroup: (id: number) => del<{ deleted: boolean }>(`/proxy/groups/${id}`),
  // 组内代理列表(分页 + 状态/来源/协议/地区筛选 + 搜索)，counts 是各状态总数。
  proxyEntries: (
    groupId: number,
    input?: {
      status?: string;
      source?: string;
      protocol?: string;
      region?: string;
      countryCode?: string;
      q?: string;
      sort?: string;
      desc?: boolean;
      limit?: number;
      offset?: number;
    },
  ) => {
    const p = new URLSearchParams();
    if (input?.status) p.set("status", input.status);
    if (input?.source) p.set("source", input.source);
    if (input?.protocol) p.set("protocol", input.protocol);
    if (input?.region) p.set("region", input.region);
    if (input?.countryCode) p.set("country_code", input.countryCode);
    if (input?.q) p.set("q", input.q);
    if (input?.sort) p.set("sort", input.sort);
    if (input?.desc === false) p.set("desc", "0");
    p.set("limit", String(input?.limit ?? 50));
    p.set("offset", String(input?.offset ?? 0));
    return get<{ proxies: ProxyEntry[]; total: number; counts: Record<string, number> }>(
      `/proxy/groups/${groupId}/proxies?${p.toString()}`,
    ).then((r) => ({ proxies: arr(r.proxies), total: r.total, counts: r.counts ?? {} }));
  },
  createProxy: (groupId: number, input: { url: string; label?: string; region?: string }) =>
    post<{ proxy: ProxyEntry; existing: boolean }>(`/proxy/groups/${groupId}/proxies`, input),
  updateProxy: (id: number, input: { label?: string; region?: string; status?: string }) =>
    put<{ ok: boolean }>(`/proxy/${id}`, input),
  deleteProxy: (id: number) => del<{ deleted: boolean }>(`/proxy/${id}`),
  // 批量删除组内代理；status 非空时按状态清理(如清掉全部 dead)，ids 优先。
  deleteProxies: (groupId: number, input: { ids?: number[]; status?: string }) =>
    del<{ deleted: number }>(`/proxy/groups/${groupId}/proxies`, input),
  // 组上的代理池 API 接入。
  proxySources: (groupId: number) =>
    get<{ sources: ProxySource[] }>(`/proxy/groups/${groupId}/sources`).then((r) => ({ sources: arr(r.sources) })),
  createProxySource: (groupId: number, input: Record<string, unknown>) =>
    post<{ source: ProxySource }>(`/proxy/groups/${groupId}/sources`, input),
  updateProxySource: (id: number, input: Record<string, unknown>) =>
    put<{ source: ProxySource }>(`/proxy/sources/${id}`, input),
  deleteProxySource: (id: number) => del<{ deleted: boolean }>(`/proxy/sources/${id}`),
  // 立即从该源拉取一轮代理。
  syncProxySource: (id: number) =>
    post<{ imported: number; total: number; message?: string }>(`/proxy/sources/${id}/sync`),
  // 内置代理源注册表(商业 / 公益 / 在野搜集)。
  proxyProviders: () => get<{ providers: ProxyProviderDef[] }>(`/proxy/providers`).then((r) => arr(r.providers)),
  // 一键接入全部公益池 + 在野搜集源(免凭据源),省去逐个添加。
  syncAllProxySources: (groupId: number) =>
    post<{ added: number; sources: ProxySource[] }>(`/proxy/groups/${groupId}/sources/auto`),
  // 触发测活。ids 为空则测整组。
  checkProxies: (groupId: number, ids?: number[]) =>
    post<{ run_id: number; checking: number }>(`/proxy/groups/${groupId}/check`, { ids: ids ?? [] }),
  // 测活批次台账。
  proxyCheckRuns: (groupId: number, limit = 20) =>
    get<{ runs: ProxyCheckRun[] }>(`/proxy/check-runs?group_id=${groupId}&limit=${limit}`).then((r) => ({
      runs: arr(r.runs),
    })),
  // 仪表盘:总览 + 时间序列 + 按代理的调度排行(一条请求取齐)。
  proxyDashboard: (input?: { window_mins?: number; group_id?: number }) => {
    const p = new URLSearchParams();
    if (input?.window_mins) p.set("window_mins", String(input.window_mins));
    if (input?.group_id) p.set("group_id", String(input.group_id));
    const qs = p.toString();
    return get<{
      summary: ProxyTrafficSummary;
      series: ProxyTrafficStat[];
      usage: ProxyUsageStat[];
      egress: ProxyEgressStatus;
    }>(`/proxy/dashboard${qs ? `?${qs}` : ""}`).then((r) => ({
      summary: r.summary ?? {
        total: 0,
        failed: 0,
        success_rate: 0,
        avg_ms: 0,
        bytes_in: 0,
        bytes_out: 0,
        window_mins: input?.window_mins ?? 60,
      },
      series: arr(r.series),
      usage: arr(r.usage),
      egress: r.egress,
    }));
  },
  // 请求流水(分页 + 筛选)。
  proxyRequests: (input?: {
    group_id?: number;
    proxy_id?: number;
    task_id?: number;
    only_failed?: boolean;
    host?: string;
    q?: string;
    limit?: number;
    offset?: number;
  }) => {
    const p = new URLSearchParams();
    if (input?.group_id) p.set("group_id", String(input.group_id));
    if (input?.proxy_id) p.set("proxy_id", String(input.proxy_id));
    if (input?.task_id) p.set("task_id", String(input.task_id));
    if (input?.only_failed) p.set("only_failed", "1");
    if (input?.host) p.set("host", input.host);
    if (input?.q) p.set("q", input.q);
    p.set("limit", String(input?.limit ?? 50));
    p.set("offset", String(input?.offset ?? 0));
    return get<{ requests: ProxyRequest[]; total: number }>(`/proxy/requests?${p.toString()}`).then((r) => ({
      requests: arr(r.requests),
      total: r.total,
    }));
  },
  // 清空请求流水(窗口之前的)。
  purgeProxyRequests: (beforeMins = 0) =>
    del<{ deleted: number }>(`/proxy/requests${beforeMins > 0 ? `?before_mins=${beforeMins}` : ""}`),
  // 地区分布(代理列表的国家筛选器用);group_id 省略 = 全平台。
  // pending 是「已拿到出口 IP 但还没识别国家」的条数,pending 归零即识别完成。
  proxyCountries: (groupId = 0) =>
    get<{ countries: ProxyCountryStat[]; pending: number; enabled: boolean }>(
      `/proxy/countries${groupId > 0 ? `?group_id=${groupId}` : ""}`,
    ).then((r) => ({ countries: arr(r.countries), pending: r.pending ?? 0, enabled: r.enabled !== false })),
  // 手动触发一轮地区识别(同步执行,受主动限速约束故单次上限 300)。
  resolveProxyGeo: (groupId: number, limit = 60) =>
    post<{ resolved: number; failed: number; updated: number; pending: number }>(
      `/proxy/groups/${groupId}/geo`,
      { limit },
    ),

  // ============================================================
  // VPS 中心:任务级执行环境
  // ============================================================
  // 仪表盘:总览 + 执行量序列 + 占用机器 + 最近流水(一条请求取齐,仿 proxyDashboard)。
  vpsDashboard: (windowHours = 24) =>
    get<VpsDashboard>(`/vps/dashboard?window_h=${windowHours}`).then((r) => ({
      summary:
        r.summary ??
        {
          total: 0,
          online: 0,
          offline: 0,
          busy: 0,
          maintenance: 0,
          executions_today: 0,
          active_tasks: 0,
        },
      series: arr(r.series),
      busy_machines: arr(r.busy_machines),
      recent: arr(r.recent),
    })),
  // 机器池(分页 + 筛选)。status/tag/provider 省略 = 全部;total 为当前筛选视图条数。
  vpsMachines: (input?: {
    status?: string;
    provider?: string;
    tag?: string;
    q?: string;
    limit?: number;
    offset?: number;
  }) => {
    const p = new URLSearchParams();
    if (input?.status) p.set("status", input.status);
    if (input?.provider) p.set("provider", input.provider);
    if (input?.tag) p.set("tag", input.tag);
    if (input?.q) p.set("q", input.q);
    p.set("limit", String(input?.limit ?? 50));
    p.set("offset", String(input?.offset ?? 0));
    return get<{ machines: VpsMachine[]; total: number }>(`/vps/machines?${p.toString()}`).then((r) => ({
      machines: arr(r.machines),
      total: r.total,
    }));
  },
  // 录入机器(录入即自动测活 + 系统信息采集,同步返回带初始状态的机器)。
  createVpsMachine: (input: VpsMachineInput) => post<VpsMachine>("/vps/machines", input),
  // 编辑机器(凭据字段缺省 = 不改;status 仅维护状态切换走这里)。
  updateVpsMachine: (id: number, input: Partial<VpsMachineInput>) => put<VpsMachine>(`/vps/machines/${id}`, input),
  deleteVpsMachine: (id: number) => del<{ deleted: boolean }>(`/vps/machines/${id}`),
  // 手动测活(同步,几秒内返回;顺带刷新系统信息)。
  checkVpsMachine: (id: number) => post<VpsMachine>(`/vps/machines/${id}/check`),
  // 工具检测(同步跑一轮已知工具探测,返回最新矩阵)。
  detectVpsMachine: (id: number) =>
    post<{ detections: VpsToolDetection[] }>(`/vps/machines/${id}/detect`).then((r) => ({
      detections: arr(r.detections),
    })),
  vpsMachine: (id: number) => get<VpsMachineDetail>(`/vps/machines/${id}`).then((r) => ({
    machine: r.machine,
    detections: arr(r.detections),
    current_task: r.current_task ?? null,
  })),
  // 单命令执行(手动试跑框)。timeout_sec 缺省 = 服务端默认(120s)。
  vpsExec: (machineId: number, command: string, timeoutSec?: number) =>
    post<VpsExecResult>(`/vps/exec`, { machine_id: machineId, command, timeout_sec: timeoutSec }),
  // 执行流水(按机器或任务筛选;machineId/taskId 至少给一个,0 = 不限)。
  vpsExecutions: (input: { machineId?: number; taskId?: number; source?: string; q?: string; status?: string; limit?: number; offset?: number }) => {
    const p = new URLSearchParams();
    if (input.machineId) p.set("machine_id", String(input.machineId));
    if (input.taskId) p.set("task_id", String(input.taskId));
    if (input.source) p.set("source", input.source);
    if (input.q) p.set("q", input.q);
    if (input.status) p.set("status", input.status);
    p.set("limit", String(input.limit ?? 50));
    p.set("offset", String(input.offset ?? 0));
    return get<{ executions: VpsExecution[]; total: number }>(`/vps/executions?${p.toString()}`).then((r) => ({
      executions: arr(r.executions),
      total: r.total,
    }));
  },
  // 某条流水的完整输出(命令输出可能被截断,完整版在这里)。
  vpsExecutionOutput: (id: number) => httpText(`/vps/executions/${id}/output`),
  // 指派历史(machineId=0 时按全部机器)。
  vpsAssignments: (machineId: number, limit = 50) =>
    get<{ assignments: VpsAssignment[] }>(`/vps/assignments?machine_id=${machineId}&limit=${limit}`).then((r) => ({
      assignments: arr(r.assignments),
    })),
  // Web 终端 WebSocket 地址(wsUrl 直连后端,token 走 query)。
  vpsTerminalUrl: (machineId: number) => `/api/vps/machines/${machineId}/terminal`,

  // 工具集 CRUD。
  vpsToolsets: () => get<{ toolsets: VpsToolset[] }>("/vps/toolsets").then((r) => ({ toolsets: arr(r.toolsets) })),
  createVpsToolset: (input: { name: string; description?: string; script: string }) =>
    post<VpsToolset>("/vps/toolsets", input),
  updateVpsToolset: (id: number, input: { name?: string; description?: string; script?: string }) =>
    put<VpsToolset>(`/vps/toolsets/${id}`, input),
  deleteVpsToolset: (id: number) => del<{ deleted: boolean }>(`/vps/toolsets/${id}`),
  // 应用工具集到机器(同步执行脚本,可能耗时较长;结果落执行流水 source=toolset)。
  applyVpsToolset: (id: number, machineIds: number[]) =>
    post<{ results: { machine_id: number; ok: boolean; exit_code: number | null; error: string; execution_id: number }[] }>(
      `/vps/toolsets/${id}/apply`,
      { machine_ids: machineIds },
    ),

  // 任务 ↔ 机器指派:创建任务后在任务详情指派/解绑,或创建时带 vps 字段。
  assignTaskVps: (taskId: number, machineId: number | "auto") =>
    post<{ machine: VpsMachine | null }>(`/tasks/${taskId}/vps`, {
      machine_id: machineId === "auto" ? 0 : machineId,
      auto: machineId === "auto",
    }),
  releaseTaskVps: (taskId: number) => del<{ released: boolean }>(`/tasks/${taskId}/vps`),
  // 任务详情「执行环境」tab 数据。
  taskVpsEnvironment: (taskId: number) =>
    get<TaskVpsEnvironment>(`/tasks/${taskId}/vps-environment`).then((r) => ({
      machine: r.machine ?? null,
      executions: arr(r.executions),
    })),

  // ── C2 中心（契约 C2_CONTRACTS.md;类型与路由以冻结版为准）──
  c2Overview: () => get<C2Overview>("/c2/overview").then((r) => ({
    ...r,
    recent_sessions: arr(r.recent_sessions),
    recent_tasks: arr(r.recent_tasks),
  })),
  // 监听器 CRUD + 启停。start/stop 返回运行时实际监听地址。
  c2Listeners: () => get<{ listeners: C2Listener[]; total: number }>("/c2/listeners").then((r) => ({
    listeners: arr(r.listeners),
    total: r.total,
  })),
  c2Listener: (id: number) => get<C2Listener>(`/c2/listeners/${id}`),
  c2CreateListener: (input: C2ListenerInput) => post<{ id: number }>("/c2/listeners", input),
  c2UpdateListener: (id: number, input: Partial<C2ListenerInput>) => put<C2Listener>(`/c2/listeners/${id}`, input),
  c2DeleteListener: (id: number) => del<{ deleted: boolean }>(`/c2/listeners/${id}`),
  c2StartListener: (id: number) => post<{ addr: string }>(`/c2/listeners/${id}/start`),
  c2StopListener: (id: number) => post<{ stopped: boolean }>(`/c2/listeners/${id}/stop`),
  // 会话列表/详情/管控。companyId/taskId 传 0 = 不过滤。
  c2Sessions: (input: { status?: string; listenerId?: number; taskId?: number; companyId?: number; q?: string; limit?: number; offset?: number }) => {
    const p = new URLSearchParams();
    if (input.status) p.set("status", input.status);
    if (input.listenerId) p.set("listener_id", String(input.listenerId));
    if (input.taskId) p.set("task_id", String(input.taskId));
    if (input.companyId) p.set("company_id", String(input.companyId));
    if (input.q) p.set("q", input.q);
    p.set("limit", String(input.limit ?? 50));
    p.set("offset", String(input.offset ?? 0));
    return get<{ sessions: C2Session[]; total: number }>(`/c2/sessions?${p.toString()}`).then((r) => ({
      sessions: arr(r.sessions),
      total: r.total,
    }));
  },
  c2Session: (id: number) => get<C2Session>(`/c2/sessions/${id}`),
  c2KillSession: (id: number) => post<{ killed: boolean }>(`/c2/sessions/${id}/kill`),
  c2SleepSession: (id: number, sleepSec: number, jitterPct: number) =>
    post<{ ok: boolean }>(`/c2/sessions/${id}/sleep`, { sleep_sec: sleepSec, jitter_pct: jitterPct }),
  c2BindSession: (id: number, taskId: number) => post<{ ok: boolean }>(`/c2/sessions/${id}/bind`, { task_id: taskId }),
  // 会话任务:增量拉取(afterId=已见最大 id)+下发(shell/files_list/info)。
  c2SessionTasks: (sessionId: number, afterId = 0, limit = 100) =>
    get<{ tasks: C2Task[] }>(`/c2/sessions/${sessionId}/tasks?after_id=${afterId}&limit=${limit}`).then((r) => ({
      tasks: arr(r.tasks),
    })),
  c2CreateTask: (sessionId: number, kind: "shell" | "files_list" | "info", input = "") =>
    post<{ id: number }>(`/c2/sessions/${sessionId}/tasks`, { kind, input }),
  c2TaskOutput: (id: number) => httpText(`/c2/tasks/${id}/output`),
  // 文件:浏览(files_list 任务)、上传(multipart,勿传 JSON 头)、下载(建 transfer 轮询)。
  c2FilesList: (sessionId: number, path: string) =>
    post<{ task_id: number }>(`/c2/sessions/${sessionId}/files/list`, { path }),
  c2UploadFile: (sessionId: number, file: File, remoteDir: string) => {
    const fd = new FormData();
    fd.append("file", file);
    fd.append("path", remoteDir);
    return post<{ transfer_id: number }>(`/c2/sessions/${sessionId}/files/upload`, fd);
  },
  c2DownloadFile: (sessionId: number, remotePath: string) =>
    post<{ transfer_id: number }>(`/c2/sessions/${sessionId}/files/download`, { remote_path: remotePath }),
  c2SessionTransfers: (sessionId: number, limit = 50) =>
    get<{ transfers: C2Transfer[] }>(`/c2/sessions/${sessionId}/transfers?limit=${limit}`).then((r) => ({
      transfers: arr(r.transfers),
    })),
  c2TransferDownloadUrl: (id: number) => `/api/c2/transfers/${id}/download`,
  // 战利品。列表 content 恒空串;明文走详情接口。
  c2Loot: (input: { kind?: string; sessionId?: number; q?: string; limit?: number; offset?: number }) => {
    const p = new URLSearchParams();
    if (input.kind) p.set("kind", input.kind);
    if (input.sessionId) p.set("session_id", String(input.sessionId));
    if (input.q) p.set("q", input.q);
    p.set("limit", String(input.limit ?? 50));
    p.set("offset", String(input.offset ?? 0));
    return get<{ loot: C2Loot[]; total: number }>(`/c2/loot?${p.toString()}`).then((r) => ({
      loot: arr(r.loot),
      total: r.total,
    }));
  },
  c2LootDetail: (id: number) => get<C2Loot>(`/c2/loot/${id}`),
  c2CreateLoot: (input: { session_id: number; kind: C2LootKind; name: string; content: string; source_path?: string }) =>
    post<{ id: number }>("/c2/loot", input),
  c2DeleteLoot: (id: number) => del<{ deleted: boolean }>(`/c2/loot/${id}`),
  // 载荷。生成时 callback 为空的监听器会被 400 拒绝(先去监听器页补回调地址)。
  c2Payloads: (listenerId?: number) => {
    const p = new URLSearchParams();
    if (listenerId) p.set("listener_id", String(listenerId));
    return get<{ payloads: C2Payload[]; total: number }>(`/c2/payloads?${p.toString()}`).then((r) => ({
      payloads: arr(r.payloads),
      total: r.total,
    }));
  },
  c2GeneratePayload: (input: { listener_id: number; format: C2PayloadFormat; target?: C2PayloadTarget; name?: string; evasion_profile_id?: number; build_vps_machine_id?: number; artifact?: C2PayloadArtifact; direction?: C2Direction; staged?: boolean }) =>
    post<{ id: number; oneliner: string; job_id?: number }>("/c2/payloads", input),
  c2DeletePayload: (id: number) => del<{ deleted: boolean }>(`/c2/payloads/${id}`),
  c2PayloadDownloadUrl: (id: number) => `/api/c2/payloads/${id}/download`,

  // ── C2 二期(契约 §11-§16):免杀工作台 / 异步作业 / loot 转发现 ──
  // 免杀档案 CRUD。
  c2EvasionProfiles: (input?: { av?: string; format?: string }) => {
    const p = new URLSearchParams();
    if (input?.av) p.set("target_av", input.av);
    if (input?.format) p.set("payload_format", input.format);
    return get<{ profiles: C2EvasionProfile[]; total: number }>(`/c2/evasion/profiles?${p.toString()}`).then((r) => ({
      profiles: arr(r.profiles),
      total: r.total,
    }));
  },
  c2CreateEvasionProfile: (input: C2EvasionProfileInput) => post<{ id: number }>("/c2/evasion/profiles", input),
  c2UpdateEvasionProfile: (id: number, input: Partial<C2EvasionProfileInput>) =>
    put<C2EvasionProfile>(`/c2/evasion/profiles/${id}`, input),
  c2DeleteEvasionProfile: (id: number) => del<{ deleted: boolean }>(`/c2/evasion/profiles/${id}`),
  // AI 变形生成:异步 job,202 返回 job_id;同档案+监听器已有 running 作业时 409。
  c2EvasionGenerate: (input: { listener_id: number; format: C2PayloadFormat; target?: C2PayloadTarget; name?: string; profile_id?: number; instructions?: string }) =>
    post<{ job_id: number }>("/c2/evasion/generate", input),
  // 异步作业(evasion|build)。
  c2Jobs: (input?: { kind?: C2JobKind; status?: C2JobStatus; listenerId?: number; limit?: number; offset?: number }) => {
    const p = new URLSearchParams();
    if (input?.kind) p.set("kind", input.kind);
    if (input?.status) p.set("status", input.status);
    if (input?.listenerId) p.set("listener_id", String(input.listenerId));
    p.set("limit", String(input?.limit ?? 50));
    p.set("offset", String(input?.offset ?? 0));
    return get<{ jobs: C2Job[]; total: number }>(`/c2/jobs?${p.toString()}`).then((r) => ({
      jobs: arr(r.jobs),
      total: r.total,
    }));
  },
  c2Job: (id: number) => get<C2Job>(`/c2/jobs/${id}`),
  c2RetryJob: (id: number) => post<{ job_id: number }>(`/c2/jobs/${id}/retry`),
  // 战利品转发现(需会话已绑定任务;重复转换 409)。
  c2LootToFinding: (id: number) => post<{ finding_id: number }>(`/c2/loot/${id}/to-finding`),

  // ── C2 三期(契约 §17-§23):正向连接 / 插件板块 ──
  // 正向连接:平台主动 dial 目标(bind beacon 在监听),仅 tcp/kcp 传输。
  c2ConnectBind: (input: { listener_id: number; host: string; port: number }) =>
    post<{ session_id: number }>("/c2/sessions/connect", input),
  // 监听器测活(契约 §24B):local 探端口/vps 探转发链/oss 验桶/kcp 进程级。
  c2CheckListener: (id: number) =>
    post<C2ListenerCheck>(`/c2/listeners/${id}/check`),
  // 插件库 CRUD(内置插件 builtin=true 不可改删)。
  c2Plugins: (input?: { platform?: string; kind?: string; q?: string; limit?: number; offset?: number }) => {
    const p = new URLSearchParams();
    if (input?.platform) p.set("platform", input.platform);
    if (input?.kind) p.set("kind", input.kind);
    if (input?.q) p.set("q", input.q);
    p.set("limit", String(input?.limit ?? 50));
    p.set("offset", String(input?.offset ?? 0));
    return get<{ plugins: C2Plugin[]; total: number }>(`/c2/plugins?${p.toString()}`).then((r) => ({
      plugins: arr(r.plugins),
      total: r.total,
    }));
  },
  c2CreatePlugin: (input: C2PluginInput) => post<{ id: number }>("/c2/plugins", input),
  c2UpdatePlugin: (id: number, input: Partial<C2PluginInput>) => put<C2Plugin>(`/c2/plugins/${id}`, input),
  c2DeletePlugin: (id: number) => del<{ deleted: boolean }>(`/c2/plugins/${id}`),
  // 会话内运行插件:script 直走命令通道,binary 走文件通道;结果进任务流水。
  c2RunPlugin: (sessionId: number, pluginId: number, params: Record<string, string>) =>
    post<{ task_id: number }>(`/c2/sessions/${sessionId}/plugins/${pluginId}/run`, { params }),
};
