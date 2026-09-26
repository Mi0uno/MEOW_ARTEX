"use client";

// C2 中心 · 监听器 —— beacon 回连入口的列表页。
//
// 监听器 = 独立端口的信令 server(三期支持 http/ws/tcp/kcp/oss 五种传输协议):
// 启动后等待 beacon 注册,载荷经 <callback>/<url_path>/p/<token> 投递。本页负责
// 监听器的增删改查与启停;会话与任务在「会话」页,载荷生成在「载荷」页。
//
// 载体(二期):本机直听,或选一台 VPS 机器经 SSH 远程端口转发把机器公网口
// 的流量转回本机监听器(契约 §12)。oss 协议(三期)直连云端对象存储,没有
// bind_host/载体概念,改为填写 endpoint/bucket/AK/SK/prefix(契约 §18)。
// 启停失败(端口占用 / SSH 不通最常见)把后端错误原文报出来。
//
// 接口(c2Listeners)没有服务端筛选/分页参数,四件套(搜索/筛选/排序/分页)
// 全部在客户端对全量列表做。

import * as React from "react";

import { useSearchParams } from "next/navigation";

import {
  ChevronDownIcon,
  ChevronUpIcon,
  CopyIcon,
  PencilIcon,
  PlayIcon,
  PlusIcon,
  RadioIcon,
  RefreshCwIcon,
  SearchIcon,
  SquareIcon,
  Trash2Icon,
} from "lucide-react";
import { toast } from "sonner";

