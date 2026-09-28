"use client";

// 导出前「已导出过」提醒。点「导出已选」先查 POST /api/reports/export-check:
//   无命中 → 直接 onProceed(全部 id) 打开批量导出对话框;
//   有命中 → 弹提醒,由用户选「仍导出全部」或「只导出没导出过的」再继续。
// 刻意自包含(内联 fetch + localStorage token,不走 api.ts):报告导出 v2 契约
// (REPORT_EXPORT_V2_CONTRACTS.md)冻结期 api.ts 与 reports 页面由并行会话所有,
// 本文件独立成篇把共享文件接触面压到最小,page.tsx 只需数行接线。
// 「导出过」判定口径在服务端:exported 事件 ∪ done 导出作业(跨同步/后台两种形态)。

import * as React from "react";

import { toast } from "sonner";

import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";

// 与 api.ts getToken 同源:令牌存 localStorage(此处内联以避免改冻结文件)。
function localToken(): string | null {
  if (typeof window === "undefined") return null;
  return localStorage.getItem("artex_token");
}

export function useExportedReminder(onProceed: (ids: number[]) => void) {
  // warn 非空 = 需要弹提醒(含全量 id 与其中已导出过的子集)。
  const [warn, setWarn] = React.useState<{ all: number[]; exported: number[] } | null>(null);
  const [checking, setChecking] = React.useState(false);

  // checkAndProceed 查「已导出过」并分流:无命中直接继续,有命中弹提醒。
  const checkAndProceed = async (ids: number[]) => {
    if (ids.length === 0) {
      toast.error("请先勾选要导出的报告");
      return;
    }
    setChecking(true);
    try {
      const token = localToken();
      const r = await fetch("/api/reports/export-check", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
        },
        body: JSON.stringify({ ids }),
      });
      if (!r.ok) {
        let msg = `检查导出记录失败:${r.status}`;
        try {
          const j = await r.json();
          if (typeof j?.error === "string") msg = j.error;
        } catch {
          /* 非 JSON 错误体,保留状态码信息 */
        }
        throw new Error(msg);
      }
      const data = (await r.json()) as { exported?: number[] };
      const exported = Array.isArray(data.exported) ? data.exported : [];
      if (exported.length === 0) {
        onProceed(ids);
        return;
      }
      setWarn({ all: ids, exported });
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setChecking(false);
    }
  };

  const proceed = (ids: number[]) => {
    setWarn(null);
    onProceed(ids);
  };

  const exportedSet = new Set(warn?.exported ?? []);
  const fresh = (warn?.all ?? []).filter((id) => !exportedSet.has(id));

  const reminder = (
    <AlertDialog
      open={warn !== null}
      onOpenChange={(v) => {
        if (!v) setWarn(null);
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>选中的报告中有 {warn?.exported.length ?? 0} 份此前已导出过</AlertDialogTitle>
          <AlertDialogDescription>
            共选中 {warn?.all.length ?? 0} 份报告,其中 {warn?.exported.length ?? 0}
            份已导出过(含单份导出、AI 导出与批量打包),重复导出会再次打包相同内容。
            {fresh.length > 0 ? `也可以只导出还没导出过的 ${fresh.length} 份。` : ""}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>取消</AlertDialogCancel>
          {fresh.length > 0 && (
            <Button variant="outline" onClick={() => proceed(fresh)}>
              只导出没导出过的 ({fresh.length})
            </Button>
          )}
          <Button onClick={() => proceed(warn?.all ?? [])}>仍导出全部 ({warn?.all.length ?? 0})</Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );

  return { checkAndProceed, checking, reminder };
}
