"use client";

// recon 详情页签内容(链路图/执行轨迹/暂存区):由共享的 TaskDetailView 渲染。
// taskStatus === "running" 时各面板每 5s 自动刷新。

import * as React from "react";

import { CheckIcon, DownloadIcon, EyeIcon, XIcon } from "lucide-react";
import { toast } from "sonner";

import { ReconGraph } from "@/components/recon-graph";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { CollectorRun, Edge, StagedAsset, StagedAssetCounts, Task, TaskNode } from "@/lib/types";

function fmtTime(iso?: string | null): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString();
}

function runBadgeClass(status: string): string {
  switch (status) {
    case "running":
      return "bg-blue-500/15 text-blue-600 border-blue-500/30";
    case "done":
      return "bg-emerald-500/15 text-emerald-600 border-emerald-500/30";
    case "failed":
      return "bg-red-500/15 text-red-600 border-red-500/30";
    default:
      return "bg-muted text-muted-foreground";
  }
}

function statsSummary(run: CollectorRun): string {
  const stats = run.stats ?? {};
  if (run.status === "skipped") return "已跳过";
  const parts: string[] = [];
  if (typeof stats.total === "number") parts.push(`产出 ${stats.total}`);
  if (typeof stats.staged === "number") parts.push(`暂存 ${stats.staged}`);
  if (typeof stats.unique_hosts === "number") parts.push(`主机 ${stats.unique_hosts}`);
  return parts.join(" · ") || "—";
}

function usePolling(active: boolean, intervalMs: number, fn: () => void) {
  const ref = React.useRef(fn);
  ref.current = fn;
  React.useEffect(() => {
    if (!active) return;
    const timer = setInterval(() => ref.current(), intervalMs);
    return () => clearInterval(timer);
  }, [active, intervalMs]);
}

function EmptyHint({ text }: { text: string }) {
  return <p className="text-muted-foreground py-16 text-center text-sm">{text}</p>;
}

export function ReconGraphPanel({ taskId, taskStatus }: { taskId: string; taskStatus?: string }) {
  const [nodes, setNodes] = React.useState<TaskNode[]>([]);
  const [edges, setEdges] = React.useState<Edge[]>([]);
  const sigRef = React.useRef("");

  const load = React.useCallback(() => {
    api
      .explorationGraph(taskId)
      .then((g) => {
        const ns = g.nodes ?? [];
        const es = g.edges ?? [];
        const sig = JSON.stringify([
          ns.map((n) => [n.id, n.type, n.state, n.payload]),
          es.map((e) => [e.src, e.dst, e.rel]),
        ]);
        if (sig === sigRef.current) return;
        sigRef.current = sig;
        setNodes(ns);
        setEdges(es);
      })
      .catch(() => undefined);
  }, [taskId]);

  React.useEffect(() => {
    load();
  }, [load]);
  usePolling(taskStatus === "running", 5000, load);

  return (
    <Card>
      <CardContent className="p-0">
        {nodes.length === 0 ? (
          <EmptyHint text="链路图将在任务运行后生成(目标 → 步骤 → 发现 → 汇总)" />
        ) : (
          <ReconGraph nodes={nodes} edges={edges} className="h-[72vh]" />
        )}
      </CardContent>
    </Card>
  );
}

