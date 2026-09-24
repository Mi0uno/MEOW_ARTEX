"use client";

// SRC 模式 · 目标详情(?id=目标 id&platform=平台 id;静态导出没有动态路由,只能用 query)。
//
// 一个目标可以同时挂在多个平台下,而授权范围/资产都是「平台 × 目标」这一层的属性——
// 所以本页维护一个「当前平台」,由「授权范围」页签的切换器驱动,「资产」页签跟随它。
// 换平台不是换目标,页面头部(名称/企业)不随平台变化。

import * as React from "react";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";

import { ArrowLeftIcon, RocketIcon, SaveIcon, TargetIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { CreateSRCTaskDialog } from "@/components/src-create-task-dialog";
import { api } from "@/lib/api";
import type { SRCPlatform, SRCTarget, SRCTargetLink } from "@/lib/types";

import { AssetsTab } from "../_components/assets-tab";
import { OverviewTab } from "../_components/overview-tab";
import { RewardsTab } from "../_components/rewards-tab";
import { ScopeTab } from "../_components/scope-tab";
import type { SaveBridge } from "../_components/shared";

// useSearchParams 必须包在 Suspense 里(静态导出硬约束)。
export default function SRCTargetDetailPage() {
  return (
    <React.Suspense fallback={null}>
      <SRCTargetDetailInner />
    </React.Suspense>
  );
}

function SRCTargetDetailInner() {
  const searchParams = useSearchParams();
  const router = useRouter();
  const id = Number(searchParams.get("id") ?? 0);
  const platformParam = Number(searchParams.get("platform") ?? 0);

  const [target, setTarget] = React.useState<SRCTarget | null>(null);
  const [links, setLinks] = React.useState<SRCTargetLink[]>([]);
  const [missing, setMissing] = React.useState(false);
  const [loaded, setLoaded] = React.useState(false);
  const [tab, setTab] = React.useState("overview");
  const [platformId, setPlatformId] = React.useState(0);
  // 建任务对话框要拿到平台对象(它用平台名渲染注入说明),而本页只有 link(含 platform_name)。
  // 按需拉一次当前平台,拉不到就退化用一个只带 id/name 的最小对象——不该因为统计接口失败
  // 就让用户建不了任务。
  const [taskPlatform, setTaskPlatform] = React.useState<SRCPlatform | null>(null);
  const [createOpen, setCreateOpen] = React.useState(false);
  // 头部保存按钮由页签注册进来(只有概览/授权范围有可保存的表单)。
  // 两个页签都被 forceMount 常驻,所以按页签名分槽存——共用一个槽会互相覆盖,
  // 结果是「概览」页签上永远看不到保存按钮。
  const [bridges, setBridges] = React.useState<Record<string, SaveBridge | undefined>>({});
  // 首次加载时把 ?platform= 或第一个平台设为当前平台;之后由用户切换,不再被数据覆盖。
  const platformInitRef = React.useRef(false);

  const loadTarget = React.useCallback(
    (targetId: number) =>
      api
        .getSRCTarget(targetId)
        .then((t) => {
          setTarget(t);
          return t;
        })
        .catch(() => {
          setMissing(true);
          return null;
        }),
    [],
  );

  const loadLinks = React.useCallback(
    (targetId: number) =>
      api
        .srcTargetLinks(targetId)
        .then((ls) => {
          setLinks(ls);
          return ls;
        })
        .catch(() => {
          setLinks([]);
          return [];
        }),
    [],
  );

  React.useEffect(() => {
    if (!Number.isFinite(id) || id <= 0) {
      setMissing(true);
      setLoaded(true);
      return;
    }
    let alive = true;
    // 两个请求都已各自 catch,这里只负责在两者都结束后收起加载态;void 显式忽略返回值。
    void Promise.all([loadTarget(id), loadLinks(id)]).finally(() => {
      if (alive) setLoaded(true);
    });
    return () => {
      alive = false;
    };
  }, [id, loadTarget, loadLinks]);

  // 当前平台:优先用 ?platform=(从平台详情跳进来时带的上下文),否则取第一个平台。
  React.useEffect(() => {
    if (!loaded || platformInitRef.current || links.length === 0) return;
    const preferred = links.find((l) => l.platform_id === platformParam);
    setPlatformId(preferred?.platform_id ?? links[0].platform_id);
    platformInitRef.current = true;
  }, [loaded, links, platformParam]);

  // 解除关联后当前平台可能已经不在列表里,兜底切到第一个,避免表单指向不存在的 link。
  // links 被清空(最后一个平台也解除了)时归零,否则资产页签会继续按一个已失效的
  // platform_id 过滤,表现为「明明有资产却是空列表」。
  React.useEffect(() => {
    if (links.length === 0) {
      setPlatformId(0);
      // 复位初始化标记:最后一个平台被解除后若又挂上平台,要让初始化 effect 重新跑一遍,
      // 否则 platformId 会一直停在 0,授权范围表单没有数据可编。
      platformInitRef.current = false;
      return;
    }
    // platformId=0 表示「还没选定」:交给上面的初始化 effect 决定(?platform= 优先)。
    // 这里若也抢着写 links[0],同一批次里两个 setState 会以最后一个为准,
    // 结果就是 ?platform= 指到非首个平台时被静默覆盖。
    if (platformId === 0) return;
    if (!links.some((l) => l.platform_id === platformId)) setPlatformId(links[0].platform_id);
  }, [links, platformId]);

  const reload = React.useCallback(() => {
    void loadTarget(id);
    void loadLinks(id);
  }, [id, loadTarget, loadLinks]);

  // 当前平台对象(建任务对话框要用平台名与 id)。platformId 变化时重拉;
  // 失败时用 link 里的名字兜底,保证建任务入口永远可用。
  React.useEffect(() => {
    if (platformId <= 0) {
      setTaskPlatform(null);
      return;
    }
    let alive = true;
    api
      .getSRCPlatform(platformId)
      .then((p) => {
        if (alive && p?.id) setTaskPlatform(p);
      })
      .catch(() => {
        if (!alive) return;
        const link = links.find((l) => l.platform_id === platformId);
        setTaskPlatform({
          id: platformId,
          name: link?.platform_name || `平台 #${platformId}`,
          homepage: "",
          description: "",
          reward_standard: "",
          scoring_standard: "",
          code_of_conduct: "",
          rules: "",
          enabled: true,
          created_at: "",
          updated_at: "",
        });
      });
    return () => {
      alive = false;
    };
  }, [platformId, links]);

  // 这几个回调必须是稳定引用:页签把它们放进 effect 依赖里注册「保存」动作,
  // 每次渲染都换新函数会导致 effect 反复重跑 → setBridge → 再渲染的死循环。
  const onOverviewSaved = React.useCallback(
    (t: SRCTarget) => {
      setTarget(t);
      reload();
    },
    [reload],
  );
  const onOverviewBridge = React.useCallback((b: SaveBridge | null) => {
    setBridges((prev) => ({ ...prev, overview: b ?? undefined }));
  }, []);
  const onScopeBridge = React.useCallback((b: SaveBridge | null) => {
    setBridges((prev) => ({ ...prev, scope: b ?? undefined }));
  }, []);

  const remove = async () => {
    if (!target) return;
    if (
      !window.confirm(
        `删除目标「${target.name}」?该目标在全部平台下的授权范围与资产关联会一并删除(资产本体不受影响)。此操作不可恢复。`,
      )
    )
      return;
    try {
      await api.deleteSRCTarget(target.id);
      toast.success("目标已删除");
      router.push("/function/src/platforms");
    } catch (e) {
      toast.error(`删除失败:${(e as Error).message}`);
    }
  };

  // 返回出口带上平台上下文:从平台详情进来的,退回那个平台;否则回平台列表。
  const backHref = platformParam > 0 ? `/function/src/platforms/detail?id=${platformParam}` : "/function/src/platforms";

  if (missing) {
    return (
      <div className="p-4 md:p-6">
        <Card>
          <CardContent className="flex flex-col items-center gap-3 py-16 text-center">
            <TargetIcon className="text-muted-foreground size-8 opacity-40" />
            <p className="text-muted-foreground text-sm">目标不存在或已被删除(参数缺失时也会看到这一页)。</p>
            <Button variant="outline" size="sm" asChild>
              <Link href="/function/src/platforms">返回平台列表</Link>
            </Button>
          </CardContent>
        </Card>
      </div>
    );
  }

  if (!target) {
    return <div className="text-muted-foreground p-4 text-center text-sm md:p-6">加载中…</div>;
  }

  const currentLink = links.find((l) => l.platform_id === platformId);
  const currentPlatformName = currentLink?.platform_name || (platformId > 0 ? `平台 #${platformId}` : "");
  // 当前页签注册上来的保存动作(切到资产/成果页签时自然为空,头部不显示保存按钮)。
  const bridge = bridges[tab];
  const showSave = !!bridge;

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="mb-1 flex items-center gap-2">
            <Button variant="ghost" size="icon-sm" asChild title="返回">
              <Link href={backHref} aria-label="返回">
                <ArrowLeftIcon className="size-4" />
              </Link>
            </Button>
            <span className="text-muted-foreground text-sm">SRC 目标</span>
          </div>
          <h1 className="truncate text-xl font-semibold tracking-tight" title={target.name}>
            {target.name}
          </h1>
          <p className="text-muted-foreground text-sm">
            {target.company_name || "未关联企业"}
            {links.length > 0 ? ` · 共 ${links.length} 个平台` : ""}
            {currentPlatformName && tab !== "overview" ? ` · 当前平台:${currentPlatformName}` : ""}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          {/* 建任务入口:平台与目标都已确定,这是「开干」最自然的位置。
              没有可用平台时禁用——任务必须绑定「平台×目标」才能注入授权边界。 */}
          <Button
            size="sm"
            disabled={!taskPlatform}
            title={taskPlatform ? `对「${taskPlatform.name} × ${target.name}」建 SRC 任务` : "请先给该目标挂上平台"}
            onClick={() => setCreateOpen(true)}
          >
            <RocketIcon className="size-4" />
            建 SRC 任务
          </Button>
          {showSave && (
            <Button size="sm" variant="outline" disabled={!bridge.dirty || bridge.saving} onClick={() => void bridge.save()}>
              <SaveIcon className="size-4" />
              {bridge.saving ? "保存中…" : "保存"}
            </Button>
          )}
          <Button variant="ghost" size="sm" onClick={() => void remove()}>
            <Trash2Icon className="size-4 text-red-600" />
            删除目标
          </Button>
        </div>
      </div>

      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="overview">概览</TabsTrigger>
          <TabsTrigger value="scope">授权范围 {links.length}</TabsTrigger>
          <TabsTrigger value="assets">资产 {target.asset_count ?? 0}</TabsTrigger>
          <TabsTrigger value="rewards">成果 {target.reward_count ?? 0}</TabsTrigger>
        </TabsList>

        {/* 概览与授权范围有本地表单:forceMount 保住未保存的输入,切走再切回不会丢。 */}
        <TabsContent value="overview" forceMount className="mt-4 data-[state=inactive]:hidden">
          <OverviewTab
            target={target}
            links={links}
            onSaved={onOverviewSaved}
            onSaveBridge={onOverviewBridge}
          />
        </TabsContent>

        <TabsContent value="scope" forceMount className="mt-4 data-[state=inactive]:hidden">
          <ScopeTab
            targetId={target.id}
            targetName={target.name}
            platformId={platformId}
            links={links}
            onPlatformChange={setPlatformId}
            onLinksChanged={reload}
            onSaveBridge={onScopeBridge}
          />
        </TabsContent>

        {/* 资产/成果是服务端分页列表,没有本地编辑态:不 forceMount,进页面时才发请求。 */}
        <TabsContent value="assets" className="mt-4">
          <AssetsTab
            targetId={target.id}
            platformId={platformId}
            links={links}
            onPlatformChange={setPlatformId}
            hasCompany={!!target.company_id}
            onCountsChanged={reload}
          />
        </TabsContent>

        <TabsContent value="rewards" className="mt-4">
          <RewardsTab targetId={target.id} />
        </TabsContent>
      </Tabs>

      {taskPlatform && (
        <CreateSRCTaskDialog
          open={createOpen}
          onOpenChange={setCreateOpen}
          platform={taskPlatform}
          target={target}
        />
      )}
    </div>
  );
}
