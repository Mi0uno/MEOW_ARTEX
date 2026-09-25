"use client";

// 漏洞库:0day 与 Nday —— 人工沉淀的漏洞情报与利用指引。
//
// 与「CVE 情报」页分离(2026-09-24):CVE 是机器持续灌入的大批量情报(数千条),
// 重检索与同步状态;0day/Nday 是人工/实测沉淀的少量高价值条目,重编辑与利用指引。
// 混在一页会让页签失去意义,故 CVE 独立成页。

import * as React from "react";

import {
  ChevronDownIcon,
  ChevronUpIcon,
  PencilIcon,
  PlusIcon,
  SearchIcon,
  ShieldAlertIcon,
  SparklesIcon,
  Trash2Icon,
} from "lucide-react";
import { toast } from "sonner";

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { TablePagination } from "@/components/table-pagination";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { categoryBadgeClass, severityBadgeClass, VulnDetailDialog, VulnEntryDialog } from "@/components/vulndb-dialogs";
import { api } from "@/lib/api";
import type { VulnEntry } from "@/lib/types";
import { cn } from "@/lib/utils";

const PAGE_SIZE = 50;

// CATEGORY_TABS 是分类快捷切换的页签:全部 / 0day / Nday。
// CVE 不在此列——它有独立页面(/function/vulndb/cve),这里只做 0day/Nday 的筛选。
const CATEGORY_TABS = [
  { key: "all", label: "全部" },
  { key: "0day", label: "0day" },
  { key: "nday", label: "Nday" },
] as const;

