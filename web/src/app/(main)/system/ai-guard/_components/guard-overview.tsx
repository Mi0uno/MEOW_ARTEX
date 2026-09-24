"use client";

// AI监管·门限总览
//
// 一屏回答"监管现在到底在不在工作",以及用户要的五档动作各自拦了多少。
//
// 数据口径刻意分两个维度呈现,不合并成一个数字(合并会给出看似精确、实则编造的结论):
//   - 规则侧(overview.action_counts):intercept_rules 按 action 分组的**规则条数**,
//     回答"我配了多少条规则、各动作几条",与历史命中无关。
//   - 审计侧(overview.status_counts):intercept_pending 按 status 分组的**历史落库条数**,
//     回答"过去各档实际命中多少"。
// 五档里的"正则拦截 / AI拦截"要把审计侧的 denied 再按判定来源(规则/模型)拆开,而
// guard-overview 只给 status 一维聚合,所以这里用已有的审批记录分页接口(page=1&size=1,
// 只取 total)补这一次拆分;拆分请求失败时该档显示"—"并说明,绝不用合并值冒充单档,
// 否则用户会拿一个错误的数字去判断监管是否在按预期工作。
//
// "AI放通"一档例外:它的语义是"这次调用被放通了",而放通的来源除了模型判定,还有规则
// 显式放通与人工批准,所以主数字取审计侧的放通总数,规则/模型/人工的拆分放在副标题里。
// 只有"模型判定放通"这一个子项才严格等于用户说的"AI放通"。

import * as React from "react";

import Link from "next/link";

import {
  BanIcon,
  BotIcon,
  CheckCircle2Icon,
  GaugeIcon,
  RefreshCwIcon,
  ScanSearchIcon,
  ShieldAlertIcon,
  ShieldCheckIcon,
  UserCheckIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { InterceptApprovalFilter } from "@/lib/types";

// 直接从 api.ts 的返回类型推导,避免在页面里再抄一份字段形状(抄本会漂移)。
type Overview = Awaited<ReturnType<typeof api.interceptGuardOverview>>;

// 判定来源拆分。null = 取不到(请求失败),页面显示"—"而不是 0——0 会被误读成"这一档没有命中"。
type SourceSplit = {
  deniedRule: number | null;
  deniedModel: number | null;
  allowedRule: number | null;
  allowedModel: number | null;
};

const EMPTY_SPLIT: SourceSplit = {
  deniedRule: null,
  deniedModel: null,
  allowedRule: null,
  allowedModel: null,
};

function countOf(map: Record<string, number> | undefined, key: string): number {
  return map?.[key] ?? 0;
}

// 演示模式(NEXT_PUBLIC_MOCK=1)的 mock 没有 guard-overview 路由,读兜底会返回 {}。
// 用空对象当总览会得出"监管未生效、0 条规则"这种**编造的结论**,所以先校验必需字段:
// 字段不全就当作"拿不到数据"报错重试,而不是把缺失当成 0。
function normalizeOverview(raw: Overview): Overview | null {
  if (typeof raw?.judge_enabled !== "boolean" || typeof raw?.jev_enabled !== "boolean") return null;
  if (typeof raw?.rules_total !== "number" || typeof raw?.rules_enabled !== "number") return null;
  if (!Array.isArray(raw?.enabled_tools)) return null;
  return {
    ...raw,
    enabled_tools: raw.enabled_tools.filter((t): t is string => typeof t === "string"),
    action_counts: raw.action_counts ?? {},
    status_counts: raw.status_counts ?? {},
  };
}

// 五档动作的配色:拦截=红、判疑=琥珀、放通=绿、转人工=蓝。
const TONE = {
  ruleDeny: { box: "border-red-400/50 bg-red-50/50 dark:bg-red-950/20", num: "text-red-600 dark:text-red-400" },
  modelDeny: { box: "border-rose-400/50 bg-rose-50/50 dark:bg-rose-950/20", num: "text-rose-600 dark:text-rose-400" },
  flagged: {
    box: "border-amber-400/50 bg-amber-50/50 dark:bg-amber-950/20",
    num: "text-amber-600 dark:text-amber-400",
  },
  allowed: {
    box: "border-emerald-400/50 bg-emerald-50/50 dark:bg-emerald-950/20",
    num: "text-emerald-600 dark:text-emerald-400",
  },
  ask: { box: "border-blue-400/50 bg-blue-50/50 dark:bg-blue-950/20", num: "text-blue-600 dark:text-blue-400" },
} as const;

function TierTile({
  icon,
  label,
  count,
  hint,
  tone,
}: {
  icon: React.ReactNode;
  label: string;
  count: number | null;
  hint: string;
  tone: { box: string; num: string };
}) {
  return (
    <div className={`rounded-lg border p-3 ${tone.box}`}>
      <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
        {icon}
        <span className="truncate">{label}</span>
      </div>
      <div className={`mt-1 font-semibold text-2xl tabular-nums ${tone.num}`}>{count ?? "—"}</div>
      <p className="mt-1 text-[10px] text-muted-foreground leading-snug">{hint}</p>
    </div>
  );
}

// 一层门限的生效状态行:标题 + 结论徽标 + 细节。href 非空时整行可跳转配置处。
function LayerRow({
  title,
  state,
  detail,
  href,
}: {
  title: string;
  state: { label: string; tone: string };
  detail: string;
  href?: string;
}) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 rounded-md border px-3 py-2">
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <span className="font-medium text-sm">{title}</span>
        <Badge variant="outline" className={`px-1.5 py-0 text-[10px] ${state.tone}`}>
          {state.label}
        </Badge>
        <span className="text-muted-foreground text-xs">{detail}</span>
      </div>
      {href ? (
        <Button variant="ghost" size="sm" className="h-7 shrink-0 text-xs" asChild>
          <Link href={href}>去配置</Link>
        </Button>
      ) : null}
    </div>
  );
}

