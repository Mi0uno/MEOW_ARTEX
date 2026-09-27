"use client";

// CVE 情报简报对话框(契约 POC_INTEL_CONTRACTS.md,Agent A 所有):
// 任务详情「CVE 情报」入口——展示按本任务资产指纹(技术栈/服务名/页面标题)自动
// 匹配的库内漏洞:fingerprints 徽标 + KEV/高危/其他三段条目。数据与注入 agent 的
// 简报同源同缓存(GET /api/tasks/{id}/vuln-brief),保证「界面看到的 = AI 拿到的」。
// 只做组件,不挂页面(集成期主会话挂载);接口失败静默降级,不崩任务详情页。

import * as React from "react";

import { Loader2Icon, ShieldAlertIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { severityBadgeClass } from "@/components/vulndb-dialogs";
import { api } from "@/lib/api";
import type { VulnBriefDTO, VulnEntry } from "@/lib/types";

// 后端 wire = {"brief": VulnBriefDTO}(契约 §4.3);api.ts 冻结签名把整个响应体
// 标成了 VulnBriefDTO——这里做一次兼容解包,带 brief 键与平铺两种形态都吃。
function unwrapBrief(resp: VulnBriefDTO | null): VulnBriefDTO | null {
  if (!resp) return null;
  const inner = (resp as unknown as { brief?: VulnBriefDTO }).brief;
  if (inner && typeof inner === "object" && "entries" in inner) return inner;
  return resp;
}

// 按 KEV / 高危(critical|high)/ 其他 分组,与服务端简报的分段保持一致。
function groupEntries(entries: VulnEntry[]) {
  const kev: VulnEntry[] = [];
  const high: VulnEntry[] = [];
  const rest: VulnEntry[] = [];
  for (const entry of entries) {
    if (entry.kev) kev.push(entry);
    else if (entry.severity === "critical" || entry.severity === "high") high.push(entry);
    else rest.push(entry);
  }
  return { kev, high, rest };
}

// 单条命中:严重度徽标 + KEV 徽标 + 编号/标题 + 厂商产品与 PoC 数。
function BriefingEntryRow({ entry }: { entry: VulnEntry }) {
  const vp = [entry.vendor, entry.product].filter(Boolean).join(" ");
  return (
    <div className="flex items-start gap-2 border-b py-1.5 text-xs last:border-b-0">
      <Badge variant="outline" className={`shrink-0 ${severityBadgeClass(entry.severity)}`}>
        {entry.severity || "-"}
      </Badge>
      {entry.kev ? (
        <Badge variant="outline" className="shrink-0 border-red-500/30 bg-red-500/15 text-red-600">
          KEV
        </Badge>
      ) : null}
      <div className="min-w-0 flex-1">
        <div className="break-words font-medium">
          {entry.cve_id ? <span className="mr-1.5 font-mono">{entry.cve_id}</span> : null}
          {entry.title}
        </div>
        {(vp || entry.poc_count > 0) && (
          <div className="text-muted-foreground">
            {vp}
            {vp && entry.poc_count > 0 ? " · " : ""}
            {entry.poc_count > 0 ? `PoC x${entry.poc_count}` : ""}
          </div>
        )}
      </div>
    </div>
  );
}

// 简报的一个分组段(标题 + 条目行;空段不渲染)。
function BriefingGroup({ title, entries }: { title: string; entries: VulnEntry[] }) {
  if (entries.length === 0) return null;
  return (
    <div className="rounded-md border p-3">
      <div className="mb-1.5 text-sm font-medium">{title}</div>
      {entries.map((entry) => (
        <BriefingEntryRow key={`${entry.id}-${entry.cve_id ?? ""}`} entry={entry} />
      ))}
    </div>
  );
}

// CVE 情报简报对话框:trigger 缺省渲染一个「CVE 情报简报」按钮;首次打开时拉取。
// 联动关闭(enabled=false)与加载失败都给出非阻断文案,不影响宿主页面。
export function VulnBriefingDialog({ taskId, trigger }: { taskId: string; trigger?: React.ReactNode }) {
  const [open, setOpen] = React.useState(false);
  const [brief, setBrief] = React.useState<VulnBriefDTO | null>(null);
  const [failed, setFailed] = React.useState(false);

  React.useEffect(() => {
    if (!open || brief || failed) return;
    let cancelled = false;
    api
      .taskVulnBrief(taskId)
      .then((resp) => {
        if (!cancelled) setBrief(unwrapBrief(resp));
      })
      .catch(() => {
        // 静默降级:情报卡不可用不能影响任务详情页其余部分。
        if (!cancelled) setFailed(true);
      });
    return () => {
      cancelled = true;
    };
  }, [open, taskId, brief, failed]);

  const groups = React.useMemo(() => groupEntries(brief?.entries ?? []), [brief]);
  const total = groups.kev.length + groups.high.length + groups.rest.length;

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        {trigger ?? (
          <Button variant="outline" size="sm">
            <ShieldAlertIcon className="size-3.5" />
            CVE 情报简报
          </Button>
        )}
      </DialogTrigger>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>CVE 情报简报 · 任务 #{taskId}</DialogTitle>
          <DialogDescription>
            按本任务资产指纹(技术栈/服务/页面标题)匹配的库内漏洞;任务启动时已注入 AI。
            {brief?.generated_at ? ` 生成于 ${new Date(brief.generated_at).toLocaleString()}` : ""}
          </DialogDescription>
        </DialogHeader>
        {failed ? (
          <p className="text-muted-foreground text-sm">简报加载失败,请稍后重试。</p>
        ) : !brief ? (
          <p className="text-muted-foreground flex items-center gap-2 text-sm">
            <Loader2Icon className="size-4 animate-spin" /> 加载中…
          </p>
        ) : !brief.enabled ? (
          <p className="text-muted-foreground text-sm">
            本任务未启用 CVE 情报联动(任务级开关或全局开关关闭)。可在创建任务的高级设置或漏洞库配置中开启。
          </p>
        ) : (
          <div className="flex flex-col gap-3">
            {brief.fingerprints.length > 0 ? (
              <div className="flex flex-col gap-1.5">
                <div className="text-muted-foreground text-xs font-medium">
                  匹配指纹({brief.fingerprints.length})
                </div>
                <div className="flex flex-wrap gap-1">
                  {brief.fingerprints.map((f) => (
                    <Badge key={f} variant="secondary" className="font-mono text-xs">
                      {f}
                    </Badge>
                  ))}
                </div>
              </div>
            ) : (
              <p className="text-muted-foreground text-sm">任务资产暂无可匹配的指纹词。</p>
            )}
            {total === 0 ? (
              <p className="text-muted-foreground text-sm">
                库内暂无命中本任务指纹的带 PoC 条目;随着采集持续更新,命中会自动增加。
              </p>
            ) : (
              <>
                <BriefingGroup title={`KEV 在野利用(${groups.kev.length})`} entries={groups.kev} />
                <BriefingGroup title={`高危(critical/high, ${groups.high.length})`} entries={groups.high} />
                <BriefingGroup title={`其他匹配(${groups.rest.length})`} entries={groups.rest} />
              </>
            )}
            <p className="text-muted-foreground text-xs">
              PoC 仅作验证参考,使用前严格遵守任务约束与授权范围;可到「漏洞库 / CVE 情报」页按指纹或 CVE 深查。
            </p>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
