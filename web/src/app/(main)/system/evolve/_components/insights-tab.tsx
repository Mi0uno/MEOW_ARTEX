"use client";

// 自进化 · 洞察页签:ExpeL 式经验洞察(正面经验 / 负面教训)。
// 状态页签(带 evolveOverview 计数,不受筛选影响) + kind 筛选 + 搜索 + 分页;
// 人工闸门:candidate → 转正/否决,active → 退役,rejected/retired → 仅删除。
// 列表数据走 api.evolveInsights;任何变更后刷新本列表并回报父级刷新概览。

import * as React from "react";

import { ArchiveIcon, CheckIcon, PlusIcon, SearchIcon, Trash2Icon, XIcon } from "lucide-react";
import { toast } from "sonner";

import { TablePagination } from "@/components/table-pagination";
import { Badge } from "@/components/ui/badge";
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
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { EvolveCategory, EvolveInsight, EvolveStatus, EvolveStatusCounts } from "@/lib/types";
import {
  PillTab,
  STATUS_ACTION_LABELS,
  STATUS_TABS,
  ScopeFields,
  EvolveStatusBadge,
  statusCount,
  useCompanies,
  type EvolveTabStatus,
} from "./evolve-shared";

// 类别中文标签(与后端 evolve_insights.category 同取值;未知值原样展示)。
const CATEGORY_LABELS: Record<string, string> = {
  technique: "技术",
  tool: "工具",
  recon: "信息搜集",
  pitfall: "踩坑",
  evasion: "免杀",
  ops: "运维",
};

