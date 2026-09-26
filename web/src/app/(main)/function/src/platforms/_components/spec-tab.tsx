"use client";

// 规范与文件页签:平台级规范的四类文本域 + 统一附件区(上传 / AI 识别填入 / 类别改派)。
//
// 为什么文本域与附件放在同一屏:两者最终会拼成同一份注入文本(db.SRCPlatformSpecText),
// 用户在这里写的每一行都会变成 AI 的硬约束。分成两处编辑,用户很难意识到
// 「附件没解析成功 = 这段规范其实没生效」——所以解析状态必须在文件旁边直接可见。
//
// 为什么上传不再按四类分开:上传者手里往往只有一堆平台文档,要求他上传前就判断
// 「这份算奖励标准还是规范」会把人卡住。统一收进「未归类」(kind='other'),再靠
// 每行的类别下拉改派或 AI 识别归类,把分类成本从上传时挪到上传后。

import * as React from "react";

import {
  ChevronDownIcon,
  ChevronRightIcon,
  DownloadIcon,
  FileTextIcon,
  Loader2Icon,
  SparklesIcon,
  Trash2Icon,
  UploadIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { api, http } from "@/lib/api";
import {
  SRC_PLATFORM_FILE_KINDS,
  type SPECAIFillSuggestion,
  type SRCPlatform,
  type SRCPlatformFile,
} from "@/lib/types";

import { fmtBytes, ParseStatusBadge, parseStatusMeta, SPEC_FIELDS, SpecTextarea } from "./platform-shared";

// PREVIEW_LIMIT 是正文预览的截断长度。附件正文可能有几十万字,整段塞进 DOM 会让
// 页签切换明显卡顿;2000 字足够判断「解析出来的是不是我要的那份文档」。
const PREVIEW_LIMIT = 2000;

// AI_FILL_TIMEOUT_MS 是 AI 识别请求的硬上限。LLM 结构化识别实测在 deepseek 上
// 会超过 120s(1.7 万字附件),必须大于服务端 ctx 的 300s——前端先超时的话,
// 用户看到的是超时错误,而后端其实还在继续算,结果被白白丢弃。
const AI_FILL_TIMEOUT_MS = 320_000;

// KIND_OPTIONS 是类别改派下拉的候选 = 四类规范 + 未归类。'other' 不在
// SRC_PLATFORM_FILE_KINDS 里(它不是规范类别,是统一上传的暂存类别),
// 但列表里的文件可能正处于该状态,必须能显示出来、也能改回去。
const KIND_OPTIONS = [...SRC_PLATFORM_FILE_KINDS, { value: "other", label: "未归类" }];

const kindLabel = (v: string) => KIND_OPTIONS.find((k) => k.value === v)?.label ?? v;

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
  const [uploading, setUploading] = React.useState(false);
  const [expanded, setExpanded] = React.useState<Set<number>>(new Set());
  // AI 识别中按钮 spinner+禁用;期间不做取消(后端无对应端点),只能等——文案里写明。
  const [aiBusy, setAiBusy] = React.useState(false);
  // aiBusy 的 ref 镜像:上传完成回调要判断「此刻是否已有识别在途」,而 state 在
  // await 之后读到的是发起上传那一帧的旧值,ref 才是实时真相。
  const aiBusyRef = React.useRef(false);
  // aiNote 是 LLM 返回的识别依据说明(用了哪些附件/置信提示),展示给用户参考。
  const [aiNote, setAiNote] = React.useState("");
  // 勾选的附件 id:空集 = 用全部附件(契约缺省语义),非空 = 只识别勾选的。
  const [pickedIds, setPickedIds] = React.useState<Set<number>>(new Set());
  const [reassigningId, setReassigningId] = React.useState<number | null>(null);
  const uploadInputRef = React.useRef<HTMLInputElement>(null);
  // 每个规范文本域的按钮忙态:key=字段,值=upload(上传识别)/polish(AI 润色)。
  // 逐字段而非全局:四个按钮互不阻塞,用户可以一边润色守则一边上传奖励标准。
  const [fieldBusy, setFieldBusy] = React.useState<Record<string, "upload" | "polish" | null>>({});

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

  // upload 是唯一的上传入口:类别固定 'other',先收文件再归类。
  // 上传解析成功后自动对该文件触发一次 AI 识别——旧流程要用户再手动点
  // 「AI 识别并填入」,多数人会漏掉这一步,规范其实没生效。
  const upload = async (file: File) => {
    setUploading(true);
    try {
      const r = await api.uploadSRCPlatformFile(platformId, file, "other");
      // 解析结果决定这份规范是否真的注入 AI,必须在提示里说清楚而不是只报「上传成功」。
      const f = r.file;
      const meta = parseStatusMeta(f?.parse_status ?? "");
      loadFiles();
      // 解析失败或没解析出正文:自动识别也无从识别,保持原提示即可,不触发。
      if (!f || f.parse_status === "failed" || !f.content_text.trim()) {
        toast.success(`「${file.name}」已上传：${meta.label}`);
        return;
      }
      // 已有识别在途:不并发叠请求(共用一个 aiBusy,叠加只会互相覆盖结果),
      // 提示用户稍后手动补一次;新文件已入列表,勾选或全量识别都能覆盖到它。
      if (aiBusyRef.current) {
        toast.success(`「${file.name}」已上传：${meta.label}`);
        toast.info("已有识别在进行，可稍后手动点「AI 识别并填入」");
        return;
      }
      toast.success(`「${file.name}」已上传，正在自动识别…（${meta.label}）`);
      // 只识别这一份新文件,不动用户的勾选集;识别中/结果提示与手动路径共用。
      void aiFill([f.id]);
    } catch (e) {
      toast.error(`上传失败:${(e as Error).message}`);
    } finally {
      setUploading(false);
    }
  };

  const removeFile = async (f: SRCPlatformFile) => {
    if (!window.confirm(`删除附件「${f.filename}」?删除后其正文不再注入 AI 约束,不可恢复。`)) return;
    try {
      await api.deleteSRCPlatformFile(platformId, f.id);
      // 已删除的文件不该留在 AI 识别的勾选集里,否则勾选数与列表对不上。
      setPickedIds((prev) => {
        const n = new Set(prev);
        n.delete(f.id);
        return n;
      });
      toast.success("附件已删除");
      loadFiles();
    } catch (e) {
      toast.error(`删除失败:${(e as Error).message}`);
    }
  };

  // reassign 是类别改派:统一上传的文件由用户(或核对 AI 识别结果时)手动归类。
  const reassign = async (f: SRCPlatformFile, kind: string) => {
    setReassigningId(f.id);
    try {
      await api.updateSRCPlatformFileKind(platformId, f.id, kind);
      toast.success(`「${f.filename}」已归类为「${kindLabel(kind)}」`);
      loadFiles();
    } catch (e) {
      toast.error(`归类失败:${(e as Error).message}`);
    } finally {
      setReassigningId(null);
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

  const togglePick = (id: number, on: boolean) => {
    setPickedIds((prev) => {
      const n = new Set(prev);
      if (on) n.add(id);
      else n.delete(id);
      return n;
    });
  };

  // aiFill 调 AI 识别接口,把返回的四类建议预填进文本域(不落库,用户检查后走既有保存)。
  // 不走 api.aiFillPlatformSpec:它经 http() 的 60s 默认超时,而 LLM 识别常要 1-2 分钟。
  // 直接用 http 并传 180s AbortSignal(http 见到调用方 signal 就不再叠加默认超时),
  // 与 batch 页直连 POST /tasks 是同一种「标准封装不适合长请求」的处理。
  // overrideIds 可选:上传成功后的自动触发传「新文件 id」绕过勾选集;
  // 手动按钮不传,保持「缺省=全部 / 勾选=只识别勾选」的既有语义。
  const aiFill = async (overrideIds?: number[]) => {
    // 勾选集与当前列表求交:勾选可能残留已删除文件的 id,交一下最稳。
    const picked = overrideIds ?? files.filter((f) => pickedIds.has(f.id)).map((f) => f.id);
    setAiBusy(true);
    aiBusyRef.current = true;
    setAiNote("");
    try {
      const r = await http<{ suggestion: SPECAIFillSuggestion }>(`/src/platforms/${platformId}/spec/ai-fill`, {
        method: "POST",
        // file_ids 缺省(undefined)= 用全部有正文的附件,与契约「缺省=全部」一致。
        body: JSON.stringify({ file_ids: picked.length > 0 ? picked : undefined }),
        signal: AbortSignal.timeout(AI_FILL_TIMEOUT_MS),
      });
      const s = r.suggestion;
      // 只用非空建议覆盖对应文本域:契约约定「正文没提到的类别返回空串」,
      // 若把空串也写进表单,会把用户已手工填好的段落清空——AI 只建议,不能帮倒忙。
      const patch: Partial<SRCPlatform> = {};
      let filled = 0;
      for (const f of SPEC_FIELDS) {
        const v = (s?.[f.key] ?? "").trim();
        if (v) {
          patch[f.key] = v;
          filled += 1;
        }
      }
      setAiNote(s?.note ?? "");
      if (filled === 0) {
        toast.warning("AI 没有识别出可填入的规范内容，请确认附件已成功解析（见各行徽标）");
        return;
      }
      set(patch);
      // 预填 ≠ 保存:必须让用户逐段检查后自己点保存,避免识别误差直接落库。
      toast.success(`AI 已填入 ${filled}/4 段，请检查后保存`);
    } catch (e) {
      toast.error(`AI 识别失败:${(e as Error).message}`);
    } finally {
      setAiBusy(false);
      aiBusyRef.current = false;
    }
  };

  const hasSkipped = files.some((f) => f.parse_status === "skipped");

  // 上传识别:文件 → 后端纯解析 → 正文回填本字段文本框。不存附件行——
  // 要存档走下方统一上传(两条路径对同一文件解析出的正文一致,不会不一致)。
  const fieldLabel = (key: string) => SPEC_FIELDS.find((f) => f.key === key)?.label ?? key;
  const uploadToField = async (key: string, file: File) => {
    setFieldBusy((s) => ({ ...s, [key]: "upload" }));
    try {
      const r = await api.parseSpecFile(file);
      if (r.parse_status === "failed") {
        toast.error(`解析失败:${r.note || "无法从该文件提取文本"}`);
        return;
      }
      if (!r.text.trim()) {
        toast.warning("文件里没有解析出文本内容");
        return;
      }
      // 回填进表单:key 是四个规范字段之一(与 SPEC_FIELDS 同源),不会是别的列
      set({ [key]: r.text } as Partial<SRCPlatform>);
      toast.success(`已识别填入「${fieldLabel(key)}」(共 ${r.text.length} 字),请检查后保存`);
    } catch (e) {
      toast.error(`识别失败:${(e as Error).message}`);
    } finally {
      setFieldBusy((s) => ({ ...s, [key]: null }));
    }
  };

  // AI 润色:只改当前字段的措辞结构,事实(金额/分值/禁令)由后端提示词锁死不改。
  // 润色结果同样只预填,落库仍走「保存全部规范文本」。
  const polishField = async (key: keyof SRCPlatform & string) => {
    const current = String(form[key] ?? "").trim();
    if (!current) {
      toast.error("请先填写内容或上传文件");
      return;
    }
    setFieldBusy((s) => ({ ...s, [key]: "polish" }));
    try {
      const { polished } = await api.polishSpecField(key, current);
      set({ [key]: polished } as Partial<SRCPlatform>);
      toast.success("AI 润色完成，请检查后保存");
    } catch (e) {
      toast.error(`AI 润色失败:${(e as Error).message}`);
    } finally {
      setFieldBusy((s) => ({ ...s, [key]: null }));
    }
  };

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle className="text-sm">规范文本</CardTitle>
          <p className="text-muted-foreground mt-1 text-xs">
            四类文本会与下方附件正文一起渲染成注入 AI 的硬约束，也是「AI 识别并填入」的落点。四段共用一次保存（与页面顶部「保存」是同一个动作）——
            规范是一个整体，分四次提交容易出现「改了守则但奖励标准还是旧的」这类半套配置
          </p>
        </CardHeader>
        <CardContent className="space-y-5">
          {SPEC_FIELDS.map((f, i) => (
            <React.Fragment key={f.key}>
              {i > 0 && <Separator />}
              <SpecTextarea
                field={f}
                value={form[f.key] ?? ""}
                onChange={(v) => set({ [f.key]: v })}
                uploading={fieldBusy[f.key] === "upload"}
                polishing={fieldBusy[f.key] === "polish"}
                onUploadFile={(file) => void uploadToField(f.key, file)}
                onPolish={() => void polishField(f.key)}
              />
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
            统一上传，不必先选类别——新文件先归入「未归类」，可用每行的类别下拉改派，或交给 AI 识别归类。解析出的正文会与上方规范文本一起注入
            AI 约束（解析状态见各行徽标）。老版 .doc 请另存为 .docx 后上传，单份上限 32MB。
          </p>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2 rounded-md border bg-muted/30 p-3">
            <div className="flex flex-wrap items-center gap-2">
              <Button size="sm" disabled={aiBusy || files.length === 0} onClick={() => void aiFill()}>
                {aiBusy ? <Loader2Icon className="size-4 animate-spin" /> : <SparklesIcon className="size-4" />}
                {aiBusy ? "AI 识别中，约 1 分钟…" : "AI 识别并填入"}
              </Button>
              <span className="text-muted-foreground text-xs">
                {aiBusy
                  ? "正在读取附件正文并结构化归类，请勿关闭页面"
                  : pickedIds.size > 0
                    ? `只识别勾选的 ${pickedIds.size} 份附件`
                    : "默认识别全部附件，勾选行可只识别部分"}
              </span>
            </div>
            {aiNote && <p className="text-muted-foreground text-xs">AI 说明：{aiNote}</p>}
            <p className="text-muted-foreground text-xs">
              识别结果只预填进上方文本域，不会自动保存——请逐段检查修改后再点「保存」。
            </p>
          </div>

          <div className="flex items-center justify-between gap-2">
            <span className="text-muted-foreground text-xs tabular-nums">共 {files.length} 份附件</span>
            <input
              ref={uploadInputRef}
              type="file"
              accept=".md,.markdown,.txt,.docx,.doc,.pdf,.xlsx,.xls"
              className="hidden"
              onChange={(e) => {
                const f = e.target.files?.[0];
                if (f) void upload(f);
                // 清空 value:同一个文件改完再传一次也要能触发 change。
                e.target.value = "";
              }}
            />
            <Button variant="outline" size="sm" disabled={uploading} onClick={() => uploadInputRef.current?.click()}>
              <UploadIcon className="size-4" />
              {uploading ? "上传中…" : "上传规范附件"}
            </Button>
          </div>

          {filesLoading && files.length === 0 ? (
            <p className="text-muted-foreground py-8 text-center text-sm">加载中…</p>
          ) : files.length === 0 ? (
            <p className="text-muted-foreground rounded-md border border-dashed px-3 py-4 text-center text-xs">
              暂无附件。上传平台文档（奖励规则 / 评分说明 / 守则 / 提交规范等）后，AI 可据此识别填入上方四类规范。
            </p>
          ) : (
            <ul className="space-y-1.5">
              {files.map((f) => (
                <FileRow
                  key={f.id}
                  file={f}
                  reassigning={reassigningId === f.id}
                  picked={pickedIds.has(f.id)}
                  expanded={expanded.has(f.id)}
                  onPick={togglePick}
                  onReassign={(kind) => void reassign(f, kind)}
                  onDelete={() => void removeFile(f)}
                  onTogglePreview={() => togglePreview(f.id)}
                />
              ))}
            </ul>
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

// FileRow 是统一附件列表里的一行:勾选(限定 AI 识别范围)/文件名/大小/解析状态/
// 类别改派 Select/下载/删除/正文预览展开。文件不再按四类分组——分组信息改由
// 每行的类别 Select 承载,列表顺序即上传顺序,最接近「收件箱」的心智。
function FileRow({
  file,
  reassigning,
  picked,
  expanded,
  onPick,
  onReassign,
  onDelete,
  onTogglePreview,
}: {
  file: SRCPlatformFile;
  reassigning: boolean;
  picked: boolean;
  expanded: boolean;
  onPick: (id: number, on: boolean) => void;
  onReassign: (kind: string) => void;
  onDelete: () => void;
  onTogglePreview: () => void;
}) {
  return (
    <li className="rounded-md border px-3 py-2">
      <div className="flex flex-wrap items-center gap-2">
        <Checkbox
          checked={picked}
          onCheckedChange={(v) => onPick(file.id, v === true)}
          aria-label={`勾选附件 ${file.filename}`}
          className="shrink-0"
        />
        <FileTextIcon className="text-muted-foreground size-4 shrink-0" />
        <span className="min-w-0 flex-1 truncate text-sm" title={file.filename}>
          {file.filename}
        </span>
        <span className="text-muted-foreground shrink-0 text-xs tabular-nums">{fmtBytes(file.byte_size)}</span>
        <ParseStatusBadge status={file.parse_status} />
        <Select value={file.kind} onValueChange={onReassign} disabled={reassigning}>
          <SelectTrigger size="sm" className="w-28 shrink-0" title="附件类别（可改派）">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {KIND_OPTIONS.map((k) => (
              <SelectItem key={k.value} value={k.value}>
                {k.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <div className="flex shrink-0 items-center gap-0.5">
          {/* 下载走原生导航(带 token 的 fetch 拿不到浏览器下载语义)。 */}
          <Button variant="ghost" size="icon-xs" asChild title="下载原件">
            <a href={api.srcPlatformFileDownloadUrl(file.platform_id, file.id)} download={file.filename}>
              <DownloadIcon className="size-3.5" />
            </a>
          </Button>
          <Button variant="ghost" size="icon-xs" onClick={onDelete} title="删除附件">
            <Trash2Icon className="text-muted-foreground size-3.5" />
          </Button>
          {file.content_text ? (
            <Button
              variant="ghost"
              size="icon-xs"
              onClick={onTogglePreview}
              title={expanded ? "收起正文预览" : "查看解析正文"}
            >
              {expanded ? <ChevronDownIcon className="size-3.5" /> : <ChevronRightIcon className="size-3.5" />}
            </Button>
          ) : null}
        </div>
      </div>

      {/* parse_note 是解析器的自述(文本直读多少字 / docx 章节数 / 失败原因),
          失败与仅存档时必须显示,成功时也有参考价值。 */}
      {file.parse_note && <p className="text-muted-foreground mt-1 text-xs">{file.parse_note}</p>}

      {expanded && file.content_text ? (
        <pre className="bg-muted/30 mt-2 max-h-64 overflow-auto rounded-md px-3 py-2 font-mono text-xs whitespace-pre-wrap">
          {file.content_text.length > PREVIEW_LIMIT
            ? `${file.content_text.slice(0, PREVIEW_LIMIT)}\n\n…（已截断，共 ${file.content_text.length} 字）`
            : file.content_text}
        </pre>
      ) : null}
    </li>
  );
}
