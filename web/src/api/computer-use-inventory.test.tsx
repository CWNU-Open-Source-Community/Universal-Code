import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { APIClient } from "./client";
import { ExtensionSettings } from "../components/shared-settings-panels";
import { LocaleProvider } from "../lib/locale";
import { mcpServer } from "../test/extension-onboarding-fixtures";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); window.localStorage.clear(); });

function builtin() {
  return { ...mcpServer("enabled"), id: "windows-computer-use", name: "Windows Computer Use",
    transport: "stdio", target: "builtin:windows-computer-use",
    source: { kind: "builtin", uri: "universal-code:computer-use", version: "1" } };
}

function respond(data: unknown) {
  return new Response(JSON.stringify({ version: "api.v1", request_id: "builtin-computer-use", data }),
    { status: 200, headers: { "Content-Type": "application/json" } });
}

it("labels the host-provided MCP, omits connection credentials, and retains pinned disable", async () => {
  let server = builtin();
  const fetch = vi.fn(async (_url: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      server = { ...server, state: "disabled", generation: server.generation + 1 };
      return respond(server);
    }
    return respond({ protocol_version: "extension-inventory.v1", workspace_id: "project-one",
      mcp_servers: [server], mcp_calls: [], plugins: [],
      onboarding: { mcp_registration: true, plugin_import: true, lsp_configuration: true, mcp_credentials: true } });
  });
  vi.stubGlobal("fetch", fetch);
  const client = new APIClient("read", "/api/v1", "control", { extensionControlEnabled: true });
  vi.spyOn(client, "codeIntelInventory").mockResolvedValue({ protocol_version: "code-intel-lsp.v1",
    enabled: true, qualifications: [], servers: [], configurations: [] });
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: {
    queries: { retry: false }, mutations: { retry: false },
  } })}><LocaleProvider><ExtensionSettings client={client} selectedRunID="" selectedWorkspaceID="project-one" /></LocaleProvider></QueryClientProvider>);
  const name = await screen.findByText("Windows Computer Use");
  const article = name.closest("article");
  expect(article).not.toBeNull();
  const card = within(article!);
  expect(card.getByText("内置 · 随宿主提供")).toBeVisible();
  expect(card.getByText("版本").nextElementSibling).toHaveTextContent("1");
  expect(card.getByText(/随宿主自动登记/)).toBeVisible();
  expect(card.queryByText("凭据引用")).not.toBeInTheDocument();
  expect(card.queryByText("核对登记")).not.toBeInTheDocument();
  expect(card.queryByRole("textbox")).not.toBeInTheDocument();
  expect(card.getByRole("button", { name: "立即关闭" })).toBeEnabled();
  await userEvent.click(card.getByRole("button", { name: "立即关闭" }));
  await waitFor(() => expect(fetch.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1));
  const post = fetch.mock.calls.find(([, init]) => init?.method === "POST")!;
  expect(post[0]).toBe("/api/v1/extensions/mcp/windows-computer-use/review");
  expect(JSON.parse(String(post[1]?.body))).toEqual({ version: "extension-control.v1", action: "disable",
    expected_descriptor_fingerprint: server.descriptor_fingerprint });
});

it.each([
  { source: { kind: "manual", uri: "universal-code:computer-use", version: "1" } },
  { source: { kind: "builtin", uri: "universal-code:other", version: "1" } },
  { source: { kind: "builtin", uri: "universal-code:computer-use", version: "2" } },
  { target: "builtin:other" },
  { transport: "streamable_http" },
  { credential_ref: "external-credential" },
  { env_credential_refs: { API_KEY: "external-credential" } },
])("rejects a builtin descriptor with an inconsistent connection: %j", async (change) => {
  vi.stubGlobal("fetch", vi.fn(async () => respond({ protocol_version: "extension-inventory.v1",
    mcp_servers: [{ ...builtin(), ...change }], mcp_calls: [], plugins: [] })));
  await expect(new APIClient("read").extensionInventory()).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
});
