import { useEffect, useReducer, useRef } from "react";
import { useV2RecoveryStore, type V2RecoveryStore } from "../v2/recovery-storage";

const unavailable = "当前连接无法持久保存验证草稿或原请求；请连接提供数据存储身份的 Desktop 后重试。";
const invalid = "验证内容超出可保存范围或格式不完整；请修正内容，或放弃未保存草稿。";
const maximumRecordBytes = 2 * 1024 * 1024;

export function recoveryObject(value: unknown): value is Record<string, unknown> {
  return Boolean(value && typeof value === "object" && !Array.isArray(value));
}
export function recoveryText(value: unknown, maximum: number): value is string {
  return typeof value === "string" && value.length <= maximum && !value.includes("\0") &&
    new TextEncoder().encode(value).byteLength <= maximum;
}
export function recoveryPath(value: unknown): value is string {
  return recoveryText(value, 4096) && value.length > 0 && !/^(?:[\/\\]|[a-z]:)/iu.test(value) &&
    !value.split(/[\/\\]/u).includes("..") && !/[\r\n]/u.test(value);
}

interface Binding<T> {
  store: V2RecoveryStore | null; key: string; fallback: T; value: T; error: string | null;
  validate: (value: unknown) => value is T;
}
function validRecord<T>(binding: Binding<T>, value: unknown): value is T {
  return binding.validate(value) && new TextEncoder().encode(JSON.stringify(value)).byteLength <= maximumRecordBytes;
}
function read<T>(binding: Binding<T>) {
  if (!binding.store) return;
  const value = binding.store.read<unknown>(binding.key, undefined);
  if (value !== undefined && !validRecord(binding, value)) binding.store.reject(binding.key);
  try {
    binding.store.assertReadable(binding.key);
    binding.value = value === undefined ? binding.fallback : value as T;
    binding.error = binding.store.unsavedWarning(binding.key);
  } catch (failure) { binding.error = failure instanceof Error ? failure.message : invalid; }
}

// The browser-verification and batch forms share the existing store. Request
// callers use save before dispatch; edit alone may retain an unsaved local draft.
export function useVerificationRecord<T>(key: string, fallback: T, validate: (value: unknown) => value is T) {
  const store = useV2RecoveryStore();
  const [, refresh] = useReducer((value: number) => value + 1, 0);
  const current = useRef<Binding<T> | null>(null);
  if (!current.current || current.current.store !== store || current.current.key !== key) {
    current.current = { store, key, fallback, value: fallback, error: null, validate };
    read(current.current);
  }
  const binding = current.current;
  useEffect(() => store?.subscribePrefix?.(key, () => { read(binding); refresh(); }), [store, key, binding]);
  const save = (value: T) => {
    try {
      if (!validRecord(binding, value)) throw new Error(invalid);
      if (!binding.store) throw new Error(unavailable);
      binding.store.assertReadable(binding.key);
      binding.store.write(binding.key, value);
      binding.value = value; binding.error = null;
    } catch (failure) {
      binding.error = failure instanceof Error ? failure.message : invalid;
      throw failure;
    } finally { refresh(); }
  };
  return {
    value: binding.value, error: binding.error ?? (store ? null : unavailable), store, key,
    save,
    edit: (update: T | ((previous: T) => T)) => {
      const value = typeof update === "function" ? (update as (previous: T) => T)(binding.value) : update;
      try { save(value); }
      catch {
        binding.value = value;
        binding.store?.markUnsaved(binding.key, binding.error ?? invalid);
        refresh();
      }
    },
    remove: () => {
      try {
        if (!binding.store) throw new Error(unavailable);
        binding.store.assertReadable(binding.key);
        binding.store.remove(binding.key);
        binding.value = binding.fallback; binding.error = null;
      } catch (failure) {
        binding.error = failure instanceof Error ? failure.message : invalid;
        throw failure;
      } finally { refresh(); }
    },
    discard: () => {
      try {
        if (!binding.store) throw new Error(unavailable);
        // This is an explicit user discard, including a retained invalid
        // record. A successful removal clears its shared unsaved warning.
        binding.store.remove(binding.key);
        binding.value = binding.fallback; binding.error = null;
      } catch (failure) {
        binding.error = failure instanceof Error ? failure.message : invalid;
        throw failure;
      } finally { refresh(); }
    },
  };
}
