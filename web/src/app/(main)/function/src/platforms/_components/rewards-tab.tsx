"use client";

// 成果页签:该平台下的 SRC 成果。
//
// 只读列表(提交状态在「成果产出」页改):详情页这一屏的目的是回答
// 「这个平台到底产出了多少东西、有没有被收录」,编辑状态是另一个工作流,
// 混进来会让平台配置页变成第二套成果管理界面。

import * as React from "react";

import Link from "next/link";

import { AwardIcon, SearchIcon } from "lucide-react";
import { toast } from "sonner";

import { TablePagination } from "@/components/table-pagination";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { SRCReward } from "@/lib/types";

import { fmtTime } from "./platform-shared";

const PAGE_SIZE = 50;

// 与「成果产出」页保持同一套状态/严重度配色,同一份数据在两地颜色一致。
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

export function RewardsTab({ platformId }: { platformId: number }) {
  const [rewards, setRewards] = React.useState<SRCReward[]>([]);
  const [total, setTotal] = React.useState(0);
  const [query, setQuery] = React.useState("");
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);
  const [loading, setLoading] = React.useState(true);

  const load = React.useCallback(
    (q: string, p: number, size: number) => {
      setLoading(true);
      api
        .srcRewards({ platformId, q, limit: size, offset: (p - 1) * size })
        .then((r) => {
          setRewards(r.rewards ?? []);
          setTotal(r.total);
        })
        .catch((e) => toast.error(`加载成果失败:${(e as Error).message}`))
        .finally(() => setLoading(false));
    },
    [platformId],
  );

  React.useEffect(() => {
    load(query, page, pageSize);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [platformId, page, pageSize]);

  const search = () => {
    setPage(1);
    load(query, 1, pageSize);
  };

  return (
    <Card>
      <CardContent className="px-0 pt-4">
        <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
          <div className="relative w-full sm:max-w-xs">
            <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && search()}
              placeholder="搜索成果标题 / 漏洞类别"
              className="pl-8"
            />
          </div>
          <Button variant="outline" size="sm" onClick={search}>
            搜索
          </Button>
          <span className="text-muted-foreground ml-auto text-xs tabular-nums">
            {query ? `命中 ${total} / ` : ""}共 {total} 条成果
          </span>
        </div>

        {loading && rewards.length === 0 ? (
          <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
        ) : rewards.length === 0 ? (
          <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
            <AwardIcon className="size-8 opacity-40" />
            <span>
              {query
                ? "没有匹配的成果。"
                : "本平台下还没有成果。用本平台的目标建 SRC 任务，任务打出漏洞时会自动生成待提交成果。"}
            </span>
          </div>
        ) : (
          <>
            <Table className="table-fixed">
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  {/* 标题列不设宽度(自动吃余量);固定列之和 560px(96+96+128+96+144),远小于 ~897px 容器。 */}
                  <TableHead>成果标题</TableHead>
                  <TableHead className="w-24">严重度</TableHead>
                  <TableHead className="w-24">状态</TableHead>
                  <TableHead className="w-32">漏洞类别</TableHead>
                  <TableHead className="w-24">来源任务</TableHead>
                  <TableHead className="w-36">产出时间</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rewards.map((r) => {
                  const meta = STATUS_META[r.status] ?? { label: r.status, badge: "bg-muted text-muted-foreground" };
                  return (
                    <TableRow key={r.id}>
                      <TableCell className="min-w-0">
                        <span className="block w-full truncate font-medium" title={r.title}>
                          {r.title}
                        </span>
                      </TableCell>
                      <TableCell>
                        {r.severity ? (
                          <Badge variant="outline" className={SEVERITY_META[r.severity] ?? ""}>
                            {r.severity}
                          </Badge>
                        ) : (
                          <span className="text-muted-foreground">—</span>
                        )}
                      </TableCell>
                      <TableCell>
                        <Badge variant="outline" className={meta.badge}>
                          {meta.label}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-muted-foreground truncate text-xs" title={r.vuln_class}>
                        {r.vuln_class || "—"}
                      </TableCell>
                      <TableCell className="text-xs">
                        {r.task_id ? (
                          <Link
                            href={`/function/tasks/detail?id=${r.task_id}`}
                            className="text-blue-600 hover:underline"
                          >
                            #{r.task_id}
                          </Link>
                        ) : (
                          <span className="text-muted-foreground">—</span>
                        )}
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
