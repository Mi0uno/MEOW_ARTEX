"use client";

// 记忆库 · 任务复盘页签:任务终态的一次复盘记录(与任务 1:1)。
// 状态筛选 + 分页;行内「AI 重新复盘」触发 LLM 复盘(202 异步,进行中 409),
// 详情弹层展示 摘要/做成了/失败与教训/未走完的线索/统计/复盘错误。

import * as React from "react";

import { CheckIcon, EyeIcon, RefreshCwIcon, TriangleAlertIcon } from "lucide-react";
import { toast } from "sonner";

import { TablePagination } from "@/components/table-pagination";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { MemoryRun } from "@/lib/types";

// 复盘状态中文标签与徽标(pending=进行中 次要+呼吸,done=已完成 默认,
// failed=复盘失败 危险,skipped=未复盘 描边)。
function RunStatusBadge({ status }: { status: string }) {
  if (status === "pending") {
    return (
      <Badge variant="secondary" className="animate-pulse">
        进行中
      </Badge>
    );
  }
  if (status === "failed") return <Badge variant="destructive">复盘失败</Badge>;
  if (status === "skipped") return <Badge variant="outline">未复盘</Badge>;
  return <Badge>已完成</Badge>;
}

// 阶段中文标签('' = 通用/跨阶段任务)。
const STAGE_LABELS: Record<string, string> = {
  "": "通用",
  recon: "信息搜集",
  exploit: "渗透",
  post: "后渗透",
  report: "报告",
};

const SOURCE_LABELS: Record<string, string> = {
  ai: "AI",
  backfill: "回填",
  manual: "人工",
};

// 复盘 stats 常见键的中文标签(未知键原样展示)。
const STAT_LABELS: Record<string, string> = {
  tokens_in: "输入 tokens",
  tokens_out: "输出 tokens",
  findings: "发现数",
  assets: "资产数",
  error_count: "错误次数",
};

