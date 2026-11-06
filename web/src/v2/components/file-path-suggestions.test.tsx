import { useState } from "react";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { APIClient } from "../../api/client";
import type { WorkspaceExplorerView } from "../../api/types";
import { LocaleProvider } from "../../lib/locale";
import { MarkdownInput } from "./markdown-input";
import { V2Composer } from "./composer";
import { V2RecoveryProvider } from "../recovery-storage";

const digest = "a".repeat(64);
const snapshot = (path: string, kind: "directory" | "file", names: string[] = []): WorkspaceExplorerView => ({
  protocol_version: "workspace_explorer.v1", workspace_id: "workspace", path, kind, content: kind === "file" ? "proof" : "",
  entries: names.map((name) => ({ name, path: path === "." ? name : `${path}/${name}`,
    kind: name.includes(".") ? "file" : "directory", readable: true, size_bytes: 5 })),
  redaction_count: 0, truncated: false, root_path_exposed: false, total_bytes: 0, returned_bytes: 0,
  provenance: { version: "context_provenance.v1", source_kind: kind === "file" ? "workspace_file" : "workspace_listing",
    source_ref: path, content_sha256: digest, instruction_authorized: false },
});
const directory = (path: string) => path === "." ? snapshot(path, "directory", ["src", "资料 目录"])
  : path === "src" ? snapshot(path, "directory", ["component.ts", "context.ts"])
    : path === "资料 目录" ? snapshot(path, "directory", ["说明 文.md"]) : snapshot(path, "file");
const clientWith = (explore = vi.fn(async (_id: string, path: string, _signal?: AbortSignal) => directory(path))) =>
  ({ hasEvidenceAttachment: true, workspaceExplore: explore } as unknown as APIClient);

function Harness({ plain, client, scope = "one", attach = vi.fn(), submit = vi.fn() }: {
  plain: boolean; client: APIClient; scope?: string; attach?: (file: WorkspaceExplorerView) => void; submit?: () => void;
}) {
  const [value, setValue] = useState("");
  return <><MarkdownInput value={value} onChange={setValue} onSubmit={submit} plainText={plain} scope={scope}
    label="Message" placeholder="" disabled={false} inputRef={null}
    filePaths={{ client, workspaceID: "workspace", onAttach: (file, next) => { attach(file); setValue(next); } }} />
    <output data-testid="draft">{value}</output></>;
}
function wrap() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return ({ children }: { children: React.ReactNode }) => <LocaleProvider><QueryClientProvider client={queryClient}>
    {children}</QueryClientProvider></LocaleProvider>;
}
function enter(text: string, plain: boolean, caret = text.length) {
  const input = screen.getByRole("textbox");
  act(() => input.focus());
  if (plain) fireEvent.change(input, { target: { value: text, selectionStart: caret, selectionEnd: caret } });
  else fireEvent.paste(input, { clipboardData: { getData: (type: string) => type === "text/plain" ? text : "" } });
  return input;
}
beforeAll(() => Object.defineProperties(Range.prototype, {
  getClientRects: { configurable: true, value: () => [] },
  getBoundingClientRect: { configurable: true, value: () => new DOMRect() },
}));
beforeEach(() => { localStorage.clear(); localStorage.setItem("prayu.locale.v1", "zh-CN"); });
afterEach(cleanup);

it.each([true, false])("browses directories and attaches the real snapshot with Enter/Tab in plain mode %s", async (plain) => {
  const client = clientWith(); const attach = vi.fn(); const submit = vi.fn();
  render(<Harness plain={plain} client={client} attach={attach} submit={submit} />, { wrapper: wrap() });
  const input = enter("检查 @", plain);
  await screen.findByRole("option", { name: "资料 目录/" });
  fireEvent.keyDown(input, { key: "ArrowDown" });
  fireEvent.keyDown(input, { key: "Tab" });
  await screen.findByRole("option", { name: "说明 文.md" });
  expect(screen.getByTestId("draft")).toHaveTextContent('@"资料 目录/"');
  expect(client.workspaceExplore).toHaveBeenCalledTimes(2);
  fireEvent.keyDown(input, { key: "Enter" });
  await waitFor(() => expect(attach).toHaveBeenCalledWith(expect.objectContaining({ path: "资料 目录/说明 文.md",
    provenance: expect.objectContaining({ content_sha256: digest }) })));
  expect(screen.getByTestId("draft").textContent).toBe('检查 @"资料 目录/说明 文.md" ');
  expect(submit).not.toHaveBeenCalled();
  expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
});

