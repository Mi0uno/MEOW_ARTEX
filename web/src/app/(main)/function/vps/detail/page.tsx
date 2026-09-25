"use client";

// VPS 机器详情(?id=机器 id;静态导出没有动态路由,useSearchParams 必须包 Suspense)。
//
// 五个页签:
//   概览     —— 连接信息(凭据永不回显)/ 系统信息 / 标签备注 / 健康状态 + 手动测活、工具检测、单命令试跑;
//   终端     —— Web 终端(vps-terminal.tsx,必须 dynamic(ssr:false),xterm 依赖浏览器 DOM);
//   工具检测 —— 已知工具探测矩阵,可整台重测;
//   执行流水 —— 本机的远程命令记录(来源筛选 + 分页 + 完整输出查看);
//   指派历史 —— 这台机器被哪些任务占过。
//
// 支持 /function/vps/detail?id=1&tab=terminal 深链(列表页「终端」按钮会带过来)。
// 返回链接跟随来源:任务页「机器详情 / 打开终端」进来带 from=task&taskId=N,页头返回
// 改「返回任务」指回任务详情;其余入口一律「返回机器池」。

import * as React from "react";

import Link from "next/link";
import dynamic from "next/dynamic";
import { useSearchParams } from "next/navigation";

import {
  ActivityIcon,
  ArrowLeftIcon,
  CheckCircle2Icon,
  CircleXIcon,
  ClockIcon,
  EyeIcon,
  HistoryIcon,
  InfoIcon,
  KeyRoundIcon,
  Loader2Icon,
  MapPinIcon,
  MonitorIcon,
  PlayIcon,
  RefreshCwIcon,
  SearchIcon,
  ServerIcon,
  SquareTerminalIcon,
  TagIcon,
  WrenchIcon,
} from "lucide-react";
import { toast } from "sonner";

import { TablePagination } from "@/components/table-pagination";
import {
  fmtBytes,
  fmtDuration,
  fmtMemory,
  fmtTime,
  num,
  VpsExecOutputDialog,
  vpsSourceBadgeClass,
  vpsSourceLabel,
  vpsStatusBadgeClass,
  vpsStatusLabel,
} from "@/components/vps-shared";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type {
  VpsAssignment,
  VpsExecResult,
  VpsExecution,
  VpsMachineDetail,
  VpsToolDetection,
} from "@/lib/types";
import { cn } from "@/lib/utils";

const PAGE_SIZE = 50;
const ALL = "__all__";
// 工具检测 tab:每页条数与安装状态筛选取值(Radix Select 空值语义用哨兵串,不用 "")。
const DETECT_PAGE_SIZE = 10;
const DETECT_INSTALLED = "installed";
const DETECT_NOT_INSTALLED = "not_installed";

// xterm 只在浏览器可用:必须 dynamic({ssr:false})(静态导出约束,契约 §7)。
const VpsTerminal = dynamic(() => import("@/components/vps-terminal").then((m) => m.VpsTerminal), {
  ssr: false,
  loading: () => (
    <div className="text-muted-foreground flex h-[420px] items-center justify-center rounded-lg border text-sm">
      终端组件加载中…
    </div>
  ),
});

const TAB_VALUES = ["overview", "terminal", "detect", "execs", "assignments"] as const;
type TabValue = (typeof TAB_VALUES)[number];

// useSearchParams 必须挂在 Suspense 边界内(静态导出约束)。
export default function VpsDetailPage() {
  return (
    <React.Suspense fallback={null}>
      <VpsDetailInner />
    </React.Suspense>
  );
}

function VpsDetailInner() {
  const searchParams = useSearchParams();
  const id = Number(searchParams.get("id") ?? "0");
  const tabParam = searchParams.get("tab") ?? "";
  // 来源参数:任务页跳转带 from=task&taskId=<任务id>;非法 / 缺失一律按 0(回机器池)。
  const fromTaskRaw = searchParams.get("from") === "task" ? Number(searchParams.get("taskId") ?? "0") : 0;
  const fromTask = Number.isFinite(fromTaskRaw) && fromTaskRaw > 0 ? fromTaskRaw : 0;
  // 无效 id 必须在任何数据 hook 之前早退:「读参数」与「用参数」拆成两个组件,
  // 才能既早退又不在同一组件里条件调用 hook(与代理组详情页同一手法)。
  if (!id || Number.isNaN(id)) return <MissingMachine />;
  return <VpsDetailBody machineId={id} initialTab={tabParam} fromTask={fromTask} />;
}

