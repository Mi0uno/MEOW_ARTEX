"use client";

// SRC 平台详情:一个平台下的全部配置与产物。
//
// 四个页签分工:
//   概览       —— 平台自身的名称/主页/描述/启用状态 + 统计;
//   规范与文件 —— 四类规范文本域与附件(附件解析正文会和文本域一起注入 AI 约束);
//   目标       —— 该平台收录的目标(点进去才管资产与授权范围);
//   成果       —— 该平台下的 SRC 成果。
// 页签划分的依据是「改动的粒度」:规范文本域与附件是平台级配置,目标与成果是平台下的数据。

import * as React from "react";

import Link from "next/link";
import { useSearchParams } from "next/navigation";

import { ArrowLeftIcon, Loader2Icon, ShieldAlertIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { SRCPlatform } from "@/lib/types";

// 详情页在 platforms/detail/ 下,共享组件在 platforms/_components/ 里,故用 ../
import { OverviewTab } from "../_components/overview-tab";
import { fmtTime } from "../_components/platform-shared";
import { RewardsTab } from "../_components/rewards-tab";
import { SpecTab } from "../_components/spec-tab";
import { TargetsTab } from "../_components/targets-tab";

// useSearchParams must sit under a Suspense boundary for static export.
export default function SRCPlatformDetailPage() {
  return (
    <React.Suspense fallback={null}>
      <SRCPlatformDetailInner />
    </React.Suspense>
  );
}

function SRCPlatformDetailInner() {
  const searchParams = useSearchParams();
  const id = Number(searchParams.get("id") ?? "0");
  // 无效 id 必须在此处(任何 hook 之前)返回——把「读 id」与「用 id」拆成两个组件,
  // 才能既早退又不在同一个组件里条件调用 hook。
  if (!Number.isFinite(id) || id <= 0) {
    return <MissingPlatform reason="未指定平台。请从「平台选择」列表进入。" />;
  }
  return <SRCPlatformDetailBody platformId={id} />;
}

function MissingPlatform({ reason }: { reason: string }) {
  return (
    <div className="p-4 md:p-6">
      <div className="text-muted-foreground flex flex-col items-center gap-3 py-16 text-center text-sm">
        <ShieldAlertIcon className="size-8 opacity-40" />
        <p>{reason}</p>
        <Button asChild variant="outline" size="sm">
          <Link href="/function/src/platforms">
            <ArrowLeftIcon className="size-4" />
            返回平台列表
          </Link>
        </Button>
      </div>
    </div>
  );
}

function SRCPlatformDetailBody({ platformId }: { platformId: number }) {
  const [tab, setTab] = React.useState("overview");
  const [platform, setPlatform] = React.useState<SRCPlatform | null>(null);
  // form 是「待提交的草稿」:四个页签里的编辑都写进它,由头部「保存」统一提交,
  // 避免每个页签各自发请求导致半套配置生效(规范文本域与启用状态必须同批落库)。
  const [form, setForm] = React.useState<Partial<SRCPlatform>>({});
  const [missing, setMissing] = React.useState(false);
  const [loading, setLoading] = React.useState(true);
  const [saving, setSaving] = React.useState(false);

  React.useEffect(() => {
    setLoading(true);
    api
      .getSRCPlatform(platformId)
      .then((p) => {
        // 后端 404 会抛错(走 catch),但 mock/异常后端可能回空对象——同样按「不存在」处理。
        if (!p || !p.id) {
          setMissing(true);
          return;
        }
        setPlatform(p);
        setForm(p);
      })
      .catch(() => setMissing(true))
      .finally(() => setLoading(false));
  }, [platformId]);

  // 只刷新统计(目标/资产/成果数),不动 form:挂完目标回来若重置 form,用户未保存的规范草稿就丢了。
  const refreshStats = React.useCallback(() => {
    api
      .getSRCPlatform(platformId)
      .then((p) => {
        if (p?.id) setPlatform(p);
      })
      .catch(() => {
        // 统计刷新失败不打扰用户:主数据没变,下次操作会再刷。
      });
  }, [platformId]);

  const set = (patch: Partial<SRCPlatform>) => setForm((f) => ({ ...f, ...patch }));

  const save = async () => {
    const name = (form.name ?? "").trim();
    if (!name) {
      toast.error("平台名称必填");
      return;
    }
    setSaving(true);
    try {
      const r = await api.saveSRCPlatform({ ...form, name });
      setPlatform(r.platform);
      setForm(r.platform);
      toast.success("平台已保存");
    } catch (e) {
      // 重名由后端判 409,原文透出(「同名平台已存在」比「保存失败」可操作)。
      toast.error(`保存失败:${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return <p className="text-muted-foreground p-4 py-16 text-center text-sm md:p-6">加载中…</p>;
  }
  if (missing || !platform) {
    return <MissingPlatform reason={`平台 #${platformId} 不存在，可能已被删除。`} />;
  }

  // 脏检查只为在头部提示「有未保存改动」;用四个规范字段一起比,少比一个就会出现
  // 「明明改了却没提示」的假阴性。
  const dirty =
    (form.name ?? "") !== platform.name ||
    (form.homepage ?? "") !== platform.homepage ||
    (form.description ?? "") !== platform.description ||
    (form.reward_standard ?? "") !== platform.reward_standard ||
    (form.scoring_standard ?? "") !== platform.scoring_standard ||
    (form.code_of_conduct ?? "") !== platform.code_of_conduct ||
    (form.rules ?? "") !== platform.rules ||
    (form.enabled ?? true) !== platform.enabled;

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="mb-1 flex items-center gap-2">
            <Button asChild variant="ghost" size="icon-sm" title="返回平台列表">
              <Link href="/function/src/platforms" aria-label="返回平台列表">
                <ArrowLeftIcon className="size-4" />
              </Link>
            </Button>
            <h1 className="truncate font-semibold text-xl tracking-tight">{platform.name}</h1>
          </div>
          <p className="text-muted-foreground font-mono text-xs">
            {platform.homepage || "未填主页"}
            {` · 更新于 ${fmtTime(platform.updated_at)}`}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {dirty && <span className="text-amber-600 text-xs">有未保存的改动</span>}
          <Button size="sm" disabled={saving} onClick={() => void save()}>
            {saving && <Loader2Icon className="size-4 animate-spin" />}
            {saving ? "保存中…" : "保存"}
          </Button>
        </div>
      </div>

      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="overview">概览</TabsTrigger>
          <TabsTrigger value="spec">规范与文件</TabsTrigger>
          <TabsTrigger value="targets">目标 {platform.target_count ?? 0}</TabsTrigger>
          <TabsTrigger value="rewards">成果 {platform.reward_count ?? 0}</TabsTrigger>
        </TabsList>

        <TabsContent value="overview" className="mt-3">
          <OverviewTab platform={platform} form={form} set={set} onSave={() => void save()} saving={saving} />
        </TabsContent>

        <TabsContent value="spec" className="mt-3">
          <SpecTab platformId={platformId} form={form} set={set} onSave={() => void save()} saving={saving} />
        </TabsContent>

        <TabsContent value="targets" className="mt-3">
          <TargetsTab platformId={platformId} onChanged={refreshStats} />
        </TabsContent>

        <TabsContent value="rewards" className="mt-3">
          <RewardsTab platformId={platformId} />
        </TabsContent>
      </Tabs>
    </div>
  );
}
