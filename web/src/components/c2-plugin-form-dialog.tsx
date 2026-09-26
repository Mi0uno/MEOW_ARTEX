"use client";

// C2 插件新建/编辑对话框(契约 §21,插件库页用):name/platform/kind/language/
// entry/content/params_schema(JSON 文本域)/description。编辑走局部更新语义
// (留空字段保持原值);builtin 插件不可编辑,入口在列表页就不给按钮。

import * as React from "react";

import { toast } from "sonner";

import {
  C2_PAYLOAD_TARGET_LABEL,
  c2PluginKindMeta,
  c2PluginLanguageMeta,
} from "@/components/c2-shared";
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
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { C2Plugin, C2PayloadTarget } from "@/lib/types";

const PLATFORMS: C2PayloadTarget[] = ["windows", "linux", "macos", "cross"];
const KINDS = ["script", "binary"] as const;
const LANGUAGES = ["ps1", "sh", "py"] as const;

export function C2PluginFormDialog({
  open,
  onOpenChange,
  editing,
  onSaved,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  /** 编辑目标;null = 新建。 */
  editing: C2Plugin | null;
  onSaved: () => void;
}) {
  const [name, setName] = React.useState("");
  const [platform, setPlatform] = React.useState<C2PayloadTarget>("cross");
  const [kind, setKind] = React.useState<"script" | "binary">("script");
  const [language, setLanguage] = React.useState<"ps1" | "sh" | "py">("sh");
  const [entry, setEntry] = React.useState("");
  const [content, setContent] = React.useState("");
  const [schemaText, setSchemaText] = React.useState("{}");
  const [description, setDescription] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    if (!open) return;
    if (editing) {
      setName(editing.name);
      setPlatform(editing.platform);
      setKind(editing.kind);
      setLanguage(editing.language);
      setEntry(editing.entry);
      setContent(editing.content);
      setDescription(editing.description);
      try {
        setSchemaText(JSON.stringify(editing.params_schema ?? {}, null, 2));
      } catch {
        setSchemaText("{}");
      }
    } else {
      setName("");
      setPlatform("cross");
      setKind("script");
      setLanguage("sh");
      setEntry("");
      setContent("");
      setSchemaText("{}");
      setDescription("");
    }
  }, [open, editing]);

  const submit = async () => {
    if (!name.trim()) {
      toast.error("请填写插件名称");
      return;
    }
    let schema: Record<string, unknown> = {};
    try {
      const parsed = JSON.parse(schemaText || "{}") as unknown;
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
        throw new Error("必须是 JSON 对象");
      }
      schema = parsed as Record<string, unknown>;
    } catch (e) {
      toast.error(`params_schema 不是合法 JSON 对象:${(e as Error).message}`);
      return;
    }
    if (kind === "binary" && !entry.includes("{{file}}")) {
      toast.error("binary 插件的 entry 必须包含 {{file}} 占位(运行时替换为远端落盘路径)");
      return;
    }
    setBusy(true);
    try {
      if (editing) {
        await api.c2UpdatePlugin(editing.id, {
          name: name.trim(),
          platform,
          kind,
          language,
          entry,
          content,
          params_schema: schema,
          description: description.trim(),
        });
        toast.success("插件已更新");
      } else {
        await api.c2CreatePlugin({
          name: name.trim(),
          platform,
          kind,
          language,
          entry,
          content,
          params_schema: schema,
          description: description.trim(),
        });
        toast.success("插件已创建");
      }
      onSaved();
    } catch (e) {
      toast.error(`保存失败:${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[88vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{editing ? `编辑插件:${editing.name}` : "新建插件"}</DialogTitle>
          <DialogDescription>
            script 插件把 entry 模板渲染成命令直走命令通道;binary 插件先把 content 当文件上传到目标临时目录,再执行
            entry({"{{file}}"} = 远端路径)。参数只允许经 {"{{param}}"} 占位注入,后端做白名单校验与注入消毒。
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <div className="col-span-2 grid gap-1.5">
              <Label htmlFor="c2-plugin-name">名称</Label>
              <Input
                id="c2-plugin-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="如:端口存活探测"
              />
            </div>
            <div className="grid gap-1.5">
              <Label>平台</Label>
              <Select value={platform} onValueChange={(v) => setPlatform(v as C2PayloadTarget)}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {PLATFORMS.map((p) => (
                      <SelectItem key={p} value={p}>
                        {C2_PAYLOAD_TARGET_LABEL[p]}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label>语言</Label>
              <Select value={language} onValueChange={(v) => setLanguage(v as (typeof LANGUAGES)[number])}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {LANGUAGES.map((l) => (
                      <SelectItem key={l} value={l}>
                        {c2PluginLanguageMeta(l).label}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </div>
          </div>
          <div className="grid gap-1.5">
            <Label>形态(kind)</Label>
            <Select value={kind} onValueChange={(v) => setKind(v as (typeof KINDS)[number])}>
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  {KINDS.map((k) => (
                    <SelectItem key={k} value={k}>
                      {c2PluginKindMeta(k).label}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            <p className="text-muted-foreground text-[10px]">
              {kind === "binary"
                ? "binary:content 作为文件上传到目标临时目录后执行 entry;entry 必须包含 {{file}} 占位。"
                : "script:entry 模板渲染成命令,经 beacon 命令通道执行;可在 entry 里用 {{content}} 引用下方脚本正文。"}
            </p>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="c2-plugin-entry">入口命令模板(entry)</Label>
            <Textarea
              id="c2-plugin-entry"
              value={entry}
              onChange={(e) => setEntry(e.target.value)}
              rows={2}
              className="font-mono text-xs"
              placeholder={'如:nc -zv {{host}} {{port:22}};verbose 开关写 {{verbose:0|1}}'}
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="c2-plugin-content">{kind === "binary" ? "上传内容(content)" : "脚本正文(content,可选)"}</Label>
            <Textarea
              id="c2-plugin-content"
              value={content}
              onChange={(e) => setContent(e.target.value)}
              rows={5}
              className="font-mono text-xs"
              placeholder={
                kind === "binary"
                  ? "binary 插件要上传的文件内容(文本;更大的二进制请走文件通道另传)"
                  : "脚本正文,可被 entry 里的 {{content}} 占位引用;不引用时仅作存档说明"
              }
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="c2-plugin-schema">参数声明(params_schema,JSON)</Label>
            <Textarea
              id="c2-plugin-schema"
              value={schemaText}
              onChange={(e) => setSchemaText(e.target.value)}
              rows={5}
              className="font-mono text-xs"
              placeholder={'{"host":{"type":"string","desc":"目标主机"},"verbose":{"type":"enum","options":["0","1"],"default":"0"}}'}
            />
            <p className="text-muted-foreground text-[10px]">
              每个参数:{`{type: string|bool|enum, default?, options?, desc?}`};运行页按这里的声明动态渲染表单。
            </p>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="c2-plugin-desc">描述</Label>
            <Input
              id="c2-plugin-desc"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="插件用途与防御视角说明"
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy}>
            取消
          </Button>
          <Button disabled={busy} onClick={() => void submit()}>
            {busy ? "保存中…" : editing ? "保存" : "创建"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
