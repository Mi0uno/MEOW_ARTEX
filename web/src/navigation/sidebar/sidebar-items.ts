import {
  Activity,
  Bot,
  Brain,
  Bug,
  ClipboardList,
  Crosshair,
  DoorOpen,
  FolderOpen,
  FolderSync,
  LayoutDashboard,
  type LucideIcon,
  MessageSquare,
  Network,
  Plug,
  Radar,
  Radio,
  RadioTower,
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

export type NavBadge = "new" | "soon";

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

// 侧边栏十大分类：除"仪表盘"（单页直达）外全部为可展开/收回的折叠分组，
// 展开状态按分类 id 持久化在 localStorage（见 nav-main.tsx）。
// 页面归属约定：
//   任务中心   —— 任务全生命周期的操作面（任务/对话/发现/工作空间）
//   监管&规则  —— 过程监控与安全闸门（拦截规则/审批/流量/日志）
//   AI管理     —— Agent 的模型与能力配置（LLM/Agent/MCP/Skill/LLM 录制）
//   信息搜集   —— 资产测绘与同步
//   渗透测试   —— 执行与操作记录（工具执行；后续攻击模块也归这里）
//   后渗透/C2/VPS —— 规划中的模块，先占位（badge=soon，子项禁用）
//   系统设置   —— 平台自身的配置（工具/系统功能/系统设置）
export const sidebarItems: NavGroup[] = [
  {
    id: 1,
    items: [
      { id: "dashboard", title: "仪表盘", url: "/dashboard", icon: LayoutDashboard },
      {
        id: "task-center",
        title: "任务中心",
        icon: Target,
        subItems: [
          { id: "tasks", title: "任务", url: "/function/tasks", icon: Target },
          { id: "chat", title: "对话", url: "/chat", icon: MessageSquare },
          { id: "findings", title: "发现", url: "/function/findings", icon: Bug },
          { id: "workspace", title: "工作空间", url: "/function/workspace", icon: FolderOpen },
        ],
      },
      {
        id: "gov-rules",
        title: "监管&规则中心",
        icon: ShieldAlert,
        subItems: [
          { id: "intercept", title: "拦截规则", url: "/system/intercept", icon: ShieldAlert },
          { id: "approvals", title: "审批记录", url: "/system/intercept/approvals", icon: ClipboardList },
          { id: "traffic", title: "流量", url: "/function/traffic", icon: Activity },
          { id: "logs", title: "日志", url: "/system/logs", icon: ScrollText },
        ],
      },
      {
        id: "ai-mgmt",
        title: "AI管理",
        icon: Brain,
        subItems: [
          { id: "llm", title: "LLM", url: "/system/llm", icon: Brain },
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
          { id: "assets", title: "资产", url: "/function/assets", icon: Network },
          { id: "sync", title: "资产同步", url: "/function/sync", icon: FolderSync },
        ],
      },
      {
        id: "pentest",
        title: "渗透测试",
        icon: Crosshair,
        subItems: [{ id: "commands", title: "工具执行", url: "/function/commands", icon: Terminal }],
      },
      {
        id: "postex",
        title: "后渗透",
        icon: DoorOpen,
        badge: "soon",
        subItems: [{ id: "postex-todo", title: "建设中", url: "#", disabled: true }],
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
