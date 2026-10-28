import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { V2KeyboardSettings } from "./keyboard-settings";
import { chatPreferencesKey, saveChatPreferences, useChatShortcuts } from "../chat-preferences";

afterEach(() => localStorage.removeItem(chatPreferencesKey));
function Commands({ search, findInChat = vi.fn(), previousChat = vi.fn(), nextChat = vi.fn(), settings = vi.fn() }: {
  search: () => void; findInChat?: () => void | boolean; previousChat?: () => void | boolean; nextChat?: () => void | boolean; settings?: () => void;
}) {
  useChatShortcuts({ search, findInChat, previousChat, nextChat, newChat: vi.fn(), settings, sidebar: vi.fn(), composer: vi.fn() });
  return <V2KeyboardSettings />;
}

function ModelShortcut({ action }: { action: () => void }) {
  useChatShortcuts({ modelPicker: action });
  return null;
}

it("leaves unregistered model shortcuts available and dispatches to a local registration", () => {
  const modelPicker = vi.fn();
  const view = render(<Commands search={vi.fn()} />);
  const key = { key: "m", code: "KeyM", ctrlKey: true, shiftKey: true, cancelable: true };
  const unregistered = new KeyboardEvent("keydown", key);
  fireEvent(window, unregistered);
  expect(unregistered.defaultPrevented).toBe(false);
  expect(screen.getByRole("button", { name: "Change shortcut for Open model picker" })).toHaveTextContent("Ctrl+Shift+M");
  view.rerender(<><Commands search={vi.fn()} /><ModelShortcut action={modelPicker} /></>);
  const registered = new KeyboardEvent("keydown", key);
  fireEvent(window, registered);
  expect(registered.defaultPrevented).toBe(true);
  expect(modelPicker).toHaveBeenCalledOnce();
});

it("records, persists and clears the model picker shortcut without activating it during recording", () => {
  const modelPicker = vi.fn();
  const view = render(<><Commands search={vi.fn()} /><ModelShortcut action={modelPicker} /></>);
  const record = screen.getByRole("button", { name: "Change shortcut for Open model picker" });
  fireEvent.click(record);
  fireEvent.keyDown(record, { key: "j", code: "KeyJ", ctrlKey: true, shiftKey: true });
  expect(modelPicker).not.toHaveBeenCalled();
  expect(localStorage.getItem(chatPreferencesKey)).toContain('"modelPicker":"Mod+Shift+J"');
  view.unmount();
  render(<><Commands search={vi.fn()} /><ModelShortcut action={modelPicker} /></>);
  const saved = screen.getByRole("button", { name: "Change shortcut for Open model picker" });
  expect(saved).toHaveTextContent("Ctrl+Shift+J");
  fireEvent.keyDown(window, { key: "m", code: "KeyM", ctrlKey: true, shiftKey: true });
  expect(modelPicker).not.toHaveBeenCalled();
  fireEvent.keyDown(window, { key: "j", code: "KeyJ", ctrlKey: true, shiftKey: true });
  expect(modelPicker).toHaveBeenCalledOnce();
  fireEvent.click(saved);
  fireEvent.keyDown(saved, { key: "Backspace" });
  expect(saved).toHaveTextContent("Unassigned");
  fireEvent.keyDown(window, { key: "j", code: "KeyJ", ctrlKey: true, shiftKey: true });
  expect(modelPicker).toHaveBeenCalledOnce();
});

it("preserves an older custom binding that uses the new model picker default", () => {
  saveChatPreferences({ shortcuts: { settings: "Mod+Shift+M" } });
  const settings = vi.fn();
  const modelPicker = vi.fn();
  render(<><Commands search={vi.fn()} settings={settings} /><ModelShortcut action={modelPicker} /></>);
  expect(screen.getByRole("button", { name: "Change shortcut for Open model picker" })).toHaveTextContent("Unassigned");
  fireEvent.keyDown(window, { key: "m", code: "KeyM", ctrlKey: true, shiftKey: true });
  expect(settings).toHaveBeenCalledOnce();
  expect(modelPicker).not.toHaveBeenCalled();
});

