"use client";

// 自进化(契约 AI_MEMORY_CONTRACTS.md §9):经验洞察库 + 攻击剧本库。
// 顶部说明条讲清分级信任:candidate 不注入任务;≥2 次独立印证或人工转正后生效;
// 可随时否决/退役。概览卡随变更由页签回报刷新;页签 ?tab= 存 URL(初始化读
// location.search、切换 replaceState 回写,静态导出下不依赖 useSearchParams/Suspense)。

import * as React from "react";

import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { EvolveOverview, EvolveStatusCounts } from "@/lib/types";

import { InsightsTab } from "./_components/insights-tab";
import { PlaybooksTab } from "./_components/playbooks-tab";

// 概览统计卡:生效数为主值,待验证数作副行(对齐记忆库概览卡形态)。
function StatCard({ label, value, sub }: { label: string; value: React.ReactNode; sub?: string }) {
  return (
    <Card className="gap-1.5">
      <CardHeader className="pb-0">
        <CardDescription>{label}</CardDescription>
        <CardTitle className="text-2xl tabular-nums">{value}</CardTitle>
      </CardHeader>
      {sub && <CardContent className="text-muted-foreground text-xs">{sub}</CardContent>}
    </Card>
  );
}

// 页签胶囊按钮:选中实底、未选描边,带可选计数,aria-pressed 标识。
function PillTab({
  active,
  onClick,
  count,
  children,
}: {
  active: boolean;
  onClick: () => void;
  count?: number;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={`inline-flex items-center gap-1.5 rounded-full border px-3 py-1 text-xs transition-colors ${
        active
          ? "border-primary bg-primary text-primary-foreground"
          : "text-muted-foreground hover:bg-muted border-input"
      }`}
    >
      {children}
      {count !== undefined && <span className="tabular-nums opacity-80">{count}</span>}
    </button>
  );
}

// Record<EvolveStatus, number> 求和(计数兜底 0,缺键按 0 处理)。
function totalOf(counts: EvolveStatusCounts | null | undefined): number | undefined {
  if (!counts) return undefined;
  return (counts.candidate ?? 0) + (counts.active ?? 0) + (counts.rejected ?? 0) + (counts.retired ?? 0);
}

export default function EvolvePage() {
  // 页签:URL ?tab= 初始化(静态导出下直读 location.search)。
  const [tab, setTab] = React.useState<"insights" | "playbooks">(() => {
    if (typeof window !== "undefined") {
      const t = new URLSearchParams(window.location.search).get("tab");
      if (t === "insights" || t === "playbooks") return t;
    }
    return "insights";
  });
  const switchTab = (t: "insights" | "playbooks") => {
    setTab(t);
    try {
      const u = new URL(window.location.href);
      u.searchParams.set("tab", t);
      window.history.replaceState(null, "", u.toString());
    } catch {
      /* 隐私模式等 replaceState 失败不影响页签功能 */
    }
  };

  const [overview, setOverview] = React.useState<EvolveOverview | null>(null);

  // 概览计数:页签内任何变更都会经 onChanged 回报刷新,保持页签计数与列表一致。
  const loadOverview = React.useCallback(() => {
    api
      .evolveOverview()
      .then(setOverview)
      .catch(() => setOverview(null));
  }, []);

  React.useEffect(() => {
    loadOverview();
  }, [loadOverview]);

  const insightCounts = overview?.insights ?? null;
  const playbookCounts = overview?.playbooks ?? null;

  return (
    <div className="p-4 md:p-6">
      {/* 顶部:标题 + 分级信任说明条。 */}
      <div className="mb-4">
        <h1 className="font-semibold text-xl">自进化</h1>
        <p className="text-muted-foreground text-sm">任务复盘沉淀的经验洞察与攻击剧本:印证越多越可信,越可信越优先注入任务</p>
      </div>
      <div className="text-muted-foreground mb-4 flex items-start gap-2 rounded-lg border bg-muted/40 px-4 py-2.5 text-xs">
        <span className="bg-primary/10 text-primary shrink-0 rounded px-1.5 py-0.5 font-medium">分级信任</span>
        <p>
          待验证(candidate)的经验不注入任务;同一经验被 ≥2
          次独立任务印证、或人工「转正」后才会生效注入;可随时否决/退役,退役后立即停止注入。
        </p>
      </div>

      {/* 概览卡:洞察/剧本的生效与待验证计数。 */}
      <div className="mb-4 grid grid-cols-2 gap-3 lg:grid-cols-4">
        <StatCard
          label="洞察 · 生效"
          value={overview?.insights.active ?? 0}
          sub={`待验证 ${overview?.insights.candidate ?? 0}`}
        />
        <StatCard
          label="洞察 · 待验证"
          value={overview?.insights.candidate ?? 0}
          sub={`生效 ${overview?.insights.active ?? 0}`}
        />
        <StatCard
          label="剧本 · 生效"
          value={overview?.playbooks.active ?? 0}
          sub={`待验证 ${overview?.playbooks.candidate ?? 0}`}
        />
        <StatCard
          label="剧本 · 待验证"
          value={overview?.playbooks.candidate ?? 0}
          sub={`生效 ${overview?.playbooks.active ?? 0}`}
        />
      </div>

      {/* 主页签:洞察 / 剧本(可点,带计数;状态存 URL)。 */}
      <div className="mb-3 flex items-center gap-2">
        <PillTab active={tab === "insights"} onClick={() => switchTab("insights")} count={totalOf(insightCounts)}>
          经验洞察
        </PillTab>
        <PillTab active={tab === "playbooks"} onClick={() => switchTab("playbooks")} count={totalOf(playbookCounts)}>
          攻击剧本
        </PillTab>
      </div>

      {tab === "insights" ? (
        <InsightsTab key="insights" counts={insightCounts} onChanged={loadOverview} />
      ) : (
        <PlaybooksTab key="playbooks" counts={playbookCounts} onChanged={loadOverview} />
      )}
    </div>
  );
}
