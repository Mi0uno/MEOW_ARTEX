"use client";

// SRC 模式 · 平台选择:平台列表页。
//
// 这一页是「平台 → 目标 → 资产」三级链路的入口:平台是顶层实体(补天/漏洞盒子/教育 SRC),
// 平台规范(奖励标准/评分标准/守则/规范)在建 SRC 任务时注入 AI 硬约束。
// 所以列表页只做平台的检索与增删改,进入详情页才挂目标、传规范附件、看成果。

import * as React from "react";

import Link from "next/link";

import { ArrowRightIcon, PencilIcon, PlusIcon, SearchIcon, ShieldAlertIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { TablePagination } from "@/components/table-pagination";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { SRCPlatform } from "@/lib/types";

import { EnabledBadge, fmtTime } from "./_components/platform-shared";
import { PlatformSheet } from "./_components/platform-sheet";

const PAGE_SIZE = 50;

export default function SRCPlatformsPage() {
  const [platforms, setPlatforms] = React.useState<SRCPlatform[]>([]);
  const [total, setTotal] = React.useState(0);
  // counts 来自后端未过滤全量:分类页签的数字不能受当前筛选影响,
  // 否则选中「已启用」后「已停用」会归零,页签看起来像坏了。
  const [counts, setCounts] = React.useState<Record<string, number>>({});
  const [query, setQuery] = React.useState("");
  const [enabled, setEnabled] = React.useState("all");
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);
  const [loading, setLoading] = React.useState(true);
  const [editing, setEditing] = React.useState<SRCPlatform | null>(null);
  const [creating, setCreating] = React.useState(false);

  const load = React.useCallback((q: string, en: string, p: number, size: number) => {
    setLoading(true);
    api
      .srcPlatforms({
        q,
        // "all" 是 UI 哨兵,api 层只在非 all 时才带上 enabled 参数(空串=不过滤)。
        enabled: en,
        limit: size,
        offset: (p - 1) * size,
      })
      .then((r) => {
        setPlatforms(r.platforms ?? []);
        setCounts(r.counts ?? {});
        setTotal(r.total);
      })
      .catch((e) => toast.error(`加载平台失败:${(e as Error).message}`))
      .finally(() => setLoading(false));
  }, []);

  React.useEffect(() => {
    load(query, enabled, page, pageSize);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled, page, pageSize]);

  const search = () => {
    setPage(1);
    load(query, enabled, 1, pageSize);
  };
  const reload = () => load(query, enabled, page, pageSize);

  const remove = async (p: SRCPlatform) => {
    if (
      !window.confirm(
        `删除平台「${p.name}」?该平台下的目标挂链、资产关联与规范附件一并删除,不可恢复。目标本身与其他平台的关联不受影响。`,
      )
    )
      return;
    try {
      await api.deleteSRCPlatform(p.id);
      toast.success("平台已删除");
      reload();
    } catch (e) {
      toast.error(`删除失败:${(e as Error).message}`);
    }
  };

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div>
          <h1 className="font-semibold text-xl">平台选择</h1>
          <p className="text-muted-foreground text-sm">
            进入平台 → 添加目标 → 添加资产。平台规范(奖励标准/评分标准/守则/规范及其附件)会作为硬约束注入 SRC
            任务,防止 AI 越界。
          </p>
        </div>
        <Button size="sm" onClick={() => setCreating(true)}>
          <PlusIcon className="size-4" />
          新建平台
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
                placeholder="搜索平台名 / 描述 / 主页"
                className="pl-8"
              />
            </div>
            <Button variant="outline" size="sm" onClick={search}>
              搜索
            </Button>
            {/* 分类器必须是可点页签:只读徽标会让「快捷切换」消失(用户报障过)。 */}
            <Tabs
              value={enabled}
              onValueChange={(v) => {
                setPage(1);
                setEnabled(v);
              }}
            >
              <TabsList>
                <TabsTrigger value="all">全部 {counts.all ?? 0}</TabsTrigger>
                <TabsTrigger value="enabled">已启用 {counts.enabled ?? 0}</TabsTrigger>
                <TabsTrigger value="disabled">已停用 {counts.disabled ?? 0}</TabsTrigger>
              </TabsList>
            </Tabs>
            <span className="text-muted-foreground ml-auto text-xs tabular-nums">
              {query ? `命中 ${total} / ` : ""}共 {total} 个平台
            </span>
          </div>

          {loading && platforms.length === 0 ? (
            <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
          ) : platforms.length === 0 ? (
            <div className="text-muted-foreground flex flex-col items-center gap-3 py-16 text-center text-sm">
              <ShieldAlertIcon className="size-8 opacity-40" />
              <span>
                {query || enabled !== "all"
                  ? "当前筛选没有命中的平台。换个关键词，或把分类切回「全部」。"
                  : "还没有平台。点「新建平台」录入补天 / 漏洞盒子 / 教育 SRC 这类提交平台，进去后添加目标与规范附件。"}
              </span>
              {!query && enabled === "all" && (
                <Button size="sm" variant="outline" onClick={() => setCreating(true)}>
                  <PlusIcon className="size-4" />
                  新建平台
                </Button>
              )}
            </div>
          ) : (
            <>
              <Table className="table-fixed">
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    {/* 平台名列不设宽度:table-fixed 下自动吃掉余量(设 w-full 会挤掉后面的列)。
                        其余固定列之和 688px(80+80+96+80+96+144+112),容器约 897px(1280 视口),
                        名字列留得下十几字;再多一列就要重新分配,否则名字会被压到看不见。 */}
                    <TableHead>平台名称</TableHead>
                    <TableHead className="w-20">目标数</TableHead>
                    <TableHead className="w-20">资产数</TableHead>
                    <TableHead className="w-24">规范文件数</TableHead>
                    <TableHead className="w-20">成果数</TableHead>
                    <TableHead className="w-24">状态</TableHead>
                    <TableHead className="w-36">更新时间</TableHead>
                    <TableHead className="w-28">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {platforms.map((p) => (
                    <TableRow key={p.id}>
                      {/* 平台名即入口:进入详情页挂目标、传规范附件、看成果。 */}
                      <TableCell className="min-w-0">
                        <Link
                          href={`/function/src/platforms/detail?id=${p.id}`}
                          className="block w-full truncate text-left font-medium hover:underline"
                          title={p.name}
                        >
                          {p.name}
                        </Link>
                      </TableCell>
                      <TableCell className="tabular-nums">{p.target_count ?? 0}</TableCell>
                      <TableCell className="tabular-nums">{p.asset_count ?? 0}</TableCell>
                      <TableCell className="tabular-nums">{p.file_count ?? 0}</TableCell>
                      <TableCell className="tabular-nums">{p.reward_count ?? 0}</TableCell>
                      <TableCell>
                        <EnabledBadge enabled={p.enabled} />
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs">{fmtTime(p.updated_at)}</TableCell>
                      <TableCell>
                        <div className="flex items-center gap-0.5">
                          <Button variant="ghost" size="icon-xs" onClick={() => setEditing(p)} title="编辑">
                            <PencilIcon className="size-3.5" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon-xs"
                            onClick={() => void remove(p)}
                            title="删除(含目标挂链与规范附件)"
                          >
                            <Trash2Icon className="text-muted-foreground size-3.5" />
                          </Button>
                          <Button variant="ghost" size="icon-xs" asChild title="进入平台">
                            <Link href={`/function/src/platforms/detail?id=${p.id}`} aria-label="进入平台">
                              <ArrowRightIcon className="size-3.5" />
                            </Link>
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
              <TablePagination
                page={page}
                pageSize={pageSize}
                total={total}
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

      <PlatformSheet
        platform={editing}
        open={creating || !!editing}
        onOpenChange={(o) => {
          if (!o) {
            setCreating(false);
            setEditing(null);
          }
        }}
        onSaved={() => {
          setCreating(false);
          setEditing(null);
          reload();
        }}
      />
    </div>
  );
}
