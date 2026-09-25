"use client";

// VPS 中心 · 工具集 —— 渗透工具的一键安装脚本包。
//
// 内置工具集(基础侦察 / Web 漏扫 / 爆破破解)由后端播种:脚本可查看,但名字与脚本
// 不可改、也不可删(后端对 builtin 的 PUT/DELETE 回 400,前端直接禁用按钮并说明)。
// 「应用到机器」对所选在线机器同步执行脚本,逐台展示 ok / 退出码 / 错误,
// 结果同时落执行流水(source=toolset),可在机器详情回看完整输出。

import * as React from "react";

import {
  ArrowDownIcon,
  ArrowUpDownIcon,
  ArrowUpIcon,
  HardDriveDownloadIcon,
  LockIcon,
  PencilIcon,
  PlusIcon,
  RefreshCwIcon,
  SearchIcon,
  Trash2Icon,
  WrenchIcon,
  XIcon,
} from "lucide-react";
import { toast } from "sonner";

import { TablePagination } from "@/components/table-pagination";
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
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { num } from "@/components/vps-shared";
import { api } from "@/lib/api";
import { type SortDirection, useStoredSortPreference } from "@/lib/sort-preference";
import type { VpsMachine, VpsToolset } from "@/lib/types";
import { cn } from "@/lib/utils";

