import { CircleAlert } from "lucide-react";
import type { ThreadRunRecoveryView } from "../../api/types";
import { useLocale } from "../../lib/locale";

export function V2ThreadRunRecovery({ recovery, approvalSaved = false }: {
  recovery: ThreadRunRecoveryView;
  approvalSaved?: boolean;
}) {
  const { t } = useLocale();
  return <section aria-label={t("对话继续提示", "Conversation continuation notice")} aria-live="polite" className="v2-run-recovery">
    <span className="v2-run-recovery-icon"><CircleAlert aria-hidden="true" size={18} /></span>
    <div>
      <strong>{approvalSaved ? t("审批已保存，后续执行已停止", "Approval saved; subsequent execution stopped") : t("本轮执行已停止，对话仍可继续", "This run stopped; the conversation can continue")}</strong>
      <p>{recovery.detail}</p>
      <small>{t("可以直接发送“继续”或补充要求。若上次提交的结果尚未确认，系统会先核对，再发送新要求。", "Send “continue” or add instructions. If the previous submission is unconfirmed, it is checked before new instructions are sent.")}</small>
      {!recovery.quiescent && <small className="v2-run-recovery-waiting">
        {t("上一次执行仍在释放资源；你可以先编辑消息，发送时系统会给出可重试提示。", "The previous run is still releasing resources. You can edit a message now; sending will show a retryable notice.")}</small>}
    </div>
  </section>;
}
