import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { LocaleProvider } from "../lib/locale";
import { SkillPackageRegistry, type SkillPackageRegistrySource } from "./skill-package-registry";
import type { SkillPackageInventoryView } from "../api/types";

afterEach(() => vi.unstubAllGlobals());

function item(skill: string, overrides: Partial<{ status: string; object_verified: boolean;
  removal: SkillPackageInventoryView["items"][number]["removal"] }> = {}) {
  return {
    skill, surface: "code", profiles: ["review"], surfaces: ["code"], phases: ["deliver"],
    roles: ["root"], user_invocable: true, model_invocable: false, explicit_only: true,
    tool_dependencies: ["read_file"], content_sha256: "a".repeat(64), content_bytes: 24,
    content_token_upper_bound: 24, archive_sha256: "b".repeat(64),
    package_fingerprint: "c".repeat(64), archive_bytes: 512, uncompressed_bytes: 256,
    entry_count: 2, trust_class: "operator_installed_untrusted",
    risk_codes: ["untrusted_instructions"], status: "installed", object_verified: true,
    operator_confirmed: true, import_command_execution: false, import_network_access: false,
    import_provider_calls: false, run_selection_authorized: false,
    context_injection_authorized: false, tool_capability_grant: false,
    content_body_exposed: false, installed_by: "operator", created_at: "2026-10-15T00:00:00Z",
    completed_at: "2026-10-15T00:00:01Z",
    ...overrides,
  } as unknown as SkillPackageInventoryView["items"][number];
}

function inventory(items: SkillPackageInventoryView["items"]): SkillPackageInventoryView {
  return {
    protocol_version: "skill_package_inventory.v1", capability_grant: false,
    surface: "", profile: "", include_removed: items.some((value) => value.status === "removed"),
    items, returned_count: items.length,
    unverified_count: items.filter((value) => !value.object_verified &&
      value.status === "installed").length,
  };
}

function mount(source: SkillPackageRegistrySource, enabled = true) {
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(<QueryClientProvider client={queryClient}><LocaleProvider>
    <SkillPackageRegistry enabled={enabled} source={source} />
  </LocaleProvider></QueryClientProvider>);
}

it("stays silent without the management capability and never reads the Registry", async () => {
  const load = vi.fn(async () => inventory([]));
  mount({ load, remove: vi.fn() }, false);
  expect(await screen.findByText(/连接具备 Skill 管理权限的服务后/)).toBeInTheDocument();
  expect(load).not.toHaveBeenCalled();
});

it("lists registered packages, flags unreadable content and hides removal for tombstones", async () => {
  const load = vi.fn(async () => inventory([
    item("readable.review@1.0.0"),
    item("broken.review@1.0.0", { object_verified: false }),
    item("gone.review@1.0.0", { status: "removed", removal: {
      package_object_retained: true, historical_recovery_preserved: true,
      future_selection_enabled: false, run_selection_authorized: false,
      context_injection_authorized: false, tool_capability_grant: false,
      removed_by: "operator", removed_at: "2026-10-15T01:00:00Z",
    } }),
  ]));
  mount({ load, remove: vi.fn() });
  expect(await screen.findByText("readable.review@1.0.0")).toBeInTheDocument();
  expect(screen.getByText("broken.review@1.0.0")).toBeInTheDocument();
  expect(screen.getByText(/包内容暂不可读/)).toBeInTheDocument();
  expect(screen.getByText("gone.review@1.0.0")).toBeInTheDocument();
  expect(screen.getAllByText("已移除")).toHaveLength(1);
  expect(screen.getAllByText("移除登记")).toHaveLength(2);
  expect(screen.getByText(/包对象已保留/)).toBeInTheDocument();
});

it("requires explicit confirmation and reuses one operation key across a failed retry", async () => {
  const remove = vi.fn(async (_skill: string, _key: string) => {
    throw new Error("run_pinned");
  });
  mount({ load: vi.fn(async () => inventory([item("retry.review@1.0.0")])), remove });
  const button = await screen.findByRole("button", { name: "移除登记" });
  expect(button).toBeDisabled();
  await userEvent.click(screen.getByRole("checkbox", { name: /确认移除此技能包/ }));
  expect(button).toBeEnabled();
  await userEvent.click(button);
  await waitFor(() => expect(remove).toHaveBeenCalledTimes(1));
  expect(screen.getByText("run_pinned")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "移除登记" }));
  await waitFor(() => expect(remove).toHaveBeenCalledTimes(2));
  const keys = remove.mock.calls.map((call) => String((call as [string, string])[1]));
  expect(keys[0]).toMatch(/^skill-package-remove-/u);
  expect(keys).toEqual([keys[0], keys[0]]);
});

it("refreshes the list after a confirmed removal and reports the retained history", async () => {
  const remove = vi.fn(async (_skill: string, _key: string) => ({}));
  const load = vi.fn(async () => inventory([item("first.review@1.0.0")]));
  mount({ load, remove });
  await screen.findByText("first.review@1.0.0");
  await userEvent.click(screen.getByRole("checkbox", { name: /确认移除此技能包/ }));
  await userEvent.click(screen.getByRole("button", { name: "移除登记" }));
  expect(await screen.findByText(/已移除登记，历史任务记录保持不变/)).toBeInTheDocument();
  await waitFor(() => expect(remove).toHaveBeenCalledTimes(1));
  expect(remove.mock.calls[0][0]).toBe("first.review@1.0.0");
  expect(load.mock.calls.length).toBeGreaterThanOrEqual(2);
});

it("distinguishes an empty Registry from one without removable entries", async () => {
  const load = vi.fn(async () => inventory([]));
  mount({ load, remove: vi.fn() });
  expect(await screen.findByText(/本机没有可移除的历史技能包/)).toBeInTheDocument();
  await userEvent.click(screen.getByRole("checkbox", { name: /显示已移除记录/ }));
  expect(await screen.findByText(/本机没有已登记的历史技能包/)).toBeInTheDocument();
  await waitFor(() => expect(load).toHaveBeenLastCalledWith(true, expect.any(AbortSignal)));
});
