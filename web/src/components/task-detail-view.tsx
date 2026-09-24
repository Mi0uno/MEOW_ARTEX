"use client";

// 任务详情视图(本体):由 /function/tasks/detail 与 /function/recon/detail 两条
// 路由共享渲染。阶段感知:
//   - exploit/post 任务 → 原有 agent 页签(会话/总览/探索链路/…/报告);
//   - recon 任务        → recon 专属页签(链路图/执行轨迹/暂存区),头部操作同样替换
//                         (LLM 配置链/暂停/归档 → 刷新/重跑/删除)。

import * as React from "react";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";

import {
  ArchiveIcon,
  ArrowLeftIcon,
  BrainIcon,
  CheckIcon,
  CircleAlertIcon,
  PauseIcon,
  PlayIcon,
  RefreshCwIcon,
  Trash2Icon,
} from "lucide-react";
import { toast } from "sonner";

import { ReconRunsPanel, ReconGraphPanel, ReconStagedPanel, ReconOverviewPanel, ReconAssetsPanel, ReconEvidencePanel } from "@/components/recon-detail-panels";
import { StatusBadge } from "@/components/status-badge";
import { TaskLLMProfileChain } from "@/components/task-llm-profile-chain";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import {
  Popover,
  PopoverContent,
  PopoverDescription,
  PopoverHeader,
  PopoverTitle,
  PopoverTrigger,
} from "@/components/ui/popover";
import { Separator } from "@/components/ui/separator";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { Spinner } from "@/components/ui/spinner";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { api } from "@/lib/api";
import type { LLMProfile, Task } from "@/lib/types";

import { AssetsTab } from "@/app/(main)/function/tasks/detail/_tabs/assets-tab";
import { BroadcastTab } from "@/app/(main)/function/tasks/detail/_tabs/broadcast-tab";
import { CoverageGraphTab } from "@/app/(main)/function/tasks/detail/_tabs/coverage-graph-tab";
import { FindingsTab } from "@/app/(main)/function/tasks/detail/_tabs/findings-tab";
import { GraphTab } from "@/app/(main)/function/tasks/detail/_tabs/graph-tab";
import { InterceptTab } from "@/app/(main)/function/tasks/detail/_tabs/intercept-tab";
import { OverviewTab } from "@/app/(main)/function/tasks/detail/_tabs/overview-tab";
import { ReportTab } from "@/app/(main)/function/tasks/detail/_tabs/report-tab";
import { RetestsTab } from "@/app/(main)/function/tasks/detail/_tabs/retests-tab";
import { SessionsTab } from "@/app/(main)/function/tasks/detail/_tabs/sessions-tab";

const TABS = [
  { value: "sessions", label: "会话" },
  { value: "overview", label: "总览" },
  { value: "graph", label: "探索链路" },
  { value: "broadcast", label: "播报板" },
  { value: "findings", label: "发现" },
  { value: "retests", label: "复测" },
  { value: "assets", label: "测试资产" },
  { value: "coverage", label: "资产覆盖图" },
  { value: "intercept", label: "拦截审批" },
  { value: "report", label: "报告" },
];

// recon 阶段页签(针对性替换,页签样式与 agent 侧一致)。
const RECON_TABS = [
  { value: "overview", label: "总览" },
  { value: "graph", label: "链路图" },
  { value: "runs", label: "执行轨迹" },
  { value: "assets", label: "发现资产" },
  { value: "staged", label: "暂存区" },
  { value: "evidence", label: "证据" },
];

function taskProfileIDs(task: Task): string[] {
  if (task.llm_profile_ids && task.llm_profile_ids.length > 0) {
    return task.llm_profile_ids.map(String);
  }
  return task.llm_profile_id ? [String(task.llm_profile_id)] : [];
}

