"use client";

// C2 中心 · 监听器 —— beacon HTTP 回连入口的列表页。
//
// 监听器 = 独立端口的 HTTP server:启动后等待 beacon 注册,载荷经
// <callback>/<url_path>/p/<token> 投递。本页负责监听器的增删改查与启停;
// 会话与任务在「会话」页,载荷生成在「载荷」页。
//
// 接口(c2Listeners)没有服务端筛选/分页参数,四件套(搜索/筛选/排序/分页)
// 全部在客户端对全量列表做;启停失败(端口占用最常见)把后端错误原文报出来。

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
import { fmtTime, listenerChannelURL, num, randomURLPath } from "@/components/c2-shared";
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
import type { C2Listener } from "@/lib/types";
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
            beacon 的 HTTP 回连入口:启动后监听 <code className="font-mono text-xs">POST /&lt;信道路径&gt;/e</code>
            ,载荷经投递 URL 下发。生成载荷前必须先把回调地址填好。
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
                        固定列合计 688px(192+64+80+112+128+112),常规容器下余量充足;
                        再加列必须先瘦身其他列,否则名称列会被压到看不见。 */}
                    <SortHead field="name">名称</SortHead>
                    <TableHead className="w-48">回调地址</TableHead>
                    <TableHead className="w-16">会话</TableHead>
                    <TableHead className="w-20">启用状态</TableHead>
                    <TableHead className="w-28">备注</TableHead>
                    <SortHead field="created" width="w-32">
                      创建时间
                    </SortHead>
                    <TableHead className="w-28">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {pageItems.map((l) => {
                    const running = !!l.runtime_addr;
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
// port 留空 = 0(启动时自动分配);url_path 新建时自动生成、可改;
// callback 是写进载荷的回调地址,生成载荷前必填。
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
  const [bindHost, setBindHost] = React.useState("0.0.0.0");
  const [portText, setPortText] = React.useState("");
  const [callback, setCallback] = React.useState("");
  const [urlPath, setUrlPath] = React.useState("");
  const [notes, setNotes] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    if (!open) return;
    if (listener) {
      setName(listener.name);
      setBindHost(listener.bind_host || "0.0.0.0");
      setPortText(listener.port > 0 ? String(listener.port) : "");
      setCallback(listener.callback);
      setUrlPath(listener.url_path);
      setNotes(listener.notes);
    } else {
      setName("");
      setBindHost("0.0.0.0");
      setPortText("");
      setCallback("");
      setUrlPath(randomURLPath());
      setNotes("");
    }
  }, [open, listener]);

  const submit = async () => {
    const n = name.trim();
    if (!n) {
      toast.error("请填写名称");
      return;
    }
    const cb = callback.trim();
    if (!cb) {
      toast.error("请填写回调地址(写进载荷的回调地址,如 http://1.2.3.4:8443)");
      return;
    }
    if (!/^https?:\/\//i.test(cb)) {
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
            监听器是 beacon 的 HTTP 回连入口。目标机执行载荷后会连到回调地址上的信道路径,受控会话随即上线。
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid grid-cols-3 gap-3">
            <div className="col-span-2 grid gap-1.5">
              <Label htmlFor="c2-listener-name">名称</Label>
              <Input
                id="c2-listener-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="例如:边界机-8443"
              />
            </div>
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
          </div>

          <div className="grid grid-cols-3 gap-3">
            <div className="grid gap-1.5">
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
            <div className="col-span-2 grid gap-1.5">
              <Label htmlFor="c2-listener-callback">回调地址</Label>
              <Input
                id="c2-listener-callback"
                value={callback}
                onChange={(e) => setCallback(e.target.value)}
                placeholder="http://1.2.3.4:8443"
                className="font-mono"
              />
              <p className="text-muted-foreground text-[10px]">
                写进载荷的回调地址,如 http://1.2.3.4:8443(目标机必须能访问到)
              </p>
            </div>
          </div>

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
