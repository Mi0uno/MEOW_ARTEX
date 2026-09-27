"use client";

// 报告中心:集中管理平台产出的全部报告(任务总结/漏洞报告/自定义)。
// 报告由平台自动汇聚(任务终态归档 / 漏洞详细报告写入即入库),并与信息搜集的
// 资产主体(企业)联动——**进入页面先选资产主体,再看该主体名下的报告与分类**;
// 归属经「报告 → 任务 → 企业」链推导(漏洞报告经锚定资产的企业兜底)。
//
// 选择资产主体用整页选择(CompanyPickerPage)而不是下拉框:企业多了下拉要翻找,
// 也看不到各企业的报告规模。选择页顶部固定提供「全部资产主体」「未关联企业」
// 两个出口——历史报告大多没有企业归属,必须保证用户能一键看到全部数据。
//
// 资产主体存在 URL(?company=all|unassigned|<id>)里,刷新与浏览器后退都能保持视图。

import * as React from "react";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";

import {
  Building2Icon,
  DownloadIcon,
  EyeIcon,
  FileTextIcon,
  LayoutTemplateIcon,
  PlusIcon,
  SearchIcon,
  Trash2Icon,
} from "lucide-react";
import { toast } from "sonner";

import { CompanyPickerPage } from "@/components/company-picker-page";
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
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { LLMProfile, ReportCompanyCount, ReportSummary, ReportTemplate } from "@/lib/types";

const PAGE_SIZE = 50;

const KIND_META: Record<string, { label: string; badgeClass: string }> = {
  task: { label: "任务报告", badgeClass: "bg-blue-500/15 text-blue-600 border-blue-500/30" },
  finding: { label: "漏洞报告", badgeClass: "bg-red-500/15 text-red-600 border-red-500/30" },
  custom: { label: "自定义", badgeClass: "bg-muted text-muted-foreground" },
};

function fmtTime(iso?: string | null): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString();
}

// relationText 列表「关联」列:优先显示来源任务,其次是来源发现。
function relationText(r: ReportSummary): string {
  if (r.task_id) return `任务 #${r.task_id}`;
  if (r.finding_id) return `发现 #${r.finding_id}`;
  return "—";
}

// useSearchParams must sit under a Suspense boundary for static export.
export default function ReportsCenterPage() {
  return (
    <React.Suspense fallback={null}>
      <ReportsCenterInner />
    </React.Suspense>
  );
}

