"use client";

import * as React from "react";

import {
  BotIcon,
  CheckIcon,
  ChevronDownIcon,
  ChevronRightIcon,
  ClipboardListIcon,
  CopyIcon,
  FileCodeIcon,
  RefreshCwIcon,
  ShieldAlertIcon,
  XIcon,
} from "lucide-react";
import { toast } from "sonner";

import { ApprovalDetail } from "@/components/approval-records";
import { TablePagination } from "@/components/table-pagination";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import { extractScriptSections, intentSummary } from "@/lib/approval-script";
import type { ApprovalScriptSection } from "@/lib/approval-script";
import type {
  InterceptApprovalFilter,
  InterceptApprovalRow,
  InterceptConsoleCounts,
  InterceptPending,
  InterceptToolCount,
} from "@/lib/types";
import { cn } from "@/lib/utils";

type Decide = (id: number, decision: "allowed" | "denied") => Promise<void>;

// 展开详情行用常量 colSpan：本表无响应式隐藏列（契约 §4.10，不做 approvalBreakpoints）。
const CONSOLE_COLUMNS = 9;

const SEARCH_DEBOUNCE_MS = 400;

const STATUS_TABS: { key: "all" | keyof InterceptConsoleCounts; label: string }[] = [
  { key: "all", label: "全部" },
  { key: "pending", label: "待审批" },
  { key: "allowed", label: "已允许" },
  { key: "denied", label: "已拒绝" },
  { key: "timeout", label: "已超时" },
  { key: "flagged", label: "已判疑" },
];

const stageLabels: Record<string, string> = {
  recon: "搜集",
  exploit: "渗透",
  post: "后渗透",
  report: "报告",
};

