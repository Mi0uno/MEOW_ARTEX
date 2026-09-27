import * as React from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { toast } from "sonner";
import { api } from "@/lib/api";
import type { PocReviewStats } from "@/lib/types";
import { cn } from "@/lib/utils";

// POC_INTEL_CONTRACTS:PoC 审核软质检层的展示组件(Agent D,集成期主会话挂载)。
// 样式口径与 vulndb-dialogs.tsx 的 Badge 用法对齐(outline + 语义色)。

// POC_REVIEW_BADGE PoC 审核三态的徽标文案/样式/缺省提示(approved 绿「已核」、
// pending 灰「待审」、rejected 红「存疑」;未知状态回退 pending,不报错)。
export const POC_REVIEW_BADGE: Record<string, { label: string; badge: string; title: string }> = {
  approved: {
    label: "已核",
    badge: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30",
    title: "审核通过:LLM/人工判定可用",
  },
  pending: {
    label: "待审",
    badge: "bg-muted text-muted-foreground border-border",
    title: "尚未审核,或置信度不足保持待审",
  },
  rejected: {
    label: "存疑",
    badge: "bg-red-500/15 text-red-600 border-red-500/30",
    title: "审核存疑:仅标记不删除,使用前人工核实",
  },
};

// pocReviewBadgeMeta 取三态元信息,未知状态安全回退为待审。
export function pocReviewBadgeMeta(status: string) {
  return POC_REVIEW_BADGE[status] ?? POC_REVIEW_BADGE.pending;
}

// PocReviewBadge PoC 审核状态徽标;note(LLM/人工审核理由)作为悬停提示。
export function PocReviewBadge({ status, note }: { status: string; note?: string }) {
  const meta = pocReviewBadgeMeta(status);
  const tip = note ? `${meta.title} — ${note}` : meta.title;
  return (
    <Badge variant="outline" className={cn(meta.badge, "cursor-default")} title={tip}>
      {meta.label}
    </Badge>
  );
}

// PocReviewPanel 审核工作台小组件:三态计数 + 「审核待审」批量按钮 + 说明文案。
// 审核在后台异步生效,30s 轻轮询让计数自己追上;送审成功/失败用 toast 反馈。
export function PocReviewPanel({
  className,
  onQueued,
}: {
  className?: string;
  onQueued?: () => void;
}) {
  const [stats, setStats] = React.useState<PocReviewStats | null>(null);
  const [busy, setBusy] = React.useState(false);

  const refresh = React.useCallback(() => {
    api
      .pocReviewStats()
      .then(setStats)
      .catch(() => setStats(null));
  }, []);

  React.useEffect(() => {
    refresh();
    const timer = window.setInterval(refresh, 30_000);
    return () => window.clearInterval(timer);
  }, [refresh]);

  const reviewPending = async () => {
    setBusy(true);
    try {
      const r = await api.reviewPocsBatch();
      toast.success(`已送审 ${r.queued} 条待审 PoC,审核在后台异步进行`);
      onQueued?.();
    } catch (e) {
      toast.error(`送审失败:${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className={cn("flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border p-3", className)}>
      <span className="text-sm font-medium">PoC 审核</span>
      {stats ? (
        <div className="flex items-center gap-1.5">
          <Badge variant="outline" className={POC_REVIEW_BADGE.pending.badge}>
            待审 {stats.pending}
          </Badge>
          <Badge variant="outline" className={POC_REVIEW_BADGE.approved.badge}>
            已核 {stats.approved}
          </Badge>
          <Badge variant="outline" className={POC_REVIEW_BADGE.rejected.badge}>
            存疑 {stats.rejected}
          </Badge>
        </div>
      ) : (
        <span className="text-xs text-muted-foreground">审核统计加载失败</span>
      )}
      <Button
        size="sm"
        variant="outline"
        disabled={busy || !stats || stats.pending === 0}
        onClick={reviewPending}
      >
        {busy ? "送审中…" : "审核待审"}
      </Button>
      <p className="text-xs text-muted-foreground">
        自动采集的 PoC 入库即待审;LLM 只做文本软质检打标(绝不访问 PoC 内链接),存疑仅标记不删除,可人工改判。
      </p>
    </div>
  );
}
