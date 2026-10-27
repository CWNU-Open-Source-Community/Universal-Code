import type { ComponentProps } from "react";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { APIClient } from "../../api/client";
import { LocaleProvider } from "../../lib/locale";
import { V2Composer } from "./composer";

afterEach(() => { cleanup(); localStorage.clear(); });

function setup(plainText = true, overrides: Partial<ComponentProps<typeof V2Composer>> = {}) {
  localStorage.setItem("prayu.locale.v1", "zh-CN");
  localStorage.setItem("universal-code.composer-plain-text", String(plainText));
  const onStop = vi.fn(() => true);
  const onSubmit = vi.fn(async () => undefined);
  const onDraftChange = vi.fn();
  const client = { hasThreadControl: true } as APIClient;
  render(<LocaleProvider><QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <V2Composer client={client} threadID="thread-stop" workspaceID="workspace-stop" workspaces={[]}
      onWorkspaceChange={vi.fn()} draft="尚未提交的草稿" onDraftChange={onDraftChange}
      onSubmit={onSubmit} onStop={onStop} {...overrides} />
  </QueryClientProvider></LocaleProvider>);
  return { onStop, onSubmit, onDraftChange, input: screen.getByRole("textbox", { name: "继续对话" }) };
}

it.each([true, false])("stops from the message input without submitting or clearing its draft (plain=%s)", (plainText) => {
  const { input, onStop, onSubmit, onDraftChange } = setup(plainText);
  act(() => input.focus());
  const escape = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
  fireEvent(input, escape);
  expect(onStop).toHaveBeenCalledOnce();
  expect(escape.defaultPrevented).toBe(true);
  expect(onSubmit).not.toHaveBeenCalled();
  expect(onDraftChange).not.toHaveBeenCalled();
  if (plainText) expect(input).toHaveValue("尚未提交的草稿");
  else expect(input).toHaveTextContent("尚未提交的草稿");
});

it("leaves composing, modified, repeated and previously handled Escape events alone", () => {
  const { input, onStop } = setup();
  for (const options of [{ isComposing: true }, { keyCode: 229 }, { repeat: true },
    { altKey: true }, { ctrlKey: true }, { metaKey: true }, { shiftKey: true }]) {
    const event = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true, ...options });
    fireEvent(input, event);
    expect(event.defaultPrevented).toBe(false);
  }
  const handled = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
  handled.preventDefault();
  fireEvent(input, handled);
  fireEvent.compositionStart(input);
  fireEvent.keyDown(input, { key: "Escape" });
  expect(onStop).not.toHaveBeenCalled();
  fireEvent.compositionEnd(input);
  fireEvent.keyDown(input, { key: "Escape" });
  expect(onStop).toHaveBeenCalledOnce();
});

it("takes precedence over a native message-editor Escape binding only when stopping is available", () => {
  const onStop = vi.fn(() => false);
  const { input } = setup(false, { onStop });
  // Tiptap consumes Escape to select a parent node in a real browser.
  const editorEscape = vi.fn((event: Event) => event.preventDefault());
  input.addEventListener("keydown", editorEscape);
  fireEvent.keyDown(input, { key: "Escape" });
  expect(onStop).toHaveBeenCalledOnce();
  expect(editorEscape).toHaveBeenCalledOnce();
  onStop.mockReturnValue(true);
  fireEvent.keyDown(input, { key: "Escape" });
  expect(onStop).toHaveBeenCalledTimes(2);
  expect(editorEscape).toHaveBeenCalledOnce();
});

it("does not take Escape from toolbar controls, other editors, or an unavailable stop action", () => {
  const unavailable = vi.fn(() => false);
  const { input } = setup(true, { onStop: unavailable });
  fireEvent.keyDown(screen.getByRole("button", { name: "添加附件" }), { key: "Escape" });
  fireEvent.keyDown(document.body, { key: "Escape" });
  const otherEditor = document.createElement("textarea");
  document.body.append(otherEditor);
  try { fireEvent.keyDown(otherEditor, { key: "Escape" }); } finally { otherEditor.remove(); }
  expect(unavailable).not.toHaveBeenCalled();
  const event = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
  fireEvent(input, event);
  expect(event.defaultPrevented).toBe(false);
  expect(unavailable).toHaveBeenCalledOnce();
});

it("keeps disabled input from stopping a run", () => {
  const { input, onStop } = setup(true, { disabled: true });
  fireEvent.keyDown(input, { key: "Escape" });
  expect(onStop).not.toHaveBeenCalled();
});

it("closes the attachment menu and project file dialog without stopping", async () => {
  const client = { hasThreadControl: true, hasEvidenceAttachment: true,
    workspaceExplore: vi.fn().mockRejectedValue(new Error("Workspace is unavailable")) } as unknown as APIClient;
  const { input, onStop } = setup(true, { client });
  const user = userEvent.setup();
  const add = screen.getByRole("button", { name: "添加附件" });
  await user.click(add);
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  expect(onStop).not.toHaveBeenCalled();
  await user.click(add);
  await user.click(screen.getByRole("menuitem", { name: "引用项目文件" }));
  expect(await screen.findByRole("dialog", { name: "选择项目文件" })).toBeInTheDocument();
  // Even a stale input target must leave an open dialog's Escape to its owner.
  fireEvent.keyDown(input, { key: "Escape" });
  expect(onStop).not.toHaveBeenCalled();
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(add).toHaveFocus();
  expect(input).toHaveValue("尚未提交的草稿");
});

it("leaves model selection Escape to the menu before allowing a later input Escape", async () => {
  const client = { hasThreadControl: true, hasModelControl: true,
    threadModelRoute: vi.fn().mockResolvedValue({ provider: "local", model: "model", source: "default" }),
    availableModelRoutes: vi.fn().mockResolvedValue({ generation: 1, routes: [] }) } as unknown as APIClient;
  const { input, onStop } = setup(true, { client, onManageModels: vi.fn() });
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: /模型路由，当前 local · model/u }));
  expect(screen.getByRole("menu", { name: "模型与响应设置" })).toBeInTheDocument();
  fireEvent.keyDown(input, { key: "Escape" });
  expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  expect(onStop).not.toHaveBeenCalled();
  act(() => input.focus());
  fireEvent.keyDown(input, { key: "Escape" });
  expect(onStop).toHaveBeenCalledOnce();
});
