import type { ReactNode } from "react";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import type { APIClient } from "../api/client";
import type { ThreadReplyUpdateView, ThreadReplyUpdatesView } from "../api/types";
import { useConnectionStore } from "../state/connection";
import { V2RecoveryProvider } from "./recovery-storage";
import { useV2ThreadUnread } from "./use-thread-unread";

const key = (scope = "database-one") => `v2_recovery.v1:${encodeURIComponent(window.location.origin)}:${encodeURIComponent("/api/v1")}:${scope}:reply-read-state`;
const saved = (scope?: string) => JSON.parse(localStorage.getItem(key(scope))!).value;
const reply = (id: string, threadID = "other-chat", ordinal = 1): ThreadReplyUpdateView => ({
  thread_id: threadID, message_id: id, thread_status: "active", run_ordinal: ordinal, sequence: 1, created_at: "2026-10-22T00:00:00Z",
});
const page = (cursor: string, items: ThreadReplyUpdateView[] = [], has_more = false, data_store_id = "database-one"): ThreadReplyUpdatesView =>
  ({ version: "thread_reply_updates.v1", cursor, items, has_more, data_store_id });
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((accept) => { resolve = accept; });
  return { promise, resolve };
}
function setup() {
  const get = vi.fn().mockResolvedValue(page("baseline"));
  const health = vi.fn().mockResolvedValue({ data_store_id: "database-one" });
  const client = { baseURL: "/api/v1", get, health } as unknown as APIClient;
  let scope = "database-one";
  const wrapper = ({ children }: { children: ReactNode }) => <V2RecoveryProvider client={client} scopeID={scope}>{children}</V2RecoveryProvider>;
  const mount = () => renderHook(() => useV2ThreadUnread(client, scope), { wrapper });
  return { client, get, health, mount, changeScope: (value: string) => { scope = value; } };
}
beforeEach(() => localStorage.clear());
afterEach(() => { cleanup(); vi.restoreAllMocks(); useConnectionStore.getState().disconnect(); });

it("baselines existing history, continues empty source pages and retains distinct saved identities across Runs", async () => {
  const fixture = setup();
  const view = fixture.mount();
  await waitFor(() => expect(saved().cursor).toBe("baseline"));
  expect(fixture.get).toHaveBeenCalledWith("/threads/reply-updates", { limit: 100 }, expect.any(AbortSignal));
  expect(view.result.current.unreadThreadIDs.size).toBe(0);
  fixture.get.mockResolvedValueOnce(page("filtered", [], true))
    .mockResolvedValueOnce(page("new", [reply("saved-reply", "other-chat", 2), reply("another-saved-reply", "other-chat", 2), reply("late-old-run", "other-chat", 1)]));
  await act(async () => { await view.result.current.retry(); });
  expect(fixture.get).toHaveBeenLastCalledWith("/threads/reply-updates", { limit: 100, cursor: "filtered" }, expect.any(AbortSignal));
  expect([...view.result.current.unreadThreadIDs]).toEqual(["other-chat"]);
  expect(saved().threads["other-chat"].pending).toEqual(["saved-reply", "another-saved-reply", "late-old-run"]);
  fixture.get.mockResolvedValue(page("new", [reply("saved-reply")]));
  await act(async () => { await view.result.current.retry(); });
  expect(saved().threads["other-chat"].pending).toHaveLength(3);
});

it("does not resurrect replies read before their feed response and retains observations made during a scan", async () => {
  const fixture = setup();
  const view = fixture.mount();
  await waitFor(() => expect(saved().cursor).toBe("baseline"));
  act(() => view.result.current.reading.observe("other-chat", ["already-visible"]));
  const incoming = deferred<ThreadReplyUpdatesView>();
  fixture.get.mockReturnValueOnce(incoming.promise);
  let scanning!: Promise<boolean>;
  act(() => { scanning = view.result.current.retry(); });
  act(() => view.result.current.reading.observe("other-chat", ["visible-during-request"]));
  await act(async () => { incoming.resolve(page("first", [reply("already-visible")])); await scanning; });
  expect(view.result.current.unreadThreadIDs.size).toBe(0);
  expect(saved().threads["other-chat"].seen).toEqual(["visible-during-request"]);
  fixture.get.mockResolvedValue(page("second", [reply("visible-during-request")]));
  await act(async () => { await view.result.current.retry(); });
  expect(view.result.current.unreadThreadIDs.size).toBe(0);
  expect(saved().threads).toEqual({});
});

