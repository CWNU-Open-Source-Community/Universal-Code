import { APIRequestError } from "./client";
import { applicationServiceIdentity } from "./application-services";

export const commandServicePolicyVersion = "thread_command_service_policy.v1";
export type CommandServicePolicy = "auto" | "exclusive";
export interface ThreadCommandServicePolicyView {
  version: typeof commandServicePolicyVersion;
  thread_id: string;
  policy: CommandServicePolicy;
  revision: number;
}
export interface ThreadCommandServicePolicyRequest {
  version: typeof commandServicePolicyVersion;
  policy: CommandServicePolicy;
  expected_revision: number;
}

const record = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === "object" && !Array.isArray(value);
const policy = (value: unknown): value is CommandServicePolicy => value === "auto" || value === "exclusive";
const revision = (value: unknown): value is number => typeof value === "number" && Number.isSafeInteger(value) && value > 0;

export function validCommandServicePolicyRequest(value: unknown): value is ThreadCommandServicePolicyRequest {
  return record(value) && Object.keys(value).length === 3 && value.version === commandServicePolicyVersion &&
    policy(value.policy) && revision(value.expected_revision);
}

export function parseThreadCommandServicePolicy(value: unknown, threadID: string): ThreadCommandServicePolicyView {
  if (!record(value) || Object.keys(value).length !== 4 || value.version !== commandServicePolicyVersion ||
    !applicationServiceIdentity(value.thread_id) || value.thread_id !== threadID || !policy(value.policy) || !revision(value.revision)) {
    throw new APIRequestError("开发服务策略无法验证，请重新读取当前任务设置。", "INVALID_RESPONSE", 502);
  }
  return value as unknown as ThreadCommandServicePolicyView;
}
