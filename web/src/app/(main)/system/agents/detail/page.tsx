"use client";

// Agent 详情页(深链接:/system/agents/detail?key=<agent key>,参数名固定不可改,外部已引用)。
//
// 页面分三层,各管一件事,避免堆成一个超长表单:
//   头部   —— 返回 / 名称 / key / 状态徽标组 / 操作(编辑信息、删除);
//   信息卡 —— 身份与绑定摘要(角色、归属阶段、模型绑定、可见资源计数)+ 名称与描述编辑入口;
//   编辑器 —— 直接复用 AgentEditor,它内部已按「配置与提示词 / 收尾提示词 / MCP / Skill / Tools /
//             触发」分页签,详情页不再套一层 Tabs(两层页签会让人找不到北)。
//
// 详情页相对旧版(只把 AgentEditor 全宽铺开)补齐的能力:名称/描述编辑(后端 updateAgent 一直
// 有接口,但此前前端无入口)、状态徽标组、删除入口、参数缺失/不存在的空态。

import * as React from "react";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";

import { ArrowLeftIcon, BotIcon, PencilIcon, SaveIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { AgentEditor } from "@/components/agent-editor";
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
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { AgentDetail, Tool } from "@/lib/types";

import { EnabledBadge, OriginBadge, StageBadge, stageLabel } from "../_components/agent-meta";

// LIST_HREF 列表页地址:返回按钮、删除成功后跳转、空态出口都用它。
const LIST_HREF = "/system/agents";

// EditMetaDialog 编辑自定义 Agent 的名称与描述(key/角色不可改,由后端约束)。
function EditMetaDialog({
  open,
  onOpenChange,
  agentKey,
  name,
  description,
  onSaved,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  agentKey: string;
  name: string;
  description: string;
  onSaved: () => void;
}) {
  const [draftName, setDraftName] = React.useState(name);
  const [draftDesc, setDraftDesc] = React.useState(description);
  const [busy, setBusy] = React.useState(false);

  // 每次打开都从最新的 agent 值重灌草稿,避免上次取消留下的脏值被提交。
  React.useEffect(() => {
    if (open) {
      setDraftName(name);
      setDraftDesc(description);
    }
  }, [open, name, description]);

  async function save() {
    if (!draftName.trim()) {
      toast.error("名称不能为空");
      return;
    }
    setBusy(true);
    try {
      await api.updateAgent(agentKey, draftName.trim(), draftDesc.trim());
      toast.success("已保存名称与描述");
      onOpenChange(false);
      onSaved();
    } catch (e) {
      toast.error(`保存失败：${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>编辑基本信息</DialogTitle>
          <DialogDescription>
            标识 key「{agentKey}」与角色创建后不可更改；名称与描述用于在列表与会话中识别。
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-4 py-2">
          <div className="grid gap-1.5">
            <Label htmlFor="agent-meta-name">名称</Label>
            <Input id="agent-meta-name" value={draftName} onChange={(e) => setDraftName(e.target.value)} />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="agent-meta-desc">描述</Label>
            <Textarea
              id="agent-meta-desc"
              rows={3}
              placeholder="一句话说明这个 Agent 是干什么的"
              value={draftDesc}
              onChange={(e) => setDraftDesc(e.target.value)}
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy}>
            取消
          </Button>
          <Button onClick={() => void save()} disabled={busy || !draftName.trim()}>
            <SaveIcon /> 保存
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// MetaItem 信息卡里的「标签 + 值」单元;值可以是徽标也可以是文本。
function MetaItem({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid gap-1">
      <span className="text-muted-foreground text-xs">{label}</span>
      <span className="text-sm">{children}</span>
    </div>
  );
}

function MissingAgent({ reason }: { reason: string }) {
  return (
    <div className="p-4 md:p-6">
      <div className="flex flex-col items-center gap-3 py-16 text-center text-muted-foreground text-sm">
        <BotIcon className="size-8 opacity-40" />
        <p>{reason}</p>
        <Button asChild variant="outline" size="sm">
          <Link href={LIST_HREF}>
            <ArrowLeftIcon className="size-4" />
            返回列表
          </Link>
        </Button>
      </div>
    </div>
  );
}

function AgentDetailInner() {
  const searchParams = useSearchParams();
  const router = useRouter();
  const key = (searchParams.get("key") ?? "").trim();

  const [detail, setDetail] = React.useState<AgentDetail | null>(null);
  const [tools, setTools] = React.useState<Tool[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [missing, setMissing] = React.useState(false);
  const [metaOpen, setMetaOpen] = React.useState(false);

  const load = React.useCallback(() => {
    if (!key) {
      setLoading(false);
      setMissing(true);
      return;
    }
    setLoading(true);
    api
      .getAgent(key)
      .then((d) => {
        setDetail(d);
        setMissing(false);
      })
      .catch(() => {
        setDetail(null);
        setMissing(true);
      })
      .finally(() => setLoading(false));
  }, [key]);
  React.useEffect(() => {
    load();
  }, [load]);
  // 工具绑定数不在 agent 详情里返回,用工具目录反查(失败则显示 0,不打断页面)。
  React.useEffect(() => {
    api
      .tools()
      .then(setTools)
      .catch(() => setTools([]));
  }, []);

  const agent = detail?.agent;

  const remove = async () => {
    if (!agent) return;
    try {
      await api.deleteAgent(agent.key);
      toast.success(`已删除 Agent「${agent.name}」`);
      router.push(LIST_HREF);
    } catch (e) {
      toast.error(`删除失败：${(e as Error).message}`);
    }
  };

  if (!key) {
    return <MissingAgent reason="未指定 Agent（缺少 ?key= 参数）。请从「Agent」列表进入。" />;
  }
  if (loading) {
    return (
      <div className="p-4 md:p-6">
        <p className="py-16 text-center text-muted-foreground text-sm">加载中…</p>
      </div>
    );
  }
  if (missing || !agent) {
    return <MissingAgent reason={`未找到 Agent「${key}」，它可能已被删除。`} />;
  }

  const profileName =
    agent.llm_profile_id == null
      ? ""
      : (detail?.llm_profiles?.find((p) => String(p.id) === String(agent.llm_profile_id))?.name ??
        `#${agent.llm_profile_id}`);
  const toolCount = tools.filter((t) => t.agents.includes(agent.key)).length;

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div className="flex min-w-0 items-start gap-2">
          <Button asChild variant="ghost" size="icon-sm" className="mt-0.5">
            <Link href={LIST_HREF} aria-label="返回 Agent 列表">
              <ArrowLeftIcon className="size-4" />
            </Link>
          </Button>
          <div className="min-w-0">
            <h1 className="truncate font-semibold text-xl" title={agent.name}>
              {agent.name}
            </h1>
            <p className="mt-0.5 font-mono text-muted-foreground text-xs">{agent.key}</p>
            <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
              <StageBadge stage={agent.stage} />
              <OriginBadge builtin={agent.builtin} />
              <EnabledBadge enabled={agent.enabled} />
            </div>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {/* 内置 Agent 的名称/描述不可改(后端 pgUpdateAgent 会拒绝),故不给编辑入口。 */}
          {!agent.builtin && (
            <Button variant="outline" size="sm" onClick={() => setMetaOpen(true)}>
              <PencilIcon className="size-4" />
              编辑信息
            </Button>
          )}
          {!agent.builtin && (
            <AlertDialog>
              <AlertDialogTrigger asChild>
                <Button variant="outline" size="sm">
                  <Trash2Icon className="size-4" />
                  删除
                </Button>
              </AlertDialogTrigger>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>删除 Agent「{agent.name}」？</AlertDialogTitle>
                  <AlertDialogDescription>
                    将一并删除它的提示词、变量、可见性、触发器与工具绑定。此操作不可撤销。
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>取消</AlertDialogCancel>
                  <AlertDialogAction onClick={() => void remove()}>删除</AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          )}
        </div>
      </div>

      <Card className="mb-4">
        <CardHeader>
          <CardTitle>基本信息</CardTitle>
          <CardDescription className="truncate" title={agent.description || undefined}>
            {agent.description || "（无描述）"}
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          <MetaItem label="标识 key">
            <span className="font-mono text-xs">{agent.key}</span>
          </MetaItem>
          <MetaItem label="角色">
            <span className="font-mono text-xs">{agent.role || "—"}</span>
          </MetaItem>
          <MetaItem label="归属阶段">
            <span>{stageLabel(agent.stage ?? "")}</span>
          </MetaItem>
          <MetaItem label="模型绑定">
            {agent.llm_profile_id == null ? (
              <span className="text-muted-foreground">跟随任务 / 全局激活配置</span>
            ) : (
              <span className="truncate" title={profileName}>
                {profileName}
              </span>
            )}
          </MetaItem>
          <MetaItem label="绑定工具">
            <span className="tabular-nums">{toolCount}</span>
          </MetaItem>
          <MetaItem label="可见 MCP">
            <span className="tabular-nums">{detail?.visibility?.mcp?.length ?? 0}</span>
          </MetaItem>
          <MetaItem label="可见 Skill">
            <span className="tabular-nums">{detail?.visibility?.skill?.length ?? 0}</span>
          </MetaItem>
          <MetaItem label="提示词版本">
            <span className="tabular-nums">{detail?.versions?.length ?? 0}</span>
          </MetaItem>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>编辑 Agent</CardTitle>
          <CardDescription>
            按页签分别编辑配置与提示词、收尾提示词、MCP / Skill 可见性、工具绑定与触发器；保存即时生效。
          </CardDescription>
        </CardHeader>
        <CardContent className="px-0 pb-0">
          <div className="flex min-h-[30rem] flex-col border-t">
            <AgentEditor agentKey={agent.key} onSaved={load} />
          </div>
        </CardContent>
      </Card>

      <EditMetaDialog
        open={metaOpen}
        onOpenChange={setMetaOpen}
        agentKey={agent.key}
        name={agent.name}
        description={agent.description ?? ""}
        onSaved={load}
      />
    </div>
  );
}

// useSearchParams must sit under a Suspense boundary for static export.
export default function AgentDetailPage() {
  return (
    <React.Suspense fallback={null}>
      <AgentDetailInner />
    </React.Suspense>
  );
}
