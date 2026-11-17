import { useCallback, useEffect, useId, useMemo, useRef, useState, type RefObject } from "react";
import { CircleAlert, Search, X } from "lucide-react";
import { useLocale, type LocaleText } from "../../lib/locale";
import type { NarrativeEntry } from "../projection/narrative";
import { findConversationEntryElement } from "../use-scroll-memory";
import "./navigator-rail.css";

export type NavigatorStopKind = "turn" | "checkpoint" | "error";
export type NavigatorSectionFilter = "all" | "checkpoints" | "errors";

export interface NavigatorStop {
  id: string;
  entryKey: string;
  kind: NavigatorStopKind;
  turnNumber: number;
  tagLabel: string;
  createdAt: string;
  promptPreview: string;
  outcomePreview: string;
  hasError?: boolean;
}

type UILocale = ReturnType<typeof useLocale>["locale"];

const timeFormats: Record<UILocale, Intl.DateTimeFormat> = {
  "zh-CN": new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit", hourCycle: "h23" }),
  "en-US": new Intl.DateTimeFormat("en-US", { hour: "2-digit", minute: "2-digit", hourCycle: "h23" }),
};

const fullTimeFormats: Record<UILocale, Intl.DateTimeFormat> = {
  "zh-CN": new Intl.DateTimeFormat("zh-CN", { dateStyle: "medium", timeStyle: "short" }),
  "en-US": new Intl.DateTimeFormat("en-US", { dateStyle: "medium", timeStyle: "short" }),
};

