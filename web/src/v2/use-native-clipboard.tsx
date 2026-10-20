import { useLocale , type LocaleText } from "../lib/locale";
import { useEffect, useMemo, useReducer } from "react";
import type { WorkspaceImageAttachment } from "../api/image-attachments";
import type { WorkspaceFileAttachment } from "../api/file-attachments";
import { desktopClipboardFilesAvailable, inspectDesktopClipboardFiles, parseDesktopClipboardFilesResult,
  pasteDesktopClipboardFiles, type DesktopClipboardFilesResult } from "../lib/desktop-bridge";
import { useV2RecoveryStore, type V2RecoveryStore } from "./recovery-storage";

interface NativePasteIntent {
  version: "v2_native_clipboard_recovery.v1";
  workspaceID: string;
  threadID: string;
  operationKey: string;
  createdAt: string;
  result?: DesktopClipboardFilesResult;
  observation?: boolean;
  added?: boolean;
}
interface NativeClipboardProps {
  workspaceID: string;
  threadID?: string;
  disabled?: boolean;
  onImported: (images: WorkspaceImageAttachment[], attachments: WorkspaceFileAttachment[]) => void;
  captureImport?: () => (images: WorkspaceImageAttachment[], attachments: WorkspaceFileAttachment[]) => void;
}
const record = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);
const identity = (value: unknown): value is string => typeof value === "string" && /^[\w.-]{1,256}$/u.test(value);
const errorText = (error: unknown, t: LocaleText) => error instanceof Error && error.message ? error.message : t("原生粘贴结果暂时无法确认，请保留原记录。", "Native paste is unconfirmed. Keep the original record.");

function readIntents(store: V2RecoveryStore | null, prefix: string, workspaceID: string, threadID: string, t: LocaleText): { entries: Array<[string, NativePasteIntent]>; error: string | null } {
  if (!store) return { entries: [], error: null };
  const entries: Array<[string, NativePasteIntent]> = [];
  let invalid = false;
  for (const [key, value] of store.entries<unknown>(prefix)) {
    let valid = record(value) && Object.keys(value).every((field) => ["version", "workspaceID", "threadID", "operationKey", "createdAt", "result", "observation", "added"].includes(field)) &&
      value.version === "v2_native_clipboard_recovery.v1" && value.workspaceID === workspaceID && value.threadID === threadID &&
      identity(value.operationKey) && value.operationKey.length >= 16 && key === prefix + value.operationKey &&
      typeof value.createdAt === "string" && Number.isFinite(Date.parse(value.createdAt)) &&
      (value.observation === undefined || typeof value.observation === "boolean") && (value.added === undefined || typeof value.added === "boolean");
    if (valid && record(value) && value.result !== undefined) {
      try { parseDesktopClipboardFilesResult(value.result, workspaceID, value.observation === true); } catch { valid = false; }
    }
    if (valid && record(value) && value.added === true && value.result === undefined) valid = false;
    if (!valid) { store.reject(key); invalid = true; } else entries.push([key, value as unknown as NativePasteIntent]);
  }
  try { store.assertReadable(prefix); } catch (error) { return { entries, error: errorText(error, t) }; }
  return { entries: entries.sort((a, b) => a[1].createdAt.localeCompare(b[1].createdAt)), error: invalid ? t("原生粘贴恢复记录无效，原记录已保留。", "The native paste recovery record is invalid. The original record is kept.") : null };
}

