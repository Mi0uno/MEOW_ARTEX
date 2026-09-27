"use client";

// 代理中心共用对话框:代理组新建/编辑、批量录入代理、接入代理池 API。
// 「代理池」(组列表) / 「代理组详情」 / 「仪表盘」三个页面共用同一份,避免行为漂移;
// 徽标样式与中文标签也收在这里,保证同一状态在各页面上的颜色与叫法完全一致。

import * as React from "react";

import { ExternalLinkIcon } from "lucide-react";
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
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { ProxyGroup, ProxyProviderDef, ProxySource } from "@/lib/types";

// ---- 展示映射(徽标样式 + 中文标签) ----

// 未测/未知状态的统一样式:中性灰,不用颜色暗示好坏。
const MUTED_BADGE = "bg-muted text-muted-foreground";

const GROUP_KIND_BADGE: Record<string, string> = {
  manual: "bg-blue-500/15 text-blue-600 border-blue-500/30",
  api: "bg-purple-500/15 text-purple-600 border-purple-500/30",
  wild: "bg-amber-500/15 text-amber-600 border-amber-500/30",
  mixed: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30",
};

const GROUP_KIND_LABEL: Record<string, string> = {
  manual: "手工",
  api: "API 接入",
  wild: "在野搜集",
  mixed: "混合",
};

const PROXY_STATUS_BADGE: Record<string, string> = {
  untested: MUTED_BADGE,
  alive: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30",
  dead: "bg-red-500/15 text-red-600 border-red-500/30",
  checking: "bg-amber-500/15 text-amber-600 border-amber-500/30",
};

const STRATEGY_LABEL: Record<string, string> = {
  round_robin: "轮询",
  random: "随机",
  least_latency: "最低延迟优先",
  failover: "故障转移",
  quality: "质量加权",
};

const SOURCE_LABEL: Record<string, string> = {
  manual: "手工",
  api: "代理池 API",
  public: "公益池",
  wild: "在野搜集",
};

const ANONYMITY_LABEL: Record<string, string> = {
  transparent: "透明",
  anonymous: "匿名",
  elite: "高匿",
};

const PROVIDER_KIND_LABEL: Record<string, string> = {
  commercial: "商业代理池",
  public: "公益代理池",
  wild: "全网在野搜集",
};

// groupKindBadgeClass 返回代理组类型的徽标样式(未匹配时按「混合」兜底,不报错)。
export function groupKindBadgeClass(kind: string): string {
  return GROUP_KIND_BADGE[kind] ?? GROUP_KIND_BADGE.mixed;
}

// proxyStatusBadgeClass 返回测活状态的徽标样式(未匹配时按「未测」兜底)。
export function proxyStatusBadgeClass(status: string): string {
  return PROXY_STATUS_BADGE[status] ?? MUTED_BADGE;
}

// strategyLabel 返回调度策略中文名(未知值原样返回,便于排查)。
export function strategyLabel(s: string): string {
  return STRATEGY_LABEL[s] ?? s;
}

// sourceLabel 返回代理来源中文名(未知值原样返回)。
export function sourceLabel(s: string): string {
  return SOURCE_LABEL[s] ?? s;
}

// anonymityLabel 返回匿名度中文名;空串=还没测出匿名度。
export function anonymityLabel(a: string): string {
  if (!a) return "未测";
  return ANONYMITY_LABEL[a] ?? a;
}

// fmtLatency 渲染测活延迟;0=未测(后端约定),显示破折号而不是「0 ms」。
export function fmtLatency(ms: number): string {
  if (!ms || ms <= 0) return "—";
  return `${ms} ms`;
}

// groupKindLabel 返回代理组类型中文名。
export function groupKindLabel(kind: string): string {
  return GROUP_KIND_LABEL[kind] ?? kind;
}

