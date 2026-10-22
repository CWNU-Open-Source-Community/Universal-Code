import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { APIClient } from "../../api/client";
import { LocaleProvider } from "../../lib/locale";
import { readTypography, typographyStorageKey } from "../../lib/typography";
import { TypographySettingsPanel } from "./typography-settings";
import { V2Settings } from "./settings";

afterEach(() => {
  localStorage.removeItem(typographyStorageKey);
  localStorage.removeItem("prayu.locale.v1");
  document.documentElement.style.removeProperty("--prayu-message-size");
  document.documentElement.style.removeProperty("--prayu-code-size");
  vi.restoreAllMocks();
});

it.each([
  ["zh-CN", "消息文字字号", "代码字号", "还原字号"],
  ["en-US", "Message font size", "Code font size", "Reset font sizes"],
] as const)("updates, reopens and resets accessible %s font controls", (locale, messageLabel, codeLabel, resetLabel) => {
  localStorage.setItem("prayu.locale.v1", locale);
  const view = render(<TypographySettingsPanel />, { wrapper: LocaleProvider });
  expect(screen.getByRole("slider", { name: messageLabel })).toHaveValue("14");
  expect(screen.getByRole("slider", { name: codeLabel })).toHaveValue("13");
  expect(screen.getByRole("button", { name: resetLabel })).toBeDisabled();
  fireEvent.change(screen.getByRole("slider", { name: messageLabel }), { target: { value: "24" } });
  fireEvent.change(screen.getByRole("slider", { name: codeLabel }), { target: { value: "20" } });
  expect(document.documentElement.style.getPropertyValue("--prayu-message-size")).toBe("24px");
  expect(document.documentElement.style.getPropertyValue("--prayu-code-size")).toBe("20px");
  view.unmount();
  render(<TypographySettingsPanel />, { wrapper: LocaleProvider });
  expect(screen.getByRole("slider", { name: messageLabel })).toHaveValue("24");
  expect(screen.getByRole("slider", { name: codeLabel })).toHaveValue("20");
  fireEvent.click(screen.getByRole("button", { name: resetLabel }));
  expect(readTypography()).toEqual({ messageSize: 14, codeSize: 13 });
  expect(document.documentElement.style.getPropertyValue("--prayu-message-size")).toBe("14px");
  expect(document.documentElement.style.getPropertyValue("--prayu-code-size")).toBe("13px");
});

it("reports storage failure and leaves the previous control value intact", () => {
  localStorage.setItem("prayu.locale.v1", "en-US");
  render(<TypographySettingsPanel />, { wrapper: LocaleProvider });
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new DOMException("Storage unavailable"); });
  fireEvent.change(screen.getByRole("slider", { name: "Message font size" }), { target: { value: "24" } });
  expect(screen.getByRole("alert")).toHaveTextContent("Font sizes could not be saved");
  expect(screen.getByRole("slider", { name: "Message font size" })).toHaveValue("14");
});

it("mounts the reading controls in the production Appearance settings", () => {
  render(<QueryClientProvider client={new QueryClient()}><V2Settings client={{} as APIClient}
    section="appearance" threadID="" workspaces={[]} onSelectSection={vi.fn()} onOpenInspector={vi.fn()} /></QueryClientProvider>,
  { wrapper: LocaleProvider });
  expect(screen.getByRole("region", { name: "阅读字号" })).toBeInTheDocument();
  expect(screen.getByRole("slider", { name: "消息文字字号" })).toBeInTheDocument();
});
