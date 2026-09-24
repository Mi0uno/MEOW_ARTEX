"use client";

// MCP 表单的共享件:列表页的快速编辑抽屉与详情页的「配置」页签共用同一套字段。
// 两处各写一份的话字段清单迟早漂移,漏一个字段就是功能降级。

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import type { MCPServer } from "@/lib/types";

export type McpTransport = "stdio" | "http" | "sse";

export type McpFormState = {
  name: string;
  transport: McpTransport;
  command: string;
  args: string;
  url: string;
  env: string;
  insecure: boolean;
  enabled: boolean;
};

export const emptyMcpForm: McpFormState = {
  name: "",
  transport: "stdio",
  command: "",
  args: "",
  url: "",
  env: "",
  insecure: false,
  enabled: true,
};

// 传输方式的展示元数据(徽标与按钮共用,避免两处文案不一致)。
export const TRANSPORT_OPTIONS: { value: McpTransport; label: string }[] = [
  { value: "stdio", label: "stdio（本地）" },
  { value: "http", label: "http（远程）" },
  { value: "sse", label: "sse（旧版）" },
];

const TRANSPORT_META: Record<McpTransport, { label: string; badgeClass: string }> = {
  stdio: { label: "stdio", badgeClass: "bg-blue-500/15 text-blue-600 border-blue-500/30" },
  http: { label: "http", badgeClass: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30" },
  sse: { label: "sse", badgeClass: "bg-amber-500/15 text-amber-600 border-amber-500/30" },
};

export function transportMeta(t: string): { label: string; badgeClass: string } {
  return TRANSPORT_META[t as McpTransport] ?? { label: t, badgeClass: "bg-muted text-muted-foreground" };
}

// env 在 UI 里是「每行 KEY=VALUE」的文本,存库是对象;远程模式下同一字段承载请求头。
export function parseEnv(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    const idx = trimmed.indexOf("=");
    if (idx > 0) {
      const key = trimmed.slice(0, idx).trim();
      if (key) out[key] = trimmed.slice(idx + 1).trim();
    }
  }
  return out;
}

export function envToText(env: Record<string, string> | undefined): string {
  return Object.entries(env ?? {})
    .map(([k, v]) => `${k}=${v}`)
    .join("\n");
}

export function formFromServer(s: MCPServer): McpFormState {
  return {
    name: s.name,
    transport: s.transport,
    command: s.command ?? "",
    // args 的类型是非可选数组,但库里是 jsonb 列——理论上可能存成 null,
    // 这里用 Array.isArray 兜住,免得 join 直接炸掉整个表单。
    args: (Array.isArray(s.args) ? s.args : []).join(" "),
    url: s.url ?? "",
    env: envToText(s.env),
    insecure: s.insecure ?? false,
    enabled: s.enabled,
  };
}

// validateMcpForm 返回第一条不合法原因;null = 可提交。
export function validateMcpForm(f: McpFormState): string | null {
  if (!f.name.trim()) return "请填写名称";
  if (f.transport === "stdio" && !f.command.trim()) return "请填写命令";
  if (f.transport !== "stdio" && !f.url.trim()) return "请填写远程 URL";
  return null;
}

// formToPayload 折成后端 SaveMCP 认识的形状:stdio 与远程是两套互斥字段,
// 另一套必须显式清空——否则从 http 切回 stdio 后旧 URL 会留在库里继续生效。
export function formToPayload(f: McpFormState, id?: number): Partial<MCPServer> {
  const base =
    f.transport !== "stdio"
      ? {
          transport: f.transport,
          url: f.url.trim(),
          command: "",
          args: [] as string[],
          env: parseEnv(f.env), // 远程模式下 env 即请求头
          insecure: f.insecure,
        }
      : {
          transport: "stdio" as const,
          command: f.command.trim(),
          args: f.args.trim() ? f.args.trim().split(/\s+/) : [],
          env: parseEnv(f.env),
          insecure: false,
        };
  return {
    ...(id != null ? { id } : {}),
    name: f.name.trim(),
    enabled: f.enabled,
    ...base,
  };
}

