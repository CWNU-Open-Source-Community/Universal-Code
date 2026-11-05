import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { useV2Navigation } from "./navigation";

const leave = vi.hoisted(() => vi.fn<(proceed: () => void, cancel?: () => void) => void>());
vi.mock("../components/file-edit-drafts", () => ({ useFileEditLeave: () => leave }));
afterEach(() => { cleanup(); leave.mockReset(); window.history.replaceState({}, "", "/"); });

it("keeps read-state side effects pending until navigation is accepted", () => {
  window.history.replaceState({}, "", "#/threads/current");
  const accepted = vi.fn();
  const view = renderHook(() => useV2Navigation());
  act(() => view.result.current.navigate({ kind: "thread", threadID: "unread" }, false, accepted));
  // Cancelling the leave dialog never invokes its proceed callback.
  expect(accepted).not.toHaveBeenCalled();
  expect(view.result.current.route.threadID).toBe("current");
  act(() => view.result.current.navigate({ kind: "thread", threadID: "unread" }, false, accepted));
  act(() => leave.mock.calls.at(-1)![0]());
  expect(accepted).toHaveBeenCalledOnce();
  expect(view.result.current.route.threadID).toBe("unread");
});

it("acknowledges explicitly reopening the current route without another leave request", () => {
  window.history.replaceState({}, "", "#/threads/current");
  const accepted = vi.fn();
  const view = renderHook(() => useV2Navigation());
  act(() => view.result.current.navigate({ kind: "thread", threadID: "current" }, false, accepted));
  expect(accepted).toHaveBeenCalledOnce();
  expect(leave).not.toHaveBeenCalled();
});

it("restores a resource when leaving is cancelled and can then return to its existing history entry", async () => {
  window.history.replaceState({}, "", "#/history");
  leave.mockImplementation((proceed) => proceed());
  const view = renderHook(() => useV2Navigation());
  act(() => view.result.current.navigate({ kind: "run", resourceID: "run-dirty" }));
  leave.mockReset();
  act(() => view.result.current.back());
  await waitFor(() => expect(leave).toHaveBeenCalled());
  expect(view.result.current.route.resourceID).toBe("run-dirty");
  act(() => leave.mock.calls.at(-1)![1]!());
  await waitFor(() => expect(window.location.hash).toBe("#/runs/run-dirty"));
  expect(window.history.state.v2PreviousHash).toBe("#/history");
  leave.mockReset();
  act(() => view.result.current.back());
  await waitFor(() => expect(leave).toHaveBeenCalled());
  act(() => leave.mock.calls.at(-1)![0]());
  expect(view.result.current.route.kind).toBe("history");
  expect(window.history.state.v2NavigationIndex).toBe(0);
});
