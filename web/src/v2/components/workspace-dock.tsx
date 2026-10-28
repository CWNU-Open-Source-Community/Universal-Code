import { lazy, useEffect, useRef, useState, type KeyboardEvent, type PointerEvent, type RefObject } from "react";
import { createPortal } from "react-dom";
import { useQuery } from "@tanstack/react-query";
import {
  FileCode2,
  FileDiff,
  FolderTree,
  LoaderCircle,
  Maximize2,
  Minimize2,
  PanelRightClose,
  PanelTop,
  Plus,
  SquareTerminal,
  X,
} from "lucide-react";
import type { APIClient } from "../../api/client";
import { readThreadReview } from "../../api/task-delivery";
import type { ThreadDetailView, WorkspaceView } from "../../api/types";
import type { FileEditReviewTarget } from "../../components/file-edit-panel";
import { useFileEditLeave } from "../../components/file-edit-drafts";
import { UserTerminalPanel } from "../../components/user-terminal-panel";
import { WorkspaceExplorer } from "../../components/workspace-explorer";
import { useModalFocusTrap } from "../../hooks/use-modal-focus-trap";
import { desktopUserTerminalEnabled } from "../../lib/desktop-bridge";
import { useLocale } from "../../lib/locale";
import { localUIMessage } from "../../lib/local-ui-message";
import type { ApplicationPreviewRequest } from "../application-preview-request";
import type { ReviewCommentResult } from "../review-comment-draft";
import {
  addWorkspaceFileTab,
  clampWorkspaceWidth,
  clearWorkspaceNotice,
  closeWorkspaceTab,
  defaultWorkspaceWidth,
  focusWorkspaceSingletonTab,
  maxWorkspaceWidth,
  minWorkspaceWidth,
  selectWorkspaceTab,
  setWorkspaceTerminalSession,
  setWorkspaceTerminalShell,
  setWorkspaceWidth,
  updateWorkspaceFileTabPath,
  workspaceCollapseAriaLabel,
  workspaceSurfaceAriaLabel,
  workspaceTabTitle,
  workspaceTabTooltip,
  type ThreadWorkspaceState,
  type WorkspaceTab,
} from "../workspace-tabs";
import { V2IdentityDisclosure } from "./identity-disclosure";
import { V2LazySurface } from "./lazy-surface";
import type { TaskReviewToolMemory } from "./task-review-tools";
import "./workspace-dock.css";

const V2TaskReview = lazy(() => import("./task-review").then((module) => ({ default: module.V2TaskReview })));
const V2ApplicationPreview = lazy(() =>
  import("./application-preview").then((module) => ({ default: module.V2ApplicationPreview })));

function sanitizeDomToken(value: string): string {
  return value.replace(/[^a-zA-Z0-9_-]/gu, "-");
}

function tabButtonID(threadID: string, tabID: string): string {
  return `v2-ws-tab-${sanitizeDomToken(threadID)}-${sanitizeDomToken(tabID)}`;
}

function tabPanelID(threadID: string, tabID: string): string {
  return `v2-ws-panel-${sanitizeDomToken(threadID)}-${sanitizeDomToken(tabID)}`;
}

function WorkspaceTabIcon({ tab }: { tab: WorkspaceTab }) {
  switch (tab.kind) {
    case "file":
      return tab.path && tab.path !== "."
        ? <FileCode2 aria-hidden="true" size={14} />
        : <FolderTree aria-hidden="true" size={14} />;
    case "terminal":
      return <SquareTerminal aria-hidden="true" size={14} />;
    case "preview":
      return <PanelTop aria-hidden="true" size={14} />;
    case "review":
      return <FileDiff aria-hidden="true" size={14} />;
  }
}

