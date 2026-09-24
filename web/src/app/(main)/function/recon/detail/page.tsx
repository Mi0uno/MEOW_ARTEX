"use client";

import * as React from "react";

import { TaskDetailView } from "@/components/task-detail-view";

// 信息搜集任务详情 = 任务详情本体视图(完全复用,recon 任务自动呈现
// 链路图/执行轨迹/暂存区页签,头部操作为 刷新/重跑/删除)。
export default function ReconTaskDetailPage() {
  return (
    <React.Suspense fallback={null}>
      <TaskDetailView />
    </React.Suspense>
  );
}
