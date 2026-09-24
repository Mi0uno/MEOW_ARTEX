"use client";

// 模型配置的快速编辑抽屉（新建 / 编辑共用）。
//
// 它现在只是「详情页的表单」的一个薄壳：字段、校验、保存体组装都在 profile-form.tsx
// 里，与 /system/llm/detail 共用同一套实现。列表页保留它，是为了让「改个优先级」
// 这类小改不必跳页；重活（拉模型、看运行时状态、配重试覆盖）去详情页。

import * as React from "react";

import { Loader2Icon, PlugZapIcon, PlusIcon, SaveIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api } from "@/lib/api";
import type { LLMProfile } from "@/lib/types";

import { buildPayload, ProfileFields, useProfileForm, validateValues } from "./profile-form";

export function ProfileSheet({
  profile,
  open,
  onOpenChange,
  onSaved,
}: {
  profile: LLMProfile | null; // null = 新建
  open: boolean;
  onOpenChange: (o: boolean) => void;
  onSaved: (id: string) => void;
}) {
  const isNew = !profile;
  const form = useProfileForm();
  const { hydrate } = form;
  const [saving, setSaving] = React.useState(false);
  // 侧栏弹出卡顿修复：滑入动画（200ms）期间不同步挂载整棵长表单，动画结束后再挂载，
  // 动画帧只画骨架 → 不掉帧。所有侧栏抽屉通用此模式。
  const [sheetReady, setSheetReady] = React.useState(false);

  React.useEffect(() => {
    if (!open) {
      setSheetReady(false);
      return;
    }
    const t = setTimeout(() => setSheetReady(true), 240);
    return () => clearTimeout(t);
  }, [open]);

  // 每次打开时从传入的 profile 灌一遍表单（新建则重置为默认值）。抽屉关掉再打开
  // 就是一次干净的开始，不会留下上一个配置的残影。
  React.useEffect(() => {
    if (!open) return;
    hydrate(profile);
  }, [open, profile, hydrate]);

  async function save() {
    const err = validateValues(form.v);
    if (err) {
      toast.error(err);
      return;
    }
    if (saving) return;
    setSaving(true);
    try {
      const { id } = await api.saveLLMProfile(buildPayload(form.v, form.profileId));
      if (isNew) toast.success(`已新建：${form.v.name.trim()}（在列表上「设为激活」以启用）`);
      else toast.success(profile?.is_default ? "已保存，激活配置即时生效，无需重启" : "已保存");
      onSaved(String(id));
      onOpenChange(false);
    } catch (e) {
      toast.error(`保存失败：${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="right"
        className="flex flex-col gap-0 p-0 data-[side=right]:min-w-[420px] data-[side=right]:sm:max-w-xl"
      >
        <SheetHeader className="px-4">
          <SheetTitle className="flex items-center gap-2">
            {isNew ? "新建模型配置" : `编辑：${profile?.name}`}
            {profile?.is_default && (
              <Badge variant="outline" className="border-amber-400/50 text-amber-500">
                激活中
              </Badge>
            )}
          </SheetTitle>
          <SheetDescription>
            {isNew
              ? "新建后不会自动激活，请在列表上「设为激活」以启用。"
              : "修改后点击保存；激活配置保存后对全部 Agent 立即生效。"}
          </SheetDescription>
        </SheetHeader>

        {/* 侧栏弹出卡顿修复：滑入动画帧里只画骨架，动画结束后再挂载整棵表单 */}
        {!sheetReady ? (
          <div className="flex min-h-0 flex-1 flex-col gap-3 px-4 py-4">
            {[0, 1, 2, 3, 4, 5, 6, 7].map((i) => (
              <div
                key={i}
                className="bg-muted/60 h-9 animate-pulse rounded-md"
                style={{ width: `${90 - (i % 3) * 10}%` }}
              />
            ))}
          </div>
        ) : (
          <>
            <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 pb-4">
              <ProfileFields form={form} idPrefix="pf" />
            </div>

            <div className="flex gap-2 border-t px-4 py-3">
              <Button variant="outline" onClick={() => void form.testConnection()} disabled={form.testing}>
                {form.testing ? <Loader2Icon className="animate-spin" /> : <PlugZapIcon />}
                {form.testing ? "测试中…" : "测试连接"}
              </Button>
              <Button onClick={() => void save()} disabled={saving} className="flex-1">
                {saving && <Loader2Icon className="animate-spin" />}
                {!saving && (isNew ? <PlusIcon /> : <SaveIcon />)}
                {isNew ? "新建" : "保存"}
              </Button>
            </div>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}
