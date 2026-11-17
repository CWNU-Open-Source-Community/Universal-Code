import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type RefObject,
} from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Check,
  ChevronDown,
  Cloud,
  Command,
  Database,
  FolderGit2,
  FolderOpen,
  GitBranch,
  Laptop,
  Paperclip,
  Plus,
  RotateCcw,
  SlidersHorizontal,
  Split,
  Wrench,
} from "lucide-react";
import type { APIClient } from "../../api/client";
import type { WorkspaceView } from "../../api/types";
import { useLocale, type LocaleText } from "../../lib/locale";
import { useV2PersistentState } from "../recovery-storage";
import type { V2WorkPhase } from "./phase-picker";
import "./composer-integrated-bar.css";

export type V2ExecutionEnvironment = "local" | "cloud";
export type V2CloudSandboxBackend = "sbx" | "docker";

export interface V2ComposerIntegratedState {
  targetBranch: string;
  createBranch: boolean;
  worktreeEnabled: boolean;
  worktreeName: string;
  environment: V2ExecutionEnvironment;
  cloudBackend: V2CloudSandboxBackend;
}

export interface V2ComposerDispatchContext {
  branch: string;
  currentRepoBranch: string;
  createBranch: boolean;
  worktree: {
    enabled: boolean;
    name: string;
    branch: string;
  };
  environment: V2ExecutionEnvironment;
  sandboxBackend: "local" | V2CloudSandboxBackend;
  hasOverrides: boolean;
}

export const defaultComposerIntegratedState: V2ComposerIntegratedState = {
  targetBranch: "",
  createBranch: false,
  worktreeEnabled: false,
  worktreeName: "",
  environment: "local",
  cloudBackend: "sbx",
};

export function normalizeComposerIntegratedState(raw: unknown): V2ComposerIntegratedState {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    return defaultComposerIntegratedState;
  }
  const record = raw as Record<string, unknown>;
  return {
    targetBranch: typeof record.targetBranch === "string" ? record.targetBranch.slice(0, 120) : "",
    createBranch: record.createBranch === true,
    worktreeEnabled: record.worktreeEnabled === true,
    worktreeName: typeof record.worktreeName === "string" ? record.worktreeName.slice(0, 80) : "",
    environment: record.environment === "cloud" ? "cloud" : "local",
    cloudBackend: record.cloudBackend === "docker" ? "docker" : "sbx",
  };
}

function sanitizeSlug(input: string): string {
  const cleaned = input
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/gu, "-")
    .replace(/^-+|-+$/gu, "")
    .slice(0, 36);
  return cleaned || "isolated-task";
}

export function deriveWorktreeSlug(state: V2ComposerIntegratedState, draftText: string): string {
  if (state.worktreeName.trim()) {
    return sanitizeSlug(state.worktreeName);
  }
  if (state.targetBranch.trim()) {
    const branchLeaf = state.targetBranch.trim().split("/").filter(Boolean).at(-1) ?? state.targetBranch;
    return `wt-${sanitizeSlug(branchLeaf)}`;
  }
  const asciiDraft = draftText
    .trim()
    .replace(/[^A-Za-z0-9\s_-]+/gu, " ")
    .trim();
  if (asciiDraft) {
    return `wt-${sanitizeSlug(asciiDraft)}`;
  }
  return "wt-isolated";
}

export function resolveComposerDispatchContext(
  state: V2ComposerIntegratedState,
  currentRepoBranch: string,
  draftText: string,
): V2ComposerDispatchContext {
  const repoBranch = currentRepoBranch.trim() || "main";
  const chosenBranch = state.targetBranch.trim();
  const worktreeSlug = deriveWorktreeSlug(state, draftText);
  const worktreeBranch = chosenBranch || `worktree/${worktreeSlug.replace(/^wt-/u, "")}`;
  const effectiveBranch = state.worktreeEnabled
    ? worktreeBranch
    : chosenBranch || repoBranch;
  const branchOverridden = chosenBranch !== "" && (state.createBranch || chosenBranch !== repoBranch);
  const hasOverrides = branchOverridden || state.worktreeEnabled || state.environment === "cloud";

  return {
    branch: effectiveBranch,
    currentRepoBranch: repoBranch,
    createBranch: state.createBranch || (state.worktreeEnabled && !chosenBranch),
    worktree: {
      enabled: state.worktreeEnabled,
      name: state.worktreeEnabled ? worktreeSlug : "",
      branch: state.worktreeEnabled ? worktreeBranch : "",
    },
    environment: state.environment,
    sandboxBackend: state.environment === "cloud" ? state.cloudBackend : "local",
    hasOverrides,
  };
}

