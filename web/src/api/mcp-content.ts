import type { ThreadActivityMCPContentView } from "./types";

const byteLength = (value: string) => new TextEncoder().encode(value).length;
const record = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);
const text = (value: unknown, limit: number) => typeof value === "string" && byteLength(value) <= limit && !/[\p{Cc}\p{Cf}]/u.test(value);
const exact = (value: Record<string, unknown>, keys: string[]) => Object.keys(value).length === keys.length && keys.every((key) => Object.hasOwn(value, key));

export function safeMCPContentLink(value: string): boolean {
  if (!text(value, 4096) || /[\\]/u.test(value)) return false;
  try {
    const url = new URL(value);
    return (url.protocol === "http:" || url.protocol === "https:") && !!url.hostname && !url.username && !url.password && !url.hash;
  } catch { return false; }
}

export function validMCPContents(value: unknown): value is ThreadActivityMCPContentView[] {
  if (!Array.isArray(value) || value.length > 16) return false;
  let total = 0;
  for (const item of value) {
    if (!record(item) || typeof item.kind !== "string" || !["text", "resource_text", "resource_link", "image", "audio"].includes(item.kind)) return false;
    const metadata = ["uri", "name", "mime_type", "role"].filter((key) => item[key] !== undefined);
    if (item.uri !== undefined && !text(item.uri, 4096) || item.name !== undefined && !text(item.name, 8192) ||
      item.mime_type !== undefined && !text(item.mime_type, 256) || item.role !== undefined && item.role !== "user" && item.role !== "assistant") return false;
    if (item.kind === "text" || item.kind === "resource_text") {
      if (!exact(item, ["kind", "text", ...metadata]) || typeof item.text !== "string" || !item.text || byteLength(item.text) > 32768 ||
        /[\p{Cf}]/u.test(item.text) || Array.from(item.text).some((character) => /[\p{Cc}]/u.test(character) && character !== "\n" && character !== "\t")) return false;
    } else if (item.kind === "resource_link") {
      if (!exact(item, ["kind", ...metadata]) || typeof item.uri !== "string" || !item.uri) return false;
    } else {
      const dimensions = item.kind === "image" ? ["width", "height"] : [];
      if (!exact(item, ["kind", "data_base64", "byte_size", ...dimensions, ...metadata]) || typeof item.data_base64 !== "string" ||
        item.data_base64.length > 131072 || !Number.isSafeInteger(item.byte_size) || Number(item.byte_size) < 1 || Number(item.byte_size) > 131072) return false;
      let data: string;
      try { data = atob(item.data_base64); if (btoa(data) !== item.data_base64 || data.length !== item.byte_size) return false; } catch { return false; }
      if (item.kind === "image") {
        if (!Number.isSafeInteger(item.width) || !Number.isSafeInteger(item.height) || Number(item.width) < 1 || Number(item.height) < 1 ||
          Number(item.width) > 8192 || Number(item.height) > 8192 || Number(item.width)*Number(item.height) > 16*1024*1024) return false;
        const formatMatched = item.mime_type === "image/png" && data.startsWith("\x89PNG\r\n\x1a\n") ||
          item.mime_type === "image/jpeg" && data.charCodeAt(0) === 255 && data.charCodeAt(1) === 216 && data.charCodeAt(2) === 255 ||
          item.mime_type === "image/gif" && (data.startsWith("GIF87a") || data.startsWith("GIF89a")) ||
          item.mime_type === "image/webp" && data.startsWith("RIFF") && data.slice(8, 12) === "WEBP";
        if (!formatMatched) return false;
      } else {
        const formatMatched = (item.mime_type === "audio/wav" || item.mime_type === "audio/x-wav") && data.length >= 44 && data.startsWith("RIFF") && data.slice(8, 12) === "WAVE" ||
          item.mime_type === "audio/mpeg" && data.length >= 10 && (data.startsWith("ID3") || data.charCodeAt(0) === 255 && (data.charCodeAt(1)&224) === 224) ||
          item.mime_type === "audio/ogg" && data.length >= 27 && data.startsWith("OggS") && data.charCodeAt(4) === 0;
        if (!formatMatched) return false;
      }
    }
    total += byteLength(JSON.stringify(item))+256;
    if (total > 131072) return false;
  }
  return true;
}
