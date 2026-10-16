import { createContext, useCallback, useContext, useEffect, useMemo, useReducer, useRef, useSyncExternalStore } from "react";
import type { Dispatch, ReactNode, SetStateAction } from "react";
import type { APIClient } from "../api/client";

const VERSION = "v2_recovery.v1";
const UNSUPPORTED = "当前连接尚未提供数据存储身份，草稿仅保留在本窗口。";
const READ_FAILED = "无法读取本机恢复数据；原记录保持不变，当前编辑可能仅保留在本窗口。";
const WRITE_FAILED = "无法保存本机恢复数据；当前编辑可能仅保留在本窗口，刷新或退出后会丢失。";
const INVALID_RECORD = "本机恢复记录格式异常；原记录已保留，当前修改无法覆盖它。";
const REMOVE_FAILED = "无法移除本机恢复记录；原记录可能仍然存在。";

export class V2PersistenceError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "V2PersistenceError";
  }
}

export interface V2RecoveryStore {
  read<T>(key: string, fallback: T): T;
  write<T>(key: string, value: T): void;
  remove(key: string): void;
  entries<T>(prefix: string): Array<[string, T]>;
  assertReadable(prefix?: string): void;
  reject(key: string): void;
  markUnsaved(key: string, message: string): void;
  unsavedWarning(key: string): string | null;
  subscribePrefix?(prefix: string, listener: () => void): () => void;
}

function decodeRecord(raw: string): unknown {
  const record: unknown = JSON.parse(raw);
  if (!record || typeof record !== "object" || Array.isArray(record) ||
    Object.keys(record).length !== 2 || !("version" in record) || record.version !== VERSION ||
    !("value" in record) || !Object.prototype.hasOwnProperty.call(record, "value")) {
    throw new V2PersistenceError(INVALID_RECORD);
  }
  return record.value;
}

// Only the envelope is validated here. Callers must validate recovered business
// payloads before using them, especially identities for unresolved submissions.
class RecoveryStorage implements V2RecoveryStore {
  private readonly listeners = new Map<string, Set<() => void>>();
  private readonly prefixListeners = new Map<string, Set<() => void>>();
  private readonly warningListeners = new Set<() => void>();
  private readonly failures = new Map<string, string>();
  private readonly unsaved = new Map<string, string>();
  private warning: string | null = null;
  private warningNotificationQueued = false;

  constructor(private readonly prefix: string) {}

  private storageKey(key: string): string { return this.prefix + encodeURIComponent(key); }

  private fail(operation: string, message: string): V2PersistenceError {
    this.failures.set(operation, message);
    this.updateWarning();
    return new V2PersistenceError(message);
  }

  private clearFailure(...operations: string[]): void {
    operations.forEach((operation) => this.failures.delete(operation));
    this.updateWarning();
  }

  private updateWarning(): void {
    const warning = this.failures.values().next().value ?? this.unsaved.values().next().value ?? null;
    if (warning === this.warning) return;
    this.warning = warning;
    // Reads can occur during render. Notify warning consumers after that render,
    // rather than updating another component from inside a lazy initializer.
    if (this.warningNotificationQueued) return;
    this.warningNotificationQueued = true;
    queueMicrotask(() => {
      this.warningNotificationQueued = false;
      this.warningListeners.forEach((listener) => listener());
    });
  }

  private readRecord(key: string): { found: false } | { found: true; value: unknown } {
    let raw: string | null;
    try { raw = window.localStorage.getItem(this.storageKey(key)); }
    catch { throw this.fail(`read:${key}`, READ_FAILED); }
    if (raw === null) {
      this.clearFailure(`read:${key}`);
      return { found: false };
    }
    try {
      const value = decodeRecord(raw);
      this.clearFailure(`read:${key}`);
      return { found: true, value };
    } catch { throw this.fail(`read:${key}`, INVALID_RECORD); }
  }

