"use client";

import * as React from "react";

import { ArchiveIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Spinner } from "@/components/ui/spinner";
import { api } from "@/lib/api";
import type { TaskAssetUpsert } from "@/lib/types";
import { cn } from "@/lib/utils";

// 归档类型的 UI 六项。HTTP 服务与端口服务提交时同为 type=service(后端按有无
// url 区分,契约 §6/§8):有 url 走 HTTP 服务,host+port 走端口服务;
// host 是 IP 时按「带开放端口的 IP」入库(open_ports),与提取管线的类型映射一致。
type ArchiveKind = "root_domain" | "subdomain" | "ip" | "http" | "port" | "app";

const ARCHIVE_KINDS: { value: ArchiveKind; label: string }[] = [
  { value: "root_domain", label: "根域名" },
  { value: "subdomain", label: "子域名" },
  { value: "ip", label: "IP" },
  { value: "http", label: "HTTP 服务" },
  { value: "port", label: "端口服务" },
  { value: "app", label: "应用" },
];

interface ArchiveFields {
  domain: string;
  icp: string;
  recordType: string;
  ip: string;
  boundDomains: string;
  url: string;
  pageTitle: string;
  host: string;
  port: string;
  serviceName: string;
  appName: string;
}

const EMPTY_FIELDS: ArchiveFields = {
  appName: "",
  boundDomains: "",
  domain: "",
  host: "",
  icp: "",
  ip: "",
  pageTitle: "",
  port: "",
  recordType: "",
  serviceName: "",
  url: "",
};

const IPV4_PATTERN = /^\d{1,3}(\.\d{1,3}){3}$/;
const URL_PREFIX_PATTERN = /^https?:\/\//i;

function isIPv4(value: string): boolean {
  const text = value.trim();
  if (!IPV4_PATTERN.test(text)) return false;
  return text.split(".").every((part) => Number(part) <= 255);
}

