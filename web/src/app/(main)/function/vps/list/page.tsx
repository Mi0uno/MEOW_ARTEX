"use client";

// VPS 中心 · 机器池 —— 机器的录入、测活、编辑与删除(列表页)。
//
// 录入/编辑对话框共用 components/vps-shared.tsx 的 VpsMachineDialog(凭据不回显,留空不改)。
// 行内「终端」跳详情页的终端页签(?tab=terminal),不在列表页直接开 WS——连接生命周期归详情页管。
//
// 说明:机器池接口(vpsMachines)只有 status/q/limit/offset 筛选参数,没有服务端排序,
// 表头排序是**当前页内**排序(工具栏有说明);分页 total 一律取接口返回值(已在筛选后算)。
// 支持 /function/vps/list?status=offline 深链(仪表盘告警条会带过来),useSearchParams 包 Suspense。

import * as React from "react";

import Link from "next/link";
import { useSearchParams } from "next/navigation";

import {
  ActivityIcon,
  ChevronDownIcon,
  ChevronUpIcon,
  Loader2Icon,
  PencilIcon,
  PlusIcon,
  RefreshCwIcon,
  SearchIcon,
  ServerIcon,
  SquareTerminalIcon,
  Trash2Icon,
} from "lucide-react";
import { toast } from "sonner";

import { TablePagination } from "@/components/table-pagination";
import { fmtTime, num, VpsMachineDialog, vpsStatusBadgeClass, vpsStatusLabel } from "@/components/vps-shared";
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
import { api } from "@/lib/api";
import type { VpsMachine } from "@/lib/types";

const PAGE_SIZE = 50;

// Radix Select 的 value="" 是保留值,空语义一律用哨兵串。
const ALL = "__all__";

// 状态排序用的人序:可用 → 占用 → 维护 → 离线 → 未测。
const STATUS_RANK: Record<string, number> = {
  online: 0,
  busy: 1,
  maintenance: 2,
  offline: 3,
  unknown: 4,
};

type SortKey = "name" | "status" | "check";

export default function VpsListPage() {
  return (
    <React.Suspense fallback={null}>
      <VpsListInner />
    </React.Suspense>
  );
}

