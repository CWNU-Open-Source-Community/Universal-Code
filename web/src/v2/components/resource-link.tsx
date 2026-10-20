import { useLocale } from "../../lib/locale";
import { Children, createContext, isValidElement, useContext, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useQuery } from "@tanstack/react-query";
import { Copy, Download, ExternalLink, File, GitPullRequest, GitBranch, Globe, PanelsTopLeft } from "lucide-react";
import { desktopBridgeAvailable } from "../../lib/desktop-bridge";
import { fileLabel, nativeResourcesAvailable, openWebURL, parseProjectFileLink, previewWebURL, readChatResource, resourceAction, saveWebURL, shortWebLabel, webURL, type ChatResourceRef } from "../../lib/chat-resources";
import { fileMenuItems, ResourceMenu, type MenuPosition, type ResourceMenuItem } from "./resource-menu";
import { V2ImageViewer } from "./image-viewer";

export const ChatResourceContext = createContext({ workspaceID: "" });
type OpenFile = (target: { path: string; line?: number; runID?: string; triggerElement?: HTMLElement | null }) => void;
const text = (value: ReactNode): string => Children.toArray(value).map((child): string =>
  isValidElement<{ children?: ReactNode }>(child) ? text(child.props.children) : String(child)).join("");

function FloatingPreview({ trigger, filePath, fileRef, url, onEnter, onLeave, onOpen }: {
  trigger: HTMLElement; filePath?: string; fileRef?: ChatResourceRef; url?: URL; onEnter: () => void; onLeave: () => void; onOpen: () => void;
}) {
  const { t } = useLocale();
  const element = useRef<HTMLDivElement>(null);
  const rect = trigger.getBoundingClientRect();
  const [point, setPoint] = useState({ x: rect.left, y: rect.bottom + 8, maxHeight: 520 });
  const preview = useQuery({ queryKey: ["chat-link-preview", url?.href], queryFn: () => previewWebURL(url!.href), enabled: Boolean(url), staleTime: 5 * 60_000, gcTime: 10 * 60_000, retry: false });
  const local = useQuery({ queryKey: ["chat-file-label", fileRef], queryFn: async () => (await readChatResource(fileRef!)).path,
    enabled: Boolean(fileRef?.workspace_id && nativeResourcesAvailable()), staleTime: 30_000, retry: false });
  useLayoutEffect(() => {
    const card = element.current; if (!card) return;
    const place = () => {
      const box = card.getBoundingClientRect(), anchor = trigger.getBoundingClientRect();
      const above = Math.max(0, anchor.top - 20), below = Math.max(0, window.innerHeight - anchor.bottom - 20);
      const height = Math.min(card.scrollHeight + 2, 520);
      const useBelow = height <= below || below >= above;
      const maxHeight = Math.min(520, useBelow ? below : above);
      const next = { x: Math.max(12, Math.min(anchor.left, window.innerWidth - box.width - 12)),
        y: useBelow ? anchor.bottom + 8 : anchor.top - Math.min(height, maxHeight) - 8, maxHeight };
      setPoint((current) => current.x === next.x && current.y === next.y && current.maxHeight === next.maxHeight ? current : next);
    };
    place();
    const observer = typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(place);
    observer?.observe(card);
    return () => observer?.disconnect();
  }, [trigger, preview.data, preview.isError, local.data]);
  return createPortal(<div role={filePath ? "tooltip" : "dialog"} aria-label={filePath ? undefined : t("链接预览", "Link preview")} ref={element}
    className={`v2-link-preview${filePath ? " v2-resource-file-tip" : ""}`} style={{ left: point.x, top: point.y, maxHeight: point.maxHeight }}
    onMouseEnter={onEnter} onMouseLeave={onLeave}>
    {filePath ? `${local.data || fileRef?.path || filePath}${filePath.match(/ \(line \d+\)$/u)?.[0] ?? ""}` : <>
      <header>{url?.hostname === "github.com" ? <GitBranch size={16} /> : <Globe size={16} />}<span>{url?.hostname}</span><button type="button" onClick={onOpen}>{t("打开", "Open")}<ExternalLink size={14} /></button></header>
      {preview.data?.image && <img src={preview.data.image} alt={t("网页预览", "Web preview")} />}
      <div className="v2-link-preview-body"><strong>{preview.data?.title || shortWebLabel(url!)}</strong>
        {preview.data?.description && preview.data.description.replace(/\/$/u, "") !== url?.href.replace(/\/$/u, "") && <p>{preview.data.description}</p>}<p>{url?.href}</p>
        {preview.isLoading && <p role="status">{t("正在读取网页信息…", "Loading page information…")}</p>}
        {preview.isError && <p>{t("预览暂不可用，仍可打开原链接。", "Preview unavailable. You can still open the original link.")}</p>}
      </div>
    </>}
  </div>, trigger.closest(".v2-shell") ?? document.body);
}

