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
  expect(readV2Route("#/new/settings/environment")).toEqual({ kind: "new", section: "environment" });
  expect(readV2Route("#/threads/thread-42/settings/environment")).toEqual({ kind: "thread", threadID: "thread-42", section: "environment" });
  expect(readV2Route("#/new/settings/usage")).toEqual({ kind: "new", section: "usage" });
  expect(readV2Route("#/threads/thread-42/settings/configuration")).toEqual({ kind: "thread", threadID: "thread-42", section: "configuration" });
  for (const hash of ["#/threads/%ZZ", "#/threads/../../another", "#/threads/%2fother", "#/threads/task/settings/unknown"]) {
    expect(readV2Route(hash)).toEqual({ kind: "invalid" });
  }
});

const resourceRoutes = [
  ["#/history", { kind: "history" }],
  ["#/threads/thread-source/records", { kind: "thread", threadID: "thread-source", panel: "records" }],
  ["#/runs/run-history", { kind: "run", resourceID: "run-history" }],
  ["#/sessions/session-history", { kind: "session", resourceID: "session-history" }],
  ["#/automations", { kind: "automations" }],
  ["#/automations/runs/run-scheduled", { kind: "automations", resourceID: "run-scheduled" }],
  ["#/threads/thread-source/runs/run-history", { kind: "run", threadID: "thread-source", resourceID: "run-history" }],
  ["#/threads/thread-source/sessions/session-history", { kind: "session", threadID: "thread-source", resourceID: "session-history" }],
  ["#/threads/thread-source/automations", { kind: "automations", threadID: "thread-source" }],
  ["#/threads/thread-source/automations/runs/run-scheduled", { kind: "automations", threadID: "thread-source", resourceID: "run-scheduled" }],
  ["#/threads/thread-source/messages/message-source", { kind: "thread", threadID: "thread-source", messageID: "message-source" }],
] as const;

it.each(resourceRoutes)("restores and serializes the explicit source %s", (hash, route) => {
  window.history.replaceState({}, "", "#/new");
  const { result } = renderHook(() => useV2Navigation());
  expect(readV2Route(hash)).toEqual(route);
  act(() => result.current.navigate(route));
  expect(window.location.hash).toBe(hash);
  expect(Object.keys(window.history.state)).toEqual(["v2NavigationIndex"]);
});

it.each(resourceRoutes)("returns settings to %s without browser history or losing resource identity", (hash, route) => {
  window.history.replaceState({ v2NavigationIndex: 7 }, "", `${hash}/settings/models`);
  const historyBack = vi.spyOn(window.history, "back");
  const { result, unmount } = renderHook(() => useV2Navigation());
  expect(result.current.route).toEqual({ ...route, section: "models" });
  act(() => result.current.back());
  expect(historyBack).not.toHaveBeenCalled();
  expect(window.location.hash).toBe(hash);
  unmount();
  const refreshed = renderHook(() => useV2Navigation());
  expect(refreshed.result.current.route).toEqual(route);
});

it.each(["context", "checkpoints", "tools", "ui-evidence"] as const)("retains the exact Run's %s pane through settings", (pane) => {
  const { result } = renderHook(() => useV2Navigation());
  act(() => result.current.navigate({ kind: "run", threadID: "thread-source", resourceID: "run-history", pane }));
  const source = `#/threads/thread-source/runs/run-history/${pane}`;
  expect(window.location.hash).toBe(source);
  expect(readV2Route(source)).toEqual(result.current.route);
  act(() => result.current.navigate({ ...result.current.route, section: "connections" }));
  act(() => result.current.back());
  expect(window.location.hash).toBe(source);
  expect(readV2Route(window.location.hash).resourceID).toBe("run-history");
});

it.each([
  ["#/runs/run-1", "#/history"],
  ["#/sessions/session-1", "#/history"],
  ["#/threads/task/runs/run-1", "#/threads/task/records"],
  ["#/threads/task/sessions/session-1", "#/threads/task/records"],
  ["#/automations/runs/run-1", "#/new"],
  ["#/threads/task/automations/runs/run-1", "#/threads/task"],
  ["#/threads/task/records", "#/threads/task"],
] as const)("returns %s to its explicit destination %s", (source, target) => {
  window.history.replaceState({ v2NavigationIndex: 4 }, "", source);
  const historyBack = vi.spyOn(window.history, "back");
  const { result } = renderHook(() => useV2Navigation());
  act(() => result.current.back());
  expect(historyBack).not.toHaveBeenCalled();
  expect(window.location.hash).toBe(target);
});

it("rejects malformed resources, unexpected panes and old Inspector addresses", () => {
  for (const hash of ["#/runs/run-1/unknown", "#/runs/run-1/%2fcontext", "#/runs/run-1/",
    "#/sessions/session-1/context", "#/automations/runs/run-1/checkpoints", "#/automations/runs/",
    "#/runs/%2frun/context", "#/runs/run-1/context/context", "#/sessions/%ZZ", "#/threads/task/history",
    "#/threads/thread-source/runs/%2fother", "#/new/inspector", "#/threads/task/inspector",
    "#/new/inspector/runs/run-1", "#/threads/task/inspector/sessions/session-1", "#/new/settings/inspector",
    "#/threads/task/settings/inspector", "#/history/settings/unknown"]) {
    expect(readV2Route(hash)).toEqual({ kind: "invalid" });
  }
  for (const path of ["/legacy", "/legacy/", "/legacy/threads/task", "/legacy/runs/run-direct",
    "/legacy/sessions/session-direct", "/legacy/runs/%2fother", "/legacy/sessions/%ZZ"]) {
    expect(readV2Route("", path)).toEqual({ kind: "invalid" });
    expect(readV2Route("#/threads/current", path)).toEqual({ kind: "invalid" });
  }
});
