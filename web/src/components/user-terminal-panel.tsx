import { useCallback, useEffect, useEffectEvent, useRef, useState } from "react";
import { FitAddon } from "@xterm/addon-fit";
import { Terminal, type ITheme } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { Bot, LoaderCircle, Play, Quote, RotateCcw, ShieldOff, Square } from "lucide-react";
import {
  closeDesktopUserTerminal,
  desktopDebugTerminalAgentInputEnabled,
  desktopErrorMessage,
  desktopIsMacPlatform,
  desktopUserTerminalEnabled,
  getDesktopDebugTerminalAgentInput,
  getDesktopUserTerminal,
  grantDesktopDebugTerminalAgentInput,
  readDesktopUserTerminal,
  revokeDesktopDebugTerminalAgentInput,
  resizeDesktopUserTerminal,
  startDesktopUserTerminal,
  writeDesktopUserTerminal,
  type DesktopDebugTerminalAgentInputBinding,
  type DesktopTerminalSession,
} from "../lib/desktop-bridge";
import { usePrayuTheme, type PrayuTheme } from "../lib/appearance";
import { useLocale } from "../lib/locale";
import { openUserTerminal } from "../lib/open-user-terminal";
import { readDesktopClipboardText, writeDesktopClipboardText } from "../lib/desktop-text-clipboard";
import { createTerminalClipboardKeyHandler } from "../lib/terminal-clipboard";
import { desktopTerminalSettingsAvailable, getDesktopTerminalSettings, terminalSettingsChanged,
  type TerminalSettingsView } from "../lib/desktop-terminal-settings";

const terminalPollMilliseconds = 120;

export type TerminalQuoteResult = "added" | "unsaved" | "blocked";

export function terminalXtermTheme(theme: PrayuTheme): ITheme {
  if (theme === "light") {
    return {
      background: "#ffffff",
      foreground: "#202020",
      cursor: "#202020",
      cursorAccent: "#ffffff",
      selectionBackground: "#20202024",
      black: "#202020",
      red: "#b42318",
      green: "#166534",
      yellow: "#854d0e",
      blue: "#1d4ed8",
      magenta: "#7e22ce",
      cyan: "#0e7490",
      white: "#4b5563",
      brightBlack: "#595959",
      brightRed: "#c81e1e",
      brightGreen: "#15803d",
      brightYellow: "#92400e",
      brightBlue: "#1e40af",
      brightMagenta: "#86198f",
      brightCyan: "#155e75",
      brightWhite: "#111827",
    };
  }
  if (theme === "glass") {
    return {
      background: "#eff1f5",
      foreground: "#30363d",
      cursor: "#30363d",
      cursorAccent: "#eff1f5",
      selectionBackground: "#30363d26",
      black: "#30363d",
      red: "#d20f39",
      green: "#166534",
      yellow: "#854d0e",
      blue: "#1e66f5",
      magenta: "#8839ef",
      cyan: "#179299",
      white: "#5c5f77",
      brightBlack: "#697078",
      brightRed: "#b91c1c",
      brightGreen: "#15803d",
      brightYellow: "#92400e",
      brightBlue: "#1d4ed8",
      brightMagenta: "#7e22ce",
      brightCyan: "#0e7490",
      brightWhite: "#1f2328",
    };
  }
  return {
    background: "#181818",
    foreground: "#ededed",
    cursor: "#ededed",
    cursorAccent: "#181818",
    selectionBackground: "#ffffff38",
    black: "#181818",
    red: "#ef6b5d",
    green: "#8fbf71",
    yellow: "#e2b45f",
    blue: "#78a9d4",
    magenta: "#c792b6",
    cyan: "#74b6b0",
    white: "#ededed",
    brightBlack: "#8a8a8a",
    brightRed: "#f4877c",
    brightGreen: "#a7d08c",
    brightYellow: "#ecc87d",
    brightBlue: "#94bee3",
    brightMagenta: "#d7a9c8",
    brightCyan: "#90cac4",
    brightWhite: "#ffffff",
  };
}

type UserTerminalPanelProps = {
  runID: string;
  sessionID: string;
  onSession: (sessionID: string) => void;
  onQuote?: (content: string) => TerminalQuoteResult;
  onReturnToDraft?: () => void;
  recoverable?: boolean;
  active?: boolean;
  onShellChange?: (shellName: string) => void;
};

