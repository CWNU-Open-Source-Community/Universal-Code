import { localUIMessage } from "../../lib/local-ui-message";
import { lazy, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type RefObject } from "react";
import { createPortal } from "react-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Archive, BookOpen, ChevronDown, CircleEllipsis, Download, FileDiff, Folder, FolderTree, LoaderCircle, MessagesSquare, Microscope, RefreshCw, ShieldCheck, PanelTop, SquareTerminal, X } from "lucide-react";
import { APIRequestError, type APIClient } from "../../api/client";
import { useLocale } from "../../lib/locale";
import type { ThreadDetailView, ThreadView, WorkspaceView } from "../../api/types";
import { usePublicModelStream } from "../../hooks/use-public-model-stream";
import { useRunEventStream } from "../../hooks/use-run-event-stream";
import { prepareThreadNarrative, projectLiveThreadNarrative, type NarrativeEntry } from "../projection/narrative";
import { useV2ThreadTranscript } from "../use-thread-transcript";
import { useV2ReplyReading } from "../use-reply-reading";
import type { V2ReplyReading } from "../use-thread-unread";
import { useV2SavedMessage } from "../use-saved-message";
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
import { applicationPreviewStartRequest, applicationPreviewRequestCopy, applicationPreviewRequestKey, applicationServicesQueryKey,
  trackApplicationPreviewSubmission, type ApplicationPreviewRequest } from "../application-preview-request";
import type { FileEditReviewTarget } from "../../components/file-edit-panel";
import type { V2RunPane } from "../navigation";
import type { TaskReviewToolMemory } from "./task-review-tools";
import { V2ThreadContext } from "./thread-context";
import { V2ComposerStats } from "./composer-stats";
import { V2ThreadPinPrompt } from "./thread-context-controls";
import { V2ThreadPlanControl } from "./thread-plan";
import { useV2ThreadSubmissions, useV2ThreadTurn, V2SubmissionError, V2RecoveredSubmissionError, removeV2Submission, v2TurnFailed, v2TurnOutcomeKnown, v2TurnWasNotQueued, type V2TurnInput } from "../use-thread-turn";
import { inspectV2SteeringRequest, inspectV2TurnRequest } from "../recovery-api";
import { useV2RecoveryStore } from "../recovery-storage";
import { settleRecoveryTurn } from "../recovery-session";
import { appendReviewComment, type ReviewCommentResult } from "../review-comment-draft";
import { assertV2DraftVersion, useV2DraftDocument } from "../draft-context";
import type { V2DraftVersion } from "../draft-version";
import { V2DraftConflict } from "./draft-conflict";
import { MCPInputCards } from "./mcp-input-cards";
import { V2ApprovalCards } from "./approval-cards";
import { V2Composer } from "./composer";
import { V2ThreadRunRecovery } from "./thread-run-recovery";
import { useV2ThreadExecution, V2ThreadExecutionControl, V2PausedThreadControl } from "./thread-execution-control";
import { useFileEditLeave } from "../../components/file-edit-drafts";
import { UserTerminalPanel, type TerminalQuoteResult } from "../../components/user-terminal-panel";
import { desktopUserTerminalEnabled } from "../../lib/desktop-bridge";
import { useModalFocusTrap } from "../../hooks/use-modal-focus-trap";
import { eventShortcut, shortcutLabel, useChatPreferences } from "../chat-preferences";
import {
  collapseWorkspacePanel,
  focusWorkspaceFiles,
  focusWorkspaceSingletonTab,
  openWorkspaceFile,
  setWorkspaceFullView,
  toggleWorkspaceFiles,
  toggleWorkspaceFullView,
  toggleWorkspaceSingletonTab,
  useThreadWorkspaceState,
} from "../workspace-tabs";
import { V2FileWorkspaceContent, V2WorkspaceDock } from "./workspace-dock";
import { V2ThreadExport } from "./thread-export";

export { parseProjectFileLink, type FileLinkTarget } from "./narrative";

const V2Inspector = lazy(() => import("./inspector").then((module) => ({ default: module.V2Inspector })));