function fmtTime(value?: string) {
  if (!value) return "—";
  return new Date(value).toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

function sourceOf(row: InterceptApprovalRow) {
  if (row.decision_source) return row.decision_source;
  if (row.rule_id) return "rule";
  return row.reason?.startsWith("[模型]") ? "model" : "unknown";
}

function StatusBadge({ status }: { status: string }) {
  const labels: Record<string, string> = {
    pending: "待审批",
    allowed: "已允许",
    denied: "已拒绝",
    timeout: "已超时",
    flagged: "已判疑",
  };
  let variant: "default" | "destructive" | "secondary" | "outline" = "outline";
  if (status === "allowed") variant = "default";
  if (status === "denied") variant = "destructive";
  if (status === "pending") variant = "secondary";
  const className = status === "flagged" ? "border-amber-400 text-amber-600 dark:text-amber-400" : undefined;
  return (
    <Badge variant={variant} className={className}>
      {labels[status] ?? status}
    </Badge>
  );
}

function MatchCell({ row }: { row: InterceptApprovalRow }) {
  const reason = row.reason?.replace(/^\[模型\]\s*/, "");
  return (
    <div className="flex min-w-0 flex-col gap-1">
      {sourceOf(row) === "model" ? (
        <Badge variant="outline">
          <BotIcon />
          模型判定
        </Badge>
      ) : (
        <span className="truncate" title={row.rule_name || undefined}>
          {row.rule_name || "规则未记录或已删除"}
        </span>
      )}
      <p className="truncate text-muted-foreground text-xs" title={reason}>
        {reason || "未记录理由"}
      </p>
    </div>
  );
}

// 任务来源列：任务名优先（+ 阶段/SRC 徽标，整格链接到任务详情）；
// 无任务但有会话 → 会话标题；都没有 → —。次行显示 agent 名。
function TaskOriginCell({ row }: { row: InterceptApprovalRow }) {
  const taskLabel = row.task_name || (row.task_id ? `任务 #${row.task_id}` : "");
  const stage = row.task_stage ? stageLabels[row.task_stage] : undefined;
  const conversationLabel = row.conversation_id
    ? row.conv_title || `对话 #${row.conversation_id}`
    : "";
  return (
    <div className="flex min-w-0 flex-col gap-1">
      {taskLabel ? (
        <a
          href={`/function/tasks/detail?id=${encodeURIComponent(row.task_id ?? "")}`}
          className="flex min-w-0 flex-wrap items-center gap-1 text-primary underline-offset-4 hover:underline"
          title={taskLabel}
          aria-label={`打开任务详情：${taskLabel}`}
          onClick={(e) => e.stopPropagation()}
        >
          <span className="min-w-0 truncate">{taskLabel}</span>
          {stage ? (
            <Badge variant="outline" className="shrink-0 px-1 py-0 text-[10px]">
              {stage}
            </Badge>
          ) : null}
          {row.task_src ? (
            <Badge variant="outline" className="shrink-0 px-1 py-0 text-[10px]">
              SRC
            </Badge>
          ) : null}
        </a>
      ) : conversationLabel ? (
        <span className="min-w-0 truncate" title={conversationLabel}>
          {conversationLabel}
        </span>
      ) : (
        <span className="text-muted-foreground">—</span>
      )}
      <span className="min-w-0 truncate text-muted-foreground text-xs">{row.agent_name || row.conv_agent_key}</span>
    </div>
  );
}

// 内容意图列：一行摘要（title 悬停全文）；命中脚本候选键时追加「脚本」按钮打开查看器。
function IntentCell({
  row,
  onOpenScript,
}: {
  row: InterceptApprovalRow;
  onOpenScript: (row: InterceptApprovalRow, sections: ApprovalScriptSection[]) => void;
}) {
  const summary = intentSummary(row.tool_name, row.tool_input);
  const sections = extractScriptSections(row.tool_name, row.tool_input);
  return (
    <div className="flex min-w-0 items-center gap-1">
      <span className="min-w-0 flex-1 truncate" title={summary || undefined}>
        {summary || "—"}
      </span>
      {sections.length ? (
        <Button
          variant="ghost"
          size="icon-xs"
          aria-label={`查看 ${row.tool_name} #${row.id} 的脚本内容（${sections.length} 段）`}
          onClick={(e) => {
            e.stopPropagation();
            onOpenScript(row, sections);
          }}
        >
          <FileCodeIcon />
        </Button>
      ) : null}
    </div>
  );
}

// 单段脚本视图：行号列 + 原文列；「复制」按钮复制不带行号的原文。
function ScriptSectionView({ section }: { section: ApprovalScriptSection }) {
  const lines = section.text.split("\n");
  async function copy() {
    try {
      await navigator.clipboard.writeText(section.text);
      toast.success("已复制");
    } catch {
      toast.error("复制失败，请手动选择内容复制");
    }
  }
  return (
    <section className="flex min-w-0 flex-col gap-2" aria-label={section.label}>
      <div className="flex items-center justify-between gap-2">
        <h4 className="min-w-0 truncate font-medium text-muted-foreground text-xs">{section.label}</h4>
        <Button variant="ghost" size="icon-xs" aria-label={`复制${section.label}`} onClick={() => void copy()}>
          <CopyIcon />
        </Button>
      </div>
      <div className="max-h-96 min-w-0 overflow-auto rounded-lg bg-muted/60 p-3 font-mono text-xs leading-6">
        {lines.map((line, index) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: 脚本行是纯静态展示，无增删重排。
          <div key={index} className="flex">
            <span
              aria-hidden
              className="w-9 shrink-0 pr-3 text-right text-muted-foreground/70 tabular-nums select-none"
            >
              {index + 1}
            </span>
            <span className="min-w-0 flex-1 break-all whitespace-pre-wrap [overflow-wrap:anywhere]">
              {line === "" ? "\u00A0" : line}
            </span>
          </div>
        ))}
      </div>
    </section>
  );
}

