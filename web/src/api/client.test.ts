import { readFileSync } from "node:fs";
import { APIClient, clientCapabilitiesFromRuntime } from "./client";
import type { ProviderDefinitionView, RunEventStreamView, RunLifecycleControlView,
  ScheduledJobCreateRequestView, UIEvidenceArtifactMetadata } from "./types";
import { standardCodeDeliveryFixture } from "../test/standard-code-delivery";
import pausedPlanReadiness from "../test/fixtures/readiness-paused-plan.json";
import currentPausedPlanReadiness from "../test/fixtures/readiness-paused-plan-current.json";

const healthEnvelope = {
  version: "api.v1",
  request_id: "req-health",
  data: { status: "ok", api_version: "api.v1", app_version: "test", schema_version: 37 },
};

function commandRuntimeAdapterData() {
  return {
    kind: "host_unsandboxed", backend: "run_owned_command_runtime",
    backend_identity: "host-process.v1", isolation_grade: "host_unsandboxed",
    network_policy: "host_available", credential_policy: "host_available", ready: true,
  } as const;
}

function runtimeCapabilitiesData(overrides: Record<string, unknown> = {}) {
  return {
    protocol_version: "runtime_capabilities.v1",
    workspace_import_enabled: false,
    agent_code_tools_enabled: true,
    code_intel_enabled: true,
    execution_permission_control_enabled: true, operator_approval_enabled: true,
    workspace_sandbox_enabled: false,
    danger_full_access_enabled: true,
    command_runtime_enabled: true,
    command_runtime_protocol_available: true,
    command_runtime_adapter_installed: true,
    command_runtime_adapter_ready: true,
    command_runtime_adapters: [commandRuntimeAdapterData()],
    browser_cdp_permission_control_enabled: true, full_cdp_debug_enabled: true,
    full_cdp_session_control_enabled: true,
    run_control_enabled: true, run_creation_enabled: true,
    standard_code_preset_enabled: true,
    session_message_enabled: true, thread_control_enabled: true,
    session_steering_control_enabled: true,
    run_lifecycle_enabled: true, run_execution_enabled: true,
    plan_delivery_control_enabled: true, approval_control_enabled: true,


    model_control_enabled: true, provider_credential_enabled: true,
    file_edit_review_enabled: true, file_edit_proposal_enabled: true,
    file_edit_apply_enabled: true, run_wake_control_enabled: true,
    run_wake_execution_enabled: true, run_wake_worker_enabled: true,
    scheduled_job_control_enabled: true, scheduled_job_worker_enabled: true,
    skill_installation_enabled: true, evidence_attachment_enabled: true,
    verification_evidence_enabled: true,
    ui_evidence_control_enabled: true,
    embedded_analyzer_execution_enabled: true,
	workspace_checkpoint_control_enabled: true,
    git_advanced_control_enabled: true,
    github_review_control_enabled: true,
    batch_delivery_control_enabled: true,
    batch_delivery_host_validation_enabled: true,
    process_execution_enabled: true, shell_execution_enabled: true,
    docker_execution_enabled: false,
    wake_worker: { protocol_version: "run_wake_worker_health.v1", enabled: true,
      state: "running", active: false, poll_interval_ms: 2000, concurrency: 1,
      max_steps: 1, runtime_enable_supported: false, persistent_service: false },
    scheduled_job_worker: { protocol_version: "scheduled-job-worker-health.v1", enabled: true,
      state: "running", active: false, poll_interval_ms: 2000, concurrency: 1,
      runtime_enable_supported: false, persistent_service: false, authority_escalation: false },
    ...overrides,
  };
}

function capabilityReadinessEnvelope(): typeof currentPausedPlanReadiness {
  // Go checks the committed specimen against its real HTTP response. A focused
  // cross-language run can also parse the exact envelope exported by that test.
  const path = process.env.TRAVERSE_TEST_READINESS_OUTPUT;
  return path ? JSON.parse(readFileSync(path, "utf8")) : currentPausedPlanReadiness;
}

function capabilityReadinessData() {
  return { ...structuredClone(capabilityReadinessEnvelope().data), run_id: "run-1" };
}

function standardCodeTrustData(overrides: Record<string, unknown> = {}) {
  return {
    protocol_version: "standard_code_preset.v1", status: "blocked",
    workspace_id: "workspace-1", action: "configure", backend_intent: "auto",
    selected_backend: "local", selection_reason: "auto_local_ready",
    local_readiness: { backend: "local", available: true, blocked_by: [], remediation: [] },
    docker_readiness: { backend: "docker", available: false,
      blocked_by: ["docker_unavailable"], remediation: ["install_or_start_docker"] },
    blocked_by: ["workspace_untrusted"], next_steps: ["confirm_workspace_trust"],
    trust_required: true, trust_digest: "a".repeat(64), drydock_ready: false,
    network: "disabled", credentials: "none", replayed: false, capability_grant: false,
    ...overrides,
  };
}

function scheduledJobData(overrides: Record<string, unknown> = {}) {
  return {
    id: "scheduled-job-1", owner_run_id: "run-1", owner_root_agent_id: "agent-root",
    status: "active", revision: 1, active_lease_generation: 0, rounds_completed: 0,
    consecutive_unchanged: 0, model_calls: 0, last_event_sequence: 0,
    next_wake_at: "2026-08-20T11:00:00Z", created_by: "http_control",
    created_at: "2026-08-20T10:00:00Z", updated_at: "2026-08-20T10:00:00Z",
    spec: { version: "scheduled-job.v1", target_run_id: "run-1", execution_mode: "read_only",
      schedule: { kind: "once", timezone: "UTC", anchor_at: "2026-08-20T11:00:00Z",
        misfire_policy: "run_once" }, deadline_at: "2026-08-20T12:00:00Z",
      stop_on_target_terminal: true, max_rounds: 1, max_model_calls: 0,
      max_elapsed_seconds: 3600, retry: { max_attempts: 3, initial_backoff_seconds: 5,
        max_backoff_seconds: 60 }, notification: "on_change" },
    ...overrides,
  };
}

const runCreationData = {
  mission: {
    id: "mission-created", goal: "Create parser", workspace_id: "workspace-1", profile: "code",
    scope: { workspace_id: "workspace-1", network_mode: "disabled" },
  },
  run: {
    id: "run-created", mission_id: "mission-created", session_id: "sess-created",
    status: "created",
    config: { interactive: true, model_route: "code" }, budget: { max_turns: 100, max_tool_calls: 100 },
  },
  session: { id: "sess-created", workspace_id: "workspace-1", title: "Create parser", route: "code", status: "active" },
  mode: {
    protocol_version: "run_mode.v1", policy_version: "mode_policy.v1", revision: 1,
    profile: "code", surface: "code", phase: "deliver",
    scope: { workspace_id: "workspace-1", network_mode: "disabled" }, capability_grant: false,
    requested_by: "operator", reason: "initial Run mode",
    created_at: "2026-08-24T00:00:00Z",
  },
  replayed: false,
};

const threadData = {
  id: "thread-created", protocol_version: "thread.v1", workspace_id: "workspace-1",
  mission_id: "mission-created", title: "Create parser", status: "active",
  active_run_id: "run-created", last_run_id: "run-created", version: 2,
  composer_state: "ready", created_at: "2026-08-24T00:00:00Z",
  updated_at: "2026-08-24T00:00:00Z",
};

const threadCreationData = { ...runCreationData, thread: threadData };

const networkRunCreationData = {
  ...runCreationData,
  mission: { ...runCreationData.mission, scope: { workspace_id: "workspace-1",
    network_mode: "allowlist", allowed_targets: ["api.example.com", "docs.example.com"] } },
  mode: { ...runCreationData.mode, scope: { workspace_id: "workspace-1",
    network_mode: "allowlist", allowed_targets: ["api.example.com", "docs.example.com"] } },
};
const networkThreadCreationData = { ...networkRunCreationData, thread: threadData };

const threadMessageData = {
  version: "thread_message_submission.v1", thread: threadData,
  run_id: "run-created", session_id: "sess-created", successor_created: false,
  steering: {
    id: "thread-steer-1", sequence: 1, status: "pending", prepared: false,
    created_at: "2026-08-24T00:01:00Z",
  },
  replayed: false, execution_started: false, model_called: false,
  tool_called: false, capability_grant: false,
};

const sessionMessageData = {
  version: "session_message_submission.v1",
  run_id: "run-1",
  session_id: "sess-1",
  steering: {
    id: "steer-1", sequence: 1, status: "pending", prepared: false,
    created_at: "2026-07-18T00:00:00Z",
  },
  replayed: false,
  execution_started: false,
  model_called: false,
  tool_called: false,
  capability_grant: false,
};

const sessionSteeringCancellationData = {
  version: "session_steering_cancellation.v1",
  run_id: "run-1", session_id: "sess-1",
  steering: {
    id: "steer-1", sequence: 1, status: "cancelled", prepared: false,
    created_at: "2026-07-18T00:00:00Z", cancelled_at: "2026-07-18T00:01:00Z",
  },
  cancellation_id: "cancel-1", cancellation_kind: "operator", replayed: false,
  execution_started: false, model_called: false, tool_called: false, capability_grant: false,
};

const runLifecycleData = {
  version: "run_lifecycle_control.v1",
  run: {
    id: "run-1", mission_id: "mission-1", session_id: "sess-1", status: "running",
    config: { model_route: "code", interactive: true }, budget: { max_turns: 4 },
    created_at: "2026-07-18T00:00:00Z", updated_at: "2026-07-18T00:01:00Z",
  },
  action: "start", expected_status: "created", applied_status: "running",
  event_sequence_start: 5, event_sequence_end: 6, replayed: false,
  execution_started: false, model_called: false, tool_called: false, capability_grant: false,
};

const runExecutionData = {
  version: "run_execution_handoff.v1", operation_id: "run-handoff-1",
  run_id: "run-1", session_id: "sess-1", max_steps: 2, selected_count: 2,
  status: "completed", run_status: "running", stop_reason: "selection_drained",
  steps_completed: 1, pending_count: 0, prepared_count: 0, committed_count: 1,
  cancelled_count: 1, completion_event_sequence: 12, replayed: false,
  execution_started: true, model_called: true, tool_called: false, capability_grant: false,
};

const modelCancellationData = {
  id: "cancel-model-1", run_id: "run-1", attempt_id: "attempt-1", model_attempt: 1,
  status: "pending", requested_at: "2026-07-18T00:00:00Z", replayed: false,
};

const publicModelStreamData = {
  version: "model_public_stream.v3",
  call: {
    run_id: "run-1", session_id: "sess-1", attempt_id: "attempt-1",
    model_attempt: 1, transport_attempt: 1, max_attempts: 3, protocol_repair: 0,
    tool_round: 0, provider: "deepseek", model: "deepseek-chat",
    started_at: "2026-08-08T00:00:00Z", stream_chunks: 2, stream_bytes: 64,
    cancel_requested: false,
  },
  revision: 3, response_id: "response-1", event_sequence: 4, items: [{
    response_id: "response-1", id: "item-tool-1", type: "tool_call",
    status: "in_progress", call_id: "call-tool-1", tool_name: "read_file",
    argument_bytes: 24, provisional: true, durable: false,
  }],
  content_kind: "root_message", text: "A safe provisional answer", message_complete: false,
  provisional: true, updated_at: "2026-08-08T00:00:01Z",
};

const specialistModelCancellationData = {
  id: "cancel-agent-1", run_id: "run-1", agent_id: "agent-1", attempt_id: "attempt-2",
  model_attempt: 2, status: "observed", requested_at: "2026-07-18T00:00:00Z", replayed: false,
};

function uiEvidencePassedAttemptData() {
  return {
    protocol_version: "ui-evidence-attempt.v1",
    manifest: {
      protocol_version: "ui-evidence.v1", attempt_id: "ui-attempt-1", run_id: "run-1",
      mission_id: "mission-1", session_id: "session-1", workspace_id: "workspace-1",
      source: { repository_kind: "git", commit: "a".repeat(40), branch: "codex/test",
        dirty: false, dirty_digest: "b".repeat(64), root_fingerprint: "c".repeat(64),
        index_sha256: "d".repeat(64), manifest_sha256: "e".repeat(64) },
      start: { protocol_version: "command-runtime.v2", profile: "powershell",
        executable_name: "powershell.exe", executable_path_sha256: "1".repeat(64),
        executable_sha256: "2".repeat(64), canonical_argv: ["powershell.exe", "-Command",
          "npm run dev"], working_directory: "web", environment_names: [],
        environment_sha256: "3".repeat(64), timeout_milliseconds: 60_000,
        network: "disabled", credentials: "none", purpose: "UI evidence",
        fingerprint: "4".repeat(64) },
      readiness: { url: "http://127.0.0.1:4173/", method: "GET", expected_status: [200],
        timeout_milliseconds: 60_000, interval_milliseconds: 250 },
      browser: { product: "edge", version: "140.0.0.0",
        executable_sha256: "5".repeat(64),
        driver_protocol: "restricted-cdp-ui-evidence.v1", headless: true,
        temporary_profile: true },
      url: "http://127.0.0.1:4173/", route: "/", environment: {
        viewport: { width: 1440, height: 900, dpr: 1 }, locale: "en-US",
        theme: "light", reduced_motion: false,
      },
      fixture: { name: "fixture", seed: "seed", page_state: "{}",
        data_sha256: "6".repeat(64), deterministic: true, synthetic: true },
      steps: [{ id: "navigate", kind: "navigate", capture_after: true }],
      capture: { screenshot: true, dom: true, accessibility: true, console: true,
        network: true, performance: true, video: false, mask_selectors: [] },
      failure_policy: { fail_on_console_error: true, fail_on_page_error: true,
        fail_on_request_error: true, fail_on_http_status: true },
      authority: { process_start: false, network_access: false, credential_access: false,
        personal_profile: false, request_mutation: false, verification_pass: false },
      created_at: "2026-08-20T00:00:00Z", fingerprint: "7".repeat(64),
    },
    operation_digest: "8".repeat(64), request_fingerprint: "7".repeat(64), status: "passed",
    failure_stage: "none", diagnostics: { console_warnings: 0, console_errors: 0,
      page_errors: 0, failed_requests: 0, http_failures: 0, allowed_requests: 1,
      blocked_requests: 0 }, cleanup: { browser_tree_reaped: true,
      application_tree_reaped: true, profile_removed: true, network_released: true,
      port_released: true }, artifact_count: 6, artifact_bytes: 1_024, version: 3,
    created_at: "2026-08-20T00:00:00Z", started_at: "2026-08-20T00:00:01Z",
    completed_at: "2026-08-20T00:00:02Z", updated_at: "2026-08-20T00:00:02Z",
  };
}

function uiEvidencePassedBundleData() {
  const attempt = uiEvidencePassedAttemptData();
  const kinds = ["screenshot", "dom", "accessibility", "console", "network",
    "performance"] as const;
  const sizes = [500, 100, 100, 100, 100, 124];
  return {
    attempt,
    artifacts: kinds.map((kind, index) => ({
      protocol_version: "ui-evidence-artifact.v1", id: `ui-artifact-${kind}`,
      attempt_id: attempt.manifest.attempt_id, run_id: attempt.manifest.run_id,
      step_id: "navigate", kind, mime: kind === "screenshot" ? "image/png" : "application/json",
      sha256: String(index + 1).repeat(64), bytes: sizes[index],
      ...(kind === "screenshot" ? { width: 1440, height: 900 } : {}),
      viewport: { width: 1440, height: 900, dpr: 1 },
      source_commit: attempt.manifest.source.commit, retention_policy: "run_history",
      redacted: true, untrusted: true, created_at: "2026-08-20T00:00:01Z",
      fingerprint: String(index + 2).repeat(64),
    })),
    steps: [{ protocol_version: "ui-evidence-step.v1",
      attempt_id: attempt.manifest.attempt_id, step_id: "navigate", sequence: 1,
      kind: "navigate", status: "passed", failure_stage: "none",
      started_at: "2026-08-20T00:00:01Z", completed_at: "2026-08-20T00:00:02Z",
      fingerprint: "f".repeat(64) }],
  };
}

