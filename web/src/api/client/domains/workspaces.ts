import { maximumFileBytes, validFileAttachment } from "../../file-attachments";
import { acceptedImageTypes, maximumImageBytes, validImageAttachment } from "../../image-attachments";
import { APIRequestError } from "../protocol";
import {
  normalizeWorkspaceName,
  parseProjectlessDirectory,
  parseWorkspaceExplorer,
  parseWorkspaceProject,
  parseWorkspaceSearch,
} from "../responses/workspaces";
import {
  boundedIdentity,
  boundedText,
  hasExactKeys,
  isRecord,
  isSHA256,
  validDate,
  validWorkspaceRelativePath,
} from "../validation";
import type { WorkspaceFileAttachment } from "../../file-attachments";
import type { WorkspaceImageAttachment } from "../../image-attachments";
import type {
  ProjectlessDirectory,
  ProjectlessWorkspaceView,
  WorkspaceExplorerView,
  WorkspaceImportView,
  WorkspaceProjectView,
  WorkspaceSearchView,
} from "../../types";
import type { ClientFeatures } from "../capabilities";
import type { APITransport } from "../transport";

export class WorkspacesClient {
  constructor(
    private readonly transport: APITransport,
    private readonly features: ClientFeatures,
  ) {}

  async createProjectlessWorkspace(idempotencyKey: string, signal?: AbortSignal): Promise<ProjectlessWorkspaceView> {
    if (!this.features.hasProjectlessWorkspaces) throw new Error("Projectless conversations are unavailable");
    const value = await this.transport.sendControl<unknown>("/workspaces/projectless", {
      version: "projectless_workspace.v1",
    }, idempotencyKey, signal);
    if (!hasExactKeys(value, ["version", "workspace", "replayed"]) ||
      value.version !== "projectless_workspace.v1" || typeof value.replayed !== "boolean" ||
      !hasExactKeys(value.workspace, ["id", "name", "created_at"]) ||
      !boundedIdentity(value.workspace.id) || !boundedText(value.workspace.name, 128) ||
      !validDate(value.workspace.created_at)) {
      throw new APIRequestError("Projectless workspace response is invalid", "INVALID_RESPONSE", 502);
    }
    return value as unknown as ProjectlessWorkspaceView;
  }

  async getProjectlessDirectory(signal?: AbortSignal): Promise<ProjectlessDirectory> {
    if (!this.features.hasProjectlessWorkspaces) throw new Error("Projectless conversations are unavailable");
    return parseProjectlessDirectory(await this.transport.sendControlRequest<unknown>(
      "/settings/projectless-directory", undefined, signal, "", "GET"));
  }

  async setProjectlessDirectory(directoryPath: string, signal?: AbortSignal): Promise<ProjectlessDirectory> {
    if (!this.features.hasProjectlessWorkspaces || !this.features.hasWorkspaceImport || !directoryPath.trim()) {
      throw new Error("Choose a directory with workspace import enabled");
    }
    return parseProjectlessDirectory(await this.transport.sendControlRequest<unknown>("/settings/projectless-directory", {
      version: "projectless_directory.v1", directory_path: directoryPath, confirmed: true,
    }, signal, "", "PUT"));
  }

  async getWorkspaceProject(id: string, signal?: AbortSignal): Promise<WorkspaceProjectView> {
    if (!boundedIdentity(id)) throw new Error("A project identity is required");
    return parseWorkspaceProject(await this.transport.sendControlRequest<unknown>(`/workspaces/${encodeURIComponent(id)}/project`, undefined, signal, "", "GET"), id);
  }

  async renameWorkspace(id: string, name: string, signal?: AbortSignal): Promise<WorkspaceProjectView> {
    if (!boundedIdentity(id)) throw new Error("A project identity is required");
    return parseWorkspaceProject(await this.transport.sendControlRequest<unknown>(`/workspaces/${encodeURIComponent(id)}/name`, { name: normalizeWorkspaceName(name) }, signal, "", "PUT"), id);
  }