export function formatComposerExecutionDirective(
  context: V2ComposerDispatchContext,
  t: LocaleText,
): string {
  if (!context.hasOverrides) return "";
  const parts: string[] = [];
  if (context.environment === "cloud") {
    parts.push(
      t(
        `运行环境: 云端沙盒 (${context.sandboxBackend})`,
        `Environment: Cloud sandbox (${context.sandboxBackend})`,
      ),
    );
  } else {
    parts.push(t("运行环境: 本地环境 (local)", "Environment: Local (local)"));
  }
  if (context.worktree.enabled) {
    parts.push(
      t(
        `独立工作树: ${context.worktree.name} (分支 ${context.worktree.branch})`,
        `Isolated worktree: ${context.worktree.name} (branch ${context.worktree.branch})`,
      ),
    );
  } else if (context.branch !== context.currentRepoBranch || context.createBranch) {
    parts.push(
      context.createBranch
        ? t(`Git 分支: 新建并切换 ${context.branch}`, `Git branch: create & switch ${context.branch}`)
        : t(`Git 分支: ${context.branch}`, `Git branch: ${context.branch}`),
    );
  }
  return `[${t("执行环境配置", "Execution Context")} · ${parts.join(" · ")}]`;
}

export function useV2ComposerIntegratedState(workspaceID: string, threadID: string) {
  const storageKey = `composer-integrated:${workspaceID || "local"}:${threadID || "new"}`;
  const [rawState, setRawState] = useV2PersistentState<V2ComposerIntegratedState>(
    storageKey,
    defaultComposerIntegratedState,
  );
  const state = useMemo(() => normalizeComposerIntegratedState(rawState), [rawState]);
  const updateState = (
    patch:
      | Partial<V2ComposerIntegratedState>
      | ((prev: V2ComposerIntegratedState) => V2ComposerIntegratedState),
  ) => {
    setRawState((previous) => {
      const normalized = normalizeComposerIntegratedState(previous);
      const next = typeof patch === "function" ? patch(normalized) : { ...normalized, ...patch };
      return normalizeComposerIntegratedState(next);
    });
  };
  return [state, updateState] as const;
}

export interface V2ComposerIntegratedBarProps {
  client: APIClient;
  workspaceID: string;
  threadID: string;
  runID?: string;
  workspaces: WorkspaceView[];
  disabled?: boolean;
  busy?: boolean;
  draftText: string;
  plainText: boolean;
  onToggleEditorMode: (trigger: HTMLElement | null) => void;
  state: V2ComposerIntegratedState;
  onStateChange: (
    patch:
      | Partial<V2ComposerIntegratedState>
      | ((prev: V2ComposerIntegratedState) => V2ComposerIntegratedState),
  ) => void;
  onResolvedBranchChange?: (repoBranch: string) => void;
  phase?: V2WorkPhase;
  onPhaseChange?: (phase: V2WorkPhase) => void;
  approvalMode?: "ask" | "auto" | "full";
  onUploadAttachment?: () => void;
  canUploadAttachment?: boolean;
  onOpenFileReference?: () => void;
  canReferenceFiles?: boolean;
  onOpenMCPResource?: () => void;
  onOpenMCPPrompt?: () => void;
  canUseMCP?: boolean;
  onOpenTaskConfiguration?: () => void;
  sideChatOpen: boolean;
  sideChatTurnCount: number;
  onToggleSideChat: () => void;
  sideChatTriggerRef: RefObject<HTMLButtonElement | null>;
}

