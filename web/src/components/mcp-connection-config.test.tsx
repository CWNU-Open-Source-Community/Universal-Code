import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { APIClient } from "../api/client";
import { LocaleProvider } from "../lib/locale";
import { mcpServer } from "../test/extension-onboarding-fixtures";
import { credentialServer, credentialStatus } from "../test/mcp-credential-fixtures";
import { MCPRegistrationForm } from "./extension-onboarding";
import { MCPEnvironmentCredentialControls } from "./mcp-environment-credentials";

afterEach(() => vi.restoreAllMocks());
function mount(children: React.ReactNode) {
  window.localStorage.setItem("prayu.locale.v1", "en-US");
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const view = render(<QueryClientProvider client={queries}><LocaleProvider>{children}</LocaleProvider></QueryClientProvider>);
  return { ...view, queries };
}

it("submits stdio cwd, explicit environment and credential names while retaining failed input", async () => {
  const user = userEvent.setup();
  const client = new APIClient("read", "/api/v1", "control");
  const register = vi.spyOn(client, "registerMCPServer").mockRejectedValueOnce(new Error("Server registration unavailable"))
    .mockResolvedValue({ protocol_version: "extension-onboarding.v1", next_step: "approve_discovery", replayed: false, server: mcpServer() });
  mount(<MCPRegistrationForm client={client} enabled workspaceID="project-one" runID="" />);
  await user.click(screen.getByText("Register MCP server"));
  await user.type(screen.getByLabelText("Server ID"), "manual-config");
  await user.type(screen.getByLabelText("Display name"), "Manual config");
  await user.selectOptions(screen.getByLabelText("Transport"), "stdio");
  await user.type(screen.getByLabelText("Absolute executable path"), "C:\\tools\\server.exe");
  await user.type(screen.getByLabelText("Working directory (existing absolute path, optional)"), "C:\\project");
  await user.type(screen.getByLabelText("Non-sensitive environment (NAME=value per line)"), "NODE_ENV=production");
  await user.type(screen.getByLabelText("Environment credential references (NAME=credential-name per line)"), "API_KEY=mcp-fixture");
  await user.click(screen.getByRole("button", { name: "Register descriptor" }));
  await screen.findByRole("alert");
  expect(screen.getByLabelText("Working directory (existing absolute path, optional)")).toHaveValue("C:\\project");
  expect(screen.getByLabelText("Environment credential references (NAME=credential-name per line)")).toHaveValue("API_KEY=mcp-fixture");
  await user.click(screen.getByRole("button", { name: "Register descriptor" }));
  await screen.findByText(/Registration returned state/u);
  expect(register.mock.calls[0][0].descriptor).toMatchObject({ protocol_version: "mcp-client.v3", transport: "stdio", cwd: "C:\\project",
    env: { NODE_ENV: "production" }, env_credential_refs: { API_KEY: "mcp-fixture" } });
  expect(register.mock.calls[0][0]).toEqual(register.mock.calls[1][0]);
});

it("reports duplicate header rows before registration and submits local HTTP with fixed headers", async () => {
  const user = userEvent.setup();
  const client = new APIClient("read", "/api/v1", "control");
  const register = vi.spyOn(client, "registerMCPServer").mockResolvedValue({ protocol_version: "extension-onboarding.v1", next_step: "approve_discovery", replayed: false, server: mcpServer() });
  mount(<MCPRegistrationForm client={client} enabled workspaceID="project-one" runID="" />);
  await user.click(screen.getByText("Register MCP server"));
  await user.type(screen.getByLabelText("Server ID"), "manual-http");
  await user.type(screen.getByLabelText("Display name"), "Manual HTTP");
  await user.type(screen.getByLabelText("Server URL (HTTPS / local HTTP)"), "http://127.0.0.1:8099/mcp");
  const headers = screen.getByLabelText("Fixed non-sensitive HTTP headers (Name: value per line)");
  await user.type(headers, "X-Project: one\nx-project: two");
  await user.click(screen.getByRole("button", { name: "Register descriptor" }));
  await screen.findByRole("alert");
  expect(register).not.toHaveBeenCalled();
  expect(headers).toHaveValue("X-Project: one\nx-project: two");
  await user.clear(headers); await user.type(headers, "X-Project: fixture");
  await user.click(screen.getByRole("button", { name: "Register descriptor" }));
  await waitFor(() => expect(register).toHaveBeenCalledTimes(1));
  expect(register.mock.calls[0][0].descriptor).toMatchObject({ protocol_version: "mcp-client.v3", target: "http://127.0.0.1:8099/mcp", headers: { "X-Project": "fixture" } });
});

it("stores environment secrets only in request-local input and blocks readonly changes", async () => {
  const user = userEvent.setup();
  const client = new APIClient("read", "/api/v1", "control");
  const server = { ...credentialServer(), transport: "stdio", target: "C:\\tools\\server.exe", env_credential_refs: { API_KEY: "mcp-fixture" } };
  vi.spyOn(client, "mcpCredentialStatus").mockResolvedValue(credentialStatus(server));
  const change = vi.spyOn(client, "changeMCPCredential").mockResolvedValue(credentialStatus(server, true));
  const view = mount(<MCPEnvironmentCredentialControls client={client} server={server} capability />);
  await screen.findByText("No local environment credential stored");
  await user.click(screen.getByText("Manage environment credential: mcp-fixture"));
  await user.type(screen.getByLabelText("Environment secret (8–2560 bytes, no whitespace)"), "synthetic-environment-value");
  expect(screen.getByRole("button", { name: "Save environment credential" })).toBeDisabled();
  await user.click(screen.getByRole("checkbox", { name: "I confirm changing this system credential name and all its references." }));
  await user.click(screen.getByRole("button", { name: "Save environment credential" }));
  await screen.findByText("Environment credential saved and presence verified.");
  expect(screen.getByLabelText("Environment secret (8–2560 bytes, no whitespace)")).toHaveValue("");
  expect(change.mock.calls[0][0]).toMatchObject({ action: "set", secret: "synthetic-environment-value", binding: { target: server.target, credential_ref: "mcp-fixture" } });
  expect(JSON.stringify(view.queries.getMutationCache().getAll().map((item) => item.state))).not.toContain("synthetic-environment-value");
  expect(JSON.stringify(view.queries.getQueryCache().getAll().map((item) => item.state.data))).not.toContain("synthetic-environment-value");
  view.unmount();
  const readonly = new APIClient("read");
  vi.spyOn(readonly, "mcpCredentialStatus").mockResolvedValue(credentialStatus(server));
  mount(<MCPEnvironmentCredentialControls client={readonly} server={server} capability />);
  await screen.findByText("No local environment credential stored");
  expect(screen.getByLabelText("Environment secret (8–2560 bytes, no whitespace)")).toBeDisabled();
});
