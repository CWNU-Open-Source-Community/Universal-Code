import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, vi } from "vitest";
import { UserTerminalPanel } from "./user-terminal-panel";

const terminalMocks = vi.hoisted(() => ({
  agentEnabled: false,
  close: vi.fn(async () => undefined),
  emitData: (_data: string) => {},
  fit: vi.fn(),
  get: vi.fn(),
  getAgent: vi.fn(),
  grantAgent: vi.fn(),
  output: vi.fn(),
  read: vi.fn(),
  resize: vi.fn(async () => undefined),
  revokeAgent: vi.fn(async () => undefined),
  terminalResize: vi.fn(),
  start: vi.fn(),
  write: vi.fn(async () => ({ protocol_version: "desktop_user_terminal.v1", session_id: "unused", bytes_written: 1 })),
}));

vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    cols = 120;
    rows = 32;
    element = document.createElement("div");
    loadAddon() {}
    open(host: HTMLElement) {
      host.appendChild(this.element);
      this.write.mockImplementation((_bytes: Uint8Array, callback?: () => void) => callback?.());
      terminalMocks.output = this.write;
      terminalMocks.terminalResize = this.resize;
    }
    onData(callback: (data: string) => void) {
      terminalMocks.emitData = callback;
      return { dispose() {} };
    }
    clear() {}
    dispose() {}
    focus() {}
    resize = vi.fn((columns: number, rows: number) => { this.cols = columns; this.rows = rows; });
    reset() {}
    write = vi.fn();
  },
}));

vi.mock("@xterm/addon-fit", () => ({
  FitAddon: class {
    fit = terminalMocks.fit;
  },
}));

vi.mock("../lib/desktop-bridge", () => ({
  closeDesktopUserTerminal: terminalMocks.close,
  desktopDebugTerminalAgentInputEnabled: () => terminalMocks.agentEnabled,
  desktopErrorMessage: (value: unknown) => String(value),
  desktopUserTerminalEnabled: () => true,
  getDesktopUserTerminal: terminalMocks.get,
  getDesktopDebugTerminalAgentInput: terminalMocks.getAgent,
  grantDesktopDebugTerminalAgentInput: terminalMocks.grantAgent,
  readDesktopUserTerminal: terminalMocks.read,
  resizeDesktopUserTerminal: terminalMocks.resize,
  revokeDesktopDebugTerminalAgentInput: terminalMocks.revokeAgent,
  startDesktopUserTerminal: terminalMocks.start,
  writeDesktopUserTerminal: terminalMocks.write,
}));

