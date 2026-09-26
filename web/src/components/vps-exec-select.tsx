"use client";

// 建任务时的「执行环境」选择器(任务级覆盖):任务的 Bash 命令在哪台机器上跑。
//
// 三值语义(与后端 vps 指派一致):
//   "local"   = 平台本机(默认),不带 vps 字段
//   "machine" = 指定机器,创建请求带 vps_machine_id(创建即指派,发生在任务启动之前)
//   "auto"    = 自动分配,创建请求带 vps_auto=true(vps_machine_id 为空时后端生效)
//
// 为什么抽成组件:建任务入口有多个(通用任务抽屉、SRC 对话框、SRC 批量),
// 逐个手写必然漏——出网方式就经历过「SRC 对话框漏掉代理池选项」。执行环境决定命令
// 真正跑在哪台机器上,各入口必须给出同一套选项与同一段说明。
//
// 在线空闲机器列表只在选中「指定机器」时才拉(online 且 assigned_task_id===0):
// 不选机器就不必付这次请求;接口 404/500(VPS 服务未就绪)静默降级为空下拉。

import * as React from "react";

import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { api } from "@/lib/api";
import type { VpsMachine } from "@/lib/types";

export type VpsExecMode = "local" | "machine" | "auto";

/** machineId 的哨兵串:Radix Select 的 value="" 是保留值(表示未选中),空语义必须用哨兵。 */
export const VPS_MACHINE_PICK = "__pick__";

export type VpsExecValue = { mode: VpsExecMode; machineId: string };

/** 把 UI 的下拉值转成归一化的执行环境模式(未知值回落 local)。 */
export function toVpsExecMode(v: string): VpsExecMode {
  return v === "machine" ? "machine" : v === "auto" ? "auto" : "local";
}

/** 提交用的机器 id:只有「指定机器」且真的选中了某台才有意义,其余一律 undefined(=不指派)。 */
export function vpsMachineIdForSubmit(v: VpsExecValue): number | undefined {
  return v.mode === "machine" && v.machineId !== VPS_MACHINE_PICK ? Number(v.machineId) : undefined;
}

/** 提交用的自动分配开关:仅「自动分配」为 true。 */
export function vpsAutoForSubmit(v: VpsExecValue): boolean {
  return v.mode === "auto";
}

export function VpsExecSelect({
  mode,
  machineId,
  onChange,
  disabled,
}: {
  mode: VpsExecMode;
  /** 当前选中的机器 id;未选 = VPS_MACHINE_PICK 哨兵。 */
  machineId: string;
  /** onChange 传归一化后的 mode 与哨兵处理过的 machineId(离开「指定机器」自动重置哨兵)。 */
  onChange: (v: VpsExecValue) => void;
  disabled?: boolean;
}) {
  // useId 而不是写死 id:同一页面可能同时挂着建任务抽屉与 SRC 对话框,写死的 id 会重复,
  // label 的 htmlFor 会指到另一个控件上(与 proxy-egress-select 同理)。
  const modeId = React.useId();
  const [machines, setMachines] = React.useState<VpsMachine[]>([]);

  // 选了「指定机器」才拉候选;每次切回「指定机器」都重新拉一次(空闲机器随时在变)。
  React.useEffect(() => {
    if (mode !== "machine") return;
    api
      .vpsMachines({ status: "online", limit: 200 })
      .then((r) => setMachines((r.machines ?? []).filter((m) => m.assigned_task_id === 0)))
      .catch(() => setMachines([]));
  }, [mode]);

  return (
    <div className="grid gap-2">
      <Label htmlFor={modeId}>执行环境</Label>
      <Select
        value={mode}
        onValueChange={(v) => onChange({ mode: toVpsExecMode(v), machineId: v === "machine" ? machineId : VPS_MACHINE_PICK })}
        disabled={disabled}
      >
        <SelectTrigger id={modeId} className="w-full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectGroup>
            <SelectItem value="local">平台本机（默认）</SelectItem>
            <SelectItem value="machine">指定机器</SelectItem>
            <SelectItem value="auto">自动分配</SelectItem>
          </SelectGroup>
        </SelectContent>
      </Select>
      {mode === "machine" ? (
        <Select value={machineId} onValueChange={(v) => onChange({ mode, machineId: v })} disabled={disabled}>
          <SelectTrigger className="w-full">
            <SelectValue placeholder={machines.length === 0 ? "暂无在线空闲机器" : "选择执行机器"} />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              <SelectItem value={VPS_MACHINE_PICK} disabled>
                请选择机器…
              </SelectItem>
              {machines.map((m) => (
                <SelectItem key={m.id} value={String(m.id)}>
                  {m.name} · {m.host}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
      ) : null}
      {mode === "machine" && machines.length === 0 ? (
        <p className="text-amber-600 text-xs dark:text-amber-400">
          没有可指派的机器。请先到「VPS 中心 → 机器池」录入并测活通过(状态=在线空闲)。
        </p>
      ) : null}
      <p className="text-muted-foreground text-xs">
        「指定机器 / 自动分配」只影响 Bash 命令的执行位置:命令将在该 Linux 机器的独立 shell
        里跑、工作目录固定为远程任务工作区;Read/Write 等文件工具仍操作平台本地工作区。创建成功后才会实际指派,
        失败时任务照常在本机执行。
      </p>
    </div>
  );
}
