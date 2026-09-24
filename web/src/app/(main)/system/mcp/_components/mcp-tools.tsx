"use client";

// 工具清单面板:列表页的快速编辑抽屉与详情页的「工具」页签共用。
// 工具清单来自后端缓存(mcp_tools_cache),只有刷新时才真正连一次服务器。

import * as React from "react";

import { Loader2Icon, RefreshCwIcon, SearchIcon, WrenchIcon } from "lucide-react";
import { toast } from "sonner";

import { FormattedPayload } from "@/components/formatted-payload";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api } from "@/lib/api";
import type { MCPTool } from "@/lib/types";

// 后端缓存表只落 name + description,参数 schema 目前不落库。
// 这里对「将来多带了 schema 字段」做兼容读取:有就渲染出来,没有就不显示空壳,
// 免得为一个还不存在的字段在 UI 上摆一排「—」。
function toolSchemaRaw(t: MCPTool): string | null {
  const extra = t as unknown as Record<string, unknown>;
  const schema = extra.input_schema ?? extra.inputSchema ?? extra.schema;
  if (!schema || typeof schema !== "object") return null;
  try {
    return JSON.stringify(schema, null, 2);
  } catch {
    return null;
  }
}

export function McpToolsPanel({
  serverId,
  serverName,
  onRefreshed,
}: {
  serverId: number;
  serverName: string;
  onRefreshed?: () => void;
}) {
  const [tools, setTools] = React.useState<MCPTool[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [refreshing, setRefreshing] = React.useState(false);
  const [query, setQuery] = React.useState("");

  const load = React.useCallback(() => {
    setLoading(true);
    api
      .mcpTools(serverId)
      .then(setTools)
      .catch((e) => toast.error(`加载工具列表失败:${(e as Error).message}`))
      .finally(() => setLoading(false));
  }, [serverId]);

  React.useEffect(() => {
    load();
  }, [load]);

  async function refresh() {
    setRefreshing(true);
    try {
      const t = await api.refreshMcpServer(serverId);
      setTools(t);
      toast.success(`发现 ${t.length} 个工具`);
      onRefreshed?.();
    } catch (e) {
      // 刷新失败通常是连不上或命令写错,原因要原样透出才好定位。
      toast.error(`刷新失败:${(e as Error).message}`);
    } finally {
      setRefreshing(false);
    }
  }

  const filtered = React.useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return tools;
    return tools.filter((t) => t.name.toLowerCase().includes(q) || (t.description ?? "").toLowerCase().includes(q));
  }, [tools, query]);

  // 三态用 if/else 而非嵌套三元:嵌套三元会被 lint 判为难以阅读,且这里分支较多。
  let body: React.ReactNode;
  if (loading) {
    body = <p className="text-muted-foreground py-10 text-center text-sm">加载中…</p>;
  } else if (tools.length === 0) {
    body = (
      <div className="text-muted-foreground flex flex-col items-center gap-2 py-10 text-center text-sm">
        <WrenchIcon className="size-8 opacity-40" />
        <span>
          「{serverName}」尚未发现工具。可能原因:服务器未启用、未连接成功,或命令 / URL 填写有误。
          <br />
          点「刷新」重新连接并发现工具;失败原因会以提示形式给出。
        </span>
      </div>
    );
  } else if (filtered.length === 0) {
    body = <p className="text-muted-foreground py-10 text-center text-sm">没有匹配的工具</p>;
  } else {
    body = (
      <div className="flex flex-col divide-y rounded-lg border">
        {filtered.map((t) => {
          const schema = toolSchemaRaw(t);
          return (
            <div key={t.name} className="flex flex-col gap-2 p-3">
              <code className="font-mono text-sm font-medium">{t.name}</code>
              {t.description ? (
                <p className="text-muted-foreground text-xs leading-relaxed whitespace-pre-wrap">{t.description}</p>
              ) : (
                <p className="text-muted-foreground text-xs">（无描述）</p>
              )}
              {schema && <FormattedPayload raw={schema} maxHeight="max-h-60" />}
            </div>
          );
        })}
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative w-full sm:max-w-xs">
          <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="搜索工具名 / 描述"
            className="pl-8"
          />
        </div>
        <span className="text-muted-foreground text-xs tabular-nums">
          {query ? `命中 ${filtered.length} / ` : ""}共 {tools.length} 个工具
        </span>
        <Button size="sm" variant="outline" className="ml-auto" disabled={refreshing} onClick={() => void refresh()}>
          {refreshing ? <Loader2Icon className="size-4 animate-spin" /> : <RefreshCwIcon className="size-4" />}
          刷新
        </Button>
      </div>

      {body}
    </div>
  );
}
