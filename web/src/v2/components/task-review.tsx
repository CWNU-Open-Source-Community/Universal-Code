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
import { ProjectActions } from "./project-actions";
import { ProjectPreparation } from "./project-preparation";
import { TaskReviewTools, type TaskReviewToolMemory, type TaskReviewToolReviews } from "./task-review-tools";
import type { WorkspaceView } from "../../api/types";
import "./task-review.css";

type ReviewTab = "overview" | "git" | "pr" | "tools" | "files" | "checks" | "records" | "restore" | "evidence";
const historyTabs: [ReviewTab, string][] = [["files", "编辑明细"],
  ["records", "执行记录"], ["evidence", "参考资料"], ["restore", "撤销与恢复"]];
const primaryTabs = [{ value: "overview", label: "查看改动", icon: FileDiff },
  { value: "git", label: "提交与推送", icon: GitCommitHorizontal },
  { value: "pr", label: "PR 状态", icon: GitPullRequest },
  { value: "checks", label: "检查与交付", icon: FileDiff },
  { value: "tools", label: "更多交付工具", icon: Wrench }] as const;
const executionStatusLabels: Record<string, string> = { created: "尚未开始", preparing: "准备中", running: "未结束",
  paused: "已暂停", waiting_approval: "等待批准", completed: "已完成",
  failed: "执行失败", cancelled: "已停止" };

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