// 脚本查看器：Dialog 壳 + 每段 label + 行号 + 复制 + 空内容占位。审批台表格的
// 「脚本」按钮打开本组件；导出供其他页面复用（ApprovalDetail 的脚本区块走
// Collapsible+CodeBlock，样式对齐详情区既有形态，不经 Dialog）。
export function ScriptDialog({
  open,
  onOpenChange,
  title,
  sections,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  sections: ApprovalScriptSection[];
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl lg:max-w-3xl">
        <DialogHeader>
          <DialogTitle className="min-w-0 break-all">{title || "脚本内容"}</DialogTitle>
          <DialogDescription>按行号查看原始内容；「复制」按钮复制的是不带行号的原文。</DialogDescription>
        </DialogHeader>
        <div className="flex max-h-[60dvh] min-w-0 flex-col gap-4 overflow-y-auto pr-1">
          {sections.length ? (
            sections.map((section) => <ScriptSectionView key={section.label} section={section} />)
          ) : (
            <p className="py-6 text-center text-muted-foreground text-sm">未记录脚本内容</p>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

function ConsoleTable({
  rows,
  busy,
  decide,
  revision,
  label,
  onOpenScript,
}: {
  rows: InterceptApprovalRow[];
  busy: boolean;
  decide: Decide;
  revision: number;
  label: string;
  onOpenScript: (row: InterceptApprovalRow, sections: ApprovalScriptSection[]) => void;
}) {
  const [expanded, setExpanded] = React.useState<Set<number>>(() => new Set());
  const prefix = React.useId();
  function toggle(id: number) {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }
  return (
    <Table className="table-fixed" aria-label={label}>
      <TableHeader>
        <TableRow>
          <TableHead className="w-10">
            <span className="sr-only">展开详情</span>
          </TableHead>
          <TableHead className="w-14">#</TableHead>
          <TableHead className="w-28">工具</TableHead>
          <TableHead className="w-36">任务来源</TableHead>
          <TableHead>内容意图</TableHead>
          <TableHead className="w-36">匹配规则</TableHead>
          <TableHead className="w-24">状态</TableHead>
          <TableHead className="w-36">申请时间</TableHead>
          <TableHead className="w-36">决定时间</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((row) => {
          const open = expanded.has(row.id);
          const panelID = `${prefix}-${row.id}`;
          return (
            <React.Fragment key={row.id}>
              <TableRow
                data-state={open ? "selected" : undefined}
                className="cursor-pointer"
                onClick={() => toggle(row.id)}
              >
                <TableCell>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={`${open ? "收起" : "展开"}审批 #${row.id}`}
                    aria-expanded={open}
                    aria-controls={open ? panelID : undefined}
                    onClick={(e) => {
                      e.stopPropagation();
                      toggle(row.id);
                    }}
                  >
                    {open ? <ChevronDownIcon /> : <ChevronRightIcon />}
                  </Button>
                </TableCell>
                <TableCell className="text-muted-foreground">{row.id}</TableCell>
                <TableCell>
                  <code className="block truncate text-xs" title={row.tool_name}>
                    {row.tool_name}
                  </code>
                </TableCell>
                <TableCell>
                  <TaskOriginCell row={row} />
                </TableCell>
                <TableCell>
                  <IntentCell row={row} onOpenScript={onOpenScript} />
                </TableCell>
                <TableCell>
                  <MatchCell row={row} />
                </TableCell>
                <TableCell>
                  <StatusBadge status={row.status} />
                </TableCell>
                <TableCell className="text-muted-foreground text-xs">{fmtTime(row.created_at)}</TableCell>
                <TableCell className="text-muted-foreground text-xs">{fmtTime(row.decided_at)}</TableCell>
              </TableRow>
              {open ? (
                <TableRow className="hover:bg-transparent has-aria-expanded:bg-transparent">
                  <TableCell colSpan={CONSOLE_COLUMNS} className="whitespace-normal p-0">
                    <section id={panelID} aria-label={`审批详情 #${row.id}`}>
                      <ApprovalDetail row={row} busy={busy} decide={decide} revision={revision} />
                    </section>
                  </TableCell>
                </TableRow>
              ) : null}
            </React.Fragment>
          );
        })}
      </TableBody>
    </Table>
  );
}

export function ApprovalConsole() {
  const [rows, setRows] = React.useState<InterceptApprovalRow[]>([]);
  const [pendingRows, setPendingRows] = React.useState<InterceptPending[]>([]);
  const [counts, setCounts] = React.useState<InterceptConsoleCounts>({
    pending: 0,
    allowed: 0,
    denied: 0,
    timeout: 0,
    flagged: 0,
  });
  const [tools, setTools] = React.useState<InterceptToolCount[]>([]);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(20);
  const [total, setTotal] = React.useState(0);
  const [filter, setFilter] = React.useState<InterceptApprovalFilter>({});
  const [searchText, setSearchText] = React.useState("");
  const [loading, setLoading] = React.useState(true);
  const [refreshing, setRefreshing] = React.useState(false);
  const [error, setError] = React.useState("");
  const [pendingError, setPendingError] = React.useState("");
  const [deciding, setDeciding] = React.useState(false);
  const [revision, setRevision] = React.useState(0);
  const [scriptView, setScriptView] = React.useState<{ title: string; sections: ApprovalScriptSection[] } | null>(null);
  const request = React.useRef(0);
  const decisionLock = React.useRef(false);
  const searchTimer = React.useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const filtered = Boolean(filter.status || filter.decision_source || filter.q || filter.tool);

  const load = React.useCallback(
    async (manual = false) => {
      const id = ++request.current;
      if (manual) setRefreshing(true);
      try {
        // 待处理队列独立于当前页签/筛选：老请求必须保持可裁决，即使新裁决
        // 已把该行移出当前筛选范围。
        const [consolePage, pending] = await Promise.allSettled([
          api.interceptConsole(page, pageSize, filter),
          api.interceptPending(),
        ]);
        if (id !== request.current) return;
        if (consolePage.status === "fulfilled") {
          setRows(consolePage.value.items ?? []);
          setTotal(consolePage.value.total);
          setCounts(consolePage.value.counts);
          setTools(consolePage.value.tools ?? []);
          setError("");
        } else {
          setError((consolePage.reason as Error).message || "加载失败");
        }
        if (pending.status === "fulfilled") {
          setPendingRows(pending.value);
          setPendingError("");
        } else {
          setPendingError((pending.reason as Error).message || "加载失败");
        }
        if (manual) setRevision((v) => v + 1);
      } finally {
        if (id === request.current) {
          setLoading(false);
          setRefreshing(false);
        }
      }
    },
    [page, pageSize, filter],
  );
  const latestLoad = React.useRef(load);
  latestLoad.current = load;

  React.useEffect(() => {
    void load();
    const timer = setInterval(() => void load(), 5000);
    return () => {
      request.current++;
      clearInterval(timer);
    };
  }, [load]);

  React.useEffect(() => () => clearTimeout(searchTimer.current), []);

  const changePageSize = (next: number) => {
    setPageSize(next);
    setPage(1);
  };

  const changeFilter = (next: InterceptApprovalFilter) => {
    // 旧筛选的在途响应不得回填表格。
    request.current++;
    setFilter(next);
    setPage(1);
    setRows([]);
    setTotal(0);
    setError("");
    setLoading(true);
  };

  function commitSearch(value: string) {
    const q = value.trim() || undefined;
    if ((filter.q ?? undefined) === q) return;
    changeFilter({ ...filter, q });
  }

  function onSearchInput(value: string) {
    setSearchText(value);
    clearTimeout(searchTimer.current);
    searchTimer.current = setTimeout(() => commitSearch(value), SEARCH_DEBOUNCE_MS);
  }

  const clearFilters = () => {
    setSearchText("");
    clearTimeout(searchTimer.current);
    changeFilter({});
  };

  const decide: Decide = async (id, decision) => {
    if (decisionLock.current) return;
    decisionLock.current = true;
    setDeciding(true);
    try {
      await api.interceptDecide(id, decision);
      // 作废裁决前发起的列表请求。
      request.current++;
      // 乐观更新当前页行与独立待处理队列，立即反馈；下方重拉以服务端为准。
      setRows((prev) =>
        prev.map((row) => (row.id === id ? { ...row, status: decision, decided_at: new Date().toISOString() } : row)),
      );
      setPendingRows((prev) => prev.filter((row) => row.id !== id));
      setRevision((v) => v + 1);
      toast.success(decision === "allowed" ? "已允许执行" : "已拒绝执行");
      // 重拉计数与行：被裁决的记录可能已不匹配当前筛选。
      await latestLoad.current(true);
    } catch (e) {
      toast.error((e as Error).message);
      await latestLoad.current(true);
    } finally {
      decisionLock.current = false;
      setDeciding(false);
    }
  };

  const openScript = (row: InterceptApprovalRow, sections: ApprovalScriptSection[]) => {
    setScriptView({ title: `${row.tool_name} · 审批 #${row.id}`, sections });
  };

  const rowById = new Map(rows.map((row) => [row.id, row]));
  // 待处理队列只收真正在等用户决定的 pending（后端已只返回 pending，这里再挡一道）。
  const pending: InterceptApprovalRow[] = pendingRows
    .filter((row) => row.status === "pending")
    .map((row) => ({
      conv_title: "",
      conv_agent_key: "",
      rule_name: row.rule_id ? `规则 #${row.rule_id}` : "",
      ...rowById.get(row.id),
      ...row,
    }));

  const totalOfCounts = counts.pending + counts.allowed + counts.denied + counts.timeout + counts.flagged;

  return (
    <div className="flex min-w-0 flex-col gap-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <ClipboardListIcon className="size-5 text-muted-foreground" />
          <h1 className="font-semibold text-xl">审批台</h1>
          {pending.length ? <Badge variant="secondary">{pending.length} 待审批</Badge> : null}
        </div>
        <Button variant="outline" size="sm" onClick={() => void load(true)} disabled={loading || refreshing}>
          <RefreshCwIcon data-icon="inline-start" className={cn(refreshing && "animate-spin")} />
          刷新
        </Button>
      </div>
      <p className="text-muted-foreground text-sm">
        按分类检索拦截审批记录，搜索工具、命令、脚本原文、任务名或审批理由，展开记录查看上下文与执行结果后裁决。
      </p>

      {/* 分类页签：可点按钮（aria-pressed），计数受其他筛选影响、不受页签自身影响。 */}
      <div className="flex flex-wrap items-center gap-1 self-start rounded-lg border border-border p-0.5" role="group" aria-label="审批状态分类">
        {STATUS_TABS.map((tab) => {
          const count = tab.key === "all" ? totalOfCounts : counts[tab.key];
          const active = (filter.status ?? "all") === tab.key;
          return (
            <button
              key={tab.key}
              type="button"
              aria-pressed={active}
              onClick={() => changeFilter({ ...filter, status: tab.key === "all" ? undefined : tab.key })}
              className={cn(
                "rounded-md px-2.5 py-1 font-medium text-xs transition-colors",
                active
                  ? "bg-foreground text-background"
                  : "text-muted-foreground hover:bg-muted hover:text-foreground",
              )}
            >
              {tab.label} <span className="tabular-nums opacity-80">{count}</span>
            </button>
          );
        })}
      </div>

      <FieldGroup className="flex-row flex-wrap items-end gap-3" aria-label="审批台筛选">
        <Field className="w-full sm:w-64">
          <FieldLabel htmlFor="approval-console-search">搜索</FieldLabel>
          <Input
            id="approval-console-search"
            type="search"
            value={searchText}
            placeholder="搜索工具 / 命令 / 脚本 / 任务名 / 理由"
            onChange={(e) => onSearchInput(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                clearTimeout(searchTimer.current);
                commitSearch(searchText);
              }
            }}
          />
        </Field>
        <Field className="w-full sm:w-36">
          <FieldLabel htmlFor="approval-console-source">判定来源</FieldLabel>
          <Select
            value={filter.decision_source ?? "all"}
            onValueChange={(value) =>
              changeFilter({
                ...filter,
                decision_source: value === "all" ? undefined : (value as InterceptApprovalFilter["decision_source"]),
              })
            }
          >
            <SelectTrigger id="approval-console-source">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value="all">全部来源</SelectItem>
                <SelectItem value="model">模型判定</SelectItem>
                <SelectItem value="rule">规则判定</SelectItem>
                <SelectItem value="unknown">来源未知</SelectItem>
              </SelectGroup>
            </SelectContent>
          </Select>
        </Field>
        <Field className="w-full sm:w-44">
          <FieldLabel htmlFor="approval-console-tool">工具</FieldLabel>
          <Select
            value={filter.tool ?? "all"}
            onValueChange={(value) => changeFilter({ ...filter, tool: value === "all" ? undefined : value })}
          >
            <SelectTrigger id="approval-console-tool">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value="all">全部工具</SelectItem>
                {tools.map((tool) => (
                  <SelectItem key={tool.tool} value={tool.tool}>
                    {tool.tool}（{tool.count}）
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
        </Field>
        {filtered ? (
          <Button variant="ghost" size="sm" onClick={clearFilters}>
            清除筛选
          </Button>
        ) : null}
      </FieldGroup>

      {error ? (
        <Alert variant="destructive">
          <AlertDescription>记录加载失败：{error}。请点击刷新重试。</AlertDescription>
        </Alert>
      ) : null}
      {pendingError ? (
        <Alert variant="destructive">
          <AlertDescription>待审批加载失败：{pendingError}。请点击刷新重试。</AlertDescription>
        </Alert>
      ) : null}

      {pending.length ? (
        <section className="overflow-hidden rounded-xl border">
          <div className="flex items-center gap-2 border-b bg-muted/40 px-4 py-3 font-medium text-sm">
            <ShieldAlertIcon className="size-4" />
            待处理（{pending.length}）
            <span className="text-muted-foreground text-xs">展开后允许或拒绝</span>
          </div>
          <ConsoleTable
            rows={pending}
            busy={deciding}
            decide={decide}
            revision={revision}
            label="待处理审批"
            onOpenScript={openScript}
          />
        </section>
      ) : null}

      <section className="overflow-hidden rounded-xl border">
        <div className="border-b px-4 py-3 font-medium text-sm">
          {filtered ? "筛选结果" : "全部记录"}（{total}）
        </div>
        {loading ? (
          <div className="flex flex-col gap-3 p-4">
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-10 w-full" />
          </div>
        ) : null}
        {!loading && !rows.length && !error ? (
          <Empty>
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <ClipboardListIcon />
              </EmptyMedia>
              <EmptyTitle>{filtered ? "没有符合筛选条件的审批记录" : "暂无审批记录"}</EmptyTitle>
              <EmptyDescription>
                {filtered
                  ? "请调整分类页签、搜索词或筛选器，或清除筛选查看全部记录。"
                  : "规则或模型作出审批决定后，记录将显示在这里。"}
              </EmptyDescription>
            </EmptyHeader>
          </Empty>
        ) : null}
        {rows.length ? (
          <ConsoleTable
            rows={rows}
            busy={deciding}
            decide={decide}
            revision={revision}
            label="审批台记录列表"
            onOpenScript={openScript}
          />
        ) : null}
        {!loading && !error ? (
          <TablePagination
            page={page}
            pageSize={pageSize}
            total={total}
            onPageChange={setPage}
            onPageSizeChange={changePageSize}
            pageSizeOptions={[10, 20, 50]}
          />
        ) : null}
      </section>

      <ScriptDialog
        open={Boolean(scriptView)}
        onOpenChange={(open) => {
          if (!open) setScriptView(null);
        }}
        title={scriptView?.title ?? ""}
        sections={scriptView?.sections ?? []}
      />
    </div>
  );
}