function fmtTime(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

export default function VulnDBPage() {
  return (
    <React.Suspense fallback={null}>
      <VulnDBInner />
    </React.Suspense>
  );
}

function VulnDBInner() {
  const [entries, setEntries] = React.useState<VulnEntry[]>([]);
  const [total, setTotal] = React.useState(0);
  const [counts, setCounts] = React.useState<Record<string, number>>({});
  const [query, setQuery] = React.useState("");
  // category 是分类快捷切换:all(0day+Nday) / 0day / nday。
  const [category, setCategory] = React.useState("all");
  const [severity, setSeverity] = React.useState("all");
  const [source, setSource] = React.useState("all");
  const [hasPoc, setHasPoc] = React.useState("all");
  const [sort, setSort] = React.useState("updated");
  const [sortDesc, setSortDesc] = React.useState(true);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);
  const [loading, setLoading] = React.useState(true);
  const [editing, setEditing] = React.useState<VulnEntry | null>(null);
  const [creating, setCreating] = React.useState(false);
  const [detailID, setDetailID] = React.useState<number | null>(null);

  // 行多选(选中集跨筛选/翻页保留):全选只作用于当前页;批量执行时已不存在的
  // id 自然失败并计入失败数,无需专门收敛。
  const [checkedIds, setCheckedIds] = React.useState<Set<number>>(new Set());
  const [deleting, setDeleting] = React.useState(false);
  const [confirmOpen, setConfirmOpen] = React.useState(false);

  const toggleChecked = (id: number, checked: boolean) => {
    setCheckedIds((prev) => {
      const next = new Set(prev);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  };

  const allSelected = entries.length > 0 && entries.every((e) => checkedIds.has(e.id));
  const someSelected = !allSelected && entries.some((e) => checkedIds.has(e.id));

  const load = React.useCallback(
    (
      cat: string,
      q: string,
      sev: string,
      src: string,
      poc: string,
      srt: string,
      desc: boolean,
      p: number,
      size: number,
    ) => {
      setLoading(true);
      api
        .vulnEntries({
          // 分类快捷切换:选了具体分类就只取该类,否则取 0day + Nday 两类
          // (服务端过滤,分页按过滤后集合算)。CVE 有自己的页面,这里永不取 cve。
          categories: cat === "all" ? ["0day", "nday"] : [cat],
          q,
          severity: sev === "all" ? "" : sev,
          source: src === "all" ? "" : src,
          hasPoc: poc === "all" ? "" : poc,
          sort: srt,
          limit: size,
          offset: (p - 1) * size,
        })
        .then((r) => {
          setEntries(r.entries);
          setCounts(r.counts);
          setTotal(r.total);
        })
        .catch((e) => toast.error(`加载漏洞库失败:${(e as Error).message}`))
        .finally(() => setLoading(false));
    },
    [],
  );

  React.useEffect(() => {
    load(category, query, severity, source, hasPoc, sort, sortDesc, page, pageSize);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [category, severity, source, hasPoc, sort, sortDesc, page, pageSize]);

  const search = () => {
    setPage(1);
    load(category, query, severity, source, hasPoc, sort, sortDesc, 1, pageSize);
  };
  const reload = () => load(category, query, severity, source, hasPoc, sort, sortDesc, page, pageSize);

  const remove = async (e: VulnEntry) => {
    if (!window.confirm(`删除漏洞条目「${e.title}」?其下 PoC 一并删除,不可恢复。`)) return;
    try {
      await api.deleteVulnEntry(e.id);
      toast.success("已删除");
      reload();
    } catch (err) {
      toast.error(`删除失败:${(err as Error).message}`);
    }
  };

  // 批量删除已选条目:并行逐个调删除接口(连带删各自 PoC),统计成功/失败后
  // 清空选择并用页面既有的 reload() 刷新。
  const batchDelete = async () => {
    const ids = [...checkedIds];
    setDeleting(true);
    try {
      const rs = await Promise.allSettled(ids.map((id) => api.deleteVulnEntry(id)));
      const ok = rs.filter((r) => r.status === "fulfilled").length;
      const fail = rs.length - ok;
      if (fail === 0) toast.success(`已删除 ${ok} 项`);
      else toast.warning(`成功 ${ok} 项，失败 ${fail} 项`);
      setCheckedIds(new Set());
      setConfirmOpen(false);
      reload();
    } finally {
      setDeleting(false);
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
            setSortDesc(field !== "created");
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
          <h1 className="font-semibold text-xl">漏洞库</h1>
          <p className="text-muted-foreground text-sm">
            0day 与 Nday 漏洞情报、利用指引与 PoC;CVE 情报已独立成页,由后台定时搜集
          </p>
        </div>
        <Button size="sm" onClick={() => setCreating(true)}>
          <PlusIcon className="size-4" />
          新增漏洞
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
                placeholder="搜索标题 / 厂商 / 产品 / 描述"
                className="pl-8"
              />
            </div>
            <Button variant="outline" size="sm" onClick={search}>
              搜索
            </Button>
            <Select
              value={severity}
              onValueChange={(v) => {
                setPage(1);
                setSeverity(v);
              }}
            >
              <SelectTrigger className="w-32">
                <SelectValue placeholder="严重度" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="all">全部严重度</SelectItem>
                  <SelectItem value="critical">严重</SelectItem>
                  <SelectItem value="high">高</SelectItem>
                  <SelectItem value="medium">中</SelectItem>
                  <SelectItem value="low">低</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
            <Select
              value={source}
              onValueChange={(v) => {
                setPage(1);
                setSource(v);
              }}
            >
              <SelectTrigger className="w-36">
                <SelectValue placeholder="来源" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="all">全部来源</SelectItem>
                  <SelectItem value="ai_finding">实测成果</SelectItem>
                  <SelectItem value="manual">手工录入</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
            <Select
              value={hasPoc}
              onValueChange={(v) => {
                setPage(1);
                setHasPoc(v);
              }}
            >
              <SelectTrigger className="w-32">
                <SelectValue placeholder="PoC" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="all">全部</SelectItem>
                  <SelectItem value="1">有 PoC</SelectItem>
                  <SelectItem value="0">无 PoC</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
            {/* 分类快捷切换:带计数的页签,点了立即按类过滤(0day/Nday 是最常用的两个维度,
                做成页签比塞进下拉更直观)。计数来自后端 counts,不受当前分类筛选影响。 */}
            <div className="flex items-center gap-1 rounded-lg border border-border p-0.5">
              {CATEGORY_TABS.map((t) => {
                const n = t.key === "all" ? (counts["0day"] ?? 0) + (counts.nday ?? 0) : (counts[t.key] ?? 0);
                const active = category === t.key;
                return (
                  <button
                    key={t.key}
                    type="button"
                    onClick={() => {
                      setPage(1);
                      setCategory(t.key);
                    }}
                    aria-pressed={active}
                    className={cn(
                      "rounded-md px-2.5 py-1 text-xs font-medium transition-colors",
                      active
                        ? "bg-foreground text-background"
                        : "text-muted-foreground hover:bg-muted hover:text-foreground",
                    )}
                  >
                    {t.label} <span className="tabular-nums opacity-80">{n}</span>
                  </button>
                );
              })}
            </div>
            {checkedIds.size > 0 && (
              <Button variant="destructive" size="sm" disabled={deleting} onClick={() => setConfirmOpen(true)}>
                <Trash2Icon className="size-4" />
                删除已选 ({checkedIds.size})
              </Button>
            )}
            <span className="text-muted-foreground ml-auto text-xs tabular-nums">
              {query ? `命中 ${total} / ` : ""}共 {total} 条
            </span>
          </div>

          {loading && entries.length === 0 ? (
            <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
          ) : entries.length === 0 ? (
            <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
              <ShieldAlertIcon className="size-8 opacity-40" />
              <span>
                {query
                  ? "没有匹配的漏洞"
                  : "还没有 0day / Nday 条目。点「新增漏洞」录入,或由渗透任务实测成果自动入库。"}
              </span>
            </div>
          ) : (
            <>
              <Table className="table-fixed">
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    <TableHead className="w-8">
                      <Checkbox
                        checked={allSelected ? true : someSelected ? "indeterminate" : false}
                        onCheckedChange={(v) =>
                          setCheckedIds((prev) => {
                            const next = new Set(prev);
                            for (const e of entries) {
                              if (v === true) next.add(e.id);
                              else next.delete(e.id);
                            }
                            return next;
                          })
                        }
                        aria-label="全选本页"
                      />
                    </TableHead>
                    <TableHead className="w-20">分类</TableHead>
                    {/* 标题列不设宽度:table-fixed 下自动吃掉余量(设 w-full 会挤掉后面的列)。 */}
                    <TableHead>标题</TableHead>
                    <TableHead className="w-32">厂商 / 产品</TableHead>
                    <TableHead className="w-20">严重度</TableHead>
                    <SortHead field="poc" width="w-14">
                      PoC
                    </SortHead>
                    <TableHead className="w-20">来源</TableHead>
                    <SortHead field="updated" width="w-40">
                      更新时间
                    </SortHead>
                    <TableHead className="w-24">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {entries.map((e) => (
                    <TableRow key={e.id}>
                      <TableCell className="w-8" onClick={(e2) => e2.stopPropagation()}>
                        <Checkbox
                          checked={checkedIds.has(e.id)}
                          onCheckedChange={(v) => toggleChecked(e.id, v === true)}
                          aria-label={`选择 ${e.title}`}
                        />
                      </TableCell>
                      <TableCell>
                        <Badge variant="outline" className={categoryBadgeClass(e.category)}>
                          {e.category === "0day" ? "0day" : "Nday"}
                        </Badge>
                      </TableCell>
                      {/* 标题列:table-fixed + truncate 保证长标题不溢出到相邻列 */}
                      <TableCell className="min-w-0">
                        <button
                          type="button"
                          onClick={() => setDetailID(e.id)}
                          className="block w-full truncate text-left font-medium hover:underline"
                          title={e.title}
                        >
                          {e.title}
                        </button>
                      </TableCell>
                      <TableCell
                        className="text-muted-foreground truncate text-xs"
                        title={[e.vendor, e.product].filter(Boolean).join(" / ")}
                      >
                        {[e.vendor, e.product].filter(Boolean).join(" / ") || "—"}
                      </TableCell>
                      <TableCell>
                        {e.severity ? (
                          <Badge variant="outline" className={severityBadgeClass(e.severity)}>
                            {e.severity}
                          </Badge>
                        ) : (
                          "—"
                        )}
                      </TableCell>
                      <TableCell className="tabular-nums">
                        {e.poc_count > 0 ? (
                          <span className="text-emerald-600">{e.poc_count}</span>
                        ) : (
                          <span className="text-muted-foreground">0</span>
                        )}
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs">
                        {e.source === "ai_finding" ? (
                          <span className="inline-flex items-center gap-1 text-purple-600">
                            <SparklesIcon className="size-3" />
                            实测
                          </span>
                        ) : (
                          "手工"
                        )}
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs">{fmtTime(e.updated_at)}</TableCell>
                      <TableCell>
                        <div className="flex items-center gap-1">
                          <Button variant="ghost" size="sm" onClick={() => setEditing(e)} title="编辑">
                            <PencilIcon className="size-3.5" />
                          </Button>
                          <Button variant="ghost" size="sm" onClick={() => void remove(e)} title="删除">
                            <Trash2Icon className="text-muted-foreground size-3.5" />
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

      {/* 批量删除确认:兄弟节点,不进上方三元链。 */}
      <AlertDialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>确认删除</AlertDialogTitle>
            <AlertDialogDescription>
              将删除已选的 {checkedIds.size} 条漏洞条目(含其 PoC),该操作不可恢复。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleting}>取消</AlertDialogCancel>
            <AlertDialogAction
              disabled={deleting}
              onClick={(e) => {
                e.preventDefault(); // 删除完成后再关弹层,期间确认按钮呈禁用态
                void batchDelete();
              }}
            >
              {deleting ? "删除中…" : "删除"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <VulnEntryDialog
        entry={editing}
        defaultCategory="nday"
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
      <VulnDetailDialog
        entryID={detailID}
        onOpenChange={(o) => !o && setDetailID(null)}
        onChanged={reload}
        onEdit={(e) => {
          setDetailID(null);
          setEditing(e);
        }}
      />
    </div>
  );
}
