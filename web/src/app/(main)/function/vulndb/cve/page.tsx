"use client";

// CVE 情报:独立页面,只展示 CVE 类漏洞(有编号的公开漏洞)。
// 数据由后台定时采集器从 NVD / GitHub Advisory / Exploit-DB / GitHub PoC 持续拉取。
//
// 与「漏洞库」页分离的原因:CVE 是机器持续灌入的大批量情报(数千条),
// 与人工沉淀的 0day/Nday 在阅读方式与操作习惯上完全不同——前者重检索与同步状态,
// 后者重编辑与利用指引。混在一页会让页签失去意义。

import * as React from "react";

import {
  ChevronDownIcon,
  ChevronUpIcon,
  ExternalLinkIcon,
  PencilIcon,
  PlusIcon,
  RefreshCwIcon,
  SearchIcon,
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
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { TablePagination } from "@/components/table-pagination";
import { api } from "@/lib/api";
import type { VulnEntry, VulnSyncRun } from "@/lib/types";
import { VulnDetailDialog, VulnEntryDialog, severityBadgeClass } from "@/components/vulndb-dialogs";

const PAGE_SIZE = 50;

function fmtTime(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

export default function CVEPage() {
  const [entries, setEntries] = React.useState<VulnEntry[]>([]);
  const [total, setTotal] = React.useState(0);
  const [query, setQuery] = React.useState("");
  const [severity, setSeverity] = React.useState("all");
  const [hasPoc, setHasPoc] = React.useState("all");
  const [sort, setSort] = React.useState("updated");
  const [sortDesc, setSortDesc] = React.useState(true);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);
  const [loading, setLoading] = React.useState(true);
  const [sync, setSync] = React.useState<{ runs: VulnSyncRun[]; enabled: boolean; interval_hours: number } | null>(
    null,
  );
  const [syncing, setSyncing] = React.useState(false);
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
    (q: string, sev: string, poc: string, srt: string, desc: boolean, p: number, size: number) => {
      setLoading(true);
      api
        .vulnEntries({
          category: "cve", // 固定 CVE 分类:本页只展示有编号的公开漏洞
          q,
          severity: sev === "all" ? "" : sev,
          hasPoc: poc === "all" ? "" : poc,
          sort: srt,
          limit: size,
          offset: (p - 1) * size,
        })
        .then((r) => {
          setEntries(r.entries);
          setTotal(r.total);
        })
        .catch((e) => toast.error(`加载 CVE 失败:${(e as Error).message}`))
        .finally(() => setLoading(false));
    },
    [],
  );

  const loadSync = React.useCallback(() => {
    api
      .vulnSyncStatus()
      .then((r) => setSync({ runs: r.runs, enabled: r.enabled, interval_hours: r.interval_hours }))
      .catch(() => undefined);
  }, []);

  React.useEffect(() => {
    load(query, severity, hasPoc, sort, sortDesc, page, pageSize);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [severity, hasPoc, sort, sortDesc, page, pageSize]);

  React.useEffect(() => {
    loadSync();
  }, [loadSync]);

  const search = () => {
    setPage(1);
    load(query, severity, hasPoc, sort, sortDesc, 1, pageSize);
  };
  const reload = () => load(query, severity, hasPoc, sort, sortDesc, page, pageSize);

  const runSync = async () => {
    setSyncing(true);
    try {
      await api.triggerVulnSync();
      toast.success("已触发采集,后台从公开漏洞源拉取中");
      setTimeout(() => {
        loadSync();
        reload();
      }, 8000);
    } catch (e) {
      toast.error(`触发失败:${(e as Error).message}`);
    } finally {
      setSyncing(false);
    }
  };

  const remove = async (e: VulnEntry) => {
    if (!window.confirm(`删除 ${e.cve_id || e.title}?`)) return;
    try {
      await api.deleteVulnEntry(e.id);
      toast.success("已删除");
      reload();
    } catch (err) {
      toast.error(`删除失败:${(err as Error).message}`);
    }
  };

  // 批量删除已选 CVE:并行逐个调删除接口(连带删各自 PoC),统计成功/失败后
  // 清空选择并用页面既有的 reload() 刷新。数千条情报的清库靠检索 + 分页批量删。
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

  const lastRun = sync?.runs?.[0];

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
          <h1 className="font-semibold text-xl">CVE 情报</h1>
          <p className="text-muted-foreground text-sm">
            有编号的公开漏洞;后台定时从 NVD / GitHub Advisory / Exploit-DB / GitHub PoC 持续搜集并自动发现 PoC
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {sync && (
            <Button
              variant="outline"
              size="sm"
              onClick={() =>
                void api
                  .setVulnSyncConfig({ enabled: !sync.enabled })
                  .then(() => {
                    toast.success(sync.enabled ? "已关闭定时采集" : "已开启定时采集");
                    loadSync();
                  })
                  .catch((e) => toast.error(`设置失败:${(e as Error).message}`))
              }
            >
              {sync.enabled ? `定时采集:开(每 ${sync.interval_hours}h)` : "定时采集:关"}
            </Button>
          )}
          <Button variant="outline" size="sm" disabled={syncing} onClick={() => void runSync()}>
            <RefreshCwIcon className={syncing ? "size-4 animate-spin" : "size-4"} />
            立即采集
          </Button>
          <Button size="sm" onClick={() => setCreating(true)}>
            <PlusIcon className="size-4" />
            新增 CVE
          </Button>
        </div>
      </div>

      {lastRun && (
        <p className="text-muted-foreground mb-3 text-xs">
          上次采集:{lastRun.source} · {fmtTime(lastRun.started_at)} · 新增 {lastRun.inserted} / 更新 {lastRun.updated} /
          PoC {lastRun.pocs_found}
          {lastRun.status === "failed" && <span className="text-destructive"> · 失败:{lastRun.error}</span>}
        </p>
      )}

      <Card>
        <CardContent className="px-0 pt-4">
          <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
            <div className="relative w-full sm:max-w-xs">
              <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
              <Input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && search()}
                placeholder="搜索 CVE 编号 / 标题 / 厂商 / 产品"
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
            <p className="text-muted-foreground py-16 text-center text-sm">
              {query ? "没有匹配的 CVE" : "还没有 CVE 数据。点「立即采集」从公开漏洞源拉取。"}
            </p>
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
                    <TableHead className="w-32">CVE 编号</TableHead>
                    {/* 标题列不设宽度:table-fixed 下它会自动吃掉其余列之外的余量。
                        设 w-full 会挤掉后面的列(实测导致「更新时间」与「操作」重叠)。 */}
                    <TableHead>标题</TableHead>
                    <TableHead className="w-28">厂商 / 产品</TableHead>
                    <TableHead className="w-20">严重度</TableHead>
                    <SortHead field="poc" width="w-14">
                      PoC
                    </SortHead>
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
                          aria-label={`选择 ${e.cve_id || e.title}`}
                        />
                      </TableCell>
                      <TableCell className="font-mono text-xs">
                        {e.cve_id ? (
                          <a
                            href={`https://nvd.nist.gov/vuln/detail/${e.cve_id}`}
                            target="_blank"
                            rel="noreferrer"
                            className="text-blue-600 hover:underline"
                            title={`在 NVD 查看 ${e.cve_id}`}
                          >
                            {e.cve_id}
                          </a>
                        ) : (
                          "—"
                        )}
                      </TableCell>
                      {/* 标题列:table-fixed + truncate 保证长标题不溢出到相邻列 */}
                      <TableCell className="min-w-0">
                        <button
                          type="button"
                          onClick={() => setDetailID(e.id)}
                          className="block w-full truncate text-left hover:underline"
                          title={e.title}
                        >
                          {e.title}
                        </button>
                      </TableCell>
                      <TableCell className="text-muted-foreground truncate text-xs" title={[e.vendor, e.product].filter(Boolean).join(" / ")}>
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
                      <TableCell className="text-muted-foreground text-xs">{fmtTime(e.updated_at)}</TableCell>
                      <TableCell>
                        <div className="flex items-center gap-1">
                          <Button variant="ghost" size="sm" onClick={() => setDetailID(e.id)} title="查看详情与 PoC">
                            <ExternalLinkIcon className="size-3.5" />
                          </Button>
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
              将删除已选的 {checkedIds.size} 条 CVE 情报(含其 PoC),该操作不可恢复。
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
        defaultCategory="cve"
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
