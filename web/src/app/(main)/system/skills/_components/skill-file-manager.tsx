"use client";

// 技能的文件管理：文件树 + 内容查看/编辑/保存 + 新建文件/文件夹 + 删除。
//
// 这是改造前右侧详情面板的核心能力，原样搬进详情页的「文件」页签。逻辑保持与原页面
// 一致（内联新建输入用 ref 快照避免 onBlur 读到过期 state；目录条目带尾斜杠需要单独
// 建节点），只是从"页面里的一段"变成独立组件，方便详情页与列表页复用同一份实现。

import * as React from "react";

import {
  ChevronRightIcon,
  FilePlusIcon,
  FileTextIcon,
  FolderIcon,
  FolderOpenIcon,
  FolderPlusIcon,
  SaveIcon,
  Trash2Icon,
} from "lucide-react";
import { toast } from "sonner";

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
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import { cn } from "@/lib/utils";

// ── Tree node ──────────────────────────────────────────────────────────────
interface TreeNode {
  name: string;
  path: string; // relative to skill root, dirs WITHOUT trailing slash
  type: "file" | "dir";
  children: TreeNode[];
}

// Backend returns dirs as "scripts/" (trailing slash) and files as "scripts/a.py".
function buildTree(entries: string[]): TreeNode[] {
  const root: TreeNode[] = [];
  const dirMap = new Map<string, TreeNode>();

  function ensureDir(dirPath: string): TreeNode[] {
    if (dirMap.has(dirPath)) return dirMap.get(dirPath)!.children;
    const parts = dirPath.split("/");
    let nodes = root;
    let cur = "";
    for (const part of parts) {
      cur = cur ? `${cur}/${part}` : part;
      if (!dirMap.has(cur)) {
        const d: TreeNode = { name: part, path: cur, type: "dir", children: [] };
        nodes.push(d);
        dirMap.set(cur, d);
      }
      nodes = dirMap.get(cur)!.children;
    }
    return nodes;
  }

  for (const entry of [...entries].sort()) {
    if (entry.endsWith("/")) {
      // Explicit directory entry — ensure the node exists (may already be created)
      ensureDir(entry.slice(0, -1));
    } else {
      // File — ensure parent dirs exist, then push file node
      const parts = entry.split("/");
      let nodes = root;
      let cur = "";
      for (let i = 0; i < parts.length - 1; i++) {
        cur = cur ? `${cur}/${parts[i]}` : parts[i];
        if (!dirMap.has(cur)) {
          const d: TreeNode = { name: parts[i], path: cur, type: "dir", children: [] };
          nodes.push(d);
          dirMap.set(cur, d);
        }
        nodes = dirMap.get(cur)!.children;
      }
      const fname = parts[parts.length - 1];
      if (fname) nodes.push({ name: fname, path: entry, type: "file", children: [] });
    }
  }
  sortNodes(root);
  return root;
}

// sortNodes orders every level like a file explorer: directories before files,
// then case-insensitive by name. Recurses into children.
function sortNodes(nodes: TreeNode[]): void {
  nodes.sort((a, b) => {
    if (a.type !== b.type) return a.type === "dir" ? -1 : 1;
    return a.name.localeCompare(b.name, undefined, { sensitivity: "base" });
  });
  for (const n of nodes) {
    if (n.children.length > 0) sortNodes(n.children);
  }
}

type Creating = { inDir: string; kind: "file" | "dir" } | null;

