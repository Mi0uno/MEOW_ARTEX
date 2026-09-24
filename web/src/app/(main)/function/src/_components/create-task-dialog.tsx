"use client";

// 建 SRC 任务对话框:从平台/目标详情页直接对某个「平台 × 目标」开任务。
//
// 为什么要有它:原先只有「平台批量」页能建任务,用户进了平台、进了目标都找不到入口——
// 而详情页恰恰是「我已经选好这个目标了,现在开干」的自然位置。批量页适合勾选多个,
// 单个目标的入口应该就在它自己页面上。
//
// 关键约束:src_mode 必须为 true,src_platform_id / src_target_id 必须带上,
// 否则后端不会注入该平台的规范与该目标在该平台下的授权范围——任务就失去了防越界边界。

import * as React from "react";

import { useRouter } from "next/navigation";

import { Loader2Icon, RocketIcon, ShieldCheckIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { SRCPlatform, SRCTarget } from "@/lib/types";

/** 默认任务描述/目标:让用户不必从零写,但仍可改。 */
function defaultName(platform: SRCPlatform, target: SRCTarget): string {
  return `SRC · ${platform.name} · ${target.name}`;
}
function defaultGoal(platform: SRCPlatform, target: SRCTarget): string {
  return `在 ${platform.name} 的授权范围内对 ${target.name} 进行漏洞挖掘并产出可提交成果`;
}

export function CreateSRCTaskDialog({
  open,
  onOpenChange,
  platform,
  target,
  /** 建成功后的跳转目标:默认进任务详情,便于立刻看规划与约束。 */
  onCreated,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  platform: SRCPlatform;
  target: SRCTarget;
  /** 任务 id 在 wire 上是字符串(后端 int64 → string 约定),不要当数字用。 */
  onCreated?: (taskId: string) => void;
}) {
  const router = useRouter();
  const [name, setName] = React.useState("");
  const [goal, setGoal] = React.useState("");
  const [description, setDescription] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  // 每次打开都按当前平台/目标重置:同一个对话框会服务不同的目标,残留上次的输入会开错任务。
  React.useEffect(() => {
    if (!open) return;
    setName(defaultName(platform, target));
    setGoal(defaultGoal(platform, target));
    setDescription(`SRC 模式任务 · ${platform.name} · ${target.name}`);
  }, [open, platform, target]);

  const submit = async () => {
    const n = name.trim();
    if (!n) {
      toast.error("任务名称必填");
      return;
    }
    setBusy(true);
    try {
      const t = await api.createTask({
        name: n,
        description: description.trim() || n,
        goal: goal.trim() || defaultGoal(platform, target),
        // 目标未关联企业时给空数组:后端校验企业存在性,传 [0] 会被判无效。
        companyIds: target.company_id ? [target.company_id] : [],
        stage: "exploit",
        srcMode: true,
        srcPlatformId: platform.id,
        srcTargetId: target.id,
      });
      toast.success("SRC 任务已创建,平台规范与授权范围已注入");
      onOpenChange(false);
      if (onCreated) onCreated(t.id);
      else router.push(`/function/tasks/detail?id=${t.id}`);
    } catch (e) {
      toast.error(`创建失败:${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>新建 SRC 任务</DialogTitle>
          <DialogDescription className="text-xs">
            任务会绑定「{platform.name} × {target.name}」,创建时自动把平台规范与目标在该平台下的授权范围
            转成硬约束注入 AI,防止越界。
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="flex items-start gap-2 rounded-lg border border-emerald-500/30 bg-emerald-500/10 p-3 text-xs">
            <ShieldCheckIcon className="mt-0.5 size-4 shrink-0 text-emerald-600" />
            <div className="space-y-1">
              <p className="font-medium text-emerald-700">将注入的约束</p>
              <p className="text-muted-foreground">
                平台「{platform.name}」的奖励标准 / 评分标准 / 守则 / 规范(含附件解析正文)
                {target.name} 在本平台下的授权范围、禁止事项、允许类型、速率限制
              </p>
            </div>
          </div>

          <div className="space-y-1.5">
            <Label className="font-medium text-muted-foreground text-xs uppercase tracking-wide">任务名称</Label>
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="任务名称" />
          </div>

          <div className="space-y-1.5">
            <Label className="font-medium text-muted-foreground text-xs uppercase tracking-wide">任务目标</Label>
            <Textarea
              value={goal}
              onChange={(e) => setGoal(e.target.value)}
              rows={3}
              placeholder="要达成的结果"
            />
          </div>

          <div className="space-y-1.5">
            <Label className="font-medium text-muted-foreground text-xs uppercase tracking-wide">描述(可选)</Label>
            <Input
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="补充说明"
            />
          </div>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button disabled={busy} onClick={() => void submit()}>
            {busy ? <Loader2Icon className="size-4 animate-spin" /> : <RocketIcon className="size-4" />}
            {busy ? "创建中…" : "创建并启动"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
