"use client";

import * as React from "react";

import { Bot, PlusIcon, SearchIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { AgentEditor } from "@/components/agent-editor";
import { TablePagination } from "@/components/table-pagination";
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
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { Agent } from "@/lib/types";

// stageLabel 阶段徽标文案(''=通用,不显示徽标)。
function stageLabel(stage: string): string {
  if (stage === "recon") return "信息搜集";
  if (stage === "exploit") return "渗透测试";
  if (stage === "report") return "报告中心";
  return "后渗透";
}

// AgentGridCard is one clickable tile opening the agent's editor drawer. Custom
// (non-builtin) agents get a delete button.
function AgentGridCard({ agent, onOpen, onDeleted }: { agent: Agent; onOpen: () => void; onDeleted: () => void }) {
  async function del() {
    try {
      await api.deleteAgent(agent.key);
      toast.success(`已删除 Agent「${agent.name}」`);
      onDeleted();
    } catch (e) {
      toast.error(`删除失败：${(e as Error).message}`);
    }
  }
  return (
    <div className="group relative flex flex-col gap-2 rounded-lg border p-4 transition-colors hover:border-primary/50">
      <button type="button" onClick={onOpen} className="flex flex-col gap-2 text-left">
        <div className="flex flex-wrap items-center gap-2">
          <Bot className="size-4 text-muted-foreground" />
          <span className="font-medium text-sm">{agent.name}</span>
          <span className="font-mono text-muted-foreground text-xs">{agent.key}</span>
          {agent.builtin ? (
            <Badge variant="secondary" className="px-1.5 py-0 text-[10px]">
              内置
            </Badge>
          ) : (
            <Badge variant="outline" className="px-1.5 py-0 text-[10px]">
              自定义
            </Badge>
          )}
          {!agent.enabled && (
            <Badge variant="outline" className="px-1.5 py-0 text-[10px] text-destructive">
              已停用
            </Badge>
          )}
          {agent.stage ? (
            <Badge variant="outline" className="border-blue-500/30 px-1.5 py-0 text-[10px] text-blue-600">
              {stageLabel(agent.stage)}
            </Badge>
          ) : null}
        </div>
        <p className="line-clamp-2 min-h-8 text-muted-foreground text-xs">{agent.description ?? "（无描述）"}</p>
        <div className="flex flex-wrap gap-1.5 text-[10px] text-muted-foreground">
          <span className="rounded border px-1.5 py-0.5">MCP {agent.mcp_count ?? 0}</span>
          <span className="rounded border px-1.5 py-0.5">Skill {agent.skill_count ?? 0}</span>
          <span className="rounded border px-1.5 py-0.5">工具 {agent.tool_count ?? 0}</span>
        </div>
      </button>
      {!agent.builtin && (
        <AlertDialog>
          <AlertDialogTrigger asChild>
            <Button
              variant="ghost"
              size="icon-sm"
              className="absolute top-2 right-2 text-muted-foreground opacity-0 transition-opacity hover:text-destructive group-hover:opacity-100"
            >
              <Trash2Icon className="size-3.5" />
            </Button>
          </AlertDialogTrigger>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>删除 Agent「{agent.name}」？</AlertDialogTitle>
              <AlertDialogDescription>
                将一并删除它的提示词、变量、可见性与工具绑定。此操作不可撤销。
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>取消</AlertDialogCancel>
              <AlertDialogAction onClick={del}>删除</AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      )}
    </div>
  );
}

function CreateAgentDialog({ onCreated }: { onCreated: (key: string) => void }) {
  const [open, setOpen] = React.useState(false);
  const [key, setKey] = React.useState("");
  const [name, setName] = React.useState("");
  const [description, setDescription] = React.useState("");
  const [stage, setStage] = React.useState<"" | "recon" | "exploit" | "post" | "report">("");
  const [busy, setBusy] = React.useState(false);

  async function create() {
    setBusy(true);
    try {
      const a = await api.createAgent(key.trim(), name.trim(), description.trim(), stage);
      toast.success(`已创建 Agent「${a.name}」`);
      setOpen(false);
      setKey("");
      setName("");
      setDescription("");
      setStage("");
      onCreated(a.key);
    } catch (e) {
      toast.error(`创建失败：${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  }

  const keyOk = /^[a-z][a-z0-9_]*$/.test(key.trim());
  const canCreate = keyOk && name.trim().length > 0 && !busy;

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button size="sm">
          <PlusIcon /> 新建 Agent
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>新建自定义 Agent</DialogTitle>
          <DialogDescription>
            创建一个会话型助手。key 用于内部标识，创建后不可更改；名称与描述用于识别。
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-4 py-2">
          <div className="grid gap-1.5">
            <Label htmlFor="agent-key">Key</Label>
            <Input
              id="agent-key"
              placeholder="如 research_helper"
              value={key}
              onChange={(e) => setKey(e.target.value)}
              className="font-mono"
            />
            {key.length > 0 && !keyOk && (
              <span className="text-destructive text-xs">小写字母开头，仅含小写字母/数字/下划线</span>
            )}
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="agent-name">名称</Label>
            <Input id="agent-name" placeholder="如 研究助手" value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="agent-desc">描述</Label>
            <Textarea
              id="agent-desc"
              placeholder="一句话说明这个 Agent 是干什么的"
              rows={2}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="agent-stage">归属阶段</Label>
            <Select
              value={stage || "__common__"}
              onValueChange={(v) => setStage(v === "__common__" ? "" : (v as "recon" | "exploit" | "post" | "report"))}
            >
              <SelectTrigger id="agent-stage" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="__common__">通用（跨阶段 · 任务中心）</SelectItem>
                  <SelectItem value="recon">信息搜集</SelectItem>
                  <SelectItem value="exploit">渗透测试</SelectItem>
                  <SelectItem value="post">后渗透</SelectItem>
                  <SelectItem value="report">报告中心</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
            <span className="text-muted-foreground text-xs">侧边栏阶段分组的「对话」只显示归属该阶段的 Agent 会话</span>
          </div>
        </div>
        <DialogFooter>
          <Button onClick={create} disabled={!canCreate}>
            创建
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export default function AgentsPage() {
  const [agents, setAgents] = React.useState<Agent[]>([]);
  const [editKey, setEditKey] = React.useState<string | null>(null);
  // 列表页标准能力:搜索 / 归属阶段筛选 / 来源(内置·自定义)筛选 / 分页。
  const [query, setQuery] = React.useState("");
  const [stageFilter, setStageFilter] = React.useState("all");
  const [originFilter, setOriginFilter] = React.useState("all");
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(12);

  const reload = React.useCallback(() => {
    api
      .agents()
      .then(setAgents)
      .catch(() => setAgents([]));
  }, []);
  React.useEffect(() => {
    reload();
  }, [reload]);

  const filtered = React.useMemo(() => {
    const q = query.trim().toLowerCase();
    return agents.filter((a) => {
      // 「通用」用哨兵 __common__ 表达(空串是 Radix Select 保留值)。
      if (stageFilter === "__common__" && (a.stage || "") !== "") return false;
      if (stageFilter !== "all" && stageFilter !== "__common__" && (a.stage || "") !== stageFilter) return false;
      if (originFilter === "builtin" && !a.builtin) return false;
      if (originFilter === "custom" && a.builtin) return false;
      if (!q) return true;
      return (
        a.name.toLowerCase().includes(q) ||
        a.key.toLowerCase().includes(q) ||
        (a.description ?? "").toLowerCase().includes(q)
      );
    });
  }, [agents, query, stageFilter, originFilter]);

  const paged = React.useMemo(
    () => filtered.slice((page - 1) * pageSize, page * pageSize),
    [filtered, page, pageSize],
  );

  // biome-ignore lint/correctness/useExhaustiveDependencies: filters intentionally reset pagination.
  React.useEffect(() => {
    setPage(1);
  }, [query, stageFilter, originFilter]);

  const editing = agents.find((a) => a.key === editKey) ?? null;

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h1 className="font-semibold text-xl tracking-tight">Agent</h1>
          <p className="text-muted-foreground text-sm">内置 Agent 的提示词/配置，以及自定义会话 Agent 的创建与管理</p>
        </div>
        <CreateAgentDialog
          onCreated={(key) => {
            reload();
            setEditKey(key);
          }}
        />
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Agent 清单</CardTitle>
          <CardDescription>
            {filtered.length === agents.length ? `共 ${agents.length} 个` : `${filtered.length}/${agents.length} 个`}
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <div className="flex flex-wrap items-center gap-2">
            <div className="relative w-full sm:max-w-xs">
              <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
              <Input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder="搜索名称 / key / 描述"
                className="pl-8"
              />
            </div>
            <Select value={stageFilter} onValueChange={setStageFilter}>
              <SelectTrigger className="w-36">
                <SelectValue placeholder="归属阶段" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="all">全部阶段</SelectItem>
                  {/* 空字符串是 Radix Select 的保留值(表示未选中),通用阶段用哨兵。 */}
                  <SelectItem value="__common__">通用</SelectItem>
                  <SelectItem value="recon">信息搜集</SelectItem>
                  <SelectItem value="exploit">渗透测试</SelectItem>
                  <SelectItem value="post">后渗透</SelectItem>
                  <SelectItem value="report">报告中心</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
            <Select value={originFilter} onValueChange={setOriginFilter}>
              <SelectTrigger className="w-32">
                <SelectValue placeholder="来源" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="all">全部来源</SelectItem>
                  <SelectItem value="builtin">内置</SelectItem>
                  <SelectItem value="custom">自定义</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
          </div>
          {filtered.length === 0 ? (
            <p className="py-6 text-center text-muted-foreground text-sm">
              {agents.length === 0 ? "（暂无 Agent）" : "没有匹配的 Agent"}
            </p>
          ) : (
            <>
              <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                {paged.map((a) => (
                  <AgentGridCard key={a.key} agent={a} onOpen={() => setEditKey(a.key)} onDeleted={reload} />
                ))}
              </div>
              <TablePagination
                page={page}
                pageSize={pageSize}
                total={filtered.length}
                onPageChange={setPage}
                onPageSizeChange={(s) => {
                  setPage(1);
                  setPageSize(s);
                }}
                pageSizeOptions={[12, 24, 48]}
              />
            </>
          )}
        </CardContent>
      </Card>

      <Sheet open={!!editing} onOpenChange={(o) => !o && setEditKey(null)}>
        <SheetContent
          side="right"
          className="flex flex-col gap-0 p-0 data-[side=right]:w-[45vw] data-[side=right]:sm:max-w-[45vw]"
        >
          {editing && (
            <>
              <SheetHeader className="px-4">
                <SheetTitle className="flex items-center gap-2">
                  {editing.name}
                  <span className="font-mono text-muted-foreground text-xs">{editing.key}</span>
                  {!editing.builtin && (
                    <Badge variant="outline" className="px-1.5 py-0 text-[10px]">
                      自定义
                    </Badge>
                  )}
                </SheetTitle>
                <SheetDescription>{editing.description || "提示词、配置、可见资源与工具绑定"}</SheetDescription>
              </SheetHeader>
              <AgentEditor agentKey={editing.key} onSaved={reload} />
            </>
          )}
        </SheetContent>
      </Sheet>
    </div>
  );
}
