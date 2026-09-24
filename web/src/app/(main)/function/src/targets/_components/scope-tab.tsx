"use client";

// 目标详情 · 授权范围页签(本页核心)。
//
// 关键模型:授权范围挂在「平台 × 目标」这一层,不是挂在目标上。同一目标在补天与
// 漏洞盒子下的授权范围是两份独立记录,所以这里必须先选平台、再编辑那一份。
// 页面上反复出现「只对当前平台生效」的提示,是因为这是该模型最容易被误解的地方——
// 用户改完 A 平台的规范后会以为 B 平台也跟着变了。
//
// 切换平台前若表单是 dirty 的会 confirm:否则未保存的编辑会被静默丢弃。

import * as React from "react";

import Link from "next/link";

import { ExternalLinkIcon, InfoIcon, LayersIcon, SaveIcon, UnlinkIcon } from "lucide-react";
import { toast } from "sonner";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { SRCTargetLink } from "@/lib/types";

import { Field, fmtTime, type SaveBridge } from "./shared";

interface ScopeForm {
  scope_note: string;
  forbidden: string;
  allowed_types: string;
  rate_limit: string;
  rules: string;
  enabled: boolean;
}

function formOf(link: SRCTargetLink | undefined): ScopeForm {
  return {
    scope_note: link?.scope_note ?? "",
    forbidden: link?.forbidden ?? "",
    allowed_types: link?.allowed_types ?? "",
    rate_limit: link?.rate_limit ?? "",
    rules: link?.rules ?? "",
    enabled: link?.enabled ?? true,
  };
}

