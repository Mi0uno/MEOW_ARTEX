"use client";

// C2 中心 · 载荷与免杀 —— 载荷生成、投递与清单页。
//
// 生成:选监听器(回调地址为空的项禁选)+ 格式 + 目标 → 后端渲染源码落盘,
// 返回 oneliner(目标机一行执行命令)。列表支持下载、复制一行命令、复制投递 URL
// (<callback>/<url_path>/p/<token>)与删除。
// 页底「免杀工作台」是二期占位:只读说明 + 徽标,不放任何可点控件。

import * as React from "react";

import { useSearchParams } from "next/navigation";

import {
  ChevronDownIcon,
  ChevronUpIcon,
  DownloadIcon,
  LinkIcon,
  PackageOpenIcon,
  PlusIcon,
  RefreshCwIcon,
  SearchIcon,
  ShieldIcon,
  TerminalIcon,
  Trash2Icon,
} from "lucide-react";
import { toast } from "sonner";

import { TablePagination } from "@/components/table-pagination";
import {
  c2PayloadFormatMeta,
  c2PayloadTargetLabel,
  fmtBytes,
  fmtTime,
  num,
  payloadDeliveryURL,
} from "@/components/c2-shared";
import { CopyButton } from "@/components/copy-button";
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
import type { C2Listener, C2Payload, C2PayloadFormat, C2PayloadTarget } from "@/lib/types";
import { copyText } from "@/lib/utils";

const PAGE_SIZE = 50;

// Radix Select 的 value="" 是保留值,空语义一律用哨兵串。
const ALL = "__all__";

const FORMATS: C2PayloadFormat[] = ["ps1", "bash", "py", "gosrc"];
const TARGETS: C2PayloadTarget[] = ["windows", "linux", "macos", "cross"];

type SortKey = "name" | "created";

// useSearchParams 必须挂在 Suspense 边界内(静态导出约束;?listener_id= 深链)。
export default function C2PayloadsPage() {
  return (
    <React.Suspense fallback={null}>
      <C2PayloadsInner />
    </React.Suspense>
  );
}

