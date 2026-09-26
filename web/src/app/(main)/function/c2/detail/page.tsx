"use client";

// C2 会话详情(?id=会话 id;静态导出没有动态路由,useSearchParams 必须包 Suspense)。
//
// 头部主机卡:全部元信息 + 心跳节奏(sleep/jitter)调节 + 终止按钮。
// 四个页签(支持 ?tab= 深链):
//   交互   —— 命令-响应式通道(非交互 PTY):monospace 日志区自动滚底,
//            底部命令输入行下发 shell 任务,2s 轮询增量追加输出;
//   文件   —— 远端目录浏览(files_list 任务轮询解析)、上传(FormData)、
//            下载(transfer 轮询,完成后取产物)与本会话传输记录;
//   流水   —— 本会话全部任务(时间/操作者/类型/输入/状态/输出可展开);
//   档案   —— 会话元信息只读展示 +「刷新采集」(info 任务)。
//
// 轮询约定:增量拉取 after_id 只越过「已终态」的任务,排队/下发中的任务会在
// 后续轮询里重复返回,直到看到终态为止(详见 InteractTab 的 afterIdRef 注释)。

import * as React from "react";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";

import {
  ArrowLeftIcon,
  ChevronDownIcon,
  ChevronRightIcon,
  ClockIcon,
  DownloadIcon,
  FileTextIcon,
  FolderIcon,
  FolderUpIcon,
  HistoryIcon,
  InfoIcon,
  Loader2Icon,
  PlugZapIcon,
  PuzzleIcon,
  RefreshCwIcon,
  SkullIcon,
  SquareTerminalIcon,
  TimerIcon,
  UploadIcon,
} from "lucide-react";
import { toast } from "sonner";

import {
  c2DirectionMeta,
  c2ProtoMeta,
  c2SessionDirection,
  c2SessionStatusMeta,
  c2TaskInputText,
  c2TaskKindLabel,
  c2TaskStatusMeta,
  fmtBytes,
  fmtSleep,
  fmtTime,
  guessFileRoot,
  num,
  parentPath,
  pathJoin,
  relTime,
} from "@/components/c2-shared";
// 三期(契约 §21,Agent G):会话详情「插件」页签——插件库选择/参数表单/运行入队。
import { C2PluginRunPanel } from "@/components/c2-plugin-run-panel";
import { TablePagination } from "@/components/table-pagination";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
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
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { C2Listener, C2Session, C2Task, C2Transfer } from "@/lib/types";
import { cn } from "@/lib/utils";

const ALL = "__all__";
// 交互日志/流水展开里单条输出的预览上限;超长落盘的给「查看完整输出」链接。
const OUTPUT_PREVIEW = 16 * 1024;

const TAB_VALUES = ["interact", "files", "tasks", "profile"] as const;
type TabValue = (typeof TAB_VALUES)[number];

// useSearchParams 必须挂在 Suspense 边界内(静态导出约束)。
export default function C2DetailPage() {
  return (
    <React.Suspense fallback={null}>
      <C2DetailInner />
    </React.Suspense>
  );
}

function C2DetailInner() {
  const searchParams = useSearchParams();
  const id = Number(searchParams.get("id") ?? "0");
  const tabParam = searchParams.get("tab") ?? "";
  // 无效 id 必须在任何数据 hook 之前早退:「读参数」与「用参数」拆成两个组件
  // (与 VPS 详情页同一手法)。
  if (!id || Number.isNaN(id)) return <MissingSession />;
  return (
    <C2DetailBody
      sessionId={id}
      initialTab={(TAB_VALUES as readonly string[]).includes(tabParam) ? (tabParam as TabValue) : "interact"}
    />
  );
}

function MissingSession() {
  return (
    <div className="p-4 md:p-6">
      <div className="text-muted-foreground flex flex-col items-center gap-3 py-16 text-center text-sm">
        <p>未指定会话。请从「会话」列表进入。</p>
        <Button asChild variant="outline" size="sm">
          <Link href="/function/c2/sessions">
            <ArrowLeftIcon className="size-4" />
            返回会话列表
          </Link>
        </Button>
      </div>
    </div>
  );
}