it("filters only the requested directory, preserves surrounding text, and exposes truncation", async () => {
  const explore = vi.fn(async (_id: string, path: string) => ({ ...directory(path), truncated: true }));
  render(<Harness plain client={clientWith(explore)} />, { wrapper: wrap() });
  const input = enter("前文 @src/com 后文", true, "前文 @src/com".length);
  await screen.findByRole("option", { name: "component.ts" });
  expect(screen.queryByRole("option", { name: "context.ts" })).not.toBeInTheDocument();
  expect(explore).toHaveBeenCalledWith("workspace", "src", expect.any(AbortSignal));
  expect(screen.getByText(/仅列出部分目录条目/)).toBeVisible();
  fireEvent.keyDown(input, { key: "Enter" });
  await waitFor(() => expect(screen.getByTestId("draft")).toHaveTextContent("前文 @src/component.ts 后文"));
  expect((input as HTMLTextAreaElement).selectionStart).toBe("前文 @src/component.ts ".length);
});

it("keeps rich formatting and an ordinary local undo entry", async () => {
  render(<Harness plain={false} client={clientWith()} />, { wrapper: wrap() });
  const input = enter("**重点** @src/com", false);
  await screen.findByRole("option", { name: "component.ts" });
  fireEvent.keyDown(input, { key: "Enter" });
  await waitFor(() => expect(screen.getByTestId("draft")).toHaveTextContent("**重点** @src/component.ts"));
  expect(input.querySelector("strong")).toHaveTextContent("重点");
  fireEvent.keyDown(input, { key: "z", ctrlKey: true });
  await waitFor(() => expect(screen.getByTestId("draft").textContent).not.toContain("component.ts"));
});

it.each([true, false])("keeps file-read failures and ignores closed pending selections in plain mode %s", async (plain) => {
  let finish!: (file: WorkspaceExplorerView) => void;
  const explore = vi.fn((_id: string, path: string) => path.endsWith(".ts")
    ? new Promise<WorkspaceExplorerView>((resolve) => { finish = resolve; }) : Promise.resolve(directory(path)));
  const attach = vi.fn();
  render(<Harness plain={plain} client={clientWith(explore)} attach={attach} />, { wrapper: wrap() });
  const input = enter("检查 @src/com", plain);
  await screen.findByRole("option", { name: "component.ts" });
  fireEvent.keyDown(input, { key: "Enter" });
  await screen.findByText("正在读取文件…");
  fireEvent.keyDown(input, { key: "Escape" });
  fireEvent.select(input);
  fireEvent.keyUp(input, { key: "Escape" });
  await act(async () => finish(snapshot("src/component.ts", "file")));
  expect(attach).not.toHaveBeenCalled();
  expect(screen.getByTestId("draft")).toHaveTextContent("检查 @src/com");
  expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
});

it.each(["switch", "edit", "mode"])("rejects a late file response after %s", async (change) => {
  let finish!: (file: WorkspaceExplorerView) => void;
  const explore = vi.fn((_id: string, path: string) => path.endsWith(".ts")
    ? new Promise<WorkspaceExplorerView>((resolve) => { finish = resolve; }) : Promise.resolve(directory(path)));
  const client = clientWith(explore); const attach = vi.fn();
  const component = (scope: string, plain = true) => <Harness plain={plain} client={client} attach={attach} scope={scope} />;
  const view = render(component("one"), { wrapper: wrap() });
  const input = enter("@src/com", true);
  await screen.findByRole("option", { name: "component.ts" });
  fireEvent.keyDown(input, { key: "Enter" });
  await screen.findByText("正在读取文件…");
  if (change === "switch") { view.rerender(component("two")); view.rerender(component("one")); }
  else if (change === "mode") view.rerender(component("one", false));
  else enter("新的草稿", true);
  await act(async () => finish(snapshot("src/component.ts", "file")));
  expect(attach).not.toHaveBeenCalled();
});

