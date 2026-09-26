"use client";

// C2 中心 · 免杀工作台(二期,契约 §13)—— LLM 载荷变形 + 本地静态自测 + 作业管线。
//
// 三块:① 档案管理(免杀档案 CRUD:name / target_av / payload_format / instructions / notes);
// ② AI 变形生成(选监听器 + 格式 + 必选档案 + 临时指示 → POST /c2/evasion/generate 建异步作业);
// ③ 作业列表(c2_jobs,running/queued 时 3 秒轮询,详情弹窗渲染静态自测报告)。
//
// 红线:静态自测只在本地做特征串扫描,载荷内容不经过本平台 LLM 之外的任何服务。
// 接口按冻结的 lib/api.ts / lib/types.ts;?status= 深链筛选作业,?job= 深链打开作业详情。

import * as React from "react";

import Link from "next/link";
import { useSearchParams } from "next/navigation";

import {
  ChevronDownIcon,
  ChevronUpIcon,
  PencilIcon,
  PlusIcon,
  RefreshCwIcon,
  RotateCcwIcon,
  SearchIcon,
  ShieldAlertIcon,
  ShieldIcon,
  Trash2Icon,
} from "lucide-react";
import { toast } from "sonner";

import { TablePagination } from "@/components/table-pagination";
import {
  C2_EVASION_AVS,
  C2StaticReportView,
  c2EvasionAVMeta,
  c2EvasionProfileSourceMeta,
  c2JobKindMeta,
  c2JobStatusMeta,
  c2PayloadFormatMeta,
  fmtTime,
  num,
  relTime,
} from "@/components/c2-shared";
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
import { Separator } from "@/components/ui/separator";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type {
  C2EvasionProfile,
  C2EvasionProfileInput,
  C2EvasionTargetAV,
  C2Job,
  C2JobStatus,
  C2Listener,
  C2PayloadFormat,
} from "@/lib/types";

const PAGE_SIZE = 50;

// Radix Select 的 value="" 是保留值,空语义一律用哨兵串。
const ALL = "__all__";
// 免杀变形只吃脚本类格式;gosrc 走载荷页的 VPS 构建路径(契约 §13:静态自测跳过 gosrc)。
const EVASION_FORMATS: C2PayloadFormat[] = ["ps1", "bash", "py"];

type ProfileSortKey = "name" | "updated";
type JobSortKey = "created" | "status";

// useSearchParams 必须挂在 Suspense 边界内(静态导出约束)。
export default function C2EvasionPage() {
  return (
    <React.Suspense fallback={null}>
      <C2EvasionInner />
    </React.Suspense>
  );
}

