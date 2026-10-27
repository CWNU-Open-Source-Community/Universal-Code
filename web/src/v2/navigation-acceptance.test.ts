import { act, cleanup, renderHook } from "@testing-library/react";
import { useV2Navigation } from "./navigation";

const leave = vi.hoisted(() => vi.fn<(proceed: () => void) => void>());
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
