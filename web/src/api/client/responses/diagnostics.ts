import { APIRequestError } from "../protocol";
import { hasExactKeys, hasOnlyKeys, isRecord, validDate } from "../validation";
import type { DiagnosticBundleView } from "../../types";

export function validDiagnosticRedaction(value: unknown): boolean {
  return hasExactKeys(value, ["command_input", "event_payloads", "prompts", "secrets",
    "terminal_input"]) && value.command_input === "withheld" &&
    value.event_payloads === "withheld" && value.prompts === "withheld" &&
    value.terminal_input === "withheld" && value.secrets === "redacted";
}

export function parseDiagnosticBundle(value: unknown, runID: string): DiagnosticBundleView {
  if (!hasExactKeys(value, ["debug", "doctor", "generated_at", "protocol_version"]) ||
    value.protocol_version !== "diagnostic-bundle.v1" || !validDate(value.generated_at) ||
    !isRecord(value.debug) || value.debug.protocol_version !== "debug-query.v1" ||
    value.debug.run_id !== runID || !Array.isArray(value.debug.items) ||
    value.debug.items.length > 100 || !validDiagnosticRedaction(value.debug.redaction) ||
    value.debug.items.some((item) => !hasExactKeys(item, ["category", "evidence",
      "observed_at", "occurred_at", "payload_state", "sequence", "source", "subject_id",
      "timestamp_adjusted", "type"]) || item.payload_state !== "withheld" ||
      item.evidence !== "persisted_event") || !isRecord(value.doctor) ||
    value.doctor.protocol_version !== "doctor-snapshot.v1" ||
    !hasOnlyKeys(value.doctor, ["build", "checks", "generated_at", "models",
      "protocol_version", "ready", "redaction", "run", "schema_version"]) ||
    ["build", "models", "schema_version"].some((key) =>
      !Object.prototype.hasOwnProperty.call(value.doctor, key)) ||
    !validDate(value.doctor.generated_at) || typeof value.doctor.ready !== "boolean" ||
    !Array.isArray(value.doctor.checks) ||
    value.doctor.checks.some((check) => !hasExactKeys(check,
      ["component", "detail_code", "evidence", "status"]) ||
      !["ready", "degraded", "not_configured", "not_probed"].includes(String(check.status))) ||
    !validDiagnosticRedaction(value.doctor.redaction) ||
    (value.doctor.run !== undefined && (!isRecord(value.doctor.run) ||
      !hasOnlyKeys(value.doctor.run, ["allowed_network_target_count", "execution_permission",
        "model_route", "network_mode", "phase", "process_capability_granted", "profile",
        "read_only_tools_eligible", "repair_tools_eligible", "root_agent_id", "run_id",
        "status", "surface", "workspace_id"]) || value.doctor.run.run_id !== runID ||
      value.doctor.run.process_capability_granted !== false))) {
    throw new APIRequestError("Diagnostic bundle violated its redaction contract",
      "INVALID_RESPONSE", 502);
  }
  return value as unknown as DiagnosticBundleView;
}
