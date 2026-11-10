import type { APIClient } from "./client";
import { boundedIdentity, isRecord } from "./client/validation";

export type BrowserNetworkAccess = "off" | "internet" | "restricted";
export type BrowserNetworkTarget = { kind: "runs" | "workspaces"; id: string };

export interface BrowserNetworkPreference {
  workspace_id: string;
  revision: number;
  browser_network_access: BrowserNetworkAccess;
  browser_origins?: string[];
}

export interface BrowserNetworkView extends BrowserNetworkPreference {
  version: "browser_network_access.v1";
  run_id?: string;
  project_preference?: BrowserNetworkPreference;
  replayed?: boolean;
}

export interface BrowserNetworkRequest {
  version: "browser_network_access.v1";
  expected_revision: number;
  browser_network_access: BrowserNetworkAccess;
  browser_origins: string[];
  remember_for_project?: boolean;
}

export const browserNetworkQueryKey = (baseURL: string, target: BrowserNetworkTarget) =>
  ["browser-network", baseURL, target.kind, target.id] as const;

function path(target: BrowserNetworkTarget): string {
  if (!boundedIdentity(target.id)) throw new Error("Browser network target is invalid");
  return `/${target.kind}/${encodeURIComponent(target.id)}/browser-network`;
}

function validPreference(value: unknown): value is BrowserNetworkPreference {
  return isRecord(value) && Boolean(boundedIdentity(value.workspace_id)) &&
    Number.isSafeInteger(value.revision) && Number(value.revision) >= 0 &&
    ["off", "internet", "restricted"].includes(String(value.browser_network_access)) &&
    (value.browser_origins === undefined || Array.isArray(value.browser_origins) &&
      value.browser_origins.every((origin) => typeof origin === "string" && origin.length > 0));
}

export function parseBrowserNetwork(value: unknown, target: BrowserNetworkTarget): BrowserNetworkView {
  if (!validPreference(value) || !isRecord(value) || value.version !== "browser_network_access.v1" ||
    (target.kind === "runs" ? value.run_id !== target.id : value.workspace_id !== target.id) ||
    (value.replayed !== undefined && typeof value.replayed !== "boolean") ||
    (value.project_preference !== undefined && (!validPreference(value.project_preference) ||
      value.project_preference.workspace_id !== value.workspace_id))) {
    throw new Error("Browser network response is invalid");
  }
  return value as unknown as BrowserNetworkView;
}

export async function readBrowserNetwork(client: Pick<APIClient, "get">, target: BrowserNetworkTarget, signal?: AbortSignal) {
  return parseBrowserNetwork(await client.get<unknown>(path(target), {}, signal), target);
}

export async function saveBrowserNetwork(client: Pick<APIClient, "putControl">, target: BrowserNetworkTarget,
  request: BrowserNetworkRequest, operationKey: string) {
  const result = parseBrowserNetwork(await client.putControl<unknown>(path(target), request, operationKey), target);
  if (result.browser_network_access !== request.browser_network_access || result.revision < request.expected_revision ||
    typeof result.replayed !== "boolean") throw new Error("Browser network update response is invalid");
  return result;
}
