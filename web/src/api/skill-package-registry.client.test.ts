import { afterEach, expect, it, vi } from "vitest";
import { APIClient } from "./client";

afterEach(() => vi.unstubAllGlobals());

function envelope(requestID: string, data: unknown, status = 200) {
  return new Response(JSON.stringify({ version: "api.v1", request_id: requestID, data }),
    { status, headers: { "Content-Type": "application/json" } });
}

const registryItem = {
  skill: "review.helper@1.0.0", surface: "code", profiles: ["review"], surfaces: ["code"],
  phases: ["deliver"], roles: ["root"], user_invocable: true, model_invocable: false,
  explicit_only: true, tool_dependencies: ["read_file"], content_sha256: "a".repeat(64),
  content_bytes: 24, content_token_upper_bound: 24, archive_sha256: "b".repeat(64),
  package_fingerprint: "c".repeat(64), archive_bytes: 512, uncompressed_bytes: 256,
  entry_count: 2, trust_class: "operator_installed_untrusted", risk_codes: [],
  status: "installed", object_verified: true, operator_confirmed: true,
  import_command_execution: false, import_network_access: false, import_provider_calls: false,
  run_selection_authorized: false, context_injection_authorized: false,
  tool_capability_grant: false, content_body_exposed: false, installed_by: "operator",
  created_at: "2026-10-15T00:00:00Z", completed_at: "2026-10-15T00:00:01Z",
};

const registryInventory = {
  protocol_version: "skill_package_inventory.v1", capability_grant: false, surface: "",
  profile: "", include_removed: false, items: [registryItem], returned_count: 1,
  unverified_count: 0,
};

const removalReceipt = {
  protocol_version: "operation_receipt.v1", kind: "skill_package_remove", outcome: "removed",
  durable: true, replayed: false, retry_safe: true, retry_strategy: "same_operation_key",
  recovery_action: "none", cleanup_state: "not_applicable",
};

const removal = {
  protocol_version: "skill_package_removal_control.v1", skill: "review.helper@1.0.0",
  surface: "code", status: "removed", capability_grant: false, package_object_retained: true,
  historical_recovery_preserved: true, future_selection_enabled: false,
  run_selection_authorized: false, context_injection_authorized: false,
  tool_capability_grant: false, removed_by: "http_operator",
  removed_at: "2026-10-15T01:00:00Z", replayed: false, receipt: removalReceipt,
};

function clientWith(fetchMock: typeof fetch, skillInstallationEnabled = true) {
  vi.stubGlobal("fetch", fetchMock);
  return new APIClient("read-secret", "/api/v1", "control-secret", {
    skillInstallationEnabled,
  });
}

it("reads the installed Skill inventory through the read bearer and keeps it inert", async () => {
  const fetchMock = vi.fn(async () => envelope("req-inventory", registryInventory));
  await expect(clientWith(fetchMock).listSkillPackages({ includeRemoved: true }))
    .resolves.toEqual(registryInventory);
  const [path, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
  expect(path).toContain("/skills/packages?include_removed=true");
  expect(init.method ?? "GET").toBe("GET");
  expect((init.headers as Record<string, string>).Authorization).toBe("Bearer read-secret");
  expect((init.headers as Record<string, string>)["Idempotency-Key"]).toBeUndefined();
});

it("rejects an inventory that widens Registry authority or hides private identity", async () => {
  for (const mutation of [
    { capability_grant: true }, { unverified_count: 4 },
    { items: [{ ...registryItem, content_body_exposed: true }] },
    { items: [{ ...registryItem, installation_id: "skill-install-1" }] },
  ]) {
    const fetchMock = vi.fn(async () => envelope("req-inventory-bad",
      { ...registryInventory, ...mutation }));
    await expect(clientWith(fetchMock).listSkillPackages())
      .rejects.toThrow(/inventory response is invalid|Registry authority|projection is invalid/u);
  }
});

it("refuses removal without capability or explicit confirmation", async () => {
  const fetchMock = vi.fn(async () => envelope("req-remove", removal));
  await expect(clientWith(fetchMock, false).removeSkillPackage({
    protocol_version: "skill_package_removal_request.v1", skill: "review.helper@1.0.0",
    confirm_remove: true,
  }, "skill-remove-operation-0001")).rejects.toThrow(/confirmation is required/u);
  await expect(clientWith(fetchMock).removeSkillPackage({
    protocol_version: "skill_package_removal_request.v1", skill: "review.helper@1.0.0",
    confirm_remove: false as true,
  }, "skill-remove-operation-0001")).rejects.toThrow(/confirmation is required/u);
  expect(fetchMock).not.toHaveBeenCalled();
});

it("sends one removal with its idempotency key and validates the durable receipt", async () => {
  const fetchMock = vi.fn(async () => envelope("req-remove", removal));
  await expect(clientWith(fetchMock).removeSkillPackage({
    protocol_version: "skill_package_removal_request.v1", skill: "review.helper@1.0.0",
    confirm_remove: true,
  }, "skill-remove-operation-0001")).resolves.toEqual(removal);
  const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
  expect(init.method).toBe("POST");
  expect((init.headers as Record<string, string>)["Idempotency-Key"])
    .toBe("skill-remove-operation-0001");
  expect((init.headers as Record<string, string>).Authorization).toBe("Bearer control-secret");

  const widened = vi.fn(async () => envelope("req-remove-bad",
    { ...removal, future_selection_enabled: true }));
  await expect(clientWith(widened).removeSkillPackage({
    protocol_version: "skill_package_removal_request.v1", skill: "review.helper@1.0.0",
    confirm_remove: true,
  }, "skill-remove-operation-0002")).rejects.toThrow(/inert Registry authority/u);

  const wrongRetry = vi.fn(async () => envelope("req-remove-retry",
    { ...removal, receipt: { ...removalReceipt, retry_strategy: "same_wake_generation" } }));
  await expect(clientWith(wrongRetry).removeSkillPackage({
    protocol_version: "skill_package_removal_request.v1", skill: "review.helper@1.0.0",
    confirm_remove: true,
  }, "skill-remove-operation-0003")).rejects.toThrow(/recovery authority/u);
});
