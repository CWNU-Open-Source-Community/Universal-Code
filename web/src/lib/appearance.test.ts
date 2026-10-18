import {
  applyPrayuGlassSettings,
  applyPrayuTheme,
  defaultPrayuGlassSettings,
  prayuGlassPresets,
  readPrayuGlassSettings,
  readPrayuTheme,
  resetPrayuGlassSettings,
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
    vi.restoreAllMocks();
  });

  it("persists and restores the Catppuccin Latte glass mode with default Mica parameters", () => {
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

