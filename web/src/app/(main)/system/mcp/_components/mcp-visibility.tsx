"use client";

// MCP 可见性面板:哪些 Agent 能拿到这台服务器的工具。
//
// 后端是 agent_visibility 关联表(resource_kind='mcp'),资源侧读、按 agent 逐条切换。
// 这个能力原来长在列表页的卡片里,改成表格后卡片消失,所以整体搬到详情页。

import * as React from "react";

import { EyeIcon, Loader2Icon, SearchIcon, ShieldCheckIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import type { Agent } from "@/lib/types";

export function McpVisibilityPanel({ serverId }: { serverId: number }) {
  const [agents, setAgents] = React.useState<Agent[]>([]);
  const [visible, setVisible] = React.useState<string[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [query, setQuery] = React.useState("");
  const [pending, setPending] = React.useState<string | null>(null);

  const load = React.useCallback(() => {
    setLoading(true);
    Promise.all([api.agents(), api.resourceVisibility("mcp", serverId)])
      .then(([ags, ids]) => {
        setAgents(ags ?? []);
        setVisible(ids ?? []);
      })
      .catch((e) => toast.error(`加载可见性失败:${(e as Error).message}`))
      .finally(() => setLoading(false));
  }, [serverId]);

  React.useEffect(() => {
    load();
  }, [load]);

  async function toggle(agent: Agent) {
    const on = visible.includes(agent.id);
    setPending(agent.id);
    try {
      await api.toggleVisibility(agent.id, "mcp", serverId, !on);
      setVisible((v) => (on ? v.filter((x) => x !== agent.id) : [...v, agent.id]));
      toast.success(`${on ? "取消" : "授予"}「${agent.name}」可见`);
    } catch (e) {
      toast.error(`操作失败:${(e as Error).message}`);
    } finally {
      setPending(null);
    }
  }

  const filtered = React.useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return agents;
    return agents.filter((a) => a.name.toLowerCase().includes(q) || a.key.toLowerCase().includes(q));
  }, [agents, query]);

  // 三态用 if/else 而非嵌套三元:嵌套三元会被 lint 判为难以阅读。
  let body: React.ReactNode;
  if (loading) {
    body = <p className="text-muted-foreground py-10 text-center text-sm">加载中…</p>;
  } else if (agents.length === 0) {
    body = (
      <div className="text-muted-foreground flex flex-col items-center gap-2 py-10 text-center text-sm">
        <ShieldCheckIcon className="size-8 opacity-40" />
        <span>还没有 Agent,无法授权。</span>
      </div>
    );
  } else if (filtered.length === 0) {
    body = <p className="text-muted-foreground py-10 text-center text-sm">没有匹配的 Agent</p>;
  } else {
    body = (
      <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
        {filtered.map((a) => {
          const on = visible.includes(a.id);
          return (
            <div key={a.id} className="hover:bg-muted/50 flex items-center gap-2.5 rounded-lg border p-3 transition">
              <Checkbox
                id={`mcp-vis-${a.id}`}
                checked={on}
                disabled={pending === a.id}
                onCheckedChange={() => void toggle(a)}
              />
              <Label
                htmlFor={`mcp-vis-${a.id}`}
                className="flex min-w-0 flex-1 cursor-pointer items-center gap-2.5 font-normal"
              >
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm font-medium">{a.name}</span>
                  <span className="text-muted-foreground block truncate font-mono text-[11px]">{a.key}</span>
                </span>
                <Badge
                  variant="outline"
                  className={
                    on ? "bg-emerald-500/15 text-emerald-600 border-emerald-500/30" : "bg-muted text-muted-foreground"
                  }
                >
                  {on ? "可见" : "不可见"}
                </Badge>
              </Label>
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
            placeholder="搜索 Agent 名称 / key"
            className="pl-8"
          />
        </div>
        <span className="text-muted-foreground text-xs tabular-nums">
          {visible.length} / {agents.length} 个 Agent 可见
        </span>
        <Button size="sm" variant="outline" className="ml-auto" disabled={loading} onClick={load}>
          {loading ? <Loader2Icon className="size-4 animate-spin" /> : <EyeIcon className="size-4" />}
          重新加载
        </Button>
      </div>

      {body}
    </div>
  );
}
