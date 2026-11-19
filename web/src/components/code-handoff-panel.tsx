import { useMutation, useQuery } from "@tanstack/react-query";
import { ArrowRight, BookOpenCheck, Download, RefreshCw } from "lucide-react";
import type { APIClient } from "../api/client";
import { formatBytes, formatDate, shortID } from "../lib/format";
import { downloadTextFile } from "../lib/download";
import { useLocale } from "../lib/locale";
import { EmptyState, ErrorState, KeyValue, LoadingState, StatusBadge, StatusLabel } from "./common";
import type { ReceiptReviewNavigationTarget } from "./receipt-review-navigation";
import { CodeHandoffHostCommands } from "./code-handoff-host-commands";
import { LifecycleStatusBadge } from "./lifecycle-status";

export function CodeHandoffPanel({ client, runID, onOpenDelivery, onOpenReceiptReview }: {
  client: APIClient;
  runID: string;
  onOpenDelivery?: () => void;
  onOpenReceiptReview?: (target: ReceiptReviewNavigationTarget) => void;
}) {
  const { t } = useLocale();
  const query = useQuery({
    queryKey: ["run", runID, "code-handoff"],
    queryFn: ({ signal }) => client.codeHandoff(runID, signal),
    enabled: Boolean(runID),
  });
  const exportHandoff = useMutation({
    mutationFn: (format: "markdown" | "json") => client.codeHandoffExport(runID, format),
    onSuccess: (value) => downloadTextFile(value.filename, value.mime_type, value.content),
  });
  return <section aria-label={t("代码交接", "Code handoff")} className="code-handoff-panel">
    <header className="projection-heading">
      <div><BookOpenCheck aria-hidden="true" size={17} /><h2>{t("代码交接", "Code handoff")}</h2></div>
      <div>{query.data && <span>{t("记录时执行状态：", "Recorded execution state: ")}<LifecycleStatusBadge status={query.data.run_status} /></span>}
        <button aria-label={t("刷新代码交接", "Refresh Code handoff")} className="icon-button"
          disabled={query.isFetching} onClick={() => void query.refetch()}
          title={t("刷新", "Refresh")} type="button"><RefreshCw aria-hidden="true"
            className={query.isFetching ? "spin" : ""} size={15} /></button></div>
    </header>
    {query.isLoading && <LoadingState label={t("正在生成代码交接", "Building Code handoff")} />}
    {query.isError && <ErrorState error={query.error} />}
    {query.data && <>
      <dl className="handoff-grid">
        <KeyValue label={t("阶段", "Phase")} value={<StatusLabel status={query.data.phase} />} />
        <KeyValue label={t("计划", "Plan")} value={<StatusLabel status={query.data.plan.state} />} />
        <KeyValue label={t("模块", "Modules")} value={`${query.data.plan.completed_count} / ${query.data.plan.module_count}`} />
        <KeyValue label={t("队列", "Queue")} value={t(`${query.data.queue.pending} 项待处理`, `${query.data.queue.pending} pending`)} />
        <KeyValue label={t("变更", "Changes")} value={`${query.data.change_set.returned_count} / ${formatBytes(query.data.change_set.total_diff_bytes)}`} />
        <KeyValue label={t("验证", "Verification")} value={t(`${query.data.verification.pass_count} 通过 / ${query.data.verification.fail_count} 失败`, `${query.data.verification.pass_count} pass / ${query.data.verification.fail_count} fail`)} />
        <KeyValue label={t("检查清单", "Checklists")} value={query.data.verification_plans.returned_count} />
        <KeyValue label={t("覆盖率", "Coverage")} value={`${query.data.verification_coverage.observed_plan_item_count} / ${query.data.verification_coverage.plan_item_count}`} />
        <KeyValue label={t("矛盾项", "Contradictions")} value={query.data.verification_coverage.contradictory_item_count} />
        <KeyValue label={t("收据审阅", "Receipt reviews")} value={t(`${query.data.verification_snapshot_receipt_reviews.metadata_confirmed_count} 已确认 / ${query.data.verification_snapshot_receipt_reviews.metadata_disputed_count} 有争议`, `${query.data.verification_snapshot_receipt_reviews.metadata_confirmed_count} confirmed / ${query.data.verification_snapshot_receipt_reviews.metadata_disputed_count} disputed`)} />
        <KeyValue label={t("待办操作", "Actions")} value={query.data.pending_action_count} />
        <KeyValue label={t("事件高水位", "Event high-water")} value={query.data.source_event_sequence} />
        <KeyValue label={t("生成时间", "Generated")} value={formatDate(query.data.generated_at)} />
        {query.data.standard_code_delivery && <>
          <KeyValue label={t("交付检查", "Delivery checks")}
            value={<StatusBadge status={query.data.standard_code_delivery.status} />} />
          <KeyValue label={t("交付收据", "Delivery receipt")}
            value={shortID(query.data.standard_code_delivery.receipt_sha256)} />
          <KeyValue label={t("交付文件", "Delivery files")}
            value={query.data.standard_code_delivery.diff.changed_count} />
          <KeyValue label={t("交付命令", "Delivery commands")}
            value={query.data.standard_code_delivery.verifications.length} />
        </>}
      </dl>
      {query.data.standard_code_delivery && <div className="delivery-handoff-truth">
        <span><strong>{t("对应的交付报告", "Referenced delivery report")}</strong>
          <code>{query.data.standard_code_delivery.receipt_sha256}</code></span>
        {onOpenDelivery && <button className="compact-command" onClick={onOpenDelivery} type="button">
          {t("打开交付页", "Open delivery")}</button>}
      </div>}
      <div className="handoff-export-actions">
        <span>{t("经 SHA-256 校验的下载", "SHA-256 verified download")}</span>
        <button className="compact-command" disabled={exportHandoff.isPending}
          onClick={() => exportHandoff.mutate("markdown")} type="button">
          <Download aria-hidden="true" size={14} />Markdown</button>
        <button className="compact-command" disabled={exportHandoff.isPending}
          onClick={() => exportHandoff.mutate("json")} type="button">
          <Download aria-hidden="true" size={14} />JSON</button>
      </div>
      {exportHandoff.error && <ErrorState error={exportHandoff.error} />}
      {query.data.host_commands && <CodeHandoffHostCommands client={client} commands={query.data.host_commands} />}
      <div className="handoff-reference-columns">
        <section><h3>{t("待办操作", "Pending actions")}</h3>
          {query.data.pending_actions.length === 0 ? <EmptyState>{t("没有待办操作", "No pending actions")}</EmptyState> :
            <div className="handoff-reference-list">{query.data.pending_actions.map((item) =>
              <div key={item.id}><span><strong>{item.kind.replaceAll("_", " ")}</strong>
                <code>{shortID(item.id)}</code></span><StatusBadge status={item.state} /></div>)}</div>}
        </section>
        <section><h3>{t("报告", "Reports")}</h3>
          {query.data.report_references.length === 0 ? <EmptyState>{t("没有报告", "No reports")}</EmptyState> :
            <div className="handoff-reference-list">{query.data.report_references.map((item) =>
              <div key={item.id}><span><strong>{shortID(item.id)}</strong>
                <small>{t(`${item.finding_count} 项发现`, `${item.finding_count} findings`)}</small></span>
                <StatusBadge status={item.status} /></div>)}</div>}
        </section>
        <section className="handoff-coverage"><h3>{t("验证覆盖", "Verification coverage")}</h3>
          {query.data.verification_coverage.items.length === 0 ?
            <EmptyState>{t("暂无检查项。", "No checklist items yet.")}</EmptyState> :
            <div className="handoff-reference-list">{query.data.verification_coverage.items.map((item) => {
              const contradictory = item.pass_count > 0 && item.fail_count > 0;
              return <div key={`${item.plan_id}:${item.ordinal}`}><span>
                <strong>{shortID(item.plan_id)} / {t("检查项", "item")} {item.ordinal}</strong>
                <small>{t(`${item.pass_count} 通过 / ${item.fail_count} 失败 / ${item.unknown_count} 未知`, `${item.pass_count} pass / ${item.fail_count} fail / ${item.unknown_count} unknown`)}</small>
              </span><StatusBadge status={contradictory ? "conflict" :
                item.associated_evidence_count > 0 ? "observed" : "unobserved"} /></div>;
            })}</div>}
        </section>
        <section><h3>{t("收据元数据审阅", "Receipt metadata reviews")}</h3>
          {query.data.verification_snapshot_receipt_reviews.references.length === 0 ?
            <EmptyState>{t("没有收据审阅", "No receipt reviews")}</EmptyState> :
            <div className="handoff-reference-list">
              {query.data.verification_snapshot_receipt_reviews.references.map((item) =>
                <div key={item.id}><span><strong>{shortID(item.receipt_id)}</strong>
                  <small>{t("事件", "event")} {item.review_event_sequence}</small></span>
                  <div className="handoff-reference-actions">
                    <StatusBadge status={item.decision} />
                    {onOpenReceiptReview && <button aria-label={t(`在验证页打开收据审阅 ${item.id}`, `Open receipt review ${item.id} in Verify`)}
                      className="icon-button" onClick={() => onOpenReceiptReview(item)}
                      title={t("在验证页打开这条收据审阅", "Open exact receipt review in Verify")} type="button">
                      <ArrowRight aria-hidden="true" size={14} />
                    </button>}
                  </div></div>)}
            </div>}
        </section>
      </div>
    </>}
  </section>;
}
