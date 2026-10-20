import type { ReactNode } from "react";
import { act, renderHook, waitFor } from "@testing-library/react";
import { APIClient } from "../api/client";
import { V2RecoveryProvider } from "./recovery-storage";
import { useProjectlessWorkspace } from "./projectless-workspace";

const workspace = { id: "local-one", name: "Chat", created_at: "2026-10-19T00:00:00Z" };
const client = new APIClient("read", "/api/v1", "control", { projectlessWorkspacesEnabled: true });
function wrapper({ children }: { children: ReactNode }) {
  return <V2RecoveryProvider client={client} scopeID="local-folders-test">{children}</V2RecoveryProvider>;
}
beforeEach(() => localStorage.clear());
afterEach(() => vi.restoreAllMocks());

it("retries a lost response with the saved allocation key and restores the directory on reload", async () => {
  const allocate = vi.spyOn(client, "createProjectlessWorkspace")
    .mockRejectedValueOnce(new Error("connection lost"))
    .mockResolvedValue({ version: "projectless_workspace.v1", workspace, replayed: true });
  const first = renderHook(() => useProjectlessWorkspace(client, true), { wrapper });
  await waitFor(() => expect(first.result.current.error).toBe("connection lost"));
  const key = allocate.mock.calls[0][0];
  act(() => first.result.current.retry());
  await waitFor(() => expect(first.result.current.workspaceID).toBe(workspace.id));
  expect(allocate.mock.calls[1][0]).toBe(key);
  first.unmount();
  const second = renderHook(() => useProjectlessWorkspace(client, true), { wrapper });
  expect(second.result.current.workspaceID).toBe(workspace.id);
  expect(allocate).toHaveBeenCalledTimes(2);
});

it("allocates a different folder after a chat is created, and keeps drafts when selecting a project", async () => {
  const allocate = vi.spyOn(client, "createProjectlessWorkspace")
    .mockResolvedValueOnce({ version: "projectless_workspace.v1", workspace, replayed: false })
    .mockResolvedValueOnce({ version: "projectless_workspace.v1", workspace: { ...workspace, id: "local-two" }, replayed: false });
  const hook = renderHook(({ active }) => useProjectlessWorkspace(client, active), { wrapper, initialProps: { active: false } });
  expect(allocate).not.toHaveBeenCalled();
  hook.rerender({ active: true });
  await waitFor(() => expect(hook.result.current.workspaceID).toBe("local-one"));
  hook.rerender({ active: false });
  hook.rerender({ active: true });
  expect(allocate).toHaveBeenCalledTimes(1);
  act(() => hook.result.current.consume("local-one"));
  await waitFor(() => expect(hook.result.current.workspaceID).toBe("local-two"));
  expect(allocate.mock.calls[1][0]).not.toBe(allocate.mock.calls[0][0]);
});

it("does not allocate when the operation key cannot be saved", async () => {
  const allocate = vi.spyOn(client, "createProjectlessWorkspace");
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("disk full"); });
  const hook = renderHook(() => useProjectlessWorkspace(client, true), { wrapper });
  await waitFor(() => expect(hook.result.current.error).toContain("尚未保存"));
  expect(allocate).not.toHaveBeenCalled();
});

vi.mock("../lib/locale", () => ({ useLocale: () => ({ t: (chinese: string) => chinese, locale: "zh-CN" }) }));
