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
  EyeIcon,
  FileTextIcon,
  LayoutTemplateIcon,
  PlusIcon,
  SearchIcon,
  Trash2Icon,
} from "lucide-react";
import { toast } from "sonner";

import { CompanyPickerPage } from "@/components/company-picker-page";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
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
  const [loading, setLoading] = React.useState(true);
  const [offset, setOffset] = React.useState(0);
  const [creating, setCreating] = React.useState(false);

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

  const load = React.useCallback((search: string, k: string, off: number, cid: number | null) => {
    setLoading(true);
    api
      .listReports({
        kind: k === "all" ? "" : k,
        q: search,
        // cid 为 0(全部)时不传 company_id,即不限企业。
        companyId: cid != null && cid > 0 ? cid : undefined,
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
  const loadUnassigned = React.useCallback((search: string, k: string, off: number) => {
    setLoading(true);
    api
      .listUnassignedReports({ kind: k === "all" ? "" : k, q: search, limit: PAGE_SIZE, offset: off })
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
      loadUnassigned(queryRef.current, kind, offset);
      return;
    }
    load(queryRef.current, kind, offset, companyID);
  }, [load, loadUnassigned, kind, offset, companyID]);

  const search = () => {
    setOffset(0);
    if (companyID === -1) {
      loadUnassigned(query, kind, 0);
      return;
    }
    load(query, kind, 0, companyID);
  };

  const remove = async (r: ReportSummary) => {
    if (!window.confirm(`删除报告「${r.title}」?该操作不可恢复。`)) return;
    try {
      await api.deleteReport(r.id);
      toast.success("报告已删除");
      if (companyID === -1) loadUnassigned(query, kind, offset);
      else load(query, kind, offset, companyID);
    } catch (e) {
      toast.error(`删除失败:${(e as Error).message}`);
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

      <CreateReportDialog
        open={creating}
        onOpenChange={setCreating}
        onCreated={(id) => {
          setCreating(false);
          if (companyID === -1) loadUnassigned(query, kind, offset);
          else load(query, kind, offset, companyID);
          router.push(`/function/reports/detail?id=${id}`);
        }}
      />
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