export function cleanNavigatorPreviewText(raw: string): string {
  if (!raw) return "";
  return raw
    .replace(/```[\s\S]*?```/gu, " [code] ")
    .replace(/`([^`]+)`/gu, "$1")
    .replace(/!\[([^\]]*)\]\([^)]*\)/gu, "$1")
    .replace(/\[([^\]]+)\]\([^)]*\)/gu, "$1")
    .replace(/^#{1,6}\s+/gmu, "")
    .replace(/[*_~>]+/gu, " ")
    .replace(/\s+/gu, " ")
    .trim();
}

function isFailureStatus(status: string | undefined): boolean {
  if (!status) return false;
  return /(?:^|[^a-z])(fail|failed|error|blocked|denied|deny|timeout|rejected|verification_unavailable)(?:$|[^a-z])/iu.test(status);
}

function isCompletedVerifyStatus(status: string | undefined): boolean {
  if (!status) return true;
  return /(complete|completed|pass|passed|success|result|verified)/iu.test(status) && !isFailureStatus(status);
}

function isSkippedUserEntry(entry: NarrativeEntry): boolean {
  return entry.kind === "user" && entry.status === "cancelled" && Boolean(entry.promotedToMessageID);
}

export function extractNavigatorStops(
  entries: ReadonlyArray<NarrativeEntry>,
  t: LocaleText = (zh) => zh,
): NavigatorStop[] {
  const stops: NavigatorStop[] = [];
  let currentTurn = 0;
  let currentPrompt = "";

  for (let index = 0; index < entries.length; index++) {
    const entry = entries[index]!;
    if (isSkippedUserEntry(entry)) continue;

    if (entry.kind === "user") {
      currentTurn += 1;
      const promptText = cleanNavigatorPreviewText(entry.text) || t("（附件消息）", "(Attachment message)");
      currentPrompt = promptText;

      let latestReply = "";
      let fallbackNotice = "";
      let emittedErrorInTurn = false;
      for (let lookahead = index + 1; lookahead < entries.length; lookahead++) {
        const next = entries[lookahead]!;
        if (next.kind === "user" && !isSkippedUserEntry(next)) break;
        if (next.kind === "assistant" && next.text.trim()) {
          latestReply = next.text;
        } else if (next.kind === "notice" && (next.tone === "warning" || Boolean(next.failureOrigin))) {
          fallbackNotice = next.text;
          emittedErrorInTurn = true;
        } else if (next.kind === "activity") {
          const activityFailed = isFailureStatus(next.status) ||
            next.items.some((item) => isFailureStatus(item.status) || item.fileChange === "failed" || item.fileChange === "denied");
          if (activityFailed) emittedErrorInTurn = true;
        }
      }

      const taskFailed = entry.task?.state === "failed";
      const outcomePreview = cleanNavigatorPreviewText(latestReply || fallbackNotice) || (
        entry.provisional || entry.task?.state === "running"
          ? t("正在处理…", "In progress…")
          : t("暂无回复摘要", "No reply summary yet")
      );
      const tagLabel = entry.deliveryMode === "steer"
        ? t("引导提问", "Guidance")
        : taskFailed
          ? t("回合失败", "Turn failed")
          : entry.provisional || entry.task?.state === "running"
            ? t("进行中", "In progress")
            : t("对话回合", "Prompt turn");

      stops.push({
        id: `turn:${entry.id}`,
        entryKey: entry.id,
        kind: "turn",
        turnNumber: currentTurn,
        tagLabel,
        createdAt: entry.createdAt,
        promptPreview: promptText,
        outcomePreview,
        ...(taskFailed && !emittedErrorInTurn ? { hasError: true } : {}),
      });
      continue;
    }

    if (entry.kind === "activity") {
      const failedLeaf = entry.items.find((item) =>
        isFailureStatus(item.status) || item.fileChange === "failed" || item.fileChange === "denied");
      if (isFailureStatus(entry.status) || failedLeaf) {
        const detail = cleanNavigatorPreviewText(
          [failedLeaf?.title || entry.title, failedLeaf?.detail || entry.detail].filter(Boolean).join(" · "),
        );
        stops.push({
          id: `error:${entry.id}`,
          entryKey: entry.id,
          kind: "error",
          turnNumber: currentTurn,
          tagLabel: t("工具失败", "Tool failure"),
          createdAt: entry.createdAt,
          promptPreview: currentPrompt || cleanNavigatorPreviewText(entry.title),
          outcomePreview: detail || t("工具执行返回错误", "Tool execution reported an error"),
        });
        continue;
      }

      const hasFileCheckpoint = entry.items.some((item) =>
        item.fileChange === "applied" || item.fileChange === "approved" || item.fileChange === "proposed");
      const isPlanPhase = entry.activity === "plan";
      const isVerifyCheckpoint = entry.activity === "verify" && isCompletedVerifyStatus(entry.status);
      const isExplicitMilestone = /checkpoint|phase|milestone|检查点|阶段|里程碑/iu.test(`${entry.title} ${entry.detail}`);

      if (isPlanPhase || hasFileCheckpoint || isVerifyCheckpoint || isExplicitMilestone) {
        const tagLabel = isPlanPhase
          ? t("阶段 · 计划", "Phase · Plan")
          : hasFileCheckpoint
            ? t("检查点 · 变更", "Checkpoint · Changes")
            : isVerifyCheckpoint
              ? t("检查点 · 验证", "Checkpoint · Verify")
              : t("阶段节点", "Phase milestone");
        const detail = cleanNavigatorPreviewText([entry.title, entry.detail].filter(Boolean).join(" · "));
        stops.push({
          id: `checkpoint:${entry.id}`,
          entryKey: entry.id,
          kind: "checkpoint",
          turnNumber: currentTurn,
          tagLabel,
          createdAt: entry.createdAt,
          promptPreview: currentPrompt || cleanNavigatorPreviewText(entry.title),
          outcomePreview: detail,
        });
      }
      continue;
    }

    if (entry.kind === "notice") {
      if (entry.tone === "warning" || Boolean(entry.failureOrigin)) {
        stops.push({
          id: `error:${entry.id}`,
          entryKey: entry.id,
          kind: "error",
          turnNumber: currentTurn,
          tagLabel: t("错误告警", "Error"),
          createdAt: entry.createdAt,
          promptPreview: currentPrompt || t("系统运行提示", "Runtime notice"),
          outcomePreview: cleanNavigatorPreviewText(entry.text),
        });
      } else if (entry.tone === "success") {
        stops.push({
          id: `checkpoint:${entry.id}`,
          entryKey: entry.id,
          kind: "checkpoint",
          turnNumber: currentTurn,
          tagLabel: t("检查点 · 完成", "Checkpoint · Done"),
          createdAt: entry.createdAt,
          promptPreview: currentPrompt || t("阶段完成", "Milestone completed"),
          outcomePreview: cleanNavigatorPreviewText(entry.text),
        });
      }
      continue;
    }

    if (entry.kind === "peer") {
      stops.push({
        id: `checkpoint:${entry.id}`,
        entryKey: entry.id,
        kind: "checkpoint",
        turnNumber: currentTurn,
        tagLabel: t("协作节点", "Peer handoff"),
        createdAt: entry.createdAt,
        promptPreview: currentPrompt || cleanNavigatorPreviewText(entry.message.source_title || "Peer"),
        outcomePreview: cleanNavigatorPreviewText(entry.text),
      });
    }
  }

  return stops;
}

export function filterNavigatorStops(
  stops: ReadonlyArray<NavigatorStop>,
  section: NavigatorSectionFilter,
  query: string,
): NavigatorStop[] {
  const normalizedQuery = query.trim().toLowerCase();
  return stops.filter((stop) => {
    const matchesSection = section === "all"
      ? true
      : section === "checkpoints"
        ? stop.kind === "checkpoint"
        : stop.kind === "error" || Boolean(stop.hasError);
    if (!matchesSection) return false;
    if (!normalizedQuery) return true;
    const turnToken = stop.turnNumber > 0 ? `#${stop.turnNumber}` : "";
    const haystack = `${turnToken} ${stop.tagLabel} ${stop.promptPreview} ${stop.outcomePreview}`.toLowerCase();
    return haystack.includes(normalizedQuery);
  });
}

