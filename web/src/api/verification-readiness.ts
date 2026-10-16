export interface VerificationFeatureReadiness {
  installed: boolean;
  blocked_by: Array<"startup_gate_closed" | "native_control_unavailable">;
  remediation: Array<"enable_and_restart" | "restart_application" | "open_desktop_settings">;
}

export function validVerificationFeatureReadiness(value: unknown): value is VerificationFeatureReadiness {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const item = value as Record<string, unknown>;
  if (Object.keys(item).length !== 3 || typeof item.installed !== "boolean" ||
    !Array.isArray(item.blocked_by) || !Array.isArray(item.remediation)) return false;
  const { blocked_by: blockers, remediation } = item;
  if (item.installed) return blockers.length === 0 &&
    (remediation.length === 0 || (remediation.length === 1 && remediation[0] === "restart_application"));
  if (blockers[0] !== "startup_gate_closed" || remediation.length !== 1) return false;
  if (blockers.length === 2 && blockers[1] === "native_control_unavailable") return remediation[0] === "open_desktop_settings";
  return blockers.length === 1 && ["enable_and_restart", "restart_application"].includes(remediation[0]);
}
