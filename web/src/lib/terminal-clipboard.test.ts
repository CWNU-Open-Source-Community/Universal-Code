import { createTerminalClipboardKeyHandler } from "./terminal-clipboard";

function fixture(platform: "windows" | "macos" | "other" = "windows", selection = "中文\n  output ") {
  const readText = vi.fn().mockResolvedValue("  中文\r\nnext ");
  const writeText = vi.fn().mockResolvedValue(undefined);
  const paste = vi.fn();
  const capturePaste = vi.fn<() => ((text: string) => void) | null>(() => paste);
  const onError = vi.fn();
  const handle = createTerminalClipboardKeyHandler({
    platform, clipboard: { readText, writeText }, getSelection: () => selection, capturePaste, onError,
  });
  return { handle, readText, writeText, paste, capturePaste, onError };
}

function key(key: string, options: KeyboardEventInit = {}, type = "keydown") {
  return new KeyboardEvent(type, { key, ctrlKey: true, bubbles: true, cancelable: true, ...options });
}

it.each([
  ["c", {}], ["C", { shiftKey: true }], ["Insert", {}],
] as const)("copies the exact selection with %s %j without pasting or forwarding the key", async (name, modifiers) => {
  const f = fixture();
  const event = key(name, modifiers);
  expect(f.handle(event)).toBe(false);
  expect(event.defaultPrevented).toBe(true);
  await Promise.resolve();
  expect(f.writeText).toHaveBeenCalledExactlyOnceWith("中文\n  output ");
  expect(f.readText).not.toHaveBeenCalled();
  expect(f.capturePaste).not.toHaveBeenCalled();
});

it("keeps unselected Ctrl+C available for interruption and does not clear the clipboard", () => {
  const f = fixture("windows", "");
  const event = key("c");
  expect(f.handle(event)).toBe(true);
  expect(event.defaultPrevented).toBe(false);
  expect(f.writeText).not.toHaveBeenCalled();
  expect(f.handle(key("C", { shiftKey: true }))).toBe(false);
  expect(f.writeText).not.toHaveBeenCalled();
});

it.each([
  ["v", {}], ["V", { shiftKey: true }], ["Insert", { ctrlKey: false, shiftKey: true }],
] as const)("routes %s %j through the captured paste action exactly once", async (name, modifiers) => {
  const f = fixture();
  const event = key(name, modifiers);
  expect(f.handle(event)).toBe(false);
  expect(event.defaultPrevented).toBe(true);
  await Promise.resolve();
  expect(f.capturePaste).toHaveBeenCalledOnce();
  expect(f.readText).toHaveBeenCalledOnce();
  expect(f.paste).toHaveBeenCalledExactlyOnceWith("  中文\r\nnext ");
  expect(f.writeText).not.toHaveBeenCalled();
});

it.each(["", "selected output"])("keeps Linux Ctrl+C available for interruption with selection %j", (selection) => {
  const f = fixture("other", selection);
  const event = key("c");
  expect(f.handle(event)).toBe(true);
  expect(event.defaultPrevented).toBe(false);
  expect(f.writeText).not.toHaveBeenCalled();
  expect(f.readText).not.toHaveBeenCalled();
});

it("keeps Linux Ctrl+V as terminal input while supporting explicit clipboard shortcuts", async () => {
  const f = fixture("other");
  expect(f.handle(key("v"))).toBe(true);
  expect(f.readText).not.toHaveBeenCalled();
  expect(f.handle(key("C", { shiftKey: true }))).toBe(false);
  expect(f.handle(key("Insert"))).toBe(false);
  expect(f.handle(key("V", { shiftKey: true }))).toBe(false);
  await Promise.resolve();
  expect(f.writeText).toHaveBeenCalledTimes(2);
  expect(f.paste).toHaveBeenCalledOnce();
});

it.each([
  ["c", { metaKey: true, ctrlKey: false }], ["v", { metaKey: true, ctrlKey: false }],
  ["c", {}], ["C", { shiftKey: true }], ["V", { shiftKey: true }],
] as const)("leaves macOS %s %j to native editing and xterm", (name, modifiers) => {
  const f = fixture("macos");
  expect(f.handle(key(name, modifiers))).toBe(true);
  expect(f.writeText).not.toHaveBeenCalled();
  expect(f.readText).not.toHaveBeenCalled();
});

it.each([
  { altKey: true }, { metaKey: true }, { isComposing: true }, { keyCode: 229 },
])("does not intercept IME or extra modifiers: %j", (modifiers) => {
  const f = fixture();
  expect(f.handle(key("v", modifiers))).toBe(true);
  expect(f.readText).not.toHaveBeenCalled();
});

it("consumes repeated clipboard shortcuts without repeating the operation or leaking a control character", () => {
  const f = fixture();
  expect(f.handle(key("v", { repeat: true }))).toBe(false);
  expect(f.handle(key("c", { repeat: true }))).toBe(false);
  expect(f.readText).not.toHaveBeenCalled();
  expect(f.writeText).not.toHaveBeenCalled();
  expect(f.handle(key("v", {}, "keyup"))).toBe(true);
});

it("reports failures without falling through to xterm or pasting anything", async () => {
  const f = fixture();
  const error = new Error("clipboard unavailable");
  f.writeText.mockRejectedValue(error);
  f.readText.mockRejectedValue(error);
  expect(f.handle(key("c"))).toBe(false);
  expect(f.handle(key("v"))).toBe(false);
  await vi.waitFor(() => expect(f.onError).toHaveBeenCalledTimes(2));
  expect(f.onError).toHaveBeenCalledWith("copy", error);
  expect(f.onError).toHaveBeenCalledWith("paste", error);
  expect(f.paste).not.toHaveBeenCalled();
});

it("does not read for an unavailable terminal or paste an empty clipboard", async () => {
  const f = fixture();
  f.capturePaste.mockReturnValueOnce(null);
  expect(f.handle(key("v"))).toBe(false);
  expect(f.readText).not.toHaveBeenCalled();
  expect(f.onError).toHaveBeenCalledWith("paste", expect.objectContaining({ message: "The terminal is not running" }));
  f.readText.mockResolvedValue("");
  expect(f.handle(key("v"))).toBe(false);
  await Promise.resolve();
  expect(f.paste).not.toHaveBeenCalled();
});
