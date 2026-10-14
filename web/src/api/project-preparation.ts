import { APIRequestError } from "./client";
import type { ProjectActionJobView } from "./types";

export interface ProjectPreparationPlan {
  fingerprint: string;
  ecosystem: string;
  toolchain: string;
  requested_version: string;
  files: Array<{ path: string; sha256: string }>;
  sources: string[];
  package_count: number;
  install_command: string;
  test_command: string;
  ready: boolean;
  issues: string[];
}

export type ProjectPreparationState = "pending" | "downloading" | "installing" | "ready" | "failed" | "cancelled" | "interrupted";
export interface ProjectPreparationOperation {
  protocol_version: "project-preparation.v1";
  run_id: string;
  target: string;
  id: string;
  state: ProjectPreparationState;
  completed_packages: number;
  total_packages: number;
  downloaded_bytes: number;
  cache_hits: number;
  job?: ProjectActionJobView;
  error: string;
  can_cancel: boolean;
  created_at: string;
  updated_at: string;
}
export interface ProjectPreparationView {
  protocol_version: "project-preparation.v1";
  run_id: string;
  backend: string;
  target: string;
  available: boolean;
  reason: string;
  plan: ProjectPreparationPlan;
  cache: { status: "empty" | "partial" | "ready"; cached_packages: number; total_packages: number };
  latest?: ProjectPreparationOperation;
}
export interface ProjectPreparationRequest {
  version: "project-preparation.v1";
  target: string;
  expected_fingerprint: string;
  allowed_sources: string[];
  confirm_execution: true;
  run_install_scripts: boolean;
}

const record = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === "object" && !Array.isArray(value);
const text = (value: unknown, limit = 4096): value is string =>
  typeof value === "string" && value.length <= limit && !/[\u0000\u007f]/u.test(value);
const count = (value: unknown): value is number => Number.isSafeInteger(value) && Number(value) >= 0;
const hash = (value: unknown): value is string => typeof value === "string" && /^[a-f0-9]{64}$/u.test(value);
const states = new Set<ProjectPreparationState>(["pending", "downloading", "installing", "ready", "failed", "cancelled", "interrupted"]);
const jobStates = new Set(["prepared", "preparing", "running", "stopping", "completed", "failed", "timed_out", "cancelled", "killed", "interrupted"]);
function invalid(): never {
  throw new APIRequestError("项目准备响应与当前执行不匹配。", "INVALID_RESPONSE", 502);
}
function source(value: unknown): value is string {
  if (!text(value, 2048)) return false;
  try {
    const url = new URL(value);
    return url.protocol === "https:" && url.origin === value && !url.username && !url.password &&
      !url.hostname.includes("*") && url.hostname !== "localhost";
  } catch { return false; }
}
export function validPreparationTarget(value: unknown): value is string {
  return text(value) && value.length > 0 && value.trim() === value && !/^[\\/]|^[a-z]:/iu.test(value) &&
    !value.split(/[\\/]/u).some((part) => part === "..") && !/[\u0000-\u001f\u007f]/u.test(value);
}
export function normalizePreparationTarget(value: string): string {
  if (!validPreparationTarget(value)) throw new Error("请输入项目内的相对目录。");
  return value.split(/[\\/]/u).filter((part) => part && part !== ".").join("/") || ".";
}
export function projectPreparationIsActive(state: string): boolean {
  return state === "pending" || state === "downloading" || state === "installing";
}
export function validProjectPreparationRequest(value: unknown): value is ProjectPreparationRequest {
  return record(value) && value.version === "project-preparation.v1" && validPreparationTarget(value.target) &&
    hash(value.expected_fingerprint) && Array.isArray(value.allowed_sources) && value.allowed_sources.length <= 32 &&
    value.allowed_sources.every(source) && new Set(value.allowed_sources).size === value.allowed_sources.length &&
    value.confirm_execution === true && typeof value.run_install_scripts === "boolean";
}

export function parseProjectPreparationOperation(value: unknown, runID: string, operationID?: string): ProjectPreparationOperation {
  if (!record(value) || value.protocol_version !== "project-preparation.v1" || value.run_id !== runID ||
    !validPreparationTarget(value.target) || !text(value.id, 256) || !value.id ||
    (operationID !== undefined && value.id !== operationID) || !states.has(value.state as ProjectPreparationState) ||
    !count(value.completed_packages) || !count(value.total_packages) || value.completed_packages > value.total_packages ||
    !count(value.downloaded_bytes) || !count(value.cache_hits) || value.cache_hits > value.total_packages ||
    !text(value.error, 8192) || typeof value.can_cancel !== "boolean" ||
    (value.can_cancel && !projectPreparationIsActive(String(value.state))) ||
    !text(value.created_at, 128) || !text(value.updated_at, 128)) invalid();
  if (value.job !== undefined) {
    const job = value.job;
    if (!record(job) || !text(job.id, 256) || !job.id || !jobStates.has(String(job.state)) ||
      !text(job.stdout, 131072) || !text(job.stderr, 131072) || typeof job.stdout_truncated !== "boolean" ||
      typeof job.stderr_truncated !== "boolean" || typeof job.can_stop !== "boolean" ||
      (job.exit_code !== undefined && !Number.isSafeInteger(job.exit_code))) invalid();
  }
  return value as unknown as ProjectPreparationOperation;
}

export function parseProjectPreparation(value: unknown, runID: string, target?: string): ProjectPreparationView {
  if (!record(value) || value.protocol_version !== "project-preparation.v1" || value.run_id !== runID ||
    !validPreparationTarget(value.target) || (target !== undefined && value.target !== target) ||
    !text(value.backend, 128) || typeof value.available !== "boolean" || !text(value.reason, 8192) ||
    !record(value.plan) || !record(value.cache)) invalid();
  const plan = value.plan;
  if (!hash(plan.fingerprint) || !text(plan.ecosystem, 128) || !text(plan.toolchain, 256) ||
    !text(plan.requested_version, 256) || !Array.isArray(plan.files) || plan.files.length > 64 ||
    plan.files.some((file) => !record(file) || !validPreparationTarget(file.path) || !hash(file.sha256)) ||
    !Array.isArray(plan.sources) || plan.sources.length > 32 || !plan.sources.every(source) ||
    new Set(plan.sources).size !== plan.sources.length || !count(plan.package_count) ||
    !text(plan.install_command) || !text(plan.test_command) || typeof plan.ready !== "boolean" ||
    !Array.isArray(plan.issues) || plan.issues.length > 64 || !plan.issues.every((item) => text(item, 8192)) ||
    !["empty", "partial", "ready"].includes(String(value.cache.status)) ||
    !count(value.cache.cached_packages) || !count(value.cache.total_packages) ||
    value.cache.cached_packages > value.cache.total_packages ||
    (value.available && !plan.ready)) invalid();
  if (value.latest !== undefined && parseProjectPreparationOperation(value.latest, runID).target !== value.target) invalid();
  return value as unknown as ProjectPreparationView;
}
