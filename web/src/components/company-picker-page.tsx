"use client";

// CompanyPickerPage —— 资产主体(企业)选择页:任务页与报告中心的**进入页**。
//
// 为什么用整页选择而不是下拉框:下拉框只能看见当前展开的几项,企业一多就要滚动翻找,
// 也看不到各企业的资产/任务/报告规模——用户明确反馈「下拉选择不稳妥,应该先进入
// 选择资产的页面」。
//
// 关键设计:**顶部两行合成选项「全部资产主体」「未关联企业」**。
// 它们是本页作为进入页的安全阀——用户永远有一个「不筛选、看全部」的出口。
// 2026-09-24 的「任务凭空消失」回归正是因为当时的选择页没有这个出口:
// 历史任务大多没有企业归属,一旦被迫选某家企业,这些数据就全被过滤掉了。
// 所以「选企业」是选择维度,不是数据门禁;合成行保证了这一点在 UI 上成立。
//
// 形态与平台其他列表页一致:搜索 + 筛选器 + 排序 + 分页(服务端分页)。

import * as React from "react";

import { Building2Icon, ChevronDownIcon, ChevronUpIcon, InboxIcon, LayersIcon, SearchIcon } from "lucide-react";
import { toast } from "sonner";

import { TablePagination } from "@/components/table-pagination";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { CompanyWithCounts } from "@/lib/types";
import { cn } from "@/lib/utils";

const PAGE_SIZE_DEFAULT = 20;

// 资产范围类型筛选(与 company_scope.kind 同取值)。
const SCOPE_KINDS: { value: string; label: string }[] = [
  { value: "all", label: "全部范围类型" },
  { value: "domain", label: "域名" },
  { value: "ip", label: "IP" },
  { value: "cidr", label: "网段" },
  { value: "icp", label: "ICP 备案" },
  { value: "keyword", label: "企业关键词" },
];

type SortField = "name" | "assets" | "tasks" | "reports" | "updated";

// CompanyPick 是选择结果。合成选项(all/unassigned)与具体企业都走同一个回调,
// 调用方按 kind 分支即可——不必为「看全部」再单独做一条入口。
export type CompanyPick = { kind: "all" } | { kind: "unassigned" } | { kind: "company"; company: CompanyWithCounts };

// syntheticRows 是顶部合成行(全部 / 未关联企业)。
export interface CompanyPickerSynthetic {
  // 当前选中的是哪个(用于高亮);null = 还没选过(刚进入本页)。
  selected: "all" | "unassigned" | number | null;
  // 「全部」行右侧展示的条数(该页面的总量,如全部任务数)。
  allCount: number;
  // 「未关联企业」行的条数;为 0 时该行禁用(点进去只会是空列表)。
  unassignedCount: number;
  // 计数列的单位文案,如「个任务」「份报告」。
  unit: string;
}

// scopeSummary 把范围规则汇总成「域名 3 · IP 12 · 网段 1」这样的短摘要。
function scopeSummary(c: CompanyWithCounts): string {
  if (!c.scope?.length) return "未录入范围";
  const counts = new Map<string, number>();
  for (const r of c.scope) counts.set(r.kind, (counts.get(r.kind) ?? 0) + 1);
  const label: Record<string, string> = { domain: "域名", ip: "IP", cidr: "网段", icp: "ICP", keyword: "关键词" };
  return [...counts.entries()].map(([k, n]) => `${label[k] ?? k} ${n}`).join(" · ");
}

