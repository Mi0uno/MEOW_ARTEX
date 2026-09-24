"use client";

// 代理中心 · 流量与调度 —— 代理池出网的总览页。
//
// 与「流量」页的分工:流量页是 MITM 录制的全量 HTTP 往来(含报文原文);本页只看
// 代理池这一条出网链路——当前出口是谁、哪个代理正在被调度、失败落在哪条代理上。
//
// 出网模式(本地裸网 / 代理池)在这里一键切换。切换会重建 agent(出网地址在 agent
// 构造时注入),只对之后新启动的 run 生效,进行中的 run 不受影响;这一点必须写在
// 页头副标题里,否则用户切换后看不到立刻变化,会以为"切了没反应"。

import * as React from "react";

import Link from "next/link";

import {
  ActivityIcon,
  ArrowRightLeftIcon,
  GlobeIcon,
  Loader2Icon,
  SearchIcon,
  WaypointsIcon,
  ZapIcon,
} from "lucide-react";
import { toast } from "sonner";

import {
  fmtLatency,
  groupKindBadgeClass,
  proxyStatusBadgeClass,
  sourceLabel,
  strategyLabel,
} from "@/components/proxy-dialogs";
import { TablePagination } from "@/components/table-pagination";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { ProxyEgressStatus, ProxyGroup, ProxyRequest } from "@/lib/types";
import { cn } from "@/lib/utils";

const PAGE_SIZE = 50;
// 与后端 window_mins 对齐;默认 1 小时。
const WINDOWS = [
  { mins: 15, label: "最近 15 分钟" },
  { mins: 60, label: "最近 1 小时" },
  { mins: 360, label: "最近 6 小时" },
  { mins: 1440, label: "最近 24 小时" },
];
// 后端按分钟分桶,24 小时窗口能有上千个桶;趋势图最多画这么多柱,多出来的按序合并。
const MAX_BARS = 72;
// 冷启动时 summary/egress 可能是 null(后端空集合序列化成 JSON null),给一份零值兜底,
// 保证首帧不白屏。
const EMPTY_SUMMARY = {
  total: 0,
  failed: 0,
  success_rate: 0,
  avg_ms: 0,
  bytes_in: 0,
  bytes_out: 0,
  window_mins: 60,
};
// 代理组类型 / 代理状态的展示文案(徽章配色由 proxy-dialogs 的助手函数给)。
const GROUP_KIND_LABEL: Record<string, string> = {
  manual: "手工",
  api: "API 接入",
  wild: "在野搜集",
  mixed: "混合",
};
const PROXY_STATUS_LABEL: Record<string, string> = {
  untested: "未测",
  alive: "可用",
  dead: "失效",
  checking: "测活中",
};

function fmtTime(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

function fmtClock(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}

function fmtBytes(n: number): string {
  if (!n || n <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), units.length - 1);
  const v = n / 1024 ** i;
  return `${i === 0 ? v : v.toFixed(1)} ${units[i]}`;
}

// num 把可能为 null / undefined / NaN 的数值收敛成 0。
// 后端把空集合序列化成 JSON null 是这台平台的既有行为,而 null 参与算术会变成 NaN、
// 参与 .toFixed() 会直接抛错白屏;所有来自接口的数字都从这里过一遍。
function num(n: number | null | undefined): number {
  return typeof n === "number" && Number.isFinite(n) ? n : 0;
}

export default function ProxyCenterPage() {
  return (
    <React.Suspense fallback={null}>
      <ProxyCenterInner />
    </React.Suspense>
  );
}

