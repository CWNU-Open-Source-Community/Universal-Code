import { useCallback, useEffect, useId, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { CheckCircle2, Mic, MicOff, PhoneOff, RotateCcw, Send, Volume2, VolumeX, X } from "lucide-react";
import { useLocale } from "../../lib/locale";
import type { AgentOrbMode } from "../projection/agent-activity";
import type { NarrativeEntry } from "../projection/narrative";
import { AgentOrb } from "./agent-orb";
import "./voice-call-mode.css";

export const voicePreferencesStorageKey = "universal-code.voice-preferences.v1";
const voicePreferencesChangedEvent = "universal-code:voice-preferences";

export interface VoicePreferences {
  autoSpeakSummaries: boolean;
  speechRate: number;
  defaultVoiceMuted: boolean;
  autoListenAfterReply: boolean;
}

export const defaultVoicePreferences: VoicePreferences = {
  autoSpeakSummaries: true,
  speechRate: 1.0,
  defaultVoiceMuted: false,
  autoListenAfterReply: true,
};

function clampSpeechRate(value: unknown): number {
  if (typeof value !== "number" || !Number.isFinite(value)) return defaultVoicePreferences.speechRate;
  return Math.round(Math.min(1.5, Math.max(0.8, value)) * 10) / 10;
}

function parseVoicePreferences(raw: string | null): VoicePreferences {
  if (!raw) return defaultVoicePreferences;
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object") return defaultVoicePreferences;
    const record = parsed as Record<string, unknown>;
    return {
      autoSpeakSummaries:
        typeof record.autoSpeakSummaries === "boolean"
          ? record.autoSpeakSummaries
          : defaultVoicePreferences.autoSpeakSummaries,
      speechRate: clampSpeechRate(record.speechRate),
      defaultVoiceMuted:
        typeof record.defaultVoiceMuted === "boolean"
          ? record.defaultVoiceMuted
          : defaultVoicePreferences.defaultVoiceMuted,
      autoListenAfterReply:
        typeof record.autoListenAfterReply === "boolean"
          ? record.autoListenAfterReply
          : defaultVoicePreferences.autoListenAfterReply,
    };
  } catch {
    return defaultVoicePreferences;
  }
}

function readRawVoicePreferences(): string | null {
  try {
    return window.localStorage.getItem(voicePreferencesStorageKey);
  } catch {
    return null;
  }
}

function subscribeVoicePreferences(notify: () => void): () => void {
  const onStorage = (event: StorageEvent) => {
    if (!event.key || event.key === voicePreferencesStorageKey) notify();
  };
  window.addEventListener(voicePreferencesChangedEvent, notify);
  window.addEventListener("storage", onStorage);
  return () => {
    window.removeEventListener(voicePreferencesChangedEvent, notify);
    window.removeEventListener("storage", onStorage);
  };
}

export function readVoicePreferences(): VoicePreferences {
  return parseVoicePreferences(readRawVoicePreferences());
}

export function saveVoicePreferences(patch: Partial<VoicePreferences>): VoicePreferences {
  const current = readVoicePreferences();
  const next: VoicePreferences = {
    autoSpeakSummaries: patch.autoSpeakSummaries ?? current.autoSpeakSummaries,
    speechRate: patch.speechRate !== undefined ? clampSpeechRate(patch.speechRate) : current.speechRate,
    defaultVoiceMuted: patch.defaultVoiceMuted ?? current.defaultVoiceMuted,
    autoListenAfterReply: patch.autoListenAfterReply ?? current.autoListenAfterReply,
  };
  window.localStorage.setItem(voicePreferencesStorageKey, JSON.stringify(next));
  window.dispatchEvent(new Event(voicePreferencesChangedEvent));
  return next;
}

export function resetVoicePreferences(): VoicePreferences {
  window.localStorage.setItem(voicePreferencesStorageKey, JSON.stringify(defaultVoicePreferences));
  window.dispatchEvent(new Event(voicePreferencesChangedEvent));
  return defaultVoicePreferences;
}

