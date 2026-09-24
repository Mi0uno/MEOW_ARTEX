"use client";

// Decision:决策引擎(前置决策模型)配置页。
// JEV / Laya 这类决策模型的凭据、端点与模型在此配置;启用开关与流水线编排见 监管&规则 · AI监管。

import * as React from "react";

import { KeyRoundIcon, SaveIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import type { DecisionEngine } from "@/lib/types";

function EngineConfigCard({ engine, onChanged }: { engine: DecisionEngine; onChanged: () => void }) {
  const [baseURL, setBaseURL] = React.useState(engine.base_url);
  const [model, setModel] = React.useState(engine.model);
  const [apiKey, setApiKey] = React.useState("");
  const [saving, setSaving] = React.useState(false);
  const [testing, setTesting] = React.useState(false);
  const [testResult, setTestResult] = React.useState("");

  // 外部重新加载后同步非敏感字段。
  React.useEffect(() => {
    setBaseURL(engine.base_url);
    setModel(engine.model);
  }, [engine.base_url, engine.model]);

  const save = async (patch: { enabled?: boolean; base_url?: string; api_key?: string; model?: string }) => {
    setSaving(true);
    try {
      await api.updateDecisionEngine(engine.key, patch);
      toast.success(`「${engine.title}」配置已保存`);
      onChanged();
    } catch (e) {
      toast.error(`保存失败: ${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  };

  const test = async () => {
    setTesting(true);
    setTestResult("");
    try {
      // 测试前先把表单里未保存的修改(端点/模型/新凭据)落盘,保证测的就是当前填的内容。
      const patch: { base_url?: string; model?: string; api_key?: string } = {};
      if (baseURL !== engine.base_url) patch.base_url = baseURL;
      if (model !== engine.model) patch.model = model;
      if (apiKey.trim()) patch.api_key = apiKey.trim();
      if (Object.keys(patch).length > 0) {
        await api.updateDecisionEngine(engine.key, patch);
        setApiKey("");
        onChanged();
      }
      const r = await api.testDecisionEngine(engine.key);
      setTestResult(
        r.ok ? `连通正常(${r.latency_ms ?? 0}ms)${r.detail ? ` · ${r.detail}` : ""}` : `失败:${r.error ?? "未知错误"}`,
      );
    } catch (e) {
      setTestResult(`失败:${(e as Error).message}`);
    } finally {
      setTesting(false);
    }
  };

  return (
    <Card>
      <CardContent className="space-y-4 p-4">
        <div className="flex items-center justify-between gap-2">
          <div className="flex items-center gap-2">
            <span className="font-medium text-sm">{engine.title}</span>
            {engine.available ? (
              <Badge variant="secondary" className="px-1.5 py-0 text-[10px]">
                已接入
              </Badge>
            ) : (
              <Badge variant="outline" className="px-1.5 py-0 text-[10px] text-amber-600">
                协议预留
              </Badge>
            )}
            {engine.enabled && (
              <Badge variant="outline" className="border-violet-500/30 px-1.5 py-0 text-[10px] text-violet-600">
                已启用
              </Badge>
            )}
          </div>
          <div className="flex items-center gap-2">
            <span className="text-muted-foreground text-xs">{engine.enabled ? "启用中" : "未启用"}</span>
            <Switch checked={engine.enabled} onCheckedChange={(v) => void save({ enabled: v })} />
          </div>
        </div>
        <p className="text-muted-foreground text-xs">{engine.description}</p>

        <div className="grid gap-3 md:grid-cols-2">
          <div className="grid gap-1.5">
            <Label htmlFor={`${engine.key}-base`} className="text-xs">
              API 地址
            </Label>
            <Input
              id={`${engine.key}-base`}
              value={baseURL}
              onChange={(e) => setBaseURL(e.target.value)}
              placeholder={engine.default_base_url || "https://…"}
              className="font-mono text-xs"
            />
            {engine.endpoint_path ? (
              <p className="text-muted-foreground text-[11px]">填根地址即可,系统自动拼接 {engine.endpoint_path}</p>
            ) : null}
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor={`${engine.key}-model`} className="text-xs">
              模型
            </Label>
            <Input
              id={`${engine.key}-model`}
              value={model}
              onChange={(e) => setModel(e.target.value)}
              placeholder={engine.default_model || "decision-model"}
              className="font-mono text-xs"
            />
          </div>
          <div className="grid gap-1.5 md:col-span-2">
            <Label htmlFor={`${engine.key}-key`} className="text-xs">
              API Key
            </Label>
            <div className="flex items-center gap-2">
              <KeyRoundIcon className="size-4 shrink-0 text-muted-foreground" />
              <Input
                id={`${engine.key}-key`}
                type="password"
                value={apiKey}
                onChange={(e) => setApiKey(e.target.value)}
                placeholder={engine.api_key_set ? "已配置(输入新值覆盖;留空保持不变)" : "未配置"}
                className="font-mono text-xs"
                autoComplete="new-password"
              />
              {engine.api_key_set && (
                <Button
                  variant="ghost"
                  size="sm"
                  className="shrink-0 text-muted-foreground"
                  disabled={saving}
                  onClick={() => void save({ api_key: "" })}
                >
                  清除
                </Button>
              )}
            </div>
          </div>
        </div>

        <div className="flex items-center justify-between gap-2">
          {testResult ? (
            <p className={`text-xs ${testResult.startsWith("失败") ? "text-destructive" : "text-emerald-600"}`}>
              {testResult}
            </p>
          ) : (
            <span />
          )}
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              title="保存当前表单(含新输入的 Key)并测试连通"
              disabled={testing || saving || (!engine.api_key_set && !apiKey.trim())}
              onClick={() => void test()}
            >
              测试连通
            </Button>
            <Button
              size="sm"
              disabled={saving}
              onClick={() => {
                const patch: { base_url?: string; model?: string; api_key?: string } = {};
                if (baseURL !== engine.base_url) patch.base_url = baseURL;
                if (model !== engine.model) patch.model = model;
                if (apiKey.trim()) patch.api_key = apiKey.trim();
                if (Object.keys(patch).length === 0) {
                  toast.info("没有需要保存的修改");
                  return;
                }
                void save(patch).then(() => setApiKey(""));
              }}
            >
              <SaveIcon /> 保存
            </Button>
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

export default function DecisionPage() {
  const [engines, setEngines] = React.useState<DecisionEngine[]>([]);
  const [loaded, setLoaded] = React.useState(false);

  const load = React.useCallback(async () => {
    try {
      setEngines(await api.decisionEngines());
    } catch (e) {
      toast.error(`加载决策引擎失败: ${(e as Error).message}`);
    } finally {
      setLoaded(true);
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  return (
    <div className="flex flex-1 flex-col gap-4 p-4 md:p-6">
      <div>
        <h1 className="font-semibold text-xl tracking-tight">Decision</h1>
        <p className="mt-0.5 text-muted-foreground text-sm">
          决策模型(前置决策引擎)配置:JEV / Laya 等在拦截规则未命中时做快速结构化安全评估;启用与流水线编排见{" "}
          <a href="/system/ai-guard" className="underline decoration-dotted underline-offset-2 hover:text-foreground">
            监管&规则 · AI监管
          </a>
        </p>
      </div>
      {!loaded ? (
        <p className="py-16 text-center text-muted-foreground text-sm">加载中…</p>
      ) : (
        engines.map((engine) => <EngineConfigCard key={engine.key} engine={engine} onChanged={load} />)
      )}
    </div>
  );
}
