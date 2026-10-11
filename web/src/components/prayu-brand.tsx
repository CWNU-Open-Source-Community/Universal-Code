import { useLocale } from "../lib/locale";
import brandMark from "../assets/traverse-board-mark.png";

type PrayuBrandVariant = "compact" | "hero" | "icon";

export function PrayuBrand({ className = "", variant = "compact" }: {
  className?: string;
  variant?: PrayuBrandVariant;
}) {
  const { locale } = useLocale();
  const iconOnly = variant === "icon";
  const displayName = "Universal-Code";
  return (
    <span aria-label={displayName} className={`prayu-brand prayu-brand-${variant} ${className}`.trim()}
      role="img">
      <img alt="" aria-hidden="true" className="prayu-brand-icon" src={brandMark} />
      {!iconOnly && <span aria-hidden="true" className="prayu-brand-copy">
        <strong><span className="prayu-brand-name prayu-brand-name-latin" lang="en">{displayName}</span></strong>
        <small>{locale === "zh-CN" ? <>
          <span lang="en">Agent</span><span lang="zh-CN">工作台</span>
        </> : <span lang="en">Agent Workbench</span>}</small>
      </span>}
    </span>
  );
}
