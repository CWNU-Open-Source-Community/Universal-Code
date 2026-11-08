import type { HookDiagnosticsView, PluginHistoryView } from "../api/types";
import { plugin } from "./extension-onboarding-fixtures";

export function pluginHistory(): PluginHistoryView {
  const current = { ...plugin("enabled"), id: "plugin-current", generation: 4, signature_present: true,
    signature_valid: true, publisher_fingerprint: "f".repeat(64), manifest: { ...plugin().manifest, version: "2.0.0" } };
  const target = { ...plugin("rolled_back"), id: "plugin-old", generation: 5, package_fingerprint: "d".repeat(64), signature_present: true,
    signature_valid: true, publisher_fingerprint: "f".repeat(64) };
  return { protocol_version: "plugin-lifecycle.v1", installation_id: current.id, package_id: current.manifest.id,
    installations: [current, target], total_versions: 2, total_publisher_installations: 1, publisher_installation_ids: [current.id],
    publisher: { fingerprint: "f".repeat(64), publisher: "operator", state: "trusted", generation: 3, reviewed_at: "2026-10-10T01:00:00Z" } };
}

export function hookDiagnostics(): HookDiagnosticsView {
  return { protocol_version: "hook-diagnostics.v1", run_id: "run-one", workspace_id: "project-one", omitted_declarations: 0,
    declarations: [{ installation_id: "plugin-current", plugin_id: "first-plugin", package_fingerprint: "c".repeat(64), installation_state: "enabled",
      active: true, scope: "local", hook_id: "guard", event: "pre_tool", action: "deny", failure_policy: "deny", timeout_ms: 100, tool_names: ["file_write"], tool_pattern: "", remove_fields: [] }],
    observations: [{ id: "observed-one", plugin_id: "first-plugin", hook_id: "guard", event: "pre_tool", action: "deny", package_fingerprint: "d".repeat(64),
      run_id: "run-one", workspace_id: "project-one", tool_name: "file_write", outcome: "completed", decision: "rejected", created_at: "2026-10-10T01:00:00Z" },
      { id: "historical-one", plugin_id: "first-plugin", hook_id: "guard", event: "pre_tool", run_id: "run-one", workspace_id: "project-one",
        tool_name: "file_write", outcome: "completed", decision: "unknown", created_at: "2026-10-09T01:00:00Z" }] };
}
