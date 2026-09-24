"use client";

// 企业详情:资产页企业名点击进入(?id=公司 id)。
// 壳与任务详情同构(sticky 顶栏 + 页签),内容针对性:
//   资产(查看/管理:筛选+删除) / 关系图(资产拓扑) / 范围(企业 scope 规则)。

import * as React from "react";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { ArrowLeftIcon, RefreshCwIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { ReconGraph } from "@/components/recon-graph";
import { assetsToTaskGraph } from "@/components/asset-graph";
import { StatusBadge } from "@/components/status-badge";
import { api } from "@/lib/api";
import type { Asset, Company } from "@/lib/types";

const ASSET_TYPE_LABEL: Record<string, string> = {
  root_domain: "根域名",
  subdomain: "子域名",
  domain: "子域名",
  ip: "IP",
  service: "Web 服务",
  app: "应用",
  endpoint: "接口",
};

function fmtTime(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString();
}

function assetValue(a: Asset): string {
  const t = a.type as string;
  switch (t) {
    case "root_domain":
      return a.domain || a.root_domain || `#${a.id}`;
    case "subdomain":
    case "domain":
      return a.domain || `#${a.id}`;
    case "ip":
      return a.ip || `#${a.id}`;
    case "app":
      return a.app_name || a.bundle_id || `#${a.id}`;
    case "endpoint":
      return `${a.method ?? ""} ${a.domain || a.url || ""}`.trim() || `#${a.id}`;
    default: {
      if (a.url) return a.url;
      return (a.domain || a.ip || "") + (a.port ? `:${a.port}` : "");
    }
  }
}

export default function CompanyDetailPage() {
  const searchParams = useSearchParams();
  const id = searchParams.get("id") ?? "";

  return (
    <React.Suspense fallback={null}>
      {id ? <CompanyDetailInner companyId={Number(id)} /> : <MissingId />}
    </React.Suspense>
  );
}

function MissingId() {
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-3 p-10 text-center">
      <p className="text-muted-foreground">缺少企业 id,请从「资产」页进入</p>
      <Button asChild variant="outline">
        <Link href="/function/assets">
          <ArrowLeftIcon /> 返回资产
        </Link>
      </Button>
    </div>
  );
}

// 企业资产 → 同一图程序(ReconGraph)的数据转换。
function CompanyAssetGraph({ assets, className }: { assets: Asset[]; className?: string }) {
  const graph = React.useMemo(() => assetsToTaskGraph(assets), [assets]);
  return <ReconGraph nodes={graph.nodes} edges={graph.edges} className={className} />;
}

