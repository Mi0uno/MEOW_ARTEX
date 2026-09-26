"use client";

// AI 记忆库(契约 AI_MEMORY_CONTRACTS.md §9):跨任务沉淀的记忆事实与任务复盘。
// 头部 = 企业选择器(Radix 空值哨兵 __all__,选项带 runs/facts 计数)
//   + 「AI 自动复盘」开关(乐观更新,失败回滚) + 概览卡;
// 页签 = 事实 / 任务复盘,?tab= 存 URL(初始化读 location.search、切换
// replaceState 回写,SPA 静态导出下不依赖 useSearchParams/Suspense);
// 页签计数来自 overview 接口,不受当前列表筛选影响。

import * as React from "react";

import { toast } from "sonner";

import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import type { MemoryCompanyStat, MemoryOverview } from "@/lib/types";

import { FactsTab } from "./_components/facts-tab";
import { RunsTab } from "./_components/runs-tab";

// 「全部企业」哨兵:value="" 是 Radix Select 保留值,空态一律用非空哨兵串。
const ALL_COMPANIES = "__all__";

// 概览统计卡(对齐任务详情总览页的 StatCard 形态)。
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

// 页签胶囊按钮:选中实底、未选描边,带可选计数,aria-pressed 标识(可点,非只读徽标)。
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

export default function MemoryPage() {
  // 页签:URL ?tab= 初始化(静态导出下直读 location.search,不用 useSearchParams)。
  const [tab, setTab] = React.useState<"facts" | "runs">(() => {
    if (typeof window !== "undefined") {
      const t = new URLSearchParams(window.location.search).get("tab");
      if (t === "facts" || t === "runs") return t;
    }
    return "facts";
  });
  const switchTab = (t: "facts" | "runs") => {
    setTab(t);
    try {
      const u = new URL(window.location.href);
      u.searchParams.set("tab", t);
      window.history.replaceState(null, "", u.toString());
    } catch {
      /* 隐私模式等 replaceState 失败不影响页签功能 */
    }
  };

  // 企业选择器 + 概览 + 开关状态。
  const [companies, setCompanies] = React.useState<MemoryCompanyStat[]>([]);
  const [companyKey, setCompanyKey] = React.useState(ALL_COMPANIES);
  const [overview, setOverview] = React.useState<MemoryOverview | null>(null);
  const [aiReflect, setAiReflect] = React.useState<boolean | null>(null);
  const [reflectBusy, setReflectBusy] = React.useState(false);

  // companyId 0 = 不限企业。两个数据页签以 key={companyId} 重挂载,切换即回到第一页。
  const companyId = companyKey === ALL_COMPANIES ? 0 : Number(companyKey);
  const companyName =
    companyKey === ALL_COMPANIES ? "" : (companies.find((c) => String(c.company_id) === companyKey)?.company_name ?? `企业 #${companyKey}`);

  const loadCompanies = React.useCallback(() => {
    api
      .memoryCompanies()
      .then((r) => setCompanies(r.companies))
      .catch(() => setCompanies([]));
  }, []);

  const loadOverview = React.useCallback(() => {
    api
      .memoryOverview(companyId || undefined)
      .then(setOverview)
      .catch(() => setOverview(null));
  }, [companyId]);

  React.useEffect(() => {
    loadCompanies();
  }, [loadCompanies]);

  React.useEffect(() => {
    loadOverview();
  }, [loadOverview]);

  // AI 自动复盘开关:初始值拉取失败时整个开关不渲染(后端老版本可能没有该接口)。
  React.useEffect(() => {
    api
      .memoryConfig()
      .then((r) => setAiReflect(r.ai_reflect))
      .catch(() => setAiReflect(null));
  }, []);

  const toggleReflect = (v: boolean) => {
    const prev = aiReflect;
    setReflectBusy(true);
    setAiReflect(v); // 乐观更新,失败回滚
    api
      .memorySetConfig({ ai_reflect: v })
      .then(() => toast.success(v ? "已开启 AI 自动复盘:agent 任务终态后自动复盘沉淀经验" : "已关闭 AI 自动复盘"))
      .catch((e) => {
        setAiReflect(prev);
        toast.error(`设置失败:${(e as Error).message}`);
      })
      .finally(() => setReflectBusy(false));
  };

  return (
    <div className="p-4 md:p-6">
      {/* 顶部:标题 + AI 自动复盘开关。 */}
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div>
          <h1 className="font-semibold text-xl">记忆库</h1>
          <p className="text-muted-foreground text-sm">
            跨任务沉淀的记忆事实与任务复盘:新任务自动注入历史记忆,任务终态自动复盘,避免重复探索烧 token
          </p>
        </div>
        {aiReflect !== null && (
          <div className="flex items-center gap-2 rounded-lg border px-3 py-1.5">
            <Switch
              id="memory-ai-reflect"
              size="sm"
              checked={aiReflect}
              disabled={reflectBusy}
              onCheckedChange={toggleReflect}
            />
            <Label htmlFor="memory-ai-reflect" className="text-xs font-normal">
              AI 自动复盘
            </Label>
          </div>
        )}
      </div>

      {/* 企业选择器(选项带各企业记忆计数)。 */}
      <div className="mb-3">
        <Select value={companyKey} onValueChange={setCompanyKey}>
          <SelectTrigger className="w-72" aria-label="选择企业">
            <SelectValue placeholder="选择企业" />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              <SelectItem value={ALL_COMPANIES}>全部企业</SelectItem>
              {companies.map((c) => (
                <SelectItem key={c.company_id} value={String(c.company_id)}>
                  {c.company_name || `企业 #${c.company_id}`}（复盘 {c.runs} · 事实 {c.facts_active}）
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
      </div>

      {/* 概览统计卡:随企业选择联动。 */}
      <div className="mb-4 grid grid-cols-2 gap-3 sm:grid-cols-3">
        <StatCard label="记忆事实(全部)" value={overview?.facts_total ?? 0} />
        <StatCard label="生效事实" value={overview?.facts_active ?? 0} sub="未失效、会注入新任务的事实" />
        <StatCard label="任务复盘" value={overview?.runs_total ?? 0} sub="agent 任务终态后的复盘记录" />
      </div>

      {/* 主页签:事实 / 任务复盘(可点,带计数;状态存 URL)。 */}
      <div className="mb-3 flex items-center gap-2">
        <PillTab active={tab === "facts"} onClick={() => switchTab("facts")} count={overview?.facts_total}>
          事实
        </PillTab>
        <PillTab active={tab === "runs"} onClick={() => switchTab("runs")} count={overview?.runs_total}>
          任务复盘
        </PillTab>
      </div>

      {tab === "facts" ? (
        <FactsTab key={companyId} companyId={companyId} companyName={companyName} onChanged={loadOverview} />
      ) : (
        <RunsTab key={companyId} companyId={companyId} onChanged={loadOverview} />
      )}
    </div>
  );
}
