import { act, renderHook, waitFor } from "@testing-library/react";
import { useLayoutEffect } from "react";
import type { APIClient } from "../api/client";
import type { HealthView, PublicModelStreamSnapshot } from "../api/types";
import { useConnectionStore } from "../state/connection";
import { usePublicModelStream } from "./use-public-model-stream";

const first: PublicModelStreamSnapshot = {
  version: "model_public_stream.v3",
  call: {
    run_id: "run-1", session_id: "sess-1", attempt_id: "attempt-1",
    model_attempt: 1, transport_attempt: 1, max_attempts: 3, protocol_repair: 0,
    tool_round: 0, provider: "deepseek", model: "deepseek-chat",
    started_at: "2026-08-08T00:00:00Z", stream_chunks: 1, stream_bytes: 16,
    cancel_requested: false,
  },
  revision: 1, response_id: "response-1", event_sequence: 3, items: [],
  content_kind: "tool_commentary", text: "First safe text", message_complete: false,
  provisional: true, updated_at: "2026-08-08T00:00:01Z",
};

describe("usePublicModelStream", () => {
  it("does not expose the previous model snapshot on the first render after switching clients", async () => {
    const firstClient = { pollPublicModelStream: vi.fn().mockResolvedValue(first) } as unknown as APIClient;
    const nextClient = { pollPublicModelStream: vi.fn().mockReturnValue(new Promise(() => {})) } as unknown as APIClient;
    const observed: { client: APIClient; snapshot: PublicModelStreamSnapshot | null }[] = [];
    const hook = renderHook(({ client }) => {
      const state = usePublicModelStream(client, "run-1", true);
      useLayoutEffect(() => { observed.push({ client, snapshot: state.snapshot }); }, [client, state.snapshot]);
      return state;
    }, { initialProps: { client: firstClient } });
    await waitFor(() => expect(hook.result.current.snapshot).toEqual(first));
    hook.rerender({ client: nextClient });
    expect(observed.filter((entry) => entry.client === nextClient).every((entry) => entry.snapshot === null)).toBe(true);
    hook.unmount();
  });

  it("restarts model polling when the backing data store changes", async () => {
    useConnectionStore.setState({ health: { data_store_id: "store-one" } as HealthView });
    const pollPublicModelStream = vi.fn().mockResolvedValueOnce(first).mockReturnValue(new Promise(() => {}));
    const client = { pollPublicModelStream } as unknown as APIClient;
    const hook = renderHook(() => usePublicModelStream(client, "run-1", true));
    try {
      await waitFor(() => expect(hook.result.current.snapshot).toEqual(first));
      act(() => useConnectionStore.setState({ health: { data_store_id: "store-two" } as HealthView }));
      await waitFor(() => expect(pollPublicModelStream).toHaveBeenCalledTimes(2));
      expect(hook.result.current.snapshot).toBeNull();
      expect(hook.result.current.status).toBe("waiting");
    } finally {
      hook.unmount();
      useConnectionStore.setState({ health: null });
    }
  });

  it("ignores a late model response from the previous data store", async () => {
    useConnectionStore.setState({ health: { data_store_id: "store-one" } as HealthView });
    let resolveOld!: (value: PublicModelStreamSnapshot) => void;
    const oldPoll = new Promise<PublicModelStreamSnapshot>((resolve) => { resolveOld = resolve; });
    const fresh = { ...first, text: "New data store", revision: 2 };
    const pollPublicModelStream = vi.fn().mockReturnValueOnce(oldPoll).mockResolvedValue(fresh);
    const client = { pollPublicModelStream } as unknown as APIClient;
    const hook = renderHook(() => usePublicModelStream(client, "run-1", true));
    try {
      act(() => useConnectionStore.setState({ health: { data_store_id: "store-two" } as HealthView }));
      await waitFor(() => expect(hook.result.current.snapshot).toEqual(fresh));
      await act(async () => resolveOld(first));
      expect(hook.result.current.snapshot).toEqual(fresh);
    } finally {
      hook.unmount();
      useConnectionStore.setState({ health: null });
    }
  });

  it("backs off repeated transient errors and resets the delay after a successful poll", async () => {
    vi.useFakeTimers();
    try {
      const pollPublicModelStream = vi.fn()
        .mockRejectedValueOnce(new Error("offline"))
        .mockRejectedValueOnce(new Error("offline"))
        .mockRejectedValueOnce(new Error("offline"))
        .mockResolvedValueOnce(first)
        .mockRejectedValue(new Error("offline again"));
      const client = { pollPublicModelStream } as unknown as APIClient;
      const hook = renderHook(() => usePublicModelStream(client, "run-1", true));
      await act(async () => { await Promise.resolve(); });
      expect(pollPublicModelStream).toHaveBeenCalledTimes(1);
      await act(async () => vi.advanceTimersByTimeAsync(500));
      expect(pollPublicModelStream).toHaveBeenCalledTimes(2);
      await act(async () => vi.advanceTimersByTimeAsync(999));
      expect(pollPublicModelStream).toHaveBeenCalledTimes(2);
      await act(async () => vi.advanceTimersByTimeAsync(1));
      expect(pollPublicModelStream).toHaveBeenCalledTimes(3);
      await act(async () => vi.advanceTimersByTimeAsync(2_000));
      expect(pollPublicModelStream).toHaveBeenCalledTimes(4);
      expect(hook.result.current.status).toBe("live");
      await act(async () => vi.advanceTimersByTimeAsync(150));
      expect(pollPublicModelStream).toHaveBeenCalledTimes(5);
      await act(async () => vi.advanceTimersByTimeAsync(500));
      expect(pollPublicModelStream).toHaveBeenCalledTimes(6);
      hook.unmount();
    } finally {
      vi.useRealTimers();
    }
  });

  it("caps the reconnect delay and cancels pending retries on unmount", async () => {
    vi.useFakeTimers();
    try {
      const pollPublicModelStream = vi.fn().mockRejectedValue(new Error("offline"));
      const client = { pollPublicModelStream } as unknown as APIClient;
      const hook = renderHook(() => usePublicModelStream(client, "run-1", true));
      await act(async () => { await Promise.resolve(); });
      await act(async () => vi.advanceTimersByTimeAsync(15_500));
      expect(pollPublicModelStream).toHaveBeenCalledTimes(6);
      await act(async () => vi.advanceTimersByTimeAsync(7_999));
      expect(pollPublicModelStream).toHaveBeenCalledTimes(6);
      await act(async () => vi.advanceTimersByTimeAsync(1));
      expect(pollPublicModelStream).toHaveBeenCalledTimes(7);
      hook.unmount();
      await act(async () => vi.advanceTimersByTimeAsync(16_000));
      expect(pollPublicModelStream).toHaveBeenCalledTimes(7);
    } finally {
      vi.useRealTimers();
    }
  });

  it("uses a bounded idle cadence instead of probing an absent call at live-stream speed", async () => {
    vi.useFakeTimers();
    try {
      const pollPublicModelStream = vi.fn().mockResolvedValue(null);
      const client = { pollPublicModelStream } as unknown as APIClient;
      const hook = renderHook(() => usePublicModelStream(client, "run-1", true));

      await act(async () => { await Promise.resolve(); });
      expect(pollPublicModelStream).toHaveBeenCalledTimes(1);
      await act(async () => { await vi.advanceTimersByTimeAsync(999); });
      expect(pollPublicModelStream).toHaveBeenCalledTimes(1);
      await act(async () => { await vi.advanceTimersByTimeAsync(1); });
      expect(pollPublicModelStream).toHaveBeenCalledTimes(2);
      hook.unmount();
    } finally {
      vi.useRealTimers();
    }
  });

  it("recovers from an initial idle projection and converges on full revision snapshots", async () => {
    const second = { ...first, revision: 2, text: "Second safe text",
      updated_at: "2026-08-08T00:00:02Z" };
    const pollPublicModelStream = vi.fn()
      .mockResolvedValueOnce(null)
      .mockResolvedValueOnce(second)
      .mockResolvedValue(first);
    const client = { pollPublicModelStream } as unknown as APIClient;
    const { result, rerender } = renderHook(({ enabled }) =>
      usePublicModelStream(client, "run-1", enabled), {
      initialProps: { enabled: true },
    });

    await waitFor(() => expect(result.current.snapshot?.revision).toBe(2), { timeout: 1_500 });
    expect(result.current.snapshot?.text).toBe("Second safe text");
    expect(pollPublicModelStream.mock.calls[0]?.[0]).toBe("run-1");
    expect(pollPublicModelStream.mock.calls[0]?.[1]).toBeInstanceOf(AbortSignal);
    await new Promise((resolve) => window.setTimeout(resolve, 220));
    expect(result.current.snapshot?.revision).toBe(2);

    rerender({ enabled: false });
    await waitFor(() => expect(result.current.status).toBe("stopped"));
    expect(result.current.snapshot).toBeNull();
  });

  it("clears a finished provisional snapshot after a bounded 404 grace period", async () => {
    const pollPublicModelStream = vi.fn()
      .mockResolvedValueOnce(first)
      .mockResolvedValue(null);
    const client = { pollPublicModelStream } as unknown as APIClient;
    const { result } = renderHook(() => usePublicModelStream(client, "run-1", true));

    await waitFor(() => expect(result.current.snapshot?.revision).toBe(1));
    await waitFor(() => expect(result.current.status).toBe("finalizing"), { timeout: 1_000 });
    await waitFor(() => expect(result.current.snapshot).toBeNull(), { timeout: 2_000 });
    expect(result.current.status).toBe("waiting");
  });
});
