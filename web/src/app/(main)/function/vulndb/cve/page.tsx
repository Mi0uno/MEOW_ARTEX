"use client";

// CVE&CNVD 情报:有编号的公开漏洞(CVE)与国家库收录的国内漏洞(CNVD)。
// 数据由后台定时采集器从 NVD / GitHub Advisory / Exploit-DB / CISA KEV /
// PocOrExp / nuclei 模板 / 安全社区文章等公开源持续拉取并自动发现 PoC;
// CNVD 编号经文章源提取入库(vulndb_articles.go),结构化数据可在系统设置配
// vulndb_cnvd_url 数据端点接入(官方会员 API/自建镜像,见 vulndb_cnvd.go)。
//
// 页面形态:所有操作按钮收进顶部工具栏,内容分「情报源」「CVE」「CNVD」三个
// 页签,除情报源外各自配齐 搜索/分类页签/筛选器/分页器 四件套:
//   - 情报源:各源实时状态 + 采集记录(源分类页签 + 状态筛选 + 搜索 + 分页)
//   - CVE / CNVD:严重度分类页签(带计数) + 搜索 + PoC/KEV 筛选 + 分页表格
// 页签状态存 URL(?tab=sources|cve|cnvd),刷新/后退一致。

import * as React from "react";

import {
  ChevronDownIcon,
  ChevronUpIcon,
  ExternalLinkIcon,
  PencilIcon,
  PlusIcon,
  RefreshCwIcon,
  SearchIcon,
  Trash2Icon,
} from "lucide-react";
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
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { TablePagination } from "@/components/table-pagination";
import { api } from "@/lib/api";
import type { VulnEntry, VulnSyncRun } from "@/lib/types";
import { VulnDetailDialog, VulnEntryDialog, severityBadgeClass } from "@/components/vulndb-dialogs";
import { VulndbBackfillCard } from "@/components/vulndb-backfill-card";
import { PocReviewPanel } from "@/components/vulndb-poc-review";

const PAGE_SIZE = 50;
const RUNS_PAGE_SIZE = 20;

// 各采集源的中文展示名(与后端 vulndb_sync_runs.source 取值对应,含 v2 新增源)。
const SOURCE_LABELS: Record<string, string> = {
  nvd: "NVD",
  github_advisory: "GitHub Advisory",
  exploitdb: "Exploit-DB",
  github_poc: "GitHub PoC",
  kev: "CISA KEV 在野利用",
  pocorexp_today: "PocOrExp",
  nuclei: "nuclei 模板",
  articles: "安全社区文章",
  nvd_backfill: "NVD 历史回填",
  cnvd_feed: "CNVD 数据端点",
  trickest: "trickest 全网聚合",
};
// 各源实时状态表的固定展示顺序;后端新增的未知源追加在尾部,不漏显。
const SOURCE_ORDER = [
  "nvd",
  "github_advisory",
  "exploitdb",
  "github_poc",
  "kev",
  "pocorexp_today",
  "nuclei",
  "articles",
  "nvd_backfill",
  "cnvd_feed",
  "trickest",
];

// 采集记录的源分类(情报源页签的分类页签):每类是一组 source 键,后端
// source 参数支持逗号分隔多值。"全部"组不传 source。
const SOURCE_GROUPS: { key: string; label: string; sources: string[] }[] = [
  { key: "all", label: "全部", sources: [] },
  { key: "intel", label: "漏洞情报", sources: ["nvd", "github_advisory", "kev", "nvd_backfill", "cnvd_feed"] },
  { key: "poc", label: "PoC 源", sources: ["exploitdb", "github_poc", "pocorexp_today", "nuclei", "trickest"] },
  { key: "community", label: "社区文章", sources: ["articles"] },
];

// 严重度分类页签(带实时计数,数据来自 /vulndb/entries 的 sev_counts)。
const SEVERITY_TABS: { key: string; label: string }[] = [
  { key: "all", label: "全部" },
  { key: "critical", label: "严重" },
  { key: "high", label: "高" },
  { key: "medium", label: "中" },
  { key: "low", label: "低" },
];

