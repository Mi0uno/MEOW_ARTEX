"use client";

// 存储设置(契约 STORAGE_CONTRACTS.md):存储桶接入 + 历史数据一键迁移。
// 双页签 ?tab=buckets|migrate(URL 存状态,静态导出安全):
//   桶页签   = 搜索 + 协议/状态筛选器 + 协议分类页签(带计数) + 分页器四件套,
//              行操作:测活/设为默认/取消默认/编辑/删除(默认桶与已有对象 409)。
//   迁移页签 = 类别清单(本地量/已上桶量) + 一键迁移 + 5s 轮询进度卡(取消/续跑)
//              + 本地已上桶文件清理(仅 report-jobs/task-archives)。

import { useCallback, useEffect, useMemo, useState } from "react";
import {
  CheckCircle2,
  CloudUpload,
  HardDrive,
  Pencil,
  PlugZap,
  Plus,
  RefreshCw,
  Search,
  Trash2,
  XCircle,
} from "lucide-react";

import { api } from "@/lib/api";
import type {
  StorageBucket,
  StorageBucketInput,
  StorageCategory,
  StorageCategoryOverview,
  StorageMigration,
} from "@/lib/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { TablePagination } from "@/components/table-pagination";

// ---- 常量 ----

const CATEGORY_LABELS: Record<StorageCategory, string> = {
  "report-jobs": "报告产物",
  "report-templates": "报告模板",
  uploads: "会话附件",
  evidence: "发现证据",
  "task-archives": "任务归档",
  c2: "C2 文件",
  vps: "VPS 输出",
  wordbooks: "侦察词表",
  "src-platform": "SRC 附件",
  "task-workdirs": "任务工作区",
};

const PROTOCOL_LABELS: Record<string, string> = { s3: "S3 兼容", webdav: "WebDAV" };
const STATUS_LABELS: Record<string, string> = { ok: "正常", degraded: "异常", unchecked: "未测活" };

