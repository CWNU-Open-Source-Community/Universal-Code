import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type FormEvent,
  type KeyboardEvent as ReactKeyboardEvent,
  type RefObject,
} from "react";
import { createPortal } from "react-dom";
import { useQuery, useQueryClient, type InfiniteData } from "@tanstack/react-query";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import {
  ArrowDownLeft,
  ArrowUp,
  Check,
  Copy,
  Layers,
  LoaderCircle,
  Minimize2,
  RotateCcw,
  Split,
} from "lucide-react";
import type { APIClient } from "../../api/client";
import type {
  AvailableModelRouteView,
  PageResult,
  ThreadDetailView,
  ThreadTranscriptItemView,
  WorkspaceView,
} from "../../api/types";
import { useLocale, type LocaleText } from "../../lib/locale";
import { getSafeLocalStorage } from "../../lib/prompt-commands";
import { useConnectionStore } from "../../state/connection";
import { prepareThreadNarrative } from "../projection/narrative";
import { v2QueryKeys } from "../query-keys";
import type { V2PendingModelRoute } from "./model-route-control";
import "./side-conversation.css";

export type V2SideContextScope = "full" | "goal_only";

export interface V2SideConversationTurnSnapshot {
  role: "user" | "assistant";
  text: string;
}

export interface V2SideConversationContext {
  workspaceName?: string;
  workspacePath?: string;
  threadTitle?: string;
  goal?: string;
  runStatus?: string;
  stepSummary?: string;
  turns?: V2SideConversationTurnSnapshot[];
  referencedFiles?: string[];
}

export interface V2SideChatMessage {
  id: string;
  role: "user" | "assistant";
  content: string;
  modelLabel: string;
  createdAt: string;
}

export interface V2SideChatModelSelection {
  provider: string;
  model: string;
  label: string;
}

export interface V2SideChatAskRequest {
  question: string;
  prompt: string;
  modelRoute: V2SideChatModelSelection;
  contextScope: V2SideContextScope;
  snapshot: Required<V2SideConversationContext>;
  history: V2SideChatMessage[];
  signal: AbortSignal;
}

const knownProviderLabels: Record<string, string> = {
  "official-anthropic": "Claude",
  "official-openai": "OpenAI",
  "official-deepseek": "DeepSeek",
  "official-google-gemini": "Gemini",
  "official-xai": "Grok",
  "official-minimax": "MiniMax",
  "official-mimo": "MiMo",
  "official-kimi": "Kimi",
  "official-kimi-coding": "Kimi for Coding",
  "opencode-go": "OpenCode Go",
  "github-copilot": "GitHub Copilot",
};

function formatRouteLabel(provider: string, model: string): string {
  const providerTitle = knownProviderLabels[provider] ?? provider;
  const shortModel = model.split("/").filter(Boolean).at(-1) || model;
  return providerTitle ? `${providerTitle} · ${shortModel}` : shortModel;
}

function encodeRouteKey(provider: string, model: string): string {
  return `${provider}::${model}`;
}

function decodeRouteKey(value: string): { provider: string; model: string } | null {
  const index = value.indexOf("::");
  if (index <= 0) return null;
  const provider = value.slice(0, index).trim();
  const model = value.slice(index + 2).trim();
  if (!provider || !model) return null;
  return { provider, model };
}

export function v2SideChatStorageKey(
  dataStoreID: string,
  threadID: string,
  workspaceID: string,
): string {
  const scope = dataStoreID.trim() || "local";
  const target = threadID.trim() || `new:${workspaceID.trim() || "local"}`;
  return `v2:${scope}:side-chat:${target}`;
}

function v2SideChatModelStorageKey(dataStoreID: string): string {
  const scope = dataStoreID.trim() || "local";
  return `v2:${scope}:side-chat-model`;
}

function readStoredSideMessages(storageKey: string): V2SideChatMessage[] {
  const storage = getSafeLocalStorage();
  if (!storage) return [];
  try {
    const raw = storage.getItem(storageKey);
    if (!raw) return [];
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return [];
    return parsed
      .filter((item): item is Record<string, unknown> => Boolean(item && typeof item === "object"))
      .filter(
        (item) =>
          (item.role === "user" || item.role === "assistant") &&
          typeof item.content === "string" &&
          item.content.trim() !== "",
      )
      .slice(-40)
      .map((item, idx) => ({
        id: typeof item.id === "string" && item.id ? item.id : `side-msg-${idx}`,
        role: item.role as "user" | "assistant",
        content: String(item.content),
        modelLabel: typeof item.modelLabel === "string" ? item.modelLabel : "",
        createdAt:
          typeof item.createdAt === "string" ? item.createdAt : new Date(0).toISOString(),
      }));
  } catch {
    return [];
  }
}

