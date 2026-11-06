import { initializePrayuAppearance } from "./appearance";
import { applyTypography, defaultTypography, normalizeTypography, readTypography, saveTypography, typographyStorageKey } from "./typography";

afterEach(() => {
  localStorage.removeItem(typographyStorageKey);
  document.documentElement.style.removeProperty("--prayu-message-size");
  document.documentElement.style.removeProperty("--prayu-code-size");
  vi.restoreAllMocks();
});

it("restores saved sizes during appearance initialization and keeps existing defaults", () => {
  expect(readTypography()).toEqual({ messageSize: 14, codeSize: 13 });
  initializePrayuAppearance();
  expect(document.documentElement.style.getPropertyValue("--prayu-message-size")).toBe("14px");
  expect(document.documentElement.style.getPropertyValue("--prayu-code-size")).toBe("13px");
  saveTypography({ messageSize: 20, codeSize: 18 });
  document.documentElement.style.removeProperty("--prayu-message-size");
  document.documentElement.style.removeProperty("--prayu-code-size");
  initializePrayuAppearance();
  expect(readTypography()).toEqual({ messageSize: 20, codeSize: 18 });
  expect(document.documentElement.style.getPropertyValue("--prayu-message-size")).toBe("20px");
  expect(document.documentElement.style.getPropertyValue("--prayu-code-size")).toBe("18px");
});

it("bounds persisted sizes to whole pixels and rejects non-numeric values", () => {
  expect(normalizeTypography({ messageSize: 1000, codeSize: -10 })).toEqual({ messageSize: 24, codeSize: 10 });
  expect(normalizeTypography({ messageSize: 12.6, codeSize: 23.8 })).toEqual({ messageSize: 13, codeSize: 24 });
  for (const value of [null, [], "20", { messageSize: "24px", codeSize: null }, { messageSize: NaN, codeSize: Infinity }]) {
    expect(normalizeTypography(value)).toEqual(defaultTypography);
  }
  localStorage.setItem(typographyStorageKey, "broken json");
  expect(readTypography()).toEqual(defaultTypography);
});

it("does not apply an unsaved change when persistence fails", () => {
  applyTypography({ messageSize: 18, codeSize: 16 });
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new DOMException("Storage unavailable"); });
  expect(() => saveTypography({ messageSize: 24, codeSize: 24 })).toThrow();
  expect(document.documentElement.style.getPropertyValue("--prayu-message-size")).toBe("18px");
  expect(document.documentElement.style.getPropertyValue("--prayu-code-size")).toBe("16px");
});