function fmtTime(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

function fmtTimeShort(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

export function RunsTab({ companyId, onChanged }: { companyId: number; onChanged?: () => void }) {
  const [runs, setRuns] = React.useState<MemoryRun[]>([]);
  const [total, setTotal] = React.useState(0);
  const [status, setStatus] = React.useState("all");
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(20);
  const [loading, setLoading] = React.useState(true);
  const [detail, setDetail] = React.useState<MemoryRun | null>(null);
  const [rerunningID, setRerunningID] = React.useState<number | null>(null);

  // 状态筛选走服务端参数(/api/memory/runs?status=),total 由后端在过滤后计算。
  const load = React.useCallback(
    (p: number, size: number, st: string) => {
      setLoading(true);
      api
        .memoryRuns({
          companyId: companyId || undefined,
          status: st === "all" ? undefined : st,
          limit: size,
          offset: (p - 1) * size,
        })
        .then((r) => {
          setRuns(r.runs);
          setTotal(r.total);
        })
        .catch((e) => toast.error(`加载复盘失败:${(e as Error).message}`))
        .finally(() => setLoading(false));
    },
    [companyId],
  );

  React.useEffect(() => {
    load(page, pageSize, status);
  }, [page, pageSize, status, load]);

  const reload = () => {
    load(page, pageSize, status);
    onChanged?.();
  };

  // AI 重新复盘:202 异步;409 = 该任务已在复盘进行中;3 秒后刷新看结果。
  const rerun = async (run: MemoryRun) => {
    setRerunningID(run.id);
    try {
      await api.memoryRerunReflect(run.id);
      toast.success("已触发 AI 重新复盘,稍后自动刷新");
      setTimeout(() => {
        load(page, pageSize, status);
        onChanged?.();
      }, 3000);
    } catch (e) {
      const msg = (e as Error).message;
      if (msg.includes("进行中")) toast.info("复盘进行中,请稍候");
      else toast.error(`触发失败:${msg}`);
    } finally {
      setRerunningID(null);
    }
  };

  const detailFailures = detail?.failures ?? [];
  const detailHighlights = detail?.highlights ?? [];
  const detailLeads = detail?.open_leads ?? [];
  const detailStats = Object.entries(detail?.stats ?? {});

  return (
    <Card>
      <CardContent className="px-0 pt-4">
        {/* 工具栏:状态筛选 + 计数。 */}
        <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
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
                <SelectItem value="all">全部状态</SelectItem>
                <SelectItem value="pending">进行中</SelectItem>
                <SelectItem value="done">已完成</SelectItem>
                <SelectItem value="failed">复盘失败</SelectItem>
                <SelectItem value="skipped">未复盘</SelectItem>
              </SelectGroup>
            </SelectContent>
          </Select>
          <span className="text-muted-foreground ml-auto text-xs tabular-nums">共 {total} 条</span>
        </div>

        {loading && runs.length === 0 ? (
          <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
        ) : runs.length === 0 ? (
          <p className="text-muted-foreground py-16 text-center text-sm">
            还没有任务复盘。开启「AI 自动复盘」后,agent 任务终态会自动复盘沉淀;历史任务由开机回填补齐。
          </p>
        ) : (
          <>
            <Table className="table-fixed text-xs">
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="w-16">任务</TableHead>
                  <TableHead className="w-20">阶段</TableHead>
                  <TableHead className="w-24">状态</TableHead>
                  <TableHead className="w-16">来源</TableHead>
                  {/* 摘要列不设宽度:table-fixed 下它吃掉其余列之外的余量。 */}
                  <TableHead>摘要</TableHead>
                  <TableHead className="w-28">时间</TableHead>
                  <TableHead className="w-28">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {runs.map((r) => (
                  <TableRow key={r.id}>
                    <TableCell className="truncate font-medium tabular-nums" title={`任务 #${r.task_id}`}>
                      #{r.task_id}
                    </TableCell>
                    <TableCell className="truncate" title={r.stage}>
                      {(STAGE_LABELS[r.stage] ?? r.stage) || "—"}
                    </TableCell>
                    <TableCell>
                      <RunStatusBadge status={r.status} />
                    </TableCell>
                    <TableCell className="text-muted-foreground">{SOURCE_LABELS[r.source] ?? r.source}</TableCell>
                    <TableCell className="truncate" title={r.summary}>
                      {r.summary || "—"}
                    </TableCell>
                    <TableCell className="text-muted-foreground truncate" title={fmtTime(r.updated_at)}>
                      {fmtTimeShort(r.updated_at)}
                    </TableCell>
                    <TableCell>
                      <div className="flex items-center gap-1">
                        <Button variant="ghost" size="sm" title="查看复盘详情" onClick={() => setDetail(r)}>
                          <EyeIcon className="text-muted-foreground size-3.5" />
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          title="AI 重新复盘(LLM 重新归纳这次任务)"
                          disabled={rerunningID === r.id || r.status === "pending"}
                          onClick={() => void rerun(r)}
                        >
                          <RefreshCwIcon className={`text-muted-foreground size-3.5 ${rerunningID === r.id ? "animate-spin" : ""}`} />
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

      {/* 复盘详情弹层:摘要 / 做成了 / 失败与教训 / 未走完的线索 / 统计 / 复盘错误。 */}
      <Dialog open={!!detail} onOpenChange={(o) => !o && setDetail(null)}>
        <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle className="flex flex-wrap items-center gap-2">
              任务复盘 · #{detail?.task_id}
              {detail && <RunStatusBadge status={detail.status} />}
            </DialogTitle>
            <DialogDescription>
              {detail
                ? `${(STAGE_LABELS[detail.stage] ?? detail.stage) || "通用"} · 来源 ${SOURCE_LABELS[detail.source] ?? detail.source} · 更新于 ${fmtTime(detail.updated_at)}`
                : ""}
            </DialogDescription>
          </DialogHeader>
          {detail && (
            <div className="flex flex-col gap-3 text-sm">
              <section className="flex flex-col gap-1 rounded-md border p-3">
                <div className="text-xs font-medium text-muted-foreground">摘要</div>
                <p className="break-words whitespace-pre-wrap">{detail.summary || "—"}</p>
              </section>

              {detailHighlights.length > 0 && (
                <section className="flex flex-col gap-1 rounded-md border p-3">
                  <div className="text-xs font-medium text-muted-foreground">做成了</div>
                  <ul className="flex flex-col gap-1">
                    {detailHighlights.map((h, i) => (
                      <li key={i} className="flex items-start gap-1.5">
                        <CheckIcon className="mt-0.5 size-3.5 shrink-0 text-emerald-500" />
                        <span className="break-words">{h}</span>
                      </li>
                    ))}
                  </ul>
                </section>
              )}

              {detailFailures.length > 0 && (
                <section className="flex flex-col gap-1 rounded-md border p-3">
                  <div className="text-xs font-medium text-muted-foreground">失败与教训(自进化最值钱的数据)</div>
                  <Table className="table-fixed text-xs">
                    <TableHeader>
                      <TableRow className="hover:bg-transparent">
                        <TableHead className="w-24">尝试了什么</TableHead>
                        <TableHead className="w-24">根因</TableHead>
                        <TableHead>代价</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {detailFailures.map((f, i) => (
                        <TableRow key={i}>
                          <TableCell className="truncate" title={f.what}>
                            {f.what || "—"}
                          </TableCell>
                          <TableCell className="truncate" title={f.root_cause}>
                            {f.root_cause || "—"}
                          </TableCell>
                          <TableCell className="truncate" title={f.cost_note}>
                            {f.cost_note || "—"}
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </section>
              )}

              {detailLeads.length > 0 && (
                <section className="flex flex-col gap-1 rounded-md border p-3">
                  <div className="text-xs font-medium text-muted-foreground">未走完的线索</div>
                  <ul className="flex flex-col gap-1">
                    {detailLeads.map((l, i) => (
                      <li key={i} className="break-words">
                        • {l}
                      </li>
                    ))}
                  </ul>
                </section>
              )}

              {detailStats.length > 0 && (
                <section className="flex flex-col gap-1 rounded-md border p-3">
                  <div className="text-xs font-medium text-muted-foreground">统计</div>
                  <div className="grid grid-cols-2 gap-x-4 gap-y-1 sm:grid-cols-3">
                    {detailStats.map(([k, v]) => (
                      <div key={k} className="flex items-baseline justify-between gap-2 text-xs">
                        <span className="text-muted-foreground">{STAT_LABELS[k] ?? k}</span>
                        <span className="tabular-nums font-medium">{typeof v === "number" ? v : String(v ?? "—")}</span>
                      </div>
                    ))}
                  </div>
                </section>
              )}

              {detail.status === "failed" && detail.reflect_error && (
                <div className="flex items-start gap-2 rounded-md border border-red-500/30 bg-red-500/5 p-3 text-xs">
                  <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0 text-red-500" />
                  <div className="flex flex-col gap-1">
                    <span className="font-medium text-red-500">复盘错误</span>
                    <p className="break-words text-red-500/90">{detail.reflect_error}</p>
                  </div>
                </div>
              )}
            </div>
          )}
        </DialogContent>
      </Dialog>
    </Card>
  );
}
