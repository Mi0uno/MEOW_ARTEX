"use client";

// MCP 详情页:一台服务器的完整编辑面(?id=服务器 id;静态导出没有动态路由)。
//
// 三个页签分工:配置承载全部可写字段,工具是它当前暴露的能力清单,
// 可见性决定哪些 Agent 能拿到这些工具。列表页只做检索与快速开关,改字段进这里。

import * as React from "react";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";

import { ArrowLeftIcon, RefreshCwIcon, SaveIcon, ServerIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { MCPServer } from "@/lib/types";

import {
  formFromServer,
  formToPayload,
  McpFormFields,
  type McpFormState,
  transportMeta,
  validateMcpForm,
} from "../_components/mcp-form";
import { McpToolsPanel } from "../_components/mcp-tools";
import { McpVisibilityPanel } from "../_components/mcp-visibility";

// useSearchParams must sit under a Suspense boundary for static export.
export default function McpDetailPage() {
  return (
    <React.Suspense fallback={null}>
      <McpDetailInner />
    </React.Suspense>
  );
}

function McpDetailInner() {
  const searchParams = useSearchParams();
  const raw = searchParams.get("id") ?? "";
  const id = Number(raw);
  // 无效 id 必须在此处(任何 hook 之前)返回——把「读 id」与「用 id」拆成两个组件,
  // 才能既早退又不在同一个组件里条件调用 hook。
  if (!raw || !Number.isFinite(id) || id <= 0) return <MissingServer />;
  return <McpDetailBody serverId={id} />;
}

function MissingServer() {
  return (
    <div className="flex flex-1 flex-col gap-4">
      <div className="text-muted-foreground flex flex-col items-center gap-3 py-16 text-center text-sm">
        <ServerIcon className="size-8 opacity-40" />
        <p>未指定 MCP 服务器。请从「MCP」列表进入。</p>
        <Button asChild variant="outline" size="sm">
          <Link href="/system/mcp">
            <ArrowLeftIcon className="size-4" />
            返回列表
          </Link>
        </Button>
      </div>
    </div>
  );
}

function McpDetailBody({ serverId }: { serverId: number }) {
  const router = useRouter();
  const [tab, setTab] = React.useState("config");
  const [server, setServer] = React.useState<MCPServer | null>(null);
  const [notFound, setNotFound] = React.useState(false);
  const [loading, setLoading] = React.useState(true);
  const [form, setForm] = React.useState<McpFormState | null>(null);
  const [saving, setSaving] = React.useState(false);
  const [refreshing, setRefreshing] = React.useState(false);
  const [deleting, setDeleting] = React.useState(false);

  const load = React.useCallback(() => {
    setLoading(true);
    api
      .mcpServers()
      .then((ss) => {
        const found = ss.find((s) => s.id === serverId) ?? null;
        setServer(found);
        setNotFound(!found);
        if (found) setForm(formFromServer(found));
      })
      .catch((e) => {
        setNotFound(true);
        toast.error(`加载失败:${(e as Error).message}`);
      })
      .finally(() => setLoading(false));
  }, [serverId]);

  React.useEffect(() => {
    load();
  }, [load]);

  async function save() {
    if (!form || !server) return;
    const invalid = validateMcpForm(form);
    if (invalid) {
      toast.error(invalid);
      return;
    }
    setSaving(true);
    try {
      await api.saveMcpServer(formToPayload(form, server.id));
      toast.success("已保存");
      load();
    } catch (e) {
      toast.error(`保存失败:${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  async function refreshTools() {
    if (!server) return;
    setRefreshing(true);
    try {
      const t = await api.refreshMcpServer(server.id);
      toast.success(`发现 ${t.length} 个工具`);
      load();
    } catch (e) {
      toast.error(`刷新失败:${(e as Error).message}`);
    } finally {
      setRefreshing(false);
    }
  }

  async function remove() {
    if (!server) return;
    if (!window.confirm(`删除 MCP 服务器「${server.name}」?该服务器的工具与按 Agent 的授权一并移除,不可恢复。`)) return;
    setDeleting(true);
    try {
      await api.deleteMcpServer(server.id);
      toast.success(`已删除:${server.name}`);
      router.push("/system/mcp");
    } catch (e) {
      toast.error(`删除失败:${(e as Error).message}`);
      setDeleting(false);
    }
  }

  if (loading && !server) {
    return <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>;
  }

  if (notFound || !server || !form) {
    return (
      <div className="flex flex-1 flex-col gap-4">
        <div className="text-muted-foreground flex flex-col items-center gap-3 py-16 text-center text-sm">
          <ServerIcon className="size-8 opacity-40" />
          <p>没有找到 id 为 {serverId} 的 MCP 服务器,可能已被删除。</p>
          <Button asChild variant="outline" size="sm">
            <Link href="/system/mcp">
              <ArrowLeftIcon className="size-4" />
              返回列表
            </Link>
          </Button>
        </div>
      </div>
    );
  }

  const tm = transportMeta(server.transport);
  const endpoint =
    server.transport === "stdio"
      ? [server.command, ...(server.args ?? [])].filter(Boolean).join(" ")
      : (server.url ?? "");

  return (
    <div className="flex flex-1 flex-col gap-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 items-start gap-2">
          <Button asChild variant="ghost" size="icon-sm" title="返回列表">
            <Link href="/system/mcp">
              <ArrowLeftIcon className="size-4" />
            </Link>
          </Button>
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <h1 className="truncate font-semibold text-xl tracking-tight">{server.name}</h1>
              <Badge variant="outline" className={tm.badgeClass}>
                {tm.label}
              </Badge>
              <Badge
                variant="outline"
                className={
                  server.enabled
                    ? "bg-emerald-500/15 text-emerald-600 border-emerald-500/30"
                    : "bg-muted text-muted-foreground"
                }
              >
                {server.enabled ? "启用" : "停用"}
              </Badge>
            </div>
            <p className="text-muted-foreground truncate font-mono text-xs" title={endpoint || undefined}>
              id={server.id} · {server.transport}
              {endpoint ? ` · ${endpoint}` : ""}
            </p>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button size="sm" variant="outline" disabled={refreshing} onClick={() => void refreshTools()}>
            <RefreshCwIcon className={`size-4 ${refreshing ? "animate-spin" : ""}`} />
            刷新工具
          </Button>
          <Button size="sm" variant="outline" disabled={deleting} onClick={() => void remove()}>
            <Trash2Icon className="size-4" />
            删除
          </Button>
          <Button size="sm" disabled={saving} onClick={() => void save()}>
            <SaveIcon className="size-4" />
            保存
          </Button>
        </div>
      </div>

      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="config">配置</TabsTrigger>
          <TabsTrigger value="tools">
            工具{(server.tools?.length ?? 0) > 0 ? ` ${server.tools?.length}` : ""}
          </TabsTrigger>
          <TabsTrigger value="visibility">可见性</TabsTrigger>
        </TabsList>

        <TabsContent value="config">
          <Card>
            <CardHeader>
              <CardTitle>服务器配置</CardTitle>
              <CardDescription>
                改完点右上角「保存」。stdio 与远程是两套互斥字段,切换传输方式后另一套会被清空。
              </CardDescription>
            </CardHeader>
            <CardContent>
              <McpFormFields
                form={form}
                onPatch={(patch) => setForm((f) => (f ? { ...f, ...patch } : f))}
                idPrefix="mcp-detail"
              />
              <div className="mt-4 flex justify-end gap-2">
                <Button variant="outline" disabled={saving} onClick={load}>
                  放弃改动
                </Button>
                <Button disabled={saving} onClick={() => void save()}>
                  <SaveIcon className="size-4" />
                  保存
                </Button>
              </div>
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="tools">
          <Card>
            <CardHeader>
              <CardTitle>工具清单</CardTitle>
              <CardDescription>来自最近一次工具发现的缓存。改完命令 / URL 后点「刷新」重新连接并拉取。</CardDescription>
            </CardHeader>
            <CardContent>
              <McpToolsPanel serverId={server.id} serverName={server.name} onRefreshed={load} />
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="visibility">
          <Card>
            <CardHeader>
              <CardTitle>按 Agent 授权</CardTitle>
              <CardDescription>勾选后该 Agent 才能看到这台服务器提供的工具。</CardDescription>
            </CardHeader>
            <CardContent>
              <McpVisibilityPanel serverId={server.id} />
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </div>
  );
}