function formatStopTime(createdAt: string, locale: UILocale): { short: string; full: string; iso: string } | null {
  if (!createdAt) return null;
  const date = new Date(createdAt);
  if (!Number.isFinite(date.getTime())) return null;
  return {
    short: timeFormats[locale].format(date),
    full: fullTimeFormats[locale].format(date),
    iso: date.toISOString(),
  };
}

function stopAccessibleLabel(stop: NavigatorStop, t: LocaleText): string {
  const snippet = stop.kind === "turn" ? stop.promptPreview : (stop.outcomePreview || stop.promptPreview);
  const shortSnippet = snippet.length > 56 ? `${snippet.slice(0, 56)}…` : snippet;
  if (stop.kind === "turn") {
    return t(`#${stop.turnNumber} ${stop.tagLabel}：${shortSnippet}`, `#${stop.turnNumber} ${stop.tagLabel}: ${shortSnippet}`);
  }
  const prefix = stop.turnNumber > 0 ? `#${stop.turnNumber} ` : "";
  return `${prefix}${stop.tagLabel}: ${shortSnippet}`;
}

export interface V2NavigatorRailProps {
  entries: ReadonlyArray<NarrativeEntry>;
  scrollRef?: RefObject<HTMLElement | null>;
  scopeRef?: RefObject<HTMLElement | null>;
  enabled?: boolean;
  onJumpToEntry?: (entryKey: string, stop: NavigatorStop, scrollTop?: number) => void;
}

