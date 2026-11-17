import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { AgentBrowserStatus } from "../api/agent-browser";
import type { PublicModelStreamSnapshot, RunEventStreamView, ThreadTranscriptItemView } from "../api/types";
import type { SharedBrowserState } from "../lib/desktop-shared-browser";
import { useLocale, type LocaleText } from "../lib/locale";
import { focusWorkspaceSingletonTab } from "./workspace-tabs";

export type BrowserActionKind =
  | "navigate"
  | "click"
  | "type"
  | "move"
  | "scroll"
  | "inspect"
  | "screenshot"
  | "other";

export interface BrowserCursorPoint {
  x: number;
  y: number;
  percentX: number;
  percentY: number;
  viewportWidth: number;
  viewportHeight: number;
  actionKind: BrowserActionKind;
  targetLabel?: string;
  ripple: boolean;
  sequenceKey: string;
  simulated?: boolean;
}

export interface BrowserOperationStep {
  id: string;
  index: number;
  actionKind: BrowserActionKind;
  toolName: string;
  label: string;
  detail?: string;
  status: "running" | "completed" | "failed";
  coordinates?: { x: number; y: number };
}

export interface BrowserCollaborationState {
  hasBrowserActivity: boolean;
  agentOperating: boolean;
  manualTakeover: boolean;
  manualCloseOverride: boolean;
  cursor: BrowserCursorPoint | null;
  steps: BrowserOperationStep[];
  activeUrl?: string;
  activeTitle?: string;
  notifyManualClose: () => void;
  triggerStopAndTakeOver: () => Promise<void>;
  resumeWithAgent: () => void;
}

export interface UseAutoBrowserDockOptions {
  threadID: string;
  runID: string;
  runActive: boolean;
  enabled?: boolean;
  isBrowserOpen?: boolean;
  transcriptItems?: readonly ThreadTranscriptItemView[];
  liveSnapshot?: PublicModelStreamSnapshot | null;
  eventFrames?: readonly RunEventStreamView[];
  agentBrowserStatus?: Partial<AgentBrowserStatus> | null;
  sharedBrowserState?: Partial<SharedBrowserState> | null;
  onAutoOpenBrowser?: () => void;
  onCancelRun?: () => Promise<unknown> | unknown;
  onResumeAgent?: () => void;
}

const DEFAULT_VIEWPORT = { width: 1280, height: 800 } as const;

const BROWSER_TOOL_PATTERN =
  /^(?:browser[_.:-]|agent_browser[_.:-]|playwright[_.:-]|webview[_.:-]|cdp[_.:-]|ui_evidence$|fill_form$|submit_form$|open_page$|inspect_dom$)/iu;

const POINTER_EVENT_PATTERN =
  /^(?:browser|agent_browser|webview|playwright)[_.:-](?:pointer|mouse|cursor|click|move|type|fill|navigate|scroll|action|step)/iu;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function isBrowserToolName(name: string | undefined | null): boolean {
  if (!name) return false;
  return BROWSER_TOOL_PATTERN.test(name.trim());
}

export function classifyBrowserAction(toolName: string, rawHint = ""): BrowserActionKind {
  const lower = `${toolName} ${rawHint}`.toLowerCase();
  if (/(navigate|goto|open_url|open_page|visit|reload|back|forward)/u.test(lower)) return "navigate";
  if (/(type|fill|input|form|select|Option|write_field|enter_text)/iu.test(lower)) return "type";
  if (/(click|tap|press_button|submit)/u.test(lower)) return "click";
  if (/(move|hover|pointer|cursor)/u.test(lower)) return "move";
  if (/(scroll|wheel|swipe)/u.test(lower)) return "scroll";
  if (/(screenshot|capture|ui_evidence)/u.test(lower)) return "screenshot";
  if (/(snapshot|dom|inspect|query|read_page|accessibility)/u.test(lower)) return "inspect";
  return "other";
}

function parseJSONRecord(raw: unknown): Record<string, unknown> | null {
  if (isRecord(raw)) return raw;
  if (typeof raw !== "string") return null;
  const trimmed = raw.trim();
  if (!trimmed.startsWith("{") || !trimmed.endsWith("}")) return null;
  try {
    const parsed: unknown = JSON.parse(trimmed);
    return isRecord(parsed) ? parsed : null;
  } catch {
    return null;
  }
}

