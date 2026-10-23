import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../api/client";
import { ContextContinuityPanel } from "./context-continuity-panel";

describe("ContextContinuityPanel", () => {
  it("explains pinned sources, manages explicit memory, and exposes non-authorizing branches", async () => {
    const user = userEvent.setup();
    const instructionState = projectInstructionState();
    const get = vi.fn().mockImplementation((path: string) => {
      if (path.includes("project-instructions")) return Promise.resolve(instructionState);
      if (path === "/memories") return Promise.resolve([memoryView()]);
      if (path === "/memory-candidates") return Promise.resolve([]);
      if (path.includes("/tree")) return Promise.resolve(sessionTree());
      return Promise.reject(new Error(`unexpected read ${path}`));
    });
    const postControl = vi.fn().mockImplementation((path: string) => {
      if (path.includes("project-instructions/refresh")) {
        return Promise.resolve({ ...instructionState, stale: false, refresh_confirmed: true });
      }
      if (path.includes("continuity-checkpoints")) {
        return Promise.resolve({ id: "continuity-new" });
      }
      return Promise.reject(new Error(`unexpected control ${path}`));
    });
    const patchControl = vi.fn().mockResolvedValue({ ...memoryView(), status: "disabled", version: 2 });
    renderPanel({ hasControl: true, get, postControl, patchControl,
      deleteControl: vi.fn() } as unknown as APIClient);

    expect(await screen.findByText("Carry context forward and review permissions")).toBeInTheDocument();
    expect(await screen.findByText("Repository workflow")).toBeInTheDocument();
    await user.click(screen.getByText("Repository workflow"));
    expect(screen.getByText("root applies to src/main.go")).toBeInTheDocument();
    expect(screen.getByText("Test convention")).toBeInTheDocument();
    expect(screen.getByText(/References: docs\/testing\.md/)).toBeInTheDocument();
    expect(screen.getByText("Branch comparison")).toBeInTheDocument();
    expect(screen.getByText(/main@111111111111/)).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Fork" })).toHaveLength(2);

    await user.click(screen.getByRole("button", { name: "Confirm refresh" }));
    await waitFor(() => expect(postControl).toHaveBeenCalledWith(
      "/runs/run-1/project-instructions/refresh",
      expect.objectContaining({ expected_fingerprint: "a".repeat(64),
        expected_live_fingerprint: "b".repeat(64), confirm: true }),
      expect.stringMatching(/^web-instruction-refresh-/),
    ));

    await user.click(screen.getByRole("button", { name: "Disable" }));
    await waitFor(() => expect(patchControl).toHaveBeenCalledWith("/memories/memory-1",
      expect.objectContaining({ expected_version: 1, status: "disabled" })));

    await user.click(screen.getByRole("button", { name: "Create checkpoint" }));
    await waitFor(() => expect(postControl).toHaveBeenCalledWith(
      "/runs/run-1/continuity-checkpoints",
      { title: "", summary: "" }, expect.stringMatching(/^web-continuity-checkpoint-/),
    ));
  });

  it("retains memory through the end of the selected local calendar day", async () => {
    const user = userEvent.setup();
    const get = vi.fn().mockImplementation((path: string) => {
      if (path.includes("project-instructions")) return Promise.resolve(projectInstructionState());
      if (path === "/memories") return Promise.resolve([memoryView()]);
      if (path === "/memory-candidates") return Promise.resolve([]);
      if (path.includes("/tree")) return Promise.resolve(sessionTree());
      return Promise.reject(new Error(`unexpected read ${path}`));
    });
    const postControl = vi.fn().mockResolvedValue(memoryView());
    renderPanel({ hasControl: true, get, postControl, patchControl: vi.fn(),
      deleteControl: vi.fn() } as unknown as APIClient);

    await screen.findByText("Test convention");
    await user.type(screen.getByLabelText("Memory title"), "Release checklist");
    await user.type(screen.getByLabelText("Memory content"), "Check the release notes.");
    fireEvent.change(screen.getByLabelText("Retain until"), { target: { value: "2030-02-28" } });
    await user.click(screen.getByRole("button", { name: "Create memory" }));

    await waitFor(() => expect(postControl).toHaveBeenCalledWith("/memories",
      expect.objectContaining({ title: "Release checklist", content: "Check the release notes.",
        retention_until: new Date(2030, 1, 28, 23, 59, 59, 999).toISOString() }),
      expect.stringMatching(/^web-memory-create-/)));
  });

  it("opens the memory editor on the retained local calendar day", async () => {
    const user = userEvent.setup();
    // Local midnight makes the UTC date differ from the local day in non-UTC time zones.
    const retentionUntil = new Date(2030, 2, 15, 0, 30).toISOString();
    const get = vi.fn().mockImplementation((path: string) => {
      if (path.includes("project-instructions")) return Promise.resolve(projectInstructionState());
      if (path === "/memories") return Promise.resolve([{ ...memoryView(), retention_until: retentionUntil }]);
      if (path === "/memory-candidates") return Promise.resolve([]);
      if (path.includes("/tree")) return Promise.resolve(sessionTree());
      return Promise.reject(new Error(`unexpected read ${path}`));
    });
    renderPanel({ hasControl: true, get, postControl: vi.fn(), patchControl: vi.fn(),
      deleteControl: vi.fn() } as unknown as APIClient);

    await screen.findByText("Test convention");
    await user.click(screen.getByRole("button", { name: "Edit" }));
    const article = screen.getByText("Test convention").closest("article");
    expect(within(article as HTMLElement).getByLabelText("Retain until")).toHaveValue("2030-03-15");
  });

  it("keeps mutation controls disabled on a read-only connection", async () => {
    renderPanel({ hasControl: false,
      get: vi.fn().mockImplementation((path: string) => path.includes("project-instructions")
        ? Promise.resolve({ ...projectInstructionState(), stale: false })
        : path === "/memories" || path === "/memory-candidates" ? Promise.resolve([]) : Promise.resolve(sessionTree())),
    } as unknown as APIClient);

    expect(await screen.findByText(/This connection is read-only/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create memory" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Create checkpoint" })).toBeDisabled();
    await screen.findByText("Repository workflow");
    expect(screen.getAllByRole("button", { name: "Fork" })[0]).toBeDisabled();
  });
});

