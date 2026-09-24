"use client";

// 决策引擎的表单底座:列表页的快速编辑对话框与详情页共用同一套字段与语义。
// 抽出来的理由不是"复用组件"本身,而是 api_key 的三态语义必须在两处完全一致:
// 留空=不改动、填值=覆盖、显式清除=写空串。任何一处写错都会静默清掉用户已配的凭据。

import { KeyRoundIcon, Loader2Icon, PlugZapIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import type { DecisionEngine } from "@/lib/types";

export interface EngineDraft {
  baseURL: string;
  model: string;
  apiKey: string;
}

export type EnginePatch = { enabled?: boolean; base_url?: string; api_key?: string; model?: string };

export function draftFromEngine(engine: DecisionEngine): EngineDraft {
  // api_key 只写不读(后端只回 api_key_set 布尔),所以草稿里的 Key 永远从空串起步。
  return { baseURL: engine.base_url, model: engine.model, apiKey: "" };
}

// buildPatch 只把真正变化的字段放进 patch:后端对 base_url/model 是"给了就写",
// 无脑全量提交会把用户没碰过的字段一起写一遍,徒增无意义的写入与审计噪音。
export function buildPatch(engine: DecisionEngine, draft: EngineDraft): EnginePatch {
  const patch: EnginePatch = {};
  if (draft.baseURL !== engine.base_url) patch.base_url = draft.baseURL;
  if (draft.model !== engine.model) patch.model = draft.model;
  if (draft.apiKey.trim()) patch.api_key = draft.apiKey.trim();
  return patch;
}

export function patchSize(patch: EnginePatch): number {
  return Object.keys(patch).length;
}

export interface TestState {
  status: "idle" | "testing" | "ok" | "fail";
  latencyMs?: number;
  detail?: string;
  error?: string;
  at?: number;
}

export function fmtClock(at?: number): string {
  if (!at) return "—";
  return new Date(at).toLocaleTimeString();
}

// runEngineTest 是"先保存再测试":端点/模型/新凭据若未落盘,探测的就是旧配置,
// 用户会误以为刚填的地址不通。所以这里自己先提交一次,再打探测请求。
// 返回结构化结果,由调用方决定是 toast 还是面板展示。
export async function runEngineTest(engine: DecisionEngine, draft: EngineDraft): Promise<TestState> {
  const at = Date.now();
  const patch = buildPatch(engine, draft);
  if (patchSize(patch) > 0) await api.updateDecisionEngine(engine.key, patch);
  const r = await api.testDecisionEngine(engine.key);
  return {
    status: r.ok ? "ok" : "fail",
    latencyMs: r.latency_ms,
    detail: r.detail,
    error: r.error,
    at,
  };
}

// EngineFields 是三个可编辑字段本身(API 地址 / 模型 / API Key)。
// 启用开关与保存按钮不在这里——它们在列表页对话框和详情页头部的形态不同。
export function EngineFields({
  engine,
  draft,
  onDraftChange,
  disabled,
  onClearKey,
  clearing,
}: {
  engine: DecisionEngine;
  draft: EngineDraft;
  onDraftChange: (patch: Partial<EngineDraft>) => void;
  disabled?: boolean;
  onClearKey?: () => void;
  clearing?: boolean;
}) {
  // 用显式布尔量而不是 `disabled || clearing`:后者的短路语义在 disabled=false
  // 时会直接吞掉 clearing,清除进行中按钮反而可点。
  const busy = Boolean(disabled) || Boolean(clearing);

  return (
    <div className="grid gap-4 md:grid-cols-2">
      <div className="grid gap-1.5">
        <Label htmlFor={`${engine.key}-base`} className="text-xs">
          API 地址
        </Label>
        <Input
          id={`${engine.key}-base`}
          value={draft.baseURL}
          onChange={(e) => onDraftChange({ baseURL: e.target.value })}
          placeholder={engine.default_base_url || "https://…"}
          className="font-mono text-xs"
          disabled={disabled}
        />
        {engine.endpoint_path ? (
          <p className="text-muted-foreground text-[11px]">填根地址即可,系统自动拼接 {engine.endpoint_path}</p>
        ) : (
          <p className="text-muted-foreground text-[11px]">协议预留:接入前仅对该根地址做可达性检查,不发送判定请求。</p>
        )}
      </div>

      <div className="grid gap-1.5">
        <Label htmlFor={`${engine.key}-model`} className="text-xs">
          模型
        </Label>
        <Input
          id={`${engine.key}-model`}
          value={draft.model}
          onChange={(e) => onDraftChange({ model: e.target.value })}
          placeholder={engine.default_model || "decision-model"}
          className="font-mono text-xs"
          disabled={disabled}
        />
        {engine.default_model ? (
          <p className="text-muted-foreground text-[11px]">留空则用默认模型 {engine.default_model}</p>
        ) : null}
      </div>

      <div className="grid gap-1.5 md:col-span-2">
        <div className="flex items-center gap-2">
          <Label htmlFor={`${engine.key}-key`} className="text-xs">
            API Key
          </Label>
          {/* 明文永不回显:只回"是否已配置",避免凭据出现在 DOM / 截图 / 日志里。 */}
          <Badge variant="outline" className="px-1.5 py-0 text-[10px]">
            {engine.api_key_set ? "已配置" : "未配置"}
          </Badge>
        </div>
        <div className="flex items-center gap-2">
          <KeyRoundIcon className="text-muted-foreground size-4 shrink-0" />
          <Input
            id={`${engine.key}-key`}
            type="password"
            value={draft.apiKey}
            onChange={(e) => onDraftChange({ apiKey: e.target.value })}
            placeholder={engine.api_key_set ? "已配置(输入新值覆盖;留空保持不变)" : "未配置(粘贴 Key 后保存)"}
            className="font-mono text-xs"
            autoComplete="new-password"
            disabled={disabled}
          />
          {engine.api_key_set && onClearKey ? (
            <Button
              variant="ghost"
              size="sm"
              className="text-muted-foreground shrink-0"
              disabled={busy}
              onClick={onClearKey}
              title="清除已保存的 Key(立即生效)"
            >
              清除
            </Button>
          ) : null}
        </div>
        <p className="text-muted-foreground text-[11px]">留空表示不修改当前凭据;Key 只写不读,保存后无法再查看明文。</p>
      </div>
    </div>
  );
}

function TestStatusBadge({ status }: { status: TestState["status"] }) {
  if (status === "testing") {
    return (
      <Badge variant="outline" className="bg-muted text-muted-foreground">
        测试中
      </Badge>
    );
  }
  if (status === "ok") {
    return (
      <Badge variant="outline" className="border-emerald-500/30 bg-emerald-500/15 text-emerald-600">
        成功
      </Badge>
    );
  }
  if (status === "fail") {
    return (
      <Badge variant="outline" className="border-red-500/30 bg-red-500/15 text-red-600">
        失败
      </Badge>
    );
  }
  return (
    <Badge variant="outline" className="bg-muted text-muted-foreground">
      未测试
    </Badge>
  );
}

// TestBody 按状态渲染面板正文。拆成独立函数而不是嵌套三元:
// 四个分支各有多行内容,嵌套三元会一路缩进到看不见,可读性比 if 链差得多。
function TestBody({ state }: { state: TestState }) {
  if (state.status === "idle") {
    return (
      <p className="text-muted-foreground">
        尚未测试。测试会先保存未提交的修改,再用一条最小探测请求验证端点与凭据;已接入引擎走真实协议,预留引擎只验根地址可达。
      </p>
    );
  }
  if (state.status === "testing") {
    return (
      <p className="text-muted-foreground flex items-center gap-2">
        <Loader2Icon className="size-3.5 animate-spin" />
        正在探测端点与凭据…
      </p>
    );
  }
  if (state.status === "ok") {
    return (
      <div className="space-y-1">
        <p className="text-emerald-600">
          连通正常{typeof state.latencyMs === "number" ? `(${state.latencyMs}ms)` : ""}
        </p>
        {state.detail ? <p className="text-muted-foreground break-all">{state.detail}</p> : null}
      </div>
    );
  }
  return (
    <div className="space-y-1">
      <p className="text-destructive">连接失败</p>
      {/* 错误原文照实展示:排查时要的就是原始报文/状态码,不要二次包装。 */}
      <p className="text-muted-foreground font-mono break-all">{state.error ?? "未知错误"}</p>
    </div>
  );
}

// TestPanel 是详情页/对话框里的连通性测试面板:测试中 / 成功 / 失败 + 错误原文 + 最近一次时间。
// 后端不返回历史测试记录,所以"最近一次"只代表本次会话内的最后一次,文案里如实标明。
export function TestPanel({
  state,
  onTest,
  disabled,
  disabledHint,
}: {
  state: TestState;
  onTest: () => void;
  disabled?: boolean;
  disabledHint?: string;
}) {
  const testing = state.status === "testing";
  const blocked = Boolean(disabled) || testing;

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium text-sm">连通性测试</span>
        <TestStatusBadge status={state.status} />
        {state.at ? (
          <span className="text-muted-foreground ml-auto text-[11px] tabular-nums">
            最近一次 {fmtClock(state.at)}(本次会话)
          </span>
        ) : null}
      </div>

      <div className="bg-muted/30 rounded-lg border p-3 text-xs">
        <TestBody state={state} />
      </div>

      <Button variant="outline" size="sm" disabled={blocked} onClick={onTest} title={disabledHint}>
        {testing ? <Loader2Icon className="animate-spin" /> : <PlugZapIcon />} 测试连通性
      </Button>
      {/* 提示在按钮可用时也要显示:"测试会先保存"这件事用户点之前就该知道。 */}
      {disabledHint ? <p className="text-muted-foreground text-[11px]">{disabledHint}</p> : null}
    </div>
  );
}
