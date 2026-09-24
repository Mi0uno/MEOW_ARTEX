"use client";

// 目标详情 · 资产页签的「添加资产」对话框。
//
// 三个入口共用一个对话框,用 Tabs 切换,因为它们的产物是同一种东西(挂到本目标下的资产),
// 只是来源不同:
//   手工录入   —— 用户直接敲值,前端推断类型(推断结果做成可编辑预览表,不让用户猜);
//   信息搜集   —— 该目标关联企业下待采纳的暂存资产,按 staged_ids 导入;
//   关联已有   —— 企业资产图里已有的资产,按 asset_ids 关联。
//
// 导入列表里的 imported=true 项置灰不可选:后端已按「平台×目标」去重,再选一次只会被跳过,
// 不如直接告诉用户「这条已经在了」。

import * as React from "react";

import { InboxIcon, Link2Icon, PencilLineIcon, PlusIcon, SearchIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { SRCTargetImportSource } from "@/lib/types";

import { ASSET_TYPE_BADGES, ASSET_TYPE_LABELS, ASSET_TYPE_ORDER, assetTypeLabel, parseAssetLines } from "./shared";

type Entry = "manual" | "staged" | "asset";

// 手工录入预览行的类型下拉:只允许真实存在的资产类型。
const TYPE_OPTIONS = ASSET_TYPE_ORDER;

export function AddAssetsDialog({
  open,
  onOpenChange,
  targetId,
  platformId,
  hasCompany,
  onAdded,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  targetId: number;
  platformId: number;
  hasCompany: boolean;
  onAdded: () => void;
}) {
  const [entry, setEntry] = React.useState<Entry>("manual");
  const [text, setText] = React.useState("");
  const [rows, setRows] = React.useState<ReturnType<typeof parseAssetLines>>([]);
  const [sources, setSources] = React.useState<SRCTargetImportSource[]>([]);
  const [picked, setPicked] = React.useState<Set<number>>(new Set());
  const [sourceQuery, setSourceQuery] = React.useState("");
  const [loadingSources, setLoadingSources] = React.useState(false);
  const [busy, setBusy] = React.useState(false);

  // 打开时重置:上一次的勾选/草稿带进来只会让用户误提交。
  React.useEffect(() => {
    if (!open) return;
    setEntry("manual");
    setText("");
    setRows([]);
    setSources([]);
    setPicked(new Set());
    setSourceQuery("");
  }, [open]);

  // 文本变化即重算预览(纯本地推断,不需要防抖)。
  const onTextChange = (v: string) => {
    setText(v);
    setRows(parseAssetLines(v));
  };

  const patchRow = (key: string, patch: Partial<{ type: string; value: string; port: number }>) =>
    setRows((rs) => rs.map((r) => (r.key === key ? { ...r, ...patch } : r)));

  const loadSources = React.useCallback(
    async (kind: Entry, q: string) => {
      if (kind === "manual") return;
      setLoadingSources(true);
      try {
        const list = await api.srcTargetImportSources(targetId, platformId, { kind, q });
        setSources(list);
      } catch (e) {
        toast.error(`加载可导入来源失败:${(e as Error).message}`);
        setSources([]);
      } finally {
        setLoadingSources(false);
      }
    },
    [targetId, platformId],
  );

  // 切到导入类入口时才拉数据:手工录入不该顺带发两个请求。
  // 同时清空勾选——staged 与 asset 的 id 来自两张不同的表,数值可能撞车,
  // 带着上一个入口的勾选切过来会提交到错误的来源。
  React.useEffect(() => {
    if (!open) return;
    setPicked(new Set());
    if (entry === "manual") return;
    void loadSources(entry, "");
  }, [open, entry, loadSources]);

  const selectable = sources.filter((s) => !s.imported);
  const allPicked = selectable.length > 0 && selectable.every((s) => picked.has(s.id));

  const toggleAll = () =>
    setPicked((p) => {
      if (allPicked) return new Set();
      const n = new Set(p);
      for (const s of selectable) n.add(s.id);
      return n;
    });

  const toggleOne = (id: number) =>
    setPicked((p) => {
      const n = new Set(p);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });

  const submit = async () => {
    // 手工录入的 type 由用户可改,过滤掉空值与空值行,避免把空串当资产发上去。
    const manual = rows.filter((r) => r.value.trim());
    if (entry === "manual" && manual.length === 0) {
      toast.error("请至少录入一条资产");
      return;
    }
    if (entry !== "manual" && picked.size === 0) {
      toast.error("请至少勾选一条");
      return;
    }
    setBusy(true);
    try {
      const ids = [...picked];
      const res = await api.addSRCTargetAssets(targetId, {
        platform_id: platformId,
        assets: entry === "manual" ? manual.map((r) => ({ type: r.type, value: r.value.trim(), port: r.port })) : undefined,
        staged_ids: entry === "staged" ? ids : undefined,
        asset_ids: entry === "asset" ? ids : undefined,
        source: entry === "manual" ? "manual" : entry === "staged" ? "recon" : "existing",
      });
      // 跳过与 errors 必须都告诉用户:只报「新增 N 条」会让人以为全成功。
      if (res.errors?.length) {
        toast.warning(`新增 ${res.added} 条,跳过 ${res.skipped} 条;${res.errors.length} 条有问题:${res.errors.slice(0, 3).join(";")}`);
      } else {
        toast.success(`新增 ${res.added} 条,跳过 ${res.skipped} 条`);
      }
      onAdded();
      onOpenChange(false);
    } catch (e) {
      toast.error(`添加失败:${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[88vh] overflow-y-auto sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>添加资产</DialogTitle>
          <DialogDescription>
            资产会挂到当前目标{platformId > 0 ? "与该平台" : ""}下。重复项由后端幂等跳过,不会报错。
          </DialogDescription>
        </DialogHeader>

        <Tabs value={entry} onValueChange={(v) => setEntry(v as Entry)}>
          <TabsList>
            <TabsTrigger value="manual">
              <PencilLineIcon className="size-3.5" />
              手工录入
            </TabsTrigger>
            <TabsTrigger value="staged">
              <InboxIcon className="size-3.5" />
              从信息搜集导入
            </TabsTrigger>
            <TabsTrigger value="asset">
              <Link2Icon className="size-3.5" />
              关联已有资产
            </TabsTrigger>
          </TabsList>

          <TabsContent value="manual" className="mt-3 grid gap-3">
            <Textarea
              value={text}
              onChange={(e) => onTextChange(e.target.value)}
              rows={5}
              className="font-mono text-xs"
              placeholder={"每行一条,支持混合:\nacme.com\napi.acme.com:8443\n198.51.100.20\nhttps://www.acme.com/login"}
            />
            {rows.length > 0 && (
              <div className="rounded-lg border">
                <p className="text-muted-foreground border-b px-3 py-2 text-xs">
                  解析预览(共 {rows.length} 条,类型与端口可改)
                </p>
                <Table className="table-fixed">
                  <TableHeader>
                    <TableRow className="hover:bg-transparent">
                      {/* 值列不设宽度:table-fixed 下自动吃掉余量。 */}
                      <TableHead>值</TableHead>
                      <TableHead className="w-32">类型</TableHead>
                      <TableHead className="w-20">端口</TableHead>
                      <TableHead className="w-14">操作</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {rows.map((r) => (
                      <TableRow key={r.key}>
                        <TableCell className="min-w-0">
                          <Input
                            value={r.value}
                            onChange={(e) => patchRow(r.key, { value: e.target.value })}
                            className="h-7"
                            title={r.raw}
                          />
                        </TableCell>
                        <TableCell>
                          <Select value={r.type} onValueChange={(v) => patchRow(r.key, { type: v })}>
                            <SelectTrigger size="sm" className="w-full">
                              <SelectValue />
                            </SelectTrigger>
                            <SelectContent>
                              {TYPE_OPTIONS.map((t) => (
                                <SelectItem key={t} value={t}>
                                  {ASSET_TYPE_LABELS[t]}
                                </SelectItem>
                              ))}
                            </SelectContent>
                          </Select>
                        </TableCell>
                        <TableCell>
                          <Input
                            value={r.port ? String(r.port) : ""}
                            onChange={(e) => patchRow(r.key, { port: Number(e.target.value.replace(/\D/g, "")) || 0 })}
                            className="h-7 tabular-nums"
                            placeholder="—"
                          />
                        </TableCell>
                        <TableCell>
                          <Button
                            variant="ghost"
                            size="icon-xs"
                            onClick={() => setRows((rs) => rs.filter((x) => x.key !== r.key))}
                            title="从本次提交中移除"
                          >
                            <Trash2Icon className="size-3.5" />
                          </Button>
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            )}
          </TabsContent>

          <TabsContent value="staged" className="mt-3 grid gap-3">
            <SourcePicker
              kind="staged"
              sources={sources}
              loading={loadingSources}
              hasCompany={hasCompany}
              query={sourceQuery}
              onQueryChange={setSourceQuery}
              onSearch={() => void loadSources("staged", sourceQuery)}
              picked={picked}
              allPicked={allPicked}
              onToggleAll={toggleAll}
              onToggleOne={toggleOne}
            />
          </TabsContent>

          <TabsContent value="asset" className="mt-3 grid gap-3">
            <SourcePicker
              kind="asset"
              sources={sources}
              loading={loadingSources}
              hasCompany={hasCompany}
              query={sourceQuery}
              onQueryChange={setSourceQuery}
              onSearch={() => void loadSources("asset", sourceQuery)}
              picked={picked}
              allPicked={allPicked}
              onToggleAll={toggleAll}
              onToggleOne={toggleOne}
            />
          </TabsContent>
        </Tabs>

        <DialogFooter>
          <span className="text-muted-foreground mr-auto text-xs tabular-nums">
            {entry === "manual" ? `待提交 ${rows.length} 条` : `已选 ${picked.size} 条`}
          </span>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy}>
            取消
          </Button>
          <Button onClick={() => void submit()} disabled={busy}>
            <PlusIcon className="size-4" />
            {busy ? "添加中…" : "添加"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// SourcePicker 渲染「信息搜集暂存区」与「已有资产」两个导入列表(结构完全一致,只差文案)。
function SourcePicker({
  kind,
  sources,
  loading,
  hasCompany,
  query,
  onQueryChange,
  onSearch,
  picked,
  allPicked,
  onToggleAll,
  onToggleOne,
}: {
  kind: "staged" | "asset";
  sources: SRCTargetImportSource[];
  loading: boolean;
  hasCompany: boolean;
  query: string;
  onQueryChange: (v: string) => void;
  onSearch: () => void;
  picked: Set<number>;
  allPicked: boolean;
  onToggleAll: () => void;
  onToggleOne: (id: number) => void;
}) {
  const placeholder = kind === "staged" ? "搜索待采纳资产的值 / 任务 / 收集器" : "搜索已有资产的值";
  const emptyText =
    kind === "staged"
      ? "该目标关联的企业下暂无待采纳的信息搜集资产。"
      : "该目标关联的企业下暂无已有资产。";

  if (!hasCompany) {
    return (
      <div className="text-muted-foreground flex flex-col items-center gap-2 rounded-lg border py-12 text-center text-sm">
        <InboxIcon className="size-8 opacity-40" />
        <span>该目标还没有关联企业。先到「概览」页签关联企业,才能按企业筛选可导入的资产。</span>
      </div>
    );
  }

  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative w-full sm:max-w-xs">
          <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
          <Input
            value={query}
            onChange={(e) => onQueryChange(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && onSearch()}
            placeholder={placeholder}
            className="pl-8"
          />
        </div>
        <Button variant="outline" size="sm" onClick={onSearch}>
          搜索
        </Button>
        <span className="text-muted-foreground ml-auto text-xs tabular-nums">
          共 {sources.length} 条 · 可选 {sources.filter((s) => !s.imported).length} 条
        </span>
      </div>

      {loading ? (
        <p className="text-muted-foreground py-12 text-center text-sm">加载中…</p>
      ) : sources.length === 0 ? (
        <div className="text-muted-foreground flex flex-col items-center gap-2 rounded-lg border py-12 text-center text-sm">
          <InboxIcon className="size-8 opacity-40" />
          <span>{emptyText}</span>
        </div>
      ) : (
        <div className="max-h-80 overflow-y-auto rounded-lg border">
          <Table className="table-fixed">
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="w-10">
                  <Checkbox checked={allPicked} onCheckedChange={onToggleAll} aria-label="全选可导入项" />
                </TableHead>
                {/* 值列不设宽度:table-fixed 下自动吃掉余量。 */}
                <TableHead>值</TableHead>
                <TableHead className="w-24">类型</TableHead>
                <TableHead className="w-16">端口</TableHead>
                <TableHead className="w-44">{kind === "staged" ? "来源任务 / 收集器" : "状态"}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {sources.map((s) => (
                <TableRow key={`${s.kind}-${s.id}`} className={s.imported ? "opacity-50" : undefined}>
                  <TableCell>
                    <Checkbox
                      checked={picked.has(s.id)}
                      disabled={s.imported}
                      onCheckedChange={() => onToggleOne(s.id)}
                      aria-label={s.imported ? `${s.value}(已在目标下)` : `选择 ${s.value}`}
                    />
                  </TableCell>
                  <TableCell className="min-w-0">
                    <span className="block w-full truncate font-mono text-xs" title={s.value}>
                      {s.value}
                    </span>
                  </TableCell>
                  <TableCell>
                    <Badge variant="outline" className={ASSET_TYPE_BADGES[s.type] ?? "bg-muted text-muted-foreground"}>
                      {assetTypeLabel(s.type)}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-muted-foreground text-xs tabular-nums">{s.port || "—"}</TableCell>
                  <TableCell className="text-muted-foreground min-w-0 text-xs">
                    {s.imported ? (
                      <span className="text-muted-foreground">已在目标下</span>
                    ) : kind === "staged" ? (
                      <span className="block truncate" title={`${s.task_name ?? ""} ${s.collector ?? ""}`}>
                        {[s.task_name, s.collector].filter(Boolean).join(" · ") || "—"}
                      </span>
                    ) : (
                      <span>可关联</span>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </>
  );
}
