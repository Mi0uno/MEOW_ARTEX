"use client";

// AI监管:操作审查的 AI 流水线编排页。
// 流水线:拦截规则未命中 → 决策引擎(前置快速判定,配置在 AI管理·Decision) →
// 监管模型(LLM 语义审查,从 AI管理·LLM 的配置中选模型) → 人工审批。

import * as React from "react";

import Link from "next/link";

import { BotIcon, PlugZapIcon, WorkflowIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { DecisionEngine, JudgeConfig, LLMProfile } from "@/lib/types";

// ---- 决策引擎(前置) ----

function DecisionEnginesCard() {
  const [engines, setEngines] = React.useState<DecisionEngine[]>([]);
  const [testing, setTesting] = React.useState<string>("");
  const [testResult, setTestResult] = React.useState<Record<string, string>>({});

  const load = React.useCallback(async () => {
    try {
      setEngines(await api.decisionEngines());
    } catch (e) {
      toast.error(`加载决策引擎失败: ${(e as Error).message}`);
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  const toggle = async (engine: DecisionEngine, enabled: boolean) => {
    try {
      await api.updateDecisionEngine(engine.key, { enabled });
      await load();
    } catch (e) {
      toast.error(`更新失败: ${(e as Error).message}`);
    }
  };

  const test = async (engine: DecisionEngine) => {
    setTesting(engine.key);
    setTestResult((r) => ({ ...r, [engine.key]: "" }));
    try {
      const r = await api.testDecisionEngine(engine.key);
      setTestResult((prev) => ({
        ...prev,
        [engine.key]: r.ok
          ? `连通正常(${r.latency_ms ?? 0}ms)${r.detail ? ` · ${r.detail}` : ""}`
          : `失败:${r.error ?? "未知错误"}`,
      }));
    } catch (e) {
      setTestResult((prev) => ({ ...prev, [engine.key]: `失败:${(e as Error).message}` }));
    } finally {
      setTesting("");
    }
  };

  return (
    <Card>
      <CardContent className="space-y-4 p-4">
        <div className="flex items-center gap-2">
          <PlugZapIcon className="h-4 w-4 text-muted-foreground" />
          <p className="font-medium text-sm">决策引擎（前置快速判定）</p>
          <span className="text-muted-foreground text-xs">
            拦截规则未命中时先做结构化快评,高置信直接判定,低置信转监管模型/人工
          </span>
        </div>
        {engines.length === 0 ? (
          <p className="py-6 text-center text-muted-foreground text-sm">加载中…</p>
        ) : (
          engines.map((engine) => (
            <div key={engine.key} className="rounded-md border p-3">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="flex min-w-0 items-center gap-2">
                  <span className="font-medium text-sm">{engine.title}</span>
                  {engine.enabled ? (
                    <Badge variant="outline" className="border-violet-500/30 px-1.5 py-0 text-[10px] text-violet-600">
                      已启用
                    </Badge>
                  ) : (
                    <Badge variant="outline" className="px-1.5 py-0 text-[10px] text-muted-foreground">
                      未启用
                    </Badge>
                  )}
                  {!engine.available && (
                    <Badge variant="outline" className="px-1.5 py-0 text-[10px] text-amber-600">
                      协议预留
                    </Badge>
                  )}
                  {engine.api_key_set ? (
                    <Badge variant="secondary" className="px-1.5 py-0 text-[10px]">
                      凭据已配置
                    </Badge>
                  ) : (
                    <Badge variant="outline" className="border-destructive/40 px-1.5 py-0 text-[10px] text-destructive">
                      缺凭据
                    </Badge>
                  )}
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  <Button variant="ghost" size="sm" asChild>
                    <Link href="/system/decision">配置凭据</Link>
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={testing === engine.key || !engine.api_key_set}
                    onClick={() => void test(engine)}
                  >
                    测试
                  </Button>
                  <Switch checked={engine.enabled} onCheckedChange={(v) => void toggle(engine, v)} />
                </div>
              </div>
              <p className="mt-1.5 text-muted-foreground text-xs">{engine.description}</p>
              <p className="mt-1 font-mono text-[11px] text-muted-foreground">
                {engine.model} @ {engine.base_url}
              </p>
              {testResult[engine.key] ? (
                <p
                  className={`mt-1 text-xs ${testResult[engine.key].startsWith("失败") ? "text-destructive" : "text-emerald-600"}`}
                >
                  {testResult[engine.key]}
                </p>
              ) : null}
            </div>
          ))
        )}
      </CardContent>
    </Card>
  );
}

// ---- 监管模型(LLM 兜底审查) ----

const FOLLOW_ACTIVE = "0"; // profile_id 0 = 跟随激活/默认配置

const defaultJudge = (): JudgeConfig => ({
  enabled: false,
  profile_id: 0,
  prompt: "",
  timeout_seconds: 15,
  fail_action: "allow",
  ask_timeout_seconds: 300,
  ask_timeout_action: "deny",
});

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1.5">
      <Label className="font-medium text-muted-foreground text-xs uppercase tracking-wide">{label}</Label>
      {children}
    </div>
  );
}

function JudgeCard() {
  const [cfg, setCfg] = React.useState<JudgeConfig>(defaultJudge());
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [saving, setSaving] = React.useState(false);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const [j, ps] = await Promise.all([api.interceptGetJudgeConfig(), api.llmProfiles()]);
      setCfg(j);
      setProfiles(ps);
    } catch (e) {
      toast.error(`加载监管模型配置失败: ${(e as Error).message}`);
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  function patch(p: Partial<JudgeConfig>) {
    setCfg((c) => ({ ...c, ...p }));
  }

  async function save() {
    setSaving(true);
    try {
      await api.interceptSetJudgeConfig(cfg);
      toast.success("监管模型配置已保存");
      await load(); // 回读:提示词若清空则回填内置模板
    } catch (e) {
      toast.error(`保存失败: ${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  async function restorePrompt() {
    // 清空提示词并保存 → 服务端下次返回内置模板全文,回填到输入框。
    setSaving(true);
    try {
      await api.interceptSetJudgeConfig({ ...cfg, prompt: "" });
      const j = await api.interceptGetJudgeConfig();
      setCfg(j);
      toast.success("已恢复内置默认模板");
    } catch (e) {
      toast.error(`恢复失败: ${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="space-y-4">
      {/* 启用开关 —— 独立高亮条 */}
      <div
        className={`flex items-center justify-between gap-3 rounded-lg border px-4 py-3 ${
          cfg.enabled ? "border-violet-400/50 bg-violet-50/40 dark:bg-violet-950/20" : "bg-muted/40"
        }`}
      >
        <div className="flex items-center gap-2.5">
          <BotIcon className={`h-5 w-5 shrink-0 ${cfg.enabled ? "text-violet-600" : "text-muted-foreground"}`} />
          <div>
            <p className="font-semibold text-sm leading-tight">监管模型（LLM 语义审查）</p>
            <p className="mt-0.5 text-muted-foreground text-xs">
              在<span className="font-medium text-foreground">拦截范围</span>内、且
              <span className="font-medium text-foreground">没有任何拦截规则命中</span>
              、决策引擎也未给出高置信判定时,由监管模型做语义审查（放行 / 转人工 / 拦截）
            </p>
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <span className="text-muted-foreground text-xs">{cfg.enabled ? "已启用" : "未启用"}</span>
          <Switch checked={cfg.enabled} disabled={loading} onCheckedChange={(v) => patch({ enabled: v })} />
        </div>
      </div>

      {cfg.enabled && (
        <div className="grid gap-4 lg:grid-cols-5">
          {/* 左:提示词编辑器(直接展开,主区域) */}
          <Card className="lg:col-span-3">
            <CardContent className="flex h-full flex-col gap-2 p-4">
              <div className="flex items-center justify-between">
                <div>
                  <p className="font-medium text-sm">审查提示词</p>
                  <p className="text-muted-foreground text-xs">模型据此判定 ALLOW / ASK / DENY，可直接编辑</p>
                </div>
                <Button variant="ghost" size="sm" className="h-7 text-xs" onClick={restorePrompt} disabled={saving}>
                  恢复默认模板
                </Button>
              </div>
              <Textarea
                className="min-h-[22rem] flex-1 resize-none font-mono text-xs leading-relaxed"
                value={cfg.prompt}
                onChange={(e) => patch({ prompt: e.target.value })}
                placeholder="留空使用内置模板"
                spellCheck={false}
              />
              <p className="text-right text-[11px] text-muted-foreground">{cfg.prompt.length} 字</p>
            </CardContent>
          </Card>

          {/* 右:判定参数(设置栏) */}
          <Card className="lg:col-span-2">
            <CardContent className="space-y-5 p-4">
              <div className="space-y-4">
                <p className="font-semibold text-[10px] text-muted-foreground uppercase tracking-wider">
                  监管模型与策略
                </p>
                <Field label="监管模型">
                  <Select value={String(cfg.profile_id || 0)} onValueChange={(v) => patch({ profile_id: Number(v) })}>
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={FOLLOW_ACTIVE}>跟随激活配置</SelectItem>
                      {profiles.map((p) => (
                        <SelectItem key={p.id} value={p.id}>
                          {p.name}（{p.model}）
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
                <p className="text-muted-foreground text-xs">
                  模型来自{" "}
                  <Link
                    href="/system/llm"
                    className="underline decoration-dotted underline-offset-2 hover:text-foreground"
                  >
                    AI管理 · LLM
                  </Link>{" "}
                  中配置的模型。
                </p>
                <Field label="模型判定超时（秒）">
                  <Input
                    type="number"
                    min={1}
                    value={cfg.timeout_seconds}
                    onChange={(e) => {
                      const n = parseInt(e.target.value, 10);
                      if (n > 0) patch({ timeout_seconds: n });
                    }}
                  />
                </Field>
                <Field label="模型失败时（出错 / 超时 / 无法解析）">
                  <Select
                    value={cfg.fail_action}
                    onValueChange={(v) => patch({ fail_action: v as JudgeConfig["fail_action"] })}
                  >
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="allow">放行</SelectItem>
                      <SelectItem value="ask">转人工审批</SelectItem>
                      <SelectItem value="deny">拦截</SelectItem>
                    </SelectContent>
                  </Select>
                </Field>
              </div>

              <Separator />

              <div className="space-y-4">
                <p className="font-semibold text-[10px] text-muted-foreground uppercase tracking-wider">
                  人工审批（模型判为「转人工」时）
                </p>
                <Field label="审批等待超时（秒）">
                  <Input
                    type="number"
                    min={5}
                    value={cfg.ask_timeout_seconds}
                    onChange={(e) => {
                      const n = parseInt(e.target.value, 10);
                      if (n > 0) patch({ ask_timeout_seconds: n });
                    }}
                  />
                </Field>
                <Field label="超时后默认动作">
                  <Select
                    value={cfg.ask_timeout_action}
                    onValueChange={(v) => patch({ ask_timeout_action: v as JudgeConfig["ask_timeout_action"] })}
                  >
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="deny">拦截</SelectItem>
                      <SelectItem value="allow">放行</SelectItem>
                    </SelectContent>
                  </Select>
                </Field>
              </div>
            </CardContent>
          </Card>
        </div>
      )}

      <div className="flex justify-end">
        <Button size="sm" onClick={save} disabled={saving || loading}>
          {saving ? "保存中…" : "保存配置"}
        </Button>
      </div>
    </div>
  );
}

// ---- page ----

export default function AiGuardPage() {
  return (
    <div className="flex flex-1 flex-col gap-4 p-4 md:p-6">
      <div>
        <h1 className="flex items-center gap-2 font-semibold text-xl tracking-tight">
          <WorkflowIcon className="h-5 w-5" />
          AI监管
        </h1>
        <p className="mt-0.5 text-muted-foreground text-sm">
          操作审查的 AI 流水线:拦截规则未命中 → 决策引擎快速判定 → 监管模型语义审查 → 人工审批
        </p>
      </div>
      <DecisionEnginesCard />
      <div className="flex items-center gap-2 text-muted-foreground text-xs">
        <span className="h-px flex-1 bg-border" />
        以下为规则与决策引擎均未给出结论时的兜底审查
        <span className="h-px flex-1 bg-border" />
      </div>
      <JudgeCard />
    </div>
  );
}
