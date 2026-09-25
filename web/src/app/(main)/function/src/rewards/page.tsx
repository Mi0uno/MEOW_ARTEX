"use client";

// SRC 模式 · 成果产出:跟踪任务在 SRC 模式下产出的可提交成果。
// 任务产出漏洞时自动建成果记录(待提交),这里维护提交状态与平台反馈。

import * as React from "react";

import Link from "next/link";
import {
  AwardIcon,
  ChevronDownIcon,
  ChevronUpIcon,
  PencilIcon,
  SearchIcon,
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
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { TablePagination } from "@/components/table-pagination";
import { api } from "@/lib/api";
import type { Company, SRCOverview, SRCPlatform, SRCReward, SRCTarget } from "@/lib/types";

const PAGE_SIZE = 50;

const STATUS_META: Record<string, { label: string; badge: string }> = {
  pending: { label: "待提交", badge: "bg-muted text-muted-foreground" },
  submitted: { label: "已提交", badge: "bg-blue-500/15 text-blue-600 border-blue-500/30" },
  accepted: { label: "已收录", badge: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30" },
  rejected: { label: "已驳回", badge: "bg-red-500/15 text-red-600 border-red-500/30" },
  duplicate: { label: "重复", badge: "bg-amber-500/15 text-amber-600 border-amber-500/30" },
};

const SEVERITY_META: Record<string, string> = {
  critical: "bg-red-500/15 text-red-600 border-red-500/30",
  high: "bg-orange-500/15 text-orange-600 border-orange-500/30",
  medium: "bg-amber-500/15 text-amber-600 border-amber-500/30",
  low: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30",
};

function fmtTime(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

export default function SRCRewardsPage() {
  const [rewards, setRewards] = React.useState<SRCReward[]>([]);
  const [companies, setCompanies] = React.useState<Company[]>([]);
  const [overview, setOverview] = React.useState<SRCOverview | null>(null);
  const [total, setTotal] = React.useState(0);
  const [counts, setCounts] = React.useState<Record<string, number>>({});
  const [query, setQuery] = React.useState("");
  const [companyFilter, setCompanyFilter] = React.useState("all");
  const [platformFilter, setPlatformFilter] = React.useState("all");
  const [targetFilter, setTargetFilter] = React.useState("all");
  const [statusFilter, setStatusFilter] = React.useState("all");
  const [severity, setSeverity] = React.useState("all");
  const [sort, setSort] = React.useState("created");
  const [sortDesc, setSortDesc] = React.useState(true);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);
  const [loading, setLoading] = React.useState(true);
  const [editing, setEditing] = React.useState<SRCReward | null>(null);
  const [platforms, setPlatforms] = React.useState<SRCPlatform[]>([]);
  const [targets, setTargets] = React.useState<SRCTarget[]>([]);
  // 行多选与批量删除:三态全选按当前页行计算。
  const [checkedIds, setCheckedIds] = React.useState<Set<number>>(new Set());
  const [batchConfirmOpen, setBatchConfirmOpen] = React.useState(false);
  const [batchDeleting, setBatchDeleting] = React.useState(false);
  // 平台筛选变化时精确拉该平台下的目标列表(比按计数过滤准确)
  React.useEffect(() => {
    if (platformFilter === "all") {
      api
        .srcTargets({ limit: 200 })
        .then((r) => setTargets(r.targets ?? []))
        .catch(() => setTargets([]));
      return;
    }
    api
      .srcPlatformTargets(Number(platformFilter), { limit: 200 })
      .then((r) => setTargets(r.targets ?? []))
      .catch(() => setTargets([]));
  }, [platformFilter]);

  const load = React.useCallback(
    (q: string, cid: string, pid: string, tid: string, st: string, sev: string, srt: string, p: number, size: number) => {
      setLoading(true);
      api
        .srcRewards({
          q,
          companyId: cid === "all" ? undefined : Number(cid),
          platformId: pid === "all" ? undefined : Number(pid),
          targetId: tid === "all" ? undefined : Number(tid),
          status: st === "all" ? "" : st,
          severity: sev === "all" ? "" : sev,
          sort: srt,
          limit: size,
          offset: (p - 1) * size,
        })
        .then((r) => {
          setRewards(r.rewards);
          setTotal(r.total);
          setCounts(r.counts);
        })
        .catch((e) => toast.error(`加载失败:${(e as Error).message}`))
        .finally(() => setLoading(false));
    },
    [],
  );

  React.useEffect(() => {
    api
      .companies()
      .then(setCompanies)
      .catch(() => undefined);
    // 平台/目标筛选数据:成果现在带平台与目标归属,筛选器要跟上(「补天这个月收录多少」一屏可答)。
    api
      .srcPlatforms({ limit: 200 })
      .then((r) => setPlatforms(r.platforms ?? []))
      .catch(() => undefined);
    api
      .srcTargets({ limit: 200 })
      .then((r) => setTargets(r.targets ?? []))
      .catch(() => setTargets([]));
    api
      .srcOverview()
      .then((r) =>
        // 后端可能返回 null 数组(Go nil slice),归一化后再用,避免读 .length 崩页。
        setOverview({
          total: r?.total ?? 0,
          by_status: r?.by_status ?? {},
          by_company: Array.isArray(r?.by_company) ? r.by_company : [],
        }),
      )
      .catch(() => undefined);
  }, []);

  React.useEffect(() => {
    load(query, companyFilter, platformFilter, targetFilter, statusFilter, severity, sort, page, pageSize);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [companyFilter, platformFilter, targetFilter, statusFilter, severity, sort, page, pageSize]);

  const search = () => {
    setPage(1);
    load(query, companyFilter, platformFilter, targetFilter, statusFilter, severity, sort, 1, pageSize);
  };
  const reload = () => {
    load(query, companyFilter, platformFilter, targetFilter, statusFilter, severity, sort, page, pageSize);
    api
      .srcOverview()
      .then(setOverview)
      .catch(() => undefined);
  };

  const remove = async (r: SRCReward) => {
    if (!window.confirm(`删除成果「${r.title}」?`)) return;
    try {
      await api.deleteSRCReward(r.id);
      toast.success("已删除");
      reload();
    } catch (e) {
      toast.error(`删除失败:${(e as Error).message}`);
    }
  };

  // 当前页行(服务端分页,rewards 即本页)驱动三态全选。
  const pageIds = React.useMemo(() => rewards.map((r) => r.id), [rewards]);
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

  // 批量删除:逐条走单删接口,allSettled 保证一条失败不拖垮其余。
  const confirmBatchDelete = async () => {
    const ids = Array.from(checkedIds);
    setBatchDeleting(true);
    try {
      const results = await Promise.allSettled(ids.map((id) => api.deleteSRCReward(id)));
      const ok = results.filter((r) => r.status === "fulfilled").length;
      const failed = results.length - ok;
      if (failed === 0) toast.success(`已删除 ${ok} 项`);
      else toast.warning(`成功 ${ok} 项，失败 ${failed} 项`);
      setCheckedIds(new Set());
      reload();
    } finally {
      setBatchDeleting(false);
      setBatchConfirmOpen(false);
    }
  };

  const countOf = (k: string) => (k === "all" ? Object.values(counts).reduce((a, b) => a + b, 0) : (counts[k] ?? 0));

  const SortHead = ({ field, children }: { field: string; children: React.ReactNode }) => (
    <TableHead>
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
      <div className="mb-4">
        <h1 className="font-semibold text-xl">SRC 模式 · 成果产出</h1>
        <p className="text-muted-foreground text-sm">
          SRC 任务产出的可提交成果:任务打出漏洞时自动建记录,这里跟踪提交状态与平台反馈
        </p>
      </div>

      {overview && (
        <div className="mb-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <Card>
            <CardContent className="pt-4">
              <p className="text-muted-foreground text-xs">成果总数</p>
              <p className="text-2xl font-semibold tabular-nums">{overview.total}</p>
            </CardContent>
          </Card>
          <Card>
            <CardContent className="pt-4">
              <p className="text-muted-foreground text-xs">待提交</p>
              <p className="text-2xl font-semibold tabular-nums">{overview.by_status.pending ?? 0}</p>
            </CardContent>
          </Card>
          <Card>
            <CardContent className="pt-4">
              <p className="text-muted-foreground text-xs">已收录</p>
              <p className="text-2xl font-semibold tabular-nums text-emerald-600">
                {overview.by_status.accepted ?? 0}
              </p>
            </CardContent>
          </Card>
          <Card>
            <CardContent className="pt-4">
              <p className="text-muted-foreground text-xs">覆盖企业</p>
              <p className="text-2xl font-semibold tabular-nums">{overview.by_company.length}</p>
            </CardContent>
          </Card>
        </div>
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
                placeholder="搜索标题 / 漏洞类别"
                className="pl-8"
              />
            </div>
            <Button variant="outline" size="sm" onClick={search}>
              搜索
            </Button>
            <Select
              value={companyFilter}
              onValueChange={(v) => {
                setPage(1);
                setCompanyFilter(v);
              }}
            >
              <SelectTrigger className="w-48">
                <SelectValue placeholder="企业" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="all">全部企业</SelectItem>
                  {companies.map((c) => (
                    <SelectItem key={c.id} value={String(c.id)}>
                      {c.name}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            <Select
              value={platformFilter}
              onValueChange={(v) => {
                setPage(1);
                setPlatformFilter(v);
                // 平台切换后目标选项随之失效:目标归属按平台×目标,切平台要重选目标。
                setTargetFilter("all");
              }}
            >
              <SelectTrigger className="w-44">
                <SelectValue placeholder="平台" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="all">全部平台</SelectItem>
                  {platforms.map((p) => (
                    <SelectItem key={p.id} value={String(p.id)}>
                      {p.name}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            <Select value={targetFilter} onValueChange={(v) => { setPage(1); setTargetFilter(v); }}>
              <SelectTrigger className="w-40">
                <SelectValue placeholder="目标" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="all">全部目标</SelectItem>
                  {targets.map((t) => (
                    <SelectItem key={t.id} value={String(t.id)}>
                      {t.name}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
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
              value={statusFilter}
              onValueChange={(v) => {
                setPage(1);
                setStatusFilter(v);
              }}
            >
              <SelectTrigger className="w-36">
                <SelectValue placeholder="提交状态" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="all">全部状态 {countOf("all")}</SelectItem>
                  {Object.entries(STATUS_META).map(([k, m]) => (
                    <SelectItem key={k} value={k}>
                      {m.label} {counts[k] ?? 0}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            <div className="ml-auto flex items-center gap-2">
              {checkedIds.size > 0 && (
                <Button variant="destructive" size="sm" onClick={() => setBatchConfirmOpen(true)}>
                  <Trash2Icon className="size-3.5" />
                  删除已选 (<span className="tabular-nums">{checkedIds.size}</span>)
                </Button>
              )}
              <span className="text-muted-foreground text-xs tabular-nums">共 {total} 条</span>
            </div>
          </div>

          {loading && rewards.length === 0 ? (
            <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
          ) : rewards.length === 0 ? (
            <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
              <AwardIcon className="size-8 opacity-40" />
              <span>
                还没有 SRC 成果。在「企业模式」选企业建 SRC 任务,任务打出漏洞时会自动在这里生成待提交成果。
              </span>
            </div>
          ) : (
            <>
              <Table>
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    <TableHead className="w-8">
                      <Checkbox
                        checked={allChecked ? true : someChecked ? "indeterminate" : false}
                        onCheckedChange={(v) => toggleAllPage(v === true)}
                        aria-label="全选当前页成果"
                      />
                    </TableHead>
                    <TableHead>成果标题</TableHead>
                    <TableHead className="w-40">企业</TableHead>
                    <TableHead className="w-20">严重度</TableHead>
                    <TableHead className="w-32">漏洞类别</TableHead>
                    <TableHead className="w-24">状态</TableHead>
                    <TableHead className="w-32">来源任务</TableHead>
                    <SortHead field="created">产出时间</SortHead>
                    <TableHead className="w-32">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {rewards.map((r) => {
                    const meta = STATUS_META[r.status] ?? STATUS_META.pending;
                    return (
                      <TableRow key={r.id} className={checkedIds.has(r.id) ? "bg-muted/40" : undefined}>
                        <TableCell className="w-8" onClick={(e) => e.stopPropagation()}>
                          <Checkbox
                            checked={checkedIds.has(r.id)}
                            onCheckedChange={(v) => toggleRow(r.id, v === true)}
                            aria-label={`选择成果 ${r.title}`}
                          />
                        </TableCell>
                        <TableCell className="max-w-96">
                          <span className="truncate font-medium" title={r.title}>
                            {r.title}
                          </span>
                        </TableCell>
                        <TableCell className="text-muted-foreground text-xs">
                          {r.company_name || (r.company_id ? `企业 #${r.company_id}` : "—")}
                        </TableCell>
                        <TableCell>
                          {r.severity ? (
                            <Badge variant="outline" className={SEVERITY_META[r.severity] ?? ""}>
                              {r.severity}
                            </Badge>
                          ) : (
                            "—"
                          )}
                        </TableCell>
                        <TableCell className="text-muted-foreground text-xs">{r.vuln_class || "—"}</TableCell>
                        <TableCell>
                          <Badge variant="outline" className={meta.badge}>
                            {meta.label}
                          </Badge>
                        </TableCell>
                        <TableCell className="text-xs">
                          {r.task_id ? (
                            <Link href={`/function/tasks/detail?id=${r.task_id}`} className="text-blue-600 hover:underline">
                              #{r.task_id}
                            </Link>
                          ) : (
                            "—"
                          )}
                        </TableCell>
                        <TableCell className="text-muted-foreground text-xs">{fmtTime(r.created_at)}</TableCell>
                        <TableCell>
                          <div className="flex items-center gap-1">
                            <Button variant="ghost" size="sm" onClick={() => setEditing(r)}>
                              <PencilIcon className="size-3.5" />
                              提交
                            </Button>
                            <Button variant="ghost" size="sm" onClick={() => void remove(r)}>
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

      {/* 批量删除确认:兄弟节点渲染,不进三元链。 */}
      <AlertDialog open={batchConfirmOpen} onOpenChange={setBatchConfirmOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>确认批量删除成果</AlertDialogTitle>
            <AlertDialogDescription>
              将永久删除选中的 <span className="font-semibold tabular-nums">{checkedIds.size}</span>{" "}
              条成果记录及其提交状态、平台反馈，此操作不可撤销。
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

      <RewardDialog
        reward={editing}
        open={!!editing}
        onOpenChange={(o) => !o && setEditing(null)}
        onSaved={() => {
          setEditing(null);
          reload();
        }}
      />
    </div>
  );
}

// RewardDialog 更新成果的提交状态与平台反馈。
function RewardDialog({
  reward,
  open,
  onOpenChange,
  onSaved,
}: {
  reward: SRCReward | null;
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onSaved: () => void;
}) {
  const [form, setForm] = React.useState<Partial<SRCReward>>({});
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    if (open && reward) setForm(reward);
  }, [open, reward]);

  const set = (k: keyof SRCReward, v: unknown) => setForm((f) => ({ ...f, [k]: v }));

  const submit = async () => {
    if (!reward) return;
    setBusy(true);
    try {
      await api.updateSRCReward(reward.id, form);
      toast.success("已更新");
      onSaved();
    } catch (e) {
      toast.error(`更新失败:${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>提交状态 · {reward?.title}</DialogTitle>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid grid-cols-2 gap-3">
            <div className="grid gap-1.5">
              <Label>提交状态</Label>
              <Select value={String(form.status ?? "pending")} onValueChange={(v) => set("status", v)}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {Object.entries(STATUS_META).map(([k, m]) => (
                      <SelectItem key={k} value={k}>
                        {m.label}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label>严重度</Label>
              <Select value={String(form.severity ?? "medium")} onValueChange={(v) => set("severity", v)}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    <SelectItem value="critical">严重</SelectItem>
                    <SelectItem value="high">高</SelectItem>
                    <SelectItem value="medium">中</SelectItem>
                    <SelectItem value="low">低</SelectItem>
                  </SelectGroup>
                </SelectContent>
              </Select>
            </div>
          </div>
          <div className="grid gap-1.5">
            <Label>平台侧编号 / 链接</Label>
            <Input
              value={String(form.platform_ref ?? "")}
              onChange={(e) => set("platform_ref", e.target.value)}
              placeholder="平台漏洞编号或链接"
            />
          </div>
          <div className="grid gap-1.5">
            <Label>平台反馈 / 赏金说明</Label>
            <Textarea
              value={String(form.feedback ?? "")}
              onChange={(e) => set("feedback", e.target.value)}
              rows={4}
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button disabled={busy} onClick={() => void submit()}>
            {busy ? "保存中…" : "保存"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
