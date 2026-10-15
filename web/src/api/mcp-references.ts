export interface MCPReference {
  server_id: string;
  capability_fingerprint: string;
  method: "resources/read" | "prompts/get";
  arguments: { uri: string } | { uri_template: string; variables: Record<string, string> } |
    { name: string; arguments: Record<string, string> };
}

export interface MCPDraftReference {
  id: string;
  label: string;
  serverName: string;
  reference: MCPReference;
}

export const maximumMCPReferences = 4;
const record = (value: unknown): value is Record<string, unknown> =>
  !!value && typeof value === "object" && !Array.isArray(value);
const text = (value: unknown, max: number): value is string => typeof value === "string" &&
  value.length > 0 && value.length <= max && !/[\u0000-\u001f\u007f]/u.test(value);
const keys = (value: Record<string, unknown>, expected: string[]) =>
  Object.keys(value).length === expected.length && expected.every((key) => Object.hasOwn(value, key));
const variables = (value: unknown): value is Record<string, string> => record(value) &&
  Object.keys(value).length <= 64 && Object.entries(value).every(([name, value]) =>
    text(name, 256) && typeof value === "string" && value.length <= 16384);

export function validMCPReferences(value: unknown): value is MCPDraftReference[] {
  return Array.isArray(value) && value.length <= maximumMCPReferences && value.every((item) => {
    if (!record(item) || !keys(item, ["id", "label", "serverName", "reference"]) ||
      !text(item.id, 256) || !text(item.label, 4096) || !text(item.serverName, 256) || !record(item.reference)) return false;
    const ref = item.reference;
    if (!keys(ref, ["server_id", "capability_fingerprint", "method", "arguments"]) ||
      !text(ref.server_id, 256) || typeof ref.capability_fingerprint !== "string" ||
      !/^[a-f0-9]{64}$/u.test(ref.capability_fingerprint) || !record(ref.arguments)) return false;
    const args = ref.arguments;
    return ref.method === "resources/read" && (
      keys(args, ["uri"]) && text(args.uri, 4096) ||
      keys(args, ["uri_template", "variables"]) && text(args.uri_template, 4096) && variables(args.variables)) ||
      ref.method === "prompts/get" && keys(args, ["name", "arguments"]) && text(args.name, 256) && variables(args.arguments);
  }) && new Set(value.map(({ id }) => id)).size === value.length;
}

export const mcpReferenceIdentities = (items: readonly MCPDraftReference[] = []) => items.map(({ reference }) => reference);

// RFC 6570 varspecs may have a prefix length or explode suffix. Variable values
// remain literal strings; the Go MCP gateway owns URI expansion and dispatch.
export function mcpTemplateVariables(template: string): string[] {
  const names = new Set<string>();
  for (const expression of template.matchAll(/\{([^{}]+)\}/gu)) {
    for (const spec of expression[1].replace(/^[+#./;?&]/u, "").split(",")) {
      const name = spec.replace(/(?::\d+|\*)$/u, "");
      if (name) names.add(name);
    }
  }
  return [...names];
}
