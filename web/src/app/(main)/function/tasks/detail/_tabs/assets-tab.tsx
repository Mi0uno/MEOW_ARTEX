"use client";

import * as React from "react";

import {
  ChevronLeftIcon,
  ChevronRightIcon,
  GlobeIcon,
  KeyRoundIcon,
  LayoutTemplateIcon,
  LinkIcon,
  type LucideIcon,
  NetworkIcon,
  PlusIcon,
  RefreshCwIcon,
  SmartphoneIcon,
  Trash2Icon,
} from "lucide-react";
import { toast } from "sonner";

import { AssetDslSearch } from "@/components/asset-dsl-search";
import { ScopeTextEditor } from "@/components/scope-text-editor";
import { TaskAssetArchive } from "@/components/task-asset-archive";
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
import { Card, CardContent, CardFooter } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { api } from "@/lib/api";
import { parseCompanyScopeText } from "@/lib/company-scope";
import { taskAssetSourceLabel } from "@/lib/task-assets";
import type { Asset, AssetExtractRun, NewAssetType } from "@/lib/types";
import { cn } from "@/lib/utils";

const PAGE_SIZES = [25, 50, 100, 200];

const METHOD_COLOR: Record<string, string> = {
  DELETE: "bg-red-100 text-red-700",
  GET: "bg-emerald-100 text-emerald-700",
  HEAD: "bg-purple-100 text-purple-700",
  OPTIONS: "bg-slate-100 text-slate-600",
  PATCH: "bg-orange-100 text-orange-700",
  POST: "bg-blue-100 text-blue-700",
  PUT: "bg-amber-100 text-amber-700",
};

const TABS: { key: NewAssetType; label: string; icon: LucideIcon }[] = [
  { key: "root_domain", label: "根域名", icon: GlobeIcon },
  { key: "ip", label: "IP", icon: NetworkIcon },
  { key: "subdomain", label: "子域名", icon: GlobeIcon },
  { key: "app", label: "应用", icon: SmartphoneIcon },
  { key: "service", label: "服务", icon: LayoutTemplateIcon },
  { key: "endpoint", label: "接口", icon: LinkIcon },
];

function firstText(values: Array<string | undefined>, fallback: string): string {
  return values.find((value) => value?.trim()) ?? fallback;
}

function assetLabel(asset: Asset): string {
  switch (asset.type) {
    case "root_domain":
    case "subdomain":
      return firstText([asset.domain], `#${asset.id}`);
    case "ip":
      return firstText([asset.ip], `#${asset.id}`);
    case "app":
      return firstText([asset.app_name, asset.url], `#${asset.id}`);
    case "service": {
      const host = firstText([asset.ip, asset.domain], "");
      const address = [host, asset.port].filter((value) => value !== undefined && value !== "").join(":");
      return firstText([asset.url, address, asset.service_name], `#${asset.id}`);
    }
    case "endpoint":
      return firstText([[asset.method, asset.url].filter(Boolean).join(" ")], `#${asset.id}`);
  }
}

function MethodBadge({ method }: { method: string }) {
  const normalized = method.toUpperCase();
  return (
    <span
      className={cn(
        "inline-block rounded px-1.5 py-0.5 font-mono font-semibold text-[10px] leading-none",
        METHOD_COLOR[normalized] ?? "bg-muted text-muted-foreground",
      )}
    >
      {normalized || "—"}
    </span>
  );
}

function statusTone(code: number) {
  if (code >= 500) return "text-red-500";
  if (code >= 400) return "text-amber-500";
  if (code >= 300) return "text-blue-500";
  if (code >= 200) return "text-emerald-500";
  return "text-muted-foreground";
}

function fmtBytes(value?: number | null) {
  if (!value || !Number.isFinite(value) || value <= 0) return "—";
  const units = ["B", "KB", "MB"];
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1);
  return `${index === 0 ? value : (value / 1024 ** index).toFixed(1)} ${units[index]}`;
}

function Chips({ items, mono }: { items: string[]; mono?: boolean }) {
  const clean = items.filter(Boolean);
  if (clean.length === 0) return <span className="text-muted-foreground text-xs">—</span>;
  return (
    <div className="flex flex-wrap gap-1">
      {clean.map((item) => (
        <Badge key={item} variant="outline" className={cn(mono && "font-mono")}>
          {item}
        </Badge>
      ))}
    </div>
  );
}

