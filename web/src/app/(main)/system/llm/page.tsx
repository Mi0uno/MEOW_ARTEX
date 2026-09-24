"use client";

// LLM:全 Agent 共享的模型配置列表。
//
// 这一页只做「找到某条配置」与轻量操作(设为激活 / 快速改 / 删除);每条配置的完整
// 编辑(拉模型列表、代理、思考参数、重试覆盖、运行时熔断状态)收在详情页(点名称进入)。
// 轮询(故障转移)是全局状态而非单条属性,所以那个抽屉留在这一页。

import * as React from "react";

import Link from "next/link";

import { ArrowRightIcon, PencilIcon, PlusIcon, SearchIcon, StarIcon, Trash2Icon, ZapIcon } from "lucide-react";
import { toast } from "sonner";

import { TablePagination } from "@/components/table-pagination";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { LLMPoolMember, LLMPoolStatus, LLMProfile } from "@/lib/types";
import { cn } from "@/lib/utils";

import {
  contextWindowIsDefault,
  contextWindowText,
  FORMAT_LABEL_LONG,
  FORMAT_LABEL_SHORT,
  formatBadgeClass,
  healthOf,
  isTripped,
  LLM_FORMATS,
} from "./_components/llm-shared";
import { PoolSheet } from "./_components/pool-sheet";
import { ProfileSheet } from "./_components/profile-sheet";
import { RetryPolicyPanel } from "./_components/retry";

const PAGE_SIZE = 20;

/** 状态筛选。熔断状态依赖轮询数据，轮询关着时不会有熔断记录。 */
const STATUS_OPTIONS = [
  { value: "all", label: "全部状态" },
  { value: "active", label: "已激活" },
  { value: "tripped", label: "熔断中" },
];

