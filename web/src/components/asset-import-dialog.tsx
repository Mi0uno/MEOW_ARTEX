"use client";

import * as React from "react";

import { UploadIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { AssetImportResult } from "@/lib/types";

// 导入文件的前端限制：与后端契约一致（txt/csv/xlsx，≤10MB）。
const IMPORT_ACCEPT = [".txt", ".csv", ".xlsx"];
const IMPORT_MAX_BYTES = 10 * 1024 * 1024;

// 导入文件大小的展示文案（<1MB 用 KB，避免出现 0.00 MB）。
function formatImportFileSize(bytes: number): string {
  if (bytes >= 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(2)} MB`;
  return `${Math.max(1, Math.round(bytes / 1024))} KB`;
}

interface AssetImportDialogProps {
  // 传入时本次导入的资产强制归属该企业（企业详情页用），说明文案随之切换为企业语境；
  // 不传则走后端按各企业资产范围自动认领，行为与资产主页一致。
  companyId?: number;
  // 关闭对话框时回调：外层刷新列表（摘要 toast 由组件内部负责）。
  onImported: () => void;
}

// 导入资产对话框：选择 txt/csv/xlsx 上传，成功后停留在对话框内展示导入结果；
// 关闭对话框时（onImported）刷新外层列表并给一条摘要 toast。
export function AssetImportDialog({ companyId, onImported }: AssetImportDialogProps) {
  const [open, setOpen] = React.useState(false);
  const [file, setFile] = React.useState<File | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [result, setResult] = React.useState<AssetImportResult | null>(null);
  const fileInputRef = React.useRef<HTMLInputElement>(null);
  const companyScoped = companyId != null && companyId > 0;

  // 每次打开都重置上次的选择与结果。
  React.useEffect(() => {
    if (!open) return;
    setFile(null);
    setResult(null);
  }, [open]);

  const handleOpenChange = (next: boolean) => {
    if (!next) {
      // 关闭时刷新外层列表；有导入结果时补一条摘要 toast。
      if (result) {
        if (result.failed > 0)
          toast.warning(`成功 ${result.imported} / 失败 ${result.failed} / 跳过 ${result.skipped}`);
        else toast.success(`成功导入 ${result.imported} 项资产`);
      }
      onImported();
    }
    setOpen(next);
  };

  // 选中文件：先做扩展名与大小校验，超限直接 toast.error、不发请求。
  const pickFile = (picked: File | null) => {
    if (!picked) {
      setFile(null);
      return;
    }
    const ext = `.${(picked.name.split(".").pop() ?? "").toLowerCase()}`;
    if (!IMPORT_ACCEPT.includes(ext)) {
      toast.error("仅支持 txt / csv / xlsx 文件");
    } else if (picked.size > IMPORT_MAX_BYTES) {
      toast.error("文件超过 10MB 上限");
    } else {
      setFile(picked);
      return;
    }
    // 校验失败：清掉选择，同一个文件也能重新选。
    setFile(null);
    if (fileInputRef.current) fileInputRef.current.value = "";
  };

  const submit = async () => {
    if (!file || busy) return;
    setBusy(true);
    try {
      const res = await api.importAssets(file, companyScoped ? companyId : undefined);
      // 成功后停留在对话框内展示结果，等用户关闭时再刷新列表。
      setResult(res);
    } catch (e) {
      toast.error(`导入失败：${String((e as Error)?.message ?? e)}`);
    } finally {
      setBusy(false);
    }
  };

  // 结果展示后支持重传：清掉上次文件与结果，重新唤起文件选择器。
  const reselect = () => {
    setResult(null);
    setFile(null);
    if (fileInputRef.current) {
      fileInputRef.current.value = "";
      fileInputRef.current.click();
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm">
          <UploadIcon className="size-4" /> 导入资产
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>导入资产</DialogTitle>
          <DialogDescription>
            上传 txt / csv / xlsx 文件批量导入资产，导入结果在下方展示。
            {companyScoped && "导入的资产将归属当前企业。"}
          </DialogDescription>
        </DialogHeader>
        <FieldGroup className="py-2">
          <Field>
            <FieldLabel htmlFor="asset-import-file">资产文件</FieldLabel>
            <Input
              id="asset-import-file"
              ref={fileInputRef}
              type="file"
              accept=".txt,.csv,.xlsx"
              disabled={busy}
              onChange={(e) => pickFile(e.target.files?.[0] ?? null)}
            />
            {file && (
              <p className="text-muted-foreground text-xs">
                已选择：<span className="font-mono">{file.name}</span>
                <span className="tabular-nums">（{formatImportFileSize(file.size)}）</span>
              </p>
            )}
            <FieldDescription>
              txt 每行一条（域名 / IP / host:port / URL）；csv 与 xlsx 需包含「域名 / URL / IP /
              端口」等列名，或单列目标列表。# 开头的行视为注释。
              {companyScoped ? "导入的资产将归属当前企业。" : "资产归属按各企业的资产范围自动认领。"}
            </FieldDescription>
          </Field>

          {/* 导入结果：成功后停留在对话框内展示，主按钮变「重新选择文件」支持重传 */}
          {result && (
            <div className="space-y-2 rounded-lg border p-3">
              <div className="grid grid-cols-4 gap-2 text-center">
                <div className="rounded-md bg-muted/50 px-2 py-1.5">
                  <div className="text-muted-foreground text-xs">成功</div>
                  <div className="font-semibold text-emerald-600 text-sm tabular-nums">{result.imported}</div>
                </div>
                <div className="rounded-md bg-muted/50 px-2 py-1.5">
                  <div className="text-muted-foreground text-xs">失败</div>
                  <div className="font-semibold text-red-600 text-sm tabular-nums">{result.failed}</div>
                </div>
                <div className="rounded-md bg-muted/50 px-2 py-1.5">
                  <div className="text-muted-foreground text-xs">跳过</div>
                  <div className="font-semibold text-amber-600 text-sm tabular-nums">{result.skipped}</div>
                </div>
                <div className="rounded-md bg-muted/50 px-2 py-1.5">
                  <div className="text-muted-foreground text-xs">合计</div>
                  <div className="font-semibold text-sm tabular-nums">{result.total}</div>
                </div>
              </div>
              {result.errors.length === 0 ? (
                <p className="py-1 text-center text-muted-foreground text-xs">全部导入成功</p>
              ) : (
                <>
                  <div className="max-h-56 overflow-auto rounded-md border">
                    <Table>
                      <TableHeader className="sticky top-0 z-10 bg-card">
                        <TableRow>
                          <TableHead className="w-16">行号</TableHead>
                          <TableHead>内容</TableHead>
                          <TableHead>原因</TableHead>
                        </TableRow>
                      </TableHeader>
                      <TableBody>
                        {result.errors.map((err) => (
                          <TableRow key={`${err.row}:${err.value}`}>
                            <TableCell className="text-xs tabular-nums">{err.row}</TableCell>
                            <TableCell className="max-w-[12rem] truncate font-mono text-xs" title={err.value}>
                              {err.value}
                            </TableCell>
                            <TableCell className="text-muted-foreground text-xs">{err.error}</TableCell>
                          </TableRow>
                        ))}
                      </TableBody>
                    </Table>
                  </div>
                  {result.failed > result.errors.length && (
                    <p className="text-right text-muted-foreground text-xs">仅显示前 200 条</p>
                  )}
                </>
              )}
            </div>
          )}
        </FieldGroup>
        <DialogFooter>
          <Button variant="outline" onClick={() => setOpen(false)} disabled={busy}>
            {result ? "关闭" : "取消"}
          </Button>
          {result ? (
            <Button onClick={reselect}>重新选择文件</Button>
          ) : (
            <Button onClick={submit} disabled={busy || !file}>
              {busy ? "导入中…" : "开始导入"}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
