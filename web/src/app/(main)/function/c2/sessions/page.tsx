"use client";

// C2 中心 · 会话 —— 上线 beacon 主机的列表页。
//
// 列表走服务端筛选(status / listener_id / q)与服务端分页;表头排序是当前页内。
// 行操作:详情(跳 /function/c2/detail?id=)、绑定任务(Dialog 选该会话任务,
// 0 = 解绑)、终止(confirm 后置 dead,beacon 下次心跳自行退出)。
// 支持 ?status=&listener_id= 深链,useSearchParams 包 Suspense。

import * as React from "react";

import Link from "next/link";
import { useSearchParams } from "next/navigation";

import {
  ArrowRightIcon,
  ChevronDownIcon,
  ChevronUpIcon,
  Link2Icon,
  Link2OffIcon,
  MonitorSmartphoneIcon,
  RefreshCwIcon,
  SearchIcon,
  ShieldAlertIcon,
  SkullIcon,
} from "lucide-react";
import { toast } from "sonner";

import { TablePagination } from "@/components/table-pagination";
import {
  C2_SESSION_STATUS_RANK,
  c2DirectionMeta,
  c2ProtoMeta,
  c2SessionDirection,
  c2SessionStatusMeta,
  fmtSleep,
  fmtTime,
  num,
  relTime,
} from "@/components/c2-shared";
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
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { C2Listener, C2Session, Task } from "@/lib/types";
import { cn } from "@/lib/utils";

const PAGE_SIZE = 50;

// Radix Select 的 value="" 是保留值,空语义一律用哨兵串。
const ALL = "__all__";

type SortKey = "hostname" | "status" | "last_seen";

// useSearchParams 必须挂在 Suspense 边界内(静态导出约束)。
export default function C2SessionsPage() {
  return (
    <React.Suspense fallback={null}>
      <C2SessionsInner />
    </React.Suspense>
  );
}

