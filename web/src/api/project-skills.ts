export interface ProjectSkillChoice {
  refs: string[];
  confirmUntrustedContext: boolean;
  // "installed" admits refs the project file never pinned. It is a superset of the
  // pinned admission, so one mixed selection can still travel as a single choice.
  source?: "project" | "installed";
}

// Native refs copy the server's canonical skill_read pin. Keep the original
// string for submission and recovery; display labels never become identities.
export function validSkillSelectionRef(value: unknown): value is string {
  if (typeof value !== "string" || value.length > 8192) return false;
  if (/^[a-z0-9.-]+@[a-z0-9.-]+$/u.test(value)) return value.length <= 128;
  try {
    const pin: unknown = JSON.parse(value);
    if (!pin || typeof pin !== "object" || Array.isArray(pin)) return false;
    const record = pin as Record<string, unknown>;
    const fields = ["installation_id", "package_id", "component_id", "revision", "installation_generation"];
    const identity = (part: unknown) => typeof part === "string" && part.length > 0 && part.trim() === part &&
      new TextEncoder().encode(part).length <= 256 && !/[\u0000-\u001f\u007f-\u009f]/u.test(part);
    if (!(Object.keys(record).length === fields.length && Object.keys(record).every((key) => fields.includes(key)) &&
      identity(record.installation_id) && identity(record.package_id) && identity(record.component_id) &&
      typeof record.revision === "string" && /^[a-f0-9]{64}$/u.test(record.revision) &&
      Number.isSafeInteger(record.installation_generation) && Number(record.installation_generation) >= 1)) return false;
    const canonical = JSON.stringify({ installation_id: record.installation_id, package_id: record.package_id,
      component_id: record.component_id, revision: record.revision, installation_generation: record.installation_generation })
      .replace(/[<>&\u2028\u2029]/gu, (part) => `\\u${part.charCodeAt(0).toString(16).padStart(4, "0")}`);
    return canonical === value;
  } catch { return false; }
}

export function validProjectSkillChoice(value: unknown): value is ProjectSkillChoice {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const choice = value as Record<string, unknown>;
  return Object.keys(choice).every((key) => ["refs", "confirmUntrustedContext", "source"].includes(key)) &&
    typeof choice.confirmUntrustedContext === "boolean" && Array.isArray(choice.refs) && choice.refs.length <= 16 &&
    choice.refs.every(validSkillSelectionRef) &&
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
