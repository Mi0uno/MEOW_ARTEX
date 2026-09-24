"use client";

// Agent 列表页与详情页共用的展示元数据:阶段文案、阶段配色、来源徽标。
//
// 放 _components 而不是各页写一份:两页头部/表格都要显示同一批徽标,分散实现迟早文案漂移
// (旧卡片网格里的阶段徽标是硬编码蓝色,详情页再写一次就会不一致)。

import { Badge } from "@/components/ui/badge";

// COMMON_STAGE 是「通用(跨阶段)」在筛选控件里的哨兵值:stage 字段本身用空串表达,
// 但空串是 Radix Select / Tabs 的保留值(表示未选中),故用哨兵代替。
export const COMMON_STAGE = "__common__";

// STAGE_TABS 是分类器的维度与顺序,列表页据此渲染页签并取计数。
export const STAGE_TABS: { value: string; label: string }[] = [
  { value: "all", label: "全部" },
  { value: COMMON_STAGE, label: "通用" },
  { value: "recon", label: "信息搜集" },
  { value: "exploit", label: "渗透测试" },
  { value: "post", label: "后渗透" },
  { value: "report", label: "报告中心" },
];

// stageLabel 阶段文案。空串=通用(跨阶段,会话只在任务中心可见)——必须显式判空,
// 否则会掉进「后渗透」兜底分支。
export function stageLabel(stage: string): string {
  if (!stage) return "通用";
  if (stage === "recon") return "信息搜集";
  if (stage === "exploit") return "渗透测试";
  if (stage === "report") return "报告中心";
  return "后渗透";
}

// stageBadgeClass 阶段配色:各阶段给不同色相,便于在长表格里按颜色扫读。
export function stageBadgeClass(stage: string): string {
  switch (stage) {
    case "recon":
      return "bg-blue-500/15 text-blue-600 border-blue-500/30";
    case "exploit":
      return "bg-orange-500/15 text-orange-600 border-orange-500/30";
    case "post":
      return "bg-violet-500/15 text-violet-600 border-violet-500/30";
    case "report":
      return "bg-emerald-500/15 text-emerald-600 border-emerald-500/30";
    default:
      return "bg-muted text-muted-foreground";
  }
}

export function StageBadge({ stage }: { stage?: string }) {
  return (
    <Badge variant="outline" className={stageBadgeClass(stage ?? "")}>
      {stageLabel(stage ?? "")}
    </Badge>
  );
}

// OriginBadge 来源徽标:内置(随版本发布,不可删) / 自定义(用户创建)。
export function OriginBadge({ builtin }: { builtin: boolean }) {
  return builtin ? <Badge variant="secondary">内置</Badge> : <Badge variant="outline">自定义</Badge>;
}

// EnabledBadge 启用状态徽标。注意:后端没有开关 agent 的接口(见 pgSaveAgentConfig 字段表),
// 所以这里只读展示,不做开关。
export function EnabledBadge({ enabled }: { enabled: boolean }) {
  return (
    <Badge
      variant="outline"
      className={
        enabled ? "bg-emerald-500/15 text-emerald-600 border-emerald-500/30" : "bg-muted text-muted-foreground"
      }
    >
      {enabled ? "启用" : "停用"}
    </Badge>
  );
}