function finiteNumber(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function extractCoordinates(source: Record<string, unknown> | null, fallbackText = ""): {
  x: number;
  y: number;
  viewportWidth: number;
  viewportHeight: number;
} | null {
  if (source) {
    const nestedArgs =
      parseJSONRecord(source.arguments) ??
      parseJSONRecord(source.args) ??
      parseJSONRecord(source.input) ??
      parseJSONRecord(source.payload);
    const bag = nestedArgs ? { ...source, ...nestedArgs } : source;
    const point = isRecord(bag.point) ? bag.point : isRecord(bag.position) ? bag.position : null;
    const coordTuple = Array.isArray(bag.coordinate) && bag.coordinate.length >= 2 ? bag.coordinate : null;
    const x =
      finiteNumber(point?.x) ??
      finiteNumber(bag.x) ??
      finiteNumber(bag.clientX) ??
      (coordTuple ? finiteNumber(coordTuple[0]) : undefined);
    const y =
      finiteNumber(point?.y) ??
      finiteNumber(bag.y) ??
      finiteNumber(bag.clientY) ??
      (coordTuple ? finiteNumber(coordTuple[1]) : undefined);
    if (x !== undefined && y !== undefined) {
      const viewport =
        (point && isRecord(point.viewport) ? point.viewport : null) ??
        (isRecord(bag.viewport) ? bag.viewport : null);
      const viewportWidth =
        finiteNumber(viewport?.width) ?? finiteNumber(bag.viewport_width) ?? finiteNumber(bag.width) ?? DEFAULT_VIEWPORT.width;
      const viewportHeight =
        finiteNumber(viewport?.height) ?? finiteNumber(bag.viewport_height) ?? finiteNumber(bag.height) ?? DEFAULT_VIEWPORT.height;
      return {
        x: Math.round(x),
        y: Math.round(y),
        viewportWidth: Math.max(1, viewportWidth),
        viewportHeight: Math.max(1, viewportHeight),
      };
    }
  }
  if (fallbackText) {
    const jsonCandidate = parseJSONRecord(fallbackText);
    if (jsonCandidate) return extractCoordinates(jsonCandidate, "");
    const match = fallbackText.match(/\(\s*(\d{1,4})\s*,\s*(\d{1,4})\s*\)/u) ??
      fallbackText.match(/"x"\s*:\s*(\d{1,4})\s*,\s*"y"\s*:\s*(\d{1,4})/u);
    if (match) {
      return {
        x: Number(match[1]),
        y: Number(match[2]),
        viewportWidth: DEFAULT_VIEWPORT.width,
        viewportHeight: DEFAULT_VIEWPORT.height,
      };
    }
  }
  return null;
}

function extractTargetHint(source: Record<string, unknown> | null, detailText = ""): {
  targetLabel?: string;
  url?: string;
} {
  const nested =
    source &&
    (parseJSONRecord(source.arguments) ??
      parseJSONRecord(source.args) ??
      parseJSONRecord(source.input) ??
      parseJSONRecord(source.payload));
  const bag = source ? (nested ? { ...source, ...nested } : source) : parseJSONRecord(detailText);

  let url: string | undefined;
  let targetLabel: string | undefined;

  if (bag) {
    if (typeof bag.url === "string" && bag.url.trim()) url = bag.url.trim();
    const target = isRecord(bag.target) ? bag.target : null;
    const rawTarget =
      (typeof bag.label === "string" && bag.label.trim()) ||
      (typeof bag.step_label === "string" && bag.step_label.trim()) ||
      (target && typeof target.name === "string" && target.name.trim()) ||
      (target && typeof target.label === "string" && target.label.trim()) ||
      (typeof bag.field === "string" && bag.field.trim()) ||
      (typeof bag.selector === "string" && bag.selector.trim()) ||
      (typeof bag.element_ref === "string" && bag.element_ref.trim()) ||
      (typeof bag.key === "string" && bag.key.trim()) ||
      "";
    if (rawTarget) targetLabel = rawTarget.slice(0, 48);
  }

  if (!url && detailText) {
    const urlMatch = detailText.match(/https?:\/\/[^\s)"'`]+/u);
    if (urlMatch) url = urlMatch[0];
  }

  return { ...(targetLabel ? { targetLabel } : {}), ...(url ? { url } : {}) };
}

function shortUrlDisplay(rawUrl: string | undefined): string {
  if (!rawUrl) return "";
  try {
    const parsed = new URL(rawUrl);
    const path = parsed.pathname && parsed.pathname !== "/" ? parsed.pathname : "";
    return `${parsed.host}${path}`.slice(0, 42);
  } catch {
    return rawUrl.slice(0, 42);
  }
}

function formatOperationLabel(
  actionKind: BrowserActionKind,
  toolName: string,
  rawTitle: string | undefined,
  targetLabel: string | undefined,
  url: string | undefined,
  t: LocaleText,
): string {
  const cleanedTitle = rawTitle?.trim() ?? "";
  const genericTitle =
    !cleanedTitle ||
    cleanedTitle === toolName ||
    /^(?:工具执行|Tool execution|browser_|playwright_|agent_browser)/iu.test(cleanedTitle);

  if (!genericTitle) {
    return cleanedTitle;
  }

  const pageTarget = targetLabel || shortUrlDisplay(url);
  switch (actionKind) {
    case "navigate":
      return pageTarget
        ? t(`打开 ${pageTarget}`, `Open ${pageTarget}`)
        : t("打开目标网页", "Open target page");
    case "type":
      return targetLabel
        ? t(`填写${targetLabel}`, `Fill ${targetLabel}`)
        : t("填写网页表单", "Fill page form");
    case "click":
      return targetLabel
        ? t(`点击${targetLabel}`, `Click ${targetLabel}`)
        : t("点击页面元素", "Click page element");
    case "move":
      return targetLabel
        ? t(`移动光标至 ${targetLabel}`, `Move cursor to ${targetLabel}`)
        : t("移动页面光标", "Move page cursor");
    case "scroll":
      return t("滚动浏览页面", "Scroll page");
    case "screenshot":
      return t("捕获页面画面", "Capture page view");
    case "inspect":
      return t("读取页面结构", "Inspect page DOM");
    default:
      return targetLabel || t("执行网页操作", "Perform browser action");
  }
}

function normalizeStepStatus(status: string | undefined, stage?: string): "running" | "completed" | "failed" {
  const lower = `${status ?? ""} ${stage ?? ""}`.toLowerCase();
  if (/(fail|error|deny|denied|cancel|block|timeout)/u.test(lower)) return "failed";
  if (/(complete|success|done|pass|result|ready_for_validation)/u.test(lower)) return "completed";
  return "running";
}

function simulatedCoordinatesForStep(index: number, actionKind: BrowserActionKind): {
  x: number;
  y: number;
  viewportWidth: number;
  viewportHeight: number;
} {
  const presets: Array<[number, number]> = [
    [420, 220],
    [520, 340],
    [610, 460],
    [480, 520],
    [360, 300],
  ];
  const [baseX, baseY] = presets[(Math.max(1, index) - 1) % presets.length]!;
  const offset = actionKind === "type" ? 24 : actionKind === "click" ? 12 : 0;
  return {
    x: baseX + offset,
    y: baseY + offset,
    viewportWidth: DEFAULT_VIEWPORT.width,
    viewportHeight: DEFAULT_VIEWPORT.height,
  };
}

function toCursorPoint(
  coords: { x: number; y: number; viewportWidth: number; viewportHeight: number },
  actionKind: BrowserActionKind,
  sequenceKey: string,
  targetLabel?: string,
  simulated = false,
): BrowserCursorPoint {
  const percentX = Math.max(6, Math.min(94, (coords.x / Math.max(1, coords.viewportWidth)) * 100));
  const percentY = Math.max(8, Math.min(92, (coords.y / Math.max(1, coords.viewportHeight)) * 100));
  return {
    x: coords.x,
    y: coords.y,
    percentX,
    percentY,
    viewportWidth: coords.viewportWidth,
    viewportHeight: coords.viewportHeight,
    actionKind,
    ...(targetLabel ? { targetLabel } : {}),
    ripple: actionKind === "click" || actionKind === "type",
    sequenceKey,
    ...(simulated ? { simulated: true } : {}),
  };
}

export function useAutoBrowserDock({
  threadID,
  runID,
  runActive,
  enabled = true,
  isBrowserOpen,
  transcriptItems = [],
  liveSnapshot = null,
  eventFrames = [],
  agentBrowserStatus = null,
  sharedBrowserState = null,
  onAutoOpenBrowser,
  onCancelRun,
  onResumeAgent,
}: UseAutoBrowserDockOptions): BrowserCollaborationState {
  const { t } = useLocale();
  const [dismissedScope, setDismissedScope] = useState<string | null>(null);
  const [takeoverRunID, setTakeoverRunID] = useState<string | null>(null);
  const autoOpenedScopeRef = useRef<string | null>(null);
  const observedOpenScopeRef = useRef<string | null>(null);

  useEffect(() => {
    setDismissedScope(null);
    setTakeoverRunID(null);
    autoOpenedScopeRef.current = null;
    observedOpenScopeRef.current = null;
  }, [threadID]);

  const latestTaskKey = useMemo(() => {
    for (let i = transcriptItems.length - 1; i >= 0; i--) {
      const item = transcriptItems[i]!;
      if (runID && item.run_id && item.run_id !== runID) continue;
      if (item.task?.id) return item.task.id;
      if (item.source === "operator" && item.kind === "operator_input") {
        return item.canonical_id || item.id;
      }
    }
    return "initial";
  }, [runID, transcriptItems]);

  const currentScopeKey = `${runID || "no-run"}::${latestTaskKey}`;

  const derived = useMemo(() => {
    const stepMap = new Map<string, Omit<BrowserOperationStep, "index">>();
    const stepOrder: string[] = [];
    let latestCursor: BrowserCursorPoint | null = null;
    let activeUrl = agentBrowserStatus?.url;
    let activeTitle = agentBrowserStatus?.title;
    let hasCurrentRunActivity = false;

    const upsertStep = (key: string, candidate: Omit<BrowserOperationStep, "index">) => {
      if (!stepMap.has(key)) {
        stepOrder.push(key);
      }
      const existing = stepMap.get(key);
      stepMap.set(key, {
        ...existing,
        ...candidate,
        label: candidate.label || existing?.label || "",
        detail: candidate.detail || existing?.detail,
        coordinates: candidate.coordinates ?? existing?.coordinates,
      });
    };

    for (const item of transcriptItems) {
      if (runID && item.run_id !== runID) continue;
      const browserItem = item.kind === "browser" || isBrowserToolName(item.tool_name);
      if (!browserItem) continue;
      hasCurrentRunActivity = true;
      const toolName = item.tool_name || "browser_action";
      const actionKind = classifyBrowserAction(toolName, `${item.title} ${item.detail ?? ""}`);
      const { targetLabel, url } = extractTargetHint(null, item.detail ?? item.web_evidence?.url ?? "");
      if (url) activeUrl = url;
      const coords = extractCoordinates(null, item.detail ?? "");
      const key = item.canonical_id || item.durable_call_id || item.stream_call_id || item.id;
      const status = normalizeStepStatus(item.status, item.stage);

      if (actionKind !== "move") {
        upsertStep(key, {
          id: key,
          actionKind,
          toolName,
          label: formatOperationLabel(actionKind, toolName, item.title, targetLabel, url, t),
          detail: item.detail?.trim() || url,
          status,
          ...(coords ? { coordinates: { x: coords.x, y: coords.y } } : {}),
        });
      }

      const effectiveCoords = coords ?? simulatedCoordinatesForStep(stepOrder.length || 1, actionKind);
      latestCursor = toCursorPoint(
        effectiveCoords,
        actionKind,
        `${key}:${item.sequence}:${status}`,
        targetLabel || shortUrlDisplay(url),
        !coords,
      );
    }

    if (liveSnapshot && (!runID || liveSnapshot.call.run_id === runID)) {
      for (const item of liveSnapshot.items) {
        if (item.type !== "tool_call" || !isBrowserToolName(item.tool_name)) continue;
        hasCurrentRunActivity = true;
        const toolName = item.tool_name ?? "browser_action";
        const actionKind = classifyBrowserAction(toolName);
        const key = item.durable_call_id || item.call_id || item.id;
        const status = normalizeStepStatus(item.status);
        if (actionKind !== "move" && !stepMap.has(key)) {
          upsertStep(key, {
            id: key,
            actionKind,
            toolName,
            label: formatOperationLabel(actionKind, toolName, undefined, undefined, activeUrl, t),
            status,
          });
        }
        const effectiveCoords = simulatedCoordinatesForStep(stepOrder.length || 1, actionKind);
        latestCursor = toCursorPoint(effectiveCoords, actionKind, `live:${key}:${item.status}`, undefined, true);
      }
    }

    for (const frame of eventFrames) {
      if (runID && frame.run_id !== runID) continue;
      const payload = parseJSONRecord(frame.event.payload);
      const eventToolName =
        (typeof payload?.tool_name === "string" && payload.tool_name) ||
        (typeof payload?.name === "string" && payload.name) ||
        (typeof payload?.action === "string" && payload.action) ||
        "";
      const isPointerOrBrowserEvent =
        POINTER_EVENT_PATTERN.test(frame.event.type) ||
        isBrowserToolName(eventToolName) ||
        frame.event.type.startsWith("browser.") ||
        frame.event.type.startsWith("agent_browser.");
      if (!isPointerOrBrowserEvent) continue;

      hasCurrentRunActivity = true;
      const effectiveName = eventToolName || frame.event.type;
      const actionKind = classifyBrowserAction(effectiveName, JSON.stringify(payload ?? {}));
      const { targetLabel, url } = extractTargetHint(payload);
      if (url) activeUrl = url;
      if (typeof payload?.title === "string" && payload.title.trim()) {
        activeTitle = payload.title.trim();
      }
      const coords = extractCoordinates(payload);
      const callKey =
        (typeof payload?.call_id === "string" && payload.call_id) ||
        (typeof payload?.step_id === "string" && payload.step_id) ||
        `event:${frame.sequence}`;
      const status = normalizeStepStatus(
        typeof payload?.status === "string" ? payload.status : undefined,
        typeof payload?.stage === "string" ? payload.stage : undefined,
      );

      if (actionKind !== "move") {
        const rawTitle = typeof payload?.title === "string" ? payload.title : undefined;
        upsertStep(callKey, {
          id: callKey,
          actionKind,
          toolName: effectiveName,
          label: formatOperationLabel(actionKind, effectiveName, rawTitle, targetLabel, url, t),
          detail: url || targetLabel,
          status,
          ...(coords ? { coordinates: { x: coords.x, y: coords.y } } : {}),
        });
      }

      const effectiveCoords = coords ?? simulatedCoordinatesForStep(stepOrder.length || 1, actionKind);
      latestCursor = toCursorPoint(
        effectiveCoords,
        actionKind,
        `frame:${frame.sequence}:${actionKind}`,
        targetLabel || shortUrlDisplay(url),
        !coords,
      );
    }

    if (
      agentBrowserStatus &&
      (!runID || !agentBrowserStatus.run_id || agentBrowserStatus.run_id === runID) &&
      agentBrowserStatus.state &&
      ["starting", "ready", "loading", "busy", "waiting_user"].includes(agentBrowserStatus.state)
    ) {
      if (agentBrowserStatus.last_action || agentBrowserStatus.state === "busy" || agentBrowserStatus.state === "loading") {
        hasCurrentRunActivity = true;
      }
      if (agentBrowserStatus.last_action && stepOrder.length === 0) {
        const actionKind = classifyBrowserAction(agentBrowserStatus.last_action);
        const key = `status:${agentBrowserStatus.session_id ?? "browser"}:${agentBrowserStatus.document_epoch ?? 0}:${agentBrowserStatus.last_action}`;
        upsertStep(key, {
          id: key,
          actionKind,
          toolName: agentBrowserStatus.last_action,
          label: formatOperationLabel(
            actionKind,
            agentBrowserStatus.last_action,
            undefined,
            agentBrowserStatus.title,
            agentBrowserStatus.url,
            t,
          ),
          status: agentBrowserStatus.state === "busy" || agentBrowserStatus.state === "loading" ? "running" : "completed",
        });
        if (!latestCursor) {
          latestCursor = toCursorPoint(
            simulatedCoordinatesForStep(1, actionKind),
            actionKind,
            key,
            agentBrowserStatus.title || shortUrlDisplay(agentBrowserStatus.url),
            true,
          );
        }
      }
    }

    if (sharedBrowserState?.control_owner === "agent" && (!runID || !sharedBrowserState.run_id || sharedBrowserState.run_id === runID)) {
      hasCurrentRunActivity = true;
      const agentTab = sharedBrowserState.tabs?.find((tab) => tab.tab_id === sharedBrowserState.agent_tab_id);
      if (agentTab) {
        activeUrl = activeUrl || agentTab.url;
        activeTitle = activeTitle || agentTab.title;
      }
    }

    const steps: BrowserOperationStep[] = stepOrder.map((key, idx) => ({
      ...stepMap.get(key)!,
      index: idx + 1,
    }));

    const hasRunningStep = steps.some((step) => step.status === "running");
    const statusOperating =
      agentBrowserStatus?.state === "starting" ||
      agentBrowserStatus?.state === "loading" ||
      agentBrowserStatus?.state === "busy" ||
      sharedBrowserState?.control_owner === "agent";

    return {
      hasBrowserActivity: hasCurrentRunActivity,
      steps,
      cursor: latestCursor,
      activeUrl,
      activeTitle,
      hasRunningStep,
      statusOperating,
    };
  }, [agentBrowserStatus, eventFrames, liveSnapshot, runID, sharedBrowserState, t, transcriptItems]);

  const manualTakeover = Boolean(takeoverRunID && takeoverRunID === (runID || "no-run"));
  const manualCloseOverride = dismissedScope === currentScopeKey;
  const agentOperating =
    !manualTakeover &&
    derived.hasBrowserActivity &&
    (runActive || derived.hasRunningStep || Boolean(derived.statusOperating));

  // Detect implicit user close when the browser tab/dock transitions from open -> closed during the same scope.
  useEffect(() => {
    if (isBrowserOpen === undefined) return;
    if (isBrowserOpen) {
      observedOpenScopeRef.current = currentScopeKey;
      return;
    }
    if (observedOpenScopeRef.current === currentScopeKey && autoOpenedScopeRef.current === currentScopeKey) {
      setDismissedScope(currentScopeKey);
    }
  }, [currentScopeKey, isBrowserOpen]);

  useEffect(() => {
    if (!enabled || !threadID || !derived.hasBrowserActivity) return;
    if (!runActive && !derived.hasRunningStep && !derived.statusOperating) return;
    if (manualCloseOverride || manualTakeover) return;
    if (autoOpenedScopeRef.current === currentScopeKey) return;

    autoOpenedScopeRef.current = currentScopeKey;
    if (!isBrowserOpen) {
      if (onAutoOpenBrowser) {
        onAutoOpenBrowser();
      } else {
        focusWorkspaceSingletonTab(threadID, "browser");
      }
    }
  }, [
    currentScopeKey,
    derived.hasBrowserActivity,
    derived.hasRunningStep,
    derived.statusOperating,
    enabled,
    isBrowserOpen,
    manualCloseOverride,
    manualTakeover,
    onAutoOpenBrowser,
    runActive,
    threadID,
  ]);

  const notifyManualClose = useCallback(() => {
    setDismissedScope(currentScopeKey);
  }, [currentScopeKey]);

  const triggerStopAndTakeOver = useCallback(async () => {
    setTakeoverRunID(runID || "no-run");
    await onCancelRun?.();
  }, [onCancelRun, runID]);

  const resumeWithAgent = useCallback(() => {
    setTakeoverRunID(null);
    setDismissedScope(null);
    onResumeAgent?.();
  }, [onResumeAgent]);

  return {
    hasBrowserActivity: derived.hasBrowserActivity,
    agentOperating,
    manualTakeover,
    manualCloseOverride,
    cursor: derived.cursor,
    steps: derived.steps,
    ...(derived.activeUrl ? { activeUrl: derived.activeUrl } : {}),
    ...(derived.activeTitle ? { activeTitle: derived.activeTitle } : {}),
    notifyManualClose,
    triggerStopAndTakeOver,
    resumeWithAgent,
  };
}
