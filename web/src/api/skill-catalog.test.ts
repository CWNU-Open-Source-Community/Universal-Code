import { afterEach, expect, it, vi } from "vitest";
import { APIClient } from "./client";

afterEach(() => vi.unstubAllGlobals());
const publisher = { fingerprint: "a".repeat(64), public_key: btoa("a".repeat(32)), name: "Team", team: "",
  trust_class: "trusted", created_at: "2026-10-16T00:00:00Z", updated_at: "2026-10-16T00:00:01Z", trusted_at: "2026-10-16T00:00:01Z" };
function response(data: unknown) {
  return new Response(JSON.stringify({ version: "api.v1", request_id: "catalog", data }), { status: 200, headers: { "Content-Type": "application/json" } });
}

it("keeps the exact observed Catalog version on revoke and checks the response identity", async () => {
  const fetchMock = vi.fn().mockResolvedValueOnce(response({ protocol_version: "skill-catalog-control.v1", control_enabled: true, publishers: [publisher] }))
    .mockResolvedValueOnce(response({ ...publisher, trust_class: "revoked", updated_at: "2026-10-16T00:00:02Z", revoked_at: "2026-10-16T00:00:02Z" }))
    .mockResolvedValueOnce(response({ ...publisher, fingerprint: "b".repeat(64) }));
  vi.stubGlobal("fetch", fetchMock);
  const client = new APIClient("read", "/api/v1", "control", { skillInstallationEnabled: true });
  const current = await client.listSkillCatalogPublishers();
  const request = { version: "skill-catalog-control.v1" as const, action: "revoke" as const, fingerprint: publisher.fingerprint,
    expected_updated_at: current.publishers[0].updated_at, confirm: true };
  await expect(client.controlSkillCatalogPublisher(request)).resolves.toMatchObject({ trust_class: "revoked" });
  expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toEqual(request);
  await expect(client.controlSkillCatalogPublisher(request)).rejects.toMatchObject({ code: "INVALID_RESPONSE" });
});

it("requires explicit confirmation and an observed version before revocation", async () => {
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  const client = new APIClient("read", "/api/v1", "control", { skillInstallationEnabled: true });
  await expect(client.controlSkillCatalogPublisher({ version: "skill-catalog-control.v1", action: "revoke", fingerprint: publisher.fingerprint,
    expected_updated_at: "", confirm: true })).rejects.toThrow("observed version");
  expect(fetchMock).not.toHaveBeenCalled();
});

it("reads the HTTP projection of a legacy CLI publisher and preserves its revoke version", async () => {
  // HTTP canonicalizes the CLI's line-wrapped key; legacy team labels can be
  // 128 characters, matching the existing store limit.
  const legacy = { ...publisher, name: "Legacy publisher", team: "t".repeat(128), updated_at: "2026-10-16T00:00:01.123456789Z" };
  const fetchMock = vi.fn().mockResolvedValueOnce(response({ protocol_version: "skill-catalog-control.v1", control_enabled: true, publishers: [legacy] }))
    .mockResolvedValueOnce(response({ ...legacy, trust_class: "revoked", updated_at: "2026-10-16T00:00:02Z", revoked_at: "2026-10-16T00:00:02Z" }));
  vi.stubGlobal("fetch", fetchMock);
  const client = new APIClient("read", "/api/v1", "control", { skillInstallationEnabled: true });
  const listed = await client.listSkillCatalogPublishers();
  expect(listed.publishers).toEqual([legacy]);
  await client.controlSkillCatalogPublisher({ version: "skill-catalog-control.v1", action: "revoke", fingerprint: legacy.fingerprint,
    expected_updated_at: listed.publishers[0].updated_at, confirm: true });
  expect(JSON.parse(fetchMock.mock.calls[1][1].body).expected_updated_at).toBe(legacy.updated_at);
});