export function ReconRunsPanel({ taskId, taskStatus }: { taskId: string; taskStatus?: string }) {
  const [runs, setRuns] = React.useState<CollectorRun[]>([]);

  const load = React.useCallback(() => {
    api
      .collectorRuns(taskId)
      .then(setRuns)
      .catch(() => undefined);
  }, [taskId]);

  React.useEffect(() => {
    load();
  }, [load]);
  usePolling(taskStatus === "running", 5000, load);

  return (
    <Card>
      <CardContent className="pt-4">
        {runs.length === 0 ? (
          <EmptyHint text="还没有执行记录" />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>收集器</TableHead>
                <TableHead className="w-24">状态</TableHead>
                <TableHead>结果</TableHead>
                <TableHead className="w-44">时间</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {runs.map((run) => (
                <TableRow key={run.id}>
                  <TableCell className="font-medium">{run.collector}</TableCell>
                  <TableCell>
                    <Badge variant="outline" className={runBadgeClass(run.status)}>
                      {run.status === "running" ? "运行中" : run.status === "done" ? "完成" : run.status}
                    </Badge>
                  </TableCell>
                  <TableCell>
                    <div className="text-sm">{statsSummary(run)}</div>
                    {run.error ? <div className="mt-0.5 text-red-600 text-xs">{run.error}</div> : null}
                  </TableCell>
                  <TableCell className="text-muted-foreground text-xs">
                    {fmtTime(run.started_at)}
                    {run.ended_at ? ` → ${fmtTime(run.ended_at)}` : ""}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}

export function ReconStagedPanel({ taskId, taskStatus }: { taskId: string; taskStatus?: string }) {
  const [assets, setAssets] = React.useState<StagedAsset[]>([]);
  const [counts, setCounts] = React.useState<StagedAssetCounts | null>(null);
  const [statusFilter, setStatusFilter] = React.useState<string>("all");
  const [busy, setBusy] = React.useState(false);

  const load = React.useCallback(() => {
    api
      .stagedAssets(taskId, statusFilter)
      .then((r) => {
        setAssets(r.assets);
        setCounts(r.counts);
      })
      .catch(() => undefined);
  }, [taskId, statusFilter]);

  React.useEffect(() => {
    load();
  }, [load]);
  usePolling(taskStatus === "running", 5000, load);

  // decide 落定暂存资产:ids 为空数组 = 全部 pending;完成后刷新 + 计数随响应更新。
  const decide = async (ids: number[], action: "adopt" | "ignore") => {
    setBusy(true);
    try {
      const r = await api.decideStagedAssets(taskId, ids, action);
      const verb = action === "adopt" ? "采纳" : "忽略";
      if (r.errors.length > 0) {
        toast.error(`${verb}完成,${r.errors.length} 条失败:${r.errors[0].error}`);
      } else if (r.results.length > 0) {
        toast.success(`${verb} ${r.results.length} 条资产`);
      } else {
        toast.info("没有可处理的待采纳资产");
      }
      load();
    } catch (e) {
      toast.error(`操作失败:${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  const pendingCount = counts?.pending ?? 0;

  const decideButtons = (id: number) => (
    <div className="flex items-center gap-1">
      <Button variant="ghost" size="sm" disabled={busy} onClick={() => void decide([id], "adopt")}>
        <CheckIcon className="size-4 text-emerald-600" />
        采纳
      </Button>
      <Button variant="ghost" size="sm" disabled={busy} onClick={() => void decide([id], "ignore")}>
        <XIcon className="size-4 text-muted-foreground" />
        忽略
      </Button>
    </div>
  );

  return (
    <Card>
      <CardContent className="pt-4">
        <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
          <div className="text-muted-foreground text-sm">
            {counts ? `待采纳 ${counts.pending} · 已采纳 ${counts.accepted} · 已忽略 ${counts.ignored}` : ""}
          </div>
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              disabled={busy || pendingCount === 0}
              onClick={() => void decide([], "adopt")}
            >
              <CheckIcon className="size-4" />
              采纳全部待采纳
            </Button>
            <Button
              variant="outline"
              size="sm"
              disabled={busy || pendingCount === 0}
              onClick={() => void decide([], "ignore")}
            >
              <XIcon className="size-4" />
              忽略全部待采纳
            </Button>
            <Select value={statusFilter} onValueChange={setStatusFilter}>
              <SelectTrigger className="w-32">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="all">全部</SelectItem>
                  <SelectItem value="pending">待采纳</SelectItem>
                  <SelectItem value="accepted">已采纳</SelectItem>
                  <SelectItem value="ignored">已忽略</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
          </div>
        </div>
        {assets.length === 0 ? (
          <EmptyHint text="暂无数据" />
        ) : (
          <div className="max-h-[60vh] overflow-y-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-24">类型</TableHead>
                  <TableHead>值</TableHead>
                  <TableHead className="w-16">端口</TableHead>
                  <TableHead className="w-16">批次</TableHead>
                  <TableHead className="w-20">状态</TableHead>
                  <TableHead className="w-40">时间</TableHead>
                  <TableHead className="w-44">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {assets.map((a) => (
                  <TableRow key={a.id}>
                    <TableCell>
                      <Badge variant="secondary" className="text-xs">
                        {a.type}
                      </Badge>
                    </TableCell>
                    <TableCell className="font-mono text-xs">{a.value}</TableCell>
                    <TableCell className="text-muted-foreground">{a.port > 0 ? a.port : "—"}</TableCell>
                    <TableCell className="text-muted-foreground">{a.batch_id}</TableCell>
                    <TableCell>
                      <Badge
                        variant="outline"
                        className={
                          a.status === "accepted"
                            ? "bg-emerald-500/15 text-emerald-600 border-emerald-500/30"
                            : a.status === "ignored"
                              ? "bg-muted text-muted-foreground"
                              : "bg-amber-500/15 text-amber-600 border-amber-500/30"
                        }
                      >
                        {a.status === "pending" ? "待采纳" : a.status === "accepted" ? "已采纳" : "已忽略"}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-muted-foreground text-xs">{fmtTime(a.created_at)}</TableCell>
                    <TableCell>{a.status === "pending" ? decideButtons(a.id) : null}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

// ---- 总览:任务概况 + 企业目标 + 步骤配置 + 执行/暂存统计 ----

type ReconOverviewProps = {
  taskId: string;
  taskStatus?: string;
  task: Task | null;
};

export function ReconOverviewPanel({ taskId, taskStatus, task }: ReconOverviewProps) {
  const [runs, setRuns] = React.useState<CollectorRun[]>([]);
  const [counts, setCounts] = React.useState<StagedAssetCounts | null>(null);
  const [companies, setCompanies] = React.useState<{ id: number; name: string }[]>([]);

  const load = React.useCallback(() => {
    api
      .collectorRuns(taskId)
      .then(setRuns)
      .catch(() => undefined);
    api
      .stagedAssets(taskId, "all")
      .then((r) => setCounts(r.counts))
      .catch(() => undefined);
    api
      .companies()
      .then(setCompanies)
      .catch(() => undefined);
  }, [taskId]);

  React.useEffect(() => {
    load();
  }, [load]);
  usePolling(taskStatus === "running", 5000, load);

  const done = runs.filter((r) => r.status === "done").length;
  const failed = runs.filter((r) => r.status === "failed").length;
  const companyNames =
    task?.company_ids?.map((id) => companies.find((c) => c.id === id)?.name ?? `#${id}`).join("、") || "—";

  const rows: { label: string; value: React.ReactNode }[] = [
    { label: "任务 ID", value: `#${taskId}` },
    { label: "步骤", value: task?.step_key ?? "—" },
    { label: "目标企业", value: companyNames },
    { label: "状态", value: task?.status ?? "—" },
    { label: "创建时间", value: fmtTime(task?.created_at) },
    { label: "完成时间", value: fmtTime(task?.completed_at) },
    { label: "执行收集器", value: `${done} 成功 / ${failed} 失败 / 共 ${runs.length} 次` },
    {
      label: "暂存资产",
      value: counts ? `待采纳 ${counts.pending} · 已采纳 ${counts.accepted} · 已忽略 ${counts.ignored}` : "—",
    },
  ];

  let stepConfigText = "";
  if (task?.step_config && Object.keys(task.step_config).length > 0) {
    stepConfigText = JSON.stringify(task.step_config, null, 2);
  }

  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Card>
        <CardContent className="pt-4">
          <div className="mb-2 font-medium text-sm">概况</div>
          <dl className="space-y-2">
            {rows.map((r) => (
              <div key={r.label} className="flex items-start justify-between gap-4 text-sm">
                <dt className="text-muted-foreground shrink-0">{r.label}</dt>
                <dd className="min-w-0 break-all text-right">{r.value}</dd>
              </div>
            ))}
          </dl>
        </CardContent>
      </Card>
      <Card>
        <CardContent className="pt-4">
          <div className="mb-2 font-medium text-sm">步骤配置</div>
          {stepConfigText ? (
            <pre className="max-h-64 overflow-auto rounded-md bg-muted/50 p-3 font-mono text-xs">{stepConfigText}</pre>
          ) : (
            <p className="text-muted-foreground text-sm">该步骤无额外配置</p>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

// ---- 发现资产:暂存产出按类型分组浏览(类型筛选 + 关键字搜索) ----

const ASSET_TYPE_LABEL: Record<string, string> = {
  root_domain: "根域名",
  domain: "子域名",
  ip: "IP",
  service: "Web 服务",
  app: "应用",
};

export function ReconAssetsPanel({ taskId, taskStatus }: { taskId: string; taskStatus?: string }) {
  const [assets, setAssets] = React.useState<StagedAsset[]>([]);
  const [typeFilter, setTypeFilter] = React.useState<string>("all");
  const [query, setQuery] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  const load = React.useCallback(() => {
    api
      .stagedAssets(taskId, "all")
      .then((r) => setAssets(r.assets))
      .catch(() => undefined);
  }, [taskId]);

  React.useEffect(() => {
    load();
  }, [load]);
  usePolling(taskStatus === "running", 5000, load);

  const decide = async (ids: number[], action: "adopt" | "ignore") => {
    setBusy(true);
    try {
      const r = await api.decideStagedAssets(taskId, ids, action);
      if (r.errors.length > 0) {
        toast.error(`${r.errors.length} 条失败:${r.errors[0].error}`);
      }
      load();
    } catch (e) {
      toast.error(`操作失败:${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  const filtered = assets.filter((a) => {
    if (typeFilter !== "all" && a.type !== typeFilter) return false;
    if (!query.trim()) return true;
    const q = query.trim().toLowerCase();
    return a.value.toLowerCase().includes(q) || a.collector.toLowerCase().includes(q);
  });

  const byType = new Map<string, number>();
  for (const a of assets) byType.set(a.type, (byType.get(a.type) ?? 0) + 1);

  return (
    <Card>
      <CardContent className="pt-4">
        <div className="mb-3 flex flex-wrap items-center gap-2">
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="搜索资产值 / 来源"
            className="w-56"
          />
          <Select value={typeFilter} onValueChange={setTypeFilter}>
            <SelectTrigger className="w-32">
              <SelectValue placeholder="类型" />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value="all">全部类型</SelectItem>
                {[...byType.entries()].map(([type]) => (
                  <SelectItem key={type} value={type}>
                    {ASSET_TYPE_LABEL[type] ?? type}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
          <span className="text-muted-foreground text-xs tabular-nums">
            {filtered.length}/{assets.length} 条
          </span>
        </div>
        {filtered.length === 0 ? (
          <EmptyHint text="暂无产出" />
        ) : (
          <div className="max-h-[60vh] overflow-y-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-24">类型</TableHead>
                  <TableHead>值</TableHead>
                  <TableHead className="w-16">端口</TableHead>
                  <TableHead className="w-28">来源</TableHead>
                  <TableHead className="w-24">状态</TableHead>
                  <TableHead className="w-40">发现时间</TableHead>
                  <TableHead className="w-44">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {filtered.map((a) => (
                  <TableRow key={a.id}>
                    <TableCell>
                      <Badge variant="secondary" className="text-xs">
                        {ASSET_TYPE_LABEL[a.type] ?? a.type}
                      </Badge>
                    </TableCell>
                    <TableCell className="font-mono text-xs">{a.value}</TableCell>
                    <TableCell className="text-muted-foreground">{a.port > 0 ? a.port : "—"}</TableCell>
                    <TableCell className="text-muted-foreground text-xs">{a.collector}</TableCell>
                    <TableCell>
                      <Badge
                        variant="outline"
                        className={
                          a.status === "accepted"
                            ? "bg-emerald-500/15 text-emerald-600 border-emerald-500/30"
                            : a.status === "ignored"
                              ? "bg-muted text-muted-foreground"
                              : "bg-amber-500/15 text-amber-600 border-amber-500/30"
                        }
                      >
                        {a.status === "pending" ? "待采纳" : a.status === "accepted" ? "已采纳" : "已忽略"}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-muted-foreground text-xs">{fmtTime(a.created_at)}</TableCell>
                    <TableCell>
                      {a.status === "pending" ? (
                        <div className="flex items-center gap-1">
                          <Button
                            variant="ghost"
                            size="sm"
                            disabled={busy}
                            onClick={() => void decide([a.id], "adopt")}
                          >
                            <CheckIcon className="size-4 text-emerald-600" />
                            采纳
                          </Button>
                          <Button
                            variant="ghost"
                            size="sm"
                            disabled={busy}
                            onClick={() => void decide([a.id], "ignore")}
                          >
                            <XIcon className="size-4 text-muted-foreground" />
                            忽略
                          </Button>
                        </div>
                      ) : null}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

// ---- 证据:每次执行的原始输出(blob)预览与下载 ----

const RAW_PREVIEW_BYTES = 100_000;

export function ReconEvidencePanel({ taskId }: { taskId: string }) {
  const [runs, setRuns] = React.useState<CollectorRun[]>([]);
  const [preview, setPreview] = React.useState<{ run: CollectorRun; text: string; truncated: boolean } | null>(null);
  const [loading, setLoading] = React.useState(false);

  const load = React.useCallback(() => {
    api
      .collectorRuns(taskId)
      .then(setRuns)
      .catch(() => undefined);
  }, [taskId]);

  React.useEffect(() => {
    load();
  }, [load]);

  const open = async (run: CollectorRun) => {
    setLoading(true);
    try {
      const text = await api.rawEvidence(taskId, run.id);
      setPreview({ run, text, truncated: text.length >= RAW_PREVIEW_BYTES });
    } catch (e) {
      toast.error(`读取原始输出失败：${(e as Error).message}`);
    } finally {
      setLoading(false);
    }
  };

  const download = (run: CollectorRun) => {
    const url = `/api/tasks/${encodeURIComponent(taskId)}/collector-runs/${run.id}/raw`;
    const token = localStorage.getItem("artex_token");
    fetch(url, { headers: token ? { Authorization: `Bearer ${token}` } : undefined })
      .then((r) => (r.ok ? r.blob() : Promise.reject(new Error(String(r.status)))))
      .then((blob) => {
        const a = document.createElement("a");
        a.href = URL.createObjectURL(blob);
        a.download = `raw-${run.collector}-run${run.id}.txt`;
        a.click();
        URL.revokeObjectURL(a.href);
      })
      .catch((e) => toast.error(`下载失败：${(e as Error).message}`));
  };

  const withRaw = runs.filter((r) => r.raw_evidence_hash);

  return (
    <Card>
      <CardContent className="pt-4">
        {withRaw.length === 0 ? (
          <EmptyHint text="还没有原始输出落盘记录" />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>收集器</TableHead>
                <TableHead className="w-24">状态</TableHead>
                <TableHead className="w-40">时间</TableHead>
                <TableHead className="w-40">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {withRaw.map((run) => (
                <TableRow key={run.id}>
                  <TableCell className="font-medium">{run.collector}</TableCell>
                  <TableCell>
                    <Badge variant="outline" className={runBadgeClass(run.status)}>
                      {run.status === "done" ? "完成" : run.status}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-muted-foreground text-xs">{fmtTime(run.started_at)}</TableCell>
                  <TableCell>
                    <div className="flex items-center gap-1">
                      <Button variant="ghost" size="sm" onClick={() => void open(run)} disabled={loading}>
                        <EyeIcon className="size-4" />
                        预览
                      </Button>
                      <Button variant="ghost" size="sm" onClick={() => download(run)}>
                        <DownloadIcon className="size-4" />
                        下载
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}

        <Dialog open={!!preview} onOpenChange={(o) => !o && setPreview(null)}>
          <DialogContent className="sm:max-w-3xl">
            <DialogHeader>
              <DialogTitle>
                原始输出 · {preview?.run.collector} (run #{preview?.run.id})
              </DialogTitle>
              <DialogDescription>
                <span className="font-mono text-xs">{preview?.run.raw_evidence_hash}</span>
                {preview?.truncated ? " · 预览仅显示前 100KB,完整内容请下载" : ""}
              </DialogDescription>
            </DialogHeader>
            <pre className="max-h-[55vh] overflow-auto rounded-md bg-muted/50 p-3 font-mono text-xs whitespace-pre-wrap">
              {preview?.text}
            </pre>
          </DialogContent>
        </Dialog>
      </CardContent>
    </Card>
  );
}
