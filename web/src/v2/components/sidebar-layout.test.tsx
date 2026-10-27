import { createRef } from "react";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { sidebarWidthKey, V2SidebarLayout } from "./sidebar-layout";

const sidebarRef = createRef<HTMLDivElement>();
function layout(visible = true, narrow = false) {
  return <V2SidebarLayout sidebar={<nav>Saved chats</nav>} sidebarRef={sidebarRef} visible={visible} narrow={narrow}>
    <main><textarea aria-label="Draft" defaultValue="Keep this draft" /></main>
  </V2SidebarLayout>;
}
function handle() { return screen.getByRole("separator", { name: "Resize sidebar" }); }
function pointer(type: "Down" | "Move" | "Up" | "Cancel" | "Lost", x: number, id = 1, button = 0) {
  const target = handle();
  const init = { pointerId: id, clientX: x, button };
  if (type === "Lost") fireEvent.lostPointerCapture(target, init);
  else fireEvent[`pointer${type}`](target, init);
}
beforeEach(() => {
  vi.stubGlobal("innerWidth", 1280);
  Element.prototype.setPointerCapture = vi.fn();
  Element.prototype.hasPointerCapture = vi.fn(() => true);
  Element.prototype.releasePointerCapture = vi.fn();
});
afterEach(() => {
  cleanup(); localStorage.removeItem(sidebarWidthKey); vi.restoreAllMocks(); vi.unstubAllGlobals();
});

it("tracks the grabbed position, ignores other pointers, and saves only the released width", () => {
  render(layout());
  pointer("Down", 296, 1, 2);
  pointer("Move", 396);
  expect(handle()).toHaveAttribute("aria-valuenow", "299");
  pointer("Down", 296);
  expect(handle()).toHaveFocus();
  pointer("Move", 396, 2);
  expect(handle()).toHaveAttribute("aria-valuenow", "299");
  pointer("Move", 396);
  expect(handle()).toHaveAttribute("aria-valuenow", "399");
  expect(localStorage.getItem(sidebarWidthKey)).toBeNull();
  pointer("Up", 396);
  expect(localStorage.getItem(sidebarWidthKey)).toBe("399");
  expect(screen.getByRole("textbox", { name: "Draft" })).toHaveValue("Keep this draft");
  pointer("Move", 420);
  expect(handle()).toHaveAttribute("aria-valuenow", "399");
});

it("bounds dragging and cancels interrupted gestures without overwriting the saved preference", () => {
  localStorage.setItem(sidebarWidthKey, "350");
  render(layout());
  for (const cancel of ["escape", "pointer", "capture", "blur"]) {
    pointer("Down", 350);
    pointer("Move", 900);
    expect(handle()).toHaveAttribute("aria-valuenow", "420");
    pointer("Move", -100);
    expect(handle()).toHaveAttribute("aria-valuenow", "232");
    if (cancel === "escape") fireEvent.keyDown(handle(), { key: "Escape" });
    else if (cancel === "pointer") pointer("Cancel", -100);
    else if (cancel === "capture") pointer("Lost", -100);
    else fireEvent.blur(window);
    expect(handle()).toHaveAttribute("aria-valuenow", "350");
    expect(localStorage.getItem(sidebarWidthKey)).toBe("350");
    expect(handle().closest(".v2-shell-body")).toHaveAttribute("data-resizing", "false");
  }
});

it("supports keyboard sizing, resetting, and reopening with the chosen width", () => {
  const view = render(layout());
  fireEvent.keyDown(handle(), { key: "ArrowRight" });
  expect(handle()).toHaveAttribute("aria-valuenow", "311");
  fireEvent.keyDown(handle(), { key: "ArrowLeft" });
  expect(handle()).toHaveAttribute("aria-valuenow", "299");
  fireEvent.keyDown(handle(), { key: "End" });
  expect(handle()).toHaveAttribute("aria-valuenow", "420");
  fireEvent.keyDown(handle(), { key: "Home", ctrlKey: true });
  expect(handle()).toHaveAttribute("aria-valuenow", "420");
  fireEvent.keyDown(handle(), { key: "Home" });
  expect(handle()).toHaveAttribute("aria-valuenow", "232");
  fireEvent.keyDown(handle(), { key: "Enter" });
  expect(handle()).toHaveAttribute("aria-valuenow", "299");
  fireEvent.keyDown(handle(), { key: "End" });
  view.unmount();
  render(layout());
  expect(handle()).toHaveAttribute("aria-valuenow", "420");
  fireEvent.doubleClick(handle());
  expect(localStorage.getItem(sidebarWidthKey)).toBe("299");
});

it("preserves the preferred width through window resizing and disables the narrow overlay handle", () => {
  localStorage.setItem(sidebarWidthKey, "420");
  const view = render(layout());
  act(() => { window.innerWidth = 800; fireEvent.resize(window); });
  expect(handle()).toHaveAttribute("aria-valuenow", "380");
  expect(handle()).toHaveAttribute("aria-valuemax", "380");
  expect(localStorage.getItem(sidebarWidthKey)).toBe("420");
  pointer("Down", 376);
  pointer("Up", 376);
  expect(localStorage.getItem(sidebarWidthKey)).toBe("420");
  view.rerender(layout(true, true));
  expect(screen.queryByRole("separator")).not.toBeInTheDocument();
  view.rerender(layout(false));
  expect(screen.queryByRole("separator")).not.toBeInTheDocument();
  view.rerender(layout());
  act(() => { window.innerWidth = 1280; fireEvent.resize(window); });
  expect(handle()).toHaveAttribute("aria-valuenow", "420");
  expect(screen.getByRole("textbox", { name: "Draft" })).toHaveValue("Keep this draft");
});

it("defaults invalid stored widths and keeps resizing usable if storage is unavailable", () => {
  localStorage.setItem(sidebarWidthKey, "Infinity");
  render(layout());
  expect(handle()).toHaveAttribute("aria-valuenow", "299");
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("storage unavailable"); });
  fireEvent.keyDown(handle(), { key: "ArrowRight" });
  expect(handle()).toHaveAttribute("aria-valuenow", "311");
});