function C2PayloadsInner() {
  const searchParams = useSearchParams();
  const [listeners, setListeners] = React.useState<C2Listener[]>([]);
  const [payloads, setPayloads] = React.useState<C2Payload[]>([]);
  const [loading, setLoading] = React.useState(true);
  // 生成表单。
  const [genListener, setGenListener] = React.useState("");
  const [genFormat, setGenFormat] = React.useState<C2PayloadFormat>("ps1");
  const [genTarget, setGenTarget] = React.useState<C2PayloadTarget>("windows");
  const [genName, setGenName] = React.useState("");
  const [generating, setGenerating] = React.useState(false);
  // 列表筛选(客户端搜索 + 服务端 listener_id 过滤)与排序/分页。
  const [filterListener, setFilterListener] = React.useState(() => {
    const v = Number(searchParams.get("listener_id"));
    return Number.isFinite(v) && v > 0 ? String(v) : ALL;
  });
  const [query, setQuery] = React.useState("");
  const [queryApplied, setQueryApplied] = React.useState("");
  const [sortKey, setSortKey] = React.useState<SortKey>("created");
  const [sortDesc, setSortDesc] = React.useState(true);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);
  // 「一行命令」查看对话框。
  const [viewing, setViewing] = React.useState<C2Payload | null>(null);
  // 删除确认。
  const [removing, setRemoving] = React.useState<C2Payload | null>(null);
  const [deleteBusy, setDeleteBusy] = React.useState(false);

  const loadListeners = React.useCallback(() => {
    api
      .c2Listeners()
      .then((r) => setListeners(r.listeners ?? []))
      .catch(() => setListeners([]));
  }, []);

  const load = React.useCallback((lid: string) => {
    setLoading(true);
    api
      .c2Payloads(lid === ALL ? undefined : Number(lid))
      .then((r) => setPayloads(r.payloads ?? []))
      .catch((e) => {
        setPayloads([]);
        toast.error(`加载载荷失败:${(e as Error).message}`);
      })
      .finally(() => setLoading(false));
  }, []);

  React.useEffect(() => {
    loadListeners();
  }, [loadListeners]);

  React.useEffect(() => {
    load(filterListener);
  }, [filterListener, load]);

  const reload = () => load(filterListener);

  const generate = async () => {
    if (!genListener) {
      toast.error("请选择监听器");
      return;
    }
    const l = listeners.find((x) => x.id === Number(genListener));
    if (l && !l.callback) {
      toast.error("该监听器未配置回调地址,请先到「监听器」页补上再生成载荷");
      return;
    }
    setGenerating(true);
    try {
      const r = await api.c2GeneratePayload({
        listener_id: Number(genListener),
        format: genFormat,
        target: genTarget,
        name: genName.trim() || undefined,
      });
      toast.success(`载荷已生成(#${r.id}),可在列表下载或复制投递命令`);
      setGenName("");
      reload();
    } catch (e) {
      // callback 为空等情况后端回 400,错误原文里带原因。
      toast.error(`生成失败:${(e as Error).message}`);
    } finally {
      setGenerating(false);
    }
  };

  const listenerById = React.useMemo(() => {
    const m = new Map<number, C2Listener>();
    for (const l of listeners) m.set(l.id, l);
    return m;
  }, [listeners]);

  const copyDeliveryURL = async (p: C2Payload) => {
    const url = payloadDeliveryURL(listenerById.get(p.listener_id), p.token);
    if (!url) {
      toast.error("所属监听器缺失或未配置回调地址,无法拼出投递 URL");
      return;
    }
    const ok = await copyText(url);
    if (ok) toast.success(`已复制投递 URL:${url}`);
    else toast.error("复制失败,请手动选择文本复制");
  };

  // ---- 客户端搜索 / 排序 / 分页 ----
  const filtered = React.useMemo(() => {
    const q = queryApplied.trim().toLowerCase();
    if (!q) return payloads;
    return payloads.filter((p) => [p.name, p.format, p.target, p.token, p.sha256].join(" ").toLowerCase().includes(q));
  }, [payloads, queryApplied]);

  const sorted = React.useMemo(() => {
    const arr = [...filtered];
    const dir = sortDesc ? -1 : 1;
    if (sortKey === "name") arr.sort((a, b) => (a.name ?? "").localeCompare(b.name ?? "") * dir);
    else arr.sort((a, b) => (num(a.id) - num(b.id)) * dir);
    return arr;
  }, [filtered, sortKey, sortDesc]);

  const totalPages = Math.max(1, Math.ceil(sorted.length / pageSize));
  const safePage = Math.min(Math.max(1, page), totalPages);
  const pageItems = sorted.slice((safePage - 1) * pageSize, safePage * pageSize);

  const confirmRemove = async () => {
    if (!removing) return;
    setDeleteBusy(true);
    try {
      await api.c2DeletePayload(removing.id);
      toast.success("已删除");
      setRemoving(null);
      reload();
    } catch (e) {
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
      <div className="mb-4">
        <h1 className="font-semibold text-xl">C2 中心 · 载荷与免杀</h1>
        <p className="text-muted-foreground text-sm">
          生成 beacon 源码载荷(ps1 / bash / py / go 源码工程),复制一行命令或投递 URL 到目标机执行即可上线。
        </p>
      </div>

      {/* ── 生成表单 ── */}
      <Card className="mb-4">
        <CardContent className="pt-4">
          <p className="mb-3 flex items-center gap-1.5 text-sm font-medium">
            <PlusIcon className="text-muted-foreground size-4" />
            生成载荷
          </p>
          <div className="grid gap-3 lg:grid-cols-5">
            <div className="grid gap-1.5 lg:col-span-2">
              <Label>监听器</Label>
              <Select value={genListener} onValueChange={setGenListener}>
                <SelectTrigger className="w-full">
                  <SelectValue placeholder="选择监听器" />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {listeners.length === 0 ? (
                      <SelectItem value="__none__" disabled>
                        还没有监听器,请先到「监听器」页创建
                      </SelectItem>
                    ) : (
                      listeners.map((l) => (
                        <SelectItem key={l.id} value={String(l.id)} disabled={!l.callback}>
                          {l.name}
                          {l.callback ? `(${l.callback})` : "(未配置回调地址)"}
                        </SelectItem>
                      ))
                    )}
                  </SelectGroup>
                </SelectContent>
              </Select>
              <p className="text-muted-foreground text-[10px]">回调地址为空的监听器不可选:先到「监听器」页补齐回调地址</p>
            </div>
            <div className="grid gap-1.5">
              <Label>格式</Label>
              <Select value={genFormat} onValueChange={(v) => setGenFormat(v as C2PayloadFormat)}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {FORMATS.map((f) => (
                      <SelectItem key={f} value={f}>
                        {f === "gosrc" ? "gosrc(Go 源码工程)" : f}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label>目标平台</Label>
              <Select value={genTarget} onValueChange={(v) => setGenTarget(v as C2PayloadTarget)}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {TARGETS.map((t) => (
                      <SelectItem key={t} value={t}>
                        {c2PayloadTargetLabel(t)}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="c2-payload-name">名称(可选)</Label>
              <div className="flex items-center gap-2">
                <Input
                  id="c2-payload-name"
                  value={genName}
                  onChange={(e) => setGenName(e.target.value)}
                  placeholder="留空自动命名"
                />
                <Button onClick={() => void generate()} disabled={generating}>
                  {generating ? "生成中…" : "生成"}
                </Button>
              </div>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* ── 载荷列表 ── */}
      <Card className="mb-4">
        <CardContent className="px-0 pt-4">
          <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
            <div className="relative w-full sm:max-w-xs">
              <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
              <Input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && search()}
                placeholder="搜索名称 / token / SHA256"
                className="pl-8"
              />
            </div>
            <Button variant="outline" size="sm" onClick={search}>
              搜索
            </Button>
            <Select
              value={filterListener}
              onValueChange={(v) => {
                setPage(1);
                setFilterListener(v);
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
            <Button variant="ghost" size="sm" onClick={reload} title="重新加载">
              <RefreshCwIcon className="size-3.5" />
              刷新
            </Button>
            <span className="text-muted-foreground text-xs">投递前确认监听器已启动</span>
            <span className="text-muted-foreground ml-auto text-xs tabular-nums">
              {queryApplied || filterListener !== ALL ? `命中 ${filtered.length} / ` : ""}共 {payloads.length} 个
            </span>
          </div>

          {loading && payloads.length === 0 ? (
            <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
          ) : pageItems.length === 0 ? (
            <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
              <PackageOpenIcon className="size-8 opacity-40" />
              <span>
                {queryApplied || filterListener !== ALL
                  ? "没有匹配的载荷"
                  : "还没有载荷。用上方表单生成一个:选好监听器与格式,生成后复制一行命令到目标机执行。"}
              </span>
            </div>
          ) : (
            <>
              <Table className="table-fixed">
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    {/* 名称列不设宽度:table-fixed 下自动吃掉余量。
                        固定列合计 592px(64+80+112+80+144+112),常规容器下余量充足。 */}
                    <SortHead field="name">名称</SortHead>
                    <TableHead className="w-16">格式</TableHead>
                    <TableHead className="w-20">目标</TableHead>
                    <TableHead className="w-28">SHA256</TableHead>
                    <TableHead className="w-20">大小</TableHead>
                    <SortHead field="created" width="w-36">
                      创建时间
                    </SortHead>
                    <TableHead className="w-28">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {pageItems.map((p) => {
                    const fm = c2PayloadFormatMeta(String(p.format));
                    const delivery = payloadDeliveryURL(listenerById.get(p.listener_id), p.token);
                    return (
                      <TableRow key={p.id}>
                        <TableCell className="min-w-0">
                          <span className="block w-full truncate font-medium" title={p.name}>
                            {p.name}
                          </span>
                          <span className="text-muted-foreground block truncate text-xs" title={delivery || "投递 URL 不可用"}>
                            {delivery || "投递 URL 不可用(监听器缺失 / 未配回调地址)"}
                          </span>
                        </TableCell>
                        <TableCell>
                          <Badge variant="outline" className={fm.badge}>
                            {fm.label}
                          </Badge>
                        </TableCell>
                        <TableCell className="text-xs">{c2PayloadTargetLabel(String(p.target))}</TableCell>
                        <TableCell className="min-w-0">
                          <span className="block truncate font-mono text-xs" title={p.sha256}>
                            {p.sha256 ? p.sha256.slice(0, 12) : "—"}
                          </span>
                        </TableCell>
                        <TableCell className="text-muted-foreground text-xs tabular-nums">{fmtBytes(p.size)}</TableCell>
                        <TableCell className="text-muted-foreground text-xs tabular-nums">{fmtTime(p.created_at)}</TableCell>
                        <TableCell>
                          <div className="flex items-center gap-0.5">
                            <Button
                              variant="ghost"
                              size="icon-xs"
                              title="下载载荷"
                              onClick={() => window.open(api.c2PayloadDownloadUrl(p.id))}
                            >
                              <DownloadIcon className="size-3.5" />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon-xs"
                              title="查看一行执行命令"
                              onClick={() => setViewing(p)}
                              disabled={!p.oneliner}
                            >
                              <TerminalIcon className="size-3.5" />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon-xs"
                              title="复制投递 URL"
                              onClick={() => void copyDeliveryURL(p)}
                              disabled={!delivery}
                            >
                              <LinkIcon className="size-3.5" />
                            </Button>
                            <Button variant="ghost" size="icon-xs" onClick={() => setRemoving(p)} title="删除载荷">
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

      {/* ── 免杀工作台占位(二期):只读说明,不放任何可点控件 ── */}
      <Card>
        <CardContent className="pt-4">
          <div className="flex flex-wrap items-center gap-2">
            <ShieldIcon className="text-muted-foreground size-4" />
            <p className="text-sm font-medium">免杀工作台</p>
            <Badge variant="outline" className="border-amber-500/30 bg-amber-500/15 text-amber-600">
              二期建设中
            </Badge>
          </div>
          <p className="text-muted-foreground mt-2 text-xs">
            规划中的能力:LLM 对载荷做变形混淆(变量重命名、字符串编码、分段加载)并本地静态自测(特征串 / 熵值),
            产出对抗评分与建议。全程不接外部沙箱——载荷上传第三方服务等于泄漏。Go 编译型载荷同期经指派 VPS 的
            SSH 通道构建,当前版本只提供源码工程(gosrc)。
          </p>
        </CardContent>
      </Card>

      {/* ── 一行命令查看 ── */}
      <Dialog open={!!viewing} onOpenChange={(o) => !o && setViewing(null)}>
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>一行执行命令</DialogTitle>
            <DialogDescription>
              在目标机上执行这条命令,它会拉取载荷源码并就地执行,随后 beacon 按监听器的心跳节奏回连上线。
            </DialogDescription>
          </DialogHeader>
          <Textarea
            readOnly
            rows={4}
            className="min-h-24 font-mono text-xs"
            value={viewing?.oneliner ?? ""}
            onFocus={(e) => e.currentTarget.select()}
          />
          <DialogFooter>
            <CopyButton
              text={viewing?.oneliner ?? ""}
              successMessage="已复制一行命令"
              label="复制一行命令"
            />
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 删除确认 */}
      <AlertDialog open={!!removing} onOpenChange={(o) => !o && setRemoving(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>确认删除载荷</AlertDialogTitle>
            <AlertDialogDescription>
              将删除载荷「{removing?.name}」及其投递令牌:已投递的该载荷将无法再被目标机拉取。此操作不可撤销。
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