export function V2FileWorkspaceContent({
  client,
  threadID,
  workspaceID: defaultWorkspaceID,
  runID,
  initialPath = ".",
  initialLine,
  onPathChange,
  onQuote,
  onReturnToDraft,
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
}) {
  const { t } = useLocale();
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
          throw new Error(t("此执行的文件目录当前不可用。", "The file directory for this run is currently unavailable."));
        }
        if (review.current_run_id === runID && review.target?.state === "available" && review.target.workspace_id) {
          return review.target.workspace_id;
        }
        const changeMatch = [...(review.applied_changes ?? []), ...(review.unapplied_changes ?? [])].find(
          (c) => c.run_id === runID && c.workspace_id
        );
        if (changeMatch?.workspace_id) return changeMatch.workspace_id;
      }
      throw new Error(t("尚未找到此执行的文件目录绑定。", "No file directory binding was found for this run."));
    },
    enabled: Boolean(runID || defaultWorkspaceID),
    staleTime: 30_000,
    retry: false,
  });

  const resolvedWorkspaceID = workspaceQuery.data ?? "";

  return <>
    {runID && <div className="v2-file-drawer-context">
      <p>{t("查看此执行绑定目录中的文件。", "View files in the directory bound to this run.")}</p>
      <V2IdentityDisclosure identity={runID} identityLabel={t("执行 ID", "Run ID")} summary={t("执行信息", "Run details")} />
    </div>}
    <div className="v2-file-drawer-body">
      {workspaceQuery.isLoading ? (
        <div className="v2-file-drawer-loading" role="status">
          <LoaderCircle className="spin" size={20} />
          <span>{t("正在确认工作区…", "Confirming workspace…")}</span>
        </div>
      ) : workspaceQuery.isError ? (
        <div className="v2-notice" role="alert">
          <p>{t("无法确认此执行的工作区，文件目录可能已清理或暂时无法读取。请重试确认后查看文件。", "The workspace for this run could not be confirmed. Its directory may have been cleaned up or may be temporarily unavailable. Retry before viewing files.")}</p>
          <button className="compact-command" disabled={workspaceQuery.isFetching} onClick={() => void workspaceQuery.refetch()} type="button">
            {t("重试工作区确认", "Retry workspace check")}
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
          onQuote={onQuote}
          onReturnToDraft={onReturnToDraft}
        />
      )}
    </div>
  </>;
}

