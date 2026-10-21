import { APIRequestError } from "../protocol";
import {
  boundedText,
  hasExactKeys,
  isRecord,
  isSHA256,
  safeBoundedCount,
  validDate,
  validWorkspaceEntryName,
  validWorkspaceRelativePath,
} from "../validation";
import type {
  ProjectlessDirectory,
  WorkspaceExplorerView,
  WorkspaceProjectView,
  WorkspaceSearchView,
} from "../../types";

export function parseWorkspaceExplorer(value: unknown, workspaceID: string,
  expectedPath: string): WorkspaceExplorerView {
  if (!hasExactKeys(value, ["content", "entries", "kind", "path", "protocol_version",
    "provenance", "redaction_count", "returned_bytes", "root_path_exposed", "total_bytes",
    "truncated", "workspace_id"]) || value.protocol_version !== "workspace_explorer.v1" ||
    value.workspace_id !== workspaceID || value.path !== expectedPath ||
    (value.kind !== "directory" && value.kind !== "file") || !Array.isArray(value.entries) ||
    value.entries.length > 200 || typeof value.content !== "string" ||
    value.content.length > 131_072 || !safeBoundedCount(value.total_bytes, Number.MAX_SAFE_INTEGER) ||
    !safeBoundedCount(value.returned_bytes, 131_072) ||
    !safeBoundedCount(value.redaction_count, 65_536) || typeof value.truncated !== "boolean" ||
    value.root_path_exposed !== false || !isRecord(value.provenance) ||
    !hasExactKeys(value.provenance, ["content_sha256", "instruction_authorized", "source_kind",
      "source_ref", "version"]) || value.provenance.version !== "context_provenance.v1" ||
    value.provenance.source_ref !== expectedPath || !isSHA256(value.provenance.content_sha256) ||
    value.provenance.instruction_authorized !== false ||
    (value.kind === "directory" && (value.content !== "" || value.total_bytes !== 0 ||
      value.returned_bytes !== 0 || value.provenance.source_kind !== "workspace_listing")) ||
    (value.kind === "file" && (value.entries.length !== 0 ||
      value.provenance.source_kind !== "workspace_file" ||
      new TextEncoder().encode(value.content).length !== value.returned_bytes))) {
    throw new APIRequestError("Workspace explorer response violated its bounded evidence contract",
      "INVALID_RESPONSE", 502);
  }
  const entries = value.entries.map((entry) => {
    if (!hasExactKeys(entry, ["kind", "name", "path", "readable", "size_bytes"])) {
      throw new APIRequestError("Workspace explorer entry widened renderer path authority",
        "INVALID_RESPONSE", 502);
    }
    const expectedEntryPath = expectedPath === "." ? String(entry.name) :
      `${expectedPath}/${String(entry.name)}`;
    if (!validWorkspaceEntryName(entry.name) || !validWorkspaceRelativePath(entry.path) ||
      entry.path !== expectedEntryPath ||
      !["directory", "file", "blocked"].includes(String(entry.kind)) ||
      !safeBoundedCount(entry.size_bytes, Number.MAX_SAFE_INTEGER) ||
      typeof entry.readable !== "boolean" ||
      (entry.kind === "blocked" ? entry.readable !== false : entry.readable !== true) ||
      String(entry.name).startsWith(".cyberagent-edit-")) {
      throw new APIRequestError("Workspace explorer entry widened renderer path authority",
        "INVALID_RESPONSE", 502);
    }
    return entry;
  });
  return { ...value, entries } as unknown as WorkspaceExplorerView;
}

export function parseWorkspaceSearch(value: unknown, workspaceID: string): WorkspaceSearchView {
  if (!hasExactKeys(value, ["protocol_version", "results", "root_path_exposed",
    "scanned_bytes", "scanned_entries", "scanned_files", "truncated", "workspace_id"]) ||
    value.protocol_version !== "workspace_search.v1" || value.workspace_id !== workspaceID ||
    !Array.isArray(value.results) || value.results.length > 50 ||
    !safeBoundedCount(value.scanned_entries, 1000) ||
    !safeBoundedCount(value.scanned_files, 64) ||
    !safeBoundedCount(value.scanned_bytes, 64 * (64 * 1024 + 4)) ||
    typeof value.truncated !== "boolean" || value.root_path_exposed !== false) {
    throw new APIRequestError("Workspace search response violated its bounded evidence contract",
      "INVALID_RESPONSE", 502);
  }
  const results = value.results.map((item) => {
    if (!hasExactKeys(item, ["content_truncated", "line", "match_kind", "path",
      "provenance", "snippet"]) || !validWorkspaceRelativePath(item.path) ||
      !["filename", "content", "filename_and_content"].includes(String(item.match_kind)) ||
      !safeBoundedCount(item.line, Number.MAX_SAFE_INTEGER) ||
      typeof item.snippet !== "string" || new TextEncoder().encode(item.snippet).length > 512 ||
      typeof item.content_truncated !== "boolean" || !hasExactKeys(item.provenance,
        ["content_sha256", "instruction_authorized", "source_kind", "source_ref", "version"]) ||
      item.provenance.version !== "context_provenance.v1" ||
      item.provenance.source_kind !== "workspace_file" ||
      item.provenance.source_ref !== item.path || !isSHA256(item.provenance.content_sha256) ||
      item.provenance.instruction_authorized !== false ||
      (item.match_kind === "filename" ? item.line !== 0 || item.snippet !== "" : item.line < 1)) {
      throw new APIRequestError("Workspace search result widened renderer or instruction authority",
        "INVALID_RESPONSE", 502);
    }
    return item;
  });
  return { ...value, results } as unknown as WorkspaceSearchView;
}

export function parseProjectlessDirectory(value: unknown): ProjectlessDirectory {
  if (!hasExactKeys(value, ["version", "directory_path", "default_directory_path"]) ||
    value.version !== "projectless_directory.v1" || !boundedText(value.directory_path, 4096) ||
    !boundedText(value.default_directory_path, 4096)) {
    throw new APIRequestError("Projectless directory response is invalid", "INVALID_RESPONSE", 502);
  }
  return value as ProjectlessDirectory;
}

export function normalizeWorkspaceName(name: string): string {
  if (typeof name !== "string" || !name.trim() || new TextEncoder().encode(name.trim()).length > 128 || /[\u0000-\u001f\u007f-\u009f]/u.test(name))
    throw new APIRequestError("A project name of 1–128 UTF-8 bytes without control characters is required", "INVALID_ARGUMENT", 400);
  return name.trim();
}

export function parseWorkspaceProject(value: unknown, id: string): WorkspaceProjectView {
  if (!hasExactKeys(value, ["id", "name", "created_at", "directory_path", "thread_count", "active_thread_count"]) || value.id !== id ||
    (typeof value.name !== "string" || !value.name) || !validDate(value.created_at) || typeof value.directory_path !== "string" || !value.directory_path || value.directory_path.length > 4096 ||
    typeof value.thread_count !== "number" || !Number.isSafeInteger(value.thread_count) || value.thread_count < 0 ||
    typeof value.active_thread_count !== "number" || !Number.isSafeInteger(value.active_thread_count) || value.active_thread_count < 0 || value.active_thread_count > value.thread_count)
    throw new APIRequestError("Project details response is invalid", "INVALID_RESPONSE", 502);
  return value as unknown as WorkspaceProjectView;
}
