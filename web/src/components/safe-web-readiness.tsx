import { useQuery } from "@tanstack/react-query";
import type { APIClient } from "../api/client";
import { useLocale } from "../lib/locale";
import { ErrorState, LoadingState, StatusBadge } from "./common";

const SafeWebReadinessProduct = "chrome";

export function SafeWebReadinessPanel({ client }: { client: APIClient }) {
  const { t } = useLocale();
  const query = useQuery({
    queryKey: ["safe-web-readiness", SafeWebReadinessProduct],
    queryFn: ({ signal }) => client.safeWebReadiness(SafeWebReadinessProduct, signal),
    retry: false,
  });
  if (query.isLoading) {
    return <LoadingState label={t("正在检查 Safe Web readiness", "Checking Safe Web readiness")} />;
  }
  if (query.isError || !query.data) {
    return <div><ErrorState error={query.error} />
      <button className="settings-action" disabled={query.isFetching} onClick={() => void query.refetch()} type="button">
        {t("重新检查网页环境", "Check web environment again")}</button></div>;
  }
  const readiness = query.data;
  return (
    <div className="safe-web-readiness" aria-label={t("Safe Web readiness", "Safe Web readiness")}>
      <StatusBadge
        status={readiness.ready ? "ready" : (readiness.blocking_reason ?? "blocked")}
      />
      <p>{readiness.ready ? t("网页隔离检查已通过。", "Web isolation checks passed.")
        : readiness.blocking_reason === "review_missing" || readiness.blocking_reason === "review_not_accepted"
          ? t("请在服务端审查浏览器隔离证据。", "Browser isolation evidence needs review on the service.")
          : readiness.blocking_reason === "evidence_not_passed"
            ? t("请修复服务诊断中的失败项，重新采集并审查隔离证据。", "Resolve the failed service checks, then collect and review new isolation evidence.")
            : t("请为当前浏览器和运行环境重新采集并审查隔离证据。", "Collect and review new isolation evidence for this browser and environment.")}</p>
      {!readiness.ready && (
        <p className="safe-web-readiness-blocking-reason">
          {t("阻塞原因", "Blocking reason")}: {readiness.blocking_reason}
        </p>
      )}
      <button className="settings-action" disabled={query.isFetching} onClick={() => void query.refetch()} type="button">
        {query.isFetching ? t("正在检查…", "Checking…") : t("重新检查网页环境", "Check web environment again")}</button>
    </div>
  );
}
