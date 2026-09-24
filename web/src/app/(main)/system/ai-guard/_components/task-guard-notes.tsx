"use client";

// AI监管·任务级门限说明
//
// 任务可以覆盖全局监管姿态(tasks.guard_mode / guard_rule_ids)。配置入口在任务侧
// (任务详情),AI监管页只负责说明语义与指路——这里刻意不做任务选择器:同一个开关
// 出现在两处会让"改的是哪个任务"变得不确定,任务级配置必须贴着任务本身改。

import Link from "next/link";

import { ArrowRightIcon, LayersIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Separator } from "@/components/ui/separator";

// 三档任务级监管姿态。值与 db/schema.sql 的 tasks_guard_mode_check 一致。
const MODES: Array<{ value: string; label: string; tone: string; detail: string }> = [
  {
    value: "跟随全局",
    label: "跟随全局",
    tone: "text-muted-foreground",
    detail: "不覆盖，完全按本页的全局配置执行（默认）。",
  },
  {
    value: "off",
    label: "本任务不跑监管",
    tone: "text-destructive",
    detail: "该任务的工具调用不进入拦截规则、决策引擎与监管模型，全部直接放行。",
  },
  {
    value: "enforce",
    label: "强制启用监管",
    tone: "text-emerald-600 dark:text-emerald-400",
    detail: "显式声明本任务必须受监管，用于对监管姿态有硬要求的任务。",
  },
];

export function TaskGuardNotes() {
  return (
    <Card>
      <CardContent className="space-y-3 p-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-2">
            <LayersIcon className="h-4 w-4 text-muted-foreground" />
            <p className="font-medium text-sm">任务级门限（覆盖全局）</p>
          </div>
          <Button variant="ghost" size="sm" className="h-7 text-xs" asChild>
            <Link href="/function/tasks">
              前往任务列表
              <ArrowRightIcon className="h-3.5 w-3.5" />
            </Link>
          </Button>
        </div>

        <p className="text-muted-foreground text-xs leading-relaxed">
          本页配置是<span className="font-medium text-foreground">全局默认</span>。单个任务可以覆盖它，
          覆盖配置在任务详情页的监管设置里修改 —— 每个任务的门限贴着任务本身设置，避免在这里改错对象。
        </p>

        <div className="grid gap-2 md:grid-cols-3">
          {MODES.map((m) => (
            <div key={m.value} className="rounded-md border p-3">
              <Badge variant="outline" className={`px-1.5 py-0 text-[10px] ${m.tone}`}>
                {m.label}
              </Badge>
              <p className="mt-1.5 text-muted-foreground text-xs leading-snug">{m.detail}</p>
            </div>
          ))}
        </div>

        <Separator />

        <div className="space-y-1.5">
          <p className="font-semibold text-[10px] text-muted-foreground uppercase tracking-wider">规则集限定</p>
          <p className="text-muted-foreground text-xs leading-relaxed">
            任务还可以指定<span className="font-medium text-foreground">只用哪几条拦截规则</span>：留空表示用
            本页启用中的全部规则；指定后，该任务只按这些规则匹配（未选中的规则对该任务等同于不存在）。
            规则集限定只作用于规则这一层，决策引擎与监管模型照常参与。
          </p>
        </div>

        <p className="text-[10px] text-muted-foreground leading-relaxed">
          生效范围：任务级配置对<span className="font-medium">任务自身的 agent 工具调用</span>生效；
          在会话（对话）里手动执行的操作不受任务级覆盖影响，仍按全局配置审查。
        </p>
      </CardContent>
    </Card>
  );
}
