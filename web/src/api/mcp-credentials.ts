import type { MCPCredentialBindingView, MCPCredentialStatusView } from "./types";
import { absoluteMCPPath, validManualMCPURL } from "./mcp-config";

const sha256 = /^[a-f0-9]{64}$/u;
const name = /^[\p{L}\p{Nd}_-]+$/u;
const identity = (value: unknown): value is string => typeof value === "string" && value.length > 0 &&
  value.length <= 256 && value === value.trim() && !/[\s\p{Cc}]/u.test(value);
const credentialName = (value: unknown): value is string => typeof value === "string" && name.test(value) &&
  new TextEncoder().encode(value).length <= 64;

export function validMCPCredentialBinding(value: MCPCredentialBindingView): boolean {
  if (!identity(value.server_id) || !identity(value.workspace_id) || !sha256.test(value.expected_descriptor_fingerprint) ||
    !credentialName(value.credential_ref) || (value.run_id !== undefined && !identity(value.run_id)) ||
    value.target !== value.target.trim() || value.target.length > 4096) return false;
  return validManualMCPURL(value.target) || absoluteMCPPath(value.target);
}

export function validMCPBearerSecret(secret: string): boolean {
  const bytes = new TextEncoder().encode(secret).length;
  return bytes >= 8 && bytes <= 2560 && !/[\s\p{Cc}]/u.test(secret);
}

export function parseMCPCredentialStatus(value: unknown, binding: MCPCredentialBindingView): MCPCredentialStatusView {
  const keys = ["protocol_version", "server_id", "workspace_id", "descriptor_fingerprint", "target", "credential_ref",
    "configured", "store_kind", "store_available", "plaintext_returned", "registration_count", "reference_fingerprint", "endpoint_conflict"];
  const record = value as Record<string, unknown> | null;
  if (binding.run_id) keys.push("run_id");
  if (!record || typeof record !== "object" || Array.isArray(record) || Object.keys(record).length !== keys.length ||
    keys.some((key) => !Object.prototype.hasOwnProperty.call(record, key)) ||
    record.protocol_version !== "mcp-credential.v1" || record.server_id !== binding.server_id ||
    record.workspace_id !== binding.workspace_id || (record.run_id ?? "") !== (binding.run_id ?? "") ||
    record.descriptor_fingerprint !== binding.expected_descriptor_fingerprint || record.target !== binding.target ||
    record.credential_ref !== binding.credential_ref || typeof record.configured !== "boolean" ||
    typeof record.store_available !== "boolean" || typeof record.endpoint_conflict !== "boolean" || record.plaintext_returned !== false ||
    !identity(record.store_kind) || !Number.isSafeInteger(record.registration_count) || Number(record.registration_count) < 1 ||
    typeof record.reference_fingerprint !== "string" || !sha256.test(record.reference_fingerprint) ||
    (!record.store_available && record.configured)) {
    throw new Error("MCP credential status violated its metadata or binding contract");
  }
  return value as MCPCredentialStatusView;
}
