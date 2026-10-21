import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { V2KeyboardSettings } from "./keyboard-settings";
import { chatPreferencesKey, saveChatPreferences, useChatShortcuts } from "../chat-preferences";

afterEach(() => localStorage.removeItem(chatPreferencesKey));
function Commands({ search, findInChat = vi.fn() }: { search: () => void; findInChat?: () => void | boolean }) {
  useChatShortcuts({ search, findInChat, newChat: vi.fn(), settings: vi.fn(), sidebar: vi.fn(), composer: vi.fn() });
  return <V2KeyboardSettings />;
}

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
