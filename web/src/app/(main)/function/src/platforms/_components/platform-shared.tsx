"use client";

// SRC 平台选择共用的展示元数据与表单零件。
//
// 列表页与详情页要显示同一批文案与徽标(启用状态、附件解析状态、四类规范文本域),
// 各写一份迟早漂移——「已解析并注入约束」这种文案一旦两边不一致,用户就会怀疑
// 规范到底有没有生效,所以集中在这里定义。

import * as React from "react";

import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import type { SRCPlatformFileKind } from "@/lib/types";

// fmtTime 统一的本地时间展示;空值/非法值一律给破折号,避免表格里出现 Invalid Date。
export function fmtTime(iso?: string | null): string {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

// fmtBytes 附件大小的人类可读展示(KB 起,再小的文件按字节)。
export function fmtBytes(n?: number): string {
  const v = n ?? 0;
  if (v <= 0) return "—";
  if (v < 1024) return `${v} B`;
  if (v < 1024 * 1024) return `${(v / 1024).toFixed(1)} KB`;
  return `${(v / (1024 * 1024)).toFixed(1)} MB`;
}

// Field 是表单字段的统一包装(照 system/intercept 的惯例):小号大写标签 + 控件。
export function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1.5">
      <Label className="font-medium text-muted-foreground text-xs uppercase tracking-wide">{label}</Label>
      {children}
    </div>
  );
}

// SRCPlatformTextField 就是四类规范文本域的列名。用 Exclude 收窄而不是直接复用
// SRCPlatformFileKind:kind 联合加入 'other'(统一上传的暂存类别)后,'other' 不是
// 平台表的列,再用整个 kind 联合索引 Partial<SRCPlatform> 会报 TS7053——
// 而四个文本域恰好对应除 other 外的四个 kind,语义上也正该排除。
export type SRCPlatformTextField = Exclude<SRCPlatformFileKind, "other">;

// SPEC_FIELDS 定义四个规范文本域的顺序、标签与示例。
// placeholder 必须给真实示例而不是「请输入…」:这四段文本会被渲染成 AI 的硬约束,
// 用户看到真实范例才知道该写成什么粒度。
export const SPEC_FIELDS: {
  key: SRCPlatformTextField;
  label: string;
  placeholder: string;
  hint: string;
}[] = [
  {
    key: "reward_standard",
    label: "奖励标准",
    placeholder: "严重 3000-10000 元；高危 1000-3000 元；中危 300-1000 元；低危 50-300 元。",
    hint: "按等级列出奖金/积分区间，AI 据此判断哪些漏洞值得深入",
  },
  {
    key: "scoring_standard",
    label: "评分标准",
    placeholder: "按漏洞等级 + 影响范围评分；同一资产重复提交按首个有效；影响核心系统加倍计分。",
    hint: "决定优先级排序：分数高的资产先测",
  },
  {
    key: "code_of_conduct",
    label: "守则",
    placeholder: "禁止社工、禁止 DoS、禁止触碰非授权域名；测试需保持最小影响面。",
    hint: "会转成 deny 约束：写进来的行为 AI 一律不做",
  },
  {
    key: "rules",
    label: "规范",
    placeholder: "提交需附可复现步骤与完整请求响应；生产数据不得下载留存。",
    hint: "报告与留证要求，注入 Planner / Worker 系统提示",
  },
];

// SpecTextarea 是单个规范文本域(标签 + 多行输入 + 用途说明)。
// 列表页 Sheet 与详情页「规范与文件」页签共用,保证两处的顺序与示例一致。
export function SpecTextarea({
  field,
  value,
  onChange,
  rows = 4,
}: {
  field: (typeof SPEC_FIELDS)[number];
  value: string;
  onChange: (v: string) => void;
  rows?: number;
}) {
  return (
    <Field label={field.label}>
      <Textarea value={value} onChange={(e) => onChange(e.target.value)} placeholder={field.placeholder} rows={rows} />
      <p className="text-muted-foreground text-xs">{field.hint}</p>
    </Field>
  );
}

// PARSE_STATUS_META 是附件解析状态的展示三元组。
// skipped 的文案必须明说「不注入约束」:用户传了 pdf 后如果界面只写「已上传」,
// 他会以为规范已经生效——这是这类功能最危险的误解(后端也为此写了固定说明)。
const PARSE_STATUS_META: Record<string, { label: string; badge: string }> = {
  parsed: { label: "已解析并注入约束", badge: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30" },
  skipped: { label: "仅存档，不注入约束", badge: "bg-muted text-muted-foreground" },
  failed: { label: "解析失败", badge: "bg-red-500/15 text-red-600 border-red-500/30" },
};

export function parseStatusMeta(status: string): { label: string; badge: string } {
  return PARSE_STATUS_META[status] ?? { label: status || "未知", badge: "bg-muted text-muted-foreground" };
}

export function ParseStatusBadge({ status }: { status: string }) {
  const meta = parseStatusMeta(status);
  return (
    <Badge variant="outline" className={meta.badge}>
      {meta.label}
    </Badge>
  );
}

// EnabledBadge 平台启用状态。停用的平台不参与任务建单,列表里要一眼能扫出来。
export function EnabledBadge({ enabled }: { enabled: boolean }) {
  return (
    <Badge
      variant="outline"
      className={enabled ? "bg-emerald-500/15 text-emerald-600 border-emerald-500/30" : "bg-muted text-muted-foreground"}
    >
      {enabled ? "启用" : "停用"}
    </Badge>
  );
}