export function McpFormFields({
  form,
  onPatch,
  idPrefix = "mcp",
}: {
  form: McpFormState;
  onPatch: (patch: Partial<McpFormState>) => void;
  idPrefix?: string;
}) {
  return (
    <div className="grid gap-4">
      <div className="grid gap-2">
        <Label>传输方式</Label>
        <div className="flex flex-wrap gap-2">
          {TRANSPORT_OPTIONS.map((t) => (
            <Button
              key={t.value}
              type="button"
              size="sm"
              variant={form.transport === t.value ? "default" : "outline"}
              onClick={() => onPatch({ transport: t.value })}
            >
              {t.label}
            </Button>
          ))}
        </div>
      </div>

      <div className="grid gap-2">
        <Label htmlFor={`${idPrefix}-name`}>名称</Label>
        <Input
          id={`${idPrefix}-name`}
          placeholder="filesystem"
          value={form.name}
          onChange={(e) => onPatch({ name: e.target.value })}
        />
      </div>

      {form.transport === "stdio" ? (
        <>
          <div className="grid gap-2">
            <Label htmlFor={`${idPrefix}-cmd`}>命令</Label>
            <Input
              id={`${idPrefix}-cmd`}
              className="font-mono"
              placeholder="npx"
              value={form.command}
              onChange={(e) => onPatch({ command: e.target.value })}
            />
          </div>
          <div className="grid gap-2">
            <Label htmlFor={`${idPrefix}-args`}>参数（空格分隔）</Label>
            <Input
              id={`${idPrefix}-args`}
              className="font-mono"
              placeholder="-y @modelcontextprotocol/server-filesystem /data"
              value={form.args}
              onChange={(e) => onPatch({ args: e.target.value })}
            />
          </div>
        </>
      ) : (
        <div className="grid gap-2">
          <Label htmlFor={`${idPrefix}-url`}>远程 URL</Label>
          <Input
            id={`${idPrefix}-url`}
            className="font-mono"
            placeholder="https://mcp.example.com/mcp"
            value={form.url}
            onChange={(e) => onPatch({ url: e.target.value })}
          />
          {/* Radix Checkbox 渲染成 <button>(labelable 元素),所以 label 的 htmlFor
              指向它的 id 既合法又让整行文字可点。 */}
          <div className="flex items-center gap-2 text-sm">
            <Checkbox
              id={`${idPrefix}-insecure`}
              checked={form.insecure}
              onCheckedChange={(v) => onPatch({ insecure: v === true })}
            />
            <Label htmlFor={`${idPrefix}-insecure`} className="font-normal">
              跳过 TLS 证书校验（自签证书）
            </Label>
          </div>
        </div>
      )}

      <div className="grid gap-2">
        <Label htmlFor={`${idPrefix}-env`}>
          {form.transport !== "stdio"
            ? "请求头（每行 KEY=VALUE，如 Authorization=Bearer xxx）"
            : "环境变量（每行 KEY=VALUE）"}
        </Label>
        <Textarea
          id={`${idPrefix}-env`}
          className="font-mono"
          rows={4}
          placeholder={form.transport !== "stdio" ? "Authorization=Bearer xxxx" : "API_KEY=xxxx\nFOO=bar"}
          value={form.env}
          onChange={(e) => onPatch({ env: e.target.value })}
        />
      </div>

      <div className="flex items-center justify-between gap-3 rounded-lg border p-3">
        <div className="min-w-0">
          <Label htmlFor={`${idPrefix}-enabled`}>启用</Label>
          <p className="text-muted-foreground text-xs">停用后 Agent 不会看到这个服务器的工具。</p>
        </div>
        <Switch id={`${idPrefix}-enabled`} checked={form.enabled} onCheckedChange={(v) => onPatch({ enabled: v })} />
      </div>
    </div>
  );
}
