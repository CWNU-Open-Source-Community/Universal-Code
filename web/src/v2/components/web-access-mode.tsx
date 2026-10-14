import type { WebAccessMode } from "../../api/types";
import "./web-access.css";

export const webAccessModeLabels: Record<WebAccessMode, string> = {
  disabled: "禁止搜索",
  rules: "规则搜索",
  full: "完全访问",
};

export const webAccessModeDetails: Record<WebAccessMode, string> = {
  disabled: "关闭新的网页搜索与抓取，已保存的网页仍可阅读。",
  rules: "使用已配置的搜索服务。预先允许的域名可直接读取，其他网页请求批准。",
  full: "使用已配置的搜索服务，直接读取公网 HTTPS 网页，无需逐站批准。",
};

export function V2WebAccessModeSelect({ mode, onChange, disabled = false,
  label = "网页搜索模式", describedBy }: {
  mode: WebAccessMode;
  onChange: (mode: WebAccessMode) => void;
  disabled?: boolean;
  label?: string;
  describedBy?: string;
}) {
  return <select aria-label={label} aria-describedby={describedBy}
    className="v2-web-access-select" disabled={disabled} value={mode}
    onChange={(event) => {
      const next = event.target.value;
      if (next === "disabled" || next === "rules" || next === "full") onChange(next);
    }}>
    <option value="disabled">禁止搜索</option>
    <option value="rules">规则搜索</option>
    <option disabled value="cached">缓存搜索（待支持）</option>
    <option disabled value="indexed">索引搜索（待支持）</option>
    <option value="full">完全访问</option>
  </select>;
}