describe("APIClient", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("imports a confirmed directory with the independent control bearer and no operation key", async () => {
    const data = { protocol_version: "workspace_import.v1",
      workspace: { id: "ws-import-1", name: "project", created_at: "2026-09-08T00:00:00Z" },
      directory_content_modified: false, agent_authority_granted: false };
    const fetchMock = vi.fn().mockImplementation(async () => new Response(JSON.stringify({
      version: "api.v1", request_id: "req-import", data,
    }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: false, workspaceImportEnabled: true,
    });
    const signal = new AbortController().signal;
    const directory = "D:\\用户 项目\\project";
    expect(client.hasControl).toBe(false);
    expect(await client.importWorkspace(directory, signal)).toEqual(data);
    expect(await client.importWorkspace(directory, signal)).toEqual(data);
    const [url, request] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/workspaces/import");
    expect(request).toMatchObject({ method: "POST", signal, cache: "no-store",
      credentials: "omit", referrerPolicy: "no-referrer" });
    expect(request.headers).toEqual({ Accept: "application/json", Authorization: "Bearer control-secret",
      "Content-Type": "application/json" });
    expect(JSON.parse(request.body as string)).toEqual({ version: "workspace_import.v1",
      directory_path: directory, confirmed: true });
  });

  it("keeps workspace import disabled without both advertised capability and control bearer", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    for (const [token, capability] of [["", true], ["control-secret", false],
      ["control-secret", undefined]] as const) {
      const client = new APIClient("read-secret", "/api/v1", token,
        { workspaceImportEnabled: capability });
      expect(client.hasWorkspaceImport).toBe(false);
      await expect(client.importWorkspace("D:\\private-project"))
        .rejects.toMatchObject({ code: "PERMISSION_DENIED" });
    }
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("rejects workspace import authority drift, path disclosure and malformed metadata", async () => {
    const valid = { protocol_version: "workspace_import.v1",
      workspace: { id: "ws-import-1", name: "project", created_at: "2026-09-08T00:00:00Z" },
      directory_content_modified: false, agent_authority_granted: false };
    const client = new APIClient("read-secret", "/api/v1", "control-secret",
      { workspaceImportEnabled: true });
    const bad = [{ ...valid, agent_authority_granted: true },
      { ...valid, directory_content_modified: true }, { ...valid, protocol_version: "invalid-version" },
      { ...valid, directory_path: "D:\\private-project" },
      ...[{ root_path: "D:\\private-project" }, { id: "D:\\private-project" },
        { created_at: "invalid" }]
        .map((drift) => ({ ...valid, workspace: { ...valid.workspace, ...drift } })),
      { protocol_version: valid.protocol_version, workspace: valid.workspace }];
    for (const data of bad) {
      vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-import", data,
      }), { status: 200 })));
      await expect(client.importWorkspace("D:\\private-project"))
        .rejects.toMatchObject({ code: "INVALID_RESPONSE", status: 502 });
    }
  });

  it("removes host paths from import server and transport failures", async () => {
    const directory = "D:\\private-project";
    const client = new APIClient("read-secret", "/api/v1", "control-secret",
      { workspaceImportEnabled: true });
    for (const fetchMock of [vi.fn().mockRejectedValue(new Error(directory)),
      vi.fn().mockResolvedValue(new Response(JSON.stringify({ version: "api.v1", request_id: directory,
        error: { code: "INVALID_ARGUMENT", message: `invalid directory: ${directory}` },
      }), { status: 400 }))]) {
      vi.stubGlobal("fetch", fetchMock);
      const failure = await client.importWorkspace(directory).catch((error: unknown) => error);
      expect(failure).toBeInstanceOf(Error);
      expect(String(failure)).not.toContain(directory);
      expect(JSON.stringify(failure)).not.toContain(directory);
    }
  });

  it("rejects empty, control-bearing and oversized directory input before sending", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret",
      { workspaceImportEnabled: true });
    for (const directory of ["", "  ", "D:\\project\nother", "D:\\project\0", "项".repeat(1_400)]) {
      await expect(client.importWorkspace(directory)).rejects.toMatchObject({ code: "INVALID_ARGUMENT" });
    }
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("treats a missing import capability as disabled and rejects a malformed advertised flag", async () => {
    for (const advertised of [undefined, false, true, "true"]) {
      const data: Record<string, unknown> = runtimeCapabilitiesData();
      if (advertised === undefined) delete data.workspace_import_enabled;
      else data.workspace_import_enabled = advertised;
      vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-capabilities", data,
      }), { status: 200 })));
      const request = new APIClient("read-secret").runtimeCapabilities();
      if (typeof advertised === "string") {
        await expect(request).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
      } else {
        const view = await request;
        expect(clientCapabilitiesFromRuntime(view).workspaceImportEnabled).toBe(advertised === true);
      }
    }
  });

  it("reads interruption history while rejecting execution identity and authority drift", async () => {
    const valid = { version: "thread_execution.v1", thread_id: "thread-1", state: "idle",
      queued_messages: 0, capability_grant: false, last_turn_interrupted: true };
    const respond = (data: unknown) => vi.stubGlobal("fetch", vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ version: "api.v1", request_id: "req-execution", data }), {
        status: 200, headers: { "Content-Type": "application/json" },
      })));
    const client = new APIClient("read-secret");
    respond(valid);
    expect(await client.threadExecution("thread-1")).toEqual(valid);
    for (const drift of [{ thread_id: "thread-2" }, { capability_grant: true },
      { state: "running" }, { execution_id: "stale-execution" }, { last_turn_interrupted: "yes" }]) {
      respond({ ...valid, ...drift });
      await expect(client.threadExecution("thread-1")).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    }
  });

  it("requires an explicit boolean execution read capability without requiring a control token", async () => {
    for (const advertised of [undefined, false, true, "true"]) {
      const data: Record<string, unknown> = runtimeCapabilitiesData();
      if (advertised !== undefined) data.thread_execution_read_enabled = advertised;
      vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-capabilities", data,
      }), { status: 200 })));
      const readClient = new APIClient("read-secret");
      const request = readClient.runtimeCapabilities();
      if (typeof advertised === "string") {
        await expect(request).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
      } else {
        const runtime = await request;
        const connected = new APIClient("read-secret", "/api/v1", "", clientCapabilitiesFromRuntime(runtime));
        expect(connected.hasThreadExecutionRead).toBe(advertised === true);
        expect(connected.hasRunExecution).toBe(false);
        expect(connected.hasThreadControl).toBe(false);
      }
    }
  });

  it.each([
    ["", {}, "missing_control_credential"],
    ["   ", { uiEvidenceControlEnabled: true }, "missing_control_credential"],
    ["control-secret", {}, "ui_evidence_disabled"],
    ["control-secret", { uiEvidenceControlEnabled: false, runExecutionEnabled: false }, "ui_evidence_disabled"],
    ["control-secret", { uiEvidenceControlEnabled: true, runExecutionEnabled: false }, "run_execution_disabled"],
    ["control-secret", { uiEvidenceControlEnabled: true, runExecutionEnabled: true,
      browserCDPPermissionControlEnabled: false }, "browser_cdp_control_disabled"],
    ["control-secret", { uiEvidenceControlEnabled: true, runExecutionEnabled: true,
      browserCDPPermissionControlEnabled: true }, null],
    ["control-secret", { uiEvidenceControlEnabled: true, runExecutionEnabled: true,
      browserCDPPermissionControlEnabled: true, runControlEnabled: false }, null],
  ] as const)("projects the UI evidence availability reason for control %j and capabilities %j", (control, capabilities, reason) => {
    const client = new APIClient("read-secret", "/api/v1", control, capabilities);
    expect(client.uiEvidenceUnavailableReason).toBe(reason);
    expect(client.hasUIEvidence).toBe(reason === null);
    if ("runControlEnabled" in capabilities && capabilities.runControlEnabled === false) {
      expect(client.hasControl).toBe(false);
    }
  });

  it("keeps the bearer out of the URL and sends it only in Authorization", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(healthEnvelope), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await new APIClient("read-secret").health();

    expect(result.schema_version).toBe(37);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/health");
    expect(url).not.toContain("read-secret");
    expect(init.headers).toMatchObject({ Authorization: "Bearer read-secret" });
    expect(init.credentials).toBe("omit");
  });

  it.each([
    ["blocked request", (attempt: ReturnType<typeof uiEvidencePassedAttemptData>) => {
      attempt.diagnostics.blocked_requests = 1;
    }],
    ["missing core capture", (attempt: ReturnType<typeof uiEvidencePassedAttemptData>) => {
      attempt.manifest.capture.accessibility = false;
    }],
    ["forged lifecycle version", (attempt: ReturnType<typeof uiEvidencePassedAttemptData>) => {
      attempt.version = 2;
    }],
    ["nested manifest extension", (attempt: ReturnType<typeof uiEvidencePassedAttemptData>) => {
      (attempt.manifest.source as Record<string, unknown>).unreviewed_path = "C:\\secret";
    }],
    ["network-enabled start recipe", (attempt: ReturnType<typeof uiEvidencePassedAttemptData>) => {
      attempt.manifest.start.network = "enabled";
    }],
    ["non-loopback browser target", (attempt: ReturnType<typeof uiEvidencePassedAttemptData>) => {
      attempt.manifest.url = "https://example.com:443/";
    }],
    ["duplicate manifest step", (attempt: ReturnType<typeof uiEvidencePassedAttemptData>) => {
      attempt.manifest.steps.push({ ...attempt.manifest.steps[0] });
    }],
  ])("rejects passed UI evidence with a %s", async (_label, mutate) => {
    const attempt = uiEvidencePassedAttemptData();
    mutate(attempt);
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-ui-evidence", data: [attempt],
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    await expect(new APIClient("read-secret").uiEvidence("run-1"))
      .rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("rejects a terminal UI evidence bundle whose artifact totals are incomplete", async () => {
    const bundle = uiEvidencePassedBundleData();
    bundle.artifacts.pop();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-ui-evidence-bundle", data: bundle,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    await expect(new APIClient("read-secret").uiEvidenceBundle("ui-attempt-1"))
      .rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("rejects screenshot dimensions that do not match the sealed viewport and DPR", async () => {
    const bundle = uiEvidencePassedBundleData();
    bundle.artifacts[0].width = 1;
    bundle.artifacts[0].height = 1;
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-ui-evidence-dimensions", data: bundle,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    await expect(new APIClient("read-secret").uiEvidenceBundle("ui-attempt-1"))
      .rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it.each([
    ["artifact after completion", (bundle: ReturnType<typeof uiEvidencePassedBundleData>) => {
      bundle.artifacts[0].created_at = "2026-08-20T00:00:03Z";
    }],
    ["step before attempt start", (bundle: ReturnType<typeof uiEvidencePassedBundleData>) => {
      bundle.steps[0].started_at = "2026-08-20T00:00:00Z";
    }],
  ])("rejects UI evidence chronology with %s", async (_label, mutate) => {
    const bundle = uiEvidencePassedBundleData();
    mutate(bundle);
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-ui-evidence-chronology", data: bundle,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    await expect(new APIClient("read-secret").uiEvidenceBundle("ui-attempt-1"))
      .rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("accepts a complete source-bound UI evidence bundle with explicit retention", async () => {
    const bundle = uiEvidencePassedBundleData();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-ui-evidence-complete", data: bundle,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    await expect(new APIClient("read-secret").uiEvidenceBundle("ui-attempt-1"))
      .resolves.toEqual(bundle);
  });

  it("streams an exact hash-bound artifact and rejects bytes beyond the sealed size", async () => {
    const content = new TextEncoder().encode("bounded evidence");
    const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", content));
    const sha256 = [...digest].map((byte) => byte.toString(16).padStart(2, "0")).join("");
    const metadata = { ...uiEvidencePassedBundleData().artifacts[1],
      bytes: content.byteLength, sha256 } as UIEvidenceArtifactMetadata;
    const headers = { "Content-Type": metadata.mime, ETag: `"${sha256}"`,
      "X-CyberAgent-Content-SHA256": sha256, "X-CyberAgent-Evidence-Untrusted": "true" };
    const fetchMock = vi.fn().mockResolvedValueOnce(new Response(content, { headers }))
      .mockResolvedValueOnce(new Response(new Uint8Array([...content, 1]), { headers }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret");

    await expect(client.downloadUIEvidenceArtifact("ui-attempt-1", metadata))
      .resolves.toMatchObject({ size: content.byteLength, type: metadata.mime });
    await expect(client.downloadUIEvidenceArtifact("ui-attempt-1", metadata))
      .rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("rejects a cross-origin API base before issuing a request", () => {
    expect(() => new APIClient("read-secret", "https://example.com/api/v1"))
      .toThrow("current browser origin");
    expect(() => new APIClient("read-secret", "/api/v10"))
      .toThrow("must be /api/v1");
  });

  it("discovers Run-owned commands and model workspace tools independently", async () => {
    const data = {
      protocol_version: "runtime_capabilities.v1",
      workspace_import_enabled: false,
      agent_code_tools_enabled: true,
      code_intel_enabled: true,
      execution_permission_control_enabled: true, operator_approval_enabled: true,
      workspace_sandbox_enabled: false,
      danger_full_access_enabled: true,
      command_runtime_enabled: true,
      command_runtime_protocol_available: true,
      command_runtime_adapter_installed: true,
      command_runtime_adapter_ready: true,
      command_runtime_adapters: [commandRuntimeAdapterData()],
      browser_cdp_permission_control_enabled: true, full_cdp_debug_enabled: true,
      full_cdp_session_control_enabled: true,
      run_control_enabled: true, run_creation_enabled: true,
      standard_code_preset_enabled: true,
      session_message_enabled: true, thread_control_enabled: true,
      session_steering_control_enabled: true,
      run_lifecycle_enabled: true, run_execution_enabled: true,
      plan_delivery_control_enabled: true, approval_control_enabled: true,


      model_control_enabled: true, provider_credential_enabled: true,
      file_edit_review_enabled: true, file_edit_proposal_enabled: true,
      file_edit_apply_enabled: true, run_wake_control_enabled: true,
      run_wake_execution_enabled: true, run_wake_worker_enabled: true,
      scheduled_job_control_enabled: true, scheduled_job_worker_enabled: true,
      skill_installation_enabled: true, evidence_attachment_enabled: true,
      verification_evidence_enabled: true,
      ui_evidence_control_enabled: true,
      embedded_analyzer_execution_enabled: true,
	  workspace_checkpoint_control_enabled: true,
      git_advanced_control_enabled: true,
      github_review_control_enabled: true,
      batch_delivery_control_enabled: true,
      batch_delivery_host_validation_enabled: true,
      process_execution_enabled: true, shell_execution_enabled: true,
      docker_execution_enabled: false,
      wake_worker: { protocol_version: "run_wake_worker_health.v1", enabled: true,
        state: "running", active: false, poll_interval_ms: 2000, concurrency: 1,
        max_steps: 1, runtime_enable_supported: false, persistent_service: false },
      scheduled_job_worker: { protocol_version: "scheduled-job-worker-health.v1", enabled: true,
        state: "running", active: false, poll_interval_ms: 2000, concurrency: 1,
        runtime_enable_supported: false, persistent_service: false, authority_escalation: false },
    } as const;
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-capabilities", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));
    const view = await new APIClient("read-secret").runtimeCapabilities();
    expect(view).toEqual(data);
    expect(clientCapabilitiesFromRuntime(view)).toMatchObject({
      executionPermissionControlEnabled: true, operatorApprovalEnabled: true,
      workspaceSandboxEnabled: false,
      dangerFullAccessEnabled: true,
      commandRuntimeEnabled: true,
      commandRuntimeProtocolAvailable: true,
      commandRuntimeAdapterInstalled: true,
      commandRuntimeAdapterReady: true,
      agentCodeToolsEnabled: true,
      codeIntelEnabled: true,
      browserCDPPermissionControlEnabled: true, fullCDPDebugEnabled: true,
      fullCDPSessionControlEnabled: true,

      fileEditProposalEnabled: true, providerCredentialEnabled: true,
      runWakeWorkerEnabled: true,
      verificationEvidenceEnabled: true,
      uiEvidenceControlEnabled: true,
      embeddedAnalyzerExecutionEnabled: true,
	  workspaceCheckpointControlEnabled: true,
      gitAdvancedControlEnabled: true,
      batchDeliveryControlEnabled: true,
      batchDeliveryHostValidationEnabled: true,
    });
  });

  it("rejects worker health that reports activity before the loop is running", async () => {
    const data = runtimeCapabilitiesData({
      scheduled_job_worker: {
        protocol_version: "scheduled-job-worker-health.v1", enabled: true,
        state: "ready", active: true, poll_interval_ms: 2000, concurrency: 1,
        runtime_enable_supported: false, persistent_service: false,
        authority_escalation: false,
      },
    });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-invalid-worker-health", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));
    await expect(new APIClient("read-secret").runtimeCapabilities())
      .rejects.toThrow("Run wake worker capability response is invalid");
  });

  it("accepts Docker execution capability only with permission control", async () => {
    const data = runtimeCapabilitiesData({ docker_execution_enabled: true });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-docker-capabilities", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));
    const view = await new APIClient("read-secret").runtimeCapabilities();
    expect(view).toEqual(data);
    expect(clientCapabilitiesFromRuntime(view)).toMatchObject({
      dockerExecutionEnabled: true,
    });
  });

  it("accepts Full CDP with Full Access while the Debug runtime is disabled", async () => {
    const data = runtimeCapabilitiesData({  });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-full-cdp-with-full-access", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    await expect(new APIClient("read-secret").runtimeCapabilities())
      .resolves.toEqual(data);
  });

  it("rejects Full CDP without the Full Access adapter", async () => {
    const data = runtimeCapabilitiesData({
      danger_full_access_enabled: false,

      full_cdp_debug_enabled: true,
    });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-full-cdp-without-full-access", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    await expect(new APIClient("read-secret").runtimeCapabilities())
      .rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("rejects Full CDP session control without the production Full CDP runtime", async () => {
    const data = runtimeCapabilitiesData({
      full_cdp_debug_enabled: false,
      full_cdp_session_control_enabled: true,
    });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-full-cdp-session-without-runtime", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    await expect(new APIClient("read-secret").runtimeCapabilities())
      .rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("rejects an inconsistent Command Runtime adapter receipt", async () => {
    const adapter = { ...commandRuntimeAdapterData(),
      kind: "sandboxed_workspace", isolation_grade: "workspace_sandbox" };
    const data = runtimeCapabilitiesData({ command_runtime_adapters: [adapter] });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-command-runtime-capabilities", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    await expect(new APIClient("read-secret").runtimeCapabilities())
      .rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("accepts only the exact Run capability readiness protocol", async () => {
    const data = capabilityReadinessData();
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-run-readiness", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(new APIClient("read-secret").runCapabilityReadiness("run-1"))
      .resolves.toEqual(data);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/runs/run-1/capability-readiness");
    expect(init.headers).toMatchObject({ Authorization: "Bearer read-secret" });
  });

  it.each([
    { installed: true, blocked_by: ["startup_gate_closed"], remediation: [] },
    { installed: false, blocked_by: [], remediation: [] },
    { installed: false, blocked_by: ["startup_gate_closed"], remediation: ["grant_full_access"] },
    { installed: false, blocked_by: ["startup_gate_closed"], remediation: ["enable_and_restart"], capability_grant: true },
  ])("rejects inconsistent verification startup guidance %j", async (feature) => {
    const data = { ...capabilityReadinessData(), ui_evidence: feature };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-verification-invalid", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));
    await expect(new APIClient("read-secret").runCapabilityReadiness("run-1")).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("accepts the current Go paused Plan response with three permissions and an installed LPAC adapter without a Run grant", async () => {
    const envelope = capabilityReadinessEnvelope();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(envelope), {
      status: 200, headers: { "Content-Type": "application/json" },
    })));
    const client = new APIClient("read-secret");
    const result = await client.runCapabilityReadiness(envelope.data.run_id);
    expect(result).toEqual(envelope.data);
    expect(result.permissions.map((option) => option.value)).toEqual(["ask", "auto", "full"]);
    expect(result.command_runtime).toMatchObject({ adapter_installed: true, adapter_ready: true,
      adapter_kind: "sandboxed_workspace", backend: "local_windows_lpac", current_run_granted: false });
    expect(result.capability_grant).toBe(false);
  });

  it("rejects an archived five-mode readiness response as the current selectable contract", async () => {
    // Preserve the original Phase H HTTP capture as historical evidence. Current
    // readiness offers only writable choices; historical permission reads are
    // exercised separately by approval-mode-integration.test.tsx.
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(pausedPlanReadiness), {
      status: 200, headers: { "Content-Type": "application/json" },
    })));
    await expect(new APIClient("read-secret").runCapabilityReadiness(pausedPlanReadiness.data.run_id))
      .rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it.each([
    ["missing Auto", (data: ReturnType<typeof capabilityReadinessData>) => { data.permissions.splice(1, 1); }],
    ["duplicate Ask", (data: ReturnType<typeof capabilityReadinessData>) => { data.permissions[1] = { ...data.permissions[0]! }; }],
    ["unknown mode", (data: ReturnType<typeof capabilityReadinessData>) => { data.permissions[1]!.value = "unknown_permission"; }],
    ["retired mode", (data: ReturnType<typeof capabilityReadinessData>) => { data.permissions[1]!.value = "workspace_access"; }],
    ["extra mode", (data: ReturnType<typeof capabilityReadinessData>) => { data.permissions.push({ ...data.permissions[2]! }); }],
    ["reordered modes", (data: ReturnType<typeof capabilityReadinessData>) => { data.permissions.reverse(); }],
    ["multiple selected modes", (data: ReturnType<typeof capabilityReadinessData>) => { data.permissions[1]!.selected = true; }],
  ])("rejects a noncanonical current readiness permission group with %s", async (_label, mutate) => {
    const data = capabilityReadinessData();
    mutate(data);
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-readiness-permission-drift", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));
    await expect(new APIClient("read-secret").runCapabilityReadiness(data.run_id))
      .rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("rejects incomplete or malformed adapter identities even without an active Run grant", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    for (const patch of [{ adapter_kind: undefined }, { backend: undefined }, { adapter_kind: "unknown_adapter" },
      { adapter_kind: ["sandboxed_workspace"] }, { backend: "b".repeat(257) }, { adapter_kind: "" }, { backend: "" }]) {
      const envelope = capabilityReadinessEnvelope();
      const data = { ...envelope.data, command_runtime: { ...envelope.data.command_runtime, ...patch } };
      fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ ...envelope, data }), {
        status: 200, headers: { "Content-Type": "application/json" },
      }));
      await expect(new APIClient("read-secret").runCapabilityReadiness(data.run_id))
        .rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    }
  });

  it("creates a first-run Standard Code target without leaking control authority", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-standard-code-create",
      data: standardCodeTrustData(),
    }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: false, standardCodePresetEnabled: true,
    });

    await expect(client.createStandardCode({ version: "standard_code_preset.v1",
      workspace_id: "workspace-1", goal: "Implement the parser",
      backend_intent: "auto", confirm_workspace_trust: false,
    }, "web-standard-code-create-0001")).resolves.toMatchObject({
      status: "blocked", trust_required: true, workspace_id: "workspace-1",
      capability_grant: false,
    });

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/standard-code/preset");
    expect(url).not.toContain("control-secret");
    expect(init.headers).toMatchObject({ Authorization: "Bearer control-secret",
      "Idempotency-Key": "web-standard-code-create-0001" });
    expect(String(init.body)).not.toContain("control-secret");
  });

  it("sends an explicit sbx request and rejects a response rebound to another backend or without sbx readiness", async () => {
    const data = standardCodeTrustData({ backend_intent: "sbx", selected_backend: "sbx", selection_reason: "explicit_sbx",
      sbx_readiness: { backend: "sbx", available: true, blocked_by: [], remediation: [] } });
    const fetchMock = vi.fn(); vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read", "/api/v1", "control", { standardCodePresetEnabled: true });
    const respond = (value: unknown) => new Response(JSON.stringify({ version: "api.v1", request_id: "sbx-preset", data: value }),
      { status: 202, headers: { "Content-Type": "application/json" } });
    fetchMock.mockResolvedValueOnce(respond(data));
    const body = { version: "standard_code_preset.v1" as const, backend_intent: "sbx" as const, confirm_workspace_trust: false };
    expect(await client.configureStandardCode("run-1", "configure", body, "web-preset-sbx-0001"))
      .toMatchObject({ selected_backend: "sbx", trust_required: true });
    expect(JSON.parse(String(fetchMock.mock.calls[0]?.[1].body))).toEqual(body);
    for (const value of [standardCodeTrustData(), { ...data, sbx_readiness: undefined },
      { ...data, sbx_readiness: { backend: "sbx", available: true, blocked_by: ["sbx_unavailable"], remediation: ["install_or_start_sbx"] } }]) {
      fetchMock.mockResolvedValueOnce(respond(value));
      await expect(client.configureStandardCode("run-1", "configure", body, "web-preset-sbx-0001"))
        .rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    }
  });

  it("reads historical three and current four backend profiles and rejects unavailable sbx claims of a ready runtime", async () => {
    const current = capabilityReadinessData();
    const old = { ...current, profiles: current.profiles.filter((profile) => profile.value !== "sbx") };
    const sbx = { value: "sbx", selected: false, selectable: false, runtime_available: false,
      restart_required: false, blocked_by: ["sbx_unavailable"], remediation: ["install_or_start_sbx"] };
    const data = { ...old, profiles: [...old.profiles, sbx] };
    const fetchMock = vi.fn(); vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read");
    const respond = (value: unknown) => new Response(JSON.stringify({ version: "api.v1", request_id: "sbx-readiness", data: value }),
      { status: 200, headers: { "Content-Type": "application/json" } });
    fetchMock.mockResolvedValueOnce(respond(old));
    expect((await client.runCapabilityReadiness("run-1")).profiles).toHaveLength(3);
    fetchMock.mockResolvedValueOnce(respond(data));
    expect((await client.runCapabilityReadiness("run-1")).profiles).toHaveLength(4);
    fetchMock.mockResolvedValueOnce(respond({ ...data, profiles: [...old.profiles, { ...sbx, runtime_available: true }] }));
    await expect(client.runCapabilityReadiness("run-1")).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("fails closed for an invalid or rebound first-run Standard Code target", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-standard-code-rebound",
      data: standardCodeTrustData({ workspace_id: "workspace-other" }),
    }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      standardCodePresetEnabled: true,
    });

    await expect(client.createStandardCode({ version: "standard_code_preset.v1",
      workspace_id: " workspace-1", goal: "Implement the parser",
      backend_intent: "auto", confirm_workspace_trust: false,
    }, "web-standard-code-create-0002")).rejects.toThrow("normalized Workspace");
    expect(fetchMock).not.toHaveBeenCalled();

    await expect(client.createStandardCode({ version: "standard_code_preset.v1",
      workspace_id: "workspace-1", goal: "Implement the parser",
      backend_intent: "auto", confirm_workspace_trust: false,
    }, "web-standard-code-create-0003")).rejects.toThrow("changed the requested target");
  });

  it.each([
    ["unknown protocol", (data: ReturnType<typeof capabilityReadinessData>) => {
      data.protocol_version = "run_capability_readiness.v2";
    }],
    ["capability grant", (data: ReturnType<typeof capabilityReadinessData>) => {
      data.capability_grant = true;
    }],
    ["unknown blocker", (data: ReturnType<typeof capabilityReadinessData>) => {
      data.profiles[0]!.blocked_by = ["private_runtime_error"];
      data.profiles[0]!.remediation = ["pause_run"];
    }],
    ["missing remediation", (data: ReturnType<typeof capabilityReadinessData>) => {
      data.profiles[0]!.blocked_by = ["run_not_quiescent"];
    }],
    ["unrelated remediation", (data: ReturnType<typeof capabilityReadinessData>) => {
      data.profiles[0]!.blocked_by = ["run_not_quiescent"];
      data.profiles[0]!.remediation = ["pause_run", "trust_workspace"];
    }],
    ["unsorted blockers", (data: ReturnType<typeof capabilityReadinessData>) => {
      data.profiles[0]!.blocked_by = ["backend_not_ready", "startup_gate_closed"];
      data.profiles[0]!.remediation = ["restart_with_startup_gate", "retry_backend_readiness"];
      data.profiles[0]!.runtime_available = false;
      data.profiles[0]!.restart_required = true;
    }],
    ["missing selected permission", (data: ReturnType<typeof capabilityReadinessData>) => {
      data.permissions[0]!.selected = false;
    }],
    ["adapter ready without installation", (data: ReturnType<typeof capabilityReadinessData>) => {
      data.command_runtime.adapter_installed = false;
    }],
    ["granted adapter without identity", (data: ReturnType<typeof capabilityReadinessData>) => {
      data.command_runtime.current_run_granted = true;
      delete (data.command_runtime as Partial<typeof data.command_runtime>).adapter_kind;
      delete (data.command_runtime as Partial<typeof data.command_runtime>).backend;
    }],
    ["private extension", (data: ReturnType<typeof capabilityReadinessData>) => {
      (data.permissions[0] as Record<string, unknown>).root_path = "C:\\private";
    }],
  ])("fails closed for a malformed readiness response with %s", async (_label, mutate) => {
    const data = capabilityReadinessData();
    mutate(data);
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-run-readiness-invalid", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    await expect(new APIClient("read-secret").runCapabilityReadiness("run-1"))
      .rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("creates scheduled jobs through the control bearer and validates closed authority", async () => {
    const job = scheduledJobData();
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-scheduled-create", data: {
        protocol_version: "scheduled-job-control.v1", action: "create", job,
        replayed: false, execution_started: false, authority_bypass: false,
      },
    }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      scheduledJobControlEnabled: true,
    });
    const body = { ...job.spec, confirm_repair: false } as unknown as
      ScheduledJobCreateRequestView & { target_run_id?: string };
    delete (body as Partial<typeof body>).target_run_id;
    await expect(client.createScheduledJob("run-1", body,
      "scheduled-client-operation-0001")).resolves.toMatchObject({ job });
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/runs/run-1/scheduled-jobs");
    expect(init.headers).toMatchObject({ Authorization: "Bearer control-secret",
      "Idempotency-Key": "scheduled-client-operation-0001" });
  });

  it.each(["valid", "different-job", "missing-consent"])("binds observation confirmation to the exact job and persisted consent: %s", async (scenario) => {
    const job = scheduledJobData({ observation_consent_version: scenario === "missing-consent" ? 0 : 1,
      ...(scenario === "different-job" ? { id: "scheduled-job-other" } : {}) });
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-observation", data: {
        protocol_version: "scheduled-job-control.v1", action: "enable-observation", job,
        replayed: false, execution_started: false, authority_bypass: false,
      },
    }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      scheduledJobControlEnabled: true,
    });
    const result = client.enableScheduledJobObservation("run-1", "scheduled-job-1", {
      version: "scheduled-job-control.v1", expected_revision: 1, observation_consent_version: 1,
    }, "scheduled-observation-client-0001");
    if (scenario === "valid") await expect(result).resolves.toMatchObject({ job });
    else await expect(result).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/runs/run-1/scheduled-jobs/scheduled-job-1/enable-observation");
    expect(init.headers).toMatchObject({ Authorization: "Bearer control-secret",
      "Idempotency-Key": "scheduled-observation-client-0001" });
  });

  it.each([undefined, "confirmed_read_only", "confirmed_jobs", "all_jobs", ["all_jobs"], "unknown"])("validates worker selection scope without coercion: %j", async (scope) => {
    const data = runtimeCapabilitiesData();
    const scoped = { ...data, scheduled_job_worker: { ...data.scheduled_job_worker,
      ...(scope === undefined ? {} : { selection_scope: scope }) } };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-scope", data: scoped,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));
    const result = new APIClient("read-secret").runtimeCapabilities();
    if (scope === undefined || scope === "confirmed_read_only" || scope === "confirmed_jobs" || scope === "all_jobs") {
      await expect(result).resolves.toMatchObject({ scheduled_job_worker: scoped.scheduled_job_worker });
    } else await expect(result).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it.each(["valid", "no-consent", "observation-consent", "cross-target", "private-receipt", "wrong-job"])(
    "validates automatic execution consent, targets, and outcome identity: %s", async (scenario) => {
      const original = scheduledJobData();
      const job = { ...original, observation_consent_version: scenario === "observation-consent" ? 1 : 0,
        execution_consent_version: scenario === "no-consent" ? 0 : 1,
        spec: { ...original.spec, execution_mode: "automatic", stop_on_target_terminal: false,
          execution_target: { kind: "thread", thread_id: "thread-1", prompt: "Check the changes",
            ...(scenario === "cross-target" ? { workflow_definition_id: "workflow-1" } : {}) } } };
      const execution = { job_id: scenario === "wrong-job" ? "different-job" : job.id, occurrence_at: "2026-10-18T10:00:00Z",
        run_id: "run-1", message_id: "message-1", state: "waiting_approval",
        result: "Waiting for a tool approval", created_at: "2026-10-18T10:00:00Z", updated_at: "2026-10-18T10:01:00Z",
        ...(scenario === "private-receipt" ? { fence_token: "private" } : {}) };
      vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-automatic", data: { protocol_version: "scheduled-job.v1",
          snapshot: { job, rounds: [], notifications: [], executions: [execution] } },
      }), { status: 200, headers: { "Content-Type": "application/json" } })));
      const result = new APIClient("read-secret").getScheduledJob(job.id);
      if (scenario === "valid") await expect(result).resolves.toMatchObject({ snapshot: { job, executions: [execution] } });
      else await expect(result).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    });

  it("rejects private fencing fields and unredacted diagnostic timeline data", async () => {
    const job = scheduledJobData();
    const detail = { protocol_version: "scheduled-job.v1", snapshot: { job,
      notifications: [], rounds: [{ protocol_version: "scheduled-job-round.v1",
        job_id: job.id, occurrence_at: "2026-08-20T11:00:00Z", ordinal: 1, attempt: 1,
        claim_generation: 1, status: "completed", event_sequence: 2,
        changed: true, model_called: false, tool_called: false,
        started_at: "2026-08-20T11:00:00Z", completed_at: "2026-08-20T11:00:01Z",
        fence_token: "private" }] } };
    const redaction = { command_input: "withheld", event_payloads: "withheld",
      prompts: "withheld", secrets: "redacted", terminal_input: "withheld" };
    const bundle = { protocol_version: "diagnostic-bundle.v1",
      generated_at: "2026-08-20T11:00:00Z",
      doctor: { protocol_version: "doctor-snapshot.v1",
        generated_at: "2026-08-20T11:00:00Z", ready: true, schema_version: 119,
        build: {}, models: {}, checks: [], redaction },
      debug: { protocol_version: "debug-query.v1", run_id: "run-1",
        from: "2026-08-20T10:00:00Z", to: "2026-08-20T11:00:00Z",
        after_sequence: 0, next_after_sequence: 1, limit: 100, scanned: 1,
        has_more: false, redaction, items: [{ sequence: 1, type: "run.started",
          source: "test", subject_id: "run-1", category: "application",
          occurred_at: "2026-08-20T10:30:00Z", observed_at: "2026-08-20T10:30:00Z",
          timestamp_adjusted: false, evidence: "persisted_event", payload_state: "withheld",
          payload: "private" }] } };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-scheduled-detail", data: detail }), { status: 200,
        headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-diagnostic-bundle", data: bundle }), { status: 200,
        headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret");
    await expect(client.getScheduledJob("scheduled-job-1")).rejects.toThrow("exposed payload");
    await expect(client.diagnosticBundle("run-1")).rejects.toThrow("redaction contract");
  });

  it("accepts bounded batch delivery projections and rejects private child authority fields", async () => {
    const gitObject = "a".repeat(64);
    const toolProfile = {
      version: "batch-delivery-tools.v1",
      workspace_list: true, workspace_read: true, workspace_search: true,
      workspace_change: true, workspace_apply: true, git_status: true,
      git_diff: true, git_commit: true, workspace_delete: false, shell: false,
      process: false, network: false, credentials: false, debug_terminal: false,
      approvals: false, spawn_children: false,
    };
    const snapshot = {
      protocol_version: "batch-delivery.v1",
      plan: {
        id: "batch-1", run_id: "run-1", proposal_id: "proposal-1",
        root_agent_id: "agent-root", workspace_id: "workspace-1", status: "active",
        spec: {
          version: "batch-delivery.v1",
          tasks: [{ ordinal: 1,
            ownership_hints: [{ path: "internal/parser", kind: "directory" }],
            dependency_ordinals: [],
            budget: { turn_limit: 3, token_limit: 2_048, timeout_millis: 120_000 },
            validations: [{ id: "diff", kind: "git_diff_check", scope: "." }],
            expected_artifacts: [{ path_hint: "internal/parser", kind: "code" }],
          }],
          contract: { require_clean: true, require_independent_review: true,
            require_all_validations: true, max_changed_files: 32,
            max_diff_bytes: 1024 * 1024 },
        },
        base_commit: gitObject, source_branch: "main", created_by: "operator",
        created_at: "2026-08-20T00:00:00Z", updated_at: "2026-08-20T00:00:01Z",
      },
      children: [{
        workspace: {
          plan_id: "batch-1", ordinal: 1, agent_id: "agent-child", generation: 1,
          status: "dispatched", branch: "codex/batch/task-1", base_commit: gitObject,
          tool_profile: toolProfile, lease_expires_at: "2026-08-20T01:00:00Z",
          last_heartbeat_at: "2026-08-20T00:00:01Z",
          created_at: "2026-08-20T00:00:00Z", updated_at: "2026-08-20T00:00:01Z",
        },
        mailbox: [{ id: "message-1", ordinal: 1, generation: 1, sequence: 1,
          kind: "dispatch", actor: "agent-root", summary: "worktree dispatched",
          evidence_refs: [], created_at: "2026-08-20T00:00:01Z" }],
      }],
      merge_steps: [],
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-batch", data: snapshot }), { status: 200,
        headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-batch-private", data: { ...snapshot, children: [{
          ...snapshot.children[0], workspace: { ...snapshot.children[0].workspace,
            worktree_root: "C:/private/worktree" },
        }] } }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret");

    await expect(client.getRunBatchDelivery("run-1", "batch-1"))
      .resolves.toMatchObject({ plan: { base_commit: gitObject }, children: [{
        workspace: { tool_profile: { network: false, spawn_children: false } },
      }] });
    await expect(client.getRunBatchDelivery("run-1", "batch-1"))
      .rejects.toThrow("Batch delivery snapshot is invalid");
  });

  it("rejects Docker execution capability without permission control", async () => {
    const data = runtimeCapabilitiesData({ docker_execution_enabled: true,
      execution_permission_control_enabled: false });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-docker-capabilities", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));
    await expect(new APIClient("read-secret").runtimeCapabilities())
      .rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("rejects paths that escape the API base", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    await expect(new APIClient("read-secret").get("/../health"))
      .rejects.toThrow("escaped the configured base path");
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("parses a ready Safe Web readiness receipt", async () => {
    const data = {
      protocol_version: "browser_safe_web_readiness.v1",
      evidence_fingerprint: "a".repeat(64),
      review_fingerprint: "b".repeat(64),
      executable_identity_fingerprint: "c".repeat(64),
      acceptance_fingerprint: "d".repeat(64),
      adapter: "windows_wfp_dynamic.v1",
      policy_version: "browser_network_containment_policy.v2",
      operating_system: "windows",
      architecture: "amd64",
      collector_identity: "probe-operator",
      reviewer_identity: "reviewer",
      ready: true,
      issued_at: "2026-08-16T00:00:00Z",
      expires_at: "2026-08-16T00:15:00Z",
      fingerprint: "e".repeat(64),
    };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-readiness", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));
    const view = await new APIClient("read-secret").safeWebReadiness("chrome");
    expect(view).toEqual(data);
  });

  it("rejects a ready receipt that also carries a blocking reason", async () => {
    const data = {
      protocol_version: "browser_safe_web_readiness.v1",
      evidence_fingerprint: "",
      review_fingerprint: "",
      executable_identity_fingerprint: "c".repeat(64),
      acceptance_fingerprint: "d".repeat(64),
      adapter: "windows_wfp_dynamic.v1",
      policy_version: "browser_network_containment_policy.v2",
      operating_system: "windows",
      architecture: "amd64",
      collector_identity: "probe-operator",
      reviewer_identity: "reviewer",
      ready: true,
      blocking_reason: "evidence_missing",
      issued_at: "2026-08-16T00:00:00Z",
      expires_at: "2026-08-16T00:15:00Z",
      fingerprint: "e".repeat(64),
    };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-readiness", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));
    await expect(new APIClient("read-secret").safeWebReadiness("chrome"))
      .rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("rejects a not-ready receipt without a blocking reason", async () => {
    const data = {
      protocol_version: "browser_safe_web_readiness.v1",
      evidence_fingerprint: "",
      review_fingerprint: "",
      executable_identity_fingerprint: "c".repeat(64),
      acceptance_fingerprint: "d".repeat(64),
      adapter: "windows_wfp_dynamic.v1",
      policy_version: "browser_network_containment_policy.v2",
      operating_system: "windows",
      architecture: "amd64",
      collector_identity: "probe-operator",
      reviewer_identity: "reviewer",
      ready: false,
      issued_at: "2026-08-16T00:00:00Z",
      expires_at: "2026-08-16T00:15:00Z",
      fingerprint: "e".repeat(64),
    };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-readiness", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));
    await expect(new APIClient("read-secret").safeWebReadiness("chrome"))
      .rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it("returns typed API errors without exposing request headers", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1",
      request_id: "req-denied",
      error: { code: "POLICY_DENIED", message: "valid bearer authorization is required" },
    }), { status: 401, headers: { "Content-Type": "application/json" } })));

    const request = new APIClient("wrong-secret").health();
    await expect(request).rejects.toMatchObject({
      code: "POLICY_DENIED",
      status: 401,
      requestID: "req-denied",
    });
  });

  it("forwards an opaque collection cursor without leaking the bearer", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1",
        request_id: "req-runs-1",
        data: [{ id: "run-paused", status: "paused" }],
        page: { limit: 1, next_cursor: "opaque+/cursor=one" },
      }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1",
        request_id: "req-runs-2",
        data: [{ id: "run-completed", status: "completed" }],
        page: { limit: 1 },
      }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret");

    const first = await client.getPage<{ id: string; status: string }>("/runs", { limit: 1 });
    const second = await client.getPage<{ id: string; status: string }>(
      "/runs", { limit: 1 }, first.page.next_cursor,
    );

    expect(first.items[0]?.status).toBe("paused");
    expect(second.items[0]?.status).toBe("completed");
    const [firstURL] = fetchMock.mock.calls[0] as [string, RequestInit];
    const [secondURL, secondInit] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(firstURL).toBe("/api/v1/runs?limit=1");
    expect(secondURL).toContain("limit=1");
    expect(secondURL).toContain("cursor=opaque%2B%2Fcursor%3Done");
    expect(secondURL).not.toContain("read-secret");
    expect(secondInit.headers).toMatchObject({ Authorization: "Bearer read-secret" });
  });

  it("keeps the optional control token in Authorization and out of URLs and bodies", async () => {
    const responseEnvelope = {
      version: "api.v1",
      request_id: "req-profile",
      data: { replayed: false, execution_profile: { profile: "docker" } },
    };
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(responseEnvelope), {
      status: 202,
      headers: { "Content-Type": "application/json" },
    }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret");

    const result = await client.postControl<{ execution_profile: { profile: string } }>(
      "/runs/run-1/execution-profile",
      { profile: "docker" },
      "web-execution-profile-test-0001",
    );

    expect(result.execution_profile.profile).toBe("docker");
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/runs/run-1/execution-profile");
    expect(url).not.toContain("control-secret");
    expect(init.headers).toMatchObject({
      Authorization: "Bearer control-secret",
      "Content-Type": "application/json",
      "Idempotency-Key": "web-execution-profile-test-0001",
    });
    expect(init.body).toBe(JSON.stringify({ profile: "docker" }));
    expect(String(init.body)).not.toContain("control-secret");
  });

  it("does not expose control operations without a distinct control token", async () => {
    vi.stubGlobal("fetch", vi.fn());
    const client = new APIClient("read-secret");
    await expect(client.postControl("/runs/run-1/execution-profile", { profile: "docker" },
      "web-execution-profile-test-0002")).rejects.toThrow("control bearer token");
    expect(fetch).not.toHaveBeenCalled();
  });

  it("uses the distinct control bearer for optimistic PATCH and DELETE without leaking it", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-memory-patch",
        data: { id: "memory-1", version: 2, status: "disabled" },
      }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-memory-delete",
        data: { id: "memory-1", deleted: true, recoverable: false },
      }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret");

    const updated = await client.patchControl<{ version: number; status: string }>(
      "/memories/memory-1", { expected_version: 1, status: "disabled" });
    const deleted = await client.deleteControl<{ deleted: boolean; recoverable: boolean }>(
      "/memories/memory-1", { expected_version: 2 });

    expect(updated).toMatchObject({ version: 2, status: "disabled" });
    expect(deleted).toEqual({ id: "memory-1", deleted: true, recoverable: false });
    const [patchURL, patchInit] = fetchMock.mock.calls[0] as [string, RequestInit];
    const [deleteURL, deleteInit] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(patchURL).toBe("/api/v1/memories/memory-1");
    expect(deleteURL).toBe("/api/v1/memories/memory-1");
    expect(patchInit).toMatchObject({ method: "PATCH" });
    expect(deleteInit).toMatchObject({ method: "DELETE" });
    expect(patchInit.headers).toMatchObject({ Authorization: "Bearer control-secret" });
    expect(deleteInit.headers).toMatchObject({ Authorization: "Bearer control-secret" });
    expect(String(patchInit.body)).not.toContain("control-secret");
    expect(String(deleteInit.body)).not.toContain("control-secret");
  });

  it("separates Run creation from existing Run controls and validates closed authority", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-create", data: runCreationData,
    }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: false,
      runCreationEnabled: true,
    });
    expect(client.hasControl).toBe(false);
    expect(client.hasRunCreation).toBe(true);
    await expect(client.postControl("/runs/run-1/execution-profile", { profile: "docker" },
      "web-profile-separated-0001")).rejects.toThrow("control bearer token");
    const result = await client.createRun({
      version: "run_creation.v1", goal: "Create parser", workspace_id: "workspace-1",
    }, "web-run-create-operation-0001");
    expect(result.run.id).toBe("run-created");
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/runs");
    expect(url).not.toContain("control-secret");
    expect(init.headers).toMatchObject({
      Authorization: "Bearer control-secret",
      "Idempotency-Key": "web-run-create-operation-0001",
    });
    expect(String(init.body)).not.toContain("control-secret");
  });

  it("rejects a Run creation response that widens authority", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1",
      request_id: "req-create-forged",
      data: { ...runCreationData, mode: { ...runCreationData.mode, capability_grant: true } },
    }), { status: 202, headers: { "Content-Type": "application/json" } })));
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: false,
      runCreationEnabled: true,
    });
    await expect(client.createRun({ version: "run_creation.v1", goal: "Create parser",
      workspace_id: "workspace-1" }, "web-run-create-operation-0002"))
      .rejects.toThrow("closed authority");
  });

  it("rejects a Run creation response bound to a different requested workspace", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1",
      request_id: "req-create-forged-workspace",
      data: runCreationData,
    }), { status: 202, headers: { "Content-Type": "application/json" } })));
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: false,
      runCreationEnabled: true,
    });
    await expect(client.createRun({ version: "run_creation.v1", goal: "Create parser",
      workspace_id: "workspace-other" }, "web-run-create-operation-0003"))
      .rejects.toThrow("closed authority");
  });

  it("rejects a Run creation response with a cross-Workspace Mission scope", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1",
      request_id: "req-create-forged-scope",
      data: {
        ...runCreationData,
        mission: { ...runCreationData.mission,
          scope: { ...runCreationData.mission.scope, workspace_id: "workspace-other" } },
      },
    }), { status: 202, headers: { "Content-Type": "application/json" } })));
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: false,
      runCreationEnabled: true,
    });
    await expect(client.createRun({ version: "run_creation.v1", goal: "Create parser",
      workspace_id: "workspace-1" }, "web-run-create-operation-scope"))
      .rejects.toThrow("closed authority");
  });

  it("rejects a Run creation response bound to a different requested goal", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1",
      request_id: "req-create-forged-goal",
      data: runCreationData,
    }), { status: 202, headers: { "Content-Type": "application/json" } })));
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: false,
      runCreationEnabled: true,
    });
    await expect(client.createRun({ version: "run_creation.v1", goal: "Different goal",
      workspace_id: "workspace-1" }, "web-run-create-operation-0004"))
      .rejects.toThrow("closed authority");
  });

  it("creates a Thread and validates its canonical identity binding", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-thread-create", data: threadCreationData,
    }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret");

    const result = await client.createThread({
      version: "thread_creation.v1", goal: "Create parser", workspace_id: "workspace-1",
    }, "web-thread-create-operation-0001");

    expect(result.thread).toEqual(threadData);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/threads");
    expect(init.headers).toMatchObject({ Authorization: "Bearer control-secret",
      "Idempotency-Key": "web-thread-create-operation-0001" });
  });

  it("accepts the Go-resolved default route while preserving identity and authority checks", async () => {
    const routed = {
      ...threadCreationData,
      run: { ...threadCreationData.run,
        config: { ...threadCreationData.run.config, model_route: "openai/default-model" } },
      session: { ...threadCreationData.session, route: "openai/default-model" },
    };
    const client = new APIClient("read-secret", "/api/v1", "control-secret");
    const request = { version: "thread_creation.v1", goal: "Create parser",
      workspace_id: "workspace-1" } as const;
    const respond = (data: unknown) => vi.stubGlobal("fetch", vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ version: "api.v1", request_id: "req-default-route", data }),
        { status: 202, headers: { "Content-Type": "application/json" } })));
    respond(routed);
    await expect(client.createThread(request, "web-default-route-0001")).resolves.toEqual(routed);
    for (const invalid of [
      { ...routed, session: { ...routed.session, route: "openai/other-model" } },
      { ...routed, mission: { ...routed.mission, workspace_id: "other-workspace" } },
      { ...routed, mode: { ...routed.mode, capability_grant: true } },
      { ...routed, run: { ...routed.run,
        config: { ...routed.run.config, model_route: "openai//model" } },
        session: { ...routed.session, route: "openai//model" } },
    ]) {
      respond(invalid);
      await expect(client.createThread(request, "web-default-route-0002")).rejects.toThrow();
    }
    respond(routed);
    await expect(client.createThread({ ...request, provider: "openai", model: "chosen-model" },
      "web-default-route-0003")).rejects.toThrow("closed authority");
  });

  it("binds an explicitly selected Provider/model to the first Thread Run", async () => {
    const routed = {
      ...threadCreationData,
      run: { ...threadCreationData.run,
        config: { ...threadCreationData.run.config, model_route: "deepseek/deepseek-chat" } },
      session: { ...threadCreationData.session, route: "deepseek/deepseek-chat" },
    };
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-thread-create-routed", data: routed,
    }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret");

    await expect(client.createThread({
      version: "thread_creation.v1", goal: "Create parser", workspace_id: "workspace-1",
      provider: "deepseek", model: "deepseek-chat",
    }, "web-thread-create-routed-0001")).resolves.toEqual(routed);
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toMatchObject({
      provider: "deepseek", model: "deepseek-chat",
    });

    await expect(client.createThread({
      version: "thread_creation.v1", goal: "Create parser", workspace_id: "workspace-1",
      provider: "deepseek",
    }, "web-thread-create-routed-0002")).rejects.toThrow("Provider/model pair");
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("submits Thread messages and lifecycle changes through the stable Thread URL", async () => {
    const archivedThread = { ...threadData, status: "archived", version: 3,
      composer_state: "unavailable", archived_at: "2026-08-24T00:02:00Z",
      updated_at: "2026-08-24T00:02:00Z" };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-thread-message", data: threadMessageData,
      }), { status: 202, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-thread-archive",
        data: { version: "thread_lifecycle.v1", thread: archivedThread,
          capability_grant: false },
      }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret");

    await expect(client.submitThreadMessage("thread-created", {
      version: "thread_message_submission.v1", content: "Continue safely",
    }, "web-thread-message-operation-0001")).resolves.toEqual(threadMessageData);
    await expect(client.transitionThread("thread-created", "archive", {
      version: "thread_lifecycle.v1", expected_version: 2,
    }, "web-thread-archive-operation-0001")).resolves.toMatchObject({ thread: archivedThread });

    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      "/api/v1/threads/thread-created/messages", "/api/v1/threads/thread-created/archive",
    ]);
    expect((fetchMock.mock.calls[0]?.[1] as RequestInit).headers).toMatchObject({
      "Idempotency-Key": "web-thread-message-operation-0001",
    });
    expect((fetchMock.mock.calls[1]?.[1] as RequestInit).headers).toMatchObject({
      "Idempotency-Key": "web-thread-archive-operation-0001",
    });
  });

  it("accepts an additive Thread execution observation without replacing composer state", async () => {
    const response = { ...threadMessageData,
      thread: { ...threadData, execution_state: "running" } };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-thread-execution-observation", data: response,
    }), { status: 202, headers: { "Content-Type": "application/json" } })));
    const client = new APIClient("read-secret", "/api/v1", "control-secret");
    await expect(client.submitThreadTurn("thread-created", {
      version: "thread_message_submission.v1", content: "Continue this task",
    }, "web-thread-execution-observation-0001")).resolves.toMatchObject({
      thread: { execution_state: "running", composer_state: "ready" },
    });
  });

  it("keeps Thread execution observations inside the closed public vocabulary", async () => {
    const client = new APIClient("read-secret", "/api/v1", "control-secret");
    for (const execution_state of ["ready", "future_state", false, ["idle"], { state: "idle" }]) {
      vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-thread-invalid-observation",
        data: { ...threadMessageData, thread: { ...threadData, execution_state } },
      }), { status: 202, headers: { "Content-Type": "application/json" } })));
      await expect(client.submitThreadTurn("thread-created", {
        version: "thread_message_submission.v1", content: "Continue this task",
      }, "web-thread-invalid-observation-0001")).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    }
  });

  it("accepts a synchronously completed Thread turn without a stale active Run binding", async () => {
    const completedThread = {
      ...threadData,
      active_run_id: undefined,
      composer_state: "successor_required",
      version: 3,
      updated_at: "2026-08-24T00:02:00Z",
    };
    const response = {
      ...threadMessageData,
      thread: completedThread,
      execution_started: true,
      model_called: true,
    };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-thread-message-completed", data: response,
    }), { status: 202, headers: { "Content-Type": "application/json" } })));
    const client = new APIClient("read-secret", "/api/v1", "control-secret");

    await expect(client.submitThreadTurn("thread-created", {
      version: "thread_message_submission.v1", content: "Finish synchronously",
    }, "web-thread-message-completed-0001")).resolves.toEqual(response);
  });

  it("accepts a prepared pending Thread input when its tool call awaits approval", async () => {
    // The real initial web_fetch journey leaves the selected operator message
    // pending/prepared while the Run waits for the operator's decision.
    const response = { ...threadMessageData,
      thread: { ...threadData, composer_state: "waiting_approval" },
      steering: { ...threadMessageData.steering, prepared: true },
      execution_started: true, model_called: true, tool_called: true };
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-thread-prepared-input", data: response,
    }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret");
    await expect(client.submitThreadTurn("thread-created", {
      version: "thread_message_submission.v1", content: "Read the official documentation",
    }, "web-thread-prepared-input-0001")).resolves.toEqual(response);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("keeps prepared input status, identity, and execution authority checks closed", async () => {
    const valid = { ...threadMessageData,
      thread: { ...threadData, composer_state: "waiting_approval" },
      steering: { ...threadMessageData.steering, prepared: true },
      execution_started: true, model_called: true, tool_called: true };
    const client = new APIClient("read-secret", "/api/v1", "control-secret");
    for (const response of [
      { ...valid, steering: { ...valid.steering, prepared: "true" } },
      { ...valid, steering: { ...valid.steering, prepared: undefined } },
      { ...valid, steering: { ...valid.steering, status: "committed", committed_at: "2026-08-24T00:02:00Z" } },
      { ...valid, steering: { ...valid.steering, status: "cancelled", cancelled_at: "2026-08-24T00:02:00Z" } },
      { ...valid, steering: { ...valid.steering, committed_at: "2026-08-24T00:02:00Z" } },
      { ...valid, steering: { ...valid.steering, cancelled_at: "2026-08-24T00:02:00Z" } },
      { ...valid, thread: { ...valid.thread, id: "thread-other" } },
      { ...valid, run_id: "run-other" },
      { ...valid, execution_started: false },
      { ...valid, capability_grant: true },
    ]) {
      vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-thread-invalid-prepared-input", data: response,
      }), { status: 202, headers: { "Content-Type": "application/json" } })));
      await expect(client.submitThreadTurn("thread-created", {
        version: "thread_message_submission.v1", content: "Read the official documentation",
      }, "web-thread-invalid-prepared-input-0001")).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    }
  });

  it("sends file snapshots only through the unified turn with its control token and operation identity", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-turn-files", data: threadMessageData,
    }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret");
    const body = { version: "thread_message_submission.v1", content: "Use these project files",
      files: [{ source_kind: "workspace_file", path: "docs/说明.md", expected_sha256: "a".repeat(64) }] } as const;
    await expect(client.submitThreadTurn("thread-created", { ...body, files: [...body.files] },
      "web-turn-files-0001")).resolves.toEqual(threadMessageData);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/threads/thread-created/turns");
    expect(init.headers).toMatchObject({ Authorization: "Bearer control-secret",
      "Idempotency-Key": "web-turn-files-0001" });
    expect(JSON.parse(String(init.body))).toEqual(body);
  });

  it("rejects malformed, repeated, excessive or unavailable file references before dispatch", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    const file = { source_kind: "workspace_file", path: "README.md", expected_sha256: "a".repeat(64) };
    const invalidLists = [[{ ...file, path: "../outside.md" }], [{ ...file, path: "." }],
      [{ ...file, expected_sha256: "A".repeat(64) }], [{ ...file, instruction_authorized: true }],
      [file, file], Array.from({ length: 5 }, (_, index) => ({ ...file, path: `file-${index}.md` }))];
    const client = new APIClient("read-secret", "/api/v1", "control-secret");
    for (const files of invalidLists) {
      await expect(client.submitThreadTurn("thread-created", {
        version: "thread_message_submission.v1", content: "Use files", files,
      } as Parameters<APIClient["submitThreadTurn"]>[1], "web-invalid-files-0001")).rejects.toThrow("重新选择");
    }
    const disabled = new APIClient("read-secret", "/api/v1", "control-secret",
      { evidenceAttachmentEnabled: false });
    await expect(disabled.submitThreadTurn("thread-created", {
      version: "thread_message_submission.v1", content: "Use files", files: [file],
    } as Parameters<APIClient["submitThreadTurn"]>[1], "web-disabled-files-0001")).rejects.toThrow("重新选择");
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("distinguishes a terminal unqueued turn rejection from an unknown error and rejects widened markers", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret");
    for (const marker of [false, undefined, true]) {
      fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1", request_id: "turn-rejected",
        error: { code: "CONFLICT", message: "Cannot complete submission",
          ...(marker !== undefined ? { message_queued: marker } : {}) },
      }), { status: 409, headers: { "Content-Type": "application/json" } }));
      await expect(client.submitThreadTurn("thread-created", {
        version: "thread_message_submission.v1", content: "Check this file",
      }, "web-turn-rejected-0001")).rejects.toMatchObject({
        code: marker === true ? "INVALID_RESPONSE" : "CONFLICT", messageQueued: marker === false ? false : undefined,
      });
    }
  });

  it("only accepts an explicit true operation-key invalidation marker", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: true, standardCodePresetEnabled: true,
    });
    for (const marker of [true, undefined, false, "true", null]) {
      fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1", request_id: "preset-invalidated",
        error: { code: "CONFLICT", message: "Configuration changed",
          ...(marker !== undefined ? { operation_key_invalidated: marker } : {}) },
      }), { status: 409, headers: { "Content-Type": "application/json" } }));
      await expect(client.configureStandardCode("run-1", "configure", {
        version: "standard_code_preset.v1", backend_intent: "auto", confirm_workspace_trust: false,
      }, "web-preset-rejected-0001")).rejects.toMatchObject({
        code: marker === true || marker === undefined ? "CONFLICT" : "INVALID_RESPONSE",
        operationKeyInvalidated: marker === true ? true : undefined,
      });
    }
  });

  it("only trusts an explicit terminal failed-turn marker and rejects contradictory queue facts", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret");
    for (const marker of [true, undefined, false, "true", null]) {
      fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1", request_id: "turn-failed",
        error: { code: "UNAVAILABLE", message: "This turn failed",
          ...(marker !== undefined ? { turn_failed: marker } : {}) },
      }), { status: 503, headers: { "Content-Type": "application/json" } }));
      await expect(client.submitThreadTurn("thread-created", {
        version: "thread_message_submission.v1", content: "Continue the task",
      }, "web-turn-failure-0001")).rejects.toMatchObject({
        code: marker === true || marker === undefined ? "UNAVAILABLE" : "INVALID_RESPONSE",
        turnFailed: marker === true ? true : undefined,
      });
    }
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1", request_id: "contradiction",
      error: { code: "CONFLICT", message: "Invalid facts", turn_failed: true, message_queued: false },
    }), { status: 409, headers: { "Content-Type": "application/json" } }));
    await expect(client.submitThreadTurn("thread-created", {
      version: "thread_message_submission.v1", content: "Continue the task",
    }, "web-turn-failure-0001")).rejects.toMatchObject({ code: "INVALID_RESPONSE", turnFailed: undefined });
  });

  it("recovers an exact failed Thread boundary without widening successor authority", async () => {
    const failedAt = "2026-08-24T00:03:00Z";
    const recoveredThread = { ...threadData, active_run_id: undefined, version: 3,
      composer_state: "successor_required", updated_at: failedAt };
    const failedRun = { ...runCreationData.run, status: "failed",
      created_at: "2026-08-24T00:00:00Z", started_at: "2026-08-24T00:01:00Z",
      finished_at: failedAt, updated_at: failedAt };
    const response = { version: "thread_run_recovery.v1", thread: recoveredThread,
      failed_run: failedRun, successor_required: true, replayed: false };
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-thread-recovery", data: response,
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret");

    await expect(client.recoverThreadRun("thread-created", {
      version: "thread_run_recovery.v1", run_id: "run-created",
      handoff_operation_id: "run-handoff-failed-1",
    }, "web-thread-recovery-operation-0001")).resolves.toEqual(response);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/threads/thread-created/recovery");
    expect(init.headers).toMatchObject({ Authorization: "Bearer control-secret",
      "Idempotency-Key": "web-thread-recovery-operation-0001" });
    expect(JSON.parse(String(init.body))).toEqual({ version: "thread_run_recovery.v1",
      run_id: "run-created", handoff_operation_id: "run-handoff-failed-1" });
  });

  it("rejects a Thread recovery response that keeps the failed Run active", async () => {
    const failedAt = "2026-08-24T00:03:00Z";
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-thread-recovery-forged", data: {
        version: "thread_run_recovery.v1", thread: { ...threadData, updated_at: failedAt },
        failed_run: { ...runCreationData.run, status: "failed",
          created_at: "2026-08-24T00:00:00Z", started_at: "2026-08-24T00:01:00Z",
          finished_at: failedAt, updated_at: failedAt },
        successor_required: true, replayed: false,
      },
    }), { status: 200, headers: { "Content-Type": "application/json" } })));
    const client = new APIClient("read-secret", "/api/v1", "control-secret");
    await expect(client.recoverThreadRun("thread-created", {
      version: "thread_run_recovery.v1", run_id: "run-created",
      handoff_operation_id: "run-handoff-failed-1",
    }, "web-thread-recovery-operation-0002")).rejects.toThrow("continuation contract");
  });

  it("validates the closed, ordered Thread transcript projection", async () => {
    const transcriptItem = {
      version: "thread_transcript.v1", id: "event-1", canonical_id: "item-1",
      run_id: "run-created", run_ordinal: 1, sequence: 9, activity_type: "search",
      stage: "running", kind: "tool_call", source: "harness", title: "Tool started",
      status: "running", verifiable: true, instruction_authorized: false,
      tool_name: "web_search", durable_call_id: "call-web-search-1",
      activity_detail_ref: "call-web-search-1", detail_available: true,
      stream_item_id: "item-1", provisional: false,
      durable: true, created_at: "2026-08-24T00:01:00Z",
    };
    const evidenceItem = {
      ...transcriptItem, sequence: 10, stage: "result", title: "Web evidence fetched",
      tool_name: "web_fetch", web_evidence: {
        version: "web_evidence_presentation.v1", source_id: "source-web-1",
        snapshot_id: "snapshot-web-1", url: "https://docs.example.com/report",
        title: "Fetched report", state: "partial", fetched_at: "2026-08-24T00:01:00Z",
        stale_at: "2026-08-25T00:01:00Z", digest: "a".repeat(64), partial: true,
        stale: false, citeable: true, untrusted: true, instruction_authorized: false,
      },
    };
    const operatorItem = {
      version: "thread_transcript.v1", id: "event-op-1", canonical_id: "item-op-1",
      run_id: "run-created", run_ordinal: 1, sequence: 11, activity_type: "message",
      stage: "result", kind: "operator_input", source: "operator", title: "用户消息",
      status: "committed", verifiable: true, instruction_authorized: false,
      delivery_mode: "steer", provisional: false,
      durable: true, created_at: "2026-08-24T00:02:00Z",
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-transcript", data: [transcriptItem],
        page: { limit: 100 },
      }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-transcript-evidence", data: [evidenceItem],
        page: { limit: 100 },
      }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-transcript-forged",
        data: [{ ...transcriptItem, arguments: { path: "private.txt" } }],
        page: { limit: 100 },
      }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-transcript-unsafe-evidence",
        data: [{ ...evidenceItem, web_evidence: {
          ...evidenceItem.web_evidence, url: "javascript:alert(1)",
        } }],
        page: { limit: 100 },
      }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-transcript-steer", data: [operatorItem],
        page: { limit: 100 },
      }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-transcript-bad-mode",
        data: [{ ...operatorItem, delivery_mode: "queued" }],
        page: { limit: 100 },
      }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-transcript-mode-source",
        data: [{ ...operatorItem, source: "harness" }],
        page: { limit: 100 },
      }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret");

    await expect(client.getPage("/threads/thread-created/transcript", { limit: 100 }))
      .resolves.toMatchObject({ items: [transcriptItem] });
    await expect(client.getPage("/threads/thread-created/transcript", { limit: 100 }))
      .resolves.toMatchObject({ items: [evidenceItem] });
    await expect(client.getPage("/threads/thread-created/transcript", { limit: 100 }))
      .rejects.toThrow("transcript item is invalid");
    await expect(client.getPage("/threads/thread-created/transcript", { limit: 100 }))
      .rejects.toThrow("Thread Web evidence is invalid");
    // Operator steering messages carry a bounded delivery_mode on the operator source only.
    await expect(client.getPage("/threads/thread-created/transcript", { limit: 100 }))
      .resolves.toMatchObject({ items: [operatorItem] });
    await expect(client.getPage("/threads/thread-created/transcript", { limit: 100 }))
      .rejects.toThrow("transcript provenance is invalid");
    await expect(client.getPage("/threads/thread-created/transcript", { limit: 100 }))
      .rejects.toThrow("transcript provenance is invalid");
  });

  it.each(["disabled", "host"])("lazily reads the strict Thread command activity projection (%s network)", async (network) => {
    const detail = {
      version: "thread_activity_detail.v2", activity_ref: "call-command-1",
      run_id: "run-created", tools: [{ name: "command_runtime", label: "运行命令",
        agent_id: "agent-root", agent_role: "root", agent_label: "Root Agent", status: "completed",
        started_at: "2026-08-24T00:01:00Z", completed_at: "2026-08-24T00:01:02Z",
        duration_milliseconds: 2_000, detail: { kind: "command", command: { commands: [{ command: "pnpm test session",
          working_directory: "packages/core", execution_environment: network === "host" ? "Host · Full Access" : "Workspace Sandbox",
           network, status: "completed", exit_code: 0,
           duration_milliseconds: 2_000, stdout_preview: "42 tests passed",
           stderr_preview: "", truncated: false, artifacts: [] }] } } }],
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-activity-detail", data: detail,
      }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-activity-detail-forged",
        data: { ...detail, tools: [{ ...detail.tools[0], environment: { API_KEY: "secret" } }] },
      }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-activity-detail-unknown-network",
        data: { ...detail, tools: [{ ...detail.tools[0], detail: { kind: "command", command: {
          commands: [{ ...detail.tools[0].detail.command.commands[0], network: "allowlist" }],
        } } }] },
      }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret");

    await expect(client.threadActivityDetail("thread-created", "call-command-1"))
      .resolves.toEqual(detail);
    await expect(client.threadActivityDetail("thread-created", "call-command-1"))
      .rejects.toThrow("tool detail is invalid");
    await expect(client.threadActivityDetail("thread-created", "call-command-1"))
      .rejects.toThrow("tool detail is invalid");
    expect(fetchMock.mock.calls[0]?.[0])
      .toBe("/api/v1/threads/thread-created/activities/call-command-1");
  });

  it("accepts a typed non-command activity union and rejects raw payload fields", async () => {
    const detail = {
      version: "thread_activity_detail.v2", activity_ref: "call-web-1", run_id: "run-created",
      tools: [{ name: "web_fetch", label: "网页读取", agent_id: "agent-root",
        agent_role: "root", agent_label: "Root Agent", status: "completed",
        started_at: "2026-09-03T00:00:00Z", completed_at: "2026-09-03T00:00:01Z",
        duration_milliseconds: 1_000, detail: { kind: "web_fetch", web_fetch: {
          operation: "fetch", url: "https://example.com/reference?lang=zh&page=2", state: "fetched",
          http_status: 200, robots: "allowed", robots_policy: "audit_only", redirects: 0,
          partial: false, citeable: true, boundary: { authorization: "policy_checked",
            error_code: "", failure_reason: "", truncated: false, untrusted: true },
        } } }],
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-web-detail", data: detail }),
      { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-web-detail-forged", data: { ...detail, tools: [{ ...detail.tools[0],
          detail: { ...detail.tools[0].detail, payload_json: "{secret}" } }] } }),
      { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-web-detail-sensitive-query", data: { ...detail, tools: [{
          ...detail.tools[0], detail: { kind: "web_fetch", web_fetch: {
            ...detail.tools[0].detail.web_fetch,
            url: "https://example.com/reference?page=2&auth=private-canary",
          } },
        }] } }),
      { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret");

    await expect(client.threadActivityDetail("thread-created", "call-web-1"))
      .resolves.toEqual(detail);
    await expect(client.threadActivityDetail("thread-created", "call-web-1"))
      .rejects.toThrow("tool detail is invalid");
    await expect(client.threadActivityDetail("thread-created", "call-web-1"))
      .rejects.toThrow("tool detail is invalid");
  });

  it("requires a closed Run-bound reference for an available file activity Diff", async () => {
    const fileEdit = { operation: "apply", action: "replace", path: "src/session.ts",
      destination_path: "", apply_status: "applied", applied: true, file_written: true,
      replayed: false, edit_id: "edit-1", diff_available: true,
      diff: { added_lines: 1, removed_lines: 1, hunks: 1,
        summary: "修改 src/session.ts · +1 −1" },
      boundary: { authorization: "policy_checked", error_code: "", failure_reason: "",
        truncated: false, untrusted: false } };
    const detail = {
      version: "thread_activity_detail.v2", activity_ref: "call-edit-1", run_id: "run-created",
      tools: [{ name: "workspace_apply", label: "文件修改", agent_id: "agent-root",
        agent_role: "root", agent_label: "Root Agent", status: "completed",
        started_at: "2026-09-03T00:00:00Z", duration_milliseconds: 10,
        detail: { kind: "file_edit", file_edit: fileEdit } }],
    };
    const response = (data: unknown) => new Response(JSON.stringify({ version: "api.v1",
      request_id: "req-file-edit-detail", data }),
    { status: 200, headers: { "Content-Type": "application/json" } });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(response(detail))
      .mockResolvedValueOnce(response({ ...detail, tools: [{ ...detail.tools[0],
        detail: { kind: "file_edit", file_edit: { ...fileEdit, edit_id: "" } } }] }))
      .mockResolvedValueOnce(response({ ...detail, tools: [{ ...detail.tools[0],
        detail: { kind: "file_edit", file_edit: { ...fileEdit, payload_json: "{secret}" } } }] }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret");

    await expect(client.threadActivityDetail("thread-created", "call-edit-1"))
      .resolves.toEqual(detail);
    await expect(client.threadActivityDetail("thread-created", "call-edit-1"))
      .rejects.toThrow("tool detail is invalid");
    await expect(client.threadActivityDetail("thread-created", "call-edit-1"))
      .rejects.toThrow("tool detail is invalid");
  });

  it("accepts semantic query parameters in ranked search sources and rejects credential queries",
    async () => {
      const source = { rank: 1, title: "Runtime guide",
        url: "https://docs.example.com/runtime?lang=zh&page=2", provider: "provider-native",
        state: "fetched", citeable: true };
      const detail = { version: "thread_activity_detail.v2", activity_ref: "call-search-1",
        run_id: "run-created", tools: [{ name: "web_search", label: "联网搜索",
          agent_id: "agent-root", agent_role: "root", agent_label: "Root Agent",
          status: "completed", started_at: "2026-09-03T00:00:00Z",
          duration_milliseconds: 10, detail: { kind: "web_search", web_search: {
            operation: "search", query: "runtime architecture", limit: 2,
            provider: "provider-native", search_policy: "provider_native",
            selection_reason: "configured route", source_count: 1, citeable: true,
            sources: [source], boundary: { authorization: "policy_checked", error_code: "",
              failure_reason: "", truncated: false, untrusted: true },
          } },
        }] };
      const fetchMock = vi.fn()
        .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
          request_id: "req-search-detail", data: detail }),
        { status: 200, headers: { "Content-Type": "application/json" } }))
        .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
          request_id: "req-search-detail-sensitive", data: { ...detail, tools: [{
            ...detail.tools[0], detail: { kind: "web_search", web_search: {
              ...detail.tools[0].detail.web_search,
              sources: [{ ...source, url: "https://docs.example.com/runtime?page=2&token=private" }],
            } },
          }] } }),
        { status: 200, headers: { "Content-Type": "application/json" } }));
      vi.stubGlobal("fetch", fetchMock);
      const client = new APIClient("read-secret", "/api/v1", "control-secret");

      await expect(client.threadActivityDetail("thread-created", "call-search-1"))
        .resolves.toEqual(detail);
      await expect(client.threadActivityDetail("thread-created", "call-search-1"))
        .rejects.toThrow("tool detail is invalid");
    });

  it("lazily reads a bounded sanitized Thread activity artifact", async () => {
    const content = "complete output";
    const digest = new Uint8Array(await globalThis.crypto.subtle.digest(
      "SHA-256", new TextEncoder().encode(content)));
    const sha256 = [...digest].map((byte) => byte.toString(16).padStart(2, "0")).join("");
    const artifact = { version: "thread_activity_artifact.v1",
      activity_ref: "call-command-1", artifact_ref: "artifact-stdout-1", stream: "stdout",
      mime: "text/plain; charset=utf-8", content, sha256,
      size_bytes: new TextEncoder().encode(content).byteLength, redacted: true, truncated: false,
      untrusted: true, instruction_authorized: false };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-activity-artifact", data: artifact }),
      { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-activity-artifact-forged", data: { ...artifact,
          instruction_authorized: true } }),
      { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-activity-artifact-digest-mismatch", data: { ...artifact,
          sha256: "b".repeat(64) } }),
      { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret");

    await expect(client.threadActivityArtifact(
      "thread-created", "call-command-1", "artifact-stdout-1")).resolves.toEqual(artifact);
    await expect(client.threadActivityArtifact(
      "thread-created", "call-command-1", "artifact-stdout-1"))
      .rejects.toThrow("activity artifact is invalid");
    await expect(client.threadActivityArtifact(
      "thread-created", "call-command-1", "artifact-stdout-1"))
      .rejects.toThrow("artifact digest verification failed");
    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      "/api/v1/threads/thread-created/activities/call-command-1/artifacts/artifact-stdout-1");
  });

  it("parses bounded command summaries and counts Unicode code points like Go", async () => {
    const summary = { version: "thread_activity_summary.v1",
      activity_ref: "call-command-1", command: "pnpm test session", status: "running",
      duration_milliseconds: 250, command_count: 1 };
    const transcriptItem = { version: "thread_transcript.v1", id: "item-command-1",
      canonical_id: "call-command-1", run_id: "run-created", run_ordinal: 1, sequence: 1,
      activity_type: "execute", stage: "running", kind: "tool_call", source: "harness",
      title: "运行命令", status: "pending", verifiable: true,
      instruction_authorized: false, tool_name: "command_runtime",
      durable_call_id: "call-command-1", activity_detail_ref: "call-command-1",
      detail_available: true, activity_summary: summary, provisional: false, durable: true,
      created_at: "2026-09-02T00:00:00Z" };
    const unicodeCommand = "😀".repeat(4_096);
    const detail = { version: "thread_activity_detail.v2", activity_ref: "call-command-1",
      run_id: "run-created", tools: [{ name: "command_runtime", label: "运行命令",
        agent_id: "agent-root", agent_role: "root", agent_label: "Root Agent", status: "completed",
        started_at: "2026-09-02T00:00:00Z", duration_milliseconds: 1,
        detail: { kind: "command", command: { commands: [{ command: unicodeCommand, working_directory: ".",
           execution_environment: "Workspace Sandbox", network: "disabled",
           status: "completed", exit_code: 0, duration_milliseconds: 1,
           stdout_preview: "", stderr_preview: "", truncated: false, artifacts: [] }] } } }] };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-command-summary", data: [transcriptItem], page: { limit: 100 } }),
      { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-command-summary-forged", data: [{ ...transcriptItem,
          activity_summary: { ...summary, environment: { API_KEY: "secret" } } }],
        page: { limit: 100 } }),
      { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-command-unicode", data: detail }),
      { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-command-unicode-too-long", data: { ...detail, tools: [{
          ...detail.tools[0], detail: { kind: "command", command: { commands: [{
            ...detail.tools[0].detail.command.commands[0], command: "😀".repeat(4_097) }] } } }] } }),
      { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret");

    await expect(client.getPage("/threads/thread-created/transcript", { limit: 100 }))
      .resolves.toMatchObject({ items: [transcriptItem] });
    await expect(client.getPage("/threads/thread-created/transcript", { limit: 100 }))
      .rejects.toThrow("activity summary is invalid");
    await expect(client.threadActivityDetail("thread-created", "call-command-1"))
      .resolves.toEqual(detail);
    await expect(client.threadActivityDetail("thread-created", "call-command-1"))
      .rejects.toThrow("activity tool detail is invalid");
  });

  it("rejects forged Thread authority and cross-Thread projections", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-thread-forged-authority",
        data: { ...threadMessageData, model_called: true },
      }), { status: 202, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-thread-cross-projection",
        data: { ...threadMessageData, thread: { ...threadData, id: "thread-other" } },
      }), { status: 202, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-thread-forged-lifecycle",
        data: { version: "thread_lifecycle.v1",
          thread: { ...threadData, status: "archived", version: 3,
            composer_state: "unavailable", archived_at: "2026-08-24T00:02:00Z",
            updated_at: "2026-08-24T00:02:00Z" }, capability_grant: true },
      }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret");
    const request = { version: "thread_message_submission.v1" as const, content: "Continue" };

    await expect(client.submitThreadMessage("thread-created", request,
      "web-thread-message-operation-0002")).rejects.toThrow("invalid");
    await expect(client.submitThreadMessage("thread-created", request,
      "web-thread-message-operation-0003")).rejects.toThrow("invalid");
    await expect(client.transitionThread("thread-created", "archive", {
      version: "thread_lifecycle.v1", expected_version: 2,
    }, "web-thread-archive-operation-0002")).rejects.toThrow("invalid");
  });

  it("separates Session messages and validates the closed submission response", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-session-message", data: sessionMessageData,
    }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: false,
      runCreationEnabled: false,
      sessionMessageEnabled: true,
    });
    expect(client.hasControl).toBe(false);
    expect(client.hasRunCreation).toBe(false);
    expect(client.hasSessionMessages).toBe(true);
    const result = await client.submitSessionMessage("sess-1", {
      version: "session_message_submission.v1", content: "Review the latest change",
    }, "web-session-message-operation-0001");
    expect(result.steering.id).toBe("steer-1");
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/sessions/sess-1/messages");
    expect(url).not.toContain("control-secret");
    expect(init.headers).toMatchObject({
      Authorization: "Bearer control-secret",
      "Idempotency-Key": "web-session-message-operation-0001",
    });
    expect(init.body).toBe(JSON.stringify({
      version: "session_message_submission.v1", content: "Review the latest change",
    }));
  });

  it("rejects forged Session message authority and cross-Session responses", async () => {
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: false,
      runCreationEnabled: false,
      sessionMessageEnabled: true,
    });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-session-forged",
      data: { ...sessionMessageData, model_called: true },
    }), { status: 202, headers: { "Content-Type": "application/json" } })).mockResolvedValueOnce(
      new Response(JSON.stringify({
        version: "api.v1", request_id: "req-session-cross",
        data: { ...sessionMessageData, session_id: "sess-other" },
      }), { status: 202, headers: { "Content-Type": "application/json" } }),
    ));
    const request = { version: "session_message_submission.v1" as const, content: "Review" };
    await expect(client.submitSessionMessage("sess-1", request,
      "web-session-message-operation-0002")).rejects.toThrow("invalid");
    await expect(client.submitSessionMessage("sess-1", request,
      "web-session-message-operation-0003")).rejects.toThrow("invalid");
  });

  it("does not expose Session messages without their distinct capability", async () => {
    vi.stubGlobal("fetch", vi.fn());
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: false,
      runCreationEnabled: true,
      sessionMessageEnabled: false,
    });
    await expect(client.submitSessionMessage("sess-1", {
      version: "session_message_submission.v1", content: "Review",
    }, "web-session-message-operation-0004")).rejects.toThrow("capability");
    expect(fetch).not.toHaveBeenCalled();
  });

  it("separates pending Session steering cancellation and validates its authority", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-session-cancel",
      data: sessionSteeringCancellationData,
    }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: false, runCreationEnabled: false, sessionMessageEnabled: false,
      sessionSteeringControlEnabled: true,
    });
    expect(client.hasControl).toBe(false);
    expect(client.hasSessionMessages).toBe(false);
    expect(client.hasSessionSteeringControl).toBe(true);
    await expect(client.cancelSessionSteering("sess-1", "steer-1", {
      version: "session_steering_cancellation.v1", reason: "operator cancelled",
    }, "web-session-steering-cancel-0001")).resolves.toEqual(sessionSteeringCancellationData);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/sessions/sess-1/messages/steer-1/cancel");
    expect(init.headers).toMatchObject({
      Authorization: "Bearer control-secret",
      "Idempotency-Key": "web-session-steering-cancel-0001",
    });
  });

  it("rejects forged or cross-message Session steering cancellation responses", async () => {
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: false, runCreationEnabled: false, sessionMessageEnabled: false,
      sessionSteeringControlEnabled: true,
    });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-session-cancel-forged",
      data: { ...sessionSteeringCancellationData, execution_started: true },
    }), { status: 202, headers: { "Content-Type": "application/json" } })).mockResolvedValueOnce(
      new Response(JSON.stringify({
        version: "api.v1", request_id: "req-session-cancel-cross",
        data: { ...sessionSteeringCancellationData,
          steering: { ...sessionSteeringCancellationData.steering, id: "steer-other" } },
      }), { status: 202, headers: { "Content-Type": "application/json" } }),
    ));
    const body = { version: "session_steering_cancellation.v1" as const, reason: "cancel" };
    await expect(client.cancelSessionSteering("sess-1", "steer-1", body,
      "web-session-steering-cancel-0002")).rejects.toThrow("invalid");
    await expect(client.cancelSessionSteering("sess-1", "steer-1", body,
      "web-session-steering-cancel-0003")).rejects.toThrow("invalid");
  });

  it("does not expose Session steering cancellation without its capability", async () => {
    vi.stubGlobal("fetch", vi.fn());
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      sessionMessageEnabled: true, sessionSteeringControlEnabled: false,
    });
    await expect(client.cancelSessionSteering("sess-1", "steer-1", {
      version: "session_steering_cancellation.v1", reason: "cancel",
    }, "web-session-steering-cancel-0004")).rejects.toThrow("capability");
    expect(fetch).not.toHaveBeenCalled();
  });

  it("separates Run lifecycle and bounded execution capabilities", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-lifecycle", data: runLifecycleData,
      }), { status: 202, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-execute", data: runExecutionData,
      }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: false, runCreationEnabled: false, sessionMessageEnabled: false,
      sessionSteeringControlEnabled: false, runLifecycleEnabled: true,
      runExecutionEnabled: true,
    });
    expect(client.hasControl).toBe(false);
    expect(client.hasRunLifecycle).toBe(true);
    expect(client.hasRunExecution).toBe(true);
    await expect(client.controlRunLifecycle("run-1", {
      version: "run_lifecycle_control.v1", action: "start",
    }, "web-run-lifecycle-operation-0001")).resolves.toEqual(runLifecycleData);
    await expect(client.executeRun("run-1", {
      version: "run_execution_handoff.v1", max_steps: 2,
    }, "web-run-execution-operation-0001")).resolves.toEqual(runExecutionData);
    const [lifecycleURL, lifecycleInit] = fetchMock.mock.calls[0] as [string, RequestInit];
    const [executionURL, executionInit] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(lifecycleURL).toBe("/api/v1/runs/run-1/lifecycle");
    expect(executionURL).toBe("/api/v1/runs/run-1/execute");
    expect(lifecycleInit.headers).toMatchObject({ Authorization: "Bearer control-secret",
      "Idempotency-Key": "web-run-lifecycle-operation-0001" });
    expect(executionInit.headers).toMatchObject({ Authorization: "Bearer control-secret",
      "Idempotency-Key": "web-run-execution-operation-0001" });
  });

  it("rejects forged Run lifecycle and execution metadata", async () => {
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runLifecycleEnabled: true, runExecutionEnabled: true,
    });
    vi.stubGlobal("fetch", vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-lifecycle-forged",
        data: { ...runLifecycleData, model_called: true },
      }), { status: 202, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-execute-forged",
        data: { ...runExecutionData, committed_count: 2 },
      }), { status: 202, headers: { "Content-Type": "application/json" } })));
    await expect(client.controlRunLifecycle("run-1", {
      version: "run_lifecycle_control.v1", action: "start",
    }, "web-run-lifecycle-operation-0002")).rejects.toThrow("invalid");
    await expect(client.executeRun("run-1", {
      version: "run_execution_handoff.v1", max_steps: 2,
    }, "web-run-execution-operation-0002")).rejects.toThrow("invalid");
  });

  it("rejects a Run execution response with an unknown Run status", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-execute-status-forged",
      data: { ...runExecutionData, run_status: "arbitrary" },
    }), { status: 202, headers: { "Content-Type": "application/json" } })));
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runExecutionEnabled: true,
    });

    await expect(client.executeRun("run-1", {
      version: "run_execution_handoff.v1", max_steps: 2,
    }, "web-run-execution-status-0001")).rejects.toThrow("invalid");
  });

  it("accepts an exact lifecycle replay after the Run has advanced", async () => {
    const delayedReplay = {
      ...runLifecycleData, replayed: true,
      run: { ...runLifecycleData.run, status: "paused" },
    } as RunLifecycleControlView;
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-lifecycle-delayed", data: delayedReplay,
    }), { status: 202, headers: { "Content-Type": "application/json" } })));
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runLifecycleEnabled: true,
    });
    await expect(client.controlRunLifecycle("run-1", {
      version: "run_lifecycle_control.v1", action: "start",
    }, "web-run-lifecycle-delayed-0001")).resolves.toEqual(delayedReplay);
  });

  it("does not expose Run operations without their distinct capabilities", async () => {
    vi.stubGlobal("fetch", vi.fn());
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runLifecycleEnabled: false, runExecutionEnabled: false,
    });
    await expect(client.controlRunLifecycle("run-1", {
      version: "run_lifecycle_control.v1", action: "start",
    }, "web-run-lifecycle-operation-0003")).rejects.toThrow("capability");
    await expect(client.executeRun("run-1", {
      version: "run_execution_handoff.v1", max_steps: 1,
    }, "web-run-execution-operation-0003")).rejects.toThrow("capability");
    expect(fetch).not.toHaveBeenCalled();
  });

  it("cancels a Supervisor model call and validates the bound response", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-cancel-model", data: modelCancellationData,
    }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: true,
    });
    expect(client.hasControl).toBe(true);
    await expect(client.cancelModelCall("run-1", {
      attempt_id: "attempt-1", model_attempt: 1, reason: "operator halt",
    }, "web-run-cancel-call-0001")).resolves.toEqual(modelCancellationData);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/runs/run-1/active-call/cancel");
    expect(url).not.toContain("control-secret");
    expect(init.headers).toMatchObject({ Authorization: "Bearer control-secret",
      "Idempotency-Key": "web-run-cancel-call-0001" });
    expect(init.body).toBe(JSON.stringify({
      attempt_id: "attempt-1", model_attempt: 1, reason: "operator halt",
    }));
    expect(String(init.body)).not.toContain("control-secret");
  });

  it("reads an exact Run-bound public model stream with the read bearer", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-public-stream", data: publicModelStreamData,
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1");

    await expect(client.getPublicModelStream("run-1")).resolves.toEqual(publicModelStreamData);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/runs/run-1/active-call");
    expect(init.headers).toMatchObject({ Authorization: "Bearer read-secret" });
  });

  it("polls an inactive public model stream without using an HTTP error status", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-public-stream-idle",
      data: { version: "model_public_stream_poll.v1", active: false },
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1");

    await expect(client.pollPublicModelStream("run-1")).resolves.toBeNull();
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/runs/run-1/active-call/poll");
  });

  it("polls and validates an active public model stream snapshot", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-public-stream-active",
      data: { version: "model_public_stream_poll.v1", active: true,
        snapshot: publicModelStreamData },
    }), { status: 200, headers: { "Content-Type": "application/json" } })));
    const client = new APIClient("read-secret", "/api/v1");

    await expect(client.pollPublicModelStream("run-1")).resolves.toEqual(publicModelStreamData);
  });

  it("rejects contradictory public model stream poll projections", async () => {
    vi.stubGlobal("fetch", vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-public-stream-idle-snapshot",
        data: { version: "model_public_stream_poll.v1", active: false,
          snapshot: publicModelStreamData },
      }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-public-stream-active-missing",
        data: { version: "model_public_stream_poll.v1", active: true },
      }), { status: 200, headers: { "Content-Type": "application/json" } })));
    const client = new APIClient("read-secret", "/api/v1");

    await expect(client.pollPublicModelStream("run-1")).rejects.toThrow("Inactive");
    await expect(client.pollPublicModelStream("run-1")).rejects.toThrow("omitted");
  });

  it("accepts more than 32 provider chunks within the bounded output size", async () => {
    const data = { ...publicModelStreamData,
      call: { ...publicModelStreamData.call, stream_chunks: 128, stream_bytes: 128 } };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-public-stream-many-chunks", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));
    const client = new APIClient("read-secret", "/api/v1");

    await expect(client.getPublicModelStream("run-1")).resolves.toEqual(data);
  });

  it("rejects widened or cross-Run public model stream snapshots", async () => {
    vi.stubGlobal("fetch", vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-public-stream-widened",
        data: { ...publicModelStreamData, raw_output: "private" },
      }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-public-stream-cross-run",
        data: { ...publicModelStreamData,
          call: { ...publicModelStreamData.call, run_id: "run-other" } },
      }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-public-stream-arguments",
        data: { ...publicModelStreamData, items: [{
          ...publicModelStreamData.items[0], arguments: { path: "private.md" },
        }] },
      }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-public-stream-duplicate-item",
        data: { ...publicModelStreamData,
          items: [publicModelStreamData.items[0], publicModelStreamData.items[0]] },
      }), { status: 200, headers: { "Content-Type": "application/json" } })));
    const client = new APIClient("read-secret", "/api/v1");

    await expect(client.getPublicModelStream("run-1")).rejects.toThrow("invalid");
    await expect(client.getPublicModelStream("run-1")).rejects.toThrow("invalid");
    await expect(client.getPublicModelStream("run-1")).rejects.toThrow("invalid");
    await expect(client.getPublicModelStream("run-1")).rejects.toThrow("invalid");
  });

  it("cancels a Specialist model call on its nested agent path", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-cancel-specialist", data: specialistModelCancellationData,
    }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: true,
    });
    await expect(client.cancelSpecialistModelCall("run-1", "agent-1", {
      attempt_id: "attempt-2", model_attempt: 2,
    }, "web-agent-cancel-call-0001")).resolves.toEqual(specialistModelCancellationData);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/runs/run-1/agents/agent-1/active-call/cancel");
    expect(init.headers).toMatchObject({ Authorization: "Bearer control-secret",
      "Idempotency-Key": "web-agent-cancel-call-0001" });
    expect(init.body).toBe(JSON.stringify({ attempt_id: "attempt-2", model_attempt: 2 }));
  });

  it("requires a control token before cancelling any model call", async () => {
    vi.stubGlobal("fetch", vi.fn());
    const client = new APIClient("read-secret");
    await expect(client.cancelModelCall("run-1", { attempt_id: "attempt-1", model_attempt: 1 },
      "web-run-cancel-call-0002")).rejects.toThrow("control bearer token");
    await expect(client.cancelSpecialistModelCall("run-1", "agent-1",
      { attempt_id: "attempt-2", model_attempt: 2 }, "web-agent-cancel-call-0002"))
      .rejects.toThrow("control bearer token");
    expect(fetch).not.toHaveBeenCalled();
  });

  it("rejects a model cancellation response bound to a different attempt", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-cancel-forged",
      data: { ...modelCancellationData, model_attempt: 2 },
    }), { status: 202, headers: { "Content-Type": "application/json" } })));
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: true,
    });
    await expect(client.cancelModelCall("run-1", { attempt_id: "attempt-1", model_attempt: 1 },
      "web-run-cancel-call-0003")).rejects.toThrow("invalid");
  });

  it("rejects a Specialist cancellation response bound to a different agent", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-cancel-specialist-forged",
      data: { ...specialistModelCancellationData, agent_id: "agent-other" },
    }), { status: 202, headers: { "Content-Type": "application/json" } })));
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: true,
    });
    await expect(client.cancelSpecialistModelCall("run-1", "agent-1",
      { attempt_id: "attempt-2", model_attempt: 2 }, "web-agent-cancel-call-0003"))
      .rejects.toThrow("invalid");
  });

  it("reads a single artifact detail over the access token", async () => {
    const data = {
      id: "artifact-1", run_id: "run-1", session_id: "session-1", workspace_id: "ws-1",
      kind: "log", source_id: "source-1", tool_name: "shell", stream: "stdout",
      mime: "text/plain", encoding: "utf-8", size_bytes: 42, redacted: false,
      sha256: "a".repeat(64), created_at: "2026-07-31T00:00:00Z",
    };
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-artifact", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret");
    await expect(client.getArtifact("artifact-1")).resolves.toEqual(data);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/artifacts/artifact-1");
    expect(init.headers).toMatchObject({ Authorization: "Bearer read-secret" });
  });

  it("rejects an artifact response bound to a different identity", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-artifact-forged",
      data: { id: "artifact-other", run_id: "run-1" },
    }), { status: 200, headers: { "Content-Type": "application/json" } })));
    await expect(new APIClient("read-secret").getArtifact("artifact-1"))
      .rejects.toThrow("invalid");
  });

  it("reads a single note detail over the access token", async () => {
    const data = {
      id: "note-1", run_id: "run-1", owner: "root", owner_agent_id: "agent-1",
      category: "decision", visibility: "run", status: "active", pinned: true,
      title: "Decision", content: "body", tags: ["a"], source_refs: ["ref-1"],
      evidence_ids: ["ev-1"], version: 3, created_at: "2026-07-31T00:00:00Z",
      updated_at: "2026-07-31T01:00:00Z",
    };
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-note", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(new APIClient("read-secret").getNote("note-1")).resolves.toEqual(data);
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/notes/note-1");
  });

  it("reads a single work item detail over the access token", async () => {
    const data = {
      id: "work-1", run_id: "run-1", title: "Task", status: "in_progress",
      priority: "high", version: 2, acceptance_criteria: ["done"], dependencies: [],
      created_at: "2026-07-31T00:00:00Z", updated_at: "2026-07-31T01:00:00Z",
    };
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-work", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(new APIClient("read-secret").getWorkItem("work-1")).resolves.toEqual(data);
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/work-items/work-1");
  });

  it("reads the run external skill projection and binds it to the run", async () => {
    const data = {
      protocol_version: "external_skill_projection.v1", run_id: "run-1", skills: [],
    };
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-external-skills", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(new APIClient("read-secret").getRunExternalSkills("run-1"))
      .resolves.toEqual(data);
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/runs/run-1/external-skills");
  });

  it("rejects an external skill projection bound to a different run or protocol", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-external-skills-forged",
      data: { protocol_version: "external_skill_projection.v1", run_id: "run-other", skills: [] },
    }), { status: 200, headers: { "Content-Type": "application/json" } })));
    await expect(new APIClient("read-secret").getRunExternalSkills("run-1"))
      .rejects.toThrow("invalid");
  });

  it("validates redacted model availability without probing through the client", async () => {
    const data = {
      protocol_version: "model_availability.v2", generation: 1,
      providers: [{ name: "mock", kind: "local", status: "available", models: ["mock-code"],
        display_name: "mock", custom: false, enabled: true, definition_revision: 0,
        transport: "mock", search_mode: "disabled",
        native_web_search_capability: "unsupported",
        native_web_search_runtime_enabled: false,
        harnesses: [{ protocol_version: "model_harness.v1", model: "mock-code",
          transport_protocol: "mock", tool_strategy: "native", json_strategy: "native",
          qualification_status: "trusted_builtin", tool_calls_qualified: true,
          tool_results_qualified: true, strict_json_qualified: true,
          streaming_qualified: true, root_eligible: true,
          structured_json_eligible: true, latest_qualification_status: "",
          qualification_checked_at: "", qualification_source: "",
          qualified_at: "", expires_at: "" }],
        credential_source: "none", network_required: false, configuration_error: false }],
      routes: [{ name: "code", provider: "mock", model: "mock-code", available: true,
        harness_ready: true }],
    };
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-models", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(new APIClient("read-secret").modelAvailability()).resolves.toEqual(data);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/models");
    expect(init.method).toBe("GET");

    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-models-ollama",
      data: { ...data, providers: [...data.providers, { name: "ollama", kind: "ollama",
        status: "not_configured", models: ["llama3.2:3b"],
        display_name: "ollama", custom: false, enabled: true, definition_revision: 0,
        transport: "ollama_chat", search_mode: "disabled",
        native_web_search_capability: "unsupported",
        native_web_search_runtime_enabled: false,
        harnesses: [{ protocol_version: "model_harness.v1", model: "llama3.2:3b",
          transport_protocol: "ollama_chat", tool_strategy: "none", json_strategy: "none",
          qualification_status: "qualification_required", tool_calls_qualified: false,
          tool_results_qualified: false, strict_json_qualified: false,
          streaming_qualified: false, root_eligible: false,
          structured_json_eligible: false, latest_qualification_status: "not_configured",
          qualification_checked_at: "", qualification_source: "availability",
          qualified_at: "", expires_at: "" }],
        credential_source: "none", network_required: true, configuration_error: false }] },
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    await expect(new APIClient("read-secret").modelAvailability()).resolves.toEqual(
      expect.objectContaining({
        providers: expect.arrayContaining([
          expect.objectContaining({ name: "ollama", kind: "ollama",
            status: "not_configured" }),
        ]),
      }));

    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-models-bad-kind",
      data: { ...data, providers: [{ ...data.providers[0], kind: "lan_scan" }] },
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    await expect(new APIClient("read-secret").modelAvailability())
      .rejects.toThrow("invalid");

    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-models-forged",
      data: { ...data, providers: [{ ...data.providers[0], base_url: "https://private.invalid" }] },
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    await expect(new APIClient("read-secret").modelAvailability()).rejects.toThrow("invalid");

    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-models-unbound",
      data: { ...data, routes: [{ ...data.routes[0], provider: "missing" }] },
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    await expect(new APIClient("read-secret").modelAvailability()).rejects.toThrow("invalid");
  });

  it("keeps Plan entry, direction, and Deliver as independently validated controls", async () => {
    const plan = {
      version: "plan_delivery_control.v1", run_id: "run-1",
      applied_mode: { phase: "plan", capability_grant: false },
      current_mode: { phase: "plan", capability_grant: false }, replayed: false,
      execution_started: false, model_called: false, tool_called: false, capability_grant: false,
    };
    const direction = {
      version: "plan_delivery_control.v1", run_id: "run-1", proposal_id: "proposal-1",
      selection_id: "selection-1", note_id: "note-1", direction: 2, work_item_count: 1,
      replayed: false, phase_changed: false, execution_started: false, model_called: false,
      tool_called: false, capability_grant: false,
    };
    const delivery = {
      version: "plan_delivery_control.v1", run_id: "run-1", selection_id: "selection-1",
      applied_mode: { phase: "deliver", capability_grant: false },
      current_mode: { phase: "deliver", capability_grant: false }, replayed: false,
      execution_started: false, model_called: false, tool_called: false, capability_grant: false,
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-plan-enter", data: plan,
      }), { status: 202, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-plan-direction", data: direction,
      }), { status: 202, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-plan-deliver", data: delivery,
      }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: false, runCreationEnabled: false, sessionMessageEnabled: false,
      sessionSteeringControlEnabled: false, runLifecycleEnabled: false, runExecutionEnabled: false,
      planDeliveryControlEnabled: true, approvalControlEnabled: false,
    });
    expect(client.hasControl).toBe(false);
    expect(client.hasPlanDelivery).toBe(true);
    expect(client.hasApprovalControl).toBe(false);
    await expect(client.enterPlanMode("run-1", {
      version: "plan_delivery_control.v1",
    }, "web-plan-enter-operation-0001")).resolves.toEqual(plan);
    await expect(client.selectPlanDirection("run-1", {
      version: "plan_delivery_control.v1", proposal_id: "proposal-1", direction: 2,
    }, "web-plan-direction-operation-0001")).resolves.toEqual(direction);
    await expect(client.enterPlanDelivery("run-1", {
      version: "plan_delivery_control.v1",
    }, "web-plan-deliver-operation-0001")).resolves.toEqual(delivery);
    expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/v1/runs/run-1/plan/enter");
    expect(fetchMock.mock.calls[1]?.[0]).toBe("/api/v1/runs/run-1/plan/direction");
    expect(fetchMock.mock.calls[2]?.[0]).toBe("/api/v1/runs/run-1/plan/deliver");
  });

  it("binds approval previews to identity and actual effect before allowing a decision", async () => {
    const preview = { protocol_version: "approval_queue.v1", run_id: "run-1",
      approval_id: "approval-1", proposal_id: "proposal-1", workspace_id: "workspace-1",
      tool_name: "shell", effect: "dry_run", working_directory: ".",
      fields: [{ name: "command", value: "echo inspection" }], source_current: true,
      redacted: false, truncated: false };
    const respond = (data: unknown) => new Response(JSON.stringify({
      version: "api.v1", request_id: "request-preview", data }), {
      status: 200, headers: { "Content-Type": "application/json" } });
    const fetchMock = vi.fn().mockResolvedValueOnce(respond(preview));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret");
    await expect(client.approvalPreview("run-1", "approval-1")).resolves.toEqual(preview);
    for (const drift of [{ run_id: "run-other" }, { approval_id: "another" },
      { effect: "fetch_public_https" }, { truncated: true }, { capability_grant: true }]) {
      fetchMock.mockResolvedValueOnce(respond({ ...preview, ...drift }));
      await expect(client.approvalPreview("run-1", "approval-1")).rejects.toThrow();
    }
  });

  it("observes the original Session message with a read-only request and the read bearer", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-session-observe",
      data: { version: "session_message_submission.v1", session_id: "sess-1",
        state: "received", message_id: "steer-1", message_status: "pending" },
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      sessionMessageEnabled: true,
    });
    const observed = await client.inspectSessionMessageOperation("sess-1", "web-session-observe-0001");
    expect(observed).toMatchObject({ state: "received", message_id: "steer-1" });
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/sessions/sess-1/messages/operations/web-session-observe-0001");
    expect(init.method).toBe("GET");
    expect(init.body).toBeUndefined();
    expect(init.headers).toMatchObject({ Authorization: "Bearer read-secret" });
  });

  it("accepts exact file approval previews for all four edit operations without granting an effect", async () => {
    // Shape captured from the real package.json create proposal; no mutation is performed.
    const preview = { protocol_version: "approval_queue.v1", run_id: "run-20260910064350-a4f973a7ede3",
      approval_id: "approval-20260910072057-6583f54ed917", proposal_id: "edit-99e4b9e0fc83e37efdda6745e8835851",
      tool_name: "create_file", workspace_id: "ws-import-ddd63ce66f99ecc63b9ff3c8",
      effect: "file_review_required", working_directory: ".", fields: [
        { name: "operation", value: "create" }, { name: "path", value: "package.json" }],
      source_current: true, redacted: false, truncated: false };
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret");
    const respond = (data: unknown) => new Response(JSON.stringify({ version: "api.v1", request_id: "request-preview", data }),
      { status: 200, headers: { "Content-Type": "application/json" } });
    for (const operation of ["create", "replace", "move", "delete"]) {
      const value = { ...preview, tool_name: `${operation}_file`, fields: [
        { name: "operation", value: operation }, { name: "path", value: "package.json" }] };
      fetchMock.mockResolvedValueOnce(respond(value));
      await expect(client.approvalPreview(value.run_id, value.approval_id)).resolves.toEqual(value);
      fetchMock.mockResolvedValueOnce(respond({ ...value, effect: "dry_run" }));
      await expect(client.approvalPreview(value.run_id, value.approval_id)).rejects.toThrow("different effect");
    }
    for (const drift of [{ run_id: "another-run" }, { approval_id: "another-approval" },
      { capability_grant: true }, { effect: "fetch_public_https" }, { tool_name: "unknown_file" }]) {
      fetchMock.mockResolvedValueOnce(respond({ ...preview, ...drift }));
      await expect(client.approvalPreview(preview.run_id, preview.approval_id)).rejects.toThrow();
    }
  });

  it("validates a metadata-only approval queue and closed approve-once response", async () => {
    const queue = {
      protocol_version: "approval_queue.v1", run_id: "run-1", truncated: false,
      process_execution_enabled: false, session_grant_created: false, capability_grant: false,
      items: [{ id: "approval-1", proposal_id: "proposal-1", run_id: "run-1",
        session_id: "session-1", workspace_id: "workspace-1", tool_name: "shell",
        action_class: "shell", mode: "per_call", status: "pending",
        allowed_actions: ["approve_once", "deny"], version: 1,
        created_at: "2026-07-18T00:00:00Z", updated_at: "2026-07-18T00:00:00Z",
        process_execution_enabled: false, capability_grant: false }],
    };
    const decision = {
      version: "approval_control.v1", run_id: "run-1", approval_id: "approval-1",
      proposal_id: "proposal-1", tool_name: "shell", action: "approve_once",
      status: "approved", replayed: false, process_execution_enabled: false,
      shell_execution_enabled: false, docker_execution_enabled: false,
      workspace_write_applied: false, session_grant_created: false, capability_grant: false,
      execution_resumed: false, retry_completed: false, retry_scheduled: false,
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-approval-queue", data: queue,
      }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-approval-decision", data: decision,
      }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: false, approvalControlEnabled: true,
    });
    await expect(client.approvalQueue("run-1")).resolves.toEqual(queue);
    await expect(client.decideApproval("run-1", "approval-1", {
      version: "approval_control.v1", action: "approve_once",
    }, "web-approval-operation-0001")).resolves.toEqual(decision);
    expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/v1/runs/run-1/approvals");
    expect(fetchMock.mock.calls[1]?.[0]).toBe("/api/v1/runs/run-1/approvals/approval-1/decision");
    const decisionInit = fetchMock.mock.calls[1]?.[1] as RequestInit;
    expect(decisionInit.headers).toMatchObject({ Authorization: "Bearer control-secret" });

    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-approval-unbound",
      data: { ...queue, items: [{ ...queue.items[0], session_id: "" }] },
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    await expect(client.approvalQueue("run-1")).rejects.toThrow("invalid");
  });

  it("accepts a source-bound web fetch approval and conversation-scoped continuation", async () => {
    const item = { id: "approval-web-1", proposal_id: "web-fetch-authorization-1",
      run_id: "run-1", session_id: "session-1", workspace_id: "",
      tool_name: "web_fetch", action_class: "public_https_fetch", mode: "per_call",
      status: "pending", allowed_actions: ["approve_once", "approve_for_thread", "deny"],
      canonical_url: "https://arxiv.org/abs/2608.13637", exact_target: "arxiv.org",
      version: 1, created_at: "2026-09-02T00:00:00Z",
      updated_at: "2026-09-02T00:00:00Z", process_execution_enabled: false,
      capability_grant: false };
    const queue = { protocol_version: "approval_queue.v1", run_id: "run-1",
      truncated: false, process_execution_enabled: false, session_grant_created: false,
      capability_grant: false, items: [item] };
    const decision = { version: "approval_control.v1", run_id: "run-1",
      approval_id: item.id, proposal_id: item.proposal_id, tool_name: "web_fetch",
      action: "approve_for_thread", status: "approved", replayed: false,
      process_execution_enabled: false, shell_execution_enabled: false,
      docker_execution_enabled: false, workspace_write_applied: false,
      session_grant_created: false, capability_grant: false,
      execution_resumed: false, retry_completed: false, retry_scheduled: true };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-web-approval-queue", data: queue }),
      { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-web-approval-decision", data: decision }),
      { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: false, approvalControlEnabled: true,
    });

    await expect(client.approvalQueue("run-1")).resolves.toEqual(queue);
    await expect(client.decideApproval("run-1", item.id, {
      version: "approval_control.v1", action: "approve_for_thread",
    }, "web-fetch-thread-approval-0001")).resolves.toEqual(decision);
    const init = fetchMock.mock.calls[1]?.[1] as RequestInit;
    expect(init.headers).toMatchObject({
      Authorization: "Bearer control-secret",
      "Idempotency-Key": "web-fetch-thread-approval-0001",
    });

    const unscheduledDecision = { ...decision, action: "deny", status: "denied",
      retry_scheduled: false };
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
      request_id: "req-web-approval-unscheduled", data: unscheduledDecision }),
    { status: 202, headers: { "Content-Type": "application/json" } }));
    await expect(client.decideApproval("run-1", item.id, {
      version: "approval_control.v1", action: "deny",
    }, "web-fetch-unscheduled-approval-0001")).resolves.toEqual(unscheduledDecision);

    const recoveryQueue = { ...queue, items: [{ ...item, status: "approved",
      allowed_actions: ["approve_for_thread"] }] };
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
      request_id: "req-web-approval-recovery", data: recoveryQueue }),
    { status: 200, headers: { "Content-Type": "application/json" } }));
    await expect(client.approvalQueue("run-1")).resolves.toEqual(recoveryQueue);
  });

  it("reviews only fixed Go command proposals and rejects instruction-bearing evidence", async () => {
    const pending = {
      id: "command-proposal-1", protocol_version: "controlled_command_proposal.v1",
      policy_version: "controlled_command_proposal_policy.v1", run_id: "run-1",
      mission_id: "mission-1", session_id: "session-1", workspace_id: "workspace-1",
      kind: "git-status", timeout_milliseconds: 30_000,
      purpose: "Inspect the current repository state", permission_mode: "conservative",
      permission_revision: 1, operator_review_required: true,
      instruction_authorized: false, execution_authorized: false, capability_grant: false,
      fingerprint: "a".repeat(64), created_at: "2026-07-29T00:00:00Z",
      evidence_instruction_authorized: false,
    };
    const reviewed = {
      ...pending,
      review: { id: "review-1", decision: "approve", reviewed_by: "http_control_operator",
        reason: "Operator approved the exact fixed Go command",
        single_use_execution_authorized: true, capability_grant: false,
        created_at: "2026-07-29T00:01:00Z" },
      result: { id: "result-1", status: "completed", source_kind: "go_command_result",
        source_ref: "session-message-1", content_sha256: "b".repeat(64),
        instruction_authorized: false, raw_output_persisted: false,
        automatic_retry_allowed: false, created_at: "2026-07-29T00:01:01Z" },
      receipt: {
        request_id: "controlled-exec-0001", backend: "windows-controlled-v1", exit_code: 0,
        stdout_observed_bytes: 6, stdout_captured_bytes: 6,
        stdout_prefix_sha256: "c".repeat(64), stdout_truncated: false,
        stderr_observed_bytes: 0, stderr_captured_bytes: 0,
        stderr_prefix_sha256: "d".repeat(64), stderr_truncated: false,
        started_at: "2026-07-29T00:01:00Z", completed_at: "2026-07-29T00:01:01Z",
        timed_out: false, cancelled: false, output_limit_exceeded: false, tree_reaped: true,
        restricted_token: true, low_integrity_token: true, job_assigned_at_creation: true,
        kill_on_job_close: true, active_process_limit: 1,
        process_memory_limit: 512 * 1024 * 1024, stdin_closed: true,
        environment_inherited: false, network_requested: false, persistent_process: false,
        product_execution_enabled: true,
      },
      review_replayed: false, execution_replayed: false,
      untrusted_evidence: "UNTRUSTED GO COMMAND RESULT\nstdout_begin\nclean\nstdout_end",
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "request-proposals", data: [pending],
        page: { limit: 100 },
      }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "request-review", data: reviewed,
      }), { status: 202, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "request-invalid-result",
        data: { ...reviewed, evidence_instruction_authorized: true },
      }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: false,
    });

    await expect(client.controlledCommandProposals("run-1"))
      .resolves.toMatchObject({ items: [pending], requestID: "request-proposals" });
    await expect(client.controlledCommandProposal(
      "run-1", "command-proposal-1",
    )).resolves.toEqual(reviewed);
    expect(fetchMock.mock.calls[0]?.[0])
      .toBe("/api/v1/runs/run-1/command-proposals?limit=100");
    expect(fetchMock.mock.calls[1]?.[0])
      .toBe("/api/v1/runs/run-1/command-proposals/command-proposal-1");
    const reviewInit = fetchMock.mock.calls[1]?.[1] as RequestInit;
    expect(reviewInit.headers).toMatchObject({ Authorization: "Bearer read-secret" });
    expect(reviewInit.body).toBeUndefined();
    await expect(client.controlledCommandProposal(
      "run-1", "command-proposal-1",
    )).rejects.toThrow("invalid");
  });

  it("reads exact historical host evidence and resumes without new execution", async () => {
    const pending = {
      id: "host-command-proposal-1", protocol_version: "host_command_proposal.v1",
      policy_version: "host_command_policy.v1", run_id: "run-1",
      mission_id: "mission-1", session_id: "session-1", workspace_id: "workspace-1",
      executable_path: "C:\\Program Files\\Go\\bin\\go.exe",
      executable_sha256: "a".repeat(64), argv: ["test", "./internal/application"],
      working_directory: "D:\\GitProjects\\Prayu",
      environment_policy: "sanitized_host_environment.v1",
      environment_keys: ["PATH", "SYSTEMROOT"], environment_sha256: "b".repeat(64),
      network_intent: "host", timeout_milliseconds: 120_000,
      purpose: "Run focused application tests", spec_fingerprint: "c".repeat(64),
      permission_mode: "approval", permission_revision: 3,
      operator_review_required: true, non_sandboxed: true,
      automatic_retry_allowed: false, instruction_authorized: false,
      execution_authorized: false, capability_grant: false,
      fingerprint: "d".repeat(64), created_at: "2026-08-09T00:00:00Z",
      evidence_instruction_authorized: false,
    };
    const reviewed = {
      ...pending,
      review: { id: "review-1", decision: "approve", reviewed_by: "http_control_operator",
        reason: "Operator verified the exact host command",
        single_use_execution_authorized: true, capability_grant: false,
        created_at: "2026-08-09T00:01:00Z" },
      result: { id: "result-1", status: "completed", source_kind: "go_command_result",
        source_ref: "session-message-1", content_sha256: "e".repeat(64),
        instruction_authorized: false, raw_output_persisted: false,
        automatic_retry_allowed: false, created_at: "2026-08-09T00:01:01Z" },
      receipt: {
        request_id: "host-exec-0001", backend: "windows-host-job-v1", exit_code: 0,
        stdout_observed_bytes: 2, stdout_captured_bytes: 2,
        stdout_prefix_sha256: "f".repeat(64), stdout_truncated: false,
        stderr_observed_bytes: 0, stderr_captured_bytes: 0,
        stderr_prefix_sha256: "0".repeat(64), stderr_truncated: false,
        started_at: "2026-08-09T00:01:00Z", completed_at: "2026-08-09T00:01:01Z",
        timed_out: false, cancelled: false, output_limit_exceeded: false, tree_reaped: true,
        non_sandboxed: true, restricted_token: false, low_integrity_token: false,
        job_assigned_at_creation: true, kill_on_job_close: true, active_process_limit: 32,
        job_memory_limit: 2 * 1024 * 1024 * 1024, stdin_closed: true,
        environment_inherited: false, network_requested: true, persistent_process: false,
        product_execution_enabled: true,
      },
      review_replayed: false, execution_replayed: false,
      untrusted_evidence: "UNTRUSTED HOST COMMAND RESULT\nstdout_begin\nok\nstdout_end",
      continuation: { state: "failed", replayed: false, model_called: true, tool_called: false,
        handoff_id: "handoff-after-review", error_code: "FAILED_PRECONDITION", message: "Subsequent model step failed" },
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "request-host-proposals", data: [pending],
        page: { limit: 100 },
      }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "request-host-review", data: reviewed,
      }), { status: 202, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "request-widened-host-review",
        data: { ...reviewed, receipt: { ...reviewed.receipt, persistent_process: true } },
      }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: false, operatorApprovalEnabled: true,

    });

    await expect(client.hostCommandProposals("run-1"))
      .resolves.toMatchObject({ items: [pending], requestID: "request-host-proposals" });
    await expect(client.resumeHostCommandProposal(
      "run-1", "host-command-proposal-1",
    )).resolves.toEqual(reviewed);
    expect(fetchMock.mock.calls[0]?.[0])
      .toBe("/api/v1/runs/run-1/host-command-proposals?limit=100");
    expect(fetchMock.mock.calls[1]?.[0])
      .toBe("/api/v1/runs/run-1/host-command-proposals/host-command-proposal-1/resume");
    const reviewInit = fetchMock.mock.calls[1]?.[1] as RequestInit;
    expect(reviewInit.headers).toMatchObject({ Authorization: "Bearer control-secret" });
    expect(reviewInit.body).toBeUndefined();
    await expect(client.hostCommandProposal(
      "run-1", "host-command-proposal-1",
    )).rejects.toThrow("boundary");
    const readOnly = new APIClient("read-secret", "/api/v1", "", {
       operatorApprovalEnabled: false,
    });
    const unknown = { ...pending, uncertain: true, review: reviewed.review };
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
      request_id: "history-unknown", data: unknown }), { status: 200 }));
    await expect(readOnly.hostCommandProposal("run-1", pending.id)).resolves.toEqual(unknown);
    expect((fetchMock.mock.calls.at(-1)?.[1] as RequestInit).headers)
      .toMatchObject({ Authorization: "Bearer read-secret" });
    const reads = fetchMock.mock.calls.length;
    await expect(readOnly.resumeHostCommandProposal("run-1", pending.id)).rejects.toThrow("control");
    expect(fetchMock).toHaveBeenCalledTimes(reads);
    for (const uncertain of ["true", 1]) {
      fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "history-invalid-unknown", data: { ...unknown, uncertain } }), { status: 200 }));
      await expect(readOnly.hostCommandProposal("run-1", pending.id)).rejects.toThrow("invalid");
    }
    for (const continuation of [
      { ...reviewed.continuation, state: "unknown" },
      { ...reviewed.continuation, model_called: undefined },
      { ...reviewed.continuation, execution_authorized: true },
    ]) {
      fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "invalid-continuation", data: { ...reviewed, continuation } }), { status: 200 }));
      await expect(client.hostCommandProposal("run-1", "host-command-proposal-1")).rejects.toThrow("continuation");
    }
    const savedOutput = {
      result_id: reviewed.result.id, request_id: reviewed.receipt.request_id,
      stdout: { text: "中文\nstdout_end\nstderr_begin", utf8_bytes: 30, truncated: false, redacted: true },
      stderr: { text: "", utf8_bytes: 0, truncated: false, redacted: true },
    };
    savedOutput.stdout.utf8_bytes = new TextEncoder().encode(savedOutput.stdout.text).byteLength;
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
      request_id: "saved-output", data: { ...reviewed, saved_output: savedOutput } }), { status: 200 }));
    await expect(client.hostCommandProposal("run-1", pending.id))
      .resolves.toMatchObject({ saved_output: savedOutput });
    for (const saved_output of [
      null, { ...savedOutput, result_id: "another-result" },
      { ...savedOutput, request_id: "another-request" },
      { ...savedOutput, stdout: { ...savedOutput.stdout, utf8_bytes: 1 } },
      { ...savedOutput, stdout: { ...savedOutput.stdout, redacted: false } },
      { ...savedOutput, stdout: { ...savedOutput.stdout, raw: "unreviewed" } },
      { ...savedOutput, stdout: { ...savedOutput.stdout, truncated: "false" } },
      { ...savedOutput, stdout: { ...savedOutput.stdout, text: "a".repeat(16384), utf8_bytes: 16384 },
        stderr: { ...savedOutput.stderr, text: "b", utf8_bytes: 1 } },
    ]) {
      fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "invalid-saved-output", data: { ...reviewed, saved_output } }), { status: 200 }));
      await expect(client.hostCommandProposal("run-1", pending.id)).rejects.toThrow("saved output");
    }
  });

  it("reads historical risk state and resumes without granting authority", async () => {
    const pending = {
      id: "risk-escalation-0001", protocol_version: "risk_escalation.v1",
      policy_version: "risk_escalation_policy.v1", run_id: "run-1",
      mission_id: "mission-1", session_id: "session-1", workspace_id: "workspace-1",
      executable_path: "C:\\Program Files\\Go\\bin\\go.exe",
      executable_sha256: "a".repeat(64), argv: ["test", "./internal/application"],
      working_directory: "D:\\GitProjects\\Prayu",
      environment_policy: "sanitized_host_environment.v1", environment_keys: ["PATH"],
      environment_sha256: "b".repeat(64), network_intent: "host",
      timeout_milliseconds: 120_000, purpose: "Download a declared module and verify it",
      spec_fingerprint: "c".repeat(64), permission_mode: "workspace_access",
      permission_revision: 4, operator_review_required: true, non_sandboxed: true,
      automatic_retry_allowed: false, instruction_authorized: false,
      execution_authorized: false, capability_grant: false, fingerprint: "d".repeat(64),
      created_at: "2026-08-09T00:00:00Z", evidence_instruction_authorized: false,
      state: "waiting_approval", supervisor_turn: 2, supervisor_tool_call_id: "call-1",
      tool_invocation_id: "invocation-1", mode_snapshot_id: "mode-1", mode_revision: 2,
      interaction_snapshot_id: "interaction-1", interaction_revision: 3,
      execution_profile_snapshot_id: "profile-1", execution_profile_revision: 3,
      permission_snapshot_id: "permission-1", workspace_root_fingerprint: "e".repeat(64),
      capability_generation: "f".repeat(64), scope_fingerprint: "1".repeat(64),
      risk_kinds: ["credential", "host_path", "network", "policy_denial"],
      network_targets: ["proxy.golang.org:443"], network_purpose: "Download declared module",
      credential_kinds: ["system_proxy"], host_paths: ["C:\\ProgramData\\go\\cache"],
      policy_code: "workspace_network_denied", policy_reason: "Workspace network is denied",
      max_output_bytes: 64 * 1024 * 1024, active_process_limit: 32,
      process_memory_bytes: 2 * 1024 * 1024 * 1024,
      approval_id: "approval-1", approval_status: "pending",
    };
    const reviewed = {
      ...pending, state: "completed", operator_review_required: false,
      execution_authorized: true, capability_grant: true, approval_status: "approved",
      grant_id: "grant-1", grant_generation: 1, grant_max_uses: 1,
      grant_uses_remaining: 0, grant_expires_at: "2026-08-09T00:05:00Z",
      grant_consumption_id: "consumption-1",
      review: { id: "approval-1", decision: "approve", reviewed_by: "http_control_operator",
        reason: "Operator approved bounded exact scope", single_use_execution_authorized: false,
        capability_grant: true, created_at: "2026-08-09T00:01:00Z" },
      result: { id: "result-1", status: "completed", source_kind: "go_command_result",
        source_ref: "session-message-1", content_sha256: "2".repeat(64),
        instruction_authorized: false, raw_output_persisted: false,
        automatic_retry_allowed: false, created_at: "2026-08-09T00:01:01Z" },
      receipt: {
        request_id: "risk-exec-0001", backend: "windows-host-job-v1", exit_code: 0,
        stdout_observed_bytes: 2, stdout_captured_bytes: 2,
        stdout_prefix_sha256: "3".repeat(64), stdout_truncated: false,
        stderr_observed_bytes: 0, stderr_captured_bytes: 0,
        stderr_prefix_sha256: "0".repeat(64), stderr_truncated: false,
        started_at: "2026-08-09T00:01:00Z", completed_at: "2026-08-09T00:01:01Z",
        timed_out: false, cancelled: false, output_limit_exceeded: false, tree_reaped: true,
        non_sandboxed: true, restricted_token: false, low_integrity_token: false,
        job_assigned_at_creation: true, kill_on_job_close: true, active_process_limit: 32,
        job_memory_limit: 2 * 1024 * 1024 * 1024, stdin_closed: true,
        environment_inherited: false, network_requested: true, persistent_process: false,
        product_execution_enabled: true,
      },
      untrusted_evidence: "UNTRUSTED APPROVED RISK ESCALATION RESULT\nstdout_begin\nok\nstdout_end",
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "request-risk-proposals", data: [pending],
        page: { limit: 100 },
      }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "request-risk-review", data: reviewed,
      }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: false, operatorApprovalEnabled: true,

    });

    await expect(client.hostCommandProposals("run-1"))
      .resolves.toMatchObject({ items: [pending], requestID: "request-risk-proposals" });
    await expect(client.resumeHostCommandProposal("run-1", "risk-escalation-0001")).resolves.toEqual(reviewed);
    const init = fetchMock.mock.calls[1]?.[1] as RequestInit;
    expect(init.body).toBeUndefined();
    expect(init.method).toBe("POST");
    expect(init.headers).toMatchObject({ Authorization: "Bearer control-secret" });
    expect(fetchMock.mock.calls[1]?.[0]).toBe("/api/v1/runs/run-1/host-command-proposals/risk-escalation-0001/resume");
    await expect(client.resumeHostCommandProposal(" run-1", "risk-escalation-0001")).rejects.toThrow("exact identities");

  });

  it("validates content-free model diagnostics and exact persisted routes", async () => {
    const route = { name: "code", provider: "mock", model: "mock-code", available: true,
      harness_ready: true };
    const diagnostic = {
      protocol_version: "provider_diagnostic.v1", provider: "mock", model: "mock-code",
      status: "reachable", outcome: "success", failure_reason: "none", retryable: false,
      network_request_attempted: false, model_called: true, tool_called: false,
      response_content_returned: false, qualification_status: "available",
      duration_ms: 2,
    };
    const qualification = {
      protocol_version: "model_harness_qualification.v1", provider: "mock",
      model: "mock-code", status: "qualified", outcome: "success", failure_reason: "none",
      retryable: false,
      network_request_attempted: false, model_calls: 0, synthetic_tool_calls: 0,
      tool_executed: false, response_content_returned: false,
      qualification_status: "available", duration_ms: 0,
      harness: {
        protocol_version: "model_harness.v1", model: "mock-code",
        transport_protocol: "mock", tool_strategy: "native", json_strategy: "native",
        qualification_status: "trusted_builtin", tool_calls_qualified: true,
        tool_results_qualified: true, strict_json_qualified: true,
        streaming_qualified: true, root_eligible: true,
        structured_json_eligible: true, latest_qualification_status: "available",
        qualification_checked_at: "2026-07-18T00:00:00Z",
        qualification_source: "harness_qualification",
        qualified_at: "", expires_at: "",
      },
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-route", data: route,
      }), { status: 202, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-diagnostic", data: diagnostic,
      }), { status: 202, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-qualification", data: qualification,
      }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: false, modelControlEnabled: true,
    });
    await expect(client.selectModelRoute("code", {
      version: "model_route_control.v1", provider: "mock", model: "mock-code",
    })).resolves.toEqual(route);
    await expect(client.diagnoseProvider({
      version: "provider_diagnostic.v1", provider: "mock", model: "mock-code",
      confirm_diagnostic: true,
    })).resolves.toEqual(diagnostic);
    await expect(client.qualifyModelHarness({
      version: "model_harness_qualification.v1", provider: "mock", model: "mock-code",
      confirm_qualification: true,
    })).resolves.toEqual(qualification);
    expect((fetchMock.mock.calls[0]?.[1] as RequestInit).headers)
      .not.toHaveProperty("Idempotency-Key");

    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-diagnostic-forged",
      data: { ...diagnostic, response_content_returned: true, response: "private" },
    }), { status: 202, headers: { "Content-Type": "application/json" } }));
    await expect(client.diagnoseProvider({
      version: "provider_diagnostic.v1", provider: "mock", model: "mock-code",
      confirm_diagnostic: true,
    })).rejects.toThrow("content-free");
  });

  it("lists bounded model routes and controls a Thread route through GET and PUT", async () => {
    const catalog = {
      protocol_version: "model_route_catalog.v1", generation: 9,
      routes: [{ provider_id: "deepseek", provider_name: "DeepSeek", model: "deepseek-chat",
		definition_revision: 0,
        enabled: true, credential_status: "configured", qualification_status: "verified",
        harness_ready: true, selectable: true, unavailable_reason: "",
        default_for_routes: ["code"] }],
    };
    const current = {
      protocol_version: "thread_model_route.v1", thread_id: "thread-1",
      provider: "deepseek", model: "deepseek-chat", source: "active_run",
      effective_run_id: "run-1", applies_to: "current_and_next",
      active_run_unchanged: false, replayed: false,
    };
    const selected = {
      protocol_version: "thread_model_route.v1", thread_id: "thread-1",
      provider: "openai", model: "gpt-5-mini", source: "thread_preference",
      applies_to: "next_run", active_run_unchanged: true, replayed: false,
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-route-catalog", data: catalog }), { status: 200,
        headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-thread-route", data: current }), { status: 200,
        headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-thread-route-select", data: selected }), { status: 200,
        headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      modelControlEnabled: true,
    });

    await expect(client.availableModelRoutes()).resolves.toEqual(catalog);
    await expect(client.threadModelRoute("thread-1")).resolves.toEqual(current);
    await expect(client.selectThreadModelRoute("thread-1", {
      version: "thread_model_route_control.v1", action: "select", provider: "openai",
      model: "gpt-5-mini", operation_key: "route-operation-0001",
      requested_by: "desktop-ui",
    })).resolves.toEqual(selected);

    expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/v1/models/routes/available");
    expect(fetchMock.mock.calls[1]?.[0]).toBe("/api/v1/threads/thread-1/model-route");
    const [putURL, putInit] = fetchMock.mock.calls[2] as [string, RequestInit];
    expect(putURL).toBe("/api/v1/threads/thread-1/model-route");
    expect(putInit.method).toBe("PUT");
    expect(putInit.headers).toMatchObject({ Authorization: "Bearer control-secret" });
    expect(putInit.headers).not.toHaveProperty("Idempotency-Key");
  });

  it("fails closed for contradictory model catalogs and rebound Thread routes", async () => {
    const invalidCatalog = {
      protocol_version: "model_route_catalog.v1", generation: 1,
      routes: [{ provider_id: "deepseek", provider_name: "DeepSeek", model: "deepseek-chat",
		definition_revision: 0,
        enabled: true, credential_status: "configured", qualification_status: "verified",
        harness_ready: true, selectable: true, unavailable_reason: "credential_not_configured",
        default_for_routes: [] }],
    };
    const rebound = {
      protocol_version: "thread_model_route.v1", thread_id: "thread-other",
      provider: "deepseek", model: "deepseek-chat", source: "thread_preference",
      applies_to: "next_run", active_run_unchanged: true, replayed: false,
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-route-catalog-invalid", data: invalidCatalog }), { status: 200,
        headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-thread-route-rebound", data: rebound }), { status: 200,
        headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      modelControlEnabled: true,
    });

    await expect(client.availableModelRoutes()).rejects.toThrow("eligibility contract");
    await expect(client.threadModelRoute("thread-1")).rejects.toThrow("Thread model route response");
    await expect(client.selectThreadModelRoute("thread-1", {
      version: "thread_model_route_control.v1", action: "reset", provider: "deepseek",
      operation_key: "route-operation-0002", requested_by: "desktop-ui",
    })).rejects.toThrow("exact Thread model route intent");
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it.each(["provider_native", "web"])("reads a fail-closed %s search readiness projection", async (searchPolicy) => {
    const readiness = {
      protocol_version: "provider_search_readiness.v1", thread_id: "thread-1",
      run_id: "run-1", model_route: "deepseek/deepseek-chat", provider: "deepseek",
      model: "deepseek-chat", search_policy: searchPolicy,
      state: "missing_allowlist", reason: "search_endpoint_not_allowlisted",
      remediation: "add_required_target", required_target: searchPolicy === "web" ? "html.duckduckgo.com" : "api.deepseek.com",
      network_mode: "allowlist", mode_revision: 4, runtime_ready: false,
      capability_grant: false,
    };
    const fetchMock = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-search-readiness", data: readiness,
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret");

    await expect(client.providerSearchReadiness("thread-1")).resolves.toEqual(readiness);
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/threads/thread-1/search-readiness",
      expect.objectContaining({ method: "GET" }),
    );
  });

  it.each(["provider_native", "web"])("rejects contradictory or authority-bearing %s search readiness", async (searchPolicy) => {
    const invalid = {
      protocol_version: "provider_search_readiness.v1", thread_id: "thread-1",
      run_id: "run-1", model_route: "deepseek/deepseek-chat", provider: "deepseek",
      model: "deepseek-chat", search_policy: searchPolicy, state: "ready",
      reason: "search_backend_ready", remediation: "add_required_target",
      required_target: "api.deepseek.com/path", network_mode: "allowlist", mode_revision: 4,
      runtime_ready: false, capability_grant: true,
    };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-search-readiness-invalid", data: invalid,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));
    const client = new APIClient("read-secret");

    await expect(client.providerSearchReadiness("thread-1"))
      .rejects.toThrow("Provider search readiness response is invalid");
  });

  it("reads one exact redacted file Diff without loading the edit queue", async () => {
    const edit = { id: "edit-1", session_id: "session-1", workspace_id: "workspace-1",
      path: "README.md", operation: "replace", status: "applied",
      diff: "--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-old\n+new\n",
      original_hash: "a".repeat(64), proposed_hash: "b".repeat(64),
      secrets_redacted: true, allowed_actions: [], created_at: "2026-07-18T00:00:00Z",
      updated_at: "2026-07-18T00:00:01Z", apply_enabled: false };
    const response = (data: unknown) => new Response(JSON.stringify({
      version: "api.v1", request_id: "req-exact-edit", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(response(edit))
      .mockResolvedValueOnce(response({ ...edit, id: "edit-other" }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret");

    await expect(client.fileEdit("run-1", "edit-1")).resolves.toEqual(edit);
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe(
      "/api/v1/runs/run-1/file-edits/edit-1");
    expect(fetchMock.mock.calls[0]?.[1]).toEqual(expect.objectContaining({ method: "GET" }));
    await expect(client.fileEdit("run-1", "edit-1"))
      .rejects.toThrow("wrong identity");
  });

  it("rejects FileEdit body leakage and validates review-only decisions", async () => {
    const edit = { id: "edit-1", session_id: "session-1", workspace_id: "workspace-1",
      path: "README.md", operation: "replace", status: "proposed", diff: "--- a/README.md\n+++ b/README.md\n",
      original_hash: "missing", proposed_hash: "a".repeat(64), secrets_redacted: false,
      allowed_actions: ["approve_intent", "deny"], created_at: "2026-07-18T00:00:00Z",
      updated_at: "2026-07-18T00:00:00Z", apply_enabled: false };
    const queue = { protocol_version: "file_edit_review.v1", run_id: "run-1",
      items: [edit], truncated: false, apply_enabled: false };
    const decided = { protocol_version: "file_edit_review.v1", run_id: "run-1",
      action: "approve_intent", edit: { ...edit, status: "approved", allowed_actions: [] },
      replayed: false, file_written: false };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-edits", data: queue,
      }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-edit-review", data: decided,
      }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: false, fileEditReviewEnabled: true,
    });
    await expect(client.fileEditQueue("run-1")).resolves.toEqual(queue);
    await expect(client.reviewFileEdit("run-1", "edit-1", {
      version: "file_edit_review.v1", action: "approve_intent",
    })).resolves.toEqual(decided);

    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-edit-leak",
      data: { ...queue, items: [{ ...edit, proposed_text: "private body" }] },
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    await expect(client.fileEditQueue("run-1")).rejects.toThrow("metadata-only");

    const continuation = { state: "completed", replayed: false, model_called: true,
      tool_called: true, handoff_id: "handoff-after-review" };
    const reviewResponse = (data: unknown) => new Response(JSON.stringify({ version: "api.v1",
      request_id: "req-review-continuation", data }), { status: 200 });
    for (const next of [
      continuation,
      { state: "not_started", replayed: false, model_called: false, tool_called: false },
      { state: "queued", replayed: true, model_called: false, tool_called: false },
      { state: "failed", replayed: false, model_called: false, tool_called: false,
        error_code: "UNAVAILABLE", message: "Continuation unavailable" },
    ]) {
      fetchMock.mockResolvedValueOnce(reviewResponse({ ...decided, continuation: next }));
      await expect(client.reviewFileEdit("run-1", "edit-1", {
        version: "file_edit_review.v1", action: "approve_intent",
      })).resolves.toMatchObject({ file_written: false, edit: { status: "approved" }, continuation: next });
    }
    for (const invalid of [
      { ...decided, continuation: { ...continuation, state: "queued" } },
      { ...decided, continuation: { ...continuation, state: ["completed"] } },
      { ...decided, continuation: { ...continuation, tool_called: undefined } },
      { ...decided, continuation: { ...continuation, future_authority: true } },
      { ...decided, continuation: { ...continuation, handoff_id: undefined } },
      { ...decided, continuation, file_written: true },
      { ...decided, continuation, edit: { ...decided.edit, status: "applied" } },
    ]) {
      fetchMock.mockResolvedValueOnce(reviewResponse(invalid));
      await expect(client.reviewFileEdit("run-1", "edit-1", {
        version: "file_edit_review.v1", action: "approve_intent",
      })).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    }
  });

  it("validates a multi-file change set without accepting batch authority", async () => {
    const item = { id: "edit-1", path: "README.md", operation: "replace",
      status: "proposed", diff_bytes: 42,
      secrets_redacted: false, allowed_actions: ["approve_intent", "deny"],
      apply_enabled: false, updated_at: "2026-07-18T00:00:00Z" };
    const changeSet = { protocol_version: "file_edit_change_set.v1", run_id: "run-1",
      session_id: "session-1", workspace_id: "workspace-1", items: [item],
      proposed_count: 1, approved_count: 0, applied_count: 0, denied_count: 0,
      failed_count: 0, returned_count: 1, total_diff_bytes: 42, truncated: false,
      review_independent: true, apply_independent: true, atomic_apply: false,
      batch_mutation_supported: false, partial_apply_visible: true,
      diff_content_included: false };
    const envelope = (data: unknown) => new Response(JSON.stringify({
      version: "api.v1", request_id: "req-change-set", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(envelope(changeSet))
      .mockResolvedValueOnce(envelope({ ...changeSet, batch_mutation_supported: true }))
      .mockResolvedValueOnce(envelope({ ...changeSet, applied_count: 1 }))
      .mockResolvedValueOnce(envelope({ ...changeSet, items: [
        { ...item, allowed_actions: [] },
      ] }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret");

    await expect(client.fileEditChangeSet("run-1")).resolves.toEqual(changeSet);
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe(
      "/api/v1/runs/run-1/file-edit-change-set");
    await expect(client.fileEditChangeSet("run-1"))
      .rejects.toThrow("batch mutation authority");
    await expect(client.fileEditChangeSet("run-1"))
      .rejects.toThrow("inconsistent partial state");
    await expect(client.fileEditChangeSet("run-1")).resolves.toEqual({
      ...changeSet, items: [{ ...item, allowed_actions: [] }],
    });
  });

  it("validates bounded wake scheduling without accepting execution authority", async () => {
    const intent = { id: "wake-1", protocol_version: "run_wake_intent.v1", run_id: "run-1",
      session_id: "session-1", status: "queued", max_attempts: 3, attempt_count: 0,
      initial_delay_seconds: 0, base_backoff_seconds: 5, max_backoff_seconds: 60,
      max_elapsed_seconds: 300, next_wake_at: "2026-07-18T00:00:00Z",
      deadline_at: "2026-07-18T00:05:00Z", execution_enabled: false,
      background_loop_enabled: false, created_at: "2026-07-18T00:00:00Z",
      updated_at: "2026-07-18T00:00:00Z" };
    const result = { protocol_version: "run_wake_control.v1", action: "schedule", intent,
      replayed: false, execution_started: false, model_called: false, tool_called: false };
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-wake", data: result,
    }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: false, runWakeControlEnabled: true,
    });
    await expect(client.scheduleRunWake("run-1", {
      version: "run_wake_control.v1", max_attempts: 3, initial_delay_seconds: 0,
      base_backoff_seconds: 5, max_backoff_seconds: 60, max_elapsed_seconds: 300,
    }, "web-wake-operation-0001")).resolves.toEqual(result);
    expect((fetchMock.mock.calls[0]?.[1] as RequestInit).headers)
      .toMatchObject({ "Idempotency-Key": "web-wake-operation-0001" });

    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-wake-forged",
      data: { ...result, execution_started: true },
    }), { status: 202, headers: { "Content-Type": "application/json" } }));
    await expect(client.scheduleRunWake("run-1", {
      version: "run_wake_control.v1", max_attempts: 3, initial_delay_seconds: 0,
      base_backoff_seconds: 5, max_backoff_seconds: 60, max_elapsed_seconds: 300,
    }, "web-wake-operation-0002")).rejects.toThrow("authority");
  });

  it("validates apply, foreground wake, and inert Skill installation boundaries", async () => {
    const appliedEdit = { id: "edit-1", session_id: "session-1", workspace_id: "workspace-1",
      path: "safe.txt", operation: "replace", status: "applied",
      diff: "--- safe.txt\n+++ safe.txt\n+ok\n",
      original_hash: "missing", proposed_hash: "a".repeat(64), secrets_redacted: false,
      allowed_actions: [], created_at: "2026-07-18T00:00:00Z",
      updated_at: "2026-07-18T00:00:01Z", apply_enabled: false };
    const applyResult = { protocol_version: "file_edit_apply.v1", run_id: "run-1",
      edit: appliedEdit, status: "applied", replayed: false, file_written: true,
      policy_rechecked: true, receipt: operationReceipt("file_edit_apply", "applied",
        "same_operation_key", "complete") };
    const completedIntent = { id: "wake-1", protocol_version: "run_wake_intent.v1",
      run_id: "run-1", session_id: "session-1", status: "completed", max_attempts: 3,
      attempt_count: 1, initial_delay_seconds: 0, base_backoff_seconds: 5,
      max_backoff_seconds: 60, max_elapsed_seconds: 300,
      next_wake_at: "2026-07-18T00:05:00Z", deadline_at: "2026-07-18T00:05:00Z",
      execution_enabled: false, background_loop_enabled: false,
      created_at: "2026-07-18T00:00:00Z", updated_at: "2026-07-18T00:00:01Z" };
    const wakeResult = { protocol_version: "run_wake_consumer.v1", run_id: "run-1",
      intent: completedIntent, consumption_status: "completed", stop_reason: "waiting",
      replayed: false, execution_started: true, model_called: true, tool_called: false,
      background_loop_enabled: false, receipt: operationReceipt("run_wake_consume", "completed",
        "same_wake_generation", "not_applicable") };
    const skillResult = { protocol_version: "skill_package_installation.v1",
      name: "review-helper", version: "1.0.0", surface: "code",
      trust_class: "operator_installed_untrusted", archive_sha256: "b".repeat(64),
      package_fingerprint: "c".repeat(64), replayed: false, recovered_pending: false,
      import_command_execution: false, import_network_access: false,
      import_provider_calls: false, tool_capability_grant: false,
      run_selection_authorized: false, context_injection_authorized: false,
      receipt: operationReceipt("skill_package_install", "installed",
        "same_operation_key", "not_applicable") };
    const envelope = (requestID: string, data: unknown) => new Response(JSON.stringify({
      version: "api.v1", request_id: requestID, data,
    }), { status: 202, headers: { "Content-Type": "application/json" } });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(envelope("req-apply", applyResult))
      .mockResolvedValueOnce(envelope("req-consume", wakeResult))
      .mockResolvedValueOnce(envelope("req-skill", skillResult))
      .mockResolvedValueOnce(envelope("req-skill-forged", {
        ...skillResult, import_command_execution: true,
      }))
      .mockResolvedValueOnce(envelope("req-apply-mismatch", {
        ...applyResult, status: "failed", edit: { ...appliedEdit, status: "failed" },
      }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: false, fileEditApplyEnabled: true,
      runWakeExecutionEnabled: true, skillInstallationEnabled: true,
    });
    await expect(client.applyFileEdit("run-1", "edit-1", {
      version: "file_edit_apply.v1",
    }, "web-file-apply-operation-0001")).resolves.toEqual(applyResult);
    await expect(client.consumeRunWake("run-1", {
      version: "run_wake_consumer.v1", max_steps: 1,
    })).resolves.toEqual(wakeResult);
    const skillRequest = { version: "skill_package_installation.v1" as const,
      archive_base64: "UEsDBA==", surface: "code" as const, confirm_untrusted: true };
    await expect(client.installSkillPackage(skillRequest,
      "web-skill-install-operation-0001")).resolves.toEqual(skillResult);
    expect((fetchMock.mock.calls[0]?.[1] as RequestInit).headers)
      .toMatchObject({ "Idempotency-Key": "web-file-apply-operation-0001" });
    expect((fetchMock.mock.calls[1]?.[1] as RequestInit).headers)
      .not.toHaveProperty("Idempotency-Key");
    await expect(client.installSkillPackage(skillRequest,
      "web-skill-install-operation-0002")).rejects.toThrow("inert Registry authority");
    await expect(client.applyFileEdit("run-1", "edit-1", {
      version: "file_edit_apply.v1",
    }, "web-file-apply-operation-0002")).rejects.toThrow("durable recovery contract");
  });

  it("validates bounded Workspace evidence without accepting local root authority", async () => {
    const snapshot = { protocol_version: "workspace_explorer.v1", workspace_id: "workspace-1",
      path: "src", kind: "directory", entries: [{ name: "main.go", path: "src/main.go",
        kind: "file", size_bytes: 120, readable: true }], content: "", total_bytes: 0,
      returned_bytes: 0, truncated: false, redaction_count: 0, root_path_exposed: false,
      provenance: { version: "context_provenance.v1", source_kind: "workspace_listing",
        source_ref: "src", content_sha256: "a".repeat(64), instruction_authorized: false } };
    const envelope = (data: unknown) => new Response(JSON.stringify({
      version: "api.v1", request_id: "req-explorer", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(envelope(snapshot))
      .mockResolvedValueOnce(envelope({ ...snapshot, root_path: "C:\\private" }))
      .mockResolvedValueOnce(envelope({ ...snapshot, entries: [
        { ...snapshot.entries[0], path: "other/main.go" },
      ] }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret");
    await expect(client.workspaceExplore("workspace-1", "src")).resolves.toEqual(snapshot);
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain("path=src");
    await expect(client.workspaceExplore("workspace-1", "src"))
      .rejects.toThrow("bounded evidence contract");
    await expect(client.workspaceExplore("workspace-1", "src"))
      .rejects.toThrow("renderer path authority");
    await expect(client.workspaceExplore("workspace-1", "../private"))
      .rejects.toThrow("Go-issued relative path");
    await expect(client.workspaceExplore("workspace-1", "C:private"))
      .rejects.toThrow("Go-issued relative path");
  });

  it("validates repository state as a root-bound read-only projection", async () => {
    const state = { protocol_version: "repository_state.v1", workspace_id: "workspace-1",
      kind: "git", available: true, clean: false, detached: false, branch: "main",
      head: "1234567890ab", changes: [{ path: "src/main.go", staging: "unmodified",
        worktree: "modified" }], staged_count: 0, worktree_count: 1,
      untracked_count: 0, conflicted_count: 0, redaction_count: 0, truncated: false,
      read_only: true, root_path_exposed: false, content_included: false,
      remote_config_included: false, process_started: false, network_used: false,
      hooks_executed: false };
    const envelope = (data: unknown) => new Response(JSON.stringify({
      version: "api.v1", request_id: "req-repository", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(envelope(state))
      .mockResolvedValueOnce(envelope({ ...state, root_path_exposed: true }))
      .mockResolvedValueOnce(envelope({ ...state, changes: [
        { ...state.changes[0], path: "../outside" },
      ] }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret");

    await expect(client.repositoryState("workspace-1")).resolves.toEqual(state);
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe(
      "/api/v1/workspaces/workspace-1/repository-state");
    await expect(client.repositoryState("workspace-1"))
      .rejects.toThrow("read-only bounded contract");
    await expect(client.repositoryState("workspace-1"))
      .rejects.toThrow("path or status authority");
  });

  it("validates repository Diff, operator verification, and resumable Code handoff", async () => {
    const patch = "--- a/src/main.go\n+++ b/src/main.go\n@@ -1 +1 @@\n-old\n+new\n";
    const patchBytes = new TextEncoder().encode(patch).length;
    const diff = { protocol_version: "repository_diff.v1", workspace_id: "workspace-1",
      kind: "git", available: true, base_head: "1234567890ab", next_after: "",
      items: [{ path: "src/main.go", staging: "unmodified", worktree: "modified",
        content_state: "text", patch, patch_bytes: patchBytes, added_lines: 1,
        deleted_lines: 1, redacted: false, truncated: false, statistics_complete: true }],
      returned_count: 1, omitted_count: 0, redaction_count: 0,
      total_patch_bytes: patchBytes, truncated: false, read_only: true,
      instruction_authorized: false, mutation_supported: false, authority_granted: false,
      root_path_exposed: false, raw_content_included: false, patch_content_included: true,
      remote_config_included: false, process_started: false, network_used: false,
      hooks_executed: false };
    const item = { protocol_version: "operator_verification_evidence.v1",
      id: "verification-1", run_id: "run-1", session_id: "session-1",
      workspace_id: "workspace-1", outcome: "pass", title: "Focused tests",
      summary: "Go and React suites passed", summary_sha256: "a".repeat(64), redacted: false,
      recorded_at: "2026-07-19T12:00:00Z", immutable: true, operator_supplied: true,
      command_executed: false, model_assertion: false, approval: false,
      authority_granted: false };
    const inventory = { protocol_version: "operator_verification_inventory.v1",
      run_id: "run-1", session_id: "session-1", workspace_id: "workspace-1", items: [item],
      pass_count: 1, fail_count: 0, unknown_count: 0, truncated: false };
    const recorded = { ...item, replayed: false };
    const handoff = { protocol_version: "code_handoff.v1", run_id: "run-1",
      mission_id: "mission-1", session_id: "session-1", workspace_id: "workspace-1",
      run_status: "paused", surface: "code", phase: "deliver", mode_revision: 2,
      source_event_sequence: 42,
      generated_at: "2026-07-19T12:01:00Z",
      plan: { state: "none", proposal_id: "", selection_id: "", direction_count: 0,
        selected_direction: 0, module_count: 0, pending_count: 0, in_progress_count: 0,
        blocked_count: 0, completed_count: 0, cancelled_count: 0 },
      queue: { pending: 0, prepared: 0, committed: 0, cancelled: 0 },
      change_set: { proposed: 0, approved: 0, applied: 0, denied: 0, failed: 0,
        returned_count: 0, total_diff_bytes: 0, truncated: false },
      verification: { pass_count: 1, fail_count: 0, unknown_count: 0, returned_count: 1,
        truncated: false, references: [{ id: item.id, outcome: "pass", redacted: false,
          recorded_at: item.recorded_at }] },
      verification_plans: { returned_count: 0, truncated: false, references: [] },
      verification_coverage: { protocol_version: "operator_verification_plan_coverage.v1",
        plan_count: 0, plan_item_count: 0, observed_plan_item_count: 0,
        unobserved_plan_item_count: 0, associated_evidence_count: 0,
        contradictory_item_count: 0, returned_item_count: 0, truncated: false, items: [],
        metadata_only: true, read_only: true, result_inferred: false,
        private_bodies_included: false },
      verification_snapshot_receipt_reviews: {
        protocol_version: "operator_verification_plan_item_snapshot_receipt_review_inventory.v1",
        metadata_confirmed_count: 1, metadata_disputed_count: 0, returned_count: 1,
        truncated: false, references: [{ id: "verification-snapshot-receipt-review-1",
          receipt_id: "verification-snapshot-receipt-1", receipt_content_sha256: "d".repeat(64),
          receipt_event_sequence: 40, decision: "metadata_confirmed",
          review_event_sequence: 41, reviewed_at: "2026-07-19T12:00:00Z" }],
        metadata_only: true, read_only: true, review_non_authorizing: true,
        content_included: false, private_bodies_included: false,
        operator_identity_included: false, snapshot_accepted: false, result_accepted: false,
        result_inferred: false, record_rewritten: false, approval: false,
        authority_granted: false, execution_started: false },
      pending_action_count: 0, pending_actions_truncated: false, pending_actions: [],
      report_references_truncated: false, report_references: [], regenerable: true,
      durable_sources: true, private_bodies_included: false, composite_mutation: false,
      resume_authorized: false, execution_started: false,
      standard_code_delivery: standardCodeDeliveryFixture() };
    const envelope = (requestID: string, data: unknown, status = 200) =>
      new Response(JSON.stringify({ version: "api.v1", request_id: requestID, data }),
        { status, headers: { "Content-Type": "application/json" } });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(envelope("req-diff", diff))
      .mockResolvedValueOnce(envelope("req-verification", inventory))
      .mockResolvedValueOnce(envelope("req-record", recorded, 202))
      .mockResolvedValueOnce(envelope("req-handoff", handoff))
      .mockResolvedValueOnce(envelope("req-forged-diff", { ...diff, authority_granted: true }))
      .mockResolvedValueOnce(envelope("req-forged-handoff", { ...handoff,
        pending_action_count: 1, pending_actions: [{ id: "action-forged-reference",
          kind: "approval_pending", state: "queued", destination: "wake",
          available_at: "2026-07-19T12:00:00Z" }],
      }))
      .mockResolvedValueOnce(envelope("req-forged-review-handoff", { ...handoff,
        verification_snapshot_receipt_reviews: {
          ...handoff.verification_snapshot_receipt_reviews,
          operator_identity_included: true,
        },
      }))
      .mockResolvedValueOnce(envelope("req-forged-delivery-handoff", { ...handoff,
        standard_code_delivery: { ...handoff.standard_code_delivery,
          safeguards: { ...handoff.standard_code_delivery.safeguards,
            private_reasoning_stored: true },
        },
      }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: false, verificationEvidenceEnabled: true,
    });
    await expect(client.repositoryDiff("workspace-1")).resolves.toEqual(diff);
    await expect(client.verificationEvidence("run-1")).resolves.toEqual(inventory);
    await expect(client.recordVerificationEvidence("run-1", {
      version: "operator_verification_evidence.v1", outcome: "pass",
      title: item.title, summary: item.summary,
    }, "web-verification-operation-0001")).resolves.toEqual(recorded);
    await expect(client.codeHandoff("run-1")).resolves.toEqual(handoff);
    const [recordURL, recordInit] = fetchMock.mock.calls[2] as [string, RequestInit];
    expect(recordURL).toBe("/api/v1/runs/run-1/verification-evidence");
    expect(recordInit.headers).toMatchObject({ Authorization: "Bearer control-secret",
      "Idempotency-Key": "web-verification-operation-0001" });
    await expect(client.repositoryDiff("workspace-1"))
      .rejects.toThrow("bounded read-only contract");
    await expect(client.codeHandoff("run-1"))
      .rejects.toThrow("widened navigation");
    await expect(client.codeHandoff("run-1"))
      .rejects.toThrow("receipt reviews widened authority");
    await expect(client.codeHandoff("run-1"))
      .rejects.toThrow("delivery truth projection");
    for (const count of [1, 2, 3]) {
      const actualPlan = { ...handoff, plan: { ...handoff.plan, state: "selected", proposal_id: "proposal-1",
        selection_id: "selection-1", direction_count: count, selected_direction: count } };
      fetchMock.mockResolvedValueOnce(envelope(`req-plan-${count}`, actualPlan));
      await expect(client.codeHandoff("run-1")).resolves.toEqual(actualPlan);
      for (const policy of ["required", "on_demand"]) {
        const withPolicy = { ...actualPlan, plan: { ...actualPlan.plan, manual_acceptance: policy } };
        fetchMock.mockResolvedValueOnce(envelope(`req-plan-${count}-${policy}`, withPolicy));
        await expect(client.codeHandoff("run-1")).resolves.toEqual(withPolicy);
      }
      fetchMock.mockResolvedValueOnce(envelope(`req-plan-${count}-outside`, { ...actualPlan,
        plan: { ...actualPlan.plan, selected_direction: count + 1 } }));
      await expect(client.codeHandoff("run-1")).rejects.toThrow();
    }
    for (const policy of [null, true, "optional"]) {
      fetchMock.mockResolvedValueOnce(envelope("req-invalid-plan-policy", { ...handoff,
        plan: { ...handoff.plan, manual_acceptance: policy } }));
      await expect(client.codeHandoff("run-1")).rejects.toThrow("Plan summary is inconsistent");
    }
    const hostCommand = { proposal_id: "host-proposal-1", run_id: handoff.run_id,
      session_id: handoff.session_id, workspace_id: handoff.workspace_id, purpose: "Run project tests",
      working_directory: "D:/project", spec_fingerprint: "c".repeat(64), created_at: handoff.generated_at };
    const hostReceipt = { request_id: "host-request-1", exit_code: 0, timed_out: false, cancelled: false,
      stdout_truncated: false, stderr_truncated: false, output_limit_exceeded: false,
      tree_reaped: true, non_sandboxed: true, started_at: handoff.generated_at, completed_at: handoff.generated_at };
    const executedHost = { ...hostCommand, review_id: "host-review-1", review_decision: "approve",
      result_id: "host-result-1", result_status: "completed", source_ref: "host-command-proposal:host-proposal-1",
      content_sha256: "d".repeat(64), receipt: hostReceipt };
    for (const command of [hostCommand, executedHost,
      { ...executedHost, result_status: "failed", receipt: { ...hostReceipt, exit_code: 3 } }]) {
      const projected = { ...handoff, host_commands: { items: [command], truncated: false } };
      fetchMock.mockResolvedValueOnce(envelope("req-host-facts", projected));
      await expect(client.codeHandoff("run-1")).resolves.toEqual(projected);
    }
    for (const command of [{ ...executedHost, run_id: "another-run" },
      { ...hostCommand, result_status: "completed" }, { ...executedHost, verified: true },
      { ...executedHost, receipt: { ...hostReceipt, exit_code: 2 } },
      { ...executedHost, source_ref: "host-command-proposal:another-proposal" }]) {
      fetchMock.mockResolvedValueOnce(envelope("req-host-invalid", { ...handoff,
        host_commands: { items: [command], truncated: false } }));
      await expect(client.codeHandoff("run-1")).rejects.toThrow(/Code handoff host/);
    }
    await expect(client.recordVerificationEvidence("run-1", {
      version: "operator_verification_evidence.v1", outcome: "pass",
      title: "Focused tests", summary: "line one\rline two",
    }, "web-verification-operation-control"))
      .rejects.toThrow("bounded observation");
  });

  it("validates local history, guidance-only verification plans, and digest-bound handoff exports", async () => {
    const history = { protocol_version: "repository_history.v1", workspace_id: "workspace-1",
      kind: "git", available: true, head: "1234567890ab", detached: false,
      commits: [{ hash: "1234567890ab", object_id: "1234567890abcdef1234567890abcdef12345678",
        subject: "bounded commit", parent_count: 0,
        committed_at: "2026-07-19T10:00:00Z", redacted: false, subject_bounded: false }],
      branches: [{ name: "main", head: "1234567890ab", current: true }],
      returned_commit_count: 1, returned_branch_count: 1, omitted_branch_count: 0,
      redaction_count: 0, truncated: false, first_parent_only: true, read_only: true,
      root_path_exposed: false, author_identity_included: false, commit_body_included: false,
      remote_config_included: false, process_started: false, network_used: false,
      hooks_executed: false };
    const planItem = { ordinal: 1, title: "Focused tests",
      expected_observation: "Observe a pass", item_sha256: "b".repeat(64), redacted: false };
    const plan = { protocol_version: "operator_verification_plan.v1",
      id: "verification-plan-1", run_id: "run-1", session_id: "session-1",
      workspace_id: "workspace-1", title: "Release checks", summary: "Operator guidance",
      plan_sha256: "c".repeat(64), redacted: false, created_at: "2026-07-19T11:00:00Z",
      items: [planItem], item_count: 1, immutable: true, operator_supplied: true,
      guidance_only: true, command_executed: false, model_assertion: false,
      result_inferred: false, approval: false, authority_granted: false };
    const plans = { protocol_version: "operator_verification_plan_inventory.v1",
      run_id: "run-1", session_id: "session-1", workspace_id: "workspace-1",
      items: [plan], truncated: false };
    const exactLimitPlans = { ...plans,
      items: Array.from({ length: 50 }, (_, index) => ({
        ...plan, id: `verification-plan-${index + 1}`,
      })),
    };
    const recorded = { ...plan, replayed: false };
    const content = `${JSON.stringify({ protocol_version: "code_handoff.v1", run_id: "run-1",
      source_event_sequence: 42,
      verification_coverage: { protocol_version: "operator_verification_plan_coverage.v1",
        result_inferred: false },
      verification_snapshot_receipt_reviews: {
        protocol_version: "operator_verification_plan_item_snapshot_receipt_review_inventory.v1",
        metadata_only: true, read_only: true, review_non_authorizing: true,
        content_included: false, private_bodies_included: false,
        operator_identity_included: false, snapshot_accepted: false, result_accepted: false,
        result_inferred: false, record_rewritten: false, approval: false,
        authority_granted: false, execution_started: false,
      },
    }, null, 2)}\n`;
    const bytes = new TextEncoder().encode(content);
    const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
    const contentSHA256 = [...digest].map((byte) => byte.toString(16).padStart(2, "0")).join("");
    const exported = { protocol_version: "code_handoff_export.v1", format: "json",
      filename: "cyberagent-code-handoff-run-1.json", mime_type: "application/json",
      run_id: "run-1", source_event_sequence: 42, generated_at: "2026-07-19T12:00:00Z",
      content_sha256: contentSHA256, content_bytes: bytes.length, content,
      read_only: true, download_only: true, private_bodies: false, resume_authorized: false,
      mutation_supported: false, report_acceptance: false, execution_started: false };
    const envelope = (data: unknown, status = 200) => new Response(JSON.stringify({
      version: "api.v1", request_id: "req-new-projections", data,
    }), { status, headers: { "Content-Type": "application/json" } });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(envelope(history))
      .mockResolvedValueOnce(envelope(plans))
      .mockResolvedValueOnce(envelope(recorded, 202))
      .mockResolvedValueOnce(envelope(exported))
      .mockResolvedValueOnce(envelope(exactLimitPlans));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      verificationEvidenceEnabled: true,
    });
    await expect(client.repositoryHistory("workspace-1")).resolves.toEqual(history);
    await expect(client.verificationPlans("run-1")).resolves.toEqual(plans);
    await expect(client.recordVerificationPlan("run-1", {
      version: "operator_verification_plan.v1", title: plan.title, summary: plan.summary,
      items: [{ title: planItem.title, expected_observation: planItem.expected_observation }],
    }, "web-verification-plan-operation-0001")).resolves.toEqual(recorded);
    await expect(client.codeHandoffExport("run-1", "json")).resolves.toEqual(exported);
    await expect(client.verificationPlans("run-1")).resolves.toEqual(exactLimitPlans);
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe(
      "/api/v1/workspaces/workspace-1/repository-history");
    expect(String(fetchMock.mock.calls[3]?.[0])).toBe(
      "/api/v1/runs/run-1/code-handoff/export?format=json");
  });

  it("verifies exact bounded verification snapshot downloads before returning content", async () => {
    const planSHA = "a".repeat(64);
    const itemSHA = "b".repeat(64);
    const snapshot = {
      protocol_version: "operator_verification_plan_item_snapshot.v1", run_id: "run-1",
      session_id: "session-1", workspace_id: "workspace-1", plan_id: "plan-1",
      plan_sha256: planSHA, plan_item_ordinal: 1, plan_item_sha256: itemSHA,
      snapshot_high_water_event_sequence: 9, associated_evidence_count: 2,
      pass_count: 1, fail_count: 1, unknown_count: 0, returned_association_count: 2,
      associations_truncated: false,
      associations: [{ id: "association-2", plan_id: "plan-1", plan_item_ordinal: 1,
        plan_item_sha256: itemSHA, evidence_id: "evidence-2", evidence_outcome: "fail",
        evidence_event_sequence: 8, association_event_sequence: 9,
        associated_at: "2026-07-20T01:02:03Z" },
      { id: "association-1", plan_id: "plan-1", plan_item_ordinal: 1,
        plan_item_sha256: itemSHA, evidence_id: "evidence-1", evidence_outcome: "pass",
        evidence_event_sequence: 6, association_event_sequence: 7,
        associated_at: "2026-07-20T01:01:03Z" }],
      metadata_only: true, read_only: true, private_plan_body_included: false,
      private_evidence_bodies_included: false, operator_identity_included: false,
      result_inferred: false, command_executed: false, model_assertion: false,
      record_rewritten: false, approval: false, authority_granted: false,
      mutation_supported: false, execution_started: false,
    };
    const content = `${JSON.stringify(snapshot, null, 2)}\n`;
    const bytes = new TextEncoder().encode(content);
    const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
    const contentSHA256 = [...digest].map((byte) => byte.toString(16).padStart(2, "0")).join("");
    const exported = {
      protocol_version: "operator_verification_plan_item_snapshot_export.v1",
      snapshot_protocol_version: "operator_verification_plan_item_snapshot.v1", format: "json",
      filename: "cyberagent-verification-snapshot-run-1-plan-1-item-1.json",
      mime_type: "application/json", run_id: "run-1", session_id: "session-1",
      workspace_id: "workspace-1", plan_id: "plan-1", plan_sha256: planSHA,
      plan_item_ordinal: 1, plan_item_sha256: itemSHA,
      snapshot_high_water_event_sequence: 9, associated_evidence_count: 2,
      pass_count: 1, fail_count: 1, unknown_count: 0, returned_association_count: 2,
      associations_truncated: false, content_sha256: contentSHA256,
      content_bytes: bytes.length, content, metadata_only: true, read_only: true,
      download_only: true, private_plan_body_included: false,
      private_evidence_bodies_included: false, operator_identity_included: false,
      result_inferred: false, command_executed: false, model_assertion: false,
      record_rewritten: false, approval: false, authority_granted: false,
      mutation_supported: false, execution_started: false,
    };
    const envelope = (data: unknown) => new Response(JSON.stringify({ version: "api.v1",
      request_id: "req-verification-snapshot", data }),
    { status: 200, headers: { "Content-Type": "application/json" } });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(envelope(exported))
      .mockResolvedValueOnce(envelope({ ...exported, result_inferred: true }))
      .mockResolvedValueOnce(envelope({ ...exported, content: `${content} ` }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret");
    await expect(client.verificationPlanItemSnapshotExport("run-1", "plan-1", 1, "json"))
      .resolves.toEqual(exported);
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe(
      "/api/v1/runs/run-1/verification-plan-coverage/plan-1/items/1/snapshot-export?format=json");
    await expect(client.verificationPlanItemSnapshotExport("run-1", "plan-1", 1, "json"))
      .rejects.toThrow("read-only boundary");
    await expect(client.verificationPlanItemSnapshotExport("run-1", "plan-1", 1, "json"))
      .rejects.toThrow("metadata does not match");
  });

  it("validates exact commit metadata and explicit plan evidence coverage", async () => {
    const objectID = "1234567890abcdef1234567890abcdef12345678";
    const commit = { protocol_version: "repository_commit_detail.v1",
      workspace_id: "workspace-1", kind: "git", available: true, object_id: objectID,
      hash: objectID.slice(0, 12), subject: "bounded commit", committed_at: "2026-07-19T10:00:00Z",
      parent_count: 1, changes: [{ path: "internal/check.go", change: "added",
        previous_kind: "", current_kind: "regular", content_changed: true, mode_changed: true }],
      changed_file_count: 1, returned_change_count: 1, omitted_change_count: 0,
      redaction_count: 0, truncated: false, first_parent_only: true, read_only: true,
      root_path_exposed: false, author_identity_included: false, commit_body_included: false,
      file_content_included: false, patch_included: false, remote_config_included: false,
      checkout_performed: false, reference_updated: false, process_started: false,
      network_used: false, hooks_executed: false };
    const fileHistory = { protocol_version: "repository_file_history.v1",
      workspace_id: "workspace-1", kind: "git", available: true, head: objectID.slice(0, 12),
      path: "internal/check.go", entries: [{ object_id: objectID, hash: objectID.slice(0, 12),
        subject: "bounded commit", committed_at: "2026-07-19T10:00:00Z", change: "modified",
        previous_kind: "regular", current_kind: "regular", content_changed: true,
        mode_changed: false, redacted: false, subject_bounded: false },
      { object_id: "abcdef1234567890abcdef1234567890abcdef12", hash: "abcdef123456",
        subject: "ancestor with a later clock", committed_at: "2026-07-19T11:00:00Z",
        change: "added", previous_kind: "", current_kind: "regular", content_changed: true,
        mode_changed: true, redacted: false, subject_bounded: false }], scanned_commit_count: 2,
      returned_entry_count: 2, redaction_count: 0, observed: true, truncated: false,
      first_parent_only: true, rename_inferred: false, metadata_only: true, read_only: true,
      authority_granted: false, root_path_exposed: false, author_identity_included: false,
      commit_body_included: false, file_content_included: false, patch_included: false,
      remote_config_included: false, checkout_performed: false, reference_updated: false,
      process_started: false, network_used: false, hooks_executed: false };
    const previewContent = "SESSION_SECRET=[REDACTED:secret]\nsafe preview\n";
    const previewDigest = new Uint8Array(await globalThis.crypto.subtle.digest("SHA-256",
      new TextEncoder().encode(previewContent)));
    const previewSHA256 = [...previewDigest]
      .map((byte) => byte.toString(16).padStart(2, "0")).join("");
    const preview = { protocol_version: "repository_commit_file_preview.v1",
      workspace_id: "workspace-1", object_id: objectID, hash: objectID.slice(0, 12),
      path: "internal/check.go", kind: "regular", content: previewContent,
      total_bytes: 52, returned_bytes: new TextEncoder().encode(previewContent).length,
      redaction_count: 1, redacted: true,
      provenance: { version: "context_provenance.v1", source_kind: "repository_commit_file",
        source_ref: "internal/check.go", content_sha256: previewSHA256,
        instruction_authorized: false },
      read_only: true, mutation_supported: false, authority_granted: false,
      root_path_exposed: false, raw_blob_included: false, redacted_content_included: true,
      remote_config_included: false, checkout_performed: false, reference_updated: false,
      process_started: false, network_used: false, hooks_executed: false };
    const itemSHA = "b".repeat(64);
    const coverage = { protocol_version: "operator_verification_plan_coverage.v1",
      run_id: "run-1", session_id: "session-1", workspace_id: "workspace-1",
      plans: [{ plan_id: "verification-plan-1", plan_sha256: "c".repeat(64), item_count: 1,
        observed_item_count: 1, associated_evidence_count: 1,
        items: [{ ordinal: 1, item_sha256: itemSHA, associated_evidence_count: 1,
          pass_count: 1, fail_count: 0, unknown_count: 0,
          latest_association_event_sequence: 13 }] }],
      plan_count: 1, plan_item_count: 1, observed_plan_item_count: 1,
      associated_evidence_count: 1,
      associations: [{ id: "verification-association-1", plan_id: "verification-plan-1",
        plan_item_ordinal: 1, plan_item_sha256: itemSHA, evidence_id: "verification-1",
        evidence_outcome: "pass", evidence_event_sequence: 12,
        association_event_sequence: 13, associated_at: "2026-07-19T12:00:00Z" }],
      plans_truncated: false, associations_truncated: false, metadata_only: true,
      read_only: true, result_inferred: false, command_executed: false,
      model_assertion: false, record_rewritten: false, approval: false,
      authority_granted: false };
    const coverageDetail = {
      protocol_version: "operator_verification_plan_item_coverage.v1", run_id: "run-1",
      session_id: "session-1", workspace_id: "workspace-1",
      plan_id: "verification-plan-1", plan_sha256: "c".repeat(64), plan_item_ordinal: 1,
      plan_item_sha256: itemSHA, associated_evidence_count: 1, pass_count: 1,
      fail_count: 0, unknown_count: 0, latest_association_event_sequence: 13,
      associations: coverage.associations, associations_truncated: false, metadata_only: true,
      read_only: true, private_plan_body_included: false,
      private_evidence_bodies_included: false, operator_identity_included: false,
      result_inferred: false, command_executed: false, model_assertion: false,
      record_rewritten: false, approval: false, authority_granted: false,
    };
    const pagedNewestAssociation = { id: "verification-association-3",
      plan_id: "verification-plan-1", plan_item_ordinal: 1, plan_item_sha256: itemSHA,
      evidence_id: "verification-3", evidence_outcome: "fail", evidence_event_sequence: 14,
      association_event_sequence: 15, associated_at: "2026-07-19T12:05:00Z" };
    const pagedCoverageDetail = { ...coverageDetail, associated_evidence_count: 2,
      pass_count: 1, fail_count: 1, latest_association_event_sequence: 15,
      associations: [pagedNewestAssociation], associations_truncated: true };
    const associated = { protocol_version: "operator_verification_plan_evidence_association.v1",
      id: "verification-association-2", run_id: "run-1", session_id: "session-1",
      workspace_id: "workspace-1", plan_id: "verification-plan-1", plan_item_ordinal: 1,
      plan_item_sha256: itemSHA, evidence_id: "verification-2", evidence_outcome: "unknown",
      evidence_event_sequence: 14, association_event_sequence: 15,
      associated_at: "2026-07-19T12:05:00Z", immutable: true, operator_supplied: true,
      metadata_only: true, command_executed: false, model_assertion: false,
      result_inferred: false, record_rewritten: false, approval: false,
      authority_granted: false, replayed: false };
    const envelope = (data: unknown, status = 200, page?: unknown) => new Response(JSON.stringify({
      version: "api.v1", request_id: "req-exact-metadata", data, ...(page ? { page } : {}),
    }), { status, headers: { "Content-Type": "application/json" } });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(envelope(commit))
      .mockResolvedValueOnce(envelope(fileHistory))
      .mockResolvedValueOnce(envelope(preview))
      .mockResolvedValueOnce(envelope(coverage))
      .mockResolvedValueOnce(envelope(coverageDetail, 200, { limit: 50 }))
      .mockResolvedValueOnce(envelope(pagedCoverageDetail, 200,
        { limit: 1, next_cursor: "older-evidence" }))
      .mockResolvedValueOnce(envelope({ ...pagedCoverageDetail,
        associations: coverage.associations, associations_truncated: false }, 200, { limit: 1 }))
      .mockResolvedValueOnce(envelope(associated, 202))
      .mockResolvedValueOnce(envelope({ ...coverage, result_inferred: true }))
      .mockResolvedValueOnce(envelope({ ...fileHistory, authority_granted: true }))
      .mockResolvedValueOnce(envelope({ ...coverageDetail, operator_identity_included: true },
        200, { limit: 50 }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      verificationEvidenceEnabled: true,
    });
    await expect(client.repositoryCommit("workspace-1", objectID)).resolves.toEqual(commit);
    await expect(client.repositoryFileHistory("workspace-1", "internal/check.go"))
      .resolves.toEqual(fileHistory);
    await expect(client.repositoryCommitFilePreview("workspace-1", objectID,
      "internal/check.go")).resolves.toEqual(preview);
    await expect(client.verificationPlanCoverage("run-1")).resolves.toEqual(coverage);
    await expect(client.verificationPlanItemCoverage("run-1", "verification-plan-1", 1))
      .resolves.toEqual(coverageDetail);
    await expect(client.verificationPlanItemCoveragePage(
      "run-1", "verification-plan-1", 1, "", 1)).resolves.toEqual({
        detail: pagedCoverageDetail, page: { limit: 1, next_cursor: "older-evidence" },
        requestID: "req-exact-metadata",
      });
    await expect(client.verificationPlanItemCoveragePage(
      "run-1", "verification-plan-1", 1, "older-evidence", 1)).resolves.toEqual({
        detail: { ...pagedCoverageDetail, associations: coverage.associations,
          associations_truncated: false }, page: { limit: 1 },
        requestID: "req-exact-metadata",
      });
    await expect(client.associateVerificationEvidence("run-1", {
      version: "operator_verification_plan_evidence_association.v1",
      plan_id: "verification-plan-1", plan_item_ordinal: 1, evidence_id: "verification-2",
    }, "web-verification-association-operation-0001")).resolves.toEqual(associated);
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe(
      `/api/v1/workspaces/workspace-1/repository-commits/${objectID}`);
    expect(String(fetchMock.mock.calls[1]?.[0])).toBe(
      "/api/v1/workspaces/workspace-1/repository-file-history?path=internal%2Fcheck.go");
    expect(String(fetchMock.mock.calls[2]?.[0])).toBe(
      `/api/v1/workspaces/workspace-1/repository-commits/${objectID}/file-preview?path=internal%2Fcheck.go`);
    expect(String(fetchMock.mock.calls[4]?.[0])).toBe(
      "/api/v1/runs/run-1/verification-plan-coverage/verification-plan-1/items/1");
    expect(String(fetchMock.mock.calls[5]?.[0])).toBe(
      "/api/v1/runs/run-1/verification-plan-coverage/verification-plan-1/items/1?limit=1");
    expect(String(fetchMock.mock.calls[6]?.[0])).toBe(
      "/api/v1/runs/run-1/verification-plan-coverage/verification-plan-1/items/1?" +
      "limit=1&cursor=older-evidence");
    const [associationURL, associationInit] = fetchMock.mock.calls[7] as [string, RequestInit];
    expect(associationURL).toBe("/api/v1/runs/run-1/verification-plan-associations");
    expect(associationInit.headers).toMatchObject({ Authorization: "Bearer control-secret",
      "Idempotency-Key": "web-verification-association-operation-0001" });
    await expect(client.verificationPlanCoverage("run-1"))
      .rejects.toThrow("metadata-only authority");
    await expect(client.repositoryFileHistory("workspace-1", "internal/check.go"))
      .rejects.toThrow("exact metadata contract");
    await expect(client.verificationPlanItemCoverage("run-1", "verification-plan-1", 1))
      .rejects.toThrow("read-only boundary");
  });

  it("keeps snapshot receipt reviews immutable and non-authorizing", async () => {
    const review = {
      protocol_version: "operator_verification_plan_item_snapshot_receipt_review.v1",
      id: "snapshot-review-1", run_id: "run-1", session_id: "session-1",
      workspace_id: "workspace-1", receipt_id: "snapshot-receipt-1",
      receipt_content_sha256: "a".repeat(64), receipt_event_sequence: 9,
      decision: "metadata_confirmed", review_event_sequence: 10,
      reviewed_at: "2026-07-20T01:12:00Z", immutable: true, operator_reviewed: true,
      metadata_only: true, read_only: true, review_non_authorizing: true,
      content_included: false, private_bodies_included: false,
      operator_identity_included: false, snapshot_accepted: false, result_accepted: false,
      result_inferred: false, record_rewritten: false, approval: false,
      authority_granted: false, execution_started: false,
    } as const;
    const inventory = {
      protocol_version:
        "operator_verification_plan_item_snapshot_receipt_review_inventory.v1",
      run_id: "run-1", session_id: "session-1", workspace_id: "workspace-1",
      items: [review], truncated: false, metadata_only: true, read_only: true,
      review_non_authorizing: true, snapshot_accepted: false, result_accepted: false,
      result_inferred: false, record_rewritten: false, approval: false,
      authority_granted: false, execution_started: false,
    } as const;
    const envelope = (data: unknown, status = 200) => new Response(JSON.stringify({
      version: "api.v1", request_id: "req-snapshot-review", data,
    }), { status, headers: { "Content-Type": "application/json" } });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(envelope(inventory))
      .mockResolvedValueOnce(envelope({ ...review, replayed: false }, 202))
      .mockResolvedValueOnce(envelope({ ...review, replayed: false, result_accepted: true }, 202));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      verificationEvidenceEnabled: true,
    });
    await expect(client.verificationSnapshotReceiptReviews("run-1"))
      .resolves.toEqual(inventory);
    const request = {
      version: "operator_verification_plan_item_snapshot_receipt_review.v1" as const,
      receipt_id: "snapshot-receipt-1", receipt_content_sha256: "a".repeat(64),
      receipt_event_sequence: 9, decision: "metadata_confirmed" as const,
      confirm_non_authorizing_review: true,
    };
    await expect(client.recordVerificationSnapshotReceiptReview("run-1", request,
      "web-snapshot-review-operation-0001")).resolves.toEqual({ ...review, replayed: false });
    const [reviewURL, reviewInit] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(reviewURL).toBe("/api/v1/runs/run-1/verification-snapshot-receipt-reviews");
    expect(reviewInit.headers).toMatchObject({ Authorization: "Bearer control-secret",
      "Idempotency-Key": "web-snapshot-review-operation-0001" });
    await expect(client.recordVerificationSnapshotReceiptReview("run-1", request,
      "web-snapshot-review-operation-0002")).rejects.toThrow("widened acceptance or authority");
  });

  it("accepts only bounded metadata-only exact commit comparisons", async () => {
    const baseObjectID = "a".repeat(40);
    const headObjectID = "b".repeat(40);
    const comparison = {
      protocol_version: "repository_commit_comparison.v1", workspace_id: "workspace-1",
      kind: "git", available: true, base_object_id: baseObjectID,
      base_hash: baseObjectID.slice(0, 12), base_subject: "comparison base",
      base_committed_at: "2026-07-19T10:00:00Z", base_redacted: false,
      base_subject_bounded: false, head_object_id: headObjectID,
      head_hash: headObjectID.slice(0, 12), head_subject: "comparison head",
      head_committed_at: "2026-07-19T11:00:00Z", head_redacted: false,
      head_subject_bounded: false, same_object: false,
      changes: [{ path: "internal/check.go", change: "modified",
        previous_kind: "regular", current_kind: "executable", content_changed: true,
        mode_changed: true }], changed_file_count: 1, returned_change_count: 1,
      omitted_change_count: 0, redaction_count: 0, truncated: false,
      metadata_only: true, read_only: true, rename_inferred: false,
      ancestor_required: false, authority_granted: false, root_path_exposed: false,
      author_identity_included: false, commit_body_included: false,
      file_content_included: false, patch_included: false, remote_config_included: false,
      checkout_performed: false, reference_updated: false, process_started: false,
      network_used: false, hooks_executed: false,
    };
    const envelope = (data: unknown) => new Response(JSON.stringify({
      version: "api.v1", request_id: "req-comparison", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(envelope(comparison))
      .mockResolvedValueOnce(envelope({ ...comparison, file_content_included: true }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret");
    await expect(client.repositoryCommitComparison("workspace-1", baseObjectID, headObjectID))
      .resolves.toEqual(comparison);
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe(
      "/api/v1/workspaces/workspace-1/repository-commit-comparison?" +
      `base_object_id=${baseObjectID}&head_object_id=${headObjectID}`);
    await expect(client.repositoryCommitComparison("workspace-1", baseObjectID, headObjectID))
      .rejects.toThrow("metadata contract");
    await expect(client.repositoryCommitComparison("workspace-1", "short", headObjectID))
      .rejects.toThrow("exact commit identities");
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("validates Workspace search, evidence attachment, and metadata-only receipt history", async () => {
    const provenance = { version: "context_provenance.v1", source_kind: "workspace_file",
      source_ref: "README.md", content_sha256: "d".repeat(64),
      instruction_authorized: false };
    const search = { protocol_version: "workspace_search.v1", workspace_id: "workspace-1",
      results: [{ path: "README.md", match_kind: "content", line: 2,
        snippet: "Notes for automated assistants", content_truncated: false, provenance }],
      scanned_entries: 1, scanned_files: 1, scanned_bytes: 80,
      truncated: false, root_path_exposed: false };
    const attachment = { protocol_version: "session_evidence_attachment.v1",
      attachment_id: "evidence-1", run_id: "run-1", session_id: "session-1",
      workspace_id: "workspace-1", source_kind: "workspace_file", source_ref: "README.md",
      content_sha256: provenance.content_sha256, session_message_id: 8,
      instruction_authorized: false, replayed: false, execution_started: false,
      model_called: false, tool_called: false, capability_grant: false };
    const receipt = operationReceipt("file_edit_apply", "applied",
      "same_operation_key", "complete");
    const history = { protocol_version: "operation_receipt_history.v1", truncated: false,
      items: [{ id: "receipt-opaque", scope: "run", run_id: "run-1",
        completed_at: "2026-07-19T10:00:00Z", receipt }] };
    const envelope = (requestID: string, data: unknown, status = 200) =>
      new Response(JSON.stringify({ version: "api.v1", request_id: requestID, data }),
        { status, headers: { "Content-Type": "application/json" } });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(envelope("req-search", search))
      .mockResolvedValueOnce(envelope("req-evidence", attachment, 202))
      .mockResolvedValueOnce(envelope("req-history", history))
      .mockResolvedValueOnce(envelope("req-history-forged", {
        ...history, items: [{ ...history.items[0], receipt: {
          ...receipt, kind: "shell_execute", outcome: "completed",
        } }],
      }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runControlEnabled: false, evidenceAttachmentEnabled: true,
    });

    await expect(client.workspaceSearch("workspace-1", "automated assistants"))
      .resolves.toEqual(search);
    await expect(client.attachEvidence("run-1", {
      version: "session_evidence_attachment.v1", source_kind: "workspace_file",
      source_ref: "README.md", content_sha256: provenance.content_sha256,
    }, "web-evidence-operation-0001")).resolves.toEqual(attachment);
    await expect(client.operationReceiptHistory("run-1")).resolves.toEqual(history);
    const [searchURL] = fetchMock.mock.calls[0] as [string, RequestInit];
    const [attachURL, attachInit] = fetchMock.mock.calls[1] as [string, RequestInit];
    const [historyURL] = fetchMock.mock.calls[2] as [string, RequestInit];
    expect(searchURL).toContain("/workspaces/workspace-1/search?query=automated+assistants");
    expect(attachURL).toBe("/api/v1/runs/run-1/evidence-attachments");
    expect(attachInit.headers).toMatchObject({ Authorization: "Bearer control-secret",
      "Idempotency-Key": "web-evidence-operation-0001" });
    expect(String(attachInit.body)).not.toContain("control-secret");
    expect(historyURL).toContain("/operation-receipts?run_id=run-1&limit=100");
    await expect(client.operationReceiptHistory("run-1"))
      .rejects.toThrow("unsupported terminal result");
  });

  it("validates bounded operator actions and metadata-only evidence inventory", async () => {
    const inventory = { protocol_version: "session_evidence_inventory.v1", run_id: "run-1",
      truncated: false, items: [{ attachment_id: "evidence-1", run_id: "run-1",
        session_id: "session-1", workspace_id: "workspace-1", source_kind: "workspace_file",
        source_ref: "README.md", content_sha256: "c".repeat(64),
        instruction_authorized: false, attached_at: "2026-07-19T10:00:00Z" }] };
    const actions = { protocol_version: "operator_action_center.v1", run_id: "run-1",
      generated_at: "2026-07-19T12:00:00Z", truncated: false,
      items: [{ id: "action-opaque", kind: "wake_due", state: "queued",
        destination: "wake", available_at: "2026-07-19T11:00:00Z",
        due_at: "2026-07-19T11:00:00Z" }] };
    const envelope = (requestID: string, data: unknown) => new Response(JSON.stringify({
      version: "api.v1", request_id: requestID, data,
    }), { status: 200, headers: { "Content-Type": "application/json" } });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(envelope("req-inventory", inventory))
      .mockResolvedValueOnce(envelope("req-actions", actions))
      .mockResolvedValueOnce(envelope("req-forged-actions", {
        ...actions, items: [{ ...actions.items[0], due_at: "2026-07-20T11:00:00Z" }],
      }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret");

    await expect(client.evidenceInventory("run-1")).resolves.toEqual(inventory);
    await expect(client.operatorActionCenter("run-1")).resolves.toEqual(actions);
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe(
      "/api/v1/runs/run-1/evidence-attachments");
    expect(String(fetchMock.mock.calls[1]?.[0])).toBe(
      "/api/v1/runs/run-1/operator-actions");
    await expect(client.operatorActionCenter("run-1"))
      .rejects.toThrow("closed navigation contract");
  });

  it("polls Run events with a stream-compatible opaque cursor and validates the envelope", async () => {
    const frame: RunEventStreamView = {
      version: "run-events.v1",
      request_id: "req-poll",
      run_id: "run-1",
      cursor: "opaque-2",
      sequence: 2,
      event: {
        event_id: "event-2",
        version: "v1",
        run_id: "run-1",
        mission_id: "mission-1",
        sequence: 2,
        type: "run.updated",
        source: "test",
        payload: {},
        created_at: "2026-07-18T00:00:00Z",
      },
    };
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1",
      request_id: "req-poll",
      data: {
        version: "run-event-poll.v1",
        run_id: "run-1",
        cursor: "opaque-2",
        frames: [frame],
        has_more: false,
      },
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);

    const result = await new APIClient("read-secret").pollRunEvents("run-1", "opaque-1", 25);

    expect(result.frames).toEqual([frame]);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toContain("/api/v1/runs/run-1/events/poll?");
    expect(url).toContain("cursor=opaque-1");
    expect(url).toContain("limit=25");
    expect(url).not.toContain("read-secret");
    expect(init.headers).toMatchObject({ Authorization: "Bearer read-secret" });
  });

  it("rejects a poll cursor that does not match the final validated frame", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1",
      request_id: "req-poll",
      data: {
        version: "run-event-poll.v1",
        run_id: "run-1",
        cursor: "forged-final",
        has_more: false,
        frames: [{
          version: "run-events.v1",
          request_id: "req-poll",
          run_id: "run-1",
          cursor: "actual-final",
          sequence: 1,
          event: {
            event_id: "event-1", version: "v1", run_id: "run-1", mission_id: "mission-1",
            sequence: 1, type: "run.created", source: "test", payload: {},
            created_at: "2026-07-18T00:00:00Z",
          },
        }],
      },
    }), { status: 200, headers: { "Content-Type": "application/json" } })));

    await expect(new APIClient("read-secret").pollRunEvents("run-1"))
      .rejects.toThrow("final frame");
  });

  it("reports an open empty SSE stream after validating the response", async () => {
    const onOpen = vi.fn();
    const onFrame = vi.fn();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(": keepalive\n\n", {
      status: 200, headers: { "Content-Type": "text/event-stream" },
    })));
    await new APIClient("read-secret").streamRunEvents("run-1", {
      signal: new AbortController().signal, onOpen, onFrame,
    });
    expect(onOpen).toHaveBeenCalledTimes(1);
    expect(onFrame).not.toHaveBeenCalled();
  });

  it.each([403, 200])("does not report an open SSE stream for an invalid response with status %s", async (status) => {
    const onOpen = vi.fn();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "invalid-sse", error: { code: "PERMISSION_DENIED", message: "SSE unavailable" },
    }), { status, headers: { "Content-Type": "application/json" } })));
    await expect(new APIClient("read-secret").streamRunEvents("run-1", {
      signal: new AbortController().signal, onOpen, onFrame: vi.fn(),
    })).rejects.toThrow(status === 403 ? "SSE unavailable" : "invalid event stream");
    expect(onOpen).not.toHaveBeenCalled();
  });

  it("resumes SSE with Last-Event-ID and validates the matching cursor", async () => {
    const frame: RunEventStreamView = {
      version: "run-events.v1",
      request_id: "req-stream",
      run_id: "run-1",
      cursor: "cursor-2",
      sequence: 2,
      event: {
        event_id: "event-2",
        version: "v1",
        run_id: "run-1",
        mission_id: "mission-1",
        sequence: 2,
        type: "run.updated",
        source: "test",
        payload: {},
        created_at: "2026-07-13T00:00:00Z",
      },
    };
    const body = `id: cursor-2\nevent: run.event\ndata: ${JSON.stringify(frame)}\n\n`;
    const fetchMock = vi.fn().mockResolvedValue(new Response(body, {
      status: 200,
      headers: { "Content-Type": "text/event-stream" },
    }));
    vi.stubGlobal("fetch", fetchMock);
    const received: RunEventStreamView[] = [];
    const controller = new AbortController();

    await new APIClient("read-secret").streamRunEvents("run-1", {
      cursor: "cursor-1",
      signal: controller.signal,
      onFrame: (value) => received.push(value),
    });

    expect(received).toEqual([frame]);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).not.toContain("cursor");
    expect(init.headers).toMatchObject({
      Accept: "text/event-stream",
      Authorization: "Bearer read-secret",
      "Last-Event-ID": "cursor-1",
    });
  });

  it("rejects a run event frame without the matching SSE id", async () => {
    const frame = {
      version: "run-events.v1",
      request_id: "req-stream",
      run_id: "run-1",
      cursor: "cursor-1",
      sequence: 1,
      event: {
        event_id: "event-1",
        version: "v1",
        run_id: "run-1",
        mission_id: "mission-1",
        sequence: 1,
        type: "run.created",
        source: "test",
        payload: {},
        created_at: "2026-07-13T00:00:00Z",
      },
    };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(
      `event: run.event\ndata: ${JSON.stringify(frame)}\n\n`,
      { status: 200, headers: { "Content-Type": "text/event-stream" } },
    )));

    await expect(new APIClient("read-secret").streamRunEvents("run-1", {
      signal: new AbortController().signal,
      onFrame: () => undefined,
    })).rejects.toThrow("id does not match");
  });

  it("rejects an explicit agent browser live-view close after delivering frames", async () => {
    const frame = {
      version: "agent_browser_screencast.v1", run_id: "run-1", session_id: "browser-live-session",
      generation: 1, media_type: "image/jpeg", width: 1280, height: 900, data: "aGVsbG8=",
      captured_at: "2026-10-28T00:00:00Z",
    };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(
      `event: agent_browser.frame\ndata: ${JSON.stringify(frame)}\n\n` +
        'event: agent_browser.closed\ndata: {"version":"agent_browser_screencast.v1","reason":"ended"}\n\n',
      { status: 200, headers: { "Content-Type": "text/event-stream" } },
    )));
    const onFrame = vi.fn();
    await expect(new APIClient("read-secret").streamAgentBrowserScreencast("run-1", {
      signal: new AbortController().signal, onFrame,
    })).rejects.toThrow("live view closed");
    expect(onFrame).toHaveBeenCalledWith(frame);
  });

  it("rejects a malformed agent browser live frame instead of ignoring it", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(
      'event: agent_browser.frame\ndata: {"version":"agent_browser_screencast.v1","run_id":"run-1",' +
        '"session_id":"browser-live-session","generation":1,"media_type":"image/jpeg","width":1280,' +
        '"height":900,"data":"not base64!","captured_at":"2026-10-28T00:00:00Z"}\n\n',
      { status: 200, headers: { "Content-Type": "text/event-stream" } },
    )));
    await expect(new APIClient("read-secret").streamAgentBrowserScreencast("run-1", {
      signal: new AbortController().signal, onFrame: vi.fn(),
    })).rejects.toThrow("帧无效");
  });

  it("keeps Provider credentials write-only and returns status metadata", async () => {
    const items = ["anthropic", "deepseek", "mimo", "openai"].map((provider) => ({
      protocol_version: "provider_credential.v1", provider, configured: false,
      store_kind: "windows_credential_manager", store_available: true,
      plaintext_returned: false, restart_required: false,
      registry_reloaded: false, registry_generation: 1,
    }));
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-credential-list", data: {
          protocol_version: "provider_credential.v1", items,
        } }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-credential-set", data: { ...items[2], configured: true,
          registry_reloaded: true, registry_generation: 2 } }), { status: 202,
        headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret",
      { providerCredentialEnabled: true });
    await expect(client.providerCredentialStatuses()).resolves.toMatchObject({ items });
    const secret = "temporary-provider-key";
    await expect(client.changeProviderCredential("mimo", {
      version: "provider_credential.v1", action: "set", secret, confirm: true,
    })).resolves.toMatchObject({ provider: "mimo", configured: true,
      plaintext_returned: false, restart_required: false, registry_reloaded: true,
      registry_generation: 2 });
    const [url, init] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(url).toBe("/api/v1/models/credentials/mimo");
    expect(url).not.toContain(secret);
    expect(init.headers).toMatchObject({ Authorization: "Bearer control-secret" });
    expect(JSON.parse(String(init.body))).toEqual({ version: "provider_credential.v1",
      action: "set", secret, confirm: true });
  });

  it.each(["auto", "web"] as const)("persists custom Provider definitions with %s search and accepts their OS-owned credential", async (searchMode) => {
    const draft: ProviderDefinitionView = {
      version: "provider_definition.v1", id: "team-gateway", display_name: "Team Gateway",
      note: "Responses-compatible company endpoint", website_url: "",
      endpoint_url: "https://models.example.org/v1/responses",
      default_model: "team-model", models: ["team-model"],
      transport: "openai_responses", search_mode: searchMode,
      native_web_search_capability: searchMode === "web" ? "unsupported" : "declared_unverified",
      advanced_config: { request_headers: {
        Authorization: { $credential: "team-gateway", template: "Bearer ${secret}" },
      } }, enabled: true, revision: 0,
    };
    const saved = { ...draft, revision: 1 };
    const empty = { version: "provider_definition_collection.v1", revision: 0, providers: [] };
    const populated = { version: "provider_definition_collection.v1", revision: 1,
      providers: [saved] };
    const removed = { version: "provider_definition_collection.v1", revision: 2, providers: [] };
    const credentialItems = ["anthropic", "deepseek", "mimo", "openai", "team-gateway"]
      .map((provider) => ({ protocol_version: "provider_credential.v1", provider,
        configured: false, store_kind: "windows_credential_manager", store_available: true,
        plaintext_returned: false, restart_required: false, registry_reloaded: false,
        registry_generation: 2 }));
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-provider-list", data: empty }), { status: 200,
        headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-provider-upsert", data: {
          protocol_version: "provider_definition_control.v1", collection: populated,
          definition: saved, registry_reloaded: true, registry_generation: 2,
        } }), { status: 202, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-custom-credential-list", data: {
          protocol_version: "provider_credential.v1", items: credentialItems,
        } }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-custom-credential-set", data: { ...credentialItems[4],
          configured: true, registry_reloaded: true, registry_generation: 3 },
      }), { status: 202, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-provider-delete", data: {
          protocol_version: "provider_definition_control.v1", collection: removed,
          deleted_id: "team-gateway", registry_reloaded: true, registry_generation: 4,
        } }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret",
      { modelControlEnabled: true, providerCredentialEnabled: true });

    await expect(client.providerDefinitions()).resolves.toEqual(empty);
    await expect(client.upsertProviderDefinition("team-gateway", {
      version: "provider_definition_control.v1", expected_collection_revision: 0,
      definition: draft, confirm: true,
    })).resolves.toMatchObject({ collection: populated, definition: saved,
      registry_reloaded: true });
    await expect(client.providerCredentialStatuses()).resolves.toMatchObject({
      items: credentialItems,
    });
    const transientSecret = "temporary-custom-provider-key";
    await expect(client.changeProviderCredential("team-gateway", {
      version: "provider_credential.v1", action: "set", secret: transientSecret,
      confirm: true,
    })).resolves.toMatchObject({ provider: "team-gateway", configured: true });
    await expect(client.deleteProviderDefinition("team-gateway", {
      version: "provider_definition_control.v1", expected_collection_revision: 1,
      expected_definition_revision: 1, confirm: true,
    })).resolves.toMatchObject({ deleted_id: "team-gateway", collection: removed });

    const [upsertURL, upsertInit] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(upsertURL).toBe("/api/v1/models/provider-definitions/team-gateway");
    expect(String(upsertInit.body)).not.toContain(transientSecret);
    const [credentialURL] = fetchMock.mock.calls[3] as [string, RequestInit];
    expect(credentialURL).toBe("/api/v1/models/credentials/team-gateway");
    const [deleteURL] = fetchMock.mock.calls[4] as [string, RequestInit];
    expect(deleteURL).toBe("/api/v1/models/provider-definitions/team-gateway/delete");
  });

  it.each([false, true])("accepts OpenAI-compatible availability (custom web=%s) and rejects unknown qualification reasons", async (customWeb) => {
    const providerID = customWeb ? "team-openai" : "openai";
    const harness = {
      protocol_version: "model_harness.v1", model: "gpt-4.1-mini",
      transport_protocol: "openai_chat_completions", tool_strategy: "native",
      json_strategy: "native", qualification_status: "qualification_required",
      tool_calls_qualified: false, tool_results_qualified: false,
      strict_json_qualified: false, streaming_qualified: false, root_eligible: false,
      structured_json_eligible: false, latest_qualification_status: "",
      qualification_checked_at: "", qualification_source: "", qualified_at: "",
      expires_at: "",
    };
    const availability = {
      protocol_version: "model_availability.v2", generation: 2,
      providers: [{ name: providerID, kind: "openai_compatible", status: "available",
        display_name: providerID, custom: customWeb, enabled: true, definition_revision: customWeb ? 1 : 0,
        transport: "openai_chat_completions", search_mode: customWeb ? "web" : "disabled",
        native_web_search_capability: "unsupported",
        native_web_search_runtime_enabled: false,
        models: ["gpt-4.1-mini"], harnesses: [harness], credential_source: "environment",
        network_required: true, configuration_error: false }],
      routes: [{ name: "code", provider: providerID, model: "gpt-4.1-mini",
        available: true, harness_ready: false }],
    };
    const diagnostic = {
      protocol_version: "provider_diagnostic.v1", provider: "openai",
      model: "gpt-4.1-mini", status: "unreachable", outcome: "permanent",
      failure_reason: "authentication", retryable: false, network_request_attempted: true,
      model_called: true, tool_called: false, response_content_returned: false,
      qualification_status: "auth_failed",
      duration_ms: 4,
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-openai-models", data: availability }), { status: 200,
        headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-openai-diagnostic", data: diagnostic }), { status: 202,
        headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-openai-forged", data: { ...diagnostic, failure_reason: "raw_error" } }),
      { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      modelControlEnabled: true,
    });
    await expect(client.modelAvailability()).resolves.toEqual(availability);
    const request = { version: "provider_diagnostic.v1" as const, provider: "openai",
      model: "gpt-4.1-mini", confirm_diagnostic: true };
    await expect(client.diagnoseProvider(request)).resolves.toEqual(diagnostic);
    await expect(client.diagnoseProvider(request)).rejects.toThrow("content-free");
  });

  it.each(["context_limit", "output_limit", "paused", "refusal"])("accepts typed incomplete provider completion %s", async (reason) => {
    const diagnostic = {
      protocol_version: "provider_diagnostic.v1", provider: "openai", model: "model",
      status: "unreachable", outcome: "permanent", failure_reason: reason,
      retryable: false, network_request_attempted: true, model_called: true,
      tool_called: false, response_content_returned: false,
      qualification_status: "response_incomplete", duration_ms: 4,
    };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "typed-terminal", data: diagnostic,
    }), { status: 202, headers: { "Content-Type": "application/json" } })));
    const client = new APIClient("read-secret", "/api/v1", "control-secret", { modelControlEnabled: true });
    await expect(client.diagnoseProvider({ version: "provider_diagnostic.v1", provider: "openai",
      model: "model", confirm_diagnostic: true })).resolves.toEqual(diagnostic);
  });

  it("rejects contradictory provider diagnostic semantics", async () => {
    const forged = {
      protocol_version: "provider_diagnostic.v1", provider: "openai",
      model: "gpt-4.1-mini", status: "unreachable", outcome: "success",
      failure_reason: "authentication", retryable: false, network_request_attempted: true,
      model_called: true, tool_called: false, response_content_returned: false,
      qualification_status: "network_failed",
      duration_ms: 4,
    };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-openai-forged-semantics", data: forged,
    }), { status: 202, headers: { "Content-Type": "application/json" } })));
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      modelControlEnabled: true,
    });
    await expect(client.diagnoseProvider({ version: "provider_diagnostic.v1", provider: "openai",
      model: "gpt-4.1-mini", confirm_diagnostic: true })).rejects.toThrow("content-free");
  });

  it("rejects unknown or non-string diagnostic and qualification enums", async () => {
    const diagnostic = {
      protocol_version: "provider_diagnostic.v1", provider: "openai",
      model: "gpt-4.1-mini", status: "unreachable", outcome: "vendor_error",
      failure_reason: "authentication", retryable: false, network_request_attempted: true,
      model_called: true, tool_called: false, response_content_returned: false,
      qualification_status: "capacity",
      duration_ms: 4,
    };
    const qualification = {
      protocol_version: "model_harness_qualification.v1", provider: "openai",
      model: "gpt-4.1-mini", status: "unreachable", outcome: "vendor_error",
      failure_reason: "authentication", retryable: false, network_request_attempted: true,
      model_calls: 1, synthetic_tool_calls: 0, tool_executed: false,
      response_content_returned: false, qualification_status: "rate_limit",
      duration_ms: 4,
      harness: {
        protocol_version: "model_harness.v1", model: "gpt-4.1-mini",
        transport_protocol: "openai_chat_completions", tool_strategy: "native",
        json_strategy: "native", qualification_status: "qualification_required",
        tool_calls_qualified: false, tool_results_qualified: false,
        strict_json_qualified: false, streaming_qualified: false, root_eligible: false,
        structured_json_eligible: false, latest_qualification_status: "",
        qualification_checked_at: "", qualification_source: "",
        qualified_at: "", expires_at: "",
      },
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-unknown-diagnostic-outcome", data: diagnostic }), { status: 202,
        headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-unknown-qualification-outcome", data: qualification }), { status: 202,
        headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-array-diagnostic-outcome",
        data: { ...diagnostic, outcome: ["permanent"] } }), { status: 202,
        headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-array-qualification-reason",
        data: { ...qualification, outcome: "permanent", failure_reason: ["authentication"] } }),
      { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      modelControlEnabled: true,
    });
    await expect(client.diagnoseProvider({ version: "provider_diagnostic.v1", provider: "openai",
      model: "gpt-4.1-mini", confirm_diagnostic: true })).rejects.toThrow("content-free");
    await expect(client.qualifyModelHarness({ version: "model_harness_qualification.v1",
      provider: "openai", model: "gpt-4.1-mini", confirm_qualification: true }))
      .rejects.toThrow("qualification response is invalid");
    await expect(client.diagnoseProvider({ version: "provider_diagnostic.v1", provider: "openai",
      model: "gpt-4.1-mini", confirm_diagnostic: true })).rejects.toThrow("content-free");
    await expect(client.qualifyModelHarness({ version: "model_harness_qualification.v1",
      provider: "openai", model: "gpt-4.1-mini", confirm_qualification: true }))
      .rejects.toThrow("qualification response is invalid");
  });

  it("creates only a pending FileEdit from an opaque Go-issued source", async () => {
    const source = { protocol_version: "file_edit_proposal.v1", run_id: "run-1",
      workspace_id: "workspace-1", path: "README.md", content: "before\n",
      content_sha256: "a".repeat(64), source_handle: "B".repeat(43),
      expires_at: "2099-07-18T00:05:00Z", editable: true, file_write: false };
    const reissued = { ...source, source_handle: "C".repeat(43) };
    const edit = { id: "edit-1", session_id: "session-1", workspace_id: "workspace-1",
      path: "README.md", operation: "replace", status: "proposed",
      diff: "--- a/README.md\n+++ b/README.md\n",
      original_hash: "a".repeat(64), proposed_hash: "b".repeat(64),
      secrets_redacted: false, allowed_actions: ["approve_intent", "deny"],
      apply_enabled: false, created_at: "2026-07-18T00:00:00Z",
      updated_at: "2026-07-18T00:00:00Z" };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-source", data: source }), { status: 200,
        headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-reissue", data: reissued }), { status: 200,
        headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-recovery", data: {
          protocol_version: "file_edit_proposal_recovery.v1", run_id: "run-1",
          workspace_id: "workspace-1", edit_id: "edit-1", path: "README.md",
          original_content: "before\n", proposed_content: "after\n",
          original_sha256: "a".repeat(64), proposed_sha256: "b".repeat(64),
          current_content_sha256: "a".repeat(64), status: "proposed", stale: false,
          review_required: true, editable: false, file_write: false,
        } }), { status: 200, headers: { "Content-Type": "application/json" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ version: "api.v1",
        request_id: "req-proposal", data: { protocol_version: "file_edit_proposal.v1",
          run_id: "run-1", edit, replayed: false, approval_required: true,
          file_written: false } }), { status: 202,
        headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret",
      { fileEditProposalEnabled: true });
    await expect(client.issueFileEditProposalSource("run-1", "README.md"))
      .resolves.toEqual(source);
    await expect(client.reissueFileEditProposalSource("run-1", "README.md",
      source.content_sha256)).resolves.toEqual(reissued);
    await expect(client.recoverFileEditProposal("run-1", "edit-1"))
      .resolves.toMatchObject({ edit_id: "edit-1", stale: false, editable: false });
    await expect(client.createFileEditProposal("run-1", {
      version: "file_edit_proposal.v1", source_handle: reissued.source_handle,
      proposed_text: "after\n",
    })).resolves.toMatchObject({ approval_required: true, file_written: false,
      edit: { status: "proposed" } });
    const [url, init] = fetchMock.mock.calls[3] as [string, RequestInit];
    expect(url).toBe("/api/v1/runs/run-1/file-edit-proposals");
    expect(JSON.parse(String(init.body))).toEqual({ version: "file_edit_proposal.v1",
      source_handle: reissued.source_handle, proposed_text: "after\n" });
    expect(String(init.body)).not.toContain("README.md");
  });

  it("accepts a read-only recovery for a still-missing proposed file", async () => {
    const recovery = { protocol_version: "file_edit_proposal_recovery.v1", run_id: "run-1",
      workspace_id: "workspace-1", edit_id: "edit-new", path: "outputs/new.txt",
      original_content: "", proposed_content: "new file\n", original_sha256: "missing",
      proposed_sha256: "b".repeat(64), current_content_sha256: "missing",
      status: "proposed", stale: false, review_required: true, editable: false,
      file_write: false };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-new-file-recovery", data: recovery,
    }), { status: 200, headers: { "Content-Type": "application/json" } })));
    const client = new APIClient("read-secret", "/api/v1", "control-secret",
      { fileEditProposalEnabled: true });
    await expect(client.recoverFileEditProposal("run-1", "edit-new"))
      .resolves.toEqual(recovery);
  });

  it("binds a no-write inverse proposal to the exact source and existing review authority", async () => {
    const edit = { id: "edit-inverse", session_id: "session-1", workspace_id: "workspace-1",
      path: "README.md", operation: "replace", status: "proposed",
      diff: "--- a/README.md\n+++ b/README.md\n-after\n+before\n",
      original_hash: "b".repeat(64), proposed_hash: "a".repeat(64),
      secrets_redacted: false, allowed_actions: ["approve_intent", "deny"],
      apply_enabled: false, created_at: "2026-07-18T00:00:00Z", updated_at: "2026-07-18T00:00:00Z" };
    const data = { protocol_version: "file_edit_proposal.v1", run_id: "run-1", source_edit_id: "edit-source",
      edit, replayed: false, file_written: false };
    const response = (value: unknown) => new Response(JSON.stringify({ version: "api.v1", request_id: "req-inverse", data: value }),
      { status: 200, headers: { "Content-Type": "application/json" } });
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(response(data)));
    vi.stubGlobal("fetch", fetchMock);
    for (const disabled of [new APIClient("read-secret", "/api/v1", "control-secret", { fileEditReviewEnabled: false }),
      new APIClient("read-secret", "/api/v1", "", { fileEditReviewEnabled: true }),
      new APIClient("read-secret", "/api/v1", "control-secret", { fileEditProposalEnabled: true, fileEditReviewEnabled: false })]) {
      await expect(disabled.createFileEditRevertProposal("run-1", "edit-source", "web-revert-contract-key-1")).rejects.toThrow(/review authority/);
    }
    expect(fetchMock).not.toHaveBeenCalled();
    const client = new APIClient("read-secret", "/api/v1", "control-secret", { fileEditReviewEnabled: true });
    await expect(client.createFileEditRevertProposal("run-1", "edit-source", "web-revert-contract-key-1")).resolves.toEqual(data);
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/v1/runs/run-1/file-edits/edit-source/revert-proposal");
    expect(JSON.parse(String(init.body))).toEqual({ version: "file_edit_proposal.v1" });
    expect(new Headers(init.headers).get("Authorization")).toBe("Bearer control-secret");
    expect(new Headers(init.headers).get("Idempotency-Key")).toBe("web-revert-contract-key-1");
    const replay = { ...data, replayed: true, edit: { ...edit, status: "applied", allowed_actions: [] } };
    fetchMock.mockResolvedValueOnce(response(replay));
    await expect(client.createFileEditRevertProposal("run-1", "edit-source", "web-revert-contract-key-1")).resolves.toEqual(replay);
    for (const invalid of [{ ...data, run_id: "other-run" }, { ...data, source_edit_id: "other-source" },
      { ...data, file_written: true }, { ...data, approval_required: true },
      { ...data, edit: { ...edit, id: "edit-source" } }, { ...data, edit: { ...edit, status: "applied", allowed_actions: [] } },
      { ...data, edit: { ...edit, secrets_redacted: true } }]) {
      fetchMock.mockResolvedValueOnce(response(invalid));
      await expect(client.createFileEditRevertProposal("run-1", "edit-source", "web-revert-contract-key-1")).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    }
  });

  it("accepts bounded Code Intel health without exposing process launch details", async () => {
    const capabilities = { workspace_symbols: true, document_symbols: true,
      definition: true, references: true, implementation: true, hover: true,
      signature_help: true, diagnostics: true, call_hierarchy: true,
      type_hierarchy: true };
    const data = { protocol_version: "code-intel-lsp.v1", enabled: true,
      servers: [{ protocol_version: "code-intel-lsp.v1", server_id: "gopls",
        server_name: "gopls", workspace_id: "workspace-1", languages: ["go"],
        source_kind: "operator_config", source_label: "code-intel.json",
        source_sha256: "a".repeat(64), descriptor_fingerprint: "b".repeat(64),
        capability_fingerprint: "c".repeat(64), generation: "d".repeat(64),
        health: "healthy", capabilities, model_visible_tools: [
          "code_workspace_symbols", "code_document_symbols", "code_definition",
          "code_references", "code_implementation", "code_hover",
          "code_signature_help", "code_diagnostics", "code_call_hierarchy",
          "code_type_hierarchy"], server_version: "v0.20.0", process_owned: true,
        read_only: true, network_access_granted: false, credentials_granted: false,
        shell_profile_loaded: false, qualified_at: "2026-08-20T01:00:00Z" }],
      qualifications: [{ protocol_version: "code-intel-lsp.v1", server_id: "gopls",
        workspace_id: "workspace-1", eligible: true, health: "configured",
        descriptor_fingerprint: "b".repeat(64), executable_hash_matched: true,
        reviewed: true, process_owned: true, minimal_environment: true,
        network_access_granted: false, credentials_granted: false,
        shell_profile_loaded: false }] };
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-code-intel", data,
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(new APIClient("read-secret").codeIntelInventory("workspace-1"))
      .resolves.toEqual(data);
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/code-intel?workspace_id=workspace-1");
    expect(JSON.stringify(data)).not.toMatch(
      /"(?:executable|arguments|argv|environment|credential|token)"\s*:/i,
    );

    const unsortedLanguages = structuredClone(data);
    unsortedLanguages.servers[0].languages = ["typescript", "go"];
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-code-intel-unsorted", data: unsortedLanguages,
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    await expect(new APIClient("read-secret").codeIntelInventory("workspace-1"))
      .rejects.toMatchObject({ code: "INVALID_RESPONSE" });

    const contradictoryQualification = { ...structuredClone(data),
      qualifications: data.qualifications.map((item) => ({ ...item, reason: "unexpected" })) };
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-code-intel-qualification",
      data: contradictoryQualification,
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    await expect(new APIClient("read-secret").codeIntelInventory("workspace-1"))
      .rejects.toMatchObject({ code: "INVALID_RESPONSE" });
  });

  it.each(["executable", "environment", "token"])(
    "rejects a Code Intel projection containing forbidden %s metadata", async (field) => {
      const data = { protocol_version: "code-intel-lsp.v1", enabled: true,
        servers: [], qualifications: [], [field]: "must-not-cross" };
      vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
        version: "api.v1", request_id: "req-code-intel-invalid", data,
      }), { status: 200, headers: { "Content-Type": "application/json" } })));
      await expect(new APIClient("read-secret").codeIntelInventory())
        .rejects.toMatchObject({ code: "INVALID_RESPONSE" });
    });

  it("keeps extension reads scoped and sends pinned disable controls without secrets", async () => {
    const digestA = "a".repeat(64);
    const digestB = "b".repeat(64);
    const server = { protocol_version: "mcp-client-server.v1", id: "mcp-local",
      name: "Local MCP", transport: "stdio", target: "C:\\tools\\mcp.exe",
      credential_ref: "", declared_capabilities: ["tools"], scope: "run",
      run_id: "run-1", workspace_id: "workspace-1",
      source: { kind: "manual", uri: "operator" }, descriptor_fingerprint: digestA,
      state: "enabled", capabilities: { negotiated: ["tools"], tools: ["inspect"],
        resources: [], prompts: [], fingerprint: digestB },
      approved_capability_fingerprint: digestB, health: "healthy", generation: 2,
      created_at: "2026-08-20T01:00:00Z", updated_at: "2026-08-20T01:01:00Z" };
    const plugin = { protocol_version: "plugin-installation.v1", id: "plugin-local",
      manifest: { id: "review", name: "Review", version: "1.0.0", publisher: "local",
        description: "review", capabilities: ["hooks"] },
      source: { kind: "local_file", uri: "C:\\plugins\\review.zip", sha256: digestA },
      archive_sha256: digestA, package_fingerprint: digestB, signature_present: false,
      signature_valid: false, state: "enabled", enabled_capabilities: ["hooks"],
      generation: 4, staged_by: "cli_operator", created_at: "2026-08-20T01:00:00Z",
      updated_at: "2026-08-20T01:01:00Z" };
    const envelope = (requestID: string, data: unknown) => new Response(JSON.stringify({
      version: "api.v1", request_id: requestID, data,
    }), { status: 200, headers: { "Content-Type": "application/json" } });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(envelope("req-extensions", { protocol_version: "extension-inventory.v1",
        run_id: "run-1", workspace_id: "workspace-1", mcp_servers: [server],
        mcp_calls: [], plugins: [plugin] }))
      .mockResolvedValueOnce(envelope("req-disable-mcp", { ...server, state: "disabled" }))
      .mockResolvedValueOnce(envelope("req-disable-plugin", { ...plugin, state: "disabled",
        enabled_capabilities: [], generation: 5 }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret",
      { extensionControlEnabled: true });

    await expect(client.extensionInventory("run-1")).resolves.toMatchObject({
      run_id: "run-1", mcp_servers: [{ id: "mcp-local" }], plugins: [{ id: "plugin-local" }],
    });
    await client.reviewMCPServer("mcp-local", { version: "extension-control.v1",
      action: "disable", expected_descriptor_fingerprint: digestA });
    await client.reviewPluginInstallation("plugin-local", { version: "extension-control.v1",
      action: "disable", expected_package_fingerprint: digestB, expected_generation: 4,
      confirm_untrusted: false });

    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/extensions?run_id=run-1");
    for (const call of fetchMock.mock.calls.slice(1)) {
      const [url, init] = call as [string, RequestInit];
      expect(url).not.toContain("control-secret");
      expect(init.headers).toMatchObject({ Authorization: "Bearer control-secret" });
      expect(String(init.body)).not.toMatch(/credential|secret|token|arguments|result/);
    }
  });

  it("rejects secret-bearing extension projections", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-extension-secret", data: {
        protocol_version: "extension-inventory.v1", mcp_servers: [], mcp_calls: [],
        plugins: [], secret: "must-not-cross-the-boundary",
      },
    }), { status: 200, headers: { "Content-Type": "application/json" } })));
    await expect(new APIClient("read-secret").extensionInventory())
      .rejects.toThrow("invalid");
  });

  it("expands Run network authority only for exact revision-bound HTTPS hosts", async () => {
    const data = {
      version: "run_network_authority_control.v1", run_id: "run-created",
      mode: { ...runCreationData.mode, revision: 2,
        scope: { workspace_id: "workspace-1", network_mode: "allowlist",
          allowed_targets: ["search.example.org"] } },
      added_targets: ["search.example.org"], replayed: false, capability_grant: true,
    };
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-network-authority", data,
    }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret");
    const result = await client.expandRunNetworkAuthority("run-created", {
      version: "run_network_authority_control.v1", expected_mode_revision: 1,
      add_allowed_targets: ["https://SEARCH.Example.org/"],
      reason: "operator approved search",
    }, "web-network-authority-0001");
    expect(result).toMatchObject({ run_id: "run-created", capability_grant: true,
      mode: { revision: 2, scope: { allowed_targets: ["search.example.org"] } } });
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/runs/run-created/network-authority");
    expect(init.headers).toMatchObject({ Authorization: "Bearer control-secret",
      "Idempotency-Key": "web-network-authority-0001" });
    await expect(client.expandRunNetworkAuthority("run-created", {
      version: "run_network_authority_control.v1", expected_mode_revision: 2,
      add_allowed_targets: ["public_https"],
    }, "web-network-authority-0002")).rejects.toThrow("exact public HTTPS hosts");
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("rejects a Run network authority response with an unrequested grant", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-network-authority-forged", data: {
        version: "run_network_authority_control.v1", run_id: "run-created",
        mode: { ...runCreationData.mode, revision: 2,
          scope: { workspace_id: "workspace-1", network_mode: "allowlist",
            allowed_targets: ["other.example.org", "search.example.org"] } },
        added_targets: ["other.example.org"], replayed: false, capability_grant: true,
      },
    }), { status: 202, headers: { "Content-Type": "application/json" } })));
    const client = new APIClient("read-secret", "/api/v1", "control-secret");
    await expect(client.expandRunNetworkAuthority("run-created", {
      version: "run_network_authority_control.v1", expected_mode_revision: 1,
      add_allowed_targets: ["search.example.org"],
    }, "web-network-authority-forged-0001")).rejects.toThrow("exact target grant");
  });

  it("rejects a non-canonical Run network authority response", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-network-authority-noncanonical", data: {
        version: "run_network_authority_control.v1", run_id: "run-created",
        mode: { ...runCreationData.mode, revision: 2,
          scope: { workspace_id: "workspace-1", network_mode: "allowlist",
            allowed_targets: ["SEARCH.Example.org"] } },
        added_targets: ["SEARCH.Example.org"], replayed: false, capability_grant: true,
      },
    }), { status: 202, headers: { "Content-Type": "application/json" } })));
    const client = new APIClient("read-secret", "/api/v1", "control-secret");
    await expect(client.expandRunNetworkAuthority("run-created", {
      version: "run_network_authority_control.v1", expected_mode_revision: 1,
      add_allowed_targets: ["search.example.org"],
    }, "web-network-authority-noncanonical-0001")).rejects.toThrow("exact target grant");
  });

  it("rejects malformed nested Run mode authority projections", async () => {
    for (const [index, mutation] of [
      { unexpected: true },
      { created_at: "not-a-date" },
      { profile: 42 },
      { surface: ["code"] },
    ].entries()) {
      const mode = { ...runCreationData.mode, revision: 2, ...mutation,
        scope: { workspace_id: "workspace-1", network_mode: "allowlist",
          allowed_targets: ["search.example.org"] } };
      vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
        version: "api.v1", request_id: `req-network-authority-nested-${index}`, data: {
          version: "run_network_authority_control.v1", run_id: "run-created", mode,
          added_targets: ["search.example.org"], replayed: false, capability_grant: true,
        },
      }), { status: 202, headers: { "Content-Type": "application/json" } })));
      const client = new APIClient("read-secret", "/api/v1", "control-secret");
      await expect(client.expandRunNetworkAuthority("run-created", {
        version: "run_network_authority_control.v1", expected_mode_revision: 1,
        add_allowed_targets: ["search.example.org"],
      }, `web-network-authority-nested-${index}`)).rejects.toThrow(/Run mode|invalid/i);
    }
  });

  it("creates a Thread with exact network authority through the Thread-first contract", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-thread-create-network",
      data: networkThreadCreationData,
    }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret");

    const result = await client.createThread({
      version: "thread_creation.v1", goal: "Create parser", workspace_id: "workspace-1",
      network_mode: "allowlist",
      allowed_targets: ["HTTPS://Docs.Example.COM:443/", "api.example.com"],
    }, "web-thread-create-network-operation-0001");

    expect(result.thread).toEqual(threadData);
    expect(result.mission.scope).toMatchObject({ network_mode: "allowlist",
      allowed_targets: ["api.example.com", "docs.example.com"] });
  });

  it("creates a Run with a canonical exact network allowlist", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-create-network", data: networkRunCreationData,
    }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runCreationEnabled: true,
    });
    const result = await client.createRun({
      version: "run_creation.v1", goal: "Create parser", workspace_id: "workspace-1",
      network_mode: "allowlist",
      allowed_targets: ["HTTPS://Docs.Example.COM.:443/", "api.example.com",
        "docs.example.com"],
    }, "web-run-create-network-operation-0001");
    expect(result.mission.scope).toMatchObject({ network_mode: "allowlist",
      allowed_targets: ["api.example.com", "docs.example.com"] });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("rejects broad or malformed creation network authority before transport", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      runCreationEnabled: true,
    });
    for (const [index, authority] of [
      { network_mode: "allowlist" as const, allowed_targets: [] },
      { network_mode: "allowlist" as const, allowed_targets: ["*.example.com"] },
      { network_mode: "allowlist" as const, allowed_targets: ["http://docs.example.com"] },
      { network_mode: "allowlist" as const, allowed_targets: ["https://8.8.8.8"] },
      { network_mode: "disabled" as const, allowed_targets: ["docs.example.com"] },
    ].entries()) {
      await expect(client.createRun({ version: "run_creation.v1", goal: "Create parser",
        workspace_id: "workspace-1", ...authority },
      `web-run-create-invalid-network-${index}`)).rejects.toThrow(/network|allowlist/i);
    }
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("controls a redacted Full CDP production session lifecycle", async () => {
    const startedAt = "2026-08-30T01:02:03Z";
    const expiresAt = "2026-08-30T01:17:03Z";
    const none = { version: "full_cdp_session.v1", run_id: "run-1", state: "none",
      runtime_available: true, cdp_closed: false, process_tree_quiescent: false,
      profile_released: false, profile_cleaned: false };
    const ready = { ...none, session_id: "full_cdp_session-client-0001", state: "ready",
      browser: { product: "edge", channel: "stable" },
      target_origin: "http://127.0.0.1:18080", started_at: startedAt, expires_at: expiresAt };
    const closed = { ...ready, state: "closed", completed_at: "2026-08-30T01:03:03Z",
      close_reason: "operator_closed", cdp_closed: true, process_tree_quiescent: true,
      profile_released: true, profile_cleaned: true };
    const envelope = (requestID: string, session: unknown) => new Response(JSON.stringify({
      version: "api.v1", request_id: requestID, data: { session, replayed: false },
    }), { status: 200, headers: { "Content-Type": "application/json" } });
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(envelope("req-full-cdp-get", none))
      .mockResolvedValueOnce(envelope("req-full-cdp-open", ready))
      .mockResolvedValueOnce(envelope("req-full-cdp-close", closed));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      browserCDPPermissionControlEnabled: true,
      fullCDPDebugEnabled: true,
      fullCDPSessionControlEnabled: true,
    });

    await expect(client.getFullCDPSession("run-1")).resolves.toMatchObject({ session: none });
    await expect(client.openFullCDPSession("run-1", {
      version: "full_cdp_session.v1", target: "http://127.0.0.1:18080",
      browser: { product: "edge", channel: "stable" },
      expected_execution_permission_revision: 4,
      expected_browser_cdp_permission_revision: 6,
      confirm_full_cdp: true,
    }, "full-cdp-client-open-0001")).resolves.toMatchObject({ session: ready });
    await expect(client.closeFullCDPSession("run-1", {
      version: "full_cdp_session_close.v1",
      expected_session_id: "full_cdp_session-client-0001",
    }, "full-cdp-client-close-0001")).resolves.toMatchObject({ session: closed });

    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/runs/run-1/full-cdp-session");
    expect((fetchMock.mock.calls[0][1] as RequestInit).headers)
      .toMatchObject({ Authorization: "Bearer read-secret" });
    expect((fetchMock.mock.calls[1][1] as RequestInit).headers)
      .toMatchObject({ Authorization: "Bearer control-secret",
        "Idempotency-Key": "full-cdp-client-open-0001" });
    expect(fetchMock.mock.calls[2][0]).toBe("/api/v1/runs/run-1/full-cdp-session/close");
  });

  it("accepts pre-ready and staged Full CDP cleanup views", async () => {
    const base = { version: "full_cdp_session.v1", run_id: "run-1",
      session_id: "full_cdp_session-recovery-0001", state: "closing",
      browser: { product: "chrome", channel: "stable" }, runtime_available: true,
      process_tree_quiescent: false, profile_released: false, profile_cleaned: false };
    const preReady = { ...base, close_reason: "open_failed", cdp_closed: false,
      failure_code: "open_failed" };
    const staged = { ...base, close_reason: "permission_revoked", cdp_closed: true,
      target_origin: "http://127.0.0.1:18080", started_at: "2026-08-30T01:02:03Z",
      expires_at: "2026-08-30T01:17:03Z", failure_code: "cleanup_recovery_required" };
    const envelope = (requestID: string, session: unknown) => new Response(JSON.stringify({
      version: "api.v1", request_id: requestID, data: { session, replayed: false },
    }), { status: 200, headers: { "Content-Type": "application/json" } });
    vi.stubGlobal("fetch", vi.fn()
      .mockResolvedValueOnce(envelope("req-full-cdp-pre-ready", preReady))
      .mockResolvedValueOnce(envelope("req-full-cdp-staged", staged)));
    const client = new APIClient("read-secret", "/api/v1", "control-secret", {
      browserCDPPermissionControlEnabled: true,
      fullCDPDebugEnabled: true,
      fullCDPSessionControlEnabled: true,
    });

    await expect(client.getFullCDPSession("run-1"))
      .resolves.toMatchObject({ session: preReady });
    await expect(client.getFullCDPSession("run-1"))
      .resolves.toMatchObject({ session: staged });
  });

  it("executes the embedded analyzer through the control token and validates its redacted receipt", async () => {
    const data = {
      version: "embedded_analyzer_execution_control.v1",
      execution_id: "execution-1", artifact_id: "artifact-1", run_id: "run-1",
      session_id: "session-1", workspace_id: "workspace-1",
      analyzer: "fixture.digest.v1", status: "succeeded", media_type: "text/plain",
      input_bytes: 12, line_count: 2, sha256: "a".repeat(64), utf8: true,
      metadata_only: true, capability_consumed: true, artifact_atomic: true,
      filesystem_mounted: false, network_enabled: false, subprocess_enabled: false,
      host_process_authorized: false, raw_request_included: false,
      bearer_token_included: false, replayed: false,
    };
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      version: "api.v1", request_id: "req-analyzer", data,
    }), { status: 201, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new APIClient("read-secret", "/api/v1", "control-secret",
      { embeddedAnalyzerExecutionEnabled: true });

    await expect(client.executeEmbeddedAnalyzer("run-1", {
      version: "embedded_analyzer_operator_request.v1", text: "hello\nworld\n",
      media_type: "text/plain", confirmation: "RUN-EMBEDDED-ANALYZER",
    })).resolves.toEqual(data);

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/runs/run-1/analyzer-executions");
    expect(init.headers).toMatchObject({ Authorization: "Bearer control-secret" });
    expect(url).not.toContain("control-secret");
  });
});