  read<T>(key: string, fallback: T): T {
    try {
      const record = this.readRecord(key);
      return record.found ? record.value as T : fallback;
    } catch { return fallback; }
  }

  write<T>(key: string, value: T): void {
    // A malformed or newer-version record is not silently overwritten. Explicit
    // removal remains available to the caller after the user discards it.
    if (this.failures.has(`invalid:${key}`)) throw new V2PersistenceError(INVALID_RECORD);
    this.readRecord(key);
    try {
      const raw = JSON.stringify({ version: VERSION, value });
      decodeRecord(raw);
      window.localStorage.setItem(this.storageKey(key), raw);
    } catch { throw this.fail(`write:${key}`, WRITE_FAILED); }
    this.unsaved.delete(key);
    this.clearFailure(`write:${key}`, `remove:${key}`);
    this.notify(key);
  }

  remove(key: string): void {
    try { window.localStorage.removeItem(this.storageKey(key)); }
    catch { throw this.fail(`remove:${key}`, REMOVE_FAILED); }
    this.unsaved.delete(key);
    this.clearFailure(`read:${key}`, `write:${key}`, `remove:${key}`, `invalid:${key}`);
    this.notify(key);
  }

  entries<T>(prefix: string): Array<[string, T]> {
    const keys: string[] = [];
    try {
      const storage = window.localStorage;
      let invalidKey = false;
      for (let index = 0; index < storage.length; index++) {
        const storedKey = storage.key(index);
        if (!storedKey?.startsWith(this.prefix)) continue;
        try {
          const key = decodeURIComponent(storedKey.slice(this.prefix.length));
          if (this.storageKey(key) !== storedKey) invalidKey = true;
          else if (key.startsWith(prefix)) keys.push(key);
        } catch { invalidKey = true; }
      }
      if (invalidKey) this.fail("entries", INVALID_RECORD);
      else this.clearFailure("entries");
    } catch {
      this.fail("entries", READ_FAILED);
    }
    const result: Array<[string, T]> = [];
    for (const key of keys.sort()) {
      try {
        const record = this.readRecord(key);
        if (record.found) result.push([key, record.value as T]);
      } catch { /* Retain malformed records, warning already recorded. */ }
    }
    return result;
  }

  assertReadable(prefix?: string): void {
    // A denied storage access may become available without a reload. Recheck
    // previously unreadable keys; this never writes or discards their records.
    for (const operation of [...this.failures.keys()]) {
      if (!operation.startsWith("read:")) continue;
      if (prefix !== undefined && !operation.slice(5).startsWith(prefix)) continue;
      try { this.readRecord(operation.slice(5)); } catch { /* Still unreadable. */ }
    }
    for (const [operation, message] of this.failures) {
      if (prefix !== undefined && operation !== "entries" && !operation.slice(operation.indexOf(":") + 1).startsWith(prefix)) continue;
      if (operation === "entries" || operation.startsWith("read:") || operation.startsWith("invalid:")) {
        throw new V2PersistenceError(message);
      }
    }
    // A store-wide check guards restart against edits that never reached
    // storage. Scoped readers/writers must still be able to repair that draft.
    if (prefix === undefined && this.unsaved.size) throw new V2PersistenceError(this.unsaved.values().next().value!);
  }

  markUnsaved(key: string, message: string): void {
    this.unsaved.set(key, message);
    this.updateWarning();
  }

  unsavedWarning(key: string): string | null { return this.unsaved.get(key) ?? null; }

  reject(key: string): void {
    // Valid JSON can still contain an invalid business payload. Do not allow
    // initial empty UI state to silently overwrite that original evidence.
    this.fail(`invalid:${key}`, INVALID_RECORD);
  }

  subscribe(key: string, listener: () => void): () => void {
    const listeners = this.listeners.get(key) ?? new Set();
    listeners.add(listener);
    this.listeners.set(key, listeners);
    return () => {
      listeners.delete(listener);
      if (listeners.size === 0) this.listeners.delete(key);
    };
  }

