"use client";

// 决策引擎详情:一个引擎的完整编辑页(?key=引擎标识;静态导出没有动态路由)。
//
// 引擎是后端静态定义的,不可增删,所以这页只有编辑与诊断:
//   头部   —— 返回 / 名称 / 标识 / 启用开关 / 保存 / 测试连通性;
//   配置卡 —— API 地址、模型、API Key(留空=不改动)、默认值提示;
//   诊断卡 —— 连通性测试面板(测试中 / 成功 / 失败 + 错误原文 + 最近一次时间);
//   预留位 —— available=false 的引擎给出显式说明,而不是让用户对着表单白填。

import * as React from "react";

import Link from "next/link";
import { useSearchParams } from "next/navigation";

import { ArrowLeftIcon, CircleAlertIcon, Loader2Icon, PlugZapIcon, SaveIcon, ServerCogIcon } from "lucide-react";
import { toast } from "sonner";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import type { DecisionEngine } from "@/lib/types";

import {
  buildPatch,
  draftFromEngine,
  type EngineDraft,
  EngineFields,
  patchSize,
  runEngineTest,
  TestPanel,
  type TestState,
} from "../_components/engine-form";

function DecisionDetailInner() {
  const searchParams = useSearchParams();
  const key = searchParams.get("key") ?? "";

  const [engine, setEngine] = React.useState<DecisionEngine | null>(null);
  const [missing, setMissing] = React.useState(false);
  const [draft, setDraft] = React.useState<EngineDraft>({ baseURL: "", model: "", apiKey: "" });
  const [saving, setSaving] = React.useState(false);
  const [clearing, setClearing] = React.useState(false);
  const [test, setTest] = React.useState<TestState>({ status: "idle" });
  // 记录草稿是为哪个引擎初始化的:重新拉取(保存后 / 测试后)不能覆盖用户正在输入的内容,
  // 只有"换了一个引擎"或"首次加载"才用服务端值重建草稿。
  const draftKeyRef = React.useRef("");

  const load = React.useCallback(async () => {
    if (!key) {
      setMissing(true);
      return;
    }
    try {
      const all = await api.decisionEngines();
      const found = all.find((e) => e.key === key);
      if (!found) {
        setMissing(true);
        return;
      }
      setEngine(found);
      if (draftKeyRef.current !== found.key) {
        draftKeyRef.current = found.key;
        setDraft(draftFromEngine(found));
      }
    } catch (e) {
      toast.error(`加载引擎配置失败:${(e as Error).message}`);
      setMissing(true);
    }
  }, [key]);

  React.useEffect(() => {
    void load();
  }, [load]);

  const patchDraft = (p: Partial<EngineDraft>) => setDraft((d) => ({ ...d, ...p }));

  const dirty = engine ? patchSize(buildPatch(engine, draft)) > 0 : false;

  const save = async () => {
    if (!engine) return;
    const patch = buildPatch(engine, draft);
    if (patchSize(patch) === 0) {
      toast.info("没有需要保存的修改");
      return;
    }
    setSaving(true);
    try {
      await api.updateDecisionEngine(engine.key, patch);
      toast.success(`「${engine.title}」配置已保存`);
      setDraft((d) => ({ ...d, apiKey: "" }));
      await load();
    } catch (e) {
      toast.error(`保存失败:${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  };

  const testConnectivity = async () => {
    if (!engine) return;
    setTest({ status: "testing" });
    try {
      const r = await runEngineTest(engine, draft);
      setTest(r);
      if (r.status === "ok") {
        toast.success(`连通正常${typeof r.latencyMs === "number" ? `(${r.latencyMs}ms)` : ""}`);
      } else {
        toast.error(`连通失败:${r.error || "未知错误"}`);
      }
      // 测试会顺带保存未提交的修改,所以清掉 Key 输入并重新拉取以同步 api_key_set。
      setDraft((d) => ({ ...d, apiKey: "" }));
      await load();
    } catch (e) {
      const message = (e as Error).message;
      setTest({ status: "fail", error: message, at: Date.now() });
      toast.error(`测试失败:${message}`);
    }
  };

  const toggleEnabled = async (next: boolean) => {
    if (!engine) return;
    setSaving(true);
    try {
      await api.updateDecisionEngine(engine.key, { enabled: next });
      toast.success(next ? `已启用「${engine.title}」` : `已停用「${engine.title}」`);
      await load();
    } catch (e) {
      toast.error(`操作失败:${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  };

  const clearKey = async () => {
    if (!engine) return;
    if (!window.confirm(`清除「${engine.title}」已保存的 API Key?清除后该引擎不可用,需重新填写。`)) return;
    setClearing(true);
    try {
      await api.updateDecisionEngine(engine.key, { api_key: "" });
      toast.success("已清除 API Key");
      setDraft((d) => ({ ...d, apiKey: "" }));
      await load();
    } catch (e) {
      toast.error(`清除失败:${(e as Error).message}`);
    } finally {
      setClearing(false);
    }
  };

  // 参数缺失或引擎不存在:给明确空态与返回出口,而不是渲染一个空表单让人误以为配置丢了。
  if (missing) {
    return (
      <div className="p-4 md:p-6">
        <Card>
          <CardContent className="flex flex-col items-center gap-3 py-16 text-center">
            <ServerCogIcon className="text-muted-foreground size-8 opacity-40" />
            <div>
              <p className="text-sm">
                {key ? `未找到标识为「${key}」的决策引擎。` : "缺少查询参数 key,无法定位决策引擎。"}
              </p>
              <p className="text-muted-foreground mt-1 text-xs">
                决策引擎由后端静态定义(如 jev、laya),标识需与之完全一致。
              </p>
            </div>
            <Button variant="outline" size="sm" asChild>
              <Link href="/system/decision">
                <ArrowLeftIcon className="size-4" />
                返回列表
              </Link>
            </Button>
          </CardContent>
        </Card>
      </div>
    );
  }

  if (!engine) {
    return (
      <div className="p-4 md:p-6">
        <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
      </div>
    );
  }

  const busy = saving || clearing;
  // 刚粘贴进草稿还没保存的 Key 也算"有凭据":runEngineTest 会先把它落盘再探测,
  // 若这里只看 api_key_set,用户填完 Key 却点不动测试按钮。
  const hasCredential = engine.api_key_set || Boolean(draft.apiKey.trim());

  // 测试按钮为什么被禁用/为什么点下去会顺带保存——集中成一句提示,避免嵌套三元堆在 JSX 里。
  const testHint = (() => {
    if (!hasCredential) return "需先填写 API Key 才能测试连通性。";
    if (dirty) return "存在未保存修改:测试会先保存当前表单再探测。";
    return undefined;
  })();

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="mb-1 flex items-center gap-2">
            <Button variant="ghost" size="sm" asChild>
              <Link href="/system/decision">
                <ArrowLeftIcon className="size-4" />
                决策引擎
              </Link>
            </Button>
            <Badge
              variant="outline"
              className={
                engine.available
                  ? "border-blue-500/30 bg-blue-500/15 text-blue-600"
                  : "border-amber-500/30 bg-amber-500/15 text-amber-600"
              }
            >
              {engine.available ? "可用" : "预留位"}
            </Badge>
            <Badge
              variant="outline"
              className={
                engine.enabled
                  ? "border-violet-500/30 bg-violet-500/15 text-violet-600"
                  : "bg-muted text-muted-foreground"
              }
            >
              {engine.enabled ? "已启用" : "未启用"}
            </Badge>
            {dirty ? (
              <Badge variant="outline" className="border-amber-500/30 bg-amber-500/15 text-amber-600">
                有未保存修改
              </Badge>
            ) : null}
          </div>
          <h1 className="truncate font-semibold text-xl">{engine.title}</h1>
          <p className="text-muted-foreground font-mono text-xs">{engine.key}</p>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <div className="flex items-center gap-2 rounded-lg border px-3 py-1.5">
            <Label htmlFor="detail-enabled" className="text-xs">
              启用
            </Label>
            <Switch
              id="detail-enabled"
              checked={engine.enabled}
              disabled={busy}
              onCheckedChange={(v) => void toggleEnabled(v)}
            />
          </div>
          <Button
            variant="outline"
            size="sm"
            disabled={busy || test.status === "testing"}
            onClick={() => void testConnectivity()}
          >
            {test.status === "testing" ? <Loader2Icon className="animate-spin" /> : <PlugZapIcon />}
            测试连通性
          </Button>
          <Button size="sm" disabled={busy} onClick={() => void save()}>
            {saving ? <Loader2Icon className="animate-spin" /> : <SaveIcon />}
            保存
          </Button>
        </div>
      </div>

      {engine.description ? <p className="text-muted-foreground mb-4 text-sm">{engine.description}</p> : null}

      {/* 预留位提示放在最前:用户看到 available=false 就该先知道"现在填了也不参与判定",
          否则会以为配好地址就能用。 */}
      {!engine.available ? (
        <Alert className="mb-4 border-amber-500/30 bg-amber-500/10">
          <CircleAlertIcon />
          <AlertTitle>协议预留位:当前不参与前置决策</AlertTitle>
          <AlertDescription>
            该引擎的客户端协议尚未接入,启用开关与下面的配置都会被保存,但拦截流水线不会调用它——预留位的连通性测试只验证根地址可达,不发送判定请求。
            {engine.endpoint_path ? "" : "接入后需由后端补上协议端点路径,届时本页无需改动即可生效。"}
          </AlertDescription>
        </Alert>
      ) : null}

      <div className="grid gap-4 lg:grid-cols-3">
        <Card className="lg:col-span-2">
          <CardHeader>
            <CardTitle className="text-sm">引擎配置</CardTitle>
            <CardDescription className="text-xs">
              地址填根地址,系统在运行时自动拼接协议路径;凭据只写不读,保存后无法再查看明文。
            </CardDescription>
          </CardHeader>
          <CardContent>
            <EngineFields
              engine={engine}
              draft={draft}
              onDraftChange={patchDraft}
              disabled={busy}
              onClearKey={() => void clearKey()}
              clearing={clearing}
            />
            <div className="mt-4 flex items-center gap-2">
              <Button size="sm" disabled={busy} onClick={() => void save()}>
                {saving ? <Loader2Icon className="animate-spin" /> : <SaveIcon />}
                保存配置
              </Button>
              <Button
                variant="ghost"
                size="sm"
                className="text-muted-foreground"
                disabled={busy || !dirty}
                onClick={() => setDraft(draftFromEngine(engine))}
              >
                还原修改
              </Button>
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="text-sm">运行状态</CardTitle>
            <CardDescription className="text-xs">当前生效值与连通性诊断</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <dl className="space-y-2 text-xs">
              <div className="flex items-start justify-between gap-2">
                <dt className="text-muted-foreground shrink-0">凭据</dt>
                <dd>
                  <Badge
                    variant="outline"
                    className={
                      engine.api_key_set
                        ? "border-emerald-500/30 bg-emerald-500/15 text-emerald-600"
                        : "bg-muted text-muted-foreground"
                    }
                  >
                    {engine.api_key_set ? "已配置" : "未配置"}
                  </Badge>
                </dd>
              </div>
              <div className="flex items-start justify-between gap-2">
                <dt className="text-muted-foreground shrink-0">默认地址</dt>
                <dd className="font-mono break-all text-right">{engine.default_base_url || "—"}</dd>
              </div>
              <div className="flex items-start justify-between gap-2">
                <dt className="text-muted-foreground shrink-0">默认模型</dt>
                <dd className="font-mono break-all text-right">{engine.default_model || "—"}</dd>
              </div>
              <div className="flex items-start justify-between gap-2">
                <dt className="text-muted-foreground shrink-0">协议路径</dt>
                <dd className="font-mono break-all text-right">{engine.endpoint_path || "未接入"}</dd>
              </div>
              <div className="flex items-start justify-between gap-2">
                <dt className="text-muted-foreground shrink-0">配置来源</dt>
                <dd className="text-right">
                  <a
                    href="/system/ai-guard"
                    className="underline decoration-dotted underline-offset-2 hover:text-foreground"
                  >
                    监管&规则 · AI监管
                  </a>
                </dd>
              </div>
            </dl>

            <div className="border-t pt-4">
              <TestPanel
                state={test}
                onTest={() => void testConnectivity()}
                disabled={busy || !hasCredential}
                disabledHint={testHint}
              />
            </div>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

// useSearchParams 在静态导出下必须包在 Suspense 里。
export default function DecisionDetailPage() {
  return (
    <React.Suspense fallback={null}>
      <DecisionDetailInner />
    </React.Suspense>
  );
}
