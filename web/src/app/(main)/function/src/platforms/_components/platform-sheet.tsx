"use client";

// 新建/编辑平台的抽屉(轻量表单)。
//
// 创建轻量化:新建只填名称(+主页/描述)即可,四类规范文本域已从创建表单移除——
// 用户此刻往往还没拿到平台文档,逼他当场填四段规范只会产出占位垃圾。创建成功后
// 直接跳转平台详情页,规范统一在详情页完善(那里有统一上传区与 AI 识别填入,
// 识别结果预填文本域、人工检查后再保存)。
// 编辑模式同样只维护名称/主页/描述/启用,规范编辑也收敛到详情页——避免同一份
// 规范出现两个编辑入口,列表页改一半、详情页又改一半,很难对账。

import * as React from "react";

import { useRouter } from "next/navigation";

import { Loader2Icon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import type { SRCPlatform } from "@/lib/types";

import { Field } from "./platform-shared";

// PlatformSheet 的 form 只装「用户改过的字段」,提交时整体发给后端(全量覆盖语义)。
export function PlatformSheet({
  platform,
  open,
  onOpenChange,
  onSaved,
}: {
  platform: SRCPlatform | null;
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onSaved: (p: SRCPlatform) => void;
}) {
  const router = useRouter();
  const [form, setForm] = React.useState<Partial<SRCPlatform>>({});
  const [busy, setBusy] = React.useState(false);

  // 每次打开都从入参重置:上次编辑到一半关掉再打开,不该看到上次的残留值。
  React.useEffect(() => {
    if (!open) return;
    setForm(
      platform ?? {
        name: "",
        homepage: "",
        description: "",
        enabled: true,
      },
    );
  }, [open, platform]);

  const set = (patch: Partial<SRCPlatform>) => setForm((f) => ({ ...f, ...patch }));

  const submit = async () => {
    const name = (form.name ?? "").trim();
    if (!name) {
      toast.error("平台名称必填");
      return;
    }
    setBusy(true);
    try {
      const r = await api.saveSRCPlatform({ ...form, name });
      if (platform) {
        toast.success("平台已保存");
        onSaved(r.platform);
      } else {
        // 创建即跳详情:规范四段文本与附件(AI 识别填入)统一在详情页完成,
        // 列表页不留半个规范编辑入口,避免「创建时草草填、之后忘了改」。
        toast.success("平台已创建，规范请在详情页完善");
        onSaved(r.platform);
        router.push(`/function/src/platforms/detail?id=${r.platform.id}`);
      }
    } catch (e) {
      // 重名由后端判 409,这里把原文透出去(「同名平台已存在」比「保存失败」有用得多)。
      toast.error(`保存失败:${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="flex flex-col gap-0 p-0 sm:max-w-xl">
        <SheetHeader className="border-b px-6 py-4">
          <SheetTitle>{platform ? "编辑平台" : "新建平台"}</SheetTitle>
          <SheetDescription className="text-xs">
            仅填名称即可创建。平台规范(奖励标准/评分标准/守则/规范)与附件请进平台详情页完善，支持上传后 AI 识别填入
          </SheetDescription>
        </SheetHeader>

        <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-6 py-5">
          <Field label="平台名称">
            <Input
              value={form.name ?? ""}
              onChange={(e) => set({ name: e.target.value })}
              placeholder="补天 / 漏洞盒子 / 教育 SRC"
            />
          </Field>

          <Field label="主页">
            <Input
              value={form.homepage ?? ""}
              onChange={(e) => set({ homepage: e.target.value })}
              placeholder="https://www.butian.net"
              className="font-mono"
            />
          </Field>

          <Field label="描述">
            <Input
              value={form.description ?? ""}
              onChange={(e) => set({ description: e.target.value })}
              placeholder="国内主流众测平台，厂商自行发布众测项目与奖励标准。"
            />
          </Field>

          {/* 启用开关只在编辑模式出现:新建默认启用,不必让用户在创建时多做一次决定。 */}
          {platform && (
            // Switch 渲染成 button,<label> 关联不到它(可访问名由 aria-label 提供),故用 div 排版。
            <div className="flex items-center gap-3">
              <Switch
                checked={form.enabled ?? true}
                onCheckedChange={(v) => set({ enabled: v })}
                aria-label="启用此平台"
              />
              <div>
                <p className="font-medium text-sm">启用此平台</p>
                <p className="text-muted-foreground text-xs">停用后该平台不出现在建单选择里，已有目标与成果保留</p>
              </div>
            </div>
          )}
        </div>

        <SheetFooter className="flex-row justify-end gap-2 border-t px-6 py-4">
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button disabled={busy} onClick={() => void submit()}>
            {busy && <Loader2Icon className="size-4 animate-spin" />}
            {busy ? "保存中…" : "保存"}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  );
}
