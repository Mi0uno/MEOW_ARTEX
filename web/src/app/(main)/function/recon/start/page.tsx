"use client";

// 信息搜集:选企业 + 一键开始,系统自动跑全部可用数据源(被动+主动+测绘)。
// 不选收集器,不挑步骤——这是平台的事,不是用户的事。

import * as React from "react";

import Link from "next/link";
import { PlayIcon, RefreshCwIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { Company } from "@/lib/types";

const STATUS_LABEL: Record<string, string> = {
  created: "待运行", running: "运行中", done: "已完成",
  failed: "失败", paused: "已暂停", queued: "排队中", timeout: "超时",
};

function statusBadgeClass(s: string): string {
  switch (s) {
    case "running": return "bg-blue-500/15 text-blue-600 border-blue-500/30";
    case "done": return "bg-emerald-500/15 text-emerald-600 border-emerald-500/30";
    case "failed": case "timeout": return "bg-red-500/15 text-red-600 border-red-500/30";
    default: return "bg-muted text-muted-foreground";
  }
}

export default function ReconStartPage() {
  const [companies, setCompanies] = React.useState<Company[]>([]);
  const [selectedCompany, setSelectedCompany] = React.useState<Company | null>(null);
  const [description, setDescription] = React.useState("");
  const [manualText, setManualText] = React.useState("");
  const [starting, setStarting] = React.useState(false);
  const [lastResult, setLastResult] = React.useState<{ status: string; assets: { type: string; value: string }[]; stats: Record<string, unknown> } | null>(null);

  React.useEffect(() => {
    api.companies().then(setCompanies).catch((e) => toast.error(`读取企业失败：${e.message}`));
  }, []);

  const start = async () => {
    if (!selectedCompany) { toast.error("请选择目标企业"); return; }
    setStarting(true);
    try {
      const t = await api.createTask({
        description: description.trim() || `信息搜集:${selectedCompany.name}`,
        goal: "收集企业攻击面资产(域名/子域名/IP/服务)",
        companyIds: [Number(selectedCompany.id)],
        stage: "recon",
      });
      toast.success(`信息搜集已启动(#${t.id}),自动使用全部可用数据源`);
      setLastResult(null);
      // 轮询等完成
      const check = setInterval(async () => {
        const tasks = await api.tasks();
        const mine = tasks.tasks.find((x) => x.id === t.id);
        if (mine && (mine.status === "done" || mine.status === "failed")) {
          clearInterval(check);
          setStarting(false);
        }
      }, 3000);
    } catch (e) {
      toast.error(`启动失败：${(e as Error).message}`);
      setStarting(false);
    }
  };

  return (
    <div className="mx-auto max-w-xl space-y-4 p-4 md:p-6">
      <div>
        <h1 className="font-semibold text-xl">开始信息搜集</h1>
        <p className="text-muted-foreground text-sm">选企业 → 开始,系统自动跑全部可用数据源</p>
      </div>
      <Card>
        <CardContent className="space-y-4 pt-4">
          <div className="space-y-1.5">
            <Label>目标企业</Label>
            {companies.length === 0 ? (
              <p className="text-muted-foreground text-sm">
                还没有企业,请先在「资产」页新增并录入根域名范围
              </p>
            ) : (
              <div className="flex flex-wrap gap-2">
                {companies.map((c) => (
                  <button
                    key={c.id}
                    type="button"
                    onClick={() => setSelectedCompany(c)}
                    className={`rounded-full border px-3 py-1 text-xs transition-colors ${
                      selectedCompany?.id === c.id
                        ? "border-primary bg-primary text-primary-foreground"
                        : "hover:bg-muted"
                    }`}
                  >
                    {c.name}
                  </button>
                ))}
              </div>
            )}
          </div>
          <Button className="w-full" size="lg" onClick={() => void start()} disabled={starting || !selectedCompany}>
            <PlayIcon className="size-4" />
            {starting ? "信息搜集运行中…" : "开始信息搜集"}
          </Button>
        </CardContent>
      </Card>
    </div>
  );
}
