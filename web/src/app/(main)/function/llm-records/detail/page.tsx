"use client";

// LLM 录制详情:单条记录的完整请求 / 响应。
//
// 列表页只做检索,阅读与复制全部收在这里——因为排查 provider 问题时需要的是
// 整屏的报文,而不是被切成半屏的内联面板。
//
// 视图分两层,不要混淆:
//   1) 数据来源(本页的 reqSource/respSource):「归一化视图」= Recorder 归一化后
//      的请求/响应(不含工具 schema、无 tool_use 块);「HTTP 原文」= provider 实际
//      收发的字节(请求含工具 schema,响应是原始 SSE 帧)。旧记录没有原文。
//   2) 排版(FormattedPayload 内部):同一份文本的「整理视图」与「原始 JSON」。
// 原文是排查 provider 侧问题的唯一依据,所以第 1 层必须保留;整理视图只是为了好读。

import * as React from "react";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";

import {
  ArrowLeftIcon,
  BracesIcon,
  CopyIcon,
  FileQuestionIcon,
  Loader2Icon,
  RadioIcon,
  Trash2Icon,
} from "lucide-react";
import { toast } from "sonner";

import { FormattedPayload } from "@/components/formatted-payload";
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
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { LLMRecordDetail } from "@/lib/types";
import { cn, copyText } from "@/lib/utils";

import { fmtLatency, fmtTime, fmtTokens, isSlow, StatusBadge } from "../_components/llm-record-shared";

type PayloadSource = "normalized" | "raw";

// pickPayload 决定当前该显示哪一份文本。来源选「归一化」但归一化为空时回落到原文
// (反之亦然),避免切到一份空的视图看着像「这条记录没内容」。
function pickPayload(source: PayloadSource, normalized?: string, raw?: string): string {
  const n = normalized ?? "";
  const r = raw ?? "";
  return source === "raw" ? r || n : n || r;
}

// 概览里的一行:标签 + 值。值可能是徽标 / 长文本,所以不限制渲染形态。
function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid gap-1 sm:grid-cols-[7rem_1fr] sm:gap-3">
      <dt className="text-muted-foreground text-xs">{label}</dt>
      <dd className="min-w-0 text-xs leading-relaxed break-words">{children}</dd>
    </div>
  );
}

export default function LLMRecordDetailPage() {
  return (
    <React.Suspense fallback={null}>
      <LLMRecordDetailInner />
    </React.Suspense>
  );
}