function CompanyDetailInner({ companyId }: { companyId: number }) {
  const [company, setCompany] = React.useState<Company | null>(null);
  const [loaded, setLoaded] = React.useState(false);
  const [assets, setAssets] = React.useState<Asset[]>([]);
  const [assetsLoaded, setAssetsLoaded] = React.useState(false);
  const [typeFilter, setTypeFilter] = React.useState<string>("all");
  const [query, setQuery] = React.useState("");
  const [selected, setSelected] = React.useState<Set<number>>(new Set());
  const [tab, setTab] = React.useState<string | null>(null);
  const activeTab = tab ?? "assets";

  const load = React.useCallback(() => {
    api
      .companies()
      .then((cs) => setCompany(cs.find((c) => c.id === companyId) ?? null))
      .catch(() => undefined)
      .finally(() => setLoaded(true));
    api
      .assetsByCompany(companyId)
      .then((r) => setAssets(r.assets))
      .catch(() => undefined)
      .finally(() => setAssetsLoaded(true));
  }, [companyId]);

  React.useEffect(() => {
    load();
  }, [load]);

  const filtered = assets.filter((a) => {
    if (typeFilter !== "all" && a.type !== typeFilter) return false;
    if (!query.trim()) return true;
    const q = query.trim().toLowerCase();
    return assetValue(a).toLowerCase().includes(q) || (a.service_name ?? "").toLowerCase().includes(q);
  });

  const removeSelected = async () => {
    if (selected.size === 0) return;
    try {
      await api.deleteAssets([...selected]);
      toast.success(`已删除 ${selected.size} 个资产`);
      setSelected(new Set());
      load();
    } catch (e) {
      toast.error(`删除失败：${(e as Error).message}`);
    }
  };

  const toggle = (id: number) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const typeCounts = new Map<string, number>();
  for (const a of assets) typeCounts.set(a.type, (typeCounts.get(a.type) ?? 0) + 1);

  if (!company) {
    return (
      <div className="flex flex-1 flex-col items-center justify-center gap-3 p-10 text-center">
        <p className="text-muted-foreground">{loaded ? `企业 #${companyId} 不存在或已删除` : "加载中…"}</p>
        {loaded && (
          <Button asChild variant="outline">
            <Link href="/function/assets">
              <ArrowLeftIcon /> 返回资产
            </Link>
          </Button>
        )}
      </div>
    );
  }

  return (
    <Tabs value={activeTab} onValueChange={setTab} className="flex flex-1 flex-col gap-0">
      {/* Top fixed area — 与任务详情同构 */}
      <header className="bg-background/95 sticky top-0 z-10 flex flex-col gap-2 border-b px-4 py-2.5 backdrop-blur lg:px-6">
        <div className="flex items-center gap-2">
          <SidebarTrigger className="-ml-1" />
          <Button asChild variant="ghost" size="icon" className="size-7">
            <Link href="/function/assets">
              <ArrowLeftIcon />
            </Link>
          </Button>
          <h1 className="min-w-0 flex-1 truncate text-sm font-semibold" title={company.name}>
            {company.name}
          </h1>
          <code className="hidden rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground sm:inline">
            #{company.id}
          </code>
          <Separator orientation="vertical" className="mx-1 hidden h-4 sm:block" />
          <Badge variant="outline">{company.asset_count} 资产</Badge>
          <Button
            size="icon-sm"
            variant="ghost"
            onClick={() => {
              load();
              toast.success("已刷新");
            }}
            aria-label="刷新"
          >
            <RefreshCwIcon />
          </Button>
        </div>
        <p className="truncate text-xs text-muted-foreground">
          {company.scope?.length
            ? `范围:${company.scope.map((s) => s.raw).join(" · ")}`
            : "未配置资产范围"}
        </p>
        <div className="no-scrollbar min-w-0 overflow-x-auto">
          <TabsList variant="default" className="min-w-max">
            <TabsTrigger value="assets">资产({assets.length})</TabsTrigger>
            <TabsTrigger value="graph">关系图</TabsTrigger>
            <TabsTrigger value="scope">范围</TabsTrigger>
          </TabsList>
        </div>
      </header>

      <div className="flex-1 p-4 lg:p-6">
        <TabsContent value="assets" className="mt-0">
          <Card>
            <CardContent className="pt-4">
              <div className="flex flex-wrap items-center gap-1.5">
                {/* 类型条:与主资产页同款,点击筛选,带计数 */}
                <button
                  type="button"
                  onClick={() => setTypeFilter("all")}
                  className={`rounded-full border px-3 py-1 text-xs transition-colors ${
                    typeFilter === "all" ? "border-primary bg-primary text-primary-foreground" : "hover:bg-muted"
                  }`}
                >
                  全部 {assets.length}
                </button>
                {[...typeCounts.entries()]
                  .sort((a, b) => b[1] - a[1])
                  .map(([t, n]) => (
                    <button
                      key={t}
                      type="button"
                      onClick={() => setTypeFilter(t)}
                      className={`rounded-full border px-3 py-1 text-xs transition-colors ${
                        typeFilter === t ? "border-primary bg-primary text-primary-foreground" : "hover:bg-muted"
                      }`}
                    >
                      {ASSET_TYPE_LABEL[t] ?? t} {n}
                    </button>
                  ))}
              </div>
              <div className="flex flex-wrap items-center gap-2">
                <Input
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  placeholder="搜索资产值 / 服务名"
                  className="w-64"
                />
                <span className="text-muted-foreground text-xs tabular-nums">
                  {filtered.length}/{assets.length} 条
                </span>
                <div className="ml-auto">
                  <AlertDialog>
                    <AlertDialogTrigger asChild>
                      <Button variant="outline" size="sm" disabled={selected.size === 0}>
                        <Trash2Icon className="size-4" />
                        删除选中({selected.size})
                      </Button>
                    </AlertDialogTrigger>
                    <AlertDialogContent>
                      <AlertDialogHeader>
                        <AlertDialogTitle>删除 {selected.size} 个资产?</AlertDialogTitle>
                        <AlertDialogDescription>
                          从全局资产库硬删除,不可撤销;关联任务中的引用将失效。
                        </AlertDialogDescription>
                      </AlertDialogHeader>
                      <AlertDialogFooter>
                        <AlertDialogCancel>取消</AlertDialogCancel>
                        <AlertDialogAction onClick={() => void removeSelected()}>删除</AlertDialogAction>
                      </AlertDialogFooter>
                    </AlertDialogContent>
                  </AlertDialog>
                </div>
              </div>
              {assetsLoaded && filtered.length === 0 ? (
                <p className="text-muted-foreground py-12 text-center text-sm">
                  {assets.length === 0
                    ? "该企业还没有资产:建一个信息搜集步骤任务(手动导入/crt.sh/测绘),采纳后进这里"
                    : "没有匹配的记录"}
                </p>
              ) : (
                <div className="max-h-[64vh] overflow-y-auto">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead className="w-10">
                          <input
                            type="checkbox"
                            checked={selected.size > 0 && selected.size === filtered.length}
                            onChange={(e) =>
                              setSelected(e.target.checked ? new Set(filtered.map((a) => a.id)) : new Set())
                            }
                          />
                        </TableHead>
                        <TableHead className="w-24">类型</TableHead>
                        <TableHead>值</TableHead>
                        <TableHead className="w-40">说明</TableHead>
                        <TableHead className="w-40">最近活跃</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {filtered.map((a) => (
                        <TableRow key={a.id}>
                          <TableCell>
                            <input type="checkbox" checked={selected.has(a.id)} onChange={() => toggle(a.id)} />
                          </TableCell>
                          <TableCell>
                            <Badge variant="secondary" className="text-xs">
                              {ASSET_TYPE_LABEL[a.type] ?? a.type}
                            </Badge>
                          </TableCell>
                          <TableCell className="font-mono text-xs">{assetValue(a)}</TableCell>
                          <TableCell className="text-muted-foreground max-w-[240px] truncate text-xs">
                            {a.page_title || a.service_name || a.service_type || a.icp || "—"}
                          </TableCell>
                          <TableCell className="text-muted-foreground text-xs">{fmtTime(a.last_seen)}</TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="graph" className="mt-0">
          <Card>
            <CardContent className="p-0">
              {assets.length === 0 ? (
                <p className="text-muted-foreground py-16 text-center text-sm">
                  资产关系图将在有资产数据后生成(根域名 → 子域名 → IP → Web 服务)
                </p>
              ) : (
                <CompanyAssetGraph assets={assets} className="h-[72vh]" />
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="scope" className="mt-0">
          <Card>
            <CardContent className="pt-4">
              {company.scope?.length ? (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead className="w-28">类型</TableHead>
                      <TableHead>规则值</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {company.scope.map((s) => (
                      <TableRow key={s.id}>
                        <TableCell>
                          <Badge variant="secondary" className="text-xs">
                            {s.kind === "domain" ? "根域名" : s.kind === "ip" || s.kind === "cidr" ? "IP 段" : s.kind}
                          </Badge>
                        </TableCell>
                        <TableCell className="font-mono text-xs">{s.raw}</TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              ) : (
                <p className="text-muted-foreground py-8 text-center text-sm">未配置范围</p>
              )}
            </CardContent>
          </Card>
        </TabsContent>
      </div>
    </Tabs>
  );
}