// The caller routes a single explicit paste event here only after DOM File
// handling has declined it. No keyboard/default text edit is intercepted here.
export function useNativeClipboard({ workspaceID, threadID = "", disabled = false, onImported, captureImport }: NativeClipboardProps) {
  const { t } = useLocale();
  const store = useV2RecoveryStore();
  const [, redraw] = useReducer((value: number) => value + 1, 0);
  const scope = useMemo(() => ({ prefix: `native-clipboard:${JSON.stringify([workspaceID, threadID])}:`,
    busy: new Set<string>(), observed: new Set<string>(), error: null as string | null }), [workspaceID, threadID, store]);
  const available = desktopClipboardFilesAvailable();
  const state = readIntents(store, scope.prefix, workspaceID, threadID, t);
  const entryKey = state.entries.map(([key]) => key).join("|");
  useEffect(() => store?.subscribePrefix?.(scope.prefix, redraw), [store, scope]);

  const stillSaved = (key: string, intent: NativePasteIntent) => {
    const current = readIntents(store, scope.prefix, workspaceID, threadID, t);
    return !current.error && current.entries.some(([savedKey, saved]) => savedKey === key && saved.operationKey === intent.operationKey);
  };
  useEffect(() => {
    if (!store || !available || state.error) return;
    for (const [key, intent] of state.entries) {
      if (scope.busy.has(key) || scope.observed.has(key) || (intent.result && !intent.observation)) continue;
      scope.observed.add(key); scope.busy.add(key); redraw();
      void inspectDesktopClipboardFiles(workspaceID, intent.operationKey).then((result) => {
        const latest = readIntents(store, scope.prefix, workspaceID, threadID, t);
        const current = !latest.error && latest.entries.find(([savedKey, saved]) => savedKey === key && saved.operationKey === intent.operationKey)?.[1];
        // A different window may already have added the saved receipts, or the
        // original paste may have returned while this observation was pending.
        if (current && !(current.result && !current.observation)) store.write(key, { ...current, result, observation: true });
      }).catch((error) => { scope.error = errorText(error, t); }).finally(() => { scope.busy.delete(key); redraw(); });
    }
    // Scope objects keep late replies/errors attached to their original page.
    // Each original key is inspected at most once per mount, never polled.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [store, scope, available, entryKey]);

  const addSaved = (key: string, intent: NativePasteIntent, callback = onImported) => {
    if (!store || !intent.result || intent.added || !stillSaved(key, intent)) return;
    const latest = readIntents(store, scope.prefix, workspaceID, threadID, t).entries.find(([savedKey]) => savedKey === key)?.[1];
    if (latest?.added) return;
    callback(intent.result.images, intent.result.attachments);
    const saved = { ...intent, added: true };
    store.write(key, saved);
    if (intent.result.batch_complete) store.remove(key);
  };

  const onPasteFallback = async () => {
    if (disabled || !workspaceID || !available || scope.busy.size) return;
    scope.error = null;
    if (!store) { scope.error = t("当前连接不能保存粘贴恢复记录，请使用选择文件。", "This connection cannot save paste recovery records. Use Choose files."); redraw(); return; }
    const before = readIntents(store, scope.prefix, workspaceID, threadID, t);
    if (before.error || before.entries.length) { scope.error = before.error ?? t("请先核对上一次粘贴结果，再粘贴新文件。", "Check the previous paste result before pasting more files."); redraw(); return; }
    const operationKey = `native-paste-${crypto.randomUUID()}`;
    const key = scope.prefix + operationKey;
    const intent: NativePasteIntent = { version: "v2_native_clipboard_recovery.v1", workspaceID, threadID, operationKey, createdAt: new Date().toISOString() };
    let capturedOnImported: NativeClipboardProps["onImported"];
    try { capturedOnImported = captureImport?.() ?? onImported; store.assertReadable(scope.prefix); store.write(key, intent); }
    catch (error) { scope.error = errorText(error, t); redraw(); return; }
    scope.busy.add(key); scope.observed.add(key); redraw();
    try {
      const result = await pasteDesktopClipboardFiles(workspaceID, operationKey);
      if (!stillSaved(key, intent)) return;
      const saved = { ...intent, result, observation: false };
      store.write(key, saved);
      if (result.status === "unsupported") {
        store.remove(key); scope.error = t("此平台没有原生文件粘贴支持，请使用选择文件。", "Native file paste is unavailable on this platform. Use Choose files.");
      } else if (result.status === "empty") { store.remove(key); }
      else {
        if (result.images.length || result.attachments.length) addSaved(key, saved, capturedOnImported);
        else store.remove(key);
        if (result.rejected.length) scope.error = result.rejected.map((item) => `${item.name}：${item.message}`).join("；");
      }
    } catch (error) { scope.error = errorText(error, t); }
    finally { scope.busy.delete(key); redraw(); }
  };

  const dismiss = (key: string, intent: NativePasteIntent) => {
    if (!store || scope.busy.has(key) || !stillSaved(key, intent)) return;
    try { store.remove(key); scope.error = null; } catch (error) { scope.error = errorText(error, t); }
    redraw();
  };
  const error = state.error ?? scope.error;
  const notice = (error || state.entries.length > 0) ? <div className="v2-composer-attachment-notice" role="status">
    {error && <p>{error}</p>}
    {state.entries.map(([key, intent]) => {
      const result = intent.result, count = (result?.images.length ?? 0) + (result?.attachments.length ?? 0);
      const busy = scope.busy.has(key);
      return <div key={key}>
        <p>{busy ? t("正在核对本次粘贴。", "Checking this paste operation.") : intent.added ? t("已加入找到的附件，原批次是否完整仍待确认。核对附件列表后，可点击“结束本次粘贴核对”。", "Found attachments were added, but the original batch may be incomplete. Check the attachment list, then select Finish paste review.") : result?.batch_complete ?
          t("附件已保存。点击“加入已保存的附件”放入当前消息。", "Attachments are saved. Select Add saved attachments to include them in this message.") : count ? t("已找到原请求保存的附件，原批次是否完整仍待确认。可点击“加入已保存的附件”放入当前消息。", "Saved attachments from the original request were found, but the batch may be incomplete. Select Add saved attachments to include them.") : t("原粘贴结果尚待确认，暂未找到已保存的附件。可点击“不加入本条消息”结束本次核对。", "The paste result is unconfirmed and no saved attachments were found. Select Leave out of this message to finish this review.")}</p>
        {count > 0 && <ul>{[...result!.images, ...result!.attachments].map((item) => <li key={item.id}>{item.name || t("图片", "Image")}</li>)}</ul>}
        <div className="v2-composer-attachment-actions">
          {count > 0 && !intent.added && <button type="button" className="compact-command" disabled={busy || disabled} onClick={() => {
            try { addSaved(key, intent); scope.error = null; } catch (error) { scope.error = errorText(error, t); } redraw();
          }}>{t("加入已保存的附件", "Add saved attachments")}</button>}
          <button type="button" className="compact-command" disabled={busy} onClick={() => dismiss(key, intent)}>{intent.added ? t("结束本次粘贴核对", "Finish paste review") : t("不加入本条消息", "Leave out of this message")}</button>
        </div>
        {intent.added && <p>{t("已加入的附件仍在草稿中，可在附件卡片移除。", "Added attachments remain in the draft. Remove them from their attachment cards.")}</p>}
      </div>;
    })}
  </div> : null;
  return { onPasteFallback, pending: state.entries.length > 0, busy: scope.busy.size > 0, error, notice };
}