function writeStoredSideMessages(storageKey: string, messages: V2SideChatMessage[]): void {
  const storage = getSafeLocalStorage();
  if (!storage) return;
  try {
    if (!messages.length) {
      storage.removeItem(storageKey);
    } else {
      storage.setItem(storageKey, JSON.stringify(messages.slice(-40)));
    }
  } catch {
    // Ephemeral in-memory history continues working if Web Storage is full/disabled.
  }
}

function readStoredSideModel(storageKey: string): { provider: string; model: string } | null {
  const storage = getSafeLocalStorage();
  if (!storage) return null;
  try {
    const raw = storage.getItem(storageKey);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as unknown;
    if (!parsed || typeof parsed !== "object") return null;
    const record = parsed as Record<string, unknown>;
    if (
      typeof record.provider === "string" &&
      record.provider.trim() &&
      typeof record.model === "string" &&
      record.model.trim()
    ) {
      return { provider: record.provider.trim(), model: record.model.trim() };
    }
    return null;
  } catch {
    return null;
  }
}

function writeStoredSideModel(
  storageKey: string,
  selection: { provider: string; model: string },
): void {
  const storage = getSafeLocalStorage();
  if (!storage) return;
  try {
    storage.setItem(storageKey, JSON.stringify(selection));
  } catch {
    // Ignore storage quota failures.
  }
}

export function useV2SideChatHistory(threadID: string, workspaceID: string) {
  const dataStoreID = useConnectionStore((state) => state.health?.data_store_id) || "local";
  const storageKey = useMemo(
    () => v2SideChatStorageKey(dataStoreID, threadID, workspaceID),
    [dataStoreID, threadID, workspaceID],
  );
  const [messages, setMessagesState] = useState<V2SideChatMessage[]>(() =>
    readStoredSideMessages(storageKey),
  );

  useEffect(() => {
    setMessagesState(readStoredSideMessages(storageKey));
  }, [storageKey]);

  const setMessages = useCallback(
    (
      updater:
        | V2SideChatMessage[]
        | ((current: V2SideChatMessage[]) => V2SideChatMessage[]),
    ) => {
      setMessagesState((current) => {
        const next = typeof updater === "function" ? updater(current) : updater;
        writeStoredSideMessages(storageKey, next);
        return next;
      });
    },
    [storageKey],
  );

  const userTurnCount = useMemo(
    () => messages.filter((item) => item.role === "user").length,
    [messages],
  );

  return { messages, setMessages, userTurnCount, storageKey };
}

export function buildSideConversationPrompt(
  question: string,
  snapshot: Required<V2SideConversationContext>,
  contextScope: V2SideContextScope,
  history: V2SideChatMessage[],
  t: LocaleText,
): string {
  const lines: string[] = [
    t(
      "你正在回答一条「并行支线问答 (Parallel Side Conversation)」。请基于以下只读主对话上下文直接解答问题，不要假定需要中断或修改正在执行的主任务。",
      "You are answering a Parallel Side Conversation. Use the read-only main conversation context below without interrupting or mutating the active main task.",
    ),
    "",
    `## ${t("主任务只读上下文快照", "Main Thread Read-Only Context Snapshot")}`,
    `- ${t("工作区", "Workspace")}: ${snapshot.workspacePath || snapshot.workspaceName || "local"}`,
    `- ${t("当前目标", "Current Goal")}: ${snapshot.goal || snapshot.threadTitle || t("未命名任务", "Untitled task")}`,
    `- ${t("主任务状态", "Main Run Status")}: ${snapshot.runStatus || "idle"}${snapshot.stepSummary ? ` (${snapshot.stepSummary})` : ""}`,
  ];

  if (snapshot.referencedFiles.length > 0) {
    lines.push(
      `- ${t("关联代码文件", "Referenced Files")}: ${snapshot.referencedFiles.join(", ")}`,
    );
  }

  if (contextScope === "full" && snapshot.turns.length > 0) {
    lines.push("", `## ${t("主对话近期上下文", "Recent Main Conversation Turns")}`);
    for (const turn of snapshot.turns.slice(-8)) {
      const speaker = turn.role === "user" ? t("用户", "User") : t("主智能体", "Main Agent");
      const excerpt = turn.text.trim().slice(0, 600);
      lines.push(`[${speaker}]: ${excerpt}`);
    }
  }

  if (history.length > 0) {
    lines.push("", `## ${t("本支线历史问答", "Previous Side Conversation Turns")}`);
    for (const msg of history.slice(-6)) {
      const speaker = msg.role === "user" ? t("支线提问", "Side Question") : t("支线回答", "Side Answer");
      lines.push(`[${speaker}]: ${msg.content.trim().slice(0, 600)}`);
    }
  }

  lines.push("", `## ${t("当前支线问题", "Current Side Question")}`, question.trim());
  return lines.join("\n");
}

