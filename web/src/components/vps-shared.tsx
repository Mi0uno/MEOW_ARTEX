"use client";

// VPS 中心共用件:状态/来源徽标映射、时间与体积格式化、录入与编辑机器对话框、
// 执行输出查看对话框、可指派机器拉取。
//
// 徽标配色与中文标签收在这一份里,仪表盘 / 机器池 / 机器详情 / 任务详情「执行环境」
// 四个入口共用,保证同一状态在各页面的叫法与颜色完全一致(与 proxy-dialogs 同一思路)。

import * as React from "react";

import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { VpsExecution, VpsMachine } from "@/lib/types";
import { cn } from "@/lib/utils";

// ---- 展示映射 ----

// 未测/未知状态的统一样式:中性灰,不用颜色暗示好坏。
const MUTED_BADGE = "bg-muted text-muted-foreground";

const VPS_STATUS_BADGE: Record<string, string> = {
  online: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30",
  busy: "bg-blue-500/15 text-blue-600 border-blue-500/30",
  offline: "bg-red-500/15 text-red-600 border-red-500/30",
  maintenance: "bg-amber-500/15 text-amber-600 border-amber-500/30",
  unknown: MUTED_BADGE,
};

const VPS_STATUS_LABEL: Record<string, string> = {
  online: "在线",
  busy: "占用中",
  offline: "离线",
  maintenance: "维护",
  unknown: "未测活",
};

const VPS_SOURCE_BADGE: Record<string, string> = {
  agent: "bg-purple-500/15 text-purple-600 border-purple-500/30",
  terminal: "bg-blue-500/15 text-blue-600 border-blue-500/30",
  toolset: "bg-amber-500/15 text-amber-600 border-amber-500/30",
  manual: MUTED_BADGE,
  detect: MUTED_BADGE,
};

const VPS_SOURCE_LABEL: Record<string, string> = {
  agent: "Agent",
  terminal: "终端",
  toolset: "工具集",
  manual: "手动",
  detect: "探测",
};

export function vpsStatusBadgeClass(status: string): string {
  return VPS_STATUS_BADGE[status] ?? MUTED_BADGE;
}

export function vpsStatusLabel(status: string): string {
  return VPS_STATUS_LABEL[status] ?? (status || "未测活");
}

export function vpsSourceBadgeClass(source: string): string {
  return VPS_SOURCE_BADGE[source] ?? MUTED_BADGE;
}

export function vpsSourceLabel(source: string): string {
  return VPS_SOURCE_LABEL[source] ?? (source || "—");
}

export function vpsAuthLabel(authType: string): string {
  return authType === "key" ? "私钥" : "密码";
}

// ---- 格式化 ----

// num 把可能为 null / undefined / NaN 的数值收敛成 0。后端把空集合序列化成 JSON null
// 是这台平台的既有行为,来自接口的数字都从这里过一遍,否则参与算术/toFixed 会白屏。
export function num(n: number | null | undefined): number {
  return typeof n === "number" && Number.isFinite(n) ? n : 0;
}

export function fmtTime(iso?: string | null): string {
  if (!iso) return "—";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
}

export function fmtClock(iso?: string | null): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}

export function fmtBytes(n: number): string {
  if (!n || n <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), units.length - 1);
  const v = n / 1024 ** i;
  return `${i === 0 ? v : v.toFixed(1)} ${units[i]}`;
}

// 执行耗时;0 = 无数据。
export function fmtDuration(ms: number): string {
  const v = num(ms);
  if (v <= 0) return "—";
  if (v < 1000) return `${v} ms`;
  const s = v / 1000;
  if (s < 60) return `${s.toFixed(1)} s`;
  const m = Math.floor(s / 60);
  return `${m}m ${Math.round(s - m * 60)}s`;
}

// 内存 MB → 人读;0 = 未采集。
export function fmtMemory(mb: number): string {
  const v = num(mb);
  if (v <= 0) return "—";
  return v >= 1024 ? `${(v / 1024).toFixed(1)} GB` : `${v} MB`;
}

/**
 * loadAssignCandidates 拉取「可指派」机器:online 且未被任务占用。
 * 三个入口共用(创建任务抽屉 / 任务详情执行环境 tab / 工具集应用),保证口径一致。
 * 接口 404/500(后端未就绪)时静默回空,由调用方显示空态。
 */
export async function loadAssignCandidates(): Promise<VpsMachine[]> {
  const r = await api.vpsMachines({ status: "online", limit: 200 });
  return (r.machines ?? []).filter((m) => num(m.assigned_task_id) === 0);
}

// ---- 录入 / 编辑机器对话框 ----

// 编辑模式不回显凭据(后端永不回传,前端也不留残影);留空 = 不修改。
type MachineForm = {
  name: string;
  host: string;
  port: string;
  username: string;
  authType: string;
  password: string;
  privateKey: string;
  keyPassphrase: string;
  region: string;
  tags: string;
  workspaceBase: string;
  notes: string;
};

