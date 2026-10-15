import { afterEach, expect, it, vi } from "vitest";
import { APIClient } from "./client";
import { hookDiagnostics, pluginHistory } from "../test/plugin-lifecycle-fixtures";
import type { PluginRollbackRequestView } from "./types";

afterEach(() => vi.unstubAllGlobals());
function response(data: unknown) { return new Response(JSON.stringify({ version: "api.v1", request_id: "lifecycle", data }), { headers: { "Content-Type": "application/json" } }); }
function rollbackRequest(): PluginRollbackRequestView { return { version: "plugin-lifecycle.v1", target_installation_id: "plugin-old",
  expected_current_fingerprint: "c".repeat(64), expected_current_generation: 4, expected_target_fingerprint: "d".repeat(64),
  expected_target_generation: 5, capabilities: ["hooks"], confirm_untrusted: true }; }
function rollbackReply() { const [current, target] = pluginHistory().installations;
  return { protocol_version: "plugin-lifecycle.v1", current: { ...current, state: "rolled_back", generation: 5, enabled_capabilities: [] },
    target: { ...target, state: "enabled", generation: 6, enabled_capabilities: ["hooks"] } }; }

it("reads bounded history and exact Hook scope with the read token and preserved historical decisions", async () => {
  const fetchMock = vi.fn().mockResolvedValueOnce(response(pluginHistory())).mockResolvedValueOnce(response(hookDiagnostics()));
  vi.stubGlobal("fetch", fetchMock);
  const client = new APIClient("read");
  expect((await client.pluginHistory("plugin-current")).publisher?.state).toBe("trusted");
  const receipts = await client.hookDiagnostics("run-one", "project-one");
  expect(receipts.observations.map((item) => item.decision)).toEqual(["rejected", "unknown"]);
  expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/extensions/plugins/plugin-current/history");
  expect(fetchMock.mock.calls[1][0]).toBe("/api/v1/extensions/hooks?run_id=run-one&workspace_id=project-one");
  expect(fetchMock.mock.calls.every(([, options]) => options.headers.Authorization === "Bearer read")).toBe(true);
});

it("shows revoked signing-key trust across publisher display names", async () => {
  const value = pluginHistory();
  value.installations[0].manifest.publisher = "Renamed publisher";
  value.publisher!.state = "revoked";
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(value)));
  const actual = await new APIClient("read").pluginHistory("plugin-current");
  expect(actual.publisher?.state).toBe("revoked");
  expect(actual.publisher?.fingerprint).toBe(actual.installations[0].publisher_fingerprint);
});

it.each(["trusted-alias", "foreign-key"])("keeps publisher trust bound for %s", async (kind) => {
  const value = pluginHistory();
  if (kind === "trusted-alias") value.installations[0].manifest.publisher = "Renamed publisher";
  else { value.publisher!.state = "revoked"; value.publisher!.fingerprint = "a".repeat(64); }
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(value)));
  await expect(new APIClient("read").pluginHistory("plugin-current")).rejects.toThrow();
});

it("verifies both rollback generations, selected capabilities and publisher revocation fingerprint", async () => {
  const fetchMock = vi.fn().mockResolvedValueOnce(response(rollbackReply())).mockResolvedValueOnce(response({ protocol_version: "plugin-lifecycle.v1", installation_id: "plugin-current",
    publisher: { ...pluginHistory().publisher, state: "revoked", generation: 4 } }));
  vi.stubGlobal("fetch", fetchMock);
  const client = new APIClient("read", "/api/v1", "control");
  expect((await client.rollbackPluginInstallation("plugin-current", rollbackRequest())).target.id).toBe("plugin-old");
  expect((await client.revokePluginPublisher("plugin-current", { version: "plugin-lifecycle.v1", expected_publisher_fingerprint: "f".repeat(64), expected_publisher_generation: 3, confirm: true })).publisher.state).toBe("revoked");
  expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual(rollbackRequest());
  expect(fetchMock.mock.calls.every(([, options]) => options.headers.Authorization === "Bearer control")).toBe(true);
});

