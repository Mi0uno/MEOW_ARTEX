"use client";

// 建任务表单的「操作约束」行编辑器(allow=允许 / deny=禁止)。
//
// 约束的落点:创建时随 POST /api/tasks 的 constraints 提交,后端在首轮 Planner 之前写入
// task_constraints(origin=user),任务详情既有的「操作约束」面板直接可见/可改——
// 所以这里只负责把行编辑好,不做预览,也不做行内的 kind 文案解释(与详情面板同一套语义)。
//
// 行上限 40 与后端校验一致:parse-input 的识别结果截断到 40 条;这里是用户显式提交,
// 超限后端直接 400,所以 UI 在 40 行就封口。kind Select 的 value 非空("allow"/"deny"),
// 不涉及 Radix Select 的 value="" 保留值问题。

import { PlusIcon, XIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import type { TaskConstraintInput } from "@/lib/types";

// 与后端「≤40 条、单条 ≤2000 runes」对齐;UI 只封行数,单条长度交给后端校验兜底。
const MAX_CONSTRAINT_ROWS = 40;

export function TaskConstraintsEditor({
  value,
  onChange,
  disabled,
}: {
  value: TaskConstraintInput[];
  onChange: (v: TaskConstraintInput[]) => void;
  disabled?: boolean;
}) {
  const update = (index: number, patch: Partial<TaskConstraintInput>) =>
    onChange(value.map((row, i) => (i === index ? { ...row, ...patch } : row)));
  const remove = (index: number) => onChange(value.filter((_, i) => i !== index));
  const add = () => onChange([...value, { kind: "deny", text: "" }]);
  const full = value.length >= MAX_CONSTRAINT_ROWS;

  return (
    <div className="grid gap-2">
      <Label>操作约束（可选）</Label>
      {value.length === 0 ? null : (
        <div className="grid gap-2">
          {value.map((row, index) => (
            <div key={index} className="flex items-center gap-2">
              <Select
                value={row.kind === "allow" ? "allow" : "deny"}
                onValueChange={(v) => update(index, { kind: v === "allow" ? "allow" : "deny" })}
                disabled={disabled}
              >
                <SelectTrigger className="w-24 shrink-0">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    <SelectItem value="allow">允许</SelectItem>
                    <SelectItem value="deny">禁止</SelectItem>
                  </SelectGroup>
                </SelectContent>
              </Select>
              <Input
                value={row.text}
                onChange={(e) => update(index, { text: e.target.value })}
                placeholder="如：禁止对登录口做爆破；仅测试 *.example.com"
                disabled={disabled}
              />
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                className="shrink-0"
                aria-label="删除该条约束"
                disabled={disabled}
                onClick={() => remove(index)}
              >
                <XIcon />
              </Button>
            </div>
          ))}
        </div>
      )}
      <div>
        <Button type="button" variant="outline" size="sm" onClick={add} disabled={disabled || full}>
          <PlusIcon data-icon="inline-start" />
          {full ? "已达 40 条上限" : "添加一条"}
        </Button>
      </div>
      <p className="text-muted-foreground text-xs">创建后写入操作约束，任务总览可查看/修改。</p>
    </div>
  );
}
