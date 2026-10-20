import { useLayoutEffect, useRef, useState, type RefObject } from "react";
import { createPortal } from "react-dom";
import { Copy, Download, Maximize2, Minus, Plus, X } from "lucide-react";
import type { WorkspaceImageAttachment } from "../../api/image-attachments";
import { useModalFocusTrap } from "../../hooks/use-modal-focus-trap";

type PreviewImage = Pick<WorkspaceImageAttachment, "name" | "mime_type" | "width" | "height">;
export async function copyImageToClipboard(blob: Blob, image: PreviewImage): Promise<void> {
  if (!navigator.clipboard?.write || typeof ClipboardItem === "undefined") throw new Error("当前浏览器不支持复制图片，请下载原图。");
  const png = clipboardPNG(blob, image);
  void png.catch(() => {});
  await navigator.clipboard.write([new ClipboardItem({ "image/png": png })]);
}
async function clipboardPNG(blob: Blob, image: PreviewImage): Promise<Blob> {
  // Reuse the authenticated, hash-verified bytes. Fetching the display's blob:
  // URL is unnecessary and disallowed by the page's connect-src policy.
  if (blob.type !== image.mime_type) throw new Error("原图格式与保存记录不一致。");
  if (blob.type === "image/png") return blob;
  if (typeof createImageBitmap === "undefined") throw new Error("当前浏览器不支持将此格式复制为 PNG，请下载原图。");
  const bitmap = await createImageBitmap(blob);
  try {
    if (bitmap.width !== image.width || bitmap.height !== image.height) throw new Error("原图尺寸与保存记录不一致。");
    const canvas = document.createElement("canvas");
    canvas.width = bitmap.width;
    canvas.height = bitmap.height;
    const context = canvas.getContext("2d");
    if (!context) throw new Error("当前浏览器无法转换图片，请下载原图。");
    context.drawImage(bitmap, 0, 0);
    return await new Promise<Blob>((resolve, reject) => canvas.toBlob((png) =>
      png ? resolve(png) : reject(new Error("图片复制转换失败，请下载原图。")), "image/png"));
  } finally { bitmap.close(); }
}

/** Presents the already verified original Blob URL; zoom never rewrites pixels. */
export function V2ImageViewer({ image, url, blob, onClose, returnFocusRef }: {
  image: PreviewImage;
  url: string;
  blob: Blob;
  onClose: () => void;
  returnFocusRef: RefObject<HTMLElement | null>;
}) {
  const closeButton = useRef<HTMLButtonElement>(null);
  const stage = useRef<HTMLDivElement>(null);
  const modal = useModalFocusTrap<HTMLElement>(true, onClose, false, closeButton,
    { isolateBackground: true, returnFocusRef });
  const [zoom, setZoom] = useState<number | null>(null);
  const [copying, setCopying] = useState(false);
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState("");
  const [space, setSpace] = useState(() => ({ width: Math.max(1, window.innerWidth - 96), height: Math.max(1, window.innerHeight - 180) }));
  useLayoutEffect(() => {
    const element = stage.current;
    if (!element) return;
    const measure = () => {
      if (element.clientWidth > 0 && element.clientHeight > 0) {
        setSpace({ width: Math.max(1, element.clientWidth - 48), height: Math.max(1, element.clientHeight - 48) });
      }
    };
    measure();
    const observer = typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(measure);
    observer?.observe(element);
    window.addEventListener("resize", measure);
    return () => { observer?.disconnect(); window.removeEventListener("resize", measure); };
  }, []);
  const fit = Math.min(1, space.width / image.width, space.height / image.height);
  const scale = zoom ?? fit;
  const minimum = Math.min(0.05, fit);
  const changeZoom = (factor: number) => setZoom(Math.min(4, Math.max(minimum, scale * factor)));
  const name = image.name || "截图";
  const copy = async () => {
    if (copying) return;
    setCopied(false); setCopyError("");
    if (!navigator.clipboard?.write || typeof ClipboardItem === "undefined") {
      setCopyError("当前浏览器不支持复制图片，请下载原图。");
      return;
    }
    setCopying(true);
    try {
      await copyImageToClipboard(blob, image);
      setCopied(true);
    } catch (failure) {
      setCopyError(failure instanceof DOMException && failure.name === "NotAllowedError"
        ? "浏览器未允许写入剪贴板，请允许权限后重试，或下载原图。"
        : `复制图片失败：${failure instanceof Error ? failure.message : "请重试，或下载原图。"}`);
    } finally { setCopying(false); }
  };

  return createPortal(<div className="v2-image-overlay" onMouseDown={(event) => {
    if (event.target === event.currentTarget) onClose();
  }}>
    <section className="v2-image-dialog" role="dialog" aria-modal="true" aria-label={`图片预览 ${name}`} ref={modal}
      onKeyDown={(event) => {
        if (event.ctrlKey || event.metaKey || event.altKey) return;
        if (["+", "=", "-", "0", "1"].includes(event.key)) event.preventDefault();
        if (event.key === "+" || event.key === "=") changeZoom(1.25);
        else if (event.key === "-") changeZoom(0.8);
        else if (event.key === "0") setZoom(null);
        else if (event.key === "1") setZoom(1);
      }}>
      <header className="v2-image-viewer-header">
        <div><strong title={name}>{name}</strong><small>原图 {image.width} × {image.height}</small></div>
        <button type="button" aria-label="复制图片" title="复制图片（剪贴板使用 PNG）" onClick={() => { void copy(); }} disabled={copying}><Copy size={18} aria-hidden="true" /></button>
        <a href={url} download={name} aria-label={`下载原图 ${name}`} title="下载原图"><Download size={18} aria-hidden="true" /></a>
        <button ref={closeButton} onClick={onClose} aria-label="关闭图片预览" title="关闭（Esc）" type="button"><X size={20} aria-hidden="true" /></button>
      </header>
      <div className="v2-image-viewer-stage" ref={stage} tabIndex={0} aria-label="原图查看区域，放大后可滚动查看">
        <div className="v2-image-viewer-canvas"><img src={url} alt={name} draggable={false}
          style={{ width: image.width * scale, height: image.height * scale }} /></div>
      </div>
      <footer className="v2-image-viewer-toolbar" aria-label="图片缩放">
        <button type="button" aria-label="缩小图片" title="缩小（−）" disabled={scale <= minimum} onClick={() => changeZoom(0.8)}><Minus size={18} aria-hidden="true" /></button>
        <output aria-label="图片缩放比例" aria-live="polite">{Math.round(scale * 100)}%</output>
        <button type="button" aria-label="放大图片" title="放大（+）" disabled={scale >= 4} onClick={() => changeZoom(1.25)}><Plus size={18} aria-hidden="true" /></button>
        <span aria-hidden="true" />
        <button type="button" aria-pressed={zoom === null} onClick={() => setZoom(null)} title="适应窗口（0）"><Maximize2 size={15} aria-hidden="true" />适应窗口</button>
        <button type="button" aria-pressed={zoom === 1} onClick={() => setZoom(1)} title="原始尺寸（1）">100%</button>
        {copying && <p className="v2-image-viewer-notice" role="status">正在复制图片…</p>}
        {copied && <p className="v2-image-viewer-notice" role="status">已复制图片</p>}
        {copyError && <p className="v2-image-viewer-notice" role="alert">{copyError}</p>}
      </footer>
    </section>
  </div>, document.body);
}
