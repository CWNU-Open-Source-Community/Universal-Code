import { useEffect, useId, useMemo, useReducer, useRef, useState, type RefObject } from "react";
import { createPortal } from "react-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, Download, File, LoaderCircle, MoreHorizontal, X } from "lucide-react";
import { nativeResourcesAvailable, resourceAction } from "../../lib/chat-resources";
import { fileMenuItems, ResourceMenu, type MenuPosition } from "./resource-menu";
import type { APIClient } from "../../api/client";
import { useModalFocusTrap } from "../../hooks/use-modal-focus-trap";
import { useLocale } from "../../lib/locale";
import { maximumFileAttachments, maximumFileBytes, type WorkspaceFileAttachment } from "../../api/file-attachments";
import { v2AttachmentReferenceKey } from "../attachment-keys";
import { useV2DraftDocument } from "../draft-context";
import type { DraftCapture } from "../draft-document";
import { useV2RecoveryStore } from "../recovery-storage";
import "./file-input.css";

export function useV2AttachmentReferences(workspaceID: string, threadID: string) {
  const draft = useV2DraftDocument(workspaceID, threadID);
  const queries = useQueryClient();
  const key = v2AttachmentReferenceKey(workspaceID, threadID);
  const query = useQuery<WorkspaceFileAttachment[]>({ queryKey: key, queryFn: () => [], enabled: false, initialData: [], gcTime: Infinity });
  const update = (change: (files: WorkspaceFileAttachment[]) => WorkspaceFileAttachment[]) => {
    if (draft) draft.document.update(draft.scope, (current) => ({ ...current, attachments: change(current.attachments ?? []) }), draft.state.ref);
    else queries.setQueryData<WorkspaceFileAttachment[]>(key, (current) => change(current ?? []));
  };
  return { attachments: draft ? draft.state.snapshot.attachments ?? [] : query.data, update, draft };
}

const bytesLabel = (bytes: number) => bytes < 1024 ? `${bytes} B` : bytes < 1024 * 1024 ?
  `${(bytes / 1024).toFixed(1)} KiB` : `${(bytes / 1024 / 1024).toFixed(1)} MiB`;
function TextAttachmentPreview({ client, file, onClose, onRestore, returnFocusRef }: {
  client: APIClient; file: WorkspaceFileAttachment; onClose: () => void; onRestore?: (text: string) => void;
  returnFocusRef: RefObject<HTMLButtonElement | null>;
}) {
  const { t } = useLocale();
  const titleID = useId();
  const close = useRef<HTMLButtonElement>(null);
  const dialog = useModalFocusTrap<HTMLElement>(true, onClose, false, close, { isolateBackground: true, returnFocusRef });
  const query = useQuery({ queryKey: ["file-attachment-text", file.workspace_id, file.id, file.sha256],
    queryFn: async ({ signal }) => {
      const blob = await client.downloadWorkspaceFile(file, signal);
      return new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(await blob.arrayBuffer());
    }, staleTime: Infinity, retry: false });
  return createPortal(<div className="v2-overlay" role="presentation" onMouseDown={(event) => {
    if (event.target === event.currentTarget) onClose();
  }}>
    <section className="v2-dialog v2-text-attachment-dialog" role="dialog" aria-modal="true" aria-labelledby={titleID} ref={dialog} tabIndex={-1}>
      <header><File size={18} aria-hidden="true" /><h2 id={titleID}>{file.name}</h2>
        <button aria-label={t("关闭预览", "Close preview")} onClick={onClose} ref={close} type="button"><X size={16} /></button></header>
      {query.isPending && <p role="status">{t("正在读取文本…", "Loading text…")}</p>}
      {query.isError && <p role="alert">{t("无法预览此文件，请下载原文件查看。", "Cannot preview this file. Download the original to view it.")}</p>}
      {query.data !== undefined && <pre tabIndex={0}>{query.data.slice(0, 65536)}</pre>}
      {query.data !== undefined && query.data.length > 65536 && <p>{t("预览已截断；下载或放回正文会保留完整内容。", "Preview shortened. Downloading or restoring keeps the full content.")}</p>}
      <footer><span>{bytesLabel(file.byte_size)}</span>
        {onRestore && <button className="primary" type="button" disabled={query.data === undefined}
          onClick={() => { if (query.data !== undefined) { onRestore(query.data); onClose(); } }}>{t("放回正文", "Restore to message")}</button>}
      </footer>
    </section>
  </div>, document.body);
}

