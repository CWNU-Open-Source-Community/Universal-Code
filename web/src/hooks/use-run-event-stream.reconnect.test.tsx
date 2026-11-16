import { act, renderHook } from "@testing-library/react";
import { vi } from "vitest";
import { StreamProtocolError, type APIClient } from "../api/client";
import type { HealthView, RunEventPollView, RunEventStreamView } from "../api/types";
import { useConnectionStore } from "../state/connection";
import { clearDesktopRunEventMemory, useRunEventStream } from "./use-run-event-stream";

const runtime = vi.hoisted(() => ({ desktop: true }));
vi.mock("../lib/desktop-bridge", () => ({ desktopRuntimeActive: () => runtime.desktop }));

interface StreamConnection {
  options: {
    cursor: string;
    onOpen?: () => void;
    onFrame: (frame: RunEventStreamView) => void;
  };
  resolve: () => void;
  reject: (error: unknown) => void;
}

function streamClient(): {
  client: APIClient;
  connections: StreamConnection[];
  streamRunEvents: ReturnType<typeof vi.fn>;
} {
  const connections: StreamConnection[] = [];
  const streamRunEvents = vi.fn(
    (_runID: string, options: StreamConnection["options"] & { signal: AbortSignal }) => {
      const connection: StreamConnection = {
        options: { cursor: options.cursor, onOpen: options.onOpen, onFrame: options.onFrame },
        resolve: () => undefined,
        reject: () => undefined,
      };
      connections.push(connection);
      return new Promise<void>((resolve, reject) => {
        connection.resolve = resolve;
        connection.reject = reject;
      });
    },
  );
  const client = { streamRunEvents } as unknown as APIClient;
  return { client, connections, streamRunEvents };
}

function frame(sequence: number, cursor: string): RunEventStreamView {
  return {
    version: "run-events.v1",
    run_id: "run-web",
    request_id: "request-web",
    cursor,
    sequence,
    event: {
      version: "v1",
      run_id: "run-web",
      sequence,
      event_id: `event-${sequence}`,
      mission_id: "mission-web",
      type: "run.progress",
      source: "test",
      created_at: "2026-11-17T00:00:00Z",
      payload_json: "{}",
    },
  } as unknown as RunEventStreamView;
}

function poll(frames: RunEventStreamView[], cursor: string, hasMore: boolean): RunEventPollView {
  return { version: "run-event-poll.v1", run_id: "run-web", cursor, frames, has_more: hasMore };
}