function TaskLLMControl({ task, profiles, onUpdated }: { task: Task; profiles: LLMProfile[]; onUpdated: () => void }) {
  const [open, setOpen] = React.useState(false);
  const [profileIDs, setProfileIDs] = React.useState<string[]>(() => taskProfileIDs(task));
  const [activeProfileID, setActiveProfileID] = React.useState(
    task.active_llm_profile_id ? String(task.active_llm_profile_id) : (taskProfileIDs(task)[0] ?? ""),
  );
  const [saving, setSaving] = React.useState(false);
  const popoverContentRef = React.useRef<HTMLDivElement>(null);

  const chain = taskProfileIDs(task);
  const exhausted = task.llm_failover_state === "chain_exhausted";
  // 任何状态都可以改链,终态也不例外:任务结束后主 Agent 对话仍走这条链,
  // 链上模型出问题时必须能换掉,否则已完成任务就没法继续交互。
  const terminal = ["done", "failed", "timeout"].includes(task.status);
  // A null active profile on an exhausted, non-empty chain is a persisted end
  // cursor. Keep the status display honest; choosing the first profile is only
  // the editor's reset draft and does not mean it is currently active.
  let activeID = chain[0] ?? "";
  if (exhausted) activeID = "";
  if (task.active_llm_profile_id) activeID = String(task.active_llm_profile_id);
  const activeProfile = profiles.find((profile) => profile.id === activeID);
  const currentLabel = exhausted
    ? "配置链已耗尽"
    : (activeProfile?.name ?? (activeID ? `配置 #${activeID}` : "跟随默认配置"));
  const activeIndex = chain.indexOf(activeID);
  const backupCount = activeIndex >= 0 ? Math.max(0, chain.length - activeIndex - 1) : 0;
  const currentTitle = [currentLabel, activeProfile?.model, backupCount > 0 ? `${backupCount} 个备用` : ""]
    .filter(Boolean)
    .join(" · ");
  let editorDescription = "调整顺序或当前配置后，将从下一次 LLM 调用开始生效。";
  if (terminal) editorDescription = "任务已结束，改动只影响后续的主 Agent 对话。";
  let saveLabel = "保存";
  if (exhausted) saveLabel = "保存并重置";
  if (saving) saveLabel = "保存中";

  const syncDraft = React.useCallback(() => {
    const next = taskProfileIDs(task);
    setProfileIDs(next);
    setActiveProfileID(task.active_llm_profile_id ? String(task.active_llm_profile_id) : (next[0] ?? ""));
  }, [task]);

  const handleOpenChange = (next: boolean) => {
    setOpen(next);
    if (next) syncDraft();
  };

  const handleProfileIDsChange = (next: string[]) => {
    setProfileIDs(next);
    setActiveProfileID((current) => (next.includes(current) ? current : (next[0] ?? "")));
  };

  const save = async () => {
    setSaving(true);
    try {
      const result = await api.updateTaskLLMProfiles(
        task.id,
        profileIDs.map(Number),
        activeProfileID ? Number(activeProfileID) : undefined,
      );
      if (result.switch_event) {
        toast.info(result.switch_event.summary, { id: `task-${task.id}-llm-${result.switch_event.seq}` });
      } else {
        toast.success(
          result.reopened_intents > 0
            ? `LLM 配置已更新，并恢复 ${result.reopened_intents} 条额度阻塞意图`
            : "LLM 配置已更新",
        );
      }
      setOpen(false);
      onUpdated();
    } catch (error) {
      toast.error("更新失败：" + (error as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Popover open={open} onOpenChange={handleOpenChange}>
      <PopoverTrigger asChild>
        <Button
          size="sm"
          variant={exhausted ? "destructive" : "outline"}
          aria-label="查看或切换任务 LLM 配置"
          title={currentTitle}
        >
          <BrainIcon data-icon="inline-start" />
          <span className="hidden max-w-36 truncate lg:inline">{currentLabel}</span>
          {backupCount > 0 && <span className="hidden text-muted-foreground xl:inline">+{backupCount}</span>}
        </Button>
      </PopoverTrigger>
      <PopoverContent ref={popoverContentRef} align="start" className="w-[min(28rem,calc(100vw-2rem))] gap-4 p-4">
        <PopoverHeader>
          <PopoverTitle>任务 LLM 配置链</PopoverTitle>
          <PopoverDescription>{editorDescription}</PopoverDescription>
        </PopoverHeader>

        {exhausted && (
          <Alert variant="destructive">
            <CircleAlertIcon />
            <AlertTitle>配置链额度已耗尽</AlertTitle>
            <AlertDescription>
              {task.llm_failover_reason ?? "所有已选配置均被判定为额度不足。保存配置链可重置故障状态。"}
            </AlertDescription>
          </Alert>
        )}

        <TaskLLMProfileChain
          profiles={profiles}
          value={profileIDs}
          onValueChange={handleProfileIDsChange}
          activeProfileId={activeProfileID}
          onActiveProfileChange={setActiveProfileID}
          inputId="task-llm-profiles"
          disabled={saving}
          portalContainer={popoverContentRef}
        />

        <div className="flex justify-end gap-2">
          <Button type="button" variant="outline" size="sm" onClick={() => setOpen(false)}>
            关闭
          </Button>
          <Button type="button" size="sm" onClick={save} disabled={saving}>
            {saving && <Spinner data-icon="inline-start" />}
            {saveLabel}
          </Button>
        </div>
      </PopoverContent>
    </Popover>
  );
}

export function TaskDetailView() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const id = searchParams.get("id") ?? "";
  const [task, setTask] = React.useState<Task | null>(null);
  const [paused, setPaused] = React.useState(false);
  const [loaded, setLoaded] = React.useState(false);
  const [tab, setTab] = React.useState<string | null>(null);
  const [interceptPendingCount, setInterceptPendingCount] = React.useState(0);
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [archiving, setArchiving] = React.useState(false);
  const [rerunning, setRerunning] = React.useState(false);
  const [deleting, setDeleting] = React.useState(false);

  React.useEffect(() => {
    api
      .llmProfiles()
      .then(setProfiles)
      .catch(() => setProfiles([]));
  }, []);

  React.useEffect(() => {
    let alive = true;
    const load = () =>
      api
        .interceptTask(id)
        .then((rows) => {
          if (alive) setInterceptPendingCount(rows.filter((r) => r.status === "pending").length);
        })
        .catch(() => {
          // Polling is best-effort; the next interval retries automatically.
        });
    void load();
    const t = setInterval(load, 5000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, [id]);

  const taskLoadInFlight = React.useRef<string | null>(null);
  const load = React.useCallback(() => {
    if (taskLoadInFlight.current === id) return;
    taskLoadInFlight.current = id;
    Promise.all([api.task(id), api.stats(id).catch(() => null)])
      .then(([task, s]) => {
        if (taskLoadInFlight.current !== id) return;
        const base = { ...task };
        const at = s?.active_task;
        if (at) {
          base.in_flight = at.in_flight;
          base.goals_total = at.goals_total;
          base.goals_met = at.goals_met;
          base.engine_mode = s?.engine_mode ?? at.engine_mode;
          base.paused = at.paused;
        }
        setTask(base);
        setPaused(at?.paused ?? base.paused ?? false);
      })
      .catch(() => {
        // Keep the last rendered task state during a transient poll failure.
      })
      .finally(() => {
        if (taskLoadInFlight.current !== id) {
          taskLoadInFlight.current = null;
          setLoaded(true);
        }
      });
  }, [id]);
  React.useEffect(() => {
    load();
    const timer = setInterval(load, 5000);
    return () => clearInterval(timer);
  }, [load]);

  async function togglePause() {
    if (task && ["done", "failed", "timeout"].includes(task.status)) return;
    const next = !paused;
    try {
      await api.controlTask(id, next ? "pause" : "resume");
      setPaused(next);
      toast.success(next ? "已暂停探索" : "已恢复探索");
    } catch (e) {
      toast.error("操作失败：" + (e as Error).message);
    }
  }

  async function rerunRecon() {
    if (rerunning) return;
    setRerunning(true);
    try {
      await api.rerunRecon(id);
      toast.success("已触发重跑");
      load();
    } catch (e) {
      toast.error(`重跑失败：${(e as Error).message}`);
    } finally {
      setRerunning(false);
    }
  }

  async function deleteReconTask() {
    if (deleting) return;
    setDeleting(true);
    try {
      await api.deleteTask(id, {
        delete_assets: false,
        delete_traffic: false,
        delete_files: false,
        delete_findings: false,
        delete_llm_records: false,
      });
      toast.success(`任务 #${id} 已删除`);
      router.push("/function/tasks?stage=recon");
    } catch (e) {
      toast.error(`删除失败：${(e as Error).message}`);
      setDeleting(false);
    }
  }

  async function archiveTask() {
    if (!task || archiving) return;
    setArchiving(true);
    try {
      await api.archiveTask(task.id);
      toast.success("任务已加入归档队列");
      router.push("/function/tasks");
    } catch (error) {
      toast.error(`归档失败：${(error as Error).message}`);
      setArchiving(false);
    }
  }

  if (!task) {
    return (
      <div className="flex flex-1 flex-col items-center justify-center gap-3 p-10 text-center">
        <p className="text-muted-foreground">{loaded ? `任务 ${id} 已被删除、归档或不存在` : "加载中…"}</p>
        {loaded && (
          <Button asChild variant="outline">
            <Link href="/function/tasks">
              <ArrowLeftIcon /> 返回任务列表
            </Link>
          </Button>
        )}
      </div>
    );
  }

  const isRecon = task.stage === "recon";
  const tabs = isRecon ? RECON_TABS : TABS;
  const activeTab = tab ?? (isRecon ? "overview" : "sessions");
  const completed = task.status === "done";
  const terminal = ["done", "failed", "timeout"].includes(task.status);
  const archiveLifecycleEligible = terminal || paused || task.status === "paused";
  const canArchive = archiveLifecycleEligible && !task.archive_blocked_by_task_id;
  let archiveDisabledReason = task.queued ? "排队中的任务必须先暂停" : "运行中的任务必须先暂停";
  if (archiveLifecycleEligible && task.archive_blocked_by_task_id) {
    archiveDisabledReason = `任务被未归档任务 #${task.archive_blocked_by_task_id} 直接继承，请先归档依赖任务`;
  }
  const engineMode = paused ? "paused" : (task.engine_mode ?? "idle");
  let controlVariant: "default" | "secondary" | "outline" = "outline";
  let controlIcon = <PauseIcon data-icon="inline-start" />;
  let controlLabel = "暂停";
  if (terminal) {
    controlVariant = "secondary";
    controlIcon = <CheckIcon data-icon="inline-start" />;
    controlLabel = completed ? "已完成" : "已结束";
  } else if (paused) {
    controlVariant = "default";
    controlIcon = <PlayIcon data-icon="inline-start" />;
    controlLabel = "恢复";
  }

  return (
    <Tabs value={activeTab} onValueChange={setTab} className="flex flex-1 flex-col gap-0">
      {/* Top fixed area */}
      <header className="sticky top-0 z-10 flex flex-col gap-2 border-b bg-background/95 px-4 py-2.5 backdrop-blur lg:px-6">
        <div className="flex items-center gap-2">
          <SidebarTrigger className="-ml-1" />
          <Button asChild variant="ghost" size="icon" className="size-7">
            <Link href={isRecon ? "/function/tasks?stage=recon" : "/function/tasks"}>
              <ArrowLeftIcon />
            </Link>
          </Button>
          <h1 className="min-w-0 flex-1 truncate text-sm font-semibold" title={task.name || task.description}>
            {task.name || task.description}
          </h1>
          <code className="hidden rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground sm:inline">
            {task.id}
          </code>
          <Separator orientation="vertical" className="mx-1 hidden h-4 sm:block" />
          {!isRecon && <TaskLLMControl task={task} profiles={profiles} onUpdated={load} />}
          <StatusBadge domain={terminal ? "task" : "engine"} value={terminal ? task.status : engineMode} dot />
          {isRecon ? (
            <>
              <Button size="icon-sm" variant="ghost" onClick={() => load()} aria-label="刷新">
                <RefreshCwIcon />
              </Button>
              {!terminal && (
                <Button size="sm" variant="outline" onClick={() => void rerunRecon()} disabled={rerunning}>
                  <PlayIcon data-icon="inline-start" />
                  {rerunning ? "触发中…" : "重跑"}
                </Button>
              )}
              {terminal && (
                <AlertDialog>
                  <AlertDialogTrigger asChild>
                    <Button
                      size="icon-sm"
                      variant="ghost"
                      disabled={deleting}
                      className="text-red-600 hover:text-red-700"
                      aria-label="删除任务"
                    >
                      <Trash2Icon />
                    </Button>
                  </AlertDialogTrigger>
                  <AlertDialogContent>
                    <AlertDialogHeader>
                      <AlertDialogTitle>删除信息搜集任务 #{task.id}?</AlertDialogTitle>
                      <AlertDialogDescription>
                        任务及其链路图、执行轨迹、暂存区记录将一并删除;正式资产不受影响。此操作不可撤销。
                      </AlertDialogDescription>
                    </AlertDialogHeader>
                    <AlertDialogFooter>
                      <AlertDialogCancel>取消</AlertDialogCancel>
                      <AlertDialogAction onClick={() => void deleteReconTask()}>删除</AlertDialogAction>
                    </AlertDialogFooter>
                  </AlertDialogContent>
                </AlertDialog>
              )}
            </>
          ) : (
            <>
              {canArchive ? (
                <AlertDialog>
                  <AlertDialogTrigger asChild>
                    <Button
                      size="icon-sm"
                      variant="ghost"
                      disabled={archiving}
                      aria-label={canArchive ? "归档任务" : archiveDisabledReason}
                    >
                      {archiving ? <Spinner /> : <ArchiveIcon />}
                    </Button>
                  </AlertDialogTrigger>
                  <AlertDialogContent>
                    <AlertDialogHeader>
                      <AlertDialogTitle>归档任务 #{task.id}？</AlertDialogTitle>
                      <AlertDialogDescription>
                        任务图谱、关联记录、独占资产与流量、工作文件和 LLM
                        历史将压缩到冷存储。归档完成后可在任务列表的“已归档”页还原。
                      </AlertDialogDescription>
                    </AlertDialogHeader>
                    <AlertDialogFooter>
                      <AlertDialogCancel>取消</AlertDialogCancel>
                      <AlertDialogAction onClick={() => void archiveTask()}>确认归档</AlertDialogAction>
                    </AlertDialogFooter>
                  </AlertDialogContent>
                </AlertDialog>
              ) : (
                <Tooltip>
                  <TooltipTrigger asChild>
                    <span className="inline-flex">
                      <Button
                        size="icon-sm"
                        variant="ghost"
                        disabled={!canArchive || archiving}
                        aria-label={canArchive ? "归档任务" : archiveDisabledReason}
                      >
                        {archiving ? <Spinner /> : <ArchiveIcon />}
                      </Button>
                    </span>
                  </TooltipTrigger>
                  <TooltipContent>{archiveDisabledReason}</TooltipContent>
                </Tooltip>
              )}
              <Button size="sm" variant={controlVariant} onClick={togglePause} disabled={terminal}>
                {controlIcon}
                {controlLabel}
              </Button>
            </>
          )}
        </div>
        <p className="truncate text-xs text-muted-foreground">{task.goal}</p>
        {/* Tabs */}
        <div className="no-scrollbar min-w-0 overflow-x-auto">
          <TabsList variant="default" className="min-w-max">
            {tabs.map((t) => (
              <TabsTrigger key={t.value} value={t.value}>
                {t.label}
                {t.value === "intercept" && interceptPendingCount > 0 && (
                  <span className="ml-1.5 inline-flex h-4 min-w-[16px] items-center justify-center rounded-full bg-amber-500 px-1 text-[10px] font-semibold leading-none text-white">
                    {interceptPendingCount > 99 ? "99+" : interceptPendingCount}
                  </span>
                )}
              </TabsTrigger>
            ))}
          </TabsList>
        </div>
      </header>

      {/* Tab content */}
      <div className="flex-1 p-4 lg:p-6">
        {isRecon ? (
          <>
            <TabsContent value="overview" className="mt-0">
              <ReconOverviewPanel taskId={id} taskStatus={task.status} task={task} />
            </TabsContent>
            <TabsContent value="graph" className="mt-0">
              <ReconGraphPanel taskId={id} taskStatus={task.status} />
            </TabsContent>
            <TabsContent value="runs" className="mt-0">
              <ReconRunsPanel taskId={id} taskStatus={task.status} />
            </TabsContent>
            <TabsContent value="assets" className="mt-0">
              <ReconAssetsPanel taskId={id} taskStatus={task.status} />
            </TabsContent>
            <TabsContent value="staged" className="mt-0">
              <ReconStagedPanel taskId={id} taskStatus={task.status} />
            </TabsContent>
            <TabsContent value="evidence" className="mt-0">
              <ReconEvidencePanel taskId={id} />
            </TabsContent>
          </>
        ) : (
          <>
            <TabsContent value="sessions" className="mt-0">
              <SessionsTab taskId={id} />
            </TabsContent>
            <TabsContent value="overview" className="mt-0">
              <OverviewTab taskId={id} />
            </TabsContent>
            <TabsContent value="graph" className="mt-0">
              <GraphTab taskId={id} />
            </TabsContent>
            <TabsContent value="broadcast" className="mt-0">
              <BroadcastTab taskId={id} />
            </TabsContent>
            <TabsContent value="findings" className="mt-0">
              <FindingsTab taskId={id} />
            </TabsContent>
            <TabsContent value="retests" className="mt-0">
              <RetestsTab key={id} taskId={id} />
            </TabsContent>
            <TabsContent value="assets" className="mt-0">
              <AssetsTab taskId={id} />
            </TabsContent>
            <TabsContent value="coverage" className="mt-0">
              <CoverageGraphTab taskId={id} coverageEnabled={task?.coverage_enabled !== false} />
            </TabsContent>
            <TabsContent value="intercept" className="mt-0">
              <InterceptTab taskId={id} />
            </TabsContent>
            <TabsContent value="report" className="mt-0">
              <ReportTab taskId={id} />
            </TabsContent>
          </>
        )}
      </div>
    </Tabs>
  );
}
