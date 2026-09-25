"use client";

// 建任务时的「出网方式」选择器(任务级覆盖)。
//
// 三值语义(与后端 db.ValidTaskProxyMode 一致):
//   ""(UI 上显示「跟随全局」) = 跟随全局出网模式,不覆盖
//   "direct"                  = 本任务强制直连
//   "pool"                    = 本任务强制走代理池,可再指定用哪个代理组
//
// 为什么抽成组件:建任务入口有四个(SRC 对话框、通用任务抽屉、SRC 批量、信息搜集),
// 逐个手写必然漏——SRC 对话框就曾漏掉,表现是「建 SRC 任务时根本看不到代理池选项」。
// 出口是任务级安全语义,四个入口必须给出同一套选项与同一段说明。
//
// 代理组列表只在选中「走代理池」时才拉:不选池就不必付这次请求。

import * as React from "react";

import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { api } from "@/lib/api";

export type ProxyEgressMode = "" | "direct" | "pool";

/** value=0 表示「用全局选中的代理组」(后端语义:proxy_group_id=0)。 */
export type ProxyEgressValue = { mode: ProxyEgressMode; groupId: number };

/** 把 UI 的下拉值(含哨兵 "inherit")转成后端语义值。 */
export function toProxyEgressMode(v: string): ProxyEgressMode {
  if (v === "direct" || v === "pool") return v;
  return "";
}

/** 提交用的组 id:只有「走代理池」时组才有意义,其余一律 0。 */
export function proxyGroupIdForSubmit(v: ProxyEgressValue): number {
  return v.mode === "pool" ? v.groupId : 0;
}

export function ProxyEgressSelect({
  mode,
  groupId,
  onChange,
  disabled,
}: {
  mode: ProxyEgressMode;
  groupId: number;
  onChange: (v: ProxyEgressValue) => void;
  disabled?: boolean;
}) {
  // useId 而不是写死 "proxy-mode":同一页可能同时挂着建任务抽屉与 SRC 对话框,
  // 写死的 id 会重复,label 的 htmlFor 会指到另一个控件上。
  const modeId = React.useId();
  const [groups, setGroups] = React.useState<
    { id: number; name: string; alive_count: number; proxy_count: number }[]
  >([]);

  React.useEffect(() => {
    if (mode !== "pool" || groups.length > 0) return;
    api
      .proxyGroups({ limit: 200 })
      .then((r) =>
        setGroups(
          r.groups.map((g) => ({ id: g.id, name: g.name, alive_count: g.alive_count, proxy_count: g.proxy_count })),
        ),
      )
      .catch(() => setGroups([]));
  }, [mode, groups.length]);

  return (
    <div className="grid gap-2">
      <Label htmlFor={modeId}>出网方式</Label>
      <Select
        value={mode === "" ? "inherit" : mode}
        onValueChange={(v) => onChange({ mode: toProxyEgressMode(v), groupId })}
        disabled={disabled}
      >
        <SelectTrigger id={modeId} className="w-full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectGroup>
            <SelectItem value="inherit">跟随全局（默认）</SelectItem>
            <SelectItem value="direct">强制直连（不走代理池）</SelectItem>
            <SelectItem value="pool">走代理池</SelectItem>
          </SelectGroup>
        </SelectContent>
      </Select>
      {mode === "pool" ? (
        <Select
          value={groupId === 0 ? "auto" : String(groupId)}
          onValueChange={(v) => onChange({ mode, groupId: v === "auto" ? 0 : Number(v) })}
          disabled={disabled}
        >
          <SelectTrigger className="w-full">
            <SelectValue placeholder="代理组" />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              <SelectItem value="auto">用全局选中的代理组</SelectItem>
              {groups.map((g) => (
                <SelectItem key={g.id} value={String(g.id)}>
                  {g.name}（可用 {g.alive_count}/{g.proxy_count}）
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
      ) : null}
      <p className="text-muted-foreground text-xs">
        本任务的 agent 与信息搜集流水线都按这里出网。选「走代理池」后请求会经代理组按策略轮换出口；
        代理池不可用时回落到全局出口而不是静默直连，避免真实 IP 意外暴露。
      </p>
    </div>
  );
}
