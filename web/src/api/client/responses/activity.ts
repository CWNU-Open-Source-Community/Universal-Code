import { validMCPContents } from "../../mcp-content";
import { APIRequestError } from "../protocol";
import {
  boundedIdentity,
  hasExactKeys,
  hasOnlyKeys,
  isRecord,
  safeBoundedCount,
  validDate,
} from "../validation";
import type { ThreadActivityArtifactView, ThreadActivityDetailView } from "../../types";

export const threadActivityToolStatuses = ["pending", "completed", "denied", "failed"];

export const threadActivityCommandStatuses = [...threadActivityToolStatuses, "prepared", "running",
  "stopping", "timed_out", "cancelled", "killed", "interrupted"];

export const threadActivityEnvironments = ["Workspace Sandbox", "Host · Full Access",
  "Legacy execution boundary"];

export const threadActivityKinds = ["command", "web_search", "web_fetch", "file_read", "file_edit",
  "verification", "mcp", "browser"];

export const threadActivityToolNames = ["command_runtime", "workspace_list", "workspace_read",
  "workspace_glob", "workspace_grep", "workspace_change", "workspace_apply", "workspace_delete",
  "web_search", "web_fetch", "web_citation", "mcp_tool_search", "mcp_tool_call", "code_workspace_symbols",
  "code_document_symbols", "code_definition", "code_references", "code_implementation",
  "code_hover", "code_signature_help", "code_diagnostics", "code_call_hierarchy",
  "code_type_hierarchy", "github_review_evidence_list", "github_review_evidence_read",
  "browser_status", "browser_navigate", "browser_snapshot", "browser_click", "browser_type",
  "browser_screenshot"];

export const maxThreadActivityArtifactBytes = 4 * 1024 * 1024;

export function validThreadActivityString(value: unknown, maximum: number,
  optional = false): value is string {
  if (typeof value !== "string" || value.includes("\0")) return false;
  const codePoints = Array.from(value).length;
  return codePoints <= maximum && (optional || codePoints > 0);
}

export function validThreadActivityFactText(value: unknown, maximum: number,
  optional = false): value is string {
  if (!validThreadActivityString(value, maximum, optional)) return false;
  if (value === "") return optional;
  return value.trim() === value && !Array.from(value)
    .some((character) => /[\p{Cc}\p{Cf}]/u.test(character) && character !== "\n" && character !== "\t");
}

export function validThreadActivityFactIdentity(value: unknown, optional = false): value is string {
  if (!validThreadActivityString(value, 256, optional)) return false;
  if (value === "") return optional;
  return value.trim() === value && !/[\p{Cc}\p{Cf}]/u.test(value);
}

export function validThreadActivityJSONFields(value: unknown): boolean {
  if (!Array.isArray(value) || value.length > 16) return false;
  const names = new Set<string>();
  for (const field of value) {
    if (!isRecord(field) || !hasExactKeys(field, ["name", "summary", "type"]) ||
      !validThreadActivityFactIdentity(field.name) || names.has(field.name) ||
      !validThreadActivityFactIdentity(field.type) ||
      !validThreadActivityFactText(field.summary, 2_048)) return false;
    names.add(field.name);
  }
  return true;
}

export function validThreadActivityBoundary(value: unknown): boolean {
  return isRecord(value) && hasExactKeys(value, ["authorization", "error_code",
    "failure_reason", "truncated", "untrusted"]) &&
    ["policy_checked", "pending", "denied"].includes(String(value.authorization)) &&
    validThreadActivityFactIdentity(value.error_code, true) &&
    validThreadActivityFactText(value.failure_reason, 2_048, true) &&
    typeof value.truncated === "boolean" && typeof value.untrusted === "boolean";
}

export function validThreadActivityArtifactReference(value: unknown): boolean {
  return isRecord(value) && hasExactKeys(value,
    ["artifact_ref", "mime", "size_bytes", "stream", "truncated"]) &&
    Boolean(boundedIdentity(value.artifact_ref)) &&
    ["stdout", "stderr"].includes(String(value.stream)) &&
    value.mime === "text/plain; charset=utf-8" &&
    safeBoundedCount(value.size_bytes, maxThreadActivityArtifactBytes) &&
    Number(value.size_bytes) > 0 && typeof value.truncated === "boolean";
}

