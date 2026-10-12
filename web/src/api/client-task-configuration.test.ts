import { APIClient } from "./client";
import { creationBudgetMatches, normalizedTaskBudget, parseTaskConfiguration } from "./task-configuration";
import type { TaskConfigurationView } from "./types";

export const configurationFixture = (workspaceID = "ws-configuration"): TaskConfigurationView => ({
  version: "task_configuration.v1", workspace_id: workspaceID, profile: "code",
  requested_budget: { max_turns: 30, max_tool_calls: 40 }, budget: { max_turns: 12, max_tool_calls: 8 },
  sources: ["max_turns", "max_tokens", "max_tool_calls", "max_cost_usd", "timeout_seconds"].map((key) => ({ field: `budget.${key}` as TaskConfigurationView["sources"][number]["field"], source: key === "max_turns" || key === "max_tool_calls" ? "project" : "default" })),
  project_disposition: "applied", project: { protocol: "project_config.v1", read_only: false, allowed_profiles: [], excluded_path_count: 2, skill_suggestion_count: 1 },
  project_fingerprint: "a".repeat(64), fingerprint: "b".repeat(64), rejections: [], capability_grant: false,
});

afterEach(() => { vi.unstubAllGlobals(); });

function response(data: unknown, status = 200) { return new Response(JSON.stringify({ version: "api.v1", request_id: "req-configuration", data }), { status, headers: { "Content-Type": "application/json" } }); }

