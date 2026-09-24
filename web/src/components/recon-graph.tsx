"use client";

// 信息搜集阶段专属链路图:节点类型与渗透阶段(起点/目标/意图/事实/漏洞)完全分型。
//   起点(fact/origin,复用) → 步骤(recon_step) → 发现(recon_finding) → 汇总(recon_summary)
// 数据源与探索图相同(GET /api/exploration/graph),渲染层按 recon 类型注册表出牌。

import * as React from "react";

import {
  Background,
  BackgroundVariant,
  Controls,
  Handle,
  MiniMap,
  type Node as RFNode,
  type NodeProps,
  Panel,
  Position,
  ReactFlow,
  ReactFlowProvider,
  useEdgesState,
  useNodesState,
  type Edge as RFEdge,
} from "@xyflow/react";
import {
  Flag,
  Layers,
  Radar,
  ScanSearch,
  Server,
  Wrench,
  type LucideIcon,
} from "lucide-react";

import { cn } from "@/lib/utils";
import type { Edge, TaskNode } from "@/lib/types";

type ReconKind =
  | "begin"
  | "recon_step"
  | "recon_finding"
  | "recon_summary"
  // 资产拓扑(企业关系图复用同一渲染程序):根域→子域→IP→服务。
  | "asset_root"
  | "asset_domain"
  | "asset_ip"
  | "asset_service";

type TypeMeta = {
  label: string;
  icon: LucideIcon;
  iconBg: string;
  chip: string;
  hex: string;
};

// recon 阶段节点注册表:与渗透图的 起点/目标/意图/事实/漏洞 一一对应位置,语义全不同。
const typeMeta: Record<ReconKind, TypeMeta> = {
  begin: { label: "目标", icon: Flag, iconBg: "bg-slate-500", chip: "bg-slate-500/15 text-slate-600 dark:text-slate-300", hex: "#64748b" },
  recon_step: { label: "步骤", icon: Wrench, iconBg: "bg-blue-500", chip: "bg-blue-500/15 text-blue-600 dark:text-blue-400", hex: "#3b82f6" },
  recon_finding: { label: "发现", icon: ScanSearch, iconBg: "bg-amber-500", chip: "bg-amber-500/15 text-amber-600 dark:text-amber-400", hex: "#f59e0b" },
  recon_summary: { label: "汇总", icon: Layers, iconBg: "bg-teal-500", chip: "bg-teal-500/15 text-teal-600 dark:text-teal-400", hex: "#14b8a6" },
  asset_root: { label: "根域名", icon: Radar, iconBg: "bg-emerald-500", chip: "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400", hex: "#10b981" },
  asset_domain: { label: "子域名", icon: Radar, iconBg: "bg-blue-500", chip: "bg-blue-500/15 text-blue-600 dark:text-blue-400", hex: "#3b82f6" },
  asset_ip: { label: "IP", icon: Server, iconBg: "bg-amber-500", chip: "bg-amber-500/15 text-amber-600 dark:text-amber-400", hex: "#f59e0b" },
  asset_service: { label: "Web 服务", icon: Layers, iconBg: "bg-rose-500", chip: "bg-rose-500/15 text-rose-600 dark:text-rose-400", hex: "#f43f5e" },
};

// recon 阶段边语义:派生(目标→步骤)/产出(步骤→发现)/汇聚(汇总←步骤);
// 资产拓扑边语义:归属(子域→根域)/解析(IP→域名)/部署于(服务→命中节点)。
const relMeta: Record<string, { label: string; color: string }> = {
  spawns: { label: "派生", color: "#10b981" },
  yields: { label: "产出", color: "#f59e0b" },
  derived_from: { label: "汇聚", color: "#6366f1" },
  proves: { label: "证明", color: "#ef4444" },
  belongs: { label: "归属", color: "#6366f1" },
  resolves: { label: "解析", color: "#10b981" },
  deploys: { label: "部署于", color: "#f59e0b" },
};