function MissingMachine() {
  return (
    <div className="p-4 md:p-6">
      <div className="text-muted-foreground flex flex-col items-center gap-3 py-16 text-center text-sm">
        <p>未指定机器。请从「机器池」列表进入。</p>
        <Button asChild variant="outline" size="sm">
          <Link href="/function/vps/list">
            <ArrowLeftIcon className="size-4" />
            返回机器池
          </Link>
        </Button>
      </div>
    </div>
  );
}

function VpsDetailBody({ machineId, initialTab, fromTask }: { machineId: number; initialTab: string; fromTask: number }) {
  const [tab, setTab] = React.useState<TabValue>(
    (TAB_VALUES as readonly string[]).includes(initialTab) ? (initialTab as TabValue) : "overview",
  );
  const [detail, setDetail] = React.useState<VpsMachineDetail | null>(null);
  const [failed, setFailed] = React.useState(false);
  const [checking, setChecking] = React.useState(false);
  const [detecting, setDetecting] = React.useState(false);

  const load = React.useCallback(() => {
    api
      .vpsMachine(machineId)
      .then((d) => {
        setDetail(d);
        setFailed(false);
      })
      .catch(() => {
        // 404(机器已删)/500(后端未就绪)统一落到「不存在」空态,不白屏。
        setFailed(true);
      });
  }, [machineId]);

  React.useEffect(() => {
    load();
  }, [load]);

  // 手动测活(同步):结果直接写回机器行,顺带把系统信息刷新。
  const check = async () => {
    setChecking(true);
    try {
      const m = await api.checkVpsMachine(machineId);
      if (m.status === "offline") {
        toast.warning(`测活失败:${m.last_error || "连接失败"}`);
      } else {
        toast.success(`测活通过(${vpsStatusLabel(String(m.status))}),系统信息已刷新`);
      }
      load();
    } catch (e) {
      toast.error(`测活失败:${(e as Error).message}`);
    } finally {
      setChecking(false);
    }
  };

  // 工具检测(同步跑一轮探测,耗时较久):完成后自动跳到「工具检测」页签看矩阵。
  const detect = async () => {
    setDetecting(true);
    try {
      const r = await api.detectVpsMachine(machineId);
      const installed = r.detections.filter((d) => d.installed).length;
      toast.success(`检测完成:已知工具 ${r.detections.length} 个,已安装 ${installed} 个`);
      load();
      setTab("detect");
    } catch (e) {
      toast.error(`工具检测失败:${(e as Error).message}`);
    } finally {
      setDetecting(false);
    }
  };

  if (failed) {
    return (
      <div className="p-4 md:p-6">
        <div className="text-muted-foreground flex flex-col items-center gap-3 py-16 text-center text-sm">
          <p>机器不存在、已被删除,或 VPS 服务暂不可用。</p>
          <Button asChild variant="outline" size="sm">
            <Link href="/function/vps/list">
              <ArrowLeftIcon className="size-4" />
              返回机器池
            </Link>
          </Button>
        </div>
      </div>
    );
  }

  const machine = detail?.machine ?? null;
  const detections = detail?.detections ?? [];
  const currentTask = detail?.current_task ?? null;
  // 返回链接跟随来源:from=task 时回任务详情,其余回机器池(与当前页签无关)。
  const backHref = fromTask > 0 ? `/function/tasks/detail?id=${fromTask}` : "/function/vps/list";
  const backLabel = fromTask > 0 ? "返回任务" : "返回机器池";

  return (
    <div className="flex flex-1 flex-col">
      {/* ── 页头:返回 / 机器名 / 状态 / 全局操作 ── */}
      <header className="sticky top-0 z-10 border-b bg-background/95 px-4 py-3 backdrop-blur lg:px-6">
        <div className="flex flex-wrap items-center gap-2">
          <Button asChild variant="ghost" size="sm" className="text-muted-foreground">
            <Link href={backHref} aria-label={backLabel}>
              <ArrowLeftIcon className="size-3.5" />
              {backLabel}
            </Link>
          </Button>
          <h1 className="min-w-0 truncate font-semibold text-lg">
            {machine?.name ?? `机器 #${machineId}`}
          </h1>
          {machine ? (
            <Badge variant="outline" className={vpsStatusBadgeClass(String(machine.status))}>
              {vpsStatusLabel(String(machine.status))}
            </Badge>
          ) : null}
          {machine ? (
            <code className="hidden rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground sm:inline">
              {machine.host}:{machine.port}
            </code>
          ) : null}
          {currentTask ? (
            <Link
              href={`/function/tasks/detail?id=${currentTask.id}`}
              className="text-blue-600 text-xs hover:underline"
              title={currentTask.name}
            >
              被任务「{currentTask.name}」占用
            </Link>
          ) : null}
          <div className="ml-auto flex items-center gap-2">
            <Button variant="outline" size="sm" onClick={() => void check()} disabled={checking}>
              {checking ? <Loader2Icon className="size-3.5 animate-spin" /> : <ActivityIcon className="size-3.5" />}
              手动测活
            </Button>
            <Button variant="outline" size="sm" onClick={() => void detect()} disabled={detecting}>
              {detecting ? <Loader2Icon className="size-3.5 animate-spin" /> : <WrenchIcon className="size-3.5" />}
              {detecting ? "检测中…" : "工具检测"}
            </Button>
            <Button
              variant={tab === "terminal" ? "default" : "outline"}
              size="sm"
              onClick={() => setTab("terminal")}
            >
              <SquareTerminalIcon className="size-3.5" />
              打开终端
            </Button>
          </div>
        </div>
      </header>

      <Tabs value={tab} onValueChange={(v) => setTab(v as TabValue)} className="flex flex-1 flex-col p-4 lg:p-6">
        <TabsList className="mb-4 w-fit">
          <TabsTrigger value="overview">概览</TabsTrigger>
          <TabsTrigger value="terminal">终端</TabsTrigger>
          <TabsTrigger value="detect">
            工具检测
            {detections.length > 0 && <Badge variant="outline" className="ml-1.5 px-1 py-0 text-[10px]">{detections.length}</Badge>}
          </TabsTrigger>
          <TabsTrigger value="execs">执行流水</TabsTrigger>
          <TabsTrigger value="assignments">指派历史</TabsTrigger>
        </TabsList>

        <TabsContent value="overview" className="mt-0">
          <OverviewSection machineId={machineId} machine={machine} />
        </TabsContent>

        {/* Radix Tabs 默认卸载非激活内容:终端页签离开即断开 WS,回到页签重新连。 */}
        <TabsContent value="terminal" className="mt-0">
          {machine ? (
            <VpsTerminal machineId={machineId} machineName={machine.name} />
          ) : (
            <p className="text-muted-foreground py-12 text-center text-sm">加载中…</p>
          )}
        </TabsContent>

        <TabsContent value="detect" className="mt-0">
          <DetectSection machineId={machineId} detections={detections} detecting={detecting} onDetect={() => void detect()} />
        </TabsContent>

        <TabsContent value="execs" className="mt-0">
          <ExecsSection machineId={machineId} />
        </TabsContent>

        <TabsContent value="assignments" className="mt-0">
          <AssignmentsSection machineId={machineId} />
        </TabsContent>
      </Tabs>
    </div>
  );
}

