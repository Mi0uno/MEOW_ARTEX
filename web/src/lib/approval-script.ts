// 审批台脚本提取 helper（契约 APPROVAL_CONSOLE_CONTRACTS §5）。
// tool_input 是任意 JSON：只认对象形态，逐键匹配候选键；值为非空 string 才收录。

export interface ApprovalScriptSection {
  label: string;
  text: string;
}

// 候选键按契约顺序：命令/脚本/代码类字段优先，通用键靠后。
const SCRIPT_KEYS = [
  "command",
  "script",
  "code",
  "content",
  "file_text",
  "new_string",
  "payload",
  "body",
  "template",
  "query",
  "input",
] as const;

// URL 类字段：intentSummary 按 k=v 拼接展示。
const SUMMARY_KV_KEYS = ["url", "path", "host", "target"] as const;

function asRecord(value: unknown): Record<string, unknown> | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  return value as Record<string, unknown>;
}

function truncate(text: string, max: number): string {
  const trimmed = text.trim();
  return trimmed.length > max ? `${trimmed.slice(0, max)}…` : trimmed;
}

// 从 tool_input 中按候选键顺序提取可读的脚本/命令段落。
// label 以工具名为语境前缀（如 "Bash · command"），便于在查看器中区分来源。
export function extractScriptSections(toolName: string, toolInput: unknown): ApprovalScriptSection[] {
  const input = asRecord(toolInput);
  if (!input) return [];
  const sections: ApprovalScriptSection[] = [];
  for (const key of SCRIPT_KEYS) {
    const value = input[key];
    if (typeof value === "string" && value.trim()) {
      sections.push({ label: toolName ? `${toolName} · ${key}` : key, text: value });
    }
  }
  return sections;
}

// 一行摘要：command → 首行（截 120）；url/path/host/target → k=v 空格拼接（截 120）；
// 否则第一个字符串值截 80；再退 JSON.stringify 截 80；空对象返回 ""。
export function intentSummary(toolName: string, toolInput: unknown): string {
  // toolName 仅为签名对齐契约保留，摘要规则本身不使用它。
  void toolName;
  const input = asRecord(toolInput);
  if (!input) return "";

  const command = input.command;
  if (typeof command === "string" && command.trim()) {
    const firstLine = command
      .split("\n")
      .map((line) => line.trim())
      .find(Boolean);
    return truncate(firstLine ?? "", 120);
  }

  const kv: string[] = [];
  for (const key of SUMMARY_KV_KEYS) {
    const value = input[key];
    if (typeof value === "string" && value.trim()) kv.push(`${key}=${value.trim()}`);
  }
  if (kv.length) return truncate(kv.join(" "), 120);

  for (const value of Object.values(input)) {
    if (typeof value === "string" && value.trim()) return truncate(value, 80);
  }
  if (Object.keys(input).length) return truncate(JSON.stringify(input), 80);
  return "";
}
