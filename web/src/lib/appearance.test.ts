import { act, renderHook, waitFor } from "@testing-library/react";
import {
  applyPrayuGlassSettings,
  applyPrayuTheme,
  defaultPrayuGlassSettings,
  initializePrayuAppearance,
  prayuGlassPresets,
  readPrayuGlassSettings,
  readPrayuTheme,
  readPrayuThemePreference,
  resetPrayuGlassSettings,
  usePrayuTheme,
} from "./appearance";

describe("Prayu appearance", () => {
  beforeEach(() => {
    window.localStorage.clear();
    delete document.documentElement.dataset.prayuTheme;
    delete document.documentElement.dataset.prayuGlassMode;
    document.documentElement.style.colorScheme = "";
    document.documentElement.style.removeProperty("--prayu-glass-blur");
    document.documentElement.style.removeProperty("--prayu-glass-frost");
    document.documentElement.style.removeProperty("--prayu-glass-brightness-white");
    document.documentElement.style.removeProperty("--prayu-glass-glow");
  });

  afterEach(() => {
    applyPrayuTheme("dark", false);
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("follows system changes, restores the preference, and stops following after a manual choice", async () => {
    const media = Object.assign(new EventTarget(), { matches: true });
    vi.stubGlobal("matchMedia", vi.fn(() => media));
    const light = vi.fn();
    const dark = vi.fn();
    const previousRuntime = window.runtime;
    window.runtime = { WindowSetLightTheme: light, WindowSetDarkTheme: dark };
    try {
      expect(readPrayuThemePreference()).toBe("system");
      applyPrayuTheme("system");
      expect(window.localStorage.getItem("prayu.theme")).toBe("system");
      expect(document.documentElement.dataset.prayuTheme).toBe("light");
      expect(light).toHaveBeenCalledOnce();
      const { result, unmount } = renderHook(usePrayuTheme);
      expect(result.current).toBe("light");

      media.matches = false;
      await act(async () => { media.dispatchEvent(new Event("change")); });
      await waitFor(() => expect(result.current).toBe("dark"));
      expect(readPrayuTheme()).toBe("dark");
      expect(readPrayuThemePreference()).toBe("system");
      expect(document.documentElement.dataset.prayuTheme).toBe("dark");
      expect(document.documentElement.style.colorScheme).toBe("dark");
      expect(dark).toHaveBeenCalledOnce();
      unmount();

      initializePrayuAppearance();
      light.mockClear();
      media.matches = true;
      media.dispatchEvent(new Event("change"));
      expect(document.documentElement.dataset.prayuTheme).toBe("light");
      expect(light).toHaveBeenCalledOnce();

      for (const theme of ["glass", "dark", "light"] as const) {
        applyPrayuTheme(theme);
        media.matches = !media.matches;
        media.dispatchEvent(new Event("change"));
        expect(document.documentElement.dataset.prayuTheme).toBe(theme);
        expect(readPrayuThemePreference()).toBe(theme);
      }
    } finally {
      window.runtime = previousRuntime;
    }
  });

  it("persists and restores mist with default Mica parameters", () => {
    applyPrayuTheme("glass");

    expect(document.documentElement.dataset.prayuTheme).toBe("glass");
    expect(document.documentElement.dataset.prayuGlassMode).toBe("mica");
    expect(document.documentElement.style.colorScheme).toBe("light");
    expect(document.documentElement.style.getPropertyValue("--prayu-glass-blur")).toBe("5px");
    expect(document.documentElement.style.getPropertyValue("--prayu-glass-frost")).toBe("0.4");
    expect(document.documentElement.style.getPropertyValue("--prayu-glass-brightness-white")).toBe("0");
    expect(document.documentElement.style.getPropertyValue("--prayu-glass-glow")).toBe("0");
    expect(window.localStorage.getItem("prayu.theme")).toBe("glass");
    expect(readPrayuTheme()).toBe("glass");
    expect(readPrayuGlassSettings()).toEqual(defaultPrayuGlassSettings);
  });

  it("clamps, persists, and resets glass material parameters", () => {
    const updated = applyPrayuGlassSettings({
      mode: "compat",
      blur: 48,
      frost: 45,
      brightness: 75,
      glow: true,
    });

    expect(updated).toEqual({
      mode: "compat",
      blur: 40,
      frost: 45,
      brightness: 75,
      glow: true,
    });
    expect(document.documentElement.dataset.prayuGlassMode).toBe("compat");
    expect(document.documentElement.style.getPropertyValue("--prayu-glass-blur")).toBe("40px");
    expect(document.documentElement.style.getPropertyValue("--prayu-glass-frost")).toBe("0.9");
    expect(document.documentElement.style.getPropertyValue("--prayu-glass-brightness-white")).toBe("0.5");
    expect(document.documentElement.style.getPropertyValue("--prayu-glass-glow")).toBe("1");
    expect(readPrayuGlassSettings()).toEqual(updated);

    applyPrayuGlassSettings(prayuGlassPresets.clear);
    expect(readPrayuGlassSettings()).toMatchObject({ blur: 0, frost: 8, brightness: 50 });

    const reset = resetPrayuGlassSettings();
    expect(reset).toEqual(defaultPrayuGlassSettings);
    expect(document.documentElement.dataset.prayuGlassMode).toBe("mica");
    expect(document.documentElement.style.getPropertyValue("--prayu-glass-blur")).toBe("5px");
  });

  it("keeps appearance changes non-blocking when storage is unavailable", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("storage disabled", "SecurityError");
    });

    expect(() => applyPrayuTheme("glass")).not.toThrow();
    expect(() => applyPrayuGlassSettings({ blur: 12, frost: 45 })).not.toThrow();
    expect(document.documentElement.dataset.prayuTheme).toBe("glass");
    expect(document.documentElement.style.getPropertyValue("--prayu-glass-blur")).toBe("12px");
  });
});