export function ResourceLink({ href = "", children, runID, onOpenFile }: { href?: string; children: ReactNode; runID?: string; onOpenFile?: OpenFile }) {
  const { t } = useLocale();
  const { workspaceID } = useContext(ChatResourceContext);
  const file = parseProjectFileLink(href), url = webURL(href);
  const trigger = useRef<HTMLAnchorElement>(null);
  const [menu, setMenu] = useState<MenuPosition | null>(null);
  const [hover, setHover] = useState(false);
  const [notice, setNotice] = useState({ message: "", error: false });
  const [image, setImage] = useState<{ url: string; blob: Blob; name: string; mime_type: "image/png" | "image/jpeg" | "image/webp"; width: number; height: number } | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  useEffect(() => () => { if (image) URL.revokeObjectURL(image.url); }, [image]);
  useEffect(() => {
    if (!hover) return;
    const close = () => setHover(false);
    const scroll = (event: Event) => { if (!(event.target instanceof Element && event.target.closest(".v2-link-preview"))) close(); };
    const escape = (event: KeyboardEvent) => { if (event.key === "Escape") close(); };
    window.addEventListener("resize", close); document.addEventListener("scroll", scroll, true); document.addEventListener("keydown", escape);
    return () => { window.removeEventListener("resize", close); document.removeEventListener("scroll", scroll, true); document.removeEventListener("keydown", escape); };
  }, [hover]);
  const result = (message: string, error = false) => setNotice({ message, error });
  const reference: ChatResourceRef = { workspace_id: workspaceID, run_id: runID, path: file?.path };
  const enter = () => { clearTimeout(timer.current); timer.current = setTimeout(() => setHover(true), 250); };
  const leave = () => { clearTimeout(timer.current); timer.current = setTimeout(() => setHover(false), 160); };
  const open = async () => {
    setHover(false);
    if (url) return openWebURL(url.href);
    if (!file) return;
    let path = file.path;
    if (nativeResourcesAvailable() && workspaceID) {
      const data = await readChatResource(reference);
      path = data.relative_path;
      const media = data.mime_type.split(";")[0];
      if (media === "image/png" || media === "image/jpeg" || media === "image/webp") {
        const blob = new Blob([Uint8Array.from(atob(data.data_base64), (c) => c.charCodeAt(0))], { type: media });
        const bitmap = await createImageBitmap(blob);
        try {
          if (bitmap.width * bitmap.height > 16 * 1024 * 1024) throw new Error(t("图片尺寸过大，请另存为后查看。", "This image is too large to preview. Save it to view it."));
          setImage({ url: URL.createObjectURL(blob), blob, name: data.name, mime_type: media, width: bitmap.width, height: bitmap.height });
        } finally { bitmap.close(); }
        return;
      }
      if (/\.(pdf|docx?|xlsx?|pptx?|zip|7z)$/iu.test(path)) { await resourceAction(reference, "open"); return; }
    }
    onOpenFile?.({ path, line: file.line, runID, triggerElement: trigger.current });
  };
  const label = text(children), rawLabel = children == null || label === href || label === url?.href;
  if (!file && !url) return <span>{children}</span>;
  const Icon = file ? File : url?.hostname === "github.com" ? GitBranch : Globe;
  const items: ResourceMenuItem[] = file ? nativeResourcesAvailable() && workspaceID ? fileMenuItems(reference, open, t) : [
    { label: t("打开文件", "Open file"), icon: <File />, action: open }, { label: t("复制路径", "Copy path"), successMessage: t("已复制路径", "Path copied"), icon: <Copy />, action: () => navigator.clipboard.writeText(file.path) },
  ] : [
    ...(url?.hostname === "github.com" && /^\/[^/]+\/[^/]+\/pull\/\d+\/?$/u.test(url.pathname) ? [{ label: t("在 GitHub 查看改动", "View changes on GitHub"), icon: <GitPullRequest />, action: () => openWebURL(`${url.origin}${url.pathname.replace(/\/$/u, "")}/files`) }] : []),
    { label: t("在浏览器中打开", "Open in browser"), icon: <Globe />, action: () => openWebURL(url!.href) },
    ...(!desktopBridgeAvailable() ? [
      { label: t("在新标签页打开", "Open in new tab"), icon: <PanelsTopLeft />, action: () => openWebURL(url!.href, "tab") },
      { label: t("在新窗口打开", "Open in new window"), icon: <ExternalLink />, action: () => openWebURL(url!.href, "window") },
    ] : []),
    { label: t("复制链接", "Copy link"), successMessage: t("已复制链接", "Link copied"), icon: <Copy />, divider: true, action: () => navigator.clipboard.writeText(url!.href) },
    ...(nativeResourcesAvailable() ? [{ label: t("链接另存为…", "Save link as…"), icon: <Download />, action: () => saveWebURL(url!.href) }] : []),
  ];
  return <><a ref={trigger} href={file ? "#workspace-file" : url!.href} className={`v2-resource-link${file ? " v2-file-link" : ""}`}
    title={file ? `${file.path}${file.line ? ` (line ${file.line})` : ""}` : url!.href}
    onClick={(event) => { event.preventDefault(); void open().catch((error) => result(error instanceof Error ? error.message : t("无法打开资源", "Could not open resource"), true)); }}
    onMouseEnter={enter} onMouseLeave={leave} onFocus={enter} onBlur={leave}
    onContextMenu={(event) => { event.preventDefault(); clearTimeout(timer.current); setHover(false); setMenu({ x: event.clientX, y: event.clientY, trigger: event.currentTarget }); }}
    onKeyDown={(event) => { if (event.key === "ContextMenu" || (event.shiftKey && event.key === "F10")) { event.preventDefault(); const rect = event.currentTarget.getBoundingClientRect(); setHover(false); setMenu({ x: rect.left, y: rect.bottom, trigger: event.currentTarget }); } }}>
    <Icon aria-hidden="true" />{rawLabel ? file ? fileLabel(file) : shortWebLabel(url!) : children}
  </a>
    {hover && !menu && trigger.current && <FloatingPreview trigger={trigger.current} filePath={file ? `${file.path}${file.line ? ` (line ${file.line})` : ""}` : undefined} fileRef={file ? reference : undefined}
      url={url ?? undefined} onEnter={() => clearTimeout(timer.current)} onLeave={leave} onOpen={() => { void open().catch((error) => result(String(error), true)); }} />}
    {menu && <ResourceMenu position={menu} items={items} onClose={() => setMenu(null)} onResult={result} />}
    {notice.message && <span className="v2-resource-status" role={notice.error ? "alert" : "status"}>{notice.message}</span>}
    {image && <V2ImageViewer image={image} blob={image.blob} url={image.url} onClose={() => setImage(null)} returnFocusRef={trigger} />}
  </>;
}

