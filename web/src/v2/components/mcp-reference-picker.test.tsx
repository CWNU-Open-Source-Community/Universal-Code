import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { fireEvent, render as renderComponent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { APIClient } from "../../api/client";
import { mcpServer } from "../../test/extension-onboarding-fixtures";
import { V2Composer } from "./composer";
import { mcpReferenceIdentities, mcpTemplateVariables, validMCPReferences } from "../../api/mcp-references";
import { validDraftSnapshot } from "../draft-document";
import type { ComponentProps } from "react";

function fixture(onSubmit = vi.fn<ComponentProps<typeof V2Composer>["onSubmit"]>(async () => undefined)) {
  const server = { ...mcpServer("enabled"), approved_capability_fingerprint: "b".repeat(64),
    capabilities: { negotiated: ["resources", "prompts"], tools: [], fingerprint: "b".repeat(64),
      resources: ["fixture://project/notes"], resource_templates: ["fixture://tickets/{id}{?language}"], prompts: ["review", "prototype"],
      prompt_details: [{ name: "review", arguments: [{ name: "code", description: "需要审阅的代码，可包含空格", required: true },
        { name: "language", required: false }] }, { name: "prototype", arguments: [{ name: "__proto__", required: true }] }] } };
  const extensionInventory = vi.fn(async () => ({ mcp_servers: [server, { ...server, id: "disabled", state: "disabled" }] }));
  const client = { hasThreadControl: true, hasExtensionControl: true, extensionInventory } as unknown as APIClient;
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <V2Composer client={client} threadID="" runID="run-one" workspaceID="project-one" workspaces={[]}
      onWorkspaceChange={() => undefined} onSubmit={onSubmit} />
  </QueryClientProvider>);
  return { onSubmit, extensionInventory };
}

it("selects a Prompt with labeled required arguments and sends its typed reference without reading it in the browser", async () => {
  const user = userEvent.setup(); const { onSubmit, extensionInventory } = fixture();
  await user.click(screen.getByRole("button", { name: "添加附件" }));
  await user.click(screen.getByRole("menuitem", { name: "使用 MCP Prompt" }));
  await user.click(await screen.findByRole("button", { name: "First MCP · Prompt · review" }));
  expect(screen.getByText("需要审阅的代码，可包含空格")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "添加到消息" })).toBeDisabled();
  await user.type(screen.getByRole("textbox", { name: /code（必填）/u }), "const answer = 42");
  await user.click(screen.getByRole("button", { name: "添加到消息" }));
  expect(screen.getByRole("button", { name: "发送消息" })).toBeEnabled();
  expect(onSubmit).not.toHaveBeenCalled();
  expect(extensionInventory).toHaveBeenCalledWith("run-one", expect.any(AbortSignal), "project-one");
  await user.click(screen.getByRole("button", { name: "发送消息" }));
  const selected = onSubmit.mock.calls[0]?.[5];
  expect(mcpReferenceIdentities(selected)).toEqual([{ server_id: "first-mcp", capability_fingerprint: "b".repeat(64),
    method: "prompts/get", arguments: { name: "review", arguments: { code: "const answer = 42" } } }]);
  expect(screen.queryByLabelText("待发送的 MCP 引用")).not.toBeInTheDocument();
});

it("keeps resource templates literal, omits optional blank variables, and preserves selection when sending fails", async () => {
  const user = userEvent.setup(); const onSubmit = vi.fn().mockRejectedValue(new Error("暂时无法发送")); fixture(onSubmit);
  await user.click(screen.getByRole("button", { name: "添加附件" }));
  await user.click(screen.getByRole("menuitem", { name: "引用 MCP 资源" }));
  await user.type(await screen.findByRole("searchbox"), "tickets");
  await user.click(screen.getByRole("button", { name: /First MCP · 资源模板/u }));
  await user.type(screen.getByRole("textbox", { name: "id（可选）" }), "a/b");
  await user.click(screen.getByRole("button", { name: "添加到消息" }));
  await user.click(screen.getByRole("button", { name: "发送消息" }));
  await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("暂时无法发送"));
  expect(onSubmit.mock.calls[0]?.[5]?.[0]?.reference.arguments).toEqual({ uri_template: "fixture://tickets/{id}{?language}", variables: { id: "a/b" } });
  expect(screen.getByLabelText("待发送的 MCP 引用")).toHaveTextContent("fixture://tickets/{id}{?language}");
});

it("treats prototype-named arguments as ordinary strings and restores focus on Escape", async () => {
  const user = userEvent.setup(); fixture();
  const trigger = screen.getByRole("button", { name: "添加附件" });
  await user.click(trigger); await user.click(screen.getByRole("menuitem", { name: "使用 MCP Prompt" }));
  await user.click(await screen.findByRole("button", { name: "First MCP · Prompt · prototype" }));
  expect(screen.getByRole("textbox", { name: "__proto__（必填）" })).toHaveValue("");
  expect(screen.getByRole("button", { name: "添加到消息" })).toBeDisabled();
  fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  await waitFor(() => expect(trigger).toHaveFocus());
});

it("retains typed MCP selections in the validated draft and rejects a forged execution method", () => {
  const selected = [{ id: "reference-one", label: "fixture://notes", serverName: "Notes",
    reference: { server_id: "notes", capability_fingerprint: "a".repeat(64), method: "resources/read", arguments: { uri: "fixture://notes" } } }];
  expect(validMCPReferences(selected)).toBe(true);
  expect(validDraftSnapshot({ text: "", files: [], images: [], mcpReferences: selected }, "project-one")).toBe(true);
  expect(validMCPReferences([{ ...selected[0], reference: { ...selected[0].reference, method: "tools/call" } }])).toBe(false);
  expect(mcpTemplateVariables("fixture://{+path}{?language,fields*}{/path:3}")).toEqual(["path", "language", "fields"]);
});

function render(ui: ReactElement, options?: RenderOptions) {
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  return renderComponent(ui, { wrapper: LocaleProvider, ...options });
}
