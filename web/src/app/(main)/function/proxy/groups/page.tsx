"use client";

// 代理中心 · 代理池:代理组的列表页。
//
// 代理组是调度的单位——出网模式切到「代理池」时,agent 的流量走某一个组,
// 组内的代理按该组的调度策略轮换。所以这一页只做「组」的增删改查与检索,
// 组内的代理清单、接入源与测活批次都收在详情页(点组名进入)。

import * as React from "react";

import Link from "next/link";

import {
  ArrowRightIcon,
  ChevronDownIcon,
  ChevronUpIcon,
  PencilIcon,
  PlusIcon,
  SearchIcon,
  ShieldAlertIcon,
  Trash2Icon,
} from "lucide-react";
import { toast } from "sonner";

import { groupKindBadgeClass, groupKindLabel, ProxyGroupDialog, strategyLabel } from "@/components/proxy-dialogs";
import { TablePagination } from "@/components/table-pagination";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { ProxyGroup } from "@/lib/types";

const PAGE_SIZE = 50;

function fmtTime(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

export default function ProxyGroupsPage() {
  const [groups, setGroups] = React.useState<ProxyGroup[]>([]);
  const [total, setTotal] = React.useState(0);
  const [counts, setCounts] = React.useState<Record<string, number>>({});
  const [query, setQuery] = React.useState("");
  const [kind, setKind] = React.useState("all");
  const [sort, setSort] = React.useState("updated");
  const [sortDesc, setSortDesc] = React.useState(true);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);
  const [loading, setLoading] = React.useState(true);
  const [editing, setEditing] = React.useState<ProxyGroup | null>(null);
  const [creating, setCreating] = React.useState(false);

  const load = React.useCallback((q: string, k: string, srt: string, desc: boolean, p: number, size: number) => {
    setLoading(true);
    api
      .proxyGroups({
        kind: k === "all" ? "" : k,
        q,
        sort: srt,
        desc,
        limit: size,
        offset: (p - 1) * size,
      })
      .then((r) => {
        setGroups(r.groups ?? []);
        setCounts(r.counts ?? {});
        setTotal(r.total);
      })
      .catch((e) => toast.error(`加载代理组失败:${(e as Error).message}`))
      .finally(() => setLoading(false));
  }, []);

  React.useEffect(() => {
    load(query, kind, sort, sortDesc, page, pageSize);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [kind, sort, sortDesc, page, pageSize]);

  const search = () => {
    setPage(1);
    load(query, kind, sort, sortDesc, 1, pageSize);
  };
  const reload = () => load(query, kind, sort, sortDesc, page, pageSize);

  const remove = async (g: ProxyGroup) => {
    if (!window.confirm(`删除代理组「${g.name}」?组内代理与接入配置一并删除,不可恢复。`)) return;
    try {
      await api.deleteProxyGroup(g.id);
      toast.success("已删除");
      reload();
    } catch (e) {
      toast.error(`删除失败:${(e as Error).message}`);
    }
  };

  const SortHead = ({ field, width, children }: { field: string; width?: string; children: React.ReactNode }) => (
    <TableHead className={width}>
      <button
        type="button"
        onClick={() => {
          setPage(1);
          if (sort === field) setSortDesc((d) => !d);
          else {
            setSort(field);
            setSortDesc(field !== "name");
          }
        }}
        className="hover:text-foreground inline-flex items-center gap-1"
      >
        {children}
        {sort === field ? (
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
          <h1 className="font-semibold text-xl">代理中心 · 代理池</h1>
          <p className="text-muted-foreground text-sm">
            代理组是调度的单位:出网走代理池时按组内策略轮换使用。进入组可接入主流代理池 API、搜集全网在野代理并跑测活。
          </p>
        </div>
        <Button size="sm" onClick={() => setCreating(true)}>
          <PlusIcon className="size-4" />
          新建代理组
        </Button>
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
                placeholder="搜索组名 / 备注"
                className="pl-8"
              />
            </div>
            <Button variant="outline" size="sm" onClick={search}>
              搜索
            </Button>
            <Select
              value={kind}
              onValueChange={(v) => {
                setPage(1);
                setKind(v);
              }}
            >
              <SelectTrigger className="w-32">
                <SelectValue placeholder="组类型" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="all">全部类型</SelectItem>
                  <SelectItem value="manual">手工</SelectItem>
                  <SelectItem value="api">API 接入</SelectItem>
                  <SelectItem value="wild">在野搜集</SelectItem>
                  <SelectItem value="mixed">混合</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
            <div className="flex items-center gap-2 text-xs">
              {["manual", "api", "wild", "mixed"].map((k) => (
                <Badge key={k} variant="outline" className={groupKindBadgeClass(k)}>
                  {groupKindLabel(k)} {counts?.[k] ?? 0}
                </Badge>
              ))}
            </div>
            <span className="text-muted-foreground ml-auto text-xs tabular-nums">
              {query ? `命中 ${total} / ` : ""}共 {total} 个组
            </span>
          </div>

          {loading && groups.length === 0 ? (
            <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
          ) : groups.length === 0 ? (
            <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
              <ShieldAlertIcon className="size-8 opacity-40" />
              <span>
                {query
                  ? "没有匹配的代理组"
                  : "还没有代理组。点「新建代理组」创建,进入组后可接入代理池 API、搜集全网在野代理并自动测活。"}
              </span>
            </div>
          ) : (
            <>
              <Table className="table-fixed">
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    {/* 组名列不设宽度:table-fixed 下自动吃掉余量(设 w-full 会挤掉后面的列)。
                        其余列的固定宽度之和必须明显小于容器宽,否则组名会被压到看不见
                        (实测 992px 固定宽 + ~897px 容器 → 组名列归零、表还撑出横向滚动条)。
                        故列宽按最小可读值给:「存活 / 失效」用 w-24 也放得下「87 / 4」。 */}
                    <SortHead field="name">组名</SortHead>
                    <TableHead className="w-20">类型</TableHead>
                    <TableHead className="w-24">调度策略</TableHead>
                    <SortHead field="count" width="w-20">
                      代理数
                    </SortHead>
                    <TableHead className="w-24">存活 / 失效</TableHead>
                    <TableHead className="w-16">接入源</TableHead>
                    <TableHead className="w-20">监听端口</TableHead>
                    <TableHead className="w-16">状态</TableHead>
                    <SortHead field="updated" width="w-36">
                      更新时间
                    </SortHead>
                    <TableHead className="w-16">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {(groups ?? []).map((g) => (
                    <TableRow key={g.id}>
                      {/* 组名即入口:点击进入详情页,在那里管理代理、接入源与测活。 */}
                      <TableCell className="min-w-0">
                        <Link
                          href={`/function/proxy/detail?id=${g.id}`}
                          className="block w-full truncate text-left font-medium hover:underline"
                          title={g.name}
                        >
                          {g.name}
                        </Link>
                      </TableCell>
                      <TableCell>
                        <Badge variant="outline" className={groupKindBadgeClass(g.kind)}>
                          {groupKindLabel(g.kind)}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs">{strategyLabel(g.strategy)}</TableCell>
                      <TableCell className="tabular-nums">
                        <div className="flex flex-col">
                          <span>{g.proxy_count ?? 0}</span>
                          {g.alive_count > 0 && <span className="text-emerald-600 text-xs">存活 {g.alive_count}</span>}
                        </div>
                      </TableCell>
                      <TableCell className="tabular-nums">
                        <span className="text-emerald-600">{g.alive_count ?? 0}</span>
                        <span className="text-muted-foreground"> / </span>
                        <span className={g.dead_count > 0 ? "text-red-600" : "text-muted-foreground"}>
                          {g.dead_count ?? 0}
                        </span>
                      </TableCell>
                      <TableCell className="tabular-nums">{g.source_count ?? 0}</TableCell>
                      <TableCell className="font-mono text-xs">{g.listen_port ? g.listen_port : "自动"}</TableCell>
                      <TableCell>
                        <Badge
                          variant="outline"
                          className={
                            g.enabled
                              ? "bg-emerald-500/15 text-emerald-600 border-emerald-500/30"
                              : "bg-muted text-muted-foreground"
                          }
                        >
                          {g.enabled ? "启用" : "停用"}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs">{fmtTime(g.updated_at)}</TableCell>
                      <TableCell>
                        <div className="flex items-center gap-0.5">
                          <Button variant="ghost" size="icon-xs" onClick={() => setEditing(g)} title="编辑">
                            <PencilIcon className="size-3.5" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon-xs"
                            onClick={() => void remove(g)}
                            title="删除(组内代理与接入配置一并删除)"
                          >
                            <Trash2Icon className="text-muted-foreground size-3.5" />
                          </Button>
                          <Button variant="ghost" size="icon-xs" asChild title="进入代理组">
                            <Link href={`/function/proxy/detail?id=${g.id}`} aria-label="进入代理组">
                              <ArrowRightIcon className="size-3.5" />
                            </Link>
                          </Button>
                        </div>
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

      <ProxyGroupDialog
        group={editing}
        open={creating || !!editing}
        onOpenChange={(o) => {
          if (!o) {
            setCreating(false);
            setEditing(null);
          }
        }}
        onSaved={() => {
          setCreating(false);
          setEditing(null);
          reload();
        }}
      />
    </div>
  );
}