function fmtTime(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

// 列表/面板列用的紧凑时间(MM-DD HH:mm),完整时间放单元格 title,省列宽。
function fmtTimeShort(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

// 搜索词 → 采集源 key:允许直接输 source 键(如 "nvd"),也允许输中文名的
// 一部分(如 "模板" → nuclei、"文章" → articles);匹配不上返回 null 走通用 q 检索。
function sourceKeyFromQuery(q: string): string | null {
  const s = q.trim().toLowerCase();
  if (!s) return null;
  if (SOURCE_ORDER.includes(s)) return s;
  for (const key of SOURCE_ORDER) {
    if ((SOURCE_LABELS[key] ?? "").toLowerCase().includes(s)) return key;
  }
  return null;
}

// 状态徽标文案:done/running/failed → 中文。
function runStatusText(status: string): React.ReactNode {
  if (status === "failed") return <span className="text-destructive font-medium">失败</span>;
  if (status === "running") return <span>进行中</span>;
  return <span className="text-muted-foreground">完成</span>;
}

export default function CVEPage() {
  // 页签:URL ?tab= 初始化(SPA 静态导出下不依赖 useSearchParams/Suspense)。
  // cve/cnvd 两个数据页签共用同一套列表状态,切换时只换 category 重新加载。
  const [tab, setTab] = React.useState<"sources" | "cve" | "cnvd">(() => {
    if (typeof window !== "undefined") {
      const t = new URLSearchParams(window.location.search).get("tab");
      if (t === "sources" || t === "cve" || t === "cnvd") return t;
    }
    return "cve";
  });
  const switchTab = (t: "sources" | "cve" | "cnvd") => {
    setTab(t);
    try {
      const u = new URL(window.location.href);
      u.searchParams.set("tab", t);
      window.history.replaceState(null, "", u.toString());
    } catch {
      /* 隐私模式等 replaceState 失败不影响页签功能 */
    }
  };
  // 数据页签当前加载的分类:CVE 页签=cve,CNVD 页签=cnvd(情报源页签不加载列表)。
  const listCategory = tab === "cnvd" ? "cnvd" : "cve";

  // ---- CVE 列表状态 ----
  const [entries, setEntries] = React.useState<VulnEntry[]>([]);
  const [total, setTotal] = React.useState(0);
  const [sevCounts, setSevCounts] = React.useState<Record<string, number>>({});
  const [query, setQuery] = React.useState("");
  const [severity, setSeverity] = React.useState("all");
  const [hasPoc, setHasPoc] = React.useState("all");
  // 在野利用筛选:""=全部(接口层用 "all" 表示),1=仅在野(KEV),0=仅非在野。
  const [kev, setKev] = React.useState("all");
  // 默认按编号新到旧(年份+序列号自然排序);表头点击可正序/反序。
  const [sort, setSort] = React.useState("number");
  const [sortDesc, setSortDesc] = React.useState(true);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);
  const [loading, setLoading] = React.useState(true);
  const [editing, setEditing] = React.useState<VulnEntry | null>(null);
  const [creating, setCreating] = React.useState(false);
  const [detailID, setDetailID] = React.useState<number | null>(null);

  // 行多选(选中集跨筛选/翻页保留):全选只作用于当前页;批量执行时已不存在的
  // id 自然失败并计入失败数,无需专门收敛。
  const [checkedIds, setCheckedIds] = React.useState<Set<number>>(new Set());
  const [deleting, setDeleting] = React.useState(false);
  const [confirmOpen, setConfirmOpen] = React.useState(false);

  // ---- 采集同步状态(情报源页签) ----
  const [syncing, setSyncing] = React.useState(false);
  const [syncMeta, setSyncMeta] = React.useState<{
    enabled: boolean;
    interval_hours: number;
    counts: Record<string, number>;
    latest: VulnSyncRun[];
    cnvd_url: string;
  } | null>(null);
  // CNVD 数据端点输入框状态(与已保存值分离,点保存才提交)。
  const [cnvdUrlInput, setCnvdUrlInput] = React.useState("");
  const [cnvdUrlSaving, setCnvdUrlSaving] = React.useState(false);
  const [runs, setRuns] = React.useState<VulnSyncRun[]>([]);
  const [runsTotal, setRunsTotal] = React.useState(0);
  const [runStatusCounts, setRunStatusCounts] = React.useState<Record<string, number>>({});
  const [runsLoading, setRunsLoading] = React.useState(false);
  const [runGroup, setRunGroup] = React.useState("all");
  const [runStatus, setRunStatus] = React.useState("all");
  const [runQuery, setRunQuery] = React.useState("");
  const [runQueryApplied, setRunQueryApplied] = React.useState("");
  const [runPage, setRunPage] = React.useState(1);
  const [runPageSize, setRunPageSize] = React.useState(RUNS_PAGE_SIZE);

  // NVD 历史回填状态(断点续跑;running 时每 5s 轮询,结束自动停止)。
  const [backfill, setBackfill] = React.useState<Awaited<ReturnType<typeof api.vulnBackfillStatus>> | null>(null);
  const [backfillYear, setBackfillYear] = React.useState("2016");
  const [backfillOpen, setBackfillOpen] = React.useState(false);
  const [backfillBusy, setBackfillBusy] = React.useState<"start" | "cancel" | null>(null);

  const toggleChecked = (id: number, checked: boolean) => {
    setCheckedIds((prev) => {
      const next = new Set(prev);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  };

  const allSelected = entries.length > 0 && entries.every((e) => checkedIds.has(e.id));
  const someSelected = !allSelected && entries.some((e) => checkedIds.has(e.id));

  const load = React.useCallback(
    (cat: string, q: string, sev: string, poc: string, kevf: string, srt: string, desc: boolean, p: number, size: number) => {
      setLoading(true);
      api
        .vulnEntries({
          category: cat, // cve / cnvd:由当前数据页签决定
          q,
          severity: sev === "all" ? "" : sev,
          hasPoc: poc === "all" ? "" : poc,
          kev: kevf, // "all" 由 api 层忽略,不传参
          sort: srt,
          limit: size,
          offset: (p - 1) * size,
        })
        .then((r) => {
          setEntries(r.entries);
          setTotal(r.total);
          setSevCounts(r.sevCounts);
        })
        .catch((e) => toast.error(`加载列表失败:${(e as Error).message}`))
        .finally(() => setLoading(false));
    },
    [],
  );

  const loadMeta = React.useCallback(() => {
    api
      .vulnSyncStatus()
      .then((r) => {
        setSyncMeta({
          enabled: r.enabled,
          interval_hours: r.interval_hours,
          counts: r.counts,
          latest: r.latest,
          cnvd_url: r.cnvd_url,
        });
        setCnvdUrlInput((prev) => (prev === "" ? r.cnvd_url : prev));
      })
      .catch(() => undefined);
  }, []);

  const saveCnvdUrl = () => {
    setCnvdUrlSaving(true);
    api
      .setVulnSyncConfig({ cnvd_url: cnvdUrlInput.trim() })
      .then(() => {
        toast.success(cnvdUrlInput.trim() ? "已保存 CNVD 数据端点,下轮采集生效" : "已停用 CNVD 数据端点");
        loadMeta();
      })
      .catch((e) => toast.error(`保存失败:${(e as Error).message}`))
      .finally(() => setCnvdUrlSaving(false));
  };

  const loadRuns = React.useCallback(
    (group: string, status: string, qApplied: string, p: number, size: number) => {
      setRunsLoading(true);
      // 搜索词优先翻译成 source 键(支持中文名片段);翻不出键时走通用 q(匹配 source/错误信息)。
      const key = sourceKeyFromQuery(qApplied);
      api
        .vulnSyncStatus({
          source: key ?? SOURCE_GROUPS.find((g) => g.key === group)?.sources.join(",") ?? "",
          status,
          q: key ? "" : qApplied,
          limit: size,
          offset: (p - 1) * size,
        })
        .then((r) => {
          setRuns(r.runs);
          setRunsTotal(r.total);
          setRunStatusCounts(r.status_counts);
        })
        .catch(() => undefined)
        .finally(() => setRunsLoading(false));
    },
    [],
  );

  const loadBackfill = React.useCallback(() => {
    api
      .vulnBackfillStatus()
      .then(setBackfill)
      .catch(() => undefined);
  }, []);

  React.useEffect(() => {
    load(listCategory, query, severity, hasPoc, kev, sort, sortDesc, page, pageSize);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [listCategory, severity, hasPoc, kev, sort, sortDesc, page, pageSize]);

  React.useEffect(() => {
    loadMeta();
    loadBackfill();
  }, [loadMeta, loadBackfill]);

  // 采集记录:切到情报源页签或筛选变化时加载;页签激活期间每 20 秒静默刷新,
  // 让「进行中」状态和最新记录自动浮现。
  React.useEffect(() => {
    if (tab !== "sources") return;
    loadRuns(runGroup, runStatus, runQueryApplied, runPage, runPageSize);
    const t = setInterval(() => {
      loadRuns(runGroup, runStatus, runQueryApplied, runPage, runPageSize);
      loadMeta();
    }, 20000);
    return () => clearInterval(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tab, runGroup, runStatus, runQueryApplied, runPage, runPageSize, loadRuns, loadMeta]);

  // 回填进行中每 5 秒轮询进度,结束后自动停止。
  React.useEffect(() => {
    if (!backfill?.running) return;
    const t = setInterval(loadBackfill, 5000);
    return () => clearInterval(t);
  }, [backfill?.running, loadBackfill]);

  const search = () => {
    setPage(1);
    load(listCategory, query, severity, hasPoc, kev, sort, sortDesc, 1, pageSize);
  };
  const reload = () => load(listCategory, query, severity, hasPoc, kev, sort, sortDesc, page, pageSize);
  const searchRuns = () => {
    setRunPage(1);
    setRunQueryApplied(runQuery);
  };

  const runSync = async () => {
    setSyncing(true);
    try {
      await api.triggerVulnSync();
      toast.success("已触发采集,后台从公开漏洞源拉取中");
      setTimeout(() => {
        loadMeta();
        loadRuns(runGroup, runStatus, runQueryApplied, runPage, runPageSize);
        reload();
      }, 8000);
    } catch (e) {
      toast.error(`触发失败:${(e as Error).message}`);
    } finally {
      setSyncing(false);
    }
  };

  const remove = async (e: VulnEntry) => {
    if (!window.confirm(`删除 ${e.cve_id || e.title}?`)) return;
    try {
      await api.deleteVulnEntry(e.id);
      toast.success("已删除");
      reload();
    } catch (err) {
      toast.error(`删除失败:${(err as Error).message}`);
    }
  };

  // 批量删除已选 CVE:并行逐个调删除接口(连带删各自 PoC),统计成功/失败后
  // 清空选择并用页面既有的 reload() 刷新。数千条情报的清库靠检索 + 分页批量删。
  const batchDelete = async () => {
    const ids = [...checkedIds];
    setDeleting(true);
    try {
      const rs = await Promise.allSettled(ids.map((id) => api.deleteVulnEntry(id)));
      const ok = rs.filter((r) => r.status === "fulfilled").length;
      const fail = rs.length - ok;
      if (fail === 0) toast.success(`已删除 ${ok} 项`);
      else toast.warning(`成功 ${ok} 项，失败 ${fail} 项`);
      setCheckedIds(new Set());
      setConfirmOpen(false);
      reload();
    } finally {
      setDeleting(false);
    }
  };

  // 历史回填:启动(带确认,写明量级/耗时/可中断)与取消(优雅停止,断点保留)。
  const startBackfill = async () => {
    setBackfillBusy("start");
    try {
      await api.startVulnBackfill({ start_year: Number(backfillYear) });
      toast.success("历史回填已启动,后台执行中,可离开本页稍后回来看进度");
      setBackfillOpen(false);
      loadBackfill();
    } catch (e) {
      toast.error(`启动失败:${(e as Error).message}`);
    } finally {
      setBackfillBusy(null);
    }
  };

  const cancelBackfill = async () => {
    setBackfillBusy("cancel");
    try {
      await api.cancelVulnBackfill();
      toast.success("已请求取消,当前窗口完成后停止;进度保留,可随时续跑");
      loadBackfill();
    } catch (e) {
      toast.error(`取消失败:${(e as Error).message}`);
    } finally {
      setBackfillBusy(null);
    }
  };

  // 各源实时状态:后端 DISTINCT ON 每源最新一轮,按固定顺序排,未知源追加尾部。
  const latestRuns = React.useMemo(() => {
    const seen = new Map<string, VulnSyncRun>();
    for (const r of syncMeta?.latest ?? []) if (!seen.has(r.source)) seen.set(r.source, r);
    const ordered = SOURCE_ORDER.map((s) => seen.get(s)).filter((r): r is VulnSyncRun => !!r);
    for (const [s, r] of seen) if (!SOURCE_ORDER.includes(s)) ordered.push(r);
    return ordered;
  }, [syncMeta]);

  // 严重度「全部」页签计数:sev_counts 全值求和(含空严重度的老洞条目)。
  const sevAll = React.useMemo(() => Object.values(sevCounts).reduce((a, b) => a + b, 0), [sevCounts]);

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

  // 页签胶囊按钮(分类页签通用):选中实底、未选描边,带可选计数,aria-pressed 标识。
  const PillTab = ({
    active,
    onClick,
    count,
    children,
  }: {
    active: boolean;
    onClick: () => void;
    count?: number;
    children: React.ReactNode;
  }) => (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={`inline-flex items-center gap-1.5 rounded-full border px-3 py-1 text-xs transition-colors ${
        active ? "bg-primary text-primary-foreground border-primary" : "text-muted-foreground hover:bg-muted border-input"
      }`}
    >
      {children}
      {count !== undefined && <span className="tabular-nums opacity-80">{count}</span>}
    </button>
  );

  return (
    <div className="p-4 md:p-6">
      {/* 顶部:标题 + 全部操作按钮(用户要求按钮统一放顶部)。 */}
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div>
          <h1 className="font-semibold text-xl">CVE&CNVD 情报</h1>
          <p className="text-muted-foreground text-sm">
            有编号的公开漏洞与国家库收录漏洞;后台定时从 NVD / GitHub Advisory / Exploit-DB / CISA KEV /
            安全社区文章等公开源持续搜集并自动发现 PoC
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {syncMeta && (
            <Button
              variant="outline"
              size="sm"
              onClick={() =>
                void api
                  .setVulnSyncConfig({ enabled: !syncMeta.enabled })
                  .then(() => {
                    toast.success(syncMeta.enabled ? "已关闭定时采集" : "已开启定时采集");
                    loadMeta();
                  })
                  .catch((e) => toast.error(`设置失败:${(e as Error).message}`))
              }
            >
              {syncMeta.enabled ? `定时采集:开(每 ${syncMeta.interval_hours}h)` : "定时采集:关"}
            </Button>
          )}
          <Button variant="outline" size="sm" disabled={syncing} onClick={() => void runSync()}>
            <RefreshCwIcon className={syncing ? "size-4 animate-spin" : "size-4"} />
            立即采集
          </Button>
          {backfill?.running ? (
            <Button
              variant="destructive"
              size="sm"
              disabled={backfillBusy !== null}
              onClick={() => void cancelBackfill()}
            >
              取消回填
            </Button>
          ) : (
            <Button variant="outline" size="sm" onClick={() => setBackfillOpen(true)}>
              启动历史回填
            </Button>
          )}
          <Button size="sm" onClick={() => setCreating(true)}>
            <PlusIcon className="size-4" />
            新增 CVE
          </Button>
        </div>
      </div>

      {/* 主页签:情报源 / CVE / CNVD(可点,带计数;状态存 URL)。 */}
      <div className="mb-3 flex items-center gap-2">
        <PillTab active={tab === "sources"} onClick={() => switchTab("sources")} count={latestRuns.length || undefined}>
          情报源
        </PillTab>
        <PillTab active={tab === "cve"} onClick={() => switchTab("cve")} count={syncMeta?.counts?.cve}>
          CVE
        </PillTab>
        <PillTab active={tab === "cnvd"} onClick={() => switchTab("cnvd")} count={syncMeta?.counts?.cnvd}>
          CNVD
        </PillTab>
      </div>

      {/* ===== 情报源页签 ===== */}
      {tab === "sources" && (
        <div className="space-y-3">
          {/* CNVD 数据端点配置:指向采集代理(tools/cnvd_fetch.py)或官方会员 API 的 JSON 地址。 */}
          <div className="flex flex-wrap items-center gap-2 rounded-lg border px-4 py-2.5">
            <span className="text-sm font-medium">CNVD 数据端点</span>
            <Input
              value={cnvdUrlInput}
              onChange={(e) => setCnvdUrlInput(e.target.value)}
              placeholder="http://127.0.0.1:8930/cnvd.json(采集代理 tools/cnvd_fetch.py;留空=未启用)"
              className="h-8 min-w-0 flex-1 basis-64 text-xs"
            />
            <Button variant="outline" size="sm" disabled={cnvdUrlSaving} onClick={() => void saveCnvdUrl()}>
              保存
            </Button>
          </div>

          {/* 回填状态一行(启动/取消按钮在顶部工具栏):运行中显示推进窗口,空闲时给入口提示。 */}
          {backfill && (backfill.running || backfill.done || backfill.start_year > 0 || backfill.error) && (
            <div className="flex flex-wrap items-center gap-2 rounded-lg border px-4 py-2.5 text-sm">
              <span className="font-medium">NVD 历史回填</span>
              {backfill.running ? (
                <span className="text-muted-foreground text-xs">
                  {backfill.phase === "pocorexp"
                    ? "PocOrExp 历史挂载中"
                    : `NVD 历史分窗拉取中(已推进至 ${backfill.window_end?.split("T")[0] || "…"} )`}
                  <span className="ml-2">· 每 5 秒自动刷新</span>
                </span>
              ) : backfill.done ? (
                <span className="text-emerald-600 text-xs">历史回填已完成</span>
              ) : (
                <span className="text-amber-600 text-xs">
                  上次回填未完成{backfill.error ? `(${backfill.error})` : ""},再次启动将从断点续跑
                </span>
              )}
            </div>
          )}

          {/* PoC 补全回填(nuclei 正文 / trickest 历史)+ LLM 审核工作台(POC_INTEL 契约)。 */}
          <div className="grid gap-3 lg:grid-cols-3">
            <VulndbBackfillCard kind="nuclei" />
            <VulndbBackfillCard kind="trickest" />
            <PocReviewPanel />
          </div>

          {/* 各源实时状态(每源最近一轮,DISTINCT ON)。 */}
          <Card>
            <CardContent className="px-0 pt-4">
              <p className="text-muted-foreground px-4 pb-2 text-xs lg:px-6">各采集源最近一轮状态</p>
              <div className="overflow-hidden">
                <Table className="table-fixed text-xs">
                  <TableHeader>
                    <TableRow className="hover:bg-transparent">
                      <TableHead className="w-44">采集源</TableHead>
                      <TableHead className="w-16">状态</TableHead>
                      <TableHead className="w-16">新增</TableHead>
                      <TableHead className="w-16">PoC</TableHead>
                      <TableHead className="w-32">时间</TableHead>
                      <TableHead>说明</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {latestRuns.map((r) => (
                      <TableRow key={r.source}>
                        <TableCell className="truncate font-medium" title={SOURCE_LABELS[r.source] ?? r.source}>
                          {SOURCE_LABELS[r.source] ?? r.source}
                        </TableCell>
                        <TableCell>{runStatusText(r.status)}</TableCell>
                        <TableCell className="tabular-nums">{r.inserted}</TableCell>
                        <TableCell className="tabular-nums">{r.pocs_found}</TableCell>
                        <TableCell className="text-muted-foreground" title={fmtTime(r.started_at)}>
                          {fmtTimeShort(r.started_at)}
                        </TableCell>
                        <TableCell
                          className="text-muted-foreground truncate"
                          title={r.status === "failed" ? r.error : undefined}
                        >
                          {r.status === "failed" ? r.error || "—" : "—"}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            </CardContent>
          </Card>

          {/* 采集记录:源分类页签 + 状态筛选 + 搜索 + 分页。 */}
          <Card>
            <CardContent className="px-0 pt-4">
              <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
                {SOURCE_GROUPS.map((g) => (
                  <PillTab
                    key={g.key}
                    active={runGroup === g.key}
                    onClick={() => {
                      setRunGroup(g.key);
                      setRunPage(1);
                    }}
                  >
                    {g.label}
                  </PillTab>
                ))}
                <div className="relative w-full sm:max-w-[220px] sm:ml-2">
                  <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
                  <Input
                    value={runQuery}
                    onChange={(e) => setRunQuery(e.target.value)}
                    onKeyDown={(e) => e.key === "Enter" && searchRuns()}
                    placeholder="搜索源名称 / 错误信息"
                    className="pl-8"
                  />
                </div>
                <Button variant="outline" size="sm" onClick={searchRuns}>
                  搜索
                </Button>
                <Select
                  value={runStatus}
                  onValueChange={(v) => {
                    setRunStatus(v);
                    setRunPage(1);
                  }}
                >
                  <SelectTrigger className="w-32">
                    <SelectValue placeholder="状态" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      <SelectItem value="all">
                        全部状态 {runStatusCounts ? `(${Object.values(runStatusCounts).reduce((a, b) => a + b, 0)})` : ""}
                      </SelectItem>
                      <SelectItem value="done">完成 {runStatusCounts?.done ? `(${runStatusCounts.done})` : ""}</SelectItem>
                      <SelectItem value="running">
                        进行中 {runStatusCounts?.running ? `(${runStatusCounts.running})` : ""}
                      </SelectItem>
                      <SelectItem value="failed">失败 {runStatusCounts?.failed ? `(${runStatusCounts.failed})` : ""}</SelectItem>
                    </SelectGroup>
                  </SelectContent>
                </Select>
                <span className="text-muted-foreground ml-auto text-xs tabular-nums">共 {runsTotal} 条</span>
              </div>

              {runsLoading && runs.length === 0 ? (
                <p className="text-muted-foreground py-12 text-center text-sm">加载中…</p>
              ) : runs.length === 0 ? (
                <p className="text-muted-foreground py-12 text-center text-sm">没有匹配的采集记录</p>
              ) : (
                <>
                  <Table className="table-fixed text-xs">
                    <TableHeader>
                      <TableRow className="hover:bg-transparent">
                        <TableHead className="w-32">时间</TableHead>
                        <TableHead className="w-36">采集源</TableHead>
                        <TableHead className="w-16">状态</TableHead>
                        <TableHead className="w-14">新增</TableHead>
                        <TableHead className="w-14">更新</TableHead>
                        <TableHead className="w-14">PoC</TableHead>
                        <TableHead>说明</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {runs.map((r) => (
                        <TableRow key={r.id}>
                          <TableCell className="text-muted-foreground truncate" title={fmtTime(r.started_at)}>
                            {fmtTimeShort(r.started_at)}
                          </TableCell>
                          <TableCell className="truncate font-medium" title={SOURCE_LABELS[r.source] ?? r.source}>
                            {SOURCE_LABELS[r.source] ?? r.source}
                          </TableCell>
                          <TableCell>{runStatusText(r.status)}</TableCell>
                          <TableCell className="tabular-nums">{r.inserted}</TableCell>
                          <TableCell className="tabular-nums">{r.updated}</TableCell>
                          <TableCell className="tabular-nums">{r.pocs_found}</TableCell>
                          <TableCell
                            className="text-muted-foreground truncate"
                            title={r.status === "failed" ? r.error : r.error || undefined}
                          >
                            {r.error || "—"}
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                  <TablePagination
                    page={runPage}
                    pageSize={runPageSize}
                    total={runsTotal}
                    onPageChange={setRunPage}
                    onPageSizeChange={(s) => {
                      setRunPage(1);
                      setRunPageSize(s);
                    }}
                  />
                </>
              )}
            </CardContent>
          </Card>
        </div>
      )}

      {/* ===== CVE / CNVD 数据页签(共用同一套列表,仅 category 不同) ===== */}
      {tab !== "sources" && (
        <Card>
          <CardContent className="px-0 pt-4">
            {/* 严重度分类页签(带实时计数)。 */}
            <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
              {SEVERITY_TABS.map((s) => (
                <PillTab
                  key={s.key}
                  active={severity === s.key}
                  count={s.key === "all" ? sevAll : sevCounts?.[s.key]}
                  onClick={() => {
                    setSeverity(s.key);
                    setPage(1);
                  }}
                >
                  {s.label}
                </PillTab>
              ))}
            </div>
            <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
              <div className="relative w-full sm:max-w-xs">
                <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
                <Input
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  onKeyDown={(e) => e.key === "Enter" && search()}
                  placeholder="搜索 CVE 编号 / 标题 / 厂商 / 产品"
                  className="pl-8"
                />
              </div>
              <Button variant="outline" size="sm" onClick={search}>
                搜索
              </Button>
              <Select
                value={hasPoc}
                onValueChange={(v) => {
                  setPage(1);
                  setHasPoc(v);
                }}
              >
                <SelectTrigger className="w-32">
                  <SelectValue placeholder="PoC" />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    <SelectItem value="all">全部</SelectItem>
                    <SelectItem value="1">有 PoC</SelectItem>
                    <SelectItem value="0">无 PoC</SelectItem>
                  </SelectGroup>
                </SelectContent>
              </Select>
              <Select
                value={kev}
                onValueChange={(v) => {
                  setPage(1);
                  setKev(v);
                }}
              >
                <SelectTrigger className="w-28" title="CISA 已知被在野利用(KEV)筛选">
                  <SelectValue placeholder="在野利用" />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    <SelectItem value="all">全部</SelectItem>
                    <SelectItem value="1">在野利用</SelectItem>
                    <SelectItem value="0">非在野</SelectItem>
                  </SelectGroup>
                </SelectContent>
              </Select>
              {checkedIds.size > 0 && (
                <Button variant="destructive" size="sm" disabled={deleting} onClick={() => setConfirmOpen(true)}>
                  <Trash2Icon className="size-4" />
                  删除已选 ({checkedIds.size})
                </Button>
              )}
              <span className="text-muted-foreground ml-auto text-xs tabular-nums">
                {query ? `命中 ${total} / ` : ""}
                共 {total} 条
              </span>
            </div>

            {loading && entries.length === 0 ? (
              <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
            ) : entries.length === 0 ? (
              <p className="text-muted-foreground py-16 text-center text-sm">
                {query
                  ? "没有匹配的条目"
                  : listCategory === "cnvd"
                    ? "还没有 CNVD 数据。CNVD 编号会随安全社区文章持续入库;配置 vulndb_cnvd_url 数据端点(官方会员 API/自建镜像)可获得官方全量数据。"
                    : "还没有 CVE 数据。点「立即采集」从公开漏洞源拉取。"}
              </p>
            ) : (
              <>
                <Table className="table-fixed">
                  <TableHeader>
                    <TableRow className="hover:bg-transparent">
                      <TableHead className="w-8">
                        <Checkbox
                          checked={allSelected ? true : someSelected ? "indeterminate" : false}
                          onCheckedChange={(v) =>
                            setCheckedIds((prev) => {
                              const next = new Set(prev);
                              for (const e of entries) {
                                if (v === true) next.add(e.id);
                                else next.delete(e.id);
                              }
                              return next;
                            })
                          }
                          aria-label="全选本页"
                        />
                      </TableHead>
                      <SortHead field="number" width="w-32">
                        编号
                      </SortHead>
                      {/* 标题列不设宽度:table-fixed 下它会自动吃掉其余列之外的余量。
                          设 w-full 会挤掉后面的列(实测导致「更新时间」与「操作」重叠)。
                          固定列宽总和(688px)必须明显小于容器宽(约 897px),新列的宽度
                          从既有固定列里省(厂商 w-28→w-24、更新时间 w-40→w-36),别再加。 */}
                      <TableHead>标题</TableHead>
                      <TableHead className="w-24">厂商 / 产品</TableHead>
                      <TableHead className="w-20">严重度</TableHead>
                      <TableHead className="w-14" title="CISA 已知被在野利用">
                        KEV
                      </TableHead>
                      <SortHead field="poc" width="w-14">
                        PoC
                      </SortHead>
                      <SortHead field="updated" width="w-36">
                        更新时间
                      </SortHead>
                      <TableHead className="w-24">操作</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {entries.map((e) => {
                      // CNVD 条目没有 cve_id 列,编号在标题开头(入库格式「编号 描述」),
                      // 提取出来展示并链到 CNVD 官网详情。
                      const first = e.title.split(" ")[0] ?? "";
                      const cnvdId = !e.cve_id && /^CNVD-\d{4}-\d{4,7}$/.test(first) ? first : "";
                      return (
                      <TableRow key={e.id}>
                        <TableCell className="w-8" onClick={(e2) => e2.stopPropagation()}>
                          <Checkbox
                            checked={checkedIds.has(e.id)}
                            onCheckedChange={(v) => toggleChecked(e.id, v === true)}
                            aria-label={`选择 ${e.cve_id || e.title}`}
                          />
                        </TableCell>
                        <TableCell className="font-mono text-xs">
                          {e.cve_id ? (
                            <a
                              href={`https://nvd.nist.gov/vuln/detail/${e.cve_id}`}
                              target="_blank"
                              rel="noreferrer"
                              className="text-blue-600 hover:underline"
                              title={`在 NVD 查看 ${e.cve_id}`}
                            >
                              {e.cve_id}
                            </a>
                          ) : cnvdId ? (
                            <a
                              href={`https://www.cnvd.org.cn/flaw/show/${cnvdId}`}
                              target="_blank"
                              rel="noreferrer"
                              className="text-blue-600 hover:underline"
                              title={`在 CNVD 查看 ${cnvdId}`}
                            >
                              {cnvdId}
                            </a>
                          ) : (
                            "—"
                          )}
                        </TableCell>
                        {/* 标题列:table-fixed + truncate 保证长标题不溢出到相邻列 */}
                        <TableCell className="min-w-0">
                          <button
                            type="button"
                            onClick={() => setDetailID(e.id)}
                            className="block w-full truncate text-left hover:underline"
                            title={e.title}
                          >
                            {e.title}
                          </button>
                        </TableCell>
                        <TableCell
                          className="text-muted-foreground truncate text-xs"
                          title={[e.vendor, e.product].filter(Boolean).join(" / ")}
                        >
                          {[e.vendor, e.product].filter(Boolean).join(" / ") || "—"}
                        </TableCell>
                        <TableCell>
                          {e.severity ? (
                            <Badge variant="outline" className={severityBadgeClass(e.severity)}>
                              {e.severity}
                            </Badge>
                          ) : (
                            "—"
                          )}
                        </TableCell>
                        <TableCell>
                          {e.kev ? (
                            <Badge
                              variant="outline"
                              className="border-red-500/30 bg-red-500/15 px-1.5 text-[10px] text-red-600"
                              title="CISA 已知被在野利用"
                            >
                              KEV
                            </Badge>
                          ) : (
                            <span className="text-muted-foreground">—</span>
                          )}
                        </TableCell>
                        <TableCell className="tabular-nums">
                          {e.poc_count > 0 ? (
                            <span className="text-emerald-600">{e.poc_count}</span>
                          ) : (
                            <span className="text-muted-foreground">0</span>
                          )}
                        </TableCell>
                        <TableCell className="text-muted-foreground truncate text-xs" title={fmtTime(e.updated_at)}>
                          {fmtTimeShort(e.updated_at)}
                        </TableCell>
                        <TableCell>
                          <div className="flex items-center gap-1">
                            <Button variant="ghost" size="sm" onClick={() => setDetailID(e.id)} title="查看详情与 PoC">
                              <ExternalLinkIcon className="size-3.5" />
                            </Button>
                            <Button variant="ghost" size="sm" onClick={() => setEditing(e)} title="编辑">
                              <PencilIcon className="size-3.5" />
                            </Button>
                            <Button variant="ghost" size="sm" onClick={() => void remove(e)} title="删除">
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
      )}

      {/* 批量删除确认:兄弟节点,不进上方三元链。 */}
      <AlertDialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>确认删除</AlertDialogTitle>
            <AlertDialogDescription>
              将删除已选的 {checkedIds.size} 条 CVE 情报(含其 PoC),该操作不可恢复。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleting}>取消</AlertDialogCancel>
            <AlertDialogAction
              disabled={deleting}
              onClick={(e) => {
                e.preventDefault(); // 删除完成后再关弹层,期间确认按钮呈禁用态
                void batchDelete();
              }}
            >
              {deleting ? "删除中…" : "删除"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* 历史回填启动确认:选起始年,并写明数据量/耗时/可中断,避免误触发大批量拉取。 */}
      <AlertDialog open={backfillOpen} onOpenChange={setBackfillOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>启动 NVD 历史回填</AlertDialogTitle>
            <AlertDialogDescription>
              从选定的起始年起分窗全量拉取 NVD 历史 CVE,随后逐年在 PocOrExp 上挂载历史 PoC。全量约 20+
              万条,首次回填预计 15-30 分钟;可随时中断、从断点续跑,期间实例正常可用。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <div className="grid gap-1.5">
            <Label>起始年份</Label>
            <Select value={backfillYear} onValueChange={setBackfillYear}>
              <SelectTrigger className="w-44">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="2016">2016 年起(推荐)</SelectItem>
                  <SelectItem value="2011">2011 年起</SelectItem>
                  <SelectItem value="2020">2020 年起</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
          </div>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={backfillBusy !== null}>取消</AlertDialogCancel>
            <AlertDialogAction
              disabled={backfillBusy !== null}
              onClick={(e) => {
                e.preventDefault(); // 启动完成后再关弹层,期间确认按钮呈禁用态
                void startBackfill();
              }}
            >
              {backfillBusy === "start" ? "启动中…" : "开始回填"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <VulnEntryDialog
        entry={editing}
        defaultCategory="cve"
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
      <VulnDetailDialog
        entryID={detailID}
        onOpenChange={(o) => !o && setDetailID(null)}
        onChanged={reload}
        onEdit={(e) => {
          setDetailID(null);
          setEditing(e);
        }}
      />
    </div>
  );
}
