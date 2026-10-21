import {
  parseSandboxEnvironment,
  sandboxSettingsMatch,
  validSandboxEnvironmentRequest,
} from "../../sandbox-environment";
import { APIRequestError } from "../protocol";
import { parseFullCDPSessionControl } from "../responses/browser";
import { parseDiagnosticBundle } from "../responses/diagnostics";
import {
  parseEmbeddedAnalyzerExecution,
  parseRunCapabilityReadiness,
  parseRuntimeCapabilities,
  parseSafeWebReadiness,
  parseStandardCodePreset,
} from "../responses/runtime";
import {
  boundedIdentity,
  hasRequiredOnlyKeys,
  isRecord,
  isSHA256,
  safePositiveInteger,
  validDate,
} from "../validation";
import type {
  BrowserSafeWebReadiness,
  DiagnosticBundleView,
  DockerEnvironmentView,
  DockerSandboxAdmissionRequestView,
  DockerSandboxAdmissionView,
  DockerSandboxCancelRequestView,
  DockerSandboxCancellationView,
  DockerSandboxStartRequestView,
  DockerSandboxStatusView,
  EmbeddedAnalyzerExecutionControlView,
  EmbeddedAnalyzerExecutionRequestView,
  FullCDPSessionCloseRequestView,
  FullCDPSessionControlView,
  FullCDPSessionOpenRequestView,
  HealthView,
  RunCapabilityReadinessView,
  RuntimeCapabilitiesView,
  SandboxEnvironmentControlRequestView,
  SandboxEnvironmentView,
  StandardCodePresetControlRequestView,
  StandardCodePresetControlView,
} from "../../types";
import type { ClientFeatures } from "../capabilities";
import type { APITransport } from "../transport";

export class RuntimeClient {
  constructor(
    private readonly transport: APITransport,
    private readonly features: ClientFeatures,
  ) {}

  async health(signal?: AbortSignal): Promise<HealthView> {
    return this.transport.get<HealthView>("/health", {}, signal);
  }

  async runtimeCapabilities(signal?: AbortSignal): Promise<RuntimeCapabilitiesView> {
    return parseRuntimeCapabilities(await this.transport.get<unknown>("/capabilities", {}, signal));
  }