export function useVoicePreferences(): VoicePreferences {
  const raw = useSyncExternalStore(subscribeVoicePreferences, readRawVoicePreferences, () => null);
  return useMemo(() => parseVoicePreferences(raw), [raw]);
}

function statusFallbackSummary(status: string | undefined, preferEnglish: boolean): string {
  switch ((status ?? "").toLowerCase()) {
    case "running":
    case "preparing":
    case "busy":
      return preferEnglish ? "Working on your request now." : "正在执行当前任务，请稍候。";
    case "waiting_approval":
      return preferEnglish ? "An action is waiting for your approval." : "有一项操作正在等待您的确认。";
    case "completed":
    case "done":
      return preferEnglish ? "The current step is complete." : "当前执行步骤已完成。";
    case "failed":
    case "error":
      return preferEnglish ? "Execution encountered an issue. Check details in history." : "执行遇到异常，可在完整记录中查看原因。";
    default:
      return "";
  }
}

/**
 * Strips fenced code blocks, raw diffs, noisy file paths, and markdown formatting
 * into a crisp, conversational 1–2 sentence summary suitable for live TTS and minimal display.
 */
export function summarizeForVoice(
  text: string,
  status?: string,
  locale?: "zh-CN" | "en-US",
): string {
  const raw = text ?? "";

  if (!raw.trim()) {
    return statusFallbackSummary(status, locale === "en-US");
  }

  let hadCodeOrDiff = false;

  // 1. Strip fenced code blocks (```...``` or ~~~...~~~)
  let cleaned = raw.replace(/(?:```|~~~)[\s\S]*?(?:```|~~~)/gu, () => {
    hadCodeOrDiff = true;
    return " ";
  });
  // Handle unclosed streaming fenced code blocks at the end
  cleaned = cleaned.replace(/(?:```|~~~)[\s\S]*$/gu, () => {
    hadCodeOrDiff = true;
    return " ";
  });

  // 2. Strip unified git diff hunks and +/- diff lines
  cleaned = cleaned
    .split(/\r?\n/u)
    .filter((line) => {
      const trimmed = line.trim();
      if (
        /^diff --git\s/u.test(trimmed) ||
        /^index [0-9a-f]+\.\.[0-9a-f]+/u.test(trimmed) ||
        /^(?:---|\+\+\+)\s+[ab]\//u.test(trimmed) ||
        /^@@\s+-\d+/u.test(trimmed) ||
        /^[+-]{1}(?![+-])(?=.*[{};=()[\]<>])/u.test(trimmed)
      ) {
        hadCodeOrDiff = true;
        return false;
      }
      // Drop markdown table separator rows
      if (/^\|?[\s:-]+\|[\s|:-]+$/u.test(trimmed)) return false;
      return true;
    })
    .join(" ");

  // 3. Convert markdown links/images and strip bare URLs
  cleaned = cleaned.replace(/!\[([^\]]*)\]\([^)]*\)/gu, "$1");
  cleaned = cleaned.replace(/\[([^\]]+)\]\([^)]*\)/gu, "$1");
  cleaned = cleaned.replace(/file:\/\/\/[^\s)`"']+/gu, (match) => {
    const base = match.split(/[\\/]/u).pop() ?? "";
    return base.replace(/#L\d+(?:-L?\d+)?$/iu, "").replace(/:\d+(?:-\d+)?$/u, "");
  });
  cleaned = cleaned.replace(/https?:\/\/[^\s)`"']+/gu, "");

  // 4. Condense multi-segment file paths (e.g. web/src/v2/components/login.tsx#L12-L40 -> login.tsx)
  cleaned = cleaned.replace(
    /(?:[a-zA-Z]:[\\/]|\/)?(?:[\w.-]+[\\/]){1,}([\w.-]+\.[a-zA-Z0-9]{1,8})(?:#L\d+(?:-L?\d+)?|:\d+(?:-\d+)?)?/gu,
    "$1",
  );

  // 5. Strip markdown tokens (headings, bold, italics, backticks, blockquotes, list markers)
  cleaned = cleaned
    .replace(/(?:^|\s)#{1,6}\s+/gu, " ")
    .replace(/(?:^|\s)>\s+/gu, " ")
    .replace(/(?:^|\s)(?:[-*+]|\d+\.)\s+\[[ xX]\]\s+/gu, " ")
    .replace(/(?:^|\s)(?:[-*+])\s+/gu, " ")
    .replace(/[*_~`]+/gu, "")
    .replace(/<[^>]+>/gu, " ")
    .replace(/\|/gu, " ")
    .replace(/\s+/gu, " ")
    .trim();

  // Remove dangling introducers left behind before stripped code blocks (e.g. "代码如下：")
  cleaned = cleaned
    .replace(/(?:具体)?(?:代码|改动|实现|示例如下|如下|Here is the code|Below is the diff)[^。！？.!?]{0,14}[:：]\s*$/iu, "")
    .trim();

  const preferEnglish =
    locale === "en-US" ||
    (locale === undefined && !/[\u4e00-\u9fff]/u.test(cleaned) && /[a-zA-Z]{3,}/u.test(cleaned));

  if (!cleaned) {
    if (hadCodeOrDiff) {
      return preferEnglish
        ? "Code changes are ready; check the full transcript for details."
        : "已完成代码变更整理，可在完整对话记录中查看细节。";
    }
    return statusFallbackSummary(status, preferEnglish);
  }

  // 6. Split into sentences and keep 1–2 concise sentences
  const sentenceMatches = cleaned.match(/[^。！？!?；;\n]+[。！？!?；;]?/gu) ?? [cleaned];
  const sentences = sentenceMatches
    .map((part) => part.trim())
    .filter((part) => part.length > 0);

  const selected = sentences.slice(0, 2).join(preferEnglish ? " " : "");
  const maxChars = preferEnglish ? 220 : 150;
  if (selected.length <= maxChars) {
    return /[.!?。！？]$/u.test(selected) ? selected : `${selected}${preferEnglish ? "." : "。"}`;
  }
  const clipped = selected.slice(0, maxChars).replace(/[,，、;；:\s]+$/u, "");
  return `${clipped}…`;
}