function VpsListInner() {
  const searchParams = useSearchParams();
  // 仪表盘告警条会带 ?status=offline 深链进来。
  const [status, setStatus] = React.useState(() => {
    const s = searchParams.get("status");
    return s && s !== "all" ? s : ALL;
  });
  const [machines, setMachines] = React.useState<VpsMachine[]>([]);
  const [total, setTotal] = React.useState(0);
  const [query, setQuery] = React.useState("");
  const [queryApplied, setQueryApplied] = React.useState("");
  // 客户端排序(当前页内):default = 按创建顺序(接口默认)。
  const [sortKey, setSortKey] = React.useState<SortKey | "default">("default");
  const [sortDesc, setSortDesc] = React.useState(true);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);
  const [loading, setLoading] = React.useState(true);
  const [dialogOpen, setDialogOpen] = React.useState(false);
  const [editing, setEditing] = React.useState<VpsMachine | null>(null);
  const [checkingId, setCheckingId] = React.useState<number | null>(null);
  // 行多选与批量删除:三态全选按当前页行计算。
  const [checkedIds, setCheckedIds] = React.useState<Set<number>>(new Set());
  const [batchConfirmOpen, setBatchConfirmOpen] = React.useState(false);
  const [batchDeleting, setBatchDeleting] = React.useState(false);

  const load = React.useCallback((q: string, st: string, p: number, size: number) => {
    setLoading(true);
    api
      .vpsMachines({
        q,
        status: st === ALL ? "" : st,
        limit: size,
        offset: (p - 1) * size,
      })
      .then((r) => {
        setMachines(r.machines ?? []);
        setTotal(r.total);
      })
      .catch((e) => {
        // 后端未就绪/失败:清空列表 + 提示,不白屏。
        setMachines([]);
        setTotal(0);
        toast.error(`加载机器池失败:${(e as Error).message}`);
      })
      .finally(() => setLoading(false));
  }, []);

  React.useEffect(() => {
    load(queryApplied, status, page, pageSize);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status, page, pageSize]);

  const search = () => {
    const q = query.trim();
    setPage(1);
    setQueryApplied(q);
    load(q, status, 1, pageSize);
  };
  const reload = () => load(queryApplied, status, page, pageSize);

  const remove = async (m: VpsMachine) => {
    if (!window.confirm(`删除机器「${m.name}」?执行流水与指派历史保留,但机器本体的凭据将被移除,不可恢复。`)) return;
    try {
      await api.deleteVpsMachine(m.id);
      toast.success("已删除");
      // 删掉当前页最后一条后留在空页:回退一页更贴心。
      if (machines.length === 1 && page > 1) setPage(page - 1);
      else reload();
    } catch (e) {
      // 被任务占用时后端回 409,错误信息里带原因。
      toast.error(`删除失败:${(e as Error).message}`);
    }
  };

  // 手动测活(同步):几秒内返回,顺带刷新系统信息。
  const check = async (m: VpsMachine) => {
    setCheckingId(m.id);
    try {
      const r = await api.checkVpsMachine(m.id);
      if (r.status === "offline") {
        toast.warning(`「${m.name}」测活失败:${r.last_error || "连接失败"}`);
      } else {
        toast.success(`「${m.name}」测活通过(${vpsStatusLabel(String(r.status))})`);
      }
      reload();
    } catch (e) {
      toast.error(`测活失败:${(e as Error).message}`);
    } finally {
      setCheckingId(null);
    }
  };

  // ---- 行多选 ----
  const pageIds = React.useMemo(() => machines.map((m) => m.id), [machines]);
  const allChecked = pageIds.length > 0 && pageIds.every((id) => checkedIds.has(id));
  const someChecked = pageIds.some((id) => checkedIds.has(id));
  const toggleRow = (id: number, checked: boolean) => {
    setCheckedIds((prev) => {
      const next = new Set(prev);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  };
  const toggleAllPage = (checked: boolean) => {
    setCheckedIds((prev) => {
      const next = new Set(prev);
      for (const id of pageIds) {
        if (checked) next.add(id);
        else next.delete(id);
      }
      return next;
    });
  };

  // 批量删除:逐条走单删接口,allSettled 保证一条失败不拖垮其余;被占用的会失败并计数。
  const confirmBatchDelete = async () => {
    const ids = Array.from(checkedIds);
    setBatchDeleting(true);
    try {
      const results = await Promise.allSettled(ids.map((id) => api.deleteVpsMachine(id)));
      const ok = results.filter((r) => r.status === "fulfilled").length;
      const failed = results.length - ok;
      if (failed === 0) toast.success(`已删除 ${ok} 台机器`);
      else toast.warning(`成功 ${ok} 台,失败 ${failed} 台(占用中的机器会被拒绝删除)`);
      setCheckedIds(new Set());
      reload();
    } finally {
      setBatchDeleting(false);
      setBatchConfirmOpen(false);
    }
  };

  // ---- 当前页内排序 ----
  const sorted = React.useMemo(() => {
    const arr = [...machines];
    const dir = sortDesc ? -1 : 1;
    if (sortKey === "name") {
      arr.sort((a, b) => (a.name ?? "").localeCompare(b.name ?? "") * dir);
    } else if (sortKey === "status") {
      arr.sort((a, b) => ((STATUS_RANK[String(a.status)] ?? 9) - (STATUS_RANK[String(b.status)] ?? 9)) * dir);
    } else if (sortKey === "check") {
      const t = (m: VpsMachine) => (m.last_check_at ? Date.parse(m.last_check_at) : 0);
      arr.sort((a, b) => (t(a) - t(b)) * dir);
    }
    return arr;
  }, [machines, sortKey, sortDesc]);

  const SortHead = ({ field, width, children }: { field: SortKey; width?: string; children: React.ReactNode }) => (
    <TableHead className={width}>
      <button
        type="button"
        title="排序(当前页内)"
        onClick={() => {
          if (sortKey === field) setSortDesc((d) => !d);
          else {
            setSortKey(field);
            setSortDesc(field !== "name");
          }
        }}
        className="hover:text-foreground inline-flex items-center gap-1"
      >
        {children}
        {sortKey === field ? (
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
          <h1 className="font-semibold text-xl">VPS 中心 · 机器池</h1>
          <p className="text-muted-foreground text-sm">
            你的 Linux 机器池。录入时平台会做一次 SSH 连通性测试并采集系统信息;任务运行时 Bash 命令在指派的机器上执行。
          </p>
        </div>
        <div className="flex items-center gap-2">
          {checkedIds.size > 0 && (
            <Button variant="destructive" size="sm" onClick={() => setBatchConfirmOpen(true)}>
              <Trash2Icon className="size-4" />
              删除已选 (<span className="tabular-nums">{checkedIds.size}</span>)
            </Button>
          )}
          <Button
            size="sm"
            onClick={() => {
              setEditing(null);
              setDialogOpen(true);
            }}
          >
            <PlusIcon className="size-4" />
            录入机器
          </Button>
        </div>
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
                placeholder="搜索名称 / 主机 / 标签 / 备注"
                className="pl-8"
              />
            </div>
            <Button variant="outline" size="sm" onClick={search}>
              搜索
            </Button>
            <Select
              value={status}
              onValueChange={(v) => {
                setPage(1);
                setStatus(v);
              }}
            >
              <SelectTrigger className="w-32">
                <SelectValue placeholder="状态" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value={ALL}>全部状态</SelectItem>
                  <SelectItem value="online">在线空闲</SelectItem>
                  <SelectItem value="busy">占用中</SelectItem>
                  <SelectItem value="offline">离线</SelectItem>
                  <SelectItem value="maintenance">维护</SelectItem>
                  <SelectItem value="unknown">未测活</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
            <Button variant="ghost" size="sm" onClick={reload} title="重新加载">
              <RefreshCwIcon className="size-3.5" />
              刷新
            </Button>
            <span className="text-muted-foreground text-xs">表头排序作用于当前页</span>
            <span className="text-muted-foreground ml-auto text-xs tabular-nums">
              {queryApplied ? `命中 ${total} / ` : ""}共 {total} 台
            </span>
          </div>

          {loading && machines.length === 0 ? (
            <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
          ) : machines.length === 0 ? (
            <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
              <ServerIcon className="size-8 opacity-40" />
              <span>
                {queryApplied || status !== ALL
                  ? "没有匹配的机器"
                  : "机器池还是空的。点「录入机器」把你的第一台 Linux VPS 加进来;录入后会立即做一次连通性测试。"}
              </span>
            </div>
          ) : (
            <>
              <Table className="table-fixed">
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    {/* 机器名列不设宽度:table-fixed 下自动吃掉余量。
                        固定列合计 720px(32+160+80+112+112+112+112),常规容器下余量充足;
                        再加列必须先瘦身其他列,否则机器名列会被压到看不见(代理组页踩过)。 */}
                    <TableHead className="w-8">
                      <Checkbox
                        checked={allChecked ? true : someChecked ? "indeterminate" : false}
                        onCheckedChange={(v) => toggleAllPage(v === true)}
                        aria-label="全选当前页机器"
                      />
                    </TableHead>
                    <SortHead field="name">机器</SortHead>
                    <TableHead className="w-40">主机</TableHead>
                    <SortHead field="status" width="w-20">
                      状态
                    </SortHead>
                    <TableHead className="w-28">占用任务</TableHead>
                    <TableHead className="w-28">标签</TableHead>
                    <SortHead field="check" width="w-28">
                      最近检测
                    </SortHead>
                    <TableHead className="w-28">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {sorted.map((m) => {
                    const taskID = num(m.assigned_task_id);
                    const busyTask = taskID > 0;
                    return (
                      <TableRow key={m.id} className={checkedIds.has(m.id) ? "bg-muted/40" : undefined}>
                        <TableCell className="w-8">
                          <Checkbox
                            checked={checkedIds.has(m.id)}
                            onCheckedChange={(v) => toggleRow(m.id, v === true)}
                            aria-label={`选择机器 ${m.name}`}
                          />
                        </TableCell>
                        {/* 机器名即入口:点击进入详情页;第二行给系统/地区一眼可读的摘要。 */}
                        <TableCell className="min-w-0">
                          <Link
                            href={`/function/vps/detail?id=${m.id}`}
                            className="block w-full truncate text-left font-medium hover:underline"
                            title={m.name}
                          >
                            {m.name}
                          </Link>
                          <span className="text-muted-foreground block truncate text-xs" title={[m.os_name, m.arch, m.region].filter(Boolean).join(" · ")}>
                            {[m.os_name, m.arch, m.region].filter(Boolean).join(" · ") || "系统信息未采集"}
                          </span>
                        </TableCell>
                        <TableCell className="min-w-0">
                          <span className="block truncate font-mono text-xs" title={`${m.host}:${m.port}`}>
                            {m.host}:{m.port}
                          </span>
                        </TableCell>
                        <TableCell>
                          <Badge variant="outline" className={vpsStatusBadgeClass(String(m.status))}>
                            {busyTask ? "占用中" : vpsStatusLabel(String(m.status))}
                          </Badge>
                        </TableCell>
                        <TableCell className="text-xs">
                          {busyTask ? (
                            <Link
                              href={`/function/tasks/detail?id=${taskID}`}
                              className="block w-full truncate text-blue-600 hover:underline"
                              title={m.assigned_task_name || `任务 #${taskID}`}
                            >
                              {m.assigned_task_name || `#${taskID}`}
                            </Link>
                          ) : (
                            <span className="text-muted-foreground">空闲</span>
                          )}
                        </TableCell>
                        <TableCell className="min-w-0">
                          <span className="text-muted-foreground block truncate text-xs" title={(m.tags ?? []).join(", ")}>
                            {(m.tags ?? []).join(", ") || "—"}
                          </span>
                        </TableCell>
                        <TableCell className="text-muted-foreground text-xs tabular-nums" title={m.last_error || undefined}>
                          {m.status === "offline" ? (
                            <span className="text-red-600 dark:text-red-400" title={m.last_error || "测活失败"}>
                              失联
                            </span>
                          ) : (
                            fmtTime(m.last_check_at)
                          )}
                        </TableCell>
                        <TableCell>
                          <div className="flex items-center gap-0.5">
                            <Button
                              variant="ghost"
                              size="icon-xs"
                              onClick={() => void check(m)}
                              disabled={checkingId === m.id}
                              title="手动测活"
                            >
                              {checkingId === m.id ? (
                                <Loader2Icon className="size-3.5 animate-spin" />
                              ) : (
                                <ActivityIcon className="size-3.5" />
                              )}
                            </Button>
                            <Button variant="ghost" size="icon-xs" asChild title="打开终端">
                              <Link href={`/function/vps/detail?id=${m.id}&tab=terminal`} aria-label="打开终端">
                                <SquareTerminalIcon className="size-3.5" />
                              </Link>
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon-xs"
                              onClick={() => {
                                setEditing(m);
                                setDialogOpen(true);
                              }}
                              title="编辑(凭据留空不修改)"
                            >
                              <PencilIcon className="size-3.5" />
                            </Button>
                            <Button variant="ghost" size="icon-xs" onClick={() => void remove(m)} title="删除机器">
                              <Trash2Icon className="text-muted-foreground size-3.5" />
                            </Button>
                          </div>
                        </TableCell>
                      </TableRow>
                    );
                  })}
                </TableBody>
              </Table>
              <TablePagination
                page={page}
                pageSize={pageSize}
                total={total}
                pageSizeOptions={[25, 50, 100, 200]}
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

      <VpsMachineDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        machine={editing}
        onSaved={() => reload()}
      />

      {/* 批量删除确认:被占用机器会被后端 409 拒绝,文案里写明。 */}
      <AlertDialog open={batchConfirmOpen} onOpenChange={setBatchConfirmOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>确认批量删除机器</AlertDialogTitle>
            <AlertDialogDescription>
              将删除选中的 <span className="font-semibold tabular-nums">{checkedIds.size}</span>{" "}
              台机器及其加密凭据;执行流水与指派历史保留。被任务占用的机器会被拒绝删除。此操作不可撤销。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={batchDeleting}>取消</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault();
                void confirmBatchDelete();
              }}
              disabled={batchDeleting}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {batchDeleting ? "删除中…" : "确认删除"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