describe("UserTerminalPanel", () => {
  let notifyResize: () => void;

  beforeEach(() => {
    vi.stubGlobal("ResizeObserver", class {
      constructor(callback: ResizeObserverCallback) {
        notifyResize = () => callback([], this as unknown as ResizeObserver);
      }
      observe() {}
      disconnect() {}
    });
    terminalMocks.fit.mockReset();
    terminalMocks.agentEnabled = false;
    vi.clearAllMocks();
  });

  it("closes a terminal whose asynchronous start completes after the Run changed", async () => {
    let resolveStart: (value: unknown) => void = () => undefined;
    terminalMocks.start.mockReturnValueOnce(new Promise((resolve) => {
      resolveStart = resolve;
    }));
    const onSession = vi.fn();
    const view = render(<UserTerminalPanel runID="run-original" sessionID=""
      onSession={onSession} />);

    fireEvent.click(screen.getByRole("button", { name: "Start" }));
    expect(terminalMocks.start).toHaveBeenCalledWith("run-original", 120, 32, false);

    view.rerender(<UserTerminalPanel runID="run-next" sessionID=""
      onSession={onSession} />);
    await act(async () => {
      resolveStart({
        protocol_version: "desktop_user_terminal.v1",
        session_id: "terminal-stale",
        run_id: "run-original",
        state: "running",
        backend: "windows-conpty-user-v1",
        columns: 120,
        rows: 32,
        output_base_cursor: 0,
        output_next_cursor: 0,
        exit_code: 0,
        user_owned: true,
        agent_input_default: false,
        job_assigned_at_creation: true,
        kill_on_job_close: true,
        persistent: true,
        process_local: true,
        raw_output_persisted: false,
      });
    });

    await waitFor(() => {
      expect(terminalMocks.close).toHaveBeenCalledWith("terminal-stale");
    });
    expect(onSession).not.toHaveBeenCalled();
  });

  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("fits unknown or exited sessions without resizing or answering replayed terminal queries", async () => {
    let completeGet: (value: unknown) => void = () => {};
    const cursorQuery = "\x1b[6n";
    const cursorReply = "\x1b[1;1R";
    const exited = {
      session_id: "terminal-exited", run_id: "run-1", state: "exited",
      columns: 120, rows: 32,
      output_base_cursor: 0, output_next_cursor: cursorQuery.length,
    };
    terminalMocks.get.mockReset().mockResolvedValue(exited)
      .mockReturnValueOnce(new Promise((resolve) => { completeGet = resolve; }));
    terminalMocks.read.mockReset().mockResolvedValue({
      data_bytes: cursorQuery.length, data_base64: btoa(cursorQuery), next_cursor: cursorQuery.length, dropped: false,
    });
    render(<UserTerminalPanel runID="run-1" sessionID="terminal-exited" onSession={vi.fn()} />);
    const initialFits = terminalMocks.fit.mock.calls.length;
    act(() => notifyResize());
    await act(async () => { terminalMocks.emitData(cursorReply); });
    expect(terminalMocks.fit).toHaveBeenCalledTimes(initialFits);
    expect(terminalMocks.resize).not.toHaveBeenCalled();
    expect(terminalMocks.write).not.toHaveBeenCalled();

    await act(async () => { completeGet(exited); });
    expect(new TextDecoder().decode(terminalMocks.output.mock.calls[0][0])).toBe(cursorQuery);
    await act(async () => { terminalMocks.emitData(cursorReply); });
    act(() => notifyResize());
    expect(terminalMocks.fit).toHaveBeenCalledTimes(initialFits + 2);
    expect(terminalMocks.resize).not.toHaveBeenCalled();
    expect(terminalMocks.write).not.toHaveBeenCalled();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("syncs dimensions and forwards terminal replies only for the matching running session", async () => {
    vi.useFakeTimers();
    const cursorReply = "\x1b[1;1R";
    const running = (session_id: string) => ({
      session_id, run_id: "run-1", state: "running", output_base_cursor: 0, output_next_cursor: 0,
      columns: 120, rows: 32,
    });
    let completeGet: (value: unknown) => void = () => {};
    terminalMocks.get.mockReset().mockResolvedValue(running("terminal-first"))
      .mockReturnValueOnce(new Promise((resolve) => { completeGet = resolve; }));
    terminalMocks.read.mockReset().mockResolvedValue({ data_bytes: 0, next_cursor: 0, dropped: false });
    const onSession = vi.fn();
    const view = render(<UserTerminalPanel runID="run-1" sessionID="terminal-first" onSession={onSession} />);
    act(() => notifyResize());
    expect(terminalMocks.resize).not.toHaveBeenCalled();
    await act(async () => { completeGet(running("terminal-first")); });
    // No further container resize is needed after the asynchronous session lookup.
    expect(terminalMocks.resize).toHaveBeenCalledTimes(1);
    expect(terminalMocks.resize).toHaveBeenLastCalledWith("terminal-first", 120, 32);
    await act(async () => { terminalMocks.emitData(cursorReply); });
    expect(terminalMocks.write).toHaveBeenCalledTimes(1);
    expect(terminalMocks.write).toHaveBeenLastCalledWith("terminal-first", cursorReply);

    terminalMocks.get.mockResolvedValue(running("terminal-next"))
      .mockReturnValueOnce(new Promise((resolve) => { completeGet = resolve; }));
    view.rerender(<UserTerminalPanel runID="run-1" sessionID="terminal-next" onSession={onSession} />);
    act(() => notifyResize());
    await act(async () => { terminalMocks.emitData(cursorReply); });
    expect(terminalMocks.resize).toHaveBeenCalledTimes(1);
    expect(terminalMocks.write).toHaveBeenCalledTimes(1);

    await act(async () => { completeGet(running("terminal-next")); });
    expect(terminalMocks.resize).toHaveBeenCalledTimes(2);
    expect(terminalMocks.resize).toHaveBeenLastCalledWith("terminal-next", 120, 32);
    await act(async () => { terminalMocks.emitData(cursorReply); });
    expect(terminalMocks.write).toHaveBeenCalledTimes(2);
    expect(terminalMocks.write).toHaveBeenLastCalledWith("terminal-next", cursorReply);
    act(() => notifyResize());
    expect(terminalMocks.resize).toHaveBeenCalledTimes(3);
  });

  it("drains all retained output after exit, then stops polling", async () => {
    vi.useFakeTimers();
    const baseCursor = 123;
    const firstPage = "x".repeat(65536);
    const finalPage = "\r\nFINAL_BUILD_RESULT\r\n";
    const output = firstPage + finalPage;
    terminalMocks.get.mockReset().mockResolvedValue({
      session_id: "terminal-exited", run_id: "run-1", state: "exited",
      columns: 120, rows: 32,
      output_base_cursor: baseCursor, output_next_cursor: baseCursor + output.length,
    });
    terminalMocks.read.mockReset().mockImplementation(async (_sessionID: string, cursor: number) => {
      const data = output.slice(cursor - baseCursor, cursor - baseCursor + 65536);
      return { data_bytes: data.length, data_base64: btoa(data), next_cursor: cursor + data.length, dropped: false };
    });
    render(<UserTerminalPanel runID="run-1" sessionID="terminal-exited" onSession={vi.fn()} />);
    await act(async () => {});
    expect(terminalMocks.read).toHaveBeenNthCalledWith(1, "terminal-exited", baseCursor);

    await act(async () => { await vi.advanceTimersByTimeAsync(120); });
    expect(terminalMocks.read).toHaveBeenNthCalledWith(2, "terminal-exited", baseCursor + firstPage.length);
    const writtenPages = terminalMocks.output.mock.calls.map(([bytes]) => new TextDecoder().decode(bytes));
    expect(writtenPages).toEqual([firstPage, finalPage]);
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(terminalMocks.read).toHaveBeenCalledTimes(2);
    expect(terminalMocks.output).toHaveBeenCalledTimes(2);
  });

  it.each(["read", "parse"])("stops replay when an in-flight %s completes after unmount", async (phase) => {
    vi.useFakeTimers();
    let completeRead: (value: unknown) => void = () => {};
    let completeWrite = () => {};
    terminalMocks.get.mockReset().mockResolvedValue({
      session_id: "terminal-exited", run_id: "run-1", state: "exited",
      columns: 120, rows: 32,
      output_base_cursor: 0, output_next_cursor: 65540,
    });
    terminalMocks.read.mockReset()
      .mockResolvedValueOnce({ data_bytes: 65536, data_base64: btoa("x".repeat(65536)), next_cursor: 65536, dropped: false })
      .mockReturnValueOnce(new Promise((resolve) => { completeRead = resolve; }));
    const view = render(<UserTerminalPanel runID="run-1" sessionID="terminal-exited" onSession={vi.fn()} />);
    terminalMocks.output.mockImplementationOnce((_bytes: Uint8Array, callback: () => void) => callback())
      .mockImplementationOnce((_bytes: Uint8Array, callback: () => void) => { completeWrite = callback; });
    await act(async () => { await vi.advanceTimersByTimeAsync(120); });
    expect(terminalMocks.read).toHaveBeenCalledTimes(2);
    expect(terminalMocks.output).toHaveBeenCalledTimes(1);
    const finalPage = { data_bytes: 4, data_base64: btoa("late"), next_cursor: 65540, dropped: false };
    if (phase === "parse") await act(async () => { completeRead(finalPage); });
    const writeCount = terminalMocks.output.mock.calls.length;
    const getCount = terminalMocks.get.mock.calls.length;

    view.unmount();
    await act(async () => {
      if (phase === "read") completeRead(finalPage);
      else completeWrite();
      await vi.advanceTimersByTimeAsync(1000);
    });
    expect(terminalMocks.output).toHaveBeenCalledTimes(writeCount);
    expect(terminalMocks.get).toHaveBeenCalledTimes(getCount);
    expect(terminalMocks.read).toHaveBeenCalledTimes(2);
  });

  it("keeps backend dimensions through every parsed history page before fitting the container", async () => {
    vi.useFakeTimers();
    const initial = {
      session_id: "terminal-running", run_id: "run-1", state: "running",
      columns: 41, rows: 24, output_base_cursor: 0, output_next_cursor: 6,
    };
    terminalMocks.get.mockReset().mockResolvedValue({ ...initial, output_next_cursor: 9 })
      .mockResolvedValueOnce(initial);
    terminalMocks.read.mockReset()
      .mockResolvedValueOnce({ data_bytes: 3, data_base64: btoa("one"), next_cursor: 3, dropped: false })
      .mockResolvedValueOnce({ data_bytes: 3, data_base64: btoa("two"), next_cursor: 6, dropped: false })
      .mockResolvedValueOnce({ data_bytes: 3, data_base64: btoa("new"), next_cursor: 9, dropped: false });
    terminalMocks.fit.mockImplementation(() => terminalMocks.terminalResize(80, 24));
    render(<UserTerminalPanel runID="run-1" sessionID="terminal-running" onSession={vi.fn()} />);
    const parsed: Array<() => void> = [];
    terminalMocks.output.mockImplementation((_bytes: Uint8Array, callback: () => void) => { parsed.push(callback); });
    await act(async () => {});
    const initialFits = terminalMocks.fit.mock.calls.length;
    act(() => { notifyResize(); notifyResize(); });
    await act(async () => { await vi.advanceTimersByTimeAsync(500); });
    expect(terminalMocks.read).toHaveBeenCalledTimes(1);
    expect(terminalMocks.fit).toHaveBeenCalledTimes(initialFits);
    expect(terminalMocks.terminalResize).toHaveBeenLastCalledWith(41, 24);
    expect(terminalMocks.resize).not.toHaveBeenCalled();

    await act(async () => { parsed.shift()!(); await vi.advanceTimersByTimeAsync(120); });
    expect(terminalMocks.read).toHaveBeenCalledTimes(2);
    act(() => notifyResize());
    expect(terminalMocks.terminalResize).toHaveBeenLastCalledWith(41, 24);
    expect(terminalMocks.fit).toHaveBeenCalledTimes(initialFits);

    await act(async () => { parsed.shift()!(); });
    expect(terminalMocks.fit).toHaveBeenCalledTimes(initialFits + 1);
    expect(terminalMocks.terminalResize).toHaveBeenLastCalledWith(80, 24);
    expect(terminalMocks.resize).toHaveBeenLastCalledWith("terminal-running", 80, 24);
    await act(async () => { await vi.advanceTimersByTimeAsync(120); });
    expect(terminalMocks.read).toHaveBeenCalledTimes(3);
    act(() => notifyResize());
    expect(terminalMocks.fit).toHaveBeenCalledTimes(initialFits + 2);
    await act(async () => { parsed.shift()!(); });
  });

  it.each(["lookup", "read"])("resumes container fitting after a replay %s error", async (phase) => {
    terminalMocks.get.mockReset().mockResolvedValue({
      session_id: "terminal-exited", run_id: "run-1", state: "exited",
      columns: 41, rows: 24, output_base_cursor: 0, output_next_cursor: 10,
    });
    terminalMocks.read.mockReset().mockRejectedValue(new Error("replay failed"));
    if (phase === "lookup") terminalMocks.get.mockRejectedValueOnce(new Error("lookup failed"));
    render(<UserTerminalPanel runID="run-1" sessionID="terminal-exited" onSession={vi.fn()} />);
    await act(async () => {});
    const fitCount = terminalMocks.fit.mock.calls.length;
    act(() => notifyResize());
    expect(terminalMocks.fit).toHaveBeenCalledTimes(fitCount + 1);
    expect(terminalMocks.resize).not.toHaveBeenCalled();
    expect(screen.getByRole("status")).toHaveTextContent("failed");
  });

  it("requires a visible confirmation before granting bounded Agent input", async () => {
    terminalMocks.agentEnabled = true;
    terminalMocks.getAgent.mockRejectedValueOnce(new Error("no active binding"));
    terminalMocks.get.mockResolvedValue({
      protocol_version: "desktop_user_terminal.v1",
      session_id: "terminal-1",
      run_id: "run-1",
      state: "running",
      backend: "windows-conpty-user-v1",
      columns: 120,
      rows: 32,
      output_base_cursor: 0,
      output_next_cursor: 0,
      exit_code: 0,
      user_owned: true,
      agent_input_default: false,
      job_assigned_at_creation: true,
      kill_on_job_close: true,
      persistent: true,
      process_local: true,
      raw_output_persisted: false,
    });
    terminalMocks.read.mockResolvedValue({
      protocol_version: "desktop_user_terminal.v1",
      session_id: "terminal-1",
      base_cursor: 0,
      next_cursor: 0,
      data_base64: "",
      data_bytes: 0,
      dropped: false,
      state: "running",
    });
    const binding = {
      protocol_version: "desktop_debug_terminal_agent_input.v1",
      binding_id: "terminal-input-binding-1",
      run_id: "run-1",
      terminal_session_id: "terminal-1",
      issued_at: new Date().toISOString(),
      expires_at: new Date(Date.now() + 300_000).toISOString(),
      process_local: true,
      token_exposed: false,
      raw_input_persisted: false,
    };
    terminalMocks.grantAgent.mockResolvedValue(binding);
    const confirm = vi.spyOn(globalThis, "confirm").mockReturnValue(true);
    render(<UserTerminalPanel runID="run-1" sessionID="terminal-1"
      onSession={vi.fn()} />);

    fireEvent.click(await screen.findByRole("button", { name: "Allow Agent · 5m" }));
    expect(confirm).toHaveBeenCalledWith(expect.stringContaining("host files and network"));
    await waitFor(() => {
      expect(terminalMocks.grantAgent).toHaveBeenCalledWith(
        "run-1", "terminal-1", 300,
      );
    });
    fireEvent.click(await screen.findByRole("button", { name: "Revoke Agent" }));
    await waitFor(() => {
      expect(terminalMocks.revokeAgent).toHaveBeenCalledWith(binding.binding_id);
    });
    confirm.mockRestore();
  });
});
