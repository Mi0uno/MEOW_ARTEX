"use client";

// VPS Web 终端:xterm.js over WebSocket。
//
// 协议(契约 §5,api.vpsTerminalUrl + wsUrl 已给地址与 token):
//   C→S JSON:{"type":"stdin","data":"..."} / {"type":"resize","cols":n,"rows":n} / {"type":"ping"}
//   S→C JSON:{"type":"stdout","data":"..."} / {"type":"exit","code":n} / {"type":"error","message":"..."}
//
// 部署约束:本组件必须经 next/dynamic({ssr:false}) 挂载(xterm 操作 DOM),
// 且 WebSocket 走 wsUrl 直连 Go 后端(Next 代理不支持 WS 升级)。
//
// 生命周期:一个连接对应一次 effect(mount 或「重连」时 connectSeq+1);
// cleanup 里关 WS/断开 observer/释放终端,React 严格模式的双挂载也安全。

import * as React from "react";

import { RotateCcwIcon, SquareTerminalIcon } from "lucide-react";

import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { api, wsUrl } from "@/lib/api";
import { cn } from "@/lib/utils";

type TerminalStatus = "connecting" | "connected" | "closed" | "exited";

// 服务端下行消息;stdout 之外的字段都可能缺,按 type 分发。
type ServerMessage = { type?: string; data?: string; code?: number; message?: string };

const STATUS_META: Record<TerminalStatus, { label: string; badge: string }> = {
  connecting: { label: "连接中…", badge: "bg-amber-500/15 text-amber-600 border-amber-500/30" },
  connected: { label: "已连接", badge: "bg-emerald-500/15 text-emerald-600 border-emerald-500/30" },
  closed: { label: "已断开", badge: "bg-red-500/15 text-red-600 border-red-500/30" },
  exited: { label: "会话结束", badge: "bg-muted text-muted-foreground" },
};

export function VpsTerminal({ machineId, machineName }: { machineId: number; machineName?: string }) {
  const holderRef = React.useRef<HTMLDivElement | null>(null);
  // 重连计数:0 = 首次;点「重连」+1 触发 effect 重建整条链路。
  const [connectSeq, setConnectSeq] = React.useState(0);
  const [status, setStatus] = React.useState<TerminalStatus>("connecting");
  const [exitCode, setExitCode] = React.useState<number | null>(null);

  React.useEffect(() => {
    const holder = holderRef.current;
    if (!holder) return;

    setStatus("connecting");
    setExitCode(null);

    const term = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      fontFamily: '"Geist Mono", ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
      scrollback: 5000,
      theme: { background: "#0d0d12" },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(holder);
    // 容器尺寸为 0(如折叠态)时 fit 会抛错,吞掉即可,下轮 resize observer 会再试。
    try {
      fit.fit();
    } catch {
      /* ignore */
    }

    let ws: WebSocket;
    try {
      ws = new WebSocket(wsUrl(api.vpsTerminalUrl(machineId)));
    } catch (e) {
      setStatus("closed");
      term.write(`\x1b[31m无法创建 WebSocket:${(e as Error).message}\x1b[0m\r\n`);
      return;
    }

    // 服务端已透出结束语(exit / error 消息)后,连接关闭不再追加通用报错;
    // cleanup(卸载/重连)主动关连接时也不写终端(终端随即释放)。
    let finished = false;
    let disposed = false;

    const sendResize = () => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
      }
    };

    ws.onopen = () => {
      setStatus("connected");
      // 服务端按首次上报的尺寸开 PTY,连上先同步一次本地尺寸。
      sendResize();
    };
    ws.onmessage = (ev: MessageEvent) => {
      let msg: ServerMessage;
      try {
        msg = JSON.parse(String(ev.data)) as ServerMessage;
      } catch {
        return;
      }
      if (msg.type === "stdout") {
        term.write(msg.data ?? "");
      } else if (msg.type === "exit") {
        const code = typeof msg.code === "number" ? msg.code : 0;
        finished = true;
        setExitCode(code);
        setStatus("exited");
        term.write(`\r\n\x1b[33m[会话已结束,退出码 ${code}]\x1b[0m\r\n`);
        try {
          ws.close();
        } catch {
          /* ignore */
        }
      } else if (msg.type === "error") {
        // 服务端错误(机器离线、终端服务不可用等):原样透出 message,替代固定文案。
        finished = true;
        setStatus("closed");
        term.write(`\r\n\x1b[31m[错误] ${msg.message || "未知错误"}\x1b[0m\r\n`);
      }
    };
    // onerror 之后必然触发 onclose:异常断开的提示统一放在 onclose 输出,避免重复写两遍。
    ws.onclose = () => {
      // exit 主动关的连接已在 onmessage 里置为 exited;服务端 error 也已透出原样消息,
      // 这两种情况不再追加;其余异常断开给出区分后的文案(重连按钮随 closed 状态保留)。
      setStatus((cur) => (cur === "exited" ? cur : "closed"));
      if (!finished && !disposed) {
        term.write(`\r\n\x1b[31m[连接出错] 后端不可达或终端会话被关闭\x1b[0m\r\n`);
      }
    };

    // 键盘输入上行;连接未就绪/已结束时不发(丢按键比报错更不可取,直接忽略)。
    const dataDisposable = term.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "stdin", data }));
      }
    });

    // 容器随窗口/侧边栏伸缩:先 fit 再把新尺寸上报服务端。
    const observer = new ResizeObserver(() => {
      try {
        fit.fit();
      } catch {
        return;
      }
      sendResize();
    });
    observer.observe(holder);

    // 低频心跳:穿过 NAT 保活,也给服务端的空闲判定以外的探活留个抓手。
    const ping = window.setInterval(() => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "ping" }));
      }
    }, 60_000);

    return () => {
      disposed = true;
      window.clearInterval(ping);
      observer.disconnect();
      dataDisposable.dispose();
      try {
        ws.close();
      } catch {
        /* ignore */
      }
      term.dispose();
    };
  }, [machineId, connectSeq]);

  const meta = STATUS_META[status];
  const canReconnect = status === "closed" || status === "exited";

  return (
    <div className="flex min-h-0 flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <span className="flex items-center gap-1.5 text-sm font-medium">
          <SquareTerminalIcon className="text-muted-foreground size-4" />
          Web 终端{machineName ? ` · ${machineName}` : ""}
        </span>
        <Badge variant="outline" className={cn(meta.badge)}>
          <span
            className={cn(
              "size-1.5 rounded-full",
              status === "connected" && "animate-pulse bg-emerald-500",
              status === "connecting" && "animate-pulse bg-amber-500",
              status === "closed" && "bg-red-500",
              status === "exited" && "bg-muted-foreground",
            )}
          />
          {meta.label}
        </Badge>
        {status === "exited" && exitCode != null ? (
          <span className="text-muted-foreground text-xs">退出码 {exitCode}</span>
        ) : null}
        {canReconnect ? (
          <Button size="sm" variant="outline" onClick={() => setConnectSeq((s) => s + 1)}>
            <RotateCcwIcon className="size-3.5" />
            重新连接
          </Button>
        ) : null}
        <span className="text-muted-foreground ml-auto text-xs">
          stdin/resize 走 WebSocket 直连后端;空闲 30 分钟服务端会主动断开
        </span>
      </div>

      {/* 终端本体:xterm 需要确定高度,固定可视行数,宽度交给容器(内嵌 fit)。 */}
      <div
        ref={holderRef}
        className="min-h-[420px] flex-1 overflow-hidden rounded-lg border p-1"
        style={{ background: "#0d0d12" }}
      />
    </div>
  );
}