export function V2TaskReview({ client, detail, working, onClose, onRequestChange, returnFocusRef, onOpenWorktree, initialFileTarget,
  initialToolTarget, toolMemoryRef }: {
  client: APIClient; detail: ThreadDetailView; working: boolean;
  onClose: () => void; onRequestChange: (context: string) => void;
  returnFocusRef: RefObject<HTMLElement | null>;
  onOpenWorktree?: (workspace: WorkspaceView) => void;
  initialFileTarget?: FileEditReviewTarget;
  initialToolTarget?: { runID: string; tool: "github-review" };
  toolMemoryRef?: RefObject<TaskReviewToolMemory>;
}) {
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
  const navigation = useRef<HTMLDivElement>(null);
  const selectTab = (value: ReviewTab, focusNavigation = false) => {
    setTab(value); setFilePath(null);
    setShowHistory((expanded) => value === "checks" ? expanded : !primaryTabs.some((item) => item.value === value));
    if (focusNavigation) requestAnimationFrame(() => navigation.current?.querySelector<HTMLButtonElement>(`[data-review-tab="${value}"]`)?.focus());
  };
  const [filePath, setFilePath] = useState<{ path: string; workspaceID: string } | null>(null);
  const close = useRef<HTMLButtonElement>(null);
  const dialog = useModalFocusTrap<HTMLElement>(true, onClose, false, close,
    { isolateBackground: true, returnFocusRef });
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
    `请调整以下审阅对象：\n${context}\n执行记录：${reviewedRunID}\n具体要求：`);
  return createPortal(<div className="v2-inspector-backdrop" role="presentation"
    onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
    <section aria-label="审阅任务改动" aria-modal="true" className="v2-inspector-drawer v2-review-drawer"
      ref={dialog} role="dialog" tabIndex={-1}>
      <header><div className="v2-review-title"><strong>审阅与交付</strong><span title={detail.thread.title}>{detail.thread.title}</span></div>
        <button aria-label="关闭任务审阅" onClick={onClose} ref={close} type="button"><X aria-hidden="true" size={18} /></button></header>
      <div className="v2-review-navigation" ref={navigation}>
        <div className="v2-review-main-nav">
          <div aria-label="交付流程" role="group">{primaryTabs.map(({ value, label, icon: Icon }) => <button
            aria-pressed={tab === value} data-review-tab={value} key={value} onClick={() => selectTab(value)}
            type="button"><Icon size={16} aria-hidden="true" />{label}</button>)}</div>
          <button className="v2-review-history-toggle" aria-expanded={showHistory} aria-controls="v2-review-history-nav"
            onClick={() => setShowHistory((value) => !value)} type="button">记录与恢复<ChevronDown size={14} aria-hidden="true" /></button>
        </div>
        {showHistory && <div id="v2-review-history-nav" className="v2-review-history-nav" aria-label="历史与高级审阅" role="group">
          {historyTabs.map(([value, label]) => <button aria-pressed={tab === value} data-review-tab={value} key={value}
            onClick={() => selectTab(value)} type="button">{label}</button>)}
        </div>}
        {!["overview", "git", "pr"].includes(tab) && <label>执行记录范围 <select aria-label="选择审阅的执行记录" onChange={(event) => {
          setSelectedRunID(event.target.value); setFileTarget(undefined); setFilePath(null); lifecycle.reset(); report.reset();
        }} value={reviewedRunID}>
          {!selectedRun && <option value={selectedRunID}>目标执行记录尚未找到</option>}
          {detail.runs.map(({ ordinal, run }) => <option key={run.id} value={run.id}>
            {run.id === currentRun.id ? "当前" : "历史"} · 第 {ordinal} 次执行</option>)}
          {!detail.runs.some(({ run }) => run.id === currentRun.id) && <option value={currentRun.id}>当前执行</option>}
        </select></label>}
      </div>
      <div className="v2-review-body" key={`${reviewedRunID}:${tab}`}>
        {!["overview", "git", "pr"].includes(tab) && <div className="v2-review-history-heading">
          <button onClick={() => selectTab("overview", true)} type="button"><ArrowLeft size={14} aria-hidden="true" />返回任务改动</button>
          <h2>{primaryTabs.find((item) => item.value === tab)?.label ?? historyTabs.find(([value]) => value === tab)?.[1]}</h2></div>}
        {tab === "overview" && <TaskOverview client={client} threadID={detail.thread.id} onFeedback={onRequestChange}
          onReviewFile={reviewFile} onGit={() => selectTab("git", true)}
          onPullRequest={() => selectTab("pr", true)}
          onChecks={() => selectTab("checks", true)}
          currentRunDetail={currentPlanQuery.data} />}
        {tab === "git" && <TaskGit client={client} threadID={detail.thread.id} working={working} onFeedback={onRequestChange} onPullRequest={() => selectTab("pr", true)} onOpenWorktree={onOpenWorktree} />}
        {tab === "pr" && <TaskPullRequest client={client} threadID={detail.thread.id} working={working} onFeedback={onRequestChange} onGit={() => selectTab("git", true)} />}
        {!["overview", "git", "pr"].includes(tab) && !selectedRun && <p role="alert">无法找到目标执行记录 {selectedRunID}，尚未打开审阅工具。请刷新任务后重试，或明确选择另一次执行。</p>}
        {tab === "tools" && selectedRun && <TaskReviewTools client={client} runID={reviewedRunID}
          key={`${detail.thread.id}:${reviewedRunID}`} threadID={detail.thread.id}
          retainedReviews={toolMemory.current?.reviews}
          initialTool={initialToolTarget?.runID === reviewedRunID ? initialToolTarget.tool : undefined}
          onGitReviewChange={(review) => retainToolReview("git", review)}
          onGithubReviewChange={(review) => retainToolReview("github", review)}
          onOpenDelivery={() => selectTab("checks", true)} />}
        {tab === "files" && selectedRun && <>
          <p>{selectedRun.id === currentRun.id ? "查看当前执行的文件提案与编辑记录。" : "查看所选历史执行的文件提案与编辑记录。"}先核对提案差异与目录，再批准应用。要核对项目当前内容，可展开下方的“来源项目当前差异”。</p>
          <FileEditPanel client={client} runID={reviewedRunID} runStatus={selectedRun.status} initialTarget={fileTarget} onChanged={() => refresh()}
            requestRevertUnavailableReason={detail.thread.status === "archived"
              ? "此对话已归档，取消归档后才能发送撤销要求。"
              : !client.hasThreadControl || !client.hasSessionMessages
                ? "当前连接不能发送对话控制请求，无法发起撤销。"
                : !client.hasFileEditReview ? "当前连接没有文件编辑控制权限，无法发起撤销。" : undefined}
            onRequestRevert={(edit) => onRequestChange(
              `请撤销 ${edit.path} 的这次已应用编辑。根据下面的来源记录生成精确逆向待审提案，等我批准后再应用；保留此后用户修改。\n来源执行：${reviewedRunID}\n编辑记录：${edit.id}\n来源目录：${edit.workspace_id}\n预期当前版本：${edit.proposed_hash}`)}
            onRequestChange={(edit) => requestChange(`文件：${edit.path}${edit.destination_path ? ` → ${edit.destination_path}` : ""}\n目录身份：${edit.workspace_id}\n编辑：${edit.id}\n版本：${edit.original_hash} → ${edit.proposed_hash}`)} />
          <details className="v2-review-project-diff"><summary>查看来源项目当前差异（含任务外修改）</summary>
            <p>以下显示接入项目当前的全部改动，包含手动编辑和其他任务产生的修改。请核对目录与文件，选择属于本次任务的范围。</p>
            <RepositoryDiffPanel client={client} workspaceID={workspaceID}
              onRequestChange={(path, head) => requestChange(`项目当前差异：${path}\n基准提交：${head}\n请先重新读取当前文件，保留与本次要求无关的修改。`)} />
          </details>
        </>}
        {tab === "checks" && selectedRun && <>
          <ProjectPreparation client={client} runID={reviewedRunID} threadID={detail.thread.id} working={working} />
          <ProjectActions client={client} runID={reviewedRunID} threadID={detail.thread.id}
            working={working} onFeedback={onRequestChange} />
          <section aria-label="编码环境与计划">
            <h2>编码环境与计划</h2>
            <p>查看所选执行的编码环境与计划。选择计划方向后进入交付，检查和执行进度可在对话与工作记录中查看。</p>
            {(runDetail.isLoading || readiness.isLoading) && <p role="status">正在读取编码环境与计划…</p>}
            {(runDetail.isError || readiness.isError) && <p role="alert">编码环境或计划读取失败，已有输入保留。
              <button onClick={() => { void runDetail.refetch(); void readiness.refetch(); }} type="button">重试编码环境与计划</button></p>}
            {runDetail.data && readiness.data && <>
              <p>所选执行阶段：{runDetail.data.mode.phase === "plan" ? "计划" : runDetail.data.mode.phase === "deliver" ? "交付" : "尚未确认"}
                {" · 执行记录状态："}{executionStatusLabels[runDetail.data.run.status] ?? "尚未确认"}</p>
              <StandardCodeReadinessPanel client={client} detail={runDetail.data} readiness={readiness.data}
                key={`preset:${reviewedRunID}`} threadID={detail.thread.id}
                configureDisabledReason={detail.active_run?.id === reviewedRunID &&
                  ["created", "paused", "running"].includes(runDetail.data.run.status) && runDetail.data.mode.surface === "code"
                  ? undefined : "这里只能配置当前未结束的 Code 执行；历史执行仍可审阅。继续任务请返回原对话发送消息。"} />
              {runDetail.data.plan_delivery && <PlanDeliveryPanel client={client} detail={runDetail.data}
                key={`plan:${reviewedRunID}`} state={runDetail.data.plan_delivery} threadID={detail.thread.id} />}
            </>}
          </section>
          {client.hasWorkspaceCheckpointControl && client.hasStandardCodePreset && presetConfigured === true && reportIntent.data?.state !== "unknown" && <div className="v2-review-report-action">
            <button disabled={working || Boolean(reportIntent.data)} onClick={createReport} type="button">
              {reportIntent.data?.state === "pending" ? "正在生成报告…" : "生成当前交付报告"}</button>
            <p>此执行已配置标准编码环境。交付报告汇总已有检查记录；可在上方运行项目测试，再核对报告。</p>
          </div>}
          {presetConfigured === false && !hasHistoricalReport && <>
            <p>此执行使用普通编码流程，下方可查看代码交接与已有命令结果。</p>
            <CodeHandoffPanel client={client} runID={reviewedRunID} />
          </>}
          {presetConfigured === undefined && <p role={runDetail.isError ? "alert" : "status"}>
            {runDetail.isFetching ? "正在核对交付配置…" : "尚未确认此执行的交付配置，暂不能生成报告。"}
            {runDetail.isError && <button onClick={() => void runDetail.refetch()} type="button">重试交付配置</button>}
          </p>}
          {reportIntent.data?.state === "pending" && <p role="status">报告请求处理中；关闭后可回到此执行查看结果。</p>}
          {reportIntent.data?.state === "unknown" && <div role="alert">
            <p>报告结果尚未确认。请先点击“确认上次报告”，使用原请求核对结果；确认完成后才能生成新报告。{reportIntent.data.error}</p>
            <button disabled={!client.hasWorkspaceCheckpointControl} onClick={() => submitReport(reportIntent.data!)} type="button">确认上次报告</button>
          </div>}
          {(presetConfigured !== false || hasHistoricalReport) && <StandardCodeDeliveryPanel client={client} runID={reviewedRunID}
            onOpenCheckpoints={() => selectTab("restore", true)}
            onOpenFile={(path, actualWorkspaceID) => setFilePath({ path, workspaceID: actualWorkspaceID ?? workspaceID })} />}
          {filePath && <WorkspaceExplorer client={client} workspaceID={filePath.workspaceID} initialPath={filePath.path} />}
        </>}
        {tab === "records" && selectedRun && <ExecutionRecords client={client} runID={reviewedRunID} />}
        {tab === "evidence" && selectedRun && <>
          <p>这些资料已附加到所选执行的上下文中。打开来源会读取项目当前文件，内容可能与当时保存的摘要不同。</p>
          <EvidenceInventory client={client} runID={reviewedRunID}
            onOpenSource={(path) => setFilePath({ path, workspaceID })} />
          {filePath && <WorkspaceExplorer client={client} workspaceID={filePath.workspaceID} initialPath={filePath.path} />}
        </>}
        {tab === "restore" && selectedRun && <>
          <p>项目快照恢复会影响整个项目，请先预览恢复范围。撤销单个文件时，在“编辑明细”中选择已应用的编辑，审阅撤销提案。检查点之外的命令影响需另行处理。</p>
          {client.hasRunLifecycle && ["running", "paused"].includes(selectedRun.status) && <div className="v2-review-pause">
            <button disabled={working || lifecycle.isPending} onClick={() => lifecycle.mutate({
              runID: reviewedRunID, threadID: detail.thread.id, workspaceID,
              action: selectedRun.status === "paused" ? "resume" : "pause" })} type="button">
              {selectedRun.status === "paused" ? "恢复此执行" : "暂停此执行以预览撤销"}</button>
            {working && <p>请先停止当前执行，再进行恢复操作。</p>}
            {lifecycle.isError && <p role="alert">无法切换所选执行的状态：{lifecycle.error.message}</p>}
          </div>}
          <WorkspaceCheckpointPanel variant="conversation" client={client} onChanged={() => refresh()}
            runID={reviewedRunID} runStatus={selectedRun.status} />
        </>}
      </div>
      <footer><span>关闭面板会保留当前改动。</span>
        <button onClick={onClose} type="button">返回对话</button></footer>
    </section>
  </div>, document.body);
}

