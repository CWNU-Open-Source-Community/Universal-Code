import { act, renderHook } from "@testing-library/react";
import { afterEach, vi } from "vitest";
import { readV2Route, useV2Navigation } from "./navigation";

afterEach(() => {
  window.history.replaceState({}, "", "/");
  vi.restoreAllMocks();
});

it("restores task/settings identity and rejects malformed links without guessing another task", () => {
  expect(readV2Route("#/threads/thread-42/messages/event-8")).toEqual({ kind: "thread", threadID: "thread-42", messageID: "event-8" });
  expect(readV2Route("#/threads/thread-42/messages/%2fother")).toEqual({ kind: "invalid" });
  expect(readV2Route("#/threads/thread-42/settings/models")).toEqual({ kind: "thread", threadID: "thread-42", section: "models" });
  expect(readV2Route("#/threads/thread-42/settings/advanced-models")).toEqual({
    kind: "thread", threadID: "thread-42", section: "advanced-models",
  });
  expect(readV2Route("#/new")).toEqual({ kind: "new" });
  expect(readV2Route("#/new/settings/task-configuration")).toEqual({ kind: "new", section: "task-configuration" });
  expect(readV2Route("#/new/settings/configuration")).toEqual({ kind: "new", section: "configuration" });
  expect(readV2Route("#/new/settings/usage")).toEqual({ kind: "new", section: "usage" });
  expect(readV2Route("#/threads/thread-42/settings/configuration")).toEqual({ kind: "thread", threadID: "thread-42", section: "configuration" });
  for (const hash of ["#/threads/%ZZ", "#/threads/../../another", "#/threads/%2fother", "#/threads/task/settings/unknown"]) {
    expect(readV2Route(hash)).toEqual({ kind: "invalid" });
  }
});

it("restores Inspector and its settings source directly without browser history", () => {
  expect(readV2Route("#/threads/thread-42/inspector")).toEqual({ kind: "thread", threadID: "thread-42", view: "inspector" });
  expect(readV2Route("#/threads/thread-42/inspector/settings/appearance")).toEqual({
    kind: "thread", threadID: "thread-42", view: "inspector", section: "appearance",
  });
  expect(readV2Route("#/new/inspector")).toEqual({ kind: "new", view: "inspector" });
  expect(readV2Route("#/new/inspector/settings/models")).toEqual({ kind: "new", view: "inspector", section: "models" });
  for (const hash of ["#/threads/%2fother/inspector", "#/threads/task/inspector/inspector",
    "#/new/inspector/settings/unknown", "#/threads/task/settings/models/inspector"]) {
    expect(readV2Route(hash)).toEqual({ kind: "invalid" });
  }
});

it.each([0, 7])("returns settings to the explicit Inspector source even with history index %s", (index) => {
  window.history.replaceState({ v2NavigationIndex: index }, "", "#/threads/thread-42/inspector/settings/models");
  const historyBack = vi.spyOn(window.history, "back");
  const { result, unmount } = renderHook(() => useV2Navigation());
  expect(result.current.route).toEqual({ kind: "thread", threadID: "thread-42", view: "inspector", section: "models" });
  act(() => result.current.back());
  expect(historyBack).not.toHaveBeenCalled();
  expect(window.location.hash).toBe("#/threads/thread-42/inspector");
  expect(result.current.route).toEqual({ kind: "thread", threadID: "thread-42", view: "inspector" });
  unmount();
  const refreshed = renderHook(() => useV2Navigation());
  expect(refreshed.result.current.route).toEqual({ kind: "thread", threadID: "thread-42", view: "inspector" });
});