export function validThreadActivityCommand(value: unknown): boolean {
  const required = ["artifacts", "command", "duration_milliseconds", "execution_environment",
    "network", "status", "stderr_preview", "stdout_preview", "truncated",
    "working_directory"];
  return isRecord(value) && hasOnlyKeys(value, [...required, "exit_code"]) &&
    required.every((key) => Object.prototype.hasOwnProperty.call(value, key)) &&
    validThreadActivityString(value.command, 4_096) &&
    validThreadActivityString(value.working_directory, 1_024) &&
    threadActivityEnvironments.includes(String(value.execution_environment)) &&
    (value.network === "disabled" || value.network === "host") &&
    threadActivityCommandStatuses.includes(String(value.status)) &&
    (value.exit_code === undefined ||
      (typeof value.exit_code === "number" && Number.isSafeInteger(value.exit_code))) &&
    safeBoundedCount(value.duration_milliseconds, Number.MAX_SAFE_INTEGER) &&
    validThreadActivityString(value.stdout_preview, 8_192, true) &&
    validThreadActivityString(value.stderr_preview, 8_192, true) &&
    typeof value.truncated === "boolean" && Array.isArray(value.artifacts) &&
    value.artifacts.length <= 2 && value.artifacts.every(validThreadActivityArtifactReference);
}

export function validThreadActivityJSONSummary(value: unknown): boolean {
  return isRecord(value) && hasExactKeys(value, ["count", "fields", "summary", "type"]) &&
    safeBoundedCount(value.count, Number.MAX_SAFE_INTEGER) &&
    validThreadActivityFactIdentity(value.type) &&
    validThreadActivityFactText(value.summary, 2_048) &&
    validThreadActivityJSONFields(value.fields);
}

export function validSafeHTTPSActivityURL(value: unknown, optional = false): boolean {
  if (!validThreadActivityFactText(value, 2_048, optional)) return false;
  if (value === "") return optional;
  try {
    const parsed = new URL(value);
    if (parsed.protocol !== "https:" || parsed.username !== "" || parsed.password !== "" ||
      parsed.port !== "" || parsed.hash !== "") return false;
    for (const [key, queryValue] of parsed.searchParams) {
      const compact = key.trim().toLowerCase().replace(/[-. _[\]]/gu, "");
      if (["key", "sig", "jwt", "session", "sessionid", "auth"].includes(compact) ||
        ["apikey", "accesstoken", "refreshtoken", "idtoken", "secret", "password",
          "passwd", "privatekey", "signature", "authorization", "credential", "cookie",
          "setcookie", "bearertoken"].some((part) => compact.includes(part)) ||
        compact.startsWith("auth") || compact.endsWith("token") ||
        /(?:bearer\s+|\bsk-(?:proj-)?[a-z0-9_-]{12,})/iu.test(queryValue)) return false;
    }
    return true;
  } catch {
    return false;
  }
}

