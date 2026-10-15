import { CheckCircle2, History, ShieldAlert } from "lucide-react";
import type { OperationReceiptView } from "../api/types";
import { useLocale } from "../lib/locale";

const outcomeLabels: Record<OperationReceiptView["outcome"], [string, string]> = {
  applied: ["修改已应用", "Changes applied"],
  failed: ["操作失败", "Operation failed"],
  completed: ["操作已完成", "Operation completed"],
  installed: ["安装已完成", "Installation completed"],
  removed: ["移除已完成", "Removal completed"],
};

export function OperationReceipt({ receipt }: { receipt: OperationReceiptView }) {
  const { t } = useLocale();
  const pending = receipt.cleanup_state === "pending_review";
  const failed = receipt.outcome === "failed";
  const warning = pending || failed;
  const Icon = warning ? ShieldAlert : receipt.replayed ? History : CheckCircle2;
  return <div className={`operation-receipt ${warning ? "receipt-warning" : ""}`}
    role={failed ? "alert" : "status"}>
    <Icon aria-hidden="true" size={15} />
    <div>
      <strong>{t(...outcomeLabels[receipt.outcome])}</strong>
      <span>{receipt.kind.replaceAll("_", " ")}{receipt.replayed ? t(" / 已确认原操作结果", " / original outcome confirmed") : ""}</span>
      {failed && <small>{t("请核对当前内容和恢复建议，再决定后续操作。", "Inspect current contents and recovery guidance before proceeding.")}</small>}
      {pending && <small>{t("暂存区正在等待清理。宽限期后确认原请求可查看清理结果。", "Staging cleanup is pending. Confirm the original request after the grace period to check cleanup.")}</small>}
      {(receipt.retry_strategy || receipt.recovery_action || failed) && <details>
        <summary>{t("查看恢复与请求详情", "View recovery and request details")}</summary>
        <small>{t("重试", "Retry")}: {receipt.retry_strategy || "-"} · {t("恢复", "Recovery")}: {receipt.recovery_action || "-"}</small>
        {failed && <small>{t("相同操作键会返回这份已保存的失败结果。", "The same operation key returns this saved failed result.")}</small>}
      </details>}
    </div>
  </div>;
}
