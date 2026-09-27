"use client";

// CVE 情报简报(契约 POC_INTEL_CONTRACTS.md 修订 §10,Agent 2 所有):
//  - TaskVulnBriefingCard 任务详情总览的整宽卡(与 TaskMemoryBriefingCard 同构):
//    统计行(KEV/高危/其他/共 N 条)+ KEV 前三条预览 + 「查看完整简报」入口;
//    拉取失败静默 null,不崩任务详情页。联动关闭渲染一行关闭说明。
//  - VulnBriefingDialog 双页签对话框:「匹配漏洞」(分类页签 全部/KEV/高危/其他 +
//    搜索 400ms 防抖 + 严重度/排序 Select + TablePagination,行点击打开
//    VulnDetailDialog)与「指纹」(term+hits 徽标,hits=0 灰显,点击指纹即按该词搜索)。
// 数据与注入 agent 的简报同源同缓存(GET /api/tasks/{id}/vuln-brief),保证
// 「界面看到的 = AI 拿到的」;分页列表走 GET /api/tasks/{id}/vuln-brief/entries。

import * as React from "react";

import { FileTextIcon, Loader2Icon, SearchIcon, ShieldAlertIcon } from "lucide-react";

import { TablePagination } from "@/components/table-pagination";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { VulnDetailDialog, severityBadgeClass } from "@/components/vulndb-dialogs";
import { api } from "@/lib/api";
import type { VulnBriefDTO, VulnBriefEntriesDTO, VulnEntry, VulnFingerprintHit } from "@/lib/types";

// 后端 wire = {"brief": VulnBriefDTO}(契约 §4.3);api.ts 冻结签名把整个响应体
// 标成了 VulnBriefDTO——这里做一次兼容解包,带 brief 键与平铺两种形态都吃。
function unwrapBrief(resp: VulnBriefDTO | null): VulnBriefDTO | null {
  if (!resp) return null;
  const inner = (resp as unknown as { brief?: VulnBriefDTO }).brief;
  if (inner && typeof inner === "object" && "entries" in inner) return inner;
  return resp;
}

// 分类计数:新 wire 带 counts;旧 wire(集成期瞬态)按 KEV/高危(critical|high)/其他
// 从兼容保留的全量 entries 现算,语义与服务端分段一致。
function resolveCounts(brief: VulnBriefDTO): { kev: number; high: number; rest: number; total: number } {
  if (brief.counts) return brief.counts;
  const g = { kev: 0, high: 0, rest: 0, total: 0 };
  for (const e of brief.entries ?? []) {
    g.total++;
    if (e.kev) g.kev++;
    else if (e.severity === "critical" || e.severity === "high") g.high++;
    else g.rest++;
  }
  return g;
}

// 指纹词兼容:新 wire = {term,hits}[];旧 wire 是纯 string[](无命中数,不渲染计数)。
function fingerprintHits(fps: VulnBriefDTO["fingerprints"]): VulnFingerprintHit[] {
  return (fps ?? []).map((f) => (typeof f === "string" ? { term: f, hits: -1 } : f));
}

// 标题以「cve_id + 空格」开头时剥掉前缀,避免与前面的编号徽标重复显示。
function stripCvePrefix(entry: VulnEntry): string {
  const cve = entry.cve_id?.trim();
  if (cve && entry.title.startsWith(`${cve} `)) return entry.title.slice(cve.length + 1).trim();
  return entry.title;
}

// 页签胶囊按钮(可点切换,非只读徽标):选中实底、未选描边,带可选计数,aria-pressed 标识。
function PillTab({
  active,
  onClick,
  count,
  children,
}: {
  active: boolean;
  onClick: () => void;
  count?: number;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={`inline-flex items-center gap-1.5 rounded-full border px-3 py-1 text-xs transition-colors ${
        active ? "border-primary bg-primary text-primary-foreground" : "border-input text-muted-foreground hover:bg-muted"
      }`}
    >
      {children}
      {count !== undefined && <span className="tabular-nums opacity-80">{count}</span>}
    </button>
  );
}