function fmtTimeShort(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

function fmtConfidence(v: number): string {
  if (!Number.isFinite(v)) return "—";
  return `${Math.round(v * 100)}%`;
}

// 来源任务列:前 3 个 + 「+N」;完整列表放 title。
function sourceTasksText(ids: number[]): { text: string; full: string } {
  const list = ids ?? [];
  const head = list
    .slice(0, 3)
    .map((id) => `#${id}`)
    .join(" ");
  const rest = list.length - 3;
  return {
    text: rest > 0 ? `${head} +${rest}` : head || "—",
    full: list.map((id) => `#${id}`).join(" "),
  };
}

export function InsightsTab({
  counts,
  onChanged,
}: {
  counts: EvolveStatusCounts | null; // 状态页签计数(evolveOverview.insights)
  onChanged?: () => void; // 变更后回报父级刷新概览/页签计数
}) {
  const [insights, setInsights] = React.useState<EvolveInsight[]>([]);
  const [total, setTotal] = React.useState(0);
  const [tabStatus, setTabStatus] = React.useState<EvolveTabStatus>("all");
  const [kind, setKind] = React.useState("all");
  const [query, setQuery] = React.useState("");
  const [queryApplied, setQueryApplied] = React.useState("");
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(20);
  const [loading, setLoading] = React.useState(true);
  // 新增洞察表单。
  const [createOpen, setCreateOpen] = React.useState(false);
  const [formTitle, setFormTitle] = React.useState("");
  const [formContent, setFormContent] = React.useState("");
  const [formKind, setFormKind] = React.useState("positive");
  const [formCategory, setFormCategory] = React.useState("technique");
  const [formScopeKind, setFormScopeKind] = React.useState<"global" | "company">("global");
  const [formScopeId, setFormScopeId] = React.useState(0);
  const [saving, setSaving] = React.useState(false);
  const companies = useCompanies();

  const load = React.useCallback(
    (st: string, kf: string, q: string, p: number, size: number) => {
      setLoading(true);
      api
        .evolveInsights({
          status: st === "all" ? "" : st,
          kind: kf === "all" ? "" : kf,
          q,
          limit: size,
          offset: (p - 1) * size,
        })
        .then((r) => {
          setInsights(r.insights);
          setTotal(r.total);
        })
        .catch((e) => toast.error(`加载洞察失败:${(e as Error).message}`))
        .finally(() => setLoading(false));
    },
    [],
  );

  React.useEffect(() => {
    load(tabStatus, kind, queryApplied, page, pageSize);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tabStatus, kind, queryApplied, page, pageSize, load]);

  const reload = () => {
    load(tabStatus, kind, queryApplied, page, pageSize);
    onChanged?.();
  };

  const search = () => {
    setPage(1);
    setQueryApplied(query.trim());
  };

  // 人工闸门:转正/否决/退役(撤销回 candidate 也走同一接口)。
  const setStatus = async (row: EvolveInsight, status: EvolveStatus) => {
    try {
      await api.evolveInsightStatus(row.id, status);
      toast.success(`已${STATUS_ACTION_LABELS[status]}:「${row.title.slice(0, 24)}」`);
      reload();
    } catch (e) {
      toast.error(`操作失败:${(e as Error).message}`);
    }
  };

  const remove = async (row: EvolveInsight) => {
    if (!window.confirm(`删除洞察「${row.title.slice(0, 40)}」?该操作不可恢复。`)) return;
    try {
      await api.evolveDeleteInsight(row.id);
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
    setSaving(true);
    try {
      await api.evolveCreateInsight({
        title,
        content: formContent.trim() || undefined,
        kind: formKind as EvolveInsight["kind"],
        category: formCategory as EvolveCategory,
        scope_kind: formScopeKind,
        scope_id: formScopeId || undefined,
      });
      toast.success("已新增洞察(待验证,不注入任务;转正后生效)");
      setCreateOpen(false);
      setFormTitle("");
      setFormContent("");
      setFormScopeKind("global");
      setFormScopeId(0);
      setPage(1);
      onChanged?.();
      load(tabStatus, kind, queryApplied, 1, pageSize);
    } catch (e) {
      toast.error(`保存失败:${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  };

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
              placeholder="搜索标题 / 内容"
              className="pl-8"
            />
          </div>
          <Button variant="outline" size="sm" onClick={search}>
            搜索
          </Button>
          <Select
            value={kind}
            onValueChange={(v) => {
              setPage(1);
              setKind(v);
            }}
          >
            <SelectTrigger className="w-32">
              <SelectValue placeholder="类型" />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value="all">全部类型</SelectItem>
                <SelectItem value="positive">正面经验</SelectItem>
                <SelectItem value="negative">负面教训</SelectItem>
              </SelectGroup>
            </SelectContent>
          </Select>
          <span className="text-muted-foreground ml-auto text-xs tabular-nums">共 {total} 条</span>
          <Button size="sm" onClick={() => setCreateOpen(true)}>
            <PlusIcon className="size-4" />
            新增洞察
          </Button>
        </div>

        {loading && insights.length === 0 ? (
          <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
        ) : insights.length === 0 ? (
          <p className="text-muted-foreground py-16 text-center text-sm">
            {queryApplied
              ? "没有匹配的洞察"
              : "还没有经验洞察。任务复盘会自动归纳正面经验与负面教训(待验证),被 ≥2 次独立任务印证或人工转正后生效。"}
          </p>
        ) : (
          <>
            <Table className="table-fixed text-xs">
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  {/* 标题列不设宽度:table-fixed 下它吃掉其余列之外的余量。 */}
                  <TableHead>标题</TableHead>
                  <TableHead className="w-16">类别</TableHead>
                  <TableHead className="w-14">类型</TableHead>
                  <TableHead className="w-24">信任</TableHead>
                  <TableHead className="w-16">状态</TableHead>
                  <TableHead className="w-24">来源任务</TableHead>
                  <TableHead className="w-24">更新时间</TableHead>
                  <TableHead className="w-24">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {insights.map((i) => {
                  const st = sourceTasksText(i.source_task_ids ?? []);
                  return (
                    <TableRow key={i.id}>
                      <TableCell className="truncate font-medium" title={`${i.title}\n${i.content}`}>
                        {i.title}
                      </TableCell>
                      <TableCell>
                        <Badge variant="outline">{CATEGORY_LABELS[i.category] ?? i.category}</Badge>
                      </TableCell>
                      <TableCell>
                        {i.kind === "negative" ? (
                          <Badge variant="outline" className="border-red-500/40 text-red-600">
                            负面
                          </Badge>
                        ) : (
                          <Badge variant="outline" className="border-emerald-500/40 text-emerald-600">
                            正面
                          </Badge>
                        )}
                      </TableCell>
                      <TableCell>
                        <div className="tabular-nums">{i.corroboration} 次印证</div>
                        <div className="text-muted-foreground text-[10px] tabular-nums">置信 {fmtConfidence(i.confidence)}</div>
                      </TableCell>
                      <TableCell>
                        <EvolveStatusBadge status={i.status} />
                      </TableCell>
                      <TableCell className="text-muted-foreground truncate" title={st.full}>
                        {st.text}
                      </TableCell>
                      <TableCell className="text-muted-foreground tabular-nums" title={i.updated_at}>
                        {fmtTimeShort(i.updated_at)}
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center gap-1">
                          {i.status === "candidate" && (
                            <>
                              <Button variant="ghost" size="sm" title="转正(立即生效并注入任务)" onClick={() => void setStatus(i, "active")}>
                                <CheckIcon className="size-3.5 text-emerald-500" />
                              </Button>
                              <Button variant="ghost" size="sm" title="否决(标记为误经验,不再印证)" onClick={() => void setStatus(i, "rejected")}>
                                <XIcon className="size-3.5 text-red-500" />
                              </Button>
                            </>
                          )}
                          {i.status === "active" && (
                            <Button variant="ghost" size="sm" title="退役(停止注入,保留备查)" onClick={() => void setStatus(i, "retired")}>
                              <ArchiveIcon className="text-muted-foreground size-3.5" />
                            </Button>
                          )}
                          <Button variant="ghost" size="sm" title="删除" onClick={() => void remove(i)}>
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

      {/* 新增洞察:人工沉淀一条经验,初始为待验证。 */}
      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>新增洞察</DialogTitle>
            <DialogDescription>人工录入一条经验或教训,初始为「待验证」,不会注入任务;转正后才生效。</DialogDescription>
          </DialogHeader>
          <div className="grid gap-3">
            <div className="grid gap-1.5">
              <Label htmlFor="insight-title">标题(一句话规律)</Label>
              <Input
                id="insight-title"
                value={formTitle}
                onChange={(e) => setFormTitle(e.target.value)}
                placeholder="如「该站 WAF 对 sqlmap UA 直接封禁,需改用裸请求」"
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="insight-content">展开说明(可选)</Label>
              <Textarea
                id="insight-content"
                value={formContent}
                onChange={(e) => setFormContent(e.target.value)}
                placeholder="适用条件、依据、注意事项"
              />
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div className="grid gap-1.5">
                <Label>类型</Label>
                <Select value={formKind} onValueChange={setFormKind}>
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      <SelectItem value="positive">正面经验</SelectItem>
                      <SelectItem value="negative">负面教训</SelectItem>
                    </SelectGroup>
                  </SelectContent>
                </Select>
              </div>
              <div className="grid gap-1.5">
                <Label>类别</Label>
                <Select value={formCategory} onValueChange={setFormCategory}>
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      {Object.entries(CATEGORY_LABELS).map(([v, label]) => (
                        <SelectItem key={v} value={v}>
                          {label}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
              </div>
            </div>
            <ScopeFields
              scopeKind={formScopeKind}
              onScopeKind={setFormScopeKind}
              scopeId={formScopeId}
              onScopeId={setFormScopeId}
              companies={companies}
            />
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
