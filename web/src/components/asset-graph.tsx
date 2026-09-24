// 企业资产 → 任务图(TaskNode/Edge)转换器。
// 渲染统一走 ReconGraph(与任务详情的链路图同一程序,零分叉);
// 本模块只做数据形状转换:Asset[] → recon-graph 可消费的节点/边。
//
// 节点类型:asset_root(根域名) / asset_domain(子域名) / asset_ip / asset_service;
// 边:belongs(子域→根域,后缀匹配) / resolves(IP→bound_domains) / deploys(服务→URL host 命中节点)。

import type { Asset, Edge, TaskNode } from "@/lib/types";

type AssetGraphKind = "asset_root" | "asset_domain" | "asset_ip" | "asset_service";

function kindOf(a: Asset): AssetGraphKind {
  const t = a.type as string;
  if (t === "root_domain") return "asset_root";
  if (t === "subdomain" || t === "domain") return "asset_domain";
  if (t === "ip") return "asset_ip";
  return "asset_service"; // service/app/endpoint 都进服务层
}

function valueOf(a: Asset): string {
  switch (kindOf(a)) {
    case "asset_root":
      return a.domain || a.root_domain || `#${a.id}`;
    case "asset_domain":
      return a.domain || `#${a.id}`;
    case "asset_ip":
      return a.ip || `#${a.id}`;
    case "asset_service":
      return a.url || `${a.domain || a.ip || ""}${a.port ? `:${a.port}` : ""}` || `#${a.id}`;
  }
}

function subOf(a: Asset): string {
  if (kindOf(a) === "asset_service") {
    const parts = [a.service_name, a.service_type, a.page_title?.slice(0, 30)].filter(Boolean);
    return parts.join(" · ");
  }
  if (kindOf(a) === "asset_root") return a.icp ?? "";
  return a.record_type ?? "";
}

const LEVEL: Record<AssetGraphKind, number> = { asset_root: 1, asset_domain: 2, asset_ip: 3, asset_service: 4 };

function hostOf(u: string): string {
  try {
    return new URL(u).hostname.toLowerCase();
  } catch {
    return u.toLowerCase();
  }
}

export function assetsToTaskGraph(assets: Asset[]): { nodes: TaskNode[]; edges: Edge[] } {
  const nodes: TaskNode[] = [];
  const edges: Edge[] = [];
  const byKindValue = new Map<string, string>(); // `${kind}|${value}` → node id

  for (const a of assets) {
    const kind = kindOf(a);
    const value = valueOf(a);
    const id = `a${a.id}`;
    byKindValue.set(`${kind}|${value}`, id);
    // domain/ip 也可按纯值索引(边命中用)。
    if (kind === "asset_root" || kind === "asset_domain") byKindValue.set(`d|${value}`, id);
    if (kind === "asset_ip") byKindValue.set(`i|${value}`, id);
    nodes.push({
      id,
      // 渲染层(recon-graph)按字符串识别 asset_* 类型;这里双转以适配 TaskNode 静态类型。
      type: kind as unknown as TaskNode["type"],
      state: "confirmed",
      priority: 0,
      origin: "assets",
      ts: a.last_seen || "",
      payload: JSON.stringify({
        value,
        port: a.port ?? 0,
        service_name: a.service_name ?? "",
        icp: a.icp ?? "",
        level: LEVEL[kind], // 层级随节点走,边据此选进出边(正向右出左入,反向左出右入)
      }),
    });
  }

  const find = (k: "d" | "i", value: string): string | undefined =>
    byKindValue.get(`${k}|${value}`) ?? byKindValue.get(`d|${value}`) ?? byKindValue.get(`i|${value}`);

  // 层级表:边据此选进出把手(正向右出左入,反向/同层左出右入),避免回边拧团。
  const levels = new Map<string, number>();
  for (const n of nodes) {
    try {
      const p = JSON.parse(n.payload ?? "{}") as { level?: number };
      if (typeof p.level === "number") levels.set(n.id, p.level);
    } catch {
      /* 无 level 的节点走正向默认 */
    }
  }

  const link = (src: string | undefined, rel: string, dst: string | undefined) => {
    if (!src || !dst || src === dst) return;
    const backward = (levels.get(src) ?? 0) >= (levels.get(dst) ?? 0);
    edges.push({
      src,
      dst,
      rel: rel as unknown as Edge["rel"],
      sourceHandle: backward ? "sl" : "sr",
      targetHandle: backward ? "tr" : "tl",
      type: "smoothstep",
    } as unknown as Edge);
  };

  for (const a of assets) {
    const kind = kindOf(a);
    const self = byKindValue.get(`${kind}|${valueOf(a)}`);
    if (kind === "asset_domain" && a.root_domain) {
      link(self, "belongs", find("d", a.root_domain));
    }
    if (kind === "asset_ip" && a.bound_domains) {
      for (const d of a.bound_domains) link(self, "resolves", find("d", d.toLowerCase()));
    }
    if (kind === "asset_service") {
      const host = a.url ? hostOf(a.url) : (a.domain || a.ip || "");
      if (host) link(self, "deploys", find("d", host) ?? find("i", host));
    }
  }

  return { nodes, edges };
}