export function validThreadActivityTypedDetail(value: unknown): boolean {
  if (!isRecord(value) || !threadActivityKinds.includes(String(value.kind))) return false;
  switch (value.kind) {
    case "command": {
      if (!hasExactKeys(value, ["command", "kind"]) || !isRecord(value.command) ||
        !hasExactKeys(value.command, ["commands"]) || !Array.isArray(value.command.commands) ||
        value.command.commands.length > 32) return false;
      return value.command.commands.every(validThreadActivityCommand);
    }
    case "web_search": {
      if (!hasExactKeys(value, ["kind", "web_search"]) || !isRecord(value.web_search)) return false;
      const detail = value.web_search;
      if (!hasExactKeys(detail, ["boundary", "citeable", "limit", "operation", "provider",
        "query", "search_policy", "selection_reason", "source_count", "sources"]) ||
        !validThreadActivityFactText(detail.operation, 2_048) ||
        !validThreadActivityFactText(detail.query, 2_048) ||
        !safeBoundedCount(detail.limit, 10) || Number(detail.limit) < 1 ||
        !validThreadActivityFactText(detail.provider, 2_048, true) ||
        !validThreadActivityFactIdentity(detail.search_policy, true) ||
        !validThreadActivityFactText(detail.selection_reason, 2_048, true) ||
        !safeBoundedCount(detail.source_count, Number.MAX_SAFE_INTEGER) ||
        typeof detail.citeable !== "boolean" || !Array.isArray(detail.sources) ||
        detail.sources.length > 10 || !validThreadActivityBoundary(detail.boundary)) return false;
      return detail.sources.every((source, index) => isRecord(source) &&
        hasExactKeys(source, ["citeable", "provider", "rank", "state", "title", "url"]) &&
        source.rank === index + 1 && validThreadActivityFactText(source.title, 2_048, true) &&
        validSafeHTTPSActivityURL(source.url) &&
        validThreadActivityFactText(source.provider, 2_048, true) &&
        validThreadActivityFactIdentity(source.state) && typeof source.citeable === "boolean");
    }
    case "web_fetch": {
      if (!hasExactKeys(value, ["kind", "web_fetch"]) || !isRecord(value.web_fetch)) return false;
      const detail = value.web_fetch;
      return hasExactKeys(detail, ["boundary", "citeable", "http_status", "operation",
        "partial", "redirects", "robots", "robots_policy", "state", "url"]) &&
        validThreadActivityFactText(detail.operation, 2_048) &&
        (detail.url === "已发现的网页来源" || detail.url === "已抓取的网页快照" ||
          validSafeHTTPSActivityURL(detail.url)) && validThreadActivityFactIdentity(detail.state, true) &&
        safeBoundedCount(detail.http_status, 599) &&
        (detail.http_status === 0 || Number(detail.http_status) >= 100) &&
        validThreadActivityFactIdentity(detail.robots, true) &&
        validThreadActivityFactIdentity(detail.robots_policy, true) &&
        safeBoundedCount(detail.redirects, 32) && typeof detail.partial === "boolean" &&
        typeof detail.citeable === "boolean" && validThreadActivityBoundary(detail.boundary);
    }
    case "file_read": {
      if (!hasExactKeys(value, ["file_read", "kind"]) || !isRecord(value.file_read)) return false;
      const detail = value.file_read;
      return hasExactKeys(detail, ["boundary", "end_line", "limit", "operation", "path",
        "pattern", "query", "result_count", "start_line", "summary", "truncated"]) &&
        validThreadActivityFactText(detail.operation, 2_048) &&
        validThreadActivityFactText(detail.path, 2_048, true) &&
        validThreadActivityFactText(detail.pattern, 2_048, true) &&
        validThreadActivityFactText(detail.query, 2_048, true) &&
        safeBoundedCount(detail.start_line, Number.MAX_SAFE_INTEGER) &&
        safeBoundedCount(detail.end_line, Number.MAX_SAFE_INTEGER) &&
        safeBoundedCount(detail.limit, Number.MAX_SAFE_INTEGER) &&
        safeBoundedCount(detail.result_count, Number.MAX_SAFE_INTEGER) &&
        typeof detail.truncated === "boolean" &&
        validThreadActivityFactText(detail.summary, 2_048) &&
        validThreadActivityBoundary(detail.boundary);
    }
    case "file_edit": {
      if (!hasExactKeys(value, ["file_edit", "kind"]) || !isRecord(value.file_edit)) return false;
      const detail = value.file_edit;
      return hasExactKeys(detail, ["action", "applied", "apply_status", "boundary",
        "destination_path", "diff", "diff_available", "edit_id", "file_written", "operation",
        "path", "replayed"]) &&
        validThreadActivityFactText(detail.operation, 2_048) &&
        validThreadActivityFactIdentity(detail.action, true) &&
        validThreadActivityFactText(detail.path, 2_048, true) &&
        validThreadActivityFactText(detail.destination_path, 2_048, true) &&
        validThreadActivityFactIdentity(detail.apply_status, true) &&
        typeof detail.diff_available === "boolean" &&
        validThreadActivityFactIdentity(detail.edit_id, !detail.diff_available) &&
        (!detail.diff_available || detail.edit_id !== "") &&
        typeof detail.applied === "boolean" && typeof detail.file_written === "boolean" &&
        typeof detail.replayed === "boolean" && isRecord(detail.diff) &&
        hasExactKeys(detail.diff, ["added_lines", "hunks", "removed_lines", "summary"]) &&
        safeBoundedCount(detail.diff.added_lines, Number.MAX_SAFE_INTEGER) &&
        safeBoundedCount(detail.diff.removed_lines, Number.MAX_SAFE_INTEGER) &&
        safeBoundedCount(detail.diff.hunks, Number.MAX_SAFE_INTEGER) &&
        validThreadActivityFactText(detail.diff.summary, 2_048, true) &&
        validThreadActivityBoundary(detail.boundary);
    }
    case "mcp": {
      if (!hasExactKeys(value, ["kind", "mcp"]) || !isRecord(value.mcp)) return false;
      const detail = value.mcp;
      return hasExactKeys(detail, ["arguments", "boundary", "operation", "result", "server",
        "tool", ...["contents", "omitted_contents", "content_truncated"].filter((key) => detail[key] !== undefined)]) &&
        (detail.contents === undefined || validMCPContents(detail.contents)) &&
        (detail.omitted_contents === undefined || safeBoundedCount(detail.omitted_contents, 65536)) &&
        (detail.content_truncated === undefined || typeof detail.content_truncated === "boolean") && validThreadActivityFactText(detail.operation, 2_048) &&
        validThreadActivityFactIdentity(detail.server) && validThreadActivityFactIdentity(detail.tool) &&
        validThreadActivityJSONFields(detail.arguments) &&
        validThreadActivityJSONSummary(detail.result) &&
        validThreadActivityBoundary(detail.boundary);
    }
    case "verification": {
      if (!hasExactKeys(value, ["kind", "verification"]) || !isRecord(value.verification)) return false;
      const detail = value.verification;
      return hasExactKeys(detail, ["boundary", "direction", "limit", "operation", "path",
        "position", "query", "result_count", "summary", "tool", "truncated"]) &&
        validThreadActivityFactText(detail.operation, 2_048) &&
        validThreadActivityFactIdentity(detail.tool) &&
        validThreadActivityFactText(detail.path, 2_048, true) &&
        validThreadActivityFactText(detail.query, 2_048, true) &&
        validThreadActivityFactIdentity(detail.position, true) &&
        validThreadActivityFactIdentity(detail.direction, true) &&
        safeBoundedCount(detail.limit, Number.MAX_SAFE_INTEGER) &&
        safeBoundedCount(detail.result_count, Number.MAX_SAFE_INTEGER) &&
        typeof detail.truncated === "boolean" &&
        validThreadActivityFactText(detail.summary, 2_048) &&
        validThreadActivityBoundary(detail.boundary);
    }
    case "browser": {
      if (!hasExactKeys(value, ["browser", "kind"]) || !isRecord(value.browser)) return false;
      const detail = value.browser;
      return hasExactKeys(detail, ["action", "artifact_bytes", "boundary", "input_length",
        "operation", "selector", "summary", "url"]) &&
        validThreadActivityFactText(detail.operation, 2_048) &&
        validThreadActivityFactIdentity(detail.action) &&
        validThreadActivityFactText(detail.url, 2_048, true) &&
        validThreadActivityFactText(detail.selector, 2_048, true) &&
        safeBoundedCount(detail.input_length, Number.MAX_SAFE_INTEGER) &&
        safeBoundedCount(detail.artifact_bytes, maxThreadActivityArtifactBytes) &&
        validThreadActivityFactText(detail.summary, 2_048) &&
        validThreadActivityBoundary(detail.boundary);
    }
    default:
      return false;
  }
}

