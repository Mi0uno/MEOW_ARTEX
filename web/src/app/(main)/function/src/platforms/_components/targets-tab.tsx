"use client";

// 目标页签:该平台下收录的目标。
//
// 平台与目标多对多,授权范围挂在「平台×目标」这一层。所以这一页只负责
// 「把目标挂进本平台 / 解除挂链」——目标的资产与授权范围在目标详情页里管
// (那边能同时看到该目标在别的平台下的另一套范围,避免用户以为只有一份)。

import * as React from "react";

import Link from "next/link";

import { ArrowRightIcon, PlusIcon, SearchIcon, TargetIcon, Trash2Icon } from "lucide-react";
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
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { Company, SRCTarget } from "@/lib/types";

import { Field, fmtTime } from "./platform-shared";

const PAGE_SIZE = 50;
// Radix Select 的 value="" 是保留值(表示未选中),「不关联企业 / 请选择」必须用哨兵串。
const NO_COMPANY = "__none__";
const PICK_TARGET = "__pick__";

export function TargetsTab({ platformId, onChanged }: { platformId: number; onChanged: () => void }) {
  const [targets, setTargets] = React.useState<SRCTarget[]>([]);
  const [total, setTotal] = React.useState(0);
  const [query, setQuery] = React.useState("");
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);
  const [loading, setLoading] = React.useState(true);
  const [addOpen, setAddOpen] = React.useState(false);

  const load = React.useCallback(
    (q: string, p: number, size: number) => {
      setLoading(true);
      api
        .srcPlatformTargets(platformId, { q, limit: size, offset: (p - 1) * size })
        .then((r) => {
          setTargets(r.targets ?? []);
          setTotal(r.total);
        })
        .catch((e) => toast.error(`加载目标失败:${(e as Error).message}`))
        .finally(() => setLoading(false));
    },
    [platformId],
  );

  React.useEffect(() => {
    load(query, page, pageSize);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [platformId, page, pageSize]);

  const search = () => {
    setPage(1);
    load(query, 1, pageSize);
  };

  const remove = async (t: SRCTarget) => {
    if (
      !window.confirm(
        `把「${t.name}」移出本平台?只解除与本平台的关联,目标本身与其他平台的关联不受影响,目标的资产也不会删除。`,
      )
    )
      return;
    try {
      await api.removeSRCPlatformTarget(platformId, t.id);
      toast.success("已移出本平台");
      load(query, page, pageSize);
      onChanged();
    } catch (e) {
      toast.error(`移除失败:${(e as Error).message}`);
    }
  };

  return (
    <Card>
      <CardContent className="px-0 pt-4">
        <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
          <div className="relative w-full sm:max-w-xs">
            <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && search()}
              placeholder="搜索目标名 / 描述"
              className="pl-8"
            />
          </div>
          <Button variant="outline" size="sm" onClick={search}>
            搜索
          </Button>
          <Button size="sm" className="ml-auto" onClick={() => setAddOpen(true)}>
            <PlusIcon className="size-4" />
            添加目标
          </Button>
        </div>

        {loading && targets.length === 0 ? (
          <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
        ) : targets.length === 0 ? (
          <div className="text-muted-foreground flex flex-col items-center gap-3 py-16 text-center text-sm">
            <TargetIcon className="size-8 opacity-40" />
            <span>
              {query
                ? "没有匹配的目标。换个关键词试试。"
                : "本平台下还没有目标。点「添加目标」新建一个，或把已有的目标挂进来——之后在目标详情页里添加资产与授权范围。"}
            </span>
            {!query && (
              <Button size="sm" variant="outline" onClick={() => setAddOpen(true)}>
                <PlusIcon className="size-4" />
                添加目标
              </Button>
            )}
          </div>
        ) : (
          <>
            <Table className="table-fixed">
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  {/* 目标名列不设宽度(自动吃余量);固定列之和 656px(160+80+80+96+144+96),远小于 ~897px 容器。 */}
                  <TableHead>目标名称</TableHead>
                  <TableHead className="w-40">关联企业</TableHead>
                  <TableHead className="w-20">资产数</TableHead>
                  <TableHead className="w-20">平台数</TableHead>
                  <TableHead className="w-24">状态</TableHead>
                  <TableHead className="w-36">更新时间</TableHead>
                  <TableHead className="w-24">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {targets.map((t) => (
                  <TableRow key={t.id}>
                    <TableCell className="min-w-0">
                      {/* 带上 platform 参数:目标详情页需要知道「现在是从哪个平台的视角进来的」,
                          授权范围是按平台×目标存的,不带平台就会编辑错那一份。 */}
                      <Link
                        href={`/function/src/targets/detail?id=${t.id}&platform=${platformId}`}
                        className="block w-full truncate text-left font-medium hover:underline"
                        title={t.name}
                      >
                        {t.name}
                      </Link>
                    </TableCell>
                    <TableCell className="text-muted-foreground truncate text-xs" title={t.company_name ?? ""}>
                      {t.company_name || "未关联企业"}
                    </TableCell>
                    <TableCell className="tabular-nums">{t.asset_count ?? 0}</TableCell>
                    <TableCell className="tabular-nums">{t.platform_count ?? 0}</TableCell>
                    <TableCell>
                      <Badge
                        variant="outline"
                        className={
                          t.enabled
                            ? "bg-emerald-500/15 text-emerald-600 border-emerald-500/30"
                            : "bg-muted text-muted-foreground"
                        }
                      >
                        {t.enabled ? "启用" : "停用"}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-muted-foreground text-xs">{fmtTime(t.updated_at)}</TableCell>
                    <TableCell>
                      <div className="flex items-center gap-0.5">
                        <Button
                          variant="ghost"
                          size="icon-xs"
                          onClick={() => void remove(t)}
                          title="移出本平台(只解除关联)"
                        >
                          <Trash2Icon className="text-muted-foreground size-3.5" />
                        </Button>
                        <Button variant="ghost" size="icon-xs" asChild title="进入目标">
                          <Link
                            href={`/function/src/targets/detail?id=${t.id}&platform=${platformId}`}
                            aria-label="进入目标"
                          >
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

      <AddTargetDialog
        platformId={platformId}
        open={addOpen}
        onOpenChange={setAddOpen}
        onAdded={() => {
          setAddOpen(false);
          load(query, page, pageSize);
          onChanged();
        }}
      />
    </Card>
  );
}

// AddTargetDialog 合一两种添加语义(与后端 addSRCPlatformTarget 的契约一致):
// 「新建目标」传 name(+可选 company_id/description),「挂已有目标」传 target_id>0。
// 合成一个对话框是因为用户心里想的是「把这个对象加进来」,不该先自问它是否已存在。
function AddTargetDialog({
  platformId,
  open,
  onOpenChange,
  onAdded,
}: {
  platformId: number;
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onAdded: () => void;
}) {
  const [mode, setMode] = React.useState<"new" | "existing">("new");
  const [name, setName] = React.useState("");
  const [companyId, setCompanyId] = React.useState<string>(NO_COMPANY);
  const [description, setDescription] = React.useState("");
  const [scopeNote, setScopeNote] = React.useState("");
  const [targetId, setTargetId] = React.useState<string>(PICK_TARGET);
  const [companies, setCompanies] = React.useState<Company[]>([]);
  const [candidates, setCandidates] = React.useState<SRCTarget[]>([]);
  const [loadingOptions, setLoadingOptions] = React.useState(false);
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    if (!open) return;
    setMode("new");
    setName("");
    setCompanyId(NO_COMPANY);
    setDescription("");
    setScopeNote("");
    setTargetId(PICK_TARGET);
    setLoadingOptions(true);
    // 企业列表与已有目标列表并行拉;任一失败不该挡住另一个(企业挂了仍可挂已有目标)。
    Promise.all([
      api.companies().catch(() => [] as Company[]),
      api.srcTargets({ limit: 200 }).catch(() => ({ targets: [] as SRCTarget[], total: 0 })),
    ])
      .then(([cs, ts]) => {
        setCompanies(cs ?? []);
        setCandidates(ts.targets ?? []);
      })
      .finally(() => setLoadingOptions(false));
  }, [open]);

  const submit = async () => {
    if (mode === "new" && !name.trim()) {
      toast.error("请填写目标名称");
      return;
    }
    if (mode === "existing" && targetId === PICK_TARGET) {
      toast.error("请选择一个已有目标");
      return;
    }
    setBusy(true);
    try {
      if (mode === "existing") {
        await api.addSRCPlatformTarget(platformId, { target_id: Number(targetId), scope_note: scopeNote });
      } else {
        await api.addSRCPlatformTarget(platformId, {
          name: name.trim(),
          company_id: companyId === NO_COMPANY ? 0 : Number(companyId),
          description,
          scope_note: scopeNote,
        });
      }
      toast.success("目标已添加到本平台");
      onAdded();
    } catch (e) {
      // 同名目标由后端判 409,原文透出便于用户改名字或改选已有目标。
      toast.error(`添加失败:${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>添加目标</DialogTitle>
          <DialogDescription className="text-xs">
            目标是被测试的对象（厂商/学校）。授权范围按「平台×目标」分别保存，同一目标在别的平台下的范围不受影响。
          </DialogDescription>
        </DialogHeader>

        {/* 两个模式是互斥语义(新建 / 挂已有),用 Tabs 表达;切模式不清空另一侧已填内容。 */}
        <div className="flex items-center gap-1 rounded-lg bg-muted p-1 text-sm">
          <button
            type="button"
            onClick={() => setMode("new")}
            aria-pressed={mode === "new"}
            className={`flex-1 rounded-md px-3 py-1.5 transition-colors ${
              mode === "new" ? "bg-background font-medium shadow-sm" : "text-muted-foreground"
            }`}
          >
            新建目标
          </button>
          <button
            type="button"
            onClick={() => setMode("existing")}
            aria-pressed={mode === "existing"}
            className={`flex-1 rounded-md px-3 py-1.5 transition-colors ${
              mode === "existing" ? "bg-background font-medium shadow-sm" : "text-muted-foreground"
            }`}
          >
            挂已有目标
          </button>
        </div>

        <div className="space-y-4">
          {mode === "new" ? (
            <>
              <Field label="目标名称">
                <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="Acme Corp / 上海交通大学" />
              </Field>
              <Field label="关联企业(可选)">
                <Select value={companyId} onValueChange={setCompanyId}>
                  <SelectTrigger className="w-full">
                    <SelectValue placeholder={loadingOptions ? "加载中…" : "暂不关联企业"} />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      <SelectItem value={NO_COMPANY}>暂不关联企业</SelectItem>
                      {companies.map((c) => (
                        <SelectItem key={c.id} value={String(c.id)}>
                          {c.name}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
                <p className="text-muted-foreground text-xs">
                  可以先只登记目标名，等资产进来再关联企业（目标的 company_id 允许为空）
                </p>
              </Field>
              <Field label="描述">
                <Input
                  value={description}
                  onChange={(e) => setDescription(e.target.value)}
                  placeholder="主营业务、核心资产域名…"
                />
              </Field>
            </>
          ) : (
            <Field label="选择已有目标">
              <Select value={targetId} onValueChange={setTargetId}>
                <SelectTrigger className="w-full">
                  <SelectValue placeholder={loadingOptions ? "加载中…" : "选择一个已有目标"} />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    <SelectItem value={PICK_TARGET}>请选择…</SelectItem>
                    {candidates.map((t) => (
                      <SelectItem key={t.id} value={String(t.id)}>
                        {t.name}
                        {t.company_name ? ` · ${t.company_name}` : ""}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
              <p className="text-muted-foreground text-xs">
                已有目标会保留它在其他平台下的授权范围；这里只新增本平台的关联
              </p>
            </Field>
          )}

          <Field label="本平台授权范围(可选)">
            <Textarea
              value={scopeNote}
              onChange={(e) => setScopeNote(e.target.value)}
              rows={3}
              placeholder="仅限 *.acme.com 及其子域；不含第三方托管系统。"
            />
            <p className="text-muted-foreground text-xs">
              禁止事项 / 允许类型 / 速率限制等其余四类文本在目标详情页里编辑
            </p>
          </Field>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button disabled={busy} onClick={() => void submit()}>
            {busy ? "添加中…" : "添加"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
