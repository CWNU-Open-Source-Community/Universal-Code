import type { ReactElement } from "react";
import type { RenderOptions } from "@testing-library/react";
import { LocaleProvider } from "../lib/locale";
import { act, fireEvent, render as renderComponent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { V2RecoveryProvider } from "./recovery-storage";
import { useNativeClipboard } from "./use-native-clipboard";

const file = { id: "attachment-one", workspace_id: "workspace-a", name: "requirements.md", mime_type: "text/markdown", sha256: "a".repeat(64),
  byte_size: 7, readability: "text" as const, text_sha256: "b".repeat(64), text_bytes: 7, redacted: false };
const result = (extra: Record<string, unknown> = {}) => ({ version: "desktop_clipboard_files.v1", workspace_id: "workspace-a", status: "processed",
  batch_complete: true, images: [], attachments: [file], rejected: [], ...extra });
function native() {
  const paste = vi.fn().mockResolvedValue(result()), inspect = vi.fn().mockResolvedValue(result({ status: "partial", batch_complete: false }));
  window.go = { desktop: { DesktopBridge: { Bootstrap: vi.fn(), InstallSkillPackage: vi.fn(), SelectSkillPackage: vi.fn(), PreviewSkillPackage: vi.fn(), PasteClipboardFiles: paste, InspectClipboardFiles: inspect } } };
  return { paste, inspect };
}
function deferred<T>() { let resolve!: (value: T) => void; let reject!: (error: Error) => void; const promise = new Promise<T>((a, b) => { resolve = a; reject = b; }); return { promise, resolve, reject }; }
function Harness({ workspaceID = "workspace-a", onImported = vi.fn(), captureImport }: { workspaceID?: string; onImported?: (images: unknown[], files: unknown[]) => void; captureImport?: () => (images: unknown[], files: unknown[]) => void }) {
  const input = useNativeClipboard({ workspaceID, threadID: "thread-a", onImported, captureImport });
  return <><button onClick={() => void input.onPasteFallback()} disabled={input.busy}>粘贴文件</button><span>{input.pending ? "存在待核对" : "没有待核对"}</span>{input.notice}</>;
}
const app = (workspaceID = "workspace-a", onImported = vi.fn()) => <V2RecoveryProvider client={{ baseURL: "http://native-test/api/v1" }} scopeID="clipboard-db"><Harness workspaceID={workspaceID} onImported={onImported} /></V2RecoveryProvider>;
const savedRecords = () => Object.keys(localStorage).filter((key) => decodeURIComponent(key).includes("native-clipboard:")).map((key) => ({ key, value: JSON.parse(localStorage.getItem(key)!).value }));
beforeEach(() => localStorage.clear());
afterEach(() => { delete window.go; vi.restoreAllMocks(); });

it("persists the original intent before one explicit paste and adds the exact receipts", async () => {
  const { paste, inspect } = native(); const pending = deferred<unknown>(); paste.mockReturnValue(pending.promise);
  const imported = vi.fn(); render(app("workspace-a", imported));
  fireEvent.click(screen.getByText("粘贴文件"));
  expect(paste).toHaveBeenCalledTimes(1); expect(savedRecords()).toHaveLength(1);
  expect(savedRecords()[0].value.operationKey).toBe(paste.mock.calls[0][0].operation_key);
  await act(async () => pending.resolve(result()));
  expect(imported).toHaveBeenCalledExactlyOnceWith([], [file]);
  expect(savedRecords()).toHaveLength(0); expect(inspect).not.toHaveBeenCalled();
});

it("after a lost reply and reopen only inspects the original key and explicitly adds partial receipts", async () => {
  const { paste, inspect } = native(); paste.mockRejectedValue(new Error("粘贴响应丢失"));
  const first = render(app()); fireEvent.click(screen.getByText("粘贴文件"));
  await screen.findByText("粘贴响应丢失"); const original = paste.mock.calls[0][0]; first.unmount();
  const imported = vi.fn(); render(app("workspace-a", imported));
  await screen.findByText("加入已保存的附件");
  expect(inspect).toHaveBeenCalledExactlyOnceWith(original); expect(paste).toHaveBeenCalledTimes(1); expect(imported).not.toHaveBeenCalled();
  fireEvent.click(screen.getByText("加入已保存的附件"));
  expect(imported).toHaveBeenCalledExactlyOnceWith([], [file]);
  expect(screen.getByText("已加入找到的附件，原批次是否完整仍待确认。核对附件列表后，可点击“结束本次粘贴核对”。")).toBeInTheDocument();
  expect(savedRecords()).toHaveLength(1);
  fireEvent.click(screen.getByText("结束本次粘贴核对"));
  expect(savedRecords()).toHaveLength(0); expect(paste).toHaveBeenCalledTimes(1);
});

it("keeps a late result and callback on its original workspace without leaking busy or error to another", async () => {
  const { paste } = native(); const pending = deferred<unknown>(); paste.mockReturnValue(pending.promise);
  const firstImported = vi.fn(() => { throw new Error("原草稿容量已满"); }), secondImported = vi.fn();
  const page = render(app("workspace-a", firstImported)); fireEvent.click(screen.getByText("粘贴文件"));
  page.rerender(app("workspace-b", secondImported)); expect(screen.getByText("粘贴文件")).toBeEnabled();
  await act(async () => pending.resolve(result()));
  expect(firstImported).toHaveBeenCalledExactlyOnceWith([], [file]); expect(secondImported).not.toHaveBeenCalled();
  expect(screen.queryByText("原草稿容量已满")).not.toBeInTheDocument();
  expect(savedRecords()[0].value.workspaceID).toBe("workspace-a");
  page.rerender(app("workspace-a", firstImported)); await screen.findByText("加入已保存的附件");
});

it("retains saved receipts after draft capacity failure and retries addition without another paste", async () => {
  const { paste, inspect } = native(); const imported = vi.fn().mockImplementationOnce(() => { throw new Error("附件数量超过上限"); });
  render(app("workspace-a", imported)); fireEvent.click(screen.getByText("粘贴文件"));
  await screen.findByText("附件数量超过上限");
  expect(savedRecords()[0].value.result.attachments).toEqual([file]);
  fireEvent.click(screen.getByText("加入已保存的附件"));
  expect(imported).toHaveBeenCalledTimes(2); expect(paste).toHaveBeenCalledTimes(1); expect(inspect).not.toHaveBeenCalled();
  expect(savedRecords()).toHaveLength(0);
});

it("does not access the native clipboard when local intent persistence fails", async () => {
  const { paste } = native(); render(app());
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("disk full"); });
  fireEvent.click(screen.getByText("粘贴文件"));
  await screen.findByText(/无法保存本机恢复数据/);
  expect(paste).not.toHaveBeenCalled();
});

