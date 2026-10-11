import { useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { FolderOpen, LoaderCircle, X } from "lucide-react";
import { APIRequestError, type APIClient } from "../../api/client";
import type { WorkspaceView } from "../../api/types";
import { useModalFocusTrap } from "../../hooks/use-modal-focus-trap";
import { desktopWorkspaceImportEnabled, importDesktopWorkspace } from "../../lib/desktop-bridge";
import { v2QueryKeys } from "../query-keys";

export function V2WorkspaceStart({ client, onSelect }: {
  client: APIClient; onSelect: (workspace: WorkspaceView) => void;
}) {
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [directory, setDirectory] = useState("");
  const titleID = useId();
  const descriptionID = useId();
  const trigger = useRef<HTMLButtonElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const close = useRef<HTMLButtonElement>(null);
  const native = desktopWorkspaceImportEnabled();
  const importing = useMutation({
    mutationFn: (path: string | undefined) => native ? importDesktopWorkspace()
      : client.importWorkspace(path!).then((result) => result.workspace),
    onSuccess: async (workspace) => {
      if (!workspace) return;
      onSelect(workspace);
      setOpen(false);
      setDirectory("");
      await queryClient.invalidateQueries({ queryKey: v2QueryKeys.workspaces });
    },
  });
  const onClose = () => { if (!importing.isPending) setOpen(false); };
  const dialog = useModalFocusTrap<HTMLElement>(open, onClose, importing.isPending,
    client.hasWorkspaceImport ? input : close, { isolateBackground: true, returnFocusRef: trigger });
  useEffect(() => {
    if (!open) return;
    if (importing.isPending) dialog.current?.focus();
    else if (importing.isError) input.current?.focus();
  }, [open, importing.isPending, importing.isError, dialog]);
  const failure = importing.error instanceof APIRequestError
    ? importing.error.code === "INVALID_ARGUMENT"
      ? "目录未能接入。请确认输入的是服务电脑上已存在的文件夹完整路径。"
      : importing.error.code === "POLICY_DENIED" || importing.error.code === "UNAUTHENTICATED" || importing.error.code === "NOT_FOUND"
        ? "此连接不能接入目录。请检查服务是否启用了目录导入，并使用控制令牌重新连接。"
        : "目录接入结果尚待确认。请使用相同路径重试，已有项目会保留。"
    : "目录未能接入，请检查连接后重试。输入的路径和任务草稿已保留。";
  return <div className="v2-workspace-start">
    <button className="v2-composer-chip" disabled={importing.isPending}
      onClick={() => { importing.reset(); if (native) importing.mutate(undefined); else setOpen(true); }}
      ref={trigger} type="button">
      {importing.isPending ? <LoaderCircle aria-hidden="true" className="spin" size={15} />
        : <FolderOpen aria-hidden="true" size={15} />}{native ? "打开项目" : "接入项目"}
    </button>
    {native && importing.isError && <p role="alert">未能打开项目，请重试选择文件夹。现有输入已保留。</p>}
    {open && createPortal(<div className="v2-overlay" role="presentation" onMouseDown={(event) => {
      if (event.target === event.currentTarget) onClose();
    }}><section aria-labelledby={titleID} aria-describedby={descriptionID} aria-modal="true"
      className="v2-dialog v2-project-import" ref={dialog} role="dialog" tabIndex={-1}>
      <header><FolderOpen aria-hidden="true" size={18} /><h2 id={titleID}>接入已有项目</h2>
        <button aria-label="关闭项目接入" disabled={importing.isPending} onClick={onClose} ref={close}
          type="button"><X aria-hidden="true" size={17} /></button></header>
      <form onSubmit={(event) => { event.preventDefault(); if (client.hasWorkspaceImport && directory.trim() && !importing.isPending)
        importing.mutate(directory.trim()); }}>
        <div className="v2-project-import-body">
          <p id={descriptionID}>输入运行 Universal-Code 服务的电脑上的文件夹完整路径。接入后可在这个目录中开始任务，文件保留在原位置，任务按你选择的权限执行。</p>
          {client.hasWorkspaceImport ? <label>项目文件夹路径<input autoComplete="off" disabled={importing.isPending}
            maxLength={4096} onChange={(event) => setDirectory(event.target.value)} placeholder="D:\Projects\my-project 或 /home/me/my-project"
            ref={input} required spellCheck={false} type="text" value={directory} /></label>
            : <div className="v2-notice" role="status">此连接尚未启用目录导入。在服务的启动命令中加入
              <code> --enable-workspace-import</code>，并使用控制令牌重新连接。桌面应用可直接通过「打开项目」选择文件夹。
            </div>}
          {importing.isError && <p className="v2-inline-error" role="alert">{failure}</p>}
        </div>
        <footer><button disabled={importing.isPending} onClick={onClose} type="button">取消</button>
          {client.hasWorkspaceImport && <button className="primary" disabled={!directory.trim() || importing.isPending}
            type="submit">{importing.isPending ? "正在接入…" : "接入此目录"}</button>}</footer>
      </form>
    </section></div>, document.body)}
  </div>;
}
