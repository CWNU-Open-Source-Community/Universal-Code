import { useLocale } from "../../lib/locale";
import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowRight, FolderOpen, GitBranch, GitPullRequest, RefreshCw, ShieldCheck } from "lucide-react";
import type { APIClient } from "../../api/client";
import { readThreadReview, type ThreadReview, type ThreadReviewChange } from "../../api/task-delivery";
import type { RunDetailView } from "../../api/types";
import { ErrorState, LoadingState, StatusBadge } from "../../components/common";
import type { FileEditReviewTarget } from "../../components/file-edit-panel";
import { PlanDeliveryPanel } from "../../components/run-workspace";
import { DiffWrapButton, parseReviewDiff, ReviewDiff } from "./review-diff";
import "./task-delivery.css";



export const taskReviewKey = (threadID: string) => ["thread", threadID, "task-review"] as const;

export function isCheckPassed(outcome: string): boolean {
  const normalized = outcome.toLowerCase().trim();
  return normalized === "pass" || normalized === "passed" || normalized === "success";
}

export function isCheckFailed(outcome: string): boolean {
  const normalized = outcome.toLowerCase().trim();
  return normalized === "fail" || normalized === "failed" || normalized === "failure" || normalized === "error";
}

export function TaskOverview({ client, threadID, onFeedback, onGit, onPullRequest, onChecks, onReviewFile, currentRunDetail }: {
  client: APIClient; threadID: string; onFeedback: (context: string) => void; onGit?: () => void;
  onPullRequest?: () => void; onChecks?: () => void;
  onReviewFile?: (target: FileEditReviewTarget) => void;
  currentRunDetail?: RunDetailView;
}) {
  const { t } = useLocale();
  const query = useQuery({ queryKey: taskReviewKey(threadID), queryFn: ({ signal }) => readThreadReview(client, threadID, signal), refetchOnMount: "always" });
  const review = query.data;
  const feedback = (source: string) => onFeedback(t(`请根据以下任务审阅来源继续修改：\n${source}\n任务：${threadID}\n请核对当前文件，保留无关修改。\n具体要求：`, `Continue editing based on this task review source:\n${source}\nTask: ${threadID}\nCheck current files and preserve unrelated changes.\nRequirements:`));

  const currentRunChecks = review?.checks.filter((c) => c.run_id === review.current_run_id) ?? [];
  const historicalChecks = review?.checks.filter((c) => c.run_id !== review.current_run_id) ?? [];
  const totalChecks = review?.checks.length ?? 0;
  const passedChecksCount = review?.checks.filter((c) => isCheckPassed(c.outcome)).length ?? 0;
  const failedChecksCount = review?.checks.filter((c) => isCheckFailed(c.outcome)).length ?? 0;
  const allPassed = totalChecks > 0 && passedChecksCount === totalChecks;

  return <section className="v2-task-delivery" aria-label={t("整个任务的审阅", "Whole-task review")}>
    <div className="v2-delivery-heading"><div><h2>{t("任务改动", "Task changes")}</h2><p className="v2-delivery-muted">{t("先核对改动，再选择要提交的文件与交付流程。", "Review changes, then choose files to commit and a delivery workflow.")}</p></div><button disabled={query.isFetching} onClick={() => void query.refetch()} type="button">
      <RefreshCw size={15} aria-hidden="true" />{query.isFetching ? t("正在核对…", "Checking…") : t("刷新当前状态", "Refresh current status")}</button></div>
    {query.isLoading && <LoadingState label={t("正在汇总任务改动并核对当前目录…", "Collecting task changes and checking the current directory…")} />}
    {query.isError && <ErrorState error={query.error} />}
    {review && <>
      {query.isError && <p role="alert">{t("以下为上次读取的结果，当前状态尚未确认。", "The results below are from the previous read. Current status is unconfirmed.")}</p>}
      <div className="v2-delivery-summary">
        <div className="v2-overview-location"><FolderOpen size={18} aria-hidden="true" />
          <strong>{review.target.root_path?.replace(/[\\/]+$/, "").split(/[\\/]/).pop() || t("目录待确认", "Directory unconfirmed")}</strong>
          <span>{review.target.kind === "drydock" ? t("隔离工作目录", "Isolated working directory") : review.target.kind === "source" ? t("项目来源目录", "Source project directory") : t("目录类型待确认", "Directory type unconfirmed")}</span>
          {review.revision.repository_kind === "none" ? <span>{t("尚未使用 Git", "Not using Git")}</span> : <span><GitBranch size={14} aria-hidden="true" />{review.revision.branch || t("未确认分支", "Branch unconfirmed")}</span>}
        </div>
        <div className="v2-overview-counts" aria-label={t("已记录的任务范围", "Recorded task scope")}>
          <span><strong>{review.applied_changes.length}</strong> {t(" 条已应用编辑", " applied edits")}</span>
          <span><strong>{review.unapplied_changes.length}</strong> {t(" 条未应用提案", " unapplied proposals")}</span>
          <span><strong>{totalChecks}</strong> {t(" 项检查记录", " check records")}{failedChecksCount > 0 ? t(`（${failedChecksCount} 项未通过）`, ` (${failedChecksCount} failed)`) : allPassed ? t("（全部通过）", " (all passed)") : ""}</span>
        </div>
        <details className="v2-delivery-details"><summary>{t("目录与版本详情", "Directory and revision details")}</summary><dl>
          <div><dt>{t("实际执行目录", "Execution directory")}</dt><dd>{review.target.root_path || t("尚无法确认", "Unconfirmed")}</dd></div>
          <div><dt>{t("当前提交", "Current commit")}</dt><dd><code>{review.revision.head || t("未确认提交", "Commit unconfirmed")}</code></dd></div>
          <div><dt>{t("读取时间", "Read at")}</dt><dd>{new Date(review.observed_at).toLocaleString(t("zh-CN", "en-US"))} {t(" · 覆盖 ", " · Covering ")}{review.runs.length} / {review.total_runs} {t(" 次执行", " runs")}</dd></div></dl>
        </details>
        {review.partial && <p role="status">{t("此审阅覆盖部分记录。请在执行记录和提交页面核对其余改动与检查。", "This review covers only some records. Check execution records and the commit page for remaining changes and checks.")}</p>}
        <p className="v2-delivery-muted">{t("下方汇总已记录的文件编辑。命令、手动编辑等产生的其他变化，请在提交页面核对。", "Recorded file edits are summarized below. Review command-generated, manual and other changes on the commit page.")}</p>
        {(review.reasons.length > 0 || review.revision.reasons.length > 0) && <details><summary>{t("审阅范围与缺失信息", "Review scope and missing information")}</summary>
          <ul>{[...new Set([...review.reasons, ...review.revision.reasons])].map((reason) => <li key={reason}>{reason}</li>)}</ul></details>}
      </div>
      <div className="v2-overview-next">
        <div className="v2-overview-next-label">
          <strong>{t("交付与下一步操作", "Delivery and next actions")}</strong>
          <span>{t("查看当前目录的全部改动，选择本次提交范围、进入 PR 或查看完整检查报告。", "View all changes in the current directory, select a commit scope, open the PR or view full check reports.")}</span>
        </div>
        <div className="v2-overview-next-actions">
          {onGit && <button className="v2-delivery-primary" onClick={onGit} type="button">
            {t("选择文件并提交", "Select files and commit")}<ArrowRight size={15} aria-hidden="true" /></button>}
          {onPullRequest && <button className="v2-delivery-secondary" onClick={onPullRequest} type="button">
            <GitPullRequest size={15} aria-hidden="true" />{t("PR 状态与交付", "PR status and delivery")}</button>}
          {onChecks && <button className="v2-delivery-secondary" onClick={onChecks} type="button">
            <ShieldCheck size={15} aria-hidden="true" />{t("完整检查与环境", "All checks and environment")}</button>}
        </div>
      </div>
      {currentRunDetail?.plan_delivery && <section className="v2-overview-plan-delivery" aria-label={t("交付计划", "Delivery plan")}>
        <h3>{t("当前执行的交付计划", "Current run's delivery plan")}</h3>
        <PlanDeliveryPanel client={client} detail={currentRunDetail}
          key={`plan:${review.current_run_id}`} state={currentRunDetail.plan_delivery} threadID={threadID} />
      </section>}
      <div className="v2-overview-review-grid"><section aria-label={t("任务编辑", "Task edits")}>
      <h3>{t("尚未应用的提案 ", "Unapplied proposals")}<span>({review.unapplied_changes.length})</span></h3>
      {!review.unapplied_changes.length && <p>{t("没有已记录的待应用提案。", "No recorded proposals awaiting application.")}</p>}
      {[...review.unapplied_changes].sort((left, right) => Number(right.run_id === review.current_run_id) - Number(left.run_id === review.current_run_id))
        .map((change) => <TaskChange key={`${change.run_id}:${change.edit_id}`} change={change} observedAt={review.observed_at}
          onFeedback={feedback} onReviewFile={onReviewFile} />)}
      <h3>{t("已应用的编辑 ", "Applied edits")}<span>({review.applied_changes.length})</span></h3>
      {!review.applied_changes.length && <p>{t("尚无已记录的应用编辑。工作目录的其他改动可在提交页面核对。", "No recorded applied edits. Check other working directory changes on the commit page.")}</p>}
      {review.applied_changes.map((change) => <TaskChange key={`${change.run_id}:${change.edit_id}`} change={change} observedAt={review.observed_at} onFeedback={feedback} />)}
      </section><section aria-label={t("任务检查", "Task checks")}>
      <h3>{t("检查结果 ", "Check results")}<span>({review.checks.length})</span></h3>
      {!review.checks.length && <p>{t("尚无已记录的检查结果。", "No recorded check results.")}</p>}
      {currentRunChecks.length > 0 && <div className="v2-delivery-checks-group">
        <h4>{t("当前执行 ", "Current run")}<span>({currentRunChecks.length})</span></h4>
        <div className="v2-delivery-checks">{currentRunChecks.map((check) => <CheckCard key={`${check.run_id}:${check.source_kind}:${check.id}`}
          check={check} observedAt={review.observed_at} isFetching={query.isFetching || query.isError} onFeedback={feedback} />)}</div>
      </div>}
      {historicalChecks.length > 0 && <div className="v2-delivery-checks-group">
        <h4>{t("历史执行 ", "Past run")}<span>({historicalChecks.length})</span></h4>
        <div className="v2-delivery-checks">{historicalChecks.map((check) => <CheckCard key={`${check.run_id}:${check.source_kind}:${check.id}`}
          check={check} observedAt={review.observed_at} isFetching={query.isFetching || query.isError} onFeedback={feedback} />)}</div>
      </div>}
      </section></div>
    </>}
  </section>;
}

