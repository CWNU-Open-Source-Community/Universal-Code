import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { APIClient } from "../api/client";
import type { MCPCredentialStatusView } from "../api/types";
import { credentialBinding, credentialServer, credentialStatus } from "../test/mcp-credential-fixtures";
import { MCPCredentialControls } from "./mcp-credential-controls";

afterEach(() => vi.restoreAllMocks());
function mount(client = new APIClient("read", "/api/v1", "control"), server = credentialServer(), capability = true) {
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const view = render(<QueryClientProvider client={queries}><MCPCredentialControls client={client} server={server} capability={capability} /></QueryClientProvider>);
  return { ...view, queries, client, server, switchServer(next = server, nextClient = client) {
    view.rerender(<QueryClientProvider client={queries}><MCPCredentialControls client={nextClient} server={next} capability={capability} /></QueryClientProvider>);
  } };
}
const confirmLabel = /I confirm changing this system credential name/u;
const tokenLabel = /Bearer token \(8–2560 bytes/u;
async function open() { await userEvent.click(screen.getByText("Manage MCP authentication token")); }

it("enters, updates and removes a token explicitly, clears plaintext and preserves review separation", async () => {
  const user = userEvent.setup();
  const client = new APIClient("read", "/api/v1", "control");
  vi.spyOn(client, "mcpCredentialStatus").mockResolvedValue(credentialStatus());
  const change = vi.spyOn(client, "changeMCPCredential").mockImplementation(async (body) => credentialStatus(undefined, body.action === "set"));
  const view = mount(client);
  await screen.findByText("No local token stored"); await open();
  await user.type(screen.getByLabelText(tokenLabel), "synthetic-first-token");
  expect(change).not.toHaveBeenCalled();
  expect(screen.getByRole("button", { name: "Save local token" })).toBeDisabled();
  await user.click(screen.getByRole("checkbox", { name: confirmLabel }));
  await user.click(screen.getByRole("button", { name: "Save local token" }));
  await screen.findByText("Token saved and local presence verified.");
  expect(screen.getByLabelText(tokenLabel)).toHaveValue("");
  expect(change.mock.calls[0][0]).toMatchObject({ action: "set", binding: credentialBinding(), secret: "synthetic-first-token", confirm: true });
  expect(screen.getByText(/first review the descriptor and select Approve discovery/u)).toBeInTheDocument();
  await user.type(screen.getByLabelText(tokenLabel), "synthetic-updated-token");
  await user.click(screen.getByRole("checkbox", { name: confirmLabel }));
  await user.click(screen.getByRole("button", { name: "Update local token" }));
  await waitFor(() => expect(change).toHaveBeenCalledTimes(2));
  await user.click(screen.getByRole("checkbox", { name: confirmLabel }));
  await user.click(screen.getByRole("button", { name: "Remove local token" }));
  await screen.findByText("Local token removed and absence verified.");
  expect(change.mock.calls[2][0]).not.toHaveProperty("secret");
  expect(JSON.stringify(view.queries.getQueryCache().getAll().map((query) => query.state.data))).not.toContain("synthetic-first-token");
  expect(JSON.stringify(view.queries.getMutationCache().getAll().map((mutation) => mutation.state))).not.toContain("synthetic-first-token");
  expect(Object.values(window.localStorage).join(" ")).not.toContain("synthetic-first-token");
});

it("updates available next steps when the server review state changes with a stored token", async () => {
  const client = new APIClient("read", "/api/v1", "control");
  vi.spyOn(client, "mcpCredentialStatus").mockResolvedValue(credentialStatus(undefined, true));
  const change = vi.spyOn(client, "changeMCPCredential");
  const view = mount(client); await open(); await screen.findByText("Local token stored");
  for (const state of ["staged", "disabled", "quarantined"]) {
    view.switchServer({ ...view.server, state });
    expect(screen.getByText(/first review the descriptor and select Approve discovery/u)).toBeInTheDocument();
  }
  view.switchServer({ ...view.server, state: "discovery_approved" });
  expect(screen.getByText(/Once discovery completes, review the capability fingerprint/u)).toBeInTheDocument();
  expect(screen.queryByText(/first review the descriptor and select Approve discovery/u)).not.toBeInTheDocument();
  view.switchServer({ ...view.server, state: "capabilities_pending" });
  expect(screen.getByText(/Then review the capability fingerprint and select Review and enable capabilities/u)).toBeInTheDocument();
  view.switchServer({ ...view.server, state: "enabled" });
  expect(screen.getByText(/then follow the returned review state/u)).toBeInTheDocument();
  view.switchServer({ ...view.server, state: "revoked" });
  expect(screen.getByText(/To reconnect, register and review a new server descriptor/u)).toBeInTheDocument();
  expect(screen.queryByText(/select Rediscover/u)).not.toBeInTheDocument();
  expect(change).not.toHaveBeenCalled();
});

it.each(["readonly", "unsupported-store", "endpoint-conflict"])("shows presence but blocks mutation for %s", async (kind) => {
  const client = new APIClient("read", "/api/v1", kind === "readonly" ? "" : "control");
  const data = credentialStatus();
  if (kind === "unsupported-store") data.store_available = false;
  if (kind === "endpoint-conflict") { data.endpoint_conflict = true; data.registration_count = 2; }
  vi.spyOn(client, "mcpCredentialStatus").mockResolvedValue(data);
  const change = vi.spyOn(client, "changeMCPCredential");
  mount(client); await open();
  await waitFor(() => expect(screen.getByLabelText(tokenLabel)).toBeDisabled());
  expect(screen.getByRole("button", { name: "Save local token" })).toBeDisabled();
  expect(change).not.toHaveBeenCalled();
});

it.each(["no-capability", "stdio", "no-reference"])("does not offer an unsupported credential workflow: %s", (kind) => {
  const client = new APIClient("read", "/api/v1", "control");
  const read = vi.spyOn(client, "mcpCredentialStatus");
  const server = credentialServer();
  if (kind === "stdio") server.transport = "stdio";
  if (kind === "no-reference") server.credential_ref = "";
  mount(client, server, kind !== "no-capability");
  expect(screen.queryByLabelText(tokenLabel)).not.toBeInTheDocument();
  expect(read).not.toHaveBeenCalled();
});

it("discards old presence and token input when descriptor/scope or connection changes", async () => {
  let resolveOld!: (value: MCPCredentialStatusView) => void;
  const oldClient = new APIClient("read", "/api/v1", "control");
  vi.spyOn(oldClient, "mcpCredentialStatus").mockImplementation(() => new Promise((resolve) => { resolveOld = resolve; }));
  const view = mount(oldClient); await open();
  const server = { ...credentialServer(), workspace_id: "workspace-two", descriptor_fingerprint: "f".repeat(64) };
  const nextClient = new APIClient("read-two", "/api/v1", "control-two");
  vi.spyOn(nextClient, "mcpCredentialStatus").mockResolvedValue(credentialStatus(server));
  view.switchServer(server, nextClient); await open();
  await screen.findByText("No local token stored");
  await act(async () => { resolveOld(credentialStatus(undefined, true)); });
  expect(screen.queryByText("Local token stored")).not.toBeInTheDocument();
  expect(screen.getByLabelText(tokenLabel)).toHaveValue("");
});

it("a late mutation updates only its original binding and cannot show success in the replacement form", async () => {
  const user = userEvent.setup();
  let resolveChange!: (value: MCPCredentialStatusView) => void;
  const client = new APIClient("read", "/api/v1", "control");
  vi.spyOn(client, "mcpCredentialStatus").mockImplementation(async (binding) => credentialStatus(
    { ...credentialServer(), id: binding.server_id, workspace_id: binding.workspace_id, descriptor_fingerprint: binding.expected_descriptor_fingerprint }));
  vi.spyOn(client, "changeMCPCredential").mockImplementation(() => new Promise((resolve) => { resolveChange = resolve; }));
  const view = mount(client); await open();
  await screen.findByText("No local token stored");
  await user.type(screen.getByLabelText(tokenLabel), "synthetic-pending-token");
  await user.click(screen.getByRole("checkbox", { name: confirmLabel }));
  await user.click(screen.getByRole("button", { name: "Save local token" }));
  expect(screen.getByLabelText(tokenLabel)).toHaveValue("");
  const server = { ...credentialServer(), id: "second-mcp", descriptor_fingerprint: "f".repeat(64) };
  view.switchServer(server); await open();
  await screen.findByText("No local token stored");
  await act(async () => { resolveChange(credentialStatus(undefined, true)); });
  expect(screen.queryByText("Token saved and local presence verified.")).not.toBeInTheDocument();
  expect(screen.getByLabelText(tokenLabel)).toHaveValue("");
  expect(screen.getByRole("checkbox", { name: confirmLabel })).not.toBeChecked();
});

it("requires a new confirmation after sharing metadata changes", async () => {
  const user = userEvent.setup();
  const client = new APIClient("read", "/api/v1", "control");
  vi.spyOn(client, "mcpCredentialStatus").mockResolvedValue(credentialStatus());
  const view = mount(client); await open(); await screen.findByText("No local token stored");
  await user.type(screen.getByLabelText(tokenLabel), "synthetic-local-token");
  await user.click(screen.getByRole("checkbox", { name: confirmLabel }));
  expect(screen.getByRole("button", { name: "Save local token" })).toBeEnabled();
  await act(async () => {
    const key = view.queries.getQueryCache().getAll()[0].queryKey;
    view.queries.setQueryData(key, { ...credentialStatus(), registration_count: 2, reference_fingerprint: "f".repeat(64) });
  });
  await waitFor(() => expect(screen.getByRole("checkbox", { name: confirmLabel })).not.toBeChecked());
  expect(screen.getByRole("button", { name: "Save local token" })).toBeDisabled();
  expect(screen.getByText(/Multiple MCP registrations share/u)).toBeInTheDocument();
});

it("a failed refresh reports unknown presence even when older cached metadata exists", async () => {
  const client = new APIClient("read", "/api/v1", "control");
  const read = vi.spyOn(client, "mcpCredentialStatus").mockResolvedValueOnce(credentialStatus(undefined, true))
    .mockRejectedValueOnce(new Error("MCP credential presence could not be verified"));
  mount(client); await open(); await screen.findByText("Local token stored");
  await userEvent.click(screen.getByRole("button", { name: "Refresh credential presence" }));
  await screen.findByText("Credential presence is unknown; refresh.");
  expect(read).toHaveBeenCalledTimes(2);
  expect(screen.queryByText("Local token stored")).not.toBeInTheDocument();
  expect(screen.getByLabelText(tokenLabel)).toBeDisabled();
});
