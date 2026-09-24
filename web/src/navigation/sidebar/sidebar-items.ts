import {
  Activity,
  Award,
  Bot,
  Brain,
  Bug,
  Building2,
  ClipboardList,
  Crosshair,
  Database,
  DoorOpen,
  FileText,
  FolderOpen,
  FolderSync,
  Layers,
  LayoutDashboard,
  LayoutTemplateIcon,
  type LucideIcon,
  MessageSquare,
  Network,
  Plug,
  Radar,
  Radio,
  RadioTower,
  Scale,
  ScrollText,
  Server,
  Settings2,
  ShieldAlert,
  SlidersHorizontal,
  Sparkles,
  Target,
  Terminal,
  Wrench,
} from "lucide-react";

export type NavBadge = "new" | "soon" | "wip";

export interface NavSubItem {
  id: string;
  title: string;
  url: string;
  icon?: LucideIcon;
  badge?: NavBadge;
  disabled?: boolean;
  newTab?: boolean;
}

interface NavItemBase {
  id: string;
  title: string;
  icon?: LucideIcon;
  badge?: NavBadge;
  disabled?: boolean;
  newTab?: boolean;
}

export interface NavMainLinkItem extends NavItemBase {
  url: string;
  subItems?: never;
}

export interface NavMainParentItem extends NavItemBase {
  subItems: NavSubItem[];
}

export type NavMainItem = NavMainLinkItem | NavMainParentItem;

export interface NavGroup {
  id: number;
  label?: string;
  items: NavMainItem[];
}