export default function LLMPage() {
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [pool, setPool] = React.useState<LLMPoolStatus | null>(null);
  const [poolOpen, setPoolOpen] = React.useState(false);
  const [loading, setLoading] = React.useState(true);
  // 搜索走 query + Enter 触发：appliedQuery 才是真正参与过滤的值，免得每敲一个字
  // 就重排整张表。
  const [query, setQuery] = React.useState("");
  const [appliedQuery, setAppliedQuery] = React.useState("");
  // 分类器维度 = 协议格式；筛选器另有状态一维。两者都作用于同一份列表。
  const [formatKind, setFormatKind] = React.useState("all");
  const [statusKind, setStatusKind] = React.useState("all");
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);

  // 抽屉的开关和内容分开存：关闭时 editing 保持不变，否则关闭动画期间标题会从
  // 「编辑 X」闪成「新建」。editing = null 表示新建。
  const [editOpen, setEditOpen] = React.useState(false);
  const [editing, setEditing] = React.useState<LLMProfile | null>(null);
  const openEditor = React.useCallback((p: LLMProfile | null) => {
    setEditing(p);
    setEditOpen(true);
  }, []);

  const loadPool = React.useCallback(async () => {
    try {
      setPool(await api.llmPool());
    } catch {
      /* 轮询状态拿不到不该挡住配置列表 */
    }
  }, []);

  const load = React.useCallback(async () => {
    try {
      setProfiles(await api.llmProfiles());
    } catch (e) {
      toast.error(`加载模型配置失败：${(e as Error).message}`);
    } finally {
      setLoading(false);
    }
    await loadPool();
  }, [loadPool]);

  React.useEffect(() => {
    void load();
  }, [load]);

  // 健康徽章按 profile id 取轮询状态。
  const health = React.useMemo(() => {
    const m = new Map<string, LLMPoolMember>();
    for (const c of pool?.chain ?? []) m.set(c.profile_id, c);
    return m;
  }, [pool]);

  // 分类器计数必须来自【未过滤的全量数据】：若跟着当前分类走，选中 anthropic 后
  // 其他格式的计数会全变 0，用户就再也没法从页签上看出还有多少条别的格式。
  const formatCounts = React.useMemo(() => {
    const c: Record<string, number> = { all: profiles.length };
    for (const f of LLM_FORMATS) c[f] = 0;
    for (const p of profiles) {
      const f = p.format ?? "";
      c[f] = (c[f] ?? 0) + 1;
    }
    return c;
  }, [profiles]);

  const filtered = React.useMemo(() => {
    const q = appliedQuery.trim().toLowerCase();
    return profiles.filter((p) => {
      if (formatKind !== "all" && (p.format ?? "") !== formatKind) return false;
      if (statusKind === "active" && !p.is_default) return false;
      if (statusKind === "tripped" && !isTripped(p, health.get(p.id))) return false;
      if (!q) return true;
      return (
        p.name.toLowerCase().includes(q) ||
        (p.model ?? "").toLowerCase().includes(q) ||
        (p.base_url ?? "").toLowerCase().includes(q)
      );
    });
  }, [profiles, appliedQuery, formatKind, statusKind, health]);

  const paged = React.useMemo(() => filtered.slice((page - 1) * pageSize, page * pageSize), [filtered, page, pageSize]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: 筛选条件一变就回到第 1 页。
  React.useEffect(() => {
    setPage(1);
  }, [appliedQuery, formatKind, statusKind]);

  const search = () => {
    setPage(1);
    setAppliedQuery(query);
  };

  const filtersActive = !!appliedQuery || formatKind !== "all" || statusKind !== "all";
  const clearFilters = () => {
    setQuery("");
    setAppliedQuery("");
    setFormatKind("all");
    setStatusKind("all");
  };

  async function activate(id: string, name: string) {
    try {
      await api.activateLLMProfile(id);
      toast.success(`已激活：${name}`);
      await load();
    } catch (e) {
      toast.error(`激活失败：${(e as Error).message}`);
    }
  }

  async function remove(p: LLMProfile) {
    if (p.is_default) {
      toast.error("无法删除当前激活的配置");
      return;
    }
    if (!window.confirm(`删除模型配置「${p.name}」？该操作不可恢复。`)) return;
    try {
      await api.deleteLLMProfile(p.id);
      toast.success(`已删除：${p.name}`);
      await load();
    } catch (e) {
      toast.error(`删除失败：${(e as Error).message}`);
    }
  }

  const poolOn = pool?.enabled ?? false;

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div>
          <h1 className="font-semibold text-xl">LLM</h1>
          <p className="text-muted-foreground text-sm">
            全 Agent 共享的格式 / 模型 / 限速配置。点名称进入详情页编辑；带星标的为当前激活配置。
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button size="sm" variant="outline" onClick={() => setPoolOpen(true)}>
            <ZapIcon className="size-4" /> 轮询配置
            {poolOn && (
              <Badge variant="outline" className="ml-1 border-emerald-500/50 text-emerald-600 dark:text-emerald-400">
                已开启
              </Badge>
            )}
          </Button>
          <Button size="sm" onClick={() => openEditor(null)}>
            <PlusIcon className="size-4" />
            新建配置
          </Button>
        </div>
      </div>

      <Tabs defaultValue="profiles">
        <TabsList>
          <TabsTrigger value="profiles">模型配置</TabsTrigger>
          <TabsTrigger value="retry">重试与退避</TabsTrigger>
        </TabsList>

        <TabsContent value="profiles" className="mt-4">
          <Card>
            <CardContent className="px-0 pt-4">
              {/* 第一行：搜索 + 总数 */}
              <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
                <div className="relative w-full sm:max-w-xs">
                  <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
                  <Input
                    value={query}
                    onChange={(e) => setQuery(e.target.value)}
                    onKeyDown={(e) => e.key === "Enter" && search()}
                    placeholder="搜索名称 / 模型 / 地址"
                    className="pl-8"
                  />
                </div>
                <Button variant="outline" size="sm" onClick={search}>
                  搜索
                </Button>
                <span className="text-muted-foreground ml-auto text-xs tabular-nums">
                  {appliedQuery ? `命中 ${filtered.length} / ` : ""}共 {profiles.length} 条
                </span>
              </div>

              {/* 第二行：分类器（可点页签，计数取自全量）+ 筛选器 */}
              <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
                <Tabs
                  value={formatKind}
                  onValueChange={(v) => {
                    setPage(1);
                    setFormatKind(v);
                  }}
                >
                  <TabsList>
                    <TabsTrigger value="all">全部 {formatCounts.all ?? 0}</TabsTrigger>
                    {LLM_FORMATS.map((f) => (
                      <TabsTrigger key={f} value={f} title={FORMAT_LABEL_LONG[f]}>
                        {FORMAT_LABEL_SHORT[f]} {formatCounts[f] ?? 0}
                      </TabsTrigger>
                    ))}
                  </TabsList>
                </Tabs>
                <div className="ml-auto flex flex-wrap items-center gap-2">
                  {/* 与上面的页签共用同一个 state（两个入口、同一份筛选），故永不失同步。 */}
                  <Select
                    value={formatKind}
                    onValueChange={(v) => {
                      setPage(1);
                      setFormatKind(v);
                    }}
                  >
                    <SelectTrigger className="w-36">
                      <SelectValue placeholder="协议格式" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectGroup>
                        <SelectItem value="all">全部格式</SelectItem>
                        {LLM_FORMATS.map((f) => (
                          <SelectItem key={f} value={f}>
                            {FORMAT_LABEL_SHORT[f]}
                          </SelectItem>
                        ))}
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                  <Select
                    value={statusKind}
                    onValueChange={(v) => {
                      setPage(1);
                      setStatusKind(v);
                    }}
                  >
                    <SelectTrigger className="w-32">
                      <SelectValue placeholder="状态" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectGroup>
                        {STATUS_OPTIONS.map((o) => (
                          <SelectItem key={o.value} value={o.value}>
                            {o.label}
                          </SelectItem>
                        ))}
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                </div>
              </div>

              {loading && profiles.length === 0 ? (
                <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
              ) : filtered.length === 0 ? (
                <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
                  <ZapIcon className="size-8 opacity-40" />
                  <span>{profiles.length === 0 ? "还没有模型配置，点右上角「新建配置」添加" : "没有匹配的配置"}</span>
                  {filtersActive && (
                    <Button variant="outline" size="sm" onClick={clearFilters}>
                      清除筛选
                    </Button>
                  )}
                </div>
              ) : (
                <>
                  {/* 列宽铁律：名称列不设宽度（table-fixed 下自动吃掉余量，设 w-full 会挤掉
                      后面的列），其余列按最小可读值给固定宽度。固定列之和 592px 明显小于
                      1280 视口下的 ~897px 容器，名称列仍有 ~300px，不会撑出横向滚动条。
                      状态列给 w-28 是因为「已熔断 · 5min」要完整显示——按 w-24 会被截成
                      「已熔断 · 5mi…」，恰恰把最关键的信息切掉了。 */}
                  <Table className="table-fixed">
                    <TableHeader>
                      <TableRow className="hover:bg-transparent">
                        <TableHead>名称</TableHead>
                        <TableHead className="w-24">协议格式</TableHead>
                        <TableHead className="w-40">模型</TableHead>
                        <TableHead className="w-20">上下文窗口</TableHead>
                        <TableHead className="w-28">状态</TableHead>
                        <TableHead className="w-16">优先级</TableHead>
                        <TableHead className="w-24">操作</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {paged.map((p) => {
                        const h = healthOf(p, health.get(p.id));
                        return (
                          <TableRow key={p.id} className={p.is_default ? "bg-amber-400/5" : undefined}>
                            {/* 名称即入口：点击进入详情页做完整编辑。 */}
                            <TableCell className="min-w-0">
                              <div className="flex min-w-0 items-center gap-1.5">
                                {p.is_default && (
                                  <StarIcon
                                    className="size-3.5 shrink-0 fill-amber-400 text-amber-400"
                                    aria-label="当前激活配置"
                                  />
                                )}
                                <Link
                                  href={`/system/llm/detail?id=${p.id}`}
                                  className="block w-full truncate text-left font-medium hover:underline"
                                  title={p.name}
                                >
                                  {p.name}
                                </Link>
                              </div>
                            </TableCell>
                            <TableCell>
                              <Badge
                                variant="outline"
                                className={cn("max-w-full", formatBadgeClass(p.format))}
                                title={FORMAT_LABEL_LONG[p.format] ?? p.format}
                              >
                                <span className="truncate">{FORMAT_LABEL_SHORT[p.format] ?? p.format}</span>
                              </Badge>
                            </TableCell>
                            <TableCell className="min-w-0">
                              <code className="text-muted-foreground block truncate font-mono text-xs" title={p.model}>
                                {p.model}
                              </code>
                            </TableCell>
                            <TableCell
                              className={cn(
                                "tabular-nums",
                                contextWindowIsDefault(p.context_window_k) && "text-muted-foreground",
                              )}
                              title={contextWindowIsDefault(p.context_window_k) ? "未设置，按默认 200K" : undefined}
                            >
                              {contextWindowText(p.context_window_k)}
                            </TableCell>
                            <TableCell>
                              <div className="flex flex-col items-start gap-1">
                                <Badge variant="outline" className={cn("max-w-full", h.cls)} title={h.hint}>
                                  <span className="truncate">{h.label}</span>
                                </Badge>
                                {p.is_default ? (
                                  <span className="font-medium text-[10px] text-amber-600 dark:text-amber-400">
                                    激活配置
                                  </span>
                                ) : (
                                  <Button
                                    variant="ghost"
                                    size="xs"
                                    className="h-5 px-1 text-[10px]"
                                    title="设为激活：激活后全 Agent 立即改用这一条"
                                    onClick={() => void activate(p.id, p.name)}
                                  >
                                    设为激活
                                  </Button>
                                )}
                              </div>
                            </TableCell>
                            <TableCell className="text-muted-foreground tabular-nums">
                              {p.pool_exclude ? (
                                <span title="不参与轮询：不会被当作故障转移目标，仍可被 Agent / 任务显式指定">
                                  不参与
                                </span>
                              ) : (
                                (p.priority ?? 0)
                              )}
                            </TableCell>
                            <TableCell>
                              <div className="flex items-center gap-0.5">
                                <Button
                                  variant="ghost"
                                  size="icon-xs"
                                  onClick={() => openEditor(p)}
                                  title="快速编辑（抽屉）"
                                  aria-label="快速编辑"
                                >
                                  <PencilIcon className="size-3.5" />
                                </Button>
                                <Button
                                  variant="ghost"
                                  size="icon-xs"
                                  onClick={() => void remove(p)}
                                  title="删除该配置"
                                  aria-label="删除"
                                >
                                  <Trash2Icon className="text-muted-foreground size-3.5" />
                                </Button>
                                <Button variant="ghost" size="icon-xs" asChild title="进入详情页">
                                  <Link href={`/system/llm/detail?id=${p.id}`} aria-label="进入详情页">
                                    <ArrowRightIcon className="size-3.5" />
                                  </Link>
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
                    total={filtered.length}
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

        {/* 重试与退避：五层重试的全局默认值。每个配置的三层覆盖在详情页里调。 */}
        <TabsContent value="retry" className="mt-4">
          <RetryPolicyPanel />
        </TabsContent>
      </Tabs>

      <ProfileSheet profile={editing} open={editOpen} onOpenChange={setEditOpen} onSaved={() => void load()} />
      <PoolSheet open={poolOpen} onOpenChange={setPoolOpen} pool={pool} onReload={loadPool} />
    </div>
  );
}
