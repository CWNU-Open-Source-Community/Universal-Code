import { useLocale, type LocaleText } from "../lib/locale";
import type { APIClient } from "../api/client";
import type { ApprovalQueueItemView } from "../api/types";

export function fileApprovalLabel(tool: string, t: LocaleText) {
  switch (tool) {
    case "create_file": return t("创建文件", "Create file");
    case "replace_file": return t("替换文件内容", "Replace file contents");
    case "move_file": return t("移动文件", "Move file");
    case "delete_file": return t("删除文件", "Delete file");
  }
}

export function approvalToolLabel(tool: string, t: LocaleText) {
  switch (tool) {
    case "shell":
    case "script_process": return t("模拟执行命令", "Simulate command");
    case "command_runtime": return t("运行命令", "Run command");
    case "mcp_tool_call": return t("调用 MCP 工具", "Call MCP tool");
    case "git.advanced": return t("执行 Git 操作", "Run Git operation");
    case "agent_browser_sensitive": return t("浏览器操作", "Browser action");
    case "web_fetch": return t("读取网站", "Read website");
    default: return fileApprovalLabel(tool, t) ?? tool;
  }
}

export async function readApprovalPreview(client: APIClient, item: ApprovalQueueItemView, runID: string, signal: AbortSignal, t: LocaleText) {
  const value = await client.approvalPreview(runID, item.id, signal);
  if (item.run_id !== runID || value.run_id !== runID || value.approval_id !== item.id ||
    value.proposal_id !== item.proposal_id || value.tool_name !== item.tool_name || value.workspace_id !== item.workspace_id) {
    throw new Error(t("审批预览与当前操作不一致，请刷新。", "The approval preview does not match this action. Refresh it."));
  }
  return value;
}

export function ApprovalDenialReason({ label, value, onChange, disabled }: {
  label: string; value: string; onChange: (value: string) => void; disabled: boolean;
}) {
  const { t } = useLocale();
  return <details className="approval-denial-reason">
    <summary>{t("拒绝理由", "Denial reason")}</summary>
    <input aria-label={label} disabled={disabled} maxLength={2048}
      onChange={(event) => onChange(event.target.value)} placeholder={t("可选", "Optional")} value={value} />
  </details>;
}
