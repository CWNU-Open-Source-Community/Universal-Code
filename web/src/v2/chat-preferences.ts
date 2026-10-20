import { useEffect, useMemo, useRef, useSyncExternalStore } from "react";

export const chatPreferencesKey = "universal-code.chat-preferences.v1";
const changed = "universal-code:chat-preferences";
export type SendPreference = "enter" | "multiline" | "modifier";
export const shortcutCommands = [
  { id: "newChat", zh: "新对话", en: "New chat", binding: "Mod+Shift+O" },
  { id: "search", zh: "搜索聊天", en: "Search chats", binding: "Mod+K" },
  { id: "settings", zh: "打开设置", en: "Open settings", binding: "Mod+," },
  { id: "sidebar", zh: "展开／收起侧栏", en: "Toggle sidebar", binding: "Mod+B" },
  { id: "composer", zh: "聚焦输入框", en: "Focus composer", binding: "Mod+Shift+I" },
] as const;
export type ShortcutCommand = typeof shortcutCommands[number]["id"];
export interface ChatPreferences { send: SendPreference; shortcuts: Partial<Record<ShortcutCommand, string>> }
const defaults: ChatPreferences = { send: "enter", shortcuts: {} };
const read = () => { try { return localStorage.getItem(chatPreferencesKey); } catch { return null; } };
const subscribe = (notify: () => void) => {
  const storage = (event: StorageEvent) => { if (!event.key || event.key === chatPreferencesKey) notify(); };
  window.addEventListener(changed, notify); window.addEventListener("storage", storage);
  return () => { window.removeEventListener(changed, notify); window.removeEventListener("storage", storage); };
};
function parse(raw: string | null): ChatPreferences {
  try {
    const value = JSON.parse(raw ?? "null");
    if (!value || typeof value !== "object") return defaults;
    const shortcuts: ChatPreferences["shortcuts"] = {};
    for (const { id } of shortcutCommands) {
      const binding = value.shortcuts?.[id];
      if (binding === "" || (typeof binding === "string" && validShortcut(binding))) shortcuts[id] = binding;
    }
    return { send: ["enter", "multiline", "modifier"].includes(value.send) ? value.send : "enter", shortcuts };
  } catch { return defaults; }
}
export function useChatPreferences() {
  const raw = useSyncExternalStore(subscribe, read, () => null);
  return useMemo(() => parse(raw), [raw]);
}
// Persist before notifying: a quota failure must not look like a saved preference.
export function saveChatPreferences(patch: Partial<ChatPreferences>) {
  const next = { ...parse(read()), ...patch };
  localStorage.setItem(chatPreferencesKey, JSON.stringify(next));
  window.dispatchEvent(new Event(changed));
}
export function sendsOnEnter(mode: SendPreference, text: string, modified: boolean, structured = false) {
  return modified || (!structured && mode !== "modifier" && (mode !== "multiline" || !text.includes("\n")));
}
export function shortcutBinding(preferences: ChatPreferences, id: ShortcutCommand) {
  return preferences.shortcuts[id] ?? shortcutCommands.find((command) => command.id === id)!.binding;
}
export function shortcutLabel(binding: string) {
  return binding.replace("Mod", /Mac|iPhone|iPad/u.test(navigator.platform) ? "⌘" : "Ctrl");
}
export function eventShortcut(event: Pick<KeyboardEvent, "key" | "code" | "ctrlKey" | "metaKey" | "altKey" | "shiftKey">): string {
  if ((!event.ctrlKey && !event.metaKey) || (event.ctrlKey && event.metaKey)) return "";
  const key = /^Key[A-Z]$/u.test(event.code) ? event.code.slice(3)
    : /^Digit\d$/u.test(event.code) ? event.code.slice(5) : event.key.toUpperCase();
  if (!/^[A-Z0-9,./;[\]\\'=-]$/u.test(key)) return "";
  return ["Mod", ...(event.altKey ? ["Alt"] : []), ...(event.shiftKey ? ["Shift"] : []), key].join("+");
}
export function validShortcut(binding: string) {
  if (!/^Mod\+(?:Alt\+)?(?:Shift\+)?[A-Z0-9,./;[\]\\'=-]$/u.test(binding)) return false;
  // Keep native editing, browser/window controls and zoom available.
  return !["Mod+A", "Mod+C", "Mod+V", "Mod+X", "Mod+Z", "Mod+Y", "Mod+S", "Mod+F", "Mod+L",
    "Mod+R", "Mod+W", "Mod+T", "Mod+N", "Mod+Q", "Mod+0", "Mod+=", "Mod+-", "Mod+Shift+Z",
    "Mod+Shift+V", "Mod+Shift+T", "Mod+Shift+N", "Mod+Shift+W", "Mod+Shift+R"].includes(binding);
}

export function useChatShortcuts(actions: Record<ShortcutCommand, () => void>) {
  const preferences = useChatPreferences();
  const latest = useRef({ actions, preferences });
  latest.current = { actions, preferences };
  useEffect(() => {
    const keydown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.repeat || event.isComposing || event.keyCode === 229 ||
        (event.target instanceof Element && event.target.closest("[data-shortcut-recorder]")) ||
        document.querySelector('[role="dialog"][aria-modal="true"]')) return;
      const binding = eventShortcut(event);
      if (!binding) return;
      const command = shortcutCommands.find(({ id }) => shortcutBinding(latest.current.preferences, id) === binding);
      if (!command) return;
      event.preventDefault();
      latest.current.actions[command.id]();
    };
    window.addEventListener("keydown", keydown);
    return () => window.removeEventListener("keydown", keydown);
  }, []);
}
