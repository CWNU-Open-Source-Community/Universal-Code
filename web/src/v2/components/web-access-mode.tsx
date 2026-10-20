import type { WebAccessMode } from "../../api/types";
import { useLocale, type LocaleText } from "../../lib/locale";
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

export function webAccessModeLabel(mode: WebAccessMode, t: LocaleText): string {
  return t(webAccessModeLabels[mode], mode === "disabled" ? "Search disabled"
    : mode === "rules" ? "Rule-based search" : "Full access");
}

export function webAccessModeDetail(mode: WebAccessMode, t: LocaleText): string {
  return t(webAccessModeDetails[mode], mode === "disabled"
    ? "Disable new web searches and page reads. Saved pages remain readable."
    : mode === "rules" ? "Use the configured search service. Read pre-approved domains directly; other pages require approval."
      : "Use the configured search service and read public HTTPS pages without per-site approval.");
}

export function V2WebAccessModeSelect({ mode, onChange, disabled = false,
  label, describedBy }: {
  mode: WebAccessMode;
  onChange: (mode: WebAccessMode) => void;
  disabled?: boolean;
  label?: string;
  describedBy?: string;
}) {
  const { t } = useLocale();
  return <select aria-label={label ?? t("网页搜索模式", "Web search mode")} aria-describedby={describedBy}
    className="v2-web-access-select" disabled={disabled} value={mode}
    onChange={(event) => {
      const next = event.target.value;
      if (next === "disabled" || next === "rules" || next === "full") onChange(next);
    }}>
    <option value="disabled">{t("禁止搜索", "Search disabled")}</option>
    <option value="rules">{t("规则搜索", "Rule-based search")}</option>
    <option disabled value="cached">{t("缓存搜索（待支持）", "Cached search (coming soon)")}</option>
    <option disabled value="indexed">{t("索引搜索（待支持）", "Indexed search (coming soon)")}</option>
    <option value="full">{t("完全访问", "Full access")}</option>
  </select>;
}
