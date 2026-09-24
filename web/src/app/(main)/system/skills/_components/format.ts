// Skill 模块共用的格式化与派生计算。
//
// 列表页、详情页、概览三处都要显示「最近调用」与「文件数」，放这里避免各写一份
// 导致同一个时间戳/同一个计数在不同位置显示不一致。

import type { SkillItem } from "@/lib/types";

// fmtTime 是列表/概览用的紧凑时间（无年份）：这些位置列宽紧张，年份几乎总是当年。
export function fmtTime(ts?: string | null): string {
  if (!ts) return "从未调用";
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

// fmtFullTime 是详情页用的完整时间（含年份），那里宽度不是约束。
export function fmtFullTime(ts?: string | null): string {
  if (!ts) return "—";
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString("zh-CN");
}

// fileCount 只数文件、不数目录：后端清单里目录以 "/" 结尾（scripts/），
// 把它算进「文件数」会让数字虚高，与实际装了什么东西对不上。
export function fileCount(s: SkillItem): number {
  return s.files.filter((f) => !f.endsWith("/")).length;
}
