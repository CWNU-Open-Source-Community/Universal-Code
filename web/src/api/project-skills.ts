export interface ProjectSkillChoice {
  refs: string[];
  confirmUntrustedContext: boolean;
  // "installed" admits refs the project file never pinned. It is a superset of the
  // pinned admission, so one mixed selection can still travel as a single choice.
  source?: "project" | "installed";
}

export function validProjectSkillChoice(value: unknown): value is ProjectSkillChoice {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const choice = value as Record<string, unknown>;
  return Object.keys(choice).every((key) => ["refs", "confirmUntrustedContext", "source"].includes(key)) &&
    typeof choice.confirmUntrustedContext === "boolean" && Array.isArray(choice.refs) && choice.refs.length <= 16 &&
    choice.refs.every((ref) => typeof ref === "string" && ref.length <= 128 && /^[a-z0-9.-]+@[a-z0-9.-]+$/u.test(ref)) &&
    new Set(choice.refs).size === choice.refs.length &&
    (choice.source === undefined || choice.source === "project" || choice.source === "installed");
}

type SkillSelectionRoutes = {
  selectProjectSkills(runID: string, choice: ProjectSkillChoice, idempotencyKey: string): Promise<void>;
  selectInstalledSkills(runID: string, choice: ProjectSkillChoice, idempotencyKey: string): Promise<void>;
};

// One owner for the route decision, because both draft submit paths must derive the
// same idempotency key for one choice; replay depends on the pair staying together.
export function submitSkillChoice(routes: SkillSelectionRoutes, runID: string,
  choice: ProjectSkillChoice, operationID: string,
): Promise<void> {
  return choice.source === "installed"
    ? routes.selectInstalledSkills(runID, choice, `v2-installed-skills-${operationID}`)
    : routes.selectProjectSkills(runID, choice, `v2-project-skills-${operationID}`);
}