it.each(["generation", "capabilities", "version", "secret"])("rejects rollback response drift: %s", async (drift) => {
  const reply = rollbackReply();
  if (drift === "generation") reply.target.generation++;
  if (drift === "capabilities") reply.target.enabled_capabilities = ["skills"];
  if (drift === "version") reply.target.package_fingerprint = "e".repeat(64);
  if (drift === "secret") Object.assign(reply.target, { publisher_public_key: "private material" });
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(reply)));
  await expect(new APIClient("read", "/api/v1", "control").rollbackPluginInstallation("plugin-current", rollbackRequest())).rejects.toThrow();
});

it.each(["scope", "rejection", "message", "historical", "active"])("rejects Hook diagnostic drift: %s", async (drift) => {
  const value = hookDiagnostics();
  if (drift === "scope") value.observations[0].run_id = "other-run";
  if (drift === "rejection") value.observations[0].decision = "continued";
  if (drift === "message") Object.assign(value.declarations[0], { message: "plugin-controlled message" });
  if (drift === "historical") value.observations[1].decision = "continued";
  if (drift === "active") value.declarations[0].installation_state = "revoked";
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(value)));
  await expect(new APIClient("read").hookDiagnostics("run-one", "project-one")).rejects.toThrow();
});

it("refuses lifecycle writes locally on a read connection", async () => {
  const fetchMock = vi.fn(); vi.stubGlobal("fetch", fetchMock);
  const client = new APIClient("read");
  await expect(client.rollbackPluginInstallation("plugin-current", rollbackRequest())).rejects.toThrow();
  await expect(client.revokePluginPublisher("plugin-current", { version: "plugin-lifecycle.v1", expected_publisher_fingerprint: "f".repeat(64), expected_publisher_generation: 3, confirm: true })).rejects.toThrow();
  expect(fetchMock).not.toHaveBeenCalled();
});

it("grants publisher trust only with the exact reviewed binding", async () => {
  const reply = () => response({ protocol_version: "plugin-lifecycle.v1", installation_id: "plugin-current",
    publisher: { ...pluginHistory().publisher, state: "trusted", generation: 4 } });
  const fetchMock = vi.fn().mockResolvedValue(reply());
  vi.stubGlobal("fetch", fetchMock);
  const client = new APIClient("read", "/api/v1", "control");
  const granted = await client.trustPluginPublisher("plugin-current", {
    version: "plugin-lifecycle.v1", expected_publisher_fingerprint: "f".repeat(64),
    expected_publisher_generation: 3, confirm: true,
  });
  expect(granted.publisher.state).toBe("trusted");
  expect(granted.publisher.generation).toBe(4);
  expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/extensions/plugins/plugin-current/publisher-trust");
  expect(fetchMock.mock.calls[0][1].headers.Authorization).toBe("Bearer control");
  expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({
    version: "plugin-lifecycle.v1", expected_publisher_fingerprint: "f".repeat(64),
    expected_publisher_generation: 3, confirm: true,
  });

  const drifted = vi.fn().mockResolvedValue(response({ protocol_version: "plugin-lifecycle.v1",
    installation_id: "plugin-current",
    publisher: { ...pluginHistory().publisher, state: "trusted", generation: 9 } }));
  vi.stubGlobal("fetch", drifted);
  await expect(new APIClient("read", "/api/v1", "control").trustPluginPublisher("plugin-current", {
    version: "plugin-lifecycle.v1", expected_publisher_fingerprint: "f".repeat(64),
    expected_publisher_generation: 3, confirm: true,
  })).rejects.toThrow("Publisher trust binding is invalid");

  vi.stubGlobal("fetch", vi.fn());
  await expect(new APIClient("read").trustPluginPublisher("plugin-current", {
    version: "plugin-lifecycle.v1", expected_publisher_fingerprint: "f".repeat(64),
    expected_publisher_generation: 3, confirm: true,
  })).rejects.toThrow("exact publisher trust binding and confirmation are required");
  await expect(new APIClient("read", "/api/v1", "control").trustPluginPublisher("plugin-current", {
    version: "plugin-lifecycle.v1", expected_publisher_fingerprint: "f".repeat(64),
    expected_publisher_generation: 3, confirm: false as true,
  })).rejects.toThrow("exact publisher trust binding and confirmation are required");
});