function renderPanel(client: APIClient) {
  const queryClient = new QueryClient({ defaultOptions: {
    queries: { retry: false }, mutations: { retry: false },
  } });
  return render(<QueryClientProvider client={queryClient}>
    <ContextContinuityPanel client={client} runID="run-1" sessionID="session-1"
      workspaceID="workspace-1" />
  </QueryClientProvider>);
}

function projectInstructionState() {
  const source = {
    ordinal: 1, path: "AGENTS.md", kind: "agents", scope: "", depth: 0,
    precedence: 100, content: "Use deterministic tests.", content_sha256: "c".repeat(64),
    loaded_at: "2026-08-19T00:00:00Z", trust: "project_workflow_untrusted",
    applicable_to: ["src/main.go"], why_effective: "root applies to src/main.go",
    redacted: false, authority: { workflow_guidance: true, formatting_guidance: true,
      validation_guidance: true, tool_grant: false, network_grant: false,
      secret_access: false, debug_grant: false, plugin_grant: false,
      hook_execution: false, policy_override: false },
  };
  const snapshot = { protocol_version: "project_instruction_snapshot.v1",
    target_path: "src/main.go", sources: [source], ignored: [], conflicts: [],
    fingerprint: "a".repeat(64), loaded_at: "2026-08-19T00:00:00Z",
    limits: { max_files: 64, max_file_bytes: 65536, max_total_bytes: 262144, max_depth: 32 } };
  return { run_id: "run-1", workspace_id: "workspace-1", pinned_present: true,
    pinned: { id: "snapshot-1", run_id: "run-1", revision: 1, snapshot,
      diff: { from_fingerprint: "", to_fingerprint: "a".repeat(64), added: [],
        removed: [], changed: [], order_changed: false, requires_confirmation: false },
      confirmed_by: "operator", created_at: "2026-08-19T00:00:00Z" },
    live: { ...snapshot, fingerprint: "b".repeat(64) },
    diff: { from_fingerprint: "a".repeat(64), to_fingerprint: "b".repeat(64),
      added: [], removed: [], changed: ["AGENTS.md"], order_changed: false,
      requires_confirmation: true }, history: [], stale: true,
    refresh_confirmed: false, capability_grant: false };
}

function memoryView() {
  return { id: "memory-1", protocol_version: "context_memory.v1", scope: "project",
    scope_id: "workspace-1", title: "Test convention", content: "Run deterministic tests",
    content_sha256: "d".repeat(64), status: "active", source_kind: "operator_explicit",
    references: ["docs/testing.md"], redacted: false, created_by: "operator", updated_by: "operator",
    version: 1, created_at: "2026-08-19T00:00:00Z", updated_at: "2026-08-19T00:00:00Z" };
}

function sessionTree() {
  return { protocol_version: "session_tree.v1", session_id: "session-1",
    workspace_id: "workspace-1", capability_grant: false,
    generated_at: "2026-08-19T00:00:00Z", nodes: [
      { id: "continuity-root", kind: "root", run_id: "run-1", session_id: "session-1",
        title: "Run created", status: "current", warnings: [], derived: false,
        fingerprint: "e".repeat(64), project_config_fingerprint: "a".repeat(64),
        project_instructions_fingerprint: "b".repeat(64), git_branch: "main",
        git_head: "1".repeat(40), created_at: "2026-08-19T00:00:00Z" },
      { id: "continuity-checkpoint", parent_id: "continuity-root", kind: "checkpoint",
        run_id: "run-1", session_id: "session-1", title: "Repository workflow",
        summary: "Ready to branch", status: "valid", warnings: [], derived: false,
        fingerprint: "f".repeat(64), project_config_fingerprint: "a".repeat(64),
        project_instructions_fingerprint: "c".repeat(64), git_branch: "feature/context",
        git_head: "2".repeat(40), created_at: "2026-08-19T00:01:00Z" },
    ] };
}
