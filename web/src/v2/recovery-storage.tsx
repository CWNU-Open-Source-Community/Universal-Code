import { useLocale } from "../lib/locale";
import { localUIMessage } from "../lib/local-ui-message";
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

type PersistentConflict = { id: number; key: string; keepLocal: () => void; useRemote: () => void };
let nextPersistentBindingID = 0;

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
  private readonly conflictListeners = new Set<() => void>();
  private conflicts: PersistentConflict[] = [];
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
    try { return this.readOrThrow(key, fallback); } catch { return fallback; }
  }

  readOrThrow<T>(key: string, fallback: T): T {
    const record = this.readRecord(key);
    return record.found ? record.value as T : fallback;
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

  setConflict(conflict: PersistentConflict): void {
    this.conflicts = [...this.conflicts.filter((item) => item.id !== conflict.id), conflict];
    this.conflictListeners.forEach((listener) => listener());
  }

  clearConflict(id: number): void {
    const next = this.conflicts.filter((item) => item.id !== id);
    if (next.length === this.conflicts.length) return;
    this.conflicts = next;
    this.conflictListeners.forEach((listener) => listener());
  }

  discardUnsaved(key: string, failedWriteKey = key): void {
    this.unsaved.delete(key);
    this.clearFailure(`write:${failedWriteKey}`);
  }

  subscribeConflicts = (listener: () => void): (() => void) => {
    this.conflictListeners.add(listener);
    return () => { this.conflictListeners.delete(listener); };
  };
  getConflicts = (): PersistentConflict[] => this.conflicts;

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
const noConflicts: PersistentConflict[] = [];
const getNoConflicts = () => noConflicts;

export function useV2PersistenceWarning(): string | null {
  const { t } = useLocale();
  const context = useContext(RecoveryContext);
  const warning = useSyncExternalStore(context?.store?.subscribeWarning ?? noSubscription,
    context?.store?.getWarning ?? noWarning, noWarning);
  const message = warning ?? context?.warning;
  return message ? localUIMessage(message, t) : null;
}

export function V2PersistenceConflicts() {
  const { t } = useLocale();
  const store = useContext(RecoveryContext)?.store ?? null;
  const conflicts = useSyncExternalStore(store?.subscribeConflicts ?? noSubscription,
    store?.getConflicts ?? getNoConflicts, getNoConflicts);
  if (!conflicts.length) return null;
  return <div className="v2-toast v2-persistence-conflicts" role="alert">{conflicts.map((conflict) => <div key={conflict.id}>
    <p>{t("另一窗口修改了同一条记录。本窗口的未保存编辑已保留，请选择要保存的版本。",
      "Another window changed the same record. Your unsaved edits are preserved; choose which version to keep.")}</p>
    <code>{conflict.key}</code>
    <button type="button" onClick={conflict.keepLocal}>{t("保留本窗口版本", "Keep this window's version")}</button>
    <button type="button" onClick={conflict.useRemote}>{t("使用另一窗口版本", "Use the other window's version")}</button>
  </div>)}</div>;
}

function samePersistentValue(left: unknown, right: unknown): boolean {
  try { return JSON.stringify(left) === JSON.stringify(right); } catch { return false; }
}

export function useV2PersistentState<T>(key: string, initial: T | (() => T)): [T, Dispatch<SetStateAction<T>>] {
  const store = useContext(RecoveryContext)?.store ?? null;
  const [, rerender] = useReducer((version: number) => version + 1, 0);
  const current = useRef<{ id: number; store: RecoveryStorage | null; key: string; value: T; fallback: T; base: T; dirty: boolean; writing: boolean } | null>(null);
  if (!current.current || current.current.store !== store || current.current.key !== key) {
    const fallback = typeof initial === "function" ? (initial as () => T)() : initial;
    const value = store ? store.read(key, fallback) : fallback;
    current.current = { id: ++nextPersistentBindingID, store, key, fallback, value, base: value, dirty: false, writing: false };
  }
  const binding = current.current;
  const registerConflict = (observedRemote: T) => {
    const target = binding.store;
    if (!target) return;
    const unsavedKey = `persistent:${binding.id}:${binding.key}`;
    target.markUnsaved(unsavedKey, WRITE_FAILED);
    target.setConflict({ id: binding.id, key: binding.key,
      keepLocal: () => {
        binding.writing = true;
        try {
          const latestRemote = target.readOrThrow(binding.key, binding.fallback);
          if (!samePersistentValue(latestRemote, observedRemote)) {
            registerConflict(latestRemote);
            return;
          }
          target.write(binding.key, binding.value);
          binding.base = binding.value; binding.dirty = false;
          target.discardUnsaved(unsavedKey, binding.key); target.clearConflict(binding.id);
        } catch { /* Keep both versions available for another retry. */ }
        finally { binding.writing = false; rerender(); }
      },
      useRemote: () => {
        try {
          target.assertReadable(binding.key);
          binding.value = target.readOrThrow(binding.key, binding.fallback);
          binding.base = binding.value; binding.dirty = false;
          target.discardUnsaved(unsavedKey, binding.key); target.clearConflict(binding.id);
          rerender();
        } catch { /* Retain local edits when the other version cannot be read. */ }
      },
    });
  };
  const setState = useCallback<Dispatch<SetStateAction<T>>>((update) => {
    const value = typeof update === "function" ? (update as (previous: T) => T)(binding.value) : update;
    binding.value = value;
    if (binding.store) {
      try {
        binding.store.assertReadable(binding.key);
        const remote = binding.store.readOrThrow(binding.key, binding.fallback);
        if (!samePersistentValue(remote, binding.base)) {
          binding.dirty = true; registerConflict(remote); rerender(); return;
        }
        binding.writing = true;
        binding.store.write(binding.key, value);
        binding.base = value; binding.dirty = false;
        binding.store.discardUnsaved(`persistent:${binding.id}:${binding.key}`, binding.key);
        binding.store.clearConflict(binding.id);
      } catch { binding.dirty = true; binding.store.markUnsaved(`persistent:${binding.id}:${binding.key}`, WRITE_FAILED); }
      finally { binding.writing = false; }
    }
    rerender();
  }, [binding]);
  useEffect(() => store?.subscribe(key, () => {
    if (binding.writing) return;
    try {
      const remote = store.readOrThrow(key, binding.fallback);
      if (binding.dirty) {
        if (!samePersistentValue(remote, binding.base)) registerConflict(remote);
        return;
      }
      binding.value = remote; binding.base = remote;
      rerender();
    } catch { /* A read failure must not replace the visible value with a fallback. */ }
  }), [store, key, binding]);
  return [binding.value, setState];
}
