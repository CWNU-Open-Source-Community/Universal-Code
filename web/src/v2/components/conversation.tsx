import { lazy, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type RefObject } from "react";
import { createPortal } from "react-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Archive, BookOpen, ChevronDown, CircleEllipsis, FileDiff, Folder, FolderTree, LoaderCircle, MessagesSquare, Microscope, ShieldCheck, PanelTop, Square, SquareTerminal, X } from "lucide-react";
import { APIRequestError, type APIClient } from "../../api/client";
import type { ThreadDetailView, ThreadView, WorkspaceView } from "../../api/types";
import { readThreadReview } from "../../api/task-delivery";
import { usePublicModelStream } from "../../hooks/use-public-model-stream";
import { useRunEventStream } from "../../hooks/use-run-event-stream";
import { threadActivityLabel } from "../../lib/thread-activity-label";
import { prepareThreadNarrative, projectLiveThreadNarrative, type NarrativeEntry } from "../projection/narrative";
import { useV2ThreadTranscript } from "../use-thread-transcript";
import { V2Narrative } from "./narrative";
import { V2LazySurface } from "./lazy-surface";
import { projectAgentActivity } from "../projection/agent-activity";
import { V2AgentActivity } from "./agent-activity";
import { narrativeRepresentsFailedSubmission, recoveryRepresentsNotice } from "../projection/failure-feedback";
import { v2QueryKeys } from "../query-keys";
import { ControlledCommandProposalPanel } from "../../components/controlled-command-proposal-panel";
import { HostCommandProposalPanel } from "../../components/host-command-proposal-panel";
import { v2FileReferenceKey, type V2FileReference } from "./file-context";
import { imageIdentities, type WorkspaceImageAttachment } from "../../api/image-attachments";
import { fileAttachmentIdentities, type WorkspaceFileAttachment } from "../../api/file-attachments";
import { mcpReferenceIdentities, type MCPDraftReference } from "../../api/mcp-references";
import { v2MCPReferenceKey } from "./mcp-reference-picker";
import { useV2QueuedMessagesQuery, V2QueuedMessages } from "./queued-messages";
import { V2AgentBrowser } from "./agent-browser";
import { v2AttachmentReferenceKey } from "../attachment-keys";
import { v2ImageReferenceKey } from "./image-input";
import { APPLICATION_PREVIEW_START_REQUEST, applicationPreviewRequestCopy, applicationPreviewRequestKey, applicationServicesQueryKey,
  trackApplicationPreviewSubmission, type ApplicationPreviewRequest } from "../application-preview-request";
import type { FileEditReviewTarget } from "../../components/file-edit-panel";
import type { V2RunPane } from "../navigation";
import type { TaskReviewToolMemory } from "./task-review-tools";
import { V2ThreadContext } from "./thread-context";
import { V2ThreadPinPrompt } from "./thread-context-controls";
import { V2ThreadPlanControl } from "./thread-plan";
import { useV2ThreadSubmissions, useV2ThreadTurn, V2SubmissionError, V2RecoveredSubmissionError, removeV2Submission, v2TurnFailed, v2TurnOutcomeKnown, v2TurnWasNotQueued, type V2TurnInput } from "../use-thread-turn";
import { inspectV2SteeringRequest, inspectV2TurnRequest } from "../recovery-api";
import { useV2RecoveryStore } from "../recovery-storage";
import { settleRecoveryTurn } from "../recovery-session";
import { assertV2DraftVersion, useV2DraftDocument } from "../draft-context";
import type { V2DraftVersion } from "../draft-version";
import { V2DraftConflict } from "./draft-conflict";
import { MCPInputCards } from "./mcp-input-cards";
import { V2ApprovalCards } from "./approval-cards";
import { V2Composer } from "./composer";
import { V2ThreadRunRecovery } from "./thread-run-recovery";
import { useV2ThreadExecution, V2ThreadExecutionControl, V2PausedThreadControl } from "./thread-execution-control";
import { WorkspaceExplorer } from "../../components/workspace-explorer";
import { useFileEditLeave } from "../../components/file-edit-drafts";
import { UserTerminalPanel } from "../../components/user-terminal-panel";
import { desktopUserTerminalEnabled, closeDesktopUserTerminal } from "../../lib/desktop-bridge";
import { useModalFocusTrap } from "../../hooks/use-modal-focus-trap";
import { V2IdentityDisclosure } from "./identity-disclosure";

export { parseProjectFileLink, type FileLinkTarget } from "./narrative";

const V2TaskReview = lazy(() => import("./task-review").then((module) => ({ default: module.V2TaskReview })));
const V2Inspector = lazy(() => import("./inspector").then((module) => ({ default: module.V2Inspector })));
const V2ApplicationPreview = lazy(() => import("./application-preview").then((module) => ({ default: module.V2ApplicationPreview })));

const threadTerminalSessions = new Map<string, string>();
const terminalListeners = new Set<() => void>();

function getThreadTerminalSession(threadID: string): string {
  return threadTerminalSessions.get(threadID) ?? "";
}

function setThreadTerminalSession(threadID: string, sessionID: string) {
  if (sessionID) {
    threadTerminalSessions.set(threadID, sessionID);
  } else {
    threadTerminalSessions.delete(threadID);
  }
  terminalListeners.forEach((l) => l());
}

function subscribeTerminalSessions(listener: () => void) {
  terminalListeners.add(listener);
  return () => {
    terminalListeners.delete(listener);
  };
}

export function V2FileDrawer({
  client,
  threadID,
  workspaceID: defaultWorkspaceID,
  runID,
  initialPath = ".",
  initialLine,
  onPathChange,
  onClose: close,
  returnFocusRef,
  docked = false,
}: {
  client: APIClient;
  threadID?: string;
  workspaceID: string;
  runID: string;
  initialPath?: string;
  initialLine?: number;
  onPathChange?: (path: string) => void;
  onClose: () => void;
  returnFocusRef: RefObject<HTMLElement | null>;
  docked?: boolean;
}) {
  const closeButton = useRef<HTMLButtonElement>(null);
  const leaveFileEdit = useFileEditLeave();
  const onClose = () => leaveFileEdit(close);
  const dialog = useModalFocusTrap<HTMLElement>(true, onClose, false, closeButton, {
    isolateBackground: !docked,
    trapFocus: !docked,
    returnFocusRef,
  });

  const workspaceQuery = useQuery({
    queryKey: ["v2", "file-drawer-workspace", runID, defaultWorkspaceID, threadID],
    queryFn: async ({ signal }) => {
      if (!runID) return defaultWorkspaceID;
      if (typeof client.fileEditChangeSet === "function") {
        try {
          const changeSet = await client.fileEditChangeSet(runID, signal);
          if (changeSet?.workspace_id) return changeSet.workspace_id;
        } catch (error) {
          if (signal.aborted) throw error;
          // The task review can still confirm this exact Run's workspace.
        }
      }
      if (threadID && typeof client.get === "function") {
        const review = await readThreadReview(client, threadID, signal);
        const runMatch = review.runs?.find((r) => r.run_id === runID);
        if (runMatch) {
          if (runMatch.workspace_id) return runMatch.workspace_id;
          // A recorded Run with no workspace has an unavailable target;
          // neither the current target nor the source can replace it.
          throw new Error("此执行的文件目录当前不可用。");
        }
        if (review.current_run_id === runID && review.target?.state === "available" && review.target.workspace_id) {
          return review.target.workspace_id;
        }
        const changeMatch = [...(review.applied_changes ?? []), ...(review.unapplied_changes ?? [])].find(
          (c) => c.run_id === runID && c.workspace_id
        );
        if (changeMatch?.workspace_id) return changeMatch.workspace_id;
      }
      throw new Error("尚未找到此执行的文件目录绑定。");
    },
    enabled: Boolean(runID || defaultWorkspaceID),
    staleTime: 30_000,
    retry: false,
  });

  const resolvedWorkspaceID = workspaceQuery.data ?? "";

  const content = (
      <section
        aria-label="工作区文件"
        aria-modal={docked ? undefined : "true"}
        className={`v2-inspector-drawer v2-file-drawer${docked ? " is-docked" : ""}`}
        ref={dialog}
        role={docked ? "complementary" : "dialog"}
        tabIndex={-1}
      >
        <header>
          <div className="v2-file-drawer-title">
            <FolderTree aria-hidden="true" size={17} />
            <strong>工作区文件</strong>
          </div>
          <button aria-label="关闭文件面板" onClick={onClose} ref={closeButton} type="button">
            <X aria-hidden="true" size={18} />
          </button>
        </header>
        {runID && <div className="v2-file-drawer-context">
          <p>查看此执行绑定目录中的文件。</p>
          <V2IdentityDisclosure identity={runID} identityLabel="执行 ID" summary="执行信息" />
        </div>}
        <div className="v2-file-drawer-body">
          {workspaceQuery.isLoading ? (
            <div className="v2-file-drawer-loading" role="status">
              <LoaderCircle className="spin" size={20} />
              <span>正在确认工作区…</span>
            </div>
          ) : workspaceQuery.isError ? (
            <div className="v2-notice" role="alert">
              <p>无法确认此执行的工作区，文件目录可能已清理或暂时无法读取。请重试确认后查看文件。</p>
              <button className="compact-command" disabled={workspaceQuery.isFetching} onClick={() => void workspaceQuery.refetch()} type="button">
                重试工作区确认
              </button>
            </div>
          ) : (
            <WorkspaceExplorer
              client={client}
              workspaceID={resolvedWorkspaceID}
              runID={runID}
              initialPath={initialPath}
              initialLine={initialLine}
              onPathChange={onPathChange}
            />
          )}
        </div>
      </section>
  );
  return docked ? content : createPortal(
    <div className="v2-inspector-backdrop" role="presentation" onMouseDown={(event) => {
      if (event.target === event.currentTarget) onClose();
    }}>{content}</div>, document.body);
}

