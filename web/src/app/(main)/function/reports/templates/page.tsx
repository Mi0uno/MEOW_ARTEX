"use client";

// 报告模板管理:上传 Word(.docx)/Markdown 模板,精准解析结构;
// 支持分类、检索、分页与增删改查;可 AI 读取模板生成「数据占位版」草稿;
// 导出报告时按模板填充生成。

import * as React from "react";

import {
  CheckIcon,
  ExternalLinkIcon,
  Loader2Icon,
  PencilIcon,
  PlusIcon,
  SearchIcon,
  SparklesIcon,
  Trash2Icon,
  UploadIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
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
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { LLMProfile, ReportTemplate } from "@/lib/types";

const PAGE_SIZE = 20;

// 预设分类;新建/编辑时可自由调整。
const CATEGORIES = ["渗透测试报告", "漏洞扫描报告", "等保测评报告", "安全检查报告", "通用模板"];

function fmtStructure(t: ReportTemplate): string {
  const st = t.structure;
  if (!st) return t.source_note ?? "—";
  const parts: string[] = [];
  if (st.headings?.length) parts.push(`章节 ${st.headings.length}`);
  if (typeof st.paragraphs === "number") parts.push(`段落 ${st.paragraphs}`);
  if (typeof st.tables === "number" && st.tables > 0) parts.push(`表格 ${st.tables}`);
  return parts.join(" · ") || (t.source_note ?? "—");
}

// AnalyzeStatusBadge AI 占位识别状态:上传/手动识别后异步任务在服务端跑,
// 列表 3s 轮询(仅存在 running 行时),状态在这里实时变化。
function AnalyzeStatusBadge({ t }: { t: ReportTemplate }) {
  if (t.analyze_status === "running") {
    return (
      <Badge variant="outline" className="gap-1 text-blue-600">
        <Loader2Icon className="size-3 animate-spin" />
        识别中
      </Badge>
    );
  }
  if (t.analyze_status === "done") {
    return (
      <Badge variant="outline" className="gap-1 text-emerald-600">
        <CheckIcon className="size-3" />
        草稿就绪
      </Badge>
    );
  }
  if (t.analyze_status === "failed") {
    return (
      <Badge variant="outline" className="text-destructive" title={t.analyze_error || "识别失败"}>
        失败
      </Badge>
    );
  }
  return <span className="text-muted-foreground text-xs">—</span>;
}

function CategorySelect({ id, value, onChange }: { id?: string; value: string; onChange: (v: string) => void }) {
  return (
    <Select value={value} onValueChange={onChange}>
      <SelectTrigger id={id} className="w-full">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectGroup>
          {CATEGORIES.map((c) => (
            <SelectItem key={c} value={c}>
              {c}
            </SelectItem>
          ))}
        </SelectGroup>
      </SelectContent>
    </Select>
  );
}

export default function ReportTemplatesPage() {
  const [templates, setTemplates] = React.useState<ReportTemplate[]>([]);
  const [total, setTotal] = React.useState(0);
  const [loaded, setLoaded] = React.useState(false);
  const [category, setCategory] = React.useState<string>("all");
  const [query, setQuery] = React.useState("");
  const [offset, setOffset] = React.useState(0);

  const [uploadOpen, setUploadOpen] = React.useState(false);
  const [editing, setEditing] = React.useState<ReportTemplate | null>(null);

  const queryRef = React.useRef(query);
  queryRef.current = query;
  const categoryRef = React.useRef(category);
  categoryRef.current = category;

  const load = React.useCallback((cat: string, q: string, off: number) => {
    api
      .reportTemplates({ category: cat === "all" ? "" : cat, q, limit: PAGE_SIZE, offset: off })
      .then((r) => {
        setTemplates(r.templates);
        setTotal(r.total);
        setLoaded(true);
      })
      .catch((e) => toast.error(`加载模板失败:${(e as Error).message}`));
  }, []);

  React.useEffect(() => {
    load(category, queryRef.current, offset);
  }, [load, category, offset]);

  // 存在识别中的模板时轮询刷新(异步识别任务在服务端跑,状态落库后这里可见)。
  const hasRunning = templates.some((t) => t.analyze_status === "running");
  React.useEffect(() => {
    if (!hasRunning) return;
    const timer = setInterval(() => load(categoryRef.current, queryRef.current, offset), 3000);
    return () => clearInterval(timer);
  }, [hasRunning, load, offset]);

  const search = () => {
    setOffset(0);
    load(category, query, 0);
  };

  const onSaved = () => load(category, queryRef.current, offset);

  const remove = async (t: ReportTemplate) => {
    if (!window.confirm(`删除模板「${t.name}」?该操作不可恢复。`)) return;
    try {
      await api.deleteReportTemplate(t.id);
      toast.success("模板已删除");
      load(category, queryRef.current, offset);
    } catch (e) {
      toast.error(`删除失败:${(e as Error).message}`);
    }
  };

  return (
    <div className="flex flex-1 flex-col gap-4 p-4 md:p-6">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h1 className="font-semibold text-xl">报告模板</h1>
          <p className="text-muted-foreground mt-0.5 text-sm">
            上传 Word(.docx)/Markdown 模板,系统解析章节结构;导出报告时按模板填充生成。
          </p>
        </div>
        <Button size="sm" onClick={() => setUploadOpen(true)}>
          <PlusIcon className="size-4" />
          上传模板
        </Button>
      </div>

      <div className="rounded-lg border">
        <div className="flex flex-wrap items-center gap-2 border-b p-3">
          <div className="relative">
            <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && search()}
              placeholder="搜索模板名称"
              className="w-56 pl-8"
            />
          </div>
          <Button variant="outline" size="sm" onClick={search}>
            搜索
          </Button>
          <Select
            value={category}
            onValueChange={(v) => {
              setOffset(0);
              setCategory(v);
            }}
          >
            <SelectTrigger className="w-40">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value="all">全部分类</SelectItem>
                {CATEGORIES.map((c) => (
                  <SelectItem key={c} value={c}>
                    {c}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
          <span className="text-muted-foreground ml-auto text-xs tabular-nums">共 {total} 个</span>
        </div>

        {!loaded ? (
          <p className="py-16 text-center text-muted-foreground text-sm">加载中…</p>
        ) : templates.length === 0 ? (
          <p className="py-16 text-center text-muted-foreground text-sm">还没有模板,点右上角「上传模板」开始。</p>
        ) : (
          <>
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="w-36">分类</TableHead>
                  <TableHead>名称</TableHead>
                  <TableHead className="w-20">格式</TableHead>
                  <TableHead>结构</TableHead>
                  <TableHead className="w-28">AI 识别</TableHead>
                  <TableHead className="w-36">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {templates.map((t) => (
                  <TableRow key={t.id}>
                    <TableCell>
                      {t.category ? (
                        <Badge variant="outline" className="text-xs">
                          {t.category}
                        </Badge>
                      ) : (
                        <span className="text-muted-foreground text-xs">未分类</span>
                      )}
                    </TableCell>
                    <TableCell className="max-w-72 truncate font-medium">{t.name}</TableCell>
                    <TableCell className="text-muted-foreground text-xs uppercase">{t.format}</TableCell>
                    <TableCell className="text-muted-foreground text-xs">{fmtStructure(t)}</TableCell>
                    <TableCell>
                      <AnalyzeStatusBadge t={t} />
                    </TableCell>
                    <TableCell>
                      <div className="flex items-center gap-1">
                        <Button variant="ghost" size="sm" onClick={() => setEditing(t)}>
                          <PencilIcon className="size-3.5" />
                          编辑
                        </Button>
                        {!!t.analyze_task_id && (
                          <Button variant="ghost" size="sm" asChild>
                            <a href={`/function/tasks/detail?id=${t.analyze_task_id}`}>
                              <ExternalLinkIcon className="size-3.5" />
                              过程
                            </a>
                          </Button>
                        )}
                        <Button variant="ghost" size="sm" onClick={() => void remove(t)}>
                          <Trash2Icon className="size-3.5 text-muted-foreground" />
                          删除
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
            <div className="flex items-center justify-between border-t p-3 text-muted-foreground text-xs">
              <span>
                第 {offset + 1}-{Math.min(offset + templates.length, total)} 个 / 共 {total} 个
              </span>
              <div className="flex items-center gap-2">
                <Button
                  variant="outline"
                  size="sm"
                  disabled={offset === 0}
                  onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
                >
                  上一页
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={offset + PAGE_SIZE >= total}
                  onClick={() => setOffset(offset + PAGE_SIZE)}
                >
                  下一页
                </Button>
              </div>
            </div>
          </>
        )}
      </div>

      <UploadDialog
        open={uploadOpen}
        onOpenChange={setUploadOpen}
        onUploaded={() => load(categoryRef.current, queryRef.current, offset)}
      />

      <EditTemplateDialog template={editing} onOpenChange={(o) => !o && setEditing(null)} onSaved={onSaved} />
    </div>
  );
}

// UploadDialog 上传新模板:文件 + 名称 + 分类。
function UploadDialog({
  open,
  onOpenChange,
  onUploaded,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onUploaded: () => void;
}) {
  const [file, setFile] = React.useState<File | null>(null);
  const [name, setName] = React.useState("");
  const [category, setCategory] = React.useState(CATEGORIES[0]);
  const [uploading, setUploading] = React.useState(false);
  const fileInputRef = React.useRef<HTMLInputElement>(null);

  const upload = async () => {
    if (!file) {
      toast.error("请先点击上方文件框选择模板文件(.docx / .md / .txt)");
      return;
    }
    setUploading(true);
    try {
      await api.uploadReportTemplate(file, name.trim() || undefined, category);
      toast.success("模板已上传,AI 正在后台自动生成占位草稿(可在 报告中心 → 任务/对话 监看)");
      setFile(null);
      setName("");
      onUploaded();
      onOpenChange(false);
    } catch (e) {
      toast.error(`上传失败:${(e as Error).message}`);
    } finally {
      setUploading(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>上传模板</DialogTitle>
          <DialogDescription>支持 Word(.docx) / Markdown(.md/.txt);系统自动解析章节结构。</DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid gap-1.5">
            <Label>模板文件</Label>
            <button
              type="button"
              onClick={() => fileInputRef.current?.click()}
              className="hover:border-primary/50 flex h-9 min-w-0 items-center gap-2 rounded-md border bg-background px-3 text-left text-xs"
            >
              <UploadIcon className="text-muted-foreground size-4 shrink-0" />
              <span className={"min-w-0 truncate " + (file ? "" : "text-muted-foreground")}>
                {file ? file.name : "点击选择文件(.docx / .md / .txt)"}
              </span>
            </button>
            <input
              ref={fileInputRef}
              type="file"
              accept=".docx,.md,.markdown,.txt"
              className="hidden"
              onChange={(e) => setFile(e.target.files?.[0] ?? null)}
            />
          </div>
          <div className="grid gap-1.5">
            <Label>模板名称</Label>
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="缺省用文件名" />
          </div>
          <div className="grid gap-1.5">
            <Label>分类</Label>
            <CategorySelect value={category} onChange={setCategory} />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button disabled={uploading} onClick={() => void upload()}>
            <UploadIcon className="size-4" />
            {uploading ? "上传解析中…" : "上传"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// EditTemplateDialog 编辑模板:名称/分类/正文 + AI 生成占位模板。
// AI 识别为异步任务:点击后 202 立即返回,这里轮询模板状态直到 done/failed;
// done 后草稿存服务端,点「采用 AI 草稿」填入编辑器,保存由用户手动确认。
function EditTemplateDialog({
  template,
  onOpenChange,
  onSaved,
}: {
  template: ReportTemplate | null;
  onOpenChange: (v: boolean) => void;
  onSaved: () => void;
}) {
  const [name, setName] = React.useState("");
  const [category, setCategory] = React.useState(CATEGORIES[0]);
  const [content, setContent] = React.useState("");
  const [saving, setSaving] = React.useState(false);
  const [analyzing, setAnalyzing] = React.useState(false);
  const [suggestion, setSuggestion] = React.useState("");
  const [llmProfiles, setLlmProfiles] = React.useState<LLMProfile[]>([]);
  const [analyzeProfileId, setAnalyzeProfileId] = React.useState<string>("0");
  const pollRef = React.useRef<ReturnType<typeof setInterval> | null>(null);

  const stopPoll = React.useCallback(() => {
    if (pollRef.current) {
      clearInterval(pollRef.current);
      pollRef.current = null;
    }
  }, []);
  React.useEffect(() => stopPoll, [stopPoll]);

  // startPoll 轮询模板识别状态直到终态;done 时把服务端草稿取回待采用。
  const startPoll = React.useCallback(
    (tid: number) => {
      stopPoll();
      pollRef.current = setInterval(() => {
        api
          .reportTemplate(tid)
          .then((full) => {
            if (full.analyze_status === "running") return;
            stopPoll();
            setAnalyzing(false);
            if (full.analyze_status === "done" && full.suggested_content) {
              setSuggestion(full.suggested_content);
              toast.success("AI 草稿已生成,点「采用 AI 草稿」填入编辑器");
            } else if (full.analyze_status === "failed") {
              toast.error(`AI 识别失败:${full.analyze_error || "未知原因,可重试"}`);
            }
          })
          .catch(() => undefined); // 瞬时轮询失败忽略,下一轮继续
      }, 3000);
    },
    [stopPoll],
  );

  React.useEffect(() => {
    stopPoll();
    setSuggestion("");
    setAnalyzing(false);
    if (!template) return;
    setName(template.name);
    setCategory(template.category || CATEGORIES[0]);
    api
      .reportTemplate(template.id)
      .then((full) => {
        setContent(full.content ?? "");
        if (full.analyze_status === "running") {
          setAnalyzing(true);
          startPoll(template.id);
        } else if (full.analyze_status === "done" && full.suggested_content) {
          setSuggestion(full.suggested_content);
        }
        return api.llmProfiles().then(setLlmProfiles);
      })
      .catch(() => undefined);
  }, [template, startPoll, stopPoll]);

  // 发起 AI 识别:202 后进入轮询;生成在服务端任务里跑,关掉对话框也不中断。
  const analyze = async () => {
    if (!template) return;
    setAnalyzing(true);
    try {
      await api.analyzeReportTemplate(
        template.id,
        analyzeProfileId === "0" ? undefined : Number(analyzeProfileId),
      );
      toast.success("AI 识别任务已启动,可在 报告中心 → 任务/对话 监看过程");
      startPoll(template.id);
    } catch (e) {
      setAnalyzing(false);
      toast.error(`启动 AI 识别失败:${(e as Error).message}`);
    }
  };

  const save = async () => {
    if (!template) return;
    setSaving(true);
    try {
      await api.updateReportTemplate(template.id, {
        name: name.trim() || template.name,
        category,
        content,
      });
      toast.success("模板已保存");
      onSaved();
      onOpenChange(false);
    } catch (e) {
      toast.error(`保存失败:${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open={!!template} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>编辑模板 · {template?.name}</DialogTitle>
          <DialogDescription>
            修改名称、分类与模板正文(下次导出生效);可用变量:
            <code className="rounded bg-muted px-1 font-mono">{"{{content}}"}</code>
            <code className="mx-1 rounded bg-muted px-1 font-mono">{"{{findings_table}}"}</code>
            <code className="mx-1 rounded bg-muted px-1 font-mono">{"{{findings_detail}}"}</code>
            <code className="mx-1 rounded bg-muted px-1 font-mono">{"{{summary}}"}</code> 等。也可让 AI
            自动生成占位草稿。
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid grid-cols-2 gap-3">
            <div className="grid gap-1.5">
              <Label htmlFor="etpl-name">名称</Label>
              <Input id="etpl-name" value={name} onChange={(e) => setName(e.target.value)} />
            </div>
            <div className="grid gap-1.5">
              <Label>分类</Label>
              <CategorySelect value={category} onChange={setCategory} />
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Select value={analyzeProfileId} onValueChange={setAnalyzeProfileId}>
              <SelectTrigger className="h-8 min-w-52 flex-1">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="0">跟随激活的 LLM 配置</SelectItem>
                  {llmProfiles.map((p) => (
                    <SelectItem key={p.id} value={String(p.id)}>
                      {p.name}（{p.model}）
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            <Button variant="outline" size="sm" disabled={analyzing} onClick={() => void analyze()}>
              {analyzing ? <Loader2Icon className="size-4 animate-spin" /> : <SparklesIcon className="size-4" />}
              {analyzing ? "AI 识别中…(后台任务)" : "AI 生成占位模板"}
            </Button>
            {!!suggestion && !analyzing && (
              <Button variant="outline" size="sm" onClick={() => setContent(suggestion)}>
                <CheckIcon className="size-4" />
                采用 AI 草稿
              </Button>
            )}
            {template?.analyze_task_id ? (
              <Button variant="ghost" size="sm" asChild>
                <a href={`/function/tasks/detail?id=${template.analyze_task_id}`}>
                  <ExternalLinkIcon className="size-3.5" />
                  过程
                </a>
              </Button>
            ) : null}
          </div>
          <Textarea
            value={content}
            onChange={(e) => setContent(e.target.value)}
            className="min-h-[46vh] font-mono text-xs"
          />
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button disabled={saving} onClick={() => void save()}>
            {saving ? "保存中…" : "保存"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
