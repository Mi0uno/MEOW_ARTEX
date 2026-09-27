"use client";

// PoC 历史回填卡(nuclei / trickest 共用,契约 POC_INTEL_CONTRACTS.md):
// 同一 PocBackfillStatus wire,按 kind 切换对应 API。渲染状态行(进行中/已完成
// 徽标、processed/matched 计数、错误)与启动/取消按钮;running 时 5s 轮询。
// 集成期由主会话挂到 cve 页情报源页签;trickest 后端(Agent B)按同形 wire 实现。

import * as React from "react";
import { Loader2Icon, PlayIcon, SquareIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { PocBackfillStatus } from "@/lib/types";

export type VulndbBackfillKind = "nuclei" | "trickest";

const backfillMeta: Record<
  VulndbBackfillKind,
  {
    title: string;
    description: string;
    matchedLabel: string;
    status: () => Promise<PocBackfillStatus>;
    start: () => Promise<{ status: string }>;
    cancel: () => Promise<{ status: string }>;
  }
> = {
  nuclei: {
    title: "nuclei 模板正文回填",
    description: "为已入库的 nuclei 模板 PoC 补齐官方 YAML 正文(raw 源,不占 GitHub API 配额;断点续跑)。",
    matchedLabel: "正文写入",
    status: () => api.nucleiBackfillStatus(),
    start: () => api.startNucleiBackfill(),
    cancel: () => api.cancelNucleiBackfill(),
  },
  trickest: {
    title: "trickest PoC 猎取回填",
    description: "按库内 CVE 条目逐条拉取 trickest 聚合页,补挂 GitHub PoC 与引用(断点续跑)。",
    matchedLabel: "挂载 PoC",
    status: () => api.trickestBackfillStatus(),
    start: () => api.startTrickestBackfill(),
    cancel: () => api.cancelTrickestBackfill(),
  },
};

// fmtTime RFC3339 → 本地短时间;空串原样返回(未开始/未结束)。
function fmtTime(v: string): string {
  if (!v) return "";
  const t = new Date(v);
  return Number.isNaN(t.getTime()) ? v : t.toLocaleString();
}

export function VulndbBackfillCard({ kind }: { kind: VulndbBackfillKind }) {
  const meta = backfillMeta[kind];
  const [status, setStatus] = React.useState<PocBackfillStatus | null>(null);
  const [busy, setBusy] = React.useState(false);

  const refresh = React.useCallback(() => {
    meta
      .status()
      .then(setStatus)
      .catch(() => {
        // 状态拉取失败不打断页面(后端未起/重启窗口),下轮轮询自愈。
      });
  }, [meta]);

  React.useEffect(() => {
    refresh();
  }, [refresh]);

  // running 时 5s 轮询(依赖布尔而非整个 status 对象,避免每轮重建定时器)。
  React.useEffect(() => {
    if (!status?.running) return;
    const t = setInterval(refresh, 5000);
    return () => clearInterval(t);
  }, [status?.running, refresh]);

  const start = async () => {
    setBusy(true);
    try {
      await meta.start();
      toast.success("回填已启动");
      refresh();
    } catch (e) {
      const msg = (e as Error).message ?? "";
      if (msg.includes("进行中") || msg.includes("409")) {
        toast.error("回填进行中");
      } else {
        toast.error(`启动失败:${msg}`);
      }
    } finally {
      setBusy(false);
    }
  };

  const cancel = async () => {
    setBusy(true);
    try {
      await meta.cancel();
      toast.success("正在取消,光标已保留可续跑");
      refresh();
    } catch (e) {
      toast.error(`取消失败:${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  const running = status?.running ?? false;
  const done = status?.done ?? false;

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          {meta.title}
          {running ? (
            <Badge className="gap-1">
              <Loader2Icon className="h-3 w-3 animate-spin" />
              进行中
            </Badge>
          ) : done ? (
            <Badge variant="secondary">已完成</Badge>
          ) : (
            <Badge variant="outline">空闲</Badge>
          )}
        </CardTitle>
        <CardDescription>{meta.description}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-muted-foreground">
          <span>
            已处理 <span className="font-medium text-foreground">{status?.processed ?? 0}</span>
          </span>
          <span>
            {meta.matchedLabel}{" "}
            <span className="font-medium text-foreground">{status?.matched ?? 0}</span>
          </span>
          {status?.started_at ? <span>开始于 {fmtTime(status.started_at)}</span> : null}
          {status?.finished_at ? <span>结束于 {fmtTime(status.finished_at)}</span> : null}
        </div>
        {status?.error ? <p className="text-sm text-destructive">{status.error}</p> : null}
        <div className="flex gap-2">
          <Button size="sm" onClick={start} disabled={busy || running}>
            <PlayIcon className="h-4 w-4" />
            启动回填
          </Button>
          <Button size="sm" variant="outline" onClick={cancel} disabled={busy || !running}>
            <SquareIcon className="h-4 w-4" />
            取消
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}
