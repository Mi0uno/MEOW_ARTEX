"use client";

// SRC 模式 · 企业模式:选企业 → 查看/配置该企业的 SRC 规范 → 进入任务页建 SRC 任务。
//
// 企业 SRC 规范是「防止 AI 越界」的关键:规范文本会转成任务的 deny/allow 约束
// 注入 Planner/Worker 系统提示,框定授权边界(授权范围、禁止事项、速率限制)。

import * as React from "react";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { Building2Icon, FileTextIcon, PencilIcon, SearchIcon, ShieldCheckIcon, TargetIcon } from "lucide-react";
import { toast } from "sonner";

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
import { TablePagination } from "@/components/table-pagination";
import { api } from "@/lib/api";
import type { Company, SRCProfile } from "@/lib/types";

const PAGE_SIZE = 20;

export default function SRCEnterprisePage() {
  const router = useRouter();
  const [profiles, setProfiles] = React.useState<SRCProfile[]>([]);
  const [companies, setCompanies] = React.useState<Company[]>([]);
  const [total, setTotal] = React.useState(0);
  const [query, setQuery] = React.useState("");
  const [companyFilter, setCompanyFilter] = React.useState("all");
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);
  const [loading, setLoading] = React.useState(true);
  const [editing, setEditing] = React.useState<SRCProfile | null>(null);
  const [creating, setCreating] = React.useState(false);

  const load = React.useCallback((q: string, cid: string, p: number, size: number) => {
    setLoading(true);
    api
      .srcProfiles({
        q,
        companyId: cid === "all" ? undefined : Number(cid),
        limit: size,
        offset: (p - 1) * size,
      })
      .then((r) => {
        setProfiles(r.profiles);
        setTotal(r.total);
      })
      .catch((e) => toast.error(`加载失败:${(e as Error).message}`))
      .finally(() => setLoading(false));
  }, []);

  React.useEffect(() => {
    api
      .companies()
      .then(setCompanies)
      .catch(() => undefined);
  }, []);

  React.useEffect(() => {
    load(query, companyFilter, page, pageSize);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [companyFilter, page, pageSize]);

  const search = () => {
    setPage(1);
    load(query, companyFilter, 1, pageSize);
  };
  const reload = () => load(query, companyFilter, page, pageSize);

  const remove = async (p: SRCProfile) => {
    if (!window.confirm(`删除「${p.company_name || p.name}」的 SRC 规范?`)) return;
    try {
      await api.deleteSRCProfile(p.id);
      toast.success("已删除");
      reload();
    } catch (e) {
      toast.error(`删除失败:${(e as Error).message}`);
    }
  };

  // 已有规范的企业(新建时排除,一个企业一套规范)。
  const configured = new Set(profiles.map((p) => p.company_id));

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div>
          <h1 className="font-semibold text-xl">SRC 模式 · 企业模式</h1>
          <p className="text-muted-foreground text-sm">
            为企业配置 SRC 规范(授权范围 / 禁止事项 / 速率限制),规范会作为硬约束注入任务,防止 AI 越界
          </p>
        </div>
        <Button size="sm" onClick={() => setCreating(true)}>
          <ShieldCheckIcon className="size-4" />
          配置 SRC 规范
        </Button>
      </div>

      <Card>
        <CardContent className="px-0 pt-4">
          <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
            <div className="relative w-full sm:max-w-xs">
              <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
              <Input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && search()}
                placeholder="搜索企业 / 平台 / 规范名"
                className="pl-8"
              />
            </div>
            <Button variant="outline" size="sm" onClick={search}>
              搜索
            </Button>
            <Select
              value={companyFilter}
              onValueChange={(v) => {
                setPage(1);
                setCompanyFilter(v);
              }}
            >
              <SelectTrigger className="w-48">
                <SelectValue placeholder="企业" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="all">全部企业</SelectItem>
                  {companies.map((c) => (
                    <SelectItem key={c.id} value={String(c.id)}>
                      {c.name}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            <span className="text-muted-foreground ml-auto text-xs tabular-nums">共 {total} 家</span>
          </div>

          {loading && profiles.length === 0 ? (
            <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
          ) : profiles.length === 0 ? (
            <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
              <ShieldCheckIcon className="size-8 opacity-40" />
              <span>还没有企业配置 SRC 规范。点「配置 SRC 规范」为某家企业设定授权边界。</span>
            </div>
          ) : (
            <>
              <Table>
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    <TableHead>企业</TableHead>
                    <TableHead className="w-36">SRC 平台</TableHead>
                    <TableHead>授权范围</TableHead>
                    <TableHead className="w-24">状态</TableHead>
                    <TableHead className="w-44">更新时间</TableHead>
                    <TableHead className="w-44">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {profiles.map((p) => (
                    <TableRow key={p.id}>
                      <TableCell className="font-medium">
                        <span className="inline-flex items-center gap-2">
                          <Building2Icon className="text-muted-foreground size-4" />
                          {p.company_name || p.name || `企业 #${p.company_id}`}
                        </span>
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs">{p.platform || "—"}</TableCell>
                      <TableCell className="text-muted-foreground max-w-72 truncate text-xs">
                        {p.scope_note || "—"}
                      </TableCell>
                      <TableCell>
                        <Badge
                          variant="outline"
                          className={p.enabled ? "text-emerald-600" : "text-muted-foreground"}
                        >
                          {p.enabled ? "已启用" : "已停用"}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs">
                        {new Date(p.updated_at).toLocaleString()}
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center gap-1">
                          <Button variant="ghost" size="sm" onClick={() => setEditing(p)}>
                            <PencilIcon className="size-3.5" />
                            编辑
                          </Button>
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => router.push(`/function/tasks?company=${p.company_id}`)}
                          >
                            <TargetIcon className="size-3.5" />
                            建任务
                          </Button>
                          <Button variant="ghost" size="sm" onClick={() => void remove(p)}>
                            删除
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
      </Card>

      <Card className="mt-4">
        <CardContent className="flex flex-wrap items-center gap-3 pt-4 text-sm">
          <FileTextIcon className="text-muted-foreground size-4" />
          <span className="text-muted-foreground">
            规范生效方式:创建 SRC 任务时自动把该企业的规范转成任务的 allow/deny 约束,注入 Planner 与 Worker 系统提示。
          </span>
          <Button variant="outline" size="sm" className="ml-auto" asChild>
            <Link href="/function/src/rewards">查看成果产出</Link>
          </Button>
        </CardContent>
      </Card>

      <SRCProfileDialog
        profile={editing}
        open={creating || !!editing}
        companies={companies.filter((c) => !configured.has(c.id) || c.id === editing?.company_id)}
        onOpenChange={(o) => {
          if (!o) {
            setCreating(false);
            setEditing(null);
          }
        }}
        onSaved={() => {
          setCreating(false);
          setEditing(null);
          reload();
        }}
      />
    </div>
  );
}

// SRCProfileDialog 配置某企业的 SRC 规范。
function SRCProfileDialog({
  profile,
  open,
  companies,
  onOpenChange,
  onSaved,
}: {
  profile: SRCProfile | null;
  open: boolean;
  companies: Company[];
  onOpenChange: (v: boolean) => void;
  onSaved: () => void;
}) {
  const [form, setForm] = React.useState<Partial<SRCProfile>>({});
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    if (!open) return;
    setForm(
      profile ?? {
        platform: "",
        name: "",
        scope_note: "",
        forbidden: "",
        allowed_types: "",
        rate_limit: "",
        rules: "",
        enabled: true,
      },
    );
  }, [open, profile]);

  const set = (k: keyof SRCProfile, v: unknown) => setForm((f) => ({ ...f, [k]: v }));

  const submit = async () => {
    if (!profile && !form.company_id) {
      toast.error("请选择企业");
      return;
    }
    setBusy(true);
    try {
      await api.saveSRCProfile(form);
      toast.success("已保存");
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
          <DialogTitle>{profile ? `编辑 SRC 规范 · ${profile.company_name}` : "配置企业 SRC 规范"}</DialogTitle>
          <DialogDescription>
            规范文本会转成任务的 deny/allow 约束注入 Planner 与 Worker 系统提示,是防止 AI 越界的硬边界
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          {!profile && (
            <div className="grid gap-1.5">
              <Label>企业</Label>
              <Select value={String(form.company_id ?? "")} onValueChange={(v) => set("company_id", Number(v))}>
                <SelectTrigger>
                  <SelectValue placeholder="选择企业" />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {companies.map((c) => (
                      <SelectItem key={c.id} value={String(c.id)}>
                        {c.name}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </div>
          )}
          <div className="grid grid-cols-2 gap-3">
            <div className="grid gap-1.5">
              <Label>规范名称(可选)</Label>
              <Input
                value={String(form.name ?? "")}
                onChange={(e) => set("name", e.target.value)}
                placeholder="例:2026 Q1 教育 SRC"
              />
            </div>
            <div className="grid gap-1.5">
              <Label>SRC 平台</Label>
              <Input
                value={String(form.platform ?? "")}
                onChange={(e) => set("platform", e.target.value)}
                placeholder="补天 / 漏洞盒子 / 教育SRC / 厂商自建"
              />
            </div>
          </div>
          <div className="grid gap-1.5">
            <Label>授权范围说明</Label>
            <Textarea
              value={String(form.scope_note ?? "")}
              onChange={(e) => set("scope_note", e.target.value)}
              rows={2}
              placeholder="例:仅限 *.example.edu.cn 及其子域;不含第三方托管系统"
            />
          </div>
          <div className="grid gap-1.5">
            <Label>禁止事项(每行一条,逐条作为硬约束注入)</Label>
            <Textarea
              value={String(form.forbidden ?? "")}
              onChange={(e) => set("forbidden", e.target.value)}
              rows={5}
              placeholder={"例:\n禁止对生产数据库执行写操作\n禁止上传 webshell\n禁止 DoS / 压力测试\n禁止社工与钓鱼\n禁止触碰非授权域名"}
            />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div className="grid gap-1.5">
              <Label>允许的测试类型</Label>
              <Input
                value={String(form.allowed_types ?? "")}
                onChange={(e) => set("allowed_types", e.target.value)}
                placeholder="仅 Web 漏洞 / 含 API 测试"
              />
            </div>
            <div className="grid gap-1.5">
              <Label>速率 / 强度限制</Label>
              <Input
                value={String(form.rate_limit ?? "")}
                onChange={(e) => set("rate_limit", e.target.value)}
                placeholder="例:单目标 ≤ 10 请求/秒;禁用爆破"
              />
            </div>
          </div>
          <div className="grid gap-1.5">
            <Label>额外规范(Markdown,整体作为约束注入)</Label>
            <Textarea
              className="font-mono text-xs"
              value={String(form.rules ?? "")}
              onChange={(e) => set("rules", e.target.value)}
              rows={4}
              placeholder="平台特有规则、报告格式要求、提交注意事项…"
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
