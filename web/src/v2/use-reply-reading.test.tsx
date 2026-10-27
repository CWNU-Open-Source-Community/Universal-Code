import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { useV2ReplyReading } from "./use-reply-reading";

function deferred() {
  let resolve!: () => void;
  let reject!: (reason: Error) => void;
  const promise = new Promise<void>((accept, fail) => { resolve = accept; reject = fail; });
  return { promise, resolve, reject };
}
function setup() {
  const scroll = document.createElement("div");
  scroll.innerHTML = '<div class="v2-assistant-turn" data-message-ids="saved-one saved-two"></div><div class="v2-assistant-turn is-provisional" data-message-ids="streaming"></div><div data-message-ids="user-message"></div>';
  document.body.append(scroll);
  Object.defineProperties(scroll, { clientHeight: { configurable: true, value: 100 }, scrollHeight: { configurable: true, value: 100 } });
  vi.spyOn(scroll, "getBoundingClientRect").mockReturnValue({ top: 0, bottom: 100 } as DOMRect);
  vi.spyOn(scroll.firstElementChild!, "getBoundingClientRect").mockReturnValue({ top: 20, bottom: 80 } as DOMRect);
  const focus = vi.spyOn(document, "hasFocus").mockReturnValue(true);
  const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  const reading = { observe: vi.fn(), prepare: vi.fn().mockResolvedValue(["old-outside-window"]), confirm: vi.fn() };
  const refresh = vi.fn<() => Promise<void>>().mockResolvedValue();
  const following = vi.fn().mockReturnValue(true);
  const options = { threadID: "thread-a", request: 0, readable: true, ready: true, content: {}, scroll: { current: scroll }, following, refresh, reading };
  return { scroll, focus, visibility, reading, refresh, following, options };
}
afterEach(() => { cleanup(); document.body.replaceChildren(); vi.restoreAllMocks(); });

it("observes durable visible identities and confirms an old pending range only after a fresh transcript read", async () => {
  const fixture = setup();
  const pending = deferred();
  fixture.refresh.mockReturnValue(pending.promise);
  const view = renderHook((options) => useV2ReplyReading(options), { initialProps: fixture.options });
  await waitFor(() => expect(fixture.refresh).toHaveBeenCalledWith({ refreshMutable: true, throwOnError: true }));
  expect(fixture.reading.observe).toHaveBeenCalledWith("thread-a", ["saved-one", "saved-two"]);
  expect(fixture.reading.confirm).not.toHaveBeenCalled();
  // A later feed item is not part of the earlier prepared range.
  fixture.reading.prepare.mockResolvedValue(["old-outside-window", "arrived-during-request"]);
  view.rerender({ ...fixture.options, content: {} });
  await act(async () => { pending.resolve(); await pending.promise; });
  await waitFor(() => expect(fixture.reading.confirm).toHaveBeenCalledWith("thread-a", ["old-outside-window"]));
  expect(fixture.refresh).toHaveBeenCalledOnce();
});

it.each(["history", "blurred", "hidden", "scrolled-up", "covered"])("does not acknowledge while %s", async (mode) => {
  const fixture = setup();
  if (mode === "history" || mode === "covered") fixture.options.readable = false;
  if (mode === "blurred") fixture.focus.mockReturnValue(false);
  if (mode === "hidden") fixture.visibility.mockReturnValue("hidden");
  if (mode === "scrolled-up") fixture.following.mockReturnValue(false);
  renderHook(() => useV2ReplyReading(fixture.options));
  await act(async () => {});
  expect(fixture.reading.observe).not.toHaveBeenCalled();
  expect(fixture.reading.prepare).not.toHaveBeenCalled();
  expect(fixture.reading.confirm).not.toHaveBeenCalled();
});

it("keeps pending identities after a failed head read and retries when the same chat is explicitly opened again", async () => {
  const fixture = setup();
  fixture.refresh.mockRejectedValueOnce(new Error("head unavailable"));
  const view = renderHook((options) => useV2ReplyReading(options), { initialProps: fixture.options });
  await waitFor(() => expect(fixture.refresh).toHaveBeenCalledOnce());
  await act(async () => {});
  expect(fixture.reading.confirm).not.toHaveBeenCalled();
  view.rerender({ ...fixture.options, request: 1 });
  await waitFor(() => expect(fixture.reading.confirm).toHaveBeenCalledWith("thread-a", ["old-outside-window"]));
});

it.each(["blur", "scroll", "thread"])("rejects a completed read after %s changes the reading context", async (change) => {
  const fixture = setup();
  const pending = deferred();
  fixture.refresh.mockReturnValue(pending.promise);
  const view = renderHook((options) => useV2ReplyReading(options), { initialProps: fixture.options });
  await waitFor(() => expect(fixture.refresh).toHaveBeenCalledOnce());
  if (change === "blur") act(() => { fixture.focus.mockReturnValue(false); window.dispatchEvent(new Event("blur")); });
  if (change === "scroll") act(() => { fixture.following.mockReturnValue(false); view.result.current(); });
  if (change === "thread") view.rerender({ ...fixture.options, threadID: "thread-b", readable: false });
  await act(async () => { pending.resolve(); await pending.promise; });
  expect(fixture.reading.confirm).not.toHaveBeenCalled();
});

it("starts a fresh confirmation when the reader returns to the bottom", async () => {
  const fixture = setup();
  fixture.following.mockReturnValue(false);
  const view = renderHook(() => useV2ReplyReading(fixture.options));
  expect(fixture.refresh).not.toHaveBeenCalled();
  act(() => { fixture.following.mockReturnValue(true); view.result.current(); });
  await waitFor(() => expect(fixture.reading.confirm).toHaveBeenCalledWith("thread-a", ["old-outside-window"]));
});
