import { Buffer } from "node:buffer";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { afterEach, expect, it, vi } from "vitest";
import { APIClient } from "./client";
import { safeMCPContentLink, validMCPContents } from "./mcp-content";

afterEach(() => vi.unstubAllGlobals());
const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6BYIAAAAASUVORK5CYII=", "base64");
const image = { kind: "image", mime_type: "image/png", data_base64: png.toString("base64"), byte_size: png.length, width: 1, height: 1 };

function response(contents?: unknown) {
  return { version: "api.v1", request_id: "media-fixture", data: { version: "thread_activity_detail.v2", activity_ref: "call-media", run_id: "run-media",
    tools: [{ name: "mcp_tool_call", label: "MCP", agent_id: "agent-media", agent_role: "root", agent_label: "Root Agent", status: "completed",
      started_at: "2026-10-14T16:00:00Z", duration_milliseconds: 12,
      detail: { kind: "mcp", mcp: { operation: "call", server: "media-server", tool: "resources.read", arguments: [],
        result: { type: "object", count: 0, summary: "Result", fields: [] },
        ...(contents === undefined ? {} : { contents }), boundary: { authorization: "policy_checked", error_code: "", failure_reason: "", truncated: false, untrusted: true } } } }] } };
}

it("reads the Go-projected media response and preserves older history without contents", async () => {
  const directory = process.env.UC_MCP_CONTENT_EVIDENCE;
  const saved = directory ? JSON.parse(readFileSync(join(directory, "media-fixture.json"), "utf8")) : response([image]);
  vi.stubGlobal("fetch", vi.fn().mockResolvedValueOnce(new Response(JSON.stringify(saved), { status: 200 }))
    .mockResolvedValueOnce(new Response(JSON.stringify(response()), { status: 200 })));
  const client = new APIClient("read");
  const actual = await client.threadActivityDetail("thread-media", "call-media");
  const detail = actual.tools[0].detail;
  if (detail.kind !== "mcp") throw new Error("MCP detail missing");
  expect(detail.mcp.contents).toEqual(saved.data.tools[0].detail.mcp.contents);
  const legacy = (await client.threadActivityDetail("thread-media", "call-media")).tools[0].detail;
  if (legacy.kind !== "mcp") throw new Error("Legacy MCP detail missing");
  expect(legacy.mcp.contents).toBeUndefined();
});

it("accepts on-demand MCP catalog search as typed MCP activity", async () => {
  const saved = response();
  saved.data.tools[0].name = "mcp_tool_search";
  saved.data.tools[0].detail.mcp.operation = "search";
  saved.data.tools[0].detail.mcp.tool = "mcp_tool_search";
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(saved), { status: 200 })));
  const actual = await new APIClient("read").threadActivityDetail("thread-media", "call-media");
  expect(actual.tools[0].name).toBe("mcp_tool_search");
  expect(actual.tools[0].detail.kind).toBe("mcp");
});

it.each([
  ["non-string kind", [{ ...image, kind: { toString: null } }]],
  ["SVG MIME", [{ ...image, mime_type: "image/svg+xml" }]],
  ["wrong MIME", [{ ...image, mime_type: "image/jpeg" }]],
  ["byte count", [{ ...image, byte_size: 1 }]],
  ["base64 whitespace", [{ ...image, data_base64: `${image.data_base64}\n` }]],
  ["oversized dimensions", [{ ...image, width: 9000 }]],
  ["raw authority fields", [{ ...image, credential_ref: "hidden" }]],
  ["oversized collection", Array.from({ length: 17 }, () => ({ kind: "text", text: "value" }))],
  ["control text", [{ kind: "text", text: "visible\u0000hidden" }]],
])("rejects invalid MCP content: %s", async (_name, contents) => {
  expect(validMCPContents(contents)).toBe(false);
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(response(contents)), { status: 200 })));
  await expect(new APIClient("read").threadActivityDetail("thread-media", "call-media")).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
});

it("keeps opaque resource URIs as text and opens only safe HTTP links", () => {
  expect(validMCPContents([{ kind: "resource_text", text: "Text\n<script>literal</script>", uri: "notes://guide", mime_type: "text/html" }])).toBe(true);
  for (const uri of ["https://docs.example.com/guide", "http://127.0.0.1:18000/guide"]) expect(safeMCPContentLink(uri)).toBe(true);
  for (const uri of ["notes://guide", "file:///private", "javascript:alert(1)", "data:text/html,unsafe", "https://user:password@example.com", "https://example.com/#fragment", "https:\\example.com"]) {
    expect(safeMCPContentLink(uri)).toBe(false);
  }
});
