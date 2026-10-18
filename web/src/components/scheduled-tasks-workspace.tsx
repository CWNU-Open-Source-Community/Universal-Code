import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CalendarClock, Download, LoaderCircle, Pause, Play, Plus, RefreshCw, Square } from "lucide-react";
import type { APIClient } from "../api/client";
import type { ScheduledJobCreateRequestView, ScheduledJobObservationRequestView, ScheduledJobView, ThreadDetailView, ThreadView } from "../api/types";
import { readThreadReview } from "../api/task-delivery";
import { useLocale } from "../lib/locale";
import { AutomationPRFollowup } from "./automation-pr-followup";
import "./scheduled-tasks-workspace.css";

const scheduleListKey = ["scheduled-jobs"] as const;
type AutomationMode = "thread" | "workflow" | "pr" | "observation";
const terminal = (status: string) => ["completed", "failed", "cancelled", "exhausted"].includes(status);

function localDateTime(date: Date): string {
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 16);
}
function operationKey(action: string): string {
  const random = typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
    ? crypto.randomUUID() : Date.now() + "-" + Math.random().toString(36).slice(2);
  return "web-scheduled-job-" + action + "-" + random;
}
function formatTime(value: string | undefined, locale: string): string {
  return value ? new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(new Date(value)) : "—";
}
function downloadBundle(runID: string, value: unknown): void {
  const url = URL.createObjectURL(new Blob([JSON.stringify(value, null, 2)], { type: "application/json" }));
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = "cyberagent-diagnostics-" + runID + ".json";
  anchor.click();
  URL.revokeObjectURL(url);
}

