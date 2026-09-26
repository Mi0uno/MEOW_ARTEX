"use client";

// 自进化页签共用的小件:状态页签 / 状态徽标 / 归属(scope)选择器 / 企业列表钩子。
// 分级信任的文案与状态语义只在这一处定义,洞察与剧本两个页签共用。

import * as React from "react";

import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { api } from "@/lib/api";
import type { EvolveStatus, EvolveStatusCounts, MemoryCompanyStat } from "@/lib/types";

/** 状态页签的取值:all = 不按状态过滤(哨兵 "all",空串是 Radix 保留值)。 */
export type EvolveTabStatus = "all" | EvolveStatus;

/** 状态页签定义(全部/待验证/生效/已否决/已退役),计数由调用方从 overview 传入。 */
export const STATUS_TABS: { key: EvolveTabStatus; label: string }[] = [
  { key: "all", label: "全部" },
  { key: "candidate", label: "待验证" },
  { key: "active", label: "生效" },
  { key: "rejected", label: "已否决" },
  { key: "retired", label: "已退役" },
];

/** 页签计数:来自 evolveOverview,不受当前列表筛选影响;overview 未就绪时返回 undefined(不显示计数)。 */
export function statusCount(counts: EvolveStatusCounts | null, key: EvolveTabStatus): number | undefined {
  if (!counts) return undefined;
  if (key === "all") return counts.candidate + counts.active + counts.rejected + counts.retired;
  return counts[key];
}

/** 状态 → 中文标签 + 徽标形态(candidate=次要 / active=默认 / rejected=危险 / retired=描边)。 */
export const EVOLVE_STATUS_META: Record<string, { label: string; badge: "secondary" | "default" | "destructive" | "outline" }> = {
  candidate: { label: "待验证", badge: "secondary" },
  active: { label: "生效中", badge: "default" },
  rejected: { label: "已否决", badge: "destructive" },
  retired: { label: "已退役", badge: "outline" },
};

export function EvolveStatusBadge({ status }: { status: string }) {
  const meta = EVOLVE_STATUS_META[status];
  return (
    <Badge variant={meta?.badge ?? "outline"}>{meta?.label ?? status}</Badge>
  );
}

/** 状态操作的中文名(status 接口共用,toast 文案用)。 */
export const STATUS_ACTION_LABELS: Record<EvolveStatus, string> = {
  candidate: "撤销为待验证",
  active: "转正",
  rejected: "否决",
  retired: "退役",
};

/** 页签胶囊按钮:选中实底、未选描边,带可选计数,aria-pressed 标识。 */
export function PillTab({
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

/**
 * 新增洞察/剧本共用的归属选择器:全局 或 指定企业。
 * scope_kind=company 时必须选中一个企业(scope_id > 0,后端强校验)。
 */
export function ScopeFields({
  scopeKind,
  onScopeKind,
  scopeId,
  onScopeId,
  companies,
}: {
  scopeKind: "global" | "company";
  onScopeKind: (v: "global" | "company") => void;
  scopeId: number; // 0 = 未选
  onScopeId: (v: number) => void;
  companies: MemoryCompanyStat[];
}) {
  return (
    <>
      <div className="grid gap-1.5">
        <Label>归属</Label>
        <Select
          value={scopeKind}
          onValueChange={(v) => {
            onScopeKind(v as "global" | "company");
            if (v === "global") onScopeId(0);
          }}
        >
          <SelectTrigger className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              <SelectItem value="global">全局(所有企业共用)</SelectItem>
              <SelectItem value="company">指定企业</SelectItem>
            </SelectGroup>
          </SelectContent>
        </Select>
      </div>
      {scopeKind === "company" && (
        <div className="grid gap-1.5">
          <Label>企业</Label>
          <Select value={scopeId ? String(scopeId) : ""} onValueChange={(v) => onScopeId(Number(v))}>
            <SelectTrigger className="w-full">
              <SelectValue placeholder="选择企业" />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                {companies.map((c) => (
                  <SelectItem key={c.company_id} value={String(c.company_id)}>
                    {c.company_name || `企业 #${c.company_id}`}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
        </div>
      )}
    </>
  );
}

/** 企业列表(归属选择器数据源;memoryCompanies 返回全部有任务/企业的记录)。 */
export function useCompanies(): MemoryCompanyStat[] {
  const [companies, setCompanies] = React.useState<MemoryCompanyStat[]>([]);
  React.useEffect(() => {
    api
      .memoryCompanies()
      .then((r) => setCompanies(r.companies))
      .catch(() => setCompanies([]));
  }, []);
  return companies;
}