export function V2WorkspaceDock({
  client,
  threadID,
  detail,
  currentRunID,
  workspaceName,
  working,
  docked,
  panelVisible,
  state,
  fullViewShortcutLabel,
  onToggleFullView,
  onResizeStateChange,
  onCollapse,
  returnFocusRef,
  canQuote,
  onAddComment,
  onReturnToDraft,
  onRequestChange,
  onOpenWorktree,
  previewStartRequest,
  canRequestPreviewStart,
  onRequestPreviewStart,
  reviewFileTarget,
  reviewToolTarget,
  reviewMemoryRef,
}: {
  client: APIClient;
  threadID: string;
  detail: ThreadDetailView;
  currentRunID: string;
  workspaceName: string;
  working: boolean;
  docked: boolean;
  panelVisible: boolean;
  state: ThreadWorkspaceState;
  fullViewShortcutLabel: string;
  onToggleFullView: () => void;
  onResizeStateChange?: (resizing: boolean, liveWidth: number | null) => void;
  onCollapse: () => void;
  returnFocusRef: RefObject<HTMLElement | null>;
  canQuote: boolean;
  onAddComment?: (content: string) => ReviewCommentResult;
  onReturnToDraft: () => void;
  onRequestChange: (content: string) => void;
  onOpenWorktree?: (workspace: WorkspaceView) => void;
  previewStartRequest?: ApplicationPreviewRequest | null;
  canRequestPreviewStart: boolean;
  onRequestPreviewStart: () => void;
  reviewFileTarget?: FileEditReviewTarget;
  reviewToolTarget?: { runID: string; tool: "github-review" };
  reviewMemoryRef?: RefObject<TaskReviewToolMemory>;
}) {
  const { t } = useLocale();
  const leaveFileEdit = useFileEditLeave();
  const collapseButtonRef = useRef<HTMLButtonElement>(null);
  const addAnchorRef = useRef<HTMLDivElement>(null);
  const addTriggerRef = useRef<HTMLButtonElement>(null);
  const firstMenuItemRef = useRef<HTMLButtonElement>(null);
  const tabTriggerRefs = useRef(new Map<string, HTMLButtonElement>());
  const [addMenuOpen, setAddMenuOpen] = useState(false);
  const dragRef = useRef<{
    pointerID: number;
    element: HTMLDivElement;
    originX: number;
    originWidth: number;
    preferred: number;
    maximum: number;
  } | null>(null);

  const activeTab = state.tabs.find((tab) => tab.id === state.activeTabID) ?? state.tabs.at(-1) ?? null;
  const collapsePanel = () => {
    leaveFileEdit(onCollapse);
  };

  const handleSelectTab = (tabID: string) => {
    if (tabID === activeTab?.id) return;
    if (activeTab?.kind === "file") {
      leaveFileEdit(() => selectWorkspaceTab(threadID, tabID));
      return;
    }
    selectWorkspaceTab(threadID, tabID);
  };

  const dialogRef = useModalFocusTrap<HTMLElement>(!docked && panelVisible, collapsePanel, false, collapseButtonRef, {
    isolateBackground: !docked && panelVisible,
    trapFocus: !docked && panelVisible,
    returnFocusRef,
  });

  useEffect(() => {
    setAddMenuOpen(false);
  }, [threadID, panelVisible]);

  useEffect(() => {
    if (!addMenuOpen) return;
    firstMenuItemRef.current?.focus();
    const closeOutside = (event: MouseEvent) => {
      const target = event.target;
      if (!(target instanceof Node) || addAnchorRef.current?.contains(target)) return;
      setAddMenuOpen(false);
    };
    const closeEscape = (event: globalThis.KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      event.stopPropagation();
      setAddMenuOpen(false);
      addTriggerRef.current?.focus();
    };
    document.addEventListener("click", closeOutside);
    document.addEventListener("keydown", closeEscape, true);
    return () => {
      document.removeEventListener("click", closeOutside);
      document.removeEventListener("keydown", closeEscape, true);
    };
  }, [addMenuOpen]);

  const cancelResize = () => {
    if (!dragRef.current) return;
    const current = dragRef.current;
    dragRef.current = null;
    onResizeStateChange?.(false, null);
    if (current.element.hasPointerCapture(current.pointerID)) {
      current.element.releasePointerCapture(current.pointerID);
    }
  };

  useEffect(() => {
    window.addEventListener("blur", cancelResize);
    window.addEventListener("resize", cancelResize);
    return () => {
      window.removeEventListener("blur", cancelResize);
      window.removeEventListener("resize", cancelResize);
    };
  }, []);

  const canResize = docked && panelVisible && !state.fullView;
  useEffect(() => {
    if (!canResize) cancelResize();
  }, [canResize]);

  if (!activeTab) return null;

  const computeMaxWidth = (element: HTMLElement | null) => {
    const stageWidth = element?.closest<HTMLElement>(".v2-conversation-stage")?.clientWidth ?? window.innerWidth;
    return Math.max(minWorkspaceWidth, Math.min(maxWorkspaceWidth, stageWidth - 340));
  };

  const onResizeKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.isDefaultPrevented() || event.nativeEvent.isComposing || event.ctrlKey || event.metaKey || event.altKey) return;
    if (event.key === "Escape" && dragRef.current) {
      event.preventDefault();
      event.stopPropagation();
      cancelResize();
      return;
    }
    if (dragRef.current) return;
    const maximum = computeMaxWidth(event.currentTarget);
    // Dragging the left edge further left widens the right workspace pane.
    const next = event.key === "ArrowLeft" ? state.width + 16
      : event.key === "ArrowRight" ? state.width - 16
        : event.key === "Home" ? minWorkspaceWidth
          : event.key === "End" ? maximum
            : event.key === "Enter" ? defaultWorkspaceWidth
              : undefined;
    if (next !== undefined) {
      event.preventDefault();
      setWorkspaceWidth(threadID, clampWorkspaceWidth(next, maximum));
    }
  };

  const onResizePointerDown = (event: PointerEvent<HTMLDivElement>) => {
    if (event.button !== 0 || dragRef.current) return;
    event.preventDefault();
    event.currentTarget.focus();
    const maximum = computeMaxWidth(event.currentTarget);
    const currentWidth = clampWorkspaceWidth(state.width, maximum);
    dragRef.current = {
      pointerID: event.pointerId,
      element: event.currentTarget,
      originX: event.clientX,
      originWidth: currentWidth,
      preferred: state.width,
      maximum,
    };
    event.currentTarget.setPointerCapture(event.pointerId);
    onResizeStateChange?.(true, currentWidth);
  };

  const onResizePointerMove = (event: PointerEvent<HTMLDivElement>) => {
    const current = dragRef.current;
    if (!current || current.pointerID !== event.pointerId) return;
    const next = clampWorkspaceWidth(current.originWidth + (current.originX - event.clientX), current.maximum);
    onResizeStateChange?.(true, next);
  };

  const onResizePointerUp = (event: PointerEvent<HTMLDivElement>) => {
    const current = dragRef.current;
    if (!current || current.pointerID !== event.pointerId) return;
    const next = clampWorkspaceWidth(current.originWidth + (current.originX - event.clientX), current.maximum);
    dragRef.current = null;
    onResizeStateChange?.(false, null);
    if (next !== current.originWidth) {
      setWorkspaceWidth(threadID, next);
    }
    if (event.currentTarget.hasPointerCapture(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId);
    }
  };

  const handleCloseTab = (tab: WorkspaceTab) => {
    const performClose = () => {
      const index = state.tabs.findIndex((item) => item.id === tab.id);
      if (index === -1) return;
      if (state.tabs.length === 1) {
        closeWorkspaceTab(threadID, tab.id);
        onCollapse();
        return;
      }
      const nextFocusID = tab.id === activeTab.id
        ? (state.tabs[index + 1]?.id ?? state.tabs[index - 1]?.id ?? null)
        : activeTab.id;
      closeWorkspaceTab(threadID, tab.id);
      if (nextFocusID) {
        requestAnimationFrame(() => tabTriggerRefs.current.get(nextFocusID)?.focus());
      }
    };
    if (tab.kind === "file") {
      leaveFileEdit(performClose);
      return;
    }
    performClose();
  };

  const handleTabKeyDown = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    if (event.altKey || event.ctrlKey || event.metaKey) return;
    const count = state.tabs.length;
    if (count <= 1) return;
    const targetIndex = event.key === "ArrowRight" ? (index + 1) % count
      : event.key === "ArrowLeft" ? (index - 1 + count) % count
        : event.key === "Home" ? 0
          : event.key === "End" ? count - 1
            : -1;
    if (targetIndex === -1) return;
    event.preventDefault();
    const target = state.tabs[targetIndex];
    handleSelectTab(target.id);
    requestAnimationFrame(() => tabTriggerRefs.current.get(target.id)?.focus());
  };

  const terminalAvailable = desktopUserTerminalEnabled();
  const hasTerminalTab = state.tabs.some((tab) => tab.id === "terminal");
  const fullViewLabel = state.fullView
    ? t("退出完整视图", "Exit full view")
    : t("进入完整视图", "Enter full view");
  const surfaceLabel = workspaceSurfaceAriaLabel(activeTab?.kind ?? "file", t);
  const collapseLabel = workspaceCollapseAriaLabel(activeTab?.kind ?? "file", t);

  const content = (
    <section
      aria-label={surfaceLabel}
      aria-modal={docked ? undefined : "true"}
      className={`v2-workspace-dock${docked ? " is-docked" : ""}${docked && state.fullView ? " is-full-view" : ""}${activeTab?.kind === "file" ? " v2-file-drawer" : activeTab?.kind === "terminal" ? " v2-terminal-drawer" : ""}`}
      data-active-kind={activeTab?.kind ?? "file"}
      onKeyDown={(event) => {
        if (!docked || event.key !== "Escape" || event.isDefaultPrevented() || event.nativeEvent.isComposing) return;
        if (event.target instanceof Element && event.target.closest(".xterm")) return;
        event.stopPropagation();
        collapsePanel();
      }}
      ref={dialogRef}
      role={docked ? "complementary" : "dialog"}
      tabIndex={-1}
    >
      <header className="v2-workspace-tabbar">
        <div className="v2-workspace-tabs-track">
          <div aria-label={t("右侧工作区标签", "Workspace tabs")} className="v2-workspace-tablist" role="tablist">
            {state.tabs.map((tab, index) => {
              const isActive = tab.id === activeTab.id;
              const title = workspaceTabTitle(tab, t);
              const tooltip = workspaceTabTooltip(tab, workspaceName, t);
              return (
                <div className="v2-workspace-tab-item" data-active={isActive} data-tab-kind={tab.kind} key={tab.id}>
                  <button
                    aria-controls={tabPanelID(threadID, tab.id)}
                    aria-pressed={isActive}
                    aria-selected={isActive}
                    className="v2-workspace-tab-trigger"
                    id={tabButtonID(threadID, tab.id)}
                    onAuxClick={(event) => {
                      if (event.button === 1) {
                        event.preventDefault();
                        handleCloseTab(tab);
                      }
                    }}
                    onClick={() => handleSelectTab(tab.id)}
                    onKeyDown={(event) => handleTabKeyDown(event, index)}
                    ref={(node) => {
                      if (node) tabTriggerRefs.current.set(tab.id, node);
                      else tabTriggerRefs.current.delete(tab.id);
                    }}
                    role="tab"
                    tabIndex={isActive ? 0 : -1}
                    title={tooltip}
                    type="button"
                  >
                    <WorkspaceTabIcon tab={tab} />
                    <span className="v2-workspace-tab-title">{title}</span>
                  </button>
                  <button
                    aria-label={t(`关闭标签 ${title}`, `Close tab ${title}`)}
                    className="v2-workspace-tab-close"
                    onClick={() => handleCloseTab(tab)}
                    tabIndex={isActive ? 0 : -1}
                    title={tab.kind === "terminal"
                      ? t("关闭终端标签（进程继续运行）", "Close terminal tab (process keeps running)")
                      : t("关闭标签", "Close tab")}
                    type="button"
                  >
                    <X aria-hidden="true" size={13} />
                  </button>
                </div>
              );
            })}
          </div>
          <div className="v2-workspace-add-anchor" ref={addAnchorRef}>
            <button
              aria-expanded={addMenuOpen}
              aria-haspopup="menu"
              aria-label={t("新建或切换工作区标签", "Open workspace tab")}
              className="v2-workspace-icon-btn"
              onClick={() => setAddMenuOpen((open) => !open)}
              ref={addTriggerRef}
              title={t("打开标签", "Open tab")}
              type="button"
            >
              <Plus aria-hidden="true" size={15} />
            </button>
            {addMenuOpen && (
              <div className="v2-workspace-add-menu" role="menu">
                <button
                  onClick={() => {
                    setAddMenuOpen(false);
                    if (activeTab?.kind === "file") {
                      leaveFileEdit(() => addWorkspaceFileTab(threadID, currentRunID));
                      return;
                    }
                    addWorkspaceFileTab(threadID, currentRunID);
                  }}
                  ref={firstMenuItemRef}
                  role="menuitem"
                  type="button"
                >
                  <FolderTree aria-hidden="true" size={15} />
                  <span>{t("新建文件标签", "New file tab")}</span>
                </button>
                <button
                  onClick={() => {
                    setAddMenuOpen(false);
                    if (activeTab?.kind === "file") {
                      leaveFileEdit(() => focusWorkspaceSingletonTab(threadID, "terminal"));
                      return;
                    }
                    focusWorkspaceSingletonTab(threadID, "terminal");
                  }}
                  role="menuitem"
                  type="button"
                >
                  <SquareTerminal aria-hidden="true" size={15} />
                  <span>{t("终端", "Terminal")}</span>
                </button>
                <button
                  onClick={() => {
                    setAddMenuOpen(false);
                    if (activeTab?.kind === "file") {
                      leaveFileEdit(() => focusWorkspaceSingletonTab(threadID, "preview"));
                      return;
                    }
                    focusWorkspaceSingletonTab(threadID, "preview");
                  }}
                  role="menuitem"
                  type="button"
                >
                  <PanelTop aria-hidden="true" size={15} />
                  <span>{t("应用预览", "App preview")}</span>
                </button>
                <button
                  onClick={() => {
                    setAddMenuOpen(false);
                    if (activeTab?.kind === "file") {
                      leaveFileEdit(() => focusWorkspaceSingletonTab(threadID, "review"));
                      return;
                    }
                    focusWorkspaceSingletonTab(threadID, "review");
                  }}
                  role="menuitem"
                  type="button"
                >
                  <FileDiff aria-hidden="true" size={15} />
                  <span>{t("改动与交付", "Changes and delivery")}</span>
                </button>
              </div>
            )}
          </div>
        </div>
        <div className="v2-workspace-window-actions">
          {docked && (
            <button
              aria-label={fullViewLabel}
              aria-pressed={state.fullView}
              className="v2-workspace-icon-btn has-kbd-tooltip"
              onClick={onToggleFullView}
              type="button"
            >
              {state.fullView ? <Minimize2 aria-hidden="true" size={15} /> : <Maximize2 aria-hidden="true" size={15} />}
              <span aria-hidden="true" className="v2-workspace-kbd-tooltip">
                <span>{fullViewLabel}</span>
                {fullViewShortcutLabel && <kbd>{fullViewShortcutLabel}</kbd>}
              </span>
            </button>
          )}
          <button
            aria-label={collapseLabel}
            className="v2-workspace-icon-btn"
            onClick={collapsePanel}
            ref={collapseButtonRef}
            title={t("收起面板（保留已打开标签与运行中的终端）", "Collapse panel (keeps open tabs and running terminals)")}
            type="button"
          >
            {docked ? <PanelRightClose aria-hidden="true" size={16} /> : <X aria-hidden="true" size={16} />}
          </button>
        </div>
      </header>
      {state.notice && (
        <div className="v2-workspace-notice" role="status">
          <span>{localUIMessage(state.notice, t)}</span>
          <button
            aria-label={t("关闭提示", "Dismiss notice")}
            className="v2-workspace-notice-close"
            onClick={() => clearWorkspaceNotice(threadID)}
            type="button"
          >
            <X aria-hidden="true" size={13} />
          </button>
        </div>
      )}
      <div className="v2-workspace-dock-body">
        {state.tabs.filter((tab) => tab.kind === "file").map((tab) => (
          <div
            aria-labelledby={tabButtonID(threadID, tab.id)}
            className="v2-workspace-tabpanel"
            hidden={tab.id !== activeTab.id}
            id={tabPanelID(threadID, tab.id)}
            key={tab.id}
            role="tabpanel"
          >
            <V2FileWorkspaceContent
              client={client}
              threadID={threadID}
              workspaceID={detail.thread.workspace_id ?? ""}
              runID={tab.runID || currentRunID}
              initialPath={tab.path}
              initialLine={tab.line}
              onPathChange={(nextPath) => updateWorkspaceFileTabPath(threadID, tab.id, nextPath)}
              onQuote={canQuote && onAddComment
                ? (content) => onAddComment(`${t("任务：", "Thread: ")}${threadID}\n${content}`)
                : undefined}
              onReturnToDraft={onReturnToDraft}
            />
          </div>
        ))}
        {hasTerminalTab && (
          <div
            aria-labelledby={tabButtonID(threadID, "terminal")}
            className="v2-workspace-tabpanel"
            hidden={activeTab.id !== "terminal"}
            id={tabPanelID(threadID, "terminal")}
            role="tabpanel"
          >
            <div className="v2-terminal-drawer-body">
              {terminalAvailable ? (
                <UserTerminalPanel
                  active={panelVisible && activeTab.id === "terminal"}
                  onQuote={canQuote && onAddComment
                    ? (content) => onAddComment(`${t("任务：", "Thread: ")}${threadID}\n${content}`)
                    : undefined}
                  onReturnToDraft={onReturnToDraft}
                  onSession={(sessionID, expectedSessionID) =>
                    setWorkspaceTerminalSession(threadID, sessionID, expectedSessionID)}
                  onShellChange={(shellName) => setWorkspaceTerminalShell(threadID, shellName)}
                  recoverable={state.hadTerminalSession}
                  runID={currentRunID}
                  sessionID={state.terminalSessionID}
                />
              ) : (
                <div className="v2-terminal-disabled-notice" role="status">
                  <SquareTerminal aria-hidden="true" size={24} />
                  <p>{t("当前运行环境未启用桌面终端。仅在 Universal-Code 桌面端运行时支持本机 Debug 终端。", "The desktop terminal is unavailable in this environment. Local debug terminals require the Universal-Code desktop app.")}</p>
                </div>
              )}
            </div>
          </div>
        )}
        {/*
          Preview and review surfaces are intentionally lazy mounted only when active & visible.
          This prevents background service polling, memory overhead, and duplicate DOM status
          announcements (e.g. composer vs preview startup banner). State continuity (targets,
          runIDs, review memory) is maintained at the conversation parent level.
        */}
        {panelVisible && activeTab.id === "preview" && (
          <div
            aria-labelledby={tabButtonID(threadID, "preview")}
            className="v2-workspace-tabpanel"
            id={tabPanelID(threadID, "preview")}
            role="tabpanel"
          >
            <V2LazySurface
              errorLabel={t("应用预览", "App preview")}
              loadingText={t("正在加载应用预览…", "Loading app preview…")}
              onDismiss={collapsePanel}
              resetKey={threadID}
            >
              <V2ApplicationPreview
                canRequestStart={canRequestPreviewStart}
                client={client}
                embedded
                key={threadID}
                onClose={collapsePanel}
                onRequestStart={onRequestPreviewStart}
                returnFocusRef={returnFocusRef}
                runID={currentRunID}
                startRequest={previewStartRequest}
                threadID={threadID}
              />
            </V2LazySurface>
          </div>
        )}
        {panelVisible && activeTab.id === "review" && (
          <div
            aria-labelledby={tabButtonID(threadID, "review")}
            className="v2-workspace-tabpanel"
            id={tabPanelID(threadID, "review")}
            role="tabpanel"
          >
            <V2LazySurface
              errorLabel={t("改动审阅", "Change review")}
              loadingText={t("正在加载改动审阅…", "Loading change review…")}
              onDismiss={collapsePanel}
              resetKey={threadID}
            >
              <V2TaskReview
                client={client}
                detail={detail}
                embedded
                initialFileTarget={reviewFileTarget}
                initialToolTarget={reviewToolTarget}
                key={threadID}
                onAddComment={canQuote ? onAddComment : undefined}
                onClose={collapsePanel}
                onOpenWorktree={onOpenWorktree}
                onRequestChange={onRequestChange}
                returnFocusRef={returnFocusRef}
                toolMemoryRef={reviewMemoryRef}
                working={working}
              />
            </V2LazySurface>
          </div>
        )}
      </div>
      <div
        aria-controls={docked ? `v2-workbench-slot-${sanitizeDomToken(threadID)}` : undefined}
        aria-label={t("调整右侧工作区宽度", "Resize workspace panel")}
        aria-orientation="vertical"
        aria-valuemax={maxWorkspaceWidth}
        aria-valuemin={minWorkspaceWidth}
        aria-valuenow={state.width}
        aria-valuetext={t(`${state.width} 像素`, `${state.width} pixels`)}
        className="v2-workspace-resize-handle"
        hidden={!canResize}
        onDoubleClick={() => setWorkspaceWidth(threadID, defaultWorkspaceWidth)}
        onKeyDown={onResizeKeyDown}
        onLostPointerCapture={(event) => {
          if (dragRef.current?.pointerID === event.pointerId) cancelResize();
        }}
        onPointerCancel={(event) => {
          if (dragRef.current?.pointerID === event.pointerId) cancelResize();
        }}
        onPointerDown={onResizePointerDown}
        onPointerMove={onResizePointerMove}
        onPointerUp={onResizePointerUp}
        role="separator"
        tabIndex={canResize ? 0 : -1}
        title={t("拖动调整右侧工作区宽度；双击或 Enter 恢复默认", "Drag to resize workspace panel; double-click or Enter to reset")}
      />
    </section>
  );

  return docked ? content : createPortal(
    <div
      className="v2-inspector-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) collapsePanel();
      }}
      role="presentation"
    >
      {content}
    </div>,
    document.body,
  );
}
