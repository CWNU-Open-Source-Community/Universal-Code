import { useRef } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider, useLocale } from "../lib/locale";
import { useConnectionStore } from "../state/connection";
import {
  MAX_SCROLL_MEMORY_THREADS,
  readPersistedScrollMemoryMap,
  readPersistedThreadScrollMemory,
  scrollMemoryQueryKey,
  scrollMemoryStorageKey,
  useV2ScrollMemory,
  writePersistedThreadScrollMemory,
  type ScrollMemoryEntryLike,
} from "./use-scroll-memory";

interface TestHarnessProps {
  threadID: string;
  entries: ScrollMemoryEntryLike[];
  ready?: boolean;
  scope?: string;
  entryMetrics?: Record<string, { top: number; height: number }>;
  scrollMetrics: { scrollHeight: number; clientHeight: number };
}

function ScrollMemoryTestHarness({
  threadID,
  entries,
  ready = true,
  scope,
  entryMetrics,
  scrollMetrics,
}: TestHarnessProps) {
  const { t } = useLocale();
  const scrollRef = useRef<HTMLDivElement>(null);
  const memory = useV2ScrollMemory({
    threadID,
    scrollRef,
    entries,
    ready,
    scope,
  });

  return (
    <div className="v2-conversation-viewport">
      <div
        className="v2-conversation-scroll"
        data-following={String(memory.following)}
        data-testid="scroller"
        onScroll={memory.onScroll}
        ref={(node) => {
          scrollRef.current = node;
          if (!node) return;
          let currentScrollTop = node.scrollTop;
          Object.defineProperties(node, {
            scrollHeight: {
              configurable: true,
              get: () => scrollMetrics.scrollHeight,
            },
            clientHeight: {
              configurable: true,
              get: () => scrollMetrics.clientHeight,
            },
            scrollTop: {
              configurable: true,
              get: () => currentScrollTop,
              set: (next: number) => {
                const max = Math.max(0, scrollMetrics.scrollHeight - scrollMetrics.clientHeight);
                currentScrollTop = Math.max(0, Math.min(next, max));
              },
            },
          });
          node.getBoundingClientRect = () => ({
            top: 0,
            bottom: scrollMetrics.clientHeight,
            left: 0,
            right: 600,
            width: 600,
            height: scrollMetrics.clientHeight,
            x: 0,
            y: 0,
            toJSON: () => ({}),
          });
        }}
      >
        <div className="v2-conversation-content">
          <ol className="v2-narrative v2-conv-entries">
            {entries.map((entry) => (
              <li
                data-conv-entry-key={entry.id}
                key={entry.id}
                ref={(row) => {
                  if (!row || !entryMetrics?.[entry.id]) return;
                  row.getBoundingClientRect = () => {
                    const metric = entryMetrics[entry.id]!;
                    const scrollerTop = scrollRef.current?.scrollTop ?? 0;
                    const top = metric.top - scrollerTop;
                    return {
                      top,
                      bottom: top + metric.height,
                      left: 0,
                      right: 560,
                      width: 560,
                      height: metric.height,
                      x: 0,
                      y: top,
                      toJSON: () => ({}),
                    };
                  };
                }}
              >
                {entry.id}
              </li>
            ))}
          </ol>
        </div>
      </div>
      {memory.showScrollToBottom && (
        <button
          className={`v2-jump-latest${memory.isUnreadAnchored ? " has-unread" : ""}`}
          onClick={memory.jumpToBottom}
          type="button"
        >
          <span>{memory.isUnreadAnchored ? t("↓ 跳至最新", "↓ Jump to bottom") : t("↓ 回到底部", "↓ Scroll to bottom")}</span>
          {memory.unreadCount > 0 && (
            <span className="v2-jump-latest-badge" data-testid="scroll-unread-badge">
              {memory.unreadCount}
            </span>
          )}
        </button>
      )}
    </div>
  );
}

