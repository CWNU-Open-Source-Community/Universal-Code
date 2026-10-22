import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, vi } from "vitest";
import { UserTerminalPanel } from "./user-terminal-panel";

const terminalMocks = vi.hoisted(() => ({
  instances: [] as Array<{ select: (text: string) => void; disposed: boolean; selectionDisposed: boolean; write: (data: Uint8Array) => void }>,
  agentEnabled: false,
  close: vi.fn(async () => undefined),
  get: vi.fn(),
  getAgent: vi.fn(),
  grantAgent: vi.fn(),
  read: vi.fn(),
  revokeAgent: vi.fn(async () => undefined),
  start: vi.fn(),
  write: vi.fn(async () => ({ protocol_version: "desktop_user_terminal.v1", session_id: "unused", bytes_written: 1 })),
}));

vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    selected = "";
    changed = () => {};
    disposed = false;
    selectionDisposed = false;
    constructor() { terminalMocks.instances.push(this); }
    select(text: string) { this.selected = text; this.changed(); }
    getSelection() { return this.selected; }
    clearSelection() { this.select(""); }
    onSelectionChange(changed: () => void) {
      this.changed = changed;
      return { dispose: () => { this.selectionDisposed = true; this.changed = () => {}; } };
    }
    cols = 120;
    rows = 32;
    loadAddon() {}
    open() {}
    onData() {
      return { dispose() {} };
    }
    clear() {}
    dispose() { this.disposed = true; }
    focus() {}
    reset() {}
    write = vi.fn();
  },
}));

vi.mock("@xterm/addon-fit", () => ({
  FitAddon: class {
    fit() {}
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
  resizeDesktopUserTerminal: vi.fn(async () => undefined),
  revokeDesktopDebugTerminalAgentInput: terminalMocks.revokeAgent,
  startDesktopUserTerminal: terminalMocks.start,
  writeDesktopUserTerminal: terminalMocks.write,
}));

describe("UserTerminalPanel", () => {
  beforeEach(() => {
    terminalMocks.agentEnabled = false;
    vi.clearAllMocks();
    terminalMocks.instances = [];
    terminalMocks.get.mockImplementation(async (id: string) => ({ session_id: id, run_id: "source-run", state: "exited", output_base_cursor: 0 }));
    terminalMocks.read.mockResolvedValue({ data_bytes: 0, next_cursor: 0, dropped: false, state: "exited" });
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

  it("quotes only the current selection with its actual source, preserving whitespace and Markdown fences", async () => {
    const onQuote = vi.fn().mockReturnValue("added");
    const onReturnToDraft = vi.fn();
    render(<UserTerminalPanel runID="current-run" sessionID="terminal-1" onSession={vi.fn()}
      onQuote={onQuote} onReturnToDraft={onReturnToDraft} />);
    const button = screen.getByRole("button", { name: "Quote selection" });
    expect(button).toBeDisabled();
    act(() => terminalMocks.instances[0].select("old selection"));
    await waitFor(() => expect(button).toBeEnabled());
    const excerpt = "  build failed 中文\n\n```\n  final line  ";
    act(() => terminalMocks.instances[0].select(excerpt));
    fireEvent.click(button);
    expect(onQuote).toHaveBeenCalledOnce();
    const content = onQuote.mock.calls[0][0];
    expect(content).toContain("Source run: source-run\nSource terminal: terminal-1");
    expect(content).toContain(`\n\`\`\`\`text\n${excerpt}\n\`\`\`\``);
    expect(content).not.toContain("current-run");
    expect(content).not.toContain("old selection");
    expect(button).toBeDisabled();
    expect(screen.getByRole("status")).toHaveTextContent("Not sent");
    fireEvent.click(screen.getByRole("button", { name: "Return to chat draft" }));
    expect(onReturnToDraft).toHaveBeenCalledOnce();
    expect(terminalMocks.grantAgent).not.toHaveBeenCalled();
    expect(terminalMocks.write).not.toHaveBeenCalled();
  });

  it.each(["blocked", "unsaved"] as const)("handles a %s draft without losing a rejected selection or duplicating an accepted append", async (result) => {
    const onQuote = vi.fn().mockReturnValue(result);
    render(<UserTerminalPanel runID="run-1" sessionID="terminal-1" onSession={vi.fn()} onQuote={onQuote} />);
    act(() => terminalMocks.instances[0].select("failure"));
    const button = screen.getByRole("button", { name: "Quote selection" });
    await waitFor(() => expect(button).toBeEnabled());
    fireEvent.click(button);
    expect(screen.getByRole("alert")).toHaveTextContent(result === "blocked" ? "Could not append" : "local saving failed");
    if (result === "blocked") {
      expect(button).toBeEnabled();
      onQuote.mockReturnValue("added");
      fireEvent.click(button);
      expect(onQuote).toHaveBeenCalledTimes(2);
    } else {
      expect(button).toBeDisabled();
      fireEvent.click(button);
      expect(onQuote).toHaveBeenCalledOnce();
    }
  });

  it.each(["run", "session"] as const)("discards the old terminal and a late read on %s changes", async (changed) => {
    let completeRead: (value: unknown) => void = () => {};
    terminalMocks.read.mockReturnValueOnce(new Promise((resolve) => { completeRead = resolve; }));
    const onSession = vi.fn(); const onQuote = vi.fn().mockReturnValue("added");
    const view = render(<UserTerminalPanel runID="run-1" sessionID="terminal-1" onSession={onSession} onQuote={onQuote} />);
    await waitFor(() => expect(terminalMocks.read).toHaveBeenCalledOnce());
    const oldTerminal = terminalMocks.instances[0];
    act(() => oldTerminal.select("old output"));
    expect(screen.getByRole("button", { name: "Quote selection" })).toBeEnabled();
    view.rerender(<UserTerminalPanel runID={changed === "run" ? "run-2" : "run-1"}
      sessionID={changed === "session" ? "terminal-2" : "terminal-1"} onSession={onSession} onQuote={onQuote} />);
    expect(oldTerminal.disposed).toBe(true);
    expect(oldTerminal.selectionDisposed).toBe(true);
    await act(async () => completeRead({ data_bytes: 4, data_base64: btoa("late"), next_cursor: 4, dropped: false }));
    const button = screen.getByRole("button", { name: "Quote selection" });
    expect(button).toBeDisabled();
    fireEvent.click(button);
    expect(onQuote).not.toHaveBeenCalled();
    expect(terminalMocks.instances[1].write).not.toHaveBeenCalled();
    view.unmount();
    expect(terminalMocks.instances[1].selectionDisposed).toBe(true);
  });

  it("does not quote before source identity is loaded or if it does not match the displayed session", async () => {
    let completeGet: (value: unknown) => void = () => {};
    terminalMocks.get.mockReturnValueOnce(new Promise((resolve) => { completeGet = resolve; }));
    const onQuote = vi.fn();
    render(<UserTerminalPanel runID="run-1" sessionID="terminal-1" onSession={vi.fn()} onQuote={onQuote} />);
    act(() => terminalMocks.instances[0].select("selected"));
    const button = screen.getByRole("button", { name: "Quote selection" });
    expect(button).toBeDisabled();
    terminalMocks.get.mockResolvedValue({ session_id: "wrong-session", run_id: "wrong-run", state: "exited", output_base_cursor: 0 });
    await act(async () => completeGet({ session_id: "wrong-session", run_id: "wrong-run", state: "exited", output_base_cursor: 0 }));
    expect(button).toBeDisabled();
    fireEvent.click(button);
    expect(onQuote).not.toHaveBeenCalled();
  });
});
