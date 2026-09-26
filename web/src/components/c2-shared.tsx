"use client";

// C2 中心共用工具:会话/任务/战利品/载荷的中文标签与徽标样式、时间与字节的
// 本地格式化、URL 拼接(信道路径 / 投递 URL)。六个页面共用这一份,保证同一
// 状态在各页面的颜色与叫法完全一致(仿 proxy-dialogs.tsx 的收口纪律)。
//
// 时间/字节格式化不 import vps-shared:C2 页面只依赖自己的 c2-* 组件与冻结的
// lib/api.ts、lib/types.ts,避免把 VPS 的对话框组件拖进依赖图。

import { Badge } from "@/components/ui/badge";
// 协议/产物徽标助手把图标组件存进 META 表当值用,这里必须按值导入;LucideIcon 是纯类型。
import {
  AntennaIcon,
  BinaryIcon,
  BoxesIcon,
  CloudIcon,
  FileCodeIcon,
  GlobeIcon,
  NetworkIcon,
  PackageIcon,
  WaypointsIcon,
  ZapIcon,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";
import type {
  C2Direction,
  C2EvasionTargetAV,
  C2JobKind,
  C2JobStatus,
  C2LootKind,
  C2Listener,
  C2PayloadArtifact,
  C2PayloadFormat,
  C2PayloadTarget,
  C2Proto,
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

const TASK_KIND_LABEL: Record<C2TaskKind, string> & { plugin: string } = {
  shell: "命令",
  files_list: "文件浏览",
  upload: "上传",
  download: "下载",
  loot: "战利品",
  info: "信息采集",
  plugin: "插件",
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

// ══ 三期(契约 §17-§22):传输协议 / 载荷矩阵 / 连接方向 ══
// 本段由三期前端(Agent C)追加;后段函数命名保持 c2 前缀,与插件助手互不重叠。

// ---- 传输协议(监听器 proto / 会话 transport 共用)----

const C2_PROTO_META: Record<C2Proto, { label: string; badge: string; desc: string; Icon: LucideIcon }> = {
  http: {
    label: "HTTP",
    badge: "bg-blue-500/15 text-blue-600 border-blue-500/30",
    desc: "HTTP 轮询回连:兼容性最好,流量伪装成普通 Web 请求",
    Icon: GlobeIcon,
  },
  ws: {
    label: "WebSocket",
    badge: "bg-indigo-500/15 text-indigo-600 border-indigo-500/30",
    desc: "WebSocket 长连接:低延迟准实时下发,与 HTTP 同端口语义",
    Icon: WaypointsIcon,
  },
  tcp: {
    label: "TCP",
    badge: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30",
    desc: "TCP 长度前缀帧:裸 socket 直连,支持正向 bind",
    Icon: NetworkIcon,
  },
  kcp: {
    label: "KCP",
    badge: "bg-amber-500/15 text-amber-600 border-amber-500/30",
    desc: "KCP 协议:抗丢包快速重传;载荷仅支持 py/gosrc,支持正向 bind",
    Icon: ZapIcon,
  },
  oss: {
    label: "OSS",
    badge: "bg-cyan-500/15 text-cyan-600 border-cyan-500/30",
    desc: "对象存储中转:无连接,bucket 双向队列交换帧,beacon 直连云端",
    Icon: CloudIcon,
  },
};

/** 五种传输协议的展示顺序(监听器协议选择/徽标共用)。 */
export const C2_PROTOS: C2Proto[] = ["http", "ws", "tcp", "kcp", "oss"];

export function c2ProtoMeta(proto: string): { label: string; badge: string; desc: string; Icon: LucideIcon } {
  return (
    C2_PROTO_META[proto as C2Proto] ?? {
      label: proto,
      badge: "bg-muted text-muted-foreground",
      desc: "",
      Icon: GlobeIcon,
    }
  );
}

// ---- 载荷矩阵(契约 §19 合法组合表 + §18 beacon 模板支持矩阵,freeze)----
// 前后端同一份校验语义:后端组合校验失败 400 带原因,前端按本表禁选并给原因 tooltip。

/** 一条载荷组合查询:c2ComboAllowed 的入参集合。proto/target 可省略(缺省不校验该轴)。 */
export interface C2PayloadCombo {
  format: C2PayloadFormat;
  artifact: C2PayloadArtifact;
  direction: C2Direction;
  staged: boolean;
  proto?: C2Proto;
  target?: C2PayloadTarget;
}

interface C2MatrixRow {
  /** 该产物允许的格式。 */
  formats: C2PayloadFormat[];
  /** 是否支持 staged(stager)形态。 */
  staged: boolean;
  /** 不支持 staged 时给解释(禁用 tooltip 文案)。 */
  stagedReason: string;
  /** 允许的连接方向;缺省 = reverse|bind 都可。 */
  directions?: C2Direction[];
  /** 允许的目标平台;缺省不限(ebpf 仅 linux)。 */
  targets?: C2PayloadTarget[];
  /** 产物一句话说明(选项旁文案)。 */
  desc: string;
}

/** §19 合法组合表:产物 → 允许的格式 / staged 能力 / 方向 / 目标平台。 */
export const C2_PAYLOAD_MATRIX: Record<C2PayloadArtifact, C2MatrixRow> = {
  script: {
    formats: ["ps1", "bash", "py"],
    staged: false,
    stagedReason: "script 不做 staged:脚本整段直出执行,无需 stager",
    desc: "ps1 / bash / py 源码载荷,目标机拉取后就地执行",
  },
  exe: {
    formats: ["gosrc"],
    staged: true,
    stagedReason: "",
    desc: "gosrc 经 VPS go build 编译;staged = 小体积 stager 拉完整产物落盘执行(OPSEC 弱)",
  },
  dll: {
    formats: ["gosrc"],
    staged: false,
    stagedReason: "dll 不支持 staged:导出 Run 入口按 stageless 交付(rundll32 或加载器调用)",
    desc: "gosrc c-shared 编译的动态库,导出 Run 入口",
  },
  shellcode: {
    formats: ["gosrc"],
    staged: true,
    stagedReason: "",
    desc: "dll 产物经 sRDI 转换,加载即执行;staged = bootstrap 从信道拉 DLL stage 后反射加载",
  },
  ebpf: {
    formats: ["gosrc"],
    staged: false,
    stagedReason: "eBPF 触发器不支持 staged:源码工程 + 配套 bind beacon 直出",
    directions: ["bind"],
    targets: ["linux"],
    desc: "tc/XDP 魔法包触发器,仅 linux + bind;构建需 VPS 有 clang",
  },
};

/** §18 beacon 模板支持矩阵:各传输协议支持的载荷格式(kcp 仅 py/gosrc,ps1/bash 不可用)。 */
export const C2_PROTO_FORMAT_SUPPORT: Record<C2Proto, C2PayloadFormat[]> = {
  http: ["ps1", "bash", "py", "gosrc"],
  ws: ["ps1", "bash", "py", "gosrc"],
  tcp: ["ps1", "bash", "py", "gosrc"],
  kcp: ["py", "gosrc"],
  oss: ["ps1", "bash", "py", "gosrc"],
};

export const C2_PAYLOAD_FORMATS: C2PayloadFormat[] = ["ps1", "bash", "py", "gosrc"];
export const C2_PAYLOAD_ARTIFACTS: C2PayloadArtifact[] = ["script", "exe", "dll", "shellcode", "ebpf"];
export const C2_DIRECTIONS: C2Direction[] = ["reverse", "bind"];

// c2ComboAllowed §19 合法组合表的唯一前端实现,载荷页四级联动与监听器页 kcp
// 提示共用;返回 allowed=false 时 reason 即禁选原因(tooltip / toast 文案)。
export function c2ComboAllowed(
  format: C2PayloadFormat,
  artifact: C2PayloadArtifact,
  direction: C2Direction,
  staged: boolean,
  proto?: C2Proto,
  target?: C2PayloadTarget,
): { allowed: boolean; reason: string } {
  const row = C2_PAYLOAD_MATRIX[artifact];
  const am = c2ArtifactMeta(artifact);
  if (!row) return { allowed: false, reason: `未知产物形态:${artifact}` };
  if (!row.formats.includes(format)) {
    return {
      allowed: false,
      reason: `${am.label} 仅支持 ${row.formats.join(" / ")} 格式(${format} 不可用)`,
    };
  }
  if (staged && !row.staged) return { allowed: false, reason: row.stagedReason };
  const dirs = row.directions ?? C2_DIRECTIONS;
  if (!dirs.includes(direction)) {
    return {
      allowed: false,
      reason: `${am.label} 仅支持${dirs.map((d) => c2DirectionMeta(d).label).join(" / ")}(${c2DirectionMeta(direction).label} 不可用)`,
    };
  }
  if (target && row.targets && !row.targets.includes(target)) {
    return {
      allowed: false,
      reason: `${am.label} 仅支持 ${row.targets.map((t) => c2PayloadTargetLabel(t)).join(" / ")} 目标(${c2PayloadTargetLabel(target)} 不可用)`,
    };
  }
  if (proto && !C2_PROTO_FORMAT_SUPPORT[proto].includes(format)) {
    const ok = C2_PROTO_FORMAT_SUPPORT[proto];
    return {
      allowed: false,
      reason: `${c2ProtoMeta(proto).label} 传输下 ${format} 不可用(该协议载荷仅支持 ${ok.join(" / ")})`,
    };
  }
  return { allowed: true, reason: "" };
}

// ---- 产物形态徽标/说明 ----

const C2_ARTIFACT_META: Record<C2PayloadArtifact, { label: string; badge: string; desc: string; Icon: LucideIcon }> = {
  script: {
    label: "脚本",
    badge: "bg-blue-500/15 text-blue-600 border-blue-500/30",
    desc: "ps1 / bash / py 源码,目标机拉取后就地执行",
    Icon: FileCodeIcon,
  },
  exe: {
    label: "EXE",
    badge: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30",
    desc: "gosrc 经 VPS go build 编译的可执行文件",
    Icon: BinaryIcon,
  },
  dll: {
    label: "DLL",
    badge: "bg-purple-500/15 text-purple-600 border-purple-500/30",
    desc: "c-shared 编译动态库,导出 Run 入口(rundll32 或加载器调用)",
    Icon: BoxesIcon,
  },
  shellcode: {
    label: "Shellcode",
    badge: "bg-amber-500/15 text-amber-600 border-amber-500/30",
    desc: "经 sRDI 转换,加载即执行",
    Icon: ZapIcon,
  },
  ebpf: {
    label: "eBPF",
    badge: "bg-cyan-500/15 text-cyan-600 border-cyan-500/30",
    desc: "linux + bind 专属触发器;构建需 VPS 有 clang",
    Icon: AntennaIcon,
  },
};

export function c2ArtifactMeta(artifact: string): { label: string; badge: string; desc: string; Icon: LucideIcon } {
  return (
    C2_ARTIFACT_META[artifact as C2PayloadArtifact] ?? {
      label: artifact,
      badge: "bg-muted text-muted-foreground",
      desc: "",
      Icon: PackageIcon,
    }
  );
}

// ---- 连接方向(reverse / bind)----

const C2_DIRECTION_META: Record<C2Direction, { label: string; badge: string; desc: string }> = {
  reverse: {
    label: "反向",
    badge: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30",
    desc: "beacon 主动连回平台监听器",
  },
  bind: {
    label: "正向",
    badge: "bg-amber-500/15 text-amber-600 border-amber-500/30",
    desc: "beacon 在目标机监听端口,平台主动连接(仅 tcp/kcp)",
  },
};

export function c2DirectionMeta(direction: string): { label: string; badge: string; desc: string } {
  return (
    C2_DIRECTION_META[direction as C2Direction] ?? {
      label: direction,
      badge: "bg-muted text-muted-foreground",
      desc: "",
    }
  );
}

// c2SessionDirection 从会话 meta 里读连接方向:三期 bind 会话由平台在注册时标注
// meta.direction="bind"(契约 §18);一期/反向数据缺省 reverse。meta 可能是
// null(后端 nil map 序列化),按 unknown 兜底。
export function c2SessionDirection(meta: unknown): C2Direction {
  if (meta && typeof meta === "object" && (meta as Record<string, unknown>).direction === "bind") {
    return "bind";
  }
  return "reverse";
}

// ══ 三期(契约 §21):插件板块(Agent G 追加,纯 append,不动上文) ══

// ---- 插件形态 / 语言 / 平台徽标(插件库页与会话「插件」页签共用) ----

const PLUGIN_KIND_META: Record<string, { label: string; badge: string }> = {
  script: { label: "脚本", badge: "bg-blue-500/15 text-blue-600 border-blue-500/30" },
  binary: { label: "二进制", badge: "bg-purple-500/15 text-purple-600 border-purple-500/30" },
};

export function c2PluginKindMeta(kind: string): { label: string; badge: string } {
  return PLUGIN_KIND_META[kind] ?? { label: kind, badge: "bg-muted text-muted-foreground" };
}

const PLUGIN_LANGUAGE_META: Record<string, { label: string; badge: string }> = {
  ps1: { label: "PowerShell", badge: "bg-sky-500/15 text-sky-600 border-sky-500/30" },
  sh: { label: "Shell", badge: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30" },
  py: { label: "Python", badge: "bg-amber-500/15 text-amber-600 border-amber-500/30" },
};

export function c2PluginLanguageMeta(language: string): { label: string; badge: string } {
  return PLUGIN_LANGUAGE_META[language] ?? { label: language, badge: "bg-muted text-muted-foreground" };
}

const PLUGIN_PLATFORM_BADGE: Record<string, string> = {
  windows: "bg-blue-500/15 text-blue-600 border-blue-500/30",
  linux: "bg-orange-500/15 text-orange-600 border-orange-500/30",
  macos: "bg-zinc-500/15 text-zinc-600 border-zinc-500/30",
  cross: "bg-teal-500/15 text-teal-600 border-teal-500/30",
};

// c2PluginPlatformBadge 插件平台徽标样式(文案复用上面的 c2PayloadTargetLabel)。
export function c2PluginPlatformBadge(platform: string): string {
  return PLUGIN_PLATFORM_BADGE[platform] ?? "bg-muted text-muted-foreground";
}

// ---- params_schema 的前端约定(与后端 server/c2_plugin.go 渲染器同一口径) ----

/** 单个插件参数的声明(type: string|bool|enum,可选 default/options/desc)。 */
export interface C2PluginParamDef {
  name: string;
  type: "string" | "bool" | "enum";
  default: string;
  options: string[];
  desc: string;
}

// c2PluginParamDefs 把 params_schema 对象收敛成参数声明列表,供动态表单渲染;
// 坏 schema 容错为空列表。file/content 是后端保留占位,不接受用户输入,这里同样
// 剔除;after_clean 是 binary 清理开关,保留(bool 表单可切换)。
export function c2PluginParamDefs(schema: Record<string, unknown> | null | undefined): C2PluginParamDef[] {
  const out: C2PluginParamDef[] = [];
  const raw =
    schema && typeof schema === "object" && !Array.isArray(schema)
      ? (schema as Record<string, unknown>)
      : {};
  for (const [name, value] of Object.entries(raw)) {
    if (name === "file" || name === "content") continue;
    if (!value || typeof value !== "object" || Array.isArray(value)) continue;
    const f = value as Record<string, unknown>;
    const type = f.type === "bool" ? "bool" : f.type === "enum" ? "enum" : "string";
    const options = Array.isArray(f.options)
      ? f.options.filter((o): o is string => typeof o === "string")
      : [];
    const def =
      typeof f.default === "string"
        ? f.default
        : typeof f.default === "boolean"
          ? f.default
            ? "1"
            : "0"
          : "";
    out.push({
      name,
      type,
      default: def,
      options,
      desc: typeof f.desc === "string" ? f.desc : "",
    });
  }
  return out;
}

// c2PluginDefaultParams 按参数声明生成默认取值表(bool 归一 "1"/"0")。
export function c2PluginDefaultParams(defs: C2PluginParamDef[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const d of defs) {
    if (d.default !== "") {
      out[d.name] = d.type === "bool" ? (["1", "true"].includes(d.default.toLowerCase()) ? "1" : "0") : d.default;
    }
  }
  return out;
}

// c2PluginPlatformMatches 插件平台与会话操作系统的适配判断(cross 恒适配;
// beacon 上报的 os 串按关键词识别 windows/linux/macos)。
export function c2PluginPlatformMatches(platform: string, sessionOS: string): boolean {
  if (!platform || platform === "cross") return true;
  const os = (sessionOS ?? "").toLowerCase();
  switch (platform) {
    case "windows":
      return os.includes("win");
    case "linux":
      return os.includes("linux");
    case "macos":
      return os.includes("dar") || os.includes("mac");
    default:
      return true;
  }
}
