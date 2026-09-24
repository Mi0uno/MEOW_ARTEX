"use client";

// AI监管:操作审查的 AI 流水线编排页。
//
// 页面顺序按"能否一眼回答监管到底在不在工作"排:
//   门限总览(生效状态 + 五档动作分布) → 决策引擎(前置快速判定) →
//   监管模型(LLM 语义审查) → 任务级门限说明。
//
// 流水线:拦截规则未命中 → 决策引擎(前置快速判定,配置在 AI管理·Decision) →
// 监管模型(LLM 语义审查,从 AI管理·LLM 的配置中选模型) → 人工审批。
// 两层模型各有独立开关:监管模型关闭不会让决策引擎失效(两者是平行的一层)。

import * as React from "react";

import Link from "next/link";

import {
  BanIcon,
  BotIcon,
  CheckCircle2Icon,
  InfoIcon,
  PlugZapIcon,
  ScanSearchIcon,
  ScrollTextIcon,
  SlidersHorizontalIcon,
  UserCheckIcon,
  WorkflowIcon,
} from "lucide-react";
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
import type { DecisionEngine, JevThresholds, JevThresholdsResponse, JudgeConfig, LLMProfile } from "@/lib/types";

import { GuardOverview } from "./_components/guard-overview";
import { TaskGuardNotes } from "./_components/task-guard-notes";

// ---- 决策引擎(前置) ----

// JEV 的判据(questions)是硬编码在 Go 侧(server/jev.go)的协议内容,后端没有下发,
// 这里只做说明——**不做假的编辑器**。数值阈值同样来自 server/jev.go 的常量
// (jevConfidenceMin/jevAllowConf/jevNoulDanger),若后端调整需同步这份文案。
const JEV_CRITERIA = [
  { name: "action", kind: "choice", detail: "block / review / allow 三分类:危险直接拦、需人工复核、安全放行" },
  { name: "is_destructive", kind: "noul", detail: "是否做不可逆破坏(删数据、格式化、重置凭据、卸载软件)" },
  { name: "is_exfiltration", kind: "noul", detail: "是否把敏感数据送出授权测试范围" },
  { name: "is_privilege", kind: "noul", detail: "是否尝试对目标提权或绕过认证" },
];

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
        <div className="flex flex-wrap items-center gap-2">
          <PlugZapIcon className="h-4 w-4 text-muted-foreground" />
          <p className="font-medium text-sm">决策引擎（前置快速判定）</p>
          <span className="text-muted-foreground text-xs">
            拦截规则未命中时先做结构化快评,高置信直接判定,低置信转监管模型/人工
          </span>
        </div>

        <p className="rounded-md bg-muted/40 px-3 py-2 text-muted-foreground text-xs leading-relaxed">
          决策引擎与监管模型是<span className="font-medium text-foreground">两个独立开关的平行层</span>：
          本卡片里的引擎只要自己启用且凭据可用，即使监管模型关闭也会照常参与判定；引擎的裁决同样会落入五档动作
          并计入上方门限总览（JEV 目前只输出「拦截」与「放通」两档，未裁决的情况交给监管模型）。
        </p>

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
                {engine.endpoint_path ? ` · ${engine.endpoint_path}` : ""}
              </p>
              {testResult[engine.key] ? (
                <p
                  className={`mt-1 text-xs ${testResult[engine.key].startsWith("失败") ? "text-destructive" : "text-emerald-600"}`}
                >
                  {testResult[engine.key]}
                </p>
              ) : null}

              {/* 判据说明:如实标注"不可自定义",不提供假编辑器。 */}
              <div className="mt-2 rounded-md border border-dashed px-2.5 py-2">
                <div className="flex flex-wrap items-center gap-1.5">
                  <ScrollTextIcon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                  <span className="font-medium text-[11px]">判据（questions）</span>
                  <Badge variant="outline" className="px-1.5 py-0 text-[10px] text-muted-foreground">
                    由引擎协议定义 · 当前不可自定义
                  </Badge>
                </div>
                {engine.key === "jev" ? (
                  <>
                    <ul className="mt-1.5 space-y-0.5">
                      {JEV_CRITERIA.map((q) => (
                        <li key={q.name} className="text-[11px] text-muted-foreground leading-snug">
                          <span className="font-mono text-foreground">{q.name}</span>
                          <span className="ml-1 text-[10px] opacity-70">[{q.kind}]</span>
                          <span className="ml-1.5">{q.detail}</span>
                        </li>
                      ))}
                    </ul>
                    <p className="mt-1.5 text-[10px] text-muted-foreground leading-relaxed">
                      引擎只给「终局裁决」：<span className="font-medium">高置信</span>且明确的
                      block / allow 才直接拦截或放行； 置信度不足、review、以及未达阈值的危险信号一律
                      <span className="font-medium">不作裁决</span>
                      ，交给下一层（监管模型 / 人工），因此判不准时不会拦死。 阈值可在下方调整。
                    </p>
                  </>
                ) : (
                  <p className="mt-1.5 text-[10px] text-muted-foreground leading-relaxed">
                    该引擎为协议预留位：客户端协议接入后，其判据同样由引擎协议固定，本页不做自定义。
                  </p>
                )}
              </div>

              {/* 门限阈值:JEV 的可调参数。默认取向是"宁可交给下一层,不要误拦"——
                  误拦会直接中断整个渗透任务,而放过去的调用后面还有两层兜底。 */}
              {engine.key === "jev" ? <JevThresholdPanel /> : null}
            </div>
          ))
        )}
      </CardContent>
    </Card>
  );
}

