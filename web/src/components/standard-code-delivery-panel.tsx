import { useQuery } from "@tanstack/react-query";
import { useId, useState } from "react";
import { FileCheck2, RefreshCw, RotateCcw, TestTube2 } from "lucide-react";
import type { APIClient } from "../api/client";
import { APIRequestError } from "../api/client";
import { formatBytes, formatDate, shortID } from "../lib/format";
import { useLocale } from "../lib/locale";
import { EmptyState, ErrorState, KeyValue, LoadingState, StatusBadge } from "./common";
import { ArtifactDetail } from "./artifact-detail";
import { SavedCommandOutput } from "./saved-command-output";

const observationReasons: Record<string, [string, string]> = {
  workspace_modified_after_verification: ["报告记录后项目内容已变化", "Workspace changed after the report was recorded"],
  permission_generation_drift: ["验证时的权限版本已变化", "Permission revision changed after verification"],
  backend_generation_drift: ["验证时的执行环境已变化", "Execution backend changed after verification"],
  drydock_binding_unavailable: ["报告对应的隔离工作区已不可用", "The report's isolated workspace is unavailable"],
  workspace_revision_unavailable: ["目前无法读取项目版本", "The current workspace revision could not be read"],
};

const outputSourceReasons: Record<string, [string, string]> = {
  activity_source_unavailable: ["此历史记录缺少可读取正文的命令活动来源。", "This historical record has no readable command activity source."],
  output_not_public: ["此命令的输出不适合公开展示，仍可核对来源记录。", "This command's output is not eligible for public display; its source record remains available."],
  artifact_binding_mismatch: ["输出记录的来源无法与报告精确对应，正文暂不可读。", "The output source could not be matched exactly to this report; its text is unavailable."],
};

