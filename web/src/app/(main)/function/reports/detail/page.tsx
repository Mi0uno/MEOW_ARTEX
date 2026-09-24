"use client";

// 报告详情:Markdown 渲染 / 原文切换,复制、下载 .md、删除。

import * as React from "react";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";

import { ArrowLeftIcon, CopyIcon, FileDownIcon, FileTextIcon, HistoryIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { CopyButton } from "@/components/copy-button";
import { Markdown } from "@/components/markdown";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { ReportDetail, ReportEvent, ReportTemplate } from "@/lib/types";
import { copyText } from "@/lib/utils";

const KIND_META: Record<string, { label: string; badgeClass: string }> = {
  task: { label: "任务报告", badgeClass: "bg-blue-500/15 text-blue-600 border-blue-500/30" },
  finding: { label: "漏洞报告", badgeClass: "bg-red-500/15 text-red-600 border-red-500/30" },
  custom: { label: "自定义", badgeClass: "bg-muted text-muted-foreground" },
};

// 生成过程事件的展示元数据。
const EVENT_META: Record<string, { label: string; badgeClass: string }> = {
  created: { label: "创建", badgeClass: "bg-blue-500/15 text-blue-600 border-blue-500/30" },
  archived: { label: "归档", badgeClass: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30" },
  updated: { label: "更新存档", badgeClass: "bg-amber-500/15 text-amber-600 border-amber-500/30" },
  exported: { label: "导出", badgeClass: "bg-violet-500/15 text-violet-600 border-violet-500/30" },
  uploaded: { label: "上传模板", badgeClass: "bg-muted text-muted-foreground border-muted-foreground/30" },
  ai_analyzed: { label: "AI 分析模板", badgeClass: "bg-cyan-500/15 text-cyan-600 border-cyan-500/30" },
  deleted: { label: "删除", badgeClass: "bg-red-500/15 text-red-600 border-red-500/30" },
};

const ACTOR_LABEL: Record<string, string> = {
  user: "用户",
  agent: "AI Agent",
  system: "系统",
};

// eventDetailText 把事件 detail JSON 摘要成一句可读说明。
function eventDetailText(d?: Record<string, unknown>): string {
  if (!d) return "";
  const parts: string[] = [];
  if (typeof d.words === "number") parts.push(`约 ${d.words} 字`);
  if (typeof d.source === "string") parts.push(`入口 ${d.source}`);
  if (typeof d.tool === "string") parts.push(`工具 ${d.tool}`);
  if (typeof d.format === "string") parts.push(`格式 ${d.format}`);
  if (typeof d.template_name === "string") parts.push(`模板 ${d.template_name}`);
  if (typeof d.finding_id === "number") parts.push(`发现 #${d.finding_id}`);
  if (typeof d.filename === "string") parts.push(`文件 ${d.filename}`);
  if (typeof d.structure === "string") parts.push(d.structure);
  return parts.join(" · ");
}

function fmtTime(iso?: string | null): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString();
}

function ReportDetailInner() {
  const searchParams = useSearchParams();
  const router = useRouter();
  const id = Number(searchParams.get("id") ?? "0");
  const [report, setReport] = React.useState<ReportDetail | null>(null);
  const [missing, setMissing] = React.useState(false);
  const [events, setEvents] = React.useState<ReportEvent[]>([]);
  // 按模板导出
  const [exportOpen, setExportOpen] = React.useState(false);
  const [templates, setTemplates] = React.useState<ReportTemplate[]>([]);
  const [tplId, setTplId] = React.useState<string>("none");
  const [format, setFormat] = React.useState<"md" | "docx">("md");
  const [exporting, setExporting] = React.useState(false);

  React.useEffect(() => {
    if (!Number.isFinite(id) || id <= 0) {
      setMissing(true);
      return;
    }
    Promise.all([api.reportDetail(id), api.reportEvents(id).catch(() => [] as ReportEvent[])])
      .then(([r, evs]) => {
        setReport(r);
        setEvents(evs);
      })
      .catch(() => setMissing(true));
  }, [id]);

  const remove = async () => {
    if (!report) return;
    if (!window.confirm(`删除报告「${report.title}」?该操作不可恢复。`)) return;
    try {
      await api.deleteReport(report.id);
      toast.success("报告已删除");
      router.push("/function/reports");
    } catch (e) {
      toast.error(`删除失败:${(e as Error).message}`);
    }
  };

  const openExport = async () => {
    setExportOpen(true);
    try {
      const r = await api.reportTemplates();
      setTemplates(r.templates);
    } catch {
      setTemplates([]);
    }
  };

  const runExport = async () => {
    if (!report) return;
    setExporting(true);
    try {
      await api.exportReport(report.id, tplId === "none" ? undefined : Number(tplId), format);
      toast.success("已导出");
      setExportOpen(false);
      api
        .reportEvents(report.id)
        .then(setEvents)
        .catch(() => undefined);
    } catch (e) {
      toast.error(`导出失败:${(e as Error).message}`);
    } finally {
      setExporting(false);
    }
  };

  if (missing) {
    return (
      <div className="p-4 md:p-6">
        <Card>
          <CardContent className="flex flex-col items-center gap-3 py-16 text-center text-muted-foreground text-sm">
            <FileTextIcon className="size-8 opacity-40" />
            报告不存在或已被删除。
            <Button variant="outline" size="sm" asChild>
              <Link href="/function/reports">返回报告中心</Link>
            </Button>
          </CardContent>
        </Card>
      </div>
    );
  }

  if (!report) {
    return <div className="p-4 text-center text-muted-foreground text-sm md:p-6">加载中…</div>;
  }

  const meta = KIND_META[report.kind] ?? KIND_META.custom;

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="mb-1 flex items-center gap-2">
            <Button variant="ghost" size="sm" asChild>
              <Link href="/function/reports">
                <ArrowLeftIcon className="size-4" />
                报告中心
              </Link>
            </Button>
            <Badge variant="outline" className={meta.badgeClass}>
              {meta.label}
            </Badge>
          </div>
          <h1 className="truncate font-semibold text-xl">{report.title}</h1>
          <p className="text-muted-foreground text-sm">
            {report.summary ? `${report.summary} · ` : ""}
            更新于 {fmtTime(report.updated_at)}
            {report.task_id ? (
              <>
                {" · "}
                <Link
                  href={`/function/tasks/detail?id=${report.task_id}`}
                  className="underline decoration-dotted underline-offset-2 hover:text-foreground"
                >
                  关联任务 #{report.task_id}
                </Link>
              </>
            ) : null}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <CopyButton text={report.content} successMessage="已复制 Markdown" />
          <Button variant="ghost" size="sm" onClick={() => void openExport()}>
            <FileDownIcon className="size-4" />
            按模板导出
          </Button>
          <Button variant="ghost" size="sm" onClick={() => void remove()}>
            <Trash2Icon className="size-4 text-red-600" />
            删除
          </Button>
        </div>
      </div>

      <Tabs defaultValue="rendered">
        <TabsList>
          <TabsTrigger value="rendered">渲染</TabsTrigger>
          <TabsTrigger value="source">Markdown 原文</TabsTrigger>
        </TabsList>
        <TabsContent value="rendered" className="mt-3">
          <Card>
            <CardContent className="max-h-[72vh] overflow-auto">
              {report.content.trim() ? (
                <Markdown text={report.content} />
              ) : (
                <p className="py-16 text-center text-muted-foreground text-sm">报告正文为空。</p>
              )}
            </CardContent>
          </Card>
        </TabsContent>
        <TabsContent value="source" className="mt-3">
          <Card>
            <CardHeader className="flex-row items-center justify-between">
              <CardTitle className="text-sm">Markdown 原文</CardTitle>
              <Button
                variant="ghost"
                size="sm"
                onClick={() =>
                  void copyText(report.content).then((ok) =>
                    toast[ok ? "success" : "error"](ok ? "已复制" : "复制失败"),
                  )
                }
              >
                <CopyIcon className="size-4" />
                复制
              </Button>
            </CardHeader>
            <CardContent>
              <pre className="max-h-[68vh] overflow-auto whitespace-pre-wrap rounded-md bg-muted/20 p-4 font-mono text-xs">
                {report.content}
              </pre>
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>

      {/* 生成过程:报告从产生到导出的完整事件时间线 */}
      <Card>
        <CardHeader className="flex-row items-center gap-2">
          <HistoryIcon className="text-muted-foreground size-4" />
          <CardTitle className="text-sm">生成过程</CardTitle>
          {events.length > 0 && (
            <Badge variant="secondary" className="text-[10px]">
              {events.length} 条记录
            </Badge>
          )}
        </CardHeader>
        <CardContent>
          {events.length === 0 ? (
            <p className="text-muted-foreground py-6 text-center text-sm">
              暂无过程记录。报告创建、归档、导出等操作会自动记录在这里。
            </p>
          ) : (
            <ol className="relative flex flex-col gap-4 border-l pl-5">
              {events.map((ev) => {
                const meta = EVENT_META[ev.kind] ?? {
                  label: ev.kind,
                  badgeClass: "bg-muted text-muted-foreground",
                };
                const summaryText = eventDetailText(ev.detail);
                return (
                  <li key={ev.id} className="relative">
                    <span className="absolute top-1.5 -left-[1.4rem] size-2 rounded-full bg-primary/60 ring-4 ring-primary/10" />
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge variant="outline" className={meta.badgeClass}>
                        {meta.label}
                      </Badge>
                      <span className="text-muted-foreground text-xs">{ACTOR_LABEL[ev.actor] ?? ev.actor}</span>
                      <span className="text-muted-foreground text-xs tabular-nums">{fmtTime(ev.created_at)}</span>
                      {summaryText && <span className="text-muted-foreground text-xs">{summaryText}</span>}
                    </div>
                  </li>
                );
              })}
            </ol>
          )}
        </CardContent>
      </Card>

      <Dialog open={exportOpen} onOpenChange={setExportOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>按模板导出</DialogTitle>
            <DialogDescription>
              选择报告模板后,系统按模板结构填充报告内容;「不使用模板」直接导出报告原文。
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-3">
            <div className="grid gap-1.5">
              <Label>报告模板</Label>
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
                  还没有模板:在报告中心页点「模板管理」上传 .docx / .md 模板。
                </p>
              )}
            </div>
            <div className="grid gap-1.5">
              <Label>导出格式</Label>
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
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setExportOpen(false)}>
              取消
            </Button>
            <Button disabled={exporting} onClick={() => void runExport()}>
              <FileDownIcon className="size-4" />
              {exporting ? "导出中…" : "导出"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

// useSearchParams must sit under a Suspense boundary for static export.
export default function ReportDetailPage() {
  return (
    <React.Suspense fallback={null}>
      <ReportDetailInner />
    </React.Suspense>
  );
}