export function V2TerminalDrawer({
  runID,
  sessionID,
  threadTitle,
  workspaceName,
  onSession,
  onClose,
  returnFocusRef,
  docked = false,
}: {
  runID: string;
  sessionID: string;
  threadTitle: string;
  workspaceName: string;
  onSession: (sessionID: string) => void;
  onClose: () => void;
  returnFocusRef: RefObject<HTMLElement | null>;
  docked?: boolean;
}) {
  const closeButton = useRef<HTMLButtonElement>(null);
  const dialog = useModalFocusTrap<HTMLElement>(true, onClose, false, closeButton, {
    isolateBackground: !docked,
    trapFocus: !docked,
    returnFocusRef,
  });
  const terminalAvailable = desktopUserTerminalEnabled();
  const [terminating, setTerminating] = useState(false);
  const [terminationError, setTerminationError] = useState<string | null>(null);

  const handleTerminate = async () => {
    if (!sessionID || terminating) return;
    const closingSessionID = sessionID;
    setTerminating(true);
    setTerminationError(null);
    try {
      await closeDesktopUserTerminal(closingSessionID);
      if (sessionID === closingSessionID) {
        onSession("");
      }
    } catch (err) {
      setTerminationError(err instanceof Error ? err.message : "终止终端失败");
    } finally {
      setTerminating(false);
    }
  };

  const content = (
      <section
        aria-label="任务终端"
        aria-modal={docked ? undefined : "true"}
        className={`v2-inspector-drawer v2-terminal-drawer${docked ? " is-docked" : ""}`}
        ref={dialog}
        role={docked ? "complementary" : "dialog"}
        tabIndex={-1}
      >
        <header>
          <div className="v2-terminal-drawer-title">
            <SquareTerminal aria-hidden="true" size={17} />
            <strong>任务终端 · {threadTitle}</strong>
            <small title="工作区目录">{workspaceName}</small>
          </div>
          <div className="v2-terminal-header-actions">
            <button
              aria-label="收起终端"
              className="v2-terminal-action-btn"
              onClick={onClose}
              title="收起终端（后台进程保持运行）"
              type="button"
            >
              <ChevronDown aria-hidden="true" size={14} />
              <span>收起</span>
            </button>
            {sessionID && (
              <button
                aria-label="终止终端进程"
                className="v2-terminal-action-btn danger"
                disabled={terminating}
                onClick={() => void handleTerminate()}
                title="结束终端（终止当前进程）"
                type="button"
              >
                <Square aria-hidden="true" size={12} />
                <span>{terminating ? "正在终止…" : "终止"}</span>
              </button>
            )}
            <button aria-label="关闭终端面板" onClick={onClose} ref={closeButton} type="button">
              <X aria-hidden="true" size={18} />
            </button>
          </div>
        </header>
        {terminationError && (
          <div className="v2-terminal-error-notice" role="alert">
            <span>终止终端失败：{terminationError}</span>
            <button onClick={() => setTerminationError(null)} type="button">关闭提示</button>
          </div>
        )}
        <div className="v2-terminal-drawer-body">
          {terminalAvailable ? (
            <UserTerminalPanel runID={runID} sessionID={sessionID} onSession={onSession} />
          ) : (
            <div className="v2-terminal-disabled-notice" role="status">
              <SquareTerminal aria-hidden="true" size={24} />
              <p>当前运行环境未启用桌面终端。仅在 Universal-Code 桌面端运行时支持本机 Debug 终端。</p>
            </div>
          )}
        </div>
      </section>
  );
  return docked ? content : createPortal(
    <div className="v2-inspector-backdrop" role="presentation" onMouseDown={(event) => {
      if (event.target === event.currentTarget) onClose();
    }}>{content}</div>, document.body);
}

