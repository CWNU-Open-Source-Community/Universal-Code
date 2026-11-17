import { useEffect, useId, useMemo, useRef, useState, type FormEvent } from "react";
import { createPortal } from "react-dom";
import { useInfiniteQuery, useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import {
  CalendarClock,
  CheckCircle2,
  Clock,
  Download,
  ExternalLink,
  Eye,
  FileCode2,
  History,
  LoaderCircle,
  Maximize2,
  Pause,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  Search,
  Sparkles,
  Square,
  X,
  Zap,
} from "lucide-react";
import type { APIClient } from "../../api/client";
import type {
  ScheduledJobCreateRequestView,
  ScheduledJobDetailView,
  ScheduledJobObservationRequestView,
  ScheduledJobView,
  ThreadDetailView,
  ThreadView,
  WorkspaceView,
} from "../../api/types";
import { AutomationPRFollowup } from "../../components/automation-pr-followup";
import { useModalFocusTrap } from "../../hooks/use-modal-focus-trap";
import { downloadTextFile } from "../../lib/download";
import { useLocale } from "../../lib/locale";
import {
  AUTOMATION_PROMPT_TEMPLATES,
  compileScheduledJobRequest,
  consumeAutomationDraftPrefill,
  describeExistingSchedule,
  NATURAL_SCHEDULE_PRESETS,
  parseNaturalSchedule,
} from "../automation-nl-schedule";
import "./resource-page.css";
import "./automations-page.css";

const scheduleListKey = ["scheduled-jobs"] as const;

type AutomationMode = "thread" | "workflow" | "pr" | "observation";
type StudioTab = "schedules" | "history";
type HistoryFilter = "all" | "succeeded" | "failed" | "running" | "queued" | "paused";
type ApprovalPolicyChoice = "ask" | "auto" | "full";

interface NormalizedHistoryRecord {
  id: string;
  job: ScheduledJobView;
  jobTitle: string;
  category: Exclude<HistoryFilter, "all">;
  rawState: string;
  timestamp: string;
  roundsCompleted: number;
  maxRounds: number;
  resultText?: string;
  errorCode?: string;
  threadID?: string;
  runID?: string;
  workflowExecutionID?: string;
}

const isTerminalStatus = (status: string) =>
  ["completed", "failed", "cancelled", "exhausted"].includes(status);

function makeOperationKey(action: string): string {
  const random =
    typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
      ? crypto.randomUUID()
      : `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  return `v2-scheduled-job-${action}-${random}`;
}

function formatDateTime(value: string | undefined, locale: string): string {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "—";
  return new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(date);
}

function categorizeExecutionState(
  state: string,
  jobStatus: string,
): Exclude<HistoryFilter, "all"> {
  if (state === "paused" || (state === "job_snapshot" && jobStatus === "paused")) return "paused";
  if (state === "failed" || state === "retry_wait") return "failed";
  if (state === "running" || state === "claimed") return "running";
  if (state === "queued" || state === "waiting_approval" || state === "blocked") return "queued";
  if (state === "completed" || state === "unchanged") return "succeeded";
  if (jobStatus === "failed") return "failed";
  if (jobStatus === "paused") return "paused";
  return "succeeded";
}

export function V2AutomationsPage({
  client,
  initialRunID = "",
  initialThreadID = "",
}: {
  client: APIClient;
  initialRunID?: string;
  initialThreadID?: string;
}) {
  const { locale, t } = useLocale();
  const isZh = locale.startsWith("zh");
  const queryClient = useQueryClient();
  const formUid = useId();

  // Studio view tabs & filters
  const [activeTab, setActiveTab] = useState<StudioTab>("schedules");
  const [historyFilter, setHistoryFilter] = useState<HistoryFilter>("all");
  const [jobStatusFilter, setJobStatusFilter] = useState<"all" | "active" | "paused" | "ended">("all");
  const [searchQuery, setSearchQuery] = useState("");

  // Automation creation / editing state
  const [mode, setMode] = useState<AutomationMode>("thread");
  const [threadID, setThreadID] = useState(initialThreadID);
  const initialTargetResolved = useRef(Boolean(initialThreadID));
  const [observationRunID, setObservationRunID] = useState(initialRunID);
  const [selectedJobID, setSelectedJobID] = useState("");

  const [studioTitle, setStudioTitle] = useState("");
  const [prompt, setPrompt] = useState("");
  const [scheduleExpr, setScheduleExpr] = useState(() =>
    isZh ? "每天早上 9 点" : "every day at 9am",
  );
  const [scheduleTouched, setScheduleTouched] = useState(false);
  const [promptTouched, setPromptTouched] = useState(false);
  const [maxRoundsInput, setMaxRoundsInput] = useState("30");
  const [notification, setNotification] =
    useState<ScheduledJobCreateRequestView["notification"]>("on_change");
  const [workflowID, setWorkflowID] = useState("");
  const [workflowInputs, setWorkflowInputs] = useState("{}");

  // Fullscreen editor state
  const [fullscreenOpen, setFullscreenOpen] = useState(false);
  const [editingJob, setEditingJob] = useState<ScheduledJobView | null>(null);
  const [markdownPreview, setMarkdownPreview] = useState(false);
  const [studioWorkspaceID, setStudioWorkspaceID] = useState("");
  const [selectedModelKey, setSelectedModelKey] = useState("");
  const [approvalPolicy, setApprovalPolicy] = useState<ApprovalPolicyChoice>("ask");
  const [approvalTouched, setApprovalTouched] = useState(false);
  const [actionNotice, setActionNotice] = useState("");

  const fullscreenTriggerRef = useRef<HTMLButtonElement>(null);
  const fullscreenPromptRef = useRef<HTMLTextAreaElement>(null);
  const quickScheduleInputRef = useRef<HTMLInputElement>(null);
  const studioDialogRef = useModalFocusTrap<HTMLElement>(
    fullscreenOpen,
    () => {
      setFullscreenOpen(false);
      setEditingJob(null);
    },
    false,
    fullscreenPromptRef,
    { isolateBackground: true, returnFocusRef: fullscreenTriggerRef },
  );

  const pendingObservation = useRef<{
    client: APIClient;
    runID: string;
    jobID: string;
    body: ScheduledJobObservationRequestView;
    key: string;
  } | null>(null);

  useEffect(() => {
    initialTargetResolved.current = Boolean(initialThreadID);
    setThreadID(initialThreadID);
    setObservationRunID(initialRunID);
  }, [initialRunID, initialThreadID]);

  useEffect(() => {
    const prefill = consumeAutomationDraftPrefill();
    if (!prefill) return;
    if (prefill.prompt) setPrompt(prefill.prompt);
    if (prefill.scheduleExpression) setScheduleExpr(prefill.scheduleExpression);
    if (prefill.title) setStudioTitle(prefill.title);
    setFullscreenOpen(true);
  }, []);

  const parsedSchedule = useMemo(
    () => parseNaturalSchedule(scheduleExpr),
    [scheduleExpr],
  );

  // Queries (all guarded so minimal mock clients never crash or hang)
  const threadsQuery = useInfiniteQuery({
    queryKey: ["automation-threads"],
    initialPageParam: "",
    enabled: typeof client.getPage === "function",
    queryFn: ({ signal, pageParam }) =>
      client.getPage<ThreadView>("/threads", { limit: 100, status: "active" }, pageParam, signal),
    getNextPageParam: (last) => last.page.next_cursor || undefined,
  });
  const threadItems = useMemo(
    () => threadsQuery.data?.pages.flatMap((page) => page.items) ?? [],
    [threadsQuery.data],
  );

  const workspacesQuery = useInfiniteQuery({
    queryKey: ["v2", "workspaces"],
    initialPageParam: "",
    enabled: fullscreenOpen && typeof client.getPage === "function",
    queryFn: ({ signal, pageParam }) =>
      client.getPage<WorkspaceView>("/workspaces", { limit: 100 }, pageParam, signal),
    getNextPageParam: (last) => last.page.next_cursor || undefined,
    staleTime: 30_000,
  });
  const workspaces = useMemo(
    () => workspacesQuery.data?.pages.flatMap((page) => page.items) ?? [],
    [workspacesQuery.data],
  );

  const modelRoutesQuery = useQuery({
    queryKey: ["v2", "models", "available-routes"],
    enabled: fullscreenOpen && typeof client.availableModelRoutes === "function",
    queryFn: () => client.availableModelRoutes(),
  });
  const selectableRoutes = useMemo(
    () => (modelRoutesQuery.data?.routes ?? []).filter((route) => route.selectable),
    [modelRoutesQuery.data?.routes],
  );

  useEffect(() => {
    if (!initialTargetResolved.current && !threadID && initialRunID) {
      const source = threadItems.find(
        (item) => item.active_run_id === initialRunID || item.last_run_id === initialRunID,
      );
      if (source) {
        initialTargetResolved.current = true;
        setThreadID(source.id);
      }
    }
  }, [threadID, initialRunID, threadItems]);

  const threadDetailQuery = useQuery({
    queryKey: ["v2", "thread", threadID],
    enabled: Boolean(threadID) && typeof client.get === "function",
    queryFn: ({ signal }) =>
      client.get<ThreadDetailView>(`/threads/${encodeURIComponent(threadID)}`, {}, signal),
  });
  const targetThread =
    threadDetailQuery.data?.thread ?? threadItems.find((item) => item.id === threadID);

  const permissionQuery = useQuery({
    queryKey: ["v2", "thread", threadID, "permission"],
    enabled: Boolean(threadID) && typeof client.getThreadExecutionPermission === "function",
    queryFn: ({ signal }) => client.getThreadExecutionPermission(threadID, signal),
  });

  useEffect(() => {
    if (!approvalTouched && permissionQuery.data?.execution_permission?.approval_mode) {
      const modeValue = permissionQuery.data.execution_permission.approval_mode;
      if (modeValue === "ask" || modeValue === "auto" || modeValue === "full") {
        setApprovalPolicy(modeValue);
      }
    }
  }, [approvalTouched, permissionQuery.data?.execution_permission?.approval_mode]);

  const workflowDefsQuery = useQuery({
    queryKey: ["workflow-definitions"],
    enabled: mode === "workflow" && Boolean(client.workflows?.definitions),
    queryFn: ({ signal }) => client.workflows.definitions(signal),
  });

  const listQuery = useInfiniteQuery({
    queryKey: scheduleListKey,
    initialPageParam: "",
    enabled: typeof client.listScheduledJobs === "function",
    queryFn: ({ signal, pageParam }) => client.listScheduledJobs("", 100, signal, pageParam),
    getNextPageParam: (last) => last.page?.next_cursor || undefined,
    refetchInterval: client.hasScheduledJobWorker ? 5_000 : false,
  });
  const jobs = useMemo(
    () => listQuery.data?.pages.flatMap((page) => page.items) ?? [],
    [listQuery.data],
  );

  // Select initial job automatically when jobs load
  useEffect(() => {
    if (selectedJobID && jobs.some((job) => job.id === selectedJobID)) return;
    if (initialRunID) {
      const match = jobs.find(
        (job) => job.owner_run_id === initialRunID || job.id === initialRunID,
      );
      if (match) {
        setSelectedJobID(match.id);
        return;
      }
    }
    if (jobs[0]) {
      setSelectedJobID(jobs[0].id);
    }
  }, [jobs, selectedJobID, initialRunID]);

  const detailQuery = useQuery({
    queryKey: ["scheduled-job", selectedJobID],
    enabled: Boolean(selectedJobID) && typeof client.getScheduledJob === "function",
    queryFn: ({ signal }) => client.getScheduledJob(selectedJobID, signal),
    refetchInterval: client.hasScheduledJobWorker ? 5_000 : false,
  });

  // When viewing Run History tab, also load details for top jobs so full execution history is available
  const historyDetailQueries = useQueries({
    queries: (activeTab === "history" && typeof client.getScheduledJob === "function"
      ? jobs.slice(0, 8)
      : []
    ).map((job) => ({
      queryKey: ["scheduled-job", job.id],
      queryFn: ({ signal }: { signal: AbortSignal }) => client.getScheduledJob(job.id, signal),
      staleTime: 10_000,
    })),
  });

  const healthQuery = useQuery({
    queryKey: ["scheduled-jobs-worker-health"],
    enabled: typeof client.runtimeCapabilities === "function",
    queryFn: ({ signal }) => client.runtimeCapabilities(signal),
    refetchInterval: client.hasScheduledJobWorker ? 5_000 : false,
  });

  const selectedJob =
    detailQuery.data?.snapshot.job ?? jobs.find((job) => job.id === selectedJobID);

  const runID =
    mode === "observation" && !threadID
      ? observationRunID.trim()
      : targetThread?.active_run_id ||
        targetThread?.last_run_id ||
        threadDetailQuery.data?.active_run?.id ||
        threadDetailQuery.data?.last_run?.id ||
        (threadID ? initialRunID : "");

  const ownerRun =
    threadDetailQuery.data?.active_run?.id === runID
      ? threadDetailQuery.data.active_run
      : threadDetailQuery.data?.last_run;

  const jobTitle = (job: ScheduledJobView): string => {
    const target = job.spec.execution_target;
    if (target?.kind === "thread") {
      const matched = threadItems.find((item) => item.id === target.thread_id);
      if (matched?.title) return matched.title;
      if (target.prompt) {
        const firstLine = target.prompt
          .split(/\r?\n/u)
          .map((line) => line.replace(/^#+\s*/u, "").trim())
          .find(Boolean);
        if (firstLine) return firstLine.slice(0, 60);
      }
      return target.thread_id || job.id;
    }
    if (target?.kind === "workflow") {
      return (
        workflowDefsQuery.data?.find((item) => item.id === target.workflow_definition_id)?.spec
          .name ||
        target.workflow_definition_id ||
        job.id
      );
    }
    return job.owner_run_id || job.id;
  };

  const refreshJobs = async (updated?: ScheduledJobView) => {
    if (updated) {
      queryClient.setQueryData(scheduleListKey, (previous: typeof listQuery.data) =>
        previous
          ? {
              ...previous,
              pages: previous.pages.map((page, index) => ({
                ...page,
                items:
                  index === 0
                    ? [updated, ...page.items.filter((item) => item.id !== updated.id)]
                    : page.items.filter((item) => item.id !== updated.id),
              })),
            }
          : previous,
      );
      setSelectedJobID(updated.id);
    }
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: scheduleListKey }),
      ...(updated ? [queryClient.invalidateQueries({ queryKey: ["scheduled-job", updated.id] })] : []),
    ]);
  };

  const saveAutomationMutation = useMutation({
    mutationFn: async () => {
      if (!parsedSchedule.ok) {
        throw new Error(isZh ? parsedSchedule.error.zh : parsedSchedule.error.en);
      }
      const rounds = Number.parseInt(maxRoundsInput, 10);
      if (!Number.isSafeInteger(rounds) || rounds < 1 || rounds > 1_000) {
        throw new Error(t("最多执行次数需在 1 到 1000 之间。", "Maximum executions must be between 1 and 1000."));
      }

      let effectiveThreadID = threadID;
      let effectiveRunID = runID;

      // In Fullscreen Studio, allow creating a dedicated automation thread when a workspace is chosen without an existing thread
      if (
        mode === "thread" &&
        !effectiveThreadID &&
        studioWorkspaceID &&
        client.hasThreadControl &&
        typeof client.createThread === "function"
      ) {
        const routeParts = selectedModelKey ? selectedModelKey.split("::") : [];
        const created = await client.createThread(
          {
            version: "thread_creation.v1",
            workspace_id: studioWorkspaceID,
            goal: studioTitle.trim() || prompt.trim().slice(0, 120) || t("定时自动化任务", "Scheduled automation"),
            profile: "code",
            surface: "code",
            phase: "deliver",
            network_mode: "disabled",
            web_access_mode: "rules",
            web_allowed_targets: [],
            ...(routeParts.length === 2 && routeParts[0] && routeParts[1]
              ? { provider: routeParts[0], model: routeParts[1] }
              : {}),
          } as Parameters<APIClient["createThread"]>[0] & { provider?: string; model?: string },
          makeOperationKey("create-thread"),
        );
        effectiveThreadID = created.thread.id;
        effectiveRunID = created.thread.active_run_id || created.thread.last_run_id || "";
        setThreadID(effectiveThreadID);
      }

      if (!effectiveRunID && editingJob) {
        effectiveRunID = editingJob.owner_run_id;
      }

      if (!effectiveRunID) {
        throw new Error(
          t(
            "请选择一个已有任务（或在全屏编辑器中选择工作区新建自动化会话）。",
            "Select an existing task (or pick a workspace in Fullscreen Studio to create a new automation thread).",
          ),
        );
      }

      let executionTarget: ScheduledJobCreateRequestView["execution_target"];
      if (mode === "thread") {
        const finalPrompt = studioTitle.trim()
          ? `# ${studioTitle.trim()}\n\n${prompt.trim()}`
          : prompt.trim();
        if (!effectiveThreadID || !finalPrompt) {
          throw new Error(t("请填写自动化执行指令并关联目标会话。", "Enter automation instructions and select a target thread."));
        }
        executionTarget = {
          kind: "thread",
          thread_id: effectiveThreadID,
          prompt: finalPrompt,
        };
      } else if (mode === "workflow") {
        if (!workflowID) {
          throw new Error(t("请选择要定时运行的工作流。", "Select a saved workflow to schedule."));
        }
        const parsedInputs: unknown = JSON.parse(workflowInputs);
        if (!parsedInputs || typeof parsedInputs !== "object" || Array.isArray(parsedInputs)) {
          throw new Error(t("工作流输入必须是 JSON 对象。", "Workflow inputs must be a JSON object."));
        }
        executionTarget = {
          kind: "workflow",
          workflow_definition_id: workflowID,
          inputs: parsedInputs,
        };
      }

      if (
        effectiveThreadID &&
        approvalTouched &&
        client.hasExecutionPermissionControl &&
        typeof client.changeThreadExecutionPermission === "function"
      ) {
        await client.changeThreadExecutionPermission(
          effectiveThreadID,
          {
            mode: approvalPolicy,
            confirm_full: approvalPolicy === "full",
          },
          makeOperationKey("permission"),
        );
      }

      if (
        editingJob &&
        !isTerminalStatus(editingJob.status) &&
        typeof client.transitionScheduledJob === "function"
      ) {
        await client.transitionScheduledJob(
          editingJob.owner_run_id,
          editingJob.id,
          "cancel",
          {
            version: "scheduled-job-control.v1",
            expected_revision: editingJob.revision,
          },
          makeOperationKey("supersede"),
        );
      }

      const requestBody = compileScheduledJobRequest({
        parsed: parsedSchedule,
        mode: mode === "observation" ? "observation" : mode === "workflow" ? "workflow" : "thread",
        executionTarget,
        maxRounds: rounds,
        notification,
      });

      return client.createScheduledJob(effectiveRunID, requestBody, makeOperationKey("create"));
    },
    onSuccess: async (result) => {
      const wasEditing = Boolean(editingJob);
      setFullscreenOpen(false);
      setEditingJob(null);
      setStudioTitle("");
      setPrompt("");
      setPromptTouched(false);
      setScheduleTouched(false);
      setActionNotice(
        wasEditing
          ? t("自动化计划已更新并启用。", "Automation schedule updated and enabled.")
          : t("自动化计划已创建并加入调度队列。", "Automation created and scheduled."),
      );
      await refreshJobs(result.job);
    },
  });

  const transitionMutation = useMutation({
    mutationFn: ({
      job,
      action,
    }: {
      job: ScheduledJobView;
      action: "pause" | "resume" | "cancel";
    }) =>
      client.transitionScheduledJob(
        job.owner_run_id,
        job.id,
        action,
        {
          version: "scheduled-job-control.v1",
          expected_revision: job.revision,
        },
        makeOperationKey(action),
      ),
    onSuccess: async (result, variables) => {
      setActionNotice(
        variables.action === "pause"
          ? t("计划已暂停。", "Automation paused.")
          : variables.action === "resume"
            ? t("计划已恢复运行。", "Automation resumed.")
            : t("计划已取消。", "Automation cancelled."),
      );
      await refreshJobs(result.job);
    },
  });

  const runNowMutation = useMutation({
    mutationFn: async (job: ScheduledJobView) => {
      const target = job.spec.execution_target;
      if (
        target?.kind === "thread" &&
        target.thread_id &&
        client.hasThreadControl &&
        typeof client.submitThreadTurn === "function"
      ) {
        await client.submitThreadTurn(
          target.thread_id,
          {
            version: "thread_message_submission.v1",
            content: target.prompt || t("立即执行定时巡检任务", "Run scheduled check now"),
          },
          makeOperationKey("run-now-turn"),
        );
        return { kind: "turn" as const, job };
      }
      if (client.hasScheduledJobControl && typeof client.createScheduledJob === "function") {
        const now = new Date();
        const immediateAnchor = new Date(now.getTime() + 2_000).toISOString();
        const immediateDeadline = new Date(now.getTime() + 3_600_000).toISOString();
        const timezone = job.spec.schedule.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
        const body: ScheduledJobCreateRequestView = {
          version: "scheduled-job.v1",
          schedule: {
            kind: "once",
            timezone,
            anchor_at: immediateAnchor,
            misfire_policy: "run_once",
          },
          deadline_at: immediateDeadline,
          stop_on_target_terminal: job.spec.execution_mode === "read_only",
          max_rounds: 1,
          max_model_calls: 0,
          max_elapsed_seconds: 3_600,
          retry: job.spec.retry,
          notification: job.spec.notification,
          execution_mode: job.spec.execution_mode,
          confirm_repair: false,
          observation_consent_version: job.spec.execution_mode === "read_only" ? 1 : 0,
          ...(target
            ? {
                execution_target: target,
                execution_consent_version: 1,
              }
            : {}),
        };
        const created = await client.createScheduledJob(
          job.owner_run_id,
          body,
          makeOperationKey("run-now-once"),
        );
        return { kind: "job" as const, job: created.job };
      }
      throw new Error(t("当前连接未开启立即执行权限。", "Immediate execution requires control permissions."));
    },
    onSuccess: async (outcome) => {
      setActionNotice(
        t("已触发立即执行一次，运行结果将同步到历史运行记录。", "Triggered an immediate run; results will appear in Run History."),
      );
      await refreshJobs(outcome.kind === "job" ? outcome.job : undefined);
    },
  });

  const enableObservationMutation = useMutation({
    mutationFn: async (job: ScheduledJobView) => {
      const previous = pendingObservation.current;
      const attempt =
        previous?.client === client &&
        previous.runID === job.owner_run_id &&
        previous.jobID === job.id &&
        previous.body.expected_revision === job.revision
          ? previous
          : {
              client,
              runID: job.owner_run_id,
              jobID: job.id,
              body: {
                version: "scheduled-job-control.v1" as const,
                expected_revision: job.revision,
                observation_consent_version: 1,
              },
              key: makeOperationKey("enable-observation"),
            };
      pendingObservation.current = attempt;
      const result = await client.enableScheduledJobObservation(
        attempt.runID,
        attempt.jobID,
        attempt.body,
        attempt.key,
      );
      if (pendingObservation.current === attempt) pendingObservation.current = null;
      return result;
    },
    onSuccess: (result) => refreshJobs(result.job),
  });

  const bundleMutation = useMutation({
    mutationFn: (job: ScheduledJobView) => client.diagnosticBundle(job.owner_run_id),
    onSuccess: (value, job) => {
      downloadTextFile(
        `cyberagent-diagnostics-${job.owner_run_id}.json`,
        "application/json",
        JSON.stringify(value, null, 2),
      );
    },
  });

  const openFullscreenStudio = (jobToEdit?: ScheduledJobView) => {
    if (jobToEdit) {
      setEditingJob(jobToEdit);
      const target = jobToEdit.spec.execution_target;
      if (target?.kind === "thread") {
        setMode("thread");
        setThreadID(target.thread_id || "");
        setPrompt(target.prompt || "");
      } else if (target?.kind === "workflow") {
        setMode("workflow");
        setWorkflowID(target.workflow_definition_id || "");
        setWorkflowInputs(JSON.stringify(target.inputs ?? {}, null, 2));
      } else {
        setMode("observation");
        setObservationRunID(jobToEdit.owner_run_id);
      }
      setMaxRoundsInput(String(jobToEdit.spec.max_rounds));
      setNotification(jobToEdit.spec.notification);
      if (jobToEdit.spec.schedule.kind === "periodic" && jobToEdit.spec.schedule.interval_seconds) {
        const mins = Math.round(jobToEdit.spec.schedule.interval_seconds / 60);
        if (mins % 10_080 === 0) {
          setScheduleExpr(isZh ? "每周一 10:00" : "every monday at 10:00");
        } else if (mins % 1_440 === 0) {
          const anchorDate = new Date(jobToEdit.spec.schedule.anchor_at);
          const hh = String(anchorDate.getHours()).padStart(2, "0");
          const mm = String(anchorDate.getMinutes()).padStart(2, "0");
          setScheduleExpr(isZh ? `每天 ${hh}:${mm}` : `every day at ${hh}:${mm}`);
        } else if (mins % 60 === 0) {
          setScheduleExpr(isZh ? `每 ${mins / 60} 小时` : `every ${mins / 60} hours`);
        } else {
          setScheduleExpr(isZh ? `每 ${mins} 分钟` : `every ${mins} minutes`);
        }
      }
    } else {
      setEditingJob(null);
    }
    setMarkdownPreview(false);
    setFullscreenOpen(true);
  };

  // Worker status summary
  const worker = healthQuery.data?.scheduled_job_worker;
  const workerLabel = healthQuery.isError
    ? t("调度状态待确认", "Scheduler status unconfirmed")
    : !worker
      ? t("自然语言智能调度台", "Natural-language automation studio")
      : !worker.enabled
        ? t("调度器待启用", "Scheduler awaits enablement")
        : worker.state === "running"
          ? t("调度器运行中", "Scheduler running")
          : worker.state === "ready"
            ? t("调度器正在启动", "Scheduler starting")
            : t("调度器已暂停", "Scheduler paused");

  const statusBadgeCopy = (status: string): { label: string; tone: string } => {
    switch (status) {
      case "active":
        return { label: t("已启用", "Active"), tone: "active" };
      case "paused":
        return { label: t("已暂停", "Paused"), tone: "paused" };
      case "completed":
      case "succeeded":
      case "unchanged":
        return { label: t("成功", "Succeeded"), tone: "succeeded" };
      case "failed":
      case "retry_wait":
        return { label: t("失败", "Failed"), tone: "failed" };
      case "running":
      case "claimed":
        return { label: t("运行中", "Running"), tone: "running" };
      case "queued":
      case "waiting_approval":
      case "blocked":
        return { label: t("等待中", "Queued"), tone: "queued" };
      case "cancelled":
        return { label: t("已取消", "Cancelled"), tone: "paused" };
      case "exhausted":
        return { label: t("已达上限", "Completed"), tone: "succeeded" };
      default:
        return { label: status, tone: "queued" };
    }
  };

  // Filtered automations list
  const normalizedSearch = searchQuery.trim().toLowerCase();
  const filteredJobs = useMemo(() => {
    return jobs.filter((job) => {
      if (jobStatusFilter === "active" && job.status !== "active") return false;
      if (jobStatusFilter === "paused" && job.status !== "paused") return false;
      if (jobStatusFilter === "ended" && !isTerminalStatus(job.status)) return false;
      if (!normalizedSearch) return true;
      const haystack = [
        jobTitle(job),
        job.id,
        job.owner_run_id,
        job.spec.execution_target?.prompt ?? "",
        job.spec.execution_target?.thread_id ?? "",
      ]
        .join(" ")
        .toLowerCase();
      return haystack.includes(normalizedSearch);
    });
  }, [jobs, jobStatusFilter, normalizedSearch, threadItems, workflowDefsQuery.data]);

  // Unified historical run records across loaded details + job snapshots
  const historyRecords = useMemo<NormalizedHistoryRecord[]>(() => {
    const snapshotsByJob = new Map<string, ScheduledJobDetailView["snapshot"]>();
    if (detailQuery.data?.snapshot) {
      snapshotsByJob.set(detailQuery.data.snapshot.job.id, detailQuery.data.snapshot);
    }
    for (const q of historyDetailQueries) {
      if (q.data?.snapshot) {
        snapshotsByJob.set(q.data.snapshot.job.id, q.data.snapshot);
      }
    }

    const records: NormalizedHistoryRecord[] = [];
    for (const job of jobs) {
      const title = jobTitle(job);
      const threadTargetID =
        job.spec.execution_target?.kind === "thread"
          ? job.spec.execution_target.thread_id
          : undefined;
      const snapshot = snapshotsByJob.get(job.id);
      let addedFromSnapshot = false;

      if (snapshot?.executions?.length) {
        for (const exec of snapshot.executions) {
          addedFromSnapshot = true;
          records.push({
            id: `exec:${job.id}:${exec.occurrence_at}:${exec.run_id ?? ""}`,
            job,
            jobTitle: title,
            category: categorizeExecutionState(exec.state, job.status),
            rawState: exec.state,
            timestamp: exec.occurrence_at || exec.updated_at || exec.created_at,
            roundsCompleted: job.rounds_completed,
            maxRounds: job.spec.max_rounds,
            resultText: exec.result,
            errorCode: exec.error_code,
            threadID: threadTargetID,
            runID: exec.run_id || job.owner_run_id,
            workflowExecutionID: exec.workflow_execution_id,
          });
        }
      }

      if (!addedFromSnapshot && snapshot?.rounds?.length) {
        for (const round of snapshot.rounds) {
          addedFromSnapshot = true;
          records.push({
            id: `round:${job.id}:${round.ordinal}:${round.occurrence_at}`,
            job,
            jobTitle: title,
            category: categorizeExecutionState(round.status, job.status),
            rawState: round.status,
            timestamp: round.completed_at || round.started_at || round.occurrence_at,
            roundsCompleted: job.rounds_completed,
            maxRounds: job.spec.max_rounds,
            resultText: round.result,
            errorCode: round.error_code,
            threadID: threadTargetID,
            runID: job.owner_run_id,
          });
        }
      }

      if (
        !addedFromSnapshot &&
        (job.rounds_completed > 0 ||
          job.status === "paused" ||
          job.status === "failed" ||
          Boolean(job.last_result) ||
          Boolean(job.last_error_code))
      ) {
        const syntheticState =
          job.status === "paused"
            ? "paused"
            : job.last_error_code || job.status === "failed"
              ? "failed"
              : "completed";
        records.push({
          id: `job:${job.id}:${job.revision}`,
          job,
          jobTitle: title,
          category: categorizeExecutionState(syntheticState, job.status),
          rawState: syntheticState,
          timestamp: job.completed_at || job.updated_at || job.created_at,
          roundsCompleted: job.rounds_completed,
          maxRounds: job.spec.max_rounds,
          resultText: job.last_result,
          errorCode: job.last_error_code,
          threadID: threadTargetID,
          runID: job.owner_run_id,
        });
      }
    }

    return records.sort(
      (a, b) => new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime(),
    );
  }, [jobs, detailQuery.data, historyDetailQueries, threadItems, workflowDefsQuery.data]);

  const historyCounts = useMemo(() => {
    const counts: Record<HistoryFilter, number> = {
      all: historyRecords.length,
      succeeded: 0,
      failed: 0,
      running: 0,
      queued: 0,
      paused: 0,
    };
    for (const rec of historyRecords) {
      counts[rec.category] += 1;
    }
    return counts;
  }, [historyRecords]);

  const filteredHistoryRecords = useMemo(() => {
    return historyRecords.filter((record) => {
      if (historyFilter !== "all" && record.category !== historyFilter) return false;
      if (!normalizedSearch) return true;
      const haystack = [
        record.jobTitle,
        record.job.id,
        record.resultText ?? "",
        record.errorCode ?? "",
        record.threadID ?? "",
      ]
        .join(" ")
        .toLowerCase();
      return haystack.includes(normalizedSearch);
    });
  }, [historyRecords, historyFilter, normalizedSearch]);

  const handleQuickCreateSubmit = (event: FormEvent) => {
    event.preventDefault();
    setScheduleTouched(true);
    setPromptTouched(true);
    if (!parsedSchedule.ok) {
      quickScheduleInputRef.current?.focus();
      return;
    }
    if (mode === "thread" && !prompt.trim()) return;
    saveAutomationMutation.mutate();
  };

  const activeError =
    saveAutomationMutation.error ??
    transitionMutation.error ??
    runNowMutation.error ??
    enableObservationMutation.error ??
    bundleMutation.error;

  const canControl = Boolean(client.hasScheduledJobControl);
  const canRunNow = Boolean(
    (client.hasThreadControl && typeof client.submitThreadTurn === "function") ||
      (client.hasScheduledJobControl && typeof client.createScheduledJob === "function"),
  );

  const renderNaturalSchedulePreview = (previewID: string) => {
    if (parsedSchedule.ok) {
      return (
        <div className="v2-nl-preview-box" id={previewID}>
          <div className="v2-nl-preview-summary">
            <Sparkles aria-hidden="true" size={14} />
            <strong>{isZh ? parsedSchedule.summary.zh : parsedSchedule.summary.en}</strong>
            <span>
              ·{" "}
              {parsedSchedule.kind === "once"
                ? t("单次触发", "One-time")
                : t(
                    `间隔 ${parsedSchedule.intervalMinutes} 分钟`,
                    `${parsedSchedule.intervalMinutes}m interval`,
                  )}
            </span>
          </div>
          <div>
            <span>{t("未来触发预览：", "Next triggers: ")}</span>
            <ul
              aria-label={t("未来 3 次触发时间预览", "Next 3 scheduled trigger times")}
              className="v2-nl-preview-triggers"
              role="list"
            >
              {parsedSchedule.nextTriggers.map((iso) => (
                <li key={iso}>{formatDateTime(iso, locale)}</li>
              ))}
            </ul>
          </div>
        </div>
      );
    }
    if (!scheduleTouched && !scheduleExpr.trim()) return null;
    return (
      <div className="v2-nl-preview-box is-invalid" id={previewID} role="status">
        <span>{isZh ? parsedSchedule.error.zh : parsedSchedule.error.en}</span>
      </div>
    );
  };

  return (
    <section className="v2-resource-page v2-automations-studio">
      <header className="v2-automations-header">
        <div className="v2-automations-title-group">
          <span aria-hidden="true" className="v2-automations-title-icon">
            <CalendarClock size={19} />
          </span>
          <div className="v2-automations-title-copy">
            <h1>{t("自动化", "Automations")}</h1>
            <p>
              {workerLabel} ·{" "}
              {t(
                "告别繁琐 Cron 表达式，支持自然语言定时、全屏指令编排与历史运行追踪",
                "Zero-Cron natural-language scheduling, fullscreen prompt studio, and execution history",
              )}
            </p>
          </div>
        </div>
        <div className="v2-automations-header-actions">
          <button
            className="v2-automations-btn is-primary"
            onClick={() => openFullscreenStudio()}
            ref={fullscreenTriggerRef}
            type="button"
          >
            <Maximize2 aria-hidden="true" size={14} />
            {t("展开全屏编辑器", "Fullscreen Studio")}
          </button>
          <button
            aria-label={t("刷新自动化", "Refresh automations")}
            className="v2-automations-btn"
            disabled={listQuery.isFetching}
            onClick={() => {
              void listQuery.refetch();
              void healthQuery.refetch();
              if (selectedJobID) void detailQuery.refetch();
            }}
            type="button"
          >
            <RefreshCw
              aria-hidden="true"
              className={listQuery.isFetching ? "spin" : ""}
              size={14}
            />
            {t("刷新", "Refresh")}
          </button>
        </div>
      </header>

      <div className="v2-automations-scroll">
        {/* Quick Natural-Language Schedule Setup Card */}
        <form
          aria-labelledby={`${formUid}-quick-heading`}
          className="v2-automations-quick-card"
          onSubmit={handleQuickCreateSubmit}
        >
          <div className="v2-automations-quick-top">
            <strong id={`${formUid}-quick-heading`}>
              <Plus aria-hidden="true" size={15} />
              {t("自然语言快速新建自动化", "Quick Natural-Language Automation")}
            </strong>
            <span className="v2-automations-zero-cron-badge">
              <Sparkles aria-hidden="true" size={12} />
              {t("免 Cron 表达式 · 实时触发预览", "Zero Cron · Live trigger preview")}
            </span>
          </div>

          <div
            aria-label={t("一键定时预设", "One-click schedule presets")}
            className="v2-nl-preset-row"
            role="group"
          >
            {NATURAL_SCHEDULE_PRESETS.map((preset) => {
              const expr = isZh ? preset.expression.zh : preset.expression.en;
              const pressed = scheduleExpr.trim().toLowerCase() === expr.toLowerCase();
              return (
                <button
                  aria-pressed={pressed}
                  className="v2-nl-preset-chip"
                  key={preset.id}
                  onClick={() => {
                    setScheduleExpr(expr);
                    setScheduleTouched(false);
                  }}
                  type="button"
                >
                  <Clock aria-hidden="true" size={12} />
                  {isZh ? preset.label.zh : preset.label.en}
                </button>
              );
            })}
          </div>

          <div className="v2-automations-quick-grid">
            <div className="v2-automations-field">
              <label htmlFor={`${formUid}-mode`}>{t("自动化类型", "Automation mode")}</label>
              <select
                id={`${formUid}-mode`}
                onChange={(event) => {
                  setMode(event.target.value as AutomationMode);
                  saveAutomationMutation.reset();
                }}
                value={mode}
              >
                <option value="thread">{t("定时执行会话指令", "Scheduled task prompt")}</option>
                <option value="workflow">{t("定时运行工作流", "Scheduled workflow")}</option>
                <option value="pr">{t("PR 与 CI 持续跟进", "PR & CI follow-up")}</option>
                <option value="observation">{t("只读状态巡检", "Read-only observation")}</option>
              </select>
            </div>

            <div className="v2-automations-field">
              <label htmlFor={`${formUid}-thread`}>{t("关联会话", "Target thread")}</label>
              <select
                id={`${formUid}-thread`}
                onChange={(event) => {
                  initialTargetResolved.current = true;
                  setThreadID(event.target.value);
                }}
                value={threadID}
              >
                <option value="">
                  {threadsQuery.isLoading
                    ? t("正在读取会话…", "Loading threads…")
                    : t("选择一个已有会话", "Select an existing thread")}
                </option>
                {initialThreadID && !threadItems.some((item) => item.id === initialThreadID) && (
                  <option value={initialThreadID}>{targetThread?.title || initialThreadID}</option>
                )}
                {threadItems.map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.title || item.id}
                  </option>
                ))}
              </select>
            </div>

            {mode !== "pr" && (
              <div className="v2-automations-field">
                <label htmlFor={`${formUid}-nl-schedule`}>
                  {t("自然语言定时设置", "Natural-language schedule")}
                </label>
                <input
                  aria-describedby={`${formUid}-nl-preview`}
                  aria-invalid={scheduleTouched && !parsedSchedule.ok ? true : undefined}
                  id={`${formUid}-nl-schedule`}
                  onBlur={() => setScheduleTouched(true)}
                  onChange={(event) => {
                    setScheduleExpr(event.target.value);
                    if (scheduleTouched) setScheduleTouched(false);
                  }}
                  placeholder={t(
                    "例如：每天早上 9 点、工作日早上 9:30、每周一 10:00、每 30 分钟",
                    "e.g. every day at 9am, weekdays at 6pm, every monday at 10:00, every 45 minutes",
                  )}
                  ref={quickScheduleInputRef}
                  type="text"
                  value={scheduleExpr}
                />
              </div>
            )}

            {mode === "thread" && (
              <div className="v2-automations-field is-wide">
                <label htmlFor={`${formUid}-quick-prompt`}>
                  {t("自动化执行指令", "Automation instructions")}
                </label>
                <textarea
                  aria-invalid={promptTouched && !prompt.trim() ? true : undefined}
                  id={`${formUid}-quick-prompt`}
                  onBlur={() => setPromptTouched(true)}
                  onChange={(event) => {
                    setPrompt(event.target.value);
                    if (promptTouched && event.target.value.trim()) setPromptTouched(false);
                  }}
                  placeholder={t(
                    "描述每次定时触发时希望智能体执行的检查、汇总或修复任务…（可展开全屏编辑器使用模板与 Markdown 预览）",
                    "Describe what the agent should inspect, summarize, or fix on each trigger…",
                  )}
                  rows={2}
                  value={prompt}
                />
              </div>
            )}

            {mode === "workflow" && (
              <>
                <div className="v2-automations-field">
                  <label htmlFor={`${formUid}-workflow`}>{t("已保存工作流", "Saved workflow")}</label>
                  <select
                    id={`${formUid}-workflow`}
                    onChange={(event) => setWorkflowID(event.target.value)}
                    value={workflowID}
                  >
                    <option value="">{t("选择工作流", "Select a workflow")}</option>
                    {workflowDefsQuery.data?.map((item) => (
                      <option key={item.id} value={item.id}>
                        {item.spec.name}
                      </option>
                    ))}
                  </select>
                </div>
                <div className="v2-automations-field is-wide">
                  <label htmlFor={`${formUid}-workflow-inputs`}>
                    {t("工作流输入（JSON）", "Workflow inputs (JSON)")}
                  </label>
                  <textarea
                    autoCapitalize="none"
                    autoCorrect="off"
                    id={`${formUid}-workflow-inputs`}
                    onChange={(event) => setWorkflowInputs(event.target.value)}
                    rows={2}
                    spellCheck={false}
                    value={workflowInputs}
                  />
                  {threadID && ownerRun && ownerRun.status !== "running" && (
                    <small>{t("提示：工作流需绑定正在运行的任务。", "Note: workflows require an active running task.")}</small>
                  )}
                </div>
              </>
            )}

            {mode === "observation" && !threadID && (
              <div className="v2-automations-field is-wide">
                <label htmlFor={`${formUid}-obs-run`}>
                  {t("目标 Run ID（或在上方选择关联会话）", "Target Run ID (or choose a thread above)")}
                </label>
                <input
                  autoCapitalize="none"
                  autoCorrect="off"
                  id={`${formUid}-obs-run`}
                  onChange={(event) => setObservationRunID(event.target.value)}
                  placeholder="run-…"
                  spellCheck={false}
                  type="text"
                  value={observationRunID}
                />
              </div>
            )}
          </div>

          {mode === "pr" ? (
            threadID ? (
              <AutomationPRFollowup client={client} key={threadID} threadID={threadID} />
            ) : (
              <p className="v2-automations-status-banner">
                {t("请先选择一个关联会话以配置 PR 与 CI 持续跟进。", "Select a target thread first to configure PR & CI follow-up.")}
              </p>
            )
          ) : (
            <>
              {renderNaturalSchedulePreview(`${formUid}-nl-preview`)}
              <div className="v2-automations-quick-footer">
                <div>
                  {!canControl && (
                    <small>
                      {t(
                        "连接控制凭证并开启定时执行权限后即可保存与启停自动化。",
                        "Connect a control credential with scheduled job permissions to create or pause automations.",
                      )}
                    </small>
                  )}
                </div>
                <div className="v2-automations-header-actions">
                  <button
                    className="v2-automations-btn"
                    onClick={() => openFullscreenStudio()}
                    type="button"
                  >
                    <Maximize2 aria-hidden="true" size={14} />
                    {t("使用全屏编辑器配置", "Configure in Fullscreen Studio")}
                  </button>
                  <button
                    className="v2-automations-btn is-primary"
                    disabled={
                      !canControl ||
                      saveAutomationMutation.isPending ||
                      !parsedSchedule.ok ||
                      !runID ||
                      (mode === "thread" && !prompt.trim())
                    }
                    type="submit"
                  >
                    {saveAutomationMutation.isPending ? (
                      <LoaderCircle aria-hidden="true" className="spin" size={14} />
                    ) : (
                      <Plus aria-hidden="true" size={14} />
                    )}
                    {t("创建自动化", "Create automation")}
                  </button>
                </div>
              </div>
            </>
          )}
        </form>

        {actionNotice && (
          <p className="v2-automations-status-banner" role="status">
            {actionNotice}
          </p>
        )}
        {activeError && (
          <p className="v2-automations-error-box" role="alert">
            {activeError instanceof Error
              ? activeError.message
              : t("自动化操作未能完成，请重试。", "Automation action failed. Please try again.")}
          </p>
        )}

        {/* View Switcher & Search */}
        <div className="v2-automations-tabs-bar">
          <div
            aria-label={t("自动化工作台视图", "Automations studio views")}
            className="v2-automations-tabs"
            role="group"
          >
            <button
              aria-pressed={activeTab === "schedules"}
              onClick={() => setActiveTab("schedules")}
              type="button"
            >
              <CalendarClock aria-hidden="true" size={14} />
              {t("计划列表", "Schedules")} ({jobs.length})
            </button>
            <button
              aria-pressed={activeTab === "history"}
              onClick={() => setActiveTab("history")}
              type="button"
            >
              <History aria-hidden="true" size={14} />
              {t("历史运行记录", "Run History")} ({historyRecords.length})
            </button>
          </div>

          <div aria-label={t("筛选自动化与运行记录", "Filter automations and run records")} role="search">
            <label className="v2-automations-search" htmlFor={`${formUid}-search`}>
              <Search aria-hidden="true" size={14} />
              <input
                autoCapitalize="none"
                autoCorrect="off"
                id={`${formUid}-search`}
                onChange={(event) => setSearchQuery(event.target.value)}
                placeholder={t("搜索计划名称、ID 或错误码…", "Search schedules, IDs, or errors…")}
                spellCheck={false}
                type="search"
                value={searchQuery}
              />
            </label>
          </div>
        </div>

        {/* Tab 1: Automations Master-Detail */}
        {activeTab === "schedules" ? (
          <>
            <div
              aria-label={t("计划状态筛选", "Filter schedules by status")}
              className="v2-automations-filter-row"
              role="group"
            >
              {(
                [
                  ["all", t("全部计划", "All"), jobs.length],
                  ["active", t("已启用", "Active"), jobs.filter((j) => j.status === "active").length],
                  ["paused", t("已暂停", "Paused"), jobs.filter((j) => j.status === "paused").length],
                  ["ended", t("已结束", "Ended"), jobs.filter((j) => isTerminalStatus(j.status)).length],
                ] as const
              ).map(([key, label, count]) => (
                <button
                  aria-pressed={jobStatusFilter === key}
                  className="v2-automations-filter-pill"
                  key={key}
                  onClick={() => setJobStatusFilter(key)}
                  type="button"
                >
                  {label} ({count})
                </button>
              ))}
            </div>

            {filteredJobs.length === 0 ? (
              <div className="v2-automations-empty">
                <CalendarClock aria-hidden="true" size={28} />
                <strong>
                  {jobs.length === 0
                    ? t("还没有定时自动化计划", "No scheduled automations yet")
                    : t("没有匹配的自动化计划", "No matching automations")}
                </strong>
                <p>
                  {t(
                    "可直接点击上方自然语言预设（如“每天早报 09:00”）或展开全屏编辑器创建。",
                    "Click a natural-language preset above or open Fullscreen Studio to create your first schedule.",
                  )}
                </p>
              </div>
            ) : (
              <div className="v2-automations-master-detail">
                <div
                  aria-label={t("自动化列表", "Automation list")}
                  className="v2-automations-job-list"
                >
                  {filteredJobs.map((job) => {
                    const badge = statusBadgeCopy(job.status);
                    return (
                      <button
                        className={`v2-automations-job-card${job.id === selectedJob?.id ? " is-selected" : ""}`}
                        key={job.id}
                        onClick={() => setSelectedJobID(job.id)}
                        type="button"
                      >
                        <div className="v2-automations-job-card-head">
                          <strong>{jobTitle(job)}</strong>
                          <span className={`v2-automation-status-badge tone-${badge.tone}`}>
                            {badge.label}
                          </span>
                        </div>
                        <div className="v2-automations-job-meta">
                          {describeExistingSchedule(job.spec.schedule, locale, t)}
                        </div>
                        <div className="v2-automations-job-meta">
                          {t("下次触发：", "Next run: ")}
                          {formatDateTime(job.next_wake_at, locale)} ·{" "}
                          {t(
                            `已运行 ${job.rounds_completed}/${job.spec.max_rounds} 次`,
                            `Runs ${job.rounds_completed}/${job.spec.max_rounds}`,
                          )}
                        </div>
                      </button>
                    );
                  })}
                  {listQuery.hasNextPage && (
                    <button
                      className="v2-automations-btn"
                      disabled={listQuery.isFetchingNextPage}
                      onClick={() => void listQuery.fetchNextPage()}
                      type="button"
                    >
                      {listQuery.isFetchingNextPage
                        ? t("正在加载更多…", "Loading more…")
                        : t("加载更多计划", "Load more schedules")}
                    </button>
                  )}
                </div>

                {selectedJob && (
                  <div className="v2-automations-detail-pane">
                    <div className="v2-automations-detail-head">
                      <div>
                        <h2>{jobTitle(selectedJob)}</h2>
                        <small>{selectedJob.id}</small>
                      </div>
                      <span
                        className={`v2-automation-status-badge tone-${statusBadgeCopy(selectedJob.status).tone}`}
                      >
                        {statusBadgeCopy(selectedJob.status).label}
                      </span>
                    </div>

                    <dl className="v2-automations-facts">
                      <div>
                        <dt>{t("定时规则", "Schedule")}</dt>
                        <dd>{describeExistingSchedule(selectedJob.spec.schedule, locale, t)}</dd>
                      </div>
                      <div>
                        <dt>{t("下次触发时间", "Next trigger")}</dt>
                        <dd>{formatDateTime(selectedJob.next_wake_at, locale)}</dd>
                      </div>
                      <div>
                        <dt>{t("历史执行次数", "Execution count")}</dt>
                        <dd>
                          {selectedJob.rounds_completed} / {selectedJob.spec.max_rounds}
                        </dd>
                      </div>
                      <div>
                        <dt>{t("最近运行状态", "Latest result")}</dt>
                        <dd>
                          {selectedJob.last_result ||
                            selectedJob.last_error_code ||
                            t("等待首次触发", "Awaiting first run")}
                        </dd>
                      </div>
                    </dl>

                    {selectedJob.spec.execution_target?.prompt && (
                      <div className="v2-automations-prompt-box">
                        <ReactMarkdown remarkPlugins={[remarkGfm]}>
                          {selectedJob.spec.execution_target.prompt}
                        </ReactMarkdown>
                      </div>
                    )}

                    {selectedJob.last_error_code && (
                      <p className="v2-automations-error-box">
                        {t("最近错误诊断：", "Latest error code: ")}
                        <code>{selectedJob.last_error_code}</code>
                      </p>
                    )}

                    <div className="v2-automations-actions-bar">
                      <button
                        className="v2-automations-btn is-primary"
                        disabled={!canRunNow || runNowMutation.isPending}
                        onClick={() => runNowMutation.mutate(selectedJob)}
                        type="button"
                      >
                        <Zap aria-hidden="true" size={14} />
                        {t("立即执行一次", "Run now")}
                      </button>

                      {selectedJob.status === "active" && (
                        <button
                          className="v2-automations-btn"
                          disabled={!canControl || transitionMutation.isPending}
                          onClick={() =>
                            transitionMutation.mutate({ job: selectedJob, action: "pause" })
                          }
                          type="button"
                        >
                          <Pause aria-hidden="true" size={14} />
                          {t("暂停", "Pause")}
                        </button>
                      )}

                      {selectedJob.status === "paused" && (
                        <button
                          className="v2-automations-btn"
                          disabled={!canControl || transitionMutation.isPending}
                          onClick={() =>
                            transitionMutation.mutate({ job: selectedJob, action: "resume" })
                          }
                          type="button"
                        >
                          <Play aria-hidden="true" size={14} />
                          {t("恢复", "Resume")}
                        </button>
                      )}

                      <button
                        className="v2-automations-btn"
                        onClick={() => openFullscreenStudio(selectedJob)}
                        type="button"
                      >
                        <Pencil aria-hidden="true" size={14} />
                        {t("全屏编辑", "Edit in Fullscreen")}
                      </button>

                      {selectedJob.observation_consent_version !== 1 &&
                        selectedJob.spec.execution_mode === "read_only" &&
                        selectedJob.spec.max_model_calls === 0 &&
                        ["active", "paused"].includes(selectedJob.status) && (
                          <button
                            className="v2-automations-btn"
                            disabled={!canControl || enableObservationMutation.isPending}
                            onClick={() => enableObservationMutation.mutate(selectedJob)}
                            type="button"
                          >
                            <CheckCircle2 aria-hidden="true" size={14} />
                            {t("确认自动观察", "Confirm observation")}
                          </button>
                        )}

                      {!isTerminalStatus(selectedJob.status) && (
                        <button
                          className="v2-automations-btn is-danger"
                          disabled={!canControl || transitionMutation.isPending}
                          onClick={() =>
                            transitionMutation.mutate({ job: selectedJob, action: "cancel" })
                          }
                          type="button"
                        >
                          <Square aria-hidden="true" size={13} />
                          {t("取消计划", "Cancel")}
                        </button>
                      )}

                      {typeof client.diagnosticBundle === "function" && (
                        <button
                          className="v2-automations-btn"
                          disabled={bundleMutation.isPending}
                          onClick={() => bundleMutation.mutate(selectedJob)}
                          type="button"
                        >
                          <Download aria-hidden="true" size={14} />
                          {t("导出诊断包", "Export diagnostics")}
                        </button>
                      )}

                      {selectedJob.spec.execution_target?.kind === "thread" &&
                        selectedJob.spec.execution_target.thread_id && (
                          <a
                            className="v2-automations-link"
                            href={`#/threads/${encodeURIComponent(selectedJob.spec.execution_target.thread_id)}`}
                          >
                            <ExternalLink aria-hidden="true" size={13} />
                            {t("查看关联会话", "Open thread")}
                          </a>
                        )}
                    </div>

                    {/* Per-automation execution timeline */}
                    <section aria-label={t("单计划最近执行时间线", "Recent execution timeline")}>
                      <strong>{t("最近运行记录", "Recent executions")}</strong>
                      {detailQuery.data?.snapshot.executions?.length ? (
                        <div className="v2-automations-history-list" style={{ marginTop: 10 }}>
                          {detailQuery.data.snapshot.executions.map((item) => {
                            const badge = statusBadgeCopy(item.state);
                            return (
                              <article
                                className="v2-automations-history-card"
                                key={`${item.job_id}:${item.occurrence_at}`}
                              >
                                <div className="v2-automations-history-top">
                                  <div className="v2-automations-history-title">
                                    <span className={`v2-automation-status-badge tone-${badge.tone}`}>
                                      {badge.label}
                                    </span>
                                    <small>{formatDateTime(item.occurrence_at, locale)}</small>
                                  </div>
                                  {item.run_id && (
                                    <a
                                      className="v2-automations-link"
                                      href={`#/runs/${encodeURIComponent(item.run_id)}`}
                                    >
                                      <ExternalLink aria-hidden="true" size={13} />
                                      {t("查看运行记录", "View run")}
                                    </a>
                                  )}
                                </div>
                                {item.result && <p style={{ margin: 0, fontSize: 13 }}>{item.result}</p>}
                                {item.error_code && (
                                  <p className="v2-automations-error-box">
                                    {t("错误码：", "Error: ")}
                                    <code>{item.error_code}</code>
                                  </p>
                                )}
                              </article>
                            );
                          })}
                        </div>
                      ) : (
                        <p className="v2-automations-job-meta" style={{ marginTop: 6 }}>
                          {t("此计划暂无详细历史触发记录。", "No execution occurrences recorded yet for this schedule.")}
                        </p>
                      )}
                    </section>
                  </div>
                )}
              </div>
            )}
          </>
        ) : (
          /* Tab 2: Historical Run Records */
          <section aria-label={t("历史运行记录面板", "Historical run records")}>
            <div
              aria-label={t("按运行状态筛选", "Filter by execution status")}
              className="v2-automations-filter-row"
              role="group"
              style={{ marginBottom: 14 }}
            >
              {(
                [
                  ["all", t("全部状态", "All"), historyCounts.all],
                  ["succeeded", t("成功", "Succeeded"), historyCounts.succeeded],
                  ["failed", t("失败", "Failed"), historyCounts.failed],
                  ["running", t("运行中", "Running"), historyCounts.running],
                  ["queued", t("等待中", "Queued"), historyCounts.queued],
                  ["paused", t("已暂停", "Paused"), historyCounts.paused],
                ] as const
              ).map(([filterKey, label, count]) => (
                <button
                  aria-pressed={historyFilter === filterKey}
                  className="v2-automations-filter-pill"
                  key={filterKey}
                  onClick={() => setHistoryFilter(filterKey)}
                  type="button"
                >
                  {label} ({count})
                </button>
              ))}
            </div>

            {filteredHistoryRecords.length === 0 ? (
              <div className="v2-automations-empty">
                <History aria-hidden="true" size={28} />
                <strong>{t("暂无匹配的历史运行记录", "No matching historical run records")}</strong>
                <p>
                  {t(
                    "自动化触发后，这里将汇总每次运行状态、耗时节点、错误诊断与关联会话入口。",
                    "Once automations run, every trigger status, diagnostic code, and linked thread appears here.",
                  )}
                </p>
              </div>
            ) : (
              <div className="v2-automations-history-list">
                {filteredHistoryRecords.map((record) => {
                  const badge = statusBadgeCopy(record.category);
                  return (
                    <article className="v2-automations-history-card" key={record.id}>
                      <div className="v2-automations-history-top">
                        <div className="v2-automations-history-title">
                          <strong>{record.jobTitle}</strong>
                          <span className={`v2-automation-status-badge tone-${badge.tone}`}>
                            {badge.label}
                          </span>
                        </div>
                        <div className="v2-automations-actions-bar">
                          {record.threadID && (
                            <a
                              className="v2-automations-link"
                              href={`#/threads/${encodeURIComponent(record.threadID)}`}
                            >
                              <ExternalLink aria-hidden="true" size={13} />
                              {t("查看关联会话", "Open thread")}
                            </a>
                          )}
                          {record.runID && (
                            <a
                              className="v2-automations-link"
                              href={`#/runs/${encodeURIComponent(record.runID)}`}
                            >
                              <ExternalLink aria-hidden="true" size={13} />
                              {t("查看运行记录", "View run")}
                            </a>
                          )}
                          <button
                            className="v2-automations-btn"
                            disabled={!canRunNow || runNowMutation.isPending}
                            onClick={() => runNowMutation.mutate(record.job)}
                            type="button"
                          >
                            <Zap aria-hidden="true" size={13} />
                            {t("立即执行一次", "Run now")}
                          </button>
                          {record.job.status === "active" && (
                            <button
                              className="v2-automations-btn"
                              disabled={!canControl || transitionMutation.isPending}
                              onClick={() =>
                                transitionMutation.mutate({ job: record.job, action: "pause" })
                              }
                              type="button"
                            >
                              <Pause aria-hidden="true" size={13} />
                              {t("暂停", "Pause")}
                            </button>
                          )}
                          {record.job.status === "paused" && (
                            <button
                              className="v2-automations-btn"
                              disabled={!canControl || transitionMutation.isPending}
                              onClick={() =>
                                transitionMutation.mutate({ job: record.job, action: "resume" })
                              }
                              type="button"
                            >
                              <Play aria-hidden="true" size={13} />
                              {t("恢复", "Resume")}
                            </button>
                          )}
                        </div>
                      </div>

                      <div className="v2-automations-history-meta">
                        <span>
                          {t("触发时间：", "Triggered: ")}
                          {formatDateTime(record.timestamp, locale)}
                        </span>
                        <span>
                          {t("累计运行：", "Run count: ")}
                          {record.roundsCompleted} / {record.maxRounds}
                        </span>
                        <span>ID: {record.job.id}</span>
                      </div>

                      {record.resultText && (
                        <p style={{ margin: 0, fontSize: 13 }}>{record.resultText}</p>
                      )}
                      {record.errorCode && (
                        <p className="v2-automations-error-box">
                          {t("错误诊断：", "Error diagnostic: ")}
                          <code>{record.errorCode}</code>
                        </p>
                      )}
                    </article>
                  );
                })}
              </div>
            )}
          </section>
        )}
      </div>

      {/* Full-Screen Automation Studio Modal */}
      {fullscreenOpen &&
        createPortal(
          <div
            className="v2-automation-studio-overlay"
            onMouseDown={(event) => {
              if (event.target === event.currentTarget && !saveAutomationMutation.isPending) {
                setFullscreenOpen(false);
                setEditingJob(null);
              }
            }}
            role="presentation"
          >
            <section
              aria-labelledby={`${formUid}-studio-title`}
              aria-modal="true"
              className="v2-automation-studio-dialog"
              ref={studioDialogRef}
              role="dialog"
              tabIndex={-1}
            >
              <header className="v2-automation-studio-header">
                <div className="v2-automations-title-group">
                  <span aria-hidden="true" className="v2-automations-title-icon">
                    <Sparkles size={18} />
                  </span>
                  <div>
                    <h2 id={`${formUid}-studio-title`}>
                      {editingJob
                        ? t("全屏编辑自动化计划", "Edit Automation in Fullscreen Studio")
                        : t("全屏自动化编排工作台", "Fullscreen Automation Studio")}
                    </h2>
                  </div>
                </div>
                <button
                  aria-label={t("关闭全屏编辑器", "Close Fullscreen Studio")}
                  className="v2-automations-btn"
                  disabled={saveAutomationMutation.isPending}
                  onClick={() => {
                    setFullscreenOpen(false);
                    setEditingJob(null);
                  }}
                  type="button"
                >
                  <X aria-hidden="true" size={15} />
                  {t("收起全屏", "Exit fullscreen")}
                </button>
              </header>

              <div className="v2-automation-studio-body">
                {/* Main Instruction Editor Canvas */}
                <div className="v2-automation-studio-canvas">
                  <div className="v2-automations-field">
                    <label htmlFor={`${formUid}-studio-name`}>
                      {t("自动化标题（可选）", "Automation title (optional)")}
                    </label>
                    <input
                      id={`${formUid}-studio-name`}
                      onChange={(event) => setStudioTitle(event.target.value)}
                      placeholder={t(
                        "例如：每日主干构建与单测回归巡检",
                        "e.g. Daily main-branch build & unit test check",
                      )}
                      type="text"
                      value={studioTitle}
                    />
                  </div>

                  <div>
                    <span className="v2-automations-job-meta">
                      {t("一键套用常用指令模板：", "Reusable prompt templates:")}
                    </span>
                    <div className="v2-automation-template-bar" style={{ marginTop: 6 }}>
                      {AUTOMATION_PROMPT_TEMPLATES.map((tpl) => (
                        <button
                          className="v2-nl-preset-chip"
                          key={tpl.id}
                          onClick={() => {
                            setStudioTitle(isZh ? tpl.title.zh : tpl.title.en);
                            setPrompt(isZh ? tpl.prompt.zh : tpl.prompt.en);
                            setScheduleExpr(isZh ? tpl.schedule.zh : tpl.schedule.en);
                            setMarkdownPreview(false);
                          }}
                          type="button"
                        >
                          <FileCode2 aria-hidden="true" size={12} />
                          {isZh ? tpl.title.zh : tpl.title.en}
                        </button>
                      ))}
                    </div>
                  </div>

                  <div className="v2-automation-editor-toolbar">
                    <label htmlFor={`${formUid}-fullscreen-prompt`}>
                      <strong>{t("自动化 Prompt 指令画布", "Automation Prompt Canvas")}</strong>
                    </label>
                    <div
                      aria-label={t("编辑器预览切换", "Editor view mode")}
                      className="v2-automations-tabs"
                      role="group"
                    >
                      <button
                        aria-pressed={!markdownPreview}
                        onClick={() => setMarkdownPreview(false)}
                        type="button"
                      >
                        <Pencil aria-hidden="true" size={13} />
                        {t("编写 Markdown", "Write")}
                      </button>
                      <button
                        aria-pressed={markdownPreview}
                        onClick={() => setMarkdownPreview(true)}
                        type="button"
                      >
                        <Eye aria-hidden="true" size={13} />
                        {t("实时预览", "Live Preview")}
                      </button>
                    </div>
                  </div>

                  {markdownPreview ? (
                    <div
                      aria-label={t("Markdown 预览内容", "Markdown preview")}
                      className="v2-automation-markdown-preview"
                    >
                      {prompt.trim() ? (
                        <ReactMarkdown remarkPlugins={[remarkGfm]}>{prompt}</ReactMarkdown>
                      ) : (
                        <p className="v2-automations-job-meta">
                          {t("暂无指令内容，请切换回编写模式输入或选择上方模板。", "Nothing to preview yet.")}
                        </p>
                      )}
                    </div>
                  ) : (
                    <textarea
                      className="v2-automation-fullscreen-textarea"
                      id={`${formUid}-fullscreen-prompt`}
                      onChange={(event) => setPrompt(event.target.value)}
                      placeholder={t(
                        "支持完整 Markdown 语法。详细写明触发时的检查步骤、工具使用偏好与汇报格式…",
                        "Full Markdown supported. Describe inspection steps, tool preferences, and output structure…",
                      )}
                      ref={fullscreenPromptRef}
                      value={prompt}
                    />
                  )}
                </div>

                {/* Right Schedule & Target Configuration Rail */}
                <aside
                  aria-label={t("定时与运行环境配置", "Schedule and execution settings")}
                  className="v2-automation-studio-rail"
                >
                  <div className="v2-automations-field">
                    <label htmlFor={`${formUid}-studio-schedule`}>
                      {t("自然语言定时规则（免 Cron）", "Natural-language schedule (Zero Cron)")}
                    </label>
                    <input
                      aria-describedby={`${formUid}-studio-nl-preview`}
                      id={`${formUid}-studio-schedule`}
                      onBlur={() => setScheduleTouched(true)}
                      onChange={(event) => setScheduleExpr(event.target.value)}
                      placeholder={t("如：每天早上 9 点、每周一 10:00", "e.g. every day at 9am")}
                      type="text"
                      value={scheduleExpr}
                    />
                  </div>

                  <div className="v2-nl-preset-row">
                    {NATURAL_SCHEDULE_PRESETS.map((preset) => {
                      const expr = isZh ? preset.expression.zh : preset.expression.en;
                      return (
                        <button
                          aria-pressed={scheduleExpr.trim().toLowerCase() === expr.toLowerCase()}
                          className="v2-nl-preset-chip"
                          key={preset.id}
                          onClick={() => setScheduleExpr(expr)}
                          type="button"
                        >
                          {isZh ? preset.label.zh : preset.label.en}
                        </button>
                      );
                    })}
                  </div>

                  {renderNaturalSchedulePreview(`${formUid}-studio-nl-preview`)}

                  <div className="v2-automations-field">
                    <label htmlFor={`${formUid}-studio-thread`}>
                      {t("绑定已有会话", "Bind existing thread")}
                    </label>
                    <select
                      id={`${formUid}-studio-thread`}
                      onChange={(event) => setThreadID(event.target.value)}
                      value={threadID}
                    >
                      <option value="">
                        {t("不绑定已有会话（在下方选择工作区新建）", "Create new thread in selected workspace")}
                      </option>
                      {threadItems.map((item) => (
                        <option key={item.id} value={item.id}>
                          {item.title || item.id}
                        </option>
                      ))}
                    </select>
                  </div>

                  {!threadID && (
                    <div className="v2-automations-field">
                      <label htmlFor={`${formUid}-studio-workspace`}>
                        {t("目标项目工作区", "Target workspace")}
                      </label>
                      <select
                        id={`${formUid}-studio-workspace`}
                        onChange={(event) => setStudioWorkspaceID(event.target.value)}
                        value={studioWorkspaceID}
                      >
                        <option value="">{t("选择工作区", "Select workspace")}</option>
                        {workspaces.map((ws) => (
                          <option key={ws.id} value={ws.id}>
                            {ws.name}
                          </option>
                        ))}
                      </select>
                    </div>
                  )}

                  <div className="v2-automations-field">
                    <label htmlFor={`${formUid}-studio-model`}>
                      {t("模型路由", "Model route")}
                    </label>
                    <select
                      id={`${formUid}-studio-model`}
                      onChange={(event) => setSelectedModelKey(event.target.value)}
                      value={selectedModelKey}
                    >
                      <option value="">{t("跟随工作区默认模型", "Workspace default model")}</option>
                      {selectableRoutes.map((route) => {
                        const key = `${route.provider_id}::${route.model}`;
                        return (
                          <option key={key} value={key}>
                            {route.provider_name || route.provider_id} · {route.model}
                          </option>
                        );
                      })}
                    </select>
                  </div>

                  <div className="v2-automations-field">
                    <label htmlFor={`${formUid}-studio-approval`}>
                      {t("审批策略", "Approval policy")}
                    </label>
                    <select
                      id={`${formUid}-studio-approval`}
                      onChange={(event) => {
                        setApprovalPolicy(event.target.value as ApprovalPolicyChoice);
                        setApprovalTouched(true);
                      }}
                      value={approvalPolicy}
                    >
                      <option value="ask">{t("Ask · 敏感操作请求审批", "Ask · Request approval")}</option>
                      <option value="auto">{t("Auto · 自动执行受控操作", "Auto · Approve safe actions")}</option>
                      <option value="full">{t("Full · 完全自主访问", "Full · Full access")}</option>
                    </select>
                  </div>

                  <div className="v2-automations-quick-grid">
                    <div className="v2-automations-field">
                      <label htmlFor={`${formUid}-studio-rounds`}>
                        {t("最多执行次数", "Max executions")}
                      </label>
                      <input
                        id={`${formUid}-studio-rounds`}
                        max={1000}
                        min={1}
                        onChange={(event) => setMaxRoundsInput(event.target.value)}
                        type="number"
                        value={maxRoundsInput}
                      />
                    </div>
                    <div className="v2-automations-field">
                      <label htmlFor={`${formUid}-studio-notify`}>
                        {t("通知提醒", "Notification policy")}
                      </label>
                      <select
                        id={`${formUid}-studio-notify`}
                        onChange={(event) =>
                          setNotification(
                            event.target.value as ScheduledJobCreateRequestView["notification"],
                          )
                        }
                        value={notification}
                      >
                        <option value="on_change">{t("结果变化时", "On change")}</option>
                        <option value="on_failure">{t("仅失败时", "On failure")}</option>
                        <option value="all">{t("每次执行后", "Every run")}</option>
                        <option value="silent">{t("静默记录", "Silent")}</option>
                      </select>
                    </div>
                  </div>
                </aside>
              </div>

              <footer className="v2-automation-studio-footer">
                <div className="v2-automations-job-meta">
                  {parsedSchedule.ok
                    ? `${isZh ? parsedSchedule.summary.zh : parsedSchedule.summary.en} · ${t("下次触发", "Next")}: ${formatDateTime(parsedSchedule.anchorAt, locale)}`
                    : t("请完善自然语言定时规则后再保存", "Enter a valid natural-language schedule to continue")}
                </div>
                <div className="v2-automations-header-actions">
                  <button
                    className="v2-automations-btn"
                    disabled={saveAutomationMutation.isPending}
                    onClick={() => {
                      setFullscreenOpen(false);
                      setEditingJob(null);
                    }}
                    type="button"
                  >
                    {t("取消", "Cancel")}
                  </button>
                  <button
                    className="v2-automations-btn is-primary"
                    disabled={
                      !canControl ||
                      saveAutomationMutation.isPending ||
                      !parsedSchedule.ok ||
                      (!runID && !studioWorkspaceID && !editingJob) ||
                      (mode === "thread" && !prompt.trim())
                    }
                    onClick={() => saveAutomationMutation.mutate()}
                    type="button"
                  >
                    {saveAutomationMutation.isPending ? (
                      <LoaderCircle aria-hidden="true" className="spin" size={14} />
                    ) : (
                      <CheckCircle2 aria-hidden="true" size={14} />
                    )}
                    {editingJob
                      ? t("保存并更新自动化", "Save & update automation")
                      : t("保存并启用自动化", "Save & enable automation")}
                  </button>
                </div>
              </footer>
            </section>
          </div>,
          document.body,
        )}
    </section>
  );
}