// 单条命中:严重度徽标 + KEV 徽标 + 编号/标题 + 厂商产品与 PoC 数。
// onClick 存在时整行可点(cursor-pointer + hover),点击打开 VulnDetailDialog。
function BriefingEntryRow({ entry, onClick }: { entry: VulnEntry; onClick?: () => void }) {
  const vp = [entry.vendor, entry.product].filter(Boolean).join(" ");
  return (
    <div
      onClick={onClick}
      className={`flex items-start gap-2 rounded-md px-1.5 py-1.5 text-xs ${
        onClick ? "hover:bg-muted/60 cursor-pointer" : ""
      }`}
    >
      <Badge variant="outline" className={`shrink-0 ${severityBadgeClass(entry.severity)}`}>
        {entry.severity || "-"}
      </Badge>
      {entry.kev ? (
        <Badge variant="outline" className="shrink-0 border-red-500/30 bg-red-500/15 text-red-600">
          KEV
        </Badge>
      ) : null}
      <div className="min-w-0 flex-1">
        <div className="break-words font-medium">
          {entry.cve_id ? <span className="mr-1.5 font-mono">{entry.cve_id}</span> : null}
          {stripCvePrefix(entry)}
        </div>
        {(vp || entry.poc_count > 0) && (
          <div className="text-muted-foreground">
            {vp}
            {vp && entry.poc_count > 0 ? " · " : ""}
            {entry.poc_count > 0 ? `PoC x${entry.poc_count}` : ""}
          </div>
        )}
      </div>
    </div>
  );
}

// ---- 对话框页签:匹配漏洞 / 指纹 ----
type BriefTab = "entries" | "fingerprints";
type BriefScope = "all" | "kev" | "high" | "rest";

