import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { APIClient } from "../api/client";
import { credentialBinding, credentialStatus } from "../test/mcp-credential-fixtures";
import { MCPOAuthControls } from "./mcp-oauth-controls";

it.each(["pre_registered", "metadata_url", "auto", "dynamic"] as const)("discovers an issuer, starts a %s login and cancels its exact flow", async (mode) => {
  const user = userEvent.setup();
  const client = new APIClient("read", "/api/v1", "control");
  let current = credentialStatus();
  vi.spyOn(client, "mcpCredentialStatus").mockImplementation(async () => current);
  const change = vi.spyOn(client, "changeMCPOAuth").mockImplementation(async (body) => {
    if (body.action === "oauth-discover") return { ...current, oauth: { state: "discovered", issuers: ["https://issuer.example"] } };
    if (body.action === "oauth-start") {
      current = { ...current, oauth: { state: "authorizing", issuer: "https://issuer.example", flow_id: "a".repeat(26) } };
      return { ...current, oauth: { ...current.oauth!, authorization_url: "https://issuer.example/authorize?state=fixture" } };
    }
    current = { ...current, oauth: { state: "cancelled", flow_id: "a".repeat(26) } };
    return current;
  });
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={queries}><MCPOAuthControls client={client} binding={credentialBinding()} queryKey={["fixture-oauth"]} /></QueryClientProvider>);
  await user.click(screen.getByText("OAuth browser authorization"));
  await waitFor(() => expect(screen.getByRole("checkbox")).toBeEnabled());
  expect(screen.getByRole("button", { name: "Discover OAuth issuers" })).toBeDisabled();
  await user.click(screen.getByRole("checkbox"));
  await user.click(screen.getByRole("button", { name: "Discover OAuth issuers" }));
  await waitFor(() => expect(screen.getByLabelText("Confirm trusted issuer")).toHaveValue("https://issuer.example"));
  await user.selectOptions(screen.getByLabelText("Client ID type"), mode);
  const clientID = mode === "metadata_url" ? "https://client.example/metadata.json" : mode === "pre_registered" ? "public-fixture" : "";
  if (clientID) await user.type(screen.getByLabelText(mode === "metadata_url" ? "Client metadata URL" : "Client ID"), clientID);
  if (mode === "pre_registered") {
    await user.type(screen.getByLabelText("Client secret (optional)"), "discard-on-mode-change");
    await user.selectOptions(screen.getByLabelText("Client ID type"), "dynamic");
    await user.selectOptions(screen.getByLabelText("Client ID type"), "pre_registered");
    expect(screen.getByLabelText("Client secret (optional)")).toHaveValue("");
    await user.type(screen.getByLabelText("Client secret (optional)"), "fixture-private-secret");
  }
  await user.type(screen.getByLabelText("Callback port (blank for automatic)"), "8766");
  await user.click(screen.getByRole("button", { name: "Start authorization" }));
  expect(await screen.findByRole("link", { name: "Open authorization page" })).toHaveAttribute("href", "https://issuer.example/authorize?state=fixture");
  expect(change.mock.calls.at(-1)?.[0]).toMatchObject({ oauth_client_id_mode: mode, ...(clientID ? {oauth_client_id: clientID} : {}), oauth_callback_port: 8766 });
  if (mode === "pre_registered") {
    expect(change.mock.calls.at(-1)?.[0].oauth_client_secret).toBe("fixture-private-secret");
    expect(screen.getByLabelText("Client secret (optional)")).toHaveValue("");
    expect(JSON.stringify(queries.getQueryData(["fixture-oauth"]))).not.toContain("fixture-private-secret");
    expect(queries.getMutationCache().getAll().some((mutation) => JSON.stringify(mutation.state.variables).includes("fixture-private-secret"))).toBe(false);
  }
  await user.click(screen.getByRole("button", { name: "Cancel authorization" }));
  await waitFor(() => expect(change.mock.calls.at(-1)?.[0]).toMatchObject({ action: "oauth-cancel", oauth_flow_id: "a".repeat(26) }));
  await waitFor(() => expect(screen.queryByRole("link", { name: "Open authorization page" })).not.toBeInTheDocument());
});
