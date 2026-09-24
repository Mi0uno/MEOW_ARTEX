"use client";

// 代理组详情:管理一个代理组里的代理条目与接入源(?id=组 id;静态导出没有动态路由)。
//
// 两个页签分工明确:
//   代理列表 —— 手工录入 / 代理池拉取 / 在野搜集来的代理都落在这里,负责筛选、批量清理与测活;
//   接入源   —— 代理池 API 与公益池的接入配置,拉取结果直接进入左侧列表。
// 组本身的信息(类型/策略/端口/测活间隔)提到页面头部,不随页签切换——它属于整个页面。

import * as React from "react";

import Link from "next/link";
import { useSearchParams } from "next/navigation";

import {
  ActivityIcon,
  ArrowLeftIcon,
  ChevronDownIcon,
  ChevronUpIcon,
  DatabaseIcon,
  GlobeIcon,
  Loader2Icon,
  PencilIcon,
  PlugZapIcon,
  PlusIcon,
  RefreshCwIcon,
  SearchIcon,
  Trash2Icon,
} from "lucide-react";
import { toast } from "sonner";

import {
  anonymityLabel,
  fmtLatency,
  groupKindBadgeClass,
  groupKindLabel,
  ProxyEntryDialog,
  ProxySourceDialog,
  proxyStatusBadgeClass,
  sourceLabel,
  strategyLabel,
} from "@/components/proxy-dialogs";
import { TablePagination } from "@/components/table-pagination";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
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
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type {
  ProxyCheckRun,
  ProxyCountryStat,
  ProxyEntry,
  ProxyGroup,
  ProxyProviderDef,
  ProxySource,
} from "@/lib/types";

const PAGE_SIZE = 50;

// 来源徽标:与 ProxyEntry.source 的取值一一对应(manual/api/public/wild)。
// 只在本页用,故不放进共用 dialogs,避免和代理组的 kind 语义混淆。
const SOURCE_BADGE: Record<string, string> = {
  manual: "bg-muted text-muted-foreground",
  api: "bg-blue-500/15 text-blue-600 border-blue-500/30",
  public: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30",
  wild: "bg-purple-500/15 text-purple-600 border-purple-500/30",
};

function sourceBadgeClass(s: string): string {
  return SOURCE_BADGE[s] ?? "bg-muted text-muted-foreground";
}