  async runCapabilityReadiness(runID: string,
    signal?: AbortSignal): Promise<RunCapabilityReadinessView> {
    if (!boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("A normalized Run identity is required");
    }
    return parseRunCapabilityReadiness(await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/capability-readiness`, {}, signal), runID);
  }

  async getFullCDPSession(runID: string,
    signal?: AbortSignal): Promise<FullCDPSessionControlView> {
    if (!this.features.hasFullCDPSessionControl || !boundedIdentity(runID) || runID.trim() !== runID) {
      throw new Error("Full CDP production session capability and a normalized Run are required");
    }
    return parseFullCDPSessionControl(await this.transport.get<unknown>(
      `/runs/${encodeURIComponent(runID)}/full-cdp-session`, {}, signal), runID);
  }

  async openFullCDPSession(runID: string, body: FullCDPSessionOpenRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<FullCDPSessionControlView> {
    if (!this.features.hasFullCDPSessionControl || !boundedIdentity(runID) || runID.trim() !== runID ||
      body.version !== "full_cdp_session.v1" || body.confirm_full_cdp !== true ||
      !safePositiveInteger(body.expected_execution_permission_revision) ||
      !safePositiveInteger(body.expected_browser_cdp_permission_revision) ||
      !["chrome", "edge"].includes(body.browser.product) ||
      !["stable", "beta", "dev", "canary"].includes(body.browser.channel) ||
      typeof body.target !== "string" || body.target.trim() !== body.target ||
      body.target.length === 0 || body.target.length > 2_048) {
      throw new Error("A confirmed, revision-bound Full CDP session request is required");
    }
    return parseFullCDPSessionControl(await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/full-cdp-session`, body, idempotencyKey, signal), runID);
  }

  async closeFullCDPSession(runID: string, body: FullCDPSessionCloseRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<FullCDPSessionControlView> {
    if (!this.features.hasFullCDPSessionControl || !boundedIdentity(runID) || runID.trim() !== runID ||
      body.version !== "full_cdp_session_close.v1" ||
      !boundedIdentity(body.expected_session_id)) {
      throw new Error("An exact Full CDP session cleanup request is required");
    }
    return parseFullCDPSessionControl(await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}/full-cdp-session/close`, body,
      idempotencyKey, signal), runID);
  }

  async configureStandardCode(runID: string,
    action: "configure" | "pause_and_configure",
    body: StandardCodePresetControlRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<StandardCodePresetControlView> {
    if (!this.features.hasStandardCodePreset) {
      throw new Error("Standard Code preset capability is required for this operation");
    }
    if (!boundedIdentity(runID) || runID.trim() !== runID ||
      body.version !== "standard_code_preset.v1" ||
      !["auto", "local", "docker", "sbx"].includes(body.backend_intent) ||
      body.workspace_id !== undefined || body.goal !== undefined ||
      body.confirm_workspace_trust !== (body.expected_trust_digest !== undefined) ||
      (body.expected_trust_digest !== undefined && !isSHA256(body.expected_trust_digest))) {
      throw new Error("A valid existing-Run Standard Code preset request is required");
    }
    const suffix = action === "pause_and_configure"
      ? "/standard-code/pause-and-configure" : "/standard-code/preset";
    const result = await this.transport.sendControl<unknown>(
      `/runs/${encodeURIComponent(runID)}${suffix}`, body, idempotencyKey, signal);
    const parsed = parseStandardCodePreset(result, action);
    if (parsed.backend_intent !== body.backend_intent) {
      throw new APIRequestError("Standard Code response changed the requested backend", "INVALID_RESPONSE", 502);
    }
    return parsed;
  }

  async createStandardCode(body: StandardCodePresetControlRequestView,
    idempotencyKey: string, signal?: AbortSignal): Promise<StandardCodePresetControlView> {
    if (!this.features.hasStandardCodePreset) {
      throw new Error("Standard Code preset capability is required for this operation");
    }
    const workspaceID = boundedIdentity(body.workspace_id);
    const goal = body.goal;
    if (body.version !== "standard_code_preset.v1" ||
      !["auto", "local", "docker", "sbx"].includes(body.backend_intent) ||
      workspaceID !== body.workspace_id || typeof goal !== "string" ||
      goal.trim() !== goal || goal.length === 0 || goal.includes("\0") ||
      new TextEncoder().encode(goal).byteLength > 4096 ||
      body.confirm_workspace_trust !== (body.expected_trust_digest !== undefined) ||
      (body.expected_trust_digest !== undefined && !isSHA256(body.expected_trust_digest))) {
      throw new Error("A normalized Workspace, goal, and trust intent are required for Standard Code");
    }
    const result = parseStandardCodePreset(await this.transport.sendControl<unknown>(
      "/standard-code/preset", body, idempotencyKey, signal), "configure");
    if (result.workspace_id !== workspaceID ||
      result.backend_intent !== body.backend_intent) {
      throw new APIRequestError("Standard Code preset response changed the requested target",
        "INVALID_RESPONSE", 502);
    }
    return result;
  }

  async safeWebReadiness(product: string, signal?: AbortSignal): Promise<BrowserSafeWebReadiness> {
    return parseSafeWebReadiness(await this.transport.get<unknown>("/browser/safe-web-readiness",
      { product }, signal));
  }

  async diagnosticBundle(runID: string, signal?: AbortSignal): Promise<DiagnosticBundleView> {
    if (boundedIdentity(runID) !== runID) {
      throw new Error("A normalized Run identity is required for diagnostic export");
    }
    return parseDiagnosticBundle(await this.transport.get<unknown>("/diagnostic-bundle", {
      run_id: runID, limit: 100,
    }, signal), runID);
  }

  async executeEmbeddedAnalyzer(runID: string,
    body: EmbeddedAnalyzerExecutionRequestView, signal?: AbortSignal,
  ): Promise<EmbeddedAnalyzerExecutionControlView> {
    if (!this.features.hasEmbeddedAnalyzerExecution) {
      throw new Error("Embedded analyzer execution capability is required");
    }
    const normalizedRunID = boundedIdentity(runID);
    if (!normalizedRunID || normalizedRunID !== runID ||
      body.version !== "embedded_analyzer_operator_request.v1" ||
      body.confirmation !== "RUN-EMBEDDED-ANALYZER" ||
      ((body.text ?? "") === "") === ((body.file ?? "").trim() === "")) {
      throw new Error("A normalized Run and one explicitly confirmed analyzer input are required");
    }
    const result = await this.transport.sendControlRequest<unknown>(
      `/runs/${encodeURIComponent(runID)}/analyzer-executions`, body, signal,
    );
    return parseEmbeddedAnalyzerExecution(result, runID);
  }

  async getSandboxEnvironment(signal?: AbortSignal): Promise<SandboxEnvironmentView> {
    const value = await this.transport.get<unknown>("/sandbox/environment", {}, signal);
    try { return parseSandboxEnvironment(value); }
    catch { throw new APIRequestError("Sandbox environment response is invalid", "INVALID_RESPONSE", 502); }
  }

  async saveSandboxEnvironment(body: SandboxEnvironmentControlRequestView, signal?: AbortSignal): Promise<SandboxEnvironmentView> {
    if (!this.features.hasControl || !validSandboxEnvironmentRequest(body)) throw new Error("Complete sandbox settings and their current revision are required");
    const value = await this.transport.sendControlRequest<unknown>("/sandbox/environment", body, signal, "", "PUT");
    let result: SandboxEnvironmentView;
    try { result = parseSandboxEnvironment(value); }
    catch { throw new APIRequestError("Sandbox environment response is invalid", "INVALID_RESPONSE", 502); }
    if (!sandboxSettingsMatch(result.settings, body.settings) || result.revision !== body.expected_revision + 1) {
      throw new APIRequestError("Sandbox settings receipt changed the saved request", "INVALID_RESPONSE", 502);
    }
    return result;
  }

  async getDockerEnvironment(signal?: AbortSignal): Promise<DockerEnvironmentView> {
    const value = await this.transport.get<unknown>("/sandbox/docker/environment", {}, signal);
    if (!hasRequiredOnlyKeys(value, ["protocol_version", "feature_enabled", "image_configured", "restart_required"],
      ["image_digest", "readiness"]) || value.protocol_version !== "docker_environment.v1" ||
      typeof value.feature_enabled !== "boolean" || typeof value.image_configured !== "boolean" ||
      typeof value.restart_required !== "boolean" ||
      (value.image_configured ? typeof value.image_digest !== "string" || !/^sha256:[a-f0-9]{64}$/u.test(value.image_digest) : value.image_digest !== undefined) ||
      (value.readiness !== undefined && (!hasRequiredOnlyKeys(value.readiness,
        ["protocol_version", "status", "ready", "feature_enabled", "reason_code", "remediation_code", "checked_at", "expires_at", "endpoint_class", "endpoint_fingerprint", "daemon_reachable", "image_inspected", "image_profile_safe", "readiness_fingerprint"], ["network_mode"]) ||
        value.readiness.protocol_version !== "sandbox.readiness.v1" ||
        !["ready", "disabled", "unavailable"].includes(String(value.readiness.status)) ||
        value.readiness.ready !== (value.readiness.status === "ready") ||
        value.readiness.feature_enabled !== value.feature_enabled ||
        !validDate(value.readiness.checked_at) || !validDate(value.readiness.expires_at) ||
        Date.parse(String(value.readiness.expires_at)) - Date.parse(String(value.readiness.checked_at)) !== 30_000 ||
        !["local_unix", "local_npipe"].includes(String(value.readiness.endpoint_class)) ||
        !isSHA256(value.readiness.endpoint_fingerprint) || !isSHA256(value.readiness.readiness_fingerprint) ||
        (value.readiness.network_mode !== undefined && value.readiness.network_mode !== "disabled") ||
        (value.readiness.ready && (!value.image_configured || value.readiness.daemon_reachable !== true || value.readiness.image_inspected !== true || value.readiness.image_profile_safe !== true || value.readiness.network_mode !== "disabled")) ||
        ["daemon_reachable", "image_inspected", "image_profile_safe"].some((key) => typeof (value.readiness as Record<string, unknown>)[key] !== "boolean") ||
        typeof value.readiness.reason_code !== "string" || typeof value.readiness.remediation_code !== "string"))) {
      throw new APIRequestError("Docker environment response is invalid", "INVALID_RESPONSE", 502);
    }
    return value as unknown as DockerEnvironmentView;
  }

  async getDockerSandboxStatus(admissionID: string,
    signal?: AbortSignal): Promise<DockerSandboxStatusView> {
    const result = await this.transport.get<unknown>("/sandbox/docker/status", { admission_id: admissionID }, signal);
    if (!isRecord(result) || !boundedIdentity(String(result.admission_id))) {
      throw new APIRequestError("Docker sandbox status is invalid", "INVALID_RESPONSE", 502);
    }
    return result as unknown as DockerSandboxStatusView;
  }

  async admitDockerSandbox(body: DockerSandboxAdmissionRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<DockerSandboxAdmissionView> {
    if (!this.features.hasControl) {
      throw new Error("A control bearer token is required for this operation");
    }
    if (!boundedIdentity(body.plan_id) || !boundedIdentity(body.requested_by) ||
      !isRecord(body.manifest)) {
      throw new Error("A plan identity, requester, and manifest object are required");
    }
    const result = await this.transport.sendControl<unknown>("/sandbox/docker/admissions", body, idempotencyKey, signal);
    if (!isRecord(result) || typeof result.allowed !== "boolean") {
      throw new APIRequestError("Docker sandbox admission is invalid", "INVALID_RESPONSE", 502);
    }
    return result as unknown as DockerSandboxAdmissionView;
  }

  async startDockerSandbox(body: DockerSandboxStartRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<DockerSandboxStatusView> {
    if (!this.features.hasControl) {
      throw new Error("A control bearer token is required for this operation");
    }
    if (!boundedIdentity(body.admission_id) || !boundedIdentity(body.requested_by)) {
      throw new Error("An admission identity and requester are required");
    }
    const result = await this.transport.sendControl<unknown>("/sandbox/docker/starts", body, idempotencyKey, signal);
    return result as unknown as DockerSandboxStatusView;
  }

  async cancelDockerSandbox(body: DockerSandboxCancelRequestView, idempotencyKey: string,
    signal?: AbortSignal): Promise<DockerSandboxCancellationView> {
    if (!this.features.hasControl) {
      throw new Error("A control bearer token is required for this operation");
    }
    if (!boundedIdentity(body.admission_id) || !boundedIdentity(body.requested_by)) {
      throw new Error("An admission identity and requester are required");
    }
    const result = await this.transport.sendControl<unknown>("/sandbox/docker/cancellations", body, idempotencyKey, signal);
    return result as unknown as DockerSandboxCancellationView;
  }
}
