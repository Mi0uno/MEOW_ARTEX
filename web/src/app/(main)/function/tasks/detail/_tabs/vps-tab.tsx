"use client";

// 任务详情 ·「执行环境」页签:这个任务的 Bash 命令在哪台机器上跑。
//
// 两种状态:
//   未指派 —— 显示引导文案 + 指派入口(从在线空闲机器里挑一台,或点「自动分配」让平台挑);
//   已指派 —— 机器卡片(跳详情 / 打开终端 / 解绑)+ 该任务在这台机器上的执行流水。
//
// 数据走 api.taskVpsEnvironment(无指派时 machine 为 null,接口 404/500 时按「未指派」
// 降级 + toast 提示,不白屏——后端(Agent C)晚于本页面上线也是合法状态)。

import * as React from "react";

import Link from "next/link";

import {
  CpuIcon,
  EyeIcon,
  HistoryIcon,
  MonitorIcon,
  RefreshCwIcon,
  ServerIcon,
  SquareTerminalIcon,
  UnlinkIcon,
  ZapIcon,
} from "lucide-react";
import { toast } from "sonner";

import {
  fmtDuration,
  fmtMemory,
  fmtTime,
  loadAssignCandidates,
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
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { TaskVpsEnvironment, VpsExecution, VpsMachine } from "@/lib/types";
import { cn } from "@/lib/utils";

// Radix Select 空值哨兵:「还没选机器」与「选了某台」必须可区分。
const PICK = "__pick__";

export function TaskVpsTab({ taskId }: { taskId: string }) {
  const [env, setEnv] = React.useState<TaskVpsEnvironment | null>(null);
  const [loaded, setLoaded] = React.useState(false);
  const [candidates, setCandidates] = React.useState<VpsMachine[]>([]);
  const [picked, setPicked] = React.useState(PICK);
  const [assigning, setAssigning] = React.useState(false);
  const [releasing, setReleasing] = React.useState(false);
  const [viewing, setViewing] = React.useState<VpsExecution | null>(null);

  const numericTaskId = Number(taskId);

  const load = React.useCallback(() => {
    api
      .taskVpsEnvironment(numericTaskId)
      .then((r) => setEnv(r))
      .catch((e) => {
        // 接口不可用:按「未指派」降级展示(指派动作会再报具体错误)。
        setEnv({ machine: null, executions: [] });
        if (loaded) toast.error(`加载执行环境失败:${(e as Error).message}`);
      })
      .finally(() => setLoaded(true));
  }, [numericTaskId, loaded]);

  const loadCandidates = React.useCallback(() => {
    loadAssignCandidates()
      .then(setCandidates)
      .catch(() => setCandidates([]));
  }, []);

  React.useEffect(() => {
    load();
    loadCandidates();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [numericTaskId]);

  // 轻量轮询:机器状态(在线/离线)会随后台测活变化。
  React.useEffect(() => {
    const t = setInterval(() => {
      load();
      loadCandidates();
    }, 10_000);
    return () => clearInterval(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [numericTaskId]);

  // 指派:指定机器(machineId)或自动("auto")。失败不阻断任务——文案里说清楚。
  const assign = async (machineId: number | "auto") => {
    setAssigning(true);
    try {
      const r = await api.assignTaskVps(numericTaskId, machineId);
      toast.success(r.machine ? `已指派机器「${r.machine.name}」,任务的 Bash 命令将改在该机器上执行` : "已提交自动分配");
      load();
      loadCandidates();
    } catch (e) {
      toast.error(`指派失败:${(e as Error).message}(任务仍在本机执行,可稍后重试)`);
    } finally {
      setAssigning(false);
    }
  };

  const release = async () => {
    if (!window.confirm("解绑机器?任务之后的命令将回到平台本机执行;机器立即恢复空闲、可被其他任务指派。")) return;
    setReleasing(true);
    try {
      await api.releaseTaskVps(numericTaskId);
      toast.success("已解绑,机器恢复空闲");
      load();
      loadCandidates();
    } catch (e) {
      toast.error(`解绑失败:${(e as Error).message}`);
    } finally {
      setReleasing(false);
    }
  };

  const machine = env?.machine ?? null;
  const executions: VpsExecution[] = env?.executions ?? [];

  return (
    <div className="grid gap-4">
      {/* ── 机器卡片 / 未指派引导 ── */}
      {!loaded ? (
        <p className="text-muted-foreground py-8 text-center text-sm">加载中…</p>
      ) : machine ? (
        <Card>
          <CardContent className="pt-4">
            <div className="flex flex-wrap items-start gap-x-8 gap-y-3">
              <div className="grid min-w-0 gap-1">
                <p className="text-muted-foreground text-xs">当前机器</p>
                <div className="flex items-center gap-2">
                  <Link
                    href={`/function/vps/detail?id=${machine.id}`}
                    className="truncate font-medium hover:underline"
                    title={machine.name}
                  >
                    <ServerIcon className="mr-1 inline size-3.5" />
                    {machine.name}
                  </Link>
                  <Badge variant="outline" className={vpsStatusBadgeClass(String(machine.status))}>
                    {vpsStatusLabel(String(machine.status))}
                  </Badge>
                </div>
                <span className="text-muted-foreground truncate font-mono text-xs">
                  {machine.username}@{machine.host}:{machine.port}
                </span>
              </div>

              <div className="grid gap-1">
                <p className="text-muted-foreground text-xs">系统</p>
                <span className="text-xs">
                  {[machine.os_name, machine.os_version].filter(Boolean).join(" ") || "未采集"}
                  {machine.arch ? ` · ${machine.arch}` : ""}
                </span>
                <span className="text-muted-foreground text-xs tabular-nums">
                  {num(machine.cpu_cores) > 0 ? `${num(machine.cpu_cores)} 核` : ""}
                  {num(machine.memory_mb) > 0 ? ` · ${fmtMemory(machine.memory_mb)}` : ""}
                </span>
              </div>

              <div className="grid gap-1">
                <p className="text-muted-foreground text-xs">说明</p>
                <span className="text-muted-foreground max-w-md text-xs">
                  任务的 Bash 命令在这台机器上执行(工作目录为远程任务工作区);Read/Write 等文件工具仍操作平台本地工作区,
                  远程文件请用 Bash cat/tee。
                </span>
              </div>

              <div className="flex flex-wrap items-center gap-2 sm:ml-auto">
                <Button asChild variant="outline" size="sm">
                  <Link href={`/function/vps/detail?id=${machine.id}`}>
                    <MonitorIcon className="size-3.5" />
                    机器详情
                  </Link>
                </Button>
                <Button asChild variant="outline" size="sm">
                  <Link href={`/function/vps/detail?id=${machine.id}&tab=terminal`}>
                    <SquareTerminalIcon className="size-3.5" />
                    打开终端
                  </Link>
                </Button>
                <Button variant="outline" size="sm" onClick={() => void release()} disabled={releasing}>
                  {releasing ? <RefreshCwIcon className="size-3.5 animate-spin" /> : <UnlinkIcon className="size-3.5" />}
                  {releasing ? "解绑中…" : "解绑机器"}
                </Button>
              </div>
            </div>
          </CardContent>
        </Card>
      ) : (
        <Card>
          <CardContent className="pt-4">
            <div className="flex flex-wrap items-center gap-x-8 gap-y-3">
              <div className="grid gap-1">
                <p className="text-sm font-medium">未指派机器</p>
                <span className="text-muted-foreground max-w-md text-xs">
                  任务当前在平台本机执行。指派一台 Linux 机器后,agent 的 Bash 命令将改在该机器上执行(远程工作区固定,
                  每次调用是独立 shell);不指派则一切照旧。
                </span>
              </div>
              <div className="flex flex-wrap items-center gap-2 sm:ml-auto">
                <Select value={picked} onValueChange={setPicked}>
                  <SelectTrigger className="w-64" title={candidates.length === 0 ? "暂无在线空闲机器" : undefined}>
                    <SelectValue placeholder={candidates.length === 0 ? "暂无在线空闲机器" : "选择机器"} />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      {candidates.length === 0 ? (
                        <SelectItem value={PICK} disabled>
                          暂无在线空闲机器
                        </SelectItem>
                      ) : (
                        candidates.map((m) => (
                          <SelectItem key={m.id} value={String(m.id)}>
                            {m.name} · {m.host}
                          </SelectItem>
                        ))
                      )}
                    </SelectGroup>
                  </SelectContent>
                </Select>
                <Button
                  size="sm"
                  disabled={assigning || picked === PICK}
                  title={picked === PICK ? "先从下拉里选一台机器" : undefined}
                  onClick={() => void assign(Number(picked))}
                >
                  <CpuIcon className="size-3.5" />
                  {assigning ? "指派中…" : "指派该机器"}
                </Button>
                <Button variant="outline" size="sm" disabled={assigning} onClick={() => void assign("auto")}>
                  <ZapIcon className="size-3.5" />
                  自动分配
                </Button>
              </div>
            </div>
            <p className="text-muted-foreground mt-2 text-[10px]">
              「自动分配」会挑一台在线且空闲的机器(维护/离线不参与);指派失败不影响任务,任务继续在本机执行。
            </p>
          </CardContent>
        </Card>
      )}

      {/* ── 该任务的执行流水 ── */}
      <Card>
        <CardContent className="px-0 pt-4">
          <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
            <span className="flex items-center gap-1.5 text-sm font-medium">
              <HistoryIcon className="text-muted-foreground size-4" />
              执行流水
            </span>
            <span className="text-muted-foreground text-xs">本任务在远程机器上的命令记录(agent / 终端 / 工具集)</span>
            <Button variant="ghost" size="sm" className="ml-auto" onClick={load}>
              <RefreshCwIcon className="size-3.5" />
              刷新
            </Button>
          </div>

          {executions.length === 0 ? (
            <p className="text-muted-foreground py-12 text-center text-sm">
              {machine
                ? "这台机器上还没有本任务的执行记录;agent 开始跑命令后会出现在这里。"
                : "未指派机器,因此没有远程执行流水。"}
            </p>
          ) : (
            <Table className="table-fixed">
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="w-36">时间</TableHead>
                  <TableHead className="w-20">来源</TableHead>
                  {/* 命令列不设宽度:table-fixed 下自动吃掉余量。 */}
                  <TableHead>命令</TableHead>
                  <TableHead className="w-16">退出码</TableHead>
                  <TableHead className="w-20">耗时</TableHead>
                  <TableHead className="w-14">输出</TableHead>
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
          )}
        </CardContent>
      </Card>

      <VpsExecOutputDialog execution={viewing} onOpenChange={(o) => !o && setViewing(null)} />
    </div>
  );
}
