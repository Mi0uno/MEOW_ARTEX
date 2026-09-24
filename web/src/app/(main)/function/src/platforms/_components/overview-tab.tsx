"use client";

// 概览页签:平台自身的信息 + 统计卡。
//
// 统计数字来自后端聚合(target_count/asset_count/file_count/reward_count),
// 前端不逐个拉列表去数——那样每进一次详情页就要发四个请求,且数字与列表分页语义不同。

import * as React from "react";

import { AwardIcon, FileTextIcon, LayersIcon, TargetIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import type { SRCPlatform } from "@/lib/types";

import { EnabledBadge, Field, fmtTime } from "./platform-shared";

// StatCard 单个统计卡:数字 + 说明。四个卡复用同一实现,避免列宽/字号漂移。
function StatCard({ icon, label, value }: { icon: React.ReactNode; label: string; value: number }) {
  return (
    <Card>
      <CardContent className="flex items-center gap-3 pt-4">
        <span className="text-muted-foreground shrink-0">{icon}</span>
        <div className="min-w-0">
          <p className="text-muted-foreground text-xs">{label}</p>
          <p className="font-semibold text-2xl tabular-nums">{value}</p>
        </div>
      </CardContent>
    </Card>
  );
}

export function OverviewTab({
  platform,
  form,
  set,
  onSave,
  saving,
}: {
  platform: SRCPlatform;
  form: Partial<SRCPlatform>;
  set: (patch: Partial<SRCPlatform>) => void;
  onSave: () => void;
  saving: boolean;
}) {
  return (
    <div className="space-y-4">
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard icon={<TargetIcon className="size-5" />} label="目标数" value={platform.target_count ?? 0} />
        <StatCard icon={<LayersIcon className="size-5" />} label="资产数" value={platform.asset_count ?? 0} />
        <StatCard icon={<FileTextIcon className="size-5" />} label="规范文件数" value={platform.file_count ?? 0} />
        <StatCard icon={<AwardIcon className="size-5" />} label="成果数" value={platform.reward_count ?? 0} />
      </div>

      <Card>
        <CardHeader className="flex-row items-center justify-between">
          <CardTitle className="text-sm">平台信息</CardTitle>
          <EnabledBadge enabled={platform.enabled} />
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="平台名称">
              <Input value={form.name ?? ""} onChange={(e) => set({ name: e.target.value })} />
            </Field>
            <Field label="主页">
              <Input
                value={form.homepage ?? ""}
                onChange={(e) => set({ homepage: e.target.value })}
                placeholder="https://www.butian.net"
                className="font-mono"
              />
            </Field>
          </div>

          <Field label="描述">
            <Input
              value={form.description ?? ""}
              onChange={(e) => set({ description: e.target.value })}
              placeholder="该平台的定位、收录范围与提交方式"
            />
          </Field>

          {/* Switch 渲染成 button,<label> 关联不到它(可访问名由 aria-label 提供),故用 div 排版。 */}
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

          <div className="text-muted-foreground flex flex-wrap items-center gap-x-4 gap-y-1 text-xs">
            <span>创建于 {fmtTime(platform.created_at)}</span>
            <span>更新于 {fmtTime(platform.updated_at)}</span>
          </div>

          <div className="flex justify-end">
            <Button size="sm" disabled={saving} onClick={onSave}>
              {saving ? "保存中…" : "保存平台信息"}
            </Button>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
