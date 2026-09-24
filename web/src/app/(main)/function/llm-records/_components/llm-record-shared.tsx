"use client";

// LLM 录制列表页与详情页共用的展示工具。
//
// 抽出来的唯一理由:同一条记录在两个页面必须给出完全一致的读数(时间格式、
// 慢调用阈值、token 缩写、成功/失败判定)。任何一处单独改都会让两边对不上。

import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

// fmtTime 只到秒、不带年份:一屏 50 行时列宽很紧张,年份对排查没有增量信息。
export function fmtTime(ts: string): string {
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

export function fmtLatency(ms: number): string {
  return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`;
}

// 30s 以上标黄:超时/网关挂起是 provider 侧问题最常见的表象,值得一眼可见。
export function isSlow(ms: number): boolean {
  return ms > 30000;
}

export function fmtTokens(n: number): string {
  return n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n);
}

// 后端只在成功时写 "ok",其余一律是失败(见 llmrec.Recorder 里的 status 赋值)。
export function isOk(status: string): boolean {
  return status === "ok";
}

export function StatusBadge({ status, className }: { status: string; className?: string }) {
  return isOk(status) ? (
    <Badge variant="secondary" className={cn("text-xs text-emerald-600", className)}>
      成功
    </Badge>
  ) : (
    <Badge variant="destructive" className={cn("text-xs", className)}>
      失败
    </Badge>
  );
}