export function V2Conversation({ client, threadID, workspaces, onArchive, onManageModels,
  onOpenInspector, draft: legacyDraft, onDraftChange: legacyDraftChange, view = "conversation", onOpenTool, onOpenInspectorHome, onOpenWorktree, onExitInspector,
  reviewEntry, onReviewEntryHandled, reviewMemoryRef }: {
  client: APIClient;
  threadID: string;
  workspaces: WorkspaceView[];
  onArchive: (thread: ThreadView) => void;
  onOpenInspector: (returnFocus: HTMLElement | null) => void;
  onManageModels: () => void;
  draft?: string;
  onDraftChange?: (content: string, expected?: string) => void;
  view?: "conversation" | "inspector";
  onOpenTool?: (tool: "run" | "session" | "schedule", resourceID?: string, pane?: V2RunPane) => void;
  onOpenInspectorHome?: () => void;
  onOpenWorktree?: (workspace: WorkspaceView) => void;
  onExitInspector?: () => void;
  reviewEntry?: { threadID: string; runID: string; requestID: string };
  onReviewEntryHandled?: (requestID: string) => void;
  reviewMemoryRef?: RefObject<TaskReviewToolMemory>;
}) {
  const queryClient = useQueryClient();
  const previewRequest = useQuery<ApplicationPreviewRequest | null>({
    queryKey: applicationPreviewRequestKey(threadID), queryFn: () => null,
    initialData: null, enabled: false, gcTime: Infinity,
  });
  // The preview panel owns network reads and polling. This observer retains its
  // validated source matches without loading the panel or fetching on arrival.
  const previewServices = useQuery({ queryKey: applicationServicesQueryKey(threadID),
    queryFn: ({ signal }) => client.listThreadApplicationServices(threadID, signal), enabled: false });
  const recovery = useV2RecoveryStore();
  const checkedRecovery = useRef(new Set<string>());
  const [checking, setChecking] = useState<string[]>([]);
  const [observations, setObservations] = useState<Record<string, string>>({});
  const [menuOpen, setMenuOpen] = useState(false);
  const [activePane, setActivePane] = useState<"review" | "preview" | "context" | "files" | "terminal" | null>(null);
  const leaveFileEdit = useFileEditLeave();
  const setPaneOpen = (pane: NonNullable<typeof activePane>, open: boolean) =>
    (open || activePane === pane) && leaveFileEdit(() => setActivePane(open ? pane : null));
  const reviewOpen = activePane === "review";
  const previewOpen = activePane === "preview";
  const contextOpen = activePane === "context";
  const filesOpen = activePane === "files";
  const terminalDrawerOpen = activePane === "terminal";
  const setReviewOpen = (open: boolean) => setPaneOpen("review", open);
  const setPreviewOpen = (open: boolean) => setPaneOpen("preview", open);
  const setContextOpen = (open: boolean) => setPaneOpen("context", open);
  const setTerminalDrawerOpen = (open: boolean) => setPaneOpen("terminal", open);
  const [reviewFileTarget, setReviewFileTarget] = useState<FileEditReviewTarget | undefined>();
  const [reviewToolTarget, setReviewToolTarget] = useState<{ runID: string; tool: "github-review" }>();
  const reviewReturnFocus = useRef<HTMLButtonElement | null>(null);
  const contextTrigger = useRef<HTMLButtonElement>(null);
  const previewTrigger = useRef<HTMLButtonElement>(null);
  const [inspectorComposerOpen, setInspectorComposerOpen] = useState(false);
  const composerContainerRef = useRef<HTMLDivElement>(null);
  const [composerFocusRequest, setComposerFocusRequest] = useState<{ threadID: string } | null>(null);
  const [confirmedSubmission, setConfirmedSubmission] = useState<V2TurnInput | null>(null);
  const [deliveryMode, setDeliveryMode] = useState<"next_turn" | "steer">("next_turn");
  const reviewTrigger = useRef<HTMLButtonElement>(null);
  const [fileDrawerState, setFileDrawerState] = useState<{
    path: string;
    line?: number;
    runID?: string;
  }>({ path: "." });
  const fileTrigger = useRef<HTMLButtonElement>(null);
  const fileReturnFocus = useRef<HTMLElement | null>(null);

  const terminalTrigger = useRef<HTMLButtonElement>(null);
  const terminalReturnFocus = useRef<HTMLElement | null>(null);
  const terminalAvailable = desktopUserTerminalEnabled();

  const currentTerminalSessionID = useSyncExternalStore(
    subscribeTerminalSessions,
    () => getThreadTerminalSession(threadID),
    () => ""
  );
  const handleTerminalSessionChange = useCallback((newSessionID: string) => {
    setThreadTerminalSession(threadID, newSessionID);
  }, [threadID]);

  const openFile = useCallback(({
    path,
    line,
    runID,
    triggerElement,
  }: {
    path: string;
    line?: number;
    runID?: string;
    triggerElement?: HTMLElement | null;
  }) => {
    leaveFileEdit(() => {
      fileReturnFocus.current = triggerElement ?? fileTrigger.current;
      setActivePane("files");
      setFileDrawerState({ path, line, runID });
    });
  }, [leaveFileEdit]);
  const turn = useV2ThreadTurn(client);
  const submissions = useV2ThreadSubmissions(threadID);
  const submitTrackedTurn = async (input: V2TurnInput) => {
    trackApplicationPreviewSubmission(queryClient, input, "submitting");
    try {
      const result = await turn.mutateAsync(input);
      trackApplicationPreviewSubmission(queryClient, input, "accepted", {
        runID: "run_id" in result ? result.run_id : undefined, messageID: result.steering.id,
      });
      return result;
    } catch (error) {
      trackApplicationPreviewSubmission(queryClient, input,
        v2TurnFailed(error) ? "failed" : v2TurnWasNotQueued(error) ? "rejected" : "unconfirmed",
        error instanceof APIRequestError ? { runID: error.turnFailure?.run_id, messageID: error.turnFailure?.message_id } : undefined);
      throw error;
    }
  };
  const confirmSubmission = async (input: V2TurnInput): Promise<"accepted" | "rejected" | "unresolved"> => {
    // Older connectors without the read contract retain their explicit-key
    // retry behavior. A durable recovery scope always uses the read-only API.
    if (!recovery) {
      try {
        await submitTrackedTurn(input);
        setConfirmedSubmission(input);
        if (draft === (input.draft ?? input.content)) onDraftChange?.("", draft);
        return "accepted";
      } catch (error) {
        if (!v2TurnOutcomeKnown(error)) throw new V2SubmissionError(input, error);
        setConfirmedSubmission(input);
        return "rejected";
      }
    }
    setChecking((current) => [...current, input.operationKey]);
    try {
      const result = input.deliveryMode === "steer"
        ? await inspectV2SteeringRequest(client, input)
        : await inspectV2TurnRequest(client, input);
      if (result.state === "not_received" || (result.state === "received" && !result.message_id)) {
        setObservations((current) => ({ ...current, [input.operationKey]: result.state === "not_received"
          ? "服务端暂未找到原提交。请继续核对；确认需要重试时，可主动发送原消息。"
          : "原提交已登记，但消息尚未入队。可以继续核对，或主动继续提交原消息。" }));
        return "unresolved";
      }
      const accepted = result.state !== "rejected" &&
        !(input.deliveryMode === "steer" && result.message_status === "cancelled");
      trackApplicationPreviewSubmission(queryClient, input,
        result.state === "failed" ? "failed" : accepted ? "accepted" : "rejected",
        { runID: "run_id" in result ? result.run_id : undefined, messageID: result.message_id });
      settleRecoveryTurn(recovery, input, accepted);
      if (accepted && !input.draftVersion) queryClient.setQueryData<V2FileReference[]>(v2FileReferenceKey(input.workspaceID, input.threadID),
        (current) => current?.filter(({ id }) => !input.files?.some((file) => file.id === id)));
      if (accepted && !input.draftVersion) queryClient.setQueryData<WorkspaceImageAttachment[]>(v2ImageReferenceKey(input.workspaceID, input.threadID),
        (current) => current?.filter(({ id }) => !input.images?.some((image) => image.id === id)));
      if (accepted && !input.draftVersion) queryClient.setQueryData<WorkspaceFileAttachment[]>(v2AttachmentReferenceKey(input.workspaceID, input.threadID),
        (current) => current?.filter((file) => !input.attachments?.some((sent) => sent.id === file.id &&
          sent.workspace_id === file.workspace_id && sent.sha256 === file.sha256 && sent.byte_size === file.byte_size)));
      if (accepted && !input.draftVersion) queryClient.setQueryData<MCPDraftReference[]>(v2MCPReferenceKey(input.workspaceID, input.threadID),
        (current) => current?.filter(({ id }) => !input.mcpReferences?.some((item) => item.id === id)));
      removeV2Submission(queryClient, input);
      setConfirmedSubmission(input);
      if (accepted && !managedDraft && !input.draftVersion && draft === (input.draft ?? input.content)) onDraftChange?.("", draft);
      await Promise.allSettled([
        queryClient.invalidateQueries({ queryKey: v2QueryKeys.thread(input.threadID) }),
        queryClient.invalidateQueries({ queryKey: v2QueryKeys.transcript(input.threadID) }),
      ]);
      return accepted ? "accepted" : "rejected";
    } catch (error) {
      setObservations((current) => ({ ...current, [input.operationKey]: "暂时无法读取原提交结果，原消息和新草稿均已保留。" }));
      throw new V2SubmissionError(input, error);
    } finally { setChecking((current) => current.filter((key) => key !== input.operationKey)); }
  };
  useEffect(() => {
    for (const { input, error } of submissions) {
      if (!(error instanceof V2RecoveredSubmissionError) || checkedRecovery.current.has(input.operationKey)) continue;
      checkedRecovery.current.add(input.operationKey);
      void confirmSubmission(input).catch(() => undefined);
    }
  }, [submissions]);
  const executionQuery = useV2ThreadExecution(client, threadID);
  const conversationRef = useRef<HTMLElement>(null);
  const [roomForDock, setRoomForDock] = useState(false);
  const docked = view === "conversation" && roomForDock;
  const scrollRef = useRef<HTMLDivElement>(null);
  const menuTriggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const firstMenuItemRef = useRef<HTMLButtonElement>(null);
  const olderScrollAnchorRef = useRef<{ threadID: string; height: number; top: number; pages: number;
    row?: HTMLElement; offset?: number } | null>(null);
  const readingThreadRef = useRef("");
  const followLatestRef = useRef(true);
  const [hasNewContent, setHasNewContent] = useState(false);
  const optimistic = useMemo(() => submissions.filter(({ pending }) => pending).map(({ input }) => ({
    id: input.operationKey, text: input.content, images: input.images, attachments: input.attachments, createdAt: input.createdAt,
  })), [submissions]);
  const turnSubmitting = submissions.some(({ pending }) => pending);
  const reconciling = submissions.some((submission) => submission.reconciling);
  const detailQuery = useQuery({
    queryKey: v2QueryKeys.thread(threadID),
    queryFn: ({ signal }) => client.get<ThreadDetailView>(
      `/threads/${encodeURIComponent(threadID)}`, {}, signal),
    enabled: Boolean(threadID),
    refetchInterval: (query) => {
      const detail = query.state.data;
      if (detail?.active_run && ["preparing", "running", "waiting_approval"]
        .includes(detail.active_run.status)) return 1_500;
      return optimistic.length > 0 ? 750 : false;
    },
  });
  const managedDraft = useV2DraftDocument(detailQuery.data?.thread.workspace_id ?? "", threadID);
  const draft = managedDraft?.state.snapshot.text ?? legacyDraft;
  const onDraftChange = managedDraft?.changeText ?? legacyDraftChange;
  const activeRun = detailQuery.data?.active_run;
  const streamRunID = activeRun?.id ?? "";
  const streamEnabled = Boolean(activeRun && ["preparing", "running", "waiting_approval"]
    .includes(activeRun.status));
  const queuedRun = activeRun ?? detailQuery.data?.last_run;
  const queueBinding = queuedRun?.session_id ? { threadID, runID: queuedRun.id, sessionID: queuedRun.session_id,
    workspaceID: detailQuery.data?.thread.workspace_id ?? "" } : null;
  // The panel owns polling; this observer shares its validated snapshots.
  const queueQuery = useV2QueuedMessagesQuery(client, queueBinding, false);
  const eventStream = useRunEventStream(client, streamEnabled ? streamRunID : "");
  const publicStream = usePublicModelStream(client, streamRunID, streamEnabled);
  const transcriptQuery = useV2ThreadTranscript(client, threadID, {
    // Events drive ordinary updates. A slower head-only check also covers a
    // quiet connection or a durable write that follows its notification.
    refetchInterval: optimistic.length > 0 ? 750 : streamEnabled
      ? eventStream.status === "live" ? 10_000 : 1_200 : false,
  });
  const refreshTranscript = transcriptQuery.refetch;
  const liveSnapshot = publicStream.snapshot?.call.run_id === streamRunID
    ? publicStream.snapshot : null;
  const latestFrame = eventStream.frames.at(-1);
  const refreshTimer = useRef<number | null>(null);
  useEffect(() => {
    if (!latestFrame || latestFrame.run_id !== streamRunID ||
      latestFrame.event.type === "model.delta" || refreshTimer.current !== null) return;
    refreshTimer.current = window.setTimeout(() => {
      refreshTimer.current = null;
      void queryClient.invalidateQueries({ queryKey: v2QueryKeys.threads("active") });
      void queryClient.invalidateQueries({ queryKey: v2QueryKeys.thread(threadID), exact: true });
      void queryClient.invalidateQueries({ queryKey: v2QueryKeys.transcript(threadID), exact: true }, { cancelRefetch: false });
      void queryClient.invalidateQueries({ queryKey: v2QueryKeys.approvals(streamRunID) });
      void queryClient.invalidateQueries({ queryKey: ["run", streamRunID] });
    }, 100);
  }, [latestFrame, queryClient, streamRunID, threadID]);
  useEffect(() => () => {
    if (refreshTimer.current !== null) {
      window.clearTimeout(refreshTimer.current);
      refreshTimer.current = null;
    }
  }, [streamRunID, threadID]);
  const previousStream = useRef<{ threadID: string; runID: string; enabled: boolean;
    events: string; finalizing: boolean } | null>(null);
  useEffect(() => {
    const previous = previousStream.current;
    const finalizing = publicStream.status === "finalizing" || Boolean(previous?.threadID === threadID &&
      previous.runID === streamRunID && previous.finalizing);
    const modelSettled = finalizing && publicStream.status === "waiting";
    previousStream.current = { threadID, runID: streamRunID, enabled: streamEnabled,
      events: eventStream.status, finalizing: finalizing && !modelSettled && streamEnabled };
    if (!previous || previous.threadID !== threadID) return;
    const finished = previous.enabled && (!streamEnabled || previous.runID !== streamRunID);
    const reconnected = previous.events === "reconnecting" && eventStream.status === "live";
    if (finished || modelSettled || reconnected) {
      // If an older read is still in flight, request one sequential catch-up
      // pass so it cannot hide a terminal write or a changed historical status.
      void refreshTranscript({ refreshMutable: true });
    }
  }, [threadID, streamRunID, streamEnabled, eventStream.status, publicStream.status, refreshTranscript]);
  const transcriptItems = transcriptQuery.items;
  const preparedNarrative = useMemo(() => prepareThreadNarrative(transcriptItems), [transcriptItems]);
  const durableNarrative = preparedNarrative.entries;
  const liveNarrative = useMemo(() => projectLiveThreadNarrative(preparedNarrative, {
    runId: streamRunID, snapshot: liveSnapshot, status: publicStream.status,
  }), [liveSnapshot, publicStream.status, streamRunID, preparedNarrative]);
  const existingUserText = useMemo(() => new Set(durableNarrative.filter((entry) => entry.kind === "user")
    .map((entry) => JSON.stringify([entry.text, imageIdentities(entry.images), fileAttachmentIdentities(entry.attachments)]))),
  [durableNarrative]);
  const narrative = useMemo(() => {
    const pending: NarrativeEntry[] = optimistic.filter(({ text, images, attachments }) => !existingUserText.has(JSON.stringify([text, imageIdentities(images), fileAttachmentIdentities(attachments)])))
      .map((entry) => ({ id: entry.id, kind: "user", text: entry.text,
        images: entry.images, attachments: entry.attachments, createdAt: entry.createdAt, provisional: true }));
    return pending.length ? [...liveNarrative, ...pending] : liveNarrative;
  }, [liveNarrative, existingUserText, optimistic]);
  const transcriptSources = useMemo(() => new Map(transcriptItems.map((item) => [item.id, item])), [transcriptItems]);
  const visibleNarrative = useMemo(() => {
    // The complete queue is identity checked by readQueuedMessages. A failed
    // refresh or a different Run must keep every unconfirmed historical row.
    const queuedIDs = new Set(queueQuery.isSuccess ? queueQuery.data.items.map((item) => item.id) : []);
    return narrative.filter((entry) => {
      if (recoveryRepresentsNotice(entry, detailQuery.data?.recovery)) return false;
      const source = entry.kind === "user" ? transcriptSources.get(entry.id) : undefined;
      return !(source?.source === "operator" && source.kind === "operator_input" && source.status === "pending" &&
        source.durable && !source.provisional && source.run_id === queueBinding?.runID &&
        source.source_ref && queuedIDs.has(source.source_ref));
    });
  }, [narrative, detailQuery.data?.recovery, queueBinding?.runID, queueQuery.data, queueQuery.isSuccess, transcriptSources]);
  // Contents the user already submitted: in-flight, just confirmed, or the
  // latest durable user entry. A draft identical to any of these is leftover
  // from a sent message, so the composer offers to clear it before the user
  // accidentally appends a new request to already-submitted text.
  const submissionWorkspaceID = detailQuery.data?.thread.workspace_id ?? "";
  const submittedContents = useMemo(() => {
    const contents = new Set<string>();
    for (const { input, pending } of submissions) if (pending) contents.add(input.content);
    // A request can settle after this component has switched to another Thread.
    if (confirmedSubmission?.threadID === threadID &&
      confirmedSubmission.workspaceID === submissionWorkspaceID) contents.add(confirmedSubmission.content);
    const lastUserEntry = durableNarrative.findLast((entry) => entry.kind === "user");
    if (lastUserEntry?.kind === "user") contents.add(lastUserEntry.text);
    return contents;
  }, [submissions, confirmedSubmission, durableNarrative, threadID, submissionWorkspaceID]);
  const failedSubmissions = submissions.filter(({ pending, error }) => !pending && error);
  const submissionNotices = failedSubmissions.filter(({ error }) => !(error instanceof APIRequestError &&
    error.turnFailed && narrativeRepresentsFailedSubmission(durableNarrative, threadID, error.turnFailure)));
  useLayoutEffect(() => {
    if (view !== "conversation") { readingThreadRef.current = ""; return; }
    const element = scrollRef.current;
    if (!element || transcriptQuery.isLoading) return;
    if (readingThreadRef.current !== threadID) {
      readingThreadRef.current = threadID;
      const saved = queryClient.getQueryData<{ top: number; following: boolean }>(["v2", "reading", threadID]);
      followLatestRef.current = saved?.following ?? true;
      element.scrollTop = saved && !saved.following ? saved.top : element.scrollHeight;
      setHasNewContent(false);
      return;
    }
    const anchor = olderScrollAnchorRef.current;
    if (anchor?.threadID === threadID) {
      // Streaming can repaint while an older page is still in flight. Keep the
      // anchor until that page actually arrives instead of consuming it early.
      if ((transcriptQuery.data?.pages.length ?? 0) <= anchor.pages) {
        if (transcriptQuery.isFetchNextPageError && !transcriptQuery.isFetchingNextPage) olderScrollAnchorRef.current = null;
        return;
      }
      element.scrollTop = anchor.row?.isConnected && anchor.offset !== undefined
        ? element.scrollTop + anchor.row.getBoundingClientRect().top - anchor.offset
        : anchor.top + (element.scrollHeight - anchor.height);
      olderScrollAnchorRef.current = null;
      queryClient.setQueryData(["v2", "reading", threadID], { top: element.scrollTop, following: followLatestRef.current });
      return;
    }
    if (followLatestRef.current) element.scrollTop = element.scrollHeight;
    else setHasNewContent(true);
  }, [visibleNarrative, detailQuery.data?.active_run?.status, liveSnapshot?.revision,
    transcriptQuery.data?.pages.length, transcriptQuery.isLoading, transcriptQuery.isFetchNextPageError,
    transcriptQuery.isFetchingNextPage, threadID, queryClient, view]);

  useEffect(() => {
    setMenuOpen(false);
    setActivePane(null);
    setReviewFileTarget(undefined);
    setReviewToolTarget(undefined);
    setFileDrawerState({ path: ".", line: undefined, runID: undefined });
    setComposerFocusRequest(null);
    olderScrollAnchorRef.current = null;
    setDeliveryMode("next_turn");
  }, [threadID, view]);
  useEffect(() => {
    if (!reviewEntry || reviewEntry.threadID !== threadID) return;
    setReviewFileTarget(undefined);
    setReviewToolTarget({ runID: reviewEntry.runID, tool: "github-review" });
    reviewReturnFocus.current = reviewTrigger.current;
    setActivePane("review");
    onReviewEntryHandled?.(reviewEntry.requestID);
  }, [reviewEntry?.requestID, threadID]);
  useEffect(() => {
    if (!composerFocusRequest || composerFocusRequest.threadID !== threadID || reviewOpen || previewOpen || contextOpen ||
      filesOpen || terminalDrawerOpen ||
      (view === "inspector" && !inspectorComposerOpen)) return;
    const frame = requestAnimationFrame(() => {
      const container = composerContainerRef.current;
      if (container?.dataset.threadId === composerFocusRequest.threadID && !container.hidden) {
        container.querySelector<HTMLElement>('textarea, [contenteditable="true"][role="textbox"]')?.focus();
      }
      setComposerFocusRequest((current) => current === composerFocusRequest ? null : current);
    });
    return () => cancelAnimationFrame(frame);
  }, [composerFocusRequest, threadID, activePane, view, inspectorComposerOpen]);
  useEffect(() => {
    if (!menuOpen) return;
    firstMenuItemRef.current?.focus();
    const closeForOutsideClick = (event: MouseEvent) => {
      const target = event.target;
      if (!(target instanceof Node) || menuRef.current?.contains(target) ||
        menuTriggerRef.current?.contains(target)) return;
      setMenuOpen(false);
    };
    const closeForEscape = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      event.stopPropagation();
      setMenuOpen(false);
      menuTriggerRef.current?.focus();
    };
    document.addEventListener("click", closeForOutsideClick);
    document.addEventListener("keydown", closeForEscape);
    return () => {
      document.removeEventListener("click", closeForOutsideClick);
      document.removeEventListener("keydown", closeForEscape);
    };
  }, [menuOpen]);

  useLayoutEffect(() => {
    const container = conversationRef.current;
    if (!container) return;
    // Reserve at least 600px for the conversation beside a 340px panel.
    const measure = () => setRoomForDock(container.clientWidth >= 960);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(container);
    return () => observer.disconnect();
  }, [detailQuery.isSuccess]);

  if (detailQuery.isLoading) return <div className="v2-main-loading"><LoaderCircle className="spin" size={20} />
    <span>正在打开对话…</span></div>;
  if (detailQuery.isError || !detailQuery.data) return <div className="v2-main-error" role="alert">
    <strong>无法打开对话</strong><span>请检查服务连接，然后重试。已有输入会保留。</span>
    <button onClick={() => void detailQuery.refetch()} type="button">重新打开</button>
    <details><summary>查看原因</summary>{detailQuery.error instanceof Error
      ? detailQuery.error.message : "请求未返回对话数据"}</details></div>;
  const detail = detailQuery.data;
  const currentRun = detail.active_run ?? detail.last_run;
  const workspace = workspaces.find(({ id }) => id === detail.thread.workspace_id);
  const runActive = executionQuery.data ? executionQuery.data.state !== "idle"
    : Boolean(detail.active_run && ["preparing", "running"].includes(detail.active_run.status));
  const modelActive = Boolean(liveSnapshot) &&
    (publicStream.status === "live" || publicStream.status === "finalizing");
  const working = turnSubmitting || modelActive || (!executionQuery.isError &&
    (executionQuery.data?.state === "running" || executionQuery.data?.state === "stopping"));
  const replyToPinProposal = async (content: string, operationKey: string) => {
    const previous = submissions.find((item) => item.input.operationKey === operationKey)?.input;
    if (previous && previous.content !== content) throw new Error("原答复身份与当前内容不一致，请重新读取。");
    await submitTrackedTurn(previous ?? { threadID, workspaceID: detail.thread.workspace_id ?? "",
      content, operationKey, createdAt: new Date().toISOString() });
  };
  const canSteer = executionQuery.data?.state === "running" ||
    (!executionQuery.data && detail.active_run?.status === "running");
  // A paused run is not gone: steer stays unavailable, but the explanation
  // names the pause so the operator does not think the task ended.
  const steerUnavailable = currentRun.status === "paused" ? "当前任务已暂停"
    : "当前任务已停止或不再运行";
  const activityLabel = threadActivityLabel({ threadID, execution: executionQuery.data,
    readable: client.hasThreadExecutionRead === true, error: executionQuery.isError });
  const fileReferenceUnavailableReason = currentRun.status === "waiting_approval"
      ? "当前任务正在等待批准，暂时不能新增文件引用。请先处理待批准的操作。"
      : working || runActive ? modelActive || activityLabel === "正在工作" || activityLabel === "正在停止"
        ? "项目内文件引用需等执行结束后发送；补充文字和上传附件可以排队。"
        : turnSubmitting ? "消息正在提交，暂时不能新增文件引用。已有输入会保留。"
          : activityLabel === "停止未完成" ? "停止尚未完成，暂时不能新增文件引用。请先重试停止。"
            : "活动状态尚未确认，暂时不能新增文件引用。已有输入会保留。"
        : undefined;

  const send = async (content: string, files?: V2FileReference[], images?: WorkspaceImageAttachment[], draftVersion?: V2DraftVersion, attachments?: WorkspaceFileAttachment[], mcpReferences?: MCPDraftReference[]) => {
    if (deliveryMode === "steer" && !canSteer) {
      throw new Error(`${steerUnavailable}。纠正草稿已保留；请选择“下一轮处理”发送。`);
    }
    if (deliveryMode === "steer" && (files?.length || images?.length || attachments?.length || mcpReferences?.length)) {
      throw new Error("更新当前任务目前只支持文字。附件和引用已保留，请选择“下一轮处理”发送完整消息。");
    }
    const fingerprint = JSON.stringify([deliveryMode, content, files ?? [], imageIdentities(images), fileAttachmentIdentities(attachments), mcpReferenceIdentities(mcpReferences)]);
    let replaced = submissions.findLast(({ error }) => v2TurnOutcomeKnown(error))?.input.operationKey;
    // The user's new request waits for unknown earlier submissions to settle.
    // Confirmation uses the original payload and key, never the edited draft.
    for (const { input } of submissions.filter(({ error }) => error && !v2TurnOutcomeKnown(error))) {
      const outcome = await confirmSubmission(input);
      if (outcome === "accepted" && JSON.stringify([input.deliveryMode ?? "next_turn", input.content, input.files ?? [], imageIdentities(input.images), fileAttachmentIdentities(input.attachments), mcpReferenceIdentities(input.mcpReferences)]) === fingerprint) return;
      if (outcome === "rejected") replaced = input.operationKey;
      if (outcome === "unresolved") {
        if (recovery) {
          if (JSON.stringify([input.deliveryMode ?? "next_turn", input.content, input.files ?? [], imageIdentities(input.images), fileAttachmentIdentities(input.attachments), mcpReferenceIdentities(input.mcpReferences)]) === fingerprint) { await submitTrackedTurn(input); return; }
          throw new V2SubmissionError(input, new Error("原提交尚未确认。新草稿已保留，请先核对或继续提交原消息。"));
        }
        replaced = input.operationKey;
      }
    }
    if (managedDraft && draftVersion) assertV2DraftVersion(managedDraft, draftVersion,
      { text: draft ?? content, files: files ?? [], images: images ?? [], attachments, mcpReferences });
    const operationKey = `v2-thread-turn-${globalThis.crypto.randomUUID()}`;
    const input: V2TurnInput = { threadID, workspaceID: detail.thread.workspace_id ?? "", content,
      ...(deliveryMode === "steer" ? { deliveryMode: "steer" as const, sessionID: currentRun.session_id } : {}),
      ...(draft !== undefined ? { draft } : {}),
      operationKey, createdAt: new Date().toISOString(), ...(files?.length ? { files } : {}),
      ...(images?.length ? { images } : {}),
      ...(attachments?.length ? { attachments } : {}),
      ...(mcpReferences?.length ? { mcpReferences } : {}),
      ...(draftVersion ? { draftVersion } : {}),
      ...(replaced ? { replacesOperationKey: replaced } : {}) };
    trackApplicationPreviewSubmission(queryClient, input, "submitting", undefined, true);
    try {
      await submitTrackedTurn(input);
      // Remember the accepted payload: once the settled mutation leaves the
      // cache, a matching leftover draft is still recognized as submitted.
      setConfirmedSubmission(input);
    } catch (error) {
      // The client validates this sealed reference against this request's Thread.
      // Its input was committed even though execution failed. Let the Composer
      // retire only this submitted draft/files; the failed mutation and durable
      // explanation stay visible. Original-key confirmations use the path above.
      if (error instanceof APIRequestError && error.turnFailed === true &&
        error.turnFailure?.thread_id === input.threadID) { setConfirmedSubmission(input); return; }
      throw new V2SubmissionError(input, error);
    }
  };

  const loadOlderTranscript = async () => {
    const element = scrollRef.current;
    const top = element?.getBoundingClientRect().top ?? 0;
    const row = element ? Array.from(element.querySelectorAll<HTMLElement>(".v2-narrative > li"))
      .find((candidate) => { const rect = candidate.getBoundingClientRect(); return rect.height > 0 && rect.bottom > top; }) : undefined;
    const anchor = element ? { threadID, height: element.scrollHeight, top: element.scrollTop,
      pages: transcriptQuery.data?.pages.length ?? 0, row,
      offset: row?.getBoundingClientRect().top } : null;
    olderScrollAnchorRef.current = anchor;
    try {
      await transcriptQuery.fetchNextPage();
    } catch {
      if (olderScrollAnchorRef.current === anchor) olderScrollAnchorRef.current = null;
    }
  };

  const agentActivity = projectAgentActivity({ threadID, runID: detail.active_run?.id,
    runStatus: currentRun.status, archived: detail.thread.status !== "active",
    execution: executionQuery.data, executionReadable: client.hasThreadExecutionRead === true,
    executionError: executionQuery.isError, submitting: turnSubmitting, reconciling,
    snapshot: liveSnapshot, streamStatus: publicStream.status,
    progressError: Boolean(eventStream.error || publicStream.error || transcriptQuery.isError),
    transcript: transcriptItems });
  const appendDraftAndReveal = (content: string) => {
    if (content) onDraftChange?.(draft?.trim() ? `${draft}\n\n${content}` : content);
    if (view === "inspector") setInspectorComposerOpen(true);
    setReviewOpen(false); setPreviewOpen(false); setContextOpen(false);
    setComposerFocusRequest({ threadID });
  };
  const companion = <>
    {filesOpen && <V2FileDrawer client={client} threadID={threadID}
      workspaceID={detail.thread.workspace_id ?? ""}
      runID={fileDrawerState.runID || currentRun.id} initialPath={fileDrawerState.path}
      initialLine={fileDrawerState.line} docked={docked}
      onPathChange={(path) => setFileDrawerState((current) => ({ ...current, path, line: undefined }))}
      onClose={() => {
        setActivePane(null);
        if (fileReturnFocus.current?.isConnected) fileReturnFocus.current.focus();
        else fileTrigger.current?.focus();
      }} returnFocusRef={fileReturnFocus} />}
    {terminalDrawerOpen && <V2TerminalDrawer runID={currentRun.id} sessionID={currentTerminalSessionID}
      threadTitle={detail.thread.title} workspaceName={workspace?.name ?? "本地工作区"}
      onSession={handleTerminalSessionChange} docked={docked}
      onClose={() => {
        setTerminalDrawerOpen(false);
        if (terminalReturnFocus.current?.isConnected) terminalReturnFocus.current.focus();
        else terminalTrigger.current?.focus();
      }} returnFocusRef={terminalReturnFocus} />}
  </>;
  return <section className={`v2-conversation${view === "inspector" ? " is-inspector" : ""}`} ref={conversationRef}>
    <header className="v2-conversation-header">
      <div><Folder aria-hidden="true" size={17} /><strong>{detail.thread.title}</strong></div>
      <div className="v2-header-actions">
        <V2ThreadExecutionControl client={client} execution={executionQuery.data}
          threadID={threadID} />
        <button aria-expanded={menuOpen} aria-haspopup="menu" aria-label="对话操作"
          onClick={() => setMenuOpen((value) => !value)} ref={menuTriggerRef} type="button">
          <CircleEllipsis aria-hidden="true" size={18} />
        </button>
        {menuOpen && <div className="v2-thread-menu" ref={menuRef} role="menu">
          <button disabled={detail.thread.status !== "active" || !client.hasThreadControl} onClick={() => {
            setMenuOpen(false);
            menuTriggerRef.current?.focus();
            onArchive(detail.thread);
          }} ref={firstMenuItemRef} role="menuitem" type="button">
            <Archive aria-hidden="true" size={15} />归档对话</button>
          {view !== "inspector" && <button onClick={() => { setMenuOpen(false); onOpenInspector(menuTriggerRef.current); }}
            role="menuitem" type="button">
            <Microscope aria-hidden="true" size={15} />打开 Inspector</button>}
          {view === "inspector" && onExitInspector && <button onClick={() => {
            setMenuOpen(false); menuTriggerRef.current?.focus(); onExitInspector(); }}
            role="menuitem" type="button">
            <MessagesSquare aria-hidden="true" size={15} />返回对话视图</button>}
        </div>}
      </div>
    </header>
    <nav className="v2-task-navigation" aria-label="任务工作区">
      <div className="v2-task-workbench" role="group" aria-label="对话与工作台">
        <button aria-pressed={view === "conversation" && activePane === null} disabled={view === "inspector" && !onExitInspector}
          onClick={() => leaveFileEdit(() => { setActivePane(null); onExitInspector?.(); })} type="button"><MessagesSquare size={16} aria-hidden="true" />对话</button>
        <button aria-label="工作区文件" aria-pressed={filesOpen} onClick={() => {
          if (filesOpen) { leaveFileEdit(() => setActivePane(null)); return; }
          fileReturnFocus.current = fileTrigger.current;
          setFileDrawerState({ path: ".", line: undefined, runID: currentRun.id }); setActivePane("files");
        }} ref={fileTrigger} type="button"><FolderTree aria-hidden="true" size={16} />文件</button>
        <button aria-label="终端" aria-pressed={terminalDrawerOpen} title={terminalAvailable ? "打开任务终端" : "桌面终端仅在桌面端可用"} onClick={() => {
          terminalReturnFocus.current = terminalTrigger.current; setTerminalDrawerOpen(!terminalDrawerOpen);
        }} ref={terminalTrigger} type="button"><SquareTerminal aria-hidden="true" size={16} />终端</button>
        <button aria-label="应用预览" aria-pressed={previewOpen} onClick={() => setPreviewOpen(true)}
          ref={previewTrigger} type="button"><PanelTop aria-hidden="true" size={16} />应用预览</button>
      </div>
      <div className="v2-task-workflows" role="group" aria-label="任务流程">
        <button aria-label="审阅改动" aria-pressed={reviewOpen} onClick={() => {
          setReviewFileTarget(undefined); setReviewToolTarget(undefined); reviewReturnFocus.current = reviewTrigger.current; setReviewOpen(true);
        }} ref={reviewTrigger} type="button"><FileDiff aria-hidden="true" size={16} />改动与交付</button>
        <button aria-label="查看上下文" aria-pressed={contextOpen} onClick={() => setContextOpen(true)}
          ref={contextTrigger} type="button"><BookOpen aria-hidden="true" size={16} />上下文与恢复</button>
        <button aria-pressed={view === "inspector" && activePane === null}
          onClick={(event) => { const trigger = event.currentTarget; leaveFileEdit(() => { setActivePane(null); onOpenInspector(trigger); }); }} type="button">
          <Microscope aria-hidden="true" size={16} />观察执行</button>
      </div>
    </nav>
    {view === "inspector" && (onOpenTool || onOpenInspectorHome) && <nav className="v2-inspector-advanced" aria-label="执行观察工具">
      {onOpenInspectorHome && <button onClick={onOpenInspectorHome} type="button">全部运行与会话</button>}
      {onOpenTool && <>
        <button onClick={() => onOpenTool("run", currentRun.id)} type="button">运行与工具</button>
        <button onClick={() => onOpenTool("run", currentRun.id, "ui-evidence")} type="button">界面观察证据</button>
        <button onClick={() => onOpenTool("schedule", currentRun.id)} type="button">定时观察</button>
      </>}
    </nav>}
    {contextOpen && <V2ThreadContext client={client} threadID={threadID} detail={detail}
      onReply={replyToPinProposal}
      onOpenRecovery={onOpenTool ? (pane) => { setContextOpen(false); onOpenTool("run", currentRun.id, pane); } : undefined}
      onOpenSession={onOpenTool && currentRun.session_id ? () => { setContextOpen(false); onOpenTool("session", currentRun.session_id); } : undefined}
      onClose={() => setContextOpen(false)} onRequestChange={appendDraftAndReveal} returnFocusRef={contextTrigger} />}
    {client.hasControl && <V2ThreadPinPrompt key={threadID} client={client} threadID={threadID}
      paused={working || activePane !== null || menuOpen} onReply={replyToPinProposal} />}
    {!docked && companion}
    {previewOpen && <V2LazySurface loadingText="正在加载应用预览…" errorLabel="应用预览" resetKey={threadID}
      onDismiss={() => { setPreviewOpen(false); previewTrigger.current?.focus(); }}>
      <V2ApplicationPreview key={threadID} client={client} runID={currentRun.id} threadID={threadID}
      startRequest={previewRequest.data} canRequestStart={Boolean(onDraftChange)}
      onClose={() => setPreviewOpen(false)} returnFocusRef={previewTrigger} onRequestStart={() => {
        if (!onDraftChange) return;
        if (!draft?.includes(APPLICATION_PREVIEW_START_REQUEST)) appendDraftAndReveal(APPLICATION_PREVIEW_START_REQUEST);
        else appendDraftAndReveal("");
        setDeliveryMode("next_turn");
        queryClient.setQueryData<ApplicationPreviewRequest>(applicationPreviewRequestKey(threadID), {
          request: APPLICATION_PREVIEW_START_REQUEST, phase: "draft",
        });
      }} />
    </V2LazySurface>}
    {reviewOpen && <V2LazySurface loadingText="正在加载改动审阅…" errorLabel="改动审阅" resetKey={threadID}
      onDismiss={() => { setReviewOpen(false); (reviewReturnFocus.current?.isConnected ? reviewReturnFocus.current : reviewTrigger.current)?.focus(); }}>
      <V2TaskReview client={client} detail={detail} working={working}
      onOpenWorktree={onOpenWorktree}
      initialFileTarget={reviewFileTarget}
      initialToolTarget={reviewToolTarget} toolMemoryRef={reviewMemoryRef}
      onClose={() => {
        if (!reviewReturnFocus.current?.isConnected) reviewReturnFocus.current = reviewTrigger.current;
        setReviewOpen(false);
      }} returnFocusRef={reviewReturnFocus} onRequestChange={appendDraftAndReveal} /></V2LazySurface>}
    <div className="v2-conversation-stage">
    <div className="v2-conversation-main-column">
    {view === "inspector" && !transcriptQuery.isLoading && <V2LazySurface loadingText="正在加载 Inspector…"
      errorLabel="Inspector" resetKey={threadID} onDismiss={onExitInspector}><V2Inspector client={client} key={threadID}
      detail={detail} threadID={threadID} durableItems={transcriptItems}
      liveSnapshot={liveSnapshot} liveStatus={publicStream.status}
      hasOlder={Boolean(transcriptQuery.hasNextPage)} isFetchingOlder={transcriptQuery.isFetchingNextPage}
      onLoadOlder={() => void transcriptQuery.fetchNextPage()} /></V2LazySurface>}
    <div className="v2-conversation-scroll" ref={scrollRef} onScroll={(event) => {
      if (view !== "conversation") return;
      const element = event.currentTarget;
      const following = element.scrollHeight - element.clientHeight - element.scrollTop <= 48;
      followLatestRef.current = following;
      if (following) setHasNewContent(false);
      queryClient.setQueryData(["v2", "reading", threadID], { top: element.scrollTop, following });
    }}>
      <div className="v2-conversation-content">
        {detail.thread.status === "archived" && <p className="v2-notice" role="status">
          此对话已归档，仍可查看记录。请在设置的「已归档的聊天」中取消归档后继续发送。
        </p>}
        {transcriptQuery.isLoading && <div className="v2-transcript-loading"><LoaderCircle className="spin" size={16} />
          正在整理工作记录…</div>}
        {view === "conversation" && transcriptQuery.hasNextPage && <div className="v2-transcript-loading">
          <button className="v2-composer-chip" disabled={transcriptQuery.isFetchingNextPage}
            onClick={() => void loadOlderTranscript()} type="button">
            {transcriptQuery.isFetchingNextPage
              ? <><LoaderCircle aria-hidden="true" className="spin" size={16} />正在加载更早记录…</>
              : "加载更早记录"}
          </button>
        </div>}
        {transcriptQuery.isFetchNextPageError && <div className="v2-notice tone-warning" role="alert">
          更早的工作记录加载失败，请重试。
        </div>}
        {transcriptQuery.isError && !transcriptQuery.isFetchNextPageError && <div className="v2-notice tone-warning" role="alert">
          工作记录加载失败，任务结果尚待核对。请重新读取记录。
          <button onClick={() => void transcriptQuery.refetch()} type="button">重试工作记录</button>
        </div>}
        {!client.hasThreadExecutionRead && <div className="v2-notice" role="status">
          当前连接不提供 Agent 活动状态。对话与工作记录仍可查看。
        </div>}
        {executionQuery.isError && <div className="v2-notice tone-warning" role="alert">
          Agent 的当前活动尚待确认。请刷新执行状态；已有对话内容仍可查看。
          <button onClick={() => void executionQuery.refetch()} type="button">刷新执行状态</button>
        </div>}
        {view === "conversation" && !transcriptQuery.isLoading && !transcriptQuery.isError && narrative.length === 0 && <div className="v2-transcript-empty">
          <span><ShieldCheck aria-hidden="true" size={18} /></span><p>{detail.thread.status === "archived"
            ? "此归档对话没有公开进展记录。" : "任务已经创建。Agent 的公开进展会出现在这里。"}</p></div>}
        {view === "conversation" && <V2Narrative client={client} entries={visibleNarrative} threadID={threadID} onOpenFile={openFile} />}
        {view === "conversation" && <V2AgentBrowser client={client} runID={currentRun.id} running={working || runActive} />}
        {submissionNotices.map(({ input, error }) =>
          <div className="v2-notice tone-warning" key={input.operationKey} role="alert">
            <p>{v2TurnWasNotQueued(error) ? "这条消息未入队，草稿与引用已保留。请重新选择、移除引用或处理当前限制后重试。"
              : v2TurnFailed(error) ? "本轮执行未完成，失败记录与已完成的修改会保留。可以直接发送“继续”或补充要求。"
              : observations[input.operationKey] ?? "暂时无法确认这条消息的结果。草稿与引用已保留；发送新要求时会先核对原提交，也可以重试核对。"}</p>
            <details><summary>查看原因</summary>{error instanceof Error ? error.message : "请求失败"}</details>
            {!v2TurnOutcomeKnown(error) &&
              <button disabled={turnSubmitting || checking.includes(input.operationKey)} onClick={() => {
                void confirmSubmission(input).catch(() => undefined);
              }} type="button">{checking.includes(input.operationKey) ? "正在核对…" : "重试核对"}</button>}
            {recovery && observations[input.operationKey] && !v2TurnOutcomeKnown(error) &&
              <button disabled={turnSubmitting || checking.includes(input.operationKey)} onClick={() => {
                void submitTrackedTurn(input).then(() => {
                  if (!managedDraft && !input.draftVersion && draft === (input.draft ?? input.content)) onDraftChange?.("", draft);
                }).catch(() => undefined);
              }} type="button">继续提交原消息</button>}
            {onDraftChange && <button disabled={draft?.trim() === input.content.trim()}
              onClick={() => onDraftChange(draft?.trim() ? `${draft}\n\n${input.content}` : input.content)}
              type="button">{draft?.trim() ? "添加到草稿" : "放回输入框"}</button>}
          </div>)}
        {(eventStream.error || publicStream.error) && working && <div className="v2-notice tone-warning"
          role="status">实时进度暂不可用；持久工作记录仍会继续同步。</div>}
        {detail.active_run && <MCPInputCards client={client} runID={currentRun.id} />}
        {detail.active_run && <V2ApprovalCards client={client}
          runID={currentRun.id} threadID={threadID} onReviewFile={(target, trigger) => {
            reviewReturnFocus.current = trigger; setReviewToolTarget(undefined); setReviewFileTarget(target); setReviewOpen(true);
          }} />}
        {detail.active_run && <div className="v2-command-approvals">
          <ControlledCommandProposalPanel client={client} runID={currentRun.id} threadID={threadID} />
          <HostCommandProposalPanel client={client} runID={currentRun.id} threadID={threadID} compact={view === "inspector"} />
        </div>}
      </div>
    </div>
    {view === "conversation" && hasNewContent && <button className="v2-jump-latest" onClick={() => {
      followLatestRef.current = true;
      if (scrollRef.current) scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
      setHasNewContent(false);
    }} type="button">有新内容 · 回到最新</button>}
    <div className="v2-composer-dock">
      <V2AgentActivity activity={agentActivity} />
      {previewRequest.data && <p className="v2-composer-caption" role={previewRequest.data.phase === "failed" ? "alert" : "status"}>
        {applicationPreviewRequestCopy(previewRequest.data, !previewServices.isError && Boolean(previewRequest.data.runID &&
          previewRequest.data.messageID && previewServices.data?.services.some((service) =>
            service.run_id === previewRequest.data?.runID && service.source_message_id === previewRequest.data?.messageID)))}
        <button onClick={() => setPreviewOpen(true)} type="button">查看应用服务</button>
      </p>}
      {managedDraft && <V2DraftConflict key={threadID} client={client} workspaceID={detail.thread.workspace_id ?? ""}
        state={managedDraft.state} onResolve={(token, ref) => { managedDraft.document.resolve(managedDraft.scope, token, ref); }} />}
      {currentRun.status === "paused" && <V2PausedThreadControl client={client} threadID={threadID} runID={currentRun.id} />}
      {executionQuery.data?.state === "stop_failed" && <p className="v2-composer-caption" role="alert">
        停止尚未完成。请重试停止，确认后再发送；已受理的要求会保留。
      </p>}
      {reconciling ? <p className="v2-composer-caption" role="status">正在核对上次提交，避免重复执行。可以继续编辑，核对完成后再发送。</p>
        : executionQuery.data?.state === "stopping" && <p className="v2-composer-caption" role="status">正在停止执行。可以继续编辑，停止完成后再发送。</p>}
      {working && draft && onDraftChange && submittedContents.has(draft.trim()) &&
        <button className="v2-composer-chip" onClick={() => onDraftChange("")} type="button">编写下一条</button>}
      {detail.recovery && <V2ThreadRunRecovery recovery={detail.recovery}
        approvalSaved={narrative.some((entry) => recoveryRepresentsNotice(entry, detail.recovery))} />}
      {executionQuery.data?.state === "idle" && executionQuery.data.last_turn_interrupted &&
        <p className="v2-composer-caption" role="status">上次执行已中断。可以发送新消息继续，已完成的修改会保留。</p>}
      {view === "inspector" && <button className="v2-inspector-composer-toggle"
        aria-expanded={inspectorComposerOpen} onClick={() => setInspectorComposerOpen((open) => !open)} type="button">
        {inspectorComposerOpen ? "收起消息编辑器" : draft ? "继续编辑草稿" : "补充消息"}</button>}
      <div className="v2-composer-options" hidden={view === "inspector" && !inspectorComposerOpen}>
      {(canSteer || deliveryMode === "steer") && <label className="v2-composer-caption">发送方式
        <select aria-label="发送方式" value={deliveryMode} onChange={(event) => setDeliveryMode(event.target.value as "next_turn" | "steer")}>
          <option value="next_turn">下一轮处理</option>
          <option value="steer">更新当前任务（仅文字）</option>
        </select>
      </label>}
      {deliveryMode === "steer" && !canSteer && <p className="v2-composer-caption" role="alert">
        {steerUnavailable}，不能更新当前任务；纠正草稿会保留。
        <button className="v2-composer-chip" onClick={() => setDeliveryMode("next_turn")} type="button">切换为下一轮处理</button>
      </p>}
      </div>
      <div className="v2-queue-composer-stack">
      {currentRun.session_id && <V2QueuedMessages client={client} threadID={threadID} runID={currentRun.id} sessionID={currentRun.session_id}
        workspaceID={detail.thread.workspace_id ?? ""} running={working || runActive}
        canPromote={client.hasThreadExecutionRead === true
          ? executionQuery.isSuccess && executionQuery.data.state === "running" &&
            executionQuery.data.execution_id === queueQuery.data?.execution_id
          : !executionQuery.isError && canSteer} />}
      <div className="v2-shared-composer" ref={composerContainerRef} data-thread-id={threadID} hidden={view === "inspector" && !inspectorComposerOpen}>
      <V2Composer client={client} disabled={!client.hasThreadControl ||
        detail.thread.status !== "active"}
        submitDisabled={reconciling || executionQuery.data?.state === "stopping" ||
          executionQuery.data?.state === "stop_failed"}
        draft={draft} onDraftChange={onDraftChange}
        threadControls={(hasUnsentDraft) => <V2ThreadPlanControl client={client} threadID={threadID} runID={currentRun.id}
          active={detail.thread.status === "active"} working={working || turnSubmitting || reconciling}
          hasUnsentDraft={hasUnsentDraft}
          onRequestChange={appendDraftAndReveal} />}
        confirmedSubmission={confirmedSubmission}
        presentedSubmissionErrors={failedSubmissions.map(({ input }) => input)}
        fileReferenceUnavailableReason={fileReferenceUnavailableReason}
        key={threadID}
        onManageModels={onManageModels} onSubmit={send} onWorkspaceChange={() => undefined}
        placeholder="输入消息…" runActive={runActive}
        runID={currentRun.id} threadID={threadID} workspaceID={detail.thread.workspace_id ?? ""}
        workspaces={workspaces} />
      <small className="v2-composer-caption">{workspace?.name ?? "本地工作区"} · {working
        ? deliveryMode === "steer"
          ? "文字纠正会进入当前任务后续模型请求；已经开始的操作会保留。"
          : "发送后将在下一轮处理，进度会显示在对话中。"
        : "Ctrl + Enter 发送，Shift + Enter 换行"}</small>
      </div>
      </div>
    </div>
    </div>
    {docked && companion}
    </div>
  </section>;
}