function fmtBytes(n: number): string {
  if (!n) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v >= 100 || i === 0 ? 0 : 1)} ${units[i]}`;
}

function fmtTime(t: number | string | null | undefined): string {
  if (!t) return "—";
  const d = typeof t === "number" ? new Date(t) : new Date(t);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

// ---- 页签胶囊(与 vulndb/cve 页 PillTab 同构) ----

function PillTab({
  active,
  count,
  children,
  onClick,
}: {
  active: boolean;
  count?: number;
  children: React.ReactNode;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={`inline-flex items-center gap-1.5 rounded-full border px-3 py-1 text-sm transition-colors ${
        active
          ? "border-primary bg-primary text-primary-foreground"
          : "border-border bg-background text-muted-foreground hover:text-foreground"
      }`}
    >
      {children}
      {count !== undefined && <span className="tabular-nums opacity-80">{count}</span>}
    </button>
  );
}

function StatusBadge({ status }: { status: string }) {
  const tone =
    status === "ok" ? "default" : status === "degraded" ? "destructive" : "outline";
  return <Badge variant={tone as "default" | "destructive" | "outline"}>{STATUS_LABELS[status] ?? status}</Badge>;
}

// ---- 页面 ----

export default function StoragePage() {
  const [tab, setTab] = useState<"buckets" | "migrate">(() => {
    if (typeof window !== "undefined") {
      const t = new URLSearchParams(window.location.search).get("tab");
      if (t === "migrate") return "migrate";
    }
    return "buckets";
  });
  const switchTab = (t: "buckets" | "migrate") => {
    setTab(t);
    if (typeof window !== "undefined") {
      const u = new URL(window.location.href);
      u.searchParams.set("tab", t);
      window.history.replaceState(null, "", u.toString());
    }
  };

  return (
    <div className="space-y-4">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div>
          <h1 className="text-xl font-semibold">存储设置</h1>
          <p className="text-muted-foreground text-sm">
            接入对象存储桶(S3 兼容 / WebDAV),历史数据一键迁移;迁移后桶为真源,本地自动回退。
          </p>
        </div>
        <div className="flex gap-2">
          <PillTab active={tab === "buckets"} onClick={() => switchTab("buckets")}>
            <HardDrive className="h-4 w-4" /> 存储桶
          </PillTab>
          <PillTab active={tab === "migrate"} onClick={() => switchTab("migrate")}>
            <CloudUpload className="h-4 w-4" /> 数据迁移
          </PillTab>
        </div>
      </div>
      {tab === "buckets" ? <BucketsTab /> : <MigrateTab />}
    </div>
  );
}

// ---- 存储桶页签 ----

function BucketsTab() {
  const [items, setItems] = useState<StorageBucket[]>([]);
  const [total, setTotal] = useState(0);
  const [counts, setCounts] = useState<Record<string, number>>({});
  const [statusCounts, setStatusCounts] = useState<Record<string, number>>({});
  const [protocol, setProtocol] = useState("");
  const [status, setStatus] = useState("");
  const [q, setQ] = useState("");
  const [appliedQ, setAppliedQ] = useState("");
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [editBucket, setEditBucket] = useState<StorageBucket | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [busyId, setBusyId] = useState(0);
  const [err, setErr] = useState("");

  const load = useCallback(async () => {
    try {
      const r = await api.storageBuckets({
        protocol: protocol || undefined,
        status: status || undefined,
        q: appliedQ || undefined,
        limit: pageSize,
        offset: (page - 1) * pageSize,
      });
      setItems(r.items);
      setTotal(r.total);
      setCounts(r.counts);
      setStatusCounts(r.status_counts);
      setErr("");
    } catch (e) {
      setErr(String(e));
    }
  }, [protocol, status, appliedQ, page, pageSize]);

  useEffect(() => {
    load();
  }, [load]);

  const action = async (id: number, fn: () => Promise<unknown>) => {
    setBusyId(id);
    try {
      await fn();
      await load();
    } catch (e) {
      setErr(String(e));
    } finally {
      setBusyId(0);
    }
  };

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <div className="flex items-center gap-1">
          <PillTab active={protocol === ""} count={Object.values(counts).reduce((a, b) => a + b, 0)} onClick={() => { setProtocol(""); setPage(1); }}>
            全部
          </PillTab>
          {Object.keys(PROTOCOL_LABELS).map((p) => (
            <PillTab key={p} active={protocol === p} count={counts[p]} onClick={() => { setProtocol(p); setPage(1); }}>
              {PROTOCOL_LABELS[p]}
            </PillTab>
          ))}
        </div>
        <Select value={status || "__all__"} onValueChange={(v) => { setStatus(v === "__all__" ? "" : v); setPage(1); }}>
          <SelectTrigger className="w-32"><SelectValue placeholder="状态" /></SelectTrigger>
          <SelectContent>
            <SelectItem value="__all__">全部状态</SelectItem>
            {Object.entries(STATUS_LABELS).map(([k, v]) => (
              <SelectItem key={k} value={k}>{v}({statusCounts[k] ?? 0})</SelectItem>
            ))}
          </SelectContent>
        </Select>
        <div className="ml-auto flex items-center gap-2">
          <Input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => { if (e.key === "Enter") { setAppliedQ(q); setPage(1); } }}
            placeholder="名称 / 端点 / 桶名"
            className="w-56"
          />
          <Button variant="outline" size="sm" onClick={() => { setAppliedQ(q); setPage(1); }}>
            <Search className="h-4 w-4" />
          </Button>
          <Button size="sm" onClick={() => setCreateOpen(true)}>
            <Plus className="h-4 w-4" /> 接入存储桶
          </Button>
        </div>
      </div>

      {err && <p className="text-destructive text-sm">{err}</p>}

      <div className="rounded-lg border">
        <Table className="table-fixed">
          <TableHeader>
            <TableRow>
              <TableHead className="w-36">名称</TableHead>
              <TableHead className="w-24">协议</TableHead>
              <TableHead>端点 / 桶</TableHead>
              <TableHead className="w-20">默认</TableHead>
              <TableHead className="w-24">状态</TableHead>
              <TableHead className="w-44">最近测活</TableHead>
              <TableHead className="w-44">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((b) => (
              <TableRow key={b.id}>
                <TableCell className="truncate" title={b.name}>{b.name}</TableCell>
                <TableCell>{PROTOCOL_LABELS[b.protocol] ?? b.protocol}</TableCell>
                <TableCell className="truncate" title={`${b.endpoint}/${b.bucket}${b.path_prefix ? ` (${b.path_prefix})` : ""}`}>
                  {b.endpoint}/{b.bucket}
                </TableCell>
                <TableCell>
                  {b.active ? <Badge>默认</Badge> : <span className="text-muted-foreground text-sm">—</span>}
                </TableCell>
                <TableCell><StatusBadge status={b.status} /></TableCell>
                <TableCell className="text-muted-foreground text-xs" title={b.last_check_error}>
                  {fmtTime(b.last_check_at)}
                </TableCell>
                <TableCell>
                  <div className="flex items-center gap-1">
                    <Button variant="outline" size="sm" disabled={busyId === b.id}
                      onClick={() => action(b.id, () => api.storageBucketCheck(b.id))}>
                      <RefreshCw className="h-4 w-4" /> 测活
                    </Button>
                    {b.active ? (
                      <Button variant="outline" size="sm" disabled={busyId === b.id}
                        onClick={() => action(b.id, () => api.storageDeactivate())}>
                        取消默认
                      </Button>
                    ) : (
                      <Button variant="outline" size="sm" disabled={busyId === b.id}
                        onClick={() => action(b.id, () => api.storageBucketActivate(b.id))}>
                        <PlugZap className="h-4 w-4" /> 设为默认
                      </Button>
                    )}
                    <Button variant="ghost" size="icon" title="编辑" onClick={() => setEditBucket(b)}>
                      <Pencil className="h-4 w-4" />
                    </Button>
                    <Button variant="ghost" size="icon" title="删除"
                      onClick={() => { if (window.confirm(`删除存储桶「${b.name}」?`)) action(b.id, () => api.storageBucketDelete(b.id)); }}>
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
            {items.length === 0 && (
              <TableRow>
                <TableCell colSpan={7} className="text-muted-foreground py-10 text-center">
                  还没有接入存储桶——点击右上角「接入存储桶」开始
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>
      <TablePagination
        page={page}
        pageSize={pageSize}
        total={total}
        onPageChange={setPage}
        onPageSizeChange={(n) => { setPageSize(n); setPage(1); }}
      />

      <BucketDialog open={createOpen} onOpenChange={setCreateOpen} onSaved={load} />
      {editBucket && (
        <BucketDialog open onOpenChange={() => setEditBucket(null)} bucket={editBucket} onSaved={load} />
      )}
    </div>
  );
}

// ---- 建/改桶对话框 ----

function BucketDialog({
  open,
  onOpenChange,
  bucket,
  onSaved,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  bucket?: StorageBucket | null;
  onSaved: () => void;
}) {
  const editing = !!bucket;
  const [name, setName] = useState(bucket?.name ?? "");
  const [protocol, setProtocol] = useState(bucket?.protocol ?? "s3");
  const [endpoint, setEndpoint] = useState(bucket?.endpoint ?? "");
  const [bucketName, setBucketName] = useState(bucket?.bucket ?? "");
  const [region, setRegion] = useState(bucket?.region || "us-east-1");
  const [prefix, setPrefix] = useState(bucket?.path_prefix ?? "");
  const [accessKey, setAccessKey] = useState(""); // 密钥永不回显,编辑态留空 = 不改
  const [secretKey, setSecretKey] = useState("");
  const [activate, setActivate] = useState(!editing);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");

  const input: StorageBucketInput = {
    name, protocol, endpoint, bucket: bucketName,
    region: region || undefined, path_prefix: prefix || undefined,
    access_key: accessKey || undefined,
    secret_key: secretKey || undefined,
    activate,
  };

  const submit = async () => {
    setBusy(true);
    setMsg("");
    try {
      if (editing && bucket) {
        await api.storageBucketUpdate(bucket.id, input);
        setMsg("已保存");
      } else {
        const r = await api.storageBucketCreate(input);
        setMsg(r && "warning" in (r as Record<string, unknown>) ? String((r as Record<string, unknown>).warning) : "已接入");
      }
      onSaved();
    } catch (e) {
      setMsg(String(e));
    } finally {
      setBusy(false);
    }
  };

  const testConn = async () => {
    setBusy(true);
    setMsg("测试中…");
    try {
      if (editing && bucket) {
        // 编辑态:先存再测(测活按已保存配置)
        await api.storageBucketUpdate(bucket.id, input);
        const r = await api.storageBucketCheck(bucket.id);
        setMsg(r.ok ? `连接成功(${r.latency_ms}ms)` : `连接失败: ${r.error}`);
      } else {
        const r = await api.storageBucketCreate({ ...input, activate: false });
        const c = await api.storageBucketCheck(r.id);
        setMsg(c.ok ? `连接成功(${c.latency_ms}ms),已保存` : `连接失败: ${c.error}(配置已保存,可修改后重试)`);
      }
      onSaved();
    } catch (e) {
      setMsg(String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg space-y-3">
        <DialogHeader>
          <DialogTitle>{editing ? "编辑存储桶" : "接入存储桶"}</DialogTitle>
        </DialogHeader>
        <div className="grid grid-cols-2 gap-3">
          <div className="space-y-1">
            <label className="text-sm">名称</label>
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="MinIO 内网" />
          </div>
          <div className="space-y-1">
            <label className="text-sm">协议</label>
            <Select value={protocol} onValueChange={(v) => setProtocol(v as StorageBucketInput["protocol"])}>
              <SelectTrigger><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="s3">S3 兼容(MinIO/OSS/COS/R2)</SelectItem>
                <SelectItem value="webdav">WebDAV</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="col-span-2 space-y-1">
            <label className="text-sm">端点</label>
            <Input value={endpoint} onChange={(e) => setEndpoint(e.target.value)}
              placeholder={protocol === "s3" ? "https://minio.example:9000" : "https://dav.example/dav"} />
          </div>
          <div className="space-y-1">
            <label className="text-sm">{protocol === "s3" ? "桶名" : "远端目录"}</label>
            <Input value={bucketName} onChange={(e) => setBucketName(e.target.value)} placeholder="artex" />
          </div>
          {protocol === "s3" && (
            <div className="space-y-1">
              <label className="text-sm">Region</label>
              <Input value={region} onChange={(e) => setRegion(e.target.value)} placeholder="us-east-1" />
            </div>
          )}
          <div className={protocol === "s3" ? "space-y-1" : "col-span-2 space-y-1"}>
            <label className="text-sm">对象前缀(可选)</label>
            <Input value={prefix} onChange={(e) => setPrefix(e.target.value)} placeholder="artex/" />
          </div>
          <div className="space-y-1">
            <label className="text-sm">{protocol === "s3" ? "Access Key" : "用户名(可选)"}</label>
            <Input value={accessKey} onChange={(e) => setAccessKey(e.target.value)} />
          </div>
          <div className="space-y-1">
            <label className="text-sm">{protocol === "s3" ? "Secret Key" : "密码(可选)"}</label>
            <Input type="password" value={secretKey} onChange={(e) => setSecretKey(e.target.value)}
              placeholder={editing && bucket?.has_secret ? "留空 = 不修改" : ""} />
          </div>
          <label className="col-span-2 flex items-center gap-2 text-sm">
            <input type="checkbox" checked={activate} onChange={(e) => setActivate(e.target.checked)} />
            保存后测活并设为默认桶
          </label>
        </div>
        {msg && <p className="text-muted-foreground text-sm">{msg}</p>}
        <DialogFooter>
          <Button variant="outline" onClick={testConn} disabled={busy || !endpoint || !bucketName}>
            测试连接
          </Button>
          <Button onClick={submit} disabled={busy || !name || !endpoint || !bucketName}>
            保存
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---- 迁移页签 ----

function MigrateTab() {
  const [categories, setCategories] = useState<StorageCategoryOverview[]>([]);
  const [activeBucketId, setActiveBucketId] = useState(0);
  const [mig, setMig] = useState<StorageMigration | null>(null);
  const [last, setLast] = useState<StorageMigration | null>(null);
  const [running, setRunning] = useState(false);
  const [selected, setSelected] = useState<Record<string, boolean>>({});
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [hasActive, setHasActive] = useState(false);

  const loadStatus = useCallback(async () => {
    try {
      const r = await api.storageMigrateStatus();
      setMig(r.migration);
      setLast(r.last);
      setRunning(r.running);
    } catch (e) {
      setErr(String(e));
    }
  }, []);

  const loadOverview = useCallback(async () => {
    try {
      const r = await api.storageOverview();
      setCategories(r.categories);
      setActiveBucketId(r.active_bucket_id);
      setHasActive(r.active_bucket_id > 0);
    } catch (e) {
      setErr(String(e));
    }
  }, []);

  useEffect(() => {
    loadOverview();
    loadStatus();
  }, [loadOverview, loadStatus]);

  // 迁移进行中 5s 轮询 + 结束时刷新总览
  useEffect(() => {
    if (!running) return;
    const t = setInterval(() => {
      loadStatus();
      loadOverview();
    }, 5000);
    return () => clearInterval(t);
  }, [running, loadStatus, loadOverview]);

  const allSelected = useMemo(
    () => categories.length > 0 && categories.every((c) => selected[c.category] !== false),
    [categories, selected],
  );

  const start = async () => {
    setBusy(true);
    setErr("");
    try {
      const cats = categories.filter((c) => selected[c.category] !== false).map((c) => c.category);
      await api.storageMigrate(cats);
      await loadStatus();
    } catch (e) {
      setErr(String(e));
    } finally {
      setBusy(false);
    }
  };

  const cancel = async () => {
    setBusy(true);
    try {
      await api.storageMigrateCancel();
      await loadStatus();
    } catch (e) {
      setErr(String(e));
    } finally {
      setBusy(false);
    }
  };

  const purge = async (cats: string[]) => {
    if (!window.confirm(`删除本地已上桶文件?类别: ${cats.join(", ")}(桶上副本不受影响)`)) return;
    setBusy(true);
    try {
      const r = await api.storagePurgeLocal(cats);
      window.alert(`已清理本地文件 ${r.purged} 个,跳过 ${r.skipped} 个`);
      await loadOverview();
    } catch (e) {
      setErr(String(e));
    } finally {
      setBusy(false);
    }
  };

  const pct = mig && mig.total_files > 0 ? Math.round(((mig.done_files + mig.failed_files) / mig.total_files) * 100) : 0;
  void pct; // 预留:顶部汇总条(明细卡内已按作业行计算)

  return (
    <div className="space-y-4">
      {!hasActive && (
        <div className="rounded-lg border border-amber-500/40 bg-amber-500/10 px-4 py-3 text-sm">
          尚未启用默认存储桶——先在「存储桶」页签接入并设为默认,才能发起迁移。
        </div>
      )}

      {/* 进行中 / 最近一次迁移进度卡 */}
      {(running || mig || last) && (
        <div className="rounded-lg border p-4">
          <div className="mb-2 flex items-center gap-2">
            <Badge variant={running ? "default" : (last?.status === "failed" ? "destructive" : "outline")}>
              {running ? "迁移中" : last?.status === "canceled" ? "已取消" : last?.status === "failed" ? "失败" : "已完成"}
            </Badge>
            <span className="text-muted-foreground text-sm">
              #{(running ? mig : last)?.id} · 目标桶 {(running ? mig : last)?.bucket_id} · 起于 {fmtTime((running ? mig : last)?.started_at)}
            </span>
            {running && (
              <Button variant="outline" size="sm" className="ml-auto" onClick={cancel} disabled={busy}>
                取消(保留断点)
              </Button>
            )}
            {!running && last && (last.failed_files > 0 || last.status === "canceled") && (
              <Button variant="outline" size="sm" className="ml-auto" onClick={start} disabled={busy}>
                从断点续跑
              </Button>
            )}
          </div>
          {(() => {
            const m = running ? mig : last;
            if (!m) return null;
            const pct = m.total_files > 0 ? Math.round(((m.done_files + m.failed_files) / m.total_files) * 100) : 0;
            return (
              <div className="space-y-2">
                <div className="h-2 w-full overflow-hidden rounded-full bg-muted">
                  <div className="h-full bg-primary transition-all" style={{ width: `${pct}%` }} />
                </div>
                <p className="text-muted-foreground text-sm">
                  {m.done_files}/{m.total_files} 已上桶{m.failed_files > 0 ? `,${m.failed_files} 失败` : ""}
                  {m.error ? ` · ${m.error}` : ""}
                </p>
                <p className="text-muted-foreground text-xs">
                  类别:{m.categories.map((c) => CATEGORY_LABELS[c as StorageCategory] ?? c).join("、")}
                </p>
              </div>
            );
          })()}
        </div>
      )}

      {/* 类别清单 */}
      <div className="rounded-lg border">
        <div className="flex items-center justify-between border-b px-4 py-2">
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={allSelected}
              onChange={(e) => {
                const next: Record<string, boolean> = {};
                for (const c of categories) next[c.category] = e.target.checked;
                setSelected(next);
              }}
            />
            全选
          </label>
          <Button size="sm" onClick={start} disabled={busy || running || !hasActive}>
            <CloudUpload className="h-4 w-4" /> 一键迁移
          </Button>
        </div>
        <Table className="table-fixed">
          <TableHeader>
            <TableRow>
              <TableHead className="w-10" />
              <TableHead className="w-40">数据类别</TableHead>
              <TableHead className="w-32">本地文件数</TableHead>
              <TableHead className="w-32">本地大小</TableHead>
              <TableHead className="w-32">已上桶</TableHead>
              <TableHead>操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {categories.map((c) => (
              <TableRow key={c.category}>
                <TableCell>
                  <input
                    type="checkbox"
                    checked={selected[c.category] !== false}
                    onChange={(e) => setSelected({ ...selected, [c.category]: e.target.checked })}
                    disabled={running}
                  />
                </TableCell>
                <TableCell>{CATEGORY_LABELS[c.category] ?? c.category}</TableCell>
                <TableCell className="tabular-nums">{c.files.toLocaleString()}</TableCell>
                <TableCell className="tabular-nums">{fmtBytes(c.bytes)}</TableCell>
                <TableCell className="tabular-nums">{c.uploaded_files.toLocaleString()}</TableCell>
                <TableCell>
                  {(c.category === "report-jobs" || c.category === "task-archives") && c.uploaded_files > 0 && (
                    <Button variant="outline" size="sm" disabled={busy || running}
                      onClick={() => purge([c.category])}>
                      <XCircle className="h-4 w-4" /> 清理本地已上桶({c.uploaded_files})
                    </Button>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>

      {err && <p className="text-destructive text-sm">{err}</p>}
      <p className="text-muted-foreground text-xs">
        迁移逐文件上传并登记,支持取消后从断点续跑;traffic 流量体不纳管。迁移完成的类别可在上表清理本地副本
        (桶为真源,读取自动回源)。当前默认桶 ID:{activeBucketId || "无"}。
      </p>
      <div className="flex items-center gap-2 text-muted-foreground text-xs">
        <CheckCircle2 className="h-3 w-3" /> 迁移幂等:重复发起只会补齐缺失文件。
      </div>
    </div>
  );
}
