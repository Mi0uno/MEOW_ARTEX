"use client";

// Decision:决策引擎(前置决策模型)的列表页。
//
// 引擎集合是后端静态定义的(JEV / Laya),不能新建也不能删除,所以这一页不做增删,
// 只做检索 + 状态总览 + 高频小改:分类器按启用状态切、筛选器按可用性切、
// 搜索覆盖名称/key/模型/地址;点引擎名进入详情页做完整编辑,行内保留快速编辑与连通测试。
//
// 列宽说明(踩过的坑):table-fixed 下名称列不设宽度,自动吃掉余量;其余列按最小可读值给。
// 这里固定列合计 720px(容器约 897px),名称列仍有约 177px,不会把名称压到看不见。

import * as React from "react";

import Link from "next/link";

import { ArrowRightIcon, Loader2Icon, PencilIcon, PlugZapIcon, SearchIcon, ServerCogIcon } from "lucide-react";
import { toast } from "sonner";

import { TablePagination } from "@/components/table-pagination";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { DecisionEngine } from "@/lib/types";

import { draftFromEngine, runEngineTest } from "./_components/engine-form";
import { QuickEditDialog } from "./_components/quick-edit-dialog";

const PAGE_SIZE = 20;

// 可用性筛选:可用性来自引擎静态定义,只有"协议已接入"与"协议预留"两种。
const AVAILABILITY_LABEL: Record<string, string> = {
  all: "全部可用性",
  available: "可用",
  reserved: "预留位",
};

// matchQuery 做大小写无关的多字段模糊匹配:名称 / key / 模型 / API 地址 / 说明。
// 这些都是用户可能"记得个大概"的检索入口,漏掉任一个都会让人觉得搜不到。
function matchQuery(e: DecisionEngine, q: string): boolean {
  const needle = q.trim().toLowerCase();
  if (!needle) return true;
  return [e.title, e.key, e.model, e.base_url, e.description]
    .filter(Boolean)
    .some((v) => v.toLowerCase().includes(needle));
}

