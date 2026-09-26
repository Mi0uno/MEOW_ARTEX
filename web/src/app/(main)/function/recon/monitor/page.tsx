"use client";

// 资产监控(24h 持续子域测绘)前端页,契约见 MONITOR_CONTRACTS.md。
// 页面四块:
//   ① 全局配置卡:总开关 + 全局间隔(1-720h) + 状态徽标 + 最近一轮时间 + 立即全跑
//   ② 企业监控表:每企业开关/间隔覆盖(跟随全局或 N 小时)/上轮新增/跑一次
//   ③ 轮次历史:状态页签(带计数) + 分页表格 + 详情弹层(收集器明细 + 新增清单)
//   ④ 新发现 feed:近 7 天首次发现的子域名/根域名(assetsNewer)
// 轮询:config.running 或存在 status=running 的轮次时每 5s 刷新 overview+runs,
// 空闲自动停止;组件卸载清理定时器。
// 类型与 API 函数已冻结(web/src/lib/types.ts / api.ts),本页只消费不改。

import * as React from "react";

import Link from "next/link";
import { EyeIcon, PlayIcon, RefreshCwIcon, SaveIcon, ZapIcon } from "lucide-react";
import { toast } from "sonner";

import { TablePagination } from "@/components/table-pagination";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { Asset, Company, ProxyGroup, ReconMonitorOverview, ReconMonitorRun, ReconMonitorTarget } from "@/lib/types";

const RUNS_PAGE_SIZE = 20;
const FEED_PAGE_SIZE = 20;
const FEED_DAYS = 7;

// new_domains 里的资产类型 → 中文(new_domains 用的是 recon 侧命名 domain;资产库同义类型 subdomain 一并映射)。
const NEW_TYPE_LABEL: Record<string, string> = {
  domain: "子域名",
  subdomain: "子域名",
  root_domain: "根域名",
  ip: "IP",
  service: "服务",
};

// 轮次状态筛选页签(带实时计数,计数来自后端 total)。
const RUN_STATUS_TABS: { key: "all" | "running" | "done" | "failed"; label: string }[] = [
  { key: "all", label: "全部" },
  { key: "running", label: "运行中" },
  { key: "done", label: "完成" },
  { key: "failed", label: "失败" },
];

function fmtTime(iso?: string | null): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString();
}

// 列表列用的紧凑时间(MM-DD HH:mm),完整时间放单元格 title,省列宽。
function fmtTimeShort(iso?: string | null): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

// 轮次状态徽标(running/done/failed)。
function runStatusBadge(status: string): React.ReactNode {
  if (status === "running")
    return (
      <Badge variant="outline" className="border-blue-500/30 bg-blue-500/15 text-blue-600">
        运行中
      </Badge>
    );
  if (status === "failed")
    return (
      <Badge variant="outline" className="border-red-500/30 bg-red-500/15 text-red-600">
        失败
      </Badge>
    );
  if (status === "done")
    return (
      <Badge variant="outline" className="border-emerald-500/30 bg-emerald-500/15 text-emerald-600">
        完成
      </Badge>
    );
  return <Badge variant="outline">{status}</Badge>;
}

function collectorStatusText(status: string): React.ReactNode {
  if (status === "done") return <span className="text-emerald-600">完成</span>;
  if (status === "failed") return <span className="text-red-500 font-medium">失败</span>;
  if (status === "running") return <span className="text-blue-600">进行中</span>;
  if (status === "skipped") return <span className="text-muted-foreground">跳过</span>;
  return status || "—";
}

// active 期间每 intervalMs 静默执行 fn(ref 保最新闭包),active 变 false 自动停。
function usePolling(active: boolean, intervalMs: number, fn: () => void) {
  const ref = React.useRef(fn);
  ref.current = fn;
  React.useEffect(() => {
    if (!active) return;
    const timer = setInterval(() => ref.current(), intervalMs);
    return () => clearInterval(timer);
  }, [active, intervalMs]);
}

// 页签胶囊按钮(与 CVE 情报页同款):选中实底、未选描边,带可选计数。
function PillTab({
  active,
  onClick,
  count,
  children,
}: {
  active: boolean;
  onClick: () => void;
  count?: number;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={`inline-flex items-center gap-1.5 rounded-full border px-3 py-1 text-xs transition-colors ${
        active ? "border-primary bg-primary text-primary-foreground" : "border-input text-muted-foreground hover:bg-muted"
      }`}
    >
      {children}
      {count !== undefined && <span className="tabular-nums opacity-80">{count}</span>}
    </button>
  );
}

