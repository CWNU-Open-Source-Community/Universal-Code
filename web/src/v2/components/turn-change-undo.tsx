import { useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Loader2, Undo2, X } from "lucide-react";
import type { APIClient } from "../../api/client";
import type { ThreadChangeSummary } from "../../api/thread-changes";
import { useModalFocusTrap } from "../../hooks/use-modal-focus-trap";
import { useLocale } from "../../lib/locale";
import { v2QueryKeys } from "../query-keys";

export function TurnChangeUndo({ client, summary }: { client: APIClient; summary: ThreadChangeSummary }) {
  const { t } = useLocale();
  const { thread_id: threadID, run_id: runID, source_event_id: sourceEventID } = summary;
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const cancel = useRef<HTMLButtonElement>(null);
  const titleID = useId();
  const descriptionID = useId();
  const previewKey = ["thread", threadID, "turn-undo", sourceEventID];
  const preview = useQuery({ queryKey: previewKey,
    queryFn: ({ signal }) => client.undoThreadChanges(threadID, runID, sourceEventID, false, signal),
    enabled: open, retry: false, refetchOnWindowFocus: false });
  const undo = useMutation({
    mutationFn: () => client.undoThreadChanges(threadID, runID, sourceEventID, true),
    onSuccess: (result) => {
      queryClient.setQueryData(previewKey, result);
      queryClient.setQueryData<ThreadChangeSummary>(["thread", threadID, "turn-changes", sourceEventID],
        (current) => current ? { ...current, undo: result } : current);
      void queryClient.invalidateQueries({ queryKey: ["run", runID] });
      void queryClient.invalidateQueries({ queryKey: v2QueryKeys.thread(threadID) });
    },
  });
  const busy = undo.isPending;
  const dialog = useModalFocusTrap<HTMLElement>(open, () => setOpen(false), busy, cancel,
    { isolateBackground: true, returnFocusRef: trigger });
  const result = summary.undo?.state === "completed" ? summary.undo : preview.data ?? summary.undo;
  const completed = result?.state === "completed";
  const checking = preview.isFetching;
  const error = preview.isError || undo.isError;
  const canUndo = result?.state === "available" && !checking && !error && !busy;

  const reasonLabel = (reason: string) => {
    switch (reason) {
      case "files_changed": return t("文件在本轮之后发生了修改，请先检查当前内容。", "This file changed after this turn. Review its current content first.");
      case "run_busy": return t("任务正在执行，结束后才能撤销。", "Wait for the task to stop before undoing changes.");
      case "incomplete_history": return t("缺少完整的原始编辑记录，无法恢复。", "The original edit records are incomplete and cannot be restored.");
      case "permission_denied": return t("当前没有恢复文件的权限。", "File restore permission is unavailable.");
      case "workspace_changed": return t("工作目录已发生变化，请先检查当前目录。", "The working directory has changed. Check the current directory first.");
      case "restore_failed": return t("文件恢复未完成，请检查文件状态后重试。", "The file restore did not complete. Check the file state before retrying.");
      default: return t("当前状态无法安全恢复，请检查文件与任务状态后重试。", "Check the file and task state before retrying the restore.");
    }
  };

  return <>
    <button className="v2-turn-changes-undo" type="button" ref={trigger} disabled={busy} aria-disabled={completed || undefined}
      aria-label={completed ? t("本轮改动已撤销", "This turn’s changes were undone") : t("撤销本轮改动", "Undo this turn’s changes")}
      onClick={() => { if (!completed) { undo.reset(); setOpen(true); } }}>
      {completed ? <Check size={15} aria-hidden="true" /> : <Undo2 size={15} aria-hidden="true" />}
      {completed ? t("已撤销", "Undone") : t("撤销", "Undo")}
    </button>
    {open && createPortal(<div className="v2-change-overlay" role="presentation" onMouseDown={(event) => {
      if (event.target === event.currentTarget && !busy) setOpen(false);
    }}>
      <section className="v2-dialog v2-turn-undo-dialog" role="dialog" aria-modal="true"
        aria-labelledby={titleID} aria-describedby={descriptionID} tabIndex={-1} ref={dialog}>
        <header><span><Undo2 size={17} aria-hidden="true" /></span>
          <h2 id={titleID}>{completed ? t("本轮改动已撤销", "This turn’s changes were undone") : t("撤销本轮改动", "Undo this turn’s changes")}</h2>
          <button type="button" aria-label={t("关闭", "Close")} disabled={busy} onClick={() => setOpen(false)}><X size={17} aria-hidden="true" /></button>
        </header>
        <div className="v2-turn-undo-body">
          <p id={descriptionID}>{completed
            ? t("已恢复本轮修改前的文件内容。", "The files have been restored to their state before this turn.")
            : t(`将恢复这轮对 ${summary.file_count} 个文件的修改。`, `Undo this turn’s changes to ${summary.file_count} ${summary.file_count === 1 ? "file" : "files"}.`)}</p>
          {(checking || busy) && <p className="v2-turn-undo-status" role="status"><Loader2 size={15} className="v2-turn-undo-spinner" aria-hidden="true" />
            {busy ? t("正在撤销…", "Undoing changes…") : t("正在检查文件状态…", "Checking file state…")}</p>}
          {!checking && result?.state === "available" && !busy && <p className="v2-turn-undo-note">
            {t("确认前已检查当前文件；如有后续修改，将停止并显示冲突。", "Current files have been checked. Later edits will stop the restore and be shown as conflicts.")}</p>}
          {!checking && result?.state === "partial" && <p role="status">{t("部分文件已恢复，其余文件尚未撤销。", "Some files were restored. The remaining files have not been undone.")}</p>}
          {!checking && result?.state === "applying" && !busy && <p role="status">{t("上次撤销尚未确认完成，请重新检查结果。", "The previous undo has not completed. Check its result again.")}</p>}
          {error && <p className="v2-turn-undo-error" role="alert">{undo.isError
            ? t("暂时无法确认撤销结果。请重新检查后继续。", "The undo result could not be confirmed. Check again before continuing.")
            : t("无法检查撤销状态，请重试。", "The undo state could not be checked. Try again.")}</p>}
          {!checking && result && result.conflicts.length > 0 && <ul className="v2-turn-undo-conflicts" aria-label={t("无法撤销的原因", "Reasons undo is unavailable")}>
            {result.conflicts.map((conflict, index) => <li key={index}>
              {conflict.path && <code>{conflict.path}</code>}<span>{reasonLabel(conflict.reason)}</span>
            </li>)}
          </ul>}
          {!completed && result && result.restored_paths.length > 0 && <details className="v2-turn-undo-paths">
            <summary>{t(`已恢复 ${result.restored_paths.length} 个路径`, `${result.restored_paths.length} ${result.restored_paths.length === 1 ? "path" : "paths"} restored`)}</summary>
            <ul>{result.restored_paths.map((path) => <li key={path}><code>{path}</code></li>)}</ul>
          </details>}
          {!completed && result && result.restored_paths.length > 0 && result.remaining_paths.length > 0 && <details className="v2-turn-undo-paths">
            <summary>{t(`尚未恢复 ${result.remaining_paths.length} 个路径`, `${result.remaining_paths.length} ${result.remaining_paths.length === 1 ? "path" : "paths"} remaining`)}</summary>
            <ul>{result.remaining_paths.map((path) => <li key={path}><code>{path}</code></li>)}</ul>
          </details>}
        </div>
        <footer><button type="button" ref={cancel} disabled={busy} onClick={() => setOpen(false)}>{completed ? t("关闭", "Close") : t("取消", "Cancel")}</button>
          {!completed && (canUndo || busy ? <button type="button" className="danger" disabled={!canUndo} onClick={() => undo.mutate()}>
            {busy ? t("正在撤销…", "Undoing…") : t("确认撤销", "Confirm undo")}</button>
            : <button type="button" disabled={checking} onClick={() => { undo.reset(); void preview.refetch(); }}>{t("重新检查", "Check again")}</button>)}
        </footer>
      </section>
    </div>, document.body)}
  </>;
}