// ── 概览 ─────────────────────────────────────────────────────────────────────

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-3 py-1.5">
      <span className="text-muted-foreground shrink-0 text-xs">{label}</span>
      <span className="min-w-0 truncate text-right text-xs">{children}</span>
    </div>
  );
}

function OverviewSection({ machineId, machine }: { machineId: number; machine: VpsMachineDetail["machine"] | null }) {
  const [cmd, setCmd] = React.useState("");
  const [running, setRunning] = React.useState(false);
  const [result, setResult] = React.useState<VpsExecResult | null>(null);

  if (!machine) {
    return <p className="text-muted-foreground py-12 text-center text-sm">加载中…</p>;
  }

  // 单命令试跑:走与 agent 相同的执行通道(source=manual),方便验证机器与装了什么。
  const run = async () => {
    const c = cmd.trim();
    if (!c) return;
    setRunning(true);
    setResult(null);
    try {
      const r = await api.vpsExec(machineId, c);
      setResult(r);
    } catch (e) {
      toast.error(`执行失败:${(e as Error).message}`);
    } finally {
      setRunning(false);
    }
  };

  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Card>
        <CardContent className="pt-4">
          <p className="mb-2 flex items-center gap-1.5 text-sm font-medium">
            <ServerIcon className="text-muted-foreground size-4" />
            连接信息
            <span className="text-muted-foreground ml-auto text-[10px] font-normal">凭据加密存储,永不回显</span>
          </p>
          <Row label="主机">
            <span className="font-mono">
              {machine.host}:{machine.port}
            </span>
          </Row>
          <Row label="用户名">
            <span className="font-mono">{machine.username || "—"}</span>
          </Row>
          <Row label="认证方式">
            <span className="inline-flex items-center gap-1">
              <KeyRoundIcon className="size-3" />
              {machine.auth_type === "key" ? "私钥" : "密码"}
            </span>
          </Row>
          <Row label="凭据">
            <span className="font-mono text-muted-foreground">••••••••(不回显)</span>
          </Row>
          <Row label="工作区根目录">
            <span className="font-mono" title={machine.workspace_base}>
              {machine.workspace_base || "(服务端默认)"}
            </span>
          </Row>
          <Row label="录入时间">
            <span className="tabular-nums">{fmtTime(machine.created_at)}</span>
          </Row>
        </CardContent>
      </Card>

      <Card>
        <CardContent className="pt-4">
          <p className="mb-2 flex items-center gap-1.5 text-sm font-medium">
            <MonitorIcon className="text-muted-foreground size-4" />
            系统信息
            <span className="text-muted-foreground ml-auto text-[10px] font-normal">测活时自动采集</span>
          </p>
          <Row label="操作系统">
            {[machine.os_name, machine.os_version].filter(Boolean).join(" ") || "未采集"}
          </Row>
          <Row label="内核">
            <span className="font-mono">{machine.kernel || "—"}</span>
          </Row>
          <Row label="架构">
            {machine.arch || "—"}
          </Row>
          <Row label="CPU">{num(machine.cpu_cores) > 0 ? `${num(machine.cpu_cores)} 核` : "—"}</Row>
          <Row label="内存">{fmtMemory(machine.memory_mb)}</Row>
          <Row label="磁盘">{num(machine.disk_gb) > 0 ? `${num(machine.disk_gb)} GB` : "—"}</Row>
        </CardContent>
      </Card>

      <Card>
        <CardContent className="pt-4">
          <p className="mb-2 flex items-center gap-1.5 text-sm font-medium">
            <ActivityIcon className="text-muted-foreground size-4" />
            健康状态
          </p>
          <div className="mb-2 flex flex-wrap items-center gap-2">
            <Badge variant="outline" className={vpsStatusBadgeClass(String(machine.status))}>
              {vpsStatusLabel(String(machine.status))}
            </Badge>
            {num(machine.assigned_task_id) > 0 ? (
              <Badge variant="outline" className="border-blue-500/30 bg-blue-500/15 text-blue-600">
                任务 #{machine.assigned_task_id} 占用中
              </Badge>
            ) : null}
          </div>
          <Row label="最近测活">
            <span className="tabular-nums">{fmtTime(machine.last_check_at)}</span>
          </Row>
          <Row label="最近在线">
            <span className="tabular-nums">{fmtTime(machine.last_online_at)}</span>
          </Row>
          {machine.last_error ? (
            <p className="mt-2 flex items-start gap-1.5 rounded-md border border-red-500/30 bg-red-500/5 p-2 text-red-600 text-xs dark:text-red-400">
              <CircleXIcon className="mt-0.5 size-3.5 shrink-0" />
              <span className="min-w-0 break-all">{machine.last_error}</span>
            </p>
          ) : null}
          <p className="text-muted-foreground mt-2 text-[10px]">
            后台测活循环每分钟自动运行;占用中的机器只做连通性探测,不会被自动改为离线(由你判断任务是否受影响)。
          </p>
        </CardContent>
      </Card>

      <Card>
        <CardContent className="pt-4">
          <p className="mb-2 flex items-center gap-1.5 text-sm font-medium">
            <TagIcon className="text-muted-foreground size-4" />
            标签与备注
          </p>
          <div className="mb-3 flex flex-wrap gap-1.5">
            {(machine.tags ?? []).length > 0 ? (
              (machine.tags ?? []).map((t) => (
                <Badge key={t} variant="outline" className="text-xs">
                  <MapPinIcon className="size-3" />
                  {t}
                </Badge>
              ))
            ) : (
              <span className="text-muted-foreground text-xs">无标签</span>
            )}
            {machine.region ? (
              <Badge variant="outline" className="text-xs">
                {machine.region}
              </Badge>
            ) : null}
          </div>
          <p className="text-muted-foreground min-h-10 text-xs break-all">{machine.notes || "(无备注)"}</p>
        </CardContent>
      </Card>

      {/* 单命令试跑:同步执行,输出直接回显;完整输出同样落执行流水。 */}
      <Card className="lg:col-span-2">
        <CardContent className="pt-4">
          <p className="mb-2 flex items-center gap-1.5 text-sm font-medium">
            <PlayIcon className="text-muted-foreground size-4" />
            单命令试跑
            <span className="text-muted-foreground ml-auto text-[10px] font-normal">
              以登录用户在任务工作区执行,记录会落「执行流水」(来源=手动)
            </span>
          </p>
          <div className="flex flex-wrap items-center gap-2">
            <Input
              className="max-w-xl font-mono text-xs"
              placeholder='如:uname -a / nmap --version'
              value={cmd}
              onChange={(e) => setCmd(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && !running && void run()}
            />
            <Button size="sm" onClick={() => void run()} disabled={running || !cmd.trim()}>
              {running ? <Loader2Icon className="size-3.5 animate-spin" /> : <PlayIcon className="size-3.5" />}
              {running ? "执行中…" : "执行"}
            </Button>
          </div>
          {result ? (
            <div className="mt-3">
              <div className="mb-1.5 flex flex-wrap items-center gap-3 text-xs tabular-nums">
                <span className="flex items-center gap-1">
                  <InfoIcon className="size-3" />
                  退出码
                  <span
                    className={cn(
                      "font-mono font-semibold",
                      num(result.exit_code) === 0 ? "text-emerald-600 dark:text-emerald-400" : "text-red-600 dark:text-red-400",
                    )}
                  >
                    {result.exit_code ?? "—"}
                  </span>
                </span>
                <span className="text-muted-foreground">耗时 {fmtDuration(num(result.duration_ms))}</span>
                <span className="text-muted-foreground">输出 {fmtBytes(result.output.length)}</span>
                {result.truncated ? (
                  <span className="text-amber-600 dark:text-amber-400">输出已截断,完整版见执行流水</span>
                ) : null}
              </div>
              <pre className="max-h-72 overflow-auto rounded-lg border bg-muted/30 p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap break-all">
                {result.output || "(无输出)"}
              </pre>
            </div>
          ) : null}
        </CardContent>
      </Card>
    </div>
  );
}

