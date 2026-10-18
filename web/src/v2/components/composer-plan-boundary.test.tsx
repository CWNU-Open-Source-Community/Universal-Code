import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../../lib/locale";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render as renderComponent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { APIClient } from "../../api/client";
import type { WorkspaceImageAttachment } from "../../api/image-attachments";
import { V2Composer } from "./composer";
import { v2FileReferenceKey, type V2FileReference } from "./file-context";
import { v2ImageReferenceKey } from "./image-input";

const workspaceID = "plan-boundary-workspace";
const threadID = "plan-boundary-thread";
const image: WorkspaceImageAttachment = { id: "selected-image", workspace_id: workspaceID, name: "修改要求.png",
  sha256: "a".repeat(64), mime_type: "image/png", byte_size: 64, width: 4, height: 4 };
const file: V2FileReference = { id: "selected-file", path: "requirements.md", digest: "b".repeat(64), partial: false, redacted: false };

beforeEach(() => {
  vi.stubGlobal("URL", Object.assign(URL, { createObjectURL: vi.fn(() => "blob:plan-boundary-image"), revokeObjectURL: vi.fn() }));
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

function mount() {
  const queries = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const upload = vi.fn(async (_workspace: string, _file: File, _key: string): Promise<WorkspaceImageAttachment> => image);
  const client = { baseURL: "/api/v1", hasThreadControl: true, hasEvidenceAttachment: true,
    getThreadExecutionPermission: vi.fn(async () => { throw new Error("This fixture does not expose permission controls"); }),
    downloadWorkspaceImage: vi.fn(async () => new Blob(["fixture image"], { type: "image/png" })),
    uploadWorkspaceImage: upload } as unknown as APIClient;
  const confirm = vi.fn(); const submit = vi.fn(async () => {});
  // No RecoveryProvider: the real hooks must use their existing QueryClient
  // fallback. Only the consumer button is a probe; the supplied state is real.
  render(<QueryClientProvider client={queries}><V2Composer client={client} workspaceID={workspaceID} threadID={threadID}
    workspaces={[]} onWorkspaceChange={() => {}} onSubmit={submit}
    threadControls={(hasUnsentDraft) => <button type="button" disabled={hasUnsentDraft} onClick={confirm}>确认已看过的计划</button>} />
  </QueryClientProvider>);
  return { queries, upload, confirm, submit };
}

test.each(["file", "image"] as const)("blocks plan confirmation for an unsent %s without recovery storage, then unblocks after real removal", async (kind) => {
  const page = mount(); const user = userEvent.setup();
  const confirm = screen.getByRole("button", { name: "确认已看过的计划" });
  expect(confirm).toBeEnabled();
  expect(screen.getByRole("textbox", { name: "继续对话" })).toHaveValue("");
  act(() => {
    if (kind === "file") page.queries.setQueryData(v2FileReferenceKey(workspaceID, threadID), [file]);
    else page.queries.setQueryData(v2ImageReferenceKey(workspaceID, threadID), [image]);
  });
  const remove = await screen.findByRole("button", { name: kind === "file" ? "移除引用 requirements.md" : "移除图片 修改要求.png" });
  await waitFor(() => expect(confirm).toBeDisabled());
  await user.click(confirm);
  expect(page.confirm).not.toHaveBeenCalled();
  await user.click(remove);
  await waitFor(() => expect(confirm).toBeEnabled());
  expect(page.queries.getQueryData(kind === "file" ? v2FileReferenceKey(workspaceID, threadID) : v2ImageReferenceKey(workspaceID, threadID))).toEqual([]);
  await user.click(confirm);
  expect(page.confirm).toHaveBeenCalledOnce();
  expect(page.submit).not.toHaveBeenCalled();
});

test("blocks confirmation while a real image upload is pending, stays blocked for its saved reference, and allows confirmation only after removal", async () => {
  const page = mount(); let finish!: (image: WorkspaceImageAttachment) => void;
  page.upload.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
  const user = userEvent.setup(); const confirm = screen.getByRole("button", { name: "确认已看过的计划" });
  const input = screen.getByRole("textbox", { name: "继续对话" });
  expect(confirm).toBeEnabled();
  const selected = new File(["image fixture bytes"], "修改要求.png", { type: "image/png" });
  fireEvent.paste(input, { clipboardData: { files: [selected] } });
  expect(confirm).toBeDisabled(); // Includes asynchronous clipboard format recognition.
  await waitFor(() => expect(page.upload).toHaveBeenCalledWith(workspaceID, selected, expect.stringMatching(/^v2-image-upload-/u)));
  expect(page.queries.getQueryData(v2ImageReferenceKey(workspaceID, threadID))).toEqual([]);
  expect(screen.getByText("正在保存图片，完成后可发送…")).toBeVisible();
  expect(confirm).toBeDisabled();
  await user.click(confirm);
  expect(page.confirm).not.toHaveBeenCalled();
  await act(async () => finish(image));
  const remove = await screen.findByRole("button", { name: "移除图片 修改要求.png" });
  expect(confirm).toBeDisabled();
  await user.click(remove);
  await waitFor(() => expect(confirm).toBeEnabled());
  expect(input).toHaveValue("");
  expect(page.submit).not.toHaveBeenCalled();
});

// Retain textarea coverage through the supported plain-text editing mode.
beforeEach(() => localStorage.setItem("universal-code.composer-plain-text", "true"));

function render(ui: ReactElement, options?: RenderOptions) {
  window.localStorage.setItem("prayu.locale.v1", "zh-CN");
  return renderComponent(ui, { wrapper: LocaleProvider, ...options });
}
