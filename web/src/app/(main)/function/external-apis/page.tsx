"use client";

// 外部接口管理:FOFA/Hunter 等空间测绘 API 的配置、测试连接、启停。
// 凭据 AES-GCM 加密落库,页面上只显示掩码;测试连接 = 发一条最小查询验证。

import * as React from "react";

import {
  EyeIcon,
  PencilIcon,
  PlugZapIcon,
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
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { Spinner } from "@/components/ui/spinner";
import { TablePagination } from "@/components/table-pagination";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { api } from "@/lib/api";
import type { ExternalAPIConfig, ExternalAPIProviderDesc } from "@/lib/types";

function fmtTime(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString();
}

export default function ExternalApisPage() {
  const [apis, setApis] = React.useState<ExternalAPIConfig[]>([]);
  const [providers, setProviders] = React.useState<ExternalAPIProviderDesc[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [editing, setEditing] = React.useState<ExternalAPIConfig | null>(null);
  const [creating, setCreating] = React.useState(false);
  const [testing, setTesting] = React.useState<number | null>(null);
  // 列表页标准能力:搜索 / 状态筛选 / 分页(接口数量有限,客户端处理即可)。
  const [query, setQuery] = React.useState("");
  const [statusFilter, setStatusFilter] = React.useState("all");
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(20);
  // 行多选 + 批量删除/启停:范围是当前页(paged);实体量小,批量直接循环单条接口。
  const [checkedIds, setCheckedIds] = React.useState<Set<number>>(new Set());
  const [batchDeleteOpen, setBatchDeleteOpen] = React.useState(false);
  const [busyBatch, setBusyBatch] = React.useState(false);

  const filtered = React.useMemo(() => {
    const q = query.trim().toLowerCase();
    return apis.filter((a) => {
      if (statusFilter === "enabled" && !a.enabled) return false;
      if (statusFilter === "disabled" && a.enabled) return false;
      if (!q) return true;
      return (
        a.name.toLowerCase().includes(q) ||
        (a.provider ?? "").toLowerCase().includes(q) ||
        (a.provider_title ?? "").toLowerCase().includes(q)
      );
    });
  }, [apis, query, statusFilter]);

  const paged = React.useMemo(
    () => filtered.slice((page - 1) * pageSize, page * pageSize),
    [filtered, page, pageSize],
  );

  // biome-ignore lint/correctness/useExhaustiveDependencies: filters intentionally reset pagination.
  React.useEffect(() => {
    setPage(1);
  }, [query, statusFilter]);

  const load = React.useCallback(() => {
    api
      .externalApis()
      .then((r) => {
        setApis(r.apis);
        setProviders(r.providers);
      })
      .catch((e) => toast.error(`读取外部接口失败：${e.message}`))
      .finally(() => setLoading(false));
  }, []);

  React.useEffect(() => {
    load();
  }, [load]);

  const test = (id: number) => {
    setTesting(id);
    api
      .testExternalApi(id)
      .then((r) => {
        if (r.ok) {
          const quota = r.quota_info ? ` · ${JSON.stringify(r.quota_info)}` : "";
          toast.success(`连接成功${quota}`);
        } else {
          toast.error(`连接失败：${r.message}`);
        }
        load();
      })
      .catch((e) => toast.error(`测试失败：${e.message}`))
      .finally(() => setTesting(null));
  };

  const toggleEnabled = (a: ExternalAPIConfig) => {
    api
      .updateExternalApi(a.id, { enabled: !a.enabled })
      .then(() => load())
      .catch((e) => toast.error(`操作失败：${e.message}`));
  };

  const remove = (id: number) => {
    api
      .deleteExternalApi(id)
      .then((r) => {
        toast.success(r.deleted ? "已删除" : "配置不存在");
        load();
      })
      .catch((e) => toast.error(`删除失败：${e.message}`));
  };

  // 当前页行(客户端分页,paged 即本页)驱动三态全选。
  const pageIds = React.useMemo(() => paged.map((a) => a.id), [paged]);
  const allChecked = pageIds.length > 0 && pageIds.every((id) => checkedIds.has(id));
  const someChecked = pageIds.some((id) => checkedIds.has(id));

  // 行首勾选:v 是显式 checked 语义(true 勾上 / false 取消)。
  const toggleRow = (id: number, checked: boolean) => {
    setCheckedIds((prev) => {
      const next = new Set(prev);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  };

  // 表头全选:indeterminate 状态点击上报 true → 全选当前页;已全选点击上报 false → 清当前页。
  const toggleAllPage = (checked: boolean) => {
    setCheckedIds((prev) => {
      const next = new Set(prev);
      for (const id of pageIds) {
        if (checked) next.add(id);
        else next.delete(id);
      }
      return next;
    });
  };

  // 批量启停:循环 updateExternalApi,allSettled 保证一条失败不拖垮其余;完成后保留选择便于连续操作。
  const setBatchEnabled = async (enabled: boolean) => {
    const ids = Array.from(checkedIds);
    setBusyBatch(true);
    try {
      const results = await Promise.allSettled(ids.map((id) => api.updateExternalApi(id, { enabled })));
      const ok = results.filter((r) => r.status === "fulfilled").length;
      const failed = results.length - ok;
      if (failed === 0) toast.success(enabled ? `已启用 ${ok} 项` : `已停用 ${ok} 项`);
      else toast.warning(`${enabled ? "启用" : "停用"}：成功 ${ok} 项，失败 ${failed} 项`);
      load();
    } finally {
      setBusyBatch(false);
    }
  };

  // 批量删除:同款循环;全成功/有失败分别 toast,然后清空选择并刷新。
  const confirmBatchDelete = async () => {
    const ids = Array.from(checkedIds);
    setBusyBatch(true);
    try {
      const results = await Promise.allSettled(ids.map((id) => api.deleteExternalApi(id)));
      const ok = results.filter((r) => r.status === "fulfilled").length;
      const failed = results.length - ok;
      if (failed === 0) toast.success(`已删除 ${ok} 项`);
      else toast.warning(`成功 ${ok} 项，失败 ${failed} 项`);
      setCheckedIds(new Set());
      load();
    } finally {
      setBusyBatch(false);
      setBatchDeleteOpen(false);
    }
  };

  return (
    <div className="p-4 md:p-6">
      <div className="mb-2 flex items-center gap-2">
        <SidebarTrigger className="-ml-1" />
      </div>
      <div className="mb-4 flex items-start justify-between gap-4">
        <div>
          <h1 className="font-semibold text-xl">外部接口</h1>
          <p className="text-muted-foreground text-sm">
            空间测绘 API 配置(FOFA/Hunter);凭据加密落库,页面上只显示掩码。信息搜集步骤任务运行时自动解密注入
          </p>
        </div>
        <div className="flex items-center gap-2">
          {checkedIds.size > 0 && (
            <>
              <Button variant="outline" size="sm" disabled={busyBatch} onClick={() => void setBatchEnabled(true)}>
                批量启用
              </Button>
              <Button variant="outline" size="sm" disabled={busyBatch} onClick={() => void setBatchEnabled(false)}>
                批量停用
              </Button>
              <Button
                variant="destructive"
                size="sm"
                disabled={busyBatch}
                onClick={() => setBatchDeleteOpen(true)}
              >
                <Trash2Icon className="size-4" />
                删除已选 (<span className="tabular-nums">{checkedIds.size}</span>)
              </Button>
            </>
          )}
          <Button variant="outline" size="sm" onClick={() => void load()}>
            <RefreshCwIcon className="size-4" />
            刷新
          </Button>
          <Button size="sm" onClick={() => setCreating(true)}>
            <PlusIcon className="size-4" />
            新增接口
          </Button>
        </div>
      </div>

      <Card>
        <CardContent className="px-0 pt-4">
          <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
            <div className="relative w-full sm:max-w-xs">
              <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
              <Input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder="搜索接口名称 / Provider"
                className="pl-8"
              />
            </div>
            <Select value={statusFilter} onValueChange={setStatusFilter}>
              <SelectTrigger className="w-32">
                <SelectValue placeholder="状态" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="all">全部状态</SelectItem>
                  <SelectItem value="enabled">已启用</SelectItem>
                  <SelectItem value="disabled">已停用</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
            <span className="text-muted-foreground ml-auto text-xs tabular-nums">
              {filtered.length}/{apis.length} 个
            </span>
          </div>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-8">
                  <Checkbox
                    checked={allChecked ? true : someChecked ? "indeterminate" : false}
                    onCheckedChange={(v) => toggleAllPage(v === true)}
                    aria-label="全选当前页接口"
                  />
                </TableHead>
                <TableHead className="w-32">Provider</TableHead>
                <TableHead>名称</TableHead>
                <TableHead>凭据(掩码)</TableHead>
                <TableHead className="w-24">状态</TableHead>
                <TableHead className="w-44">上次测试</TableHead>
                <TableHead className="w-40">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {loading ? (
                <TableRow>
                  <TableCell colSpan={7} className="text-muted-foreground py-8 text-center">
                    加载中…
                  </TableCell>
                </TableRow>
              ) : filtered.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={7} className="text-muted-foreground py-8 text-center">
                    {apis.length === 0 ? "还没有配置,点右上角「新增接口」接入 FOFA / Hunter" : "没有匹配的接口"}
                  </TableCell>
                </TableRow>
              ) : (
                paged.map((a) => (
                  <TableRow key={a.id} className={checkedIds.has(a.id) ? "bg-muted/40" : undefined}>
                    <TableCell className="w-8" onClick={(e) => e.stopPropagation()}>
                      <Checkbox
                        checked={checkedIds.has(a.id)}
                        onCheckedChange={(v) => toggleRow(a.id, v === true)}
                        aria-label={`选择接口 ${a.name}`}
                      />
                    </TableCell>
                    <TableCell>
                      <Badge variant="secondary" className="text-xs">
                        {a.provider_title || a.provider}
                      </Badge>
                    </TableCell>
                    <TableCell className="font-medium">{a.name}</TableCell>
                    <TableCell>
                      <div className="flex flex-wrap gap-2">
                        {Object.entries(a.masked ?? {}).map(([k, v]) => (
                          <span key={k} className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">
                            {k}={v}
                          </span>
                        ))}
                      </div>
                    </TableCell>
                    <TableCell>
                      <Badge
                        variant="outline"
                        className={
                          a.enabled
                            ? "bg-emerald-500/15 text-emerald-600 border-emerald-500/30"
                            : "bg-muted text-muted-foreground"
                        }
                      >
                        {a.enabled ? "启用" : "停用"}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-muted-foreground text-xs">
                      {fmtTime(a.last_test_at)}
                      {a.last_test_ok != null ? (a.last_test_ok ? " · 成功" : " · 失败") : ""}
                    </TableCell>
                    <TableCell>
                      <div className="flex items-center gap-1">
                        <Tooltip>
                          <TooltipTrigger asChild>
                            <Button
                              variant="ghost"
                              size="icon"
                              className="size-7"
                              onClick={() => test(a.id)}
                              disabled={testing === a.id}
                              aria-label="测试连接"
                            >
                              {testing === a.id ? <Spinner className="size-4" /> : <PlugZapIcon className="size-4" />}
                            </Button>
                          </TooltipTrigger>
                          <TooltipContent>测试连接</TooltipContent>
                        </Tooltip>
                        <Tooltip>
                          <TooltipTrigger asChild>
                            <Button variant="ghost" size="icon" className="size-7" onClick={() => setEditing(a)} aria-label="编辑">
                              <PencilIcon className="size-4" />
                            </Button>
                          </TooltipTrigger>
                          <TooltipContent>编辑</TooltipContent>
                        </Tooltip>
                        <Tooltip>
                          <TooltipTrigger asChild>
                            <Button variant="ghost" size="icon" className="size-7" onClick={() => toggleEnabled(a)} aria-label={a.enabled ? "停用" : "启用"}>
                              <EyeIcon className="size-4" />
                            </Button>
                          </TooltipTrigger>
                          <TooltipContent>{a.enabled ? "停用" : "启用"}</TooltipContent>
                        </Tooltip>
                        <AlertDialog>
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <AlertDialogTrigger asChild>
                                <Button variant="ghost" size="icon" className="text-red-600 size-7 hover:text-red-700" aria-label="删除">
                                  <Trash2Icon className="size-4" />
                                </Button>
                              </AlertDialogTrigger>
                            </TooltipTrigger>
                            <TooltipContent>删除</TooltipContent>
                          </Tooltip>
                          <AlertDialogContent>
                            <AlertDialogHeader>
                              <AlertDialogTitle>删除外部接口 «{a.name}»?</AlertDialogTitle>
                              <AlertDialogDescription>
                                引用此接口的步骤任务重跑时会报"接口已删除",需要重新选择;此操作不可撤销。
                              </AlertDialogDescription>
                            </AlertDialogHeader>
                            <AlertDialogFooter>
                              <AlertDialogCancel>取消</AlertDialogCancel>
                              <AlertDialogAction onClick={() => remove(a.id)}>删除</AlertDialogAction>
                            </AlertDialogFooter>
                          </AlertDialogContent>
                        </AlertDialog>
                      </div>
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
          {filtered.length > 0 && (
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
          )}
        </CardContent>
      </Card>

      {/* 批量删除确认:兄弟节点渲染,不进三元链。 */}
      <AlertDialog open={batchDeleteOpen} onOpenChange={setBatchDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>确认批量删除外部接口</AlertDialogTitle>
            <AlertDialogDescription>
              将永久删除选中的 <span className="font-semibold tabular-nums">{checkedIds.size}</span>{" "}
              个接口配置，引用它们的步骤任务重跑时会报“接口已删除”，需要重新选择；此操作不可撤销。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busyBatch}>取消</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault();
                void confirmBatchDelete();
              }}
              disabled={busyBatch}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {busyBatch ? "删除中…" : "确认删除"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {creating || editing ? (
        <ExternalApiDialog
          providers={providers}
          existing={editing}
          onDone={() => {
            setCreating(false);
            setEditing(null);
            load();
          }}
          onCancel={() => {
            setCreating(false);
            setEditing(null);
          }}
        />
      ) : null}
    </div>
  );
}

// ---- 新增/编辑对话框 ----

function ExternalApiDialog({
  providers,
  existing,
  onDone,
  onCancel,
}: {
  providers: ExternalAPIProviderDesc[];
  existing: ExternalAPIConfig | null;
  onDone: () => void;
  onCancel: () => void;
}) {
  const initialProvider = existing?.provider ?? providers[0]?.key ?? "";
  const [provider, setProvider] = React.useState(initialProvider);
  const [name, setName] = React.useState(existing?.name ?? "");
  const [baseUrl, setBaseUrl] = React.useState(existing?.base_url ?? "");
  const [creds, setCreds] = React.useState<Record<string, string>>({});
  const [saving, setSaving] = React.useState(false);
  const [testing, setTesting] = React.useState(false);
  const [testResult, setTestResult] = React.useState<{ ok: boolean; message: string } | null>(null);

  const def = providers.find((p) => p.key === provider);

  const submit = async () => {
    if (!def) return;
    setSaving(true);
    try {
      if (existing) {
        await api.updateExternalApi(existing.id, {
          name: name.trim() || undefined,
          baseUrl: baseUrl.trim() || undefined,
          credentials: Object.keys(creds).length > 0 ? creds : undefined,
        });
        toast.success("已保存");
      } else {
        await api.createExternalApi({
          provider,
          name: name.trim() || undefined,
          baseUrl: baseUrl.trim() || undefined,
          credentials: creds,
        });
        toast.success("接口已新增");
      }
      onDone();
    } catch (e) {
      toast.error(`保存失败：${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  };

  const testNow = async () => {
    if (!def) return;
    setTesting(true);
    setTestResult(null);
    try {
      let targetId = existing?.id;
      if (!targetId) {
        // 未保存的新配置:先落库再测试。
        const created = await api.createExternalApi({
          provider,
          name: name.trim() || undefined,
          baseUrl: baseUrl.trim() || undefined,
          credentials: creds,
        });
        targetId = created.id;
      } else if (Object.keys(creds).length > 0) {
        // 已编辑过凭据:先保存再测。
        await api.updateExternalApi(targetId, { credentials: creds });
      }
      const r = await api.testExternalApi(targetId);
      setTestResult({ ok: r.ok, message: r.ok ? `连接成功${r.quota_info ? ` · ${JSON.stringify(r.quota_info)}` : ""}` : r.message });
      if (r.ok) onDone();
    } catch (e) {
      setTestResult({ ok: false, message: (e as Error).message });
    } finally {
      setTesting(false);
    }
  };

  return (
    <Dialog open onOpenChange={(o) => !o && onCancel()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{existing ? "编辑外部接口" : "新增外部接口"}</DialogTitle>
          <DialogDescription>
            {existing ? "凭据留空 = 保持原值不变" : "凭据加密落库;测试连接会向上游发一条最小查询"}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label>Provider</Label>
            <Select
              value={provider}
              onValueChange={(v) => {
                setProvider(v);
                setBaseUrl("");
              }}
              disabled={!!existing}
            >
              <SelectTrigger>
                <SelectValue placeholder="选择 provider" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  {providers.map((p) => (
                    <SelectItem key={p.key} value={p.key}>
                      {p.title}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="extapi-name">名称(可选)</Label>
            <Input id="extapi-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="例:公司主力 FOFA 账号" />
          </div>

          {def?.cred_fields.map((f) => (
            <div key={f.key} className="space-y-1.5">
              <Label htmlFor={`extapi-${f.key}`}>
                {f.label}
                {existing && f.secret ? "(留空=不变)" : ""}
              </Label>
              <Input
                id={`extapi-${f.key}`}
                type={f.secret && !existing ? "password" : "text"}
                value={creds[f.key] ?? ""}
                onChange={(e) => setCreds((prev) => ({ ...prev, [f.key]: e.target.value }))}
                placeholder={existing?.masked?.[f.key]}
              />
            </div>
          ))}

          <div className="space-y-1.5">
            <Label htmlFor="extapi-base">自定义网关(可选,默认官方地址)</Label>
            <Input
              id="extapi-base"
              value={baseUrl}
              onChange={(e) => setBaseUrl(e.target.value)}
              placeholder={def?.default_base ?? "https://"}
            />
            <p className="text-muted-foreground text-xs">仅允许 http/https 且拒绝内网地址;请求前会强制校验</p>
          </div>

          {testResult ? (
            <div className={testResult.ok ? "text-emerald-600 text-sm" : "text-red-600 text-sm"}>
              {testResult.ok ? "✓ " : "✗ "}
              {testResult.message}
            </div>
          ) : null}
        </div>

        <DialogFooter className="gap-2 sm:justify-between">
          <Button variant="outline" onClick={testNow} disabled={testing || saving}>
            {testing ? "测试中…" : "测试连接"}
          </Button>
          <div className="flex gap-2">
            <Button variant="ghost" onClick={onCancel}>
              取消
            </Button>
            <Button onClick={submit} disabled={saving}>
              {saving ? "保存中…" : "保存"}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