function extractReplyFromTranscriptItems(
  items: ThreadTranscriptItemView[],
  t: LocaleText,
): string {
  const projected = prepareThreadNarrative(items, t).entries.filter(
    (entry): entry is Extract<ReturnType<typeof prepareThreadNarrative>["entries"][number], { kind: "assistant" }> =>
      entry.kind === "assistant" && entry.text.trim() !== "",
  );
  if (projected.length > 0) {
    return projected.at(-1)!.text.trim();
  }
  const rawModelItems = items.filter(
    (item) => item.source === "model" && item.kind !== "model_call",
  );
  for (let idx = rawModelItems.length - 1; idx >= 0; idx -= 1) {
    const item = rawModelItems[idx];
    const candidate = item.detail?.trim() || item.title?.trim() || "";
    if (candidate) return candidate;
  }
  return "";
}

async function waitAbortable(ms: number, signal: AbortSignal): Promise<void> {
  if (signal.aborted) throw new DOMException("Aborted", "AbortError");
  await new Promise<void>((resolve, reject) => {
    const timer = globalThis.setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    const onAbort = () => {
      globalThis.clearTimeout(timer);
      reject(new DOMException("Aborted", "AbortError"));
    };
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

async function executeIsolatedSideTurn(
  client: APIClient,
  workspaceID: string,
  request: V2SideChatAskRequest,
  t: LocaleText,
): Promise<string> {
  const { signal, modelRoute, prompt, question, snapshot, contextScope } = request;
  const canCreateIsolatedThread = Boolean(
    client.hasThreadControl &&
      typeof client.createThread === "function" &&
      typeof client.submitThreadTurn === "function",
  );

  if (!canCreateIsolatedThread) {
    const sharedTurnsNote =
      contextScope === "full"
        ? t(`${snapshot.turns.length} 轮近期对话`, `${snapshot.turns.length} recent turns`)
        : t("仅当前目标", "current goal only");
    return t(
      `已通过独立支线模型 **${modelRoute.label}** 读取只读上下文快照（工作区：\`${snapshot.workspacePath || snapshot.workspaceName || "local"}\`，共享范围：${sharedTurnsNote}，主任务状态：\`${snapshot.runStatus || "idle"}\`）。\n\n针对问题「${question}」：\n- 当前处于离线或轻量预览连接，主对话历史与运行任务未受任何影响。\n- 接入完整服务控制端后，支线会自动通过隔离模型会话返回实时推理结果。`,
      `Read-only context snapshot captured via side model **${modelRoute.label}** (workspace: \`${snapshot.workspacePath || snapshot.workspaceName || "local"}\`, scope: ${sharedTurnsNote}, main status: \`${snapshot.runStatus || "idle"}\`).\n\nRegarding "${question}":\n- Running in offline/preview mode; the main transcript and active run remain completely untouched.\n- When connected to a live runtime, side queries execute in an isolated model session.`,
    );
  }

  const operationID = globalThis.crypto.randomUUID();
  const hasExplicitRoute =
    Boolean(modelRoute.provider && modelRoute.model) && modelRoute.provider !== "default";
  const directRouteAllowed =
    hasExplicitRoute &&
    !modelRoute.provider.includes("/") &&
    !modelRoute.model.includes("/");

  const created = await client.createThread(
    {
      version: "thread_creation.v1",
      workspace_id: workspaceID || "local-workspace",
      goal: `[Side Q&A] ${question.slice(0, 120)}`,
      profile: "code",
      surface: "code",
      phase: client.hasPlanDelivery ? "plan" : "deliver",
      network_mode: "disabled",
      ...(directRouteAllowed
        ? { provider: modelRoute.provider, model: modelRoute.model }
        : {}),
    },
    `v2-side-chat-create-${operationID}`,
    signal,
  );

  const sideThreadID = created.thread.id;
  let expectedVersion = created.thread.version ?? 1;

  try {
    if (
      hasExplicitRoute &&
      !directRouteAllowed &&
      client.hasModelControl &&
      typeof client.selectThreadModelRoute === "function"
    ) {
      await client
        .selectThreadModelRoute(
          sideThreadID,
          {
            version: "thread_model_route_control.v1",
            action: "select",
            provider: modelRoute.provider,
            model: modelRoute.model,
            operation_key: `v2-side-chat-route-${operationID}`,
            requested_by: "side-chat",
          },
          signal,
        )
        .catch(() => undefined);
    }

    const turnResult = await client.submitThreadTurn(
      sideThreadID,
      {
        version: "thread_message_submission.v1",
        content: prompt,
      },
      `v2-side-chat-turn-${operationID}`,
      signal,
    );

    if (typeof client.getPage === "function") {
      const delays = turnResult.execution_started ? [0, 80, 180, 320] : [0];
      for (const delayMs of delays) {
        if (delayMs > 0) await waitAbortable(delayMs, signal);
        const page = await client.getPage<ThreadTranscriptItemView>(
          `/threads/${encodeURIComponent(sideThreadID)}/transcript`,
          { limit: 25 },
          "",
          signal,
        );
        const reply = extractReplyFromTranscriptItems(page.items ?? [], t);
        if (reply) return reply;
      }
    }

    if (turnResult.run_id && typeof client.getPublicModelStream === "function") {
      const live = await client.getPublicModelStream(turnResult.run_id, signal).catch(() => null);
      const streamedText = (live as { output_text?: string } | null)?.output_text?.trim();
      if (streamedText) return streamedText;
    }

    return t(
      "支线模型已受理提问，但暂未返回公开文本结果。请稍后重试或切换支线模型路由。",
      "The side model accepted the question, but no text response was returned yet. Retry or switch the side model route.",
    );
  } finally {
    if (typeof client.transitionThread === "function") {
      if (typeof client.get === "function") {
        const refreshed = await client
          .get<ThreadDetailView>(`/threads/${encodeURIComponent(sideThreadID)}`)
          .catch(() => null);
        if (refreshed?.thread?.version) {
          expectedVersion = refreshed.thread.version;
        }
      }
      await client
        .transitionThread(
          sideThreadID,
          "archive",
          {
            version: "thread_lifecycle.v1",
            expected_version: expectedVersion,
          },
          `v2-side-chat-archive-${operationID}`,
        )
        .catch(() => undefined);
    }
  }
}

export interface V2SideConversationProps {
  open: boolean;
  onClose: () => void;
  returnFocusRef: RefObject<HTMLElement | null>;
  client: APIClient;
  threadID: string;
  workspaceID: string;
  workspaces: WorkspaceView[];
  busy?: boolean;
  draftText: string;
  referencedFilePaths?: string[];
  pendingModelRoute?: V2PendingModelRoute | null;
  context?: V2SideConversationContext;
  messages: V2SideChatMessage[];
  onMessagesChange: (
    updater:
      | V2SideChatMessage[]
      | ((current: V2SideChatMessage[]) => V2SideChatMessage[]),
  ) => void;
  onInsertToComposer: (text: string) => void;
  onSideChatAsk?: (request: V2SideChatAskRequest) => Promise<string>;
}

export function V2SideConversation({
  open,
  onClose,
  returnFocusRef,
  client,
  threadID,
  workspaceID,
  workspaces,
  busy = false,
  draftText,
  referencedFilePaths = [],
  pendingModelRoute,
  context,
  messages,
  onMessagesChange,
  onInsertToComposer,
  onSideChatAsk,
}: V2SideConversationProps) {
  const { t } = useLocale();
  const queryClient = useQueryClient();
  const dataStoreID = useConnectionStore((state) => state.health?.data_store_id) || "local";
  const modelStorageKey = useMemo(() => v2SideChatModelStorageKey(dataStoreID), [dataStoreID]);

  const [question, setQuestion] = useState("");
  const [contextScope, setContextScope] = useState<V2SideContextScope>("full");
  const [selectedModelKey, setSelectedModelKey] = useState<string>(() => {
    const saved = readStoredSideModel(modelStorageKey);
    return saved ? encodeRouteKey(saved.provider, saved.model) : "";
  });
  const [asking, setAsking] = useState(false);
  const [errorText, setErrorText] = useState("");
  const [statusNotice, setStatusNotice] = useState("");
  const [copiedID, setCopiedID] = useState("");

  const inputRef = useRef<HTMLTextAreaElement>(null);
  const messagesContainerRef = useRef<HTMLDivElement>(null);
  const abortControllerRef = useRef<AbortController | null>(null);

  useEffect(() => {
    const saved = readStoredSideModel(modelStorageKey);
    if (saved) {
      setSelectedModelKey(encodeRouteKey(saved.provider, saved.model));
    }
  }, [modelStorageKey]);

  useEffect(() => {
    return () => {
      abortControllerRef.current?.abort();
    };
  }, []);

  useEffect(() => {
    if (!open) return;
    const frame = requestAnimationFrame(() => {
      inputRef.current?.focus();
    });
    return () => cancelAnimationFrame(frame);
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const container = messagesContainerRef.current;
    if (container) {
      container.scrollTop = container.scrollHeight;
    }
  }, [open, messages.length, asking]);

  const availableRoutesQuery = useQuery({
    queryKey: ["v2", "models", "available-routes"],
    queryFn: ({ signal }) => client.availableModelRoutes(signal),
    enabled: Boolean(
      open && client.hasModelControl && typeof client.availableModelRoutes === "function",
    ),
    staleTime: 30_000,
    retry: false,
  });

  const mainThreadRouteQuery = useQuery({
    queryKey: ["v2", "models", "thread-route", threadID],
    queryFn: ({ signal }) => client.threadModelRoute(threadID, signal),
    enabled: Boolean(
      open &&
        threadID &&
        client.hasModelControl &&
        typeof client.threadModelRoute === "function",
    ),
    staleTime: 30_000,
    retry: false,
  });

  const projectQuery = useQuery({
    queryKey: ["v2", "composer-workspace-project", workspaceID],
    queryFn: ({ signal }) => client.getWorkspaceProject(workspaceID, signal),
    enabled: Boolean(
      open &&
        !context?.workspacePath &&
        workspaceID &&
        client.hasControl &&
        typeof client.getWorkspaceProject === "function",
    ),
    staleTime: 30_000,
    retry: false,
  });

  const selectableRoutes = useMemo<AvailableModelRouteView[]>(() => {
    const all = availableRoutesQuery.data?.routes ?? [];
    const ready = all.filter((route) => route.selectable);
    return ready.length > 0 ? ready : all;
  }, [availableRoutesQuery.data?.routes]);

  const modelOptions = useMemo<V2SideChatModelSelection[]>(() => {
    const seen = new Set<string>();
    const list: V2SideChatModelSelection[] = [];
    const pushOption = (provider: string, model: string, label?: string) => {
      const key = encodeRouteKey(provider, model);
      if (seen.has(key)) return;
      seen.add(key);
      list.push({
        provider,
        model,
        label: label ?? formatRouteLabel(provider, model),
      });
    };

    for (const route of selectableRoutes) {
      pushOption(route.provider_id, route.model);
    }
    if (pendingModelRoute?.provider && pendingModelRoute?.model) {
      pushOption(pendingModelRoute.provider, pendingModelRoute.model);
    }
    if (
      mainThreadRouteQuery.data?.provider &&
      mainThreadRouteQuery.data?.model
    ) {
      pushOption(mainThreadRouteQuery.data.provider, mainThreadRouteQuery.data.model);
    }
    const decodedSaved = decodeRouteKey(selectedModelKey);
    if (decodedSaved) {
      pushOption(decodedSaved.provider, decodedSaved.model);
    }
    if (list.length === 0) {
      pushOption("default", "auto", t("默认模型路由", "Default model route"));
    }
    return list;
  }, [
    selectableRoutes,
    pendingModelRoute,
    mainThreadRouteQuery.data,
    selectedModelKey,
    t,
  ]);

  const activeModelSelection = useMemo<V2SideChatModelSelection>(() => {
    const explicit = modelOptions.find(
      (item) => encodeRouteKey(item.provider, item.model) === selectedModelKey,
    );
    if (explicit) return explicit;
    if (pendingModelRoute) {
      const fromPending = modelOptions.find(
        (item) =>
          item.provider === pendingModelRoute.provider &&
          item.model === pendingModelRoute.model,
      );
      if (fromPending) return fromPending;
    }
    if (mainThreadRouteQuery.data?.provider && mainThreadRouteQuery.data?.model) {
      const fromMain = modelOptions.find(
        (item) =>
          item.provider === mainThreadRouteQuery.data.provider &&
          item.model === mainThreadRouteQuery.data.model,
      );
      if (fromMain) return fromMain;
    }
    return modelOptions[0];
  }, [modelOptions, selectedModelKey, pendingModelRoute, mainThreadRouteQuery.data]);

  const snapshot = useMemo<Required<V2SideConversationContext>>(() => {
    const cachedDetail = threadID
      ? queryClient.getQueryData<ThreadDetailView>(v2QueryKeys.thread(threadID))
      : undefined;
    const cachedTranscript = threadID
      ? queryClient.getQueryData<InfiniteData<PageResult<ThreadTranscriptItemView>, string>>(
          v2QueryKeys.transcript(threadID),
        )
      : undefined;

    let fallbackTurns: V2SideConversationTurnSnapshot[] = [];
    if (!context?.turns && cachedTranscript?.pages?.length) {
      const flatItems = cachedTranscript.pages
        .slice()
        .reverse()
        .flatMap((page) => page.items ?? []);
      fallbackTurns = prepareThreadNarrative(flatItems, t)
        .entries.filter(
          (entry): entry is Extract<ReturnType<typeof prepareThreadNarrative>["entries"][number], { kind: "user" | "assistant" }> =>
            entry.kind === "user" || entry.kind === "assistant",
        )
        .slice(-12)
        .map((entry) => ({ role: entry.kind, text: entry.text }));
    }

    const workspaceObj = workspaces.find((item) => item.id === workspaceID);
    const workspaceName =
      context?.workspaceName ||
      workspaceObj?.name ||
      (workspaceID ? workspaceID : t("本地工作区", "Local workspace"));
    const workspacePath =
      context?.workspacePath || projectQuery.data?.directory_path || workspaceName;
    const threadTitle =
      context?.threadTitle ||
      cachedDetail?.thread.title ||
      (draftText.trim() ? draftText.trim().slice(0, 80) : t("当前未发送草稿", "Current draft"));
    const goal =
      context?.goal ||
      cachedDetail?.mission?.goal ||
      threadTitle;
    const runStatus =
      context?.runStatus ||
      cachedDetail?.active_run?.status ||
      cachedDetail?.last_run?.status ||
      (busy ? "running" : "idle");
    const stepSummary = context?.stepSummary || "";
    const turns = context?.turns ?? fallbackTurns;
    const referencedFiles =
      context?.referencedFiles && context.referencedFiles.length > 0
        ? context.referencedFiles
        : referencedFilePaths;

    return {
      workspaceName,
      workspacePath,
      threadTitle,
      goal,
      runStatus,
      stepSummary,
      turns,
      referencedFiles,
    };
  }, [
    threadID,
    queryClient,
    context,
    workspaces,
    workspaceID,
    projectQuery.data?.directory_path,
    draftText,
    busy,
    referencedFilePaths,
    t,
  ]);

  const minimizePanel = useCallback(() => {
    onClose();
    requestAnimationFrame(() => {
      returnFocusRef.current?.focus();
    });
  }, [onClose, returnFocusRef]);

  const handlePanelKeyDown = (event: ReactKeyboardEvent<HTMLElement>) => {
    if (event.key === "Escape" && !event.nativeEvent.isComposing) {
      event.preventDefault();
      event.stopPropagation();
      minimizePanel();
    }
  };

  const handleModelSelect = (value: string) => {
    setSelectedModelKey(value);
    const parsed = decodeRouteKey(value);
    if (parsed && parsed.provider !== "default") {
      writeStoredSideModel(modelStorageKey, parsed);
    }
  };

  const handleClearSideChat = () => {
    abortControllerRef.current?.abort();
    abortControllerRef.current = null;
    setAsking(false);
    setErrorText("");
    setStatusNotice("");
    onMessagesChange([]);
    requestAnimationFrame(() => inputRef.current?.focus());
  };

  const handleSubmitSideQuestion = async (event?: FormEvent<HTMLFormElement>) => {
    event?.preventDefault();
    event?.stopPropagation();
    const trimmed = question.trim();
    if (!trimmed || asking) return;

    const previousHistory = messages;
    const userMessage: V2SideChatMessage = {
      id: `side-user-${globalThis.crypto.randomUUID()}`,
      role: "user",
      content: trimmed,
      modelLabel: activeModelSelection.label,
      createdAt: new Date().toISOString(),
    };

    setQuestion("");
    setErrorText("");
    setStatusNotice("");
    setAsking(true);
    onMessagesChange((current) => [...current, userMessage]);

    const controller = new AbortController();
    abortControllerRef.current = controller;

    const prompt = buildSideConversationPrompt(
      trimmed,
      snapshot,
      contextScope,
      previousHistory,
      t,
    );
    const askRequest: V2SideChatAskRequest = {
      question: trimmed,
      prompt,
      modelRoute: activeModelSelection,
      contextScope,
      snapshot,
      history: previousHistory,
      signal: controller.signal,
    };

    try {
      const answer = onSideChatAsk
        ? await onSideChatAsk(askRequest)
        : await executeIsolatedSideTurn(client, workspaceID, askRequest, t);
      if (controller.signal.aborted) return;
      const assistantMessage: V2SideChatMessage = {
        id: `side-assistant-${globalThis.crypto.randomUUID()}`,
        role: "assistant",
        content: answer.trim(),
        modelLabel: activeModelSelection.label,
        createdAt: new Date().toISOString(),
      };
      onMessagesChange((current) => [...current, assistantMessage]);
    } catch (failure) {
      if (controller.signal.aborted) return;
      setErrorText(
        failure instanceof Error && failure.message
          ? failure.message
          : t("支线问答请求失败，主任务未受影响，请重试。", "Side query failed; the main task is unaffected. Try again."),
      );
    } finally {
      if (abortControllerRef.current === controller) {
        abortControllerRef.current = null;
        setAsking(false);
      }
    }
  };

  const handleInsertAnswer = (text: string) => {
    onInsertToComposer(text);
    setStatusNotice(t("已插入主输入框（未自动发送）", "Inserted into main composer (not sent)"));
  };

  const handleCopyAnswer = async (id: string, text: string) => {
    try {
      if (navigator?.clipboard?.writeText) {
        await navigator.clipboard.writeText(text);
        setCopiedID(id);
        globalThis.setTimeout(() => {
          setCopiedID((current) => (current === id ? "" : current));
        }, 1600);
      }
    } catch {
      // Ignore clipboard permission rejections.
    }
  };

  if (!open || typeof document === "undefined") return null;

  const sharedTurnCount = contextScope === "full" ? snapshot.turns.length : 0;
  const sharedBadgeText =
    contextScope === "full"
      ? t(
          `已共享上下文: ${sharedTurnCount} 轮对话 · 不打断主任务`,
          `Shared context: ${sharedTurnCount} turns · Zero interruption`,
        )
      : t(
          "已共享上下文: 仅当前目标 · 不打断主任务",
          "Shared context: Current goal only · Zero interruption",
        );

  return createPortal(
    <aside
      aria-label={t("并行支线对话", "Parallel side conversation")}
      className="v2-side-conversation"
      onKeyDown={handlePanelKeyDown}
    >
      <header className="v2-side-header">
        <div className="v2-side-title-row">
          <div className="v2-side-title-group">
            <Split aria-hidden="true" size={16} />
            <h2>{t("并行支线问答", "Parallel Side Chat")}</h2>
          </div>
          <div className="v2-side-header-actions">
            <button
              aria-label={t("新建支线", "New side chat")}
              className="v2-side-icon-btn"
              disabled={messages.length === 0 && !errorText}
              onClick={handleClearSideChat}
              title={t("清空当前支线历史，开启新支线问答", "Clear current side history and start fresh")}
              type="button"
            >
              <RotateCcw aria-hidden="true" size={13} />
              <span>{t("新建支线", "Clear")}</span>
            </button>
            <button
              aria-label={t("最小化支线对话", "Minimize side conversation")}
              className="v2-side-icon-btn"
              onClick={minimizePanel}
              title={t("收起面板并保留支线记录 (Esc)", "Minimize panel and keep side thread (Esc)")}
              type="button"
            >
              <Minimize2 aria-hidden="true" size={14} />
            </button>
          </div>
        </div>

        <div className="v2-side-controls-row">
          <label className="v2-side-model-label">
            <span>{t("支线模型", "Side model")}</span>
            <select
              aria-label={t("支线模型路由", "Side chat model route")}
              className="v2-side-model-select"
              onChange={(event) => handleModelSelect(event.target.value)}
              value={encodeRouteKey(activeModelSelection.provider, activeModelSelection.model)}
            >
              {modelOptions.map((option) => {
                const key = encodeRouteKey(option.provider, option.model);
                return (
                  <option key={key} value={key}>
                    {option.label}
                  </option>
                );
              })}
            </select>
          </label>
        </div>

        <div className="v2-side-context-bar">
          <span className="v2-side-context-badge">
            <Layers aria-hidden="true" size={13} />
            <span>{sharedBadgeText}</span>
          </span>
          <div
            aria-label={t("上下文共享范围", "Context sharing scope")}
            className="v2-side-scope-toggle"
            role="group"
          >
            <button
              aria-pressed={contextScope === "full"}
              className="v2-side-scope-btn"
              onClick={() => setContextScope("full")}
              type="button"
            >
              {t("完整近期历史", "Full history")}
            </button>
            <button
              aria-pressed={contextScope === "goal_only"}
              className="v2-side-scope-btn"
              onClick={() => setContextScope("goal_only")}
              type="button"
            >
              {t("仅当前目标", "Goal only")}
            </button>
          </div>
        </div>

        <details className="v2-side-context-details">
          <summary>{t("查看只读上下文快照", "Inspect read-only context snapshot")}</summary>
          <dl className="v2-side-context-snapshot">
            <div>
              <dt>{t("工作区", "Workspace")}</dt>
              <dd title={snapshot.workspacePath}>{snapshot.workspacePath}</dd>
            </div>
            <div>
              <dt>{t("任务目标", "Goal")}</dt>
              <dd title={snapshot.goal}>{snapshot.goal}</dd>
            </div>
            <div>
              <dt>{t("主任务状态", "Main run")}</dt>
              <dd>
                {snapshot.runStatus}
                {snapshot.stepSummary ? ` · ${snapshot.stepSummary}` : ""}
              </dd>
            </div>
            {snapshot.referencedFiles.length > 0 && (
              <div>
                <dt>{t("引用文件", "Files")}</dt>
                <dd title={snapshot.referencedFiles.join(", ")}>
                  {snapshot.referencedFiles.join(", ")}
                </dd>
              </div>
            )}
          </dl>
        </details>
      </header>

      <div className="v2-side-messages" ref={messagesContainerRef}>
        {messages.length === 0 && !asking ? (
          <div className="v2-side-empty">
            <Split aria-hidden="true" size={22} />
            <p>
              {busy
                ? t(
                    "主智能体正在执行任务。你可以在这里调用任意模型探讨代码细节、排查思路或生成片段，绝不打断主任务，也不会污染主对话历史。",
                    "The main agent is busy. Ask any model here to explore code details, debug ideas, or draft snippets without interrupting the main run or polluting the main transcript.",
                  )
                : t(
                    "随时开启独立支线问答：自动共享当前对话与代码上下文，不写入主对话记录，支持一键将结论插入主输入框。",
                    "Start a parallel side Q&A anytime: shares current conversation context without writing to the main transcript, with one-click insertion back into the main composer.",
                  )}
            </p>
          </div>
        ) : (
          messages.map((item) => (
            <div
              className={`v2-side-turn ${item.role === "user" ? "is-user" : "is-assistant"}`}
              key={item.id}
            >
              {item.role === "user" ? (
                <div className="v2-side-bubble">{item.content}</div>
              ) : (
                <>
                  <div className="v2-side-bubble">
                    <ReactMarkdown remarkPlugins={[remarkGfm]}>{item.content}</ReactMarkdown>
                  </div>
                  <div className="v2-side-turn-meta">
                    {item.modelLabel && <span>{item.modelLabel}</span>}
                    <div className="v2-side-turn-actions">
                      <button
                        className="v2-side-action-chip"
                        onClick={() => handleInsertAnswer(item.content)}
                        type="button"
                      >
                        <ArrowDownLeft aria-hidden="true" size={12} />
                        <span>{t("复制至主输入框", "Insert into main composer")}</span>
                      </button>
                      <button
                        className="v2-side-action-chip"
                        onClick={() => void handleCopyAnswer(item.id, item.content)}
                        type="button"
                      >
                        {copiedID === item.id ? (
                          <Check aria-hidden="true" size={12} />
                        ) : (
                          <Copy aria-hidden="true" size={12} />
                        )}
                        <span>
                          {copiedID === item.id ? t("已复制", "Copied") : t("复制", "Copy")}
                        </span>
                      </button>
                    </div>
                  </div>
                </>
              )}
            </div>
          ))
        )}

        {asking && (
          <div className="v2-side-loading" role="status">
            <LoaderCircle aria-hidden="true" className="spin" size={15} />
            <span>
              {t(
                `正在通过 ${activeModelSelection.label} 独立解答…`,
                `Answering independently via ${activeModelSelection.label}…`,
              )}
            </span>
          </div>
        )}

        {errorText && (
          <p className="v2-side-error" role="alert">
            {errorText}
          </p>
        )}
      </div>

      {statusNotice && (
        <p className="v2-side-status" role="status">
          {statusNotice}
        </p>
      )}

      <form className="v2-side-form" onSubmit={(event) => void handleSubmitSideQuestion(event)}>
        <textarea
          aria-label={t("支线问题输入", "Side question input")}
          disabled={asking}
          onChange={(event) => {
            setQuestion(event.target.value);
            if (statusNotice) setStatusNotice("");
          }}
          onKeyDown={(event) => {
            if (
              event.key === "Enter" &&
              !event.shiftKey &&
              !event.nativeEvent.isComposing
            ) {
              event.preventDefault();
              void handleSubmitSideQuestion();
            }
          }}
          placeholder={t(
            "输入支线问题（Enter 发送，Shift+Enter 换行，Esc 收起）…",
            "Ask a side question (Enter to send, Shift+Enter for newline, Esc to minimize)…",
          )}
          ref={inputRef}
          rows={2}
          value={question}
        />
        <button
          aria-label={t("发送支线问题", "Send side question")}
          className="v2-side-send-btn"
          disabled={!question.trim() || asking}
          type="submit"
        >
          <ArrowUp aria-hidden="true" size={16} />
        </button>
      </form>
    </aside>,
    document.body,
  );
}
