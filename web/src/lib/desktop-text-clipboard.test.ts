import { readDesktopClipboardText, writeDesktopClipboardText } from "./desktop-text-clipboard";

afterEach(() => { Reflect.deleteProperty(window, "runtime"); });

it("uses native text clipboard methods and preserves whitespace and Unicode", async () => {
  const text = "  中文\r\nnext\t ";
  const read = vi.fn().mockResolvedValue(text);
  const write = vi.fn().mockResolvedValue(true);
  window.runtime = { ClipboardGetText: read, ClipboardSetText: write };
  expect(await readDesktopClipboardText()).toBe(text);
  await writeDesktopClipboardText(text);
  expect(read).toHaveBeenCalledExactlyOnceWith();
  expect(write).toHaveBeenCalledExactlyOnceWith(text);
});

it("reports absent native clipboard methods", async () => {
  await expect(readDesktopClipboardText()).rejects.toThrow("reading is unavailable");
  await expect(writeDesktopClipboardText("text")).rejects.toThrow("writing is unavailable");
});

it("rejects malformed reads and unconfirmed copies instead of reporting success", async () => {
  window.runtime = {
    ClipboardGetText: vi.fn().mockResolvedValue(null),
    ClipboardSetText: vi.fn().mockResolvedValue(false),
  };
  await expect(readDesktopClipboardText()).rejects.toThrow("invalid text");
  await expect(writeDesktopClipboardText("text")).rejects.toThrow("did not confirm");
});

it("preserves native clipboard errors", async () => {
  const error = new Error("clipboard is locked");
  window.runtime = {
    ClipboardGetText: vi.fn().mockRejectedValue(error),
    ClipboardSetText: vi.fn().mockRejectedValue(error),
  };
  await expect(readDesktopClipboardText()).rejects.toBe(error);
  await expect(writeDesktopClipboardText("text")).rejects.toBe(error);
});