function C2DetailBody({ sessionId, initialTab }: { sessionId: number; initialTab: TabValue }) {
  const [tab, setTab] = React.useState<TabValue>(initialTab);
  const [session, setSession] = React.useState<C2Session | null>(null);
  const [failed, setFailed] = React.useState(false);
  const [sleepOpen, setSleepOpen] = React.useState(false);
  const [connectOpen, setConnectOpen] = React.useState(false);
  const [killOpen, setKillOpen] = React.useState(false);
  const [killBusy, setKillBusy] = React.useState(false);

  const load = React.useCallback(() => {
    api
      .c2Session(sessionId)
      .then((s) => {
        setSession(s);
        setFailed(false);
      })
      .catch(() => {
        // 404(已删除)/500(后端未就绪)统一落到「不存在」空态,不白屏。
        setFailed(true);
      });
  }, [sessionId]);

  React.useEffect(() => {
    load();
    const iv = setInterval(load, 10_000);
    return () => clearInterval(iv);
  }, [load]);

  if (failed) {
    return (
      <div className="p-4 md:p-6">
        <div className="text-muted-foreground flex flex-col items-center gap-3 py-16 text-center text-sm">
          <p>会话不存在、已被删除,或 C2 服务暂不可用。</p>
          <Button asChild variant="outline" size="sm">
            <Link href="/function/c2/sessions">
              <ArrowLeftIcon className="size-4" />
              返回会话列表
            </Link>
          </Button>
        </div>
      </div>
    );
  }

  const meta = session ? c2SessionStatusMeta(String(session.status)) : null;
  // 三期:承载传输与连接方向(bind 会话由平台在 meta.direction 标注,契约 §18)。
  const transportMeta = session ? c2ProtoMeta(String(session.transport ?? "http")) : null;
  const direction = session ? c2SessionDirection(session.meta) : "reverse";
  const directionMeta = c2DirectionMeta(direction);

  return (
    <div className="flex flex-1 flex-col">
      {/* ── 页头:返回 / 主机名 / 状态 / 传输与方向徽标 / 全局操作 ── */}
      <header className="sticky top-0 z-10 border-b bg-background/95 px-4 py-3 backdrop-blur lg:px-6">
        <div className="flex flex-wrap items-center gap-2">
          <Button asChild variant="ghost" size="sm" className="text-muted-foreground">
            <Link href="/function/c2/sessions" aria-label="返回会话列表">
              <ArrowLeftIcon className="size-3.5" />
              返回会话
            </Link>
          </Button>
          <h1 className="min-w-0 truncate font-semibold text-lg">
            {session?.hostname ?? `会话 #${sessionId}`}
          </h1>
          {meta ? (
            <span className="inline-flex items-center gap-1.5 text-xs">
              <span className={cn("size-2 rounded-full", meta.dot)} />
              {meta.label}
            </span>
          ) : null}
          {transportMeta ? (
            <Badge
              variant="outline"
              className={transportMeta.badge}
              title={`承载传输:${transportMeta.label}${transportMeta.desc ? ` · ${transportMeta.desc}` : ""}`}
            >
              <transportMeta.Icon className="size-2.5" />
              {transportMeta.label}
            </Badge>
          ) : null}
          {direction === "bind" ? (
            <Badge variant="outline" className={directionMeta.badge} title={`连接方向:${directionMeta.desc}`}>
              {directionMeta.label} · bind
            </Badge>
          ) : null}
          {session?.elevated ? (
            <Badge variant="outline" className="border-red-500/30 bg-red-500/15 text-red-600">
              高权限
            </Badge>
          ) : null}
          {session ? (
            <code className="text-muted-foreground hidden font-mono text-xs sm:inline">
              {fmtSleep(session.sleep_sec, session.jitter_pct)}
            </code>
          ) : null}
          <div className="ml-auto flex items-center gap-2">
            <Button variant="outline" size="sm" onClick={() => setConnectOpen(true)} disabled={!session}>
              <PlugZapIcon className="size-3.5" />
              正向连接
            </Button>
            <Button variant="outline" size="sm" onClick={() => setSleepOpen(true)} disabled={!session}>
              <TimerIcon className="size-3.5" />
              心跳节奏
            </Button>
            <Button
              variant="destructive"
              size="sm"
              onClick={() => setKillOpen(true)}
              disabled={!session || session.status === "dead"}
            >
              <SkullIcon className="size-3.5" />
              {session?.status === "dead" ? "已终止" : "终止会话"}
            </Button>
          </div>
        </div>
      </header>

      {/* ── 主机卡:全部元信息 ── */}
      <div className="px-4 pt-4 lg:px-6">
        <Card>
          <CardContent className="grid gap-x-8 gap-y-1 pt-4 sm:grid-cols-2 lg:grid-cols-4">
            <MetaRow label="用户" value={session?.username || "—"} />
            <MetaRow label="系统 / 架构" value={[session?.os, session?.arch].filter(Boolean).join(" · ") || "—"} />
            <MetaRow label="内网 IP" value={session?.local_ips || "—"} mono />
            <MetaRow label="进程" value={session ? (session.process ? `${session.process} (${session.pid})` : "—") : "…"} mono />
            <MetaRow label="归属监听器" value={session ? `#${session.listener_id}` : "…"} />
            <MetaRow
              label="承载传输"
              value={session ? c2ProtoMeta(String(session.transport ?? "http")).label : "…"}
            />
            <MetaRow
              label="连接方向"
              value={session ? (direction === "bind" ? `${directionMeta.label}(bind,平台主动连接)` : "反向(reverse,beacon 回连)") : "…"}
            />
            <MetaRow
              label="心跳节奏"
              value={session ? `${fmtSleep(session.sleep_sec, session.jitter_pct)}(抖动 ${num(session?.jitter_pct)}%)` : "…"}
            />
            <MetaRow label="归属任务" value={session && num(session.task_id) > 0 ? `#${session.task_id}` : "未绑定"} />
            <MetaRow label="Beacon UID" value={session?.beacon_uid || "—"} mono />
            <MetaRow label="首次上线" value={fmtTime(session?.first_seen)} />
            <MetaRow label="最后心跳" value={session ? `${relTime(session.last_seen)}(${fmtTime(session.last_seen)})` : "…"} />
            <MetaRow label="会话 ID" value={session ? `#${session.id}` : "…"} />
            <MetaRow label="标签" value={session?.labels || "—"} />
          </CardContent>
        </Card>
      </div>

      <Tabs value={tab} onValueChange={(v) => setTab(v as TabValue)} className="flex flex-1 flex-col p-4 lg:p-6">
        <TabsList className="mb-4 w-fit">
          <TabsTrigger value="interact">
            <SquareTerminalIcon className="size-3.5" />
            交互
          </TabsTrigger>
          <TabsTrigger value="files">
            <FolderIcon className="size-3.5" />
            文件
          </TabsTrigger>
          {/* 三期(契约 §21,Agent G):插件页签。 */}
          <TabsTrigger value="plugins">
            <PuzzleIcon className="size-3.5" />
            插件
          </TabsTrigger>
          <TabsTrigger value="tasks">
            <HistoryIcon className="size-3.5" />
            流水
          </TabsTrigger>
          <TabsTrigger value="profile">
            <InfoIcon className="size-3.5" />
            档案
          </TabsTrigger>
        </TabsList>

        <TabsContent value="interact" className="mt-0">
          {session ? <InteractTab session={session} /> : null}
        </TabsContent>
        <TabsContent value="files" className="mt-0">
          {session ? <FilesTab session={session} /> : null}
        </TabsContent>
        {/* 三期(契约 §21,Agent G):插件页签。 */}
        <TabsContent value="plugins" className="mt-0">
          {session ? <C2PluginRunPanel session={session} /> : null}
        </TabsContent>
        <TabsContent value="tasks" className="mt-0">
          {session ? <TasksTab session={session} /> : null}
        </TabsContent>
        <TabsContent value="profile" className="mt-0">
          {session ? <ProfileTab session={session} /> : null}
        </TabsContent>
      </Tabs>

      <C2SleepDialog
        session={session}
        open={sleepOpen}
        onOpenChange={setSleepOpen}
        onSaved={() => {
          setSleepOpen(false);
          load();
        }}
      />

      {/* 三期:正向连接(bind)——平台以所选 tcp/kcp 监听器的密钥/协议主动 dial 目标。 */}
      <C2ConnectDialog open={connectOpen} onOpenChange={setConnectOpen} session={session} />

      {/* 终止确认:置 dead 后由 beacon 下次心跳自行退出。 */}
      <AlertDialog open={killOpen} onOpenChange={setKillOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>确认终止会话</AlertDialogTitle>
            <AlertDialogDescription>
              将终止「{session?.hostname || `会话 #${sessionId}`}」:会话置为 dead,beacon 在下次心跳时收到退出指令并自行退出。
              该操作不可恢复。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={killBusy}>取消</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault();
                setKillBusy(true);
                api
                  .c2KillSession(sessionId)
                  .then(() => {
                    toast.success("已下发终止指令,beacon 将在下次心跳时退出");
                    setKillOpen(false);
                    load();
                  })
                  .catch((err) => toast.error(`终止失败:${(err as Error).message}`))
                  .finally(() => setKillBusy(false));
              }}
              disabled={killBusy}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {killBusy ? "终止中…" : "确认终止"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

// ── 主机卡元信息行 ────────────────────────────────────────────────────────────

function MetaRow({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex min-w-0 items-center justify-between gap-3 py-0.5">
      <span className="text-muted-foreground shrink-0 text-xs">{label}</span>
      <span className={cn("min-w-0 truncate text-right text-xs", mono && "font-mono")} title={value}>
        {value}
      </span>
    </div>
  );
}

// ── 心跳节奏调节 ─────────────────────────────────────────────────────────────

function C2SleepDialog({
  session,
  open,
  onOpenChange,
  onSaved,
}: {
  session: C2Session | null;
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onSaved: () => void;
}) {
  const [sleepText, setSleepText] = React.useState("");
  const [jitterText, setJitterText] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    if (!open || !session) return;
    setSleepText(String(num(session.sleep_sec) || 5));
    setJitterText(String(num(session.jitter_pct)));
  }, [open, session]);

  const submit = async () => {
    if (!session) return;
    const sleep = Number(sleepText);
    const jitter = Number(jitterText || "0");
    if (!Number.isInteger(sleep) || sleep < 1 || sleep > 3600) {
      toast.error("心跳间隔必须是 1-3600 秒");
      return;
    }
    if (!Number.isInteger(jitter) || jitter < 0 || jitter > 90) {
      toast.error("抖动百分比必须是 0-90 的整数");
      return;
    }
    setBusy(true);
    try {
      await api.c2SleepSession(session.id, sleep, jitter);
      toast.success("心跳节奏已更新,beacon 在下次心跳时生效");
      onSaved();
    } catch (e) {
      toast.error(`保存失败:${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>调节心跳节奏</DialogTitle>
          <DialogDescription>
            心跳间隔越短响应越快、流量特征越明显;抖动按百分比随机化间隔,规避固定周期检测。
          </DialogDescription>
        </DialogHeader>
        <div className="grid grid-cols-2 gap-3">
          <div className="grid gap-1.5">
            <Label htmlFor="c2-sleep-sec">心跳间隔(秒)</Label>
            <Input
              id="c2-sleep-sec"
              type="number"
              min={1}
              max={3600}
              value={sleepText}
              onChange={(e) => setSleepText(e.target.value)}
              className="tabular-nums"
            />
            <p className="text-muted-foreground text-[10px]">1-3600 秒</p>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="c2-sleep-jitter">抖动(%)</Label>
            <Input
              id="c2-sleep-jitter"
              type="number"
              min={0}
              max={90}
              value={jitterText}
              onChange={(e) => setJitterText(e.target.value)}
              className="tabular-nums"
            />
            <p className="text-muted-foreground text-[10px]">0-90,0 = 固定间隔</p>
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy}>
            取消
          </Button>
          <Button disabled={busy} onClick={() => void submit()}>
            {busy ? "保存中…" : "保存"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ── 正向连接(三期,契约 §18)─────────────────────────────────────────────────

// C2ConnectDialog 正向连接:平台以所选 tcp/kcp 监听器的密钥/协议主动 dial 目标
// (bind beacon 在目标机监听端口等首帧 reg)。仅 tcp/kcp 支持正向;连接失败
// (TCP 拒绝 / 超时最常见)把后端错误原文报出来。成功后跳新会话详情。
function C2ConnectDialog({
  open,
  onOpenChange,
  session,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  session: C2Session | null;
}) {
  const router = useRouter();
  const [listeners, setListeners] = React.useState<C2Listener[]>([]);
  const [listenerId, setListenerId] = React.useState("");
  const [host, setHost] = React.useState("");
  const [portText, setPortText] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    if (!open) return;
    setHost("");
    setPortText("");
    let alive = true;
    api
      .c2Listeners()
      .then((r) => {
        if (!alive) return;
        const ls = r.listeners ?? [];
        setListeners(ls);
        // 预选:当前会话的监听器(若支持正向),否则第一个 tcp/kcp 监听器。
        const bindable = (l: C2Listener) => l.proto === "tcp" || l.proto === "kcp";
        const cur = ls.find((l) => l.id === session?.listener_id && bindable(l));
        const first = cur ?? ls.find(bindable);
        setListenerId(first ? String(first.id) : "");
      })
      .catch(() => {
        if (alive) setListeners([]);
      });
    return () => {
      alive = false;
    };
  }, [open, session?.listener_id]);

  const submit = async () => {
    const lid = Number(listenerId);
    if (!Number.isInteger(lid) || lid <= 0) {
      toast.error("请选择用于正向连接的监听器(仅 tcp / kcp 支持)");
      return;
    }
    const h = host.trim();
    if (!h) {
      toast.error("请填写目标 host(bind beacon 正在监听的地址)");
      return;
    }
    const port = Number(portText);
    if (!Number.isInteger(port) || port <= 0 || port > 65535) {
      toast.error("端口必须是 1-65535 的整数");
      return;
    }
    setBusy(true);
    try {
      const r = await api.c2ConnectBind({ listener_id: lid, host: h, port });
      toast.success(`正向连接成功,会话 #${r.session_id} 已建立,正在跳转…`);
      onOpenChange(false);
      router.push(`/function/c2/detail?id=${r.session_id}`);
    } catch (e) {
      // 目标无 beacon 监听时 TCP 拒绝 / 超时都会发生:错误原文直接呈现。
      toast.error(`正向连接失败:${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>正向连接(bind)</DialogTitle>
          <DialogDescription>
            目标机上的 bind beacon 正在监听端口等待握手:平台以所选监听器的密钥与协议主动拨号建立会话。仅
            tcp / kcp 监听器支持正向连接。
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid gap-1.5">
            <Label htmlFor="c2-connect-listener">监听器(提供密钥与协议)</Label>
            <Select value={listenerId} onValueChange={setListenerId}>
              <SelectTrigger className="w-full">
                <SelectValue placeholder="选择 tcp / kcp 监听器" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  {listeners.length === 0 ? (
                    <SelectItem value="__none__" disabled>
                      没有可用监听器,请先到「监听器」页创建
                    </SelectItem>
                  ) : (
                    listeners.map((l) => {
                      const bindable = l.proto === "tcp" || l.proto === "kcp";
                      return (
                        <SelectItem key={l.id} value={String(l.id)} disabled={!bindable}>
                          [{c2ProtoMeta(String(l.proto ?? "http")).label}] {l.name}
                          {!bindable ? "(不支持正向)" : ""}
                        </SelectItem>
                      );
                    })
                  )}
                </SelectGroup>
              </SelectContent>
            </Select>
            <p className="text-muted-foreground text-[10px]">
              平台用该监听器的密钥与目标握手,协议必须与 bind beacon 一致
            </p>
          </div>
          <div className="grid grid-cols-3 gap-3">
            <div className="col-span-2 grid gap-1.5">
              <Label htmlFor="c2-connect-host">目标 host</Label>
              <Input
                id="c2-connect-host"
                value={host}
                onChange={(e) => setHost(e.target.value)}
                placeholder="bind beacon 监听的地址,如 10.0.0.5"
                className="font-mono"
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="c2-connect-port">端口</Label>
              <Input
                id="c2-connect-port"
                type="number"
                min={1}
                max={65535}
                value={portText}
                onChange={(e) => setPortText(e.target.value)}
                placeholder="如 4444"
                className="font-mono"
              />
            </div>
          </div>
          <p className="text-muted-foreground text-[10px]">
            连不上会原样报错(TCP 拒绝 / 超时最常见):确认目标防火墙放行、bind beacon 已在监听。
          </p>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy}>
            取消
          </Button>
          <Button disabled={busy} onClick={() => void submit()}>
            {busy ? (
              <>
                <Loader2Icon className="size-3.5 animate-spin" />
                连接中…
              </>
            ) : (
              "连接"
            )}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ── 共用:等待任务终态 ────────────────────────────────────────────────────────

// waitTaskTerminal 每 2s 增量拉取会话任务,直到指定任务到达终态;超时返回 null。
// after_id 取 taskId-1:增量接口只回 id 更大的任务,该任务一定落在返回里。
async function waitTaskTerminal(sessionId: number, taskId: number, timeoutMs = 120_000): Promise<C2Task | null> {
  const after = Math.max(0, taskId - 1);
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    await new Promise((r) => setTimeout(r, 2000));
    try {
      const r = await api.c2SessionTasks(sessionId, after, 20);
      const t = (r.tasks ?? []).find((x) => x.id === taskId);
      if (t && (t.status === "done" || t.status === "error")) return t;
    } catch {
      // 网络抖动:继续重试直到超时。
    }
  }
  return null;
}

// 任务输出渲染:超长截断 + 「查看完整输出」链接(大输出落盘走该接口)。
function TaskOutputBlock({ task, className }: { task: C2Task; className?: string }) {
  const long = task.output.length > OUTPUT_PREVIEW || !!task.output_path;
  return (
    <div className={cn("min-w-0", className)}>
      <pre className="overflow-x-auto font-mono text-xs leading-relaxed whitespace-pre-wrap break-all">
        {long ? task.output.slice(0, OUTPUT_PREVIEW) : task.output || "(无输出)"}
        {long ? "…\n" : ""}
      </pre>
      {long ? (
        <a
          href={`/api/c2/tasks/${task.id}/output`}
          target="_blank"
          rel="noreferrer"
          className="text-blue-600 inline-flex items-center gap-1 text-xs hover:underline"
        >
          <FileTextIcon className="size-3" />
          查看完整输出({fmtBytes(task.output.length > 0 ? task.output.length : num(task.output_size))})
        </a>
      ) : null}
    </div>
  );
}

// ── 交互(命令-响应式通道)───────────────────────────────────────────────────

function InteractTab({ session }: { session: C2Session }) {
  // 会话任务 id → 最新已知状态。乐观插入创建返回的 id,轮询用真实数据覆盖。
  const [tasks, setTasks] = React.useState<Map<number, C2Task>>(new Map());
  const [input, setInput] = React.useState("");
  const [sending, setSending] = React.useState(false);
  // afterId 只越过「已终态」的任务:排队/下发中的任务 id 大于 afterId,
  // 会在后续轮询里重复返回,状态更新(queued→sent→done)因此可见。
  const afterIdRef = React.useRef(0);
  // 日志区自动滚底:用户往上翻就暂停跟随,滚回底部恢复。
  const scrollRef = React.useRef<HTMLDivElement | null>(null);
  const pinnedRef = React.useRef(true);

  React.useEffect(() => {
    let alive = true;
    const tick = async () => {
      try {
        const r = await api.c2SessionTasks(session.id, afterIdRef.current, 100);
        if (!alive || r.tasks.length === 0) return;
        setTasks((prev) => {
          const next = new Map(prev);
          for (const t of r.tasks) next.set(t.id, t);
          return next;
        });
        for (const t of r.tasks) {
          if (t.status === "done" || t.status === "error") {
            afterIdRef.current = Math.max(afterIdRef.current, t.id);
          }
        }
      } catch {
        // 轮询失败静默,下一轮再试。
      }
    };
    void tick();
    const iv = setInterval(tick, 2000);
    return () => {
      alive = false;
      clearInterval(iv);
    };
  }, [session.id]);

  // 新内容到达时,若用户停在底部附近则自动滚底。
  React.useEffect(() => {
    const el = scrollRef.current;
    if (el && pinnedRef.current) el.scrollTop = el.scrollHeight;
  });

  const onScroll = () => {
    const el = scrollRef.current;
    if (!el) return;
    pinnedRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 48;
  };

  const send = async () => {
    const cmd = input.trim();
    if (!cmd || sending) return;
    setInput("");
    setSending(true);
    try {
      const r = await api.c2CreateTask(session.id, "shell", cmd);
      const now = new Date().toISOString();
      setTasks((prev) => {
        const next = new Map(prev);
        next.set(r.id, {
          id: r.id,
          session_id: session.id,
          kind: "shell",
          input: cmd,
          output: "",
          output_path: "",
          output_size: 0,
          status: "queued",
          actor: "user",
          created_by: "",
          error: "",
          created_at: now,
        });
        return next;
      });
    } catch (e) {
      toast.error(`下发失败:${(e as Error).message}`);
    } finally {
      setSending(false);
    }
  };

  // 日志只呈现 shell 任务(文件/采集任务的进出在「流水」页签看)。
  const shellTasks = React.useMemo(
    () => [...tasks.values()].filter((t) => t.kind === "shell").sort((a, b) => a.id - b.id),
    [tasks],
  );

  return (
    <div className="flex flex-col gap-3">
      <p className="text-muted-foreground text-xs">
        命令-响应式通道,非交互 PTY:每条命令在目标上独立执行,cd / export 等运行状态不会保留;agent 的指令也走同一条队列。
      </p>
      <div
        ref={scrollRef}
        onScroll={onScroll}
        className="h-[420px] overflow-y-auto rounded-lg border bg-muted/30 p-3"
      >
        {shellTasks.length === 0 ? (
          <p className="text-muted-foreground py-16 text-center text-sm">
            还没有命令记录。在下方输入命令并回车下发;目标机下次心跳时取走执行。
          </p>
        ) : (
          <div className="flex flex-col gap-3">
            {shellTasks.map((t) => {
              const st = c2TaskStatusMeta(String(t.status));
              return (
                <div key={t.id} className="min-w-0">
                  <div className="flex items-baseline gap-2">
                    <span className="text-muted-foreground shrink-0 font-mono text-[10px] tabular-nums" title={fmtTime(t.created_at)}>
                      {fmtTime(t.created_at)}
                    </span>
                    <span className="text-emerald-600 font-mono text-xs font-semibold break-all dark:text-emerald-400">
                      $ {c2TaskInputText(t.input)}
                    </span>
                    {t.actor === "agent" ? (
                      <Badge variant="outline" className="px-1 py-0 text-[10px]">
                        agent
                      </Badge>
                    ) : null}
                  </div>
                  {t.status === "queued" || t.status === "sent" ? (
                    <p className="text-muted-foreground mt-1 pl-1 font-mono text-xs">等待目标回传…({st.label})</p>
                  ) : t.status === "error" ? (
                    <p className="mt-1 pl-1 font-mono text-xs whitespace-pre-wrap break-all text-red-600 dark:text-red-400">
                      {t.error || "执行失败"}
                    </p>
                  ) : (
                    <div className="mt-1 pl-1">
                      <TaskOutputBlock task={t} />
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        )}
      </div>
      <div className="flex items-center gap-2">
        <span className="text-emerald-600 shrink-0 font-mono text-sm font-semibold dark:text-emerald-400">$</span>
        <Input
          className="font-mono text-xs"
          placeholder="输入命令,回车下发(如 whoami / ipconfig)"
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && !e.nativeEvent.isComposing && void send()}
          disabled={session.status === "dead"}
        />
        <Button size="sm" onClick={() => void send()} disabled={sending || !input.trim() || session.status === "dead"}>
          {sending ? <Loader2Icon className="size-3.5 animate-spin" /> : <ChevronRightIcon className="size-3.5" />}
          下发
        </Button>
      </div>
      {session.status === "dead" ? (
        <p className="text-xs text-red-600 dark:text-red-400">会话已终止,不再接受新命令。</p>
      ) : null}
    </div>
  );
}

// ── 文件 ─────────────────────────────────────────────────────────────────────

// beacon files_list 输出的目录项:{n 名称, s 大小, d 是否目录, m 修改时间}。
interface FileEntry {
  n: string;
  s: number;
  d: boolean;
  m: string | number;
}

// 解析浏览结果;JSON 损坏返回 null(把原始输出展示出来便于排查)。
function parseFileEntries(output: string): FileEntry[] | null {
  try {
    const raw = JSON.parse(output) as unknown;
    if (!Array.isArray(raw)) return null;
    return raw
      .map((x): FileEntry => {
        const o = (x ?? {}) as Record<string, unknown>;
        return {
          n: String(o.n ?? ""),
          s: typeof o.s === "number" ? o.s : 0,
          d: o.d === true,
          m: typeof o.m === "string" || typeof o.m === "number" ? o.m : "",
        };
      })
      .filter((x) => x.n);
  } catch {
    return null;
  }
}

// beacon 侧 mtime 可能是 unix 秒 / 毫秒 / ISO 串,统一兜住。
function fileEntryTime(m: string | number): string {
  if (m === "") return "—";
  if (typeof m === "number") {
    const ms = m > 1e12 ? m : m * 1000;
    return fmtTime(new Date(ms).toISOString());
  }
  return fmtTime(m);
}

// waitTransfer 每 2s 拉取会话传输列表,直到指定 transfer 到达终态;超时返回 null。
async function waitTransfer(sessionId: number, transferId: number, timeoutMs = 300_000): Promise<C2Transfer | null> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    await new Promise((r) => setTimeout(r, 2000));
    try {
      const r = await api.c2SessionTransfers(sessionId, 50);
      const t = (r.transfers ?? []).find((x) => x.id === transferId);
      if (t && (t.status === "done" || t.status === "error")) return t;
    } catch {
      // 网络抖动:继续重试直到超时。
    }
  }
  return null;
}

function FilesTab({ session }: { session: C2Session }) {
  const [path, setPath] = React.useState(() => guessFileRoot(session.os));
  const [listing, setListing] = React.useState<{ state: "idle" | "loading" | "error" | "ok"; items: FileEntry[]; message: string }>({
    state: "idle",
    items: [],
    message: "",
  });
  const [browsing, setBrowsing] = React.useState(false);
  const [uploading, setUploading] = React.useState(false);
  const [downloadingPath, setDownloadingPath] = React.useState<string | null>(null);
  const fileRef = React.useRef<HTMLInputElement | null>(null);
  // 本会话传输记录。
  const [transfers, setTransfers] = React.useState<C2Transfer[]>([]);

  const refreshTransfers = React.useCallback(() => {
    api
      .c2SessionTransfers(session.id, 50)
      .then((r) => setTransfers(r.transfers ?? []))
      .catch(() => {});
  }, [session.id]);

  React.useEffect(() => {
    refreshTransfers();
  }, [refreshTransfers]);

  // 有进行中的传输时 3s 轮询刷新记录表。
  React.useEffect(() => {
    if (!transfers.some((t) => t.status === "queued" || t.status === "running")) return;
    const iv = setInterval(refreshTransfers, 3000);
    return () => clearInterval(iv);
  }, [transfers, refreshTransfers]);

  const browse = async (raw?: string) => {
    const target = (raw ?? path).trim() || "/";
    setPath(target);
    setBrowsing(true);
    setListing({ state: "loading", items: [], message: "" });
    try {
      const r = await api.c2FilesList(session.id, target);
      const t = await waitTaskTerminal(session.id, r.task_id);
      if (!t) {
        setListing({ state: "error", items: [], message: "目标超时未回传浏览结果(会话可能已失联)" });
      } else if (t.status === "error") {
        setListing({ state: "error", items: [], message: t.error || "浏览失败" });
      } else {
        const items = parseFileEntries(t.output);
        if (items === null) {
          setListing({ state: "error", items: [], message: `浏览结果无法解析:${t.output.slice(0, 200) || "(空输出)"}` });
        } else {
          // 目录在前,同类按名称排序。
          items.sort((a, b) => (a.d === b.d ? a.n.localeCompare(b.n) : a.d ? -1 : 1));
          setListing({ state: "ok", items, message: "" });
        }
      }
    } catch (e) {
      setListing({ state: "error", items: [], message: (e as Error).message });
    } finally {
      setBrowsing(false);
    }
  };

  const upload = async (file: File) => {
    setUploading(true);
    try {
      const r = await api.c2UploadFile(session.id, file, path);
      toast.info(`上传任务已建立(传输 #${r.transfer_id}),等待目标拉取分片…`);
      const t = await waitTransfer(session.id, r.transfer_id);
      if (t && t.status === "done") {
        toast.success(`已上传:${file.name}`);
        refreshTransfers();
        void browse(path);
      } else {
        toast.error(`上传失败:${t?.error || "超时未完成"}`);
        refreshTransfers();
      }
    } catch (e) {
      toast.error(`上传失败:${(e as Error).message}`);
    } finally {
      setUploading(false);
      if (fileRef.current) fileRef.current.value = "";
    }
  };

  const download = async (remotePath: string) => {
    setDownloadingPath(remotePath);
    try {
      const r = await api.c2DownloadFile(session.id, remotePath);
      toast.info(`下载任务已建立(传输 #${r.transfer_id}),等待目标回传分片…`);
      refreshTransfers();
      const t = await waitTransfer(session.id, r.transfer_id);
      if (t && t.status === "done") {
        toast.success("已取回,开始下载产物");
        window.open(api.c2TransferDownloadUrl(r.transfer_id));
      } else {
        toast.error(`下载失败:${t?.error || "超时未完成"}`);
      }
      refreshTransfers();
    } catch (e) {
      toast.error(`下载失败:${(e as Error).message}`);
    } finally {
      setDownloadingPath(null);
    }
  };

  const parent = parentPath(path);

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <Input
          className="max-w-xl font-mono text-xs"
          placeholder={guessFileRoot(session.os)}
          value={path}
          onChange={(e) => setPath(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && !browsing && void browse()}
        />
        <Button size="sm" onClick={() => void browse()} disabled={browsing || session.status === "dead"}>
          {browsing ? <Loader2Icon className="size-3.5 animate-spin" /> : <FolderIcon className="size-3.5" />}
          {browsing ? "浏览中…" : "浏览"}
        </Button>
        <Button
          variant="outline"
          size="sm"
          onClick={() => fileRef.current?.click()}
          disabled={uploading || session.status === "dead"}
          title={`上传到当前目录:${path}`}
        >
          {uploading ? <Loader2Icon className="size-3.5 animate-spin" /> : <UploadIcon className="size-3.5" />}
          {uploading ? "上传中…" : "上传到当前目录"}
        </Button>
        <input
          ref={fileRef}
          type="file"
          className="hidden"
          onChange={(e) => {
            const f = e.target.files?.[0];
            if (f) void upload(f);
          }}
        />
      </div>

      <Card>
        <CardContent className="px-0 pt-4">
          {listing.state === "idle" ? (
            <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
              <FolderIcon className="size-8 opacity-40" />
              <span>输入远端路径点「浏览」;结果是一条 files_list 任务的回传,目标离线时会一直排队。</span>
            </div>
          ) : listing.state === "loading" ? (
            <p className="text-muted-foreground py-16 text-center text-sm">浏览中…(等待目标心跳回传,最多等 2 分钟)</p>
          ) : listing.state === "error" ? (
            <div className="flex flex-col items-center gap-2 py-16 text-center text-sm">
              <p className="text-red-600 dark:text-red-400">{listing.message}</p>
              <Button variant="outline" size="sm" onClick={() => void browse()}>
                <RefreshCwIcon className="size-3.5" />
                重试
              </Button>
            </div>
          ) : (
            <Table className="table-fixed">
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  {/* 名称列不设宽度:table-fixed 下自动吃掉余量。 */}
                  <TableHead>名称</TableHead>
                  <TableHead className="w-16">类型</TableHead>
                  <TableHead className="w-20">大小</TableHead>
                  <TableHead className="w-36">修改时间</TableHead>
                  <TableHead className="w-16">操作</TableHead>
                </TableRow>
              </TableHeader>
                <TableBody>
                  {parent ? (
                    <TableRow>
                      <TableCell colSpan={5}>
                        <button
                          type="button"
                          className="flex w-full items-center gap-1.5 text-left font-mono text-xs hover:underline"
                          onClick={() => void browse(parent)}
                        >
                          <FolderUpIcon className="size-3.5 shrink-0" />
                          ..
                        </button>
                      </TableCell>
                    </TableRow>
                  ) : null}
                  {listing.items.length === 0 && !parent ? (
                    <TableRow>
                      <TableCell colSpan={5} className="text-muted-foreground py-10 text-center text-xs">
                        目录为空
                      </TableCell>
                    </TableRow>
                  ) : (
                    listing.items.map((it) => {
                      const full = pathJoin(path, it.n);
                      return (
                        <TableRow key={it.n}>
                          <TableCell className="min-w-0">
                            {it.d ? (
                              <button
                                type="button"
                                className="flex w-full items-center gap-1.5 text-left hover:underline"
                                title={full}
                                onClick={() => void browse(full)}
                              >
                                <FolderIcon className="text-muted-foreground size-3.5 shrink-0" />
                                <span className="truncate font-mono text-xs">{it.n}</span>
                              </button>
                            ) : (
                              <span className="flex items-center gap-1.5" title={full}>
                                <FileTextIcon className="text-muted-foreground size-3.5 shrink-0" />
                                <span className="truncate font-mono text-xs">{it.n}</span>
                              </span>
                            )}
                          </TableCell>
                          <TableCell className="text-muted-foreground text-xs">{it.d ? "目录" : "文件"}</TableCell>
                          <TableCell className="text-muted-foreground text-xs tabular-nums">{it.d ? "—" : fmtBytes(it.s)}</TableCell>
                          <TableCell className="text-muted-foreground text-xs tabular-nums">{fileEntryTime(it.m)}</TableCell>
                          <TableCell>
                            {!it.d ? (
                              <Button
                                variant="ghost"
                                size="icon-xs"
                                onClick={() => void download(full)}
                                disabled={downloadingPath !== null || session.status === "dead"}
                                title="下载到平台"
                              >
                                {downloadingPath === full ? (
                                  <Loader2Icon className="size-3.5 animate-spin" />
                                ) : (
                                  <DownloadIcon className="size-3.5" />
                                )}
                              </Button>
                            ) : null}
                          </TableCell>
                        </TableRow>
                      );
                    })
                  )}
                </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {/* ── 本会话传输记录 ── */}
      <Card>
        <CardContent className="px-0 pt-4">
          <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
            <span className="flex items-center gap-1.5 text-sm font-medium">
              <UploadIcon className="text-muted-foreground size-4" />
              传输记录
            </span>
            <span className="text-muted-foreground text-xs">上传(up = 平台 → 目标)与下载(down = 目标 → 平台)的分片传输</span>
            <Button variant="ghost" size="sm" className="ml-auto" onClick={refreshTransfers}>
              <RefreshCwIcon className="size-3.5" />
              刷新
            </Button>
          </div>
          {transfers.length === 0 ? (
            <p className="text-muted-foreground py-10 text-center text-sm">本会话还没有传输记录。</p>
          ) : (
            <Table className="table-fixed">
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="w-16">方向</TableHead>
                  {/* 远端路径列不设宽度:table-fixed 下自动吃掉余量。 */}
                  <TableHead>远端路径</TableHead>
                  <TableHead className="w-32">进度</TableHead>
                  <TableHead className="w-16">状态</TableHead>
                  <TableHead className="w-36">时间</TableHead>
                  <TableHead className="w-16">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {transfers.map((t) => {
                  const done = t.status === "done";
                  const size = num(t.size);
                  const doneSize = num(t.done_size);
                  return (
                    <TableRow key={t.id}>
                      <TableCell className="text-xs">{t.direction === "up" ? "上传" : "下载"}</TableCell>
                      <TableCell className="min-w-0">
                        <span className="block truncate font-mono text-xs" title={t.remote_path}>
                          {t.remote_path || "—"}
                        </span>
                        {t.error ? <span className="block truncate text-xs text-red-600 dark:text-red-400">{t.error}</span> : null}
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs tabular-nums">
                        {size > 0 ? `${fmtBytes(doneSize)} / ${fmtBytes(size)}` : fmtBytes(doneSize)}
                      </TableCell>
                      <TableCell>
                        <Badge
                          variant="outline"
                          className={
                            t.status === "done"
                              ? "bg-emerald-500/15 text-emerald-600 border-emerald-500/30"
                              : t.status === "error"
                                ? "bg-red-500/15 text-red-600 border-red-500/30"
                                : "bg-blue-500/15 text-blue-600 border-blue-500/30"
                          }
                        >
                          {t.status === "done" ? "完成" : t.status === "error" ? "失败" : t.status === "running" ? "传输中" : "排队"}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs tabular-nums">{fmtTime(t.created_at)}</TableCell>
                      <TableCell>
                        {done && t.direction === "down" ? (
                          <Button
                            variant="ghost"
                            size="icon-xs"
                            title="下载产物"
                            onClick={() => window.open(api.c2TransferDownloadUrl(t.id))}
                          >
                            <DownloadIcon className="size-3.5" />
                          </Button>
                        ) : null}
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

// ── 流水(全部任务)──────────────────────────────────────────────────────────

function TasksTab({ session }: { session: C2Session }) {
  const [tasks, setTasks] = React.useState<C2Task[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [kind, setKind] = React.useState(ALL);
  const [page, setPage] = React.useState(1);
  const pageSize = 50;
  // 输出可展开的行集合。
  const [expanded, setExpanded] = React.useState<Set<number>>(new Set());
  const afterIdRef = React.useRef(0);

  React.useEffect(() => {
    let alive = true;
    const tick = async () => {
      try {
        const r = await api.c2SessionTasks(session.id, afterIdRef.current, 200);
        if (!alive) return;
        if (r.tasks.length > 0) {
          setTasks((prev) => {
            const map = new Map(prev.map((t) => [t.id, t]));
            for (const t of r.tasks) map.set(t.id, t);
            return [...map.values()].sort((a, b) => b.id - a.id);
          });
          // 只越过已终态的任务:排队/下发中的任务要在后续轮询里重复返回,
          // 状态变化(queued→sent→done)才能被看见。
          for (const t of r.tasks) {
            if (t.status === "done" || t.status === "error") {
              afterIdRef.current = Math.max(afterIdRef.current, t.id);
            }
          }
        }
      } catch {
        // 轮询失败静默。
      } finally {
        if (alive) setLoading(false);
      }
    };
    void tick();
    const iv = setInterval(tick, 5000);
    return () => {
      alive = false;
      clearInterval(iv);
    };
  }, [session.id]);

  const filtered = React.useMemo(
    () => (kind === ALL ? tasks : tasks.filter((t) => t.kind === kind)),
    [tasks, kind],
  );
  const totalPages = Math.max(1, Math.ceil(filtered.length / pageSize));
  const safePage = Math.min(Math.max(1, page), totalPages);
  const pageItems = filtered.slice((safePage - 1) * pageSize, safePage * pageSize);

  const toggle = (id: number) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  const KINDS = ["shell", "files_list", "upload", "download", "loot", "info", "plugin"];

  return (
    <Card>
      <CardContent className="px-0 pt-4">
        <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
          <span className="flex items-center gap-1.5 text-sm font-medium">
            <HistoryIcon className="text-muted-foreground size-4" />
            任务流水
          </span>
          <span className="text-muted-foreground text-xs">本会话全部指令,含 agent 工具产生的那部分;5s 自动刷新</span>
          <Select
            value={kind}
            onValueChange={(v) => {
              setPage(1);
              setKind(v);
            }}
          >
            <SelectTrigger className="ml-auto w-32">
              <SelectValue placeholder="类型" />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value={ALL}>全部类型</SelectItem>
                {KINDS.map((k) => (
                  <SelectItem key={k} value={k}>
                    {c2TaskKindLabel(k)}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
        </div>
        {loading && tasks.length === 0 ? (
          <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
        ) : filtered.length === 0 ? (
          <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
            <ClockIcon className="size-8 opacity-40" />
            <span>还没有任务记录。</span>
          </div>
        ) : (
          <>
            <Table className="table-fixed">
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="w-36">时间</TableHead>
                  <TableHead className="w-16">操作者</TableHead>
                  <TableHead className="w-20">类型</TableHead>
                  {/* 输入列不设宽度:table-fixed 下自动吃掉余量。 */}
                  <TableHead>输入</TableHead>
                  <TableHead className="w-16">状态</TableHead>
                  <TableHead className="w-16">输出</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {pageItems.map((t) => {
                  const st = c2TaskStatusMeta(String(t.status));
                  const terminal = t.status === "done" || t.status === "error";
                  const open = expanded.has(t.id);
                  return (
                    <React.Fragment key={t.id}>
                      <TableRow>
                        <TableCell className="text-muted-foreground text-xs tabular-nums">{fmtTime(t.created_at)}</TableCell>
                        <TableCell className="text-xs">{t.actor === "agent" ? "agent" : "人工"}</TableCell>
                        <TableCell className="text-xs">{c2TaskKindLabel(String(t.kind))}</TableCell>
                        <TableCell className="min-w-0">
                          <span className="block truncate font-mono text-xs" title={c2TaskInputText(t.input)}>
                            {c2TaskInputText(t.input) || "—"}
                          </span>
                        </TableCell>
                        <TableCell>
                          <Badge variant="outline" className={st.badge}>
                            {st.label}
                          </Badge>
                        </TableCell>
                        <TableCell>
                          <Button
                            variant="ghost"
                            size="icon-xs"
                            disabled={!terminal}
                            title={terminal ? (open ? "收起输出" : "展开输出") : "等待终态"}
                            onClick={() => toggle(t.id)}
                          >
                            {open ? <ChevronDownIcon className="size-3.5" /> : <ChevronRightIcon className="size-3.5" />}
                          </Button>
                        </TableCell>
                      </TableRow>
                      {open ? (
                        <TableRow className="hover:bg-transparent">
                          <TableCell colSpan={6} className="bg-muted/20">
                            {t.status === "error" && t.error ? (
                              <p className="font-mono text-xs whitespace-pre-wrap break-all text-red-600 dark:text-red-400">{t.error}</p>
                            ) : null}
                            <TaskOutputBlock task={t} />
                          </TableCell>
                        </TableRow>
                      ) : null}
                    </React.Fragment>
                  );
                })}
              </TableBody>
            </Table>
            <TablePagination
              page={safePage}
              pageSize={pageSize}
              total={filtered.length}
              pageSizeOptions={[50]}
              onPageChange={setPage}
              onPageSizeChange={() => setPage(1)}
            />
          </>
        )}
      </CardContent>
    </Card>
  );
}

// ── 档案 ─────────────────────────────────────────────────────────────────────

function ProfileTab({ session }: { session: C2Session }) {
  const [collecting, setCollecting] = React.useState(false);

  // 刷新采集:下发 info 任务,beacon 心跳取走后回传主机信息(结果看「流水」页签)。
  const collect = async () => {
    setCollecting(true);
    try {
      await api.c2CreateTask(session.id, "info");
      toast.success("信息采集任务已下发,回传后可在「流水」页签查看");
    } catch (e) {
      toast.error(`下发失败:${(e as Error).message}`);
    } finally {
      setCollecting(false);
    }
  };

  // meta 类型上是必填对象,但后端 nil map 会序列化成 null,这里按 unknown 兜底。
  const metaJson = React.useMemo(() => {
    try {
      const raw = session.meta as unknown;
      return JSON.stringify(raw && typeof raw === "object" ? raw : {}, null, 2);
    } catch {
      return "{}";
    }
  }, [session.meta]);

  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Card>
        <CardContent className="pt-4">
          <div className="mb-2 flex items-center justify-between gap-2">
            <p className="flex items-center gap-1.5 text-sm font-medium">
              <InfoIcon className="text-muted-foreground size-4" />
              会话档案
            </p>
            <Button variant="outline" size="sm" onClick={() => void collect()} disabled={collecting || session.status === "dead"}>
              {collecting ? <Loader2Icon className="size-3.5 animate-spin" /> : <RefreshCwIcon className="size-3.5" />}
              {collecting ? "下发中…" : "刷新采集"}
            </Button>
          </div>
          <Row label="会话 ID">
            <span className="font-mono">#{session.id}</span>
          </Row>
          <Row label="主机名">{session.hostname || "—"}</Row>
          <Row label="用户">{[session.username, session.elevated ? "高权限" : "普通权限"].filter(Boolean).join(" · ")}</Row>
          <Row label="操作系统">{[session.os, session.arch].filter(Boolean).join(" · ") || "—"}</Row>
          <Row label="内网 IP">
            <span className="font-mono">{session.local_ips || "—"}</span>
          </Row>
          <Row label="进程">
            <span className="font-mono">{session.process ? `${session.process} (${session.pid})` : "—"}</span>
          </Row>
          <Row label="归属监听器">
            <span className="font-mono">#{session.listener_id}</span>
          </Row>
          <Row label="承载传输">{c2ProtoMeta(String(session.transport ?? "http")).label}</Row>
          <Row label="连接方向">
            {c2SessionDirection(session.meta) === "bind"
              ? "正向 bind(平台主动连接,见页头「正向连接」)"
              : "反向 reverse(beacon 回连)"}
          </Row>
          <Row label="归属任务">{num(session.task_id) > 0 ? `#${session.task_id}` : "未绑定"}</Row>
          <Row label="标签">{session.labels || "—"}</Row>
        </CardContent>
      </Card>

      <Card>
        <CardContent className="pt-4">
          <p className="mb-2 flex items-center gap-1.5 text-sm font-medium">
            <ClockIcon className="text-muted-foreground size-4" />
            Beacon 标识与节奏
          </p>
          <Row label="Beacon UID">
            <span className="font-mono">{session.beacon_uid || "—"}</span>
          </Row>
          <Row label="心跳间隔">{`${num(session.sleep_sec)} 秒`}</Row>
          <Row label="抖动">{`${num(session.jitter_pct)}%`}</Row>
          <Row label="状态">{c2SessionStatusMeta(String(session.status)).label}</Row>
          <Row label="首次上线">{fmtTime(session.first_seen)}</Row>
          <Row label="最后心跳">{`${relTime(session.last_seen)} · ${fmtTime(session.last_seen)}`}</Row>
        </CardContent>
      </Card>

      <Card className="lg:col-span-2">
        <CardContent className="pt-4">
          <p className="mb-2 flex items-center gap-1.5 text-sm font-medium">
            <FileTextIcon className="text-muted-foreground size-4" />
            扩展元信息(meta)
          </p>
          <pre className="max-h-64 overflow-auto rounded-lg border bg-muted/30 p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap break-all">
            {metaJson}
          </pre>
          <p className="text-muted-foreground mt-2 text-[10px]">
            beacon 注册与 info 任务回传的扩展字段;「刷新采集」会下发 info 任务更新这份内容。
          </p>
        </CardContent>
      </Card>
    </div>
  );
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-3 py-1.5">
      <span className="text-muted-foreground shrink-0 text-xs">{label}</span>
      <span className="min-w-0 truncate text-right text-xs">{children}</span>
    </div>
  );
}
