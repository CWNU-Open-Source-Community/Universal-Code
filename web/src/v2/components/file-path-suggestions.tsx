import { useEffect, useImperativeHandle, useRef, useState, type Ref } from "react";
import { useQuery } from "@tanstack/react-query";
import { File, Folder, X } from "lucide-react";
import type { APIClient } from "../../api/client";
import type { WorkspaceExplorerView } from "../../api/types";
import { useLocale } from "../../lib/locale";
import type { FilePathMention } from "./file-path-mention";
import "./file-path-suggestions.css";

export interface FilePathTarget extends FilePathMention { original: string }
export interface FilePathSuggestionsHandle { keyDown(event: KeyboardEvent): boolean }

export function FilePathSuggestions({ client, workspaceID, target, listID, inputRef, onActive, onClose, onSelect }: {
  client: APIClient; workspaceID: string; target: FilePathTarget; listID: string;
  inputRef: Ref<FilePathSuggestionsHandle>; onActive: (id: string | undefined) => void; onClose: () => void;
  onSelect: (target: FilePathTarget, path: string, file?: WorkspaceExplorerView) => void;
}) {
  const { t } = useLocale();
  const [selected, setSelected] = useState(0);
  const [error, setError] = useState("");
  const [reading, setReading] = useState(false);
  const request = useRef<AbortController | null>(null);
  const active = useRef<HTMLButtonElement>(null);
  const current = useRef(target);
  current.current = target;
  const directory = useQuery({ queryKey: ["workspace", workspaceID, "explore", target.parent],
    queryFn: ({ signal }) => client.workspaceExplore(workspaceID, target.parent, signal), retry: false });
  const entries = directory.data?.kind === "directory" ? directory.data.entries.filter((entry) =>
    entry.readable && entry.name.toLocaleLowerCase().startsWith(target.prefix.toLocaleLowerCase())) : [];
  const index = Math.min(selected, Math.max(0, entries.length - 1));
  useEffect(() => {
    setSelected(0); setError(""); setReading(false);
    return () => { request.current?.abort(); request.current = null; };
  }, [target, client, workspaceID]);
  useEffect(() => { onActive(entries.length ? `${listID}-${index}` : undefined); }, [listID, index, entries.length, onActive]);
  useEffect(() => { active.current?.scrollIntoView?.({ block: "nearest" }); }, [index, target, entries.length]);
  const select = async (entry: typeof entries[number]) => {
    if (reading) return;
    if (entry.kind === "directory") { onSelect(target, entry.path); return; }
    const controller = new AbortController();
    request.current = controller;
    setReading(true); setError("");
    let attaching = false;
    try {
      const file = await client.workspaceExplore(workspaceID, entry.path, controller.signal);
      if (controller.signal.aborted || current.current !== target) return;
      if (file.kind !== "file") throw new Error(t("文件已变化，请重新选择。", "The file changed. Choose it again."));
      attaching = true;
      onSelect(target, entry.path, file);
    } catch (reason) {
      if (!controller.signal.aborted && current.current === target) setError(attaching && reason instanceof Error ? reason.message :
        t("文件读取失败，输入已保留。请重新选择文件。", "Unable to read the file. Your input is kept. Choose the file again."));
    } finally {
      if (!controller.signal.aborted && current.current === target) setReading(false);
    }
  };
  useImperativeHandle(inputRef, () => ({ keyDown: (event) => {
    if (event.isComposing || event.keyCode === 229 || event.ctrlKey || event.metaKey || event.altKey || event.shiftKey) return false;
    if (!["ArrowUp", "ArrowDown", "Enter", "Tab", "Escape"].includes(event.key)) return false;
    if (event.key === "Tab" && !entries.length) { onClose(); return false; }
    event.preventDefault(); event.stopPropagation();
    if (event.key === "Escape") onClose();
    else if (event.key === "ArrowUp") setSelected(Math.max(0, index - 1));
    else if (event.key === "ArrowDown") setSelected(Math.max(0, Math.min(entries.length - 1, index + 1)));
    else if (entries[index]) void select(entries[index]);
    return true;
  } }));

  return <div className="v2-file-path-suggestions">
    <div className="v2-file-path-heading"><span>{t("项目路径", "Project paths")} · <code>{target.parent}</code></span>
      <button type="button" aria-label={t("关闭路径补全", "Close path suggestions")} onClick={onClose}>
        <X aria-hidden="true" size={14} /></button></div>
    <div id={listID} role="listbox" aria-label={t("项目路径候选", "Project path suggestions")} aria-busy={directory.isFetching || reading}>
      {entries.map((entry, item) => {
        const Icon = entry.kind === "directory" ? Folder : File;
        return <button type="button" role="option" tabIndex={-1} id={`${listID}-${item}`} key={entry.path}
          ref={index === item ? active : undefined}
          aria-selected={index === item} aria-disabled={reading} title={entry.path}
          onPointerDown={(event) => event.preventDefault()} onMouseDown={(event) => event.preventDefault()}
          onClick={() => void select(entry)}>
          <Icon aria-hidden="true" size={15} /><span>{entry.name}{entry.kind === "directory" ? "/" : ""}</span>
        </button>;
      })}
    </div>
    {error ? <p role="alert">{error}</p> : directory.isError ?
      <p role="alert">{t("无法列出此目录，输入已保留。", "Unable to list this directory. Your input is kept.")}</p> :
      reading ? <p role="status">{t("正在读取文件…", "Reading file…")}</p> :
        directory.isPending ? <p role="status">{t("正在列出目录…", "Listing directory…")}</p> :
          !entries.length ? <p role="status">{t("当前目录没有匹配项。", "No matches in this directory.")}</p> : null}
    {directory.data?.truncated && <p>{t("仅列出部分目录条目；可输入完整父目录路径。", "Only part of this directory is listed. You can type the full parent path.")}</p>}
    <p className="v2-file-path-help">{t("↑↓ 选择 · Enter / Tab 补全 · Esc 关闭", "↑↓ select · Enter / Tab complete · Esc close")}</p>
  </div>;
}