export default function ReconMonitorPage() {
  // ---- overview(全局配置 + 企业 targets) ----
  const [overview, setOverview] = React.useState<ReconMonitorOverview | null>(null);
  const config = overview?.config ?? null;
  const targets = React.useMemo(() => overview?.targets ?? [], [overview]);

  // ---- 轮次历史 ----
  const [runStatus, setRunStatus] = React.useState<"all" | "running" | "done" | "failed">("all");
  const [runs, setRuns] = React.useState<ReconMonitorRun[]>([]);
  const [runsTotal, setRunsTotal] = React.useState(0);
  const [statusCounts, setStatusCounts] = React.useState<Record<string, number>>({});
  const [runPage, setRunPage] = React.useState(1);
  const [runPageSize, setRunPageSize] = React.useState(RUNS_PAGE_SIZE);
  const [runsLoading, setRunsLoading] = React.useState(true);

  // ---- 轮次详情弹层 ----
  const [detailId, setDetailId] = React.useState<number | null>(null);
  const [detail, setDetail] = React.useState<ReconMonitorRun | null>(null);
  const [detailLoading, setDetailLoading] = React.useState(false);

  // ---- 新发现 feed ----
  const [feedType, setFeedType] = React.useState<"subdomain" | "root_domain">("subdomain");
  const [feedRows, setFeedRows] = React.useState<Asset[]>([]);
  const [feedTotal, setFeedTotal] = React.useState(0);
  const [feedPage, setFeedPage] = React.useState(1);
  const [feedPageSize, setFeedPageSize] = React.useState(FEED_PAGE_SIZE);
  const [feedLoading, setFeedLoading] = React.useState(true);
  const [companies, setCompanies] = React.useState<Company[]>([]);

  // ---- 操作 busy 态 ----
  const [configBusy, setConfigBusy] = React.useState(false);
  const [triggerAllBusy, setTriggerAllBusy] = React.useState(false);
  const [togglingId, setTogglingId] = React.useState(0);
  const [runningOneId, setRunningOneId] = React.useState(0);
  const [intervalTarget, setIntervalTarget] = React.useState<ReconMonitorTarget | null>(null);
  const [targetIntervalInput, setTargetIntervalInput] = React.useState("");
  const [targetIntervalBusy, setTargetIntervalBusy] = React.useState(false);

  // ---- 数据加载 ----
  const loadOverview = React.useCallback(() => {
    api
      .reconMonitorOverview()
      .then(setOverview)
      .catch(() => undefined); // 轮询期静默,失败保留上一次快照
  }, []);

  // 当前筛选页 + 四个状态计数(各 limit=1 只取 total),全部来自后端。
  const loadRuns = React.useCallback(() => {
    setRunsLoading(true);
    Promise.all([
      api.reconMonitorRuns({ status: runStatus, limit: runPageSize, offset: (runPage - 1) * runPageSize }),
      api.reconMonitorRuns({ status: "all", limit: 1, offset: 0 }),
      api.reconMonitorRuns({ status: "running", limit: 1, offset: 0 }),
      api.reconMonitorRuns({ status: "done", limit: 1, offset: 0 }),
      api.reconMonitorRuns({ status: "failed", limit: 1, offset: 0 }),
    ])
      .then(([pageRes, allRes, runningRes, doneRes, failedRes]) => {
        setRuns(pageRes.runs);
        setRunsTotal(pageRes.total);
        setStatusCounts({
          all: allRes.total,
          running: runningRes.total,
          done: doneRes.total,
          failed: failedRes.total,
        });
      })
      .catch(() => undefined)
      .finally(() => setRunsLoading(false));
  }, [runStatus, runPage, runPageSize]);

  const loadFeed = React.useCallback(() => {
    setFeedLoading(true);
    api
      .assetsNewer(FEED_DAYS, feedType, feedPageSize, (feedPage - 1) * feedPageSize)
      .then((r) => {
        setFeedRows(r.assets);
        setFeedTotal(r.total);
      })
      .catch(() => undefined)
      .finally(() => setFeedLoading(false));
  }, [feedType, feedPage, feedPageSize]);

  // 静默刷新已打开的详情弹层(running 轮询时跟进 collectors/新增清单)。
  const loadDetailSilent = React.useCallback((id: number) => {
    api.reconMonitorRun(id).then(setDetail).catch(() => undefined);
  }, []);

  React.useEffect(() => {
    loadOverview();
    api
      .companies()
      .then(setCompanies)
      .catch(() => undefined);
    // loadRuns / loadFeed 由各自依赖驱动的 effect 首挂加载,这里只做一次性的 overview + 企业名映射。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  React.useEffect(() => {
    loadRuns();
  }, [loadRuns]);

  React.useEffect(() => {
    loadFeed();
  }, [loadFeed]);

  // 详情弹层打开时拉取完整轮次(含 new_domains;列表接口一律为空数组)。
  React.useEffect(() => {
    if (detailId == null) {
      setDetail(null);
      return;
    }
    setDetailLoading(true);
    api
      .reconMonitorRun(detailId)
      .then(setDetail)
      .catch((e) => {
        toast.error(`加载轮次详情失败:${(e as Error).message}`);
        setDetailId(null);
      })
      .finally(() => setDetailLoading(false));
  }, [detailId]);

  // 轮询:全局在跑或列表里有 running 轮次时每 5s 刷新 overview+runs(+打开中的详情)。
  const anyRunning = !!config?.running || runs.some((r) => r.status === "running") || detail?.status === "running";
  usePolling(anyRunning, 5000, () => {
    loadOverview();
    loadRuns();
    if (detailId != null) loadDetailSilent(detailId);
  });

  // ---- 企业名映射(feed 用:companies 全量兜底,targets 补充) ----
  const companyNameById = React.useMemo(() => {
    const m = new Map<number, string>();
    for (const t of overview?.targets ?? []) m.set(t.company_id, t.company_name);
    for (const c of companies) if (!m.has(c.id)) m.set(c.id, c.name);
    return m;
  }, [overview, companies]);

  // ---- 操作 ----
  const saveEnabled = (v: boolean) => {
    setConfigBusy(true);
    api
      .saveReconMonitorConfig({ enabled: v })
      .then(() => {
        toast.success(v ? "资产监控已开启" : "资产监控已关闭");
        loadOverview();
      })
      .catch((e) => toast.error(`保存失败:${(e as Error).message}`))
      .finally(() => setConfigBusy(false));
  };

  const saveInterval = () => {
    const n = Number(globalIntervalInput);
    if (!Number.isInteger(n) || n < 1 || n > 720) {
      toast.error("全局间隔需为 1-720 的整数小时");
      return;
    }
    setConfigBusy(true);
    api
      .saveReconMonitorConfig({ interval_hours: n })
      .then(() => {
        toast.success(`全局间隔已设为 ${n} 小时`);
        loadOverview();
      })
      .catch((e) => toast.error(`保存失败:${(e as Error).message}`))
      .finally(() => setConfigBusy(false));
  };

  // ---- 监控独立出网(空=跟随全局/direct/pool;pool 可指定组) ----
  const [proxyGroups, setProxyGroups] = React.useState<ProxyGroup[]>([]);
  React.useEffect(() => {
    api
      .proxyGroups({ limit: 200 })
      .then((r) => setProxyGroups(r.groups))
      .catch(() => setProxyGroups([]));
  }, []);

  const saveProxyMode = (mode: string) => {
    setConfigBusy(true);
    api
      .saveReconMonitorConfig({ proxy_mode: mode })
      .then(() => {
        toast.success(
          mode === "direct" ? "监控出网:直连" : mode === "pool" ? "监控出网:代理池" : "监控出网:跟随全局",
        );
        loadOverview();
      })
      .catch((e) => toast.error(`保存失败:${(e as Error).message}`))
      .finally(() => setConfigBusy(false));
  };

  const saveProxyGroup = (id: number) => {
    setConfigBusy(true);
    api
      .saveReconMonitorConfig({ proxy_group_id: id })
      .then(() => {
        toast.success(id > 0 ? "监控代理池已指定代理组" : "监控代理池已跟随全局组选择");
        loadOverview();
      })
      .catch((e) => toast.error(`保存失败:${(e as Error).message}`))
      .finally(() => setConfigBusy(false));
  };

  const triggerAll = () => {
    setTriggerAllBusy(true);
    api
      .triggerReconMonitor()
      .then((r) => {
        if (r.started) {
          toast.success("已触发监控轮次,所有到期企业将错峰串行执行");
          loadOverview();
          loadRuns();
        } else {
          toast("已有轮次在跑,请稍候");
        }
      })
      .catch((e) => toast.error(`触发失败:${(e as Error).message}`))
      .finally(() => setTriggerAllBusy(false));
  };

  const triggerOne = (t: ReconMonitorTarget) => {
    setRunningOneId(t.company_id);
    api
      .triggerReconMonitor(t.company_id)
      .then((r) => {
        if (r.started) toast.success(`已触发 ${t.company_name} 的监控轮次`);
        else toast("已有轮次在跑,请稍候");
        loadOverview();
        loadRuns();
      })
      .catch((e) => toast.error(`触发失败:${(e as Error).message}`))
      .finally(() => setRunningOneId(0));
  };

  const toggleTarget = (t: ReconMonitorTarget, v: boolean) => {
    setTogglingId(t.company_id);
    api
      .saveReconMonitorTarget(t.company_id, { enabled: v })
      .then(() => {
        toast.success(v ? `已开启 ${t.company_name} 的监控` : `已停用 ${t.company_name} 的监控`);
        loadOverview();
      })
      .catch((e) => toast.error(`保存失败:${(e as Error).message}`))
      .finally(() => setTogglingId(0));
  };

  const openIntervalDialog = (t: ReconMonitorTarget) => {
    setIntervalTarget(t);
    setTargetIntervalInput(String(t.interval_hours ?? config?.interval_hours ?? 24));
  };

  const saveTargetInterval = (clear: boolean) => {
    if (!intervalTarget) return;
    let n = 0;
    if (!clear) {
      n = Number(targetIntervalInput);
      if (!Number.isInteger(n) || n < 1 || n > 720) {
        toast.error("间隔需为 1-720 的整数小时");
        return;
      }
    }
    setTargetIntervalBusy(true);
    api
      .saveReconMonitorTarget(intervalTarget.company_id, { interval_hours: clear ? null : n })
      .then(() => {
        toast.success(clear ? `已清除覆盖,${intervalTarget.company_name} 跟随全局间隔` : `已设为 ${n} 小时`);
        setIntervalTarget(null);
        loadOverview();
      })
      .catch((e) => toast.error(`保存失败:${(e as Error).message}`))
      .finally(() => setTargetIntervalBusy(false));
  };

  // ---- 派生展示 ----
  const watchingCount = targets.filter((t) => t.watched).length;
  const [globalIntervalInput, setGlobalIntervalInput] = React.useState("");
  React.useEffect(() => {
    // 首次拿到配置后回填输入框;之后不覆盖用户正在编辑的值。
    setGlobalIntervalInput((prev) => (prev === "" ? String(config?.interval_hours ?? 24) : prev));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [config?.interval_hours]);

  const configStatusBadge = !config ? (
    <span className="text-muted-foreground text-xs">加载中…</span>
  ) : config.running ? (
    <Badge variant="outline" className="border-blue-500/30 bg-blue-500/15 text-blue-600">
      运行中
    </Badge>
  ) : config.enabled ? (
    <Badge variant="outline" className="border-emerald-500/30 bg-emerald-500/15 text-emerald-600">
      空闲
    </Badge>
  ) : (
    <Badge variant="secondary">已关闭</Badge>
  );

  const refreshAll = () => {
    loadOverview();
    loadRuns();
    loadFeed();
    if (detailId != null) loadDetailSilent(detailId);
  };

  return (
    <div className="p-4 md:p-6">
      {/* 顶部:标题 + 刷新 */}
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div>
          <h1 className="font-semibold text-xl">资产监控</h1>
          <p className="text-muted-foreground text-sm">
            开启后,资产范围含根域名的企业按固定间隔自动测绘子域名(被动收集 + 探活),新发现直写资产库;在此查看节奏、轮次与新发现
          </p>
        </div>
        <Button variant="outline" size="sm" onClick={refreshAll}>
          <RefreshCwIcon className="size-4" />
          刷新
        </Button>
      </div>

      <div className="space-y-4">
        {/* ===== ① 全局配置卡 ===== */}
        <Card>
          <CardContent className="flex flex-wrap items-center gap-x-6 gap-y-3 pt-4">
            <div className="flex items-center gap-2">
              <Switch
                id="monitor-enabled"
                size="sm"
                checked={config?.enabled ?? false}
                disabled={configBusy || !config}
                onCheckedChange={(v) => saveEnabled(v)}
              />
              <Label htmlFor="monitor-enabled" className="text-sm font-medium">
                监控总开关
              </Label>
            </div>
            <div className="flex items-center gap-2">
              <Label htmlFor="monitor-interval" className="text-sm">
                全局间隔
              </Label>
              <Input
                id="monitor-interval"
                type="number"
                min={1}
                max={720}
                value={globalIntervalInput}
                onChange={(e) => setGlobalIntervalInput(e.target.value)}
                disabled={configBusy || !config}
                className="h-8 w-24"
              />
              <span className="text-muted-foreground text-xs">小时(1-720)</span>
              <Button variant="outline" size="sm" disabled={configBusy || !config} onClick={saveInterval}>
                <SaveIcon className="size-3.5" />
                保存间隔
              </Button>
            </div>
            <div className="flex items-center gap-2">
              <Label htmlFor="monitor-egress" className="text-sm">
                出网
              </Label>
              <Select
                value={config?.proxy_mode || "inherit"}
                onValueChange={(v) => saveProxyMode(v === "inherit" ? "" : v)}
                disabled={configBusy || !config}
              >
                <SelectTrigger id="monitor-egress" className="h-8 w-28">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="inherit">跟随全局</SelectItem>
                  <SelectItem value="direct">直连</SelectItem>
                  <SelectItem value="pool">代理池</SelectItem>
                </SelectContent>
              </Select>
              {config?.proxy_mode === "pool" && (
                <Select
                  value={String(config?.proxy_group_id ?? 0)}
                  onValueChange={(v) => saveProxyGroup(Number(v))}
                  disabled={configBusy || !config}
                >
                  <SelectTrigger className="h-8 w-40">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="0">默认组(自动)</SelectItem>
                    {proxyGroups.map((g) => (
                      <SelectItem key={g.id} value={String(g.id)}>
                        {g.name}
                        {g.enabled ? "" : "(已停用)"}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            </div>
            <div className="flex flex-wrap items-center gap-2 text-sm">
              {configStatusBadge}
              <span className="text-muted-foreground text-xs">
                最近一轮 <span className="tabular-nums">{fmtTime(config?.last_round_at)}</span>
              </span>
              <span className="text-muted-foreground text-xs">
                企业 <span className="tabular-nums">{targets.length}</span> 家纳入 · 监控中{" "}
                <span className="tabular-nums">{watchingCount}</span> 家
              </span>
            </div>
            <div className="ml-auto flex items-center gap-2">
              <Button size="sm" disabled={triggerAllBusy || !config} onClick={() => void triggerAll()}>
                <ZapIcon className="size-3.5" />
                立即全跑
              </Button>
            </div>
          </CardContent>
        </Card>

        {/* ===== ② 企业监控表 ===== */}
        <Card>
          <CardContent className="px-0 pt-4">
            <p className="text-muted-foreground px-4 pb-2 text-xs lg:px-6">
              资产范围含根域名(kind=domain)的企业自动纳入;可单独停开或覆盖间隔
            </p>
            <div className="overflow-hidden">
              <Table className="table-fixed text-xs">
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    <TableHead>企业</TableHead>
                    <TableHead className="w-16">根域名数</TableHead>
                    <TableHead className="w-24">监控</TableHead>
                    <TableHead className="w-24">间隔覆盖</TableHead>
                    <TableHead className="w-28">上次运行</TableHead>
                    <TableHead className="w-28">下次运行</TableHead>
                    <TableHead className="w-16">上轮新增</TableHead>
                    <TableHead className="w-20">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {targets.length === 0 ? (
                    <TableRow>
                      <TableCell colSpan={8} className="text-muted-foreground py-10 text-center">
                        {overview
                          ? "还没有企业纳入监控范围:给企业的资产范围添加根域名规则后自动纳入"
                          : "加载中…"}
                      </TableCell>
                    </TableRow>
                  ) : (
                    targets.map((t) => {
                      const domains = t.domains ?? [];
                      const lastNew = t.last_run?.new_count ?? 0;
                      return (
                        <TableRow key={t.company_id}>
                          <TableCell className="min-w-0">
                            <span
                              className={`block w-full truncate font-medium ${t.watched ? "" : "text-muted-foreground"}`}
                              title={t.company_name}
                            >
                              {t.company_name}
                            </span>
                          </TableCell>
                          <TableCell className="tabular-nums" title={domains.join("、") || "无域名范围"}>
                            {t.domain_count > 0 ? t.domain_count : <span className="text-muted-foreground">0</span>}
                          </TableCell>
                          <TableCell>
                            <div className="flex flex-col items-start gap-0.5">
                              <Switch
                                size="sm"
                                checked={t.enabled}
                                disabled={t.domain_count === 0 || togglingId === t.company_id}
                                onCheckedChange={(v) => toggleTarget(t, v)}
                                aria-label={`${t.enabled ? "停用" : "开启"} ${t.company_name} 的监控`}
                              />
                              {/* watched=false 时灰显原因:全局关或无域名范围;行开关自身关闭则开关已足够表意 */}
                              {!t.watched && t.enabled && (
                                <span className="text-muted-foreground text-[10px] leading-none">
                                  {t.domain_count === 0 ? "无域名范围" : "全局未开启"}
                                </span>
                              )}
                            </div>
                          </TableCell>
                          <TableCell>
                            <Button
                              variant="ghost"
                              size="sm"
                              className="h-7 px-2 text-xs"
                              onClick={() => openIntervalDialog(t)}
                              title="设置该企业的监控间隔覆盖"
                            >
                              {t.interval_hours == null ? (
                                <span className="text-muted-foreground">跟随全局</span>
                              ) : (
                                <span className="tabular-nums">{t.interval_hours} 小时</span>
                              )}
                            </Button>
                          </TableCell>
                          <TableCell className="text-muted-foreground truncate" title={fmtTime(t.last_run_at)}>
                            {fmtTimeShort(t.last_run_at)}
                          </TableCell>
                          <TableCell className="text-muted-foreground truncate" title={fmtTime(t.next_run_at)}>
                            {t.watched ? fmtTimeShort(t.next_run_at) : "—"}
                          </TableCell>
                          <TableCell>
                            {t.last_run == null ? (
                              <span className="text-muted-foreground">—</span>
                            ) : lastNew > 0 ? (
                              <Badge
                                variant="outline"
                                className="border-emerald-500/30 bg-emerald-500/15 px-1.5 text-emerald-600"
                                title={`上轮状态:${t.last_run.status}`}
                              >
                                +{lastNew}
                              </Badge>
                            ) : (
                              <span className="text-muted-foreground tabular-nums" title={`上轮状态:${t.last_run.status}`}>
                                0
                              </span>
                            )}
                          </TableCell>
                          <TableCell>
                            <Button
                              variant="ghost"
                              size="sm"
                              className="h-7 px-2 text-xs"
                              disabled={runningOneId === t.company_id}
                              onClick={() => void triggerOne(t)}
                              title="立即对该企业跑一轮监控(绕过全局开关)"
                            >
                              <PlayIcon className="size-3" />
                              跑一次
                            </Button>
                          </TableCell>
                        </TableRow>
                      );
                    })
                  )}
                </TableBody>
              </Table>
            </div>
          </CardContent>
        </Card>

        {/* ===== ③ 轮次历史 ===== */}
        <Card>
          <CardContent className="px-0 pt-4">
            <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
              {RUN_STATUS_TABS.map((s) => (
                <PillTab
                  key={s.key}
                  active={runStatus === s.key}
                  count={s.key === "all" ? (statusCounts.all ?? runsTotal) : statusCounts[s.key] ?? 0}
                  onClick={() => {
                    setRunStatus(s.key);
                    setRunPage(1);
                  }}
                >
                  {s.label}
                </PillTab>
              ))}
              {anyRunning && (
                <span className="text-muted-foreground text-xs">有轮次在跑 · 每 5 秒自动刷新</span>
              )}
              <span className="text-muted-foreground ml-auto text-xs tabular-nums">共 {runsTotal} 条</span>
            </div>

            {runsLoading && runs.length === 0 ? (
              <p className="text-muted-foreground py-12 text-center text-sm">加载中…</p>
            ) : runs.length === 0 ? (
              <p className="text-muted-foreground py-12 text-center text-sm">
                {runStatus === "all"
                  ? "还没有监控轮次:开启总开关等待调度,或点「立即全跑 / 跑一次」试跑"
                  : `没有${RUN_STATUS_TABS.find((s) => s.key === runStatus)?.label ?? ""}的轮次`}
              </p>
            ) : (
              <>
                <Table className="table-fixed text-xs">
                  <TableHeader>
                    <TableRow className="hover:bg-transparent">
                      <TableHead className="w-28">时间</TableHead>
                      <TableHead>企业</TableHead>
                      <TableHead className="w-16">触发</TableHead>
                      <TableHead className="w-16">状态</TableHead>
                      <TableHead className="w-20">收集器</TableHead>
                      <TableHead className="w-16">产出</TableHead>
                      <TableHead className="w-16">新增</TableHead>
                      <TableHead className="w-20">探活</TableHead>
                      <TableHead className="w-16">详情</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {runs.map((r) => {
                      const cs = r.collectors ?? [];
                      const okN = cs.filter((c) => c.status === "done").length;
                      return (
                        <TableRow key={r.id}>
                          <TableCell className="text-muted-foreground truncate" title={fmtTime(r.started_at)}>
                            {fmtTimeShort(r.started_at)}
                          </TableCell>
                          <TableCell className="min-w-0">
                            <span className="block w-full truncate font-medium" title={r.company_name}>
                              {r.company_name || "—"}
                            </span>
                          </TableCell>
                          <TableCell>
                            <Badge variant="outline" className="px-1.5 text-[10px]">
                              {r.trigger === "manual" ? "手动" : "定时"}
                            </Badge>
                          </TableCell>
                          <TableCell>{runStatusBadge(r.status)}</TableCell>
                          <TableCell className="tabular-nums" title={`成功收集器 ${okN} / 共 ${cs.length}`}>
                            {okN}/{cs.length}
                          </TableCell>
                          <TableCell className="tabular-nums">{r.total_found}</TableCell>
                          <TableCell>
                            {r.new_count > 0 ? (
                              <span className="text-emerald-600 font-medium tabular-nums">+{r.new_count}</span>
                            ) : (
                              <span className="text-muted-foreground tabular-nums">0</span>
                            )}
                          </TableCell>
                          <TableCell
                            className="tabular-nums"
                            title={`探活成功 ${r.probe_ok} / 失败 ${r.probe_failed}`}
                          >
                            <span className="text-emerald-600">{r.probe_ok}</span>
                            <span className="text-muted-foreground">/</span>
                            <span className={r.probe_failed > 0 ? "text-red-500" : "text-muted-foreground"}>
                              {r.probe_failed}
                            </span>
                          </TableCell>
                          <TableCell>
                            <Button
                              variant="ghost"
                              size="sm"
                              className="h-7 px-2"
                              onClick={() => setDetailId(r.id)}
                              title="查看轮次详情(收集器明细 + 新增清单)"
                            >
                              <EyeIcon className="size-3.5" />
                            </Button>
                          </TableCell>
                        </TableRow>
                      );
                    })}
                  </TableBody>
                </Table>
                <TablePagination
                  page={runPage}
                  pageSize={runPageSize}
                  total={runsTotal}
                  onPageChange={setRunPage}
                  onPageSizeChange={(s) => {
                    setRunPage(1);
                    setRunPageSize(s);
                  }}
                />
              </>
            )}
          </CardContent>
        </Card>

        {/* ===== ④ 新发现 feed ===== */}
        <Card>
          <CardContent className="px-0 pt-4">
            <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
              <PillTab
                active={feedType === "subdomain"}
                count={feedType === "subdomain" ? feedTotal : undefined}
                onClick={() => {
                  setFeedType("subdomain");
                  setFeedPage(1);
                }}
              >
                子域名
              </PillTab>
              <PillTab
                active={feedType === "root_domain"}
                count={feedType === "root_domain" ? feedTotal : undefined}
                onClick={() => {
                  setFeedType("root_domain");
                  setFeedPage(1);
                }}
              >
                根域名
              </PillTab>
              <span className="text-muted-foreground text-xs">近 {FEED_DAYS} 天首次发现(created_at),分页见下方</span>
              <span className="text-muted-foreground ml-auto text-xs tabular-nums">共 {feedTotal} 条</span>
            </div>

            {feedLoading && feedRows.length === 0 ? (
              <p className="text-muted-foreground py-12 text-center text-sm">加载中…</p>
            ) : feedRows.length === 0 ? (
              <p className="text-muted-foreground py-12 text-center text-sm">
                近 {FEED_DAYS} 天没有首次发现的{feedType === "subdomain" ? "子域名" : "根域名"}
              </p>
            ) : (
              <>
                <Table className="table-fixed text-xs">
                  <TableHeader>
                    <TableRow className="hover:bg-transparent">
                      <TableHead>值</TableHead>
                      <TableHead className="w-36">归属企业</TableHead>
                      <TableHead className="w-32">首次发现</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {feedRows.map((a) => {
                      const name = a.company_id != null ? companyNameById.get(a.company_id) : undefined;
                      const value =
                        feedType === "subdomain"
                          ? a.domain
                          : a.domain || a.root_domain;
                      return (
                        <TableRow key={a.id}>
                          <TableCell className="min-w-0">
                            <span className="block w-full truncate font-mono" title={value ?? undefined}>
                              {value || "—"}
                            </span>
                          </TableCell>
                          <TableCell>
                            {a.company_id != null ? (
                              <Link
                                href={`/function/assets/detail?id=${a.company_id}`}
                                className="truncate hover:underline"
                                title={`查看企业详情:${name ?? `#${a.company_id}`}`}
                              >
                                {name || `企业 #${a.company_id}`}
                              </Link>
                            ) : (
                              <span className="text-muted-foreground">未归属</span>
                            )}
                          </TableCell>
                          <TableCell className="text-muted-foreground truncate" title={fmtTime(a.created_at)}>
                            {fmtTimeShort(a.created_at)}
                          </TableCell>
                        </TableRow>
                      );
                    })}
                  </TableBody>
                </Table>
                <TablePagination
                  page={feedPage}
                  pageSize={feedPageSize}
                  total={feedTotal}
                  onPageChange={setFeedPage}
                  onPageSizeChange={(s) => {
                    setFeedPage(1);
                    setFeedPageSize(s);
                  }}
                />
              </>
            )}
          </CardContent>
        </Card>
      </div>

      {/* ===== 间隔覆盖编辑弹层 ===== */}
      <Dialog
        open={intervalTarget != null}
        onOpenChange={(o) => {
          if (!o) setIntervalTarget(null);
        }}
      >
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>间隔覆盖 · {intervalTarget?.company_name}</DialogTitle>
            <DialogDescription>
              全局间隔为 {config?.interval_hours ?? 24} 小时;为该企业设置独立间隔,或清除覆盖跟随全局。
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-1.5">
            <Label htmlFor="target-interval">覆盖间隔(小时,1-720)</Label>
            <Input
              id="target-interval"
              type="number"
              min={1}
              max={720}
              value={targetIntervalInput}
              onChange={(e) => setTargetIntervalInput(e.target.value)}
            />
          </div>
          <DialogFooter className="gap-2 sm:justify-between">
            <Button
              variant="outline"
              disabled={targetIntervalBusy || intervalTarget?.interval_hours == null}
              onClick={() => saveTargetInterval(true)}
            >
              清除覆盖(跟随全局)
            </Button>
            <div className="flex items-center gap-2">
              <Button variant="ghost" onClick={() => setIntervalTarget(null)} disabled={targetIntervalBusy}>
                取消
              </Button>
              <Button disabled={targetIntervalBusy} onClick={() => saveTargetInterval(false)}>
                {targetIntervalBusy ? "保存中…" : "保存"}
              </Button>
            </div>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* ===== 轮次详情弹层 ===== */}
      <Dialog
        open={detailId != null}
        onOpenChange={(o) => {
          if (!o) {
            setDetailId(null);
            setDetail(null);
          }
        }}
      >
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>
              轮次详情 #{detailId}
              {detail?.company_name ? ` · ${detail.company_name}` : ""}
            </DialogTitle>
            <DialogDescription>监控轮执行明细:各收集器产出与新增资产清单</DialogDescription>
          </DialogHeader>
          {detailLoading && !detail ? (
            <p className="text-muted-foreground py-8 text-center text-sm">加载中…</p>
          ) : !detail ? null : (
            <div className="space-y-4">
              {/* 概要行 */}
              <div className="flex flex-wrap items-center gap-x-4 gap-y-1.5 text-xs">
                {runStatusBadge(detail.status)}
                <span className="text-muted-foreground">
                  触发 <span className="text-foreground">{detail.trigger === "manual" ? "手动" : "定时"}</span>
                </span>
                <span className="text-muted-foreground">
                  开始 <span className="text-foreground tabular-nums">{fmtTime(detail.started_at)}</span>
                </span>
                <span className="text-muted-foreground">
                  结束 <span className="text-foreground tabular-nums">{fmtTime(detail.finished_at)}</span>
                </span>
                <span className="text-muted-foreground">
                  产出 <span className="text-foreground tabular-nums">{detail.total_found}</span>
                </span>
                <span className="text-muted-foreground">
                  新增 <span className="text-emerald-600 tabular-nums">{detail.new_count}</span>
                </span>
                <span className="text-muted-foreground">
                  探活{" "}
                  <span className="tabular-nums">
                    <span className="text-emerald-600">{detail.probe_ok}</span>/
                    <span className={detail.probe_failed > 0 ? "text-red-500" : ""}>{detail.probe_failed}</span>
                  </span>
                </span>
              </div>

              {detail.error && (
                <div className="rounded-md border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-600">
                  {detail.error}
                </div>
              )}

              {/* 收集器明细 */}
              <div className="space-y-1.5">
                <p className="text-sm font-medium">收集器</p>
                <div className="overflow-hidden rounded-md border">
                  <Table className="table-fixed text-xs">
                    <TableHeader>
                      <TableRow className="hover:bg-transparent">
                        <TableHead className="w-28">Key</TableHead>
                        <TableHead className="w-20">状态</TableHead>
                        <TableHead className="w-16">产出</TableHead>
                        <TableHead>错误</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {(detail.collectors ?? []).length === 0 ? (
                        <TableRow>
                          <TableCell colSpan={4} className="text-muted-foreground py-6 text-center">
                            无收集器记录
                          </TableCell>
                        </TableRow>
                      ) : (
                        (detail.collectors ?? []).map((c, i) => (
                          <TableRow key={`${c.key}-${i}`}>
                            <TableCell className="truncate font-mono" title={c.key}>
                              {c.key}
                            </TableCell>
                            <TableCell>{collectorStatusText(c.status)}</TableCell>
                            <TableCell className="tabular-nums">{c.found}</TableCell>
                            <TableCell className="text-muted-foreground truncate" title={c.error || undefined}>
                              {c.error || "—"}
                            </TableCell>
                          </TableRow>
                        ))
                      )}
                    </TableBody>
                  </Table>
                </div>
              </div>

              {/* 新增清单(仅详情接口有;列表恒为空数组) */}
              <div className="space-y-1.5">
                <div className="flex flex-wrap items-center gap-2">
                  <p className="text-sm font-medium">新增清单</p>
                  <span className="text-muted-foreground text-xs tabular-nums">
                    共 {(detail.new_domains ?? []).length} 条
                    {detail.new_truncated ? "(超过 300 条已截断)" : ""}
                  </span>
                </div>
                {detail.status === "running" && (detail.new_domains ?? []).length === 0 ? (
                  <p className="text-muted-foreground text-xs">轮次进行中,新增清单将在完成后生成</p>
                ) : (detail.new_domains ?? []).length === 0 ? (
                  <p className="text-muted-foreground text-xs">本轮没有新发现</p>
                ) : (
                  <div className="flex max-h-56 flex-wrap gap-1 overflow-y-auto rounded-md border p-2">
                    {(detail.new_domains ?? []).map((d, i) => (
                      <Badge key={`${d.type}-${d.value}-${i}`} variant="outline" className="max-w-full font-mono text-[11px]">
                        <span className="mr-1 font-sans text-muted-foreground">{NEW_TYPE_LABEL[d.type] ?? d.type}</span>
                        <span className="truncate" title={d.value}>
                          {d.value}
                        </span>
                      </Badge>
                    ))}
                  </div>
                )}
              </div>
            </div>
          )}
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => {
                setDetailId(null);
                setDetail(null);
              }}
            >
              关闭
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
