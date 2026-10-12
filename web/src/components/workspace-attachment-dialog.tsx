import { FolderOpen, X } from "lucide-react";
import type { APIClient } from "../api/client";
import { WorkspaceExplorer } from "./workspace-explorer";
import { useFileEditLeave } from "./file-edit-drafts";
import { useModalFocusTrap } from "../hooks/use-modal-focus-trap";

export function WorkspaceAttachmentDialog({ client, open, onClose: close, runID, workspaceID }: {
  client: APIClient;
  open: boolean;
  onClose: () => void;
  runID: string;
  workspaceID: string;
}) {
  const leave = useFileEditLeave();
  const onClose = () => leave(close);
  const dialogRef = useModalFocusTrap<HTMLElement>(open, onClose);
  if (!open) return null;
  return <div className="desktop-dialog-backdrop" role="presentation">
    <section aria-labelledby="workspace-attachment-title" aria-modal="true"
      className="desktop-dialog workspace-attachment-dialog" ref={dialogRef}
      role="dialog" tabIndex={-1}>
      <header>
        <div><span className="dialog-icon"><FolderOpen aria-hidden="true" size={17} /></span>
          <div><h2 id="workspace-attachment-title">文件和文件夹</h2>
            <small>工作区证据浏览器</small></div></div>
        <button aria-label="关闭" className="icon-button" onClick={onClose}
          title="关闭" type="button"><X aria-hidden="true" size={16} /></button>
      </header>
      <div className="workspace-attachment-body">
        <WorkspaceExplorer client={client} runID={runID} workspaceID={workspaceID} />
      </div>
    </section>
  </div>;
}