// CVE 情报简报对话框(受控 open,宿主 TaskVulnBriefingCard 提供「查看完整简报」入口):
// 首次打开拉简报元数据(counts/指纹),条目列表走分页端点按需拉取。
export function VulnBriefingDialog({
  taskId,
  open,
  onOpenChange,
}: {
  taskId: string;
  open: boolean;
  onOpenChange: (v: boolean) => void;
}) {
  const [brief, setBrief] = React.useState<VulnBriefDTO | null>(null);
  const [failed, setFailed] = React.useState(false);
  const [tab, setTab] = React.useState<BriefTab>("entries");
  const [scope, setScope] = React.useState<BriefScope>("all");
  const [severity, setSeverity] = React.useState("all"); // Radix 空串保留值,用 "all" 哨兵
  const [sort, setSort] = React.useState("severity");
  const [searchInput, setSearchInput] = React.useState("");
  const [q, setQ] = React.useState("");
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(20);
  const [list, setList] = React.useState<VulnBriefEntriesDTO | null>(null);
  const [listLoading, setListLoading] = React.useState(false);
  const [listFailed, setListFailed] = React.useState(false);
  const [reloadTick, setReloadTick] = React.useState(0);
  const [detailId, setDetailId] = React.useState<number | null>(null);

  // 简报元数据:首次打开时拉取,之后缓存复用;失败给出非阻断文案。
  React.useEffect(() => {
    if (!open || brief || failed) return;
    let cancelled = false;
    api
      .taskVulnBrief(taskId)
      .then((resp) => {
        if (!cancelled) setBrief(unwrapBrief(resp));
      })
      .catch(() => {
        // 静默降级:情报卡不可用不能影响任务详情页其余部分。
        if (!cancelled) setFailed(true);
      });
    return () => {
      cancelled = true;
    };
  }, [open, taskId, brief, failed]);

  // 搜索框 400ms 防抖 → 生效 q;任何筛选变化都回到第 1 页。
  React.useEffect(() => {
    if (!open) return;
    const t = setTimeout(() => {
      setQ(searchInput.trim());
      setPage(1);
    }, 400);
    return () => clearTimeout(t);
  }, [searchInput, open]);

  const enabled = brief?.enabled ?? false;

  // 分页条目列表:切页签/翻页/改筛选/详情对话框 onChanged 都会重新拉当前页。
  React.useEffect(() => {
    if (!open || !enabled) return;
    let cancelled = false;
    setListLoading(true);
    setListFailed(false);
    api
      .taskVulnBriefEntries(taskId, {
        scope,
        severity: severity === "all" ? "" : severity,
        q,
        sort,
        desc: true,
        limit: pageSize,
        offset: (page - 1) * pageSize,
      })
      .then((r) => {
        if (!cancelled) setList(r);
      })
      .catch(() => {
        if (!cancelled) setListFailed(true);
      })
      .finally(() => {
        if (!cancelled) setListLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [open, enabled, taskId, scope, severity, q, sort, page, pageSize, reloadTick]);

  // 点指纹徽标:切回「匹配漏洞」页签,q=该词(跳过防抖直填),offset 归零。
  const searchByFingerprint = (term: string) => {
    setSearchInput(term);
    setQ(term);
    setPage(1);
    setTab("entries");
  };

  const counts = brief ? resolveCounts(brief) : { kev: 0, high: 0, rest: 0, total: 0 };
  const fps = brief ? fingerprintHits(brief.fingerprints) : [];
  const entries = list?.entries ?? [];
  const filtered = scope !== "all" || severity !== "all" || q !== "";

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>CVE 情报简报 · 任务 #{taskId}</DialogTitle>
          <DialogDescription>
            按本任务资产指纹(技术栈/服务/页面标题)匹配的库内漏洞;任务启动时已注入 AI。
            {brief?.generated_at ? ` 生成于 ${new Date(brief.generated_at).toLocaleString()}` : ""}
          </DialogDescription>
        </DialogHeader>
        {failed ? (
          <p className="text-muted-foreground text-sm">简报加载失败,请稍后重试。</p>
        ) : !brief ? (
          <p className="text-muted-foreground flex items-center gap-2 text-sm">
            <Loader2Icon className="size-4 animate-spin" /> 加载中…
          </p>
        ) : !brief.enabled ? (
          <p className="text-muted-foreground text-sm">
            本任务未启用 CVE 情报联动(任务级开关或全局开关关闭)。可在创建任务的高级设置或漏洞库配置中开启。
          </p>
        ) : (
          <div className="flex flex-col gap-3">
            {/* 页签:可点切换,带计数,aria-pressed */}
            <div className="flex items-center gap-2">
              <PillTab active={tab === "entries"} onClick={() => setTab("entries")} count={counts.total}>
                匹配漏洞
              </PillTab>
              <PillTab active={tab === "fingerprints"} onClick={() => setTab("fingerprints")} count={fps.length}>
                指纹
              </PillTab>
            </div>

            {tab === "entries" ? (
              <div className="flex flex-col gap-2">
                {/* 分类页签:计数来自简报 counts(语义同服务端 scope 分段) */}
                <div className="flex flex-wrap items-center gap-1.5">
                  <PillTab
                    active={scope === "all"}
                    onClick={() => {
                      setScope("all");
                      setPage(1);
                    }}
                    count={counts.total}
                  >
                    全部
                  </PillTab>
                  <PillTab
                    active={scope === "kev"}
                    onClick={() => {
                      setScope("kev");
                      setPage(1);
                    }}
                    count={counts.kev}
                  >
                    KEV
                  </PillTab>
                  <PillTab
                    active={scope === "high"}
                    onClick={() => {
                      setScope("high");
                      setPage(1);
                    }}
                    count={counts.high}
                  >
                    高危
                  </PillTab>
                  <PillTab
                    active={scope === "rest"}
                    onClick={() => {
                      setScope("rest");
                      setPage(1);
                    }}
                    count={counts.rest}
                  >
                    其他
                  </PillTab>
                </div>
                {/* 筛选工具行:搜索(防抖)+ 严重度 + 排序 */}
                <div className="flex flex-wrap items-center gap-2">
                  <div className="relative min-w-48 flex-1">
                    <SearchIcon className="text-muted-foreground absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
                    <Input
                      value={searchInput}
                      onChange={(e) => setSearchInput(e.target.value)}
                      placeholder="搜索 CVE 编号 / 标题 / 厂商 / 产品"
                      className="pl-8"
                    />
                  </div>
                  <Select
                    value={severity}
                    onValueChange={(v) => {
                      setSeverity(v);
                      setPage(1);
                    }}
                  >
                    <SelectTrigger className="w-28">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="all">全部严重度</SelectItem>
                      <SelectItem value="critical">严重</SelectItem>
                      <SelectItem value="high">高</SelectItem>
                      <SelectItem value="medium">中</SelectItem>
                      <SelectItem value="low">低</SelectItem>
                    </SelectContent>
                  </Select>
                  <Select value={sort} onValueChange={setSort}>
                    <SelectTrigger className="w-32">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="severity">按严重度</SelectItem>
                      <SelectItem value="poc">按 PoC 数</SelectItem>
                      <SelectItem value="updated">按更新时间</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                {/* 条目列表 + 分页器 */}
                <div className="rounded-md border">
                  <div className={`flex flex-col p-1 ${listLoading && list ? "pointer-events-none opacity-60" : ""}`}>
                    {listFailed ? (
                      <div className="flex items-center gap-2 p-2 text-sm text-muted-foreground">
                        列表加载失败,请稍后重试。
                        <Button variant="outline" size="sm" onClick={() => setReloadTick((t) => t + 1)}>
                          重试
                        </Button>
                      </div>
                    ) : listLoading && !list ? (
                      <p className="text-muted-foreground flex items-center gap-2 p-2 text-sm">
                        <Loader2Icon className="size-4 animate-spin" /> 加载中…
                      </p>
                    ) : entries.length === 0 ? (
                      <p className="text-muted-foreground p-2 text-sm">
                        {filtered
                          ? "当前筛选条件下没有匹配条目;试试清除搜索词或切换分类页签。"
                          : "库内暂无命中本任务指纹的带 PoC 条目;随着采集持续更新,命中会自动增加。"}
                      </p>
                    ) : (
                      entries.map((entry) => (
                        <BriefingEntryRow key={entry.id} entry={entry} onClick={() => setDetailId(entry.id)} />
                      ))
                    )}
                  </div>
                  <TablePagination
                    page={page}
                    pageSize={pageSize}
                    total={list?.total ?? 0}
                    onPageChange={setPage}
                    onPageSizeChange={(s) => {
                      setPageSize(s);
                      setPage(1);
                    }}
                  />
                </div>
              </div>
            ) : (
              <div className="flex flex-col gap-2">
                {fps.length === 0 ? (
                  <p className="text-muted-foreground text-sm">任务资产暂无可匹配的指纹词。</p>
                ) : (
                  <div className="flex flex-wrap gap-1.5">
                    {fps.map((f) => (
                      <button
                        key={f.term}
                        type="button"
                        onClick={() => searchByFingerprint(f.term)}
                        title={f.hits >= 0 ? `库内命中 ${f.hits} 条,点击按该词搜索` : "点击按该词搜索"}
                        className={`inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 font-mono text-xs transition-colors hover:bg-muted ${
                          f.hits === 0 ? "text-muted-foreground opacity-50" : ""
                        }`}
                      >
                        {f.term}
                        {f.hits >= 0 && (
                          <span className="bg-primary/10 rounded-full px-1.5 text-[10px] leading-4 tabular-nums">
                            {f.hits}
                          </span>
                        )}
                      </button>
                    ))}
                  </div>
                )}
                <p className="text-muted-foreground text-xs">
                  点击指纹词可跳到「匹配漏洞」按该词搜索;hits 为该词在库内带 PoC 条目的命中数,0 表示暂无命中。
                </p>
              </div>
            )}

            <p className="text-muted-foreground text-xs">
              PoC 仅作验证参考,使用前严格遵守任务约束与授权范围;点击条目可查看漏洞详情与 PoC。
            </p>
          </div>
        )}
        {/* 行点击打开的漏洞详情:编辑入口无(简报只读),onChanged 刷新当前页 */}
        <VulnDetailDialog
          entryID={detailId}
          onOpenChange={(v) => {
            if (!v) setDetailId(null);
          }}
          onChanged={() => setReloadTick((t) => t + 1)}
          onEdit={() => {}}
        />
      </DialogContent>
    </Dialog>
  );
}

// 任务详情总览页签的「CVE 情报简报」卡:与 TaskMemoryBriefingCard 同构整宽 Card。
// 联动关闭 → 一行关闭说明;命中为空 → 引导文案;否则统计行 + KEV 前三条预览;
// 接口失败 → 整卡不渲染(静默,不崩任务详情页)。
export function TaskVulnBriefingCard({ taskId }: { taskId: string }) {
  const [brief, setBrief] = React.useState<VulnBriefDTO | null>(null);
  const [failed, setFailed] = React.useState(false);
  const [open, setOpen] = React.useState(false);

  React.useEffect(() => {
    let cancelled = false;
    api
      .taskVulnBrief(taskId)
      .then((resp) => {
        if (!cancelled) setBrief(unwrapBrief(resp));
      })
      .catch(() => {
        // 静默降级:情报卡不可用不能影响任务详情页其余卡片。
        if (!cancelled) setFailed(true);
      });
    return () => {
      cancelled = true;
    };
  }, [taskId]);

  if (failed || !brief) return null;

  const counts = resolveCounts(brief);
  const fps = fingerprintHits(brief.fingerprints);
  // KEV 前三条预览:兼容保留的全量 entries(上限 30)里取 KEV 子集前三条。
  const kevPreview = (brief.entries ?? []).filter((e) => e.kev).slice(0, 3);

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between gap-2 space-y-0">
        <CardTitle className="flex items-center gap-2 text-base">
          <ShieldAlertIcon className="text-primary size-4" /> CVE 情报简报
          <span className="text-muted-foreground text-xs font-normal">(按本任务资产指纹匹配，任务启动时已注入 AI)</span>
        </CardTitle>
        {brief.enabled ? (
          <Button variant="outline" size="sm" onClick={() => setOpen(true)}>
            <FileTextIcon className="size-3.5" />
            查看完整简报
          </Button>
        ) : null}
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        {!brief.enabled ? (
          <p className="text-muted-foreground text-sm">
            本任务未启用 CVE 情报联动(任务级开关或全局开关关闭)。可在创建任务的高级设置或漏洞库配置中开启。
          </p>
        ) : counts.total === 0 ? (
          <p className="text-muted-foreground text-sm">
            库内暂无命中本任务指纹的带 PoC 条目({fps.length} 个指纹词);随着采集持续更新,命中会自动增加。
          </p>
        ) : (
          <>
            <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
              <span className={counts.kev > 0 ? "font-medium text-red-600" : "text-muted-foreground"}>
                KEV {counts.kev}
              </span>
              <span className="text-muted-foreground">·</span>
              <span className={counts.high > 0 ? "font-medium text-amber-600" : "text-muted-foreground"}>
                高危 {counts.high}
              </span>
              <span className="text-muted-foreground">·</span>
              <span className="text-muted-foreground">其他 {counts.rest}</span>
              <span className="text-muted-foreground">·</span>
              <span className="text-muted-foreground">
                共 {counts.total} 条（{fps.length} 个指纹）
              </span>
            </div>
            {kevPreview.length > 0 && (
              <div className="flex flex-col">
                {kevPreview.map((e) => (
                  <div key={e.id} className="flex items-center gap-2 border-b py-1 text-xs last:border-b-0">
                    <Badge variant="outline" className="shrink-0 border-red-500/30 bg-red-500/15 text-red-600">
                      KEV
                    </Badge>
                    <span className="min-w-0 flex-1 truncate">
                      {e.cve_id ? <span className="mr-1.5 font-mono">{e.cve_id}</span> : null}
                      {stripCvePrefix(e)}
                    </span>
                  </div>
                ))}
              </div>
            )}
          </>
        )}
      </CardContent>
      <VulnBriefingDialog taskId={taskId} open={open} onOpenChange={setOpen} />
    </Card>
  );
}
