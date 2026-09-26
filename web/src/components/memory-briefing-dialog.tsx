"use client";

// AI 记忆简报共享组件(契约 AI_MEMORY_CONTRACTS.md §4/§9):
//  - MemoryBriefingDialog 完整简报弹层:按服务端 sections 的固定标题(一~七)逐段
//    渲染,等宽字体、可滚动——溯源阅读优先,防投毒需要能看到每条记忆的出处。
//  - TaskMemoryBriefingCard 任务详情「记忆简报」卡:总览页签里的一张紧凑卡,挂载即
//    拉取 api.memoryBriefing;加载失败静默降级(渲染 null,不崩任务详情页)。

import * as React from "react";

import { DatabaseIcon, FileTextIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { api } from "@/lib/api";
import type { MemoryBriefing } from "@/lib/types";

// 取正文的头 n 行(忽略空行);more=还有剩余,尾部加省略号提示看完整简报。
function headLines(body: string, n: number): { text: string; more: boolean } {
  const lines = body
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean);
  return { text: lines.slice(0, n).join("\n"), more: lines.length > n };
}

// 简报单段的预览:标题 + 前 2 行等宽正文(超出行数用 line-clamp 截断)。
function BriefingSectionPreview({ title, body }: { title: string; body: string }) {
  const { text, more } = headLines(body, 2);
  if (!text) return null;
  return (
    <div className="flex flex-col gap-1">
      <div className="text-muted-foreground text-xs font-medium">{title}</div>
      <p className="line-clamp-2 font-mono text-xs break-words whitespace-pre-wrap text-muted-foreground">
        {text}
        {more ? " …" : ""}
      </p>
    </div>
  );
}

// 完整简报弹层:sections 与 text 同内容(服务端确定性拼装),优先按段渲染。
export function MemoryBriefingDialog({
  briefing,
  open,
  onOpenChange,
}: {
  briefing: MemoryBriefing | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const sections = briefing?.sections ?? [];
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>记忆简报 · 任务 #{briefing?.task_id ?? ""}</DialogTitle>
          <DialogDescription>
            历史任务沉淀的资产、发现、经验与剧本;任务启动时注入 AI,避免重复探索烧 token。
            {briefing?.generated_at ? ` 生成于 ${new Date(briefing.generated_at).toLocaleString()}` : ""}
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-2.5">
          {sections.length > 0 ? (
            sections.map((s, i) => (
              <div key={`${i}-${s.title}`} className="flex flex-col gap-1 rounded-md border p-3">
                <div className="text-sm font-medium">{s.title}</div>
                <p className="font-mono text-xs break-words whitespace-pre-wrap text-muted-foreground">{s.body}</p>
              </div>
            ))
          ) : (
            <p className="font-mono text-xs break-words whitespace-pre-wrap text-muted-foreground">
              {briefing?.text || "暂无内容"}
            </p>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

// 任务详情总览页签的「记忆简报」卡:has_content → 一/二两段摘要 + 完整简报入口;
// 无内容 → 暂无历史记忆;接口失败 → 整卡不渲染(静默)。
export function TaskMemoryBriefingCard({ taskId }: { taskId: string }) {
  const [briefing, setBriefing] = React.useState<MemoryBriefing | null>(null);
  const [failed, setFailed] = React.useState(false);
  const [open, setOpen] = React.useState(false);

  React.useEffect(() => {
    let cancelled = false;
    api
      .memoryBriefing(taskId)
      .then((b) => {
        if (!cancelled) setBriefing(b);
      })
      .catch(() => {
        // 静默降级:简报接口不可用不能影响任务详情页其余卡片。
        if (!cancelled) setFailed(true);
      });
    return () => {
      cancelled = true;
    };
  }, [taskId]);

  if (failed || !briefing) return null;

  if (!briefing.has_content) {
    return (
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <DatabaseIcon className="text-primary size-4" /> 记忆简报
          </CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-muted-foreground text-sm">暂无历史记忆。任务终态复盘后,这里会沉淀该企业的资产要点与历史发现。</p>
        </CardContent>
      </Card>
    );
  }

  // 一(已知资产要点)/ 二(历史发现)两段摘要;sections 缺失段时回退全文头两行。
  const sections = briefing.sections ?? [];
  const assetsSec = sections.find((s) => s.title.includes("已知资产") || s.title.includes("一、"));
  const findingsSec = sections.find((s) => s.title.includes("历史发现") || s.title.includes("二、"));
  const preview = [assetsSec, findingsSec].filter((s): s is { title: string; body: string } => !!s && !!s.body.trim());

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between gap-2 space-y-0">
        <CardTitle className="flex items-center gap-2 text-base">
          <DatabaseIcon className="text-primary size-4" /> 记忆简报
          <span className="text-muted-foreground text-xs font-normal">(来自历史任务沉淀,任务启动时已注入 AI)</span>
        </CardTitle>
        <Button variant="outline" size="sm" onClick={() => setOpen(true)}>
          <FileTextIcon className="size-3.5" />
          查看完整简报
        </Button>
      </CardHeader>
      <CardContent className="flex flex-col gap-2.5">
        {preview.length > 0 ? (
          preview.map((s) => <BriefingSectionPreview key={s.title} title={s.title} body={s.body} />)
        ) : (
          <BriefingSectionPreview title="简报摘要" body={briefing.text} />
        )}
      </CardContent>
      <MemoryBriefingDialog briefing={briefing} open={open} onOpenChange={setOpen} />
    </Card>
  );
}