it("confirms only the captured pending range and keeps manual unread through observation, polling and reload", async () => {
  const fixture = setup();
  let view = fixture.mount();
  await waitFor(() => expect(saved().cursor).toBe("baseline"));
  fixture.get.mockResolvedValue(page("old", [reply("outside-latest-window")]));
  let captured!: string[] | null;
  await act(async () => { captured = await view.result.current.reading.prepare("other-chat"); });
  expect(captured).toEqual(["outside-latest-window"]);
  fixture.get.mockResolvedValue(page("new", [reply("arrived-during-transcript-read")]));
  await act(async () => { await view.result.current.retry(); });
  act(() => {
    view.result.current.setRead("other-chat", false);
    view.result.current.reading.confirm("other-chat", captured!);
  });
  expect(saved().threads["other-chat"].pending).toEqual(["arrived-during-transcript-read"]);
  act(() => view.result.current.reading.observe("other-chat", ["arrived-during-transcript-read"]));
  expect(view.result.current.unreadThreadIDs.has("other-chat")).toBe(true);
  fixture.get.mockResolvedValue(page("new"));
  await act(async () => { await view.result.current.retry(); });
  view.unmount();
  view = fixture.mount();
  expect(view.result.current.unreadThreadIDs.has("other-chat")).toBe(true);
  act(() => view.result.current.open("other-chat"));
  expect(view.result.current.unreadThreadIDs.has("other-chat")).toBe(false);
  act(() => view.result.current.setRead("other-chat", false));
  act(() => view.result.current.setRead("other-chat", true));
  expect(view.result.current.unreadThreadIDs.has("other-chat")).toBe(false);
});

it("retains the complete checkpoint on request failure and on a failed persistence write", async () => {
  const fixture = setup();
  const view = fixture.mount();
  await waitFor(() => expect(saved().cursor).toBe("baseline"));
  fixture.get.mockRejectedValueOnce(new Error("offline"));
  await act(async () => { expect(await view.result.current.retry()).toBe(false); });
  expect(view.result.current.error).toBe(true);
  expect(saved().cursor).toBe("baseline");
  const setItem = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("quota"); });
  fixture.get.mockResolvedValue(page("new", [reply("retained-reply")]));
  await act(async () => { await view.result.current.retry(); });
  expect(view.result.current.unreadThreadIDs.has("other-chat")).toBe(true);
  expect(saved()).toEqual({ cursor: "baseline", threads: {} });
  setItem.mockRestore();
  view.unmount();
  const reloaded = fixture.mount();
  await waitFor(() => expect(reloaded.result.current.unreadThreadIDs.has("other-chat")).toBe(true));
  expect(fixture.get).toHaveBeenLastCalledWith("/threads/reply-updates", { limit: 100, cursor: "baseline" }, expect.any(AbortSignal));
  expect(saved().cursor).toBe("new");
});

it("isolates data stores, ignores the old in-flight response and resumes a previous namespace", async () => {
  const fixture = setup();
  const view = fixture.mount();
  await waitFor(() => expect(saved().cursor).toBe("baseline"));
  act(() => view.result.current.setRead("other-chat", false));
  const old = deferred<ThreadReplyUpdatesView>();
  fixture.get.mockReturnValueOnce(old.promise);
  let oldRequest!: Promise<boolean>;
  act(() => { oldRequest = view.result.current.retry(); });
  fixture.changeScope("database-two");
  fixture.get.mockResolvedValue(page("second-baseline", [], false, "database-two"));
  view.rerender();
  await waitFor(() => expect(saved("database-two").cursor).toBe("second-baseline"));
  await act(async () => { old.resolve(page("wrong-database", [reply("stale")])); await oldRequest; });
  expect(view.result.current.unreadThreadIDs.size).toBe(0);
  expect(saved("database-two").cursor).toBe("second-baseline");
  fixture.changeScope("database-one");
  fixture.get.mockResolvedValue(page("baseline"));
  view.rerender();
  await waitFor(() => expect(view.result.current.unreadThreadIDs.has("other-chat")).toBe(true));
  expect(fixture.get).toHaveBeenLastCalledWith("/threads/reply-updates", { limit: 100, cursor: "baseline" }, expect.any(AbortSignal));
});

it("refreshes health on a scope mismatch without applying that response to the old record", async () => {
  const fixture = setup();
  const view = fixture.mount();
  await waitFor(() => expect(saved().cursor).toBe("baseline"));
  fixture.get.mockResolvedValue(page("foreign", [reply("foreign-reply")], false, "database-two"));
  fixture.health.mockResolvedValue({ data_store_id: "database-two" });
  await act(async () => { expect(await view.result.current.retry()).toBe(false); });
  expect(useConnectionStore.getState().health?.data_store_id).toBe("database-two");
  expect(saved()).toEqual({ cursor: "baseline", threads: {} });
});

it("keeps malformed recovered state untouched and surfaces an unresolved scope mismatch", async () => {
  const record = JSON.stringify({ version: "v2_recovery.v1", value: { cursor: "invalid\ncheckpoint", threads: {} } });
  localStorage.setItem(key(), record);
  const fixture = setup();
  const view = fixture.mount();
  await waitFor(() => expect(fixture.get).toHaveBeenCalled());
  expect(fixture.get).toHaveBeenCalledWith("/threads/reply-updates", { limit: 100 }, expect.any(AbortSignal));
  expect(localStorage.getItem(key())).toBe(record);
  fixture.get.mockResolvedValue(page("foreign", [reply("foreign-reply")], false, "database-two"));
  await act(async () => { expect(await view.result.current.retry()).toBe(false); });
  expect(view.result.current.error).toBe(true);
  expect(view.result.current.unreadThreadIDs.size).toBe(0);
  expect(localStorage.getItem(key())).toBe(record);
});
