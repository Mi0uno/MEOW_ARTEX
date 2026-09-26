"use client";

// C2 插件参数动态表单(契约 §21):按 params_schema 的参数声明(string/bool/enum
// 三类)渲染控件,取值表由受控组件方式上抛。声明解析与默认值归一口径与后端
// server/c2_plugin.go 的渲染器一致(见 c2-shared.tsx 的 c2PluginParamDefs):
//   - string → 单行输入;bool → 开关(归一 "1"/"0");enum → 下拉(声明选项)。
// 会话详情「插件」页签用它收集参数;后端还会再做白名单校验与注入消毒。

import * as React from "react";

import { Label } from "@/components/ui/label";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import type { C2PluginParamDef } from "@/components/c2-shared";

export function C2PluginParamForm({
  defs,
  values,
  onChange,
  disabled,
}: {
  defs: C2PluginParamDef[];
  values: Record<string, string>;
  onChange: (next: Record<string, string>) => void;
  disabled?: boolean;
}) {
  if (defs.length === 0) {
    return (
      <p className="text-muted-foreground text-xs">该插件没有可配置参数,直接运行即可。</p>
    );
  }
  const set = (name: string, v: string) => onChange({ ...values, [name]: v });
  return (
    <div className="grid gap-3">
      {defs.map((d) => (
        <div key={d.name} className="grid gap-1.5">
          {d.type === "bool" ? (
            <div className="flex items-center justify-between gap-3">
              <Label htmlFor={`c2-plugin-param-${d.name}`} className="text-xs">
                {d.name}
                {d.desc ? <span className="text-muted-foreground ml-1.5 font-normal">({d.desc})</span> : null}
              </Label>
              <Switch
                id={`c2-plugin-param-${d.name}`}
                checked={values[d.name] === "1"}
                onCheckedChange={(v) => set(d.name, v ? "1" : "0")}
                disabled={disabled}
              />
            </div>
          ) : d.type === "enum" && d.options.length > 0 ? (
            <>
              <Label htmlFor={`c2-plugin-param-${d.name}`} className="text-xs">
                {d.name}
                {d.desc ? <span className="text-muted-foreground ml-1.5 font-normal">({d.desc})</span> : null}
              </Label>
              <Select value={values[d.name] ?? d.options[0]} onValueChange={(v) => set(d.name, v)} disabled={disabled}>
                <SelectTrigger id={`c2-plugin-param-${d.name}`} className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {d.options.map((o) => (
                      <SelectItem key={o} value={o}>
                        {o}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </>
          ) : (
            <>
              <Label htmlFor={`c2-plugin-param-${d.name}`} className="text-xs">
                {d.name}
                {d.desc ? <span className="text-muted-foreground ml-1.5 font-normal">({d.desc})</span> : null}
              </Label>
              <Input
                id={`c2-plugin-param-${d.name}`}
                value={values[d.name] ?? ""}
                onChange={(e) => set(d.name, e.target.value)}
                disabled={disabled}
                className="font-mono text-xs"
                placeholder="参数值(后端会做注入消毒)"
              />
            </>
          )}
        </div>
      ))}
    </div>
  );
}
