"use client";

// C2 中心 · 载荷与免杀 —— 载荷生成、投递与清单页。
//
// 生成(三期四级联动,契约 §19/§22):选监听器(回调地址为空的项禁选)→ 产物
// (script/exe/dll/shellcode/ebpf)→ 格式(ps1/bash/py/gosrc)→ 方向
// (reverse/bind)→ staged 开关;按 C2_PAYLOAD_MATRIX 合法组合表禁选非法组合并给
// 原因 tooltip,监听器协议进一步收窄格式(kcp 仅 py/gosrc)。后端渲染源码/经 VPS
// 编译后返回 oneliner。列表支持下载、复制一行命令、复制投递 URL
// (<callback>/<url_path>/p/<token>)与删除。
// 二期(契约 §13/§14):gosrc 可选指派一台 VPS 机器,生成转为异步构建作业
// (源码经 SSH 通道在机器上 go build,产物自动回传),前端 3 秒轮询作业状态;
// 免杀档案变形过的载荷标「已变形」,VPS 编译出的标「已编译」。变形与静态自测
// 在「免杀工作台」页。
// v3.1(契约 §24A):生成表单加「免杀档案」下拉——script 产物(ps1/bash/py)
// 选中档案后提交带 evasion_profile_id,后端改走免杀变形作业管线(与免杀工作台
// 同一条链路),响应带 job_id 复用既有 3 秒作业轮询进度条;编译型产物不支持
// 档案变形,选中档案时预先禁用提交并提示;切格式后不匹配的档案自动清回「不使用」。

import * as React from "react";

import Link from "next/link";
import { useSearchParams } from "next/navigation";

import {
  ChevronDownIcon,
  ChevronUpIcon,
  DownloadIcon,
  LinkIcon,
  PackageOpenIcon,
  PlusIcon,
  RefreshCwIcon,
  SearchIcon,
  ShieldIcon,
  TerminalIcon,
  Trash2Icon,
  TriangleAlertIcon,
} from "lucide-react";
import { toast } from "sonner";

import { TablePagination } from "@/components/table-pagination";
import {
  C2StaticReportView,
  c2ArtifactMeta,
  c2ComboAllowed,
  c2DirectionMeta,
  c2EvasionAVMeta,
  c2EvasionProfileSourceMeta,
  c2JobStatusMeta,
  c2PayloadFormatMeta,
  c2PayloadTargetLabel,
  c2ProtoMeta,
  c2VpsStatusLabel,
  C2_DIRECTIONS,
  C2_PAYLOAD_ARTIFACTS,
  C2_PAYLOAD_FORMATS,
  C2_PAYLOAD_MATRIX,
  C2_PROTO_FORMAT_SUPPORT,
  fmtBytes,
  fmtTime,
  num,
  payloadDeliveryURL,
} from "@/components/c2-shared";
import { CopyButton } from "@/components/copy-button";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type {
  C2Direction,
  C2EvasionProfile,
  C2Job,
  C2Listener,
  C2Payload,
  C2PayloadArtifact,
  C2PayloadFormat,
  C2PayloadTarget,
  C2Proto,
  VpsMachine,
} from "@/lib/types";
import { cn, copyText } from "@/lib/utils";

const PAGE_SIZE = 50;

// Radix Select 的 value="" 是保留值,空语义一律用哨兵串。
const ALL = "__all__";

const TARGETS: C2PayloadTarget[] = ["windows", "linux", "macos", "cross"];

type SortKey = "name" | "created";

// useSearchParams 必须挂在 Suspense 边界内(静态导出约束;?listener_id= 深链)。
export default function C2PayloadsPage() {
  return (
    <React.Suspense fallback={null}>
      <C2PayloadsInner />
    </React.Suspense>
  );
}

