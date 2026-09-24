"use client";

// 信息搜集任务已并入任务中心本体:/function/tasks?stage=recon
// (同款表格 + 阶段锁定,排序/分页/批量选择原生生效)。
// 本页仅做兼容重定向;创建与详情页仍为 recon 专属:
//   /function/recon/create  新建步骤任务
//   /function/recon/detail?id=  链路图/执行轨迹/暂存区

import * as React from "react";

import { useRouter } from "next/navigation";

export default function ReconTasksRedirectPage() {
  const router = useRouter();
  React.useEffect(() => {
    router.replace("/function/tasks?stage=recon");
  }, [router]);
  return null;
}
