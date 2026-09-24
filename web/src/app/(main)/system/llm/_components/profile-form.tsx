"use client";

// LLM 配置表单的共用件：一个 hook（状态 + 拉模型 + 测连接 + 组装保存体）
// 与一套字段组件（全部可编辑字段的呈现）。
//
// 拆出来的原因：同一套字段现在有两个宿主——列表页的快速编辑抽屉（ProfileSheet）
// 与专属详情页（/system/llm/detail）。字段若各写一份，迟早出现「抽屉里能改的
// 详情页改不了」这类缺口。表单状态与保存体组装只此一处，两个宿主共用。

import * as React from "react";

import { Loader2Icon, RefreshCwIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import type { LLMProfile, LLMRetryOverride } from "@/lib/types";

import {
  BASE_URL_PRESETS,
  EFFORT_LEVELS,
  FORMAT_LABEL_LONG,
  fromStore,
  LLM_FORMATS,
  type LLMFormat,
  MAX_TOKENS_FIELD_HINTS,
  MAX_TOKENS_FIELDS,
  NONE,
  THINKING_TYPES,
  toStore,
} from "./llm-shared";
import { ProfileRetryFields, ZERO_OVERRIDE } from "./retry";

/** 表单里的原始文本值。数字类字段留字符串：中间态（"-"、"1e"）不该被强行 parse 掉。 */
export interface ProfileFormValues {
  name: string;
  format: LLMFormat;
  model: string;
  baseUrl: string;
  proxy: string;
  apiKey: string;
  rps: string;
  rpm: string;
  cw: string;
  thinkingType: string; // NONE 哨兵（"none"）= 不发送
  effort: string; // 同上
  priority: string;
  poolExclude: boolean;
  streaming: boolean;
  maxTokens: string;
  maxTokensField: string; // 同上；仅 openai 有意义
  sessionHeaderKey: string;
  retry: LLMRetryOverride;
}

export const EMPTY_VALUES: ProfileFormValues = {
  name: "",
  format: "anthropic",
  model: "",
  baseUrl: "",
  proxy: "",
  apiKey: "",
  rps: "0",
  rpm: "0",
  cw: "0",
  thinkingType: NONE,
  effort: NONE,
  priority: "0",
  poolExclude: false,
  streaming: true,
  maxTokens: "0",
  maxTokensField: NONE,
  sessionHeaderKey: "",
  retry: ZERO_OVERRIDE,
};

/** 把一份后端配置灌成表单值。新建传 null 得默认值。 */
export function valuesFromProfile(p: LLMProfile | null): ProfileFormValues {
  if (!p) return { ...EMPTY_VALUES, retry: ZERO_OVERRIDE };
  return {
    name: p.name ?? "",
    format: p.format === "openai" || p.format === "openai-responses" ? p.format : "anthropic",
    model: p.model ?? "",
    baseUrl: p.base_url ?? "",
    proxy: p.proxy ?? "",
    apiKey: "",
    rps: String(p.rate_per_second ?? 0),
    rpm: String(p.rate_per_minute ?? 0),
    cw: String(p.context_window_k ?? 0),
    thinkingType: fromStore(p.thinking_type),
    effort: fromStore(p.reasoning_effort),
    priority: String(p.priority ?? 0),
    poolExclude: p.pool_exclude ?? false,
    streaming: p.streaming ?? true,
    maxTokens: String(p.max_tokens ?? 0),
    maxTokensField: fromStore(p.max_tokens_field),
    sessionHeaderKey: p.session_header_key ?? "",
    retry: p.retry ?? ZERO_OVERRIDE,
  };
}

/** 组装保存体。api_key 留空 = 后端保持原 Key 不变（新建时即空 Key）。 */
export function buildPayload(v: ProfileFormValues, profileId?: number) {
  return {
    ...(profileId ? { id: profileId } : {}),
    name: v.name.trim(),
    format: v.format,
    model: v.model.trim(),
    base_url: v.baseUrl.trim(),
    proxy: v.proxy.trim(),
    api_key: v.apiKey,
    rate_per_second: Number(v.rps) || 0,
    rate_per_minute: Number(v.rpm) || 0,
    context_window_k: Number(v.cw) || 0,
    thinking_type: toStore(v.thinkingType),
    reasoning_effort: toStore(v.effort),
    priority: Number(v.priority) || 0,
    pool_exclude: v.poolExclude,
    streaming: v.streaming,
    max_tokens: Math.max(0, Number(v.maxTokens) || 0),
    // 字段名开关只对 openai(Chat Completions) 有意义，其它格式一律回落到默认；
    // 后端也会再做一次同样的归一化，这里只是别让 UI 送出自相矛盾的值。
    max_tokens_field: v.format === "openai" ? toStore(v.maxTokensField) : "",
    session_header_key: v.sessionHeaderKey.trim(),
    retry: v.retry,
  };
}

/** 保存前的必填校验，返回错误文案（null = 通过）。 */
export function validateValues(v: ProfileFormValues): string | null {
  if (!v.name.trim() || !v.model.trim()) return "请填写名称与模型";
  return null;
}

export interface ProfileForm {
  v: ProfileFormValues;
  patch: (p: Partial<ProfileFormValues>) => void;
  /** 已存配置的 id（新建为 undefined）；拉模型 / 测连接时后端用它回退到已存的 Key。 */
  profileId?: number;
  keyHint: string;
  models: string[];
  loadingModels: boolean;
  /** 拉取可用模型列表并回显（成功时返回列表，便于宿主决定是否展开下拉）。 */
  loadModels: () => Promise<string[]>;
  testing: boolean;
  testConnection: () => Promise<void>;
}

/**
 * 配置表单的状态机。hydrate(profile) 把一份配置灌进来（新建传 null）。
 * 两个宿主（抽屉 / 详情页）都调它，因此字段语义只在这里定义一次。
 */
export function useProfileForm(): ProfileForm & { hydrate: (p: LLMProfile | null) => void } {
  const [v, setV] = React.useState<ProfileFormValues>(EMPTY_VALUES);
  const [profileId, setProfileId] = React.useState<number | undefined>(undefined);
  const [keyHint, setKeyHint] = React.useState("");
  const [models, setModels] = React.useState<string[]>([]);
  const [loadingModels, setLoadingModels] = React.useState(false);
  const [testing, setTesting] = React.useState(false);

  // loadModels / testConnection 要读「最新」的表单值，但它们被当成稳定的回调
  // 传给按钮，闭包里的 v 会是旧的。用 ref 兜住最新值。
  const vRef = React.useRef(v);
  vRef.current = v;
  const idRef = React.useRef(profileId);
  idRef.current = profileId;

  const hydrate = React.useCallback((p: LLMProfile | null) => {
    setV(valuesFromProfile(p));
    setKeyHint(p?.api_key_hint ?? "");
    setModels([]);
    const pid = p ? Number(p.id) : Number.NaN;
    setProfileId(Number.isFinite(pid) && pid > 0 ? pid : undefined);
    // 固化的模型列表：编辑已存端点时，回填最近一次成功拉取的列表（可直接选用）。
    if (Number.isFinite(pid) && pid > 0) {
      api
        .llmModelsCache(pid)
        .then(setModels)
        .catch(() => undefined);
    }
  }, []);

  const patch = React.useCallback((p: Partial<ProfileFormValues>) => {
    setV((cur) => ({ ...cur, ...p }));
  }, []);

  const loadModels = React.useCallback(async (): Promise<string[]> => {
    setLoadingModels(true);
    setModels([]);
    try {
      const cur = vRef.current;
      const r = await api.fetchLLMModels(cur.format, cur.baseUrl, cur.apiKey, cur.proxy, idRef.current);
      if (r.ok && r.models && r.models.length > 0) {
        setModels(r.models);
        toast.success(`已加载 ${r.models.length} 个模型`);
        return r.models;
      }
      toast.error(`加载模型失败：${r.error ?? "未获取到模型"}`);
      return [];
    } catch (e) {
      toast.error(`加载模型出错：${(e as Error).message}`);
      return [];
    } finally {
      setLoadingModels(false);
    }
  }, []);

  const testConnection = React.useCallback(async () => {
    setTesting(true);
    try {
      // 用配置实际会跑的思考参数来测，这样不支持该字段的模型在这里就失败，
      // 而不是等到跑任务时才炸。传 profile id：Key 输入框留空时用已存的 Key。
      const cur = vRef.current;
      const r = await api.testLLM(
        cur.format,
        cur.model,
        cur.baseUrl,
        cur.apiKey,
        cur.proxy,
        toStore(cur.thinkingType),
        toStore(cur.effort),
        idRef.current,
        cur.streaming,
        cur.sessionHeaderKey.trim(),
      );
      // 回复内容一并展示：看得见模型确实说了话，才算和会话里跑通是一回事。
      if (r.ok) {
        toast.success(`连接成功 · ${r.latency_ms ?? "?"}ms · ${r.model ?? cur.model}`, {
          description: r.reply ? `回复：${r.reply}` : undefined,
        });
      } else {
        toast.error(`连接失败：${r.error ?? "未知"}`);
      }
    } catch (e) {
      toast.error(`测试出错：${(e as Error).message}`);
    } finally {
      setTesting(false);
    }
  }, []);

  return { v, patch, profileId, keyHint, models, loadingModels, loadModels, testing, testConnection, hydrate };
}

// ─────────────────────────────────────────────────────────────────────────────
// 字段呈现
// ─────────────────────────────────────────────────────────────────────────────

/**
 * 全部可编辑字段。idPrefix 用来保证同一页出现两份表单时 label 的 htmlFor 不撞车
 * （目前抽屉与详情页不会同屏，但接口留好，别等以后踩）。
 * retryExtra 给详情页塞「全局策略在哪调」的指路文案——列表页抽屉里不需要。
 */
export function ProfileFields({
  form,
  idPrefix = "p",
  retryExtra,
}: {
  form: ProfileForm;
  idPrefix?: string;
  retryExtra?: React.ReactNode;
}) {
  const { v, patch } = form;
  const [modelsOpen, setModelsOpen] = React.useState(false);
  const base = BASE_URL_PRESETS[v.format] ?? { placeholder: "https://", presets: [] };

  return (
    <>
      <div className="grid gap-4 sm:grid-cols-2">
        <div className="grid gap-2">
          <Label htmlFor={`${idPrefix}-name`}>名称</Label>
          <Input
            id={`${idPrefix}-name`}
            placeholder="例如：OpenAI 生产"
            value={v.name}
            onChange={(e) => patch({ name: e.target.value })}
          />
        </div>
        <div className="grid gap-2">
          <Label>格式</Label>
          <Select value={v.format} onValueChange={(x) => patch({ format: x as LLMFormat })}>
            <SelectTrigger>
              <SelectValue placeholder="选择格式" />
            </SelectTrigger>
            <SelectContent>
              {LLM_FORMATS.map((f) => (
                <SelectItem key={f} value={f}>
                  {FORMAT_LABEL_LONG[f]}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>

      <div className="grid gap-2">
        <Label htmlFor={`${idPrefix}-model`}>模型</Label>
        <div className="flex gap-2">
          <Input
            id={`${idPrefix}-model`}
            className="font-mono"
            placeholder="claude-opus-4-8"
            value={v.model}
            onChange={(e) => patch({ model: e.target.value })}
          />
          {/* modal：这个 Popover 的内容被 portal 到 <body>，在 Sheet 的滚动锁之外，
              不加 modal 时列表能渲染却滚不动。modal 让它自己持有最上层滚动锁。 */}
          <Popover open={modelsOpen} onOpenChange={setModelsOpen} modal>
            <PopoverTrigger asChild>
              <Button
                type="button"
                variant="outline"
                size="icon"
                className="shrink-0"
                disabled={form.loadingModels}
                onClick={() => void form.loadModels().then((list) => setModelsOpen(list.length > 0))}
                title="从 API 加载可用模型"
              >
                {form.loadingModels ? <Loader2Icon className="animate-spin" /> : <RefreshCwIcon />}
              </Button>
            </PopoverTrigger>
            {form.models.length > 0 && (
              <PopoverContent className="max-h-72 w-72 gap-0 overflow-y-auto overscroll-contain p-1" align="end">
                <div className="text-muted-foreground sticky top-0 bg-popover px-2 py-1 text-xs">
                  共 {form.models.length} 个模型 · 点击选用
                </div>
                {form.models.map((m) => (
                  <button
                    key={m}
                    type="button"
                    className={`w-full shrink-0 rounded-md px-2 py-1.5 text-left font-mono text-xs hover:bg-accent hover:text-accent-foreground ${
                      m === v.model ? "bg-accent text-accent-foreground" : ""
                    }`}
                    onClick={() => {
                      patch({ model: m });
                      setModelsOpen(false);
                    }}
                  >
                    {m}
                  </button>
                ))}
              </PopoverContent>
            )}
          </Popover>
        </div>
        <p className="text-muted-foreground text-xs">
          右侧按钮用当前的地址 / Key 拉一次可用模型列表；已存配置会自动回显上次拉取的结果。
        </p>
      </div>

      <div className="grid gap-2">
        <Label htmlFor={`${idPrefix}-base-url`}>Base URL（可选，留空 = 该格式官方地址）</Label>
        <Input
          id={`${idPrefix}-base-url`}
          className="font-mono"
          placeholder={base.placeholder}
          value={v.baseUrl}
          onChange={(e) => patch({ baseUrl: e.target.value })}
        />
        {/* 常用端点预设：一键填充；粘贴完整补全路径（…/chat/completions）保存时会自动剥掉 */}
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="text-muted-foreground text-xs">常用:</span>
          {base.presets.map((preset) => (
            <button
              key={preset.url}
              type="button"
              onClick={() => patch({ baseUrl: preset.url })}
              className="rounded-full border px-2.5 py-0.5 text-xs transition-colors hover:bg-muted"
              title={preset.url}
            >
              {preset.label}
            </button>
          ))}
        </div>
      </div>

      <div className="grid gap-2">
        <Label htmlFor={`${idPrefix}-proxy`}>代理（可选）</Label>
        <Input
          id={`${idPrefix}-proxy`}
          className="font-mono"
          placeholder="socks5://user:pass@127.0.0.1:1080 · http://127.0.0.1:8080"
          value={v.proxy}
          onChange={(e) => patch({ proxy: e.target.value })}
        />
        <p className="text-muted-foreground text-xs">
          仅 LLM 出站请求走此代理，支持 http/https/socks5，可带账号密码（如
          socks5://user:pass@host:port，密码含特殊字符需 URL 编码）；留空表示不使用代理（直连）。
        </p>
      </div>

      <div className="grid gap-2">
        <Label htmlFor={`${idPrefix}-session-header`}>自定义会话头（可选）</Label>
        <Input
          id={`${idPrefix}-session-header`}
          className="font-mono"
          placeholder="如 x-session-id（留空=不发送）"
          value={v.sessionHeaderKey}
          onChange={(e) => patch({ sessionHeaderKey: e.target.value })}
        />
        <p className="text-muted-foreground text-xs">
          填写头名后，每次请求都会带上这个 HTTP 头，头值自动填为 <b>当前会话的 session id</b>（chat 会话如
          conv-12、worker 如 exp3-worker-i87）。用于按 session-id 头做提示缓存 / 粘性路由的网关；同一会话多轮稳定、
          不同会话互不相同。留空则不发送。
        </p>
      </div>

      <div className="grid gap-2">
        <Label htmlFor={`${idPrefix}-api-key`}>API Key</Label>
        <Input
          id={`${idPrefix}-api-key`}
          type="password"
          placeholder={form.keyHint ? `已设置（${form.keyHint}），留空保持不变` : "sk-…"}
          value={v.apiKey}
          onChange={(e) => patch({ apiKey: e.target.value })}
        />
      </div>

      <div className="grid gap-4 sm:grid-cols-3">
        <div className="grid gap-2">
          <Label htmlFor={`${idPrefix}-rps`}>每秒限速</Label>
          <Input
            id={`${idPrefix}-rps`}
            type="number"
            min={0}
            value={v.rps}
            onChange={(e) => patch({ rps: e.target.value })}
          />
        </div>
        <div className="grid gap-2">
          <Label htmlFor={`${idPrefix}-rpm`}>每分钟限速</Label>
          <Input
            id={`${idPrefix}-rpm`}
            type="number"
            min={0}
            value={v.rpm}
            onChange={(e) => patch({ rpm: e.target.value })}
          />
        </div>
        <div className="grid gap-2">
          <Label htmlFor={`${idPrefix}-cw`}>上下文窗口(K)</Label>
          <Input
            id={`${idPrefix}-cw`}
            type="number"
            min={0}
            max={1000}
            value={v.cw}
            onChange={(e) => patch({ cw: e.target.value })}
            placeholder="200"
          />
        </div>
      </div>
      <p className="-mt-2 text-muted-foreground text-xs">
        限速 0 = 不限，全 Agent 共享。上下文窗口单位 K（千 token），0 = 默认 200K，上限 1000（即
        1M）；设太高会导致压缩不触发。
      </p>

      <div className="grid gap-3 rounded-lg border p-3">
        <div className="flex items-center justify-between gap-4">
          <div className="grid gap-0.5">
            <Label htmlFor={`${idPrefix}-priority`} className="text-sm">
              轮询优先级
            </Label>
            <p className="text-muted-foreground text-xs">
              数字越大越先被选中；激活配置恒为第 1 顺位，与本值无关。相同优先级的配置会轮流打头，天然分摊额度。
            </p>
          </div>
          <Input
            id={`${idPrefix}-priority`}
            type="number"
            className="w-24 shrink-0"
            value={v.priority}
            onChange={(e) => patch({ priority: e.target.value })}
          />
        </div>
        <div className="flex items-center justify-between gap-4 border-t pt-3">
          <div className="grid gap-0.5">
            <Label className="text-sm">不参与轮询</Label>
            <p className="text-muted-foreground text-xs">
              开启后不会被当作故障转移目标（仍可被 Agent / 任务显式指定使用）。 适合「只给某个 Agent
              专用、不希望别人失败时烧掉」的昂贵配置。
            </p>
          </div>
          <Switch checked={v.poolExclude} onCheckedChange={(x) => patch({ poolExclude: x })} aria-label="不参与轮询" />
        </div>
        <div className="flex items-center justify-between gap-4 border-t pt-3">
          <div className="grid gap-0.5">
            <Label className="text-sm">流式输出 · streaming</Label>
            <p className="text-muted-foreground text-xs">
              开启（默认）走流式 SSE，有运行中实时进度与实时 token 计数。 关闭则走真·非流式（stream:false，
              一次性返回完整响应）——可绕开部分网关糟糕的 SSE 实现（空帧 / 思考字段丢帧）， 代价是失去运行中的实时进度。
            </p>
          </div>
          <Switch checked={v.streaming} onCheckedChange={(x) => patch({ streaming: x })} aria-label="流式输出" />
        </div>
      </div>

      <div className="grid gap-3 rounded-lg border p-3">
        <div className="flex items-center justify-between gap-4">
          <div className="grid gap-0.5">
            <Label htmlFor={`${idPrefix}-max-tokens`} className="text-sm">
              输出上限 · max tokens
            </Label>
            <p className="text-muted-foreground text-xs">
              单次回复最多生成多少 token，随每次请求发出。0（默认）= 不发送该字段，由服务端默认值决定。
              这与上面的「上下文窗口」是两回事：那是模型总容量，只在本地用来算压缩阈值。
              设太小会让推理模型在思考阶段就被截断，一个字答案都出不来。
            </p>
          </div>
          <Input
            id={`${idPrefix}-max-tokens`}
            type="number"
            min={0}
            className="w-28 shrink-0"
            value={v.maxTokens}
            onChange={(e) => patch({ maxTokens: e.target.value })}
            placeholder="0"
          />
        </div>
        <div className="flex items-center justify-between gap-4 border-t pt-3">
          <div className="grid gap-0.5">
            <Label className="text-sm">上限字段名</Label>
            <p className="text-muted-foreground text-xs">{MAX_TOKENS_FIELD_HINTS[v.format]}</p>
          </div>
          <Select
            value={v.format === "openai" ? v.maxTokensField : NONE}
            onValueChange={(x) => patch({ maxTokensField: x })}
            disabled={v.format !== "openai"}
          >
            <SelectTrigger className="w-56 shrink-0">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {MAX_TOKENS_FIELDS.map((o) => (
                <SelectItem key={o.value} value={o.value}>
                  {o.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>

      <div className="grid gap-3 rounded-lg border p-3">
        <div className="flex items-center justify-between gap-4">
          <div className="grid gap-0.5">
            <Label className="text-sm">思考开关 · thinking.type</Label>
            <p className="text-muted-foreground text-xs">
              控制是否发送 thinking 字段。不发送=不带该字段（兼容 MiniMax 等不支持 的模型）；关闭=发 disabled；开启=发
              enabled。与下面的强度互相独立。
            </p>
          </div>
          <Select value={v.thinkingType} onValueChange={(x) => patch({ thinkingType: x })}>
            <SelectTrigger className="w-32 shrink-0">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {THINKING_TYPES.map((o) => (
                <SelectItem key={o.value} value={o.value}>
                  {o.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="flex items-center justify-between gap-4 border-t pt-3">
          <div className="grid gap-0.5">
            <Label className="text-sm">思考强度 · reasoning_effort</Label>
            <p className="text-muted-foreground text-xs">
              独立的强度档位（OpenAI reasoning_effort / Anthropic output_config.effort）。 有些接口没有 thinking
              字段、只靠强度即可激活思考，故可单独设置、不发送思考开关。
            </p>
          </div>
          <Select value={v.effort} onValueChange={(x) => patch({ effort: x })}>
            <SelectTrigger className="w-32 shrink-0">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {EFFORT_LEVELS.map((o) => (
                <SelectItem key={o.value} value={o.value}>
                  {o.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>

      <ProfileRetryFields value={v.retry} onChange={(r) => patch({ retry: r })} />
      {retryExtra}
    </>
  );
}