function LLMRecordDetailInner() {
  const searchParams = useSearchParams();
  const router = useRouter();
  const rawID = searchParams.get("id") ?? "";
  const id = Number(rawID);

  const [record, setRecord] = React.useState<LLMRecordDetail | null>(null);
  const [loading, setLoading] = React.useState(true);
  const [missing, setMissing] = React.useState(false);

  // 数据来源切换(默认归一化视图,与列表页原有行为一致)。
  const [reqSource, setReqSource] = React.useState<PayloadSource>("normalized");
  const [respSource, setRespSource] = React.useState<PayloadSource>("normalized");

  // 按任务删除记录(后端只有任务级删除,没有单条删除接口)。
  const [deleteOpen, setDeleteOpen] = React.useState(false);
  const [deleting, setDeleting] = React.useState(false);

  React.useEffect(() => {
    if (!rawID || !Number.isFinite(id) || id <= 0) {
      setMissing(true);
      setLoading(false);
      return;
    }
    let alive = true;
    setLoading(true);
    api
      .llmRecordDetail(id)
      .then((d) => {
        if (alive) setRecord(d);
      })
      .catch(() => {
        if (alive) setMissing(true);
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [id, rawID]);

  const reqText = record ? pickPayload(reqSource, record.request_body, record.raw_request) : "";
  const respText = record ? pickPayload(respSource, record.response_body, record.raw_response) : "";
  const hasRaw = !!(record?.raw_request || record?.raw_response);

  const copy = async (text: string, label: string) => {
    const ok = await copyText(text);
    toast[ok ? "success" : "error"](ok ? `已复制${label}` : "复制失败，请手动选择文本复制");
  };

  const confirmDelete = () => {
    if (!record?.task_id) return;
    setDeleting(true);
    api
      .llmRecordsDeleteTask(record.task_id)
      .then(() => {
        toast.success(`已删除任务 #${record.task_id} 的全部记录`);
        router.push("/function/llm-records");
      })
      .catch((e) => toast.error(`删除失败:${(e as Error).message}`))
      .finally(() => setDeleting(false));
  };

  // 参数缺失或记录不存在:给明确出口,不留白屏。
  if (missing) {
    return (
      <div className="p-4 md:p-6">
        <Card>
          <CardContent className="text-muted-foreground flex flex-col items-center gap-3 py-16 text-center text-sm">
            <FileQuestionIcon className="size-8 opacity-40" />
            <span>
              {rawID
                ? `找不到 ID 为 ${rawID} 的 LLM 调用记录（可能已被按任务删除）。`
                : "缺少记录 ID 参数，无法定位这条 LLM 调用记录。"}
            </span>
            <Button variant="outline" size="sm" asChild>
              <Link href="/function/llm-records">
                <ArrowLeftIcon className="size-4" />
                返回列表
              </Link>
            </Button>
          </CardContent>
        </Card>
      </div>
    );
  }

  if (loading || !record) {
    return (
      <div className="text-muted-foreground flex items-center justify-center gap-2 p-4 text-sm md:p-6">
        <Loader2Icon className="size-4 animate-spin" />
        加载中…
      </div>
    );
  }

  const totalTokens = record.input_tokens + record.output_tokens;

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="mb-1 flex items-center gap-2">
            <Button variant="ghost" size="sm" asChild>
              <Link href="/function/llm-records">
                <ArrowLeftIcon className="size-4" />
                LLM 录制
              </Link>
            </Button>
            <Badge variant="outline" className="font-mono text-xs">
              #{record.id}
            </Badge>
            <StatusBadge status={record.status} />
          </div>
          {/* 标题用模型名(排查时的第一识别特征),时间与延迟跟在后面 */}
          <h1 className="truncate font-mono text-xl font-semibold" title={record.model || "—"}>
            {record.model || "（未知模型）"}
          </h1>
          <p className="text-muted-foreground text-sm">
            {fmtTime(record.ts)}
            {" · "}
            <span className={cn("tabular-nums", isSlow(record.latency_ms) && "text-amber-500")}>
              {fmtLatency(record.latency_ms)}
            </span>
            {" · "}
            <span className="tabular-nums">
              {fmtTokens(record.input_tokens)} / {fmtTokens(record.output_tokens)} tokens
            </span>
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="outline" size="sm" disabled={!reqText} onClick={() => void copy(reqText, "请求")}>
            <CopyIcon className="size-4" />
            复制请求
          </Button>
          <Button variant="outline" size="sm" disabled={!respText} onClick={() => void copy(respText, "响应")}>
            <CopyIcon className="size-4" />
            复制响应
          </Button>
          {/* 后端没有单条删除接口,能做的等价操作是删掉该任务的全部记录 */}
          <Button
            variant="outline"
            size="sm"
            className="text-muted-foreground"
            disabled={!record.task_id || deleting}
            title={record.task_id ? `删除任务 #${record.task_id} 的全部记录` : "该记录没有任务归属，无法按任务删除"}
            onClick={() => setDeleteOpen(true)}
          >
            <Trash2Icon className="size-4" />
            删除该任务记录
          </Button>
        </div>
      </div>

      <Tabs defaultValue="overview">
        <TabsList>
          <TabsTrigger value="overview">概览</TabsTrigger>
          <TabsTrigger value="request">请求</TabsTrigger>
          <TabsTrigger value="response">响应</TabsTrigger>
          <TabsTrigger value="raw">原始报文</TabsTrigger>
        </TabsList>

        <TabsContent value="overview" className="mt-3">
          <Card>
            <CardHeader>
              <CardTitle className="text-sm">调用信息</CardTitle>
            </CardHeader>
            <CardContent>
              <dl className="flex flex-col gap-3">
                <Field label="记录 ID">
                  <span className="font-mono">{record.id}</span>
                </Field>
                <Field label="模型">
                  <span className="font-mono">{record.model || "—"}</span>
                </Field>
                <Field label="Profile">{record.profile_name || "—"}</Field>
                <Field label="时间">
                  <span className="tabular-nums">{fmtTime(record.ts)}</span>
                </Field>
                <Field label="延迟">
                  <span className={cn("tabular-nums", isSlow(record.latency_ms) && "text-amber-500")}>
                    {fmtLatency(record.latency_ms)}
                  </span>
                </Field>
                <Field label="Tokens">
                  <span className="tabular-nums">
                    输入 {record.input_tokens} · 输出 {record.output_tokens} · 合计 {totalTokens}
                  </span>
                  {(record.cache_read > 0 || record.cache_write > 0) && (
                    <span className="text-muted-foreground ml-2 tabular-nums">
                      （缓存读 {record.cache_read} / 写 {record.cache_write}）
                    </span>
                  )}
                </Field>
                <Field label="状态">
                  <StatusBadge status={record.status} />
                </Field>
                <Field label="任务">
                  {record.task_id ? (
                    <span className="font-mono">#{record.task_id}</span>
                  ) : (
                    <span className="text-muted-foreground">—</span>
                  )}
                </Field>
                <Field label="会话 ID">
                  {record.session_id ? (
                    <span className="font-mono break-all">{record.session_id}</span>
                  ) : (
                    <span className="text-muted-foreground">—</span>
                  )}
                </Field>
                <Field label="Worker">{record.worker || <span className="text-muted-foreground">—</span>}</Field>
                {record.error && (
                  <Field label="错误信息">
                    <span className="text-red-600 dark:text-red-400 whitespace-pre-wrap">{record.error}</span>
                  </Field>
                )}
              </dl>
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="request" className="mt-3">
          <Card>
            <CardHeader className="flex flex-row items-center justify-between">
              <CardTitle className="text-sm">
                请求
                {reqSource === "raw" && <span className="text-muted-foreground ml-1 font-normal">· HTTP 原文</span>}
              </CardTitle>
              <div className="flex items-center gap-2">
                <SourceToggle
                  value={reqSource}
                  onChange={setReqSource}
                  rawAvailable={!!record.raw_request}
                  rawHint="该记录录制于此功能上线前，无 HTTP 原文"
                />
                <Button variant="ghost" size="sm" disabled={!reqText} onClick={() => void copy(reqText, "请求")}>
                  <CopyIcon className="size-4" />
                  复制
                </Button>
              </div>
            </CardHeader>
            <CardContent>
              {reqText ? (
                <FormattedPayload raw={reqText} maxHeight="max-h-[64vh]" />
              ) : (
                <p className="text-muted-foreground py-12 text-center text-sm">这条记录没有保存请求内容。</p>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="response" className="mt-3">
          <Card>
            <CardHeader className="flex flex-row items-center justify-between">
              <CardTitle className="text-sm">
                响应
                {respSource === "raw" && (
                  <span className="text-muted-foreground ml-1 font-normal">· HTTP 原文（SSE）</span>
                )}
              </CardTitle>
              <div className="flex items-center gap-2">
                <SourceToggle
                  value={respSource}
                  onChange={setRespSource}
                  rawAvailable={!!record.raw_response}
                  rawHint="该记录录制于此功能上线前，无 HTTP 原文"
                />
                <Button variant="ghost" size="sm" disabled={!respText} onClick={() => void copy(respText, "响应")}>
                  <CopyIcon className="size-4" />
                  复制
                </Button>
              </div>
            </CardHeader>
            <CardContent>
              {respText ? (
                <FormattedPayload raw={respText} maxHeight="max-h-[64vh]" />
              ) : (
                <p className="text-muted-foreground py-12 text-center text-sm">这条记录没有保存响应内容。</p>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        {/* 原始报文:两份未加工的文本并排,便于整段复制出去做复现 */}
        <TabsContent value="raw" className="mt-3">
          {hasRaw ? (
            <div className="grid gap-3 lg:grid-cols-2">
              <RawPane
                title="请求原文"
                hint="provider 实际收到的 body（含工具 schema）"
                text={record.raw_request ?? ""}
                onCopy={(t) => void copy(t, "请求原文")}
              />
              <RawPane
                title="响应原文"
                hint="provider 实际返回的字节（原始 SSE 帧）"
                text={record.raw_response ?? ""}
                onCopy={(t) => void copy(t, "响应原文")}
              />
            </div>
          ) : (
            <Card>
              <CardContent className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
                <BracesIcon className="size-8 opacity-40" />
                <span>
                  这条记录没有 HTTP 原文（录制于此功能上线前，或调用在任何请求发出前就失败了）。上面「请求 /
                  响应」页签里的归一化内容仍然可用。
                </span>
              </CardContent>
            </Card>
          )}
        </TabsContent>
      </Tabs>

      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除任务「{record.task_id}」的全部 LLM 对话？</AlertDialogTitle>
            <AlertDialogDescription>
              将永久删除该任务的所有 LLM 调用记录（含请求 /
              响应原文），此操作不可撤销。后端只提供按任务删除，没有单条删除接口。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleting}>取消</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault();
                confirmDelete();
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

// SourceToggle 切换「归一化视图 / HTTP 原文」。没有原文时禁用而不是静默回退,
// 否则看起来像「原文与归一化一致」,会把排查方向带偏。
function SourceToggle({
  value,
  onChange,
  rawAvailable,
  rawHint,
}: {
  value: PayloadSource;
  onChange: (v: PayloadSource) => void;
  rawAvailable: boolean;
  rawHint: string;
}) {
  return (
    <div className="flex items-center gap-1">
      <Button
        variant={value === "normalized" ? "secondary" : "ghost"}
        size="sm"
        className="h-7 text-xs"
        onClick={() => onChange("normalized")}
      >
        归一化视图
      </Button>
      <Button
        variant={value === "raw" ? "secondary" : "ghost"}
        size="sm"
        className="h-7 text-xs"
        disabled={!rawAvailable}
        title={rawAvailable ? "查看与 provider 实际收发的 HTTP 原文" : rawHint}
        onClick={() => onChange("raw")}
      >
        HTTP 原文
      </Button>
    </div>
  );
}

// RawPane 展示一份未加工的文本。这里刻意不用 FormattedPayload:后者复制的是
// 重新排版后的 JSON,而这一页的目的是把原始字节整段复制出去做复现。
function RawPane({
  title,
  hint,
  text,
  onCopy,
}: {
  title: string;
  hint: string;
  text: string;
  onCopy: (text: string) => void;
}) {
  return (
    <Card className="flex min-h-0 flex-col">
      <CardHeader className="flex flex-row items-center justify-between">
        <div className="min-w-0">
          <CardTitle className="text-sm">{title}</CardTitle>
          <p className="text-muted-foreground mt-0.5 text-xs">{hint}</p>
        </div>
        <Button variant="ghost" size="sm" disabled={!text} onClick={() => onCopy(text)}>
          <CopyIcon className="size-4" />
          复制
        </Button>
      </CardHeader>
      <CardContent className="min-h-0">
        {text ? (
          <pre className="max-h-[60vh] overflow-auto rounded-md border bg-muted/20 p-3 font-mono text-xs break-all whitespace-pre-wrap">
            {text}
          </pre>
        ) : (
          <p className="text-muted-foreground py-10 text-center text-sm">
            <RadioIcon className="mx-auto mb-2 size-6 opacity-40" />
            没有这一侧的原文。
          </p>
        )}
      </CardContent>
    </Card>
  );
}
