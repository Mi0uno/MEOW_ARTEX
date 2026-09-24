// LLM 模块的共用常量与展示辅助（列表页 / 详情页 / 快速编辑抽屉三方共用）。
//
// 抽出来的原因：改造后同一套语义要在三处出现——协议格式的标签与配色、空值哨兵
// （"none" ↔ ""）、健康度文案。散在各文件里各写一份，迟早出现「列表说已熔断、
// 详情说正常」这种自相矛盾。凡是「同一件事的两种说法」都收到这里。

import type { LLMPoolMember, LLMProfile } from "@/lib/types";

// ─────────────────────────────────────────────────────────────────────────────
// 协议格式
// ─────────────────────────────────────────────────────────────────────────────

/** 后端 LLMProfileDTO.format 只有这三种（db.LLMProfile.Format）。 */
export type LLMFormat = "anthropic" | "openai" | "openai-responses";

export const LLM_FORMATS: LLMFormat[] = ["anthropic", "openai", "openai-responses"];

/** 短名：给页签与表格徽标用。页签上四个标签横排，全称会把 TabsList 撑得很宽。 */
export const FORMAT_LABEL_SHORT: Record<string, string> = {
  anthropic: "Anthropic",
  openai: "OpenAI Chat",
  "openai-responses": "OpenAI Responses",
};

/** 全称：下拉选项与详情页字段用，含 API 名字，避免与短名混淆。 */
export const FORMAT_LABEL_LONG: Record<string, string> = {
  anthropic: "Anthropic (Messages API)",
  openai: "OpenAI (Chat Completions)",
  "openai-responses": "OpenAI (Responses API)",
};

/** 格式徽标配色：列表里要能一眼扫出哪几条是同一种协议。 */
export const FORMAT_BADGE_CLASS: Record<string, string> = {
  anthropic: "border-violet-500/40 bg-violet-500/10 text-violet-600 dark:text-violet-400",
  openai: "border-emerald-500/40 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400",
  "openai-responses": "border-blue-500/40 bg-blue-500/10 text-blue-600 dark:text-blue-400",
};

/** 未知格式（后端将来新增）也要有字面量可显示，不能空白。 */
export function formatLabel(f?: string): string {
  return FORMAT_LABEL_LONG[f ?? ""] ?? (f ? f : "—");
}

export function formatBadgeClass(f?: string): string {
  return FORMAT_BADGE_CLASS[f ?? ""] ?? "bg-muted text-muted-foreground";
}

// ─────────────────────────────────────────────────────────────────────────────
// 空值哨兵：存库空字符串 = 该字段【不发送】
// ─────────────────────────────────────────────────────────────────────────────
//
// Radix Select 不接受空 value，故 UI 用 "none" 哨兵表示「不发送」，存取时与 "" 互转。
// thinking.type 与 reasoning_effort 是两个【互相独立】的字段，各自单独设置——有些接口
// 没有 thinking 字段、只靠强度参数就能激活思考，故需解耦。

export const NONE = "none";
export const fromStore = (v?: string) => (v ? v : NONE);
export const toStore = (v: string) => (v === NONE ? "" : v);

export const THINKING_TYPES: { value: string; label: string }[] = [
  { value: NONE, label: "不发送（默认）" },
  { value: "disabled", label: "关闭" },
  { value: "enabled", label: "开启" },
];

export const EFFORT_LEVELS: { value: string; label: string }[] = [
  { value: NONE, label: "不发送（默认）" },
  { value: "low", label: "low" },
  { value: "medium", label: "medium" },
  { value: "high", label: "high" },
  { value: "xhigh", label: "xhigh" },
  { value: "max", label: "max" },
];

/** 输出上限用哪个请求字段名（仅 openai 格式有意义）。NONE ↔ "" 走同一套哨兵转换。 */
export const MAX_TOKENS_FIELDS: { value: string; label: string }[] = [
  { value: NONE, label: "max_tokens（默认）" },
  { value: "max_completion_tokens", label: "max_completion_tokens" },
];

/** 另外两种格式各自定死了字段名，选项对它们无意义，说明文案里直接讲清楚。 */
export const MAX_TOKENS_FIELD_HINTS: Record<string, string> = {
  openai:
    "上限发哪个键。max_tokens 是默认，绝大多数兼容网关只认它；OpenAI 官方推理模型（o 系列 / GPT-5）反过来只认 max_completion_tokens，收到 max_tokens 会直接报 unsupported_parameter。",
  anthropic: "仅 openai 格式可选。Anthropic 的字段名固定为 max_tokens。",
  "openai-responses": "仅 openai 格式可选。Responses API 的字段名固定为 max_output_tokens。",
};