  async importWorkspace(directoryPath: string, signal?: AbortSignal, name?: string): Promise<WorkspaceImportView> {
    if (!this.features.hasWorkspaceImport) {
      throw new APIRequestError("Workspace import is unavailable for this connection",
        "PERMISSION_DENIED", 403);
    }
    if (typeof directoryPath !== "string" || !directoryPath.trim() ||
      /[\u0000-\u001f\u007f]/u.test(directoryPath) ||
      new TextEncoder().encode(directoryPath).length > 4_096) {
      throw new APIRequestError("A valid project directory is required", "INVALID_ARGUMENT", 400);
    }
    if (name !== undefined) name = normalizeWorkspaceName(name);
    try {
      const value = await this.transport.sendControlRequest<unknown>("/workspaces/import", {
        version: "workspace_import.v1", directory_path: directoryPath, confirmed: true, ...(name !== undefined ? { name } : {}),
      }, signal);
      if (!hasExactKeys(value, ["protocol_version", "workspace",
        "directory_content_modified", "agent_authority_granted"]) ||
        value.protocol_version !== "workspace_import.v1" ||
        value.directory_content_modified !== false || value.agent_authority_granted !== false ||
        !hasExactKeys(value.workspace, ["id", "name", "created_at"]) ||
        typeof value.workspace.id !== "string" ||
        !/^[A-Za-z0-9_.-]{1,256}$/u.test(value.workspace.id) ||
        !boundedText(value.workspace.name, 128) ||
        /[\u0000-\u001f\u007f]/u.test(value.workspace.name) ||
        !validDate(value.workspace.created_at)) {
        throw new APIRequestError("Workspace import response is invalid", "INVALID_RESPONSE", 502);
      }
      return value as unknown as WorkspaceImportView;
    } catch (error) {
      // Transport and server messages may contain the submitted host path.
      // Keep bounded machine status for recovery while discarding raw details.
      if (signal?.aborted || (error instanceof DOMException && error.name === "AbortError")) {
        throw new DOMException("Workspace import was cancelled", "AbortError");
      }
      const code = error instanceof APIRequestError && /^[A-Z_]{1,64}$/u.test(error.code)
        ? error.code : "UNAVAILABLE";
      throw new APIRequestError("Unable to import the project. Check the directory and try again.",
        code, error instanceof APIRequestError ? error.status : 0);
    }
  }

  async uploadWorkspaceFile(workspaceID: string, file: File, operationKey: string,
    signal?: AbortSignal): Promise<WorkspaceFileAttachment> {
    if (!boundedIdentity(workspaceID) || file.size > maximumFileBytes) throw new Error("每个文件不能超过 5 MiB。");
    const bytes = new Uint8Array(await file.arrayBuffer());
    let binary = "";
    for (let start = 0; start < bytes.length; start += 8192) binary += String.fromCharCode(...bytes.subarray(start, start + 8192));
    const name = [...file.name.replace(/[\u0000-\u001f\u007f/\\]/gu, "_")].slice(0, 160).join("") || "附件";
    const result = await this.transport.postControl<{ attachment: unknown }>(
      `/workspaces/${encodeURIComponent(workspaceID)}/file-attachments`, {
        version: "workspace_file_upload.v1", name, mime_type: file.type || "application/octet-stream", data_base64: btoa(binary),
      }, operationKey, signal);
    const digest = [...new Uint8Array(await crypto.subtle.digest("SHA-256", bytes))]
      .map((byte) => byte.toString(16).padStart(2, "0")).join("");
    if (!validFileAttachment(result?.attachment) || result.attachment.workspace_id !== workspaceID ||
        result.attachment.sha256 !== digest || result.attachment.byte_size !== bytes.length || result.attachment.name !== name) {
      throw new APIRequestError("文件上传结果与原文件不一致，原请求标识已保留。", "INVALID_RESPONSE", 502);
    }
    return result.attachment;
  }

  async inspectWorkspaceFileUpload(workspaceID: string, operationKey: string, signal?: AbortSignal):
    Promise<{ state: "not_received" | "stored"; attachment?: WorkspaceFileAttachment }> {
    if (!boundedIdentity(workspaceID) || !boundedIdentity(operationKey)) throw new Error("文件上传请求标识无效。");
    const value = (await this.transport.request<unknown>(`/workspaces/${encodeURIComponent(workspaceID)}/file-attachments/request`,
      {}, signal, operationKey)).data;
    if (!isRecord(value) || !["not_received", "stored"].includes(String(value.state)) ||
        (value.state === "stored" && (!validFileAttachment(value.attachment) || value.attachment.workspace_id !== workspaceID)) ||
        (value.state === "not_received" && value.attachment !== undefined)) {
      throw new APIRequestError("文件上传状态无法核对，原请求已保留。", "INVALID_RESPONSE", 502);
    }
    return value as { state: "not_received" | "stored"; attachment?: WorkspaceFileAttachment };
  }

  async downloadWorkspaceFile(metadata: WorkspaceFileAttachment, signal?: AbortSignal): Promise<Blob> {
    if (!validFileAttachment(metadata)) throw new Error("文件引用无效，无法下载。");
    return this.downloadVerifiedAttachment(`/workspaces/${encodeURIComponent(metadata.workspace_id)}/file-attachments/` +
      `${encodeURIComponent(metadata.id)}/content`, { ...metadata, mime_type: "application/octet-stream" }, signal);
  }

