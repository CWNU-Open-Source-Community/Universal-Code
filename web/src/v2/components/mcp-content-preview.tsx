import { useLocale } from "../../lib/locale";
import { useEffect, useRef, useState } from "react";
import { Maximize2 } from "lucide-react";
import type { ThreadActivityMCPContentView } from "../../api/types";
import { safeMCPContentLink } from "../../api/mcp-content";
import { V2ImageViewer } from "./image-viewer";
import "./image-input.css";
import "./mcp-content-preview.css";

function ContentURI({ uri }: { uri: string }) {
  return safeMCPContentLink(uri) ? <a href={uri} rel="noopener noreferrer" target="_blank">{uri}</a> : <code>{uri}</code>;
}

function EmbeddedImage({ content }: { content: ThreadActivityMCPContentView }) {
  const { t } = useLocale();
  const trigger = useRef<HTMLButtonElement>(null);
  const [dimensions, setDimensions] = useState<{ width: number; height: number }>();
  const [preview, setPreview] = useState<{ url: string; blob: Blob }>();
  const [failed, setFailed] = useState(false);
  useEffect(() => () => { if (preview) URL.revokeObjectURL(preview.url); }, [preview]);
  if (failed) return <p role="status">{t("已保存的图片无法显示。", "Could not display the saved image.")}</p>;
  const media = content.mime_type;
  if (media !== "image/png" && media !== "image/jpeg" && media !== "image/webp" && media !== "image/gif") return null;
  const url = `data:${media};base64,${content.data_base64}`;
  const name = content.name || `mcp-image.${media === "image/jpeg" ? "jpg" : media.slice(6)}`;
  return <>
    <button ref={trigger} className="v2-mcp-image-preview" type="button" disabled={!dimensions}
      aria-label={t(`查看原图 ${name}`, `View original ${name}`)} onClick={() => {
        const blob = new Blob([Uint8Array.from(atob(content.data_base64!), (character) => character.charCodeAt(0))], { type: media });
        setPreview({ url: URL.createObjectURL(blob), blob });
      }}>
      <img src={url} alt={content.name || t("MCP 返回图片", "Image returned by MCP")}
        width={content.width} height={content.height} loading="lazy" decoding="async" onLoad={({ currentTarget }) => {
          if (currentTarget.naturalWidth > 0 && currentTarget.naturalHeight > 0) {
            setDimensions({ width: currentTarget.naturalWidth, height: currentTarget.naturalHeight });
          }
        }} onError={() => { setFailed(true); setPreview(undefined); }} />
      <span><Maximize2 size={14} aria-hidden="true" />{t("查看原图", "View original")}</span>
    </button>
    {preview && dimensions && <V2ImageViewer image={{ name, mime_type: media, ...dimensions }} url={preview.url} blob={preview.blob}
      onClose={() => setPreview(undefined)} returnFocusRef={trigger} />}
  </>;
}

function EmbeddedAudio({ content }: { content: ThreadActivityMCPContentView }) {
  const { t } = useLocale();
  const [failed, setFailed] = useState(false);
  return <><audio controls preload="none" src={`data:${content.mime_type};base64,${content.data_base64}`}
    aria-label={content.name || t("MCP 返回音频", "Audio returned by MCP")} onError={() => setFailed(true)} />
    {failed && <p role="status">{t("已保存的音频无法播放。", "Could not play the saved audio.")}</p>}</>;
}

export function MCPContentPreview({ contents, omitted = 0, truncated = false }: {
  contents?: ThreadActivityMCPContentView[]; omitted?: number; truncated?: boolean;
}) {
  const { t } = useLocale();
  if (!contents?.length && !omitted && !truncated) return null;
  return <section className="v2-mcp-contents" aria-label={t("MCP 返回内容", "Content returned by MCP")}>
    {contents?.map((content, index) => <figure className="v2-mcp-content" key={`${index}:${content.kind}:${content.uri ?? ""}`}>
      {(content.name || content.role) && <figcaption>{content.role ? `${content.role} · ` : ""}{content.name}</figcaption>}
      {content.uri && <p><ContentURI uri={content.uri} /></p>}
      {(content.kind === "text" || content.kind === "resource_text") && <pre className="v2-mcp-content-text">{content.text}</pre>}
      {content.kind === "image" && <EmbeddedImage key={content.data_base64} content={content} />}
      {content.kind === "audio" && <EmbeddedAudio content={content} />}
    </figure>)}
    {truncated && <p className="v2-command-truncated">{t("返回内容超过预览上限，当前显示部分内容。", "The response exceeds the preview limit. Only part is shown.")}</p>}
    {omitted > 0 && <p className="v2-command-truncated">{t(`${omitted} 项内容超出上限、格式无效或暂不支持预览。`, `${omitted} items exceed the limit, have an invalid format, or cannot be previewed.`)}</p>}
  </section>;
}
