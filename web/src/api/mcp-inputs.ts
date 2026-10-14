import type { APIClient } from "./client";
export interface MCPInputField { name: string; title: string; description?: string; type: "string" | "boolean" | "integer" | "number" | "array"; required: boolean; enum?: string[]; enum_titles?: string[]; default?: unknown }
export interface MCPInput { id: string; run_id: string; server_id: string; server_name: string; method: string; message: string; mode: "form" | "url"; url?: string; host?: string; fields: MCPInputField[] | null; expires_at: string }
export async function listMCPInputs(client: APIClient, runID: string, signal?: AbortSignal): Promise<MCPInput[]> {
  const value = await client.get<{ version: string; items: MCPInput[] }>(`/runs/${encodeURIComponent(runID)}/mcp-inputs`, {}, signal);
  if (value.version !== "mcp-input.v1" || !Array.isArray(value.items) || value.items.length > 64) throw new Error("Invalid MCP inputs");
  for (const item of value.items) {
    if (item.run_id !== runID || typeof item.id !== "string" || !["form", "url"].includes(item.mode) || !Number.isFinite(Date.parse(item.expires_at))) throw new Error("Invalid MCP input binding");
    if (item.mode === "url") {
      const url = new URL(item.url ?? "");
      const declaredHost = new URL(`https://${item.host}`);
      if (url.protocol !== "https:" || url.username || url.password || declaredHost.username || declaredHost.password ||
        declaredHost.pathname !== "/" || declaredHost.search || declaredHost.hash || url.host !== declaredHost.host) throw new Error("Invalid MCP external authorization URL");
    } else if (!Array.isArray(item.fields) || item.fields.length > 32 || item.fields.some((field) => !["string", "boolean", "number", "integer", "array"].includes(field.type))) throw new Error("Invalid MCP form");
  }
  return value.items.filter((item) => Date.parse(item.expires_at) > Date.now());
}
export async function respondMCPInput(client: APIClient, input: MCPInput, action: "accept" | "decline" | "cancel", content?: Record<string, unknown>): Promise<void> {
  const result = await client.respondMCPInput(input.run_id, input.id, { version: "mcp-input.v1", action, ...(content ? { content } : {}) });
  if (result.accepted !== true) throw new Error("MCP input response was not accepted");
}