// ---- JEV 门限阈值 ----

function JevThresholdPanel() {
  const [th, setTh] = React.useState<JevThresholdsResponse | null>(null);
  const [saving, setSaving] = React.useState(false);
  const [open, setOpen] = React.useState(false);

  const load = React.useCallback(async () => {
    try {
      setTh(await api.jevThresholds());
    } catch (e) {
      toast.error(`加载门限阈值失败: ${(e as Error).message}`);
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  async function save(next: JevThresholds) {
    setSaving(true);
    try {
      const r = await api.saveJevThresholds(next);
      setTh(r);
      toast.success("门限阈值已保存");
    } catch (e) {
      toast.error(`保存失败: ${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  if (!th) {
    return <p className="mt-2 text-[11px] text-muted-foreground">加载门限阈值…</p>;
  }

  const pct = (v: number) => `${Math.round(v * 100)}%`;

  return (
    <div className="mt-2 rounded-md border px-2.5 py-2">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full flex-wrap items-center gap-1.5 text-left"
      >
        <SlidersHorizontalIcon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        <span className="font-medium text-[11px]">门限阈值</span>
        <Badge variant="outline" className="px-1.5 py-0 text-[10px] text-muted-foreground">
          拦截 ≥ {pct(th.block_confidence)} · 放行 ≥ {pct(th.allow_confidence)} · 危险 ≥ {pct(th.danger_score)}
        </Badge>
        {th.privilege_blocks ? null : (
          <Badge variant="outline" className="border-amber-500/40 px-1.5 py-0 text-[10px] text-amber-600">
            提权不硬拦
          </Badge>
        )}
        <span className="ml-auto text-[10px] text-muted-foreground">{open ? "收起" : "调整"}</span>
      </button>

      {open ? (
        <div className="mt-2 space-y-2.5">
          <p className="text-[10px] text-muted-foreground leading-relaxed">
            阈值调高 = 引擎更少下结论（判不准就交给监管模型/人工）；调低 = 引擎更敢直接拦。
            <span className="font-medium">误拦会中断整个渗透任务</span>，而放过去的调用后面还有两层兜底，
            所以默认偏向"少拦"。
          </p>

          <div className="grid gap-2.5 sm:grid-cols-3">
            <Field label={`拦截置信度门槛（当前 ${pct(th.block_confidence)}）`}>
              <Input
                type="number"
                min={1}
                max={100}
                value={Math.round(th.block_confidence * 100)}
                onChange={(e) => {
                  const n = parseInt(e.target.value, 10);
                  if (n > 0 && n <= 100) setTh({ ...th, block_confidence: n / 100 });
                }}
              />
            </Field>
            <Field label={`放行置信度门槛（当前 ${pct(th.allow_confidence)}）`}>
              <Input
                type="number"
                min={1}
                max={100}
                value={Math.round(th.allow_confidence * 100)}
                onChange={(e) => {
                  const n = parseInt(e.target.value, 10);
                  if (n > 0 && n <= 100) setTh({ ...th, allow_confidence: n / 100 });
                }}
              />
            </Field>
            <Field label={`危险信号阈值（当前 ${pct(th.danger_score)}）`}>
              <Input
                type="number"
                min={1}
                max={100}
                value={Math.round(th.danger_score * 100)}
                onChange={(e) => {
                  const n = parseInt(e.target.value, 10);
                  if (n > 0 && n <= 100) setTh({ ...th, danger_score: n / 100 });
                }}
              />
            </Field>
          </div>

          <div className="flex flex-wrap items-center justify-between gap-2 rounded-md bg-muted/40 px-2.5 py-1.5">
            <div className="min-w-0">
              <p className="text-[11px] font-medium">提权/绕认证信号单独硬拦</p>
              <p className="text-[10px] text-muted-foreground">
                默认关闭——授权渗透里"尝试提权、绕过认证"正是被授权的核心动作，用它硬拦等于把渗透任务本身判成攻击。
              </p>
            </div>
            <Switch
              checked={th.privilege_blocks}
              onCheckedChange={(v) => setTh({ ...th, privilege_blocks: v })}
            />
          </div>

          <Field label="置信度不足时">
            <Select
              value={th.low_confidence_action || "inherit"}
              onValueChange={(v) =>
                setTh({ ...th, low_confidence_action: (v === "inherit" ? "" : v) as JevThresholds["low_confidence_action"] })
              }
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="inherit">交给下一层（监管模型 / 人工，默认）</SelectItem>
                <SelectItem value="flag">判疑放通（标记但放行，留痕不阻塞）</SelectItem>
                <SelectItem value="ask">转人工审批</SelectItem>
                <SelectItem value="allow">直接放行</SelectItem>
              </SelectContent>
            </Select>
          </Field>

          <div className="flex items-center justify-end gap-2">
            <Button
              variant="ghost"
              size="sm"
              disabled={saving}
              onClick={() => void save(th.defaults)}
            >
              恢复默认
            </Button>
            <Button size="sm" disabled={saving} onClick={() => void save(th)}>
              {saving ? "保存中…" : "保存阈值"}
            </Button>
          </div>
        </div>
      ) : null}
    </div>
  );
}

// ---- 监管模型(LLM 兜底审查) ----

const FOLLOW_ACTIVE = "0"; // profile_id 0 = 跟随激活/默认配置

const defaultJudge = (): JudgeConfig => ({
  enabled: false,
  profile_id: 0,
  prompt: "",
  timeout_seconds: 15,
  // 与后端 defaultJudgeFailAction 保持一致(失效方向必须是"拒")。这里的占位值只在
  // 首次加载完成前显示;若加载失败,保存按钮会被 loadError 禁用,不会把占位值写回。
  fail_action: "deny",
  ask_timeout_seconds: 300,
  ask_timeout_action: "deny",
});

// 五档裁决的含义。提示词编辑器旁边直接列出,用户才知道可以要求模型输出哪几种动作。
const VERDICTS: Array<{ token: string; label: string; tone: string; detail: string }> = [
  {
    token: "deny",
    label: "AI拦截",
    tone: "text-red-600 dark:text-red-400",
    detail: "模型判定为真实破坏,直接阻止执行,不转人工。",
  },
  {
    token: "flag",
    label: "AI判疑",
    tone: "text-amber-600 dark:text-amber-400",
    detail: "可疑但置信度不足以拦截:工具照常执行,只落一条判疑审计供事后复盘,不阻塞、不转人工。",
  },
  {
    token: "allow",
    label: "AI放通",
    tone: "text-emerald-600 dark:text-emerald-400",
    detail: "模型判定安全,放行执行。",
  },
  {
    token: "ask",
    label: "AI转人工",
    tone: "text-blue-600 dark:text-blue-400",
    detail: "信息不足且判错代价高,转人工审批,等待超时后按下方策略处理。",
  },
];

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
  // 加载失败时禁止保存:此时表单里是占位默认值,写回会把用户的真实配置覆盖掉。
  const [loadError, setLoadError] = React.useState(false);
  // 载入时的提示词快照:用于如实标注"当前内容是否与载入时一致",不猜测来源。
  const [loadedPrompt, setLoadedPrompt] = React.useState("");
  // 本次会话内点击过"恢复默认模板" → 此时内容确实等于内置模板,可以如实标注。
  const [restoredDefault, setRestoredDefault] = React.useState(false);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const [j, ps] = await Promise.all([api.interceptGetJudgeConfig(), api.llmProfiles()]);
      setCfg(j);
      setLoadedPrompt(j.prompt);
      setProfiles(ps);
      setLoadError(false);
    } catch (e) {
      setLoadError(true);
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
    if (p.prompt !== undefined) setRestoredDefault(false);
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
      setLoadedPrompt(j.prompt);
      setRestoredDefault(true);
      toast.success("已恢复内置默认模板");
    } catch (e) {
      toast.error(`恢复失败: ${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  const promptDirty = cfg.prompt !== loadedPrompt;

  // 提示词来源徽标。后端不存"自定义/内置"标记:未设置时 GET 直接回填内置模板全文,
  // 因此二者在响应里长得一模一样,本页无法可靠区分——只陈述能确证的事实:
  // 本次会话点过恢复默认 → 确定是内置模板;与载入内容不一致 → 确定是未保存的改动;
  // 其余情况标"来源未知"并说明留空即使用内置模板,不编造来源。
  let promptOrigin = { label: "来源未知", tone: "text-muted-foreground" };
  if (restoredDefault) {
    promptOrigin = { label: "内置默认模板", tone: "border-emerald-400/50 text-emerald-600 dark:text-emerald-400" };
  } else if (promptDirty) {
    promptOrigin = { label: "已修改（未保存）", tone: "border-amber-400/60 text-amber-600 dark:text-amber-400" };
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
              、决策引擎也未给出高置信判定时,由监管模型做语义审查（放通 / 判疑 / 转人工 / 拦截）
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
              <div className="flex items-start justify-between gap-2">
                <div>
                  <div className="flex flex-wrap items-center gap-1.5">
                    <p className="font-medium text-sm">审查提示词</p>
                    <Badge variant="outline" className={`px-1.5 py-0 text-[10px] ${promptOrigin.tone}`}>
                      {promptOrigin.label}
                    </Badge>
                  </div>
                  <p className="mt-0.5 text-muted-foreground text-xs">
                    模型据此判定 allow / flag / ask / deny，可直接编辑
                  </p>
                </div>
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-7 shrink-0 text-xs"
                  onClick={restorePrompt}
                  disabled={saving}
                >
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
              <div className="flex items-center justify-between gap-2 text-[11px] text-muted-foreground">
                <span>{promptDirty ? "有未保存的改动" : "与载入内容一致"}</span>
                <span>{cfg.prompt.length} 字</span>
              </div>

              {/* 提示词语义说明:用户要"配置提示词和规则",这里必须说清提示词能表达什么。 */}
              <div className="space-y-1.5 rounded-md bg-muted/40 px-3 py-2.5">
                <p className="font-medium text-[11px]">提示词里可以让模型输出的四种裁决</p>
                <ul className="space-y-1">
                  {VERDICTS.map((v) => (
                    <li key={v.token} className="flex flex-wrap items-baseline gap-x-1.5 text-[11px] leading-snug">
                      <span className={`font-medium font-mono ${v.tone}`}>{v.token}</span>
                      <span className="font-medium">{v.label}</span>
                      <span className="text-muted-foreground">{v.detail}</span>
                    </li>
                  ))}
                </ul>
                <p className="text-[10px] text-muted-foreground leading-relaxed">
                  其中 <span className="font-mono">flag</span>（AI判疑）是「标记但放通」这一档：工具照常执行、不阻塞、
                  不转人工，只在审批记录里留下一条判疑行。默认模板已包含 flag 的适用条件与 FLAG 条款；
                  自定义提示词时可直接要求模型在「可疑但不够拦」时输出 flag。
                </p>
                <p className="text-[10px] text-muted-foreground leading-relaxed">
                  无论自定义与否，服务端保存后都会在提示词末尾追加《审查输入边界》与《裁决输出协议》—— 输出格式（只输出
                  decision/comment 的 JSON、四项裁决的取值范围）由平台固定，无需自己声明；提示词只需要写
                  <span className="font-medium">判定策略</span>。
                </p>
                <p className="text-[10px] text-muted-foreground leading-relaxed">
                  来源标注说明：后端不记录「自定义 / 内置默认」标记（未设置时直接回填内置模板全文），
                  因此本页无法可靠区分二者，只标注能确证的状态。留空保存即恢复使用内置模板；
                  「恢复默认模板」会清空自定义内容并回填内置模板。
                </p>
              </div>
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
                      <SelectItem value="flag">判疑放通（标记但放行，留痕）</SelectItem>
                      <SelectItem value="ask">转人工审批</SelectItem>
                      <SelectItem value="deny">拦截</SelectItem>
                    </SelectContent>
                  </Select>
                </Field>
                <p className="text-[11px] text-muted-foreground leading-relaxed">
                  模型不可用时按此策略降级。「判疑放通」比「放行」更可取：工具照常执行，但每次失败都留下判疑记录，
                  事后能看出监管实际处于降级状态。
                </p>
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
                <p className="text-[11px] text-muted-foreground leading-relaxed">
                  仅对「转人工」这一档生效：等待超过该秒数仍未处理时按上方动作放行或拦截。
                  待审批与超时的记录都会计入门限总览的「AI转人工」。
                </p>
              </div>
            </CardContent>
          </Card>
        </div>
      )}

      {/* 监管模型关闭时,上面整块参数区都不显示 —— 这里补一句它意味着什么,避免用户
          误以为"关掉模型=监管全关"(决策引擎与规则仍然独立生效)。 */}
      {!cfg.enabled && (
        <div className="flex items-start gap-2 rounded-md border border-dashed px-3 py-2.5">
          <InfoIcon className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          <p className="text-muted-foreground text-xs leading-relaxed">
            监管模型未启用：规则未命中的调用不再有模型兜底，也不会产生「AI判疑 / AI放通 / AI转人工」这几档模型裁决。
            拦截规则与决策引擎是各自独立的一层，不受此开关影响。
          </p>
        </div>
      )}

      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-muted-foreground text-xs">
          {loadError
            ? "配置加载失败，为避免覆盖服务端现有配置，保存已禁用 — 请先重试加载。"
            : "提示词与参数改动需要保存后才生效；启用开关同样在保存后写入。"}
        </p>
        <div className="flex items-center gap-2">
          {loadError && (
            <Button variant="outline" size="sm" onClick={() => void load()} disabled={loading}>
              重新加载
            </Button>
          )}
          <Button size="sm" onClick={save} disabled={saving || loading || loadError}>
            {saving ? "保存中…" : "保存配置"}
          </Button>
        </div>
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
          操作审查的门限控制:拦截规则未命中 → 决策引擎快速判定 → 监管模型语义审查 → 人工审批, 最终落到五档动作（正则拦截
          / AI拦截 / AI判疑 / AI放通 / AI转人工）
        </p>
        {/* 五档动作速览:标题下方一行,先给用户"这一页在管什么"的完整心智模型。 */}
        <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
          <span className="inline-flex items-center gap-1">
            <BanIcon className="h-3.5 w-3.5 text-red-600 dark:text-red-400" />
            正则拦截
          </span>
          <span className="inline-flex items-center gap-1">
            <BotIcon className="h-3.5 w-3.5 text-rose-600 dark:text-rose-400" />
            AI拦截
          </span>
          <span className="inline-flex items-center gap-1">
            <ScanSearchIcon className="h-3.5 w-3.5 text-amber-600 dark:text-amber-400" />
            AI判疑（标记但放通）
          </span>
          <span className="inline-flex items-center gap-1">
            <CheckCircle2Icon className="h-3.5 w-3.5 text-emerald-600 dark:text-emerald-400" />
            AI放通
          </span>
          <span className="inline-flex items-center gap-1">
            <UserCheckIcon className="h-3.5 w-3.5 text-blue-600 dark:text-blue-400" />
            AI转人工
          </span>
        </div>
      </div>

      <GuardOverview />
      <DecisionEnginesCard />
      <div className="flex items-center gap-2 text-muted-foreground text-xs">
        <span className="h-px flex-1 bg-border" />
        以下为规则与决策引擎均未给出结论时的兜底审查
        <span className="h-px flex-1 bg-border" />
      </div>
      <JudgeCard />
      <TaskGuardNotes />
    </div>
  );
}
