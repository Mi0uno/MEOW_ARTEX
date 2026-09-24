"use client";

// MCP 列表页:外部 MCP 工具服务器的总览与检索。
//
// 这一页只负责「找得到、看得懂、快速开关」:完整字段编辑、工具清单与按 Agent 的
// 可见性授权都收在详情页(点名称进入 /system/mcp/detail?id=)。列表里保留一个
// 快速编辑抽屉与刷新工具按钮,是为了不打断「顺手改个参数」的动线。

import * as React from "react";

import Link from "next/link";

import { ArrowRightIcon, PencilIcon, PlusIcon, RefreshCwIcon, SearchIcon, ServerIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { TablePagination } from "@/components/table-pagination";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { MCPServer } from "@/lib/types";

import {
  emptyMcpForm,
  formFromServer,
  formToPayload,
  McpFormFields,
  type McpFormState,
  TRANSPORT_OPTIONS,
  transportMeta,
  validateMcpForm,
} from "./_components/mcp-form";
import { McpToolsPanel } from "./_components/mcp-tools";

const PAGE_SIZE = 20;

// 一行里给用户看「它到底连的是什么」:stdio 看命令+参数,远程看 URL。
function endpointText(s: MCPServer): string {
  if (s.transport === "stdio") {
    // args 声明为非可选数组,但库里是 jsonb 列,脏数据可能是 null——用 Array.isArray 兜住。
    const args = Array.isArray(s.args) ? s.args : [];
    return [s.command, ...args].filter(Boolean).join(" ");
  }
  return s.url ?? "";
}

export default function MCPPage() {
  const [servers, setServers] = React.useState<MCPServer[]>([]);
  const [loading, setLoading] = React.useState(true);

  // 分类器(启用状态)与筛选器(传输方式)是两个正交维度:
  // 分类器给出「有几台在跑」的整体分布,筛选器再按接入方式收窄。
  const [statusKind, setStatusKind] = React.useState("all");
  const [transportFilter, setTransportFilter] = React.useState("all");
  const [query, setQuery] = React.useState("");
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);

  const [sheetOpen, setSheetOpen] = React.useState(false);
  const [editing, setEditing] = React.useState<MCPServer | null>(null); // null = 新建
  const [sheetTab, setSheetTab] = React.useState("config");
  const [form, setForm] = React.useState<McpFormState>(emptyMcpForm);
  const [saving, setSaving] = React.useState(false);
  const [refreshingId, setRefreshingId] = React.useState<number | null>(null);

  const load = React.useCallback(() => {
    setLoading(true);
    api
      .mcpServers()
      .then(setServers)
      .catch((e) => toast.error(`加载 MCP 服务器失败:${(e as Error).message}`))
      .finally(() => setLoading(false));
  }, []);

  React.useEffect(() => {
    load();
  }, [load]);

  // 计数必须来自未过滤全量:否则选中「已启用」后「已停用」的计数会归零,
  // 用户就再也切不回去了。
  const counts = React.useMemo(
    () => ({
      all: servers.length,
      enabled: servers.filter((s) => s.enabled).length,
      disabled: servers.filter((s) => !s.enabled).length,
    }),
    [servers],
  );

  const filtered = React.useMemo(() => {
    const q = query.trim().toLowerCase();
    return servers.filter((s) => {
      if (statusKind === "enabled" && !s.enabled) return false;
      if (statusKind === "disabled" && s.enabled) return false;
      if (transportFilter !== "all" && s.transport !== transportFilter) return false;
      if (!q) return true;
      return (
        s.name.toLowerCase().includes(q) ||
        (s.command ?? "").toLowerCase().includes(q) ||
        (s.url ?? "").toLowerCase().includes(q)
      );
    });
  }, [servers, query, statusKind, transportFilter]);

  // 分页按客户端切片:服务器数量有限,整表拉回来比来回请求更省。
  const paged = React.useMemo(() => filtered.slice((page - 1) * pageSize, page * pageSize), [filtered, page, pageSize]);

  const search = () => setPage(1);

  function openAdd() {
    setEditing(null);
    setForm(emptyMcpForm);
    setSheetTab("config");
    setSheetOpen(true);
  }

  function openEdit(s: MCPServer) {
    setEditing(s);
    setForm(formFromServer(s));
    setSheetTab("config");
    setSheetOpen(true);
  }

  async function saveForm() {
    const invalid = validateMcpForm(form);
    if (invalid) {
      toast.error(invalid);
      return;
    }
    setSaving(true);
    try {
      await api.saveMcpServer(formToPayload(form, editing?.id));
      toast.success(editing ? "已保存" : "已添加 MCP 服务器");
      setSheetOpen(false);
      load();
    } catch (e) {
      toast.error(`保存失败:${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  async function toggleEnabled(s: MCPServer) {
    try {
      await api.saveMcpServer({ ...s, enabled: !s.enabled });
      toast.success(s.enabled ? `已停用:${s.name}` : `已启用:${s.name}`);
      load();
    } catch (e) {
      toast.error(`操作失败:${(e as Error).message}`);
    }
  }

  // 刷新工具列表:重新连一次服务器并回写缓存,失败原因(连不上/命令写错)原样透出。
  async function refreshTools(s: MCPServer) {
    setRefreshingId(s.id);
    try {
      const t = await api.refreshMcpServer(s.id);
      toast.success(`「${s.name}」发现 ${t.length} 个工具`);
      load();
    } catch (e) {
      toast.error(`刷新失败:${(e as Error).message}`);
    } finally {
      setRefreshingId(null);
    }
  }

  async function removeServer(s: MCPServer) {
    if (!window.confirm(`删除 MCP 服务器「${s.name}」?该服务器的工具与按 Agent 的授权一并移除,不可恢复。`)) return;
    try {
      await api.deleteMcpServer(s.id);
      toast.success(`已删除:${s.name}`);
      load();
    } catch (e) {
      toast.error(`删除失败:${(e as Error).message}`);
    }
  }

  let body: React.ReactNode;
  if (loading && servers.length === 0) {
    body = <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>;
  } else if (servers.length === 0) {
    body = (
      <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
        <ServerIcon className="size-8 opacity-40" />
        <span>还没有 MCP 服务器。点右上角「新建 MCP」接入本地 stdio 或远程 HTTP 服务器。</span>
      </div>
    );
  } else if (filtered.length === 0) {
    body = (
      <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
        <ServerIcon className="size-8 opacity-40" />
        <span>没有匹配的 MCP 服务器</span>
      </div>
    );
  } else {
    body = (
      <>
        <Table className="table-fixed">
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              {/* 名称列不设宽度:table-fixed 下自动吃掉余量(设 w-full 会挤掉后面的列)。
                  其余固定列宽度之和 608px 明显小于 1280 视口下的内容区(~897px),
                  名称列仍有 ~289px 可读宽度。操作列按 4 个 icon-xs(24px)+间距给
                  w-32(128px,扣掉单元格 p-2 后 112px ≥ 102px),w-20 会撑出横向滚动条。 */}
              <TableHead>名称</TableHead>
              <TableHead className="w-24">传输方式</TableHead>
              <TableHead className="w-56">命令 / URL</TableHead>
              <TableHead className="w-20">工具数</TableHead>
              <TableHead className="w-20">启用状态</TableHead>
              <TableHead className="w-32">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {paged.map((s) => {
              const tm = transportMeta(s.transport);
              const endpoint = endpointText(s);
              const toolCount = s.tools?.length ?? 0;
              return (
                <TableRow key={s.id}>
                  <TableCell className="min-w-0">
                    <Link
                      href={`/system/mcp/detail?id=${s.id}`}
                      className="block w-full truncate text-left font-medium hover:underline"
                      title={s.name}
                    >
                      {s.name}
                    </Link>
                  </TableCell>
                  <TableCell>
                    <Badge variant="outline" className={tm.badgeClass}>
                      {tm.label}
                    </Badge>
                  </TableCell>
                  <TableCell className="min-w-0">
                    <span
                      className="text-muted-foreground block truncate font-mono text-xs"
                      title={endpoint || undefined}
                    >
                      {endpoint || "—"}
                    </span>
                  </TableCell>
                  <TableCell className="tabular-nums">
                    {toolCount > 0 ? toolCount : <span className="text-muted-foreground">未发现</span>}
                  </TableCell>
                  <TableCell>
                    {/* 徽标本身就是开关:原来卡片上的 Switch 一键启停不能因为改成表格而丢掉。 */}
                    <button type="button" onClick={() => void toggleEnabled(s)} title="点击启用 / 停用">
                      <Badge
                        variant="outline"
                        className={
                          s.enabled
                            ? "bg-emerald-500/15 text-emerald-600 border-emerald-500/30"
                            : "bg-muted text-muted-foreground"
                        }
                      >
                        {s.enabled ? "启用" : "停用"}
                      </Badge>
                    </button>
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center gap-0.5">
                      <Button variant="ghost" size="icon-xs" onClick={() => openEdit(s)} title="快速编辑">
                        <PencilIcon className="size-3.5" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon-xs"
                        disabled={refreshingId === s.id}
                        onClick={() => void refreshTools(s)}
                        title="刷新工具列表"
                      >
                        <RefreshCwIcon className={`size-3.5 ${refreshingId === s.id ? "animate-spin" : ""}`} />
                      </Button>
                      <Button variant="ghost" size="icon-xs" onClick={() => void removeServer(s)} title="删除">
                        <Trash2Icon className="text-muted-foreground size-3.5" />
                      </Button>
                      <Button variant="ghost" size="icon-xs" asChild title="进入详情">
                        <Link href={`/system/mcp/detail?id=${s.id}`} aria-label="进入详情">
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
          onPageSizeChange={(size) => {
            setPage(1);
            setPageSize(size);
          }}
        />
      </>
    );
  }

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="font-semibold text-xl">MCP</h1>
          <p className="text-muted-foreground text-sm">外部 MCP 工具服务器 · 按 Agent 授权可见</p>
        </div>
        <Button size="sm" onClick={openAdd}>
          <PlusIcon className="size-4" />
          新建 MCP
        </Button>
      </div>

      <Card>
        <CardContent className="px-0 pt-4">
          <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
            <div className="relative w-full sm:max-w-xs">
              <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
              <Input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && search()}
                placeholder="搜索名称 / 命令 / URL"
                className="pl-8"
              />
            </div>
            <Button variant="outline" size="sm" onClick={search}>
              搜索
            </Button>
            <Select
              value={transportFilter}
              onValueChange={(v) => {
                setPage(1);
                setTransportFilter(v);
              }}
            >
              <SelectTrigger className="w-32">
                <SelectValue placeholder="传输方式" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="all">全部传输方式</SelectItem>
                  {TRANSPORT_OPTIONS.map((t) => (
                    <SelectItem key={t.value} value={t.value}>
                      {t.value}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            <Tabs
              value={statusKind}
              onValueChange={(v) => {
                setPage(1);
                setStatusKind(v);
              }}
            >
              <TabsList>
                <TabsTrigger value="all">全部 {counts.all}</TabsTrigger>
                <TabsTrigger value="enabled">已启用 {counts.enabled}</TabsTrigger>
                <TabsTrigger value="disabled">已停用 {counts.disabled}</TabsTrigger>
              </TabsList>
            </Tabs>
            <span className="text-muted-foreground ml-auto text-xs tabular-nums">
              {query || transportFilter !== "all" || statusKind !== "all" ? `命中 ${filtered.length} / ` : ""}共{" "}
              {counts.all} 个
            </span>
          </div>

          {body}
        </CardContent>
      </Card>

      <Sheet
        open={sheetOpen}
        onOpenChange={(o) => {
          setSheetOpen(o);
          if (!o) setEditing(null);
        }}
      >
        <SheetContent side="right" className="w-full data-[side=right]:sm:max-w-lg">
          <SheetHeader>
            <SheetTitle>{editing ? editing.name : "新建 MCP 服务器"}</SheetTitle>
            <SheetDescription>
              stdio（本地起进程）或 http（远程 Streamable HTTP）;完整编辑与工具清单在详情页。
            </SheetDescription>
          </SheetHeader>

          {editing ? (
            <Tabs value={sheetTab} onValueChange={setSheetTab} className="flex min-h-0 flex-1 flex-col px-4">
              <TabsList>
                <TabsTrigger value="config">配置</TabsTrigger>
                <TabsTrigger value="tools">工具列表</TabsTrigger>
              </TabsList>
              <TabsContent value="config" className="min-h-0 flex-1 overflow-y-auto">
                <div className="py-4">
                  <McpFormFields
                    form={form}
                    onPatch={(patch) => setForm((f) => ({ ...f, ...patch }))}
                    idPrefix="mcp-sheet"
                  />
                </div>
                <div className="flex gap-2 pt-2 pb-6">
                  <Button onClick={() => void saveForm()} disabled={saving}>
                    保存
                  </Button>
                </div>
              </TabsContent>
              <TabsContent value="tools" className="min-h-0 flex-1 overflow-y-auto">
                <div className="py-4">
                  <McpToolsPanel serverId={editing.id} serverName={editing.name} onRefreshed={load} />
                </div>
              </TabsContent>
            </Tabs>
          ) : (
            <div className="flex min-h-0 flex-1 flex-col overflow-y-auto px-4">
              <div className="py-4">
                <McpFormFields
                  form={form}
                  onPatch={(patch) => setForm((f) => ({ ...f, ...patch }))}
                  idPrefix="mcp-sheet"
                />
              </div>
              <div className="pt-2 pb-6">
                <Button onClick={() => void saveForm()} disabled={saving}>
                  <PlusIcon /> 添加
                </Button>
              </div>
            </div>
          )}
        </SheetContent>
      </Sheet>
    </div>
  );
}