interface SpeechRecognitionAlternativeLike {
  transcript: string;
}

interface SpeechRecognitionResultLike {
  isFinal: boolean;
  0: SpeechRecognitionAlternativeLike;
  length: number;
}

interface SpeechRecognitionEventLike {
  resultIndex: number;
  results: ArrayLike<SpeechRecognitionResultLike>;
}

interface SpeechRecognitionInstanceLike {
  lang: string;
  continuous: boolean;
  interimResults: boolean;
  onstart: (() => void) | null;
  onend: (() => void) | null;
  onerror: ((event: { error?: string }) => void) | null;
  onresult: ((event: SpeechRecognitionEventLike) => void) | null;
  start(): void;
  stop(): void;
  abort(): void;
}

type SpeechRecognitionConstructorLike = new () => SpeechRecognitionInstanceLike;

function getSpeechRecognitionConstructor(): SpeechRecognitionConstructorLike | null {
  if (typeof window === "undefined") return null;
  const win = window as unknown as {
    SpeechRecognition?: SpeechRecognitionConstructorLike;
    webkitSpeechRecognition?: SpeechRecognitionConstructorLike;
  };
  return win.SpeechRecognition ?? win.webkitSpeechRecognition ?? null;
}

function getSpeechSynthesis(): SpeechSynthesis | null {
  if (typeof window === "undefined" || !("speechSynthesis" in window)) return null;
  const synth = window.speechSynthesis;
  return synth && typeof synth.speak === "function" && typeof synth.cancel === "function" ? synth : null;
}

function createUtterance(text: string, rate: number, lang: string): SpeechSynthesisUtterance {
  if (typeof SpeechSynthesisUtterance === "function") {
    const utterance = new SpeechSynthesisUtterance(text);
    utterance.rate = rate;
    utterance.lang = lang;
    return utterance;
  }
  return { text, rate, lang } as unknown as SpeechSynthesisUtterance;
}