export function V2ComposerIntegratedBar({
  client,
  workspaceID,
  threadID,
  runID,
  workspaces,
  disabled = false,
  busy = false,
  draftText,
  plainText,
  onToggleEditorMode,
  state,
  onStateChange,
  onResolvedBranchChange,
  phase,
  onPhaseChange,
  approvalMode,
  onUploadAttachment,
  canUploadAttachment = false,
  onOpenFileReference,
  canReferenceFiles = false,
  onOpenMCPResource,
  onOpenMCPPrompt,
  canUseMCP = false,
  onOpenTaskConfiguration,
  sideChatOpen,
  sideChatTurnCount,
  onToggleSideChat,
  sideChatTriggerRef,
}: V2ComposerIntegratedBarProps) {
  const { t } = useLocale();
  const toolbarRef = useRef<HTMLDivElement>(null);
  const branchTriggerRef = useRef<HTMLButtonElement>(null);
  const worktreePreviewTriggerRef = useRef<HTMLButtonElement>(null);
  const projectToolsTriggerRef = useRef<HTMLButtonElement>(null);
  const [openPopover, setOpenPopover] = useState<"branch" | "worktree" | "project" | null>(null);
  const [branchFilter, setBranchFilter] = useState("");
  const [activeStopIndex, setActiveStopIndex] = useState(0);

  const [savedPhase, setSavedPhase] = useV2PersistentState<unknown>(
    `new-thread-phase:${workspaceID}`,
    "deliver",
  );
  const effectivePhase: V2WorkPhase = phase ?? (savedPhase === "plan" ? "plan" : "deliver");
  const changePhase = (nextPhase: V2WorkPhase) => {
    if (onPhaseChange) onPhaseChange(nextPhase);
    else setSavedPhase(nextPhase);
  };

  const repoStateQuery = useQuery({
    queryKey: ["v2", "composer-repo-state", workspaceID],
    queryFn: ({ signal }) => client.repositoryState(workspaceID, signal),
    enabled: Boolean(workspaceID && typeof client.repositoryState === "function"),
    staleTime: 15_000,
    retry: false,
  });

  const repoHistoryQuery = useQuery({
    queryKey: ["v2", "composer-repo-history", workspaceID],
    queryFn: ({ signal }) => client.repositoryHistory(workspaceID, signal),
    enabled: Boolean(
      openPopover === "branch" && workspaceID && typeof client.repositoryHistory === "function",
    ),
    staleTime: 15_000,
    retry: false,
  });

  const projectQuery = useQuery({
    queryKey: ["v2", "composer-workspace-project", workspaceID],
    queryFn: ({ signal }) => client.getWorkspaceProject(workspaceID, signal),
    enabled: Boolean(
      openPopover === "project" &&
        workspaceID &&
        client.hasControl &&
        typeof client.getWorkspaceProject === "function",
    ),
    staleTime: 30_000,
    retry: false,
  });

  const sandboxQuery = useQuery({
    queryKey: ["sandbox", "environment"],
    queryFn: ({ signal }) => client.getSandboxEnvironment(signal),
    enabled: Boolean(
      (openPopover === "project" || state.environment === "cloud") &&
        typeof client.getSandboxEnvironment === "function",
    ),
    staleTime: 30_000,
    retry: false,
  });

  const permissionQuery = useQuery({
    queryKey: ["v2", "thread", threadID, "permission"],
    queryFn: ({ signal }) => client.getThreadExecutionPermission(threadID, signal),
    enabled: Boolean(
      openPopover === "project" &&
        threadID &&
        typeof client.getThreadExecutionPermission === "function",
    ),
    staleTime: 15_000,
    retry: false,
  });

  const skillsQuery = useQuery({
    queryKey: ["v2", "composer-skills-summary"],
    queryFn: ({ signal }) => client.listSkillPackages({}, signal),
    enabled: Boolean(openPopover === "project" && typeof client.listSkillPackages === "function"),
    staleTime: 30_000,
    retry: false,
  });

  const extensionsQuery = useQuery({
    queryKey: ["v2", "composer-extensions-summary", workspaceID, runID ?? ""],
    queryFn: ({ signal }) => client.extensionInventory(runID ?? "", signal, workspaceID),
    enabled: Boolean(openPopover === "project" && typeof client.extensionInventory === "function"),
    staleTime: 30_000,
    retry: false,
  });

  const repoData = repoStateQuery.data;
  const repoAvailable = repoData?.available === true;
  const currentRepoBranch = repoAvailable && repoData
    ? repoData.detached
      ? "HEAD"
      : repoData.branch || "main"
    : "main";

  useEffect(() => {
    onResolvedBranchChange?.(currentRepoBranch);
  }, [currentRepoBranch, onResolvedBranchChange]);

  const resolvedContext = useMemo(
    () => resolveComposerDispatchContext(state, currentRepoBranch, draftText),
    [state, currentRepoBranch, draftText],
  );

  const knownBranches = useMemo(() => {
    const names = new Set<string>();
    if (currentRepoBranch) names.add(currentRepoBranch);
    for (const item of repoHistoryQuery.data?.branches ?? []) {
      if (item.name) names.add(item.name);
    }
    if (!names.size) names.add("main");
    return [...names];
  }, [currentRepoBranch, repoHistoryQuery.data?.branches]);

  const trimmedFilter = branchFilter.trim();
  const filteredBranches = useMemo(() => {
    if (!trimmedFilter) return knownBranches;
    const needle = trimmedFilter.toLowerCase();
    return knownBranches.filter((name) => name.toLowerCase().includes(needle));
  }, [knownBranches, trimmedFilter]);

  useEffect(() => {
    if (disabled) setOpenPopover(null);
  }, [disabled]);

  useEffect(() => {
    if (!openPopover) return;
    const onPointerDown = (event: MouseEvent) => {
      if (!toolbarRef.current?.contains(event.target as Node)) {
        setOpenPopover(null);
      }
    };
    const onKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      event.stopPropagation();
      const current = openPopover;
      setOpenPopover(null);
      if (current === "branch") branchTriggerRef.current?.focus();
      else if (current === "worktree") worktreePreviewTriggerRef.current?.focus();
      else if (current === "project") projectToolsTriggerRef.current?.focus();
    };
    document.addEventListener("mousedown", onPointerDown);
    window.addEventListener("keydown", onKeyDown, true);
    return () => {
      document.removeEventListener("mousedown", onPointerDown);
      window.removeEventListener("keydown", onKeyDown, true);
    };
  }, [openPopover]);

  // Roving tabindex fallback when native focusgroup is unsupported.
  const nativeFocusGroup =
    typeof HTMLElement !== "undefined" && "focusGroup" in HTMLElement.prototype;

  const getToolbarStops = () =>
    Array.from(
      toolbarRef.current?.querySelectorAll<HTMLButtonElement>(
        ":scope > .v2-integrated-group > button:not(:disabled), :scope > .v2-integrated-group > .v2-integrated-anchor > button:not(:disabled)",
      ) ?? [],
    );

  const handleToolbarKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (nativeFocusGroup || openPopover) return;
    if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
    const stops = getToolbarStops();
    const currentIndex = stops.indexOf(event.target as HTMLButtonElement);
    if (currentIndex < 0 || stops.length === 0) return;
    event.preventDefault();
    const rtl =
      toolbarRef.current && typeof getComputedStyle === "function"
        ? getComputedStyle(toolbarRef.current).direction === "rtl"
        : false;
    let nextIndex = currentIndex;
    if (event.key === "Home") nextIndex = 0;
    else if (event.key === "End") nextIndex = stops.length - 1;
    else {
      const step =
        event.key === "ArrowRight" ? (rtl ? -1 : 1) : rtl ? 1 : -1;
      nextIndex = (currentIndex + step + stops.length) % stops.length;
    }
    setActiveStopIndex(nextIndex);
    stops[nextIndex]?.focus();
  };

  const selectBranch = (branchName: string, createNew: boolean) => {
    const normalizedName = branchName.trim();
    if (!normalizedName) return;
    onStateChange({
      targetBranch: !createNew && normalizedName === currentRepoBranch ? "" : normalizedName,
      createBranch: createNew,
    });
    setBranchFilter("");
    setOpenPopover(null);
    branchTriggerRef.current?.focus();
  };

  const resetBranch = () => {
    onStateChange({ targetBranch: "", createBranch: false });
    setBranchFilter("");
    setOpenPopover(null);
    branchTriggerRef.current?.focus();
  };

  const workspaceName =
    workspaces.find((item) => item.id === workspaceID)?.name ??
    (workspaceID ? workspaceID : t("本地聊天", "Local chats"));

  const effectiveApproval =
    permissionQuery.data?.execution_permission.approval_mode ?? approvalMode ?? "ask";
  const approvalText =
    effectiveApproval === "full"
      ? t("完全访问", "Full access")
      : effectiveApproval === "auto"
        ? t("帮我批准", "Approve for me")
        : t("请求批准", "Request approval");

  const repoStatusSummary = useMemo(() => {
    if (!workspaceID) return t("未绑定项目 · 可预置分支", "No project · Pre-stage branch");
    if (repoStateQuery.isLoading) return t("正在读取 Git 状态…", "Reading Git status…");
    if (!repoAvailable || !repoData) return t("离线或未初始化 Git · 发送前预置分支", "Offline or uninitialized Git · Pre-stage branch");
    if (repoData.clean) return t("工作区干净", "Working tree clean");
    const changeCount =
      repoData.staged_count +
      repoData.worktree_count +
      repoData.untracked_count;
    return t(`${changeCount} 处本地改动`, `${changeCount} local changes`);
  }, [workspaceID, repoStateQuery.isLoading, repoAvailable, repoData, t]);

  const activeSkillsCount = useMemo(
    () => skillsQuery.data?.items?.length,
    [skillsQuery.data],
  );
  const activeMCPCount = extensionsQuery.data?.mcp_servers?.length;

  const cloudBackendStatus = useMemo(() => {
    const entry = sandboxQuery.data?.backends.find((item) => item.backend === state.cloudBackend);
    if (!entry) return "";
    return entry.ready ? t("已就绪", "Ready") : t("待配置", "Setup needed");
  }, [sandboxQuery.data, state.cloudBackend, t]);

  const displayBranch = state.targetBranch.trim() || currentRepoBranch;
  const branchChanged =
    state.targetBranch.trim() !== "" &&
    (state.createBranch || state.targetBranch.trim() !== currentRepoBranch);

  const tabStop = (index: number) => (nativeFocusGroup ? undefined : activeStopIndex === index ? 0 : -1);

  return (
    <div
      aria-label={t("集成式任务控制栏", "Integrated task controls")}
      className="v2-composer-integrated-bar"
      onFocusCapture={(event) => {
        if (nativeFocusGroup) return;
        const stops = getToolbarStops();
        const idx = stops.indexOf(event.target as unknown as HTMLButtonElement);
        if (idx >= 0) setActiveStopIndex(idx);
      }}
      onKeyDown={handleToolbarKeyDown}
      ref={toolbarRef}
      role="toolbar"
      {...({ focusgroup: "toolbar wrap" } as Record<string, string>)}
    >
      <div
        aria-label={t("Git 与工作树", "Git and worktree")}
        className="v2-integrated-group"
        role="group"
      >
        <div className="v2-integrated-anchor">
          <button
            aria-expanded={openPopover === "branch"}
            aria-haspopup="dialog"
            aria-label={`${t("切换 Git 分支", "Switch Git branch")}: ${displayBranch}`}
            className="v2-integrated-pill v2-branch-trigger"
            disabled={disabled}
            onClick={() =>
              setOpenPopover((current) => (current === "branch" ? null : "branch"))
            }
            ref={branchTriggerRef}
            tabIndex={tabStop(0)}
            title={`${t("切换 Git 分支", "Switch Git branch")} (${displayBranch})`}
            type="button"
          >
            <GitBranch aria-hidden="true" size={13} />
            <span className="v2-integrated-branch-name">{displayBranch}</span>
            {state.createBranch && (
              <em className="v2-integrated-badge">{t("新建", "New")}</em>
            )}
            {!state.createBranch && branchChanged && (
              <em className="v2-integrated-badge">{t("待切换", "Staged")}</em>
            )}
            <ChevronDown aria-hidden="true" size={12} />
          </button>

          {openPopover === "branch" && (
            <div
              aria-label={t("切换 Git 分支", "Switch Git branch")}
              className="v2-integrated-popover v2-branch-popover"
              role="dialog"
            >
              <div className="v2-integrated-popover-header">
                <strong>{t("切换 Git 分支", "Switch Git branch")}</strong>
                <small>{repoStatusSummary}</small>
              </div>
              <div className="v2-branch-search" role="search">
                <input
                  aria-label={t("搜索或输入分支名称", "Search or enter branch name")}
                  autoFocus
                  maxLength={120}
                  onChange={(event) => setBranchFilter(event.target.value)}
                  onKeyDown={(event) => {
                    if (event.key === "Enter") {
                      event.preventDefault();
                      event.stopPropagation();
                      if (trimmedFilter) {
                        selectBranch(trimmedFilter, !knownBranches.includes(trimmedFilter));
                      }
                    }
                  }}
                  placeholder={t(
                    "筛选分支或输入新分支名…",
                    "Filter branches or enter new branch…",
                  )}
                  type="search"
                  value={branchFilter}
                />
              </div>

              <ul
                aria-label={t("本地分支列表", "Local branches")}
                className="v2-branch-list"
              >
                {filteredBranches.map((branchName) => {
                  const isSelected = displayBranch === branchName && !state.createBranch;
                  const isRepoCurrent = branchName === currentRepoBranch && repoAvailable;
                  return (
                    <li key={branchName}>
                      <button
                        aria-pressed={isSelected}
                        className="v2-branch-option"
                        onClick={() => selectBranch(branchName, false)}
                        type="button"
                      >
                        <span>{branchName}</span>
                        <span>
                          {isRepoCurrent && (
                            <small>{t("当前", "Current")}</small>
                          )}{" "}
                          {isSelected && <Check aria-hidden="true" size={13} />}
                        </span>
                      </button>
                    </li>
                  );
                })}
              </ul>

              {(trimmedFilter || state.targetBranch) && (
                <div className="v2-branch-custom-actions">
                  {trimmedFilter && !knownBranches.includes(trimmedFilter) && (
                    <>
                      <button
                        className="v2-integrated-action-row"
                        onClick={() => selectBranch(trimmedFilter, true)}
                        type="button"
                      >
                        <Plus aria-hidden="true" size={14} />
                        <span>
                          {t(
                            `新建并切换分支 "${trimmedFilter}"`,
                            `Create & switch branch "${trimmedFilter}"`,
                          )}
                        </span>
                      </button>
                      <button
                        className="v2-integrated-action-row"
                        onClick={() => selectBranch(trimmedFilter, false)}
                        type="button"
                      >
                        <GitBranch aria-hidden="true" size={14} />
                        <span>
                          {t(
                            `切换至已有分支 "${trimmedFilter}"`,
                            `Switch to existing branch "${trimmedFilter}"`,
                          )}
                        </span>
                      </button>
                    </>
                  )}
                  {state.targetBranch && (
                    <button
                      className="v2-integrated-action-row"
                      onClick={resetBranch}
                      type="button"
                    >
                      <RotateCcw aria-hidden="true" size={14} />
                      <span>
                        {t(
                          `恢复跟随仓库分支 (${currentRepoBranch})`,
                          `Follow repository branch (${currentRepoBranch})`,
                        )}
                      </span>
                    </button>
                  )}
                </div>
              )}
            </div>
          )}
        </div>

        <button
          aria-label={t("开启独立工作树", "Enable isolated worktree")}
          aria-pressed={state.worktreeEnabled}
          className="v2-integrated-pill v2-worktree-toggle"
          disabled={disabled}
          onClick={() =>
            onStateChange((prev) => ({
              ...prev,
              worktreeEnabled: !prev.worktreeEnabled,
            }))
          }
          tabIndex={tabStop(1)}
          title={t(
            "开启独立 Git Worktree 隔离工作目录运行本次任务",
            "Run this task inside an isolated Git worktree",
          )}
          type="button"
        >
          <FolderGit2 aria-hidden="true" size={13} />
          <span>{t("独立工作树", "Worktree")}</span>
        </button>

        {state.worktreeEnabled && (
          <div className="v2-integrated-anchor">
            <button
              aria-expanded={openPopover === "worktree"}
              aria-haspopup="dialog"
              aria-label={t("配置独立工作树", "Configure isolated worktree")}
              className="v2-integrated-pill v2-worktree-preview-btn"
              disabled={disabled}
              onClick={() =>
                setOpenPopover((current) =>
                  current === "worktree" ? null : "worktree",
                )
              }
              ref={worktreePreviewTriggerRef}
              tabIndex={-1}
              title={t("预览或自定义独立工作树名称", "Preview or customize worktree name")}
              type="button"
            >
              <code className="v2-integrated-worktree-slug">
                {resolvedContext.worktree.name}
              </code>
              <ChevronDown aria-hidden="true" size={11} />
            </button>

            {openPopover === "worktree" && (
              <div
                aria-label={t("独立工作树设置", "Isolated worktree settings")}
                className="v2-integrated-popover v2-worktree-popover"
                role="dialog"
              >
                <div className="v2-integrated-popover-header">
                  <strong>{t("独立工作树预览", "Isolated worktree preview")}</strong>
                  <small>{t("发送前自动隔离", "Pre-send isolation")}</small>
                </div>
                <label className="v2-worktree-field">
                  <span>{t("自定义工作树名称（可选）", "Custom worktree name (optional)")}</span>
                  <input
                    aria-label={t("工作树名称", "Worktree name")}
                    maxLength={64}
                    onChange={(event) =>
                      onStateChange({ worktreeName: event.target.value })
                    }
                    placeholder={deriveWorktreeSlug(
                      { ...state, worktreeName: "" },
                      draftText,
                    )}
                    type="text"
                    value={state.worktreeName}
                  />
                </label>
                <dl className="v2-worktree-preview-card">
                  <div>
                    <dt>{t("工作树目录", "Worktree dir")}</dt>
                    <dd>{resolvedContext.worktree.name}</dd>
                  </div>
                  <div>
                    <dt>{t("隔离分支", "Worktree branch")}</dt>
                    <dd>{resolvedContext.worktree.branch}</dd>
                  </div>
                  <div>
                    <dt>{t("基准分支", "Base branch")}</dt>
                    <dd>{currentRepoBranch}</dd>
                  </div>
                </dl>
              </div>
            )}
          </div>
        )}
      </div>

      <span aria-orientation="vertical" className="v2-integrated-sep" role="separator" />

      <div
        aria-label={t("运行环境", "Execution environment")}
        className="v2-integrated-group v2-env-segmented"
        role="group"
      >
        <button
          aria-pressed={state.environment === "local"}
          className="v2-env-segment"
          disabled={disabled}
          onClick={() => onStateChange({ environment: "local" })}
          tabIndex={tabStop(2)}
          title={t("在本地工作区环境中执行任务", "Run task in local workspace environment")}
          type="button"
        >
          <Laptop aria-hidden="true" size={13} />
          <span>{t("本地环境", "Local")}</span>
        </button>
        <button
          aria-pressed={state.environment === "cloud"}
          className="v2-env-segment"
          disabled={disabled}
          onClick={() => onStateChange({ environment: "cloud" })}
          tabIndex={tabStop(3)}
          title={t(
            "在云端或隔离沙盒环境中执行任务",
            "Run task in cloud or isolated sandbox environment",
          )}
          type="button"
        >
          <Cloud aria-hidden="true" size={13} />
          <span>{t("云端环境", "Cloud")}</span>
        </button>
      </div>

      <span aria-orientation="vertical" className="v2-integrated-sep" role="separator" />

      <div
        aria-label={t("项目工具与支线", "Project tools and side chat")}
        className="v2-integrated-group"
        role="group"
      >
        <div className="v2-integrated-anchor">
          <button
            aria-expanded={openPopover === "project"}
            aria-haspopup="dialog"
            aria-label={t("项目设置与工具", "Project settings and tools")}
            className="v2-integrated-pill v2-project-tools-trigger"
            disabled={disabled}
            onClick={() =>
              setOpenPopover((current) => (current === "project" ? null : "project"))
            }
            ref={projectToolsTriggerRef}
            tabIndex={tabStop(4)}
            title={t(
              "查看项目根目录、审批策略、运行环境与可用工具",
              "Inspect workspace root, approval policy, runtime environment, and tools",
            )}
            type="button"
          >
            <SlidersHorizontal aria-hidden="true" size={13} />
            <span className="v2-integrated-optional">
              {t("项目设置与工具", "Settings & Tools")}
            </span>
            <ChevronDown aria-hidden="true" size={12} />
          </button>

          {openPopover === "project" && (
            <div
              aria-label={t("项目设置与工具", "Project settings and tools")}
              className="v2-integrated-popover is-wide v2-project-tools-popover"
              role="dialog"
            >
              <div className="v2-integrated-popover-header">
                <strong>{t("项目设置与工具", "Project settings and tools")}</strong>
                <small>{workspaceName}</small>
              </div>

              <dl className="v2-project-summary-card">
                <div>
                  <dt>{t("工作区根目录", "Workspace root")}</dt>
                  <dd title={projectQuery.data?.directory_path || workspaceName}>
                    {projectQuery.data?.directory_path ||
                      (workspaceID ? workspaceName : t("本地默认目录", "Local default folder"))}
                  </dd>
                </div>
                <div>
                  <dt>{t("Git 状态", "Git target")}</dt>
                  <dd>
                    {resolvedContext.branch}
                    {state.worktreeEnabled ? ` · ${resolvedContext.worktree.name}` : ""}
                  </dd>
                </div>
                <div>
                  <dt>{t("审批策略", "Approval policy")}</dt>
                  <dd>{approvalText}</dd>
                </div>
                <div>
                  <dt>{t("执行环境摘要", "Environment")}</dt>
                  <dd>
                    {state.environment === "cloud"
                      ? `${t("云端沙盒", "Cloud sandbox")} (${state.cloudBackend}${cloudBackendStatus ? ` · ${cloudBackendStatus}` : ""})`
                      : t("本地环境 (local)", "Local workspace")}
                  </dd>
                </div>
              </dl>

              <div className="v2-project-tools-section">
                <h4>{t("发送前快捷设置", "Pre-send quick settings")}</h4>

                <div className="v2-project-setting-row">
                  <span>{t("沙盒运行后端", "Cloud sandbox runtime")}</span>
                  <div
                    aria-label={t("沙盒运行后端", "Cloud sandbox runtime")}
                    className="v2-project-inline-choices"
                    role="group"
                  >
                    <button
                      aria-pressed={state.cloudBackend === "sbx"}
                      className="v2-project-choice-btn"
                      onClick={() =>
                        onStateChange({ environment: "cloud", cloudBackend: "sbx" })
                      }
                      type="button"
                    >
                      sbx MicroVM
                    </button>
                    <button
                      aria-pressed={state.cloudBackend === "docker"}
                      className="v2-project-choice-btn"
                      onClick={() =>
                        onStateChange({ environment: "cloud", cloudBackend: "docker" })
                      }
                      type="button"
                    >
                      Docker
                    </button>
                  </div>
                </div>

                {!threadID && (
                  <div className="v2-project-setting-row">
                    <span>{t("任务阶段", "Task phase")}</span>
                    <div
                      aria-label={t("任务阶段", "Task phase")}
                      className="v2-project-inline-choices"
                      role="group"
                    >
                      <button
                        aria-pressed={effectivePhase === "deliver"}
                        className="v2-project-choice-btn"
                        onClick={() => changePhase("deliver")}
                        type="button"
                      >
                        {t("直接执行", "Deliver")}
                      </button>
                      <button
                        aria-pressed={effectivePhase === "plan"}
                        className="v2-project-choice-btn"
                        disabled={!client.hasPlanDelivery && effectivePhase !== "plan"}
                        onClick={() => changePhase("plan")}
                        type="button"
                      >
                        {t("计划模式", "Plan")}
                      </button>
                    </div>
                  </div>
                )}

                <div className="v2-project-setting-row">
                  <span>{t("编辑器模式", "Editor mode")}</span>
                  <div
                    aria-label={t("编辑器模式", "Editor mode")}
                    className="v2-project-inline-choices"
                    role="group"
                  >
                    <button
                      aria-pressed={!plainText}
                      className="v2-project-choice-btn"
                      onClick={() => {
                        if (plainText) onToggleEditorMode(null);
                      }}
                      type="button"
                    >
                      Markdown
                    </button>
                    <button
                      aria-pressed={plainText}
                      className="v2-project-choice-btn"
                      onClick={() => {
                        if (!plainText) onToggleEditorMode(null);
                      }}
                      type="button"
                    >
                      {t("纯文本", "Plain text")}
                    </button>
                  </div>
                </div>
              </div>

              <div className="v2-project-tools-section">
                <h4>{t("已启用工具与上下文", "Enabled tools and context")}</h4>
                <div className="v2-project-tools-inventory">
                  <span className="v2-project-inventory-pill">
                    <Wrench aria-hidden="true" size={12} />
                    {activeSkillsCount !== undefined
                      ? t(`已安装技能 ${activeSkillsCount}`, `Skills ${activeSkillsCount}`)
                      : t("内置代码工具就绪", "Code tools ready")}
                  </span>
                  <span className="v2-project-inventory-pill">
                    <Database aria-hidden="true" size={12} />
                    {activeMCPCount !== undefined
                      ? t(`MCP 服务 ${activeMCPCount}`, `MCP servers ${activeMCPCount}`)
                      : t("MCP 扩展按需连接", "MCP on-demand")}
                  </span>
                </div>

                <div className="v2-project-tools-grid">
                  {onUploadAttachment && (
                    <button
                      className="v2-integrated-action-row"
                      disabled={!canUploadAttachment}
                      onClick={() => {
                        setOpenPopover(null);
                        onUploadAttachment();
                      }}
                      type="button"
                    >
                      <Paperclip aria-hidden="true" size={13} />
                      <span>{t("上传附件", "Upload files")}</span>
                    </button>
                  )}
                  {onOpenFileReference && (
                    <button
                      className="v2-integrated-action-row"
                      disabled={!canReferenceFiles}
                      onClick={() => {
                        setOpenPopover(null);
                        onOpenFileReference();
                      }}
                      type="button"
                    >
                      <FolderOpen aria-hidden="true" size={13} />
                      <span>{t("引用项目文件", "Project files")}</span>
                    </button>
                  )}
                  {onOpenMCPResource && (
                    <button
                      className="v2-integrated-action-row"
                      disabled={!canUseMCP}
                      onClick={() => {
                        setOpenPopover(null);
                        onOpenMCPResource();
                      }}
                      type="button"
                    >
                      <Database aria-hidden="true" size={13} />
                      <span>{t("MCP 资源", "MCP resource")}</span>
                    </button>
                  )}
                  {onOpenMCPPrompt && (
                    <button
                      className="v2-integrated-action-row"
                      disabled={!canUseMCP}
                      onClick={() => {
                        setOpenPopover(null);
                        onOpenMCPPrompt();
                      }}
                      type="button"
                    >
                      <Command aria-hidden="true" size={13} />
                      <span>{t("MCP Prompt", "MCP prompt")}</span>
                    </button>
                  )}
                  {onOpenTaskConfiguration && (
                    <button
                      className="v2-integrated-action-row"
                      onClick={() => {
                        setOpenPopover(null);
                        onOpenTaskConfiguration();
                      }}
                      type="button"
                    >
                      <SlidersHorizontal aria-hidden="true" size={13} />
                      <span>{t("任务预算配置", "Task budget")}</span>
                    </button>
                  )}
                </div>
              </div>
            </div>
          )}
        </div>
      </div>

      <div
        aria-label={t("并行支线对话入口", "Parallel side conversation")}
        className="v2-integrated-group v2-integrated-group-end"
        role="group"
      >
        <button
          aria-label={t("支线问答", "Side Chat")}
          aria-pressed={sideChatOpen}
          className={`v2-integrated-pill v2-side-chat-trigger${busy ? " is-main-busy" : ""}`}
          onClick={onToggleSideChat}
          ref={sideChatTriggerRef}
          tabIndex={tabStop(5)}
          title={t(
            "开启并行支线问答 (Alt+S) · 共享完整上下文且不打断主任务",
            "Open parallel side chat (Alt+S) · Shares full context without interrupting main task",
          )}
          type="button"
        >
          <Split aria-hidden="true" size={13} />
          <span>{t("支线问答", "Side Chat")}</span>
          {busy ? (
            <em className="v2-side-chat-busy-badge">
              {t("主任务运行中", "Busy · Parallel")}
            </em>
          ) : sideChatTurnCount > 0 ? (
            <em className="v2-integrated-badge">{sideChatTurnCount}</em>
          ) : (
            <kbd className="v2-side-chat-kbd">Alt+S</kbd>
          )}
        </button>
      </div>
    </div>
  );
}
