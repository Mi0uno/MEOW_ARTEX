"use client";

// 技能库列表页。
//
// 改造前这一页是「左树右详情」：左侧技能树、右侧选中技能的配置与文件编辑器。功能都在，
// 但技能一多就没有总览、没法按维度筛、也没法把某个技能单独收藏成链接。现在拆成
// 「列表页（检索 + 分类 + 分页）→ 详情页（文件管理 / 元数据 / 可见性 / 调用记录）」，
// 列表页只负责找到并进入，编辑全部收进详情页。
//
// 分类器维度说明：SkillItem 里没有「来源 / 是否内置」字段（后端只返回 name/description/
// license/compatibility/mcps/files + 调用统计，内置技能也只是同一目录下的普通子目录，
// 前端无法区分），所以按规范改用真实存在的维度 —— 使用状态（calls > 0）。
// 筛选器用同样免费可得的「关联 MCP」，两者都由 api.skills() 单次请求返回的字段算出，
// 不额外发请求（可见性需要逐技能请求，代价高，故只放详情页）。

import * as React from "react";

import Link from "next/link";
import { useRouter } from "next/navigation";

import {
  AlertTriangleIcon,
  ArrowRightIcon,
  ChevronDownIcon,
  ChevronUpIcon,
  PackageIcon,
  PencilIcon,
  PlusIcon,
  SearchIcon,
  Trash2Icon,
  UploadIcon,
} from "lucide-react";
import { toast } from "sonner";

import { TablePagination } from "@/components/table-pagination";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { Agent, MCPServer, MissingSkill, SkillItem } from "@/lib/types";

import { fmtTime, fileCount } from "./_components/format";
import { SkillCreateSheet, SkillDeleteDialog, SkillMetaDialog } from "./_components/skill-dialogs";
import { SkillsOverview } from "./_components/skills-overview";

const PAGE_SIZE = 20;

// 分类维度：使用状态。calls 来自 skill_usage 账本，是这里唯一能区分技能"活性"的真实字段。
const KINDS = [
  { value: "all", label: "全部" },
  { value: "used", label: "已调用" },
  { value: "unused", label: "未使用" },
] as const;
type Kind = (typeof KINDS)[number]["value"];

// 筛选维度：是否声明了 mcps（关联 MCP 的技能加载时会解锁对应工具，行为上确实是一类）。
const MCP_FILTERS = [
  { value: "all", label: "全部" },
  { value: "linked", label: "已关联 MCP" },
  { value: "plain", label: "未关联 MCP" },
] as const;

type SortField = "name" | "files" | "calls" | "last_used";

// compareSkills 定义在组件外：它没有闭包依赖（排序键与方向都从参数进），
// 放模块级既省掉每次渲染重建比较器，也让排序逻辑可以单独读懂。
function compareSkills(a: SkillItem, b: SkillItem, field: SortField, desc: boolean): number {
  const dir = desc ? -1 : 1;
  switch (field) {
    case "files":
      return (fileCount(a) - fileCount(b)) * dir;
    case "calls":
      return (a.calls - b.calls) * dir;
    case "last_used": {
      // 从未调用的排最后：它们没有时间可比较，混在中间会误导。
      const av = a.last_used ?? "";
      const bv = b.last_used ?? "";
      if (!av && !bv) return 0;
      if (!av) return 1;
      if (!bv) return -1;
      return (av < bv ? -1 : 1) * dir;
    }
    default:
      return a.name.localeCompare(b.name) * dir;
  }
}