export function SkillFileManager({
  skill,
  files,
  onChanged,
}: {
  skill: string;
  files: string[];
  onChanged: () => void;
}) {
  const [selectedPath, setSelectedPath] = React.useState<string | null>(null);
  const [expanded, setExpanded] = React.useState<Set<string>>(new Set());
  const [creating, setCreating] = React.useState<Creating>(null);
  const [newEntryName, setNewEntryName] = React.useState("");
  const [pendingDelete, setPendingDelete] = React.useState<{ path: string; kind: "file" | "dir" } | null>(null);
  const [deleting, setDeleting] = React.useState(false);

  // file editor
  const [fileContent, setFileContent] = React.useState("");
  const [fileLoading, setFileLoading] = React.useState(false);
  const [dirty, setDirty] = React.useState(false);
  const [saving, setSaving] = React.useState(false);

  const inlineRef = React.useRef<HTMLInputElement>(null);
  // cancelRef prevents onBlur from committing when Escape was pressed
  const cancelRef = React.useRef(false);

  const tree = React.useMemo(() => buildTree(files), [files]);

  // 换 skill（同一组件被复用）时清掉上一个 skill 的选中与展开，否则会拿旧路径去读新 skill。
  // skill 不在闭包里被读到，所以规则认为它是多余依赖；但这里的重置正是由 skill 变化触发的。
  // biome-ignore lint/correctness/useExhaustiveDependencies: skill 变化本身就是要重置的时机
  React.useEffect(() => {
    setSelectedPath(null);
    setExpanded(new Set());
    setFileContent("");
    setDirty(false);
  }, [skill]);

  React.useEffect(() => {
    if (creating) {
      // Small timeout so the element is actually in the DOM before focusing
      setTimeout(() => inlineRef.current?.focus(), 20);
    }
  }, [creating]);

  React.useEffect(() => {
    if (selectedPath === null) {
      setFileContent("");
      setDirty(false);
      return;
    }
    setFileLoading(true);
    api
      .readSkillFile(skill, selectedPath)
      .then((c) => {
        setFileContent(c);
        setDirty(false);
      })
      .catch(() => toast.error("读取文件失败"))
      .finally(() => setFileLoading(false));
  }, [skill, selectedPath]);

  // ── Expand helpers ────────────────────────────────────────────────────────
  function toggleExpanded(key: string) {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }

  function ensureExpanded(dirPath: string) {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (dirPath) {
        let cur = "";
        for (const part of dirPath.split("/")) {
          cur = cur ? `${cur}/${part}` : part;
          next.add(cur);
        }
      }
      return next;
    });
  }

  // ── Inline create ─────────────────────────────────────────────────────────
  function startCreate(inDir: string, kind: "file" | "dir") {
    cancelRef.current = false;
    ensureExpanded(inDir);
    setCreating({ inDir, kind });
    setNewEntryName("");
  }

  // Use refs so commitCreate reads the latest values without stale closures
  // (it is called from onBlur, which captures the render it was created in).
  const creatingRef = React.useRef<Creating>(null);
  React.useEffect(() => {
    creatingRef.current = creating;
  }, [creating]);
  const newEntryRef = React.useRef("");
  React.useEffect(() => {
    newEntryRef.current = newEntryName;
  }, [newEntryName]);

  async function commitCreate() {
    if (cancelRef.current) {
      cancelRef.current = false;
      return;
    }
    const c = creatingRef.current;
    const name = newEntryRef.current.trim();
    setCreating(null);
    setNewEntryName("");
    if (!c || !name) return;

    const fullPath = c.inDir ? `${c.inDir}/${name}` : name;
    try {
      if (c.kind === "dir") {
        await api.createSkillDir(skill, fullPath);
        toast.success(`已创建文件夹：${fullPath}`);
        ensureExpanded(fullPath);
      } else {
        await api.writeSkillFile(skill, fullPath, "");
        toast.success(`已创建文件：${fullPath}`);
        setSelectedPath(fullPath);
        ensureExpanded(c.inDir);
      }
      onChanged();
    } catch (e) {
      toast.error("创建失败：" + (e as Error).message);
    }
  }

  function cancelCreate() {
    cancelRef.current = true;
    setCreating(null);
    setNewEntryName("");
  }

  async function saveFile() {
    if (selectedPath === null) return;
    setSaving(true);
    try {
      await api.writeSkillFile(skill, selectedPath, fileContent);
      toast.success("已保存");
      setDirty(false);
    } catch (e) {
      toast.error("保存失败：" + (e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  async function runDelete() {
    const p = pendingDelete;
    if (!p) return;
    setDeleting(true);
    try {
      await api.deleteSkillPath(skill, p.path);
      toast.success(`已删除：${p.path}`);
      if (selectedPath === p.path) setSelectedPath(null);
      onChanged();
    } catch (e) {
      toast.error("删除失败：" + (e as Error).message);
    } finally {
      setDeleting(false);
      setPendingDelete(null);
    }
  }

  // ── Inline input JSX helper (NOT a React component — avoids remount on re-render) ──
  function inlineInputJSX(indent: number) {
    return (
      <div key="__inline_create__" className="flex items-center gap-1 py-0.5 pr-2" style={{ paddingLeft: indent }}>
        {creating?.kind === "dir" ? (
          <FolderIcon className="size-3.5 shrink-0 text-amber-500" />
        ) : (
          <FileTextIcon className="text-muted-foreground size-3.5 shrink-0" />
        )}
        <Input
          ref={inlineRef}
          className="h-6 flex-1 px-1 py-0 font-mono text-xs"
          placeholder={creating?.kind === "dir" ? "folder-name" : "filename.py"}
          value={newEntryName}
          onChange={(e) => setNewEntryName(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              cancelRef.current = false;
              void commitCreate();
            }
            if (e.key === "Escape") cancelCreate();
          }}
          onBlur={() => {
            void commitCreate();
          }}
        />
      </div>
    );
  }

  // ── Recursive tree renderer ───────────────────────────────────────────────
  function renderTree(nodes: TreeNode[], depth: number): React.ReactNode {
    // depth+1: the skill root sits at 8px (px-2); its children indent one step
    // further in so the tree reads as nested under the skill folder.
    const baseIndent = 8 + (depth + 1) * 14;
    return nodes.map((node) => {
      if (node.type === "dir") {
        const open = expanded.has(node.path);
        return (
          <div key={node.path}>
            <div
              className="group hover:bg-muted relative flex cursor-pointer items-center gap-1 rounded py-0.5 pr-1 text-sm select-none"
              style={{ paddingLeft: baseIndent }}
              onClick={() => toggleExpanded(node.path)}
            >
              <ChevronRightIcon className={cn("size-3.5 shrink-0 transition-transform", open && "rotate-90")} />
              {open ? (
                <FolderOpenIcon className="size-3.5 shrink-0 text-amber-500" />
              ) : (
                <FolderIcon className="size-3.5 shrink-0 text-amber-500" />
              )}
              <span className="min-w-0 flex-1 truncate" title={node.path}>
                {node.name}
              </span>
              {/* Absolute so a long name can never push the actions out of view */}
              <span className="bg-muted absolute inset-y-0 right-1 hidden items-center gap-0.5 rounded pl-1 group-hover:flex">
                <Button
                  size="icon"
                  variant="ghost"
                  className="size-5"
                  title="新建文件"
                  onClick={(e) => {
                    e.stopPropagation();
                    startCreate(node.path, "file");
                  }}
                >
                  <FilePlusIcon className="text-muted-foreground size-3" />
                </Button>
                <Button
                  size="icon"
                  variant="ghost"
                  className="size-5"
                  title="新建文件夹"
                  onClick={(e) => {
                    e.stopPropagation();
                    startCreate(node.path, "dir");
                  }}
                >
                  <FolderPlusIcon className="text-muted-foreground size-3" />
                </Button>
                <Button
                  size="icon"
                  variant="ghost"
                  className="size-5"
                  title="删除文件夹"
                  onClick={(e) => {
                    e.stopPropagation();
                    setPendingDelete({ path: node.path, kind: "dir" });
                  }}
                >
                  <Trash2Icon className="text-destructive size-3" />
                </Button>
              </span>
            </div>
            {open && (
              <>
                {renderTree(node.children, depth + 1)}
                {creating?.inDir === node.path && inlineInputJSX(baseIndent + 14)}
              </>
            )}
          </div>
        );
      }

      // file node
      const isSelected = selectedPath === node.path;
      return (
        <div
          key={node.path}
          className={cn(
            "group relative flex cursor-pointer items-center gap-1 rounded py-0.5 pr-1 text-sm select-none",
            isSelected ? "bg-accent text-accent-foreground" : "hover:bg-muted",
          )}
          style={{ paddingLeft: baseIndent + 16 }}
          onClick={() => setSelectedPath(node.path)}
        >
          <FileTextIcon className="text-muted-foreground size-3.5 shrink-0" />
          <span className="min-w-0 flex-1 truncate font-mono text-xs" title={node.path}>
            {node.name}
          </span>
          <span
            className={cn(
              "absolute inset-y-0 right-1 hidden items-center rounded pl-1 group-hover:flex",
              isSelected ? "bg-accent" : "bg-muted",
            )}
          >
            <Button
              size="icon"
              variant="ghost"
              className="size-5"
              title="删除文件"
              onClick={(e) => {
                e.stopPropagation();
                setPendingDelete({ path: node.path, kind: "file" });
              }}
            >
              <Trash2Icon className="text-destructive size-3" />
            </Button>
          </span>
        </div>
      );
    });
  }

  return (
    <div className="flex h-[68vh] min-h-[26rem] gap-4">
      {/* ── 左：文件树 ── */}
      <div className="flex w-72 shrink-0 flex-col rounded-lg border">
        <div className="flex items-center gap-1 border-b p-2">
          <span className="min-w-0 flex-1 truncate font-mono text-xs font-medium" title={skill}>
            {skill}
          </span>
          <Button size="icon" variant="ghost" className="size-6" title="在根目录新建文件" onClick={() => startCreate("", "file")}>
            <FilePlusIcon className="size-3.5" />
          </Button>
          <Button size="icon" variant="ghost" className="size-6" title="在根目录新建文件夹" onClick={() => startCreate("", "dir")}>
            <FolderPlusIcon className="size-3.5" />
          </Button>
        </div>
        {/* Radix viewport wraps children in a display:table div that grows with
            content — force it to block so long names truncate instead of
            widening the rows past the sidebar. */}
        <ScrollArea className="flex-1 [&>[data-slot=scroll-area-viewport]>div]:!block">
          <div className="p-1">
            {files.length === 0 ? (
              <p className="text-muted-foreground p-2 text-xs">该 Skill 目录为空。</p>
            ) : (
              <>
                {renderTree(tree, 0)}
                {creating?.inDir === "" && inlineInputJSX(22)}
              </>
            )}
          </div>
        </ScrollArea>
      </div>

      {/* ── 右：文件内容 ── */}
      <div className="flex min-w-0 flex-1 flex-col rounded-lg border">
        {selectedPath === null ? (
          <div className="text-muted-foreground flex flex-1 items-center justify-center p-6 text-center text-sm">
            从左侧选择一个文件查看/编辑，或用左上角的按钮新建文件、文件夹。
          </div>
        ) : (
          <>
            <div className="flex items-center gap-2 border-b px-3 py-2">
              <span className="text-muted-foreground min-w-0 flex-1 truncate font-mono text-xs" title={selectedPath}>
                {selectedPath}
              </span>
              {dirty && <span className="shrink-0 text-[11px] text-amber-600">未保存</span>}
              <Button size="sm" onClick={() => void saveFile()} disabled={!dirty || saving}>
                <SaveIcon className="size-3.5" />
                {saving ? "保存中…" : "保存"}
              </Button>
            </div>
            {fileLoading ? (
              <p className="text-muted-foreground p-4 text-xs">加载中…</p>
            ) : (
              <Textarea
                className="max-h-none min-h-0 flex-1 resize-none rounded-none border-0 font-mono text-xs focus-visible:ring-0"
                value={fileContent}
                onChange={(e) => {
                  setFileContent(e.target.value);
                  setDirty(true);
                }}
              />
            )}
          </>
        )}
      </div>

      {/* 删除确认：这里用 window.confirm 而不是 AlertDialog —— 页面内已有若干对话框，
          再叠一层 Radix 弹层会让焦点管理变复杂；文件/文件夹删除的影响面一句话说得清。 */}
      {pendingDelete !== null && (
        <DeleteConfirm
          path={pendingDelete.path}
          kind={pendingDelete.kind}
          busy={deleting}
          onCancel={() => setPendingDelete(null)}
          onConfirm={() => void runDelete()}
        />
      )}
    </div>
  );
}

// DeleteConfirm 是 SkillFileManager 的受控确认层。用 AlertDialog 以便与列表页的删除
// 确认保持同一种交互（都展示影响面），并在删除中禁用按钮防止重复提交。
function DeleteConfirm({
  path,
  kind,
  busy,
  onCancel,
  onConfirm,
}: {
  path: string;
  kind: "file" | "dir";
  busy: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  return (
    <AlertDialog open onOpenChange={(o) => { if (!o) onCancel(); }}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{kind === "dir" ? `删除文件夹「${path}」？` : `删除文件「${path}」？`}</AlertDialogTitle>
          <AlertDialogDescription>
            {kind === "dir" ? "将一并删除该文件夹下的所有文件。此操作不可撤销。" : "此操作不可撤销。"}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>取消</AlertDialogCancel>
          <AlertDialogAction
            disabled={busy}
            onClick={(e) => {
              e.preventDefault();
              onConfirm();
            }}
          >
            {busy ? "删除中…" : "删除"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
