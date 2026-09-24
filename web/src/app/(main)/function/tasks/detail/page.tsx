"use client";

import * as React from "react";

import { TaskDetailView } from "@/components/task-detail-view";

// 任务详情(本体视图)。recon 任务会自动呈现 recon 专属页签(链路图/执行轨迹/暂存区)。
export default function TaskDetailPage() {
  return (
    <React.Suspense fallback={null}>
      <TaskDetailView />
    </React.Suspense>
  );
}
