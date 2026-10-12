import { useCallback, useMemo } from "react";
import type { TaskBudgetSettings } from "../api/types";
import { normalizedTaskBudget } from "../api/task-configuration";
import { useV2PersistentState } from "./recovery-storage";
import { validProjectSkillChoice, type ProjectSkillChoice } from "../api/project-skills";

const fields = ["max_turns", "max_tokens", "max_tool_calls", "max_cost_usd", "timeout_seconds"] as const;
const record = (value: unknown): value is Record<string, unknown> => Boolean(value) && typeof value === "object" && !Array.isArray(value);
type DraftTaskConfiguration = { budget?: TaskBudgetSettings; valid: boolean; projectSkills?: ProjectSkillChoice };
const invalid = (): DraftTaskConfiguration => ({ budget: { max_turns: Number.NaN }, valid: false });
const numericValidity = (budget?: TaskBudgetSettings) => {
  try { normalizedTaskBudget(budget); return true; } catch { return false; }
};

function readConfiguration(value: unknown): DraftTaskConfiguration {
  if (value === undefined) return { valid: true };
  if (!record(value) || Object.keys(value).some((key) => !["budget", "valid", "projectSkills"].includes(key)) || typeof value.valid !== "boolean" ||
    (value.projectSkills !== undefined && !validProjectSkillChoice(value.projectSkills))) return invalid();
  const projectSkills = value.projectSkills as ProjectSkillChoice | undefined;
  if (value.budget === undefined) return { valid: value.valid, projectSkills };
  if (!record(value.budget) || Object.keys(value.budget).some((key) => !fields.includes(key as typeof fields[number]))) return invalid();
  const budget: TaskBudgetSettings = {};
  for (const key of fields) {
    if (!Object.hasOwn(value.budget, key)) continue;
    const number = value.budget[key];
    budget[key] = typeof number === "number" ? number : Number.NaN;
  }
  return { budget, valid: value.valid && numericValidity(budget), projectSkills };
}

// Keep draft settings in the same client/data-store scope as other recovery
// data. Invalid number input needs a JSON-safe marker so reopening never turns
// an incomplete value into an omitted field with the default budget.
export function useV2DraftTaskConfiguration(workspaceID: string) {
  const [saved, setSaved] = useV2PersistentState<unknown>("new-thread-task-configuration", {});
  const configuration = useMemo(() => record(saved)
    ? readConfiguration(Object.hasOwn(saved, workspaceID) ? saved[workspaceID] : undefined)
    : invalid(), [saved, workspaceID]);
  const onBudgetChange = useCallback((budget: TaskBudgetSettings | undefined) => {
    const encoded = budget ? Object.fromEntries(Object.entries(budget).map(([key, value]) =>
      [key, typeof value === "number" && Number.isFinite(value) ? value : "invalid"])) : undefined;
    setSaved((previous: unknown) => {
      const entries = record(previous) ? previous : {};
      const current = record(previous)
        ? readConfiguration(Object.hasOwn(entries, workspaceID) ? entries[workspaceID] : undefined) : invalid();
      // Editing a valid number or resetting defaults cannot clear an already
      // observed rejection. Only a completed preview may confirm availability.
      return { ...entries, [workspaceID]: { ...(encoded ? { budget: encoded } : {}),
        ...(current.projectSkills ? { projectSkills: current.projectSkills } : {}),
        valid: current.valid && numericValidity(budget) } };
    });
  }, [setSaved, workspaceID]);
  const onValidityChange = useCallback((valid: boolean) => {
    setSaved((previous: unknown) => {
      const entries = record(previous) ? previous : {};
      const current = readConfiguration(Object.hasOwn(entries, workspaceID) ? entries[workspaceID] : undefined);
      const nextValid = valid && numericValidity(current.budget);
      if (current.valid === nextValid) return previous;
      return { ...entries, [workspaceID]: { ...(record(entries[workspaceID]) ? entries[workspaceID] : {}), valid: nextValid } };
    });
  }, [setSaved, workspaceID]);
  const onProjectSkillsChange = useCallback((projectSkills: ProjectSkillChoice) => {
    if (!validProjectSkillChoice(projectSkills)) return;
    setSaved((previous: unknown) => {
      const entries = record(previous) ? previous : {};
      const current = record(previous) ? readConfiguration(Object.hasOwn(entries, workspaceID) ? entries[workspaceID] : undefined) : invalid();
      return { ...entries, [workspaceID]: { ...(record(entries[workspaceID]) ? entries[workspaceID] : {}),
        valid: current.valid, projectSkills } };
    });
  }, [setSaved, workspaceID]);
  return { ...configuration, onBudgetChange, onValidityChange, onProjectSkillsChange };
}