export function ScopeTab({
  targetId,
  targetName,
  platformId,
  links,
  onPlatformChange,
  onLinksChanged,
  onSaveBridge,
}: {
  targetId: number;
  targetName: string;
  platformId: number;
  links: SRCTargetLink[];
  onPlatformChange: (platformId: number) => void;
  onLinksChanged: () => void;
  onSaveBridge: (b: SaveBridge | null) => void;
}) {
  const link = React.useMemo(() => links.find((l) => l.platform_id === platformId), [links, platformId]);
  const [form, setForm] = React.useState<ScopeForm>(() => formOf(link));
  const [saving, setSaving] = React.useState(false);
  const [unlinking, setUnlinking] = React.useState(false);
  const dirtyRef = React.useRef(false);
  const loadedRef = React.useRef(-1);
  // 保存动作要发给页面头部注册,必须保持引用稳定:若把 form 放进 useCallback 依赖,
  // 每次击键都会重新注册一次,头部跟着整页重渲染。用 ref 拿最新值。
  const formRef = React.useRef(form);
  const linkRef = React.useRef(link);
  React.useEffect(() => {
    formRef.current = form;
  }, [form]);
  React.useEffect(() => {
    linkRef.current = link;
  }, [link]);

  const set = (patch: Partial<ScopeForm>) => setForm((f) => ({ ...f, ...patch }));

  const dirty =
    !!link &&
    (form.scope_note !== link.scope_note ||
      form.forbidden !== link.forbidden ||
      form.allowed_types !== link.allowed_types ||
      form.rate_limit !== link.rate_limit ||
      form.rules !== link.rules ||
      form.enabled !== link.enabled);

  // dirty 先写进 ref,供下面的加载 effect 判断「能否用新数据覆盖表单」。
  React.useEffect(() => {
    dirtyRef.current = dirty;
  }, [dirty]);

  // 平台切换时无条件重载表单;同一平台下 links 被刷新(保存后/概览改动后)时,
  // 只在没有未保存改动的情况下同步,否则会把用户正在编辑的内容覆盖掉。
  React.useEffect(() => {
    if (!link) return;
    if (loadedRef.current !== platformId || !dirtyRef.current) {
      setForm(formOf(link));
      loadedRef.current = platformId;
    }
  }, [link, platformId]);

  const save = React.useCallback(async () => {
    const cur = linkRef.current;
    if (!cur) return;
    setSaving(true);
    try {
      await api.saveSRCTargetLink(targetId, platformId, formRef.current);
      toast.success(`已保存「${cur.platform_name || `平台 #${platformId}`}」下的授权范围`);
      onLinksChanged();
    } catch (e) {
      toast.error(`保存失败:${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }, [targetId, platformId, onLinksChanged]);

  React.useEffect(() => {
    onSaveBridge({ dirty, saving, save });
    return () => onSaveBridge(null);
  }, [dirty, saving, save, onSaveBridge]);

  const switchPlatform = (next: string) => {
    const nextId = Number(next);
    if (nextId === platformId) return;
    // 未保存内容会被丢弃,必须让用户确认——静默丢失编辑是这类多平台表单最常被投诉的点。
    if (dirty && !window.confirm("当前平台的授权范围有未保存的修改,切换平台会丢弃这些修改。继续切换?")) return;
    onPlatformChange(nextId);
  };

  const unlink = async () => {
    if (!link) return;
    const name = link.platform_name || `平台 #${platformId}`;
    if (
      !window.confirm(
        `解除「${targetName}」与「${name}」的关联?只解除本平台下的关联与该平台下的授权范围,目标本身不会被删除。`,
      )
    )
      return;
    setUnlinking(true);
    try {
      await api.unlinkSRCTarget(targetId, platformId);
      toast.success(`已解除与「${name}」的关联`);
      onLinksChanged();
    } catch (e) {
      toast.error(`解除关联失败:${(e as Error).message}`);
    } finally {
      setUnlinking(false);
    }
  };

  if (links.length === 0) {
    return (
      <Card>
        <CardContent className="flex flex-col items-center gap-3 py-16 text-center">
          <LayersIcon className="text-muted-foreground size-8 opacity-40" />
          <p className="text-muted-foreground text-sm">
            该目标还没有归属任何平台,因此没有可配置的授权范围。授权范围是「平台 × 目标」这一层的属性,
            先到平台详情页把本目标挂到平台下。
          </p>
          <Button variant="outline" size="sm" asChild>
            <Link href="/function/src/platforms">前往平台列表</Link>
          </Button>
        </CardContent>
      </Card>
    );
  }

  const currentName = link?.platform_name || `平台 #${platformId}`;

  return (
    <div className="grid gap-4">
      {/* 多对多模型的核心提示:放在表单上方,用户改任何字段前都会先看到它。 */}
      <Alert>
        <InfoIcon />
        <AlertTitle>授权范围按平台分别维护</AlertTitle>
        <AlertDescription>
          当前编辑的是「{currentName}」下这一份授权范围,<strong>只对该平台生效</strong>。同一目标在其他平台下
          有各自独立的授权范围,修改这里不会影响它们。共 {links.length} 个平台。
        </AlertDescription>
      </Alert>

      <Card>
        <CardHeader className="flex-row flex-wrap items-center justify-between gap-2">
          <div className="flex flex-wrap items-center gap-2">
            <CardTitle className="text-sm">编辑平台</CardTitle>
            <Select value={String(platformId)} onValueChange={switchPlatform}>
              <SelectTrigger className="w-56">
                <SelectValue placeholder="选择平台" />
              </SelectTrigger>
              <SelectContent>
                {links.map((l) => (
                  <SelectItem key={l.platform_id} value={String(l.platform_id)}>
                    {l.platform_name || `平台 #${l.platform_id}`}
                    {l.enabled ? "" : "(停用)"}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {dirty && (
              <Badge variant="outline" className="bg-amber-500/15 text-amber-600 border-amber-500/30">
                有未保存修改
              </Badge>
            )}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="outline" size="sm" asChild>
              <Link href={`/function/src/platforms/detail?id=${platformId}`}>
                <ExternalLinkIcon className="size-4" />
                平台规范
              </Link>
            </Button>
            <Button variant="ghost" size="sm" disabled={unlinking} onClick={() => void unlink()}>
              <UnlinkIcon className="size-4 text-red-600" />
              解除与本平台的关联
            </Button>
          </div>
        </CardHeader>
        <CardContent className="grid gap-4">
          <div className="text-muted-foreground flex flex-wrap items-center gap-x-4 gap-y-1 text-xs">
            <span>
              平台:<span className="text-foreground font-medium">{currentName}</span>
            </span>
            <span>目标:{targetName}</span>
            <span>最近更新:{fmtTime(link?.updated_at)}</span>
          </div>

          <Field label="授权范围说明">
            <Textarea
              value={form.scope_note}
              onChange={(e) => set({ scope_note: e.target.value })}
              rows={2}
              placeholder="例:仅限 *.example.edu.cn 及其子域;不含第三方托管系统"
            />
          </Field>
          <Field label="禁止事项(每行一条,逐条作为硬约束注入)">
            <Textarea
              value={form.forbidden}
              onChange={(e) => set({ forbidden: e.target.value })}
              rows={5}
              placeholder={"例:\n禁止对生产数据库执行写操作\n禁止上传 webshell\n禁止 DoS / 压力测试\n禁止社工与钓鱼"}
            />
          </Field>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Field label="允许的测试类型">
              <Input
                value={form.allowed_types}
                onChange={(e) => set({ allowed_types: e.target.value })}
                placeholder="仅 Web 漏洞 / 含 API 测试"
              />
            </Field>
            <Field label="速率 / 强度限制">
              <Input
                value={form.rate_limit}
                onChange={(e) => set({ rate_limit: e.target.value })}
                placeholder="例:单目标 ≤ 10 请求/秒;禁用爆破"
              />
            </Field>
          </div>
          <Field label="补充规范(Markdown,整体作为约束注入)">
            <Textarea
              className="font-mono text-xs"
              value={form.rules}
              onChange={(e) => set({ rules: e.target.value })}
              rows={5}
              placeholder="本平台特有的规则、报告格式要求、提交注意事项…"
            />
          </Field>
          <div className="flex items-center gap-3">
            <Switch checked={form.enabled} onCheckedChange={(v) => set({ enabled: v })} />
            <div>
              <p className="font-medium text-sm">启用本平台下的授权范围</p>
              <p className="text-muted-foreground text-xs">
                停用后建任务时不注入这一份约束(平台级规范仍会注入)。
              </p>
            </div>
          </div>

          <div className="flex items-center justify-end gap-2">
            <Button variant="outline" size="sm" disabled={!dirty} onClick={() => setForm(formOf(link))}>
              还原
            </Button>
            <Button size="sm" disabled={!dirty || saving} onClick={() => void save()}>
              <SaveIcon className="size-4" />
              {saving ? "保存中…" : "保存本平台授权范围"}
            </Button>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
