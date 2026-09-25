"use client";

// SRC 模式 · 目标:全部目标的一等入口。
//
// 为什么要有本页:目标原先只能从「平台详情 → 目标」页签里看到,「我有哪些目标」
// 「这个目标挂在哪几个平台」没有一个能一屏回答的地方——多平台场景下用户得逐个
// 点进目标详情。本页跨平台聚合全部目标,搜索/筛选/建任务都在这一层完成。

import * as React from "react";

import Link from "next/link";
import { useRouter } from "next/navigation";

import { ArrowRightIcon, PlusIcon, RocketIcon, SearchIcon, TargetIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { TablePagination } from "@/components/table-pagination";
import { CreateSRCTaskDialog } from "@/components/src-create-task-dialog";
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
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { SRCPlatform, SRCTarget } from "@/lib/types";

const PAGE_SIZE = 20;
// Radix Select 保留 value="",「全部/无」语义用哨兵串。
const PLATFORM_ALL = "__all__";

export default function SRCTargetsPage() {
  const router = useRouter();
  const [targets, setTargets] = React.useState<SRCTarget[]>([]);
  const [total, setTotal] = React.useState(0);
  const [platforms, setPlatforms] = React.useState<SRCPlatform[]>([]);
  const [query, setQuery] = React.useState("");
  const [applied, setApplied] = React.useState("");
  const [platformFilter, setPlatformFilter] = React.useState(PLATFORM_ALL);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);
  const [loading, setLoading] = React.useState(true);
  // 行内「建任务」需要平台上下文;一个目标可能挂多平台,记下它所有挂链平台供对话框选择。
  const [taskTarget, setTaskTarget] = React.useState<SRCTarget | null>(null);
  const [taskPlatform, setTaskPlatform] = React.useState<SRCPlatform | null>(null);
  // 行多选与删除:本页原先没有删除入口,行内删除与批量删除共用一个确认弹层(deleteIds 区分)。
  const [checkedIds, setCheckedIds] = React.useState<Set<number>>(new Set());
  const [deleteIds, setDeleteIds] = React.useState<number[]>([]);
  const [deleteOpen, setDeleteOpen] = React.useState(false);
  const [deleting, setDeleting] = React.useState(false);

  const load = React.useCallback((q: string, platformId: string, p: number, size: number) => {
    setLoading(true);
    api
      .srcTargets({ q, platformId: platformId === PLATFORM_ALL ? undefined : Number(platformId), limit: size, offset: (p - 1) * size })
      .then((r) => {
        setTargets(r.targets);
        setTotal(r.total);
      })
      .catch((e) => toast.error(`加载失败:${(e as Error).message}`))
      .finally(() => setLoading(false));
  }, []);

  React.useEffect(() => {
    api
      .srcPlatforms({ limit: 200 })
      .then((r) => setPlatforms(r.platforms ?? []))
      .catch(() => setPlatforms([]));
  }, []);

  React.useEffect(() => {
    load(applied, platformFilter, page, pageSize);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [applied, platformFilter, page, pageSize]);

  const search = () => {
    setPage(1);
    setApplied(query);
  };

  // 当前页行(服务端分页,targets 即本页)驱动三态全选。
  const pageIds = React.useMemo(() => targets.map((t) => t.id), [targets]);
  const allChecked = pageIds.length > 0 && pageIds.every((id) => checkedIds.has(id));
  const someChecked = pageIds.some((id) => checkedIds.has(id));

  // 行首勾选:v 是显式 checked 语义(true 勾上 / false 取消)。
  const toggleRow = (id: number, checked: boolean) => {
    setCheckedIds((prev) => {
      const next = new Set(prev);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  };

  // 表头全选:indeterminate 状态点击上报 true → 全选当前页;已全选点击上报 false → 清当前页。
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

  // 行内删除与批量删除共用:逐条走单删接口,allSettled 保证一条失败不拖垮其余。
  const confirmDelete = async () => {
    setDeleting(true);
    try {
      const results = await Promise.allSettled(deleteIds.map((id) => api.deleteSRCTarget(id)));
      const ok = results.filter((r) => r.status === "fulfilled").length;
      const failed = results.length - ok;
      if (failed === 0) toast.success(`已删除 ${ok} 项`);
      else toast.warning(`成功 ${ok} 项，失败 ${failed} 项`);
      setCheckedIds(new Set());
      load(applied, platformFilter, page, pageSize);
    } finally {
      setDeleting(false);
      setDeleteOpen(false);
    }
  };

  // 行内建任务:目标只挂一个平台时直接锁定该平台开对话框;多平台时先让用户选平台。
  const startTask = (t: SRCTarget) => {
    if (!t.platform_count) {
      toast.error("该目标还没有挂到任何平台下,请先在平台详情里添加目标");
      return;
    }
    setTaskTarget(t);
    if (t.platform_count === 1) {
      // 单平台:取它唯一挂链的平台(id 从 links 拿最准,但列表页只有计数;
      // 让对话框的 links 加载兜底——这里先取第一个含该目标的平台)。
      resolveSinglePlatform(t);
    } else {
      setTaskPlatform(null);
    }
  };

  const resolveSinglePlatform = async (t: SRCTarget) => {
    try {
      const links = await api.srcTargetLinks(t.id);
      const l = links[0];
      const p = platforms.find((x) => x.id === l?.platform_id);
      setTaskPlatform(p ?? { id: l?.platform_id ?? 0, name: l?.platform_name ?? "", homepage: "", description: "", reward_standard: "", scoring_standard: "", code_of_conduct: "", rules: "", enabled: true, created_at: "", updated_at: "" });
    } catch {
      setTaskPlatform(null);
    }
  };

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div>
          <h1 className="font-semibold text-xl">SRC 模式 · 目标</h1>
          <p className="text-muted-foreground text-sm">
            被测试的对象(厂商/学校/单位)。授权范围与资产挂在「平台×目标」上,同一目标在不同平台下的边界互相独立。
          </p>
        </div>
        <div className="flex items-center gap-2">
          {checkedIds.size > 0 && (
            <Button
              variant="destructive"
              size="sm"
              onClick={() => {
                setDeleteIds(Array.from(checkedIds));
                setDeleteOpen(true);
              }}
            >
              <Trash2Icon className="size-4" />
              删除已选 (<span className="tabular-nums">{checkedIds.size}</span>)
            </Button>
          )}
          <Button size="sm" asChild>
            <Link href="/function/src/platforms">
              <PlusIcon className="size-4" />
              去平台下添加目标
            </Link>
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
                placeholder="搜索目标名 / 描述"
                className="pl-8"
              />
            </div>
            <Button variant="outline" size="sm" onClick={search}>
              搜索
            </Button>
            <Select value={platformFilter} onValueChange={(v) => { setPage(1); setPlatformFilter(v); }}>
              <SelectTrigger className="w-44">
                <SelectValue placeholder="平台" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value={PLATFORM_ALL}>全部平台</SelectItem>
                  {platforms.map((p) => (
                    <SelectItem key={p.id} value={String(p.id)}>
                      {p.name}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            <span className="text-muted-foreground ml-auto text-xs tabular-nums">
              {applied || platformFilter !== PLATFORM_ALL ? `命中 ${total} / ` : ""}共 {total} 个
            </span>
          </div>

          {loading && targets.length === 0 ? (
            <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
          ) : targets.length === 0 ? (
            <div className="text-muted-foreground flex flex-col items-center gap-3 py-16 text-center text-sm">
              <TargetIcon className="size-8 opacity-40" />
              <span>
                {applied || platformFilter !== PLATFORM_ALL
                  ? "没有匹配的目标。换个关键词或清掉筛选试试。"
                  : "还没有目标。到「平台选择」进入某个平台后添加目标——目标是挂在平台下的。"}
              </span>
              {!applied && platformFilter === PLATFORM_ALL && (
                <Button size="sm" variant="outline" asChild>
                  <Link href="/function/src/platforms">
                    <PlusIcon className="size-4" />
                    去平台下添加目标
                  </Link>
                </Button>
              )}
            </div>
          ) : (
            <>
              <Table className="table-fixed">
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    {/* 目标名列不设宽度吃余量;固定列合计 648px(32+160+80+80+80+96+144,含行首勾选列 w-8),小于 ~897px 容器 */}
                    <TableHead className="w-8">
                      <Checkbox
                        checked={allChecked ? true : someChecked ? "indeterminate" : false}
                        onCheckedChange={(v) => toggleAllPage(v === true)}
                        aria-label="全选当前页目标"
                      />
                    </TableHead>
                    <TableHead>目标名称</TableHead>
                    <TableHead className="w-40">关联企业</TableHead>
                    <TableHead className="w-20">平台数</TableHead>
                    <TableHead className="w-20">资产数</TableHead>
                    <TableHead className="w-20">成果数</TableHead>
                    <TableHead className="w-24">状态</TableHead>
                    <TableHead className="w-36">更新时间</TableHead>
                    <TableHead className="w-24">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {targets.map((t) => (
                    <TableRow key={t.id} className={checkedIds.has(t.id) ? "bg-muted/40" : undefined}>
                      <TableCell className="w-8" onClick={(e) => e.stopPropagation()}>
                        <Checkbox
                          checked={checkedIds.has(t.id)}
                          onCheckedChange={(v) => toggleRow(t.id, v === true)}
                          aria-label={`选择目标 ${t.name}`}
                        />
                      </TableCell>
                      <TableCell className="min-w-0">
                        <Link
                          href={`/function/src/targets/detail?id=${t.id}`}
                          className="block w-full truncate text-left font-medium hover:underline"
                          title={t.name}
                        >
                          {t.name}
                        </Link>
                        {t.description && (
                          <span className="text-muted-foreground block truncate text-xs" title={t.description}>
                            {t.description}
                          </span>
                        )}
                      </TableCell>
                      <TableCell className="text-muted-foreground truncate text-xs" title={t.company_name ?? ""}>
                        {t.company_name || "未关联企业"}
                      </TableCell>
                      <TableCell className="tabular-nums">{t.platform_count ?? 0}</TableCell>
                      <TableCell className="tabular-nums">{t.asset_count ?? 0}</TableCell>
                      <TableCell className="tabular-nums">{t.reward_count ?? 0}</TableCell>
                      <TableCell>
                        <Badge
                          variant="outline"
                          className={
                            t.enabled
                              ? "bg-emerald-500/15 text-emerald-600 border-emerald-500/30"
                              : "bg-muted text-muted-foreground"
                          }
                        >
                          {t.enabled ? "启用" : "停用"}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs">
                        {new Date(t.updated_at).toLocaleString()}
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center gap-0.5">
                          <Button variant="ghost" size="icon-xs" onClick={() => startTask(t)} title="对该目标建 SRC 任务">
                            <RocketIcon className="size-3.5" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon-xs"
                            onClick={() => {
                              setDeleteIds([t.id]);
                              setDeleteOpen(true);
                            }}
                            title="删除目标(含挂链与资产关联)"
                          >
                            <Trash2Icon className="text-muted-foreground size-3.5" />
                          </Button>
                          <Button variant="ghost" size="icon-xs" asChild title="进入目标">
                            <Link href={`/function/src/targets/detail?id=${t.id}`} aria-label="进入目标">
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

      {/* 多平台目标:让用户选平台;单平台已在 startTask 里锁定。 */}
      <PlatformPickDialog
        open={!!taskTarget && !taskPlatform}
        targetName={taskTarget?.name ?? ""}
        platforms={platforms}
        onCancel={() => setTaskTarget(null)}
        onPick={(p) => setTaskPlatform(p)}
      />
      {taskTarget && taskPlatform && (
        <CreateSRCTaskDialog
          open
          onOpenChange={(o) => {
            if (!o) {
              setTaskTarget(null);
              setTaskPlatform(null);
            }
          }}
          platform={taskPlatform}
          target={taskTarget}
        />
      )}

      {/* 行内/批量删除共用确认:兄弟节点渲染,不进三元链。级联后果(挂链与资产关联)写明。 */}
      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>确认删除目标</AlertDialogTitle>
            <AlertDialogDescription>
              将永久删除选中的 <span className="font-semibold tabular-nums">{deleteIds.length}</span>{" "}
              个目标，其平台挂链与资产关联将一并清理。此操作不可撤销。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleting}>取消</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault();
                void confirmDelete();
              }}
              disabled={deleting}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {deleting ? "删除中…" : "确认删除"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

// PlatformPickDialog 多平台目标的建任务前置选择:授权范围按「平台×目标」存,
// 建任务前必须确定是哪个平台下的边界,不能默认第一个。
function PlatformPickDialog({
  open,
  targetName,
  platforms,
  onCancel,
  onPick,
}: {
  open: boolean;
  targetName: string;
  platforms: SRCPlatform[];
  onCancel: () => void;
  onPick: (p: SRCPlatform) => void;
}) {
  const [value, setValue] = React.useState("__none__");
  React.useEffect(() => {
    if (open) setValue("__none__");
  }, [open]);
  const picked = platforms.find((p) => String(p.id) === value);
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onCancel()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>选择平台</DialogTitle>
          <DialogDescription className="text-xs">
            「{targetName}」挂在多个平台下,各平台下的授权范围不同——请选择本次任务按哪个平台的边界执行。
          </DialogDescription>
        </DialogHeader>
        <Select value={value} onValueChange={setValue}>
          <SelectTrigger className="w-full">
            <SelectValue placeholder="选择平台" />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              <SelectItem value="__none__">请选择平台…</SelectItem>
              {platforms.map((p) => (
                <SelectItem key={p.id} value={String(p.id)}>
                  {p.name}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
        <DialogFooter>
          <Button variant="outline" onClick={onCancel}>
            取消
          </Button>
          <Button disabled={!picked} onClick={() => picked && onPick(picked)}>
            下一步
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