function SourceCell({ asset }: { asset: Asset }) {
  const source = firstText([asset.task_source], "legacy");
  const summary = firstText([asset.task_source_summary], "由历史任务资产关联迁移，暂无更详细来源说明");
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Badge variant="outline" className="max-w-28 shrink-0 font-normal">
          <span className="truncate">{taskAssetSourceLabel(source)}</span>
        </Badge>
      </TooltipTrigger>
      <TooltipContent side="left" align="start" className="max-w-sm">
        <div className="flex min-w-0 flex-col gap-1">
          <span className="font-medium">{taskAssetSourceLabel(source)}</span>
          <span className="[overflow-wrap:anywhere]">{summary}</span>
          {asset.task_source_node_id ? (
            <span className="font-mono opacity-80">来源节点 #{asset.task_source_node_id}</span>
          ) : null}
        </div>
      </TooltipContent>
    </Tooltip>
  );
}

// 识别轮状态徽标(契约 §8):无 run=灰点「未识别」;running=Spinner「识别中」;
// done=绿「已入库 N」(tooltip 细分计数);partial/failed=黄/红(tooltip 错误原因)。
function ExtractStatusBadge({ run }: { run: AssetExtractRun | null }) {
  const status = run?.status ?? "";
  const extracting = status === "running" || status === "pending";
  let dotClass = "bg-muted-foreground/40";
  let label = "未识别";
  let tip: React.ReactNode = "该任务尚未运行过资产识别";
  if (extracting) {
    label = "识别中";
    tip = "正在扫描任务过程数据并提取资产候选";
  } else if (status === "done" && run) {
    dotClass = "bg-emerald-500";
    label = `已入库 ${run.adopted_count}`;
    tip = `提取 ${run.deterministic_count} · AI 确认 ${run.ai_confirmed_count} · 新发现 ${run.ai_new_count}`;
  } else if (status === "partial" && run) {
    dotClass = "bg-amber-500";
    label = "部分完成";
    tip = run.error || "确定性提取完成，AI 识别段未执行或失败；候选保留在暂存区待人工采纳";
  } else if (status === "failed" && run) {
    dotClass = "bg-red-500";
    label = "识别失败";
    tip = run.error || "识别过程出现异常，可稍后重新识别";
  }
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Badge variant="outline" className="gap-1.5 font-normal">
          {extracting ? (
            <Spinner className="size-3" />
          ) : (
            <span aria-hidden className={cn("size-1.5 rounded-full", dotClass)} />
          )}
          {label}
        </Badge>
      </TooltipTrigger>
      <TooltipContent side="bottom" align="end" className="max-w-xs">
        <div className="flex min-w-0 flex-col gap-1">
          <span className="font-medium">{label}</span>
          <span className="[overflow-wrap:anywhere]">{tip}</span>
        </div>
      </TooltipContent>
    </Tooltip>
  );
}

