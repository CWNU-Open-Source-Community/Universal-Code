import { useLocale } from "../../lib/locale";
import { useEffect, useRef, useState, type RefObject } from "react";
import { createPortal } from "react-dom";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, ChevronDown, FileDiff, GitCommitHorizontal, GitPullRequest, Wrench, X } from "lucide-react";
import type { APIClient } from "../../api/client";
import type { RunDetailView, StandardCodeDeliveryRecordRequestView, StandardCodeDeliveryView, SupervisorToolRoundView, ThreadDetailView } from "../../api/types";
import { FileEditPanel, type FileEditReviewTarget } from "../../components/file-edit-panel";
import { RepositoryDiffPanel } from "../../components/repository-diff-panel";
import { StandardCodeDeliveryPanel } from "../../components/standard-code-delivery-panel";
import { CodeHandoffPanel } from "../../components/code-handoff-panel";
import { StandardCodeReadinessPanel } from "../../components/run-permission-settings";
import { PlanDeliveryPanel } from "../../components/plan-delivery-panel";
import { WorkspaceCheckpointPanel } from "../../components/workspace-checkpoint-panel";
import { WorkspaceExplorer } from "../../components/workspace-explorer";
import { EvidenceInventory } from "../../components/evidence-inventory";
import { useModalFocusTrap } from "../../hooks/use-modal-focus-trap";
import { ThreadActivityToolDetailPanel } from "./activity-detail";
import { v2QueryKeys } from "../query-keys";
import { TaskOverview } from "./task-overview";
import { TaskGit } from "./task-git";
import { TaskPullRequest } from "./task-pull-request";
import { ProjectChecks } from "./project-checks";
import { TaskReviewTools, type TaskReviewToolMemory, type TaskReviewToolReviews } from "./task-review-tools";
import type { WorkspaceView } from "../../api/types";
import type { ReviewCommentResult } from "../review-comment-draft";
import "./task-review.css";

type ReviewTab = "overview" | "git" | "pr" | "tools" | "files" | "checks" | "records" | "restore" | "evidence";




interface ReportAttempt {
  runID: string;
  threadID: string;
  workspaceID: string;
  operationKey: string;
  body: StandardCodeDeliveryRecordRequestView;
  state: "pending" | "unknown";
  error?: string;
}
const reportIntentKey = (runID: string) => ["run", runID, "standard-code-delivery-intent"] as const;