function operationReceipt(kind: "file_edit_apply" | "run_wake_consume" | "skill_package_install",
  outcome: "applied" | "completed" | "installed",
  retryStrategy: "same_operation_key" | "same_wake_generation",
  cleanupState: "complete" | "not_applicable") {
  return { protocol_version: "operation_receipt.v1", kind, outcome, durable: true,
    replayed: false, retry_safe: true, retry_strategy: retryStrategy,
    recovery_action: "none", cleanup_state: cleanupState };
}

describe("project metadata", () => {
  afterEach(() => vi.unstubAllGlobals());
  it("reads private details and renames with the control bearer", async () => {
    const data={id:"ws-project",name:"项目 / Example",created_at:"2026-10-20T00:00:00Z",directory_path:"D:/projects/example",thread_count:19,active_thread_count:2};
    const fetchMock=vi.fn().mockImplementation(async()=>new Response(JSON.stringify({version:"api.v1",request_id:"project",data}),{status:200}));vi.stubGlobal("fetch",fetchMock);
    const client=new APIClient("read-secret","/api/v1","control-secret");
    expect(await client.getWorkspaceProject(data.id)).toEqual(data);
    expect(await client.renameWorkspace(data.id," 项目 / Example ")).toEqual(data);
    expect(fetchMock.mock.calls[0][1]).toMatchObject({method:"GET",headers:{Authorization:"Bearer control-secret"}});
    expect(fetchMock.mock.calls[1][0]).toBe("/api/v1/workspaces/ws-project/name");
    expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toEqual({name:data.name});
    await expect(client.renameWorkspace(data.id,"bad\nname")).rejects.toMatchObject({code:"INVALID_ARGUMENT"});
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });
  it("imports a supplied display name without changing old call signatures",async()=>{
    const data={protocol_version:"workspace_import.v1",workspace:{id:"ws-project",name:"Name / name",created_at:"2026-10-20T00:00:00Z"},directory_content_modified:false,agent_authority_granted:false};
    const fetchMock=vi.fn().mockResolvedValue(new Response(JSON.stringify({version:"api.v1",request_id:"project",data}),{status:200}));vi.stubGlobal("fetch",fetchMock);
    const client=new APIClient("read-secret","/api/v1","control-secret",{workspaceImportEnabled:true});
    expect(await client.importWorkspace("D:/projects/name",undefined," Name / name ")).toEqual(data);
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toMatchObject({name:"Name / name"});
  });
});
