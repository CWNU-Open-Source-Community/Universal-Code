import { useSyncExternalStore } from "react";
import { setDesktopWindowTheme } from "./desktop-window";
import { applyTypography, readTypography } from "./typography";

export type PrayuTheme = "light" | "dark" | "glass";
export type PrayuThemePreference = PrayuTheme | "system";
export type PrayuGlassMode = "mica" | "compat";
export type PrayuGlassPreset = "clear" | "standard" | "frosted";

export interface PrayuGlassSettings {
  mode: PrayuGlassMode;
  blur: number;
  frost: number;
  brightness: number;
  glow: boolean;
}

const themeStorageKey = "prayu.theme";
const glassStorageKey = "prayu.glass";
let stopWatchingSystemTheme: (() => void) | undefined;

export const defaultPrayuGlassSettings: Readonly<PrayuGlassSettings> = Object.freeze({
  mode: "mica",
  blur: 5,
  frost: 20,
  brightness: 50,
  glow: false,
});

export const prayuGlassPresets: Readonly<
  Record<PrayuGlassPreset, Readonly<Pick<PrayuGlassSettings, "blur" | "frost" | "brightness">>>
> = Object.freeze({
  clear: Object.freeze({ blur: 0, frost: 8, brightness: 50 }),
  standard: Object.freeze({ blur: 5, frost: 20, brightness: 50 }),
  frosted: Object.freeze({ blur: 12, frost: 45, brightness: 50 }),
});

function clampStep(value: unknown, min: number, max: number, step: number, fallback: number): number {
  const num = typeof value === "number" ? value : Number(value);
  if (!Number.isFinite(num)) return fallback;
  const clamped = Math.min(max, Math.max(min, num));
  const stepped = Math.round(clamped / step) * step;
  return Number(stepped.toFixed(2));
}

export function normalizePrayuGlassSettings(
  input?: Partial<PrayuGlassSettings> | null,
): PrayuGlassSettings {
  const mode: PrayuGlassMode = input?.mode === "compat" ? "compat" : "mica";
  const blur = clampStep(input?.blur, 0, 40, 0.5, defaultPrayuGlassSettings.blur);
  const frost = clampStep(input?.frost, 0, 100, 1, defaultPrayuGlassSettings.frost);
  const brightness = clampStep(
    input?.brightness,
    50,
    100,
    1,
    defaultPrayuGlassSettings.brightness,
  );
  const glow = typeof input?.glow === "boolean" ? input.glow : defaultPrayuGlassSettings.glow;
  return { mode, blur, frost, brightness, glow };
}

export function readPrayuGlassSettings(): PrayuGlassSettings {
  if (typeof window === "undefined") return { ...defaultPrayuGlassSettings };
  try {
    const raw = window.localStorage.getItem(glassStorageKey);
    if (!raw) return { ...defaultPrayuGlassSettings };
    const parsed = JSON.parse(raw) as Partial<PrayuGlassSettings>;
    return normalizePrayuGlassSettings(parsed);
  } catch {
    return { ...defaultPrayuGlassSettings };
  }
}

function syncGlassVariables(settings: PrayuGlassSettings): void {
  const root = document.documentElement;
  root.dataset.prayuGlassMode = settings.mode;
  const frostMultiplier = Number(Math.min(settings.frost / 50, 1.4).toFixed(3));
  const brightnessWhite = Number(
    Math.max(0, Math.min(1, (settings.brightness - 50) / 50)).toFixed(3),
  );
  root.style.setProperty("--prayu-glass-blur", `${settings.blur}px`);
  root.style.setProperty("--prayu-glass-frost", String(frostMultiplier));
  root.style.setProperty("--prayu-glass-brightness-white", String(brightnessWhite));
  root.style.setProperty("--prayu-glass-glow", settings.glow ? "1" : "0");
}

export function applyPrayuGlassSettings(
  next: Partial<PrayuGlassSettings>,
  persist = true,
): PrayuGlassSettings {
  const current = readPrayuGlassSettings();
  const normalized = normalizePrayuGlassSettings({ ...current, ...next });
  syncGlassVariables(normalized);
  if (persist) {
    try {
      window.localStorage.setItem(glassStorageKey, JSON.stringify(normalized));
    } catch {
      // Display preferences must never block the workbench.
    }
  }
  return normalized;
}

export function resetPrayuGlassSettings(persist = true): PrayuGlassSettings {
  const normalized = { ...defaultPrayuGlassSettings };
  syncGlassVariables(normalized);
  if (persist) {
    try {
      window.localStorage.setItem(glassStorageKey, JSON.stringify(normalized));
    } catch {
      // Display preferences must never block the workbench.
    }
  }
  return normalized;
}

export function readPrayuThemePreference(): PrayuThemePreference {
  if (typeof window === "undefined") return "system";
  try {
    const stored = window.localStorage.getItem(themeStorageKey);
    if (stored === "system" || stored === "light" || stored === "dark" || stored === "glass") return stored;
  } catch {
    // Fall through to the system preference.
  }
  return "system";
}

export function readPrayuTheme(): PrayuTheme {
  const preference = readPrayuThemePreference();
  if (preference !== "system") return preference;
  return typeof window !== "undefined" && window.matchMedia?.("(prefers-color-scheme: light)").matches ? "light" : "dark";
}

function subscribeToTheme(onChange: () => void): () => void {
  const observer = new MutationObserver(onChange);
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ["data-prayu-theme"] });
  return () => observer.disconnect();
}

function readAppliedTheme(): PrayuTheme {
  return (document.documentElement.dataset.prayuTheme as PrayuTheme | undefined) ?? readPrayuTheme();
}

export function usePrayuTheme(): PrayuTheme {
  return useSyncExternalStore(subscribeToTheme, readAppliedTheme, readPrayuTheme);
}

export function applyPrayuTheme(theme: PrayuThemePreference, persist = true): void {
  stopWatchingSystemTheme?.();
  stopWatchingSystemTheme = undefined;
  const systemTheme = theme === "system" ? window.matchMedia?.("(prefers-color-scheme: light)") : undefined;
  const syncTheme = () => {
    const resolved = theme === "system" ? systemTheme?.matches ? "light" : "dark" : theme;
    document.documentElement.dataset.prayuTheme = resolved;
    document.documentElement.style.colorScheme = resolved === "dark" ? "dark" : "light";
    setDesktopWindowTheme(resolved);
  };
  syncTheme();
  syncGlassVariables(readPrayuGlassSettings());
  if (systemTheme) {
    systemTheme.addEventListener("change", syncTheme);
    stopWatchingSystemTheme = () => systemTheme.removeEventListener("change", syncTheme);
  }
  if (!persist) return;
  try {
    window.localStorage.setItem(themeStorageKey, theme);
  } catch {
    // Display preferences must never block the workbench.
  }
}

export function initializePrayuAppearance(): void {
  applyPrayuTheme(readPrayuThemePreference(), false);
  applyTypography(readTypography());
}

