export interface TypographySettings {
  messageSize: number;
  codeSize: number;
}

export const typographyStorageKey = "prayu.typography.v1";
export const defaultTypography: Readonly<TypographySettings> = Object.freeze({ messageSize: 14, codeSize: 13 });
export const typographyLimits = { messageSize: { min: 12, max: 24 }, codeSize: { min: 10, max: 24 } } as const;

function fontSize(value: unknown, key: keyof TypographySettings): number {
  if (typeof value !== "number" || !Number.isFinite(value)) return defaultTypography[key];
  const { min, max } = typographyLimits[key];
  return Math.max(min, Math.min(max, Math.round(value)));
}

export function normalizeTypography(value: unknown): TypographySettings {
  const settings = value && typeof value === "object" ? value as Partial<TypographySettings> : {};
  return { messageSize: fontSize(settings.messageSize, "messageSize"), codeSize: fontSize(settings.codeSize, "codeSize") };
}

export function readTypography(): TypographySettings {
  try { return normalizeTypography(JSON.parse(localStorage.getItem(typographyStorageKey) ?? "null")); }
  catch { return { ...defaultTypography }; }
}

export function applyTypography(settings: TypographySettings): void {
  const next = normalizeTypography(settings);
  document.documentElement.style.setProperty("--prayu-message-size", `${next.messageSize}px`);
  document.documentElement.style.setProperty("--prayu-code-size", `${next.codeSize}px`);
}

export function saveTypography(settings: TypographySettings): TypographySettings {
  const next = normalizeTypography(settings);
  localStorage.setItem(typographyStorageKey, JSON.stringify(next));
  applyTypography(next);
  return next;
}
