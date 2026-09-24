"use client";

// 目标详情页的共享小件:Field 包装、资产类型/来源文案、手工录入解析。
//
// 抽成一份是因为「资产」页签与「添加资产」对话框必须用同一套类型推断与文案:
// 各写一份必然漂移,用户会在预览表里看到与表格不一致的叫法。

import * as React from "react";

import { Label } from "@/components/ui/label";

/** 资产类型 → 中文文案,与 /function/assets 分类器同一套叫法。 */
export const ASSET_TYPE_LABELS: Record<string, string> = {
  root_domain: "根域名",
  subdomain: "子域名",
  ip: "IP",
  service: "服务",
  app: "应用",
  endpoint: "端点",
};

/** 分类器顺序:后端 counts 是 map,顺序得前端定,否则每次渲染页签乱跳。 */
export const ASSET_TYPE_ORDER = ["root_domain", "subdomain", "ip", "service", "app", "endpoint"] as const;

/** 类型徽标配色。 */
export const ASSET_TYPE_BADGES: Record<string, string> = {
  root_domain: "bg-blue-500/15 text-blue-600 border-blue-500/30",
  subdomain: "bg-cyan-500/15 text-cyan-600 border-cyan-500/30",
  ip: "bg-violet-500/15 text-violet-600 border-violet-500/30",
  service: "bg-amber-500/15 text-amber-600 border-amber-500/30",
  app: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30",
  endpoint: "bg-rose-500/15 text-rose-600 border-rose-500/30",
};

/** 目标资产来源:手工录入 / 信息搜集 / 已有资产。 */
export const ASSET_SOURCE_LABELS: Record<string, string> = {
  manual: "手工录入",
  recon: "信息搜集",
  existing: "已有资产",
};

export const ASSET_SOURCE_BADGES: Record<string, string> = {
  manual: "bg-muted text-muted-foreground",
  recon: "bg-blue-500/15 text-blue-600 border-blue-500/30",
  existing: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30",
};

export function assetTypeLabel(type?: string): string {
  if (!type) return "—";
  return ASSET_TYPE_LABELS[type] ?? type;
}

export function assetSourceLabel(source?: string): string {
  if (!source) return "—";
  return ASSET_SOURCE_LABELS[source] ?? source;
}

export function fmtTime(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

/** 表单字段包装:详情页各页签统一用这一种标签样式。 */
export function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1.5">
      <Label className="font-medium text-muted-foreground text-xs uppercase tracking-wide">{label}</Label>
      {children}
    </div>
  );
}

/**
 * SaveBridge 是「头部保存按钮」与「各页签表单」之间的桥。
 * 头部只有一个保存按钮,而四个页签里只有概览/授权范围有可保存的表单——
 * 让页签把自己的保存动作注册上来,头部按当前页签调用,避免把全部表单状态都提到页面级
 * (那样任何一次输入都会重渲染整页,包括资产表格)。
 */
export interface SaveBridge {
  dirty: boolean;
  saving: boolean;
  save: () => Promise<void>;
}

/** 手工录入解析出的一行(预览表里可改,所以 type/value/port 都是独立可编辑字段)。 */
export interface ParsedAssetRow {
  key: string;
  raw: string;
  type: string;
  value: string;
  port: number;
}

/** isIPv4 严格校验(每段 0-255 且无前导零),避免把 999.1.1.1 当成 IP 收下。 */
function isIPv4(v: string): boolean {
  const parts = v.split(".");
  if (parts.length !== 4) return false;
  return parts.every((p) => /^\d{1,3}$/.test(p) && Number(p) <= 255 && (p === "0" || !p.startsWith("0")));
}

/** isIPv6 只做形态判断:录入阶段不该用完整解析器把用户挡在门外,预览表可改。 */
function isIPv6(v: string): boolean {
  return /^[0-9a-fA-F:.]+$/.test(v) && (v.match(/:/g)?.length ?? 0) >= 2;
}

/** splitHostPort 拆 host:port;[::1]:443 这种带方括号的 IPv6 也支持。 */
function splitHostPort(v: string): { host: string; port: number } | null {
  if (v.startsWith("[")) {
    const m = /^\[([^\]]+)\](?::(\d{1,5}))?$/.exec(v);
    return m ? { host: m[1], port: m[2] ? Number(m[2]) : 0 } : null;
  }
  const idx = v.lastIndexOf(":");
  if (idx <= 0) return null;
  const host = v.slice(0, idx);
  const portText = v.slice(idx + 1);
  if (!/^\d{1,5}$/.test(portText)) return null;
  // 裸 IPv6 里的冒号不是端口分隔符,直接放弃拆分。
  if (host.includes(":")) return null;
  return { host, port: Number(portText) };
}

/**
 * inferAsset 按用户一行文本推断资产类型。
 * 规则(与需求约定一致):含 :// → service(url);能当 IP 或 IP:端口 → ip;
 * 否则含点 → 去掉 www 后段数 >=3 判子域名,否则根域名;拿不准一律按根域名收。
 */
export function inferAsset(line: string): { type: string; value: string; port: number } {
  const raw = line.trim();
  if (raw.includes("://")) {
    let port = 0;
    try {
      const u = new URL(raw);
      if (u.port) port = Number(u.port);
    } catch {
      // 非法 URL 也原样收下:让用户在预览表里人工修正,好过静默丢行。
    }
    return { type: "service", value: raw, port };
  }
  const hp = splitHostPort(raw);
  if (hp && (isIPv4(hp.host) || isIPv6(hp.host))) {
    return { type: "ip", value: hp.host, port: hp.port };
  }
  if (isIPv4(raw) || isIPv6(raw)) return { type: "ip", value: raw, port: 0 };
  // 域名带端口(如 api.acme.com:8443)也必须把端口拆出来:留在 value 里会写成
  // 「api.acme.com:8443」这种既不是合法域名、端口列又空着的资产。
  if (hp && hp.host.includes(".")) {
    const host = hp.host.replace(/^www\./i, "");
    const labels = host.split(".").filter(Boolean);
    return { type: labels.length >= 3 ? "subdomain" : "root_domain", value: hp.host, port: hp.port };
  }
  // 只取主机段数判断类型,值仍保留用户原文(可能带路径,后端按原值入库)。
  const host = (raw.split("/")[0] ?? raw).replace(/^www\./i, "");
  const labels = host.split(".").filter(Boolean);
  return { type: labels.length >= 3 ? "subdomain" : "root_domain", value: raw, port: 0 };
}

/** parseAssetLines 把多行文本转成预览行;空行忽略。 */
export function parseAssetLines(text: string): ParsedAssetRow[] {
  return text
    .split(/\r?\n/)
    .map((l) => l.trim())
    .filter(Boolean)
    .map((raw, i) => ({ key: `${i}-${raw}`, raw, ...inferAsset(raw) }));
}
