"use client";

// FormattedPayload:把探索节点 payload / 漏洞证据这类单行 JSON 自动整理成人能读的
// 结构化视图(中文字段名 + 长文本整段展开 + 数组徽标);非 JSON 原样展示。
// 右上角可切回「原始 JSON」并一键复制。

import * as React from "react";

import { BracesIcon, CopyIcon, ListTreeIcon } from "lucide-react";
import { toast } from "sonner";

import { cn } from "@/lib/utils";

// 探索图 / 发现记录里高频字段的中文标注;未命中字段原样展示 key。
const FIELD_LABELS: Record<string, string> = {
  summary: "摘要",
  description: "描述",
  text: "内容",
  name: "名称",
  title: "标题",
  goal: "目标",
  state: "状态",
  status: "状态",
  next_run: "下次运行",
  next_run_at: "下次运行",
  last_run: "上次运行",
  last_result: "上次结果",
  worker: "执行者",
  origin: "来源",
  source: "来源",
  asset_ids: "关联资产",
  assets: "资产",
  findings: "漏洞",
  vulnclass: "漏洞类型",
  severity: "严重等级",
  evidence: "证据",
  evidence_blob: "证据引用",
  poc: "PoC",
  by: "执行者",
  verification: "验证",
  baseline: "基线",
  supporting: "支撑证据",
  target: "目标",
  command: "命令",
  result: "结果",
  body: "正文",
  hits: "命中",
  probes: "探测",
  rounds: "轮次",
  verdict: "结论",
  recommendation: "修复建议",
  remediation: "修复建议",
  refs: "引用",
  params: "参数",
  url: "URL",
  host: "主机",
  port: "端口",
  service: "服务",
  created_at: "创建时间",
  date: "日期",
  reason: "原因",
  note: "备注",
  task: "任务",
  intent: "意图",
  scope: "范围",
  proxy: "代理",
};

function labelOf(key: string): string {
  return FIELD_LABELS[key] ?? key;
}

function ValueView({ value }: { value: unknown }) {
  if (value == null || value === "") {
    return <span className="text-muted-foreground">—</span>;
  }
  if (typeof value === "boolean") {
    return <span className="font-mono text-amber-600">{value ? "是" : "否"}</span>;
  }
  if (typeof value === "number") {
    return <span className="font-mono">{String(value)}</span>;
  }
  if (typeof value === "string") {
    // 长文本(摘要/证据正文)整段展开,保留换行。
    return <span className="break-words whitespace-pre-wrap">{value}</span>;
  }
  if (Array.isArray(value)) {
    const allScalar = value.every((v) => v == null || typeof v !== "object");
    if (allScalar) {
      if (value.length === 0) return <span className="text-muted-foreground">—</span>;
      return (
        <span className="flex flex-wrap gap-1">
          {value.map((v, i) => (
            <code key={i} className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] break-all">
              {String(v)}
            </code>
          ))}
        </span>
      );
    }
    return (
      <div className="flex flex-col gap-1.5">
        {value.map((v, i) => (
          <div key={i} className="rounded border bg-background/60 p-2">
            <ValueView value={v} />
          </div>
        ))}
      </div>
    );
  }
  const entries = Object.entries(value as Record<string, unknown>).filter(
    ([, v]) => v != null && v !== "" && !(Array.isArray(v) && v.length === 0),
  );
  if (entries.length === 0) return <span className="text-muted-foreground">—</span>;
  return <EntryList obj={Object.fromEntries(entries)} nested />;
}

function EntryList({ obj, nested }: { obj: Record<string, unknown>; nested?: boolean }) {
  return (
    <dl className={cn("flex flex-col", nested ? "gap-1.5" : "gap-2.5")}>
      {Object.entries(obj).map(([k, v]) => (
        <div key={k} className={cn(nested ? "flex flex-col gap-0.5" : "grid gap-1 sm:grid-cols-[9rem_1fr] sm:gap-3")}>
          <dt className={cn("text-muted-foreground shrink-0", nested ? "text-[11px]" : "text-xs")}>{labelOf(k)}</dt>
          <dd className="min-w-0 text-xs leading-relaxed">
            <ValueView value={v} />
          </dd>
        </div>
      ))}
    </dl>
  );
}

export function FormattedPayload({
  raw,
  className,
  maxHeight = "max-h-80",
}: {
  raw?: string | null;
  className?: string;
  maxHeight?: string;
}) {
  const [showRaw, setShowRaw] = React.useState(false);
  const text = raw ?? "";

  const parsed = React.useMemo<Record<string, unknown> | undefined>(() => {
    if (!text.trim()) return undefined;
    try {
      const v: unknown = JSON.parse(text);
      if (v && typeof v === "object" && !Array.isArray(v)) {
        return v as Record<string, unknown>;
      }
      return undefined;
    } catch {
      return undefined;
    }
  }, [text]);

  const pretty = React.useMemo(() => (parsed ? JSON.stringify(parsed, null, 2) : text), [parsed, text]);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(pretty);
      toast.success("已复制");
    } catch {
      toast.error("复制失败");
    }
  };

  return (
    <div className={cn("overflow-hidden rounded-md border bg-background", className)}>
      <div className="flex items-center justify-between gap-2 border-b px-2.5 py-1">
        <button
          type="button"
          onClick={() => setShowRaw((s) => !s)}
          className="text-muted-foreground hover:text-foreground flex items-center gap-1 text-[11px]"
        >
          {showRaw ? <BracesIcon className="size-3" /> : <ListTreeIcon className="size-3" />}
          {showRaw ? "整理视图" : "原始 JSON"}
        </button>
        <button
          type="button"
          onClick={() => void copy()}
          className="text-muted-foreground hover:text-foreground flex items-center gap-1 text-[11px]"
        >
          <CopyIcon className="size-3" />
          复制
        </button>
      </div>
      <div className={cn("overflow-auto p-3", maxHeight)}>
        {showRaw || !parsed ? (
          <pre className="font-mono text-xs break-words whitespace-pre-wrap">{text || "（空）"}</pre>
        ) : (
          <EntryList obj={parsed} />
        )}
      </div>
    </div>
  );
}
