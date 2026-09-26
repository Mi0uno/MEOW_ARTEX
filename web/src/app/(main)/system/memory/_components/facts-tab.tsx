"use client";

// 记忆库 · 事实页签:结构化记忆事实(双时间戳失效制,valid_to 非空 = 已被取代)。
// 搜索 + kind 筛选 + 「含已失效」 + 分页四件套;新增事实 scope 固定归属当前企业
//(未选企业时按钮置灰并给引导提示);失效/删除后刷新本列表并回报父级刷新概览。

import * as React from "react";

import { BanIcon, PlusIcon, SearchIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { TablePagination } from "@/components/table-pagination";
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
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { api } from "@/lib/api";
import type { MemoryFact, MemoryFactKind } from "@/lib/types";

// 事实分类中文标签(与后端 memory_facts.kind 同取值)。
const KIND_LABELS: Record<string, string> = {
  asset_note: "资产备注",
  tech_stack: "技术栈",
  path: "可用路径",
  credential_ref: "凭据线索",
  guard_note: "防护观察",
  other: "其他",
};

// 事实来源中文标签。
const SOURCE_LABELS: Record<string, string> = {
  ai: "AI 复盘",
  manual: "人工",
  recon: "信息搜集",
  backfill: "历史回填",
};

function fmtTime(iso?: string | null): string {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

// 列表用的紧凑时间(MM-DD HH:mm),完整时间放单元格 title。
function fmtTimeShort(iso?: string | null): string {
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

export function FactsTab({
  companyId,
  companyName,
  onChanged,
}: {
  companyId: number; // 0 = 全部企业
  companyName: string; // 已选企业的展示名(新增事实的归属提示)
  onChanged?: () => void; // 增删后回报父级刷新概览/页签计数
}) {
  const [facts, setFacts] = React.useState<MemoryFact[]>([]);
  const [total, setTotal] = React.useState(0);
  const [query, setQuery] = React.useState("");
  const [queryApplied, setQueryApplied] = React.useState("");
  const [kind, setKind] = React.useState("all");
  const [includeInvalid, setIncludeInvalid] = React.useState(false);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(20);
  const [loading, setLoading] = React.useState(true);
  // 新增事实表单。
  const [createOpen, setCreateOpen] = React.useState(false);
  const [formKind, setFormKind] = React.useState("asset_note");
  const [formSubject, setFormSubject] = React.useState("");
  const [formContent, setFormContent] = React.useState("");
  const [saving, setSaving] = React.useState(false);

  const load = React.useCallback(
    (q: string, kindF: string, invalid: boolean, p: number, size: number) => {
      setLoading(true);
      api
        .memoryFacts({
          companyId: companyId || undefined,
          kind: kindF === "all" ? "" : kindF,
          q,
          includeInvalid: invalid,
          limit: size,
          offset: (p - 1) * size,
        })
        .then((r) => {
          setFacts(r.facts);
          setTotal(r.total);
        })
        .catch((e) => toast.error(`加载事实失败:${(e as Error).message}`))
        .finally(() => setLoading(false));
    },
    [companyId],
  );

  React.useEffect(() => {
    load(queryApplied, kind, includeInvalid, page, pageSize);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [queryApplied, kind, includeInvalid, page, pageSize, load]);

  const reload = () => {
    load(queryApplied, kind, includeInvalid, page, pageSize);
    onChanged?.();
  };

  // 搜索:落定搜索词并回第一页;page/queryApplied 变化由上方 effect 统一触发加载。
  const search = () => {
    setPage(1);
    setQueryApplied(query.trim());
  };

  // 标记失效:保留历史(失效制),只是不再注入新任务。
  const invalidate = async (f: MemoryFact) => {
    if (!window.confirm(`失效「${f.subject || f.content.slice(0, 40)}」?已失效的事实不再注入新任务,记录保留。`)) return;
    try {
      await api.memoryInvalidateFact(f.id);
      toast.success("已标记失效");
      reload();
    } catch (e) {
      toast.error(`操作失败:${(e as Error).message}`);
    }
  };

  const remove = async (f: MemoryFact) => {
    if (!window.confirm(`删除「${f.subject || f.content.slice(0, 40)}」?该操作不可恢复。`)) return;
    try {
      await api.memoryDeleteFact(f.id);
      toast.success("已删除");
      reload();
    } catch (e) {
      toast.error(`删除失败:${(e as Error).message}`);
    }
  };

  const create = async () => {
    const content = formContent.trim();
    if (!companyId || !content) return;
    setSaving(true);
    try {
      await api.memoryCreateFact({
        scope_kind: "company",
        scope_id: companyId,
        kind: formKind as MemoryFactKind,
        subject: formSubject.trim(),
        content,
      });
      toast.success("已新增事实,下次任务简报将带上它");
      setCreateOpen(false);
      setFormSubject("");
      setFormContent("");
      setPage(1);
      onChanged?.();
      load(queryApplied, kind, includeInvalid, 1, pageSize);
    } catch (e) {
      toast.error(`保存失败:${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card>
      <CardContent className="px-0 pt-4">
        {/* 工具栏:搜索 + kind 筛选 + 含已失效 + 新增。 */}
        <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
          <div className="relative w-full sm:max-w-xs">
            <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && search()}
              placeholder="搜索主体 / 内容"
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
                {Object.entries(KIND_LABELS).map(([v, label]) => (
                  <SelectItem key={v} value={v}>
                    {label}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
          <div className="flex items-center gap-1.5">
            <Checkbox
              id="facts-include-invalid"
              checked={includeInvalid}
              onCheckedChange={(v) => {
                setPage(1);
                setIncludeInvalid(v === true);
              }}
            />
            <Label htmlFor="facts-include-invalid" className="text-xs font-normal">
              含已失效
            </Label>
          </div>
          <span className="text-muted-foreground ml-auto text-xs tabular-nums">共 {total} 条</span>
          {companyId ? (
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <PlusIcon className="size-4" />
              新增事实
            </Button>
          ) : (
            <Tooltip>
              <TooltipTrigger asChild>
                <span className="inline-flex">
                  <Button size="sm" disabled>
                    <PlusIcon className="size-4" />
                    新增事实
                  </Button>
                </span>
              </TooltipTrigger>
              <TooltipContent>先在顶部选择一个企业,新增的事实将归属该企业</TooltipContent>
            </Tooltip>
          )}
        </div>

        {loading && facts.length === 0 ? (
          <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
        ) : facts.length === 0 ? (
          <p className="text-muted-foreground py-16 text-center text-sm">
            {queryApplied
              ? "没有匹配的记忆事实"
              : "还没有记忆事实。任务复盘会自动沉淀;选择企业后也可用「新增事实」人工录入。"}
          </p>
        ) : (
          <>
            <Table className="table-fixed text-xs">
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="w-40">主体</TableHead>
                  <TableHead className="w-20">类型</TableHead>
                  {/* 内容列不设宽度:table-fixed 下它吃掉其余列之外的余量,别再加宽固定列。 */}
                  <TableHead>内容</TableHead>
                  <TableHead className="w-16">来源</TableHead>
                  <TableHead className="w-14">置信度</TableHead>
                  <TableHead className="w-24">状态</TableHead>
                  <TableHead className="w-28">记录时间</TableHead>
                  <TableHead className="w-20">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {facts.map((f) => {
                  const invalid = !!f.valid_to;
                  return (
                    <TableRow key={f.id} className={invalid ? "opacity-60" : undefined}>
                      <TableCell className="truncate font-medium" title={f.subject}>
                        {f.subject || "—"}
                      </TableCell>
                      <TableCell>
                        <Badge variant="outline">{KIND_LABELS[f.kind] ?? f.kind}</Badge>
                      </TableCell>
                      <TableCell className="truncate" title={f.content}>
                        {f.content}
                      </TableCell>
                      <TableCell className="text-muted-foreground truncate" title={SOURCE_LABELS[f.source_kind] ?? f.source_kind}>
                        {SOURCE_LABELS[f.source_kind] ?? f.source_kind}
                      </TableCell>
                      <TableCell className="tabular-nums">{fmtConfidence(f.confidence)}</TableCell>
                      <TableCell>
                        {invalid ? (
                          <Badge variant="outline" className="text-muted-foreground" title={`失效于 ${fmtTime(f.valid_to)}`}>
                            已失效
                          </Badge>
                        ) : (
                          <Badge variant="outline" className="border-emerald-500/40 text-emerald-600">
                            有效
                          </Badge>
                        )}
                      </TableCell>
                      <TableCell className="text-muted-foreground truncate" title={fmtTime(f.observed_at)}>
                        {fmtTimeShort(f.observed_at)}
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center gap-1">
                          {!invalid && (
                            <Button variant="ghost" size="sm" title="标记失效(不再注入新任务)" onClick={() => void invalidate(f)}>
                              <BanIcon className="text-muted-foreground size-3.5" />
                            </Button>
                          )}
                          <Button variant="ghost" size="sm" title="删除" onClick={() => void remove(f)}>
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

      {/* 新增事实:scope 固定归属当前所选企业。 */}
      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>新增事实</DialogTitle>
            <DialogDescription>
              人工沉淀一条结构化记忆,归属「{companyName || `企业 #${companyId}`}」;下次该企业的任务简报会自动带上它。
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-3">
            <div className="grid gap-1.5">
              <Label>类型</Label>
              <Select value={formKind} onValueChange={setFormKind}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {Object.entries(KIND_LABELS).map(([v, label]) => (
                      <SelectItem key={v} value={v}>
                        {label}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="fact-subject">主体(host / URL / 资产名)</Label>
              <Input
                id="fact-subject"
                value={formSubject}
                onChange={(e) => setFormSubject(e.target.value)}
                placeholder="如 api.example.com"
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="fact-content">内容</Label>
              <Textarea
                id="fact-content"
                value={formContent}
                onChange={(e) => setFormContent(e.target.value)}
                placeholder="一条事实性结论,如「后台登录口已下线,改走 /admin/v2」"
              />
            </div>
            <p className="text-muted-foreground text-xs">
              凭据线索(credential_ref)只存指针(文件路径 / 任务号),不要贴明文凭据。
            </p>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCreateOpen(false)}>
              取消
            </Button>
            <Button disabled={saving || !formContent.trim()} onClick={() => void create()}>
              {saving ? "保存中…" : "保存"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}
