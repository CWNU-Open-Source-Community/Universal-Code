const environmentName = /^[A-Za-z_][A-Za-z0-9_]{0,127}$/u;
const headerName = /^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,128}$/u;
const credentialName = /^[\p{L}\p{Nd}_-]+$/u;
const secretName = /authorization|cookie|token|secret|password|passwd|api[-_]?key|private[-_]?key|credential/iu;
const controlledHeader = /^(host|content-length|transfer-encoding|connection|trailer|te|upgrade|proxy-authorization|accept|content-type|mcp-protocol-version|mcp-session-id|mcp-method|mcp-name|last-event-id)$/iu;
const bytes = (value: string) => new TextEncoder().encode(value).length;
const literal = (value: unknown): value is string => typeof value === "string" && bytes(value) <= 4096 &&
  !/[\p{Cc}]/u.test(value) && !/^\s*bearer\s/iu.test(value) && !value.startsWith("uc-mcp-oauth-v1:") && !/\b(sk-|gh[pousr]_|github_pat_)[A-Za-z0-9_-]{20,}/u.test(value);

export function validManualMCPURL(value: string): boolean {
  if (value !== value.trim() || bytes(value) > 4096 || /[?#\\\p{Cc}]/u.test(value)) return false;
  try {
    const url = new URL(value);
    const host = url.hostname;
    const loopback = host === "localhost" || /^127(?:\.\d{1,3}){3}$/u.test(host) || host === "[::1]";
    return (url.protocol === "https:" || url.protocol === "http:" && loopback) &&
      !!host && !url.username && !url.password && !url.search && !url.hash;
  } catch { return false; }
}

export function absoluteMCPPath(value: string): boolean {
  return value.length > 0 && bytes(value) <= 4096 && !/[\p{Cc}]/u.test(value) &&
    (/^[A-Za-z]:[\\/]/u.test(value) || value.startsWith("/") || value.startsWith("\\\\"));
}

type ConnectionFields = { transport: string; cwd?: string; env?: Record<string, string>;
  env_credential_refs?: Record<string, string>; headers?: Record<string, string> };

export function validMCPConnectionConfig(value: ConnectionFields): boolean {
  if (value.cwd !== undefined && typeof value.cwd !== "string") return false;
  if (value.transport === "streamable_http" && (value.cwd || value.env && Object.keys(value.env).length ||
    value.env_credential_refs && Object.keys(value.env_credential_refs).length)) return false;
  if (value.transport === "stdio" && (value.headers && Object.keys(value.headers).length || value.cwd && !absoluteMCPPath(value.cwd))) return false;
  const names = new Set<string>();
  const maps = [[value.env, "env"], [value.env_credential_refs, "refs"], [value.headers, "headers"]] as const;
  let envCount = 0;
  for (const [map, kind] of maps) {
    if (map === undefined) continue;
    if (!map || typeof map !== "object" || Array.isArray(map) || Object.keys(map).length > 32) return false;
    if (kind !== "headers") envCount += Object.keys(map).length;
    const headerNames = new Set<string>();
    for (const [name, item] of Object.entries(map)) {
      if (kind === "headers") {
        const normalized = name.toLowerCase();
        if (!headerName.test(name) || headerNames.has(normalized) || secretName.test(name) || controlledHeader.test(name) || !literal(item)) return false;
        headerNames.add(normalized);
      } else {
        const normalized = name.toUpperCase();
        if (!environmentName.test(name) || names.has(normalized)) return false;
        names.add(normalized);
        if (kind === "refs" ? typeof item !== "string" || !credentialName.test(item) || bytes(item) > 64 : secretName.test(name) || !literal(item)) return false;
      }
    }
  }
  return envCount <= 32;
}

// Errors describe the row shape, never echo a possibly sensitive input value.
export function parseMCPConfigLines(value: string, separator: "=" | ":", message: string): Record<string, string> {
  const entries: Record<string, string> = Object.create(null) as Record<string, string>;
  const seen = new Set<string>();
  for (const line of value.split(/\r?\n/u)) {
    if (!line.trim()) continue;
    const index = line.indexOf(separator);
    const name = line.slice(0, index).trim();
    const normalized = name.toUpperCase();
    if (index < 1 || !name || seen.has(normalized)) throw new Error(message);
    seen.add(normalized);
    entries[name] = separator === ":" ? line.slice(index + 1).trim() : line.slice(index + 1);
  }
  return entries;
}