export function V2NavigatorRail({
  entries,
  scrollRef,
  scopeRef,
  enabled = true,
  onJumpToEntry,
}: V2NavigatorRailProps) {
  const { locale, t } = useLocale();
  const previewID = useId();
  const popoverID = useId();
  const stops = useMemo(() => extractNavigatorStops(entries, t), [entries, t]);
  const [activeStopID, setActiveStopID] = useState<string>("");
  const [previewStopID, setPreviewStopID] = useState<string | null>(null);
  const [panelOpen, setPanelOpen] = useState(false);
  const [sectionFilter, setSectionFilter] = useState<NavigatorSectionFilter>("all");
  const [searchQuery, setSearchQuery] = useState("");

  const railRootRef = useRef<HTMLElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const searchInputRef = useRef<HTMLInputElement>(null);
  const stopRefs = useRef(new Map<string, HTMLButtonElement>());
  const previewDismissTimer = useRef<number | null>(null);
  const pulseTimerRef = useRef<number | null>(null);
  const pulsedElementRef = useRef<HTMLElement | null>(null);

  const clearPreviewTimer = useCallback(() => {
    if (previewDismissTimer.current !== null) {
      window.clearTimeout(previewDismissTimer.current);
      previewDismissTimer.current = null;
    }
  }, []);

  const clearPulse = useCallback(() => {
    if (pulseTimerRef.current !== null) {
      window.clearTimeout(pulseTimerRef.current);
      pulseTimerRef.current = null;
    }
    if (pulsedElementRef.current) {
      pulsedElementRef.current.classList.remove("is-nav-pulse");
      pulsedElementRef.current.removeAttribute("data-nav-highlighted");
      pulsedElementRef.current = null;
    }
  }, []);

  useEffect(() => () => {
    clearPreviewTimer();
    clearPulse();
  }, [clearPreviewTimer, clearPulse]);

  // Keep activeStopID valid when stops change, and measure scroll position when available.
  const syncActiveStopFromScroll = useCallback(() => {
    if (stops.length === 0) {
      setActiveStopID("");
      return;
    }
    const scroller = scrollRef?.current;
    if (!scroller) {
      setActiveStopID((current) => stops.some((stop) => stop.id === current) ? current : stops[stops.length - 1]!.id);
      return;
    }
    const scrollerRect = scroller.getBoundingClientRect();
    const focalY = scrollerRect.top + Math.min(120, Math.max(36, scroller.clientHeight * 0.28));
    let matched: NavigatorStop | undefined;
    let hasGeometry = false;

    for (const stop of stops) {
      const el = findConversationEntryElement(scroller, stop.entryKey);
      if (!el) continue;
      const rect = el.getBoundingClientRect();
      if (rect.height > 0 || rect.top !== scrollerRect.top) {
        hasGeometry = true;
        if (rect.top <= focalY + 8) {
          matched = stop;
        } else if (!matched && rect.bottom > scrollerRect.top) {
          matched = stop;
        }
      } else if (el.offsetHeight > 0 || el.offsetTop > 0) {
        hasGeometry = true;
        const viewFocal = scroller.scrollTop + Math.min(120, Math.max(36, scroller.clientHeight * 0.28));
        if (el.offsetTop <= viewFocal) {
          matched = stop;
        }
      }
    }

    if (hasGeometry && scroller.clientHeight > 0 &&
      scroller.scrollHeight - scroller.clientHeight - scroller.scrollTop <= 24) {
      matched = stops[stops.length - 1];
    }

    if (matched) {
      setActiveStopID(matched.id);
    } else {
      setActiveStopID((current) => stops.some((stop) => stop.id === current) ? current : stops[stops.length - 1]!.id);
    }
  }, [scrollRef, stops]);

  useEffect(() => {
    syncActiveStopFromScroll();
  }, [syncActiveStopFromScroll]);

  useEffect(() => {
    const scroller = scrollRef?.current;
    if (!scroller || stops.length === 0) return;
    let frame: number | null = null;
    const handleScroll = () => {
      if (frame !== null) return;
      frame = window.requestAnimationFrame(() => {
        frame = null;
        syncActiveStopFromScroll();
      });
    };
    scroller.addEventListener("scroll", handleScroll, { passive: true });
    return () => {
      scroller.removeEventListener("scroll", handleScroll);
      if (frame !== null) window.cancelAnimationFrame(frame);
    };
  }, [scrollRef, stops.length, syncActiveStopFromScroll]);

  const jumpToStop = useCallback((stop: NavigatorStop) => {
    setActiveStopID(stop.id);
    clearPreviewTimer();
    setPreviewStopID(null);

    let nextScrollTop: number | undefined;
    const scroller = scrollRef?.current;
    if (scroller) {
      const target = findConversationEntryElement(scroller, stop.entryKey)
        ?? findConversationEntryElement(scroller, `task:${stop.entryKey}`);
      if (target) {
        const scrollerRect = scroller.getBoundingClientRect();
        const targetRect = target.getBoundingClientRect();
        const reducedMotion = typeof window !== "undefined" &&
          window.matchMedia?.("(prefers-reduced-motion: reduce)")?.matches === true;
        if (targetRect.height > 0 || targetRect.top !== scrollerRect.top) {
          nextScrollTop = Math.max(0, scroller.scrollTop + (targetRect.top - scrollerRect.top) - 12);
          scroller.scrollTop = nextScrollTop;
        } else if (target.offsetTop > 0) {
          nextScrollTop = Math.max(0, target.offsetTop - 12);
          scroller.scrollTop = nextScrollTop;
        } else {
          target.scrollIntoView?.({ block: "start", behavior: reducedMotion ? "auto" : "smooth" });
          nextScrollTop = scroller.scrollTop;
        }

        clearPulse();
        target.classList.add("is-nav-pulse");
        target.setAttribute("data-nav-highlighted", "true");
        pulsedElementRef.current = target;
        pulseTimerRef.current = window.setTimeout(() => {
          target.classList.remove("is-nav-pulse");
          target.removeAttribute("data-nav-highlighted");
          if (pulsedElementRef.current === target) pulsedElementRef.current = null;
          pulseTimerRef.current = null;
        }, 1350);
      }
    }

    onJumpToEntry?.(stop.entryKey, stop, nextScrollTop);
  }, [clearPreviewTimer, clearPulse, onJumpToEntry, scrollRef]);

  // Keyboard shortcuts: Alt+ArrowUp (previous turn) and Alt+ArrowDown (next turn).
  const latestStateRef = useRef({ stops, activeStopID, enabled, jumpToStop });
  latestStateRef.current = { stops, activeStopID, enabled, jumpToStop };

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      const { stops: currentStops, activeStopID: currentActiveID, enabled: isEnabled, jumpToStop: jump } = latestStateRef.current;
      if (!isEnabled || currentStops.length === 0) return;
      if (event.defaultPrevented || event.isComposing || event.keyCode === 229) return;
      if (!event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
      if (event.key !== "ArrowUp" && event.key !== "ArrowDown") return;
      if (document.querySelector('[role="dialog"][aria-modal="true"]')) return;
      if (event.target instanceof Element && event.target.closest(".monaco-editor, .xterm, .v2-workbench-slot")) return;

      const scopeElement = scopeRef?.current;
      if (scopeElement) {
        if (scopeElement.closest("[inert]")) return;
        const active = document.activeElement;
        if (active && active !== document.body && active !== document.documentElement && !scopeElement.contains(active)) {
          return;
        }
      }

      const navigableTurns = currentStops.filter((stop) => stop.kind === "turn");
      const candidates = navigableTurns.length > 0 ? navigableTurns : currentStops;
      const activeStop = currentStops.find((stop) => stop.id === currentActiveID);
      let currentIndex = candidates.findIndex((candidate) => candidate.id === currentActiveID);
      if (currentIndex < 0 && activeStop && activeStop.turnNumber > 0) {
        currentIndex = candidates.findIndex((candidate) => candidate.turnNumber === activeStop.turnNumber);
      }

      let targetIndex: number;
      if (event.key === "ArrowUp") {
        targetIndex = currentIndex <= 0 ? 0 : currentIndex - 1;
        if (currentIndex < 0) targetIndex = candidates.length - 1;
      } else {
        targetIndex = currentIndex < 0 ? 0 : Math.min(candidates.length - 1, currentIndex + 1);
      }

      const nextStop = candidates[targetIndex];
      if (!nextStop) return;
      event.preventDefault();
      jump(nextStop);
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [scopeRef]);

  // Dismiss quick-jump popover or hover preview on Escape / outside click.
  useEffect(() => {
    if (!panelOpen && !previewStopID) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      if (panelOpen) {
        event.preventDefault();
        event.stopPropagation();
        setPanelOpen(false);
        triggerRef.current?.focus();
      } else if (previewStopID) {
        event.preventDefault();
        setPreviewStopID(null);
      }
    };
    const handlePointerDown = (event: MouseEvent) => {
      if (!panelOpen) return;
      const target = event.target;
      if (target instanceof Node && !railRootRef.current?.contains(target)) {
        setPanelOpen(false);
      }
    };
    document.addEventListener("keydown", handleKeyDown);
    document.addEventListener("mousedown", handlePointerDown);
    return () => {
      document.removeEventListener("keydown", handleKeyDown);
      document.removeEventListener("mousedown", handlePointerDown);
    };
  }, [panelOpen, previewStopID]);

  useEffect(() => {
    if (!panelOpen) return;
    const frame = window.requestAnimationFrame(() => {
      searchInputRef.current?.focus();
    });
    return () => window.cancelAnimationFrame(frame);
  }, [panelOpen]);

  const checkpointCount = useMemo(() => stops.filter((stop) => stop.kind === "checkpoint").length, [stops]);
  const errorCount = useMemo(() => stops.filter((stop) => stop.kind === "error" || Boolean(stop.hasError)).length, [stops]);
  const filteredStops = useMemo(
    () => filterNavigatorStops(stops, sectionFilter, searchQuery),
    [stops, sectionFilter, searchQuery],
  );

  if (stops.length === 0) return null;

  const effectiveActiveID = stops.some((stop) => stop.id === activeStopID) ? activeStopID : stops[stops.length - 1]!.id;
  const previewStop = !panelOpen && previewStopID ? stops.find((stop) => stop.id === previewStopID) ?? null : null;
  const previewTime = previewStop ? formatStopTime(previewStop.createdAt, locale) : null;

  const schedulePreviewDismiss = () => {
    clearPreviewTimer();
    previewDismissTimer.current = window.setTimeout(() => {
      setPreviewStopID(null);
      previewDismissTimer.current = null;
    }, 110);
  };

  return (
    <nav
      aria-label={t("对话快速导航", "Conversation quick navigation")}
      className="v2-navigator-rail"
      ref={railRootRef}
    >
      <div className="v2-navigator-rail-anchor">
        <div className="v2-navigator-rail-surface">
          <button
            aria-controls={panelOpen ? popoverID : undefined}
            aria-expanded={panelOpen}
            aria-label={t("快速导航列表", "Quick navigation list")}
            className="v2-navigator-list-trigger"
            onClick={() => {
              clearPreviewTimer();
              setPreviewStopID(null);
              setPanelOpen((open) => !open);
            }}
            ref={triggerRef}
            title={t("快速导航列表 (Alt+↑ / Alt+↓)", "Quick navigation list (Alt+↑ / Alt+↓)")}
            type="button"
          >
            <svg aria-hidden="true" fill="none" height="14" stroke="currentColor" strokeLinecap="round" strokeWidth="1.75" viewBox="0 0 16 16" width="14">
              <path d="M3 4h10M6 8h7M4 12h9" />
            </svg>
            {errorCount > 0 && <span aria-hidden="true" className="v2-navigator-error-dot" />}
          </button>
          <span aria-hidden="true" className="v2-navigator-divider" />
          <ol className="v2-navigator-stops" role="list">
            {stops.map((stop, index) => {
              const isActive = stop.id === effectiveActiveID;
              const isPreviewed = previewStop?.id === stop.id;
              return (
                <li key={stop.id}>
                  <button
                    aria-current={isActive ? "true" : undefined}
                    aria-describedby={isPreviewed ? previewID : undefined}
                    aria-label={stopAccessibleLabel(stop, t)}
                    className={`v2-navigator-stop kind-${stop.kind}${isActive ? " is-active" : ""}`}
                    data-stop-entry-key={stop.entryKey}
                    data-stop-kind={stop.kind}
                    onBlur={schedulePreviewDismiss}
                    onClick={() => jumpToStop(stop)}
                    onFocus={() => {
                      clearPreviewTimer();
                      if (!panelOpen) setPreviewStopID(stop.id);
                    }}
                    onKeyDown={(event) => {
                      if (event.altKey || event.ctrlKey || event.metaKey) return;
                      let nextIndex = -1;
                      if (event.key === "ArrowDown") nextIndex = Math.min(stops.length - 1, index + 1);
                      else if (event.key === "ArrowUp") nextIndex = Math.max(0, index - 1);
                      else if (event.key === "Home") nextIndex = 0;
                      else if (event.key === "End") nextIndex = stops.length - 1;
                      if (nextIndex >= 0 && nextIndex !== index) {
                        event.preventDefault();
                        const nextStop = stops[nextIndex]!;
                        stopRefs.current.get(nextStop.id)?.focus();
                      }
                    }}
                    onMouseEnter={() => {
                      clearPreviewTimer();
                      if (!panelOpen) setPreviewStopID(stop.id);
                    }}
                    onMouseLeave={schedulePreviewDismiss}
                    ref={(node) => {
                      if (node) stopRefs.current.set(stop.id, node);
                      else stopRefs.current.delete(stop.id);
                    }}
                    tabIndex={isActive ? 0 : -1}
                    title={t("点击跳转 · Alt+↑ / Alt+↓ 切换回合", "Click to jump · Alt+↑ / Alt+↓ between turns")}
                    type="button"
                  >
                    <span aria-hidden="true" className="v2-navigator-tick" />
                  </button>
                </li>
              );
            })}
          </ol>
        </div>

        {previewStop && (
          <div
            className="v2-navigator-preview"
            data-testid="navigator-preview-card"
            id={previewID}
            onMouseEnter={clearPreviewTimer}
            onMouseLeave={schedulePreviewDismiss}
            role="tooltip"
          >
            <div className="v2-navigator-preview-header">
              {previewStop.turnNumber > 0 && (
                <span className="v2-navigator-turn-badge">{`#${previewStop.turnNumber}`}</span>
              )}
              <span className={`v2-navigator-tag kind-${previewStop.kind}`}>
                {previewStop.tagLabel}
              </span>
              {previewTime && (
                <time className="v2-navigator-time" dateTime={previewTime.iso} title={previewTime.full}>
                  {previewTime.short}
                </time>
              )}
            </div>
            <p className="v2-navigator-preview-prompt">{previewStop.promptPreview}</p>
            {previewStop.outcomePreview && (
              <p className="v2-navigator-preview-outcome">{previewStop.outcomePreview}</p>
            )}
            <div className="v2-navigator-preview-footer">
              <span>{t("点击定位到该处", "Click to jump to entry")}</span>
              <kbd>Alt+↑ / Alt+↓</kbd>
            </div>
          </div>
        )}

        {panelOpen && (
          <div
            aria-label={t("对话快速跳转", "Conversation quick jump")}
            className="v2-navigator-popover"
            id={popoverID}
            role="dialog"
          >
            <header className="v2-navigator-popover-header">
              <div className="v2-navigator-popover-title">
                <strong>{t("快速导航", "Quick navigation")}</strong>
                <kbd className="v2-navigator-popover-shortcut">Alt+↑ / Alt+↓</kbd>
              </div>
              <button
                aria-label={t("关闭快速导航", "Close quick navigation")}
                className="v2-navigator-popover-close"
                onClick={() => {
                  setPanelOpen(false);
                  triggerRef.current?.focus();
                }}
                type="button"
              >
                <X aria-hidden="true" size={15} />
              </button>
            </header>

            <div className="v2-navigator-search">
              <Search aria-hidden="true" size={14} />
              <input
                aria-label={t("搜索对话节点", "Search conversation stops")}
                onChange={(event) => setSearchQuery(event.target.value)}
                placeholder={t("搜索提问、阶段或错误…", "Search prompts, phases, or errors…")}
                ref={searchInputRef}
                type="search"
                value={searchQuery}
              />
            </div>

            <div
              aria-label={t("节点分类筛选", "Filter stops by section")}
              className="v2-navigator-filters"
              role="group"
            >
              <button
                aria-pressed={sectionFilter === "all"}
                onClick={() => setSectionFilter("all")}
                type="button"
              >
                <span>{t("全部回合", "All Turns")}</span>
                <span className="v2-navigator-filter-count">{stops.length}</span>
              </button>
              <button
                aria-pressed={sectionFilter === "checkpoints"}
                onClick={() => setSectionFilter("checkpoints")}
                type="button"
              >
                <span>{t("检查点与阶段", "Checkpoints & Phases")}</span>
                <span className="v2-navigator-filter-count">{checkpointCount}</span>
              </button>
              <button
                aria-pressed={sectionFilter === "errors"}
                onClick={() => setSectionFilter("errors")}
                type="button"
              >
                {errorCount > 0 && <CircleAlert aria-hidden="true" size={12} />}
                <span>{t("错误与工具失败", "Errors & Tool Failures")}</span>
                <span className="v2-navigator-filter-count">{errorCount}</span>
              </button>
            </div>

            {filteredStops.length === 0 ? (
              <p className="v2-navigator-empty">
                {t("没有匹配的导航节点", "No matching navigation stops")}
              </p>
            ) : (
              <ol className="v2-navigator-list" role="list">
                {filteredStops.map((stop) => {
                  const isActive = stop.id === effectiveActiveID;
                  const time = formatStopTime(stop.createdAt, locale);
                  return (
                    <li key={stop.id}>
                      <button
                        aria-current={isActive ? "true" : undefined}
                        className={`v2-navigator-list-item${isActive ? " is-active" : ""}`}
                        onClick={() => {
                          jumpToStop(stop);
                          setPanelOpen(false);
                        }}
                        type="button"
                      >
                        <div className="v2-navigator-preview-header">
                          {stop.turnNumber > 0 && (
                            <span className="v2-navigator-turn-badge">{`#${stop.turnNumber}`}</span>
                          )}
                          <span className={`v2-navigator-tag kind-${stop.kind}`}>
                            {stop.tagLabel}
                          </span>
                          {time && (
                            <time className="v2-navigator-time" dateTime={time.iso}>
                              {time.short}
                            </time>
                          )}
                        </div>
                        <p className="v2-navigator-preview-prompt">{stop.promptPreview}</p>
                        {stop.outcomePreview && (
                          <p className="v2-navigator-preview-outcome">{stop.outcomePreview}</p>
                        )}
                      </button>
                    </li>
                  );
                })}
              </ol>
            )}
          </div>
        )}
      </div>
    </nav>
  );
}
