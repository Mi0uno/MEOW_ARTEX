"use client";

// 目标详情 · 成果页签:本目标产出的 SRC 成果(可提交条目)与提交状态。
//
// 只读列表 + 分页。状态/严重度用与「成果产出」页一致的徽标配色,避免同一个词两种颜色。

import * as React from "react";

import Link from "next/link";

import { AwardIcon } from "lucide-react";
import { toast } from "sonner";

import { TablePagination } from "@/components/table-pagination";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { SRCReward } from "@/lib/types";

import { fmtTime } from "./shared";

const PAGE_SIZE = 20;

const STATUS_META: Record<string, { label: string; badge: string }> = {
  pending: { label: "待提交", badge: "bg-muted text-muted-foreground" },
  submitted: { label: "已提交", badge: "bg-blue-500/15 text-blue-600 border-blue-500/30" },
  accepted: { label: "已收录", badge: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30" },
  rejected: { label: "已驳回", badge: "bg-red-500/15 text-red-600 border-red-500/30" },
  duplicate: { label: "重复", badge: "bg-amber-500/15 text-amber-600 border-amber-500/30" },
};

const SEVERITY_META: Record<string, { label: string; badge: string }> = {
  critical: { label: "严重", badge: "bg-red-500/15 text-red-600 border-red-500/30" },
  high: { label: "高危", badge: "bg-orange-500/15 text-orange-600 border-orange-500/30" },
  medium: { label: "中危", badge: "bg-amber-500/15 text-amber-600 border-amber-500/30" },
  low: { label: "低危", badge: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30" },
};

export function RewardsTab({ targetId }: { targetId: number }) {
  const [rewards, setRewards] = React.useState<SRCReward[]>([]);
  const [total, setTotal] = React.useState(0);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);
  const [loading, setLoading] = React.useState(true);

  const load = React.useCallback(
    (p: number, size: number) => {
      setLoading(true);
      api
        .srcRewards({ targetId, limit: size, offset: (p - 1) * size })
        .then((r) => {
          setRewards(r.rewards);
          setTotal(r.total);
        })
        .catch((e) => toast.error(`加载成果失败:${(e as Error).message}`))
        .finally(() => setLoading(false));
    },
    [targetId],
  );

  React.useEffect(() => {
    load(page, pageSize);
  }, [page, pageSize, load]);

  return (
    <Card>
      <CardContent className="px-0 pt-4">
        <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
          <span className="text-muted-foreground text-sm">
            本目标产出的 SRC 成果。提交状态在「成果产出」页维护。
          </span>
          <span className="text-muted-foreground ml-auto text-xs tabular-nums">共 {total} 条</span>
        </div>

        {loading && rewards.length === 0 ? (
          <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
        ) : rewards.length === 0 ? (
          <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
            <AwardIcon className="size-8 opacity-40" />
            <span>该目标还没有产出成果。建 SRC 任务并产出漏洞后,成果会自动记录在这里。</span>
          </div>
        ) : (
          <>
            <Table className="table-fixed">
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  {/* 标题列不设宽度:table-fixed 下自动吃掉余量。 */}
                  <TableHead>标题</TableHead>
                  <TableHead className="w-20">严重度</TableHead>
                  <TableHead className="w-20">状态</TableHead>
                  <TableHead className="w-40">时间</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rewards.map((r) => {
                  const st = STATUS_META[r.status] ?? { label: r.status || "—", badge: "bg-muted text-muted-foreground" };
                  const sev = SEVERITY_META[r.severity] ?? { label: r.severity || "—", badge: "bg-muted text-muted-foreground" };
                  return (
                    <TableRow key={r.id}>
                      <TableCell className="min-w-0">
                        {/* 成果详情页不在本任务范围内,标题给任务详情作为出口(有任务时)。 */}
                        {r.task_id ? (
                          <Link
                            href={`/function/tasks/detail?id=${r.task_id}`}
                            className="block w-full truncate text-left font-medium hover:underline"
                            title={r.title}
                          >
                            {r.title}
                          </Link>
                        ) : (
                          <span className="block w-full truncate font-medium" title={r.title}>
                            {r.title}
                          </span>
                        )}
                      </TableCell>
                      <TableCell>
                        <Badge variant="outline" className={sev.badge}>
                          {sev.label}
                        </Badge>
                      </TableCell>
                      <TableCell>
                        <Badge variant="outline" className={st.badge}>
                          {st.label}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs">{fmtTime(r.created_at)}</TableCell>
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
  );
}
