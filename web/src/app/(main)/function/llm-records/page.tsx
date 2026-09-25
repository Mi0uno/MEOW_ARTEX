"use client";

// LLM 录制:LLM 调用记录列表。
//
// 这一页只负责「找到那条记录」——分类、筛选、搜索、分页;单条记录的完整请求 /
// 响应原文收在详情页(/function/llm-records/detail?id=)。原先的内联 split 面板
// 之所以撤掉,是因为它把列表挤成半屏、又要同时承担检索与阅读两件事。
//
// 关于分类计数(重要):后端 GET /llm/records 只支持 model / session / task 三个
// 筛选参数,不返回按状态聚合的计数。因此状态分类的计数取自「当前页返回的原始
// 数据」(未按状态过滤),它不受选中的分类影响(切到「失败」不会让「成功」归零),
// 但也不是全量数字。界面上以「本页」措辞标明范围,不冒充全量。

import * as React from "react";

import Link from "next/link";

import { ArrowRightIcon, CopyIcon, Loader2Icon, RadioIcon, SearchIcon, SearchXIcon, Trash2Icon } from "lucide-react";
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
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { LLMRecordItem, LLMTask } from "@/lib/types";
import { cn, copyText } from "@/lib/utils";

import { fmtLatency, fmtTime, fmtTokens, isOk, isSlow, StatusBadge } from "./_components/llm-record-shared";

const PAGE_SIZES = [25, 50, 100];

// 状态分类的取值:all 表示不按状态过滤。
type StatusFilter = "all" | "ok" | "error";

// 行内「任务 / 会话」列的展示文本:两者都有时用 · 连接,超长由 truncate 收尾。
function taskSessionText(rec: LLMRecordItem): string {
  const parts: string[] = [];
  if (rec.task_id) parts.push(`#${rec.task_id}`);
  if (rec.session_id) parts.push(rec.session_id);
  return parts.join(" · ") || "—";
}

