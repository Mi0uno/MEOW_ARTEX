"use client";

// SRC 模式 · 平台批量:按真实平台分组,勾选目标批量建 SRC 任务。
//
// 旧版靠 src_profiles.platform 字符串前端 group-by,平台不是实体,于是「补天」与「补天 」
// 会分成两组、也没法带上平台 id。新模型里平台是实体(src_platforms),分组改为
// 「平台 → 该平台下的目标(src_platform_targets)」,建任务时把 src_platform_id 与
// src_target_id 一起发过去,后端据此注入「平台规范 + 该目标在该平台下的授权范围」。

import * as React from "react";

import Link from "next/link";
import { useRouter } from "next/navigation";

import { ChevronDownIcon, ChevronRightIcon, LayersIcon, RocketIcon, SearchIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ProxyEgressSelect, proxyGroupIdForSubmit, type ProxyEgressValue } from "@/components/proxy-egress-select";
import { api, http } from "@/lib/api";
import type { SRCPlatform, SRCTarget, Task } from "@/lib/types";

// 一次拉全量平台与每个平台下的目标:批量建任务要先看得见全部目标才有意义,
// 分页会把「同平台的目标」切到两页,勾选与全选都会失真。
const FETCH_LIMIT = 200;

interface Group {
  platform: SRCPlatform;
  targets: SRCTarget[];
}

// 选择键用「平台:目标」复合键:同一目标可以挂在多个平台下,只用 target.id 会让
// 勾选补天下的 A 时把漏洞盒子下的 A 一起勾上,进而建出重复任务。
function keyOf(platformId: number, targetId: number): string {
  return `${platformId}:${targetId}`;
}

/**
 * createSRCTask 直接 POST /tasks 并带上 src_platform_id / src_target_id。
 *
 * 不走 api.createTask 是因为它的入参类型还没有这两个字段(本任务由主 agent 统一补 api.ts)。
 * 字段名与其余默认值照抄 api.createTask 的实现,保证与其它建任务入口行为一致——
 * 尤其 src_mode 必须为 true,否则后端不会注入平台规范与授权范围约束。
 *
 * 出网方式由调用方传入:批量建任务同样要给「走代理池」这个选项,否则只能拿到
 * 跟随全局,SRC 场景下(公网靶标、需隐藏来源)用户就没有选择权。
 */
function createSRCTask(platform: SRCPlatform, target: SRCTarget, egress: ProxyEgressValue): Promise<Task> {
  return http<Task>("/tasks", {
    method: "POST",
    body: JSON.stringify({
      name: `SRC · ${platform.name} · ${target.name}`,
      category_id: null,
      description: `SRC 模式批量任务 · ${platform.name} · ${target.name}`,
      goal: `在 ${platform.name} 的授权范围内对 ${target.name} 进行漏洞挖掘并产出可提交成果`,
      llm_profile_ids: [],
      source_task_ids: [],
      // 目标未关联企业时给空数组:后端按「最多 N 个有效企业」校验,传 [0] 会被判无效。
      company_ids: target.company_id ? [target.company_id] : [],
      timeout_seconds: 0,
      seed_first_intent: false,
      plan_heartbeat_seconds: 0,
      coverage_enabled: true,
      stage: "exploit",
      src_mode: true,
      src_platform_id: platform.id,
      src_target_id: target.id,
      proxy_mode: egress.mode,
      proxy_group_id: proxyGroupIdForSubmit(egress),
      guard_mode: "",
      guard_rule_ids: [],
    }),
  });
}