export function V2TaskReview({ client, detail, working, onClose, onRequestChange, onAddComment, returnFocusRef, onOpenWorktree, initialFileTarget,
  initialToolTarget, toolMemoryRef, embedded = false }: {
  client: APIClient; detail: ThreadDetailView; working: boolean;
  onClose: () => void; onRequestChange: (context: string) => void;
  onAddComment?: (context: string) => ReviewCommentResult;
  returnFocusRef: RefObject<HTMLElement | null>;
  onOpenWorktree?: (workspace: WorkspaceView) => void;
  initialFileTarget?: FileEditReviewTarget;
  initialToolTarget?: { runID: string; tool: "github-review" };
  toolMemoryRef?: RefObject<TaskReviewToolMemory>;
  embedded?: boolean;
}) {
  const { t } = useLocale();
  const [commentResult, setCommentResult] = useState<ReviewCommentResult | null>(null);
  const addComment = onAddComment ? (context: string) => {
    const result = onAddComment(t(`任务：${detail.thread.id}\n${context}`, `Task: ${detail.thread.id}\n${context}`));
    setCommentResult(result);
    return result;
  } : undefined;
  const historyTabs: [ReviewTab, string][] = [["files", t("编辑明细", "Edit details")],
  ["records", t("执行记录", "Execution records")], ["evidence", t("参考资料", "References")], ["restore", t("撤销与恢复", "Undo and recovery")]];
  const primaryTabs = [{ value: "overview", label: t("查看改动", "View changes"), icon: FileDiff },
  { value: "git", label: t("提交与推送", "Commit and push"), icon: GitCommitHorizontal },
  { value: "pr", label: t("PR 状态", "PR status"), icon: GitPullRequest },
  { value: "checks", label: t("检查与交付", "Checks and delivery"), icon: FileDiff },
  { value: "tools", label: t("更多交付工具", "More delivery tools"), icon: Wrench }] as const;
  const executionStatusLabels: Record<string, string> = { created: t("尚未开始", "Not started"), preparing: t("准备中", "Preparing"), running: t("未结束", "Not finished"),
  paused: t("已暂停", "Paused"), waiting_approval: t("等待批准", "Awaiting approval"), completed: t("已完成", "Completed"),
  failed: t("执行失败", "Execution failed"), cancelled: t("已停止", "Stopped") };
  const queryClient = useQueryClient();
  const currentRun = detail.active_run ?? detail.last_run;
  const localToolMemory = useRef<TaskReviewToolMemory>({ generation: 0 });
  const toolMemory = toolMemoryRef ?? localToolMemory;
  const retainedRunID = toolMemory.current.client === client && toolMemory.current.threadID === detail.thread.id
    ? toolMemory.current.runID : undefined;
  const [runSelection, setRunSelection] = useState({ threadID: detail.thread.id,
    runID: initialFileTarget?.runID ?? initialToolTarget?.runID ?? retainedRunID ?? currentRun.id });
  const selectedRunID = runSelection.threadID === detail.thread.id
    ? runSelection.runID : initialFileTarget?.runID ?? initialToolTarget?.runID ?? currentRun.id;
  const setSelectedRunID = (runID: string) => {
    if (runID !== selectedRunID || runSelection.threadID !== detail.thread.id)
      toolMemory.current = { generation: toolMemory.current.generation + 1 };
    setRunSelection({ threadID: detail.thread.id, runID });
  };
  const [fileTarget, setFileTarget] = useState<FileEditReviewTarget | undefined>(initialFileTarget);
  const [tab, setTab] = useState<ReviewTab>(initialFileTarget ? "files" : initialToolTarget ? "tools" : "overview");
  const [showHistory, setShowHistory] = useState(Boolean(initialFileTarget));
  useEffect(() => {
    if (!initialFileTarget) return;
    setSelectedRunID(initialFileTarget.runID); setFileTarget(initialFileTarget);
    setTab("files"); setShowHistory(true);
  }, [initialFileTarget?.runID, initialFileTarget?.editID, initialFileTarget?.workspaceID]);
  useEffect(() => {
    if (!initialToolTarget) return;
    setSelectedRunID(initialToolTarget.runID); setFileTarget(undefined);
    setTab("tools"); setShowHistory(false);
  }, [initialToolTarget?.runID, initialToolTarget?.tool]);
  const navigation = useRef<HTMLDivElement>(null);
  const selectTab = (value: ReviewTab, focusNavigation = false) => {
    setTab(value); setFilePath(null);
    setShowHistory((expanded) => value === "checks" ? expanded : !primaryTabs.some((item) => item.value === value));
    if (focusNavigation) requestAnimationFrame(() => navigation.current?.querySelector<HTMLButtonElement>(`[data-review-tab="${value}"]`)?.focus());
  };
  const [filePath, setFilePath] = useState<{ path: string; workspaceID: string } | null>(null);
  const close = useRef<HTMLButtonElement>(null);
  const dialog = useModalFocusTrap<HTMLElement>(!embedded, onClose, false, close,
    { isolateBackground: !embedded, trapFocus: !embedded, returnFocusRef });
  const keys = useRef(new Map<string, string>());
  const selectedRun = detail.runs.find(({ run }) => run.id === selectedRunID)?.run
    ?? (selectedRunID === currentRun.id ? currentRun : undefined);
  const reviewedRunID = selectedRun?.id ?? selectedRunID;
  if (toolMemory.current.client !== client || toolMemory.current.threadID !== detail.thread.id || toolMemory.current.runID !== reviewedRunID)
    toolMemory.current = { generation: toolMemory.current.generation + 1, client, threadID: detail.thread.id, runID: reviewedRunID };
  const toolMemoryGeneration = toolMemory.current.generation;
  const retainToolReview = <T extends keyof TaskReviewToolReviews>(kind: T, value: TaskReviewToolReviews[T]) => {
    if (toolMemory.current.generation !== toolMemoryGeneration) return;
    toolMemory.current = { generation: toolMemoryGeneration, client, threadID: detail.thread.id, runID: reviewedRunID,
      reviews: { git: null, github: null, ...toolMemory.current?.reviews, [kind]: value } };
  };
  const reviewFile = (target: FileEditReviewTarget) => {
    setSelectedRunID(target.runID); setFileTarget(target); setFilePath(null);
    setTab("files"); setShowHistory(true);
  };
  const workspaceID = detail.thread.workspace_id ?? "";
  const runDetail = useQuery({ queryKey: ["run", reviewedRunID],
    queryFn: ({ signal }) => client.get<RunDetailView>(`/runs/${encodeURIComponent(reviewedRunID)}`, {}, signal),
    enabled: tab === "checks" && Boolean(selectedRun) });
  const currentPlanQuery = useQuery({
    queryKey: ["run", currentRun.id, "overview-current-plan"],
    queryFn: async ({ signal }) => {
      const res = await client.get<RunDetailView>(`/runs/${encodeURIComponent(currentRun.id)}`, {}, signal);
      if (res?.run?.id !== currentRun.id) return undefined;
      return res;
    },
    enabled: tab === "overview" && Boolean(currentRun?.id) && Boolean(client.hasPlanDelivery),
    staleTime: 30_000,
  });
  const readiness = useQuery({ queryKey: ["run", reviewedRunID, "capability-readiness"],
    queryFn: ({ signal }) => client.runCapabilityReadiness(reviewedRunID, signal), enabled: tab === "checks" && Boolean(selectedRun) });
  const presetConfigured = (runDetail.isSuccess && !runDetail.isFetching ? runDetail.data?.run?.standard_code_preset_configured : undefined)
    ?? selectedRun?.standard_code_preset_configured;
  const reportIntent = useQuery<ReportAttempt | null>({ queryKey: reportIntentKey(reviewedRunID),
    queryFn: () => null, enabled: false, initialData: null, gcTime: Infinity });
  const savedReport = useQuery<StandardCodeDeliveryView>({ queryKey: ["run", reviewedRunID, "standard-code-delivery"],
    queryFn: ({ signal }) => client.standardCodeDelivery(reviewedRunID, signal), enabled: false });
  const hasHistoricalReport = savedReport.data?.binding.run_id === reviewedRunID || Boolean(reportIntent.data);
  const keyFor = (intent: string) => {
    let key = keys.current.get(intent);
    if (!key) { key = `v2-review-${globalThis.crypto.randomUUID()}`; keys.current.set(intent, key); }
    return key;
  };
  const refresh = (runID = reviewedRunID) => {
    void queryClient.invalidateQueries({ queryKey: v2QueryKeys.thread(detail.thread.id) });
    void queryClient.invalidateQueries({ queryKey: ["run", runID] });
    void queryClient.invalidateQueries({ queryKey: ["workspace", workspaceID] });
  };
  const lifecycle = useMutation({
    mutationFn: ({ runID, action }: { runID: string; threadID: string; workspaceID: string; action: "pause" | "resume" }) => client.controlRunLifecycle(runID,
      { version: "run_lifecycle_control.v1", action }, keyFor(`${runID}:${action}`)),
    onSuccess: (_result, { runID, action }) => { keys.current.delete(`${runID}:${action}`); },
    onSettled: (_result, _error, { runID, threadID, workspaceID }) => {
      void queryClient.invalidateQueries({ queryKey: v2QueryKeys.thread(threadID) });
      void queryClient.invalidateQueries({ queryKey: ["run", runID] });
      void queryClient.invalidateQueries({ queryKey: ["workspace", workspaceID] });
    },
  });
  const report = useMutation({
    mutationFn: (attempt: ReportAttempt) => client.recordStandardCodeDelivery(attempt.runID, attempt.body, attempt.operationKey),
    onSuccess: (result, attempt) => {
      queryClient.setQueryData(reportIntentKey(attempt.runID), (current: ReportAttempt | null | undefined) =>
        current?.operationKey === attempt.operationKey ? null : current);
      queryClient.setQueryData(["run", attempt.runID, "standard-code-delivery"], result.report);
    },
    onError: (error, attempt) => {
      queryClient.setQueryData<ReportAttempt | null>(reportIntentKey(attempt.runID), (current) =>
        current?.operationKey === attempt.operationKey ? { ...attempt, state: "unknown", error: error.message } : current);
    },
    onSettled: (_result, _error, attempt) => {
      void queryClient.invalidateQueries({ queryKey: ["run", attempt.runID] });
      void queryClient.invalidateQueries({ queryKey: v2QueryKeys.thread(attempt.threadID) });
      void queryClient.invalidateQueries({ queryKey: ["workspace", attempt.workspaceID] });
    },
  });
  const submitReport = (attempt: ReportAttempt) => {
    const current = queryClient.getQueryData<ReportAttempt | null>(reportIntentKey(attempt.runID));
    if (!client.hasWorkspaceCheckpointControl || current?.state === "pending" ||
      (current && current.operationKey !== attempt.operationKey)) return;
    queryClient.setQueryData<ReportAttempt>(reportIntentKey(attempt.runID), { ...attempt, state: "pending", error: undefined });
    report.mutate(attempt);
  };
  const createReport = () => {
    if (!selectedRun || working || presetConfigured !== true || !client.hasStandardCodePreset ||
      queryClient.getQueryData(reportIntentKey(reviewedRunID))) return;
    const operationKey = `v2-report-${globalThis.crypto.randomUUID()}`;
    submitReport({ runID: reviewedRunID, threadID: detail.thread.id, workspaceID, operationKey,
      body: { operation_key: operationKey, verification_job_ids: [], uncovered_items: [] }, state: "pending" });
  };
  const requestChange = (context: string) => onRequestChange(
    t(`请调整以下审阅对象：\n${context}\n执行记录：${reviewedRunID}\n具体要求：`, `Update the following reviewed item:\n${context}\nRun: ${reviewedRunID}\nRequirements:`));
  const reviewInner = <>
      {!embedded && <header><div className="v2-review-title"><strong>{t("审阅与交付", "Review and delivery")}</strong><span title={detail.thread.title}>{detail.thread.title}</span></div>
        <button aria-label={t("关闭任务审阅", "Close task review")} onClick={onClose} ref={close} type="button"><X aria-hidden="true" size={18} /></button></header>}
      <div className="v2-review-navigation" ref={navigation}>
        <div className="v2-review-main-nav">
          <div aria-label={t("交付流程", "Delivery workflow")} role="group">{primaryTabs.map(({ value, label, icon: Icon }) => <button
            aria-pressed={tab === value} data-review-tab={value} key={value} onClick={() => selectTab(value)}
            type="button"><Icon size={16} aria-hidden="true" />{label}</button>)}</div>
          <button className="v2-review-history-toggle" aria-expanded={showHistory} aria-controls="v2-review-history-nav"
            onClick={() => setShowHistory((value) => !value)} type="button">{t("记录与恢复", "Records and recovery")}<ChevronDown size={14} aria-hidden="true" /></button>
        </div>
        {showHistory && <div id="v2-review-history-nav" className="v2-review-history-nav" aria-label={t("历史与高级审阅", "History and advanced review")} role="group">
          {historyTabs.map(([value, label]) => <button aria-pressed={tab === value} data-review-tab={value} key={value}
            onClick={() => selectTab(value)} type="button">{label}</button>)}
        </div>}
        {!["overview", "git", "pr"].includes(tab) && <label>{t("执行记录范围 ", "Execution record scope")}<select aria-label={t("选择审阅的执行记录", "Choose a run to review")} onChange={(event) => {
          setSelectedRunID(event.target.value); setFileTarget(undefined); setFilePath(null); lifecycle.reset(); report.reset();
        }} value={reviewedRunID}>
          {!selectedRun && <option value={selectedRunID}>{t("目标执行记录尚未找到", "Target execution record not found")}</option>}
          {detail.runs.map(({ ordinal, run }) => <option key={run.id} value={run.id}>
            {run.id === currentRun.id ? t("当前", "Current") : t("历史", "Past")} {t(" · 第 ", " · Run ")}{ordinal} {t(" 次执行", " runs")}</option>)}
          {!detail.runs.some(({ run }) => run.id === currentRun.id) && <option value={currentRun.id}>{t("当前执行", "Current run")}</option>}
        </select></label>}
      </div>
      <div className="v2-review-body" key={`${reviewedRunID}:${tab}`}>
        {!["overview", "git", "pr"].includes(tab) && <div className="v2-review-history-heading">
          <button onClick={() => selectTab("overview", true)} type="button"><ArrowLeft size={14} aria-hidden="true" />{t("返回任务改动", "Back to task changes")}</button>
          <h2>{primaryTabs.find((item) => item.value === tab)?.label ?? historyTabs.find(([value]) => value === tab)?.[1]}</h2></div>}
        {tab === "overview" && <TaskOverview client={client} threadID={detail.thread.id} onFeedback={onRequestChange} onAddComment={addComment}
          onReviewFile={reviewFile} onGit={() => selectTab("git", true)}
          onPullRequest={() => selectTab("pr", true)}
          onChecks={() => selectTab("checks", true)}
          currentRunDetail={currentPlanQuery.data} />}
        {tab === "git" && <TaskGit client={client} threadID={detail.thread.id} working={working} onFeedback={onRequestChange} onAddComment={addComment} onPullRequest={() => selectTab("pr", true)} onOpenWorktree={onOpenWorktree} />}
        {tab === "pr" && <TaskPullRequest client={client} threadID={detail.thread.id} working={working} onFeedback={onRequestChange} onGit={() => selectTab("git", true)} />}
        {!["overview", "git", "pr"].includes(tab) && !selectedRun && <p role="alert">{t("无法找到目标执行记录 ", "Could not find the target execution record ")}{selectedRunID}{t("，尚未打开审阅工具。请刷新任务后重试，或明确选择另一次执行。", ". Review tools have not opened. Refresh the task and retry, or select another run.")}</p>}
        {tab === "tools" && selectedRun && <TaskReviewTools client={client} runID={reviewedRunID}
          key={`${detail.thread.id}:${reviewedRunID}`} threadID={detail.thread.id}
          retainedReviews={toolMemory.current?.reviews}
          initialTool={initialToolTarget?.runID === reviewedRunID ? initialToolTarget.tool : undefined}
          onGitReviewChange={(review) => retainToolReview("git", review)}
          onGithubReviewChange={(review) => retainToolReview("github", review)}
          onOpenDelivery={() => selectTab("checks", true)} />}
        {tab === "files" && selectedRun && <>
          <p>{selectedRun.id === currentRun.id ? t("查看当前执行的文件提案与编辑记录。", "View file proposals and edit records for the current run.") : t("查看所选历史执行的文件提案与编辑记录。", "View file proposals and edit records for the selected past run.")}{t("先核对提案差异与目录，再批准应用。要核对项目当前内容，可展开下方的“来源项目当前差异”。", "Review proposal diffs and the directory before approving application. Expand Current source project diff to check current project content.")}</p>
          <FileEditPanel client={client} runID={reviewedRunID} runStatus={selectedRun.status} initialTarget={fileTarget} onChanged={() => refresh()}
            requestRevertUnavailableReason={detail.thread.status === "archived"
              ? t("此对话已归档，取消归档后才能发送撤销要求。", "This conversation is archived. Unarchive it to send an undo request.")
              : !client.hasThreadControl || !client.hasSessionMessages
                ? t("当前连接不能发送对话控制请求，无法发起撤销。", "This connection cannot send conversation control requests or request undo.")
                : !client.hasFileEditReview ? t("当前连接没有文件编辑控制权限，无法发起撤销。", "This connection lacks file-edit control permission and cannot request undo.") : undefined}
            onRequestRevert={(edit) => onRequestChange(
              t(`请撤销 ${edit.path} 的这次已应用编辑。根据下面的来源记录生成精确逆向待审提案，等我批准后再应用；保留此后用户修改。\n来源执行：${reviewedRunID}\n编辑记录：${edit.id}\n来源目录：${edit.workspace_id}\n预期当前版本：${edit.proposed_hash}`, `Undo this applied edit to ${edit.path}. Use the source records below to prepare an exact inverse proposal for review. Wait for my approval before applying it and preserve subsequent user edits.\nSource run: ${reviewedRunID}\nEdit: ${edit.id}\nSource directory: ${edit.workspace_id}\nExpected current revision: ${edit.proposed_hash}`))}
            onRequestChange={(edit) => requestChange(t(`文件：${edit.path}${edit.destination_path ? ` → ${edit.destination_path}` : ""}\n目录身份：${edit.workspace_id}\n编辑：${edit.id}\n版本：${edit.original_hash} → ${edit.proposed_hash}`, `File: ${edit.path}${edit.destination_path ? ` → ${edit.destination_path}` : ""}\nDirectory: ${edit.workspace_id}\nEdit: ${edit.id}\nRevision: ${edit.original_hash} → ${edit.proposed_hash}`))} />
          <details className="v2-review-project-diff"><summary>{t("查看来源项目当前差异（含任务外修改）", "View current source project diff (including changes outside this task)")}</summary>
            <p>{t("以下显示接入项目当前的全部改动，包含手动编辑和其他任务产生的修改。请核对目录与文件，选择属于本次任务的范围。", "All current changes in the connected project are shown, including manual edits and other tasks' changes. Review the directory and files to select this task's scope.")}</p>
            <RepositoryDiffPanel client={client} workspaceID={workspaceID}
              onRequestChange={(path, head) => requestChange(t(`项目当前差异：${path}\n基准提交：${head}\n请先重新读取当前文件，保留与本次要求无关的修改。`, `Current project diff: ${path}\nBase commit: ${head}\nRead the current file again and preserve changes unrelated to this request.`))} />
          </details>
        </>}
        {tab === "checks" && selectedRun && <>
          <ProjectChecks client={client} runID={reviewedRunID} threadID={detail.thread.id}
            working={working} onFeedback={onRequestChange} />
          <section aria-label={t("编码环境与计划", "Coding environment and plan")}>
            <h2>{t("编码环境与计划", "Coding environment and plan")}</h2>
            <p>{t("查看所选执行的编码环境与计划。选择计划方向后进入交付，检查和执行进度可在对话与工作记录中查看。", "View the selected run's coding environment and plan. Choose a plan to proceed to delivery. Check progress in the conversation and work records.")}</p>
            {(runDetail.isLoading || readiness.isLoading) && <p role="status">{t("正在读取编码环境与计划…", "Loading coding environment and plan…")}</p>}
            {(runDetail.isError || readiness.isError) && <p role="alert">{t("编码环境或计划读取失败，已有输入保留。", "Could not load the coding environment or plan. Existing input is preserved.")}<button onClick={() => { void runDetail.refetch(); void readiness.refetch(); }} type="button">{t("重试编码环境与计划", "Reload coding environment and plan")}</button></p>}
            {runDetail.data && readiness.data && <>
              <p>{t("所选执行阶段：", "Selected run phase: ")}{runDetail.data.mode.phase === "plan" ? t("计划", "Plan") : runDetail.data.mode.phase === "deliver" ? t("交付", "Delivery") : t("尚未确认", "Unconfirmed")}
                {t(" · 执行记录状态：", " · Run status: ")}{executionStatusLabels[runDetail.data.run.status] ?? t("尚未确认", "Unconfirmed")}</p>
              <StandardCodeReadinessPanel client={client} detail={runDetail.data} readiness={readiness.data}
                key={`preset:${reviewedRunID}`} threadID={detail.thread.id}
                configureDisabledReason={detail.active_run?.id === reviewedRunID &&
                  ["created", "paused", "running"].includes(runDetail.data.run.status) && runDetail.data.mode.surface === "code"
                  ? undefined : t("这里只能配置当前未结束的 Code 执行；历史执行仍可审阅。继续任务请返回原对话发送消息。", "Only the current unfinished Code run can be configured here. Past runs remain reviewable. Return to the conversation and send a message to continue.")} />
              {runDetail.data.plan_delivery && <PlanDeliveryPanel client={client} detail={runDetail.data}
                key={`plan:${reviewedRunID}`} state={runDetail.data.plan_delivery} threadID={detail.thread.id} />}
            </>}
          </section>
          {client.hasWorkspaceCheckpointControl && client.hasStandardCodePreset && presetConfigured === true && reportIntent.data?.state !== "unknown" && <div className="v2-review-report-action">
            <button disabled={working || Boolean(reportIntent.data)} onClick={createReport} type="button">
              {reportIntent.data?.state === "pending" ? t("正在生成报告…", "Generating report…") : t("生成当前交付报告", "Generate current delivery report")}</button>
            <p>{t("此执行已配置标准编码环境。交付报告汇总已有检查记录；可在上方运行项目测试，再核对报告。", "This run uses the standard coding environment. Delivery reports summarize recorded checks. Run project tests above, then review the report.")}</p>
          </div>}
          {presetConfigured === false && !hasHistoricalReport && <>
            <p>{t("此执行使用普通编码流程，下方可查看代码交接与已有命令结果。", "This run uses the regular coding workflow. View code handoff and existing command results below.")}</p>
            <CodeHandoffPanel client={client} runID={reviewedRunID} />
          </>}
          {presetConfigured === undefined && <p role={runDetail.isError ? "alert" : "status"}>
            {runDetail.isFetching ? t("正在核对交付配置…", "Checking delivery configuration…") : t("尚未确认此执行的交付配置，暂不能生成报告。", "Delivery configuration is unconfirmed. A report cannot be generated yet.")}
            {runDetail.isError && <button onClick={() => void runDetail.refetch()} type="button">{t("重试交付配置", "Reload delivery configuration")}</button>}
          </p>}
          {reportIntent.data?.state === "pending" && <p role="status">{t("报告请求处理中；关闭后可回到此执行查看结果。", "The report request is processing. Return to this run later to view the result.")}</p>}
          {reportIntent.data?.state === "unknown" && <div role="alert">
            <p>{t("报告结果尚未确认。请先点击“确认上次报告”，使用原请求核对结果；确认完成后才能生成新报告。", "The report result is unconfirmed. Select Confirm previous report to check the original request before generating another.")}{reportIntent.data.error}</p>
            <button disabled={!client.hasWorkspaceCheckpointControl} onClick={() => submitReport(reportIntent.data!)} type="button">{t("确认上次报告", "Confirm previous report")}</button>
          </div>}
          {(presetConfigured !== false || hasHistoricalReport) && <StandardCodeDeliveryPanel client={client} runID={reviewedRunID}
            onOpenCheckpoints={() => selectTab("restore", true)}
            onOpenFile={(path, actualWorkspaceID) => setFilePath({ path, workspaceID: actualWorkspaceID ?? workspaceID })} />}
          {filePath && <WorkspaceExplorer client={client} workspaceID={filePath.workspaceID} initialPath={filePath.path} />}
        </>}
        {tab === "records" && selectedRun && <ExecutionRecords client={client} runID={reviewedRunID} />}
        {tab === "evidence" && selectedRun && <>
          <p>{t("这些资料已附加到所选执行的上下文中。打开来源会读取项目当前文件，内容可能与当时保存的摘要不同。", "These references were attached to the selected run. Opening a source reads the current project file, which may differ from the saved summary.")}</p>
          <EvidenceInventory client={client} runID={reviewedRunID}
            onOpenSource={(path) => setFilePath({ path, workspaceID })} />
          {filePath && <WorkspaceExplorer client={client} workspaceID={filePath.workspaceID} initialPath={filePath.path} />}
        </>}
        {tab === "restore" && selectedRun && <>
          <p>{t("项目快照恢复会影响整个项目，请先预览恢复范围。撤销单个文件时，在“编辑明细”中选择已应用的编辑，审阅撤销提案。检查点之外的命令影响需另行处理。", "Restoring a project snapshot affects the whole project; preview the scope first. To undo one file, select an applied edit in Edit details and review its undo proposal. Handle command effects outside the checkpoint separately.")}</p>
          {client.hasRunLifecycle && ["running", "paused"].includes(selectedRun.status) && <div className="v2-review-pause">
            <button disabled={working || lifecycle.isPending} onClick={() => lifecycle.mutate({
              runID: reviewedRunID, threadID: detail.thread.id, workspaceID,
              action: selectedRun.status === "paused" ? "resume" : "pause" })} type="button">
              {selectedRun.status === "paused" ? t("恢复此执行", "Resume this run") : t("暂停此执行以预览撤销", "Pause this run to preview undo")}</button>
            {working && <p>{t("请先停止当前执行，再进行恢复操作。", "Stop the current run before recovery.")}</p>}
            {lifecycle.isError && <p role="alert">{t("无法切换所选执行的状态：", "Could not change the selected run's state: ")}{lifecycle.error.message}</p>}
          </div>}
          <WorkspaceCheckpointPanel variant="conversation" client={client} onChanged={() => refresh()}
            runID={reviewedRunID} runStatus={selectedRun.status} />
        </>}
      </div>
      <footer className={onAddComment ? "v2-review-comment-status" : undefined}>
        {onAddComment ? <p role={commentResult === "unsaved" ? "alert" : "status"}>{commentResult === "unsaved"
          ? t("意见已加入本窗口草稿，但保存或版本有问题。请返回草稿处理，暂勿刷新或退出。", "Comment added in this window, but the draft has a storage or version issue. Return to the draft before reloading or leaving.")
          : commentResult === "added" ? t("意见已加入聊天草稿，尚未发送。可继续评论其他行。", "Comment added to the chat draft, not sent. You can comment on more lines.")
          : t("点击差异行旁的 + 写意见，加入聊天草稿后再发送。", "Use + beside a diff line to add feedback to your chat draft, then send from chat.")}</p> : <span>{t("关闭面板会保留当前改动。", "Closing this panel preserves current changes.")}</span>}
        <button onClick={onAddComment ? () => onRequestChange("") : onClose} type="button">
          {onAddComment ? t("返回聊天草稿", "Return to chat draft") : t("返回对话", "Back to conversation")}</button>
      </footer>
  </>;
  if (embedded) return <div className="v2-review-drawer is-embedded">{reviewInner}</div>;
  return createPortal(<div className="v2-inspector-backdrop" role="presentation"
    onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
    <section aria-label={t("审阅任务改动", "Review task changes")} aria-modal="true" className="v2-inspector-drawer v2-review-drawer"
      ref={dialog} role="dialog" tabIndex={-1}>
      {reviewInner}
    </section>
  </div>, document.body);
}