export function expectedThreadActivityKind(tool: string): string {
  if (tool === "command_runtime") return "command";
  if (tool === "web_search") return "web_search";
  if (tool === "web_fetch" || tool === "web_citation") return "web_fetch";
  if (["workspace_list", "workspace_read", "workspace_glob", "workspace_grep"].includes(tool)) return "file_read";
  if (["workspace_change", "workspace_apply", "workspace_delete"].includes(tool)) return "file_edit";
  if (tool === "mcp_tool_call" || tool === "mcp_tool_search") return "mcp";
  if (tool.startsWith("browser_")) return "browser";
  return "verification";
}

export function parseThreadActivityDetail(value: unknown, expectedThreadID: string,
  expectedActivityRef: string): ThreadActivityDetailView {
  if (!hasExactKeys(value, ["activity_ref", "run_id", "tools", "version"]) ||
    value.version !== "thread_activity_detail.v2" ||
    boundedIdentity(value.activity_ref) !== expectedActivityRef ||
    !boundedIdentity(value.run_id) || !Array.isArray(value.tools) || value.tools.length !== 1) {
    throw new APIRequestError("Thread activity detail is invalid", "INVALID_RESPONSE", 502);
  }
  const tool = value.tools[0];
  const toolRequired = ["agent_id", "agent_label", "agent_role", "detail",
    "duration_milliseconds", "label", "name", "started_at", "status"];
  if (!isRecord(tool) || !hasOnlyKeys(tool, [...toolRequired, "completed_at"]) ||
    toolRequired.some((key) => !Object.prototype.hasOwnProperty.call(tool, key)) ||
    !threadActivityToolNames.includes(String(tool.name)) || !validThreadActivityString(tool.label, 128) ||
    !validThreadActivityString(tool.agent_label, 128) ||
    !boundedIdentity(tool.agent_id) || !["root", "specialist", "unknown"].includes(String(tool.agent_role)) ||
    !threadActivityToolStatuses.includes(String(tool.status)) || !validDate(tool.started_at) ||
    (tool.completed_at !== undefined && (!validDate(tool.completed_at) ||
      Date.parse(String(tool.completed_at)) < Date.parse(String(tool.started_at)))) ||
    !safeBoundedCount(tool.duration_milliseconds, Number.MAX_SAFE_INTEGER) ||
    !validThreadActivityTypedDetail(tool.detail) ||
    !isRecord(tool.detail) || tool.detail.kind !== expectedThreadActivityKind(String(tool.name))) {
    throw new APIRequestError("Thread activity tool detail is invalid", "INVALID_RESPONSE", 502);
  }
  // The endpoint is Thread-scoped at the URL and store join. Keep the parameter
  // in this parser contract so callers cannot accidentally reuse a response
  // under another Thread cache key.
  if (boundedIdentity(expectedThreadID) !== expectedThreadID) {
    throw new APIRequestError("Thread activity owner is invalid", "INVALID_RESPONSE", 502);
  }
  return value as unknown as ThreadActivityDetailView;
}