const EMPTY_FORM: MachineForm = {
  name: "",
  host: "",
  port: "22",
  username: "root",
  authType: "password",
  password: "",
  privateKey: "",
  keyPassphrase: "",
  region: "",
  tags: "",
  workspaceBase: "",
  notes: "",
};

export function VpsMachineDialog({
  open,
  onOpenChange,
  /** null = 录入;非 null = 编辑该机器(凭据留空不改)。 */
  machine,
  onSaved,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  machine: VpsMachine | null;
  onSaved?: (m: VpsMachine) => void;
}) {
  const editing = machine != null;
  const [form, setForm] = React.useState<MachineForm>(EMPTY_FORM);
  const [busy, setBusy] = React.useState(false);

  // 每次打开都重置:录入要清掉上次的输入;编辑只回显非敏感字段。
  React.useEffect(() => {
    if (!open) return;
    if (machine) {
      setForm({
        name: machine.name ?? "",
        host: machine.host ?? "",
        port: String(num(machine.port) || 22),
        username: machine.username ?? "",
        authType: machine.auth_type === "key" ? "key" : "password",
        password: "",
        privateKey: "",
        keyPassphrase: "",
        region: machine.region ?? "",
        tags: (machine.tags ?? []).join(", "),
        workspaceBase: machine.workspace_base ?? "",
        notes: machine.notes ?? "",
      });
    } else {
      setForm(EMPTY_FORM);
    }
  }, [open, machine]);

  const set = (k: keyof MachineForm) => (e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) =>
    setForm((f) => ({ ...f, [k]: e.target.value }));

  const submit = async () => {
    const name = form.name.trim();
    const host = form.host.trim();
    const username = form.username.trim();
    if (!name || !host || !username) {
      toast.error("名称 / 主机 / 用户名为必填项");
      return;
    }
    if (form.authType === "password" && !editing && !form.password) {
      toast.error("密码认证必须填写密码");
      return;
    }
    if (form.authType === "key" && !editing && !form.privateKey.trim()) {
      toast.error("私钥认证必须粘贴私钥内容");
      return;
    }
    const port = Math.min(Math.max(Math.floor(Number(form.port) || 22), 1), 65535);
    const tags = form.tags
      .split(/[,，]/)
      .map((t) => t.trim())
      .filter(Boolean);
    setBusy(true);
    try {
      if (editing && machine) {
        // 编辑:凭据字段留空 = 不修改,只有填了才带上(后端按缺省跳过)。
        const input: Record<string, unknown> = {
          name,
          host,
          port,
          username,
          auth_type: form.authType,
          region: form.region.trim(),
          tags,
          workspace_base: form.workspaceBase.trim(),
          notes: form.notes.trim(),
        };
        if (form.password) input.password = form.password;
        if (form.privateKey.trim()) input.private_key = form.privateKey;
        if (form.keyPassphrase) input.key_passphrase = form.keyPassphrase;
        const m = await api.updateVpsMachine(machine.id, input);
        toast.success("机器已保存" + (form.password || form.privateKey.trim() ? "(凭据已更新)" : "(凭据未改动)"));
        onSaved?.(m);
      } else {
        // 录入:后端同步做首测(可能十几秒);失败也入库,状态带回 last_error。
        const m = await api.createVpsMachine({
          name,
          host,
          port,
          username,
          auth_type: form.authType,
          password: form.password || undefined,
          private_key: form.privateKey.trim() || undefined,
          key_passphrase: form.keyPassphrase || undefined,
          region: form.region.trim(),
          tags,
          workspace_base: form.workspaceBase.trim() || undefined,
          notes: form.notes.trim(),
        });
        if (m.status === "offline") {
          toast.warning(`机器已入库,但首测连接失败:${m.last_error || "未知错误"}`, { duration: 10000 });
        } else {
          toast.success("机器已录入,首测通过");
        }
        onSaved?.(m);
      }
      onOpenChange(false);
    } catch (e) {
      toast.error(`${editing ? "保存" : "录入"}失败:${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{editing ? `编辑机器「${machine.name}」` : "录入机器"}</DialogTitle>
          <DialogDescription className="text-xs">
            {editing
              ? "凭据不回显:密码 / 私钥 / 口令留空表示不修改。保存后可在列表里手动测活验证。"
              : "保存后会立即对机器做一次 SSH 连通性测试并采集系统信息;连接失败也会入库(状态=离线),可稍后修复再测活。"}
          </DialogDescription>
        </DialogHeader>

        <div className="grid gap-4">
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div className="grid gap-1.5">
              <Label>名称 *</Label>
              <Input value={form.name} onChange={set("name")} placeholder="如:香港渗透机 01" />
            </div>
            <div className="grid gap-1.5">
              <Label>主机 *</Label>
              <Input value={form.host} onChange={set("host")} placeholder="IP 或域名" />
            </div>
            <div className="grid gap-1.5">
              <Label>端口</Label>
              <Input type="number" min={1} max={65535} value={form.port} onChange={set("port")} />
            </div>
            <div className="grid gap-1.5">
              <Label>用户名 *</Label>
              <Input value={form.username} onChange={set("username")} placeholder="root" />
            </div>
          </div>

          <div className="grid gap-1.5">
            <Label>认证方式</Label>
            <Select value={form.authType} onValueChange={(v) => setForm((f) => ({ ...f, authType: v }))}>
              <SelectTrigger className="w-48">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="password">密码</SelectItem>
                  <SelectItem value="key">私钥</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
          </div>

          {form.authType === "password" ? (
            <div className="grid gap-1.5">
              <Label>密码 {editing ? "(留空 = 不修改)" : "*"}</Label>
              <Input
                type="password"
                autoComplete="new-password"
                value={form.password}
                onChange={set("password")}
                placeholder={editing ? "留空表示不修改密码" : "SSH 登录密码"}
              />
            </div>
          ) : (
            <>
              <div className="grid gap-1.5">
                <Label>私钥 {editing ? "(留空 = 不修改)" : "*"}</Label>
                <Textarea
                  className="font-mono text-xs"
                  rows={5}
                  value={form.privateKey}
                  onChange={set("privateKey")}
                  placeholder={editing ? "留空表示不修改私钥" : "-----BEGIN OPENSSH PRIVATE KEY-----"}
                />
              </div>
              <div className="grid gap-1.5">
                <Label>私钥口令(可选)</Label>
                <Input
                  type="password"
                  autoComplete="new-password"
                  value={form.keyPassphrase}
                  onChange={set("keyPassphrase")}
                  placeholder={editing ? "留空表示不修改" : "私钥有口令时填写"}
                />
              </div>
            </>
          )}

          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div className="grid gap-1.5">
              <Label>地区(可选)</Label>
              <Input value={form.region} onChange={set("region")} placeholder="如:香港 / 东京" />
            </div>
            <div className="grid gap-1.5">
              <Label>标签(逗号分隔,可选)</Label>
              <Input value={form.tags} onChange={set("tags")} placeholder="渗透,外网" />
            </div>
          </div>

          <div className="grid gap-1.5">
            <Label>工作区根目录(可选)</Label>
            <Input
              value={form.workspaceBase}
              onChange={set("workspaceBase")}
              placeholder="缺省 = 服务端默认;任务的远程工作区建在其下 task_<id>/"
            />
          </div>

          <div className="grid gap-1.5">
            <Label>备注(可选)</Label>
            <Textarea rows={2} value={form.notes} onChange={set("notes")} placeholder="机器用途、供应商、到期时间等" />
          </div>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy}>
            取消
          </Button>
          <Button onClick={() => void submit()} disabled={busy}>
            {busy ? (editing ? "保存中…" : "录入并首测中…") : editing ? "保存" : "录入机器"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// ---- 执行输出查看对话框 ----

export function VpsExecOutputDialog({
  execution,
  onOpenChange,
}: {
  /** null = 关闭;非 null = 查看这条流水的完整输出。 */
  execution: VpsExecution | null;
  onOpenChange: (v: boolean) => void;
}) {
  const [output, setOutput] = React.useState("");
  const [loading, setLoading] = React.useState(false);
  const [error, setError] = React.useState("");

  React.useEffect(() => {
    if (!execution) return;
    let alive = true;
    setLoading(true);
    setError("");
    setOutput("");
    api
      .vpsExecutionOutput(execution.id)
      .then((text) => {
        if (alive) setOutput(text ?? "");
      })
      .catch((e) => {
        if (alive) setError((e as Error).message);
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [execution]);

  const exitCode = execution ? execution.exit_code : null;
  return (
    <Dialog open={execution != null} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle className="flex flex-wrap items-center gap-2">
            执行输出
            {execution ? (
              <Badge variant="outline" className={vpsSourceBadgeClass(execution.source)}>
                {vpsSourceLabel(execution.source)}
              </Badge>
            ) : null}
            {exitCode != null ? (
              <Badge
                variant="outline"
                className={cn(
                  "border-emerald-500/30 bg-emerald-500/15 text-emerald-600",
                  num(exitCode) !== 0 && "border-red-500/30 bg-red-500/15 text-red-600",
                )}
              >
                exit {num(exitCode)}
              </Badge>
            ) : (
              <Badge variant="outline" className={MUTED_BADGE}>
                exit —
              </Badge>
            )}
          </DialogTitle>
          <DialogDescription className="min-w-0">
            <span className="block truncate font-mono text-xs" title={execution?.command}>
              {execution?.command || "—"}
            </span>
          </DialogDescription>
        </DialogHeader>

        <div className="text-muted-foreground flex flex-wrap gap-x-4 gap-y-1 text-xs tabular-nums">
          <span>{fmtTime(execution?.created_at)}</span>
          <span>耗时 {fmtDuration(num(execution?.duration_ms))}</span>
          <span>输出 {fmtBytes(num(execution?.output_size))}</span>
        </div>

        <pre className="max-h-[50vh] overflow-auto rounded-lg border bg-muted/30 p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap break-all">
          {loading ? "加载中…" : error ? `读取失败:${error}` : output || "(无输出)"}
        </pre>
      </DialogContent>
    </Dialog>
  );
}