import { TablePagination } from "@/components/table-pagination";
import {
  c2ProtoMeta,
  c2VpsStatusLabel,
  C2_PAYLOAD_FORMATS,
  C2_PROTOS,
  C2_PROTO_FORMAT_SUPPORT,
  fmtTime,
  listenerChannelURL,
  num,
  randomURLPath,
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
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { C2Listener, C2Proto, VpsMachine } from "@/lib/types";
import { cn, copyText } from "@/lib/utils";

const PAGE_SIZE = 50;

// Radix Select 的 value="" 是保留值,空语义一律用哨兵串。
const ALL = "__all__";
const ENABLED = "enabled";
const DISABLED = "disabled";

type SortKey = "name" | "created";

// useSearchParams 必须挂在 Suspense 边界内(静态导出约束)。
export default function C2ListenersPage() {
  return (
    <React.Suspense fallback={null}>
      <C2ListenersInner />
    </React.Suspense>
  );
}

function C2ListenersInner() {
  const searchParams = useSearchParams();
  const [listeners, setListeners] = React.useState<C2Listener[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [query, setQuery] = React.useState("");
  const [queryApplied, setQueryApplied] = React.useState("");
  // 启用状态筛选(客户端)。
  const [enabled, setEnabled] = React.useState(() => (searchParams.get("enabled") === "0" ? DISABLED : ALL));
  // 客户端排序(当前页内):默认按创建时间倒序。
  const [sortKey, setSortKey] = React.useState<SortKey>("created");
  const [sortDesc, setSortDesc] = React.useState(true);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);
  // 新建/编辑对话框。
  const [dialogOpen, setDialogOpen] = React.useState(false);
  const [editing, setEditing] = React.useState<C2Listener | null>(null);
  // 行级启停 busy。
  const [busyId, setBusyId] = React.useState<number | null>(null);
  // 删除确认。
  const [removing, setRemoving] = React.useState<C2Listener | null>(null);
  const [deleteBusy, setDeleteBusy] = React.useState(false);

  const load = React.useCallback(() => {
    setLoading(true);
    api
      .c2Listeners()
      .then((r) => {
        setListeners(r.listeners ?? []);
      })
      .catch((e) => {
        setListeners([]);
        toast.error(`加载监听器失败:${(e as Error).message}`);
      })
      .finally(() => setLoading(false));
  }, []);

  React.useEffect(() => {
    load();
  }, [load]);

  // ---- 客户端搜索 / 筛选 / 排序 / 分页 ----
  const filtered = React.useMemo(() => {
    const q = queryApplied.trim().toLowerCase();
    return listeners.filter((l) => {
      if (enabled === ENABLED && !l.enabled) return false;
      if (enabled === DISABLED && l.enabled) return false;
      if (!q) return true;
      return [l.name, l.callback, l.url_path, l.notes, String(l.port)]
        .join(" ")
        .toLowerCase()
        .includes(q);
    });
  }, [listeners, queryApplied, enabled]);

  const sorted = React.useMemo(() => {
    const arr = [...filtered];
    const dir = sortDesc ? -1 : 1;
    if (sortKey === "name") {
      arr.sort((a, b) => (a.name ?? "").localeCompare(b.name ?? "") * dir);
    } else {
      arr.sort((a, b) => (num(a.id) - num(b.id)) * dir);
    }
    return arr;
  }, [filtered, sortKey, sortDesc]);

  const totalPages = Math.max(1, Math.ceil(sorted.length / pageSize));
  const safePage = Math.min(Math.max(1, page), totalPages);
  const pageItems = sorted.slice((safePage - 1) * pageSize, safePage * pageSize);

  // 启停:启动成功 toast 实际监听地址;失败(端口占用常见)把后端错误原文报出来。
  const toggleRun = async (l: C2Listener) => {
    const starting = !l.runtime_addr;
    setBusyId(l.id);
    try {
      if (starting) {
        const r = await api.c2StartListener(l.id);
        toast.success(`「${l.name}」已启动,监听 ${r.addr || `${l.bind_host}:${l.port}`}`);
      } else {
        await api.c2StopListener(l.id);
        toast.success(`「${l.name}」已停止`);
      }
      load();
    } catch (e) {
      toast.error(`${starting ? "启动" : "停止"}失败:${(e as Error).message}`);
    } finally {
      setBusyId(null);
    }
  };

  // 复制回调地址(信道路径根,<callback>/<url_path>)。
  const copyChannel = async (l: C2Listener) => {
    const url = listenerChannelURL(l);
    if (!url) {
      toast.error("该监听器未配置回调地址,请先编辑补上");
      return;
    }
    const ok = await copyText(url);
    if (ok) toast.success(`已复制:${url}`);
    else toast.error("复制失败,请手动选择文本复制");
  };

  const confirmRemove = async () => {
    if (!removing) return;
    setDeleteBusy(true);
    try {
      await api.c2DeleteListener(removing.id);
      toast.success("已删除");
      setRemoving(null);
      load();
    } catch (e) {
      // 有会话的监听器后端回 409:先清掉该监听器下的会话再删。
      toast.error(`删除失败:${(e as Error).message}`);
    } finally {
      setDeleteBusy(false);
    }
  };

  const search = () => {
    setPage(1);
    setQueryApplied(query.trim());
  };

  const SortHead = ({ field, width, children }: { field: SortKey; width?: string; children: React.ReactNode }) => (
    <TableHead className={width}>
      <button
        type="button"
        title="排序"
        onClick={() => {
          if (sortKey === field) setSortDesc((d) => !d);
          else {
            setSortKey(field);
            setSortDesc(field !== "name");
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

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div>
          <h1 className="font-semibold text-xl">C2 中心 · 监听器</h1>
          <p className="text-muted-foreground text-sm">
            beacon 的回连入口:http / ws / tcp / kcp / oss 五种传输协议,启动后等待 beacon 注册,载荷经投递 URL
            下发。生成载荷前必须先把回调地址填好。
          </p>
        </div>
        <Button
          size="sm"
          onClick={() => {
            setEditing(null);
            setDialogOpen(true);
          }}
        >
          <PlusIcon className="size-4" />
          新建监听器
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
                placeholder="搜索名称 / 回调地址 / 备注"
                className="pl-8"
              />
            </div>
            <Button variant="outline" size="sm" onClick={search}>
              搜索
            </Button>
            <Select
              value={enabled}
              onValueChange={(v) => {
                setPage(1);
                setEnabled(v);
              }}
            >
              <SelectTrigger className="w-32">
                <SelectValue placeholder="状态" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value={ALL}>全部状态</SelectItem>
                  <SelectItem value={ENABLED}>启用</SelectItem>
                  <SelectItem value={DISABLED}>停用</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
            <Button variant="ghost" size="sm" onClick={load} title="重新加载">
              <RefreshCwIcon className="size-3.5" />
              刷新
            </Button>
            <span className="text-muted-foreground text-xs">搜索 / 筛选 / 排序在本页数据上进行</span>
            <span className="text-muted-foreground ml-auto text-xs tabular-nums">
              {queryApplied || enabled !== ALL ? `命中 ${filtered.length} / ` : ""}共 {listeners.length} 个
            </span>
          </div>

          {loading && listeners.length === 0 ? (
            <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
          ) : pageItems.length === 0 ? (
            <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
              <RadioIcon className="size-8 opacity-40" />
              <span>
                {queryApplied || enabled !== ALL
                  ? "没有匹配的监听器"
                  : "还没有监听器。点「新建监听器」创建第一个回连入口,启动后即可生成载荷投递。"}
              </span>
            </div>
          ) : (
            <>
              <Table className="table-fixed">
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    {/* 名称列不设宽度:table-fixed 下自动吃掉余量。
                        固定列合计 688px(64+112+96+48+80+64+112+112),常规容器下余量充足;
                        再加列必须先瘦身其他列,否则名称列会被压到看不见。 */}
                    <SortHead field="name">名称</SortHead>
                    <TableHead className="w-16">协议</TableHead>
                    <TableHead className="w-28">回调地址</TableHead>
                    <TableHead className="w-24">载体</TableHead>
                    <TableHead className="w-12">会话</TableHead>
                    <TableHead className="w-20">启用状态</TableHead>
                    <TableHead className="w-16">备注</TableHead>
                    <SortHead field="created" width="w-28">
                      创建时间
                    </SortHead>
                    <TableHead className="w-28">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {pageItems.map((l) => {
                    const running = !!l.runtime_addr;
                    const pm = c2ProtoMeta(String(l.proto ?? "http"));
                    return (
                      <TableRow key={l.id}>
                        <TableCell className="min-w-0">
                          <span className="block w-full truncate font-medium" title={l.name}>
                            {l.name}
                          </span>
                          <span className="text-muted-foreground block truncate font-mono text-xs" title={`${l.bind_host}:${l.port}`}>
                            {l.bind_host}:{l.port > 0 ? l.port : "自动"}
                          </span>
                        </TableCell>
                        <TableCell>
                          <Badge
                            variant="outline"
                            className={pm.badge}
                            title={`${pm.label}${pm.desc ? ` · ${pm.desc}` : ""}`}
                          >
                            <pm.Icon className="size-2.5" />
                            {pm.label}
                          </Badge>
                        </TableCell>
                        <TableCell className="min-w-0">
                          {l.callback ? (
                            <button
                              type="button"
                              className="block w-full truncate text-left font-mono text-xs text-blue-600 hover:underline"
                              title={`点击复制信道路径:${listenerChannelURL(l)}`}
                              onClick={() => void copyChannel(l)}
                            >
                              {l.callback}
                              <span className="text-muted-foreground">/{l.url_path}</span>
                            </button>
                          ) : (
                            <span className="text-amber-600 text-xs dark:text-amber-400" title="生成载荷前必须先填回调地址">
                              未配置回调地址
                            </span>
                          )}
                        </TableCell>
                        <TableCell>
                          {l.host_kind === "vps" ? (
                            <Badge
                              variant="outline"
                              className="border-violet-500/30 bg-violet-500/15 text-violet-600"
                              title={`VPS 载体:${l.vps_machine_name || `机器 #${l.vps_machine_id}`} · 公网侧转发端口 ${l.remote_port}`}
                            >
                              <span className="max-w-20 truncate">{l.vps_machine_name || `机器 #${l.vps_machine_id}`}</span>
                            </Badge>
                          ) : (
                            <span className="text-muted-foreground text-xs">本机</span>
                          )}
                        </TableCell>
                        <TableCell className="tabular-nums">
                          {num(l.session_count) > 0 ? (
                            <span>{num(l.session_count)}</span>
                          ) : (
                            <span className="text-muted-foreground">0</span>
                          )}
                        </TableCell>
                        <TableCell>
                          <div className="flex flex-col items-start gap-0.5">
                            <Badge
                              variant="outline"
                              className={
                                l.enabled
                                  ? "bg-emerald-500/15 text-emerald-600 border-emerald-500/30"
                                  : "bg-muted text-muted-foreground"
                              }
                            >
                              {l.enabled ? "启用" : "停用"}
                            </Badge>
                            <span
                              className={cn(
                                "inline-flex items-center gap-1 text-[10px]",
                                running ? "text-emerald-600 dark:text-emerald-400" : "text-muted-foreground",
                              )}
                              title={running ? `运行中:${l.runtime_addr}` : "未运行"}
                            >
                              <span className={cn("size-1.5 rounded-full", running ? "bg-emerald-500" : "bg-muted-foreground/40")} />
                              {running ? "运行中" : "未运行"}
                            </span>
                          </div>
                        </TableCell>
                        <TableCell className="min-w-0">
                          <span className="text-muted-foreground block truncate text-xs" title={l.notes}>
                            {l.notes || "—"}
                          </span>
                        </TableCell>
                        <TableCell className="text-muted-foreground text-xs tabular-nums">{fmtTime(l.created_at)}</TableCell>
                        <TableCell>
                          <div className="flex items-center gap-0.5">
                            <Button
                              variant="ghost"
                              size="icon-xs"
                              onClick={() => void toggleRun(l)}
                              disabled={busyId === l.id}
                              title={running ? "停止监听" : "启动监听"}
                            >
                              {busyId === l.id ? (
                                <RefreshCwIcon className="size-3.5 animate-spin" />
                              ) : running ? (
                                <SquareIcon className="size-3.5" />
                              ) : (
                                <PlayIcon className="size-3.5" />
                              )}
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon-xs"
                              onClick={() => void copyChannel(l)}
                              disabled={!l.callback}
                              title="复制回调地址(信道路径)"
                            >
                              <CopyIcon className="size-3.5" />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon-xs"
                              onClick={() => {
                                setEditing(l);
                                setDialogOpen(true);
                              }}
                              title="编辑(运行中改端口 / 信道路径需先停止)"
                            >
                              <PencilIcon className="size-3.5" />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon-xs"
                              onClick={() => setRemoving(l)}
                              title="删除(有会话时需先清会话)"
                            >
                              <Trash2Icon className="text-muted-foreground size-3.5" />
                            </Button>
                          </div>
                        </TableCell>
                      </TableRow>
                    );
                  })}
                </TableBody>
              </Table>
              <TablePagination
                page={safePage}
                pageSize={pageSize}
                total={sorted.length}
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

      <C2ListenerDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        listener={editing}
        onSaved={() => {
          setDialogOpen(false);
          setEditing(null);
          load();
        }}
      />

      {/* 删除确认:有会话的监听器会被后端 409 拒绝,文案写明先清会话。 */}
      <AlertDialog open={!!removing} onOpenChange={(o) => !o && setRemoving(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>确认删除监听器</AlertDialogTitle>
            <AlertDialogDescription>
              将删除监听器「{removing?.name}」。仍有会话的监听器会被拒绝删除——请先终止其名下全部会话;
              已生成的载荷将随之失效。此操作不可撤销。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleteBusy}>取消</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault();
                void confirmRemove();
              }}
              disabled={deleteBusy}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {deleteBusy ? "删除中…" : "确认删除"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

// C2ListenerDialog 新建 / 编辑监听器。
// 三期(契约 §22):先选传输协议(http/ws/tcp/kcp/oss 五张协议卡)——
//   kcp:提示「载荷仅支持 py/gosrc」;kcp 走 UDP,与 VPS 载体(SSH TCP 转发)不兼容,给警示;
//   oss:直连云端对象存储,无 bind_host/端口/信道路径/载体概念,改为填
//        endpoint/bucket/AK/SK/prefix——凭据密封存储永不回显,编辑已配置的监听器时
//        显示「已配置」占位,留空 = 不修改。
// port 留空 = 0(启动时自动分配);url_path 新建时自动生成、可改;
// callback 是写进载荷的回调地址,local 载体必填;vps 载体(二期)可选机器 + 公网侧
// 转发端口,回调地址按机器 host:端口预填、可改,留空时后端按机器信息放宽校验。
function C2ListenerDialog({
  open,
  onOpenChange,
  listener,
  onSaved,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  listener: C2Listener | null;
  onSaved: () => void;
}) {
  const [name, setName] = React.useState("");
  // 三期:传输协议(默认 http,与一期行为一致)。
  const [proto, setProto] = React.useState<C2Proto>("http");
  const [bindHost, setBindHost] = React.useState("0.0.0.0");
  const [portText, setPortText] = React.useState("");
  const [callback, setCallback] = React.useState("");
  const [urlPath, setUrlPath] = React.useState("");
  const [notes, setNotes] = React.useState("");
  // 载体(二期):local = 平台本机直听;vps = 经 SSH 远程端口转发以 VPS 机器为出入口。
  const [hostKind, setHostKind] = React.useState<"local" | "vps">("local");
  const [machines, setMachines] = React.useState<VpsMachine[]>([]);
  const [machineId, setMachineId] = React.useState("");
  const [remotePortText, setRemotePortText] = React.useState("");
  // 三期 oss 传输配置:密封存储永不回显;编辑已配置监听器时留空 = 不修改。
  const [ossEndpoint, setOssEndpoint] = React.useState("");
  const [ossBucket, setOssBucket] = React.useState("");
  const [ossPrefix, setOssPrefix] = React.useState("");
  const [ossAK, setOssAK] = React.useState("");
  const [ossSK, setOssSK] = React.useState("");
  // 用户手改过回调地址后不再自动预填。
  const [callbackTouched, setCallbackTouched] = React.useState(false);
  const [busy, setBusy] = React.useState(false);

  const isOss = proto === "oss";
  // 编辑的监听器是否已配置过 oss 凭据(接口只回布尔,凭据本体永不回显)。
  const ossConfigured = !!listener?.transport_configured;

  React.useEffect(() => {
    if (!open) return;
    if (listener) {
      setName(listener.name);
      // 老数据 proto 缺省 http;非法值兜底回 http。
      const lp = listener.proto as C2Proto;
      setProto(C2_PROTOS.includes(lp) ? lp : "http");
      setBindHost(listener.bind_host || "0.0.0.0");
      setPortText(listener.port > 0 ? String(listener.port) : "");
      setCallback(listener.callback);
      setUrlPath(listener.url_path);
      setNotes(listener.notes);
      setHostKind(listener.host_kind === "vps" ? "vps" : "local");
      setMachineId(listener.vps_machine_id > 0 ? String(listener.vps_machine_id) : "");
      setRemotePortText(listener.remote_port > 0 ? String(listener.remote_port) : "");
    } else {
      setName("");
      setProto("http");
      setBindHost("0.0.0.0");
      setPortText("");
      setCallback("");
      setUrlPath(randomURLPath());
      setNotes("");
      setHostKind("local");
      setMachineId("");
      setRemotePortText("");
    }
    setOssEndpoint("");
    setOssBucket("");
    setOssPrefix("");
    setOssAK("");
    setOssSK("");
    setCallbackTouched(false);
  }, [open, listener]);

  // 选了 VPS 载体才拉机器列表(拿最新在线状态)。
  React.useEffect(() => {
    if (!open || hostKind !== "vps") return;
    let alive = true;
    api
      .vpsMachines()
      .then((r) => {
        if (alive) setMachines(r.machines ?? []);
      })
      .catch(() => {
        if (alive) setMachines([]);
      });
    return () => {
      alive = false;
    };
  }, [open, hostKind]);

  // 回调地址预填:http://<机器host>:<转发端口>。用户手改过就不再动。
  React.useEffect(() => {
    if (!open || hostKind !== "vps" || callbackTouched) return;
    const m = machines.find((x) => x.id === Number(machineId));
    const rp = Number(remotePortText);
    if (m && remotePortText.trim() !== "" && Number.isInteger(rp) && rp > 0) {
      setCallback(`http://${m.host}:${rp}`);
    }
  }, [open, hostKind, machineId, remotePortText, machines, callbackTouched]);

  const submit = async () => {
    const n = name.trim();
    if (!n) {
      toast.error("请填写名称");
      return;
    }
    // ---- oss:直连云端,收集 endpoint/bucket/AK/SK/prefix;其余字段不参与 ----
    if (isOss) {
      const endpoint = ossEndpoint.trim();
      const bucket = ossBucket.trim();
      const ak = ossAK.trim();
      const sk = ossSK.trim();
      if (!(ossConfigured && listener)) {
        // 新建(或编辑但从未配置过)时四项必填;已配置过则留空 = 不修改。
        if (!endpoint) {
          toast.error("请填写 OSS endpoint(如 https://oss-cn-hangzhou.aliyuncs.com)");
          return;
        }
        if (!bucket) {
          toast.error("请填写 OSS bucket 名称");
          return;
        }
        if (!ak || !sk) {
          toast.error("请填写 AccessKey 与 SecretKey");
          return;
        }
      }
      if (endpoint && !/^https?:\/\//i.test(endpoint)) {
        toast.error("endpoint 需带协议,如 https://oss-cn-hangzhou.aliyuncs.com");
        return;
      }
      setBusy(true);
      try {
        // transport_config 只带非空字段:编辑已配置监听器时留空项由后端保留原值。
        const tc: Record<string, string> = {};
        if (endpoint) tc.endpoint = endpoint;
        if (bucket) tc.bucket = bucket;
        if (ak) tc.access_key = ak;
        if (sk) tc.secret_key = sk;
        const prefix = ossPrefix.trim();
        if (prefix) tc.prefix = prefix;
        // oss 的 callback 语义 = beacon 直连的 OSS endpoint(契约 §18)。
        // 宽对象绕过冻结的 C2ListenerInput(v3 字段由后端按契约接收)。
        const input = {
          name: n,
          callback: endpoint || undefined,
          notes: notes.trim(),
          proto,
          host_kind: "local" as const,
          vps_machine_id: 0,
          remote_port: 0,
          ...(Object.keys(tc).length > 0 ? { transport_config: tc } : {}),
        };
        if (listener) await api.c2UpdateListener(listener.id, input);
        else await api.c2CreateListener(input);
        toast.success(listener ? "已保存" : "已创建 oss 监听器(平台侧按 sleep 节奏轮询对象存储)");
        onSaved();
      } catch (e) {
        toast.error(`保存失败:${(e as Error).message}`);
      } finally {
        setBusy(false);
      }
      return;
    }
    // ---- http/ws/tcp/kcp:既有载体与回调校验 ----
    const isVps = hostKind === "vps";
    const machineNum = Number(machineId);
    if (isVps && (!Number.isInteger(machineNum) || machineNum <= 0)) {
      toast.error("VPS 载体必须选择一台机器");
      return;
    }
    let remotePort = 0;
    if (isVps) {
      remotePort = Number(remotePortText);
      if (!Number.isInteger(remotePort) || remotePort <= 0 || remotePort > 65535) {
        toast.error("转发端口必须是 1-65535 的整数(在机器公网侧监听,需放行防火墙)");
        return;
      }
    }
    const cb = callback.trim();
    if (!isVps && !cb) {
      toast.error("请填写回调地址(写进载荷的回调地址,如 http://1.2.3.4:8443)");
      return;
    }
    if (cb && !/^https?:\/\//i.test(cb)) {
      toast.error("回调地址需带协议,如 http://1.2.3.4:8443");
      return;
    }
    const up = urlPath.trim();
    if (!/^[a-zA-Z0-9_-]+$/.test(up)) {
      toast.error("信道路径只能包含字母、数字、下划线与中划线");
      return;
    }
    let port = 0;
    if (portText.trim() !== "") {
      port = Number(portText);
      if (!Number.isInteger(port) || port < 0 || port > 65535) {
        toast.error("端口必须是 0-65535 的整数(留空 = 自动分配)");
        return;
      }
    }
    setBusy(true);
    try {
      const input = {
        name: n,
        bind_host: bindHost.trim() || "0.0.0.0",
        port,
        callback: cb,
        url_path: up,
        notes: notes.trim(),
        host_kind: hostKind,
        vps_machine_id: isVps ? machineNum : 0,
        remote_port: isVps ? remotePort : 0,
        // 三期 v3 扩展(冻结的 C2ListenerInput 尚未声明 proto,后端按契约 §18 接收)。
        proto,
      };
      if (listener) await api.c2UpdateListener(listener.id, input);
      else await api.c2CreateListener(input);
      toast.success(listener ? "已保存" : "已创建,记得启动监听");
      onSaved();
    } catch (e) {
      toast.error(`保存失败:${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[88vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{listener ? "编辑监听器" : "新建监听器"}</DialogTitle>
          <DialogDescription>
            监听器是 beacon 的回连入口(http / ws / tcp / kcp / oss)。目标机执行载荷后按所选协议连回信道路径,受控会话随即上线。
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          {/* ── 传输协议(三期):五张协议卡,选中态高亮 ── */}
          <div className="grid gap-1.5">
            <Label>传输协议</Label>
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
              {C2_PROTOS.map((p) => {
                const m = c2ProtoMeta(p);
                const active = proto === p;
                return (
                  <button
                    key={p}
                    type="button"
                    onClick={() => setProto(p)}
                    title={m.desc}
                    className={cn(
                      "rounded-md border p-2 text-left transition-colors",
                      active
                        ? "border-primary bg-primary/5 ring-primary/30 ring-1"
                        : "hover:border-primary/40 hover:bg-muted/40",
                    )}
                  >
                    <span className="flex items-center gap-1.5">
                      <m.Icon className={cn("size-3.5", active ? "text-primary" : "text-muted-foreground")} />
                      <span className="text-xs font-medium">{m.label}</span>
                    </span>
                    <span className="text-muted-foreground mt-1 block text-[10px] leading-snug">{m.desc}</span>
                  </button>
                );
              })}
            </div>
            {proto === "kcp" ? (
              <p className="rounded-md border border-amber-500/40 bg-amber-500/10 px-2.5 py-1.5 text-[11px] text-amber-700 dark:text-amber-400">
                kcp 传输的载荷仅支持 {C2_PROTO_FORMAT_SUPPORT.kcp.join(" / ")}
                :生成载荷时格式会自动收窄,
                {C2_PAYLOAD_FORMATS.filter((f) => !C2_PROTO_FORMAT_SUPPORT.kcp.includes(f)).join(" / ")} 不可用;
                kcp 走 UDP,与 VPS 载体(SSH TCP 转发)不兼容。
              </p>
            ) : null}
          </div>

          <div className="grid grid-cols-3 gap-3">
            <div className={cn("grid gap-1.5", isOss && "col-span-3")}>
              <Label htmlFor="c2-listener-name">名称</Label>
              <Input
                id="c2-listener-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="例如:边界机-8443"
              />
            </div>
            {!isOss ? (
              <div className="grid gap-1.5">
                <Label htmlFor="c2-listener-port">端口</Label>
                <Input
                  id="c2-listener-port"
                  type="number"
                  min={0}
                  max={65535}
                  value={portText}
                  onChange={(e) => setPortText(e.target.value)}
                  placeholder="自动"
                  className="font-mono"
                />
                <p className="text-muted-foreground text-[10px]">留空 = 自动分配</p>
              </div>
            ) : null}
          </div>

          {isOss ? (
            <>
              {/* ── oss 传输配置(三期):无 bind_host/端口/载体/信道路径概念 ── */}
              <p className="rounded-md border border-cyan-500/40 bg-cyan-500/10 px-2.5 py-1.5 text-[11px] text-cyan-700 dark:text-cyan-400">
                OSS 直连云端:对象存储做双向队列,平台与 beacon 都直连 endpoint,没有
                bind_host / 端口 / 载体 / 信道路径概念;平台按 sleep 节奏轮询对象存储取帧。
              </p>
              <div className="grid gap-3 sm:grid-cols-2">
                <div className="grid gap-1.5">
                  <Label htmlFor="c2-listener-oss-endpoint">Endpoint</Label>
                  <Input
                    id="c2-listener-oss-endpoint"
                    value={ossEndpoint}
                    onChange={(e) => setOssEndpoint(e.target.value)}
                    placeholder={ossConfigured ? "已配置(留空 = 不修改)" : "https://oss-cn-hangzhou.aliyuncs.com"}
                    className="font-mono"
                    autoComplete="off"
                  />
                </div>
                <div className="grid gap-1.5">
                  <Label htmlFor="c2-listener-oss-bucket">Bucket</Label>
                  <Input
                    id="c2-listener-oss-bucket"
                    value={ossBucket}
                    onChange={(e) => setOssBucket(e.target.value)}
                    placeholder={ossConfigured ? "已配置(留空 = 不修改)" : "my-c2-bucket"}
                    className="font-mono"
                    autoComplete="off"
                  />
                </div>
                <div className="grid gap-1.5">
                  <Label htmlFor="c2-listener-oss-ak">AccessKey</Label>
                  <Input
                    id="c2-listener-oss-ak"
                    type="password"
                    value={ossAK}
                    onChange={(e) => setOssAK(e.target.value)}
                    placeholder={ossConfigured ? "已配置(留空 = 不修改)" : ""}
                    className="font-mono"
                    autoComplete="new-password"
                  />
                </div>
                <div className="grid gap-1.5">
                  <Label htmlFor="c2-listener-oss-sk">SecretKey</Label>
                  <Input
                    id="c2-listener-oss-sk"
                    type="password"
                    value={ossSK}
                    onChange={(e) => setOssSK(e.target.value)}
                    placeholder={ossConfigured ? "已配置(留空 = 不修改)" : ""}
                    className="font-mono"
                    autoComplete="new-password"
                  />
                </div>
                <div className="grid gap-1.5 sm:col-span-2">
                  <Label htmlFor="c2-listener-oss-prefix">对象前缀 prefix(可选)</Label>
                  <Input
                    id="c2-listener-oss-prefix"
                    value={ossPrefix}
                    onChange={(e) => setOssPrefix(e.target.value)}
                    placeholder={ossConfigured ? "已配置(留空 = 不修改)" : "如 artex/c2/(帧对象写到前缀之下)"}
                    className="font-mono"
                    autoComplete="off"
                  />
                </div>
              </div>
              <p className="text-muted-foreground text-[10px]">
                AK / SK 与 endpoint 等配置密封存储,接口永不回显;beacon 用同一组凭据直连 OSS 交换帧,
                请确保该凭据仅可访问此 bucket。
              </p>
            </>
          ) : (
            <>
              {/* ── 载体(二期):本机直听 / VPS 机器经 SSH 远程转发做公网出入口 ── */}
              <div className="grid grid-cols-3 gap-3">
                <div className="grid gap-1.5">
                  <Label>载体</Label>
                  <Select value={hostKind} onValueChange={(v) => setHostKind(v === "vps" ? "vps" : "local")}>
                    <SelectTrigger className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectGroup>
                        <SelectItem value="local">本机</SelectItem>
                        <SelectItem value="vps">VPS 机器</SelectItem>
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                  <p className="text-muted-foreground text-[10px]">VPS = 机器公网口经 SSH 转发回本机监听</p>
                </div>
                {hostKind === "vps" ? (
                  <div className="col-span-2 grid gap-1.5">
                    <Label htmlFor="c2-listener-machine">VPS 机器</Label>
                    <Select
                      value={machineId}
                      onValueChange={(v) => {
                        setMachineId(v);
                        setCallbackTouched(false);
                      }}
                    >
                      <SelectTrigger className="w-full">
                        <SelectValue placeholder="选择机器" />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectGroup>
                          {machines.length === 0 ? (
                            <SelectItem value="__none__" disabled>
                              没有可选机器,请先到「VPS 中心」录入
                            </SelectItem>
                          ) : (
                            machines.map((m) => (
                              <SelectItem key={m.id} value={String(m.id)}>
                                {m.name}({m.host}) · {c2VpsStatusLabel(String(m.status))}
                              </SelectItem>
                            ))
                          )}
                        </SelectGroup>
                      </SelectContent>
                    </Select>
                    <p className="text-muted-foreground text-[10px]">标注在线状态;离线机器也可选,启动时 SSH 连不上会报错</p>
                  </div>
                ) : (
                  <div className="col-span-2 grid gap-1.5">
                    <Label htmlFor="c2-listener-bind">绑定地址</Label>
                    <Input
                      id="c2-listener-bind"
                      value={bindHost}
                      onChange={(e) => setBindHost(e.target.value)}
                      placeholder="0.0.0.0"
                      className="font-mono"
                    />
                    <p className="text-muted-foreground text-[10px]">0.0.0.0 = 所有网卡</p>
                  </div>
                )}
              </div>
              {proto === "kcp" && hostKind === "vps" ? (
                <p className="text-[11px] text-amber-600 dark:text-amber-400">
                  注意:kcp 走 UDP,SSH 远程转发仅能转发 TCP——kcp 监听器建议使用本机载体。
                </p>
              ) : null}

              {hostKind === "vps" ? (
                <div className="grid grid-cols-3 gap-3">
                  <div className="grid gap-1.5">
                    <Label htmlFor="c2-listener-remote-port">转发端口</Label>
                    <Input
                      id="c2-listener-remote-port"
                      type="number"
                      min={1}
                      max={65535}
                      value={remotePortText}
                      onChange={(e) => {
                        setRemotePortText(e.target.value);
                        setCallbackTouched(false);
                      }}
                      placeholder="如 8443"
                      className="font-mono"
                    />
                    <p className="text-muted-foreground text-[10px]">在机器公网侧监听,需放行防火墙</p>
                  </div>
                  <div className="col-span-2 grid gap-1.5">
                    <Label htmlFor="c2-listener-callback">回调地址</Label>
                    <Input
                      id="c2-listener-callback"
                      value={callback}
                      onChange={(e) => {
                        setCallback(e.target.value);
                        setCallbackTouched(true);
                      }}
                      placeholder="http://1.2.3.4:8443(按机器与端口预填,可改)"
                      className="font-mono"
                    />
                    <p className="text-muted-foreground text-[10px]">
                      预填 <code className="font-mono">http://&lt;机器 host&gt;:&lt;转发端口&gt;</code>
                      ,可改;目标机必须能访问到
                    </p>
                  </div>
                </div>
              ) : (
                <div className="grid gap-1.5">
                  <Label htmlFor="c2-listener-callback">回调地址</Label>
                  <Input
                    id="c2-listener-callback"
                    value={callback}
                    onChange={(e) => {
                      setCallback(e.target.value);
                      setCallbackTouched(true);
                    }}
                    placeholder="http://1.2.3.4:8443"
                    className="font-mono"
                  />
                  <p className="text-muted-foreground text-[10px]">
                    写进载荷的回调地址,如 http://1.2.3.4:8443(目标机必须能访问到)
                  </p>
                </div>
              )}

              <div className="grid gap-1.5">
                <Label htmlFor="c2-listener-path">信道路径</Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="c2-listener-path"
                    value={urlPath}
                    onChange={(e) => setUrlPath(e.target.value)}
                    className="font-mono"
                  />
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => setUrlPath(randomURLPath())}
                    title="重新生成随机路径"
                  >
                    <RefreshCwIcon className="size-3.5" />
                    重新生成
                  </Button>
                </div>
                <p className="text-muted-foreground text-[10px]">
                  载荷投递与回连都会带上这段随机路径,起到伪装作用;已上线会话不因修改而断开
                </p>
              </div>
            </>
          )}

          <div className="grid gap-1.5">
            <Label htmlFor="c2-listener-notes">备注</Label>
            <Textarea
              id="c2-listener-notes"
              value={notes}
              onChange={(e) => setNotes(e.target.value)}
              rows={2}
              placeholder="用途、目标网段、注意事项…"
            />
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