export function ScheduledTasksWorkspace({ client, initialRunID = "", initialThreadID = "" }: {
  client: APIClient; initialRunID?: string; initialThreadID?: string;
}) {
  const { locale, t } = useLocale();
  const queryClient = useQueryClient();
  const [mode, setMode] = useState<AutomationMode>("thread");
  const [threadID, setThreadID] = useState(initialThreadID);
  const initialTargetResolved = useRef(Boolean(initialThreadID));
  const [observationRunID, setObservationRunID] = useState(initialRunID);
  const [selectedJobID, setSelectedJobID] = useState("");
  const [prompt, setPrompt] = useState("");
  const [workflowID, setWorkflowID] = useState("");
  const [inputs, setInputs] = useState("{}");
  const [kind, setKind] = useState<"once" | "periodic">("once");
  const [anchorAt, setAnchorAt] = useState(() => localDateTime(new Date(Date.now() + 300_000)));
  const [deadlineAt, setDeadlineAt] = useState(() => localDateTime(new Date(Date.now() + 86_400_000)));
  const [intervalMinutes, setIntervalMinutes] = useState("15");
  const [maxRounds, setMaxRounds] = useState("12");
  const [notification, setNotification] = useState<ScheduledJobCreateRequestView["notification"]>("on_change");
  const pendingCreate = useRef<{ client: APIClient; intent: string; runID: string; body: ScheduledJobCreateRequestView; key: string } | null>(null);
  const pendingObservation = useRef<{ client: APIClient; runID: string; jobID: string; body: ScheduledJobObservationRequestView; key: string } | null>(null);

  useEffect(() => {
    initialTargetResolved.current = Boolean(initialThreadID);
    setThreadID(initialThreadID);
    setObservationRunID(initialRunID);
  }, [initialRunID, initialThreadID]);
  const threads = useInfiniteQuery({
    queryKey: ["automation-threads"], initialPageParam: "",
    queryFn: ({ signal, pageParam }) => client.getPage<ThreadView>("/threads", { limit: 100, status: "active" }, pageParam, signal),
    getNextPageParam: (last) => last.page.next_cursor || undefined,
  });
  const threadItems = useMemo(() => threads.data?.pages.flatMap((page) => page.items) ?? [], [threads.data]);
  useEffect(() => {
    if (!initialTargetResolved.current && !threadID && initialRunID) {
      const source = threadItems.find((item) => item.active_run_id === initialRunID || item.last_run_id === initialRunID);
      if (source) {
        initialTargetResolved.current = true;
        setThreadID(source.id);
      }
    }
  }, [threadID, initialRunID, threadItems]);
  const thread = useQuery({ queryKey: ["v2", "thread", threadID],
    queryFn: ({ signal }) => client.get<ThreadDetailView>("/threads/" + encodeURIComponent(threadID), {}, signal), enabled: Boolean(threadID) });
  const targetThread = thread.data?.thread ?? threadItems.find((item) => item.id === threadID);
  const runID = mode === "observation" && !threadID ? observationRunID.trim()
    : targetThread?.active_run_id || targetThread?.last_run_id || "";
  const ownerRun = thread.data?.active_run?.id === runID ? thread.data.active_run : thread.data?.last_run;
  const permission = useQuery({ queryKey: ["v2", "thread", threadID, "permission"],
    queryFn: ({ signal }) => client.getThreadExecutionPermission(threadID, signal), enabled: Boolean(threadID) });
  const directory = useQuery({ queryKey: ["thread", threadID, "task-review"],
    queryFn: ({ signal }) => readThreadReview(client, threadID, signal), enabled: Boolean(threadID) });
  const definitions = useQuery({ queryKey: ["workflow-definitions"],
    queryFn: ({ signal }) => client.workflows.definitions(signal), enabled: mode === "workflow" });
  const list = useInfiniteQuery({ queryKey: scheduleListKey, initialPageParam: "",
    queryFn: ({ signal, pageParam }) => client.listScheduledJobs("", 100, signal, pageParam),
    getNextPageParam: (last) => last.page?.next_cursor || undefined,
    refetchInterval: client.hasScheduledJobWorker ? 5_000 : false });
  const jobs = useMemo(() => list.data?.pages.flatMap((page) => page.items) ?? [], [list.data]);
  const detail = useQuery({ queryKey: ["scheduled-job", selectedJobID],
    queryFn: ({ signal }) => client.getScheduledJob(selectedJobID, signal), enabled: Boolean(selectedJobID),
    refetchInterval: client.hasScheduledJobWorker ? 5_000 : false });
  const health = useQuery({ queryKey: ["scheduled-jobs-worker-health"], queryFn: ({ signal }) => client.runtimeCapabilities(signal), refetchInterval: 5_000 });
  const selected = detail.data?.snapshot.job ?? jobs.find((job) => job.id === selectedJobID);
  const refresh = async (job?: ScheduledJobView) => {
    if (job) {
      queryClient.setQueryData(scheduleListKey, (previous: typeof list.data) => previous ? { ...previous,
        pages: previous.pages.map((page, index) => ({ ...page,
          items: index === 0 ? [job, ...page.items.filter((item) => item.id !== job.id)] : page.items.filter((item) => item.id !== job.id) })) } : previous);
      setSelectedJobID(job.id);
    }
    await Promise.all([queryClient.invalidateQueries({ queryKey: scheduleListKey }),
      ...(job ? [queryClient.invalidateQueries({ queryKey: ["scheduled-job", job.id] })] : [])]);
  };
  const create = useMutation({ mutationFn: async () => {
    const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
    const intent = JSON.stringify([runID, mode, threadID, prompt, workflowID, inputs, kind, anchorAt, deadlineAt, intervalMinutes, maxRounds, notification, timezone]);
    const previous = pendingCreate.current;
    if (previous?.client === client && previous.intent === intent) {
      const result = await client.createScheduledJob(previous.runID, previous.body, previous.key);
      if (pendingCreate.current === previous) pendingCreate.current = null;
      return result;
    }
    const anchor = new Date(anchorAt), deadline = new Date(deadlineAt);
    const interval = Number(intervalMinutes), rounds = Number(maxRounds);
    const elapsedSeconds = Math.floor((deadline.getTime() - Date.now()) / 1_000);
    if (!runID || !Number.isFinite(anchor.getTime()) || !Number.isFinite(deadline.getTime()) || deadline <= anchor ||
      elapsedSeconds < 1 || elapsedSeconds > 90 * 24 * 60 * 60 || !Number.isSafeInteger(rounds) || rounds < 1 || rounds > 1_000 ||
      (kind === "periodic" && (!Number.isSafeInteger(interval) || interval < 1 || interval > 30 * 24 * 60))) {
      throw new Error(t("请填写有效的目标、时间与次数上限", "Enter a valid target, time window, and execution limit"));
    }
    let executionTarget: ScheduledJobCreateRequestView["execution_target"];
    if (mode === "thread") {
      if (!threadID || !prompt.trim()) throw new Error(t("选择任务并填写执行要求", "Select a task and enter its instructions"));
      executionTarget = { kind: "thread", thread_id: threadID, prompt: prompt.trim() };
    } else if (mode === "workflow") {
      if (!workflowID || ownerRun?.status !== "running") throw new Error(t("选择已保存工作流，并绑定正在运行的任务", "Select a saved workflow and a running task"));
      const parsed: unknown = JSON.parse(inputs);
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error(t("工作流输入需要 JSON 对象", "Workflow inputs must be a JSON object"));
      executionTarget = { kind: "workflow", workflow_definition_id: workflowID, inputs: parsed };
    }
    const body: ScheduledJobCreateRequestView = {
      version: "scheduled-job.v1", schedule: { kind, timezone, anchor_at: anchor.toISOString(), misfire_policy: "run_once", ...(kind === "periodic" ? { interval_seconds: interval * 60 } : {}) },
      deadline_at: deadline.toISOString(), stop_on_target_terminal: mode === "observation", max_rounds: rounds, max_model_calls: 0,
      max_elapsed_seconds: elapsedSeconds, retry: { max_attempts: 3, initial_backoff_seconds: 5, max_backoff_seconds: 60 },
      notification, execution_mode: mode === "observation" ? "read_only" : "automatic", confirm_repair: false,
      observation_consent_version: mode === "observation" ? 1 : 0,
      ...(executionTarget ? { execution_target: executionTarget, execution_consent_version: 1 } : {}),
    };
    const attempt = { client, intent, runID, body, key: operationKey("create") };
    pendingCreate.current = attempt;
    const result = await client.createScheduledJob(runID, body, attempt.key);
    if (pendingCreate.current === attempt) pendingCreate.current = null;
    return result;
  }, onSuccess: (result) => refresh(result.job) });
  const transition = useMutation({ mutationFn: ({ job, action }: { job: ScheduledJobView; action: "pause" | "resume" | "cancel" }) => client.transitionScheduledJob(job.owner_run_id, job.id, action,
    { version: "scheduled-job-control.v1", expected_revision: job.revision }, operationKey(action)), onSuccess: (result) => refresh(result.job) });
  const bundle = useMutation({ mutationFn: (job: ScheduledJobView) => client.diagnosticBundle(job.owner_run_id), onSuccess: (value, job) => downloadBundle(job.owner_run_id, value) });
  const enableObservation = useMutation({ mutationFn: async (job: ScheduledJobView) => {
    const previous = pendingObservation.current;
    const attempt = previous?.client === client && previous.runID === job.owner_run_id && previous.jobID === job.id && previous.body.expected_revision === job.revision ? previous : {
      client, runID: job.owner_run_id, jobID: job.id, body: { version: "scheduled-job-control.v1" as const, expected_revision: job.revision, observation_consent_version: 1 }, key: operationKey("enable-observation") };
    pendingObservation.current = attempt;
    const result = await client.enableScheduledJobObservation(attempt.runID, attempt.jobID, attempt.body, attempt.key);
    if (pendingObservation.current === attempt) pendingObservation.current = null;
    return result;
  }, onSuccess: (result) => refresh(result.job) });

  const error = create.error ?? transition.error ?? enableObservation.error ?? bundle.error ?? list.error ?? detail.error ?? health.error ?? threads.error ?? thread.error ?? permission.error ?? directory.error ?? definitions.error;
  const worker = health.data?.scheduled_job_worker;
  const workerLabel = health.isError ? t("调度状态暂时无法确认", "Scheduler status could not be confirmed")
    : !worker ? t("正在检查调度状态…", "Checking scheduler status…") : !worker.enabled ? t("调度器待启用", "Scheduler awaits enablement")
      : worker.state === "running" ? worker.selection_scope === "confirmed_read_only" ? t("观察器运行中", "Observer is running") : t("调度器运行中", "Scheduler is running")
        : worker.state === "ready" ? t("调度器正在启动", "Scheduler is starting") : t("观察器已停止或正在退出", "Observer is stopped or shutting down");
  const scopeLabel = worker?.selection_scope === "all_jobs" ? t("显式全计划调度（包含旧计划）", "Explicit scheduling of all jobs, including legacy jobs")
    : worker?.selection_scope === "confirmed_read_only" ? t("观察范围：已确认的只读计划", "Observation scope: confirmed read-only schedules")
      : worker?.selection_scope === "confirmed_jobs" ? t("调度范围：已确认的观察与执行计划", "Scheduling scope: confirmed observation and execution schedules") : "";
  const canExecute = client.hasRunExecution && client.hasModelControl;
  const currentPermission = permission.data?.execution_permission;
  const fullInactive = currentPermission?.approval_mode === "full" && currentPermission.full_activation !== "active";
  const permissionLabel = fullInactive
    ? currentPermission.full_activation === "unavailable" ? t("FULL · 不可用", "FULL · unavailable") : t("FULL · 未激活", "FULL · inactive")
    : currentPermission?.approval_mode.toUpperCase() || "—";
  const createDisabled = !client.hasScheduledJobControl || create.isPending || !runID ||
    (mode !== "observation" && !canExecute) ||
    (mode === "thread" && (!threadID || !prompt.trim())) || (mode === "workflow" && (!workflowID || ownerRun?.status !== "running"));
  const submit = (event: FormEvent) => { event.preventDefault(); if (!createDisabled) create.mutate(); };
  const jobTitle = (job: ScheduledJobView) => {
    const target = job.spec.execution_target;
    return target?.kind === "thread" ? threadItems.find((item) => item.id === target.thread_id)?.title || target.thread_id
      : target?.kind === "workflow" ? definitions.data?.find((item) => item.id === target.workflow_definition_id)?.spec.name || target.workflow_definition_id : job.owner_run_id;
  };
  const modeLabel = (job: ScheduledJobView) => job.spec.execution_target?.kind === "workflow" ? t("工作流", "Workflow")
    : job.spec.execution_mode === "automatic" ? t("任务", "Task") : t("只读观察", "Read-only observation");
  const statusLabel = (value: string) => ({ active: t("已启用", "Active"), paused: t("已暂停", "Paused"), completed: t("已完成", "Completed"),
    failed: t("失败", "Failed"), cancelled: t("已取消", "Cancelled"), exhausted: t("已达上限", "Limit reached"), queued: t("已排队", "Queued"),
    running: t("运行中", "Running"), waiting_approval: t("等待审批", "Waiting for approval"), blocked: t("等待处理", "Blocked") }[value] || value);

  return <section className="utility-workspace scheduled-tasks-workspace">
    <header><CalendarClock aria-hidden="true" size={18} /><div><h1>{t("自动化", "Automations")}</h1><small>{workerLabel}{scopeLabel && " · " + scopeLabel}</small></div>
      <button aria-label={t("刷新自动化", "Refresh automations")} className="compact-command" disabled={list.isFetching}
        onClick={() => { void list.refetch(); void health.refetch(); }} type="button"><RefreshCw aria-hidden="true" className={list.isFetching ? "spin" : ""} size={14} />{t("刷新", "Refresh")}</button></header>
    <p>{t("按时间继续已有任务、运行已保存工作流，或跟进 PR 与 CI。应用运行时自动推进；需要审批时在原任务中等待处理。",
      "Continue an existing task on a schedule, run a saved workflow, or follow PR and CI feedback. Automations advance while the app is open and wait in the source task when approval is needed.")}</p>
    <div className="automation-mode-picker" role="group" aria-label={t("自动化方式", "Automation type")}>
      {([ ["thread", t("定时执行任务", "Scheduled task")], ["workflow", t("定时运行工作流", "Scheduled workflow")], ["pr", t("PR 与 CI 反馈", "PR and CI feedback")], ["observation", t("只读观察", "Read-only observation")] ] as const).map(([value, label]) =>
        <button type="button" key={value} aria-pressed={mode === value} onClick={() => { setMode(value); create.reset(); }}>{label}</button>)}
    </div>
    <section className="automation-target" aria-label={t("目标任务与权限", "Target task and permissions")}>
      <label>{t("已有任务", "Existing task")}<select value={threadID} onChange={(event) => { initialTargetResolved.current = true; setThreadID(event.target.value); }}>
        <option value="">{threads.isLoading ? t("正在读取任务…", "Loading tasks…") : t("选择一个任务", "Select a task")}</option>
        {initialThreadID && !threadItems.some((item) => item.id === initialThreadID) && <option value={initialThreadID}>{targetThread?.title || initialThreadID}</option>}
        {threadItems.map((item) => <option key={item.id} value={item.id}>{item.title || item.id}</option>)}</select></label>
      {threads.hasNextPage && <button type="button" className="compact-command" disabled={threads.isFetchingNextPage} onClick={() => void threads.fetchNextPage()}>{t("加载更多任务", "Load more tasks")}</button>}
      {threadID && <div className="automation-target-facts"><span>{t("绑定目录", "Directory")} <strong>{directory.data?.target.root_path || (directory.isLoading ? t("正在读取…", "Loading…") : t("尚未读取目录", "Directory unavailable"))}</strong></span>
        <span>{t("当前权限", "Current permission")} <strong>{permissionLabel}</strong></span>
        <a href={"#/threads/" + encodeURIComponent(threadID)}>{t("打开任务与审批", "Open task and approvals")}</a></div>}
      {threadID && fullInactive && <p role="status">{t("Full 尚未生效。请在原任务中重新激活后再执行。", "Full is not active. Reactivate it in the source task before execution.")}</p>}
      {threadID && mode !== "observation" && <p>{t("使用本任务当前的 Full / Ask / Auto 权限和运行预算。暂停或取消自动化会停止后续触发；已开始的执行可在原任务中停止。", "Uses this task’s current Full / Ask / Auto permissions and run budget. Pausing or cancelling stops future triggers; stop an execution already in progress in its source task.")}</p>}
    </section>
    {mode === "pr" ? threadID ? <AutomationPRFollowup key={threadID} client={client} threadID={threadID} />
      : <p className="utility-empty-state">{t("选择任务，设置 PR 与 CI 的持续跟进。", "Select a task to follow its PR and CI feedback.")}</p> : <>
      <form className="scheduled-create-form" onSubmit={submit}>
        <div className="scheduled-form-heading"><Plus aria-hidden="true" size={15} /><strong>{mode === "observation" ? t("新建观察计划", "Create an observation schedule") : t("新建自动化", "Create an automation")}</strong></div>
        {mode === "thread" && <label className="automation-wide-field">{t("执行要求", "Instructions")}<textarea required rows={3} value={prompt} onChange={(event) => setPrompt(event.target.value)} placeholder={t("例如：检查新增审阅意见，完成修复并运行相关测试", "For example: address new review feedback and run the relevant tests")} /></label>}
        {mode === "workflow" && <><label className="automation-wide-field">{t("已保存工作流", "Saved workflow")}<select value={workflowID} onChange={(event) => setWorkflowID(event.target.value)}><option value="">{t("选择工作流", "Select a workflow")}</option>{definitions.data?.map((item) => <option key={item.id} value={item.id}>{item.spec.name}</option>)}</select></label>
          {definitions.isSuccess && !definitions.data.length && <p className="automation-wide-field">{t("还没有已保存工作流。可在任务的“运行与工具”中创建。", "No saved workflows yet. Create one in the task’s Run and tools workspace.")}</p>}
          <label className="automation-wide-field">{t("工作流输入（JSON 对象）", "Workflow inputs (JSON object)")}<textarea rows={3} value={inputs} onChange={(event) => setInputs(event.target.value)} spellCheck={false} /></label>
          {threadID && ownerRun?.status !== "running" && <p className="automation-wide-field inline-warning">{t("工作流需绑定正在运行的任务。请先在原任务中继续执行。", "A workflow requires a running task. Continue execution in the source task first.")}</p>}</>}
        {mode === "observation" && !threadID && <label className="automation-wide-field">{t("目标 Run ID（也可在上方选择任务）", "Target Run ID (or select a task above)")}<input maxLength={256} value={observationRunID} onChange={(event) => setObservationRunID(event.target.value)} placeholder="run-…" /></label>}
        <label>{t("调度类型", "Schedule type")}<select value={kind} onChange={(event) => setKind(event.target.value as "once" | "periodic")}><option value="once">{t("单次", "Once")}</option><option value="periodic">{t("周期", "Periodic")}</option></select></label>
        <label>{t("首次运行", "First run")}<input type="datetime-local" value={anchorAt} onChange={(event) => setAnchorAt(event.target.value)} /></label>
        {kind === "periodic" && <label>{t("间隔（分钟）", "Interval (minutes)")}<input type="number" min={1} value={intervalMinutes} onChange={(event) => setIntervalMinutes(event.target.value)} /></label>}
        <label>{t("截止时间", "Deadline")}<input type="datetime-local" value={deadlineAt} onChange={(event) => setDeadlineAt(event.target.value)} /></label>
        <label>{mode === "observation" ? t("最多观察次数", "Maximum observations") : t("最多执行次数", "Maximum executions")}<input type="number" min={1} max={1000} value={maxRounds} onChange={(event) => setMaxRounds(event.target.value)} /></label>
        <label>{t("通知", "Notifications")}<select value={notification} onChange={(event) => setNotification(event.target.value as ScheduledJobCreateRequestView["notification"])}><option value="on_change">{t("状态变化时", "On change")}</option><option value="on_failure">{t("失败时", "On failure")}</option><option value="all">{t("每次执行", "Every execution")}</option><option value="silent">{t("静默", "Silent")}</option></select></label>
        <button className="command-button" disabled={createDisabled} type="submit">{create.isPending ? <LoaderCircle aria-hidden="true" className="spin" size={14} /> : <Plus aria-hidden="true" size={14} />}{mode === "observation" ? t("创建并观察", "Create and observe") : t("创建自动化", "Create automation")}</button>
        {mode === "observation" && <p className="automation-wide-field">{t("只读取任务状态并记录变化，模型调用保持关闭。", "Reads task state and records changes with model calls disabled.")}</p>}
      </form>
      {!client.hasScheduledJobControl && <p className="inline-warning">{t("启用定时执行控制并连接控制凭证后，可创建或调整自动化。", "Enable scheduled execution control and connect a control credential to create or adjust automations.")}</p>}
      {mode !== "observation" && !canExecute && <p className="inline-warning">{t("当前连接未启用模型执行控制。可切换为只读观察，或连接具备任务执行权限的应用。", "Model execution control is unavailable on this connection. Choose read-only observation or connect an app with task execution enabled.")}</p>}
    </>}
    {error && <p className="inline-warning" role="alert">{error instanceof Error ? error.message : t("自动化操作失败", "Automation operation failed")}</p>}
    <div className="automation-list-heading"><strong>{t("时间计划", "Time schedules")}</strong><span>{jobs.length} {t("个计划", "schedules")} · {jobs.filter((job) => job.status === "active").length} {t("已启用", "active")}</span></div>
    <div className="scheduled-task-layout">
      <div className="scheduled-task-list" aria-label={t("自动化列表", "Automation list")}>
        {jobs.map((job) => <button className={job.id === selectedJobID ? "selected" : ""} key={job.id} onClick={() => setSelectedJobID(job.id)} type="button"><span><strong>{jobTitle(job)}</strong><span className="status-badge">{statusLabel(job.status)}</span></span><small>{modeLabel(job)} · {job.spec.schedule.kind === "once" ? t("单次", "Once") : t("周期", "Periodic")} · {formatTime(job.next_wake_at, locale)}</small><small>{job.id}</small>
          {["confirmed_read_only", "confirmed_jobs"].includes(worker?.selection_scope ?? "") && job.status === "active" && job.spec.execution_mode === "read_only" && job.observation_consent_version !== 1 && <small>{t("普通桌面观察尚未确认", "Desktop observation needs confirmation")}</small>}</button>)}
        {!list.isLoading && jobs.length === 0 && <div className="utility-empty-state"><CalendarClock aria-hidden="true" size={25} /><strong>{t("还没有时间计划，选择任务与触发时间开始。", "No time schedules yet. Choose a task and trigger time to begin.")}</strong></div>}
        {list.hasNextPage && <button className="compact-command" disabled={list.isFetchingNextPage}
          onClick={() => void list.fetchNextPage()} type="button">{list.isFetchingNextPage
            ? t("正在加载计划…", "Loading schedules…") : list.isFetchNextPageError
              ? t("重试加载更多计划", "Retry loading more schedules") : t("加载更多计划", "Load more schedules")}</button>}
        {list.data?.pages.at(-1)?.page?.truncated && <p role="status">{t("已达到计划浏览上限。", "Schedule browsing limit reached.")}</p>}
      </div>
      <div className="scheduled-task-detail">{selected ? <>
        <header><div><strong>{jobTitle(selected)}</strong><small>{selected.id}</small></div><span className="status-badge">{statusLabel(selected.status)}</span></header>
        <dl><div><dt>{t("下次运行", "Next run")}</dt><dd>{formatTime(selected.next_wake_at, locale)}</dd></div><div><dt>{t("截止时间", "Deadline")}</dt><dd>{formatTime(selected.spec.deadline_at, locale)}</dd></div><div><dt>{t("已执行次数", "Executions")}</dt><dd>{selected.rounds_completed} / {selected.spec.max_rounds}</dd></div><div><dt>{t("最近结果", "Latest result")}</dt><dd>{selected.last_result || selected.last_error_code || "—"}</dd></div></dl>
        <div className="scheduled-task-actions">
          {selected.observation_consent_version !== 1 && selected.spec.execution_mode === "read_only" && selected.spec.max_model_calls === 0 && ["active", "paused"].includes(selected.status) && <button className="compact-command" disabled={!client.hasScheduledJobControl || enableObservation.isPending || transition.isPending} onClick={() => enableObservation.mutate(selected)} type="button"><Play aria-hidden="true" size={13} />{t("确认普通桌面的自动观察", "Confirm automatic desktop observation")}</button>}
          {selected.status === "active" && <button className="compact-command" disabled={!client.hasScheduledJobControl || transition.isPending || enableObservation.isPending} onClick={() => transition.mutate({ job: selected, action: "pause" })} type="button"><Pause aria-hidden="true" size={13} />{t("暂停", "Pause")}</button>}
          {selected.status === "paused" && <button className="compact-command" disabled={!client.hasScheduledJobControl || transition.isPending || enableObservation.isPending} onClick={() => transition.mutate({ job: selected, action: "resume" })} type="button"><Play aria-hidden="true" size={13} />{t("恢复", "Resume")}</button>}
          {!terminal(selected.status) && <button className="compact-command danger" disabled={!client.hasScheduledJobControl || transition.isPending || enableObservation.isPending} onClick={() => transition.mutate({ job: selected, action: "cancel" })} type="button"><Square aria-hidden="true" size={12} />{t("取消", "Cancel")}</button>}
          <button className="compact-command" disabled={bundle.isPending} onClick={() => bundle.mutate(selected)} type="button"><Download aria-hidden="true" size={13} />{t("导出诊断包", "Export diagnostics")}</button>
        </div>
        {selected.spec.execution_target?.kind === "thread" && selected.spec.execution_target.thread_id && <a className="automation-result-link" href={"#/threads/" + encodeURIComponent(selected.spec.execution_target.thread_id)}>{t("打开执行任务", "Open execution task")}</a>}
        {detail.data?.snapshot.executions?.length ? <section className="automation-executions" aria-label={t("执行结果", "Execution results")}><strong>{t("执行结果", "Execution results")}</strong>{detail.data.snapshot.executions.map((item) => <article key={item.job_id + ":" + item.occurrence_at}><div><span className="status-badge">{statusLabel(item.state)}</span><small>{formatTime(item.occurrence_at, locale)}</small></div>{item.result && <p>{item.result}</p>}{item.error_code && <p role="alert">{item.error_code}</p>}{item.run_id && <a href={"#/new/inspector/runs/" + encodeURIComponent(item.run_id)}>{t("查看运行记录", "View run record")}</a>}{item.workflow_execution_id && <small>{t("工作流执行", "Workflow execution")} {item.workflow_execution_id}</small>}</article>)}</section> : null}
        {detail.data?.snapshot.notifications.length ? <section className="scheduled-notifications"><strong>{t("通知", "Notifications")}</strong>{detail.data.snapshot.notifications.map((notice) => <p key={notice.id}><span className="status-badge">{notice.kind}</span>{notice.summary}</p>)}</section> : null}
      </> : <div className="utility-empty-state"><CalendarClock aria-hidden="true" size={25} /><strong>{t("选择计划，查看执行结果、通知与暂停控制。", "Select a schedule to view results, notifications, and pause controls.")}</strong></div>}</div>
    </div>
  </section>;
}
