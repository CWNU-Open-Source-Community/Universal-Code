import { useCallback, useEffect, useEffectEvent, useRef, useState } from "react";
import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { Bot, LoaderCircle, Play, ShieldOff, Square } from "lucide-react";
import {
  closeDesktopUserTerminal,
  desktopDebugTerminalAgentInputEnabled,
  desktopErrorMessage,
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
import { useLocale } from "../lib/locale";
import { openUserTerminal } from "../lib/open-user-terminal";

const terminalPollMilliseconds = 120;

export function UserTerminalPanel({ runID, sessionID, onSession }: {
  runID: string;
  sessionID: string;
  onSession: (sessionID: string) => void;
}) {
  const { t } = useLocale();
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
  const enabled = desktopUserTerminalEnabled();
  const agentInputEnabled = desktopDebugTerminalAgentInputEnabled();

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

  const resizeTerminal = useEffectEvent(() => {
    if (replayingRef.current) return;
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
      // Keep the terminal canvas neutral; ANSI colors preserve command-output meaning.
      theme: {
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
      },
    });
    const fit = new FitAddon();
    terminal.loadAddon(fit);
    openUserTerminal(terminal, fit, hostRef.current);
    terminalRef.current = terminal;
    fitRef.current = fit;
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
      terminal.dispose();
      terminalRef.current = null;
      fitRef.current = null;
    };
  }, []);

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
        if (current.state === "running" || cursorRef.current < current.output_next_cursor) {
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
    if (!runID || !sessionID || !agentInputEnabled) {
      setAgentBinding(null);
      return;
    }
    let cancelled = false;
    void getDesktopDebugTerminalAgentInput(runID).then((binding) => {
      if (!cancelled && binding.terminal_session_id === sessionID) {
        setAgentBinding(binding);
      }
    }).catch(() => {
      if (!cancelled) setAgentBinding(null);
    });
    return () => { cancelled = true; };
  }, [agentInputEnabled, runID, sessionID]);

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
    setBusy(true);
    setMessage("");
    try {
      if (agentBinding) {
        await revokeDesktopDebugTerminalAgentInput(agentBinding.binding_id)
          .catch(() => undefined);
      }
      await closeDesktopUserTerminal(sessionID);
      if (!mountedRef.current) return;
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
    if (!agentInputEnabled || !runID || !sessionID) return;
    if (!globalThis.confirm(
      t("允许 Agent 在未来 5 分钟向此终端提交命令？命令经过策略检查后，可访问当前用户的本机文件与网络、启动后台进程。命令文本和脱敏后的有界结果会保存到执行记录；凭据请通过专用设置管理。", "Allow the Agent to submit commands to this terminal for 5 minutes? After policy checks, commands can access the current user's host files and network and start background processes. Command text and bounded, redacted results are saved in execution records; manage credentials in dedicated settings."),
    )) return;
    setBusy(true);
    setMessage("");
    try {
      const binding = await grantDesktopDebugTerminalAgentInput(runID, sessionID, 300);
      if (!mountedRef.current || runRef.current !== runID ||
        sessionRef.current !== sessionID) {
        await revokeDesktopDebugTerminalAgentInput(binding.binding_id).catch(() => undefined);
        return;
      }
      setAgentBinding(binding);
    } catch (error) {
      if (mountedRef.current) setMessage(desktopErrorMessage(error));
    } finally {
      if (mountedRef.current) setBusy(false);
    }
  };

  const revokeAgentInput = async () => {
    if (!agentBinding) return;
    setBusy(true);
    setMessage("");
    try {
      await revokeDesktopDebugTerminalAgentInput(agentBinding.binding_id);
      if (mountedRef.current) setAgentBinding(null);
    } catch (error) {
      if (mountedRef.current) setMessage(desktopErrorMessage(error));
    } finally {
      if (mountedRef.current) setBusy(false);
    }
  };

  return <div className="user-terminal-panel">
    <div aria-label={t("用户终端控制", "User terminal controls")} className="user-terminal-controls" role="toolbar">
      {!sessionID && <button disabled={!enabled || !runID || busy}
        onClick={() => void start()} title={t("启动 Debug 终端", "Start debug terminal")} type="button">
        {busy ? <LoaderCircle aria-hidden="true" className="spin" size={14} /> :
          <Play aria-hidden="true" size={14} />}
        <span>{t("启动", "Start")}</span>
      </button>}
      {sessionID && <button disabled={busy} onClick={() => void stop()}
        title={t("关闭终端", "Close terminal")} type="button">
        {busy ? <LoaderCircle aria-hidden="true" className="spin" size={14} /> :
          <Square aria-hidden="true" size={13} />}
        <span>{t("关闭", "Close")}</span>
      </button>}
      {sessionID && session?.state === "running" && !agentBinding && <button
        disabled={busy || !agentInputEnabled} onClick={() => void grantAgentInput()}
        title={t("限时授权：本机文件、网络和后台进程；命令与脱敏结果保存到执行记录", "Temporary access: host files, network, and background processes; commands and redacted results are saved in execution records")}
        type="button">
        <Bot aria-hidden="true" size={14} />
        <span>{t("允许 Agent · 5m", "Allow Agent · 5m")}</span>
      </button>}
      {sessionID && agentBinding && <button className="agent-input-active"
        disabled={busy} onClick={() => void revokeAgentInput()}
        title={t(`Agent 输入有效至 ${new Date(agentBinding.expires_at).toLocaleTimeString()}`, `Agent input allowed until ${new Date(agentBinding.expires_at).toLocaleTimeString()}`)}
        type="button">
        <ShieldOff aria-hidden="true" size={14} />
        <span>{t("撤销 Agent", "Revoke Agent")}</span>
      </button>}
      <span className={`user-terminal-state ${session?.state ?? "idle"}`}>
        {session?.state ?? (enabled ? "idle" : "disabled")}
      </span>
      {message && <span className="user-terminal-message" role="status">{message}</span>}
    </div>
    {!enabled && <p>{t("请在 Desktop 中启用用户终端并核对执行权限，再启动。", "Enable the user terminal in Desktop and review execution permissions, then start it.")}</p>}
    <div aria-label={t("用户终端", "User terminal")} className="user-terminal-host" ref={hostRef} />
  </div>;
}