export function parseThreadActivityArtifact(value: unknown, expectedActivityRef: string,
  expectedArtifactRef: string): ThreadActivityArtifactView {
  if (!isRecord(value) || !hasExactKeys(value, ["activity_ref", "artifact_ref", "content",
    "instruction_authorized", "mime", "redacted", "sha256", "size_bytes", "stream",
    "truncated", "untrusted", "version"]) ||
    value.version !== "thread_activity_artifact.v1" ||
    value.activity_ref !== expectedActivityRef || value.artifact_ref !== expectedArtifactRef ||
    !["stdout", "stderr"].includes(String(value.stream)) ||
    value.mime !== "text/plain; charset=utf-8" ||
    !validThreadActivityString(value.content, maxThreadActivityArtifactBytes, true) ||
    new TextEncoder().encode(value.content as string).byteLength !== value.size_bytes ||
    !safeBoundedCount(value.size_bytes, maxThreadActivityArtifactBytes) ||
    !/^[0-9a-f]{64}$/u.test(String(value.sha256)) || typeof value.redacted !== "boolean" ||
    typeof value.truncated !== "boolean" || value.untrusted !== true ||
    value.instruction_authorized !== false) {
    throw new APIRequestError("Thread activity artifact is invalid", "INVALID_RESPONSE", 502);
  }
  return value as unknown as ThreadActivityArtifactView;
}
