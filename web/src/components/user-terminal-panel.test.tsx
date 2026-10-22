import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, vi } from "vitest";
import { UserTerminalPanel } from "./user-terminal-panel";

const terminalMocks = vi.hoisted(() => ({
  agentEnabled: false,
  close: vi.fn(async () => undefined),
  fit: vi.fn(),
  get: vi.fn(),
  getAgent: vi.fn(),
  grantAgent: vi.fn(),
  output: vi.fn(),
  read: vi.fn(),
  resize: vi.fn(async () => undefined),
  revokeAgent: vi.fn(async () => undefined),
  start: vi.fn(),
}));

vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    cols = 120;
    rows = 32;
    element = document.createElement("div");
    loadAddon() {}
    open(host: HTMLElement) { host.appendChild(this.element); terminalMocks.output = this.write; }
    onData() {
      return { dispose() {} };
    }
    clear() {}
    dispose() {}
    focus() {}
    resize() {}
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
  writeDesktopUserTerminal: vi.fn(async () => ({
    protocol_version: "desktop_user_terminal.v1",
    session_id: "unused",
    bytes_written: 1,
  })),
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

  it("fits a reopened terminal without resizing an unknown or exited backend session", async () => {
    let completeGet: (value: unknown) => void = () => {};
    const exited = {
      session_id: "terminal-exited", run_id: "run-1", state: "exited",
      output_base_cursor: 0, output_next_cursor: 0,
    };
    terminalMocks.get.mockReset().mockResolvedValue(exited)
      .mockReturnValueOnce(new Promise((resolve) => { completeGet = resolve; }));
    terminalMocks.read.mockReset().mockResolvedValue({ data_bytes: 0, next_cursor: 0, dropped: false });
    render(<UserTerminalPanel runID="run-1" sessionID="terminal-exited" onSession={vi.fn()} />);
    const initialFits = terminalMocks.fit.mock.calls.length;
    act(() => notifyResize());
    expect(terminalMocks.fit).toHaveBeenCalledTimes(initialFits + 1);
    expect(terminalMocks.resize).not.toHaveBeenCalled();

    await act(async () => { completeGet(exited); });
    act(() => notifyResize());
    expect(terminalMocks.fit).toHaveBeenCalledTimes(initialFits + 2);
    expect(terminalMocks.resize).not.toHaveBeenCalled();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("syncs dimensions once a reopened session is confirmed running and keeps session IDs isolated", async () => {
    vi.useFakeTimers();
    const running = (session_id: string) => ({
      session_id, run_id: "run-1", state: "running", output_base_cursor: 0, output_next_cursor: 0,
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

    terminalMocks.get.mockResolvedValue(running("terminal-next"))
      .mockReturnValueOnce(new Promise((resolve) => { completeGet = resolve; }));
    view.rerender(<UserTerminalPanel runID="run-1" sessionID="terminal-next" onSession={onSession} />);
    act(() => notifyResize());
    expect(terminalMocks.resize).toHaveBeenCalledTimes(1);

    await act(async () => { completeGet(running("terminal-next")); });
    expect(terminalMocks.resize).toHaveBeenCalledTimes(2);
    expect(terminalMocks.resize).toHaveBeenLastCalledWith("terminal-next", 120, 32);
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

  it("discards a retained output page that resolves after unmount", async () => {
    vi.useFakeTimers();
    let completeRead: (value: unknown) => void = () => {};
    terminalMocks.get.mockReset().mockResolvedValue({
      session_id: "terminal-exited", run_id: "run-1", state: "exited",
      output_base_cursor: 0, output_next_cursor: 65540,
    });
    terminalMocks.read.mockReset()
      .mockResolvedValueOnce({ data_bytes: 65536, data_base64: btoa("x".repeat(65536)), next_cursor: 65536, dropped: false })
      .mockReturnValueOnce(new Promise((resolve) => { completeRead = resolve; }));
    const view = render(<UserTerminalPanel runID="run-1" sessionID="terminal-exited" onSession={vi.fn()} />);
    await act(async () => { await vi.advanceTimersByTimeAsync(120); });
    expect(terminalMocks.read).toHaveBeenCalledTimes(2);
    expect(terminalMocks.output).toHaveBeenCalledTimes(1);
    const getCount = terminalMocks.get.mock.calls.length;

    view.unmount();
    await act(async () => {
      completeRead({ data_bytes: 4, data_base64: btoa("late"), next_cursor: 65540, dropped: false });
      await vi.advanceTimersByTimeAsync(1000);
    });
    expect(terminalMocks.output).toHaveBeenCalledTimes(1);
    expect(terminalMocks.get).toHaveBeenCalledTimes(getCount);
    expect(terminalMocks.read).toHaveBeenCalledTimes(2);
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