export function GuardOverview() {
  const [ov, setOv] = React.useState<Overview | null>(null);
  const [split, setSplit] = React.useState<SourceSplit>(EMPTY_SPLIT);
  const [loading, setLoading] = React.useState(true);
  // 三种失败要分开表达,因为用户能做的事完全不同:
  //   ""          = 正常
  //   "unsupported" = 响应字段不全(演示模式 mock / 旧版后端) — 重试无用,要升级后端
  //   "error"       = 请求失败(网络/鉴权/500) — 重试有意义
  const [failure, setFailure] = React.useState<"" | "unsupported" | "error">("");
  const [reloadKey, setReloadKey] = React.useState(0);

  // 按判定来源统计审计行数。page/size 传 1 只为了拿 total——服务端 page 分支才会
  // 返回总数(不带分页参数的旧分支只回当前批次的长度)。
  const countRows = React.useCallback(
    async (status: "denied" | "allowed", source: "rule" | "model"): Promise<number | null> => {
      const filter: InterceptApprovalFilter = { status, decision_source: source };
      try {
        const r = await api.interceptHistoryPage(1, 1, filter);
        // total 必须是数字才算数:演示模式/旧后端返回 {} 时 r.total 是 undefined,
        // 直接透传会让"0"和"拿不到"分不清。
        return typeof r.total === "number" ? r.total : null;
      } catch {
        return null;
      }
    },
    [],
  );

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadKey 不在闭包里被读取,它的作用就是"改一下就重跑本 effect"(手动刷新)。
  React.useEffect(() => {
    let alive = true;
    void (async () => {
      setLoading(true);
      try {
        // 总览是主数据:它失败就整卡报错。来源拆分是补充维度:失败降级为"—"。
        const [overview, deniedRule, deniedModel, allowedRule, allowedModel] = await Promise.all([
          api.interceptGuardOverview(),
          countRows("denied", "rule"),
          countRows("denied", "model"),
          countRows("allowed", "rule"),
          countRows("allowed", "model"),
        ]);
        if (!alive) return;
        const normalized = normalizeOverview(overview);
        if (!normalized) {
          // 字段不全:不能把缺失字段当 0 显示,那会编造"监管未生效、0 条规则"的结论。
          setOv(null);
          setFailure("unsupported");
          return;
        }
        setOv(normalized);
        setSplit({ deniedRule, deniedModel, allowedRule, allowedModel });
        setFailure("");
      } catch (e) {
        if (!alive) return;
        setOv(null);
        setFailure("error");
        toast.error(`加载门限总览失败: ${(e as Error).message}`);
      } finally {
        if (alive) setLoading(false);
      }
    })();
    return () => {
      alive = false;
    };
  }, [countRows, reloadKey]);

  if (loading && !ov) {
    return (
      <Card>
        <CardContent className="p-4">
          <p className="py-6 text-center text-muted-foreground text-sm">加载门限总览…</p>
        </CardContent>
      </Card>
    );
  }

  if (!ov) {
    const unsupported = failure === "unsupported";
    return (
      <Card>
        <CardContent className="flex flex-wrap items-center justify-between gap-3 p-4">
          <div className="flex min-w-0 items-start gap-2 text-sm">
            <ShieldAlertIcon className="mt-0.5 h-4 w-4 shrink-0 text-destructive" />
            <div>
              <p className="font-medium">{unsupported ? "无法读取门限总览" : "门限总览加载失败"}</p>
              <p className="mt-0.5 text-muted-foreground text-xs">
                {unsupported
                  ? "后端没有返回门限总览所需的字段（演示模式或后端版本较旧），本页无法判断监管是否生效。下方各卡片的配置仍可正常查看与修改。"
                  : "无法确认监管当前是否生效，请检查后端服务后重试。下方各卡片的配置仍可正常查看与修改。"}
              </p>
            </div>
          </div>
          {!unsupported && (
            <Button variant="outline" size="sm" className="shrink-0" onClick={() => setReloadKey((k) => k + 1)}>
              重试
            </Button>
          )}
        </CardContent>
      </Card>
    );
  }

  const tools = ov.enabled_tools;
  const toolsOk = tools.length > 0;
  // 三层门限的生效判定。工具范围是所有层的前置条件:不在范围内的工具调用根本不会
  // 进入拦截链路(见 intercept.IsToolEnabled),所以工具为空时三层都不生效。
  const rulesOn = toolsOk && ov.rules_enabled > 0;
  const engineOn = toolsOk && ov.jev_enabled && ov.jev_configured;
  const judgeOn = toolsOk && ov.judge_enabled;
  const anyOn = rulesOn || engineOn || judgeOn;

  // 决策引擎的状态描述:未启用 → 缺凭据 → 已启用。用 if 链而非嵌套三元,便于后续再加
  // 状态(如"协议预留")时不会写出难读的深层嵌套。
  let engineState: { label: string; tone: string } = {
    label: "已启用",
    tone: "text-emerald-600 dark:text-emerald-400",
  };
  if (!ov.jev_enabled) {
    engineState = { label: "未启用", tone: "text-muted-foreground" };
  } else if (!ov.jev_configured) {
    engineState = { label: "缺凭据", tone: "text-destructive" };
  }

  const status = ov.status_counts;
  const flagged = countOf(status, "flagged");
  const pending = countOf(status, "pending");
  const timeout = countOf(status, "timeout");
  const allowedTotal = countOf(status, "allowed");
  const deniedTotal = countOf(status, "denied");
  const auditTotal = allowedTotal + deniedTotal + flagged + pending + timeout;
  const splitOk = split.allowedRule !== null && split.allowedModel !== null;
  // 兜底取 max(0, ...):拆分计数与聚合计数理论上同源不会矛盾,但两者是两次独立查询,
  // 中间有新行落库时会短暂不一致;负数出现在页面上会像 bug,钳到 0。
  const otherAllowed = splitOk
    ? Math.max(0, allowedTotal - (split.allowedRule ?? 0) - (split.allowedModel ?? 0))
    : null;

  // 规则侧:action_counts 数的是规则条数,不是命中次数。
  const ruleActions: Array<{ key: string; label: string }> = [
    { key: "deny", label: "正则拦截" },
    { key: "flag", label: "AI判疑" },
    { key: "ask", label: "转人工" },
    { key: "allow", label: "放通" },
  ];
  const ruleActionText = ruleActions.map((a) => `${a.label} ${countOf(ov.action_counts, a.key)} 条`).join(" · ");

  // 各层的结论文案。抽成变量而不是写在 JSX 里的三元链,是因为这三段都需要两级判断,
  // 嵌在 props 里会同时踩到可读性和 lint(noNestedTernary)。
  const ON = "text-emerald-600 dark:text-emerald-400";
  const OFF = "text-muted-foreground";
  const rulesState = rulesOn ? { label: "已生效", tone: ON } : { label: "未生效", tone: OFF };
  const rulesDetail =
    ov.rules_enabled > 0
      ? `已启用 ${ov.rules_enabled} / 共 ${ov.rules_total} 条规则`
      : `共 ${ov.rules_total} 条规则，但没有启用中的规则`;
  let engineDetail = "JEV 已启用且凭据已配置，规则未命中时先由它判定";
  if (!ov.jev_enabled) {
    engineDetail = "JEV 未启用，这一层不参与判定";
  } else if (!ov.jev_configured) {
    engineDetail = "JEV 已启用但没有 API 凭据，不会生效";
  }
  const judgeState = ov.judge_enabled ? { label: "已生效", tone: ON } : { label: "未生效", tone: OFF };
  const judgeDetail = ov.judge_enabled
    ? "规则与决策引擎都未给出结论时，由模型兜底审查"
    : "未启用，规则未命中时不再有模型兜底";

  return (
    <Card>
      <CardContent className="space-y-4 p-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-2">
            <GaugeIcon className="h-4 w-4 text-muted-foreground" />
            <p className="font-medium text-sm">门限总览</p>
            <span className="text-muted-foreground text-xs">监管是否生效 · 拦截范围 · 五档动作分布</span>
          </div>
          <Button variant="ghost" size="sm" className="h-7 text-xs" onClick={() => setReloadKey((k) => k + 1)}>
            <RefreshCwIcon className={`h-3.5 w-3.5 ${loading ? "animate-spin" : ""}`} />
            刷新
          </Button>
        </div>

        {/* ---- 生效状态条:整页最重要的一句话 ---- */}
        <div
          className={`flex flex-wrap items-center justify-between gap-x-4 gap-y-2 rounded-lg border px-4 py-3 ${
            anyOn
              ? "border-emerald-400/50 bg-emerald-50/40 dark:bg-emerald-950/20"
              : "border-destructive/40 bg-destructive/5"
          }`}
        >
          <div className="flex min-w-0 items-center gap-2.5">
            {anyOn ? (
              <ShieldCheckIcon className="h-5 w-5 shrink-0 text-emerald-600 dark:text-emerald-400" />
            ) : (
              <ShieldAlertIcon className="h-5 w-5 shrink-0 text-destructive" />
            )}
            <div>
              <p className="font-semibold text-sm leading-tight">{anyOn ? "监管生效中" : "监管未生效"}</p>
              <p className="mt-0.5 text-muted-foreground text-xs">
                {anyOn
                  ? "在拦截范围内的工具调用会依次经过：拦截规则 → 决策引擎 → 监管模型"
                  : "没有任何一层门限会介入工具调用，AI 的操作当前不受审查"}
              </p>
            </div>
          </div>
          <div className="flex shrink-0 items-center gap-2">
            <span className="text-muted-foreground text-xs">拦截范围</span>
            {toolsOk ? (
              <Badge variant="secondary" className="shrink-0">
                {tools.length} 个工具
              </Badge>
            ) : (
              <Badge variant="destructive" className="shrink-0">
                未启用任何工具
              </Badge>
            )}
          </div>
        </div>

        {/* 工具范围为 0 是"看起来配好了其实全都不生效"的典型陷阱,单独显眼提示。 */}
        {!toolsOk && (
          <div className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-amber-400/60 bg-amber-50/50 px-3 py-2 dark:bg-amber-950/20">
            <p className="text-amber-700 text-xs dark:text-amber-500">
              未启用任何工具 — 拦截规则、决策引擎与监管模型都不会介入任何工具调用。
            </p>
            <Button variant="default" size="sm" className="h-7 shrink-0 text-xs" asChild>
              <Link href="/system/intercept">调整拦截范围</Link>
            </Button>
          </div>
        )}

        {toolsOk ? (
          <p className="text-muted-foreground text-xs" title={tools.join("、")}>
            拦截范围内工具：<span className="truncate">{tools.slice(0, 8).join("、")}</span>
            {tools.length > 8 ? ` 等 ${tools.length} 个` : ""}
          </p>
        ) : null}

        {/* ---- 三层门限各自的生效状态 ---- */}
        <div className="space-y-2">
          <LayerRow title="拦截规则（正则/字符串）" state={rulesState} detail={rulesDetail} href="/system/intercept" />
          <LayerRow
            title="决策引擎（前置快速判定）"
            state={engineState}
            detail={engineDetail}
            href="/system/decision"
          />
          <LayerRow title="监管模型（LLM 语义审查）" state={judgeState} detail={judgeDetail} />
        </div>

        {/* ---- 五档动作(审计侧:历史实际落库) ---- */}
        <div className="space-y-2">
          <div className="flex flex-wrap items-baseline justify-between gap-2">
            <p className="font-semibold text-[10px] text-muted-foreground uppercase tracking-wider">
              五档动作 · 历史命中
            </p>
            <p className="text-[10px] text-muted-foreground">数据来源：审批审计表按状态聚合，共 {auditTotal} 条记录</p>
          </div>
          <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-5">
            <TierTile
              icon={<BanIcon className="h-3.5 w-3.5" />}
              label="正则拦截"
              count={split.deniedRule}
              hint="规则命中且动作为拦截，直接阻止执行"
              tone={TONE.ruleDeny}
            />
            <TierTile
              icon={<BotIcon className="h-3.5 w-3.5" />}
              label="AI拦截"
              count={split.deniedModel}
              hint="决策引擎或监管模型判定危险，直接阻止执行"
              tone={TONE.modelDeny}
            />
            <TierTile
              icon={<ScanSearchIcon className="h-3.5 w-3.5" />}
              label="AI判疑"
              count={flagged}
              hint="标记但放通：工具照常执行，仅留痕供事后复盘"
              tone={TONE.flagged}
            />
            <TierTile
              icon={<CheckCircle2Icon className="h-3.5 w-3.5" />}
              label="AI放通"
              count={allowedTotal}
              hint={`模型判定安全放行 ${split.allowedModel ?? "—"} · 规则放通 ${split.allowedRule ?? "—"} · 人工批准或来源未标注 ${otherAllowed ?? "—"}`}
              tone={TONE.allowed}
            />
            <TierTile
              icon={<UserCheckIcon className="h-3.5 w-3.5" />}
              label="AI转人工"
              count={pending + timeout}
              hint={`等待人工决定 ${pending} · 超时未决 ${timeout}`}
              tone={TONE.ask}
            />
          </div>
          {!splitOk && (
            <p className="text-[11px] text-amber-600 dark:text-amber-500">
              正则拦截 / AI拦截 / AI放通 三档需要按判定来源拆分审计记录，本次拆分请求失败，暂显示「—」；
              上方「历史命中合计」与「规则侧分布」不受影响。
            </p>
          )}
          <p className="text-[10px] text-muted-foreground leading-relaxed">
            分档口径：正则拦截与 AI拦截是审计的拦截总数（{deniedTotal}）按判定来源拆出的规则侧与模型侧，
            两者之和可能小于拦截总数（来源未标注的历史行不计入任何一侧）。「AI转人工」为待审批 {pending} 与超时未决{" "}
            {timeout} 之和；人工处理完成后该行状态会转为放通或拒绝，因此这一档数的是"尚未有结论"的量。
          </p>
        </div>

        {/* ---- 口径说明:规则侧与审计侧是两个维度,分开陈述 ---- */}
        <div className="space-y-1.5 rounded-md bg-muted/40 px-3 py-2.5">
          <p className="text-[11px] text-muted-foreground">
            <span className="font-medium text-foreground">规则侧（配置了多少条规则）</span>
            ：共 {ov.rules_total} 条 · 启用 {ov.rules_enabled} 条 · {ruleActionText}。
            <Link
              href="/system/intercept"
              className="ml-1 underline decoration-dotted underline-offset-2 hover:text-foreground"
            >
              去配置规则
            </Link>
          </p>
          <p className="text-[11px] text-muted-foreground">
            <span className="font-medium text-foreground">审计侧（历史落库多少条）</span>
            ：放通 {allowedTotal}（规则 {split.allowedRule ?? "—"} · 模型 {split.allowedModel ?? "—"} ·
            人工批准或未标注来源 {otherAllowed ?? "—"}）· 拦截 {deniedTotal}（规则 {split.deniedRule ?? "—"} · 模型{" "}
            {split.deniedModel ?? "—"}）· 判疑 {flagged} · 待审批 {pending} · 超时 {timeout}。
            <Link
              href="/system/intercept/approvals"
              className="ml-1 underline decoration-dotted underline-offset-2 hover:text-foreground"
            >
              查看审批记录
            </Link>
          </p>
          <p className="text-[10px] text-muted-foreground">
            两行数字口径不同，不能相加：规则侧数的是「配了几条规则」，审计侧数的是「历史命中了几次」。
            判疑档也可能来自把动作配成「AI判疑」的规则，不限于模型判定。
          </p>
        </div>
      </CardContent>
    </Card>
  );
}
