import type { Ref } from "react";
import { ListChecks } from "lucide-react";
import { useLocale } from "../../lib/locale";
import "./thread-plan.css";

export type V2WorkPhase = "plan" | "deliver";

export function V2PhasePicker({ phase, onChange, disabled = false, planAvailable = true, buttonRef }: {
  phase: V2WorkPhase;
  onChange: (phase: V2WorkPhase) => void;
  disabled?: boolean;
  planAvailable?: boolean;
  buttonRef?: Ref<HTMLButtonElement>;
}) {
  const { t } = useLocale();
  const planning = phase === "plan";
  return <button type="button" ref={buttonRef} className={`v2-phase-picker v2-phase-toggle${planning ? " is-active" : ""}`}
    aria-pressed={planning} disabled={disabled || !planning && !planAvailable}
    title={planning ? t("关闭计划模式，发送消息后按默认方式处理", "Turn off plan mode and handle the next message normally")
      : !planAvailable ? t("当前连接未启用计划模式", "Plan mode is not enabled for this connection") : t("开启后先分析并形成计划，确认计划后执行", "Analyze and prepare a plan, then execute after confirmation")}
    onClick={() => onChange(planning ? "deliver" : "plan")}>
    <ListChecks aria-hidden="true" size={14} /><span className="v2-composer-optional-label">{t("计划模式", "Plan mode")}</span>
  </button>;
}