describe("task configuration contract", () => {
  it("selects exact project Skills through control and validates the existing selection receipt", async () => {
    const receipt = { run_id: "run-pinned", refs: ["code@1.0.0"], builtin_selection_id: "skill-selection-existing", replayed: false, capability_grant: false };
    const fetch = vi.fn().mockResolvedValue(response(receipt)); vi.stubGlobal("fetch", fetch);
    const choice = { refs: ["code@1.0.0"], confirmUntrustedContext: true };
    await expect(new APIClient("read", "/api/v1").selectProjectSkills("run-pinned", choice, "project-selection-0001")).rejects.toThrow("task control");
    expect(fetch).not.toHaveBeenCalled();
    const client = new APIClient("read", "/api/v1", "control");
    await client.selectProjectSkills("run-pinned", choice, "project-selection-0001");
    expect(fetch.mock.calls[0][0]).toBe("/api/v1/runs/run-pinned/project-skills/select");
    expect(fetch.mock.calls[0][1]).toMatchObject({ method: "POST", headers: { Authorization: "Bearer control", "Idempotency-Key": "project-selection-0001" } });
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({ refs: ["code@1.0.0"], confirm_untrusted_context: true });
    fetch.mockResolvedValue(response({ ...receipt, refs: ["code@2.0.0"] }));
    await expect(client.selectProjectSkills("run-pinned", choice, "project-selection-0001")).rejects.toThrow("does not match");
  });
  it("previews inert configuration using the read bearer without control capability", async () => {
    const fetch = vi.fn().mockResolvedValue(response(configurationFixture())); vi.stubGlobal("fetch", fetch);
    const client = new APIClient("read-token", "/api/v1");
    await expect(client.previewTaskConfiguration({ workspace_id: "ws-configuration", budget: { max_turns: 30, max_tool_calls: 40 } })).resolves.toEqual(configurationFixture());
    expect(fetch.mock.calls[0][0]).toBe("/api/v1/task-configuration/preview");
    expect(fetch.mock.calls[0][1]).toMatchObject({ method: "POST", headers: { Authorization: "Bearer read-token" } });
    expect(fetch.mock.calls[0][1].headers).not.toHaveProperty("Idempotency-Key");
  });

  it("reads a pinned Run endpoint and rejects a mismatched preview identity", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(response(configurationFixture()))));
    const client = new APIClient("read-token", "/api/v1");
    await expect(client.getRunTaskConfiguration("run-pinned")).resolves.toEqual(configurationFixture());
    await expect(client.previewTaskConfiguration({ workspace_id: "ws-other" })).rejects.toThrow("来源不匹配");
  });

  it("rejects unsafe, widening, partial rejection and malformed source projections", () => {
    const view = configurationFixture();
    for (const invalid of [
      { ...view, root_path: "D:/secret" }, { ...view, capability_grant: true },
      { ...view, budget: { max_turns: 31, max_tool_calls: 8 } },
      { ...view, project: { ...view.project, exclude_paths: ["secret"] } },
      { ...view, sources: [{ field: "credential", source: "project" }] },
      { ...view, project_disposition: "rejected", rejections: [{ field: "project_config", reason: "failed" }] },
    ]) expect(() => parseTaskConfiguration(invalid)).toThrow();
  });

  it("rejects explicit null response budget fields instead of treating them as zero", () => {
    const view = configurationFixture();
    for (const section of ["budget", "requested_budget"] as const) {
      for (const field of ["max_turns", "max_tokens", "max_tool_calls", "max_cost_usd", "timeout_seconds"]) {
        expect(() => parseTaskConfiguration({ ...view, [section]: { ...view[section], [field]: null } })).toThrow();
      }
    }
  });

  it("reads legacy snapshot ceilings without product defaults and rejects them as creation input", async () => {
    for (const toolLimit of [undefined, 0]) {
      const saved = { max_turns: 20_000, max_tokens: 2_000_000_000, max_cost_usd: 200_000, timeout_seconds: 1_000_000,
        ...(toolLimit === undefined ? {} : { max_tool_calls: toolLimit }) };
      const legacy = { ...configurationFixture(), requested_budget: saved, budget: saved,
        sources: configurationFixture().sources.map((source) => ({ ...source, source: "snapshot" as const })),
        project_disposition: "absent" as const, project: undefined, project_fingerprint: undefined };
      vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(response(legacy))));
      await expect(new APIClient("read-token", "/api/v1").getRunTaskConfiguration("run-legacy")).resolves.toEqual(legacy);
      expect(() => parseTaskConfiguration(legacy)).toThrow();
      expect(() => normalizedTaskBudget(saved)).toThrow();
      expect(() => parseTaskConfiguration({ ...legacy, budget: { ...saved, max_tokens: saved.max_tokens + 1 } }, undefined, undefined, "snapshot")).toThrow();
      expect(() => parseTaskConfiguration({ ...legacy, budget: { ...saved, max_tool_calls: null } }, undefined, undefined, "snapshot")).toThrow();
    }
  });

  it("validates explicit budgets before sending and canonicalizes finite micro-USD limits", async () => {
    const fetch = vi.fn(); vi.stubGlobal("fetch", fetch);
    const client = new APIClient("read-token", "/api/v1");
    for (const budget of [{ max_turns: 0 }, { max_tool_calls: 0 }, { max_cost_usd: NaN }, { max_tokens: 1.5 }, { timeout_seconds: 604801 }]) {
      await expect(client.previewTaskConfiguration({ workspace_id: "ws-configuration", budget })).rejects.toThrow();
    }
    await expect(client.previewTaskConfiguration({ workspace_id: "ws-configuration", budget: { max_tokens: null } as unknown as import("./types").TaskBudgetSettings })).rejects.toThrow();
    expect(fetch).not.toHaveBeenCalled();
    expect(normalizedTaskBudget({ max_cost_usd: 1.2345671 }).max_cost_usd).toBe(1.234567);
    expect(creationBudgetMatches({ max_turns: 12, max_tool_calls: 8 }, { requested_budget: { max_turns: 30, max_tool_calls: 40 }, project_config_fingerprint: "a".repeat(64) }, { max_turns: 30, max_tool_calls: 40 })).toBe(true);
    expect(creationBudgetMatches({ max_turns: 31, max_tool_calls: 8 }, { project_config_fingerprint: "a".repeat(64) }, { max_turns: 30, max_tool_calls: 40 })).toBe(false);
  });

  it("passes bounded budgets through Thread creation and accepts only a narrowing result", async () => {
    const requested = { max_turns: 30, max_tool_calls: 40 };
    const data = {
      mission: { id: "mission-created", goal: "Review config", workspace_id: "ws-configuration", profile: "code", scope: { workspace_id: "ws-configuration", network_mode: "disabled" } },
      run: { id: "run-created", mission_id: "mission-created", session_id: "sess-created", status: "created", config: { interactive: true, model_route: "code", requested_budget: requested, project_config_fingerprint: "a".repeat(64) }, budget: { max_turns: 12, max_tool_calls: 8 } },
      session: { id: "sess-created", workspace_id: "ws-configuration", title: "Review config", route: "code", status: "active" },
      mode: { protocol_version: "run_mode.v1", policy_version: "mode_policy.v1", revision: 1, profile: "code", surface: "code", phase: "deliver", scope: { workspace_id: "ws-configuration", network_mode: "disabled" }, capability_grant: false },
      thread: { id: "thread-created", protocol_version: "thread.v1", workspace_id: "ws-configuration", mission_id: "mission-created", title: "Review config", status: "active", active_run_id: "run-created", last_run_id: "run-created", version: 1, composer_state: "ready", created_at: "2026-10-09T00:00:00Z", updated_at: "2026-10-09T00:00:00Z" }, replayed: false,
    };
    const fetch = vi.fn().mockResolvedValue(response(data, 202)); vi.stubGlobal("fetch", fetch);
    const client = new APIClient("read", "/api/v1", "control");
    await expect(client.createThread({ version: "thread_creation.v1", goal: "Review config", workspace_id: "ws-configuration", budget: requested }, "create-config-operation-0001")).resolves.toEqual(data);
    expect(JSON.parse(fetch.mock.calls[0][1].body).budget).toEqual(requested);
  });
});
