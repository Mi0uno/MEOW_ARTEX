"use client";

// SRC 模式 · 平台批量:按 SRC 平台批量建任务。
// 一个 SRC 平台通常对应多家企业(如教育 SRC 下多所高校),这里按平台聚合,
// 勾选多家企业一次性建 SRC 任务(每家一条),省去逐个进入企业模式。

import * as React from "react";

import { useRouter } from "next/navigation";
import { LayersIcon, RocketIcon, SearchIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { TablePagination } from "@/components/table-pagination";
import { api } from "@/lib/api";
import type { SRCProfile } from "@/lib/types";

const PAGE_SIZE = 20;

// 平台分组:同一 SRC 平台下的全部企业。
interface PlatformGroup {
  platform: string;
  profiles: SRCProfile[];
}

export default function SRCBatchPage() {
  const router = useRouter();
  const [profiles, setProfiles] = React.useState<SRCProfile[]>([]);
  const [total, setTotal] = React.useState(0);
  const [query, setQuery] = React.useState("");
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(PAGE_SIZE);
  const [loading, setLoading] = React.useState(true);
  const [selected, setSelected] = React.useState<Set<number>>(new Set());
  const [creating, setCreating] = React.useState(false);

  const load = React.useCallback((q: string, p: number, size: number) => {
    setLoading(true);
    api
      .srcProfiles({ q, limit: size, offset: (p - 1) * size })
      .then((r) => {
        setProfiles(r.profiles);
        setTotal(r.total);
      })
      .catch((e) => toast.error(`加载失败:${(e as Error).message}`))
      .finally(() => setLoading(false));
  }, []);

  React.useEffect(() => {
    load(query, page, pageSize);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [page, pageSize]);

  const search = () => {
    setPage(1);
    load(query, 1, pageSize);
  };

  // 按平台分组(空平台归入「未指定平台」)。
  const groups = React.useMemo<PlatformGroup[]>(() => {
    const m = new Map<string, SRCProfile[]>();
    for (const p of profiles) {
      const key = p.platform?.trim() || "未指定平台";
      if (!m.has(key)) m.set(key, []);
      m.get(key)!.push(p);
    }
    return [...m.entries()].map(([platform, ps]) => ({ platform, profiles: ps }));
  }, [profiles]);

  const toggle = (companyID: number) => {
    setSelected((s) => {
      const n = new Set(s);
      if (n.has(companyID)) n.delete(companyID);
      else n.add(companyID);
      return n;
    });
  };

  const toggleGroup = (g: PlatformGroup) => {
    const ids = g.profiles.map((p) => p.company_id);
    const allOn = ids.every((id) => selected.has(id));
    setSelected((s) => {
      const n = new Set(s);
      for (const id of ids) {
        if (allOn) n.delete(id);
        else n.add(id);
      }
      return n;
    });
  };

  // 批量为选中企业建 SRC 任务(每家一条,带 SRC 模式标记与规范约束)。
  const batchCreate = async () => {
    if (selected.size === 0) {
      toast.error("请先勾选企业");
      return;
    }
    setCreating(true);
    const created: string[] = [];
    const failed: string[] = [];
    for (const cid of selected) {
      const prof = profiles.find((p) => p.company_id === cid);
      const name = prof?.company_name || `企业 #${cid}`;
      try {
        const t = await api.createTask({
          name: `SRC · ${name}`,
          description: `SRC 模式批量任务 · ${name}`,
          goal: `在 SRC 授权范围内对 ${name} 进行漏洞挖掘并产出可提交成果`,
          companyIds: [cid],
          stage: "exploit",
          srcMode: true,
        });
        created.push(t.id);
      } catch (e) {
        failed.push(`${name}(${(e as Error).message})`);
      }
    }
    setCreating(false);
    if (created.length > 0) {
      toast.success(`已创建 ${created.length} 条 SRC 任务`);
      setSelected(new Set());
      router.push("/function/tasks?stage=exploit");
    }
    if (failed.length > 0) {
      toast.error(`${failed.length} 条失败:${failed.slice(0, 3).join(";")}`);
    }
  };

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div>
          <h1 className="font-semibold text-xl">SRC 模式 · 平台批量</h1>
          <p className="text-muted-foreground text-sm">
            按 SRC 平台聚合企业,勾选多家一次性批量建 SRC 任务(每家一条,自动带上该企业的 SRC 规范约束)
          </p>
        </div>
        <Button size="sm" disabled={creating || selected.size === 0} onClick={() => void batchCreate()}>
          <RocketIcon className="size-4" />
          {creating ? "创建中…" : `批量建任务(${selected.size})`}
        </Button>
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
                placeholder="搜索企业 / 平台"
                className="pl-8"
              />
            </div>
            <Button variant="outline" size="sm" onClick={search}>
              搜索
            </Button>
            <span className="text-muted-foreground ml-auto text-xs tabular-nums">
              共 {total} 家企业 · 已选 {selected.size}
            </span>
          </div>

          {loading && profiles.length === 0 ? (
            <p className="text-muted-foreground py-16 text-center text-sm">加载中…</p>
          ) : profiles.length === 0 ? (
            <div className="text-muted-foreground flex flex-col items-center gap-2 py-16 text-center text-sm">
              <LayersIcon className="size-8 opacity-40" />
              <span>还没有企业配置 SRC 规范。先到「企业模式」为几家企业配置规范与平台,再回来批量建任务。</span>
            </div>
          ) : (
            <>
              <Table>
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    <TableHead className="w-10" />
                    <TableHead className="w-48">SRC 平台</TableHead>
                    <TableHead>企业</TableHead>
                    <TableHead className="w-24">状态</TableHead>
                    <TableHead className="w-64">授权范围</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {groups.map((g) => (
                    <React.Fragment key={g.platform}>
                      <TableRow className="bg-muted/40 hover:bg-muted/40">
                        <TableCell>
                          <input
                            type="checkbox"
                            aria-label={`选择平台 ${g.platform} 全部企业`}
                            checked={g.profiles.every((p) => selected.has(p.company_id))}
                            onChange={() => toggleGroup(g)}
                          />
                        </TableCell>
                        <TableCell className="font-medium">
                          {g.platform}
                          <Badge variant="outline" className="ml-2 text-xs">
                            {g.profiles.length} 家
                          </Badge>
                        </TableCell>
                        <TableCell colSpan={3} className="text-muted-foreground text-xs">
                          勾选平台行可一次选中该平台下全部企业
                        </TableCell>
                      </TableRow>
                      {g.profiles.map((p) => (
                        <TableRow key={p.id}>
                          <TableCell>
                            <input
                              type="checkbox"
                              aria-label={`选择 ${p.company_name}`}
                              checked={selected.has(p.company_id)}
                              onChange={() => toggle(p.company_id)}
                            />
                          </TableCell>
                          <TableCell />
                          <TableCell className="font-medium">{p.company_name || `企业 #${p.company_id}`}</TableCell>
                          <TableCell>
                            <Badge
                              variant="outline"
                              className={p.enabled ? "text-emerald-600" : "text-muted-foreground"}
                            >
                              {p.enabled ? "已启用" : "已停用"}
                            </Badge>
                          </TableCell>
                          <TableCell className="text-muted-foreground max-w-64 truncate text-xs">
                            {p.scope_note || "—"}
                          </TableCell>
                        </TableRow>
                      ))}
                    </React.Fragment>
                  ))}
                </TableBody>
              </Table>
              <TablePagination
                page={page}
                pageSize={pageSize}
                total={total}
                onPageChange={setPage}
                onPageSizeChange={(s) => {
                  setPage(1);
                  setPageSize(s);
                }}
              />
            </>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