// TaskNode.type 的静态类型是渗透阶段的 ExploreKind,但实际节点类型由后端契约
// 决定(recon_* 与 asset_* 都是合法字符串)——按字符串比较,不走联合类型收窄。
function viewKind(n: TaskNode): ReconKind {
  const t = n.type as string;
  if (t === "recon_step") return "recon_step";
  if (t === "recon_finding") return "recon_finding";
  if (t === "recon_summary") return "recon_summary";
  if (t === "asset_root") return "asset_root";
  if (t === "asset_domain") return "asset_domain";
  if (t === "asset_ip") return "asset_ip";
  if (t === "asset_service") return "asset_service";
  return "begin"; // fact/origin 起点 + 其余兜底
}

type Payload = Record<string, unknown>;

function parsePayload(n: TaskNode): Payload {
  const raw = (n.payload ?? "").trim();
  if (!raw) return {};
  try {
    const obj: unknown = JSON.parse(raw);
    return obj && typeof obj === "object" ? (obj as Payload) : {};
  } catch {
    return { text: raw };
  }
}

function nodeTitle(n: TaskNode): string {
  const kind = viewKind(n);
  const p = parsePayload(n);
  switch (kind) {
    case "recon_step": {
      const title = typeof p.title === "string" ? p.title : typeof p.collector === "string" ? p.collector : "步骤";
      const status = typeof p.status === "string" ? p.status : "";
      return status ? `${title} · ${status === "done" ? "完成" : status === "failed" ? "失败" : status}` : title;
    }
    case "recon_finding": {
      const value = typeof p.value === "string" ? p.value : "";
      const port = typeof p.port === "number" && p.port > 0 ? `:${p.port}` : "";
      const type = typeof p.type === "string" ? p.type : "";
      return value ? `[${type}] ${value}${port}` : "发现";
    }
    case "recon_summary":
      return typeof p.summary === "string" ? p.summary : "汇总";
    case "asset_root":
    case "asset_domain":
    case "asset_ip":
    case "asset_service": {
      const value = typeof p.value === "string" ? p.value : "";
      const port = typeof p.port === "number" && p.port > 0 ? `:${p.port}` : "";
      return value ? `${value}${port}` : "资产";
    }
    default:
      return typeof p.summary === "string" ? p.summary : "目标";
  }
}

function nodeSub(n: TaskNode): string {
  const p = parsePayload(n);
  const kind = viewKind(n);
  if (kind === "recon_finding" && typeof p.source === "string") return `来源 ${p.source}`;
  if (kind === "asset_service" && typeof p.service_name === "string" && p.service_name) return p.service_name;
  if (kind === "asset_root" && typeof p.icp === "string" && p.icp) return p.icp;
  return "";
}

// ---- 布局:四层水平流 起点→步骤→发现→汇总 ----

function layout(nodes: TaskNode[], edges: Edge[]): RFNode[] {
  const levelOf = new Map<string, number>();
  for (const n of nodes) {
    const k = viewKind(n);
    const lv =
      k === "begin"
        ? 0
        : k === "recon_step" || k === "asset_root"
          ? 1
          : k === "recon_finding" || k === "asset_domain"
            ? 2
            : k === "asset_ip"
              ? 3
              : k === "recon_summary" || k === "asset_service"
                ? 4
                : 1;
    levelOf.set(n.id, lv);
  }
  // 步骤/发现按同层出现顺序纵向排布
  const counters = new Map<number, number>();
  const positions = new Map<string, { x: number; y: number }>();
  for (const n of nodes) {
    const level = levelOf.get(n.id) ?? 1;
    const idx = counters.get(level) ?? 0;
    counters.set(level, idx + 1);
    positions.set(n.id, { x: level * 300, y: idx * 120 });
  }
  return nodes.map((n) => {
    const pos = positions.get(n.id) ?? { x: 0, y: 0 };
    const kind = viewKind(n);
    const isAsset = kind.startsWith("asset_");
    let level = 0;
    try {
      const p = JSON.parse(n.payload ?? "{}") as { level?: number };
      if (typeof p.level === "number") level = p.level;
    } catch {
      /* 默认 0 */
    }
    return {
      id: n.id,
      // 资产节点用四向把手卡片(回边分流),任务节点保持原双把手卡片。
      type: isAsset ? "assetCard" : "reconCard",
      position: pos,
      data: { kind, title: nodeTitle(n), sub: nodeSub(n), state: n.state, level },
    } satisfies RFNode;
  });
}

