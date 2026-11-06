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

export function useV2ImageInput({ client, workspaceID, threadID, disabled }: {
  client: APIClient; workspaceID: string; threadID: string; disabled: boolean;
}) {
  const { t } = useLocale();
  const references = useV2ImageReferences(workspaceID, threadID);
  const status = useMemo(() => ({ busy: false, error: "" }), [workspaceID, threadID]);
  const [, refresh] = useReducer((count: number) => count + 1, 0);
  const setError = (error: string) => { status.error = error; refresh(); };
  const setUploading = (busy: boolean) => { status.busy = busy; refresh(); };
  const uploading = status.busy;
  const error = status.error;
  const picker = useRef<HTMLInputElement>(null);
  const add = async (files: File[], batchCapture?: DraftCapture) => {
    if (disabled || !workspaceID || !files.length || status.busy) return;
    if (files.length + references.images.length > maximumImages) { setError(t("每条消息最多添加 4 张图片。", "Each message supports up to 4 images.")); return; }
    if (files.some((file) => !acceptedImageTypes.includes(file.type) || !file.size || file.size > maximumImageBytes)) {
      setError(t("请选择 PNG、JPEG 或 WebP，每张不超过 5 MiB。", "Choose PNG, JPEG or WebP images, up to 5 MiB each.")); return;
    }
    setUploading(true); setError("");
    try {
      const captured = batchCapture ?? references.draft?.document.capture(references.draft.scope, references.draft.state.ref);
      for (const file of files) {
        const image = await client.uploadWorkspaceImage(workspaceID, file, `v2-image-upload-${crypto.randomUUID()}`);
        if (captured && references.draft) references.draft.document.updateCaptured(captured,
          (current) => ({ ...current, images: current.images.some(({ id }) => id === image.id) ? current.images : [...current.images, image] }));
        else references.update((current) => current.some(({ id }) => id === image.id) ? current : [...current, image]);
      }
    } catch (failure) { setError(failure instanceof Error ? failure.message : t("图片上传失败，请重新选择。", "Image upload failed. Select it again.")); }
    finally { setUploading(false); }
  };
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
  return { ...references, add, uploading, error, button, onPaste, onDrop,
    onDragOver: (event: DragEvent<HTMLFormElement>) => { if (event.dataTransfer.types.includes("Files")) event.preventDefault(); } };
}
