import type {
  CapabilityReadinessOptionView,
  RunCapabilityReadinessView,
} from "../api/types";

function option(value: CapabilityReadinessOptionView["value"], selected: boolean,
  selectable = true, runtimeAvailable = true,
  disposition: Pick<CapabilityReadinessOptionView,
    "blocked_by" | "remediation" | "restart_required"> = {
      blocked_by: [], remediation: [], restart_required: false,
    }): CapabilityReadinessOptionView {
  return {
    value, selected, selectable, runtime_available: runtimeAvailable,
    ...disposition,
  };
}

export function capabilityReadinessFixture(
  runID = "run-1",
): RunCapabilityReadinessView {
  return {
    protocol_version: "run_capability_readiness.v1",
    run_id: runID,
    capability_grant: false,
    ui_evidence: { installed: false, blocked_by: ["startup_gate_closed", "native_control_unavailable"], remediation: ["open_desktop_settings"] },
    batch_validation: { installed: false, blocked_by: ["startup_gate_closed", "native_control_unavailable"], remediation: ["open_desktop_settings"] },
    permissions: [
      option("ask", true),
      option("auto", false),
      option("full", false, false, false, {
        blocked_by: ["startup_gate_closed"],
        remediation: ["restart_with_startup_gate"], restart_required: true,
      }),
    ],
    profiles: [
      option("preview", true),
      option("docker", false, true, false, {
        blocked_by: ["docker_unavailable"],
        remediation: ["install_or_start_docker"], restart_required: false,
      }),
      option("local", false, true, false, {
        blocked_by: ["sandbox_unproven"],
        remediation: ["verify_sandbox"], restart_required: false,
      }),
    ],
    interactions: [
      option("preview", true),
      option("controlled", false, true, false, {
        blocked_by: ["workspace_untrusted"],
        remediation: ["trust_workspace"], restart_required: false,
      }),
      option("debug", false, true, false, {
        blocked_by: ["permission_mismatch", "workspace_untrusted"],
        remediation: ["select_required_permission", "trust_workspace"],
        restart_required: false,
      }),
      option("cyber", false, false, false, {
        blocked_by: ["surface_mismatch", "profile_mismatch", "workspace_untrusted",
          "docker_unavailable"],
        remediation: ["select_required_surface", "select_required_profile",
          "trust_workspace", "install_or_start_docker"],
        restart_required: false,
      }),
    ],
    browser_cdp_permissions: [
      option("restricted", true),
      option("full_debug", false, false, false, {
        blocked_by: ["permission_mismatch"],
        remediation: ["select_required_permission"], restart_required: false,
      }),
    ],
    command_runtime: {
      protocol_available: true,
      adapter_installed: false,
      adapter_ready: false,
      current_run_granted: false,
    },
    presets: [option("standard_code", false, false, false, {
      blocked_by: ["capability_not_implemented", "workspace_untrusted",
        "sandbox_unproven"],
      remediation: ["upgrade_application", "trust_workspace", "verify_sandbox"],
      restart_required: false,
    })],
  };
}

type ReadinessGroup = "permissions" | "profiles" | "interactions" |
  "browser_cdp_permissions" | "presets";

export function patchCapabilityReadiness(
  readiness: RunCapabilityReadinessView,
  group: ReadinessGroup,
  value: CapabilityReadinessOptionView["value"],
  patch: Partial<CapabilityReadinessOptionView>,
): RunCapabilityReadinessView {
  return {
    ...readiness,
    [group]: readiness[group].map((candidate) => candidate.value === value
      ? { ...candidate, ...patch }
      : candidate),
  };
}