function toRFEdges(edges: Edge[]): RFEdge[] {
  return edges
    .filter((e) => relMeta[e.rel])
    .map((e) => {
      const meta = relMeta[e.rel] ?? { label: e.rel, color: "#94a3b8" };
      // 资产转换器会随边携带把手选择与走线类型(smoothstep);任务边无此字段走默认 bezier。
      const routed = e as unknown as { sourceHandle?: string; targetHandle?: string; type?: string };
      return {
        id: `${e.src}-${e.rel}-${e.dst}`,
        source: e.src,
        target: e.dst,
        sourceHandle: routed.sourceHandle,
        targetHandle: routed.targetHandle,
        type: routed.type,
        label: meta.label,
        style: { stroke: meta.color, strokeWidth: 1.5 },
        labelStyle: { fill: meta.color, fontSize: 11 },
        labelBgStyle: { fillOpacity: 0 },
      } satisfies RFEdge;
    });
}

function ReconCardNode({ data }: NodeProps) {
  const d = data as { kind: ReconKind; title: string; sub: string; state: string };
  const meta = typeMeta[d.kind] ?? typeMeta.begin;
  const Icon = meta.icon;
  return (
    <div
      className={cn(
        "w-[240px] rounded-lg border bg-card p-3 shadow-sm transition-shadow hover:shadow-md",
        d.kind === "recon_step" && d.state === "failed" && "border-red-500/40",
      )}
    >
      <Handle type="target" position={Position.Left} className="!bg-muted-foreground" />
      <div className="flex items-center gap-2">
        <span className={cn("flex size-6 shrink-0 items-center justify-center rounded-md text-white", meta.iconBg)}>
          <Icon className="size-3.5" />
        </span>
        <span className="text-muted-foreground text-xs">{meta.label}</span>
      </div>
      <div className="mt-2 line-clamp-3 font-medium text-sm break-all">{d.title}</div>
      {d.sub ? <div className="text-muted-foreground mt-1 text-xs">{d.sub}</div> : null}
      <Handle type="source" position={Position.Right} className="!bg-muted-foreground" />
    </div>
  );
}

const nodeTypes = { reconCard: ReconCardNode, assetCard: AssetCardNode };

// 资产卡片:四向把手 —— 正向边右出(sr)左入(tl),反向边左出(sl)右入(tr),
// 回边沿卡片外侧走,不和正向边拧团。sourceHandle/targetHandle 由转换器按层级写入边。
function AssetCardNode({ data }: NodeProps) {
  const d = data as { kind: ReconKind; title: string; sub: string; level: number };
  const meta = typeMeta[d.kind] ?? typeMeta.asset_domain;
  const Icon = meta.icon;
  return (
    <div className="bg-card w-[230px] rounded-lg border p-3 shadow-sm transition-shadow hover:shadow-md">
      <Handle id="tl" type="target" position={Position.Left} className="!bg-muted-foreground" />
      <Handle id="sl" type="source" position={Position.Left} className="!bg-muted-foreground" />
      <div className="flex items-center gap-2">
        <span className={cn("flex size-6 shrink-0 items-center justify-center rounded-md text-white", meta.iconBg)}>
          <Icon className="size-3.5" />
        </span>
        <span className="text-muted-foreground text-xs">{meta.label}</span>
      </div>
      <div className="mt-2 line-clamp-2 font-medium text-sm break-all">{d.title}</div>
      {d.sub ? <div className="text-muted-foreground mt-1 truncate text-xs">{d.sub}</div> : null}
      <Handle id="tr" type="target" position={Position.Right} className="!bg-muted-foreground" />
      <Handle id="sr" type="source" position={Position.Right} className="!bg-muted-foreground" />
    </div>
  );
}

