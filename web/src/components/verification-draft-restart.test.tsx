import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../api/client";
import type { DesktopVerificationSettingsView } from "../lib/desktop-bridge";
import { LocaleProvider } from "../lib/locale";
import { batchProposalFixture } from "../test/batch-workbench-fixture";
import { V2RecoveryProvider } from "../v2/recovery-storage";
import { BatchPreparation } from "./batch-workbench";
import { UIEvidencePreparation } from "./ui-evidence-preparation";
import { VerificationSettingsPanel } from "./verification-settings-panel";

const native = vi.hoisted(() => ({ available: vi.fn(), read: vi.fn(), save: vi.fn(), restart: vi.fn() }));
vi.mock("../lib/desktop-bridge", async () => ({
  ...await vi.importActual<typeof import("../lib/desktop-bridge")>("../lib/desktop-bridge"),
  desktopVerificationSettingsAvailable: native.available, getDesktopVerificationSettings: native.read,
  confirmDesktopVerificationSettings: native.save, restartDesktopWithVerificationSettings: native.restart,
}));

beforeEach(() => {
  localStorage.clear(); sessionStorage.clear();
  native.available.mockReset().mockReturnValue(true);
  native.read.mockReset().mockResolvedValue({
    protocol_version: "desktop_verification_settings.v1", revision: 2,
    settings: { ui_evidence_enabled: true, batch_validation_enabled: false },
    active_settings: { ui_evidence_enabled: false, batch_validation_enabled: false },
    restart_required: true, can_save: true, can_restart: true, capability_grant: false,
    ui_evidence: { installed: false, blocked_by: ["startup_gate_closed"], remediation: ["restart_application"] },
    batch_validation: { installed: false, blocked_by: ["startup_gate_closed"], remediation: ["enable_and_restart"] },
  } satisfies DesktopVerificationSettingsView);
  native.save.mockReset(); native.restart.mockReset();
});

it.each([
  { kind: "browser", label: "启动命令（PowerShell）", oversized: "中".repeat(21846), initial: "node app.js", corrected: "node repaired.js",
    key: 'ui-evidence-draft:["run-1"]' },
  { kind: "batch", label: "负责路径（每行一个）", oversized: "a".repeat(131073), initial: "internal/parser/a.go", corrected: "internal/parser/repaired.go",
    key: 'batch-workbench-draft:["run-1","proposal-1"]' },
])("blocks restart for an unsaved $kind draft across form unmount until correction or explicit discard", async ({ kind, label, oversized, initial, corrected, key }) => {
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const onStart = vi.fn();
  const client = { baseURL: "/api/v1", hasUIEvidence: true, hasBatchDeliveryHostValidation: false,
    getRunChildTaskProposals: vi.fn().mockResolvedValue({ items: [batchProposalFixture()] }), prepareBatchWorkbench: vi.fn() } as unknown as APIClient;
  const tree = (show: boolean) => <QueryClientProvider client={queries}>
    <V2RecoveryProvider client={client} scopeID="draft-restart-store"><LocaleProvider>
      {show && (kind === "browser" ? <UIEvidencePreparation client={client} runID="run-1" pending={false} unresolved={false} onStart={onStart} />
        : <BatchPreparation client={client} runID="run-1" usedProposalIDs={[]} onReviewProposals={vi.fn()} />)}
      <VerificationSettingsPanel client={client} />
    </LocaleProvider></V2RecoveryProvider>
  </QueryClientProvider>;
  const user = userEvent.setup();
  const page = render(tree(true));
  const open = async () => { if (kind === "browser") await user.click(screen.getByText("准备并启动浏览器验证")); };
  await open();
  const restart = await screen.findByRole("button", { name: "重启以应用验证设置" });
  expect(restart).toBeEnabled();
  fireEvent.change(await screen.findByLabelText(label), { target: { value: initial } });
  const storageKey = `v2_recovery.v1:${encodeURIComponent(location.origin)}:${encodeURIComponent(client.baseURL)}:draft-restart-store:${encodeURIComponent(key)}`;
  const saved = localStorage.getItem(storageKey);
  expect(saved).toContain(initial);

  fireEvent.change(screen.getByLabelText(label), { target: { value: oversized } });
  expect(screen.getByLabelText(label)).toHaveValue(oversized);
  await waitFor(() => expect(restart).toBeDisabled());
  expect(localStorage.getItem(storageKey)).toBe(saved);
  page.rerender(tree(false));
  expect(screen.getByRole("button", { name: "重启以应用验证设置" })).toBeDisabled();
  page.rerender(tree(true));
  await open();
  expect(await screen.findByLabelText(label)).toHaveValue(initial);
  expect(screen.getByRole("button", { name: "重启以应用验证设置" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "放弃未保存草稿" })).toBeEnabled();

  fireEvent.change(screen.getByLabelText(label), { target: { value: corrected } });
  await waitFor(() => expect(screen.getByRole("button", { name: "重启以应用验证设置" })).toBeEnabled());
  expect(localStorage.getItem(storageKey)).toContain(corrected);
  fireEvent.change(screen.getByLabelText(label), { target: { value: oversized } });
  await waitFor(() => expect(screen.getByRole("button", { name: "重启以应用验证设置" })).toBeDisabled());
  await user.click(screen.getByRole("button", { name: "放弃未保存草稿" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "重启以应用验证设置" })).toBeEnabled());
  expect(screen.getByLabelText(label)).toHaveValue("");
  expect(localStorage.getItem(storageKey)).toBeNull();
  expect(native.restart).not.toHaveBeenCalled();
  expect(onStart).not.toHaveBeenCalled(); expect(client.prepareBatchWorkbench).not.toHaveBeenCalled();
});
