"use client";

// C2 中心 · 插件库(三期,契约 §21)——可推送执行的能力包管理页。
//
// 四件套列表:平台/形态过滤 + 关键字搜索 + 服务端分页;列:名称(带内置徽标)/
// 平台/形态/语言/描述/操作(编辑/删除)。内置插件(builtin=true,启动播种,≥6 个
// 教育性插件)随版本维护:不可编辑、不可删除(后端 403 兜底),行内只读展示。
// 新建/编辑走 C2PluginFormDialog。

import * as React from "react";

import {
  PencilIcon,
  PlusIcon,
  PuzzleIcon,
  RefreshCwIcon,
  SearchIcon,
  Trash2Icon,
} from "lucide-react";
import { toast } from "sonner";

import {
  c2PayloadTargetLabel,
  c2PluginKindMeta,
  c2PluginLanguageMeta,
  c2PluginPlatformBadge,
} from "@/components/c2-shared";
import { C2PluginFormDialog } from "@/components/c2-plugin-form-dialog";
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
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { C2Plugin, C2PayloadTarget } from "@/lib/types";

const PAGE_SIZE = 50;
const ALL = "__all__";
const PLATFORMS: C2PayloadTarget[] = ["windows", "linux", "macos", "cross"];

export default function C2PluginsPage() {
  const [plugins, setPlugins] = React.useState<C2Plugin[]>([]);
  const [total, setTotal] = React.useState(0);
  const [platform, setPlatform] = React.useState<string>(ALL);
  const [kind, setKind] = React.useState<string>(ALL);
  const [query, setQuery] = React.useState("");
  const [queryApplied, setQueryApplied] = React.useState("");
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);
  const [loading, setLoading] = React.useState(true);
  const [creating, setCreating] = React.useState(false);
  const [editing, setEditing] = React.useState<C2Plugin | null>(null);
  const [removing, setRemoving] = React.useState<C2Plugin | null>(null);
  const [deleteBusy, setDeleteBusy] = React.useState(false);

  const load = React.useCallback(
    (pf: string, kf: string, q: string, p: number, size: number) => {
      setLoading(true);
      api
        .c2Plugins({
          platform: pf === ALL ? undefined : pf,
          kind: kf === ALL ? undefined : kf,
          q: q || undefined,
          limit: size,
          offset: (p - 1) * size,
        })
        .then((r) => {
          setPlugins(r.plugins ?? []);
          setTotal(r.total);
        })
        .catch((e) => {
          setPlugins([]);
          setTotal(0);
          toast.error(`加载插件失败:${(e as Error).message}`);
        })
        .finally(() => setLoading(false));
    },
    [],
  );

  React.useEffect(() => {
    load(platform, kind, queryApplied, page, pageSize);
  }, [platform, kind, queryApplied, page, pageSize, load]);

  const search = () => {
    setPage(1);
    setQueryApplied(query.trim());
  };

  const confirmRemove = async () => {
    if (!removing) return;
    setDeleteBusy(true);
    try {
      await api.c2DeletePlugin(removing.id);
      toast.success("已删除");
      setRemoving(null);
      load(platform, kind, queryApplied, page, pageSize);
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
          <h1 className="font-semibold text-xl">C2 中心 · 插件库</h1>
          <p className="text-muted-foreground text-sm">
            可推送执行的能力包:脚本插件渲染 entry 命令直走命令通道,二进制插件经文件通道上传后执行;参数只允许经
            {" {{param}} "}
            占位注入并做消毒。内置插件随版本播种,不可改删。
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            variant="ghost"
            size="sm"
            onClick={() => load(platform, kind, queryApplied, page, pageSize)}
            title="重新加载"
          >
            <RefreshCwIcon className="size-3.5" />
            刷新
          </Button>
          <Button size="sm" onClick={() => setCreating(true)}>
            <PlusIcon className="size-4" />
            新建插件
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
                placeholder="搜索名称 / 描述"
                className="pl-8"
              />
            </div>
            <Button variant="outline" size="sm" onClick={search}>
              搜索
            </Button>
            <Select
              value={platform}
              onValueChange={(v) => {
                setPage(1);
                setPlatform(v);
              }}
            >
              <SelectTrigger className="w-32">
                <SelectValue placeholder="平台" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value={ALL}>全部平台</SelectItem>
                  {PLATFORMS.map((p) => (
                    <SelectItem key={p} value={p}>
                      {c2PayloadTargetLabel(p)}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            <Select
              value={kind}
              onValueChange={(v) => {
                setPage(1);
                setKind(v);
              }}
            >
              <SelectTrigger className="w-32">
                <SelectValue placeholder="形态" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value={ALL}>全部形态</SelectItem>
                  <SelectItem value="script">脚本</SelectItem>
                  <SelectItem value="binary">二进制</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
            <span className="text-muted-foreground ml-auto text-xs tabular-nums">
              {queryApplied ? `命中 ${total} / ` : ""}共 {total} 个
            </span>
          </div>

          {loading && plugins.length === 0 ? (
            <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
          ) : plugins.length === 0 ? (
            <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
              <PuzzleIcon className="size-8 opacity-40" />
              <span>
                {queryApplied || platform !== ALL || kind !== ALL
                  ? "没有匹配的插件"
                  : "插件库还是空的;内置插件会在 C2 中心启动时自动播种。"}
              </span>
            </div>
          ) : (
            <>
              <Table className="table-fixed">
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    {/* 名称列不设宽度:table-fixed 下自动吃掉余量。 */}
                    <TableHead>名称</TableHead>
                    <TableHead className="w-20">平台</TableHead>
                    <TableHead className="w-20">形态</TableHead>
                    <TableHead className="w-24">语言</TableHead>
                    <TableHead className="w-20">参数</TableHead>
                    <TableHead className="w-80">描述</TableHead>
                    <TableHead className="w-16">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {plugins.map((p) => {
                    const km = c2PluginKindMeta(p.kind);
                    const lm = c2PluginLanguageMeta(p.language);
                    const paramCount = Object.keys(p.params_schema ?? {}).length;
                    return (
                      <TableRow key={p.id}>
                        <TableCell className="min-w-0">
                          <div className="flex items-center gap-1.5">
                            <span className="truncate font-medium" title={p.name}>
                              {p.name}
                            </span>
                            {p.builtin ? (
                              <Badge
                                variant="outline"
                                className="shrink-0 border-violet-500/30 bg-violet-500/15 px-1 py-0 text-[10px] text-violet-600"
                              >
                                内置
                              </Badge>
                            ) : null}
                          </div>
                          <span className="text-muted-foreground block truncate font-mono text-[10px]" title={p.entry}>
                            {p.entry || "—"}
                          </span>
                        </TableCell>
                        <TableCell>
                          <Badge variant="outline" className={c2PluginPlatformBadge(p.platform)}>
                            {c2PayloadTargetLabel(p.platform)}
                          </Badge>
                        </TableCell>
                        <TableCell>
                          <Badge variant="outline" className={km.badge}>
                            {km.label}
                          </Badge>
                        </TableCell>
                        <TableCell>
                          <Badge variant="outline" className={lm.badge}>
                            {lm.label}
                          </Badge>
                        </TableCell>
                        <TableCell className="text-muted-foreground text-xs tabular-nums">
                          {paramCount > 0 ? `${paramCount} 项` : "—"}
                        </TableCell>
                        <TableCell className="min-w-0">
                          <span className="block truncate text-xs" title={p.description}>
                            {p.description || "—"}
                          </span>
                        </TableCell>
                        <TableCell>
                          <div className="flex items-center gap-0.5">
                            <Button
                              variant="ghost"
                              size="icon-xs"
                              disabled={p.builtin}
                              title={p.builtin ? "内置插件不可编辑" : "编辑"}
                              onClick={() => setEditing(p)}
                            >
                              <PencilIcon className="size-3.5" />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon-xs"
                              disabled={p.builtin}
                              title={p.builtin ? "内置插件不可删除" : "删除"}
                              onClick={() => setRemoving(p)}
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

      <C2PluginFormDialog
        open={creating || editing !== null}
        onOpenChange={(o) => {
          if (!o) {
            setCreating(false);
            setEditing(null);
          }
        }}
        editing={editing}
        onSaved={() => {
          setCreating(false);
          setEditing(null);
          load(platform, kind, queryApplied, page, pageSize);
        }}
      />

      {/* 删除确认(builtin 在按钮层就禁用,这里是二道防线) */}
      <AlertDialog open={!!removing} onOpenChange={(o) => !o && setRemoving(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>确认删除插件</AlertDialogTitle>
            <AlertDialogDescription>
              将删除插件「{removing?.name}」;已在目标机上落盘的文件不受影响。此操作不可撤销。
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
