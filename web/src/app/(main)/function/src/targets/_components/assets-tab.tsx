"use client";

// 目标详情 · 资产页签:当前平台下本目标的资产清单(类型分类器 + 搜索 + 分页 + 添加/移除)。
//
// 资产关联表 src_target_assets 的主键是「平台 × 目标 × 资产」,所以列表与新增都必须锁定
// 一个平台:这里跟随页面的当前平台,并允许在本页直接切换——否则用户会以为看到的是
// 「这个目标的全部资产」,而新增却只落进某一个平台。
//
// 分类器是 Tabs(可点、带计数),计数取自后端未过滤全量——按类型过滤时其他类型的计数不归零。

import * as React from "react";

import { BoxesIcon, InfoIcon, PlusIcon, SearchIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { TablePagination } from "@/components/table-pagination";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { SRCTargetAsset, SRCTargetLink } from "@/lib/types";

import { AddAssetsDialog } from "./add-assets-dialog";
import {
  ASSET_SOURCE_BADGES,
  ASSET_TYPE_BADGES,
  ASSET_TYPE_LABELS,
  ASSET_TYPE_ORDER,
  assetSourceLabel,
  assetTypeLabel,
  fmtTime,
} from "./shared";

const PAGE_SIZE = 50;

export function AssetsTab({
  targetId,
  platformId,
  links,
  onPlatformChange,
  hasCompany,
  onCountsChanged,
}: {
  targetId: number;
  platformId: number;
  links: SRCTargetLink[];
  onPlatformChange: (platformId: number) => void;
  hasCompany: boolean;
  onCountsChanged: () => void;
}) {
  const [assets, setAssets] = React.useState<SRCTargetAsset[]>([]);
  const [total, setTotal] = React.useState(0);
  const [counts, setCounts] = React.useState<Record<string, number>>({});
  const [type, setType] = React.useState("all");
  const [query, setQuery] = React.useState("");
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);
  const [loading, setLoading] = React.useState(true);
  const [addOpen, setAddOpen] = React.useState(false);

  const load = React.useCallback(
    (pid: number, t: string, q: string, p: number, size: number) => {
      setLoading(true);
      api
        .srcTargetAssets(targetId, {
          // platformId=0 表示该目标还没挂到任何平台,此时按全量展示(没有平台维度可筛)。
          platformId: pid > 0 ? pid : undefined,
          type: t === "all" ? "" : t,
          q,
          limit: size,
          offset: (p - 1) * size,
        })
        .then((r) => {
          setAssets(r.assets);
          setTotal(r.total);
          setCounts(r.counts);
        })
        .catch((e) => toast.error(`加载资产失败:${(e as Error).message}`))
        .finally(() => setLoading(false));
    },
    [targetId],
  );

  React.useEffect(() => {
    load(platformId, type, query, page, pageSize);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [platformId, type, page, pageSize, targetId]);

  const search = () => {
    setPage(1);
    load(platformId, type, query, 1, pageSize);
  };
  const reload = () => load(platformId, type, query, page, pageSize);

  const remove = async (a: SRCTargetAsset) => {
    const label = a.value || `资产 #${a.asset_id}`;
    // 只解除本目标下的关联,资产本体(可能被其他目标/任务引用)不受影响,所以措辞要准确。
    if (!window.confirm(`从本目标移除资产「${label}」?只解除本目标下的关联,资产本体不会被删除。`)) return;
    try {
      await api.removeSRCTargetAssets(targetId, [a.id]);
      toast.success("已移除");
      reload();
      onCountsChanged();
    } catch (e) {
      toast.error(`移除失败:${(e as Error).message}`);
    }
  };

  const typeCounts = ASSET_TYPE_ORDER.map((t) => ({ key: t, label: ASSET_TYPE_LABELS[t], count: counts[t] ?? 0 }));
  const allCount = Object.values(counts).reduce((a, b) => a + b, 0);

  return (
    <Card>
      <CardContent className="px-0 pt-4">
        <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
          {links.length > 1 && (
            <Select
              value={String(platformId)}
              onValueChange={(v) => {
                setPage(1);
                onPlatformChange(Number(v));
              }}
            >
              <SelectTrigger className="w-48">
                <SelectValue placeholder="平台" />
              </SelectTrigger>
              <SelectContent>
                {links.map((l) => (
                  <SelectItem key={l.platform_id} value={String(l.platform_id)}>
                    {l.platform_name || `平台 #${l.platform_id}`}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
          <div className="relative w-full sm:max-w-xs">
            <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && search()}
              placeholder="搜索资产值"
              className="pl-8"
            />
          </div>
          <Button variant="outline" size="sm" onClick={search}>
            搜索
          </Button>
          <Tabs
            value={type}
            onValueChange={(v) => {
              setPage(1);
              setType(v);
            }}
          >
            <TabsList>
              <TabsTrigger value="all">全部 {allCount}</TabsTrigger>
              {typeCounts.map((t) => (
                <TabsTrigger key={t.key} value={t.key}>
                  {t.label} {t.count}
                </TabsTrigger>
              ))}
            </TabsList>
          </Tabs>
          <span className="text-muted-foreground ml-auto text-xs tabular-nums">
            {query ? `命中 ${total} / ` : ""}共 {total} 条
          </span>
          <Button size="sm" disabled={platformId <= 0} onClick={() => setAddOpen(true)} title={platformId <= 0 ? "先把目标挂到平台下" : undefined}>
            <PlusIcon className="size-4" />
            添加资产
          </Button>
        </div>

        <p className="text-muted-foreground flex items-center gap-1.5 px-4 pb-3 text-xs lg:px-6">
          <InfoIcon className="size-3.5 shrink-0" />
          {links.length === 0
            ? "该目标还没有归属任何平台。资产关联属于「平台 × 目标」这一层,先到平台详情页把目标挂到平台下。"
            : `资产关联属于「平台 × 目标」这一层,当前只显示并新增到「${
                links.find((l) => l.platform_id === platformId)?.platform_name || `平台 #${platformId}`
              }」;同一目标在其他平台下的资产是各自独立的一份。`}
        </p>

        {loading && assets.length === 0 ? (
          <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
        ) : assets.length === 0 ? (
          <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
            <BoxesIcon className="size-8 opacity-40" />
            <span>
              {query
                ? "没有匹配的资产"
                : type === "all"
                  ? "该目标在当前平台下还没有资产。点「添加资产」手工录入,或从信息搜集暂存区导入。"
                  : "该类型下还没有资产,切换分类器或添加资产。"}
            </span>
          </div>
        ) : (
          <>
            <Table className="table-fixed">
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  {/* 值列不设宽度:table-fixed 下自动吃掉余量(设 w-full 会挤掉后面的列)。 */}
                  <TableHead>值</TableHead>
                  <TableHead className="w-20">类型</TableHead>
                  <TableHead className="w-16">端口</TableHead>
                  <TableHead className="w-24">来源</TableHead>
                  <TableHead className="w-40">加入时间</TableHead>
                  <TableHead className="w-16">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {assets.map((a) => (
                  <TableRow key={a.id}>
                    <TableCell className="min-w-0">
                      <span className="block w-full truncate font-mono text-xs" title={a.value || `资产 #${a.asset_id}`}>
                        {a.value || `资产 #${a.asset_id}`}
                      </span>
                    </TableCell>
                    <TableCell>
                      <Badge variant="outline" className={ASSET_TYPE_BADGES[a.type ?? ""] ?? "bg-muted text-muted-foreground"}>
                        {assetTypeLabel(a.type)}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-muted-foreground text-xs tabular-nums">{a.port || "—"}</TableCell>
                    <TableCell>
                      <Badge variant="outline" className={ASSET_SOURCE_BADGES[a.source] ?? "bg-muted text-muted-foreground"}>
                        {assetSourceLabel(a.source)}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-muted-foreground text-xs">{fmtTime(a.created_at)}</TableCell>
                    <TableCell>
                      <Button variant="ghost" size="icon-xs" onClick={() => void remove(a)} title="从本目标移除">
                        <Trash2Icon className="text-muted-foreground size-3.5" />
                      </Button>
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

      <AddAssetsDialog
        open={addOpen}
        onOpenChange={setAddOpen}
        targetId={targetId}
        platformId={platformId}
        hasCompany={hasCompany}
        onAdded={() => {
          reload();
          onCountsChanged();
        }}
      />
    </Card>
  );
}