function C2SessionsInner() {
  const searchParams = useSearchParams();
  // 深链:?status=online&listener_id=3。
  const [status, setStatus] = React.useState(() => {
    const s = searchParams.get("status");
    return s === "online" || s === "offline" || s === "dead" ? s : ALL;
  });
  const [listenerId, setListenerId] = React.useState(() => {
    const v = Number(searchParams.get("listener_id"));
    return Number.isFinite(v) && v > 0 ? String(v) : ALL;
  });
  const [listeners, setListeners] = React.useState<C2Listener[]>([]);
  const [sessions, setSessions] = React.useState<C2Session[]>([]);
  const [total, setTotal] = React.useState(0);
  const [query, setQuery] = React.useState("");
  const [queryApplied, setQueryApplied] = React.useState("");
  // 客户端排序(当前页内):默认按心跳倒序(最近活跃在前)。
  const [sortKey, setSortKey] = React.useState<SortKey | "default">("default");
  const [sortDesc, setSortDesc] = React.useState(true);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);
  const [loading, setLoading] = React.useState(true);
  // 绑定任务对话框目标会话。
  const [binding, setBinding] = React.useState<C2Session | null>(null);
  // 终止确认目标会话。
  const [killing, setKilling] = React.useState<C2Session | null>(null);
  const [killBusy, setKillBusy] = React.useState(false);

  const load = React.useCallback((q: string, st: string, lid: string, p: number, size: number) => {
    setLoading(true);
    api
      .c2Sessions({
        q,
        status: st === ALL ? "" : st,
        listenerId: lid === ALL ? undefined : Number(lid),
        limit: size,
        offset: (p - 1) * size,
      })
      .then((r) => {
        setSessions(r.sessions ?? []);
        setTotal(r.total);
      })
      .catch((e) => {
        setSessions([]);
        setTotal(0);
        toast.error(`加载会话失败:${(e as Error).message}`);
      })
      .finally(() => setLoading(false));
  }, []);

  React.useEffect(() => {
    load(queryApplied, status, listenerId, page, pageSize);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status, listenerId, page, pageSize]);

  // 监听器下拉选项(筛选用)。
  React.useEffect(() => {
    api
      .c2Listeners()
      .then((r) => setListeners(r.listeners ?? []))
      .catch(() => setListeners([]));
  }, []);

  const search = () => {
    setPage(1);
    setQueryApplied(query.trim());
    load(query.trim(), status, listenerId, 1, pageSize);
  };
  const reload = () => load(queryApplied, status, listenerId, page, pageSize);

  // ---- 当前页内排序 ----
  const sorted = React.useMemo(() => {
    const arr = [...sessions];
    const dir = sortDesc ? -1 : 1;
    if (sortKey === "hostname") {
      arr.sort((a, b) => (a.hostname ?? "").localeCompare(b.hostname ?? "") * dir);
    } else if (sortKey === "status") {
      arr.sort((a, b) => ((C2_SESSION_STATUS_RANK[String(a.status)] ?? 9) - (C2_SESSION_STATUS_RANK[String(b.status)] ?? 9)) * dir);
    } else if (sortKey === "last_seen") {
      const t = (s: C2Session) => (s.last_seen ? Date.parse(s.last_seen) : 0);
      arr.sort((a, b) => (t(a) - t(b)) * dir);
    }
    return arr;
  }, [sessions, sortKey, sortDesc]);

  const SortHead = ({ field, width, children }: { field: SortKey; width?: string; children: React.ReactNode }) => (
    <TableHead className={width}>
      <button
        type="button"
        title="排序(当前页内)"
        onClick={() => {
          if (sortKey === field) setSortDesc((d) => !d);
          else {
            setSortKey(field);
            setSortDesc(field !== "hostname");
          }
        }}
        className="hover:text-foreground inline-flex items-center gap-1"
      >
        {children}
        {sortKey === field ? (
          sortDesc ? (
            <ChevronDownIcon className="size-3" />
          ) : (
            <ChevronUpIcon className="size-3" />
          )
        ) : null}
      </button>
    </TableHead>
  );

  const listenerName = (id: number) => listeners.find((l) => l.id === num(id))?.name;

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div>
          <h1 className="font-semibold text-xl">C2 中心 · 会话</h1>
          <p className="text-muted-foreground text-sm">
            上线 beacon 主机的注册表:心跳、权限、心跳节奏与归属任务。进入会话可下发命令、浏览文件与查看流水。
          </p>
        </div>
        <Button variant="ghost" size="sm" onClick={reload} title="重新加载">
          <RefreshCwIcon className="size-3.5" />
          刷新
        </Button>
      </div>

      <Card>
        <CardContent className="px-0 pt-4">
          <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
            <div className="relative w-full sm:max-w-xs">
              <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
              <Input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && search()}
                placeholder="搜索主机 / 用户 / 系统 / IP"
                className="pl-8"
              />
            </div>
            <Button variant="outline" size="sm" onClick={search}>
              搜索
            </Button>
            <Select
              value={status}
              onValueChange={(v) => {
                setPage(1);
                setStatus(v);
              }}
            >
              <SelectTrigger className="w-28">
                <SelectValue placeholder="状态" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value={ALL}>全部状态</SelectItem>
                  <SelectItem value="online">在线</SelectItem>
                  <SelectItem value="offline">失联</SelectItem>
                  <SelectItem value="dead">终止</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
            <Select
              value={listenerId}
              onValueChange={(v) => {
                setPage(1);
                setListenerId(v);
              }}
            >
              <SelectTrigger className="w-44">
                <SelectValue placeholder="监听器" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value={ALL}>全部监听器</SelectItem>
                  {listeners.map((l) => (
                    <SelectItem key={l.id} value={String(l.id)}>
                      {l.name}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            <span className="text-muted-foreground text-xs">表头排序作用于当前页</span>
            <span className="text-muted-foreground ml-auto text-xs tabular-nums">
              {queryApplied || status !== ALL || listenerId !== ALL ? `命中 ${total} / ` : ""}共 {total} 个会话
            </span>
          </div>

          {loading && sessions.length === 0 ? (
            <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
          ) : sessions.length === 0 ? (
            <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
              <MonitorSmartphoneIcon className="size-8 opacity-40" />
              <span>
                {queryApplied || status !== ALL || listenerId !== ALL
                  ? "没有匹配的会话"
                  : "还没有会话上线。先启动一个监听器并投递载荷,目标机执行后就会出现在这里。"}
              </span>
            </div>
          ) : (
            <>
              <Table className="table-fixed">
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    {/* 主机列不设宽度:table-fixed 下自动吃掉余量。
                        固定列合计 736px(64+96+96+112+64+64+80+64+96),常规容器下余量充足;
                        再加列必须先瘦身其他列,否则主机列会被压到看不见。 */}
                    <SortHead field="status" width="w-16">
                      状态
                    </SortHead>
                    <SortHead field="hostname">主机</SortHead>
                    <TableHead className="w-24">用户</TableHead>
                    <TableHead className="w-24">系统 / 架构</TableHead>
                    <TableHead className="w-28">内网 IP</TableHead>
                    <TableHead className="w-16">传输</TableHead>
                    <TableHead className="w-16">Sleep</TableHead>
                    <SortHead field="last_seen" width="w-20">
                      心跳
                    </SortHead>
                    <TableHead className="w-16">任务</TableHead>
                    <TableHead className="w-24">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {sorted.map((s) => {
                    const meta = c2SessionStatusMeta(String(s.status));
                    const taskID = num(s.task_id);
                    return (
                      <TableRow key={s.id}>
                        <TableCell>
                          <span
                            className="inline-flex items-center gap-1.5 text-xs"
                            title={`${meta.label} · 心跳 ${relTime(s.last_seen)}`}
                          >
                            <span className={cn("size-2 shrink-0 rounded-full", meta.dot)} />
                            {meta.label}
                          </span>
                        </TableCell>
                        {/* 主机名即入口:点击进入详情页;第二行给监听器来源与进程摘要。 */}
                        <TableCell className="min-w-0">
                          <Link
                            href={`/function/c2/detail?id=${s.id}`}
                            className="block w-full truncate text-left font-medium hover:underline"
                            title={s.hostname}
                          >
                            {s.hostname || `会话 #${s.id}`}
                          </Link>
                          <span
                            className="text-muted-foreground block truncate text-xs"
                            title={[listenerName(s.listener_id), s.process ? `${s.process}(${s.pid})` : ""].filter(Boolean).join(" · ")}
                          >
                            {[listenerName(s.listener_id) ?? `监听器 #${s.listener_id}`, s.process ? `${s.process}(${s.pid})` : ""]
                              .filter(Boolean)
                              .join(" · ") || "—"}
                          </span>
                        </TableCell>
                        <TableCell className="min-w-0">
                          <span className="flex items-center gap-1">
                            <span className="min-w-0 truncate text-xs" title={s.username}>
                              {s.username || "—"}
                            </span>
                            {s.elevated ? (
                              <Badge variant="outline" className="border-red-500/30 bg-red-500/15 px-1 text-[10px] text-red-600" title="高权限">
                                <ShieldAlertIcon className="size-2.5" />
                                高
                              </Badge>
                            ) : null}
                          </span>
                        </TableCell>
                        <TableCell className="min-w-0">
                          <span className="block truncate text-xs" title={[s.os, s.arch].filter(Boolean).join(" · ")}>
                            {[s.os, s.arch].filter(Boolean).join(" · ") || "—"}
                          </span>
                        </TableCell>
                        <TableCell className="min-w-0">
                          <span className="block truncate font-mono text-xs" title={s.local_ips}>
                            {s.local_ips || "—"}
                          </span>
                        </TableCell>
                        {/* 三期:承载传输徽标(ws/tcp/kcp/oss/http)+ 正向(bind)方向徽标。 */}
                        <TableCell>
                          <div className="flex flex-col items-start gap-0.5">
                            {(() => {
                              const pm = c2ProtoMeta(String(s.transport ?? "http"));
                              return (
                                <Badge
                                  variant="outline"
                                  className={pm.badge}
                                  title={`承载传输:${pm.label}${pm.desc ? ` · ${pm.desc}` : ""}`}
                                >
                                  <span className="max-w-12 truncate font-mono text-[10px]">
                                    {String(s.transport ?? "http")}
                                  </span>
                                </Badge>
                              );
                            })()}
                            {c2SessionDirection(s.meta) === "bind" ? (
                              <Badge
                                variant="outline"
                                className={c2DirectionMeta("bind").badge}
                                title={`连接方向:${c2DirectionMeta("bind").desc}`}
                              >
                                {c2DirectionMeta("bind").label}
                              </Badge>
                            ) : null}
                          </div>
                        </TableCell>
                        <TableCell className="text-muted-foreground text-xs tabular-nums" title={`抖动 ${num(s.jitter_pct)}%`}>
                          {fmtSleep(s.sleep_sec, 0)}
                        </TableCell>
                        <TableCell className="text-muted-foreground text-xs tabular-nums" title={fmtTime(s.last_seen)}>
                          {relTime(s.last_seen)}
                        </TableCell>
                        <TableCell className="text-xs">
                          {taskID > 0 ? (
                            <span className="text-blue-600 tabular-nums" title={`绑定任务 #${taskID}`}>
                              #{taskID}
                            </span>
                          ) : (
                            <span className="text-muted-foreground">—</span>
                          )}
                        </TableCell>
                        <TableCell>
                          <div className="flex items-center gap-0.5">
                            <Button asChild variant="ghost" size="icon-xs" title="进入会话详情">
                              <Link href={`/function/c2/detail?id=${s.id}`} aria-label="进入会话详情">
                                <ArrowRightIcon className="size-3.5" />
                              </Link>
                            </Button>
                            <Button variant="ghost" size="icon-xs" onClick={() => setBinding(s)} title="绑定 / 解绑任务">
                              {taskID > 0 ? <Link2OffIcon className="size-3.5" /> : <Link2Icon className="size-3.5" />}
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon-xs"
                              onClick={() => setKilling(s)}
                              disabled={s.status === "dead"}
                              title={s.status === "dead" ? "已终止" : "终止会话"}
                            >
                              <SkullIcon className={s.status === "dead" ? "text-muted-foreground/50 size-3.5" : "size-3.5"} />
                            </Button>
                          </div>
                        </TableCell>
                      </TableRow>
                    );
                  })}
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

      <BindTaskDialog
        session={binding}
        onOpenChange={(o) => !o && setBinding(null)}
        onSaved={() => {
          setBinding(null);
          reload();
        }}
      />

      {/* 终止确认:置 dead 后由 beacon 下次心跳自行退出,不可恢复。 */}
      <AlertDialog open={!!killing} onOpenChange={(o) => !o && setKilling(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>确认终止会话</AlertDialogTitle>
            <AlertDialogDescription>
              将终止「{killing?.hostname || `会话 #${killing?.id}` }」:会话置为 dead,beacon 在下次心跳时收到退出指令并自行退出。
              目标机上的进程由 beacon 自行结束,该操作不可恢复。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={killBusy}>取消</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault();
                if (!killing) return;
                setKillBusy(true);
                api
                  .c2KillSession(killing.id)
                  .then(() => {
                    toast.success("已下发终止指令,beacon 将在下次心跳时退出");
                    setKilling(null);
                    reload();
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

// BindTaskDialog 绑定 / 解绑任务:任务下拉列出该会话的任务(增量接口 afterId=0
// 取最近一段),选「解绑」发 task_id=0。
function BindTaskDialog({
  session,
  onOpenChange,
  onSaved,
}: {
  session: C2Session | null;
  onOpenChange: (v: boolean) => void;
  onSaved: () => void;
}) {
  const [tasks, setTasks] = React.useState<Task[]>([]);
  const [taskId, setTaskId] = React.useState("0");
  const [loading, setLoading] = React.useState(false);
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    if (!session) return;
    setTaskId(String(num(session.task_id)));
    setLoading(true);
    // 绑定目标是「平台任务」(session.task_id 外键指向 tasks 表),不是会话自己的 C2 任务流水。
    api
      .tasks()
      .then((r) => setTasks(r.tasks ?? []))
      .catch((e) => {
        setTasks([]);
        toast.error(`加载任务失败:${(e as Error).message}`);
      })
      .finally(() => setLoading(false));
  }, [session]);

  const submit = async () => {
    if (!session) return;
    setBusy(true);
    try {
      const id = Number(taskId);
      await api.c2BindSession(session.id, Number.isFinite(id) ? id : 0);
      toast.success(id > 0 ? `已绑定任务 #${id}` : "已解绑任务");
      onSaved();
    } catch (e) {
      toast.error(`绑定失败:${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={!!session} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>绑定任务</DialogTitle>
          <DialogDescription>
            把会话挂到一个业务任务上,便于按任务归集后渗透动作;选「解绑」恢复未绑定状态。
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-1.5">
          <Select value={taskId} onValueChange={setTaskId} disabled={loading}>
            <SelectTrigger className="w-full">
              <SelectValue placeholder={loading ? "加载任务中…" : "选择任务"} />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value="0">解绑(不归属任何任务)</SelectItem>
                {tasks.map((t) => (
                  <SelectItem key={t.id} value={String(t.id)}>
                    #{t.id} {(t.name || t.description || "未命名任务").slice(0, 40)}
                  </SelectItem>
                ))}
                {!loading && tasks.length === 0 ? (
                  <SelectItem value="__none__" disabled>
                    平台还没有任何任务
                  </SelectItem>
                ) : null}
              </SelectGroup>
            </SelectContent>
          </Select>
          <p className="text-muted-foreground text-xs">归属到平台任务后,会话可在任务/企业维度被筛选与归档。</p>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy}>
            取消
          </Button>
          <Button disabled={busy || loading} onClick={() => void submit()}>
            {busy ? "保存中…" : "保存"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
