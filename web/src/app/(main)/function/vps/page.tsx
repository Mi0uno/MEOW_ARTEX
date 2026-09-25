"use client";

// VPS 中心 · 仪表盘 —— 机器池与远程执行的总览页。
//
// 一条请求(api.vpsDashboard)取齐:状态汇总、执行量时间序列、当前占用机器、最近流水。
// 时间窗口切换(24h/7d)只影响执行量曲线;汇总卡是即时状态,与窗口无关。
// 后端未就绪(404/500)时不白屏:toast 报错 + 全部数字落零值 + 各区块空态。

import * as React from "react";

import Link from "next/link";

import {
  ActivityIcon,
  ArrowRightIcon,
  ClockIcon,
  CpuIcon,
  EyeIcon,
  ListTodoIcon,
  ServerIcon,
  WifiOffIcon,
  WrenchIcon,
} from "lucide-react";
import { toast } from "sonner";
import { Area, AreaChart, CartesianGrid, XAxis, YAxis } from "recharts";

import {
  fmtDuration,
  fmtTime,
  num,
  vpsSourceBadgeClass,
  vpsSourceLabel,
  vpsStatusBadgeClass,
  vpsStatusLabel,
  VpsExecOutputDialog,
} from "@/components/vps-shared";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { type ChartConfig, ChartContainer, ChartTooltip, ChartTooltipContent } from "@/components/ui/chart";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { VpsDashboard, VpsExecution, VpsMachine } from "@/lib/types";
import { cn } from "@/lib/utils";

// window_h 与后端对齐:24 = 最近 24 小时(按小时桶),168 = 最近 7 天。
const WINDOWS = [
  { hours: 24, label: "最近 24 小时" },
  { hours: 168, label: "最近 7 天" },
];

// 空库/接口失败时的零值兜底,保证首帧与失败态都不白屏。
const EMPTY_SUMMARY = {
  total: 0,
  online: 0,
  offline: 0,
  busy: 0,
  maintenance: 0,
  executions_today: 0,
  active_tasks: 0,
};

const execChartConfig = {
  count: { label: "执行次数", color: "hsl(217 91% 60%)" },
} satisfies ChartConfig;

// 曲线 X 轴刻度:24h 桶标 HH:00,7d 桶标「MM-DD HH:00」;解析失败原样截断。
function fmtBucket(ts: string, hours: number): string {
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return (ts || "").slice(5, 16);
  const hm = `${String(d.getHours()).padStart(2, "0")}:00`;
  if (hours <= 24) return hm;
  return `${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")} ${hm}`;
}

// 汇总卡:数字 + 说明,离线卡在 offline>0 时标红提示。
function StatCard({
  label,
  value,
  sub,
  tone,
}: {
  label: string;
  value: number;
  sub?: string;
  tone?: "ok" | "bad" | "warn" | "info";
}) {
  return (
    <Card>
      <CardContent className="pt-4">
        <p className="text-muted-foreground text-xs">{label}</p>
        <p
          className={cn(
            "text-2xl font-semibold tabular-nums",
            tone === "ok" && value > 0 && "text-emerald-600 dark:text-emerald-400",
            tone === "bad" && value > 0 && "text-red-600 dark:text-red-400",
            tone === "warn" && value > 0 && "text-amber-600 dark:text-amber-400",
            tone === "info" && value > 0 && "text-blue-600 dark:text-blue-400",
          )}
        >
          {value}
        </p>
        {sub ? <p className="text-muted-foreground text-xs">{sub}</p> : null}
      </CardContent>
    </Card>
  );
}