function CheckCard({ check, observedAt, isFetching, onFeedback }: {
  check: ThreadReview["checks"][number]; observedAt: string; isFetching: boolean; onFeedback: (context: string) => void;
}) {
  const { t } = useLocale();
  const freshness: Record<string, string> = { current: t("适用于当前版本", "Applies to the current revision"), stale: t("代码已变化，需重验", "Code changed; recheck required"), unbound: t("未绑定代码版本", "No code revision recorded"), unavailable: t("无法核对版本", "Could not verify revision") };
  return <article key={`${check.run_id}:${check.source_kind}:${check.id}`}>
    <div><strong>{check.title}</strong><StatusBadge status={check.outcome} />
      <span className={`v2-delivery-freshness ${isFetching ? "unavailable" : check.revision_state}`}>
        {isFetching ? t("上次检查记录，当前版本尚未确认", "Previous check; current revision unconfirmed") : freshness[check.revision_state] ?? t("无法核对版本", "Could not verify revision")}</span></div>
    <p>{check.revision_state === "unbound" ? t("检查记录未绑定代码版本。请对当前代码重新运行检查，再确认结果。", "These check records have no associated code revision. Rerun checks on the current code before confirming results.") : check.reason}</p>
    <details><summary>{t("来源与时间", "Source and time")}</summary><p>{check.run_id} · {check.id} · {new Date(check.recorded_at).toLocaleString(t("zh-CN", "en-US"))}</p>
      {check.exit_code !== undefined && <p>{t("实际退出码：", "Actual exit code: ")}{check.exit_code}</p>}</details>
    <button type="button" onClick={() => onFeedback(t(`检查：${check.title}\n结果：${check.outcome}\n版本状态：${check.revision_state}\n来源执行：${check.run_id}\n记录：${check.id}\n绑定版本：${check.recorded_revision_sha256 || t("未保存", "Not saved")}\n读取时间：${observedAt}`, `Check: ${check.title}\nResult: ${check.outcome}\nRevision status: ${check.revision_state}\nSource run: ${check.run_id}\nRecord: ${check.id}\nRecorded revision: ${check.recorded_revision_sha256 || t("未保存", "Not saved")}\nRead at: ${observedAt}`))}>{t("引用检查并继续修复", "Quote check and continue fixing")}</button>
  </article>;
}
function TaskChange({ change, observedAt, onFeedback, onReviewFile }: {
  change: ThreadReviewChange; observedAt: string; onFeedback: (context: string) => void;
  onReviewFile?: (target: FileEditReviewTarget) => void;
}) {
  const { t } = useLocale();
  const [wrapLines, setWrapLines] = useState(false);
  const canWrap = useMemo(() => Boolean(change.diff && parseReviewDiff(change.diff).hunks.length), [change.diff]);
  const source = t(`文件：${change.path}${change.destination_path ? ` → ${change.destination_path}` : ""}\n来源执行：${change.run_id}\n目录：${change.workspace_id}\n编辑记录：${change.edit_id}\n原版本：${change.original_sha256}\n提案版本：${change.proposed_sha256}\n当前观测版本：${change.current_sha256 || t("不可用", "Unavailable")}\n读取时间：${observedAt}`, `File: ${change.path}${change.destination_path ? ` → ${change.destination_path}` : ""}\nSource run: ${change.run_id}\nDirectory: ${change.workspace_id}\nEdit: ${change.edit_id}\nOriginal revision: ${change.original_sha256}\nProposed revision: ${change.proposed_sha256}\nCurrent revision: ${change.current_sha256 || t("不可用", "Unavailable")}\nRead at: ${observedAt}`);
  return <article className="v2-delivery-change"><header><strong>{change.path}{change.destination_path && ` → ${change.destination_path}`}</strong>
    <div className="v2-delivery-change-actions">
      {canWrap && <DiffWrapButton wrapLines={wrapLines} onToggle={() => setWrapLines((value) => !value)} />}
      {onReviewFile && <button className="v2-delivery-primary" type="button" onClick={() => onReviewFile({ runID: change.run_id,
        editID: change.edit_id, workspaceID: change.workspace_id })}>{t("查看并审阅", "View and review")}</button>}
      <button type="button" onClick={() => onFeedback(source)}>{t("引用文件", "Quote file")}</button>
    </div></header>
    <details><summary>{t("查看这次编辑的差异", "View this edit's diff")}</summary>
      {(change.diff_truncated || change.redacted) && <p>{t("此差异", "This diff is ")}{change.diff_truncated ? t("已截断", "Truncated") : ""}{change.redacted ? t("已脱敏", "Redacted") : ""}{t("，只引用可见内容。", "; only visible content is quoted.")}</p>}
      {change.diff ? <ReviewDiff patch={change.diff} source={source} onFeedback={onFeedback} wrapLines={wrapLines} /> : <p>{t("未保存可显示的文本差异。", "No displayable text diff was saved.")}</p>}
      <details><summary>{t("编辑来源", "Edit source")}</summary><pre>{source}</pre></details>
    </details></article>;
}