it.each([true, false])("preserves drafts on read error and does not act during IME in plain mode %s", async (plain) => {
  const client = clientWith(vi.fn(async (_id, path) => {
    if (path.endsWith(".ts")) throw new Error("文件无法读取");
    return directory(path);
  }));
  const submit = vi.fn();
  render(<Harness plain={plain} client={client} submit={submit} />, { wrapper: wrap() });
  const input = enter("@src/com", plain);
  await screen.findByRole("option", { name: "component.ts" });
  fireEvent.keyDown(input, { key: "Enter", isComposing: true });
  expect(client.workspaceExplore).toHaveBeenCalledTimes(1);
  fireEvent.compositionStart(input);
  fireEvent.keyDown(input, { key: "Enter", keyCode: 229 });
  expect(submit).not.toHaveBeenCalled();
  expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  fireEvent.compositionEnd(input);
  if (!plain) { fireEvent.keyDown(input, { key: "a", ctrlKey: true }); enter("@src/com", false); }
  await screen.findByRole("option", { name: "component.ts" });
  fireEvent.keyDown(input, { key: "Enter" });
  await screen.findByRole("alert");
  expect(screen.getByTestId("draft")).toHaveTextContent("@src/com");
  expect(submit).not.toHaveBeenCalled();
});

it("uses the real composer's draft/reference chain and gives an open listbox Escape priority", async () => {
  localStorage.setItem("universal-code.composer-plain-text", "true");
  const submit = vi.fn(async () => undefined); const stop = vi.fn(() => true);
  render(<V2RecoveryProvider client={{ baseURL: "/api/v1" }} scopeID="path-test">
    <V2Composer client={clientWith()} workspaceID="workspace" threadID="thread" workspaces={[]}
      onSubmit={submit} onStop={stop} onWorkspaceChange={() => undefined} />
  </V2RecoveryProvider>, { wrapper: wrap() });
  const input = enter("检查 @src/com", true);
  await screen.findByRole("option", { name: "component.ts" });
  fireEvent.keyDown(input, { key: "Escape" });
  expect(stop).not.toHaveBeenCalled();
  fireEvent.keyDown(input, { key: "Escape" });
  expect(stop).toHaveBeenCalledOnce();
  enter("检查 @src/cont", true);
  await screen.findByRole("option", { name: "context.ts" });
  fireEvent.keyDown(input, { key: "Enter" });
  await screen.findByRole("button", { name: "移除引用 src/context.ts" });
  fireEvent.keyDown(input, { key: "Enter", ctrlKey: true });
  await waitFor(() => expect(submit).toHaveBeenCalledWith("检查 @src/context.ts",
    [expect.objectContaining({ path: "src/context.ts", digest })], [], expect.any(Object), []));
});

it("closes an old plain-text candidate when a restored draft replaces the value", async () => {
  const client = clientWith();
  const component = (value: string) => <MarkdownInput value={value} onChange={vi.fn()} onSubmit={vi.fn()}
    plainText scope="one" label="Message" placeholder="" disabled={false} inputRef={null}
    filePaths={{ client, workspaceID: "workspace", onAttach: vi.fn() }} />;
  const view = render(component("@src/com"), { wrapper: wrap() });
  const input = screen.getByRole("textbox") as HTMLTextAreaElement;
  act(() => { input.focus(); input.setSelectionRange(input.value.length, input.value.length); });
  fireEvent.select(input);
  await screen.findByRole("option", { name: "component.ts" });
  view.rerender(component("恢复的草稿"));
  expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  expect(input).toHaveValue("恢复的草稿");
});
