import type { ApprovalPreviewView } from "../api/types";
import { useLocale } from "../lib/locale";
import "./computer-use-preview.css";

export function ComputerUsePreview({ preview }: { preview: ApprovalPreviewView }) {
  const { t } = useLocale();
  const fields = Object.fromEntries(preview.fields.map((field) => [field.name, field.value]));
  const names: Record<string, string> = {
    computer_list_windows: t("列出窗口", "List windows"), computer_observe: t("查看窗口截图", "Observe window"),
    computer_activate: t("切换到目标窗口", "Activate window"), computer_click: t("点击", "Click"),
    computer_type: t("输入文字", "Type text"), computer_key: t("按键", "Press keys"), computer_scroll: t("滚动", "Scroll"),
  };
  let parameters = fields.arguments ?? "";
  try {
    const value: unknown = JSON.parse(parameters);
    if (value && typeof value === "object" && !Array.isArray(value)) {
      const visible = Object.fromEntries(Object.entries(value).filter(([key]) =>
        !["app_id", "window_id", "observation_id"].includes(key)));
      parameters = Object.keys(visible).length ? JSON.stringify(visible, null, 2) : "";
    }
  } catch { /* Keep the exact server preview if redaction made it non-JSON. */ }
  return <div className="computer-use-preview">
    <dl>
      <div><dt>{t("目标应用", "Application")}</dt><dd><code>{fields.app_path}</code></dd></div>
      <div><dt>{t("本次操作", "Operation")}</dt><dd>{names[fields.tool_name] ?? fields.tool_name}</dd></div>
      {parameters && <div><dt>{t("操作内容", "Details")}</dt><dd><pre>{parameters}</pre></dd></div>}
      {fields.authorization_scope && <div><dt>{t("授权范围", "Scope")}</dt><dd>{fields.authorization_scope}</dd></div>}
    </dl>
    <details><summary>{t("查看应用身份与完整参数", "Show app identity and exact arguments")}</summary>
      <dl>{preview.fields.filter((field) => !["app_path", "authorization_scope"].includes(field.name)).map((field, index) => <div key={`${field.name}:${index}`}>
        <dt>{field.name}</dt><dd><pre>{field.value}</pre></dd>
      </div>)}</dl>
    </details>
  </div>;
}