function ExecutionRecords({ client, runID }: { client: APIClient; runID: string }) {
  const { t } = useLocale();
  const query = useInfiniteQuery({
    queryKey: ["run", runID, "review-tool-rounds"],
    queryFn: ({ signal, pageParam }) => client.getPage<SupervisorToolRoundView>(
      `/runs/${encodeURIComponent(runID)}/tool-rounds`, { limit: 100 }, pageParam, signal),
    initialPageParam: "", getNextPageParam: (last) => last.page.next_cursor || undefined,
  });
  const rounds = query.data?.pages.flatMap(({ items }) => items) ?? [];
  return <section aria-label={t("实际执行记录", "Actual execution records")}>
    <p>{t("查看实际工具调用及其结果。检查结论适用于当时的代码版本，请结合交付报告核对版本；代码变化后可重新运行检查。", "View actual tool calls and results. Check conclusions apply to the code at that time. Compare revisions with the delivery report and rerun checks after code changes.")}</p>
    {query.isLoading && <p role="status">{t("正在加载执行记录…", "Loading execution records…")}</p>}
    {query.isError && <p role="alert">{t("执行记录加载失败。", "Could not load execution records.")}<button onClick={() => void query.refetch()} type="button">{t("重试执行记录", "Reload execution records")}</button></p>}
    {!query.isLoading && !query.isError && rounds.length === 0 && <p>{t("此执行没有可展示的结构化工具记录，可回到对话查看其他活动。", "This run has no displayable structured tool records. View other activity in the conversation.")}</p>}
    {rounds.map((round) => <section key={`${round.attempt_id}:${round.turn}:${round.round}`}><h3>{t("第 ", "Turn ")}{round.turn} {t(" 轮", " ")}</h3>
      {round.calls.map((call) => <details key={call.call_id}><summary>{call.tool_name} · {call.status}</summary>
        {call.detail_available && call.detail ? <ThreadActivityToolDetailPanel activityRef={call.call_id}
          client={client} runID={round.run_id} threadID={round.thread_id ?? ""} tool={call.detail} />
          : <p>{t("此历史调用没有可展示的结构化结果。", "This past call has no displayable structured result.")}</p>}</details>)}
    </section>)}
    {query.hasNextPage && <button disabled={query.isFetchingNextPage}
      onClick={() => void query.fetchNextPage()} type="button">{t("加载更早执行记录", "Load earlier execution records")}</button>}
  </section>;
}
