"use client";

// 自进化 · 剧本页签:攻击剧本(前置条件 → 步骤 → 成败计数 → 证据任务)。
// 状态页签(带计数)+ 搜索 + 分页;行内「详情」弹层展示结构化步骤表;
// 人工闸门与洞察一致:candidate → 转正/否决,active → 退役,任何状态可删除。
// 新增剧本用「一行一步」的简单文本框,前端解析成 [{n, action}] 步骤数组。

import * as React from "react";

import { ArchiveIcon, CheckIcon, EyeIcon, PlusIcon, SearchIcon, Trash2Icon, XIcon } from "lucide-react";
import { toast } from "sonner";

import { TablePagination } from "@/components/table-pagination";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
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
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { EvolvePlaybook, EvolveStatus, EvolveStatusCounts } from "@/lib/types";
import {
  EvolveStatusBadge,
  PillTab,
  STATUS_ACTION_LABELS,
  STATUS_TABS,
  ScopeFields,
  statusCount,
  useCompanies,
  type EvolveTabStatus,
} from "./evolve-shared";

function fmtTimeShort(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

// 平均耗时:秒 → 人读(m 分 s 秒);0/非法 = 未统计。
function fmtSeconds(s: number): string {
  if (!Number.isFinite(s) || s <= 0) return "—";
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  const r = Math.round(s % 60);
  return r ? `${m}m${r}s` : `${m}m`;
}

// 键值对渲染(preconditions 是自由 JSONB)。
function preconditionsEntries(p: Record<string, unknown> | undefined): [string, string][] {
  if (!p) return [];
  return Object.entries(p).map(([k, v]) => [k, typeof v === "string" ? v : JSON.stringify(v)]);
}

function taskIdsText(ids: number[]): string {
  return (ids ?? []).map((id) => `#${id}`).join(" ");
}

export function PlaybooksTab({
  counts,
  onChanged,
}: {
  counts: EvolveStatusCounts | null;
  onChanged?: () => void;
}) {
  const [playbooks, setPlaybooks] = React.useState<EvolvePlaybook[]>([]);
  const [total, setTotal] = React.useState(0);
  const [tabStatus, setTabStatus] = React.useState<EvolveTabStatus>("all");
  const [query, setQuery] = React.useState("");
  const [queryApplied, setQueryApplied] = React.useState("");
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(20);
  const [loading, setLoading] = React.useState(true);
  const [detail, setDetail] = React.useState<EvolvePlaybook | null>(null);
  // 新增剧本表单。
  const [createOpen, setCreateOpen] = React.useState(false);
  const [formTitle, setFormTitle] = React.useState("");
  const [formSummary, setFormSummary] = React.useState("");
  const [formSteps, setFormSteps] = React.useState("");
  const [formScopeKind, setFormScopeKind] = React.useState<"global" | "company">("global");
  const [formScopeId, setFormScopeId] = React.useState(0);
  const [saving, setSaving] = React.useState(false);
  const companies = useCompanies();

  const load = React.useCallback((st: string, q: string, p: number, size: number) => {
    setLoading(true);
    api
      .evolvePlaybooks({
        status: st === "all" ? "" : st,
        q,
        limit: size,
        offset: (p - 1) * size,
      })
      .then((r) => {
        setPlaybooks(r.playbooks);
        setTotal(r.total);
      })
      .catch((e) => toast.error(`加载剧本失败:${(e as Error).message}`))
      .finally(() => setLoading(false));
  }, []);

  React.useEffect(() => {
    load(tabStatus, queryApplied, page, pageSize);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tabStatus, queryApplied, page, pageSize, load]);

  const reload = () => {
    load(tabStatus, queryApplied, page, pageSize);
    onChanged?.();
  };

  const search = () => {
    setPage(1);
    setQueryApplied(query.trim());
  };

  const setStatus = async (row: EvolvePlaybook, status: EvolveStatus) => {
    try {
      await api.evolvePlaybookStatus(row.id, status);
      toast.success(`已${STATUS_ACTION_LABELS[status]}:「${row.title.slice(0, 24)}」`);
      reload();
    } catch (e) {
      toast.error(`操作失败:${(e as Error).message}`);
    }
  };

  const remove = async (row: EvolvePlaybook) => {
    if (!window.confirm(`删除剧本「${row.title.slice(0, 40)}」?该操作不可恢复。`)) return;
    try {
      await api.evolveDeletePlaybook(row.id);
      toast.success("已删除");
      reload();
    } catch (e) {
      toast.error(`删除失败:${(e as Error).message}`);
    }
  };

  const create = async () => {
    const title = formTitle.trim();
    if (!title) return;
    if (formScopeKind === "company" && !formScopeId) {
      toast.warning("请选择归属企业");
      return;
    }
    // 「一行一步」→ [{n, action}]。
    const steps = formSteps
      .split("\n")
      .map((l) => l.trim())
      .filter(Boolean)
      .map((action, idx) => ({ n: idx + 1, action }));
    if (steps.length === 0) {
      toast.warning("请至少写一步(一行一步)");
      return;
    }
    setSaving(true);
    try {
      await api.evolveCreatePlaybook({
        title,
        summary: formSummary.trim() || undefined,
        scope_kind: formScopeKind,
        scope_id: formScopeId || undefined,
        steps,
      });
      toast.success("已新增剧本(待验证,不注入任务;转正后生效)");
      setCreateOpen(false);
      setFormTitle("");
      setFormSummary("");
      setFormSteps("");
      setFormScopeKind("global");
      setFormScopeId(0);
      setPage(1);
      onChanged?.();
      load(tabStatus, queryApplied, 1, pageSize);
    } catch (e) {
      toast.error(`保存失败:${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  };

  const detailSteps = detail?.steps ?? [];
  const detailPreconditions = preconditionsEntries(detail?.preconditions);
  const detailEvidence = detail?.evidence_task_ids ?? [];

  return (
    <Card>
      <CardContent className="px-0 pt-4">
        {/* 状态页签(带计数)+ 工具栏。 */}
        <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
          {STATUS_TABS.map((s) => (
            <PillTab
              key={s.key}
              active={tabStatus === s.key}
              count={statusCount(counts, s.key)}
              onClick={() => {
                setPage(1);
                setTabStatus(s.key);
              }}
            >
              {s.label}
            </PillTab>
          ))}
        </div>
        <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
          <div className="relative w-full sm:max-w-xs">
            <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && search()}
              placeholder="搜索标题 / 摘要"
              className="pl-8"
            />
          </div>
          <Button variant="outline" size="sm" onClick={search}>
            搜索
          </Button>
          <span className="text-muted-foreground ml-auto text-xs tabular-nums">共 {total} 条</span>
          <Button size="sm" onClick={() => setCreateOpen(true)}>
            <PlusIcon className="size-4" />
            新增剧本
          </Button>
        </div>

        {loading && playbooks.length === 0 ? (
          <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
        ) : playbooks.length === 0 ? (
          <p className="text-muted-foreground py-16 text-center text-sm">
            {queryApplied
              ? "没有匹配的剧本"
              : "还没有攻击剧本。任务复盘会把可复用的打法沉淀成结构化步骤(待验证),转正后供后续任务参考。"}
          </p>
        ) : (
          <>
            <Table className="table-fixed text-xs">
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  {/* 标题列不设宽度:table-fixed 下它吃掉其余列之外的余量。 */}
                  <TableHead>标题</TableHead>
                  <TableHead className="w-16">状态</TableHead>
                  <TableHead className="w-14">步骤</TableHead>
                  <TableHead className="w-16">成/败</TableHead>
                  <TableHead className="w-20">平均耗时</TableHead>
                  <TableHead className="w-20">信任</TableHead>
                  <TableHead className="w-24">更新时间</TableHead>
                  <TableHead className="w-28">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {playbooks.map((p) => {
                  const stepCount = (p.steps ?? []).length;
                  return (
                    <TableRow key={p.id}>
                      <TableCell className="truncate font-medium" title={`${p.title}\n${p.summary}`}>
                        {p.title}
                      </TableCell>
                      <TableCell>
                        <EvolveStatusBadge status={p.status} />
                      </TableCell>
                      <TableCell className="tabular-nums">{stepCount} 步</TableCell>
                      <TableCell className="tabular-nums">
                        <span className="text-emerald-600">{p.success_count}</span>
                        <span className="text-muted-foreground"> / </span>
                        <span className="text-red-500">{p.fail_count}</span>
                      </TableCell>
                      <TableCell className="tabular-nums">{fmtSeconds(p.avg_seconds)}</TableCell>
                      <TableCell className="tabular-nums">{p.corroboration} 次印证</TableCell>
                      <TableCell className="text-muted-foreground tabular-nums" title={p.updated_at}>
                        {fmtTimeShort(p.updated_at)}
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center gap-1">
                          <Button variant="ghost" size="sm" title="查看剧本详情" onClick={() => setDetail(p)}>
                            <EyeIcon className="text-muted-foreground size-3.5" />
                          </Button>
                          {p.status === "candidate" && (
                            <>
                              <Button variant="ghost" size="sm" title="转正(立即生效并注入任务)" onClick={() => void setStatus(p, "active")}>
                                <CheckIcon className="size-3.5 text-emerald-500" />
                              </Button>
                              <Button variant="ghost" size="sm" title="否决(标记为无效剧本)" onClick={() => void setStatus(p, "rejected")}>
                                <XIcon className="size-3.5 text-red-500" />
                              </Button>
                            </>
                          )}
                          {p.status === "active" && (
                            <Button variant="ghost" size="sm" title="退役(停止注入,保留备查)" onClick={() => void setStatus(p, "retired")}>
                              <ArchiveIcon className="text-muted-foreground size-3.5" />
                            </Button>
                          )}
                          <Button variant="ghost" size="sm" title="删除" onClick={() => void remove(p)}>
                            <Trash2Icon className="text-muted-foreground size-3.5" />
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
              total={total}
              onPageChange={setPage}
              onPageSizeChange={(s) => {
                setPage(1);
                setPageSize(s);
              }}
            />
          </>
        )}
      </CardContent>

      {/* 剧本详情:摘要 / 前置条件 / 步骤表 / 成败统计 / 证据任务。 */}
      <Dialog open={!!detail} onOpenChange={(o) => !o && setDetail(null)}>
        <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle className="flex flex-wrap items-center gap-2">
              {detail?.title}
              {detail && <EvolveStatusBadge status={detail.status} />}
            </DialogTitle>
            <DialogDescription>
              归属 {detail?.scope_kind === "global" ? "全局" : detail?.scope_kind === "company" ? `企业 #${detail?.scope_id}` : detail?.scope_kind}
              {detail ? ` · 更新于 ${new Date(detail.updated_at).toLocaleString()}` : ""}
            </DialogDescription>
          </DialogHeader>
          {detail && (
            <div className="flex flex-col gap-3 text-sm">
              {detail.summary?.trim() && (
                <section className="flex flex-col gap-1 rounded-md border p-3">
                  <div className="text-xs font-medium text-muted-foreground">摘要</div>
                  <p className="break-words whitespace-pre-wrap">{detail.summary}</p>
                </section>
              )}

              <section className="flex flex-col gap-1 rounded-md border p-3">
                <div className="text-xs font-medium text-muted-foreground">前置条件</div>
                {detailPreconditions.length > 0 ? (
                  <div className="grid gap-1">
                    {detailPreconditions.map(([k, v]) => (
                      <div key={k} className="flex items-start gap-2 text-xs">
                        <span className="bg-muted text-muted-foreground shrink-0 rounded px-1.5 py-0.5 font-mono">{k}</span>
                        <span className="break-words">{v}</span>
                      </div>
                    ))}
                  </div>
                ) : (
                  <p className="text-muted-foreground text-xs">无</p>
                )}
              </section>

              <section className="flex flex-col gap-1 rounded-md border p-3">
                <div className="text-xs font-medium text-muted-foreground">步骤</div>
                {detailSteps.length > 0 ? (
                  <Table className="table-fixed text-xs">
                    <TableHeader>
                      <TableRow className="hover:bg-transparent">
                        <TableHead className="w-10">#</TableHead>
                        <TableHead className="w-40">动作</TableHead>
                        <TableHead>细节</TableHead>
                        <TableHead className="w-32">备注</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {detailSteps.map((s) => (
                        <TableRow key={s.n}>
                          <TableCell className="tabular-nums">{s.n}</TableCell>
                          <TableCell className="truncate" title={s.action}>
                            {s.action}
                          </TableCell>
                          <TableCell className="truncate" title={s.detail}>
                            {s.detail || "—"}
                          </TableCell>
                          <TableCell className="truncate" title={s.notes}>
                            {s.notes || "—"}
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                ) : (
                  <p className="text-muted-foreground text-xs">无步骤记录</p>
                )}
              </section>

              <section className="flex flex-col gap-1 rounded-md border p-3">
                <div className="text-xs font-medium text-muted-foreground">成效统计</div>
                <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs">
                  <span className="tabular-nums">
                    成功 <span className="text-emerald-600 font-semibold">{detail.success_count}</span>
                  </span>
                  <span className="tabular-nums">
                    失败 <span className="text-red-500 font-semibold">{detail.fail_count}</span>
                  </span>
                  <span className="tabular-nums">平均耗时 {fmtSeconds(detail.avg_seconds)}</span>
                  <span className="tabular-nums">印证 {detail.corroboration} 次</span>
                </div>
              </section>

              <section className="flex flex-col gap-1 rounded-md border p-3">
                <div className="text-xs font-medium text-muted-foreground">证据任务</div>
                <p className="text-xs break-words" title={taskIdsText(detailEvidence)}>
                  {taskIdsText(detailEvidence) || "—"}
                </p>
              </section>
            </div>
          )}
        </DialogContent>
      </Dialog>

      {/* 新增剧本:标题 + 摘要 + 归属 + 一行一步的步骤文本。 */}
      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>新增剧本</DialogTitle>
            <DialogDescription>录入一套可复用的打法,初始为「待验证」,不会注入任务;转正后才生效。</DialogDescription>
          </DialogHeader>
          <div className="grid gap-3">
            <div className="grid gap-1.5">
              <Label htmlFor="playbook-title">标题</Label>
              <Input
                id="playbook-title"
                value={formTitle}
                onChange={(e) => setFormTitle(e.target.value)}
                placeholder="如「外网 nginx RCE 处置剧本」"
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="playbook-summary">摘要(可选)</Label>
              <Textarea
                id="playbook-summary"
                value={formSummary}
                onChange={(e) => setFormSummary(e.target.value)}
                placeholder="适用场景与预期效果"
              />
            </div>
            <ScopeFields
              scopeKind={formScopeKind}
              onScopeKind={setFormScopeKind}
              scopeId={formScopeId}
              onScopeId={setFormScopeId}
              companies={companies}
            />
            <div className="grid gap-1.5">
              <Label htmlFor="playbook-steps">步骤(一行一步,按书写顺序编号)</Label>
              <Textarea
                id="playbook-steps"
                value={formSteps}
                onChange={(e) => setFormSteps(e.target.value)}
                rows={5}
                placeholder={"如:\n枚举 api 子域并确认存活\n对存活主机做端口与指纹识别\n针对识别出的中间件比对已知漏洞"}
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCreateOpen(false)}>
              取消
            </Button>
            <Button disabled={saving || !formTitle.trim()} onClick={() => void create()}>
              {saving ? "保存中…" : "保存"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}
