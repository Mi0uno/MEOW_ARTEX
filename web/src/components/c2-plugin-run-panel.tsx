"use client";

// C2 会话详情「插件」页签(契约 §21,Agent G):插件下拉(按会话 os 平台过滤)→
// 按 params_schema 动态渲染参数表单 → 运行。运行只是入队:后端建 kind='plugin'
// 的 c2_tasks(binary 另建上传传输),结果由 beacon 回传后进任务流水——本页签
// toast 任务号并提示到「流水」页签查看(与既有页签联动,不在本地轮询)。

import * as React from "react";

import Link from "next/link";

import { HistoryIcon, Loader2Icon, PlayIcon, PuzzleIcon } from "lucide-react";
import { toast } from "sonner";

import {
  c2PluginDefaultParams,
  c2PluginKindMeta,
  c2PluginLanguageMeta,
  c2PluginParamDefs,
  c2PluginPlatformBadge,
  c2PluginPlatformMatches,
  c2PayloadTargetLabel,
} from "@/components/c2-shared";
import type { C2PluginParamDef } from "@/components/c2-shared";
import { C2PluginParamForm } from "@/components/c2-plugin-param-form";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { api } from "@/lib/api";
import type { C2Plugin, C2Session } from "@/lib/types";

export function C2PluginRunPanel({ session }: { session: C2Session }) {
  const [plugins, setPlugins] = React.useState<C2Plugin[] | null>(null);
  const [pluginId, setPluginId] = React.useState<string>("");
  const [values, setValues] = React.useState<Record<string, string>>({});
  const [running, setRunning] = React.useState(false);

  const load = React.useCallback(() => {
    api
      .c2Plugins({ limit: 200 })
      .then((r) => {
        const fitting = (r.plugins ?? []).filter((p) => c2PluginPlatformMatches(p.platform, session.os));
        setPlugins(fitting);
      })
      .catch((e) => {
        setPlugins([]);
        toast.error(`加载插件库失败:${(e as Error).message}`);
      });
  }, [session.os]);

  React.useEffect(() => {
    load();
  }, [load]);

  const plugin = React.useMemo(
    () => (plugins ?? []).find((p) => String(p.id) === pluginId) ?? null,
    [plugins, pluginId],
  );
  const defs: C2PluginParamDef[] = React.useMemo(
    () => (plugin ? c2PluginParamDefs(plugin.params_schema) : []),
    [plugin],
  );

  const pick = (id: string) => {
    setPluginId(id);
    const p = (plugins ?? []).find((x) => String(x.id) === id);
    setValues(p ? c2PluginDefaultParams(c2PluginParamDefs(p.params_schema)) : {});
  };

  const run = async () => {
    if (!plugin || running) return;
    setRunning(true);
    try {
      const r = await api.c2RunPlugin(session.id, plugin.id, values);
      toast.success(`插件 «${plugin.name}» 已下发(任务 #${r.task_id}),回传结果请到「流水」页签查看`, {
        duration: 6000,
      });
    } catch (e) {
      toast.error(`运行失败:${(e as Error).message}`);
    } finally {
      setRunning(false);
    }
  };

  const dead = session.status === "dead";

  return (
    <div className="grid gap-4 lg:grid-cols-3">
      <Card className="lg:col-span-2">
        <CardContent className="grid gap-3 pt-4">
          <div className="flex flex-wrap items-center gap-2">
            <span className="flex items-center gap-1.5 text-sm font-medium">
              <PuzzleIcon className="text-muted-foreground size-4" />
              运行插件
            </span>
            <span className="text-muted-foreground text-xs">
              选插件、填参数、一键入队;执行记录与回传输出在本会话「流水」页签
            </span>
          </div>
          {plugins === null ? (
            <p className="text-muted-foreground py-10 text-center text-sm">加载插件库中…</p>
          ) : plugins.length === 0 ? (
            <p className="text-muted-foreground py-10 text-center text-sm">
              没有适配当前平台({session.os || "未知"})的插件,可到
              <Link href="/function/c2/plugins" className="ml-1 text-blue-600 hover:underline">
                插件库
              </Link>
              新建。
            </p>
          ) : (
            <>
              <div className="grid gap-1.5">
                <Label htmlFor="c2-plugin-pick" className="text-xs">
                  插件(已按 {session.os || "未知"} 平台过滤)
                </Label>
                <Select value={pluginId} onValueChange={pick} disabled={dead}>
                  <SelectTrigger id="c2-plugin-pick" className="w-full">
                    <SelectValue placeholder="选择要运行的插件" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      {plugins.map((p) => (
                        <SelectItem key={p.id} value={String(p.id)}>
                          {p.name}
                          {p.builtin ? "(内置)" : ""}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  </SelectContent>
                </Select>
              </div>
              {plugin ? (
                <>
                  <div className="flex flex-wrap items-center gap-1.5">
                    <Badge variant="outline" className={c2PluginPlatformBadge(plugin.platform)}>
                      {c2PayloadTargetLabel(plugin.platform)}
                    </Badge>
                    <Badge variant="outline" className={c2PluginKindMeta(plugin.kind).badge}>
                      {c2PluginKindMeta(plugin.kind).label}
                    </Badge>
                    <Badge variant="outline" className={c2PluginLanguageMeta(plugin.language).badge}>
                      {c2PluginLanguageMeta(plugin.language).label}
                    </Badge>
                    {plugin.builtin ? (
                      <Badge variant="outline" className="border-violet-500/30 bg-violet-500/15 text-violet-600">
                        内置
                      </Badge>
                    ) : null}
                    <span className="text-muted-foreground min-w-0 truncate text-xs" title={plugin.description}>
                      {plugin.description || "—"}
                    </span>
                  </div>
                  <C2PluginParamForm defs={defs} values={values} onChange={setValues} disabled={dead} />
                  <div className="flex items-center gap-2">
                    <Button size="sm" onClick={() => void run()} disabled={running || dead || !plugin}>
                      {running ? <Loader2Icon className="size-3.5 animate-spin" /> : <PlayIcon className="size-3.5" />}
                      {running ? "下发中…" : "运行"}
                    </Button>
                    <p className="text-muted-foreground text-[10px]">
                      参数值只在声明的占位处替换并做注入消毒;binary 插件会先走文件通道上传再执行。
                    </p>
                  </div>
                </>
              ) : (
                <p className="text-muted-foreground py-6 text-center text-sm">选择一个插件查看参数并运行。</p>
              )}
            </>
          )}
          {dead ? <p className="text-xs text-red-600 dark:text-red-400">会话已终止,不能再下发插件。</p> : null}
        </CardContent>
      </Card>

      <Card>
        <CardContent className="grid gap-2 pt-4 text-xs">
          <p className="flex items-center gap-1.5 text-sm font-medium">
            <HistoryIcon className="text-muted-foreground size-4" />
            运行说明
          </p>
          <p className="text-muted-foreground leading-relaxed">
            脚本插件:entry 模板按参数声明渲染成命令,走 beacon 命令通道,与「交互」页签同一队列。
          </p>
          <p className="text-muted-foreground leading-relaxed">
            二进制插件:插件内容先经文件通道上传到目标临时目录(Windows 在 C:\Users\Public\,POSIX 在
            /tmp/),随后执行 entry;声明了 after_clean 开关时可在执行后清理落盘文件。
          </p>
          <p className="text-muted-foreground leading-relaxed">
            目标离线时任务排队,beacon 心跳回来后执行;状态与输出在「流水」页签(含 agent 经 c2_plugin_run
            下发的运行,类型为 plugin)。
          </p>
        </CardContent>
      </Card>
    </div>
  );
}
