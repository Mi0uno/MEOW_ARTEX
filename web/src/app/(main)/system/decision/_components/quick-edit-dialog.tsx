"use client";

// 列表页的快速编辑对话框:不离开列表就能改端点/模型/凭据,并顺手测一次连通。
// 详情页负责完整编辑与预留位说明,这里只做"高频小改"的快捷通道——两者共用 EngineFields,
// 保证字段语义(尤其 api_key 留空=不修改)完全一致。

import * as React from "react";

import { Loader2Icon, SaveIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import type { DecisionEngine } from "@/lib/types";

import { buildPatch, draftFromEngine, type EngineDraft, EngineFields, patchSize, runEngineTest } from "./engine-form";

export function QuickEditDialog({
  engine,
  open,
  onOpenChange,
  onSaved,
}: {
  engine: DecisionEngine | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSaved: () => void;
}) {
  const [draft, setDraft] = React.useState<EngineDraft>({ baseURL: "", model: "", apiKey: "" });
  const [saving, setSaving] = React.useState(false);
  const [testing, setTesting] = React.useState(false);
  const [clearing, setClearing] = React.useState(false);

  // 每次打开都以当前行数据重建草稿:对话框是"快照编辑",残留上一次的输入会造成误提交。
  React.useEffect(() => {
    if (engine && open) setDraft(draftFromEngine(engine));
  }, [engine, open]);

  const patchDraft = (p: Partial<EngineDraft>) => setDraft((d) => ({ ...d, ...p }));

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
      onSaved();
    } catch (e) {
      toast.error(`保存失败:${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  };

  const test = async () => {
    if (!engine) return;
    setTesting(true);
    try {
      const r = await runEngineTest(engine, draft);
      if (r.status === "ok") {
        toast.success(`连通正常${typeof r.latencyMs === "number" ? `(${r.latencyMs}ms)` : ""}`);
      } else {
        toast.error(`连通失败:${r.error || "未知错误"}`);
      }
      setDraft((d) => ({ ...d, apiKey: "" }));
      onSaved();
    } catch (e) {
      toast.error(`测试失败:${(e as Error).message}`);
    } finally {
      setTesting(false);
    }
  };

  const toggleEnabled = async (v: boolean) => {
    if (!engine) return;
    setSaving(true);
    try {
      await api.updateDecisionEngine(engine.key, { enabled: v });
      toast.success(v ? `已启用「${engine.title}」` : `已停用「${engine.title}」`);
      onSaved();
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
      onSaved();
    } catch (e) {
      toast.error(`清除失败:${(e as Error).message}`);
    } finally {
      setClearing(false);
    }
  };

  const busy = saving || testing || clearing;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            快速编辑 · {engine?.title ?? ""}
            {engine ? (
              <Badge variant="outline" className="font-mono px-1.5 py-0 text-[10px]">
                {engine.key}
              </Badge>
            ) : null}
          </DialogTitle>
          <DialogDescription className="text-xs">
            {engine?.description || "修改端点、模型与凭据。API Key 留空表示不修改当前凭据。"}
          </DialogDescription>
        </DialogHeader>

        {engine ? (
          <div className="space-y-4">
            <div className="flex items-center justify-between gap-2 rounded-lg border px-3 py-2">
              <div className="min-w-0">
                <Label htmlFor="quick-edit-enabled" className="text-xs">
                  启用该引擎
                </Label>
                <p className="text-muted-foreground text-[11px]">
                  {engine.available ? "启用后参与前置决策流水线" : "协议预留位:启用仅记录意图,接入后才生效"}
                </p>
              </div>
              <Switch
                id="quick-edit-enabled"
                checked={engine.enabled}
                disabled={busy}
                onCheckedChange={(v) => void toggleEnabled(v)}
              />
            </div>

            <EngineFields
              engine={engine}
              draft={draft}
              onDraftChange={patchDraft}
              disabled={busy}
              onClearKey={() => void clearKey()}
              clearing={clearing}
            />
          </div>
        ) : null}

        <DialogFooter>
          <Button variant="outline" size="sm" disabled={busy} onClick={() => void test()}>
            {testing ? <Loader2Icon className="animate-spin" /> : null} 测试连通
          </Button>
          <Button size="sm" disabled={busy} onClick={() => void save()}>
            {saving ? <Loader2Icon className="animate-spin" /> : <SaveIcon />} 保存
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