export default function SRCBatchPage() {
  const router = useRouter();
  const [groups, setGroups] = React.useState<Group[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [query, setQuery] = React.useState("");
  const [applied, setApplied] = React.useState("");
  const [selected, setSelected] = React.useState<Set<string>>(new Set());
  const [collapsed, setCollapsed] = React.useState<Set<number>>(new Set());
  const [creating, setCreating] = React.useState(false);
  // 出网方式:批量建的每条任务都用这一套设置(逐条设不现实,一次勾几十个目标)。
  const [egress, setEgress] = React.useState<ProxyEgressValue>({ mode: "", groupId: 0 });

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const { platforms } = await api.srcPlatforms({ limit: FETCH_LIMIT });
      // 并发拉取各平台下的目标:串行在 10+ 平台时肉眼可见地慢。
      const results = await Promise.all(
        platforms.map((p) =>
          api
            .srcPlatformTargets(p.id, { limit: FETCH_LIMIT })
            .then((r) => ({ platform: p, targets: r.targets }))
            // 单个平台拉失败不该让整页空白:退化成空分组,其他平台照常可勾。
            .catch(() => ({ platform: p, targets: [] as SRCTarget[] })),
        ),
      );
      setGroups(results);
    } catch (e) {
      toast.error(`加载平台失败:${(e as Error).message}`);
      setGroups([]);
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  // 搜索同时匹配平台名与目标名:用户可能记得平台、也可能记得目标,只过滤一种会让他以为没数据。
  const filtered = React.useMemo(() => {
    const kw = applied.trim().toLowerCase();
    if (!kw) return groups;
    return groups
      .map((g) => {
        const platformHit = g.platform.name.toLowerCase().includes(kw);
        return {
          platform: g.platform,
          targets: platformHit ? g.targets : g.targets.filter((t) => t.name.toLowerCase().includes(kw)),
        };
      })
      .filter((g) => g.targets.length > 0 || g.platform.name.toLowerCase().includes(kw));
  }, [groups, applied]);

  const totalTargets = React.useMemo(() => groups.reduce((n, g) => n + g.targets.length, 0), [groups]);

  const search = () => setApplied(query);

  const toggleOne = (platformId: number, targetId: number) =>
    setSelected((s) => {
      const n = new Set(s);
      const k = keyOf(platformId, targetId);
      if (n.has(k)) n.delete(k);
      else n.add(k);
      return n;
    });

  const toggleGroup = (g: Group) => {
    const keys = g.targets.map((t) => keyOf(g.platform.id, t.id));
    const allOn = keys.length > 0 && keys.every((k) => selected.has(k));
    setSelected((s) => {
      const n = new Set(s);
      for (const k of keys) {
        if (allOn) n.delete(k);
        else n.add(k);
      }
      return n;
    });
  };

  const toggleCollapse = (platformId: number) =>
    setCollapsed((c) => {
      const n = new Set(c);
      if (n.has(platformId)) n.delete(platformId);
      else n.add(platformId);
      return n;
    });

  // 批量为选中目标建 SRC 任务(每个「平台×目标」一条)。
  // 失败不中断循环:中途 break 会让用户搞不清到底建了几条,收集起来最后汇总更可控。
  const batchCreate = async () => {
    if (selected.size === 0) {
      toast.error("请先勾选目标");
      return;
    }
    setCreating(true);
    const pairs: { platform: SRCPlatform; target: SRCTarget }[] = [];
    for (const g of groups) {
      for (const t of g.targets) {
        if (selected.has(keyOf(g.platform.id, t.id))) pairs.push({ platform: g.platform, target: t });
      }
    }
    const created: string[] = [];
    const failed: string[] = [];
    for (const { platform, target } of pairs) {
      try {
        const t = await createSRCTask(platform, target, egress);
        created.push(t.id);
      } catch (e) {
        failed.push(`${platform.name} / ${target.name}(${(e as Error).message})`);
      }
    }
    setCreating(false);
    if (created.length > 0) {
      toast.success(`已创建 ${created.length} 条 SRC 任务`);
      setSelected(new Set());
      if (failed.length > 0) {
        toast.error(`${failed.length} 条失败:${failed.slice(0, 3).join(";")}${failed.length > 3 ? " …" : ""}`);
      }
      router.push("/function/tasks?stage=exploit");
      return;
    }
    if (failed.length > 0) {
      toast.error(`全部 ${failed.length} 条创建失败:${failed.slice(0, 3).join(";")}${failed.length > 3 ? " …" : ""}`);
    }
  };

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div>
          <h1 className="font-semibold text-xl">SRC 模式 · 平台批量</h1>
          <p className="text-muted-foreground text-sm">
            按平台分组列出各平台下的目标,勾选后一次性批量建 SRC 任务(每个「平台 × 目标」一条,
            自动带上该平台的规范与目标在该平台下的授权范围)
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {/* 单个目标的高频路径导流到任务页:两个入口语义互补而不是二选一 */}
          <Button variant="outline" size="sm" asChild>
            <Link href="/function/tasks?src=1">SRC 任务页</Link>
          </Button>
          <Button size="sm" disabled={creating || selected.size === 0} onClick={() => void batchCreate()}>
            <RocketIcon className="size-4" />
            {creating ? "创建中…" : `批量建 SRC 任务(${selected.size})`}
          </Button>
        </div>
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
                placeholder="搜索平台 / 目标"
                className="pl-8"
              />
            </div>
            <Button variant="outline" size="sm" onClick={search}>
              搜索
            </Button>
            {applied && (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  setQuery("");
                  setApplied("");
                }}
              >
                清除搜索
              </Button>
            )}
            <span className="text-muted-foreground ml-auto text-xs tabular-nums">
              {applied ? `命中 ${filtered.length} / ` : ""}
              {groups.length} 个平台 · {totalTargets} 个目标 · 已选 {selected.size}
            </span>
          </div>

          {/* 出网方式:批量建的每条任务共用这一套。放在列表之前——先定出口再挑目标,
              用户勾选时就知道这批任务会从哪出网。 */}
          <div className="mx-4 mb-3 max-w-md rounded-lg border p-3 lg:mx-6">
            <ProxyEgressSelect
              mode={egress.mode}
              groupId={egress.groupId}
              onChange={setEgress}
              disabled={creating}
            />
          </div>

          {loading ? (
            <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
          ) : groups.length === 0 ? (
            <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
              <LayersIcon className="size-8 opacity-40" />
              <span>还没有 SRC 平台。先到「平台」页新建平台并挂上目标,再回来批量建任务。</span>
              <Button variant="outline" size="sm" asChild>
                <Link href="/function/src/platforms">前往平台列表</Link>
              </Button>
            </div>
          ) : filtered.length === 0 ? (
            <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
              <SearchIcon className="size-8 opacity-40" />
              <span>没有匹配的平台或目标</span>
            </div>
          ) : (
            <Table className="table-fixed">
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  {/* 目标名列不设宽度:table-fixed 下自动吃掉余量(设 w-full 会挤掉后面的列)。 */}
                  <TableHead className="w-10" />
                  <TableHead>目标 / 平台</TableHead>
                  <TableHead className="w-40">关联企业</TableHead>
                  <TableHead className="w-20">资产数</TableHead>
                  <TableHead className="w-20">状态</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {filtered.map((g) => {
                  const keys = g.targets.map((t) => keyOf(g.platform.id, t.id));
                  const allOn = keys.length > 0 && keys.every((k) => selected.has(k));
                  const someOn = !allOn && keys.some((k) => selected.has(k));
                  const isCollapsed = collapsed.has(g.platform.id);
                  return (
                    <React.Fragment key={g.platform.id}>
                      {/* 平台分组行:可折叠,勾选它等于勾选该平台下全部目标。 */}
                      <TableRow className="bg-muted/40 hover:bg-muted/40">
                        <TableCell>
                          <Checkbox
                            checked={allOn ? true : someOn ? "indeterminate" : false}
                            disabled={keys.length === 0}
                            onCheckedChange={() => toggleGroup(g)}
                            aria-label={`选择平台 ${g.platform.name} 下全部目标`}
                          />
                        </TableCell>
                        <TableCell colSpan={2} className="min-w-0">
                          <button
                            type="button"
                            onClick={() => toggleCollapse(g.platform.id)}
                            className="flex w-full items-center gap-1.5 text-left font-medium hover:underline"
                            title={`${g.platform.name}(${g.targets.length} 个目标)`}
                          >
                            {isCollapsed ? (
                              <ChevronRightIcon className="size-3.5 shrink-0" />
                            ) : (
                              <ChevronDownIcon className="size-3.5 shrink-0" />
                            )}
                            <span className="truncate">{g.platform.name}</span>
                            <Badge variant="outline" className="shrink-0 text-xs">
                              {g.targets.length} 个目标
                            </Badge>
                            {!g.platform.enabled && (
                              <Badge variant="outline" className="bg-muted text-muted-foreground shrink-0 text-xs">
                                平台已停用
                              </Badge>
                            )}
                          </button>
                        </TableCell>
                        <TableCell colSpan={2} className="text-muted-foreground text-xs">
                          {g.targets.length === 0 ? "该平台下还没有目标" : "勾选平台行可一次选中该平台下全部目标"}
                        </TableCell>
                      </TableRow>
                      {!isCollapsed &&
                        g.targets.map((t) => (
                          <TableRow key={keyOf(g.platform.id, t.id)}>
                            <TableCell>
                              <Checkbox
                                checked={selected.has(keyOf(g.platform.id, t.id))}
                                onCheckedChange={() => toggleOne(g.platform.id, t.id)}
                                aria-label={`选择 ${t.name}`}
                              />
                            </TableCell>
                            <TableCell className="min-w-0">
                              <Link
                                href={`/function/src/targets/detail?id=${t.id}&platform=${g.platform.id}`}
                                className="block w-full truncate text-left font-medium hover:underline"
                                title={t.name}
                              >
                                {t.name}
                              </Link>
                            </TableCell>
                            <TableCell className="text-muted-foreground min-w-0">
                              <span className="block w-full truncate text-xs" title={t.company_name || "未关联企业"}>
                                {t.company_name || "未关联企业"}
                              </span>
                            </TableCell>
                            <TableCell className="text-xs tabular-nums">{t.asset_count ?? 0}</TableCell>
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
                          </TableRow>
                        ))}
                    </React.Fragment>
                  );
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