function ProxyCenterInner() {
  const [dashboard, setDashboard] = React.useState<Awaited<ReturnType<typeof api.proxyDashboard>> | null>(null);
  const [egress, setEgress] = React.useState<ProxyEgressStatus | null>(null);
  const [groups, setGroups] = React.useState<ProxyGroup[]>([]);
  const [targetGroup, setTargetGroup] = React.useState("0"); // 切换代理池时使用的组
  const [windowMins, setWindowMins] = React.useState(60);
  const [scopeGroup, setScopeGroup] = React.useState("all"); // 统计与流水的代理组范围
  const [requests, setRequests] = React.useState<ProxyRequest[]>([]);
  const [reqTotal, setReqTotal] = React.useState(0);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);
  const [query, setQuery] = React.useState(""); // 输入框原文
  const [queryApplied, setQueryApplied] = React.useState(""); // 已提交给服务端的检索词
  const [onlyFailed, setOnlyFailed] = React.useState("all");
  const [loading, setLoading] = React.useState(true);
  const [busy, setBusy] = React.useState(false);
  const [updatedAt, setUpdatedAt] = React.useState("");
  // 用户是否手动选过目标组:选过之后就不再自动对齐,否则轮询回来的状态会把选择改掉。
  const targetTouched = React.useRef(false);
  // alive 在组件卸载后置 false:5s 轮询的响应可能比卸载晚到,那时 setState 会报警告。
  const alive = React.useRef(true);
  React.useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  const loadGroups = React.useCallback(() => {
    api
      .proxyGroups({ limit: 200 })
      .then((r) => {
        if (alive.current) setGroups(r.groups);
      })
      .catch(() => {
        // 组列表拉不到不影响出网状态展示,静默保留上一份。
      });
  }, []);

  React.useEffect(() => {
    loadGroups();
  }, [loadGroups]);

  // req=false 时只刷新总览/趋势/调度排行,不动流水(轮询翻到历史页时用)。
  const load = React.useCallback(
    (win: number, gid: string, p: number, size: number, failed: string, q: string, req: boolean) => {
      const g = gid === "all" ? 0 : Number(gid);
      api
        .proxyDashboard({ window_mins: win, group_id: g || undefined })
        .then((d) => {
          if (!alive.current) return;
          setDashboard(d);
          // egress 后端未归一化(可能为 null),只在拿到值时覆盖,避免把好状态抹掉。
          if (d.egress) setEgress(d.egress);
          setUpdatedAt(new Date().toISOString());
        })
        .catch(() => {
          // 轮询期间的瞬时失败保留上一份快照,不弹 toast(每 5s 一次会刷屏)。
        });
      if (!req) return;
      setLoading(true);
      api
        .proxyRequests({
          group_id: g || undefined,
          only_failed: failed === "1",
          q,
          limit: size,
          offset: (p - 1) * size,
        })
        .then((r) => {
          if (!alive.current) return;
          setRequests(r.requests);
          setReqTotal(r.total);
        })
        .catch((e) => {
          if (alive.current) toast.error(`加载请求流水失败:${(e as Error).message}`);
        })
        .finally(() => {
          if (alive.current) setLoading(false);
        });
    },
    [],
  );

  React.useEffect(() => {
    // 依赖变化(翻页 / 筛选 / 窗口 / 组范围)必须取一次流水。
    load(windowMins, scopeGroup, page, pageSize, onlyFailed, queryApplied, true);
    const t = setInterval(() => {
      // 轮询:总览恒刷新;流水只在第 1 页刷新——翻到历史页时把用户拽回第 1 页更烦人,
      // 与「流量」页的自动刷新策略一致。
      load(windowMins, scopeGroup, page, pageSize, onlyFailed, queryApplied, page === 1);
    }, 5000);
    return () => clearInterval(t);
  }, [load, windowMins, scopeGroup, page, pageSize, onlyFailed, queryApplied]);

  React.useEffect(() => {
    // 目标组默认对齐当前生效组:代理池下选择器初始就指向正在用的组,直连下也给一个
    // 可切的目标(否则"直连 → 代理池"无从选起)。用户手动选过就不再覆盖。
    if (targetTouched.current) return;
    const active = egress?.active_group_id ?? 0;
    if (active > 0) {
      setTargetGroup(String(active));
      return;
    }
    if (groups.length > 0) setTargetGroup((cur) => (cur === "0" ? String(groups[0].id) : cur));
  }, [egress, groups]);

  const search = () => {
    const q = query.trim();
    setPage(1);
    // 检索词没变时(例如连点两次「搜索」)依赖不会变化,直接手动取一次首屏。
    if (q === queryApplied) {
      load(windowMins, scopeGroup, 1, pageSize, onlyFailed, q, true);
      return;
    }
    setQueryApplied(q);
  };

  const switchEgress = async (mode: "direct" | "pool") => {
    const gid = mode === "pool" ? Number(targetGroup) || 0 : 0;
    const gname = groups.find((g) => g.id === gid)?.name ?? (gid > 0 ? `#${gid}` : "");
    const msg =
      mode === "pool"
        ? `切换到代理池?切换后新启动的 agent 出网将走代理组「${gname}」,进行中的 run 不受影响。`
        : "切换到本地裸网(直连)?切换后新启动的 agent 将直接出网,进行中的 run 不受影响。";
    if (!window.confirm(msg)) return;
    setBusy(true);
    try {
      const st = await api.proxySetEgress(mode, gid);
      if (st) setEgress(st);
      toast.success(mode === "pool" ? `已切换到代理池:${gname}` : "已切换到本地裸网");
      loadGroups();
      load(windowMins, scopeGroup, page, pageSize, onlyFailed, queryApplied, true);
    } catch (e) {
      toast.error(`切换失败:${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  // summary 的每个数字都过一遍 num():后端把空集合序列化成 JSON null 是这台平台的既有
  // 行为,少一个兜底就是整页白屏(不是渲染错一格)。
  const summary = dashboard?.summary ?? EMPTY_SUMMARY;
  const summaryTotal = num(summary.total);
  const summaryFailed = num(summary.failed);
  const summaryRate = num(summary.success_rate);
  const summaryAvg = num(summary.avg_ms);
  const summaryIn = num(summary.bytes_in);
  const summaryOut = num(summary.bytes_out);
  const series = dashboard?.series ?? [];
  const usage = dashboard?.usage ?? [];
  const pool = (egress?.mode ?? "direct") === "pool";
  const activeGroupID = egress?.active_group_id ?? 0;
  const targetID = Number(targetGroup) || 0;
  const targetChanged = pool && targetID > 0 && targetID !== activeGroupID;
  // 直连时选中的组来自"上次生效的组"(选择器在直连下禁用),因此按钮在直连下恒可点;
  // 代理池下只有选了别的组才可点 —— 与"禁用与当前模式一致的那个按钮"是同一件事。
  const canSwitchPool = !busy && (pool ? targetChanged : targetID > 0);
  const activeGroup = groups.find((g) => g.id === activeGroupID);
  // 生效出口:优先后端算好的有效地址;代理池下退到本地监听地址;都空 = 裸网直连。
  const effAddr = egress?.effective_proxy || (pool ? (egress?.listen_addr ?? "") : "");
  // 直连下这个按钮是"进入代理池";已经在池里时它只用于换组,按实际动作叫「切换到该代理组」,
  // 否则"切换到代理池"在池内会让人以为点了没反应。
  const poolButtonLabel = pool ? "切换到该代理组" : "切换到代理池";
  const usageGrand = usage.reduce((a, u) => a + num(u.total), 0);

  // 按序合并过密的桶,保证柱子可读(标签取每段的起止时刻)。
  const bars = React.useMemo(() => {
    if (series.length <= MAX_BARS) {
      return series.map((s) => ({
        key: s.bucket,
        label: fmtClock(s.bucket),
        total: num(s.total),
        failed: num(s.failed),
      }));
    }
    const size = Math.ceil(series.length / MAX_BARS);
    const out: { key: string; label: string; total: number; failed: number }[] = [];
    for (let i = 0; i < series.length; i += size) {
      const chunk = series.slice(i, i + size);
      const first = chunk[0];
      const last = chunk[chunk.length - 1];
      out.push({
        key: first.bucket,
        label: `${fmtClock(first.bucket)}–${fmtClock(last.bucket)}`,
        total: chunk.reduce((a, b) => a + num(b.total), 0),
        failed: chunk.reduce((a, b) => a + num(b.failed), 0),
      });
    }
    return out;
  }, [series]);
  const peak = bars.reduce((m, b) => Math.max(m, b.total), 0);
  // 均匀取最多 6 个刻度位置(含首尾),避免柱子多时标签挤在一起。
  const tickIdx = React.useMemo(() => {
    if (bars.length === 0) return [];
    const step = Math.max(1, Math.round((bars.length - 1) / 5));
    const out: number[] = [];
    for (let i = 0; i < bars.length; i += step) out.push(i);
    if (out[out.length - 1] !== bars.length - 1) out.push(bars.length - 1);
    return out;
  }, [bars]);

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4">
        <h1 className="font-semibold text-xl">代理中心 · 流量与调度</h1>
        <p className="text-muted-foreground text-sm">
          代理池出网的总览:当前出口、正在被调度的代理与逐条请求流水。出网模式切换会重建 agent,只对之后新启动的 run
          生效,进行中的 run 不受影响。
        </p>
      </div>

      {/* ── 出网模式与快速切换 ── */}
      <Card className="mb-4">
        <CardContent className="pt-4">
          <div className="flex flex-wrap items-start gap-x-10 gap-y-4">
            <div className="grid gap-1.5">
              <p className="text-muted-foreground flex items-center gap-1 text-xs">
                <ArrowRightLeftIcon className="size-3" />
                当前出网模式
              </p>
              <div className="flex items-center gap-2">
                <Badge
                  variant="outline"
                  className={
                    pool
                      ? "border-blue-500/30 bg-blue-500/15 text-blue-600 dark:text-blue-400"
                      : "bg-muted text-muted-foreground"
                  }
                >
                  {pool ? <WaypointsIcon /> : <GlobeIcon />}
                  {pool ? "代理池" : "本地裸网(直连)"}
                </Badge>
                <span
                  className={cn(
                    "inline-flex items-center gap-1 text-xs",
                    egress?.running ? "text-emerald-600 dark:text-emerald-400" : "text-muted-foreground",
                  )}
                >
                  <ZapIcon className="size-3" />
                  {egress?.running ? "调度器运行中" : "调度器未运行"}
                </span>
              </div>
            </div>

            <div className="grid min-w-0 gap-1.5">
              <p className="text-muted-foreground text-xs">生效出口</p>
              {effAddr ? (
                <span className="max-w-[22rem] truncate font-mono text-xs" title={effAddr}>
                  {effAddr}
                </span>
              ) : (
                <span className="text-muted-foreground text-xs">直连(无出口代理)</span>
              )}
            </div>

            <div className="grid gap-1.5">
              <p className="text-muted-foreground text-xs">代理组</p>
              {pool ? (
                <div className="flex items-center gap-2">
                  <span className="text-sm">{egress?.active_group_name || "—"}</span>
                  {activeGroup ? (
                    <>
                      <Badge variant="outline" className={groupKindBadgeClass(activeGroup.kind)}>
                        {GROUP_KIND_LABEL[activeGroup.kind] ?? activeGroup.kind}
                      </Badge>
                      <span className="text-muted-foreground text-xs">
                        策略 {strategyLabel(activeGroup.strategy)} · 可用 {activeGroup.alive_count}/
                        {activeGroup.proxy_count}
                      </span>
                    </>
                  ) : null}
                </div>
              ) : (
                <span className="text-muted-foreground text-xs">未启用</span>
              )}
            </div>

            <div className="grid gap-1.5">
              <p className="text-muted-foreground text-xs">全平台代理</p>
              <span className="text-xs tabular-nums">
                可用 <span className="text-emerald-600 dark:text-emerald-400">{egress?.alive_proxies ?? 0}</span> / 共{" "}
                {egress?.total_proxies ?? 0}
                {(egress?.dead_proxies ?? 0) > 0 ? (
                  <span className="text-muted-foreground"> · 失效 {egress?.dead_proxies}</span>
                ) : null}
              </span>
            </div>
          </div>

          <div className="mt-4 flex flex-wrap items-center gap-2 border-t pt-3">
            <span className="text-muted-foreground text-xs">切换目标组</span>
            <Select
              value={targetGroup}
              onValueChange={(v) => {
                targetTouched.current = true;
                setTargetGroup(v);
              }}
              // 直连模式下没有"当前生效组"可选,禁用;目标组取上次生效的组(见上方对齐 effect)。
              disabled={!pool || busy}
            >
              <SelectTrigger className="w-64" title={pool ? undefined : "直连模式下按上次生效的组启用代理池"}>
                <SelectValue placeholder="选择代理组" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="0">未选择</SelectItem>
                  {groups.map((g) => (
                    <SelectItem key={g.id} value={String(g.id)}>
                      {g.name}(可用 {g.alive_count}/{g.proxy_count})
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            <Button size="sm" disabled={!pool || busy} onClick={() => void switchEgress("direct")}>
              <GlobeIcon className="size-3.5" />
              切换到本地裸网
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={!canSwitchPool}
              title={targetID > 0 ? undefined : "还没有代理组,请先在「代理组」页创建"}
              onClick={() => void switchEgress("pool")}
            >
              {busy ? <Loader2Icon className="size-3.5 animate-spin" /> : <WaypointsIcon className="size-3.5" />}
              {busy ? "切换中…" : poolButtonLabel}
            </Button>
            <span className="text-muted-foreground ml-auto text-xs">
              {busy ? "正在重建 agent…" : "切换即时生效于新启动的 run"}
            </span>
          </div>
        </CardContent>
      </Card>

      {/* ── 窗口总览 ── */}
      <div className="mb-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <Card>
          <CardContent className="pt-4">
            <p className="text-muted-foreground text-xs">请求总数</p>
            <p className="text-2xl font-semibold tabular-nums">{summaryTotal}</p>
            <p className={cn("text-xs tabular-nums", summaryFailed > 0 ? "text-red-500" : "text-muted-foreground")}>
              失败 {summaryFailed}
            </p>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="pt-4">
            <p className="text-muted-foreground text-xs">成功率</p>
            <p className="text-2xl font-semibold tabular-nums">{summaryRate.toFixed(1)}%</p>
            <p className="text-muted-foreground text-xs">按窗口内全部请求</p>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="pt-4">
            <p className="text-muted-foreground text-xs">平均延迟</p>
            {/* fmtLatency 对 0 返回「—」(后端约定 0=未测/无数据)。 */}
            <p className="text-2xl font-semibold tabular-nums">{fmtLatency(summaryAvg)}</p>
            <p className="text-muted-foreground text-xs">仅统计成功请求</p>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="pt-4">
            <p className="text-muted-foreground text-xs">流量</p>
            <p className="text-2xl font-semibold tabular-nums">{fmtBytes(summaryIn + summaryOut)}</p>
            <p className="text-muted-foreground text-xs tabular-nums">
              收 {fmtBytes(summaryIn)} · 发 {fmtBytes(summaryOut)}
            </p>
          </CardContent>
        </Card>
      </div>

      {/* ── 趋势 + 统计范围 ── */}
      <Card className="mb-4">
        <CardContent className="pt-4">
          <div className="flex flex-wrap items-center gap-2">
            <span className="flex items-center gap-1.5 text-sm font-medium">
              <ActivityIcon className="text-muted-foreground size-4" />
              流量趋势
            </span>
            <Select value={String(windowMins)} onValueChange={(v) => setWindowMins(Number(v))}>
              <SelectTrigger className="w-40">
                <SelectValue placeholder="时间窗口" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  {WINDOWS.map((w) => (
                    <SelectItem key={w.mins} value={String(w.mins)}>
                      {w.label}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            {/* 组范围同时作用于总览卡片、趋势、调度排行与请求流水,所以放在最靠前的位置,
                而不是塞进某张表自己的工具栏里。 */}
            <span className="text-muted-foreground text-xs">统计范围</span>
            <Select
              value={scopeGroup}
              onValueChange={(v) => {
                setPage(1);
                setScopeGroup(v);
              }}
            >
              <SelectTrigger className="w-48">
                <SelectValue placeholder="代理组" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="all">全部代理组</SelectItem>
                  {groups.map((g) => (
                    <SelectItem key={g.id} value={String(g.id)}>
                      {g.name}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            <span className="text-muted-foreground flex items-center gap-3 text-xs">
              <span className="inline-flex items-center gap-1">
                <span className="size-2 rounded-[2px] bg-emerald-500/70" />
                成功
              </span>
              <span className="inline-flex items-center gap-1">
                <span className="size-2 rounded-[2px] bg-red-500/70" />
                失败
              </span>
            </span>
            <span className="text-muted-foreground ml-auto text-xs tabular-nums">
              {updatedAt ? `更新于 ${fmtClock(updatedAt)}` : ""}
            </span>
          </div>

          <div className="mt-3">
            {bars.length === 0 ? (
              <div className="text-muted-foreground flex h-40 items-center justify-center rounded-lg border bg-muted/10 text-xs">
                窗口内没有代理流量
              </div>
            ) : (
              <>
                <div className="flex h-40 items-stretch gap-[2px] border-b">
                  {bars.map((b) => {
                    const failedPct = peak > 0 ? (b.failed / peak) * 100 : 0;
                    const okPct = peak > 0 ? ((b.total - b.failed) / peak) * 100 : 0;
                    return (
                      <div
                        key={b.key}
                        className="flex min-w-0 flex-1 flex-col justify-end hover:opacity-80"
                        title={`${b.label} · 请求 ${b.total} · 失败 ${b.failed}`}
                      >
                        {failedPct > 0 ? (
                          <div className="w-full shrink-0 bg-red-500/70" style={{ height: `${failedPct}%` }} />
                        ) : null}
                        {okPct > 0 ? (
                          <div className="w-full shrink-0 bg-emerald-500/70" style={{ height: `${okPct}%` }} />
                        ) : null}
                      </div>
                    );
                  })}
                </div>
                {/* X 轴刻度:等距取 6 个桶标 HH:MM(柱太密时逐个标会糊成一片)。 */}
                <div className="text-muted-foreground mt-1 flex justify-between text-[10px] tabular-nums">
                  {tickIdx.map((i) => (
                    <span key={bars[i].key}>{bars[i].label}</span>
                  ))}
                </div>
              </>
            )}
          </div>
        </CardContent>
      </Card>

      {/* ── 动态调度:窗口内被选中的代理排行 ── */}
      <Card className="mb-4">
        <CardContent className="px-0 pt-4">
          <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
            <span className="flex items-center gap-1.5 text-sm font-medium">
              <ZapIcon className="text-muted-foreground size-4" />
              动态调度
            </span>
            <span className="text-muted-foreground text-xs">
              窗口内被调度最多的代理(TOP {usage.length}) ·「调度次数」即该代理被选中的次数
            </span>
          </div>
          {dashboard === null ? (
            <p className="text-muted-foreground py-12 text-center text-sm">加载中…</p>
          ) : usage.length === 0 ? (
            <p className="text-muted-foreground py-12 text-center text-sm">
              窗口内还没有代理调度记录。切到「代理池」并启动任务后,这里会列出实际被选中的代理。
            </p>
          ) : (
            <Table className="table-fixed">
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  {/* 代理列不设宽度:table-fixed 下自动吃掉余量(设 w-full 会挤掉后面的列)。 */}
                  <TableHead>代理</TableHead>
                  <TableHead className="w-20">地区</TableHead>
                  <TableHead className="w-20">来源</TableHead>
                  <TableHead className="w-20">状态</TableHead>
                  <TableHead className="w-20">延迟</TableHead>
                  <TableHead className="w-24">调度次数</TableHead>
                  <TableHead className="w-16">失败</TableHead>
                  <TableHead className="w-24">平均耗时</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {usage.map((u) => {
                  const uTotal = num(u.total);
                  const uFailed = num(u.failed);
                  const share = usageGrand > 0 ? (uTotal / usageGrand) * 100 : 0;
                  return (
                    // 后端按 (proxy_id, proxy_url) 分组,两者组合天然唯一。
                    <TableRow key={`${u.proxy_id}-${u.proxy_url}`}>
                      <TableCell className="min-w-0">
                        {/* proxy_id=0 表示这条流水没有关联到具体代理条目,原样显示 proxy_url。 */}
                        <span className="block truncate font-mono text-xs" title={u.proxy_url}>
                          {u.proxy_url || (num(u.proxy_id) > 0 ? `代理 #${u.proxy_id}` : "—")}
                        </span>
                      </TableCell>
                      {/* 地区列优先显示识别出的国家(出口 IP 解析),未识别时退回源站原始地区文本。 */}
                      <TableCell
                        className="truncate text-xs"
                        title={u.country ? `${u.country}(${u.country_code})` : u.region || "未识别"}
                      >
                        {u.country ? (
                          <span className="inline-flex items-center gap-1">
                            <span className="truncate">{u.country}</span>
                            <span className="text-muted-foreground shrink-0 font-mono">{u.country_code}</span>
                          </span>
                        ) : u.region ? (
                          <span className="text-muted-foreground truncate">{u.region}</span>
                        ) : (
                          <span className="text-muted-foreground">—</span>
                        )}
                      </TableCell>
                      <TableCell>
                        {u.source ? (
                          <Badge variant="outline" className="text-xs">
                            {sourceLabel(u.source)}
                          </Badge>
                        ) : (
                          <span className="text-muted-foreground">—</span>
                        )}
                      </TableCell>
                      <TableCell>
                        {u.status ? (
                          <Badge variant="outline" className={proxyStatusBadgeClass(u.status)}>
                            {PROXY_STATUS_LABEL[u.status] ?? u.status}
                          </Badge>
                        ) : (
                          <span className="text-muted-foreground">—</span>
                        )}
                      </TableCell>
                      <TableCell className="text-xs tabular-nums">{fmtLatency(num(u.latency_ms))}</TableCell>
                      <TableCell>
                        <span className="font-semibold tabular-nums">{uTotal}</span>
                        <span className="text-muted-foreground ml-1 text-xs tabular-nums">{share.toFixed(0)}%</span>
                      </TableCell>
                      <TableCell className={cn("tabular-nums", uFailed > 0 ? "text-red-500" : "text-muted-foreground")}>
                        {uFailed}
                      </TableCell>
                      <TableCell className="text-xs tabular-nums">{fmtLatency(num(u.avg_ms))}</TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {/* ── 请求流水 ── */}
      <Card>
        <CardContent className="px-0 pt-4">
          <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
            <div className="relative w-full sm:max-w-xs">
              <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
              <Input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && search()}
                placeholder="搜索 host / URL"
                className="pl-8"
              />
            </div>
            <Button variant="outline" size="sm" onClick={search}>
              搜索
            </Button>
            <Select
              value={onlyFailed}
              onValueChange={(v) => {
                setPage(1);
                setOnlyFailed(v);
              }}
            >
              <SelectTrigger className="w-32">
                <SelectValue placeholder="结果" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="all">全部请求</SelectItem>
                  <SelectItem value="1">只看失败</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => load(windowMins, scopeGroup, page, pageSize, onlyFailed, queryApplied, true)}
            >
              刷新
            </Button>
            <span className="text-muted-foreground ml-auto text-xs tabular-nums">
              {queryApplied ? `命中 ${reqTotal} / ` : ""}共 {reqTotal} 条
            </span>
          </div>

          {loading && requests.length === 0 ? (
            <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
          ) : requests.length === 0 ? (
            <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
              <ActivityIcon className="size-8 opacity-40" />
              <span>
                {queryApplied || onlyFailed === "1"
                  ? "没有匹配的请求流水"
                  : "窗口内还没有代理请求。把出网模式切到「代理池」并启动任务后,这里会逐条记录出网请求。"}
              </span>
            </div>
          ) : (
            <>
              <Table className="table-fixed">
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    <TableHead className="w-40">时间</TableHead>
                    <TableHead className="w-16">方法</TableHead>
                    {/* 主机列不设宽度:table-fixed 下自动吃掉余量。 */}
                    <TableHead>主机</TableHead>
                    <TableHead className="w-20">状态码</TableHead>
                    <TableHead className="w-20">延迟</TableHead>
                    <TableHead className="w-48">代理</TableHead>
                    <TableHead className="w-24">任务</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {requests.map((r) => {
                    const code = num(r.status_code);
                    const taskID = num(r.task_id);
                    // ok=false 也算失败:连不上/超时可能压根没有状态码。
                    const bad = code >= 400 || !r.ok;
                    return (
                      <TableRow key={r.id}>
                        <TableCell className="text-muted-foreground text-xs tabular-nums">{fmtTime(r.ts)}</TableCell>
                        <TableCell>
                          <Badge className="shrink-0 font-mono">{r.method || "—"}</Badge>
                        </TableCell>
                        <TableCell className="min-w-0">
                          <span className="block truncate font-mono text-xs" title={r.url || r.host}>
                            {r.host || r.url || "—"}
                          </span>
                        </TableCell>
                        <TableCell>
                          <span
                            className={cn(
                              "font-mono text-xs font-semibold tabular-nums",
                              bad ? "text-red-500" : "text-emerald-600 dark:text-emerald-400",
                            )}
                            title={r.error || undefined}
                          >
                            {code > 0 ? code : "—"}
                          </span>
                        </TableCell>
                        <TableCell className="text-xs tabular-nums">{fmtLatency(num(r.latency_ms))}</TableCell>
                        <TableCell className="min-w-0">
                          <span className="block truncate font-mono text-xs" title={r.proxy_url}>
                            {r.proxy_url || "—"}
                          </span>
                        </TableCell>
                        <TableCell className="text-xs">
                          {taskID > 0 ? (
                            <Link
                              href={`/function/tasks/detail?id=${taskID}`}
                              className="text-blue-600 hover:underline"
                            >
                              #{taskID}
                            </Link>
                          ) : (
                            "—"
                          )}
                        </TableCell>
                      </TableRow>
                    );
                  })}
                </TableBody>
              </Table>
              <TablePagination
                page={page}
                pageSize={pageSize}
                total={reqTotal}
                pageSizeOptions={[25, 50, 100, 200]}
                onPageChange={setPage}
                onPageSizeChange={(s) => {
                  setPage(1);
                  setPageSize(s);
                }}
              />
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
