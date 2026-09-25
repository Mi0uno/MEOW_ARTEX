"use client";

// C2 中心 · 总览 —— beacon 会话、监听器与任务队列的一屏仪表盘。
//
// 一条请求(api.c2Overview)取齐:会话三态计数、监听器启用数、排队任务数、
// 最近 10 个上线会话与最近 10 条任务。10s 轮询(仿 vps/page.tsx):
// 失败时保留上一份快照,只有从未成功过才 toast,避免轮询刷屏。

import * as React from "react";

import Link from "next/link";

import {
  ArrowRightIcon,
  GemIcon,
  ListTodoIcon,
  MonitorSmartphoneIcon,
  PackageOpenIcon,
  RadioIcon,
  TimerIcon,
} from "lucide-react";
import { toast } from "sonner";

import {
  c2SessionStatusMeta,
  c2TaskInputText,
  c2TaskKindLabel,
  c2TaskStatusMeta,
  fmtTime,
  num,
  relTime,
} from "@/components/c2-shared";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { C2Overview } from "@/lib/types";
import { cn } from "@/lib/utils";

// 空库/接口失败时的零值兜底,保证首帧与失败态都不白屏。
const EMPTY_OVERVIEW: C2Overview = {
  sessions_online: 0,
  sessions_offline: 0,
  sessions_dead: 0,
  listeners_enabled: 0,
  listeners_total: 0,
  tasks_queued: 0,
  recent_sessions: [],
  recent_tasks: [],
};

// 汇总卡:数字 + 说明,正数按语气着色(仿 vps 仪表盘 StatCard)。
function StatCard({
  label,
  value,
  sub,
  tone,
}: {
  label: string;
  value: number;
  sub?: string;
  tone?: "ok" | "bad" | "warn" | "info";
}) {
  return (
    <Card>
      <CardContent className="pt-4">
        <p className="text-muted-foreground text-xs">{label}</p>
        <p
          className={cn(
            "text-2xl font-semibold tabular-nums",
            tone === "ok" && value > 0 && "text-emerald-600 dark:text-emerald-400",
            tone === "bad" && value > 0 && "text-red-600 dark:text-red-400",
            tone === "warn" && value > 0 && "text-amber-600 dark:text-amber-400",
            tone === "info" && value > 0 && "text-blue-600 dark:text-blue-400",
          )}
        >
          {value}
        </p>
        {sub ? <p className="text-muted-foreground text-xs">{sub}</p> : null}
      </CardContent>
    </Card>
  );
}

