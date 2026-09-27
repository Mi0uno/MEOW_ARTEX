"use client";

// 代理中心 · 调度规则:组级调度策略、自动升降级参数与优先级代理的集中控制页。
//
// 与「代理池」页的分工:那一页管组的增删改查、接入源与测活,这一页只管「调度」本身——
// 策略怎么取用代理、测活结果怎么反向影响优先级(自动升降级)、以及当前优先级最高
// 的代理样本与手动调级。数据来自 GET /proxy/schedule 一次性总览,保存分别走
// 组更新(策略)与调度规则(升降级参数)两个接口。

import * as React from "react";

import Link from "next/link";

import { RefreshCwIcon, ShieldAlertIcon } from "lucide-react";
import { toast } from "sonner";

import { fmtLatency, proxyStatusBadgeClass } from "@/components/proxy-dialogs";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { ProxyEntry, ProxyScheduleGroup, ProxySchedRules } from "@/lib/types";

// 自动升降级参数的后端默认值:sched_rules 可能是空对象反序列化出的零值,前端按这组兜底。
const DEFAULT_RULES: ProxySchedRules = {
  auto_boost: false,
  boost_every: 5,
  boost_step: 1,
  boost_cap: 50,
  demote_on_fail: 2,
};

// sanitizeRules 把组上带回的 sched_rules 归一成可编辑表单值。
// 存量行可能是 '{}'(全零=未配置)→ 整体回落默认;只要对象里任一字段有值,
// 就视为后端已归一过的完整对象,只补缺字段——demote_on_fail=0(不降级)是
// 合法配置,必须原样保留,不能按 falsy 兜底成 2。
function sanitizeRules(r?: Partial<ProxySchedRules> | null): ProxySchedRules {
  const base = r ?? {};
  const configured =
    !!base.auto_boost ||
    !!base.boost_every ||
    !!base.boost_step ||
    !!base.boost_cap ||
    (!!base.demote_on_fail && base.demote_on_fail > 0);
  if (!configured) return { ...DEFAULT_RULES };
  return {
    auto_boost: !!base.auto_boost,
    boost_every: base.boost_every ?? DEFAULT_RULES.boost_every,
    boost_step: base.boost_step ?? DEFAULT_RULES.boost_step,
    boost_cap: base.boost_cap ?? DEFAULT_RULES.boost_cap,
    demote_on_fail: base.demote_on_fail ?? DEFAULT_RULES.demote_on_fail,
  };
}

// 调度策略选项:label/desc 与 proxy-dialogs 组对话框里的策略下拉保持同一套口径。
const STRATEGY_OPTIONS: { value: string; label: string; desc: string }[] = [
  { value: "round_robin", label: "轮询", desc: "按顺序循环取用每条可用代理" },
  { value: "random", label: "随机", desc: "每次从可用代理里随机挑一条" },
  { value: "least_latency", label: "最低延迟", desc: "优先用最近测活延迟最低的代理" },
  { value: "failover", label: "故障转移", desc: "固定用当前可用代理,失败才切换到下一条" },
  {
    value: "quality",
    label: "质量加权",
    desc: "按权重随机：优先级×2 + 连续存活加成(封顶20) + 低延迟加成",
  },
];

const STATUS_LABEL: Record<string, string> = {
  untested: "未测",
  alive: "存活",
  dead: "失效",
  checking: "测活中",
};

// editableGroupPayload 组装 updateProxyGroup 的请求体。后端是全字段覆盖:
// 必须带上全部可编辑字段,只发 {strategy} 会把其余字段清成零值;
// 派生计数(proxy_count 等)、top、sched_rules 不在此列——前者后端自算,后两者各有专属来源/接口。
function editableGroupPayload(g: ProxyScheduleGroup, overrides?: Partial<ProxyScheduleGroup>) {
  return {
    name: g.name,
    kind: g.kind,
    strategy: g.strategy,
    listen_port: g.listen_port,
    enabled: g.enabled,
    auto_check: g.auto_check,
    check_interval_minutes: g.check_interval_minutes,
    test_url: g.test_url,
    timeout_ms: g.timeout_ms,
    max_failures: g.max_failures,
    concurrency: g.concurrency,
    remark: g.remark,
    ...overrides,
  };
}

