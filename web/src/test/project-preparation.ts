import type { ProjectPreparationOperation, ProjectPreparationView } from "../api/project-preparation";

export function preparationOperation(overrides: Partial<ProjectPreparationOperation> = {}): ProjectPreparationOperation {
  return { protocol_version: "project-preparation.v1", run_id: "run-1", target: ".", id: "prep-1",
    state: "ready", completed_packages: 2, total_packages: 2, downloaded_bytes: 2048, cache_hits: 0,
    error: "", can_cancel: false, created_at: "2026-10-14T00:00:00Z", updated_at: "2026-10-14T00:00:01Z",
    job: { id: "job-1", state: "completed", exit_code: 0, stdout: "installed 2 packages", stderr: "",
      stdout_truncated: false, stderr_truncated: false, can_stop: false }, ...overrides };
}
export function preparationView(overrides: Partial<ProjectPreparationView> = {}): ProjectPreparationView {
  return { protocol_version: "project-preparation.v1", run_id: "run-1", backend: "local_windows_lpac", target: ".",
    available: true, reason: "", plan: { fingerprint: "a".repeat(64), ecosystem: "npm", toolchain: "Node.js 24 / npm",
      requested_version: "24", files: [{ path: "package.json", sha256: "b".repeat(64) },
        { path: "package-lock.json", sha256: "c".repeat(64) }], sources: ["https://registry.npmjs.org"],
      package_count: 2, install_command: "npm ci --offline --ignore-scripts=true", test_command: "npm test",
      ready: true, issues: [] }, cache: { status: "empty", cached_packages: 0, total_packages: 2 }, ...overrides };
}