function ReportsCenterInner() {
  const searchParams = useSearchParams();
  // 侧边栏「报告中心」分组的分类子项经 ?kind= 预选页签;无参数 = 全部。
  const kindParam = searchParams.get("kind");
  const initialKind = kindParam === "task" || kindParam === "finding" || kindParam === "custom" ? kindParam : "all";
  // ?company= 是资产主体:缺省 = 还没选(显示选择页);all=全部;unassigned=未关联;<数字>=该企业。
  const companyParam = searchParams.get("company");
  const router = useRouter();
  const [reports, setReports] = React.useState<ReportSummary[]>([]);
  const [total, setTotal] = React.useState(0);
  const [counts, setCounts] = React.useState<Record<string, number>>({});
  const [kind, setKind] = React.useState<string>(initialKind);
  const [query, setQuery] = React.useState("");
  // 严重度筛选:报告自身没有等级列,后端经 findings 关联推导(漏洞报告=该漏洞等级,
  // 任务报告=任务下任一发现达标)。空 = 不限。
  const [severity, setSeverity] = React.useState("all");
  const [loading, setLoading] = React.useState(true);
  const [offset, setOffset] = React.useState(0);
  const [creating, setCreating] = React.useState(false);

  // 行多选(选中集跨筛选/翻页保留):全选只作用于当前页;批量执行时已不存在的
  // id 自然失败并计入失败数,无需专门收敛。
  const [checkedIds, setCheckedIds] = React.useState<Set<number>>(new Set());
  const [deleting, setDeleting] = React.useState(false);
  const [confirmOpen, setConfirmOpen] = React.useState(false);
  // 批量导出对话框:与批量删除同一勾选集合(导出不清空勾选)。
  const [exportOpen, setExportOpen] = React.useState(false);

  const toggleChecked = (id: number, checked: boolean) => {
    setCheckedIds((prev) => {
      const next = new Set(prev);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  };

  const allSelected = reports.length > 0 && reports.every((r) => checkedIds.has(r.id));
  const someSelected = !allSelected && reports.some((r) => checkedIds.has(r.id));

  // 资产主体:null=还没选(显示选择页);-1=未关联企业桶;0=全部;>0=该企业。
  const [unassigned, setUnassigned] = React.useState(0);
  const [assigned, setAssigned] = React.useState(0);
  const [companies, setCompanies] = React.useState<{ id: number; name: string }[]>([]);

  // companyID 由 URL 派生:URL 是唯一事实来源(刷新/后退/分享链接都一致)。
  const companyID: number | null = React.useMemo(() => {
    if (companyParam == null) return null; // 还没选 → 显示选择页
    if (companyParam === "all") return 0;
    if (companyParam === "unassigned") return -1;
    return /^\d+$/.test(companyParam) ? Number(companyParam) : null;
  }, [companyParam]);

  React.useEffect(() => {
    api
      .reportCompanies()
      .then((r) => {
        setUnassigned(r.unassigned);
        setAssigned(r.assigned);
        setCompanies(r.companies.map((c) => ({ id: c.company_id, name: c.name })));
      })
      .catch((e) => toast.error(`加载企业失败:${(e as Error).message}`));
  }, []);

  // switchCompany 切换资产主体并写进 URL(保留当前分类页签)。
  const switchCompany = React.useCallback(
    (v: string) => {
      const p = new URLSearchParams(searchParams.toString());
      p.set("company", v);
      router.push(`/function/reports?${p.toString()}`);
    },
    [router, searchParams],
  );

  const load = React.useCallback((search: string, k: string, off: number, cid: number | null, sev: string) => {
    setLoading(true);
    api
      .listReports({
        kind: k === "all" ? "" : k,
        q: search,
        // cid 为 0(全部)时不传 company_id,即不限企业。
        companyId: cid != null && cid > 0 ? cid : undefined,
        severity: sev === "all" ? undefined : sev,
        limit: PAGE_SIZE,
        offset: off,
      })
      .then((r) => {
        setReports(r.reports);
        setTotal(r.total);
        setCounts(r.counts);
      })
      .catch((e) => toast.error(`加载报告失败:${(e as Error).message}`))
      .finally(() => setLoading(false));
  }, []);

  // 「未关联企业」桶在服务端过滤:company_id=-1 走独立的未归属查询。
  const loadUnassigned = React.useCallback((search: string, k: string, off: number, sev: string) => {
    setLoading(true);
    api
      .listUnassignedReports({ kind: k === "all" ? "" : k, q: search, severity: sev === "all" ? undefined : sev, limit: PAGE_SIZE, offset: off })
      .then((r) => {
        setReports(r.reports);
        setTotal(r.total);
        setCounts(r.counts);
      })
      .catch((e) => toast.error(`加载报告失败:${(e as Error).message}`))
      .finally(() => setLoading(false));
  }, []);

  const queryRef = React.useRef(query);
  queryRef.current = query;

  // 侧边栏分类子项(?kind=)软导航切换时同步页签。
  React.useEffect(() => {
    setKind(initialKind);
  }, [initialKind]);

  React.useEffect(() => {
    // companyID=null 是「还没选」——此时渲染的是选择页,不该发列表请求;
    // 但 companyID=0(全部)必须照常加载,不能把「全部」误当成「没选」。
    if (companyID === null) return;
    if (companyID === -1) {
      loadUnassigned(queryRef.current, kind, offset, severity);
      return;
    }
    load(queryRef.current, kind, offset, companyID, severity);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- severity/query 经 ref/state 直读,见下
  }, [load, loadUnassigned, kind, offset, companyID, severity]);

  const search = () => {
    setOffset(0);
    if (companyID === -1) {
      loadUnassigned(query, kind, 0, severity);
      return;
    }
    load(query, kind, 0, companyID, severity);
  };

  const remove = async (r: ReportSummary) => {
    if (!window.confirm(`删除报告「${r.title}」?该操作不可恢复。`)) return;
    try {
      await api.deleteReport(r.id);
      toast.success("报告已删除");
      if (companyID === -1) loadUnassigned(query, kind, offset, severity);
      else load(query, kind, offset, companyID, severity);
    } catch (e) {
      toast.error(`删除失败:${(e as Error).message}`);
    }
  };

  // 批量删除已选报告:并行逐个调删除接口,统计成功/失败后清空选择并刷新列表
  // (刷新走该页既有的 load / loadUnassigned,与单行删除一致)。
  const batchDelete = async () => {
    const ids = [...checkedIds];
    setDeleting(true);
    try {
      const rs = await Promise.allSettled(ids.map((id) => api.deleteReport(id)));
      const ok = rs.filter((r) => r.status === "fulfilled").length;
      const fail = rs.length - ok;
      if (fail === 0) toast.success(`已删除 ${ok} 项`);
      else toast.warning(`成功 ${ok} 项，失败 ${fail} 项`);
      setCheckedIds(new Set());
      setConfirmOpen(false);
      if (companyID === -1) loadUnassigned(query, kind, offset, severity);
      else load(query, kind, offset, companyID, severity);
    } finally {
      setDeleting(false);
    }
  };

  const countOf = (k: string) => (k === "all" ? Object.values(counts).reduce((a, b) => a + b, 0) : (counts[k] ?? 0));

  // 选择资产主体页:作为进入页(未选资产主体时显示)。
  // 顶部固定提供「全部资产主体」「未关联企业」——历史报告大多没有企业归属,
  // 这两个出口保证用户永远能看到全部数据,不会被筛选藏起来。
  if (companyID === null) {
    return (
      <CompanyPickerPage
        title="报告中心 · 选择资产主体"
        description="选择资产主体查看它名下的报告;企业归属经「报告 → 任务 → 企业」链推导,与信息搜集的资产图同源。"
        onPick={(pick) => {
          if (pick.kind === "all") switchCompany("all");
          else if (pick.kind === "unassigned") switchCompany("unassigned");
          else switchCompany(String(pick.company.id));
        }}
        emptyHint="还没有企业。先在「信息搜集 → 资产」里创建企业并录入资产范围,或直接选上方「全部资产主体」浏览全部报告。"
        countLabel="报告数"
        synthetic={{
          selected: null,
          allCount: assigned + unassigned,
          unassignedCount: unassigned,
          unit: "份报告",
        }}
      />
    );
  }

  const currentCompanyName =
    companyID === 0
      ? "全部资产主体"
      : companyID === -1
        ? "未关联企业"
        : (companies.find((c) => c.id === companyID)?.name ?? `企业 #${companyID}`);

  // 列表主体三态:加载中 / 空 / 表格。空态文案区分「该主体确实没有报告」与搜索无命中。
  let body: React.ReactNode;
  if (loading && reports.length === 0) {
    body = <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>;
  } else if (reports.length === 0) {
    body = (
      <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
        <FileTextIcon className="size-8 opacity-40" />
        <span>
          {query
            ? "没有匹配的报告"
            : companyID === -1
              ? "没有未关联企业的报告。"
              : companyID === 0
                ? "还没有报告。任务终态会自动归档总结报告,漏洞详细报告写入即入库。"
                : "该资产主体名下还没有报告。可切换到「全部资产主体」查看其他报告。"}
        </span>
      </div>
    );
  } else {
    body = (
      <Table>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead className="w-8">
              <Checkbox
                checked={allSelected ? true : someSelected ? "indeterminate" : false}
                onCheckedChange={(v) =>
                  setCheckedIds((prev) => {
                    const next = new Set(prev);
                    for (const r of reports) {
                      if (v === true) next.add(r.id);
                      else next.delete(r.id);
                    }
                    return next;
                  })
                }
                aria-label="全选本页"
              />
            </TableHead>
            <TableHead className="w-24">分类</TableHead>
            {/* 标题列不设宽度:table-fixed 下自动吃掉余量(设 w-full 会挤掉后面的列)。 */}
            <TableHead>标题</TableHead>
            <TableHead className="w-72">摘要</TableHead>
            <TableHead className="w-24">关联</TableHead>
            <TableHead className="w-40">更新时间</TableHead>
            <TableHead className="w-28">操作</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {reports.map((r) => {
            const meta = KIND_META[r.kind] ?? KIND_META.custom;
            return (
              <TableRow key={r.id}>
                <TableCell className="w-8" onClick={(e) => e.stopPropagation()}>
                  <Checkbox
                    checked={checkedIds.has(r.id)}
                    onCheckedChange={(v) => toggleChecked(r.id, v === true)}
                    aria-label={`选择 ${r.title}`}
                  />
                </TableCell>
                <TableCell>
                  <Badge variant="outline" className={meta.badgeClass}>
                    {meta.label}
                  </Badge>
                </TableCell>
                <TableCell className="min-w-0">
                  <Link
                    href={`/function/reports/detail?id=${r.id}`}
                    className="block w-full truncate font-medium hover:underline"
                    title={r.title}
                  >
                    {r.title}
                  </Link>
                </TableCell>
                <TableCell className="text-muted-foreground max-w-72 truncate text-xs" title={r.summary || undefined}>
                  {r.summary || "—"}
                </TableCell>
                <TableCell className="text-muted-foreground text-xs">{relationText(r)}</TableCell>
                <TableCell className="text-muted-foreground text-xs">{fmtTime(r.updated_at)}</TableCell>
                <TableCell>
                  <div className="flex items-center gap-1">
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => router.push(`/function/reports/detail?id=${r.id}`)}
                    >
                      <EyeIcon className="size-4" />
                      查看
                    </Button>
                    <Button variant="ghost" size="sm" onClick={() => void remove(r)} title="删除">
                      <Trash2Icon className="text-muted-foreground size-4" />
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    );
  }

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div>
          <h1 className="font-semibold text-xl">报告中心</h1>
          <p className="text-muted-foreground text-sm">
            汇集平台产出的全部报告(任务总结 / 漏洞报告 / 自定义) · 资产主体:{currentCompanyName}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm" onClick={() => router.push("/function/reports")}>
            <Building2Icon className="size-4" />
            切换资产主体
          </Button>
          <Button variant="outline" size="sm" asChild>
            <Link href="/function/reports/templates">
              <LayoutTemplateIcon className="size-4" />
              模板管理
            </Link>
          </Button>
          <Button size="sm" onClick={() => setCreating(true)}>
            <PlusIcon className="size-4" />
            新建报告
          </Button>
        </div>
      </div>

      <Card>
        <CardContent className="pt-4">
          <div className="mb-3 flex flex-wrap items-center gap-2">
            <div className="relative">
              <SearchIcon className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && search()}
                placeholder="搜索标题 / 摘要 / 正文"
                className="w-64 pl-8"
              />
            </div>
            <Button variant="outline" size="sm" onClick={search}>
              搜索
            </Button>
            {/* 资产主体快速切换:已选主体后的补充手段(主入口是上面的选择页)。
                含「全部」与「未关联企业」两个出口,任何时候都能看到全部数据。 */}
            <Select
              value={companyID === 0 ? "all" : companyID === -1 ? "unassigned" : String(companyID)}
              onValueChange={switchCompany}
            >
              <SelectTrigger className="w-44">
                <SelectValue placeholder="资产主体" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="all">全部资产主体</SelectItem>
                  <SelectItem value="unassigned" disabled={unassigned === 0}>
                    未关联企业{unassigned > 0 ? ` (${unassigned})` : ""}
                  </SelectItem>
                  {companies.map((c) => (
                    <SelectItem key={c.id} value={String(c.id)}>
                      {c.name}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            {/* 严重度筛选:漏洞报告按该漏洞等级,任务报告按任务下任一发现的等级;
                自定义报告没有严重度,选了等级即被排除(后端语义)。 */}
            <Select
              value={severity}
              onValueChange={(v) => {
                setOffset(0);
                setSeverity(v);
              }}
            >
              <SelectTrigger className="w-36">
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
            <Tabs
              value={kind}
              onValueChange={(v) => {
                setOffset(0);
                setKind(v);
              }}
            >
              <TabsList>
                <TabsTrigger value="all">全部 {countOf("all")}</TabsTrigger>
                <TabsTrigger value="task">任务报告 {counts.task ?? 0}</TabsTrigger>
                <TabsTrigger value="finding">漏洞报告 {counts.finding ?? 0}</TabsTrigger>
                <TabsTrigger value="custom">自定义 {counts.custom ?? 0}</TabsTrigger>
              </TabsList>
            </Tabs>
            {checkedIds.size > 0 && (
              <>
                {/* 导出在删除左边:两者同源勾选集,导出是无损操作放外侧不抢眼。 */}
                <Button variant="outline" size="sm" onClick={() => setExportOpen(true)}>
                  <DownloadIcon className="size-4" />
                  导出已选 ({checkedIds.size})
                </Button>
                <Button variant="destructive" size="sm" disabled={deleting} onClick={() => setConfirmOpen(true)}>
                  <Trash2Icon className="size-4" />
                  删除已选 ({checkedIds.size})
                </Button>
              </>
            )}
            <span className="ml-auto text-muted-foreground text-xs tabular-nums">
              共 {total} 份{query ? `(命中 ${total})` : ""}
            </span>
          </div>

          {body}

          <div className="mt-3 flex items-center justify-between text-muted-foreground text-xs">
            <span>
              第 {reports.length === 0 ? 0 : offset + 1}-{Math.min(offset + reports.length, total)} 份 / 共 {total} 份
            </span>
            <div className="flex items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                disabled={offset === 0 || loading}
                onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
              >
                上一页
              </Button>
              <Button
                variant="outline"
                size="sm"
                disabled={offset + PAGE_SIZE >= total || loading}
                onClick={() => setOffset(offset + PAGE_SIZE)}
              >
                下一页
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* 批量删除确认:兄弟节点,不进上方三元链。 */}
      <AlertDialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>确认删除</AlertDialogTitle>
            <AlertDialogDescription>
              将删除已选的 {checkedIds.size} 份报告,该操作不可恢复。
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

      <CreateReportDialog
        open={creating}
        onOpenChange={setCreating}
        onCreated={(id) => {
          setCreating(false);
          if (companyID === -1) loadUnassigned(query, kind, offset, severity);
          else load(query, kind, offset, companyID, severity);
          router.push(`/function/reports/detail?id=${id}`);
        }}
      />

      <BatchExportDialog open={exportOpen} onOpenChange={setExportOpen} ids={[...checkedIds]} />
    </div>
  );
}

function CreateReportDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onCreated: (id: number) => void;
}) {
  const [title, setTitle] = React.useState("");
  const [summary, setSummary] = React.useState("");
  const [content, setContent] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  const submit = async () => {
    if (!title.trim()) {
      toast.error("请填写报告标题");
      return;
    }
    setBusy(true);
    try {
      const r = await api.createReport({ title: title.trim(), summary, content });
      toast.success("报告已创建");
      setTitle("");
      setSummary("");
      setContent("");
      onCreated(r.report.id);
    } catch (e) {
      toast.error(`创建失败:${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>新建报告</DialogTitle>
          <DialogDescription>手写一份 Markdown 报告存入报告中心,之后可随时编辑与导出。</DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid gap-1.5">
            <Label htmlFor="report-title">标题</Label>
            <Input id="report-title" value={title} onChange={(e) => setTitle(e.target.value)} placeholder="报告标题" />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="report-summary">摘要</Label>
            <Input
              id="report-summary"
              value={summary}
              onChange={(e) => setSummary(e.target.value)}
              placeholder="一句话摘要(可选)"
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="report-content">正文(Markdown)</Label>
            <Textarea
              id="report-content"
              value={content}
              onChange={(e) => setContent(e.target.value)}
              placeholder="# 标题&#10;&#10;正文…"
              className="min-h-56 font-mono text-xs"
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button onClick={() => void submit()} disabled={busy}>
            创建
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// BatchExportDialog 批量导出已选报告:勾选集合一键打包为 zip 下载。服务端逐份按
// 所选格式与模板渲染,失败项写入包内失败清单(不中断整体导出)。模板为可选项,
// 加载失败只 toast 不阻塞;格式/模板单选交互与详情页 ExportDialog「直接导出」同款。
function BatchExportDialog({
  open,
  onOpenChange,
  ids,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  ids: number[];
}) {
  const [format, setFormat] = React.useState<"md" | "docx">("md");
  // 哨兵 "none" = 不使用模板(Radix Select 保留空串,不能用 "" 表达空选)。
  const [tplId, setTplId] = React.useState("none");
  const [templates, setTemplates] = React.useState<ReportTemplate[]>([]);
  const [templatesLoaded, setTemplatesLoaded] = React.useState(false);
  const [busy, setBusy] = React.useState(false);

  // 打开时且首次成功拉取后不再重复请求;失败仅 toast,下次打开可重试(模板可选,不阻塞导出)。
  React.useEffect(() => {
    if (!open || templatesLoaded) return;
    api
      .reportTemplates({ limit: 100 })
      .then((r) => {
        setTemplates(r.templates);
        setTemplatesLoaded(true);
      })
      .catch((e) => toast.error(`加载模板失败:${(e as Error).message}`));
  }, [open, templatesLoaded]);

  const submit = async () => {
    // 防御:工具栏只在勾选非空时可达,这里仍兜底防空数组发请求。
    if (ids.length === 0) {
      toast.error("请先勾选要导出的报告");
      return;
    }
    setBusy(true);
    try {
      await api.exportReportsBatch(ids, format, tplId === "none" ? undefined : Number(tplId));
      toast.success(`已导出 ${ids.length} 份报告`);
      onOpenChange(false);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>批量导出报告</DialogTitle>
          <DialogDescription>
            将打包导出已选的 {ids.length} 份报告为 zip 压缩包;单份报告按所选格式与模板渲染,导出失败的报告会列在压缩包内的失败清单中。
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid gap-1.5">
            <Label>导出格式</Label>
            {/* 与详情页 ExportDialog 一致:Select 单选 md / docx。 */}
            <Select value={format} onValueChange={(v) => setFormat(v as "md" | "docx")}>
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="md">Markdown (.md)</SelectItem>
                  <SelectItem value="docx">Word (.docx)</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
          </div>
          <div className="grid gap-1.5">
            <Label>报告模板(可选)</Label>
            <Select value={tplId} onValueChange={setTplId}>
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="none">不使用模板(原文)</SelectItem>
                  {templates.map((t) => (
                    <SelectItem key={t.id} value={String(t.id)}>
                      {t.name}({t.format})
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            {templates.length === 0 && (
              <p className="text-muted-foreground text-[11px]">
                还没有模板:在报告中心页点「模板管理」上传 .docx / .md 模板;不使用模板则直接导出原文。
              </p>
            )}
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy}>
            取消
          </Button>
          {/* 大包同步生成需数秒,busy 期间禁用并换文案,避免用户以为卡死重复点击。 */}
          <Button onClick={() => void submit()} disabled={busy}>
            <DownloadIcon className="size-4" />
            {busy ? "打包中…" : "打包导出"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