export interface V2VoiceCallStageProps {
  entries: readonly NarrativeEntry[];
  activityHeadline?: string;
  working?: boolean;
  disabled?: boolean;
  onSubmitPrompt: (content: string) => Promise<void> | void;
  onEndCall: () => void;
}

export function V2VoiceCallStage({
  entries,
  activityHeadline,
  working = false,
  disabled = false,
  onSubmitPrompt,
  onEndCall,
}: V2VoiceCallStageProps) {
  const { locale, t } = useLocale();
  const preferences = useVoicePreferences();
  const quickInputID = useId();

  const [voiceMuted, setVoiceMuted] = useState<boolean>(() => preferences.defaultVoiceMuted);
  const [listening, setListening] = useState(false);
  const [speaking, setSpeaking] = useState(false);
  const [interimSpeech, setInterimSpeech] = useState("");
  const [quickText, setQuickText] = useState("");
  const [submitting, setSubmitting] = useState(false);

  const recognitionRef = useRef<SpeechRecognitionInstanceLike | null>(null);
  const lastSpokenSignatureRef = useRef<string>("");
  const onSubmitRef = useRef(onSubmitPrompt);
  onSubmitRef.current = onSubmitPrompt;

  const synthesisSupported = Boolean(getSpeechSynthesis());
  const recognitionSupported = Boolean(getSpeechRecognitionConstructor());

  const activeMessage = useMemo(() => {
    const latest = entries.at(-1);
    if (!latest) {
      if (working && activityHeadline) {
        return {
          id: "live-activity",
          speaker: "assistant" as const,
          badge: t("执行进展", "Execution progress"),
          summary: summarizeForVoice(activityHeadline, "running", locale),
          provisional: true,
        };
      }
      return {
        id: "ready",
        speaker: "system" as const,
        badge: t("实时语音就绪", "Voice call ready"),
        summary: t(
          "正在保持精简视图，仅展示当前进展。请直接说话或输入指令。",
          "Minimal call view active: showing only the current message. Speak or type below.",
        ),
        provisional: false,
      };
    }

    if (latest.kind === "assistant") {
      return {
        id: latest.id,
        speaker: "assistant" as const,
        badge: latest.provisional ? t("正在播报与整理进展", "Live summary") : t("智能体当前回复", "Current reply"),
        summary: summarizeForVoice(latest.text, latest.provisional || working ? "running" : "completed", locale),
        provisional: latest.provisional,
      };
    }

    if (latest.kind === "activity") {
      const combined = activityHeadline ? `${activityHeadline}。${latest.title}` : `${latest.title} ${latest.detail}`;
      return {
        id: latest.id,
        speaker: "assistant" as const,
        badge: t("执行进展总结", "Progress summary"),
        summary: summarizeForVoice(combined, working ? "running" : latest.status, locale),
        provisional: latest.provisional,
      };
    }

    if (latest.kind === "notice") {
      return {
        id: latest.id,
        speaker: "assistant" as const,
        badge: t("状态提示", "Status notice"),
        summary: summarizeForVoice(latest.text, latest.tone === "warning" ? "failed" : "completed", locale),
        provisional: false,
      };
    }

    return {
      id: latest.id,
      speaker: "user" as const,
      badge: t("当前您的指令", "Your current request"),
      summary:
        working && activityHeadline
          ? `${latest.text} — ${summarizeForVoice(activityHeadline, "running", locale)}`
          : latest.text,
      provisional: latest.provisional,
    };
  }, [activityHeadline, entries, locale, t, working]);

  const stopRecognition = useCallback(() => {
    const active = recognitionRef.current;
    recognitionRef.current = null;
    if (active) {
      try {
        active.stop();
      } catch {
        try {
          active.abort();
        } catch {
          /* Ignore already-stopped recognition handles */
        }
      }
    }
    setListening(false);
    setInterimSpeech("");
  }, []);

  const startRecognition = useCallback(() => {
    if (disabled) return;
    const Recognition = getSpeechRecognitionConstructor();
    if (!Recognition) return;
    stopRecognition();
    try {
      const instance = new Recognition();
      instance.lang = locale;
      instance.continuous = false;
      instance.interimResults = true;
      instance.onstart = () => setListening(true);
      instance.onend = () => {
        if (recognitionRef.current === instance) {
          recognitionRef.current = null;
          setListening(false);
        }
      };
      instance.onerror = () => {
        if (recognitionRef.current === instance) {
          recognitionRef.current = null;
          setListening(false);
        }
      };
      instance.onresult = (event) => {
        let interim = "";
        let finalTranscript = "";
        for (let i = event.resultIndex; i < event.results.length; i++) {
          const res = event.results[i];
          if (!res || !res[0]) continue;
          if (res.isFinal) finalTranscript += res[0].transcript;
          else interim += res[0].transcript;
        }
        setInterimSpeech(interim.trim());
        const committed = finalTranscript.trim();
        if (committed) {
          setInterimSpeech("");
          void Promise.resolve(onSubmitRef.current(committed)).catch(() => undefined);
        }
      };
      recognitionRef.current = instance;
      setListening(true);
      instance.start();
    } catch {
      recognitionRef.current = null;
      setListening(false);
    }
  }, [disabled, locale, stopRecognition]);

  const speakSummary = useCallback(
    (summaryText: string, signature: string) => {
      const synth = getSpeechSynthesis();
      if (!synth || !summaryText.trim()) return;
      synth.cancel();
      lastSpokenSignatureRef.current = signature;
      const utterance = createUtterance(summaryText, preferences.speechRate, locale);
      utterance.onstart = () => setSpeaking(true);
      utterance.onend = () => {
        setSpeaking(false);
        if (preferences.autoListenAfterReply && recognitionSupported && !disabled) {
          startRecognition();
        }
      };
      utterance.onerror = () => setSpeaking(false);
      setSpeaking(true);
      synth.speak(utterance);
    },
    [disabled, locale, preferences.autoListenAfterReply, preferences.speechRate, recognitionSupported, startRecognition],
  );

  // Automatically speak concise assistant/progress summaries unless voice is muted or auto-speak is off.
  useEffect(() => {
    if (voiceMuted || !preferences.autoSpeakSummaries || !synthesisSupported) return;
    if (activeMessage.speaker !== "assistant" || !activeMessage.summary.trim()) return;
    const signature = `${activeMessage.id}::${activeMessage.summary}`;
    if (lastSpokenSignatureRef.current === signature) return;

    if (!activeMessage.provisional) {
      speakSummary(activeMessage.summary, signature);
      return;
    }

    const timer = window.setTimeout(() => {
      speakSummary(activeMessage.summary, signature);
    }, 200);
    return () => window.clearTimeout(timer);
  }, [
    activeMessage.id,
    activeMessage.provisional,
    activeMessage.speaker,
    activeMessage.summary,
    preferences.autoSpeakSummaries,
    speakSummary,
    synthesisSupported,
    voiceMuted,
  ]);

  useEffect(() => {
    return () => {
      getSpeechSynthesis()?.cancel();
      const active = recognitionRef.current;
      recognitionRef.current = null;
      if (active) {
        try {
          active.abort();
        } catch {
          /* Ignore unmount abort errors */
        }
      }
    };
  }, []);

  const handleToggleVoiceMute = () => {
    setVoiceMuted((previous) => {
      const nextMuted = !previous;
      if (nextMuted) {
        getSpeechSynthesis()?.cancel();
        setSpeaking(false);
      } else {
        // Allow re-speaking if a new update arrives or let user hear status cleanly
        lastSpokenSignatureRef.current = "";
      }
      return nextMuted;
    });
  };

  const handleToggleMic = () => {
    if (listening) {
      stopRecognition();
    } else {
      getSpeechSynthesis()?.cancel();
      setSpeaking(false);
      startRecognition();
    }
  };

  const handleEndCallClick = () => {
    getSpeechSynthesis()?.cancel();
    setSpeaking(false);
    stopRecognition();
    onEndCall();
  };

  const handleQuickSubmit = async (content: string) => {
    const trimmed = content.trim();
    if (!trimmed || submitting || disabled) return;
    setSubmitting(true);
    setQuickText("");
    try {
      await onSubmitPrompt(trimmed);
    } finally {
      setSubmitting(false);
    }
  };

  const voiceState = voiceMuted
    ? "muted"
    : speaking
      ? "speaking"
      : listening
        ? "listening"
        : working
          ? "thinking"
          : "idle";

  const orbMode: AgentOrbMode = speaking ? "composing" : working ? "executing" : listening ? "searching" : "waiting";

  return (
    <section
      aria-label={t("实时语音通话视图", "Live voice call stage")}
      className="v2-voice-call-stage"
      data-testid="v2-voice-call-stage"
    >
      <div className="v2-voice-stage-center">
        <div className="v2-voice-orb-shell">
          <div className="v2-voice-orb-ring" data-voice-state={voiceState}>
            <AgentOrb mode={orbMode} />
          </div>
          <div
            aria-hidden="true"
            className="v2-voice-waveform"
            data-active={String(!voiceMuted && (speaking || listening || working))}
          >
            <span />
            <span />
            <span />
            <span />
            <span />
          </div>
          <span className="v2-voice-status-pill" role="status" aria-live="polite">
            {voiceMuted ? (
              <>
                <VolumeX aria-hidden="true" size={14} />
                <span>{t("已静音语音 · 仅阅读实时文字", "Voice muted · Reading live text only")}</span>
              </>
            ) : listening ? (
              <>
                <Mic aria-hidden="true" size={14} />
                <span>{t("正在倾听您的语音…", "Listening…")}</span>
              </>
            ) : speaking ? (
              <>
                <Volume2 aria-hidden="true" size={14} />
                <span>{t("正在语音总结执行进展…", "Speaking concise progress summary…")}</span>
              </>
            ) : working ? (
              <span>{t("智能体正在执行并整理简明摘要…", "Agent working & preparing concise summary…")}</span>
            ) : (
              <span>{t("实时语音通话中 · 仅显示当前消息", "Live voice call · Showing current message only")}</span>
            )}
          </span>
        </div>

        <article
          aria-label={t("当前消息摘要", "Current message summary")}
          className="v2-voice-single-card"
          data-speaker={activeMessage.speaker}
          data-testid="v2-voice-current-card"
        >
          <div className="v2-voice-single-card-meta">
            <span>{activeMessage.badge}</span>
            <span>{`${preferences.speechRate.toFixed(1)}x`}</span>
          </div>
          <p className="v2-voice-single-message" data-testid="v2-voice-current-message">
            {activeMessage.summary}
          </p>
          {interimSpeech && (
            <p className="v2-voice-interim-transcript">
              {t(`识别中：${interimSpeech}`, `Recognizing: ${interimSpeech}`)}
            </p>
          )}
        </article>
      </div>

      <div className="v2-voice-stage-footer">
        <form
          className="v2-voice-quick-form"
          onSubmit={(event) => {
            event.preventDefault();
            void handleQuickSubmit(quickText);
          }}
        >
          <label className="v2-voice-quick-label" htmlFor={quickInputID}>
            {t("通话中补充文字指令", "Send text message during call")}
          </label>
          <input
            autoComplete="off"
            disabled={disabled || submitting}
            id={quickInputID}
            onChange={(event) => setQuickText(event.target.value)}
            placeholder={t("也可直接输入文字指令…", "Or type a quick instruction…")}
            type="text"
            value={quickText}
          />
          <button
            aria-label={t("发送通话文字指令", "Send call text instruction")}
            className="v2-voice-quick-send"
            disabled={disabled || submitting || !quickText.trim()}
            type="submit"
          >
            <Send aria-hidden="true" size={15} />
            <span>{t("发送", "Send")}</span>
          </button>
        </form>

        <div
          aria-label={t("语音通话控制栏", "Voice call controls")}
          className="v2-voice-controls-bar"
          role="group"
        >
          <button
            aria-pressed={voiceMuted}
            className="v2-voice-control-btn"
            onClick={handleToggleVoiceMute}
            type="button"
          >
            {voiceMuted ? <VolumeX aria-hidden="true" size={16} /> : <Volume2 aria-hidden="true" size={16} />}
            <span>{t("静音语音（仅看文字）", "Mute Voice (Text Only)")}</span>
          </button>

          <button
            aria-pressed={listening}
            className="v2-voice-control-btn"
            disabled={disabled || !recognitionSupported}
            onClick={handleToggleMic}
            title={
              recognitionSupported
                ? t("开启或停止麦克风语音输入", "Toggle microphone speech input")
                : t("当前浏览器不支持语音识别，请使用上方文字框输入", "Speech recognition unavailable; use the text input above")
            }
            type="button"
          >
            {listening ? <Mic aria-hidden="true" size={16} /> : <MicOff aria-hidden="true" size={16} />}
            <span>{t("麦克风倾听", "Microphone")}</span>
          </button>

          <button
            className="v2-voice-control-btn is-end-call"
            onClick={handleEndCallClick}
            type="button"
          >
            <PhoneOff aria-hidden="true" size={16} />
            <span>{t("结束通话（查看完整记录）", "End Call & View Full History")}</span>
          </button>
        </div>

        {!recognitionSupported && (
          <p className="v2-voice-capability-hint">
            {t(
              "当前环境未启用麦克风语音识别，您仍可收听进展播报并通过文字框随时交互。",
              "Microphone recognition is unavailable in this environment; you can still listen to summaries and reply via text.",
            )}
          </p>
        )}
      </div>
    </section>
  );
}