export function V2FileDrawer({
  client,
  threadID,
  workspaceID: defaultWorkspaceID,
  runID,
  initialPath = ".",
  initialLine,
  onPathChange,
  onQuote,
  onReturnToDraft,
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
  onQuote?: (content: string) => ReviewCommentResult;
  onReturnToDraft?: () => void;
  onClose: () => void;
  returnFocusRef: RefObject<HTMLElement | null>;
  docked?: boolean;
}) {
  const { t } = useLocale();
  const closeButton = useRef<HTMLButtonElement>(null);
  const leaveFileEdit = useFileEditLeave();
  const onClose = () => leaveFileEdit(close);
  const dialog = useModalFocusTrap<HTMLElement>(!docked, onClose, false, closeButton, {
    isolateBackground: !docked,
    trapFocus: !docked,
    returnFocusRef,
  });

  const content = (
      <section
        aria-label={t("工作区文件", "Workspace files")}
        aria-modal={docked ? undefined : "true"}
        className={`v2-inspector-drawer v2-file-drawer${docked ? " is-docked" : ""}`}
        ref={dialog}
        role={docked ? "complementary" : "dialog"}
        onKeyDown={(event) => { if (docked && event.key === "Escape") { event.stopPropagation(); onClose(); } }}
        tabIndex={-1}
      >
        <header>
          <div className="v2-file-drawer-title">
            <FolderTree aria-hidden="true" size={17} />
            <strong>{t("工作区文件", "Workspace files")}</strong>
          </div>
          <button aria-label={t("关闭文件面板", "Close file panel")} onClick={onClose} ref={closeButton} type="button">
            <X aria-hidden="true" size={18} />
          </button>
        </header>
        <V2FileWorkspaceContent
          client={client}
          threadID={threadID}
          workspaceID={defaultWorkspaceID}
          runID={runID}
          initialPath={initialPath}
          initialLine={initialLine}
          onPathChange={onPathChange}
          onQuote={onQuote}
          onReturnToDraft={onReturnToDraft}
        />
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
  onQuote,
  onReturnToDraft,
  onClose,
  returnFocusRef,
  docked = false,
}: {
  runID: string;
  sessionID: string;
  threadTitle: string;
  workspaceName: string;
  onSession: (sessionID: string) => void;
  onQuote?: (content: string) => TerminalQuoteResult;
  onReturnToDraft?: () => void;
  onClose: () => void;
  returnFocusRef: RefObject<HTMLElement | null>;
  docked?: boolean;
}) {
  const { t } = useLocale();
  const closeButton = useRef<HTMLButtonElement>(null);
  const dialog = useModalFocusTrap<HTMLElement>(!docked, onClose, false, closeButton, {
    isolateBackground: !docked,
    trapFocus: !docked,
    returnFocusRef,
  });
  const terminalAvailable = desktopUserTerminalEnabled();

  const content = (
      <section
        aria-label={t("任务终端", "Task terminal")}
        aria-modal={docked ? undefined : "true"}
        className={`v2-inspector-drawer v2-terminal-drawer${docked ? " is-docked" : ""}`}
        onKeyDown={(event) => { if (docked && event.key === "Escape") { event.stopPropagation(); onClose(); } }}
        ref={dialog}
        role={docked ? "complementary" : "dialog"}
        tabIndex={-1}
      >
        <header className="v2-terminal-drawer-header">
          <div className="v2-terminal-drawer-title">
            <SquareTerminal aria-hidden="true" size={17} />
            <div className="v2-terminal-drawer-heading">
              <h3 title={threadTitle}>{t("终端", "Terminal")}</h3>
              <p title={workspaceName}>{workspaceName}</p>
            </div>
          </div>
          <div className="v2-terminal-header-actions">
            <button
              aria-label={t("收起终端", "Collapse terminal")}
              className="v2-terminal-action-btn"
              onClick={onClose}
              ref={closeButton}
              title={t("收起终端（后台进程保持运行）", "Collapse terminal (background processes keep running)")}
              type="button"
            >
              <ChevronDown aria-hidden="true" size={14} />
              <span>{t("收起", "Collapse")}</span>
            </button>
          </div>
        </header>
        <div className="v2-terminal-drawer-body">
          {terminalAvailable ? (
            <UserTerminalPanel runID={runID} sessionID={sessionID} onSession={onSession}
              onQuote={onQuote} onReturnToDraft={onReturnToDraft} />
          ) : (
            <div className="v2-terminal-disabled-notice" role="status">
              <SquareTerminal aria-hidden="true" size={24} />
              <p>{t("当前运行环境未启用桌面终端。仅在 Universal-Code 桌面端运行时支持本机 Debug 终端。", "The desktop terminal is unavailable in this environment. Local debug terminals require the Universal-Code desktop app.")}</p>
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

export function V2Conversation({ client, threadID, workspaces, onArchive, onManageModels, messageID = "", onExitMessage,
  onOpenInspector, draft: legacyDraft, onDraftChange: legacyDraftChange, view = "conversation", onOpenTool, onOpenInspectorHome, onOpenWorktree, onExitInspector,
  reviewEntry, onReviewEntryHandled, reviewMemoryRef, onBranchCreated, onOpenThread,
  replyReading, readRequest = 0, readingEnabled = true }: {
  client: APIClient;
  threadID: string;
  messageID?: string;
  onExitMessage?: () => void;
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
  onBranchCreated?: (thread: ThreadView) => void;
  onOpenThread?: (threadID: string) => void;
  replyReading?: V2ReplyReading;
  readRequest?: number;
  readingEnabled?: boolean;
}) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const branchReply = useCallback(async (sourceEventID: string, operationKey: string) => {
    const result = await client.postControl<{ version: string; thread: ThreadView; source_event_id: string }>(
      `/threads/${encodeURIComponent(threadID)}/branches`,
      { version: "thread_branch.v1", source_event_id: sourceEventID }, operationKey);
    if (result?.version !== "thread_branch.v1" || !result.thread?.id || result.thread.id === threadID || result.source_event_id !== sourceEventID) {
      throw new Error(t("无法确认新聊天，请重试。", "Could not confirm the new chat. Try again."));
    }
    void queryClient.invalidateQueries({ queryKey: v2QueryKeys.threads() });
    onBranchCreated?.(result.thread);
  }, [client, threadID, queryClient, onBranchCreated, t]);
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
  const [exportThreadID, setExportThreadID] = useState<string | null>(null);
  useEffect(() => setExportThreadID(null), [threadID]);
  const workspaceState = useThreadWorkspaceState(threadID);
  const activeWorkspaceTab = workspaceState.tabs.find((tab) => tab.id === workspaceState.activeTabID)
    ?? workspaceState.tabs.at(-1)
    ?? null;
  const chatPreferences = useChatPreferences();
  const fullViewShortcut = Object.values(chatPreferences.shortcuts).includes("Mod+Shift+F") ? "" : "Mod+Shift+F";
  const fullViewShortcutLabel = fullViewShortcut ? shortcutLabel(fullViewShortcut) : "";
  const [contextOpen, setContextOpen] = useState(false);
  const [roomForDock, setRoomForDock] = useState(false);
  const [compactPaneOpen, setCompactPaneOpen] = useState(false);
  const [dockResizing, setDockResizing] = useState(false);
  const [liveDockWidth, setLiveDockWidth] = useState<number | null>(null);
  const docked = view === "conversation" && roomForDock;
  const hasWorkspaceTabs = activeWorkspaceTab !== null;
  const dockVisible = docked && hasWorkspaceTabs && workspaceState.open;
  const modalVisible = !docked && hasWorkspaceTabs && compactPaneOpen;
  const workspaceVisible = dockVisible || modalVisible;
  const stageFullView = dockVisible && workspaceState.fullView;
  const leaveFileEdit = useFileEditLeave();
  const [reviewFileTarget, setReviewFileTarget] = useState<FileEditReviewTarget | undefined>();
  const [reviewToolTarget, setReviewToolTarget] = useState<{ runID: string; tool: "github-review" }>();
  const reviewReturnFocus = useRef<HTMLButtonElement | null>(null);
  const contextTrigger = useRef<HTMLButtonElement>(null);
  const contextReturnFocus = useRef<HTMLButtonElement | null>(null);
  const previewTrigger = useRef<HTMLButtonElement>(null);
  const [inspectorComposerOpen, setInspectorComposerOpen] = useState(false);
  const composerContainerRef = useRef<HTMLDivElement>(null);
  const mainColumnRef = useRef<HTMLDivElement>(null);
  const [composerFocusRequest, setComposerFocusRequest] = useState<{ threadID: string } | null>(null);
  const [confirmedSubmission, setConfirmedSubmission] = useState<V2TurnInput | null>(null);
  const reviewTrigger = useRef<HTMLButtonElement>(null);
  const fileTrigger = useRef<HTMLButtonElement>(null);
  const fileReturnFocus = useRef<HTMLElement | null>(null);
  const terminalTrigger = useRef<HTMLButtonElement>(null);
  const terminalReturnFocus = useRef<HTMLElement | null>(null);
  const terminalAvailable = desktopUserTerminalEnabled();
  const currentRunIDRef = useRef("");

  const openSingletonSurface = useCallback((kind: "terminal" | "preview" | "review") => {
    setContextOpen(false);
    const perform = () => {
      focusWorkspaceSingletonTab(threadID, kind);
      if (!docked) setCompactPaneOpen(true);
    };
    if (workspaceVisible && activeWorkspaceTab?.kind === "file") {
      leaveFileEdit(perform);
      return;
    }
    perform();
  }, [activeWorkspaceTab?.kind, docked, leaveFileEdit, threadID, workspaceVisible]);

  const toggleSingletonSurface = useCallback((kind: "terminal" | "preview" | "review") => {
    setContextOpen(false);
    if (docked) {
      if (workspaceVisible && activeWorkspaceTab?.kind === "file") {
        leaveFileEdit(() => toggleWorkspaceSingletonTab(threadID, kind));
        return;
      }
      toggleWorkspaceSingletonTab(threadID, kind);
      return;
    }
    if (modalVisible && activeWorkspaceTab?.kind === kind) {
      leaveFileEdit(() => {
        setCompactPaneOpen(false);
        collapseWorkspacePanel(threadID);
      });
      return;
    }
    leaveFileEdit(() => {
      focusWorkspaceSingletonTab(threadID, kind);
      setCompactPaneOpen(true);
    });
  }, [activeWorkspaceTab?.kind, docked, leaveFileEdit, modalVisible, threadID, workspaceVisible]);

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
    setContextOpen(false);
    const perform = () => {
      fileReturnFocus.current = triggerElement ?? fileTrigger.current;
      openWorkspaceFile(threadID, { path, line, runID }, currentRunIDRef.current || undefined);
      if (!docked) setCompactPaneOpen(true);
    };
    if (workspaceVisible && activeWorkspaceTab?.kind === "file") {
      leaveFileEdit(perform);
      return;
    }
    perform();
  }, [activeWorkspaceTab?.kind, docked, leaveFileEdit, threadID, workspaceVisible]);
  const turn = useV2ThreadTurn(client);
  const submissions = useV2ThreadSubmissions(threadID);
  const submitTrackedTurn = async (input: V2TurnInput) => {
    trackApplicationPreviewSubmission(queryClient, input, "submitting");
    try {
      const result = await turn.mutateAsync(input);
      trackApplicationPreviewSubmission(queryClient, input, "accepted", {
        runID: "run_id" in result ? result.run_id : undefined, messageID: result.steering.id,
      });
      const location = currentMessageLocation.current;
      if (messageID && location?.threadID === input.threadID && location.messageID === messageID) location.exit();
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
          ? t("服务端暂未找到原提交。请继续核对；确认需要重试时，可主动发送原消息。", "The original submission was not found on the server. Check again, or explicitly resend the original message if needed.")
          : t("原提交已登记，但消息尚未入队。可以继续核对，或主动继续提交原消息。", "The submission was recorded, but the message has not been queued. Check again or explicitly resume the original submission.") }));
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
      setObservations((current) => ({ ...current, [input.operationKey]: t("暂时无法读取原提交结果，原消息和新草稿均已保留。", "Could not read the original submission result. The original message and new draft are kept.") }));
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
  const stopButtonRef = useRef<HTMLButtonElement>(null);
  const conversationRef = useRef<HTMLElement>(null);
  const workbenchSlot = useRef<HTMLDivElement>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
  const menuTriggerRef = useRef<HTMLButtonElement>(null);
  const workflowFocusTarget = (target: HTMLButtonElement | null) => {
    const group = target?.closest(".v2-task-workflows");
    return group && getComputedStyle(group).display === "none" ? menuTriggerRef.current : target;
  };
  const contextFocusTarget = { get current() { return workflowFocusTarget(contextReturnFocus.current); } };
  const reviewFocusTarget = { get current() { return workflowFocusTarget(reviewReturnFocus.current); } };
  const workspaceFocusTarget: RefObject<HTMLElement | null> = {
    get current() {
      switch (activeWorkspaceTab?.kind) {
        case "file":
          return fileReturnFocus.current?.isConnected ? fileReturnFocus.current : fileTrigger.current;
        case "terminal":
          return terminalReturnFocus.current?.isConnected ? terminalReturnFocus.current : terminalTrigger.current;
        case "preview":
          return previewTrigger.current;
        case "review":
          return workflowFocusTarget(reviewReturnFocus.current?.isConnected ? reviewReturnFocus.current : reviewTrigger.current);
        default:
          return fileTrigger.current;
      }
    },
  };
  const menuRef = useRef<HTMLDivElement>(null);
  const firstMenuItemRef = useRef<HTMLButtonElement>(null);
  const olderScrollAnchorRef = useRef<{ threadID: string; height: number; top: number; pages: number;
    row?: HTMLElement; offset?: number } | null>(null);
  const readingThreadRef = useRef("");
  const followLatestRef = useRef(true);
  const locatedMessageRef = useRef<{ key: string; row: HTMLElement } | null>(null);
  const returnToLatestMessage = useCallback(() => {
    followLatestRef.current = true;
    readingThreadRef.current = "";
    queryClient.setQueryData(["v2", "reading", threadID], { top: 0, following: true });
    onExitMessage?.();
  }, [queryClient, threadID, onExitMessage]);
  const currentMessageLocation = useRef<{ threadID: string; messageID: string; exit: () => void } | null>(null);
  useLayoutEffect(() => {
    currentMessageLocation.current = { threadID, messageID, exit: returnToLatestMessage };
    return () => { currentMessageLocation.current = null; };
  }, [threadID, messageID, returnToLatestMessage]);
  const scrollSizeRef = useRef<{ content: number; viewport: number } | null>(null);
  const [hasNewContent, setHasNewContent] = useState(false);
  const [awayFromLatest, setAwayFromLatest] = useState(false);
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
      return optimistic.length > 0 ? 750 : detail?.thread.status === "active" ? 5_000 : false;
    },
  });
  const managedDraft = useV2DraftDocument(detailQuery.data?.thread.workspace_id ?? "", threadID);
  const draft = managedDraft?.state.snapshot.text ?? legacyDraft;
  const onDraftChange = managedDraft?.changeText ?? legacyDraftChange;
  const composeTarget = useRef({ draft, onDraftChange });
  composeTarget.current = { draft, onDraftChange };
  const composeMessage = useCallback((text: string) => {
    const current = composeTarget.current;
    current.onDraftChange?.(current.draft ? `${current.draft}\n\n${text}` : text, current.draft);
    requestAnimationFrame(() => document.querySelector<HTMLElement>('.v2-conversation .v2-composer :is(textarea, [contenteditable="true"][role="textbox"])')?.focus());
  }, []);
  const activeRun = detailQuery.data?.active_run;
  const streamRunID = activeRun?.id ?? "";
  const streamEnabled = Boolean(activeRun && ["preparing", "running", "waiting_approval"]
    .includes(activeRun.status));
  const queuedRun = activeRun;
  const queueBinding = queuedRun?.session_id ? { threadID, runID: queuedRun.id, sessionID: queuedRun.session_id,
    workspaceID: detailQuery.data?.thread.workspace_id ?? "" } : null;
  // The panel owns polling; this observer shares its validated snapshots.
  const queueQuery = useV2QueuedMessagesQuery(client, queueBinding, false);
  const eventStream = useRunEventStream(client, streamEnabled ? streamRunID : "");
  const publicStream = usePublicModelStream(client, streamRunID, streamEnabled);
  const latestTranscriptQuery = useV2ThreadTranscript(client, threadID, {
    // Events drive ordinary updates. A slower head-only check also covers a
    // quiet connection or a durable write that follows its notification.
    refetchInterval: optimistic.length > 0 ? 750 : streamEnabled
      ? eventStream.status === "live" ? 10_000 : 1_200 : detailQuery.data?.thread.status === "active" ? 5_000 : false,
  });
  const savedMessageQuery = useV2SavedMessage(client, threadID, messageID);
  const transcriptQuery = messageID ? savedMessageQuery : latestTranscriptQuery;
  const refreshTranscript = latestTranscriptQuery.refetch;
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
  const preparedNarrative = useMemo(() => prepareThreadNarrative(transcriptItems, t), [transcriptItems, t]);
  const durableNarrative = preparedNarrative.entries;
  const liveNarrative = useMemo(() => messageID ? durableNarrative : projectLiveThreadNarrative(preparedNarrative, {
    runId: streamRunID, snapshot: liveSnapshot, status: publicStream.status,
  }), [messageID, durableNarrative, liveSnapshot, publicStream.status, streamRunID, preparedNarrative]);
  const existingUserText = useMemo(() => new Set(durableNarrative.filter((entry) => entry.kind === "user")
    .map((entry) => JSON.stringify([entry.text, imageIdentities(entry.images), fileAttachmentIdentities(entry.attachments)]))),
  [durableNarrative]);
  const narrative = useMemo(() => {
    if (messageID) return durableNarrative;
    const pending: NarrativeEntry[] = optimistic.filter(({ text, images, attachments }) => !existingUserText.has(JSON.stringify([text, imageIdentities(images), fileAttachmentIdentities(attachments)])))
      .map((entry) => ({ id: entry.id, kind: "user", text: entry.text,
        images: entry.images, attachments: entry.attachments, createdAt: entry.createdAt, provisional: true }));
    return pending.length ? [...liveNarrative, ...pending] : liveNarrative;
  }, [messageID, durableNarrative, liveNarrative, existingUserText, optimistic]);
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
      scrollSizeRef.current = { content: element.scrollHeight, viewport: element.clientHeight };
      setHasNewContent(false);
      setAwayFromLatest(element.scrollHeight - element.clientHeight - element.scrollTop > 48);
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

  useLayoutEffect(() => {
    const location = messageID && view === "conversation" ? `${threadID}:${messageID}` : "";
    if (locatedMessageRef.current?.key === location) return;
    locatedMessageRef.current?.row.classList.remove("is-search-target");
    if (!location) { locatedMessageRef.current = null; return; }
    const row = [...(scrollRef.current?.querySelectorAll<HTMLElement>("[data-message-ids]") ?? [])]
      .find((element) => element.dataset.messageIds?.split(" ").includes(messageID));
    if (!row) return;
    followLatestRef.current = false;
    row.scrollIntoView?.({ block: "start" });
    row.focus({ preventScroll: true });
    row.classList.add("is-search-target");
    locatedMessageRef.current = { key: location, row };
  }, [threadID, messageID, visibleNarrative, view, detailQuery.isLoading]);

  useLayoutEffect(() => {
    const element = scrollRef.current;
    const content = element?.firstElementChild;
    if (view !== "conversation" || !element || !content || typeof ResizeObserver === "undefined") return;
    // Composer growth and asynchronously loaded cards change the viewport or
    // content without changing the transcript. Keep the latest content visible
    // only while following it; leave historical reading/pagination anchored.
    const observer = new ResizeObserver(() => {
      if (readingThreadRef.current === threadID && followLatestRef.current &&
        olderScrollAnchorRef.current?.threadID !== threadID) element.scrollTop = element.scrollHeight;
      scrollSizeRef.current = { content: element.scrollHeight, viewport: element.clientHeight };
      setAwayFromLatest(element.scrollHeight - element.clientHeight - element.scrollTop > 48);
    });
    observer.observe(element);
    observer.observe(content);
    return () => observer.disconnect();
  }, [detailQuery.isSuccess, threadID, view]);

  useEffect(() => {
    setMenuOpen(false);
    setContextOpen(false);
    setCompactPaneOpen(false);
    setDockResizing(false);
    setLiveDockWidth(null);
    setReviewFileTarget(undefined);
    setReviewToolTarget(undefined);
    setComposerFocusRequest(null);
    olderScrollAnchorRef.current = null;
  }, [threadID, view]);
  const reportReplyReading = useV2ReplyReading({ threadID, request: readRequest, reading: replyReading,
    readable: readingEnabled && view === "conversation" && !messageID && !contextOpen && !modalVisible && !stageFullView,
    ready: detailQuery.isSuccess && !latestTranscriptQuery.isLoading && !latestTranscriptQuery.isError,
    content: visibleNarrative, scroll: scrollRef,
    following: () => readingThreadRef.current === threadID && followLatestRef.current && olderScrollAnchorRef.current === null,
    refresh: refreshTranscript });
  useEffect(() => {
    if (!reviewEntry || reviewEntry.threadID !== threadID) return;
    setReviewFileTarget(undefined);
    setReviewToolTarget({ runID: reviewEntry.runID, tool: "github-review" });
    reviewReturnFocus.current = reviewTrigger.current;
    openSingletonSurface("review");
    onReviewEntryHandled?.(reviewEntry.requestID);
  }, [openSingletonSurface, reviewEntry?.requestID, threadID]);
  useEffect(() => {
    if (!composerFocusRequest || composerFocusRequest.threadID !== threadID ||
      contextOpen || modalVisible || stageFullView ||
      (view === "inspector" && !inspectorComposerOpen)) return;
    const frame = requestAnimationFrame(() => {
      const container = composerContainerRef.current;
      if (container?.dataset.threadId === composerFocusRequest.threadID && !container.hidden) {
        const compact = container.querySelector<HTMLElement>(".v2-composer-compact-trigger");
        const target = compact?.getClientRects().length ? compact
          : container.querySelector<HTMLElement>('textarea, [contenteditable="true"][role="textbox"]');
        target?.focus();
      }
      setComposerFocusRequest((current) => current === composerFocusRequest ? null : current);
    });
    return () => cancelAnimationFrame(frame);
  }, [composerFocusRequest, threadID, contextOpen, modalVisible, stageFullView, view, inspectorComposerOpen]);
  useEffect(() => {
    if (!docked || !dockVisible || !fullViewShortcut) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.repeat || event.isComposing || event.keyCode === 229) return;
      if (document.querySelector('[role="dialog"][aria-modal="true"]')) return;
      if (eventShortcut(event) !== fullViewShortcut) return;
      event.preventDefault();
      toggleWorkspaceFullView(threadID);
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [docked, dockVisible, fullViewShortcut, threadID]);
  useEffect(() => {
    if (!stageFullView) return;
    if (mainColumnRef.current?.contains(document.activeElement)) {
      workbenchSlot.current?.querySelector<HTMLElement>('[role="tab"][aria-selected="true"]')?.focus();
    }
  }, [stageFullView]);
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
    const shell = container.closest<HTMLElement>(".v2-shell");
    const measure = () => {
      const fits = (shell ?? container).clientWidth > (shell?.classList.contains("has-sidebar") ? 960 : 720);
      if (!fits && workbenchSlot.current?.contains(document.activeElement)) {
        workspaceFocusTarget.current?.focus();
      }
      setRoomForDock(fits);
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(container);
    if (shell) observer.observe(shell);
    return () => observer.disconnect();
  }, [detailQuery.isSuccess]);

  useEffect(() => { if (docked) setCompactPaneOpen(false); }, [docked]);

  if (detailQuery.isLoading) return <div className="v2-main-loading"><LoaderCircle className="spin" size={20} />
    <span>{t("正在打开对话…", "Opening conversation…")}</span></div>;
  if (detailQuery.isError || !detailQuery.data) return <div className="v2-main-error" role="alert">
    <strong>{t("无法打开对话", "Unable to open conversation")}</strong><span>{t("请检查服务连接，然后重试。已有输入会保留。", "Check the service connection and retry. Your input is kept.")}</span>
    <button className="v2-retry-action" onClick={() => void detailQuery.refetch()} type="button"><RefreshCw aria-hidden="true" size={15} />{t("重新打开", "Reopen")}</button>
    <details><summary><ChevronDown aria-hidden="true" size={14} />{t("查看原因", "Show reason")}</summary>{detailQuery.error instanceof Error
      ? localUIMessage(detailQuery.error.message, t) : t("请求未返回对话数据", "No conversation data returned")}</details></div>;
  const detail = detailQuery.data;
  const currentRun = detail.active_run ?? detail.last_run;
  currentRunIDRef.current = currentRun.id;
  const workspace = workspaces.find(({ id }) => id === detail.thread.workspace_id);
  const runActive = executionQuery.data ? executionQuery.data.state !== "idle"
    : Boolean(detail.active_run && ["preparing", "running"].includes(detail.active_run.status));
  const modelActive = Boolean(liveSnapshot) &&
    (publicStream.status === "live" || publicStream.status === "finalizing");
  const working = turnSubmitting || modelActive || (!executionQuery.isError &&
    (executionQuery.data?.state === "running" || executionQuery.data?.state === "stopping"));
  const replyToPinProposal = async (content: string, operationKey: string) => {
    const previous = submissions.find((item) => item.input.operationKey === operationKey)?.input;
    if (previous && previous.content !== content) throw new Error(t("原答复身份与当前内容不一致，请重新读取。", "The original reply identity does not match the current content. Reload it."));
    await submitTrackedTurn(previous ?? { threadID, workspaceID: detail.thread.workspace_id ?? "",
      content, operationKey, createdAt: new Date().toISOString() });
  };
  const canSteer = executionQuery.data?.state === "running" ||
    (!executionQuery.data && detail.active_run?.status === "running");
  const confirmedExecution = client.hasThreadExecutionRead === true && !executionQuery.isError &&
    executionQuery.data?.thread_id === threadID ? executionQuery.data : undefined;
  const fileReferenceUnavailableReason = currentRun.status === "waiting_approval"
      ? t("当前任务正在等待批准，暂时不能新增文件引用。请先处理待批准的操作。", "This task is awaiting approval. Resolve pending approvals before adding file references.")
      : working || runActive ? modelActive || confirmedExecution?.state === "running" || confirmedExecution?.state === "stopping"
        ? t("项目内文件引用需等执行结束后发送；补充文字和上传附件可以排队。", "Send project file references after execution finishes. Text and uploaded attachments can be queued.")
        : turnSubmitting ? t("消息正在提交，暂时不能新增文件引用。已有输入会保留。", "A message is being submitted. Existing input is kept; file references cannot be added yet.")
          : confirmedExecution?.state === "stop_failed" ? t("停止尚未完成，暂时不能新增文件引用。请先重试停止。", "Stopping has not finished. Retry stopping before adding file references.")
            : t("活动状态尚未确认，暂时不能新增文件引用。已有输入会保留。", "The activity status is not yet confirmed. Existing input is kept; file references cannot be added yet.")
        : undefined;

  const send = async (content: string, files?: V2FileReference[], images?: WorkspaceImageAttachment[], draftVersion?: V2DraftVersion, attachments?: WorkspaceFileAttachment[], mcpReferences?: MCPDraftReference[]) => {
    const fingerprint = JSON.stringify(["next_turn", content, files ?? [], imageIdentities(images), fileAttachmentIdentities(attachments), mcpReferenceIdentities(mcpReferences)]);
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
          throw new V2SubmissionError(input, new Error(t("原提交尚未确认。新草稿已保留，请先核对或继续提交原消息。", "The original submission is unconfirmed. Your new draft is kept; check or retry the original message first.")));
        }
        replaced = input.operationKey;
      }
    }
    if (managedDraft && draftVersion) assertV2DraftVersion(managedDraft, draftVersion,
      { text: draft ?? content, files: files ?? [], images: images ?? [], attachments, mcpReferences });
    const operationKey = `v2-thread-turn-${globalThis.crypto.randomUUID()}`;
    const input: V2TurnInput = { threadID, workspaceID: detail.thread.workspace_id ?? "", content,
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
    transcript: transcriptItems }, t);
  const appendDraftAndReveal = (content: string) => {
    if (content) onDraftChange?.(draft?.trim() ? `${draft}\n\n${content}` : content);
    if (view === "inspector") setInspectorComposerOpen(true);
    setContextOpen(false);
    if (!docked) {
      setCompactPaneOpen(false);
      collapseWorkspacePanel(threadID);
    } else if (workspaceState.fullView) {
      setWorkspaceFullView(threadID, false);
    }
    setComposerFocusRequest({ threadID });
  };
  const addReviewComment = (content: string): ReviewCommentResult => {
    if (managedDraft) return appendReviewComment(managedDraft, content);
    const current = composeTarget.current;
    if (!current.onDraftChange) return "blocked";
    const next = current.draft ? `${current.draft}\n\n${content}` : content;
    current.onDraftChange(next, current.draft);
    composeTarget.current = { ...current, draft: next };
    return "added";
  };
  const handleCollapseWorkspace = () => {
    const focusInsideDock = Boolean(workbenchSlot.current?.contains(document.activeElement));
    setCompactPaneOpen(false);
    collapseWorkspacePanel(threadID);
    if (docked && focusInsideDock) {
      // Full view makes the whole chat column inert, including its header.
      // Restore focus after React has made that column interactive again.
      requestAnimationFrame(() => workspaceFocusTarget.current?.focus());
    }
  };
  const handlePreviewStartRequest = () => {
    if (!onDraftChange) return;
    const previousRequest = previewRequest.data;
    const request = previousRequest && ["draft", "rejected"].includes(previousRequest.phase)
      ? previousRequest.request : applicationPreviewStartRequest(t);
    if (!draft?.includes(request)) appendDraftAndReveal(request);
    else appendDraftAndReveal("");
    queryClient.setQueryData<ApplicationPreviewRequest>(applicationPreviewRequestKey(threadID), {
      request, phase: "draft",
    });
  };
  const effectiveDockWidth = liveDockWidth ?? workspaceState.width;
  const renderWorkspaceDock = (isDocked: boolean, isVisible: boolean) => (
    <V2WorkspaceDock
      canQuote={Boolean(onDraftChange)}
      canRequestPreviewStart={Boolean(onDraftChange)}
      client={client}
      currentRunID={currentRun.id}
      detail={detail}
      docked={isDocked}
      fullViewShortcutLabel={fullViewShortcutLabel}
      key={threadID}
      onAddComment={onDraftChange ? addReviewComment : undefined}
      onCollapse={handleCollapseWorkspace}
      onOpenWorktree={onOpenWorktree}
      onRequestChange={appendDraftAndReveal}
      onRequestPreviewStart={handlePreviewStartRequest}
      onResizeStateChange={(resizing, width) => {
        setDockResizing(resizing);
        setLiveDockWidth(width);
      }}
      onReturnToDraft={() => appendDraftAndReveal("")}
      onToggleFullView={() => toggleWorkspaceFullView(threadID)}
      panelVisible={isVisible}
      previewStartRequest={previewRequest.data}
      returnFocusRef={workspaceFocusTarget}
      reviewFileTarget={reviewFileTarget}
      reviewMemoryRef={reviewMemoryRef}
      reviewToolTarget={reviewToolTarget}
      state={workspaceState}
      threadID={threadID}
      working={working}
      workspaceName={workspace?.name ?? t("本地工作区", "Local workspace")}
    />
  );
  return <section className={`v2-conversation${view === "inspector" ? " is-inspector" : ""}`} ref={conversationRef}>
    <div className={`v2-conversation-stage${stageFullView ? " is-full-view" : ""}`}>
    <div className="v2-conversation-main-column" ref={mainColumnRef} aria-hidden={stageFullView || undefined} inert={stageFullView}>
    <header className="v2-conversation-header">
      <div><Folder aria-hidden="true" size={17} /><strong>{detail.thread.title}</strong></div>
      <div className="v2-header-actions">
        <V2ThreadExecutionControl client={client} execution={executionQuery.data}
          threadID={threadID} stopButtonRef={stopButtonRef} />
        <button aria-expanded={menuOpen} aria-haspopup="menu" aria-label={t("对话操作", "Conversation actions")}
          onClick={() => setMenuOpen((value) => !value)} ref={menuTriggerRef} type="button">
          <CircleEllipsis aria-hidden="true" size={18} />
        </button>
        {menuOpen && <div className="v2-thread-menu" ref={menuRef} role="menu">
          <button disabled={detail.thread.status !== "active" || !client.hasThreadControl} onClick={() => {
            setMenuOpen(false);
            menuTriggerRef.current?.focus();
            onArchive(detail.thread);
          }} ref={firstMenuItemRef} role="menuitem" type="button">
            <Archive aria-hidden="true" size={15} />{t("归档对话", "Archive conversation")}</button>
          <button onClick={() => { setMenuOpen(false); setExportThreadID(threadID); }} role="menuitem" type="button">
            <Download aria-hidden="true" size={15} />{t("导出对话", "Export conversation")}</button>
          <button className="v2-task-compact-action" onClick={() => {
            setMenuOpen(false); setReviewFileTarget(undefined); setReviewToolTarget(undefined);
            reviewReturnFocus.current = menuTriggerRef.current; openSingletonSurface("review");
          }} role="menuitem" type="button"><FileDiff aria-hidden="true" size={15} />{t("审阅改动", "Review changes")}</button>
          <button className="v2-task-compact-action" onClick={() => {
            setMenuOpen(false); contextReturnFocus.current = menuTriggerRef.current; setContextOpen(true);
          }} role="menuitem" type="button"><BookOpen aria-hidden="true" size={15} />{t("查看上下文", "View context")}</button>
          {view !== "inspector" && <button onClick={() => { setMenuOpen(false); onOpenInspector(menuTriggerRef.current); }}
            role="menuitem" type="button">
            <Microscope aria-hidden="true" size={15} />{t("打开 Inspector", "Open Inspector")}</button>}
          {view === "inspector" && onExitInspector && <button onClick={() => {
            setMenuOpen(false); menuTriggerRef.current?.focus(); onExitInspector(); }}
            role="menuitem" type="button">
            <MessagesSquare aria-hidden="true" size={15} />{t("返回对话视图", "Return to conversation")}</button>}
        </div>}
      </div>
    <nav className="v2-task-navigation" aria-label={t("任务工作区", "Task workspace")}>
      <div className="v2-task-workbench" role="group" aria-label={t("对话与工作台", "Conversation and workbench")}>
        <button aria-pressed={view === "conversation" && !workspaceVisible && !contextOpen} disabled={view === "inspector" && !onExitInspector}
          onClick={() => {
            const showConversation = () => {
              setContextOpen(false);
              setCompactPaneOpen(false);
              collapseWorkspacePanel(threadID);
              onExitInspector?.();
            };
            if (modalVisible || (workspaceVisible && activeWorkspaceTab?.kind === "file")) leaveFileEdit(showConversation);
            else showConversation();
          }} title={t("对话", "Conversation")} type="button"><MessagesSquare size={16} aria-hidden="true" /><span className="v2-task-nav-label">{t("对话", "Conversation")}</span></button>
        <button aria-label={t("工作区文件", "Workspace files")} aria-pressed={workspaceVisible && activeWorkspaceTab?.kind === "file"} onClick={() => {
          setContextOpen(false);
          fileReturnFocus.current = fileTrigger.current;
          if (docked) {
            if (workspaceVisible && activeWorkspaceTab?.kind === "file") {
              leaveFileEdit(() => toggleWorkspaceFiles(threadID, currentRun.id));
              return;
            }
            toggleWorkspaceFiles(threadID, currentRun.id);
            return;
          }
          if (modalVisible && activeWorkspaceTab?.kind === "file") {
            leaveFileEdit(() => {
              setCompactPaneOpen(false);
              collapseWorkspacePanel(threadID);
            });
            return;
          }
          focusWorkspaceFiles(threadID, currentRun.id);
          setCompactPaneOpen(true);
        }} ref={fileTrigger} title={t("工作区文件", "Workspace files")} type="button"><FolderTree aria-hidden="true" size={16} /><span className="v2-task-nav-label">{t("文件", "Files")}</span></button>
        <button aria-label={t("终端", "Terminal")} aria-pressed={workspaceVisible && activeWorkspaceTab?.kind === "terminal"} title={terminalAvailable ? t("打开任务终端", "Open task terminal") : t("桌面终端仅在桌面端可用", "The terminal is available in the desktop app")} onClick={() => {
          terminalReturnFocus.current = terminalTrigger.current;
          toggleSingletonSurface("terminal");
        }} ref={terminalTrigger} type="button"><SquareTerminal aria-hidden="true" size={16} /><span className="v2-task-nav-label">{t("终端", "Terminal")}</span></button>
        <button aria-label={t("应用预览", "App preview")} aria-pressed={workspaceVisible && activeWorkspaceTab?.kind === "preview"} onClick={() => toggleSingletonSurface("preview")}
          ref={previewTrigger} title={t("应用预览", "App preview")} type="button"><PanelTop aria-hidden="true" size={16} /><span className="v2-task-nav-label">{t("应用预览", "App preview")}</span></button>
      </div>
      <div className="v2-task-workflows" role="group" aria-label={t("任务流程", "Task workflow")}>
        <button aria-label={t("审阅改动", "Review changes")} aria-pressed={workspaceVisible && activeWorkspaceTab?.kind === "review"} onClick={() => {
          setReviewFileTarget(undefined);
          setReviewToolTarget(undefined);
          reviewReturnFocus.current = reviewTrigger.current;
          toggleSingletonSurface("review");
        }} ref={reviewTrigger} title={t("改动与交付", "Changes and delivery")} type="button"><FileDiff aria-hidden="true" size={16} /><span className="v2-task-nav-label">{t("改动与交付", "Changes and delivery")}</span></button>
        <button aria-label={t("查看上下文", "View context")} aria-pressed={contextOpen} onClick={() => {
          contextReturnFocus.current = contextTrigger.current;
          if (!docked && modalVisible) {
            leaveFileEdit(() => {
              setCompactPaneOpen(false);
              setContextOpen((open) => !open);
            });
            return;
          }
          setContextOpen((open) => !open);
        }}
          ref={contextTrigger} title={t("上下文与恢复", "Context and recovery")} type="button"><BookOpen aria-hidden="true" size={16} /><span className="v2-task-nav-label">{t("上下文与恢复", "Context and recovery")}</span></button>
        <button aria-pressed={view === "inspector" && !modalVisible && !contextOpen}
          onClick={(event) => {
            const trigger = event.currentTarget;
            const enterInspector = () => {
              setContextOpen(false);
              setCompactPaneOpen(false);
              onOpenInspector(trigger);
            };
            if (modalVisible) leaveFileEdit(enterInspector);
            else enterInspector();
          }} title={t("观察执行", "Observe execution")} type="button">
          <Microscope aria-hidden="true" size={16} /><span className="v2-task-nav-label">{t("观察执行", "Observe execution")}</span></button>
      </div>
    </nav>
    </header>
    {view === "inspector" && (onOpenTool || onOpenInspectorHome) && <nav className="v2-inspector-advanced" aria-label={t("执行观察工具", "Execution tools")}>
      {onOpenInspectorHome && <button onClick={onOpenInspectorHome} type="button">{t("全部运行与会话", "All runs and sessions")}</button>}
      {onOpenTool && <>
        <button onClick={() => onOpenTool("run", currentRun.id)} type="button">{t("运行与工具", "Runs and tools")}</button>
        <button onClick={() => onOpenTool("run", currentRun.id, "ui-evidence")} type="button">{t("界面观察证据", "UI evidence")}</button>
        <button onClick={() => onOpenTool("schedule", currentRun.id)} type="button">{t("自动化", "Automations")}</button>
      </>}
    </nav>}
    {view === "inspector" && !transcriptQuery.isLoading && <V2LazySurface loadingText={t("正在加载 Inspector…", "Loading Inspector…")}
      errorLabel="Inspector" resetKey={threadID} onDismiss={onExitInspector}><V2Inspector client={client} key={threadID}
      detail={detail} threadID={threadID} durableItems={transcriptItems}
      liveSnapshot={liveSnapshot} liveStatus={publicStream.status}
      hasOlder={Boolean(transcriptQuery.hasNextPage)} isFetchingOlder={transcriptQuery.isFetchingNextPage}
      onLoadOlder={() => void transcriptQuery.fetchNextPage()} /></V2LazySurface>}
    <div className="v2-conversation-scroll" ref={scrollRef} onScroll={(event) => {
      if (view !== "conversation") return;
      const element = event.currentTarget;
      const size = scrollSizeRef.current;
      // Replacing the editor can briefly expand the transcript, clamp its
      // scrollTop, then shrink it again before ResizeObserver runs. That scroll
      // event is a layout change, not a request to stop following the latest.
      if (followLatestRef.current && size && size.viewport > 0 && size.content === element.scrollHeight &&
        size.viewport !== element.clientHeight && olderScrollAnchorRef.current?.threadID !== threadID) element.scrollTop = element.scrollHeight;
      scrollSizeRef.current = { content: element.scrollHeight, viewport: element.clientHeight };
      const following = element.scrollHeight - element.clientHeight - element.scrollTop <= 48;
      followLatestRef.current = following;
      setAwayFromLatest(!following);
      if (following) setHasNewContent(false);
      queryClient.setQueryData(["v2", "reading", threadID], { top: element.scrollTop, following });
      reportReplyReading();
    }}>
      <div className="v2-conversation-content">
        {messageID && <p className="v2-notice v2-message-location" role="status">
          {t("正在查看搜索命中的消息及更早记录。", "Viewing the matching message and earlier history.")}
          <button type="button" onClick={returnToLatestMessage}>{t("返回最新消息", "Back to latest messages")}</button>
        </p>}
        {detail.thread.status === "archived" && <p className="v2-notice" role="status">
          {t("此对话已归档，仍可查看记录。请在设置的「已归档的聊天」中取消归档后继续发送。", "This conversation is archived. You can still view its history. Unarchive it in Settings → Archived chats to continue sending messages.")}
        </p>}
        {transcriptQuery.isLoading && <div className="v2-transcript-loading"><LoaderCircle className="spin" size={16} />
          {t("正在整理工作记录…", "Preparing work history…")}</div>}
        {view === "conversation" && transcriptQuery.hasNextPage && <div className="v2-transcript-loading">
          <button className="v2-composer-chip" disabled={transcriptQuery.isFetchingNextPage}
            onClick={() => void loadOlderTranscript()} type="button">
            {transcriptQuery.isFetchingNextPage
              ? <><LoaderCircle aria-hidden="true" className="spin" size={16} />{t("正在加载更早记录…", "Loading earlier history…")}</>
              : t("加载更早记录", "Load earlier history")}
          </button>
        </div>}
        {transcriptQuery.isFetchNextPageError && <div className="v2-notice tone-warning" role="alert">
          {t("更早的工作记录加载失败，请重试。", "Earlier work history failed to load. Please retry.")}
        </div>}
        {transcriptQuery.isError && !transcriptQuery.isFetchNextPageError && <div className="v2-notice tone-warning" role="alert">
          {t("工作记录加载失败，任务结果尚待核对。请重新读取记录。", "Work history failed to load. Reload it to confirm the task result.")}
          <button onClick={() => void transcriptQuery.refetch()} type="button">{t("重试工作记录", "Retry work history")}</button>
        </div>}
        {!client.hasThreadExecutionRead && <div className="v2-notice" role="status">
          {t("当前连接不提供 Agent 活动状态。对话与工作记录仍可查看。", "Agent activity is unavailable for this connection. Conversation and work history remain available.")}
        </div>}
        {executionQuery.isError && <div className="v2-notice tone-warning" role="alert">
          {t("Agent 的当前活动尚待确认。请刷新执行状态；已有对话内容仍可查看。", "Agent activity is not yet confirmed. Refresh execution status; existing conversation content remains available.")}
          <button onClick={() => void executionQuery.refetch()} type="button">{t("刷新执行状态", "Refresh execution status")}</button>
        </div>}
        {view === "conversation" && !transcriptQuery.isLoading && !transcriptQuery.isError && narrative.length === 0 && <div className="v2-transcript-empty">
          <span><ShieldCheck aria-hidden="true" size={18} /></span><p>{detail.thread.status === "archived"
            ? t("此归档对话没有公开进展记录。", "This archived conversation has no public progress history.") : t("任务已经创建。Agent 的公开进展会出现在这里。", "Task created. Agent progress will appear here.")}</p></div>}
        {view === "conversation" && <V2Narrative client={client} entries={visibleNarrative} threadID={threadID} workspaceID={detail.thread.workspace_id ?? ""} onOpenFile={openFile}
          onBranch={client.hasThreadControl && onBranchCreated ? branchReply : undefined} onOpenThread={onOpenThread}
          onCompose={client.hasThreadControl && onDraftChange && detail.thread.status !== "archived" ? composeMessage : undefined} />}
        {submissionNotices.map(({ input, error }) =>
          <div className="v2-notice tone-warning" key={input.operationKey} role="alert">
            <p>{v2TurnWasNotQueued(error) ? t("这条消息未入队，草稿与引用已保留。请重新选择、移除引用或处理当前限制后重试。", "This message was not queued. Your draft and references are kept. Update the references or resolve the current restrictions, then retry.")
              : v2TurnFailed(error) ? t("本轮执行未完成，失败记录与已完成的修改会保留。可以直接发送“继续”或补充要求。", "This turn did not finish. The failure history and completed changes are kept. Send “continue” or add instructions.")
              : observations[input.operationKey] ?? t("暂时无法确认这条消息的结果。草稿与引用已保留；发送新要求时会先核对原提交，也可以重试核对。", "The result of this message is not yet confirmed. Your draft and references are kept. Sending new instructions will first check the original submission; you can also retry the check.")}</p>
            <details><summary>{t("查看原因", "Show reason")}</summary>{error instanceof Error ? localUIMessage(error.message, t) : t("请求失败", "Request failed")}</details>
            {!v2TurnOutcomeKnown(error) &&
              <button disabled={turnSubmitting || checking.includes(input.operationKey)} onClick={() => {
                void confirmSubmission(input).catch(() => undefined);
              }} type="button">{checking.includes(input.operationKey) ? t("正在核对…", "Checking…") : t("重试核对", "Retry check")}</button>}
            {recovery && observations[input.operationKey] && !v2TurnOutcomeKnown(error) &&
              <button disabled={turnSubmitting || checking.includes(input.operationKey)} onClick={() => {
                void submitTrackedTurn(input).then(() => {
                  if (!managedDraft && !input.draftVersion && draft === (input.draft ?? input.content)) onDraftChange?.("", draft);
                }).catch(() => undefined);
              }} type="button">{t("继续提交原消息", "Continue original submission")}</button>}
            {onDraftChange && <button disabled={draft?.trim() === input.content.trim()}
              onClick={() => onDraftChange(draft?.trim() ? `${draft}\n\n${input.content}` : input.content)}
              type="button">{draft?.trim() ? t("添加到草稿", "Add to draft") : t("放回输入框", "Restore to editor")}</button>}
          </div>)}
        {(eventStream.error || publicStream.error) && working && <div className="v2-notice tone-warning"
          role="status">{t("实时进度暂不可用；持久工作记录仍会继续同步。", "Live progress is temporarily unavailable. Saved work history will continue to sync.")}</div>}
        {detail.active_run && <MCPInputCards client={client} runID={currentRun.id} />}
        {detail.active_run && <V2ApprovalCards client={client}
          runID={currentRun.id} threadID={threadID} onReviewFile={(target, trigger) => {
            reviewReturnFocus.current = trigger; setReviewToolTarget(undefined); setReviewFileTarget(target); openSingletonSurface("review");
          }} />}
        {detail.active_run && <div className="v2-command-approvals">
          <ControlledCommandProposalPanel client={client} runID={currentRun.id} threadID={threadID} />
          <HostCommandProposalPanel client={client} runID={currentRun.id} threadID={threadID} compact={view === "inspector"} />
        </div>}
      </div>
    </div>
    {view === "conversation" && !messageID && awayFromLatest && <button className="v2-jump-latest" onClick={() => {
      followLatestRef.current = true;
      if (scrollRef.current) {
        scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
        queryClient.setQueryData(["v2", "reading", threadID], { top: scrollRef.current.scrollTop, following: true });
      }
      setAwayFromLatest(false);
      setHasNewContent(false);
      reportReplyReading();
    }} type="button">{hasNewContent ? t("有新内容 · 回到最新", "New content · Jump to latest") : t("回到最新", "Jump to latest")}</button>}
    <div className="v2-composer-dock">
      {(agentActivity || currentRun.status === "paused") && <div className="v2-composer-status">
      <V2AgentActivity activity={agentActivity} />
      {currentRun.status === "paused" && <V2PausedThreadControl client={client} threadID={threadID} runID={currentRun.id} />}
      </div>}
      {previewRequest.data && <p className="v2-composer-caption" role={previewRequest.data.phase === "failed" ? "alert" : "status"}>
        {applicationPreviewRequestCopy(previewRequest.data, !previewServices.isError && Boolean(previewRequest.data.runID &&
          previewRequest.data.messageID && previewServices.data?.services.some((service) =>
            service.run_id === previewRequest.data?.runID && service.source_message_id === previewRequest.data?.messageID)), t)}
        <button onClick={() => openSingletonSurface("preview")} type="button">{t("查看应用服务", "View app services")}</button>
      </p>}
      {managedDraft && <V2DraftConflict key={threadID} client={client} workspaceID={detail.thread.workspace_id ?? ""}
        state={managedDraft.state} onResolve={(token, ref) => { managedDraft.document.resolve(managedDraft.scope, token, ref); }} />}
      {executionQuery.data?.state === "stop_failed" && <p className="v2-composer-caption" role="alert">
        {t("停止尚未完成。请重试停止，确认后再发送；已受理的要求会保留。", "Stopping has not finished. Retry stopping and confirm before sending. Accepted instructions are kept.")}
      </p>}
      {reconciling ? <p className="v2-composer-caption" role="status">{t("正在核对上次提交，避免重复执行。可以继续编辑，核对完成后再发送。", "Checking the previous submission to avoid duplicate execution. You can keep editing and send when the check finishes.")}</p>
        : executionQuery.data?.state === "stopping" && <p className="v2-composer-caption" role="status">{t("正在停止执行。可以继续编辑，停止完成后再发送。", "Stopping execution. You can keep editing and send when stopping finishes.")}</p>}
      {working && draft && onDraftChange && submittedContents.has(draft.trim()) &&
        <button className="v2-composer-chip" onClick={() => onDraftChange("")} type="button">{t("编写下一条", "Write next message")}</button>}
      {detail.recovery && <V2ThreadRunRecovery recovery={detail.recovery}
        approvalSaved={narrative.some((entry) => recoveryRepresentsNotice(entry, detail.recovery))} />}
      {executionQuery.data?.state === "idle" && executionQuery.data.last_turn_interrupted &&
        <p className="v2-composer-caption" role="status">{t("上次执行已中断。可以发送新消息继续，已完成的修改会保留。", "The previous execution was interrupted. Send a new message to continue; completed changes are kept.")}</p>}
      {view === "inspector" && <button className="v2-inspector-composer-toggle"
        aria-expanded={inspectorComposerOpen} onClick={() => setInspectorComposerOpen((open) => !open)} type="button">
        {inspectorComposerOpen ? t("收起消息编辑器", "Collapse message editor") : draft ? t("继续编辑草稿", "Continue editing draft") : t("补充消息", "Add a message")}</button>}
      {view === "conversation" && <V2AgentBrowser client={client} runID={currentRun.id} running={working || runActive}
        onDismiss={() => setComposerFocusRequest({ threadID })} />}
      {client.hasControl && <V2ThreadPinPrompt key={threadID} client={client} threadID={threadID}
        paused={working || contextOpen || modalVisible || stageFullView || menuOpen || view !== "conversation"} onReply={replyToPinProposal}
        onClose={() => setComposerFocusRequest({ threadID })} />}
      <div className="v2-queue-composer-stack">
      {queueBinding && <V2QueuedMessages client={client} {...queueBinding} running={working || runActive}
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
        onStop={() => {
          const button = stopButtonRef.current;
          if (!button || button.disabled) return false;
          button.click();
          return true;
        }}
        threadControls={(hasUnsentDraft) => <V2ThreadPlanControl client={client} threadID={threadID} runID={currentRun.id}
          active={detail.thread.status === "active"} working={working || turnSubmitting || reconciling}
          hasUnsentDraft={hasUnsentDraft}
          onRequestChange={appendDraftAndReveal} />}
        confirmedSubmission={confirmedSubmission}
        presentedSubmissionErrors={failedSubmissions.map(({ input }) => input)}
        fileReferenceUnavailableReason={fileReferenceUnavailableReason}
        key={threadID}
        onManageModels={onManageModels} onSubmit={send} onWorkspaceChange={() => undefined}
        placeholder={t("输入消息…", "Type a message…")} runActive={runActive}
        runID={currentRun.id} threadID={threadID} workspaceID={detail.thread.workspace_id ?? ""}
        workspaces={workspaces} />
      <V2ComposerStats key={`statistics:${threadID}`} statistics={detail.statistics} />
      </div>
      </div>
    </div>
    </div>
    {view === "conversation" && !modalVisible && <div
      aria-hidden={!dockVisible}
      className={`v2-workbench-slot${dockVisible ? " is-open" : ""}`}
      data-resizing={dockResizing ? "true" : undefined}
      id={`v2-workbench-slot-${threadID.replace(/[^a-zA-Z0-9_-]/gu, "-")}`}
      inert={!dockVisible}
      ref={workbenchSlot}
      style={{ "--v2-workbench-width": `${effectiveDockWidth}px` } as CSSProperties}
    >
      {hasWorkspaceTabs && renderWorkspaceDock(true, dockVisible)}
    </div>}
    </div>
    {contextOpen && <V2ThreadContext client={client} threadID={threadID} detail={detail}
      onReply={replyToPinProposal}
      onOpenRecovery={onOpenTool ? (pane) => { setContextOpen(false); onOpenTool("run", currentRun.id, pane); } : undefined}
      onOpenSession={onOpenTool && currentRun.session_id ? () => { setContextOpen(false); onOpenTool("session", currentRun.session_id); } : undefined}
      onClose={() => setContextOpen(false)} onRequestChange={appendDraftAndReveal} returnFocusRef={contextFocusTarget} />}
    {modalVisible && renderWorkspaceDock(false, true)}
    {exportThreadID === threadID && <V2ThreadExport key={threadID} client={client} threadID={threadID} title={detail.thread.title}
      returnFocusRef={menuTriggerRef} onClose={() => setExportThreadID(null)} />}
  </section>;
}