async function flushMicrotasks(): Promise<void> {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

beforeEach(() => useConnectionStore.setState({ health: { data_store_id: "store-reconnect" } as HealthView }));
afterEach(() => useConnectionStore.setState({ health: null }));

describe("useRunEventStream protocol-error recovery", () => {
  beforeEach(() => {
    runtime.desktop = true;
    clearDesktopRunEventMemory();
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("replays once from scratch on a protocol-invalid poll, then backs off", async () => {
    const pollRunEvents = vi.fn()
      .mockResolvedValueOnce(poll([frame(1, "cursor-1")], "cursor-1", false))
      .mockRejectedValue(new StreamProtocolError("Event poll response contains a sequence gap"));
    const client = { pollRunEvents } as unknown as APIClient;
    const hook = renderHook(() => useRunEventStream(client, "run-protocol"));
    await flushMicrotasks();
    expect(pollRunEvents).toHaveBeenCalledTimes(1);
    expect(hook.result.current.frames).toHaveLength(1);
    // The caught-up delay elapses, the next poll fails protocol validation,
    // and the advanced cursor makes the failure resettable: the one-shot
    // reset replays immediately with an empty cursor.
    await act(async () => vi.advanceTimersByTimeAsync(250));
    expect(pollRunEvents).toHaveBeenCalledTimes(3);
    expect(pollRunEvents).toHaveBeenLastCalledWith("run-protocol", "", 100, expect.any(AbortSignal));
    expect(hook.result.current.frames).toHaveLength(0);
    // The second protocol failure backs off instead of replaying again.
    await act(async () => vi.advanceTimersByTimeAsync(999));
    expect(pollRunEvents).toHaveBeenCalledTimes(3);
    await act(async () => vi.advanceTimersByTimeAsync(1));
    expect(pollRunEvents).toHaveBeenCalledTimes(4);
    await act(async () => vi.advanceTimersByTimeAsync(1_999));
    expect(pollRunEvents).toHaveBeenCalledTimes(4);
    await act(async () => vi.advanceTimersByTimeAsync(1));
    expect(pollRunEvents).toHaveBeenCalledTimes(5);
    expect(hook.result.current.status).toBe("reconnecting");
    hook.unmount();
  });

  it("backs off across consecutive transport failures instead of polling every second", async () => {
    const pollRunEvents = vi.fn().mockRejectedValue(new Error("poll disconnected"));
    const client = { pollRunEvents } as unknown as APIClient;
    const hook = renderHook(() => useRunEventStream(client, "run-backoff"));
    await flushMicrotasks();
    expect(pollRunEvents).toHaveBeenCalledTimes(1);
    await act(async () => vi.advanceTimersByTimeAsync(999));
    expect(pollRunEvents).toHaveBeenCalledTimes(1);
    await act(async () => vi.advanceTimersByTimeAsync(1));
    expect(pollRunEvents).toHaveBeenCalledTimes(2);
    await act(async () => vi.advanceTimersByTimeAsync(1_999));
    expect(pollRunEvents).toHaveBeenCalledTimes(2);
    await act(async () => vi.advanceTimersByTimeAsync(1));
    expect(pollRunEvents).toHaveBeenCalledTimes(3);
    await act(async () => vi.advanceTimersByTimeAsync(3_999));
    expect(pollRunEvents).toHaveBeenCalledTimes(3);
    await act(async () => vi.advanceTimersByTimeAsync(1));
    expect(pollRunEvents).toHaveBeenCalledTimes(4);
    expect(hook.result.current.status).toBe("reconnecting");
    hook.unmount();
  });
});

describe("useRunEventStream browser SSE protocol recovery", () => {
  beforeEach(() => {
    runtime.desktop = false;
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("replays once from scratch on a protocol-invalid frame, then backs off with the cursor preserved", async () => {
    const { client, connections, streamRunEvents } = streamClient();
    const hook = renderHook(() => useRunEventStream(client, "run-web"));
    await flushMicrotasks();
    expect(connections.length).toBe(1);
    await act(async () => {
      connections[0]!.options.onOpen?.();
      connections[0]!.options.onFrame(frame(1, "cursor-first"));
      connections[0]!.reject(new StreamProtocolError("SSE frame does not match run-events.v1"));
    });
    await flushMicrotasks();
    // The confirmed cursor makes the protocol failure resettable: the one-shot
    // reset replays immediately with an empty cursor.
    expect(connections.length).toBe(2);
    expect(connections[1]!.options.cursor).toBe("");
    await act(async () => { connections[1]!.reject(new StreamProtocolError("SSE frame does not match run-events.v1")); });
    await flushMicrotasks();
    // The second protocol failure backs off instead of replaying again.
    await act(async () => vi.advanceTimersByTimeAsync(999));
    expect(connections.length).toBe(2);
    await act(async () => vi.advanceTimersByTimeAsync(1));
    expect(connections.length).toBe(3);
    await act(async () => {
      connections[2]!.options.onOpen?.();
      connections[2]!.options.onFrame(frame(1, "cursor-live"));
      connections[2]!.reject(new Error("SSE disconnected"));
    });
    await flushMicrotasks();
    // A transport failure keeps the confirmed cursor; the reconnect stays on
    // the same bounded curve.
    await act(async () => vi.advanceTimersByTimeAsync(999));
    expect(connections.length).toBe(3);
    await act(async () => vi.advanceTimersByTimeAsync(1));
    expect(connections.length).toBe(4);
    expect(connections[3]!.options.cursor).toBe("cursor-live");
    expect(hook.result.current.frames.map((item) => item.sequence)).toEqual([1]);
    hook.unmount();
  });
});