function C2PayloadsInner() {
  const searchParams = useSearchParams();
  const [listeners, setListeners] = React.useState<C2Listener[]>([]);
  const [payloads, setPayloads] = React.useState<C2Payload[]>([]);
  const [loading, setLoading] = React.useState(true);
  // 生成表单(三期四级联动:产物 → 格式 → 方向 → staged;监听器协议再收窄格式)。
  const [genListener, setGenListener] = React.useState("");
  const [genArtifact, setGenArtifact] = React.useState<C2PayloadArtifact>("script");
  const [genFormat, setGenFormat] = React.useState<C2PayloadFormat>("ps1");
  const [genDirection, setGenDirection] = React.useState<C2Direction>("reverse");
  const [genStaged, setGenStaged] = React.useState(false);
  const [genTarget, setGenTarget] = React.useState<C2PayloadTarget>("windows");
  const [genName, setGenName] = React.useState("");
  const [generating, setGenerating] = React.useState(false);
  // gosrc 的 VPS 构建(二期):空串 = 不编译,只出源码 zip。
  const [machines, setMachines] = React.useState<VpsMachine[]>([]);
  const [genBuildMachine, setGenBuildMachine] = React.useState("");
  // 免杀档案(v3.1,契约 §24A):全量拉一次、前端按当前 format 过滤;空串 = 不使用(直出)。
  const [profiles, setProfiles] = React.useState<C2EvasionProfile[]>([]);
  const [genEvasion, setGenEvasion] = React.useState("");
  // 轮询中的作业种类:vps 构建 / 档案变形(决定进度条与完成 toast 的文案)。
  const [pollJobKind, setPollJobKind] = React.useState<"build" | "evasion">("build");
  // 构建作业轮询:job 未结束时生成按钮保持转圈,结束后刷新列表。
  const [pollJobId, setPollJobId] = React.useState<number | null>(null);
  const [polledJob, setPolledJob] = React.useState<C2Job | null>(null);
  const [viewingJob, setViewingJob] = React.useState<C2Job | null>(null);
  // 列表筛选(客户端搜索 + 服务端 listener_id 过滤)与排序/分页。
  const [filterListener, setFilterListener] = React.useState(() => {
    const v = Number(searchParams.get("listener_id"));
    return Number.isFinite(v) && v > 0 ? String(v) : ALL;
  });
  const [query, setQuery] = React.useState("");
  const [queryApplied, setQueryApplied] = React.useState("");
  const [sortKey, setSortKey] = React.useState<SortKey>("created");
  const [sortDesc, setSortDesc] = React.useState(true);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);
  // 「一行命令」查看对话框。
  const [viewing, setViewing] = React.useState<C2Payload | null>(null);
  // 删除确认。
  const [removing, setRemoving] = React.useState<C2Payload | null>(null);
  const [deleteBusy, setDeleteBusy] = React.useState(false);

  const loadListeners = React.useCallback(() => {
    api
      .c2Listeners()
      .then((r) => setListeners(r.listeners ?? []))
      .catch(() => setListeners([]));
  }, []);

  const load = React.useCallback((lid: string) => {
    setLoading(true);
    api
      .c2Payloads(lid === ALL ? undefined : Number(lid))
      .then((r) => setPayloads(r.payloads ?? []))
      .catch((e) => {
        setPayloads([]);
        toast.error(`加载载荷失败:${(e as Error).message}`);
      })
      .finally(() => setLoading(false));
  }, []);

  React.useEffect(() => {
    loadListeners();
  }, [loadListeners]);

  React.useEffect(() => {
    load(filterListener);
  }, [filterListener, load]);

  // gosrc 第一次选中时拉一次机器列表(构建下拉用)。
  React.useEffect(() => {
    if (genFormat !== "gosrc" || machines.length > 0) return;
    api
      .vpsMachines()
      .then((r) => setMachines(r.machines ?? []))
      .catch(() => setMachines([]));
  }, [genFormat, machines.length]);

  // 免杀档案列表进页拉一次(前端按 format 过滤,后端还会做一致性校验兜底)。
  React.useEffect(() => {
    api
      .c2EvasionProfiles()
      .then((r) => setProfiles(r.profiles ?? []))
      .catch(() => setProfiles([]));
  }, []);

  const reload = React.useCallback(() => load(filterListener), [load, filterListener]);

  // ── 三期矩阵联动(契约 §19 合法组合表 + §18 协议支持矩阵)──
  // 当前选中监听器的传输协议;未选监听器时不按协议收窄。
  const selProto: C2Proto | undefined = React.useMemo(() => {
    const l = listeners.find((x) => x.id === Number(genListener));
    return (l?.proto as C2Proto | undefined) ?? undefined;
  }, [listeners, genListener]);

  // 某产物在当前协议下可用的格式(矩阵 ∩ 协议支持);空数组 = 该产物在当前协议下不可选。
  const formatsForArtifact = React.useCallback(
    (artifact: C2PayloadArtifact): C2PayloadFormat[] => {
      const protoOk = selProto ? C2_PROTO_FORMAT_SUPPORT[selProto] : C2_PAYLOAD_FORMATS;
      return C2_PAYLOAD_MATRIX[artifact].formats.filter((f) => protoOk.includes(f));
    },
    [selProto],
  );

  // 当前表单组合的校验结果(c2ComboAllowed 是组合表的唯一实现,也是最终闸门)。
  const currentCombo = c2ComboAllowed(genFormat, genArtifact, genDirection, genStaged, selProto, genTarget);

  // 产物 / 协议 / 各轴变化时,把表单修正回最近合法组合:每个分支只在当前组合
  // 非法时才 setState(合法组合下全为空操作),所以依赖列全不会与用户选择打架。
  // 协议切换经 formatsForArtifact 的身份变化触发(selProto 是它的 useCallback 依赖)。
  React.useEffect(() => {
    const row = C2_PAYLOAD_MATRIX[genArtifact];
    if (row.directions && !row.directions.includes(genDirection)) setGenDirection(row.directions[0]);
    if (!row.staged && genStaged) setGenStaged(false);
    if (genArtifact === "ebpf") setGenTarget("linux");
    const ok = formatsForArtifact(genArtifact);
    if (ok.length === 0) {
      // 当前协议下该产物整体不可用(如 kcp 下选 exe):自动退到第一个可选产物。
      const alt = C2_PAYLOAD_ARTIFACTS.find((a) => formatsForArtifact(a).length > 0);
      if (alt) setGenArtifact(alt);
      return;
    }
    if (!ok.includes(genFormat)) setGenFormat(ok[0]);
  }, [genArtifact, genDirection, genStaged, genFormat, formatsForArtifact]);

  // ── 免杀档案联动(v3.1,契约 §24A)──
  // 当前 format 下可用的档案(后端会再校验 profile.payload_format 与请求 format 一致)。
  const profileOptions = React.useMemo(
    () => profiles.filter((p) => p.payload_format === genFormat),
    [profiles, genFormat],
  );
  const selProfile = profileOptions.find((p) => String(p.id) === genEvasion) ?? null;
  // 档案变形仅覆盖 script 产物(ps1/bash/py);exe/dll/shellcode/ebpf 编译型产物后端 400,
  // 前端预先禁用提交并提示。
  const profileUsable = genArtifact === "script";

  // 组合联动收尾:切换 format / 产物 / 协议后,已选档案若不再匹配当前格式,自动清回「不使用」。
  React.useEffect(() => {
    if (genEvasion && !profileOptions.some((p) => String(p.id) === genEvasion)) setGenEvasion("");
  }, [genEvasion, profileOptions]);

  // 作业轮询:每 3 秒拉一次,结束( done / error )即停并刷新列表。档案变形作业
  // (v3.1)与 VPS 构建作业共用这条轮询,只按 pollJobKind 区分文案。
  React.useEffect(() => {
    if (!pollJobId) return;
    let alive = true;
    const isEvasion = pollJobKind === "evasion";
    const t = setInterval(() => {
      api
        .c2Job(pollJobId)
        .then((j) => {
          if (!alive) return;
          setPolledJob(j);
          if (j.status === "running" || j.status === "queued") return;
          clearInterval(t);
          if (j.status === "done") {
            toast.success(
              isEvasion
                ? `变形完成(作业 #${j.id}),变形载荷已入列`
                : `VPS 构建完成(作业 #${j.id}),载荷已就绪`,
            );
          } else {
            toast.error(`${isEvasion ? "变形" : "VPS 构建"}失败:${j.error || "未知原因"}`);
          }
          setPollJobId(null);
          reload();
        })
        .catch(() => {});
    }, 3000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, [pollJobId, pollJobKind, reload]);

  const generate = async () => {
    if (!genListener) {
      toast.error("请选择监听器");
      return;
    }
    const l = listeners.find((x) => x.id === Number(genListener));
    if (l && !l.callback) {
      toast.error("该监听器未配置回调地址,请先到「监听器」页补上再生成载荷");
      return;
    }
    // 最终闸门:按 §19 合法组合表 + 协议支持矩阵复检,失败把原因报出来。
    const gate = c2ComboAllowed(genFormat, genArtifact, genDirection, genStaged, selProto, genTarget);
    if (!gate.allowed) {
      toast.error(`非法组合:${gate.reason}`);
      return;
    }
    // 兜底复检(按钮已禁用):档案只配 script 产物,编译型产物后端会 400。
    if (genEvasion && genArtifact !== "script") {
      toast.error("编译型产物暂不支持档案变形,请改选 script 产物或到免杀工作台处理");
      return;
    }
    setGenerating(true);
    try {
      // 三期 v3 扩展字段(artifact/direction/staged)与 v3.1 的 evasion_profile_id
      // 随请求体下发,后端按契约 §19/§24A 接收并 400 兜底。
      const input = {
        listener_id: Number(genListener),
        format: genFormat,
        target: genTarget,
        name: genName.trim() || undefined,
        // gosrc 且选了机器 → 走 VPS 构建作业(异步);否则保持一期直出源码 zip。
        build_vps_machine_id:
          genFormat === "gosrc" && genBuildMachine ? Number(genBuildMachine) : undefined,
        artifact: genArtifact,
        direction: genDirection,
        staged: genStaged,
        // script 产物选了档案 → 后端改走免杀变形作业管线,响应带 job_id(§24A)。
        evasion_profile_id: genEvasion ? Number(genEvasion) : undefined,
      };
      const r = await api.c2GeneratePayload(input);
      if (r.job_id) {
        const evasion = !!genEvasion;
        setPollJobKind(evasion ? "evasion" : "build");
        toast.success(
          evasion
            ? `变形作业 #${r.job_id} 已创建,LLM 正在按档案变形…`
            : `构建作业 #${r.job_id} 已创建,正在 VPS 上编译…`,
        );
        setPolledJob(null);
        setPollJobId(r.job_id);
      } else {
        toast.success(`载荷已生成(#${r.id}),可在列表下载或复制投递命令`);
      }
      setGenName("");
      reload();
    } catch (e) {
      // callback 为空等情况后端回 400,错误原文里带原因。
      toast.error(`生成失败:${(e as Error).message}`);
    } finally {
      setGenerating(false);
    }
  };

  const listenerById = React.useMemo(() => {
    const m = new Map<number, C2Listener>();
    for (const l of listeners) m.set(l.id, l);
    return m;
  }, [listeners]);

  const copyDeliveryURL = async (p: C2Payload) => {
    const url = payloadDeliveryURL(listenerById.get(p.listener_id), p.token);
    if (!url) {
      toast.error("所属监听器缺失或未配置回调地址,无法拼出投递 URL");
      return;
    }
    const ok = await copyText(url);
    if (ok) toast.success(`已复制投递 URL:${url}`);
    else toast.error("复制失败,请手动选择文本复制");
  };

  // ---- 客户端搜索 / 排序 / 分页 ----
  const filtered = React.useMemo(() => {
    const q = queryApplied.trim().toLowerCase();
    if (!q) return payloads;
    return payloads.filter((p) => [p.name, p.format, p.target, p.token, p.sha256].join(" ").toLowerCase().includes(q));
  }, [payloads, queryApplied]);

  const sorted = React.useMemo(() => {
    const arr = [...filtered];
    const dir = sortDesc ? -1 : 1;
    if (sortKey === "name") arr.sort((a, b) => (a.name ?? "").localeCompare(b.name ?? "") * dir);
    else arr.sort((a, b) => (num(a.id) - num(b.id)) * dir);
    return arr;
  }, [filtered, sortKey, sortDesc]);

  const totalPages = Math.max(1, Math.ceil(sorted.length / pageSize));
  const safePage = Math.min(Math.max(1, page), totalPages);
  const pageItems = sorted.slice((safePage - 1) * pageSize, safePage * pageSize);

  const confirmRemove = async () => {
    if (!removing) return;
    setDeleteBusy(true);
    try {
      await api.c2DeletePayload(removing.id);
      toast.success("已删除");
      setRemoving(null);
      reload();
    } catch (e) {
      toast.error(`删除失败:${(e as Error).message}`);
    } finally {
      setDeleteBusy(false);
    }
  };

  const search = () => {
    setPage(1);
    setQueryApplied(query.trim());
  };

  const SortHead = ({ field, width, children }: { field: SortKey; width?: string; children: React.ReactNode }) => (
    <TableHead className={width}>
      <button
        type="button"
        title="排序"
        onClick={() => {
          if (sortKey === field) setSortDesc((d) => !d);
          else {
            setSortKey(field);
            setSortDesc(field !== "name");
          }
        }}
        className="hover:text-foreground inline-flex items-center gap-1"
      >
        {children}
        {sortKey === field ? (
          sortDesc ? (
            <ChevronDownIcon className="size-3" />
          ) : (
            <ChevronUpIcon className="size-3" />
          )
        ) : null}
      </button>
    </TableHead>
  );

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4">
        <h1 className="font-semibold text-xl">C2 中心 · 载荷与免杀</h1>
        <p className="text-muted-foreground text-sm">
          按「产物 → 格式 → 方向 → staged」四级联动生成 beacon 载荷(脚本 / EXE / DLL / Shellcode / eBPF),
          复制一行命令或投递 URL 到目标机执行即可上线;非法组合会禁选并标注原因。
        </p>
      </div>

      {/* ── 生成表单 ── */}
      <Card className="mb-4">
        <CardContent className="pt-4">
          <p className="mb-3 flex items-center gap-1.5 text-sm font-medium">
            <PlusIcon className="text-muted-foreground size-4" />
            生成载荷
          </p>
          <div className="grid gap-3 lg:grid-cols-5">
            <div className="grid gap-1.5 lg:col-span-2">
              <Label>监听器</Label>
              <Select value={genListener} onValueChange={setGenListener}>
                <SelectTrigger className="w-full">
                  <SelectValue placeholder="选择监听器" />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {listeners.length === 0 ? (
                      <SelectItem value="__none__" disabled>
                        还没有监听器,请先到「监听器」页创建
                      </SelectItem>
                    ) : (
                      listeners.map((l) => {
                        const pm = c2ProtoMeta(String(l.proto ?? "http"));
                        return (
                          <SelectItem key={l.id} value={String(l.id)} disabled={!l.callback}>
                            [{pm.label}] {l.name}
                            {l.callback ? `(${l.callback})` : "(未配置回调地址)"}
                          </SelectItem>
                        );
                      })
                    )}
                  </SelectGroup>
                </SelectContent>
              </Select>
              <p className="text-muted-foreground text-[10px]">
                回调地址为空的监听器不可选;选中监听器的协议会收窄可用格式(kcp 仅 py / gosrc)
              </p>
            </div>
            <div className="grid gap-1.5">
              <Label>目标平台</Label>
              <Select
                value={genTarget}
                onValueChange={(v) => setGenTarget(v as C2PayloadTarget)}
                disabled={genArtifact === "ebpf"}
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {TARGETS.map((t) => (
                      <SelectItem key={t} value={t}>
                        {c2PayloadTargetLabel(t)}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
              <p className="text-muted-foreground text-[10px]">
                {genArtifact === "ebpf" ? "eBPF 仅支持 Linux(tc/XDP 触发器)" : "决定载荷脚本的解释器 / 编译目标"}
              </p>
            </div>
            <div className="grid gap-1.5 lg:col-span-2">
              <Label htmlFor="c2-payload-name">名称(可选)</Label>
              <div className="flex items-center gap-2">
                <Input
                  id="c2-payload-name"
                  value={genName}
                  onChange={(e) => setGenName(e.target.value)}
                  placeholder="留空自动命名"
                />
                <Button
                  onClick={() => void generate()}
                  disabled={generating || pollJobId != null || (!!genEvasion && !profileUsable)}
                  title={genEvasion && !profileUsable ? "编译型产物暂不支持档案变形,请改选 script 产物或到免杀工作台处理" : undefined}
                >
                  {pollJobId != null ? (
                    <>
                      <RefreshCwIcon className="size-3.5 animate-spin" />
                      {pollJobKind === "evasion" ? "变形中…" : "构建中…"}
                    </>
                  ) : generating ? (
                    "生成中…"
                  ) : (
                    "生成"
                  )}
                </Button>
              </div>
            </div>
          </div>

          {/* ── 产物(四级联动第一级):五张产物卡,说明写在选项旁 ── */}
          <div className="mt-3 grid gap-1.5">
            <Label>产物形态</Label>
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-5">
              {C2_PAYLOAD_ARTIFACTS.map((a) => {
                const m = c2ArtifactMeta(a);
                const active = genArtifact === a;
                const ok = formatsForArtifact(a);
                // 当前协议下该产物无任何合法格式(如 kcp 下的 exe/dll/shellcode)时禁选。
                const unavailable = ok.length === 0;
                return (
                  <button
                    key={a}
                    type="button"
                    disabled={unavailable}
                    title={
                      unavailable && selProto
                        ? `${c2ProtoMeta(selProto).label} 传输下 ${m.label} 不可用(该协议载荷仅支持 ${C2_PROTO_FORMAT_SUPPORT[selProto].join(" / ")})`
                        : m.desc
                    }
                    onClick={() => setGenArtifact(a)}
                    className={cn(
                      "rounded-md border p-2 text-left transition-colors",
                      active
                        ? "border-primary bg-primary/5 ring-primary/30 ring-1"
                        : "hover:border-primary/40 hover:bg-muted/40",
                      unavailable && "cursor-not-allowed opacity-50",
                    )}
                  >
                    <span className="flex items-center gap-1.5">
                      <m.Icon className={cn("size-3.5", active ? "text-primary" : "text-muted-foreground")} />
                      <span className="text-xs font-medium">{m.label}</span>
                    </span>
                    <span className="text-muted-foreground mt-1 block text-[10px] leading-snug">{m.desc}</span>
                  </button>
                );
              })}
            </div>
          </div>

          {/* ── 格式 / 方向 / staged(四级联动的后三级)── */}
          <div className="mt-3 grid gap-3 lg:grid-cols-5">
            <div className="grid gap-1.5">
              <Label>格式</Label>
              <Select value={genFormat} onValueChange={(v) => setGenFormat(v as C2PayloadFormat)}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {C2_PAYLOAD_FORMATS.map((f) => {
                      const row = C2_PAYLOAD_MATRIX[genArtifact];
                      const inArtifact = row.formats.includes(f);
                      const protoOk = !selProto || C2_PROTO_FORMAT_SUPPORT[selProto].includes(f);
                      const reason = !inArtifact
                        ? c2ComboAllowed(f, genArtifact, genDirection, genStaged).reason
                        : !protoOk
                          ? c2ComboAllowed(f, genArtifact, genDirection, genStaged, selProto).reason
                          : "";
                      return (
                        <SelectItem key={f} value={f} disabled={!inArtifact || !protoOk} title={reason}>
                          {f === "gosrc" ? "gosrc(Go 源码工程)" : f}
                        </SelectItem>
                      );
                    })}
                  </SelectGroup>
                </SelectContent>
              </Select>
              <p className="text-muted-foreground text-[10px]">
                {selProto
                  ? `${c2ProtoMeta(selProto).label} 传输支持:${C2_PROTO_FORMAT_SUPPORT[selProto].join(" / ")}`
                  : "灰选项为当前产物 / 协议不支持的格式,悬停可看原因"}
              </p>
            </div>
            <div className="grid gap-1.5">
              <Label>连接方向</Label>
              <Select value={genDirection} onValueChange={(v) => setGenDirection(v as C2Direction)}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {C2_DIRECTIONS.map((d) => {
                      const dm = c2DirectionMeta(d);
                      const allowed = c2ComboAllowed(genFormat, genArtifact, d, genStaged, selProto, genTarget).allowed;
                      return (
                        <SelectItem
                          key={d}
                          value={d}
                          disabled={!allowed}
                          title={allowed ? dm.desc : c2ComboAllowed(genFormat, genArtifact, d, genStaged, selProto, genTarget).reason}
                        >
                          {dm.label}
                        </SelectItem>
                      );
                    })}
                  </SelectGroup>
                </SelectContent>
              </Select>
              <p className="text-muted-foreground text-[10px]">{c2DirectionMeta(genDirection).desc}</p>
            </div>
            <div className="grid gap-1.5 lg:col-span-2">
              <Label className="flex items-center gap-2">
                Stage 模式(staged)
                <Switch
                  checked={genStaged}
                  disabled={!C2_PAYLOAD_MATRIX[genArtifact].staged}
                  onCheckedChange={setGenStaged}
                  title={C2_PAYLOAD_MATRIX[genArtifact].staged ? "开启后产物为小体积 stager" : C2_PAYLOAD_MATRIX[genArtifact].stagedReason}
                  aria-label="staged 开关"
                />
              </Label>
              {genStaged && genArtifact === "exe" ? (
                <p className="flex items-start gap-1.5 rounded-md border border-red-500/40 bg-red-500/10 px-2.5 py-1.5 text-[11px] text-red-600 dark:text-red-400">
                  <TriangleAlertIcon className="mt-0.5 size-3.5 shrink-0" />
                  staged exe:stager 拉取完整产物后落盘执行,OPSEC 弱——完整产物会落在目标磁盘上;推荐
                  stageless 或 shellcode 反射加载。
                </p>
              ) : genStaged && genArtifact === "shellcode" ? (
                <p className="text-muted-foreground text-[10px]">
                  staged shellcode:sRDI bootstrap 从信道拉取 DLL stage 后反射加载。
                </p>
              ) : !C2_PAYLOAD_MATRIX[genArtifact].staged ? (
                <p className="text-muted-foreground text-[10px]" title={C2_PAYLOAD_MATRIX[genArtifact].stagedReason}>
                  {C2_PAYLOAD_MATRIX[genArtifact].stagedReason}
                </p>
              ) : (
                <p className="text-muted-foreground text-[10px]">关闭 = stageless 整段直出;开启 = 先投小体积 stager</p>
              )}
            </div>
            {!currentCombo.allowed ? (
              <div className="lg:col-span-5">
                <p className="rounded-md border border-amber-500/40 bg-amber-500/10 px-2.5 py-1.5 text-[11px] text-amber-700 dark:text-amber-400">
                  当前组合不可用:{currentCombo.reason}
                </p>
              </div>
            ) : null}
          </div>

          {/* ── 免杀档案(v3.1,契约 §24A):script 产物可选档案走变形作业,编译型产物禁用 ── */}
          <div className="mt-3 grid gap-3 lg:grid-cols-5">
            <div className="grid gap-1.5 lg:col-span-2">
              <Label>免杀档案(可选)</Label>
              <Select
                value={genEvasion}
                onValueChange={(v) => setGenEvasion(v === "__none__" ? "" : v)}
                disabled={!profileUsable}
              >
                <SelectTrigger className="w-full">
                  <SelectValue placeholder="不使用(直出)" />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    <SelectItem value="__none__">不使用(直出)</SelectItem>
                    {!profileUsable ? (
                      <SelectItem value="__no_evasion__" disabled>
                        编译型产物不支持档案变形
                      </SelectItem>
                    ) : profileOptions.length === 0 ? (
                      <SelectItem value="__no_profiles__" disabled>
                        暂无 {genFormat} 格式的档案,可到「免杀工作台」创建
                      </SelectItem>
                    ) : (
                      profileOptions.map((p) => {
                        const src = c2EvasionProfileSourceMeta(String(p.source));
                        const av = c2EvasionAVMeta(String(p.target_av));
                        return (
                          <SelectItem key={p.id} value={String(p.id)} title={p.instructions || p.notes}>
                            [{src.label}] {p.name} · {av.label}
                          </SelectItem>
                        );
                      })
                    )}
                  </SelectGroup>
                </SelectContent>
              </Select>
              {selProfile ? (
                <p className="text-muted-foreground flex flex-wrap items-center gap-1 text-[10px]">
                  <span>生成转为档案变形作业(LLM 变形 + 静态自测),完成后自动入列:</span>
                  <span className="text-foreground font-medium">{selProfile.name}</span>
                  <Badge
                    variant="outline"
                    className={cn("px-1 py-0", c2EvasionProfileSourceMeta(String(selProfile.source)).badge)}
                  >
                    {c2EvasionProfileSourceMeta(String(selProfile.source)).label}
                  </Badge>
                  <Badge
                    variant="outline"
                    className={cn("px-1 py-0", c2EvasionAVMeta(String(selProfile.target_av)).badge)}
                  >
                    {c2EvasionAVMeta(String(selProfile.target_av)).label}
                  </Badge>
                </p>
              ) : (
                <p className="text-muted-foreground text-[10px]">
                  {profileUsable
                    ? "选档案后生成转为 LLM 变形作业,不选则源码直出;选项按当前格式过滤,来源分内置 / 手工 / AI 生成"
                    : "档案变形仅覆盖 ps1 / bash / py 脚本产物,编译型产物请到免杀工作台处理脚本"}
                </p>
              )}
            </div>
            {genEvasion && !profileUsable ? (
              <div className="flex items-end lg:col-span-3">
                <p className="w-full rounded-md border border-amber-500/40 bg-amber-500/10 px-2.5 py-1.5 text-[11px] text-amber-700 dark:text-amber-400">
                  编译型产物暂不支持档案变形,请改选 script 产物或到免杀工作台处理
                </p>
              </div>
            ) : null}
          </div>

          {/* gosrc 专属:可选指派一台 VPS 机器编译(二期,契约 §14)。 */}
          {genFormat === "gosrc" ? (
            <div className="mt-3 grid gap-3 lg:grid-cols-5">
              <div className="grid gap-1.5 lg:col-span-2">
                <Label>在 VPS 上编译(可选)</Label>
                <Select
                  value={genBuildMachine}
                  onValueChange={(v) => setGenBuildMachine(v === "__none__" ? "" : v)}
                >
                  <SelectTrigger className="w-full">
                    <SelectValue placeholder="不编译,只出源码 zip" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      <SelectItem value="__none__">不编译,只出源码 zip</SelectItem>
                      {machines.length === 0 ? (
                        <SelectItem value="__no_machines__" disabled>
                          没有可选机器,请先到「VPS 中心」录入
                        </SelectItem>
                      ) : (
                        machines.map((m) => (
                          <SelectItem key={m.id} value={String(m.id)}>
                            {m.name}({m.host}) · {c2VpsStatusLabel(String(m.status))}
                          </SelectItem>
                        ))
                      )}
                    </SelectGroup>
                  </SelectContent>
                </Select>
                <p className="text-muted-foreground text-[10px]">
                  选机器后生成转为异步构建作业:源码经 SSH 通道在机器上编译,产物自动回传(机器需装 Go
                  {genArtifact === "ebpf" ? ";eBPF 触发器构建还需 clang,缺失时作业明确报错" : ""})
                </p>
              </div>
            </div>
          ) : null}

          {/* 作业进度条(VPS 构建 / 档案变形共用):轮询中每 3 秒刷新。 */}
          {pollJobId != null || polledJob != null ? (
            <div className="mt-3 flex flex-wrap items-center gap-2 rounded-md border px-3 py-2 text-xs">
              <RefreshCwIcon
                className={pollJobId != null ? "size-3.5 animate-spin text-blue-600" : "text-muted-foreground size-3.5"}
              />
              <span className="tabular-nums">
                {pollJobKind === "evasion" ? "变形作业" : "构建作业"} #{pollJobId ?? polledJob?.id}
              </span>
              {polledJob ? (
                <Badge variant="outline" className={c2JobStatusMeta(String(polledJob.status)).badge}>
                  {c2JobStatusMeta(String(polledJob.status)).label}
                </Badge>
              ) : null}
              {polledJob?.status === "error" && polledJob.error ? (
                <span className="min-w-0 flex-1 truncate text-red-600" title={polledJob.error}>
                  {polledJob.error}
                </span>
              ) : (
                <span className="text-muted-foreground">
                  {pollJobKind === "evasion"
                    ? "LLM 按档案变形 → 静态自测 → 载荷自动入列"
                    : "源码上传 → 远程 go build → 产物回传,全程经 SSH 通道"}
                </span>
              )}
              {polledJob && (polledJob.status === "done" || polledJob.status === "error") ? (
                <Button variant="outline" size="xs" onClick={() => setViewingJob(polledJob)}>
                  查看详情
                </Button>
              ) : null}
            </div>
          ) : null}
        </CardContent>
      </Card>

      {/* ── 载荷列表 ── */}
      <Card className="mb-4">
        <CardContent className="px-0 pt-4">
          <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
            <div className="relative w-full sm:max-w-xs">
              <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
              <Input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && search()}
                placeholder="搜索名称 / token / SHA256"
                className="pl-8"
              />
            </div>
            <Button variant="outline" size="sm" onClick={search}>
              搜索
            </Button>
            <Select
              value={filterListener}
              onValueChange={(v) => {
                setPage(1);
                setFilterListener(v);
              }}
            >
              <SelectTrigger className="w-44">
                <SelectValue placeholder="监听器" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value={ALL}>全部监听器</SelectItem>
                  {listeners.map((l) => (
                    <SelectItem key={l.id} value={String(l.id)}>
                      {l.name}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            <Button variant="ghost" size="sm" onClick={reload} title="重新加载">
              <RefreshCwIcon className="size-3.5" />
              刷新
            </Button>
            <span className="text-muted-foreground text-xs">投递前确认监听器已启动</span>
            <span className="text-muted-foreground ml-auto text-xs tabular-nums">
              {queryApplied || filterListener !== ALL ? `命中 ${filtered.length} / ` : ""}共 {payloads.length} 个
            </span>
          </div>

          {loading && payloads.length === 0 ? (
            <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
          ) : pageItems.length === 0 ? (
            <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
              <PackageOpenIcon className="size-8 opacity-40" />
              <span>
                {queryApplied || filterListener !== ALL
                  ? "没有匹配的载荷"
                  : "还没有载荷。用上方表单生成一个:选好监听器与格式,生成后复制一行命令到目标机执行。"}
              </span>
            </div>
          ) : (
            <>
              <Table className="table-fixed">
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    {/* 名称列不设宽度:table-fixed 下自动吃掉余量。
                        固定列合计 624px(96+80+112+80+144+112),常规容器下余量充足。 */}
                    <SortHead field="name">名称</SortHead>
                    <TableHead className="w-24">格式</TableHead>
                    <TableHead className="w-20">目标</TableHead>
                    <TableHead className="w-28">SHA256</TableHead>
                    <TableHead className="w-20">大小</TableHead>
                    <SortHead field="created" width="w-36">
                      创建时间
                    </SortHead>
                    <TableHead className="w-28">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {pageItems.map((p) => {
                    const fm = c2PayloadFormatMeta(String(p.format));
                    const delivery = payloadDeliveryURL(listenerById.get(p.listener_id), p.token);
                    return (
                      <TableRow key={p.id}>
                        <TableCell className="min-w-0">
                          <span className="block w-full truncate font-medium" title={p.name}>
                            {p.name}
                          </span>
                          <span className="text-muted-foreground block truncate text-xs" title={delivery || "投递 URL 不可用"}>
                            {delivery || "投递 URL 不可用(监听器缺失 / 未配回调地址)"}
                          </span>
                        </TableCell>
                        <TableCell>
                          <div className="flex flex-col items-start gap-0.5">
                            <Badge variant="outline" className={fm.badge}>
                              {fm.label}
                            </Badge>
                            {p.artifact && p.artifact !== "script" ? (
                              <Badge
                                variant="outline"
                                className={c2ArtifactMeta(String(p.artifact)).badge}
                                title={c2ArtifactMeta(String(p.artifact)).desc}
                              >
                                {c2ArtifactMeta(String(p.artifact)).label}
                              </Badge>
                            ) : null}
                            {p.direction === "bind" ? (
                              <Badge
                                variant="outline"
                                className={c2DirectionMeta("bind").badge}
                                title={c2DirectionMeta("bind").desc}
                              >
                                {c2DirectionMeta("bind").label}
                              </Badge>
                            ) : null}
                            {p.staged ? (
                              <Badge
                                variant="outline"
                                className="border-orange-500/30 bg-orange-500/15 text-orange-600"
                                title="stage 模式:产物为 stager,拉取完整产物后再执行"
                              >
                                staged
                              </Badge>
                            ) : null}
                            {p.evasion_profile_id > 0 ? (
                              <Badge
                                variant="outline"
                                className="border-amber-500/30 bg-amber-500/15 text-amber-600"
                                title={`经免杀档案 #${p.evasion_profile_id} 变形生成`}
                              >
                                已变形
                              </Badge>
                            ) : null}
                            {p.built_on_vps ? (
                              <Badge
                                variant="outline"
                                className="border-cyan-500/30 bg-cyan-500/15 text-cyan-600"
                                title="已指派 VPS 编译出二进制,下载即可用"
                              >
                                已编译
                              </Badge>
                            ) : null}
                          </div>
                        </TableCell>
                        <TableCell className="text-xs">{c2PayloadTargetLabel(String(p.target))}</TableCell>
                        <TableCell className="min-w-0">
                          <span className="block truncate font-mono text-xs" title={p.sha256}>
                            {p.sha256 ? p.sha256.slice(0, 12) : "—"}
                          </span>
                        </TableCell>
                        <TableCell className="text-muted-foreground text-xs tabular-nums">{fmtBytes(p.size)}</TableCell>
                        <TableCell className="text-muted-foreground text-xs tabular-nums">{fmtTime(p.created_at)}</TableCell>
                        <TableCell>
                          <div className="flex items-center gap-0.5">
                            <Button
                              variant="ghost"
                              size="icon-xs"
                              title="下载载荷"
                              onClick={() => window.open(api.c2PayloadDownloadUrl(p.id))}
                            >
                              <DownloadIcon className="size-3.5" />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon-xs"
                              title="查看一行执行命令"
                              onClick={() => setViewing(p)}
                              disabled={!p.oneliner}
                            >
                              <TerminalIcon className="size-3.5" />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon-xs"
                              title="复制投递 URL"
                              onClick={() => void copyDeliveryURL(p)}
                              disabled={!delivery}
                            >
                              <LinkIcon className="size-3.5" />
                            </Button>
                            <Button variant="ghost" size="icon-xs" onClick={() => setRemoving(p)} title="删除载荷">
                              <Trash2Icon className="text-muted-foreground size-3.5" />
                            </Button>
                          </div>
                        </TableCell>
                      </TableRow>
                    );
                  })}
                </TableBody>
              </Table>
              <TablePagination
                page={safePage}
                pageSize={pageSize}
                total={sorted.length}
                pageSizeOptions={[25, 50, 100, 200]}
                onPageChange={setPage}
                onPageSizeChange={(s) => {
                  setPage(1);
                  setPageSize(s);
                }}
              />
            </>
          )}
        </CardContent>
      </Card>

      {/* ── 免杀工作台指引(二期已上线,变形入口在专属页) ── */}
      <Card>
        <CardContent className="pt-4">
          <div className="flex flex-wrap items-center gap-2">
            <ShieldIcon className="text-muted-foreground size-4" />
            <p className="text-sm font-medium">免杀工作台</p>
            <Link
              href="/function/c2/evasion"
              className="inline-flex items-center gap-1 text-xs text-blue-600 hover:underline"
            >
              打开免杀工作台
              <LinkIcon className="size-3" />
            </Link>
          </div>
          <p className="text-muted-foreground mt-2 text-xs">
            LLM 按免杀档案对脚本载荷做变形混淆(变量重命名、常量改写、字符串拆分),变形后本地静态自测
            (特征串 / 熵值 / 风险分档)。静态自测仅本地特征扫描,任何载荷内容不会发送到本平台 LLM 之外的服务。
            Go 源码载荷(gosrc)可在上方生成时指派 VPS 机器编译,构建作业进度见生成表单下方。
          </p>
        </CardContent>
      </Card>

      {/* ── 作业详情(静态报告 / 构建日志,共用 C2StaticReportView;VPS 构建与档案变形共用) ── */}
      <Dialog open={!!viewingJob} onOpenChange={(o) => !o && setViewingJob(null)}>
        <DialogContent className="max-h-[88vh] overflow-y-auto sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle className="flex flex-wrap items-center gap-2">
              {pollJobKind === "evasion" ? "变形作业" : "构建作业"} #{viewingJob?.id ?? "—"}
              {viewingJob ? (
                <Badge variant="outline" className={c2JobStatusMeta(String(viewingJob.status)).badge}>
                  {c2JobStatusMeta(String(viewingJob.status)).label}
                </Badge>
              ) : null}
            </DialogTitle>
            <DialogDescription>
              {pollJobKind === "evasion"
                ? "档案变形作业的信息与静态自测报告:LLM 按免杀档案变形脚本,自测通过后载荷自动入列。"
                : "VPS 构建作业的信息与日志尾部。构建在指派机器上进行,产物经 SSH 通道回传落盘。"}
            </DialogDescription>
          </DialogHeader>
          {viewingJob ? (
            <div className="grid gap-3">
              {viewingJob.status === "error" && viewingJob.error ? (
                <p className="rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-600 dark:text-red-400">
                  失败原因:{viewingJob.error}
                </p>
              ) : null}
              <div className="text-muted-foreground grid gap-1 text-xs">
                <span>
                  载荷:#{viewingJob.payload_id > 0 ? viewingJob.payload_id : "—"}
                  {pollJobKind === "evasion"
                    ? ""
                    : ` · 构建机器:#${viewingJob.vps_machine_id > 0 ? viewingJob.vps_machine_id : "—"}`}
                </span>
                <span>
                  创建:{fmtTime(viewingJob.created_at)}
                  {viewingJob.done_at ? ` · 完成:${fmtTime(viewingJob.done_at)}` : ""}
                </span>
              </div>
              <C2StaticReportView report={viewingJob.result ?? null} />
            </div>
          ) : (
            <p className="text-muted-foreground py-8 text-center text-xs">加载中…</p>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => setViewingJob(null)}>
              关闭
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* ── 一行命令查看 ── */}
      <Dialog open={!!viewing} onOpenChange={(o) => !o && setViewing(null)}>
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>一行执行命令</DialogTitle>
            <DialogDescription>
              在目标机上执行这条命令,它会拉取载荷源码并就地执行,随后 beacon 按监听器的心跳节奏回连上线。
            </DialogDescription>
          </DialogHeader>
          <Textarea
            readOnly
            rows={4}
            className="min-h-24 font-mono text-xs"
            value={viewing?.oneliner ?? ""}
            onFocus={(e) => e.currentTarget.select()}
          />
          <DialogFooter>
            <CopyButton
              text={viewing?.oneliner ?? ""}
              successMessage="已复制一行命令"
              label="复制一行命令"
            />
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 删除确认 */}
      <AlertDialog open={!!removing} onOpenChange={(o) => !o && setRemoving(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>确认删除载荷</AlertDialogTitle>
            <AlertDialogDescription>
              将删除载荷「{removing?.name}」及其投递令牌:已投递的该载荷将无法再被目标机拉取。此操作不可撤销。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleteBusy}>取消</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault();
                void confirmRemove();
              }}
              disabled={deleteBusy}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {deleteBusy ? "删除中…" : "确认删除"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
