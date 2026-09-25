"use client";

// C2 中心 · 战利品 —— 后渗透采集成果的集中入库页。
//
// kind 页签(全部/凭据/哈希/文件/截图/其他,可点带计数,aria-pressed 标选中)+
// 搜索 + 服务端筛选分页。列表里 content 恒空串(credential/hash 密文存储),
// 「查看」走详情接口取明文,弹窗展示。支持 ?kind=&q=&session_id= 深链
// (会话详情未来可带 session_id 跳转),useSearchParams 包 Suspense。
//
// 转发现(二期,契约 §15):把一条战利品登记为发现(复用既有发现链路),
// 需要会话已绑定任务(否则 400);已转换的行显示「发现 #id」徽标可跳发现详情。
// 后端回填 c2_loot.finding_id;冻结的 C2Loot 类型尚未声明该字段,这里用本地
// 交叉类型兜底(字段缺失时不显示徽标,重复转换由 409 路径提示)。

import * as React from "react";

import Link from "next/link";
import { useSearchParams } from "next/navigation";

import {
  ArrowUpRightIcon,
  EyeIcon,
  GemIcon,
  PlusIcon,
  RefreshCwIcon,
  SearchIcon,
  Trash2Icon,
  XIcon,
} from "lucide-react";
import { toast } from "sonner";

import { TablePagination } from "@/components/table-pagination";
import { C2_LOOT_KINDS, c2LootKindMeta, fmtTime, num } from "@/components/c2-shared";
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
import type { C2Loot, C2LootKind } from "@/lib/types";
import { cn } from "@/lib/utils";

const PAGE_SIZE = 50;

// 后端在 loot 行上回填 finding_id(契约 §15),冻结的 C2Loot 尚未声明;本地交叉类型兜底。
type C2LootRow = C2Loot & { finding_id?: number };

type KindTab = "all" | C2LootKind;

const KIND_TABS: { value: KindTab; label: string }[] = [
  { value: "all", label: "全部" },
  { value: "credential", label: "凭据" },
  { value: "hash", label: "哈希" },
  { value: "file", label: "文件" },
  { value: "screenshot", label: "截图" },
  { value: "other", label: "其他" },
];

// useSearchParams 必须挂在 Suspense 边界内(静态导出约束)。
export default function C2LootPage() {
  return (
    <React.Suspense fallback={null}>
      <C2LootInner />
    </React.Suspense>
  );
}

