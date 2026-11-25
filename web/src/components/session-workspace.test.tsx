import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import type { APIClient } from "../api/client";
import type { RunDetailView, RunView, SessionDetailView,
  SessionMessageControlView } from "../api/types";
import { LocaleProvider } from "../lib/locale";
import { SessionWorkspace } from "./session-workspace";

const session = {
  id: "sess-1", workspace_id: "workspace-1", title: "Audit repository", route: "code",
  status: "active", created_at: "2026-08-12T00:00:00Z", updated_at: "2026-08-12T00:00:00Z",
} as const;

describe("SessionWorkspace", () => {
  beforeEach(() => {
    localStorage.clear();
    localStorage.setItem("prayu.locale.v1", "en-US");
    sessionStorage.clear();
  });

  it("does not present the stale bound Run as current after its refresh fails", async () => {
    const client = workspaceClient(run("running"), run("running"));
    client.get.mockImplementation((path: string) => path === "/sessions/sess-1"
      ? Promise.resolve({ session, run: run("running") } as SessionDetailView)
      : Promise.reject(new Error("run unavailable")));
    renderWorkspace(client);
    await waitFor(() => expect(screen.getByText("Bound execution record state").parentElement).toHaveTextContent("State temporarily unavailable"));
    expect(screen.getByText("Bound execution record state").parentElement).not.toHaveTextContent("Not ended");
    expect(screen.getByText("Not closed")).toBeVisible();
    expect(client.submitSessionMessage).not.toHaveBeenCalled();
    expect(client.executeRun).not.toHaveBeenCalled();
  });

  it("uses the fresh completed Run for a message-only client", async () => {
    const client = workspaceClient(run("paused"), run("completed"));

    renderWorkspace(client);

    await waitFor(() => expect(client.get).toHaveBeenCalledWith("/runs/run-1", {},
      expect.any(AbortSignal)));
    await waitFor(() => expect(screen.getByText("Bound execution record state").parentElement).toHaveTextContent("completed"));
    expect(screen.getByText("Bound execution record state")).toBeVisible();
    expect(screen.getByText("Bound Run")).toBeVisible();
    expect(screen.getByLabelText("Run-local Session message")).not.toBeVisible();
    await userEvent.setup().click(screen.getByText("Add input to this Session (advanced)"));
    expect(screen.getByLabelText("Run-local Session message")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Queue message" })).toBeDisabled();
    expect(client.controlRunLifecycle).not.toHaveBeenCalled();
    expect(client.executeRun).not.toHaveBeenCalled();
  });

  it("uses the fresh running Run when submitting a message", async () => {
    const client = workspaceClient(run("created"), run("running"));
    const user = userEvent.setup();

    renderWorkspace(client);

    const composer = await screen.findByLabelText("Run-local Session message");
    expect(screen.getByText("Not closed")).toBeVisible();
    await waitFor(() => expect(screen.getByText("Bound execution record state").parentElement).toHaveTextContent("Not ended"));
    expect(screen.getByText("Not closed").closest("[title]")).toHaveAttribute("title",
      "Context record state. Return to the conversation to continue chatting; open execution activity for current progress.");
    expect(composer).not.toBeVisible();
    await user.click(screen.getByText("Add input to this Session (advanced)"));
    await waitFor(() => expect(composer).toBeEnabled());
    await user.type(composer, "Inspect the latest changes");
    await user.click(screen.getByRole("button", { name: "Queue message" }));

    await waitFor(() => expect(client.submitSessionMessage).toHaveBeenCalledWith("sess-1", {
      version: "session_message_submission.v1", content: "Inspect the latest changes",
    }, expect.stringMatching(/^web-session-message-/), expect.any(AbortSignal)));
    expect(client.controlRunLifecycle).not.toHaveBeenCalled();
    expect(client.executeRun).not.toHaveBeenCalled();
  });

  it("keeps bound failed state visible when advanced input is collapsed without starting any work", async () => {
    const client = workspaceClient(run("paused"), run("failed"));
    const user = userEvent.setup();
    renderWorkspace(client);
    await screen.findByText("Add input to this Session (advanced)");
    const status = screen.getByText("Bound execution record state").parentElement!;
    await waitFor(() => expect(within(status).getByText("failed")).toBeVisible());
    const toggle = screen.getByText("Add input to this Session (advanced)");
    await user.click(toggle);
    expect(screen.getByLabelText("Run-local Session message")).toBeDisabled();
    expect(screen.getByLabelText("Run-local Session message")).toBeVisible();
    await user.click(toggle);
    expect(within(status).getByText("failed")).toBeVisible();
    expect(screen.getByLabelText("Run-local Session message")).not.toBeVisible();
    expect(client.submitSessionMessage).not.toHaveBeenCalled();
    expect(client.controlRunLifecycle).not.toHaveBeenCalled();
    expect(client.executeRun).not.toHaveBeenCalled();
  });

  it("preserves an unsent draft across collapse and keeps pending messages outside the input disclosure", async () => {
    const client = workspaceClient(run("running"), run("running"), true);
    const user = userEvent.setup();
    renderWorkspace(client);
    const input = await screen.findByLabelText("Run-local Session message");
    const toggle = screen.getByText("Add input to this Session (advanced)");
    await user.click(toggle);
    await waitFor(() => expect(input).toBeEnabled());
    await user.type(input, "Keep this draft for this Run only");
    await user.click(toggle);
    expect(input).not.toBeVisible();
    const queue = screen.getByRole("region", { name: "Queued Run-local Session messages" });
    expect(queue).toBeVisible();
    expect(within(queue).getByText("pending")).toBeVisible();
    expect(within(queue).getByRole("button", { name: "Cancel queued message 1" })).toBeVisible();
    await user.click(toggle);
    expect(input).toHaveValue("Keep this draft for this Run only");
    expect(client.submitSessionMessage).not.toHaveBeenCalled();
    expect(client.executeRun).not.toHaveBeenCalled();
  });

  it("reveals the original submission error after pending input is collapsed without retrying or clearing the draft", async () => {
    const client = workspaceClient(run("running"), run("running"));
    let reject!: (reason: Error) => void;
    client.submitSessionMessage.mockImplementation(() => new Promise((_resolve, rejectPromise) => { reject = rejectPromise; }));
    const user = userEvent.setup();
    renderWorkspace(client);
    const input = await screen.findByLabelText("Run-local Session message");
    const toggle = screen.getByText("Add input to this Session (advanced)");
    await user.click(toggle);
    await waitFor(() => expect(input).toBeEnabled());
    await user.type(input, "Preserve the exact original request");
    await user.click(screen.getByRole("button", { name: "Queue message" }));
    await waitFor(() => expect(client.submitSessionMessage).toHaveBeenCalledTimes(1));
    const originalKey = client.submitSessionMessage.mock.calls[0][2];
    await user.click(toggle);
    expect(input).not.toBeVisible();
    expect(await screen.findByText(/Input in progress/u)).toBeVisible();
    await act(async () => reject(new Error("fixture transport response lost")));
    await waitFor(() => expect(screen.getByText("fixture transport response lost")).toBeVisible());
    expect(screen.getAllByText("fixture transport response lost")).toHaveLength(1);
    expect(input).toBeVisible();
    expect(input).toHaveValue("Preserve the exact original request");
    expect(client.submitSessionMessage).toHaveBeenCalledTimes(1);
    expect(client.submitSessionMessage.mock.calls[0][2]).toBe(originalKey);
    expect(client.executeRun).not.toHaveBeenCalled();
    expect(screen.getByText("Bound execution record state").parentElement).toHaveTextContent("Not ended");
  });
});

function workspaceClient(staleRun: RunView, freshRun: RunView, hasQueuedMessage = false) {
  const submission = {
    version: "session_message_submission.v1", run_id: "run-1", session_id: "sess-1",
    steering: { id: "steer-1", sequence: 1, status: "pending", prepared: false,
      created_at: "2026-08-12T00:00:00Z" }, replayed: false, execution_started: false,
    model_called: false, tool_called: false, capability_grant: false,
  } as SessionMessageControlView;
  return {
    hasSessionMessages: true,
    hasSessionSteeringControl: hasQueuedMessage,
    hasPlanDelivery: false,
    hasRunLifecycle: false,
    hasRunExecution: false,
    hasEvidenceAttachment: false,
    get: vi.fn((path: string) => Promise.resolve(path === "/sessions/sess-1"
      ? { session, run: staleRun } as SessionDetailView
      : { run: freshRun, mode: { phase: "deliver" }, operator_steering: hasQueuedMessage ? {
        messages: [{ id: "existing-message", sequence: 1, status: "pending", prepared: false, created_at: session.created_at }],
      } : null } as RunDetailView)),
    getPage: vi.fn().mockResolvedValue({
      items: [], page: { limit: 100 }, requestID: "request-messages",
    }),
    submitSessionMessage: vi.fn().mockResolvedValue(submission),
    controlRunLifecycle: vi.fn(),
    executeRun: vi.fn(),
  } as unknown as APIClient & {
    get: ReturnType<typeof vi.fn>;
    submitSessionMessage: ReturnType<typeof vi.fn>;
    controlRunLifecycle: ReturnType<typeof vi.fn>;
    executeRun: ReturnType<typeof vi.fn>;
  };
}

function run(status: RunView["status"]): RunView {
  return {
    id: "run-1", mission_id: "mission-1", session_id: "sess-1", status,
    config: { model_route: "code", interactive: true }, budget: { max_turns: 10, max_tool_calls: 20 },
    created_at: "2026-08-12T00:00:00Z", updated_at: "2026-08-12T00:00:00Z",
  };
}

function renderWorkspace(client: APIClient) {
  return render(withProvider(<SessionWorkspace client={client} sessionID="sess-1" />));
}

function withProvider(node: ReactNode) {
  return <LocaleProvider><QueryClientProvider client={new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })}>{node}</QueryClientProvider></LocaleProvider>;
}
