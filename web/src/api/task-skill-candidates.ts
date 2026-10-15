import type { TaskSkillCandidatesView } from "./types";
import { validSkillSelectionRef } from "./project-skills";

export type TaskSkillCandidateScope = Pick<TaskSkillCandidatesView, "workspace_id" | "surface" | "phase" | "profile">;

export function parseTaskSkillCandidates(value: unknown, scope: TaskSkillCandidateScope): TaskSkillCandidatesView {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("Task Skill candidates response is invalid");
  const view = value as Record<string, unknown>;
  const allowed = ["version", "workspace_id", "surface", "phase", "profile", "items", "unavailable_count", "capability_grant"];
  const items = view.items;
  const seen = new Set<string>();
  if (Object.keys(view).some((key) => !allowed.includes(key)) || view.version !== "task_skill_candidates.v1" ||
    view.workspace_id !== scope.workspace_id || view.surface !== scope.surface || view.phase !== scope.phase || view.profile !== scope.profile ||
    view.capability_grant !== false || !Number.isSafeInteger(view.unavailable_count) || Number(view.unavailable_count) < 0 ||
    !Array.isArray(items) || items.length > 1024 ||
    items.some((item) => {
      if (!item || typeof item !== "object" || Array.isArray(item)) return true;
      const row = item as Record<string, unknown>;
      if (Object.keys(row).some((key) => !["ref", "name", "version", "token_upper_bound"].includes(key)) ||
        !validSkillSelectionRef(row.ref) || seen.has(row.ref) || typeof row.name !== "string" || !row.name || row.name.length > 256 ||
        (row.version !== undefined && (typeof row.version !== "string" || row.version.length > 256 * 1024)) ||
        !Number.isSafeInteger(row.token_upper_bound) || Number(row.token_upper_bound) <= 0 || Number(row.token_upper_bound) > 4096) return true;
      seen.add(row.ref);
      return false;
    })) {
    throw new Error("Task Skill candidates response format or identity does not match");
  }
  return value as TaskSkillCandidatesView;
}
