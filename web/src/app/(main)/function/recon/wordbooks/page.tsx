"use client";

// 信息搜集 → 字典库:上传/管理子域字典、路径字典等枚举弹药。
// .txt 每行一条,# 注释;上传后可被 subdomain_brute 收集器选用。

import * as React from "react";
import Link from "next/link";

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { TablePagination } from "@/components/table-pagination";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import {
  ArrowDownIcon,
  ArrowUpIcon,
  DownloadIcon,
  EyeIcon,
  PencilIcon,
  PlusIcon,
  RefreshCwIcon,
  SearchIcon,
  Trash2Icon,
  UploadIcon,
} from "lucide-react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { WORDBOOK_KIND_LABEL, type Wordbook } from "@/lib/types";

function fmtSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1048576) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1048576).toFixed(1)} MB`;
}

function fmtTime(iso?: string): string {
  if (!iso) return "—";
  return new Date(iso).toLocaleString();
}

export default function WordbooksPage() {
  const [books, setBooks] = React.useState<Wordbook[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [uploadOpen, setUploadOpen] = React.useState(false);
  const [uploadFile, setUploadFile] = React.useState<File | null>(null);
  const [uploadName, setUploadName] = React.useState("");
  const [uploadKind, setUploadKind] = React.useState("subdomain");
  const [uploadNote, setUploadNote] = React.useState("");
  const [uploading, setUploading] = React.useState(false);
  const [preview, setPreview] = React.useState<{ name: string; lines: string[] } | null>(null);
  const [editorId, setEditorId] = React.useState<number | null>(null);
  const [editorName, setEditorName] = React.useState("");
  const [editorText, setEditorText] = React.useState("");
  const [editorDirty, setEditorDirty] = React.useState(false);
  const [savingContent, setSavingContent] = React.useState(false);

  const openEditor = async (wb: Wordbook) => {
    setEditorId(wb.id); setEditorName(wb.name); setEditorText("加载中…");
    try {
      const text = await api.wordbookContent(wb.id);
      setEditorText(text);
      setEditorDirty(false);
    } catch (e) { toast.error(`加载内容失败：${(e as Error).message}`); setEditorText(""); }
  };

  const saveContent = async () => {
    if (editorId == null || !editorDirty) return;
    setSavingContent(true);
    try {
      await api.saveWordbookContent(editorId, editorText);
      toast.success("已保存");
      setEditorDirty(false);
      load();
    } catch (e) { toast.error(`保存失败：${(e as Error).message}`); }
    finally { setSavingContent(false); }
  };

  const previewBook = async (wb: Wordbook) => {
    try {
      const r = await api.wordbookPreview(wb.id, 50);
      setPreview({ name: wb.name, lines: r.lines });
    } catch (e) { toast.error(`预览失败：${(e as Error).message}`); }
  };
  const [deleting, setDeleting] = React.useState<number | null>(null);
  // 列表页标准能力:搜索 / 类型筛选 / 排序 / 分页(字典数量不大,客户端处理即可)。
  const [query, setQuery] = React.useState("");
  const [kindFilter, setKindFilter] = React.useState("all");
  const [sortDesc, setSortDesc] = React.useState(true);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(20);

  const load = React.useCallback(() => {
    api.wordbooks().then(setBooks).catch((e) => toast.error(`读取失败：${e.message}`)).finally(() => setLoading(false));
  }, []);

  const filtered = React.useMemo(() => {
    const q = query.trim().toLowerCase();
    const rows = books.filter((wb) => {
      if (kindFilter !== "all" && wb.kind !== kindFilter) return false;
      if (!q) return true;
      return wb.name.toLowerCase().includes(q) || (wb.note ?? "").toLowerCase().includes(q);
    });
    return [...rows].sort((a, b) => {
      const d = a.id - b.id;
      return sortDesc ? -d : d;
    });
  }, [books, query, kindFilter, sortDesc]);

  const paged = React.useMemo(
    () => filtered.slice((page - 1) * pageSize, page * pageSize),
    [filtered, page, pageSize],
  );

  // 筛选/排序变化时回到第一页,避免停在越界页。
  // biome-ignore lint/correctness/useExhaustiveDependencies: filters intentionally reset pagination.
  React.useEffect(() => {
    setPage(1);
  }, [query, kindFilter, sortDesc]);

  React.useEffect(() => { load(); }, [load]);

  const remove = async (id: number) => {
    setDeleting(id);
    try {
      await api.deleteWordbook(id);
      toast.success("已删除");
      load();
    } catch (e) { toast.error(`删除失败：${(e as Error).message}`); }
    finally { setDeleting(null); }
  };

  const doUpload = async () => {
    if (!uploadFile) { toast.error("请选择文件"); return; }
    setUploading(true);
    try {
      await api.uploadWordbook(uploadFile, uploadName, uploadKind, uploadNote);
      toast.success("字典已上传");
      setUploadOpen(false);
      setUploadFile(null); setUploadName(""); setUploadKind("subdomain"); setUploadNote("");
      load();
    } catch (e) { toast.error(`上传失败：${(e as Error).message}`); }
    finally { setUploading(false); }
  };

  const downloadWordbook = async (wb: Wordbook) => {
    try {
      const token = localStorage.getItem("artex_token");
      const r = await fetch(`/api/recon/wordbooks/${wb.id}/download`, {
        headers: { ...(token ? { Authorization: `Bearer ${token}` } : {}) } as Record<string, string>,
      });
      if (!r.ok) throw new Error(String(r.status));
      const blob = await r.blob();
      const a = document.createElement("a");
      a.href = URL.createObjectURL(blob);
      a.download = `${wb.name}.txt`;
      a.click();
      URL.revokeObjectURL(a.href);
    } catch (e) { toast.error(`下载失败：${(e as Error).message}`); }
  };

  return (
    <div className="p-4 md:p-6">
      <div className="mb-2"><SidebarTrigger className="-ml-1" /></div>
      <div className="mb-4 flex items-start justify-between gap-4">
        <div>
          <h1 className="font-semibold text-xl">字典库</h1>
          <p className="text-muted-foreground text-sm">上传子域字典/路径字典等枚举弹药;.txt 每行一条,# 注释</p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm" onClick={() => void load()}><RefreshCwIcon className="size-4" />刷新</Button>
          <Button size="sm" onClick={() => setUploadOpen(true)}>
            <UploadIcon className="size-4" />
            上传字典
          </Button>
        </div>
      </div>
      <Card><CardContent className="px-0 pt-4">
        <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
          <div className="relative w-full sm:max-w-xs">
            <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="搜索字典名称 / 备注"
              className="pl-8"
            />
          </div>
          <Select value={kindFilter} onValueChange={setKindFilter}>
            <SelectTrigger className="w-40">
              <SelectValue placeholder="类型" />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                <SelectItem value="all">全部类型</SelectItem>
                {Object.entries(WORDBOOK_KIND_LABEL).map(([k, label]) => (
                  <SelectItem key={k} value={k}>
                    {label}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
          <Button variant="outline" size="sm" onClick={() => setSortDesc((d) => !d)}>
            {sortDesc ? <ArrowDownIcon className="size-4" /> : <ArrowUpIcon className="size-4" />}
            上传时间
          </Button>
          <span className="text-muted-foreground ml-auto text-xs tabular-nums">
            {filtered.length}/{books.length} 个
          </span>
        </div>
        <Table>
          <TableHeader><TableRow>
            <TableHead>名称</TableHead><TableHead className="w-28">类型</TableHead>
            <TableHead className="w-20 text-right">条数</TableHead><TableHead className="w-20 text-right">大小</TableHead>
            <TableHead className="w-44">上传时间</TableHead><TableHead className="w-32 text-right">操作</TableHead>
          </TableRow></TableHeader>
          <TableBody>
            {loading ? (
              <TableRow><TableCell colSpan={6} className="text-muted-foreground py-8 text-center">加载中…</TableCell></TableRow>
            ) : filtered.length === 0 ? (
              <TableRow><TableCell colSpan={6} className="text-muted-foreground py-8 text-center">
                {books.length === 0 ? "还没有字典,点「上传字典」开始" : "没有匹配的字典"}
              </TableCell></TableRow>
            ) : paged.map((wb) => (
              <TableRow key={wb.id}>
                <TableCell className="font-medium">{wb.name}</TableCell>
                <TableCell><Badge variant="secondary" className="text-xs">{WORDBOOK_KIND_LABEL[wb.kind] ?? wb.kind}</Badge></TableCell>
                <TableCell className="text-right tabular-nums text-sm">{wb.line_count}</TableCell>
                <TableCell className="text-muted-foreground text-right text-xs">{fmtSize(wb.byte_size)}</TableCell>
                <TableCell className="text-muted-foreground text-xs">{fmtTime(wb.created_at)}</TableCell>
                <TableCell><div className="flex items-center justify-end gap-1">
                  <Tooltip><TooltipTrigger asChild>
                    <Button variant="ghost" size="icon" className="size-7" onClick={() => void previewBook(wb)}>
                      <EyeIcon className="size-4" />
                    </Button>
                  </TooltipTrigger><TooltipContent>预览</TooltipContent></Tooltip>
                  <Tooltip><TooltipTrigger asChild>
                    <Button variant="ghost" size="icon" className="size-7" onClick={() => void openEditor(wb)}>
                      <PencilIcon className="size-4" />
                    </Button>
                  </TooltipTrigger><TooltipContent>编辑</TooltipContent></Tooltip>
                  <Tooltip><TooltipTrigger asChild>
                    <Button variant="ghost" size="icon" className="size-7" onClick={() => void downloadWordbook(wb)}>
                      <DownloadIcon className="size-4" />
                    </Button>
                  </TooltipTrigger><TooltipContent>下载</TooltipContent></Tooltip>
                  <AlertDialog>
                    <Tooltip><TooltipTrigger asChild><AlertDialogTrigger asChild>
                      <Button variant="ghost" size="icon" className="text-red-600 size-7 hover:text-red-700">
                        <Trash2Icon className="size-4" />
                      </Button>
                    </AlertDialogTrigger></TooltipTrigger><TooltipContent>删除</TooltipContent></Tooltip>
                    <AlertDialogContent>
                      <AlertDialogHeader><AlertDialogTitle>删除字典 «{wb.name}»?</AlertDialogTitle>
                        <AlertDialogDescription>字典文件将被永久删除,不可撤销。</AlertDialogDescription></AlertDialogHeader>
                      <AlertDialogFooter><AlertDialogCancel>取消</AlertDialogCancel>
                        <AlertDialogAction onClick={() => void remove(wb.id)}>删除</AlertDialogAction></AlertDialogFooter>
                    </AlertDialogContent>
                  </AlertDialog>
                </div></TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        {filtered.length > 0 && (
          <TablePagination
            page={page}
            pageSize={pageSize}
            total={filtered.length}
            onPageChange={setPage}
            onPageSizeChange={(s) => {
              setPage(1);
              setPageSize(s);
            }}
          />
        )}
      </CardContent></Card>

      {/* 上传对话框 */}
      <Dialog open={uploadOpen} onOpenChange={setUploadOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader><DialogTitle>上传字典</DialogTitle></DialogHeader>
          <div className="space-y-3">
            <Label>名称</Label>
            <Input value={uploadName} onChange={(e) => setUploadName(e.target.value)} placeholder="例: 子域_top5000" />
            <Label>类型</Label>
            <Select value={uploadKind} onValueChange={setUploadKind}>
              <SelectTrigger><SelectValue /></SelectTrigger>
              <SelectContent><SelectGroup>
                {Object.entries(WORDBOOK_KIND_LABEL).map(([k, v]) => <SelectItem key={k} value={k}>{v}</SelectItem>)}
              </SelectGroup></SelectContent>
            </Select>
            <Label>字典文件(.txt)</Label>
            <Input type="file" accept=".txt,.dic,.lst" onChange={(e) => setUploadFile(e.target.files?.[0] ?? null)} />
          </div>
          <DialogFooter><Button onClick={doUpload} disabled={uploading || !uploadFile}>
            {uploading ? "上传中…" : "上传"}
          </Button></DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 预览对话框 */}
      {preview && (
        <Dialog open={!!preview} onOpenChange={(o) => !o && setPreview(null)}>
          <DialogContent className="sm:max-w-lg">
            <DialogHeader><DialogTitle>预览 · {preview.name}</DialogTitle></DialogHeader>
            <pre className="max-h-[50vh] overflow-auto rounded-md bg-muted/50 p-3 font-mono text-xs">{preview.lines.join("\n")}</pre>
          </DialogContent>
        </Dialog>
      )}

      {/* 内容编辑器 */}
      <Dialog open={editorId != null} onOpenChange={(o) => { if (!o) { setEditorId(null); setEditorDirty(false); } }}>
        <DialogContent className="sm:max-w-3xl">
          <DialogHeader>
            <DialogTitle>编辑字典 · {editorName}</DialogTitle>
          </DialogHeader>
          <textarea
            className="min-h-[50vh] w-full rounded-md border bg-muted/30 p-3 font-mono text-xs leading-relaxed focus:outline-none focus:ring-1 focus:ring-ring"
            value={editorText}
            onChange={(e) => { setEditorText(e.target.value); setEditorDirty(true); }}
            spellCheck={false}
          />
          <div className="flex items-center justify-between">
            <span className="text-muted-foreground text-xs">
              {editorText.length} 字符 · {editorText.split("\n").length} 行{editorDirty ? " · 未保存" : ""}
            </span>
            <div className="flex gap-2">
              <Button variant="outline" size="sm" onClick={() => { setEditorId(null); setEditorDirty(false); }}>关闭</Button>
              <Button size="sm" onClick={() => void saveContent()} disabled={savingContent || !editorDirty}>
                {savingContent ? "保存中…" : "保存"}
              </Button>
            </div>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}
