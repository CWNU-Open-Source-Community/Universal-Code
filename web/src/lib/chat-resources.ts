import { desktopBridgeAvailable, desktopErrorMessage } from "./desktop-bridge";

export const chatResourceVersion = "desktop_chat_resource.v1";
export interface ChatResourceRef {
  workspace_id: string;
  run_id?: string;
  path?: string;
  attachment_id?: string;
  image_id?: string;
}
export interface ChatResourceView { version: string; name: string; path: string; relative_path: string; mime_type: string; data_base64: string }
export interface LinkPreview { url: string; title: string; description: string; image: string }
export type ResourceAction = "copy_file" | "copy_path" | "copy_content" | "save_as" | "open" | "open_with" | "reveal";

export function webURL(raw: string): URL | null {
  if (raw !== raw.trim() || /[\u0000-\u0020\u007f\\]/u.test(raw) || raw.length > 8192) return null;
  try {
    const url = new URL(raw.startsWith("//") ? `https:${raw}` : raw);
    return ["https:", "http:"].includes(url.protocol) && !url.username && !url.password ? url : null;
  } catch { return null; }
}

export async function openWebURL(raw: string, disposition: "browser" | "tab" | "window" = "browser"): Promise<void> {
  const url = webURL(raw);
  if (!url) throw new Error("只能打开 HTTP 或 HTTPS 网页链接。");
  if (desktopBridgeAvailable()) {
    const open = window.go?.desktop?.DesktopBridge?.OpenExternalURL;
    if (!open) throw new Error("当前桌面版本不支持打开外链，请更新应用。");
    try { await open(url.href); } catch (error) { throw new Error(desktopErrorMessage(error)); }
  } else {
    window.open(url.href, "_blank", disposition === "window" ? "noopener,noreferrer,popup" : "noopener,noreferrer");
  }
}

export const nativeResourcesAvailable = () => typeof window.go?.desktop?.DesktopBridge?.ChatResourceAction === "function";
export async function resourceAction(ref: ChatResourceRef, action: ResourceAction): Promise<string> {
  const act = window.go?.desktop?.DesktopBridge?.ChatResourceAction;
  if (!act) throw new Error("此操作需要桌面应用。");
  try {
    const result = await act({ ...ref, version: chatResourceVersion, action });
    if (result !== "done" && result !== "cancelled") throw new Error("文件操作未返回结果。");
    return result;
  } catch (error) { throw new Error(desktopErrorMessage(error)); }
}
export async function readChatResource(ref: ChatResourceRef): Promise<ChatResourceView> {
  const read = window.go?.desktop?.DesktopBridge?.ReadChatResource;
  if (!read) throw new Error("此文件操作需要桌面应用。");
  try {
    const value = await read({ ...ref, version: chatResourceVersion }) as ChatResourceView;
    if (value?.version !== chatResourceVersion || ![value.name, value.path, value.relative_path, value.mime_type, value.data_base64].every((v) => typeof v === "string") || value.data_base64.length > 7 * 1024 * 1024) throw new Error("文件读取结果无效。");
    return value;
  } catch (error) { throw new Error(desktopErrorMessage(error)); }
}
export async function previewWebURL(url: string): Promise<LinkPreview> {
  const read = window.go?.desktop?.DesktopBridge?.PreviewExternalURL;
  if (!read) return { url, title: "", description: "", image: "" };
  try {
    const value = await read(url) as LinkPreview;
    if (!value || value.url !== url || ![value.title, value.description, value.image].every((v) => typeof v === "string") ||
      value.title.length > 1000 || value.description.length > 2000 || value.image.length > 750_000 ||
      (value.image && !/^data:image\/(png|jpeg|webp);base64,[\da-zA-Z+/=]+$/u.test(value.image))) throw new Error("网页预览数据无效。");
    return value;
  } catch (error) { throw new Error(desktopErrorMessage(error)); }
}
export async function saveWebURL(url: string): Promise<void> {
  const save = window.go?.desktop?.DesktopBridge?.SaveExternalURL;
  if (!save) throw new Error("请在浏览器中打开网页后使用另存为。");
  try { await save(url); } catch (error) { throw new Error(desktopErrorMessage(error)); }
}

export interface FileLinkTarget { path: string; line?: number }
export function parseProjectFileLink(href: string): FileLinkTarget | null {
  if (!href || href.startsWith("//") || /^[#?]/u.test(href) || /[\u0000-\u001f\u007f]/u.test(href)) return null;
  let raw: string;
  try { raw = decodeURIComponent(href); } catch { return null; }
  if (/^file:/iu.test(raw)) {
    if (!/^file:\/\/\//iu.test(raw)) return null;
    raw = raw.slice(7).replace(/^\/([a-z]:)/iu, "$1");
  } else if (/^[a-z][a-z\d+.-]*:/iu.test(raw) && !/^[a-z]:[\\/]/iu.test(raw) && !/^.+\.\w+:\d+(?::\d+)?$/u.test(raw)) return null;
  let line: number | undefined;
  const location = /(?:#L|:)(\d+)(?::\d+)?(?:-L?\d+)?$/iu.exec(raw);
  if (location) { line = Number(location[1]); raw = raw.slice(0, location.index); }
  if (line !== undefined && (!Number.isSafeInteger(line) || line < 1)) return null;
  raw = raw.replace(/\\/gu, "/").replace(/^(\.\/)+/u, "");
  if (!raw || raw === "." || raw.split("/").includes("..") || /[?#]/u.test(raw)) return null;
  return { path: raw, line };
}

export function fileLabel(file: FileLinkTarget): string {
  return `${file.path.split("/").pop()}${file.line ? ` (line ${file.line})` : ""}`;
}
export function shortWebLabel(url: URL): string {
  const parts = url.pathname.split("/").filter(Boolean);
  if (url.hostname === "github.com") {
    if (parts[2] === "pull" && /^\d+$/u.test(parts[3] ?? "")) return `PR #${parts[3]}`;
    if (parts[2] === "issues" && /^\d+$/u.test(parts[3] ?? "")) return `Issue #${parts[3]}`;
    if (parts[2] === "commit" && /^[a-f\d]+$/iu.test(parts[3] ?? "")) return `${parts[1]}@${parts[3].slice(0, 7)}`;
    if (parts.length === 2) return parts.join("/");
  }
  const tail = parts.at(-1);
  let label = url.hostname;
  if (tail) { try { label += ` / ${decodeURIComponent(tail)}`; } catch { label += ` / ${tail}`; } }
  return label.length > 60 ? `${label.slice(0, 57)}…` : label;
}
