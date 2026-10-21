export function hasRequiredOnlyKeys(value: unknown, required: string[], optional: string[] = []):
  value is Record<string, unknown> {
  return isRecord(value) && hasOnlyKeys(value, [...required, ...optional]) &&
    required.every((key) => Object.prototype.hasOwnProperty.call(value, key));
}

export function validWorkspaceRelativePath(value: unknown): value is string {
  if (typeof value !== "string" || value.length === 0 || Array.from(value).length > 512 ||
    value.trim() !== value || value.startsWith("/") || value.includes("\\") ||
    value.includes(":") || /[\u0000-\u001f\u007f]/u.test(value)) {
    return false;
  }
  if (value === ".") return true;
  return value.split("/").every((part) => part !== "" && part !== "." && part !== "..");
}

export function validWorkspaceEntryName(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 && Array.from(value).length <= 255 &&
    value.trim() === value && !value.includes("/") && !value.includes("\\") &&
    !value.includes(":") && !/[\u0000-\u001f\u007f]/u.test(value);
}

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function hasExactKeys(value: unknown, expected: string[]): value is Record<string, unknown> {
  if (!isRecord(value)) {
    return false;
  }
  const actual = Object.keys(value).sort();
  const wanted = [...expected].sort();
  return actual.length === wanted.length && actual.every((key, index) => key === wanted[index]);
}

export function hasOnlyKeys(value: Record<string, unknown>, allowed: string[]): boolean {
  const accepted = new Set(allowed);
  return Object.keys(value).every((key) => accepted.has(key));
}

export function boundedIdentity(value: unknown): string {
  return typeof value === "string" && value.trim() === value && !value.includes("\0") &&
    value.length > 0 && value.length <= 256
    ? value
    : "";
}

export function boundedText(value: unknown, maximum: number): value is string {
  return typeof value === "string" && value.trim() === value && value.length > 0 &&
    value.length <= maximum;
}

export function validDate(value: unknown): value is string {
  return typeof value === "string" && Number.isFinite(Date.parse(value));
}

export function isSHA256(value: unknown): value is string {
  return typeof value === "string" && /^[0-9a-f]{64}$/.test(value);
}

export function safePositiveInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0;
}

export function safeBoundedCount(value: unknown, maximum: number): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) &&
    value >= 0 && value <= maximum;
}

export function boundedStringArray(value: unknown, maximumItems: number,
  maximumBytes = 4_096): value is string[] {
  return Array.isArray(value) && value.length <= maximumItems &&
    value.every((item) => typeof item === "string" && item.length <= maximumBytes &&
      item.trim() === item);
}