export function UserTerminalPanel(props: UserTerminalPanelProps) {
  // A terminal's buffer, queued writes and selection belong to one source.
  // Recreate xterm when that source changes, including while a read is pending.
  return <UserTerminalSession key={`${props.runID}:${props.sessionID}`} {...props} />;
}

function UserTerminalSession({ runID, sessionID, onSession, onQuote, onReturnToDraft,
  recoverable = false, active = true, onShellChange }: UserTerminalPanelProps) {
  const { t } = useLocale();
  const theme = usePrayuTheme();
  const hostRef = useRef<HTMLDivElement>(null);
  const terminalRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const sessionRef = useRef(sessionID);
  const runRef = useRef(runID);
  const mountedRef = useRef(true);
  const cursorRef = useRef(0);
  const replayingRef = useRef(false);
  const writeQueueRef = useRef(Promise.resolve());
  const [session, setSession] = useState<DesktopTerminalSession | null>(null);
  const [agentBinding, setAgentBinding] =
    useState<DesktopDebugTerminalAgentInputBinding | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [hasSelection, setHasSelection] = useState(false);
  const [quoteResult, setQuoteResult] = useState<TerminalQuoteResult | null>(null);
  const settingsAvailable = desktopTerminalSettingsAvailable();
  const [preferences, setPreferences] = useState<TerminalSettingsView | null>(null);
  const [settingsError, setSettingsError] = useState("");
  const bindingEpoch = useRef(0);
  const mode = settingsAvailable ? preferences?.settings.agent_control : "manual";
  const ttl = preferences?.settings.manual_ttl_seconds ?? 300;
  const duration = ttl % 60 === 0 ? `${ttl / 60}m` : `${ttl}s`;
  const enabled = desktopUserTerminalEnabled();
  const agentInputEnabled = desktopDebugTerminalAgentInputEnabled();

  useEffect(() => {
    if (!settingsAvailable) return;
    let cancelled = false;
    const refresh = () => void getDesktopTerminalSettings().then((view) => {
      if (!cancelled) { setPreferences(view); setSettingsError(""); }
    }).catch((error) => {
      if (!cancelled) { setPreferences(null); setSettingsError(desktopErrorMessage(error)); }
    });
    refresh();
    window.addEventListener(terminalSettingsChanged, refresh);
    return () => { cancelled = true; window.removeEventListener(terminalSettingsChanged, refresh); };
  }, [settingsAvailable]);

  useEffect(() => {
    sessionRef.current = sessionID;
  }, [sessionID]);

  runRef.current = runID;

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const handleTerminalData = useEffectEvent((data: string) => {
    if (session?.state !== "running" || session.session_id !== sessionID) return;
    const current = sessionID;
    writeQueueRef.current = writeQueueRef.current
      .then(() => writeDesktopUserTerminal(current, data))
      .then(() => undefined)
      .catch((error) => setMessage(desktopErrorMessage(error)));
  });

  const pasteIntoSession = useEffectEvent((terminal: Terminal, sourceRunID: string, sourceSessionID: string, text: string) => {
    if (!mountedRef.current || terminalRef.current !== terminal || runID !== sourceRunID ||
        sessionID !== sourceSessionID || session?.state !== "running" || session.session_id !== sourceSessionID) return;
    terminal.paste(text);
  });

  const capturePaste = useEffectEvent(() => {
    const terminal = terminalRef.current;
    if (!terminal || session?.state !== "running" || session.session_id !== sessionID) return null;
    const sourceRunID = runID;
    const sourceSessionID = sessionID;
    return (text: string) => pasteIntoSession(terminal, sourceRunID, sourceSessionID, text);
  });

  const reportClipboardError = useEffectEvent((action: "copy" | "paste", error: unknown) => {
    if (!mountedRef.current) return;
    setMessage(`${action === "copy" ? t("复制失败", "Copy failed") : t("粘贴失败", "Paste failed")}: ${desktopErrorMessage(error)}`);
  });

  const resizeTerminal = useEffectEvent(() => {
    if (!active || replayingRef.current) return;
    fitRef.current?.fit();
    const terminal = terminalRef.current;
    if (terminal && session?.state === "running" && session.session_id === sessionID &&
      terminal.cols >= 20 && terminal.rows >= 5) {
      void resizeDesktopUserTerminal(sessionID, terminal.cols, terminal.rows)
        .catch((error) => setMessage(desktopErrorMessage(error)));
    }
  });

  useEffect(() => {
    if (!hostRef.current) return;
    const terminal = new Terminal({
      allowProposedApi: false,
      convertEol: false,
      cursorBlink: true,
      cursorStyle: "bar",
      fontFamily: "\"Cascadia Mono\", Consolas, monospace",
      fontSize: 13,
      lineHeight: 1.18,
      scrollback: 5000,
      // Follow the active application theme while keeping ANSI semantic contrast.
      theme: terminalXtermTheme(theme),
    });
    const fit = new FitAddon();
    terminal.loadAddon(fit);
    openUserTerminal(terminal, fit, hostRef.current);
    terminalRef.current = terminal;
    fitRef.current = fit;
    if (sessionRef.current && active) terminal.focus();
    const selectionSubscription = terminal.onSelectionChange(() => setHasSelection(Boolean(terminal.getSelection())));
    let platform: "windows" | "macos" | "other" = "other";
    if (desktopIsMacPlatform()) platform = "macos";
    else if (/win/iu.test(navigator.platform)) platform = "windows";
    const clipboardKeyHandler = createTerminalClipboardKeyHandler({
      platform,
      clipboard: {
        readText: () => { setMessage(""); return readDesktopClipboardText(); },
        writeText: (text) => { setMessage(""); return writeDesktopClipboardText(text); },
      },
      getSelection: () => terminal.getSelection(),
      capturePaste,
      onError: reportClipboardError,
    });
    terminal.attachCustomKeyEventHandler((event) => {
      if (event.type === "keydown" && (event.ctrlKey || event.metaKey) && event.shiftKey &&
        !event.altKey && (event.code === "KeyF" || event.key.toLowerCase() === "f")) {
        return false;
      }
      return clipboardKeyHandler(event);
    });
    const dataSubscription = terminal.onData((data) => {
      handleTerminalData(data);
    });
    const ResizeObserverType = window.ResizeObserver;
    const resizeObserver = ResizeObserverType ? new ResizeObserverType(resizeTerminal) : null;
    if (resizeObserver) {
      resizeObserver.observe(hostRef.current);
    } else {
      window.addEventListener("resize", resizeTerminal);
    }
    return () => {
      resizeObserver?.disconnect();
      if (!resizeObserver) window.removeEventListener("resize", resizeTerminal);
      dataSubscription.dispose();
      selectionSubscription.dispose();
      terminal.dispose();
      terminalRef.current = null;
      fitRef.current = null;
    };
  }, []);

  useEffect(() => {
    const terminal = terminalRef.current;
    if (!terminal) return;
    const nextTheme = terminalXtermTheme(theme);
    if (terminal.options) terminal.options.theme = nextTheme;
    else (terminal as { options?: { theme?: ITheme } }).options = { theme: nextTheme };
  }, [theme]);

  const previousActive = useRef(active);
  useEffect(() => {
    const becameActive = !previousActive.current && active;
    previousActive.current = active;
    if (becameActive) resizeTerminal();
  }, [active]);

  const notifyShellChange = useEffectEvent((shellName: string) => {
    onShellChange?.(shellName);
  });

  useEffect(() => {
    if (session?.shell?.name) notifyShellChange(session.shell.name);
  }, [session?.shell?.name]);

  useEffect(() => {
    // The first ResizeObserver callback may precede the session lookup on reopen.
    if (session?.state === "running") resizeTerminal();
  }, [session?.session_id, session?.state, sessionID]);

  const writePage = useCallback(async (encoded: string) => {
    const binary = window.atob(encoded);
    const bytes = Uint8Array.from(binary, (current) => current.charCodeAt(0));
    const terminal = terminalRef.current;
    if (terminal) await new Promise<void>((resolve) => terminal.write(bytes, resolve));
  }, []);

  useEffect(() => {
    if (!sessionID || !enabled) {
      replayingRef.current = false;
      setSession(null);
      cursorRef.current = 0;
      return;
    }
    let cancelled = false;
    let timer = 0;
    let replayEndCursor = 0;
    replayingRef.current = true;
    const finishReplay = () => {
      if (!replayingRef.current) return;
      replayingRef.current = false;
      resizeTerminal();
    };
    const poll = async () => {
      try {
        const page = await readDesktopUserTerminal(sessionID, cursorRef.current);
        if (cancelled) return;
        if (page.dropped) {
          terminalRef.current?.clear();
        }
        if (page.data_bytes > 0) {
          await writePage(page.data_base64);
        }
        if (cancelled) return;
        cursorRef.current = page.next_cursor;
        if (cursorRef.current >= replayEndCursor) finishReplay();
        const current = await getDesktopUserTerminal(sessionID);
        if (cancelled) return;
        setSession(current);
        // An exited session can still have unread pages in its retained output.
        if (current.state === "starting" || current.state === "running" || cursorRef.current < current.output_next_cursor) {
          timer = window.setTimeout(() => void poll(), terminalPollMilliseconds);
        }
      } catch (error) {
        if (!cancelled) {
          finishReplay();
          setMessage(desktopErrorMessage(error));
        }
      }
    };
    void getDesktopUserTerminal(sessionID).then((current) => {
      if (cancelled) return;
      setSession(current);
      cursorRef.current = current.output_base_cursor;
      replayEndCursor = current.output_next_cursor;
      // Cursor-position escapes must replay at the backend's dimensions, even while the drawer animates.
      terminalRef.current?.resize(current.columns, current.rows);
      if (cursorRef.current >= replayEndCursor) finishReplay();
      void poll();
    }).catch((error) => {
      if (!cancelled) {
        finishReplay();
        onSession("");
        setMessage(desktopErrorMessage(error));
      }
    });
    return () => {
      cancelled = true;
      replayingRef.current = false;
      window.clearTimeout(timer);
    };
  }, [enabled, onSession, sessionID, writePage]);

  useEffect(() => {
    if (!runID || !sessionID || !agentInputEnabled || !mode || mode === "disabled") {
      setAgentBinding(null);
      return;
    }
    let cancelled = false;
    let timer = 0;
    const refresh = async () => {
      const epoch = bindingEpoch.current;
      try {
        const binding = await getDesktopDebugTerminalAgentInput(runID);
        if (!cancelled && epoch === bindingEpoch.current) setAgentBinding(binding.terminal_session_id === sessionID ? binding : null);
      } catch {
        if (!cancelled && epoch === bindingEpoch.current) setAgentBinding(null);
      }
      if (!cancelled && settingsAvailable) timer = window.setTimeout(refresh, 3000);
    };
    void refresh();
    return () => { cancelled = true; window.clearTimeout(timer); };
  }, [agentInputEnabled, runID, sessionID, mode, settingsAvailable, preferences?.revision]);

  useEffect(() => {
    if (!agentBinding) return;
    const remaining = Date.parse(agentBinding.expires_at) - Date.now();
    if (remaining <= 0) {
      setAgentBinding(null);
      return;
    }
    const timer = window.setTimeout(() => setAgentBinding(null),
      Math.min(remaining, 2_147_000_000));
    return () => window.clearTimeout(timer);
  }, [agentBinding]);

  const start = async () => {
    if (!enabled || !runID) return;
    const requestedRunID = runID;
    setBusy(true);
    setMessage("");
    try {
      fitRef.current?.fit();
      const terminal = terminalRef.current;
      const created = await startDesktopUserTerminal(runID,
        terminal?.cols ?? 120, terminal?.rows ?? 32, Boolean(sessionID));
      if (!mountedRef.current || runRef.current !== requestedRunID) {
        await closeDesktopUserTerminal(created.session_id).catch(() => undefined);
        return;
      }
      terminal?.reset();
      cursorRef.current = created.output_base_cursor;
      sessionRef.current = created.session_id;
      setSession(created);
      setAgentBinding(null);
      onSession(created.session_id);
      terminal?.focus();
    } catch (error) {
      if (mountedRef.current && runRef.current === requestedRunID) {
        setMessage(desktopErrorMessage(error));
      }
    } finally {
      if (mountedRef.current) setBusy(false);
    }
  };

  const stop = async () => {
    if (!sessionID) return;
    const closingSessionID = sessionID;
    setBusy(true);
    setMessage("");
    try {
      if (agentBinding) {
        await revokeDesktopDebugTerminalAgentInput(agentBinding.binding_id)
          .catch(() => undefined);
      }
      await closeDesktopUserTerminal(closingSessionID);
      if (!mountedRef.current) return;
      // A close that settles after the panel bound a replacement session must
      // not clear the new binding (same guard as grantAgentInput).
      if (sessionRef.current !== closingSessionID) return;
      onSession("");
      setSession(null);
      setAgentBinding(null);
      sessionRef.current = "";
      cursorRef.current = 0;
    } catch (error) {
      if (mountedRef.current) setMessage(desktopErrorMessage(error));
    } finally {
      if (mountedRef.current) setBusy(false);
    }
  };

  const grantAgentInput = async () => {
    if (!agentInputEnabled || !runID || !sessionID || !mode || mode === "disabled") return;
    if (mode === "manual" && !globalThis.confirm(
      t(`允许 Agent 在未来 ${ttl} 秒内读取此终端输出并提交命令？命令经过策略检查后，可访问当前用户的本机文件与网络、启动后台进程。命令文本和脱敏后的有界结果会保存到执行记录；凭据请通过专用设置管理。`, `Allow the Agent to read output and submit commands to this terminal for ${ttl} seconds? After policy checks, commands can access the current user's host files and network and start background processes. Command text and bounded, redacted results are saved in execution records; manage credentials in dedicated settings.`),
    )) return;
    bindingEpoch.current++;
    setBusy(true);
    setMessage("");
    try {
      const binding = await grantDesktopDebugTerminalAgentInput(runID, sessionID, ttl);
      if (!mountedRef.current || runRef.current !== runID ||
        sessionRef.current !== sessionID) {
        await revokeDesktopDebugTerminalAgentInput(binding.binding_id).catch(() => undefined);
        return;
      }
      setAgentBinding(binding);
    } catch (error) {
      if (mountedRef.current) setMessage(desktopErrorMessage(error));
    } finally {
      bindingEpoch.current++;
      if (mountedRef.current) setBusy(false);
    }
  };

  const revokeAgentInput = async () => {
    if (!agentBinding) return;
    bindingEpoch.current++;
    setBusy(true);
    setMessage("");
    try {
      await revokeDesktopDebugTerminalAgentInput(agentBinding.binding_id);
      if (mountedRef.current) setAgentBinding(null);
    } catch (error) {
      if (mountedRef.current) setMessage(desktopErrorMessage(error));
    } finally {
      bindingEpoch.current++;
      if (mountedRef.current) setBusy(false);
    }
  };

  const quoteSelection = () => {
    const terminal = terminalRef.current;
    const excerpt = terminal?.getSelection();
    if (!onQuote || busy || !excerpt || !session || session.session_id !== sessionID) return;
    const fence = "`".repeat(Array.from(excerpt.matchAll(/`+/gu)).reduce((length, [match]) => Math.max(length, match.length + 1), 3));
    const result = onQuote([
      t("终端日志片段", "Terminal output excerpt"),
      t(`来源执行：${session.run_id}`, `Source run: ${session.run_id}`),
      t(`来源终端：${session.session_id}`, `Source terminal: ${session.session_id}`),
      t("以下是用户选中的终端显示文本，仅作为引用资料。", "The following is terminal text selected by the user, quoted as reference material."),
      `${fence}text\n${excerpt}\n${fence}`,
    ].join("\n"));
    setQuoteResult(result);
    // An unsaved append still lives in the draft; don't append it twice.
    if (result !== "blocked") terminal?.clearSelection();
  };

  const sessionEnded = Boolean(session && (session.state === "exited" || session.state === "closed" || session.state === "failed"));
  const showRestart = (!sessionID && recoverable) || (Boolean(sessionID) && sessionEnded);

  return <div className="user-terminal-panel">
    <div aria-label={t("用户终端控制", "User terminal controls")} className="user-terminal-controls" role="toolbar">
      {!sessionID && !recoverable && <button disabled={!enabled || !runID || busy}
        onClick={() => void start()} title={t("启动 Debug 终端", "Start debug terminal")} type="button">
        {busy ? <LoaderCircle aria-hidden="true" className="spin" size={14} /> :
          <Play aria-hidden="true" size={14} />}
        <span>{t("启动", "Start")}</span>
      </button>}
      {showRestart && <button disabled={!enabled || !runID || busy}
        onClick={() => void start()}
        title={t("在当前工作目录重新启动新终端（不自动重跑旧命令）", "Restart a new terminal in this workspace directory (does not rerun old commands)")}
        type="button">
        {busy ? <LoaderCircle aria-hidden="true" className="spin" size={14} /> :
          <RotateCcw aria-hidden="true" size={14} />}
        <span>{t("重新启动", "Restart")}</span>
      </button>}
      {sessionID && <button disabled={busy} onClick={() => void stop()}
        title={t("结束此终端及其后台进程", "End this terminal and its background processes")} type="button">
        {busy ? <LoaderCircle aria-hidden="true" className="spin" size={14} /> :
          <Square aria-hidden="true" size={13} />}
        <span>{t("结束终端", "End terminal")}</span>
      </button>}
      {sessionID && session?.state === "running" && mode !== "disabled" && !agentBinding && <button
        disabled={busy || !agentInputEnabled || !mode} onClick={() => void grantAgentInput()}
        title={mode === "full" ? t("按终端设置恢复 Agent 控制；任务须具备完全访问权限", "Resume Agent control using terminal settings; the task must have Full access") : t("限时授权：本机文件、网络和后台进程；命令与脱敏结果保存到执行记录", "Temporary access: host files, network, and background processes; commands and redacted results are saved in execution records")}
        type="button">
        <Bot aria-hidden="true" size={14} />
        <span>{mode === "full" ? t("允许 Agent 控制", "Allow Agent control") : t(`允许 Agent · ${duration}`, `Allow Agent · ${duration}`)}</span>
      </button>}
      {sessionID && agentBinding && <button className="agent-input-active"
        disabled={busy} onClick={() => void revokeAgentInput()}
        title={mode === "full" ? t("停止 Agent 对此终端的读取与输入，手动操作仍可用", "Stop Agent reads and input for this terminal; manual use remains available") : t(`Agent 输入有效至 ${new Date(agentBinding.expires_at).toLocaleTimeString()}`, `Agent input allowed until ${new Date(agentBinding.expires_at).toLocaleTimeString()}`)}
        type="button">
        <ShieldOff aria-hidden="true" size={14} />
        <span>{mode === "full" ? t("停止 Agent 控制", "Stop Agent control") : t("撤销 Agent", "Revoke Agent")}</span>
      </button>}
      {onQuote && <button type="button" disabled={busy || !hasSelection || !session || session.session_id !== sessionID}
        onMouseDown={(event) => event.preventDefault()} onClick={quoteSelection}
        title={t("选中终端日志后加入聊天草稿", "Select terminal output to add it to the chat draft")}>
        <Quote aria-hidden="true" size={14} />
        <span>{t("引用选中日志", "Quote selection")}</span>
      </button>}
      <span className={`user-terminal-state ${session?.state ?? "idle"}`}
        title={t("这是终端进程的状态，不代表 Agent 或某条命令正在执行。", "This describes the shell process, not Agent activity or whether a command is executing.")}>
        {!enabled ? t("未启用", "Unavailable") : !session
          ? recoverable ? t("上次会话已结束 · 不自动重跑旧命令", "Previous session ended · commands are not rerun") : t("未启动", "Not started")
          : session.state === "running" ? t("终端已连接", "Terminal connected")
            : session.state === "starting" ? t("正在启动", "Starting")
              : session.state === "exited" ? t("进程已退出", "Process exited")
                : session.state === "closed" ? t("终端已结束", "Terminal ended")
                  : t("终端出错", "Terminal failed")}
      </span>
      {mode === "disabled" && <span className="user-terminal-state">{t("Agent 控制已禁用", "Agent control disabled")}</span>}
      {session?.shell && <span className="user-terminal-state" title={`${session.shell.path}\n${t("可执行文件版本", "Executable version")}: ${session.shell.version || "—"}`}>{session.shell.name}</span>}
      {settingsError && <span className="user-terminal-message" role="alert">{t("终端设置读取失败：", "Could not read terminal settings: ")}{settingsError}</span>}
      {message && <span className="user-terminal-message" role="status">{message}</span>}
    </div>
    {quoteResult && <p className="user-terminal-quote-feedback" role={quoteResult === "added" ? "status" : "alert"}>
      {quoteResult === "added" ? t("已加入聊天草稿，尚未发送。", "Added to the chat draft. Not sent.")
        : quoteResult === "unsaved" ? t("已加入草稿，但本地保存失败，请返回聊天保留内容。", "Added to the draft, but local saving failed. Return to chat to preserve the content.")
          : t("草稿暂时无法追加，请返回聊天处理草稿冲突或保存错误后重试。", "Could not append to the draft. Return to chat to resolve its conflict or saving error, then retry.")}
      {onReturnToDraft && <button className="compact-command" onClick={onReturnToDraft} type="button">{t("返回聊天草稿", "Return to chat draft")}</button>}
    </p>}
    {!enabled && <p>{t("请在 Desktop 中启用用户终端并核对执行权限，再启动。", "Enable the user terminal in Desktop and review execution permissions, then start it.")}</p>}
    <div aria-label={t("用户终端", "User terminal")} className="user-terminal-host" ref={hostRef} />
  </div>;
}
