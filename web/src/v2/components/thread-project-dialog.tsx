import { useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { FolderInput, X } from "lucide-react";
import type { ThreadView, WorkspaceView } from "../../api/types";
import { useModalFocusTrap } from "../../hooks/use-modal-focus-trap";
import { useLocale } from "../../lib/locale";

export const threadProjectID = (thread: ThreadView) => thread.project_workspace_id ?? thread.workspace_id ?? "";

export function ThreadProjectDialog({ thread, workspaces, onClose, onMove }: {
  thread: ThreadView; workspaces: WorkspaceView[];
  onClose: () => void; onMove: (thread: ThreadView, projectID: string) => void;
}) {
  const { t } = useLocale();
  const [projectID, setProjectID] = useState(threadProjectID(thread));
  const titleID = useId();
  const selectRef = useRef<HTMLSelectElement>(null);
  const dialog = useModalFocusTrap<HTMLElement>(true, onClose, false, selectRef, { isolateBackground: true });
  return createPortal(<div className="v2-overlay" role="presentation" onMouseDown={(event) => {
    if (event.target === event.currentTarget) onClose();
  }}><section aria-labelledby={titleID} aria-modal="true" className="v2-dialog v2-thread-project-dialog" ref={dialog} role="dialog" tabIndex={-1}>
    <header><FolderInput aria-hidden="true" size={18} /><h2 id={titleID}>{t("移动到项目", "Move to project")}</h2>
      <button aria-label={t("关闭项目选择", "Close project picker")} onClick={onClose} type="button"><X aria-hidden="true" size={17} /></button></header>
    <form onSubmit={(event) => { event.preventDefault(); if (projectID !== threadProjectID(thread)) { onMove(thread, projectID); onClose(); } }}>
      <div className="v2-thread-project-dialog-body"><strong>{thread.title}</strong>
        <label>{t("目标项目", "Destination project")}<select ref={selectRef} value={projectID} onChange={(event) => setProjectID(event.target.value)}>
          <option value="">{t("本地聊天", "Local chats")}</option>
          {workspaces.map((workspace) => <option key={workspace.id} value={workspace.id}>{workspace.name}</option>)}
        </select></label>
        <p>{projectID ? t("后续消息将在目标项目中执行，已有文件保留在原位置。", "Future messages run in the destination project. Existing files stay where they are.")
          : t("移出项目后保留当前工作目录。", "The chat keeps its current working folder after leaving the project.")}</p>
      </div><footer><button onClick={onClose} type="button">{t("取消", "Cancel")}</button>
        <button className="primary" disabled={projectID === threadProjectID(thread)} type="submit">{t("移动", "Move")}</button></footer>
    </form>
  </section></div>, document.body);
}