export function StandardCodeDeliveryPanel({ client, runID, onOpenCheckpoints,
  onOpenFile }: {
  client: APIClient;
  runID: string;
  onOpenCheckpoints: () => void;
  onOpenFile: (path: string, workspaceID?: string) => void;
}) {
  const { t, locale } = useLocale();
  const [openOutputKey, setOpenOutputKey] = useState("");
  const outputRegionID = useId();
  const query = useQuery({
    queryKey: ["run", runID, "standard-code-delivery"],
    queryFn: ({ signal }) => client.standardCodeDelivery(runID, signal),
    enabled: Boolean(runID),
  });
  const report = query.data;
  const observed = report?.observation;
  const observationConfirmed = Boolean(report && observed?.observed_at && query.isSuccess && !query.isFetching);
  const status = observationConfirmed ? report!.status : "unknown";
  const reason = observed?.reason_code;
  const outputs = report?.verifications.find((verification) => `${report.id}:${verification.job_id}` === openOutputKey);
  const heading = query.isError ? t("本次核对失败，当前版本未确认", "The latest check failed; the current revision is unconfirmed")
    : query.isFetching ? t("正在核对当前版本…", "Checking the current revision…")
      : !observed?.observed_at ? t("尚未核对当前版本", "The current revision has not been checked")
        : report?.status === "stale" ? t("报告已过期，当前版本未验证", "The report is stale; the current revision is not verified")
          : report?.verified ? t("最近核对的版本已验证", "The last checked revision was verified")
            : t("最近核对的版本未验证", "The last checked revision was not verified");
  return <section aria-label={t("Standard Code 交付", "Standard Code delivery")}
    className="standard-code-delivery-panel">
    <header className="projection-heading">
      <div><FileCheck2 aria-hidden="true" size={17} />
        <h2>{t("交付检查", "Delivery checks")}</h2></div>
      <div>{report && <StatusBadge status={status} />}
        <button aria-label={t("刷新交付报告", "Refresh delivery report")} className="icon-button"
          disabled={query.isFetching} onClick={() => void query.refetch()} type="button">
          <RefreshCw aria-hidden="true" className={query.isFetching ? "spin" : ""} size={15} />
        </button></div>
    </header>
    {query.isLoading && <LoadingState label={t("加载交付报告", "Loading delivery report")} />}
    {query.isError && (!report && query.error instanceof APIRequestError && query.error.code === "NOT_FOUND"
      ? <p role="status">{t("交付报告暂不可用，检查结果待确认。请核对报告是否已生成及服务配置，再刷新；也可在「执行记录」查看已有结果。",
        "The delivery report is unavailable and check results need confirmation. Check report generation and service settings, then refresh. Existing results are available in execution records.")}</p>
      : <ErrorState error={query.error} />)}
    {report && <>
      <section className={`delivery-truth-summary delivery-truth-${status}`}>
        <div><strong>{heading}</strong>
          {reason && <p>{t(...(observationReasons[reason] ?? ["报告需要重新核对", "The report needs to be checked again"]))}</p>}
        </div>
        <StatusBadge status={status} />
      </section>
      <p>{t("检查版本来自隔离工作区。打开文件会显示当前内容。", "Checks cover the isolated workspace revision. Files open at their current contents.")}</p>
      <dl className="handoff-grid delivery-truth-grid">
        <KeyValue label={t("受影响文件", "Affected files")} value={String(report.diff.changed_count)} />
        <KeyValue label={t("已跟踪 / 未跟踪", "Tracked / untracked")}
          value={`${report.diff.tracked_count} / ${report.diff.untracked_count}`} />
        <KeyValue label={t("索引 / 工作树", "Index / worktree")}
          value={`${report.diff.index_count} / ${report.diff.worktree_count}`} />
        <KeyValue label={t("验证命令", "Verification commands")} value={String(report.verifications.length)} />
        <KeyValue label={t("Diff 大小", "Diff size")} value={formatBytes(report.diff.bytes)} />
        <KeyValue label={t("冲突", "Conflicts")} value={String(report.diff.conflict_count)} />
        <KeyValue label={t("恢复级别", "Recovery level")}
          value={<StatusBadge status={report.final_checkpoint.recovery_level} />} />
        <KeyValue label={t("记录时间", "Recorded") } value={formatDate(report.created_at)} />
        <KeyValue label={t("最近版本核对", "Last revision check")} value={observed?.observed_at ? formatDate(observed.observed_at) : t("尚无核对记录", "No check recorded")} />
        <KeyValue label={t("原始报告结论", "Recorded conclusion")} value={<StatusBadge status={report.receipt_status} />} />
      </dl>
      <details><summary>{t("查看版本与来源标识", "View revision and source identifiers")}</summary>
      <div className="delivery-truth-identities">
        <div><span>{t("收据", "Receipt")}</span><code title={report.receipt_sha256}>{report.receipt_sha256}</code></div>
        <div><span>{t("Workspace revision", "Workspace revision")}</span>
          <code title={report.final_checkpoint.revision_sha256}>{report.final_checkpoint.revision_sha256}</code></div>
        <div><span>Diff SHA-256</span><code title={report.diff.sha256}>{report.diff.sha256}</code></div>
        <div><span>Checkpoint</span><code>{report.final_checkpoint.id}</code></div>
      </div></details>
      <div className="delivery-truth-columns">
        <section><h3>{t("受影响文件", "Affected files")}</h3>
          {report.diff.files.length === 0 ? <EmptyState>{t("没有文件变更", "No changed files")}</EmptyState> :
            <div className="delivery-truth-list">{report.diff.files.map((file) =>
              <div key={file.path_sha256}><span>{file.path ? <button className="link-button"
                onClick={() => onOpenFile(file.path!, report.binding.drydock_workspace_id)} type="button">{file.path}</button> :
                <code>{t("路径已脱敏", "redacted path")} · {shortID(file.path_sha256)}</code>}
                <small>{[
                  file.committed && t("已提交", "committed"),
                  file.index_changed && t("索引", "index"),
                  file.worktree_changed && t("工作树", "worktree"),
                  file.untracked && t("未跟踪", "untracked"),
                  file.conflicted && t("冲突", "conflict"),
                ].filter(Boolean).join(" · ")}</small></span>
                <StatusBadge status={file.path_redacted ? "redacted" : "recorded"}
                  label={file.path_redacted ? undefined : t("报告记录", "Reported")} /></div>)}</div>}
        </section>
        <section><h3><TestTube2 aria-hidden="true" size={14} />{t("验证命令", "Verification commands")}</h3>
          {report.verifications.length === 0 ? <EmptyState>{t("本报告未纳入验证命令", "This report contains no verification commands")}</EmptyState> :
            <div className="delivery-truth-list">{report.verifications.map((verification) =>
              <div key={verification.job_id}><span><strong>{shortID(verification.job_id)}</strong>
                <small>{verification.state} · exit {verification.exit_code ?? "—"} · {t("重试", "retries")} {verification.retry_count}</small>
                {verification.executable_identity_kind === "template_path_sha256" &&
                  <small>{t("sbx 固定模板与来宾命令路径绑定摘要", "sbx pinned template and guest command path digest")} · <code>{verification.executable_sha256}</code></small>}
                {verification.output_truncated && <small className="inline-warning">{t("输出已截断", "output truncated")}</small>}
                {verification.artifacts.length > 0 && <button className="link-button"
                  aria-controls={outputRegionID} aria-expanded={openOutputKey === `${report.id}:${verification.job_id}`}
                  onClick={() => setOpenOutputKey((current) => current === `${report.id}:${verification.job_id}` ? "" :
                    `${report.id}:${verification.job_id}`)} type="button">{t(`查看 ${verification.artifacts.length} 条输出记录`,
                    `Inspect ${verification.artifacts.length} output records`)}</button>}</span>
                <StatusBadge status={verification.conclusion} /></div>)}</div>}
        </section>
      </div>
      {outputs && <section id={outputRegionID} aria-label={t("报告对应的输出记录", "Output records referenced by this report")}>
        <h3>{t("输出记录", "Output records")} · {shortID(outputs.job_id)}</h3>
        <p>{t("来源记录保留报告当时的大小和摘要。展示正文会再次脱敏，其大小和摘要可能不同；超出保存上限的内容无法恢复。",
          "Source records retain the size and digest recorded by the report. Displayed text is redacted again and may have a different size and digest; uncaptured output cannot be recovered.")}</p>
        {outputs.artifacts.map((artifact) => {
          const source = report.output_sources?.find((item) => item.job_id === outputs.job_id && item.artifact_id === artifact.id);
          const available = source?.status === "available" && source.thread_id && source.activity_ref;
          const unavailable = source?.reason && outputSourceReasons[source.reason];
          return <ArtifactDetail client={client} id={artifact.id} key={`${report.id}:${outputs.job_id}:${artifact.id}`}
            expected={{ runID: report.binding.run_id, sourceID: outputs.job_id, sha256: artifact.sha256,
              sizeBytes: artifact.size_bytes, stream: artifact.stream }}>
            {available ? <SavedCommandOutput client={client} threadID={source.thread_id!}
              activityRef={source.activity_ref!} artifactRef={artifact.id} locale={locale}
              reference={{ artifact_ref: artifact.id, stream: artifact.stream,
                mime: "text/plain; charset=utf-8", size_bytes: artifact.size_bytes, truncated: outputs.output_truncated }} />
              : <p role="status">{unavailable ? t(...unavailable) : t("当前服务未提供这条输出的正文来源，仍可查看来源记录。",
                "This service did not provide a readable source for this output; its source record remains available.")}</p>}
          </ArtifactDetail>;
        })}
      </section>}
      {(report.reasons.length > 0 || report.uncovered_items.length > 0) &&
        <section className="delivery-truth-reasons"><h3>{t("结论与未覆盖项", "Conclusion and uncovered items")}</h3>
          <div>{report.reasons.map((reason) => <StatusBadge key={reason.provenance_sha256}
            label={reason.code.replaceAll("_", " ")} status={reason.code} />)}</div>
          {report.uncovered_items.map((item) => <p key={item.summary_sha256}>{item.summary}</p>)}
        </section>}
      <section className="delivery-truth-recovery"><div><RotateCcw aria-hidden="true" size={16} />
        <span><strong>{t("恢复入口", "Recovery entry points")}</strong>
          <small>{t("检查点恢复记录的项目内容。项目外的进程、网络或远端变化请单独检查和处理。",
            "Checkpoints restore recorded project content. Inspect and handle processes, network effects, and remote changes outside the project separately.")}</small></span></div>
        <div><button className="compact-command" onClick={onOpenCheckpoints} type="button">
          {t("查看项目恢复选项", "View workspace recovery options")}</button></div>
      </section>
      <details className="delivery-truth-boundary"><summary>{t("报告包含哪些内容", "What the report includes")}</summary>
        <p>{t("报告保存版本、差异摘要、脱敏后的有界命令输出和恢复入口。交付操作请在对应的提交、推送或合并入口完成。",
          "The report saves the revision, diff summary, bounded and redacted command output, and recovery options. Complete delivery through the relevant commit, push, or merge controls.")}</p></details>
    </>}
  </section>;
}