// 按 format 的 Base URL 占位与常用端点预设（一键填充）。
// 注意契约差异：anthropic 的 base 不带 /v1（SDK 自己拼 /v1/messages）；
// openai / openai-responses 的 base 带 /v1（SDK 拼 /chat/completions 或 /responses）。
export const BASE_URL_PRESETS: Record<string, { placeholder: string; presets: { label: string; url: string }[] }> = {
  anthropic: {
    placeholder: "留空 = 官方 https://api.anthropic.com（不带 /v1）",
    presets: [
      { label: "官方 Anthropic", url: "" },
      { label: "网关转发(Gateway)", url: "https://your-gateway.example.com/anthropic" },
    ],
  },
  openai: {
    placeholder: "留空 = 官方 https://api.openai.com/v1；兼容网关一般填到 /v1",
    presets: [
      { label: "官方 OpenAI", url: "" },
      { label: "Ollama", url: "http://127.0.0.1:11434/v1" },
      { label: "vLLM", url: "http://127.0.0.1:8000/v1" },
      { label: "LM Studio", url: "http://127.0.0.1:1234/v1" },
      { label: "DeepSeek", url: "https://api.deepseek.com/v1" },
      { label: "Gemini 兼容", url: "https://generativelanguage.googleapis.com/v1beta/openai" },
      { label: "one-api/new-api", url: "http://127.0.0.1:3000/v1" },
    ],
  },
  "openai-responses": {
    placeholder: "留空 = 官方 https://api.openai.com/v1",
    presets: [{ label: "官方 OpenAI", url: "" }],
  },
};

// ─────────────────────────────────────────────────────────────────────────────
// 展示辅助
// ─────────────────────────────────────────────────────────────────────────────

/** 后端时间串可能是空/非法（老数据），统一兜成 "—"，别渲染出 "Invalid Date"。 */
export function fmtTime(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

/** 冷却剩余秒数的人话。60s 以下给秒（要看着它走完），以上给分钟。 */
export function cooldownText(secs: number): string {
  if (secs <= 0) return "";
  if (secs < 60) return `${secs}s`;
  return `${Math.ceil(secs / 60)}min`;
}

/** 上下文窗口的展示值：0 = 后端按 200K 默认处理，列表里直接显示实际生效值更直观。 */
export function contextWindowText(k?: number): string {
  return `${k && k > 0 ? k : 200}K`;
}

/** 上下文窗口是否用的是默认值（用于淡化显示，提醒这是「没设」而不是「设成 200」）。 */
export function contextWindowIsDefault(k?: number): boolean {
  return !k || k <= 0;
}

// 一个配置的「是否正常」。没填 Key 的配置根本发不出请求，比熔断更该先说；
// 其余状态来自轮询的熔断记录（轮询关着时不会产生新记录，此时「正常」= 没有已知故障）。
export type Health = { label: string; cls: string; hint?: string };

export function healthOf(p: LLMProfile, m?: LLMPoolMember): Health {
  if (!p.api_key_hint) {
    return {
      label: "未配置 Key",
      cls: "border-muted-foreground/40 text-muted-foreground",
      hint: "未填 API Key，无法调用",
    };
  }
  if (m?.state === "tripped") {
    return {
      label: m.cooldown_secs > 0 ? `已熔断 · ${cooldownText(m.cooldown_secs)}` : "已熔断",
      cls: "border-destructive/50 text-destructive",
      hint: m.last_error,
    };
  }
  if (m?.state === "degraded") {
    return {
      label: `异常 · 失败 ${m.fails} 次`,
      cls: "border-amber-500/50 text-amber-600 dark:text-amber-400",
      hint: m.last_error,
    };
  }
  return { label: "正常", cls: "border-emerald-500/50 text-emerald-600 dark:text-emerald-400" };
}

/** 是否处于「熔断中」——列表的状态筛选与详情页的运行时卡片共用同一判据。 */
export function isTripped(p: LLMProfile, m?: LLMPoolMember): boolean {
  return !!p.api_key_hint && m?.state === "tripped";
}