  async uploadWorkspaceImage(workspaceID: string, file: File, operationKey: string,
    signal?: AbortSignal): Promise<WorkspaceImageAttachment> {
    if (!boundedIdentity(workspaceID) || !acceptedImageTypes.includes(file.type) ||
      file.size < 1 || file.size > maximumImageBytes) throw new Error("请选择不超过 5 MiB 的 PNG、JPEG 或 WebP 图片。");
    const bytes = new Uint8Array(await file.arrayBuffer());
    let binary = "";
    for (let start = 0; start < bytes.length; start += 8192) binary += String.fromCharCode(...bytes.subarray(start, start + 8192));
    const result = await this.transport.postControl<{ image: unknown }>(
      `/workspaces/${encodeURIComponent(workspaceID)}/image-attachments`, {
        version: "workspace_image_upload.v1", name: [...file.name.replace(/[\u0000-\u001f\u007f/\\]/gu, "_")].slice(0, 160).join(""), mime_type: file.type,
        data_base64: btoa(binary),
      }, operationKey, signal);
    const digest = [...new Uint8Array(await crypto.subtle.digest("SHA-256", bytes))]
      .map((byte) => byte.toString(16).padStart(2, "0")).join("");
    if (!validImageAttachment(result?.image) || result.image.workspace_id !== workspaceID ||
      result.image.sha256 !== digest || result.image.byte_size !== bytes.length || result.image.mime_type !== file.type) {
      throw new APIRequestError("图片上传结果与原图不一致，请重新选择。", "INVALID_RESPONSE", 502);
    }
    return result.image;
  }

  async downloadWorkspaceImage(metadata: WorkspaceImageAttachment, signal?: AbortSignal): Promise<Blob> {
    if (!validImageAttachment(metadata)) throw new Error("图片引用无效，无法预览。");
    return this.downloadVerifiedImage(`/workspaces/${encodeURIComponent(metadata.workspace_id)}/image-attachments/` +
      `${encodeURIComponent(metadata.id)}/content`, metadata, signal);
  }

  async downloadVerifiedImage(path: string, metadata: { mime_type: string; byte_size: number; sha256: string },
    signal?: AbortSignal): Promise<Blob> {
    if (!acceptedImageTypes.includes(metadata.mime_type) || !isSHA256(metadata.sha256) ||
      !Number.isSafeInteger(metadata.byte_size) || metadata.byte_size < 1 || metadata.byte_size > 20 * 1024 * 1024) {
      throw new Error("图片元信息无效。");
    }
    return this.downloadVerifiedAttachment(path, metadata, signal);
  }

  private async downloadVerifiedAttachment(path: string, metadata: { mime_type: string; byte_size: number; sha256: string },
    signal?: AbortSignal): Promise<Blob> {
    const response = await fetch(this.transport.url(path), { method: "GET", headers: { ...this.transport.headers(), Accept: metadata.mime_type },
      signal, cache: "no-store", credentials: "omit", referrerPolicy: "no-referrer" });
    if (!response.ok) throw await this.transport.responseError(response);
    if (response.headers.get("content-type") !== metadata.mime_type ||
      response.headers.get("etag") !== `"${metadata.sha256}"` ||
      response.headers.get("x-cyberagent-content-sha256") !== metadata.sha256 ||
      response.headers.get("content-length") !== String(metadata.byte_size) || (!response.body && metadata.byte_size !== 0)) {
      throw new APIRequestError("附件内容已变化或无法验证，请重新读取。", "INVALID_RESPONSE", 502);
    }
    const bytes = new Uint8Array(metadata.byte_size);
    const reader = response.body?.getReader();
    let offset = 0;
    try {
      while (reader) {
        const { done, value } = await reader.read();
        if (done) break;
        if (offset + value.byteLength > bytes.byteLength) { await reader.cancel(); throw new Error("附件大小超出已验证范围。"); }
        bytes.set(value, offset);
        offset += value.byteLength;
      }
    } finally { reader?.releaseLock(); }
    const sha256 = [...new Uint8Array(await crypto.subtle.digest("SHA-256", bytes))]
      .map((byte) => byte.toString(16).padStart(2, "0")).join("");
    if (offset !== bytes.length || sha256 !== metadata.sha256) throw new Error("附件内容验证失败，请重新读取。");
    return new Blob([bytes], { type: metadata.mime_type });
  }

  async workspaceExplore(workspaceID: string, path = ".",
    signal?: AbortSignal): Promise<WorkspaceExplorerView> {
    if (!boundedIdentity(workspaceID) || !validWorkspaceRelativePath(path)) {
      throw new Error("A normalized Workspace identity and Go-issued relative path are required");
    }
    return parseWorkspaceExplorer(await this.transport.get<unknown>(
      `/workspaces/${encodeURIComponent(workspaceID)}/explore`, { path }, signal,
    ), workspaceID, path);
  }

  async workspaceSearch(workspaceID: string, query: string,
    signal?: AbortSignal): Promise<WorkspaceSearchView> {
    if (!boundedIdentity(workspaceID) || !boundedText(query, 128) ||
      /[\u0000-\u001f\u007f]/u.test(query)) {
      throw new Error("A normalized Workspace identity and bounded query are required");
    }
    return parseWorkspaceSearch(await this.transport.get<unknown>(
      `/workspaces/${encodeURIComponent(workspaceID)}/search`, { query }, signal,
    ), workspaceID);
  }
}
