import { StrictMode } from "react";
import type { Dispatch, ReactNode, SetStateAction } from "react";
import { act, cleanup, render, renderHook, screen, waitFor } from "@testing-library/react";
import { V2PersistenceError, V2RecoveryProvider, useV2PersistenceWarning, useV2PersistentState, useV2RecoveryStore } from "./recovery-storage";

const client = { baseURL: "/api/v1" };
function wrapper({ children }: { children: ReactNode }) {
  return <V2RecoveryProvider client={client} scopeID="database-one">{children}</V2RecoveryProvider>;
}
function storageKey(key: string, scope = "database-one", baseURL = "/api/v1") {
  return `v2_recovery.v1:${encodeURIComponent(window.location.origin)}:${encodeURIComponent(baseURL)}:${encodeURIComponent(scope)}:${encodeURIComponent(key)}`;
}
function envelope(value: unknown) { return JSON.stringify({ version: "v2_recovery.v1", value }); }

beforeEach(() => window.localStorage.clear());
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

describe("V2 scoped recovery storage", () => {
  it("retains unsaved draft warnings across reads and other writes until that draft saves or is discarded", async () => {
    const { result } = renderHook(() => ({ store: useV2RecoveryStore()!, warning: useV2PersistenceWarning() }), { wrapper });
    act(() => {
      result.current.store.write("draft:a", "saved");
      result.current.store.markUnsaved("draft:a", "draft exceeds capacity");
      expect(result.current.store.read("draft:a", "")).toBe("saved");
      result.current.store.write("draft:b", "other draft");
      expect(() => result.current.store.assertReadable("draft:a")).not.toThrow();
      expect(() => result.current.store.assertReadable()).toThrow("draft exceeds capacity");
    });
    await waitFor(() => expect(result.current.warning).toBe("draft exceeds capacity"));
    expect(window.localStorage.getItem(storageKey("draft:a"))).toBe(envelope("saved"));
    act(() => result.current.store.write("draft:a", "corrected"));
    expect(() => result.current.store.assertReadable()).not.toThrow();
    await waitFor(() => expect(result.current.warning).toBeNull());
    act(() => {
      result.current.store.markUnsaved("draft:a", "draft exceeds capacity");
      result.current.store.remove("draft:a");
    });
    expect(() => result.current.store.assertReadable()).not.toThrow();
    await waitFor(() => expect(result.current.warning).toBeNull());
  });

  it("restores JSON records after a provider remount without an initialization write", () => {
    const first = renderHook(() => useV2RecoveryStore(), { wrapper });
    act(() => first.result.current!.write("draft:thread-一", { text: "继续 🧭", refs: ["README.md"] }));
    expect(window.localStorage.getItem(storageKey("draft:thread-一"))).toBe(envelope({ text: "继续 🧭", refs: ["README.md"] }));
    first.unmount();
    const setItem = vi.spyOn(Storage.prototype, "setItem");
    const second = renderHook(() => useV2PersistentState("draft:thread-一", { text: "", refs: [] as string[] }), { wrapper });
    expect(second.result.current[0]).toEqual({ text: "继续 🧭", refs: ["README.md"] });
    expect(setItem).not.toHaveBeenCalled();
  });

  it("isolates data-store identity and API scope while preserving each previous record", () => {
    function Probe() {
      const [draft, setDraft] = useV2PersistentState("draft:thread-one", "");
      return <input aria-label="draft" value={draft} onChange={(event) => setDraft(event.target.value)} />;
    }
    window.localStorage.setItem(storageKey("draft:thread-one"), envelope("first home"));
    window.localStorage.setItem(storageKey("draft:thread-one", "database-two"), envelope("second home"));
    window.localStorage.setItem(storageKey("draft:thread-one", "database-two", "/alternate"), envelope("other API"));
    const view = render(<V2RecoveryProvider client={client} scopeID="database-one"><Probe /></V2RecoveryProvider>);
    expect(screen.getByRole("textbox")).toHaveValue("first home");
    view.rerender(<V2RecoveryProvider client={client} scopeID="database-two"><Probe /></V2RecoveryProvider>);
    expect(screen.getByRole("textbox")).toHaveValue("second home");
    view.rerender(<V2RecoveryProvider client={{ baseURL: "/alternate" }} scopeID="database-two"><Probe /></V2RecoveryProvider>);
    expect(screen.getByRole("textbox")).toHaveValue("other API");
    expect(window.localStorage.length).toBe(3);
  });

  it("writes individual keys without overwriting another draft or unrelated browser state", () => {
    window.localStorage.setItem("other-product", "untouched");
    const first = renderHook(() => useV2RecoveryStore(), { wrapper });
    const second = renderHook(() => useV2RecoveryStore(), { wrapper });
    first.result.current!.write("draft:a", "A");
    second.result.current!.write("draft:b", "B");
    first.result.current!.write("draft:a", "new A");
    second.result.current!.write("unknown:a", { operation_key: "exact-key" });
    expect(first.result.current!.entries<string>("draft:")).toEqual([["draft:a", "new A"], ["draft:b", "B"]]);
    expect(window.localStorage.getItem("other-product")).toBe("untouched");
    expect(window.localStorage.length).toBe(4);
  });

  it.each([
    "not json", "null", "[]", JSON.stringify({ version: "future", value: "saved" }),
    JSON.stringify({ version: "v2_recovery.v1" }),
    JSON.stringify({ version: "v2_recovery.v1", value: "saved", unexpected: true }),
  ])("preserves malformed envelopes and refuses to overwrite them: %s", async (raw) => {
    window.localStorage.setItem(storageKey("unknown:one"), raw);
    window.localStorage.setItem(storageKey("unknown:valid"), envelope({ operation_key: "valid" }));
    const { result } = renderHook(() => ({ store: useV2RecoveryStore()!, warning: useV2PersistenceWarning() }), { wrapper });
    act(() => {
      expect(result.current.store.read("unknown:one", "fallback")).toBe("fallback");
      expect(result.current.store.entries("unknown:")).toEqual([["unknown:valid", { operation_key: "valid" }]]);
      expect(() => result.current.store.write("unknown:one", "replacement")).toThrow(V2PersistenceError);
    });
    await waitFor(() => expect(result.current.warning).toContain("格式异常"));
    expect(window.localStorage.getItem(storageKey("unknown:one"))).toBe(raw);
  });

  it("permits explicit removal of a malformed record and clears its warning", async () => {
    window.localStorage.setItem(storageKey("draft:a"), "broken");
    const { result } = renderHook(() => ({ store: useV2RecoveryStore()!, warning: useV2PersistenceWarning() }), { wrapper });
    act(() => { result.current.store.read("draft:a", ""); });
    await waitFor(() => expect(result.current.warning).toContain("格式异常"));
    act(() => result.current.store.remove("draft:a"));
    await waitFor(() => expect(result.current.warning).toBeNull());
    expect(window.localStorage.getItem(storageKey("draft:a"))).toBeNull();
    result.current.store.write("draft:a", "explicit new draft");
    expect(result.current.store.read("draft:a", "")).toBe("explicit new draft");
  });

  it("protects valid envelopes rejected by business validation from initial empty-state writes", async () => {
    const key = "files:workspace-thread";
    const original = envelope({ unexpected: "not a file-reference array" });
    window.localStorage.setItem(storageKey(key), original);
    const { result } = renderHook(() => ({ store: useV2RecoveryStore()!, warning: useV2PersistenceWarning() }), { wrapper });
    act(() => {
      expect(result.current.store.read(key, null)).toEqual({ unexpected: "not a file-reference array" });
      result.current.store.reject(key);
      expect(() => result.current.store.write(key, [])).toThrow(V2PersistenceError);
      expect(() => result.current.store.assertReadable()).toThrow(V2PersistenceError);
    });
    await waitFor(() => expect(result.current.warning).toContain("格式异常"));
    expect(window.localStorage.getItem(storageKey(key))).toBe(original);
  });

  it("retains a business rejection across reads and other draft writes until explicit removal", async () => {
    const { result } = renderHook(() => ({ store: useV2RecoveryStore()!, warning: useV2PersistenceWarning() }), { wrapper });
    act(() => {
      result.current.store.write("draft:invalid", 17);
      result.current.store.reject("draft:invalid");
      expect(result.current.store.entries("draft:")).toEqual([["draft:invalid", 17]]);
      expect(result.current.store.read("draft:invalid", null)).toBe(17);
      result.current.store.write("draft:other", "still editable");
      expect(() => result.current.store.write("draft:invalid", "")).toThrow(V2PersistenceError);
      expect(() => result.current.store.assertReadable()).toThrow(V2PersistenceError);
    });
    expect(window.localStorage.getItem(storageKey("draft:other"))).toBe(envelope("still editable"));
    await waitFor(() => expect(result.current.warning).toContain("格式异常"));
    act(() => result.current.store.remove("draft:invalid"));
    expect(() => result.current.store.assertReadable()).not.toThrow();
    await waitFor(() => expect(result.current.warning).toBeNull());
    act(() => result.current.store.write("draft:invalid", "explicit replacement"));
    expect(result.current.store.read("draft:invalid", "")).toBe("explicit replacement");
  });

  it("does not clear a business rejection when explicit deletion fails", async () => {
    const { result } = renderHook(() => ({ store: useV2RecoveryStore()!, warning: useV2PersistenceWarning() }), { wrapper });
    act(() => {
      result.current.store.write("draft:invalid", false);
      result.current.store.reject("draft:invalid");
    });
    vi.spyOn(Storage.prototype, "removeItem").mockImplementation(() => { throw new Error("denied"); });
    act(() => {
      expect(() => result.current.store.remove("draft:invalid")).toThrow(V2PersistenceError);
      expect(() => result.current.store.write("draft:invalid", "")).toThrow(V2PersistenceError);
      expect(() => result.current.store.assertReadable()).toThrow(V2PersistenceError);
    });
    await waitFor(() => expect(result.current.warning).toContain("格式异常"));
    expect(window.localStorage.getItem(storageKey("draft:invalid"))).toBe(envelope(false));
  });

  it("continues reading valid records after a malformed encoded key without altering the bad key", async () => {
    const badKey = storageKey("") + "%broken";
    window.localStorage.setItem(badKey, envelope("preserve"));
    window.localStorage.setItem(storageKey("draft:valid"), envelope("valid"));
    const { result } = renderHook(() => ({ store: useV2RecoveryStore()!, warning: useV2PersistenceWarning() }), { wrapper });
    act(() => expect(result.current.store.entries("draft:")).toEqual([["draft:valid", "valid"]]));
    await waitFor(() => expect(result.current.warning).toContain("格式异常"));
    expect(window.localStorage.getItem(badKey)).toBe(envelope("preserve"));
  });

  it("preserves an existing record when JSON encoding or explicit removal fails", async () => {
    window.localStorage.setItem(storageKey("draft:a"), envelope("saved"));
    const { result } = renderHook(() => ({ store: useV2RecoveryStore()!, warning: useV2PersistenceWarning() }), { wrapper });
    const cyclic: { self?: unknown } = {};
    cyclic.self = cyclic;
    act(() => {
      expect(() => result.current.store.write("draft:a", cyclic)).toThrow(V2PersistenceError);
      expect(() => result.current.store.write("draft:a", undefined)).toThrow(V2PersistenceError);
    });
    expect(window.localStorage.getItem(storageKey("draft:a"))).toBe(envelope("saved"));
    vi.spyOn(Storage.prototype, "removeItem").mockImplementation(() => { throw new Error("denied"); });
    act(() => expect(() => result.current.store.remove("draft:a")).toThrow(V2PersistenceError));
    expect(window.localStorage.getItem(storageKey("draft:a"))).toBe(envelope("saved"));
    await waitFor(() => expect(result.current.warning).not.toBeNull());
  });

  it("throws synchronously on failed unknown-request persistence before a caller can POST", async () => {
    const { result } = renderHook(() => ({ store: useV2RecoveryStore()!, warning: useV2PersistenceWarning() }), { wrapper });
    const post = vi.fn();
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new DOMException("quota", "QuotaExceededError"); });
    act(() => {
      expect(() => {
        result.current.store.write("unknown:thread", { operation_key: "original-key" });
        post();
      }).toThrow("无法保存");
    });
    expect(post).not.toHaveBeenCalled();
    expect(window.localStorage.getItem(storageKey("unknown:thread"))).toBeNull();
    await waitFor(() => expect(result.current.warning).toContain("无法保存"));
  });

  it("blocks a fresh request after an unreadable old journal entry, even when the new key differs", async () => {
    window.localStorage.setItem(storageKey("turn:original-operation"), "broken old journal");
    const { result } = renderHook(() => ({ store: useV2RecoveryStore()!, warning: useV2PersistenceWarning() }), { wrapper });
    const post = vi.fn();
    act(() => {
      expect(result.current.store.entries("turn:")).toEqual([]);
      expect(() => {
        result.current.store.assertReadable();
        result.current.store.write("turn:new-operation", { operation_key: "new-operation" });
        post();
      }).toThrow(V2PersistenceError);
    });
    expect(post).not.toHaveBeenCalled();
    expect(window.localStorage.getItem(storageKey("turn:new-operation"))).toBeNull();
    expect(window.localStorage.getItem(storageKey("turn:original-operation"))).toBe("broken old journal");
    await waitFor(() => expect(result.current.warning).toContain("格式异常"));
    act(() => result.current.store.remove("turn:original-operation"));
    expect(() => result.current.store.assertReadable()).not.toThrow();
  });

  it("does not prevent a write retry merely because the previous write ran out of space", async () => {
    const { result } = renderHook(() => ({ store: useV2RecoveryStore()!, warning: useV2PersistenceWarning() }), { wrapper });
    const setItem = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("quota"); });
    act(() => expect(() => result.current.store.write("turn:original", { operation_key: "original" })).toThrow());
    expect(() => result.current.store.assertReadable()).not.toThrow();
    setItem.mockRestore();
    act(() => result.current.store.write("turn:original", { operation_key: "original" }));
    expect(result.current.store.read("turn:original", null)).toEqual({ operation_key: "original" });
    await waitFor(() => expect(result.current.warning).toBeNull());
  });

  it("keeps failed draft edits in the current window and persists a later successful retry", async () => {
    const { result } = renderHook(() => ({ state: useV2PersistentState("draft:a", ""), warning: useV2PersistenceWarning() }), { wrapper });
    const setItem = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("denied"); });
    act(() => result.current.state[1]("unsaved 中文"));
    expect(result.current.state[0]).toBe("unsaved 中文");
    await waitFor(() => expect(result.current.warning).toContain("无法保存"));
    setItem.mockRestore();
    act(() => result.current.state[1]((previous) => previous + " recovered"));
    expect(window.localStorage.getItem(storageKey("draft:a"))).toBe(envelope("unsaved 中文 recovered"));
    await waitFor(() => expect(result.current.warning).toBeNull());
  });

  it("catches unavailable storage for reads and enumeration but makes writes and removals fail", async () => {
    const unavailable = vi.spyOn(window, "localStorage", "get").mockImplementation(() => { throw new DOMException("unavailable", "SecurityError"); });
    const { result } = renderHook(() => ({ store: useV2RecoveryStore()!, warning: useV2PersistenceWarning() }), { wrapper });
    act(() => {
      expect(result.current.store.read("draft:a", "local")).toBe("local");
      expect(result.current.store.entries("unknown:")).toEqual([]);
      expect(() => result.current.store.write("unknown:a", {})).toThrow(V2PersistenceError);
      expect(() => result.current.store.remove("unknown:a")).toThrow(V2PersistenceError);
      expect(() => result.current.store.assertReadable()).toThrow(V2PersistenceError);
    });
    await waitFor(() => expect(result.current.warning).not.toBeNull());
    unavailable.mockRestore();
    act(() => {
      expect(result.current.store.entries("unknown:")).toEqual([]);
      expect(() => result.current.store.assertReadable()).not.toThrow();
    });
  });

  it("applies functional updates synchronously without duplicate StrictMode writes", () => {
    const { result } = renderHook(() => useV2PersistentState("counter", 0), { wrapper: ({ children }) =>
      <StrictMode><V2RecoveryProvider client={client} scopeID="database-one">{children}</V2RecoveryProvider></StrictMode> });
    const setItem = vi.spyOn(Storage.prototype, "setItem");
    act(() => {
      result.current[1]((value) => value + 1);
      result.current[1]((value) => value + 1);
      expect(window.localStorage.getItem(storageKey("counter"))).toBe(envelope(2));
    });
    expect(result.current[0]).toBe(2);
    expect(setItem).toHaveBeenCalledTimes(2);
  });

  it("notifies active state consumers only after a successful write and observes external storage changes", () => {
    const { result } = renderHook(() => ({ first: useV2PersistentState("draft:a", ""), second: useV2PersistentState("draft:a", ""), store: useV2RecoveryStore()! }), { wrapper });
    act(() => result.current.store.write("draft:a", "saved"));
    expect(result.current.first[0]).toBe("saved");
    expect(result.current.second[0]).toBe("saved");
    const setItem = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("quota"); });
    act(() => { expect(() => result.current.store.write("draft:a", "not saved")).toThrow(); });
    expect(result.current.first[0]).toBe("saved");
    setItem.mockRestore();
    act(() => {
      window.localStorage.setItem(storageKey("draft:a"), envelope("other tab"));
      window.dispatchEvent(new StorageEvent("storage", { key: storageKey("draft:a"), newValue: envelope("other tab") }));
    });
    expect(result.current.second[0]).toBe("other tab");
    act(() => result.current.store.remove("draft:a"));
    expect(result.current.first[0]).toBe("");
  });

  it("binds an earlier setter to its original key when the active draft changes", () => {
    const { result, rerender } = renderHook(({ id }) => useV2PersistentState(id, ""), { initialProps: { id: "draft:a" }, wrapper });
    act(() => result.current[1]("A"));
    const setEarlier: Dispatch<SetStateAction<string>> = result.current[1];
    rerender({ id: "draft:b" });
    act(() => result.current[1]("B"));
    act(() => setEarlier((previous) => previous + " completed"));
    expect(result.current[0]).toBe("B");
    expect(window.localStorage.getItem(storageKey("draft:a"))).toBe(envelope("A completed"));
    expect(window.localStorage.getItem(storageKey("draft:b"))).toBe(envelope("B"));
  });

  it("remains local without a provider or stable data-store identity", () => {
    const getItem = vi.spyOn(Storage.prototype, "getItem");
    const setItem = vi.spyOn(Storage.prototype, "setItem");
    const noProvider = renderHook(() => ({ store: useV2RecoveryStore(), state: useV2PersistentState("draft", ""), warning: useV2PersistenceWarning() }));
    act(() => noProvider.result.current.state[1]("local"));
    expect(noProvider.result.current.store).toBeNull();
    expect(noProvider.result.current.state[0]).toBe("local");
    expect(noProvider.result.current.warning).toBeNull();
    const unsupported = renderHook(() => ({ store: useV2RecoveryStore(), warning: useV2PersistenceWarning() }), { wrapper: ({ children }) =>
      <V2RecoveryProvider client={client}>{children}</V2RecoveryProvider> });
    expect(unsupported.result.current.store).toBeNull();
    expect(unsupported.result.current.warning).toContain("数据存储身份");
    expect(getItem).not.toHaveBeenCalled();
    expect(setItem).not.toHaveBeenCalled();
  });
});

vi.mock("../lib/locale", () => ({ useLocale: () => ({ t: (chinese: string) => chinese, locale: "zh-CN" }) }));
