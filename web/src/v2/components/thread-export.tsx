import { useEffect, useId, useRef, useState, type RefObject } from "react";
import { createPortal } from "react-dom";
import { Copy, Download, RefreshCw, X } from "lucide-react";
import type { APIClient } from "../../api/client";
import { useModalFocusTrap } from "../../hooks/use-modal-focus-trap";
import { downloadTextFile } from "../../lib/download";
import { localUIMessage } from "../../lib/local-ui-message";
import { useLocale } from "../../lib/locale";
import { formatThreadExport, readThreadExport, threadExportFilename, type ExportMessage, type ThreadExportFormat } from "../thread-export";
import "./thread-export.css";

export function V2ThreadExport({ client, threadID, title, onClose, returnFocusRef }: {
  client: APIClient; threadID: string; title: string; onClose: () => void;
  returnFocusRef: RefObject<HTMLElement | null>;
}) {
  const { t } = useLocale();
  const titleID = useId();
  const close = useRef<HTMLButtonElement>(null);
  const dialog = useModalFocusTrap<HTMLElement>(true, onClose, false, close, { isolateBackground: true, returnFocusRef });
  const [format, setFormat] = useState<ThreadExportFormat>("markdown");
  const [attempt, setAttempt] = useState(0);
  const [messages, setMessages] = useState<ExportMessage[] | null>(null);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [copying, setCopying] = useState(false);
  const active = useRef(true);
  useEffect(() => {
    active.current = true;
    const abort = new AbortController();
    setMessages(null); setError(""); setNotice("");
    void readThreadExport(client, threadID, abort.signal, t).then((result) => {
      if (!abort.signal.aborted) setMessages(result);
    }).catch((failure: unknown) => {
      if (!abort.signal.aborted) setError(failure instanceof Error ? localUIMessage(failure.message, t) : t("无法读取对话，请重试。", "Could not read the conversation. Try again."));
    });
    return () => { active.current = false; abort.abort(); };
  }, [client, threadID, attempt, t]);
  const save = async (copy: boolean) => {
    if (!messages?.length || copying) return;
    setError(""); setNotice("");
    try {
      const content = formatThreadExport(title, messages, format, t);
      if (copy) {
        setCopying(true);
        if (!navigator.clipboard?.writeText) throw new Error(t("此环境无法写入剪贴板，请下载文件。", "Clipboard access is unavailable. Download the file instead."));
        await navigator.clipboard.writeText(content);
        if (active.current) setNotice(t("已复制对话。", "Conversation copied."));
      } else {
        downloadTextFile(threadExportFilename(title, format), format === "markdown" ? "text/markdown;charset=utf-8" : "text/plain;charset=utf-8", content);
        setNotice(t("已开始下载。", "Download started."));
      }
    } catch (failure) {
      if (active.current) setError(failure instanceof Error ? localUIMessage(failure.message, t) : t("导出失败，请重试。", "Export failed. Try again."));
    } finally { if (active.current) setCopying(false); }
  };
  return createPortal(<div className="v2-overlay" role="presentation" onMouseDown={(event) => {
    if (event.target === event.currentTarget) onClose();
  }}>
    <section className="v2-dialog v2-thread-export" role="dialog" aria-modal="true" aria-labelledby={titleID} ref={dialog} tabIndex={-1}>
      <header><span><Download size={17} aria-hidden="true" /></span><h2 id={titleID}>{t("导出对话", "Export conversation")}</h2>
        <button type="button" aria-label={t("关闭导出窗口", "Close export")} ref={close} onClick={onClose}><X size={16} aria-hidden="true" /></button></header>
      <div className="v2-thread-export-body">
        <p>{t("保存已发送的用户消息和助手回复，保留原文与代码。附件列出文件名。", "Save sent user messages and assistant replies, preserving the original text and code. Attachments are listed by name.")}</p>
        <label>{t("文件格式", "File format")}<select value={format} onChange={(event) => { setFormat(event.target.value as ThreadExportFormat); setNotice(""); }}>
          <option value="markdown">Markdown (.md)</option><option value="text">{t("文本 (.txt)", "Text (.txt)")}</option>
        </select></label>
        {!messages && !error && <p role="status">{t("正在读取完整对话…", "Reading the full conversation…")}</p>}
        {messages && <p role="status">{messages.length ? t(`已准备 ${messages.length} 条消息。`, `${messages.length} ${messages.length === 1 ? "message" : "messages"} ready.`) : t("暂无可导出的消息。", "No messages to export yet.")}</p>}
        {error && <p role="alert">{error}</p>}
        {!messages && error && <button type="button" onClick={() => setAttempt((value) => value + 1)}><RefreshCw size={15} aria-hidden="true" />{t("重新读取", "Read again")}</button>}
        {notice && <p role="status">{notice}</p>}
      </div>
      <footer><button type="button" disabled={!messages?.length || copying} onClick={() => void save(true)}><Copy size={15} aria-hidden="true" />{copying ? t("正在复制…", "Copying…") : t("复制文本", "Copy text")}</button>
        <button type="button" className="primary" disabled={!messages?.length || copying} onClick={() => void save(false)}><Download size={15} aria-hidden="true" />{t("下载文件", "Download file")}</button></footer>
    </section>
  </div>, document.body);
}