function LegendPanel({ kinds }: { kinds: ReconKind[] }) {
  const present = kinds.length > 0 ? kinds : (Object.keys(typeMeta) as ReconKind[]);
  return (
    <Panel position="top-left">
      <div className="bg-background/90 flex flex-wrap items-center gap-3 rounded-lg border p-2 text-xs shadow-sm backdrop-blur">
        {present
          .filter((k) => typeMeta[k])
          .map((k) => {
            const meta = typeMeta[k];
            const Icon = meta.icon;
            return (
              <span key={k} className="flex items-center gap-1">
                <span className={cn("flex size-4 items-center justify-center rounded text-white", meta.iconBg)}>
                  <Icon className="size-2.5" />
                </span>
                {meta.label}
              </span>
            );
          })}
      </div>
    </Panel>
  );
}

// ⚠️ ReactFlow 配置铁律(对照 exploration-graph 的被验证配置):
// fitView + fitViewOptions prop 是本版本(v12)的正确姿势——内部会延迟到节点
// 测量完成后才执行适配,数据异步到达也生效。不要画蛇添足改成 rAF/手动机括,
// 那些全是赛跑(v1/v2 两次翻车的根因)。
// ⚠️ 不要加 onlyRenderVisibleElements:视口外节点不渲染=不测量,fitView 只
// 计算"已测量"节点 → 初始视口外的节点永远进不了适配,画面只剩角落里几个
// 孤儿节点(v3 翻车根因)。节点量级几百以内全渲染即可。
function GraphInner({ nodes, edges, className }: { nodes: TaskNode[]; edges: Edge[]; className?: string }) {
  const built = React.useMemo(() => {
    const rfNodes = layout(nodes, edges);
    const rfEdges = toRFEdges(edges);
    return { rfNodes, rfEdges };
  }, [nodes, edges]);
  const [rfNodes, setNodes, onNodesChange] = useNodesState<RFNode>([]);
  const [rfEdges, setEdges, onEdgesChange] = useEdgesState<RFEdge>([]);

  React.useEffect(() => {
    setNodes(built.rfNodes);
    setEdges(built.rfEdges);
  }, [built, setNodes, setEdges]);

  return (
    <div className={cn("relative", className)}>
      <ReactFlow
        nodes={rfNodes}
        edges={rfEdges}
        onNodesChange={onNodesChange}
        onEdgesChange={onEdgesChange}
        nodeTypes={nodeTypes}
        fitView
        fitViewOptions={{ padding: 0.2 }}
        minZoom={0.1}
        proOptions={{ hideAttribution: true }}
      >
        <Background variant={BackgroundVariant.Dots} gap={20} size={1} />
        <Controls />
        <MiniMap
          pannable
          zoomable
          className="!bg-card !rounded-lg !border !shadow-sm"
          maskColor="rgb(148 163 184 / 0.18)"
          nodeColor={(node) => {
            const k = (node.data as { kind?: ReconKind }).kind;
            return typeMeta[k ?? "begin"]?.hex ?? "#94a3b8";
          }}
          nodeStrokeWidth={0}
          nodeBorderRadius={4}
        />
        <LegendPanel kinds={Array.from(new Set(rfNodes.map((n) => (n.data as { kind: ReconKind }).kind)))} />
        {/* 诊断:实际进渲染管线的节点/边数(与数据源数量对比,定位丢失环节) */}
        <Panel position="bottom-right">
          <span className="bg-background/80 rounded border px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
            nf {rfNodes.length}/{built.rfNodes.length}n · {rfEdges.length}/{built.rfEdges.length}e
          </span>
        </Panel>
      </ReactFlow>
    </div>
  );
}

export function ReconGraph({ nodes, edges, className }: { nodes: TaskNode[]; edges: Edge[]; className?: string }) {
  return (
    <ReactFlowProvider>
      <GraphInner nodes={nodes} edges={edges} className={className} />
    </ReactFlowProvider>
  );
}
