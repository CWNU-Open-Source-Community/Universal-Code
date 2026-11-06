interface TerminalClipboardOptions {
  platform: "windows" | "macos" | "other";
  clipboard: { readText: () => Promise<string>; writeText: (text: string) => Promise<void> };
  getSelection: () => string;
  capturePaste: () => ((text: string) => void) | null;
  onError: (action: "copy" | "paste", error: unknown) => void;
}

export function createTerminalClipboardKeyHandler(options: TerminalClipboardOptions) {
  return (event: KeyboardEvent): boolean => {
    if (event.type !== "keydown" || event.isComposing || event.keyCode === 229 ||
        options.platform === "macos" || event.altKey || event.metaKey) return true;
    const key = event.key.toLowerCase();
    const copyKey = event.ctrlKey && (key === "c" || key === "insert" && !event.shiftKey);
    const selection = copyKey ? options.getSelection() : "";
    const copy = copyKey && (key === "insert" || event.shiftKey ||
      options.platform === "windows" && selection.length > 0);
    const paste = event.ctrlKey && key === "v" && (event.shiftKey || options.platform === "windows") ||
      !event.ctrlKey && event.shiftKey && key === "insert";
    if (!copy && !paste) return true;
    event.preventDefault();
    event.stopPropagation();
    // A held paste shortcut must not fall through to xterm's control character.
    if (event.repeat) return false;
    if (copy) {
      if (selection.length > 0) {
        void options.clipboard.writeText(selection).catch((error) => options.onError("copy", error));
      }
    } else {
      const applyPaste = options.capturePaste();
      if (!applyPaste) {
        options.onError("paste", new Error("The terminal is not running"));
        return false;
      }
      void options.clipboard.readText().then((text) => {
        if (text.length > 0) applyPaste(text);
      }).catch((error) => options.onError("paste", error));
    }
    return false;
  };
}
