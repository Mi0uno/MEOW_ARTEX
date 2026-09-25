"use client";

// 建 SRC 任务对话框。两种用法:
//
//  1. 详情页进来(platform + target 已给定):对「平台 × 目标」开任务,用户只填名称/目标;
//  2. SRC 任务页快速创建(platform + target 为 undefined):**强制先选平台与目标**。
//
// 为什么第二种必须强制选:任务的授权边界完全来自「平台规范 + 目标在该平台下的授权范围」。
// 不选就建任务,后端不会注入任何约束——AI 会在无边界状态下动作,这恰恰是 SRC 模式要防的事。
// 所以宁可在按钮上拦住用户,也不放过一个没有边界的 SRC 任务。
//
// 放在 web/src/components/ 而不是某个路由段的 _components/ 下:_ 前缀目录是路由段私有,
// 跨段引用(任务页 ← src/_components)会因 Turbopack 按路由分段打包产生加载异常——
// 实测表现为「组件在产物里、onClick 也触发了,但组件就是不挂载」。本仓库约定:
// 跨模块共享组件一律放 components/ 根目录。
//
// 关键约束:src_mode 必须为 true,src_platform_id / src_target_id 必须带上。

import * as React from "react";

import { useRouter } from "next/navigation";

import { Loader2Icon, RocketIcon, ShieldAlertIcon, ShieldCheckIcon } from "lucide-react";
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
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { ProxyEgressSelect, proxyGroupIdForSubmit, type ProxyEgressMode } from "@/components/proxy-egress-select";
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
  /** 给定 = 锁定该平台;不给 = 由用户在对话框里选(快速创建模式)。 */
  platform?: SRCPlatform;
  /** 给定 = 锁定该目标;不给 = 由用户选(需先选平台)。 */
  target?: SRCTarget;
  /** 任务 id 在 wire 上是字符串(后端 int64 → string 约定),不要当数字用。 */
  onCreated?: (taskId: string) => void;
}) {
  const router = useRouter();
  const [name, setName] = React.useState("");
  const [goal, setGoal] = React.useState("");
  const [description, setDescription] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  // 快速创建模式(未给定 platform/target)下的用户选择。哨兵串:Radix Select 的
  // value="" 是保留值(表示未选中),空语义必须用哨兵。
  const [platformId, setPlatformId] = React.useState("__none__");
  const [targetId, setTargetId] = React.useState("__none__");
  const [platforms, setPlatforms] = React.useState<SRCPlatform[]>([]);
  const [targets, setTargets] = React.useState<SRCTarget[]>([]);
  const [loadingTargets, setLoadingTargets] = React.useState(false);
  // 出网方式(任务级覆盖):SRC 任务的靶标往往在公网且需要隐藏来源 IP,
  // 所以这里必须能选代理池——早先这个对话框漏了它,用户只能拿到「跟随全局」。
  const [proxyMode, setProxyMode] = React.useState<ProxyEgressMode>("");
  const [proxyGroupID, setProxyGroupID] = React.useState(0);

  // 锁定模式:详情页给了 platform/target,用户不需要也不能改(改了就换了授权边界)。
  const locked = !!platform && !!target;

  // 快速创建模式下拉里选中的对象。锁定模式下直接用 props。
  const pickedPlatform = React.useMemo(
    () => (locked ? platform : platforms.find((p) => String(p.id) === platformId)),
    [locked, platform, platforms, platformId],
  );
  const pickedTarget = React.useMemo(
    () => (locked ? target : targets.find((t) => String(t.id) === targetId)),
    [locked, target, targets, targetId],
  );

  // 打开时加载平台列表(仅快速创建模式需要)。
  React.useEffect(() => {
    if (!open || locked) return;
    setPlatformId("__none__");
    setTargetId("__none__");
    setTargets([]);
    api
      .srcPlatforms({ limit: 200 })
      .then((r) => setPlatforms(r.platforms ?? []))
      .catch(() => setPlatforms([]));
  }, [open, locked]);

  // 选平台后加载该平台下的目标:目标是「平台×目标」的从属维度,必须先有平台。
  React.useEffect(() => {
    if (!open || locked || platformId === "__none__") {
      setTargets([]);
      setTargetId("__none__");
      return;
    }
    let alive = true;
    setLoadingTargets(true);
    setTargetId("__none__");
    api
      .srcPlatformTargets(Number(platformId), { limit: 200 })
      .then((r) => {
        if (alive) setTargets(r.targets ?? []);
      })
      .catch(() => {
        if (alive) setTargets([]);
      })
      .finally(() => {
        if (alive) setLoadingTargets(false);
      });
    return () => {
      alive = false;
    };
  }, [open, locked, platformId]);

  // 每次打开都按当前平台/目标重置:同一个对话框会服务不同的目标,残留上次的输入会开错任务。
  // 出网方式同样归零(残留「走代理池」会让下一个任务意外走池,是安全语义,必须重置)。
  // 快速创建模式下等用户选完再填默认名(见下一个 effect)。
  React.useEffect(() => {
    if (!open) return;
    setProxyMode("");
    setProxyGroupID(0);
    if (!locked) {
      setName("");
      setGoal("");
      setDescription("");
      return;
    }
    setName(defaultName(platform, target));
    setGoal(defaultGoal(platform, target));
    setDescription(`SRC 模式任务 · ${platform.name} · ${target.name}`);
  }, [open, locked, platform, target]);

  // 快速创建模式:选完「平台×目标」后自动填默认名称/目标,用户仍可改。
  React.useEffect(() => {
    if (!open || locked) return;
    if (!pickedPlatform || !pickedTarget) return;
    setName(defaultName(pickedPlatform, pickedTarget));
    setGoal(defaultGoal(pickedPlatform, pickedTarget));
    setDescription(`SRC 模式任务 · ${pickedPlatform.name} · ${pickedTarget.name}`);
  }, [open, locked, pickedPlatform, pickedTarget]);

  const submit = async () => {
    const n = name.trim();
    if (!n) {
      toast.error("任务名称必填");
      return;
    }
    // 快速创建模式必须选齐平台与目标:缺任一个都不会注入授权边界。
    if (!pickedPlatform || !pickedTarget) {
      toast.error("请先选择平台与目标——授权边界来自它们,不选就不会注入约束");
      return;
    }
    setBusy(true);
    try {
      const t = await api.createTask({
        name: n,
        description: description.trim() || n,
        goal: goal.trim() || defaultGoal(pickedPlatform, pickedTarget),
        // 目标未关联企业时给空数组:后端校验企业存在性,传 [0] 会被判无效。
        companyIds: pickedTarget.company_id ? [pickedTarget.company_id] : [],
        stage: "exploit",
        srcMode: true,
        srcPlatformId: pickedPlatform.id,
        srcTargetId: pickedTarget.id,
        // 出网方式:默认跟随全局;选「走代理池」才带组 id。
        proxyMode,
        proxyGroupId: proxyGroupIdForSubmit({ mode: proxyMode, groupId: proxyGroupID }),
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
            {locked ? (
              <>
                任务会绑定「{platform.name} × {target.name}」,创建时自动把平台规范与目标在该平台下的授权范围
                转成硬约束注入 AI,防止越界。
              </>
            ) : (
              <>必须选择平台与目标:授权边界来自「平台规范 + 目标在该平台下的授权范围」,不选则不会注入约束。</>
            )}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          {locked ? (
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
          ) : (
            <>
              <div className="space-y-1.5">
                <Label className="font-medium text-muted-foreground text-xs uppercase tracking-wide">平台</Label>
                <Select value={platformId} onValueChange={setPlatformId}>
                  <SelectTrigger className="w-full">
                    <SelectValue placeholder="选择 SRC 平台" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      <SelectItem value="__none__">请选择平台…</SelectItem>
                      {platforms.map((p) => (
                        <SelectItem key={p.id} value={String(p.id)}>
                          {p.name}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
              </div>

              <div className="space-y-1.5">
                <Label className="font-medium text-muted-foreground text-xs uppercase tracking-wide">目标</Label>
                <Select
                  value={targetId}
                  onValueChange={setTargetId}
                  disabled={platformId === "__none__" || loadingTargets}
                >
                  <SelectTrigger className="w-full">
                    <SelectValue
                      placeholder={
                        platformId === "__none__"
                          ? "请先选择平台"
                          : loadingTargets
                            ? "加载中…"
                            : "选择该平台下的目标"
                      }
                    />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      <SelectItem value="__none__">请选择目标…</SelectItem>
                      {targets.map((t) => (
                        <SelectItem key={t.id} value={String(t.id)}>
                          {t.name}
                          {t.company_name ? ` · ${t.company_name}` : ""}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
                {platformId !== "__none__" && !loadingTargets && targets.length === 0 && (
                  <p className="text-muted-foreground text-xs">
                    该平台下还没有目标。先到「平台选择 → 进入平台 → 目标」添加目标并配好授权范围。
                  </p>
                )}
              </div>

              {pickedPlatform && pickedTarget ? (
                <div className="flex items-start gap-2 rounded-lg border border-emerald-500/30 bg-emerald-500/10 p-3 text-xs">
                  <ShieldCheckIcon className="mt-0.5 size-4 shrink-0 text-emerald-600" />
                  <div className="space-y-1">
                    <p className="font-medium text-emerald-700">将注入的约束</p>
                    <p className="text-muted-foreground">
                      平台「{pickedPlatform.name}」的四类规范(含附件解析正文)+
                      「{pickedTarget.name}」在本平台下的授权范围、禁止事项、允许类型、速率限制
                    </p>
                  </div>
                </div>
              ) : (
                <div className="flex items-start gap-2 rounded-lg border border-amber-500/30 bg-amber-500/10 p-3 text-xs">
                  <ShieldAlertIcon className="mt-0.5 size-4 shrink-0 text-amber-600" />
                  <p className="text-muted-foreground">
                    未选平台与目标前无法创建:没有授权边界的 SRC 任务会让 AI 在范围外动作。
                  </p>
                </div>
              )}
            </>
          )}

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

          {/* 出网方式:与通用建任务抽屉同一组件,保证四个建任务入口选项一致。 */}
          <ProxyEgressSelect
            mode={proxyMode}
            groupId={proxyGroupID}
            onChange={(v) => {
              setProxyMode(v.mode);
              setProxyGroupID(v.groupId);
            }}
          />
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          {/* 未选齐平台与目标时按钮直接禁用(不只是提交时校验):让「必须有授权边界」
              这件事在界面上就是硬约束,而不是点了才报错。 */}
          <Button disabled={busy || !pickedPlatform || !pickedTarget} onClick={() => void submit()}>
            {busy ? <Loader2Icon className="size-4 animate-spin" /> : <RocketIcon className="size-4" />}
            {busy ? "创建中…" : "创建并启动"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