it("records, searches, persists and resets shortcuts without invoking actions while recording", () => {
  const search = vi.fn();
  const view = render(<Commands search={search} />);
  const record = screen.getByRole("button", { name: "Change shortcut for Search chats" });
  fireEvent.click(record);
  expect(screen.getByRole("textbox", { name: "Search shortcuts" })).not.toHaveAttribute("readonly");
  fireEvent.keyDown(record, { key: "j", code: "KeyJ", ctrlKey: true, shiftKey: true });
  expect(search).not.toHaveBeenCalled();
  expect(localStorage.getItem(chatPreferencesKey)).toContain("Mod+Shift+J");
  fireEvent.keyDown(window, { key: "k", code: "KeyK", ctrlKey: true });
  expect(search).not.toHaveBeenCalled();
  fireEvent.keyDown(window, { key: "j", code: "KeyJ", ctrlKey: true, shiftKey: true });
  expect(search).toHaveBeenCalledOnce();
  view.unmount();
  render(<Commands search={search} />);
  expect(screen.getByRole("button", { name: "Change shortcut for Search chats" })).toHaveTextContent("Ctrl+Shift+J");
  fireEvent.change(screen.getByRole("textbox", { name: "Search shortcuts" }), { target: { value: "Ctrl+Shift+J" } });
  expect(screen.queryByRole("button", { name: "Change shortcut for New chat" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Reset all shortcuts" }));
  fireEvent.keyDown(window, { key: "k", code: "KeyK", ctrlKey: true });
  expect(search).toHaveBeenCalledTimes(2);
  fireEvent.click(screen.getByRole("button", { name: "Record keys" }));
  fireEvent.keyDown(screen.getByRole("textbox", { name: "Search shortcuts" }), { key: "b", code: "KeyB", ctrlKey: true });
  expect(screen.getByRole("textbox", { name: "Search shortcuts" })).toHaveValue("Ctrl+B");
  expect(screen.getByRole("button", { name: "Change shortcut for Toggle sidebar" })).toBeInTheDocument();
});

it("rejects editing and duplicate bindings, and ignores IME, repeats and modal dialogs", () => {
  const search = vi.fn();
  render(<Commands search={search} />);
  const record = screen.getByRole("button", { name: "Change shortcut for Search chats" });
  fireEvent.click(record);
  fireEvent.keyDown(record, { key: "c", code: "KeyC", ctrlKey: true });
  expect(screen.getByRole("alert")).toHaveTextContent("reserved");
  fireEvent.keyDown(record, { key: "b", code: "KeyB", ctrlKey: true });
  expect(screen.getByRole("alert")).toHaveTextContent("Already used");
  fireEvent.keyDown(record, { key: "Escape" });
  fireEvent.keyDown(window, { key: "k", code: "KeyK", ctrlKey: true, isComposing: true });
  fireEvent.keyDown(window, { key: "k", code: "KeyK", ctrlKey: true, repeat: true });
  const modal = document.createElement("div"); modal.setAttribute("role", "dialog"); modal.setAttribute("aria-modal", "true"); document.body.append(modal);
  fireEvent.keyDown(window, { key: "k", code: "KeyK", ctrlKey: true });
  modal.remove();
  expect(search).not.toHaveBeenCalled();
  act(() => saveChatPreferences({ shortcuts: { search: "Mod+J" } }));
  fireEvent.keyDown(window, { key: "j", code: "KeyJ", metaKey: true });
  expect(search).toHaveBeenCalledOnce();
});

it("persists the current-chat find binding and yields to native find outside its context or in editors", () => {
  const findInChat = vi.fn<() => boolean>().mockReturnValue(false);
  const view = render(<Commands search={vi.fn()} findInChat={findInChat} />);
  const unavailable = new KeyboardEvent("keydown", { key: "f", code: "KeyF", ctrlKey: true, cancelable: true });
  fireEvent(window, unavailable);
  expect(unavailable.defaultPrevented).toBe(false);
  findInChat.mockReturnValue(true);
  const available = new KeyboardEvent("keydown", { key: "f", code: "KeyF", metaKey: true, cancelable: true });
  fireEvent(window, available);
  expect(available.defaultPrevented).toBe(true);
  for (const className of ["monaco-editor", "xterm"]) {
    const editor = document.createElement("input"); editor.className = className; document.body.append(editor);
    fireEvent.keyDown(editor, { key: "f", code: "KeyF", ctrlKey: true });
    editor.remove();
  }
  expect(findInChat).toHaveBeenCalledTimes(2);
  const record = screen.getByRole("button", { name: "Change shortcut for Find in current chat" });
  fireEvent.click(record);
  fireEvent.keyDown(record, { key: "f", code: "KeyF", ctrlKey: true });
  expect(localStorage.getItem(chatPreferencesKey)).toContain('"findInChat":"Mod+F"');
  view.unmount();
  render(<Commands search={vi.fn()} findInChat={findInChat} />);
  expect(screen.getByRole("button", { name: "Change shortcut for Find in current chat" })).toHaveTextContent("Ctrl+F");
  fireEvent.keyDown(window, { key: "f", code: "KeyF", ctrlKey: true });
  expect(findInChat).toHaveBeenCalledTimes(3);
});

it("uses physical bracket keys with Ctrl or Meta and preserves a remapped chat shortcut", () => {
  const previousChat = vi.fn();
  const nextChat = vi.fn<() => boolean>().mockReturnValue(true);
  const view = render(<Commands search={vi.fn()} previousChat={previousChat} nextChat={nextChat} />);
  fireEvent.keyDown(window, { key: "{", code: "BracketLeft", ctrlKey: true, shiftKey: true });
  fireEvent.keyDown(window, { key: "}", code: "BracketRight", metaKey: true, shiftKey: true });
  expect(previousChat).toHaveBeenCalledOnce();
  expect(nextChat).toHaveBeenCalledOnce();

  const record = screen.getByRole("button", { name: "Change shortcut for Next chat" });
  fireEvent.click(record);
  fireEvent.keyDown(record, { key: "}", code: "BracketRight", ctrlKey: true, shiftKey: true });
  expect(localStorage.getItem(chatPreferencesKey)).toContain('"nextChat":"Mod+Shift+]"');
  expect(nextChat).toHaveBeenCalledOnce();
  fireEvent.click(record);
  fireEvent.keyDown(record, { key: "u", code: "KeyU", ctrlKey: true, shiftKey: true });
  view.unmount();
  render(<Commands search={vi.fn()} nextChat={nextChat} />);
  expect(screen.getByRole("button", { name: "Change shortcut for Next chat" })).toHaveTextContent("Ctrl+Shift+U");
  fireEvent.keyDown(window, { key: "}", code: "BracketRight", ctrlKey: true, shiftKey: true });
  expect(nextChat).toHaveBeenCalledOnce();
  fireEvent.keyDown(window, { key: "u", code: "KeyU", ctrlKey: true, shiftKey: true });
  expect(nextChat).toHaveBeenCalledTimes(2);

  nextChat.mockReturnValue(false);
  const unavailable = new KeyboardEvent("keydown", { key: "u", code: "KeyU", ctrlKey: true, shiftKey: true, cancelable: true });
  fireEvent(window, unavailable);
  expect(unavailable.defaultPrevented).toBe(false);
});

it("does not switch chats from editors, terminals, popovers, IME or repeated keys", () => {
  const nextChat = vi.fn();
  render(<Commands search={vi.fn()} nextChat={nextChat} />);
  const key = { key: "}", code: "BracketRight", ctrlKey: true, shiftKey: true };
  for (const className of ["monaco-editor", "xterm"]) {
    const editor = document.createElement("input"); editor.className = className; document.body.append(editor);
    fireEvent.keyDown(editor, key);
    editor.remove();
  }
  for (const role of ["dialog", "menu", "listbox"]) {
    const popup = document.createElement("div"); popup.setAttribute("role", role); document.body.append(popup);
    fireEvent.keyDown(window, key);
    popup.remove();
  }
  const projectMenu = document.createElement("details");
  projectMenu.className = "v2-project-section-menu"; projectMenu.open = true; document.body.append(projectMenu);
  fireEvent.keyDown(window, key);
  projectMenu.remove();
  fireEvent.keyDown(window, { ...key, isComposing: true });
  fireEvent.keyDown(window, { ...key, keyCode: 229 });
  fireEvent.keyDown(window, { ...key, repeat: true });
  expect(nextChat).not.toHaveBeenCalled();
  // Expanded message details are content, not a navigation-blocking popup.
  const details = document.createElement("details"); details.open = true; document.body.append(details);
  fireEvent.keyDown(window, key);
  details.remove();
  expect(nextChat).toHaveBeenCalledOnce();
});

it("keeps an existing custom bracket binding when new navigation defaults are introduced", () => {
  saveChatPreferences({ shortcuts: { settings: "Mod+Shift+]" } });
  const settings = vi.fn();
  const nextChat = vi.fn();
  render(<Commands search={vi.fn()} settings={settings} nextChat={nextChat} />);
  expect(screen.getByRole("button", { name: "Change shortcut for Next chat" })).toHaveTextContent("Unassigned");
  fireEvent.keyDown(window, { key: "}", code: "BracketRight", ctrlKey: true, shiftKey: true });
  expect(settings).toHaveBeenCalledOnce();
  expect(nextChat).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Reset all shortcuts" }));
  expect(screen.getByRole("button", { name: "Change shortcut for Next chat" })).toHaveTextContent("Ctrl+Shift+]");
  fireEvent.keyDown(window, { key: "}", code: "BracketRight", ctrlKey: true, shiftKey: true });
  expect(nextChat).toHaveBeenCalledOnce();
});