export function CompanyPickerPage({
  title,
  description,
  onPick,
  onBack,
  emptyHint,
  countLabel,
  synthetic,
}: {
  title: string;
  description: string;
  onPick: (pick: CompanyPick) => void;
  // onBack 非空时显示「返回」按钮(从列表页切过来时用,可原路返回)。
  onBack?: () => void;
  emptyHint: string;
  // countLabel 指定计数列展示的是任务数还是报告数(两者都显示,这个只影响文案)。
  countLabel?: string;
  // synthetic 非空时在表格顶部插入「全部资产主体」「未关联企业」两行。
  // 作为进入页时必须传:它保证用户能选「看全部」,数据不会被筛选藏起来。
  synthetic?: CompanyPickerSynthetic;
}) {
  const [rows, setRows] = React.useState<CompanyWithCounts[]>([]);
  const [total, setTotal] = React.useState(0);
  const [loading, setLoading] = React.useState(true);
  const [query, setQuery] = React.useState("");
  const [scopeKind, setScopeKind] = React.useState("all");
  const [sortField, setSortField] = React.useState<SortField>("reports");
  const [sortDesc, setSortDesc] = React.useState(true);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE_DEFAULT);

  const load = React.useCallback(
    (q: string, kind: string, field: SortField, desc: boolean, p: number, size: number) => {
      setLoading(true);
      api
        .companiesPage({
          q,
          kind: kind === "all" ? "" : kind,
          sort: field,
          desc,
          limit: size,
          offset: (p - 1) * size,
        })
        .then((r) => {
          setRows(r.companies);
          setTotal(r.total);
        })
        .catch((e) => toast.error(`加载企业失败:${(e as Error).message}`))
        .finally(() => setLoading(false));
    },
    [],
  );

  React.useEffect(() => {
    load(query, scopeKind, sortField, sortDesc, page, pageSize);
    // 搜索/筛选/排序/分页变化即重查(query 由搜索按钮提交)。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scopeKind, sortField, sortDesc, page, pageSize]);

  const search = () => {
    setPage(1);
    load(query, scopeKind, sortField, sortDesc, 1, pageSize);
  };

  const toggleSort = (field: SortField) => {
    setPage(1);
    if (sortField === field) {
      setSortDesc((d) => !d);
      return;
    }
    setSortField(field);
    // 名称默认升序,数值列默认降序(看最多的在前)。
    setSortDesc(field !== "name");
  };

  const SortHead = ({
    field,
    children,
    className,
  }: {
    field: SortField;
    children: React.ReactNode;
    className?: string;
  }) => (
    <TableHead className={className}>
      <button
        type="button"
        onClick={() => toggleSort(field)}
        className="hover:text-foreground inline-flex items-center gap-1"
      >
        {children}
        {sortField === field ? (
          sortDesc ? (
            <ChevronDownIcon className="size-3" />
          ) : (
            <ChevronUpIcon className="size-3" />
          )
        ) : null}
      </button>
    </TableHead>
  );

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div>
          <h1 className="font-semibold text-xl">{title}</h1>
          <p className="text-muted-foreground mt-0.5 text-sm">{description}</p>
        </div>
        {onBack && (
          <Button variant="outline" size="sm" onClick={onBack}>
            返回
          </Button>
        )}
      </div>

      <Card>
        <CardContent className="px-0 pt-4">
          <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
            <div className="relative w-full sm:max-w-xs">
              <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
              <Input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && search()}
                placeholder="搜索企业名称"
                className="pl-8"
              />
            </div>
            <Button variant="outline" size="sm" onClick={search}>
              搜索
            </Button>
            <Select
              value={scopeKind}
              onValueChange={(v) => {
                setPage(1);
                setScopeKind(v);
              }}
            >
              <SelectTrigger className="w-40">
                <SelectValue placeholder="范围类型" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  {SCOPE_KINDS.map((k) => (
                    <SelectItem key={k.value} value={k.value}>
                      {k.label}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            <span className="text-muted-foreground ml-auto text-xs tabular-nums">
              共 {total} 家{countLabel ? ` · 按${countLabel}` : ""}
            </span>
          </div>

          {/* 合成行始终可见(不受搜索/分页影响):它们是「看全部」与「看未归属」的固定出口。 */}
          {synthetic && (
            <div className="border-b">
              <button
                type="button"
                onClick={() => onPick({ kind: "all" })}
                className={cn(
                  "hover:bg-muted/50 flex w-full items-center gap-3 px-4 py-3 text-left transition-colors lg:px-6",
                  synthetic.selected === "all" && "bg-muted/60",
                )}
              >
                <LayersIcon className="text-muted-foreground size-4 shrink-0" />
                <span className="font-medium">全部资产主体</span>
                <span className="text-muted-foreground truncate text-xs">
                  不限企业,列出全部数据(含未关联企业的历史产出)
                </span>
                <span className="text-muted-foreground ml-auto shrink-0 text-xs tabular-nums">
                  {synthetic.allCount} {synthetic.unit}
                </span>
              </button>
              <button
                type="button"
                disabled={synthetic.unassignedCount === 0}
                onClick={() => onPick({ kind: "unassigned" })}
                title={synthetic.unassignedCount === 0 ? "没有未关联企业的数据" : "只看未关联任何企业的数据"}
                className={cn(
                  "hover:bg-muted/50 flex w-full items-center gap-3 border-t px-4 py-3 text-left transition-colors lg:px-6",
                  synthetic.selected === "unassigned" && "bg-muted/60",
                  synthetic.unassignedCount === 0 && "cursor-not-allowed opacity-50",
                )}
              >
                <InboxIcon className="text-muted-foreground size-4 shrink-0" />
                <span className="font-medium">未关联企业</span>
                <span className="text-muted-foreground truncate text-xs">历史产出,创建时未挂企业</span>
                <span className="text-muted-foreground ml-auto shrink-0 text-xs tabular-nums">
                  {synthetic.unassignedCount} {synthetic.unit}
                </span>
              </button>
            </div>
          )}

          {loading && rows.length === 0 ? (
            <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
          ) : rows.length === 0 ? (
            <p className="text-muted-foreground py-16 text-center text-sm">{emptyHint}</p>
          ) : (
            <>
              <Table>
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    <SortHead field="name">企业名称</SortHead>
                    <TableHead>资产范围</TableHead>
                    <SortHead field="assets" className="w-24 text-right">
                      资产
                    </SortHead>
                    <SortHead field="tasks" className="w-24 text-right">
                      任务
                    </SortHead>
                    <SortHead field="reports" className="w-24 text-right">
                      报告
                    </SortHead>
                    <SortHead field="updated" className="w-40">
                      更新时间
                    </SortHead>
                    <TableHead className="w-24" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {rows.map((c) => (
                    <TableRow
                      key={c.id}
                      className={cn("cursor-pointer", synthetic?.selected === c.id && "bg-muted/60")}
                      onClick={() => onPick({ kind: "company", company: c })}
                    >
                      <TableCell className="font-medium">
                        <span className="inline-flex items-center gap-2">
                          <Building2Icon className="text-muted-foreground size-4" />
                          {c.name}
                        </span>
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs">{scopeSummary(c)}</TableCell>
                      <TableCell className="text-right tabular-nums">{c.asset_count}</TableCell>
                      <TableCell className="text-right tabular-nums">{c.task_count ?? 0}</TableCell>
                      <TableCell className="text-right tabular-nums">{c.report_count ?? 0}</TableCell>
                      <TableCell className="text-muted-foreground text-xs">
                        {c.updated_at ? new Date(c.updated_at).toLocaleString() : "—"}
                      </TableCell>
                      <TableCell>
                        <Button
                          size="sm"
                          variant="outline"
                          onClick={(e) => {
                            e.stopPropagation();
                            onPick({ kind: "company", company: c });
                          }}
                        >
                          选择
                        </Button>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
              <TablePagination
                page={page}
                pageSize={pageSize}
                total={total}
                onPageChange={setPage}
                onPageSizeChange={(s) => {
                  setPage(1);
                  setPageSize(s);
                }}
              />
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