function C2EvasionInner() {
  const searchParams = useSearchParams();
  // ---- 数据 ----
  const [profiles, setProfiles] = React.useState<C2EvasionProfile[]>([]);
  const [listeners, setListeners] = React.useState<C2Listener[]>([]);
  const [jobs, setJobs] = React.useState<C2Job[]>([]);
  const [loadingProfiles, setLoadingProfiles] = React.useState(true);
  const [loadingJobs, setLoadingJobs] = React.useState(true);
  const loadedOnceRef = React.useRef(false);

  // ---- 档案区状态 ----
  const [profileDialogOpen, setProfileDialogOpen] = React.useState(false);
  const [editingProfile, setEditingProfile] = React.useState<C2EvasionProfile | null>(null);
  const [removingProfile, setRemovingProfile] = React.useState<C2EvasionProfile | null>(null);
  const [deleteBusy, setDeleteBusy] = React.useState(false);
  const [profileQuery, setProfileQuery] = React.useState("");
  const [profileQueryApplied, setProfileQueryApplied] = React.useState("");
  const [profileSortKey, setProfileSortKey] = React.useState<ProfileSortKey>("updated");
  const [profileSortDesc, setProfileSortDesc] = React.useState(true);
  const [profilePage, setProfilePage] = React.useState(1);
  const [profilePageSize, setProfilePageSize] = React.useState(PAGE_SIZE);

  // ---- 生成区状态 ----
  const [genListener, setGenListener] = React.useState("");
  const [genFormat, setGenFormat] = React.useState<C2PayloadFormat>("ps1");
  const [genProfile, setGenProfile] = React.useState("");
  const [genInstructions, setGenInstructions] = React.useState("");
  const [genName, setGenName] = React.useState("");
  const [generating, setGenerating] = React.useState(false);

  // ---- 作业区状态 ----
  // ?status= 深链:服务端筛选;?job= 深链:列表加载后自动打开详情。
  const [jobStatus, setJobStatus] = React.useState(() => {
    const s = searchParams.get("status");
    return ["queued", "running", "done", "error"].includes(s ?? "") ? (s as C2JobStatus) : ALL;
  });
  const [jobKind, setJobKind] = React.useState(ALL);
  const [jobQuery, setJobQuery] = React.useState("");
  const [jobQueryApplied, setJobQueryApplied] = React.useState("");
  const [jobSortKey, setJobSortKey] = React.useState<JobSortKey>("created");
  const [jobSortDesc, setJobSortDesc] = React.useState(true);
  const [jobPage, setJobPage] = React.useState(1);
  const [jobPageSize, setJobPageSize] = React.useState(PAGE_SIZE);
  const [detailJob, setDetailJob] = React.useState<C2Job | null>(null);
  const [retryingId, setRetryingId] = React.useState<number | null>(null);

  const deepJobId = React.useMemo(() => {
    const v = Number(searchParams.get("job"));
    return Number.isFinite(v) && v > 0 ? v : 0;
  }, [searchParams]);

  const loadProfiles = React.useCallback(() => {
    setLoadingProfiles(true);
    api
      .c2EvasionProfiles()
      .then((r) => setProfiles(r.profiles ?? []))
      .catch((e) => {
        setProfiles([]);
        toast.error(`加载免杀档案失败:${(e as Error).message}`);
      })
      .finally(() => setLoadingProfiles(false));
  }, []);

  const loadListeners = React.useCallback(() => {
    api
      .c2Listeners()
      .then((r) => setListeners(r.listeners ?? []))
      .catch(() => setListeners([]));
  }, []);

  const loadJobs = React.useCallback(
    (status: string, kind: string) => {
      setLoadingJobs(true);
      api
        .c2Jobs({
          status: status === ALL ? undefined : (status as C2JobStatus),
          kind: kind === ALL ? undefined : (kind as "evasion" | "build"),
          limit: 200,
        })
        .then((r) => setJobs(r.jobs ?? []))
        .catch((e) => {
          setJobs([]);
          toast.error(`加载作业失败:${(e as Error).message}`);
        })
        .finally(() => setLoadingJobs(false));
    },
    [],
  );

  React.useEffect(() => {
    loadProfiles();
    loadListeners();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  React.useEffect(() => {
    loadJobs(jobStatus, jobKind);
  }, [jobStatus, jobKind, loadJobs]);

  // 首次加载后:?job= 深链自动打开对应作业详情。
  React.useEffect(() => {
    if (loadedOnceRef.current || loadingJobs || deepJobId <= 0) return;
    loadedOnceRef.current = true;
    const hit = jobs.find((j) => j.id === deepJobId);
    if (hit) setDetailJob(hit);
    else if (jobs.length > 0) toast.error(`深链的作业 #${deepJobId} 不在最近列表中`);
  }, [loadingJobs, jobs, deepJobId]);

  // running / queued 作业存在时 3 秒轮询刷新(静默,不闪加载态)。
  const hasActiveJob = jobs.some((j) => j.status === "running" || j.status === "queued");
  React.useEffect(() => {
    if (!hasActiveJob) return;
    const t = setInterval(() => {
      api
        .c2Jobs({
          status: jobStatus === ALL ? undefined : (jobStatus as C2JobStatus),
          kind: jobKind === ALL ? undefined : (jobKind as "evasion" | "build"),
          limit: 200,
        })
        .then((r) => setJobs(r.jobs ?? []))
        .catch(() => {});
    }, 3000);
    return () => clearInterval(t);
  }, [hasActiveJob, jobStatus, jobKind]);

  const reloadJobs = () => loadJobs(jobStatus, jobKind);

  // 详情弹窗开着时同步刷新该作业(轮询带了新数据就用新的)。
  React.useEffect(() => {
    if (!detailJob) return;
    const fresh = jobs.find((j) => j.id === detailJob.id);
    if (fresh && fresh !== detailJob) setDetailJob(fresh);
  }, [jobs, detailJob]);

  const listenerById = React.useMemo(() => {
    const m = new Map<number, C2Listener>();
    for (const l of listeners) m.set(l.id, l);
    return m;
  }, [listeners]);

  const profileById = React.useMemo(() => {
    const m = new Map<number, C2EvasionProfile>();
    for (const p of profiles) m.set(p.id, p);
    return m;
  }, [profiles]);

  const retryJob = async (j: C2Job) => {
    setRetryingId(j.id);
    try {
      const r = await api.c2RetryJob(j.id);
      toast.success(`已重新排队为新作业 #${r.job_id}`);
      reloadJobs();
    } catch (e) {
      toast.error(`重试失败:${(e as Error).message}`);
    } finally {
      setRetryingId(null);
    }
  };

  // ---- 档案区:搜索 / 排序 / 分页 ----
  const profilesFiltered = React.useMemo(() => {
    const q = profileQueryApplied.trim().toLowerCase();
    if (!q) return profiles;
    return profiles.filter((p) =>
      [p.name, p.target_av, p.payload_format, p.source, p.instructions, p.notes].join(" ").toLowerCase().includes(q),
    );
  }, [profiles, profileQueryApplied]);

  const profilesSorted = React.useMemo(() => {
    const arr = [...profilesFiltered];
    const dir = profileSortDesc ? -1 : 1;
    if (profileSortKey === "name") arr.sort((a, b) => (a.name ?? "").localeCompare(b.name ?? "") * dir);
    else arr.sort((a, b) => (num(a.id) - num(b.id)) * dir);
    return arr;
  }, [profilesFiltered, profileSortKey, profileSortDesc]);

  const profileTotalPages = Math.max(1, Math.ceil(profilesSorted.length / profilePageSize));
  const profileSafePage = Math.min(Math.max(1, profilePage), profileTotalPages);
  const profilePageItems = profilesSorted.slice(
    (profileSafePage - 1) * profilePageSize,
    profileSafePage * profilePageSize,
  );

  // ---- 作业区:搜索 / 排序 / 分页 ----
  const jobsFiltered = React.useMemo(() => {
    const q = jobQueryApplied.trim().toLowerCase();
    if (!q) return jobs;
    return jobs.filter((j) => {
      const listenerName = listenerById.get(j.listener_id)?.name ?? "";
      const profileName = profileById.get(j.profile_id)?.name ?? "";
      return [`#${j.id}`, j.kind, j.status, listenerName, profileName, j.error]
        .join(" ")
        .toLowerCase()
        .includes(q);
    });
  }, [jobs, jobQueryApplied, listenerById, profileById]);

  const jobsSorted = React.useMemo(() => {
    const arr = [...jobsFiltered];
    const dir = jobSortDesc ? -1 : 1;
    if (jobSortKey === "status") {
      const rank: Record<string, number> = { running: 0, queued: 1, error: 2, done: 3 };
      arr.sort((a, b) => ((rank[a.status] ?? 9) - (rank[b.status] ?? 9)) * dir);
    } else {
      arr.sort((a, b) => (num(a.id) - num(b.id)) * dir);
    }
    return arr;
  }, [jobsFiltered, jobSortKey, jobSortDesc]);

  const jobTotalPages = Math.max(1, Math.ceil(jobsSorted.length / jobPageSize));
  const jobSafePage = Math.min(Math.max(1, jobPage), jobTotalPages);
  const jobPageItems = jobsSorted.slice((jobSafePage - 1) * jobPageSize, jobSafePage * jobPageSize);

  // ---- 生成 ----
  const generate = async () => {
    if (!genListener) {
      toast.error("请选择监听器");
      return;
    }
    const l = listenerById.get(Number(genListener));
    if (l && !l.callback) {
      toast.error("该监听器未配置回调地址,请先到「监听器」页补上再生成");
      return;
    }
    if (!genProfile) {
      toast.error("免杀生成必须选择档案:请先在上方新建或在列表中选一个");
      return;
    }
    const p = profileById.get(Number(genProfile));
    if (p && p.payload_format !== genFormat) {
      toast.error(`档案「${p.name}」适用格式是 ${p.payload_format},与当前选择的 ${genFormat} 不一致`);
      return;
    }
    setGenerating(true);
    try {
      const r = await api.c2EvasionGenerate({
        listener_id: Number(genListener),
        format: genFormat,
        name: genName.trim() || undefined,
        profile_id: Number(genProfile),
        instructions: genInstructions.trim() || undefined,
      });
      toast.success(`变形作业 #${r.job_id} 已创建,完成后可在「载荷」页下载结果`);
      setGenInstructions("");
      setGenName("");
      reloadJobs();
    } catch (e) {
      // 同档案+监听器已有 running 作业时后端回 409,错误原文里带原因。
      toast.error(`创建失败:${(e as Error).message}`);
    } finally {
      setGenerating(false);
    }
  };

  const confirmRemoveProfile = async () => {
    if (!removingProfile) return;
    setDeleteBusy(true);
    try {
      await api.c2DeleteEvasionProfile(removingProfile.id);
      toast.success("已删除档案");
      setRemovingProfile(null);
      loadProfiles();
    } catch (e) {
      toast.error(`删除失败:${(e as Error).message}`);
    } finally {
      setDeleteBusy(false);
    }
  };

  const ProfileSortHead = ({
    field,
    width,
    children,
  }: {
    field: ProfileSortKey;
    width?: string;
    children: React.ReactNode;
  }) => (
    <TableHead className={width}>
      <button
        type="button"
        title="排序"
        onClick={() => {
          if (profileSortKey === field) setProfileSortDesc((d) => !d);
          else {
            setProfileSortKey(field);
            setProfileSortDesc(field !== "name");
          }
        }}
        className="hover:text-foreground inline-flex items-center gap-1"
      >
        {children}
        {profileSortKey === field ? (
          profileSortDesc ? (
            <ChevronDownIcon className="size-3" />
          ) : (
            <ChevronUpIcon className="size-3" />
          )
        ) : null}
      </button>
    </TableHead>
  );

  const JobSortHead = ({
    field,
    width,
    children,
  }: {
    field: JobSortKey;
    width?: string;
    children: React.ReactNode;
  }) => (
    <TableHead className={width}>
      <button
        type="button"
        title="排序"
        onClick={() => {
          if (jobSortKey === field) setJobSortDesc((d) => !d);
          else {
            setJobSortKey(field);
            setJobSortDesc(true);
          }
        }}
        className="hover:text-foreground inline-flex items-center gap-1"
      >
        {children}
        {jobSortKey === field ? (
          jobSortDesc ? (
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
        <h1 className="font-semibold text-xl">C2 中心 · 免杀工作台</h1>
        <p className="text-muted-foreground text-sm">
          维护免杀档案,LLM 按档案对脚本载荷做变形混淆(变量重命名 / 常量改写 / 字符串拆分),变形后本地静态自测评分。
        </p>
      </div>

      {/* 红线说明 */}
      <div className="mb-4 flex items-start gap-2 rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs">
        <ShieldAlertIcon className="mt-0.5 size-4 shrink-0 text-amber-600" />
        <span className="text-amber-800 dark:text-amber-300">
          静态自测仅本地特征扫描,任何载荷内容不会发送到本平台 LLM 之外的服务。变形产物请仍以本地实测为准——静态分数不是免杀保证。
        </span>
      </div>

      {/* ── 档案管理 ── */}
      <Card className="mb-4">
        <CardContent className="px-0 pt-4">
          <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
            <p className="flex items-center gap-1.5 text-sm font-medium">
              <ShieldIcon className="text-muted-foreground size-4" />
              免杀档案
            </p>
            <Badge variant="outline" className="tabular-nums">
              {profiles.length}
            </Badge>
            <div className="relative w-full sm:max-w-xs">
              <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
              <Input
                value={profileQuery}
                onChange={(e) => setProfileQuery(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") {
                    setProfilePage(1);
                    setProfileQueryApplied(profileQuery.trim());
                  }
                }}
                placeholder="搜索名称 / 目标 AV / 指示 / 备注"
                className="pl-8"
              />
            </div>
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                setProfilePage(1);
                setProfileQueryApplied(profileQuery.trim());
              }}
            >
              搜索
            </Button>
            <Button variant="ghost" size="sm" onClick={loadProfiles} title="重新加载">
              <RefreshCwIcon className="size-3.5" />
              刷新
            </Button>
            <Button
              size="sm"
              className="ml-auto"
              onClick={() => {
                setEditingProfile(null);
                setProfileDialogOpen(true);
              }}
            >
              <PlusIcon className="size-4" />
              新建档案
            </Button>
          </div>

          {loadingProfiles && profiles.length === 0 ? (
            <p className="text-muted-foreground py-12 text-center text-sm">加载中…</p>
          ) : profilePageItems.length === 0 ? (
            <div className="text-muted-foreground flex flex-col items-center gap-2 py-12 text-center text-sm">
              <ShieldIcon className="size-8 opacity-40" />
              <span>
                {profileQueryApplied
                  ? "没有匹配的档案"
                  : "还没有免杀档案。档案告诉 LLM 对着哪个 AV 变形、用什么格式与附加指示;先建一个再生成。"}
              </span>
            </div>
          ) : (
            <>
              <Table className="table-fixed">
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    {/* 名称列不设宽度:table-fixed 下自动吃掉余量。
                        固定列合计 648px(96+80+72+144+128+128),常规容器下余量充足。 */}
                    <ProfileSortHead field="name">名称</ProfileSortHead>
                    <TableHead className="w-24">目标 AV</TableHead>
                    <TableHead className="w-20">格式</TableHead>
                    <TableHead className="w-18">来源</TableHead>
                    <TableHead className="w-36">指示 / 备注</TableHead>
                    <ProfileSortHead field="updated" width="w-32">
                      更新时间
                    </ProfileSortHead>
                    <TableHead className="w-32">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {profilePageItems.map((p) => {
                    const av = c2EvasionAVMeta(String(p.target_av));
                    const fm = c2PayloadFormatMeta(String(p.payload_format));
                    const src = c2EvasionProfileSourceMeta(String(p.source));
                    return (
                      <TableRow key={p.id}>
                        <TableCell className="min-w-0">
                          <span className="block w-full truncate font-medium" title={p.name}>
                            {p.name}
                          </span>
                          {p.instructions ? (
                            <span className="text-muted-foreground block truncate text-xs" title={p.instructions}>
                              {p.instructions}
                            </span>
                          ) : null}
                        </TableCell>
                        <TableCell>
                          <Badge variant="outline" className={av.badge}>
                            {av.label}
                          </Badge>
                        </TableCell>
                        <TableCell>
                          <Badge variant="outline" className={fm.badge}>
                            {fm.label}
                          </Badge>
                        </TableCell>
                        <TableCell>
                          <Badge variant="outline" className={src.badge}>
                            {src.label}
                          </Badge>
                        </TableCell>
                        <TableCell className="min-w-0">
                          <span className="text-muted-foreground block truncate text-xs" title={p.notes}>
                            {p.notes || "—"}
                          </span>
                        </TableCell>
                        <TableCell className="text-muted-foreground text-xs tabular-nums">
                          <span title={fmtTime(p.updated_at)}>{relTime(p.updated_at)}</span>
                        </TableCell>
                        <TableCell>
                          <div className="flex items-center gap-0.5">
                            <Button
                              variant="ghost"
                              size="icon-xs"
                              onClick={() => {
                                setEditingProfile(p);
                                setProfileDialogOpen(true);
                              }}
                              title="编辑档案"
                            >
                              <PencilIcon className="size-3.5" />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon-xs"
                              onClick={() => setRemovingProfile(p)}
                              title="删除档案(不影响已生成的载荷)"
                            >
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
                page={profileSafePage}
                pageSize={profilePageSize}
                total={profilesSorted.length}
                pageSizeOptions={[25, 50, 100, 200]}
                onPageChange={setProfilePage}
                onPageSizeChange={(s) => {
                  setProfilePage(1);
                  setProfilePageSize(s);
                }}
              />
            </>
          )}
        </CardContent>
      </Card>

      {/* ── AI 变形生成 ── */}
      <Card className="mb-4">
        <CardContent className="pt-4">
          <p className="mb-3 flex items-center gap-1.5 text-sm font-medium">
            <PlusIcon className="text-muted-foreground size-4" />
            AI 变形生成
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
                      listeners.map((l) => (
                        <SelectItem key={l.id} value={String(l.id)} disabled={!l.callback}>
                          {l.name}
                          {l.callback ? `(${l.callback})` : "(未配置回调地址,禁选)"}
                        </SelectItem>
                      ))
                    )}
                  </SelectGroup>
                </SelectContent>
              </Select>
              <p className="text-muted-foreground text-[10px]">回调地址为空的监听器禁选:变形产物要写回调地址</p>
            </div>
            <div className="grid gap-1.5">
              <Label>载荷格式</Label>
              <Select value={genFormat} onValueChange={(v) => setGenFormat(v as C2PayloadFormat)}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {EVASION_FORMATS.map((f) => (
                      <SelectItem key={f} value={f}>
                        {c2PayloadFormatMeta(f).label}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
              <p className="text-muted-foreground text-[10px]">gosrc 不走 AI 变形:到「载荷」页选 VPS 构建</p>
            </div>
            <div className="grid gap-1.5 lg:col-span-2">
              <Label>免杀档案(必选)</Label>
              <Select value={genProfile} onValueChange={setGenProfile}>
                <SelectTrigger className="w-full">
                  <SelectValue placeholder="选择档案" />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {profiles.length === 0 ? (
                      <SelectItem value="__none__" disabled>
                        还没有档案,请先在上方「新建档案」
                      </SelectItem>
                    ) : (
                      profiles.map((p) => (
                        <SelectItem key={p.id} value={String(p.id)}>
                          {p.name}({c2EvasionAVMeta(String(p.target_av)).label} · {p.payload_format})
                        </SelectItem>
                      ))
                    )}
                  </SelectGroup>
                </SelectContent>
              </Select>
              <p className="text-muted-foreground text-[10px]">档案决定目标 AV 与技术底子;临时微调用下面的指示</p>
            </div>
          </div>
          <div className="mt-3 grid gap-3 lg:grid-cols-5">
            <div className="grid gap-1.5 lg:col-span-3">
              <Label htmlFor="c2-evasion-instructions">临时指示(可选)</Label>
              <Textarea
                id="c2-evasion-instructions"
                value={genInstructions}
                onChange={(e) => setGenInstructions(e.target.value)}
                rows={2}
                placeholder="本次生成额外追加给 LLM 的指示,例如:避免出现 DownloadString 字面量、变量名伪装成配置项…"
                className="text-xs"
              />
            </div>
            <div className="grid gap-1.5 lg:col-span-2">
              <Label htmlFor="c2-evasion-name">载荷名称(可选)</Label>
              <div className="flex items-center gap-2">
                <Input
                  id="c2-evasion-name"
                  value={genName}
                  onChange={(e) => setGenName(e.target.value)}
                  placeholder="留空自动命名"
                />
                <Button onClick={() => void generate()} disabled={generating}>
                  {generating ? "创建中…" : "创建变形作业"}
                </Button>
              </div>
              <p className="text-muted-foreground text-[10px]">
                生成是异步作业:变形 + 静态自测完成后产物自动落到「载荷」列表
              </p>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* ── 作业列表 ── */}
      <Card>
        <CardContent className="px-0 pt-4">
          <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
            <p className="flex items-center gap-1.5 text-sm font-medium">
              <RefreshCwIcon className="text-muted-foreground size-4" />
              生成作业
            </p>
            {hasActiveJob ? (
              <Badge variant="outline" className="gap-1 border-blue-500/30 bg-blue-500/15 text-blue-600">
                <RefreshCwIcon className="size-3 animate-spin" />
                3 秒自动刷新
              </Badge>
            ) : null}
            <Select
              value={jobKind}
              onValueChange={(v) => {
                setJobPage(1);
                setJobKind(v);
              }}
            >
              <SelectTrigger className="w-32">
                <SelectValue placeholder="类型" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value={ALL}>全部类型</SelectItem>
                  <SelectItem value="evasion">AI 变形</SelectItem>
                  <SelectItem value="build">VPS 构建</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
            <Select
              value={jobStatus}
              onValueChange={(v) => {
                setJobPage(1);
                setJobStatus(v);
              }}
            >
              <SelectTrigger className="w-32">
                <SelectValue placeholder="状态" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value={ALL}>全部状态</SelectItem>
                  <SelectItem value="queued">排队中</SelectItem>
                  <SelectItem value="running">运行中</SelectItem>
                  <SelectItem value="done">完成</SelectItem>
                  <SelectItem value="error">失败</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
            <Button variant="ghost" size="sm" onClick={reloadJobs} title="重新加载">
              <RefreshCwIcon className="size-3.5" />
              刷新
            </Button>
            <span className="text-muted-foreground ml-auto text-xs tabular-nums">共 {jobs.length} 条(最近 200)</span>
          </div>

          {loadingJobs && jobs.length === 0 ? (
            <p className="text-muted-foreground py-12 text-center text-sm">加载中…</p>
          ) : jobPageItems.length === 0 ? (
            <div className="text-muted-foreground flex flex-col items-center gap-2 py-12 text-center text-sm">
              <RefreshCwIcon className="size-8 opacity-40" />
              <span>
                {jobQueryApplied || jobKind !== ALL || jobStatus !== ALL
                  ? "没有匹配的作业"
                  : "还没有作业。用上方「AI 变形生成」创建第一个变形作业;载荷页的 VPS 构建作业也会出现在这里。"}
              </span>
            </div>
          ) : (
            <>
              <Table className="table-fixed">
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    {/* 作业列不设宽度:table-fixed 下自动吃掉余量。
                        固定列合计 688px(64+80+112+96+96+128+112),常规容器下余量充足。 */}
                    <JobSortHead field="created" width="w-16">
                      作业
                    </JobSortHead>
                    <TableHead className="w-20">类型</TableHead>
                    <JobSortHead field="status" width="w-28">
                      状态
                    </JobSortHead>
                    <TableHead className="w-24">监听器</TableHead>
                    <TableHead className="w-24">档案</TableHead>
                    <TableHead className="w-32">创建时间</TableHead>
                    <TableHead className="w-28">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {jobPageItems.map((j) => {
                    const km = c2JobKindMeta(String(j.kind));
                    const sm = c2JobStatusMeta(String(j.status));
                    const l = listenerById.get(j.listener_id);
                    const p = profileById.get(j.profile_id);
                    return (
                      <TableRow key={j.id}>
                        <TableCell className="min-w-0">
                          <span className="block w-full truncate font-medium tabular-nums">#{j.id}</span>
                        </TableCell>
                        <TableCell>
                          <Badge variant="outline" className={km.badge}>
                            {km.label}
                          </Badge>
                        </TableCell>
                        <TableCell>
                          <div className="flex flex-col items-start gap-0.5">
                            <Badge variant="outline" className={sm.badge}>
                              {sm.label}
                            </Badge>
                            {j.status === "error" && j.error ? (
                              <span className="text-red-600 block w-full truncate text-xs" title={j.error}>
                                {j.error}
                              </span>
                            ) : j.status === "done" && j.done_at ? (
                              <span className="text-muted-foreground text-xs" title={fmtTime(j.done_at)}>
                                {relTime(j.done_at)}
                              </span>
                            ) : null}
                          </div>
                        </TableCell>
                        <TableCell className="min-w-0">
                          <span className="block w-full truncate text-xs" title={l ? l.name : `监听器 #${j.listener_id}`}>
                            {l ? l.name : `#${j.listener_id}`}
                          </span>
                        </TableCell>
                        <TableCell className="min-w-0">
                          <span className="block w-full truncate text-xs" title={p ? p.name : j.profile_id > 0 ? `档案 #${j.profile_id}` : "无档案"}>
                            {p ? p.name : j.profile_id > 0 ? `#${j.profile_id}` : "—"}
                          </span>
                        </TableCell>
                        <TableCell className="text-muted-foreground text-xs tabular-nums">{fmtTime(j.created_at)}</TableCell>
                        <TableCell>
                          <div className="flex items-center gap-0.5">
                            <Button variant="ghost" size="icon-xs" onClick={() => setDetailJob(j)} title="查看详情">
                              <SearchIcon className="size-3.5" />
                            </Button>
                            {j.status === "error" ? (
                              <Button
                                variant="ghost"
                                size="icon-xs"
                                onClick={() => void retryJob(j)}
                                disabled={retryingId === j.id}
                                title="重试(重新排队)"
                              >
                                <RotateCcwIcon className={retryingId === j.id ? "size-3.5 animate-spin" : "size-3.5"} />
                              </Button>
                            ) : null}
                            {j.payload_id > 0 ? (
                              <Button
                                variant="ghost"
                                size="icon-xs"
                                asChild
                                title="去载荷页查看产物"
                              >
                                <Link href={`/function/c2/payloads?listener_id=${j.listener_id}`}>
                                  <ShieldIcon className="size-3.5" />
                                </Link>
                              </Button>
                            ) : null}
                          </div>
                        </TableCell>
                      </TableRow>
                    );
                  })}
                </TableBody>
              </Table>
              <TablePagination
                page={jobSafePage}
                pageSize={jobPageSize}
                total={jobsSorted.length}
                pageSizeOptions={[25, 50, 100, 200]}
                onPageChange={setJobPage}
                onPageSizeChange={(s) => {
                  setJobPage(1);
                  setJobPageSize(s);
                }}
              />
            </>
          )}
        </CardContent>
      </Card>

      {/* 档案新建/编辑 */}
      <EvasionProfileDialog
        open={profileDialogOpen}
        onOpenChange={setProfileDialogOpen}
        profile={editingProfile}
        onSaved={() => {
          setProfileDialogOpen(false);
          setEditingProfile(null);
          loadProfiles();
        }}
      />

      {/* 作业详情 */}
      <JobDetailDialog job={detailJob} onClose={() => setDetailJob(null)} listenerName={detailJob ? listenerById.get(detailJob.listener_id)?.name : undefined} profileName={detailJob ? profileById.get(detailJob.profile_id)?.name : undefined} />

      {/* 档案删除确认 */}
      <AlertDialog open={!!removingProfile} onOpenChange={(o) => !o && setRemovingProfile(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>确认删除免杀档案</AlertDialogTitle>
            <AlertDialogDescription>
              将删除档案「{removingProfile?.name}」。已有作业与已生成载荷不受影响,但后续生成不能再选它。此操作不可撤销。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleteBusy}>取消</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault();
                void confirmRemoveProfile();
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

// EvasionProfileDialog 新建 / 编辑免杀档案。
// techniques(技术开关 JSON)由后端按 target_av 给默认,前端不提供编辑面。
function EvasionProfileDialog({
  open,
  onOpenChange,
  profile,
  onSaved,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  profile: C2EvasionProfile | null;
  onSaved: () => void;
}) {
  const [name, setName] = React.useState("");
  const [targetAV, setTargetAV] = React.useState<C2EvasionTargetAV>("defender");
  const [format, setFormat] = React.useState<C2PayloadFormat>("ps1");
  const [instructions, setInstructions] = React.useState("");
  const [notes, setNotes] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    if (!open) return;
    if (profile) {
      setName(profile.name);
      setTargetAV(profile.target_av);
      setFormat(profile.payload_format);
      setInstructions(profile.instructions ?? "");
      setNotes(profile.notes ?? "");
    } else {
      setName("");
      setTargetAV("defender");
      setFormat("ps1");
      setInstructions("");
      setNotes("");
    }
  }, [open, profile]);

  const submit = async () => {
    const n = name.trim();
    if (!n) {
      toast.error("请填写档案名称");
      return;
    }
    setBusy(true);
    try {
      const input: C2EvasionProfileInput = {
        name: n,
        target_av: targetAV,
        payload_format: format,
        instructions: instructions.trim(),
        notes: notes.trim(),
      };
      if (profile) await api.c2UpdateEvasionProfile(profile.id, input);
      else await api.c2CreateEvasionProfile(input);
      toast.success(profile ? "档案已保存" : "档案已创建");
      onSaved();
    } catch (e) {
      toast.error(`保存失败:${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[88vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{profile ? "编辑免杀档案" : "新建免杀档案"}</DialogTitle>
          <DialogDescription>
            档案 = 变形的目标与底子:对着哪个 AV、产出什么格式、附加什么指示。生成时必选一份档案。
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid grid-cols-3 gap-3">
            <div className="col-span-2 grid gap-1.5">
              <Label htmlFor="c2-evasion-profile-name">名称</Label>
              <Input
                id="c2-evasion-profile-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="例如:Defender-PS1-温和变形"
              />
            </div>
            <div className="grid gap-1.5">
              <Label>目标 AV</Label>
              <Select value={targetAV} onValueChange={(v) => setTargetAV(v as C2EvasionTargetAV)}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {C2_EVASION_AVS.map((av) => (
                      <SelectItem key={av} value={av}>
                        {c2EvasionAVMeta(av).label}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </div>
          </div>
          <div className="grid gap-1.5">
            <Label>适用格式</Label>
            <Select value={format} onValueChange={(v) => setFormat(v as C2PayloadFormat)}>
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  {["ps1", "bash", "py", "gosrc"].map((f) => (
                    <SelectItem key={f} value={f}>
                      {c2PayloadFormatMeta(f).label}
                      {f === "gosrc" ? "(gosrc 不参与本地静态自测)" : ""}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="c2-evasion-profile-instructions">附加指示</Label>
            <Textarea
              id="c2-evasion-profile-instructions"
              value={instructions}
              onChange={(e) => setInstructions(e.target.value)}
              rows={4}
              placeholder="喂给 LLM 的变形指示,例如:字符串全部拆分拼接、禁用 IEX 字面量、变量名模仿系统函数…"
              className="text-xs"
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="c2-evasion-profile-notes">备注</Label>
            <Textarea
              id="c2-evasion-profile-notes"
              value={notes}
              onChange={(e) => setNotes(e.target.value)}
              rows={2}
              placeholder="适用场景、实测结论、注意事项…"
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy}>
            取消
          </Button>
          <Button disabled={busy} onClick={() => void submit()}>
            {busy ? "保存中…" : "保存"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// JobDetailDialog 作业详情:状态 / 错误原文 / 静态自测报告(共用 C2StaticReportView)/ 产物跳转。
function JobDetailDialog({
  job,
  onClose,
  listenerName,
  profileName,
}: {
  job: C2Job | null;
  onClose: () => void;
  listenerName?: string;
  profileName?: string;
}) {
  const report = job ? (job.result ?? null) : null;
  return (
    <Dialog
      open={!!job}
      onOpenChange={(o) => {
        if (!o) onClose();
      }}
    >
      <DialogContent className="max-h-[88vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex flex-wrap items-center gap-2">
            作业 #{job?.id ?? "—"}
            {job ? (
              <>
                <Badge variant="outline" className={c2JobKindMeta(String(job.kind)).badge}>
                  {c2JobKindMeta(String(job.kind)).label}
                </Badge>
                <Badge variant="outline" className={c2JobStatusMeta(String(job.status)).badge}>
                  {c2JobStatusMeta(String(job.status)).label}
                </Badge>
              </>
            ) : null}
          </DialogTitle>
          <DialogDescription>
            变形作业的输入、结果与静态自测报告。自测只在本地扫描特征串,分数仅供参考。
          </DialogDescription>
        </DialogHeader>
        {job ? (
          <div className="grid gap-3">
            {job.status === "error" && job.error ? (
              <p className="rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-600 dark:text-red-400">
                失败原因:{job.error}
              </p>
            ) : null}
            <div className="text-muted-foreground grid gap-1 text-xs">
              <span>
                监听器:
                <span className="text-foreground">
                  {listenerName ? listenerName : job.listener_id > 0 ? `#${job.listener_id}` : "—"}
                </span>
                {job.listener_id > 0 ? (
                  <Link
                    href={`/function/c2/listeners`}
                    className="text-blue-600 hover:underline"
                  >
                    (去监听器页)
                  </Link>
                ) : null}
              </span>
              <span>
                档案:
                <span className="text-foreground">
                  {profileName ? profileName : job.profile_id > 0 ? `#${job.profile_id}` : "无"}
                </span>
              </span>
              <span className="flex flex-wrap items-center gap-1">
                载荷产物:
                {job.payload_id > 0 ? (
                  <Link
                    href={`/function/c2/payloads?listener_id=${job.listener_id}`}
                    className="text-blue-600 hover:underline"
                  >
                    去载荷页查看 #{job.payload_id}
                  </Link>
                ) : (
                  <span>作业完成后落盘到载荷列表</span>
                )}
              </span>
              <span>
                创建:{fmtTime(job.created_at)}
                {job.done_at ? ` · 完成:${fmtTime(job.done_at)}` : ""}
              </span>
            </div>
            <Separator />
            {report ? (
              <C2StaticReportView report={report} />
            ) : (
              <p className="text-muted-foreground text-xs">暂无结果数据(作业完成后此处展示静态自测报告)。</p>
            )}
          </div>
        ) : (
          <p className="text-muted-foreground py-8 text-center text-xs">加载中…</p>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            关闭
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
