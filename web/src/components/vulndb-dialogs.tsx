"use client";

// 漏洞库共用对话框:条目新建/编辑 + 详情(含 PoC 管理)。
// 「漏洞库」(0day/Nday) 与「CVE 情报」两个页面共用同一份,避免行为漂移。

import * as React from "react";

import { DownloadIcon, PencilIcon, PlusIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
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
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { VulnEntry, VulnPoc } from "@/lib/types";

// 分类与严重度的展示样式(两页共用,保持一致)。
export const CATEGORY_BADGE: Record<string, { label: string; badge: string }> = {
  "0day": { label: "0day", badge: "bg-red-500/15 text-red-600 border-red-500/30" },
  nday: { label: "Nday", badge: "bg-amber-500/15 text-amber-600 border-amber-500/30" },
  cve: { label: "CVE", badge: "bg-blue-500/15 text-blue-600 border-blue-500/30" },
};

const SEVERITY_BADGE: Record<string, string> = {
  critical: "bg-red-500/15 text-red-600 border-red-500/30",
  high: "bg-orange-500/15 text-orange-600 border-orange-500/30",
  medium: "bg-amber-500/15 text-amber-600 border-amber-500/30",
  low: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30",
};

// severityBadgeClass 返回严重度的徽标样式(未匹配时返回空串,不报错)。
export function severityBadgeClass(severity: string): string {
  return SEVERITY_BADGE[severity] ?? "";
}

// categoryBadgeClass 返回分类的徽标样式。
export function categoryBadgeClass(category: string): string {
  return (CATEGORY_BADGE[category] ?? CATEGORY_BADGE.nday).badge;
}

// PoC 来源的中文标签(详情对话框 PoC 列表用;未知来源回退显示原始值,两页共用)。
export const POC_SOURCE_LABEL: Record<string, string> = {
  manual: "手工",
  github: "GitHub",
  exploitdb: "Exploit-DB",
  pocorexp: "PocOrExp",
  nuclei: "nuclei 模板",
  ai_generated: "AI",
};

// VulnEntryDialog 新建/编辑漏洞条目。
export function VulnEntryDialog({
  entry,
  defaultCategory = "nday",
  open,
  onOpenChange,
  onSaved,
}: {
  entry: VulnEntry | null;
  // defaultCategory 新建时的默认分类(漏洞库页传 nday,CVE 页传 cve)。
  defaultCategory?: string;
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onSaved: () => void;
}) {
  const [form, setForm] = React.useState<Partial<VulnEntry>>({});
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    if (!open) return;
    if (entry) {
      api
        .vulnEntry(entry.id)
        .then((r) => setForm(r.entry))
        .catch(() => setForm(entry));
    } else {
      setForm({ category: defaultCategory, severity: "medium", status: "active", refs: [], tags: [] });
    }
  }, [open, entry, defaultCategory]);

  const set = (k: keyof VulnEntry, v: unknown) => setForm((f) => ({ ...f, [k]: v }));

  const submit = async () => {
    if (!String(form.title ?? "").trim()) {
      toast.error("请填写标题");
      return;
    }
    if (form.category === "cve" && !String(form.cve_id ?? "").trim()) {
      toast.error("CVE 分类必须填写 CVE 编号");
      return;
    }
    setBusy(true);
    try {
      if (entry) await api.updateVulnEntry(entry.id, form);
      else await api.createVulnEntry(form);
      toast.success(entry ? "已保存" : "已新增");
      onSaved();
    } catch (e) {
      toast.error(`保存失败:${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[88vh] overflow-y-auto sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{entry ? "编辑漏洞条目" : "新增漏洞条目"}</DialogTitle>
          <DialogDescription>
            分类决定入库板块:0day(未公开自研) / Nday(已公开无 CVE) / CVE(有编号,必须填编号)
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid grid-cols-3 gap-3">
            <div className="grid gap-1.5">
              <Label>分类</Label>
              <Select value={String(form.category ?? defaultCategory)} onValueChange={(v) => set("category", v)}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    <SelectItem value="0day">0day</SelectItem>
                    <SelectItem value="nday">Nday</SelectItem>
                    <SelectItem value="cve">CVE</SelectItem>
                  </SelectGroup>
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label>严重度</Label>
              <Select value={String(form.severity ?? "medium")} onValueChange={(v) => set("severity", v)}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    <SelectItem value="critical">严重</SelectItem>
                    <SelectItem value="high">高</SelectItem>
                    <SelectItem value="medium">中</SelectItem>
                    <SelectItem value="low">低</SelectItem>
                  </SelectGroup>
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label>CVE 编号{form.category === "cve" ? "(必填)" : "(非 CVE 分类留空)"}</Label>
              <Input
                value={String(form.cve_id ?? "")}
                onChange={(e) => set("cve_id", e.target.value)}
                placeholder="CVE-2024-1234"
                className="font-mono"
              />
            </div>
          </div>
          <div className="grid gap-1.5">
            <Label>标题</Label>
            <Input value={String(form.title ?? "")} onChange={(e) => set("title", e.target.value)} />
          </div>
          <div className="grid grid-cols-3 gap-3">
            <div className="grid gap-1.5">
              <Label>厂商</Label>
              <Input value={String(form.vendor ?? "")} onChange={(e) => set("vendor", e.target.value)} />
            </div>
            <div className="grid gap-1.5">
              <Label>产品</Label>
              <Input value={String(form.product ?? "")} onChange={(e) => set("product", e.target.value)} />
            </div>
            <div className="grid gap-1.5">
              <Label>漏洞类别</Label>
              <Input
                value={String(form.vuln_class ?? "")}
                onChange={(e) => set("vuln_class", e.target.value)}
                placeholder="sql_injection / rce / ssrf"
              />
            </div>
          </div>
          <div className="grid gap-1.5">
            <Label>影响版本</Label>
            <Input value={String(form.affected ?? "")} onChange={(e) => set("affected", e.target.value)} />
          </div>
          <div className="grid gap-1.5">
            <Label>描述</Label>
            <Textarea
              value={String(form.description ?? "")}
              onChange={(e) => set("description", e.target.value)}
              rows={4}
            />
          </div>
          <div className="grid gap-1.5">
            <Label>利用指引(Markdown)</Label>
            <Textarea
              className="font-mono text-xs"
              value={String(form.exploitation ?? "")}
              onChange={(e) => set("exploitation", e.target.value)}
              rows={6}
              placeholder="复现步骤、payload、注意事项…"
            />
          </div>
          <div className="grid gap-1.5">
            <Label>修复建议</Label>
            <Textarea
              value={String(form.remediation ?? "")}
              onChange={(e) => set("remediation", e.target.value)}
              rows={3}
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button disabled={busy} onClick={() => void submit()}>
            {busy ? "保存中…" : "保存"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// VulnDetailDialog 漏洞详情 + PoC 管理(增删改查)。
export function VulnDetailDialog({
  entryID,
  onOpenChange,
  onChanged,
  onEdit,
}: {
  entryID: number | null;
  onOpenChange: (v: boolean) => void;
  onChanged: () => void;
  onEdit: (e: VulnEntry) => void;
}) {
  const [entry, setEntry] = React.useState<VulnEntry | null>(null);
  const [pocs, setPocs] = React.useState<VulnPoc[]>([]);
  const [editingPoc, setEditingPoc] = React.useState<VulnPoc | null>(null);
  const [adding, setAdding] = React.useState(false);

  const load = React.useCallback(() => {
    if (!entryID) return;
    api
      .vulnEntry(entryID)
      .then((r) => {
        setEntry(r.entry);
        setPocs(r.pocs);
      })
      .catch((e) => toast.error(`加载失败:${(e as Error).message}`));
  }, [entryID]);

  React.useEffect(() => {
    if (entryID) load();
    else {
      setEntry(null);
      setPocs([]);
    }
  }, [entryID, load]);

  const removePoc = async (p: VulnPoc) => {
    if (!window.confirm(`删除 PoC「${p.name}」?`)) return;
    try {
      await api.deleteVulnPoc(p.id);
      toast.success("已删除");
      load();
      onChanged();
    } catch (e) {
      toast.error(`删除失败:${(e as Error).message}`);
    }
  };

  if (!entryID) return null;

  return (
    <>
      <Dialog open onOpenChange={onOpenChange}>
        <DialogContent className="max-h-[88vh] overflow-y-auto sm:max-w-4xl">
          <DialogHeader>
            <DialogTitle className="flex flex-wrap items-center gap-2 break-words">
              {entry?.title}
              {entry && (
                <Badge variant="outline" className={categoryBadgeClass(entry.category)}>
                  {(CATEGORY_BADGE[entry.category] ?? CATEGORY_BADGE.nday).label}
                </Badge>
              )}
              {entry?.severity && (
                <Badge variant="outline" className={severityBadgeClass(entry.severity)}>
                  {entry.severity}
                </Badge>
              )}
              {entry?.kev && (
                <Badge
                  variant="outline"
                  className="border-red-500/30 bg-red-500/15 px-1.5 text-[10px] text-red-600"
                  title="CISA 已知被在野利用"
                >
                  KEV
                </Badge>
              )}
            </DialogTitle>
            <DialogDescription className="flex flex-wrap gap-x-3 gap-y-1 text-xs">
              {entry?.cve_id && <span className="font-mono">{entry.cve_id}</span>}
              {entry?.vendor && <span>厂商:{entry.vendor}</span>}
              {entry?.product && <span>产品:{entry.product}</span>}
              {entry?.affected && <span>影响版本:{entry.affected}</span>}
            </DialogDescription>
          </DialogHeader>

          {entry && (
            <div className="space-y-3 text-sm">
              {entry.description && (
                <div>
                  <p className="text-muted-foreground mb-1 text-xs font-medium">描述</p>
                  <p className="whitespace-pre-wrap break-words">{entry.description}</p>
                </div>
              )}
              {entry.exploitation && (
                <div>
                  <p className="text-muted-foreground mb-1 text-xs font-medium">利用指引</p>
                  <pre className="bg-muted overflow-x-auto rounded p-3 font-mono text-xs whitespace-pre-wrap break-all">
                    {entry.exploitation}
                  </pre>
                </div>
              )}
              {entry.remediation && (
                <div>
                  <p className="text-muted-foreground mb-1 text-xs font-medium">修复建议</p>
                  <p className="whitespace-pre-wrap break-words">{entry.remediation}</p>
                </div>
              )}
              {entry.refs?.length > 0 && (
                <div>
                  <p className="text-muted-foreground mb-1 text-xs font-medium">参考</p>
                  <ul className="space-y-0.5">
                    {entry.refs.map((u, i) => (
                      <li key={i}>
                        <a
                          href={u}
                          target="_blank"
                          rel="noreferrer"
                          className="text-blue-600 text-xs break-all hover:underline"
                        >
                          {u}
                        </a>
                      </li>
                    ))}
                  </ul>
                </div>
              )}

              <div className="border-t pt-3">
                <div className="mb-2 flex items-center justify-between">
                  <p className="text-xs font-medium">PoC / EXP({pocs.length})</p>
                  <Button size="sm" variant="outline" onClick={() => setAdding(true)}>
                    <PlusIcon className="size-3.5" />
                    添加 PoC
                  </Button>
                </div>
                {pocs.length === 0 ? (
                  <p className="text-muted-foreground py-3 text-center text-xs">
                    还没有 PoC。定时采集会自动发现公开 PoC,也可手动添加。
                  </p>
                ) : (
                  <div className="space-y-2">
                    {pocs.map((p) => (
                      <div key={p.id} className="rounded border p-2">
                        <div className="flex flex-wrap items-center gap-2">
                          <Badge variant="secondary" className="text-xs">
                            {p.kind}
                          </Badge>
                          <span className="min-w-0 flex-1 truncate text-xs font-medium" title={p.name}>
                            {p.name}
                          </span>
                          {p.language && <span className="text-muted-foreground text-xs">{p.language}</span>}
                          <Badge variant="outline" className="text-xs">
                            {POC_SOURCE_LABEL[p.source] ?? p.source}
                          </Badge>
                          <Button variant="ghost" size="sm" onClick={() => setEditingPoc(p)}>
                            <PencilIcon className="size-3.5" />
                          </Button>
                          <Button variant="ghost" size="sm" onClick={() => void removePoc(p)}>
                            <Trash2Icon className="text-muted-foreground size-3.5" />
                          </Button>
                        </div>
                        {p.content && (
                          <pre className="bg-muted mt-2 max-h-40 overflow-auto rounded p-2 font-mono text-xs whitespace-pre-wrap break-all">
                            {p.content}
                          </pre>
                        )}
                        {p.source_url && (
                          <a
                            href={p.source_url}
                            target="_blank"
                            rel="noreferrer"
                            className="text-blue-600 mt-1 inline-block text-xs break-all hover:underline"
                          >
                            <DownloadIcon className="mr-1 inline size-3" />
                            {p.source_url}
                          </a>
                        )}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            </div>
          )}

          <DialogFooter>
            <Button variant="outline" onClick={() => onOpenChange(false)}>
              关闭
            </Button>
            {entry && (
              <Button variant="outline" onClick={() => onEdit(entry)}>
                <PencilIcon className="size-4" />
                编辑条目
              </Button>
            )}
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <PocDialog
        entryID={entryID}
        poc={editingPoc}
        open={adding || !!editingPoc}
        onOpenChange={(o) => {
          if (!o) {
            setAdding(false);
            setEditingPoc(null);
          }
        }}
        onSaved={() => {
          setAdding(false);
          setEditingPoc(null);
          load();
          onChanged();
        }}
      />
    </>
  );
}

// PocDialog 新建/编辑 PoC。
function PocDialog({
  entryID,
  poc,
  open,
  onOpenChange,
  onSaved,
}: {
  entryID: number;
  poc: VulnPoc | null;
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onSaved: () => void;
}) {
  const [form, setForm] = React.useState<Partial<VulnPoc>>({});
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    if (!open) return;
    setForm(poc ?? { kind: "poc", language: "", content: "", source_url: "", verified: false });
  }, [open, poc]);

  const set = (k: keyof VulnPoc, v: unknown) => setForm((f) => ({ ...f, [k]: v }));

  const submit = async () => {
    setBusy(true);
    try {
      if (poc) await api.updateVulnPoc(poc.id, form);
      else await api.createVulnPoc(entryID, form);
      toast.success(poc ? "已保存" : "已添加");
      onSaved();
    } catch (e) {
      toast.error(`保存失败:${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{poc ? "编辑 PoC" : "添加 PoC"}</DialogTitle>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid grid-cols-3 gap-3">
            <div className="grid gap-1.5">
              <Label>名称</Label>
              <Input value={String(form.name ?? "")} onChange={(e) => set("name", e.target.value)} />
            </div>
            <div className="grid gap-1.5">
              <Label>类型</Label>
              <Select value={String(form.kind ?? "poc")} onValueChange={(v) => set("kind", v)}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    <SelectItem value="poc">poc(验证)</SelectItem>
                    <SelectItem value="exp">exp(利用)</SelectItem>
                    <SelectItem value="nuclei">nuclei</SelectItem>
                    <SelectItem value="script">script</SelectItem>
                  </SelectGroup>
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label>语言</Label>
              <Input
                value={String(form.language ?? "")}
                onChange={(e) => set("language", e.target.value)}
                placeholder="python / go / bash"
              />
            </div>
          </div>
          <div className="grid gap-1.5">
            <Label>来源 URL(可选)</Label>
            <Input
              value={String(form.source_url ?? "")}
              onChange={(e) => set("source_url", e.target.value)}
              placeholder="https://github.com/..."
            />
          </div>
          <div className="grid gap-1.5">
            <Label>PoC 内容</Label>
            <Textarea
              className="min-h-56 font-mono text-xs"
              value={String(form.content ?? "")}
              onChange={(e) => set("content", e.target.value)}
              placeholder="脚本 / 请求报文 / nuclei 模板…"
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button disabled={busy} onClick={() => void submit()}>
            {busy ? "保存中…" : "保存"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
