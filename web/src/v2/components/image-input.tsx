import { useLocale } from "../../lib/locale";
import { useEffect, useMemo, useReducer, useRef, useState, type ClipboardEvent, type DragEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, Download, ImagePlus, LoaderCircle, X } from "lucide-react";
import type { APIClient } from "../../api/client";
import { acceptedImageTypes, maximumImageBytes, maximumImages, type WorkspaceImageAttachment } from "../../api/image-attachments";
import { copyImageToClipboard, V2ImageViewer } from "./image-viewer";
import { ResourceMenu, type MenuPosition } from "./resource-menu";
import { useV2DraftDocument } from "../draft-context";
import type { DraftCapture } from "../draft-document";
import { useV2RecoveryStore } from "../recovery-storage";
import "./image-input.css";

export const v2ImageReferenceKey = (workspaceID: string, threadID: string) => ["v2", "draft-images", workspaceID, threadID] as const;
export function useV2ImageReferences(workspaceID: string, threadID: string) {
  const draft = useV2DraftDocument(workspaceID, threadID);
  const client = useQueryClient();
  const key = v2ImageReferenceKey(workspaceID, threadID);
  const query = useQuery<WorkspaceImageAttachment[]>({ queryKey: key, queryFn: () => [],
    enabled: false, initialData: [], gcTime: Infinity });
  const update = (change: (images: WorkspaceImageAttachment[]) => WorkspaceImageAttachment[]) => {
    if (draft) draft.document.update(draft.scope, (current) => ({ ...current, images: change(current.images) }), draft.state.ref);
    else client.setQueryData<WorkspaceImageAttachment[]>(key, (current) => change(current ?? []));
  };
  return { images: draft?.state.snapshot.images ?? query.data, update, draft };
}

function ImagePreview({ client, image, onRemove, locked }: {
  client: APIClient; image: WorkspaceImageAttachment; onRemove?: () => void; locked?: boolean;
}) {
  const { t } = useLocale();
  const [original, setOriginal] = useState<{ url: string; blob: Blob } | null>(null);
  const [error, setError] = useState("");
  const [retry, setRetry] = useState(0);
  const [open, setOpen] = useState(false);
  const [menu, setMenu] = useState<MenuPosition | null>(null);
  const [notice, setNotice] = useState({ message: "", error: false });
  const trigger = useRef<HTMLButtonElement>(null);
  const close = () => setOpen(false);
  useEffect(() => {
    const abort = new AbortController();
    let objectURL = "";
    setOriginal(null); setError("");
    void client.downloadWorkspaceImage(image, abort.signal).then((blob) => {
      if (abort.signal.aborted) return;
      objectURL = URL.createObjectURL(blob); setOriginal({ url: objectURL, blob });
    }).catch((failure) => { if (!abort.signal.aborted) setError(failure instanceof Error ? failure.message : t("图片暂时无法读取", "Image temporarily unavailable")); });
    return () => { abort.abort(); if (objectURL) URL.revokeObjectURL(objectURL); };
  }, [client, image.id, image.sha256, image.workspace_id, retry]);
  const name = image.name || t("截图", "Screenshot");
  const url = original?.url;
  const menuItems = original ? [
    { label: t("打开图片", "Open image"), action: () => setOpen(true) },
    { label: t("复制图片", "Copy image"), successMessage: t("已复制图片", "Image copied"), icon: <Copy />, action: () => copyImageToClipboard(original.blob, image, t) },
    { label: t("图片另存为…", "Save image as…"), icon: <Download />, action: () => {
      const a = document.createElement("a"); a.href = original.url; a.download = name; document.body.append(a); a.click(); a.remove();
    } },
  ] : [];
  return <div className="v2-image-item" onContextMenu={original ? (event) => {
    event.preventDefault(); setMenu({ x: event.clientX, y: event.clientY, trigger: trigger.current! });
  } : undefined}>
    <button className="v2-image-thumb" type="button" disabled={!url} ref={trigger} onClick={() => setOpen(true)}
      aria-label={t(`预览图片 ${name}`, `Preview image ${name}`)} title={`${name} · ${image.width} × ${image.height}`} onKeyDown={(event) => {
        if (original && (event.key === "ContextMenu" || event.shiftKey && event.key === "F10")) {
          event.preventDefault(); const rect = event.currentTarget.getBoundingClientRect(); setMenu({ x: rect.left, y: rect.bottom, trigger: event.currentTarget });
        }
      }}>
      {url ? <img src={url} alt={name} /> : error ? <span>{t("读取失败", "Could not load")}</span> : <LoaderCircle className="spin" size={18} />}
    </button>
    <span className="v2-image-caption" title={name}>{name}</span>
    {onRemove && <button className="v2-image-remove" type="button" aria-label={t(`移除图片 ${name}`, `Remove image ${name}`)} disabled={locked}
      onClick={onRemove}><X size={14} aria-hidden="true" /></button>}
    {locked && <small>{t("随原消息提交", "Submitting with original message")}</small>}
    {notice.message && <small className="v2-image-notice" role={notice.error ? "alert" : "status"}>{notice.message}</small>}
    {menu && <ResourceMenu position={menu} items={menuItems} onClose={() => setMenu(null)} onResult={(message, error = false) => setNotice({ message, error })} />}
    {error && <button className="v2-image-retry" title={error} type="button" onClick={() => setRetry((value) => value + 1)}>{t("重新读取图片", "Reload image")}</button>}
    {open && original && <V2ImageViewer image={image} url={original.url} blob={original.blob} onClose={close} returnFocusRef={trigger} />}
  </div>;
}

