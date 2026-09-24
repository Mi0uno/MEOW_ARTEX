"use client";

// 信息搜集 → 工具执行:手动跑一次收集器/测绘查询(结果直接返回,落执行历史
// + 原始输出留证),下方是执行历史。AI 自主调用这些工具的记录在「渗透测试 → 工具执行」。

import * as React from "react";

import { PlayIcon, RefreshCwIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type {
  Company,
  ExternalAPIConfig,
  ExternalAPIProviderDesc,
  RawReconAsset,
  ReconCollectorDesc,
  ToolExecution,
} from "@/lib/types";

// 内置非 API 工具的展示名;API 型收集器(16 家 provider)动态来自后端。
const FIXED_TOOL_LABEL: Record<string, string> = {
  manual: "文本导入",
  crtsh: "证书透明日志 (crt.sh)",
};

function fmtTime(iso?: string | null): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString();
}

export default function ReconExecutePage() {
  const [tool, setTool] = React.useState("fofa");
  const [collectors, setCollectors] = React.useState<ReconCollectorDesc[]>([]);
  const [providers, setProviders] = React.useState<ExternalAPIProviderDesc[]>([]);
  const [extApis, setExtApis] = React.useState<ExternalAPIConfig[]>([]);
  const [extApiId, setExtApiId] = React.useState<number | null>(null);
  const [query, setQuery] = React.useState("");
  const [size, setSize] = React.useState("50");
  const [manualText, setManualText] = React.useState("");
  const [crtshLimit, setCrtshLimit] = React.useState("50");
  const [companies, setCompanies] = React.useState<Company[]>([]);
  const [companyId, setCompanyId] = React.useState<number | null>(null);
  const [running, setRunning] = React.useState(false);
  const [result, setResult] = React.useState<{
    status: string;
    assets?: RawReconAsset[];
    stats?: Record<string, unknown>;
    error?: string;
  } | null>(null);
  const [history, setHistory] = React.useState<ToolExecution[]>([]);

  const loadHistory = React.useCallback(() => {
    api
      .reconExecutions(50)
      .then(setHistory)
      .catch(() => undefined);
  }, []);

  React.useEffect(() => {
    api.companies().then(setCompanies).catch(() => undefined);
    api
      .externalApis()
      .then((r) => {
        setExtApis(r.apis);
        setProviders(r.providers);
      })
      .catch(() => undefined);
    api.reconCollectors().then(setCollectors).catch(() => undefined);
    loadHistory();
  }, [loadHistory]);

  const isExt = tool === "fofa" || tool === "hunter" || collectors.some((c) => c.key === tool && c.kind === "api");
  const providerDef = providers.find((p) => p.key === tool);
  const extKind: "search" | "domain_enum" = providerDef?.kind === "domain_enum" ? "domain_enum" : "search";
  const usableApis = extApis.filter((a) => a.provider === tool && a.enabled);
  const toolLabel = (key: string) =>
    FIXED_TOOL_LABEL[key] ?? collectors.find((c) => c.key === key)?.title ?? key;

  const run = () => {
    const config: Record<string, unknown> = {};
    if (isExt) {
      if (!extApiId) {
        toast.error("请选择外部接口(在「信息搜集 → 外部接口」维护)");
        return;
      }
      config.external_api_id = extApiId;
      if (extKind === "search") {
        if (!query.trim()) {
          toast.error("请填写查询语句");
          return;
        }
        config.query = query.trim();
      } else if (query.trim()) {
        config.domain = query.trim();
      }
      if (Number(size) > 0) config.size = Number(size);
    }
    if (tool === "crtsh" && Number(crtshLimit) > 0) config.limit = Number(crtshLimit);
    if (tool === "manual") {
      if (!manualText.trim()) {
        toast.error("请填写导入内容");
        return;
      }
      config.text = manualText;
    }
    setRunning(true);
    setResult(null);
    api
      .reconExecute({ tool, config, companyId: companyId ?? undefined })
      .then((r) => {
        if (r.status === "failed") {
          toast.error(`执行失败：${r.error}`);
          setResult({ status: "failed", error: r.error });
        } else {
          setResult({ status: "done", assets: r.assets, stats: r.stats });
          toast.success(`执行完成,产出 ${r.assets?.length ?? 0} 条(结果仅前 200 条展示)`);
        }
        loadHistory();
      })
      .catch((e) => toast.error(`执行失败：${(e as Error).message}`))
      .finally(() => setRunning(false));
  };

  return (
    <div className="space-y-4 p-4 md:p-6">
      <div className="mb-2 flex items-center gap-2">
        <SidebarTrigger className="-ml-1" />
      </div>
      <div>
        <h1 className="font-semibold text-xl">工具执行</h1>
        <p className="text-muted-foreground text-sm">
          手动执行信息搜集工具,结果直接返回(不进暂存区);要落库请建步骤任务。AI 自主调用的记录见「渗透测试 → 工具执行」
        </p>
      </div>

      <Card>
        <CardHeader className="pb-3">
          <CardTitle className="text-base">执行</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid gap-4 md:grid-cols-2">
            <div className="space-y-1.5">
              <Label>工具</Label>
              <Select value={tool} onValueChange={setTool}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {collectors.map((c) => (
                      <SelectItem key={c.key} value={c.key}>
                        {toolLabel(c.key)}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label>关联企业(可选;域名枚举型数据源与 crt.sh 用其根域名)</Label>
              <Select
                value={companyId ? String(companyId) : "none"}
                onValueChange={(v) => setCompanyId(v === "none" ? null : Number(v))}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    <SelectItem value="none">不关联</SelectItem>
                    {companies.map((c) => (
                      <SelectItem key={c.id} value={String(c.id)}>
                        {c.name}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </div>
          </div>

          {isExt ? (
            <div className="space-y-4">
              <div className="space-y-1.5">
                <Label>外部接口({toolLabel(tool)})</Label>
                {usableApis.length === 0 ? (
                  <p className="text-muted-foreground text-sm">
                    没有启用的 {providerDef?.title ?? tool} 接口,请到「信息搜集 → 外部接口」配置
                  </p>
                ) : (
                  <Select value={extApiId ? String(extApiId) : ""} onValueChange={(v) => setExtApiId(Number(v))}>
                    <SelectTrigger>
                      <SelectValue placeholder="选择接口配置" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectGroup>
                        {usableApis.map((a) => (
                          <SelectItem key={a.id} value={String(a.id)}>
                            {a.name}({a.provider_title || a.provider})
                          </SelectItem>
                        ))}
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                )}
              </div>
              {extKind === "search" ? (
                <div className="space-y-1.5">
                  <Label>查询语句</Label>
                  <Textarea
                    className="font-mono text-xs"
                    rows={2}
                    placeholder='例: domain="example.com"'
                    value={query}
                    onChange={(e) => setQuery(e.target.value)}
                  />
                </div>
              ) : (
                <div className="space-y-1.5">
                  <Label>目标域名(留空 = 用关联企业的根域名)</Label>
                  <Input
                    className="font-mono text-xs"
                    placeholder="example.com"
                    value={query}
                    onChange={(e) => setQuery(e.target.value)}
                  />
                </div>
              )}
              <div className="space-y-1.5">
                <Label>拉取条数上限</Label>
                <Input type="number" min={1} className="w-40" value={size} onChange={(e) => setSize(e.target.value)} />
              </div>
            </div>
          ) : null}

          {tool === "manual" ? (
            <div className="space-y-1.5">
              <Label>导入内容(每行一个:域名 / IP[:端口] / URL)</Label>
              <Textarea
                className="font-mono text-xs"
                rows={4}
                value={manualText}
                onChange={(e) => setManualText(e.target.value)}
              />
            </div>
          ) : null}

          {tool === "crtsh" ? (
            <div className="space-y-1.5">
              <Label>产出上限(每域名)</Label>
              <Input type="number" min={1} className="w-40" value={crtshLimit} onChange={(e) => setCrtshLimit(e.target.value)} />
            </div>
          ) : null}

          <Button onClick={run} disabled={running}>
            <PlayIcon className="size-4" />
            {running ? "执行中…" : "执行"}
          </Button>

          {result ? (
            <div className="rounded-lg border p-3">
              {result.status === "failed" ? (
                <p className="text-red-600 text-sm">执行失败:{result.error}</p>
              ) : (
                <>
                  <div className="mb-2 flex items-center justify-between">
                    <span className="font-medium text-sm">结果 {result.assets?.length ?? 0} 条</span>
                    <span className="text-muted-foreground text-xs">{JSON.stringify(result.stats ?? {})}</span>
                  </div>
                  <div className="flex flex-wrap gap-1.5">
                    {(result.assets ?? []).slice(0, 100).map((a, i) => (
                      <span key={i} className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">
                        [{a.type}] {a.value}
                        {a.port ? `:${a.port}` : ""}
                      </span>
                    ))}
                  </div>
                </>
              )}
            </div>
          ) : null}
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="flex-row items-center justify-between space-y-0 pb-3">
          <CardTitle className="text-base">执行历史</CardTitle>
          <Button variant="outline" size="sm" onClick={() => void loadHistory()}>
            <RefreshCwIcon className="size-4" />
            刷新
          </Button>
        </CardHeader>
        <CardContent>
          {history.length === 0 ? (
            <p className="text-muted-foreground py-6 text-center text-sm">还没有执行记录</p>
          ) : (
            <div className="max-h-[50vh] overflow-y-auto">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="w-20">状态</TableHead>
                    <TableHead className="w-28">工具</TableHead>
                    <TableHead>结果</TableHead>
                    <TableHead className="w-40">时间</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {history.map((e) => (
                    <TableRow key={e.id}>
                      <TableCell>
                        <Badge
                          variant="outline"
                          className={
                            e.status === "done"
                              ? "bg-emerald-500/15 text-emerald-600 border-emerald-500/30"
                              : e.status === "failed"
                                ? "bg-red-500/15 text-red-600 border-red-500/30"
                                : "bg-muted text-muted-foreground"
                          }
                        >
                          {e.status === "done" ? "完成" : e.status === "failed" ? "失败" : "运行中"}
                        </Badge>
                      </TableCell>
                      <TableCell className="font-mono text-xs">{e.tool_key}</TableCell>
                      <TableCell className="text-sm">
                        {e.status === "failed"
                          ? String(e.summary?.error ?? "—")
                          : `产出 ${e.summary?.total ?? 0} 条`}
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs">{fmtTime(e.created_at)}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
