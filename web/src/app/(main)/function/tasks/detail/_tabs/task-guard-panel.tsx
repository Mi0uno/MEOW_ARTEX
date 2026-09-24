"use client";

import * as React from "react";

import { ShieldAlertIcon, ShieldCheckIcon, ShieldIcon, ShieldOffIcon } from "lucide-react";
import { toast } from "sonner";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { api } from "@/lib/api";
import type { Task } from "@/lib/types";

// 三档任务级监管门限。值与 db/schema.sql 的 tasks_guard_mode_check 逐字一致:
// ""=跟随全局 / "off"=本任务不跑监管 / "enforce"=强制启用。
const GUARD_MODE_OPTIONS = [
  { value: "inherit", label: "跟随全局（默认）", hint: "与全局监管配置一致" },
  { value: "off", label: "本任务不跑监管", hint: "规则与 AI 判定都不介入，不留审计" },
  { value: "enforce", label: "强制启用监管", hint: "即使全局关掉也照常监管" },
] as const;

function guardModeOf(task: Task): string {
  const mode = task.guard_mode ?? "";
  // 后端把 'inherit' 归一成 '' 存储,这里再兜一层,避免历史数据两种写法都出现。
  return mode === "inherit" ? "" : mode;
}

function modeLabel(mode: string): string {
  if (mode === "off") return "本任务不跑监管";
  if (mode === "enforce") return "强制启用监管";
  return "跟随全局";
}

// TaskGuardPanel 是任务详情里的监管门限查看/编辑入口。
//
// 为什么放在总览页签而不是新开一个页签:门限是一个"看一眼 + 偶尔改一下"的配置项,
// 单独占一个页签会让它被忽略——而它是安全闸门,被忽略就意味着用户不知道自己的任务
// 是不是在无人看守地跑。
export function TaskGuardPanel({ task, onUpdated }: { task: Task; onUpdated: () => void }) {
  const current = guardModeOf(task);
  // 编辑草稿:打开时同步一次,保存失败时保留用户的选择而不是回弹。
  const [draft, setDraft] = React.useState(current);
  const [saving, setSaving] = React.useState(false);

  React.useEffect(() => {
    setDraft(guardModeOf(task));
  }, [task]);

  const dirty = draft !== current;

  const save = async () => {
    setSaving(true);
    try {
      // guard_rule_ids 一律传空数组 = 用全部已启用规则(规则集限定的 UI 本次不做,
      // 但端点已支持;传空而不是省略,是为了让"改门限"这个动作的语义完整)。
      await api.updateTaskGuard(task.id, draft, []);
      toast.success(`监管门限已改为「${modeLabel(draft)}」`);
      onUpdated();
    } catch (error) {
      toast.error("更新监管门限失败：" + (error as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const ruleCount = task.guard_rule_ids?.length ?? 0;

  return (
    <Card className={current === "off" ? "border-destructive/40" : undefined}>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          {current === "off" ? (
            <ShieldOffIcon className="text-destructive size-4" />
          ) : current === "enforce" ? (
            <ShieldCheckIcon className="text-primary size-4" />
          ) : (
            <ShieldIcon className="text-primary size-4" />
          )}
          监管门限
          <Badge variant={current === "off" ? "destructive" : "outline"} className="text-xs">
            {modeLabel(current)}
          </Badge>
        </CardTitle>
        <CardDescription>
          监管在每次工具调用前裁决本任务是否放行。门限可被任务单独覆盖，改动从下一次工具调用开始生效（无需重启任务）。
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <Select value={draft === "" ? "inherit" : draft} onValueChange={(v) => setDraft(v === "inherit" ? "" : v)}>
            <SelectTrigger className="w-64" aria-label="选择任务监管门限">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup>
                {GUARD_MODE_OPTIONS.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
          <Button size="sm" onClick={save} disabled={saving || !dirty}>
            {saving && <Spinner data-icon="inline-start" />}
            {saving ? "保存中" : "保存门限"}
          </Button>
        </div>

        {draft === "off" && (
          <Alert variant="destructive">
            <ShieldAlertIcon />
            <AlertTitle>关闭后本任务将无人看守</AlertTitle>
            <AlertDescription>
              拦截规则与 AI 判定都不会再介入本任务的工具调用，
              <strong>并且不会留下任何拦截审计记录</strong>
              ，事后无法复盘 agent 执行了什么。确认任务范围已用测试范围/约束另行限定时再保存。
            </AlertDescription>
          </Alert>
        )}

        <dl className="text-muted-foreground grid grid-cols-1 gap-1 text-xs sm:grid-cols-2">
          <div className="flex gap-1">
            <dt>当前生效：</dt>
            <dd className="text-foreground">{modeLabel(current)}</dd>
          </div>
          <div className="flex gap-1">
            <dt>规则集限定：</dt>
            <dd className="text-foreground">
              {ruleCount > 0 ? `仅启用 ${ruleCount} 条规则（#${task.guard_rule_ids?.join("、#")}）` : "全部已启用规则"}
            </dd>
          </div>
        </dl>
      </CardContent>
    </Card>
  );
}
