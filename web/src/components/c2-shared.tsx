"use client";

// C2 中心共用工具:会话/任务/战利品/载荷的中文标签与徽标样式、时间与字节的
// 本地格式化、URL 拼接(信道路径 / 投递 URL)。六个页面共用这一份,保证同一
// 状态在各页面的颜色与叫法完全一致(仿 proxy-dialogs.tsx 的收口纪律)。
//
// 时间/字节格式化不 import vps-shared:C2 页面只依赖自己的 c2-* 组件与冻结的
// lib/api.ts、lib/types.ts,避免把 VPS 的对话框组件拖进依赖图。

import { Badge } from "@/components/ui/badge";
import type {
  C2EvasionTargetAV,
  C2JobKind,
  C2JobStatus,
  C2LootKind,
  C2Listener,
  C2PayloadFormat,
  C2SessionStatus,
  C2StaticReport,
  C2TaskKind,
  C2TaskStatus,
} from "@/lib/types";
import { cn } from "@/lib/utils";

// ---- 格式化 ----

// num 把可能为 null / undefined / NaN 的数值收敛成 0(后端 nil slice → JSON null
// 是这台平台的既有行为,来自接口的数字一律过一遍再参与运算)。
export function num(n: number | null | undefined): number {
  return typeof n === "number" && Number.isFinite(n) ? n : 0;
}