// 侧边栏分类：除"仪表盘"（单页直达）外全部为可展开/收回的折叠分组，
// 展开状态按分类 id 持久化在 localStorage（见 nav-main.tsx）。
// 页面归属约定：
//   任务中心   —— 所有任务的总入口：总览(原仪表盘)+全阶段任务+对话/发现/工作空间
//   报告中心   —— 产出报告的汇集地(按分类分组检索)
//   监管&规则  —— 过程监控与安全闸门（拦截规则/AI监管/审批/流量/日志）
//   代理中心   —— 出网代理的统管：流量与调度(仪表盘+裸网/代理池切换)/代理池(代理组)
//   AI管理     —— Agent 的模型与能力配置（LLM/Decision/Agent/MCP/Skill/LLM 录制）
//   渗透测试/信息搜集/后渗透 —— 三阶段分组：各组带本阶段任务(?stage=锁定)与对话
//                 (?stage= 只看本阶段 agent 的会话)，外加本阶段的专属工具面
//   C2/VPS     —— 规划中的模块，先占位（badge=soon，子项禁用）
//   系统设置   —— 平台自身的配置（工具/系统功能/系统设置）
export const sidebarItems: NavGroup[] = [
  {
    id: 1,
    items: [
      {
        id: "task-center",
        title: "任务中心",
        icon: Target,
        subItems: [
          // 总览 = 原顶层「仪表盘」：全部任务的汇总视图归任务中心统一管理。
          { id: "overview", title: "总览", url: "/dashboard", icon: LayoutDashboard },
          // 任务 = 全阶段任务总表(不带 stage 锁定)；各阶段分组的任务用 ?stage= 锁定。
          { id: "tasks", title: "任务", url: "/function/tasks", icon: Target },
          { id: "chat", title: "对话", url: "/chat", icon: MessageSquare },
          { id: "findings", title: "发现", url: "/function/findings", icon: Bug },
          { id: "workspace", title: "工作空间", url: "/function/workspace", icon: FolderOpen },
        ],
      },
      {
        id: "reports-center",
        title: "报告中心",
        icon: FileText,
        subItems: [
          // 全部报告 = 无分类锁定;其余子项以 ?kind= 预选分类页签。
          { id: "reports-all", title: "全部报告", url: "/function/reports", icon: FileText },
          { id: "reports-task", title: "任务报告", url: "/function/reports?kind=task", icon: Target },
          { id: "reports-finding", title: "漏洞报告", url: "/function/reports?kind=finding", icon: Bug },
          { id: "reports-custom", title: "自定义报告", url: "/function/reports?kind=custom", icon: FileText },
          // 模板管理 = 独立页面:上传 docx/md 模板(上传即自动 AI 识别)、采用 AI 草稿。
          { id: "reports-templates", title: "模板管理", url: "/function/reports/templates", icon: LayoutTemplateIcon },
          // AI 识别任务/对话 = 监看 AI 占位模板生成过程(?stage=report 锁定报告中心阶段)。
          { id: "reports-tasks", title: "任务", url: "/function/tasks?stage=report", icon: Target },
          { id: "reports-chat", title: "对话", url: "/chat?stage=report", icon: MessageSquare },
        ],
      },
      {
        id: "gov-rules",
        title: "监管&规则中心",
        icon: ShieldAlert,
        subItems: [
          { id: "intercept", title: "拦截规则", url: "/system/intercept", icon: ShieldAlert },
          // AI监管 = 操作审查 AI 流水线(决策引擎 + 监管模型)编排,从拦截规则独立出来。
          { id: "ai-guard", title: "AI监管", url: "/system/ai-guard", icon: Bot },
          { id: "approvals", title: "审批记录", url: "/system/intercept/approvals", icon: ClipboardList },
          { id: "traffic", title: "流量", url: "/function/traffic", icon: Activity },
          { id: "logs", title: "日志", url: "/system/logs", icon: ScrollText },
        ],
      },
      {
        id: "proxy-center",
        title: "代理中心",
        icon: Network,
        subItems: [
          // 流量与调度 = 所有走代理池的流量的仪表盘:出网模式快速切换 + 动态调度到哪条代理。
          { id: "proxy-dashboard", title: "流量与调度", url: "/function/proxy", icon: Activity },
          // 代理池 = 代理组列表(网络流量核心);进入组后接入代理池 API、搜集在野代理、自动测活。
          { id: "proxy-groups", title: "代理池", url: "/function/proxy/groups", icon: Network },
        ],
      },
      {
        id: "ai-mgmt",
        title: "AI管理",
        icon: Brain,
        subItems: [
          { id: "llm", title: "LLM", url: "/system/llm", icon: Brain },
          // Decision = 决策模型(JEV/Laya 等前置决策引擎)的凭据与端点配置。
          { id: "decision", title: "Decision", url: "/system/decision", icon: Scale },
          { id: "agents", title: "Agent", url: "/system/agents", icon: Bot },
          { id: "mcp", title: "MCP", url: "/system/mcp", icon: Plug },
          { id: "skills", title: "Skill", url: "/system/skills", icon: Sparkles },
          { id: "llm-records", title: "LLM 录制", url: "/function/llm-records", icon: Radio },
        ],
      },
      {
        id: "recon",
        title: "信息搜集",
        icon: Radar,
        subItems: [
          // 信息搜集任务 = 任务中心同款表格,带 ?stage=recon 锁定阶段(本体复用)。
          { id: "recon-tasks", title: "任务", url: "/function/tasks?stage=recon", icon: Radar },
          { id: "recon-chat", title: "对话", url: "/chat?stage=recon", icon: MessageSquare },
          // 信息搜集工具 = 系统设置工具页本体,?stage=recon 锁定阶段(统一管理)。
          { id: "recon-tools", title: "工具", url: "/system/tools?stage=recon", icon: Wrench },
          { id: "recon-execute", title: "工具执行", url: "/function/recon/execute", icon: Terminal },
          { id: "recon-wordbooks", title: "字典库", url: "/function/recon/wordbooks", icon: FolderOpen },
          { id: "external-apis", title: "外部接口", url: "/function/external-apis", icon: Plug },
          { id: "assets", title: "资产", url: "/function/assets", icon: Network },
          { id: "sync", title: "资产同步", url: "/function/sync", icon: FolderSync },
          // 漏洞库(0day/Nday 人工沉淀)与 CVE 情报(定时搜集)分为两页:
          // 前者重编辑与利用指引,后者是机器持续灌入的大批量情报,重检索与同步状态。
          { id: "vulndb", title: "漏洞库", url: "/function/vulndb", icon: Database },
          { id: "vulndb-cve", title: "CVE 情报", url: "/function/vulndb/cve", icon: ShieldAlert },
        ],
      },
      {
        id: "pentest",
        title: "渗透测试",
        icon: Crosshair,
        subItems: [
          { id: "pentest-tasks", title: "任务", url: "/function/tasks?stage=exploit", icon: Crosshair },
          { id: "pentest-chat", title: "对话", url: "/chat?stage=exploit", icon: MessageSquare },
          { id: "commands", title: "工具执行", url: "/function/commands", icon: Terminal },
        ],
      },
      {
        id: "src",
        title: "SRC 模式",
        icon: Award,
        subItems: [
          // 平台选择:选平台 → 进入平台添加目标 → 进入目标添加资产。平台的奖励标准/
          // 评分标准/守则/规范(含上传的附件)会转成 AI 硬约束,防止越界。
          { id: "src-platforms", title: "平台选择", url: "/function/src/platforms", icon: Building2 },
          // 平台批量:按平台聚合目标,勾选多个目标批量建 SRC 任务。
          { id: "src-batch", title: "平台批量", url: "/function/src/batch", icon: Layers },
          // 成果产出:SRC 任务产出的可提交成果与提交状态跟踪。
          { id: "src-rewards", title: "成果产出", url: "/function/src/rewards", icon: Award },
        ],
      },
      {
        id: "postex",
        title: "后渗透",
        icon: DoorOpen,
        badge: "soon",
        subItems: [
          { id: "postex-tasks", title: "任务", url: "/function/tasks?stage=post", icon: DoorOpen },
          { id: "postex-chat", title: "对话", url: "/chat?stage=post", icon: MessageSquare },
        ],
      },
      {
        id: "c2",
        title: "C2",
        icon: RadioTower,
        badge: "soon",
        subItems: [{ id: "c2-todo", title: "建设中", url: "#", disabled: true }],
      },
      {
        id: "vps",
        title: "VPS",
        icon: Server,
        badge: "soon",
        subItems: [{ id: "vps-todo", title: "建设中", url: "#", disabled: true }],
      },
      {
        id: "system",
        title: "系统设置",
        icon: Settings2,
        subItems: [
          { id: "tools", title: "工具", url: "/system/tools", icon: Wrench },
          { id: "functions", title: "系统功能", url: "/system/functions", icon: SlidersHorizontal },
          { id: "settings", title: "系统设置", url: "/system/settings", icon: Settings2 },
        ],
      },
    ],
  },
];