export default function C2OverviewPage() {
  const [overview, setOverview] = React.useState<C2Overview | null>(null);
  const [updatedAt, setUpdatedAt] = React.useState("");
  // 只有从未成功过才报错:轮询失败保留上次数据,不 toast 刷屏。
  const loadedRef = React.useRef(false);

  const load = React.useCallback(() => {
    api
      .c2Overview()
      .then((d) => {
        setOverview(d);
        setUpdatedAt(new Date().toISOString());
        loadedRef.current = true;
      })
      .catch((e) => {
        // 保留上一份快照;只有从未成功过才提示(避免轮询刷屏)。
        if (!loadedRef.current) toast.error(`加载 C2 总览失败:${(e as Error).message}`);
      });
  }, []);

  React.useEffect(() => {
    load();
    const t = setInterval(load, 10_000);
    return () => clearInterval(t);
  }, [load]);

  const d = overview ?? EMPTY_OVERVIEW;

  return (
    <div className="p-4 md:p-6">
      <div className="mb-4 flex flex-wrap items-start justify-between gap-2">
        <div>
          <h1 className="font-semibold text-xl">C2 中心 · 总览</h1>
          <p className="text-muted-foreground text-sm">
            后渗透阶段的指挥控制:自研 HTTP beacon 回连、监听器与会话管理、命令下发与文件传输、战利品归集。
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-muted-foreground text-xs tabular-nums">
            {updatedAt ? `更新于 ${fmtTime(updatedAt)}` : ""}
          </span>
          <Button asChild variant="outline" size="sm">
            <Link href="/function/c2/listeners">
              <RadioIcon className="size-4" />
              监听器
            </Link>
          </Button>
          <Button asChild variant="outline" size="sm">
            <Link href="/function/c2/sessions">
              <MonitorSmartphoneIcon className="size-4" />
              会话
            </Link>
          </Button>
          <Button asChild variant="outline" size="sm">
            <Link href="/function/c2/payloads">
              <PackageOpenIcon className="size-4" />
              载荷
            </Link>
          </Button>
          <Button asChild variant="outline" size="sm">
            <Link href="/function/c2/loot">
              <GemIcon className="size-4" />
              战利品
            </Link>
          </Button>
        </div>
      </div>

      {/* ── 状态统计卡 ── */}
      <div className="mb-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
        <StatCard label="在线会话" value={num(d.sessions_online)} tone="ok" sub="心跳正常的主机" />
        <StatCard label="失联会话" value={num(d.sessions_offline)} tone="warn" sub="超时未归,可能恢复" />
        <StatCard label="终止会话" value={num(d.sessions_dead)} tone="bad" sub="已下发退出指令" />
        <StatCard
          label="启用监听器"
          value={num(d.listeners_enabled)}
          tone="info"
          sub={`共 ${num(d.listeners_total)} 个监听器`}
        />
        <StatCard label="排队任务" value={num(d.tasks_queued)} tone="warn" sub="等待目标拉取的指令" />
      </div>

      {/* ── 最近上线会话 ── */}
      <Card className="mb-4">
        <CardContent className="px-0 pt-4">
          <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
            <span className="flex items-center gap-1.5 text-sm font-medium">
              <MonitorSmartphoneIcon className="text-muted-foreground size-4" />
              最近上线会话
            </span>
            <span className="text-muted-foreground text-xs">最近注册 / 心跳的 beacon 主机</span>
            <Button asChild variant="ghost" size="sm" className="ml-auto">
              <Link href="/function/c2/sessions">
                全部会话
                <ArrowRightIcon className="size-3.5" />
              </Link>
            </Button>
          </div>
          {overview === null ? (
            <p className="text-muted-foreground py-12 text-center text-sm">加载中…</p>
          ) : d.recent_sessions.length === 0 ? (
            <p className="text-muted-foreground py-12 text-center text-sm">
              还没有会话。先到
              <Link href="/function/c2/listeners" className="mx-0.5 font-medium underline underline-offset-2">
                监听器
              </Link>
              创建并启动一个监听器,再到
              <Link href="/function/c2/payloads" className="mx-0.5 font-medium underline underline-offset-2">
                载荷
              </Link>
              生成投递物,目标机执行后即上线。
            </p>
          ) : (
            <Table className="table-fixed">
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="w-20">状态</TableHead>
                  {/* 主机列不设宽度:table-fixed 下自动吃掉余量。 */}
                  <TableHead>主机</TableHead>
                  <TableHead className="w-44">系统 / 架构</TableHead>
                  <TableHead className="w-36">内网 IP</TableHead>
                  <TableHead className="w-28">心跳</TableHead>
                  <TableHead className="w-16">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {d.recent_sessions.map((s) => {
                  const meta = c2SessionStatusMeta(String(s.status));
                  return (
                    <TableRow key={s.id}>
                      <TableCell>
                        <span className="inline-flex items-center gap-1.5 text-xs">
                          <span className={cn("size-2 shrink-0 rounded-full", meta.dot)} />
                          {meta.label}
                        </span>
                      </TableCell>
                      <TableCell className="min-w-0">
                        <Link
                          href={`/function/c2/detail?id=${s.id}`}
                          className="block w-full truncate text-left font-medium hover:underline"
                          title={s.hostname}
                        >
                          {s.hostname || `会话 #${s.id}`}
                        </Link>
                        <span className="text-muted-foreground block truncate text-xs" title={s.username}>
                          {[s.username, s.elevated ? "高权限" : ""].filter(Boolean).join(" · ") || "—"}
                        </span>
                      </TableCell>
                      <TableCell className="text-muted-foreground min-w-0 truncate text-xs" title={[s.os, s.arch].filter(Boolean).join(" · ")}>
                        {[s.os, s.arch].filter(Boolean).join(" · ") || "—"}
                      </TableCell>
                      <TableCell className="min-w-0">
                        <span className="block truncate font-mono text-xs" title={s.local_ips}>
                          {s.local_ips || "—"}
                        </span>
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs tabular-nums" title={fmtTime(s.last_seen)}>
                        {relTime(s.last_seen)}
                      </TableCell>
                      <TableCell>
                        <Button asChild variant="ghost" size="icon-xs" title="进入会话详情">
                          <Link href={`/function/c2/detail?id=${s.id}`} aria-label="进入会话详情">
                            <ArrowRightIcon className="size-3.5" />
                          </Link>
                        </Button>
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {/* ── 最近任务 ── */}
      <Card>
        <CardContent className="px-0 pt-4">
          <div className="flex flex-wrap items-center gap-2 px-4 pb-3 lg:px-6">
            <span className="flex items-center gap-1.5 text-sm font-medium">
              <ListTodoIcon className="text-muted-foreground size-4" />
              最近任务
            </span>
            <span className="text-muted-foreground text-xs">命令 / 文件 / 采集指令的执行流水(人工与 agent 共用一条队列)</span>
            <span className="text-muted-foreground ml-auto inline-flex items-center gap-1 text-xs">
              <TimerIcon className="size-3.5" />
              排队 {num(d.tasks_queued)} 条
            </span>
          </div>
          {overview === null ? (
            <p className="text-muted-foreground py-12 text-center text-sm">加载中…</p>
          ) : d.recent_tasks.length === 0 ? (
            <p className="text-muted-foreground py-12 text-center text-sm">
              还没有任务。会话上线后,在会话详情的「交互」页签下发命令,agent 工具产生的指令也会记录在这里。
            </p>
          ) : (
            <Table className="table-fixed">
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="w-36">时间</TableHead>
                  <TableHead className="w-20">类型</TableHead>
                  {/* 输入列不设宽度:table-fixed 下自动吃掉余量。 */}
                  <TableHead>输入</TableHead>
                  <TableHead className="w-16">状态</TableHead>
                  <TableHead className="w-16">操作者</TableHead>
                  <TableHead className="w-16">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {d.recent_tasks.map((t) => {
                  const st = c2TaskStatusMeta(String(t.status));
                  return (
                    <TableRow key={t.id}>
                      <TableCell className="text-muted-foreground text-xs tabular-nums">{fmtTime(t.created_at)}</TableCell>
                      <TableCell className="text-xs">{c2TaskKindLabel(String(t.kind))}</TableCell>
                      <TableCell className="min-w-0">
                        <Link
                          href={`/function/c2/detail?id=${t.session_id}&tab=tasks`}
                          className="block w-full truncate font-mono text-xs hover:underline"
                          title={c2TaskInputText(t.input)}
                        >
                          {c2TaskInputText(t.input) || "—"}
                        </Link>
                      </TableCell>
                      <TableCell>
                        <Badge variant="outline" className={st.badge}>
                          {st.label}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs">{t.actor === "agent" ? "agent" : "人工"}</TableCell>
                      <TableCell>
                        <Button asChild variant="ghost" size="icon-xs" title="进入会话流水">
                          <Link href={`/function/c2/detail?id=${t.session_id}&tab=tasks`} aria-label="进入会话流水">
                            <ArrowRightIcon className="size-3.5" />
                          </Link>
                        </Button>
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