describe("useV2ScrollMemory", () => {
  beforeEach(() => {
    window.localStorage.clear();
    useConnectionStore.getState().disconnect();
  });

  afterEach(() => {
    cleanup();
    window.localStorage.clear();
    useConnectionStore.getState().disconnect();
    vi.unstubAllGlobals();
  });

  it("persists mid-thread scroll position across unmount/remount and hydrates from data_store_id localStorage on reload", () => {
    useConnectionStore.getState().setHealth({
      status: "ok",
      data_store_id: "store-alpha",
    } as Parameters<ReturnType<typeof useConnectionStore.getState>["setHealth"]>[0]);

    const entries: ScrollMemoryEntryLike[] = [
      { id: "u-1", kind: "user" },
      { id: "a-1", kind: "assistant" },
      { id: "u-2", kind: "user" },
      { id: "a-2", kind: "assistant" },
    ];
    const entryMetrics = {
      "u-1": { top: 0, height: 120 },
      "a-1": { top: 120, height: 480 },
      "u-2": { top: 600, height: 120 },
      "a-2": { top: 720, height: 900 },
    };
    const scrollMetrics = { scrollHeight: 1620, clientHeight: 400 };
    const firstClient = new QueryClient();

    const firstMount = render(
      <LocaleProvider>
        <QueryClientProvider client={firstClient}>
          <ScrollMemoryTestHarness
            entries={entries}
            entryMetrics={entryMetrics}
            scrollMetrics={scrollMetrics}
            threadID="thread-1"
          />
        </QueryClientProvider>
      </LocaleProvider>,
    );

    const scroller = screen.getByTestId("scroller");
    // Initial mount follows latest at bottom (1620 - 400 = 1220).
    expect(scroller.scrollTop).toBe(1220);

    // User scrolls up to read entry u-2 (scrollTop = 620 -> u-2 top is -20px relative to viewport).
    scroller.scrollTop = 620;
    fireEvent.scroll(scroller);

    expect(scroller).toHaveAttribute("data-following", "false");
    expect(screen.getByRole("button", { name: "↓ 回到底部" })).toBeInTheDocument();

    const cached = firstClient.getQueryData(scrollMemoryQueryKey("store-alpha", "thread-1"));
    expect(cached).toMatchObject({
      top: 620,
      following: false,
      anchorKey: "u-2",
      anchorOffset: -20,
      seenEntryCount: 4,
    });

    const stored = readPersistedThreadScrollMemory("store-alpha", "thread-1");
    expect(stored).toMatchObject({
      top: 620,
      following: false,
      anchorKey: "u-2",
      anchorOffset: -20,
      seenEntryCount: 4,
    });
    expect(window.localStorage.getItem(scrollMemoryStorageKey("store-beta"))).toBeNull();

    // Unmount and simulate an earlier entry expanding by +150px before remounting in a brand-new QueryClient (browser reload).
    firstMount.unmount();

    const reloadedMetrics = {
      "u-1": { top: 0, height: 270 },
      "a-1": { top: 270, height: 480 },
      "u-2": { top: 750, height: 120 },
      "a-2": { top: 870, height: 900 },
    };
    const reloadedScrollMetrics = { scrollHeight: 1770, clientHeight: 400 };
    const reloadedClient = new QueryClient();

    render(
      <LocaleProvider>
        <QueryClientProvider client={reloadedClient}>
          <ScrollMemoryTestHarness
            entries={entries}
            entryMetrics={reloadedMetrics}
            scrollMetrics={reloadedScrollMetrics}
            threadID="thread-1"
          />
        </QueryClientProvider>
      </LocaleProvider>,
    );

    const remountedScroller = screen.getByTestId("scroller");
    // Restores by anchorKey ("u-2" at 750) - anchorOffset (-20) = 770 rather than stale top 620!
    expect(remountedScroller.scrollTop).toBe(770);
    expect(remountedScroller).toHaveAttribute("data-following", "false");
  });

  it("enforces bounded LRU eviction in localStorage while preserving the most recent threads", () => {
    const scope = "lru-scope";
    for (let index = 1; index <= MAX_SCROLL_MEMORY_THREADS + 12; index++) {
      writePersistedThreadScrollMemory(scope, `thread-${index}`, {
        top: index * 10,
        following: false,
        anchorKey: `entry-${index}`,
        anchorOffset: 0,
        seenEntryCount: index,
        updatedAt: index * 1_000,
      });
    }

    const map = readPersistedScrollMemoryMap(scope);
    const keys = Object.keys(map);
    expect(keys).toHaveLength(MAX_SCROLL_MEMORY_THREADS);
    expect(map["thread-1"]).toBeUndefined();
    expect(map["thread-12"]).toBeUndefined();
    expect(map["thread-13"]).toBeDefined();
    expect(map[`thread-${MAX_SCROLL_MEMORY_THREADS + 12}`]).toBeDefined();
  });

  it("guards onScroll and ResizeObserver against transient layout clamps so following=false never flips to true", () => {
    const resizeCallbacks = new Set<() => void>();
    vi.stubGlobal("ResizeObserver", class {
      private trigger: () => void;
      constructor(callback: ResizeObserverCallback) {
        this.trigger = () => callback([], this as unknown as ResizeObserver);
        resizeCallbacks.add(this.trigger);
      }
      observe() {}
      disconnect() {
        resizeCallbacks.delete(this.trigger);
      }
    });

    const entries: ScrollMemoryEntryLike[] = [
      { id: "u-1", kind: "user" },
      { id: "a-1", kind: "assistant" },
      { id: "u-2", kind: "user" },
      { id: "a-2", kind: "assistant" },
    ];
    const scrollMetrics = { scrollHeight: 2200, clientHeight: 500 };
    const queryClient = new QueryClient();

    const ui = render(
      <LocaleProvider>
        <QueryClientProvider client={queryClient}>
          <ScrollMemoryTestHarness
            entries={entries}
            scope="clamp-scope"
            scrollMetrics={scrollMetrics}
            threadID="thread-a"
          />
        </QueryClientProvider>
      </LocaleProvider>,
    );

    const scroller = screen.getByTestId("scroller");
    scroller.scrollTop = 850;
    fireEvent.scroll(scroller);
    expect(scroller).toHaveAttribute("data-following", "false");

    // Switch to thread-b.
    ui.rerender(
      <LocaleProvider>
        <QueryClientProvider client={queryClient}>
          <ScrollMemoryTestHarness
            entries={entries}
            scope="clamp-scope"
            scrollMetrics={scrollMetrics}
            threadID="thread-b"
          />
        </QueryClientProvider>
      </LocaleProvider>,
    );

    // Switch back to thread-a BEFORE full content height has settled (clamped to 500 - 500 = 0, distance 0 <= 48!).
    scrollMetrics.scrollHeight = 500;
    ui.rerender(
      <LocaleProvider>
        <QueryClientProvider client={queryClient}>
          <ScrollMemoryTestHarness
            entries={entries}
            scope="clamp-scope"
            scrollMetrics={scrollMetrics}
            threadID="thread-a"
          />
        </QueryClientProvider>
      </LocaleProvider>,
    );

    // Transient clamped scroll event fires at scrollTop=0 (0px from bottom).
    fireEvent.scroll(scroller);
    expect(scroller).toHaveAttribute("data-following", "false");
    expect(readPersistedThreadScrollMemory("clamp-scope", "thread-a")?.following).toBe(false);

    // Now layout finishes expanding to 2200px and ResizeObserver fires:
    // instead of yanking to 1700 (bottom), it restores the saved 850px reading position!
    act(() => {
      scrollMetrics.scrollHeight = 2200;
      resizeCallbacks.forEach((cb) => cb());
    });

    expect(scroller.scrollTop).toBe(850);
    expect(scroller).toHaveAttribute("data-following", "false");
  });

  it("anchors at the start of unread background replies overflowing >70% viewport and clears unread badge on jump", () => {
    const initialEntries: ScrollMemoryEntryLike[] = [
      { id: "u-1", kind: "user" },
      { id: "a-1", kind: "assistant" },
    ];
    const initialMetrics: Record<string, { top: number; height: number }> = {
      "u-1": { top: 0, height: 90 },
      "a-1": { top: 90, height: 310 },
    };
    const scrollMetrics = { scrollHeight: 400, clientHeight: 500 };
    const queryClient = new QueryClient();

    const ui = render(
      <LocaleProvider>
        <QueryClientProvider client={queryClient}>
          <ScrollMemoryTestHarness
            entries={initialEntries}
            entryMetrics={initialMetrics}
            scope="unread-scope"
            scrollMetrics={scrollMetrics}
            threadID="thread-bg"
          />
        </QueryClientProvider>
      </LocaleProvider>,
    );

    const scroller = screen.getByTestId("scroller");
    expect(scroller).toHaveAttribute("data-following", "true");
    expect(readPersistedThreadScrollMemory("unread-scope", "thread-bg")).toMatchObject({
      following: true,
      seenEntryCount: 2,
    });

    // User switches to thread-other while thread-bg finishes a long background reply.
    ui.rerender(
      <LocaleProvider>
        <QueryClientProvider client={queryClient}>
          <ScrollMemoryTestHarness
            entries={[{ id: "other-u", kind: "user" }]}
            scope="unread-scope"
            scrollMetrics={{ scrollHeight: 200, clientHeight: 500 }}
            threadID="thread-other"
          />
        </QueryClientProvider>
      </LocaleProvider>,
    );

    // 3 new entries arrive in thread-bg totaling 980px (> 0.7 * 500 = 350px).
    const updatedEntries: ScrollMemoryEntryLike[] = [
      ...initialEntries,
      { id: "u-2", kind: "user" },
      { id: "act-2", kind: "activity" },
      { id: "a-2", kind: "assistant" },
    ];
    const updatedMetrics: Record<string, { top: number; height: number }> = {
      ...initialMetrics,
      "u-2": { top: 400, height: 100 },
      "act-2": { top: 500, height: 120 },
      "a-2": { top: 620, height: 760 },
    };
    scrollMetrics.scrollHeight = 1380;

    // Switch back to thread-bg.
    ui.rerender(
      <LocaleProvider>
        <QueryClientProvider client={queryClient}>
          <ScrollMemoryTestHarness
            entries={updatedEntries}
            entryMetrics={updatedMetrics}
            scope="unread-scope"
            scrollMetrics={scrollMetrics}
            threadID="thread-bg"
          />
        </QueryClientProvider>
      </LocaleProvider>,
    );

    // Anchored at the start of u-2 (top = 400), NOT yanked to bottom (880)!
    expect(scroller.scrollTop).toBe(400);
    expect(scroller).toHaveAttribute("data-following", "false");

    const jumpPill = screen.getByRole("button", { name: /↓ 跳至最新/u });
    expect(jumpPill).toBeVisible();
    expect(screen.getByTestId("scroll-unread-badge")).toHaveTextContent("3");

    // Clicking the floating pill jumps to bottom and clears unread state.
    fireEvent.click(jumpPill);
    expect(scroller.scrollTop).toBe(880);
    expect(scroller).toHaveAttribute("data-following", "true");
    expect(screen.queryByRole("button", { name: /↓ 跳至最新/u })).not.toBeInTheDocument();
    expect(readPersistedThreadScrollMemory("unread-scope", "thread-bg")).toMatchObject({
      top: 880,
      following: true,
      seenEntryCount: 5,
    });
  });

  it("anchors at the preceding user prompt when only the assistant reply arrived in background, and keeps short replies following", () => {
    const entriesBeforeReply: ScrollMemoryEntryLike[] = [
      { id: "u-1", kind: "user" },
      { id: "a-1", kind: "assistant" },
      { id: "u-2", kind: "user" },
    ];
    const metrics: Record<string, { top: number; height: number }> = {
      "u-1": { top: 0, height: 80 },
      "a-1": { top: 80, height: 240 },
      "u-2": { top: 320, height: 90 },
    };
    const scrollMetrics = { scrollHeight: 410, clientHeight: 500 };
    const queryClient = new QueryClient();

    const ui = render(
      <LocaleProvider>
        <QueryClientProvider client={queryClient}>
          <ScrollMemoryTestHarness
            entries={entriesBeforeReply}
            entryMetrics={metrics}
            scope="prompt-anchor-scope"
            scrollMetrics={scrollMetrics}
            threadID="thread-prompt"
          />
        </QueryClientProvider>
      </LocaleProvider>,
    );

    // Switch away, then add a short assistant reply (200px <= 0.7 * 500 = 350px) in thread-short,
    // and a long assistant reply (620px > 350px) in thread-prompt.
    ui.rerender(
      <LocaleProvider>
        <QueryClientProvider client={queryClient}>
          <ScrollMemoryTestHarness
            entries={[{ id: "s-u1", kind: "user" }]}
            entryMetrics={{ "s-u1": { top: 0, height: 80 } }}
            scope="prompt-anchor-scope"
            scrollMetrics={{ scrollHeight: 80, clientHeight: 500 }}
            threadID="thread-short"
          />
        </QueryClientProvider>
      </LocaleProvider>,
    );

    metrics["a-2"] = { top: 410, height: 620 };
    scrollMetrics.scrollHeight = 1030;
    ui.rerender(
      <LocaleProvider>
        <QueryClientProvider client={queryClient}>
          <ScrollMemoryTestHarness
            entries={[...entriesBeforeReply, { id: "a-2", kind: "assistant" }]}
            entryMetrics={metrics}
            scope="prompt-anchor-scope"
            scrollMetrics={scrollMetrics}
            threadID="thread-prompt"
          />
        </QueryClientProvider>
      </LocaleProvider>,
    );

    const scroller = screen.getByTestId("scroller");
    // Preceding user prompt u-2 starts at top=320, so the reader sees both their prompt and the reply start.
    expect(scroller.scrollTop).toBe(320);
    expect(scroller).toHaveAttribute("data-following", "false");
    expect(screen.getByTestId("scroll-unread-badge")).toHaveTextContent("1");

    // Switch back to thread-short where only a short 200px reply arrived: stays following at bottom!
    ui.rerender(
      <LocaleProvider>
        <QueryClientProvider client={queryClient}>
          <ScrollMemoryTestHarness
            entries={[{ id: "s-u1", kind: "user" }, { id: "s-a1", kind: "assistant" }]}
            entryMetrics={{
              "s-u1": { top: 0, height: 80 },
              "s-a1": { top: 80, height: 200 },
            }}
            scope="prompt-anchor-scope"
            scrollMetrics={{ scrollHeight: 280, clientHeight: 500 }}
            threadID="thread-short"
          />
        </QueryClientProvider>
      </LocaleProvider>,
    );

    expect(scroller).toHaveAttribute("data-following", "true");
    expect(screen.queryByTestId("scroll-unread-badge")).not.toBeInTheDocument();
  });

  it("shows localized Scroll to bottom pill only when !following and distance from bottom exceeds 160px", () => {
    window.localStorage.setItem("prayu.locale.v1", "en-US");
    const scrollMetrics = { scrollHeight: 2000, clientHeight: 500 };
    const queryClient = new QueryClient();

    render(
      <LocaleProvider>
        <QueryClientProvider client={queryClient}>
          <ScrollMemoryTestHarness
            entries={[{ id: "u-1", kind: "user" }, { id: "a-1", kind: "assistant" }]}
            scope="threshold-scope"
            scrollMetrics={scrollMetrics}
            threadID="thread-en"
          />
        </QueryClientProvider>
      </LocaleProvider>,
    );

    const scroller = screen.getByTestId("scroller");
    // Distance from bottom = 2000 - 500 - 1400 = 100px (> 48px so !following, but <= 160px so pill stays hidden).
    scroller.scrollTop = 1400;
    fireEvent.scroll(scroller);
    expect(scroller).toHaveAttribute("data-following", "false");
    expect(screen.queryByRole("button", { name: /Scroll to bottom/i })).not.toBeInTheDocument();

    // Distance from bottom = 2000 - 500 - 1250 = 250px (> 160px -> pill appears).
    scroller.scrollTop = 1250;
    fireEvent.scroll(scroller);
    expect(screen.getByRole("button", { name: "↓ Scroll to bottom" })).toBeVisible();
  });
});
