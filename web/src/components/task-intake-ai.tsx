"use client";

// 建任务表单的「AI 识别拆分」区块:把一大段描述拆成 目标 / 操作约束 / 精简描述
// (SRC 模式额外识别奖励标准)。识别结果只经 onApply 预填表单,人工检查后随创建请求
// 才落库——组件本身不持有任何表单状态,所以同一个区块可以同时服务普通任务抽屉与
// SRC 对话框(后者要传 srcMode=true + 当前平台/目标 id,让服务端注入对应规范做识别依据)。
//
// 触发规则(契约 §2):
//   - 手动按钮任何时候可点(识别中除外:同一时间只允许一个在途请求,直接忽略新触发);
//   - 自动触发:描述输入框失焦时,内容 trim ≥60 字、目标还空着、且这段内容没识别过
//     (按内容判重)→ 自动识别一次。识别失败的内容也算「识别过」,避免每次失焦都弹错误。
//
// 为什么挂在 document 的 focusout 上:这个区块不拥有描述输入框,父组件也就不必为它
// 改 onChange/onBlur 链路。失焦回调里校验「失去焦点的元素是值与 description 一致的
// 输入框/文本域」,把范围收紧到真正的描述输入框,其他字段的失焦不会误触发。
//
// 识别中不阻塞编辑:父组件的输入框不由本组件 disable,按钮自己转 spinner 即可。
// 不给 api.parseTaskInput 传 signal:保留它内置的 320s 兜底超时(LLM 拆长文实测 1-2 分钟);
// 组件卸载后用 aliveRef 丢弃迟到结果,而不是中断请求(中断没有任何收益)。

import * as React from "react";

import { Loader2Icon, SparklesIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import type { TaskInputParse } from "@/lib/types";

/** 自动触发的描述最小长度(trim 后),与契约一致。 */
const AUTO_MIN_CHARS = 60;

export function TaskIntakeAI({
  description,
  goal,
  srcMode = false,
  srcPlatformId = 0,
  srcTargetId = 0,
  onApply,
  disabled,
}: {
  description: string;
  goal: string;
  /** SRC 模式:服务端额外识别奖励标准,并按平台/目标规范做识别依据。 */
  srcMode?: boolean;
  srcPlatformId?: number;
  srcTargetId?: number;
  /** 识别成功后的落表单动作由父组件实现(目标空才直填/约束替换等,契约五条规则)。 */
  onApply: (result: TaskInputParse) => void;
  disabled?: boolean;
}) {
  const [busy, setBusy] = React.useState(false);
  const [note, setNote] = React.useState("");
  const busyRef = React.useRef(false);
  // 已触发过识别的描述内容(trim 后):按内容判重,防止同一段文字反复打 LLM。
  const parsedKeyRef = React.useRef<string | null>(null);
  const aliveRef = React.useRef(true);
  React.useEffect(() => {
    aliveRef.current = true;
    return () => {
      aliveRef.current = false;
    };
  }, []);

  // document 级监听只挂一次,回调里必须拿最新 props:用 ref 镜像,避免闭包过期值。
  const latestRef = React.useRef({ description, goal, srcMode, srcPlatformId, srcTargetId, disabled, onApply });
  latestRef.current = { description, goal, srcMode, srcPlatformId, srcTargetId, disabled, onApply };

  const run = React.useCallback(async (trigger: "auto" | "manual") => {
    const cur = latestRef.current;
    // 在途请求直接忽略:与其丢弃旧结果再让用户等两轮,不如让按钮转圈表明正在识别。
    if (busyRef.current || cur.disabled) return;
    const key = cur.description.trim();
    if (!key) {
      // 自动触发没有内容就不吭声;手动点了空描述要给个说法。
      if (trigger === "manual") toast.error("请先填写描述再识别");
      return;
    }
    if (trigger === "auto") {
      if (key.length < AUTO_MIN_CHARS) return;
      if (cur.goal.trim()) return;
      if (parsedKeyRef.current === key) return;
    }
    busyRef.current = true;
    setBusy(true);
    parsedKeyRef.current = key;
    try {
      const r = await api.parseTaskInput({
        description: cur.description,
        goal: cur.goal,
        srcMode: cur.srcMode,
        srcPlatformId: cur.srcPlatformId,
        srcTargetId: cur.srcTargetId,
      });
      if (!aliveRef.current) return;
      setNote(r.note ?? "");
      cur.onApply(r);
      // 成功后把判重键挪到「整理后的描述」:父组件会用它替换描述框,
      // 用户不改动时再次失焦不会对着精简稿再打一轮 LLM。
      parsedKeyRef.current = r.description.trim() || key;
      const parts: string[] = [];
      if (r.goal.trim()) parts.push("目标");
      if (r.constraints.length > 0) parts.push(`${r.constraints.length} 条约束`);
      if (r.rewards.trim()) parts.push("奖励标准");
      if (r.description.trim()) parts.push("描述已精简");
      if (parts.length > 0) toast.success(`已拆分：${parts.join("、")}`);
      else toast.message("识别完成", { description: "原文中未拆出目标/约束/奖励" });
    } catch (e) {
      if (aliveRef.current) toast.error("AI 识别失败：" + (e as Error).message);
    } finally {
      busyRef.current = false;
      if (aliveRef.current) setBusy(false);
    }
  }, []);

  React.useEffect(() => {
    const handleFocusOut = (e: FocusEvent) => {
      const t = e.target as HTMLElement | null;
      if (!t) return;
      const tag = t.tagName;
      if (tag !== "TEXTAREA" && tag !== "INPUT") return;
      // 只认「值与当前 description 一致」的输入框失焦 = 描述输入框失焦。
      if ((t as HTMLInputElement).value !== latestRef.current.description) return;
      void run("auto");
    };
    document.addEventListener("focusout", handleFocusOut);
    return () => document.removeEventListener("focusout", handleFocusOut);
  }, [run]);

  return (
    <div className="flex flex-col items-end gap-1">
      <Button type="button" variant="outline" size="sm" onClick={() => void run("manual")} disabled={disabled || busy}>
        {busy ? <Loader2Icon className="animate-spin" /> : <SparklesIcon />}
        {busy ? "识别中…" : "AI 识别拆分"}
      </Button>
      {note ? (
        <p className="max-w-72 text-right text-muted-foreground text-[11px] leading-snug">{note}</p>
      ) : null}
    </div>
  );
}
