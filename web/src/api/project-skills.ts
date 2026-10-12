export interface ProjectSkillChoice { refs: string[]; confirmUntrustedContext: boolean }

export function validProjectSkillChoice(value: unknown): value is ProjectSkillChoice {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const choice = value as Record<string, unknown>;
  return Object.keys(choice).every((key) => ["refs", "confirmUntrustedContext"].includes(key)) &&
    typeof choice.confirmUntrustedContext === "boolean" && Array.isArray(choice.refs) && choice.refs.length <= 16 &&
    choice.refs.every((ref) => typeof ref === "string" && ref.length <= 128 && /^[a-z0-9.-]+@[a-z0-9.-]+$/u.test(ref)) &&
    new Set(choice.refs).size === choice.refs.length;
}