// 逗号分隔列表(兼容中文逗号)。
function splitList(value: string): string[] {
  return value
    .split(/[,，]/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function Field({
  children,
  className,
  hint,
  id,
  label,
  required,
}: {
  children: React.ReactNode;
  className?: string;
  hint?: string;
  id: string;
  label: string;
  required?: boolean;
}) {
  return (
    <div className={cn("flex flex-col gap-1.5", className)}>
      <Label className="text-xs" htmlFor={id}>
        {label}
        {required ? <span className="text-destructive">*</span> : null}
      </Label>
      {children}
      {hint ? <p className="text-muted-foreground text-xs">{hint}</p> : null}
    </div>
  );
}

// 组装 TaskAssetUpsert(字段名与 POST /api/assets 内层 assets[] 一致):
// 端口服务的 host 是 IPv4 时改走 type=ip + open_ports,让端口直接落在
// IP 资产的开放端口上;是域名时走 type=service 的 UpsertOtherService。
// (提为模块级函数:port/portValid 由调用方先校验,这里直接信任。)
function buildArchivePayload(
  kind: ArchiveKind,
  fields: ArchiveFields,
  boundDomains: string[],
  portNumber: number,
): TaskAssetUpsert {
  const trim = (value: string) => value.trim();
  switch (kind) {
    case "root_domain":
      return {
        type: "root_domain",
        domain: trim(fields.domain),
        ...(trim(fields.icp) ? { icp: trim(fields.icp) } : {}),
      };
    case "subdomain":
      return {
        type: "subdomain",
        domain: trim(fields.domain),
        ...(trim(fields.recordType) ? { record_type: trim(fields.recordType) } : {}),
      };
    case "ip":
      return {
        type: "ip",
        ip: trim(fields.ip),
        ...(boundDomains.length > 0 ? { bound_domains: boundDomains } : {}),
      };
    case "http":
      return {
        type: "service",
        url: trim(fields.url),
        ...(trim(fields.pageTitle) ? { page_title: trim(fields.pageTitle) } : {}),
      };
    case "port": {
      const host = trim(fields.host);
      const service = trim(fields.serviceName);
      if (isIPv4(host)) {
        return { type: "ip", ip: host, open_ports: [{ port: portNumber, ...(service ? { service } : {}) }] };
      }
      return { type: "service", domain: host, port: portNumber, ...(service ? { service_name: service } : {}) };
    }
    case "app":
      return {
        type: "app",
        app_name: trim(fields.appName),
        ...(trim(fields.url) ? { url: trim(fields.url) } : {}),
      };
  }
}

export function TaskAssetArchive({
  onArchived,
  onOpenChange,
  open,
  taskId,
}: {
  taskId: string;
  onArchived: () => void;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const [kind, setKind] = React.useState<ArchiveKind>("root_domain");
  const [fields, setFields] = React.useState<ArchiveFields>(EMPTY_FIELDS);
  const [saving, setSaving] = React.useState(false);

  React.useEffect(() => {
    if (!open) return;
    setKind("root_domain");
    setFields(EMPTY_FIELDS);
  }, [open]);

  const set = (key: keyof ArchiveFields) => (value: string) => setFields((current) => ({ ...current, [key]: value }));

  const portNumber = Number(fields.port.trim());
  const portValid = Number.isInteger(portNumber) && portNumber >= 1 && portNumber <= 65535;
  const boundDomains = splitList(fields.boundDomains);
  const domainFilled = fields.domain.trim() !== "";

  const inputValid =
    (kind === "root_domain" && domainFilled) ||
    (kind === "subdomain" && domainFilled) ||
    (kind === "ip" && isIPv4(fields.ip)) ||
    (kind === "http" && URL_PREFIX_PATTERN.test(fields.url.trim())) ||
    (kind === "port" && fields.host.trim() !== "" && portValid) ||
    (kind === "app" && fields.appName.trim() !== "");

  const submit = async () => {
    if (!inputValid || saving) return;
    setSaving(true);
    try {
      const payload = buildArchivePayload(kind, fields, boundDomains, portNumber);
      const result = await api.archiveTaskAsset(taskId, payload);
      // 后端 200 也可能带逐条 errors(HTTP 200 + errors 非空 = 入库失败)。
      if (result.errors && result.errors.length > 0) throw new Error(result.errors[0].error);
      toast.success("已归档");
      onArchived();
      onOpenChange(false);
    } catch (reason) {
      toast.error(`归档失败：${String((reason as Error)?.message ?? reason)}`);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full sm:max-w-md">
        <SheetHeader>
          <SheetTitle>归档资产</SheetTitle>
          <SheetDescription>手动录入一条资产直接入库并关联当前任务；已存在的同类资产会被合并更新。</SheetDescription>
        </SheetHeader>
        <div className="flex min-h-0 flex-1 flex-col overflow-y-auto px-4">
          <div className="grid grid-cols-2 gap-3">
            <Field className="col-span-2" id="task-asset-archive-kind" label="资产类型" required>
              <Select value={kind} onValueChange={(value) => setKind(value as ArchiveKind)}>
                <SelectTrigger className="w-full" id="task-asset-archive-kind">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {ARCHIVE_KINDS.map((item) => (
                      <SelectItem key={item.value} value={item.value}>
                        {item.label}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </Field>

            {(kind === "root_domain" || kind === "subdomain") && (
              <Field
                className="col-span-2"
                hint={kind === "subdomain" ? "完整子域名，如 api.example.com" : undefined}
                id="task-asset-archive-domain"
                label="域名"
                required
              >
                <Input
                  id="task-asset-archive-domain"
                  value={fields.domain}
                  onChange={(event) => set("domain")(event.target.value)}
                  placeholder="example.com"
                />
              </Field>
            )}
            {kind === "root_domain" && (
              <Field className="col-span-2" id="task-asset-archive-icp" label="ICP 备案">
                <Input
                  id="task-asset-archive-icp"
                  value={fields.icp}
                  onChange={(event) => set("icp")(event.target.value)}
                  placeholder="可选，如 京ICP备2020xxxxxx号"
                />
              </Field>
            )}
            {kind === "subdomain" && (
              <Field className="col-span-2" id="task-asset-archive-record-type" label="解析类型">
                <Input
                  id="task-asset-archive-record-type"
                  value={fields.recordType}
                  onChange={(event) => set("recordType")(event.target.value)}
                  placeholder="可选，如 A / CNAME / TXT"
                />
              </Field>
            )}

            {kind === "ip" && (
              <>
                <Field className="col-span-2" hint="IPv4" id="task-asset-archive-ip" label="IP 地址" required>
                  <Input
                    id="task-asset-archive-ip"
                    value={fields.ip}
                    onChange={(event) => set("ip")(event.target.value)}
                    placeholder="192.0.2.10"
                  />
                </Field>
                <Field className="col-span-2" id="task-asset-archive-bound-domains" label="绑定域名">
                  <Input
                    id="task-asset-archive-bound-domains"
                    value={fields.boundDomains}
                    onChange={(event) => set("boundDomains")(event.target.value)}
                    placeholder="可选，多个用英文逗号分隔"
                  />
                </Field>
              </>
            )}

            {kind === "http" && (
              <>
                <Field
                  className="col-span-2"
                  hint="须以 http:// 或 https:// 开头"
                  id="task-asset-archive-url"
                  label="URL"
                  required
                >
                  <Input
                    id="task-asset-archive-url"
                    value={fields.url}
                    onChange={(event) => set("url")(event.target.value)}
                    placeholder="https://example.com:8443/admin"
                  />
                </Field>
                <Field className="col-span-2" id="task-asset-archive-page-title" label="页面标题">
                  <Input
                    id="task-asset-archive-page-title"
                    value={fields.pageTitle}
                    onChange={(event) => set("pageTitle")(event.target.value)}
                    placeholder="可选"
                  />
                </Field>
              </>
            )}

            {kind === "port" && (
              <>
                <Field
                  className="col-span-2"
                  hint="域名或 IP；IP 会连同端口一起记入开放端口"
                  id="task-asset-archive-host"
                  label="Host"
                  required
                >
                  <Input
                    id="task-asset-archive-host"
                    value={fields.host}
                    onChange={(event) => set("host")(event.target.value)}
                    placeholder="example.com 或 192.0.2.10"
                  />
                </Field>
                <Field id="task-asset-archive-port" label="端口" required>
                  <Input
                    id="task-asset-archive-port"
                    inputMode="numeric"
                    value={fields.port}
                    onChange={(event) => set("port")(event.target.value)}
                    placeholder="1-65535"
                  />
                </Field>
                <Field id="task-asset-archive-service-name" label="服务名称">
                  <Input
                    id="task-asset-archive-service-name"
                    value={fields.serviceName}
                    onChange={(event) => set("serviceName")(event.target.value)}
                    placeholder="可选，如 redis"
                  />
                </Field>
              </>
            )}

            {kind === "app" && (
              <>
                <Field className="col-span-2" id="task-asset-archive-app-name" label="应用名称" required>
                  <Input
                    id="task-asset-archive-app-name"
                    value={fields.appName}
                    onChange={(event) => set("appName")(event.target.value)}
                    placeholder="如 客户端 App"
                  />
                </Field>
                <Field className="col-span-2" id="task-asset-archive-app-url" label="地址">
                  <Input
                    id="task-asset-archive-app-url"
                    value={fields.url}
                    onChange={(event) => set("url")(event.target.value)}
                    placeholder="可选，应用下载或访问地址"
                  />
                </Field>
              </>
            )}
          </div>
        </div>
        <SheetFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={saving}>
            取消
          </Button>
          <Button onClick={() => void submit()} disabled={saving || !inputValid}>
            {saving ? <Spinner data-icon="inline-start" /> : <ArchiveIcon data-icon="inline-start" />}
            归档
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  );
}