  subscribePrefix(prefix: string, listener: () => void): () => void {
    const listeners = this.prefixListeners.get(prefix) ?? new Set();
    listeners.add(listener);
    this.prefixListeners.set(prefix, listeners);
    return () => {
      listeners.delete(listener);
      if (listeners.size === 0) this.prefixListeners.delete(prefix);
    };
  }

  private notify(key: string): void {
    this.listeners.get(key)?.forEach((listener) => listener());
    this.prefixListeners.forEach((listeners, prefix) => {
      if (key.startsWith(prefix)) listeners.forEach((listener) => listener());
    });
  }

  subscribeWarning = (listener: () => void): (() => void) => {
    this.warningListeners.add(listener);
    return () => { this.warningListeners.delete(listener); };
  };
  getWarning = (): string | null => this.warning;

  connect(): () => void {
    const onStorage = (event: StorageEvent) => {
      if (event.key === null) {
        this.listeners.forEach((listeners) => listeners.forEach((listener) => listener()));
        this.prefixListeners.forEach((listeners) => listeners.forEach((listener) => listener()));
      } else if (event.key.startsWith(this.prefix)) {
        try { this.notify(decodeURIComponent(event.key.slice(this.prefix.length))); }
        catch { this.fail("entries", INVALID_RECORD); }
      }
    };
    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
  }
}

const RecoveryContext = createContext<{ store: RecoveryStorage | null; warning: string | null } | null>(null);

export function V2RecoveryProvider({ client, scopeID, children }: {
  client: Pick<APIClient, "baseURL">;
  scopeID?: string;
  children: ReactNode;
}) {
  const scope = scopeID?.trim() ?? "";
  const origin = typeof window === "undefined" ? "" : window.location.origin;
  const store = useMemo(() => scope && origin ? new RecoveryStorage(
    `${VERSION}:${encodeURIComponent(origin)}:${encodeURIComponent(client.baseURL)}:${encodeURIComponent(scope)}:`,
  ) : null, [origin, client.baseURL, scope]);
  const context = useMemo(() => ({ store, warning: store ? null : UNSUPPORTED }), [store]);
  useEffect(() => store?.connect(), [store]);
  return <RecoveryContext.Provider value={context}>{children}</RecoveryContext.Provider>;
}

export function useV2RecoveryStore(): V2RecoveryStore | null {
  return useContext(RecoveryContext)?.store ?? null;
}

const noSubscription = () => () => {};
const noWarning = () => null;

export function useV2PersistenceWarning(): string | null {
  const context = useContext(RecoveryContext);
  const warning = useSyncExternalStore(context?.store?.subscribeWarning ?? noSubscription,
    context?.store?.getWarning ?? noWarning, noWarning);
  return warning ?? context?.warning ?? null;
}

export function useV2PersistentState<T>(key: string, initial: T | (() => T)): [T, Dispatch<SetStateAction<T>>] {
  const store = useContext(RecoveryContext)?.store ?? null;
  const [, rerender] = useReducer((version: number) => version + 1, 0);
  const current = useRef<{ store: RecoveryStorage | null; key: string; value: T; fallback: T } | null>(null);
  if (!current.current || current.current.store !== store || current.current.key !== key) {
    const fallback = typeof initial === "function" ? (initial as () => T)() : initial;
    current.current = { store, key, fallback, value: store ? store.read(key, fallback) : fallback };
  }
  const binding = current.current;
  const setState = useCallback<Dispatch<SetStateAction<T>>>((update) => {
    const value = typeof update === "function" ? (update as (previous: T) => T)(binding.value) : update;
    try { binding.store?.write(binding.key, value); }
    catch { /* Drafts stay editable in this window; the store exposes the warning. */ }
    binding.value = value;
    rerender();
  }, [binding]);
  useEffect(() => store?.subscribe(key, () => {
    binding.value = store.read(key, binding.fallback);
    rerender();
  }), [store, key, binding]);
  return [binding.value, setState];
}
