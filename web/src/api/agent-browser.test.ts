import { parseAgentBrowserStatus } from "./agent-browser";

const closed = { version: "agent_browser_status.v1", run_id: "run-browser", generation: 1,
  state: "closed", available: true, can_start: false, headless: true, document_epoch: 2,
  updated_at: "2026-10-20T00:00:00Z", cleanup_pending: false, tree_reaped: false, profile_removed: true,
  process_scope: "darwin_group_and_scoped_children.v1", process_scope_verified: true };

it("reads Darwin cleanup without inventing a Job tree receipt", () => {
  expect(parseAgentBrowserStatus(closed, "run-browser")).toMatchObject({ tree_reaped: false, process_scope_verified: true });
  expect(parseAgentBrowserStatus({ ...closed, state: "cleanup_pending", cleanup_pending: true,
    profile_removed: false, process_scope_verified: false }, "run-browser")).toMatchObject({ cleanup_pending: true });
  for (const change of [{ tree_reaped: true }, { process_scope: "unbounded-tree" }, { process_scope_verified: "yes" }, { process_scope: undefined }]) {
    expect(() => parseAgentBrowserStatus({ ...closed, ...change }, "run-browser")).toThrow();
  }
});
