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
import { Checkbox } from "@/components/ui/checkbox";
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
import { AssetImportDialog } from "@/components/asset-import-dialog";
import { ReconGraph } from "@/components/recon-graph";
import { assetsToTaskGraph } from "@/components/asset-graph";
import { StatusBadge } from "@/components/status-badge";
import { TablePagination } from "@/components/table-pagination";
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
      {id ? <CompanyDetailInner key={id} companyId={Number(id)} /> : <MissingId />}
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
  // 资产列表走服务端 分页/分类/搜索,不再一次拉全——后端单页有上限,拉全会被静默截断。
  const [assets, setAssets] = React.useState<Asset[]>([]);
  const [total, setTotal] = React.useState(0);
  const [counts, setCounts] = React.useState<Record<string, number>>({});
  const [assetsLoaded, setAssetsLoaded] = React.useState(false);
  const [typeFilter, setTypeFilter] = React.useState<string>("all");
  const [query, setQuery] = React.useState("");
  const [search, setSearch] = React.useState("");
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(50);
  const [selected, setSelected] = React.useState<Set<number>>(new Set());
  const [tab, setTab] = React.useState<string | null>(null);
  const activeTab = tab ?? "assets";
  // 关系图需要全量资产(列表只取当前页):首次切到关系图时分页循环拉齐。
  const [graphAssets, setGraphAssets] = React.useState<Asset[]>([]);
  const [graphState, setGraphState] = React.useState<"idle" | "loading" | "done" | "error">("idle");

  const loadCompany = React.useCallback(() => {
    api
      .companies()
      .then((cs) => setCompany(cs.find((c) => c.id === companyId) ?? null))
      .catch(() => undefined)
      .finally(() => setLoaded(true));
  }, [companyId]);

  const reqSeq = React.useRef(0);
  const loadAssets = React.useCallback(
    (t: string, kw: string, p: number, size: number) => {
      const seq = ++reqSeq.current;
      api
        .assetsByCompany(companyId, { type: t === "all" ? "" : t, q: kw, limit: size, offset: (p - 1) * size })
        .then((r) => {
          if (seq !== reqSeq.current) return;
          setAssets(r.assets);
          setTotal(r.total);
          setCounts(r.counts);
          setAssetsLoaded(true);
          // 删除/切筛选后当前页可能越过末页:回跳最后一页。
          if (r.assets.length === 0 && p > 1 && r.total > 0) {
            setPage(Math.max(1, Math.ceil(r.total / size)));
          }
        })
        .catch(() => setAssetsLoaded(true));
    },
    [companyId],
  );

  React.useEffect(() => {
    loadCompany();
  }, [loadCompany]);
  React.useEffect(() => {
    loadAssets(typeFilter, search, page, pageSize);
  }, [loadAssets, typeFilter, search, page, pageSize]);

  // 搜索词 400ms 防抖上服务端;每次新搜索回到第 1 页。
  React.useEffect(() => {
    const t = setTimeout(() => {
      setSearch(query.trim());
      setPage(1);
    }, 400);
    return () => clearTimeout(t);
  }, [query]);

  const graphBusyRef = React.useRef(false);
  const loadGraph = React.useCallback(async () => {
    if (graphBusyRef.current) return;
    graphBusyRef.current = true;
    setGraphState("loading");
    try {
      const out: Asset[] = [];
      const size = 200; // 后端单页上限
      for (let offset = 0; ; offset += size) {
        const r = await api.assetsByCompany(companyId, { limit: size, offset });
        out.push(...r.assets);
        if (r.assets.length < size || out.length >= r.total) break;
      }
      setGraphAssets(out);
      setGraphState("done");
    } catch {
      setGraphState("error");
    } finally {
      graphBusyRef.current = false;
    }
  }, [companyId]);

  React.useEffect(() => {
    if (activeTab === "graph") void loadGraph();
  }, [activeTab, loadGraph]);

  const reloadNow = () => {
    loadCompany();
    loadAssets(typeFilter, search, page, pageSize);
    setGraphAssets([]);
    setGraphState("idle");
    if (activeTab === "graph") void loadGraph();
  };

  const removeSelected = async () => {
    if (selected.size === 0) return;
    try {
      await api.deleteAssets([...selected]);
      toast.success(`已删除 ${selected.size} 个资产`);
      setSelected(new Set());
      loadAssets(typeFilter, search, page, pageSize);
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

  // 单行删除：走与批量删除相同的硬删除接口，成功后把该 id 从选中集合摘掉再刷新。
  const deleteOne = async (id: number) => {
    try {
      await api.deleteAssets([id]);
      toast.success("已删除 1 个资产");
      setSelected((prev) => {
        const next = new Set(prev);
        next.delete(id);
        return next;
      });
      loadAssets(typeFilter, search, page, pageSize);
    } catch (e) {
      toast.error(`删除失败：${(e as Error).message}`);
    }
  };

  // 分类计数来自后端全量聚合,不受当前类型/搜索筛选影响(分类器铁律)。
  const allCount = Object.values(counts).reduce((a, b) => a + b, 0);
  const typePills = Object.entries(counts).sort((a, b) => b[1] - a[1]);
  const isFiltered = typeFilter !== "all" || search !== "";

  // 全选范围 = 当前页行集合（与资产主页按当前页全选的语义一致）。
  const allFilteredSelected = assets.length > 0 && assets.every((a) => selected.has(a.id));
  const someFilteredSelected = assets.some((a) => selected.has(a.id));

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
              reloadNow();
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
            <TabsTrigger value="assets">资产({allCount})</TabsTrigger>
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
                {/* 类型条:与主资产页同款,点击筛选,带计数(后端全量聚合,不随筛选/搜索变化) */}
                <button
                  type="button"
                  onClick={() => {
                    setPage(1);
                    setTypeFilter("all");
                  }}
                  className={`rounded-full border px-3 py-1 text-xs transition-colors ${
                    typeFilter === "all" ? "border-primary bg-primary text-primary-foreground" : "hover:bg-muted"
                  }`}
                >
                  全部 {allCount}
                </button>
                {typePills.map(([t, n]) => (
                  <button
                    key={t}
                    type="button"
                    onClick={() => {
                      setPage(1);
                      setTypeFilter(t);
                    }}
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
                  {isFiltered ? `命中 ${total} / ` : ""}共 {allCount} 条
                </span>
                <div className="ml-auto flex items-center gap-2">
                  {/* 给该企业上传文件添加资产：导入结果强制归属当前企业，关闭后刷新列表 */}
                  <AssetImportDialog companyId={company.id} onImported={reloadNow} />
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
              {assetsLoaded && assets.length === 0 ? (
                <p className="text-muted-foreground py-12 text-center text-sm">
                  {total === 0 && !isFiltered
                    ? "该企业还没有资产:建一个信息搜集步骤任务(手动导入/crt.sh/测绘),采纳后进这里"
                    : "没有匹配的记录"}
                </p>
              ) : (
                <>
                  <div className="max-h-[64vh] overflow-y-auto">
                    <Table>
                      <TableHeader>
                        <TableRow>
                          <TableHead className="w-10">
                            {/* checkbox 自身就是按钮，点击不会带出行上其他语义（本页行无跳转） */}
                            <Checkbox
                              checked={allFilteredSelected ? true : someFilteredSelected ? "indeterminate" : false}
                              onCheckedChange={(checked) =>
                                setSelected(checked === true ? new Set(assets.map((a) => a.id)) : new Set())
                              }
                              aria-label="全选本页"
                            />
                          </TableHead>
                          <TableHead className="w-24">类型</TableHead>
                          <TableHead>值</TableHead>
                          <TableHead className="w-40">说明</TableHead>
                          <TableHead className="w-40" title="资产首次入库(created_at)时间">
                            首次发现
                          </TableHead>
                          <TableHead className="w-40">最近活跃</TableHead>
                          <TableHead className="w-10" />
                        </TableRow>
                      </TableHeader>
                      <TableBody>
                        {assets.map((a) => (
                          <TableRow key={a.id}>
                            <TableCell className="w-10 pr-0">
                              <Checkbox
                                checked={selected.has(a.id)}
                                onCheckedChange={() => toggle(a.id)}
                                aria-label={`选择资产 ${assetValue(a)}`}
                              />
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
                            <TableCell className="text-muted-foreground text-xs" title={fmtTime(a.created_at)}>
                              {fmtTime(a.created_at)}
                            </TableCell>
                            <TableCell className="text-muted-foreground text-xs">{fmtTime(a.last_seen)}</TableCell>
                            <TableCell className="w-10 pl-0 text-right">
                              <AlertDialog>
                                <AlertDialogTrigger asChild>
                                  <Button
                                    variant="ghost"
                                    size="icon"
                                    className="size-7 text-muted-foreground hover:text-destructive"
                                    aria-label={`删除资产 ${assetValue(a)}`}
                                  >
                                    <Trash2Icon className="size-3.5" />
                                  </Button>
                                </AlertDialogTrigger>
                                <AlertDialogContent>
                                  <AlertDialogHeader>
                                    <AlertDialogTitle>删除该资产?</AlertDialogTitle>
                                    <AlertDialogDescription>
                                      从全局资产库硬删除 {assetValue(a)}，不可撤销；关联任务中的引用将失效。
                                    </AlertDialogDescription>
                                  </AlertDialogHeader>
                                  <AlertDialogFooter>
                                    <AlertDialogCancel>取消</AlertDialogCancel>
                                    <AlertDialogAction onClick={() => void deleteOne(a.id)}>删除</AlertDialogAction>
                                  </AlertDialogFooter>
                                </AlertDialogContent>
                              </AlertDialog>
                            </TableCell>
                          </TableRow>
                        ))}
                      </TableBody>
                    </Table>
                  </div>
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
        </TabsContent>

        <TabsContent value="graph" className="mt-0">
          <Card>
            <CardContent className="p-0">
              {graphState !== "done" ? (
                <p className="text-muted-foreground py-16 text-center text-sm">
                  {graphState === "loading"
                    ? "正在拉取全量资产生成关系图…"
                    : graphState === "error"
                      ? "关系图数据加载失败，点右上角刷新重试"
                      : "资产关系图将在有资产数据后生成(根域名 → 子域名 → IP → Web 服务)"}
                </p>
              ) : graphAssets.length === 0 ? (
                <p className="text-muted-foreground py-16 text-center text-sm">
                  资产关系图将在有资产数据后生成(根域名 → 子域名 → IP → Web 服务)
                </p>
              ) : (
                <CompanyAssetGraph assets={graphAssets} className="h-[72vh]" />
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