export function V2ImagePreview({ client, images, onRemove, pendingIDs = [] }: {
  client: APIClient; images: WorkspaceImageAttachment[]; onRemove?: (id: string) => void; pendingIDs?: string[];
}) {
  const { t } = useLocale();
  if (!images.length) return null;
  return <div className="v2-image-list" aria-label={onRemove ? t("待发送的图片", "Images to send") : t("消息图片", "Message images")}>
    {images.map((image) => <ImagePreview key={`${image.workspace_id}:${image.id}:${image.sha256}`} client={client} image={image}
      locked={pendingIDs.includes(image.id)} onRemove={onRemove ? () => onRemove(image.id) : undefined} />)}
  </div>;
}

interface ImageUploadAttempt { version: "v2_image_upload.v1"; operationKey: string; name: string; sha256: string; byteSize: number; mimeType: string }
const validAttempt = (value: unknown): value is ImageUploadAttempt => {
  if (!value || typeof value !== "object") return false;
  const attempt = value as ImageUploadAttempt;
  return attempt.version === "v2_image_upload.v1" && /^v2-image-upload-[\w-]+$/u.test(attempt.operationKey) &&
    typeof attempt.name === "string" && attempt.name.length <= 640 && /^[a-f0-9]{64}$/u.test(attempt.sha256) &&
    Number.isSafeInteger(attempt.byteSize) && attempt.byteSize > 0 && attempt.byteSize <= maximumImageBytes &&
    acceptedImageTypes.includes(attempt.mimeType);
};

