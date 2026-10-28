import { useState } from "react";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { TextSelection } from "@tiptap/pm/state";
import type { Editor } from "@tiptap/core";
import type { APIClient } from "../../api/client";
import type { WorkspaceExplorerView } from "../../api/types";
import { LocaleProvider } from "../../lib/locale";
import { V2RecoveryProvider } from "../recovery-storage";
import { useV2DraftDocument } from "../draft-context";
import { V2Composer } from "./composer";

vi.mock("./permission-control", () => ({ V2PermissionControl: () => null }));
const newest = "**完整输入**\n\n第二段：不要截断";
const attachment = { id: "file-1", workspace_id: "ws", name: "kept.txt", sha256: "a".repeat(64), byte_size: 12,
  mime_type: "text/plain", readability: "stored_only" as const, text_bytes: 0, redacted: false };
function setup(plainText: boolean, newestText = newest) {
  localStorage.setItem("universal-code.composer-plain-text", String(plainText));
  const getPage = vi.fn(async () => ({ items: ["old", "new"].map((id, index) => ({
    id, run_id: "run-1", run_ordinal: 1, sequence: index + 1, source: "operator", kind: "operator_input",
    activity_type: "message", detail: "preview", title: "User", durable: true, provisional: false,
    created_at: `2026-10-22T00:00:0${index + 1}Z`,
  })), page: { limit: 100 }, requestID: "1" }));
  const get = vi.fn(async (path: string) => ({ message_id: path.split("/").at(-1), content: path.endsWith("new") ? newestText : "Older input" }));
  const workspaceExplore = vi.fn(async (_workspace: string, path: string): Promise<WorkspaceExplorerView> => ({
    protocol_version: "workspace_explorer.v1", workspace_id: "ws", path, kind: path === "." ? "directory" : "file",
    content: path === "." ? "" : "Readme content", entries: path === "." ? [{ name: "README.md", path: "README.md",
      kind: "file", readable: true, size_bytes: 14 }] : [], redaction_count: 0, truncated: false,
    root_path_exposed: false, total_bytes: 14, returned_bytes: 14,
    provenance: { version: "context_provenance.v1", source_kind: path === "." ? "workspace_listing" : "workspace_file",
      source_ref: path, content_sha256: "b".repeat(64), instruction_authorized: false },
  }));
  const client = { get, getPage, workspaceExplore, baseURL: "/api/v1", hasThreadControl: true, hasEvidenceAttachment: true } as unknown as APIClient;
  const submit = vi.fn(async () => undefined);
  const stop = vi.fn(() => true);
  function Fixture() {
    const [thread, setThread] = useState("thread-a");
    const draft = useV2DraftDocument("ws", thread)!;
    return <><V2Composer client={client} threadID={thread} workspaceID="ws" workspaces={[]} onWorkspaceChange={() => {}} onSubmit={submit} onStop={stop} />
      <button onClick={() => draft.changeText("> 引用需要保留")}>引用文字</button>
      <button onClick={() => draft.document.update(draft.scope, { attachments: [attachment] }, draft.state.ref)}>加入测试附件</button>
      <button onClick={() => draft.document.update(draft.scope, { files: [{ id: "ref-1", path: "README.md", digest: "a".repeat(64), partial: false, redacted: false }] }, draft.state.ref)}>添加引用</button>
      <button onClick={() => setThread("thread-b")}>切换聊天</button>
      <output data-testid="draft">{draft.state.snapshot.text}</output>
      <output data-testid="draft-files">{JSON.stringify([draft.state.snapshot.files, draft.state.snapshot.attachments])}</output>
    </>;
  }
  render(<LocaleProvider><QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <V2RecoveryProvider client={client} scopeID="history-integration"><Fixture /></V2RecoveryProvider>
  </QueryClientProvider></LocaleProvider>);
  return { get, getPage, submit, stop, workspaceExplore };
}
const input = () => screen.getByRole("textbox", { name: "继续对话" });
function selection(from: "start" | "end" | number, to?: number) {
  const box = input();
  act(() => {
    box.focus();
    if (box instanceof HTMLTextAreaElement) {
      const position = from === "start" ? 0 : from === "end" ? box.value.length : from;
      box.setSelectionRange(position, to ?? position);
    } else {
      // Set the real editor selection; jsdom cannot perform native caret moves.
      const view = (box as HTMLElement & { editor: Editor }).editor.view;
      const doc = view.state.doc;
      const position = from === "start" ? TextSelection.atStart(doc).from : from === "end" ? TextSelection.atEnd(doc).from : from;
      view.dispatch(view.state.tr.setSelection(TextSelection.create(doc, position, to ?? position)));
    }
  });
}
function key(name: string, extra = {}) { return fireEvent.keyDown(input(), { key: name, ...extra }); }

beforeAll(() => Object.defineProperties(Range.prototype, {
  getClientRects: { configurable: true, value: () => [] },
  getBoundingClientRect: { configurable: true, value: () => new DOMRect() },
}));
beforeEach(() => { localStorage.clear(); localStorage.setItem("prayu.locale.v1", "zh-CN"); });
afterEach(cleanup);