export default function SkillsPage() {
  const router = useRouter();

  const [skills, setSkills] = React.useState<SkillItem[]>([]);
  const [agents, setAgents] = React.useState<Agent[]>([]);
  const [mcpOptions, setMcpOptions] = React.useState<MCPServer[]>([]);
  const [missing, setMissing] = React.useState<MissingSkill[]>([]);
  const [loading, setLoading] = React.useState(true);

  // 工具栏状态。query 是"已提交"的检索词（点搜索/回车才生效），searchInput 是输入框里的草稿。
  const [searchInput, setSearchInput] = React.useState("");
  const [query, setQuery] = React.useState("");
  const [kind, setKind] = React.useState<Kind>("all");
  const [mcpFilter, setMcpFilter] = React.useState<string>("all");
  const [sort, setSort] = React.useState<SortField>("name");
  const [sortDesc, setSortDesc] = React.useState(false);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);

  // 对话框
  const [createOpen, setCreateOpen] = React.useState(false);
  const [editing, setEditing] = React.useState<SkillItem | null>(null);
  const [pendingDelete, setPendingDelete] = React.useState<string | null>(null);
  const [uploading, setUploading] = React.useState(false);
  const uploadRef = React.useRef<HTMLInputElement>(null);

  const load = React.useCallback(() => {
    setLoading(true);
    api
      .skills()
      .then(setSkills)
      .catch((e) => toast.error(`加载 Skill 失败：${(e as Error).message}`))
      .finally(() => setLoading(false));
    // 这两个是辅助数据（新建对话框的候选项、未命中提示），失败不阻塞列表。
    api.agents().then(setAgents).catch(() => {});
    api.mcpServers().then(setMcpOptions).catch(() => {});
    api.missingSkills().then(setMissing).catch(() => {});
  }, []);

  React.useEffect(() => {
    load();
  }, [load]);

  // ── 检索 / 分类 / 筛选 / 排序 / 分页（全部在客户端完成：api.skills() 一次返回全量）──

  // searched：只应用检索词。分类计数从这里算，选中某一类不会把其他类的计数清零。
  const searched = React.useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return skills;
    return skills.filter(
      (s) => s.name.toLowerCase().includes(q) || (s.description ?? "").toLowerCase().includes(q),
    );
  }, [skills, query]);

  const counts = React.useMemo(() => {
    const used = searched.filter((s) => s.calls > 0).length;
    return { all: searched.length, used, unused: searched.length - used };
  }, [searched]);

  const filtered = React.useMemo(() => {
    let list = searched;
    if (kind === "used") list = list.filter((s) => s.calls > 0);
    else if (kind === "unused") list = list.filter((s) => s.calls === 0);
    if (mcpFilter === "linked") list = list.filter((s) => (s.mcps ?? []).length > 0);
    else if (mcpFilter === "plain") list = list.filter((s) => (s.mcps ?? []).length === 0);
    return list;
  }, [searched, kind, mcpFilter]);

  const sorted = React.useMemo(() => {
    const list = [...filtered];
    list.sort((a, b) => compareSkills(a, b, sort, sortDesc));
    return list;
  }, [filtered, sort, sortDesc]);

  const total = sorted.length;
  const pageItems = React.useMemo(
    () => sorted.slice((page - 1) * pageSize, page * pageSize),
    [sorted, page, pageSize],
  );

  const search = () => {
    setPage(1);
    setQuery(searchInput);
  };

  const toggleSort = (field: SortField) => {
    setPage(1);
    if (sort === field) setSortDesc((d) => !d);
    else {
      setSort(field);
      // 名称默认升序（人找东西按字母），数值/时间默认降序（先看大的/新的）。
      setSortDesc(field !== "name");
    }
  };

  // ── 上传 .zip ─────────────────────────────────────────────────────────────
  async function uploadZip(file: File, overwrite = false) {
    setUploading(true);
    try {
      const r = await api.uploadSkill(file, overwrite);
      toast.success(`已安装 Skill：${r.name}（${r.files} 个文件）`);
      load();
    } catch (e) {
      const msg = (e as Error).message;
      // 同名时征询是否覆盖：后端把"已存在"当作错误返回，这里翻译成一次确认。
      if (!overwrite && msg.includes("已存在")) {
        if (window.confirm(`${msg}\n\n是否覆盖同名 Skill？`)) {
          await uploadZip(file, true);
          return;
        }
      } else {
        toast.error("上传失败：" + msg);
      }
    } finally {
      setUploading(false);
    }
  }

  function onUploadPick(e: React.ChangeEvent<HTMLInputElement>) {
    const f = e.target.files?.[0];
    e.target.value = ""; // reset so picking the same file again re-fires
    if (f) void uploadZip(f);
  }

  async function deleteSkill(name: string) {
    try {
      await api.deleteSkill(name);
      toast.success(`已删除 Skill：${name}`);
      load();
    } catch (e) {
      toast.error("删除失败：" + (e as Error).message);
    }
  }

  const SortHead = ({ field, width, children }: { field: SortField; width?: string; children: React.ReactNode }) => (
    <TableHead className={width}>
      <button
        type="button"
        onClick={() => toggleSort(field)}
        className="hover:text-foreground inline-flex items-center gap-1"
      >
        {children}
        {sort === field ? (
          sortDesc ? (
            <ChevronDownIcon className="size-3" />
          ) : (
            <ChevronUpIcon className="size-3" />
          )
        ) : null}
      </button>
    </TableHead>
  );

  const hasFilter = !!query || kind !== "all" || mcpFilter !== "all";

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <h1 className="font-semibold text-xl">技能库</h1>
          <p className="text-muted-foreground text-sm">
            Skill 是给 agent 的过程性知识（agentskills.io 规范），按 Agent 授权可见。点技能名进入详情页管理文件、元数据与授权。
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {/* 缺口清单：agent 点名调用、但库里没有的 skill —— 直接是该补什么的依据。 */}
          {missing.length > 0 && (
            <Popover>
              <PopoverTrigger asChild>
                <Button size="sm" variant="outline">
                  <AlertTriangleIcon className="size-3.5 text-amber-500" />
                  {missing.length} 个未命中调用
                </Button>
              </PopoverTrigger>
              <PopoverContent align="end" className="w-80">
                <p className="text-muted-foreground mb-2 text-xs">
                  Agent 点名调用、但技能库里不存在的 skill。按被点名次数排序。
                </p>
                <div className="space-y-1">
                  {missing.map((m) => (
                    <div key={m.skill} className="flex items-center gap-2 text-sm">
                      <code className="min-w-0 flex-1 truncate font-mono text-xs" title={m.skill}>
                        {m.skill}
                      </code>
                      <span className="text-muted-foreground shrink-0 text-xs tabular-nums">{m.calls} 次</span>
                      <span className="text-muted-foreground shrink-0 text-xs">{fmtTime(m.last_used)}</span>
                    </div>
                  ))}
                </div>
              </PopoverContent>
            </Popover>
          )}
          <input ref={uploadRef} type="file" accept=".zip,application/zip" className="hidden" onChange={onUploadPick} />
          <Button
            size="sm"
            variant="outline"
            disabled={uploading}
            onClick={() => uploadRef.current?.click()}
            title="上传包含 SKILL.md 的 .zip 压缩包"
          >
            <UploadIcon className="size-4" />
            {uploading ? "上传中…" : "上传压缩包"}
          </Button>
          <Button size="sm" onClick={() => setCreateOpen(true)}>
            <PlusIcon className="size-4" />
            新建 Skill
          </Button>
        </div>
      </div>

      {skills.length > 0 && (
        <SkillsOverview
          skills={skills}
          missing={missing}
          onSelect={(name) => router.push(`/system/skills/detail?name=${encodeURIComponent(name)}`)}
        />
      )}

      <Card>
        <CardContent className="px-0 pt-4">
          <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
            <div className="relative w-full sm:max-w-xs">
              <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
              <Input
                value={searchInput}
                onChange={(e) => setSearchInput(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && search()}
                placeholder="搜索技能名称 / 描述"
                className="pl-8"
              />
            </div>
            <Button variant="outline" size="sm" onClick={search}>
              搜索
            </Button>
            <Select
              value={mcpFilter}
              onValueChange={(v) => {
                setPage(1);
                setMcpFilter(v);
              }}
            >
              <SelectTrigger className="w-36">
                <SelectValue placeholder="MCP 关联" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  {MCP_FILTERS.map((f) => (
                    <SelectItem key={f.value} value={f.value}>
                      {f.label}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            {/* 分类器：可点的 Tabs，计数来自未受分类筛选影响的 searched 集 */}
            <Tabs
              value={kind}
              onValueChange={(v) => {
                setPage(1);
                setKind(v as Kind);
              }}
            >
              <TabsList>
                {KINDS.map((k) => (
                  <TabsTrigger key={k.value} value={k.value}>
                    {k.label} {counts[k.value] ?? 0}
                  </TabsTrigger>
                ))}
              </TabsList>
            </Tabs>
            <span className="text-muted-foreground ml-auto text-xs tabular-nums">
              {query ? `命中 ${total} / ` : ""}共 {total} 个技能
            </span>
          </div>

          {loading && skills.length === 0 ? (
            <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
          ) : skills.length === 0 ? (
            <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
              <PackageIcon className="size-8 opacity-40" />
              <span>还没有 Skill。点「新建 Skill」写一个，或用「上传压缩包」装一个含 SKILL.md 的 .zip。</span>
            </div>
          ) : pageItems.length === 0 ? (
            <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
              <SearchIcon className="size-8 opacity-40" />
              <span>{query ? `没有匹配「${query}」的技能` : "当前筛选条件下没有技能"}</span>
              {hasFilter && (
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => {
                    setSearchInput("");
                    setQuery("");
                    setKind("all");
                    setMcpFilter("all");
                    setPage(1);
                  }}
                >
                  清空筛选
                </Button>
              )}
            </div>
          ) : (
            <>
              <Table className="table-fixed">
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    {/* 技能名列不设宽度：table-fixed 下自动吃掉余量（设 w-full 会挤掉后面的列）。
                        其余固定列之和 640px，明显小于 1280 视口下约 897px 的内容区。 */}
                    <SortHead field="name">技能名称</SortHead>
                    <TableHead className="w-64">描述</TableHead>
                    <SortHead field="files" width="w-20">
                      文件数
                    </SortHead>
                    <SortHead field="calls" width="w-20">
                      调用次数
                    </SortHead>
                    <SortHead field="last_used" width="w-36">
                      最近调用
                    </SortHead>
                    <TableHead className="w-20">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {pageItems.map((s) => (
                    <TableRow key={s.name}>
                      {/* 技能名即入口：点击进入详情页管理文件与授权 */}
                      <TableCell className="min-w-0">
                        <Link
                          href={`/system/skills/detail?name=${encodeURIComponent(s.name)}`}
                          className="block w-full truncate text-left font-medium hover:underline"
                          title={s.name}
                        >
                          {s.name}
                        </Link>
                        {(s.mcps ?? []).length > 0 && (
                          <span className="text-muted-foreground mt-0.5 block truncate text-[11px]" title={`关联 MCP: ${(s.mcps ?? []).join(", ")}`}>
                            MCP: {(s.mcps ?? []).join(", ")}
                          </span>
                        )}
                      </TableCell>
                      <TableCell className="text-muted-foreground max-w-0 text-xs">
                        <span className="block truncate" title={s.description ?? ""}>
                          {s.description || "—"}
                        </span>
                      </TableCell>
                      <TableCell className="tabular-nums">{fileCount(s)}</TableCell>
                      <TableCell className="tabular-nums">
                        {s.calls > 0 ? (
                          <span title={`覆盖 ${s.tasks} 个任务 · 调用方 ${s.usage_agents.join(", ") || "—"}`}>
                            {s.calls}
                          </span>
                        ) : (
                          <span className="text-muted-foreground">0</span>
                        )}
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs">{fmtTime(s.last_used)}</TableCell>
                      <TableCell>
                        <div className="flex items-center gap-0.5">
                          <Button variant="ghost" size="icon-xs" onClick={() => setEditing(s)} title="编辑元数据">
                            <PencilIcon className="size-3.5" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon-xs"
                            onClick={() => setPendingDelete(s.name)}
                            title="删除 Skill（含全部文件与授权）"
                          >
                            <Trash2Icon className="text-muted-foreground size-3.5" />
                          </Button>
                          <Button variant="ghost" size="icon-xs" asChild title="进入详情">
                            <Link href={`/system/skills/detail?name=${encodeURIComponent(s.name)}`} aria-label="进入详情">
                              <ArrowRightIcon className="size-3.5" />
                            </Link>
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
              <TablePagination
                page={page}
                pageSize={pageSize}
                total={total}
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

      <SkillCreateSheet
        open={createOpen}
        onOpenChange={setCreateOpen}
        agents={agents}
        mcpOptions={mcpOptions}
        onCreated={load}
      />

      <SkillMetaDialog
        skill={editing}
        open={!!editing}
        onOpenChange={(o) => {
          if (!o) setEditing(null);
        }}
        mcpOptions={mcpOptions}
        onSaved={load}
      />

      <SkillDeleteDialog
        skillName={pendingDelete ?? ""}
        open={!!pendingDelete}
        onOpenChange={(o) => {
          if (!o) setPendingDelete(null);
        }}
        onConfirm={() => deleteSkill(pendingDelete ?? "")}
      />
    </div>
  );
}
