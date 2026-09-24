"use client";

// 新建信息搜集任务:选企业即可,数据源由系统自动检测。
//
// 设计要点(2026-09-24 重构):
//   - 不再让用户手选收集步骤、手写查询语句:系统自动检测「已配置且已启用」的外部
//     接口,全部纳入搜集范围;search 型数据源的 query 由 AI 依企业资产范围生成。
//   - 「自动检测」预览在选定企业后立即展示:哪些数据源会跑、哪些因未配置跳过、
//     AI 是否为它们生成了查询语句。目的就是最全面地搜集暴露面。
//   - 手动录入(粘贴已有清单)与单步调试仍保留在高级区,不干扰主流程。

import * as React from "react";

import Link from "next/link";
import { useRouter } from "next/navigation";
import {
  ArrowLeftIcon,
  CheckCircle2Icon,
  Loader2Icon,
  RefreshCwIcon,
  RocketIcon,
  SparklesIcon,
  XCircleIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { api } from "@/lib/api";
import type { Company, ReconPlan } from "@/lib/types";

export default function CreateReconTaskPage() {
  const router = useRouter();
  const [companies, setCompanies] = React.useState<Company[]>([]);
  const [name, setName] = React.useState("");
  const [description, setDescription] = React.useState("");
  const [companyIds, setCompanyIds] = React.useState<number[]>([]);
  const [manualText, setManualText] = React.useState("");
  const [submitting, setSubmitting] = React.useState(false);

  // 自动检测计划:选定企业后拉取,展示将自动调用的数据源。
  const [plan, setPlan] = React.useState<ReconPlan | null>(null);
  const [planning, setPlanning] = React.useState(false);
  const [useAI, setUseAI] = React.useState(true);

  React.useEffect(() => {
    api
      .companies()
      .then(setCompanies)
      .catch((e) => toast.error(`读取企业失败：${e.message}`));
  }, []);

  const loadPlan = React.useCallback(
    (ids: number[], ai: boolean) => {
      if (ids.length === 0) {
        setPlan(null);
        return;
      }
      setPlanning(true);
      api
        .reconPlan(ids, ai)
        .then(setPlan)
        .catch((e) => {
          toast.error(`自动检测数据源失败：${(e as Error).message}`);
          setPlan(null);
        })
        .finally(() => setPlanning(false));
    },
    [],
  );

  // 企业或 AI 开关变化时重新检测。
  React.useEffect(() => {
    loadPlan(companyIds, useAI);
  }, [companyIds, useAI, loadPlan]);

  const toggleCompany = (id: number) => {
    setCompanyIds((prev) => (prev.includes(id) ? prev.filter((v) => v !== id) : [...prev, id]));
  };

  const submit = async () => {
    if (companyIds.length === 0) {
      toast.error("请至少选择一个企业作为目标");
      return;
    }
    if (!plan || plan.available === 0) {
      toast.error("没有可用的数据源：请先在「信息搜集 → 外部接口」配置并启用至少一个接口");
      return;
    }
    setSubmitting(true);
    try {
      const t = await api.createTask({
        name: name.trim() || undefined,
        description: description.trim() || `信息搜集：${plan.target_name || "资产暴露面"}`,
        goal: "全面收集企业暴露面资产(域名/子域名/IP/服务)",
        companyIds,
        stage: "recon",
        // 不传 step_key:走「自动检测模式」——服务端按可用数据源全量执行。
      });
      toast.success(`信息搜集任务 #${t.id} 已创建,将自动调用 ${plan.available} 个数据源`);
      router.push(`/function/recon/detail?id=${t.id}`);
    } catch (e) {
      toast.error(`创建失败：${(e as Error).message}`);
    } finally {
      setSubmitting(false);
    }
  };

  // 手动录入单独建一条任务(它需要人给文本,不参与自动检测)。
  const submitManual = async () => {
    if (companyIds.length === 0) {
      toast.error("请至少选择一个企业作为目标");
      return;
    }
    if (!manualText.trim()) {
      toast.error("手动录入内容为空");
      return;
    }
    setSubmitting(true);
    try {
      const t = await api.createTask({
        name: name.trim() || undefined,
        description: description.trim() || "信息搜集：手动录入清单",
        goal: "收集企业攻击面资产(域名/子域名/IP/服务)",
        companyIds,
        stage: "recon",
        stepKey: "manual",
        stepConfig: { text: manualText },
      });
      toast.success(`手动录入任务 #${t.id} 已创建`);
      router.push(`/function/recon/detail?id=${t.id}`);
    } catch (e) {
      toast.error(`创建失败：${(e as Error).message}`);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex items-center gap-2">
        <SidebarTrigger />
        <Link href="/function/recon">
          <Button variant="ghost" size="sm">
            <ArrowLeftIcon className="size-4" />
            返回列表
          </Button>
        </Link>
      </div>
      <div className="mx-auto max-w-3xl space-y-4">
        <div>
          <h1 className="font-semibold text-xl">新建信息搜集任务</h1>
          <p className="text-muted-foreground text-sm">
            选企业即可：系统自动检测已启用的外部接口并全部调用，AI 依资产范围生成查询语句，产出进暂存区
          </p>
        </div>

        <Card>
          <CardHeader className="pb-3">
            <CardTitle className="text-base">目标</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="recon-task-name">任务名称(可选)</Label>
              <Input
                id="recon-task-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="例:Q4 子域盘点"
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="recon-task-desc">任务描述(可选)</Label>
              <Textarea
                id="recon-task-desc"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder="这轮信息搜集的目的、范围说明"
                rows={2}
              />
            </div>
            <div className="space-y-1.5">
              <Label>目标企业</Label>
              {companies.length === 0 ? (
                <p className="text-muted-foreground text-sm">
                  还没有企业，请先在「资产」页新增企业并录入根域名范围
                </p>
              ) : (
                <div className="flex flex-wrap gap-2">
                  {companies.map((c) => (
                    <button
                      key={c.id}
                      type="button"
                      onClick={() => toggleCompany(c.id)}
                      className={`rounded-full border px-3 py-1 text-xs transition-colors ${
                        companyIds.includes(c.id) ? "border-primary bg-primary text-primary-foreground" : "hover:bg-muted"
                      }`}
                    >
                      {c.name}
                    </button>
                  ))}
                </div>
              )}
            </div>
          </CardContent>
        </Card>

        {/* 自动检测:将自动调用的数据源清单 */}
        <Card>
          <CardHeader className="pb-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <CardTitle className="text-base">自动检测的数据源</CardTitle>
              <div className="flex items-center gap-2">
                <Button
                  variant={useAI ? "default" : "outline"}
                  size="sm"
                  onClick={() => setUseAI((v) => !v)}
                  title="用 AI 依企业资产范围生成查询语句(失败自动回退内置默认语句)"
                >
                  <SparklesIcon className="size-3.5" />
                  {useAI ? "AI 生成查询" : "内置默认查询"}
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={planning || companyIds.length === 0}
                  onClick={() => loadPlan(companyIds, useAI)}
                >
                  {planning ? <Loader2Icon className="size-3.5 animate-spin" /> : <RefreshCwIcon className="size-3.5" />}
                  重新检测
                </Button>
              </div>
            </div>
          </CardHeader>
          <CardContent>
            {companyIds.length === 0 ? (
              <p className="text-muted-foreground py-6 text-center text-sm">先选择目标企业，系统会自动检测可用数据源</p>
            ) : planning ? (
              <p className="text-muted-foreground flex items-center justify-center gap-2 py-6 text-sm">
                <Loader2Icon className="size-4 animate-spin" />
                正在检测数据源{useAI ? "并由 AI 生成查询语句" : ""}…
              </p>
            ) : !plan ? (
              <p className="text-muted-foreground py-6 text-center text-sm">检测失败，点「重新检测」重试</p>
            ) : (
              <div className="space-y-3">
                <div className="text-muted-foreground flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
                  <span className="text-foreground font-medium">将调用 {plan.available} 个数据源</span>
                  {plan.skipped > 0 && <span>{plan.skipped} 个因未配置跳过</span>}
                  {plan.domains.length > 0 && <span>目标域名：{plan.domains.join(", ")}</span>}
                  {plan.ai_used && (
                    <Badge variant="outline" className="gap-1 text-blue-600">
                      <SparklesIcon className="size-3" />
                      AI 已生成查询语句
                    </Badge>
                  )}
                </div>
                {plan.ai_note && <p className="text-amber-600 text-xs">{plan.ai_note}</p>}
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead className="w-8" />
                      <TableHead>数据源</TableHead>
                      <TableHead className="w-24">类型</TableHead>
                      <TableHead>查询 / 跳过原因</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {plan.sources.map((s) => {
                      const ok = !s.skip_reason;
                      return (
                        <TableRow key={s.provider}>
                          <TableCell>
                            {ok ? (
                              <CheckCircle2Icon className="size-4 text-emerald-600" />
                            ) : (
                              <XCircleIcon className="size-4 text-muted-foreground" />
                            )}
                          </TableCell>
                          <TableCell className="font-medium">{s.title}</TableCell>
                          <TableCell className="text-muted-foreground text-xs">
                            {s.kind === "domain_enum" ? "域名枚举" : "服务搜索"}
                          </TableCell>
                          <TableCell className="text-xs">
                            {ok ? (
                              <span className="flex items-center gap-1.5">
                                {s.query ? (
                                  <code className="bg-muted rounded px-1 py-0.5 font-mono">{s.query}</code>
                                ) : (
                                  <span className="text-muted-foreground">按目标域名枚举</span>
                                )}
                                {s.query_source === "ai" && (
                                  <Badge variant="outline" className="px-1 py-0 text-[10px] text-blue-600">
                                    AI
                                  </Badge>
                                )}
                              </span>
                            ) : (
                              <span className="text-muted-foreground">{s.skip_reason}</span>
                            )}
                          </TableCell>
                        </TableRow>
                      );
                    })}
                  </TableBody>
                </Table>
              </div>
            )}
          </CardContent>
        </Card>

        {/* 高级:手动录入清单(单独一条任务) */}
        <Card>
          <CardHeader className="pb-3">
            <CardTitle className="text-base">手动录入清单(可选)</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            <Textarea
              className="font-mono text-xs"
              rows={4}
              placeholder={"每行一个目标：域名 / IP[:端口] / URL\n例:\nexample.com\n1.2.3.4:8080\nhttps://admin.example.com"}
              value={manualText}
              onChange={(e) => setManualText(e.target.value)}
            />
            <div className="flex items-center justify-between">
              <p className="text-muted-foreground text-xs"># 开头的行视为注释；域名自动大小写归一</p>
              <Button variant="outline" size="sm" disabled={submitting || !manualText.trim()} onClick={submitManual}>
                单独创建录入任务
              </Button>
            </div>
          </CardContent>
        </Card>

        <div className="flex items-center justify-end gap-2">
          <Button onClick={submit} disabled={submitting || planning || !plan || plan.available === 0}>
            <RocketIcon className="size-4" />
            {submitting ? "创建中…" : "创建并自动搜集"}
          </Button>
        </div>
      </div>
    </div>
  );
}
