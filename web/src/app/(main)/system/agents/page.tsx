"use client";

// AI 管理 · Agent 列表页。
//
// 形态与其它 AI 管理页一致:分类器(Tabs)+ 筛选器 + 搜索 + 分页器 + 表格行内操作。
//   - 分类器 = 归属阶段(通用/信息搜集/渗透测试/后渗透/报告中心),带计数;
//   - 筛选器 = 来源(内置/自定义)、只看已绑定模型的;
//   - 行内操作 = 快速编辑(抽屉)/ 删除(仅自定义)/ 进入详情页。
//
// Agent 数量少(内置几个 + 用户自建),一次 api.agents() 拉全量后在前端筛选分页,
// 不引入后端分页参数——省一次往返,分类计数也能直接从全量算出来。

import * as React from "react";

import Link from "next/link";

import { ArrowRightIcon, BotIcon, PencilIcon, PlusIcon, SearchIcon, Trash2Icon } from "lucide-react";
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
import { Card, CardContent } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
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
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { Agent, LLMProfile } from "@/lib/types";

import { COMMON_STAGE, EnabledBadge, OriginBadge, STAGE_TABS, StageBadge, stageLabel } from "./_components/agent-meta";

const PAGE_SIZE = 20;

// agentDetailHref 详情页地址:参数名固定为 key(外部已有链接引用,不可改)。
function agentDetailHref(key: string): string {
  return `/system/agents/detail?key=${encodeURIComponent(key)}`;
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
              value={stage || COMMON_STAGE}
              onValueChange={(v) => setStage(v === COMMON_STAGE ? "" : (v as "recon" | "exploit" | "post" | "report"))}
            >
              <SelectTrigger id="agent-stage" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value={COMMON_STAGE}>通用（跨阶段 · 任务中心）</SelectItem>
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
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [loading, setLoading] = React.useState(true);
  // 快速编辑走抽屉(保留原有便捷路径),完整编辑走详情页。
  const [editKey, setEditKey] = React.useState<string | null>(null);
  const [query, setQuery] = React.useState("");
  // stageKind 是分类器维度;COMMON_STAGE 哨兵代表「通用」(stage 字段为空串)。
  const [stageKind, setStageKind] = React.useState("all");
  const [originFilter, setOriginFilter] = React.useState("all");
  const [boundOnly, setBoundOnly] = React.useState(false);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);

  const reload = React.useCallback(() => {
    setLoading(true);
    api
      .agents()
      .then(setAgents)
      .catch(() => setAgents([]))
      .finally(() => setLoading(false));
  }, []);
  React.useEffect(() => {
    reload();
  }, [reload]);
  // 模型绑定列要显示 profile 名称而不是裸 id,故顺带拉一次配置清单(失败则回落到 #id)。
  React.useEffect(() => {
    api
      .llmProfiles()
      .then(setProfiles)
      .catch(() => setProfiles([]));
  }, []);

  const profileName = React.useCallback(
    (id?: number | null): string => {
      if (id == null) return "";
      const p = profiles.find((x) => String(x.id) === String(id));
      return p ? p.name : `#${id}`;
    },
    [profiles],
  );

  // matches 只做搜索 + 来源 + 模型绑定三项判定,不含阶段——分类器计数要在「不含阶段」的
  // 集合上算,否则选中某阶段后其他阶段的计数会归零(用户报障过的坑)。
  const matches = React.useCallback(
    (a: Agent): boolean => {
      if (originFilter === "builtin" && !a.builtin) return false;
      if (originFilter === "custom" && a.builtin) return false;
      if (boundOnly && a.llm_profile_id == null) return false;
      const q = query.trim().toLowerCase();
      if (!q) return true;
      return (
        a.name.toLowerCase().includes(q) ||
        a.key.toLowerCase().includes(q) ||
        (a.description ?? "").toLowerCase().includes(q)
      );
    },
    [query, originFilter, boundOnly],
  );

  const scoped = React.useMemo(() => agents.filter(matches), [agents, matches]);

  // 分类计数:基于 scoped(已应用搜索/来源/模型绑定,未应用阶段),保证点某个阶段后
  // 其余阶段仍有真实计数。空串(通用)必须归到 COMMON_STAGE 哨兵键上——页签查的是哨兵,
  // 直接按原值记会记到 "" 上,「通用」页签就永远是 0。
  const counts = React.useMemo(() => {
    const c: Record<string, number> = { all: scoped.length, [COMMON_STAGE]: 0 };
    for (const a of scoped) {
      const k = a.stage ? a.stage : COMMON_STAGE;
      c[k] = (c[k] ?? 0) + 1;
    }
    return c;
  }, [scoped]);

  const filtered = React.useMemo(
    () =>
      scoped.filter((a) => {
        const s = a.stage ?? "";
        if (stageKind === "all") return true;
        if (stageKind === COMMON_STAGE) return s === "";
        return s === stageKind;
      }),
    [scoped, stageKind],
  );

  const paged = React.useMemo(() => filtered.slice((page - 1) * pageSize, page * pageSize), [filtered, page, pageSize]);

  const hasFilter = query.trim() !== "" || stageKind !== "all" || originFilter !== "all" || boundOnly;

  // 空态文案分三种:库里真没有 / 筛选无命中 / 该阶段下为空。拆成变量而不是嵌套三元,
  // 免得读的时候要数括号。
  let emptyText = "该阶段下暂无 Agent。";
  if (agents.length === 0) emptyText = "还没有 Agent。点「新建 Agent」创建自定义会话助手。";
  else if (hasFilter) emptyText = "没有匹配的 Agent，试试放宽筛选条件。";

  const editing = agents.find((a) => a.key === editKey) ?? null;

  const remove = async (a: Agent) => {
    try {
      await api.deleteAgent(a.key);
      toast.success(`已删除 Agent「${a.name}」`);
      reload();
    } catch (e) {
      toast.error(`删除失败：${(e as Error).message}`);
    }
  };

  // body 抽成变量而不是内联三元,避免三层嵌套三元(与 mcp 页写法一致)。
  let body: React.ReactNode;
  if (loading && agents.length === 0) {
    body = <p className="py-16 text-center text-muted-foreground text-sm">加载中…</p>;
  } else if (filtered.length === 0) {
    body = (
      <div className="flex flex-col items-center gap-2 py-16 text-center text-muted-foreground text-sm">
        <BotIcon className="size-8 opacity-40" />
        <span>{emptyText}</span>
      </div>
    );
  } else {
    body = (
      <>
        {/* 列宽:table-fixed 下名称列不设宽度、自动吃掉余量(设 w-full 会挤掉后面的列)。
            固定列宽度之和 = 688px,明显小于 1280 视口下的内容区宽(约 897px),
            否则名称列会被压到看不见、表还撑出横向滚动条。
            取值依据:阶段徽标「信息搜集」4 字需 w-20;key 是等宽字,最长 mainagent 需 w-24;
            操作列放 3 个 size-6 图标按钮(72px + 间距 + 单元格内边距)需 w-24;
            三个计数列只需容纳 3 位数,故压到 w-16 把宽度让给名称列。 */}
        <Table className="table-fixed">
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>名称</TableHead>
              <TableHead className="w-24">标识 key</TableHead>
              <TableHead className="w-20">归属阶段</TableHead>
              <TableHead className="w-16">来源</TableHead>
              <TableHead className="w-24">模型绑定</TableHead>
              <TableHead className="w-16">工具</TableHead>
              <TableHead className="w-16">MCP</TableHead>
              <TableHead className="w-16">Skill</TableHead>
              <TableHead className="w-16">启用状态</TableHead>
              <TableHead className="w-24">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {paged.map((a) => (
              <TableRow key={a.key}>
                {/* 名称即入口:点击进入专属详情页做完整编辑。 */}
                <TableCell className="min-w-0">
                  <Link
                    href={agentDetailHref(a.key)}
                    className="block w-full truncate text-left font-medium hover:underline"
                    title={a.name}
                  >
                    {a.name}
                  </Link>
                  {a.description ? (
                    <span className="block w-full truncate text-muted-foreground text-xs" title={a.description}>
                      {a.description}
                    </span>
                  ) : null}
                </TableCell>
                <TableCell className="truncate font-mono text-muted-foreground text-xs" title={a.key}>
                  {a.key}
                </TableCell>
                <TableCell>
                  <StageBadge stage={a.stage} />
                </TableCell>
                <TableCell>
                  <OriginBadge builtin={a.builtin} />
                </TableCell>
                <TableCell className="truncate text-xs" title={profileName(a.llm_profile_id)}>
                  {a.llm_profile_id == null ? (
                    <span className="text-muted-foreground">跟随任务/全局</span>
                  ) : (
                    <Badge variant="outline" className="border-blue-500/30 bg-blue-500/15 text-blue-600">
                      <span className="truncate">{profileName(a.llm_profile_id)}</span>
                    </Badge>
                  )}
                </TableCell>
                <TableCell className="tabular-nums">{a.tool_count ?? 0}</TableCell>
                <TableCell className="tabular-nums">{a.mcp_count ?? 0}</TableCell>
                <TableCell className="tabular-nums">{a.skill_count ?? 0}</TableCell>
                <TableCell>
                  <EnabledBadge enabled={a.enabled} />
                </TableCell>
                <TableCell>
                  <div className="flex items-center gap-0.5">
                    <Button variant="ghost" size="icon-xs" onClick={() => setEditKey(a.key)} title="快速编辑">
                      <PencilIcon className="size-3.5" />
                    </Button>
                    {/* 删除只对自定义 Agent 开放:内置 Agent 由后端保护,前端也不给入口。 */}
                    {!a.builtin && (
                      <AlertDialog>
                        <AlertDialogTrigger asChild>
                          <Button variant="ghost" size="icon-xs" title="删除">
                            <Trash2Icon className="size-3.5 text-muted-foreground" />
                          </Button>
                        </AlertDialogTrigger>
                        <AlertDialogContent>
                          <AlertDialogHeader>
                            <AlertDialogTitle>删除 Agent「{a.name}」？</AlertDialogTitle>
                            <AlertDialogDescription>
                              将一并删除它的提示词、变量、可见性与工具绑定。此操作不可撤销。
                            </AlertDialogDescription>
                          </AlertDialogHeader>
                          <AlertDialogFooter>
                            <AlertDialogCancel>取消</AlertDialogCancel>
                            <AlertDialogAction onClick={() => void remove(a)}>删除</AlertDialogAction>
                          </AlertDialogFooter>
                        </AlertDialogContent>
                      </AlertDialog>
                    )}
                    <Button variant="ghost" size="icon-xs" asChild title="进入详情页">
                      <Link href={agentDetailHref(a.key)} aria-label="进入详情页">
                        <ArrowRightIcon className="size-3.5" />
                      </Link>
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
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
      </>
    );
  }

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h1 className="font-semibold text-xl tracking-tight">Agent</h1>
          <p className="text-muted-foreground text-sm">
            内置 Agent 的提示词/配置，以及自定义会话 Agent 的创建与管理。点名称进入详情页做完整编辑。
          </p>
        </div>
        <CreateAgentDialog
          onCreated={(key) => {
            reload();
            setEditKey(key);
          }}
        />
      </div>

      <Card>
        <CardContent className="px-0 pt-4">
          <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
            <div className="relative w-full sm:max-w-xs">
              <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
              <Input
                value={query}
                onChange={(e) => {
                  setPage(1);
                  setQuery(e.target.value);
                }}
                onKeyDown={(e) => e.key === "Enter" && setPage(1)}
                placeholder="搜索名称 / key / 描述"
                className="pl-8"
              />
            </div>
            <Button variant="outline" size="sm" onClick={() => setPage(1)}>
              搜索
            </Button>
            <Select
              value={originFilter}
              onValueChange={(v) => {
                setPage(1);
                setOriginFilter(v);
              }}
            >
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
            <label htmlFor="bound-only" className="flex items-center gap-1.5 text-sm">
              <Checkbox
                id="bound-only"
                checked={boundOnly}
                onCheckedChange={(v) => {
                  setPage(1);
                  setBoundOnly(!!v);
                }}
              />
              只看已绑定模型的
            </label>
            <span className="ml-auto text-muted-foreground text-xs tabular-nums">
              {hasFilter ? `命中 ${filtered.length} / ` : ""}共 {agents.length} 个
            </span>
            {/* 6 个阶段页签加起来宽,独占一行(w-full 在 flex-wrap 下会换行),
                与上面的搜索/筛选行分开,免得挤成一团。 */}
            <Tabs
              className="w-full"
              value={stageKind}
              onValueChange={(v) => {
                setPage(1);
                setStageKind(v);
              }}
            >
              <TabsList>
                {STAGE_TABS.map((t) => (
                  <TabsTrigger key={t.value} value={t.value}>
                    {t.label} {counts[t.value] ?? 0}
                  </TabsTrigger>
                ))}
              </TabsList>
            </Tabs>
          </div>

          {body}
        </CardContent>
      </Card>

      {/* 快速编辑抽屉:不离开列表就能改提示词/可见性,完整编辑请进详情页。 */}
      <Sheet open={!!editing} onOpenChange={(o) => !o && setEditKey(null)}>
        <SheetContent
          side="right"
          className="flex flex-col gap-0 p-0 data-[side=right]:w-[45vw] data-[side=right]:sm:max-w-[45vw]"
        >
          {editing && (
            <>
              <SheetHeader className="px-4">
                <SheetTitle className="flex flex-wrap items-center gap-2">
                  {editing.name}
                  <span className="font-mono text-muted-foreground text-xs">{editing.key}</span>
                  <OriginBadge builtin={editing.builtin} />
                  <StageBadge stage={editing.stage} />
                </SheetTitle>
                <SheetDescription>
                  {editing.description ||
                    `提示词、配置、可见资源与工具绑定（阶段：${stageLabel(editing.stage ?? "")}）`}
                </SheetDescription>
              </SheetHeader>
              <AgentEditor agentKey={editing.key} onSaved={reload} />
            </>
          )}
        </SheetContent>
      </Sheet>
    </div>
  );
}