export function useV2ImageInput({ client, workspaceID, threadID, disabled }: {
  client: APIClient; workspaceID: string; threadID: string; disabled: boolean;
}) {
  const { t } = useLocale();
  const references = useV2ImageReferences(workspaceID, threadID);
  const store = useV2RecoveryStore();
  const prefix = `image-upload:${JSON.stringify([workspaceID, threadID])}:`;
  const status = useMemo(() => ({ busy: false, error: "", unreadable: false, attempts: new Map<string, ImageUploadAttempt>(), observed: new Set<string>() }), [prefix, store]);
  const [, refresh] = useReducer((count: number) => count + 1, 0);
  const setError = (error: string) => { status.error = error; refresh(); };
  const setUploading = (busy: boolean) => { status.busy = busy; refresh(); };
  const uploading = status.busy;
  const error = status.error;
  const reload = () => {
    if (!store) return;
    const entries = store.entries<unknown>(prefix); store.assertReadable(prefix);
    const recovered = new Map<string, ImageUploadAttempt>();
    for (const [key, value] of entries) {
      if (!validAttempt(value) || key !== prefix + value.operationKey) {
        store.reject(key); throw new Error(t("原图片上传记录无法读取，已保留原记录。", "The original image upload record cannot be read. Its record is kept."));
      }
      recovered.set(value.operationKey, value);
    }
    status.attempts.clear();
    recovered.forEach((attempt, key) => status.attempts.set(key, attempt));
  };
  useEffect(() => {
    const changed = () => { try { reload(); status.unreadable = false; } catch (failure) { status.error = String(failure); status.unreadable = true; } refresh(); };
    changed(); return store?.subscribePrefix?.(prefix, changed);
  }, [prefix, store]);
  const remember = (attempt: ImageUploadAttempt) => {
    store?.write(prefix + attempt.operationKey, attempt);
    status.attempts.set(attempt.operationKey, attempt); refresh();
  };
  const forget = (operationKey: string) => {
    try { store?.remove(prefix + operationKey); status.attempts.delete(operationKey); refresh(); }
    catch (failure) { setError(String(failure)); }
  };
  const stillPending = (attempt: ImageUploadAttempt) => {
    if (!store) return status.attempts.has(attempt.operationKey);
    const saved = store.read<unknown>(prefix + attempt.operationKey, null);
    store.assertReadable(prefix);
    return validAttempt(saved) && saved.operationKey === attempt.operationKey && saved.sha256 === attempt.sha256 &&
      saved.byteSize === attempt.byteSize && saved.mimeType === attempt.mimeType;
  };
  const picker = useRef<HTMLInputElement>(null);
  const add = async (files: File[], batchCapture?: DraftCapture) => {
    if (disabled || !workspaceID || !files.length || status.busy || status.unreadable) return;
    if (files.length + references.images.length + status.attempts.size > maximumImages) { setError(t("每条消息最多添加 4 张图片。", "Each message supports up to 4 images.")); return; }
    if (files.some((file) => !acceptedImageTypes.includes(file.type) || !file.size || file.size > maximumImageBytes)) {
      setError(t("请选择 PNG、JPEG 或 WebP，每张不超过 5 MiB。", "Choose PNG, JPEG or WebP images, up to 5 MiB each.")); return;
    }
    setUploading(true); setError("");
    try {
      const captured = batchCapture ?? references.draft?.document.capture(references.draft.scope, references.draft.state.ref);
      for (const file of files) {
        const bytes = await file.arrayBuffer();
        const sha256 = [...new Uint8Array(await crypto.subtle.digest("SHA-256", bytes))].map((byte) => byte.toString(16).padStart(2, "0")).join("");
        const attempt: ImageUploadAttempt = { version: "v2_image_upload.v1", operationKey: `v2-image-upload-${crypto.randomUUID()}`,
          name: [...file.name].slice(0, 160).join(""), sha256, byteSize: file.size, mimeType: file.type };
        if ([...status.attempts.values()].some((pending) => pending.sha256 === sha256 && pending.byteSize === file.size && pending.mimeType === file.type && pending.name === attempt.name)) {
          throw new Error(t("这张图片的原上传结果尚待核对，请先核对原图片上传。", "This image's original upload is unconfirmed. Check the original image upload first."));
        }
        // Never observe this window's still-running upload as an absent receipt.
        status.observed.add(attempt.operationKey); remember(attempt);
        const image = await client.uploadWorkspaceImage(workspaceID, file, attempt.operationKey);
        if (!stillPending(attempt)) continue;
        if (captured && references.draft) {
          const next = references.draft.document.updateCaptured(captured,
            (current) => ({ ...current, images: current.images.some(({ id }) => id === image.id) ? current.images : [...current.images, image] }));
          if (next.error || !next.persisted) throw new Error(next.error || t("图片尚未保存到草稿。", "The image has not been saved to the draft."));
        } else references.update((current) => current.some(({ id }) => id === image.id) ? current : [...current, image]);
        forget(attempt.operationKey);
      }
    } catch (failure) { setError(failure instanceof Error ? failure.message : t("图片上传失败，请重新选择。", "Image upload failed. Select it again.")); }
    finally { setUploading(false); }
  };
  const inspect = async (attempt: ImageUploadAttempt) => {
    if (status.busy || disabled || status.unreadable) return;
    setUploading(true); setError("");
    try {
      const result = await client.inspectWorkspaceImageUpload(workspaceID, attempt.operationKey);
      if (!stillPending(attempt)) return;
      if (result.state !== "stored" || !result.image) throw new Error(t("尚未找到原图片上传结果；原请求仍可能稍后完成。可稍后核对，或不将该图片加入本条消息。", "The original image upload was not found; it may still finish. Check later or leave the image out of this message."));
      const image = result.image;
      if (image.sha256 !== attempt.sha256 || image.byte_size !== attempt.byteSize || image.mime_type !== attempt.mimeType) throw new Error(t("原上传结果与图片不符，未加入草稿。", "The upload result does not match the image and was not added to the draft."));
      const append = (current: WorkspaceImageAttachment[]) => {
        if (current.some(({ id }) => id === image.id)) return current;
        if (current.length >= maximumImages) throw new Error(t("图片数量已达上限，请先移除一张图片。", "The image limit is reached. Remove an image first."));
        return [...current, image];
      };
      if (references.draft) {
        const latest = references.draft.document.read(references.draft.scope);
        if (latest.conflict || latest.error) throw new Error(latest.error || t("请先处理草稿版本冲突。", "Resolve the draft conflict first."));
        const next = references.draft.document.update(references.draft.scope, (current) => ({ ...current, images: append(current.images) }), latest.ref);
        if (next.error || !next.persisted) throw new Error(next.error || t("图片尚未保存到草稿。", "The image has not been saved to the draft."));
      } else references.update(append);
      forget(attempt.operationKey);
    } catch (failure) { setError(failure instanceof Error ? failure.message : t("原图片上传结果尚未确认", "The original image upload is unconfirmed")); }
    finally { setUploading(false); }
  };
  const pendingKeys = [...status.attempts.keys()].join("|");
  useEffect(() => {
    if (disabled || status.busy || status.unreadable) return;
    const attempt = [...status.attempts.values()].find((item) => !status.observed.has(item.operationKey));
    if (attempt) { status.observed.add(attempt.operationKey); void inspect(attempt); }
    // One read per recovered key and mount. Never re-upload or poll an absent result.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status, pendingKeys, uploading, disabled]);
  const onPaste = (event: ClipboardEvent<HTMLFormElement>) => {
    const files = [...event.clipboardData.files].filter((file) => file.type.startsWith("image/"));
    if (files.length) { event.preventDefault(); void add(files); }
  };
  const onDrop = (event: DragEvent<HTMLFormElement>) => {
    if (!event.dataTransfer.files.length) return;
    event.preventDefault(); void add([...event.dataTransfer.files]);
  };
  const button = <>
    <button type="button" className="v2-composer-icon" aria-label={t("添加图片", "Add image")} title={t("粘贴、拖入或选择图片", "Paste, drop or choose images")}
      disabled={disabled || !workspaceID || uploading || references.images.length >= maximumImages}
      onClick={() => picker.current?.click()}>{uploading ? <LoaderCircle className="spin" size={17} /> : <ImagePlus size={17} />}</button>
    <input ref={picker} type="file" accept={acceptedImageTypes.join(",")} multiple hidden aria-label={t("选择图片文件", "Choose image files")}
      onChange={(event) => { const files = [...(event.target.files ?? [])]; event.target.value = ""; void add(files); }} />
  </>;
  return { ...references, add, uploading, error, button, onPaste, onDrop, pending: status.unreadable || status.attempts.size > 0,
    notice: status.attempts.size > 0 && <div className="v2-attachment-pending" role="status">
      {[...status.attempts.values()].map((attempt) => <div key={attempt.operationKey}>
        <span>{attempt.name} · {status.busy ? t("正在保存或核对", "Saving or checking") : t("图片上传结果待核对", "Image upload awaiting confirmation")}</span>
        <button type="button" disabled={status.busy || disabled} onClick={() => void inspect(attempt)}>{t("核对原图片上传", "Check original image upload")}</button>
        <button type="button" disabled={status.busy} onClick={() => forget(attempt.operationKey)}>{t("不加入本条消息", "Leave out of this message")}</button>
      </div>)}
    </div>,
    onDragOver: (event: DragEvent<HTMLFormElement>) => { if (event.dataTransfer.types.includes("Files")) event.preventDefault(); } };
}
