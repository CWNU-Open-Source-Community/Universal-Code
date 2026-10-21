import { validVerificationFeatureReadiness } from "../../verification-readiness";
import { APIRequestError } from "../protocol";
import {
  boundedIdentity,
  hasExactKeys,
  hasOnlyKeys,
  isRecord,
  isSHA256,
  safeBoundedCount,
} from "../validation";
import type {
  BrowserSafeWebReadiness,
  EmbeddedAnalyzerExecutionControlView,
  RunCapabilityReadinessView,
  RuntimeCapabilitiesView,
  StandardCodePresetControlView,
} from "../../types";

export function parseRuntimeCapabilities(value: unknown): RuntimeCapabilitiesView {
  // Older servers cannot grant a capability they do not advertise.
  if (isRecord(value) && !Object.hasOwn(value, "workspace_import_enabled")) {
    value = { ...value, workspace_import_enabled: false };
  }
  const capabilityKeys = ["agent_code_tools_enabled", "approval_control_enabled",
    "workspace_import_enabled",
    "command_runtime_enabled", "command_runtime_protocol_available",
    "command_runtime_adapter_installed", "command_runtime_adapter_ready",
    "command_runtime_adapters", "docker_execution_enabled",
    "code_intel_enabled",
    "browser_cdp_permission_control_enabled", "full_cdp_debug_enabled",
    "full_cdp_session_control_enabled",
    "execution_permission_control_enabled", "workspace_sandbox_enabled",
    "operator_approval_enabled",
    "danger_full_access_enabled",
    "evidence_attachment_enabled", "verification_evidence_enabled",
    "embedded_analyzer_execution_enabled", "workspace_checkpoint_control_enabled",
    "git_advanced_control_enabled", "github_review_control_enabled",
    "batch_delivery_control_enabled", "batch_delivery_host_validation_enabled",
    "ui_evidence_control_enabled",
    "file_edit_apply_enabled", "file_edit_proposal_enabled",
    "file_edit_review_enabled", "model_control_enabled", "plan_delivery_control_enabled",
    "process_execution_enabled", "provider_credential_enabled", "protocol_version",
    "run_control_enabled", "run_creation_enabled", "standard_code_preset_enabled",
    "run_execution_enabled",
    "run_lifecycle_enabled", "run_wake_control_enabled", "run_wake_execution_enabled",
    "run_wake_worker_enabled", "scheduled_job_control_enabled",
    "scheduled_job_worker_enabled", "scheduled_job_worker", "session_message_enabled",
    "session_steering_control_enabled", "shell_execution_enabled",
    "skill_installation_enabled", "thread_control_enabled", "wake_worker"];
  if (isRecord(value) && Object.hasOwn(value, "thread_execution_read_enabled")) {
    capabilityKeys.push("thread_execution_read_enabled");
  }
  if (isRecord(value) && Object.hasOwn(value, "projectless_workspaces_enabled")) {
    capabilityKeys.push("projectless_workspaces_enabled");
  }
  if (isRecord(value) && Object.hasOwn(value, "chatgpt_auth_enabled")) {
    capabilityKeys.push("chatgpt_auth_enabled");
  }
  if (!hasExactKeys(value, capabilityKeys) || value.protocol_version !== "runtime_capabilities.v1") {
    throw new APIRequestError("Runtime capability response is invalid", "INVALID_RESPONSE", 502);
  }
  for (const key of capabilityKeys) {
    if (key !== "protocol_version" && key !== "wake_worker" && key !== "scheduled_job_worker" &&
      key !== "command_runtime_adapters" &&
      typeof value[key] !== "boolean") {
      throw new APIRequestError("Runtime capability flag is invalid", "INVALID_RESPONSE", 502);
    }
  }
  if (value.command_runtime_protocol_available !== true ||
    !Array.isArray(value.command_runtime_adapters) ||
    value.command_runtime_adapters.length > 8) {
    throw new APIRequestError("Command Runtime adapter capabilities are invalid",
      "INVALID_RESPONSE", 502);
  }
  const commandRuntimeAdapters = value.command_runtime_adapters.map((adapter) => {
    if (!hasExactKeys(adapter, ["backend", "backend_identity", "credential_policy",
      "isolation_grade", "kind", "network_policy", "ready"]) ||
      !boundedIdentity(adapter.backend) || !boundedIdentity(adapter.backend_identity) ||
      typeof adapter.ready !== "boolean") {
      throw new APIRequestError("Command Runtime adapter receipt is invalid",
        "INVALID_RESPONSE", 502);
    }
    const sandboxed = adapter.kind === "sandboxed_workspace" &&
      adapter.isolation_grade === "workspace_sandbox" &&
      adapter.network_policy === "denied" && adapter.credential_policy === "none";
    const host = adapter.kind === "host_unsandboxed" &&
      adapter.isolation_grade === "host_unsandboxed" &&
      adapter.network_policy === "host_available" &&
      adapter.credential_policy === "host_available";
    if (!sandboxed && !host) {
      throw new APIRequestError("Command Runtime adapter receipt is inconsistent",
        "INVALID_RESPONSE", 502);
    }
    return adapter;
  });
  if (new Set(commandRuntimeAdapters.map((adapter) => adapter.backend)).size !==
      commandRuntimeAdapters.length ||
    value.command_runtime_adapter_installed !== (commandRuntimeAdapters.length > 0) ||
    value.command_runtime_adapter_ready !==
      commandRuntimeAdapters.some((adapter) => adapter.ready) ||
    value.command_runtime_enabled !==
      (value.run_execution_enabled && value.command_runtime_adapter_ready)) {
    throw new APIRequestError("Command Runtime adapter capability summary is inconsistent",
      "INVALID_RESPONSE", 502);
  }
  const worker = value.wake_worker;
  const scheduledWorker = value.scheduled_job_worker;
  if (!hasExactKeys(worker, ["active", "concurrency", "enabled", "max_steps",
    "persistent_service", "poll_interval_ms", "protocol_version",
    "runtime_enable_supported", "state"]) ||
    worker.protocol_version !== "run_wake_worker_health.v1" ||
    typeof worker.enabled !== "boolean" || typeof worker.active !== "boolean" ||
    worker.concurrency !== 1 || worker.max_steps !== 1 ||
    worker.runtime_enable_supported !== false || worker.persistent_service !== false ||
    value.run_wake_worker_enabled !== worker.enabled ||
    (worker.enabled && (!["ready", "running", "draining", "stopped"].includes(String(worker.state)) ||
      !safeBoundedCount(worker.poll_interval_ms, 60_000) || worker.poll_interval_ms < 250)) ||
    ((worker.state === "ready" || worker.state === "stopped") && worker.active) ||
    (!worker.enabled && (worker.state !== "disabled" || worker.active || worker.poll_interval_ms !== 0)) ||
    !hasExactKeys(scheduledWorker, ["active", "authority_escalation", "concurrency",
      "enabled", "persistent_service", "poll_interval_ms", "protocol_version",
      "runtime_enable_supported", "state",
      ...(isRecord(scheduledWorker) && "selection_scope" in scheduledWorker ? ["selection_scope"] : [])]) ||
    (scheduledWorker.selection_scope !== undefined &&
      scheduledWorker.selection_scope !== "confirmed_read_only" && scheduledWorker.selection_scope !== "confirmed_jobs" &&
      scheduledWorker.selection_scope !== "all_jobs") ||
    scheduledWorker.protocol_version !== "scheduled-job-worker-health.v1" ||
    typeof scheduledWorker.enabled !== "boolean" || typeof scheduledWorker.active !== "boolean" ||
    scheduledWorker.concurrency !== 1 || scheduledWorker.runtime_enable_supported !== false ||
    scheduledWorker.persistent_service !== false || scheduledWorker.authority_escalation !== false ||
    value.scheduled_job_worker_enabled !== scheduledWorker.enabled ||
    (scheduledWorker.enabled &&
      (!['ready', 'running', 'draining', 'stopped'].includes(String(scheduledWorker.state)) ||
        !safeBoundedCount(scheduledWorker.poll_interval_ms, 60_000) ||
        scheduledWorker.poll_interval_ms < 250)) ||
    ((scheduledWorker.state === "ready" || scheduledWorker.state === "stopped") &&
      scheduledWorker.active) ||
    (!scheduledWorker.enabled && (scheduledWorker.state !== "disabled" ||
      scheduledWorker.active || scheduledWorker.poll_interval_ms !== 0)) ||
    (value.full_cdp_debug_enabled && (!value.browser_cdp_permission_control_enabled ||
      !value.danger_full_access_enabled)) ||
    (value.full_cdp_session_control_enabled && (!value.full_cdp_debug_enabled ||
      !value.browser_cdp_permission_control_enabled || !value.danger_full_access_enabled)) ||
    (value.workspace_sandbox_enabled && !value.execution_permission_control_enabled) ||
    value.thread_control_enabled !==
      (value.run_creation_enabled && value.session_message_enabled) ||
    (value.batch_delivery_host_validation_enabled &&
      (!value.batch_delivery_control_enabled || !value.execution_permission_control_enabled ||
        !value.operator_approval_enabled || !value.danger_full_access_enabled)) ||
    (value.ui_evidence_control_enabled && (!value.command_runtime_enabled ||
      !value.browser_cdp_permission_control_enabled)) ||
    value.process_execution_enabled !== value.command_runtime_enabled ||
    value.shell_execution_enabled !== value.command_runtime_enabled ||
    (value.docker_execution_enabled && !value.execution_permission_control_enabled) ||
    (value.git_advanced_control_enabled &&
      (!value.execution_permission_control_enabled || !value.operator_approval_enabled ||
        !value.workspace_checkpoint_control_enabled)) ||
    (value.github_review_control_enabled &&
      (!value.execution_permission_control_enabled || !value.operator_approval_enabled))) {
    throw new APIRequestError("Run wake worker capability response is invalid",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as RuntimeCapabilitiesView;
}

export const capabilityReadinessBlockers = [
  "run_not_quiescent", "execution_lease_active", "startup_gate_closed",
  "capability_not_implemented", "surface_mismatch", "profile_mismatch",
  "permission_mismatch", "workspace_untrusted", "sandbox_unproven",
  "docker_unavailable", "backend_not_ready",
  "sbx_unavailable",
] as const;

export const capabilityReadinessRemediations = [
  "pause_run", "create_new_run", "wait_for_execution_lease",
  "restart_with_startup_gate", "upgrade_application", "select_required_surface",
  "select_required_profile", "select_required_permission", "trust_workspace",
  "verify_sandbox", "install_or_start_docker", "retry_backend_readiness",
  "install_or_start_sbx",
] as const;

export type ReadinessBlocker = typeof capabilityReadinessBlockers[number];

export type ReadinessRemediation = typeof capabilityReadinessRemediations[number];

export const readinessRemediationForBlocker: Record<ReadinessBlocker,
  ReadinessRemediation | readonly ReadinessRemediation[]> = {
  run_not_quiescent: ["pause_run", "create_new_run"],
  execution_lease_active: "wait_for_execution_lease",
  startup_gate_closed: "restart_with_startup_gate",
  capability_not_implemented: "upgrade_application",
  surface_mismatch: "select_required_surface",
  profile_mismatch: "select_required_profile",
  permission_mismatch: "select_required_permission",
  workspace_untrusted: "trust_workspace",
  sandbox_unproven: "verify_sandbox",
  docker_unavailable: "install_or_start_docker",
  sbx_unavailable: "install_or_start_sbx",
  backend_not_ready: "retry_backend_readiness",
};

export function parseCapabilityReadinessOption(value: unknown,
  expectedValue: string): Record<string, unknown> {
  if (!hasExactKeys(value, ["blocked_by", "remediation", "restart_required",
    "runtime_available", "selectable", "selected", "value"]) ||
    value.value !== expectedValue || typeof value.selected !== "boolean" ||
    typeof value.selectable !== "boolean" || typeof value.runtime_available !== "boolean" ||
    typeof value.restart_required !== "boolean" || !Array.isArray(value.blocked_by) ||
    !Array.isArray(value.remediation)) {
    throw new APIRequestError("Capability readiness option is invalid", "INVALID_RESPONSE", 502);
  }
  const blockers = value.blocked_by;
  const remediation = value.remediation;
  if (blockers.length > capabilityReadinessBlockers.length ||
    remediation.length > capabilityReadinessRemediations.length ||
    blockers.some((entry) => typeof entry !== "string" ||
      !capabilityReadinessBlockers.includes(entry as ReadinessBlocker)) ||
    remediation.some((entry) => typeof entry !== "string" ||
      !capabilityReadinessRemediations.includes(entry as ReadinessRemediation)) ||
    new Set(blockers).size !== blockers.length || new Set(remediation).size !== remediation.length ||
    blockers.some((entry, index) => index > 0 &&
      capabilityReadinessBlockers.indexOf(entry as ReadinessBlocker) <=
        capabilityReadinessBlockers.indexOf(blockers[index - 1] as ReadinessBlocker)) ||
    remediation.some((entry, index) => index > 0 &&
      capabilityReadinessRemediations.indexOf(entry as ReadinessRemediation) <=
        capabilityReadinessRemediations.indexOf(remediation[index - 1] as ReadinessRemediation)) ||
    (blockers.length === 0) !== (remediation.length === 0) ||
    value.restart_required !== blockers.includes("startup_gate_closed")) {
    throw new APIRequestError("Capability readiness disposition is invalid",
      "INVALID_RESPONSE", 502);
  }
  for (const blocker of blockers as ReadinessBlocker[]) {
    const expected = readinessRemediationForBlocker[blocker];
    const accepted = Array.isArray(expected) ? expected : [expected];
    if (!accepted.some((entry) => remediation.includes(entry))) {
      throw new APIRequestError("Capability readiness remediation is incomplete",
        "INVALID_RESPONSE", 502);
    }
  }
  for (const action of remediation as ReadinessRemediation[]) {
    const matched = (blockers as ReadinessBlocker[]).some((blocker) => {
      const expected = readinessRemediationForBlocker[blocker];
      const accepted = Array.isArray(expected) ? expected : [expected];
      return accepted.includes(action);
    });
    if (!matched) {
      throw new APIRequestError("Capability readiness remediation is unrelated",
        "INVALID_RESPONSE", 502);
    }
  }
  const runtimeFailureBlockers: ReadinessBlocker[] = [
    "capability_not_implemented", "surface_mismatch", "profile_mismatch",
    "permission_mismatch", "workspace_untrusted", "sandbox_unproven",
    "docker_unavailable", "backend_not_ready", "sbx_unavailable",
  ];
  if (value.runtime_available &&
    blockers.some((entry) => runtimeFailureBlockers.includes(entry as ReadinessBlocker))) {
    throw new APIRequestError("Capability readiness claims a blocked runtime",
      "INVALID_RESPONSE", 502);
  }
  return value;
}

export function parseRunCapabilityReadiness(value: unknown,
  expectedRunID: string): RunCapabilityReadinessView {
  if (!hasExactKeys(value, ["browser_cdp_permissions", "capability_grant",
    "command_runtime", "interactions", "permissions", "presets", "profiles",
    "protocol_version", "run_id", "ui_evidence", "batch_validation"]) ||
    value.protocol_version !== "run_capability_readiness.v1" ||
    value.run_id !== expectedRunID || !boundedIdentity(value.run_id) ||
    value.capability_grant !== false || !validVerificationFeatureReadiness(value.ui_evidence) ||
    !validVerificationFeatureReadiness(value.batch_validation)) {
    throw new APIRequestError("Run capability readiness response is invalid",
      "INVALID_RESPONSE", 502);
  }
  const commandRuntime = value.command_runtime;
  const commandRuntimeRequired = ["adapter_installed", "adapter_ready",
    "current_run_granted", "protocol_available"];
  const commandRuntimeAllowed = [...commandRuntimeRequired, "adapter_kind", "backend"];
  const hasAdapterKind = isRecord(commandRuntime) && Object.prototype.hasOwnProperty.call(commandRuntime, "adapter_kind");
  const hasBackend = isRecord(commandRuntime) && Object.prototype.hasOwnProperty.call(commandRuntime, "backend");
  if (!isRecord(commandRuntime) || !hasOnlyKeys(commandRuntime, commandRuntimeAllowed) ||
    commandRuntimeRequired.some((key) =>
      !Object.prototype.hasOwnProperty.call(commandRuntime, key)) ||
    commandRuntime.protocol_available !== true ||
    typeof commandRuntime.adapter_installed !== "boolean" ||
    typeof commandRuntime.adapter_ready !== "boolean" ||
    typeof commandRuntime.current_run_granted !== "boolean" ||
    (commandRuntime.adapter_ready && !commandRuntime.adapter_installed) ||
    hasAdapterKind !== hasBackend ||
    (hasAdapterKind && (typeof commandRuntime.adapter_kind !== "string" ||
      !["sandboxed_workspace", "host_unsandboxed"].includes(commandRuntime.adapter_kind) ||
      !boundedIdentity(commandRuntime.backend))) ||
    // An installed, selected adapter remains identifiable while Plan or a paused
    // Run has no active command grant. Selection is not execution authority.
    (commandRuntime.current_run_granted && (!commandRuntime.adapter_ready || !hasAdapterKind))) {
    throw new APIRequestError("Command Runtime readiness response is invalid",
      "INVALID_RESPONSE", 502);
  }
  const groups: Array<[unknown, readonly string[], boolean]> = [
    [value.permissions, ["ask", "auto", "full"], true],
    [value.profiles, Array.isArray(value.profiles) && value.profiles.some((option) => isRecord(option) && option.value === "sbx")
      ? ["preview", "docker", "local", "sbx"] : ["preview", "docker", "local"], true],
    [value.interactions, ["preview", "controlled", "debug", "cyber"], true],
    [value.browser_cdp_permissions, ["restricted", "full_debug"], true],
    [value.presets, ["standard_code"], false],
  ];
  for (const [rawOptions, expected, requiresSelection] of groups) {
    if (!Array.isArray(rawOptions) || rawOptions.length !== expected.length) {
      throw new APIRequestError("Run capability readiness option group is incomplete",
        "INVALID_RESPONSE", 502);
    }
    const parsed = rawOptions.map((option, index) =>
      parseCapabilityReadinessOption(option, expected[index]!));
    const selected = parsed.filter((option) => option.selected === true).length;
    if (selected > 1 || (requiresSelection && selected !== 1)) {
      throw new APIRequestError("Run capability readiness selection is invalid",
        "INVALID_RESPONSE", 502);
    }
  }
  return value as unknown as RunCapabilityReadinessView;
}

export const standardCodeNextSteps = [
  "confirm_workspace_trust", "pause_and_configure", "wait_for_quiescence",
  "select_docker", "select_ask", "retry_readiness", "create_new_run",
  "select_sbx",
] as const;

export function parseStandardCodeBackendReadiness(value: unknown,
  backend: "local" | "docker" | "sbx"): void {
  if (!hasExactKeys(value, ["available", "backend", "blocked_by", "remediation"]) ||
    value.backend !== backend || typeof value.available !== "boolean" ||
    !Array.isArray(value.blocked_by) || !Array.isArray(value.remediation) ||
    value.blocked_by.length > capabilityReadinessBlockers.length ||
    value.remediation.length > capabilityReadinessRemediations.length ||
    value.blocked_by.some((entry) => typeof entry !== "string" ||
      !capabilityReadinessBlockers.includes(entry as ReadinessBlocker)) ||
    value.remediation.some((entry) => typeof entry !== "string" ||
      !capabilityReadinessRemediations.includes(entry as ReadinessRemediation)) ||
    new Set(value.blocked_by).size !== value.blocked_by.length ||
    new Set(value.remediation).size !== value.remediation.length ||
    value.available !== (value.blocked_by.length === 0)) {
    throw new APIRequestError("Standard Code backend readiness is invalid",
      "INVALID_RESPONSE", 502);
  }
}

export function parseStandardCodePreset(value: unknown,
  expectedAction: "configure" | "pause_and_configure"): StandardCodePresetControlView {
  const required = ["action", "backend_intent", "blocked_by", "capability_grant",
    "credentials", "docker_readiness", "drydock_ready", "local_readiness", "network",
    "next_steps", "protocol_version", "replayed", "status", "trust_required",
    "workspace_id"];
  const optional = ["browser_cdp_permission", "execution_interaction",
    "execution_permission", "execution_profile", "mode", "run", "run_id",
    "selected_backend", "selection_reason", "trust_digest", "sbx_readiness"];
  if (!isRecord(value) || !hasOnlyKeys(value, [...required, ...optional]) ||
    required.some((key) => !Object.prototype.hasOwnProperty.call(value, key)) ||
    value.protocol_version !== "standard_code_preset.v1" ||
    value.action !== expectedAction ||
    !["auto", "local", "docker", "sbx"].includes(String(value.backend_intent)) ||
    !["blocked", "waiting_for_pause", "configured"].includes(String(value.status)) ||
    !boundedIdentity(value.workspace_id) || value.network !== "disabled" ||
    value.credentials !== "none" || value.capability_grant !== false ||
    typeof value.drydock_ready !== "boolean" || typeof value.replayed !== "boolean" ||
    typeof value.trust_required !== "boolean" || !Array.isArray(value.blocked_by) ||
    !Array.isArray(value.next_steps) ||
    value.blocked_by.length > capabilityReadinessBlockers.length ||
    value.next_steps.length > standardCodeNextSteps.length ||
    value.blocked_by.some((entry) => typeof entry !== "string" ||
      !capabilityReadinessBlockers.includes(entry as ReadinessBlocker)) ||
    value.next_steps.some((entry) => typeof entry !== "string" ||
      !standardCodeNextSteps.includes(entry as typeof standardCodeNextSteps[number])) ||
    new Set(value.blocked_by).size !== value.blocked_by.length ||
    new Set(value.next_steps).size !== value.next_steps.length) {
    throw new APIRequestError("Standard Code preset response is invalid",
      "INVALID_RESPONSE", 502);
  }
  parseStandardCodeBackendReadiness(value.local_readiness, "local");
  parseStandardCodeBackendReadiness(value.docker_readiness, "docker");
  if (value.sbx_readiness !== undefined) parseStandardCodeBackendReadiness(value.sbx_readiness, "sbx");
  if (value.backend_intent === "sbx" && value.sbx_readiness === undefined) {
    throw new APIRequestError("Standard Code sbx readiness is missing", "INVALID_RESPONSE", 502);
  }

  const selected = value.selected_backend;
  const reason = value.selection_reason;
  const selectionValid = (selected === undefined && reason === undefined) ||
    (selected === "local" &&
      ((value.backend_intent === "auto" && reason === "auto_local_ready") ||
        (value.backend_intent === "local" && reason === "explicit_local"))) ||
    (selected === "docker" && value.backend_intent === "docker" &&
      reason === "explicit_docker") ||
    (selected === "sbx" && value.backend_intent === "sbx" && reason === "explicit_sbx");
  const trustDigestPresent = Object.prototype.hasOwnProperty.call(value, "trust_digest");
  if (!selectionValid ||
    (Object.prototype.hasOwnProperty.call(value, "run_id") && !boundedIdentity(value.run_id)) ||
    trustDigestPresent !== value.trust_required ||
    (trustDigestPresent && !isSHA256(value.trust_digest)) ||
    (value.trust_required && (value.status !== "blocked" ||
      !value.next_steps.includes("confirm_workspace_trust"))) ||
    (value.status === "waiting_for_pause" &&
      (!boundedIdentity(value.run_id) || selected === undefined)) ||
    (value.status === "configured" &&
      (!boundedIdentity(value.run_id) || selected === undefined || !value.drydock_ready ||
        value.blocked_by.length !== 0 || value.next_steps.length !== 0 ||
        !isRecord(value.run) || !isRecord(value.mode) || !isRecord(value.execution_profile) ||
        !isRecord(value.execution_interaction) || !isRecord(value.execution_permission) ||
        !isRecord(value.browser_cdp_permission)))) {
    throw new APIRequestError("Standard Code preset response is inconsistent",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as StandardCodePresetControlView;
}

export const safeWebReadinessBlockingReasons = [
  "evidence_missing", "evidence_version_mismatch", "executable_identity_mismatch",
  "acceptance_mismatch", "policy_version_mismatch", "adapter_mismatch",
  "platform_mismatch", "evidence_not_passed", "review_missing",
  "review_binding_mismatch", "review_not_accepted", "evidence_expired",
];

export function parseSafeWebReadiness(value: unknown): BrowserSafeWebReadiness {
  if (!isRecord(value)) {
    throw new APIRequestError("Safe Web readiness response is invalid", "INVALID_RESPONSE", 502);
  }
  if (value.protocol_version !== "browser_safe_web_readiness.v1" ||
    typeof value.ready !== "boolean" ||
    !isSHA256(value.executable_identity_fingerprint) ||
    !isSHA256(value.acceptance_fingerprint) || !isSHA256(value.fingerprint)) {
    throw new APIRequestError("Safe Web readiness response is invalid", "INVALID_RESPONSE", 502);
  }
  const blockingReason = typeof value.blocking_reason === "string" &&
    value.blocking_reason !== "" ? value.blocking_reason : null;
  if (value.ready === (blockingReason !== null)) {
    throw new APIRequestError("Safe Web readiness blocking reason disagrees with ready",
      "INVALID_RESPONSE", 502);
  }
  if (blockingReason !== null && !safeWebReadinessBlockingReasons.includes(blockingReason)) {
    throw new APIRequestError("Safe Web readiness blocking reason is unrecognized",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as BrowserSafeWebReadiness;
}

export function parseEmbeddedAnalyzerExecution(value: unknown,
  runID: string): EmbeddedAnalyzerExecutionControlView {
  if (!hasExactKeys(value, [
    "analyzer", "artifact_atomic", "artifact_id", "bearer_token_included",
    "capability_consumed", "execution_id", "filesystem_mounted",
    "host_process_authorized", "input_bytes", "line_count", "media_type",
    "metadata_only", "network_enabled", "raw_request_included", "replayed",
    "run_id", "session_id", "sha256", "status", "subprocess_enabled", "utf8",
    "version", "workspace_id",
  ]) || value.version !== "embedded_analyzer_execution_control.v1" ||
    value.run_id !== runID || boundedIdentity(value.execution_id) !== value.execution_id ||
    boundedIdentity(value.artifact_id) !== value.artifact_id ||
    boundedIdentity(value.session_id) !== value.session_id ||
    boundedIdentity(value.workspace_id) !== value.workspace_id ||
    value.analyzer !== "fixture.digest.v1" || value.status !== "succeeded" ||
    value.media_type !== "text/plain" ||
    !safeBoundedCount(value.input_bytes, 65_536) || value.input_bytes < 1 ||
    !safeBoundedCount(value.line_count, 65_536) || !isSHA256(value.sha256) ||
    value.utf8 !== true || value.metadata_only !== true ||
    value.capability_consumed !== true || value.artifact_atomic !== true ||
    value.filesystem_mounted !== false || value.network_enabled !== false ||
    value.subprocess_enabled !== false || value.host_process_authorized !== false ||
    value.raw_request_included !== false || value.bearer_token_included !== false ||
    typeof value.replayed !== "boolean") {
    throw new APIRequestError("Embedded analyzer response violated its fixed boundary",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as EmbeddedAnalyzerExecutionControlView;
}