function ExecutionRecords({ client, runID }: { client: APIClient; runID: string }) {
  const query = useInfiniteQuery({
    queryKey: ["run", runID, "review-tool-rounds"],
    queryFn: ({ signal, pageParam }) => client.getPage<SupervisorToolRoundView>(
      `/runs/${encodeURIComponent(runID)}/tool-rounds`, { limit: 100 }, pageParam, signal),
    initialPageParam: "", getNextPageParam: (last) => last.page.next_cursor || undefined,
  });
  const rounds = query.data?.pages.flatMap(({ items }) => items) ?? [];
  return <section aria-label="实际执行记录">
    <p>查看实际工具调用及其结果。检查结论适用于当时的代码版本，请结合交付报告核对版本；代码变化后可重新运行检查。</p>
    {query.isLoading && <p role="status">正在加载执行记录…</p>}
    {query.isError && <p role="alert">执行记录加载失败。<button onClick={() => void query.refetch()} type="button">重试执行记录</button></p>}
    {!query.isLoading && !query.isError && rounds.length === 0 && <p>此执行没有可展示的结构化工具记录，可回到对话查看其他活动。</p>}
    {rounds.map((round) => <section key={`${round.attempt_id}:${round.turn}:${round.round}`}><h3>第 {round.turn} 轮</h3>
      {round.calls.map((call) => <details key={call.call_id}><summary>{call.tool_name} · {call.status}</summary>
        {call.detail_available && call.detail ? <ThreadActivityToolDetailPanel activityRef={call.call_id}
          client={client} runID={round.run_id} threadID={round.thread_id ?? ""} tool={call.detail} />
          : <p>此历史调用没有可展示的结构化结果。</p>}</details>)}
    </section>)}
    {query.hasNextPage && <button disabled={query.isFetchingNextPage}
      onClick={() => void query.fetchNextPage()} type="button">加载更早执行记录</button>}
  </section>;
}
