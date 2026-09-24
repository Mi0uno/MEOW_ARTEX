"use client";

// 技能库总览：全部指标都由已加载的数据就地聚合（技能列表自带每技能调用统计，
// missing 与列表同批拉取），不额外发请求。
//
// 改造前它是右栏的"空态"；列表页成形后它变成表格上方的补充视图，因此改成可折叠：
// 指标行常驻（一眼看到全局），排行/最近/未使用收进折叠区，避免把表格挤到首屏之外。

import * as React from "react";

import { ChevronDownIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Label } from "@/components/ui/label";
import type { MissingSkill, SkillItem } from "@/lib/types";
import { cn } from "@/lib/utils";

import { fmtTime } from "./format";

export function SkillsOverview({
  skills,
  missing,
  onSelect,
}: {
  skills: SkillItem[];
  missing: MissingSkill[];
  onSelect: (name: string) => void;
}) {
  const [open, setOpen] = React.useState(false);

  const agg = React.useMemo(() => {
    const totalCalls = skills.reduce((n, s) => n + s.calls, 0);
    const used = skills.filter((s) => s.calls > 0);
    const ranked = [...used].sort((a, b) => b.calls - a.calls);
    const neverUsed = skills.filter((s) => s.calls === 0);
    const recent = skills
      .filter((s) => s.last_used)
      .sort((a, b) => ((a.last_used ?? "") < (b.last_used ?? "") ? 1 : -1))
      .slice(0, 6);
    const missingCalls = missing.reduce((n, m) => n + m.calls, 0);
    return {
      totalCalls,
      usedCount: used.length,
      ranked,
      topCalls: ranked[0]?.calls ?? 0,
      neverUsed,
      recent,
      missingCalls,
    };
  }, [skills, missing]);

  const stats: { label: string; value: React.ReactNode; hint?: string }[] = [
    { label: "Skill 总数", value: skills.length, hint: `${agg.usedCount} 个被调用过` },
    { label: "累计调用", value: agg.totalCalls },
    {
      label: "未使用",
      value: agg.neverUsed.length,
      hint: agg.neverUsed.length > 0 ? "从未被任何 agent 加载" : "全部用过",
    },
    {
      label: "未命中调用",
      value: agg.missingCalls,
      hint: missing.length > 0 ? `${missing.length} 个不存在的 skill` : "无",
    },
  ];

  return (
    <Card className="mb-4 gap-0 py-0">
      <Collapsible open={open} onOpenChange={setOpen}>
        <CardContent className="px-0">
          <div className="flex flex-wrap items-center gap-3 px-4 py-3 lg:px-6">
            <div className="grid flex-1 grid-cols-2 gap-3 sm:grid-cols-4">
              {stats.map((s) => (
                <div key={s.label} className="rounded-lg border px-3 py-2">
                  <p className="text-xl font-semibold tabular-nums">{s.value}</p>
                  <p className="text-xs font-medium">{s.label}</p>
                  {s.hint && <p className="text-muted-foreground mt-0.5 text-[11px]">{s.hint}</p>}
                </div>
              ))}
            </div>
            <CollapsibleTrigger asChild>
              <Button variant="ghost" size="sm" className="shrink-0">
                使用明细
                <ChevronDownIcon className={cn("size-3.5 transition-transform", open && "rotate-180")} />
              </Button>
            </CollapsibleTrigger>
          </div>

          <CollapsibleContent>
            <div className="space-y-5 border-t px-4 py-4 lg:px-6">
              {/* 调用排行 */}
              <div className="space-y-2">
                <Label className="text-muted-foreground text-xs">调用排行</Label>
                {agg.ranked.length === 0 ? (
                  <p className="text-muted-foreground rounded-lg border border-dashed p-4 text-center text-xs">
                    还没有任何 Skill 调用记录。
                  </p>
                ) : (
                  <div className="space-y-1.5">
                    {agg.ranked.slice(0, 8).map((s) => (
                      <button
                        key={s.name}
                        type="button"
                        onClick={() => onSelect(s.name)}
                        className="group hover:bg-muted flex w-full items-center gap-3 rounded-md px-2 py-1.5 text-left"
                      >
                        <span className="w-40 shrink-0 truncate font-mono text-xs" title={s.name}>
                          {s.name}
                        </span>
                        <span className="bg-muted relative h-2 flex-1 overflow-hidden rounded-full">
                          <span
                            className="bg-primary/70 absolute inset-y-0 left-0 rounded-full"
                            style={{ width: `${agg.topCalls > 0 ? (s.calls / agg.topCalls) * 100 : 0}%` }}
                          />
                        </span>
                        <span className="text-muted-foreground w-16 shrink-0 text-right text-xs tabular-nums">
                          {s.calls} 次
                        </span>
                      </button>
                    ))}
                  </div>
                )}
              </div>

              <div className="grid gap-6 sm:grid-cols-2">
                {/* 最近调用 */}
                <div className="space-y-2">
                  <Label className="text-muted-foreground text-xs">最近调用</Label>
                  {agg.recent.length === 0 ? (
                    <p className="text-muted-foreground text-xs">暂无记录。</p>
                  ) : (
                    <div className="space-y-1">
                      {agg.recent.map((s) => (
                        <button
                          key={s.name}
                          type="button"
                          onClick={() => onSelect(s.name)}
                          className="hover:bg-muted flex w-full items-center gap-2 rounded px-1.5 py-1 text-left"
                        >
                          <span className="min-w-0 flex-1 truncate font-mono text-xs" title={s.name}>
                            {s.name}
                          </span>
                          <span className="text-muted-foreground shrink-0 text-[11px] tabular-nums">
                            {fmtTime(s.last_used)}
                          </span>
                        </button>
                      ))}
                    </div>
                  )}
                </div>

                {/* 未使用（可清理 / 需曝光） */}
                <div className="space-y-2">
                  <Label className="text-muted-foreground text-xs">
                    未使用的 Skill
                    {agg.neverUsed.length > 0 && <span className="ml-1 font-normal">（{agg.neverUsed.length}）</span>}
                  </Label>
                  {agg.neverUsed.length === 0 ? (
                    <p className="text-muted-foreground text-xs">所有 Skill 都被调用过。</p>
                  ) : (
                    <div className="flex flex-wrap gap-1.5">
                      {agg.neverUsed.map((s) => (
                        <button key={s.name} type="button" onClick={() => onSelect(s.name)} title={s.name}>
                          <Badge
                            variant="outline"
                            className="hover:bg-muted max-w-[12rem] cursor-pointer truncate font-mono text-xs font-normal"
                          >
                            {s.name}
                          </Badge>
                        </button>
                      ))}
                    </div>
                  )}
                </div>
              </div>
            </div>
          </CollapsibleContent>
        </CardContent>
      </Collapsible>
    </Card>
  );
}