it("preserves invalid recovery records and does not replace them with a new native request", async () => {
  const { paste, inspect } = native(); paste.mockRejectedValue(new Error("lost"));
  const page = render(app()); fireEvent.click(screen.getByText("粘贴文件")); await screen.findByText("lost"); page.unmount();
  const entry = savedRecords()[0]; const broken = JSON.stringify({ version: "v2_recovery.v1", value: { ...entry.value, workspaceID: "wrong-workspace" } }); localStorage.setItem(entry.key, broken);
  render(app()); fireEvent.click(screen.getByText("粘贴文件"));
  await waitFor(() => expect(screen.getByText(/本机恢复记录格式异常/)).toBeInTheDocument());
  expect(localStorage.getItem(entry.key)).toBe(broken); expect(paste).toHaveBeenCalledTimes(1); expect(inspect).not.toHaveBeenCalled();
});

it("an empty file-list response leaves ordinary text paste untouched and clears only its intent", async () => {
  const { paste } = native(); paste.mockResolvedValue(result({ status: "empty", images: [], attachments: [] }));
  const imported = vi.fn(); render(app("workspace-a", imported)); fireEvent.click(screen.getByText("粘贴文件"));
  await waitFor(() => expect(savedRecords()).toHaveLength(0)); expect(imported).not.toHaveBeenCalled();
});

it("captures the original draft callback before journaling and awaiting the native reply", async () => {
  const { paste } = native(); const pending = deferred<unknown>(); paste.mockReturnValue(pending.promise);
  const captured = vi.fn(), current = vi.fn();
  const capture = vi.fn(() => { expect(savedRecords()).toHaveLength(0); expect(paste).not.toHaveBeenCalled(); return captured; });
  render(<V2RecoveryProvider client={{ baseURL: "http://native-test/api/v1" }} scopeID="clipboard-db"><Harness onImported={current} captureImport={capture} /></V2RecoveryProvider>);
  fireEvent.click(screen.getByText("粘贴文件")); expect(capture).toHaveBeenCalledTimes(1);
  await act(async () => pending.resolve(result()));
  expect(captured).toHaveBeenCalledExactlyOnceWith([], [file]); expect(current).not.toHaveBeenCalled();
});

it("a late observation does not reset another window's already-added receipt marker", async () => {
  const { paste, inspect } = native(); paste.mockRejectedValue(new Error("lost"));
  const first = render(app()); fireEvent.click(screen.getByText("粘贴文件")); await screen.findByText("lost"); first.unmount();
  const pending = deferred<unknown>(); inspect.mockReturnValue(pending.promise); render(app());
  await waitFor(() => expect(inspect).toHaveBeenCalledTimes(1));
  const entry = savedRecords()[0];
  localStorage.setItem(entry.key, JSON.stringify({ version: "v2_recovery.v1", value: { ...entry.value, result: result({ status: "partial", batch_complete: false }), observation: true, added: true } }));
  await act(async () => pending.resolve(result({ status: "partial", batch_complete: false })));
  expect(savedRecords()[0].value.added).toBe(true);
  expect(screen.queryByText("加入已保存的附件")).not.toBeInTheDocument();
  expect(screen.getByText("已加入找到的附件，原批次是否完整仍待确认。核对附件列表后，可点击“结束本次粘贴核对”。")).toBeInTheDocument();
});

function render(ui: ReactElement, options?: RenderOptions) {
 localStorage.setItem("prayu.locale.v1", "zh-CN");
 const Wrapper=options?.wrapper;
 return renderComponent(ui, {...options, wrapper: ({children}) => <LocaleProvider>{Wrapper ? <Wrapper>{children}</Wrapper> : children}</LocaleProvider>});
}