export function fmtTime(iso?: string | null): string {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

// relTime 心跳相对时间:「12 秒前」。超过 30 天退回绝对时间,避免「872 天前」没体感。
export function relTime(iso?: string | null): string {
  if (!iso) return "—";
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return "—";
  const s = Math.floor((Date.now() - t) / 1000);
  if (s < 0) return "刚刚";
  if (s < 60) return `${s} 秒前`;
  if (s < 3600) return `${Math.floor(s / 60)} 分钟前`;
  if (s < 86400) return `${Math.floor(s / 3600)} 小时前`;
  if (s < 86400 * 30) return `${Math.floor(s / 86400)} 天前`;
  return fmtTime(iso);
}

export function fmtBytes(n: number): string {
  const v = num(n);
  if (v <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.min(Math.floor(Math.log(v) / Math.log(1024)), units.length - 1);
  const b = v / 1024 ** i;
  return `${i === 0 ? b : b.toFixed(1)} ${units[i]}`;
}

// fmtSleep 心跳节奏:「60s ±10%」。sleep=0 视为未设置。
export function fmtSleep(sleepSec: number, jitterPct: number): string {
  const s = num(sleepSec);
  if (s <= 0) return "—";
  const j = num(jitterPct);
  return j > 0 ? `${s}s ±${j}%` : `${s}s`;
}

// ---- 会话状态 ----

const SESSION_STATUS_META: Record<C2SessionStatus, { label: string; dot: string; badge: string }> = {
  online: { label: "在线", dot: "bg-emerald-500", badge: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30" },
  offline: { label: "失联", dot: "bg-muted-foreground/40", badge: "bg-muted text-muted-foreground" },
  dead: { label: "终止", dot: "bg-red-500", badge: "bg-red-500/15 text-red-600 border-red-500/30" },
};

export function c2SessionStatusMeta(status: string): { label: string; dot: string; badge: string } {
  return SESSION_STATUS_META[status as C2SessionStatus] ?? { label: status, dot: "bg-muted-foreground/40", badge: "bg-muted text-muted-foreground" };
}

// 状态排序人序:在线 → 失联 → 终止(列表表头当前页排序用)。
export const C2_SESSION_STATUS_RANK: Record<string, number> = { online: 0, offline: 1, dead: 2 };

// ---- 任务 ----

const TASK_STATUS_META: Record<C2TaskStatus, { label: string; badge: string }> = {
  queued: { label: "排队", badge: "bg-amber-500/15 text-amber-600 border-amber-500/30" },
  sent: { label: "已下发", badge: "bg-blue-500/15 text-blue-600 border-blue-500/30" },
  done: { label: "完成", badge: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30" },
  error: { label: "失败", badge: "bg-red-500/15 text-red-600 border-red-500/30" },
};

export function c2TaskStatusMeta(status: string): { label: string; badge: string } {
  return TASK_STATUS_META[status as C2TaskStatus] ?? { label: status, badge: "bg-muted text-muted-foreground" };
}

const TASK_KIND_LABEL: Record<C2TaskKind, string> = {
  shell: "命令",
  files_list: "文件浏览",
  upload: "上传",
  download: "下载",
  loot: "战利品",
  info: "信息采集",
};

export function c2TaskKindLabel(kind: string): string {
  return TASK_KIND_LABEL[kind as C2TaskKind] ?? kind;
}

// ---- 战利品 ----

const LOOT_KIND_META: Record<C2LootKind, { label: string; badge: string }> = {
  credential: { label: "凭据", badge: "bg-red-500/15 text-red-600 border-red-500/30" },
  hash: { label: "哈希", badge: "bg-purple-500/15 text-purple-600 border-purple-500/30" },
  file: { label: "文件", badge: "bg-blue-500/15 text-blue-600 border-blue-500/30" },
  screenshot: { label: "截图", badge: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30" },
  other: { label: "其他", badge: "bg-muted text-muted-foreground" },
};

export const C2_LOOT_KINDS: C2LootKind[] = ["credential", "hash", "file", "screenshot", "other"];

export function c2LootKindMeta(kind: string): { label: string; badge: string } {
  return LOOT_KIND_META[kind as C2LootKind] ?? { label: kind, badge: "bg-muted text-muted-foreground" };
}

// ---- 载荷 ----

const PAYLOAD_FORMAT_META: Record<C2PayloadFormat, { label: string; badge: string }> = {
  ps1: { label: "ps1", badge: "bg-blue-500/15 text-blue-600 border-blue-500/30" },
  bash: { label: "bash", badge: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30" },
  py: { label: "py", badge: "bg-amber-500/15 text-amber-600 border-amber-500/30" },
  gosrc: { label: "go 源码", badge: "bg-purple-500/15 text-purple-600 border-purple-500/30" },
};

export function c2PayloadFormatMeta(format: string): { label: string; badge: string } {
  return PAYLOAD_FORMAT_META[format as C2PayloadFormat] ?? { label: format, badge: "bg-muted text-muted-foreground" };
}

export const C2_PAYLOAD_TARGET_LABEL: Record<string, string> = {
  windows: "Windows",
  linux: "Linux",
  macos: "macOS",
  cross: "跨平台",
};

export function c2PayloadTargetLabel(target: string): string {
  return C2_PAYLOAD_TARGET_LABEL[target] ?? target;
}

// ---- URL 拼接 ----

// joinURL 把回调地址与路径段拼成一个 URL:吃掉两端的重复斜杠,
// 「http://1.2.3.4:8443/」 + 「/abc」 → 「http://1.2.3.4:8443/abc」。
function joinURL(base: string, ...segments: string[]): string {
  const parts = [base, ...segments].map((s) => s.replace(/^\/+|\/+$/g, "")).filter(Boolean);
  return [base.replace(/\/+$/, ""), ...parts.slice(1)].filter(Boolean).join("/");
}

// listenerChannelURL 信道路径:<callback>/<url_path>(beacon POST /<url_path>/e 的根)。
// 也用作监听器列表「复制回调地址」的剪贴板内容。
export function listenerChannelURL(l: Pick<C2Listener, "callback" | "url_path">): string {
  if (!l.callback) return "";
  return joinURL(l.callback, l.url_path);
}

// payloadDeliveryURL 载荷投递 URL:<callback>/<url_path>/p/<token>(beacon GET 拉源码)。
// listener 缺失(已删除)或未配回调地址时返回空串,调用方禁用复制按钮。
export function payloadDeliveryURL(
  l: Pick<C2Listener, "callback" | "url_path"> | undefined,
  token: string,
): string {
  if (!l?.callback) return "";
  return joinURL(l.callback, l.url_path, "p", token);
}

// randomURLPath 信道路径随机段:新建监听器时自动生成的小写字母数字段。
export function randomURLPath(len = 10): string {
  const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789";
  const buf = new Uint32Array(len);
  if (typeof crypto !== "undefined" && crypto.getRandomValues) {
    crypto.getRandomValues(buf);
  } else {
    for (let i = 0; i < len; i++) buf[i] = Math.floor(Math.random() * 2 ** 32);
  }
  return Array.from(buf, (n) => alphabet[n % alphabet.length]).join("");
}

// guessFileRoot 按会话操作系统猜文件浏览的初始路径。
export function guessFileRoot(os: string): string {
  return /win/i.test(os) ? "C:\\" : "/";
}

// pathJoin 远端路径拼接:.. 与 . 交给目标侧语义,这里只做斜杠缝合。
export function pathJoin(dir: string, name: string): string {
  const d = dir.endsWith("\\") || dir.endsWith("/") || dir === "" ? dir : `${dir}/`;
  return `${d}${name}`;
}

// parentPath 上一级目录(带结尾斜杠):已是顶层时返回空串,调用方据此隐藏「..」行。
// 「/var/www」→「/var/」、「/var」→「/」、「C:\Users\admin」→「C:\」。
export function parentPath(p: string): string {
  if (!p) return "";
  const trimmed = p.replace(/[\\/]+$/, "");
  if (!trimmed) return "";
  const idx = Math.max(trimmed.lastIndexOf("/"), trimmed.lastIndexOf("\\"));
  if (idx < 0) return "";
  return trimmed.slice(0, idx + 1);
}

// 任务 input 展示:后端按 beacon 约定装 JSON(shell={"command":…}/files_list={"path":…}/
// upload/download={"transfer_id":…,"remote_path":…}),页面展示时解一层还原人话;
// 纯文本(兼容口径)与解析失败原样返回。
export function c2TaskInputText(input: string): string {
  const s = (input ?? "").trim();
  if (!s.startsWith("{")) return s;
  try {
    const o = JSON.parse(s) as Record<string, unknown>;
    if (typeof o.command === "string") return o.command;
    if (typeof o.path === "string") return o.path;
    if (typeof o.remote_path === "string") return o.remote_path;
    return s;
  } catch {
    return s;
  }
}

// ══ 二期(契约 §12-§15):免杀档案 / 异步作业 / 静态报告 ══

// ---- 目标 AV(免杀档案)----

const EVASION_AV_META: Record<C2EvasionTargetAV, { label: string; badge: string }> = {
  defender: { label: "Defender", badge: "bg-blue-500/15 text-blue-600 border-blue-500/30" },
  "360": { label: "360", badge: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30" },
  huorong: { label: "火绒", badge: "bg-amber-500/15 text-amber-600 border-amber-500/30" },
  generic: { label: "通用", badge: "bg-purple-500/15 text-purple-600 border-purple-500/30" },
  custom: { label: "自定义", badge: "bg-muted text-muted-foreground" },
};

export const C2_EVASION_AVS: C2EvasionTargetAV[] = ["defender", "360", "huorong", "generic", "custom"];

export function c2EvasionAVMeta(av: string): { label: string; badge: string } {
  return EVASION_AV_META[av as C2EvasionTargetAV] ?? { label: av, badge: "bg-muted text-muted-foreground" };
}

// 档案来源:manual(手工维护)/ llm(AI 生成)。
export function c2EvasionSourceMeta(source: string): { label: string; badge: string } {
  return source === "llm"
    ? { label: "AI 生成", badge: "bg-violet-500/15 text-violet-600 border-violet-500/30" }
    : { label: "手工", badge: "bg-muted text-muted-foreground" };
}

// ---- 异步作业(c2_jobs:evasion | build)----

const JOB_STATUS_META: Record<C2JobStatus, { label: string; badge: string }> = {
  queued: { label: "排队中", badge: "bg-amber-500/15 text-amber-600 border-amber-500/30" },
  running: { label: "运行中", badge: "bg-blue-500/15 text-blue-600 border-blue-500/30" },
  done: { label: "完成", badge: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30" },
  error: { label: "失败", badge: "bg-red-500/15 text-red-600 border-red-500/30" },
};

export function c2JobStatusMeta(status: string): { label: string; badge: string } {
  return JOB_STATUS_META[status as C2JobStatus] ?? { label: status, badge: "bg-muted text-muted-foreground" };
}

const JOB_KIND_META: Record<C2JobKind, { label: string; badge: string }> = {
  evasion: { label: "AI 变形", badge: "bg-violet-500/15 text-violet-600 border-violet-500/30" },
  build: { label: "VPS 构建", badge: "bg-cyan-500/15 text-cyan-600 border-cyan-500/30" },
};

export function c2JobKindMeta(kind: string): { label: string; badge: string } {
  return JOB_KIND_META[kind as C2JobKind] ?? { label: kind, badge: "bg-muted text-muted-foreground" };
}

// ---- 静态自测风险等级(契约 §13:low<3 | medium 3-5 | high>5 按命中数分档)----

const RISK_META: Record<string, { label: string; badge: string }> = {
  low: { label: "低", badge: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30" },
  medium: { label: "中", badge: "bg-amber-500/15 text-amber-600 border-amber-500/30" },
  high: { label: "高", badge: "bg-red-500/15 text-red-600 border-red-500/30" },
};

export function c2RiskMeta(risk: string): { label: string; badge: string } {
  return RISK_META[risk] ?? { label: risk, badge: "bg-muted text-muted-foreground" };
}

// ---- VPS 机器状态标注(listeners/payloads 页的机器下拉用,不引 vps-shared 依赖)----

export function c2VpsStatusLabel(status: string): string {
  const map: Record<string, string> = {
    online: "在线",
    offline: "离线",
    busy: "占用中",
    maintenance: "维护中",
    unknown: "未测活",
  };
  return map[status] ?? (status || "未知");
}

// ---- 静态自测报告渲染:免杀页作业详情与载荷页构建详情共用 ----

// C2StaticReportView 渲染 c2_jobs.result 的约定形状(契约 §13/§14):
// 风险等级徽标 + 熵值 + 变形轮次 + 特征串命中列表 + PLAN 说明;
// build 作业另带 build_log(VPS 构建日志尾部),有则一并渲染。
export function C2StaticReportView({
  report,
  className,
}: {
  report: C2StaticReport | null | undefined;
  className?: string;
}) {
  if (!report || (!report.hits?.length && !num(report.entropy) && !report.plan && !report.build_log)) {
    return <p className="text-muted-foreground text-xs">暂无自测报告(作业完成后此处展示)。</p>;
  }
  const hits = report.hits ?? [];
  const entropy = num(report.entropy);
  const risk = c2RiskMeta(report.risk ?? "low");
  return (
    <div className={cn("grid gap-3", className)}>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 text-xs">
        <Badge variant="outline" className={risk.badge} title="按特征串命中数分档:<3 低 / 3-5 中 / >5 高">
          静态风险:{risk.label}
        </Badge>
        <span className="text-muted-foreground" title="Shanon 熵,>5.5 视为偏高">
          熵值:<span className="text-foreground font-mono tabular-nums">{entropy > 0 ? entropy.toFixed(2) : "—"}</span>
          {entropy > 5.5 ? <span className="text-amber-600">(偏高)</span> : null}
        </span>
        {num(report.rounds) > 0 ? (
          <span className="text-muted-foreground">
            变形轮次:<span className="text-foreground tabular-nums">{num(report.rounds)}</span>
          </span>
        ) : null}
      </div>

      <div>
        <p className="mb-1.5 text-xs font-medium">特征串命中({hits.length})</p>
        {hits.length === 0 ? (
          <p className="text-muted-foreground text-xs">未命中任何已知特征串。</p>
        ) : (
          <ul className="grid gap-1">
            {hits.map((h) => (
              <li
                key={h.marker}
                className="flex items-center justify-between gap-2 rounded-md border bg-muted/30 px-2 py-1"
              >
                <code className="min-w-0 truncate font-mono text-xs" title={h.marker}>
                  {h.marker}
                </code>
                <span className="text-muted-foreground shrink-0 text-xs tabular-nums">×{num(h.count)}</span>
              </li>
            ))}
          </ul>
        )}
      </div>

      {report.plan ? (
        <div>
          <p className="mb-1 text-xs font-medium">变形说明(PLAN)</p>
          <pre className="max-h-44 overflow-auto rounded-md border bg-muted/30 p-2 font-mono text-xs leading-relaxed whitespace-pre-wrap break-all">
            {report.plan}
          </pre>
        </div>
      ) : null}

      {report.build_log ? (
        <div>
          <p className="mb-1 text-xs font-medium">VPS 构建日志(尾部)</p>
          <pre className="max-h-52 overflow-auto rounded-md border bg-muted/30 p-2 font-mono text-xs leading-relaxed whitespace-pre-wrap break-all">
            {report.build_log}
          </pre>
        </div>
      ) : null}
    </div>
  );
}
