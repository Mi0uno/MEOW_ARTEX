"use client";

// 信息搜集的工具管理已并入统一管理页本体:/system/tools?stage=recon
// (同款页面 + 阶段锁定,启停/绑定/描述编辑/自定义工具原生生效)。
// 本页仅做兼容重定向。

import * as React from "react";

import { useRouter } from "next/navigation";

export default function ReconToolsRedirectPage() {
  const router = useRouter();
  React.useEffect(() => {
    router.replace("/system/tools?stage=recon");
  }, [router]);
  return null;
}
