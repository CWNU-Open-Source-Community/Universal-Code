export async function readDesktopClipboardText(): Promise<string> {
  const read = window.runtime?.ClipboardGetText;
  if (!read) throw new Error("Desktop clipboard reading is unavailable");
  const text = await read();
  if (typeof text !== "string") throw new Error("Desktop clipboard returned invalid text");
  return text;
}

export async function writeDesktopClipboardText(text: string): Promise<void> {
  const write = window.runtime?.ClipboardSetText;
  if (!write) throw new Error("Desktop clipboard writing is unavailable");
  if (await write(text) !== true) throw new Error("Desktop clipboard did not confirm the copy");
}