// ProxyGroupDialog 新建/编辑代理组(策略、监听端口、测活参数)。
export function ProxyGroupDialog({
  group,
  open,
  onOpenChange,
  onSaved,
}: {
  group: ProxyGroup | null;
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onSaved: () => void;
}): React.JSX.Element {
  const [form, setForm] = React.useState<Partial<ProxyGroup>>({});
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    if (!open) return;
    if (group) {
      // 列表行只带派生计数,策略/测活参数要读整条记录;失败时退回行数据,至少能编辑已有字段。
      api
        .proxyGroup(group.id)
        .then((g) => setForm(g ?? group))
        .catch(() => setForm(group));
    } else {
      setForm({
        name: "",
        kind: "mixed",
        strategy: "round_robin",
        listen_port: 0,
        enabled: true,
        auto_check: true,
        check_interval_minutes: 10,
        timeout_ms: 10000,
        max_failures: 3,
        concurrency: 20,
        test_url: "http://httpbin.org/ip",
        remark: "",
      });
    }
  }, [open, group]);

  const set = (k: keyof ProxyGroup, v: unknown) => setForm((f) => ({ ...f, [k]: v }));
  // 数字输入框写回:空串按 0 处理(0 在超时/阈值等字段上由后端补默认值)。
  const setNum = (k: keyof ProxyGroup, v: string) => {
    const n = Number(v);
    set(k, v === "" || !Number.isFinite(n) ? 0 : n);
  };

  const submit = async () => {
    if (!String(form.name ?? "").trim()) {
      toast.error("请填写组名");
      return;
    }
    const port = Number(form.listen_port ?? 0);
    if (port < 0 || port > 65535) {
      toast.error("监听端口必须在 0-65535(0=自动分配)");
      return;
    }
    setBusy(true);
    try {
      if (group) await api.updateProxyGroup(group.id, form);
      else await api.createProxyGroup(form);
      toast.success(group ? "已保存" : "已新建代理组");
      onSaved();
    } catch (e) {
      toast.error(`保存失败:${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[88vh] overflow-y-auto sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{group ? "编辑代理组" : "新建代理组"}</DialogTitle>
          <DialogDescription>
            代理组是调度的单位:组内代理按这里的策略被轮换使用,自动测活按间隔在后台跑。
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid grid-cols-3 gap-3">
            <div className="col-span-2 grid gap-1.5">
              <Label>组名</Label>
              <Input
                value={String(form.name ?? "")}
                onChange={(e) => set("name", e.target.value)}
                placeholder="例如:国内高匿池 / 海外 HTTP 池"
              />
            </div>
            <div className="grid gap-1.5">
              <Label>组类型</Label>
              <Select value={String(form.kind ?? "mixed")} onValueChange={(v) => set("kind", v)}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    <SelectItem value="manual">手工</SelectItem>
                    <SelectItem value="api">API 接入</SelectItem>
                    <SelectItem value="wild">在野搜集</SelectItem>
                    <SelectItem value="mixed">混合</SelectItem>
                  </SelectGroup>
                </SelectContent>
              </Select>
            </div>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div className="grid gap-1.5">
              <Label>调度策略</Label>
              <Select value={String(form.strategy ?? "round_robin")} onValueChange={(v) => set("strategy", v)}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    <SelectItem value="round_robin">轮询</SelectItem>
                    <SelectItem value="random">随机</SelectItem>
                    <SelectItem value="least_latency">最低延迟优先</SelectItem>
                    <SelectItem value="failover">故障转移</SelectItem>
                    <SelectItem value="quality">质量加权</SelectItem>
                  </SelectGroup>
                </SelectContent>
              </Select>
              <p className="text-muted-foreground text-xs">
                轮询=按顺序依次取用;随机=每次随机挑一个;最低延迟优先=优先用最近测活延迟最低的;故障转移=当前可用就持续用,失败才切下一个;质量加权=按权重随机:优先级×2
                + 连续存活加成(封顶20) + 低延迟加成
              </p>
            </div>
            <div className="grid gap-1.5">
              <Label>监听端口</Label>
              <Input
                type="number"
                min={0}
                max={65535}
                value={String(form.listen_port ?? 0)}
                onChange={(e) => setNum("listen_port", e.target.value)}
                className="font-mono"
              />
              <p className="text-muted-foreground text-xs">0 = 自动分配(18080 起)</p>
            </div>
          </div>

          <div className="grid gap-1.5">
            <Label>测活目标 URL</Label>
            <Input
              value={String(form.test_url ?? "")}
              onChange={(e) => set("test_url", e.target.value)}
              placeholder="http://httpbin.org/ip"
              className="font-mono"
            />
            <p className="text-muted-foreground text-xs">
              需能回显出口 IP,默认 http://httpbin.org/ip;测活同时用它判断匿名度(透明/匿名/高匿)
            </p>
          </div>

          <div className="grid grid-cols-4 gap-3">
            <div className="grid gap-1.5">
              <Label>超时 ms</Label>
              <Input
                type="number"
                min={0}
                value={String(form.timeout_ms ?? 0)}
                onChange={(e) => setNum("timeout_ms", e.target.value)}
                className="tabular-nums"
              />
            </div>
            <div className="grid gap-1.5">
              <Label>连续失败阈值</Label>
              <Input
                type="number"
                min={0}
                value={String(form.max_failures ?? 0)}
                onChange={(e) => setNum("max_failures", e.target.value)}
                className="tabular-nums"
              />
            </div>
            <div className="grid gap-1.5">
              <Label>并发</Label>
              <Input
                type="number"
                min={0}
                value={String(form.concurrency ?? 0)}
                onChange={(e) => setNum("concurrency", e.target.value)}
                className="tabular-nums"
              />
            </div>
            <div className="grid gap-1.5">
              <Label>自动测活间隔分钟</Label>
              <Input
                type="number"
                min={0}
                value={String(form.check_interval_minutes ?? 0)}
                onChange={(e) => setNum("check_interval_minutes", e.target.value)}
                className="tabular-nums"
              />
            </div>
          </div>

          {/* Switch 渲染成 button,<label> 关联不到它(可访问名由 aria-label 提供),故用 div 排版。 */}
          <div className="flex flex-wrap items-center gap-6">
            <div className="flex items-center gap-2 text-sm">
              <Switch checked={!!form.auto_check} onCheckedChange={(v) => set("auto_check", v)} aria-label="自动测活" />
              自动测活
            </div>
            <div className="flex items-center gap-2 text-sm">
              <Switch checked={!!form.enabled} onCheckedChange={(v) => set("enabled", v)} aria-label="启用" />
              启用
            </div>
            <span className="text-muted-foreground text-xs">停用后该组不参与出网调度,组内代理与接入配置保留</span>
          </div>

          <div className="grid gap-1.5">
            <Label>备注</Label>
            <Textarea
              value={String(form.remark ?? "")}
              onChange={(e) => set("remark", e.target.value)}
              rows={2}
              placeholder="用途、来源渠道、注意事项…"
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button disabled={busy} onClick={() => void submit()}>
            {busy ? "保存中…" : "保存"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// 单次批量录入的条数上限:再多应改用代理池 API 接入(逐条写库会拖垮浏览器)。
const MAX_BATCH = 2000;

// parseProxyLines 把粘贴内容拆成代理地址:按换行/逗号/空格/分号切分,丢掉空项与 # 注释,
// 缺协议的补 http://(与后端 ParseProxyURL 的归一化一致),并去掉本批次内的重复。
function parseProxyLines(text: string): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  for (const raw of text.split(/[\s,;]+/)) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    const url = line.includes("://") ? line : `http://${line}`;
    if (seen.has(url)) continue;
    seen.add(url);
    out.push(url);
    if (out.length >= MAX_BATCH) break;
  }
  return out;
}

// ProxyEntryDialog 批量录入代理:一行一条粘贴,逐条写库并统计去重结果。
export function ProxyEntryDialog({
  groupId,
  open,
  onOpenChange,
  onSaved,
}: {
  groupId: number;
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onSaved: () => void;
}): React.JSX.Element {
  const [text, setText] = React.useState("");
  const [label, setLabel] = React.useState("");
  const [region, setRegion] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [done, setDone] = React.useState(0);

  const urls = React.useMemo(() => parseProxyLines(text), [text]);

  React.useEffect(() => {
    if (open) return;
    // 关掉即清空:下次打开是干净的一次批量录入,不会误把上一批再导一遍。
    setText("");
    setLabel("");
    setRegion("");
    setDone(0);
  }, [open]);

  const submit = async () => {
    if (urls.length === 0) {
      toast.error("请粘贴至少一条代理地址");
      return;
    }
    setBusy(true);
    setDone(0);
    let added = 0;
    let dup = 0;
    let failed = 0;
    // 串行写入:代理池 API 拉来的量级也就几千条,串行能让进度可见且不给后端制造并发压力。
    for (const url of urls) {
      try {
        const r = await api.createProxy(groupId, { url, label: label.trim(), region: region.trim() });
        if (r.existing) dup++;
        else added++;
      } catch {
        failed++;
      }
      setDone((n) => n + 1);
    }
    setBusy(false);
    toast.success(`导入 ${added} 条(跳过重复 ${dup} 条)`);
    if (failed > 0) toast.error(`${failed} 条导入失败:请检查地址是否为 host:port / http:// / socks5:// 形式`);
    onSaved();
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[88vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>批量添加代理</DialogTitle>
          <DialogDescription>
            每行一条:host:port、http://host:port、user:pass@host:port、socks5://host:port 都认; # 开头的行按注释忽略
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid gap-1.5">
            <Label>代理地址(一行一条)</Label>
            <Textarea
              className="min-h-48 font-mono text-xs"
              value={text}
              onChange={(e) => setText(e.target.value)}
              placeholder={"127.0.0.1:8080\nhttp://1.2.3.4:3128\nuser:pass@5.6.7.8:1080\nsocks5://9.10.11.12:1080"}
            />
            <p className="text-muted-foreground text-xs tabular-nums">
              已识别 {urls.length} 条{urls.length >= MAX_BATCH ? `(单次上限 ${MAX_BATCH} 条,超出部分请分批导入)` : ""}
            </p>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div className="grid gap-1.5">
              <Label>备注 / 标签(本批统一)</Label>
              <Input value={label} onChange={(e) => setLabel(e.target.value)} placeholder="例如:快代理-20260924" />
            </div>
            <div className="grid gap-1.5">
              <Label>地区(本批统一)</Label>
              <Input value={region} onChange={(e) => setRegion(e.target.value)} placeholder="例如:国内 / 美国 / 全球" />
            </div>
          </div>
          <p className="text-muted-foreground text-xs">
            已存在的同地址会自动跳过(按组内地址去重);导入后可在详情页选中批量测活
          </p>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy}>
            取消
          </Button>
          <Button disabled={busy || urls.length === 0} onClick={() => void submit()}>
            {busy ? `导入中… ${done}/${urls.length}` : `导入 ${urls.length} 条`}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// CUSTOM_KEY 是「不用内置源、自己填地址」在 Select 里的取值(提交时还原成空串)。
const CUSTOM_KEY = "__custom__";

// ProxySourceDialog 接入代理池 API(商业 / 公益 / 在野搜集三类内置源)。
export function ProxySourceDialog({
  groupId,
  source,
  open,
  onOpenChange,
  onSaved,
}: {
  groupId: number;
  source: ProxySource | null;
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onSaved: () => void;
}): React.JSX.Element {
  const [providers, setProviders] = React.useState<ProxyProviderDef[]>([]);
  const [providerErr, setProviderErr] = React.useState("");
  const providersLoadedRef = React.useRef(false);
  const [form, setForm] = React.useState({
    provider_key: "",
    name: "",
    api_url: "",
    api_key: "",
    username: "",
    password: "",
    interval_minutes: 30,
    enabled: true,
  });
  // 免凭据的源默认不显示 Key/账密输入框;用户要覆盖时点开(编辑已配置的源则直接展开)。
  const [showKey, setShowKey] = React.useState(false);
  const [showAuth, setShowAuth] = React.useState(false);
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    // 首次打开即拉一次注册表(它是静态的,拿到就缓存住);若失败,下次打开时再重试。
    if (!open || providersLoadedRef.current) return;
    let alive = true;
    api
      .proxyProviders()
      .then((r) => {
        if (!alive) return;
        setProviders(r ?? []);
        setProviderErr("");
        providersLoadedRef.current = true;
      })
      .catch((e) => {
        if (alive) setProviderErr((e as Error).message);
      });
    return () => {
      alive = false;
    };
  }, [open]);

  React.useEffect(() => {
    if (!open) return;
    if (source) {
      setForm({
        provider_key: source.provider_key || CUSTOM_KEY,
        name: source.name ?? "",
        api_url: source.api_url ?? "",
        // 密文永不回显:留空=不改动,只发用户这次真正输入的凭据。
        api_key: "",
        username: source.username ?? "",
        password: "",
        interval_minutes: source.interval_minutes ?? 30,
        enabled: !!source.enabled,
      });
      setShowKey(!!source.has_api_key);
      setShowAuth(!!source.has_password);
    } else {
      setForm({
        provider_key: "",
        name: "",
        api_url: "",
        api_key: "",
        username: "",
        password: "",
        interval_minutes: 30,
        enabled: true,
      });
      setShowKey(false);
      setShowAuth(false);
    }
  }, [open, source]);

  const set = (k: keyof typeof form, v: unknown) => setForm((f) => ({ ...f, [k]: v }));

  const provider = providers.find((p) => p.key === form.provider_key);
  const isCustom = form.provider_key === CUSTOM_KEY;
  const keyVisible = showKey || isCustom || !!provider?.need_key;
  const authVisible = showAuth || !!provider?.need_auth;

  const pickProvider = (key: string) => {
    if (key === CUSTOM_KEY) {
      setForm((f) => ({ ...f, provider_key: CUSTOM_KEY, name: f.name || "自定义代理源" }));
      setShowKey(true);
      return;
    }
    const def = providers.find((p) => p.key === key);
    // 选中内置源即回填名称与默认地址(地址仍可改,便于指向自建中转或镜像)。
    setForm((f) => ({
      ...f,
      provider_key: key,
      name: def?.title ?? f.name,
      api_url: def?.default_url ?? f.api_url,
    }));
    setShowKey(!!def?.need_key);
    setShowAuth(!!def?.need_auth);
  };

  const submit = async () => {
    const name = form.name.trim();
    if (!name) {
      toast.error("请填写名称");
      return;
    }
    if (!form.provider_key && !form.api_url.trim()) {
      toast.error("请选择内置代理源或填写 API 地址");
      return;
    }
    if (!source && provider?.need_key && !form.api_key.trim()) {
      toast.error(`${provider.title} 需要 API Key`);
      return;
    }
    const payload: Record<string, unknown> = {
      provider_key: form.provider_key === CUSTOM_KEY ? "" : form.provider_key,
      name,
      api_url: form.api_url.trim(),
      username: form.username.trim(),
      interval_minutes: Number(form.interval_minutes) || 0,
      enabled: form.enabled,
    };
    // 编辑时留空=保留已存凭据(后端对空值不覆盖),所以空串一律不发。
    if (form.api_key.trim()) payload.api_key = form.api_key.trim();
    if (form.password.trim()) payload.password = form.password.trim();
    setBusy(true);
    try {
      if (source) await api.updateProxySource(source.id, payload);
      else await api.createProxySource(groupId, payload);
      toast.success(source ? "已保存" : "已接入");
      onSaved();
    } catch (e) {
      toast.error(`保存失败:${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[88vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{source ? "编辑代理池接入" : "接入代理池 API"}</DialogTitle>
          <DialogDescription>
            选一个内置源即可,拉取与解析由后端按源站规则处理;地址可改成自建中转或镜像。
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid gap-1.5">
            <Label>代理源</Label>
            <Select value={form.provider_key} onValueChange={pickProvider}>
              <SelectTrigger className="w-full">
                <SelectValue placeholder="选择内置代理源(或选「自定义」自己填地址)" />
              </SelectTrigger>
              <SelectContent>
                {["commercial", "public", "wild"].map((kind) => {
                  const items = providers.filter((p) => p.kind === kind);
                  if (items.length === 0) return null;
                  return (
                    <SelectGroup key={kind}>
                      <SelectLabel>{PROVIDER_KIND_LABEL[kind] ?? kind}</SelectLabel>
                      {items.map((p) => (
                        <SelectItem key={p.key} value={p.key}>
                          {p.region ? `${p.title} · ${p.region}` : p.title}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  );
                })}
                <SelectGroup>
                  <SelectLabel>自定义</SelectLabel>
                  <SelectItem value={CUSTOM_KEY}>自定义 API(手工填写地址)</SelectItem>
                </SelectGroup>
                {/* 已保存的源若引用了注册表里已下线的 key,补一个兜底项,否则触发器会显示空白。 */}
                {form.provider_key && form.provider_key !== CUSTOM_KEY && !provider && (
                  <SelectGroup>
                    <SelectLabel>已下线 / 未知源</SelectLabel>
                    <SelectItem value={form.provider_key}>{form.provider_key}(不在内置注册表中)</SelectItem>
                  </SelectGroup>
                )}
              </SelectContent>
            </Select>
            {providers.length === 0 && !providerErr && (
              <p className="text-muted-foreground text-xs">正在加载内置代理源注册表…</p>
            )}
            {providerErr && (
              <p className="text-red-600 text-xs">
                内置代理源注册表加载失败:{providerErr};仍可选「自定义 API」手工填地址
              </p>
            )}
          </div>

          {provider && (
            <div className="bg-muted/40 grid gap-1.5 rounded-lg border p-3 text-xs">
              <div className="flex flex-wrap items-center gap-2">
                <Badge variant="outline" className={MUTED_BADGE}>
                  {PROVIDER_KIND_LABEL[provider.kind] ?? provider.kind}
                </Badge>
                <Badge variant="outline" className={MUTED_BADGE}>
                  {provider.protocol || "http"}
                </Badge>
                {provider.need_key ? (
                  <Badge variant="outline" className="bg-amber-500/15 text-amber-600 border-amber-500/30">
                    需要 API Key
                  </Badge>
                ) : null}
                {provider.need_auth ? (
                  <Badge variant="outline" className="bg-amber-500/15 text-amber-600 border-amber-500/30">
                    需要用户名密码
                  </Badge>
                ) : null}
                {!provider.need_key && !provider.need_auth ? (
                  <Badge variant="outline" className="bg-emerald-500/15 text-emerald-600 border-emerald-500/30">
                    免凭据
                  </Badge>
                ) : null}
              </div>
              <p className="text-muted-foreground">
                地区倾向:{provider.region || "不限"} · 响应解析:{provider.parse || "自动"}
              </p>
              {(provider.params?.length ?? 0) > 0 && (
                <p className="text-muted-foreground">可选参数:{provider.params?.join("、")}</p>
              )}
              <div className="flex flex-wrap gap-3">
                {provider.docs && (
                  <a
                    href={provider.docs}
                    target="_blank"
                    rel="noreferrer"
                    className="text-blue-600 inline-flex items-center gap-1 hover:underline"
                  >
                    接口文档 <ExternalLinkIcon className="size-3" />
                  </a>
                )}
                {provider.homepage && (
                  <a
                    href={provider.homepage}
                    target="_blank"
                    rel="noreferrer"
                    className="text-blue-600 inline-flex items-center gap-1 hover:underline"
                  >
                    官网 <ExternalLinkIcon className="size-3" />
                  </a>
                )}
              </div>
            </div>
          )}

          <div className="grid grid-cols-2 gap-3">
            <div className="grid gap-1.5">
              <Label>名称</Label>
              <Input
                value={form.name}
                onChange={(e) => set("name", e.target.value)}
                placeholder="显示在接入列表里的名字"
              />
            </div>
            <div className="grid gap-1.5">
              <Label>拉取间隔(分钟)</Label>
              <Input
                type="number"
                min={0}
                value={String(form.interval_minutes)}
                onChange={(e) => set("interval_minutes", Number(e.target.value) || 0)}
                className="tabular-nums"
              />
              <p className="text-muted-foreground text-xs">0 = 只手动拉取</p>
            </div>
          </div>

          <div className="grid gap-1.5">
            <Label>API 地址</Label>
            <Input
              value={form.api_url}
              onChange={(e) => set("api_url", e.target.value)}
              placeholder="https://…"
              className="font-mono text-xs"
            />
            <p className="text-muted-foreground text-xs">默认用内置源站地址,可改成自建中转或镜像</p>
          </div>

          {keyVisible ? (
            <div className="grid gap-1.5">
              <Label>API Key</Label>
              <Input
                type="password"
                autoComplete="new-password"
                value={form.api_key}
                onChange={(e) => set("api_key", e.target.value)}
                placeholder={source?.has_api_key ? "已配置(留空则不修改)" : "粘贴代理池控制台的 Key"}
              />
              <p className="text-muted-foreground text-xs">
                凭据加密存储、不回显,也不进模型上下文;留空表示不改动已存值
              </p>
            </div>
          ) : (
            <button
              type="button"
              className="text-muted-foreground hover:text-foreground justify-self-start text-xs underline"
              onClick={() => setShowKey(true)}
            >
              该源免 Key,如需追加凭据点此配置
            </button>
          )}

          {authVisible ? (
            <div className="grid grid-cols-2 gap-3">
              <div className="grid gap-1.5">
                <Label>用户名</Label>
                <Input value={form.username} onChange={(e) => set("username", e.target.value)} />
              </div>
              <div className="grid gap-1.5">
                <Label>密码</Label>
                <Input
                  type="password"
                  autoComplete="new-password"
                  value={form.password}
                  onChange={(e) => set("password", e.target.value)}
                  placeholder={source?.has_password ? "已配置(留空则不修改)" : "拉取账号的密码"}
                />
              </div>
            </div>
          ) : (
            <button
              type="button"
              className="text-muted-foreground hover:text-foreground justify-self-start text-xs underline"
              onClick={() => setShowAuth(true)}
            >
              该源免账密,如需追加凭据点此配置
            </button>
          )}

          <div className="flex items-center gap-2 text-sm">
            <Switch checked={form.enabled} onCheckedChange={(v) => set("enabled", v)} aria-label="启用" />
            启用(按间隔自动拉取;停用后仍可手动同步)
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button disabled={busy} onClick={() => void submit()}>
            {busy ? "保存中…" : "保存"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