function AttachmentCard({ client, file, onRemove, onRestore, locked }: {
  client: APIClient; file: WorkspaceFileAttachment; onRemove?: () => void; onRestore?: (text: string) => void; locked: boolean;
}) {
  const { t } = useLocale();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [preview, setPreview] = useState(false);
  const [menu, setMenu] = useState<MenuPosition | null>(null);
  const [copied, setCopied] = useState(false);
  const reference = { workspace_id: file.workspace_id, attachment_id: file.id };
  const previewTrigger = useRef<HTMLButtonElement>(null);
  const canPreview = /^text\/(plain|markdown)(?:;|$)/i.test(file.mime_type);
  const download = async () => {
    if (busy) return;
    setBusy(true); setError("");
    try {
      const blob = await client.downloadWorkspaceFile(file);
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a"); link.href = url; link.download = file.name;
      document.body.append(link); link.click(); link.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (failure) { setError(failure instanceof Error ? failure.message : "文件下载失败"); }
    finally { setBusy(false); }
  };
  const native = nativeResourcesAvailable();
  const copy = async () => {
    setError(""); setCopied(false); setBusy(true);
    try { await resourceAction(reference, "copy_file"); setCopied(true); }
    catch (failure) { setError(failure instanceof Error ? failure.message : "复制文件失败"); }
    finally { setBusy(false); }
  };
  return <div className="v2-file-attachment" onContextMenu={native ? (event) => {
    event.preventDefault(); setMenu({ x: event.clientX, y: event.clientY, trigger: event.currentTarget });
  } : undefined}>
    <File size={20} aria-hidden="true" />
    <div className="v2-file-attachment-info">{canPreview
      ? <button className="v2-file-attachment-preview" type="button" ref={previewTrigger}
        aria-label={t(`预览文件 ${file.name}`, `Preview file ${file.name}`)} title={file.name} onClick={() => setPreview(true)}>{file.name}</button>
      : <strong title={file.name}>{file.name}</strong>}
      <small className="v2-file-attachment-size">{bytesLabel(file.byte_size)}</small>
      {locked && <small>{t("随原消息提交", "Included in the original message")}</small>}
      {error && <small role="alert">{error}</small>}
      {copied && <small role="status">已复制文件，可粘贴到输入框</small>}
    </div>
    {native && <button type="button" className="v2-composer-icon" aria-label={`复制文件 ${file.name}`} title="复制文件" disabled={busy} onClick={() => void copy()}><Copy size={16} /></button>}
    <button type="button" className="v2-composer-icon" aria-label={t(`下载文件 ${file.name}`, `Download file ${file.name}`)} disabled={busy} onClick={() => void download()}>
      {busy ? <LoaderCircle size={16} className="spin" /> : <Download size={16} />}</button>
    {onRemove && <button type="button" className="v2-composer-icon" aria-label={t(`移除文件 ${file.name}`, `Remove file ${file.name}`)} disabled={locked} onClick={onRemove}><X size={16} /></button>}
    {native && <button type="button" className="v2-composer-icon" aria-label={`文件操作 ${file.name}`} aria-haspopup="menu" onClick={(event) => {
      const rect = event.currentTarget.getBoundingClientRect(); setMenu({ x: rect.right - 248, y: rect.bottom, trigger: event.currentTarget });
    }}><MoreHorizontal size={16} /></button>}
    {menu && <ResourceMenu position={menu} items={fileMenuItems(reference, () => canPreview ? setPreview(true) : resourceAction(reference, "open"))}
      onClose={() => setMenu(null)} onResult={(message, failed) => { if (failed) setError(message); else if (message === "已复制文件") setCopied(true); }} />}
    {preview && <TextAttachmentPreview client={client} file={file} onClose={() => setPreview(false)}
      onRestore={locked ? undefined : onRestore} returnFocusRef={previewTrigger} />}
  </div>;
}
export function V2FileAttachments({ client, attachments, onRemove, onRestore, pendingIDs = [] }: {
  client: APIClient; attachments: WorkspaceFileAttachment[]; onRemove?: (id: string) => void;
  onRestore?: (file: WorkspaceFileAttachment, text: string) => void; pendingIDs?: string[];
}) {
  const { t } = useLocale();
  if (!attachments.length) return null;
  return <div className="v2-file-attachments" aria-label={onRemove ? t("待发送的文件", "Files to send") : t("消息附件", "Message attachments")}>
    {attachments.map((file) => <AttachmentCard key={`${file.workspace_id}:${file.id}:${file.sha256}`} client={client} file={file}
      onRestore={onRestore ? (text) => onRestore(file, text) : undefined}
      locked={pendingIDs.includes(file.id)} onRemove={onRemove ? () => onRemove(file.id) : undefined} />)}
  </div>;
}

interface UploadAttempt { version: "v2_file_upload.v1"; operationKey: string; name: string; sha256: string; byteSize: number }
const validAttempt = (value: unknown): value is UploadAttempt => {
  if (!value || typeof value !== "object") return false;
  const attempt = value as UploadAttempt;
  return attempt.version === "v2_file_upload.v1" && /^v2-file-upload-[\w-]+$/u.test(attempt.operationKey) &&
    typeof attempt.name === "string" && attempt.name.length <= 640 && /^[a-f0-9]{64}$/u.test(attempt.sha256) &&
    Number.isSafeInteger(attempt.byteSize) && attempt.byteSize >= 0 && attempt.byteSize <= maximumFileBytes;
};
export function useV2FileInput({ client, workspaceID, threadID, disabled }: {
  client: APIClient; workspaceID: string; threadID: string; disabled: boolean;
}) {
  const references = useV2AttachmentReferences(workspaceID, threadID);
  const store = useV2RecoveryStore();
  const prefix = `file-upload:${JSON.stringify([workspaceID, threadID])}:`;
  const status = useMemo(() => ({ busy: false, error: "", attempts: new Map<string, UploadAttempt>() }), [prefix, store]);
  const [, refresh] = useReducer((count: number) => count + 1, 0);
  const reload = () => {
    if (!store) return;
    const entries = store.entries<unknown>(prefix); store.assertReadable(prefix);
    status.attempts.clear();
    for (const [key, value] of entries) {
      if (!validAttempt(value) || key !== prefix + value.operationKey) { store.reject(key); throw new Error("原文件上传记录无法读取，已保留原记录。"); }
      status.attempts.set(value.operationKey, value);
    }
  };
  useEffect(() => {
    const changed = () => { try { reload(); } catch (failure) { status.error = String(failure); } refresh(); };
    changed(); return store?.subscribePrefix?.(prefix, changed);
  }, [prefix, store]);
  const remember = (attempt: UploadAttempt) => {
    store?.write(prefix + attempt.operationKey, attempt); // Fail before POST if the original identity cannot be saved.
    status.attempts.set(attempt.operationKey, attempt); refresh();
  };
  const forget = (operationKey: string) => {
    try { store?.remove(prefix + operationKey); status.attempts.delete(operationKey); refresh(); }
    catch (failure) { status.error = String(failure); refresh(); }
  };
  const stillPending = (attempt: UploadAttempt) => {
    if (!store) return status.attempts.has(attempt.operationKey);
    const saved = store.read<unknown>(prefix + attempt.operationKey, null);
    store.assertReadable(prefix);
    return validAttempt(saved) && saved.operationKey === attempt.operationKey &&
      saved.sha256 === attempt.sha256 && saved.byteSize === attempt.byteSize;
  };
  const add = async (files: File[], batchCapture?: DraftCapture): Promise<WorkspaceFileAttachment[]> => {
    const saved: WorkspaceFileAttachment[] = [];
    if (disabled || !workspaceID || !files.length || status.busy) return saved;
    if (files.length + references.attachments.length + status.attempts.size > maximumFileAttachments) {
      status.error = "每条消息最多添加 4 个文件；请先处理待核对的附件。"; refresh(); return saved;
    }
    status.busy = true; status.error = ""; refresh();
    const failures: string[] = [];
    try {
      const captured = batchCapture ?? references.draft?.document.capture(references.draft.scope, references.draft.state.ref);
      for (const file of files) {
        if (file.size > maximumFileBytes) { failures.push(`${file.name}：超过 5 MiB，未加入。`); continue; }
        const bytes = await file.arrayBuffer();
        const sha256 = [...new Uint8Array(await crypto.subtle.digest("SHA-256", bytes))].map((byte) => byte.toString(16).padStart(2, "0")).join("");
        const attempt: UploadAttempt = { version: "v2_file_upload.v1", operationKey: `v2-file-upload-${crypto.randomUUID()}`, name: file.name, sha256, byteSize: file.size };
        remember(attempt);
        try {
          const attachment = await client.uploadWorkspaceFile(workspaceID, file, attempt.operationKey);
          if (!stillPending(attempt)) continue;
          if (captured && references.draft) {
            const next = references.draft.document.updateCaptured(captured, (current) => ({ ...current,
              attachments: (current.attachments ?? []).some(({ id }) => id === attachment.id) ? current.attachments : [...(current.attachments ?? []), attachment] }));
            if (next.error || !next.persisted) throw new Error(next.error || "附件尚未保存到草稿。");
          } else references.update((current) => current.some(({ id }) => id === attachment.id) ? current : [...current, attachment]);
          saved.push(attachment);
          forget(attempt.operationKey);
        } catch (failure) { failures.push(`${file.name}：${failure instanceof Error ? failure.message : "上传结果未确认"}`); }
      }
    } catch (failure) { failures.push(failure instanceof Error ? failure.message : "附件未加入"); }
    finally { status.busy = false; status.error = failures.join("\n"); refresh(); }
    return saved;
  };
  const inspect = async (attempt: UploadAttempt) => {
    if (status.busy || disabled) return;
    status.busy = true; status.error = ""; refresh();
    // This is an explicit read of the original key, never an upload retry.
    try {
      const result = await client.inspectWorkspaceFileUpload(workspaceID, attempt.operationKey);
      if (!stillPending(attempt)) return;
      if (result.state !== "stored" || !result.attachment) throw new Error("尚未找到原上传结果；原请求仍可能稍后完成。可稍后核对，或不将该附件加入本条消息。");
      if (result.attachment.sha256 !== attempt.sha256 || result.attachment.byte_size !== attempt.byteSize) throw new Error("原上传结果与文件不符，未加入草稿。");
      if (references.attachments.length >= maximumFileAttachments && !references.attachments.some(({ id }) => id === result.attachment!.id)) throw new Error("文件数量已达上限，请先移除一个附件。");
      references.update((current) => current.some(({ id }) => id === result.attachment!.id) ? current : [...current, result.attachment!]);
      if (references.draft) {
        const next = references.draft.document.read(references.draft.scope);
        if (next.error || !next.persisted) throw new Error(next.error || "附件尚未保存到草稿。");
      }
      forget(attempt.operationKey);
    } catch (failure) { status.error = failure instanceof Error ? failure.message : "原上传结果尚未确认"; }
    finally { status.busy = false; refresh(); }
  };
  return { ...references, add, uploading: status.busy, error: status.error, pending: status.attempts.size > 0,
    notice: status.attempts.size > 0 && <div className="v2-attachment-pending" role="status">
      {[...status.attempts.values()].map((attempt) => <div key={attempt.operationKey}>
        <span>{attempt.name} · {status.busy ? "正在保存或核对" : "上传结果待核对"}</span>
        <button type="button" disabled={status.busy || disabled} onClick={() => void inspect(attempt)}>核对原上传</button>
        <button type="button" disabled={status.busy} onClick={() => forget(attempt.operationKey)}>不加入本条消息</button>
      </div>)}
    </div> };
}
