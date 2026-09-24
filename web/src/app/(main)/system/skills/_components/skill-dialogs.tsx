"use client";

// Skill 模块的对话框集合：新建 Skill（Sheet）、编辑元数据（Dialog）、删除确认（AlertDialog）。
//
// 三个对话框列表页与详情页都要用（列表页的「新建/编辑/删除」按钮，详情页头部的
// 「编辑元数据/删除」），所以集中在这里，由调用方传 open/onOpenChange 控制。

import * as React from "react";

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
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
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
import { Sheet, SheetContent, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { Agent, MCPServer, SkillItem } from "@/lib/types";

// ── 新建 Skill ────────────────────────────────────────────────────────────────
// 三个页签（基本信息 / 关联 MCP / 可见性）与原页面一致：创建时要一次性把 MCP 关联
// 和初始可见性都定好，否则用户建完还得进详情页再配一遍。
export function SkillCreateSheet({
  open,
  onOpenChange,
  agents,
  mcpOptions,
  onCreated,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  agents: Agent[];
  mcpOptions: MCPServer[];
  onCreated: () => void;
}) {
  const [name, setName] = React.useState("");
  const [desc, setDesc] = React.useState("");
  const [license, setLicense] = React.useState("");
  const [compat, setCompat] = React.useState("");
  const [inst, setInst] = React.useState("");
  const [mcps, setMcps] = React.useState<string[]>([]);
  const [visibility, setVisibility] = React.useState<string[]>([]);
  const [busy, setBusy] = React.useState(false);

  function reset() {
    setName("");
    setDesc("");
    setLicense("");
    setCompat("");
    setInst("");
    setMcps([]);
    setVisibility([]);
  }

  async function submit() {
    if (!name.trim()) {
      toast.error("请填写 name");
      return;
    }
    if (!desc.trim()) {
      toast.error("description 为必填项");
      return;
    }
    setBusy(true);
    try {
      const n = name.trim();
      await api.createSkill({
        name: n,
        description: desc.trim(),
        license: license.trim() || undefined,
        compatibility: compat.trim() || undefined,
        mcps: mcps.length ? mcps : undefined,
        instructions: inst.trim() || undefined,
      });
      // apply initial visibility (fire-and-forget per agent; best-effort)
      await Promise.all(visibility.map((id) => api.toggleSkillVisibility(id, n, true)));
      toast.success("已创建 Skill");
      reset();
      onOpenChange(false);
      onCreated();
    } catch (e) {
      toast.error("创建失败：" + (e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="flex w-full flex-col gap-0 data-[side=right]:sm:max-w-xl">
        <SheetHeader className="px-4 pt-4">
          <SheetTitle>新建 Skill</SheetTitle>
        </SheetHeader>
        <Tabs defaultValue="basic" className="flex min-h-0 flex-1 flex-col">
          <TabsList className="mx-4 mt-3 shrink-0 justify-start">
            <TabsTrigger value="basic">基本信息</TabsTrigger>
            <TabsTrigger value="mcp">
              关联 MCP
              {mcps.length > 0 && (
                <span className="bg-primary text-primary-foreground ml-1.5 rounded-full px-1.5 py-0.5 text-[10px] leading-none">
                  {mcps.length}
                </span>
              )}
            </TabsTrigger>
            <TabsTrigger value="visibility">
              可见性
              {visibility.length > 0 && (
                <span className="bg-primary text-primary-foreground ml-1.5 rounded-full px-1.5 py-0.5 text-[10px] leading-none">
                  {visibility.length}
                </span>
              )}
            </TabsTrigger>
          </TabsList>

          {/* 基本信息 */}
          <TabsContent
            value="basic"
            className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 pt-4 pb-4 data-[state=inactive]:hidden"
          >
            <div className="grid gap-1.5">
              <Label htmlFor="sk-name">
                名称 <span className="text-destructive">*</span>
              </Label>
              <Input
                id="sk-name"
                placeholder="sqli-deepdive"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
              <p className="text-muted-foreground text-xs">小写字母 / 数字 / 连字符，1–64 字符</p>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="sk-desc">
                描述 <span className="text-destructive">*</span>
              </Label>
              <Textarea
                id="sk-desc"
                rows={2}
                className="resize-none"
                placeholder="这个 skill 做什么、何时使用。"
                value={desc}
                onChange={(e) => setDesc(e.target.value)}
              />
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div className="grid gap-1.5">
                <Label className="text-muted-foreground text-xs">license</Label>
                <Input placeholder="MIT / Proprietary" value={license} onChange={(e) => setLicense(e.target.value)} />
              </div>
              <div className="grid gap-1.5">
                <Label className="text-muted-foreground text-xs">compatibility</Label>
                <Input
                  placeholder="需要 sqlmap、python3"
                  value={compat}
                  onChange={(e) => setCompat(e.target.value)}
                />
              </div>
            </div>
            <div className="flex min-h-0 flex-1 flex-col gap-1.5">
              <Label htmlFor="sk-inst">
                正文 <span className="text-muted-foreground text-xs font-normal">（留空自动生成骨架）</span>
              </Label>
              <Textarea
                id="sk-inst"
                className="min-h-40 flex-1 resize-none font-mono text-sm leading-relaxed"
                placeholder={"## 执行方法\n\n1. 先探测错误\n2. 区分盲注类型\n\n脚本放 scripts/ 目录。"}
                value={inst}
                onChange={(e) => setInst(e.target.value)}
              />
            </div>
          </TabsContent>

          {/* 关联 MCP */}
          <TabsContent value="mcp" className="overflow-y-auto px-4 pt-4 pb-4 data-[state=inactive]:hidden">
            <p className="text-muted-foreground mb-3 text-xs">加载 Skill 时才披露并解锁所选 MCP 的工具。</p>
            {mcpOptions.length === 0 ? (
              <p className="text-muted-foreground text-xs">暂无 MCP，可在「MCP」页添加。</p>
            ) : (
              <div className="flex flex-col gap-2">
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
          </TabsContent>

          {/* 可见性 */}
          <TabsContent value="visibility" className="overflow-y-auto px-4 pt-4 pb-4 data-[state=inactive]:hidden">
            <p className="text-muted-foreground mb-3 text-xs">选中的 Agent 创建后即可见此 Skill。</p>
            {agents.length === 0 ? (
              <p className="text-muted-foreground text-xs">（暂无 Agent）</p>
            ) : (
              <div className="flex flex-col gap-2">
                {agents.map((a) => (
                  <label key={a.key} className="flex cursor-pointer items-center gap-2 text-sm">
                    <Checkbox
                      checked={visibility.includes(a.id)}
                      onCheckedChange={(on) =>
                        setVisibility((cur) => (on ? [...cur, a.id] : cur.filter((id) => id !== a.id)))
                      }
                    />
                    {a.name}
                  </label>
                ))}
              </div>
            )}
          </TabsContent>
        </Tabs>

        <SheetFooter className="flex-row justify-end gap-2 border-t px-4 py-3">
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button onClick={() => void submit()} disabled={busy}>
            {busy ? "创建中…" : "创建"}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  );
}

// ── 编辑元数据 ────────────────────────────────────────────────────────────────
// 后端的 PUT /skills/{name}/meta 只接受 mcps / description / license / compatibility
// 四个字段（名称是目录名，改不了），所以这里也只暴露这四个 —— 少给一个字段比给了存不上好。
export function SkillMetaDialog({
  skill,
  open,
  onOpenChange,
  mcpOptions,
  onSaved,
}: {
  skill: SkillItem | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  mcpOptions: MCPServer[];
  onSaved: () => void;
}) {
  const [desc, setDesc] = React.useState("");
  const [license, setLicense] = React.useState("");
  const [compat, setCompat] = React.useState("");
  const [mcps, setMcps] = React.useState<string[]>([]);
  const [busy, setBusy] = React.useState(false);

  // 每次打开都从传入的 skill 重新灌一次，避免上一次编辑的残留值串到下一个 skill。
  React.useEffect(() => {
    if (!open || !skill) return;
    setDesc(skill.description ?? "");
    setLicense(skill.license ?? "");
    setCompat(skill.compatibility ?? "");
    setMcps(skill.mcps ?? []);
  }, [open, skill]);

  async function submit() {
    if (!skill) return;
    if (!desc.trim()) {
      toast.error("description 为必填项");
      return;
    }
    setBusy(true);
    try {
      await api.updateSkillMeta(skill.name, {
        description: desc.trim(),
        license: license.trim(),
        compatibility: compat.trim(),
        mcps,
      });
      toast.success("已保存元数据");
      onOpenChange(false);
      onSaved();
    } catch (e) {
      toast.error("保存失败：" + (e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>编辑元数据</DialogTitle>
          <DialogDescription className="font-mono">{skill?.name ?? ""}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-4">
          <div className="grid gap-1.5">
            <Label htmlFor="meta-desc">
              描述 <span className="text-destructive">*</span>
            </Label>
            <Textarea
              id="meta-desc"
              rows={3}
              className="resize-none"
              value={desc}
              onChange={(e) => setDesc(e.target.value)}
            />
            <p className="text-muted-foreground text-xs">写进 SKILL.md frontmatter 的 description，agent 靠它判断何时加载。</p>
          </div>
          <div className="grid grid-cols-2 gap-3">
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
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button onClick={() => void submit()} disabled={busy}>
            {busy ? "保存中…" : "保存"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ── 删除 Skill 确认 ───────────────────────────────────────────────────────────
export function SkillDeleteDialog({
  skillName,
  open,
  onOpenChange,
  onConfirm,
}: {
  skillName: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onConfirm: () => Promise<void>;
}) {
  const [busy, setBusy] = React.useState(false);
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>删除 Skill「{skillName}」？</AlertDialogTitle>
          <AlertDialogDescription>
            将删除该 Skill 的全部文件、MCP 关联与可见性配置。此操作不可撤销。
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>取消</AlertDialogCancel>
          <AlertDialogAction
            disabled={busy}
            onClick={(e) => {
              e.preventDefault();
              setBusy(true);
              void onConfirm().finally(() => {
                setBusy(false);
                onOpenChange(false);
              });
            }}
          >
            {busy ? "删除中…" : "删除"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