// 代理源类别徽标:商业 / 公益 / 在野搜集。这是「源」的类别,不是代理组的 kind,
// 两者取值不同(commercial/public/wild vs manual/api/wild/mixed),故本地维护。
const PROVIDER_KIND_META: Record<string, { label: string; badge: string }> = {
  commercial: { label: "商业 API", badge: "bg-blue-500/15 text-blue-600 border-blue-500/30" },
  public: { label: "公益池", badge: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30" },
  wild: { label: "在野搜集", badge: "bg-purple-500/15 text-purple-600 border-purple-500/30" },
};

// 拉取状态徽标(idle/running/ok/failed)。
const SYNC_STATUS_META: Record<string, { label: string; badge: string }> = {
  idle: { label: "未拉取", badge: "bg-muted text-muted-foreground" },
  running: { label: "拉取中", badge: "bg-blue-500/15 text-blue-600 border-blue-500/30" },
  ok: { label: "成功", badge: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30" },
  failed: { label: "失败", badge: "bg-red-500/15 text-red-600 border-red-500/30" },
};

// 测活批次状态徽标(running/done/failed)。
const RUN_STATUS_META: Record<string, { label: string; badge: string }> = {
  running: { label: "进行中", badge: "bg-blue-500/15 text-blue-600 border-blue-500/30" },
  done: { label: "已完成", badge: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30" },
  failed: { label: "失败", badge: "bg-red-500/15 text-red-600 border-red-500/30" },
};

function fmtTime(iso?: string | null): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString();
}

// 代理状态中文名。共用 dialogs 只给了徽标样式,文案在本页自己定(表头/筛选器同一套词)。
const PROXY_STATUS_LABEL: Record<string, string> = {
  untested: "未测",
  alive: "存活",
  dead: "失效",
  checking: "测活中",
};

function proxyStatusLabel(status: string): string {
  return PROXY_STATUS_LABEL[status] ?? status ?? "—";
}

// useSearchParams must sit under a Suspense boundary for static export.
export default function ProxyGroupDetailPage() {
  return (
    <React.Suspense fallback={null}>
      <ProxyGroupDetailInner />
    </React.Suspense>
  );
}

function ProxyGroupDetailInner() {
  const searchParams = useSearchParams();
  const id = Number(searchParams.get("id") ?? "0");
  // 无效 id 必须在此处(任何 hook 之前)返回——把「读 id」与「用 id」拆成两个组件,
  // 才能既早退又不在同一个组件里条件调用 hook。
  if (!id || Number.isNaN(id)) return <MissingGroup />;
  return <ProxyGroupDetailBody groupId={id} />;
}

function MissingGroup() {
  return (
    <div className="p-4 md:p-6">
      <div className="text-muted-foreground flex flex-col items-center gap-3 py-16 text-center text-sm">
        <p>未指定代理组。请从「代理组」列表进入。</p>
        <Button asChild variant="outline" size="sm">
          <Link href="/function/proxy/groups">
            <ArrowLeftIcon className="size-4" />
            返回代理组
          </Link>
        </Button>
      </div>
    </div>
  );
}

function ProxyGroupDetailBody({ groupId }: { groupId: number }) {
  const [tab, setTab] = React.useState("entries");

  const [group, setGroup] = React.useState<ProxyGroup | null>(null);

  const [entries, setEntries] = React.useState<ProxyEntry[]>([]);
  const [total, setTotal] = React.useState(0);
  const [counts, setCounts] = React.useState<Record<string, number>>({});
  const [query, setQuery] = React.useState("");
  const [status, setStatus] = React.useState("all");
  const [source, setSource] = React.useState("all");
  const [protocol, setProtocol] = React.useState("all");
  const [region, setRegion] = React.useState("");
  // country 是地区识别的结果筛选(由出口 IP 解析出的国家码);region 是源站给的原始地区文本。
  const [country, setCountry] = React.useState("all");
  const [countries, setCountries] = React.useState<ProxyCountryStat[]>([]);
  const [geoPending, setGeoPending] = React.useState(0);
  const [resolvingGeo, setResolvingGeo] = React.useState(false);
  const [sort, setSort] = React.useState("created");
  const [sortDesc, setSortDesc] = React.useState(true);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);
  const [loading, setLoading] = React.useState(true);
  const [selected, setSelected] = React.useState<Set<number>>(new Set());
  const [creating, setCreating] = React.useState(false);
  const [editing, setEditing] = React.useState<ProxyEntry | null>(null);
  const [checking, setChecking] = React.useState(false);
  const [syncingAll, setSyncingAll] = React.useState(false);
  const [runs, setRuns] = React.useState<ProxyCheckRun[]>([]);

  const [sources, setSources] = React.useState<ProxySource[]>([]);
  const [providers, setProviders] = React.useState<ProxyProviderDef[]>([]);
  const [sourceLoading, setSourceLoading] = React.useState(true);
  const [sourceDialogOpen, setSourceDialogOpen] = React.useState(false);
  const [editingSource, setEditingSource] = React.useState<ProxySource | null>(null);
  const [syncingID, setSyncingID] = React.useState<number | null>(null);

  const loadGroup = React.useCallback(() => {
    api
      .proxyGroup(groupId)
      .then((g) => setGroup(g ?? null))
      .catch((e) => toast.error(`加载代理组失败:${(e as Error).message}`));
  }, [groupId]);

  const load = React.useCallback(
    (
      q: string,
      st: string,
      src: string,
      proto: string,
      reg: string,
      cc: string,
      srt: string,
      desc: boolean,
      p: number,
      size: number,
    ) => {
      setLoading(true);
      api
        .proxyEntries(groupId, {
          q,
          // "all" 是 UI 哨兵,出网前统一转成空串(空串=不过滤)。
          status: st === "all" ? "" : st,
          source: src === "all" ? "" : src,
          protocol: proto === "all" ? "" : proto,
          region: reg,
          countryCode: cc === "all" ? "" : cc,
          sort: srt,
          desc,
          limit: size,
          offset: (p - 1) * size,
        })
        .then((r) => {
          // 后端对空集合可能回 JSON null(本项目踩过白屏),所有集合落 state 前一律兜底。
          setEntries(r.proxies ?? []);
          setCounts(r.counts ?? {});
          setTotal(r.total);
        })
        .catch((e) => toast.error(`加载代理失败:${(e as Error).message}`))
        .finally(() => setLoading(false));
    },
    [groupId],
  );

  // 地区分布:筛选器选项 + 「还有多少条未识别」。识别进度会随测活/回填变化,故随列表一起刷新。
  const loadCountries = React.useCallback(() => {
    api
      .proxyCountries(groupId)
      .then((r) => {
        setCountries(r.countries ?? []);
        setGeoPending(r.pending ?? 0);
      })
      .catch(() => {
        // 地区分布只影响筛选器选项,失败不该打断主列表。
      });
  }, [groupId]);

  const loadRuns = React.useCallback(() => {
    api
      .proxyCheckRuns(groupId, 10)
      .then((r) => setRuns(r.runs ?? []))
      .catch((e) => toast.error(`加载测活记录失败:${(e as Error).message}`));
  }, [groupId]);

  const loadSources = React.useCallback(() => {
    setSourceLoading(true);
    api
      .proxySources(groupId)
      .then((r) => setSources(r.sources ?? []))
      .catch((e) => toast.error(`加载接入源失败:${(e as Error).message}`))
      .finally(() => setSourceLoading(false));
  }, [groupId]);

  React.useEffect(() => {
    loadGroup();
    loadRuns();
    loadSources();
    // 内置源注册表只用于把 source.provider_key 翻译成类别徽标,失败不影响主流程。
    api
      .proxyProviders()
      .then((r) => setProviders(r ?? []))
      .catch(() => setProviders([]));
  }, [loadGroup, loadRuns, loadSources]);

  React.useEffect(() => {
    load(query, status, source, protocol, region, country, sort, sortDesc, page, pageSize);
    loadCountries();
    // 搜索框与地区框只在点「搜索」/回车时生效(与漏洞库同款),否则每敲一个字都要打一次接口。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status, source, protocol, country, sort, sortDesc, page, pageSize]);

  // 测活在服务端后台跑,触发瞬间只能看到 running 批次;1.5 秒后补拉一次,让结果尽快出现。
  const refreshTimer = React.useRef<number | null>(null);
  React.useEffect(
    () => () => {
      if (refreshTimer.current != null) window.clearTimeout(refreshTimer.current);
    },
    [],
  );

  const search = () => {
    setPage(1);
    setSelected(new Set());
    load(query, status, source, protocol, region, country, sort, sortDesc, 1, pageSize);
  };
  const reload = () =>
    load(query, status, source, protocol, region, country, sort, sortDesc, page, pageSize);

  // 手动触发一轮地区识别。受主动限速约束(约 1.2 秒/出口 IP),单次只取一批,
  // 剩下的由后台定时回填继续跑——所以这里如实告知「还剩多少未识别」。
  const resolveGeo = async () => {
    setResolvingGeo(true);
    try {
      const r = await api.resolveProxyGeo(groupId, 60);
      if (r.resolved === 0 && r.failed === 0) {
        toast.info("没有待识别的代理(需先测活拿到出口 IP)");
      } else {
        toast.success(
          `已识别 ${r.resolved} 个出口地区,更新 ${r.updated} 条代理` +
            (r.pending > 0 ? `,还剩 ${r.pending} 条未识别(后台会继续回填)` : ""),
        );
      }
      reload();
      loadCountries();
    } catch (e) {
      toast.error(`地区识别失败:${(e as Error).message}`);
    } finally {
      setResolvingGeo(false);
    }
  };

  const checkAll = async () => {
    setChecking(true);
    try {
      const r = await api.checkProxies(groupId);
      toast.success(`已开始测活 ${r.checking} 条`);
      loadRuns();
      reload();
      if (refreshTimer.current != null) window.clearTimeout(refreshTimer.current);
      refreshTimer.current = window.setTimeout(() => {
        loadRuns();
        reload();
        loadGroup();
      }, 1500);
    } catch (e) {
      toast.error(`测活失败:${(e as Error).message}`);
    } finally {
      setChecking(false);
    }
  };

  const syncAll = async () => {
    setSyncingAll(true);
    try {
      const r = await api.syncAllProxySources(groupId);
      toast.success(`已接入 ${r.added} 个免凭据源`);
      loadSources();
      loadGroup();
    } catch (e) {
      toast.error(`一键接入失败:${(e as Error).message}`);
    } finally {
      setSyncingAll(false);
    }
  };

  const removeEntry = async (p: ProxyEntry) => {
    if (!window.confirm(`删除代理「${p.url}」?不可恢复。`)) return;
    try {
      await api.deleteProxy(p.id);
      toast.success("已删除");
      setSelected((prev) => {
        const next = new Set(prev);
        next.delete(p.id);
        return next;
      });
      reload();
    } catch (e) {
      toast.error(`删除失败:${(e as Error).message}`);
    }
  };

  const removeSelected = async () => {
    const ids = [...selected];
    if (ids.length === 0) return;
    if (!window.confirm(`删除选中的 ${ids.length} 条代理?不可恢复。`)) return;
    try {
      const r = await api.deleteProxies(groupId, { ids });
      toast.success(`已删除 ${r.deleted} 条`);
      setSelected(new Set());
      reload();
    } catch (e) {
      toast.error(`删除失败:${(e as Error).message}`);
    }
  };

  // 清空失效:按状态清理,不受当前页选择影响(失效代理可能散落在后面几页)。
  const clearDead = async () => {
    if (!window.confirm("清空本组全部失效代理?不可恢复。")) return;
    try {
      const r = await api.deleteProxies(groupId, { status: "dead" });
      toast.success(`已清空 ${r.deleted} 条失效代理`);
      setSelected(new Set());
      reload();
    } catch (e) {
      toast.error(`清空失败:${(e as Error).message}`);
    }
  };

  const syncOne = async (s: ProxySource) => {
    setSyncingID(s.id);
    try {
      const r = await api.syncProxySource(s.id);
      toast.success(`已导入 ${r.imported} 条`);
      // 新代理可能刚入库,两张表都要刷新:源行显示本次拉取结果,代理列表显示新条目。
      loadSources();
      reload();
      loadGroup();
    } catch (e) {
      toast.error(`拉取失败:${(e as Error).message}`);
      loadSources();
    } finally {
      setSyncingID(null);
    }
  };

  const removeSource = async (s: ProxySource) => {
    if (!window.confirm(`删除接入源「${s.name}」?已导入的代理不受影响,但不再自动拉取。`)) return;
    try {
      await api.deleteProxySource(s.id);
      toast.success("已删除");
      loadSources();
      loadGroup();
    } catch (e) {
      toast.error(`删除失败:${(e as Error).message}`);
    }
  };

  const toggleOne = (id: number) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  // 表头全选:按「本页行是否都已选中」判断,而不是比 size——跨页选中时 size 会失真。
  const allSelected = entries.length > 0 && entries.every((p) => selected.has(p.id));
  const toggleAll = () => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (entries.every((p) => next.has(p.id))) {
        for (const p of entries) next.delete(p.id);
      } else {
        for (const p of entries) next.add(p.id);
      }
      return next;
    });
  };

  const SortHead = ({ field, width, children }: { field: string; width?: string; children: React.ReactNode }) => (
    <TableHead className={width}>
      <button
        type="button"
        onClick={() => {
          setPage(1);
          if (sort === field) setSortDesc((d) => !d);
          else {
            setSort(field);
            setSortDesc(field !== "created");
          }
        }}
        className="hover:text-foreground inline-flex items-center gap-1"
      >
        {children}
        {sort === field ? (
          sortDesc ? (
            <ChevronDownIcon className="size-3" />
          ) : (
            <ChevronUpIcon className="size-3" />
          )
        ) : null}
      </button>
    </TableHead>
  );

  const providerOf = (key: string) => providers.find((p) => p.key === key) ?? null;

  // 头部元信息:类型/策略/端口/测活间隔一行带过(类型用徽标另显,故不在文本里重复)。
  const meta = group
    ? [
        strategyLabel(group.strategy),
        `监听 ${group.listen_port > 0 ? group.listen_port : "自动"}`,
        `测活 ${group.check_interval_minutes > 0 ? `${group.check_interval_minutes} 分钟` : "手动"}`,
        `并发 ${group.concurrency}`,
      ].join(" · ")
    : "";

  const entriesBody =
    loading && entries.length === 0 ? (
      <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
    ) : entries.length === 0 ? (
      <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
        <ActivityIcon className="size-8 opacity-40" />
        <span>
          {query || region || country !== "all" || status !== "all" || source !== "all" || protocol !== "all"
            ? "没有匹配的代理"
            : "本组还没有代理。点「新增代理」手工录入,或到「接入源」页签接入代理池自动拉取。"}
        </span>
      </div>
    ) : (
      <>
        <Table className="table-fixed">
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="w-10">
                <Checkbox checked={allSelected} onCheckedChange={toggleAll} aria-label="全选本页" />
              </TableHead>
              {/* 地址列不设宽度:table-fixed 下自动吃掉余量(设 w-full 会挤掉后面的列)。
                  其余列的固定宽度之和必须明显小于容器宽,否则地址列会被压到只剩协议前缀
                  (实测 984px 固定宽 + 1057px 容器 → 地址列仅 73px,还会撑出横向滚动条;
                  1280 视口下容器仅 ~897px,故固定列收到 712px,给地址留出可读宽度)。
                  故不单列「协议」:url 本身就带 scheme(socks5://…),重复展示只会挤掉地址。 */}
              <TableHead>代理地址</TableHead>
              <TableHead className="w-16">来源</TableHead>
              {/* 国家/地区:优先显示地区识别的结果(country),未识别时退回源站给的原始 region 文本。
                  两列合并成一列是刻意的——固定列再加一列会挤掉地址列的宽度(见上方注释)。 */}
              <TableHead className="w-20">国家/地区</TableHead>
              <TableHead className="w-16">状态</TableHead>
              <SortHead field="latency" width="w-16">
                延迟
              </SortHead>
              <TableHead className="w-24">出口 IP</TableHead>
              <TableHead className="w-16">匿名度</TableHead>
              <TableHead className="w-16">调度次数</TableHead>
              <SortHead field="checked" width="w-32">
                最后测活
              </SortHead>
              <TableHead className="w-16">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {entries.map((p) => (
              <TableRow key={p.id}>
                <TableCell>
                  <Checkbox checked={selected.has(p.id)} onCheckedChange={() => toggleOne(p.id)} />
                </TableCell>
                {/* 地址列:table-fixed + truncate 保证超长 url 不溢出到相邻列 */}
                <TableCell className="min-w-0">
                  <span className="block w-full truncate font-mono text-xs" title={p.url}>
                    {p.url}
                  </span>
                </TableCell>
                <TableCell>
                  <Badge variant="outline" className={sourceBadgeClass(p.source)}>
                    {sourceLabel(p.source)}
                  </Badge>
                </TableCell>
                {/* 国家/地区:识别出国家时显示「国家名 + 国家码」,否则退回源站原始地区文本。 */}
                <TableCell
                  className="truncate text-xs"
                  title={p.country ? `${p.country}(${p.country_code})` : p.region || "未识别"}
                >
                  {p.country ? (
                    <span className="inline-flex items-center gap-1">
                      <span className="truncate">{p.country}</span>
                      <span className="text-muted-foreground shrink-0 font-mono">{p.country_code}</span>
                    </span>
                  ) : p.region ? (
                    <span className="text-muted-foreground truncate">{p.region}</span>
                  ) : (
                    <span className="text-muted-foreground">未识别</span>
                  )}
                </TableCell>
                <TableCell>
                  <Badge variant="outline" className={proxyStatusBadgeClass(p.status)}>
                    {proxyStatusLabel(p.status)}
                  </Badge>
                </TableCell>
                <TableCell className="tabular-nums">{fmtLatency(p.latency_ms)}</TableCell>
                <TableCell className="text-muted-foreground truncate font-mono text-xs" title={p.exit_ip || "—"}>
                  {p.exit_ip || "—"}
                </TableCell>
                <TableCell className="text-muted-foreground text-xs">{anonymityLabel(p.anonymity)}</TableCell>
                <TableCell className="tabular-nums">{p.use_count}</TableCell>
                <TableCell className="text-muted-foreground text-xs">{fmtTime(p.last_checked_at)}</TableCell>
                <TableCell>
                  <div className="flex items-center gap-1">
                    <Button variant="ghost" size="sm" onClick={() => setEditing(p)} title="编辑">
                      <PencilIcon className="size-3.5" />
                    </Button>
                    <Button variant="ghost" size="sm" onClick={() => void removeEntry(p)} title="删除">
                      <Trash2Icon className="text-muted-foreground size-3.5" />
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
    );

  const sourcesBody =
    sourceLoading && sources.length === 0 ? (
      <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
    ) : sources.length === 0 ? (
      <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
        <DatabaseIcon className="size-8 opacity-40" />
        <span>还没有接入源。点「添加接入」逐个配置,或点右上角「一键接入公益池」把免凭据源一次挂上。</span>
      </div>
    ) : (
      <Table className="table-fixed">
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>名称</TableHead>
            <TableHead className="w-24">类型</TableHead>
            <TableHead>API 地址</TableHead>
            <TableHead className="w-24">凭据</TableHead>
            <TableHead className="w-24">拉取间隔</TableHead>
            <TableHead className="w-40">上次拉取</TableHead>
            <TableHead className="w-32">结果</TableHead>
            <TableHead className="w-32">操作</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {sources.map((s) => {
            const prov = providerOf(s.provider_key);
            const kindMeta = prov ? PROVIDER_KIND_META[prov.kind] : null;
            const syncMeta = SYNC_STATUS_META[s.last_status] ?? SYNC_STATUS_META.idle;
            return (
              <TableRow key={s.id}>
                <TableCell className="min-w-0">
                  <div className="truncate font-medium" title={s.name}>
                    {s.name || "—"}
                  </div>
                  {s.provider_key ? (
                    <div className="text-muted-foreground truncate text-xs">{s.provider_key}</div>
                  ) : null}
                </TableCell>
                <TableCell>
                  {kindMeta ? (
                    <Badge variant="outline" className={kindMeta.badge}>
                      {kindMeta.label}
                    </Badge>
                  ) : (
                    <span className="text-muted-foreground text-xs">{s.provider_key || "自定义"}</span>
                  )}
                </TableCell>
                <TableCell className="text-muted-foreground truncate font-mono text-xs" title={s.api_url}>
                  {s.api_url || "—"}
                </TableCell>
                <TableCell>
                  {/* 密文永不回显,只看存在性标记——凭据缺失时拉取必然失败,得让用户一眼看到。 */}
                  <div className="flex flex-wrap gap-1">
                    <Badge
                      variant="outline"
                      className={s.has_api_key ? "bg-emerald-500/15 text-emerald-600 border-emerald-500/30" : ""}
                    >
                      Key {s.has_api_key ? "已配置" : "未配置"}
                    </Badge>
                    <Badge
                      variant="outline"
                      className={s.has_password ? "bg-emerald-500/15 text-emerald-600 border-emerald-500/30" : ""}
                    >
                      密码 {s.has_password ? "已配置" : "未配置"}
                    </Badge>
                  </div>
                </TableCell>
                <TableCell className="text-muted-foreground text-xs">
                  {s.interval_minutes === 0 ? "手动" : `${s.interval_minutes} 分钟`}
                </TableCell>
                <TableCell>
                  <div className="min-w-0">
                    <div className="flex items-center gap-1">
                      <span className="text-muted-foreground text-xs tabular-nums">{fmtTime(s.last_sync_at)}</span>
                      <Badge variant="outline" className={syncMeta.badge}>
                        {syncMeta.label}
                      </Badge>
                    </div>
                    {s.last_error ? (
                      <div className="truncate text-red-600 text-xs" title={s.last_error}>
                        {s.last_error}
                      </div>
                    ) : null}
                  </div>
                </TableCell>
                <TableCell className="text-muted-foreground text-xs tabular-nums">
                  导入 {s.imported_count} / 共 {s.total_count}
                </TableCell>
                <TableCell>
                  <div className="flex items-center gap-1">
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => void syncOne(s)}
                      disabled={syncingID === s.id}
                      title="立即拉取"
                    >
                      {syncingID === s.id ? (
                        <Loader2Icon className="size-3.5 animate-spin" />
                      ) : (
                        <RefreshCwIcon className="size-3.5" />
                      )}
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => {
                        setEditingSource(s);
                        setSourceDialogOpen(true);
                      }}
                      title="编辑"
                    >
                      <PencilIcon className="size-3.5" />
                    </Button>
                    <Button variant="ghost" size="sm" onClick={() => void removeSource(s)} title="删除">
                      <Trash2Icon className="text-muted-foreground size-3.5" />
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    );

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <h1 className="font-semibold text-xl">{group ? group.name : "代理组详情"}</h1>
          <div className="mt-1 flex flex-wrap items-center gap-2">
            {group ? (
              <Badge variant="outline" className={groupKindBadgeClass(group.kind)}>
                {groupKindLabel(group.kind)}
              </Badge>
            ) : null}
            <span className="text-muted-foreground text-xs">{meta || "加载中…"}</span>
            {group ? (
              <span className="text-muted-foreground text-xs tabular-nums">
                代理 {group.proxy_count} · 存活 {group.alive_count} · 失效 {group.dead_count}
              </span>
            ) : null}
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="outline" size="sm" asChild>
            <Link href="/function/proxy/groups">
              <ArrowLeftIcon className="size-4" />
              返回
            </Link>
          </Button>
          <Button size="sm" onClick={() => setCreating(true)}>
            <PlusIcon className="size-4" />
            新增代理
          </Button>
          <Button variant="outline" size="sm" onClick={() => void checkAll()} disabled={checking}>
            {checking ? <Loader2Icon className="size-4 animate-spin" /> : <ActivityIcon className="size-4" />}
            立即测活
          </Button>
          <Button variant="outline" size="sm" onClick={() => void syncAll()} disabled={syncingAll}>
            {syncingAll ? <Loader2Icon className="size-4 animate-spin" /> : <PlugZapIcon className="size-4" />}
            一键接入公益池
          </Button>
        </div>
      </div>

      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="entries">代理列表 {total}</TabsTrigger>
          <TabsTrigger value="sources">接入源 {sources.length}</TabsTrigger>
        </TabsList>

        <TabsContent value="entries" className="mt-4">
          <Card>
            <CardContent className="px-0 pt-4">
              <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
                <div className="relative w-full sm:max-w-xs">
                  <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
                  <Input
                    value={query}
                    onChange={(e) => setQuery(e.target.value)}
                    onKeyDown={(e) => e.key === "Enter" && search()}
                    placeholder="搜索地址 / 标签 / 出口 IP"
                    className="pl-8"
                  />
                </div>
                <Button variant="outline" size="sm" onClick={search}>
                  搜索
                </Button>
                <Select
                  value={status}
                  onValueChange={(v) => {
                    setPage(1);
                    setStatus(v);
                  }}
                >
                  <SelectTrigger className="w-28">
                    <SelectValue placeholder="状态" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      <SelectItem value="all">全部状态</SelectItem>
                      <SelectItem value="untested">未测</SelectItem>
                      <SelectItem value="alive">存活</SelectItem>
                      <SelectItem value="dead">失效</SelectItem>
                      <SelectItem value="checking">测活中</SelectItem>
                    </SelectGroup>
                  </SelectContent>
                </Select>
                <Select
                  value={source}
                  onValueChange={(v) => {
                    setPage(1);
                    setSource(v);
                  }}
                >
                  <SelectTrigger className="w-32">
                    <SelectValue placeholder="来源" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      <SelectItem value="all">全部来源</SelectItem>
                      <SelectItem value="manual">手工</SelectItem>
                      <SelectItem value="api">代理池 API</SelectItem>
                      <SelectItem value="public">公益池</SelectItem>
                      <SelectItem value="wild">在野搜集</SelectItem>
                    </SelectGroup>
                  </SelectContent>
                </Select>
                <Select
                  value={protocol}
                  onValueChange={(v) => {
                    setPage(1);
                    setProtocol(v);
                  }}
                >
                  <SelectTrigger className="w-28">
                    <SelectValue placeholder="协议" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      <SelectItem value="all">全部协议</SelectItem>
                      <SelectItem value="http">http</SelectItem>
                      <SelectItem value="https">https</SelectItem>
                      <SelectItem value="socks5">socks5</SelectItem>
                    </SelectGroup>
                  </SelectContent>
                </Select>
                <Input
                  value={region}
                  onChange={(e) => setRegion(e.target.value)}
                  onKeyDown={(e) => e.key === "Enter" && search()}
                  placeholder="地区"
                  className="w-24"
                />
                {/* 国家筛选:选项来自地区识别的聚合结果(按代理数降序),带存活数便于挑可用出口。 */}
                <Select
                  value={country}
                  onValueChange={(v) => {
                    setPage(1);
                    setCountry(v);
                  }}
                >
                  <SelectTrigger className="w-40">
                    <SelectValue placeholder="国家" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      <SelectItem value="all">全部国家</SelectItem>
                      {countries.map((c) => (
                        <SelectItem key={c.country_code} value={c.country_code}>
                          {c.country || c.country_code}({c.country_code}) · {c.total}
                          {c.alive > 0 ? ` / 存活 ${c.alive}` : ""}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={resolvingGeo}
                  onClick={() => void resolveGeo()}
                  title="由出口 IP 解析代理所在国家/地区(受接口限速,单次只跑一批,其余由后台回填)"
                >
                  <GlobeIcon className="size-3.5" />
                  {resolvingGeo ? "识别中…" : "识别地区"}
                </Button>
                {geoPending > 0 ? (
                  <span className="text-muted-foreground text-xs tabular-nums" title="已拿到出口 IP、等待识别国家的代理数">
                    待识别 {geoPending}
                  </span>
                ) : null}
                <div className="flex items-center gap-2 text-xs">
                  <Badge variant="outline" className={proxyStatusBadgeClass("alive")}>
                    存活 {counts.alive ?? 0}
                  </Badge>
                  <Badge variant="outline" className={proxyStatusBadgeClass("dead")}>
                    失效 {counts.dead ?? 0}
                  </Badge>
                  <Badge variant="outline" className={proxyStatusBadgeClass("untested")}>
                    未测 {counts.untested ?? 0}
                  </Badge>
                </div>
                <span className="text-muted-foreground ml-auto text-xs tabular-nums">共 {total} 条</span>
              </div>

              {/* 批量操作:选中行后才有意义,未选中时按钮禁用而不是隐藏——位置稳定,不会让表格跳动。 */}
              <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
                <span className="text-muted-foreground text-xs tabular-nums">已选 {selected.size} 条</span>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={selected.size === 0}
                  onClick={() => void removeSelected()}
                >
                  <Trash2Icon className="size-3.5" />
                  删除选中
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={selected.size === 0 || (counts.dead ?? 0) === 0}
                  onClick={() => void clearDead()}
                >
                  清空失效{counts.dead ? ` (${counts.dead})` : ""}
                </Button>
              </div>

              {entriesBody}
            </CardContent>
          </Card>

          <Card className="mt-4">
            <CardHeader>
              <CardTitle className="text-base">最近测活批次</CardTitle>
            </CardHeader>
            <CardContent>
              {runs.length === 0 ? (
                <p className="text-muted-foreground text-sm">还没有测活记录。点「立即测活」跑一轮。</p>
              ) : (
                <ul className="flex flex-col divide-y">
                  {runs.map((r) => {
                    const runMeta = RUN_STATUS_META[r.status] ?? RUN_STATUS_META.done;
                    return (
                      <li key={r.id} className="flex flex-wrap items-center gap-2 py-2 text-xs">
                        <span className="text-muted-foreground tabular-nums">{fmtTime(r.started_at)}</span>
                        <Badge
                          variant="outline"
                          className={r.trigger === "auto" ? "" : "bg-muted text-muted-foreground"}
                        >
                          {r.trigger === "auto" ? "定时" : "手动"}
                        </Badge>
                        <Badge variant="outline" className={runMeta.badge}>
                          {runMeta.label}
                        </Badge>
                        <span className="text-muted-foreground tabular-nums">
                          检查 {r.checked} · 存活 {r.alive} · 失效 {r.dead}
                        </span>
                        {r.error ? (
                          <span className="min-w-0 truncate text-red-600" title={r.error}>
                            {r.error}
                          </span>
                        ) : null}
                      </li>
                    );
                  })}
                </ul>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="sources" className="mt-4">
          <p className="text-muted-foreground mb-3 text-sm">
            这里接入主流代理池 API 与公益代理池 API:填好地址与凭据后由平台按间隔拉取,拉到的代理自动进入「代理列表」;
            全网在野代理搜集源(公开清单 / 网页抓取)也挂在这一页,免凭据的可以直接「一键接入公益池」。
          </p>
          <Card>
            <CardContent className="px-0 pt-4">
              <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
                <span className="text-muted-foreground text-xs tabular-nums">共 {sources.length} 个接入源</span>
                <Button
                  size="sm"
                  className="ml-auto"
                  onClick={() => {
                    setEditingSource(null);
                    setSourceDialogOpen(true);
                  }}
                >
                  <PlusIcon className="size-4" />
                  添加接入
                </Button>
              </div>
              {sourcesBody}
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>

      <ProxyEntryDialog
        groupId={groupId}
        open={creating}
        onOpenChange={setCreating}
        onSaved={() => {
          setCreating(false);
          reload();
          loadGroup();
        }}
      />
      <ProxySourceDialog
        groupId={groupId}
        source={editingSource}
        open={sourceDialogOpen}
        onOpenChange={(o: boolean) => {
          setSourceDialogOpen(o);
          if (!o) setEditingSource(null);
        }}
        onSaved={() => {
          setSourceDialogOpen(false);
          setEditingSource(null);
          loadSources();
          loadGroup();
        }}
      />
      <ProxyEditDialog
        proxy={editing}
        open={!!editing}
        onOpenChange={(o) => !o && setEditing(null)}
        onSaved={() => {
          setEditing(null);
          reload();
        }}
      />
    </div>
  );
}

// ProxyEditDialog 是本页专用的轻量编辑框(共用对话框是「新增」语义,不适合改状态)。
// 只放开标签/地区/状态三个字段:地址与协议改了等于换了一条代理,应该删掉重加。
function ProxyEditDialog({
  proxy,
  open,
  onOpenChange,
  onSaved,
}: {
  proxy: ProxyEntry | null;
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onSaved: () => void;
}) {
  const [label, setLabel] = React.useState("");
  const [region, setRegion] = React.useState("");
  const [status, setStatus] = React.useState("untested");
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    if (!open || !proxy) return;
    setLabel(proxy.label ?? "");
    setRegion(proxy.region ?? "");
    setStatus(proxy.status || "untested");
  }, [open, proxy]);

  const submit = async () => {
    if (!proxy) return;
    setBusy(true);
    try {
      await api.updateProxy(proxy.id, { label, region, status });
      toast.success("已保存");
      onSaved();
    } catch (e) {
      toast.error(`保存失败:${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>编辑代理</DialogTitle>
          <DialogDescription className="truncate font-mono text-xs">{proxy?.url ?? ""}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid gap-1.5">
            <Label htmlFor="proxy-label">标签</Label>
            <Input
              id="proxy-label"
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              placeholder="备注名,便于识别"
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="proxy-region">地区</Label>
            <Input
              id="proxy-region"
              value={region}
              onChange={(e) => setRegion(e.target.value)}
              placeholder="如 国内 / 美国"
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="proxy-status">状态</Label>
            <Select value={status} onValueChange={setStatus}>
              <SelectTrigger id="proxy-status" className="w-full">
                <SelectValue placeholder="状态" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="untested">未测</SelectItem>
                  <SelectItem value="alive">存活</SelectItem>
                  <SelectItem value="dead">失效</SelectItem>
                  <SelectItem value="checking">测活中</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button onClick={() => void submit()} disabled={busy}>
            保存
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
