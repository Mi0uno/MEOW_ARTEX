"use client";

import * as React from "react";

import { KeyRoundIcon, KeyboardIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { APP_CONFIG } from "@/config/app-config";
import { CHAT_SEND_MODE_OPTIONS, type ChatSendMode, setChatSendMode, useChatSendMode } from "@/lib/chat-send-mode";

import { UpdateCard } from "./_components/update-card";
import { ChangePasswordDialog } from "../../_components/sidebar/change-password-dialog";

// 系统设置：账户安全、版本更新、界面偏好与关于。
// 运行时开关与功能配置已拆到「系统功能」页（/system/functions）。

export default function SystemSettingsPage() {
  const sendMode = useChatSendMode();
  const [pwdOpen, setPwdOpen] = React.useState(false);

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">系统设置</h1>
        <p className="text-muted-foreground text-sm">账户安全 · 版本更新 · 界面偏好</p>
      </div>

      {/* 账户与安全 */}
      <section className="space-y-2">
        <h2 className="text-muted-foreground text-sm font-medium">账户与安全</h2>
        <Card className="divide-y px-5 py-1">
          <SettingsRow
            icon={<KeyRoundIcon className="size-4" />}
            title="修改密码"
            desc="用户名固定为 ARTEX。修改后所有已签发登录 token 立即失效，需要重新登录。"
            control={
              <Button variant="outline" size="sm" onClick={() => setPwdOpen(true)}>
                修改
              </Button>
            }
          />
          <SettingsRow
            icon={<KeyRoundIcon className="size-4" />}
            title="登录保护"
            desc="同一来源 IP 连续 10 次登录失败将锁定 5 分钟；密码初始化采用原子占位，只有第一个请求生效。"
            control={<Badge variant="secondary">已启用</Badge>}
          />
        </Card>
      </section>

      {/* 版本与更新 */}
      <section className="space-y-2">
        <h2 className="text-muted-foreground text-sm font-medium">版本与更新</h2>
        <UpdateCard />
      </section>

      {/* 界面偏好 */}
      <section className="space-y-2">
        <h2 className="text-muted-foreground text-sm font-medium">界面偏好</h2>
        <Card className="divide-y px-5 py-1">
          <SettingsRow
            icon={<KeyboardIcon className="size-4" />}
            title="会话输入框发送键位"
            desc="对话页与任务详情的主 Agent 会话输入框共用此设置，选择后立即生效。该偏好只存在本浏览器，不随账号同步。"
            control={
              <Select value={sendMode} onValueChange={(v) => setChatSendMode(v as ChatSendMode)}>
                <SelectTrigger className="h-8 w-64">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {CHAT_SEND_MODE_OPTIONS.map((option) => (
                    <SelectItem key={option.value} value={option.value}>
                      {option.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            }
          />
        </Card>
      </section>

      {/* 关于 */}
      <section className="space-y-2">
        <h2 className="text-muted-foreground text-sm font-medium">关于</h2>
        <Card className="px-5 py-4">
          <div className="flex items-center gap-3">
            {/* eslint-disable-next-line @next/next/no-img-element */}
            <img src="/logo.png" alt={APP_CONFIG.name} width={40} height={40} />
            <div className="space-y-0.5">
              <p className="text-sm font-medium">
                {APP_CONFIG.name} <span className="text-muted-foreground font-normal">· AI 自主渗透测试系统</span>
              </p>
              <p className="text-muted-foreground text-xs">
                AGPL-3.0 开源协议 · 使用本软件即表示已阅读并同意《使用须知与免责声明》
              </p>
            </div>
          </div>
        </Card>
      </section>

      <ChangePasswordDialog open={pwdOpen} onOpenChange={setPwdOpen} />
    </div>
  );
}

// SettingsRow 与系统功能页的 FunctionRow 同构：标题 + 一行说明，右侧控件。
function SettingsRow({
  icon,
  title,
  desc,
  control,
}: {
  icon?: React.ReactNode;
  title: string;
  desc: string;
  control: React.ReactNode;
}) {
  return (
    <div className="flex flex-col gap-3 py-4 md:flex-row md:items-center md:justify-between md:gap-6">
      <div className="space-y-0.5 md:max-w-2xl">
        <p className="flex items-center gap-2 text-sm font-medium">
          {icon}
          {title}
        </p>
        <p className="text-muted-foreground text-xs leading-relaxed">{desc}</p>
      </div>
      <div className="flex shrink-0 items-center gap-2 md:justify-end">{control}</div>
    </div>
  );
}