// ── 工具检测矩阵 ─────────────────────────────────────────────────────────────

function DetectSection({
  machineId,
  detections,
  detecting,
  onDetect,
}: {
  machineId: number;
  detections: VpsToolDetection[];
  detecting: boolean;
  onDetect: () => void;
}) {
  const [query, setQuery] = React.useState("");
  const [status, setStatus] = React.useState(ALL);
  const [page, setPage] = React.useState(1);

  // 前端本地过滤(检测结果量小,不做服务端分页):搜索按工具名,状态三态筛选。
  const filtered = React.useMemo(() => {
    const q = query.trim().toLowerCase();
    return detections.filter((d) => {
      if (q && !d.tool_name.toLowerCase().includes(q)) return false;
      if (status === DETECT_INSTALLED && !d.installed) return false;
      if (status === DETECT_NOT_INSTALLED && d.installed) return false;
      return true;
    });
  }, [detections, query, status]);

  // 统计行跟随筛选结果更新。
  const installedCount = filtered.filter((d) => d.installed).length;
  const notInstalledCount = filtered.length - installedCount;

  const totalPages = Math.max(1, Math.ceil(filtered.length / DETECT_PAGE_SIZE));
  const safePage = Math.min(Math.max(1, page), totalPages);
  const pageItems = filtered.slice((safePage - 1) * DETECT_PAGE_SIZE, safePage * DETECT_PAGE_SIZE);

  return (
    <Card>
      <CardContent className="px-0 pt-4">
        <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
          <span className="flex items-center gap-1.5 text-sm font-medium">
            <WrenchIcon className="text-muted-foreground size-4" />
            工具检测矩阵
          </span>
          <div className="relative w-full sm:w-44">
            <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
            <Input
              value={query}
              onChange={(e) => {
                setQuery(e.target.value);
                setPage(1);
              }}
              placeholder="搜索工具名"
              className="h-8 pl-8 text-xs"
            />
          </div>
          <Select
            value={status}
            onValueChange={(v) => {
              setPage(1);
              setStatus(v);
            }}
          >
            <SelectTrigger className="h-8 w-28 text-xs">
              <SelectValue placeholder="状态" />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value={ALL}>全部</SelectItem>
                <SelectItem value={DETECT_INSTALLED}>已安装</SelectItem>
                <SelectItem value={DETECT_NOT_INSTALLED}>未安装</SelectItem>
              </SelectGroup>
            </SelectContent>
          </Select>
          <span className="text-muted-foreground ml-auto text-xs tabular-nums">
            共 {filtered.length} 个 · 已装{" "}
            <span className="text-emerald-600 dark:text-emerald-400">{installedCount}</span> · 未装 {notInstalledCount}
          </span>
          <Button variant="outline" size="sm" onClick={onDetect} disabled={detecting}>
            {detecting ? <Loader2Icon className="size-3.5 animate-spin" /> : <RefreshCwIcon className="size-3.5" />}
            {detecting ? "检测中(可能数十秒)…" : "重新检测"}
          </Button>
        </div>
        {detections.length === 0 ? (
          <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
            <WrenchIcon className="size-8 opacity-40" />
            <span>还没有检测结果。点「重新检测」对机器跑一轮已知工具探测(命令 -v + 版本号采集)。</span>
          </div>
        ) : filtered.length === 0 ? (
          <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
            <SearchIcon className="size-8 opacity-40" />
            <span>没有匹配的工具。换个关键字,或把状态筛选切回「全部」。</span>
          </div>
        ) : (
          <>
            <Table className="table-fixed">
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  {/* 工具名列不设宽度:table-fixed 下自动吃掉余量。 */}
                  <TableHead>工具</TableHead>
                  <TableHead className="w-20">状态</TableHead>
                  <TableHead className="w-48">版本</TableHead>
                  <TableHead className="w-40">检测时间</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {pageItems.map((d) => (
                  <TableRow key={d.tool_name}>
                    <TableCell className="min-w-0">
                      <span className="block truncate font-mono text-xs font-medium" title={d.tool_name}>
                        {d.tool_name}
                      </span>
                    </TableCell>
                    <TableCell>
                      {d.installed ? (
                        <span className="inline-flex items-center gap-1 text-emerald-600 text-xs dark:text-emerald-400">
                          <CheckCircle2Icon className="size-3.5" />
                          已安装
                        </span>
                      ) : (
                        <span className="text-muted-foreground inline-flex items-center gap-1 text-xs">
                          <CircleXIcon className="size-3.5" />
                          未安装
                        </span>
                      )}
                    </TableCell>
                    <TableCell className="min-w-0">
                      <span className="block truncate font-mono text-xs" title={d.version || undefined}>
                        {d.version || "—"}
                      </span>
                    </TableCell>
                    <TableCell className="text-muted-foreground text-xs tabular-nums">{fmtTime(d.detected_at)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
            <TablePagination
              page={page}
              pageSize={DETECT_PAGE_SIZE}
              total={filtered.length}
              pageSizeOptions={[DETECT_PAGE_SIZE]}
              onPageChange={setPage}
              onPageSizeChange={() => setPage(1)}
            />
          </>
        )}
      </CardContent>
    </Card>
  );
}

// ── 执行流水 ─────────────────────────────────────────────────────────────────

function ExecsSection({ machineId }: { machineId: number }) {
  const [executions, setExecutions] = React.useState<VpsExecution[]>([]);
  const [total, setTotal] = React.useState(0);
  const [source, setSource] = React.useState(ALL);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);
  const [loading, setLoading] = React.useState(true);
  const [viewing, setViewing] = React.useState<VpsExecution | null>(null);

  React.useEffect(() => {
    let alive = true;
    setLoading(true);
    api
      .vpsExecutions({
        machineId,
        source: source === ALL ? "" : source,
        limit: pageSize,
        offset: (page - 1) * pageSize,
      })
      .then((r) => {
        if (!alive) return;
        setExecutions(r.executions ?? []);
        setTotal(r.total);
      })
      .catch((e) => {
        if (!alive) return;
        setExecutions([]);
        setTotal(0);
        toast.error(`加载执行流水失败:${(e as Error).message}`);
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [machineId, source, page, pageSize]);

  return (
    <>
      <Card>
        <CardContent className="px-0 pt-4">
          <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
            <span className="flex items-center gap-1.5 text-sm font-medium">
              <HistoryIcon className="text-muted-foreground size-4" />
              执行流水
            </span>
            <Select
              value={source}
              onValueChange={(v) => {
                setPage(1);
                setSource(v);
              }}
            >
              <SelectTrigger className="w-32">
                <SelectValue placeholder="来源" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value={ALL}>全部来源</SelectItem>
                  <SelectItem value="agent">Agent</SelectItem>
                  <SelectItem value="terminal">终端</SelectItem>
                  <SelectItem value="toolset">工具集</SelectItem>
                  <SelectItem value="manual">手动</SelectItem>
                  <SelectItem value="detect">探测</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
            <span className="text-muted-foreground ml-auto text-xs tabular-nums">共 {total} 条</span>
          </div>

        {loading && executions.length === 0 ? (
          <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
        ) : executions.length === 0 ? (
          <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
            <ClockIcon className="size-8 opacity-40" />
            <span>本机还没有执行记录。agent 执行命令、终端会话、工具集脚本与手动试跑都会记录在这里。</span>
          </div>
        ) : (
          <>
            <Table className="table-fixed">
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="w-36">时间</TableHead>
                  <TableHead className="w-20">来源</TableHead>
                  <TableHead className="w-56">任务</TableHead>
                  {/* 命令列不设宽度:table-fixed 下自动吃掉余量。 */}
                  <TableHead>命令</TableHead>
                  <TableHead className="w-16">退出码</TableHead>
                  <TableHead className="w-20">耗时</TableHead>
                  <TableHead className="w-16">输出</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {executions.map((r) => (
                  <TableRow key={r.id}>
                    <TableCell className="text-muted-foreground text-xs tabular-nums">{fmtTime(r.created_at)}</TableCell>
                    <TableCell>
                      <Badge variant="outline" className={vpsSourceBadgeClass(String(r.source))}>
                        {vpsSourceLabel(String(r.source))}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-xs">
                      {num(r.task_id) > 0 ? (
                        <Link
                          href={`/function/tasks/detail?id=${r.task_id}`}
                          className="block w-full truncate text-blue-600 hover:underline"
                          title={`任务 #${r.task_id}`}
                        >
                          任务 #{r.task_id}
                        </Link>
                      ) : (
                        <span className="text-muted-foreground">—</span>
                      )}
                    </TableCell>
                    <TableCell className="min-w-0">
                      <span className="block truncate font-mono text-xs" title={r.command}>
                        {r.command || "—"}
                      </span>
                    </TableCell>
                    <TableCell className="tabular-nums">
                      {r.exit_code == null ? (
                        <span className="text-muted-foreground text-xs">—</span>
                      ) : (
                        <span
                          className={cn(
                            "font-mono text-xs font-semibold",
                            num(r.exit_code) === 0 ? "text-emerald-600 dark:text-emerald-400" : "text-red-600 dark:text-red-400",
                          )}
                        >
                          {num(r.exit_code)}
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
                ))}
              </TableBody>
            </Table>
            <TablePagination
              page={page}
              pageSize={pageSize}
              total={total}
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

      {/* 完整输出查看对话框:挂在卡片外,不进表格布局。 */}
      <VpsExecOutputDialog execution={viewing} onOpenChange={(o) => !o && setViewing(null)} />
    </>
  );
}

// ── 指派历史 ─────────────────────────────────────────────────────────────────

function AssignmentsSection({ machineId }: { machineId: number }) {
  const [assignments, setAssignments] = React.useState<VpsAssignment[]>([]);
  const [loading, setLoading] = React.useState(true);

  React.useEffect(() => {
    let alive = true;
    setLoading(true);
    api
      .vpsAssignments(machineId, 100)
      .then((r) => {
        if (alive) setAssignments(r.assignments ?? []);
      })
      .catch((e) => {
        if (!alive) return;
        setAssignments([]);
        toast.error(`加载指派历史失败:${(e as Error).message}`);
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [machineId]);

  return (
    <Card>
      <CardContent className="px-0 pt-4">
        <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
          <span className="flex items-center gap-1.5 text-sm font-medium">
            <HistoryIcon className="text-muted-foreground size-4" />
            指派历史
          </span>
          <span className="text-muted-foreground text-xs">released_at 为空 = 指派进行中</span>
        </div>
        {loading ? (
          <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
        ) : assignments.length === 0 ? (
          <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
            <HistoryIcon className="size-8 opacity-40" />
            <span>这台机器还没有被指派过。创建任务时选择「指定机器」或「自动分配」即可建立指派。</span>
          </div>
        ) : (
          <Table className="table-fixed">
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="w-36">指派时间</TableHead>
                {/* 任务列不设宽度:table-fixed 下自动吃掉余量。 */}
                <TableHead>任务</TableHead>
                <TableHead className="w-24">指派人</TableHead>
                <TableHead className="w-36">释放时间</TableHead>
                <TableHead className="w-32">释放原因</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {assignments.map((a) => {
                const active = !a.released_at;
                return (
                  <TableRow key={a.id}>
                    <TableCell className="text-muted-foreground text-xs tabular-nums">{fmtTime(a.assigned_at)}</TableCell>
                    <TableCell className="min-w-0">
                      {num(a.task_id) > 0 ? (
                        <Link
                          href={`/function/tasks/detail?id=${a.task_id}`}
                          className="block w-full truncate text-blue-600 text-xs hover:underline"
                          title={a.task_name || `任务 #${a.task_id}`}
                        >
                          {a.task_name || `任务 #${a.task_id}`}
                        </Link>
                      ) : (
                        <span className="text-muted-foreground text-xs">—</span>
                      )}
                    </TableCell>
                    <TableCell className="text-muted-foreground truncate text-xs">{a.assigned_by || "—"}</TableCell>
                    <TableCell className="text-xs tabular-nums">
                      {active ? (
                        <Badge variant="outline" className="border-blue-500/30 bg-blue-500/15 text-blue-600">
                          进行中
                        </Badge>
                      ) : (
                        <span className="text-muted-foreground tabular-nums">{fmtTime(a.released_at)}</span>
                      )}
                    </TableCell>
                    <TableCell className="text-muted-foreground truncate text-xs" title={a.release_reason}>
                      {a.release_reason || "—"}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}