function fmtTime(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

type ToolsetSortField = "name" | "updated";
type ToolsetKindFilter = "all" | "builtin" | "custom";

const TOOLSET_SORT_FIELDS: readonly ToolsetSortField[] = ["name", "updated"];
// 排序偏好落 localStorage:工具集总量小但会被反复进出,记住上次排序少一次点击。
const TOOLSET_SORT_PREFERENCE_KEY = "artex_vps_toolset_sort";

// 排序比较;并列时按 id 收尾保证顺序稳定(翻页/重渲染不闪跳)。
function compareToolsets(a: VpsToolset, b: VpsToolset, field: ToolsetSortField, direction: SortDirection): number {
  let compared = 0;
  if (field === "name") {
    compared = a.name.localeCompare(b.name, "zh-Hans-CN", { numeric: true, sensitivity: "base" });
  } else {
    const av = Date.parse(a.updated_at ?? "");
    const bv = Date.parse(b.updated_at ?? "");
    compared = (Number.isNaN(av) ? 0 : av) - (Number.isNaN(bv) ? 0 : bv);
  }
  if (compared !== 0) return direction === "asc" ? compared : -compared;
  return num(a.id) - num(b.id);
}

// 三态图标分开写而非嵌套三元:仓库 Biome 基线禁 noNestedTernary。
function toolsetSortIcon(active: boolean, direction: SortDirection) {
  if (!active) {
    return <ArrowUpDownIcon className="size-3.5 opacity-40 transition-opacity group-hover/sort:opacity-100" />;
  }
  if (direction === "asc") return <ArrowUpIcon className="size-3.5" />;
  return <ArrowDownIcon className="size-3.5" />;
}

function SortableToolsetHead({
  field,
  label,
  activeField,
  direction,
  className,
  onSort,
}: {
  field: ToolsetSortField;
  label: string;
  activeField: ToolsetSortField;
  direction: SortDirection;
  className?: string;
  onSort: (field: ToolsetSortField) => void;
}) {
  const active = activeField === field;
  let ariaSort: React.AriaAttributes["aria-sort"] = "none";
  if (active) ariaSort = direction === "asc" ? "ascending" : "descending";

  let actionLabel = `按${label}倒序排序`;
  if (active) actionLabel = `${label}当前${direction === "asc" ? "正序" : "倒序"}，点击切换排序方向`;

  return (
    <TableHead aria-sort={ariaSort} className={className}>
      <button
        type="button"
        className="group/sort inline-flex h-full w-full items-center gap-1 outline-none focus-visible:underline"
        aria-label={actionLabel}
        onClick={() => onSort(field)}
      >
        <span>{label}</span>
        {toolsetSortIcon(active, direction)}
      </button>
    </TableHead>
  );
}

export default function VpsToolsetsPage() {
  const [toolsets, setToolsets] = React.useState<VpsToolset[]>([]);
  const [loading, setLoading] = React.useState(true);
  // 编辑/查看对话框:toolset 为 null 且 open = 新建;builtin 时只读查看。
  const [dialogOpen, setDialogOpen] = React.useState(false);
  const [editing, setEditing] = React.useState<VpsToolset | null>(null);
  const [deleting, setDeleting] = React.useState<VpsToolset | null>(null);
  const [applyTarget, setApplyTarget] = React.useState<VpsToolset | null>(null);
  // 列表页标准四件套:搜索(名称/描述/脚本内容) + 内置/自定义分类 + 点表头排序 + 分页。
  // 工具集是一次性全量拉取的小列表,筛选排序分页都在前端做,与任务列表同模式。
  const [query, setQuery] = React.useState("");
  const [kindFilter, setKindFilter] = React.useState<ToolsetKindFilter>("all");
  const [sortPreference, setSortPreference] = useStoredSortPreference(
    TOOLSET_SORT_PREFERENCE_KEY,
    TOOLSET_SORT_FIELDS,
    "updated",
    "desc",
  );
  const { field: sortField, direction: sortDirection } = sortPreference;
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(20);

  const load = React.useCallback(() => {
    setLoading(true);
    api
      .vpsToolsets()
      .then((r) => setToolsets(r.toolsets ?? []))
      .catch((e) => {
        // 后端未就绪(404/500):空态 + 提示,不白屏。
        setToolsets([]);
        toast.error(`加载工具集失败:${(e as Error).message}`);
      })
      .finally(() => setLoading(false));
  }, []);

  React.useEffect(() => {
    load();
  }, [load]);

  // 搜索命中名称/描述/脚本内容——工具集的描述常是一句「装了什么」,而脚本里才有
  // 具体包名,两处都搜才能用「nmap」这类关键词直接定位。
  const filtered = React.useMemo(() => {
    const q = query.trim().toLowerCase();
    return toolsets.filter((t) => {
      if (kindFilter === "builtin" && !t.builtin) return false;
      if (kindFilter === "custom" && t.builtin) return false;
      if (!q) return true;
      return (
        t.name.toLowerCase().includes(q) ||
        (t.description ?? "").toLowerCase().includes(q) ||
        (t.script ?? "").toLowerCase().includes(q)
      );
    });
  }, [toolsets, query, kindFilter]);

  const ordered = React.useMemo(
    () => [...filtered].sort((a, b) => compareToolsets(a, b, sortField, sortDirection)),
    [filtered, sortField, sortDirection],
  );
  const paginated = React.useMemo(
    () => ordered.slice((page - 1) * pageSize, page * pageSize),
    [ordered, page, pageSize],
  );

  // 名称列首次点击给正序(字典序自然),其余给倒序(最新在前)——与资产主体选择页一致。
  const sortToolsetsBy = React.useCallback(
    (field: ToolsetSortField) => {
      setSortPreference((current) => {
        if (current.field !== field) return { field, direction: field === "name" ? "asc" : "desc" };
        return { field, direction: current.direction === "asc" ? "desc" : "asc" };
      });
    },
    [setSortPreference],
  );

  // biome-ignore lint/correctness/useExhaustiveDependencies: 筛选/排序变化时有意重置回第一页。
  React.useEffect(() => {
    setPage(1);
  }, [query, kindFilter, sortField, sortDirection]);

  const remove = async () => {
    if (!deleting) return;
    try {
      await api.deleteVpsToolset(deleting.id);
      toast.success(`工具集「${deleting.name}」已删除`);
      load();
    } catch (e) {
      toast.error(`删除失败:${(e as Error).message}`);
    } finally {
      setDeleting(null);
    }
  };

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div>
          <h1 className="font-semibold text-xl">VPS 中心 · 工具集</h1>
          <p className="text-muted-foreground text-sm">
            把常用的渗透工具安装脚本存成一键工具集,应用到机器后 agent 就能感知并直接使用这些工具;机器上缺什么也可以让
            agent 自己调工具集装。
          </p>
        </div>
        <Button
          size="sm"
          onClick={() => {
            setEditing(null);
            setDialogOpen(true);
          }}
        >
          <PlusIcon className="size-4" />
          新建工具集
        </Button>
      </div>

      <Card>
        <CardContent className="px-0 pt-4">
          <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
            <span className="flex items-center gap-1.5 text-sm font-medium">
              <WrenchIcon className="text-muted-foreground size-4" />
              工具集清单
            </span>
            <div className="relative w-full sm:max-w-xs">
              <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
              <Input
                placeholder="搜索名称 / 描述 / 脚本内容"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                className="pl-8"
              />
              {query && (
                <button
                  type="button"
                  onClick={() => setQuery("")}
                  aria-label="清除搜索"
                  className="text-muted-foreground hover:text-foreground absolute top-1/2 right-2 -translate-y-1/2"
                >
                  <XIcon className="size-4" />
                </button>
              )}
            </div>
            <Select value={kindFilter} onValueChange={(v) => setKindFilter(v as ToolsetKindFilter)}>
              <SelectTrigger className="w-32">
                <SelectValue placeholder="分类" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="all">全部分类</SelectItem>
                  <SelectItem value="builtin">内置</SelectItem>
                  <SelectItem value="custom">自定义</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
            <span className="text-muted-foreground text-xs tabular-nums">
              {filtered.length}/{toolsets.length} 个
            </span>
            <Button variant="ghost" size="sm" onClick={load} className="ml-auto">
              <RefreshCwIcon className="size-3.5" />
              刷新
            </Button>
          </div>

          {loading && toolsets.length === 0 ? (
            <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
          ) : toolsets.length === 0 ? (
            <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
              <WrenchIcon className="size-8 opacity-40" />
              <span>
                还没有工具集。平台内置了「基础侦察 / Web 漏扫 / 爆破破解」三套脚本,若此处未显示,可能是 VPS
                服务暂不可用;也可以新建自己的脚本包。
              </span>
            </div>
          ) : filtered.length === 0 ? (
            <p className="text-muted-foreground py-16 text-center text-sm">没有匹配的工具集,调整搜索或分类再试。</p>
          ) : (
            <>
              <Table className="table-fixed">
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    {/* 工具集列不设宽度:table-fixed 下自动吃掉余量(名称 + 描述两行)。 */}
                    <SortableToolsetHead
                      field="name"
                      label="工具集"
                      activeField={sortField}
                      direction={sortDirection}
                      onSort={sortToolsetsBy}
                    />
                    <TableHead className="w-16">内置</TableHead>
                    <TableHead className="w-20">脚本</TableHead>
                    <SortableToolsetHead
                      field="updated"
                      label="更新时间"
                      activeField={sortField}
                      direction={sortDirection}
                      className="w-36"
                      onSort={sortToolsetsBy}
                    />
                    <TableHead className="w-32">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {paginated.map((t) => (
                    <TableRow key={t.id}>
                      <TableCell className="min-w-0">
                        <button
                          type="button"
                          onClick={() => {
                            setEditing(t);
                            setDialogOpen(true);
                          }}
                          className="block w-full truncate text-left font-medium hover:underline"
                          title={t.name}
                        >
                          {t.name}
                        </button>
                        <span
                          className="text-muted-foreground block truncate text-xs"
                          title={t.description || undefined}
                        >
                          {t.description || "—"}
                        </span>
                      </TableCell>
                      <TableCell>
                        {t.builtin ? (
                          <Badge variant="outline" className="border-purple-500/30 bg-purple-500/15 text-purple-600">
                            <LockIcon className="size-3" />
                            内置
                          </Badge>
                        ) : (
                          <span className="text-muted-foreground text-xs">自定义</span>
                        )}
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs tabular-nums">
                        {num(t.script?.length)} 字符
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs tabular-nums">
                        {fmtTime(t.updated_at)}
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center gap-0.5">
                          <Button
                            variant="ghost"
                            size="icon-xs"
                            title={t.builtin ? "查看脚本(内置不可修改)" : "编辑"}
                            onClick={() => {
                              setEditing(t);
                              setDialogOpen(true);
                            }}
                          >
                            <PencilIcon className="size-3.5" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon-xs"
                            title="应用到机器"
                            aria-label="应用到机器"
                            onClick={() => setApplyTarget(t)}
                          >
                            {/* 魔棒图标与编辑铅笔在 12px 下几乎无法区分,换成「装进磁盘」语义的图标。 */}
                            <HardDriveDownloadIcon className="size-3.5" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon-xs"
                            disabled={t.builtin}
                            title={t.builtin ? "内置工具集不可删除" : "删除工具集"}
                            onClick={() => setDeleting(t)}
                          >
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
                total={filtered.length}
                onPageChange={setPage}
                onPageSizeChange={(nextPageSize) => {
                  setPageSize(nextPageSize);
                  setPage(1);
                }}
              />
            </>
          )}
        </CardContent>
      </Card>

      <ToolsetDialog open={dialogOpen} onOpenChange={setDialogOpen} toolset={editing} onSaved={() => load()} />

      <ApplyDialog toolset={applyTarget} onOpenChange={(o) => !o && setApplyTarget(null)} />

      {/* 删除确认:builtin 的按钮已禁用,这里再挡一层以防深链触发。 */}
      <AlertDialog open={deleting != null} onOpenChange={(o) => !o && setDeleting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除工具集「{deleting?.name}」?</AlertDialogTitle>
            <AlertDialogDescription>删除后不可恢复;已安装到机器上的工具不受影响。</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault();
                void remove();
              }}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              删除
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

// ── 新建 / 编辑(内置=只读查看)对话框 ────────────────────────────────────────

function ToolsetDialog({
  open,
  onOpenChange,
  toolset,
  onSaved,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  toolset: VpsToolset | null;
  onSaved?: () => void;
}) {
  const editing = toolset != null;
  const readonly = editing && toolset.builtin;
  const [name, setName] = React.useState("");
  const [description, setDescription] = React.useState("");
  const [script, setScript] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  // 每次打开按目标重置(同一个对话框服务新建与编辑,残留会写串)。
  React.useEffect(() => {
    if (!open) return;
    setName(toolset?.name ?? "");
    setDescription(toolset?.description ?? "");
    setScript(toolset?.script ?? "");
  }, [open, toolset]);

  const submit = async () => {
    const n = name.trim();
    if (!n) {
      toast.error("名称必填");
      return;
    }
    if (!script.trim()) {
      toast.error("脚本内容不能为空");
      return;
    }
    setBusy(true);
    try {
      if (editing) {
        await api.updateVpsToolset(toolset.id, { name: n, description: description.trim(), script });
        toast.success("工具集已保存");
      } else {
        await api.createVpsToolset({ name: n, description: description.trim(), script });
        toast.success("工具集已创建");
      }
      onSaved?.();
      onOpenChange(false);
    } catch (e) {
      toast.error(`保存失败:${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            {editing ? (readonly ? `查看「${toolset.name}」` : `编辑「${toolset.name}」`) : "新建工具集"}
            {readonly ? (
              <Badge variant="outline" className="border-purple-500/30 bg-purple-500/15 text-purple-600">
                <LockIcon className="size-3" />
                内置 · 不可修改删除
              </Badge>
            ) : null}
          </DialogTitle>
          <DialogDescription className="text-xs">
            {readonly
              ? "内置工具集由平台维护:脚本仅供参考与一键应用,名称与内容不可修改、不可删除。"
              : "脚本保存后可反复应用到任意在线机器;应用即执行,结果落执行流水。"}
          </DialogDescription>
        </DialogHeader>

        <div className="grid gap-4">
          <div className="grid gap-1.5">
            <Label>名称 *</Label>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              disabled={readonly}
              placeholder="如:内网横向工具包"
            />
          </div>
          <div className="grid gap-1.5">
            <Label>描述</Label>
            <Input
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              disabled={readonly}
              placeholder="这个工具集装什么、什么时候用"
            />
          </div>
          <div className="grid gap-1.5">
            <Label>脚本(bash){editing ? "" : " *"}</Label>
            {/* 等宽字体:脚本是给 bash 逐行执行的,对齐可读性优先。 */}
            <Textarea
              className="min-h-64 font-mono text-xs leading-relaxed"
              value={script}
              onChange={(e) => setScript(e.target.value)}
              disabled={readonly}
              placeholder={"#!/usr/bin/env bash\nset -e\napt-get update -y || true\napt-get install -y nmap || true"}
            />
            <p className="text-muted-foreground text-xs">
              脚本将以 SSH 登录用户在机器上通过 bash 执行(超时上限 600 秒,输出完整落执行流水);请写成幂等安装(如{" "}
              <code className="rounded bg-muted px-1">apt-get install -y</code>、
              <code className="rounded bg-muted px-1">|| true</code>),重复应用不出错。
            </p>
          </div>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {readonly ? "关闭" : "取消"}
          </Button>
          {!readonly && (
            <Button onClick={() => void submit()} disabled={busy}>
              {busy ? "保存中…" : "保存"}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ── 应用到机器对话框 ─────────────────────────────────────────────────────────

type ApplyResult = { machine_id: number; ok: boolean; exit_code: number | null; error: string; execution_id: number };

function ApplyDialog({ toolset, onOpenChange }: { toolset: VpsToolset | null; onOpenChange: (v: boolean) => void }) {
  const [machines, setMachines] = React.useState<VpsMachine[]>([]);
  const [loading, setLoading] = React.useState(false);
  const [selected, setSelected] = React.useState<Set<number>>(new Set());
  const [applying, setApplying] = React.useState(false);
  // null = 还没执行;非 null = 已拿到逐台结果(留在对话框里展示)。
  const [results, setResults] = React.useState<ApplyResult[] | null>(null);

  // 打开时拉取在线机器(只列 online;占用/维护/离线不参与)。
  React.useEffect(() => {
    if (!toolset) return;
    let alive = true;
    setLoading(true);
    setSelected(new Set());
    setResults(null);
    api
      .vpsMachines({ status: "online", limit: 200 })
      .then((r) => {
        if (alive) setMachines(r.machines ?? []);
      })
      .catch((e) => {
        if (!alive) return;
        setMachines([]);
        toast.error(`加载在线机器失败:${(e as Error).message}`);
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [toolset]);

  const machineName = (id: number) => machines.find((m) => m.id === id)?.name ?? `机器 #${id}`;

  const apply = async () => {
    if (!toolset || selected.size === 0) return;
    setApplying(true);
    try {
      const r = await api.applyVpsToolset(toolset.id, Array.from(selected));
      setResults(r.results ?? []);
      const ok = (r.results ?? []).filter((x) => x.ok).length;
      const all = (r.results ?? []).length;
      if (ok === all) toast.success(`已应用到 ${all} 台机器`);
      else toast.warning(`成功 ${ok} 台,失败 ${all - ok} 台,详情见下方结果`);
    } catch (e) {
      toast.error(`应用失败:${(e as Error).message}`);
    } finally {
      setApplying(false);
    }
  };

  const toggle = (id: number, checked: boolean) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  };

  return (
    <Dialog open={toolset != null} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>应用「{toolset?.name}」到机器</DialogTitle>
          <DialogDescription className="text-xs">
            脚本将在所选在线机器上同步执行(每台可能需要数分钟);输出完整落执行流水,可在机器详情「执行流水」回看。
          </DialogDescription>
        </DialogHeader>

        {results ? (
          // 执行结果:逐台 ok / 退出码 / 错误。
          <div className="grid gap-2">
            {results.map((r) => (
              <div
                key={r.machine_id}
                className={cn(
                  "flex items-start gap-2 rounded-lg border p-2.5 text-xs",
                  r.ok ? "border-emerald-500/30 bg-emerald-500/5" : "border-red-500/30 bg-red-500/5",
                )}
              >
                <span
                  className={cn(
                    "mt-0.5 font-medium",
                    r.ok ? "text-emerald-600 dark:text-emerald-400" : "text-red-600 dark:text-red-400",
                  )}
                >
                  {r.ok ? "成功" : "失败"}
                </span>
                <div className="min-w-0 flex-1">
                  <p className="font-medium">{machineName(num(r.machine_id))}</p>
                  <p className="text-muted-foreground tabular-nums">
                    退出码 {r.exit_code ?? "—"}
                    {num(r.execution_id) > 0 ? ` · 执行流水 #${num(r.execution_id)}` : ""}
                  </p>
                  {r.error ? <p className="mt-0.5 break-all text-red-600 dark:text-red-400">{r.error}</p> : null}
                </div>
              </div>
            ))}
            {results.length === 0 ? <p className="text-muted-foreground text-xs">没有返回结果。</p> : null}
          </div>
        ) : (
          <>
            <ScrollArea className="max-h-72 rounded-lg border">
              <div className="p-2">
                {loading ? (
                  <p className="text-muted-foreground py-8 text-center text-xs">加载在线机器中…</p>
                ) : machines.length === 0 ? (
                  <p className="text-muted-foreground py-8 text-center text-xs">
                    没有可用的在线机器。请先在「机器池」录入并测活通过(status=online)。
                  </p>
                ) : (
                  machines.map((m) => (
                    <label
                      key={m.id}
                      htmlFor={`apply-machine-${m.id}`}
                      className="hover:bg-muted/40 flex cursor-pointer items-center gap-2.5 rounded-md px-2 py-1.5 text-sm"
                    >
                      <Checkbox
                        id={`apply-machine-${m.id}`}
                        checked={selected.has(m.id)}
                        onCheckedChange={(v) => toggle(m.id, v === true)}
                        aria-label={`选择机器 ${m.name}`}
                      />
                      <span className="min-w-0 flex-1 truncate" title={m.name}>
                        {m.name}
                      </span>
                      <span className="text-muted-foreground shrink-0 font-mono text-xs">
                        {m.host}:{m.port}
                      </span>
                    </label>
                  ))
                )}
              </div>
            </ScrollArea>
            <p className="text-muted-foreground text-xs">
              已选 <span className="text-foreground font-medium tabular-nums">{selected.size}</span> 台 ·
              仅列出状态为「在线空闲」的机器
            </p>
          </>
        )}

        <DialogFooter>
          {results ? (
            <Button variant="outline" onClick={() => onOpenChange(false)}>
              完成
            </Button>
          ) : (
            <>
              <Button variant="outline" onClick={() => onOpenChange(false)}>
                取消
              </Button>
              <Button onClick={() => void apply()} disabled={applying || loading || selected.size === 0}>
                {applying ? "执行中…" : `应用到 ${selected.size} 台机器`}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
