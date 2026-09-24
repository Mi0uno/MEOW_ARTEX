"use client";

// LLM 配置详情页:/system/llm/detail?id=<profile id>
//
// 承载一条配置的【完整编辑能力】与【运行时状态】。列表页只做检索与轻量操作,
// 凡是要看「这条配置现在到底怎么样」的都在这里:
//   - 全部可编辑字段(与列表页的快速编辑抽屉共用 profile-form.tsx 的同一套实现)
//   - 拉取可用模型列表 / 测试连接
//   - 运行时:是否激活、轮询链上的顺位、熔断与冷却剩余秒数、健康度、最近错误
//   - 本配置的重试覆盖,并说明它与全局「重试与退避」的关系

import * as React from "react";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";

import {
  ArrowLeftIcon,
  HeartPulseIcon,
  Loader2Icon,
  PlugZapIcon,
  RotateCcwIcon,
  SaveIcon,
  StarIcon,
  Trash2Icon,
  ZapIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Separator } from "@/components/ui/separator";
import { api } from "@/lib/api";
import type { LLMPoolMember, LLMPoolStatus, LLMProfile } from "@/lib/types";
import { cn } from "@/lib/utils";

import {
  contextWindowText,
  cooldownText,
  FORMAT_LABEL_SHORT,
  formatBadgeClass,
  formatLabel,
  healthOf,
} from "../_components/llm-shared";
import { buildPayload, ProfileFields, useProfileForm, validateValues } from "../_components/profile-form";