export default function LLMRecordsPage() {
  // 分页:TablePagination 是 1-based,后端 api.llmRecords 的 page 是 0-based,
  // 转换只发生在调用处(见下方注释),这里统一用 1-based 以便与组件对齐。
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(50);

  const [keyword, setKeyword] = React.useState("");
  // keywordQ 是「按下 Enter / 点搜索」后才生效的关键字,避免每敲一个字就重排列表。
  const [keywordQ, setKeywordQ] = React.useState("");
  const [session, setSession] = React.useState("");
  const [sessionQ, setSessionQ] = React.useState("");
  const [model, setModel] = React.useState("");
  const [modelQ, setModelQ] = React.useState("");
  const [task, setTask] = React.useState("");
  const [status, setStatus] = React.useState<StatusFilter>("all");

  const [records, setRecords] = React.useState<LLMRecordItem[]>([]);
  const [total, setTotal] = React.useState(0);
  const [loading, setLoading] = React.useState(true);
  const [reloadTick, setReloadTick] = React.useState(0); // 手动重新拉取(删除后)

  // 录制开关(settings.llm_record,默认关闭)。关闭后后端不再写入新记录,已有记录保留。
  const [recEnabled, setRecEnabled] = React.useState(false);
  const [recBusy, setRecBusy] = React.useState(false);

  // 按任务删除:deleteTarget 可能是工具栏选中的任务,也可能是行内点删除的那条记录的任务。
  const [tasks, setTasks] = React.useState<LLMTask[]>([]);
  const [deleteTarget, setDeleteTarget] = React.useState("");
  const [deleteOpen, setDeleteOpen] = React.useState(false);
  const [deleting, setDeleting] = React.useState(false);

  // 行级勾选批量删除:按记录 id 记勾选,跨页保留;命名避开任务级删除的 delete* 一族。
  const [checkedIds, setCheckedIds] = React.useState<Set<number>>(() => new Set());
  const [bulkDeleteOpen, setBulkDeleteOpen] = React.useState(false);
  const [bulkDeleting, setBulkDeleting] = React.useState(false);

  const toggleChecked = (id: number, checked: boolean) => {
    setCheckedIds((prev) => {
      const next = new Set(prev);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  };

  const toggleCheckedPage = (ids: number[], checked: boolean) => {
    setCheckedIds((prev) => {
      const next = new Set(prev);
      for (const id of ids) {
        if (checked) next.add(id);
        else next.delete(id);
      }
      return next;
    });
  };

  // Load recording toggle state on mount.
  React.useEffect(() => {
    let alive = true;
    api
      .settings()
      .then((s) => {
        if (alive) setRecEnabled(!!s.llm_record);
      })
      .catch(() => {
        // 开关状态读不到不影响记录浏览,静默按「已关闭」显示。
      });
    return () => {
      alive = false;
    };
  }, []);

  const toggleRecording = async (on: boolean) => {
    setRecBusy(true);
    setRecEnabled(on); // optimistic
    try {
      const s = await api.setSettings({ llm_record: on });
      setRecEnabled(!!s.llm_record);
      toast.success(on ? "已开启录制" : "已关闭录制");
    } catch (e) {
      setRecEnabled(!on); // revert on failure
      toast.error(`切换录制开关失败:${(e as Error).message}`);
    } finally {
      setRecBusy(false);
    }
  };

  // 会话 / 模型是服务端筛选(会话走 ILIKE 子串,模型走精确匹配),输入防抖后再查,
  // 避免每敲一个字符打一次库;筛选变化必须回到第 1 页,否则可能停在一个空页。
  React.useEffect(() => {
    const t = setTimeout(() => {
      setSessionQ(session.trim());
      setPage(1);
    }, 300);
    return () => clearTimeout(t);
  }, [session]);

  React.useEffect(() => {
    const t = setTimeout(() => {
      setModelQ(model.trim());
      setPage(1);
    }, 300);
    return () => clearTimeout(t);
  }, [model]);

  // Load list.
  React.useEffect(() => {
    let alive = true;
    setLoading(true);
    api
      .llmRecords({
        model: modelQ || undefined,
        session: sessionQ || undefined,
        task: task || undefined,
        // ⚠️ off-by-one:TablePagination 的 page 是 1-based,而后端 ListLLMRecords
        // 用 `offset := page * size` 算偏移,即 0-based。这里必须减 1,否则第 1 页
        // 会直接请求到第 2 页的数据(而且第一页永远看不到)。
        page: page - 1,
        size: pageSize,
      })
      .then((r) => {
        if (!alive) return;
        setRecords(r.records ?? []);
        setTotal(r.total ?? 0);
      })
      .catch((e) => {
        if (alive) toast.error(`加载 LLM 记录失败:${(e as Error).message}`);
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [page, pageSize, sessionQ, modelQ, task, reloadTick]);

  // 任务清单(带每条任务的全量记录数),用于筛选下拉与删除影响面提示。
  React.useEffect(() => {
    let alive = true;
    api
      .llmTasks()
      .then((r) => {
        if (alive) setTasks(r.tasks ?? []);
      })
      .catch(() => {
        // 任务下拉拉不到时仍可用其余筛选,静默降级为「暂无任务记录」。
      });
    return () => {
      alive = false;
    };
  }, [reloadTick]);

  // 状态分类计数:取自当前页的原始数据(未按状态过滤),所以切分类不会让别的分类归零。
  const statusCounts = React.useMemo(() => {
    let ok = 0;
    for (const r of records) if (isOk(r.status)) ok++;
    return { all: records.length, ok, error: records.length - ok };
  }, [records]);

  // 状态与关键字是「当前页内」的过滤:后端没有 status 参数,也没有跨字段关键字参数。
  // 全量检索请用上面的会话 / 模型 / 任务筛选(它们是服务端过滤)。
  const shown = React.useMemo(() => {
    const kw = keywordQ.trim().toLowerCase();
    return records.filter((r) => {
      if (status === "ok" && !isOk(r.status)) return false;
      if (status === "error" && isOk(r.status)) return false;
      if (!kw) return true;
      return [r.model, r.session_id, r.task_id].some((v) => (v ?? "").toLowerCase().includes(kw));
    });
  }, [records, status, keywordQ]);

  const serverFiltered = !!(sessionQ || modelQ || task);
  const clientFiltered = status !== "all" || !!keywordQ;

  const runSearch = () => {
    setKeywordQ(keyword.trim());
    setPage(1);
  };

  const removeTaskRecords = (taskID: string) => {
    if (!taskID) return;
    setDeleteTarget(taskID);
    setDeleteOpen(true);
  };

  // Delete every LLM record for the target task, then refetch.
  const confirmDelete = () => {
    if (!deleteTarget) return;
    setDeleting(true);
    api
      .llmRecordsDeleteTask(deleteTarget)
      .then(() => {
        toast.success(`已删除任务 #${deleteTarget} 的全部记录`);
        setDeleteOpen(false);
        setDeleteTarget("");
        // 删掉的正是当前筛选的任务时清掉筛选,免得停在一个必然为空的列表上。
        setTask((cur) => (cur === deleteTarget ? "" : cur));
        setPage(1);
        setReloadTick((t) => t + 1);
      })
      .catch((e) => toast.error(`删除失败:${(e as Error).message}`))
      .finally(() => setDeleting(false));
  };

  const copyRowValue = async (value: string, label: string) => {
    const ok = await copyText(value);
    toast[ok ? "success" : "error"](ok ? `已复制${label}` : "复制失败，请手动选择文本复制");
  };

  // 批量删除勾选的记录:后端按 id 集合一次删除,返回实际删除数;部分失败按返回数汇报。
  const confirmBulkDelete = () => {
    const ids = [...checkedIds];
    if (ids.length === 0) return;
    setBulkDeleting(true);
    api
      .deleteLlmRecords(ids)
      .then((r) => {
        const deleted = r?.deleted ?? ids.length;
        const failed = ids.length - deleted;
        if (failed > 0) toast.warning(`成功 ${deleted} 失败 ${failed}`);
        else toast.success(`已删除 ${deleted} 项`);
        setCheckedIds(new Set());
        setBulkDeleteOpen(false);
        setReloadTick((t) => t + 1);
      })
      .catch((e) => toast.error(`删除失败:${(e as Error).message}`))
      .finally(() => setBulkDeleting(false));
  };

  const deleteCount = tasks.find((t) => t.task_id === deleteTarget)?.count;

  // 表头全选的范围 = 当前页可见行(shown):客户端筛选隐藏的行不进全选,避免删到看不见的记录。
  const shownIds = shown.map((r) => r.id);
  const checkedOnPage = shownIds.filter((id) => checkedIds.has(id)).length;
  let pageHeaderChecked: boolean | "indeterminate" = false;
  if (shownIds.length > 0 && checkedOnPage === shownIds.length) {
    pageHeaderChecked = true;
  } else if (checkedOnPage > 0) {
    pageHeaderChecked = "indeterminate";
  }

  // 列表三态在 JSX 外算好:嵌套三元既难读又触发 noNestedTernary,而且「确实没有记录」
  // 与「搜索无命中」必须给出不同出口(后者要提示放宽筛选),不能共用一个空态。
  let body: React.ReactNode;
  if (loading && records.length === 0) {
    body = (
      <div className="text-muted-foreground flex items-center justify-center gap-2 py-16 text-sm">
        <Loader2Icon className="size-4 animate-spin" />
        加载中…
      </div>
    );
  } else if (records.length === 0) {
    body = (
      <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
        {serverFiltered ? <SearchXIcon className="size-8 opacity-40" /> : <RadioIcon className="size-8 opacity-40" />}
        <span>
          {serverFiltered
            ? "没有匹配的 LLM 调用记录。放宽或清空上方筛选条件后再试。"
            : "还没有 LLM 调用记录。打开右上角「录制」开关后,后续每次 LLM 调用都会记录在这里。"}
        </span>
      </div>
    );
  } else if (shown.length === 0) {
    body = (
      <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
        <SearchXIcon className="size-8 opacity-40" />
        <span>本页没有匹配的记录。关键字与状态分类只作用于当前页,可翻页或改用「会话 / 模型 / 任务」做全量筛选。</span>
      </div>
    );
  } else {
    body = (
      <>
        {/* table-fixed 列宽:各列按最小可读值给固定宽,宽度之和 784px 明显小于
            1280 视口下的内容区(~897px),不会撑出横向滚动条;模型与任务·会话两列
            内容可能很长,统一用 truncate + title 收尾。 */}
        <Table className="table-fixed">
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="w-8">
                <Checkbox
                  checked={pageHeaderChecked}
                  onCheckedChange={(v) => toggleCheckedPage(shownIds, v === true)}
                  aria-label="选择当前页全部"
                />
              </TableHead>
              <TableHead className="w-36">时间</TableHead>
              <TableHead className="w-40">模型</TableHead>
              <TableHead className="w-32">任务 / 会话</TableHead>
              <TableHead className="w-20">状态</TableHead>
              <TableHead className="w-20">延迟</TableHead>
              <TableHead className="w-24">Tokens</TableHead>
              <TableHead className="w-24">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {shown.map((rec) => {
              const copyValue = rec.session_id || rec.task_id;
              const copyLabel = rec.session_id ? "会话 ID" : "任务 ID";
              return (
                <TableRow key={rec.id}>
                  <TableCell onClick={(e) => e.stopPropagation()}>
                    <Checkbox
                      checked={checkedIds.has(rec.id)}
                      onCheckedChange={(v) => toggleChecked(rec.id, v === true)}
                      aria-label="选择该记录"
                    />
                  </TableCell>
                  <TableCell className="text-muted-foreground text-xs tabular-nums">{fmtTime(rec.ts)}</TableCell>
                  {/* 模型列即主入口:点进详情页看完整请求 / 响应 */}
                  <TableCell className="min-w-0">
                    <Link
                      href={`/function/llm-records/detail?id=${rec.id}`}
                      className="block w-full truncate font-mono text-xs hover:underline"
                      title={rec.model || "—"}
                    >
                      {rec.model || "—"}
                    </Link>
                  </TableCell>
                  <TableCell className="min-w-0">
                    <span
                      className="text-muted-foreground block truncate font-mono text-xs"
                      title={taskSessionText(rec)}
                    >
                      {taskSessionText(rec)}
                    </span>
                  </TableCell>
                  <TableCell>
                    <StatusBadge status={rec.status} />
                  </TableCell>
                  <TableCell className={cn("text-xs tabular-nums", isSlow(rec.latency_ms) && "text-amber-500")}>
                    {fmtLatency(rec.latency_ms)}
                  </TableCell>
                  <TableCell
                    className="truncate text-xs tabular-nums"
                    title={`输入 ${rec.input_tokens} / 输出 ${rec.output_tokens}`}
                  >
                    {fmtTokens(rec.input_tokens)} / {fmtTokens(rec.output_tokens)}
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center gap-0.5">
                      {/* 列表接口不返回请求/响应正文,这里能复制的只有标识符 */}
                      <Button
                        variant="ghost"
                        size="icon-xs"
                        disabled={!copyValue}
                        title={copyValue ? `复制${copyLabel}` : "该记录没有会话 / 任务 ID"}
                        onClick={() => void copyRowValue(copyValue, copyLabel)}
                      >
                        <CopyIcon className="size-3.5" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon-xs"
                        disabled={!rec.task_id}
                        title={
                          rec.task_id ? `删除任务 #${rec.task_id} 的全部记录` : "该记录没有任务归属,无法按任务删除"
                        }
                        onClick={() => removeTaskRecords(rec.task_id)}
                      >
                        <Trash2Icon className="text-muted-foreground size-3.5" />
                      </Button>
                      <Button variant="ghost" size="icon-xs" asChild title="查看详情">
                        <Link href={`/function/llm-records/detail?id=${rec.id}`} aria-label="查看详情">
                          <ArrowRightIcon className="size-3.5" />
                        </Link>
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
          pageSizeOptions={PAGE_SIZES}
          onPageChange={setPage}
          onPageSizeChange={(s) => {
            setPage(1);
            setPageSize(s);
          }}
        />
      </>
    );
  }

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div>
          <h1 className="font-semibold text-xl">LLM 录制</h1>
          <p className="text-muted-foreground text-sm">
            记录每次 LLM 调用的模型、延迟与 token 用量,并保留请求 / 响应原文。点模型名进入详情页查看完整报文。
          </p>
        </div>
        {/* 录制开关是整页的总闸:关闭后后端不再写新记录(已有记录保留)。 */}
        <div className="flex items-center gap-2 rounded-md border px-2.5 py-1.5">
          <Switch
            id="llm-rec-toggle"
            size="sm"
            checked={recEnabled}
            onCheckedChange={toggleRecording}
            disabled={recBusy}
          />
          <label
            htmlFor="llm-rec-toggle"
            className={cn(
              "cursor-pointer text-xs font-medium select-none",
              recEnabled ? "text-foreground" : "text-muted-foreground",
            )}
          >
            {recEnabled ? "录制中" : "已关闭"}
          </label>
        </div>
      </div>

      <Card>
        <CardContent className="px-0 pt-4">
          <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
            <div className="relative w-full sm:max-w-xs">
              <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
              <Input
                value={keyword}
                onChange={(e) => setKeyword(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && runSearch()}
                placeholder="搜索模型 / 会话 / 任务 ID"
                title="在当前页内按模型、会话 ID、任务 ID 匹配(Enter 生效)"
                className="pl-8"
              />
            </div>
            <Button variant="outline" size="sm" onClick={runSearch}>
              搜索
            </Button>

            {/* 状态分类器:可点页签 + 计数。计数取自当前页(后端无按状态聚合的接口),
                所以加「本页」字样标明范围,避免被读成全站总数。 */}
            <div className="flex items-center gap-1.5">
              <span className="text-muted-foreground text-xs">本页</span>
              <Tabs
                value={status}
                onValueChange={(v) => {
                  setPage(1);
                  setStatus(v as StatusFilter);
                }}
              >
                <TabsList title="计数为当前页统计:后端未提供按状态聚合的接口">
                  <TabsTrigger value="all">全部 {statusCounts.all}</TabsTrigger>
                  <TabsTrigger value="ok">成功 {statusCounts.ok}</TabsTrigger>
                  <TabsTrigger value="error">失败 {statusCounts.error}</TabsTrigger>
                </TabsList>
              </Tabs>
            </div>

            {/* 会话 / 模型 / 任务是服务端筛选:全量生效,不受当前页限制 */}
            <Input
              value={session}
              onChange={(e) => setSession(e.target.value)}
              placeholder="会话 ID(全量)"
              title="按会话 ID 全量检索(子串匹配)"
              className="w-44"
            />
            <Input
              value={model}
              onChange={(e) => setModel(e.target.value)}
              placeholder="模型(全量)"
              title="按模型名全量检索(精确匹配)"
              className="w-40"
            />
            <Select
              value={task || "all"}
              onValueChange={(v) => {
                setPage(1);
                setTask(v === "all" ? "" : v);
              }}
            >
              <SelectTrigger className="w-48">
                <SelectValue placeholder="全部任务" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">全部任务</SelectItem>
                {tasks.length === 0 ? (
                  <SelectItem value="__none__" disabled>
                    暂无任务记录
                  </SelectItem>
                ) : (
                  tasks.map((t) => (
                    <SelectItem key={t.task_id} value={t.task_id}>
                      <span className="font-mono">#{t.task_id}</span>
                      <span className="text-muted-foreground">（{t.count}）</span>
                    </SelectItem>
                  ))
                )}
              </SelectContent>
            </Select>
            {/* 按任务删除:后端只有任务级删除(没有单条删除接口),所以入口固定绑定
                一个任务,并在确认框里写明会连带删掉该任务的全部记录。 */}
            <Button
              variant="outline"
              size="sm"
              className="text-muted-foreground"
              disabled={!task || deleting}
              title={task ? `删除任务 #${task} 的全部记录` : "先选择任务"}
              onClick={() => removeTaskRecords(task)}
            >
              <Trash2Icon className="size-4" />
              删除任务对话
            </Button>

            {/* 行级批量删除:只作用于勾选的记录,与上面的任务级整删互不影响。 */}
            {checkedIds.size > 0 && (
              <Button
                variant="destructive"
                size="sm"
                disabled={bulkDeleting}
                onClick={() => setBulkDeleteOpen(true)}
              >
                <Trash2Icon className="size-4" />
                删除已选 (<span className="tabular-nums">{checkedIds.size}</span>)
              </Button>
            )}

            <span className="text-muted-foreground ml-auto text-xs tabular-nums">
              {clientFiltered
                ? `本页 ${shown.length} / 共 ${total} 条`
                : `${serverFiltered ? `命中 ${total} / ` : ""}共 ${total} 条`}
            </span>
          </div>

          {body}
        </CardContent>
      </Card>

      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除任务「{deleteTarget}」的全部 LLM 对话？</AlertDialogTitle>
            <AlertDialogDescription>
              将永久删除该任务的
              {deleteCount === undefined ? "全部" : ` ${deleteCount} 条`}
              LLM 调用记录（含请求 / 响应原文），此操作不可撤销。
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

      <AlertDialog
        open={bulkDeleteOpen}
        onOpenChange={(open) => {
          if (!bulkDeleting) setBulkDeleteOpen(open);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除已选中的 {checkedIds.size} 条 LLM 记录？</AlertDialogTitle>
            <AlertDialogDescription>
              将永久删除选中的 {checkedIds.size} 条 LLM 调用记录（含请求 / 响应原文），此操作不可撤销。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={bulkDeleting}>取消</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault();
                confirmBulkDelete();
              }}
              disabled={bulkDeleting}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {bulkDeleting ? "删除中…" : "确认删除"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
