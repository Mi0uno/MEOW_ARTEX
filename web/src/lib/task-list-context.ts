// 任务列表(资产主体视图)的上下文参数在页面间透传的约定:company / src / stage。
//
// 任务列表页 → 任务详情 → 漏洞详情的往返链路都必须保住这三个参数,否则从详情页
// 点返回会大退到「选择资产主体」页,丢失上一次的浏览位置。没有上下文参数的详情
// 入口(仪表盘、报告中心等)返回时落到选择页,是设计内的兜底行为。
const TASK_LIST_CONTEXT_KEYS = ["company", "src", "stage"] as const;

// tasksListContextQuery 从当前 URL 参数里挑出任务列表上下文,生成透传 query 串。
// 无上下文时返回空串;调用方拼 URL 时按 `${qs ? `?${qs}` : ""}` 处理。
// 入参用结构化最小类型,同时接受 useSearchParams() 的 ReadonlyURLSearchParams。
export function tasksListContextQuery(params: Pick<URLSearchParams, "get">): string {
  const out = new URLSearchParams();
  for (const key of TASK_LIST_CONTEXT_KEYS) {
    const value = params.get(key);
    if (value != null && value !== "") out.set(key, value);
  }
  return out.toString();
}