export function ChatLinkErrors() {
  const { t } = useLocale();
  const [error, setError] = useState("");
  useEffect(() => {
    const report = (event: Event) => setError(String((event as CustomEvent).detail));
    document.addEventListener("uc-link-error", report);
    return () => document.removeEventListener("uc-link-error", report);
  }, []);
  return error ? <p className="v2-resource-status" role="alert">{error} <button type="button" onClick={() => setError("")}>{t("关闭", "Close")}</button></p> : null;
}

export function ResourceImage({ src = "", alt: imageAlt, runID, onOpenFile }: { src?: string; alt?: string; runID?: string; onOpenFile?: OpenFile }) {
  const { t } = useLocale();
  const alt = imageAlt ?? t("图片", "Image");
  const { workspaceID } = useContext(ChatResourceContext);
  const file = parseProjectFileLink(src);
  const resource = useQuery({ queryKey: ["chat-generated-image", workspaceID, runID, file?.path],
    queryFn: () => readChatResource({ workspace_id: workspaceID, run_id: runID, path: file!.path }),
    enabled: Boolean(file && workspaceID && nativeResourcesAvailable()), retry: false, staleTime: 30_000 });
  if (!file) return <img src={src} alt={alt} className="v2-resource-image" />;
  const media = resource.data?.mime_type.split(";")[0];
  return <ResourceLink href={src} runID={runID} onOpenFile={onOpenFile}>
    {resource.data && ["image/png", "image/jpeg", "image/webp"].includes(media ?? "")
      ? <img className="v2-resource-image" src={`data:${media};base64,${resource.data.data_base64}`} alt={alt} /> : alt}
  </ResourceLink>;
}
