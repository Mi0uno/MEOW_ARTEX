"use client";

import * as React from "react";

import { FlaskConicalIcon, RadioTowerIcon, SearchIcon, ShieldAlertIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import type { Settings } from "@/lib/types";

// 系统功能：全局运行时开关与功能配置，一行一项的紧凑布局。
// 与"系统设置"（账户/更新/界面偏好）分离——这里改的是 Agent 的运行行为。

export default function SystemFunctionsPage() {
  const [trafficCapture, setTrafficCapture] = React.useState(false);
  const [agentTrafficBinding, setAgentTrafficBinding] = React.useState(false);
  const [webSearch, setWebSearch] = React.useState(false);
  const [backend, setBackend] = React.useState("ddgs");
  const [braveKeySet, setBraveKeySet] = React.useState(false);
  const [braveKeyInput, setBraveKeyInput] = React.useState("");
  const [tavilyKeySet, setTavilyKeySet] = React.useState(false);
  const [tavilyKeyInput, setTavilyKeyInput] = React.useState("");
  const [savingTavilyKey, setSavingTavilyKey] = React.useState(false);
  const [proxyInput, setProxyInput] = React.useState("");
  const [savingProxy, setSavingProxy] = React.useState(false);
  const [globalProxyInput, setGlobalProxyInput] = React.useState("");
  const [savingGlobalProxy, setSavingGlobalProxy] = React.useState(false);
  const [testing, setTesting] = React.useState(false);
  const [loaded, setLoaded] = React.useState(false);
  const [saving, setSaving] = React.useState(false);
  const [savingKey, setSavingKey] = React.useState(false);
  const [pyInterp, setPyInterp] = React.useState("");
  const [workers, setWorkers] = React.useState("3");
  const [savingWorkers, setSavingWorkers] = React.useState(false);
  // 操作约束注入范围(默认都开)。
  const [injectPlanner, setInjectPlanner] = React.useState(true);
  const [injectWorker, setInjectWorker] = React.useState(true);
  // 实验功能:noa 上下文压缩(默认关)。
  const [noaCompaction, setNoaCompaction] = React.useState(false);

  const apply = React.useCallback((s: Settings) => {
    setTrafficCapture(!!s.traffic_capture);
    setAgentTrafficBinding(!!s.agent_traffic_binding);
    setWebSearch(!!s.web_search_enabled);
    setBackend(s.web_search_backend || "ddgs");
    setBraveKeySet(!!s.brave_key_set);
    setTavilyKeySet(!!s.tavily_key_set);
    setProxyInput(s.web_search_proxy ?? "");
    setGlobalProxyInput(s.global_proxy ?? "");
    setPyInterp(s.python_interpreter ?? "");
    setWorkers(String(s.workers ?? 3));
    setInjectPlanner(s.constraints_inject_planner !== false);
    setInjectWorker(s.constraints_inject_worker !== false);
    setNoaCompaction(!!s.noa_compaction);
  }, []);

  const saveWorkers = () => {
    const n = Number(workers);
    if (!Number.isInteger(n) || n <= 0) {
      toast.error("并发数必须是大于 0 的整数");
      return;
    }
    setSavingWorkers(true);
    api
      .setSettings({ workers: n })
      .then((s) => {
        apply(s);
        toast.success("已保存并发工作 agent 数（对之后启动的任务生效）");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
      .finally(() => setSavingWorkers(false));
  };

  const savePython = () => {
    api
      .setSettings({ python_interpreter: pyInterp.trim() })
      .then((s) => {
        apply(s);
        toast.success("已保存 Python 解释器配置");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message));
  };
  const detectPython = () => {
    api
      .detectPython()
      .then((r) => setPyInterp(r.python_interpreter))
      .catch(() => undefined);
  };

  React.useEffect(() => {
    api
      .settings()
      .then(apply)
      .catch(() => undefined)
      .finally(() => setLoaded(true));
  }, [apply]);

  const toggleTraffic = (v: boolean) => {
    setTrafficCapture(v); // optimistic
    setSaving(true);
    api
      .setSettings({ traffic_capture: v })
      .then(apply)
      .catch(() => setTrafficCapture(!v)) // revert on failure
      .finally(() => setSaving(false));
  };

  const toggleInjectPlanner = (v: boolean) => {
    setInjectPlanner(v); // optimistic
    api
      .setSettings({ constraints_inject_planner: v })
      .then(apply)
      .catch(() => setInjectPlanner(!v)); // revert on failure
  };

  const toggleAgentTrafficBinding = (v: boolean) => {
    setAgentTrafficBinding(v);
    setSaving(true);
    api
      .setSettings({ agent_traffic_binding: v })
      .then((s) => {
        apply(s);
        toast.success(v ? "已开启 Agent 自动绑定流量" : "已关闭 Agent 自动绑定流量");
      })
      .catch((e) => {
        setAgentTrafficBinding(!v);
        toast.error(`保存失败：${(e as Error).message}`);
      })
      .finally(() => setSaving(false));
  };

  const toggleInjectWorker = (v: boolean) => {
    setInjectWorker(v); // optimistic
    api
      .setSettings({ constraints_inject_worker: v })
      .then(apply)
      .catch(() => setInjectWorker(!v)); // revert on failure
  };

  const toggleNoaCompaction = (v: boolean) => {
    setNoaCompaction(v); // optimistic
    api
      .setSettings({ noa_compaction: v })
      .then((s) => {
        apply(s);
        toast.success(v ? "已开启 noa 上下文压缩（对之后启动的运行生效）" : "已关闭 noa 上下文压缩（恢复内置压缩）");
      })
      .catch((e) => {
        setNoaCompaction(v ? false : true); // revert on failure
        toast.error(`保存失败：${(e as Error).message}`);
      });
  };

  // Persist a web-search patch (enable and/or backend). Optimistic with refetch.
  const saveWebSearch = (patch: Partial<Settings>) => {
    setSaving(true);
    api
      .setSettings(patch)
      .then((s) => {
        apply(s);
        toast.success("已保存网络搜索配置");
      })
      .catch((e) => {
        toast.error("保存失败：" + (e as Error).message);
        api
          .settings()
          .then(apply)
          .catch(() => undefined);
      })
      .finally(() => setSaving(false));
  };

  const saveBraveKey = () => {
    setSavingKey(true);
    api
      .setSettings({ brave_search_api_key: braveKeyInput })
      .then((s) => {
        apply(s);
        setBraveKeyInput("");
        toast.success("已保存 Brave API Key");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
      .finally(() => setSavingKey(false));
  };

  const saveTavilyKey = () => {
    setSavingTavilyKey(true);
    api
      .setSettings({ tavily_search_api_key: tavilyKeyInput })
      .then((s) => {
        apply(s);
        setTavilyKeyInput("");
        toast.success("已保存 Tavily API Key");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
      .finally(() => setSavingTavilyKey(false));
  };

  const saveProxy = () => {
    setSavingProxy(true);
    api
      .setSettings({ web_search_proxy: proxyInput.trim() })
      .then((s) => {
        apply(s);
        toast.success(proxyInput.trim() ? "已保存出口代理" : "已清除出口代理（改为直连）");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
      .finally(() => setSavingProxy(false));
  };

  const saveGlobalProxy = () => {
    setSavingGlobalProxy(true);
    api
      .setSettings({ global_proxy: globalProxyInput.trim() })
      .then((s) => {
        apply(s);
        toast.success(globalProxyInput.trim() ? "已保存全局代理" : "已清除全局代理（改为直连）");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
      .finally(() => setSavingGlobalProxy(false));
  };

  // Run a real "test" search ("test") against the CURRENT form values (backend +
  // proxy + entered key), falling back to saved values server-side. Toasts result.
  const runTest = () => {
    setTesting(true);
    api
      .testWebSearch({
        web_search_backend: backend,
        web_search_proxy: proxyInput.trim(),
        brave_search_api_key: braveKeyInput,
        tavily_search_api_key: tavilyKeyInput,
      })
      .then((r) => {
        if (r.ok) toast.success(`搜索测试成功 · ${r.backend} 返回 ${r.count} 条结果`);
        else toast.error("搜索测试失败：" + (r.error || "未知错误"));
      })
      .catch((e) => toast.error("搜索测试失败：" + (e as Error).message))
      .finally(() => setTesting(false));
  };

  // brave-free selected but no key stored and none being entered → tool stays off.
  const braveNeedsKey = webSearch && backend === "brave-free" && !braveKeySet;

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">系统功能</h1>
        <p className="text-muted-foreground text-sm">全局运行时开关与功能配置 · 标注处即时生效，其余对下一轮运行生效</p>
      </div>

      <Card className="divide-y px-5 py-1">
        <FunctionRow
          icon={<RadioTowerIcon className="size-4" />}
          title="流量捕获"
          desc="所有 Agent 的 HTTP 流量经记录代理全量落库，并注入 traffic_search / traffic_get 工具与代理配置。关闭（默认）不记录、不注入。切换后即时重建 Agent。"
          control={
            <Switch id="traffic-capture" checked={trafficCapture} disabled={!loaded || saving} onCheckedChange={toggleTraffic} />
          }
          state={trafficCapture ? "已开启 · 正在记录流量" : "已关闭"}
        />

        <FunctionRow
          icon={<RadioTowerIcon className="size-4" />}
          title="Agent 自动绑定流量"
          desc="漏洞入库时，报告 Agent 自动核对已有 HTTP 请求/响应并关联流量后再编写报告。会增加 Token 消耗；对下一轮 Agent 生效。"
          control={
            <Switch
              id="agent-traffic-binding"
              checked={agentTrafficBinding}
              disabled={!loaded || saving}
              onCheckedChange={toggleAgentTrafficBinding}
            />
          }
          state={agentTrafficBinding ? "已开启 · 增加 Token 消耗" : "已关闭"}
        />

        <FunctionRow
          icon={<RadioTowerIcon className="size-4" />}
          title="全局代理"
          desc="所有 Agent 的目标流量经此出网（http / https / socks5，可带 user:pass 认证）。开启流量捕获时作为记录代理的上游；留空直连。与搜索代理、LLM 代理相互独立。"
          control={
            <div className="flex w-full items-center gap-2 md:w-auto">
              <Input
                id="global-proxy"
                className="h-8 md:w-72"
                autoComplete="off"
                placeholder="http(s):// 或 socks5://（留空=直连）"
                value={globalProxyInput}
                disabled={!loaded || savingGlobalProxy}
                onChange={(e) => setGlobalProxyInput(e.target.value)}
              />
              <Button type="button" size="sm" onClick={saveGlobalProxy} disabled={!loaded || savingGlobalProxy}>
                保存
              </Button>
            </div>
          }
          state={globalProxyInput.trim() ? "已配置" : "未配置 · 直连"}
        />

        <FunctionRow
          icon={<ShieldAlertIcon className="size-4" />}
          title="操作约束 · 注入规划者（planner）"
          desc="把任务的「操作约束」（allow/deny 条目）拼进规划者的系统提示，框定探索边界。即时生效。"
          control={<Switch id="inject-planner" checked={injectPlanner} disabled={!loaded} onCheckedChange={toggleInjectPlanner} />}
          state={injectPlanner ? "已开启" : "已关闭"}
        />

        <FunctionRow
          icon={<ShieldAlertIcon className="size-4" />}
          title="操作约束 · 注入执行者（worker）"
          desc="同上，注入到执行者的系统提示。即时生效，无需重建 Agent。"
          control={<Switch id="inject-worker" checked={injectWorker} disabled={!loaded} onCheckedChange={toggleInjectWorker} />}
          state={injectWorker ? "已开启" : "已关闭"}
        />

        <FunctionRow
          icon={<FlaskConicalIcon className="size-4" />}
          title="noa 上下文压缩（实验）"
          desc="实验功能：由模型主动压缩长对话历史，四类 Agent 改用 noa 接管上下文，取代内置压缩，压缩原文归档到任务目录。对之后启动的运行生效。"
          control={<Switch id="noa-compaction" checked={noaCompaction} disabled={!loaded} onCheckedChange={toggleNoaCompaction} />}
          state={noaCompaction ? "已开启" : "已关闭"}
        />

        {/* 网络搜索：总开关一行；展开的子配置以缩进块渲染在行下方。 */}
        <div className="py-4">
          <FunctionRow
            icon={<SearchIcon className="size-4" />}
            title="网络搜索"
            desc="总开关：开启后才能在每个 Agent 的配置里单独启用 web_search（仅返回标题/链接/摘要，抓取由 WebFetch 负责）。不走记录代理，独立于流量捕获。"
            control={
              <Switch
                id="web-search"
                checked={webSearch}
                disabled={!loaded || saving}
                onCheckedChange={(v) => {
                  setWebSearch(v); // optimistic
                  saveWebSearch({ web_search_enabled: v });
                }}
              />
            }
            state={webSearch ? "已开启 · 可在各 Agent 启用" : "已关闭"}
          />
          {webSearch && (
            <div className="border-border/60 bg-muted/20 mt-3 flex flex-col divide-y rounded-md border md:ml-6">
              <div className="flex flex-col gap-2 px-4 py-3 md:flex-row md:items-center md:justify-between">
                <div>
                  <p className="text-sm">搜索来源</p>
                  <p className="text-muted-foreground text-xs">
                    DeepSeek 来源复用当前 LLM 配置（仅官方模型 + anthropic 协议），由服务端执行、单独计费。
                  </p>
                </div>
                <Select
                  value={backend}
                  disabled={!loaded || saving}
                  onValueChange={(v) => {
                    setBackend(v); // optimistic
                    saveWebSearch({ web_search_backend: v });
                  }}
                >
                  <SelectTrigger className="h-8 w-56 shrink-0 md:w-64">
                    <SelectValue placeholder="选择来源" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="ddgs">DuckDuckGo（ddgs · 免费无 Key）</SelectItem>
                    <SelectItem value="brave-free">Brave（免费版 · 需 Key）</SelectItem>
                    <SelectItem value="tavily">Tavily（需 Key）</SelectItem>
                    <SelectItem value="deepseek">DeepSeek（官方）</SelectItem>
                  </SelectContent>
                </Select>
              </div>

              {backend === "brave-free" && (
                <div className="flex flex-col gap-2 px-4 py-3 md:flex-row md:items-center md:justify-between">
                  <div>
                    <p className="text-sm">
                      Brave API Key
                      {braveKeySet && <span className="ml-2 text-xs text-emerald-500">已配置</span>}
                    </p>
                    <p className="text-muted-foreground text-xs">
                      免费版约 2,000 次/月 · brave.com/search/api
                      {braveNeedsKey && <span className="ml-1 text-amber-500">· 未配置 Key 前工具不会启用</span>}
                    </p>
                  </div>
                  <div className="flex items-center gap-2">
                    <Input
                      id="brave-key"
                      type="password"
                      className="h-8 md:w-64"
                      autoComplete="off"
                      placeholder={braveKeySet ? "已配置（留空则不变）" : "输入 Brave API Key"}
                      value={braveKeyInput}
                      disabled={!loaded || savingKey}
                      onChange={(e) => setBraveKeyInput(e.target.value)}
                    />
                    <Button type="button" size="sm" onClick={saveBraveKey} disabled={!loaded || savingKey || braveKeyInput.trim() === ""}>
                      保存
                    </Button>
                  </div>
                </div>
              )}

              {backend === "tavily" && (
                <div className="flex flex-col gap-2 px-4 py-3 md:flex-row md:items-center md:justify-between">
                  <div>
                    <p className="text-sm">
                      Tavily API Key
                      {tavilyKeySet && <span className="ml-2 text-xs text-emerald-500">已配置</span>}
                    </p>
                    <p className="text-muted-foreground text-xs">
                      tavily.com 注册获取
                      {webSearch && backend === "tavily" && !tavilyKeySet && (
                        <span className="ml-1 text-amber-500">· 未配置 Key 前工具不会启用</span>
                      )}
                    </p>
                  </div>
                  <div className="flex items-center gap-2">
                    <Input
                      id="tavily-key"
                      type="password"
                      className="h-8 md:w-64"
                      autoComplete="off"
                      placeholder={tavilyKeySet ? "已配置（留空则不变）" : "输入 Tavily API Key（tvly-…）"}
                      value={tavilyKeyInput}
                      disabled={!loaded || savingTavilyKey}
                      onChange={(e) => setTavilyKeyInput(e.target.value)}
                    />
                    <Button
                      type="button"
                      size="sm"
                      onClick={saveTavilyKey}
                      disabled={!loaded || savingTavilyKey || tavilyKeyInput.trim() === ""}
                    >
                      保存
                    </Button>
                  </div>
                </div>
              )}

              <div className="flex flex-col gap-2 px-4 py-3 md:flex-row md:items-center md:justify-between">
                <div>
                  <p className="text-sm">出口代理（可选）</p>
                  <p className="text-muted-foreground text-xs">仅用于访问搜索端点（VPN/SOCKS 等），与记录流量的 MITM 代理无关。</p>
                </div>
                <div className="flex items-center gap-2">
                  <Input
                    id="ws-proxy"
                    className="h-8 md:w-64"
                    autoComplete="off"
                    placeholder="http://host:port 或 socks5://（留空=直连）"
                    value={proxyInput}
                    disabled={!loaded || savingProxy}
                    onChange={(e) => setProxyInput(e.target.value)}
                  />
                  <Button type="button" size="sm" onClick={saveProxy} disabled={!loaded || savingProxy}>
                    保存
                  </Button>
                </div>
              </div>

              <div className="flex items-center justify-between gap-4 px-4 py-3">
                <p className="text-muted-foreground text-xs">用当前配置（来源 + 代理 + Key）实际搜索一次「test」，验证是否可用。</p>
                <Button type="button" variant="outline" size="sm" onClick={runTest} disabled={!loaded || testing} className="shrink-0">
                  {testing ? "测试中…" : "测试搜索"}
                </Button>
              </div>
            </div>
          )}
        </div>

        <FunctionRow
          icon={<RadioTowerIcon className="size-4" />}
          title="自定义脚本 · Python 解释器"
          desc="自定义 script 类型工具用它跑 Python。留空则运行时自动检测（python3 优先）。"
          control={
            <div className="flex w-full items-center gap-2 md:w-auto">
              <Input
                className="h-8 font-mono text-sm md:w-64"
                placeholder="/usr/bin/python3（留空=自动检测）"
                value={pyInterp}
                disabled={!loaded || saving}
                onChange={(e) => setPyInterp(e.target.value)}
              />
              <Button variant="outline" size="sm" onClick={detectPython} disabled={!loaded || saving}>
                检测
              </Button>
              <Button size="sm" onClick={savePython} disabled={!loaded || saving}>
                保存
              </Button>
            </div>
          }
          state={pyInterp.trim() ? "已自定义" : "自动检测"}
        />

        <FunctionRow
          icon={<FlaskConicalIcon className="size-4" />}
          title="工作并发 · Work Agent 数"
          desc="每个任务并发运行的工作 agent 数量（默认 3）。数值越大并发探测越多、消耗也越高。对之后启动的任务生效。"
          control={
            <div className="flex items-center gap-2">
              <Input
                type="number"
                min={1}
                className="h-8 w-24 font-mono text-sm"
                placeholder="3"
                value={workers}
                disabled={!loaded || savingWorkers}
                onChange={(e) => setWorkers(e.target.value)}
              />
              <Button size="sm" onClick={saveWorkers} disabled={!loaded || savingWorkers}>
                保存
              </Button>
            </div>
          }
          state={`当前 ${workers || "3"}`}
        />
      </Card>
    </div>
  );
}

// FunctionRow 是系统功能页的基础行：左侧标题 + 一行说明与当前状态，右侧控件。
// 控件区在窄屏时独占一行（w-full 的输入框靠它撑开）。
function FunctionRow({
  icon,
  title,
  desc,
  control,
  state,
}: {
  icon?: React.ReactNode;
  title: string;
  desc: string;
  control: React.ReactNode;
  state?: string;
}) {
  return (
    <div className="flex flex-col gap-3 py-4 md:flex-row md:items-center md:justify-between md:gap-6">
      <div className="space-y-0.5 md:max-w-2xl">
        <p className="flex items-center gap-2 text-sm font-medium">
          {icon}
          {title}
        </p>
        <p className="text-muted-foreground text-xs leading-relaxed">{desc}</p>
        {state && <p className="text-muted-foreground/80 text-xs">{state}</p>}
      </div>
      <div className="flex shrink-0 items-center gap-2 md:justify-end">{control}</div>
    </div>
  );
}
