"use client";

// 规范与文件页签:平台级规范的四类文本域 + 每类的附件。
//
// 为什么文本域与附件放在同一屏:两者最终会拼成同一份注入文本(db.SRCPlatformSpecText),
// 用户在这里写的每一行都会变成 AI 的硬约束。分成两处编辑,用户很难意识到
// 「附件没解析成功 = 这段规范其实没生效」——所以解析状态必须在文件旁边直接可见。

import * as React from "react";

import { ChevronDownIcon, ChevronRightIcon, DownloadIcon, FileTextIcon, Trash2Icon, UploadIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Separator } from "@/components/ui/separator";
import { api } from "@/lib/api";
import { SRC_PLATFORM_FILE_KINDS, type SRCPlatform, type SRCPlatformFile, type SRCPlatformFileKind } from "@/lib/types";

import { fmtBytes, ParseStatusBadge, parseStatusMeta, SPEC_FIELDS, SpecTextarea } from "./platform-shared";

// PREVIEW_LIMIT 是正文预览的截断长度。附件正文可能有几十万字,整段塞进 DOM 会让
// 页签切换明显卡顿;2000 字足够判断「解析出来的是不是我要的那份文档」。
const PREVIEW_LIMIT = 2000;

export function SpecTab({
  platformId,
  form,
  set,
  onSave,
  saving,
}: {
  platformId: number;
  form: Partial<SRCPlatform>;
  set: (patch: Partial<SRCPlatform>) => void;
  onSave: () => void;
  saving: boolean;
}) {
  const [files, setFiles] = React.useState<SRCPlatformFile[]>([]);
  const [filesLoading, setFilesLoading] = React.useState(true);
  const [uploadingKind, setUploadingKind] = React.useState<string | null>(null);
  const [expanded, setExpanded] = React.useState<Set<number>>(new Set());

  const loadFiles = React.useCallback(() => {
    setFilesLoading(true);
    api
      .srcPlatformFiles(platformId)
      .then((fs) => setFiles(fs ?? []))
      .catch((e) => toast.error(`加载附件失败:${(e as Error).message}`))
      .finally(() => setFilesLoading(false));
  }, [platformId]);

  React.useEffect(() => {
    loadFiles();
  }, [loadFiles]);

  const upload = async (kind: SRCPlatformFileKind, file: File) => {
    setUploadingKind(kind);
    try {
      const r = await api.uploadSRCPlatformFile(platformId, file, kind);
      // 解析结果决定这份规范是否真的注入 AI,必须在提示里说清楚而不是只报「上传成功」。
      const meta = parseStatusMeta(r.file?.parse_status ?? "");
      toast.success(`「${file.name}」已上传：${meta.label}`);
      loadFiles();
    } catch (e) {
      toast.error(`上传失败:${(e as Error).message}`);
    } finally {
      setUploadingKind(null);
    }
  };

  const removeFile = async (f: SRCPlatformFile) => {
    if (!window.confirm(`删除附件「${f.filename}」?删除后其正文不再注入 AI 约束,不可恢复。`)) return;
    try {
      await api.deleteSRCPlatformFile(platformId, f.id);
      toast.success("附件已删除");
      loadFiles();
    } catch (e) {
      toast.error(`删除失败:${(e as Error).message}`);
    }
  };

  const togglePreview = (id: number) => {
    setExpanded((s) => {
      const n = new Set(s);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });
  };

  const hasSkipped = files.some((f) => f.parse_status === "skipped");

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle className="text-sm">规范文本</CardTitle>
          <p className="text-muted-foreground mt-1 text-xs">
            四类文本会与下方附件正文一起渲染成注入 AI 的硬约束。四段共用一次保存（与页面顶部「保存」是同一个动作）——
            规范是一个整体，分四次提交容易出现「改了守则但奖励标准还是旧的」这类半套配置
          </p>
        </CardHeader>
        <CardContent className="space-y-5">
          {SPEC_FIELDS.map((f, i) => (
            <React.Fragment key={f.key}>
              {i > 0 && <Separator />}
              <SpecTextarea field={f} value={form[f.key] ?? ""} onChange={(v) => set({ [f.key]: v })} />
            </React.Fragment>
          ))}
          <div className="flex items-center justify-end gap-3 border-t pt-4">
            <span className="text-muted-foreground text-xs">四段规范与平台信息一起提交</span>
            <Button variant="outline" size="sm" disabled={saving} onClick={onSave}>
              {saving ? "保存中…" : "保存全部规范文本"}
            </Button>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-sm">规范附件</CardTitle>
          <p className="text-muted-foreground mt-1 text-xs">
            支持 .md / .markdown / .txt 直读、.docx 解析成 Markdown；.pdf / .xlsx 只存档不解析——服务端没有这两类的解析器，
            硬抽出的正文既不可靠又会把噪声注入 AI 约束，所以宁可不注入。老版 .doc 请另存为 .docx 后上传。单份上限 32MB。
          </p>
        </CardHeader>
        <CardContent className="space-y-5">
          {filesLoading && files.length === 0 ? (
            <p className="text-muted-foreground py-8 text-center text-sm">加载中…</p>
          ) : (
            SRC_PLATFORM_FILE_KINDS.map((k, i) => {
              const kindFiles = files.filter((f) => f.kind === k.value);
              return (
                <React.Fragment key={k.value}>
                  {i > 0 && <Separator />}
                  <FileKindSection
                    label={k.label}
                    files={kindFiles}
                    uploading={uploadingKind === k.value}
                    expanded={expanded}
                    onUpload={(file) => void upload(k.value, file)}
                    onDelete={(f) => void removeFile(f)}
                    onTogglePreview={togglePreview}
                  />
                </React.Fragment>
              );
            })
          )}

          {hasSkipped && (
            <p className="text-muted-foreground bg-muted/40 rounded-md px-3 py-2 text-xs">
              有附件仅存档未解析（灰色徽标）。它们不会进入 AI 约束——若这份规范必须生效，请改用 .md / .txt / .docx
              重新上传，或把要点直接写进上方的规范文本域。
            </p>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

// FileKindSection 是某一类规范下的附件区:上传入口 + 文件清单。
// 每类独立上传入口(而不是一个上传按钮 + 类别下拉),是因为四类各自独立注入,
// 用户传「奖励标准」时不该还要记得在下拉里选对类别。
function FileKindSection({
  label,
  files,
  uploading,
  expanded,
  onUpload,
  onDelete,
  onTogglePreview,
}: {
  label: string;
  files: SRCPlatformFile[];
  uploading: boolean;
  expanded: Set<number>;
  onUpload: (f: File) => void;
  onDelete: (f: SRCPlatformFile) => void;
  onTogglePreview: (id: number) => void;
}) {
  const inputRef = React.useRef<HTMLInputElement>(null);

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <span className="font-medium text-sm">{label}</span>
          <Badge variant="secondary" className="text-[10px]">
            {files.length}
          </Badge>
        </div>
        <input
          ref={inputRef}
          type="file"
          accept=".md,.markdown,.txt,.docx,.doc,.pdf,.xlsx,.xls"
          className="hidden"
          onChange={(e) => {
            const f = e.target.files?.[0];
            if (f) onUpload(f);
            // 清空 value:同一个文件改完再传一次也要能触发 change。
            e.target.value = "";
          }}
        />
        <Button variant="outline" size="sm" disabled={uploading} onClick={() => inputRef.current?.click()}>
          <UploadIcon className="size-4" />
          {uploading ? "上传中…" : `上传${label}`}
        </Button>
      </div>

      {files.length === 0 ? (
        <p className="text-muted-foreground rounded-md border border-dashed px-3 py-4 text-center text-xs">
          暂无「{label}」附件。上传后其正文会自动拼进平台规范一起注入 AI 约束。
        </p>
      ) : (
        <ul className="space-y-1.5">
          {files.map((f) => (
            <li key={f.id} className="rounded-md border px-3 py-2">
              <div className="flex flex-wrap items-center gap-2">
                <FileTextIcon className="text-muted-foreground size-4 shrink-0" />
                <span className="min-w-0 flex-1 truncate text-sm" title={f.filename}>
                  {f.filename}
                </span>
                <span className="text-muted-foreground shrink-0 text-xs tabular-nums">{fmtBytes(f.byte_size)}</span>
                <ParseStatusBadge status={f.parse_status} />
                <div className="flex shrink-0 items-center gap-0.5">
                  {/* 下载走原生导航(带 token 的 fetch 拿不到浏览器下载语义)。 */}
                  <Button variant="ghost" size="icon-xs" asChild title="下载原件">
                    <a href={api.srcPlatformFileDownloadUrl(f.platform_id, f.id)} download={f.filename}>
                      <DownloadIcon className="size-3.5" />
                    </a>
                  </Button>
                  <Button variant="ghost" size="icon-xs" onClick={() => onDelete(f)} title="删除附件">
                    <Trash2Icon className="text-muted-foreground size-3.5" />
                  </Button>
                  {f.content_text ? (
                    <Button
                      variant="ghost"
                      size="icon-xs"
                      onClick={() => onTogglePreview(f.id)}
                      title={expanded.has(f.id) ? "收起正文预览" : "查看解析正文"}
                    >
                      {expanded.has(f.id) ? (
                        <ChevronDownIcon className="size-3.5" />
                      ) : (
                        <ChevronRightIcon className="size-3.5" />
                      )}
                    </Button>
                  ) : null}
                </div>
              </div>

              {/* parse_note 是解析器的自述(文本直读多少字 / docx 章节数 / 失败原因),
                  失败与仅存档时必须显示,成功时也有参考价值。 */}
              {f.parse_note && <p className="text-muted-foreground mt-1 text-xs">{f.parse_note}</p>}

              {expanded.has(f.id) && f.content_text ? (
                <pre className="bg-muted/30 mt-2 max-h-64 overflow-auto rounded-md px-3 py-2 font-mono text-xs whitespace-pre-wrap">
                  {f.content_text.length > PREVIEW_LIMIT
                    ? `${f.content_text.slice(0, PREVIEW_LIMIT)}\n\n…（已截断，共 ${f.content_text.length} 字）`
                    : f.content_text}
                </pre>
              ) : null}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