export default function VpsDashboardPage() {
  const [windowHours, setWindowHours] = React.useState(24);
  const [dashboard, setDashboard] = React.useState<VpsDashboard | null>(null);
  const [updatedAt, setUpdatedAt] = React.useState("");
  const [loaded, setLoaded] = React.useState(false);
  // 「查看输出」目标:非 null 时弹出输出对话框。
  const [viewing, setViewing] = React.useState<VpsExecution | null>(null);

  const load = React.useCallback((hours: number) => {
    api
      .vpsDashboard(hours)
      .then((d) => {
        setDashboard(d);
        setUpdatedAt(new Date().toISOString());
      })
      .catch((e) => {
        // 保留上一份快照;只有从未成功过才提示(避免轮询刷屏)。
        if (!loaded) toast.error(`加载 VPS 仪表盘失败:${(e as Error).message}`);
      })
      .finally(() => setLoaded(true));
  }, [loaded]);

  React.useEffect(() => {
    load(windowHours);
    const t = setInterval(() => load(windowHours), 10_000);
    return () => clearInterval(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [windowHours]);

  const summary = dashboard?.summary ?? EMPTY_SUMMARY;
  const series = dashboard?.series ?? [];
  const busyMachines: VpsMachine[] = dashboard?.busy_machines ?? [];
  const recent: VpsExecution[] = dashboard?.recent ?? [];
  const offlineCount = num(summary.offline);

  // 占用机器的 id→名称映射:最近流水里只有 machine_id,能对上就显示名字。
  const machineNames = React.useMemo(() => {
    const m = new Map<number, string>();
    for (const b of busyMachines) m.set(b.id, b.name);
    return m;
  }, [busyMachines]);

  const chartData = React.useMemo(
    () => series.map((s) => ({ label: fmtBucket(s.ts, windowHours), count: num(s.count) })),
    [series, windowHours],
  );
  const execTotal = chartData.reduce((a, b) => a + b.count, 0);

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div>
          <h1 className="font-semibold text-xl">VPS 中心 · 仪表盘</h1>
          <p className="text-muted-foreground text-sm">
            机器池状态、agent 远程执行量与占用情况的总览。任务的 Bash 命令在指派的 Linux 机器上执行,流水在这里可查。
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button asChild variant="outline" size="sm">
            <Link href="/function/vps/list">
              <ServerIcon className="size-4" />
              机器池
            </Link>
          </Button>
          <Button asChild variant="outline" size="sm">
            <Link href="/function/vps/toolsets">
              <WrenchIcon className="size-4" />
              工具集
            </Link>
          </Button>
        </div>
      </div>

      {/* ── 机器掉线告警 ── */}
      {offlineCount > 0 ? (
        <Alert className="mb-4 border-red-500/30 bg-red-500/5">
          <WifiOffIcon className="text-red-600 dark:text-red-400" />
          <AlertTitle>{offlineCount} 台机器处于离线状态</AlertTitle>
          <AlertDescription>
            离线机器上的任务命令会执行失败。请到
            <Link href="/function/vps/list?status=offline" className="mx-0.5 font-medium underline underline-offset-2">
              机器池
            </Link>
            查看详情并检查网络或凭据;后台测活循环每分钟会自动重试。
          </AlertDescription>
        </Alert>
      ) : null}

      {/* ── 状态统计卡 ── */}
      <div className="mb-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-4 xl:grid-cols-7">
        <StatCard label="机器总数" value={num(summary.total)} sub="机器池全部机器" />
        <StatCard label="在线空闲" value={num(summary.online)} tone="ok" sub="可被自动指派" />
        <StatCard label="离线" value={offlineCount} tone="bad" sub="测活失败或失联" />
        <StatCard label="占用中" value={num(summary.busy)} tone="info" sub="正被任务使用" />
        <StatCard label="维护" value={num(summary.maintenance)} tone="warn" sub="不参与自动指派" />
        <StatCard label="今日执行" value={num(summary.executions_today)} sub="命令执行流水条数" />
        <StatCard label="活跃任务" value={num(summary.active_tasks)} sub="有机器在跑的任务" />
      </div>

      {/* ── 执行量趋势 ── */}
      <Card className="mb-4">
        <CardContent className="pt-4">
          <div className="flex flex-wrap items-center gap-2">
            <span className="flex items-center gap-1.5 text-sm font-medium">
              <ActivityIcon className="text-muted-foreground size-4" />
              命令执行量
            </span>
            <Select value={String(windowHours)} onValueChange={(v) => setWindowHours(Number(v))}>
              <SelectTrigger className="w-40">
                <SelectValue placeholder="时间窗口" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  {WINDOWS.map((w) => (
                    <SelectItem key={w.hours} value={String(w.hours)}>
                      {w.label}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            <span className="text-muted-foreground text-xs tabular-nums">窗口内共 {execTotal} 次执行(按小时分桶)</span>
            <span className="text-muted-foreground ml-auto text-xs tabular-nums">
              {updatedAt ? `更新于 ${fmtTime(updatedAt)}` : ""}
            </span>
          </div>
          <div className="mt-3">
            {chartData.length === 0 ? (
              <div className="text-muted-foreground flex h-48 items-center justify-center rounded-lg border bg-muted/10 text-xs">
                {loaded ? "窗口内没有执行记录" : "加载中…"}
              </div>
            ) : (
              <ChartContainer config={execChartConfig} className="h-48 w-full">
                <AreaChart data={chartData} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
                  <CartesianGrid vertical={false} />
                  <XAxis
                    dataKey="label"
                    tickLine={false}
                    axisLine={false}
                    tick={{ fontSize: 10 }}
                    tickMargin={6}
                    interval="preserveStartEnd"
                  />
                  <YAxis
                    tickLine={false}
                    axisLine={false}
                    tick={{ fontSize: 10 }}
                    width={32}
                    allowDecimals={false}
                  />
                  <ChartTooltip cursor={false} content={<ChartTooltipContent indicator="line" />} />
                  <Area
                    dataKey="count"
                    type="monotone"
                    fill="var(--color-count)"
                    fillOpacity={0.25}
                    stroke="var(--color-count)"
                    strokeWidth={2}
                  />
                </AreaChart>
              </ChartContainer>
            )}
          </div>
        </CardContent>
      </Card>

      {/* ── 当前占用机器 ── */}
      <Card className="mb-4">
        <CardContent className="px-0 pt-4">
          <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
            <span className="flex items-center gap-1.5 text-sm font-medium">
              <CpuIcon className="text-muted-foreground size-4" />
              当前占用机器
            </span>
            <span className="text-muted-foreground text-xs">一机一任务:被指派的机器在任务结束前不参与新的指派</span>
          </div>
          {dashboard === null ? (
            <p className="text-muted-foreground py-12 text-center text-sm">加载中…</p>
          ) : busyMachines.length === 0 ? (
            <p className="text-muted-foreground py-12 text-center text-sm">
              暂无被占用的机器。创建任务时选择「指定机器」或「自动分配」后,占用情况会出现在这里。
            </p>
          ) : (
            <Table className="table-fixed">
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  {/* 机器列不设宽度:table-fixed 下自动吃掉余量。 */}
                  <TableHead>机器</TableHead>
                  <TableHead className="w-44">占用任务</TableHead>
                  <TableHead className="w-20">状态</TableHead>
                  <TableHead className="w-24">地区</TableHead>
                  <TableHead className="w-36">最近检测</TableHead>
                  <TableHead className="w-20">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {busyMachines.map((m) => {
                  const taskID = num(m.assigned_task_id);
                  return (
                    <TableRow key={m.id}>
                      <TableCell className="min-w-0">
                        <Link
                          href={`/function/vps/detail?id=${m.id}`}
                          className="block w-full truncate text-left font-medium hover:underline"
                          title={m.name}
                        >
                          {m.name}
                        </Link>
                        <span className="text-muted-foreground block truncate font-mono text-xs" title={`${m.host}:${m.port}`}>
                          {m.host}:{m.port}
                        </span>
                      </TableCell>
                      <TableCell className="text-xs">
                        {taskID > 0 ? (
                          <Link
                            href={`/function/tasks/detail?id=${taskID}`}
                            className="block w-full truncate text-blue-600 hover:underline"
                            title={m.assigned_task_name || `任务 #${taskID}`}
                          >
                            {m.assigned_task_name || `任务 #${taskID}`}
                          </Link>
                        ) : (
                          <span className="text-muted-foreground">—</span>
                        )}
                      </TableCell>
                      <TableCell>
                        <Badge variant="outline" className={vpsStatusBadgeClass(String(m.status))}>
                          {vpsStatusLabel(String(m.status))}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-muted-foreground truncate text-xs" title={m.region}>
                        {m.region || "—"}
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs tabular-nums">{fmtTime(m.last_check_at)}</TableCell>
                      <TableCell>
                        <Button asChild variant="ghost" size="icon-xs" title="进入机器详情">
                          <Link href={`/function/vps/detail?id=${m.id}`} aria-label="进入机器详情">
                            <ArrowRightIcon className="size-3.5" />
                          </Link>
                        </Button>
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {/* ── 最近执行流水 ── */}
      <Card>
        <CardContent className="px-0 pt-4">
          <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
            <span className="flex items-center gap-1.5 text-sm font-medium">
              <ListTodoIcon className="text-muted-foreground size-4" />
              最近执行流水
            </span>
            <span className="text-muted-foreground text-xs">agent / 终端 / 工具集 / 手动 / 探测产生的远程命令记录</span>
            <Button asChild variant="ghost" size="sm" className="ml-auto">
              <Link href="/function/vps/list">
                <ClockIcon className="size-3.5" />
                前往机器池
              </Link>
            </Button>
          </div>
          {dashboard === null ? (
            <p className="text-muted-foreground py-12 text-center text-sm">加载中…</p>
          ) : recent.length === 0 ? (
            <p className="text-muted-foreground py-12 text-center text-sm">
              还没有执行记录。录入机器并在任务里指派后,agent 的每条远程命令都会记录在这里。
            </p>
          ) : (
            <Table className="table-fixed">
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="w-36">时间</TableHead>
                  <TableHead className="w-28">机器</TableHead>
                  {/* 命令列不设宽度:table-fixed 下自动吃掉余量。 */}
                  <TableHead>命令</TableHead>
                  <TableHead className="w-20">来源</TableHead>
                  <TableHead className="w-16">退出码</TableHead>
                  <TableHead className="w-20">耗时</TableHead>
                  <TableHead className="w-14">输出</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {recent.map((r) => {
                  const exit = r.exit_code;
                  const name = machineNames.get(num(r.machine_id));
                  return (
                    <TableRow key={r.id}>
                      <TableCell className="text-muted-foreground text-xs tabular-nums">{fmtTime(r.created_at)}</TableCell>
                      <TableCell className="min-w-0">
                        {num(r.machine_id) > 0 ? (
                          <Link
                            href={`/function/vps/detail?id=${r.machine_id}`}
                            className="block w-full truncate text-xs hover:underline"
                            title={name || `机器 #${r.machine_id}`}
                          >
                            {name || `#${r.machine_id}`}
                          </Link>
                        ) : (
                          <span className="text-muted-foreground text-xs">—</span>
                        )}
                      </TableCell>
                      <TableCell className="min-w-0">
                        <span className="block truncate font-mono text-xs" title={r.command}>
                          {r.command || "—"}
                        </span>
                      </TableCell>
                      <TableCell>
                        <Badge variant="outline" className={vpsSourceBadgeClass(String(r.source))}>
                          {vpsSourceLabel(String(r.source))}
                        </Badge>
                      </TableCell>
                      <TableCell className="tabular-nums">
                        {exit == null ? (
                          <span className="text-muted-foreground text-xs">—</span>
                        ) : (
                          <span
                            className={cn(
                              "font-mono text-xs font-semibold",
                              num(exit) === 0 ? "text-emerald-600 dark:text-emerald-400" : "text-red-600 dark:text-red-400",
                            )}
                          >
                            {num(exit)}
                          </span>
                        )}
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs tabular-nums">
                        {fmtDuration(num(r.duration_ms))}
                      </TableCell>
                      <TableCell>
                        <Button variant="ghost" size="icon-xs" title="查看完整输出" onClick={() => setViewing(r)}>
                          <EyeIcon className="size-3.5" />
                        </Button>
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <VpsExecOutputDialog execution={viewing} onOpenChange={(o) => !o && setViewing(null)} />
    </div>
  );
}