function C2LootInner() {
  const searchParams = useSearchParams();
  const [kind, setKind] = React.useState<KindTab>(() => {
    const k = searchParams.get("kind");
    return (C2_LOOT_KINDS as string[]).includes(k ?? "") ? (k as C2LootKind) : "all";
  });
  // 会话深链:?session_id=123(由会话详情等入口带过来),空串 = 不限。
  const [sessionIdText, setSessionIdText] = React.useState(() => {
    const v = Number(searchParams.get("session_id"));
    return Number.isFinite(v) && v > 0 ? String(v) : "";
  });
  const [loot, setLoot] = React.useState<C2Loot[]>([]);
  const [total, setTotal] = React.useState(0);
  // 各 kind 计数(与当前筛选无关的全量口径)。
  const [counts, setCounts] = React.useState<Record<string, number>>({});
  const [query, setQuery] = React.useState("");
  const [queryApplied, setQueryApplied] = React.useState("");
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);
  const [loading, setLoading] = React.useState(true);
  // 手动登记对话框。
  const [creating, setCreating] = React.useState(false);
  // 查看对话框(详情接口取明文)。
  const [viewingId, setViewingId] = React.useState<number | null>(null);
  // 删除确认。
  const [removing, setRemoving] = React.useState<C2Loot | null>(null);
  const [deleteBusy, setDeleteBusy] = React.useState(false);
  // 转发现行级 busy。
  const [convertingId, setConvertingId] = React.useState<number | null>(null);

  const sessionId = sessionIdText ? Number(sessionIdText) : 0;

  const load = React.useCallback((k: string, q: string, sid: number, p: number, size: number) => {
    setLoading(true);
    api
      .c2Loot({
        kind: k === "all" ? "" : k,
        q,
        sessionId: sid > 0 ? sid : undefined,
        limit: size,
        offset: (p - 1) * size,
      })
      .then((r) => {
        setLoot(r.loot ?? []);
        setTotal(r.total);
      })
      .catch((e) => {
        setLoot([]);
        setTotal(0);
        toast.error(`加载战利品失败:${(e as Error).message}`);
      })
      .finally(() => setLoading(false));
  }, []);

  // 各 kind 计数:列表接口按 kind 过滤后取 total(limit=1 的小请求),
  // 接口没有一次返回全量计数的字段,这里并行打 6 个小查询换页签计数。
  const loadCounts = React.useCallback((sid: number) => {
    const keys: KindTab[] = ["all", ...C2_LOOT_KINDS];
    keys.forEach((k) => {
      api
        .c2Loot({ kind: k === "all" ? "" : k, sessionId: sid > 0 ? sid : undefined, limit: 1 })
        .then((r) => setCounts((prev) => ({ ...prev, [k]: r.total })))
        .catch(() => {});
    });
  }, []);

  React.useEffect(() => {
    load(kind, queryApplied, sessionId, page, pageSize);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [kind, sessionId, page, pageSize]);

  React.useEffect(() => {
    loadCounts(sessionId);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionId]);

  const search = () => {
    setPage(1);
    setQueryApplied(query.trim());
    load(kind, query.trim(), sessionId, 1, pageSize);
  };
  const reload = () => {
    load(kind, queryApplied, sessionId, page, pageSize);
    loadCounts(sessionId);
  };

  // 转发现(二期):复用既有发现登记链路。会话未绑定任务 → 400;重复转换 → 409。
  // api 只把后端 error 原文抛成 message,这里按关键词归一成两句固定文案。
  const toFinding = async (x: C2LootRow) => {
    setConvertingId(x.id);
    try {
      const r = await api.c2LootToFinding(x.id);
      toast.success(`已转为发现 #${r.finding_id},可点击行内徽标查看`);
      reload();
    } catch (e) {
      const msg = (e as Error).message || "";
      if (msg.includes("绑定")) toast.error("转换失败:先在会话页绑定任务");
      else if (msg.includes("转换") || msg.includes("重复")) toast.error("该战利品已转换过,不能重复转换");
      else toast.error(`转换失败:${msg}`);
    } finally {
      setConvertingId(null);
    }
  };

  const confirmRemove = async () => {
    if (!removing) return;
    setDeleteBusy(true);
    try {
      await api.c2DeleteLoot(removing.id);
      toast.success("已删除");
      setRemoving(null);
      reload();
    } catch (e) {
      toast.error(`删除失败:${(e as Error).message}`);
    } finally {
      setDeleteBusy(false);
    }
  };

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div>
          <h1 className="font-semibold text-xl">C2 中心 · 战利品</h1>
          <p className="text-muted-foreground text-sm">
            后渗透采集成果的集中库:凭据、哈希、文件、截图。凭据与哈希密文存储,列表不回显内容,查看走明文接口。
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="ghost" size="sm" onClick={reload} title="重新加载">
            <RefreshCwIcon className="size-3.5" />
            刷新
          </Button>
          <Button size="sm" onClick={() => setCreating(true)}>
            <PlusIcon className="size-4" />
            手动登记
          </Button>
        </div>
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
                placeholder="搜索名称 / 来源路径"
                className="pl-8"
              />
            </div>
            <Button variant="outline" size="sm" onClick={search}>
              搜索
            </Button>
            {/* 会话深链筛选:非空时显示可移除的徽标。 */}
            {sessionId > 0 ? (
              <Badge variant="outline" className="gap-1 border-blue-500/30 bg-blue-500/15 text-blue-600">
                会话 #{sessionId}
                <button
                  type="button"
                  aria-label="清除会话筛选"
                  onClick={() => {
                    setPage(1);
                    setSessionIdText("");
                  }}
                >
                  <XIcon className="size-3" />
                </button>
              </Badge>
            ) : null}
            <span className="text-muted-foreground ml-auto text-xs tabular-nums">
              {queryApplied ? `命中 ${total} / ` : ""}共 {total} 条
            </span>
          </div>

          {/* kind 页签:可点过滤,aria-pressed 标选中。 */}
          <div className="flex flex-wrap items-center gap-1.5 px-4 pb-3 lg:px-6" role="group" aria-label="按类型筛选">
            {KIND_TABS.map((t) => {
              const active = kind === t.value;
              return (
                <button
                  key={t.value}
                  type="button"
                  aria-pressed={active}
                  onClick={() => {
                    setPage(1);
                    setKind(t.value);
                  }}
                  className={cn(
                    "inline-flex items-center gap-1.5 rounded-full border px-3 py-1 text-xs transition-colors",
                    active
                      ? "border-foreground/30 bg-foreground text-background"
                      : "hover:bg-muted text-muted-foreground border-input bg-transparent",
                  )}
                >
                  {t.label}
                  <span className="tabular-nums">{counts[t.value] ?? 0}</span>
                </button>
              );
            })}
          </div>

          {loading && loot.length === 0 ? (
            <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
          ) : loot.length === 0 ? (
            <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
              <GemIcon className="size-8 opacity-40" />
              <span>
                {queryApplied || kind !== "all" || sessionId > 0
                  ? "没有匹配的战利品"
                  : "战利品库还是空的。beacon 任务采集到的凭据 / 哈希 / 文件会自动入库,也可以点「手动登记」补录。"}
              </span>
            </div>
          ) : (
            <>
              <Table className="table-fixed">
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    {/* 名称列不设宽度:table-fixed 下自动吃掉余量。
                        固定列合计 632px(80+224+80+144+112),常规容器下余量充足。 */}
                    <TableHead>名称</TableHead>
                    <TableHead className="w-20">类型</TableHead>
                    <TableHead className="w-56">来源路径</TableHead>
                    <TableHead className="w-20">会话</TableHead>
                    <TableHead className="w-36">时间</TableHead>
                    <TableHead className="w-28">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {loot.map((raw) => {
                    const x = raw as C2LootRow;
                    const meta = c2LootKindMeta(String(x.kind));
                    const findingId = num(x.finding_id);
                    return (
                      <TableRow key={x.id}>
                        <TableCell className="min-w-0">
                          <span className="block w-full truncate font-medium" title={x.name}>
                            {x.name}
                          </span>
                          {findingId > 0 ? (
                            <Link
                              href={`/function/findings/detail?id=${findingId}`}
                              className="inline-flex items-center gap-0.5 rounded-full border border-blue-500/30 bg-blue-500/15 px-1.5 py-px text-[10px] text-blue-600 hover:underline"
                              title="已登记为发现,点击查看"
                            >
                              发现 #{findingId}
                            </Link>
                          ) : null}
                        </TableCell>
                        <TableCell>
                          <Badge variant="outline" className={meta.badge}>
                            {meta.label}
                          </Badge>
                        </TableCell>
                        <TableCell className="min-w-0">
                          <span className="block truncate font-mono text-xs" title={x.source_path}>
                            {x.source_path || "—"}
                          </span>
                        </TableCell>
                        <TableCell className="text-xs tabular-nums">
                          {num(x.session_id) > 0 ? (
                            <span className="text-blue-600" title={`会话 #${x.session_id}`}>
                              #{x.session_id}
                            </span>
                          ) : (
                            <span className="text-muted-foreground">—</span>
                          )}
                        </TableCell>
                        <TableCell className="text-muted-foreground text-xs tabular-nums">{fmtTime(x.created_at)}</TableCell>
                        <TableCell>
                          <div className="flex items-center gap-0.5">
                            <Button
                              variant="ghost"
                              size="icon-xs"
                              onClick={() => setViewingId(x.id)}
                              title="查看内容"
                            >
                              <EyeIcon className="size-3.5" />
                            </Button>
                            {findingId <= 0 ? (
                              <Button
                                variant="ghost"
                                size="icon-xs"
                                onClick={() => void toFinding(x)}
                                disabled={convertingId === x.id}
                                title="转为发现(需会话已绑定任务)"
                              >
                                <ArrowUpRightIcon
                                  className={convertingId === x.id ? "size-3.5 animate-spin" : "size-3.5"}
                                />
                              </Button>
                            ) : null}
                            <Button variant="ghost" size="icon-xs" onClick={() => setRemoving(x)} title="删除">
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

      <CreateLootDialog
        open={creating}
        onOpenChange={setCreating}
        defaultSessionId={sessionId}
        onCreated={() => {
          setCreating(false);
          reload();
        }}
      />

      <ViewLootDialog id={viewingId} onClose={() => setViewingId(null)} />

      {/* 删除确认 */}
      <AlertDialog open={!!removing} onOpenChange={(o) => !o && setRemoving(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>确认删除战利品</AlertDialogTitle>
            <AlertDialogDescription>
              将删除「{removing?.name}」(凭据 / 哈希类连密文一并删除)。此操作不可撤销。
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

// CreateLootDialog 手动登记战利品(人工补录,与 beacon 采集共用一张表)。
function CreateLootDialog({
  open,
  onOpenChange,
  defaultSessionId,
  onCreated,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  defaultSessionId: number;
  onCreated: () => void;
}) {
  const [sessionText, setSessionText] = React.useState(defaultSessionId > 0 ? String(defaultSessionId) : "");
  const [kind, setKind] = React.useState<C2LootKind>("credential");
  const [name, setName] = React.useState("");
  const [content, setContent] = React.useState("");
  const [sourcePath, setSourcePath] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    if (!open) return;
    setSessionText(defaultSessionId > 0 ? String(defaultSessionId) : "");
    setKind("credential");
    setName("");
    setContent("");
    setSourcePath("");
  }, [open, defaultSessionId]);

  const submit = async () => {
    const sid = Number(sessionText);
    if (!Number.isInteger(sid) || sid <= 0) {
      toast.error("请填写关联会话 ID(正整数)");
      return;
    }
    if (!name.trim()) {
      toast.error("请填写名称");
      return;
    }
    setBusy(true);
    try {
      await api.c2CreateLoot({
        session_id: sid,
        kind,
        name: name.trim(),
        content,
        source_path: sourcePath.trim() || undefined,
      });
      toast.success("已登记");
      onCreated();
    } catch (e) {
      toast.error(`登记失败:${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[88vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>手动登记战利品</DialogTitle>
          <DialogDescription>把人工获取的凭据 / 哈希 / 文件信息补录进战利品库,与 beacon 采集共用一套检索。</DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid grid-cols-3 gap-3">
            <div className="grid gap-1.5">
              <Label htmlFor="c2-loot-session">关联会话 ID</Label>
              <Input
                id="c2-loot-session"
                type="number"
                min={1}
                value={sessionText}
                onChange={(e) => setSessionText(e.target.value)}
                placeholder="如 12"
                className="tabular-nums"
              />
            </div>
            <div className="col-span-2 grid gap-1.5">
              <Label>类型</Label>
              <Select value={kind} onValueChange={(v) => setKind(v as C2LootKind)}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {C2_LOOT_KINDS.map((k) => (
                      <SelectItem key={k} value={k}>
                        {c2LootKindMeta(k).label}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
              <p className="text-muted-foreground text-[10px]">凭据 / 哈希类内容将加密存储,列表不回显</p>
            </div>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="c2-loot-name">名称</Label>
            <Input
              id="c2-loot-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="例如:OA 系统 admin 凭据"
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="c2-loot-content">内容</Label>
            <Textarea
              id="c2-loot-content"
              value={content}
              onChange={(e) => setContent(e.target.value)}
              rows={4}
              className="font-mono text-xs"
              placeholder="凭据明文 / 哈希串 / 文件摘要…"
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="c2-loot-source">来源路径(可选)</Label>
            <Input
              id="c2-loot-source"
              value={sourcePath}
              onChange={(e) => setSourcePath(e.target.value)}
              placeholder="例如:C:\\Users\\admin\\creds.txt"
              className="font-mono text-xs"
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy}>
            取消
          </Button>
          <Button disabled={busy} onClick={() => void submit()}>
            {busy ? "登记中…" : "登记"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ViewLootDialog 查看内容:credential/hash 的明文只有详情接口回(列表 content 恒空串),
// 其余类型同样从详情接口取 content 展示。
function ViewLootDialog({ id, onClose }: { id: number | null; onClose: () => void }) {
  const [loot, setLoot] = React.useState<C2Loot | null>(null);
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    if (id == null) {
      setLoot(null);
      return;
    }
    setBusy(true);
    let alive = true;
    api
      .c2LootDetail(id)
      .then((d) => {
        if (alive) setLoot(d);
      })
      .catch((e) => {
        if (alive) {
          toast.error(`加载详情失败:${(e as Error).message}`);
          onClose();
        }
      })
      .finally(() => {
        if (alive) setBusy(false);
      });
    return () => {
      alive = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  const meta = loot ? c2LootKindMeta(String(loot.kind)) : null;

  return (
    <Dialog
      open={id != null}
      onOpenChange={(o) => {
        if (!o) onClose();
      }}
    >
      <DialogContent className="max-h-[88vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex flex-wrap items-center gap-2">
            {loot ? loot.name : "查看战利品"}
            {meta ? (
              <Badge variant="outline" className={meta.badge}>
                {meta.label}
              </Badge>
            ) : null}
          </DialogTitle>
          <DialogDescription>
            {loot && (loot.kind === "credential" || loot.kind === "hash")
              ? "凭据 / 哈希密文存储,以下内容经详情接口解密展示;关闭后不留明文。"
              : "战利品内容原文展示。"}
          </DialogDescription>
        </DialogHeader>
        {loot ? (
          <div className="grid gap-3">
            <div className="text-muted-foreground grid gap-1 text-xs">
              <span className="truncate">
                来源路径:
                <span className="font-mono">{loot.source_path || "—"}</span>
              </span>
              <span>
                会话:{num(loot.session_id) > 0 ? `#${loot.session_id}` : "—"} · 登记时间:{fmtTime(loot.created_at)}
              </span>
            </div>
            <pre className="max-h-72 overflow-auto rounded-lg border bg-muted/30 p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap break-all">
              {busy ? "解密中…" : loot.content || "(无内容)"}
            </pre>
          </div>
        ) : (
          <p className="text-muted-foreground py-8 text-center text-sm text-xs">加载中…</p>
        )}
        <DialogFooter>
          <CopyButton text={loot && !busy ? loot.content : ""} successMessage="已复制内容" label="复制内容" />
          <Button variant="outline" onClick={onClose}>
            关闭
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
