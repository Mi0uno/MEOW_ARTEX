"use client";

// Skill 详情页：文件管理 / 元数据 / 可见性 / 调用记录。
//
// 列表页点技能名进入这里（?name=<skill name>）。改造前这些能力挤在列表页右侧面板里，
// 一个技能既看不到全部字段、也没法把链接发给别人。现在按能力分页签，每块都有完整的
// 读写路径。名称是目录名，后端没有 rename 端点，所以这里不提供重命名。

import * as React from "react";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";

import { ArrowLeftIcon, HistoryIcon, PackageIcon, PencilIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { Agent, MCPServer, SkillCall, SkillItem } from "@/lib/types";

import { SkillDeleteDialog } from "../_components/skill-dialogs";
import { SkillFileManager } from "../_components/skill-file-manager";
import { fmtFullTime, fmtTime, fileCount } from "../_components/format";

export default function SkillDetailPage() {
  return (
    <React.Suspense fallback={null}>
      <SkillDetailInner />
    </React.Suspense>
  );
}

function SkillDetailInner() {
  const searchParams = useSearchParams();
  const router = useRouter();
  const name = searchParams.get("name") ?? "";

  const [skill, setSkill] = React.useState<SkillItem | null>(null);
  const [loading, setLoading] = React.useState(true);
  const [notFound, setNotFound] = React.useState(false);
  const [tab, setTab] = React.useState("files");

  const [agents, setAgents] = React.useState<Agent[]>([]);
  const [mcpOptions, setMcpOptions] = React.useState<MCPServer[]>([]);
  const [visibleIds, setVisibleIds] = React.useState<string[]>([]);
  const [deleteOpen, setDeleteOpen] = React.useState(false);

  // 技能本体：列表接口带全部元数据与调用统计，按 name 找一条即可，不必单独加详情接口。
  const loadSkill = React.useCallback(() => {
    if (!name) {
      setLoading(false);
      setNotFound(true);
      return;
    }
    setLoading(true);
    api
      .skills()
      .then((all) => {
        const found = all.find((s) => s.name === name) ?? null;
        setSkill(found);
        setNotFound(!found);
      })
      .catch((e) => {
        toast.error(`加载失败：${(e as Error).message}`);
        setNotFound(true);
      })
      .finally(() => setLoading(false));
  }, [name]);

  const loadVisibility = React.useCallback(() => {
    if (!name) return;
    api
      .skillVisibility(name)
      .then(setVisibleIds)
      .catch(() => setVisibleIds([]));
  }, [name]);

  React.useEffect(() => {
    loadSkill();
  }, [loadSkill]);

  React.useEffect(() => {
    loadVisibility();
  }, [loadVisibility]);

  React.useEffect(() => {
    api.agents().then(setAgents).catch(() => {});
    api.mcpServers().then(setMcpOptions).catch(() => {});
  }, []);

  async function toggleVisibility(agent: Agent, on: boolean) {
    try {
      await api.toggleSkillVisibility(agent.id, name, !on);
      toast.success(`${on ? "取消" : "授予"}「${agent.name}」可见`);
      loadVisibility();
    } catch (e) {
      toast.error("操作失败：" + (e as Error).message);
    }
  }

  async function deleteSkill() {
    try {
      await api.deleteSkill(name);
      toast.success(`已删除 Skill：${name}`);
      router.push("/system/skills");
    } catch (e) {
      toast.error("删除失败：" + (e as Error).message);
    }
  }

  // ── 加载中 / 未找到 ────────────────────────────────────────────────────────
  if (loading) {
    return <div className="text-muted-foreground p-4 text-sm md:p-6">加载中…</div>;
  }

  if (notFound || !skill) {
    return (
      <div className="flex flex-col items-center justify-center gap-3 p-4 py-24 text-center md:p-6">
        <PackageIcon className="text-muted-foreground size-8 opacity-40" />
        <p className="text-sm font-medium">{name ? `Skill「${name}」不存在` : "缺少 name 参数"}</p>
        <p className="text-muted-foreground max-w-md text-sm">
          {name
            ? "它可能已被删除，或名称拼写有误（技能名即目录名，区分大小写）。"
            : "请从技能库列表点某个技能名进入，或确认链接里带有 ?name= 参数。"}
        </p>
        <Button variant="outline" size="sm" asChild>
          <Link href="/system/skills">
            <ArrowLeftIcon className="size-4" />
            返回列表
          </Link>
        </Button>
      </div>
    );
  }

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="mb-1 flex items-center gap-2">
            <Button variant="ghost" size="sm" asChild>
              <Link href="/system/skills">
                <ArrowLeftIcon className="size-4" />
                技能库
              </Link>
            </Button>
          </div>
          <h1 className="truncate font-mono font-semibold text-xl" title={skill.name}>
            {skill.name}
          </h1>
          <p className="text-muted-foreground text-sm">
            {fileCount(skill)} 个文件 · 累计调用 {skill.calls} 次 · 最近 {fmtTime(skill.last_used)}
            {skill.license ? ` · ${skill.license}` : ""}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          {/* 元数据是详情页最常改的东西，头部留一个直达入口，省得先切页签再找保存。 */}
          <Button variant="ghost" size="sm" onClick={() => setTab("meta")}>
            <PencilIcon className="size-4" />
            编辑元数据
          </Button>
          <Button variant="ghost" size="sm" onClick={() => setDeleteOpen(true)}>
            <Trash2Icon className="size-4 text-red-600" />
            删除
          </Button>
        </div>
      </div>

      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="files">文件 {fileCount(skill)}</TabsTrigger>
          <TabsTrigger value="meta">元数据</TabsTrigger>
          <TabsTrigger value="visibility">可见性 {visibleIds.length}</TabsTrigger>
          <TabsTrigger value="usage">调用记录</TabsTrigger>
        </TabsList>

        {/* ── 文件 ── */}
        {/* forceMount：Radix 默认会卸载非活动页签，切走再切回会丢掉文件树展开状态、
            选中文件与未保存的编辑内容。这里保持挂载、用 hidden 隐藏（同 Sheet 内 Tabs 的写法）。 */}
        <TabsContent value="files" forceMount className="mt-3 data-[state=inactive]:hidden">
          <Card>
            <CardHeader className="flex-row items-center justify-between">
              <CardTitle className="text-sm">文件管理</CardTitle>
              <span className="text-muted-foreground text-xs">SKILL.md 是入口；脚本放 scripts/、参考资料放 references/</span>
            </CardHeader>
            <CardContent>
              <SkillFileManager skill={skill.name} files={skill.files} onChanged={loadSkill} />
            </CardContent>
          </Card>
        </TabsContent>

        {/* ── 元数据 ── */}
        <TabsContent value="meta" forceMount className="mt-3 data-[state=inactive]:hidden">
          <MetaTab skill={skill} mcpOptions={mcpOptions} onSaved={loadSkill} />
        </TabsContent>

        {/* ── 可见性 ── */}
        <TabsContent value="visibility" forceMount className="mt-3 data-[state=inactive]:hidden">
          <Card>
            <CardHeader>
              <CardTitle className="text-sm">可见性（按 Agent 授权）</CardTitle>
            </CardHeader>
            <CardContent className="space-y-3">
              <p className="text-muted-foreground text-xs">
                只有被授权的 agent 才能在自己的工具集里看到并加载这个 Skill。改动即时生效。
              </p>
              {agents.length === 0 ? (
                <p className="text-muted-foreground text-sm">（暂无 Agent）</p>
              ) : (
                <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
                  {agents.map((a) => {
                    const on = visibleIds.includes(a.id);
                    return (
                      <label
                        key={a.id}
                        className="hover:bg-muted/50 flex cursor-pointer items-center gap-2 rounded-md border px-3 py-2 text-sm"
                      >
                        <Checkbox checked={on} onCheckedChange={() => void toggleVisibility(a, on)} />
                        <span className="min-w-0 flex-1 truncate" title={a.name}>
                          {a.name}
                        </span>
                        <Badge variant="outline" className="shrink-0 font-mono text-[10px] font-normal">
                          {a.key}
                        </Badge>
                      </label>
                    );
                  })}
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        {/* ── 调用记录 ── */}
        {/* 这个页签没有本地编辑态，不 forceMount —— 否则进页面就会为一份可能没人看的列表发请求。 */}
        <TabsContent value="usage" className="mt-3">
          <UsageTab skill={skill} />
        </TabsContent>
      </Tabs>

      <SkillDeleteDialog skillName={name} open={deleteOpen} onOpenChange={setDeleteOpen} onConfirm={deleteSkill} />
    </div>
  );
}

// ── 元数据页签 ───────────────────────────────────────────────────────────────
// 后端 PUT /skills/{name}/meta 只接受 description / license / compatibility / mcps，
// 名称是目录名不可改，所以这四个字段就是全部可编辑面。
function MetaTab({
  skill,
  mcpOptions,
  onSaved,
}: {
  skill: SkillItem;
  mcpOptions: MCPServer[];
  onSaved: () => void;
}) {
  const [desc, setDesc] = React.useState(skill.description ?? "");
  const [license, setLicense] = React.useState(skill.license ?? "");
  const [compat, setCompat] = React.useState(skill.compatibility ?? "");
  const [mcps, setMcps] = React.useState<string[]>(skill.mcps ?? []);
  const [saving, setSaving] = React.useState(false);

  // 技能被重新加载（例如文件页签保存后）时，以服务端值为准重置表单。
  React.useEffect(() => {
    setDesc(skill.description ?? "");
    setLicense(skill.license ?? "");
    setCompat(skill.compatibility ?? "");
    setMcps(skill.mcps ?? []);
  }, [skill]);

  const dirty =
    desc !== (skill.description ?? "") ||
    license !== (skill.license ?? "") ||
    compat !== (skill.compatibility ?? "") ||
    JSON.stringify([...mcps].sort()) !== JSON.stringify([...(skill.mcps ?? [])].sort());

  async function save() {
    if (!desc.trim()) {
      toast.error("description 为必填项");
      return;
    }
    setSaving(true);
    try {
      await api.updateSkillMeta(skill.name, {
        description: desc.trim(),
        license: license.trim(),
        compatibility: compat.trim(),
        mcps,
      });
      toast.success("已保存元数据");
      onSaved();
    } catch (e) {
      toast.error("保存失败：" + (e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between">
        <CardTitle className="text-sm">元数据</CardTitle>
        <Button size="sm" onClick={() => void save()} disabled={!dirty || saving}>
          <PencilIcon className="size-3.5" />
          {saving ? "保存中…" : "保存"}
        </Button>
      </CardHeader>
      <CardContent className="grid gap-4">
        <div className="grid gap-1.5">
          <Label className="text-muted-foreground text-xs">名称（目录名，不可修改）</Label>
          <Input value={skill.name} disabled className="font-mono" />
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor="d-desc">
            描述 <span className="text-destructive">*</span>
          </Label>
          <Textarea id="d-desc" rows={3} className="resize-none" value={desc} onChange={(e) => setDesc(e.target.value)} />
          <p className="text-muted-foreground text-xs">写进 SKILL.md frontmatter，agent 靠它判断何时加载。</p>
        </div>
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="grid gap-1.5">
            <Label className="text-muted-foreground text-xs">license</Label>
            <Input value={license} onChange={(e) => setLicense(e.target.value)} placeholder="MIT / Proprietary" />
          </div>
          <div className="grid gap-1.5">
            <Label className="text-muted-foreground text-xs">compatibility</Label>
            <Input value={compat} onChange={(e) => setCompat(e.target.value)} placeholder="需要 sqlmap、python3" />
          </div>
        </div>
        <div className="grid gap-2">
          <Label className="text-muted-foreground text-xs">关联 MCP（加载 Skill 时才披露/解锁其工具）</Label>
          {mcpOptions.length === 0 ? (
            <p className="text-muted-foreground text-xs">暂无 MCP，可在「MCP」页添加。</p>
          ) : (
            <div className="flex flex-wrap gap-x-4 gap-y-2">
              {mcpOptions.map((m) => (
                <label key={m.id} className="flex cursor-pointer items-center gap-2 text-sm">
                  <Checkbox
                    checked={mcps.includes(m.name)}
                    onCheckedChange={(on) => setMcps((cur) => (on ? [...cur, m.name] : cur.filter((n) => n !== m.name)))}
                  />
                  {m.name}
                </label>
              ))}
            </div>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

// ── 调用记录页签 ─────────────────────────────────────────────────────────────
function UsageTab({ skill }: { skill: SkillItem }) {
  const [calls, setCalls] = React.useState<SkillCall[]>([]);
  const [loading, setLoading] = React.useState(true);

  React.useEffect(() => {
    setLoading(true);
    api
      .skillUsage(skill.name, 50)
      .then(setCalls)
      .catch(() => setCalls([]))
      .finally(() => setLoading(false));
  }, [skill.name]);

  return (
    <div className="grid gap-4 lg:grid-cols-[20rem_1fr] lg:items-start">
      <Card>
        <CardHeader>
          <CardTitle className="text-sm">调用统计</CardTitle>
        </CardHeader>
        <CardContent className="grid grid-cols-2 gap-2">
          <div className="rounded-md border p-2">
            <p className="text-lg font-semibold tabular-nums">{skill.calls}</p>
            <p className="text-muted-foreground text-xs">总调用次数</p>
          </div>
          <div className="rounded-md border p-2">
            <p className="text-lg font-semibold tabular-nums">{skill.tasks}</p>
            <p className="text-muted-foreground text-xs">覆盖任务数</p>
          </div>
          <div className="col-span-2 rounded-md border p-2">
            <p className="truncate text-sm font-medium" title={fmtFullTime(skill.last_used)}>
              {fmtFullTime(skill.last_used)}
            </p>
            <p className="text-muted-foreground text-xs">最近调用</p>
          </div>
          {skill.usage_agents.length > 0 && (
            <div className="col-span-2 flex flex-wrap items-center gap-1">
              <span className="text-muted-foreground text-xs">调用方：</span>
              {skill.usage_agents.map((k) => (
                <Badge key={k} variant="secondary" className="text-xs font-normal">
                  {k}
                </Badge>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="flex-row items-center gap-2">
          <HistoryIcon className="text-muted-foreground size-4" />
          <CardTitle className="text-sm">最近调用</CardTitle>
          {calls.length > 0 && <span className="text-muted-foreground text-xs tabular-nums">{calls.length} 条</span>}
        </CardHeader>
        <CardContent className="px-0">
          {loading ? (
            <p className="text-muted-foreground px-6 pb-4 text-xs">加载中…</p>
          ) : calls.length === 0 ? (
            <p className="text-muted-foreground px-6 pb-4 text-sm">还没有调用记录。</p>
          ) : (
            <Table className="table-fixed">
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  {/* 时间列不设宽度（自适应余量），其余固定列之和 352px，远小于内容区宽度。 */}
                  <TableHead>时间</TableHead>
                  <TableHead className="w-40">调用方 Agent</TableHead>
                  <TableHead className="w-28">场景</TableHead>
                  <TableHead className="w-20">参数长度</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {calls.map((c) => (
                  <TableRow key={`${c.ts}-${c.agent_key}-${c.task_id}-${c.session_id}`}>
                    <TableCell className="text-muted-foreground min-w-0 truncate text-xs" title={fmtFullTime(c.ts)}>
                      {fmtFullTime(c.ts)}
                    </TableCell>
                    <TableCell className="min-w-0">
                      <span className="block truncate font-mono text-xs" title={c.agent_key || "—"}>
                        {c.agent_key || "—"}
                      </span>
                    </TableCell>
                    <TableCell className="text-muted-foreground text-xs">
                      {c.task_id > 0 ? `任务 #${c.task_id}` : c.session_id ? "对话会话" : "—"}
                    </TableCell>
                    <TableCell className="text-muted-foreground text-xs tabular-nums">{c.args_len}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