function AssetCard({
  children,
  cols,
  loaded,
  onPage,
  onSize,
  page,
  size,
  total,
}: {
  children: React.ReactNode;
  cols: string[];
  loaded: boolean;
  onPage: (page: number) => void;
  onSize: (size: number) => void;
  page: number;
  size: number;
  total: number;
}) {
  const rows = React.Children.toArray(children);
  const pageCount = Math.max(1, Math.ceil(total / size));
  const start = total === 0 ? 0 : page * size + 1;
  const end = Math.min(total, page * size + rows.length);
  let tableRows: React.ReactNode;
  if (!loaded) {
    tableRows = (
      <TableRow>
        <TableCell colSpan={cols.length} className="py-14 text-center">
          <Spinner className="mx-auto" />
        </TableCell>
      </TableRow>
    );
  } else if (rows.length > 0) {
    tableRows = rows;
  } else {
    tableRows = (
      <TableRow>
        <TableCell colSpan={cols.length} className="py-10 text-center text-muted-foreground text-sm">
          当前分类暂无测试资产
        </TableCell>
      </TableRow>
    );
  }
  return (
    <Card className="flex min-h-0 flex-1 flex-col py-0">
      <CardContent className="scrollbar-thin scrollbar-track-transparent min-h-0 flex-1 overflow-auto p-0">
        <Table>
          <TableHeader className="sticky top-0 z-10 bg-card">
            <TableRow>
              {cols.map((column) => (
                <TableHead key={column}>{column}</TableHead>
              ))}
            </TableRow>
          </TableHeader>
          <TableBody>{tableRows}</TableBody>
        </Table>
      </CardContent>
      {total > 0 ? (
        <CardFooter className="gap-2 px-3 py-1.5 text-muted-foreground text-xs">
          <Select
            value={String(size)}
            onValueChange={(value) => {
              onSize(Number(value));
              onPage(0);
            }}
          >
            <SelectTrigger size="sm" className="w-24">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                {PAGE_SIZES.map((value) => (
                  <SelectItem key={value} value={String(value)}>
                    {value} / 页
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
          <span className="tabular-nums">
            {start}–{end} / {total}
          </span>
          {pageCount > 1 ? (
            <div className="ml-auto flex items-center gap-2">
              <Button
                variant="outline"
                size="icon-sm"
                disabled={page <= 0}
                onClick={() => onPage(Math.max(0, page - 1))}
                aria-label="上一页"
              >
                <ChevronLeftIcon />
              </Button>
              <span className="tabular-nums">
                {page + 1} / {pageCount}
              </span>
              <Button
                variant="outline"
                size="icon-sm"
                disabled={page + 1 >= pageCount}
                onClick={() => onPage(Math.min(pageCount - 1, page + 1))}
                aria-label="下一页"
              >
                <ChevronRightIcon />
              </Button>
            </div>
          ) : null}
        </CardFooter>
      ) : null}
    </Card>
  );
}

function AddTaskAssetsSheet({
  onAttached,
  onOpenChange,
  open,
  taskId,
}: {
  onAttached: () => void;
  onOpenChange: (open: boolean) => void;
  open: boolean;
  taskId: string;
}) {
  const [scopeText, setScopeText] = React.useState("");
  const [saving, setSaving] = React.useState(false);
  const parsedScope = React.useMemo(() => parseCompanyScopeText(scopeText), [scopeText]);

  React.useEffect(() => {
    if (!open) return;
    setScopeText("");
  }, [open]);

  const attach = async () => {
    if (parsedScope.rules.length === 0 || parsedScope.errors.length > 0) return;
    setSaving(true);
    try {
      const result = await api.registerTaskAssetScopes(taskId, parsedScope.rules);
      const assetSummary = result.assets_linked + result.assets_existing;
      toast.success(`已登记 ${result.requested} 条范围，关联 ${assetSummary} 项域名/IP 资产`);
      onAttached();
      onOpenChange(false);
    } catch (reason) {
      toast.error(`新增失败：${String((reason as Error)?.message ?? reason)}`);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>新增测试资产</SheetTitle>
          <SheetDescription>
            直接填写测试范围。域名和 IP 会创建或复用全局资产；CIDR、ICP 和关键词作为 Agent 范围上下文。
          </SheetDescription>
        </SheetHeader>
        <div className="flex min-h-0 flex-1 flex-col overflow-y-auto px-4">
          <ScopeTextEditor
            id="task-asset-scope"
            value={scopeText}
            onValueChange={setScopeText}
            parsed={parsedScope}
            label="测试资产与范围"
            description="每行一条，自动识别域名、IP、CIDR、ICP 备案和关键词。"
          />
        </div>
        <SheetFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={saving}>
            取消
          </Button>
          <Button
            onClick={() => void attach()}
            disabled={saving || parsedScope.rules.length === 0 || parsedScope.errors.length > 0}
          >
            {saving ? <Spinner data-icon="inline-start" /> : <PlusIcon data-icon="inline-start" />}
            登记 {parsedScope.rules.length > 0 ? parsedScope.rules.length : ""} 条
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  );
}

export function AssetsTab({ taskId }: { taskId: string }) {
  const [rows, setRows] = React.useState<Asset[]>([]);
  const [total, setTotal] = React.useState(0);
  const [counts, setCounts] = React.useState<Record<string, number>>({});
  const [loaded, setLoaded] = React.useState(false);
  const [tab, setTab] = React.useState<NewAssetType>("root_domain");
  const [page, setPage] = React.useState(0);
  const [size, setSize] = React.useState(50);
  const [query, setQuery] = React.useState("");
  const [loading, setLoading] = React.useState(false);
  const [dslError, setDslError] = React.useState("");
  const [refreshKey, setRefreshKey] = React.useState(0);
  const [addOpen, setAddOpen] = React.useState(false);
  const [archiveOpen, setArchiveOpen] = React.useState(false);
  const [removeTarget, setRemoveTarget] = React.useState<Asset | null>(null);
  const [removing, setRemoving] = React.useState(false);
  // 识别轮(契约 §8):run 初载一次,running 中 5s 轮询到终态;AI 开关一次加载。
  const [extractRun, setExtractRun] = React.useState<AssetExtractRun | null>(null);
  const [extractLoaded, setExtractLoaded] = React.useState(false);
  const [extractStarting, setExtractStarting] = React.useState(false);
  const [aiEnabled, setAiEnabled] = React.useState<boolean | null>(null);
  const assetsRequestRef = React.useRef(0);
  const dslMode = query.trim() !== "";

  React.useEffect(() => {
    setPage(0);
    setRows([]);
    setLoaded(false);
  }, []);

  // Switching type tabs resets the search (DSL fields differ per type; matches
  // the global asset view).
  // biome-ignore lint/correctness/useExhaustiveDependencies: tab changes intentionally reset tab-local controls.
  React.useEffect(() => {
    setQuery("");
    setDslError("");
  }, [tab]);

  // Query / tab / page-size changes restart server pagination.
  // biome-ignore lint/correctness/useExhaustiveDependencies: these controls intentionally reset server pagination.
  React.useEffect(() => setPage(0), [tab, size, query]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshKey is an explicit manual-reload trigger.
  React.useEffect(() => {
    let active = true;
    const dsl = query.trim();
    const request = ++assetsRequestRef.current;
    const load = async () => {
      setLoading(true);
      try {
        // The search always stays within this task's assets: taskAssets and
        // searchTaskAssets both pin task_id server-side.
        const [current, nextCounts] = await Promise.all([
          dsl
            ? api.searchTaskAssets(taskId, dsl, tab, size, page * size)
            : api.taskAssets(taskId, tab, size, page * size),
          api.assetCounts(taskId),
        ]);
        if (!active || assetsRequestRef.current !== request) return;
        setRows(current.assets);
        setTotal(current.total);
        setCounts(nextCounts ?? {});
        setDslError("");
      } catch (reason) {
        if (!active || assetsRequestRef.current !== request) return;
        const message = String((reason as Error)?.message ?? reason);
        if (dsl) {
          setDslError(message);
          setRows([]);
          setTotal(0);
        } else {
          toast.error(`加载任务资产失败：${message}`);
        }
      } finally {
        if (active && assetsRequestRef.current === request) {
          setLoading(false);
          setLoaded(true);
        }
      }
    };
    // Debounce DSL keystrokes; plain (re)loads and polling run immediately.
    const debounce = setTimeout(() => void load(), dsl ? 400 : 0);
    const timer = setInterval(() => void load(), 10_000);
    return () => {
      active = false;
      clearTimeout(debounce);
      clearInterval(timer);
    };
  }, [page, query, refreshKey, size, tab, taskId]);

  React.useEffect(() => {
    const maxPage = Math.max(0, Math.ceil(total / size) - 1);
    if (page > maxPage) setPage(maxPage);
  }, [page, size, total]);

  // 识别轮初载:失败静默降级为「未识别」,不阻塞资产列表。
  React.useEffect(() => {
    let cancelled = false;
    setExtractLoaded(false);
    api
      .taskAssetExtract(taskId)
      .then((run) => {
        if (!cancelled) setExtractRun(run);
      })
      .catch(() => {
        // 识别轮加载失败静默降级为「未识别」,不阻塞资产列表。
      })
      .finally(() => {
        if (!cancelled) setExtractLoaded(true);
      });
    return () => {
      cancelled = true;
    };
  }, [taskId]);

  // AI 识别开关:全局配置,进页签加载一次。
  React.useEffect(() => {
    let cancelled = false;
    api
      .assetExtractConfig()
      .then((config) => {
        if (!cancelled) setAiEnabled(Boolean(config.ai_enabled));
      })
      .catch(() => {
        if (!cancelled) setAiEnabled(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const refresh = React.useCallback(() => {
    setLoaded(false);
    setRefreshKey((current) => current + 1);
  }, []);

  // 识别进行中每 5s 轮询,到终态(done/partial/failed)停表并刷新资产列表。
  const extractActive = extractRun?.status === "running" || extractRun?.status === "pending";
  React.useEffect(() => {
    if (!extractActive) return;
    const timer = setInterval(() => {
      api
        .taskAssetExtract(taskId)
        .then((next) => {
          setExtractRun(next);
          if (next && next.status !== "running" && next.status !== "pending") refresh();
        })
        .catch(() => {
          // 瞬时错误忽略,下个周期重试。
        });
    }, 5000);
    return () => clearInterval(timer);
  }, [extractActive, refresh, taskId]);

  const toggleAiExtract = async (next: boolean) => {
    const previous = aiEnabled;
    setAiEnabled(next); // 乐观更新,失败回滚 UI 态。
    try {
      await api.saveAssetExtractConfig({ ai_enabled: next });
      toast.success(next ? "已开启 AI 识别" : "已关闭 AI 识别");
    } catch (reason) {
      setAiEnabled(previous ?? false);
      toast.error(`保存 AI 识别开关失败：${String((reason as Error)?.message ?? reason)}`);
    }
  };

  const startExtract = async () => {
    if (extractStarting) return;
    setExtractStarting(true);
    try {
      await api.runAssetExtract(taskId);
      toast.success("已开始识别");
      // 立即置本地 running 进入轮询;真实计数由轮询回填。
      setExtractRun({
        id: 0,
        task_id: Number(taskId),
        status: "running",
        deterministic_count: 0,
        ai_confirmed_count: 0,
        ai_new_count: 0,
        adopted_count: 0,
        error: "",
        created_at: "",
        updated_at: "",
      });
    } catch (reason) {
      const message = String((reason as Error)?.message ?? reason);
      // running 中重复触发是 409(错误体可能带中文 message,按状态码文案兜底)。
      if (message.includes("409")) {
        toast.error("识别正在进行中");
      } else {
        toast.error(message);
      }
      // 无论成败都同步一次真实状态:409 时本地徽标可能已过期。
      api
        .taskAssetExtract(taskId)
        .then((run) => setExtractRun(run))
        .catch(() => {
          // 同步失败忽略,下次触发或轮询会再取。
        });
    } finally {
      setExtractStarting(false);
    }
  };

  const remove = async () => {
    if (!removeTarget) return;
    setRemoving(true);
    try {
      await api.detachTaskAsset(taskId, removeTarget.id);
      toast.success(`已将 ${assetLabel(removeTarget)} 移出当前任务`);
      setRemoveTarget(null);
      refresh();
    } catch (reason) {
      toast.error(`移出失败：${String((reason as Error)?.message ?? reason)}`);
    } finally {
      setRemoving(false);
    }
  };

  const removeButton = (asset: Asset) => (
    <Button
      variant="ghost"
      size="icon-sm"
      onClick={() => setRemoveTarget(asset)}
      aria-label={`将资产 ${assetLabel(asset)} 移出任务`}
      title="移出任务"
    >
      <Trash2Icon />
    </Button>
  );

  const commonCardProps = { loaded, onPage: setPage, onSize: setSize, page, size, total };
  const totalAll = TABS.reduce((sum, item) => sum + (counts[item.key] ?? 0), 0);

  const searchBox = (
    <AssetDslSearch
      query={query}
      onChange={setQuery}
      loading={loading}
      error={dslError}
      count={dslMode ? total : undefined}
    />
  );

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <h2 className="font-medium text-sm">测试资产</h2>
          <p className="text-muted-foreground text-xs">当前任务共关联 {totalAll} 项资产</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {extractLoaded ? <ExtractStatusBadge run={extractRun} /> : null}
          <div className="flex items-center gap-1.5">
            <Switch
              id="task-asset-ai-extract"
              size="sm"
              checked={aiEnabled ?? false}
              disabled={aiEnabled === null}
              onCheckedChange={(value) => void toggleAiExtract(value)}
              aria-label="AI 识别开关"
            />
            <Label htmlFor="task-asset-ai-extract" className="text-muted-foreground text-xs">
              AI 识别
            </Label>
          </div>
          <Button
            variant="outline"
            size="icon-sm"
            disabled={extractStarting}
            onClick={() => void startExtract()}
            title="重新识别资产"
            aria-label="重新识别资产"
          >
            {extractStarting ? <Spinner /> : <RefreshCwIcon />}
          </Button>
          <Button variant="outline" size="sm" onClick={() => setArchiveOpen(true)}>
            归档资产
          </Button>
          <Button size="sm" onClick={() => setAddOpen(true)}>
            <PlusIcon data-icon="inline-start" />
            新增测试资产
          </Button>
        </div>
      </div>

      <Tabs
        value={tab}
        onValueChange={(value) => setTab(value as NewAssetType)}
        className="flex min-h-0 flex-1 flex-col gap-2"
      >
        <div className="overflow-x-auto overflow-y-hidden">
          <TabsList className="w-max">
            {TABS.map((item) => (
              <TabsTrigger key={item.key} value={item.key}>
                <item.icon data-icon="inline-start" />
                {item.label}
                <span className="text-muted-foreground tabular-nums">{counts[item.key] ?? 0}</span>
              </TabsTrigger>
            ))}
          </TabsList>
        </div>

        {searchBox}

        <TabsContent value="root_domain" className="mt-0 flex min-h-0 flex-1 flex-col">
          <AssetCard cols={["域名", "ICP 备案", "来源", "操作"]} {...commonCardProps}>
            {rows.map((asset) => (
              <TableRow key={asset.id}>
                <TableCell className="font-medium font-mono text-xs">{asset.domain}</TableCell>
                <TableCell className="text-xs">{asset.icp || "—"}</TableCell>
                <TableCell>
                  <SourceCell asset={asset} />
                </TableCell>
                <TableCell className="w-16">{removeButton(asset)}</TableCell>
              </TableRow>
            ))}
          </AssetCard>
        </TabsContent>

        <TabsContent value="ip" className="mt-0 flex min-h-0 flex-1 flex-col">
          <AssetCard cols={["IP", "C段", "绑定域名", "开放端口", "来源", "操作"]} {...commonCardProps}>
            {rows.map((asset) => (
              <TableRow key={asset.id}>
                <TableCell className="font-medium font-mono text-xs">{asset.ip}</TableCell>
                <TableCell className="font-mono text-xs">{asset.c_segment || "—"}</TableCell>
                <TableCell>
                  <Chips items={asset.bound_domains ?? []} mono />
                </TableCell>
                <TableCell>
                  <Chips
                    items={(asset.open_ports ?? []).map((item) =>
                      item.service ? `${item.port}/${item.service}` : String(item.port),
                    )}
                    mono
                  />
                </TableCell>
                <TableCell>
                  <SourceCell asset={asset} />
                </TableCell>
                <TableCell className="w-16">{removeButton(asset)}</TableCell>
              </TableRow>
            ))}
          </AssetCard>
        </TabsContent>

        <TabsContent value="subdomain" className="mt-0 flex min-h-0 flex-1 flex-col">
          <AssetCard cols={["域名", "根域名", "解析类型", "解析值", "来源", "操作"]} {...commonCardProps}>
            {rows.map((asset) => (
              <TableRow key={asset.id}>
                <TableCell className="font-medium font-mono text-xs">{asset.domain}</TableCell>
                <TableCell className="font-mono text-xs">{asset.root_domain || "—"}</TableCell>
                <TableCell className="text-xs">{asset.record_type || "—"}</TableCell>
                <TableCell className="max-w-xs truncate font-mono text-xs">
                  {(Array.isArray(asset.record_value) ? asset.record_value.join(", ") : asset.record_value) || "—"}
                </TableCell>
                <TableCell>
                  <SourceCell asset={asset} />
                </TableCell>
                <TableCell className="w-16">{removeButton(asset)}</TableCell>
              </TableRow>
            ))}
          </AssetCard>
        </TabsContent>

        <TabsContent value="app" className="mt-0 flex min-h-0 flex-1 flex-col">
          <AssetCard cols={["应用", "地址", "分类", "标题", "指纹", "来源", "操作"]} {...commonCardProps}>
            {rows.map((asset) => (
              <TableRow key={asset.id}>
                <TableCell className="max-w-48 truncate font-medium text-xs">{asset.app_name || "—"}</TableCell>
                <TableCell className="max-w-xs truncate font-mono text-xs" title={asset.url}>
                  {asset.url || "—"}
                </TableCell>
                <TableCell className="text-xs">{asset.category || "—"}</TableCell>
                <TableCell className="max-w-48 truncate text-xs">{asset.page_title || "—"}</TableCell>
                <TableCell>
                  <Chips items={asset.technologies ?? []} />
                </TableCell>
                <TableCell>
                  <SourceCell asset={asset} />
                </TableCell>
                <TableCell className="w-16">{removeButton(asset)}</TableCell>
              </TableRow>
            ))}
          </AssetCard>
        </TabsContent>

        <TabsContent value="service" className="mt-0 flex min-h-0 flex-1 flex-col">
          <AssetCard
            cols={["地址 / 服务", "状态码", "标题", "响应长度", "指纹", "认证", "来源", "操作"]}
            {...commonCardProps}
          >
            {rows.map((asset) => {
              const isHttp = asset.service_type === "http";
              const address = isHttp
                ? asset.url || ""
                : asset.service_name || [asset.ip || asset.domain, asset.port].filter(Boolean).join(":");
              return (
                <TableRow key={asset.id}>
                  <TableCell className="max-w-xs truncate font-mono text-xs" title={address}>
                    {address || "—"}
                  </TableCell>
                  <TableCell>
                    {asset.status_code != null ? (
                      <span
                        className={cn("font-mono font-semibold text-xs tabular-nums", statusTone(asset.status_code))}
                      >
                        {asset.status_code}
                      </span>
                    ) : (
                      "—"
                    )}
                  </TableCell>
                  <TableCell className="max-w-48 truncate text-xs">{asset.page_title || "—"}</TableCell>
                  <TableCell className="text-xs tabular-nums">{fmtBytes(asset.content_length)}</TableCell>
                  <TableCell>
                    <Chips items={asset.technologies ?? []} />
                  </TableCell>
                  <TableCell>
                    {(asset.auth ?? []).length === 0 ? (
                      <span className="text-muted-foreground text-xs">—</span>
                    ) : (
                      (asset.auth ?? []).map((authItem) => {
                        const item = authItem as Record<string, string>;
                        return (
                          <span
                            key={`${asset.id}-${item.type ?? ""}-${item.username ?? ""}-${JSON.stringify(item)}`}
                            className="inline-flex items-center gap-1 text-[11px]"
                          >
                            <KeyRoundIcon className="size-3 text-muted-foreground" />
                            <span className="font-mono">{item.type || item.username || "认证"}</span>
                          </span>
                        );
                      })
                    )}
                  </TableCell>
                  <TableCell>
                    <SourceCell asset={asset} />
                  </TableCell>
                  <TableCell className="w-16">{removeButton(asset)}</TableCell>
                </TableRow>
              );
            })}
          </AssetCard>
        </TabsContent>

        <TabsContent value="endpoint" className="mt-0 flex min-h-0 flex-1 flex-col">
          <AssetCard cols={["方法", "完整地址", "参数", "来源", "操作"]} {...commonCardProps}>
            {rows.map((asset) => (
              <TableRow key={asset.id}>
                <TableCell className="w-16">
                  <MethodBadge method={asset.method || ""} />
                </TableCell>
                <TableCell className="max-w-sm truncate font-mono text-xs" title={asset.url}>
                  {asset.url || "—"}
                </TableCell>
                <TableCell>
                  <Chips
                    items={(asset.params ?? []).map((param) => {
                      const item = param as Record<string, string>;
                      return item.name ? `${item.name}(${item.location || item.in || "?"})` : "?";
                    })}
                    mono
                  />
                </TableCell>
                <TableCell>
                  <SourceCell asset={asset} />
                </TableCell>
                <TableCell className="w-16">{removeButton(asset)}</TableCell>
              </TableRow>
            ))}
          </AssetCard>
        </TabsContent>
      </Tabs>

      <AddTaskAssetsSheet onAttached={refresh} onOpenChange={setAddOpen} open={addOpen} taskId={taskId} />

      <TaskAssetArchive taskId={taskId} onArchived={refresh} open={archiveOpen} onOpenChange={setArchiveOpen} />

      <AlertDialog open={Boolean(removeTarget)} onOpenChange={(open) => !open && !removing && setRemoveTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>移出当前任务？</AlertDialogTitle>
            <AlertDialogDescription className="[overflow-wrap:anywhere]">
              {removeTarget ? `将“${assetLabel(removeTarget)}”从当前任务的测试资产中移出。` : ""}
              全局资产、关联流量和历史黑板锚点会继续保留。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={removing}>取消</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={removing}
              onClick={(event) => {
                event.preventDefault();
                void remove();
              }}
            >
              {removing ? <Spinner data-icon="inline-start" /> : <Trash2Icon data-icon="inline-start" />}
              {removing ? "移出中" : "确认移出"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