export function V2VoicePostCallBanner({ onDismiss }: { onDismiss: () => void }) {
  const { t } = useLocale();
  return (
    <div className="v2-voice-post-call-banner" role="status">
      <div>
        <CheckCircle2 aria-hidden="true" size={15} />
        <span>{t("通话已结束 · 完整对话记录已展开", "Call ended · Full conversation history expanded")}</span>
      </div>
      <button aria-label={t("关闭通话结束提示", "Dismiss post-call notice")} onClick={onDismiss} type="button">
        <X aria-hidden="true" size={13} />
      </button>
    </div>
  );
}

export function V2VoiceSettings() {
  const { t } = useLocale();
  const preferences = useVoicePreferences();
  const [saveError, setSaveError] = useState(false);
  const rateInputID = useId();

  const synthesisSupported = Boolean(getSpeechSynthesis());
  const recognitionSupported = Boolean(getSpeechRecognitionConstructor());

  const update = (patch: Partial<VoicePreferences>) => {
    try {
      saveVoicePreferences(patch);
      setSaveError(false);
    } catch {
      setSaveError(true);
    }
  };

  const handleReset = () => {
    try {
      resetVoicePreferences();
      setSaveError(false);
    } catch {
      setSaveError(true);
    }
  };

  const isDefault =
    preferences.autoSpeakSummaries === defaultVoicePreferences.autoSpeakSummaries &&
    preferences.speechRate === defaultVoicePreferences.speechRate &&
    preferences.defaultVoiceMuted === defaultVoicePreferences.defaultVoiceMuted &&
    preferences.autoListenAfterReply === defaultVoicePreferences.autoListenAfterReply;

  return (
    <>
      <h1>{t("语音交互", "Voice interaction")}</h1>
      <section aria-label={t("精简语音通话", "Streamlined voice call")} className="v2-settings-section">
        <h2>{t("精简语音通话", "Streamlined voice call")}</h2>
        <div className="v2-settings-card">
          <div className="v2-setting-row">
            <div>
              <strong>{t("自动播报进展摘要", "Auto-speak concise summaries")}</strong>
              <span>
                {t(
                  "实时语音通话中自动过滤代码块与路径噪音，用 1–2 句话简明播报最新执行进展。",
                  "Automatically strip code blocks and path noise during calls and speak 1–2 sentence progress summaries.",
                )}
              </span>
            </div>
            <div className="v2-setting-value">
              <button
                aria-checked={preferences.autoSpeakSummaries}
                aria-label={t("自动播报进展摘要", "Auto-speak concise summaries")}
                className={`v2-switch v2-switch-button${preferences.autoSpeakSummaries ? "" : " is-muted"}`}
                onClick={() => update({ autoSpeakSummaries: !preferences.autoSpeakSummaries })}
                role="switch"
                type="button"
              >
                <i />
              </button>
            </div>
          </div>

          <div className="v2-setting-row">
            <div>
              <strong>
                <label htmlFor={rateInputID}>{t("语音播报语速", "Speech rate")}</label>
              </strong>
              <span>
                {t(
                  "调整语音总结的朗读速度（0.8x 至 1.5x），立即生效。",
                  "Adjust spoken summary playback speed from 0.8x to 1.5x.",
                )}
              </span>
            </div>
            <div className="v2-setting-value">
              <div className="v2-voice-rate-control">
                <input
                  aria-label={t("语音播报语速", "Speech rate")}
                  id={rateInputID}
                  max={1.5}
                  min={0.8}
                  onChange={(event) => update({ speechRate: Number(event.currentTarget.value) })}
                  step={0.1}
                  type="range"
                  value={preferences.speechRate}
                />
                <output htmlFor={rateInputID}>{`${preferences.speechRate.toFixed(1)}x`}</output>
              </div>
            </div>
          </div>

          <div className="v2-setting-row">
            <div>
              <strong>{t("默认静音语音（仅看文字）", "Default to Mute Voice (Text Only)")}</strong>
              <span>
                {t(
                  "开启后进入通话默认不播放语音，仅在整洁的单消息界面实时阅读文字摘要。",
                  "Start calls with audio muted by default so you only read the clean single-message text view.",
                )}
              </span>
            </div>
            <div className="v2-setting-value">
              <button
                aria-checked={preferences.defaultVoiceMuted}
                aria-label={t("默认静音语音（仅看文字）", "Default to Mute Voice (Text Only)")}
                className={`v2-switch v2-switch-button${preferences.defaultVoiceMuted ? "" : " is-muted"}`}
                onClick={() => update({ defaultVoiceMuted: !preferences.defaultVoiceMuted })}
                role="switch"
                type="button"
              >
                <i />
              </button>
            </div>
          </div>

          <div className="v2-setting-row">
            <div>
              <strong>{t("回复播报后自动倾听", "Auto-listen after reply")}</strong>
              <span>
                {t(
                  "智能体完成一段语音播报后，若浏览器支持语音识别则自动开启麦克风等待下一句。",
                  "Automatically activate the microphone after spoken replies finish when supported.",
                )}
              </span>
            </div>
            <div className="v2-setting-value">
              <button
                aria-checked={preferences.autoListenAfterReply}
                aria-label={t("回复播报后自动倾听", "Auto-listen after reply")}
                className={`v2-switch v2-switch-button${preferences.autoListenAfterReply ? "" : " is-muted"}`}
                onClick={() => update({ autoListenAfterReply: !preferences.autoListenAfterReply })}
                role="switch"
                type="button"
              >
                <i />
              </button>
            </div>
          </div>

          <div className="v2-setting-row">
            <div>
              <strong>{t("当前环境语音能力", "Runtime speech capabilities")}</strong>
              <span>
                {t(
                  `语音合成：${synthesisSupported ? "可用" : "不可用"} · 语音识别：${recognitionSupported ? "可用" : "不可用（可使用文字输入）"}`,
                  `Speech synthesis: ${synthesisSupported ? "Available" : "Unavailable"} · Speech recognition: ${recognitionSupported ? "Available" : "Unavailable (text fallback active)"}`,
                )}
              </span>
            </div>
            <div className="v2-setting-value">
              <button
                className="v2-setting-link"
                disabled={isDefault}
                onClick={handleReset}
                type="button"
              >
                <RotateCcw aria-hidden="true" size={14} />
                <span>{t("恢复默认", "Reset defaults")}</span>
              </button>
            </div>
          </div>
        </div>
        {saveError && (
          <p role="alert">
            {t("语音设置未能保存，请检查本机存储权限后重试。", "Could not save voice settings. Check local storage permissions and retry.")}
          </p>
        )}
      </section>
    </>
  );
}