export default function ProxySchedulePage() {
  const [groups, setGroups] = React.useState<ProxyScheduleGroup[]>([]);
  const [selectedId, setSelectedId] = React.useState<number | null>(null);
  const [loading, setLoading] = React.useState(true);
  // 两个表单:策略与升降级参数。切组时重置为该组当前配置。
  const [strategy, setStrategy] = React.useState("round_robin");
  const [rules, setRules] = React.useState<ProxySchedRules>(DEFAULT_RULES);
  const [savingStrategy, setSavingStrategy] = React.useState(false);
  const [savingRules, setSavingRules] = React.useState(false);
  // 正在调优先级的代理行 id(行内乐观更新期间禁用该行按钮)。
  const [prioritizing, setPrioritizing] = React.useState<number | null>(null);

  // 选中组:未显式选择(或刷新后 id 失效)时回落到第一个组。
  const selected = React.useMemo(
    () => groups.find((g) => g.id === selectedId) ?? groups[0] ?? null,
    [groups, selectedId],
  );

  // silent=true 用于保存/调级后的静默刷新:不进 loading 态,避免整页闪烁。
  const load = React.useCallback((silent = false) => {
    if (!silent) setLoading(true);
    api
      .proxySchedule()
      .then((r) => setGroups(r.groups ?? []))
      .catch((e) => toast.error(`加载调度总览失败:${(e as Error).message}`))
      .finally(() => setLoading(false));
  }, []);

  React.useEffect(() => {
    load();
  }, [load]);

  // 切组(或首次拿到组)时把表单重置为该组当前配置;同组刷新不打断正在编辑的内容。
  React.useEffect(() => {
    if (!selected) return;
    setStrategy(selected.strategy);
    setRules(sanitizeRules(selected.sched_rules));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selected?.id]);

  const setRuleNum = (k: "boost_every" | "boost_step" | "boost_cap" | "demote_on_fail", v: string) => {
    const n = Number(v);
    setRules((r) => ({ ...r, [k]: v === "" || !Number.isFinite(n) ? 0 : n }));
  };

  const saveStrategy = async () => {
    if (!selected) return;
    setSavingStrategy(true);
    try {
      await api.updateProxyGroup(selected.id, editableGroupPayload(selected, { strategy }));
      toast.success("调度策略已保存");
      load(true);
    } catch (e) {
      toast.error(`保存策略失败:${(e as Error).message}`);
    } finally {
      setSavingStrategy(false);
    }
  };

  const saveRules = async () => {
    if (!selected) return;
    if (rules.boost_every < 1) {
      toast.error("连续存活次数 N 至少为 1");
      return;
    }
    if (rules.boost_step < 1) {
      toast.error("提升步长至少为 1");
      return;
    }
    if (rules.boost_cap < 1) {
      toast.error("提升上限至少为 1");
      return;
    }
    if (rules.demote_on_fail < 0) {
      toast.error("失败降级量不能为负");
      return;
    }
    setSavingRules(true);
    try {
      await api.updateProxySchedRules(selected.id, rules);
      toast.success("自动升降级规则已保存");
      load(true);
    } catch (e) {
      toast.error(`保存规则失败:${(e as Error).message}`);
    } finally {
      setSavingRules(false);
    }
  };

  // 手动调级:行内乐观更新,失败回滚;成功后静默刷新同步 boosted_count 与服务端排序。
  const adjustPriority = async (e: ProxyEntry, next: number) => {
    setPrioritizing(e.id);
    const prev = e.priority;
    const patchTop = (p: number) =>
      setGroups((gs) =>
        gs.map((g) =>
          g.id === e.group_id
            ? { ...g, top: (g.top ?? []).map((t) => (t.id === e.id ? { ...t, priority: p } : t)) }
            : g,
        ),
      );
    patchTop(next);
    try {
      await api.updateProxyPriority(e.id, next);
      toast.success(`优先级已调整为 ${next}`);
      load(true);
    } catch (err) {
      toast.error(`调整优先级失败:${(err as Error).message}`);
      patchTop(prev);
    } finally {
      setPrioritizing(null);
    }
  };

  const strategyDesc = STRATEGY_OPTIONS.find((o) => o.value === strategy)?.desc;

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div>
          <h1 className="font-semibold text-xl">代理中心 · 调度规则</h1>
          <p className="text-muted-foreground text-sm">
            出网调度时,组内候选代理一律按优先级从高到低装载,同优先级按延迟从低到高。优先级可在此手动调整
            (置顶 / +10 / −10 / 归零),也可开启自动升降级交由测活结果维护。
          </p>
        </div>
        <Button variant="outline" size="sm" onClick={() => load()}>
          <RefreshCwIcon className={loading ? "size-4 animate-spin" : "size-4"} />
          刷新
        </Button>
      </div>

      {/* 组选择器:组名 + 存活/代理数 + 已提升徽标,选中组后下方展示三张卡。 */}
      <Card>
        <CardContent className="flex flex-wrap items-center gap-2">
          {loading && groups.length === 0 ? (
            <p className="text-muted-foreground w-full py-4 text-center text-sm">加载中…</p>
          ) : groups.length === 0 ? (
            <div className="text-muted-foreground flex w-full flex-col items-center gap-2 py-8 text-center text-sm">
              <ShieldAlertIcon className="size-8 opacity-40" />
              <span>
                还没有代理组。先到
                <Link href="/function/proxy/groups" className="text-blue-600 mx-1 hover:underline">
                  代理池
                </Link>
                创建组并接入代理,再回到这里配置调度。
              </span>
            </div>
          ) : (
            groups.map((g) => (
              <button
                key={g.id}
                type="button"
                onClick={() => setSelectedId(g.id)}
                className={`inline-flex items-center gap-2 rounded-full border px-3 py-1.5 text-sm transition-colors ${
                  selected?.id === g.id
                    ? "border-primary bg-primary/10 text-primary"
                    : "hover:bg-muted/60 border-input text-foreground"
                }`}
              >
                <span className="font-medium">{g.name}</span>
                <span className="text-muted-foreground text-xs tabular-nums">
                  存活 {g.alive_count ?? 0} / {g.proxy_count ?? 0}
                </span>
                {(g.boosted_count ?? 0) > 0 && (
                  <Badge
                    variant="outline"
                    className="bg-amber-500/15 text-amber-600 border-amber-500/30"
                  >
                    提升 {g.boosted_count}
                  </Badge>
                )}
              </button>
            ))
          )}
        </CardContent>
      </Card>

      {selected && (
        <>
          <div className="mt-4 grid gap-4 lg:grid-cols-2">
            {/* 卡一:调度策略。保存走 updateProxyGroup(全字段覆盖,载荷由 editableGroupPayload 组装)。 */}
            <Card>
              <CardContent className="grid gap-3">
                <div>
                  <h2 className="font-medium">调度策略</h2>
                  <p className="text-muted-foreground text-xs">
                    决定组内代理被取用的顺序;无论哪种策略,候选都按优先级从高到低装载。
                  </p>
                </div>
                <Select value={strategy} onValueChange={setStrategy}>
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      {STRATEGY_OPTIONS.map((o) => (
                        <SelectItem key={o.value} value={o.value}>
                          <div className="flex flex-col">
                            <span>{o.label}</span>
                            <span className="text-muted-foreground text-xs">{o.desc}</span>
                          </div>
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
                {strategyDesc && <p className="text-muted-foreground text-xs">当前策略:{strategyDesc}</p>}
                <div className="flex items-center justify-between">
                  <span className="text-muted-foreground text-xs">
                    修改只影响本组({selected.name});其余组参数请到代理组详情页调整
                  </span>
                  <Button
                    size="sm"
                    disabled={savingStrategy || strategy === selected.strategy}
                    onClick={() => void saveStrategy()}
                  >
                    {savingStrategy ? "保存中…" : "保存策略"}
                  </Button>
                </div>
              </CardContent>
            </Card>

            {/* 卡二:自动升降级规则。保存走 updateProxySchedRules 专属接口。 */}
            <Card>
              <CardContent className="grid gap-3">
                <div>
                  <h2 className="font-medium">自动升降级规则</h2>
                  <div className="mt-2 flex items-center gap-2 text-sm">
                    <Switch
                      checked={rules.auto_boost}
                      onCheckedChange={(v) => setRules((r) => ({ ...r, auto_boost: v }))}
                      aria-label="自动升降级"
                    />
                    <span className={rules.auto_boost ? "" : "text-muted-foreground"}>
                      {rules.auto_boost ? "已开启" : "已关闭"}
                    </span>
                  </div>
                  <p className="text-muted-foreground mt-1 text-xs">
                    开启后,连续存活每 N 次测活自动提升优先级 +step(封顶 cap);每次失败降级 demote
                  </p>
                </div>
                <div className="grid grid-cols-2 gap-3">
                  <div className="grid gap-1.5">
                    <Label>连续存活次数 N</Label>
                    <Input
                      type="number"
                      min={1}
                      max={1000}
                      disabled={!rules.auto_boost}
                      className="tabular-nums"
                      value={String(rules.boost_every)}
                      onChange={(e) => setRuleNum("boost_every", e.target.value)}
                    />
                    <p className="text-muted-foreground text-xs">每连续存活 N 次提升一级</p>
                  </div>
                  <div className="grid gap-1.5">
                    <Label>提升步长 step</Label>
                    <Input
                      type="number"
                      min={1}
                      max={100}
                      disabled={!rules.auto_boost}
                      className="tabular-nums"
                      value={String(rules.boost_step)}
                      onChange={(e) => setRuleNum("boost_step", e.target.value)}
                    />
                    <p className="text-muted-foreground text-xs">每次提升的优先级增量</p>
                  </div>
                  <div className="grid gap-1.5">
                    <Label>提升上限 cap</Label>
                    <Input
                      type="number"
                      min={1}
                      max={1000}
                      disabled={!rules.auto_boost}
                      className="tabular-nums"
                      value={String(rules.boost_cap)}
                      onChange={(e) => setRuleNum("boost_cap", e.target.value)}
                    />
                    <p className="text-muted-foreground text-xs">自动提升的优先级封顶;手动置顶不受此限</p>
                  </div>
                  <div className="grid gap-1.5">
                    <Label>失败降级 demote</Label>
                    <Input
                      type="number"
                      min={0}
                      max={1000}
                      disabled={!rules.auto_boost}
                      className="tabular-nums"
                      value={String(rules.demote_on_fail)}
                      onChange={(e) => setRuleNum("demote_on_fail", e.target.value)}
                    />
                    <p className="text-muted-foreground text-xs">每次测活/调度失败的降级量;0 = 不降级</p>
                  </div>
                </div>
                <div className="flex justify-end">
                  <Button size="sm" disabled={savingRules} onClick={() => void saveRules()}>
                    {savingRules ? "保存中…" : "保存规则"}
                  </Button>
                </div>
              </CardContent>
            </Card>
          </div>

          {/* 卡三:优先级代理表。top 由后端按优先级排好(最多 10 条)。 */}
          <Card className="mt-4">
            <CardContent className="px-0 pt-4">
              <div className="flex flex-wrap items-center justify-between gap-2 px-4 pb-3 lg:px-6">
                <div>
                  <h2 className="font-medium">优先级代理</h2>
                  <p className="text-muted-foreground text-xs">
                    「{selected.name}」中优先级最高的前 {selected.top?.length ?? 0} 条(alive 优先):
                    置顶=100,提升/降低按步进 10,归零后回到默认队列。
                  </p>
                </div>
                {(selected.boosted_count ?? 0) > 0 && (
                  <Badge variant="outline" className="bg-amber-500/15 text-amber-600 border-amber-500/30">
                    已提升 / 置顶 {selected.boosted_count} 条
                  </Badge>
                )}
              </div>

              {!selected.top || selected.top.length === 0 ? (
                <div className="text-muted-foreground flex flex-col items-center gap-2 py-12 text-center text-sm">
                  <ShieldAlertIcon className="size-8 opacity-40" />
                  <span>暂无代理，先到代理池页接入数据源并测活</span>
                  <Link href="/function/proxy/groups" className="text-blue-600 hover:underline">
                    前往代理池
                  </Link>
                </div>
              ) : (
                <Table className="table-fixed">
                  <TableHeader>
                    <TableRow className="hover:bg-transparent">
                      <TableHead className="w-28">优先级</TableHead>
                      {/* 地址列不设宽度:table-fixed 下自动吃掉余量;固定列合计 736px,~897px 容器下仍有余量。 */}
                      <TableHead>地址</TableHead>
                      <TableHead className="w-20">状态</TableHead>
                      <TableHead className="w-20">延迟</TableHead>
                      <TableHead className="w-20">连续存活</TableHead>
                      <TableHead className="w-20">被调度</TableHead>
                      <TableHead className="w-20">国家</TableHead>
                      <TableHead className="w-56">操作</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {selected.top.map((e) => (
                      <TableRow key={e.id}>
                        <TableCell>
                          {e.priority >= 100 ? (
                            <Badge
                              variant="outline"
                              className="bg-amber-500/15 text-amber-600 border-amber-500/30"
                            >
                              置顶 {e.priority}
                            </Badge>
                          ) : e.priority > 0 ? (
                            <Badge
                              variant="outline"
                              className="bg-blue-500/15 text-blue-600 border-blue-500/30"
                            >
                              已提升 {e.priority}
                            </Badge>
                          ) : (
                            <span className="text-muted-foreground tabular-nums">0</span>
                          )}
                        </TableCell>
                        <TableCell className="min-w-0">
                          <span className="block truncate font-mono text-xs" title={`http://${e.host}:${e.port}`}>
                            {e.host}:{e.port}
                          </span>
                        </TableCell>
                        <TableCell>
                          <Badge variant="outline" className={proxyStatusBadgeClass(e.status)}>
                            {STATUS_LABEL[e.status] ?? e.status}
                          </Badge>
                        </TableCell>
                        <TableCell className="tabular-nums">{fmtLatency(e.latency_ms)}</TableCell>
                        <TableCell className="tabular-nums">{e.streak}</TableCell>
                        <TableCell className="tabular-nums">{e.use_count}</TableCell>
                        <TableCell className="text-muted-foreground text-xs">
                          {e.country_code || e.country || "—"}
                        </TableCell>
                        <TableCell>
                          <div className="flex items-center gap-0.5">
                            <Button
                              variant="ghost"
                              size="sm"
                              className="h-7 px-2 text-xs"
                              disabled={prioritizing === e.id || e.priority >= 100}
                              title="置顶(优先级=100)"
                              onClick={() => void adjustPriority(e, 100)}
                            >
                              置顶
                            </Button>
                            <Button
                              variant="ghost"
                              size="sm"
                              className="h-7 px-2 text-xs"
                              disabled={prioritizing === e.id}
                              title="优先级 +10"
                              onClick={() => void adjustPriority(e, e.priority + 10)}
                            >
                              +10
                            </Button>
                            <Button
                              variant="ghost"
                              size="sm"
                              className="h-7 px-2 text-xs"
                              disabled={prioritizing === e.id || e.priority <= 0}
                              title="优先级 −10(最低到 0)"
                              onClick={() => void adjustPriority(e, Math.max(0, e.priority - 10))}
                            >
                              −10
                            </Button>
                            <Button
                              variant="ghost"
                              size="sm"
                              className="h-7 px-2 text-xs"
                              disabled={prioritizing === e.id || e.priority === 0}
                              title="归零(取消提升/置顶)"
                              onClick={() => void adjustPriority(e, 0)}
                            >
                              归零
                            </Button>
                          </div>
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              )}
            </CardContent>
          </Card>
        </>
      )}
    </div>
  );
}
