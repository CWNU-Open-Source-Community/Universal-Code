import { act, renderHook, waitFor } from "@testing-library/react";
import { useEffect, useLayoutEffect } from "react";
import { vi } from "vitest";
import { APIRequestError, type APIClient } from "../api/client";
import type { EventView, HealthView, RunEventPollView, RunEventStreamView } from "../api/types";
import { useConnectionStore } from "../state/connection";
import { clearDesktopRunEventMemory, useRunEventStream } from "./use-run-event-stream";

const runtime = vi.hoisted(() => ({ desktop: true }));
vi.mock("../lib/desktop-bridge", () => ({ desktopRuntimeActive: () => runtime.desktop }));

beforeEach(() => useConnectionStore.setState({ health: { data_store_id: "store-one" } as HealthView }));
afterEach(() => useConnectionStore.setState({ health: null }));

describe("useRunEventStream Desktop polling", () => {
  beforeEach(() => {
    runtime.desktop = true;
    clearDesktopRunEventMemory();
  });

  it("uses bounded stream cursors instead of offset pages or unsupported Windows streaming", async () => {
    const first = frame(1, "opaque-1");
    const second = frame(2, "opaque-2");
    const pollRunEvents = vi.fn()
      .mockResolvedValueOnce(poll([first], "opaque-1", true))
      .mockResolvedValueOnce(poll([second], "opaque-2", false));
    const streamRunEvents = vi.fn();
    const client = { pollRunEvents, streamRunEvents } as unknown as APIClient;
    const { result, unmount } = renderHook(() => useRunEventStream(client, "run-desktop"));

    await waitFor(() => expect(result.current.frames.map((item) => item.sequence)).toEqual([1, 2]));
    expect(result.current.status).toBe("live");
    expect(streamRunEvents).not.toHaveBeenCalled();
    expect(pollRunEvents).toHaveBeenNthCalledWith(1, "run-desktop", "", 100, expect.any(AbortSignal));
    expect(pollRunEvents).toHaveBeenNthCalledWith(2, "run-desktop", "opaque-1", 100,
      expect.any(AbortSignal));
    unmount();
  });

  it("resumes from bounded module memory after a component remount without browser storage", async () => {
    const localStorageWrite = vi.spyOn(Storage.prototype, "setItem");
    const pollRunEvents = vi.fn().mockResolvedValueOnce(poll([frame(1, "opaque-1")], "opaque-1", false))
      .mockResolvedValue(poll([frame(2, "opaque-2")], "opaque-2", false));
    const client = { pollRunEvents } as unknown as APIClient;
    const firstHook = renderHook(() => useRunEventStream(client, "run-desktop"));
    await waitFor(() => expect(firstHook.result.current.frames).toHaveLength(1));
    firstHook.unmount();

    const secondHook = renderHook(() => useRunEventStream(client, "run-desktop"));
    await waitFor(() => expect(secondHook.result.current.frames.map((item) => item.sequence)).toEqual([1, 2]));

    expect(pollRunEvents).toHaveBeenCalledWith("run-desktop", "opaque-1", 100, expect.any(AbortSignal));
    expect(localStorageWrite).not.toHaveBeenCalled();
    secondHook.unmount();
    localStorageWrite.mockRestore();
  });

  it("does not reuse another client's frames or cursor for the same Run ID", async () => {
    const firstClient = {
      pollRunEvents: vi.fn().mockResolvedValue(poll([frame(1, "old-cursor")], "old-cursor", false)),
    } as unknown as APIClient;
    const first = renderHook(() => useRunEventStream(firstClient, "run-desktop"));
    await waitFor(() => expect(first.result.current.frames).toHaveLength(1));
    first.unmount();

    const pollRunEvents = vi.fn().mockReturnValue(new Promise(() => {}));
    const secondClient = { pollRunEvents } as unknown as APIClient;
    const second = renderHook(() => useRunEventStream(secondClient, "run-desktop"));
    expect(second.result.current.frames).toEqual([]);
    expect(pollRunEvents).toHaveBeenCalledWith("run-desktop", "", 100, expect.any(AbortSignal));
    second.unmount();
  });

  it("never exposes the previous client's frames during the first render after a connection switch", async () => {
    const firstClient = {
      pollRunEvents: vi.fn().mockResolvedValue(poll([frame(1, "old-cursor")], "old-cursor", false)),
    } as unknown as APIClient;
    const secondClient = { pollRunEvents: vi.fn().mockReturnValue(new Promise(() => {})) } as unknown as APIClient;
    const observed: { client: APIClient; frames: RunEventStreamView[]; status: string }[] = [];
    const hook = renderHook(({ client }) => {
      const state = useRunEventStream(client, "run-desktop");
      useLayoutEffect(() => { observed.push({ client, frames: state.frames, status: state.status }); },
        [client, state.frames, state.status]);
      return state;
    }, { initialProps: { client: firstClient } });
    await waitFor(() => expect(hook.result.current.frames).toHaveLength(1));
    hook.rerender({ client: secondClient });
    expect(observed.filter((entry) => entry.client === secondClient).every((entry) => entry.frames.length === 0)).toBe(true);
    expect(observed.filter((entry) => entry.client === secondClient).every((entry) => entry.status !== "live")).toBe(true);
    hook.unmount();
  });

  it("restarts the same client's active poll when the backing data store changes", async () => {
    const pollRunEvents = vi.fn().mockResolvedValueOnce(poll([frame(1, "old-cursor")], "old-cursor", false))
      .mockReturnValue(new Promise(() => {}));
    const client = { pollRunEvents } as unknown as APIClient;
    const hook = renderHook(() => useRunEventStream(client, "run-desktop"));
    await waitFor(() => expect(hook.result.current.frames).toHaveLength(1));

    act(() => useConnectionStore.setState({ health: { data_store_id: "store-two" } as HealthView }));
    await waitFor(() => expect(pollRunEvents).toHaveBeenCalledTimes(2));
    expect(pollRunEvents).toHaveBeenLastCalledWith("run-desktop", "", 100, expect.any(AbortSignal));
    expect(hook.result.current.frames).toEqual([]);
    hook.unmount();
  });

  it("ignores a late event page from the previous data store", async () => {
    let resolveOld!: (value: RunEventPollView) => void;
    const oldPage = new Promise<RunEventPollView>((resolve) => { resolveOld = resolve; });
    const fresh = frame(1, "new-cursor");
    const pollRunEvents = vi.fn().mockReturnValueOnce(oldPage).mockResolvedValue(poll([fresh], fresh.cursor, false));
    const client = { pollRunEvents } as unknown as APIClient;
    const hook = renderHook(() => useRunEventStream(client, "run-desktop"));
    act(() => useConnectionStore.setState({ health: { data_store_id: "store-two" } as HealthView }));
    await waitFor(() => expect(hook.result.current.frames).toEqual([fresh]));
    await act(async () => resolveOld(poll([frame(1, "old-cursor")], "old-cursor", false)));
    expect(hook.result.current.frames).toEqual([fresh]);
    hook.unmount();
  });

  it("does not restore cached events when the data-store identity is unavailable", async () => {
    useConnectionStore.setState({ health: null });
    const pollRunEvents = vi.fn().mockResolvedValueOnce(poll([frame(1, "old-cursor")], "old-cursor", false))
      .mockReturnValue(new Promise(() => {}));
    const client = { pollRunEvents } as unknown as APIClient;
    const first = renderHook(() => useRunEventStream(client, "run-desktop"));
    await waitFor(() => expect(first.result.current.frames).toHaveLength(1));
    first.unmount();
    const second = renderHook(() => useRunEventStream(client, "run-desktop"));
    expect(second.result.current.frames).toEqual([]);
    expect(pollRunEvents).toHaveBeenLastCalledWith("run-desktop", "", 100, expect.any(AbortSignal));
    second.unmount();
  });

  it("drops one stale in-memory cursor and restarts exactly once from the durable beginning", async () => {
    const pollRunEvents = vi.fn().mockResolvedValue(poll([frame(1, "stale-cursor")], "stale-cursor", false));
    const client = { pollRunEvents } as unknown as APIClient;
    const prime = renderHook(() => useRunEventStream(client, "run-desktop"));
    await waitFor(() => expect(prime.result.current.frames).toHaveLength(1));
    prime.unmount();

    const current = frame(1, "current-cursor");
    pollRunEvents.mockReset().mockRejectedValueOnce(new APIRequestError("cursor mismatch", "INVALID_ARGUMENT", 400, "req-stale"))
      .mockResolvedValue(poll([current], "current-cursor", false));
    const hook = renderHook(() => useRunEventStream(client, "run-desktop"));

    await waitFor(() => expect(hook.result.current.frames).toEqual([current]));
    expect(pollRunEvents).toHaveBeenNthCalledWith(1, "run-desktop", "stale-cursor", 100,
      expect.any(AbortSignal));
    expect(pollRunEvents).toHaveBeenNthCalledWith(2, "run-desktop", "", 100, expect.any(AbortSignal));
    expect(hook.result.current.error).toBe("");
    hook.unmount();
  });
});