it.each([true, false])("recalls complete saved prompts and restores the managed draft in plain mode %s", async (plain) => {
  const test = setup(plain);
  selection("start"); key("ArrowUp");
  await waitFor(() => expect(screen.getByTestId("draft").textContent).toBe(newest));
  expect(screen.getByText(/历史输入 1 \/ 2/)).toBeInTheDocument();
  if (!plain) expect(input().querySelector("strong")).toHaveTextContent("完整输入");
  selection("start"); key("ArrowUp");
  await waitFor(() => expect(screen.getByTestId("draft")).toHaveTextContent("Older input"));
  selection("end"); key("ArrowDown");
  await waitFor(() => expect(screen.getByTestId("draft").textContent).toBe(newest));
  selection("end"); key("ArrowDown");
  await waitFor(() => expect(screen.getByTestId("draft")).toBeEmptyDOMElement());
  expect(test.getPage).toHaveBeenCalledOnce(); expect(test.get).toHaveBeenCalledTimes(2);
  expect(test.submit).not.toHaveBeenCalled();
});

it.each([true, false])("leaves IME, modifiers, selections and multiline caret movement alone in plain mode %s", async (plain) => {
  const test = setup(plain);
  selection("start");
  key("ArrowUp", { isComposing: true }); key("ArrowUp", { keyCode: 229 });
  for (const modifier of ["shiftKey", "ctrlKey", "metaKey", "altKey"]) key("ArrowUp", { [modifier]: true });
  fireEvent.compositionStart(input()); key("ArrowUp");
  expect(test.getPage).not.toHaveBeenCalled();
  fireEvent.compositionEnd(input()); selection("start");
  // ProseMirror's Safari IME guard (jsdom's vendor) consumes the first key
  // immediately after compositionend. The next ordinary key may recall.
  key("ArrowUp");
  key("ArrowUp");
  await waitFor(() => expect(screen.getByTestId("draft").textContent).toBe(newest));
  selection(3); key("ArrowUp"); key("ArrowDown");
  selection(1, 3); key("ArrowUp"); key("ArrowDown");
  expect(screen.getByTestId("draft").textContent).toBe(newest); expect(test.get).toHaveBeenCalledOnce();
  // A manual edit leaves history. Down must never discard the changed text.
  if (plain) fireEvent.change(input(), { target: { value: "手工修改" } });
  else fireEvent.paste(input(), { clipboardData: { getData: (type: string) => type === "text/plain" ? "手工修改" : "" } });
  const edited = screen.getByTestId("draft").textContent;
  selection("end"); key("ArrowDown");
  expect(screen.getByTestId("draft").textContent).toBe(edited);
  expect(screen.queryByText(/历史输入 1 \/ 2/)).not.toBeInTheDocument();
});

it.each(["加入测试附件", "添加引用", "引用文字"])("preserves %s and does not enter history", (action) => {
  const test = setup(true);
  fireEvent.click(screen.getByRole("button", { name: action }));
  const files = screen.getByTestId("draft-files").textContent;
  const text = screen.getByTestId("draft").textContent;
  selection("start"); key("ArrowUp");
  expect(test.getPage).not.toHaveBeenCalled();
  expect(screen.getByTestId("draft-files").textContent).toBe(files);
  expect(screen.getByTestId("draft").textContent).toBe(text);
});

it.each([true, false])("gives file candidates priority over active history and then Escape stop in plain mode %s", async (plain) => {
  const test = setup(plain, "@R");
  selection("start"); key("ArrowUp");
  await waitFor(() => expect(screen.getByTestId("draft")).toHaveTextContent("@R"));
  if (!plain) expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  selection("start");
  selection("end");
  if (plain) fireEvent.select(input());
  await screen.findByRole("option", { name: "README.md" });
  // Down at the end would restore the empty pre-history draft if history won.
  key("ArrowDown"); key("ArrowUp");
  expect(screen.getByTestId("draft")).toHaveTextContent("@R");
  key("Enter");
  await screen.findByRole("button", { name: "移除引用 README.md" });
  expect(screen.getByTestId("draft")).toHaveTextContent("@README.md");
  expect(test.get).toHaveBeenCalledOnce(); expect(test.getPage).toHaveBeenCalledOnce();
  expect(test.submit).not.toHaveBeenCalled();

  selection("end");
  if (plain) fireEvent.change(input(), { target: { value: "保留引用 @R" } });
  else fireEvent.paste(input(), { clipboardData: { getData: (type: string) => type === "text/plain" ? " @R" : "" } });
  await screen.findByRole("option", { name: "README.md" });
  const draft = screen.getByTestId("draft").textContent;
  const files = screen.getByTestId("draft-files").textContent;
  key("Escape");
  expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  expect(test.stop).not.toHaveBeenCalled();
  key("Escape");
  expect(test.stop).toHaveBeenCalledOnce();
  expect(screen.getByTestId("draft").textContent).toBe(draft);
  expect(screen.getByTestId("draft-files").textContent).toBe(files);
  expect(test.submit).not.toHaveBeenCalled();
});
