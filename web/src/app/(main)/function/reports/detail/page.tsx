"use client";

// 报告详情:Markdown 渲染 / 原文切换,复制、导出(AI 按模板生成 / 直接导出)、删除;
// 生成过程事件时间线与报告生成作业(生成记录)展示。

import * as React from "react";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";

import {
  ArrowLeftIcon,
  CopyIcon,
  FileDownIcon,
  FileTextIcon,
  HistoryIcon,
  Loader2Icon,
  SparklesIcon,
  Trash2Icon,
} from "lucide-react";
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
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { LLMProfile, ReportDetail, ReportEvent, ReportJob, ReportTemplate } from "@/lib/types";
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
  ai_enhanced: { label: "AI 润写", badgeClass: "bg-cyan-500/15 text-cyan-600 border-cyan-500/30" },
  ai_enhance_failed: { label: "AI 润写失败", badgeClass: "bg-red-500/15 text-red-600 border-red-500/30" },
  deleted: { label: "删除", badgeClass: "bg-red-500/15 text-red-600 border-red-500/30" },
};

// 报告生成作业(report_jobs)类型的展示元数据:导出=AI 按模板导出,AI 撰写=任务报告 AI 润写。
const JOB_KIND_META: Record<string, { label: string; badgeClass: string }> = {
  export: { label: "AI 导出", badgeClass: "bg-violet-500/15 text-violet-600 border-violet-500/30" },
  task_report_ai: { label: "AI 撰写", badgeClass: "bg-cyan-500/15 text-cyan-600 border-cyan-500/30" },
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
  if (typeof d.job_id === "number") parts.push(`作业 #${d.job_id}`);
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
  const [exportOpen, setExportOpen] = React.useState(false);
  const [recordsOpen, setRecordsOpen] = React.useState(false);

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

  // 导出对话框在快速导出成功 / AI 作业到达终态后回调,刷新过程事件流。
  const refreshEvents = React.useCallback(() => {
    if (!Number.isFinite(id) || id <= 0) return;
    api
      .reportEvents(id)
      .then(setEvents)
      .catch(() => undefined);
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
          <Button variant="ghost" size="sm" onClick={() => setExportOpen(true)}>
            <FileDownIcon className="size-4" />
            导出报告
          </Button>
          <Button variant="ghost" size="sm" onClick={() => setRecordsOpen(true)}>
            <HistoryIcon className="size-4" />
            导出记录
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

      <ExportDialog open={exportOpen} onOpenChange={setExportOpen} reportId={report.id} onFinished={refreshEvents} />
      <RecordsDialog open={recordsOpen} onOpenChange={setRecordsOpen} reportId={report.id} />
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

// JobStatusBadge 生成作业状态徽章:running 转圈、done 绿、failed 红(悬停看失败原因)。
function JobStatusBadge({ job }: { job: ReportJob }) {
  if (job.status === "running") {
    return (
      <Badge variant="outline" className="gap-1 border-blue-500/30 bg-blue-500/15 text-blue-600">
        <Loader2Icon className="size-3 animate-spin" />
        生成中
      </Badge>
    );
  }
  if (job.status === "done") {
    return <Badge variant="outline" className="border-emerald-500/30 bg-emerald-500/15 text-emerald-600">已完成</Badge>;
  }
  return (
    <Badge variant="outline" className="border-red-500/30 bg-red-500/15 text-red-600" title={job.error || "失败"}>
      失败
    </Badge>
  );
}

// ExportDialog 导出对话框:两种模式单选。
// 「AI 按模板生成」走 export-ai 异步作业:202 返回作业/监看任务 id 后 3s 轮询作业状态,
// done 自动下载产物、刷新事件流;关闭对话框即停止轮询(服务端生成不中断),可稍后回来
// 在「生成记录」查看或下载。「直接导出」走同步变量替换接口,立即下载。
// 对话框下方展示报告生成作业历史(含任务报告 AI 撰写),支持下载与跳转监看任务。
function ExportDialog({
  open,
  onOpenChange,
  reportId,
  onFinished,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  reportId: number;
  onFinished: () => void;
}) {
  const [mode, setMode] = React.useState<"ai" | "quick">("ai");
  const [templates, setTemplates] = React.useState<ReportTemplate[]>([]);
  const [llmProfiles, setLlmProfiles] = React.useState<LLMProfile[]>([]);

  // AI 按模板生成(异步作业):模板必选;profile 用哨兵 "0" 表示跟随激活配置。
  const [aiTplId, setAiTplId] = React.useState("none");
  const [aiFormat, setAiFormat] = React.useState<"md" | "docx">("md");
  const [aiProfileId, setAiProfileId] = React.useState("0");
  const [submitting, setSubmitting] = React.useState(false);
  const [activeJobId, setActiveJobId] = React.useState<number | null>(null);
  const [activeTaskId, setActiveTaskId] = React.useState<number | null>(null);
  const [aiError, setAiError] = React.useState("");

  // 直接导出(同步):模板可选,"none" 表示不使用模板(原文)。
  const [quickTplId, setQuickTplId] = React.useState("none");
  const [quickFormat, setQuickFormat] = React.useState<"md" | "docx">("md");
  const [exporting, setExporting] = React.useState(false);

  const [jobs, setJobs] = React.useState<ReportJob[]>([]);
  const pollRef = React.useRef<ReturnType<typeof setInterval> | null>(null);

  const stopPoll = React.useCallback(() => {
    if (pollRef.current) {
      clearInterval(pollRef.current);
      pollRef.current = null;
    }
  }, []);
  React.useEffect(() => stopPoll, [stopPoll]);

  const loadJobs = React.useCallback(() => {
    api
      .reportJobs(reportId)
      .then(setJobs)
      .catch(() => setJobs([]));
  }, [reportId]);

  // startPoll 轮询作业状态直到终态;autoDownload 仅对本对话框里提交的作业为真,
  // 重开对话框续看的历史作业完成时只提示,由用户在生成记录手动下载。
  const startPoll = React.useCallback(
    (jobId: number, autoDownload: boolean) => {
      stopPoll();
      pollRef.current = setInterval(() => {
        api
          .reportJob(jobId)
          .then((job) => {
            if (job.status === "running") return;
            stopPoll();
            setActiveJobId(null);
            setJobs((prev) => prev.map((j) => (j.id === job.id ? job : j)));
            if (job.status === "done") {
              if (autoDownload) {
                api
                  .reportJobDownload(jobId)
                  .then(() => toast.success("AI 生成完成,文件已开始下载"))
                  .catch((e) => toast.error(`下载失败:${(e as Error).message}`));
              } else {
                toast.success("AI 生成完成,可在生成记录中下载");
              }
            } else {
              setAiError(job.error || "未知原因");
              toast.error(`AI 生成失败:${job.error || "未知原因"}`);
            }
            onFinished();
          })
          .catch(() => undefined); // 瞬时轮询失败忽略,下一轮继续
      }, 3000);
    },
    [stopPoll, onFinished],
  );

  // 打开对话框:拉模板/LLM 配置/生成记录;存在进行中的作业则续接轮询(不自动下载);
  // 关闭对话框时停止轮询(服务端作业继续跑,不影响结果)。
  React.useEffect(() => {
    if (!open) {
      stopPoll();
      setActiveJobId(null);
      return;
    }
    setAiError("");
    api
      .reportTemplates()
      .then((r) => setTemplates(r.templates))
      .catch(() => setTemplates([]));
    api
      .llmProfiles()
      .then(setLlmProfiles)
      .catch(() => setLlmProfiles([]));
    api
      .reportJobs(reportId)
      .then((js) => {
        setJobs(js);
        const running = js.find((j) => j.status === "running");
        if (running) {
          setActiveJobId(running.id);
          setActiveTaskId(running.monitor_task_id ?? null);
          startPoll(running.id, false);
        }
      })
      .catch(() => setJobs([]));
  }, [open, reportId, startPoll, stopPoll]);

  // 提交 AI 按模板生成:202 返回作业/监看任务 id,随后进入 3s 轮询直到终态。
  const submitAI = async () => {
    if (aiTplId === "none") {
      toast.error("请先选择报告模板(AI 按模板生成必须指定模板)");
      return;
    }
    setSubmitting(true);
    setAiError("");
    try {
      const r = await api.exportReportAI(reportId, {
        templateId: Number(aiTplId),
        format: aiFormat,
        profileId: aiProfileId === "0" ? undefined : Number(aiProfileId),
      });
      toast.success("AI 生成任务已启动,可在 报告中心 → 任务/对话 监看过程");
      setActiveJobId(r.job_id);
      setActiveTaskId(r.task_id);
      startPoll(r.job_id, true);
      loadJobs();
    } catch (e) {
      toast.error(`启动 AI 生成失败:${(e as Error).message}`);
    } finally {
      setSubmitting(false);
    }
  };

  // 直接导出:同步接口,浏览器直接下载,完成后关闭对话框。
  const runQuickExport = async () => {
    setExporting(true);
    try {
      await api.exportReport(reportId, quickTplId === "none" ? undefined : Number(quickTplId), quickFormat);
      toast.success("已导出");
      onFinished();
      onOpenChange(false);
    } catch (e) {
      toast.error(`导出失败:${(e as Error).message}`);
    } finally {
      setExporting(false);
    }
  };

  const downloadJob = (jobId: number) => {
    api
      .reportJobDownload(jobId)
      .then(() => toast.success("文件已开始下载"))
      .catch((e) => toast.error(`下载失败:${(e as Error).message}`));
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[88vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>导出报告</DialogTitle>
          <DialogDescription>
            「AI 按模板生成」由报告 agent 按模板结构与导出写作指令逐章撰写正文;「直接导出」仅做变量替换,立即下载。
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <RadioGroup value={mode} onValueChange={(v) => setMode(v as "ai" | "quick")}>
            <label htmlFor="export-mode-ai" className="flex items-start gap-2 rounded-md border p-2.5 text-sm">
              <RadioGroupItem id="export-mode-ai" value="ai" className="mt-0.5" />
              <span>
                AI 按模板生成(推荐)
                <span className="text-muted-foreground block text-[11px]">
                  质量更好,逐章生成需数分钟;完成后自动下载,可关对话框稍后回来取。
                </span>
              </span>
            </label>
            <label htmlFor="export-mode-quick" className="flex items-start gap-2 rounded-md border p-2.5 text-sm">
              <RadioGroupItem id="export-mode-quick" value="quick" className="mt-0.5" />
              <span>
                直接导出(快速,变量替换)
                <span className="text-muted-foreground block text-[11px]">不做 AI 润写,立即下载。</span>
              </span>
            </label>
          </RadioGroup>

          {mode === "ai" ? (
            <>
              <div className="grid gap-1.5">
                <Label>报告模板(必选)</Label>
                <Select value={aiTplId} onValueChange={setAiTplId}>
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      <SelectItem value="none">请选择模板</SelectItem>
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
                <Label>LLM 配置</Label>
                <Select value={aiProfileId} onValueChange={setAiProfileId}>
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      <SelectItem value="0">跟随激活的 LLM 配置</SelectItem>
                      {llmProfiles.map((p) => (
                        <SelectItem key={p.id} value={String(p.id)}>
                          {p.name}（{p.model}）
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
              </div>
              {activeJobId != null && (
                <div className="rounded-md border border-blue-500/30 bg-blue-500/10 p-3 text-xs">
                  <p className="flex items-center gap-2 text-blue-600">
                    <Loader2Icon className="size-3.5 animate-spin" />
                    AI 生成进行中(作业 #{activeJobId})…
                  </p>
                  <p className="text-muted-foreground mt-1">
                    关闭对话框不会中断生成,可稍后回到本对话框在「生成记录」查看或下载。
                    {activeTaskId ? (
                      <>
                        {" "}
                        <a
                          href={`/function/tasks/detail?id=${activeTaskId}`}
                          className="underline decoration-dotted underline-offset-2 hover:text-foreground"
                        >
                          监看任务
                        </a>
                      </>
                    ) : null}
                  </p>
                </div>
              )}
              {aiError && activeJobId == null && <p className="text-destructive text-xs">上次 AI 生成失败:{aiError}</p>}
            </>
          ) : (
            <div className="grid gap-1.5">
              <Label>报告模板</Label>
              <Select value={quickTplId} onValueChange={setQuickTplId}>
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
          )}

          <div className="grid gap-1.5">
            <Label>导出格式</Label>
            <Select
              value={mode === "ai" ? aiFormat : quickFormat}
              onValueChange={(v) =>
                mode === "ai" ? setAiFormat(v as "md" | "docx") : setQuickFormat(v as "md" | "docx")
              }
            >
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

          <JobRecordsList jobs={jobs} onRefresh={loadJobs} onDownload={downloadJob} />
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {mode === "ai" && activeJobId != null ? "关闭(稍后在生成记录查看)" : "取消"}
          </Button>
          {mode === "ai" ? (
            <Button disabled={submitting} onClick={() => void submitAI()}>
              <SparklesIcon className="size-4" />
              {submitting ? "启动中…" : "开始 AI 生成"}
            </Button>
          ) : (
            <Button disabled={exporting} onClick={() => void runQuickExport()}>
              <FileDownIcon className="size-4" />
              {exporting ? "导出中…" : "导出"}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// JobRecordsList 报告生成作业列表:导出对话框与「导出记录」弹窗共用的展示块
//(数据获取归调用方:导出对话框随轮询增量更新,记录弹窗整表刷新)。
function JobRecordsList({
  jobs,
  onRefresh,
  onDownload,
}: {
  jobs: ReportJob[];
  onRefresh: () => void;
  onDownload: (jobId: number) => void;
}) {
  return (
    <div className="grid gap-1.5">
      <div className="flex items-center justify-between">
        <Label>生成记录</Label>
        <Button variant="ghost" size="sm" className="h-7 px-2 text-xs" onClick={onRefresh}>
          刷新
        </Button>
      </div>
      {jobs.length === 0 ? (
        <p className="text-muted-foreground text-[11px]">
          暂无生成记录。AI 按模板导出与任务报告 AI 撰写的作业会出现在这里。
        </p>
      ) : (
        <div className="max-h-48 overflow-y-auto rounded-md border">
          {jobs.map((j) => {
            const kind = JOB_KIND_META[j.kind] ?? { label: j.kind, badgeClass: "bg-muted text-muted-foreground" };
            return (
              <div
                key={j.id}
                className="flex flex-wrap items-center gap-2 border-b px-2.5 py-2 text-xs last:border-b-0"
              >
                <span className="text-muted-foreground tabular-nums">{fmtTime(j.created_at)}</span>
                <Badge variant="outline" className={kind.badgeClass}>
                  {kind.label}
                </Badge>
                <JobStatusBadge job={j} />
                {j.format ? <span className="text-muted-foreground uppercase">{j.format}</span> : null}
                {j.status === "failed" && j.error ? (
                  <span className="text-destructive max-w-40 truncate" title={j.error}>
                    {j.error}
                  </span>
                ) : null}
                <span className="ml-auto flex items-center gap-1">
                  {j.monitor_task_id ? (
                    <a
                      href={`/function/tasks/detail?id=${j.monitor_task_id}`}
                      className="text-muted-foreground underline decoration-dotted underline-offset-2 hover:text-foreground"
                    >
                      监看
                    </a>
                  ) : null}
                  {j.kind === "export" && j.status === "done" ? (
                    <Button variant="ghost" size="sm" className="h-6 px-2 text-xs" onClick={() => onDownload(j.id)}>
                      下载
                    </Button>
                  ) : null}
                </span>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

// RecordsDialog 导出记录弹窗:报告详情头部「导出记录」按钮直达,无需经过导出
// 对话框即可查看/下载全部生成作业。打开拉一次;存在进行中作业时 3s 轮询整表,
// 全部到终态自动停;关闭即停。
function RecordsDialog({
  open,
  onOpenChange,
  reportId,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  reportId: number;
}) {
  const [jobs, setJobs] = React.useState<ReportJob[]>([]);

  const loadJobs = React.useCallback(() => {
    api
      .reportJobs(reportId)
      .then(setJobs)
      .catch(() => setJobs([]));
  }, [reportId]);

  React.useEffect(() => {
    if (!open) return;
    loadJobs();
    const timer = setInterval(() => {
      api
        .reportJobs(reportId)
        .then((js) => {
          setJobs(js);
          if (!js.some((j) => j.status === "running")) clearInterval(timer);
        })
        .catch(() => undefined);
    }, 3000);
    return () => clearInterval(timer);
  }, [open, reportId, loadJobs]);

  const downloadJob = (jobId: number) => {
    api
      .reportJobDownload(jobId)
      .then(() => toast.success("文件已开始下载"))
      .catch((e) => toast.error(`下载失败:${(e as Error).message}`));
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[80vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>导出记录</DialogTitle>
          <DialogDescription>
            本报告的全部生成作业:AI 按模板导出可重新下载,AI 润写可跳转监看任务看过程。
          </DialogDescription>
        </DialogHeader>
        <JobRecordsList jobs={jobs} onRefresh={loadJobs} onDownload={downloadJob} />
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            关闭
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