describe("useRunEventStream Desktop polling pace", () => {
  beforeEach(() => {
    runtime.desktop = true;
    clearDesktopRunEventMemory();
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it.each([false, true])("keeps caught-up frame references and stops render/effect work with initial frames=%s", async (hasFrames) => {
    let pages = 0;
    const pollRunEvents = vi.fn().mockImplementation(() => {
      pages++;
      return Promise.resolve(poll(pages === 1 && hasFrames ? [frame(1, "cursor-1")] : [], `cursor-${pages}`, false));
    });
    const renders = vi.fn();
    const frameEffects = vi.fn();
    const latestEffects = vi.fn();
    const client = { pollRunEvents } as unknown as APIClient;
    const hook = renderHook(() => {
      const state = useRunEventStream(client, "run-empty");
      renders();
      useEffect(() => { frameEffects(); }, [state.frames]);
      const latest = state.frames.at(-1);
      useEffect(() => { latestEffects(); }, [latest]);
      return state;
    });
    await flushMicrotasks();
    expect(hook.result.current.status).toBe("live");
    const frames = hook.result.current.frames;
    const counts = [renders.mock.calls.length, frameEffects.mock.calls.length, latestEffects.mock.calls.length];
    await act(async () => vi.advanceTimersByTimeAsync(1_000));
    expect(pollRunEvents).toHaveBeenCalledTimes(5);
    expect(pollRunEvents).toHaveBeenNthCalledWith(5, "run-empty", "cursor-4", 100, expect.any(AbortSignal));
    expect(hook.result.current.frames).toBe(frames);
    expect([renders.mock.calls.length, frameEffects.mock.calls.length, latestEffects.mock.calls.length]).toEqual(counts);
    hook.unmount();

    // Empty pages still advance the confirmed cursor and keep the same bounded
    // immutable frame buffer for a later mount.
    const remount = renderHook(() => useRunEventStream(client, "run-empty"));
    await flushMicrotasks();
    expect(pollRunEvents).toHaveBeenNthCalledWith(6, "run-empty", "cursor-5", 100, expect.any(AbortSignal));
    expect(remount.result.current.frames).toBe(frames);
    remount.unmount();
  });

  it("keeps replayed frame identities without render/effect work while advancing the page cursor", async () => {
    const first = frame(1, "cursor-1");
    const replay = { ...frame(1, "cursor-replay"), request_id: "different-request" };
    const next = frame(2, "cursor-2");
    const pollRunEvents = vi.fn().mockResolvedValueOnce(poll([first], first.cursor, false))
      .mockResolvedValueOnce(poll([replay], replay.cursor, false))
      .mockResolvedValueOnce(poll([replay, next], next.cursor, false));
    const renders = vi.fn();
    const effects = vi.fn();
    const client = { pollRunEvents } as unknown as APIClient;
    const hook = renderHook(() => {
      const state = useRunEventStream(client, "run-desktop");
      renders();
      useEffect(() => { effects(); }, [state.frames, state.frames.at(-1)]);
      return state;
    });
    await flushMicrotasks();
    const frames = hook.result.current.frames;
    const counts = [renders.mock.calls.length, effects.mock.calls.length];
    await act(async () => vi.advanceTimersByTime(250));
    expect(hook.result.current.frames).toBe(frames);
    expect(hook.result.current.frames[0]).toBe(first);
    expect([renders.mock.calls.length, effects.mock.calls.length]).toEqual(counts);
    await act(async () => vi.advanceTimersByTime(250));
    expect(pollRunEvents).toHaveBeenNthCalledWith(3, "run-desktop", replay.cursor, 100, expect.any(AbortSignal));
    expect(hook.result.current.frames).toEqual([first, next]);
    expect(hook.result.current.frames[0]).toBe(first);
    expect(effects).toHaveBeenCalledTimes(counts[1]! + 1);
    hook.unmount();
  });

  it("retains the latest 500 frames and ignores a trimmed replay before publishing a new terminal event", async () => {
    let pages = 0;
    const terminal = { ...frame(601, "cursor-terminal"), event: { ...event(601), type: "run.completed" } };
    const pollRunEvents = vi.fn().mockImplementation(() => {
      pages++;
      if (pages === 7) return Promise.resolve(poll([frame(50, "cursor-replay")], "cursor-replay", false));
      if (pages === 8) return Promise.resolve(poll([terminal], terminal.cursor, false));
      const frames = Array.from({ length: 100 }, (_, index) => frame((pages - 1) * 100 + index + 1, `cursor-${(pages - 1) * 100 + index + 1}`));
      return Promise.resolve(poll(frames, frames.at(-1)!.cursor, pages < 6));
    });
    const client = { pollRunEvents } as unknown as APIClient;
    const hook = renderHook(() => useRunEventStream(client, "run-desktop"));
    await flushMicrotasks();
    await act(async () => vi.advanceTimersByTime(250));
    expect(pollRunEvents).toHaveBeenCalledTimes(6);
    const frames = hook.result.current.frames;
    expect(frames).toHaveLength(500);
    expect([frames[0]!.sequence, frames.at(-1)!.sequence]).toEqual([101, 600]);
    await act(async () => vi.advanceTimersByTime(250));
    expect(hook.result.current.frames).toBe(frames);
    await act(async () => vi.advanceTimersByTime(250));
    expect(pollRunEvents).toHaveBeenNthCalledWith(8, "run-desktop", "cursor-replay", 100, expect.any(AbortSignal));
    expect(hook.result.current.frames).toHaveLength(500);
    expect(hook.result.current.frames[0]).toBe(frames[1]);
    expect(hook.result.current.frames.at(-1)).toBe(terminal);
    hook.unmount();
  });

  it("recovers from a transient polling failure even when the first successful page is empty", async () => {
    const pollRunEvents = vi.fn().mockRejectedValueOnce(new Error("poll disconnected"))
      .mockResolvedValue(poll([], "cursor-current", false));
    const client = { pollRunEvents } as unknown as APIClient;
    const hook = renderHook(() => useRunEventStream(client, "run-desktop"));
    await flushMicrotasks();
    expect(hook.result.current.status).toBe("reconnecting");
    expect(hook.result.current.error).toBe("poll disconnected");
    await act(async () => vi.advanceTimersByTime(1_000));
    expect(hook.result.current.status).toBe("live");
    expect(hook.result.current.error).toBe("");
    expect(hook.result.current.frames).toHaveLength(0);
    hook.unmount();
  });

  it("backs off after an empty caught-up page instead of polling in a tight loop", async () => {
    const pollRunEvents = vi.fn().mockResolvedValue(poll([], "caught-up", false));
    const client = { pollRunEvents } as unknown as APIClient;
    const hook = renderHook(() => useRunEventStream(client, "run-empty"));

    await flushMicrotasks();
    expect(pollRunEvents).toHaveBeenCalledTimes(1);

    await flushMicrotasks();
    expect(pollRunEvents).toHaveBeenCalledTimes(1);

    await act(async () => vi.advanceTimersByTime(249));
    expect(pollRunEvents).toHaveBeenCalledTimes(1);

    await act(async () => vi.advanceTimersByTime(1));
    expect(pollRunEvents).toHaveBeenCalledTimes(2);
    hook.unmount();
  });

  it("drains a persistent backlog in bounded immediate bursts", async () => {
    let sequence = 0;
    const pollRunEvents = vi.fn().mockImplementation(() => {
      sequence++;
      return Promise.resolve(poll([frame(sequence, `cursor-${sequence}`)], `cursor-${sequence}`, true));
    });
    const client = { pollRunEvents } as unknown as APIClient;
    const hook = renderHook(() => useRunEventStream(client, "run-backlog"));

    await flushMicrotasks();
    expect(pollRunEvents).toHaveBeenCalledTimes(4);

    await flushMicrotasks();
    expect(pollRunEvents).toHaveBeenCalledTimes(4);

    await act(async () => vi.advanceTimersByTime(250));
    expect(pollRunEvents).toHaveBeenCalledTimes(8);
    hook.unmount();
  });

  it("aborts the previous poll and delay immediately when the Run changes", async () => {
    const signals = new Map<string, AbortSignal>();
    const pollRunEvents = vi.fn().mockImplementation((runID: string, _cursor: string, _limit: number,
      signal: AbortSignal) => {
      signals.set(runID, signal);
      return Promise.resolve(poll([], `${runID}-caught-up`, false));
    });
    const client = { pollRunEvents } as unknown as APIClient;
    const hook = renderHook(({ runID }) => useRunEventStream(client, runID), {
      initialProps: { runID: "run-old" },
    });

    await flushMicrotasks();
    expect(pollRunEvents).toHaveBeenCalledTimes(1);
    expect(signals.get("run-old")?.aborted).toBe(false);

    hook.rerender({ runID: "run-new" });
    await flushMicrotasks();
    expect(signals.get("run-old")?.aborted).toBe(true);
    expect(pollRunEvents.mock.calls.filter(([runID]) => runID === "run-new")).toHaveLength(1);

    await act(async () => vi.advanceTimersByTime(250));
    expect(pollRunEvents.mock.calls.filter(([runID]) => runID === "run-old")).toHaveLength(1);
    hook.unmount();
    expect(signals.get("run-new")?.aborted).toBe(true);
  });
});

describe("useRunEventStream browser SSE", () => {
  beforeEach(() => {
    runtime.desktop = false;
    vi.useFakeTimers();
  });

  afterEach(() => { vi.useRealTimers(); });

  function streamClient() {
    const connections: { options: Parameters<APIClient["streamRunEvents"]>[1];
      resolve: () => void; reject: (error: Error) => void }[] = [];
    const streamRunEvents = vi.fn((_runID: string, options: Parameters<APIClient["streamRunEvents"]>[1]) =>
      new Promise<void>((resolve, reject) => { connections.push({ options, resolve, reject }); }));
    return { connections, streamRunEvents, client: { streamRunEvents } as unknown as APIClient };
  }

  it("reports an open empty stream as live and reuses duplicate frames without renders or effects", async () => {
    const { client, connections } = streamClient();
    const renders = vi.fn();
    const effects = vi.fn();
    const hook = renderHook(() => {
      const state = useRunEventStream(client, "run-web");
      renders();
      useEffect(() => { effects(); }, [state.frames]);
      return state;
    });
    await flushMicrotasks();
    expect(hook.result.current.status).toBe("connecting");
    await act(async () => { connections[0]!.options.onOpen?.(); });
    expect(hook.result.current.status).toBe("live");
    expect(hook.result.current.frames).toHaveLength(0);
    const first = frame(1, "cursor-first");
    await act(async () => { connections[0]!.options.onFrame(first); });
    const frames = hook.result.current.frames;
    const counts = [renders.mock.calls.length, effects.mock.calls.length];
    await act(async () => {
      connections[0]!.options.onFrame({ ...frame(1, "cursor-replay"), request_id: "replayed-request" });
    });
    expect(hook.result.current.frames).toBe(frames);
    expect(hook.result.current.frames[0]).toBe(first);
    expect([renders.mock.calls.length, effects.mock.calls.length]).toEqual(counts);
    hook.unmount();
  });

  it("reconnects from the confirmed duplicate cursor and clears an error when an empty stream opens", async () => {
    const { client, connections, streamRunEvents } = streamClient();
    const hook = renderHook(() => useRunEventStream(client, "run-web"));
    await flushMicrotasks();
    await act(async () => {
      connections[0]!.options.onOpen?.();
      connections[0]!.options.onFrame(frame(1, "cursor-first"));
      connections[0]!.options.onFrame(frame(1, "cursor-replay"));
      connections[0]!.reject(new Error("SSE disconnected"));
    });
    expect(hook.result.current.status).toBe("reconnecting");
    expect(hook.result.current.error).toBe("SSE disconnected");
    const frames = hook.result.current.frames;
    await act(async () => vi.advanceTimersByTimeAsync(1_000));
    expect(streamRunEvents).toHaveBeenCalledTimes(2);
    expect(connections[1]!.options.cursor).toBe("cursor-replay");
    await act(async () => { connections[1]!.options.onOpen?.(); });
    expect(hook.result.current.status).toBe("live");
    expect(hook.result.current.error).toBe("");
    expect(hook.result.current.frames).toBe(frames);
    await act(async () => { connections[1]!.resolve(); });
    expect(hook.result.current.status).toBe("reconnecting");
    await act(async () => vi.advanceTimersByTimeAsync(1_000));
    expect(streamRunEvents).toHaveBeenCalledTimes(3);
    expect(connections[2]!.options.cursor).toBe("cursor-replay");
    hook.unmount();
  });

  it("restarts once from the beginning when a reconnect cursor is rejected", async () => {
    const { client, connections, streamRunEvents } = streamClient();
    const hook = renderHook(() => useRunEventStream(client, "run-web"));
    await flushMicrotasks();
    await act(async () => {
      connections[0]!.options.onFrame(frame(1, "stale-cursor"));
      connections[0]!.resolve();
    });
    await act(async () => vi.advanceTimersByTimeAsync(1_000));
    expect(streamRunEvents).toHaveBeenCalledTimes(2);
    expect(connections[1]!.options.cursor).toBe("stale-cursor");
    await act(async () => {
      connections[1]!.reject(new APIRequestError("cursor rejected", "INVALID_ARGUMENT", 400));
    });
    expect(streamRunEvents).toHaveBeenCalledTimes(3);
    expect(connections[2]!.options.cursor).toBe("");
    expect(hook.result.current.frames).toEqual([]);
    await act(async () => {
      connections[2]!.reject(new APIRequestError("bad request", "INVALID_ARGUMENT", 400));
    });
    expect(hook.result.current.status).toBe("stopped");
    expect(hook.result.current.error).toBe("bad request");
    await act(async () => vi.advanceTimersByTimeAsync(2_000));
    expect(streamRunEvents).toHaveBeenCalledTimes(3);
    hook.unmount();
  });

  it("retains terminal events, stops on authorization failure, and ignores an aborted Run's callbacks", async () => {
    const { client, connections, streamRunEvents } = streamClient();
    const hook = renderHook(({ runID }) => useRunEventStream(client, runID), { initialProps: { runID: "run-old" } });
    await flushMicrotasks();
    const terminal = { ...frame(2, "cursor-terminal"), event: { ...event(2), type: "run.completed" } };
    await act(async () => {
      connections[0]!.options.onOpen?.();
      connections[0]!.options.onFrame(terminal);
      connections[0]!.reject(new APIRequestError("access revoked", "PERMISSION_DENIED", 403));
    });
    expect(hook.result.current.frames.at(-1)).toBe(terminal);
    expect(hook.result.current.status).toBe("stopped");
    expect(hook.result.current.error).toBe("access revoked");
    await act(async () => vi.advanceTimersByTimeAsync(2_000));
    expect(streamRunEvents).toHaveBeenCalledTimes(1);
    hook.rerender({ runID: "run-new" });
    await flushMicrotasks();
    expect(connections[0]!.options.signal.aborted).toBe(true);
    await act(async () => {
      connections[0]!.options.onOpen?.();
      connections[0]!.options.onFrame(terminal);
    });
    expect(hook.result.current.frames).toHaveLength(0);
    expect(hook.result.current.status).toBe("connecting");
    expect(hook.result.current.error).toBe("");
    hook.unmount();
  });
});

async function flushMicrotasks(): Promise<void> {
  await act(async () => {
    for (let index = 0; index < 12; index++) {
      await Promise.resolve();
    }
  });
}

function poll(frames: RunEventStreamView[], cursor: string, hasMore: boolean): RunEventPollView {
  return {
    version: "run-event-poll.v1",
    run_id: "run-desktop",
    cursor,
    frames,
    has_more: hasMore,
  };
}

function frame(sequence: number, cursor: string): RunEventStreamView {
  return {
    version: "run-events.v1",
    request_id: `req-${sequence}`,
    run_id: "run-desktop",
    sequence,
    cursor,
    event: event(sequence),
  };
}

function event(sequence: number): EventView {
  return {
    version: "v1",
    event_id: `event-${sequence}`,
    mission_id: "mission-desktop",
    run_id: "run-desktop",
    sequence,
    type: "run.updated",
    source: "test",
    payload: {},
    created_at: "2026-07-18T00:00:00Z",
  };
}