it("serializes view changes and keeps drafts and other UI state out of the route", () => {
  window.history.replaceState({}, "", "#/threads/thread-42");
  const { result } = renderHook(() => useV2Navigation());
  act(() => result.current.navigate({ kind: "thread", threadID: "thread-42", view: "inspector" }));
  expect(window.location.hash).toBe("#/threads/thread-42/inspector");
  act(() => result.current.navigate({ ...result.current.route, section: "general" }));
  expect(window.location.hash).toBe("#/threads/thread-42/inspector/settings/general");
  act(() => result.current.back());
  expect(window.location.hash).toBe("#/threads/thread-42/inspector");
  act(() => result.current.navigate({ kind: "thread", threadID: "thread-42" }));
  expect(window.location.hash).toBe("#/threads/thread-42");
  expect(Object.keys(window.history.state)).toEqual(["v2NavigationIndex"]);
});

it.each([
  ["runs/run-history", "run", "run-history"],
  ["sessions/session-history", "session", "session-history"],
  ["schedule", "schedule", undefined],
  ["schedule/run-scheduled", "schedule", "run-scheduled"],
] as const)("keeps the explicit Thread/resource source when returning from %s settings", (path, tool, resourceID) => {
  const source = `#/threads/thread-source/inspector/${path}`;
  window.history.replaceState({ v2NavigationIndex: 4 }, "", `${source}/settings/general`);
  const { result } = renderHook(() => useV2Navigation());
  expect(result.current.route).toEqual({ kind: "thread", threadID: "thread-source", view: "inspector",
    tool, ...(resourceID ? { resourceID } : {}), section: "general" });
  act(() => result.current.back());
  expect(window.location.hash).toBe(source);
});

it("retains a task's scheduled Run when its route is serialized and refreshed", () => {
  const { result } = renderHook(() => useV2Navigation());
  act(() => result.current.navigate({ kind: "thread", threadID: "thread-source", view: "inspector",
    tool: "schedule", resourceID: "run-scheduled" }));
  expect(readV2Route(window.location.hash)).toEqual(result.current.route);
  expect(window.location.hash).toBe("#/threads/thread-source/inspector/schedule/run-scheduled");
});

it.each(["context", "checkpoints", "tools", "ui-evidence"] as const)("opens and restores the exact Run's %s pane with its settings source", (pane) => {
  const { result } = renderHook(() => useV2Navigation());
  act(() => result.current.navigate({ kind: "thread", threadID: "thread-source", view: "inspector",
    tool: "run", resourceID: "run-history", pane }));
  const source = `#/threads/thread-source/inspector/runs/run-history/${pane}`;
  expect(window.location.hash).toBe(source);
  expect(readV2Route(source)).toEqual(result.current.route);
  act(() => result.current.navigate({ ...result.current.route, section: "connections" }));
  act(() => result.current.back());
  expect(window.location.hash).toBe(source);
  expect(readV2Route(window.location.hash).resourceID).toBe("run-history");
});

it("rejects unrecognized Run panes and panes attached to non-Run records", () => {
  for (const hash of ["#/new/inspector/runs/run-1/unknown", "#/new/inspector/runs/run-1/%2fcontext",
    "#/new/inspector/sessions/session-1/context", "#/new/inspector/schedule/run-1/checkpoints",
    "#/new/inspector/runs/%2frun/context", "#/new/inspector/runs/run-1/context/context"]) {
    expect(readV2Route(hash)).toEqual({ kind: "invalid" });
  }
});

it("rejects malformed compatible resource IDs and leaves direct resources unbound to a Thread", () => {
  expect(readV2Route("", "/legacy/runs/run-direct")).toEqual({ kind: "new", view: "inspector", tool: "run", resourceID: "run-direct" });
  expect(readV2Route("", "/legacy/sessions/session-direct")).toEqual({ kind: "new", view: "inspector", tool: "session", resourceID: "session-direct" });
  for (const path of ["/legacy/runs/%2fother", "/legacy/sessions/%ZZ", "/legacy/runs/../../other"]) {
    expect(readV2Route("", path)).toEqual({ kind: "invalid" });
  }
  expect(readV2Route("#/threads/thread-source/inspector/runs/%2fother")).toEqual({ kind: "invalid" });
});
