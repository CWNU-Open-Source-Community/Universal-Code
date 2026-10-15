import { expect, it } from "vitest";
import { absoluteMCPPath, parseMCPConfigLines, validManualMCPURL, validMCPConnectionConfig } from "./mcp-config";

it("accepts fixed local HTTP and rejects query, credentials and public plaintext endpoints", () => {
  for (const url of ["http://127.0.0.1:8099/mcp", "http://localhost:8099/mcp", "http://[::1]:8099/mcp", "https://remote.invalid/mcp"]) expect(validManualMCPURL(url)).toBe(true);
  for (const url of ["http://remote.invalid/mcp", "https://name:secret@remote.invalid/mcp", "http://127.0.0.1:8099/mcp?", "https://remote.invalid/mcp#", "file:///tmp/mcp"]) expect(validManualMCPURL(url)).toBe(false);
  expect(absoluteMCPPath("C:\\tools\\server.exe")).toBe(true);
  expect(absoluteMCPPath("./server")).toBe(false);
});

it("keeps environment names and fixed headers bounded while secrets use references", () => {
  expect(validMCPConnectionConfig({ transport: "stdio", cwd: "/project", env: { NODE_ENV: "production" }, env_credential_refs: { API_KEY: "mcp-fixture" } })).toBe(true);
  expect(validMCPConnectionConfig({ transport: "stdio", env: { API_KEY: "synthetic-secret" } })).toBe(false);
  expect(validMCPConnectionConfig({ transport: "stdio", env: { NODE_ENV: "uc-mcp-oauth-v1:synthetic-envelope" } })).toBe(false);
  expect(validMCPConnectionConfig({ transport: "stdio", env: { NODE_ENV: "value" }, env_credential_refs: { node_env: "mcp-fixture" } })).toBe(false);
  expect(validMCPConnectionConfig({ transport: "streamable_http", headers: { "X-Project": "fixture" } })).toBe(true);
  for (const name of ["Authorization", "Cookie", "X-API-Key", "Mcp-Session-Id", "Host"]) expect(validMCPConnectionConfig({ transport: "streamable_http", headers: { [name]: "value" } })).toBe(false);
  expect(validMCPConnectionConfig({ transport: "streamable_http", headers: { "X-Project": "one", "x-project": "two" } })).toBe(false);
  expect(parseMCPConfigLines("NODE_ENV=production\nVALUE=has=equals", "=", "shape error")).toEqual({ NODE_ENV: "production", VALUE: "has=equals" });
  expect(() => parseMCPConfigLines("bad-row-synthetic-secret", "=", "shape error")).toThrow("shape error");
});
