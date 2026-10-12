import type { TaskBudgetSettings, TaskConfigurationView } from "./types";

const record = (value: unknown): value is Record<string, unknown> => Boolean(value) && typeof value === "object" && !Array.isArray(value);
const keys = (value: Record<string, unknown>, allowed: readonly string[]) => Object.keys(value).every((key) => allowed.includes(key));
const integer = (value: unknown, min: number, max: number) => Number.isSafeInteger(value) && Number(value) >= min && Number(value) <= max;
const digest = (value: unknown) => typeof value === "string" && /^[a-f0-9]{64}$/u.test(value);
const profiles = ["code", "learn", "review", "script"];
const budgetKeys = ["max_turns", "max_tokens", "max_tool_calls", "max_cost_usd", "timeout_seconds"] as const;
const configurationFields = budgetKeys.map((key) => `budget.${key}`).concat(["read_only", "allowed_profiles", "exclude_paths", "skill_suggestions", "test_command_id", "format_command_id"]);

type NormalizedTaskBudget = { max_turns: number; max_tokens: number; max_tool_calls: number; max_cost_usd: number; timeout_seconds: number };

export function normalizedTaskBudget(input?: TaskBudgetSettings): NormalizedTaskBudget {
	if (input !== undefined && (!record(input) || !keys(input, budgetKeys))) throw new Error("预算字段无效。");
  if (input !== undefined && Object.values(input).some((value) => typeof value !== "number" || !Number.isFinite(value))) throw new Error("预算字段必须为有限数字。");
  const result = { max_turns: 100, max_tool_calls: 100, max_tokens: 0, max_cost_usd: 0, timeout_seconds: 0, ...input };
  if (!validTaskBudget(result)) throw new Error("预算超出支持范围。");
  if (result.max_cost_usd > 0 && result.max_cost_usd < 0.000001) throw new Error("费用上限至少为 0.000001 USD。");
  result.max_cost_usd = Math.round(result.max_cost_usd * 1_000_000) / 1_000_000;
  return result;
}

function validTaskBudget(value: unknown): value is NormalizedTaskBudget {
  return validStoredBudget(value) && integer(value.max_turns, 1, 10_000) && integer(value.max_tool_calls, 1, 1_000_000) &&
    integer(value.max_tokens ?? 0, 0, 1_000_000_000) && integer(value.timeout_seconds ?? 0, 0, 604_800) && Number(value.max_cost_usd ?? 0) <= 100_000;
}

// Historical CLI Runs may have zero/omitted tool limits and larger ceilings.
// Read their saved values without applying product-creation defaults or bounds.
function validStoredBudget(value: unknown): value is NormalizedTaskBudget {
  return record(value) && keys(value, budgetKeys) && Object.values(value).every((item) => typeof item === "number" && Number.isFinite(item)) &&
    integer(value.max_turns, 1, Number.MAX_SAFE_INTEGER) && integer(value.max_tokens ?? 0, 0, Number.MAX_SAFE_INTEGER) &&
    integer(value.max_tool_calls ?? 0, 0, Number.MAX_SAFE_INTEGER) && integer(value.timeout_seconds ?? 0, 0, 9_223_372_036) && Number(value.max_cost_usd ?? 0) >= 0;
}

function sameBudget(actual: unknown, expected: NormalizedTaskBudget): boolean {
  return validTaskBudget(actual) && budgetKeys.every((key) => (actual[key] ?? 0) === expected[key]);
}

// Check the creation result against operator ceilings. Project narrowing can
// reduce turns/tools only and must carry its Go-owned immutable fingerprint.
export function creationBudgetMatches(actual: unknown, config: Record<string, unknown>, input?: TaskBudgetSettings): boolean {
  const expected = normalizedTaskBudget(input);
  if (!validTaskBudget(actual) || actual.max_turns > expected.max_turns || actual.max_tool_calls > expected.max_tool_calls ||
    ["max_tokens", "max_cost_usd", "timeout_seconds"].some((key) => (actual[key as keyof typeof expected] ?? 0) !== expected[key as keyof typeof expected])) return false;
  if (config.requested_budget !== undefined && !sameBudget(config.requested_budget, expected)) return false;
  const narrowed = actual.max_turns !== expected.max_turns || actual.max_tool_calls !== expected.max_tool_calls;
  return !narrowed || digest(config.project_config_fingerprint);
}