// 运行时状态卡片：熔断 / 冷却 / 健康度 / 在链上的顺位。数据来自 api.llmPool()，
// 与列表页同源，所以两处不会出现「列表说熔断、详情说正常」。
//
// 顺位按后端返回的 chain 顺序算：激活配置恒为第 1，其余按优先级降序；被标记
// 「不参与轮询」的成员也在 chain 里（后端特意带上，好让用户看出它为什么不在队列中），
// 算顺位时要跳过，否则顺位数字会和实际尝试顺序对不上。
function RuntimeCard({
  member,
  chain,
  poolOn,
  onRecover,
  recovering,
}: {
  member?: LLMPoolMember;
  chain: LLMPoolMember[];
  poolOn: boolean;
  onRecover: () => void;
  recovering: boolean;
}) {
  const header = (action?: React.ReactNode) => (
    <CardHeader className="flex-row items-center gap-2">
      <HeartPulseIcon className="text-muted-foreground size-4" />
      <CardTitle className="text-sm">运行时状态</CardTitle>
      {action}
    </CardHeader>
  );

  if (!poolOn) {
    return (
      <Card>
        {header()}
        <CardContent className="text-muted-foreground text-xs leading-relaxed">
          轮询（故障转移）当前<b>未开启</b>，因此不产生熔断记录——调用失败就是失败，不会换配置。
          需要故障转移时在列表页点「轮询配置」开启。
        </CardContent>
      </Card>
    );
  }
  if (!member) {
    return (
      <Card>
        {header()}
        <CardContent className="text-muted-foreground text-xs leading-relaxed">
          后端未返回这条配置的轮询状态（通常是刚建好还没被任何调用触及）。它仍可被 Agent / 任务显式指定使用。
        </CardContent>
      </Card>
    );
  }

  // 实际参与轮询的成员序列；不参与的（pool_exclude 且非激活）不占顺位。
  const inChain = chain.filter((m) => m.active || !m.excluded);
  const order = inChain.findIndex((m) => m.profile_id === member.profile_id) + 1;
  const excluded = member.excluded && !member.active;

  return (
    <Card>
      {header(
        member.state !== "ok" ? (
          <Button
            variant="outline"
            size="sm"
            className="ml-auto"
            disabled={recovering}
            onClick={onRecover}
            title="清除熔断记录，下一次调用立刻重试这条配置"
          >
            {recovering ? <Loader2Icon className="animate-spin" /> : <RotateCcwIcon />}
            清除熔断
          </Button>
        ) : undefined,
      )}
      <CardContent className="grid gap-2 text-xs">
        <div className="flex items-center justify-between gap-4">
          <span className="text-muted-foreground">轮询顺位</span>
          <span className="tabular-nums">
            {excluded ? "不参与轮询" : order > 0 ? `第 ${order} 顺位 / 共 ${inChain.length}` : "不在链上"}
          </span>
        </div>
        {member.active && (
          <p className="text-muted-foreground leading-relaxed">
            激活配置恒为链首，与优先级无关——全 Agent 默认就用这一条。
          </p>
        )}
        {excluded && (
          <p className="text-muted-foreground leading-relaxed">
            已标记「不参与轮询」：不会被自动选中，但仍可被 Agent / 任务显式指定。
          </p>
        )}
        <div className="flex items-center justify-between gap-4">
          <span className="text-muted-foreground">优先级</span>
          <span className="tabular-nums">{member.priority}</span>
        </div>
        <div className="flex items-center justify-between gap-4">
          <span className="text-muted-foreground">连续失败</span>
          <span className="tabular-nums">{member.fails} 次</span>
        </div>
        <div className="flex items-center justify-between gap-4">
          <span className="text-muted-foreground">累计熔断</span>
          <span className="tabular-nums">{member.trips} 次</span>
        </div>
        {member.state === "tripped" && member.cooldown_secs > 0 && (
          <div className="flex items-center justify-between gap-4">
            <span className="text-muted-foreground">冷却剩余</span>
            <span className="text-destructive tabular-nums">
              {cooldownText(member.cooldown_secs)}（{member.cooldown_secs}s）
            </span>
          </div>
        )}
        {member.last_at && (
          <div className="flex items-center justify-between gap-4">
            <span className="text-muted-foreground">最近一次失败</span>
            <span>{new Date(member.last_at).toLocaleString()}</span>
          </div>
        )}
        {member.last_error && (
          <div className="grid gap-1 border-t pt-2">
            <span className="text-muted-foreground">最近错误</span>
            <code className="bg-muted/40 block rounded-md p-2 font-mono break-all">{member.last_error}</code>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function LLMDetailInner() {
  const searchParams = useSearchParams();
  const router = useRouter();
  const id = searchParams.get("id") ?? "";

  const [profile, setProfile] = React.useState<LLMProfile | null>(null);
  const [pool, setPool] = React.useState<LLMPoolStatus | null>(null);
  const [loading, setLoading] = React.useState(true);
  const [missing, setMissing] = React.useState(false);
  const [saving, setSaving] = React.useState(false);
  const [recovering, setRecovering] = React.useState(false);

  const form = useProfileForm();
  const { hydrate } = form;

  const loadPool = React.useCallback(async () => {
    try {
      setPool(await api.llmPool());
    } catch {
      /* 运行时状态拿不到不该挡住编辑表单 */
    }
  }, []);

  React.useEffect(() => {
    if (!id) {
      setMissing(true);
      setLoading(false);
      return;
    }
    let alive = true;
    api
      .llmProfiles()
      .then((list) => {
        if (!alive) return;
        const found = list.find((p) => p.id === id);
        if (!found) {
          setMissing(true);
          return;
        }
        setProfile(found);
        // 表单只灌一次：之后表单归用户所有，重新拉取列表不该把正在编辑的内容冲掉。
        hydrate(found);
      })
      .catch(() => {
        if (alive) setMissing(true);
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    void loadPool();
    return () => {
      alive = false;
    };
  }, [id, hydrate, loadPool]);

  const member = React.useMemo(() => (pool?.chain ?? []).find((m) => m.profile_id === id), [pool, id]);
  const poolOn = pool?.enabled ?? false;

  // 写操作（保存 / 激活 / 清除熔断）后重新对齐服务端：概要卡与表单都按落库值刷新。
  // 表单也一起灌，是因为后端会归一化字段（如非 openai 格式的 max_tokens_field 会被清空），
  // 只刷概要会让「概要显示的值」和「输入框里的值」当场打架。
  const refresh = React.useCallback(async () => {
    try {
      const list = await api.llmProfiles();
      const found = list.find((p) => p.id === id);
      if (found) {
        setProfile(found);
        hydrate(found);
      }
    } catch {
      /* 列表拉取失败时保留页面上的旧数据，比清空好 */
    }
    await loadPool();
  }, [id, hydrate, loadPool]);

  async function save() {
    const err = validateValues(form.v);
    if (err) {
      toast.error(err);
      return;
    }
    if (saving) return;
    setSaving(true);
    try {
      await api.saveLLMProfile(buildPayload(form.v, form.profileId));
      toast.success(profile?.is_default ? "已保存，激活配置即时生效，无需重启" : "已保存");
      await refresh();
    } catch (e) {
      toast.error(`保存失败：${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  async function activate() {
    if (!profile) return;
    try {
      await api.activateLLMProfile(profile.id);
      toast.success(`已激活：${profile.name}`);
      await refresh();
    } catch (e) {
      toast.error(`激活失败：${(e as Error).message}`);
    }
  }

  async function recover() {
    if (!profile || recovering) return;
    setRecovering(true);
    try {
      await api.resetLLMPool(profile.id);
      await loadPool();
      toast.success("已清除熔断，下次调用重试该配置");
    } catch (e) {
      toast.error(`清除熔断失败：${(e as Error).message}`);
    } finally {
      setRecovering(false);
    }
  }

  async function remove() {
    if (!profile) return;
    if (profile.is_default) {
      toast.error("无法删除当前激活的配置，请先激活别的配置");
      return;
    }
    if (!window.confirm(`删除模型配置「${profile.name}」？该操作不可恢复。`)) return;
    try {
      await api.deleteLLMProfile(profile.id);
      toast.success(`已删除：${profile.name}`);
      router.push("/system/llm");
    } catch (e) {
      toast.error(`删除失败：${(e as Error).message}`);
    }
  }

  // 参数缺失（直接访问 /system/llm/detail）与配置已被删除，是两种不同的空态，
  // 文案分开写——否则用户不知道是链接错了还是数据没了。
  if (!loading && (missing || !profile)) {
    return (
      <div className="p-4 md:p-6">
        <Card>
          <CardContent className="text-muted-foreground flex flex-col items-center gap-3 py-16 text-center text-sm">
            <ZapIcon className="size-8 opacity-40" />
            {id ? "该模型配置不存在或已被删除。" : "缺少参数 id：请从列表页点击配置名称进入。"}
            <Button variant="outline" size="sm" asChild>
              <Link href="/system/llm">返回列表</Link>
            </Button>
          </CardContent>
        </Card>
      </div>
    );
  }

  if (loading || !profile) {
    return <div className="text-muted-foreground p-4 text-center text-sm md:p-6">加载中…</div>;
  }

  const h = healthOf(profile, member);

  return (
    <div className="flex flex-1 flex-col gap-4 p-4 md:p-6">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="mb-1 flex items-center gap-2">
            <Button variant="ghost" size="icon-sm" asChild aria-label="返回列表">
              <Link href="/system/llm">
                <ArrowLeftIcon className="size-4" />
              </Link>
            </Button>
            <Badge variant="outline" className={formatBadgeClass(profile.format)}>
              {FORMAT_LABEL_SHORT[profile.format] ?? profile.format}
            </Badge>
            <Badge variant="outline" className={h.cls} title={h.hint}>
              {h.label}
            </Badge>
            {profile.is_default && (
              <Badge variant="outline" className="border-amber-400/50 text-amber-500">
                激活中
              </Badge>
            )}
          </div>
          <h1 className="truncate font-semibold text-xl tracking-tight">{profile.name}</h1>
          <p className="text-muted-foreground font-mono text-xs">
            #{profile.id} · {profile.model}
          </p>
        </div>
        <div className="flex shrink-0 flex-wrap items-center gap-1">
          <Button
            variant="outline"
            size="sm"
            disabled={form.testing}
            onClick={() => void form.testConnection()}
            title="用当前表单里的参数发一次真实请求"
          >
            {form.testing ? <Loader2Icon className="animate-spin" /> : <PlugZapIcon />}
            {form.testing ? "测试中…" : "测试连接"}
          </Button>
          <Button variant="outline" size="sm" onClick={() => void activate()} disabled={profile.is_default}>
            <StarIcon className={cn("size-4", profile.is_default && "fill-amber-400 text-amber-400")} />
            {profile.is_default ? "已激活" : "设为激活"}
          </Button>
          <Button size="sm" onClick={() => void save()} disabled={saving}>
            {saving ? <Loader2Icon className="animate-spin" /> : <SaveIcon className="size-4" />}
            保存
          </Button>
        </div>
      </div>

      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_20rem]">
        <Card>
          <CardHeader>
            <CardTitle className="text-sm">配置</CardTitle>
          </CardHeader>
          <CardContent className="grid gap-4">
            <ProfileFields
              form={form}
              idPrefix="dt"
              retryExtra={
                <p className="text-muted-foreground text-xs leading-relaxed">
                  重试覆盖只影响这一条配置的三层（建连 / 空响应 / 同 provider 安全窗口）。每格
                  <b> 留空 = 跟随全局</b>；次数填 -1 = 关掉这层；间隔填了就用固定间隔取代指数退避。 另外两层——
                  <b>轮询熔断</b>与<b>意图重跑</b>——是进程级的，只能在列表页「重试与退避」页签里调。
                </p>
              }
            />
          </CardContent>
        </Card>

        <div className="grid content-start gap-4">
          <RuntimeCard
            member={member}
            chain={pool?.chain ?? []}
            poolOn={poolOn}
            onRecover={() => void recover()}
            recovering={recovering}
          />

          <Card>
            <CardHeader>
              <CardTitle className="text-sm">概要</CardTitle>
            </CardHeader>
            <CardContent className="grid gap-2 text-xs">
              <div className="flex items-center justify-between gap-4">
                <span className="text-muted-foreground">协议格式</span>
                <span>{formatLabel(profile.format)}</span>
              </div>
              <div className="flex items-center justify-between gap-4">
                <span className="text-muted-foreground">模型</span>
                <code className="truncate font-mono" title={profile.model}>
                  {profile.model}
                </code>
              </div>
              <div className="flex items-center justify-between gap-4">
                <span className="text-muted-foreground">Base URL</span>
                <code className="max-w-[60%] truncate font-mono" title={profile.base_url || "官方默认地址"}>
                  {profile.base_url || "官方默认"}
                </code>
              </div>
              <div className="flex items-center justify-between gap-4">
                <span className="text-muted-foreground">API Key</span>
                <span className="font-mono">{profile.api_key_hint || "未配置"}</span>
              </div>
              <div className="flex items-center justify-between gap-4">
                <span className="text-muted-foreground">上下文窗口</span>
                <span className="tabular-nums">
                  {contextWindowText(profile.context_window_k)}
                  {!profile.context_window_k && "（默认）"}
                </span>
              </div>
              <div className="flex items-center justify-between gap-4">
                <span className="text-muted-foreground">限速</span>
                <span className="tabular-nums">
                  {profile.rate_per_second}/s · {profile.rate_per_minute}/min
                </span>
              </div>
              <div className="flex items-center justify-between gap-4">
                <span className="text-muted-foreground">流式输出</span>
                <span>{profile.streaming === false ? "非流式" : "流式"}</span>
              </div>
              <div className="flex items-center justify-between gap-4">
                <span className="text-muted-foreground">参与轮询</span>
                <span>{profile.pool_exclude ? "不参与" : "参与"}</span>
              </div>
              <div className="flex items-center justify-between gap-4">
                <span className="text-muted-foreground">优先级</span>
                <span className="tabular-nums">{profile.priority ?? 0}</span>
              </div>
            </CardContent>
          </Card>

          {/* Card 的边框是 ring 而不是 border，危险区用 ring-destructive 才看得出是危险区 */}
          <Card className="ring-destructive/30">
            <CardHeader>
              <CardTitle className="text-sm text-destructive">危险操作</CardTitle>
            </CardHeader>
            <CardContent className="grid gap-2">
              <p className="text-muted-foreground text-xs">
                {profile.is_default
                  ? "当前激活的配置不能删除——先把别的配置设为激活，再回来删这一条。"
                  : "删除后不可恢复。正在使用这条配置的 Agent / 任务会回落到激活配置。"}
              </p>
              <Separator />
              <Button
                variant="destructive"
                size="sm"
                disabled={profile.is_default}
                onClick={() => void remove()}
                className="justify-self-start"
              >
                <Trash2Icon className="size-4" />
                删除该配置
              </Button>
            </CardContent>
          </Card>
        </div>
      </div>
    </div>
  );
}

// useSearchParams 必须在 Suspense 边界内（静态导出硬约束）。
export default function LLMDetailPage() {
  return (
    <React.Suspense fallback={null}>
      <LLMDetailInner />
    </React.Suspense>
  );
}