export default function DecisionPage() {
  const [engines, setEngines] = React.useState<DecisionEngine[]>([]);
  const [loaded, setLoaded] = React.useState(false);

  const [query, setQuery] = React.useState(""); // 输入框内容
  const [applied, setApplied] = React.useState(""); // 已提交的搜索词(回车/点按钮才生效)
  const [state, setState] = React.useState("all"); // 分类器:全部 / 已启用 / 未启用
  const [availability, setAvailability] = React.useState("all"); // 筛选器:全部 / 可用 / 预留位
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);

  const [editing, setEditing] = React.useState<DecisionEngine | null>(null);
  const [dialogOpen, setDialogOpen] = React.useState(false);
  const [testingKey, setTestingKey] = React.useState("");
  const [togglingKey, setTogglingKey] = React.useState("");

  const load = React.useCallback(async () => {
    try {
      setEngines(await api.decisionEngines());
    } catch (e) {
      toast.error(`加载决策引擎失败: ${(e as Error).message}`);
    } finally {
      setLoaded(true);
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  // 分类计数取自未过滤的全量:若先按状态过滤再计数,切到"已启用"后"未启用"的计数会归零,
  // 用户就再也看不到另一类有多少条了。
  const counts = React.useMemo(
    () => ({
      all: engines.length,
      enabled: engines.filter((e) => e.enabled).length,
      disabled: engines.filter((e) => !e.enabled).length,
    }),
    [engines],
  );

  const filtered = React.useMemo(
    () =>
      engines.filter((e) => {
        if (state === "enabled" && !e.enabled) return false;
        if (state === "disabled" && e.enabled) return false;
        if (availability === "available" && !e.available) return false;
        if (availability === "reserved" && e.available) return false;
        return matchQuery(e, applied);
      }),
    [engines, state, availability, applied],
  );

  // 引擎数量少,分页纯客户端:过滤后切片即可,不需要后端分页参数。
  const pageRows = React.useMemo(
    () => filtered.slice((page - 1) * pageSize, page * pageSize),
    [filtered, page, pageSize],
  );

  const filtering = Boolean(applied) || state !== "all" || availability !== "all";

  const search = () => {
    setPage(1);
    setApplied(query);
  };

  const resetFilters = () => {
    setQuery("");
    setApplied("");
    setState("all");
    setAvailability("all");
    setPage(1);
  };

  const toggleEnabled = async (engine: DecisionEngine, next: boolean) => {
    setTogglingKey(engine.key);
    try {
      await api.updateDecisionEngine(engine.key, { enabled: next });
      toast.success(next ? `已启用「${engine.title}」` : `已停用「${engine.title}」`);
      await load();
    } catch (e) {
      toast.error(`操作失败:${(e as Error).message}`);
    } finally {
      setTogglingKey("");
    }
  };

  // 行内连通测试:直接用当前已保存的配置探测(列表页没有未保存的表单状态),
  // 结果以 toast 反馈,详细面板在详情页。
  const testEngine = async (engine: DecisionEngine) => {
    setTestingKey(engine.key);
    try {
      const r = await runEngineTest(engine, draftFromEngine(engine));
      if (r.status === "ok") {
        toast.success(`「${engine.title}」连通正常${typeof r.latencyMs === "number" ? `(${r.latencyMs}ms)` : ""}`);
      } else {
        toast.error(`「${engine.title}」连通失败:${r.error ?? "未知错误"}`);
      }
    } catch (e) {
      toast.error(`测试失败:${(e as Error).message}`);
    } finally {
      setTestingKey("");
    }
  };

  // 三态正文单独算出来:加载中 / 两种空态 / 表格。写成嵌套三元会缩进到看不见,
  // 且"确实没有引擎"与"筛选无命中"的出口不同,拆开后各自的文案与按钮一目了然。
  let body: React.ReactNode;
  if (!loaded) {
    body = <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>;
  } else if (engines.length === 0) {
    body = (
      <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
        <ServerCogIcon className="size-8 opacity-40" />
        <span>后端未返回任何决策引擎定义,请检查服务端 decisionEngines 配置。</span>
      </div>
    );
  } else if (filtered.length === 0) {
    body = (
      <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
        <ServerCogIcon className="size-8 opacity-40" />
        <span>{applied ? `没有匹配「${applied}」的引擎` : "当前筛选条件下没有引擎"}</span>
        <Button variant="outline" size="sm" onClick={resetFilters}>
          清除筛选
        </Button>
      </div>
    );
  } else {
    body = (
      <>
        <Table className="table-fixed">
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              {/* 引擎名称列不设宽度:table-fixed 下自动吃掉余量(设 w-full 会挤掉后面的列)。
                  固定列合计 736px,容器约 897px,名称列仍有约 161px 可读空间(范本 proxy/groups
                  用了 768px 固定宽仍正常,这里再留宽些)。
                  操作列给 w-24 而不是 w-20:三个 icon-xs 按钮(24px×3)+ 间距 + 单元格内边距
                  实测需要约 92px,w-20 的 80px 会挤到换行。 */}
              <TableHead>引擎名称</TableHead>
              <TableHead className="w-20">标识</TableHead>
              <TableHead className="w-32">模型</TableHead>
              <TableHead className="w-44">API 地址</TableHead>
              <TableHead className="w-20">凭据</TableHead>
              <TableHead className="w-20">可用性</TableHead>
              <TableHead className="w-24">启用状态</TableHead>
              <TableHead className="w-24">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {pageRows.map((engine) => (
              <TableRow key={engine.key}>
                {/* 名称即入口:进入详情页做完整编辑与连通性诊断。 */}
                <TableCell className="min-w-0">
                  <Link
                    href={`/system/decision/detail?key=${encodeURIComponent(engine.key)}`}
                    className="block w-full truncate text-left font-medium hover:underline"
                    title={`${engine.title} — ${engine.description}`}
                  >
                    {engine.title}
                  </Link>
                </TableCell>
                <TableCell className="font-mono text-xs">{engine.key}</TableCell>
                <TableCell className="min-w-0">
                  <span className="text-muted-foreground block truncate font-mono text-xs" title={engine.model}>
                    {engine.model || engine.default_model || "—"}
                  </span>
                </TableCell>
                <TableCell className="min-w-0">
                  <span className="text-muted-foreground block truncate text-xs" title={engine.base_url}>
                    {engine.base_url || "—"}
                  </span>
                </TableCell>
                <TableCell>
                  {/* 凭据只显示是否已配置,明文永不出后端。 */}
                  <Badge
                    variant="outline"
                    className={
                      engine.api_key_set
                        ? "border-emerald-500/30 bg-emerald-500/15 text-emerald-600"
                        : "bg-muted text-muted-foreground"
                    }
                  >
                    {engine.api_key_set ? "已配置" : "未配置"}
                  </Badge>
                </TableCell>
                <TableCell>
                  <Badge
                    variant="outline"
                    className={
                      engine.available
                        ? "border-blue-500/30 bg-blue-500/15 text-blue-600"
                        : "border-amber-500/30 bg-amber-500/15 text-amber-600"
                    }
                    title={engine.available ? "协议已接入,可参与前置判定" : "协议预留位:可配置,接入后才参与判定"}
                  >
                    {engine.available ? "可用" : "预留位"}
                  </Badge>
                </TableCell>
                <TableCell>
                  <div className="flex items-center gap-1.5">
                    <Switch
                      size="sm"
                      checked={engine.enabled}
                      disabled={togglingKey === engine.key}
                      onCheckedChange={(v) => void toggleEnabled(engine, v)}
                      aria-label={`${engine.enabled ? "停用" : "启用"}${engine.title}`}
                      title={engine.available ? "启用后参与前置决策流水线" : "协议预留位:启用仅记录意图,接入后生效"}
                    />
                    <Badge
                      variant="outline"
                      className={
                        engine.enabled
                          ? "border-violet-500/30 bg-violet-500/15 text-violet-600"
                          : "bg-muted text-muted-foreground"
                      }
                    >
                      {engine.enabled ? "启用" : "停用"}
                    </Badge>
                  </div>
                </TableCell>
                <TableCell>
                  <div className="flex items-center gap-0.5">
                    <Button
                      variant="ghost"
                      size="icon-xs"
                      title="快速编辑(端点 / 模型 / 凭据)"
                      onClick={() => {
                        setEditing(engine);
                        setDialogOpen(true);
                      }}
                    >
                      <PencilIcon className="size-3.5" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon-xs"
                      title={engine.api_key_set ? "测试连通性" : "先配置 API Key 才能测试"}
                      disabled={!engine.api_key_set || testingKey === engine.key}
                      onClick={() => void testEngine(engine)}
                    >
                      {testingKey === engine.key ? (
                        <Loader2Icon className="size-3.5 animate-spin" />
                      ) : (
                        <PlugZapIcon className="size-3.5" />
                      )}
                    </Button>
                    <Button variant="ghost" size="icon-xs" asChild title="进入详情页">
                      <Link
                        href={`/system/decision/detail?key=${encodeURIComponent(engine.key)}`}
                        aria-label={`进入 ${engine.title} 详情页`}
                      >
                        <ArrowRightIcon className="size-3.5" />
                      </Link>
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
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
      </>
    );
  }

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div>
          <h1 className="font-semibold text-xl">Decision</h1>
          <p className="text-muted-foreground text-sm">
            决策模型(前置决策引擎)配置:JEV / Laya 等在拦截规则未命中时做快速结构化安全评估;启用与流水线编排见{" "}
            <Link
              href="/system/ai-guard"
              className="underline decoration-dotted underline-offset-2 hover:text-foreground"
            >
              监管&规则 · AI监管
            </Link>
          </p>
        </div>
      </div>

      <Card>
        <CardContent className="px-0 pt-4">
          <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
            <div className="relative w-full sm:max-w-xs">
              <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
              <Input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && search()}
                placeholder="搜索名称 / key / 模型 / API 地址"
                className="pl-8"
              />
            </div>
            <Button variant="outline" size="sm" onClick={search}>
              搜索
            </Button>

            <Select
              value={availability}
              onValueChange={(v) => {
                setPage(1);
                setAvailability(v);
              }}
            >
              <SelectTrigger className="w-32">
                <SelectValue placeholder="可用性" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  {Object.entries(AVAILABILITY_LABEL).map(([v, label]) => (
                    <SelectItem key={v} value={v}>
                      {label}
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>

            {/* 分类器:可点页签,计数来自未过滤全量。 */}
            <Tabs
              value={state}
              onValueChange={(v) => {
                setPage(1);
                setState(v);
              }}
            >
              <TabsList>
                <TabsTrigger value="all">全部 {counts.all}</TabsTrigger>
                <TabsTrigger value="enabled">已启用 {counts.enabled}</TabsTrigger>
                <TabsTrigger value="disabled">未启用 {counts.disabled}</TabsTrigger>
              </TabsList>
            </Tabs>

            {filtering ? (
              <Button variant="ghost" size="sm" className="text-muted-foreground" onClick={resetFilters}>
                清除筛选
              </Button>
            ) : null}

            <span className="text-muted-foreground ml-auto text-xs tabular-nums">
              {filtering ? `命中 ${filtered.length} / ` : ""}共 {counts.all} 个引擎
            </span>
          </div>

          {body}
        </CardContent>
      </Card>

      <QuickEditDialog
        engine={editing}
        open={dialogOpen}
        onOpenChange={(o) => {
          setDialogOpen(o);
          if (!o) setEditing(null);
        }}
        onSaved={() => void load()}
      />
    </div>
  );
}
