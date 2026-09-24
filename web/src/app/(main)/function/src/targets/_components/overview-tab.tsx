"use client";

// 目标详情 · 概览页签:目标本体的可编辑字段 + 统计 + 所属平台清单。
//
// 表单状态留在本组件内,通过 onSaveBridge 把「保存」动作交给页面头部统一渲染的按钮,
// 这样头部与页签不需要共享 form state(否则资产表格会被每次击键重渲染)。

import * as React from "react";

import Link from "next/link";

import { ArrowRightIcon, Building2Icon, LayersIcon, TargetIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { Company, SRCTarget, SRCTargetLink } from "@/lib/types";

import { Field, fmtTime, type SaveBridge } from "./shared";

// Radix Select 的 value="" 是保留值(表示未选中),「不关联企业」必须用哨兵串。
const NO_COMPANY = "__none__";

export function OverviewTab({
  target,
  links,
  onSaved,
  onSaveBridge,
}: {
  target: SRCTarget;
  links: SRCTargetLink[];
  onSaved: (t: SRCTarget) => void;
  onSaveBridge: (b: SaveBridge | null) => void;
}) {
  const [companies, setCompanies] = React.useState<Company[]>([]);
  const [name, setName] = React.useState(target.name);
  const [companyID, setCompanyID] = React.useState(target.company_id ? String(target.company_id) : NO_COMPANY);
  const [description, setDescription] = React.useState(target.description);
  const [enabled, setEnabled] = React.useState(target.enabled);
  const [saving, setSaving] = React.useState(false);
  const dirtyRef = React.useRef(false);
  // 保存动作注册给页面头部,必须引用稳定(否则每次击键都重新注册,头部跟着整页重渲染)。
  // 最新值走 ref,不进 useCallback 依赖;保存只由用户点击触发,effect 早已同步完 ref。
  const targetRef = React.useRef(target);
  const nameRef = React.useRef(name);
  const companyIDRef = React.useRef(companyID);
  const descriptionRef = React.useRef(description);
  const enabledRef = React.useRef(enabled);
  const onSavedRef = React.useRef(onSaved);

  React.useEffect(() => {
    targetRef.current = target;
    nameRef.current = name;
    companyIDRef.current = companyID;
    descriptionRef.current = description;
    enabledRef.current = enabled;
    onSavedRef.current = onSaved;
  }, [target, name, companyID, description, enabled, onSaved]);

  React.useEffect(() => {
    api
      .companies()
      .then(setCompanies)
      .catch(() => undefined);
  }, []);

  const dirty =
    name !== target.name ||
    description !== target.description ||
    enabled !== target.enabled ||
    companyID !== (target.company_id ? String(target.company_id) : NO_COMPANY);

  React.useEffect(() => {
    dirtyRef.current = dirty;
  }, [dirty]);

  // 目标被外部替换(保存后刷新)时同步回表单;但用户正在编辑时不覆盖——
  // 保存期间继续敲字是常见操作,静默回滚会让人以为改动丢了。
  React.useEffect(() => {
    if (dirtyRef.current) return;
    setName(target.name);
    setCompanyID(target.company_id ? String(target.company_id) : NO_COMPANY);
    setDescription(target.description);
    setEnabled(target.enabled);
  }, [target]);

  const save = React.useCallback(async () => {
    const cur = targetRef.current;
    if (!nameRef.current.trim()) {
      toast.error("目标名称不能为空");
      return;
    }
    setSaving(true);
    try {
      // 「不关联企业」用 undefined(JSON 里直接省略该字段):后端 SRCTargetInput.CompanyID 是
      // *int64,字段缺省即 nil,UpdateSRCTarget 会把它写成 NULL。传 null 则与类型
      // company_id?: number 冲突,传 0 又会被后端当成「无意义值」而不生效。
      const { target: saved } = await api.saveSRCTarget({
        id: cur.id,
        name: nameRef.current.trim(),
        company_id: companyIDRef.current === NO_COMPANY ? undefined : Number(companyIDRef.current),
        description: descriptionRef.current,
        enabled: enabledRef.current,
      });
      toast.success("已保存");
      onSavedRef.current(saved);
    } catch (e) {
      toast.error(`保存失败:${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }, []);

  // 把保存动作注册给头部;依赖 dirty/save 变化时重新注册,头部按钮才拿得到最新状态。
  React.useEffect(() => {
    onSaveBridge({ dirty, saving, save });
    return () => onSaveBridge(null);
  }, [dirty, saving, save, onSaveBridge]);

  return (
    <div className="grid gap-4">
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
        <StatCard icon={<LayersIcon className="size-4" />} label="所属平台" value={links.length} />
        <StatCard icon={<TargetIcon className="size-4" />} label="资产数" value={target.asset_count ?? 0} />
        <StatCard icon={<Building2Icon className="size-4" />} label="成果数" value={target.reward_count ?? 0} />
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-sm">目标信息</CardTitle>
        </CardHeader>
        <CardContent className="grid gap-4">
          <Field label="目标名称">
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="例:Acme Corp / 某某大学" />
          </Field>
          <Field label="关联企业">
            <Select value={companyID} onValueChange={setCompanyID}>
              <SelectTrigger className="w-full sm:max-w-sm">
                <SelectValue placeholder="不关联企业" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={NO_COMPANY}>不关联企业</SelectItem>
                {companies.map((c) => (
                  <SelectItem key={c.id} value={String(c.id)}>
                    {c.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-muted-foreground text-xs">
              关联企业后,信息搜集暂存区与已有资产才能按企业筛选出来导入到本目标。
            </p>
          </Field>
          <Field label="描述">
            <Textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              rows={3}
              placeholder="例:主营业务为电商与支付,核心资产在 acme.com。"
            />
          </Field>
          <div className="flex items-center gap-3">
            <Switch checked={enabled} onCheckedChange={setEnabled} />
            <div>
              <p className="font-medium text-sm">启用</p>
              <p className="text-muted-foreground text-xs">停用后该目标不再出现在建任务的目标选择里。</p>
            </div>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-sm">所属平台</CardTitle>
        </CardHeader>
        <CardContent className="px-0">
          {links.length === 0 ? (
            <p className="text-muted-foreground px-6 py-8 text-center text-sm">
              该目标还没有归属任何平台。到平台详情页把本目标挂到平台下,才能配置授权范围与资产。
            </p>
          ) : (
            <Table className="table-fixed">
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  {/* 平台名列不设宽度:table-fixed 下自动吃掉余量。 */}
                  <TableHead>平台</TableHead>
                  <TableHead className="w-20">状态</TableHead>
                  <TableHead className="w-44">更新时间</TableHead>
                  <TableHead className="w-20">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {links.map((l) => (
                  <TableRow key={l.id}>
                    <TableCell className="min-w-0">
                      <Link
                        href={`/function/src/platforms/detail?id=${l.platform_id}`}
                        className="block w-full truncate text-left font-medium hover:underline"
                        title={l.platform_name || `平台 #${l.platform_id}`}
                      >
                        {l.platform_name || `平台 #${l.platform_id}`}
                      </Link>
                    </TableCell>
                    <TableCell>
                      <Badge
                        variant="outline"
                        className={l.enabled ? "bg-emerald-500/15 text-emerald-600 border-emerald-500/30" : "bg-muted text-muted-foreground"}
                      >
                        {l.enabled ? "启用" : "停用"}
                      </Badge>
                    </TableCell>
                    <TableCell className="text-muted-foreground text-xs">{fmtTime(l.updated_at)}</TableCell>
                    <TableCell>
                      <Button variant="ghost" size="icon-xs" asChild title="进入平台详情">
                        <Link href={`/function/src/platforms/detail?id=${l.platform_id}`} aria-label="进入平台详情">
                          <ArrowRightIcon className="size-3.5" />
                        </Link>
                      </Button>
                    </TableCell>
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

function StatCard({ icon, label, value }: { icon: React.ReactNode; label: string; value: number }) {
  return (
    <Card>
      <CardContent className="flex items-center gap-3 pt-4">
        <span className="bg-muted text-muted-foreground flex size-8 shrink-0 items-center justify-center rounded-lg">
          {icon}
        </span>
        <div className="min-w-0">
          <p className="text-muted-foreground text-xs">{label}</p>
          <p className="font-semibold text-xl tabular-nums">{value}</p>
        </div>
      </CardContent>
    </Card>
  );
}