export function parseTaskConfiguration(value: unknown, workspaceID?: string, profile?: string, readMode: "preview" | "snapshot" = "preview"): TaskConfigurationView {
  if (!record(value) || !keys(value, ["version", "workspace_id", "profile", "requested_budget", "budget", "sources", "project_disposition", "project", "project_fingerprint", "rejections", "fingerprint", "capability_grant"]) ||
    value.version !== "task_configuration.v1" || value.capability_grant !== false || typeof value.workspace_id !== "string" ||
    (workspaceID !== undefined && value.workspace_id !== workspaceID) || !profiles.includes(String(value.profile)) || (profile !== undefined && value.profile !== profile) ||
    !Array.isArray(value.sources) || value.sources.length > 11 || !Array.isArray(value.rejections) || value.rejections.length > 16 ||
    !["absent", "applied", "rejected"].includes(String(value.project_disposition))) throw new Error("任务配置响应的格式或来源不匹配。");
  const seen = new Set<string>();
  for (const source of value.sources) {
    if (!record(source) || !keys(source, ["field", "source"]) || typeof source.field !== "string" || !configurationFields.includes(source.field) ||
      seen.has(source.field) || !["default", "operator", "project", "snapshot"].includes(String(source.source))) throw new Error("任务配置字段来源无效。");
    seen.add(source.field);
  }
  const legacySnapshot = value.sources.length > 0 && value.sources.every((source) => (source as Record<string, unknown>).source === "snapshot");
  const budgetValidator = readMode === "snapshot" && legacySnapshot ? validStoredBudget : validTaskBudget;
  const requestedBudget = value.requested_budget, actualBudget = value.budget;
  if (!budgetValidator(requestedBudget) || !budgetValidator(actualBudget) ||
    value.sources.some((source) => (source as Record<string, unknown>).source === "snapshot") && (readMode !== "snapshot" || !legacySnapshot)) throw new Error("任务预算绑定无效。");
  for (const rejection of value.rejections) {
    if (!record(rejection) || !keys(rejection, ["field", "reason"]) || typeof rejection.field !== "string" ||
      ![...configurationFields, "project_config"].includes(rejection.field) || typeof rejection.reason !== "string" || rejection.reason.length > 256) throw new Error("任务配置拒绝记录无效。");
  }
  if (value.project_disposition === "rejected") {
    if (value.rejections.length === 0 || value.project !== undefined || value.fingerprint !== undefined || value.project_fingerprint !== undefined || value.sources.length !== 0) throw new Error("拒绝的配置包含部分生效记录。");
  } else {
    const budgetsMatch = legacySnapshot
      ? budgetKeys.every((key) => (actualBudget[key] ?? 0) === (requestedBudget[key] ?? 0))
      : creationBudgetMatches(actualBudget, { project_config_fingerprint: value.project_fingerprint }, requestedBudget as TaskBudgetSettings);
    if (value.rejections.length !== 0 || !digest(value.fingerprint) || budgetKeys.some((key) => !seen.has(`budget.${key}`)) || !budgetsMatch) throw new Error("任务预算绑定无效。");
    if (value.project_disposition === "applied") {
      const project = value.project;
      if (!digest(value.project_fingerprint) || !record(project) || !keys(project, ["protocol", "read_only", "allowed_profiles", "excluded_path_count", "skill_suggestion_count", "skill_suggestions", "test_command_id", "format_command_id"]) ||
        project.protocol !== "project_config.v1" || typeof project.read_only !== "boolean" || !Array.isArray(project.allowed_profiles) || project.allowed_profiles.length > 4 ||
        project.allowed_profiles.some((item) => !profiles.includes(String(item))) || !integer(project.excluded_path_count, 0, 64) || !integer(project.skill_suggestion_count, 0, 16) ||
        (project.skill_suggestions !== undefined && (!Array.isArray(project.skill_suggestions) || project.skill_suggestions.length !== project.skill_suggestion_count ||
          project.skill_suggestions.some((ref) => typeof ref !== "string" || ref.length > 128 || !/^[a-z0-9.-]+@[a-z0-9.-]+$/u.test(ref)))) ||
        [project.test_command_id, project.format_command_id].some((item) => item !== undefined && (typeof item !== "string" || !/^[a-zA-Z0-9_.:-]{1,128}$/u.test(item)))) throw new Error("项目配置投影无效。");
    } else if (value.project !== undefined || value.project_fingerprint !== undefined) throw new Error("缺失项目配置却包含项目快照。");
  }
  return value as unknown as TaskConfigurationView;
}
